<#
.SYNOPSIS
  Tira o routing do ar e confirma que a corrida ainda é atribuída com haversine-fallback.
  Sobe o routing de novo e confirma que a corrida seguinte volta ao Dijkstra.

.DESCRIPTION
  Abre o painel http://localhost:30300/d/resilience e percorre, sozinho:

  1. Corrida com o routing pronto: eta_engine dijkstra-one-to-many.
  2. Apaga o HPA do routing e escala o deployment para 0.
  3. Corrida com zero pods de routing: eta_engine haversine-fallback.
  4. Recria o HPA, espera 2 pods prontos.
  5. Corrida de novo no grafo: eta_engine dijkstra-one-to-many.

  Se o script parar no meio da falha, o finally recria o HPA. Os heartbeats
  dos três motoristas atribuídos seguem por -HoldSeconds para essas corridas
  não virarem rematch no painel.

.EXAMPLE
  .\deploy\k8s\failure.ps1
  .\deploy\k8s\failure.ps1 -HoldSeconds 600
#>
param(
    [ValidateRange(10, 3600)][int]$HoldSeconds = 600
)

$ErrorActionPreference = 'Stop'
$utf8 = [System.Text.UTF8Encoding]::new($false)
$OutputEncoding = $utf8
[Console]::OutputEncoding = $utf8

$Namespace = 'nearby-drivers'
$RidePort = 18082
$DriverPort = 18083
$Dashboard = 'http://localhost:30300/d/resilience'
$EngineDijkstra = 'dijkstra-one-to-many'
$EngineHaversine = 'haversine-fallback'
$Pickup = @{ lat = -23.5613; lng = -46.6565 }
$Spots = @(
    @{ lat = -23.5613; lng = -46.6580 },
    @{ lat = -23.5590; lng = -46.6580 },
    @{ lat = -23.5601; lng = -46.6588 }
)
$Root = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$script:Hold = New-Object System.Collections.Generic.List[object]
$script:RoutingDown = $false

function Invoke-Native {
    param([Parameter(Mandatory)][scriptblock]$Command)
    $ErrorActionPreference = 'Continue'
    & $Command
    if ($LASTEXITCODE -ne 0) {
        throw "falhou (exit $LASTEXITCODE): $Command"
    }
}

function Test-Native {
    param([Parameter(Mandatory)][scriptblock]$Command)
    $ErrorActionPreference = 'Continue'
    & $Command *> $null
    return $LASTEXITCODE -eq 0
}

function Invoke-Api {
    param(
        [Parameter(Mandatory)][string]$Method,
        [Parameter(Mandatory)][string]$Url,
        $Body,
        [hashtable]$Headers = @{}
    )
    $params = @{ Method = $Method; Uri = $Url; Headers = $Headers; UseBasicParsing = $true; TimeoutSec = 15 }
    if ($null -ne $Body) {
        $params.ContentType = 'application/json'
        $params.Body = $utf8.GetBytes(($Body | ConvertTo-Json -Compress -Depth 5))
    }
    try {
        $resp = Invoke-WebRequest @params
        $data = $null
        if ($resp.Content) { $data = $resp.Content | ConvertFrom-Json }
        return [pscustomobject]@{ Status = [int]$resp.StatusCode; Data = $data }
    } catch [System.Net.WebException] {
        if ($null -eq $_.Exception.Response) { throw }
        $reader = [System.IO.StreamReader]::new($_.Exception.Response.GetResponseStream())
        return [pscustomobject]@{ Status = [int]$_.Exception.Response.StatusCode; Data = $reader.ReadToEnd() }
    }
}

function Start-Forward {
    param([string]$Service, [int]$Port)
    $proc = Start-Process kubectl -ArgumentList 'port-forward', '-n', $Namespace, "svc/$Service", "${Port}:8080" -WindowStyle Hidden -PassThru
    $deadline = (Get-Date).AddSeconds(20)
    while ((Get-Date) -lt $deadline) {
        try {
            if ((Invoke-WebRequest -Uri "http://localhost:$Port/readyz" -UseBasicParsing -TimeoutSec 2).StatusCode -eq 200) {
                return $proc
            }
        } catch {
            Start-Sleep -Milliseconds 500
        }
    }
    Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
    throw "$Service não respondeu /readyz em localhost:$Port. Rode .\deploy\k8s\up.ps1 antes."
}

