---
status: active
last-verified: 2026-10-25
verified-by: 22 条决策项逐条实测（12 仓源码 + 分支/worktree + 5 份台账；只读取证，未跑 Go / 未起服务 / 未写库）
---

# 待裁决项 · 终版建议（2026-10-25 第三轮）

> 配套：[第二批分组方案 §5](cross-repo-dispatch-plan-wave2-2026-10-25.md) · [裁决记录](cross-repo-decision-record-2026-10-25.md) · [第二轮逐条论证（b）](pending-decisions-recommendations-2026-10-25b.md)。
> **b 仍然有效**：②⑤⑥⑦⑧⑨⑩⑪⑫⑱ 的详细取证在 b，本文件不重复抄。
> **本文件的作用**：① 给全量总表（含 ⑰~⑳ 与两条台账外新项 ㉑㉒）；② **订正 b 的 ① / ⑰ / ⑧ / ② 四处**；③ 登记本轮三条新发现（两套 `index_daily` DDL / R1 读面三态不分 / QT-51 分支已实现）。
> 冲突时以本文件 + 实测为准。

---

## §0 先看：本轮三条新发现（都不是台账问题，是代码事实）

### 0.1 `market.index_daily` 有两份不同的 DDL，谁先建表谁定形

| 出处 | 形状 |
|---|---|
| core `pkg/storage/postgres.go:1180`（Migration 057） | `id BIGSERIAL PRIMARY KEY` + `UNIQUE (code, trade_date)` + `source VARCHAR(64)` + 非空 `CHECK` |
| data `internal/schema/market.go:301` | `PRIMARY KEY (code, trade_date)`、**无 `id`**、`source VARCHAR(32)`、`volume DEFAULT 0`、无 `CHECK` |

两边都是 `CREATE TABLE IF NOT EXISTS` ⇒ **先启动的一方静默决定表形状**，另一方不生效也不报错。data 的 `TableNames()`（`internal/schema/schema.go:47`）与 `docs/db-schema.md:25` 把它当自己的表；`internal/schema/visible_at.go:54` 又把它列进 C 集（门外）。

**这条比「谁是第二写者」更急**：第二写者只是重复写同一张表；两套 DDL 是**同一张表两个定义**。→ 见 ⑰。

### 0.2 R1 读面今天把「没跑完」「跑失败」「不存在」压成同一个 404

- core `/report` 的持久路径：`job == nil || job.Status != "completed"` 一律 404（`tellurion:cmd/analysis/handlers_backtest.go:191`）。所以 `pending` / `running` 与引擎 `failed` 到调用方时都是 `run_not_found`。
- analytics 已经把三态线备好了：`run_result_failed` / `run_result_incomplete` 409、`run_not_found` 404（`tellurion-analytics:internal/httpapi/api.go:296-306`，`internal/analysis/errors.go:11-13`）。缺的是 core 不发出前两个码。
→ 见 ⑲。

### 0.3 QT-51 分支已经实现，且形态与 b 的假设不同

- 分支 `task/qt51-execution-port`（`7c89aae`，worktree `C:/Users/ruoxi/Workspace/tellurion-qt51`）**已 1 ahead / 9 behind main**，不在 origin。
- 它做的是：`pkg/backtest/execution/port.go` 定义 `ExecutionPort`；`cmd/analysis/wiring/live_trader_port.go` 把 `live.LiveTrader` 适配成 port；`internal/repoguard/backtest_live_isolation_test.go` 钉住 `pkg/backtest` 不 import `pkg/live`；另带设计文档与等价性报告。
- 所以 b 的「不造 port、删 shim、把 LiveBridge 移出去」写在分支之前，**前提已变**。→ 见 ①。

---

## §1 总表（需要你拍的 16 条 + 已裁待落地 6 条）

