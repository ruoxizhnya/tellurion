---
status: active
last-verified: 2026-11-11
verified-by: 12 仓代码与文档对齐审计·阶段 A 基线（三组会话并行；本文件只覆盖 12 仓级事实与 core 深读，证据口径见 §0）
---

# 12 仓架构基线（2026-11-11）

> 本文件是**审计快照**，不是规范性架构说明。规范性说明仍以各仓 `docs/ARCHITECTURE.md` 为准。
> 阶段 B 修改 core 后，只回填本文件的「证据状态」列，不在此处复制 normative 内容。

## §0 范围与证据口径

- **范围**：12 个独立 git 仓的职责边界、跨仓调用、数据所有权、对外契约。
- **并行分工**：本会话负责「12 仓基线 + core 仓」；`agent/kit/contracts/docs/ui/orchestration` 与 `data/analytics/broker/risk/strategy` 由另外两组会话各自对齐，本文件只记录其**文档声称**的接口事实。
- **证据标签**：
  - `[代码]` = 本次在源码 / 配置 / DDL 中直接看到（附路径）。
  - `[文档]` = 只在 markdown / yaml 注释中声称，未与代码对拍。
  - `[冲突]` = 两处证据互相矛盾（列入 §5）。
- **「零调用 / 未接线」结论纪律**（吸取 strategy `core 零调用` 误判教训）：任何此类结论必须同时给出 ① 搜索模式清单 ② 命中文件:行 ③ 反向破坏验证方式；缺一不可写入本基线。

## §1 12 仓职责与边界

| 仓 | 一句话职责 | 明确不负责 | 证据 |
|---|---|---|---|
| `tellurion`（core） | API 网关 + 回测/批跑/因子/风控/执行/合规/对账主体；HTTP `:8085`，另有 `cmd/worker`（编排消费端，无监听端口）、`cmd/dualrun`、`cmd/recon`、`cmd/tmpdrill` | 不做数据抓取与 `market`/`ingest` 写入；不做策略资产版本管理；不做前端；不做券商通道 | `[代码]` `cmd/` 目录实为 analysis/dualrun/recon/tmpdrill/worker，无 `cmd/data` |
| `tellurion-data` | 统一数据协议 + connector 归一化 + 数据集服务；`market`/`ingest` schema 唯一写者，`POST /ingest/points` 唯一写端点，`GET /datasets/*` 读面；默认 `:8081` | 不做逐 bar/逐笔远程取数以外的计算、不做策略/因子计算、不存回测结果、不做跨 schema join | `[代码]` `cmd/dataservice/main.go`；`[文档]` `docs/adr/0006` |
| `tellurion-analytics` | 只读分析服务：基准、归因、敏感性、蒙特卡洛、相关性、报告；默认 `127.0.0.1:8087`，拥有 `analysis` schema | 不做数据抓取、不做回测、不做因子研究、不做实时分析 | `[代码]` `cmd/analyticsservice/main.go`、`internal/httpapi/api.go` |
| `tellurion-broker` | 交易通道网关：`XTPTrader` / `MockTrader` 同一 port 两实现；默认 `127.0.0.1:8091`，`/version.channel` 暴露通道性质；幂等映射落 `broker` schema（配 DSN 时） | 不做风控、不做策略、不做撮合、不持有订单真相、不做资金划转 | `[代码]` `cmd/brokergateway/main.go`、`internal/httpapi/httpapi.go` |
| `tellurion-risk` | 风控规则真相源 + 聚合监控与熔断；同步裁决仍在 core 进程内；默认 `127.0.0.1:8089` | 不做同步裁决服务、不持有订单/持仓、不做券商对接、不做回测 | `[代码]` `cmd/riskservice/main.go`、`internal/httpapi/api.go` |
| `tellurion-strategy` | 策略资产服务：定义/版本/artifact/resolve/promote；默认 `127.0.0.1:8090`；执行不在这里 | 不做策略运行时、不做回测 | `[代码]` `cmd/assetservice/main.go`、`internal/httpapi/api.go` |
| `tellurion-agent` | AI 实验员层：实验循环、基因池、验证与因果判定、工具编排、Copilot；默认 `0.0.0.0:8086`；拥有 `ai` schema | 不做引擎、不做数据抓取、不做前端、不做策略版本管理、不做实盘下单 | `[代码]` `cmd/ai/main.go`、`config/ai-service.yaml` |
| `tellurion-orchestration` | 编排服务：只派活不裁决；artifacts/parents/provenance/workers HTTP 面；无固定默认端口，由部署注入 | 不执行回测、不生成 AI 内容、不写业务表 | `[文档]` `docs/adr/0001`；路由无 `api-schema.md`，`[文档]` |
| `tellurion-contracts` | 跨仓 DTO / 枚举 / ABI 定义（domain、broker、run、risk、engine、fees） | 契约只增不破且零实现；不提供服务、不暴露路由、不写库 | `[文档]` `docs/adr/0001` |
| `tellurion-kit` | 通用基础设施库：鉴权/授权、HTTP client/server、DB 连接与迁移、日志、观测、沙箱 | 无业务概念、无业务表、无业务路由 | `[文档]` `docs/adr/0001-零业务概念` |
| `tellurion-ui` | 前端 SPA，消费各后端 HTTP API | 不做后端、不暴露 API、不直连 DB、不执行回测 | `[文档]` `docs/adr/0001-openapi-单一真相且不直连-db` |
| `tellurion-docs` | docs-only：跨仓架构、服务索引、系统设计与 ADR | 不实现服务、不写库、不依赖 Go 契约包 | `[文档]` `docs/REPO-MAP.md`、`adr-036` |

