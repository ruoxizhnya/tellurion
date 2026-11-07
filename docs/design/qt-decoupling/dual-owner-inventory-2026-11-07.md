---
status: active
last-verified: 2026-11-07
verified-by: SVC-DUAL-01 五列清单（2026-11-07：按 `core-governance-decision-materials-2026-10-26.md` §2 的 55 行逐行改写为「owner / 当前写者 / 读方 / 对拍方式 / 停写条件」五列；四簇各至少 1 行、每行带 `repo:path:line`；market 簇按裁决 ⑰ 不重开，只留 roster 指针；**只读取证，0 代码改动**）
---

# 双 owner 全量清单（SVC-DUAL-01）

> **本文件是冻结的只读清单**，不是计划。它回答「同一份事实今天有几个可写落点、谁写、谁读、怎么对拍、什么条件下能停」。
> 输入材料：`core-governance-decision-materials-2026-10-26.md` §2（55 行逐张可核）。**本文件不重派工**：
> 「谁该先停写」归各仓 `SVC-*-G3`，「停写后删代码」归六条 `SVC-CORE-*-RETIRE`。
> 行数口径：market 22（已裁，见 §7）+ run 2 + strategy 1 + ai 7 + core-only 23 = **55**。
> （材料 §2.3.2 第 3 行 `backtest_adjustment_anchors` 与 §2.3.6 的同名行是**同一张表**，在 55 里只算一次。）

## 1. 五列定义

| 列 | 含义 | 判据 |
|---|---|---|
| **owner** | 这张表/这份事实的**目标**唯一归属仓 | 裁决 ⑰ / ADR-036 §4 / 各仓 `db-schema.md`。**不许空**，也不许写「待定」 |
| **当前写者** | 今天真正执行写语句的进程与位置 | `repo:path:line`，取自 `git grep ... -- '*.go' ':!*_test.go'` 的字面写点；动态拼表名另注（§8） |
| **读方** | 今天读它的进程与位置 | 同上，`FROM <table>` 命中 |
| **对拍方式** | 两侧可比时用什么比 | `无` = **实测没有**工具/测试，不是「应该有」；已存在的直接写测试名 |
| **停写条件** | 允许把「当前写者」置零的前置 | 四步：建第二写者 → 对拍 → 切读 → `W=0`；特殊语义另注 |

> `W`/`R` 的含义沿用材料 §2.3：core 生产侧（不含测试）字面写点 / 读点命中数。

## 2. run 簇（2 行 T 类，新 owner = `tellurion-orchestration`）

| 表 | owner | 当前写者 | 读方 | 对拍方式 | 停写条件 |
|---|---|---|---|---|---|
| `backtest_jobs` | `tellurion-orchestration`（`run.backtest_jobs`，`tellurion-orchestration:internal/queue/ddl.go`） | core：`tellurion:pkg/storage/backtest_jobs.go:14`（W=6，含 `:29` 的 UPDATE） | core：`tellurion:pkg/storage/backtest_jobs.go:67`（R=4） | **无**（两侧都可读，`cmd/recon` 已就位：`-pair public.backtest_jobs=run.backtest_jobs`） | ①orchestration 收编 job 面 ②`id/status/updated_at` 逐行对拍 ③core `JobService` 切 HTTP ④`W=0` |
| `walk_forward_reports` | `tellurion-orchestration`（`run.walk_forward_reports`） | core：`tellurion:pkg/storage/cache.go:593`（W=1） | core：`tellurion:pkg/storage/cache.go:625`（R=3） | **无**（orchestration 侧**无生产读写**，今天无可比两侧） | 同上；**且 orchestration 侧先要有写者**（`SVC-ORCH-G2` 未落） |

> `backtest_adjustment_anchors` 在材料 §2.3.2 出现过一次，但它**无服务仓对手方** ⇒ 归 §6 core-only，不重复计行。

## 3. strategy 簇（1 行 T 类，新 owner = `tellurion-strategy`）

| 表 | owner | 当前写者 | 读方 | 对拍方式 | 停写条件 |
|---|---|---|---|---|---|
| `strategies` | `tellurion-strategy`（`strategy.strategy_definitions`） | core：`tellurion:pkg/storage/strategies.go:14`、`tellurion:pkg/storage/strategies.go:107`（软删 `is_active=FALSE`，W=2） | core：`tellurion:pkg/storage/strategies.go:37`（R=5） | **无**；且**两侧不是同形状**（core 单表 + `is_active` vs strategy 定义 + 版本链）⇒ 行级对拍无意义，判据是「唯一写者」 | ①strategy 侧建「定义 + 版本」写者 ②`strategy_id` ↔ `name/type/params` 对拍（**先对齐软删语义**）③core `tellurion:pkg/strategy/db.go` 切 HTTP ④`W=0` |

