---
status: evergreen
last-verified: 2026-11-11
verified-by: 2026-11-11 部署形态收敛：本地只留一个 `docker-compose.yml`（默认 4 服务 + `--profile full` 4 服务），撤销 QT-79 的代裁 `D-DEPLOY-1 = A`，跨仓 7 份 `deploy/compose.yaml` 注明为各仓自证资产；2026-11-11 阶段 B core 对齐（B5-1）：服务拓扑按部署形态写实、data/ai 拆仓后的入口与端口、`public` 13 张 / `quant`·`audit` 启用 / `research` 退役、Redis 面退役（保留历史）、TimescaleDB 归 data；口径与实测数据见 `docs/design/cross-repo-architecture-baseline-2026-11-11.md` §5/§6；文档拆仓后重建 core 文档入口（2026-10-12，见 AGENTS.md 尾注 _ARCHITECTURE.md 建文件纳入_）；R1 frontmatter 由 DOCS-02 补齐；QT-05 B3 契约迁移路径对拍（2026-10-23）
---

# Quant Lab — 架构总览

> **定位**：A 股量化研究平台（Quant Lab）的服务拓扑、数据归属与存储真相。
> 顶层产品定义见 [PRODUCT.md](PRODUCT.md)；技术规格（接口/端点/领域模型）见
> [SPEC.md](SPEC.md)；架构决策见 [ADR.md](ADR.md) + `adr/`。
> 本文件是 AGENTS.md §11 导航与 Rule 1 触发器（加表/端口变更）的落地目标。

---

## 1. 服务拓扑

微服务架构，Analysis Service 作为 API 网关协调 Data Service 与 AI
Service。**部署形态有两种，别把两者的配置混着读**（G-05；2026-11-11 收敛，见下）：

| 形态 | 编排来源 | 服务集合 | PG 在哪 / Redis |
|---|---|---|---|
| 本地统一栈（默认，无 profile） | 本仓 `docker-compose.yml` | 4 个：data-service / broker / analysis-service / ai-service | PG **不在容器内**：各服务经 `host.docker.internal` 连宿主机原生 PostgreSQL 16.15；**Redis 已于 2026-11-12 彻底移除**（见 §4） |
| 本地统一栈 `--profile full` | 同上 | 再加 4 个：risk / strategy / analytics / orchestration | 同上（DSN 同样走 `host.docker.internal:5432`） |
| k8s（**已退役**，2026-11-12） | `deploy/k8s/` —— 清单整体移入 `archive/`，活跃层 0 份；见 `deploy/k8s/README.md` | 历史：analysis / data / web 三个 Deployment + postgres StatefulSet | 不参与部署 |

> **2026-11-11 定案：本地只留一个 compose。** 主任指令要求单一文件，避免三份
> 各说各话；QT-79 的代裁 `D-DEPLOY-1 = A`（core compose 与
> `docker-compose.services.yml` 各自是单一 owner 的完整栈、不合并）**已撤销**。
> 原代裁的顾虑是 8081 双发布（聚合栈的 `data` 与 core 的 `data-service` 同时
> 发布同一端口）——新方案只保留 `data-service` 一份，聚合栈的 `data` 服务已删除，
> 该前提消失。`docker-compose.services.yml` / `.e2e.yml` / `.nt.yaml` 三份文件
> 均已删除，差异改由 profile 与命令行 env 表达。
>
> 跨仓的 7 份 `<repo>/deploy/compose.yaml` **不动**：它们是各仓自证 Dockerfile /
> health 的资产，不属于本地统一栈，联调一律用本仓 `docker-compose.yml` 的 profile。
>
> **2026-11-12：k8s 形态退役（`CORE-K8S-EXIT` / D7=a）。** `deploy/k8s/` 的 7 份
> YAML 与说明整体移入 `archive/`，唯一编排是 `docker-compose.yml`；活跃层出现
> 清单即被 `tools/check_deploy_consistency.py` 判红。恢复 k8s 须另立 ADR。

前端已拆仓至 `tellurion-ui`（2026-10-09）；data-service 已拆仓至
`tellurion-data`（`SVC-CORE-DATA-RETIRE`，2026-11-08）—— 两者都只是**拓扑上
的邻居**，不产出本仓代码。