function Send-Heartbeat {
    param([string]$DriverID, [hashtable]$Spot)
    $fix = @{
        lat         = $Spot.lat
        lng         = $Spot.lng
        seq         = [DateTimeOffset]::UtcNow.ToUnixTimeMilliseconds()
        observed_at = [DateTime]::UtcNow.ToString('yyyy-MM-ddTHH:mm:ss.fffZ')
        accuracy_m  = 5
    }
    $r = Invoke-Api -Method PUT -Url "http://localhost:$DriverPort/drivers/$DriverID/location" -Body $fix
    if ($r.Status -ne 204) { throw "PUT location $DriverID -> $($r.Status) $($r.Data)" }
}

function Send-Hold {
    foreach ($d in $script:Hold) { Send-Heartbeat -DriverID $d.ID -Spot $d.Spot }
}

function Get-RoutingReady {
    $ErrorActionPreference = 'Continue'
    $raw = kubectl get deployment routing -n $Namespace -o jsonpath='{.status.readyReplicas}' 2>$null
    if ($LASTEXITCODE -ne 0) { throw 'deployment routing não encontrado. Rode .\deploy\k8s\up.ps1 antes.' }
    if ([string]::IsNullOrWhiteSpace("$raw")) { return 0 }
    return [int]"$raw"
}

function Test-RoutingServing {
    $ErrorActionPreference = 'Continue'
    $raw = kubectl get endpoints routing -n $Namespace -o json 2>$null
    if ($LASTEXITCODE -ne 0) { throw 'service routing não encontrado' }
    $ips = @((($raw | ConvertFrom-Json).subsets.addresses.ip) | Where-Object { $_ })
    return $ips.Count -gt 0
}

function Test-RoutingHPA {
    return Test-Native { kubectl get hpa routing -n $Namespace }
}

function Get-RoutingHPAManifest {
    $path = Join-Path $PSScriptRoot 'routing.yaml'
    $text = [System.IO.File]::ReadAllText($path)
    $docs = [regex]::Split($text, "(?m)^---\s*$")
    $hpa = $docs | Where-Object { $_ -match '(?m)^kind:\s*HorizontalPodAutoscaler\s*$' } | Select-Object -First 1
    if (-not $hpa) { throw 'não achei o HPA em deploy/k8s/routing.yaml' }
    if ($hpa -notmatch '(?m)^  namespace:') {
        $hpa = [regex]::Replace($hpa, '(?m)^metadata:\r?\n', "metadata:`n  namespace: $Namespace`n", 1)
    }
    return $hpa.Trim() + "`n"
}

function Restore-Routing {
    Write-Host 'Recriando o HPA do routing e voltando para 2 réplicas.'
    $file = New-TemporaryFile
    try {
        [System.IO.File]::WriteAllText($file.FullName, (Get-RoutingHPAManifest), $utf8)
        Invoke-Native { kubectl apply -f $file.FullName }
    } finally {
        Remove-Item $file.FullName -Force -ErrorAction SilentlyContinue
    }
    Invoke-Native { kubectl scale deployment/routing -n $Namespace --replicas=2 }
}

function Stop-Routing {
    Write-Host 'Removendo o HPA para o autoscaler não recriar os pods.'
    Invoke-Native { kubectl delete hpa routing -n $Namespace --ignore-not-found --wait=true }
    $script:RoutingDown = $true
    Write-Host 'Escalando o routing para 0.'
    Invoke-Native { kubectl scale deployment/routing -n $Namespace --replicas=0 }
}