## 4. ai 簇（7 行 T 类，新 owner = `tellurion-agent`）

| 表 | owner | 当前写者 | 读方 | 对拍方式 | 停写条件 |
|---|---|---|---|---|---|
| `experiments` | `tellurion-agent`（`ai.experiments`） | core：`tellurion:pkg/storage/experiments.go:108`（W=4，另 `:171`/`:200`/`:304`） | core：`tellurion:pkg/storage/experiments.go:131`（R=2） | **有**：`tellurion-agent:cmd/ai/experiment_stopwrite_integration_test.go:28`（`public.experiments` count + max(id) 冻结断言） | core 侧 6 个方法**生产零调用** + 该断言长期绿 |
| `factor_hypothesis` | `tellurion-agent`（`ai.factor_hypothesis`） | core：`tellurion:pkg/storage/factor_hypothesis.go:35`、`:108`（W=2） | core：`tellurion:pkg/storage/factor_hypothesis.go:59`（R=2） | **无**（可用 `cmd/recon` 补 `-pair public.factor_hypothesis=ai.factor_hypothesis`） | `SeedBuiltinFactorHypotheses` 调用点归零（今天唯一生产调用 `tellurion:pkg/storage/postgres.go:47`，启动 seed） |
| `factor_genes` | `tellurion-agent`（`ai.factor_genes`） | **无**（core W=0） | **无**（core R=0） | **有**：`tellurion-agent:pkg/ai/gene_pool/pool_ai_schema_integration_test.go:24`（write-scope：`public.*` 0 行） | core 侧已无读写方 ⇒ **只等删 DDL** |
| `strategy_genes` | `tellurion-agent`（`ai.strategy_genes`） | **无**（core W=0） | **无**（core R=0） | **有**：同上 `:24`（write-scope） | 同上 |
| `research.profile` | `tellurion-agent`（`ai.research_profile`） | **无**（core W=0） | core：`tellurion:pkg/storage/research.go:101`（R=1） | **无** | core **只读不写** ⇒ 切读即可停（改读 agent） |
| `research.conclusion` | `tellurion-agent`（`ai.research_conclusion`） | **无**（core W=0） | core：`tellurion:pkg/storage/research.go:135`（R=1） | **无** | 同上 |
| `research.question` | `tellurion-agent`（`ai.research_question`） | **无**（core W=0） | core：`tellurion:pkg/storage/research.go:168`（R=1） | **无** | 同上 |

> 复用基线（agent 侧现有 5 件，`cmd/recon` 未重复发明）：
> `tellurion-agent:pkg/aistore/schema_core_drift_test.go:117`（列集漂移）、
> `tellurion-agent:pkg/ai/gene_pool/sql_scope_test.go:19`（静态 schema 限定）、
> `tellurion-agent:internal/repoguard/storage_import_test.go:128`（禁 import core storage）。

## 5. risk 簇（0 行表级 · 裁决权单列）

> **本节刻意不是同表双写**：risk 的冲突是「同一**裁决权**两处各有一份实现」，不是「两张表都写同一事实」。
> `risk.audit_logs` 与 `public.audit_logs` **不是同一张表**（`tellurion-risk:docs/db-schema.md:23`）⇒ 不计入双 owner。

| 事实 | owner | 当前写者/实现方 | 读方 | 对拍方式 | 停写条件 |
|---|---|---|---|---|---|
| **限额/阈值真相** | `tellurion-risk`（`risk.limits`，`tellurion-risk:internal/rule/ddl.go:65`） | **core 是今天唯一真相**：`tellurion:config/analysis-service.yaml:187-198`（`alert.*`）+ `:155-160`（`risk_manager.*`）；`risk.limits` **无生产读写方**（`tellurion-risk:internal/rule/ddl.go:63-64` 注释「本切片只建表」） | core 自身（`pkg/risk/manager.go:13-28` 的 `RiskManagerConfig` 14 个标量字段） | `无`；机械判据 = `git grep -n 'v.GetFloat64("alert.max' -- '*.go'` 今天非零 | 把 `RiskManagerConfig` 的构造源切到 risk 服务返回的 policy，**该 grep 归零**；且 `resolve_enabled` 从 `false`（`tellurion:config/analysis-service.yaml:122`）改为按策略表放行 |
| **裁决实现** | 已裁 U7：检测器留 core、通道编排归 risk | core：`tellurion:pkg/risk/manager.go:13-28` + `tellurion:pkg/alert/detectors.go`（6 个检测器） | — | `无` | 不适用（已裁的边界，不是待停写项） |
| **告警投递** | `tellurion-risk`（`risk.risk_events`，`tellurion-risk:internal/rule/ddl.go`） | core：`tellurion:pkg/storage/alert_outbox.go:53`（`quant.alert_outbox`）+ `tellurion:cmd/analysis/setup.go`（`buildAlertSystem`，`POST /api/risk/events`） | core：`tellurion:pkg/storage/alert_outbox.go`（投递腿） | **已接的一条腿**（QT-42 X3c），不是双 owner | 不适用（已收口） |
| **审计表** | 各自独立：`public.audit_logs` 归 core、`risk.audit_logs` 归 risk | core：`tellurion:pkg/auth/auth.go:505`；risk：`tellurion-risk:internal/rule/ddl.go` | 各自 | 不适用 | 不适用（**不是同一张表**） |

