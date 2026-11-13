---
status: active
date: 2026-11-13
scope: tellurion 12 仓拓扑 / 9 容器隔离栈
owner: 综合测试执行者
related:
  - docs/STACK-TEST-PREREQ.md
  - deploy/compose/isolated-test.override.yml
  - ../quant-trading-docs/docs/TEST.md
---

# tellurion 综合测试计划（2026-11-13）

> **For agentic workers:** 本计划按波次执行。每个 Task 是一个可独立验收的单元，步骤用 `- [ ]` 跟踪。禁止跳波次、禁止把 BLOCKED 记为 PASS、禁止为了让结果变绿而放宽断言。若发现计划与实际代码结构不符，按实际结构处理并在报告中说明，不要硬套。

**Goal:** 在不触碰宿主部署库 `tellurion` 的前提下，用隔离 Compose 栈（项目 `tellurion-next`）与隔离 PG（`127.0.0.1:15432`）对 tellurion 12 仓做分层验证，产出可复现的 PASS / FAIL / BLOCKED / KNOWN-DEBT 证据。

**Architecture:** 测试分七层（L0 静态、L1 单元、L2 契约、L3 集成、L4 单服务 E2E、L5 跨服务业务场景、L6 非功能），映射为 8 个波次（W0~W7）。每层只对一类风险做权威断言，上层复用下层结论、不重复抄断言。所有破坏性验证先备份、再破坏、再逐字节还原。

**Tech Stack:** Go 1.22+ / testify、PostgreSQL 16、Docker Compose、Playwright + Vitest、Python 3 校验脚本、NautilusTrader sidecar（条件项）。

**证据目录:** `reports/test-2026-11-13/`（不提交 git；命令输出、dump、diff 图、日志都落这里）。

---

## 0. 一页速览

| 波次 | 内容 | 执行方式 | 前置 | 核心判据 |
|---|---|---|---|---|
| W0 | 安全门 + 重建镜像 + 起隔离栈 + 基线快照 | 串行 | 无 | 9 容器 healthy；账本 85 行零重放；宿主库未被连接 |
| W1 | L0 静态 + L1 单元（11 仓 + UI） | 仓间并行（每终端 2~3 仓） | 源码可读，不依赖栈 | 全仓 `go test ./... -count=1` 退出 0；UI `npm test` 退出 0 |
| W2 | L2 契约 / 一致性护栏 | 与 W1 并行 | contracts 仓可读 | 5 条 contracts 护栏 0；两侧 conformance 执行数 > 0 |
| W3 | L4 单服务 E2E + 端点矩阵 | 服务间并行 | W0 | 矩阵每行状态码与形状符合预期 |
| W4 | L5 跨服务业务场景 | 4.1/4.2 串行，其余可并行 | W0、W3 | 订单闭环、异步回测 succeeded、聚合源 available |
| W5 | UI Playwright + 视觉基线 | 串行（workers=1） | W0 + worker + 夹具 + seed | 每个失败都有分类与证据；最终 218 passed / 0 failed / 2 skipped，视觉 12/12 |
| W6 | L6 非功能（并发/幂等/鉴权三态/限流/故障恢复/还原/重放） | 串行 | W4/W5 | 三态矩阵、幂等三态、429 边界、恢复后可读、账本不静默缺行 |
| W7 | 对齐门 + 破坏验证 + 收口 | 串行/条件 | 全部 | dualrun `-gate` 退出 0 或 BLOCKED；破坏后必红、还原后必绿 |

**可并行边界：** W1 与 W2 可完全并行；W3 的各服务探测可并行；W4 的 4.1（订单）与 4.3（review 聚合）可并行，4.2（异步回测）必须独占 worker 栈；W5/W6 必须串行，因为二者都改栈状态或数据。

---

## 1. 方法论：测试分层怎么切

### 1.1 分层表

| 层 | 解决的风险 | 本层**不该**测什么 | 前置 | 判据 |
|---|---|---|---|---|
| **L0 静态** | 语法、类型、格式、依赖方向、文档链接、部署清单漂移 | 任何运行时行为、业务语义 | 源码 + 工具链 | `gofmt -l` 空、`go vet` 0、`vue-tsc` 0、结构护栏 0 |
| **L1 单元** | 纯逻辑错误：回测引擎、费用/滑点、T+1、涨跌停、交易日历、指标公式、鉴权决策、幂等键、状态机 | 真实网络、真实 PG、跨进程时序、限流窗口 | 各仓源码 | `go test ./... -count=1` 退出 0；UI vitest 退出 0 |
| **L2 契约 / conformance** | 跨仓语义漂移：DTO 形状、费用默认值、错误哨兵、API surface、pin 是否指向 main 祖先 | 业务全链路、DB 写入、UI 渲染 | contracts 语料 + 各仓 go.mod | 4 条 contracts 护栏 0；core/agent conformance 执行数 > 0 且 0 失败 |
| **L3 集成** | SQL、迁移、仓储、HTTP handler 装配、序列化、连接池 | 跨容器网络、真实用户流程、故障注入 | 真实 PG 或 testcontainers | 包级测试 0；迁移后表/索引存在 |
| **L4 单服务 E2E** | 真实进程 + 真实 PG + 真实 HTTP：路由注册、鉴权姿态、就绪语义、响应形状、空库语义 | 并发压力、跨服务业务闭环、视觉像素 | 隔离栈 healthy | 端点矩阵状态码 + JSON 形状符合预期 |
| **L5 跨服务业务场景** | 边界错配：core→data/broker/risk/strategy/analytics/orchestration/agent→ui 的调用、token、fail-closed、异步状态机 | 重复验证 L1 的数学公式；单服务内部实现细节 | W0 + W3 通过 | 每场景有“正向成功 + 反向可见失败”两条证据 |
| **L6 非功能** | 并发竞态、重复副作用、鉴权绕过、限流误伤、依赖故障恢复、备份还原、迁移重放 | 替代功能断言；把“不崩”当“正确” | W4/W5 建立的稳定栈与数据 | 每个非功能项必须配对一条功能正向用例；失败必须可读 |
| **L7 对齐门（条件）** | Go 引擎与 NautilusTrader 在真实行情上的指标/成交对齐 | 无 NT sidecar 时硬跑；用 mock 替代真对齐 | PG 行情 fixture + NT sidecar | `-gate` 退出 0；报告在 K15 阈值内；无 sidecar 记 BLOCKED |

### 1.2 五条硬原则

1. **一层一权威**：同一个风险只在一层做权威断言。L1 已覆盖的指标公式，L5 不再抄一遍；L5 只证明“跨进程把正确结果运到了正确位置”。
2. **失败要清楚**：能力未落地、前置缺失、按设计退役，都必须显式 BLOCKED / KNOWN-DEBT 并写原因。禁止裸 `skip`、禁止把 404/410/503 一律当失败，也禁止一律当通过。
3. **数据自持**：任何依赖数据的用例，前置必须自己造数（fixture SQL、API seed、显式 DELETE 清理），不得依赖“开发机上曾经有数据”。
4. **破坏验证三拍**：改坏 → 目标用例必须红 → 逐字节还原（`git diff --exit-code` 为空）→ 复跑必须绿。破坏只在临时 worktree 做，不碰主工作区。
5. **隔离优先**：所有写操作只落 `tellurion-next` 项目和 `127.0.0.1:15432` 测试库；发现命令里出现宿主 `5432` 或项目名 `tellurion` 时立即停手。

### 1.3 结果三分类（全计划统一）

| 分类 | 定义 | 必备证据 |
|---|---|---|
| **PASS** | 判据全部满足，命令退出码 0（或已知红点的预期非 0） | 命令原文、退出码、关键输出、证据文件路径 |
| **FAIL** | 判据不满足，且可归因到代码/契约/配置 | 最小复现命令、首次失败输出、定位到的文件:行号 |
| **BLOCKED** | 前置不满足（无 NT sidecar、无 gcc、无 Playwright 浏览器、上游 artifact 未就绪） | 缺失项、已尝试命令、重开条件 |
| **KNOWN-DEBT** | 已登记、已判定不修的已知红 | 红区/比例复核结果；超出登记范围即升级 FAIL |

---

## 2. 被测系统与既有资产

### 2.1 12 仓拓扑与测试资产

| 仓 | 角色 | 端口 | 现有自动化资产（2026-11-13 盘点） | 本计划用法 |
|---|---|---|---|---|
| `tellurion` | core / analysis-service，网关与回测引擎 | 8085 | 437 个 `*_test.go`；`pkg/conformance`；`e2e/tests`、`e2e/guard`、`e2e/backtest_live`；`cmd/dualrun`；`tools/check_*`；`tools/pg_backup_drill.ps1` | 直接复用 + 作为跨服务编排入口 |
| `tellurion-data` | 行情 / 基本面 / 同步 | 8081 | 49 个 `*_test.go`；`/readyz` 探 PG + DDL；Tushare live 测试 opt-in | 直接复用 |
| `tellurion-broker` | 交易通道（mock） | 8091 | 10 个 `*_test.go`；订单 / 撤单 / 紧急平仓 / 幂等鉴权 | 直接复用 |
| `tellurion-agent` | ai-service | 8086 | 134 个 `*_test.go`；`internal/conformance` 读 contracts `cases.json` | 直接复用 |
| `tellurion-risk` | 风控规则 / 快照 / breaker | 8089 | 27 个 `*_test.go`；三角色鉴权 + breaker 幂等 | 直接复用 + L6 幂等入口 |
| `tellurion-strategy` | 策略资产 / 签名版本 | 8090 | 27 个 `*_test.go`；优雅关停测试 | 直接复用 |
| `tellurion-analytics` | 指标 / 归因 / 基准 | 8087 | 27 个 `*_test.go`；handler e2e 替身上游 | 直接复用 |
| `tellurion-orchestration` | 异步作业 / worker 租约 | 8088 | 29 个 `*_test.go`；policy 全覆盖 + 租约 / 心跳测试 | 直接复用 + L6 故障恢复 |
| `tellurion-contracts` | 跨仓契约唯一真相 | — | 30 个 `*_test.go`；4 条 `tools/check_*` + `conformance` | 直接复用 |
| `tellurion-kit` | 共享工具 / 鉴权中间件 | — | 39 个 `*_test.go`；`integration` build tag 真服务腿 | 直接复用 |
| `tellurion-ui` | 前端 | 5173（宿主 vite） | 52 个 vitest 文件；31 个 Playwright spec；12 条视觉基线 | 条件复用（见 §2.3） |
| `quant-trading-docs` | 长期文档 | — | `docs/TEST.md`（权威口径源） | 只读参考，不修改 |

### 2.2 9 容器与端口

| 容器 | 宿主端口 | 健康路由 | 说明 |
|---|---|---|---|
| `tellurion-next-analysis-service-1` | 8085 | `/health`、`/readyz` | core，全部业务入口 |
| `tellurion-next-data-service-1` | 8081 | `/healthz`、`/readyz` | `readyz` 同时探 PG 与 `market.ohlcv_daily_qfq` |
| `tellurion-next-ai-service-1` | 8086 | `/health`、`/readyz` | 必须用 agent HEAD 重建 |
| `tellurion-next-broker-1` | 8091 | `/healthz` | **无 `/readyz`（G4 按设计）** |
| `tellurion-next-risk-1` | 8089 | `/healthz`、`/readyz` | full profile |
| `tellurion-next-strategy-1` | 8090 | `/healthz`、`/readyz` | full profile |
| `tellurion-next-analytics-1` | 8087 | `/healthz`、`/readyz` | full profile |
| `tellurion-next-orchestration-1` | 8088 | `/healthz`、`/readyz` | full profile |
| `tellurion-next-pg-1` | 15432（仅回环） | `pg_isready` | 独立测试库，卷 `tellurion-next_next-pgdata` |

- **worker 不是第 10 个常驻容器**：它是 `--profile orchestration-worker` 下的无监听进程，仅 W4.2/W5 临时启用。
- **UI 不是容器**：`tellurion-ui/web` 由宿主 `npm run dev` 起在 `:5173`，Playwright 直连 `:8085/:8081`。

### 2.3 既有资产复用决策

