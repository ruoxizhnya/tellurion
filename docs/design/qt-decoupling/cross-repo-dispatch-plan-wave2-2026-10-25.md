---
status: active
last-verified: 2026-10-25
verified-by: 并发 agent 落完 QT-71 / QT-72 / QT-40 H4①② / QT-44 core 腿 / QT-74 之后的重新快照（12 仓 `git fetch` + `git status` + `git rev-list --count origin/main..HEAD` + 未完成项台账扫描 + 逐项代码级实测）；**同日二次订正**：QT-51 分支态（`task/qt51-execution-port`）/ H4 剩余写者（`market.index_daily` + `public.*`）/ H6 token 已落 / data 表数 23（均以 grep 与 worktree 实测为准）
---

# 跨仓施工分组方案 · 第二批（**已按 2026-10-25 18:45 实测订正**）

> 前序：[第一批分组方案](cross-repo-dispatch-plan-2026-10-25.md) · [跨仓裁决记录](cross-repo-decision-record-2026-10-25.md)（15 条裁决已落定）。
> **本版是对本文件 12:30 版的订正版**：写完 12:30 版之后，并发 agent 落掉了 **QT-71 / QT-72 / QT-40 H4①（H4② 的 HTTP 一半）/ QT-44 的 X5c + 父汇总写回 + core 腿 `cmd/worker`**，risk 仓并新立了 **QT-74**。
> ⇒ **12:30 版的 H1 / H4 / H5 三组已大部分或全部落地**；**H3 的真实前置被改判**（不是「迁 contracts」，是 **risk 仓的 QT-74 参数词典**）。
> **本版保留 12:30 版的原始断言并逐条标注「已过期 / 仍成立」**（不删改历史），供追溯。
>
> **⚠️ 2026-10-25 二次订正（本轮）**：又逮到四处**本版自己写错**——① **QT-51 不是未开工**（分支 `task/qt51-execution-port` 已有实现，见 H2）；② **H4 物理搬迁前置不成立**（core 仍写 `market.index_daily` 与 `public.*`，见 H4）；③ **H6 token 分发已落**（不是「可立刻做」）；④ **H5 的 X0 生产者不是本仓单可施工**（缺写入端点 + core worker 回传）。逐条改在对应节与 §6。

---

## 第 1 节 · 总览表（2026-10-25 18:45 实测态）

| 组 | 仓 | 组内串行顺序（单号） | 前置（等哪个组的哪个具体步骤） | 可与谁并行 | 12:30 版的判定 |
|---|---|---|---|---|---|
| **H1** | `tellurion`（core） | ~~QT-71 → QT-72~~ | **✅ 已全部落地**（`aaf37bc` QT-71 / `4b3410a` QT-72 / `134b283` 落账） | — | **已过期** |
| **H2** | `tellurion`（core） | QT-51 **收口**（提交脏文档 → rebase 分支 → 复取门禁 → 等价性复跑 → 报裁决 → 合入） | **无外部前置**；**合入那一步等 §5 ①** | H2 与 H3/H4/H5/H6 全程并行（不同仓） | **已过期**：分支 `task/qt51-execution-port`（`7c89aae`）已有实现，`pkg/backtest` 对 `pkg/live` import = **0** |
| **H3** | `tellurion-risk` | **QT-74**（参数词典）→ 迁 contracts → core 消费接线 | QT-74 **零外部前置**（只在本仓落词典）；后两步等 QT-74 | H3 与 H2/H4/H5/H6 并行 | **前置被改判**（原写「直接迁 contracts」） |
| **H4** | `tellurion-data` | QT-40 收尾：物理 `SET SCHEMA` 搬迁（**⛔ 被 core 剩余写者挡住**）→ `visible_at` 生产回填（**未授权**） | ① 两条作业停写 **✅**（`ac6b7e0`）；② 写面装配 HTTP 一半 **✅**；**③「停第二写者」整体没做完**：core 仍直写 `market.index_daily`（`pkg/storage/index_daily.go:31`），且 ETL 用裸表名写 `public.*`（`bulk_insert.go`，仓内无 `search_path`）⇒ 物理搬迁会写空 core 剩余同步腿（§5 ⑰） | H4 与 H2/H5/H6 并行；**物理搬迁不排期，等 ⑰** | **部分过期**：本版原写「前置已满足」是错的 |
| **H5** | `tellurion-orchestration` | QT-44 收尾：`producer_pending` 提示位（本仓单可做）→ X0 生产者（**跨仓，⛔**） | X5c **✅**（worker 端点 8 条 + core `cmd/worker` 装配 `106b35c`）；**X0 生产者不是本仓单可施工**：orchestration 无 artifact 写入端点、core worker 不回传三 hash ⇒ §5 ⑱ | H5 与 H2/H3/H4/H6 并行（仅提示位那一半） | **本版原写「本仓单可施工」是错的** |
| **H6** | `tellurion-analytics` | QT-43 收尾：**无可独立开工代码单**（token 已落；`base_run_spec` 卡 R3） | R2 **✅**；**token 分发也已落**（`cmd/analyticsservice/main.go:46-94` + `analytics-deployment.yaml:62-69`，`ca95f1e` 在 `origin/main`）⇒ 剩下的是**运维签发**，不是本仓代码；`base_run_spec` 卡 §5 ⑪ | H6 与 H2~H5 并行（但本批无事可做） | **已过期**：本版原写「token 分发可立刻做」 |
| **H7** | `tellurion-ui` + `tellurion-docs` + `tellurion-broker` | 三条**互不相关**，各自独立 | 全部**外部/环境前置未解除** ⇒ **只出决策材料** | — | **仍成立** |
| **H8** | `tellurion-strategy` | promote 闸门审批留痕 → **实测降级为「只出决策材料」** | **⚠️ 裁决 ⑭ 的承载体不存在**：strategy 仓**零鉴权**（无 kit auth、12 条路由全裸）、core 也**无调用方**（`/promote` 0 命中）⇒ 先裁「审批人从哪来」 | H8 与 H2~H6 并行（**且只出文档**） | **新增**（12:30 版漏了它） |

---

## 第 2 节 · 分组说明

### H2 · core：`QT-51`（回测/实盘执行语义分离 BE-02）—— **本批唯一还没开工的 core 单**

**实测复核（2026-10-25 二次订正）**：main 上 `pkg/backtest` 仍 import `pkg/live` 的**是 6 个文件**、`ExecutionPort` **0 命中**——但**实现已在 worktree 分支上**：
- `tellurion-qt51` = `task/qt51-execution-port`（`7c89aae`），**不在 origin**，基线 `170f09a` 落后 main **6 个 commit**；
- 分支里 `pkg/backtest` 对 `pkg/live` import = **0**，`pkg/backtest/execution/port.go` 已落 `ExecutionPort`，带 `docs/design/qt-decoupling/qt51-execution-port.md` 与 `docs/test-cases/QT51_BACKTEST_EQUIVALENCE_REPORT.md`；
- 分支自述「已裁决并落地（2026-10-26）」——但 **main 的裁决记录里没有这条裁决** ⇒ 合入前要主任确认（§5 ①）。

