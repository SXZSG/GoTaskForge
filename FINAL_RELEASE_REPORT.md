# GoTaskForge 最终发布报告 — v1.0.0-portfolio

日期：2026-11-09（Asia/Shanghai）
冻结范围：工程验收、真实缺陷修复、仓库整理与 GitHub 发布

---

## 1. 结论摘要

| 项目 | 结果 |
|---|---|
| 求职展示版（Go 后端 / AI Infra 实习） | **GO — 达到可展示的工程练习标准** |
| 生产级发布 | **NO-GO — 未做容器联调、容量评估与多实例验证** |
| 仓库 | <https://github.com/SXZSG/GoTaskForge> |
| 可见性 | **Public**（发布前已完成敏感信息审查，用户确认转为公开） |
| 分支 | `main` |
| 验收快照 Commit | `cd3131b9acbf27ef9f65d3eec26702acb2344394` |
| 本报告 Commit | 追加在本报告所在的提交中；`v1.0.0-portfolio` 标签指向该提交 |
| 许可 | MIT（仓库原有 `LICENSE`，未改动） |

> 该版本是**求职展示版冻结**，不是生产发布。仓库内所有性能数字均为本机**合成 Mock HTTP** 测量，不代表 GPU 吞吐、Token/s、TTFT 或线上容量。

---

## 2. 环境与工具链

| 组件 | 实际值 |
|---|---|
| 操作系统 | Microsoft Windows 11 Pro，Build 26200 |
| CPU / 内存 | AMD Ryzen 9 8940HX，16 核 32 线程 / 15.2 GB |
| Go（本机主） | go1.27.3 windows/amd64，用户目录安装 `%LOCALAPPDATA%\Go1273`，官方 ZIP SHA256 `f26081f4…5fff` 已校验 |
| Go（CI 对齐） | go1.23.12 windows/amd64（对应 `go.mod` 的 `go 1.23`） |
| WSL | Ubuntu 22.04.5 LTS，go1.27.3 linux/amd64（用户目录非特权安装，官方 tar.gz SHA256 `65efbc80…9b40` 已校验），GCC 11.4.0 |
| 依赖 | `go.sum` 已提交；`go mod verify` → `all modules verified`；`GOPROXY=off` 离线构建通过 |
| Docker / MySQL / Redis | **未安装**，未执行任何容器联调 |
| 依赖获取 | 通过本机已配置的 HTTP 代理 `127.0.0.1:7890` 访问官方 `proxy.golang.org` 与 `sum.golang.org`。**未关闭 TLS、未设置 `GOINSECURE`/`GONOSUMDB`、未使用第三方镜像** |

---

## 3. 完整测试结果与实际命令

### 3.1 Windows（go1.27.3 windows/amd64）

| 命令 | 结果 |
|---|---|
| `go mod verify` | PASS — `all modules verified` |
| `go build ./...` | PASS |
| `go build -tags mysql ./...` | PASS（修复后，见 §4.1） |
| `go vet ./...` | PASS |
| `go vet -tags mysql ./...` | PASS |
| `go test -count=1 ./...` | PASS — 7 个有测试的包全部 ok（config / gateway / httpapi / provider / rediskit / runner / task）；另有 4 个无测试文件包（cmd/loadgen、cmd/mockllm、cmd/server、internal/mysqlstore） |
| `go test -tags mysql -count=1 ./...` | PASS |
| `gofmt -l ./cmd ./internal` | PASS（无输出） |
| `go test -count=20 ./internal/task` | PASS — 验证 §4.3 的偶发失败已消除 |

### 3.2 Windows（go1.23.12，CI 版本对齐）

| 命令 | 结果 |
|---|---|
| `go build ./...` / `go build -tags mysql ./...` | PASS |
| `go vet ./...` | PASS |
| `go test -count=1 ./...` | PASS |
| `gofmt -l ./cmd ./internal` | PASS |

结论：`.github/workflows/ci.yml` 通过 `go-version-file: go.mod` 安装 Go 1.23.x，本机用同一版本实测通过；推送后 GitHub Actions 实际运行结果为 **success**（run `42578031219`，耗时 1m2s），并非只做推断。

### 3.3 WSL Ubuntu 22.04（go1.27.3 linux/amd64，CGO_ENABLED=1，GCC 11.4）