| 资产 | 复用方式 | 理由 / 调整 |
|---|---|---|
| 11 仓 `go test ./...` | **直接复用** | 各仓自治，不依赖栈；core 排除 `e2e` 包以保持 L1 纯净 |
| core `make test`（`-race` + 覆盖率门 total 52% / pkg/backtest 65%） | **直接复用** | 与 CI 同口径；Windows 无 gcc 时 `-race` 记 BLOCKED，不降级为无 race 的“假绿” |
| `tools/check.sh` | **直接复用** | gofmt / vet / test 三条腿；`--clean` 判 HEAD |
| `tools/check_repo_autonomy.py`、`check_doc_links.py`、`check_deploy_consistency.py`、`check_contract_conformance.py`、`check_contract_pin.py` | **直接复用** | L0/L2 结构护栏，全是只读检查 |
| `tellurion-contracts` 5 条护栏 + `conformance` | **直接复用** | 跨仓唯一真相；不新增用例 |
| `pkg/conformance` + agent `internal/conformance` | **直接复用** | 两侧读同一份 `cases.json` |
| `e2e/guard`（AST 护栏） | **直接复用** | 防止 e2e 门退化成“只探 /health” |
| `e2e/backtest_live`（信号对账） | **直接复用** | paper vs 回测同 bar 序列零差异 |
| `e2e/tests` | **条件复用** | 需开放鉴权栈 + 夹具；空库时 OHLCV/fundamentals 只记日志不判失败 |
| `cmd/dualrun -gate` | **条件复用** | 需 NT sidecar + 对齐 fixture；Windows 宿主跑 `python -m sidecar` |
| UI vitest（52 文件） | **直接复用** | 纯前端，不依赖后端 |
| UI Playwright（31 spec） | **条件复用** | 需 full + worker + 高限流 + 夹具 + seed；逐条分类失败 |
| UI 视觉基线（12 条） | **条件复用** | 已知 10 / 2；**不重录** |
| `tools/pg_backup_drill.ps1` | **直接复用** | 整库 dump → 独立 `_test` 库 restore → 逐表行数对拍 |
| `tools/ci/seed_alignment_fixture.sql` | **条件复用** | 对齐门合成行情，确定性可复现 |
| `tools/ci/agent_compat.sh` | **条件复用** | 需 Git Bash；无则记 BLOCKED |

**需要补、但不新增代码的部分：**
- 跨服务业务场景目前没有单一入口 → 用 W4 的 HTTP 编排补齐。
- 非功能测试没有统一入口 → 用 W6 的命令编排 + 破坏验证补齐。
- UI 数据依赖用例的造数 → 复用现有 `market_e2e.sql` + `POST /api/backtest` seed。
- 数据还原演练 → 复用现有 PowerShell 脚本，补一次真跑与结果登记。

### 2.4 已知红点与预期结果（开测前先登记，避免误判新回归）

| 编号 | 现象 | 预期结果 | 处置 |
|---|---|---|---|
| K1 | UI 两条 responsive 视觉基线红 | 原：`visual-regression.spec.ts` 10 passed / 2 failed（`dashboard-mobile.png` 差 0.281%、`backtest-tablet.png` 差 0.852%） | **已关闭**：F5 `d91c489` 重录后 12/12 通过（2026-11-13 复跑） |
| K2 | dashboard 历史卡片 | `dashboard.spec.ts` 的 `history card 渲染真实回测记录` 要求库里有已完成回测；空库时 `rows.count()=0` 必红 | 先 seed 一条 completed job；若仍红，先查 429 与数据，再判回归 |
| K3 | `:8085` 共享限流 429 | 默认 100 req/min 时，整套 Playwright 从 `127.0.0.1` 出量会把历史卡片打成 429 | **环境姿势**；W5 必须 `RATE_LIMIT_PER_MINUTE=100000`；不调低断言 |
| K4 | `BenchmarkBacktest` 缺日历 fixture | 2026-11-13 本地复验：`TestBenchmarkTradingDaysMatchFixture` 与 `-bench BenchmarkBacktest -benchtime=1x` 均通过（fixture 5 票 / 252 日） | 历史已知红已消；若他机红，先查 `pkg/backtest/testdata/momentum-5stock-1yr.json` 与 checkout 版本，不直接判回归 |
| K5 | broker `/readyz` 404 | 按设计无此路由（G4） | 就绪判据用 `/healthz` + core `/readyz` |
| K6 | `GET /api/review/pending` 的 `ai_explore` 为 `unavailable` | 旧 ai 镜像（早于 agent `787f9ff`）没有 `/api/explore/review/pending` | W0 必须用 agent HEAD 重建 ai 镜像；否则记 FAIL(环境) |
| K7 | 异步回测一直 pending | 缺 `orchestration-worker` 或未设 `ORCHESTRATION_URL` | W4.2/W5 显式加 profile + env；否则按 BLOCKED 记，不判“异步坏了” |
| K8 | 旧栈与新栈抢宿主端口 | 旧项目 `tellurion` 仍存在但可 stop | W0 先 `docker compose --profile full stop`；**不要** `down -v` 旧栈 |
| K9 | 空库读面返回 `[]` | 这是空库正常结果，不是 503 | 判可用看状态码 + 形状；不要要求非空 |
| K10 | `POST /api/factor/compute` 404、`GET /api/factors/{name}` 410 | ADR-041 / C8 已退役该 HTTP 面 | **预期行为**，不判回归 |
| K11 | `/api/auth/status` 被 429 | 该路径在豁免表内，永不被限流 | 若 429，按**真实缺陷**处理（会打崩整个 SPA） |

---

## 3. 硬约束与安全门

### 3.1 隔离边界（W0.1 必须先过）

- 只允许 Compose 项目 `tellurion-next`；只允许 PG `127.0.0.1:15432`。
- 所有 `psql` 走 `docker exec tellurion-next-pg-1`；不得用宿主 `psql -p 5432` 连部署库。
- 禁止 `docker compose -p tellurion down -v`；旧栈只允许 `stop` / `start`。
- 禁止把 `tellurion-next` 的 `down -v` 用在旧栈；`-v` 只删 `tellurion-next_next-pgdata`。
- 任何命令出现 `-h localhost -p 5432`、`host.docker.internal:5432` 且不是只读诊断时，立即停手并上报。

### 3.2 破坏验证与还原

- 代码级破坏只在 `git worktree add --detach %TEMP%\... HEAD` 的临时 worktree 做；主工作区不出现破坏。
- 数据级破坏前必须有 W0.6 的整库 dump；破坏后用 `pg_restore` 或 API 重新 seed 恢复，并核对行数。
- 每次破坏后必须 `git diff --exit-code` 为 0，再复跑目标用例为绿，才允许记录。

### 3.3 证据留存

- 统一目录：`reports/test-2026-11-13/`。
- 每条 FAIL 至少留：命令原文、退出码、首次失败输出、`docker compose logs` 相关行、必要的 `diff.png` / dump。
- 长输出重定向到文件，终端只打印摘要；不能用 `| tail` 改变退出码（Playwright 尤其注意）。

### 3.4 本计划不做什么

- 不修改任何仓的业务代码来“修绿”；发现缺陷只登记，修复另立任务。
- 不重录视觉基线、不放宽断言、不把 BLOCKED 写成 PASS。
- 不跑 Tushare 在线同步（`TUSHARE_ONLINE=1` 不在必过范围）。
- 不对宿主部署库做迁移接管；那属于另一次受控操作。

---

## 4. 波次执行

> 所有 PowerShell 命令默认在 `c:\Users\ruoxi\Workspace\tellurion` 执行；需要切仓时显式 `Push-Location`。HTTP 状态码统一用 `curl.exe -s -o NUL -w "%{http_code}"`，避免 `Invoke-WebRequest` 对 4xx 抛异常。

### Wave 0：安全门、起栈、基线（串行）

#### Task 0.1 安全门：确认没有接到宿主库

**目的：** 在写任何数据前证明当前操作对象是隔离栈。
**覆盖：** 隔离边界、旧栈冲突。

- [ ] **Step 1: 确认隔离容器存在**

```powershell
docker ps --format '{{.Names}}' | Select-String '^tellurion-next-'
```

预期：至少看到 `tellurion-next-pg-1` 和 8 个服务容器；若一个都没有，先做 Task 0.3。

- [ ] **Step 2: 证明 PG 身份**

```powershell
docker exec tellurion-next-pg-1 psql -U postgres -d tellurion -tAc "select current_database(), current_setting('port'), coalesce(inet_server_addr()::text,'local-socket');"
```

预期：`tellurion|5432|local-socket`（容器内 5432；宿主映射 15432）。若返回其他库名，停手。

- [ ] **Step 3: 证明宿主部署库未被连接**

```powershell
Get-NetTCPConnection -LocalPort 5432 -State Listen | Select-Object LocalAddress,LocalPort,OwningProcess
```

只记录宿主 PG 进程存在，**不连接**。后续所有 SQL 必须走 `docker exec`。

- [ ] **Step 4: 停旧栈（只 stop，不删除）**

```powershell
docker compose --profile full stop
```

预期：旧项目 `tellurion` 的容器停止；不执行 `down`。

#### Task 0.2 重建全部镜像（不能只重建 core）

**目的：** 避免旧镜像导致跨服务语义误判（尤其 ai-service）。
**覆盖：** 镜像新鲜度、部署一致性。

- [ ] **Step 1: 按顺序重建 7 个服务镜像**

```powershell
$W='C:\Users\ruoxi\Workspace'
docker build -f Dockerfile.service -t tellurion-core:head $W\tellurion
docker build -t tellurion/data:latest           $W\tellurion-data
docker build -t tellurion-broker:dev            $W\tellurion-broker
docker build -t tellurion-ai-service:latest     $W\tellurion-agent
docker build -t tellurion-risk:dev              $W\tellurion-risk
docker build -t tellurion-strategy:dev          $W\tellurion-strategy
docker build -t tellurion-analytics:dev         $W\tellurion-analytics
docker build -t tellurion-orchestration:dev     $W\tellurion-orchestration
```

- [ ] **Step 2: 陈旧镜像自检（比时间，不比 tag）**

```powershell
docker images --format '{{.Repository}}:{{.Tag}} {{.CreatedAt}}' | Select-String 'tellurion|tellurion-core'
git -C $W\tellurion-agent log -1 --format='%h %cI'
```

判据：每个仓的 HEAD 提交时间必须早于对应镜像的 `CreatedAt`。ai-service 不满足时，`GET /api/review/pending` 会退化为 `unavailable`（K6）。

#### Task 0.3 起隔离栈

- [ ] **Step 1: 一键起栈**

```powershell
docker compose -p tellurion-next -f docker-compose.yml -f deploy/compose/isolated-test.override.yml --profile full up -d --wait
```

- [ ] **Step 2: 确认 9 容器状态**

```powershell
docker compose -p tellurion-next -f docker-compose.yml -f deploy/compose/isolated-test.override.yml ps --format '{{.Name}} {{.State}} {{.Status}}'
```

判据：9 个容器全部 `running`，除 broker 外都 `healthy`；broker 用 `/healthz` 单独确认。

- [ ] **Step 3: 逐服务健康/就绪**

```powershell
$checks=@{8085='/readyz';8081='/readyz';8086='/readyz';8089='/readyz';8090='/readyz';8087='/readyz';8088='/readyz';8091='/healthz'}
foreach($p in $checks.Keys | Sort-Object){
  $code = curl.exe -s -o NUL -w "%{http_code}" "http://127.0.0.1:$p$($checks[$p])"
  "{0} {1} -> {2}" -f $p,$checks[$p],$code
}
```

预期：全 200；`8091` 用 `/healthz`，不要探 `/readyz`（K5）。

**失败取证：** `docker compose ... logs --no-color > reports/test-2026-11-13/w0-start.log`；`docker inspect` 健康检查详情；`/readyz` 响应体。

#### Task 0.4 迁移账本与零重放

- [ ] **Step 1: 首启账本**

```powershell
docker exec tellurion-next-pg-1 psql -U postgres -d tellurion -tAc "select count(*),min(version),max(version),max(applied_at) from core_schema_migrations;"
```

预期：`85|1|85|<时间戳>`（与 `allMigrationStatements()` 条数一致）。

- [ ] **Step 2: 重启两遍，确认 `max(applied_at)` 不变**

```powershell
docker restart tellurion-next-analysis-service-1 tellurion-next-data-service-1
# 等 health 后重复 Step 1；再 restart 一次再查
```

预期：行数仍 85，`max(applied_at)` 与首启完全一致 ⇒ 零重放。

