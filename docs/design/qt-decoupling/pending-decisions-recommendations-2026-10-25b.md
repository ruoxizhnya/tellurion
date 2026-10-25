---
status: active
last-verified: 2026-10-25
verified-by: 13 条待裁决项逐条实测（12 仓 git 快照 + 代码级取证：ADR-024/029 原文、live_bridge 调用面、risk 快照编码、A 集三集合、playwright 配置、order_acks DDL、SYSTEM-DESIGN §7）
---

# 待裁决项 · 逐条建议（2026-10-25 第二轮）

> **⚠️ 2026-10-25 第三轮订正**：全量总表与 ① / ② / ⑧ / ⑰ 四处已被 [终版建议（c）](pending-decisions-recommendations-2026-10-25c.md) 取代（① 分支已实现拆 port；⑰ data 也建了 `index_daily` 且 DDL 不同；⑧ 过期面更广；② 只迁 5 个符号编译不过）。**其余条目本文仍然有效**。
>
> 配套：[第二批分组方案](cross-repo-dispatch-plan-wave2-2026-10-25.md) §5 的 13 条 · [裁决记录](cross-repo-decision-record-2026-10-25.md) ⑯。
> **⑬（service token 401）已按本文件建议直接修了代码**，见 §0。其余 12 条**只给建议、不动代码**。
> 每条给：**建议 → 依据（实测 file:line）→ 若采纳的动作 → 代价**。

---

## §0 已动手：service token 401（⑬ / 裁决 ⑯）

**改了什么**（kit 仓，3 处 + 1 条测试）：

| 文件 | 改动 |
|---|---|
| `auth/claims.go` | 新增 `const ServiceRole = "service"`，注释写明「**不是角色枚举**」（守住 KT-09 ③-④），只因服务 token 的 subject 非数字而在此命名 |
| `auth/httpadapter/httpadapter.go` | `authenticate()` 在 `Kind != access` 检查之后、`UserIDInt64()` 之前加分支：`if claims.Role == auth.ServiceRole { return claims, "", nil }` |
| `auth/middleware.go`（gin 变体） | 同样分支：设 `CtxUsername/CtxRole/CtxClaims`，**不设 `CtxUserID`**（不伪造 user id，审计侧看到"无用户"而非用户 0） |
| `auth/httpadapter/httpadapter_test.go` | 新增 `TestAuthenticate_ServiceTokenFromIssueServiceToken`：用 `IssueServiceToken("service:risk", …)` 签一个**真 token** 喂进 `Authenticate`，并断言**非 service 角色 + 非数字 subject 仍 401**（防伪造 user token 搭便车） |

**为什么这条测试是关键**：缺陷之所以能活下来，正是因为 kit 的 `service_token_test.go` 只验签发侧（`NotEmpty` 就算过），没把 token 送进适配层；risk 的 `TestPolicy_*` 又走 `IssueTokens(1, …)` 的数字 subject。**两端各自绿、中间那条缝没人测**。

**⚠️ 未验证（如实登记）**：**本机没有 Go 工具链**（`go` / `gofmt` 不在 PATH，全盘无 `go.exe`；Docker daemon 也不可达）⇒ `go build/vet/test` **一条都没跑**。改动是逐行对照 **core 已上线并已有钉子测试的同一实现**（`tellurion:pkg/auth/middleware.go:68-81` + `middleware_test.go:164` `TestMiddleware_ServiceTokenNonNumericSubject_OK`）做的移植，但**移植正确性未经编译器确认**。

**所以我没有做下面两步**（避免把未验证的代码发出去）：
- ❌ **没打 tag**（应发 `v0.1.4`）
- ❌ **没升 risk / data / analytics 三仓的 pin**（今天都钉 `v0.1.1`）

**你需要做的**（在有 Go 的环境）：

```
cd tellurion-kit && gofmt -l . && go build ./... && go vet ./... && go test ./auth/... -count=1
git tag -a v0.1.4 -m "fix(auth): service token 的 subject 不再被 int64 校验拒掉"
# 然后三个消费仓各一次：go get github.com/ruoxizhnya/tellurion-kit@v0.1.4 && go mod tidy
```

---

## 摘要（12 条一行建议）