**组内串行原因**：rebase → 干净 checkout 复取 → 等价性复跑 → 裁决 → 合入，后三步都建立在 rebase 后的树上。它与 H3 的 core 腿、H8 都碰 `pkg/backtest/engine.go` ⇒ core 仓内串行。
**可立刻做的**：提交 main 的 5 个脏文档、rebase 分支、复取门禁、复跑等价性摘要。
**在等外部产物的**：**合入**等 §5 ①。

### H3 · risk：`QT-74` → 迁 contracts → core 消费接线

**⚠️ 前置被改判（这是本批最重要的修正）**

12:30 版把 H3 的前置写成「core 不能 import risk 仓 ⇒ 先把 risk 快照类型迁 contracts」。**实测这个判断方向对，但漏了更靠前的一环**：

risk 仓在 2026-10-25 新立了 **QT-74 · 规则参数词典**（`tellurion-risk:docs/TASKS.md:441`），其立项原文直接点名 H3：「**触发点是 QT-42 未落项 3' 的 ①：源头不冻结词典，`bundle` → `ParseBundle` → `RiskPolicy` 这条链只能由消费方猜字段名和单位**——那是本项目算错过钱的缺陷类型」。

**实测的卡点**：`SnapshotRule.Params` 是 `json.RawMessage`，`canonicalParams`（`internal/rule/model.go:150`）**只校验「是非空 JSON 对象、≤64 KiB」，不看键名**；而 core 的限额面是 14 个标量（`tellurion:pkg/risk/manager.go:13-28`），今天的来源是 `config/analysis-service.yaml:155-173` 的 `risk_manager.*`（映射代码 `pkg/bootstrap/engine.go:38-51`）。
⇒ **风险量化**：risk 自证夹具是 `{"pct":2.5}` / `{"max_pct":10}`（`internal/rule/snapshot_test.go:143-144`）——按小数读是 **10 倍净值**，按百分数读是 10%，**差两个数量级**。这就是「源头不冻结 ⇒ 消费方猜」的真实代价。

**组内串行原因（三个单严格有序）**：
1. **QT-74**（本仓落词典，零外部前置）⇒
2. **迁 contracts**（QT-74 ④ 原文：「**不**迁 contracts（那是**词典落定后**、core 真要消费时的下一张单）」）⇒
3. **core 消费接线**（QT-74 ⑥ 原文：「`Snapshot`/`SnapshotRule`/`Bundle` 迁 contracts、core 消费」是下一张单）。
**QT-74 原文明确「顺序不可颠倒：先有词典才有值得迁的形状」** ⇒ 12:30 版的「先迁 contracts」是**跳步**，已订正。

**可立刻做的**：**QT-74 全部**——零外部前置，只在本仓落两份文档 + 写侧校验 + 夹具改键名。
**在等外部产物的**：迁 contracts 与 core 消费都在 QT-74 之后。

### H4 · data：`QT-40` 收尾（物理 `SET SCHEMA` + 生产回填）

**实测复核（2026-10-25 二次订正）**：12:30 版的两个待做项里，**只有「两条作业停写」和「HTTP 写面」落了**；**「停第二写者」整体没做完**：
- ✅ `capital_flow` / `top_list` 已改投 `POST /ingest/points`，core 生产代码 `NewETLPipeline` 引用归零；
- ✅ `POST /ingest/points` → `internal/assembly.Writer` → `store.Writer` 已装配；
- ⛔ **仍在写的**：`tellurion:pkg/storage/index_daily.go:31` 的 `INSERT INTO market.index_daily`（调用链 `pkg/data/tushare.go:929` → `cmd/data/handlers_sync.go:88` → `cmd/data/main.go:155`）；以及 core 的 ETL 用**裸表名**写 `public.*`（`pkg/storage/bulk_insert.go` 的 `TableFor` 返回 `ohlcv_daily_qfq` 等裸名，仓内无 `search_path` 设置）——这些表在 data 的 `TableNames()` 里已被登记为 `market.*` 所有；
- ⚠️ `ingest.raw` 的 core 写入是**设计内**的（证据归档），但它同样说明 core 仍在写 data 声明的 schema。

**组内串行原因**：物理搬迁必须在**所有**写点停掉之后；今天只停了 2 条。
**可立刻做的**：**没有**——先把 core 剩余写者处置掉（§5 ⑰）。
**在等外部产物的**：core 停写者（**新单，未立**）；`visible_at` 生产回填另需 ⑨ 授权。

### H5 · orchestration：`QT-44` 收尾（X0 生产者 + 鉴权 R5）

**实测复核（2026-10-25 18:45）**：12:30 版写「worker 端点 + 进程未落」——**已落**：
- **worker 端点 ✅**：`internal/httpapi/workers.go:66-73` 八条路由（`GET/POST /api/runs/workers`、heartbeat、claim、drain、lease、result、cancel-ack）；
- **core 腿 ✅**（`106b35c`）：新增 `cmd/worker`（可执行体）+ `internal/orchestrationworker`（HTTP 客户端 / run_spec 解析 / 执行器 / 循环），执行经 `contracts.EngineRunner`，成功结果由 `JobStoreSink` 写回 `backtest_jobs`；**两仓仍零 import**（worker 只按 orchestration 的 api-schema 走 HTTP）。
- **父汇总写回 ✅**（台账原文：「X5b 的父汇总写回 ✅」）。

**组内串行原因**：`producer_pending` 提示位是本仓单步；X0 生产者横跨两仓。
**可立刻做的**：**`producer_pending` 提示位**（`internal/httpapi/provenance.go` 响应加提示字段，**不新增第四态**）。
**在等外部产物的**：**X0 生产者不是本仓单可施工**——实测 orchestration **无 artifact 写入端点**，core worker **不回传三 hash**（`grep RunSpecHash|DatasetSnapshotHash|ArtifactHash` 在 `cmd/worker` + `internal/orchestrationworker` 零命中）⇒ 需要两仓同批（§5 ⑱）。鉴权 R5 仍按裁-15 缓做（触发条件未到）。

### H6 · analytics：`QT-43` 收尾（token 分发 + `base_run_spec` 完整装配）

**实测复核（2026-10-25 二次订正）**：R2 **✅**；**token 分发也已落**——`cmd/analyticsservice/main.go:46-94` 已接 `-core-token` / `-data-token`（`ca95f1e`，已在 `origin/main`），`deploy/k8s/analytics-deployment.yaml:62-69` 已有 `secretKeyRef`。
**组内串行原因**：无（本批无代码单）。
**可立刻做的**：**没有**——「有 token 就能用」本仓已保证；剩下的是**运维签发/注入**，不属本仓代码。
**在等外部产物的**：`base_run_spec` 完整装配卡 R3（§5 ⑪）；R1 两条偏差卡读面语义（§5 ⑲）。

