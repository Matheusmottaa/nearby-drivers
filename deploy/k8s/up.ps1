<#
.SYNOPSIS
  Sobe o routing-service com HPA, Prometheus e Grafana no Kubernetes do Docker Desktop.

.EXAMPLE
  .\deploy\k8s\up.ps1              # build da imagem, metrics-server, apply e espera
  .\deploy\k8s\up.ps1 -SkipBuild   # reaplica sem rebuild da imagem
  .\deploy\k8s\up.ps1 -Load        # roda o Job do k6 e acompanha o log
  .\deploy\k8s\up.ps1 -Down        # remove o namespace nearby-drivers
#>
param(
    [switch]$SkipBuild,
    [switch]$Load,
    [switch]$Down
)

$ErrorActionPreference = 'Stop'
$utf8 = [System.Text.UTF8Encoding]::new($false)
$OutputEncoding = $utf8
[Console]::OutputEncoding = $utf8

$Namespace = 'nearby-drivers'
$Image = 'nearby-drivers/routing-service:latest'
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
        Write-Host "Build de $Image."
        Invoke-Native { docker build -f (Join-Path $Root 'deploy/routing-service/Dockerfile') -t $Image $Root }
    }

    $existed = Test-Native { kubectl get deployment routing -n $Namespace }

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
        Invoke-Native { kubectl rollout restart deployment/routing -n $Namespace }
    }

    foreach ($d in 'routing', 'kube-state-metrics', 'prometheus', 'grafana') {
        Invoke-Native { kubectl rollout status "deployment/$d" -n $Namespace --timeout=180s }
    }

    Write-Host ''
    Write-Host 'Grafana: http://localhost:30300  (admin / admin; o dashboard abre como página inicial)'
    Write-Host 'Carga:   .\deploy\k8s\up.ps1 -Load'
    Write-Host "Acompanhe: kubectl get hpa -n $Namespace -w   |   kubectl get pods -n $Namespace -w"
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
} else {
    Start-Cluster
}
