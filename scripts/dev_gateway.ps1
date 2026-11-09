# Starts free local deterministic mock model servers and the Go gateway.
# No paid model API and no Docker required. Ctrl+C stops the foreground API.
$ErrorActionPreference = 'Stop'
$root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
Set-Location $root
New-Item -ItemType Directory -Force -Path ./bin | Out-Null
& go build -o ./bin/mockllm.exe ./cmd/mockllm
if ($LASTEXITCODE -ne 0) { throw 'mockllm build failed' }
& go build -o ./bin/taskforge.exe ./cmd/server
if ($LASTEXITCODE -ne 0) { throw 'gateway build failed' }
$env:MOCK_ADDR='127.0.0.1:8091'; $env:MOCK_NAME='mock-a';$env:MOCK_DELAY_MS='30'
$a=Start-Process -FilePath (Join-Path $root 'bin/mockllm.exe') -PassThru -NoNewWindow
$env:MOCK_ADDR='127.0.0.1:8092'; $env:MOCK_NAME='mock-b';$env:MOCK_DELAY_MS='60'
$b=Start-Process -FilePath (Join-Path $root 'bin/mockllm.exe') -PassThru -NoNewWindow
$env:GATEWAY_BACKENDS='http://127.0.0.1:8091/v1,http://127.0.0.1:8092/v1'
$env:GATEWAY_MAX_PER_BACKEND='4'
$env:GATEWAY_TIMEOUT_SEC='90'
$env:STORAGE_BACKEND='file'
$env:PROVIDER='mock'
$env:ADDR='127.0.0.1:8080'
$env:API_KEY=''
try {
  Start-Sleep -Milliseconds 900
  Write-Host 'Gateway console at http://127.0.0.1:8080/'
  Write-Host 'Leave this terminal running; in another terminal execute: .\scripts\smoke_gateway.ps1'
  & ./bin/taskforge.exe
} finally {
  Stop-Process -Id $a.Id,$b.Id -ErrorAction SilentlyContinue
}
