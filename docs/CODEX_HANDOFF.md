# Codex local verification & takeover (2026-11-08)

## Objective

You are receiving a Go internship portfolio project `GoTaskForge` to **actually run, test, diagnose and improve**. Do not claim that a test passed unless you executed it. The archive was created by ChatGPT, with offline core Go tests completed in a Linux container (Go 1.23.2). The authoring environment had no Docker and no access to `proxy.golang.org`, so **the tagged MySQL driver build, real MySQL and Redis, and Docker Compose have not been tested here**. Those are your highest priority.

## Scope / invariants

1. Preserve the existing REST endpoints and JSON response schemas. No secrets in logs, Git or reports.
2. Keep file backend operational with **no external Go dependencies**; this is the fast regression path.
3. For the MySQL backend: idempotency key + prompt mismatch -> 409; accepted duplicate -> same ID; queued/running capacity not bypassed by concurrent submissions; `SELECT ... FOR UPDATE SKIP LOCKED` gives no duplicate claims; expired leases restore work (at-least-once); stale attempt cannot overwrite new attempt; cancellation must be observable across instances.
4. For Redis rate limiting: reject over-limit requests with 429; if Redis unavailable, fail closed with 503 for protected endpoints. Use Redis only for rate limiting, **not** as a durable queue.
5. Mock AI mode must work end-to-end without any paid API. Do not use live model APIs or incur charges unless the user explicitly configures them.
6. Do not fabricate production load, QPS, SLA, user count, or performance results for a resume.

## First run (PowerShell, from repository root)

```powershell
go version
.\scripts\test_local.ps1
# If race detector fails due to missing C compiler on Windows, report exactly that;
# run other tests regardless, then arrange a C compiler or WSL for -race.
go run ./cmd/server
# open another terminal:
.\scripts\smoke.ps1
```

Expected: format clean, unit tests PASS, vet PASS, `/healthz` and `/readyz` 200; POST jobs returns 202; duplicate same key returns same ID; final status `succeeded` in mock mode; the dashboard works at http://127.0.0.1:8080/ .

## Real infrastructure (Docker Desktop)

```powershell
Copy-Item .env.example .env
# Edit MYSQL_ROOT_PASSWORD, MYSQL_PASSWORD, TASKFORGE_API_KEY in .env to new values.
docker compose up --build -d
docker compose ps
docker compose logs --tail=200 api
.\scripts\test_compose.ps1
```

Local client only: 127.0.0.1:8080. Docker uses MySQL 8.4 and Redis 7.4; full backend binary builds with `-tags mysql`.

## Full tagged build and live integration tests

Run the following when Docker + Go dependencies can be downloaded:

```powershell
# Expose DB/Redis to local loopback ports 3307/6380 for integration tests.
docker compose -f compose.yaml -f compose.integration.yaml up --build -d
go mod download
go test -tags mysql ./...
go vet -tags mysql ./...
# Use the MYSQL_PASSWORD you set in .env (no credentials in shared logs).
$env:MYSQL_DSN = 'taskforge:<MYSQL_PASSWORD>@tcp(127.0.0.1:3307)/gotaskforge?parseTime=true&loc=UTC'
$env:REDIS_URL = 'redis://127.0.0.1:6380/0'
go test -tags 'mysql integration' -count=1 ./...
```

In PowerShell, `-tags 'mysql integration'` passes the space-delimited Go build tags argument. If driver versions or Docker image syntax need adjustment, make minimal justified changes and rerun the complete suite.

## Suggested manual failure experiments

- Set `MAX_PENDING=1`, `WORKERS=1`, slow upstream; verify second distinct request returns 503 and duplicate existing key still returns 200.
- Send concurrent POST requests with same `Idempotency-Key`; verify one durable row and same ID, differing prompt yields 409.
- Verify invalid API Key -> 401 and fixed-window quota -> 429 with `Retry-After`. Stop Redis -> 503 protected API endpoints while `/healthz` stays 200 and `/readyz` becomes 503.
- Kill server during a long task; restart. In file mode running tasks requeue on startup; in MySQL mode lease expiry requeues tasks. **At-least-once is expected, not exactly-once**.
- Cancel from a different server instance while job is running; worker should observe `cancel_requested`. Verify terminal job cannot be cancelled.
- Verify SQL injection-safe parameterized queries, malformed JSON rejection, max prompt length, key validation, no secrets in API response or logs.
- Test two workers concurrently claiming one due job from MySQL; verify one claim and attempt fencing.
- Check Docker `api` health, MySQL health, Redis health, browser dashboard, shutdown and restart persistence.

## Handoff deliverables

Return one short report with (a) environment versions; (b) commands actually run and PASS/FAIL/SKIP; (c) defect table with cause/fix; (d) test output snippets; (e) updated archive if code changed; (f) remaining limitations. Do not change research files or unrelated local repositories.

---

## v3 (2026-11-09): inference gateway takeover tasks

Read `docs/INFERENCE_GATEWAY.md` and `docs/INTERVIEW_V3.md` first. New source: `internal/gateway/`, `cmd/mockllm/`, `cmd/loadgen/`, `scripts/dev_gateway.ps1`, `scripts/smoke_gateway.ps1`.

### First, verify without Docker or external API

```powershell
go test -count=1 ./...
go test -race -count=1 ./...  # WSL or C compiler may be required
go vet ./...
.\scripts\dev_gateway.ps1
# second PowerShell terminal
.\scripts\smoke_gateway.ps1
go run ./cmd/loadgen -n 20 -c 4
```

Verify on the frontend homepage: backend metrics update; no secret in browser network responses; SSE output appears gradually instead of at the end. Use short, synthetic prompt only.

### Then, full Docker integration

```powershell
# Edit 3 demo password placeholders in your private .env, never print secrets
docker compose up --build -d
docker compose ps
.\scripts\test_compose.ps1
.\scripts\smoke_gateway.ps1 -APIKey <your-local-api-key>
```

Do these directed fault injections:
1. Stop one `mock-a` container; send requests; confirm the next few failures trip its breaker, while `mock-b` remains accessible.
2. Use very low `GATEWAY_MAX_PER_BACKEND` and delayed mock responses; verify 503 admission rejects overload.
3. Send `stream=true` and check `data:` chunks arrive incrementally until `[DONE]`.
4. Verify 1 MiB input cap, 8 MiB nonstream output cap, bad JSON/empty messages rejected.
5. Restart API and verify async task file/MySQL durability; check Redis and MySQL integration as in the v2 instructions above.
6. If connecting vLLM/SGLang, perform a **separate explicitly approved offline/local** experiment. Do not invoke paid API without permission. Record exact model/hardware and serve protocol differences. No fabricated token throughput, TTFT or CUDA speedups.
7. Code-review known limitations: per-process breaker and concurrency cap, lack of half-open probe serialization, no true GPU-aware routing, no multi-tenant auth, SSE midstream failure ambiguity.

Return PASS/FAIL/SKIP for every category, exact commands, defects repaired, artifacts created, and honest resume readiness. The role of Codex is local validation and targeted fixes; no unrelated changes.
