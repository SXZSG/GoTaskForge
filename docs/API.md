# HTTP API v2

Base: `http://127.0.0.1:8080`. If configured, provide `X-API-Key: <configured key>` for protected endpoints. Only `/`, `/healthz`, `/readyz` are unauthenticated. `Content-Type: application/json` for POST jobs. No public multi-tenant support.

| Method | Endpoint | Success |
|---|---|---|
| GET | `/` | Static dashboard |
| GET | `/healthz` | 200 `{ "status":"ok" }` |
| GET | `/readyz` | 200 `{ "status":"ready" }` or 503 (DB/Redis unavailable) |
| POST | `/v1/jobs` | 202 created, 200 duplicate with same idempotency key |
| GET | `/v1/jobs/{id}` | 200 task object |
| GET | `/v1/jobs?status=queued&limit=20` | 200 `{ "jobs": [...], "count": N }` |
| POST | `/v1/jobs/{id}/cancel` | 200 updated task |
| GET | `/metrics` | 200 `{ "jobs": {"queued":0, ...,"total":0} }` JSON, NOT Prometheus |

Example request:

```http
POST /v1/jobs HTTP/1.1
Host: 127.0.0.1:8080
Content-Type: application/json
Idempotency-Key: demo-request-001

{"prompt":"Explain channels in Go"}
```

Task response (non-binding example):

```json
{
  "id": "ab12cd34ef56ab12cd34ef56ab12cd34",
  "prompt": "Explain channels in Go",
  "status": "queued",
  "attempts": 0,
  "next_run_at": "2026-11-08T12:00:00Z",
  "created_at": "2026-11-08T12:00:00Z",
  "updated_at": "2026-11-08T12:00:00Z"
}
```

Prompt: Unicode string 1..4000 characters, trimmed. Payload max 16KiB. Other JSON keys rejected. `Idempotency-Key` optional, at most 128 bytes; same key + same prompt returns original job even if queue is full; same key + different prompt returns 409. Identical prompt WITHOUT a key is a fresh task.

Status values: `queued`, `running`, `succeeded`, `failed`, `cancelled`. Running cancellation may return `running` with `cancel_requested:true` before provider stops.

Error status: 400 malformed input; 401 missing/bad configured key; 404 unknown job; 409 idempotency conflict or cancel terminal task; 429 request rate exceeded with `Retry-After`; 503 pending capacity or unavailable Redis; 500 storage error. Error payload is `{"error":"..."}` with sanitized messages.

---

## v3: OpenAI-compatible 推理网关

- `POST /v1/chat/completions` — 接收 `model`, `messages`, `stream` 等 OpenAI Chat Completions JSON 参数。只转发至服务端配置的上游，不接受自定义 URL。`stream=true` 时透传 `text/event-stream`。状态码：400 缺参数，413 请求大于 1 MiB，429 受 API 限流，502 上游错误，503 无可用节点，504 上游超时。
- `GET /v1/gateway/backends` — 获取节点状态、inflight、请求/成功/失败计数、最近完成请求 P50/P95 时延。
- `GET /metrics/prometheus` — Prometheus exposition text；不含 GPU 指标或 Token 指标。

上述接口与异步任务 API 一样走 `X-API-Key` 和配置的 Redis/内存限流。只有在 `GATEWAY_BACKENDS` 指定上游时可用。参见 [推理网关架构](INFERENCE_GATEWAY.md)。