function Wait-Routing {
    param([Parameter(Mandatory)][int]$Ready, [int]$Seconds = 180)
    $deadline = (Get-Date).AddSeconds($Seconds)
    $last = ''
    while ((Get-Date) -lt $deadline) {
        if ($script:Hold.Count -gt 0) { Send-Hold }
        $n = Get-RoutingReady
        $hpa = Test-RoutingHPA
        $serving = Test-RoutingServing
        $line = "routing pronto $n  hpa $(if ($hpa) { 'sim' } else { 'nao' })  endpoints $(if ($serving) { 'sim' } else { 'nao' })"
        if ($last -ne $line) {
            Write-Host ("{0:HH:mm:ss}  {1}" -f (Get-Date), $line)
            $last = $line
        }
        if ($Ready -eq 0 -and $n -eq 0 -and -not $serving) { return }
        if ($Ready -gt 0 -and $n -ge $Ready -and $hpa -and $serving) { return }
        Start-Sleep -Seconds 2
    }
    throw "routing não chegou a $Ready pods prontos em ${Seconds}s. Último: $last"
}

function Test-MatchingScrape {
    $ErrorActionPreference = 'Continue'
    $raw = kubectl get deployment prometheus -n $Namespace -o json 2>$null
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace("$raw")) { return $false }
    $name = @((($raw | ConvertFrom-Json).spec.template.spec.volumes) | Where-Object { $_.name -eq 'config' } | ForEach-Object { $_.configMap.name }) | Select-Object -First 1
    if ([string]::IsNullOrWhiteSpace("$name")) { return $false }
    $raw = kubectl get configmap $name -n $Namespace -o json 2>$null
    if ($LASTEXITCODE -ne 0) { return $false }
    $yml = ($raw | ConvertFrom-Json).data.'prometheus.yml'
    return "$yml" -match 'job_name: matching'
}

function Sync-Dashboard {
    $pair = [Convert]::ToBase64String([Text.Encoding]::ASCII.GetBytes('admin:admin'))
    $dashboard = $false
    try {
        $resp = Invoke-WebRequest -Uri 'http://localhost:30300/api/dashboards/uid/resilience' -Headers @{ Authorization = "Basic $pair" } -UseBasicParsing -TimeoutSec 5
        $dashboard = $resp.StatusCode -eq 200
    } catch {
        $dashboard = $false
    }
    if ($dashboard -and (Test-MatchingScrape)) { return }

    Write-Host 'Publicando o dashboard e o scrape do matching.'
    Invoke-Native { kubectl delete job kafka-init -n $Namespace --ignore-not-found --wait=true }
    $manifest = New-TemporaryFile
    try {
        $yaml = Invoke-Native { kubectl kustomize --load-restrictor LoadRestrictionsNone (Join-Path $Root 'deploy/k8s') }
        [System.IO.File]::WriteAllText($manifest.FullName, ($yaml -join "`n"), $utf8)
        Invoke-Native { kubectl apply -f $manifest.FullName }
    } finally {
        Remove-Item $manifest.FullName -Force -ErrorAction SilentlyContinue
    }
    Invoke-Native { kubectl rollout status deployment/grafana -n $Namespace --timeout=180s }
    Invoke-Native { kubectl rollout status deployment/prometheus -n $Namespace --timeout=180s }
}

function New-DemoDriver {
    param([Parameter(Mandatory)][string]$Role, [Parameter(Mandatory)][int]$Index)
    $spot = $Spots[($Index - 1) % $Spots.Count]
    $suffix = ([guid]::NewGuid().ToString('N')).Substring(0, 6).ToUpperInvariant()
    $reg = @{
        name          = "Falha $Role"
        phone         = ('+55119{0}' -f $suffix)
        vehicle_class = 'standard'
        plate         = ('F{0}' -f $suffix)
    }
    $r = Invoke-Api -Method POST -Url "http://localhost:$DriverPort/drivers" -Body $reg -Headers @{ 'Idempotency-Key' = [guid]::NewGuid().ToString() }
    if ($r.Status -ne 201 -and $r.Status -ne 200) { throw "POST /drivers $Role -> $($r.Status) $($r.Data)" }
    $id = $r.Data.id
    $null = Invoke-Api -Method PATCH -Url "http://localhost:$DriverPort/drivers/$id/status" -Body @{ status = 'AVAILABLE' }
    $driver = [pscustomobject]@{ Role = $Role; ID = $id; Spot = $spot }
    $script:Hold.Add($driver)
    Send-Heartbeat -DriverID $id -Spot $spot
    Write-Host ("motorista {0}  {1}" -f $Role, $id)
    return $driver
}

