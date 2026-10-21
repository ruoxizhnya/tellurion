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
| 7 | **内容寻址** | `dataset_snapshot_hash` | ✅ **已冻结（QT-03，2026-10-21）** |

> **`dataset_snapshot_hash` 的唯一定义在代码**：`pkg/backtest/contracts/provenance.go` 的
> `DatasetSnapshotInput`（六分量：`universe_hash` / `window` / `frequency` /
> `data_version` / `calendar_hash` / `adjustment_basis`）。
> **本文件原先写的四项（票池 + 窗口 + 频率 + 数据版本）缺 `calendar_hash` 与
> `adjustment_basis`，已订正为引用**（2026-10-21，QT-03 / N6-2）——缺日历那份会导致
> 「供应商补了一个节假日 → 同 hash 不同结果」，正是 A4/B1 反复要防的事。
> 除该结构体之外，任何文档都不再重述这六个分量。

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
| **错误码** | 见 §4.1 E 组（**已逐个对上真实哨兵**）：注册期三条 `ErrDeclarationMissing` / `ErrDeclarationInvalid` / `ErrDeclarationMismatch` 已在码；X1b 待新建 `ErrVisibleAtMissing` / `ErrSnapshotMismatch`；`ErrNoTimeSemantics` 作废（= 第一条的旧名），`ErrUnknownDataType` 不另造（复用 `ErrUnsupported`） |

**顺序纪律**：先有契约（`contracts`），后有服务实现——与 ADR-D7 一致。

### 4.1 接口对表（M8 硬前置，✅ 2026-10-21 对完）

> **这张表是 QT-40 的施工图纸**：逐个 port / endpoint 列 **签名 + 错误语义 + 版本策略**，缺一列就是没对完表。
> 各表的列口径统一三件：**今天** = 源码实测（`file:line`，可复核，不是文档转述）；**契约位** = S1（QT-05）之后 contracts 仓的落点；**版本策略 / 幂等** = 变更时走 MAJOR / MINOR 还是必须走 ADR。C 组多一列**目标签名**——那一列是 X1b 要实现的形状，今天不存在。
> 本节即 M8 要求的「接口对表」**本体**，同时是 QT-41~45 各仓补对表时的**模板**（core `tellurion:docs/TASKS.md` X 线公共前置 ② 已把模板指到本节）。`tellurion-data:docs/TASKS.md` 原把它指向 `target-module-topology.md` 的「UnifiedDataPoint 节」——该文件 grep `UnifiedDataPoint` **零命中**（幻影引用），已订正。

**三条前置事实（先说，否则会照「现状已实现约 80%」误开 X1b）**：

| # | 事实 | 实测取证方式 | 对施工的后果 |
|---|---|---|---|
| 1 | **`DatasetSnapshotInput` 今天零生产者** | 全仓 grep `DatasetSnapshotInput{` / `DataVersion` 只命中定义处自身（`pkg/backtest/contracts/provenance.go:97-123`）与向量测试 | QT-40 ③-④ 交付的是**第一个生产者**，不是搬迁。X1a「协议冻结」之后，X1b 是真正的新增实现 |
| 2 | **api-schema §2 那四个错误码在代码里一个都不存在** | `grep -rn "ErrNoTimeSemantics\|ErrUnknownDataType\|ErrVisibleAtMissing\|ErrSnapshotMismatch" --include=*.go pkg cmd` = 0 命中 | 它们是**待新建**；今天真正在拦东西的是 E 组那 **7 个**哨兵（`ErrDeclaration*` ×3 + `ErrUnsupported` / `ErrRateLimited` / `ErrUpstreamUnavailable` / `ErrAdapterNotRegistered`）。`tellurion-data:docs/api-schema.md` 是目标契约，不是现状 |
| 3 | **协议类型今天分住两处，且第二处不在 S1 搬迁清单里** | `domain/market/`（5 个非测试文件）随 QT-05 整包迁（`tellurion-contracts:docs/API.md` §6 实测 227 符号「含 `market/` 子包」）；`pkg/data/source/`（`UnifiedDataPoint` / `ConnectorDeclaration` / `DataItem` / `DataSchema` / `FetchRequest`）**不在** QT-05 ③-① 的 `domain/`+`engine/`+`fees/` 名单里 | X1a 必须单独定 A 组后半的落点与**迁入时机**（见 A 组注 ②），否则要么把协议写死在服务里（跳序代价已被 `tellurion-data:docs/TASKS.md` 施工序 §注 点过一次），要么提前塞进 contracts 撞 ADR-0001 ③ |