```
Browser (Vue SPA，tellurion-ui 仓 :5173 dev / nginx :8080)
  │  只经 /api/* 打后端（AUD-32：nginx 托管在宿主 8080）
  ├──► analysis-service :8085 ──► PostgreSQL :5432（唯一数据面）
  │                             ──► risk-service :8089（源码在 tellurion-risk 仓，X3；
  │                                `cmd/riskservice -addr` 的默认值 127.0.0.1:8089，
  │                                与 core 的 `risk_service.resolve_url` 一致；8086
  │                                留给 ai-service（ADR-036 G0）。该服务只在
  │                                `--profile full` 里，默认栈不含它；
  │                                单主机裸跑需自行拉起或用
  │                                `RISK_SERVICE_RESOLVE_ENABLED=false` 走内置限额）
  │                                两个出站调用，都不在热路径：run 提交时取规则快照
  │                                hash（QT-66，`POST /api/risk/resolve`）；告警经
  │                                `quant.alert_outbox` 落一行再按批回传
  │                                （QT-42 X3c，`POST /api/risk/events`，开关
  │                                `risk_service.events_enabled`，默认关）
  ├──► ai-service :8086 ────────►（源码在 tellurion-agent 仓，
  │                               ADR-027 拆仓；compose 直接以 agent 仓
  │                               为构建上下文；analysis 对 copilot/pipeline/
  │                               explore/tools 四族路由纯反代）
  ├──► data-service :8081 ──────► tushare.pro 摄取（源码在 tellurion-data 仓，
  │                               SVC-CORE-DATA-RETIRE；本仓只经 HTTP 反代，
  │                               `market`/`ingest` schema 的唯一 writer 在那边）
  ├──► broker :8091 ────────────► 执行网关（源码在 tellurion-broker 仓；
  │                               默认栈以 mock 通道起，装配期 GET /version
  │                               核对 channel，不一致即拒起）
```

> **本图不含 Redis**：Redis 于 2026-11-12 随 `CORE-REDIS-EXIT` 彻底移除（三处死代码
> 与 `go-redis` 依赖已删），历史设计见 §4。

> **REV-16（2026-10-17）**：`strategy-service`（:8082）已删除——OBS-11 删掉信号端点后
> 它只剩 `/health` 与恒空的 `/strategies`，零调用方；ADR-012 已关闭。`pkg/strategy`
> 库层保留（将来若需远程形态，从库层重建服务很便宜）。

| 服务 | 端口 | 入口 | 职责 |
|---|---|---|---|
| analysis-service | 8085 | `cmd/analysis` | API 网关 + 回测/batch/walkforward + 风控/执行（in-process，ODR-021 合并）+ 信号/证据/产业链只读门 |
| data-service | 8081 | `tellurion-data` 仓（**本仓无 `cmd/data`**） | tushare 摄取、同步任务队列、`market`/`ingest` 的唯一 writer；本仓只经 `data_service.url` 反代 |
| broker | 8091 | `tellurion-broker` 仓 | 执行网关（ADR-036 G0），本地默认栈以 **mock 通道** 起；analysis 装配期必读 `GET /version` 的 `channel`，不一致即拒起 |
| ai-service | 8086 | `tellurion-agent` 仓（**本仓无 `cmd/ai`**） | Hermes AI 实验员（L3 编排层，源码不在本仓） |
| orchestration worker | 无监听 | `cmd/worker` | 用 core 引擎领 `tellurion-orchestration` 的 run（出站 `ORCHESTRATION_URL` 或 `-orchestration-url`，**两者都空即拒绝启动**）；结果落 `backtest_jobs`，三 hash 回传仍未落 |

