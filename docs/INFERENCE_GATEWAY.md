# AI 推理网关 v3：架构和边界

> 本项目是 **AI Infra 的服务层项目**，不是 CUDA 推理引擎。上游可以接 vLLM/SGLang 等具有 OpenAI Chat Completions 接口的服务，但**目前未在本环境内用真实 vLLM/SGLang 验证**。默认的 `cmd/mockllm` 只产生合成回复，绝不代表真实模型的 Token 速度、算子优化或推理质量。

## 为什么要做

一次大模型调用往往比普通 HTTP API 更长，占据较多资源。Go 网关负责：请求准入、固定后端池的路由、限制单实例并发、上游超时及故障隔离、流式输出透传、请求层可观测性。底层模型运行在另一个进程/容器中；Go 本身不运行 GPU Kernel。

```text
                  +-----------------------------+
Browser / client--| GoTaskForge API / Auth / RL  |
                  +-------------+---------------+
                                |
               /v1/chat/completions (sync or SSE)
                                |
                  +-------------v---------------+
                  | inference gateway            |
                  | least-inflight, per-node cap |
                  | breaker, timeout, stats      |
                  +-------------+---------------+
                      /                     \
        +-------------v----+        +-------v--------------+
        | mock-a / vLLM A  |        | mock-b / vLLM B      |
        | HTTP upstream    |        | HTTP upstream        |
        +------------------+        +----------------------+

Other path: POST /v1/jobs -> persistent store -> Worker Pool -> Provider
```

## 核心行为

1. **固定上游列表**：只读取服务管理员配置的 `GATEWAY_BACKENDS`（逗号分隔的 `/v1` base URL），不会信任请求体里提供的任意 URL；默认只允许 HTTPS 或本机 HTTP。Docker 演示中的内部 HTTP 需显式设置 `GATEWAY_ALLOW_HTTP=true`。禁止把这种演示配置直接开放到公网。
2. **路由与准入**：从不在熔断状态且未到并发上限的节点中挑选最少 inflight，平手轮转。所有节点不可用返回 503 和 `Retry-After: 1`。
3. **失败熔断**：HTTP 429、5xx、网络错误和上游超时算作后端不健康；达到阈值后冷却一段时间；过期再允许请求。业务 400/401 不触发熔断。客户端取消不触发熔断。
4. **流式**：`stream=true` 时逐块转发上游 `text/event-stream` 并 Flush，不把整段生成结果缓存在网关。`stream=false` 的成功响应最多读取 8 MiB，避免无声截断。
5. **认证**：客户端使用 `X-API-Key` 访问网关（非本机访问必须配置）。给上游使用独立的 `GATEWAY_UPSTREAM_KEY`，不会直接转发用户提供的 `Authorization`。上游重定向被禁止，避免令牌越界传递。请勿提交 `.env`。
6. **观测**：`GET /v1/gateway/backends` 返回请求、错误、熔断、并发、最近 256 个已完成请求的 P50/P95 HTTP 延迟。`GET /metrics/prometheus` 提供请求总数/成功/失败/并发/熔断等指标。只测 HTTP 请求，不采集真实 GPU 利用率、TTFT 或 token/s。

## 接口

| Method | Path | Description |
|---|---|---|
| POST | `/v1/chat/completions` | 兼容常见 OpenAI Chat Completions JSON 请求，支持 `stream=true` SSE |
| GET | `/v1/gateway/backends` | 节点状态、完成请求延迟分位数 |
| GET | `/metrics/prometheus` | 网关指标（Prometheus exposition text） |
| POST | `/v1/jobs` | 原有异步任务提交，与新网关相互独立 |
| GET | `/` | 浏览器管理面板：异步任务及推理网关实验区 |

示例：

```powershell
$body = @{model='local-demo';messages=@(@{role='user';content='请解释 least-inflight 调度'});stream=$false}|ConvertTo-Json -Depth 5
Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:8080/v1/chat/completions' -ContentType 'application/json' -Body $body
Invoke-RestMethod 'http://127.0.0.1:8080/v1/gateway/backends'
```

## 性能与公平实验

1. 先运行 `scripts/dev_gateway.ps1`，它启动两个本地模拟推理节点和网关。
2. 在另一终端执行 `go run ./cmd/loadgen -n 20 -c 4`，记录成功数、HTTP 状态分布、端到端 p50/p95 延迟、completed req/s。
3. 改变 `-c 1,2,4,8`，观察后端 `in_flight` 与拒绝数；提高负载前注意 API 每分钟限流上限（默认 60）。
4. 需要对 vLLM/SGLang 做真实性能测试时，固定模型、硬件、模型精度、输入/输出 Token 长度、预热、并发、采样设置。令每组多次重复，记录吞吐、TTFT、ITL、P95、显存占用及置信区间。**当前工具本身并不测量这些模型级指标。**

## 明确的非目标 / 不足

- 没有真正进行 vLLM/SGLang 安装与兼容性测试，也没有运行 CUDA 或 Triton Kernel。
- 没有多租户隔离、分布式路由状态同步、服务发现、TLS 终止、模型热加载、按 Token 的配额、请求级持久性或跨实例严格限流（Redis 只限客户端 API 调用次数）。
- 熔断器在单个 Go 进程内，未和其他实例共享；熔断后冷却没有严格单探针 Half-Open 状态。
- **不透明自动重试**：为了避免同一个非幂等生成请求重复计费，网关不做失败自动重放。原异步作业有自己的可配置重试策略，两者不能混为一谈。
- 已经开始发送的 SSE 在上游中途断开时无法再改变 HTTP 状态码为 502；客户端要检测事件流是否完整收到 `[DONE]`。
- 已测的是开发环境正确性；没有达到可直接暴露公网的安全标准，未做生产容量评估。