#### A 组 · 值对象与协议类型

| 符号 | 今天（签名） | 契约位（S1 后） | 错误语义 | 版本策略 |
|---|---|---|---|---|
| `market.TimeSemantics` | `type TimeSemantics string`，`Valid()` / `RequiresExpression()`（只 `VisibleCustom` 需表达式）/ `KnownTimeSemantics()`（`pkg/domain/market/visibility.go:61-118`） | `contracts/domain/market`（包名 `market`） | 非法枚举 ⇒ `ErrDeclarationInvalid`（注册期拒收，见 B 组） | **frozen**：取值集就是契约。加一个语义 = MINOR；改已有语义的 PIT 含义 = breaking，须先让 `pkg/data/source/registry_rev05_test.go` 那批逐源钉子红 |
| `market.FrequencySpec` / `market.Frequency` | `Valid()` / `KnownFrequencySpecs()` / `IsIntraday()` / `IntradayMinutes()`（`pkg/domain/market/frequency.go:11-127`） | 同上 | 非法 ⇒ `ErrDeclarationInvalid` | **frozen**（同上）。聚合口径变更走 ADR，不走补丁 |
| `market.OHLCV` | 实体在 `pkg/domain/market/types.go:11`（`Date` = bar 周期起点，1d = 交易日零点 UTC）；`domain.OHLCV` 是**根包别名**（`pkg/domain/types.go:52`，钉子 `market_alias_test.go:18`） | 同上 | — | **stable**：加列 MINOR；`Volume` 单位 = **股**（`kernel-interface-map.md` §一.1）是语义冻结，改单位 = breaking |
| `source.ConnectorDeclaration` | `struct{DataType string; TimeSemantics market.TimeSemantics; VisibleExpr, VisibleField string; Frequency market.FrequencySpec}`（`pkg/data/source/contract.go:41-59`） | `contracts/dataprotocol`（**新建包**，注 ①） | 见 E 组三条 `ErrDeclaration*` | **frozen**：五个字段全部必填判定已写死（`validateDeclarations` 全文）。加字段 = MINOR，但**新字段必须可选**，否则下游 connector 集体编译期红 |
| `source.VisibleFieldTradeTime` / `VisibleFieldAnnDate` | 两个保留锚点记号 `const string`（`:24`、`:38`） | 同上 | 记号名写进 `VisibleField` 时走 `validateReservedAnchorProjection` | **frozen**：记号一旦改名的代价 = 全部 connector 声明 + 全部已落库口径说明同时失效 |
| `source.UnifiedDataPoint` | `struct{Symbol string; TradeTime time.Time; Source, DataType string; Data map[string]interface{}; IngestTime time.Time}` + `DeduplicateKey()` = `Symbol\|DataType\|TradeTime(RFC3339Nano,UTC)`（`pkg/data/source/unified.go:16-72`） | 同上（注 ②） | 缺 `Symbol`/`TradeTime`/`Data` ⇒ 被 `ValidatePoints` **静默丢弃并计数**（不是 error，`etl.go:168-186`） | **stable**：`Data` 是 JSONB 语义的逃生阀，加字段优先落 `Data` 不动结构体 |
| `source.DataItem` / `DataSchema` / `SchemaField` / `FetchRequest` / `FetchResponse` | `pkg/data/source/adapter.go:88-170`（`SchemaField.Type` 取值集 `"string"\|"float"\|"int"\|"date"\|"datetime"`，`Unit` 取值集 `"yuan"\|"percent"\|"share"\|""`） | 同上 | `ErrUnsupported`（类型不服务） | **stable**；`SchemaField.Type` 取值集扩容 = MINOR，**改名 = breaking**（`validateVisibleAnchor` 按字面量判 date/datetime） |