| # | 事项 | 建议（一句话） | 现在拍？ |
|---|---|---|---|
| ① | QT-51 拆 port 还是同构桥 | **取分支的「拆 port」形态，收口后合入**；不再按 b 的「删 shim + bridge 外移」重做 | ✅ 现在 |
| ② | risk 快照迁 contracts | **新建 `risk` 包，迁 wire 类型 + 冻结枚举 + hash 纯函数；`Bundle`/`ParseBundle` 留 risk；编码逐字节不许动** | ✅ 现在 |
| ⑤ | 生产 `visible_at` 回填 | **只授权 9 张「core 无写者」的 A 集表；3 张 core 仍在写的等停写** | ✅ 现在 |
| ⑥ | UI 三服务栈怎么起 | **无栈子集先做；e2e 要你启动 Docker Desktop（或 CI）后跑；视觉基线在本机重生成** | ⚠️ 要外部条件 |
| ⑦ | XTP 柜台 P1~P5 | **保持 ⛔，本轮不投人** | ✅ 现在（确认不投） |
| ⑧ | DOC-10 范围 | **不只两行：§7 的阶梯表与三条过渡态一起按实仓订正**，§8 以后不动 | ✅ 现在 |
| ⑨ | `broker.order_acks` 保留期 | **90 天；只删 `ack IS NOT NULL`；先加 `updated_at` 部分索引** | ✅ 现在 |
| ⑩ | AI 生成物载体 | **默认 `carrier="dsl"`；只有 L3 才 wasm；`so` 已死**；hash 用 `StrategyVersionInput{Carrier:"dsl"}.Hash()` | ✅ 现在 |
| ⑪ | R3 的 RunSpec 形状 | **终态 = 执行侧装配（worker 用 `run_id` 锚点回源）；analytics 不补全** | ✅ 现在 |
| ⑫ | promote 审批人从哪来 | **面 B1：core 网关 + `X-Actor`；strategy 只对 promote/internal 三端点引 kit service-token 校验，端口仅集群内可达** | ✅ 现在 |
| ⑬ | service token 401 | **方向 3 已改完代码；只差跑门禁 → 发 `v0.1.4` → risk/data/analytics 升 pin** | ⚠️ 要 Go 环境 |
| ⑰ | `index_daily` 归属 + 第二写者 | **owner = core；data 撤认领并删重复 DDL/TableNames/C 集条目**；A 集物理搬迁等 3 张停写 | ✅ 现在 |
| ⑱ | X0 生产者契约 | **orchestration 开 artifact 写端点（service 角色），worker 调它；不直写库**；前置是 R5 鉴权 | ✅ 现在 |
| ⑲ | R1 读面语义 | **结论态放行**：404 只给不存在；pending/running 409 `run_result_incomplete`；failed 409 `run_result_failed` | ✅ 现在 |
| ⑳ | AUD-61 testid 边界 | **允许窄例外**：只给 3 个组件加 `data-testid`（纯增量），单独 commit | ✅ 现在 |
| ㉑ | g3 worktree / 分支处置 | **先确认 3 笔文档已落 main，再删 worktree+分支**（删除要你授权） | ⚠️ 要授权 |
| ㉒ | broker R9 / R10 | **R10 现在修（两处映射读失败改 `ErrIdempotencyStoreUnavailable`）；R9 暂缓到 X6c** | ✅ 现在 |

**已裁、只差落地（不用再拍）**：DR① 服务 token 分发与部署清单（QT-71 已落）/ DR④ QT-32 本代做且拆两步（agent 侧施工中）/ DR⑩ openapi 独占写 + 自检（QT-72 已落）/ DR⑪ U8 归 agent、U6 暂缓（已落）/ DR⑬ `producer_pending` 归 orchestration / DR⑮ QT-73 五包（已落）/ DR② `analysis` 包不建（已裁）/ DR⑤⑥⑦⑧ DBT-15~18（core 台账已 ✅）。

> **DR⑭（审批人取 JWT `sub`）已被 ⑫ 取代**：strategy 仓零鉴权、零调用方，原裁决没有承载体。

---

## §2 逐条建议

### ① QT-51：取分支的「拆 port」，不再重做第二种形态

**建议**：**裁决 = 拆 port**（不是「同构桥」），以 `task/qt51-execution-port` 为基线收口。b 的「删 shim + LiveBridge 外移」**不再执行**。

**依据（实测）**
- 分支已经满足 BE-02 的结构目标：`pkg/backtest` 对 `pkg/live` **零 import**，由 `internal/repoguard/backtest_live_isolation_test.go` 钉住；回测撮合仍走 `pkg/backtest/execution` 自己的 `ExecutionService`，行为不变。
- 分支把「同时认识两边」的位置收敛到装配层 `cmd/analysis/wiring/live_trader_port.go`——这比把 `LiveBridge` 搬进 `pkg/live` 更小：后者会让 `pkg/live` 反向认识回测信号类型，制造新的环。
- b 的依据仍成立：5 个 shim（`ExecuteSignalViaLiveTrader` / `ExecuteSignalsViaLiveTrader` / `HealthCheckLiveTrader` / `LiveBridge()` / `WithLiveTrader`）生产零调用；`SetLiveTrader` 只有两处装配调用。

**若采纳的动作**
1. **补裁决记录**：在裁决记录里写「BE-02 = 拆 port，形态见分支」，并在 `backtest-engine-review.md:145` 那条 checkbox 上勾掉、注明日期。
2. **rebase 到 main**（分支落后 9 笔），在**有 Go 的环境**复跑：`gofmt -l .` / `go build ./...` / `go vet ./...` / `go test ./... -count=1`；再单跑 `internal/repoguard`、`pkg/backtest/execution`、`cmd/analysis/wiring` 三组。
3. **合入后另立小单**删 5 个零调用 shim（先 grep 确认仍为 0，且测试不依赖）；`SetLiveTrader` 保留（装配层在用）。
4. 合入前**不新增** `ExecutionPort` 之外的抽象；分支的 port 已够用。

