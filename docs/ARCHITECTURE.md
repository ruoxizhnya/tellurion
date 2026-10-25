---
status: evergreen
last-verified: 2026-10-23
verified-by: 文档拆仓后重建 core 文档入口（2026-10-12，见 AGENTS.md 尾注 _ARCHITECTURE.md 建文件纳入_）；R1 frontmatter 由 DOCS-02 补齐；QT-05 B3 契约迁移路径对拍（2026-10-23）
---

# Quant Lab — 架构总览

> **定位**：A 股量化研究平台（Quant Lab）的服务拓扑、数据归属与存储真相。
> 顶层产品定义见 [PRODUCT.md](PRODUCT.md)；技术规格（接口/端点/领域模型）见
> [SPEC.md](SPEC.md)；架构决策见 [ADR.md](ADR.md) + `adr/`。
> 本文件是 AGENTS.md §11 导航与 Rule 1 触发器（加表/端口变更）的落地目标。

---

## 1. 服务拓扑

微服务架构，Analysis Service 作为 API 网关协调 Data Service 与 AI
Service。**PG 与 Redis 不在容器内**——各服务经 `host.docker.internal` 连
宿主机实例（compose 只编排 3 个 Go 服务；前端已拆仓，见下）。

```
Browser (Vue SPA，tellurion-ui 仓 :5173 dev / nginx :8080)
  │  只经 /api/* 打后端（AUD-32：nginx 托管在宿主 8080）
  ├──► analysis-service :8085 ──► PostgreSQL :5432（唯一数据面）
  │                             ──► Redis :6379（缓存）
  │                             ──► risk-service :8086（源码在 tellurion-risk 仓，X3；
  │                                `cmd/riskservice -addr` 的默认值，与 core 的
  │                                `risk_service.resolve_url` 一致。该服务不在 compose 里，
  │                                单主机裸跑要和 ai-service 的 8086 错开）
  │                                两个出站调用，都不在热路径：run 提交时取规则快照
  │                                hash（QT-66，`POST /api/risk/resolve`）；告警经
  │                                `quant.alert_outbox` 落一行再按批回传
  │                                （QT-42 X3c，`POST /api/risk/events`，开关
  │                                `risk_service.events_enabled`，默认关）
  ├──► ai-service :8086 ────────►（源码在 tellurion-agent 仓，
  │                               ADR-027 拆仓；compose 经 additional_contexts
  │                               构建；analysis 对 copilot/pipeline/explore/
  │                               tools 四族路由纯反代）
  ├──► data-service :8081 ──────► tushare.pro 摄取（唯一外部数据入口）
```

> **REV-16（2026-10-17）**：`strategy-service`（:8082）已删除——OBS-11 删掉信号端点后
> 它只剩 `/health` 与恒空的 `/strategies`，零调用方；ADR-012 已关闭。`pkg/strategy`
> 库层保留（将来若需远程形态，从库层重建服务很便宜）。

| 服务 | 端口 | 入口 | 职责 |
|---|---|---|---|
| analysis-service | 8085 | `cmd/analysis` | API 网关 + 回测/batch/walkforward + 风控/执行（in-process，ODR-021 合并）+ 信号/证据/产业链只读门 |
| data-service | 8081 | `cmd/data` | tushare 摄取、同步任务队列（sync jobs/SSE）、因子计算 |
| ai-service | 8086 | agent 仓 | Hermes AI 实验员（L3 编排层，源码不在本仓） |

> **AI 拆仓（ADR-027，2026-10-09）**：`pkg/ai` + CopilotService + cmd/ai 物理
> 迁入 `tellurion-agent`（独立 module，单向依赖 agent→core）。core 不许
> import `pkg/ai`——`internal/repoguard/ai_boundary_test.go` 白名单护栏钉住。

---

## 2. 数据归属（ADR-023，禁止双写）

可重建数据物理唯一于 PG；不可重建的研究叙事唯一于 vault markdown。共享靠
同一份数据 + `content_hash` 证据坐标，不靠复制。