> **注 ①（落点定名）**：新包定 **`github.com/ruoxizhnya/tellurion-contracts/dataprotocol`**，包名 `dataprotocol`。不用 `data/`（与 contracts §8 待裁 #1 的 `errors/`、`abi/` 之争同型：包名要能自证「这是协议不是实现」）；不叫 `dataset`（那是 X1b 的 aggregator 面，形状还没定）。
> **注 ②（迁入时机，这次对账要拍的那一颗）**：`dataprotocol` 五个符号**今天只有 core 一个使用方**，按 `tellurion-contracts:docs/adr/0001-契约只增不破且零实现.md` ③「只有单个仓使用的类型属泄漏」**不许提前迁**。执行序：X1a 只在 contracts 落 **`domain/market`**（它本来就随 QT-05 整包迁）+ 本节的错误码族；`dataprotocol` 那一笔**挂在 X1b 的第一条 commit**——`tellurion-data` 一 import，两个使用方立刻成立，泄漏判据同时满足。
> **注 ③（顺手要消灭的第三份真相）**：`pkg/storage/bulk_insert.go:17` 有一个**同名同字段的副本** `storage.UnifiedDataPoint`，注释自述理由是「storage 不能 import source（会成环）」。协议进 contracts 后这个理由消失（两者都 import contracts），**必须删副本、改引用**——留在原地就是三份 `DeduplicateKey` 语义，而幂等坐标正是 X1c/X1d 全部拒收判据的地基。

#### B 组 · connector 契约（port，注册期闸门）

| 成员 | 签名（今天） | 错误语义 | 版本策略 |
|---|---|---|---|
| `DataSourceAdapter` | `Name() string` · `Type() AdapterType` · `Enabled() bool` · `SupportedTypes() []string` · `Declarations() []ConnectorDeclaration` · `Schema(dataType string) (DataSchema, error)` · `Fetch(ctx, FetchRequest) (*FetchResponse, error)` · `HealthCheck(ctx) error` · `RateLimit() RateLimitConfig`（`pkg/data/source/adapter.go:172-220`，**9 个方法**） | `Fetch` 契约（REV-05）：`err == nil` 时返回**必须非 nil**，`(nil, nil)` 视为 adapter 失败并落备用链；限流 ⇒ `ErrRateLimited`；传输故障 ⇒ `ErrUpstreamUnavailable` | 方法集是**接口即闸门**：`Declarations()` 已在接口上，「忘声明」对新 connector 是编译错误（原文注释 `:188-194`）。增删方法 = breaking，走 MINOR→MAJOR |
| `Registry.Register` | `func (r *Registry) Register(a DataSourceAdapter) error`（`registry.go:71`） | fail-closed：缺声明 ⇒ `ErrDeclarationMissing`；枚举非法 / `VisibleCustom` 缺 `VisibleExpr` / 重复声明 / 声明了 `SupportedTypes()` 之外的类型 ⇒ `ErrDeclarationInvalid`；锚点为空 / 锚点不在 schema / 锚点类型非 date|datetime / 保留锚点投影类型非法 ⇒ `ErrDeclarationMismatch` | 校验覆盖面只增不减（放宽 = breaking） |
| `Registry.Fetch` | `func (r *Registry) Fetch(ctx, req FetchRequest) (*FetchResponse, error)`（`:150`） | 链上全员失败 ⇒ 聚合错误；未登记名 ⇒ `ErrAdapterNotRegistered` | 备用链语义（优先级取第一）稳定；`Source` 列口径变更走 ADR |
| `ETLPipeline.Process` | `func (p *ETLPipeline) Process(ctx, req FetchRequest, normalizer Normalizer) (*ProcessResult, error)`（`etl.go:95`）；`Normalizer = func(item DataItem, source, dataType string) UnifiedDataPoint`（`:161`，返回 `Data==nil` 即「丢弃该条」） | `nil Registry` / `nil normalizer` ⇒ 直接 error，不降级 | **ProcessResult 的计数语义是审计口径**（`Fetched`/`Persisted`/`Skipped`），改判定 = breaking |

