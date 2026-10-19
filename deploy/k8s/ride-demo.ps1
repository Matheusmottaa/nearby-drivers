<#
.SYNOPSIS
  Gera uma corrida de cada desfecho do painel "nearby-drivers - corridas".

.DESCRIPTION
  Abre port-forward para ride e driver e deixa no Grafana, ao mesmo tempo:

  - Sucesso: 1º do ranking
  - Sucesso: outro do ranking (o 1º já estava reservado)
  - Sucesso: rematch
  - Sem motorista: ranking esgotado
  - Sem motorista: ninguém por perto
  - Sem motorista: rematch sem candidato
  - Sem motorista: tentativas esgotadas
  - Cancelada antes do ranking
  - Cancelada com motorista

  As três primeiras saem juntas, com dois motoristas, para o ranking ainda listar
  quem a corrida vizinha já reservou. O rematch e as tentativas esgotadas esperam
  o lost (~30 s sem heartbeat). Os motoristas dos três sucessos continuam com
  heartbeat por -HoldSeconds, para essas linhas não virarem outro rematch.

.EXAMPLE
  .\deploy\k8s\ride-demo.ps1
  .\deploy\k8s\ride-demo.ps1 -HoldSeconds 600
#>
param(
    [ValidateRange(10, 3600)][int]$HoldSeconds = 600
)

$ErrorActionPreference = 'Stop'
$utf8 = [System.Text.UTF8Encoding]::new($false)
$OutputEncoding = $utf8
[Console]::OutputEncoding = $utf8

$Namespace = 'nearby-drivers'
$RidePort = 18080
$DriverPort = 18081
$Pickup = @{ lat = -23.5613; lng = -46.6565 }
# Fora do raio de 3 km: o matching publica match-failed e a corrida fica sem ranking.
$FarPickup = @{ lat = -22.9068; lng = -43.1729 }
# Nós do grafo em data/maps/graph.txt, do mais perto ao mais longe do embarque.
$Spots = @(
    @{ lat = -23.5613; lng = -46.6580 },
    @{ lat = -23.5590; lng = -46.6580 },
    @{ lat = -23.5601; lng = -46.6588 },
    @{ lat = -23.5630; lng = -46.6565 },
    @{ lat = -23.5640; lng = -46.6565 },
    @{ lat = -23.5613; lng = -46.6600 }
)
# Motoristas cujo heartbeat não pode parar: a corrida deles tem que continuar DRIVER_ASSIGNED.
$script:Hold = New-Object System.Collections.Generic.List[object]