**代价**：rebase 要在 `pkg/backtest/engine.go` / `aliases.go` / 文档三处解冲突；分支的等价性报告是分支作者在旧基线上跑的，**合入后必须重跑一次**，不能拿旧报告当合并后的证据。

---

### ② risk 快照迁 contracts：新建 `risk` 包，只迁「wire + 冻结枚举 + hash」，`Bundle` 不迁

**建议**：contracts 新增 `risk` 包（**不并入 `engine`**），迁：
- `Snapshot` / `SnapshotRule`；
- `Scope` / `RuleType` 及**冻结词表**（`ruleTypes` / `scopes`）；
- `DomainSnapshotHash`、`MaxParamsBytes`；
- hash 纯函数（建议做成 `Snapshot.Hash()`，与 `StrategyVersionInput.Hash()` 同形）；
- `ErrSnapshotMismatch` / `ErrInvalidRuleType` / `ErrInvalidScope` / `ErrInvalidParams`（hash 函数会返回它们）。

**不迁**：`Bundle`（有未导出索引字段，是 risk 的进程内派生结构）、`ParseBundle`（decode + 默认值填充，属策略）、`ForScope`（core 一期只消费 `scope="global"`，inline 过滤即可）、Store / HTTP。

**依据（实测）**
- `tellurion-risk:internal/rule/snapshot.go:20` 是域标签，`:57-100` 是**手写长度前缀编码**（`writeField` + 按 `rule_id` 排序 + `strconv`），不是 contracts 的 `NewCanonical`。**换成 Canonical = 全部历史 `risk_rules_hash` 作废**，直接违反 risk 的不变量 8。
- `SnapshotRule` 依赖 `Scope` / `RuleType`（`model.go:12/29`）与 `canonicalParams`（`model.go:150`）；hash 还引用三个哨兵（`errors.go:17-22`）⇒ 只迁「5 个符号」编译不过，必须带上这些。
- `Bundle` 的 `byScope` 未导出（`snapshot.go:121` 起），跨包无法构造；`ParseBundle` 在 risk 内返回它，迁走类型会让实现搬家，得不偿失。
- contracts 已有纯函数先例：`engine/provenance.go:78` 的 `StrategyVersionInput.Hash()`，并有跨仓对拍腿。ADR-0004 允许值对象上的纯函数。

**若采纳的动作**
1. 新建 `contracts/risk/`（包名 `risk`），**逐字节搬运**编码实现（含 `writeField`、`canonicalParams`、两个词表）。
2. **必须同时加**：固定向量测试 + 与 risk 仓的**对拍腿**（risk 侧用同一组 `SnapshotRule` 算两次，断言 hex 相同），照抄 `provenance_vector_test.go` 的做法。
3. risk 侧把这批符号改成 `contracts/risk` 的**别名/转发**，调用点零改；`ParseBundle` / `Bundle` / `ForScope` 留本仓。
4. core 侧用 `contracts/risk` 反序列化 + `Snapshot.Hash()` 自核 + inline `scope=="global"` 过滤。

**代价**：contracts 发 MINOR（建议 `v0.7.0`）；risk 首次引 contracts 依赖、core 升 pin；别名层是一层长期维护面。**不许**为了「少迁几个符号」把编码改成 Canonical——那会把历史 hash 全部作废。

---

### ⑤ 生产 `visible_at` 回填：只授权 9 张，3 张等停写

**建议**：分两批授权，**执行者是运维，不是 agent**：
- **第一批（现在可授权）**：`ohlcv_minute`、`realtime_quote`、`capital_flow`、`top_list`、`limit_up_pool`、`announcements`、`news`、`hot_search`、`global_ohlcv`（9 张）。
- **第二批（等）**：`ohlcv_daily_qfq`、`sectors`、`stock_sector_map`（3 张）——core 今天仍在写（`pkg/storage/ohlcv.go:15,53`；`pkg/storage/sector.go:56,96`，后者由 `cmd/data/handlers_sync.go:128` 的派生同步触发）。
- **`market.index_daily` 不在 A 集**（`tellurion-data:docs/db-schema.md:149`），不回填。

**依据（实测）**
- A 集 = 12 张（同上）。core 生产装配里 `RegisterExecutors` 只有 9 个执行器（`cmd/data/sync_handlers.go:143-159`），其中只有 `ohlcv` / `ohlcvAll` 写 A 集；`capital_flow` / `top_list` 已改投 data（`ac6b7e0`）；其余 7 张没有执行器，也没有 `Save*` 直写函数。
- **取证口径要修正**：不能只 grep `INSERT INTO <表>`——core 的 `BulkInsert` 是动态表名（`pkg/storage/bulk_insert.go:42-72` 的 `TableMapper` + `:331` 的 `INSERT INTO %s`）。逐表取证必须同时查：① 字面 INSERT；② `TableFor(dataType)` 映射；③ 作业注册与执行器。否则会把 `ohlcv_minute` 这类误判成「有写者」或反过来。
- 回填脚本护栏已 fail-closed：`-tags backfill` + 只认 `DATA_TEST_DSN` 且库名 `_test`（`internal/schema/backfill_pg_test.go`）。

