<#
.SYNOPSIS
  Sobe ride, driver, matching e routing, com Prometheus e Grafana, no Kubernetes do Docker Desktop.

.EXAMPLE
  .\deploy\k8s\up.ps1              # build da imagem, metrics-server, apply e espera
  .\deploy\k8s\up.ps1 -SkipBuild   # reaplica sem rebuild da imagem
  .\deploy\k8s\up.ps1 -Load        # roda o Job do k6 contra o routing e acompanha o log
  .\deploy\k8s\up.ps1 -Rides       # 30 motoristas e 8 solicitantes
  .\deploy\k8s\up.ps1 -Rides -Drivers 50 -Passengers 12 -Hold 5m
  .\deploy\k8s\up.ps1 -Down        # remove o namespace nearby-drivers
#>
param(
    [switch]$SkipBuild,
    [switch]$Load,
    [switch]$Rides,
    [int]$Drivers = 30,
    [int]$Passengers = 8,
    [string]$Hold = '3m',
    [switch]$Down
)

$ErrorActionPreference = 'Stop'
$utf8 = [System.Text.UTF8Encoding]::new($false)
$OutputEncoding = $utf8
[Console]::OutputEncoding = $utf8

$Namespace = 'nearby-drivers'
$Services = @('ride-service', 'driver-service', 'matching-service', 'routing-service')
$MetricsServerURL = 'https://github.com/kubernetes-sigs/metrics-server/releases/download/v0.7.2/components.yaml'
$Root = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path

function Invoke-Native {
    param([Parameter(Mandatory)][scriptblock]$Command)
    $ErrorActionPreference = 'Continue'
    & $Command
    if ($LASTEXITCODE -ne 0) {
        throw "falhou (exit $LASTEXITCODE): $Command"
    }
}

# Test-Native roda um comando só para ver se ele dá certo, descartando a saída.
# No Windows PowerShell, stderr de comando nativo com ErrorActionPreference=Stop vira exceção.
function Test-Native {
    param([Parameter(Mandatory)][scriptblock]$Command)
    $ErrorActionPreference = 'Continue'
    & $Command *> $null
    return $LASTEXITCODE -eq 0
}

function Assert-Context {
    $ErrorActionPreference = 'Continue'
    $ctx = (kubectl config current-context 2>$null)
    if ($ctx -ne 'docker-desktop') {
        throw "kubectl aponta para '$ctx'. Ligue o Kubernetes no Docker Desktop e rode: kubectl config use-context docker-desktop"
    }
}

function Test-MetricsAPI {
    return Test-Native { kubectl top nodes }
}

function Initialize-MetricsServer {
    if (Test-MetricsAPI) {
        Write-Host 'metrics-server respondendo.'
        return
    }

    if (-not (Test-Native { kubectl get deployment metrics-server -n kube-system })) {
        Write-Host "Instalando metrics-server ($MetricsServerURL)."
        Invoke-Native { kubectl apply -f $MetricsServerURL }
    }

    # O kubelet do Docker Desktop usa certificado autoassinado.
    $argsJSON = kubectl get deployment metrics-server -n kube-system -o jsonpath='{.spec.template.spec.containers[0].args}'
    if ($argsJSON -notmatch 'kubelet-insecure-tls') {
        Write-Host 'Aplicando --kubelet-insecure-tls no metrics-server.'
        $patch = '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'
        $patchFile = New-TemporaryFile
        try {
            [System.IO.File]::WriteAllText($patchFile.FullName, $patch, $utf8)
            Invoke-Native { kubectl patch deployment metrics-server -n kube-system --type=json --patch-file $patchFile.FullName }
        } finally {
            Remove-Item $patchFile.FullName -Force
        }
    }
    Invoke-Native { kubectl rollout status deployment/metrics-server -n kube-system --timeout=180s }

    Write-Host 'Esperando a Metrics API (kubectl top)...'
    $deadline = (Get-Date).AddSeconds(120)
    while (-not (Test-MetricsAPI)) {
        if ((Get-Date) -gt $deadline) {
            throw 'Metrics API não respondeu em 120s. Veja: kubectl logs -n kube-system deploy/metrics-server'
        }
        Start-Sleep -Seconds 5
    }
    Write-Host 'metrics-server respondendo.'
}