> **责任分界（§3.1 那条精确化的可执行版）**：字段映射 + 基础校验在 **connector 内**；语义归一（时间语义推导、频率聚合、复权口径、单位换算、去重）在 **共享 Normalizer**。`Normalizer` 是**注入的函数**，所以 X1c「12 connector 全走 Declaration + 共享 Normalizer」的判据必须是**静态的**：任何 connector 包内出现 `TradeTime =` / 时区折算 / 单位乘法即违规——这条钉子属 QT-40 ⑤ 的「漏声明被拒收」同级，光靠运行时测试抓不到「口径各写一套」。

#### C 组 · aggregator 对外 HTTP 面（数据服务唯一入口）

> 正式 schema 落 core `docs/openapi.yaml`（真相源在 core，`repo-inventory.md` §3；跨进程 wire 面按 QT-50 ③-③ 统一入表，**本仓不复制 openapi**，`tellurion-ui:AGENTS.md` §3 同条禁令）。今天 openapi 里**没有** `/datasets/*`——补进表里属 X1b 交付物，见 G 组待办 T3。

| 端点 | 目标签名 | 错误语义（HTTP ↔ 哨兵） | 幂等 / 版本策略 |
|---|---|---|---|
| `GET /datasets/resolve?universe=&index_code=&from=&to=&freq=&adjustment=&as_of=` | ⇒ `{dataset_hash, metadata}`，`metadata` 至少含六分量回显（`universe_hash`/`window`/`frequency`/`data_version`/`calendar_hash`/`adjustment_basis`） | 缺 `from`/`to`/`freq` ⇒ **400 参数缺失**（不是 `ErrUnsupported`——那条判的是「类型没登记」，两义会毁掉分诊）；`freq` 非法 ⇒ 400（`Frequency.Valid()`）；`universe` 与 `index_code` 同时给 ⇒ 400（口径二义，不许静默优先级）；票池解析为空 ⇒ **409 明确失败**，不返回空数据集（`kernel-interface-map.md` §一.1 的「缺数据 symbol 直接缺席，不补零值」在 HTTP 侧的同款） | **可重放**（只读）。`dataset_hash` 由 `contracts/engine` 的 `DatasetSnapshotInput.Hash()` 产出，**本端点不许自己实现 hash**（唯一定义处 = §2.1 注）。新增查询参数 = MINOR；**改任一参与 hash 的参数的语义 = breaking**（历史 hash 全部失去可比性） |
| `GET /datasets/{hash}` | ⇒ 数据集（按 `map[string][]OHLCV` 形态，run 前一次物化） | hash 不存在 ⇒ 404 + `ErrSnapshotMismatch` 家族区分开：「没这个 hash」≠「内容不符」；返回内容重算 hash ≠ 路径 hash ⇒ **500 + 告警**（`ErrSnapshotMismatch`，缓存串号） | **强幂等**：同一 hash 两次拉取**字节一致**（QT-40 ⑤ 第一条验收）。内容变 ⇒ 必然是新 hash，**原地改内容是绝对禁止项** |
| `GET /factors/{name}@{hash}` | ⇒ 已落库的因子**值**序列 | 无名 ⇒ 404；有定义无值 ⇒ **明确失败**（不许补零）。样板 = `ErrNoFundamentalsDetail`（定义 `pkg/data/factor_equitydeep.go:46`，语义「查到了定义但没有行」⇒ 跳过并上报，见 `pkg/data/factor.go:454`） | 只读值。**因子求值不在本面**（§7「不做因子服务」）——见 G 组界线 L1 |
| —（不提供） | **逐 bar / 逐笔端点一律不做** | — | `tellurion-data:docs/api-schema.md` §5 + QT-40 ④；违反即 §1 第 5 条硬闸门（10³~10⁶ 倍延迟 + 前视不可控） |

