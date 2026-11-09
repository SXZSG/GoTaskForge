# GoTaskForge v3 P4 最终工程验收

> **Historical record (P4 round).** The performance table below was corrected
> during the final round; the race/`go.sum` blockers were also resolved later
> the same day. See `FINAL_RELEASE_REPORT.md` for the authoritative final state.

日期：2026-11-09（Asia/Shanghai）

## 环境

- Windows 10 Pro Build 26200，AMD Ryzen 9 8940HX，Hyper-V 已检测到 hypervisor。
- Go 1.27.3 windows/amd64，安装于用户目录 `%LOCALAPPDATA%\Go1273`（官方 ZIP，SHA256 `f26081f4…5fff` 已校验）。
- `CGO_ENABLED=0`；Windows 未发现 gcc/clang。WSL Ubuntu 22.04 有 GCC 11.4，但 WSL 内没有 Go。
- Docker Desktop、Docker CLI、MySQL、Redis 未安装。
- 未调用付费模型 API；推理测试仅使用本机 Mock 节点。

## 已执行命令

- `go version`、`go env GOVERSION CGO_ENABLED GOOS GOARCH GOPATH GOMOD`：PASS。
- `go mod download`：BLOCKED，当前环境连接 `proxy.golang.org` 失败；未伪报通过。
- `go fmt ./...`：PASS。
- `go build ./...`：PASS。
- `go test -count=1 ./...`：PASS。
- `go vet ./...`：PASS。
- `go test -race -count=1 ./...`：BLOCKED，Go 报 `-race requires cgo; enable cgo`。未将工具链问题归因于代码。
- `powershell -ExecutionPolicy Bypass -File scripts/test_local.ps1`：格式、普通测试、静态分析通过；race 阶段同样因 C 编译器缺失停止。
- `scripts/dev_gateway.ps1` + `scripts/smoke_gateway.ps1`：PASS；两个 Mock 后端、Completion、网关统计正常。
- SSE 手工请求：PASS；收到多段 `data:`，最终收到 `data: [DONE]`。
- `scripts/perf_gateway.ps1 -Requests 1000`：完成 5 组各 1,000 请求，覆盖并发 1、4、16、32、64。

## 性能结果

> **2026-11-09 最终轮修正**：本节初版把“完成吞吐”（所有响应数 / 耗时，包含被准入拒绝的 503）写成了“吞吐”，与旁边“成功 24/1000”并列，容易读成 7,741 req/s 的服务能力。这是错误的表述，已按下面的口径重测、重写；并发限制与准入策略未做任何修改。

测试设置：本机环回 Go 网关（`bin/taskforge.exe`），两个合成 Mock 上游 `mock-a`（30 ms 注入延迟）与 `mock-b`（60 ms），非流式请求，`GATEWAY_MAX_PER_BACKEND=4`（2 后端 = 8 个准入名额），`RATE_PER_MINUTE=10000`（避免限流器成为瓶颈），`GATEWAY_TIMEOUT_SEC=90`。

统计口径：

- **成功吞吐** = HTTP 200 数量 / loadgen 自身测得的耗时，代表真正被服务的容量。
- **完成吞吐** = 所有响应数量 / 耗时，包含 503 准入拒绝；它不是容量指标，单独使用会高估几十倍。
- **P50/P95/P99** 只对 HTTP 200 的成功样本计算，并给出样本数 n；`-n 1000` 时高并发组只有 24 个成功样本，分位数仅供观察。
- **CPU** 为 `taskforge` 进程在每一档开始/结束时读取的累计 CPU 时间（kernel+user）差值，包含空闲轮询，不含 loadgen 与 mock 进程。
- **内存** 为每档结束后的工作集采样（不是峰值），另记进程生命周期峰值工作集 23.80–23.85 MiB。

主表（每档 1,000 请求，合计 5,000 请求）：