| 分区 | 内容 | 写入方 |
|---|---|---|
| A 原始源响应 `ingest.raw` | 外部响应的逐字节归档（class A） | data-service 摄取时自动归档（`TushareClient.call`）；`POST /api/ingest/raw` 是唯一外部写入门 |
| B 规范化行情 `public`（`stocks`/`ohlcv_daily_qfq`/`stock_fundamentals`/`trading_calendar`/`splits`/`dividends`/`index_constituents` 等） | 可从 A 重建的规范化数据 | data-service 摄取 |
| C 派生计算 `quant.*` + Redis `factor_cache` | 因子/回测/IC/信号/产业链静态层/实验日志 | 各计算路径（可 DROP 重建） |
| E 研究结构化投影 `research.*` | vault markdown 的可重建投影 | 投影写入（可 DROP 重建） |

---

## 3. 数据库 schema —— 主真相在 `migrate()`

**加表 / 加列的主路径**：`pkg/storage/postgres.go` 的 `migrate()`（数组构
造在 `migrationStatements()`，每条幂等，编号续最大）。`migrations/` 与
`docs/migrations/` 下的 `.sql` **不被执行**（golang-migrate 封装
`MigrationManager` 已于 2026-09-18 删除），只作历史镜像——新增迁移按 K8 先例
在内联 DDL 追加包级变量（如 `SupplyChainSchemaDDL`）+ 建 `docs/migrations/`
镜像副本，两侧一致性由 `pkg/storage` / `pkg/strategy` 的归一化空白比对测试钉住。

**表数与表清单以源码为准，不要引用任何文档里的数字**（AGENTS.md §11 口径）：

```bash
# 内联 DDL 建表数（REV-14：本文件不抄数字——数字一抄就会腐烂，实测已与历史
# 记录不一致，逐个数都会被读者当成契约。要数字就下面这条命令现跑）
grep -c "CREATE TABLE IF NOT EXISTS" pkg/storage/postgres.go
# 建 schema 清单
grep -n "CREATE SCHEMA IF NOT EXISTS" pkg/storage/postgres.go
```

分区结构：

- `public`：规范化市场数据（ stocks / ohlcv_daily_qfq【唯一 hypertable，migrate 内 `create_hypertable`】/ stock_fundamentals / trading_calendar / splits / dividends / index_constituents / factor_cache / factor_returns / ic_analysis / backtest_jobs【Migration 009；**Migration 059 增 `result_status`**，三值 `completed/invalid/failed`，CHECK 与 orchestration `run.backtest_jobs` 对齐】/ sync_jobs / sync_schedules / strategies / users / audit_logs 等）
- `ingest.raw`：类 A 原始响应归档（唯一键 `content_hash`）
- `quant.*`：派生计算。信号面 `external_signals`（K8，Migration 048）；产业链静态层 `sw_industry_tree` / `supply_chain_edge` / `industry_vitality` / `etf_constituent`（P2-2，Migration 049，统一 `source`/`as_of` 溯源列 + 幂等唯一约束）；run 留痕 `run_provenance`（QT-63，Migration 056，**只存输入身份、不存 status** —— status 的唯一权威是 `backtest_jobs`，查询用 join/视图；`risk_rules_hash` 由 QT-66 每次 run 提交时向 tellurion-risk 的 `POST /api/risk/resolve` 取一次，未接入记空串 + `not_configured`，取不到即 run `failed` + `risk_rules_unavailable`）；告警发件箱 `alert_outbox`（QT-42 X3c，Migration 060，投递状态机 `pending → sent | failed` —— `failed` 是**终态但保留行**，那行是唯一能回答「这条告警为什么没进 risk」的东西；relay 每轮按 `created_at` 取 pending 一批投，`attempts` 触 `events_max_attempts` 才判死。注意它**不是**第二个告警真相源：告警内容仍由 `pkg/alert` 产出，这里只存投递留痕）；内核模块表 `strategy_state` 等由各模块 store 的 `EnsureSchema` 幂等兜底（未并入 migrate 的以模块注释为准）
- `research.*`：研究投影（conclusion / question / profile）
- `market.*`：指数行情独立分区（`market.index_daily`，Migration 057，migrate 内建——指数不与个股混表）
- `audit.*`：内核事件存储（`audit.message_log`，`pkg/eventstore/schema.go` 的 `EnsureSchema` 幂等建，未并入 migrate）

