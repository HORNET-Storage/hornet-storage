$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $PSScriptRoot
$bin = Join-Path $root 'bin'
$relayDir = Join-Path $root 'relay'
$airlockDir = Join-Path $root 'airlock'
$relayBinary = Join-Path $bin 'hornets-relay.exe'
$airlockBinary = Join-Path $bin 'airlock.exe'
$sidecarBinary = Join-Path $bin 'hornets-hyperswarm.exe'
$prebuilds = Join-Path $bin 'prebuilds'
$relayPanel = Join-Path (Join-Path $relayDir 'web') 'index.html'
$setupMarker = Join-Path (Join-Path $relayDir 'data') '.hornets_setup_complete'

foreach ($required in @($relayBinary, $airlockBinary, $sidecarBinary)) {
    if (-not (Test-Path -LiteralPath $required -PathType Leaf)) {
        throw "Missing component: $required"
    }
}
if (-not (Test-Path -LiteralPath $prebuilds -PathType Container)) {
    throw "Missing hyperswarm native prebuilds: $prebuilds"
}
if (-not (Test-Path -LiteralPath $relayPanel -PathType Leaf)) {
    throw "Missing relay web panel: $relayPanel"
}

New-Item -ItemType Directory -Force -Path $relayDir, $airlockDir | Out-Null
$env:AIRLOCK_CONFIG_PATH = Join-Path $airlockDir 'config.yaml'

$relay = $null
$airlock = $null
$exitCode = 0
try {
    $relay = Start-Process -FilePath $relayBinary -WorkingDirectory $relayDir -ArgumentList @('--bootstrap-setup', '--setup-profile', 'operator', '--setup-host', '127.0.0.1', '--setup-port', '11012') -NoNewWindow -PassThru
    Write-Host 'HORNETS relay is starting in operator setup mode.'
    Write-Host 'On the first run, open http://127.0.0.1:11012, review the public relay defaults, and apply setup.'
    Write-Host 'Relay identity, Airlock identity, and the shared sidecar are handled automatically; keep this window open.'

    while (-not (Test-Path -LiteralPath $setupMarker -PathType Leaf) -or -not (Test-Path -LiteralPath $env:AIRLOCK_CONFIG_PATH -PathType Leaf)) {
        if ($relay.HasExited) {
            throw "Relay exited before first-time setup completed (exit $($relay.ExitCode))."
        }
        Start-Sleep -Milliseconds 500
        $relay.Refresh()
    }

    $airlock = Start-Process -FilePath $airlockBinary -WorkingDirectory $airlockDir -NoNewWindow -PassThru
    Write-Host 'Airlock started using the relay address saved during setup.'

    while (-not $relay.HasExited -and -not $airlock.HasExited) {
        Start-Sleep -Seconds 1
        $relay.Refresh()
        $airlock.Refresh()
    }

    if ($relay.HasExited) {
        $component = 'Relay'
        $exitCode = $relay.ExitCode
    } else {
        $component = 'Airlock'
        $exitCode = $airlock.ExitCode
    }
    if ($exitCode -eq 0) {
        $exitCode = 1
    }
    Write-Error "$component stopped unexpectedly (exit $exitCode)." -ErrorAction Continue
} finally {
    foreach ($process in @($airlock, $relay)) {
        if ($null -ne $process -and -not $process.HasExited) {
            Stop-Process -Id $process.Id -ErrorAction SilentlyContinue
            $process.WaitForExit(5000) | Out-Null
        }
    }
}

exit $exitCode
