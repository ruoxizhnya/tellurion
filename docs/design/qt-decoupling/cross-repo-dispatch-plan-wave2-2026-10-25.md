---
status: active
last-verified: 2026-10-25
verified-by: 上一批 15 条裁决全部落定后的重新快照（12 仓 `git fetch` + `git status` + `git rev-list --count origin/main..HEAD` + 未完成项台账扫描 + 逐项代码级实测）
---

# 跨仓施工分组方案 · 第二批（上一批完工后的新任务）

> 前序：[跨仓施工分组方案（第一批）](cross-repo-dispatch-plan-2026-10-25.md) · [跨仓裁决记录](cross-repo-decision-record-2026-10-25.md)（15 条裁决已落定，本批按其口径施工）。
> 快照时刻：**2026-10-25 12:30**（本地实测，`git fetch` 全部 12 仓后）。
> **本批的性质**：第一批的施工组已完工（QT-73 / QT-32-A / risk service / data service / analytics R1 / orchestration 子 run 入队 / broker G7 / ui 前置 / docs DOC-06）。本批是**剩下的单**。

---

## 第 1 节 · 总览表

| 组 | 仓 | 组内串行顺序（单号） | 前置（等哪个组的哪个具体步骤） | 可与谁并行 |
|---|---|---|---|---|
| **H1** | `tellurion`（core） | QT-71 → QT-72 | QT-71 **零外部前置**（kit `v0.1.2` 已推，实测确认）；QT-72 等 **H1 内 QT-71**（同仓同文件 `cmd/analysis/route_policy.go` + `handlers_backtest.go`） | H1 与 H2~H7 全程并行（不同仓） |
| **H2** | `tellurion`（core） | QT-51 | **零外部前置**（本批独立一单） | **H2 与 H1 同仓 ⇒ 必须串行或用 git worktree 分头**（见 §2 撞车点） |
| **H3** | `tellurion-risk` | QT-42 收尾 ①：`bundle` → `ParseBundle` → 构建 `RiskPolicy` | 等 **H1 的 QT-71**（service token 要先能签发、risk 的 resolve/events 策略表要先加 `"service"`——**实测已落**）⇒ **实际只剩「core 侧消费」这一半** | H3 与 H1/H2/H4/H5/H6/H7 并行 |
| **H4** | `tellurion-data` | QT-40 收尾 ①：写面装配接线 → ②：物理 `SET SCHEMA` 搬迁 | ① 的前置 = **core 停第二写者**（`pkg/data/source/etl.go` 仍在 + `cmd/data` 两个 executor 仍在用）⇒ **需 H2 组协调**；② 前置 = ① 完成 | H4 与 H1/H2/H3/H5/H6/H7 并行（写面域与 core 引擎域零重叠） |
| **H5** | `tellurion-orchestration` | QT-44 收尾 ①：worker 端点 + worker 进程 → ②：父汇总真实写回 | ① 的前置 = **core 侧的「派活契约」**（`Claim`/`ClaimByKernel` 已在本仓落，**缺的是 core 引擎当 worker 进程**）⇒ **跨仓且需 core 参与**；② 的前置 = ① | H5 与 H1/H2/H3/H4/H6/H7 并行 |
| **H6** | `tellurion-analytics` | QT-43 收尾 ①：R2 契约化（按裁决 ③ **留在本仓**）→ ②：token 分发 | ① **零外部前置**（裁决 ③ 已定「不迁 contracts」，只做本仓 DTO 冻结 + 触发条件登记）；② 依赖 **H1 的 QT-71** | H6 与 H1/H2/H3/H4/H5/H7 并行 |
| **H7** | `tellurion-ui` + `tellurion-docs` + `tellurion-broker` | 三条**互不相关**，各自独立 | 全部**外部/环境前置未解除** ⇒ 本批**只出决策材料**（见第 5 节） | — |

---

## 第 2 节 · 分组说明

### H1 · core：`QT-71` → `QT-72`

**组内必须串行（撞车点实测）**

| 文件 | 被哪几单碰 | 各自只许动的范围 |
|---|---|---|
| `cmd/analysis/route_policy.go` | QT-71 / QT-72 | QT-71：增第五档 `Service` + 只在 `POST /api/backtest` 登记；QT-72：登记 `GET /api/backtest/:id/provenance` |
| `pkg/auth/auth.go` | QT-71 | `RoleService` 进 `IsValid()`；`CanTrade()`/`CanAdmin()` 恒 false |
| `pkg/authz/policy.go` | QT-71 | `Policy` 增 `Service []string` 第五档；`Middleware` 增一分档；`Entries()` 出 `"service"` |
| `cmd/analysis/handlers_backtest.go` | QT-72 | 新增 `GET /api/backtest/:id/provenance`（路由组在 `:42` `api := router.Group("/api/backtest")`） |
| `pkg/storage/backtest_jobs.go` | QT-72 | 只读 `result_status`（**只读，不加列**——`result_status` 已由 B6-3 的 Migration 059 落） |
| `deploy/k8s/*.yaml` + `docker-compose.yml` | QT-71 | 新增 `risk-deployment.yaml` / `analytics-deployment.yaml` + 三个服务的 secret 注入 |