#### D 组 · 今天已有、但**不该**照原样映射成 HTTP 的细粒度面

> 这是 X1b 真正要动刀的地方。`cmd/data/main.go:117-192` 今天暴露的是**逐 symbol / 逐日期的 DB 代理**，正好是 ADR-027 §7「不把 data-service 做成通用 DB 网关」点名的形状。逐个给归位，别说「保留兼容」。

| 今天的端点 | 形状 | 归位（目标态） |
|---|---|---|
| `GET /ohlcv/:symbol`、`POST /api/v1/ohlcv/bulk` | 按 symbol 拉 K 线 | **并入 `/datasets/resolve` + `/datasets/{hash}`**（票池 = 单 symbol 也走同一面）。`bulk` 的「一次拉多 symbol」正是 Snapshot 的语义（`kernel-interface-map.md` §一.1 `BulkLoadOHLCV`） |
| `GET /stocks/:symbol`、`/stocks`、`/stocks/count`、`/market/index` | 元数据代理 | **保留**，但明确它是**票池解析的输入面**（`StockPool` / `IndexCode` → 标的列表），不是取数面 |
| `GET /fundamental/:symbol` | 按 symbol + 日期取基本面 | **PIT 侧必须走 resolve**（`visible_at = ann_date` 过滤是 aggregator 的职责，§2.2 第二道防线）；点查只能作管理/调试面，不许进回测路径 |
| `GET /index/:code/constituents` | 成分解析 | 保留（票池解析原料），但**成分变更时点**要进 resolve 的 `as_of` 语义，否则「今天的成分表」会被拿去跑三年前回测 = 幸存者偏差 |
| `GET /api/v1/trading/calendar`、`CheckCalendarExists` | 交易日历 | **保留为只读 port**，且它是 `calendar_hash` 的输入（QT-03 六分量之一）。core 侧对日历的只读取用见 `tellurion-agent:AGENTS.md` §3「只读 port」条 |
| `POST /sync/*`、`POST /api/sync/jobs`、`/jobs/:id/progress`(SSE)、`/workers` | 作业层 | **留在本仓**（写路径，冷）。红线照 `tellurion-data:docs/dataflow.md` §2：HTTP 只承载**单次有界**任务，无界批量一律走 `sync_jobs` → worker → executor → ETL（DATA-10/DATA-12） |
| `POST /api/ingest/raw`、`/api/ingest/equitydeep` | 原始归档 / snapshot 写入 | **留在本仓**（`ingest.raw` 归本仓，§1 归属表）。⚠️ 后者今天**零生产者**（core `docs/TASKS.md` DBT-16），别把它当已运行链路 |

#### E 组 · 错误语义（哨兵 ↔ 拒收位置 ↔ HTTP 映射）

