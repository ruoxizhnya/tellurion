# 数据服务契约与数据归属（ADR-D19 草案）

> 状态：草案，未实现。承接 ADR-D18（服务拆分）。
> 用户补充：① 数据库归属各自服务（可同一实例，但每服务有自己的 db，遵循 DDD）② 数据服务供给回测的数据遵循**统一内部数据协议**③ 数据服务可由多个服务聚合，但要有 **aggregator** 对外提供数据 ④ 数据服务群有多个不同数据源的 **connector**，每个 connector 归一化后入库。

---

## 0. 先说一个重要事实：这套设计你们已经做了 80%

2026-10-19 源码实测，你要的东西在 `pkg/data/source/` 里已存在：

| 你要的 | 已有实现 | 位置 |
|---|---|---|
| **connector 契约** | `ConnectorDeclaration`（注册声明）+ `Registry`（fail-closed 收口） | `contract.go` / `registry.go` |
| **多个数据源 connector** | 12 个：tushare（含 daily_basic / macro / moneyflow）、eastmoney（含 sectors）、juchao、mootdx、xueqiu、yahoo_finance、alpha_vantage、hkex + 2 个 normalizer | `pkg/data/source/` |
| **归一化后入库** | `Normalizer func(DataItem, source, dataType) UnifiedDataPoint` + `ETLPipeline.Process(...)` + `ValidatePoints` + `Deduplicate` | `etl.go` / `unified.go` |
| **统一数据协议** | `UnifiedDataPoint`（含 `DeduplicateKey()` 幂等坐标）+ `domain/market`（OHLCV / FrequencySpec / TimeSemantics） | `unified.go` / `pkg/domain/market/` |
| **时间语义（防前视）** | 注册时**必须声明** `TimeSemantics` + `VisibleExpr` + `VisibleField`；漏声明 / 声明非法 **一律拒收** | `contract.go` 注释原文 |
| **多 schema 归属** | PG 已分 `ingest` / `market` / `quant` / `research` | `postgres.go` |

`contract.go` 的注释原文值得单独引用（它是这条设计的红线）：

> 漏声明的时间语义 = 数据泄漏，且回测曲线照样漂亮，事后极难发现。

⇒ **你的设计方向完全正确，且与代码同构。** 本轮要做的不是"重新设计"，而是**把它冻结成跨服务契约**（进 `contracts` module），并把归属边界写死。

---

## 1. 数据库归属（DDD：数据归属唯一）

**同一个 PG 实例，每服务一个 schema（将来可平滑升为独立库），DDL 只有一个 owner。**

| 服务 | schema | 持有表（摘要） | DDL 归属 |
|---|---|---|---|
| **数据服务** | `market` + `ingest` | ohlcv / bars / symbols / 复权因子 / 公司行为 / fundamentals / macro / sector / supply_chain / factor_definitions / factor_values / data_sync_state | ✅ 唯一 |
| **策略服务** | `strategy` | strategy_definitions / strategy_artifacts / strategy_versions | ✅ |
| **风控服务** | `risk` | risk_rules / risk_limits / risk_events / 熔断开关 / compliance 报送 / alerts | ✅ |
| **分析服务** | `analysis` | 报表 / 基准 / 归因结果 / 敏感性扫描 | ✅ |
| **实验（agent）** | `ai` | experiments / research_* / factor_genes / strategy_genes | ✅（ADR-D10） |
| **编排服务** | `run` | backtest_jobs（~~strategy_state（断点）~~ ⚠️ 2026-10-21 H5 裁决不归 `run`，见下） | ✅ |
| **内核** | `run`（结果面） | 回测结果表 `backtest_results`（`BacktestResult` / `Trade` 等） | ✅ **owner = core（2026-10-21 裁决，单一写者；orchestration 只经 API 读结果）**；其余数据只读写通过 port |
| **内核（回测域）** | 无 schema 前缀（`public`） | `backtest_adjustment_anchors`（回测复权锚点） | ✅ **owner = core 回测域（2026-10-21 裁决）**；写者 `tellurion:pkg/backtest/adjustment_snapshot.go` |
| **数据服务（因子域）** | 无 schema 前缀（`public`，现状） | `factor_hypothesis` / `factor_returns` / `ic_analysis` | ✅ **owner = tellurion-data（SPEC-R-02）**；DDL 文本在 core `pkg/storage/postgres.go`，写者 `tellurion:pkg/storage/factor_hypothesis.go` 与 `cache.go` |
| **内核（strategy-runtime）** | `quant` | `quant.strategy_state` / `quant.external_signals` | ✅ **owner = core strategy-runtime 模块（H5 + SPEC-R-02）**；DDL 文本在 core `pkg/storage/postgres.go`（Migration 055 / 048），模块侧 `EnsureSchema` 仅幂等兜底 |
| **内核（鉴权域）** | 无 schema 前缀（`public`） | `users`（含会话/角色） | ✅ **owner = core（ADR-017 鉴权域，SPEC-R-02）**；写者 `tellurion:pkg/auth/auth.go` |