**可立刻做的**：**QT-71 全部七条**——外部前置**已解除**（实测 kit 远端已有 `v0.1.2` → `fab6188`，`auth/service_token.go:26` 的 `IssueServiceToken` 已实现）。
**在等外部产物的**：**QT-72** 等 QT-71 先把 `route_policy.go` 档位表改完（同文件）。

### H2 · core：`QT-51`（回测/实盘执行语义分离 BE-02）

**为什么单独立组而不是并进 H1**：**QT-51 与 QT-71/QT-72 撞同一个 `pkg/backtest` 域吗？—— 实测不撞**：QT-51 动的是 `pkg/backtest/{engine.go, options.go, execution/live_bridge.go}`（实测这 3 个文件 import `pkg/live`）；QT-71/72 动的是 `pkg/auth` / `pkg/authz` / `cmd/analysis/*` / `pkg/storage`。
⇒ **文件域零重叠 ⇒ 可并行**，但**同仓 ⇒ 必须各自 `git worktree`**（同仓两个 agent 抢一个工作树会互相看到对方的未提交改动）。

**可立刻做的**：QT-51 全部——**零外部前置**。
**在等外部产物的**：无。**但它有设计前置**：台账 ② 原文「BE-02 一直只有『待切 port』的意向，**无设计**」⇒ **第一步是出设计**（`ExecutionPort` 的形状 + 谁实现 + 回测侧适配层放哪），不是直接改代码。

### H3 · risk：`QT-42` 收尾（core 侧 `ParseBundle` 消费）

**组内串行原因**：无组内多步（risk 仓侧只剩一条跨仓腿）。
**可立刻做的**：**risk 仓侧本轮零改动**——实测 `riskPolicy` 表的 `service` 角色**已落**（commit `87c6aa9`）。
**真正待做的是 core 那一半**：core 拿 `bundle` → `rule.ParseBundle` → 构建进程内 `RiskPolicy`。**实测 core 全仓 `grep ParseBundle` = 0 命中**（只有文档提及）⇒ 这条腿**主体在 core**。
⇒ **跨仓腿**（core 改、risk 不改），须协调者分派。

### H4 · data：`QT-40` 收尾（写面装配 → `SET SCHEMA`）

**组内串行原因（数据依赖）**：物理搬迁必须先停掉第二写者，否则搬一次两边各写一次 ⇒ ① → ② 有序。
**可立刻做的**：**无独立可开工项**——两条都指向 core。
**实测的跨仓腿**：core 的第二写者**实测只有两个生产点**（`cmd/data/capital_flow_executor.go:94` 与 `cmd/data/top_list_executor.go:112` 调 `source.NewETLPipeline`），**不是「整条同步腿」那么宽**——这比台账原文窄，core 侧那笔活的实际工作量比想象中小。
**在等外部产物的**：core 停第二写者。

### H5 · orchestration：`QT-44` 收尾（worker 端点 + worker 进程 → 父汇总真实写回）

**组内串行原因（数据依赖）**：worker 进程要写的状态是父汇总的输入 ⇒ ① → ② 有序。
**可立刻做的**：① 的**仓内部分**（worker 端点 + 进程骨架）——`internal/queue` 的 `JobQueue`/`WorkerRegistry`/`ClaimByKernel`/`ReapOffline` **已全落**（实测 45 条共享套件）。
**在等外部产物的**：**worker 进程要跑的是 core 的引擎** ⇒ **core 必须参与**（本仓零 import core 是硬约束）⇒ 跨仓腿。

### H6 · analytics：`QT-43` 收尾（R2 契约化 → token 分发）

**组内串行原因**：R2 改 DTO 冻结与校验语义；token 分发改装配与配置键 ⇒ 同仓有序。
**可立刻做的**：**R2 ①**——按裁决 ③，**DTO 留在本仓**（不迁 contracts），只做「形状冻结 v1 + 第二个使用方触发条件登记」。**零外部前置**。
**在等外部产物的**：**② token 分发** 等 H1 的 QT-71。

---

## 第 3 节 · 派发原文

### 派发块 H1（core）