| 契约名（目标） | 今天是否已存在 | 真实拦在哪 | 语义 |
|---|---|---|---|
| `ErrDeclarationMissing` | ✅ `contract.go:66` | `Registry.Register` | 「支持了却没声明」⇒ **拒注册**。`tellurion-data:api-schema.md` §2 写的 `ErrNoTimeSemantics` **是它的旧名，作废**（G 组 T1：改本仓文档，不留两个名字） |
| `ErrDeclarationInvalid` | ✅ `:67` | `Register` | 枚举非法 / `VisibleCustom` 缺 `VisibleExpr` / 重复 / 越界类型 ⇒ 拒注册 |
| `ErrDeclarationMismatch` | ✅ `:68` | `Register` | 声明与数据形态矛盾（锚点缺 / 不在 schema / 类型非 date\|datetime）⇒ 拒注册。**这条是「声明了但等于没声明」的唯一执法点** |
| `ErrUnknownDataType` | ❌ 待新建 | 现有近似物 = `ErrUnsupported`（`adapter.go:73`） | 建议：**复用 `ErrUnsupported`**，`ErrUnknownDataType` 只作请求侧（resolve 参数）的 400 语义；造第二个同义哨兵会让 `errors.Is` 分诊表两义 |
| `ErrVisibleAtMissing` | ❌ 待新建 | 今天**没有**这一层：`ValidatePoints` 只判 `Symbol`/`TradeTime`/`Data` 非空（`etl.go:168`），**不判可见时间** | X1b 的新增执法点，位置在归一化之后、入库之前 ⇒ **拒收该批次**，`api-schema.md` §2 明写「不许用 mtime 兜底」。代价：现有已落库数据**没有一列承载 `visible_at`**，这条门一旦上线，历史行不可回填 ⇒ 必须与 §5 X1d 的 schema 迁移同批设计（加列 + 由 `TimeSemantics` 规则重算，不能猜） |
| `ErrSnapshotMismatch` | ❌ 待新建 | 无（`/datasets/{hash}` 还不存在） | `/datasets/{hash}` 重算 hash 不符 ⇒ 500 + 告警（缓存串号 / 内容被改） |
| `ErrUnsupported` / `ErrRateLimited` / `ErrUpstreamUnavailable` / `ErrAdapterNotRegistered` | ✅ `adapter.go:73-82`、`registry.go:21` | connector / Registry | 传输层语义，**不进 aggregator 对外面**（对外只露数据集，不露上游健康态；`HealthCheck` 是内部面） |

> 一条统一纪律（contracts `docs/API.md` §3 同型）：**哨兵只增不删、不改语义**；`errors.Is` 分诊表进本仓 `docs/api-schema.md` §2，且**必须点名执法函数**——只写「触发条件」不写「谁在哪一步拒」，实现就会把门放到错误的那一层（`ErrVisibleAtMissing` 今天就是这样一条悬空门）。

#### F 组 · 版本策略（三条一起写，缺一必漂）

| 面 | 规则 | 判据出处 |
|---|---|---|
| **contracts 符号** | 只认**版本号**（tag 或伪版本），不认源码路径。删/改签名或语义 = MAJOR；新增兼容能力 = MINOR；文档/无行为修正 = PATCH。0.x 期允许 breaking 但须 bump MINOR + CHANGELOG | `tellurion:docs/TASKS.md` QT-19 ③；落文档 = `tellurion-contracts:docs/TASKS.md` CT-02（⬜ 未完成，**X1a 之前必须先有**，否则第一个符号进来就没有版本纪律） |
| **HTTP 端点** | 只增不删；废弃走 `deprecated` 响应头 + 一个 MINOR 周期；**字段语义变更（尤其 `visible_at`）算 breaking，必须走 ADR** | `tellurion-data:docs/api-schema.md` §4（本节把它升格为判据，不再只是一句声明） |
| **canonical hash 分量** | `DatasetSnapshotInput` 的编码顺序在 `Hash()` 里逐行写死；**改集合或顺序 = 升域标签 v1 → v2，代价是全部历史 hash 作废** | QT-03 ④ + `pkg/backtest/contracts/provenance.go:127-136` 的「写这个结构体的规矩」 |

#### G 组 · 三条界线与待办

**界线（本次对账定死，不再各自解释）**

