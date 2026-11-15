param([string]$ServiceName = "SimpleWSRelay")

$ErrorActionPreference = "Stop"
$service = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if (-not $service) {
    Write-Host "Service $ServiceName is not installed"
    exit 0
}
if ($service.Status -ne "Stopped") {
    Stop-Service -Name $ServiceName -Force
    $service.WaitForStatus("Stopped", [TimeSpan]::FromSeconds(20))
}
sc.exe delete $ServiceName | Out-Null
Write-Host "Removed $ServiceName. config.json and logs were preserved."