| # | 事项 | 建议 | 一句话理由 |
|---|---|---|---|
| ① | QT-51 拆 port 还是同构桥 | **都不选 —— 删 shim，把 LiveBridge 搬出 `pkg/backtest`** | 实测 `pkg/backtest` 的 live 面**生产零调用**，5 个方法全是 ADR-020 的兼容 shim |
| ② | risk 快照类型迁 contracts | **迁 5 个符号 + 必须带对拍腿；不许改写成 Canonical 编码** | 改编码 = 全部历史 `risk_rules_hash` 作废（违反不变量 8） |
| ⑤ | 生产 `visible_at` 回填授权 | **按表分批授权，判据 = 该表在 core 侧已无写者** | 实测 core 仍写 3 张 A 集表 ⇒ 全量回填会让新行 PIT 拒收 |
| ⑥ | UI 三服务栈怎么起 | **先只跑不需要后端的 `vitest/typecheck/lint`；e2e 进 CI** | playwright 配置**没有 `webServer`** ⇒ 必须手工起 5173/8081/8085 + PG/Redis |
| ⑦ | XTP 柜台 P1~P5 | **保持 ⛔，本轮不投人** | 四处零 SDK 的事实没变 |
| ⑧ | DOC-10 范围 | **只改 `SYSTEM-DESIGN.md:123/127` 两处事实 + 补一行"今天到哪"** | 阶梯表是设计演进记录，重写会丢"当初为什么这样分" |
| ⑨ | `broker.order_acks` 保留期 | **90 天，且只删 `ack IS NOT NULL` 的行；同时加 `reserved_at` 索引** | 删未 ack 行 = 丢掉"发过但没回"的证据 |
| ⑩ | AI 生成物载体形态 | **不新裁 —— 按 ADR-024/029 的四层判据：默认 `carrier="dsl"`，L3 才 wasm，`so` 已被否决** | ADR-024 明写**不做 `plugin.Open`**（Windows 不支持），三选一里那条已经死了 |
| ⑪ | R3 的 RunSpec 形状 | **裁为「core 的 `cmd/worker` 装配」，analytics 不用补全** | worker 已能从 `backtest_jobs` 恢复 `{run_id}` 锚点（`106b35c` 自述） |
| ⑫ | promote 审批人从哪来 | **面 B：core 作唯一对外入口 + service token 认证调用方** | 给 strategy 引全套鉴权是 QT-71 的域，会把「补一条留痕」变成「打开 12 条裸路由」 |
| ⑰ | H4 物理搬迁被 core 剩余写者挡住 | **把 `market.index_daily` 从"第二写者"名单里划掉（明确 core 保留写）；只推进 A 集里 core 仍写的 3 张** | `index_daily` **不在 A 集 12 张内** ⇒ 它不是 X1d 的目标 |
| ⑱ | X0 生产者的契约 | **orchestration 新增 artifact 写入端点，worker 用 service token 调它** | worker 直写库同时违反「本仓零 import core」与「core 不双写」两条硬约束 |
| ③ | worker 进程归谁 | ✅ **已消解**（core `cmd/worker` 已落） | — |
| ④ | 第二写者停法 | ⚠️ **只消解一半**（见 ⑰） | — |

---

## ① QT-51：不造 `ExecutionPort`，删 shim + bridge 外移

**建议**：**不要新建 `ExecutionPort`**。真正的问题是「实盘桥住错了包」，不是「缺一个执行端口」。

**依据（实测，这条推翻了工单的问题设定）**
- 工单 ③-① 要求「定义 `ExecutionPort`（下单/撮合/成交回报）」。但实测：**回测的撮合语义来自 `pkg/backtest/execution` 自己的 `ExecutionService`**（`SetExecutionService`，见 `kernel-interface-map.md:61`），**与 `pkg/live` 无关**。
- `pkg/live` 在 `pkg/backtest` 里**只被 `LiveBridge` 用到**，而 `LiveBridge` 在引擎里只被 5 个方法碰：`Set` / `Get` / `ExecuteSignal` / `ExecuteSignals` / `HealthCheck`（`engine.go:2267/2294/2335/2344/2352`）。
- 这 5 个方法在**生产代码里零调用**：`ExecuteSignalViaLiveTrader` / `ExecuteSignalsViaLiveTrader` / `HealthCheckLiveTrader` / `LiveBridge()` / `WithLiveTrader` 全仓 grep **只命中注释与 SPEC.md 文档**，没有一处真调用。
- 唯一的生产调用是 `SetLiveTrader`：`cmd/analysis/main.go:106`、`cmd/analysis/wiring/modules_analysis.go:229` —— 而 `cmd/analysis` **本来就自己持有 trader**（`ExecutionHandler.trader live.LiveTrader`，`handlers_execution.go:34`），再塞进引擎是纯冗余。