function New-Ride {
    param([Parameter(Mandatory)][string]$Label)
    $passenger = [guid]::NewGuid().ToString()
    $body = @{ passenger_id = $passenger; pickup = $Pickup; vehicle_class = 'standard' }
    $r = Invoke-Api -Method POST -Url "http://localhost:$RidePort/rides" -Body $body -Headers @{ 'Idempotency-Key' = [guid]::NewGuid().ToString() }
    if ($r.Status -ne 202) { throw "POST /rides -> $($r.Status) $($r.Data)" }
    $ride = [pscustomobject]@{ Label = $Label; ID = $r.Data.id }
    Write-Host ("corrida {0}  {1}" -f $Label, $ride.ID)
    return $ride
}

function Stop-Ride {
    param($Ride)
    $cancel = Invoke-Api -Method POST -Url "http://localhost:$RidePort/rides/$($Ride.ID)/cancel" -Body @{ reason = 'failure-script' }
    if ($cancel.Status -ne 200) { throw "cancel $($Ride.ID) -> $($cancel.Status) $($cancel.Data)" }
    $deadline = (Get-Date).AddSeconds(30)
    while ((Get-Date) -lt $deadline) {
        Send-Hold
        $r = Invoke-Api -Method GET -Url "http://localhost:$RidePort/rides/$($Ride.ID)"
        if ($r.Data.status -eq 'CANCELLED') { return }
        Start-Sleep -Seconds 2
    }
    throw "$($Ride.Label) não cancelou"
}

function Wait-Assigned {
    param([Parameter(Mandatory)]$Ride, [int]$Seconds = 90)
    $deadline = (Get-Date).AddSeconds($Seconds)
    $last = ''
    while ((Get-Date) -lt $deadline) {
        Send-Hold
        $r = Invoke-Api -Method GET -Url "http://localhost:$RidePort/rides/$($Ride.ID)"
        if ($null -eq $r.Data -or $r.Data -is [string]) { throw "GET $($Ride.ID) -> $($r.Status) $($r.Data)" }
        $data = $r.Data
        $line = [string]$data.status
        if ($data.driver) { $line = "$line  $($data.driver.eta_engine)  eta $($data.driver.eta_s)s  $($data.driver.distance_m)m" }
        if ($last -ne $line) {
            Write-Host ("{0:HH:mm:ss}  {1}  {2}" -f (Get-Date), $Ride.Label, $line)
            $last = $line
        }
        if ($data.status -eq 'DRIVER_ASSIGNED') { return $data }
        if ($data.status -eq 'NO_DRIVER_AVAILABLE' -or $data.status -eq 'CANCELLED') {
            throw "$($Ride.Label) terminou em $($data.status)"
        }
        Start-Sleep -Seconds 2
    }
    throw "$($Ride.Label) ($($Ride.ID)) não chegou a DRIVER_ASSIGNED. Último: $last"
}