> **orchestration worker（2026-10-25，QT-44 core 腿）**：`cmd/worker` 不是常驻 HTTP 服务，也不在默认 profile 里（要起用 `--profile orchestration-worker`）；按部署需要可起多个副本。它与 orchestration 只经 HTTP 交互，本仓不 import 对方的代码；`internal/orchestrationworker` 的 run_spec 解析 / 执行 / 上报循环见 `docs/TASKS.md` 的 QT-44 指针记录。
>
> **AI 拆仓（ADR-027，2026-10-09）**：`pkg/ai` + CopilotService + cmd/ai 物理
> 迁入 `tellurion-agent`（独立 module，单向依赖 agent→core）。core 不许
> import `pkg/ai`——`internal/repoguard/ai_boundary_test.go` 白名单护栏钉住。
>
> **出站地址的启动期体检（2026-11-11）**：data-service / risk / strategy 资产
> 是**启动必需**，`cmd/analysis/endpoints.go` 在 Boot 期解析主机名，失败即点名
> env 拒起；`ai_service.url` 是**可选下游**，Boot 期只校验 URL 形状、不解析主机，
> 首次反代调用时才解析，失败返回点名 `AI_SERVICE_URL` 与主机名的 503
> （`cmd/analysis/handlers_ai_proxy.go`）。动机：ai 的 Boot 需要解析 analysis，
> 若 analysis 的 Boot 也要求解析 ai，两者互相等 DNS，冷启动必然死锁；可选下游
> 不应成为 core 的启动门。**不静默跳过 AI 功能**——调用方拿到的是明确错误。
>
> **risk 快照的开关口径**：`risk_service.resolve_enabled` 的 fail-closed 语义
> 不变（开着就必须取到，取不到即拒起），但统一 compose 默认 `false`（内置限额
> 兜底，启动日志 `source=builtin_default`），因为 risk 只在 `full` profile；
> full 里要用真规则，`up` 前显式 `RISK_SERVICE_RESOLVE_ENABLED=true`，
> `deploy/wait-for-deps.sh` 会按 `RISK_SERVICE_RESOLVE_URL` 等 risk 就绪。

---

## 2. 数据归属（ADR-023，禁止双写）

可重建数据物理唯一于 PG；不可重建的研究叙事唯一于 vault markdown。共享靠
同一份数据 + `content_hash` 证据坐标，不靠复制。

| 分区 | 内容 | 写入方 |
|---|---|---|
| A 原始源响应 `ingest.raw` | 外部响应的逐字节归档（class A） | **`tellurion-data` 唯一 writer**（摄取时自动归档）。唯一外部写端点是它的 `POST /ingest/points`（data ADR-0006，G-03）；core 侧 `POST /api/ingest/raw` 已随 `SVC-CORE-DATA-RETIRE` 删除，只剩契约测试里的历史例外条目 |
| B 规范化行情 `market.*`（`stocks`/`ohlcv_daily_qfq`/`trading_calendar`/`splits`/`dividends`/`index_constituents` 等） | 可从 A 重建的规范化数据 | **`tellurion-data` 唯一 writer**（2026-11-08 起这些表从 `public` 迁到 `market`，G-04）；core 只读四张裸表（清单见 [SPEC.md](SPEC.md) 的「跨仓只读表依赖清单」）。**例外：`stock_fundamentals` 不在此分区** —— 第 142 行已如实登记它仍是 core 的 `public` 表（归属裁决 2026-11-12：DDL/写者/读者都在 core，读面是 `GET /api/fundamental/{symbol}`） |
| C 派生计算 `quant.*` | 因子/回测/IC/信号/产业链静态层 | core 各计算路径（可 DROP 重建）。`Redis factor_cache` 已删除（2026-11-12，见 §4）；**`public.factor_cache` 已删表**（2026-11-12，`CORE-FACTOR-CACHE-RETIRE`，Migration 066 `DROP TABLE`）：无写者、无读者、不得重建 —— 因子值按 ADR-037 D2 在 run setup 现场求值并物化到 run 级内存，「实验日志」随 `SVC-CORE-AI-RETIRE` 删除 |
| ~~E 研究结构化投影 `research.*`~~ **已退役** | vault markdown 的可重建投影 | 三表（`profile`/`conclusion`/`question`）与 5 条索引随 `SVC-CORE-AI-RETIRE`（2026-11-10）删净，`migrate()` 不再建 `research` schema |

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

