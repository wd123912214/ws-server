$ErrorActionPreference = "Stop"
$root = Resolve-Path (Join-Path $PSScriptRoot "..\..")
$dist = Join-Path $root "dist\ws-server-windows-amd64"
New-Item -ItemType Directory -Force -Path $dist | Out-Null

$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
$env:GOARCH = "amd64"
Push-Location $root
try {
    go build -trimpath -ldflags "-s -w" -o (Join-Path $dist "ws-server.exe") .\cmd\ws-server
} finally {
    Pop-Location
}
Write-Host "Built $dist\ws-server.exe"
