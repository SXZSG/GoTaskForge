# GoTaskForge — Go 异步任务调度 + LLM 推理服务网关

**A Go backend portfolio project: an asynchronous AI-task service and a small OpenAI-compatible LLM inference gateway, built to be run and verified offline.**

一个可独立运行、面向 **Go 后端 / AI Infra 实习岗位** 的工程作品。它把两类真实存在的后端问题放在同一个仓库里：

1. **异步任务服务**：把一次较慢的 AI 调用变成一个可查询、可取消、可幂等重试、可持久化的任务；
2. **推理服务网关**：把在线 Chat Completion 请求路由到固定上游池，做准入控制、并发上限、熔断、超时和 SSE 透传。

> **这是求职展示版，不是生产系统。** 所有推理性能数字都来自本机**合成 Mock 上游**的 HTTP 测量，不是 GPU 吞吐、Token/s、TTFT 或线上 QPS。Docker/MySQL/Redis 联调未在本机执行（详见[已验证 / 未验证](#已验证--未验证)）。

---

## 已验证 / 未验证

本仓库的结论只写实际执行过的命令产生的结果。

| 项目 | 状态 | 说明 |
|---|---|---|
| `go build ./...` / `go vet ./...` | ✅ 通过 | Go 1.23.12 与 1.27.3，Windows 与 WSL Linux |
| `go test -count=1 ./...` | ✅ 通过 | 7 个有测试的包（config/gateway/httpapi/provider/rediskit/runner/task） |
| `go test -race -count=1 ./...` | ✅ 通过 | WSL Ubuntu 22.04 + GCC 11.4（race 需要 CGO/C 编译器） |
| `go build -tags mysql` / `go vet -tags mysql` | ✅ 通过 | 仅编译，未连接真实 MySQL |
| `go mod verify` + `GOPROXY=off` 离线构建 | ✅ 通过 | `go.sum` 已提交，依赖图完全锁定 |
| 本机 HTTP 端到端（网关 + 2 个 Mock + SSE） | ✅ 通过 | 见 [快速开始](#快速开始) |
| Docker Compose / MySQL 8.4 / Redis 7.4 联调 | ❌ 未执行 | 本机未安装 Docker，`integration` tag 未运行 |
| 真实 vLLM / SGLang / GPU 推理 | ❌ 未执行 | 无 GPU 服务，不做任何吞吐声明 |
| 多实例共享熔断/并发状态、跨实例一致性 | ❌ 未执行 | 属于已知设计边界 |

---

## 它能做什么

| 模块 | 能力 |
|---|---|
| **任务 API** | 创建 / 查询 / 列表 / 取消，RESTful JSON，`Idempotency-Key` 幂等，输入大小与长度限制 |
| **任务调度** | 有界容量、指数退避重试、状态机（queued/running/succeeded/failed/cancelled）、运行中取消 |
| **Worker Pool** | goroutine 工作池 + context 超时，任务租约与 `attempt` 过期结果隔离 |
| **存储后端** | 文件后端（单机 JSON 原子快照，断点恢复，零外部依赖）；MySQL 后端（事务 + 唯一幂等键 + `SKIP LOCKED` 领取 + 租约恢复） |
| **限流** | Redis Lua `INCR` 固定窗口；无 Redis 时用进程内固定窗口；超限 429 + `Retry-After`，Redis 故障保护性 503 |
| **推理网关** | least-inflight 路由、每后端并发上限、被动熔断 + 冷却、请求超时、SSE 逐块透传 |
| **可观测性** | 每后端成功/失败/拒绝计数、in-flight、最近 256 次成功请求 P50/P95、Prometheus 文本指标 |
| **AI Provider** | 免费 Mock；可选 OpenAI-compatible `/chat/completions`（显式配置后才会产生费用） |
| **管理面板** | `http://127.0.0.1:8080/` 单页 HTML，零前端依赖，查看任务/网关状态并发起请求 |
| **工程化** | 单元测试、race 检测、静态检查、GitHub Actions、Compose 编排、可重复压测脚本 |

---

## 架构

```
                   浏览器面板 / curl / loadgen
                              |
                    +---------v----------+
                    |  HTTP API 层        |  X-API-Key 认证
                    |  /v1/jobs           |  固定窗口限流
                    |  /v1/chat/completions|
                    +----+-----------+----+
                         |           |
        异步任务路径       |           |   在线推理路径
                         v           v
              +----------+--+   +----+-------------------+
              |  Runner     |   |  Gateway               |
              |  worker pool|   |  least-inflight        |
              |  重试/取消   |   |  per-backend cap       |
              +------+------+   |  breaker + timeout     |
                     |          +----+-------------+-----+
                     v               |             |
          +----------+---------+    v             v
          | task.Repository    |  mock-a       mock-b
          | file  |  MySQL     |  (或 vLLM / SGLang 等
          +--------------------+   OpenAI 兼容上游)
```

两条路径互相独立：异步任务不经过网关，网关不写任务存储。

---

## 技术栈

| 选择 | 原因 |
|---|---|
| 标准库 `net/http`（Go 1.22+ 路由模式） | 无框架依赖，路由与中间件行为完全可控，面试可逐行解释 |
| `log/slog` 结构化日志 | 标准库、无第三方依赖 |
| 文件快照（临时文件 + `rename` 原子替换） | 离线可跑，崩溃后 `running` 任务重新入队（至少一次语义） |
| MySQL `SELECT ... FOR UPDATE SKIP LOCKED` | 多 worker 无重复领取，配合租约做过期恢复 |
| Redis Lua `INCR` + `EXPIRE` | 单脚本原子计数，避免读改写竞态 |
| `embed` 内嵌单页面板 | 二进制自包含，不引入前端构建链 |
| Go 依赖只有一个 | `github.com/go-sql-driver/mysql`，且只在 `-tags mysql` 时编译 |

---

## 快速开始

**不需要 Docker，不需要 API Key，不需要 GPU。** 默认使用文件存储 + Mock Provider。

### Windows（PowerShell）

```powershell
go version                  # 需要 Go 1.23+
go run ./cmd/server         # 打开 http://127.0.0.1:8080/
```

另开一个终端跑冒烟测试：

```powershell
.\scripts\smoke.ps1
```

### 一键启动推理网关（2 个 Mock 上游）

```powershell
.\scripts\dev_gateway.ps1      # 终端 A：启动 mock-a / mock-b / 网关
.\scripts\smoke_gateway.ps1    # 终端 B：验证 2 后端、completion、统计
go run ./cmd/loadgen -n 20 -c 4
```

### Linux / macOS

```bash
go run ./cmd/server
go test -count=1 ./...
```

### 直接调 API

```powershell
$body = @{prompt='用一句话解释 Go 的 goroutine'} | ConvertTo-Json
$job = Invoke-RestMethod -Method POST -Uri 'http://127.0.0.1:8080/v1/jobs' `
  -ContentType 'application/json' -Body $body -Headers @{'Idempotency-Key'='demo-001'}
Invoke-RestMethod -Uri "http://127.0.0.1:8080/v1/jobs/$($job.id)"
Invoke-RestMethod -Uri 'http://127.0.0.1:8080/metrics'
```

SSE 流式（逐块输出，最后 `data: [DONE]`）：

```powershell
curl.exe -N -H "Content-Type: application/json" `
  --data-raw '{\"model\":\"local-demo\",\"stream\":true,\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}' `
  http://127.0.0.1:8080/v1/chat/completions
```

完整端点和错误码见 [docs/API.md](docs/API.md)。

### 连接真实模型（可选，会产生费用）

只有显式设置 `PROVIDER=openai` 且提供 `OPENAI_BASE_URL` / `OPENAI_MODEL` / `OPENAI_API_KEY` 时才会调用真实上游，启动时会校验三者。HTTP 408/429/5xx 与传输错误可重试，其他错误不重试。**重试会增加调用次数与费用，且系统不保证外部副作用恰好一次。** 不要把真实密钥提交到仓库。

### Docker Compose（含 MySQL + Redis + 双 Mock，本仓库未在作者环境验证）

```powershell
Copy-Item .env.example .env    # 修改三个演示密码后再启动
docker compose up --build -d
docker compose ps
.\scripts\test_compose.ps1
```

容器：MySQL 8.4 / Redis 7.4 / Go API / mock-a / mock-b；仅 API 映射到 `127.0.0.1:8080`。
> ⚠️ 本机无 Docker，这一路径**没有经过实际运行验证**，只做了文件级审查与语法检查。

---

## 测试

```bash
go build ./...                    # 编译
go vet ./...                      # 静态检查
go test -count=1 ./...            # 单元测试
go test -race -count=1 ./...      # 竞态检测（Linux/WSL + gcc，或 Windows + CGO 工具链）
go test -tags mysql -count=1 ./...   # 含 MySQL 后端代码路径
```

Windows 上 `-race` 需要 CGO + C 编译器；没有时请在 WSL 里跑（本项目即在 WSL Ubuntu 22.04 + GCC 11.4 下通过）。

真实 MySQL/Redis 集成测试需要服务与凭据：

```powershell
docker compose -f compose.yaml -f compose.integration.yaml up --build -d
$env:MYSQL_DSN = 'taskforge:<password>@tcp(127.0.0.1:3307)/gotaskforge?parseTime=true&loc=UTC'
$env:REDIS_URL = 'redis://127.0.0.1:6380/0'
go test -tags 'mysql integration' -count=1 ./...
```

Windows 一键本地检查：`.\scripts\test_local.ps1`。

---

## 性能（本机 Mock HTTP，不是 GPU 性能）

来源：`.\scripts\perf_gateway.ps1 -Requests 1000`，本机环回，两个合成 Mock 上游（注入延迟 30 ms / 60 ms），`GATEWAY_MAX_PER_BACKEND=4`（2 后端 = 8 个准入名额），`RATE_PER_MINUTE=10000`。每档 1,000 请求，合计 5,000 请求。

| 并发 | 成功/失败(503) | **成功吞吐 req/s** | 完成吞吐 req/s（含 503） | P50 | P95 | P99 | 网关 CPU 秒 |
|---:|---:|---:|---:|---:|---:|---:|---:|
| 1 | 1000/0 | **21.44** | 21.44 | 34.26 ms | 62.08 ms | 62.34 ms | 0.453 |
| 4 | 1000/0 | **96.29** | 96.29 | 31.67 ms | 61.80 ms | 62.18 ms | 0.359 |
| 16 | 24/976 | **182.46** | 7602.59 | 33.22 ms | 67.68 ms | 68.72 ms | 0.375 |
| 32 | 24/976 | **186.16** | 7756.69 | 34.19 ms | 66.05 ms | 66.58 ms | 0.250 |
| 64 | 24/976 | **181.06** | 7544.26 | 36.51 ms | 72.33 ms | 72.33 ms | 0.406 |

**统计口径（重要）**

- **成功吞吐** = HTTP 200 数 / 耗时，代表真正被服务的容量；**完成吞吐**包含被准入拒绝的 503，只反映拒绝速度，不能当作吞吐能力。两者相差 40 倍，报告时必须写清是哪一个。
- **P50/P95/P99 只统计成功请求**，并给出样本数；高并发档只有 24 个成功样本，分位数仅供参考。
- **CPU** 是 `taskforge` 进程累计 CPU 时间（kernel+user）的差值，**内存**是每档结束后的工作集采样（峰值约 23.8 MiB）。

**这个表格说明了什么**

- 8 个准入名额 + 约 45 ms 平均上游延迟 ≈ 180 req/s 的理论容量，实测饱和点 `-c 8` 为 **191.37 req/s（1000/1000 成功）**，与高并发档的成功吞吐一致 → 数据自洽。
- 高并发的 503 **精确等于**超出名额的请求数：脚本逐档比对 `HTTP 503 数` 与网关 `admission_rejections` 计数（`reconciled=True`）。
- 对照实验：把每后端上限从 4 降到 2（名额 8→4），**同样 `-c 8`** 下成功请求从 1000/1000 变成 16/984、成功吞吐从 **191.37 降到 82.89 req/s**、503 从 0 变成 984 → 瓶颈确实来自配置的准入限制，而不是随机错误。
- 顺序请求平均端到端 46.65 ms vs 注入延迟均值 45 ms ⇒ 网关自身开销约 **1.6 ms/请求**（估算，含客户端开销）。

> 这些数字**不是** GPU 吞吐、Token/s、TTFT、生产 QPS 或 SLA。要测真实模型，需要固定模型/硬件/精度/输入输出长度/预热/并发后单独实验。

---

## 目录结构

```text
cmd/server/               服务入口（mysql build tag 分离）
cmd/mockllm/              免费合成推理上游（OpenAI Chat Completions + SSE）
cmd/loadgen/              可重复 HTTP 基准（明确不是 GPU benchmark）
internal/gateway/         多后端路由、并发准入、熔断、流式透传、指标
internal/httpapi/         REST API、认证、限流中间件、内嵌网页面板
internal/runner/          goroutine Worker Pool、取消、退避重试
internal/task/            状态机、Repository 接口、文件快照存储
internal/mysqlstore/      MySQL 事务、SKIP LOCKED 领取、租约恢复
internal/rediskit/        RESP2 迷你客户端 + Redis/Memory 限流器
internal/provider/        Mock 与 OpenAI-compatible 实现
scripts/                  Windows 冒烟 / 本地检查 / Compose / 网关 / 压测
docs/                     架构、API、网关设计、面试要点
compose.yaml              MySQL + Redis + API + 2 个 Mock 上游
Dockerfile                多阶段构建（mysql tag，非 root 运行）
```

---

## 设计取舍（面试可讲的三点）

1. **至少一次，而不是恰好一次。** 文件后端在进程崩溃后把所有 `running` 任务重新入队；MySQL 后端用租约过期恢复。两者都刻意选择 at-least-once，并用 `attempt` 号做结果隔离，丢弃过期 worker 的写入（`ErrStaleAttempt`）。代价是可能重复调用上游——对会计费的模型调用必须由调用方承担幂等责任。
2. **网关做准入而不是排队。** 名额用满时立即返回 503 + `Retry-After`，而不是无限排队拖长尾延迟。这让过载行为可预测（503 数量 = 超出名额数），代价是突发流量下成功率低；压测表里 24/1000 正是这个设计的结果，不是错误。
3. **可观测性只测真实测到的东西。** 面板和 Prometheus 暴露的是 HTTP 请求级指标（成功/失败/拒绝/in-flight/延迟分位），没有伪造 GPU 利用率或 token/s——因为网关根本不运行 kernel，它无法观测这些。

---

## 已知限制

- 单 API Key，无租户隔离；Prompt 与结果明文存储。
- 熔断器和并发计数是**进程内**状态，不是多实例共享；没有严格 half-open 单探针。
- 网关不做失败自动重放（避免非幂等生成请求重复计费）；SSE 中途断开后无法再改 HTTP 状态码，客户端需自行检查 `[DONE]`。
- 准入不排队，过载立即 503（设计选择，见上）。
- Redis 只用于限流，不是持久化队列；MySQL 用轮询而非事件驱动。
- 无数据库迁移版本管理、无指标时序存储、无任务过期清理。
- 未做生产容量评估、渗透测试、TLS 终止或多副本部署。

---

## 文档

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — 一致性、可用性与技术取舍
- [docs/API.md](docs/API.md) — 端点、错误码、请求限制
- [docs/INFERENCE_GATEWAY.md](docs/INFERENCE_GATEWAY.md) — 网关设计、指标口径、性能边界
- [docs/INTERVIEW_NOTES.md](docs/INTERVIEW_NOTES.md) / [docs/INTERVIEW_V3.md](docs/INTERVIEW_V3.md) — 面试追问与简历真实性
- [docs/CODEX_HANDOFF.md](docs/CODEX_HANDOFF.md) — 原始交接与验收清单（历史记录）
- [TEST_REPORT.md](TEST_REPORT.md)、[LOCAL_ACCEPTANCE_REPORT.md](LOCAL_ACCEPTANCE_REPORT.md)、[P4_FINAL_REPORT.md](P4_FINAL_REPORT.md) — 历史测试记录
- **[FINAL_RELEASE_REPORT.md](FINAL_RELEASE_REPORT.md)** — 最终验收：环境、命令、结果、修复项、剩余风险

## 许可

[MIT](LICENSE)。