- `public`：**13 张，全是 core 自己的簿记 / 因子 / 回测元数据**（2026-11-11 实测，证据口径见[跨仓基线](design/cross-repo-architecture-baseline-2026-11-11.md) §6 第 4 项）—— `stock_fundamentals` / `factor_cache` / `factor_returns` / `ic_analysis` / `backtest_adjustment_anchors` / `users` / `audit_logs` / `fundamentals_detail` / `data_migrations` / `factor_definitions` / `factor_registrations` / `factor_registration_audit` / `factor_mining_records`。行情类表（`ohlcv_daily_qfq` / `ohlcv_minute` / `stocks` / `splits` / `dividends` / `trading_calendar` / `index_constituents` / `stock_sector_map` / `market_activity`）的 DDL 与 hypertable 调用**都已不在本仓**（`SVC-CORE-DATA-RETIRE`，owner = `tellurion-data`，落在它的 `market` schema）；`backtest_jobs` / `sync_jobs` / `sync_schedules` / `strategies` 同样已退役（任务面归 orchestration 的 `run.backtest_jobs`），`migrate()` 里留有对应的 `DROP TABLE IF EXISTS`
- `ingest.raw`：类 A 原始响应归档（唯一键 `content_hash`）—— **归 `tellurion-data`**：本仓 `migrate()` 不建该表，唯一写端点是它的 `POST /ingest/points`（data ADR-0006）
- `quant.*`：派生计算。信号面 `external_signals`（K8，Migration 048）；产业链静态层 `sw_industry_tree` / `supply_chain_edge` / `industry_vitality` / `etf_constituent`（P2-2，Migration 049，统一 `source`/`as_of` 溯源列 + 幂等唯一约束）；run 留痕 `run_provenance`（QT-63，Migration 056，**只存输入身份、不存 status** —— status 的唯一权威是 `backtest_jobs`，查询用 join/视图；`risk_rules_hash` 由 QT-66 每次 run 提交时向 tellurion-risk 的 `POST /api/risk/resolve` 取一次，未接入记空串 + `not_configured`，取不到即 run `failed` + `risk_rules_unavailable`）；告警发件箱 `alert_outbox`（QT-42 X3c，Migration 060；**Migration 061 增 `lease_until`（QT-76）**，relay 领取走 `ClaimPendingAlertOutbox` = `FOR UPDATE SKIP LOCKED` + 租约，多进程不重领、进程崩掉靠租约到期回收，投递状态机 `pending → sent | failed` —— `failed` 是**终态但保留行**，那行是唯一能回答「这条告警为什么没进 risk」的东西；`Alert.ID` 带进程级 nonce（重启同秒不再撞主键），`attempts` 触 `events_max_attempts` 才判死，关停期只剩一个总预算（`events_shutdown_budget`，到点即弃、行仍 pending）。注意它**不是**第二个告警真相源：告警内容仍由 `pkg/alert` 产出，这里只存投递留痕）；内核模块表 `strategy_state` 等由各模块 store 的 `EnsureSchema` 幂等兜底（未并入 migrate 的以模块注释为准）
- ~~`research.*`~~：**已退役**（`SVC-CORE-AI-RETIRE`，2026-11-10）—— `profile` / `conclusion` / `question` 三表与 5 条索引删净，本仓已无 `CREATE SCHEMA research`
- `market.*`：本仓只建一张 `market.index_daily`（Migration 057：指数行情独立分区，指数不与个股混表）；其余 `market.*` 表由 `tellurion-data` 建
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

本仓 `migrate()` 今天**不建任何 hypertable**：唯一一处 `create_hypertable`（针对
`ohlcv_daily_qfq`）已随 `SVC-CORE-DATA-RETIRE`（2026-11-08）删除，owner 是
`tellurion-data`。`market.index_daily` 是普通表（`factor_cache` 已删表，见下）。
`factor_cache` 已于 2026-11-12 删除（`CORE-FACTOR-CACHE-RETIRE`，Migration 066
`DROP TABLE`）：不建 hypertable、无写入、无读面；回滚 DDL 见
`docs/migrations/066_drop_factor_cache_table.sql` 的 down 段，重开须先重新裁决
因子值 owner（ADR-037）。

---

## 4. 缓存设计（Redis :6379）—— **已于 2026-11-12 移除**

> **一句话历史**：Redis 于 2026-11-12 随 `CORE-REDIS-EXIT` 移除 —— `pkg/storage` 的
> `Cache`/`RedisCache`、`pkg/trading` 的 `RedisOrderStore`、`pkg/marketdata` 的
> `CachedProvider` 三处死代码删除，`go-redis` 依赖、`redis.url` 配置段、
> `deploy/k8s/redis-deployment.yaml` 与 `wait-for-deps.sh` 的等待段同批移除。
> 本仓今天**没有任何缓存层**；因子值按 ADR-037 D2 **按需求值**、物化到 run 级
> 内存（不写库），`public.factor_cache` 已于 2026-11-12 删表
> （`CORE-FACTOR-CACHE-RETIRE`，Migration 066 `DROP TABLE`：无写者、无读面、不得重建）。
> 退役前的双层设计留在 Git 历史与本文件旧版本里，不再作为接线参照。

---

## 5. 目录结构（本仓）