> ⚠️ **2026-10-21 订正（H5 裁决，`tellurion:docs/design/system-spec-review-2026-10-21.md`）**：`strategy_state` 归 **strategy-runtime 模块**（K0 冻结契约 `contracts/kernel_modules.schema.sql`），core 继续写 `quant.strategy_state`——本文「`strategy_state` 归编排服务 / 迁 `run`」的旧口径**作废**（含上表编排行、下方第 3 条与风险表对应行）；`backtest_jobs` 迁 `run` 不受影响。

三条纪律（参考库 §七）：
1. **每张表只归一个服务写**，其他服务只读（走接口/视图，不直接摸表）；
2. 跨服务**禁止共写同一张表**——出现第二写者 = 边界已腐化；
3. 现状要修正的一点：~~`strategy_state`（断点续跑）与~~ `backtest_jobs` 目前在内核侧，**应归编排服务**（X5），内核通过 port 读写——这样"内核无持久化状态"才成立。**（⚠️ 2026-10-21 订正（H5 裁决）：`strategy_state` 不迁——归 strategy-runtime 模块，core 写 `quant.strategy_state`，"内核无持久化状态"对它是既定例外。）**

> 本地栈一个 PG 实例是**对的**（避免分布式事务、运维简单）。将来真要拆库，是"按 schema 搬走"，不是重写。

---

## 2. 统一内部数据协议（这是你这条补充的核心）

**协议 = `market.UnifiedDataPoint` + `domain/market` 语义层，冻结进 `tellurion-contracts`。**

### 2.1 协议必须含的七类要素

| # | 要素 | 说明 | 现状 |
|---|---|---|---|
| 1 | **标的标识** | symbol + 市场 + 交易所 + 内外部 code 映射 | 有（symbols） |
| 2 | **时间语义** | `as_of` / `trade_time` / **`visible_at`（可见时间）** | ✅ `TimeSemantics` + `VisibleField*` 保留锚点 |
| 3 | **频率语义** | `FrequencySpec`（1d / 1m / 15m 及聚合规则） | ✅ `domain/market/frequency.go` |
| 4 | **行情字段** | OHLC + Volume（**单位 = 股**）+ turnover + 复权因子 | ✅ `market.OHLCV` |
| 5 | **公司行为** | 分红 / 送股 / 配股（复权口径） | 部分（BE-11 待深查） |
| 6 | **质量标记** | 缺失 / 停牌 / ST / 退市 / 异常值 | `ValidatePoints` 雏形 |
| 7 | **内容寻址** | `dataset_snapshot_hash`（票池 + 窗口 + 频率 + 数据版本） | ❌ **待补**（X0） |

### 2.2 最关键的一条：`visible_at` 是防前视的第二道防线

回测里有两道防前视：

| 道 | 位置 | 机制 |
|---|---|---|
| 第一道 | 策略沙箱 | `get_bar(t_offset>0)` → trap（ADR-029） |
| **第二道** | **数据协议** | 任何数据带 `visible_at`，aggregator 只返回 `visible_at <= current_bar_time` 的数据 |

第二道比第一道更根本：**它防的是"数据本身泄漏"**（比如财报用报告期截止日当锚点、或用后复权价格）。`contract.go` 里 `VisibleFieldAnnDate` 的注释已经点明了这个坑——财报的可见时间锚点必须是**披露日**，而不是 `trade_time`（那是报告期截止日，拿它当锚点就把前视洞放回来了）。

⇒ **统一协议里 `visible_at` 必填，且必须由 connector 声明、由归一化层填实、由 aggregator 强制过滤。**

---

## 3. 架构分层：connector → 归一化 → 存储 → aggregator

```
[Tushare] [东财] [雪球/Yahoo] [巨潮/HKEX] ...   ← connector（12 个，可增）
        │  每个 connector：字段映射 + 声明 TimeSemantics/FrequencySpec
        ↓
[归一化 ETL]  Normalizer（共享函数）+ ValidatePoints + Deduplicate
        │  统一成 UnifiedDataPoint（含 visible_at、幂等坐标）
        ↓
[存储] market/ingest schema（真相源，DDL 唯一归属）
        │
        ↓
[aggregator]  ← 对外唯一面（数据服务的 API）
        │  职责：PIT 过滤 · 频率聚合 · 票池解析 · 快照 hash · 缓存
        ↓
[回测/实盘内核] [分析服务] [策略服务]
```