## §2 跨仓调用矩阵

| 调用方 → 被调方 | 通道 | 配置键（env 覆盖） | 默认 | 证据 |
|---|---|---|---|---|
| UI → core | HTTP `/api/*` | 部署侧代理，仓库内无固定键 | — | `[文档]` |
| core analysis → data | HTTP 反向代理族 `/api/sync/*`；取数 provider | `data_service.url`（`DATA_SERVICE_URL`） | `http://localhost:8081` | `[代码]` `config/analysis-service.yaml:117-122`、`cmd/analysis/handlers_proxy.go:32` |
| core analysis → agent | HTTP 反向代理 `/api/{copilot,pipeline,explore,tools}` | `ai_service.url`（`AI_SERVICE_URL`） | `http://localhost:8086` | `[代码]` `config/analysis-service.yaml:127-128`、`README.md:20-21` |
| core analysis → risk（同步 resolve） | HTTP POST | `risk_service.resolve_url`（`RISK_SERVICE_RESOLVE_URL`）、`resolve_enabled` | `http://localhost:8089`，enabled=true | `[代码]` `config/analysis-service.yaml:188-191` |
| core analysis → risk（事件回传） | outbox → HTTP | `risk_service.events_url`（`events_enabled` 默认 false） | 空 | `[代码]` `config/analysis-service.yaml:211-220` |
| core analysis → strategy | HTTP resolve | `strategy_asset.resolve_url`（`STRATEGY_ASSET_RESOLVE_URL`） | `http://localhost:8090`，enabled=true | `[代码]` `config/analysis-service.yaml:154-161` |
| core analysis → broker | HTTP 下单/查单/持仓 | `BROKER_URL`、`BROKER_CHANNEL`、`BROKER_SERVICE_TOKEN`、`BROKER_ACCOUNT_ID` | 无默认，构造期 fail-closed | `[代码]` `README.md:91`、`[文档]` `pkg/brokerhttp/trader.go` |
| core worker → orchestration | HTTP 领取任务 + 回写结果 | `ORCHESTRATION_URL` / `-orchestration-url`；`ORCHESTRATION_SERVICE_TOKEN` | 无默认，两者皆空即拒绝启动 | `[代码]` `cmd/worker/main.go:48-59,194-197` |
| agent → core | HTTP（回测/walk-forward/表达式/因子） | `ai_service.factor_api_url` | `http://127.0.0.1:8085` | `[文档]` agent `docs/ARGITECTURE.md`/`config/ai-service.yaml` |
| analytics → core / data / orchestration / artifact | HTTP | `ANALYTICS_CORE_URL`、`ANALYTICS_DATA_URL`、`ANALYTICS_ORCHESTRATION_URL`、`ANALYTICS_ARTIFACT_URL` | 代码有默认，见各仓配置 | `[代码]` `cmd/analyticsservice/main.go:31-57` |
| data → 外部数据源 | HTTP（Tushare 等） | `DATA_*` / token 键 | — | `[文档]` |