### H8 · strategy：promote 闸门审批留痕 —— **12:30 版漏了的一组**

**实测复核（2026-10-25 18:45）**：strategy 全仓 `grep "approver|approved_by|promoted_by"` = **0 命中** ⇒ **未落**。这是裁决 ⑭ 的落地动作，也是 QT-32 ③-②「闸门记录进审计，审批人/时间/结论留痕」缺的「谁批的」那一环。

**⚠️ 但裁决 ⑭ 的落地前提被实测推翻（这一条改变了本组的可施工性）**

裁决 ⑭ 写「审批人取 JWT claims 的 `sub`」。**实测 strategy 仓今天零鉴权**：
- 全仓 `grep "JWT_SECRET|Bearer |Authorization|RequireRole|Roles\b"` = **0 命中**；
- **没有**任何 auth / role / guard / middleware 命名的 `.go` 文件；
- `go.mod` **只有 contracts `v0.5.0`，没有 `tellurion-kit`** ⇒ kit 的 `auth` 包**根本没进这个仓**；
- `internal/httpapi/api.go` 的 12 条路由**全部无保护**（`:42-56`：`POST /api/strategies`、`PUT .../source`、`POST .../build`、`.../validate`、**`.../promote`**、`.../deprecate`、`GET .../resolve`、`GET .../artifact`，以及 `/internal/*` 四条）；
- core 全仓 `grep "/promote"` = **0 命中** ⇒ **今天没有任何调用方调它**。

⇒ **「从 JWT claims 取审批人」在 strategy 仓今天没有承载体**。这不是「先核一下」的小事，而是**整条路径的起点缺失**：要让审批人可信，要么 strategy 引入 kit auth（= 给 strategy 引入第一套鉴权，是 QT-71 的域且会扩大本单范围），要么审批人从**别的面**进来（core 转发 / 独立管理面 / mTLS）。

**组内串行原因**：单步，但**第一步是选审批人来源**，不是写代码。
**可立刻做的**：**决策材料**（把「审批人从哪来」的三个可选面 + 各自代价列清）——**不是**施工。
**在等外部产物的**：审批人来源被裁 ⇒ 才谈得上落库留痕。

### H7 · ui / docs / broker —— **三条纯阻塞**

全部**外部/环境前置未解除** ⇒ 本批**只出决策材料**。

---

## 第 3 节 · 派发原文

### 派发块 H2（core）

> **派发原文**：在 `tellurion` 仓完成 `QT-51`（回测/实盘执行语义分离 BE-02）。
>
> 1. **第一步是出设计，不是改代码**（台账 ② 原文：「BE-02 一直只有『待切 port』的意向，**无设计**」）：
>    - 读 `docs/design/qt-decoupling/backtest-engine-review.md:33`（BE-02 行）与 `:145` 的**未勾 checkbox**：「BE-02：回测/实盘共用执行语义，是『同构桥』还是该拆？」（**这一条就是待主任勾的那条**）。
>    - 读 `docs/design/qt-decoupling/backtest-extraction.md:46`（「**必须切断** `→ pkg/live`」）与 `:92`（「BE-02 依赖反转：把 LiveBridge 从引擎移出，改为**实盘驱动持有引擎**」）。
>    - 产出：`ExecutionPort` 的**方法集**（下单 / 撮合 / 成交回报）、**两个实现各住哪**、**回测侧适配层放哪**、以及 ③-③ 那条「若保留『同构桥』为有意设计，必须写明并在测试里锁住『改 live 不改回测结果』」的取舍。
>    - **设计里必须逐条点名实测到的耦合面**（本单的真实工作量，不是泛泛的「解耦」）：
>      | 符号 | 命中数 | 出处 |
>      |---|---|---|
>      | `live.OrderResult` | 13 | `pkg/backtest/engine.go` / `options.go` / `execution/live_bridge.go` + 测试面 |
>      | `live.LiveTrader` | 12 | 同上 |
>      | `live.EmergencyFlattenResult` | 4 | `execution/live_bridge.go` |
>      | `live.ReconcileSignals` | 3 | 同上 |
>      | `live.PositionInfo` / `live.AccountInfo` | 各 2 | `pkg/backtest/engine.go` |
>      | `live.NewPaperSession` / `live.PaperSessionConfig` | 各 2 | `pkg/backtest/engine.go` |
>      | `live.NewMockTrader` / `live.MockTraderConfig` | 各 2 | 测试面 |
>      | `live.SignalDiscrepancy` | 1 | `pkg/backtest/execution/live_bridge.go` |
>      | `live.PaperSession` | 1 | 同上 |
>
>      ⚠️ **命中面是 6 个文件**（3 生产 + 3 测试：`fakes_test.go` / `signal_reconcile_e2e_test.go` / `execution/live_bridge_test.go`）——台账 ② 写的「三处」只是**生产面**；**测试面不改，护栏腿会红**。
>      ⚠️ `live.Sh` 那 5 处命中是 `strings` 包的匹配（实测 `LIVE.SH` 大小写不敏感），**不是 `pkg/live` 的符号** ⇒ 真实耦合面比 grep 命中数少。
> 2. **按设计落代码**：只改 `pkg/backtest/{engine.go, options.go, execution/live_bridge.go}` 及其测试；**不改** `pkg/live` 的实现语义（那是 QT-45 的通道网关域）。
> 3. **护栏**：`internal/repoguard/` 加一条「`pkg/backtest` 不得 import `pkg/live`」的 AST 腿（若设计选择保留同构桥，则该腿改为「只允许经适配层」并**点名允许的符号集**）。
> 4. **验收**：`pkg/backtest` 对 `pkg/live` 的 import 为 **0**（或仅剩接口适配层且有护栏）；**同一策略回测结果在改动 live 前后逐位一致**（要有归档的比对报告，不是「跑绿了就算」）。
> 5. **破坏验证**：把适配层改成直接调 `live` 实现 ⇒ 护栏腿必须红。
>
> **验收（组级）**：`gofmt -l .` 空；`go build ./...` / `go vet ./...` / `go test ./... -count=1` 全绿；`go test ./pkg/backtest/... -count=1` 绿。
>
> **禁区**：不动 Order/Trade 值对象；不改 T+1 簿记；不引入 MQ；**不许在 `pkg/live` 里改实现来迁就回测**；**不许把 `paper_session.go` 的 `generateSignals` 平行实现顺手改掉**（w1 所有权表 §5 已登记：paper 侧单独立单，本单同理）。
>
> **跨仓腿**：无（纯 core 域）。

---

### 派发块 H3（risk → contracts → core，三步严格有序）

