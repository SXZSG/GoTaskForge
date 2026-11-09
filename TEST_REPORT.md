# GoTaskForge v3 test report — 2026-11-09

> **Historical record (P3 round).** It describes what was executed *before* the
> final acceptance round. The gaps it lists (no `go.sum`, no race run, no
> MySQL-tagged build) were closed later the same day; see
> `FINAL_RELEASE_REPORT.md` for the authoritative, up-to-date verification
> matrix. Numbers in this file remain valid only as the earlier measurement.

## Environment

- Linux amd64; Go 1.23.2; Node 22.16.0 for JavaScript syntax validation.
- Docker executable not available in this authoring environment; MySQL Go driver not in local module cache. Network-based MySQL build and Docker/Redis integration **not executed**.
- Workload: **synthetic OpenAI-compatible Mock** servers at two loopback addresses, not a real model or GPU. No paid API or external server called.

## Actually executed — PASS

- `go test -count=1 ./...`
- `go test -race -count=1 ./...`
- `go vet ./...`
- `test -z "$(gofmt -l ./cmd ./internal)"`
- `go build -o /tmp/gotaskforge-api ./cmd/server`
- `go build -o /tmp/gotaskforge-mock ./cmd/mockllm`
- `go build -o /tmp/gotaskforge-loadgen ./cmd/loadgen`
- `node --check` for dashboard embedded JavaScript.
- `PyYAML.safe_load` parsing of `compose.yaml` (syntax only, not Docker Compose schema/runtime).
- Real HTTP loopback e2e with standalone gateway and **2 independently running Mock servers**: `/healthz`, dashboard, four JSON completions, backend routing statistics, SSE completion with `[DONE]`, Prometheus metrics, original async job creation / eventual success — PASS.
- Gateway tests: authentication separation, URL validation, two-backend least-inflight admission, overload 503, circuit breaker and cooldown recovery, SSE flushing before upstream completes, request body bounds, timeout 504, client cancellation releasing capacity, API-key middleware route protection — PASS.

## Synthetic loadgen example (not a real LLM performance result)

Executed against the two local Mock upstreams (25 ms / 35 ms artificial delay), loopback HTTP, `-n 20 -c 4`:

```text
requests=20  concurrency=4  success=20  fail=0
elapsed=157.0 ms  completed HTTP req/s=127.38
success-only end-to-end p50=26.11 ms  p95=37.13 ms
```

These numbers are **not** GPU throughput, real AI tokens/s, TTFT, production QPS, or evidence of any speedup. Do not use them as stand-alone resume achievements.

## NOT executed / still requires local Codex

- `go mod download`, `go test -tags mysql ./...`, `go build -tags mysql ./cmd/server` (requires package download).
- Live MySQL 8.4, Redis 7.4, Docker Compose startup, `integration` test tag.
- Windows PowerShell scripts (`scripts/dev_gateway.ps1`, `scripts/smoke_gateway.ps1`, etc.).
- Real model servers such as vLLM/SGLang, live CUDA/Triton, real token-performance benchmarks.
- Production load, penetration tests, multiple gateway replicas, multi-tenant auth or deployment on a public internet endpoint.

## Known limitations

See `docs/INFERENCE_GATEWAY.md`. This is an auditable local portfolio project, not a production-ready AI-serving platform. Original v2 MySQL/Redis code has not been modified beyond wiring and Compose; its real integration still must be validated. Local Codex should fix genuine defects rather than report inferred test success.