## 6. core-only（23 行，无服务仓对手方）

> 判据：**没有对手方就不存在「双 owner」**，但清单必须逐行写明「谁写/谁读」，否则「不知道谁写」的表会藏在总数里。
> 对拍方式与停写条件对整节统一为 `不适用（core 自有）`。

| 表 | owner | 当前写者 | 读方 | 备注 |
|---|---|---|---|---|
| `stock_fundamentals` | core | `tellurion:pkg/storage/fundamentals.go:212`（W=6） | `tellurion:pkg/storage/fundamentals.go:68`（R=10） | data 列为 **C 集豁免**（生成列 `available_date`，`tellurion-data:internal/schema/visible_at.go:53`） |
| `fundamentals_detail` | core | `tellurion:pkg/storage/fundamentals_detail.go:46`（W=1） | `tellurion:pkg/storage/fundamentals_detail.go:106`（R=1） | — |
| `factor_cache` | core | `tellurion:pkg/storage/cache.go:37`（W=1） | `tellurion:pkg/storage/cache.go:75`（R=2） | 因子中间态 |
| `factor_returns` | core | `tellurion:pkg/storage/cache.go:134`（W=1） | `tellurion:pkg/storage/cache.go:168`（R=1） | 分析产物 |
| `ic_analysis` | core | `tellurion:pkg/storage/cache.go:207`（W=1） | `tellurion:pkg/storage/cache.go:241`（R=1） | 分析产物 |
| `backtest_adjustment_anchors` | core | `tellurion:pkg/storage/adjustment_anchors.go:84`（W=1） | `tellurion:pkg/storage/adjustment_anchors.go:35`（R=1） | 材料 §2.3.2 同名列，此处唯一计行 |
| `users` | core | `tellurion:pkg/auth/auth.go:215`、`:331`（W=3） | `tellurion:pkg/auth/auth.go:180`、`:248`（R=5） | 鉴权域，不进任何服务仓 |
| `audit_logs` | core | `tellurion:pkg/auth/auth.go:505`（W=1） | `tellurion:pkg/auth/auth.go:521`（R=1） | ≠ `risk.audit_logs` |
| `data_migrations` | core | `tellurion:pkg/storage/postgres.go:546`、`:584`（W=2） | `tellurion:pkg/storage/postgres.go:542`、`:576`（R=2） | 迁移留痕，**退役时最后处理** |
| `factor_definitions` | core | `tellurion:pkg/storage/factor_definition.go:29`（W=1） | `tellurion:pkg/storage/factor_definition.go:74`（R=1） | 表达式字段目录 |
| `factor_registrations` | core | `tellurion:pkg/storage/factor_registration.go:43`、`:79`（W=2） | `tellurion:pkg/storage/factor_registration.go:122`（R=1） | — |
| `factor_registration_audit` | core | `tellurion:pkg/storage/factor_registration.go:103`（W=1） | `tellurion:pkg/storage/factor_registration.go:163`（R=1） | — |
| `factor_mining_records` | core | `tellurion:pkg/storage/mining.go:33`（W=1） | `tellurion:pkg/storage/mining.go:69`（R=1） | — |
| `audit.message_log` | core | `tellurion:pkg/eventstore/interfaces.go:109`（W=1） | `tellurion:pkg/eventstore/interfaces.go:120`、`:130`（R=3） | 事件溯源留痕 |
| `quant.strategy_state` | core | `tellurion:pkg/strategy/state_store.go:142`（W=1） | `tellurion:pkg/strategy/state_store.go:158`、`:164`（R=2） | `tellurion-strategy:docs/db-schema.md:22` 明说不属 strategy |
| `quant.external_signals` | core | `tellurion:pkg/strategy/signal_store.go:81`（W=1） | `tellurion:pkg/strategy/signal_store.go:89`（R=1） | 同上 |
| `quant.sw_industry_tree` | core | `tellurion:pkg/storage/supply_chain.go:68`（W=1） | `tellurion:pkg/storage/supply_chain.go:267`（R=1） | — |
| `quant.supply_chain_edge` | core | `tellurion:pkg/storage/supply_chain.go:81`（W=1） | `tellurion:pkg/storage/supply_chain.go:309`（R=1） | — |
| `quant.industry_vitality` | core | `tellurion:pkg/storage/supply_chain.go:92`（W=1） | `tellurion:pkg/storage/supply_chain.go:359`（R=1） | — |
| `quant.etf_constituent` | core | `tellurion:pkg/storage/supply_chain.go:103`（W=1） | `tellurion:pkg/storage/supply_chain.go:403`、`:405`（R=3） | — |
| `quant.macro_series` | core | `tellurion:pkg/storage/macro.go:56`（W=1） | `tellurion:pkg/storage/macro.go:107`（R=1） | data 侧同名是 **B 集锚点**（`tellurion-data:internal/schema/visible_at.go:43`），**写者仍是 core** |
| `quant.alert_outbox` | core | `tellurion:pkg/storage/alert_outbox.go:101`、`:164`（W=3） | `tellurion:pkg/storage/alert_outbox.go:126`、`:236`（R=2） | risk 域发件箱，**不是**双 owner |
| `quant.run_provenance` | core | `tellurion:pkg/storage/run_provenance.go:99`（W=1） | `tellurion:pkg/storage/run_provenance.go:131`（R=1） | orchestration 侧无同名表 |