> **migrate 之外的模块内自建表（3 处，存量如实登记）**：`pkg/eventstore/schema.go`
> 的 `audit.message_log`、`pkg/strategy/state_store.go` 的 `quant.strategy_state`、
> `pkg/strategy/signal_store.go` 的 `quant.external_signals`（与 migrate 内建表重复）。
> 是否收编进 `migrate()` 须走正式 schema 变更评审。

> ⚠️ **已知过时（登记，未修）**：[SPEC.md](SPEC.md) 的「TimescaleDB Schema」节
> 手抄 DDL 已与代码脱节（`ohlcv_daily` 实为 `ohlcv_daily_qfq`；`factor_cache`
> 的 hypertable 调用已按注释刻意省略）。以本节口径与 `migrate()` 源码为准；
> 该节的修正属独立文档债，不在任何进行中 task 的范围。

### TimescaleDB

仅 `ohlcv_daily_qfq` 建 hypertable（`migrate()` 内 `create_hypertable(...,
if_not_exists => TRUE)`，TimescaleDB 不可用时 Warn 降级不阻断启动）。
`factor_cache` 等时序表按 022 迁移注释**刻意不建** hypertable（需要时单独加）。

---

## 4. 缓存设计（Redis :6379）

`pkg/storage/cache.go`（`storage.Cache` 接口，`RedisCache` 实现）：

| 用途 | 说明 |
|---|---|
| `factor_cache` | 因子计算缓存（PG 表 + Redis 双层；Redis miss 回表） |
| session store | JWT 会话（auth） |
| `sync_job_cache` | data-service 同步任务状态热数据 |
| `DataCache` | data-service 读路径缓存装饰（stocks 等热点查询） |

Redis 不可用时服务不启动即失败（连接在装配期建立）——缓存是必需依赖，
不是可选装饰。

---

## 5. 目录结构（本仓）

```
tellurion/
├── cmd/analysis/     # 主服务 :8085（API 网关 + 风控/执行 in-process）；wiring/ 是装配层
├── cmd/data/         # 数据服务 :8081（tushare 摄取 + sync 队列）
├── pkg/
│   ├── backtest/     # 回测引擎 + job/batch/walkforward（RunSpec/ResultSpec 四契约与 kernel/market 路由表 2026-10-23 起在 `tellurion-contracts:engine/`，本仓经 `aliases.go` 门面引用，DBT-06 / QT-05）
│   ├── data/         # 数据管道（tushare 客户端、因子、产业链导入）
│   ├── kernel/       # 模块化内核**框架**（Module/BootOrder/生命周期/槽位，K1–K12 线）；
│   │                 # 业务模块包装与取数接线在 cmd/analysis/wiring（QT-50 ①：框架零业务 import）
│   ├── marketdata/   # 规范层读路径（Router 透明路由 + 1m→15m/1d 聚合，数据因子蓝图 D1）
│   ├── live/         # 实盘/纸交易引擎
│   ├── risk/         # 风控（in-process 并入 analysis）
│   ├── storage/      # PG/Redis 存储——DDL 主真相在 postgres.go（3 处模块内自建表除外，见 §3）
│   ├── strategy/     # 策略框架 + 插件 + 表达式 + SignalProvider/信号面
│   └── validation/   # 验证器链（五维纯计算）
├── web/              # Vue 3 前端——⚠️ 已拆仓至 tellurion-ui（2026-10-09），本目录历史遗留
├── docs/             # 文档（SPEC/ADR/ODR/TASKS/migrations 镜像）
├── contracts/        # ⚠️ 运行时数据契约资产（field_dictionary.yaml + schema），**不是 Go 包**；四契约本体在 tellurion-contracts
├── e2e/              # Go 守卫/集成测试；`e2e/guard/` 放两把跨仓 SPEC 互锁守卫（读本仓 docs/SPEC.md）。Playwright E2E 已拆仓，真相源在 tellurion-ui/e2e；AUD-61 测试债工单亦迁至该仓 docs/TASKS.md
└── tools/            # 运维脚本（不被 CI 执行）
```

> **横切库唯一实现（裁-18 / QT-73 一期，2026-10-25）**：`errors` / `decimal` / `clock` /
> `logging` / `httpclient` 的实现已迁 `tellurion-kit`（`require v0.1.1`），本仓同名目录
> **已删除**、不留转发壳；`internal/repoguard` 把五个路径登记进 `retiredPackages`
> （复活即红）。仍在 core 的横切包：`statistics` / `httpserver` / `observability` /
> `auth` / `authz`（二期等 kit `v0.1.3`，见 QT-73 ⑥）。跨仓依赖只认 tag
> （ADR-D31），本仓已无 `replace`。