| 命令 | 结果 |
|---|---|
| `GOPROXY=off go build ./...` | PASS（证明依赖图完全由 `go.sum` + 缓存满足，无需联网） |
| `GOPROXY=off go build -tags mysql ./...` | PASS |
| `go vet ./...` / `go vet -tags mysql ./...` | PASS |
| `go test -count=1 ./...` | PASS |
| `go test -tags mysql -count=1 ./...` | PASS |
| **`go test -race -count=1 ./...`** | **PASS** — 7 个有测试的包全部通过，含 gateway / httpapi / runner / task 并发路径 |
| `gofmt -l ./cmd ./internal` | PASS |

> 上一轮报告把 race 记为 BLOCKED。本轮在 WSL 用户目录安装官方 Go 工具链后**已实际执行并通过**，该阻塞项关闭。

### 3.4 本机 HTTP 端到端（真实进程，非单元测试）

启动方式：`bin/mockllm.exe` ×2（127.0.0.1:8091/8092，注入延迟 30/60 ms）+ `bin/taskforge.exe`（127.0.0.1:8080）。

| 检查 | 结果 |
|---|---|
| `scripts/smoke.ps1` | PASS — `SMOKE_PASS`，健康检查、幂等创建、轮询至 `succeeded`、`/metrics` |
| `scripts/smoke_gateway.ps1` | PASS — `GATEWAY_SMOKE_PASS`，2 后端、completion、统计 |
| SSE（`curl -N`，`stream=true`） | PASS — 收到 7 段 `data:` 增量块，最后 `data: [DONE]` |
| 面板 `GET /` | PASS — 10885 字节 HTML，内联 JS 通过 `node --check` |
| 认证矩阵 | PASS — 无 Key：`/`、`/healthz`、`/readyz` = 200，`/v1/jobs`、`/metrics` = 401；错误 Key = 401；正确 Key = 200 |
| 固定窗口限流 | PASS — `RATE_PER_MINUTE=5`、同一身份（API Key + IP）在窗口内前 5 次 200，第 6 次起 429 |

### 3.5 静态与供应链检查

| 检查 | 结果 |
|---|---|
| 敏感信息扫描（API Key / Token / 密码 / 私钥 / 内部 IP / 个人路径） | PASS — 仅命中两处历史报告中的 Windows 用户目录路径，已脱敏；`server_test.go` 的 `test-only-secret` 为测试常量 |
| `.env` 未提交 | 确认 — 仅 `.env.example`（占位值） |
| `git ls-files --eol` | 全部索引条目为 LF（新增 `.gitattributes`：`* text=auto eol=lf`） |
| 二进制/构建产物 | `bin/*.exe` 已删除并由 `.gitignore` 排除 |

---

## 4. 修复的真实缺陷

以下是本轮**实际复现并修复**的问题，不是"审查推测"。

### 4.1 `go.sum` 缺失导致 MySQL 构建不可复现（已修）

- **现象**：仓库没有 `go.sum`。`go mod download` 只写入 `/go.mod` 哈希，随后 `go build -tags mysql ./...` 直接失败：
  `missing go.sum entry for module providing package github.com/go-sql-driver/mysql`。
- **影响**：README 里给出的 `go build -tags mysql` 路径在干净环境不可执行；依赖完整性无法验证。
- **修复**：执行 `go mod tidy` 生成完整 `go.sum`（含 `github.com/go-sql-driver/mysql v1.9.3` 与其传递依赖 `filippo.io/edwards25519 v1.1.0` 的 `h1:` 与 `/go.mod` 记录），提交入库。
- **验证**：`go mod verify` = all modules verified；`GOPROXY=off go build -tags mysql ./...` 离线通过。

### 4.2 Dockerfile 忽略 `go.sum`（已修）

- **现象**：`Dockerfile` 只 `COPY go.mod ./` 后 `go mod download`。
- **影响**：镜像构建不校验已提交的依赖哈希，`go.sum` 形同虚设，供应链校验被绕过。
- **修复**：改为 `COPY go.mod go.sum ./`。
- **状态**：该镜像**未实际构建**（本机无 Docker），属静态修复。

### 4.3 Windows 上偶发单元测试失败（已修）

- **现象**：`go test ./internal/task` 约 1/5 概率失败：
  `testing.go:1619: TempDir RemoveAll cleanup: unlinkat …\001: The directory is not empty.`
