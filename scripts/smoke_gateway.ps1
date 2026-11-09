param([string]$APIKey = '')
$ErrorActionPreference = 'Stop'
$base='http://127.0.0.1:8080'
$headers=@{}
if ($APIKey) { $headers['X-API-Key']=$APIKey }
$ready=Invoke-RestMethod -Uri "$base/readyz" -TimeoutSec 5
if ($ready.status -ne 'ready') { throw 'not ready' }
$payload=@{model='local-demo';messages=@(@{role='user';content='Go gateway smoke test'});stream=$false}|ConvertTo-Json -Depth 6
$reply=Invoke-RestMethod -Method POST -Uri "$base/v1/chat/completions" -Headers $headers -Body $payload -ContentType 'application/json' -TimeoutSec 15
if (-not $reply.choices[0].message.content.Contains('synthetic reply')) {throw 'bad mock completion'}
$stats=Invoke-RestMethod -Uri "$base/v1/gateway/backends" -Headers $headers -TimeoutSec 5
if ($stats.backends.Count -ne 2) { throw 'expected 2 backends' }
if (($stats.backends | Measure-Object -Property succeeded -Sum).Sum -lt 1) {throw 'no counted success'}
Write-Host 'GATEWAY_SMOKE_PASS: 2 backends, completion, metrics' -ForegroundColor Green
Write-Host 'For streaming example run:'
Write-Host 'curl.exe -N -H "Content-Type: application/json" --data-raw "{...}" http://127.0.0.1:8080/v1/chat/completions'