**启动期门禁**：core → data 地址必须为绝对 URL（`pkg/bootstrap/endpoints.go` 校验，空/无 scheme/无 host 即拒绝启动）；core → broker 构造期读对端 `/version.channel`，不一致/不可达/缺字段即拒绝启动。这两条是**有意 fail-closed**，不是缺陷。

## §3 数据所有权矩阵

| schema | 唯一 writer | 只读消费方 | 证据 |
|---|---|---|---|
| `market` | `tellurion-data`（例外：`market.index_daily` 归 core） | core、analytics、agent、strategy | `[文档]` data `docs/db-schema.md`、`docs/adr/0006` |
| `ingest` | `tellurion-data`（原始响应归档 / 投点） | core（经 HTTP，不直连） | `[文档]` data `docs/db-schema.md` |
| `public` | core（作业、因子、策略、告警、对账、迁移账本等） | 其他仓只读 | `[文档]` core `docs/SPEC.md` §数据归属 |
| `quant` / `research` / `audit` | core（若启用） | 其他仓只读 | `[待 B3 核实]` |
| `ai` | `tellurion-agent` | 无 | `[文档]` agent `docs/db-schema.md` |
| `analysis` | `tellurion-analytics` | 无 | `[文档]` analytics `docs/db-schema.md` |
| `risk` | `tellurion-risk` | 无 | `[文档]` risk `docs/db-schema.md` |
| `strategy` | `tellurion-strategy` | core（经 HTTP resolve） | `[文档]` strategy `docs/db-schema.md` |
| `run` | `tellurion-orchestration` | core（经 HTTP） | `[文档]` orchestration `internal/pgstore` |
| `broker` | `tellurion-broker`（仅在配 `BROKER_DATABASE_DSN` 时写幂等映射） | 无 | `[文档]` broker `docs/db-schema.md` |
| 无 DB | ui / kit / contracts / docs | — | `[文档]` |

**红线**：跨 schema 只读表清单由 core `AGENTS.md` 与 `docs/SPEC.md` §数据归属共同约束；生产代码不得 import 他仓 storage 包（各仓 `internal/repoguard` 有护栏）。

## §4 对外契约清单

### 4.1 HTTP 路由（按提供方）

- **core**：`docs/openapi.yaml` 现有 **95 个 path 条目**。族路由：`/api/sync/{path}`（透传 data）、`/api/explore`、`/api/explore/{path}`、`/api/tools`、`/api/tools/{path}`（透传 agent）；单条外部托管契约：`POST /api/ingest/raw`、`POST /api/ingest/equitydeep`（`x-served-by` 非本进程，见 `cmd/analysis/routes_contract_rev13_test.go:281-284`）。
- **data**：`GET /datasets/resolve`、`GET /datasets/{hash}`、`POST /ingest/points`，加 G3 只读兼容端点与 `/api/sync/*` 作业/排程端点。[文档]
- **analytics**：8 个分析端点（metrics、benchmark、attribution、sensitivity、montecarlo、correlation、report 查询、report 导出）。[代码]
- **broker**：`/api/broker` 下订单、撤单、查单、持仓、账户、成交、对账、紧急清仓，另有 `/version`。[代码]
- **risk**：规则管理、resolve、快照、审计、事件回传、监控、熔断。[代码]
- **strategy**：定义、源码、编译、校验、deprecated、版本列表、promotions、build job、resolve、artifact 下载。[代码]
- **agent**：`/api/explore/*`、`/api/copilot/*`、`/api/pipeline/*`、`/api/tools/*`、`/health`、`/metrics`。[文档]
- **orchestration**：artifacts、parents、provenance、workers；该仓无 `api-schema.md`，契约仅存在于代码与 `docs/ARCHITECTURE.md`。[文档]
- **ui**：前端路由（`web/src/router`），非 HTTP API。

### 4.2 事件

- core `alert_outbox` → risk `/api/risk/events`（`events_enabled` 默认 **false**，出厂不启用）。
- orchestration 任务 → core `cmd/worker` 轮询/租约（HTTP，无消息中间件）。
- 无其他跨仓事件总线。

### 4.3 DTO 来源

- **唯一来源仓**：`tellurion-contracts`（`domain/*`、`broker`、`run`、`risk`、`engine`、`fees`）。
- 各仓 `go.mod` 以版本 pin 依赖，禁止本地 `replace ../tellurion`（core `check_repo_autonomy.py` 护栏）。
- `canonical JSON` / ABI 兼容性由 contracts 仓的 conformance 语料守护。