> **派发原文**：在 `tellurion` 仓按顺序完成 `QT-71` → `QT-72`。
>
> 1. **`QT-71` · 服务间凭证 `service` 角色**（**外部前置已解除**：kit `v0.1.2` 已推，`auth/service_token.go:26` 的 `IssueServiceToken(subject, role string, ttl time.Duration) (string, error)` 已实现）：
>    - `pkg/auth/auth.go`：新增 `RoleService Role = "service"`；**必须加进 `IsValid()` 的返回集合**（实测当前集合是 `{viewer, trader, admin}` 三值，不加则签出的 token 在解析期即被判非法）；`CanTrade()` 恒 false、`CanAdmin()` 恒 false。
>    - `pkg/authz/policy.go`：`Policy` 增第五档 `Service []string`；`Middleware` 增一分档；`Entries()` 出 `"service"`。
>    - `cmd/analysis/route_policy.go`：**只在 `POST /api/backtest` 登记 `service`**（**禁止**出现在 `Trader` / `Admin` 列表）。
>    - `deploy/k8s/`：新增 `risk-deployment.yaml` / `analytics-deployment.yaml`（`data-deployment.yaml` 已存在，补 secret 即可）；`JWT_SECRET` 与三个服务 token（`RISK_SERVICE_RESOLVE_TOKEN` / `DATA_SERVICE_TOKEN` / `ANALYTICS_CORE_TOKEN`）一律 `secretKeyRef` 注入，**只经 env、不落库、不进镜像**。
>    - `docker-compose.yml`：同步给三个服务补 `JWT_SECRET`（**实测当前 `JWT_SECRET` 只出现在 analysis `:160` 与 ai `:228`，data-service 没有**）。
>    - **验收**：service token 调 `POST /api/risk/resolve`、`GET /datasets/*`、`POST /api/backtest` 均 200；同一 token 调 `POST /api/execution/orders` **403**；无 token 401；viewer token 调 resolve 仍 403；`RoleService.CanTrade()==false` 守卫测试；`route_policy.go` 的 service 登记行数 == 1；部署清单静态检查断言三处 env 存在。
>    - **破坏验证**：把 `service` 加进 `Trader` 列表 ⇒ 「service token 调下单端点 403」那条用例必须红。
>    - **跨仓腿**：risk 的两条 Roles 加 `"service"`（**实测已落**，`87c6aa9`，`/audit` 未加）；data 的两条（**实测已落**，`33b68d1`）——**本单只核不重做**。
>
> 2. **`QT-72` · X0 三 hash 对外读面落 core**（**前置：QT-71 已把 `route_policy.go` 改完**）：
>    - 在 `cmd/analysis/handlers_backtest.go` 的 `/api/backtest` 路由组（`:42`）新增 `GET /:id/provenance`。
>    - 读 `quant.run_provenance ⋈ backtest_jobs`；`result_status` **只从 `backtest_jobs` 取**（该列已由 B6-3 的 Migration 059 落，**本单不加列**）。
>    - 三态**沿用 orchestration 的三个 wire code**：`provenance_not_ready`(409) / `provenance_not_found`(404) / `provenance_missing`(500)（`invalid` 不是 `failed`）；**不引入 `producer_pending`**（按裁决 ⑬，那是 orchestration 侧自己的提示位）。
>    - 可空列显式 `null`，不省略键。
>    - 在 `cmd/analysis/route_policy.go` 登记该 GET 端点（**接手前先 `git log -1 -- cmd/analysis/route_policy.go` 确认基线**——QT-71 刚改过同一文件）。
>    - `docs/openapi.yaml` 登记端点与三态，**写完必须跑生产路由表 ↔ yaml 双向对拍 + `python tools/check_doc_links.py` 退出 0**（按裁决 ⑩）。
>    - **验收**：真 run 返回三 hash + `result_status`；四个三态用例；可空列 `null`；DTO 与 orchestration `provenanceView` 逐字对拍。
>    - **破坏验证**：把 `result_status` 的来源从 `backtest_jobs` 换成 provenance 的任意 hash 列 ⇒ 结论轴用例红。
>
> **验收（组级）**：`gofmt -l .` 空；`go build ./...` / `go vet ./...` / `go test ./... -count=1` 全绿；`python tools/check_contract_pin.py` 仍绿；`python tools/check_repo_autonomy.py` GREEN。
>
> **禁区**：不许写 `replace`；不许提交 `go.work`；**不许把 `service` 加进 `Trader`/`Admin` 列表**；不许把 token 真值写进入库 YAML；**不许在 QT-72 里让 core 写 `run_artifacts`**（那是 worker 的投影，core 不双写）；不许改 `quant.run_provenance` 的列清单；不许在 QT-72 里引入 `producer_pending`。
>
> **跨仓腿**：无新增（risk / data 的 service 档位已落）。

---

### 派发块 H2（core，独立 worktree）