> **派发原文**：在 `tellurion-risk` 仓完成 `QT-74`（规则参数词典）。**本单只在本仓落词典**；迁 contracts 与 core 消费是它之后的单。
>
> 1. **落词典（两份文档，两处同名同序，词典是唯一真相源）**：
>    - `docs/db-schema.md` 的 `rule_snapshots.rules[].params` 节 + `docs/api-schema.md` 的规则 create/update 输入口径。
>    - 字段清单按 core 的 **14 个标量 1:1 提案**（键名与 `config/analysis-service.yaml:155-173` 的 `risk_manager.*` 同名，**改名叫 core 猜**）。台账 QT-74 ② 已把 14 项列成表（`position_sizing` 7 项 / `take_profit` 1 项 / `regime` 3 项 / `stop_loss` + 四个 regime 倍数 4 项…），**按那张表抄，不要另起名**。
>    - **每字段四列齐**：名称 / 类型 / 单位·口径 / 取值域 + 必填性，**无「待定」**。
>    - **单位是唯一会让人算错钱的列**：权重一律**小数**，**禁止 `pct` / 百分数形态**。
> 2. **写侧严格（按 `type` 校验键集合与取值）**：`Create` / `Update` 对 `params` 按 `type` 校验——未知键、缺必填、类型不符、`NaN`/`Inf`、`fast_ma_period >= slow_ma_period` 一律 `ErrInvalidParams` → 400 `invalid_params`。
> 3. **读侧不重校键集合**（**关键不变量**）：`ParseBundle` / `SnapshotHash` **不**因词典新增键而拒绝历史快照——否则给词典加一个键会让全部历史 `risk_rules_hash` 集体不可读（与本仓「钉住的历史 hash 永不失效」不变量 8 冲突）。
> 4. **夹具改键名**：`internal/rule/snapshot_test.go:143-144` 的 `{"pct":2.5}` / `{"max_pct":10}` → 按新词典改成小数形态（如 `{"max_position_weight":0.10}`）。**这是本单最容易被跳过的一步**——不改正样就等于把「10 倍净值 vs 10%」的歧义留在夹具里。
> 5. **scope 消费口径显式写进 `docs/dataflow.md`**：一期 core 只消费 `scope = global`；`account` / `strategy` / `symbol` 三档在 bundle 里**可见但本 run 不参与裁决**。**不许静默忽略**（回测没有账户身份是事实，不是缺陷；但「下发了却没用」必须看得见）。
> 6. **`risk.limits` 的 `limit_key`**：本单**要么**冻结它并给词典的引用，**要么**在 `docs/db-schema.md` §1 明确它的处置（**不许留空**）。
>
> **验收（组级）**：`gofmt -l .` 空；`go build / go vet / go test ./... -count=1` 全绿；词典在两份文档里逐字段可查（**四列齐、无「待定」**）；写侧校验用例（未知键 / 缺必填 / 类型不符 / `NaN` / `Inf` / `fast >= slow`）逐条红绿成立；**历史快照仍可读**（拿一条钉住的历史 `risk_rules_hash` 读回成功 ⇒ 不变量 8 未破）。
> **破坏验证**：给词典加一个新键 ⇒ 旧夹具仍绿（证明读侧没被牵连）；把写侧校验短路 ⇒ 未知键用例红。
>
> **禁区**：**不迁 contracts**（那是下一张单）；**不改 `SnapshotHash` / canonical 编码**；**不许在读侧加键集合校验**（会破不变量 8）；不许改 `RuleType` 的四个冻结值；不许把「scope 三档不参与裁决」写成静默行为。
>
> **跨仓腿**：无（只在本仓落词典）。**迁 contracts 与 core 消费在本单之后**，顺序不可颠倒。

---

### 派发块 H4（data）

> **派发原文**：在 `tellurion-data` 仓完成 `QT-40` 收尾。
>
> 1. **物理 `SET SCHEMA` 搬迁**（把 `market` / `ingest` 的表物理迁到目标 schema）：
>    - **前置**：写面装配已完成（实测 HTTP 一半 ✅：`POST /ingest/points` → `internal/assembly.Writer` → `store.Writer`，`cmd/dataservice` 在有 `DATABASE_URL` 时真的装配）；**core 侧其余写点确认已停**——实测 core 带 schema 前缀写 `market.` / `ingest.` 的生产点**只有两处**：`pkg/storage/index_daily.go:88`（`market.index_daily`）与 `pkg/storage/ingest_raw.go`（`ingest.raw`，**这是设计内的证据表归档，不是第二写者**）。
>    - **约束**：**禁 `SET visible_at = trade_date`**（已有测试钉住）；**禁在 `Statements()` 里静默 `ALTER TABLE … SET SCHEMA`**（`internal/schema/schema.go:12` 明写这条禁令）。
>    - **验收**：迁移前后 `TableNames()` 可对账；A/B/C 三集合分类不变。
> 2. **`visible_at` 生产回填** —— **⚠️ 本轮不授权执行**（裁决 ⑨：暂不授权；回填是**写生产库 12 张 A 集表**的操作，且回填后 PIT 门立即生效、历史上 `visible_at` 为 NULL 的行会整批拒收）。
>    - **本单只做**：确认回填开关与护栏就绪（`internal/schema/backfill_pg_test.go` 用 `-tags backfill`，只认 `DATA_TEST_DSN` 且库名须 `_test`，生产 DSN 与生产库名自动 skip），把「运维执行清单」写进部署文档。
>    - **禁止**：不许任何 agent 写生产库。
>
> **验收（组级）**：`go build / go vet / go test ./... -count=1` 全绿；`DATA_TEST_DSN='postgres://postgres:postgres@127.0.0.1:5432/tellurion_data_test?sslmode=disable' go test -tags backfill ./internal/schema -count=1` 绿（**生产 DSN 与非 `_test` 库名自动 skip**）。
>
> **禁区**：**不许重新引入 `/factors/{name}@{hash}` 路由**（裁-14 已撤，`TestFactorRouteIsRemoved` 钉住）；不许 `SET visible_at = trade_date`；不许在本仓实现传输（仍在 core，按 `Transports` 注入）；**不许执行生产库回填**；不许在本仓实现第二套 PIT 语义。
>
> **跨仓腿**：core 的 `market.index_daily` 写点确认已停（**core 仓，跨仓**）。

---

### 派发块 H5（orchestration）