- **定位**：失败后残留目录实际**为空**。用独立最小程序复现（仅 `os.CreateTemp` + `os.Rename` 覆盖同一目标 41 次 + `os.RemoveAll`）同样 2/60 失败 → 属 Windows 覆盖重命名后目录项处于 delete-pending 状态，与 `testing` 框架清理竞争，**不是状态机逻辑错误**。
- **修复**：`internal/task/store_test.go` 增加 `removeAllRetry`（最多 100 次 × 5 ms），在 `t.TempDir()` 自身清理之前删除目录；注释写明了成因与复现方式。未改动任何业务断言。
- **验证**：`go test -count=20 ./internal/task` 连续 20 轮通过；Windows 全量测试通过。

### 4.4 压测工具混用"完成吞吐"与"成功吞吐"（已修）

- **现象**：`cmd/loadgen` 只输出 `throughput=<total>/elapsed`，即**包含 503 拒绝**的完成速率；P4 报告把它当"吞吐"与"成功 24/1000"并列，读起来像 7,741 req/s 的服务能力。
- **修复**：`loadgen` 现在分别输出
  `throughput success_req_s=… completed_req_s=… (completed includes rejected/failed responses)`，
  并单列 `latency success_only n=<样本数> p50/p95/p99`。
- **验证**：重跑 5,000 请求，见 §5。

### 4.5 压测脚本 `-Concurrency 2,8` 被静默解析成 28（已修）

- **现象**：`powershell.exe -File scripts\perf_gateway.ps1 -Concurrency 2,8` 实际只跑了一个 `concurrency=28` 的档位。
- **原因**：`-File` 传参为字符串，`[int[]]` 绑定把 `"2,8"` 解析为 `28`。
- **修复**：参数改为 `[string]$Concurrency = '1,4,16,32,64'`，内部按逗号切分并用 `[int]::TryParse` 校验范围（1–1000），非法值直接报错而不是猜测。
- **验证**：`-Concurrency 2,8` 现在正确产生两档（47.97 / 191.37 req/s）。

### 4.6 压测脚本在"预期失败"时中断（已修）

- **现象**：`$ErrorActionPreference='Stop'` 下，loadgen 因存在 503 而退出 1，Windows PowerShell 5.1 把原生命令的 stderr 转成终止错误，脚本在第一档高并发就整体中止。
- **修复**：仅在调用 loadgen 的最小范围内临时切换 `$ErrorActionPreference='Continue'` 并捕获输出，随后恢复；新增对 429 的显式告警（提示限流器而非网关才是瓶颈）。

### 4.7 压测缺少准入对账与指标口径（已补）

- **修复**：`scripts/perf_gateway.ps1` 每档读取 `/metrics/prometheus` 前后差值，输出
  `succeeded_delta / failed_delta / admission_rejected_requests / http_503 / reconciled`，
  并记录进程 CPU 时间增量、工作集、峰值工作集。脚本头部写明六条统计口径定义。

### 4.8 P4 报告的性能表口径错误（已修）

- **修复**：`P4_FINAL_REPORT.md` 的性能章节按 §5 的口径重写（成功吞吐/完成吞吐分列、给出样本数、CPU 与内存测量方式、准入对账、对照实验），并显式标注"初版把完成吞吐写成吞吐是错误表述"。并发限制与准入策略**未做任何修改**。

### 4.9 个人绝对路径与过期清单文件（已修）

- 两份历史报告中的 `C:\Users\<用户名>\…` 已脱敏为 `%LOCALAPPDATA%\…`。
- 删除 `SOURCE_MANIFEST_SHA256.txt`：它记录的是上一轮打包时的哈希，本轮修改后已全部失效，且含乱码字符；保留会误导读者以为可以校验完整性。
- 删除 `bin/mockllm.exe`、`bin/taskforge.exe` 构建产物。

### 4.10 仓库卫生与 CI（已修）

- `.gitignore` 扩充：密钥/环境文件（`.env` `.env.*` `!.env.example` `*.pem` `*.key`）、运行数据（`/data/` `*.db` `*.sqlite*`）、构建产物（`/bin/` `*.exe` `*.so` …）、日志与临时文件、覆盖率、编辑器与 OS 噪声。
- 新增 `.gitattributes`（`* text=auto eol=lf`），保证 Docker/CI/Linux 侧拿到 LF。
- `ci.yml`：新增 `permissions: contents: read`、`go mod verify`、显式 `go build ./...`、`go vet -tags mysql ./...`。

---

## 5. 性能测试结果与统计口径

来源：`scripts/perf_gateway.ps1 -Requests 1000`，本机环回，2 个合成 Mock 上游（注入延迟 30 ms / 60 ms），`GATEWAY_MAX_PER_BACKEND=4`（2 后端 = 8 个准入名额），`RATE_PER_MINUTE=10000`，非流式。共 5 档 × 1,000 = **5,000 请求**。