> **派发原文**：在 `tellurion` 仓完成 `QT-51`（回测/实盘执行语义分离 BE-02）。**必须用独立 `git worktree`**（与 H1 同仓不同域）。
>
> 1. **第一步是出设计，不是改代码**（台账 ② 原文：「BE-02 一直只有『待切 port』的意向，**无设计**」）：
>    - 读 `docs/design/qt-decoupling/backtest-engine-review.md` 的 BE-02 行（`:33`）与 `:145` 的未决问题「回测/实盘共用执行语义，是『同构桥』还是该拆？」。
>    - 读 `docs/design/qt-decoupling/backtest-extraction.md:46`（「**必须切断** `→ pkg/live`」）与 `:92`（「BE-02 依赖反转：把 LiveBridge 从引擎移出，改为**实盘驱动持有引擎**」）。
>    - 产出：`ExecutionPort` 的**方法集**（下单 / 撮合 / 成交回报）、**两个实现各住哪**、**回测侧适配层放哪**、以及 §5-③ 那条「若保留『同构桥』为有意设计，必须写明并在测试里锁住『改 live 不改回测结果』」的取舍。
>    - **设计里必须逐条点名实测到的耦合面**（这是本单的真实工作量，不是泛泛的「解耦」）：
>      | 符号 | 引用数 | 出处 |
>      |---|---|---|
>      | `live.LiveTrader` | 12 | `pkg/backtest/engine.go` / `options.go` |
>      | `live.OrderResult` | 13 | 同上 + `execution/live_bridge.go` |
>      | `live.EmergencyFlattenResult` | 4 | `execution/live_bridge.go` |
>      | `live.ReconcileSignals` | 3 | 同上 |
>      | `live.PositionInfo` / `AccountInfo` | 各 2 | `pkg/backtest/engine.go` |
>      | `live.NewPaperSession` / `PaperSessionConfig` | 各 2 | `pkg/backtest/engine.go` |
>      | `live.NewMockTrader` / `MockTraderConfig` | 各 2 | 测试面 |
>      | `live.SignalDiscrepancy` | 1 | `pkg/backtest/execution/live_bridge.go` |
>      | `live.PaperSession` | 1 | 同上 |
>    - ⚠️ **注意 `live.Sh` 那 5 处命中是 `strings` 包的 `strings.Contains` 之类**（实测是 `LIVE.SH` 大小写不敏感的匹配），**不是 `pkg/live` 的符号** ⇒ 真实耦合面比 grep 命中数少。
> 2. **按设计落代码**：只改 `pkg/backtest/{engine.go, options.go, execution/live_bridge.go}` 及其测试；**不改** `pkg/live` 的实现语义（那是 QT-45 的通道网关域）。
> 3. **护栏**：`internal/repoguard/` 加一条「`pkg/backtest` 不得 import `pkg/live`」的 AST 腿（若设计选择保留同构桥，则该腿改为「只允许经适配层」并**点名允许的符号集**）。
> 4. **验收**：`pkg/backtest` 对 `pkg/live` 的 import 为 **0**（或仅剩接口适配层且有护栏）；**同一策略回测结果在改动 live 前后逐位一致**（要有归档的比对报告，不是「跑绿了就算」）。
> 5. **破坏验证**：把适配层改成直接调 `live` 实现 ⇒ 护栏腿必须红。
>
> **验收（组级）**：`gofmt -l .` 空；`go build ./...` / `go vet ./...` / `go test ./... -count=1` 全绿；`go test ./pkg/backtest/... -count=1` 绿。
>
> **禁区**：不动 Order/Trade 值对象；不改 T+1 簿记；不引入 MQ；**不许在 `pkg/live` 里改实现来迁就回测**；**不许把 `paper_session.go` 的 `generateSignals` 平行实现顺手改掉**（w1 所有权表 §5 已登记：paper 侧单独立单，不在 14 里顺手改——本单同理）。
>
> **跨仓腿**：无（纯 core 域）。

---

### 派发块 H3（risk + core 跨仓腿）

> **派发原文**：在 `tellurion-risk` 仓核对 QT-42 收尾状态；**主体动作在 `tellurion` 仓**（core 侧消费接线）。
>
> 1. **risk 仓侧**：本轮**零代码改动**——实测 `internal/httpapi/policy.go` 的 `riskPolicy` 表**已含** `POST /api/risk/resolve` 与 `POST /api/risk/events` 的 `"service"` 角色（commit `87c6aa9`），`GET /api/risk/audit` **未加**（正确）。
> 2. **core 侧消费接线（主体）**：
>    - 落点：core 的 `cmd/analysis/wiring/`（与 `risk_rules.go` 同域，**同文件域 ⇒ 与 H1 的 QT-71 同仓串行**）。
>    - 动作：run 提交前调 `POST /api/risk/resolve` 取 bundle → `rule.ParseBundle` → 构建进程内 `RiskPolicy` → 限额不再来自 `config/analysis-service.yaml` 的 `risk_manager.*`。
>    - **⚠️ 跨仓 import 约束**：risk 的 `Snapshot`/`SnapshotRule`/`Bundle` 三类型目前**在 risk 仓内**（`internal/rule/snapshot.go:24/37/121`），**core 不能直接 import risk 仓**（服务仓只经 contracts 交互，见 w1 所有权表 J1）⇒ **要么先按 ADR-0001 ① 把三类型迁 contracts，要么 core 走 HTTP 拿 bundle 后本地解析**。
>      - 台账 QT-42 ④ 原文：「`ParseBundle` 的 Go 类型出现第二个使用方（core）后，`Snapshot` / `SnapshotRule` 按 ADR-0001 ① **迁 contracts**」⇒ **这是本单的前置子步**，不是可选项。
>      - ⇒ **本单需要一步 contracts 发版**（新增 risk 快照类型 + `ParseBundle` 的等价物，或只迁类型由 core 自己实现解析）。
>    - **验收**：run 的限额来自 risk 快照；改 risk 规则 → `run_spec_hash` 变；同 hash 两次解析逐位一致。
>    - **破坏验证**：把 bundle 换成篡改过的（hash 不符）⇒ 必须 `ErrSnapshotMismatch` fail-closed 拒开跑。
>
> **验收（组级）**：risk 仓 `go build/vet/test ./... -count=1` 全绿（**本轮零改动也要跑一遍确认没被别处改坏**）；core 仓门禁同 H1。
>
> **禁区**：**core 禁止 import `tellurion-risk`**（服务仓只经 contracts 交互）；**不许在 risk 仓建第二套 `ParseBundle`**；不许在 core 侧自造一套 risk 规则解释（真相源在 risk 仓）。
>
> **跨仓腿**：① contracts 发版（迁 risk 快照类型）→ ② core 消费接线。**顺序不可颠倒**。

---

### 派发块 H4（data + core 跨仓腿）