**若采纳的动作**
1. 运维逐表执行：`SELECT count(*) FROM <t> WHERE visible_at IS NULL` + 上面的三条 grep，**留证**；0 行的表直接跳过。
2. 回填语句必须幂等（`WHERE visible_at IS NULL`），幂等可重放；回填后跑一次「PIT 拒收计数」对账。
3. 第二批的前置写死：data 先有对应写端点 → core 停写 → 再回填 + 搬迁。

**代价**：PIT 门只能对部分表生效；跨多表的读路径要按「该表是否已回填」分支。比全量回填啰嗦，但不会产生「回填完又被 core 写进 NULL 行」的半污染。

---

### ⑥ UI 三服务栈：无栈子集先做，e2e 等你给环境

**建议**：
1. **现在就能做**：`cd tellurion-ui/web && npm ci && npm run typecheck && npm run lint && npm run test`（不依赖后端）。
2. **16 条结构漂移逐条判定**：组件级结构/类名/ARIA/文案能改写成 vitest 的，改写成 vitest（从此不需要真栈）；必须看真数据布局的，留在 e2e。
3. **e2e 需要你**：启动 Docker Desktop（当前 daemon 不可达）后按 compose 起 PG/Redis + data :8081 + analysis :8085 + 前端 :5173；或交 CI（service containers）。
4. **视觉基线 4 条**在**本机**重生成（字体/平台相关），CI 只做回归。

**依据（实测）**
- `tellurion-ui:e2e/playwright.config.ts` **没有 `webServer` 块**（1-40 行），只读三个 URL 环境变量 ⇒ 不会自动起任何服务；`helpers/isolation.ts` 还要连 PG。
- 本机 Docker daemon 不可达、Go 不在 PATH ⇒ 无法在 agent 侧起 analysis/core。
- `web/package.json` 的 `test` / `typecheck` / `lint` 均不依赖后端。

**代价**：结构漂移里「真数据下的布局」无法用单测替代；**不要**为了跑绿把它写成假绿单测。

---

### ⑦ XTP：保持 ⛔

**建议**：本轮不投人、不排期；QT-45 保持 ⛔。

**依据**：broker / core / 本机 / CI **四处零 SDK**（`internal/gateway/xtp.go` 只有自研骨架）；P1~P5 五项外部条件没有一项解除。台账与 `docs/x6c-xtp-readiness.md` 已记全。

**代价**：交易通道只有 `MockTrader`，这是已知且已接受的现状。**不做**无 SDK 的假实现（无法自证的代码违反验收纪律）。

---

### ⑧ DOC-10：§7 整块按实仓订正，不是只改两行

**建议**：订正范围 = `SYSTEM-DESIGN.md` §7 的**阶梯表 + 三条「已知过渡态」+ 一行状态表**，再加 `docs/README.md:40` 的 QT-60 行；**§8 以后与阶梯表的「为什么这样分」不动**。

**依据（实测，需逐格核）**
- `SYSTEM-DESIGN.md:123`「X1~X6 未触发」：六仓都有真 `cmd/*service` 监听与自有 schema 的 PG 装配 ⇒ 已不成立；但各仓完成度不同（risk / analytics / orchestration / broker 各有未落项），不能写成「全部完成」。
- `SYSTEM-DESIGN.md:127`「六个服务仓只有占位 `doc.go`」：已不成立。
- 同表 **S1/S2/S3 的状态也过期**：contracts 已发到 `v0.6.0`，agent 已无 `replace` / 无 core require（`tellurion-agent:go.mod`）⇒ S1/S2/S3 都该改 ✅；**W0 的 01/02/04 三腿是否全绿未核实**，本单要先核再改，不许照抄。
- 「agent 仍 `replace ../tellurion`」那条也过期（同上）。
- `tellurion-docs:docs/README.md:40` 仍写「工单 QT-60，未启动」，而 SYSTEM-DESIGN 同仓同页写 S-1 ✅ ⇒ 文档自相矛盾。

**若采纳的动作**：逐格改状态并指回各仓 `AGENTS.md` 的「状态」节；末尾加一行「X1 数据 / X2 策略 / X3 风控 / X4 分析 / X5 编排 / X6 交易」的「已实装 / 部分 / 阻塞」，**不复制各仓正文**。

**代价**：读者仍要两跳（状态行 → 各仓台账）才能看到细节；换来的是设计演进记录不被重写。

---

### ⑨ `broker.order_acks`：90 天，只删已回执，先加部分索引

**建议**：
1. `CREATE INDEX IF NOT EXISTS idx_order_acks_updated_at ON broker.order_acks (updated_at) WHERE ack IS NOT NULL;`
2. 清理：`DELETE FROM broker.order_acks WHERE ack IS NOT NULL AND updated_at < now() - interval '90 days'`，单批 ≤ 5000 行。
3. 90 天写进 `broker:docs/db-schema.md` §1.1，并注明「依据 = 客户端重试窗口远短于 90 天；若柜台可查窗口更长，改 180 天」——**柜台可查窗口这个事实今天未知**，别把 90 写成断言。

