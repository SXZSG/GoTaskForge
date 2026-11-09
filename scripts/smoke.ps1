param(
  [string]$BaseURL = "http://127.0.0.1:8080",
  [string]$APIKey = ""
)
$ErrorActionPreference = "Stop"
$headers = @{}
if ($APIKey) { $headers["X-API-Key"] = $APIKey }
Write-Host "[1/5] Checking /healthz and /readyz"
$h = Invoke-RestMethod -Uri "$BaseURL/healthz"
$r = Invoke-RestMethod -Uri "$BaseURL/readyz"
if ($h.status -ne "ok" -or $r.status -ne "ready") { throw "Unhealthy server" }
Write-Host "[2/5] Creating task"
$idem = "smoke-" + [guid]::NewGuid().ToString("N")
$headers["Idempotency-Key"] = $idem
$job = Invoke-RestMethod -Method POST -Uri "$BaseURL/v1/jobs" -Headers $headers -ContentType "application/json" -Body '{"prompt":"Explain Go context cancellation in one sentence."}'
if (-not $job.id) { throw "Missing job ID" }
Write-Host "[3/5] Re-sending idempotent request"
$again = Invoke-RestMethod -Method POST -Uri "$BaseURL/v1/jobs" -Headers $headers -ContentType "application/json" -Body '{"prompt":"Explain Go context cancellation in one sentence."}'
if ($again.id -ne $job.id) { throw "Idempotency failed" }
Write-Host "[4/5] Polling completion"
$headers.Remove("Idempotency-Key")
$done = $null
for ($i=0; $i -lt 60; $i++) {
  $done = Invoke-RestMethod -Uri "$BaseURL/v1/jobs/$($job.id)" -Headers $headers
  if ($done.status -in @("succeeded", "failed", "cancelled")) { break }
  Start-Sleep -Milliseconds 250
}
if ($done.status -ne "succeeded" -or -not $done.result) { throw "Unexpected final status: $($done.status) $($done.error)" }
Write-Host "[5/5] Reading metrics"
$metrics = Invoke-RestMethod -Uri "$BaseURL/metrics" -Headers $headers
if ($metrics.jobs.total -lt 1) { throw "Metrics missing tasks" }
Write-Host "SMOKE_PASS id=$($job.id) attempts=$($done.attempts)"