> **派发原文**：在 `tellurion-data` 仓完成 `QT-40` 收尾两腿；**core 侧先停第二写者**。
>
> 1. **core 侧：停第二写者（前置，跨仓腿）**：
>    - 实测第二写者**只有两个生产点**（不是台账原文的「整条同步腿」）：`cmd/data/capital_flow_executor.go:94` 与 `cmd/data/top_list_executor.go:112` 调 `source.NewETLPipeline` → `storage.BulkInsert`（`pkg/storage/bulk_insert.go`，`ON CONFLICT` upsert）。
>    - core 侧动作：把这两个 executor 的**写路径**从 core 的 `BulkInsert`（upsert 语义）切到 **`tellurion-data` 的 `store.BulkInsert`**（内容比对语义：同内容 no-op / 异内容 `ErrContentConflict` 整批回滚），**或**在 core 侧停掉这两条作业并把职责整体交给 data 仓。
>    - **验收**：core 全仓 `grep "NewETLPipeline"` 的**生产引用**归零（实测另有 `internal/repoguard/registry_etl_wiring_test.go` 的白名单护栏，**同批同步**）；`cmd/data` 不再写 `market.` / `ingest.` 两张 schema。
>    - **破坏验证**：把白名单里多放一个文件 ⇒ 护栏腿必须红。
> 2. **data 仓侧 ①：写面装配接线** —— 把 `internal/ingest.ETLPipeline` + `internal/store.BulkInsert` 挂到本仓的同步作业层。
>    - **约束**：`BulkInsert` **只开 A 集**（B/C 集拒写）；事务前 `visible_at` 判零整批拒收；同主键同内容 no-op、异内容 `ErrContentConflict` 整批回滚。
>    - **验收**：写入路径的表只出现 A 集；缺 `visible_at` 的批次**整批不落**。
>    - **破坏验证**：把 `visible_at` 判零短路（`if false && …`）⇒ `TestWriter_RejectsWholeBatchWithoutVisibleAt` 必须红。
> 3. **data 仓侧 ②：物理 `SET SCHEMA` 搬迁** —— 把 `market` / `ingest` 的表物理迁到目标 schema。
>    - **约束**：**禁 `SET visible_at = trade_date`**（已有测试钉住）；**禁在 `Statements()` 里静默 `ALTER TABLE … SET SCHEMA`**（`internal/schema/schema.go:12` 明写这条禁令）。
>    - **验收**：迁移前后 `TableNames()` 可对账；A/B/C 三集合分类不变。
>
> **验收（组级）**：`go build/vet/test ./... -count=1` 全绿；`DATA_TEST_DSN='postgres://postgres:postgres@127.0.0.1:5432/tellurion_data_test?sslmode=disable' go test -tags backfill ./internal/schema -count=1` 绿（**生产 DSN 与非 `_test` 库名自动 skip**）。
>
> **禁区**：**不许重新引入 `/factors/{name}@{hash}` 路由**（裁-14 已撤，`TestFactorRouteIsRemoved` 钉住）；不许 `SET visible_at = trade_date`；不许在本仓实现传输（Tushare / 东财 / Mootdx 仍按 `Transports` 注入，**传输实现仍在 core**）；**不许执行生产库回填**（裁决 ⑨：暂不授权，等 core 停第二写者 + 运维执行）。
>
> **跨仓腿**：① core 停第二写者 → ② data 写面装配 → ③ data 物理搬迁。

---

### 派发块 H5（orchestration + core 跨仓腿）

> **派发原文**：在 `tellurion-orchestration` 仓完成 `QT-44` 收尾两腿；**worker 进程需要 core 参与**。
>
> 1. **仓内部分：worker 端点 + worker 进程骨架**：
>    - `internal/queue` 的 `JobQueue` / `WorkerRegistry` / `Claim` / `ClaimByKernel` / `ReapExpired` / `ReapOffline` **已全落**（实测 45 条共享套件在两个实现上都跑）⇒ **本腿只补「谁调这些接口」**。
>    - 新增 worker 侧 HTTP 端点（注册 / 心跳 / 领取 / 上报状态），落点 `internal/httpapi/`。
>    - **装配期能力断言**：`ClaimByKernel` **没有生产消费方**是台账已登记的诚实事实 ⇒ 派活装配落地时必须做**类型断言**（不提供该接口的注册表不许带着「以为会回收」启动）。
>    - `kernels` 空数组必须归一成 NULL（两条用例已钉）。
> 2. **worker 进程**：
>    - **⚠️ 硬约束**：本仓**零 import core**（台账 QT-44 ⑤-① 是本单验收项）。⇒ **worker 进程跑的是 core 的引擎 ⇒ core 必须提供「引擎当 worker」的装配**，本仓只提供派活面与状态回写。
>    - ⇒ **这是本单最大的跨仓依赖**：core 侧要有「用 core 引擎当 worker 领 job」的装配（core 已有 `contracts.EngineRunner` 与 `pkg/backtest` 引擎）。
> 3. **② 父汇总真实写回** —— worker 执行完由**执行侧**调 `parents.ApplyBatchSummary`；本仓只读。
>    - **验收**：analytics 侧的 sensitivity 逐点回填**不再靠 SQL 模拟**（台账 analytics 未落项 ① 原文：「W8 的真进程 E2E 用 SQL 模拟该写回，**不代表 worker 已存在**」）。
>
> **验收（组级）**：`gofmt -l .` 空；`go build/vet/test ./... -count=1` 全绿；`ORCHESTRATION_TEST_DSN='postgres://postgres:postgres@127.0.0.1:5432/tellurion_orchestration_test?sslmode=disable' go test ./internal/pgstore -count=1` 绿（无 DSN 时 PG 腿 skip，**skip 不算通过**）。
>
> **禁区**：**本仓零 import core**；不改 `EngineRunner` 契约；不做 K8s；不搬 `cmd/dualrun`；**不许改 `plan_canonical` / `plan_hash` 身份口径**；**不许引入 `producer_pending` 之外的第四态**（三码锚点见裁决 ⑬）；鉴权仍按裁-15 缓做（不新增 JWT 面）。
>
> **跨仓腿**：① core 提供「引擎当 worker」的装配（**core 仓，跨仓**）；② 父汇总写回后 analytics 的 sensitivity 腿可去掉 SQL 模拟（**analytics 仓，只消费**）。