**依据**：`internal/ackstore/ddl.go:24-32` 有 `ack JSONB`（可空）、`reserved_at`、`updated_at`，**无任何索引**；`internal/ackstore/pg.go:102` 在回执时更新 `updated_at`。`ack IS NULL` = 已预留但柜台没回，是未决证据，删了下次同 ID 重试会被当首次提交。

**代价**：多一个要监控的定时任务；不清理则表单调增长、PK 查询变慢。

---

### ⑩ AI 载体：默认 `dsl`，L3 才 wasm，`so` 已死

**建议**：**不新裁三选一**，按 ADR-024 / ADR-029 / 2026-10-08 四层表达力裁决落：
- 默认 `carrier="dsl"`（表达式配置，L1/L2）；
- 仅 L3（自定义状态机 / L2 表达不了）才 `carrier="wasm"`；
- **`carrier="so"` 删除**（不是待选，是已否决）。
- `artifact_hash` = `contracts.engine.StrategyVersionInput{Carrier:"dsl", Source:<配置文本>, …}.Hash()`，复用 QT-41 资产服务。

**依据（实测）**
- `tellurion-docs:docs/adr/adr-024-expression-as-execution-target.md:20-23` 明写不做 `plugin.Open`（Windows 不支持）；`:24-26` 明写 LLM 生成代码只是 artifact、轨道 A 跑表达式；`adr-029` 把载体分双轨（表达式 / WASM）。
- `tellurion-contracts:engine/provenance.go:67` 的 `Carrier` 值域已含 `"dsl"`，不用扩契约。
- core **已有** wasm host 并接进真实回测：`pkg/backtest/wasm_signal_provider.go:64-99`、`pkg/backtest/options.go:219` ⇒ L3 不是空想，但**不是默认**。

**代价**：AI 产物默认退回表达式配置，表达力受限（自定义状态机要显式升 L3）；这正是 ADR-024「自由度是负债」的原意。

---

### ⑪ R3：终态 = 执行侧装配，analytics 保持 `{run_id}` 锚点

**建议**：裁 R3 为「**执行侧（core `cmd/worker`）负责装配完整 RunSpec**」；analytics 的 `base_run_spec` 只有 `run_id` 是**终态设计**，不是 interim；关闭 analytics 的该未落项。

**依据（实测）**
- core `internal/orchestrationworker/spec.go:3-38` 已支持两种形状：完整 RunSpec，或 `run_id` 锚点 + `LoadBase` 从 `backtest_jobs` 恢复基础请求，再用 `strategy_params` 覆盖。
- analytics `internal/upstream/sensitivity.go:4/60` 只发 `{"run_id": …}`；注释写「完整装配属执行侧，R3 未裁」。
- 让 analytics 装配完整 RunSpec 会**复制 core 的字段语义**（RunSpec 的真相在 core），违反单一真相。

**代价**：每个 sensitivity 子 run 多一次 `backtest_jobs` 读（可忽略）。

---

### ⑫ promote：面 B1（core 网关 + `X-Actor` + 三端点 service-token 校验）

**建议**：**面 B1**，不是 b 的裸「面 B」：
1. **core 作唯一对外入口**：新增 strategy client + `POST /api/strategies/{id}/versions/{hash}/promote` 转发路由；`route_policy` 登记 **trader/admin**；从已验证的 JWT claims 取 `sub`，写入受信头 `X-Actor`，并用 service token 调 strategy。
2. **strategy 只对 3 条端点**（`/promote` + 两条 `/internal/*`）引入 kit `auth/httpadapter` 的 service-token 校验；**不**给 12 条路由全套鉴权。`X-Actor` 缺失即 400 `actor_required`；落一条审计（新表 `strategy.strategy_promotions` 或 `compile_log` 追加：审批人/时间/结论）。
3. **strategy 端口只绑集群内/回环**（部署层）。其余 9 条路由今天仍无鉴权，靠网络隔离兜底——在 `api-schema.md` 写明。

**依据（实测）**
- strategy 全仓零鉴权（`JWT_SECRET|Bearer |Authorization|RequireRole|Roles\b` = 0 命中；无 auth/role/guard/middleware 文件；`go.mod` 只有 contracts，**无 kit**）；12 条路由全裸（`internal/httpapi/api.go:42-56`）。
- core 全仓 `grep "/promote"` = 0 命中 ⇒ 今天**没有调用方**；UI 也没有 promote 按钮。core 的 `registerStrategyRoutes`（`cmd/analysis/handlers_strategy.go:13`）读的是**本地 StrategyDB**，不是 strategy 资产服务 ⇒ 面 B 要新增 client，不是「把已有路由转出去」。
- 只校验 3 条端点而不是 12 条：把「补一条留痕」的范围压到最小；`/internal/*` 本来就是机器面，天然该用 service 角色。