⇒ **结论**：`pkg/backtest` 对实盘执行**没有需求**。强行造 `ExecutionPort` 反而要为 `OrderResult` / `PositionInfo` / `AccountInfo` / `EmergencyFlattenResult` 四个值类型造重复定义（Go 接口是结构匹配，签名换了命名类型就不匹配），**多一层成本换零收益**。

**若采纳的动作**
1. `LiveBridge`（`pkg/backtest/execution/live_bridge.go`）**整体外移**到 `pkg/livebridge`（或直接并入 `pkg/live`）——它做的是"信号→委托"的实盘语义，归实盘侧。
2. `pkg/backtest` 删：`liveBridge` 字段、`NewLiveBridge` / `LiveBridge` 两个 alias（`aliases.go:141/156`）、5 个 shim 方法、`WithLiveTrader`（`options.go:127`）。
3. `cmd/analysis` 两处 `engine.SetLiveTrader(...)` 删除；`ExecutionHandler` 已自带 trader，无需经引擎。
4. `cmd/analysis/wiring_guard_test.go:50` 那条 pin（`"engine.SetLiveTrader(executionTrader)"`）同批更新。
5. 护栏：`internal/repoguard` 加「`pkg/backtest` 不得 import `pkg/live`」。
6. 测试面：`fakes_test.go` / `signal_reconcile_e2e_test.go` / `execution/live_bridge_test.go` 三处随之调整（**这三处必须改，否则护栏腿红**）。

**代价**：`LiveBridge` 迁走后**短期内无生产调用方**（它今天也是零调用）。可接受——它不是死代码，是"实盘委托侧的组件"，未来 `ExecutionHandler` 可以复用它做"信号→下单"，而不是让回测引擎持有它。

---

## ② risk 快照迁 contracts：迁 5 个符号，**绝不许改编码**

**建议**：迁 `Snapshot` / `SnapshotRule` / `Bundle` / `SnapshotHash` / `DomainSnapshotHash` + `ErrSnapshotMismatch`；**不迁** `ParseBundle` 与 `Bundle.ForScope`。

**依据（实测）**
- **编码不能动**：`risk:internal/rule/snapshot.go:57-100` 的 `SnapshotHash` 是**手写长度前缀编码**（`strings.Builder` + `writeField` + `sort by rule_id` + `strconv`），域标签 `DomainSnapshotHash = "tellurion.risk_snapshot_hash.v1"`（`:20`）。而 contracts 有另一套 `NewCanonical(...)`（`engine/canonical.go`）。
  ⇒ **若迁进 contracts 时"顺手改成 Canonical 编码"，全部历史 `risk_rules_hash` 立即作废** —— 直接违反 risk 自己的不变量 8（「钉住的历史 hash 永不失效」）与 `SnapshotHash` 注释里的红线。
- **迁 `SnapshotHash` 是允许的**：contracts 已有同类先例 —— `engine/provenance.go:78` 的 `func (in StrategyVersionInput) Hash()` 就是「canonical 编码 + sha256」的**实现**，且带跨仓对拍腿（`provenance_vector_test.go` 的 `TestStrategyVersionInput_MatchesStrategyRepoEncoding`）。所以 ADR-0001 ②「禁止任何实现」在实践中指「无业务逻辑 / 无 DB」，**纯函数哈希不在此列**。
- **`ParseBundle` 不该迁**：它是 decode + `verifyHash` + 默认值填充（`snapshot.go:142`），属"解析策略"；放 contracts 会让 contracts 随 risk 的 wire 形状改动而发版。
- **core 其实不需要 `ParseBundle`**：`resolve` 的响应**已整包内嵌快照**（`risk:internal/httpapi/api.go:127-130` `resolveBody.Snapshot`）。core 拿到的是一份 JSON，**要做的事只有两件**：① 用 `SnapshotHash` 自核（一行）；② 把 `params` 映射到限额面（靠 QT-74 的词典）。**都不需要 `ParseBundle`**。
- **`ForScope` 也不必迁**：QT-74 ④ 已定一期 core 只消费 `scope = global` ⇒ core 侧 inline 过滤即可。