---

### 派发块 H6（analytics）

> **派发原文**：在 `tellurion-analytics` 仓完成 `QT-43` 收尾两腿。
>
> 1. **R2 契约化（按裁决 ③：留在本仓，不迁 contracts）**：
>    - `internal/analysis/upstream_contract.go` 的 `UpstreamContractVersion = "v1"` 与三个校验器（`validateResultContract` / `validatePortfolioSnapshot` / `validateFactorExposures`）**语义不动**。
>    - **新增**：把「第二个使用方触发条件」成文写进 `docs/api-schema.md` §6 —— 触发条件 = 「**core 或 orchestration 出现 import analytics DTO 的 Go 代码**」（HTTP 交互**不算**）。
>    - **验收**：三个校验器的用例全绿；`docs/api-schema.md` 有触发条件段；**contracts 仓不出现任何 `analysis` 包**（守卫：contracts 仓的包清单不含 `analysis`）。
> 2. **token 分发**（**前置：H1 的 QT-71**）：
>    - 装配 `-core-token` / `-data-token`（台账 analytics 未落项 ④ 原文：「鉴权开启后 core / UI 调用本服务必须持 token（POST trader / GET viewer）；**分发不在本仓**」）—— **本仓只保证「有 token 就能用」，不负责分发**。
>    - **验收**：无 token 401 / 越权 403（`TestAuthEnforcedCurrentState` 已是可执行锚点）；带 service token 能调本服务。
>
> **验收（组级）**：`gofmt -l .` 空；`go build / go vet / go test ./... -count=1` 全绿；`ANALYTICS_TEST_DSN='postgres://postgres:postgres@127.0.0.1:5432/tellurion_analytics_test?sslmode=disable' go test ./... -count=1` 绿（无 DSN 时 PG 腿 skip，**skip 不算通过**）。
>
> **禁区**：**不做 PDF 导出**（R5 已裁为非目标）；不许把 `AuthStatus` 改回 `"unwired"`（当前 `"enforced"`，`TestAuthEnforcedCurrentState` 是**可执行锚点**、改写而非删除）；不许动 `internal/analysis/sensitivity.go` 的 fail-closed 语义；**不许新建 contracts 的 `analysis` 包**（裁决 ③）；不许在本仓造 token 分发机制。
>
> **跨仓腿**：token 分发在 core / 运维侧（**不在本仓**）。

---

## 第 4 节 · 阶段划分（消除歧义）

> **「组间并行」指的是「开工时刻可并行」，不是「组内每一步都不用等别人」。**

### 阶段 A —— 现在就能全部并行开工，组间零等待

| 组 | 开工第一步 | 为什么现在就能开 |
|---|---|---|
| **H1** | QT-71 全部七条 | 外部前置**已解除**（kit `v0.1.2` 已推、`IssueServiceToken` 已实现） |
| **H2** | QT-51 第一步：**出设计** | 纯 core 域、零外部前置；且**无设计**（台账 ② 原文）⇒ 第一步本就是设计 |
| **H4** | core 侧**停第二写者**（实测只有 2 个生产点） | 纯 core 域；实测范围比台账窄 |
| **H5** | 仓内部分：**worker 端点 + 进程骨架** | `internal/queue` 的 port 与两个实现**已全落**（45 条共享套件） |
| **H6** | R2 ①：形状冻结 + 触发条件登记 | 裁决 ③ 已定「不迁 contracts」⇒ 零外部前置 |

**阶段 A 的并行硬约束**：**H1 与 H2 同属 core 仓** ⇒ **必须各自 `git worktree`**（文件域零重叠，但同仓抢一个工作树会互相看到对方的未提交改动）。
**H1 与 H3 的 core 侧也撞**（H3 的 core 腿落 `cmd/analysis/wiring/`，H1 的 QT-71 落 `pkg/auth`+`pkg/authz`+`cmd/analysis/route_policy.go`）⇒ **H3 的 core 腿排在 H1 之后**。

### 阶段 B —— 等某个组的某个产物之后才解锁