**代价**：strategy 首次引 kit + JWT secret + 3 条策略（部署清单在 core）；core 新增 client/路由；信任边界是「集群内 + service token」，不是端到端零信任。**另立单**：QT-32 ⑤ 的「未 promote 的 `strategy_ref` 被回测拒绝」在 core 的解析侧，与本端点无关。

---

### ⑬ service token 401：方向 3 已改完，只差发版与升 pin

**建议**：**采纳方向 3**（按 `claims.Role == "service"` 分流，只豁免 service 角色），把已改的代码跑完门禁后发 `v0.1.4`，risk / data / analytics 升 pin。

**依据（实测）**
- kit 工作区已有 4 文件改动（`auth/claims.go` 加 `ServiceRole`；`auth/httpadapter/httpadapter.go` 在 `UserIDInt64()` 前分流；`auth/middleware.go` 同；`httpadapter_test.go` 加「真 `IssueServiceToken` 签的 token 能被 `Authenticate` 接受」用例）。
- 缺陷本体：`auth/service_token.go` 允许非数字 subject，而 `auth/httpadapter/httpadapter.go:139` 要求 `UserIDInt64()` ⇒ `service:risk` 必 401。方向 1 放宽校验会放过非 service 的坏 subject；方向 2 与「service 主体可读」冲突。
- **未验证**：本机无 Go 工具链 ⇒ `go build/vet/test` 一条没跑；**未提交、未发版、三仓 pin 仍 `v0.1.1`** ⇒ 线上「仍 401」这个事实没变。

**若采纳的动作**
```
cd tellurion-kit && gofmt -l . && go build ./... && go vet ./... && go test ./auth/... -count=1
# 发版纪律：README 兼容矩阵 + CHANGELOG + tag 三件套（KT-02）
git tag -a v0.1.4 -m "fix(auth): service token 的 subject 不再被 int64 校验拒掉"
# risk / data / analytics 各一次：go get …/tellurion-kit@v0.1.4 && go mod tidy
```
必须先提交工作区那 4 个文件，再打 tag。

**代价**：4 个仓的提交与 pin；kit 发版三件套。**在此之前不要在任何台账写「服务间链路已通」**。

---

### ⑰ `index_daily`：owner = core；data 必须撤认领并删重复 DDL

**建议**：
1. **裁决 `market.index_daily` 的 owner = core**（DATA-12，Migration 057 是唯一 DDL）。
2. data 侧删除：`internal/schema/market.go:301` 的建表 + 索引、`TableNames()` 里的条目（`schema.go:47`）、`visible_at.go:54` 的 C 集条目、`docs/db-schema.md:25/151` 的认领；加守卫（data 的 schema 源码不得再出现 `market.index_daily` 的 `CREATE TABLE`）。
3. core 侧在 Migration 057 注释里写明「data 已撤认领，本处是唯一 DDL」。
4. **A 集物理 `SET SCHEMA` 不排期**：先停 core 对 3 张 A 集表的写（data 出对应写端点 → 停写 → 再搬迁 + 回填）。
5. `market.index_daily` **不迁 data、不回填 `visible_at`**（不在 A 集，且在 C 集门外）。

**依据（实测）**
- 两套 DDL 形状不同（见 §0.1）：core 有 `id` / `UNIQUE` / `source VARCHAR(64)` / `CHECK`；data 是复合 PK / `VARCHAR(32)` / `volume DEFAULT 0`。`CREATE IF NOT EXISTS` 让先启动者静默胜出。
- core 的写点 `pkg/storage/index_daily.go:31` 是设计内路径（DATA-12）；data 仓**没有** `index_daily` 的读写代码（只在 schema DDL / TableNames / C 集里出现）。
- A 集 12 张（`tellurion-data:docs/db-schema.md:149`）不含 `index_daily`。

**代价**：data 部署不再创建该表 ⇒ 依赖 core 先迁移（同一库，core 启动即建）。若将来真要让 data 拥有整个 `market` schema，再单独立项把写路径搬过去，不在本轮顺手做。

---

### ⑱ X0 生产者：orchestration 开 artifact 写端点，worker 调它

**建议**：**不**让 worker 直写库。
1. orchestration 新增 `POST /api/runs/jobs/{id}/artifacts`（或并入 `/result` 的请求体），写者身份 = **service 角色**；复用已有 `queue.ArtifactStore.SaveArtifact`（`internal/pgstore/artifacts.go:35/97`）的幂等与身份漂移拒绝。
2. **前置 = R5 鉴权**：orchestration 先接 kit `auth/httpadapter` + policy 表（至少覆盖 worker 面与新端点），否则「写者身份」不可验。
3. core worker 在 `/result` 之后（或同批）POST 三 hash；三 hash 取自本次 run 的 provenance（core 进程内已写 `quant.run_provenance`）。