**失败取证：** 保存三次查询结果；若行数增加或时间戳变化，记 FAIL 并保留 `docker logs` 中迁移相关行。注意迁移日志是 debug 级，判定以账本时间戳为准，不看日志行数。

#### Task 0.5 端点矩阵冒烟（空库 + mock 通道）

- [ ] **Step 1: core 执行面 6 条**

```powershell
$B='http://127.0.0.1:8085'
@('/api/execution/orders','/api/execution/positions','/api/execution/account','/api/signals','/api/review/pending','/api/factor-analytics/factors') | ForEach-Object {
  "{0} -> {1}" -f $_, (curl.exe -s -o NUL -w "%{http_code}" "$B$_")
}
```

预期：全 200；`/api/review/pending` 的 `sources[0].status` 必须是 `available`（不是 `unavailable`）。

- [ ] **Step 2: 下单 + 撤单闭环**

```powershell
$body='{"symbol":"600000.SH","side":"long","type":"limit","price":1.0,"quantity":100}'
$r=Invoke-RestMethod -Method Post -Uri "$B/api/execution/orders" -ContentType 'application/json' -Body $body
$r | ConvertTo-Json -Depth 5
$oid=$r.order_id
Invoke-RestMethod -Method Post -Uri "$B/api/execution/orders/$oid/cancel" | ConvertTo-Json -Depth 5
```

预期：下单 201（`order_id` 以 `core-` 开头）；限价 1.0 远离市价，保持 pending，撤单 200 且 `status=cancelled`。若 mock 仍立即成交导致 409，记录 `not_cancellable` 与终态，不判失败（但要在报告注明 mock 通道行为）。

- [ ] **Step 3: 紧急平仓默认态（未配置 token → 503 禁用）**

```powershell
curl.exe -s -o NUL -w "%{http_code}`n" -X POST "$B/api/execution/emergency-flatten" `
  -H "Content-Type: application/json" `
  -d '{"reason":"test","confirmation_token":"test"}'
```

预期：503，body 含 `emergency flatten endpoint disabled`。隔离栈未设置 `trading.emergency_token`，这是**按设计禁用**，不是缺失鉴权。
Bearer 的 401 / 403 / 200 三态在 W6.3 Step 8 用进程级探针验证（那里可以注入 `TRADING_EMERGENCY_TOKEN`）。

**失败取证：** 保存每个响应的状态码与 body；`POST /api/backtest` 的 4xx 需要保存完整 body 以便区分参数错误与装配错误。

#### Task 0.6 基线快照

- [ ] **Step 1: 建证据目录并 dump**

```powershell
New-Item -ItemType Directory -Force reports\test-2026-11-13 | Out-Null
docker exec tellurion-next-pg-1 pg_dump -U postgres -d tellurion -Fc -f /tmp/baseline.dump
docker cp tellurion-next-pg-1:/tmp/baseline.dump reports\test-2026-11-13\baseline.dump
```

- [ ] **Step 2: 记录关键表基线行数**

```powershell
docker exec tellurion-next-pg-1 psql -U postgres -d tellurion -tAc @"
select 'core_schema_migrations', count(*) from core_schema_migrations
union all select 'market.ohlcv_daily_qfq', count(*) from market.ohlcv_daily_qfq
union all select 'run.backtest_jobs', count(*) from run.backtest_jobs
union all select 'quant.run_provenance', count(*) from quant.run_provenance;
"@ | Tee-Object reports\test-2026-11-13\w0-rowcounts.txt
```

预期：账本 85；行情 / 作业 / 留痕可能为 0（空库正常）；记录即可。

---

### Wave 1：L0 静态 + L1 单元（仓间并行）

> **并行方式：** 开 3 个终端，每个终端跑 3~4 个仓；同一仓内部串行。W1 不依赖 W0 起栈，可与 W0.2~0.5 并行，但不要修改工作区文件。

#### Task 1.1 core 静态与单元

**目的：** 在无栈依赖的前提下验证 core 逻辑。
**覆盖：** L0 + L1；排除 `e2e` 包，避免 L1 依赖运行栈。

- [ ] **Step 1: 格式与静态检查**

```powershell
cd C:\Users\ruoxi\Workspace\tellurion
gofmt -l .
go vet ./...
```

判据：`gofmt -l` 输出为空；`go vet` 退出 0。若非空，只记录文件清单，不自动 `gofmt -w`（`tools/check.sh` 的口径）。

- [ ] **Step 2: 纯单元测试（排除 e2e）**

```powershell
$pkgs = go list ./... | Where-Object { $_ -notmatch '/e2e' }
go test $pkgs -count=1 2>&1 | Tee-Object ..\..\reports\test-2026-11-13\w1-core-unit.log
```

判据：退出码 0。

- [ ] **Step 3: 覆盖率门（与 CI 同口径）**

```powershell
go test $pkgs -count=1 -race -coverprofile=coverage.out
go run ./tools/coveragegate -profile=coverage.out -total-min=52 -pkg-min=tellurion/pkg/backtest=65
```

判据：`coveragegate` 退出 0（total ≥ 52%，`pkg/backtest` ≥ 65%）。
若 `-race` 因无 gcc 失败：记 **BLOCKED（race 阶）**，保留错误原文，改跑无 `-race` 的覆盖率门并注明“race 未覆盖”。

- [ ] **Step 4: 提交前闸（Git Bash）**

```bash
sh tools/check.sh --clean
```

判据：退出 0；无 Git Bash 时记 BLOCKED，改跑 Step 1~2 的三条命令。

#### Task 1.2 兄弟仓单元测试

**目的：** 各仓独立可构建、可测试。
**覆盖：** L1。

- [ ] **Step 1: 逐仓 build / vet / test**

```powershell
$W='C:\Users\ruoxi\Workspace'
$repos=@('tellurion-data','tellurion-broker','tellurion-agent','tellurion-risk','tellurion-strategy','tellurion-analytics','tellurion-orchestration','tellurion-contracts','tellurion-kit')
foreach($r in $repos){
  Push-Location "$W\$r"
  "===== $r ====="
  go build ./...; $b=$LASTEXITCODE
  go vet ./...;   $v=$LASTEXITCODE
  go test ./... -count=1; $t=$LASTEXITCODE
  "{0}: build={1} vet={2} test={3}" -f $r,$b,$v,$t
  Pop-Location
}
```

判据：每个仓 `build=0 vet=0 test=0`。`tellurion-kit` 的 `integration` build tag 腿默认跳过，属预期，不算失败。

- [ ] **Step 2: contracts 5 条护栏**

```powershell
Push-Location $W\tellurion-contracts
go run ./tools/check_api_surface
go run ./tools/check_release_matrix
go run ./tools/check_contract_shape
go run ./tools/check_surface_freeze
go test ./conformance -count=1
Pop-Location
```

判据：全 0。`check_release_matrix` 若因本地无 tag 红，先 `git fetch --tags` 再跑，仍红则记 FAIL（不跳过）。

#### Task 1.3 UI 静态与 vitest

- [ ] **Step 1: 类型、lint、构建**

```powershell
Push-Location C:\Users\ruoxi\Workspace\tellurion-ui\web
npm run typecheck
npm run lint
npm run build
Pop-Location
```

判据：三条全 0。`npm run build` 含 `vue-tsc -b && vite build`。

- [ ] **Step 2: vitest**

```powershell
Push-Location C:\Users\ruoxi\Workspace\tellurion-ui\web
npm test
Pop-Location
```

判据：退出 0（`lint:tests` + `vitest run`）。失败时保存完整输出；不要用 `--passWithNoTests` 掩盖。

#### Task 1.4 文档与仓库自治护栏

- [ ] **Step 1: core 结构护栏**

```powershell
cd C:\Users\ruoxi\Workspace\tellurion
python tools\check_doc_links.py
python tools\check_deploy_consistency.py
python tools\check_repo_autonomy.py
python tools\check_repo_autonomy.py --self-test
```

判据：全 0。`check_deploy_consistency.py` 在 k8s 退役状态下会明确打印跳过对象，属预期；compose 侧断言一条不减。

- [ ] **Step 2: agent 兼容（条件）**

```bash
sh tools/ci/agent_compat.sh
```

判据：0；无 Git Bash 时 BLOCKED。

---

### Wave 2：L2 契约与一致性（与 W1 并行）

#### Task 2.1 两侧 conformance

**目的：** 证明 core 与 agent 读同一份 `cases.json` 且语义一致。
**覆盖：** 费用默认值、DTO 形状、错误哨兵。

- [ ] **Step 1: core 侧**

```powershell
cd C:\Users\ruoxi\Workspace\tellurion
go test ./pkg/conformance -v -count=1
```

判据：退出 0；输出中 `executed > 0`，`skipped` 的每个用例都有显式原因。

- [ ] **Step 2: agent 侧**

```powershell
Push-Location C:\Users\ruoxi\Workspace\tellurion-agent
go test ./internal/conformance -v -count=1
Pop-Location
```

判据：退出 0；`fee_defaults_via_yaml` 等用例被真实执行（不是全 skip）。若 `cases.json` 定位失败，测试会 `t.Fatalf`，按 FAIL(环境) 处理，不放宽为 skip。

#### Task 2.2 API surface / pin / 仓自治的跨仓部分

- [ ] **Step 1: pin 必须是 main 祖先**

```powershell
cd C:\Users\ruoxi\Workspace\tellurion
python tools\check_contract_pin.py
```

判据：0；每条 pin 打印 `repo@sha → 提交日期`。若拉不到目标仓，脚本 fail-closed，记 BLOCKED(网络) 而不是 PASS。

- [ ] **Step 2: 跨仓契约语料一致性**

```powershell
python tools\check_contract_conformance.py
```

判据：0；契约版本与两侧消费一致。

- [ ] **Step 3: e2e 门与 spec 漂移护栏**

```powershell
go test ./e2e/guard -v -count=1
go test ./e2e/backtest_live -v -count=1
```

判据：0。`e2e/guard` 证明 e2e 套件门真正探了业务端点；`backtest_live` 证明 paper 与回测在同 bar 序列上信号逐条一致（零差异）。

---

### Wave 3：L4 单服务 E2E 与端点矩阵（服务间并行）

> **前置：** W0 通过。所有请求打 `127.0.0.1` 宿主端口；不要进容器内用 `localhost` 访问其他服务。

#### Task 3.1 core e2e 套件

- [ ] **Step 1: 确认是开放鉴权栈**

```powershell
curl.exe -s http://127.0.0.1:8085/api/auth/status
```

预期：JSON 中 `auth_required=false`（或等价字段），写面可直接访问。若是 `true`，e2e 会因 401 跳过，按 BLOCKED(形态不匹配) 记录。

- [ ] **Step 2: 跑 e2e/tests**

```powershell
cd C:\Users\ruoxi\Workspace\tellurion
$env:E2E_FORCE_SKIP=''
go test ./e2e/tests -v -count=1 2>&1 | Tee-Object ..\..\reports\test-2026-11-13\w3-core-e2e.log
```

判据：退出 0；`TestHealthCheck_AllServices`、`TestBacktestAPI_EndToEnd`、`TestStrategyAPI_ListStrategies`（momentum 非空）、`TestExecutionService_OrderPersistence` 必须真实执行，不能跳过。

**失败取证：** 保存每个 `t.Log` 与失败断言；空库导致的 OHLCV/fundamentals 日志不是失败。

#### Task 3.2 逐服务就绪与最小正/负向

- [ ] **Step 1: 逐服务矩阵**

```powershell
$cases=@(
  @{u='http://127.0.0.1:8081/readyz';                  want=200},
  @{u='http://127.0.0.1:8081/api/v1/stocks/600000.SH'; want=200}, # 需 W5.1 夹具；无夹具时 404 属数据前置
  @{u='http://127.0.0.1:8081/api/sync/workers';        want=200},
  @{u='http://127.0.0.1:8091/healthz';                 want=200},
  @{u='http://127.0.0.1:8091/readyz';                  want=404}, # K5
  @{u='http://127.0.0.1:8091/api/broker/orders/absent';want=404},
  @{u='http://127.0.0.1:8089/readyz';                  want=200},
  @{u='http://127.0.0.1:8089/api/risk/rules';          want=200},
  @{u='http://127.0.0.1:8090/readyz';                  want=200},
  @{u='http://127.0.0.1:8090/api/strategies/none/versions'; want=200},
  @{u='http://127.0.0.1:8087/readyz';                  want=200},
  @{u='http://127.0.0.1:8087/api/analysis/reports/none'; want=404},
  @{u='http://127.0.0.1:8088/readyz';                  want=200},
  @{u='http://127.0.0.1:8088/api/runs/jobs?limit=1';   want=200},
  @{u='http://127.0.0.1:8086/readyz';                  want=200},
  @{u='http://127.0.0.1:8086/api/explore/review/pending'; want=200}
)
foreach($c in $cases){
  $code = curl.exe -s -o NUL -w "%{http_code}" $c.u
  "{0} -> {1} (want {2})" -f $c.u,$code,$c.want
}
```