function Start-Cluster {
    Initialize-MetricsServer

    if (-not $SkipBuild) {
        foreach ($svc in $Services) {
            $image = "nearby-drivers/${svc}:latest"
            Write-Host "Build de $image."
            Invoke-Native { docker build -f (Join-Path $Root "deploy/$svc/Dockerfile") -t $image $Root }
        }
    }

    $existed = Test-Native { kubectl get deployment routing -n $Namespace }

    Invoke-Native { kubectl delete job kafka-init -n $Namespace --ignore-not-found --wait=true }

    $manifest = New-TemporaryFile
    try {
        $yaml = Invoke-Native { kubectl kustomize --load-restrictor LoadRestrictionsNone (Join-Path $Root 'deploy/k8s') }
        [System.IO.File]::WriteAllText($manifest.FullName, ($yaml -join "`n"), $utf8)
        Invoke-Native { kubectl apply -f $manifest.FullName }
    } finally {
        Remove-Item $manifest.FullName -Force
    }

    # A tag latest não muda no rebuild: só um restart faz os pods pegarem a imagem nova.
    if ($existed -and -not $SkipBuild) {
        Invoke-Native { kubectl rollout restart deployment/ride deployment/driver deployment/matching deployment/routing -n $Namespace }
    }

    foreach ($d in 'postgres', 'redis', 'kafka', 'ride', 'driver', 'matching', 'routing', 'kube-state-metrics', 'prometheus', 'grafana') {
        Invoke-Native { kubectl rollout status "deployment/$d" -n $Namespace --timeout=300s }
    }

    Write-Host ''
    Write-Host 'Grafana: http://localhost:30300  (admin / admin; o dashboard abre como página inicial)'
    Write-Host 'Corridas: http://localhost:30300/d/rides-flow  |  gerar corridas: .\deploy\k8s\ride-demo.ps1'
    Write-Host 'Falha:   http://localhost:30300/d/resilience  |  .\deploy\k8s\failure.ps1'
    Write-Host 'Carga:   .\deploy\k8s\up.ps1 -Load'
    Write-Host 'Corridas k6: .\deploy\k8s\up.ps1 -Rides -Drivers 30 -Passengers 8'
    Write-Host "Acompanhe: kubectl get hpa -n $Namespace -w   |   kubectl get pods -n $Namespace -w"
}

function Set-JobEnv {
    param(
        [Parameter(Mandatory)][string]$Yaml,
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][string]$Value
    )
    # O valor default do Job já é o do parâmetro. Comparar o YAML inteiro
    # acusava "não achei" quando a troca não alterava o texto.
    $pattern = "(?m)(^[ \t]*-[ \t]*name: $([regex]::Escape($Name))\r?\n[ \t]+value:[ \t]*).*$"
    if (-not [regex]::IsMatch($Yaml, $pattern)) {
        throw "não achei env $Name no Job do k6"
    }
    $quoted = '"' + $Value + '"'
    return [regex]::Replace($Yaml, $pattern, { param($m) $m.Groups[1].Value + $quoted }.GetNewClosure())
}

function Start-Rides {
    if (-not (Test-Native { kubectl get deployment ride -n $Namespace })) {
        throw 'ride não está no cluster. Rode .\deploy\k8s\up.ps1 antes.'
    }
    if ($Drivers -lt 0) { throw 'Drivers tem que ser >= 0' }
    if ($Passengers -lt 1) { throw 'Passengers tem que ser >= 1' }

    Invoke-Native { kubectl delete job k6-rides -n $Namespace --ignore-not-found --wait=true }
    $ErrorActionPreference = 'Continue'
    $rendered = (kubectl kustomize (Join-Path $Root 'deploy/k6-rides')) -join "`n"
    if ($LASTEXITCODE -ne 0) { throw "kustomize de deploy/k6-rides falhou (exit $LASTEXITCODE)" }
    $ErrorActionPreference = 'Stop'
    $rendered = Set-JobEnv $rendered 'DRIVERS' $Drivers
    $rendered = Set-JobEnv $rendered 'PASSENGERS' $Passengers
    $rendered = Set-JobEnv $rendered 'HOLD' $Hold
    $manifest = Join-Path $env:TEMP 'k6-rides.yaml'
    [System.IO.File]::WriteAllText($manifest, $rendered, $utf8)
    Invoke-Native { kubectl apply -f $manifest }
    Invoke-Native { kubectl wait --for=condition=Ready pod -l job-name=k6-rides -n $Namespace --timeout=180s }
    Write-Host "$Drivers motoristas, $Passengers solicitantes, por $Hold."
    Write-Host 'Corridas: http://localhost:30300/d/rides-flow'
    Write-Host 'Cluster:  http://localhost:30300/d/routing-hpa'
    kubectl logs -f job/k6-rides -n $Namespace
}

function Start-Load {
    if (-not (Test-Native { kubectl get deployment routing -n $Namespace })) {
        throw 'routing não está no cluster. Rode .\deploy\k8s\up.ps1 antes.'
    }

    Invoke-Native { kubectl delete job k6-eta-batch -n $Namespace --ignore-not-found --wait=true }
    Invoke-Native { kubectl apply -k (Join-Path $Root 'deploy/k6') }
    Invoke-Native { kubectl wait --for=condition=Ready pod -l job-name=k6-eta-batch -n $Namespace --timeout=120s }
    Write-Host "k6 rodando (~5 min). Em outra janela: kubectl get hpa -n $Namespace -w"
    kubectl logs -f job/k6-eta-batch -n $Namespace
}

Assert-Context
if ($Down) {
    Invoke-Native { kubectl delete namespace $Namespace --ignore-not-found }
    Invoke-Native { kubectl delete clusterrole,clusterrolebinding nearby-drivers-prometheus nearby-drivers-kube-state-metrics --ignore-not-found }
} elseif ($Load) {
    Start-Load
} elseif ($Rides) {
    Start-Rides
} else {
    Start-Cluster
}