### 3.1 对你"connector 归一化后入库"的**一处精确化**（不推翻，只防坑）

你说"每个 connector 要归一化数据后放入数据库"——**方向对**，但要拆成两层责任，否则 8 个 connector 各写一套语义归一 = 口径漂移（你们代码里已经规避了）：

| 责任 | 归谁 | 理由 |
|---|---|---|
| **字段映射**（源字段 → 标准字段）+ 基础校验 | **connector 内**（`Adapter`） | 字段映射天然是源相关的 |
| **语义归一**（时间语义推导、频率聚合、复权口径、单位换算、去重） | **共享 Normalizer 函数库**（`ETLPipeline.Process(..., normalizer)`） | 必须集中，否则每个 connector 一套口径，事后极难发现 |

现有代码正是这个形态：`type Normalizer func(item DataItem, source, dataType string) UnifiedDataPoint` 是**可注入的函数**，connector 不自己写归一化逻辑。

### 3.2 aggregator 的职责（不只是转发）

| 职责 | 说明 |
|---|---|
| **PIT 过滤** | 只返回 `visible_at <= 请求时点` 的数据（第二道防前视） |
| **频率聚合** | 按 `FrequencySpec` 从 1m 聚到 1d/15m，口径统一 |
| **票池解析** | `StockPool` / `IndexCode` → 标的列表（含成分变更） |
| **快照 hash** | 生成 `dataset_snapshot_hash`，供结果留痕 |
| **缓存** | 按 hash 缓存数据集副本，供引擎 run 前拉取 |

`marketdata.Router`（D1 规范层唯一入口）**就是 aggregator 的雏形**——拆分后它成为数据服务的对外面，引擎侧只认接口。

---

## 4. 跨服务契约面（进 `contracts` module）

| 契约 | 内容 |
|---|---|
| **数据协议类型** | `UnifiedDataPoint` / `OHLCV` / `FrequencySpec` / `TimeSemantics` / `VisibleField*` 保留锚点 |
| **connector 契约** | `ConnectorDeclaration`（TimeSemantics + FrequencySpec 必填）· `Source` 接口 · fail-closed 语义 |
| **aggregator 接口** | `GET /datasets/resolve?universe=&from=&to=&freq=` → `{dataset_hash, metadata}` · `GET /datasets/{hash}` → 数据 · `GET /factors/{name}@{hash}` |
| **错误码** | `ErrNoTimeSemantics` / `ErrUnknownDataType` / `ErrVisibleAtMissing` / `ErrSnapshotMismatch` |

**顺序纪律**：先有契约（`contracts`），后有服务实现——与 ADR-D7 一致。

---

## 5. 分期（对 X1 的细化）

| 阶段 | 内容 | 验收 |
|---|---|---|
| **X1a** | 数据协议冻结进 `contracts`（类型 + TimeSemantics 枚举 + 错误码） | core / agent 都只依赖 contracts |
| **X1b** | aggregator 面实现（PIT 过滤 + 频率聚合 + 票池解析 + `dataset_snapshot_hash`） | 同一 hash 的两次拉取返回一致 |
| **X1c** | connector 契约收口（12 个 connector 全部走 `ConnectorDeclaration` + 共享 Normalizer） | 漏声明时间语义的 connector 被拒收（已有测试） |
| **X1d** | schema 归属迁移（`market`/`ingest` 归数据服务，DDL 唯一） | 跨服务只读走接口，无第二写者 |
| **X0** | `dataset_snapshot_hash` 记入回测结果 | 与 `strategy_ref` hash 同款留痕 |

---

## 6. 风险

| 风险 | 兜底 |
|---|---|
| 各 connector 语义归一口径漂移 | 归一化收口到共享 Normalizer；`ConnectorDeclaration` fail-closed |
| `visible_at` 被漏填导致数据泄漏 | 协议必填 + `ErrVisibleAtMissing` 拒收 + aggregator 强制过滤 |
| 同一 schema 出现第二写者 | 护栏：扫描跨服务 schema 写语句，命中即红 |
| ~~`strategy_state` /~~ `backtest_jobs` 留在内核导致"内核有状态" | ~~迁到编排服务（X5），内核走 port~~ ⚠️ 2026-10-21 H5 裁决：`strategy_state` 不迁（归 strategy-runtime 模块，core 写 `quant.strategy_state`）；`backtest_jobs` 迁 `run` 不变 |