## §5 阶段 A 发现的全局性矛盾

| ID | 矛盾 | 可能过错方 | 处置 |
|---|---|---|---|
| G-01 | core `docs/ARCHITECTURE.md:51-52,144-145,222`、`README.md:26,179` 把 `data-service :8081`（`cmd/data`）写成运行中进程；实际 `cmd/` 无 `data` 目录 | **文档过期**（代码已完成 `SVC-CORE-DATA-RETIRE`） | B5 修 core 文档；不改代码 |
| G-02 | `config/data-service.yaml` 仍存在，但 Go 代码零读取（搜索模式：`data-service\.yaml`、`data_service\.yaml`，**命中仅 SPEC 与 yaml 自身注释**）；`docs/SPEC.md:3677` 仍指向 `cmd/data` | **两者都过时** | B2 判定删除或标注为历史残留，并同步 SPEC |
| G-03 | core `docs/ARCHITECTURE.md:71` 称 `POST /api/ingest/raw` 是 class A「唯一外部写入门」；data ADR-0006 称 `POST /ingest/points` 是唯一写端点，且 `ingest` schema 归 data | **跨仓契约争议** | 只报方案，不擅自改；需三组会签 |
| G-04 | core `docs/ARCHITECTURE.md:72` 称 data-service 写 `public` 规范化表（stocks/trading_calendar/splits/dividends/index_constituents）；data 仓称这些表在 `market` schema 且自己是唯一 writer | **跨仓契约争议** | 报方案；以 data `db-schema.md` + 迁移真实 schema 为准 |
| G-05 | core `docs/ARCHITECTURE.md`/`README.md` 称「compose 只编排 3 个服务、PG 不在容器内」；`docker-compose.services.yml` 已含 6~7 个服务与 postgres | **文档过期** | B5 修 core 文档，写清两个 compose 文件各自的职责 |
| G-06 | 同一批路由（`/api/copilot/*`、`/api/pipeline/*`、`/api/explore`、`/api/tools`）同时被 core `openapi.yaml` 与 agent `docs/api-schema.md` 声明；agent 又称 core `openapi.yaml` 是权威 | **双真相源** | 报方案：core openapi 只保留「反代入口 + `x-served-by`」，详细 schema 指向 agent 仓 |
| G-07 | `config/analysis-service.yaml:171` 保留 `risk_service.url: http://localhost:8085`（legacy fallback，指向自身）；文档未说明其与 `resolve_url:8089` 的关系 | **文档缺口** | B2 核实该键是否仍被读取；无读取点则标注 legacy |
| G-08 | core `cmd/worker` 无 `ORCHESTRATION_URL` 即拒绝启动，但 core 文档未把该部署前置写成约束 | **文档缺口** | B4/B5 写入 dataflow 与部署说明 |
| G-09 | core `docs/TASKS.md` 已记 `SVC-CORE-DATA-RETIRE` 收口，但同仓 `ARCHITECTURE.md` 仍把 data 面列为本仓组件 | **同仓自相矛盾** | B5 一次修净 |

## §6 仅文档声称、未代码核实（不计为已验证）

1. orchestration 的全部 HTTP 路由与 `run` schema 表清单（该仓无 `api-schema.md` / `db-schema.md` 细目）。
2. broker `broker` schema 的具体表名与幂等键。
3. risk `risk` schema 的具体表名（规则、快照、审计、事件 outbox）。
4. core 的 `quant` / `research` / `audit` schema 是否仍启用及其表清单。
5. ADR-036 的 G0~G3 就绪闸门在运行期是否有强制点（本会话只读到文档描述）。
6. data 的 G3 只读兼容路由的实际行为（是否只读、是否返回 410）。
7. agent `ai.research_*` 三张表的读写权限划分。
8. `tellurion-data` 的 `market.index_daily` 例外归属仅在文档中出现，未核对 core 侧 DDL 是否真的唯一写。

> 上述每项在阶段 B（core 部分）能核实的会回填；不属于 core 的保持 `[文档]`，交由对应组会话核实。

## §7 本文件的维护

- 阶段 B 每批提交后，只更新相关行的证据标签与 §5 状态列。
- 发现新的全局矛盾时追加 G-xx，不改写既有编号。
- 本文件不替代 `docs/ARCHITECTURE.md`；后者是 core 的规范性说明，本文件是跨仓审计快照。