**依据（实测）**
- orchestration 有 store 无路由：`internal/httpapi/api.go:90-92` 只有 jobs 的 POST/GET，无 artifact 写端点；`SaveArtifact` 仍只被测试与编译期断言消费。
- core `internal/orchestrationworker` 对 `artifact|ContentHash|provenance` 零命中 ⇒ worker 不回三 hash。
- 两条硬约束挡住直写：orchestration「零 import core」+ 裁-19「core 不做双写」。

**代价**：orchestration 首次引 kit + JWT secret + policy；worker 多一次 HTTP 往返，可用性依赖 orchestration 在线。这是两条硬约束的必然结果。

---

### ⑲ R1 读面：结论态放行，404 只给「不存在」

**建议**：改 core `/report` 的放行判据为**结论态**：

| 情形 | 今天 | 建议 |
|---|---|---|
| 行不存在 | 404 `run_not_found` | 404 `run_not_found`（不变） |
| queued / running | 404 `run_not_found` | **409 `run_result_incomplete`** |
| 引擎 failed | 404 `run_not_found` | **409 `run_result_failed`** |
| completed + invalid | 200 + `status="invalid"` | 不变 |
| completed + valid | 200 | 不变 |

**依据（实测）**
- core 今天的判据是队列态：`handlers_backtest.go:191` 的 `job.Status != "completed"` 一律 404；`:200-211` 只对已完成的行走 `result_status` 三值。
- analytics 的 wire 码与错误类型已经齐备（`internal/httpapi/api.go:296-306`、`internal/analysis/errors.go:11-13`），只差 core 发出前两个码。
- UI 的 `/report` 调用发生在轮询完成之后（`web/src/api/backtest.ts:41`），只要补 409 的错误处理即可；openapi + e2e 同笔改。

**代价**：改一个既有端点的状态码语义（非 2xx 面），要同步 openapi / UI 错误处理 / e2e；Migration 059 把历史 invalid 行回填成 `completed` 的**已丢失信息无法恢复**，如实登记为历史缺口，不尝试反推。

---

### ⑳ AUD-61：允许窄例外，只补 `data-testid`

**建议**：给 3 个组件（`NavTiles.vue` / `MarketMetrics.vue` / `AppSidebar.vue`）**只加 `data-testid` 属性**，不改类名、结构、文案、行为；**单独一个 commit**，与 e2e 对齐分开。工单 ④ 的「不动 `web/src`」按此窄例外修订。

**依据（实测）**
- `tellurion-ui:docs/TASKS.md:98`：这三个组件「只有类锚、零 testid」；`:155` 是「DO NOT 动 `web/src` 生产代码」。
- 工单自己的选择器纪律是「优先 `data-testid`，不靠文案/DOM 位置」（`:38`）。用类锚对齐会在下一次结构重构时再碎一次。

**代价**：确实动了生产代码（虽然是纯增量属性）；需要 `npm run typecheck` + vitest；必须单独 commit 以便回滚。视觉基线 4 条不吃 testid，另按 ⑥ 处理。

---

### ㉑ g3 worktree / 分支：先确认文档已落 main，再删

**建议**：
1. 确认 DBT-15/16/17 的 3 笔纯文档落账**已在 main**（core 台账已见 ✅ 完成记录）；g3 的 7 笔里 4 笔代码已判作废。
2. 如无遗漏，删除 worktree `C:/Users/ruoxi/Workspace/tellurion-g3-data` 与分支 `task/g3-data`。**删除要你明确授权**；如需留痕，先打 `archive/g3-data` tag。
3. 顺带清理我早前做验证用的临时 worktree `C:/Users/ruoxi/AppData/Local/Temp/b63-verify`（detached，无改动）。

**依据**：`git worktree list` 实测；裁决记录 §四.1 已裁「不 cherry-pick、3 笔文档在 main 重做、4 笔代码作废」。

**代价**：删分支后历史只能从 reflog/tag 找回；先确认再删。

---

### ㉒ broker R9 / R10：R10 现在修，R9 等 X6c

**建议**：
- **R10 现在修**：`internal/gateway/mock.go:415`（QueryOrder）与 `:358`（CancelOrder）把映射读失败翻译成 `ErrChannelDown`，改为 `ErrIdempotencyStoreUnavailable`（哨兵已存在：`internal/gateway/errors.go:34`，wire 码已在 `api-schema.md:88`）。两处同口径，一起改 + 各一条用例。
- **R9 暂缓**：Mock 的 T+1 / 超卖放行 / 费用恒零三条**不在本轮收紧**。它是测试替身，不是生产通道；收紧属契约行为变更，等 X6c 真柜台一起做，并把三条写进 X6c 的验收清单（`docs/x6c-xtp-readiness.md` §8）。

**依据**：实测两处都是 `fmt.Errorf("%w: %v", ErrChannelDown, err)`；`api-schema.md:88` 已定义 `idempotency_store_unavailable` = 幂等映射不可用、**不发送**。R10 是诊断质量问题，改对不会造成发送。