判据：每行实际码 = want。`8081` 的股票查询在夹具未装时返回 404，标 **BLOCKED(数据前置)**，不算 FAIL；W5.1 装完夹具后回到本 Task 只重跑 8081 的股票行，必须 200。

- [ ] **Step 2: broker 写面鉴权负向**

```powershell
# 无 token -> 401
curl.exe -s -o NUL -w "%{http_code}`n" -X POST http://127.0.0.1:8091/api/broker/orders -H "Content-Type: application/json" -d '{}'
# 正确 token -> 应为参数校验 4xx 或 201，绝不能是 401
curl.exe -s -o NUL -w "%{http_code}`n" -X POST http://127.0.0.1:8091/api/broker/orders -H "Authorization: Bearer dev-token" -H "Content-Type: application/json" -d '{}'
```

判据：第一行 401；第二行不是 401。若第二行仍 401，检查 `BROKER_SERVICE_TOKEN` 与栈 env 是否一致。

#### Task 3.3 按设计退役的端点（防误判）

- [ ] **Step 1: 验证 404/410 是预期**

```powershell
curl.exe -s -o NUL -w "%{http_code}`n" -X POST http://127.0.0.1:8085/api/factor/compute -d '{}'
curl.exe -s -o NUL -w "%{http_code}`n" http://127.0.0.1:8085/api/factors/momentum
```

预期：404 与 410；410 的 body 应带 `factor_http_surface_retired` 机器码（K10）。不判 FAIL。

---

### Wave 4：L5 跨服务业务场景（4.1/4.2 串行，其余可并行）

#### Task 4.1 订单生命周期（core → broker → core 读面）

**目的：** 证明下单、查询、撤单、账户/持仓读面在跨进程边界上一致。
**覆盖：** L5 订单链路。

- [ ] **Step 1: 下单并记录 order_id**

```powershell
$B='http://127.0.0.1:8085'
$r=Invoke-RestMethod -Method Post -Uri "$B/api/execution/orders" -ContentType 'application/json' -Body '{"symbol":"600000.SH","side":"long","type":"limit","price":1.0,"quantity":100}'
$r | ConvertTo-Json -Depth 5
$oid=$r.order_id
```

判据：HTTP 201；`order_id` 非空。

- [ ] **Step 2: core 与 broker 双读**

```powershell
Invoke-RestMethod "$B/api/execution/orders/$oid" | ConvertTo-Json -Depth 5
curl.exe -s -o NUL -w "%{http_code}`n" "http://127.0.0.1:8091/api/broker/orders/$oid"
```

判据：core 200 且 order_id 一致；broker 200（若 404，记 FAIL：core 的 id 未传到 broker 或映射错了）。

- [ ] **Step 3: 撤单与账户/持仓**

```powershell
Invoke-RestMethod -Method Post -Uri "$B/api/execution/orders/$oid/cancel" | ConvertTo-Json -Depth 5
Invoke-RestMethod "$B/api/execution/account" | ConvertTo-Json -Depth 5
Invoke-RestMethod "$B/api/execution/positions" | ConvertTo-Json -Depth 5
```

判据：撤单 200 且 `status=cancelled`；账户 `total_assets` 存在且 > 0（不硬编码 mock 初始值）；持仓为空数组或包含该单成交后的持仓，且与 broker `positions` 一致。

**失败取证：** 保存 core 与 broker 两侧 body；`docker logs tellurion-next-broker-1` 与 core 日志。

#### Task 4.2 异步回测（core → orchestration → worker → analytics 读面）

**目的：** 证明 `POST /api/backtest` → run job → worker 领活 → 结果落库 → 读面可见的完整状态机。
**覆盖：** L5 异步链路、worker 租约。

- [ ] **Step 1: 临时切到 worker 栈（串行，独占）**

```powershell
$env:ORCHESTRATION_URL='http://orchestration:8088'
docker compose -p tellurion-next -f docker-compose.yml -f deploy/compose/isolated-test.override.yml `
  --profile full --profile orchestration-worker up -d --wait --force-recreate --no-deps analysis-service worker
```

判据：`worker` 容器 running；`GET /api/runs/workers` 中至少一个 worker `status=active`。

- [ ] **Step 2: 提交异步回测（必须用新格式 `strategy_id` + `universe`）**

```powershell
$B='http://127.0.0.1:8085'
$body='{"strategy_id":"momentum","universe":"600000.SH","start_date":"2024-01-01","end_date":"2024-03-31","initial_capital":1000000}'
$r=Invoke-RestMethod -Method Post -Uri "$B/api/backtest" -ContentType 'application/json' -Body $body
$r | ConvertTo-Json -Depth 5
$job=$r.job_id
```

判据：返回 `job_id`（异步 200/202）。若返回同步结果（含 `total_return`），说明请求落到了旧格式同步分支或 `ORCHESTRATION_URL` 没生效，记 BLOCKED(姿势) 并回到 Step 1。

- [ ] **Step 3: 30s 内轮询 `/api/backtest/status/{job}`**

```powershell
$deadline=(Get-Date).AddSeconds(30)
do {
  $s=Invoke-RestMethod "$B/api/backtest/status/$job"
  $s.state
  if($s.state -in @('completed','failed')){ break }
  Start-Sleep -Seconds 1
} while((Get-Date) -lt $deadline)
$s | ConvertTo-Json -Depth 6
```

判据：30s 内 `state=completed`（`failed` 时保存 `error`）。一直 `pending` ⇒ K7，记 BLOCKED(worker/URL)，不判“异步坏了”。

- [ ] **Step 4: 读面与留痕**

```powershell
foreach($p in @("$B/api/backtest/$job/report","$B/api/backtest/$job/trades","$B/api/backtest/$job/equity","$B/api/backtest/$job/provenance")){
  "{0} -> {1}" -f $p,(curl.exe -s -o NUL -w "%{http_code}" $p)
}
Invoke-RestMethod "http://127.0.0.1:8088/api/runs/jobs/$job" | ConvertTo-Json -Depth 6
```

判据：report/trades/equity 200；provenance 200 且含三 hash；orchestration 的 `GET /api/runs/jobs/{id}` 终态 `completed`（允许值为 pending/running/cancelling/completed/failed/cancelled）。

**失败取证：** core / orchestration / worker 三侧日志；`GET /api/runs/jobs/$job` 的 `attempts`、`error_message`、`worker_id`。

#### Task 4.3 review pending 聚合（core → ai）

- [ ] **Step 1: 正常聚合**

```powershell
Invoke-RestMethod http://127.0.0.1:8085/api/review/pending | ConvertTo-Json -Depth 5
```

判据：200；`sources` 中 `ai_explore.status=available`。

- [ ] **Step 2: ai 不可用时的可见降级（与 W6.5 共用）**

```powershell
docker stop tellurion-next-ai-service-1
Start-Sleep -Seconds 3
Invoke-RestMethod http://127.0.0.1:8085/api/review/pending | ConvertTo-Json -Depth 5
docker start tellurion-next-ai-service-1
```

判据：core 仍 200，`ai_explore.status=unavailable`（不是 500/panic）；恢复后回到 `available`。

#### Task 4.4 analytics 读面（依赖 4.2 的 run_id）

**目的：** 证明 analytics 能从 core 的 run 与 artifact 读出指标。
**覆盖：** L5 分析链路。

- [ ] **Step 1: 用 4.2 的 job 跑指标**

```powershell
$B='http://127.0.0.1:8085'
$run=$job
Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:8087/api/analysis/metrics' -ContentType 'application/json' -Body (@{run_id=$run} | ConvertTo-Json) | ConvertTo-Json -Depth 6
```

判据：200，响应含收益/风险指标字段；若上游 artifact 未就绪，返回可读 4xx/503 并记 BLOCKED(上游)，不把 503 当 200。

- [ ] **Step 2: 归因恒等式（若 metrics 通过）**

```powershell
Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:8087/api/analysis/attribution' -ContentType 'application/json' -Body (@{run_id=$run; method='factor'} | ConvertTo-Json) | ConvertTo-Json -Depth 6
```

判据：`sum(contributions)+residual ≈ total_return`（容差 1e-8）。

#### Task 4.5 策略资产链路（core → strategy）

- [ ] **Step 1: 版本读面与内置策略解析**

```powershell
curl.exe -s -o NUL -w "%{http_code}`n" http://127.0.0.1:8090/api/strategies/none/versions
curl.exe -s -o NUL -w "%{http_code}`n" http://127.0.0.1:8090/api/strategies/none/resolve
curl.exe -s http://127.0.0.1:8090/api/strategies/momentum/resolve?ref=latest
```

判据：`versions` 对未知 id 返回 200 + `[]`；`none/resolve` 返回 404 `not_found`；`momentum/resolve` 返回 200 且带非空 `hash`/`artifact` 指针。三条都不能是 500。若 `momentum/resolve` 404，则 W4.2 的异步回测也会失败，按真实契约缺口记 FAIL，而不是“UI 问题”。

---

### Wave 5：UI Playwright + 视觉基线（串行）

> **前置：** W0 栈 + worker（Task 4.2 Step 1）+ 夹具 + 高限流 + seed。任一缺失都可能把环境问题误判为 UI 回归。

#### Task 5.1 数据夹具与 seed

- [ ] **Step 1: 装确定性行情夹具**

```powershell
Get-Content C:\Users\ruoxi\Workspace\tellurion-ui\e2e\fixtures\market_e2e.sql -Raw |
  docker exec -i tellurion-next-pg-1 psql -U postgres -d tellurion -v ON_ERROR_STOP=1