**若采纳的动作**
1. contracts 新增（建议放 `engine/snapshot.go`，与 `provenance.go` 同级）：四个类型 + `SnapshotHash`（**逐字节搬运实现**，含 `ruleTypes` / `scopes` 两个冻结词表）+ `DomainSnapshotHash` + `ErrSnapshotMismatch`。
2. **必须同时加**：固定向量测试 + **与 risk 仓的对拍腿**（照抄 `provenance_vector_test.go` 的做法），否则迁移后两边算出的 hash 是否一致**无法自证**。
3. risk 侧把 `internal/rule/snapshot.go` 的这 5 个符号改成 contracts 的别名/转发（保留 `ParseBundle` 与 `ForScope` 在本仓）。
4. core 侧用 `contracts.SnapshotHash` 自核 + inline `scope == "global"` 过滤。

**代价**：迁完后 **risk 与 core 都要升 contracts pin**（risk 今天零 contracts 依赖，这是它的第一个 pin）。这是一次真实的"首引依赖"，不是小改。

---

## ⑤ 生产 `visible_at` 回填：按表分批授权

**建议**：**不全量授权**。按「**该表在 core 侧已无写者**」逐表授权；core 仍在写的 3 张**排除**。

**依据（实测）**
- A 集 = **12 张**（`data:docs/db-schema.md:149`）：`ohlcv_daily_qfq`、`ohlcv_minute`、`realtime_quote`、`capital_flow`、`sectors`、`stock_sector_map`、`top_list`、`limit_up_pool`、`announcements`、`news`、`hot_search`、`global_ohlcv`。
- **core 仍在写其中 3 张**（实测 `pkg/storage` 的 INSERT）：`ohlcv.go:15/53` → `ohlcv_daily_qfq`；`sector.go:56` → `sectors`；`sector.go:96` → `stock_sector_map`。
- `capital_flow` / `top_list` **已改投 data**（`ac6b7e0`）⇒ 这两张 core 不再写。
- 回填后 PIT 门立即生效，`visible_at IS NULL` 的历史行**整批拒收**（台账红线）⇒ 对 core 仍在写的表回填，等于"回填完立刻又被 core 写进一批 NULL 行"，**回填成果会被持续污染**。
- 回填脚本护栏已就绪且 fail-closed：`internal/schema/backfill_pg_test.go` 用 `-tags backfill`，只认 `DATA_TEST_DSN` 且库名须 `_test`。

**若采纳的动作（建议的授权口径）**
- **第一批（可授权）**：`capital_flow`、`top_list` + 其余 7 张「core 侧 grep `INSERT INTO <表>` = 0」的表 —— **执行前必须逐表跑一次这个 grep 并留证**。
- **第二批（等）**：`ohlcv_daily_qfq` / `sectors` / `stock_sector_map` —— 等 core 停这三处写入（即 X1 的剩余第二写者）。
- **执行者**：运维，**不是 agent**。agent 只交「逐表 grep 取证 + 回填语句 + 幂等判据」，不碰生产库。
- 回填必须**幂等可重放**（`WHERE visible_at IS NULL`），并在回填后跑一次「PIT 拒收计数」对账。

**代价**：分批意味着 PIT 门只能对部分表生效 ⇒ 读路径若跨多表，仍需按「该表是否已回填」分支。这比"全量回填后统一开 PIT"更啰嗦，但**不会产生半污染状态**。

---

## ⑥ UI 三服务栈：先跑无后端子集，e2e 进 CI

**建议**：① 批的 16 条**按"是否需真栈"拆两半**：能落到 `vitest` / `vue-tsc` / `eslint` 的先做；需要真栈的交给 CI（或一台有 Docker 的机器）。

**依据（实测）**
- `tellurion-ui/e2e/playwright.config.ts` **没有 `webServer` 块** ⇒ Playwright **不会自动起服务**，它假定 `BASE_URL=http://localhost:5173`（前端）、`DATA_SERVICE_URL=http://localhost:8081`、`BACKEND_URL=http://localhost:8085` **已经在跑**，且 `helpers/isolation.ts` 还要连 PG 做隔离。
- 本机：**Go 不可用**（无法起 core 的 analysis-service :8085）、**Docker daemon 不可达**（无法起 PG/Redis/data-service）。
- `web/package.json` 有不依赖后端的脚本：`test`（`lint:tests && vitest run`）、`typecheck`（`vue-tsc --noEmit`）、`lint`（`eslint .`）。
- 已有 33 个 `*.test.ts` 单测文件 + 12 张视觉基线快照（`visual-regression.spec.ts-snapshots/`）。

