# Interview notes — GoTaskForge

Use only after you personally run and understand the project. A portfolio project is evidence of learning, not a commercial deployment.

## What problems does it solve?

A synchronous model HTTP call can be slow or fail with rate-limit errors. GoTaskForge receives a prompt, commits an asynchronously processed task, returns a job ID and handles execution through an N-goroutine worker pool. Clients can query status and results later.

## Why goroutines and context?

Goroutines let an N-worker pool handle multiple independent requests without starting OS processes for each task. `context.WithTimeout` places a bound on provider time; cancelling a job propagates context cancellation. Shared mutable state lives in a Repository with locking (file mode) or database transactions (MySQL mode).

## Why use MySQL for durable state and Redis only for limiting?

MySQL stores the authoritative task state using ACID transactions. On MySQL 8, `FOR UPDATE SKIP LOCKED` lets multiple workers claim different pending tasks without duplicate claims. Redis `EVAL` atomically counts API calls and enforces quotas; losing Redis prevents API operations but never loses durable job state.

## Is the execution exactly-once?

No. A worker can call an external provider successfully and crash before committing its result, so a retry can call the provider again. Durable idempotent submission prevents duplicate *jobs* for the same client key, not duplicate *provider calls*. State this limitation explicitly.

## Why use attempt fencing and leases?

A worker claim has an `attempt` number and a lease. If a worker is lost, the lease eventually expires and the job can be retried. The old worker's completion is rejected when it presents a stale attempt. This prevents stale writes, but cannot undo a call already made to the external API.

## What are the major limitations?

Static single API key; no tenant isolation; plaintext prompts/results at rest; MySQL polling overhead; file mode O(n) snapshot writes; no archival or formal schema migration versioning. For future scaling: authenticated tenant scopes, proper migrations, metrics/traces, retention policy, and a broker if queue traffic requires it.

## Suggested resume wording (only after Codex confirms the tagged build)

**GoTaskForge — Go async AI task service | Go, MySQL, Redis, Docker**

- Implemented an asynchronous task API with a goroutine worker pool, cancellation, retry/backoff and idempotent submission.
- Added transactional MySQL task reservation with `SKIP LOCKED` and lease-based recovery, plus atomic Redis API rate limiting.
- Built a browser-based control panel, optional OpenAI-compatible adapter, Docker Compose setup and test suite including race and HTTP integration checks.

Do not invent throughput, latency improvements, live production adoption or company use.