```
tellurion/
├── cmd/analysis/     # 主服务 :8085（API 网关 + 风控/执行 in-process）；wiring/ 是装配层
├── cmd/worker/       # 编排 worker（无监听；缺 ORCHESTRATION_URL / -orchestration-url 即拒绝启动）
├── cmd/dualrun/      # 双跑对齐实验编排（工具，非服务）
├── cmd/recon/        # 表级 writer 对拍工具（QT-81，非服务）
├── pkg/
│   ├── backtest/     # 回测引擎 + job/batch/walkforward（RunSpec/ResultSpec 四契约与 kernel/market 路由表 2026-10-23 起在 `tellurion-contracts:engine/`，本仓经 `aliases.go` 门面引用，DBT-06 / QT-05）
│   ├── domain/       # CT-06-B（2026-11-10）从 tellurion-contracts 迁回的 core-only 形状（52 根名）；market 子包与 ≥2 使用方形状仍在契约仓
│   ├── kernel/       # 模块化内核**框架**（Module/BootOrder/生命周期/槽位，K1–K12 线）；
│   │                 # 业务模块包装与取数接线在 cmd/analysis/wiring（QT-50 ①：框架零业务 import）
│   ├── marketdata/   # 规范层读路径（Router 透明路由 + 1m→15m/1d 聚合，数据因子蓝图 D1）
│   ├── trading/      # 交易运行域 + paper/live 执行编排（LiveTrader 抽象券商；含回放 feed 与价格笼子）
│   ├── risk/         # 风控（in-process 并入 analysis）
│   ├── storage/      # PG 存储——DDL 主真相在 postgres.go（3 处模块内自建表除外，见 §3）；Redis 实现已删除（见 §4）
│   ├── strategy/     # 策略框架 + 插件 + 表达式 + SignalProvider/信号面
│   └── validation/   # 验证器链（五维纯计算）
├── web/              # Vue 3 前端——⚠️ 已拆仓至 tellurion-ui（2026-10-09），本目录历史遗留
├── docs/             # 文档（SPEC/ADR/ODR/TASKS/migrations 镜像）
├── contracts/        # ⚠️ 运行时数据契约资产（field_dictionary.yaml + schema），**不是 Go 包**；四契约本体在 tellurion-contracts
├── e2e/              # Go 守卫/集成测试；`e2e/guard/` 放两把跨仓 SPEC 互锁守卫（读本仓 docs/SPEC.md）。Playwright E2E 已拆仓，真相源在 tellurion-ui/e2e；AUD-61 测试债工单亦迁至该仓 docs/TASKS.md
└── tools/            # 运维脚本（不被 CI 执行）
```

> **横切库唯一实现（裁-18 / QT-73 一期 + QT-75 二期，2026-11-07）**：`errors` / `decimal` /
> `clock` / `logging` / `httpclient`（一期）与 `statistics` / `httpserver`（二期）的实现已迁
> `tellurion-kit`（`require v0.1.4`），本仓同名目录**已删除**、不留转发壳；
> `internal/repoguard` 把七个路径登记进 `retiredPackages`（复活即红）。
> `httpserver` 的 gin 回环包装（`LoopbackOnly`）按 kit 的分工留在应用层
> （`pkg/bootstrap/loopback.go`，判据用 kit 的 `IsLoopbackRemoteAddr`）。
> 仍在 core 的横切包：`observability` / `auth` / `authz` / `id`（各有 core 侧业务或
> 装配依赖，kit 无对应物）。跨仓依赖只认 tag（ADR-D31），本仓已无 `replace`。