> **派发原文**：在 `tellurion-orchestration` 仓完成 `QT-44` 收尾。
>
> 1. **X0 生产者 + `producer_pending` 提示位**：
>    - 按裁决 ⑬：`producer_pending` 是**本仓端点自己的待实现提示位**（表达「这个端点的生产者还没接线，所以查不到 ≠ run 不存在」），**不是第四个状态码**（三码锚点仍是 `provenance_not_ready` / `provenance_not_found` / `provenance_missing`）。
>    - 落点：`internal/httpapi/provenance.go` 的响应里让运维看得出「生产者未接线」。
>    - **约束**：**不引入第四个 wire 码**；三码与 core 侧 `cmd/analysis/handlers_backtest_provenance.go` 逐字对齐（实测 core 侧已明确「本端点**不引入** `producer_pending`」）。
> 2. **鉴权 R5** —— 台账 QT-44 ④ 原文：「裁-15 **缓做**（不新增 JWT 面）」⇒ **本轮只在触发条件满足时做**；未满足则**如实登记「条件未到」**，不假装做完。
>    - **触发条件**：台账 X0 行的「X0 生产者」落定 + 编排面开始承接真实流量。
>
> **验收（组级）**：`gofmt -l .` 空；`go build / go vet / go test ./... -count=1` 全绿；`ORCHESTRATION_TEST_DSN='postgres://postgres:postgres@127.0.0.1:5432/tellurion_orchestration_test?sslmode=disable' go test ./internal/pgstore -count=1` 绿（无 DSN 时 PG 腿 skip，**skip 不算通过**）。
>
> **禁区**：**本仓零 import core**（实测 worker 腿也是走 HTTP，`cmd/worker` 在 core 仓、只按本仓 api-schema 走）；不改 `EngineRunner` 契约；不做 K8s；不搬 `cmd/dualrun`；**不许改 `plan_canonical` / `plan_hash` 身份口径**；**不许引入 `producer_pending` 之外的第四态**；鉴权不新增 JWT 面。
>
> **跨仓腿**：core 侧的 `cmd/worker` **已落**（`106b35c`）⇒ **本组无未解决的跨仓腿**。

---

### 派发块 H6（analytics）

> **派发原文**：在 `tellurion-analytics` 仓完成 `QT-43` 收尾。
>
> 1. **token 分发**（**前置已解除**：core 的部署清单已随 QT-71 落地，实测 `deploy/k8s/` 现在有 **10 份清单**，含新增的 `risk-deployment.yaml` / `analytics-deployment.yaml`）：
>    - 装配 `-core-token` / `-data-token`；台账未落项原文：「鉴权开启后 core / UI 调用本服务必须持 token（POST trader / GET viewer）；**分发不在本仓**」⇒ **本仓只保证「有 token 就能用」，不负责分发**。
>    - **验收**：无 token 401 / 越权 403（`TestAuthEnforcedCurrentState` 已是**可执行锚点**、改写而非删除）；带 service token 能调本服务。
> 2. **`base_run_spec` 完整装配** —— 台账未落项 ② 原文：「完整 RunSpec 的装配属执行侧（**R3 未裁**），当前锚点只保证计划身份可算、网格可展开」⇒ **R3 未裁前不许猜 RunSpec 的形状**；本单只把「缺什么」成文（`base_run_spec` 只有 `run_id`）。
>
> **验收（组级）**：`gofmt -l .` 空；`go build / go vet / go test ./... -count=1` 全绿；`ANALYTICS_TEST_DSN='postgres://postgres:postgres@127.0.0.1:5432/tellurion_analytics_test?sslmode=disable' go test ./... -count=1` 绿（无 DSN 时 PG 腿 skip，**skip 不算通过**）。
>
> **禁区**：**不做 PDF 导出**（R5 已裁为非目标）；不许把 `AuthStatus` 改回 `"unwired"`；不许动 `internal/analysis/sensitivity.go` 的 fail-closed 语义；**不许新建 contracts 的 `analysis` 包**（裁决 ③）；不许在本仓造 token 分发机制；**不许在 R3 未裁前自造 RunSpec 形状**。
>
> **跨仓腿**：token 分发在 core / 运维侧（**不在本仓**）。

---

### 派发块 H8（strategy）—— **本组只出决策材料，不出施工步骤**

> **派发原文**：在 `tellurion-strategy` 仓**只做决策材料**，**不要写代码**。
>
> 1. **实测事实（这是本组的全部依据，不再核一遍）**：本仓**零鉴权**——全仓 `grep "JWT_SECRET|Bearer |Authorization|RequireRole|Roles\b"` = 0 命中；无任何 auth/role/guard/middleware 命名的 `.go` 文件；`go.mod` 只有 contracts `v0.5.0`、**没有 `tellurion-kit`** ⇒ kit 的 auth 包没进这个仓；`internal/httpapi/api.go:42-56` 的 12 条路由（含 `POST /api/strategies/{id}/versions/{hash}/promote` 与 `/internal/*` 四条）**全部无保护**；core 全仓 `grep "/promote"` = 0 命中 ⇒ **今天没有任何调用方调它**。
> 2. **产出：三个可选面 + 各自代价**（供协调者裁）：
>    - **面 A · 给 strategy 引入 kit auth**：审批人从 JWT claims `sub` 取（= 裁决 ⑭ 原意）。**代价**：这是**给 strategy 引入第一套鉴权**（12 条路由全裸的问题会一次性被打开，超出「只补 promote 留痕」的范围）；且 strategy 要新 pin kit（今天没有）⇒ contracts 版本矩阵也要跟着核。
>    - **面 B · 审批人由 core 转发**：core 侧解析 JWT 后把审批人身份放进请求头/体。**代价**：仍是「caller 自报」形态，只是把信任边界挪到 core；core 今天也**没有调 promote 的代码**（0 命中）⇒ 要先造调用方。
>    - **面 C · 独立管理面**：strategy 起一个带鉴权的 admin 面专管 promote。**代价**：**与「不造第二套鉴权」冲突**（除非它复用面 A 的 kit auth）。
>    - **每个面都要答**：「审批人身份不可由 caller 自由填写」这条怎么保证；`compile_log`（追加式，`internal/asset/pgstore.go` 的 `Promote(...)` 的 `log` 参数**今天硬编码 `"promoted"`**）够不够，还是要新开 `strategy.strategy_promotions` 审计表；**时间与结论**的落点。
> 3. **顺带核一件不在本单范围、但会被它撞到的事**：core 的 `QT-66` / `QT-32` 都在等「AI 产物过闸门才能回测」这条闭环，而**今天 promote 端点既无鉴权也无调用方** ⇒ 闭环的最后一环缺的不是「审批留痕」，是「谁来调它」。
>
> **验收（组级）**：决策材料成文（落 `tellurion-strategy:docs/TASKS.md` 的 QT-32 闸门腿下，**只放结论与三个面，不放施工步骤**）；`go build / go vet / go test ./... -count=1` 仍全绿（**本组零代码改动**，跑一遍确认没被别处改坏）。
>
> **禁区**：**不许在本单引入 kit auth 或任何鉴权栈**（那是 QT-71 的域，且会让本单范围失控）；**不许把审批人做成可由 caller 自由填写的字符串**；不许改 promote 的状态机（`StatusValidated → StatusPromoted`）与 `cache.InvalidateLatest(id)` 同刻失效；不许在本仓实现 AI 生成逻辑（ADR-023）；**不许把「面 A/B/C 选一个」写成「已选」**——那是协调者的裁决。
>
> **跨仓腿**：core 的「谁来调 promote」那条腿属 core（**本仓不造调用方**）。