| 被解锁的步骤 | 等谁 | 等的具体产物（可机械核对） |
|---|---|---|
| **H1 · QT-72** | **H1 · QT-71** | `cmd/analysis/route_policy.go` 的档位表已被 QT-71 改完（同一文件，接手前先 `git log -1 -- cmd/analysis/route_policy.go`） |
| **H3 · core 侧消费** | **H1 · QT-71** | service token 能签发（`IssueServiceToken` + core 的 `RoleService`）+ risk/data 的策略表已加 `"service"`（**实测已落**） |
| **H3 · core 侧消费** | **contracts 发版** | contracts 出现 risk 快照类型（`Snapshot` / `SnapshotRule`）—— **core 不能 import risk 仓**（服务仓只经 contracts 交互） |
| **H4 · data 写面装配** | **core 停第二写者** | core 全仓 `NewETLPipeline` 的**生产引用**归零（实测现为 2 处：`cmd/data/capital_flow_executor.go:94` / `cmd/data/top_list_executor.go:112`） |
| **H4 · 物理 `SET SCHEMA`** | **H4 · 写面装配** | 写面已接到本仓作业层 + core 不再写这两张 schema |
| **H5 · worker 进程** | **core 提供「引擎当 worker」装配** | core 侧「用引擎领 job」的装配就位（本仓零 import core ⇒ 不能自己造） |
| **H6 · token 分发** | **H1 · QT-71** | 三个服务的 token env 已在部署清单里（裁决 ① 落地动作） |

---

## 第 5 节 · 待裁决清单（本批新增）

| 事项 | 卡住哪个单 | 需要你给什么 |
|---|---|---|
| **① QT-51 的设计取舍** | **H2**（core） | 台账 ③-③ 原文：「若保留『同构桥』为有意设计，必须写明并在测试里锁住『改 live 不改回测结果』」。**需你拍：拆 port，还是保留同构桥并锁死行为**。证据：实测耦合面是 **13 个 `pkg/live` 符号**（`LiveTrader` 12 / `OrderResult` 13 / `EmergencyFlattenResult` 4 / `ReconcileSignals` 3 / `PositionInfo` 2 / `AccountInfo` 2 / `NewPaperSession` 2 / `PaperSessionConfig` 2 / `NewMockTrader` 2 / `MockTraderConfig` 2 / `SignalDiscrepancy` 1 / `PaperSession` 1），分布在 **6 个文件**（3 生产 + 3 测试） |
| **② risk 快照类型迁 contracts 的落点** | **H3**（risk + core） | 台账 QT-42 ④：「`ParseBundle` 的 Go 类型出现第二个使用方（core）后按 ADR-0001 ① **迁 contracts**」。**需你拍：迁到 contracts 的哪个包**——新建 `risk` 包？还是并入既有 `engine`？以及 **`ParseBundle` 这个纯函数要不要一起迁**（它是 risk 的实现细节，迁进 contracts 就成了「contracts 有实现」，与 ADR-0001 ② 冲突） |
| **③ worker 进程归谁** | **H5**（orchestration + core） | 本仓**零 import core** 是硬约束 ⇒ worker 进程跑 core 引擎 ⇒ **需你拍：worker 进程是 core 的一个可执行体（core 侧新增 `cmd/worker`），还是 orchestration 侧通过 HTTP 调 core 的引擎**。前者 core 要多一个可执行体，后者要定一条新 wire 面 |
| **④ 第二写者的停法** | **H4**（core + data） | 实测只有 2 个生产点。**需你拍：core 把这 2 条作业整体交给 data 仓（core 只留调度壳），还是 core 把这 2 处改调 data 仓的 `store.BulkInsert`**。后者 core 仍有一份「core 侧作业层」，前者 core 的 `cmd/data` 同步腿要缩 |
| **⑤ 生产库回填的执行授权** | **H4**（data） | 裁决 ⑨ 已定「暂不授权」。**需你给：core 停第二写者完成后，是否授权 + 由谁执行**（本方案**不安排**任何 agent 去写生产库） |
| **⑥ UI 三服务栈的起法** | **H7**（ui） | AUD-61 ① 批 16 条结构漂移，**前置已解除**（重构分支已并入），**今天的障碍是跑不起栈**（前端 :5173 / data :8081 / analysis :8085 + PG/Redis）。**需你给：栈由谁起**（本机 Docker 不可用：实测 `docker info` 报 `//./pipe/dockerDesktopLinuxEngine` 不存在）——还是先只跑「不需要后端」的 16 条？ |
| **⑦ XTP 柜台环境** | **H7**（broker） | 仍缺 P1~P5 五项外部条件（SDK / 测试账号 / 出网 / C 工具链 / 盯盘时间窗），**本仓与 core 与本机与 CI 四处都没有 SDK**。**需你给：这五项有没有一项能提供**；没有 ⇒ QT-45 保持 ⛔ 不排期 |
| **⑧ docs DOC-10 的范围** | **H7**（docs） | 立项依据：§7 的「已知过渡态」写「六个服务仓只有占位 `doc.go`」、阶梯表写「X1~X6 未触发」——**今天已不成立**（六仓都实装了）。**需你拍：是改 §7 的事实描述，还是连阶梯表一起重写**（后者要写清每层现在到哪了） |
| **⑨ `broker.order_acks` 保留期** | **H7**（broker） | 台账未落项：「`broker.order_acks` 的**保留期未定**（当前不自动删）」。**需你给：保留多久 / 谁负责清理**（这是运维口径，不给就一直涨） |