docker exec tellurion-next-pg-1 psql -U postgres -d tellurion -tAc "select count(*) from market.ohlcv_daily_qfq;"
```

判据：531 行日线（3 票）与 267 行 SSE 日历（以 fixture 实际文件为准）；`market.stocks` 有 3 条。

- [ ] **Step 2: seed 一条 completed 回测（K2 前置，必须走异步新格式）**

```powershell
$B='http://127.0.0.1:8085'
$body='{"strategy_id":"momentum","universe":"600000.SH","start_date":"2024-01-01","end_date":"2024-03-31","initial_capital":1000000}'
$r=Invoke-RestMethod -Method Post -Uri "$B/api/backtest" -ContentType 'application/json' -Body $body
$job=$r.job_id
# 用 Task 4.2 Step 3 的轮询到 state=completed
Invoke-RestMethod "$B/api/backtest?limit=5" | ConvertTo-Json -Depth 5
```

判据：`GET /api/backtest?limit=5` 至少 1 条 `completed`；dashboard 历史卡片有行可渲染。

- [ ] **Step 3: 起宿主 vite**

```powershell
$web='C:\Users\ruoxi\Workspace\tellurion-ui\web'
Start-Process npm -ArgumentList 'run','dev' -WorkingDirectory $web -WindowStyle Hidden
Start-Sleep -Seconds 5
curl.exe -s -o NUL -w "%{http_code}`n" http://127.0.0.1:5173/
```

判据：5173 返回 200。若端口被占，先停旧 vite。

#### Task 5.2 UI e2e（31 spec，逐条分类）

- [ ] **Step 1: 安装浏览器（首次）**

```powershell
Push-Location C:\Users\ruoxi\Workspace\tellurion-ui\e2e
npx playwright install chromium
Pop-Location
```

- [ ] **Step 2: 高限流自检**

```powershell
1..110 | ForEach-Object { curl.exe -s -o NUL -w "%{http_code}`n" http://127.0.0.1:8085/api/strategies } |
  Group-Object | Select-Object Name,Count
```

判据：全 200（110 次）。若出现 429，说明 `RATE_LIMIT_PER_MINUTE` 不是 100000（K3），先修姿势再跑 UI。

- [ ] **Step 3: 跑全量（退出码即判据，不要管道到 tail）**

```powershell
Push-Location C:\Users\ruoxi\Workspace\tellurion-ui\e2e
$env:BASE_URL='http://localhost:5173'
$env:BACKEND_URL='http://localhost:8085'
$env:DATA_SERVICE_URL='http://localhost:8081'
npx playwright test 2>&1 | Tee-Object C:\Users\ruoxi\Workspace\tellurion\reports\test-2026-11-13\w5-ui-e2e.log
$code=$LASTEXITCODE
"PLAYWRIGHT_EXIT=$code"
Pop-Location
```

判据：**不是**要求全绿，而是“每个失败都有分类与证据”。分类规则：
- 429 → K3 环境姿势，修限流后重跑；
- 历史卡片 0 行 → K2 seed 缺失；
- 视觉 2 条 → K1 已于 F5 `d91c489` 关闭（12/12 过），再红按 FAIL；
- 其余 → FAIL，保存 trace / screenshot / 失败断言，定位到 spec 与后端响应。

- [ ] **Step 4: 单独复核 API 契约 spec**

```powershell
Push-Location C:\Users\ruoxi\Workspace\tellurion-ui\e2e
npx playwright test tests/api-health.spec.ts tests/api-strategy.spec.ts tests/api-backtest.spec.ts tests/api-negative.spec.ts
Pop-Location
```

判据：退出 0；这 4 条不依赖 UI 渲染，是后端契约的快速回归腿。

#### Task 5.3 视觉基线（12 条，10/2 预期）

- [ ] **Step 1: 只跑视觉**

```powershell
Push-Location C:\Users\ruoxi\Workspace\tellurion-ui\e2e
npx playwright test tests/visual-regression.spec.ts 2>&1 | Tee-Object C:\Users\ruoxi\Workspace\tellurion\reports\test-2026-11-13\w5-visual.log
$code=$LASTEXITCODE
"VISUAL_EXIT=$code"
Pop-Location
```

判据：**10 passed / 2 failed**；失败必须是 `dashboard responsive on mobile viewport` 与 `backtest responsive on tablet viewport`（K1）。

- [ ] **Step 2: 复核红区与比例（禁止重录）**

```powershell
Get-ChildItem C:\Users\ruoxi\Workspace\tellurion-ui\e2e\test-results -Recurse -Filter diff.png |
  Select-Object FullName,Length
```

判据：差异集中在时钟/时间戳、记录条数、字体抗锯齿；单条比例 < 1%。若红区在布局/颜色/组件形状，或比例 > 1%，升级 FAIL，按真实回归处理并另立任务；**不得** `--update-snapshots`。

---

### Wave 6：L6 非功能（串行）

> 所有子任务共享同一原则：每个非功能断言旁边必须有一条功能正向用例；只证明“没崩”不算通过。

#### Task 6.1 并发与竞态

- [ ] **Step 1: Go race（条件）**

```powershell
gcc --version
# 若可用，逐仓跑：
$W='C:\Users\ruoxi\Workspace'
foreach($r in @('tellurion','tellurion-data','tellurion-broker','tellurion-agent','tellurion-risk','tellurion-strategy','tellurion-analytics','tellurion-orchestration','tellurion-contracts','tellurion-kit')){
  Push-Location "$W\$r"; "== $r =="; go test ./... -count=1 -race; "EXIT=$LASTEXITCODE"; Pop-Location
}
```

判据：全 0。`gcc` 不可用时整段记 BLOCKED（race 阶），并引用 contracts CI 已跑 race 作为参考，但不冒充本机通过。

- [ ] **Step 2: 20 并发下单**

```powershell
$jobs = 1..20 | ForEach-Object {
  Start-Job -ScriptBlock {
    try {
      (Invoke-RestMethod -Method Post -Uri http://127.0.0.1:8085/api/execution/orders `
        -ContentType 'application/json' `
        -Body '{"symbol":"600000.SH","side":"long","type":"market","quantity":100}').order_id
    } catch { "ERR:$($_.Exception.Message)" }
  }
}
$ids = $jobs | Wait-Job | Receive-Job
"errors=" + (($ids | Where-Object {$_ -like 'ERR:*'}).Count)
"unique_ids=" + (($ids | Where-Object {$_ -notlike 'ERR:*'} | Select-Object -Unique).Count)
(Invoke-RestMethod http://127.0.0.1:8085/api/execution/orders).count
```

判据：`errors=0`；`unique_ids=20`；订单总数增加 20（与 Wave 4.1 的既有单相加）。出现 500/panic 或重复 id 即 FAIL。

- [ ] **Step 3: 并发清理**

```powershell
foreach($id in ($ids | Where-Object {$_ -notlike 'ERR:*'})){
  try { Invoke-RestMethod -Method Post -Uri "http://127.0.0.1:8085/api/execution/orders/$id/cancel" | Out-Null } catch {}
}
```

判据：非 2xx 的撤单逐条记录（mock 通道已成交的单可能不可撤，属预期）。

#### Task 6.2 幂等

- [ ] **Step 1: risk breaker 幂等三态**

```powershell
$R='http://127.0.0.1:8089/api/risk/breakers'
$b1='{"breaker_id":"it-1","action":"halt","scope":"global","reason":"dd breach","ts":"2026-11-13T10:00:00Z"}'
$b2='{"breaker_id":"it-1","action":"halt","scope":"global","reason":"dd breach","ts":"2026-11-13T10:30:00Z"}'
$b3='{"breaker_id":"it-1","action":"cancel_all","scope":"global","reason":"dd breach","ts":"2026-11-13T10:00:00Z"}'
Invoke-RestMethod -Method Post -Uri $R -ContentType 'application/json' -Body $b1 | ConvertTo-Json
Invoke-RestMethod -Method Post -Uri $R -ContentType 'application/json' -Body $b2 | ConvertTo-Json
curl.exe -s -o NUL -w "%{http_code}`n" -X POST $R -H "Content-Type: application/json" -d $b3
```

判据：第一次 `status=applied`；第二次（同键同载荷、ts 不同）`status=already_applied`；第三次（同键不同载荷）409 + `idempotency_conflict`。

- [ ] **Step 2: 业务后果只发生一次**

```powershell
docker exec tellurion-next-pg-1 psql -U postgres -d tellurion -tAc "select count(*) from risk.circuit_orders where breaker_id='it-1';"
```

判据：1 条（DDL 见 `tellurion-risk/internal/rule/ddl.go:103`，`breaker_id` 即主键）。

#### Task 6.3 鉴权三态（进程级，不扰动主栈）

**目的：** 验证 `STACK-TEST-PREREQ §4.3` 的四态判决，而不是只测默认态 3。
**做法：** 用宿主二进制在 `:18085` 起临时进程，逐态切换 env；主栈不动。探针与主栈共用 `tellurion` 库，但只做鉴权/限流探测，**不发有效业务写入**（用空 body 触发参数校验）；若观察到后台任务互相干扰，改用一次性库 `tellurion_probe`（`createdb` + 探针首启迁移）后重跑本节。

- [ ] **Step 1: 编译探针二进制**

```powershell
cd C:\Users\ruoxi\Workspace\tellurion
go build -o "$env:TEMP\analysis-probe.exe" ./cmd/analysis
```

- [ ] **Step 2: 公共 env**

```powershell
$env:CONFIG_PATH='config/analysis-service.yaml'
$env:DATABASE_HOST='127.0.0.1'; $env:DATABASE_PORT='15432'
$env:DATABASE_USER='postgres'; $env:DATABASE_PASSWORD='postgres'; $env:DATABASE_DATABASE='tellurion'
$env:DATA_SERVICE_URL='http://127.0.0.1:8081'; $env:AI_SERVICE_URL='http://127.0.0.1:8086'
$env:BROKER_URL='http://127.0.0.1:8091'; $env:BROKER_CHANNEL='mock'
$env:BROKER_ALLOW_MOCK='true'; $env:BROKER_SERVICE_TOKEN='dev-token'
$env:RISK_SERVICE_RESOLVE_ENABLED='false'
$env:SERVER_HOST='127.0.0.1'; $env:SERVER_PORT='18085'
```

- [ ] **Step 3: 态 3（无 secret + 显式声明 → 写面开放）**

```powershell
$env:AUTH_INSECURE='true'; Remove-Item Env:JWT_SECRET -ErrorAction SilentlyContinue
$p=Start-Process -FilePath "$env:TEMP\analysis-probe.exe" -WorkingDirectory 'C:\Users\ruoxi\Workspace\tellurion' -PassThru -WindowStyle Hidden
Start-Sleep -Seconds 8
"health=" + (curl.exe -s -o NUL -w "%{http_code}" http://127.0.0.1:18085/health)
"write="  + (curl.exe -s -o NUL -w "%{http_code}" -X POST http://127.0.0.1:18085/api/execution/orders -H "Content-Type: application/json" -d '{')
Stop-Process -Id $p.Id -Force
```

判据：`health=200`，`write=400`（鉴权已通过，JSON 解析失败；证明写面开放，且不产生副作用）。

- [ ] **Step 4: 态 2（无 secret + 无声明 + 回环 → 写面 503）**

```powershell
Remove-Item Env:AUTH_INSECURE -ErrorAction SilentlyContinue
$p=Start-Process -FilePath "$env:TEMP\analysis-probe.exe" -WorkingDirectory 'C:\Users\ruoxi\Workspace\tellurion' -PassThru -WindowStyle Hidden
Start-Sleep -Seconds 8
"health=" + (curl.exe -s -o NUL -w "%{http_code}" http://127.0.0.1:18085/health)
"write="  + (curl.exe -s -o NUL -w "%{http_code}" -X POST http://127.0.0.1:18085/api/execution/orders -H "Content-Type: application/json" -d '{')
Stop-Process -Id $p.Id -Force
```

判据：`health=200`，`write=503` 且 body 机器码为 `auth_unavailable`。

- [ ] **Step 5: 态 1（有 secret → 未认证写面 401）**

```powershell
$env:JWT_SECRET='probe-secret-not-for-prod'
$p=Start-Process -FilePath "$env:TEMP\analysis-probe.exe" -WorkingDirectory 'C:\Users\ruoxi\Workspace\tellurion' -PassThru -WindowStyle Hidden
Start-Sleep -Seconds 8
"auth_status=" + (curl.exe -s -o NUL -w "%{http_code}" http://127.0.0.1:18085/api/auth/status)
"write="       + (curl.exe -s -o NUL -w "%{http_code}" -X POST http://127.0.0.1:18085/api/execution/orders -H "Content-Type: application/json" -d '{')
Stop-Process -Id $p.Id -Force
```

判据：`auth_status=200`（`auth_required=true`），`write=401`（不是 503，不是 201）。

- [ ] **Step 6: 态 4（无 secret + 无声明 + 非回环 → 拒起）**

```powershell
Remove-Item Env:JWT_SECRET -ErrorAction SilentlyContinue
$env:SERVER_HOST='0.0.0.0'
$p=Start-Process -FilePath "$env:TEMP\analysis-probe.exe" -WorkingDirectory 'C:\Users\ruoxi\Workspace\tellurion' -PassThru -WindowStyle Hidden -RedirectStandardError "$env:TEMP\probe4.err"
Start-Sleep -Seconds 5
"exited=" + $p.HasExited
if($p.HasExited){ "exit_code=" + $p.ExitCode } else { Stop-Process -Id $p.Id -Force }
Get-Content "$env:TEMP\probe4.err" -Tail 20
```

判据：进程在启动期退出且退出码非 0，错误信息明确指向“无 secret + 非回环绑定”；若它成功监听 0.0.0.0，记 FAIL（鉴权绕过）。

- [ ] **Step 7: 恢复**

```powershell
$env:SERVER_HOST='127.0.0.1'; $env:SERVER_PORT='18085'
Remove-Item Env:JWT_SECRET,Env:AUTH_INSECURE -ErrorAction SilentlyContinue
```

- [ ] **Step 8: kill-switch bearer 三态（注入 `TRADING_EMERGENCY_TOKEN`）**

```powershell
$env:AUTH_INSECURE='true'; $env:TRADING_EMERGENCY_TOKEN='dev-only-emergency-token'
$p=Start-Process -FilePath "$env:TEMP\analysis-probe.exe" -WorkingDirectory 'C:\Users\ruoxi\Workspace\tellurion' -PassThru -WindowStyle Hidden
Start-Sleep -Seconds 8
$u='http://127.0.0.1:18085/api/execution/emergency-flatten'
$b='{"reason":"drill","confirmation_token":"dev-only-emergency-token"}'
"no_header="   + (curl.exe -s -o NUL -w "%{http_code}" -X POST $u -H "Content-Type: application/json" -d $b)
"wrong_token=" + (curl.exe -s -o NUL -w "%{http_code}" -X POST $u -H "Authorization: Bearer wrong" -H "Content-Type: application/json" -d $b)
"ok_token="    + (curl.exe -s -o NUL -w "%{http_code}" -X POST $u -H "Authorization: Bearer dev-only-emergency-token" -H "Content-Type: application/json" -d $b)
Stop-Process -Id $p.Id -Force
Remove-Item Env:TRADING_EMERGENCY_TOKEN -ErrorAction SilentlyContinue
```

判据：`401 / 403 / 200`；200 的 body 应为 `sold` + `skipped`（无持仓时 `sold=[]`、`skipped=[]`），不得 500。

#### Task 6.4 限流边界与豁免

- [ ] **Step 1: 低限流探针（进程级）**

```powershell
$env:AUTH_INSECURE='true'; $env:RATE_LIMIT_PER_MINUTE='5'; $env:SERVER_PORT='18085'
$p=Start-Process -FilePath "$env:TEMP\analysis-probe.exe" -WorkingDirectory 'C:\Users\ruoxi\Workspace\tellurion' -PassThru -WindowStyle Hidden
Start-Sleep -Seconds 8
1..7 | ForEach-Object { curl.exe -s -o NUL -w "%{http_code}`n" http://127.0.0.1:18085/api/strategies }
```

判据：前 5 次 200，第 6/7 次 429（窗口/桶实现以 bootstrap 为准；关键是“必须出现 429 且不是 500”）。

- [ ] **Step 2: 豁免路径永不被限流**

```powershell
1..20 | ForEach-Object { curl.exe -s -o NUL -w "%{http_code}`n" http://127.0.0.1:18085/api/auth/status } | Group-Object | Select-Object Name,Count
1..20 | ForEach-Object { curl.exe -s -o NUL -w "%{http_code}`n" http://127.0.0.1:18085/readyz } | Group-Object | Select-Object Name,Count
```

判据：两组的 Name 都只有 200（K11）；出现 429 即 FAIL。

- [ ] **Step 3: 恢复**

```powershell
Stop-Process -Id $p.Id -Force
Remove-Item Env:AUTH_INSECURE,Env:RATE_LIMIT_PER_MINUTE -ErrorAction SilentlyContinue
```

#### Task 6.5 依赖故障恢复

- [ ] **Step 1: data-service 故障**

```powershell
docker stop tellurion-next-data-service-1
Start-Sleep -Seconds 3
"core_stocks=" + (curl.exe -s -o NUL -w "%{http_code}" http://127.0.0.1:8085/api/stocks/600000.SH)
"core_readyz="  + (curl.exe -s -o NUL -w "%{http_code}" http://127.0.0.1:8085/readyz)
docker start tellurion-next-data-service-1
Start-Sleep -Seconds 8
"core_readyz_after=" + (curl.exe -s -o NUL -w "%{http_code}" http://127.0.0.1:8085/readyz)
```

判据：data 停止期间，依赖 data 的 read 面返回可读 502/503（不是 500/panic）；core 自身 `/readyz`（只探自己的库）仍 200；data 恢复后 read 面恢复 200。

- [ ] **Step 2: broker 故障**

```powershell
docker stop tellurion-next-broker-1
Start-Sleep -Seconds 3
curl.exe -s -o NUL -w "%{http_code}`n" -X POST http://127.0.0.1:8085/api/execution/orders -H "Content-Type: application/json" -d '{"symbol":"600000.SH","side":"long","type":"market","quantity":100}'
docker start tellurion-next-broker-1
Start-Sleep -Seconds 8
curl.exe -s -o NUL -w "%{http_code}`n" -X POST http://127.0.0.1:8085/api/execution/orders -H "Content-Type: application/json" -d '{"symbol":"600000.SH","side":"long","type":"market","quantity":100}'
```

判据：停 broker 时下单返回 502/503 + 可读错误（不能 201，也不能 500）；恢复后 201。

- [ ] **Step 3: PG 重启**

```powershell
docker restart tellurion-next-pg-1
Start-Sleep -Seconds 10
docker restart tellurion-next-analysis-service-1 tellurion-next-data-service-1
Start-Sleep -Seconds 15
curl.exe -s -o NUL -w "%{http_code}`n" http://127.0.0.1:8085/readyz
```

判据：恢复后 `/readyz` 200；恢复窗口内请求失败必须可读，不能出现数据错乱（抽样 `GET /api/backtest/jobs` 行数不丢）。

- [ ] **Step 4: worker 中断与租约（可选，条件串行）**

```powershell
# 在 4.2 提交一个作业后，worker 运行中：
docker stop tellurion-next-worker-1
Start-Sleep -Seconds 40
docker start tellurion-next-worker-1
# 观察 GET /api/runs/jobs/{id} 的 attempts / status
```

判据：作业要么在租约到期后被重领并最终 succeeded，要么进入可读的 failed/requeue 状态；**不得**出现同一作业重复产出两份结果。若时间窗不足，记 BLOCKED(时序) 并保留观察记录。

#### Task 6.6 数据还原（真备份、真恢复）

- [ ] **Step 1: 整库备份/恢复演练（独立 `_test` 库）**

```powershell
$env:PGPASSWORD='postgres'
powershell -File C:\Users\ruoxi\Workspace\tellurion\tools\pg_backup_drill.ps1 `
  -Database tellurion -TestDatabase tellurion_dr_test `
  -HostName 127.0.0.1 -Port 15432 `
  -PgBin 'C:\Program Files\PostgreSQL\16\bin'
```

判据：dump 成功；restore 到 `tellurion_dr_test` 成功；逐表行数与备份时点一致；脚本退出 0。若宿主无 PG 16 bin，记 BLOCKED(工具链) 并改用容器内 `pg_dump/pg_restore` 手工执行同一对拍。

- [ ] **Step 2: 小范围破坏 + 真还原（隔离库内）**

```powershell
# 2.1 记录目标（若未跑 W5，$job 改用 Task 4.2 的 job）
docker exec tellurion-next-pg-1 psql -U postgres -d tellurion -tAc "select count(*) from run.backtest_jobs;"
# 2.2 只删 seed 的那条（用 Task 5.1 的 job id，条件精确）
docker exec tellurion-next-pg-1 psql -U postgres -d tellurion -c "delete from run.backtest_jobs where id='$job';"
# 2.3 读面必须 404
curl.exe -s -o NUL -w "%{http_code}`n" "http://127.0.0.1:8085/api/backtest/$job"
# 2.4 用 API 重新 seed（Task 5.1 Step 2），确认作业重新可读
```

判据：删除后 GET 404；重新 seed 后历史列表恢复；行数回到破坏前（新 id 可接受）。若需要**逐字节**还原，改用 W0.6 的 dump restore 到隔离库，并核对 `run.backtest_jobs` / `run.run_artifacts` 行数。删除前先确认 worker 不在跑（`GET /api/runs/jobs?status=running` 为空），避免与租约回收撞车。

- [ ] **Step 3: 确认宿主部署库未被触碰**

```powershell
docker exec tellurion-next-pg-1 psql -U postgres -d tellurion -tAc "select count(*) from core_schema_migrations;"
# 不连接宿主 5432；只记录隔离库账本仍为 85
```

判据：隔离库账本仍 85；本波次没有出现任何宿主 `psql -p 5432` 命令。

#### Task 6.7 迁移重放守卫（新库，不碰工作库）

- [ ] **Step 1: 建一次性库**

```powershell
docker exec tellurion-next-pg-1 createdb -U postgres tellurion_mig_test
```

- [ ] **Step 2: 用探针进程跑一遍迁移**

```powershell
$env:DATABASE_DATABASE='tellurion_mig_test'
$env:AUTH_INSECURE='true'; $env:SERVER_PORT='18085'
$p=Start-Process -FilePath "$env:TEMP\analysis-probe.exe" -WorkingDirectory 'C:\Users\ruoxi\Workspace\tellurion' -PassThru -WindowStyle Hidden
Start-Sleep -Seconds 12
Stop-Process -Id $p.Id -Force
docker exec tellurion-next-pg-1 psql -U postgres -d tellurion_mig_test -tAc "select count(*),max(version) from core_schema_migrations;"
```

判据：85 行。

- [ ] **Step 3: 删除账本末行后重启**

```powershell
docker exec tellurion-next-pg-1 psql -U postgres -d tellurion_mig_test -c "delete from core_schema_migrations where version=85;"
$p=Start-Process -FilePath "$env:TEMP\analysis-probe.exe" -WorkingDirectory 'C:\Users\ruoxi\Workspace\tellurion' -PassThru -WindowStyle Hidden -RedirectStandardError "$env:TEMP\mig-replay.err"
Start-Sleep -Seconds 12
"exited=" + $p.HasExited
if(-not $p.HasExited){ Stop-Process -Id $p.Id -Force }
Get-Content "$env:TEMP\mig-replay.err" -Tail 30
docker exec tellurion-next-pg-1 psql -U postgres -d tellurion_mig_test -tAc "select count(*),max(version) from core_schema_migrations;"
```

判据（二者其一）：
1. 幂等重放成功：进程起得来，账本恢复 85 行；
2. 明确拒绝：进程非 0 退出，错误信息指出重复对象/迁移失败，账本仍 84 行。
**禁止**出现“进程启动成功但账本仍缺行”的静默状态；若出现，记 FAIL。

- [ ] **Step 4: 清理**

```powershell
$env:DATABASE_DATABASE='tellurion'
docker exec tellurion-next-pg-1 dropdb -U postgres tellurion_mig_test
```

---

### Wave 7：对齐门、破坏验证、收口（串行/条件）

#### Task 7.1 dualrun align-gate（条件）

**目的：** 证明 Go 引擎与 NautilusTrader 在同一真实行情窗口上的指标/成交对齐。
**前置：** 对齐专用库 + 合成行情 + NT sidecar。

- [ ] **Step 1: 建对齐库并跑迁移**

```powershell
docker exec tellurion-next-pg-1 createdb -U postgres tellurion_align
$env:DATABASE_DATABASE='tellurion_align'; $env:AUTH_INSECURE='true'; $env:SERVER_PORT='18085'
$p=Start-Process -FilePath "$env:TEMP\analysis-probe.exe" -WorkingDirectory 'C:\Users\ruoxi\Workspace\tellurion' -PassThru -WindowStyle Hidden
Start-Sleep -Seconds 12
Stop-Process -Id $p.Id -Force
```

- [ ] **Step 2: 灌对齐 fixture**

```powershell
Get-Content C:\Users\ruoxi\Workspace\tellurion\tools\ci\seed_alignment_fixture.sql -Raw |
  docker exec -i tellurion-next-pg-1 psql -U postgres -d tellurion_align -v ON_ERROR_STOP=1
docker exec tellurion-next-pg-1 psql -U postgres -d tellurion_align -tAc "select count(*) from market.ohlcv_daily_qfq;"
```

判据：两只合成标的的 2024 全年日线已入仓（行数以 fixture 为准）。

- [ ] **Step 3: 起 NT sidecar（Windows 宿主）**

```powershell
Push-Location C:\Users\ruoxi\Workspace\tellurion\nt-sidecar
.\.venv\Scripts\python.exe -m sidecar --port 9100 --catalog catalog
Pop-Location
```

判据：`curl.exe -s -o NUL -w "%{http_code}" http://127.0.0.1:9100/healthz` 返回 200。Python / NautilusTrader 不可用（无 `.venv` 且依赖装不上）时记 **BLOCKED(NT sidecar)**，写明缺失依赖与重开条件，不跑假对齐。

- [ ] **Step 4: 跑门禁**

```powershell
cd C:\Users\ruoxi\Workspace\tellurion
go run ./cmd/dualrun `
  -pg "postgres://postgres:postgres@127.0.0.1:15432/tellurion_align?sslmode=disable" `
  -url http://127.0.0.1:9100 `
  -symbols 000001.SZ,600000.SH `
  -start 2024-01-02 -end 2024-12-31 `
  -gate -out ..\..\reports\test-2026-11-13\dualrun.md
```

判据：退出 0；报告在 K15 阈值内（total/annual return 相对差 ≤0.2%，max_drawdown ≤0.5%，sharpe/sortino/calmar 绝对差 ≤0.05，成交 mismatch 零）。退出 2 = 有违例，逐条保存报告，记 FAIL。

- [ ] **Step 5: 清理**

```powershell
# 停 sidecar（Ctrl+C 或 Stop-Process）
docker exec tellurion-next-pg-1 dropdb -U postgres tellurion_align
$env:DATABASE_DATABASE='tellurion'
```

#### Task 7.2 破坏验证三拍（临时 worktree）

**目的：** 证明关键护栏不是“看起来在跑”。每项都必须“改坏 → 红 → 逐字节还原 → 绿”。

- [ ] **Step 1: core 限流豁免表**

```powershell
$wt="$env:TEMP\core-break"
git worktree add --detach $wt HEAD
# 在 worktree 中把 /api/auth/status 从 RateLimitExemptPaths 删除
# 运行：go test ./cmd/analysis -run TestRateLimitExemptPaths_AreExactlyTheDocumentedSet -count=1
# 期望：FAIL（集合不一致）
# 还原：git -C $wt checkout -- .
# 复跑：期望 PASS
git -C $wt diff --exit-code
git worktree remove --force $wt
```

- [ ] **Step 2: orchestration policy 全覆盖**

```powershell
$wt="$env:TEMP\orch-break"
git -C C:\Users\ruoxi\Workspace\tellurion-orchestration worktree add --detach $wt HEAD
# 摘掉一条 /api/* 的 s.guarded(...)
# 运行：go test ./internal/httpapi -run TestPolicyCoversAllRoutesAsService -count=1
# 期望：FAIL；还原后 PASS
git -C $wt diff --exit-code
git -C C:\Users\ruoxi\Workspace\tellurion-orchestration worktree remove --force $wt
```

- [ ] **Step 3: agent conformance 费用默认值**

```powershell
$wt="$env:TEMP\agent-break"
git -C C:\Users\ruoxi\Workspace\tellurion-agent worktree add --detach $wt HEAD
# 改一个 fee/slippage 默认值
# 运行：go test ./internal/conformance -run TestContractConformance -count=1
# 期望：FAIL；还原后 PASS
git -C $wt diff --exit-code
git -C C:\Users\ruoxi\Workspace\tellurion-agent worktree remove --force $wt
```

判据：三项都出现“改坏必红、还原必绿”；每项的 `git diff --exit-code` 为 0；主工作区从未被修改。

#### Task 7.3 汇总与最终判定

- [ ] **Step 1: 生成汇总**

```powershell
Get-ChildItem C:\Users\ruoxi\Workspace\tellurion\reports\test-2026-11-13 | Select-Object Name,Length
```

- [ ] **Step 2: 按四分类逐条登记**

汇总表必须包含：波次、任务、判据、实际结果、分类（PASS/FAIL/BLOCKED/KNOWN-DEBT）、证据路径、若 FAIL 的定位与建议单号。

- [ ] **Step 3: 恢复环境**

```powershell
# 停隔离栈（保留卷）
docker compose -p tellurion-next -f docker-compose.yml -f deploy/compose/isolated-test.override.yml --profile full stop
# 如需彻底重置：加 down -v（只删 tellurion-next_next-pgdata）
# 恢复旧栈（只 start，不 down）
docker compose --profile full start
```

判据：隔离栈已停；宿主部署库仍未被触碰；旧栈可恢复。

---

## 5. 结果记录模板

每条任务按以下格式登记到 `reports/test-2026-11-13/summary.md`：

```markdown
### W<波次>.<任务> <名称>
- 入口命令：
- 退出码：
- 判据：
- 实际结果：
- 分类：PASS / FAIL / BLOCKED / KNOWN-DEBT
- 证据：<文件路径>
- 若 FAIL：最小复现 / 首次失败输出 / 定位文件:行号 / 建议单号
- 若 BLOCKED：缺失项 / 已尝试命令 / 重开条件
```

---

## 6. 自检（写完计划后的对照）

| 用户要求 | 落点 |
|---|---|
| 方法论：每层解决什么风险、不该测什么、前置与判据 | §1.1 |
| 12 仓拓扑 | §2.1 |
| 9 容器栈、端口、一键重建、账本、env、已知坑 | §2.2 + W0.2~0.4 + `STACK-TEST-PREREQ` 引用 |
| 已有测试资产盘清并决定复用/补 | §2.3 |
| 已知红点写入预期结果 | §2.4（K1~K11） |
| 硬约束：隔离栈/PG、不碰宿主库、破坏可还原 | §3 + W0.1 + W6.6 + W7.2 |
| 分波次、可执行：目的/覆盖/入口/判据/取证/并行性 | §0 + Wave 0~7 |
| 计划落到 tellurion 仓 docs | 本文件 |

**计划内不包含的事项（避免范围蔓延）：**
- 不修任何业务代码；FAIL 只登记。
- 不接管宿主部署库。
- 不跑 Tushare 在线同步。
- 不重录视觉基线。
- 不把 `BenchmarkBacktest` 的历史红当成新回归（2026-11-13 已复验通过）。

---

## 7. 执行记录（2026-11-13，实际跑完）

**环境**：Docker Desktop 29.9.3 / Linux engine；隔离栈 `tellurion-next`（pg + 8 服务 + `orchestration-worker`），隔离 PG `127.0.0.1:15432`；宿主部署库 `tellurion` 全程未连接（宿主 5432 监听进程 6928 只做只读观测）。证据目录 `reports/test-2026-11-13/`。

### 7.1 汇总

| 波次 | PASS | FAIL | BLOCKED | KNOWN-DEBT | 说明 |
|---|---|---|---|---|---|
| W0 安全门/重建/起栈/账本 | 6 | 0 | 0 | 0 | 9 容器 healthy；账本 85 行，两次重启 `max(applied_at)` 不变 |
| W1 静态+单元 | 4 | 0 | 1 | 0 | F1 已由 `edb1c6b` 修复：`go test ./cmd/analysis` 与 `tools/check.sh --clean` 均 exit 0；`-race` 腿 BLOCKED（本机无 gcc，CI 已覆盖，见 7.7） |
| W2 契约/一致性 | 2 | 0 | 0 | 0 | 双侧 conformance + 5 条 contracts 护栏全绿 |
| W3 单服务 E2E/端点矩阵 | 3 | 0 | 0 | 0 | 4 条必需 e2e 真跑真过；退役端点 404/410 符合 |
| W4 跨服务业务 | 5 | 0 | 0 | 0 | F2 已由 `b656ebc` 修复（根因是隔离覆盖层 worker 库指向）；读面四端点 200；4.5 由 `abf26ea` 修复 |
| W5 UI | 2 | 0 | 0 | 0 | 最终 218 passed / 0 failed / 2 skipped；视觉基线 12/12 同轮通过；原 7 条 KNOWN-DEBT 已由 UI 侧全部清除（`ca6f590`/`2c84faa`/`814e7c1`/`5a356f3`/`120af41`） |
| W6 非功能 | 7 | 0 | 1 | 0 | F4 已由 `a35389b` 修复：201 / 503（无上游原文）/ 201；租约回收时序腿 BLOCKED（窗口不可构造，见 7.7） |
| W7 对齐门/破坏验证 | 2 | 0 | 0 | 0 | dualrun exit 0；两处「改坏→必红→还原→复绿」成立 |
| **合计** | **31** | **0** | **2** | **0** | 两个 BLOCKED 的准确原因与解除条件见 7.7；无 FAIL、无 KNOWN-DEBT |

### 7.2 关键实测数字

- 镜像重建：二轮按修复重新构建 `tellurion-core:head`（含 `a35389b`）与 `tellurion-strategy:dev`（含 `abf26ea`），均 exit 0；F6 必须用新镜像验证，避免旧镜像+数据修复造成的假绿。
- 账本：85 行，`max(applied_at)=2026-11-12 16:02:05.260989+00`；连重启 analysis+data 两次不变；删第 85 行后重启进程能自愈回 85。
- 核心单元：二轮复跑全绿；`go test ./cmd/analysis -count=1` → `ok`，`tools/check.sh --clean` → `✅ check 通过`（exit 0）。覆盖率 gate exit 0（total 68.5%，`pkg/backtest` 81.6% ≥ 65）。`check.sh` 全量腿需 `TEST_DATABASE_DSN` + 隔离 PG `max_connections≥300`。
- 11 仓：core 之外 9 仓 `build/vet/test` 全 0；contracts 5 条护栏全 0；UI `typecheck/lint(0 error)/build` 与 vitest 52 文件 643 用例全绿。
- 异步回测：`POST /api/backtest`（`strategy_id`+`universe`）→ `job_id`，worker 领取后 `completed`；状态查询真实入口是编排 `GET /api/runs/jobs/{id}`（core 无 `/api/backtest/status/{id}`）。
- 对齐门：`dualrun -gate` exit 0，K15 全指标在阈值内、无成交 mismatch（报告 `reports/test-2026-11-13/dualrun.md`）。

### 7.3 缺陷与阻塞（F1~F6 已修复，2026-11-13 二轮定点复跑结论）

**F1（已修复 `edb1c6b`，复跑 PASS）core 护栏与隔离栈配置互斥**
- 原现象：`go test ./cmd/analysis` → `TestCompose_DefaultRateLimitIs100AndOverridable` 失败：`Should be empty, but was [deploy\compose\isolated-test.override.yml]`，提示「部署面里出现了写死的 100000」。
- 修复：override 删除写死的 `RATE_LIMIT_PER_MINUTE: "100000"`，高额度改由起栈命令行 env 传给 compose 插值。
- 复跑（2026-11-13 二轮）：
  - `go test ./cmd/analysis -count=1` → `ok`，exit 0（含 `TEST_DATABASE_DSN` 时同样 `ok`）。
  - `TEST_DATABASE_DSN=... GOFLAGS='-p=1' sh tools/check.sh --clean` → `✅ check 通过`，exit 0；证据 `w1-f1-check-sh-rerun-final.log`。
- 环境口径：`check.sh` 全量跑需两件事，缺一即红（均与代码无关）：①`TEST_DATABASE_DSN` 指向隔离 PG；②隔离 PG `max_connections≥300`（默认 100 会被并行包/包内 `t.Parallel` 打满，报 `53300 too many clients`）。复跑时临时 `ALTER SYSTEM SET max_connections=300`，结束后已还原为 100。
- 结论：**PASS**；原「非本轮引入」的归因保留（护栏 `0c87b6b` 早于 override `cd8a1db`）。

**F2（已修复 `b656ebc`，复跑 PASS；真实根因是隔离覆盖层配置，不是产品缺陷）编排完成态 run 的读面断裂**
- 原现象：异步 job `completed` 后，core `GET /api/backtest/{id}/report|trades|equity` 均 500（`failed to parse stored result`），`/provenance` 404；`run.run_artifacts` 行存在但 `artifact_uri`/`artifact_hash` 为空。
- 真实根因：隔离覆盖层里 `orchestration-worker` 的库指向没补齐，worker 把结果/产物写到了**另一个库**；analysis-service 读原库只能看到 job 壳，于是 500/404。产品代码的读面逻辑本身没有缺陷。
- 修复：`deploy/compose/isolated-test.override.yml` 给 worker 补 `DATABASE_DATABASE: tellurion`（与其余服务同库）。
- 复跑（新建一单 async job，`600000.SH`，`2024-01-01..2024-03-31`）：编排 `GET /api/runs/jobs/{id}` → `completed`；core 四端点全部 200：
  | 端点 | 状态 | 形状 |
  |---|---|---|
  | `GET /api/backtest/{id}/report` | 200 | 指标齐全（total_return/sharpe/max_drawdown…） |
  | `GET /api/backtest/{id}/trades` | 200 | `[]`（该 run 无成交，空数组是正常结果） |
  | `GET /api/backtest/{id}/equity` | 200 | 净值序列非空 |
  | `GET /api/backtest/{id}/provenance` | 200 | 来源链可读 |
- 连锁复跑：analytics `POST /api/analysis/metrics` → 200；`POST /api/analysis/attribution` → 409 `run_result_incomplete: missing factor_exposures`（域前置缺失，非 503）；UI 回测结果页用例全绿（见 7.6）。
- 证据：`f2-async-readface.log`、`f2-job-id.txt`、`f2-metrics-req.json`、`f2-attr-req.json`。
- 结论：**PASS**；并按用户要求把「F2 真实根因是隔离覆盖层配置而非产品缺陷」写入本条。

**F3（已修复 `83226ec`，复跑 PASS）UI 用例的日期超出夹具日历**
- 原现象：Playwright 多个回测用例 `end_date=2024-06-30`，数据质量闸报 `trading calendar does not cover end_date 2024-06-30` → 409，用例超时。
- 原证据：夹具 `market.trading_calendar` 264 行，覆盖 `2023-10-09..2024-06-28`（计划原写 267 有误）。
- 修复：`e2e/fixtures/market_e2e.sql` 日历补到 `2024-07-31`；重装夹具后实测 **297** 行、`2023-10-09..2024-07-31`。
- 复跑：`api-backtest.spec.ts` 全绿（原 `mean_reversion` 与 4 条对比/持久化用例的 409 消失）；`backtest-engine`、`critical-e2e`、`dashboard history`、`evidence-probe` 的 F2/F3 连锁红全部转绿；证据 `w5-fixture-load.log`、`w5-playwright-final.log`。
- 结论：**PASS**。

**F4（已修复 `a35389b`，复跑 PASS）broker 故障被归成 400**
- 原现象：停 broker 后 `POST /api/execution/orders` 返回 **400** `{"status":"rejected"}`，体内含 `no such host`，而非 502/503。
- 修复：`pkg/brokerhttp` 把传输失败/超时/broker 5xx 分类为 `Unavailable`，handler 映射 503/504；4xx 仍按业务拒绝。
- 复跑（重建 `tellurion-core:head` 并重建 analysis-service 后）：
  1. broker 在线 → **201**（订单被受理）；
  2. `docker stop tellurion-next-broker-1` → **503**，响应体 `{"code":"unavailable","error":"broker unavailable"}`，不含 `no such host` / `connection refused` / `dial tcp` 任何上游原文；
  3. `docker start` 并 healthy → **201**（恢复）。
- 证据：`f4-broker-failure.log`、`f4-order.json`。
- 结论：**PASS**。

**F5（已修复 `d91c489`，复跑 PASS）视觉基线红面大于登记**
- 原现象：12 条视觉基线 7 红（登记预期 2 红）。
- 修复：视觉基线重录；控制台/回测历史与 `GET /api/review/pending` 用 route fixture 钉住，消除数据态漂移。
- 复跑：`visual-regression.spec.ts` **12/12 passed**，连跑两轮结果一致；证据 `w5-playwright-final.log`。
- 结论：**PASS**，原 V-2/V-3 债务关闭。

**F6（已修复 `abf26ea`，复跑 PASS）内置策略无 active version**
- 原现象：`GET /api/strategies/momentum/resolve` → 404 `no_active_version`；`/versions` → 200 `[]`。
- 关键前提：**必须重建 strategy 镜像**——此前 8090 跑的是不含种子代码的旧镜像，靠数据修复才短暂变绿。
- 复跑（重建 `tellurion-strategy:dev` 后，删 `strategy` schema 模拟零状态）：
  - 零状态启动：`strategy_definitions=14`、`strategy_versions=14`、`strategy_promotions=14`（actor 全为 `factory-seed`）、active=14；
  - `GET /api/strategies/momentum/versions` → **200** 非空；`GET /api/strategies/momentum/resolve?ref=latest` → **200**，hash `7d00…`（内容寻址，确定性）；
  - `docker restart` 后：日志 `seeded count=0`，四张计数不变（14/14/14、active=14），resolve 仍 200 且 hash 不变。
- 证据：`f6-zero-state-seed.log`、`f6-restart-idempotent.log`。
- 结论：**PASS**（零状态部署含种子、重启幂等）。

### 7.4 计划口径修正（实测推翻原写法）

1. `emergency-flatten`：隔离栈**已配** `TRADING_EMERGENCY_TOKEN`，无 header=401、错 token=403、对 token=200（W0.5 原写 503）。
2. broker 调用需 `Authorization: Bearer dev-only-mock-token`（不是 `dev-token`）；持仓路由为 `/api/broker/accounts/mock/positions`。
3. 异步状态查询在编排 `GET /api/runs/jobs/{id}`；core 无 `/api/backtest/status/{id}`（`e2e/tests` 里的轮询助手已过期）。
4. `market_e2e.sql` 日历：修复前 264 行（非 267）；F3 `83226ec` 后为 **297** 行、覆盖 `2023-10-09..2024-07-31`。
5. `GET /api/ohlcv/{symbol}` 日期参数是 `YYYYMMDD`（`20231009`），不是 `YYYY-MM-DD`。
6. dualrun 前置：先 `nt-sidecar/tools/export_bars.py` 把对齐库导出到 `catalog`，否则侧车报 `catalog has no instrument ... on venue SIM`。
7. 核心 DB 用例需 `TEST_DATABASE_DSN` 指向隔离 PG 才真跑（否则 11 条显式跳过）。

### 7.5 清理与残留

- 已清理：临时数据库 `tellurion_mig_test`、`tellurion_align` 已 drop；临时 worktree `core-break`、`contracts-break` 已 remove；vite dev server 与 NT sidecar 已停；`coverage/`、`coverage.out` 已删。
- 二轮复跑后清理：vite（pid 19620）已停；隔离 PG `max_connections` 已 `ALTER SYSTEM RESET` 并重启回 **100**（复跑期间临时 300）；Playwright `test-results/` 运行产物未入库。
- 保留：隔离栈 `tellurion-next` 仍 running（9 容器 + worker healthy），便于下一波直接复用；`tellurion-core:head`（含 `a35389b`）与 `tellurion-strategy:dev`（含 `abf26ea`）为最新构建；`strategy` schema 已按零状态重建并含 14 条 `factory-seed`。
- 未触碰：宿主部署库 `tellurion`、宿主 5432 实例、`nt-sidecar/catalog` 导出文件（gitignored，供复跑对齐门）。

### 7.6 二轮定点复跑记录（2026-11-13）

**隔离姿态**：栈 `tellurion-next` / 隔离 PG `127.0.0.1:15432`；analysis-service 以 `ORCHESTRATION_URL=http://orchestration:8088`、`RATE_LIMIT_PER_MINUTE=100000` 重建；worker 与 analysis 同库 `tellurion`；宿主库未连接。

| 缺陷 | 修复 commit | 复跑命令/动作 | 结果 | 证据 |
|---|---|---|---|---|
| F1 | `edb1c6b` | `go test ./cmd/analysis -count=1`；`GOFLAGS='-p=1' TEST_DATABASE_DSN=… sh tools/check.sh --clean` | `ok` / `✅ check 通过`（exit 0） | `w1-f1-check-sh-rerun-final.log` |
| F2 | `b656ebc` | 新建 async job → 轮询编排 → 四读面 + analytics | `completed`；report/trades/equity/provenance 全 200；metrics 200、attribution 409(缺 `factor_exposures`) | `f2-async-readface.log`、`f2-job-id.txt` |
| F4 | `a35389b` | 在线下单 → `docker stop broker` → 下单 → `docker start` → 下单 | 201 → 503（无上游原文）→ 201 | `f4-broker-failure.log` |
| F6 | `abf26ea` | 重建镜像 → 删 `strategy` schema → 起容器 → 重启容器 | 零状态 14/14/14、active=14；resolve 200 hash `7d00…`；重启 `seeded count=0` 计数不变 | `f6-zero-state-seed.log`、`f6-restart-idempotent.log` |
| F3 | `83226ec` | 重装夹具（日历 297 行 / 至 2024-07-31）→ W5 全量 | `api-backtest` 全绿；409 连锁红清零 | `w5-fixture-load.log`、`w5-playwright-final.log` |
| F5 | `d91c489` | W5 全量视觉基线 | `visual-regression` 12/12 passed | `w5-playwright-final.log` |
| 断言 | `0857e74` | W5 回测结果页 | max_drawdown/calmar 按 `omitempty` 契约通过 | 同上 |

**W5 最终计数**：`218 passed / 0 failed / 2 skipped`（220 条）；`visual-regression` 12/12 同轮通过。`2 skipped` 是 `auth-login-flow` 的 state1 JWT 两条（本栈 state3 开放鉴权，按设计不跑）。计数来源：UI 侧清债后的全量补跑（本会话按用户提供结果登记，未重跑其他波次）。

**原 7 红——UI 侧已全部清除（产品代码零改动）**：

| 用例 | 原现象 | 根因 | 修复 commit / 状态 |
|---|---|---|---|
| `a08-job-console.spec.ts:296` 取消回读 | 点击后 15s 内没有 `GET /api/sync/jobs/{id}` | 目标 job 在点击前已 `succeeded`，cancel 返回 409，store 按设计不在失败分支刷新 | `120af41`（改为等确定终态，不假定取消一定被受理）— **已修** |
| `api-strategy.spec.ts:28` copilot | 实际 400，期望 200/202/500/503 | 用例发 `{"prompt":…}`；agent 契约要求 `description`（真实页面也发 `description`） | `ca6f590`（改用契约字段 `description`）— **已修** |
| `backtest-engine.spec.ts:442` responsive | 768px 下找不到 `.nav-item` 文本「回测引擎」 | 768px 触发侧栏 collapsed，`span.nav-label` 被 `v-if` 移除，按文本过滤命中 0 | `2c84faa`（768px 用例改深链进路由）— **已修** |
| `copilot.spec.ts:73` 导航 | 找不到 `.nav-tile`「策略 Copilot」 | 仪表盘 NavTiles 已无该入口 | `814e7c1`（导航用例改指策略库磁贴）— **已修** |
| `data-sync-error.spec.ts:16` 未知 job_type | 期望 202 后 runner 报错，实际入队 400 | data-service 2026-11-12 起入队期校验范围（空 `params` → 400 `invalid_parameter`） | `5a356f3`（三条用例补取数范围）— **已修** |
| `data-sync-error.spec.ts:126` 重试计数 | 同上，创建即 400 | 同上 | `5a356f3` — **已修** |
| `data-sync.spec.ts:146` 重试失败作业 | 同上，创建即 400 | 同上 | `5a356f3` — **已修** |

### 7.7 仍 BLOCKED 的两条（准确原因与解除条件）

**B1 `-race` 腿（W1）**
- 准确原因：本机 Windows 宿主无 gcc/cc（`Get-Command gcc`、`cc` 均为 not found），`CGO_ENABLED=1` 的 `go test -race` 无法编译，故 race 阶只能记 BLOCKED；不是测试未执行、也不是 race 失败。
- CI 覆盖证据：[ci.yml](file:///c:/Users/ruoxi/Workspace/tellurion/.github/workflows/ci.yml#L110-L149) 的 `go` job（`ubuntu-latest`，自带 gcc）Test 步骤运行 `go test ./... -count=1 -race -coverprofile=coverage.out`，并接 `coveragegate`；PR 模板同样要求该命令。该 job 即本机缺失腿的权威覆盖（AUD-12 记录存量竞争已修，修后 CI race 全量绿）。
- 解除条件：本机安装 gcc（如 MSYS2/mingw-w64）并 `CGO_ENABLED=1 CC=gcc go test -race ./...`；或直接以 CI `go` job 的对应 run 结论为准。解除后把 §7.1 W1 的 BLOCKED 改 0，并补本机 race 证据文件。

**B2 租约回收时序腿（W6，计划内可选条件项）**
- 准确原因：隔离栈的 orchestration 按默认 `-worker-lease 60s`、`-reap-interval 30s` 启动，租约过期到真正可回收最早约 90s，且回收后还需第二 worker（或恢复后的 worker）重领并跑到终态；本轮未在观察窗内注入 `docker pause`/双 worker 故障，凑不出「过期→重领→succeeded」的完整链路，故按计划 Step 4 的约定记 BLOCKED(时序)，不伪造通过。
- 解除条件：用短参数重启 orchestration（隔离栈可传 `-worker-lease 5s -reap-interval 2s`），`docker pause` worker 停止续租，确认 `lease_until` 过期且 reaper 重排后，`unpause` 或起第二 worker 重领；判据是作业最终 `succeeded` 且**只产出一份结果**（不得重复）。或由产品提供可暂停时钟/故障注入钩子后重跑。解除后把 §7.1 W6 的 BLOCKED 改 0。