function Invoke-Api {
    param(
        [Parameter(Mandatory)][string]$Method,
        [Parameter(Mandatory)][string]$Url,
        $Body,
        [hashtable]$Headers = @{}
    )
    $params = @{ Method = $Method; Uri = $Url; Headers = $Headers; UseBasicParsing = $true; TimeoutSec = 10 }
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

function New-Ride {
    param(
        [Parameter(Mandatory)][string]$Label,
        $PickupPoint = $Pickup
    )
    $passenger = [guid]::NewGuid().ToString()
    $body = @{ passenger_id = $passenger; pickup = $PickupPoint; vehicle_class = 'standard' }
    $r = Invoke-Api -Method POST -Url "http://localhost:$RidePort/rides" -Body $body -Headers @{ 'Idempotency-Key' = [guid]::NewGuid().ToString() }
    if ($r.Status -ne 202) { throw "POST /rides -> $($r.Status) $($r.Data)" }
    $ride = [pscustomobject]@{ Label = $Label; Passenger = $passenger; ID = $r.Data.id }
    Write-Host ("corrida {0}  {1}  solicitante {2}" -f $Label, $ride.ID, $passenger)
    return $ride
}

function Send-Hold {
    foreach ($d in $script:Hold) { Send-Heartbeat -DriverID $d.ID -Spot $d.Spot }
}

function Wait-Ride {
    param(
        [Parameter(Mandatory)]$Ride,
        [Parameter(Mandatory)][scriptblock]$Done,
        [int]$Seconds = 90,
        [object[]]$Also = @()
    )
    $deadline = (Get-Date).AddSeconds($Seconds)
    $last = ''
    $data = $null
    while ((Get-Date) -lt $deadline) {
        Send-Hold
        foreach ($d in $Also) { Send-Heartbeat -DriverID $d.ID -Spot $d.Spot }
        $r = Invoke-Api -Method GET -Url "http://localhost:$RidePort/rides/$($Ride.ID)"
        if ($null -eq $r.Data -or $r.Data -is [string]) { throw "GET $($Ride.ID) -> $($r.Status) $($r.Data)" }
        $data = $r.Data
        $line = [string]$data.status
        if ($data.driver) { $line = "$line  motorista $($data.driver.id)" }
        if ($last -ne $line) {
            Write-Host ("{0:HH:mm:ss}  {1}  {2}" -f (Get-Date), $Ride.Label, $line)
            $last = $line
        }
        if (& $Done $data) { return $data }
        Start-Sleep -Seconds 5
    }
    throw "$($Ride.Label) ($($Ride.ID)) não chegou a tempo. Último estado: $last"
}

function New-DemoDriver {
    param(
        [Parameter(Mandatory)][string]$Role,
        [Parameter(Mandatory)][int]$Index
    )
    $spot = $Spots[($Index - 1) % $Spots.Count]
    $reg = @{
        name          = "Demo $Role"
        phone         = ('+551190000{0:d4}' -f $Index)
        vehicle_class = 'standard'
        plate         = ('DEMO{0:d3}' -f $Index)
    }
    $r = Invoke-Api -Method POST -Url "http://localhost:$DriverPort/drivers" -Body $reg -Headers @{ 'Idempotency-Key' = "ride-demo-$Role" }
    if ($r.Status -ne 201 -and $r.Status -ne 200) { throw "POST /drivers $Role -> $($r.Status) $($r.Data)" }
    $id = $r.Data.id
    # ON_TRIP de uma demo anterior devolve 409: o motorista volta sozinho quando a corrida é liberada.
    $null = Invoke-Api -Method PATCH -Url "http://localhost:$DriverPort/drivers/$id/status" -Body @{ status = 'AVAILABLE' }
    $driver = [pscustomobject]@{ Role = $Role; ID = $id; Spot = $spot }
    Write-Host ([string]::Format([Globalization.CultureInfo]::InvariantCulture, 'motorista {0}  {1}  ({2}, {3})', $Role, $id, $spot.lat, $spot.lng))
    return $driver
}

$ctx = (kubectl config current-context 2>$null)
if ($ctx -ne 'docker-desktop') {
    throw "kubectl aponta para '$ctx'. Rode: kubectl config use-context docker-desktop"
}

$forwards = @()
try {
    $forwards += Start-Forward -Service ride -Port $RidePort
    $forwards += Start-Forward -Service driver -Port $DriverPort

    $first = New-DemoDriver -Role 'sucesso-1' -Index 101
    $second = New-DemoDriver -Role 'sucesso-2' -Index 102
    $released = New-DemoDriver -Role 'cancelar-depois' -Index 103
    $rematchOut = New-DemoDriver -Role 'rematch-sai' -Index 104
    $rematchIn = New-DemoDriver -Role 'rematch-fica' -Index 105
    $alone = New-DemoDriver -Role 'sozinho' -Index 106
    $lost1 = New-DemoDriver -Role 'esgota-1' -Index 107
    $lost2 = New-DemoDriver -Role 'esgota-2' -Index 108
    $lost3 = New-DemoDriver -Role 'esgota-3' -Index 109
    $script:Hold.Add($first)
    $script:Hold.Add($second)
    Send-Hold

    $created = @()
    $early = New-Ride -Label 'cancelada-antes'
    $cancel = Invoke-Api -Method POST -Url "http://localhost:$RidePort/rides/$($early.ID)/cancel" -Body @{ reason = 'ride-demo' }
    if ($cancel.Status -ne 200) { throw "cancel $($early.ID) -> $($cancel.Status) $($cancel.Data)" }
    $created += $early
    $null = Wait-Ride -Ride $early -Seconds 20 -Done { param($d) $d.status -eq 'CANCELLED' }
    # O cancelamento devolve o motorista ao nearby. Mais um ciclo e os dois voltam a aparecer na busca.
    Send-Hold
    Start-Sleep -Seconds 5
    Send-Hold

    # Juntas: o ranking da segunda ainda traz o motorista que a primeira já reservou.
    $created += New-Ride -Label 'sucesso-1o'
    $created += New-Ride -Label 'sucesso-outro'
    $created += New-Ride -Label 'ranking-esgotado'
    foreach ($ride in $created | Select-Object -Last 3) {
        $null = Wait-Ride -Ride $ride -Done { param($d) $d.status -eq 'DRIVER_ASSIGNED' -or $d.status -eq 'NO_DRIVER_AVAILABLE' }
    }

    $nobody = New-Ride -Label 'ninguem-por-perto' -PickupPoint $FarPickup
    $created += $nobody
    $null = Wait-Ride -Ride $nobody -Done { param($d) $d.status -eq 'NO_DRIVER_AVAILABLE' }

    $after = New-Ride -Label 'cancelada-com-motorista'
    $created += $after
    $null = Wait-Ride -Ride $after -Also @($released) -Done { param($d) $d.status -eq 'DRIVER_ASSIGNED' }
    $cancel = Invoke-Api -Method POST -Url "http://localhost:$RidePort/rides/$($after.ID)/cancel" -Body @{ reason = 'ride-demo' }
    if ($cancel.Status -ne 200) { throw "cancel $($after.ID) -> $($cancel.Status) $($cancel.Data)" }
    $null = Wait-Ride -Ride $after -Seconds 20 -Done { param($d) $d.status -eq 'CANCELLED' }

    Write-Host 'Esperando o motorista cancelado sair do nearby (~35s).'
    $quiet = (Get-Date).AddSeconds(35)
    while ((Get-Date) -lt $quiet) { Send-Hold; Start-Sleep -Seconds 5 }

    $again = New-Ride -Label 'sucesso-rematch'
    $created += $again
    $null = Wait-Ride -Ride $again -Also @($rematchOut) -Done { param($d) $d.status -eq 'DRIVER_ASSIGNED' -and $null -ne $d.driver -and $d.driver.id -eq $rematchOut.ID }
    $script:Hold.Add($rematchIn)
    $null = Wait-Ride -Ride $again -Done { param($d) $d.status -eq 'DRIVER_ASSIGNED' -and $null -ne $d.driver -and $d.driver.id -eq $rematchIn.ID }

    $empty = New-Ride -Label 'rematch-sem-candidato'
    $created += $empty
    $null = Wait-Ride -Ride $empty -Also @($alone) -Done { param($d) $d.status -eq 'DRIVER_ASSIGNED' -and $null -ne $d.driver -and $d.driver.id -eq $alone.ID }
    $null = Wait-Ride -Ride $empty -Done { param($d) $d.status -eq 'NO_DRIVER_AVAILABLE' }

    $spent = New-Ride -Label 'tentativas-esgotadas'
    $created += $spent
    $null = Wait-Ride -Ride $spent -Also @($lost1) -Done { param($d) $d.status -eq 'DRIVER_ASSIGNED' -and $null -ne $d.driver -and $d.driver.id -eq $lost1.ID }
    $null = Wait-Ride -Ride $spent -Also @($lost2) -Done { param($d) $d.status -eq 'DRIVER_ASSIGNED' -and $null -ne $d.driver -and $d.driver.id -eq $lost2.ID }
    $null = Wait-Ride -Ride $spent -Also @($lost3) -Done { param($d) $d.status -eq 'DRIVER_ASSIGNED' -and $null -ne $d.driver -and $d.driver.id -eq $lost3.ID }
    $null = Wait-Ride -Ride $spent -Done { param($d) $d.status -eq 'NO_DRIVER_AVAILABLE' }

    Write-Host ''
    Write-Host 'Painel: http://localhost:30300/d/rides-flow'
    Write-Host "Heartbeats dos sucessos seguem por ${HoldSeconds}s (Ctrl+C para parar antes)."

    $deadline = (Get-Date).AddSeconds($HoldSeconds)
    $last = @{}
    while ((Get-Date) -lt $deadline) {
        Send-Hold
        foreach ($ride in $created) {
            $r = Invoke-Api -Method GET -Url "http://localhost:$RidePort/rides/$($ride.ID)"
            $line = $r.Data.status
            if ($r.Data.driver) { $line = "$line  motorista $($r.Data.driver.id)" }
            if ($last[$ride.ID] -ne $line) {
                Write-Host ("{0:HH:mm:ss}  {1}  {2}" -f (Get-Date), $ride.Label, $line)
                $last[$ride.ID] = $line
            }
        }
        Start-Sleep -Seconds 5
    }
    Write-Host 'Heartbeats parados. Em ~30s os sucessos viram rematch.'
} finally {
    foreach ($p in $forwards) { Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue }
}