---

## 第 6 节 · 本批的账实不符（相对台账原文的实测订正）

| # | 台账原文 | 实测 | 处置 |
|---|---|---|---|
| 1 | data QT-40：「**core 侧同步腿仍在写 `market` / `ingest`**」（读起来像整条同步腿） | core 的**生产**调用点**只有 2 处**：`cmd/data/capital_flow_executor.go:94` 与 `cmd/data/top_list_executor.go:112` 调 `source.NewETLPipeline`；core `pkg/data/source/etl.go` 的 `BulkInsert` 走 `pkg/storage/bulk_insert.go`（`ON CONFLICT` upsert）。另有 `internal/repoguard/registry_etl_wiring_test.go` 的白名单护栏钉着「只有这两处 + 一个测试文件」 | ⇒ **停第二写者的工作量比台账读起来小得多**；本批按「2 个生产点」派工，护栏腿同批同步 |
| 2 | core QT-51 ②：「`engine.go`、`execution/live_bridge.go`、`options.go` 三处 import `pkg/live`」 | 实测 **6 个文件** import（3 生产：`engine.go` / `options.go` / `execution/live_bridge.go`；3 测试：`fakes_test.go` / `signal_reconcile_e2e_test.go` / `execution/live_bridge_test.go`）。三处是**生产**面，6 个是全量面 | ⇒ 台账的「三处」指的是生产面，**测试面还有 3 个文件**要一起改（否则护栏腿会红） |
| 3 | core QT-51 ②：「BE-02 一直只有『待切 port』的意向，**无设计**」 | 实测 `docs/design/qt-decoupling/backtest-engine-review.md:145` 有一条**未决问题**（checkbox 未勾）：「BE-02：回测/实盘共用执行语义，是『同构桥』还是该拆？」（影响回测口径稳定性）；`backtest-extraction.md:46` 把「**必须切断** `→ pkg/live`」列为必须切断项，`:92` 给了方向（「把 LiveBridge 从引擎移出，改为实盘驱动持有引擎」） | ⇒ **有方向没决策**：`:145` 那条 checkbox 就是待你勾的那一条 |
| 4 | analytics 未落项 ④：「R2 的 owner 与最终形状：owner 未定」 | 已在上一批裁决 ③ 落定（owner = 本仓，不迁 contracts） | ⇒ **已订正**，见 analytics 台账 |
| 5 | agent QT-32-A ⑦①：「`artifact_hash` 在本单没有产出方……要产出真坐标必须先把『AI 生成物的载体』裁决下来：`carrier=wasm` / `carrier=so` / 维持 ADR-024 的『只审阅不执行』」 | 三选一，**无默认值** | ⇒ **待裁**（本批 §5 之外的第 10 条；未列入本批施工组，因为它是「载体形态」决策而非施工） |
| 6 | broker QT-45 施工 ⑤：「X6c 判 ⛔ 阻塞，不是 🔶 待做」 | 实测确认：broker / core / 本机 / CI **四处零 SDK**；仓内 `internal/gateway/xtp.go` 只有自研骨架 | ⇒ 台账正确，**保持 ⛔ 不排期** |
| 7 | ui AUD-61：「① 批 ⬜ 待做，等 `feat/design-tokens-v2-palette-sweep` 合并」 | 台账已自行订正（分支已并入旧仓 main，内容随迁移进本仓）⇒ **今天的障碍是跑不起栈** | ⇒ 台账已对，**本批只需解决「栈由谁起」**（§5 ⑥） |

---

## 附：本批与上一批的衔接

| 上一批的组 | 状态 | 本批的去向 |
|---|---|---|
| G1 core（QT-73 → QT-71 → QT-72） | QT-73 ✅；QT-71 前置解除、**未开工**；QT-72 连带 | → **H1** |
| G2 kit（KT-09 → KT-10） | ✅ 两单都已发版（`v0.1.2` / `v0.1.3`） | → 本批无 |
| G3 orchestration（子 run 入队 → worker 执行） | ① ✅；② **未落** | → **H5** |
| G4 risk（ParseBundle 消费） | 未落 | → **H3** |
| G5 analytics（R1 → R2） | R1 ✅；R2 **待做** | → **H6** |
| G6 data（写面装配 → SET SCHEMA） | 未落 | → **H4** |
| G7 broker | ⛔ 决策材料已落 | → **H7**（仍阻塞） |
| G8 agent（QT-32-A） | ✅ 完成 | → 后续 A2/A3 单（§6 第 5 条待裁） |
| G9 ui | 前置已解除，① 批待做 | → **H7**（待决「栈由谁起」） |
| G10 docs | DOC-06 ✅ + DOC-10 新立 | → **H7**（DOC-10） |

---

## 维护本文件

- **新增单**：写全六字段（目标 / 上下文 / 要求 / 约束与非目标 / 验收 / 边界），并显式引用**冻结契约的路径与节号**。
- **派工前先证伪前提**：读码核对工单写的前提；不符就停下回报 + 给拆法，不硬做、不扩范围。
- **跨仓引用写 `repo:path`**（如 `tellurion:docs/TASKS.md`），不做可点击相对链接。
- **顺序纪律**：同一波次内撞同一批文件的单按单号串行，一单一 commit。