---

## 6. 端口速查

| 端口 | 归属 |
|---|---|
| 5432 / 6379 | PostgreSQL / Redis（宿主机） |
| 8081 | data-service |
| 8085 | analysis-service（API 网关） |
| 8086 | ai-service（agent 仓构建） |
| 8080 / 5173 | 前端 nginx（宿主）/ Vite dev（tellurion-ui 仓） |

---

## 7. 规范层数据流（数据因子蓝图 D1，2026-10-14）

> **D 编号读法**：本文「数据因子蓝图 Dn」指 `tellurion-docs:docs/design/data-factor-unification.md`
> （ADR-031 附件）的决议序列 D1–D9，与 ADR-031 本体
> （`tellurion-docs:docs/adr/adr-031-kernel-pluggable-nt-ready.md`）的 D1–D5
> （四契约 / NT 侧车 / NT-ready / 决策门 / Go 约束）**同名异指**，勿混。

**不变量**：全系统数据取用只认一套规范层 schema——三类字段（OHLCV /
Fundamental / FactorOutput），每条记录**自带频率与可见时间点**；
connector 之外无人感知底层表。字段表 / 频率词表 / 聚合口径 / 时间语义
的文档真相在 [SPEC.md「规范层 Schema」](SPEC.md#规范层-schemacanonical-layer)，
本节只画数据流与护栏边界。

```
                     ┌──────────────────────────────┐
                     │ 物理表（迁移期共存，登记基线）  │
                     │  ohlcv_daily_qfq（1d，qfq）    │
                     │  ohlcv_minute    （1m）        │
                     └───────────┬──────────────────┘
                                 │ 唯一被许可直连的读路径
                     ┌───────────▼──────────────────┐
                     │ pkg/marketdata.Router.Bars   │ 声明频率 → 透明路由
                     │  1d → daily 表原样            │ Frequency + VisibleAt
                     │  1m → minute 表原样           │ 随每条 bar 显式声明
                     │  15m → 1m 聚合（不物化）      │ 未知频率 fail-loud
                     └───────────┬──────────────────┘
                                 │ []market.OHLCV（规范层）
              ┌──────────────────┼───────────────────┐
              ▼                  ▼                   ▼
         Go 内核直吃         NT 侧翻译层            遗留路径（基线登记，
      （回测/因子/信号）   sidecar/mapping.py      只减不增，逐项销号）
                          BAR_FIELD_MAP：
                          加数据类 = 映射加一行；
                          export_bars 是第一个
                          消费者（K13 数据流）
```

护栏（机械判据，全部在 CI 里）：

- **直连棘轮**（`internal/repoguard/canonical_layer_test.go`）：非测试
  Go 文件里的 `ohlcv_daily_qfq` / `ohlcv_minute` 字符串字面量（SQL /
  DDL / 表名常量），只允许 `pkg/marketdata` 与 `directTableAccessBaseline`
  登记的遗留文件；**新开直连 = 红，基线文件被清理 = 逼删条目**
  （只减不增，白名单不许替已消失的违规打掩护）。
- **防漂移**（`e2e/guard/spec_drift_test.go`；被比的类型自 2026-10-23 起在
  `tellurion-contracts:domain`，读的是本仓 `docs/SPEC.md`，所以守卫留在本仓）：
  SPEC 三类字段表 +
  频率词表 ↔ Go 类型反射比对（双向）；Python `FREQ_*` ↔ Go `Freq*`
  逐字钉住（`nt-sidecar/tests/test_d1_mapping.py` 读 Go 源码）。
- **回归红线**（`pkg/marketdata/router_integration_test.go`）：Router 的
  1d 路由与遗留 `storage.GetOHLCV` 逐字段对拍（真实 PG 自灌自证，
  DB 不可达 Skip 不 Fail）——改的是取数通道，不是数据。

**上游闸门（数据因子蓝图 D2，2026-10-14）**：规范层「记录自带可见时间点」
不靠消费方自觉，而是注册期的结构性闸门——connector 注册
（`pkg/data/source.Registry.Register`）时必须声明**时间语义 + 频率口径**，
缺失 / 非法 / 与数据形态矛盾一律 fail-closed 拒收（`validateDeclarations`
三类哨兵 `ErrDeclarationMissing` / `ErrDeclarationInvalid` /
`ErrDeclarationMismatch`）。语义词表唯一真相在 `tellurion-contracts:domain/market`
（`TimeSemantics` / `FrequencySpec`），存量 connector 的声明登记在
`pkg/data/source/declarations.go`（「声明即登记」）。高危样例：市场活跃度 /
拥挤度 = `after_close`（收盘后可见，盘中用当日值即偷看）。文档真相见
SPEC.md「connector 注册契约」节。

**新数据源接入样板（数据因子蓝图 D5，2026-10-15）**：「挂新数据 → 可挖因子」
全链路的第一个真实实现（市场活跃度 = Tushare `daily_basic` 换手率族）：

```
  Tushare daily_basic（api）
    └─ tushare_daily_basic adapter（声明 after_close + trade_date 锚点 + daily）
         └─ data.TushareClient.FetchDailyBasic(按日，normalize：缺失=NULL / 脏值跳过 / 全跳过 fail-loud)
              └─ market_activity 表（Migration 056，(symbol,trade_date,source) 幂等）
                   └─ expression 字段词表新来源 activity（turnover_rate 族）
                        └─ market_activity 因子（公式 turnover_rate / ts_mean(·,20) - 1）
                             └─ factor_cache（按日分数序列，缺失≠0）
```

PIT 执法点有二：① 声明侧 `after_close`（`TestDailyBasicDeclaration_MustBeAfterClose`
钉死，改宽即红）；② 取数侧 `GetMarketActivityForDateRange` 的
`trade_date <= end`（晚于计算日的行结构上不可见）。名册协议变更
（D3 之后第一个新增名册名）与端到端步骤留档见 SPEC.md「新数据源样板」节。

**多频率进策略视野（数据因子蓝图 D6，2026-10-15）**：回测只吃日线的现状
延伸到「日频决策 + 引用当日内分钟特征」，通道是**启动前快照 + 契约注入**，
不是运行时查询：

```
  进程装配（一次）
     wiring.NewMarketDataRouter(store)      ← 复用 store 池，一个进程一个 Router（QT-64；QT-50 ① 迁至 cmd/analysis/wiring）
       └─ wiring.AttachMarketDataRouter      ← 主引擎与 walk-forward 窗口引擎共用这一个装配函数

   RunBacktest 启动前（一次性）
     warmIntradayIfNeeded（仅 IntradayAware 策略触发）
       └─ marketdata.Router.Bars(Freq1m, symbol, [start, end+1d) 按 365 天分片)
            └─ 引擎 L1 快照 map[symbol][]market.OHLCV（ts 升序，Frequency/VisibleAt 已填）

   每个交易日（回放日 D）
     getSignals → LocalSignalProvider.Signals
       ├─ 类型断言 IntradayAware ⇒ 注入「绑定回放日」的 provider
       │    （快照 + D 日边界，二分切当日段；别的交易日取不到）
       │    没有快照（未接 Router / 库里无分钟数据）⇒ 不注入，策略明确失败
       └─ 策略调 IntradayFeature(symbol, "open_momentum_30m", asOf)
            └─ 闸门：只许 VisibleAt <= asOf 的 bar 进特征（10:00 bar 10:01 可见）
```

红线与执法：策略运行时禁止直接查 `marketdata.Provider`——`pkg/strategy`
不许 import `pkg/marketdata`（`internal/repoguard` import 隔离护栏），
provider 实现无 provider/router/pool 句柄（反射断言）。日频决策时刻 =
当日收盘（日线 bar 可见点），成交仍走既有日线路径（分钟级撮合另立），
纯日线策略对整条接线零敏感（回归测试逐点对拍）。契约 / 特征目录 /
闸门口径的文档真相见 SPEC.md「多频率策略视野」节。

## 8. 因子定义库数据流（数据因子蓝图 D3，2026-10-14）

**不变量**：因子定义只认「库里公式」——一份种子定义
（`tellurion-contracts:domain/factor_def_seed.go`，编译期常量）+ 一张持久表
（`factor_definitions`，迁移 051）+ 一个进程内 Registry
（`pkg/data/factor_registry.go`，构造时定稿、只读）。计算线
（`ComputeAllFactors` / `ComputeFactorByName`）的因子清单 =
Registry 投影；**connector 与策略之外无人感知内核分派**。定义
schema / 内核四态 / 版本哈希纪律的文档真相在
[SPEC.md「因子定义 Schema」](SPEC.md#因子定义-schemafactor-definitiond3)，
本节只画数据流与护栏边界。

```
   ┌─────────────────────────────────────────────────────────┐
   │ 定义层（库里公式，唯一权威）                                │
   │  contracts 仓 domain/factor_def_seed.go（种子 11 条）      │
   │    · 字段：名字/公式/域过滤/输入字段/生效日/口径/前视纪律    │
   │    · 版本哈希 = SHA-256(canonical 内容)——改公式=新哈希     │
   └──────────────┬───────────────────────────┬──────────────┘
                  │ SyncFactorDefinitions     │ NewRegistry（启动装载，
                  │ （幂等 upsert）            │ 全量 fail-closed 校验）
                  ▼                           ▼
   ┌──────────────────────────┐   ┌──────────────────────────┐
   │ factor_definitions 表     │   │ data.Registry（只读快照） │
   │ （迁移 051；加载时重新     │   │  · Definition(name)       │
   │  derive 哈希比对，不符     │   │  · CodeKernel 绑定表      │
   │  即拒载——fail-closed）   │   │  · FailClosedError 带清单 │
   └──────────────────────────┘   └────────────┬─────────────┘
                                               │ 计算分派（按名/按批）
              ┌────────────────────┬───────────┴────────────┐
              ▼                    ▼                        ▼
        leaf / formula          code                    pending
      表达式引擎现算         Go 出口闭包            不计算，fail-closed
      （pkg/expression      （CodeRef →             （ErrFactorKernel
       Formula+Validity，    Compute*Factor）        Undefined，显式
       域外即缺失）          纵向 5 因子               进 Skipped 报告）
              └────────────────────┴───────────┬────────────┘
                                               ▼
                              factor_cache（PG）+ Redis 双层
                              产出形状 = D1 FactorOutput（缺失≠0）
```

护栏（机械判据，全部在 CI 里）：

- **注册表棘轮**（`internal/repoguard/factor_registry_test.go`）：
  非测试 Go 文件引用 `Compute*Factor` 内核方法（定义/绑定三文件除外）
  = 绕过 Registry 直调内核 → 红；同一 composite literal 内聚集 ≥3 个
  名册常量 = 库外重建清单 → 红。基线带裁决理由，只减不增。
- **数值回归红线**（`pkg/data/factor_formula_test.go`）：公式库版 vs
  原代码版同输入同输出（momentum/value/quality 三列全等，域过滤与
  历史不足的缺失语义由 Validity / NaN 复现）。
- **防漂移**（`e2e/guard/spec_drift_test.go` + 契约仓的种子词表测试）：
  SPEC 定义骨架表 ↔ `FactorDefinition` 反射比对；种子字段码 ↔
  `equitydeep.FieldX` 契约常量逐一比对。
- **版本可复现**（`pkg/data/factor_registry_test.go` +
  `pkg/storage/factor_definition_test.go`）：同种子同哈希；
  库里公式被绕过写入路径改坏 → 加载期哈希比对拦住。

迁移方向：size / volatility / growth 三因子（pending）补计算内核；
回测预热清单（`pkg/backtest/cache/factor_cache.go`，基线登记）收敛为
从 Registry 读取（D6）；D4 因子治理细则（登记人/审计/生效日流程）
在本 Schema 上叠加，不改形状。

---

## 9. AI 因子挖掘流水线数据流（数据因子蓝图 D8，2026-10-15）

**不变量**：挖掘产物是**公式文本不是代码**——`pkg/mining` 的 `Proposer` /
`Evaluator` 把挖掘算法本体（LLM/MCTS/基因池）关在接口之外；公式经沙箱验证、
治理层检验后按 D4 登记入库，策略按名即用（零代码改动）。挖掘算法本体与
因子应用策略不在本层。治理语义 / 留痕 schema 的文档真相在
[SPEC.md「AI 因子挖掘流水线 + 治理层（D8）」](SPEC.md)，
本节只画数据流与护栏边界。

```
   Proposer（LLM/MCTS/基因池——接口之外）
        │ []Candidate（公式文本 + Provenance 来源留痕）
        ▼
   ┌───────────────────────────── pkg/mining.Pipeline.Run ────────────────────┐
   │                                                                          │
   │  Snapshot（启动前预加载的只读快照，无运行时查询入口——防偷看）             │
   │      │                                                                    │
   │      ▼                                                                    │
   │  (1) Sandbox.Validate：解析 + L1 闸门 + 字段词表 + Validity + 有界求值     │
   │      │  通过 → Attestation{ContentHash = VersionHash}                     │
   │      ▼                                                                    │
   │  (2) 治理层：DetectOverfit（adjP，NumTrials 含失败）+ ValidateOutOfSample  │
   │      │  任一不过 → 该条拒收（RejectReason），不中断整轮                     │
   │      ▼                                                                    │
   │  (3) admit：凭证非空 + 凭证绑定该版本 → data.RegistrationBook.Register     │
   │      │  → D4 四样齐全入库（factor_registrations）                          │
   │      ▼                                                                    │
   │  Record → domain.FactorMiningRecord（挖/测/用全程哈希可追溯审计）           │
   └──────────────────────────────────┬───────────────────────────────────────┘
                                      ▼
   factor_mining_records 表（迁移 055）+ factor_registrations 表（迁移 053）
                                      │
                                      ▼
   策略按名引用：Pipeline.Effective（取最新 active 版本）→ EvaluateOnSnapshot
```

护栏（机械判据，全部在 CI 里）：

- **拦截点**（`pkg/mining/pipeline_test.go`）：跳过沙箱（凭证缺失）→
  `ErrSandboxNotRun`；凭证不绑定该版本 → `ErrAttestationMismatch`；两条
  都在 `admit` 内把关，「反正调用方说验过了」放不进来。
- **库侧兜底**（Migration 055 DB CHECK）：`admitted ⇒ sandbox_hash = version_hash`、
  生成器非空、来源枚举、被拒必须写明原因——绕过写入路径直接 INSERT 缺样也被库拒绝。
- **三条命根子**：版本哈希内容派生（可复现）/ 求值只读快照且缺字段=NaN
  （防偷看）/ 同公式同快照逐点相等（可比），各有测试钉住。
- **防漂移**（`e2e/guard/spec_drift_test.go`）：SPEC 留痕表
  `factor_mining_record` 锚点 ↔ `FactorMiningRecord` 反射双向比对。

迁移方向：真实 `Proposer`（LLM/MCTS）与 `Evaluator`（研究验证 IC）接线；
挖掘产物只进公式库不进代码库——名册仍是冻结协议，新因子名属协议变更另立。
接线前的登记：`pkg/mining` 本包暂无导入者，已按「零件先写、后接线」（P2-9wire）
登记进 `internal/repoguard` 的 `unwiredPackages`；接线（挖掘编排入口消费本包）
时必须**删掉那条白名单条目**——否则护栏会把它当陈旧条目报出来。

---

迁移方向：connector 契约已接入（D2 完成，`validateDeclarations` fail-closed
闸门生效）→ 消费方逐个切 Router → 棘轮基线逐条销号 → 物理表直连归零。

---

_Last updated: 2026-10-21（system-spec-review 订正：compose 服务计数 4→3、§3 补 market/audit schema 分区并如实登记 3 处模块内自建表、D 编号引用改指数据因子蓝图（ADR-031 本体 D1–D5 另有其义）；REV-14：删掉手抄表数快照，只留 grep 口径；此前 §7 增补新数据源接入样板 ADR-031 D5；§7 增补多频率进策略视野 ADR-031 D6；§9 AI 因子挖掘流水线数据流 ADR-031 D8；2026-10-14 §7 增补 connector 注册契约上游闸门 ADR-031 D2；§7 规范层数据流 ADR-031 D1 + §8 因子定义库数据流 ADR-031 D3）_
_维护规则：服务/端口/schema 分区变更时更新本文件（AGENTS.md Rule 1）；表数不手抄，只给 grep 口径。_
