param([string]$APIKey = "")
$ErrorActionPreference = "Stop"
if (-not (Test-Path .env)) { throw "Copy .env.example to .env and set passwords first." }
if (-not $APIKey) {
  $line = Get-Content .env | Where-Object { $_ -match '^TASKFORGE_API_KEY=' } | Select-Object -First 1
  if ($line) { $APIKey = ($line -split '=',2)[1].Trim() }
}
if (-not $APIKey) { throw "API key required" }
docker compose up --build -d
if ($LASTEXITCODE -ne 0) { throw "Compose startup failed" }
$ready = $false
for ($i=0; $i -lt 45; $i++) {
  try {
    $response = Invoke-RestMethod -Uri 'http://127.0.0.1:8080/readyz' -TimeoutSec 2
    if ($response.status -eq 'ready') { $ready = $true; break }
  } catch { }
  Start-Sleep -Seconds 2
}
if (-not $ready) { docker compose logs --tail=150 api; throw "API not ready" }
& ./scripts/smoke.ps1 -APIKey $APIKey
if ($LASTEXITCODE -ne 0) { throw "Smoke test failed" }
Write-Host "COMPOSE_SMOKE_PASS"