## 7. market 簇（22 行）：已裁，不在本单重开

裁决 ⑰（2026-10-25）已定 `market.index_daily` 的 owner = core，K2 批次在做；`SVC-DUAL-01` ② 明确「本单不重开」。
为避免「总数对不上」，这里只留 22 个表名的 roster 与指针，**五列清单以 K2 的产物为准**：

`stocks` · `ohlcv_daily_qfq` · `trading_calendar` · `dividends` · `index_constituents` · `splits` ·
`realtime_quote` · `ohlcv_minute` · `capital_flow` · `sectors` · `stock_sector_map` · `top_list` ·
`limit_up_pool` · `announcements` · `news` · `hot_search` · `global_ohlcv` · `market_activity` ·
`market.index_daily` · `ingest.raw` · `sync_jobs` · `sync_schedules`

- 22 行的逐行写点/读点/对拍/停写条件在材料 §2.3.1（`core-governance-decision-materials-2026-10-26.md:211-236`），**本文件不镜像**。
- 今天已切的两条（如实）：`capital_flow`（core 改投 `POST /ingest/points`，`tellurion:cmd/data/capital_flow_executor.go`）、`top_list`（`tellurion:cmd/data/top_list_executor.go`）。
- `market_activity` 是**唯一反向**的一条：data 显式拒写（`tellurion-data:internal/store/write.go:239`）⇒ 停写条件不是「data 接管」而是「data 解除拒写」。

## 8. 已知边界（如实继承，不是遗漏）

- **动态拼表名不覆盖**（材料 §2.4 同一限制）：`realtime_quote` / `ohlcv_minute` / `capital_flow` / `top_list` /
  `limit_up_pool` / `announcements` / `news` / `hot_search` / `global_ohlcv` 这 9 张在 core **无字面 SQL**，
  写点来自 `BulkInsert` 拼表名 ⇒ 上表的 `W=0` 是「字面 0」，**不是「不写」**。停写验收必须以运行为准。
- 本单**不改** DDL、**不建**对拍工具（`cmd/recon` 归 QT-81，已完成代码侧）、**不排**四步先后（归各仓 `G3`）、**不动**生产库。
- 本单**不裁决** `market.index_daily` 的 DDL 形状冲突（材料 §2.5，已单独挂起）。
- **不覆盖** `quant.*` 里可能被服务仓动态读的表（今天无证据，不臆造一行）。

## 9. 自检（可复现）

| 判据 | 命令 | 期望 |
|---|---|---|
| 四簇都有行 | 本文件 §2/§3/§4/§5 各至少 1 行 | 是 |
| 无空 owner | `rg '\|\s*\|\s*$'` 于 owner 列 | 0 处 |
| 行数算术 | 22 + 2 + 1 + 7 + 23 | **55**，与材料 §2.2 的底数一致 |
| 抽样路径命中 | 随机抽 3 行 `repo:path:line` 用编辑器打开 | 3/3 命中 |