| # | 界线 | 定案 |
|---|---|---|
| L1 | **因子定义 vs 因子值 vs 因子求值** | 定义 + SeriesSpec ⇒ **strategy**（ADR-027 §2.6.1 / ADR-028 §6，见 `repo-inventory.md` §1.2 R3）；**已落库的值** ⇒ data，经 `GET /factors/{name}@{hash}`；**求值 ⇒ 消费点 in-process**（§7「不做因子服务」）。data 侧任何包不许出现「算因子」的入口 |
| L2 | **`visible_at` 的锚点谁填** | connector **声明**（`ConnectorDeclaration.VisibleField`），归一化层**填实**，aggregator **强制过滤**（`visible_at <= as_of`）。三道都在数据服务内，core 只传 `as_of`——core 侧不许二次推导可见性（那是第二道防线的定义权外溢） |
| L3 | **`adjustment_basis` 谁给** | **请求方（core）在 resolve 时把复权口径 + 锚点摘要作为参数传入**，data 只按参数取数并回算 hash。反例（禁止）：data 读 `backtest_adjustment_anchors` —— 该表 owner = core 回测域、写者 `pkg/backtest/adjustment_snapshot.go`（§1 归属表），data 去读就是跨 owner 摸表 + hash 口径两处各自算一遍 |

**待办（QT-40 开工前必须闭环的登记项）**

| # | 事项 | 落哪 |
|---|---|---|
| T1 | `tellurion-data:docs/api-schema.md` §2 的 `ErrNoTimeSemantics` 改指 `ErrDeclarationMissing`、`ErrUnknownDataType` 改复用 `ErrUnsupported`；§3 的 hash 四分量（票池+窗口+频率+数据版本）**订正为引用六分量**（缺 `calendar_hash` / `adjustment_basis`，与本文 §2.1 那次订正同源）。⇒ **✅ 已随本次对表执行**（data 仓 `api-schema.md` §1/§2/§3/§5 + `dataflow.md` §3，2026-10-21） | data 仓文档 |
| T2 | `dataprotocol` 包是否独立成包（与 contracts §8 待裁 #1「`errors/` / `abi/` 是否独立成包」同族问题）在 contracts `docs/API.md` §8 补一行登记 | contracts 仓，X1b 迁入前 |
| T3 | `/datasets/resolve` / `/datasets/{hash}` / `/factors/{name}@{hash}` 的正式 schema 进 core `docs/openapi.yaml`（QT-50 ③-③ 的统一入表）。⚠️ 实测（2026-10-21）：openapi 里**没有任何 `/datasets/*`**，但**已有 `/api/factors/{factor_name}`**（`docs/openapi.yaml:3103`）——与本仓目标的 `/factors/{name}@{hash}` **同族不同形**（一个按名取最新、一个按名+hash 取快照）。入表时必须定谁是真相，否则留下两个因子读面 = 因子值的两个口径，正是 §4.1 L1 要防的那种漂移 | core，X1b |
| T4 | `tellurion-data:docs/ARCHITECTURE.md` 补 ADR-027 §2.6.2 的**六个限界上下文包级隔离**分层（`repo-inventory.md` §1.2 data 行的义务） | data 仓，X1b |
| T5 | `ErrVisibleAtMissing` 上线与历史行不可回填的矛盾（E 组）要有一条迁移方案：加列 + 按 `TimeSemantics` 重算 + 重算不了的处理 | data 仓，X1b/X1d 设计 |


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
| `visible_at` 被漏填导致数据泄漏 | 协议必填 + `ErrVisibleAtMissing` 拒收 + aggregator 强制过滤。⚠️ 该哨兵**今天不存在**（待新建），且历史行没有承载列 ⇒ 这道门必须与 X1d 的加列 + 按 `TimeSemantics` 重算同批上（见 §4.1 E 组与 T5），否则「门写好了但一上线就把历史数据全判违规」 |
| 同一 schema 出现第二写者 | 护栏：扫描跨服务 schema 写语句，命中即红 |
| ~~`strategy_state` /~~ `backtest_jobs` 留在内核导致"内核有状态" | ~~迁到编排服务（X5），内核走 port~~ ⚠️ 2026-10-21 H5 裁决：`strategy_state` 不迁（归 strategy-runtime 模块，core 写 `quant.strategy_state`）；`backtest_jobs` 迁 `run` 不变 |