---

## 第 4 节 · 阶段划分（2026-10-25 18:45 订正）

> **「组间并行」指的是「开工时刻可并行」，不是「组内每一步都不用等别人」。**

### 阶段 A —— 现在就能全部并行开工，组间零等待

| 组 | 开工第一步 | 为什么现在就能开 |
|---|---|---|
| **H2** | 提交脏文档 → rebase `task/qt51-execution-port` → 干净 checkout 复取门禁 → 等价性复跑 | **实现已在分支**（`7c89aae`），复取零外部前置；**合入**等 §5 ① |
| **H3** | **QT-74 全部** | 只在本仓落词典，**零外部前置**；QT-74 ④/⑥ 明确「迁 contracts 与 core 消费在**本单之后**」 |
| **H4** | **不排期**（只出迁移脚本草案，不执行） | **物理搬迁前置不成立**：core 仍写 `market.index_daily` 与 `public.*`（§5 ⑰） |
| **H5** | `producer_pending` 提示位（**只这一半**） | worker 端点 ✅、core `cmd/worker` ✅；X0 生产者缺写入端点 + core 回传，属跨仓（§5 ⑱） |
| **H6** | **无可开工代码单** | token 已落（`ca95f1e`），部署清单已有 secretRef；剩运维签发 |
| **H8** | **决策材料**（三个可选面 + 代价） | 本仓零鉴权 ⇒ 裁决 ⑭ 无承载体 ⇒ **只能出材料，不能出施工步骤**（见 §5 ⑫） |

**阶段 A 的并行硬约束**：
- **H2 与 H3 的 core 腿同属 core 仓** ⇒ 必须各自 `git worktree`（文件域零重叠：`pkg/backtest/*` vs `cmd/analysis/wiring/*`）。
- **跨仓零重叠**：H3 在 risk、H4 在 data、H5 在 orchestration、H6 在 analytics、H8 在 strategy（**且只出文档**）。

### 阶段 B —— 等某个组的某个具体产物之后才解锁

| 被解锁的步骤 | 等谁 | 等的具体产物（可机械核对） |
|---|---|---|
| **H3 · 迁 contracts** | **H3 · QT-74** | risk 的 `docs/db-schema.md` + `docs/api-schema.md` 里 14 项词典逐字段可查（**四列齐、无「待定」**）+ 写侧校验用例绿 + 夹具已改小数键名 |
| **H3 · core 消费接线** | **H3 · 迁 contracts** | contracts 里出现 `Snapshot` / `SnapshotRule` / `Bundle`（core **不能 import risk 仓**，服务仓只经 contracts 交互） |
| **H6 · `base_run_spec` 完整装配** | **R3 裁决**（未裁） | RunSpec 的形状成文（当前只有 `run_id`） |
| **H5 · 鉴权 R5** | **X0 生产者落定** | `run_artifacts` 的「谁写」定下来 + 编排面开始承接真实流量 |
| **H4 · 生产回填** | **运维授权**（裁决 ⑨ 暂不授权） | 授权 + core 停第二写者确认 + 回填开关就绪 |
| **H8 · 审批人取 JWT claims** | **strategy 仓接 JWT 中间件** | strategy 仓有可用的 JWT 解析面；**接不上就换方案（报协调者）** |

---

## 第 5 节 · 待裁决清单（2026-10-25 二次订正：③④ 改判，新增 ⑰⑱⑲⑳）

> **编号说明**：①~⑬ 是本表原编号；⑭ / ⑮ / ⑯ 在 [裁决记录](cross-repo-decision-record-2026-10-25.md)（**⑯ = service token**，即本表 ⑬ 那条的裁决记录编号）。新增项从 **⑰** 起，避免与裁决记录撞号。

