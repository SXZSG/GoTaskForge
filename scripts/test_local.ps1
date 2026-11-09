$ErrorActionPreference = "Stop"
Write-Host "Go version:"; go version
Write-Host "[1/4] Formatting"
$bad = gofmt -l ./cmd ./internal
if ($bad) { throw "Go formatting required: $bad" }
Write-Host "[2/4] Unit tests"
go test -count=1 ./...
if ($LASTEXITCODE -ne 0) { throw "Unit tests failed" }
Write-Host "[3/4] Static analysis"
go vet ./...
if ($LASTEXITCODE -ne 0) { throw "go vet failed" }
Write-Host "[4/4] Race tests (requires C compiler on Windows)"
go test -race -count=1 ./...
if ($LASTEXITCODE -ne 0) { throw "Race tests failed. Ensure CGO + C compiler is installed, then rerun." }
Write-Host "LOCAL_CHECKS_PASS"