**若采纳的动作**
1. 先跑：`cd tellurion-ui/web && npm ci && npm run typecheck && npm run lint && npm run test`。
2. 16 条结构漂移里**凡是"组件结构/类名/ARIA/文案"类**的，尽量改写成 vitest 断言（这样它们从此不需要真栈）—— 这本身是测试债的根治方向。
3. 视觉回归（`visual-regression.spec.ts`）若只依赖前端 dev server，可单独在有 Node 的环境跑；它不依赖 8085（错误态可接受）。
4. `api-*.spec.ts` / `critical-e2e.spec.ts` / `copilot-e2e.spec.ts` **明确记为「需真栈，本机不可执行」**，进 CI。

**代价**：结构漂移里有一部分（如"页面在真数据下的布局"）**本质上需要真栈**，无法改写成单测 ⇒ 这部分只能等 CI。**不要为了"跑起来"而把它们写成假绿的单测**。

---

## ⑦ XTP：保持 ⛔

**建议**：**本轮不投人**，不排期。

**依据**：`broker:internal/gateway/xtp.go` 只有自研骨架、零第三方 SDK import；四层深度 grep 无 `xtptrader` / `xtp-api`。台账已记 P1~P5 五项外部条件（SDK / 测试账号 / 出网 / C 工具链 / 盯盘时间窗）。

**若采纳的动作**：保持现状（决策材料已落），**不做**「无 SDK 的假实现」——那会产出无法自证的代码，违反"验收必须可复现"。

**代价**：X6c 一直停着，交易通道只有 `MockTrader`。这是已知且已被接受的现状。

---

## ⑧ DOC-10：只改两处事实 + 补一行"今天到哪"

**建议**：**只改 `SYSTEM-DESIGN.md` 的 2 行，不重写阶梯表**。

**依据（实测）**：两处硬过期：
- `SYSTEM-DESIGN.md:123`：`| **X1~X6** | 六个能力服务按触发条件逐个起 | ⬜ 未触发 |` —— **今天六个都已实装**。
- `SYSTEM-DESIGN.md:127`：`六个服务仓只有占位 doc.go，api-schema.md 描述的是目标契约` —— **今天不对**（data/risk/analytics/orchestration/broker/strategy 都有实现与测试）。

**若采纳的动作**
1. `:123` 改为「**X1~X6 已全部起仓并实装**（各仓台账为准）」。
2. `:127` 改为「六仓均已有实现；`api-schema.md` 的"目标契约"与"已实现"差异**逐仓在各仓台账登记**」。
3. 在 §7 末尾**加一行状态表**：`X1 数据 / X2 策略 / X3 风控 / X4 分析 / X5 编排 / X6 交易` 各标「已实装 / 部分 / 阻塞」——**一行就够**，指向各仓台账。

**代价**：不重写阶梯表 ⇒ 文档里"当初为什么这样分层"的演进记录保留，代价是"今天的状态"要靠那一行状态表 + 各仓台账两跳才能看到。

---

## ⑨ `broker.order_acks` 保留期

**建议**：**90 天**，且**只删 `ack IS NOT NULL` 的行**；同时**加 `reserved_at` 索引**（现在没有）。

**依据（实测）**
- 表结构（`broker:internal/ackstore/ddl.go:24-32`）：`PRIMARY KEY (account_id, client_order_id)`，`ack JSONB`（**可空**）、`reserved_at`、`updated_at`。**没有任何 `reserved_at` 索引** ⇒ 若做「按时间删」，会全表扫。
- 语义（`ddl.go:3-5`）：「只做通道适配器的**写前日志**……记录『这个 ClientOrderID 发过没有、柜台当时回了什么』」。⇒ **`ack IS NULL` 的行表示"已预留但柜台没回"** —— 这是**未决状态**，删了就等于丢掉「发过但没回」的证据，下次同 ID 重试会被当成首次提交（**重复下单风险**）。
- 判定字段用 `updated_at`（`ack` 回来时会更新），比 `reserved_at` 更准。

**若采纳的动作**
1. 清理作业：`DELETE FROM broker.order_acks WHERE ack IS NOT NULL AND updated_at < now() - interval '90 days'`，**分批 + 限流**（单批 ≤ 5000 行，避免长事务锁表）。
2. 先加索引：`CREATE INDEX IF NOT EXISTS idx_order_acks_updated_at ON broker.order_acks(updated_at) WHERE ack IS NOT NULL`.
3. **90 天的依据**：客户端重试窗口远短于 90 天；这个数足够覆盖"跨周排障"需求。若柜台对账周期更长（如月度），改 180 天。
4. 把这条写进 `broker:docs/db-schema.md` §1.1（真相源与保留期同处）。