| 并发 | 请求 | 成功/失败(503) | 成功吞吐 req/s | 完成吞吐 req/s（含 503） | P50 | P95 | P99 | CPU 秒 | 工作集 MiB |
|------:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 1 | 1000 | 1000/0 | 21.44 | 21.44 | 34.26 ms | 62.08 ms | 62.34 ms | 0.453 | 20.89 |
| 4 | 1000 | 1000/0 | 96.29 | 96.29 | 31.67 ms | 61.80 ms | 62.18 ms | 0.359 | 20.34 |
| 16 | 1000 | 24/976 | 182.46 | 7602.59 | 33.22 ms | 67.68 ms | 68.72 ms | 0.375 | 20.59 |
| 32 | 1000 | 24/976 | 186.16 | 7756.69 | 34.19 ms | 66.05 ms | 66.58 ms | 0.250 | 20.89 |
| 64 | 1000 | 24/976 | 181.06 | 7544.26 | 36.51 ms | 72.33 ms | 72.33 ms | 0.406 | 23.63 |

准入限制与饱和点验证（每档 1,000 请求）：

| 并发 | 每后端上限 | 准入名额 | 成功/失败 | 成功吞吐 req/s | 说明 |
|------:|---:|---:|---:|---:|---|
| 2 | 4 | 8 | 1000/0 | 47.97 | 未饱和 |
| 8 | 4 | 8 | 1000/0 | 191.37 | 恰好饱和：100% 成功，成功吞吐与高并发组的 181–186 一致 |
| 8 | 2 | 4 | 16/984 | 82.89 | 对照实验：名额减半，成功吞吐随之减半，503 精确等于名额外请求 |

结论与边界：

- 503 全部来自配置的准入限制，不是随机错误：每档 `HTTP 503 数 == 网关 admission_rejections 计数 ÷ 后端数`（脚本逐档校验并输出 `reconciled=True`）；把 `GATEWAY_MAX_PER_BACKEND` 从 4 改成 2，同一并发下 503 由 976 变为 984、成功吞吐由 186 降到 83。
- 准入是**快速失败**（不排队）：8 个名额被占满后其余请求立即 503。因此 c≥16 时“完成吞吐”只反映拒绝速度，真实容量是约 190 req/s；报告任何数字时必须写清是哪一种。
- 顺序请求 (c=1) 的平均端到端 46.65 ms，而 Mock 注入延迟均值为 (30+60)/2 = 45 ms，可推算网关自身开销约 1.6 ms/请求（估算值，含 loadgen 客户端开销）。
- 以上全部是**本机 Mock HTTP** 结果，不是 GPU 推理吞吐、Token/s、TTFT、生产 QPS 或 SLA。真实模型需单独固定模型/硬件/精度/输入输出长度后测量。

## 可靠性覆盖

现有自动化测试已覆盖：文件任务幂等冲突、重试边界、取消和重启恢复；Runner 并发执行与取消；网关 least-inflight、并发拒绝、熔断与冷却恢复、请求超时、客户端取消释放容量；SSE 转发；Provider 429/5xx 重试；输入限制和认证隔离。`internal/mysqlstore/integration_test.go` 与 `internal/rediskit/integration_test.go` 已存在，但本轮因 Docker/服务未安装未执行。

多实例共享数据库/Redis 竞争、MySQL 租约恢复、Redis 故障闭环和真实容器重启仍属于待执行集成项。网关熔断器和并发计数是单进程状态，项目文档已明确该限制。

## 本轮代码变更

1. `cmd/loadgen/main.go` 增加 P99 成功请求延迟输出。
2. 新增 `scripts/perf_gateway.ps1`，可在 Windows 重复执行 1/4/16/32/64 并发、记录 P50/P95/P99、CPU 时间和工作集。
3. README 增加性能脚本使用说明，并明确 Mock HTTP 指标的边界。

修改后已重新执行 `go fmt ./...`、`go build ./...`、`go test -count=1 ./...`、`go vet ./...`，全部通过。

## 阻塞项与授权边界

- 安装 MinGW/LLVM-MinGW 或在 WSL 安装 Go 可完成 race，但属于额外软件安装；本轮未擅自执行。
- Docker Desktop、WSL 功能启用、管理员权限和系统重启未执行；这些操作需用户明确授权。
- `go mod download` 的网络失败仍需可访问 Go module proxy 或预置模块缓存。

## 最终判断

**求职作品：GO。** 已具备可展示的 Go 后端/AI Infra 工程结构、异步任务状态机、幂等、重试、SSE、熔断、Mock 网关、自动化测试和可重复性能脚本。

**冻结为生产意义上的 v1.0：NO-GO。** Race、MySQL/Redis/Docker 集成、跨实例故障恢复和依赖可复现性尚未完成；冻结前应先补齐这些环境验证，并保留真实日志和依赖校验记录。