> **回测/实盘执行语义边界（QT-51 / BE-02，2026-10-26）**：`pkg/backtest` **不 import
> `pkg/trading`**；引擎只认 `pkg/backtest/execution.ExecutionPort`（下单 / 撤单 /
> 成交回报 / 身份 / 健康），`trading.LiveTrader` 的适配只住在装配层
> `cmd/analysis/wiring/live_trader_port.go`。回测主循环的撮合仍在
> `execution` + `tracker`，出站 port 为 nil 即纯模拟（与切 port 前逐位一致）。
> 结构与行为双锁：`internal/repoguard/backtest_live_isolation_test.go` +
> `pkg/backtest/qt51_equivalence_test.go`。设计与取舍见
> `docs/design/qt-decoupling/qt51-execution-port.md`。
>
> **通道切换完成（SVC-CORE-BROKER-RETIRE 步骤 B，2026-11-10）**：broker HTTP 客户端
> 统一到 `pkg/brokerhttp` —— `BrokerHTTPPort` 实现 `ExecutionPort`（`POST/GET/DELETE
> /api/broker/orders*`，wire 形状用 `tellurion-contracts/broker` 的契约类型），
> `Trader` 实现 `trading.LiveTrader`（订单 / 持仓 / 账户 / 紧急清仓）。装配不再有
> 双实现选择：`BROKER_URL` 未配置即 **fail-loud**（进程内 MockTrader 已删除，
> 没有回退路径）；配了 URL 但缺 `BROKER_SERVICE_TOKEN` / `BROKER_ACCOUNT_ID`
> 同样装配失败（fail-closed）。持仓 / 结算 / 紧急清仓仍在 core 运行域。
>
> **内存网关边界（2026-11-11）**：`tellurion-broker` 的 `cmd/brokergateway`
> 目前只有内存 Mock 通道，而 core 从 `BROKER_URL` 无法识别对端性质。通道
> 因此是**启动期必填声明**：`BROKER_CHANNEL=mock|live`；选 `mock` 还必须
> 同时给 `BROKER_ALLOW_MOCK=true`（双键确认），否则装配失败。`mock` 会打
> 一条 WARN（订单永不触达真实柜台），`live` + 回环地址也会 WARN（提醒别把
> dev 的 `:8091` 当成真实柜台）。
>
> **声明必须可验证**：装配时还会 `GET <BROKER_URL>/version` 读网关自报的只读
> `channel`，与声明比对；`live` 对 `mock`、对端不可达、非 2xx、缺 `channel`
> 一律 **fatal 拒绝启动**（fail-closed）。理由是 core 没有第二个信息源，
> 「无法验证」若降级为 WARN，一个老版本/配错的网关就能静默冒充真实柜台。
> 实现：`pkg/brokerhttp.trader.go` 的 `resolveChannel`（声明）与
> `verifyPeerChannel`（对端核对）；broker 侧字段在
> `cmd/brokergateway/main.go` 的 `/version`。

> **回测持久化端口边界（SVC-CORE-RUN-RETIRE ③，2026-11-10）**：`pkg/backtest`
> **不 import `pkg/storage`**；因子缓存、基本面 PIT、复权基准、run 留痕四项
> 能力收成一个组合端口 `backtest.Storage`（`pkg/backtest/ports.go`），由装配层
> 注入。两侧逐字段共用的行形状（`RunProvenanceRecord` / `AdjustmentAnchor`）
> 放在叶子包 `pkg/persistence`，`pkg/storage` 以类型别名继续导出旧名——
> 生产实现 `*storage.PostgresStore` 因此结构化满足该端口，无转换层。编译期
> 自证在 `cmd/analysis/wiring`（`_ backtest.Storage = (*storage.PostgresStore)(nil)`），
> 结构护栏 `internal/repoguard/backtest_storage_decoupling_test.go`。

---

## 6. 端口速查

| 端口 | 归属 |
|---|---|
| 5432 | PostgreSQL（宿主机） |
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
（`tellurion:pkg/domain/factor_def_seed.go`，编译期常量；CT-06-B 2026-11-10 自契约仓迁回）+ 一张持久表
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
                              factor_cache（PG 单层，按需求值）
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

_Last updated: 2026-11-10（SVC-CORE-RUN-RETIRE ③：§5 增补回测持久化端口边界——`pkg/backtest` 不 import `pkg/storage`，共用行形状下沉 `pkg/persistence`，装配层编译期自证；此前 2026-10-26（QT-51 / BE-02：§5 增补回测/实盘执行语义边界——`pkg/backtest` 不 import `pkg/trading`，出站契约 `execution.ExecutionPort`，适配层在 `cmd/analysis/wiring`；此前 2026-10-21 system-spec-review 订正：compose 服务计数 4→3、§3 补 market/audit schema 分区并如实登记 3 处模块内自建表、D 编号引用改指数据因子蓝图（ADR-031 本体 D1–D5 另有其义）；REV-14：删掉手抄表数快照，只留 grep 口径；此前 §7 增补新数据源接入样板 ADR-031 D5；§7 增补多频率进策略视野 ADR-031 D6；§9 AI 因子挖掘流水线数据流 ADR-031 D8；2026-10-14 §7 增补 connector 注册契约上游闸门 ADR-031 D2；§7 规范层数据流 ADR-031 D1 + §8 因子定义库数据流 ADR-031 D3）_
_维护规则：服务/端口/schema 分区变更时更新本文件（AGENTS.md Rule 1）；表数不手抄，只给 grep 口径。_