### 5.1 统计口径

| 指标 | 定义 |
|---|---|
| **成功吞吐** | HTTP 200 数量 ÷ loadgen 自身测得耗时。代表真正被服务的容量 |
| **完成吞吐** | 全部响应数量 ÷ 耗时，**包含 503 准入拒绝**。只反映拒绝速度，不能当作容量 |
| **P50 / P95 / P99** | **仅对 HTTP 200 的成功样本**计算，并给出样本数 n |
| **CPU** | `taskforge` 进程累计 CPU 时间（kernel+user）在每档开始/结束时的差值；含空闲轮询；不含 loadgen 与 mock 进程 |
| **内存** | 每档结束后的工作集采样（**不是峰值**），另记进程生命周期峰值工作集 23.80–23.85 MiB |

### 5.2 主表（每档 1,000 请求）

| 并发 | 成功/失败(503) | 成功吞吐 req/s | 完成吞吐 req/s（含 503） | P50 | P95 | P99 | CPU 秒 | 工作集 MiB |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 1 | 1000/0 | 21.44 | 21.44 | 34.26 ms | 62.08 ms | 62.34 ms | 0.453 | 20.89 |
| 4 | 1000/0 | 96.29 | 96.29 | 31.67 ms | 61.80 ms | 62.18 ms | 0.359 | 20.34 |
| 16 | 24/976 | 182.46 | 7602.59 | 33.22 ms | 67.68 ms | 68.72 ms | 0.375 | 20.59 |
| 32 | 24/976 | 186.16 | 7756.69 | 34.19 ms | 66.05 ms | 66.58 ms | 0.250 | 20.89 |
| 64 | 24/976 | 181.06 | 7544.26 | 36.51 ms | 72.33 ms | 72.33 ms | 0.406 | 23.63 |

### 5.3 饱和点与对照实验（每档 1,000 请求）

| 并发 | 每后端上限 | 准入名额 | 成功/失败 | 成功吞吐 req/s | 结论 |
|---:|---:|---:|---:|---:|---|
| 2 | 4 | 8 | 1000/0 | 47.97 | 未饱和 |
| 8 | 4 | 8 | 1000/0 | **191.37** | 恰好饱和：100% 成功，与高并发档的 181–186 一致 |
| 8 | 2 | 4 | 16/984 | 82.89 | 名额减半 → 成功吞吐减半，503 精确等于名额外请求 |

### 5.4 结论与边界

1. **高并发 503 完全符合配置的准入限制**：每档 `HTTP 503 数 == 网关 admission_rejections ÷ 后端数`，脚本逐档输出 `reconciled=True`。把每后端上限从 4 降到 2（名额 8→4）后，**同样 `-c 8`** 的同一工作负载从 `1000/0`（191.37 req/s）变成 `16/984`（82.89 req/s）—— 归因由对照实验确认，不是巧合。
2. **成功吞吐与完成吞吐必须分开报**：c=16 时两者相差 41 倍。任何只引用"完成吞吐"的表述都会高估容量，这正是本轮修正的报告缺陷。
3. **容量自洽**：8 个名额 ÷ 约 45 ms 平均上游延迟 ≈ 178 req/s 理论值，实测饱和点 191.37 req/s。
4. **网关自身开销**：c=1 平均端到端 46.65 ms vs 注入延迟均值 45 ms ⇒ 约 1.6 ms/请求（估算，含客户端开销）。
5. **样本量限制**：高并发档只有 24 个成功样本，P99 接近最大值，仅作观察。
6. **不构成任何 GPU / 模型性能结论**：上游是返回合成文本的 Mock HTTP 服务，不运行任何模型或 CUDA kernel。

---

## 6. 未完成的验证及原因

| 项目 | 状态 | 原因 / 影响 |
|---|---|---|
| Docker Compose 启动 | 未执行 | 本机未安装 Docker Desktop/Engine。`compose.yaml`、`Dockerfile` 仅做静态审查（本轮修复了 `go.sum` 未复制的问题） |
| MySQL 8.4 / Redis 7.4 实际联调 | 未执行 | 同上；`mysql`/`integration` build tag 可编译但未连真实服务 |
| `go test -tags 'mysql integration'` | 未执行 | 需要 `MYSQL_DSN` / `REDIS_URL` 指向真实实例 |
| Windows 原生 `-race` | 不可用 | Windows 无 C 工具链（CGO_ENABLED=0）。已改用 WSL Ubuntu 22.04 + GCC 11.4 **实际执行并通过**，未把工具链缺失写成"测试通过" |
| 真实 vLLM / SGLang / GPU 推理 | 未执行 | 无可用 GPU 推理服务；不产出 Token/s、TTFT、CUDA 相关数字 |
| 多实例共享熔断/并发状态、跨实例取消 | 未验证 | 熔断器与并发计数为进程内状态，代码与文档均标注为设计边界 |
| 生产容量 / 渗透测试 / TLS 终止 | 未执行 | 超出求职展示版范围 |