**代价**：加了清理作业 ⇒ 多一个需要监控的定时任务；**不清理** ⇒ 表单调增长（每笔委托一行，日积月累会拖慢 PK 查询）。

---

## ⑩ AI 生成物载体：不新裁 —— ADR 已定，且三选一里有一条已死

**建议**：**不要在三选一里选**。按 **ADR-024（部分被 ADR-029 取代）+ 2026-10-08 四层表达力 D2 裁决**的既有判据落：
- **默认 `carrier="dsl"`**（表达式配置，L1/L2）；
- **仅当意图属 L3（自定义状态机 / 非标准滤波，L2 也表达不了）才 `carrier="wasm"`**；
- **`carrier="so"` 已不是选项**。

**依据（实测）**
- `tellurion-docs:docs/adr/adr-024-expression-as-execution-target.md:20-23` 原文：**「不做 `plugin.Open`」——完整保留**；理由「Windows 根本不支持 `-buildmode=plugin`，而开发环境就是 Windows」；并注明 wazero（WASM）「纯 Go、无 CGO、**支持 Windows**，恰好绕开本 ADR 拒绝 plugin 的核心理由」。
  ⇒ **agent 台账把 `carrier=so` 列成三选一之一，已经被 ADR-024 判死**。
- 同文件 `:11-14`：ADR-024 已被 ADR-029 §2 **部分取代**为**双轨**（A 表达式 / B WASM），`:41-43` 的 D2 裁决进一步细化为 **L0 → L1 → L2 → L3 逐层升级，每层先问"上一层能不能做"**。
- 同文件 `:24-26`：「**LLM 生成代码是 artifact** 在轨道 A 下不变 —— 轨道 A 跑的仍是表达式，代码只真编译校验不执行」
  ⇒ **`artifact_hash` 该挂的是"表达式的配置"，不是"编译出来的可执行文件"** —— 这正好解决 agent ⑦① 「本仓 go build 产出的是临时可执行文件，不是资产服务认可的载体」的困境。
- `contracts:engine/provenance.go:67` 的 `Carrier` 值域注解已是 `"wasm" / "so" / "builtin" / "dsl" / "external"` ⇒ **`dsl` 已经在契约里**，不用扩。

**若采纳的动作**
1. agent 侧 AI 产物 → 落**表达式策略配置**（`carrier="dsl"`），hash 由 `contracts.engine.StrategyVersionInput{Carrier:"dsl", Source:<配置文本>, …}.Hash()` 产出 —— **直接复用 QT-41 的资产服务**，不用新造载体。
2. 把「L3 才 wasm」写成判定规则（引用 ADR-029 §2 与 D2），**不新增默认值**。
3. 台账里 `carrier=so` 那条**删掉**（不是"待选"，是"已否决"）。

**代价**：AI 产物从"可执行 wasm"退回"表达式配置"，**表达力受限**（自定义状态机表达不了）。这正是 ADR-024「自由度是负债」的原意；真需要 L3 时按判据升层，不是现在就默认可执行。

---

## ⑪ R3 的 RunSpec 形状：裁为「core `cmd/worker` 装配」

**建议**：**R3 裁决 = 执行侧（core `cmd/worker`）负责装配完整 RunSpec；analytics 的 `base_run_spec` 保持只有 `run_id`**，这条未落项可直接关闭。

**依据（实测）**
- analytics 现状：`internal/sensitivity.go:45` `BaseRunSpec map[string]string`，`:60` `BaseRunSpec: map[string]string{"run_id": job.RunID}`；`:4` 注释明写「只带 `run_id` 作为 interim 锚点（完整 RunSpec 的装配属执行侧，**R3 未裁**）」。
- **执行侧已经能接住**：core `106b35c` 的 commit 自述原文「analytics sensitivity 的 `{run_id}` 锚点**从 `backtest_jobs` 恢复后覆盖**」⇒ worker 拿到 `{run_id}` 就知道去哪取完整参数。
- orchestration 的 `Claim`/`kernels` 已有 `kernels` 空数组归一成 NULL 的用例；派活契约已定。

⇒ **analytics 不需要补全 RunSpec**；需求方（worker）已经用"锚点 + 回源"解决了。若强行让 analytics 装配完整 RunSpec，它会**重复 core 的知识**（RunSpec 的字段语义在 core）。

