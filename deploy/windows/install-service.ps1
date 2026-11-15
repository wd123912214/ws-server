param(
    [string]$ServiceName = "SimpleWSRelay",
    [switch]$OpenFirewall
)

$ErrorActionPreference = "Stop"
$baseDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$exePath = Join-Path $baseDir "ws-server.exe"
$configPath = Join-Path $baseDir "config.json"

if (-not (Test-Path $exePath)) {
    throw "Missing $exePath"
}
if (-not (Test-Path $configPath)) {
    & (Join-Path $baseDir "init-config.ps1")
}
if (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue) {
    throw "Service $ServiceName already exists. Run uninstall-service.ps1 first."
}

New-Service `
    -Name $ServiceName `
    -BinaryPathName ('"{0}"' -f $exePath) `
    -DisplayName "Simple WebSocket Relay" `
    -Description "In-memory WebSocket and one-shot HTTP relay" `
    -StartupType Automatic

sc.exe failure $ServiceName reset= 86400 actions= restart/5000/restart/15000/restart/60000 | Out-Null

if ($OpenFirewall) {
    if (-not (Get-NetFirewallRule -DisplayName "Simple WebSocket Relay" -ErrorAction SilentlyContinue)) {
        New-NetFirewallRule -DisplayName "Simple WebSocket Relay" -Direction Inbound -Action Allow -Protocol TCP -LocalPort 8080 | Out-Null
    }
}

Start-Service $ServiceName
Write-Host "Installed and started $ServiceName"
Write-Host "The shared X-Token is stored in config.json"