**代价**：R10 改后调用方看到的错误码变化（仍是不发送 + 熔断，处置方向一致）；R9 暂缓意味着 Mock 全绿仍不等于柜台绿，这一点必须在 X6c 清单里写明。

---

## §3 已裁、只差落地（不需再拍）

| 裁决 | 现状 | 剩余动作 |
|---|---|---|
| DR① service token 分发 / 部署清单归 core | QT-71 已落（`aaf37bc` / `134b283`），`pkg/auth` 有 `RoleService`、route_policy 有 Service 档、k8s 清单 10 份 | 与 ⑬ 一起核 risk/data/analytics 的 secretRef 是否真的注入了 `JWT_SECRET` |
| DR④ QT-32 本代做、拆两步 | agent QT-32-A 施工中 | promote 留痕走 ⑫；AI 生成侧在 agent 收口 |
| DR⑩ openapi 独占写 + 双向对拍 | QT-72 已落 | 无 |
| DR⑪ U8 归 agent / U6 暂缓 | data `db-schema.md` 已改判；U6 标「本轮不裁」 | 无 |
| DR⑬ `producer_pending` 归 orchestration | core 不引入；orchestration 侧提示位待实现 | 并入 ⑱ 的 orchestration 单 |
| DR⑮ QT-73 五包 | 已落（`170f09a`），两条行为差异已登记 | 无 |
| DR② 不建 `analysis` 包 | 已裁（analytics 本仓 owner；触发条件成文） | 无 |
| DR⑤⑥⑦⑧ DBT-15~18 | core 台账均 ✅（删除已执行、产出请求成文、两表仅储备、mining 归 agent） | 无 |

---

## §4 对 b 的四处订正（b 原文保留，以本节为准）

| b 的结论 | 实测 | 本版改为 |
|---|---|---|
| ①「不造 `ExecutionPort`；删 shim + `LiveBridge` 外移」 | 分支 `7c89aae` **已经造了 port**，并带装配适配与 import 护栏；b 的前提（「还没有 port」）已变 | **取分支的拆 port 形态**；5 个零调用 shim 合入后另立小单删；不再外移 `LiveBridge` |
| ②「迁 5 个符号」 | `SnapshotRule` 依赖 `Scope`/`RuleType`/`canonicalParams`，hash 还返回三个哨兵 ⇒ 5 个符号编译不过 | 迁 **wire 类型 + 冻结枚举 + hash 纯函数 + 4 个哨兵**；`Bundle`/`ParseBundle` **不迁**（b 原写迁 `Bundle`） |
| ⑧「只改 `SYSTEM-DESIGN.md:123/127` 两处」 | 同表 S1/S2/S3 与「agent replace」「六仓占位」三条也都过期；`docs/README.md:40` 还写 QT-60 未启动 | **§7 整块订正 + README 一行**；W0 三腿先核再改 |
| ⑰「`index_daily` 不在 A 集 ⇒ 明确 core 保留写即可」 | data **也建了这张表**，且 DDL 与 core 不同（§0.1）；只在文档里「划掉」消不掉两套 DDL | **data 撤认领 + 删 DDL/TableNames/C 集条目 + 加守卫**；不是只写一句 |

---

## §5 取证边界（如实登记）

- **未跑**：本机 `go` 不在 PATH、Docker daemon 不可达 ⇒ 分支、kit 改动、任何 `go test` 都**没有**在本机跑过；所有「绿」都没有本轮证据。
- **只读取证**：未改任何业务代码、未写库、未起服务、未切分支、未删 worktree。
- **未核实**：SYSTEM-DESIGN §7 的 W0（01/02/04 三腿）完成度；六服务各仓「部分/阻塞」的逐项状态（本文件只给了判据与出处，没逐仓抄）。
- **会过期的数**：各仓 ahead/behind、A 集逐表行数、`visible_at IS NULL` 计数——执行前以当日实测为准。

---

## §6 你只需要回的 13 个决定

1. ① 取分支的拆 port（是/否）
2. ② 新建 contracts `risk` 包、`Bundle` 不迁（是/否）
3. ⑤ 只批 9 张表（是/否）
4. ⑧ §7 整块订正（是/否；W0 允许我先核再改）
5. ⑨ 90 天 + 只删已 ack（是/否）
6. ⑩ 默认 `dsl`、`so` 删除（是/否）
7. ⑪ R3 终态 = 执行侧（是/否）
8. ⑫ 面 B1（是/否）
9. ⑰ core 独占 `index_daily`、data 撤认领（是/否）
10. ⑱ orchestration 开端点 + 先做 R5 鉴权（是/否）
11. ⑲ 结论态放行（是/否）
12. ⑳ 允许给 3 个组件补 testid（是/否）
13. ㉒ 修 R10、暂缓 R9（是/否）；㉑ 授权删除 g3 worktree/分支（是/否）

> ⑬ 不需要你选方向（方向 3 已定）；只需要一台有 Go 的机器跑门禁。⑥⑦ 需要外部条件，见 §2。