function Request-Engine {
    param(
        [Parameter(Mandatory)][string]$Label,
        [Parameter(Mandatory)][string]$Engine
    )
    $ride = New-Ride -Label $Label
    $data = Wait-Assigned -Ride $ride
    if ($data.driver.eta_engine -eq $Engine) {
        return [pscustomobject]@{ Label = $Label; ID = $ride.ID; Engine = $data.driver.eta_engine; ETAS = $data.driver.eta_s; DistanceM = $data.driver.distance_m }
    }
    # O breaker aberto ainda recusa o routing por 10s depois que os pods voltam.
    if ($Engine -eq $EngineDijkstra -and $data.driver.eta_engine -eq $EngineHaversine) {
        Write-Host 'Breaker ainda aberto. Cancelando esta corrida e tentando de novo em 12s.'
        Stop-Ride -Ride $ride
        $quiet = (Get-Date).AddSeconds(12)
        while ((Get-Date) -lt $quiet) { Send-Hold; Start-Sleep -Seconds 2 }
        $ride = New-Ride -Label $Label
        $data = Wait-Assigned -Ride $ride
        if ($data.driver.eta_engine -eq $Engine) {
            return [pscustomobject]@{ Label = $Label; ID = $ride.ID; Engine = $data.driver.eta_engine; ETAS = $data.driver.eta_s; DistanceM = $data.driver.distance_m }
        }
    }
    throw "$Label saiu com engine $($data.driver.eta_engine); esperava $Engine"
}

$ctx = (kubectl config current-context 2>$null)
if ($ctx -ne 'docker-desktop') {
    throw "kubectl aponta para '$ctx'. Rode: kubectl config use-context docker-desktop"
}
if (-not (Test-Native { kubectl get deployment ride -n $Namespace })) {
    throw 'ride não está no cluster. Rode .\deploy\k8s\up.ps1 antes.'
}

Sync-Dashboard
Write-Host ''
Write-Host "Painel: $Dashboard"
Write-Host ''

$forwards = @()
$results = @()
try {
    $forwards += Start-Forward -Service ride -Port $RidePort
    $forwards += Start-Forward -Service driver -Port $DriverPort

    if ((Get-RoutingReady) -lt 2 -or -not (Test-RoutingHPA)) {
        Write-Host 'Routing não está com 2 réplicas. Restaurando antes do teste.'
        Restore-Routing
        Wait-Routing -Ready 2
        $script:RoutingDown = $false
    }

    $null = New-DemoDriver -Role 'grafo' -Index 1
    $null = New-DemoDriver -Role 'fallback' -Index 2
    $null = New-DemoDriver -Role 'volta' -Index 3
    Send-Hold

    Write-Host ''
    Write-Host '== 1. routing no ar =='
    $results += Request-Engine -Label 'grafo' -Engine $EngineDijkstra

    Write-Host ''
    Write-Host '== 2. derrubando o routing =='
    Stop-Routing
    Wait-Routing -Ready 0

    Write-Host ''
    Write-Host '== 3. corrida com o routing fora =='
    $results += Request-Engine -Label 'fallback' -Engine $EngineHaversine

    Write-Host ''
    Write-Host '== 4. subindo o routing =='
    Restore-Routing
    Wait-Routing -Ready 2
    $script:RoutingDown = $false

    Write-Host ''
    Write-Host '== 5. corrida de novo no grafo =='
    $results += Request-Engine -Label 'volta' -Engine $EngineDijkstra

    Write-Host ''
    Write-Host 'Resultado'
    foreach ($row in $results) {
        Write-Host ("{0,-10} {1}  {2}  eta {3}s  {4}m" -f $row.Label, $row.ID, $row.Engine, $row.ETAS, $row.DistanceM)
    }
    Write-Host ''
    Write-Host "Painel: $Dashboard"
    Write-Host "Heartbeats seguem por ${HoldSeconds}s (Ctrl+C para parar antes)."

    $deadline = (Get-Date).AddSeconds($HoldSeconds)
    while ((Get-Date) -lt $deadline) {
        Send-Hold
        Start-Sleep -Seconds 5
    }
    Write-Host 'Heartbeats parados. Em ~30s as corridas atribuídas viram rematch.'
} finally {
    if ($script:RoutingDown) {
        Write-Host 'O teste parou com o routing fora. Restaurando.'
        try {
            Restore-Routing
            Wait-Routing -Ready 2 -Seconds 180
            Write-Host 'Routing pronto de novo.'
        } catch {
            Write-Host "Não consegui restaurar o routing: $($_.Exception.Message)"
            Write-Host 'Rode .\deploy\k8s\up.ps1 -SkipBuild'
        }
    }
    foreach ($p in $forwards) { Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue }
}