---

## 7. GitHub 仓库状态

- **URL**：<https://github.com/SXZSG/GoTaskForge>
- **可见性**：Public（用户确认；公开前已完成密钥/路径/数据审查）
- **默认分支**：`main`
- **验收快照 Commit**：`cd3131b9acbf27ef9f65d3eec26702acb2344394`
- **远端一致性**：`git ls-remote origin refs/heads/main` 返回同 SHA，本地工作树 clean
- **Tag**：`v1.0.0-portfolio`（annotated，明确标注为求职展示版，**非生产发布**）
- **仓库描述**：`Go async task service + OpenAI-compatible LLM inference gateway (portfolio)`
- **Topics**：go, golang, llm, inference-gateway, openai, async-tasks, worker-pool, prometheus, mysql, redis, portfolio-project
- **历史**：仓库为本次新建，仅包含本项目提交；未强推、未改写他人历史、未删除任何分支
- **推送内容**：源码、单元测试、文档、脚本、CI 工作流、Compose 编排、Dockerfile、`go.sum`、`.gitattributes`

---

## 8. 项目当前工程质量判断

**作为求职作品：GO。**

- 结构清晰：`cmd/` 可执行入口与 `internal/` 领域包分离，MySQL 驱动通过 build tag 隔离，文件模式零外部依赖即可运行。
- 并发正确性有实际保障：`-race` 在真实 Linux 工具链下通过；任务状态机、取消、重试、幂等、过期 attempt 隔离都有测试覆盖。
- 工程闭环完整：`go.sum` + `GOPROXY=off` 证明依赖可复现；CI 版本（Go 1.23）在本机实测通过；冒烟脚本、压测脚本、面板、Prometheus 指标都能真实跑起来。
- 数据诚实：性能表分列成功/完成吞吐、给出样本数与测量方式，并配有准入对账和对照实验。

**作为生产系统：NO-GO。** 未做容器与真实数据库联调、未做容量评估与多实例一致性验证、单 API Key 无租户隔离、Prompt 与结果明文存储。

---

## 9. 三条可用于简历的技术亮点（均可在仓库中验证）

1. **异步任务一致性设计**：用文件快照（临时文件 + `rename` 原子替换）与 MySQL 事务 + `SELECT … FOR UPDATE SKIP LOCKED` 两套存储实现同一套 `Repository` 接口，统一提供幂等提交、指数退避重试、运行中取消与崩溃后 `running → queued` 重入队，并用 attempt 号丢弃过期 worker 的写入（`ErrStaleAttempt`），明确选择 at-least-once 语义而非虚假的 exactly-once。
2. **LLM 网关的准入与故障隔离**：实现 least-inflight 路由 + 每后端并发上限 + 被动熔断（阈值 + 冷却）与 SSE 逐块透传；用 5,000 请求实测把过载行为量化到"503 数 = 超出准入名额数"，并用**同一 `-c 8` 下把每后端上限从 4 降到 2** 的对照实验（成功吞吐 191.37 → 82.89 req/s，成功数 1000/1000 → 16/984）证明瓶颈来源，而不是只给一个吞吐数字。
3. **可复现的工程验证**：补齐 `go.sum` 并用 `GOPROXY=off` 离线构建证明依赖图完全锁定；在 WSL Ubuntu 22.04 + GCC 11.4 下跑通 `go test -race`（此前被记录为阻塞项）；同时用 Go 1.23.12 复验 CI 声明的版本，避免"CI 写法与实际工具链不符"。

---

## 10. 冻结声明

- 本项目自本报告起冻结为 **v1.0.0-portfolio**，仅做必要维护（安全问题、明显缺陷、文档更正）。
- 不新增功能，不追加大规模重构，不补充未经实测的性能或分布式能力描述。
- 未验证范围（Docker/MySQL/Redis 联调、真实模型服务、多实例一致性）在 README 与本文档中均已明确列出，后续不得在简历或介绍中表述为已完成。
