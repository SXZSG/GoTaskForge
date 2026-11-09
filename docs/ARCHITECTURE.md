# Architecture & correctness model

```text
Browser / curl / integration clients
      |
      v
HTTP API + optional X-API-Key + fixed-window limiter
      |                      |
      |              local map or Redis EVAL
      v
Repository abstraction
  | file snapshot    | MySQL 8 transaction + SKIP LOCKED + lease
  v                  v
Job state machine (queued -> running -> succeeded/failed/cancelled)
      ^
      |   worker goroutine(s), local wake + periodic polling
      +-- ReserveDue -> Provider.Generate(ctx) -> Complete(attempt fenced)
                                      |
                              Mock / OpenAI-compatible HTTP
```

### Storage modes

**File** (default): JSON snapshot is re-written on every change; atomic rename and fsync file, single-process. Workers claim under a mutex. Restart makes `running` jobs eligible again. Small dataset demo only; cost O(total jobs) per mutation; no SQL or Redis required.

**MySQL**: MySQL 8+, InnoDB. Schema created idempotently on startup. `queue_gate` row serializes new submissions to enforce pending-capacity at DB scope. Idempotency key uses unique index. Claims use `SELECT ... FOR UPDATE SKIP LOCKED` inside a transaction; claim increments `attempts` and sets `lease_until`. Recovery sweeps expired leases periodically. Complete uses `(id, attempts)` fencing and rejects stale attempts. Writes and reads have fixed 5s context timeouts. No automated retention or migration-version framework yet.

### Retry and cancellation

- Provider failures that are transient (HTTP 408/429/5xx, transport error, deadline) retry with capped exponential backoff until `MAX_ATTEMPTS`.
- Permanent failures mark `failed`. Job prompt is not logged. Response body from failed model provider is not stored.
- For queued tasks, cancel becomes terminal immediately. For running tasks, `cancel_requested` persists; current worker cancels its local context and other workers check the DB every 300ms.
- Server receives SIGTERM/SIGINT and drains HTTP with shutdown timeout. In-flight provider tasks stop on context cancellation and can retry after restart.
- Once an upstream model returns success but before its result is committed, a crash can trigger the call again. These are **at-least-once side effects**; no guarantee of exactly-once model billing.

### Limits and threat model

- `X-API-Key` required for non-loopback exposure unless `ALLOW_INSECURE_DEV=true` explicitly set. No per-user auth/authorization; all key-holders see all tasks. No TLS termination in app; use a reverse proxy for production.
- Redis fixed window counts API calls per API key + remote IP; Redis connection failures reject protected calls (503). Redis is not used to queue tasks.
- Prompt/result are stored unencrypted in JSON/MySQL, hence no sensitive information for demos. File permissions for local snapshot are 0600; DB security remains an operator responsibility.
- Local static dashboard keeps API key in live input DOM only; never localStorage. Do not reuse with privileged secrets on an untrusted machine.
- A delayed/blocked network can reduce throughput; file backend has O(n) snapshot; MySQL has polling overhead. No benchmarks or production SLOs are claimed.

### Useful future improvements (not implemented)

Job retention/archiving; streaming outputs; Prometheus/OpenTelemetry tracing; tenant isolation and RBAC; durable broker if write rate rises; per-client quotas; audit logs with sensitive-data minimization; horizontal autoscaling and real deployment load tests.