**若采纳的动作**：R3 写一条裁决记录（`base_run_spec` 的 `run_id` 锚点是**终态设计**，不是 interim），analytics 台账把该未落项标 ✅（说明"由执行侧回源，本仓不补"）。

**代价**：每次执行 sensitivity 子 run 都要多一次 `backtest_jobs` 读（可忽略）。

---

## ⑫ promote 审批人：面 B（core 网关 + service token）

**建议**：**面 B —— core 作唯一对外入口**：UI 调 core（core 已有 JWT 中间件 + `route_policy`），core 解析出审批人 `sub` 后，**用 service token 调 strategy 的 promote，并把审批人放进受信头**。**不给 strategy 引鉴权栈**。

**依据（实测）**
- strategy **零鉴权**：全仓 `JWT_SECRET|Bearer |Authorization|RequireRole|Roles\b` = **0 命中**；无 auth/role/guard/middleware 文件；`go.mod` **无 `tellurion-kit`**；`internal/httpapi/api.go:42-56` 的 **12 条路由全裸**。
- core 全仓 `grep "/promote"` = **0 命中** ⇒ 今天**没有调用方**。
- UI 已在打 `/api/strategies`（`web/src/api/strategy.ts:5/31`），且 core 自己有 `registerStrategyRoutes`（`cmd/analysis/main.go:287`）⇒ **core 已经是策略面的对外入口**，把 promote 也收进来不新增拓扑。

**为什么不选面 A（给 strategy 引 kit auth）**
- 一次只补"promote 留痕"，却要把 **12 条裸路由**一起打开 ⇒ 范围爆炸，且鉴权是 QT-71 的域（会与 core 的 `route_policy` 形成两套策略表）。
- strategy 新 pin kit ⇒ 又要升 `go.mod` + 版本矩阵核对。

**为什么面 B 不是"caller 自报"**
- 裁决 ⑭ 禁的是「**caller 自由填写**审批人」。面 B 里：调用方身份由 **service token 认证**（可验签、可轮换），审批人由 **core 在已验证的用户会话里取出** 后写入受信头 ⇒ 审批人是 core **背书**的，不是 caller 随手填的。这条信任边界要**明文写进 strategy 的 `docs/api-schema.md`**（"promote 的 actor 头由受信网关写入，直连该端点不构成有效审批"）。

**若采纳的动作**
1. core 新增 strategy client（今天没有）+ `POST /api/strategies/{id}/versions/{hash}/promote` 的转发端点，在 `route_policy.go` 登记为 **trader/admin**（**不是 service**——promote 是人的动作）。
2. strategy 的 promote 端点：读受信头 `X-Actor`（缺失即 400 `actor_required`），落 `compile_log` 或新表 `strategy.strategy_promotions`（审批人 / 时间 / 结论）。
3. strategy 侧**不为该端点加鉴权**；但在 `docs/api-schema.md` 写明「**只能由受信网关调用**」，并在部署上把 strategy 端口设为**仅集群内可达**（这是比应用层鉴权更便宜的隔离）。
4. **核心闭环那一环要另立单**：QT-32 ⑤ 要的「未过 promote 的产物被回测路径拒绝」是 **core 的 `strategy_ref` 解析**要检查 `status == promoted`（读 `GET /api/strategies/{id}/resolve`），**与 promote 端点无关**——这条现在没人做，建议单独立项。

**代价**：core 多一个 strategy client 与一条转发路由；信任依赖"服务内网 + service token"，**不是端到端零信任**。可接受——strategy 今天连端口隔离都没有，这已经是净改善。

---

## ⑰ 物理搬迁被 core 剩余写者挡住：把 `index_daily` 划出，只追 A 集 3 张

**建议**：**明确「core 保留写 `market.index_daily`」**，把它从"待消灭的第二写者"名单里**划掉**；只把 **A 集里 core 仍写的 3 张**（`ohlcv_daily_qfq` / `sectors` / `stock_sector_map`）作为后续。

**依据（实测）**
- **`market.index_daily` 不在 A 集 12 张里**。A 集清单（`data:docs/db-schema.md:149`）：`ohlcv_daily_qfq`、`ohlcv_minute`、`realtime_quote`、`capital_flow`、`sectors`、`stock_sector_map`、`top_list`、`limit_up_pool`、`announcements`、`news`、`hot_search`、`global_ohlcv` —— **无 `index_daily`**。
- X1d 的头号目标是「**A 集表的单一写者 + `visible_at` 门**」（台账原文：「迁移不是改名……头号目标是『单一写者』」）。`index_daily` 既不在 A 集，也就**不参与 `visible_at` 回填**（回填只动 A 集）。
- core `pkg/storage/index_daily.go:31/88` 写 `market.index_daily` 是**设计内的市场数据写入**（DATA-12，Migration 057），不是"遗留的第二写者"。
- A 集里 core 仍写的 **3 张**（实测 `pkg/storage` 的 INSERT）：`ohlcv.go:15/53` → `ohlcv_daily_qfq`、`sector.go:56` → `sectors`、`sector.go:96` → `stock_sector_map`。