| 事项 | 卡住哪个单 | 状态 / 需要你给什么 |
|---|---|---|
| **① QT-51 的设计取舍** | **H2** | **仍待裁**。台账 ③-③：「若保留『同构桥』为有意设计，必须写明并在测试里锁住『改 live 不改回测结果』」；设计文档 `backtest-engine-review.md:145` 有一条**未勾 checkbox** 就是这条。**需你勾**：拆 port，还是保留同构桥并锁死行为 |
| **② risk 快照类型迁 contracts 的落点** | **H3** | **已被 QT-74 改判但仍待裁**：QT-74 ④ 原文「**不**迁 contracts（那是词典落定后、core 真要消费时的下一张单）」⇒ **仍需你拍**：① 迁到 contracts 的哪个包（新建 `risk` 包？还是并入 `engine`）？② **`ParseBundle` 要不要一起迁**（它是 risk 的实现细节，迁进 contracts 就成了「contracts 有实现」，与 ADR-0001 ② 冲突） |
| **③ worker 进程归谁** | ~~H5~~ | **✅ 已消解**：core 腿 `cmd/worker` 已落（`106b35c`），**两仓仍零 import**（worker 只按 orchestration 的 api-schema 走 HTTP） |
| **④ 第二写者的停法** | **H4** | **⚠️ 只消解了一半**：`ac6b7e0` 停的是 `capital_flow` / `top_list` 两条；core 仍直写 `market.index_daily`（`pkg/storage/index_daily.go:31`）且 ETL 裸表名写 `public.*`。**新登记为 ⑰** |
| **⑤ 生产库回填的执行授权** | **H4** | **仍待裁**。裁决 ⑨ 已定「暂不授权」⇒ **需你给**：core 停第二写者已确认后，是否授权 + **由谁执行**（本方案**不安排**任何 agent 去写生产库） |
| **⑥ UI 三服务栈的起法** | **H7**（ui） | **仍待裁**。AUD-61 ① 批 16 条结构漂移，**前置已解除**（重构分支已并入），**今天的障碍是跑不起栈**。**需你给**：栈由谁起（本机 Docker 不可用：实测 `docker info` 报 `//./pipe/dockerDesktopLinuxEngine` 不存在）——还是先只跑「不需要后端」的那部分？ |
| **⑦ XTP 柜台环境** | **H7**（broker） | **仍待裁**。仍缺 P1~P5 五项外部条件（SDK / 测试账号 / 出网 / C 工具链 / 盯盘时间窗），**本仓与 core 与本机与 CI 四处都没有 SDK**。**需你给**：这五项有没有一项能提供；没有 ⇒ QT-45 保持 ⛔ 不排期 |
| **⑧ docs DOC-10 的范围** | **H7**（docs） | **仍待裁**。立项依据：§7 的「已知过渡态」写「六个服务仓只有占位 `doc.go`」、阶梯表写「X1~X6 未触发」——**今天已不成立**（六仓都实装了）。**需你拍**：只改 §7 的事实描述，还是连阶梯表一起重写 |
| **⑨ `broker.order_acks` 保留期** | **H7**（broker） | **仍待裁**。台账未落项：「`broker.order_acks` 的**保留期未定**（当前不自动删）」。**需你给**：保留多久 / 谁负责清理 |
| **⑩ AI 生成物载体形态**（12:30 版漏列） | **QT-32** 后续 | agent QT-32-A ⑦①：三选一**无默认值** —— `carrier=wasm` + 12 个 host 函数 ABI（对齐 QT-26/30/31 的 `wasmSignalProvider`）/ `carrier=so`（Windows 不支持）/ 维持 ADR-024 的「只审阅不执行」。**需你拍** |
| **⑪ R3 的 RunSpec 形状** | **H6**（analytics） | 台账未落项 ②：「完整 RunSpec 的装配属执行侧（**R3 未裁**）」⇒ **需你裁 R3**，否则 `base_run_spec` 只有 `run_id` 这半永远填不上 |
| **⑫ promote 审批人从哪来**（**原「取 JWT claims 的可行性」已订正**） | **H8**（strategy）+ **core 的「谁来调 promote」** | **裁决 ⑭ 的落地前提被实测推翻**：strategy 仓**零鉴权**（`JWT_SECRET\|Bearer \|Authorization\|RequireRole\|Roles\b` 全仓 0 命中；无 auth/role/guard/middleware 文件；`go.mod` **无 kit** ⇒ kit auth 没进这个仓；12 条路由全裸），且 core 全仓 `grep "/promote"` = **0 命中** ⇒ **今天既没有鉴权、也没有调用方**。**需你裁三个面之一**：**面 A** 给 strategy 引入 kit auth（= 给它第一套鉴权，会把 12 条路由全裸的问题一次打开，且要新 pin kit）；**面 B** core 转发审批人（仍是 caller 自报形态，且 core 今天没有调用方）；**面 C** 独立管理面（**与「不造第二套鉴权」冲突**，除非复用面 A）。**另需你答**：promote 端点该**谁来调**（core 的 QT-32/QT-66 闭环在等它） |
| **⑬ ⛔ service token 在 httpadapter 上必然 401**（**18:45 实测新发现的真缺陷**） | **H3**（core 调 risk）+ 裁-17 整条服务间链路 | **需你裁三个方向之一**：**方向 1** 放宽 `httpadapter` 的 subject 校验（= 发 kit `v0.1.4` + risk/data/analytics **三仓同步升 pin**，今天都钉 `v0.1.1`）；**方向 2** 让 `IssueServiceToken` 强制数字 subject（= 与「service 主体要可读」冲突，且与 core `pkg/auth` 的 `RoleService` 主体口径分叉）；**方向 3** 在 `httpadapter` 里按 `claims.Role` 分流（= 与方向 1 同样要发版 + 三仓升 pin）。**无论哪个方向，kit 的 `service_token_test.go` 必须补一条「签出来的 token 能被 `httpadapter.Authenticate` 接受」的用例。** 详见 [裁决记录 ⑯](cross-repo-decision-record-2026-10-25.md) |
| **⑰ ⛔ H4 物理搬迁被 core 剩余写者挡住**（**二次订正新发现**） | **H4** | 需你定：core 的 `market.index_daily` 直写（`pkg/storage/index_daily.go:31`）与 11 条同步腿的 `public.*` 写，是**迁到 data** 还是**明确保留 core 写**；要不要立项。本方案**不排期**物理搬迁 |
| **⑱ X0 生产者的契约**（**二次订正新发现**） | **H5** / 后续 X0 单 | orchestration 无 artifact 写入端点、core worker 不回传三 hash。需你定：写入端点归 orchestration（谁调 / 写者身份 / 幂等），还是 worker 直写库（与「零 import core」「不双写」冲突） |
| **⑲ analytics R1 读面语义**（台账在册，未进待裁表） | **H6** | 读面按队列态放行还是按结论态放行（直接影响 pending/running 与 K9 `failed` 的 404/409）；改在 core |
| **⑳ AUD-61 ① 的 testid 范围**（台账在册，未进待裁表） | **H7**（ui） | 工单 ④ 禁止动 `web/src`，但按「优先 data-testid」需要给 `NavTiles.vue` / `MarketMetrics.vue` / `AppSidebar.vue` 补 testid。允许放宽 ④，还是只用现有类锚 |

---

## 第 6 节 · 本批的账实不符（相对台账原文的实测订正，2026-10-25 18:45 复核）

