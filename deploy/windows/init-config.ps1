$ErrorActionPreference = "Stop"
$baseDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$configPath = Join-Path $baseDir "config.json"
$examplePath = Join-Path $baseDir "config.example.json"

if (Test-Path $configPath) {
    Write-Host "config.json already exists"
    exit 0
}
if (-not (Test-Path $examplePath)) {
    throw "Missing $examplePath"
}

$rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
try {
    $tokenBytes = New-Object byte[] 32
    $superBytes = New-Object byte[] 32
    $rng.GetBytes($tokenBytes)
    $rng.GetBytes($superBytes)
} finally {
    $rng.Dispose()
}
$token = ($tokenBytes | ForEach-Object { $_.ToString("x2") }) -join ""
$superToken = ($superBytes | ForEach-Object { $_.ToString("x2") }) -join ""
$content = [System.IO.File]::ReadAllText($examplePath)
$content = $content.Replace("CHANGE_ME_AT_LEAST_16_CHARS", $token)
$content = $content.Replace("CHANGE_ME_SUPER_TOKEN", $superToken)
$utf8NoBom = New-Object System.Text.UTF8Encoding($false)
[System.IO.File]::WriteAllText($configPath, $content, $utf8NoBom)

Write-Host "Created config.json with independent random 256-bit X-Token and X-Super-Token."
Write-Host "Use X-Super-Token only in the super administrator program."
