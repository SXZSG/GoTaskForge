param(
  [int]$Requests = 1000,
  [string]$BaseURL = 'http://127.0.0.1:8080',
  # Comma-separated levels, e.g. -Concurrency 1,4,8,16. Kept as a string because
  # powershell.exe -File cannot bind "-Concurrency 2,8" to [int[]] (it becomes 28).
  [string]$Concurrency = '1,4,16,32,64',
  [string]$APIKey = ''
)

# Local Mock-HTTP gateway benchmark. It does NOT measure GPU inference,
# tokens/s, TTFT or production capacity.
#
# Statistics contract:
#  - success_req_s     : HTTP 200 responses / loadgen-measured elapsed time
#  - completed_req_s   : all responses / elapsed time (includes 503 rejections)
#  - p50/p95/p99       : only over successful (HTTP 200) request latencies
#  - admission_rejected: gateway counter delta / configured backend count, i.e.
#                        the number of requests the gateway refused with 503
#  - cpu_seconds       : taskforge process CPU time delta over the level
#  - working_set_mb    : post-level working set sample (not a peak)
#  - peak_working_set_mb: process lifetime peak working set

$ErrorActionPreference = 'Stop'
$root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
Set-Location $root
$go = Get-Command go -ErrorAction Stop

$headers = @{}
if ($APIKey) { $headers['X-API-Key'] = $APIKey }

$levels = @()
foreach ($part in ($Concurrency -split ',')) {
  $text = $part.Trim()
  if ($text -eq '') { continue }
  $value = 0
  if (-not [int]::TryParse($text, [ref]$value) -or $value -lt 1 -or $value -gt 1000) {
    throw "invalid concurrency level: '$text' (expected 1..1000, comma separated)"
  }
  $levels += $value
}
if ($levels.Count -eq 0) { throw 'no concurrency levels given' }

function Get-GatewayCounters {
  $text = (Invoke-WebRequest -Uri "$BaseURL/metrics/prometheus" -Headers $headers -TimeoutSec 10 -UseBasicParsing).Content
  $sum = @{ succeeded = [int64]0; failed = [int64]0; rejected = [int64]0 }
  foreach ($line in ($text -split "`n")) {
    if ($line -match '^gotaskforge_gateway_requests_succeeded_total\{[^}]*\}\s+(\d+)') { $sum.succeeded += [int64]$Matches[1] }
    elseif ($line -match '^gotaskforge_gateway_requests_failed_total\{[^}]*\}\s+(\d+)') { $sum.failed += [int64]$Matches[1] }
    elseif ($line -match '^gotaskforge_gateway_admission_rejections_total\{[^}]*\}\s+(\d+)') { $sum.rejected += [int64]$Matches[1] }
  }
  return $sum
}

$backends = @((Invoke-RestMethod -Uri "$BaseURL/v1/gateway/backends" -Headers $headers -TimeoutSec 10).backends)
if ($backends.Count -lt 1) { throw 'no gateway backends configured' }
Write-Host "Gateway performance test: requests=$Requests url=$BaseURL backends=$($backends.Count)"
Write-Host 'Requires an already-running gateway with local Mock backends.'
Write-Host 'The API rate limiter (RATE_PER_MINUTE, default 60/min) also guards this route:'
Write-Host 'start the gateway with a high RATE_PER_MINUTE for the workload, e.g. $env:RATE_PER_MINUTE=10000,'
Write-Host 'otherwise 429 responses dominate and the numbers describe the limiter, not the gateway.'
Write-Host ''

foreach ($c in $levels) {
  $proc = Get-Process taskforge -ErrorAction SilentlyContinue | Select-Object -First 1
  $beforeCpu = if ($proc) { $proc.CPU } else { $null }
  $countersBefore = Get-GatewayCounters

  $started = Get-Date
  # Windows PowerShell 5.1 turns native stderr into terminating errors while
  # ErrorActionPreference is Stop, and loadgen exits non-zero when any request
  # fails (expected once admission starts rejecting). Capture it instead.
  $previousEap = $ErrorActionPreference
  $ErrorActionPreference = 'Continue'
  $output = & $go.Source run ./cmd/loadgen -url $BaseURL -n $Requests -c $c 2>&1
  $exitCode = $LASTEXITCODE
  $ErrorActionPreference = $previousEap
  $wall = ((Get-Date) - $started).TotalSeconds

  $countersAfter = Get-GatewayCounters
  $output | ForEach-Object { Write-Host $_ }

  $proc = if ($proc) { Get-Process -Id $proc.Id -ErrorAction SilentlyContinue } else { $null }
  $cpuDelta = if ($proc -and $null -ne $beforeCpu) { [math]::Round($proc.CPU - $beforeCpu, 3) } else { 'n/a' }
  $workingSet = if ($proc) { [math]::Round($proc.WorkingSet64 / 1MB, 2) } else { 'n/a' }
  $peakSet = if ($proc) { [math]::Round($proc.PeakWorkingSet64 / 1MB, 2) } else { 'n/a' }

  $succeededDelta = $countersAfter.succeeded - $countersBefore.succeeded
  $failedDelta = $countersAfter.failed - $countersBefore.failed
  $rejectedDelta = $countersAfter.rejected - $countersBefore.rejected
  $rejectedRequests = [math]::Floor($rejectedDelta / $backends.Count)

  $http503 = 0
  $http429 = 0
  # loadgen prints Go's map format: status=map[200:24 503:976]
  $statusMatch = [regex]::Match(($output -join "`n"), 'status=map\[([^\]]*)\]')
  if ($statusMatch.Success) {
    foreach ($pair in [regex]::Matches($statusMatch.Groups[1].Value, '(\d+):(\d+)')) {
      $code = [int]$pair.Groups[1].Value
      $count = [int]$pair.Groups[2].Value
      if ($code -eq 503) { $http503 = $count }
      if ($code -eq 429) { $http429 = $count }
    }
  }
  $reconciled = ($http503 -eq $rejectedRequests)
  if ($http429 -gt 0) {
    Write-Warning ("concurrency {0}: {1} requests hit the API rate limiter (429); the gateway is not the bottleneck for this level" -f $c, $http429)
  }

  Write-Host ("summary concurrency={0} wall_seconds={1} exit={2} gateway_cpu_seconds={3} working_set_mb={4} peak_working_set_mb={5}" -f $c, [math]::Round($wall, 3), $exitCode, $cpuDelta, $workingSet, $peakSet)
  Write-Host ("gateway_counters concurrency={0} succeeded_delta={1} failed_delta={2} admission_rejected_requests={3} http_503={4} reconciled={5}" -f $c, $succeededDelta, $failedDelta, $rejectedRequests, $http503, $reconciled)
  if (-not $reconciled) {
    Write-Warning ("concurrency {0}: HTTP 503 count ({1}) does not match gateway admission rejections ({2})" -f $c, $http503, $rejectedRequests)
  }
  Write-Host ''
}