**若采纳的动作**
1. 在 data 的台账与 `docs/db-schema.md` 里**写明**：「`market.index_daily` 的写者 = core，本仓不认领、不迁」。
2. 物理 `SET SCHEMA` 的**范围收窄到 A 集**，并把「core 仍写 3 张」列为该项的唯一阻塞。
3. 那 3 张的推进顺序：先给 data 加对应写端点 → 停 core 写 → 再搬迁 + 回填（与 ⑤ 的分批口径一致）。

**代价**：core 与 data 会**长期各写一部分 market 表**（core 写 index_daily + 3 张过渡期，data 写其余）。这是可接受的过渡态，**但必须在两份 `db-schema.md` 里都写明"谁是某表的唯一写者"**，否则下一个人还会把它当缺陷重查一遍。

---

## ⑱ X0 生产者契约：orchestration 开写入端点，worker 调它

**建议**：**orchestration 新增 artifact 写入端点**（`POST /api/runs/jobs/{id}/artifacts`，或并入现有 `/result` 的请求体），**由 core 的 `cmd/worker` 用 service token 调用**；**不要**让 worker 直写库。

**依据（实测）**
- orchestration **有 store 无端点**：`internal/pgstore/artifacts.go:35` `saveArtifactSQL`、`:97` `SaveArtifact`（实现 `queue.ArtifactStore`），但全仓 `HandleFunc` 里**没有任何 artifact 写入路由**（实测只命中 store 实现与 `provenance_test.go`）。
- core worker **只回 `/result`**：`internal/orchestrationworker/client.go:167/178` 只 POST `/api/runs/jobs/{id}/result`，**不带三 hash**。
- 两条硬约束挡住"worker 直写库"：① orchestration 台账 ⑤-①「**本仓零 import core**」是验收项；② 裁-19「**core 不做双写**」。
- 幂等已有先例：`saveArtifactSQL` 注释写明「写入 / **幂等重放**一次留痕」⇒ 幂等键已经设计好了。

**若采纳的动作**
1. orchestration 加 `POST /api/runs/jobs/{id}/artifacts`，入参 = worker 算出的三 hash + artifact 元数据；**幂等键 `(run_id, kind, content_hash)`**（沿用 `saveArtifactSQL` 的写法）。
2. **写者身份 = worker**：该端点 Roles 加 `"service"`（与裁-17 一致），**不加 viewer/trader**。
3. 写入时**与 `quant.run_provenance` 的三 hash 对拍**（core 侧已有 `GET /api/backtest/{id}/provenance`，worker 可先取后比）—— 不一致即 fail-closed 拒写。
4. core worker 在 `/result` 之后（或同批）调该端点。

**代价**：worker 多一次 HTTP 往返；`run_artifacts` 的写入可用性依赖 orchestration 在线。**但这正是「零 import core + 不双写」两条约束的必然结果** —— 不接受它，就只能推翻其中一条。

---

## 附：按「现在要不要拍」排序

**现在就要拍（卡住可动工的单）**：
- **①** QT-51 方向（决定 H2 能不能开工）
- **②** 迁 contracts 的符号清单 + 「不许改编码」这条红线
- **⑩** AI 载体（决定 agent 后续单能不能立）
- **⑱** X0 生产者契约（决定 H5 能不能收口）

**可以顺带拍（有明确证据、风险低）**：
- **⑤** 分批授权口径 · **⑧** DOC-10 两行 · **⑨** 90 天 + 只删已 ack · **⑪** R3 终态 · **⑰** 把 `index_daily` 划出第二写者名单

**需要你先给外部条件**：
- **⑥** 有没有一台有 Docker 的机器 / CI 跑 e2e
- **⑦** XTP SDK 与测试柜台
- **⑫** 是否接受「strategy 端口仅集群内可达」这个部署前提

**已动手、需要你在有 Go 的环境收尾**：
- **⑬** service token 401（代码已改 + 测试已写，**未编译验证**，未发 tag、未升 pin）