| # | 台账原文 | 实测 | 处置 |
|---|---|---|---|
| 1 | data QT-40：「**core 侧同步腿仍在写 `market` / `ingest`**」 | **台账对，本版 18:45 的判断错**：两条（`capital_flow` / `top_list`）已停，但 core **仍直写 `market.index_daily`**（`pkg/storage/index_daily.go:31`）且 ETL 写 `public.*`（`bulk_insert.go` 无 `search_path`） | ⇒ **H4 物理搬迁不排期**；新增待裁 ⑰ |
| 2 | core QT-51 ②：「`engine.go`、`execution/live_bridge.go`、`options.go` 三处 import `pkg/live`」 | 实测 **6 个文件**（3 生产 + 3 测试：`fakes_test.go` / `signal_reconcile_e2e_test.go` / `execution/live_bridge_test.go`） | ⇒ 「三处」是**生产面**；测试面不改护栏会红。已在派发块点名 |
| 3 | core QT-51 ②：「BE-02 一直只有『待切 port』的意向，**无设计**」 | 实测 `backtest-engine-review.md:145` 有一条**未勾 checkbox**：「BE-02：回测/实盘共用执行语义，是『同构桥』还是该拆？」（影响回测口径稳定性）；`backtest-extraction.md:46` 已把「**必须切断** `→ pkg/live`」列为必须项，`:92` 给了方向 | ⇒ **有方向没决策**：`:145` 那条就是待勾的那一条 |
| 4 | risk QT-42 未落项 3' ①：core 侧消费接线待接 | **已被 QT-74 改判**：真实卡点是**参数词典不存在**（`SnapshotRule.Params` 是 `json.RawMessage`，`canonicalParams` 只校验「是非空 JSON 对象、≤64 KiB」，**不看键名**），不是「core 不能 import risk 仓」 | ⇒ **H3 的第一步是 QT-74（落词典）**，迁 contracts 是**之后**的单。12:30 版写「先迁 contracts」是**跳步**，已订正 |
| 5 | risk QT-42 未落项 3' ① + 自证夹具 `{"pct":2.5}` / `{"max_pct":10}` | 按小数读是 **10 倍净值**、按百分数读是 10%，**差两个数量级** | ⇒ QT-74 ③ 要求夹具改小数键名；**不改就等于把歧义留在夹具里** |
| 6 | analytics 未落项 ④：「R2 的 owner 与最终形状：owner 未定」 | **已过期**：owner 已裁（本仓，裁决 ③）且**已落**（台账 frontmatter `verified-by: H6（R2 owner 落本仓 + 触发条件成文于 api-schema §6.4…）`） | ⇒ 台账已对 |
| 7 | orchestration 台账「X5c ✅；父汇总写回 ✅；QT-44 整体仍 🔶（X0 生产者 / 鉴权 R5）」 | 实测确认：worker 端点 8 条路由（`internal/httpapi/workers.go:66-73`）+ core `cmd/worker`（`106b35c`）都在 | ⇒ 台账已对；**12:30 版的 H5 第一腿作废** |
| 8 | broker QT-45 施工 ⑤：「X6c 判 ⛔ 阻塞，不是 🔶 待做」 | 实测确认：broker / core / 本机 / CI **四处零 SDK** | ⇒ 台账正确，**保持 ⛔ 不排期** |
| 9 | ui AUD-61：「① 批 ⬜ 待做，等 `feat/design-tokens-v2-palette-sweep` 合并」 | 台账已自行订正（分支已并入旧仓 main）⇒ **今天的障碍是跑不起栈** | ⇒ 台账已对，**本批只需解决「栈由谁起」**（§5 ⑥） |
| 10 | strategy QT-32 闸门腿（12:30 版写「审批人取 claims」） | 实测 `approver|approved_by|promoted_by` **零命中** ⇒ 未落；**且 strategy 仓零鉴权**（`JWT_SECRET\|Bearer \|Authorization\|RequireRole\|Roles\b` 全仓 0 命中、无 auth/role/guard/middleware 文件、`go.mod` **无 kit**、12 条路由全裸）、core 全仓 `/promote` **0 命中**（**无调用方**） | ⇒ **H8 独立成组并降级为「只出决策材料」**（12:30 版漏列）；**裁决 ⑭ 的落地前提被实测推翻** ⇒ 先裁「审批人从哪来」（§5 ⑫） |
| 11 | core QT-66 ⑦-4：「生产 `service` token 到不了角色判定」 | **复核成立且仍是活的**：kit `v0.1.1` 的 `IssueServiceToken` 允许非数字 subject（`auth/service_token.go:30/42/46`），而 `auth/httpadapter/httpadapter.go:139` 用 `claims.UserIDInt64()`（`auth/claims.go:29` = `ParseInt`）⇒ `service:risk` 这类 subject **必然 401**。risk / data / analytics 三仓**都钉在 kit `v0.1.1`** ⇒ **裁-17 的服务间链路今天全线走不通** | ⇒ **升级为待裁决第 ⑬ 条**（三个方向 + 「必须补一条端到端用例」的硬要求），已落 [裁决记录 ⑯](cross-repo-decision-record-2026-10-25.md) |
| 12 | core QT-66 ⑦-3：「QT-71 本仓 ①~③ **未开工**……本文件 QT-71 状态仍 ⬜」 | **已过期**：QT-71 **已落**（`aaf37bc` / `134b283`）——`pkg/auth/auth.go:40` `RoleService` 已在、`:48` `IsValid()` 已含它、`cmd/analysis/route_policy.go` 已有 Service 档位、部署清单 10 份 | ⇒ **已加订正块**；H3 的真实阻塞只剩 ⑦-1（参数词典 → QT-74） |
| 13 | 本版 H2：「QT-51 未开工 / 无设计 / 第一步出设计」 | **已证伪**：`task/qt51-execution-port`（`7c89aae`）已有 `ExecutionPort` 实现、设计文档、等价性报告；`pkg/backtest` 对 `pkg/live` import = 0；分支不在 origin，落后 main 6 笔；分支自述「已裁决」但 main 无裁决记录 | ⇒ H2 改为「收口」；合入等 §5 ① |
| 14 | 本版 H6：「token 分发可立刻做」 | **已过期**：`ca95f1e` 已在 `origin/main`，`cmd/analyticsservice/main.go:46-94` 已接 token，`deploy/k8s/analytics-deployment.yaml:62-69` 已有 secretRef | ⇒ H6 无可开工代码单 |
| 15 | 本版 H5：「X0 生产者是本仓单可施工」 | **已证伪**：orchestration 无 artifact 写入端点；core `cmd/worker` / `internal/orchestrationworker` 对三 hash 零命中 | ⇒ 归 §5 ⑱，跨仓 |
| 16 | data QT-40 X1d：「建表（19 张）」 | **实际 23 张**：`market` 19 + `ingest` 4（`TableNames()` 23 条） | ⇒ 改 data 台账表数 |
| 17 | 本版 §5 ④：「第二写者已消解」 | **不成立**：只停了 2 条；core 仍写 `market.index_daily` 与 `public.*` | ⇒ 新增 ⑰ |

---

## 附：本批与上一批的衔接（18:45 更新）

| 上一批的组 | 18:45 的状态 | 本批的去向 |
|---|---|---|
| G1 core（QT-73 → QT-71 → QT-72） | **三单全 ✅**（`170f09a` / `aaf37bc` / `4b3410a`） | → 本批无 |
| G2 kit（KT-09 → KT-10） | ✅ 两单已发版（`v0.1.2` / `v0.1.3`） | → 本批无 |
| G3 orchestration（子 run 入队 → worker 执行） | **①② 全 ✅**（worker 端点 + core `cmd/worker`） | → **H5**（只剩 X0 生产者 + 鉴权 R5） |
| G4 risk（ParseBundle 消费） | ⛔ 被 **QT-74** 挡住 | → **H3**（第一步 = QT-74） |
| G5 analytics（R1 → R2） | **两单全 ✅** | → **H6**（只剩 token 分发 + R3） |
| G6 data（写面装配 → SET SCHEMA） | **写面 HTTP 一半 ✅**、停第二写者 ✅ | → **H4**（只剩物理搬迁 + 生产回填） |
| G7 broker | ⛔ 决策材料已落 | → **H7**（仍阻塞） |
| G8 agent（QT-32-A） | ✅ 完成（⑦ 是后续单） | → 待裁 ⑩ |
| G9 ui | 前置已解除，① 批待做 | → **H7**（待决「栈由谁起」） |
| G10 docs | DOC-06 ✅ + DOC-10 新立 | → **H7**（DOC-10） |

**本批新增的一组**：**H8 strategy**（12:30 版漏了 promote 闸门审批留痕这一格）。

---

## 维护本文件

- **新增单**：写全六字段（目标 / 上下文 / 要求 / 约束与非目标 / 验收 / 边界），并显式引用**冻结契约的路径与节号**。
- **派工前先证伪前提**：读码核对工单写的前提；不符就停下回报 + 给拆法，不硬做、不扩范围。
- **跨仓引用写 `repo:path`**（如 `tellurion:docs/TASKS.md`），不做可点击相对链接。
- **顺序纪律**：同一波次内撞同一批文件的单按单号串行，一单一 commit。
