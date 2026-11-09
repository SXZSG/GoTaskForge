# GoTaskForge v3 本地验收报告 — 2026-11-09

> **历史记录（P3 轮）。** 本文件记录的是最终验收轮之前的状态。它列出的阻塞项
> （`go mod download` 失败、race 未执行、Docker/MySQL/Redis 未联调）中，前两项已在
> 同日的最终轮解决；权威、最新的验收结论请看 `FINAL_RELEASE_REPORT.md`。

## 环境

- Windows 10 Pro（Build 26200），AMD Ryzen 9 8940HX；Hyper-V 已检测到 hypervisor。
- Go：Go 1.27.3 windows/amd64，官方 `go1.27.3.windows-amd64.zip`，SHA256 `f26081f438319757f8a1b46d7d0b2c5ec7583d4038e485e0aa725cfde7885fff`。
- Go 安装位置：`%LOCALAPPDATA%\Go1273`（用户目录，未使用管理员 MSI）。
- `CGO_ENABLED=0`；Windows PATH 中没有 gcc/clang。WSL Ubuntu 22.04 已存在，WSL 内有 GCC 11.4，但没有 Go。
- Docker Desktop/Docker CLI 未安装。未启用或修改 Windows 功能，未重启系统。
- 未调用付费模型 API；所有推理验收均使用本机 Mock 上游。

## 命令与结果

| 类别 | 实际命令 | 结果 |
|---|---|---|
| Go 环境 | `go version`; `go env GOVERSION CGO_ENABLED GOOS GOARCH GOPATH GOMOD` | PASS |
| 依赖下载 | `go mod download` | BLOCKED：连接 `proxy.golang.org` 超时/失败；未伪报通过 |
| 编译 | `go build ./...` | PASS |
| 全量测试 | `go test -count=1 ./...` | PASS |
| 静态检查 | `go vet ./...` | PASS |
| 本地脚本 | `powershell -ExecutionPolicy Bypass -File scripts/test_local.ps1` | 部分通过：格式、单测、静态分析通过；race 因 CGO/C 编译器缺失失败 |
| Race | `go test -race -count=1 ./...` | SKIP/BLOCKED：Go 明确报 `-race requires cgo; enable cgo` |
| 网关启动 | `scripts/dev_gateway.ps1` | PASS：两个本机 Mock 节点 + Go 网关启动 |
| 网关冒烟 | `scripts/smoke_gateway.ps1` | PASS：双后端、completion、统计 |
| 压测 | `go run ./cmd/loadgen -n 20 -c 4` | PASS：20/20 成功，79.50 completed req/s，p50 32.20ms，p95 66.44ms |
| SSE | `curl.exe -N ... stream=true` | PASS：逐块 `data:` 输出并收到 `[DONE]` |
| Compose 语法 | Python YAML 解析 | SKIP：当前 Python 无 PyYAML；Docker Compose 未安装 |
| Docker/MySQL/Redis | Docker Compose + 集成测试 | SKIP：Docker Desktop、Docker Engine、Compose 均不可用 |

压测数据只表示本机环回 Go 网关与合成 Mock 的 HTTP 行为，不是 GPU 吞吐、Token/s、TTFT、生产 QPS 或 SLA。

## 代码问题与修复

- 本轮未发现可复现的代码缺陷，因此没有修改业务代码。
- 生成了本地构建产物 `bin/mockllm.exe`、`bin/taskforge.exe`，用于本轮网关验收。
- 未生成或记录任何真实 API Key、密码或外部服务凭据。

## 已验证的工程能力

- 文件存储模式、异步任务服务、网关双 Mock 后端启动。
- 双后端路由、完成请求统计、SSE 增量转发和 `[DONE]` 完整结束标记。
- 20 请求/4 并发的本机合成压测。
- 单元测试覆盖配置、网关、HTTP API、Provider、Redis 工具、Runner、任务状态机。

## 剩余问题与阻塞

1. `go mod download` 受当前网络到 `proxy.golang.org` 的连接限制，未生成 `go.sum`，MySQL tagged build 未执行。
2. Windows race 需要 CGO=1 与兼容 C 编译器；当前没有 gcc/clang。可安装 WinLibs/LLVM-MinGW，或在 WSL 内安装 Go 后执行 race，但这属于额外环境变更，本轮未擅自安装。
3. Docker Desktop、MySQL 8.4、Redis 7.4、Compose 联调和 `integration` build tag 未执行。
4. 尚未完成多进程/多节点共享熔断状态、真实数据库租约恢复和跨实例取消验证。
5. 当前压测样本很小，仅作可重复冒烟，不代表容量结论。

## 求职作品质量判断

作为 Go 后端实习作品：**达到可展示的工程练习质量**。它有清晰模块边界、异步任务状态机、文件持久化、可选 MySQL/Redis、网关路由、熔断、SSE、Mock 上游、测试和可重复脚本。

作为生产系统：**尚未达到**。Docker/MySQL/Redis 集成、race、网络依赖下载、跨实例一致性和更大规模压测仍需完成；简历中只能写已实际验证的本机 Mock 网关和离线 Go 测试结果，不能写生产 QPS、真实模型吞吐或已完成的数据库联调。
