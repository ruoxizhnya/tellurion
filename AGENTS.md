# Quant Lab — Agentic Coding Configuration

> **版本**: v3.4 (基于 AGENTS Template v2.0 迁移)
> **最后更新**: 2026-09-21 (ADR-023/024 全量反映: §1 三角色定位 / §2 三层模型 L1-L3 + 执行载体 / §3 目录归属 / §7 数据归属 / §10 Rule 1 触发表 / §11 表数口径; 详见 PRODUCT.md（`tellurion-docs:docs/PRODUCT.md`）)
> **适用项目**: A-share quantitative research platform (Quant Lab — 单人自托管研究实验室)
>
> 本文件为 AI 编码助手提供项目上下文。阅读本文件即可快速理解项目全貌。

---

## 0. 分仓拓扑：我在哪（2026-10-20 起）

本项目拆成 **12 个仓**。仓名与 module path 的唯一真相源是
[`repo-inventory.md`](docs/design/qt-decoupling/repo-inventory.md)；**动手前先确认自己要改的是哪个仓**。

| 仓 | 一句话 | 与本仓的关系 |
|---|---|---|
| **`tellurion`（本仓）** | 回测引擎 / 策略运行时 / 撮合 / 组合结算 / 仪器域存储 / 内核装配 | 热路径全在这里，**不出进程** |
| `tellurion-contracts` | port / DTO / 值对象 / 四契约 / 哨兵错误 | 本仓以**版本依赖**使用（S1 起 `require` 伪版本，**禁 `replace`**） |
| `tellurion-kit` | logging / errors / decimal / db / http 等横切工具 | 同上 |
| `tellurion-agent` | AI 实验员（探索 / 生成 / pipeline / 工具注册表） | 只经 **HTTP** 调本仓；本仓对 AI 四族路由做纯反代 |
| `tellurion-ui` | Vue 3 前端 | 只消费本仓 HTTP |
| `tellurion-data` | 数据面唯一入口（connector / aggregator） | 触发后接管 `market` / `ingest`；本仓改为只读 API |
| `tellurion-strategy` | 策略**资产**（定义 / 版本 / artifact） | 执行仍在本仓（表达式 / WASM host） |
| `tellurion-risk` | 规则真相源 + 聚合监控 + 熔断下发 | **同步裁决留本仓**（`RiskPolicy.CheckOrder`，零网络） |
| `tellurion-analytics` | 指标 / 归因 / 敏感性 / 蒙特卡洛 | 只按 `run_id` 读结果，零 import 本仓引擎 |
| `tellurion-orchestration` | job / batch / walkforward 排队与状态机 | 任务状态将从本仓迁出（`strategy_state` → `run`） |
| `tellurion-broker` | 交易通道适配（MockTrader → XTP） | 本仓 `pkg/live` 保留纸交易与实盘**运行域** |
| `tellurion-docs` | 总体设计知识库 | 跨仓的"How & Why"看那里，不要在本仓重复写 |

### 本仓的三条硬规则

1. **本仓是应用，不是库**：module path 是单段 `tellurion`，**任何仓都无法 `require` 它**（Go 要求首段含点）。
   要让别的仓复用能力 → 把**形状**放进 `tellurion-contracts`；不要新建 core client，也不要为了"复用"把业务包导出。
2. **不许在本仓重建已迁出的东西**：`web/`（前端）、`pkg/ai/**`（含 `validation` / `tools`）都不在这里。
   需要改它们就去对应仓。**在 core 新建 `web/` 或 `pkg/ai/` 一律视为边界错误。**
3. **DDL 主路径**：建表走 `pkg/storage/postgres.go` 的 `migrationStatements()` 数组（`migrate()` 启动时按序执行）。
   **唯一真相，无例外**（SPEC-R-01 收编）：`audit.message_log`（Migration 054）、`quant.strategy_state`（Migration 055）、
   `quant.external_signals`（Migration 048）的 DDL 文本只在 `pkg/storage/postgres.go` 定义；
   `pkg/eventstore` / `pkg/strategy` 侧的变量是 `storage.*SchemaDDL` 的**别名**，其 `EnsureSchema` 仅作
   「独立可安装 / 测试自证」幂等兜底入口。新增 DDL 一律加到 `migrationStatements()`。
   `migrations/` 与 `docs/migrations/` 的 `.sql` **不执行**，只作历史证据。

---

## 1. 项目概述

> **顶层定位（ADR-023, 2026-09-16）**: 这是一间**单人自托管的量化研究实验室**，不是 SaaS。三个角色：
>
> | 角色 | 谁 | 职责 |
> |---|---|---|
> | **实验室主任** | 人（若曦） | 给方向、批阅、拍板。**不操作仪器** |
> | **AI 实验员** | Hermes agent | **操作底座**：挖因子、调系数、跑回测、看结果、决定下一步 |
> | **验证器** | validation subagent | **主动证伪**，被调用、不自主循环；输出概率估计 + 质疑清单 |
>
> **AI 是操作员，不是生成器。** 底座是仪器，已存在且可用；缺的是"操作它的人"和"看见他在干什么的窗"。
> 产品定义见 **PRODUCT.md（`tellurion-docs:docs/PRODUCT.md`）**；架构决策见 **ADR-023（`tellurion-docs:docs/adr/adr-023-ai-experimenter-lab.md`）**（取代 ADR-022/021 —— 两者均为 Proposed 期间即被取代、从未实施，已入 `docs/archive/superseded-adr/`）。
>
> **单人自托管的前提**：多租户 / RBAC / 计费 / 限流 / 高可用都是**纯负担，可砍**。

**Quant Lab** 由 12 个仓组成：core（本仓，Go）+ 前端仓 + AI 仓 + 契约/工具库 + 6 个触发式服务仓 + 文档仓。

- **语言**: Go 1.25（本仓）。TypeScript + Vue 3 在 `tellurion-ui` 仓 —— **本仓没有前端代码**
- **当前版本**: Phase 4 (AI-Native Evolution)；ADR-023 三层模型已落盘 —
  AI 编排层（`pkg/ai/loop` 循环控制器 + `experiments` / `factor_hypothesis` 实验日志表）、
  能力层（MCP 工具桥 + 五维确定性校验器 + 因果维）——**这三块代码 2026-10-09 起在 `tellurion-agent` 仓**
  （`agent:pkg/ai/loop`、`agent:pkg/ai/validation`、`agent:pkg/ai/tools`、`agent:pkg/ai/causal`）；
  数据层（行情 / 财务 / 产业链 / 研究洞察）
- **状态**: 底座核心功能已完成；**执行载体已定型为表达式**（ADR-024（`tellurion-docs:docs/adr/adr-024-expression-as-execution-target.md`））——
  LLM 生成的 Go 代码只是**可审阅 artifact**，不加载、不执行
- **入口**: `cmd/analysis/main.go`（:8085 主 API）, `cmd/data/main.go`（:8081 数据服务）, `cmd/dualrun/`（双跑对账）
  —— 前端入口 `web/src/main.ts` 已随前端拆到 `tellurion-ui` 仓，本仓没有它
  - `cmd/ai` (AI 服务 :8086) —— **2026-10-09 起 AI 层整体迁往 `tellurion-agent` 仓**（AI 拆仓，ADR-027 §5 第 8 步前置切片）：`pkg/ai` + `CopilotService` + 四族 AI handler（copilot/pipeline/explore/tools）现在住在 agent 仓；本仓的 analysis-service 对这四族路由做纯反代（`handlers_ai_proxy.go`），前端与 openapi 契约不变。依赖方向单向 agent→core（repoguard 钉住）
- **构建**: `go build ./...`（本仓）。前端构建（`npm run build`）在 `tellurion-ui` 仓

### 技术栈

| 组件 | 技术 | 版本/端口 |
|------|------|----------|
| 后端 API | Go + Gin | :8085 |
| 前端 SPA | Vue 3 + Naive UI | :5173 (dev) —— **不在本仓**（`tellurion-ui`） |
| 数据服务 | Go + Gin | :8081 |
| **AI 服务** | Go + Gin | :8086（**源码在 `tellurion-agent` 仓**，2026-10-09 AI 拆仓；compose 经 additional_contexts 构建） |
| **风控 + 执行** | **in-process** | **合并到 analysis (per ODR-021, P1-15)** |
| 数据库 | PostgreSQL | :5432 |
| 缓存 | Redis | :6379 |

> **CR-36 (ODR-012)**: previously only analysis/data/strategy/AI were listed.
> risk-service(8083) and execution-service(8084) were added in CR-36.
>
> **ODR-021 (P1-15, 2026-06-12)**: risk-service + execution-service have
> been **merged into analysis-service** as in-process components
> (`risk.RiskManager` + `live.MockTrader`). Docker compose service
> count reduced 7 → 5. Risk endpoints exposed at `/api/risk/*` and
> execution endpoints at `/api/execution/*` (both with legacy aliases).
> See ODR-021（`tellurion-docs:docs/archive/odr/odr-021-p1-15-service-merge-risk-execution.md`）.

---

## 2. 架构概览

> ⚠️ **本节的框图是"拆仓前的整体服务视角"**，用来讲清系统怎么搭，**不是本仓的目录说明**。
> 本仓只实现其中两件：**analysis-service（:8085）** 与 **data-service（:8081）**；
> 前端（Browser）在 `tellurion-ui`，AI 服务（:8086）在 `tellurion-agent`，其余能力服务尚未立项。
> 看本仓实际目录请直接跳 §3。

**分层视角（ADR-023，顶层架构）**: 三层。**分界线是「能否无人值守」，不是「静态 vs 动态」**：

```
L3  AI 编排层   AI 实验员（Hermes，唯一编排者，会循环） · 因果审查（被调用，不循环）   ← 唯一需要人在环
L2  能力层      [MCP Tool Bridge] → 因子计算 | 回测 | 产业链查询 | 证据查询 | 验证器链×5   ← 禁止 AI 直接摸数据
L1  数据层      行情 | 财务 | 产业链图谱 | 研究洞察 | 实验日志
```

- **编排者只有一个**。禁止 EquityDeep（或任何其他组件）成为第二个 agent。
- **能力层存在的意义是拦住 AI 直接读库/读文件**。L1/L2 给定输入必有确定输出、可自动测试；L3 会失败，必须可监督。
- **验证器链的六个维度里五个是纯计算**（统计 / 经济 / 稳健 / 偏差 / 冗余，在 **`tellurion-agent` 的 `pkg/ai/validation`**），只有**因果**一项需要语言模型（`agent:pkg/ai/causal`）——**这两块都不在本仓**。所以验证器不是「第二个 agent」，而是**被调用的确定性函数** —— 实现成本与不确定性都低一个量级。
- **拒绝多 agent 投票与 arbitrator 仲裁**：错误相关会放大偏差；仲裁层不可解释，违背可监督性。
- **目标是可校准，不是准确**：输出「未来有效的概率 X%」，考核**校准误差**（声称 60% 的应实际命中 50–70%），而非推荐准确率。校准良好的信心才能用于配仓位。
- **EquityDeep 从「工作面」降为「数据底座」**，拆成两块：**产业链图谱**（公司-环节归属 / 上下游映射 / 传导指标，可当因子）与**研究洞察**（"为什么值得关注"的自然语言，供 AI 生成假设时检索）。纵向深研从「研究的前置步骤」变为「被异常触发的按需动作」。

> 详细定义见 PRODUCT.md（`tellurion-docs:docs/PRODUCT.md`）（顶层产品）→ ADR-023（`tellurion-docs:docs/adr/adr-023-ai-experimenter-lab.md`）（架构决策）。下列服务框图是**当前已实现形态**。

**执行载体（ADR-024）**: 一条意图的落地路径固定为

```
自然语言 → intent（语义类型）→ YAML 配置 → ExpressionStrategy → GlobalRegister → 回测
```

回测跑的是**表达式引擎**算出的信号，不是 LLM 写的 Go 代码。LLM 生成的 Go 代码继续**真编译校验**，结果留在 `result.GeneratedCode` / `result.BuildError` 里给人看，但**不加载、不执行**，失败也**不阻断**实验。能用价量表达的意图类型在 `defaultSignalExpression()` 里有确定性默认表达式（`momentum` / `mean_reversion` / `trend_following` / `breakout` / `multi_factor`）；`value` / `quality` 给不出默认表达式，**明确失败**而不是套一个无关的价格表达式。

> **不做 `plugin.Open`**（ADR-024 的主要「不做」记录）：Windows 根本不支持 Go plugin（`-buildmode=plugin` 仅 Linux / macOS / FreeBSD），而开发环境就是 Windows —— 本地无法验证的加载路径不配当主链路；依赖版本必须逐字节一致，符号冲突难复现难排查；且「让 LLM 写代码再动态加载」= AI 造仪器，与 ADR-023 冲突。**ADR-001 的插件机制本身保留** —— 它服务人工编写的、需要热插拔的策略，与"AI 生成代码"是两回事。

**服务视角（已实现）**: 今天只有 **3 个进程**：`analysis-service`（:8085，同时是 API 网关）、`data-service`（:8081）、`ai-service`（:8086，源码在 `tellurion-agent` 仓）。analysis 对 data 做反代/调用，对 ai 四族路由做纯反代。**`strategy-service`（:8082）已于 REV-16 删除**（ADR-012 关闭），别再按"三个后端服务"理解。

```
Browser (Vue SPA :5173)
  │
  ├──► GET /api/health        ──► analysis-service :8085
  ├──► POST /api/backtest     ──► analysis-service → engine.go → postgres
  ├──► GET /ohlcv/:symbol     ──► analysis-service → data-service :8081 (proxy)
  ├──► GET /api/strategies    ──► analysis-service → StrategyDB (local)
  ├──► POST /api/walkforward  ──► analysis-service → WalkForwardEngine → postgres
  ├──► POST /api/batch/backtest ──► analysis-service → BatchEngine → postgres
  ├──► GET /api/sync/stream   ──► analysis-service → SSE (real-time progress)
  ├──► GET /api/sync/status   ──► analysis-service → SyncJobManager → postgres
  ├──► POST /api/sync/jobs    ──► analysis-service → SyncQueue → WorkerPool
  ├──► GET /api/datasource/status ──► analysis-service → DataAdapter
  ├──► GET /api/plugins       ──► analysis-service → PluginLoader
  │
  └──► Redis (:6379) ◄──── factor_cache, session store, sync_job_cache
              │
              └──► PostgreSQL (:5432) ◄── stocks, ohlcv, fundamentals,
                                          backtest_jobs, sync_jobs, sync_schedules

  data-service :8081 (tushare data ingestion)
  （strategy-service :8082 已于 REV-16 删除，ADR-012 关闭）

  AI 实验员 (Hermes, L3)
    │  经 MCP 工具桥 (L2, 21 个工具) 操作底座 —— 禁止直接读库/读文件
    │  挖因子 → 调参数 → 跑回测 → 看结果 → 决定下一步（会循环）
    └──► 被调用的验证器 (validation subagent): 提案 → 一次运行 → 概率 + 质疑清单
```

> **数据归属（禁止双写）**: 可重建数据物理唯一于 PG —— A 原始源响应 `ingest.raw` /
> B 规范化 `market.*` / C 派生计算 `quant.*` + Redis `factor_cache`；
> 不可重建的研究叙事唯一于 vault markdown；`research.*` 是 markdown 的可重建投影
> （可 DROP 重建）。共享靠**同一份数据 + `content_hash` 坐标**，不靠复制。
>
> `ingest.raw` / `market.*` / `quant.*` / `research.*` 各分区**均已落盘**
> （`pkg/storage/postgres.go` 内联 DDL，见 §11）。证据服务：
> `citation = {source, dataset, key, as_of, content_hash}`，
> `GET /api/evidence/{content_hash}` 返回唯一原始记录。

### 关键架构决策

| ADR | 决策 | 核心理由 |
|-----|------|----------|
| ADR-001（`tellurion-docs:docs/adr/adr-001-plugin-loading.md`） | 策略插件动态加载 | Hot-swap，无需重启 |
| ADR-002（`tellurion-docs:docs/adr/adr-002-timescaledb.md`） | TimescaleDB 时序优化 | OHLCV 查询性能 |
| ADR-003（`tellurion-docs:docs/adr/adr-003-background-worker.md`） | 异步任务队列 | 回测不阻塞 API |
| ADR-004（`tellurion-docs:docs/adr/adr-004-scoring-method.md`） | 多因子评分框架 | 策略通用化 |
| ADR-005（`tellurion-docs:docs/adr/adr-005-strategy-config.md`） | 策略配置标准化 | 统一参数接口 |
| ADR-014（`tellurion-docs:docs/adr/adr-014-strategy-framework-refactor.md`） | 策略框架重构 | 消除重复代码，统一接口 |
| ADR-015（`tellurion-docs:docs/adr/adr-015-ai-agent-architecture.md`） | AI Agent 量化研究架构 | AI 作为资深量化研究员 |
| ADR-021（`tellurion-docs:docs/archive/superseded-adr/adr-021-equitydeep-research-layer.md`） | EquityDeep 纵向基本面研究层 | ~~与横截面层正交互补，契约式整合~~ — **Superseded by ADR-023**（Proposed 期间即被取代，从未实施） |
| ADR-022（`tellurion-docs:docs/archive/superseded-adr/adr-022-unified-research-platform.md`） | ~~统一研究平台 — 单一数据面 + 双对等工作面 + 内容坐标证据 + 飞轮闭环~~ | **Superseded by ADR-023**（同上）。方向部分正确（单一数据面 / 内容坐标证据保留），但两个对等工作面导致**两套编排**，且未触及真正的第一性原理：**AI 的可监督性** |
| ADR-023（`tellurion-docs:docs/adr/adr-023-ai-experimenter-lab.md`） | **AI 实验员 + 人类监督者实验室** — 三层模型（AI 编排 / 能力 / 数据）+ EquityDeep 降为数据底座 + 挖掘/红队双 agent + **校准优于准确率** | 编排者从 2 个收敛到 1 个；可监督性成为一等需求 |
| ADR-024（`tellurion-docs:docs/adr/adr-024-expression-as-execution-target.md`） | **策略执行载体是表达式，LLM 生成的代码只是 artifact** — 意图类型 → 确定性默认表达式映射；不做 `plugin.Open` | 起点确定、可审阅、可复现；**自由度在这里是负债** |

详见: ADR.md（`tellurion-docs:docs/ADR.md`）（共 24 条 ADR：ADR-001~024）

---

## 3. 目录结构

```
tellurion/                        # core —— 热路径全在这里，不出进程
├── cmd/
│   ├── analysis/                 # 主 HTTP 服务 (:8085)：API + 内嵌前端静态资源 + AI 四族路由纯反代
│   ├── data/                     # 数据服务 (:8081)：同步 / connector / ETL
│   └── dualrun/                  # K15 双跑对账 = 引擎对齐门禁（2026-10-20 裁决**留 core**，见 repo-inventory §3）
├── pkg/
│   ├── api/                      # HTTP 层（gin 路由与中间件）
│   ├── bootstrap/                # 启动装配（配置 / 监听 / 依赖注入）
│   ├── kernel/                   # 模块化内核装配（Clock / Bus / Store 的 BootOrder）
│   ├── backtest/                 # 引擎 + contracts(四契约) + batch + walkforward + metrics
│   ├── strategy/                 # 策略运行时 + 插件 + SignalProvider（留 core）
│   ├── execution/ execalgo/      # 撮合 / 执行算法（留 core）
│   ├── portfolio/ settlement/    # 组合 / 结算（留 core）
│   ├── risk/                     # RiskPolicy 同步裁决（留 core，永不出仓）
│   ├── compliance/ alert/        # 合规检测 + 告警编排（→ tellurion-risk，X3）
│   ├── data/ marketdata/ sync/   # 数据管道（→ tellurion-data，X1）
│   ├── live/                     # 纸交易 / 实盘运行域（通道适配 → tellurion-broker，X6）
│   ├── marketimpact/ indicator/ expression/   # 冲击模型 / 指标 / 表达式 DSL（留 core）
│   ├── fees/                     # 费率模型（形状 → contracts，计算留 core）
│   ├── storage/                  # PG 存储：**DDL 唯一真相 = migrationStatements()**（SPEC-R-01 已收编全部模块表）
│   ├── eventstore/ msgbus/       # 事件存储（audit schema）/ 消息总线（留 core，无出仓计划）
│   ├── domain/                   # 领域模型（→ tellurion-contracts，S1）
│   ├── logging/ errors/ id/ decimal/ statistics/   # 横切（→ tellurion-kit，S1）
│   ├── httpclient/ httpserver/ observability/      # 横切（→ tellurion-kit，S1）
│   ├── auth/ authz/ clock/ sandbox/                # 横切（→ tellurion-kit，S1）
│   └── testenv/ testutil/        # 测试脚手架（留 core）
├── e2e/                          # Go 端到端 / 守卫测试（guard_test.go、integration_test.go）
├── migrations/                   # ⚠️ 历史 SQL 镜像，**不执行**（见该目录 README）
├── contracts/                    # ⚠️ 运行时数据契约资产（field_dictionary.yaml + schema），**不是 Go 包**
├── deploy/                       # 部署编排（compose / Dockerfile / nginx）
├── docs/                         # 本仓文档：SPEC / ARCHITECTURE / TASKS / design/
└── tools/                        # 仓库自查脚本（doc links / repoguard / 契约 pin）
```

**已迁出（不在本仓，别在这里找）**：

| 原路径 | 现在住在 | 依据 |
|---|---|---|
| `pkg/ai/**`（loop / causal / intent / yaml / pipeline / agents / expression / gene_pool / client / metrics / search / evolution / drift / validator / prompts） | `tellurion-agent`（`pkg/ai/**`） | 2026-10-09 AI 拆仓；本仓 `cmd/analysis/handlers_ai_proxy.go` 只做纯反代 |
| `pkg/validation/`（五维确定性校验器）、`pkg/tools/`（MCP 工具桥） | `tellurion-agent`（`pkg/ai/validation`、`pkg/ai/tools`） | 同上 |
| `web/src/**`（Vue 3 前端） | `tellurion-ui`（`web/src/**`） | 2026-10-09 前端拆仓 |
| Playwright E2E（`e2e/tests/*.spec.ts`） | `tellurion-ui` | 同上；本仓 `e2e/` 只剩 Go 守卫与集成测试 |


> **⚠️ `migrations/` 与 `docs/migrations/` 里的 `.sql` 不被执行。**
> **加表 / 加列的唯一路径**是在 `pkg/storage/postgres.go` 的 `migrationStatements()`
> 数组末尾追加一条幂等 DDL（编号续最大的那个；`migrate()` 启动时按序执行）。
> 这套机制**没有版本表、没有 down、没有执行记录** —— 有意取舍，理由见 `migrations/README.md`。散落的 `.sql` 只作历史证据保留，
> 只读、不维护、不进导航 —— 详见 [`migrations/README.md`](migrations/README.md)。
> `pkg/storage/bulk_insert_ddl_test.go` 有防漂移断言：ETL 的 `TableMapper`
> 目标表必须出现在内联 DDL 里。

> **ODR-045 (2026-07-02)**: 原计划的 9 个前端 AI 组件 (FactorLab.vue,
> StrategyWorkshop.vue, EvolutionObs.vue, PipelineDashboard.vue,
> AIResearch.vue, FactorCard.vue, StrategyCard.vue, GenealogyTree.vue,
> FitnessChart.vue) **P1-13 创建后 S7-P2-7 作为死代码删除** — 那个 `web/src/components/ai/` 目录**随前端整体迁到 `tellurion-ui` 仓后已不存在**；前端侧不要再重建它。
>
> **ODR-046 (2026-07-02)**: Hermes Agent 作为自主研究层替代前端 AI UI 方案。
> 研究主路径现为: Hermes → MCP bridge (`pkg/tools/`, 21 个工具) → Go 后端。
> `pkg/ai/agents/` 是 pipeline 的实现细节，**仍在用**；ODR-046 废的是前端 AI UI
> 那条交互路径，不是这批能力（2026-09-18 更正，见该包 `doc.go` 与 TASKS P2-5）。

---

## 4. 角色与边界

### 你是谁
你是一个 **后端** Agent，只做本仓（core）：Go 服务、引擎、存储、API、测试与文档。
**前端在 `tellurion-ui`，AI 在 `tellurion-agent`** —— 两边都不要在本仓动。

### 工作范围（What You Work On）
- **后端**: `cmd/`（analysis / data / dualrun）、`pkg/`（引擎、策略运行时、撮合、结算、存储、内核装配…）
- **测试**: `e2e/`（Go 守卫 + 集成测试；**不是** Playwright）
- **文档**: `docs/`（SPEC / ARCHITECTURE / TASKS / design）
- **ODR**: `docs/archive/odr/`（过程与治理决策）

### 不归本仓（改它们请去对应仓）
| 想改 | 去 |
|---|---|
| 页面 / 组件 / 前端测试 / Playwright | `tellurion-ui` |
| AI 探索 / 生成 / pipeline / 工具注册表 / 验证器链 | `tellurion-agent` |
| 跨仓共享的类型与 port | `tellurion-contracts`（先定形状，再回来改实现） |
| 横切工具（logging / db / decimal / http client…） | `tellurion-kit` |

### 禁区（What You NEVER Modify）
- **在本仓新建 `web/` 或 `pkg/ai/`** —— 它们在 2026-10-09 已拆出，重建等于制造第二个真相
- `node_modules/`, `dist/`, `.vite/` — 自动生成（本仓已无前端；若出现在本仓说明有东西走错地方）
- `.env*` 文件 — 包含密钥
- `migrations/` 与 `docs/migrations/` 的 `.sql` — **历史记录，只读不维护**。加表 / 加列请改 `pkg/storage/postgres.go` 的 `migrate()` 数组（见 [`migrations/README.md`](migrations/README.md)）
- `docker-compose.yml` 基础配置 — 通过 `docker-compose.override.yml` 覆盖
- 二进制文件、编译产物或 vendor 目录
- `docs/archive/` — 归档文档不可变；只新增，不修改。**唯一例外**：失效的**相对链接**可以修（改层级 / 随文件名改名而更新）—— 死链会让归档失去可检索性，而链接不是内容。修的时候要保留原结论、加注说明，不要借机改写历史判断

---

## 5. 命令参考

### Backend (Go)
```bash
go build ./...                          # Build all packages
go vet ./...                            # Static analysis
go test ./pkg/backtest/... -v           # Run backtest engine tests
go test ./pkg/data/... -v               # Run data pipeline tests
go test ./pkg/storage/... -v            # Run PostgreSQL storage tests
```

### 前端 / E2E（**不在本仓**）

本仓没有 `web/`，也没有 Playwright：

```bash
# 前端（install / dev / build / lint / typecheck / vitest / playwright）
#   → 去 tellurion-ui 仓，命令见该仓 AGENTS.md
```

本仓的 `e2e/` 是 **Go** 测试：

```bash
go test ./e2e/...                        # 守卫 + 集成测试
cd e2e && go test ./guard/... -v         # 只跑守卫
```

### E2E Tests (Playwright)
```bash
cd e2e && npx playwright test            # Full E2E suite (all projects)
npx playwright test --project=chrome    # Chrome browser only
npx playwright test --grep "Dashboard" # Run specific tests by name
npx playwright test --report=html      # Generate HTML report
```

### Infrastructure
```bash
docker compose up -d                    # Start all 5 services (ODR-021: risk+exec merged into analysis)
docker compose ps                       # Check service health
docker compose logs -f analysis-service  # Tail analysis service logs
docker compose down                     # Stop all services
```

> **容器数**: 当前 5 个服务（ODR-021 合并 risk+exec 后从 7 降下来）。
> EquityDeep **不再新增 worker 容器** —— ADR-023 已把它从「工作面」降为
> **数据底座**（产业链图谱 + 研究洞察两块，见 §2），其取数经 L1 数据层，
> 由 AI 实验员按需触发。ADR-022 曾计划的 `equitydeep-research` worker 容器
> **已取消**（该 ADR 从未实施）。

---

## 6. 代码规范

### Backend (Go)

遵循 [Effective Go](https://go.dev/doc/effective_go) 规范。

- 使用 `context.Context` 作为 I/O 函数的第一个参数
- 错误处理：返回 error，生产代码中绝不 panic
- 命名：导出符号用 PascalCase，未导出用 camelCase
- 包结构：`domain/` → `data/` → `strategy/` → `backtest/` → `storage/`
- 使用 `logrus` 或标准 `log` 包记录日志（与现有代码一致）
- **库侧测试用 `storage.NewPostgresStoreConnected`（连库 + ping，不迁移），不用 `NewPostgresStore`**。`migrate()` 只由 `NewPostgresStore` 承载，那是**启动 / 部署路径**（`cmd/data/setup.go`、`pkg/bootstrap/engine.go`、`cmd/dualrun`，以及 `//go:build integration` 下 dockertest 一次性容器套件）。DATA-09 的实测代价（2026-10-15）：默认构建的测试直连开发库调迁移入口，一条 `go test ./pkg/data/...` 就把 Migration 054 的整表 ×1000 真的作用在 3,926,510 行上，无授权无提示，且 `data_migrations` 哨兵置位后**不可重放**。双向护栏在 `internal/repoguard/store_migration_isolation_test.go`（测试不许调迁移入口 / 生产不许调不迁移入口 / 两个入口的函数体形状也钉住）。代价如实登记：库侧测试从此**预设库已迁移**，新库先起一次服务或跑一次 tagged 集成套件。

**Canonical Strategy Interface**（单一事实来源）：
```go
type Strategy interface {
    Name() string
    Description() string
    Parameters() []Parameter
    Configure(params map[string]interface{}) error
    GenerateSignals(ctx context.Context,
        bars map[string][]domain.OHLCV,
        portfolio *domain.Portfolio) ([]domain.Signal, error)
    Weight(signal domain.Signal, portfolioValue float64) float64
    Cleanup()
}
```
参见: `pkg/strategy/strategy.go`, [SPEC.md](docs/SPEC.md), VISION.md（`tellurion-docs:docs/VISION.md`）

### 前端（**不在本仓**）

前端代码（Vue 3 / Naive UI / Pinia / Chart.js / Vue Router）整体在 **`tellurion-ui`** 仓，
本仓不写 `.vue` / `.ts`，也没有 `web/` 目录。要改前端 → 去那个仓读它的 `AGENTS.md`。

> 这条以前是本仓的一大段前端规范（组件命名、`shallowRef`、`markRaw`、`nextTick`）；
> 2026-10-09 前端拆仓后它留在 core 只会误导 agent 在本仓找 `web/src/`。原文已随前端迁走。

### 工作类型分类

| 类型 | 判定关键词 | 示例 |
|------|-----------|------|
| 设计 | 设计/架构/方案/选型/ADR | 新策略模块设计 |
| 审计 | 审计/审查/Review/检查 | 代码质量审计 |
| 测试 | 测试/Test/覆盖 | 编写单元测试 |
| 实现 | 实现/开发/编写/修复/重构 | 修复回测 Bug |
| 文档 | 文档/README/API文档/指南 | 更新 SPEC.md |
| 其他 | 配置/问答/调研 | 环境配置调整 |

### 前置动作（开始前必做）

| 任务类型 | 必做事项 |
|---------|---------|
| 设计 | 研究现有架构（[ARCHITECTURE.md](docs/ARCHITECTURE.md)）、查阅 ADR、横向比较方案 |
| 审计 | 通读相关设计文档、审查变更历史、确认技术规范 |
| 测试 | 理解需求（TEST.md（`tellurion-docs:docs/TEST.md`））、分析源码识别边界条件 |
| 实现 | 理解设计文档（[SPEC.md](docs/SPEC.md)）、熟悉编码规范、检查依赖兼容性 |
| 文档 | 对照源码验证一致性、检查格式规范（ADR/ODR 模板） |
| 其他 | 无特定要求 |

### 后置动作（完成后必做）

| 任务类型 | 必做事项 |
|---------|---------|
| 设计 | 更新现有文档（优先）、编写 ADR、记录待办到 TASKS.md |
| 审计 | 输出发现（ODR 格式）、更新任务列表、修复 Critical/High 问题 |
| 测试 | 确保测试通过、审计测试质量（消除无效/低效测试）、更新覆盖率 |
| 实现 | 运行 lint + typecheck、编写/更新测试、确保构建通过、更新相关文档 |
| 文档 | 验证代码一致性、检查交叉引用、更新导航表（AGENTS.md Document Index） |
| 其他 | 无特定要求 |

### 执行规范（Implementation Discipline）

> **来源**: 2026-06-29 ODR-043 综合审计后确立 — 适用于 `docs/TASKS.md` 中所有 S7-* 系列任务及后续新增任务

每个任务在执行过程中**必须**遵循以下三条规范，缺一不可：

#### 规范 1: 测试先行（Test-First）

- **每个任务必须编写对应的测试用例**，包括但不限于：
  - Bug 修复：先写一个能复现 bug 的失败测试，再修复代码使测试通过
  - 新功能：先写测试定义预期行为，再实现功能
  - 重构：确保现有测试覆盖重构范围，重构后测试全部通过
- **测试质量要求**：
  - 必须有行为断言（禁止 `assert.True(t, true)` placeholder）
  - 必须覆盖边界条件（空输入、非法输入、并发场景）
  - 禁止用 `time.Sleep` 同步并发测试，改用 channel/`require.Eventually`/`sync.WaitGroup`
  - 测试名必须描述被测行为（如 `TestT1SellBlockedByQuantityYesterday`）
- **验证标准**: `go test ./... -count=1 -race` 必须通过；前端 `npm test` 必须通过

#### 规范 2: 代码审查（Code Review）

- **每个任务完成后必须进行代码审查**，审查维度：
  - **正确性**: 逻辑是否正确？边界条件是否处理？
  - **可维护性**: 命名是否清晰？函数是否过长（>100 行警示）？参数是否过多（>5 个警示）？
  - **规范遵循**: 是否符合 AGENTS.md §6 代码规范？gofmt 是否通过？
  - **测试质量**: 测试是否有效？是否覆盖关键路径？
  - **文档同步**: 是否需要更新 SPEC.md/ARCHITECTURE.md/AGENTS.md？
- **审查方式**:
  - 自审：提交前自己通读 diff
  - 工具审：`go vet ./...` + `gofmt -l .` + `npm run typecheck`（若可用）
    ✅ **AUD-27 已修（2026-09-22）**：此前 `gofmt -l .` 在**本机**恒列出全部
    Go 文件。根因不是「仓库存了 CRLF」（登记时的实测是错的 —— `git ls-files
    --eol` 实测 558/558 个 .go 的 index 侧都是 `i/lf`，blob 本来就是 LF），
    而是本机 `core.autocrlf=true`（PortableGit 的 etc/gitconfig，**系统级**）
    在 checkout 时把 LF 写成 CRLF。已加 `.gitattributes`（`* text=auto eol=lf`，
    **零 blob 变化**）让工作区在所有平台都是 LF，并清掉被 CRLF 噪声掩盖的
    **32 个**未格式化文件。现在整仓 `gofmt -l .` 可以当结论用，CI 也有门禁。
  - 必要时使用 TRAE-code-review skill 执行结构化审查
- **审查记录**: 审查发现的问题要么当场修复，要么记录为新任务到 `docs/TASKS.md`

#### 规范 3: 原子提交（Atomic Commit）

- **每个任务采用 atomic commit 方式提交代码变更**，含义：
  - **一个任务 = 一个 commit**（或一组逻辑紧密关联的 commit）
  - **每个 commit 必须是可独立构建、可独立测试通过的完整变更**
  - 禁止"提交一半工作"（如修改了 A 但没改依赖的 B，导致编译失败）
  - 禁止"混合多个任务到一个 commit"（如把 bug 修复和重构混在一起）
- **Commit message 格式**:
  ```
  <type>(<scope>): <subject>

  <body — 说明 what/why，不要说明 how>

  Refs: S7-P0-1 (任务 ID from docs/TASKS.md)
  Reviewed: self-review + go vet + gofmt + tests pass
  ```
  - `type`: feat / fix / refactor / test / docs / chore
  - `scope`: 受影响的包或模块（如 `ai/pipeline` / `backtest` / `web`）
  - `Refs`: 任务 ID，便于追溯
- **提交前检查清单**:
  - [ ] `go build ./...` 通过
  - [ ] `go vet ./...` 无警告
  - [ ] `gofmt -l .` 无输出（已格式化）
  - [ ] `go test ./... -count=1 -race` 通过（或受影响包的测试通过）
  - [ ] 前端变更：`npm test` + `npm run typecheck`（若可用）通过
  - [ ] commit message 符合格式
- **分支策略（ODR-067，2026-10-14 起）**: 单人自托管维护期**允许直接提交 `main`**。代价是提交前门禁必须自己当闸：受影响包的 `go test` + `go vet` + `gofmt -l` 当场跑过，一个 commit 只做一件事，且**不得把新红当成既有噪声**（见 ui 仓 `docs/TASKS.md` AUD-61 的登记表机制 —— 红必须登记在案，才能区分「我这条提交弄坏的」与「本来就红的」）。**对外贡献（他人开的 PR）与本仓之外的上游（如 `nautilus_trader`）仍走 PR 工作流，不套用本授权。**

#### 规范适用范围

| 任务类型 | 测试先行 | 代码审查 | 原子提交 |
|---------|---------|---------|---------|
| Bug 修复 (P0) | ✅ 必须复现测试 | ✅ | ✅ |
| 重构 (P1/P2) | ✅ 现有测试不退化 | ✅ | ✅ |
| 新功能 (P3) | ✅ TDD 优先 | ✅ | ✅ |
| 文档同步 | N/A | ✅ 一致性审查 | ✅ 独立 commit |
| 测试补强 | ✅ 被测代码已有 | ✅ 测试质量审查 | ✅ |

#### 违规处理

- 未编写测试的任务：**不算完成**，标记为 blocked
- 未通过代码审查的提交：**不得合并**，返回修改
- 非 atomic commit：**要求拆分**为多个原子 commit 后再合并

---

### 8.5 任务定义规范（派工给独立 coding agent 的标准）

> 目标：**一个独立 coding agent 拿着一份 task，不需要读其他 task、不需要问人，就能完成并自证。**
> 这份规范是定义 task 的**强制标准**——写 task 时逐条对照，缺一条就是 task 没写好（问题在 task，不在 agent）。

#### 8.5.1 独立可运行判据（硬标准，先过这条再谈其他）

一份 task 必须满足：**一个 agent 拿着这份 task + 它显式引用的冻结契约（接口 / 消息 / DB schema / 测试），不需要读任何其他模块的实现、不需要问任何人，就能把活干完并通过 §验收。** 达不到这条，就是 task 没写细——回去补 task，不要怪 agent。（与 [design/kernel/ §5.1 模块边界契约](docs/design/kernel/target-architecture-modular-kernel.md) 同源：那里是模块级，这里是 task 级。）

#### 8.5.2 必备六字段（缺一不可）

| 字段 | 写什么 | 反例（不要这么写） |
|---|---|---|
| **① 目标 Goal** | 一句话、可判定的结果（不是动作，是 outcome） | 「优化回测」「处理一下数据」 |
| **② 上下文 Context** | 相关 `文件:行号`、现有模式、依赖的**冻结契约**（指向 `pkg/<mod>/interfaces.go` / `pkg/kernel/topics.go` / `contracts/schema.sql`）、为什么做 | 只给一句「在回测里改下」让 agent 猜架构 |
| **③ 要求 Requirements** | 必须实现什么：接口签名、数据流、行为清单（具体、可枚举） | 「让它更快」无轴向（ latency? 吞吐? 内存?） |
| **④ 约束与非目标 Constraints & Non-Goals** | **明确「不做什么」**（DO NOT CHANGE 清单：不改公共接口 / 不引入新依赖 / 不动某文件）——AI 不能推断省略的边界 | 不写非目标，agent 顺手重构了稳定代码 |
| **⑤ 验收 Acceptance** | **可机械执行的判据**：哪条测试通过、哪条破坏验证变红、哪条命令输出什么、文档同步到哪 | 「跑一下看看没问题就行」 |
| **⑥ 边界 Scope** | 只做这一个模块 / 这几个文件；超出即另立 task | 一个 task 塞 3 个模块 |

#### 8.5.3 派工三律（项目特有，与六字段叠加）

1. **一 task 一 commit**：完成即一个 atomic commit；做不完或发现要改别的，先停下拆 task。
2. **护栏必破坏验证**：凡新增检查 / 测试 / 门禁，必须做「改坏 → 变红 → 还原」，并分清「护栏错」与「脚手架错」（见 `guardrail-destruction-verification` skill）。**假护栏比没护栏更糟。**
3. **要正证据，不靠失败**：造「只有 X 成立才能成功」的场景 + 一条反证腿；跑测试前先核验真实环境（库 / 端口 / 数据在不在），不假定位置。

#### 8.5.4 推荐工作流（Test-Driven Delegation）

1. **先写测试**：让 agent 先写验收测试——能逼你把需求讲清；讲不清 = 你自己还没想好。
2. **测试先红**：实现不存在时测试必须失败（证明测试真在测东西）。
3. **再实现**：写代码让测试转绿。
4. **破坏验证**：按 §8.5.3-2 验证护栏。
5. **自证交付**：agent 跑验收命令，把「绿 / 红 / 输出」作为完成的客观证据，不靠主观说「做完了」。

#### 8.5.5 禁止的 task 写法

- ❌ 「根据你的发现修复它」——把综合判断甩给 agent，没给可判定目标。
- ❌ 缺验收标准的 task。
- ❌ 一个 task 跨多个模块 / 多个关注点（违反 §边界与一 task 一 commit）。
- ❌ 假设 agent 知道上下文（不引契约、不给文件:行号）。
- ❌ 过度指定实现细节（「用 for 循环」）——限定到「接口与行为」即可，怎么实现留给 agent。

---

## 9. 行为边界

### Always Do（必须做）

> 带 **[ui]** 的条目属于 `tellurion-ui` 仓。**本仓没有前端代码**，在 core 里不需要也不该执行它们。

- ✅ **[ui]** 在提交前端变更前运行 `npm run lint && npm run typecheck`
- ✅ 在提交后端变更前运行 `go vet ./... && go test ./...`
- ✅ **[ui]** UI 变更后运行 `npx playwright test`
- ✅ 更改接口或架构时更新相关文档
- ✅ 参考 VISION.md（`tellurion-docs:docs/VISION.md`） 进行架构决策时遵循设计原则
- ✅ **[ui]** 大对象使用 `shallowRef`（BacktestResult, portfolio_values 数组）
- ✅ **[ui]** Icon 组件使用 `markRaw()` 包装防止 Vue 响应式警告
- ✅ **[ui]** 状态更改后访问 DOM refs 前 `await nextTick()`
- ✅ **遵循 Document Self-Maintenance Protocol**（见第 10 章）— 保持文档实时更新
- ✅ 新增数据表/字段时先确认其**数据归属**（ADR-023 数据层分区）— 可重建数据物理唯一于 PG，不落 vault

### Ask First（先问再做）
- ❓ 修改数据库 schema（在 `pkg/storage/postgres.go` 的 `migrate()` 数组末尾追加幂等 DDL —— **不是**在 `migrations/` 加文件，那个目录不执行）
- ❓ 添加新的 npm 或 Go 依赖
- ❓ 更改核心接口（`Strategy`, `Domain` types in `pkg/domain/`）
- ❓ 移除或重命名现有 API 端点
- ❓ 修改 `docker-compose.yml` 基础配置
- ❓ 不确定的设计决策时，检查 `docs/adr/` 是否有先前 ADR
- ❓ 新增数据层 schema 分区（`ingest.raw` / `market.*` / `quant.*` / `research.*`）
- ❓ 让 AI 绕过能力层直连数据库 / 读文件（违反 ADR-023 §2「能力层存在的意义是拦住 AI 直接摸数据」）

### Never Do（绝对不做）
- ❌ **绝不在代码中硬编码 API 密钥、密码或凭据**
- ❌ **绝不绕过评审直接推他人贡献的分支** — 单人维护期本仓可直接提交 `main`（前提与补偿门禁见 ODR-067，正文在 `tellurion-docs` 仓 `docs/archive/odr/`）；上游仓（如 `nautilus_trader`）按各自 `AGENTS.md` 走 PR，不套用本授权
- ❌ **绝不修改 `node_modules/`, `dist/`, `.env*` 或自动生成的文件**
- ❌ **[ui]** **绝不使用 `any` 类型且无显式注释说明**
- ❌ **绝不生成无法解释的代码**
- ❌ **[ui]** **绝不使用 `ref()` 处理大对象（>50 属性或嵌套数组）— 使用 `shallowRef`**
- ❌ **[ui]** **绝不状态更改后立即访问 DOM refs（canvas, input）— await `nextTick()`**
- ❌ **[ui]** **绝不跨组件复制工具函数如 `fmtPercent()`**
- ❌ **绝不让同一份数据在两个位置各自存储** — 共享走 `content_hash` 证据坐标，不复制
- ❌ **绝不让 AI 直接摸数据**（读库 / 读文件）— 一律经能力层 MCP 工具（ADR-023 §2）
- ❌ **绝不编写独立的 Report (.md) 文件 — 使用 ODR 替代**

---

## 10. 文档维护

### 文档分类

| 类型 | 目录 | 职责 | 示例 |
|------|------|------|------|
| 设计文档 | `docs/` | 解释系统设计原理和架构 | VISION.md, SPEC.md |
| 决策文档 | `docs/adr/` | 记录架构决策的上下文和影响 | adr-001 ~ adr-024 |
| 运营决策 | `docs/archive/odr/` | 记录运营/流程/治理决策 | odr-001 ~ odr-065 |
| 任务文档 | `docs/TASKS.md` | 统一追踪可执行任务 | — |
| 参考文档 | `docs/` | 持续维护的状态/进度文档 | ROADMAP.md, TASKS.md |
| 指南文档 | `docs/guides/` | 迁移、部署等操作指南 | archive/migration-phase3-to-phase4.md |
| 归档文档 | `docs/archive/` | 过时但保留作历史参考 | reports-2026-Q2/ |

### 文档生命周期
```
Active → Stale → Archived → (optional) Purged
```
- **Active**: 当前准确，被引用在 AGENTS.md Document Index
- **Stale**: 含过时信息但有历史价值 → 在顶部添加 ⚠️ stale notice
- **Archived**: 移至 `docs/archive/` 并带季度标签（如 `reports-2026-Q2/`）
- **Purged**: 从归档中删除（仅限 12+ 个月后，需维护者批准）

**Staleness Detection** — 每次新会话开始时快速检查：
- AGENTS.md Known Issues 表是否仍反映现实？
- Data Flow Architecture 中的端口/服务是否正确？
- ODR index 中的条目是否已过时？

如果检测到过时内容，**主动修复** — 不要等用户提问。

### Rule 1: Update-on-Change Triggers

| 代码变更 | 文档 to Update | 更新内容 |
|---------|--------------|---------|
| 新增/修改 Go API 端点 | `docs/SPEC.md` | API section: endpoint path, method, request/response |
| 新增/修改数据库表 | `docs/ARCHITECTURE.md` | DB schema section: table name, columns, indexes |
| 新增/修改服务端口 | `docs/ARCHITECTURE.md` + AGENTS.md Data Flow | Service topology diagram |
| 更改 Strategy 接口 | `docs/SPEC.md` + `docs/VISION.md` + AGENTS.md Canonical section | Interface signature (all 3 must match) |
| 新增 Vue 页面/路由 | `docs/ARCHITECTURE.md` (frontend section) + `docs/design/pages/` | Page structure tree + Design PRD |
| 新增 npm/Go 依赖 | AGENTS.md Commands section (if adds new commands) | Build/run commands |
| 修复已知问题 | AGENTS.md Known Issues table | Remove the fixed entry |
| 发现新问题 | AGENTS.md Known Issues table | Add new entry with workaround |
| 变更 docker-compose services | `docs/ARCHITECTURE.md` + AGENTS.md Data Flow | Service list and ports |
| 变更**顶层定位 / 分层模型** | `docs/PRODUCT.md` + `docs/adr/adr-023-*.md` + AGENTS.md §1/§2 | 三层模型（AI 编排 / 能力 / 数据）、角色边界、可监督性 |
| 新增数据表 / **schema 分区** | `docs/adr/adr-023-*.md` + `pkg/storage/postgres.go`（内联 DDL）+ `docs/ARCHITECTURE.md` | 该数据属哪一类，是否可重建；**加表只改内联 `migrate()` 数组** |
| 变更**证据坐标格式** (`citation`) | `docs/SPEC.md` + `docs/PRODUCT.md` + AGENTS.md §7 | citation 字段、Evidence API 契约 |

### Rule 2: ODR Creation Triggers

| 操作 | ODR 类别 | 创建时机 |
|------|---------|---------|
| 删除/归档任何文档 | Cleanup | 操作后立即 |
| 审计/审查工作 | Audit | 完成后 48h 内 |
| 文档架构迁移 | Migration | 完成后 72h 内 |
| 变更开发工具或流程 | Tooling/Process | 上线前 |
| 归档文档到 `docs/archive/` | Cleanup | 与归档操作同会话 |

**ODR Template**（所有新 ODR 必须使用此格式）：
```markdown
# ODR-[next-number]: [Short Title]

> **Status**: [Proposed/Accepted/Completed/Deprecated]
> **Date**: [YYYY-MM-DD]
> **Category**: [Cleanup/Audit/Migration/Tooling/Process]
> **Related ADRs**: [adr-xxx] (if any)
> **Supersedes**: [odr-yyy] (if replacing an earlier ODR)

## Context
[Why was this needed? What problem triggered it?]

## Decision
[What was decided? Why this approach over alternatives?]

## Consequences
[Positive and negative impacts of this decision]

## Artifacts
[Files created/modified/deleted in this operation]

## Metrics
[Quantifiable results: doc count change, line reduction, etc.]

## Lessons Learned
[What would you do differently next time?]
```

创建 ODR 后，**必须同时更新** `docs/ADR.md` — 将新 ODR 添加到 "ODR Index" 表。

### Rule 3-6: 其他文档规则

**Rule 3: No Report Files — Use ODR Instead**

**CRITICAL**: 绝不创建新的 `*_REPORT.md` 文件。

Reports 是快照，会迅速过时。替代方案：
1. **决策类** → 在 `docs/archive/odr/` 创建 ODR
2. **审计发现** → 创建 ODR + 直接更新受影响的文档
3. **迁移记录** → 创建 ODR + 如需要则更新 AGENTS.md

唯一例外：`FINAL_VERIFICATION_REPORT.md` 保留作为质量门禁 artifact。

**Rule 4: AGENTS.md Self-Update**

AGENTS.md 是活文档。以下情况主动更新：

| Trigger | What to Update in AGENTS.md |
|---------|----------------------------|
| 发现新构建/测试命令 | Commands section |
| 发现新已知问题 | Known Issues table |
| 已知问题解决 | Remove from Known Issues table |
| 服务端口变更 | Data Flow Architecture diagram |
| 新文档添加到 docs/ | Document Index table |
| 文档归档到 docs/archive/ | 从 Document Index 移除，在 archive README 注记 |
| 代码模式经验教训 | Key Frontend Patterns 或 Code Style sections |
| 新 "Never Do" 规则 | Boundaries → Never Do section |

**Rule 5: Session-End Document Check**

每次编码会话结束前（特别是建议 commit 前），运行此清单：

- [ ] 是否修改了接口？→ 更新 SPEC.md / VISION.md / AGENTS.md
- [ ] 是否添加/删除了文件？→ 如是结构性变更，更新 ARCHITECTURE.md 目录树
- [ ] 是否修复了已知问题？→ 从 AGENTS.md Known Issues 移除
- [ ] 是否发现了新问题？→ 添加到 AGENTS.md Known Issues
- [ ] 是否归档/删除了文档？→ 创建 ODR + 更新 ADR.md index
- [ ] 是否改变了项目约定？→ 更新 AGENTS.md Code Style / Boundaries
- [ ] 是否添加了新依赖？→ 检查 AGENTS.md Commands 是否需要更新

---

## 11. 文档导航

> AGENTS.md 是你的快速参考；下方的 docs 提供深层上下文（"why"）。

### Explanation (理解设计)

| 文档 | Purpose | When to Read |
|------|---------|-------------|
| **PRODUCT.md（`tellurion-docs:docs/PRODUCT.md`）** | **顶层产品定义** — 一间单人自托管实验室（三角色：实验室主任 / AI 实验员 / 验证器）、三层模型（AI 编排 L3 / 能力 L2 / 数据 L1）、数据归属与不重复存储、证据服务、校准目标 | **任何任务开始前**；判断"这属于哪一层"时 |
| VISION.md（`tellurion-docs:docs/VISION.md`） | 设计原则（Accuracy First, Hot-Swap 等）、领域模型 | 开始新功能、质疑方法时 |
| [SPEC.md](docs/SPEC.md) | 技术规格、API 定义、数据模型、Strategy 接口 | 实现端点、编写策略时 |
| [ARCHITECTURE.md](docs/ARCHITECTURE.md) | 服务拓扑、DB schema（**内联 DDL 为主真相**，另有 3 处模块内自建表见其 §3；张数以 grep 源码为准，见下方口径）、缓存设计 | 理解系统布局、调试时 |
| [qt-decoupling 设计集](docs/design/qt-decoupling/README.md) | **解耦与模块化重构（ADR-D1~D30）** — 端口化模块化单体、**四 module 拓扑（kit / contracts(含 domain) / core / agent）**、策略资产服务/运行时两分、X 线服务拆分预案、**P0 基线（第一轮 20 项 + 第二轮 N1~N9）**、**分仓拓扑（D29，仓=模块/服务、S0~S3 阶梯）**、**仓清单与命名（D30：项目定名 tellurion、owner 不变、应用短名/库全名、12 仓表）**；配套 [全系统构件图](docs/design/qt-component-diagram/) | 做跨模块解耦 / 契约 / 服务边界 / 回测正确性 / **建仓改名**任务前；**开工先读 `repo-inventory.md` 的仓名表 + `p0-round2-review.md` §4 的 wave 顺序 + `repo-topology-and-naming.md` 的 S0~S3 阶梯**；引用任何 ADR-D 编号前先查其 README 索引 |
| **ADR-023（`tellurion-docs:docs/adr/adr-023-ai-experimenter-lab.md`）** | **架构决策：AI 实验员 + 人类监督者实验室** — 三层模型、EquityDeep 降为数据底座、验证器链（5 确定性 + 1 因果）、拒绝多 agent 投票、**校准优于准确率** | 判断某个组件该放哪一层时 |
| **ADR-024（`tellurion-docs:docs/adr/adr-024-expression-as-execution-target.md`）** | **执行载体：表达式而非代码** — 意图类型 → 确定性默认表达式；LLM 生成的 Go 代码只是 artifact；不做 `plugin.Open` | 碰策略生成 / 执行链路时 |
| archive/RESEARCH-equitydeep-legacy.md（`tellurion-docs:docs/archive/RESEARCH-equitydeep-legacy.md`） | **EquityDeep 详案（历史快照）** — Product 设计 + Tech Implementation（契约 C1/C2 + 三桥 B1/B2/B3 + 改造清单 C-1~C-9）；⚠️ 按 ADR-022（`tellurion-docs:docs/archive/superseded-adr/adr-022-unified-research-platform.md`） 写的，**该 ADR 从未实施**（已被 ADR-023 取代），只作设计来历追溯 | 追溯 EquityDeep 设计来历时 |

> **表数口径（2026-09-21 校准，AUD-14）**: 唯一执行路径是**内联 DDL**
> （`pkg/storage/postgres.go` 的 `migrate()` 数组，grep `CREATE TABLE IF NOT EXISTS`
> 实测）—— 唯一该被引用的数字就是**当下那一次 grep 的结果**，不是本行
> （也不是任何文档里）写死的数。
>
> `migrations/` 与 `docs/migrations/` 里的 `.sql` **不被执行**
> （`migration_manager.go` 已于 2026-09-18 删除，零调用方；见
> [`migrations/README.md`](migrations/README.md)）。因此
> 「内联 N 张 + 迁移新增 M 张 = 总数」这种口径**从根上不成立** —— 迁移那一半
> 从来不参与建表，加进去只是把两个不相干的数拼成一个看起来合理的数。
>
> ⚠️ **本行出现过的历史数字都是各自时点的快照，别照抄**：
> `6 张` → `32 张 (14+18)` → `38 张 (20+18)`（ODR-056）→ 审查报告里的
> `内联 22 张`（ODR-065 时点）→ `37 张`（AUD-14 时点）→ 此后 K8（external_signals）
> 与 P2-2（产业链四表）又各有追加。**数表就 grep 源码**，不要引用文档里的数。
>
> `pkg/storage/bulk_insert_ddl_test.go` 有防漂移断言：ETL 的 `TableMapper`
> 目标表必须出现在内联 DDL 里（零 DB 依赖，静态读源码）。

### Reference (查找状态)

| 文档 | Content | Updated When |
|------|---------|-------------|
| ROADMAP.md（`tellurion-docs:docs/ROADMAP.md`） | Sprint 进度、Phase 里程碑、状态跟踪 | 规划工作时、检查进度时 |
| NEXT_STEPS.md（`tellurion-docs:docs/archive/NEXT_STEPS.md`） (archived) | 审查发现、TODO list、行动项（历史） | 代码审查后、规划下一步时（见 TASKS.md） |
| TEST.md（`tellurion-docs:docs/TEST.md`） | 测试策略、覆盖率目标、T+1/涨跌停规格 | 编写测试、验证正确性时 |

### How-to (执行任务)

| 文档 | 用途 |
|------|------|
| E2E 测试指南 | 见 TEST.md（`tellurion-docs:docs/TEST.md`）§7.2「E2E Tests — Playwright」——原 `docs/E2E_TEST_GUIDE.md` 系幻影引用，三仓 git 历史从未存在（DOCS-01 收口） |
| [TASKS.md](docs/TASKS.md) | 统一任务追踪（含 Phase 3 实施任务） |
| [guides/archive/migration-phase3-to-phase4.md](docs/archive/migration-phase3-to-phase4.md) | Phase 3 → Phase 4 迁移指南 |
| [archive/benchmark-results.md](docs/archive/benchmark-results.md) | 性能基准测试结果 |
| **Hermes Agent 集成** (`docs/hermes/`) | **AI 研究层**: `skills/autonomous_factor_mining.md` (自主挖掘 Skill), `config/hermes.yaml` (Agent 配置 + 预算), `e2e-acceptance-test.md` (验收测试), `tools-quant-backtest.yaml` (MCP 工具清单), `prompts/quant-research.md` (研究员 prompt) — 见 ODR-046（`tellurion-docs:docs/archive/odr/odr-046-hermes-agent-integration-decision.md`） |

### Decisions (架构决策)

| 文档 | 内容 |
|------|------|
| ADR.md（`tellurion-docs:docs/ADR.md`） + `docs/adr/` + `docs/archive/odr/` | 架构 (ADR) + 运营 (ODR) 决策记录 | 质疑过往决策、理解理由时 |

**Archived** (在 `docs/archive/`): CLEANUP_REPORT.md, DOC_AUDIT_REPORT.md, MIGRATION_REPORT.md, `plans-2026-Q3/` (S7 执行计划 14 件 — 见 ODR-044（`tellurion-docs:docs/archive/odr/odr-044-s7-plan-archive-and-pattern-extraction.md`）) — 见 `docs/archive/README.md`

---

## 12. 会话管理

对于多步骤任务，在 `.session/task-current.md` 中维护实时状态。这有助于跟踪进度并向用户展示工作全貌。

### 启动新任务会话
创建或更新 `.session/task-current.md`：
```markdown
# Active Task: <brief title>

## Objective
<what you're trying to accomplish>

## Progress
- [ ] Step 1: ...
- [ ] Step 2: ...

## Notes
<blockers, discoveries, decisions made>
```

### 重置上下文（Standup 格式）

当对话漂移或上下文丢失时，使用此格式重置：
```
@AGENTS.md

## Standup
Since last session:
- Completed: <list finished items>
- In Progress: <current work>
- Blocked: <anything blocking?>
- Next: <immediate next step>

Context from previous session: <brief summary of key decisions>
Please continue from where we left off.
```

---

## 13. 当前状态

### 项目健康度
- **Phase**: 4 (AI-Native Evolution) 进行中；Phase 3 (Integration & Scale) 已收口
- **测试覆盖** (2026-06-29 实测, 总体 62.9% statement-weighted): backtest 76.0% | strategy 64.4% (顶层) / plugins 83.1% (子包) | data/source 60.9% | data/sentiment 76.8% | storage 13.2% (无 DB 时 skip) | ai 28.9% (顶层) / 子包 16-95% (avg ~67%) | live 75.8% | marketdata 79.0% | risk 85.3% | compliance 91.6% | fees 100% | expression 74.5%
- **关键服务（本仓口径）**: Analysis ✅（:8085）| Data ✅（:8081）| Sync ✅（在 data 服务内）—— **AI Research 的代码在 `tellurion-agent` 仓**；**strategy-service（:8082）已于 REV-16 删除**，不要再按"五个服务"理解
- **Hermes Agent 集成** (2026-07-02, ODR-046): Phase 1-2 完成 — **21 个 MCP 工具** (`pkg/tools/builtin/`, ODR-057 新增 `research.profile`) + L1-L4 验证门禁 + 自主挖掘 Skill + 预算控制器配置. 前端 AI UI 已弃用 (ODR-045), Hermes 自然语言交互替代. `pkg/ai/agents/` Go-native agents 保留向后兼容但已弃用.
- **审计状态** (2026-06-29 ODR-043): 4 维度审计完成, 12 Critical / 18 High / 10 Medium 问题点; 5 真实 bug 已识别待修复
- **架构定位** (2026-09-16, Accepted — ADR-023（`tellurion-docs:docs/adr/adr-023-ai-experimenter-lab.md`） / ODR-048（`tellurion-docs:docs/archive/odr/odr-048-top-level-product-redefinition.md`）, 取代 ADR-022（`tellurion-docs:docs/archive/superseded-adr/adr-022-unified-research-platform.md`） / ADR-021): **一间单人自托管实验室** — AI 实验员（唯一编排者，会循环）操作底座，人当实验室主任；EquityDeep 从「工作面」**降为数据底座**（产业链图谱 + 研究洞察），不再是第二个 agent。三层模型见 §2。执行载体见 ADR-024（`tellurion-docs:docs/adr/adr-024-expression-as-execution-target.md`）（表达式，不是 LLM 生成的代码）。
- **全栈静态审查** (2026-09-21, ODR-065（`tellurion-docs:docs/archive/odr/odr-065-fullstack-static-review.md`）): 24 项发现（5 Critical / 9 High / 6 Medium / 4 Low）。**Critical 5 项与 High 8 项已全部关闭**（AUD-01~13）；Medium/Low 在 [TASKS.md](docs/TASKS.md) 的 AUD-14~29。

### 任务追踪
> **Phase 3 任务追踪**: [docs/TASKS.md](docs/TASKS.md)
> **Phase 4 (AI-Native) 任务追踪**: docs/archive/tasks-phase-2.md（`tellurion-docs:docs/archive/tasks-phase-2.md`）
>
> Phase 3 任务（P0-P3 + 实施任务）在 TASKS.md 中维护。
> Phase 4 任务（AI Agent + Live Trading）在 archive/tasks-phase-2.md 中维护。

### 技术债
- Phase 3 大部分已完成（见 [TASKS.md](docs/TASKS.md)；NEXT_STEPS 已归档）
- Phase 4 新增技术债：AI Agent 测试覆盖、表达式引擎性能、LLM 成本优化
- LiveTrader 已实现接口和 MockTrader，但缺少真实券商对接实现

---

## 14. 已知问题与变通方案

| Issue | Workaround |
|-------|-----------|
| **Legacy HTML UI 已退役** | **已删除（[AUD-33]，2026-09-22）** —— 6 个文件（`cmd/analysis/static/`，5 html + 1 css）+ `main.go` 的 10 条 HTML 路由与 `/static` 挂载 + 4 条裸镜像路由（`/ohlcv/:symbol`、`POST /screen`、`/stocks/count`、`/market/index`）全部移除。**analysis-service 现在只提供 API**：`:8085/` 返回 404 是**预期行为**，不是故障。前端只有 `tellurion-ui` 仓的 `web/`（Vue SPA），由 nginx 托管在**宿主 8080**（[AUD-32]），只通过 `/api/*` 打后端。**不要重新引入** catch-all `/` 或 `/static` 挂载 —— 那会让两个前端在同一端口上悄悄分叉，且 `cmd/analysis/deps_test.go` 的 `mustNotHave` 会立刻报错 |
| **`LiveEngine` 生产零调用方**（[AUD-34]，裁决 2026-09-22：**保留**） | `pkg/live/engine.go` 的 `LiveEngine`（约 450 行完整的 Start/Stop/SubmitOrder/PositionManager 编排）**没有任何生产调用方** —— 唯一的生产消费者 `handlers_paper_trading.go` 已随 [AUD-33] 之前的 AUD-19 删除；活的 `/api/execution/*` 走 `MockTrader`，不经过它。**它只被测试覆盖**（`live_test.go` / `engine_test.go`），**从未被真实请求跑过**；`SimulatedBroker` 同样零调用方。**这是被明确接受的风险，不是已解决的问题** —— 一个从没被真实请求执行过的路径，等于把首次运行留给生产。**接线到实时行情 / 真实券商之前，先补一条从 HTTP 层真的接一次的集成测试**，不要直接上资金 |
| **[ui]** `ChatbubbleEllipsisOutline` icon name doesn't exist | 正确名是 `ChatbubbleEllipsesOutline`（`s` 前有 `e`）。**这条属于 `tellurion-ui` 仓**（本仓无前端） |
| **[ui]** Trade markers may not render if `portfolio_values` is empty | 渲染前确保回测返回有效数据。**属于 `tellurion-ui` 仓**（本仓无前端） |
| **[ui]** **前端 AI 组件已删除** (ODR-045, 2026-07-02) | 9 个组件 P1-13 创建后 S7-P2-7 作为死代码删除; Hermes Agent 自然语言交互替代 (ODR-046). 不要重建 `web/src/components/ai/`（现在在 `tellurion-ui` 仓）；AI 侧走 `tellurion-agent` 的 `pkg/ai/tools` MCP 工具层 |
| `pkg/ai/agents/optimize.go` 不存在 | S10-1 误标 ✅; TPE/遗传算法在 `pkg/ai/search/` 但无 agent 包装层. 新代码用 MCP 工具 `walk_forward_validate` (ODR-046) |
| **ODR-011 Multi-Source Risks** (CR-48, ODR-012) | See sub-table below |
| `fundamentals` 与 `stock_fundamentals` 表字段重叠 | **已收口**（ODR-056（`tellurion-docs:docs/archive/odr/odr-056-fundamentals-table-consolidation.md`） / EQD-P3-1，2026-09-15）：迁移 025 将 `fundamentals` 存量按 `symbol → ts_code` 并入 `stock_fundamentals` 后 **DROP 旧表**；4 个 `symbol` 系读写函数已收敛。**基本面数据一律读写 `stock_fundamentals`，不要再引用 `fundamentals` 表** |
| **基本面深度不足** (`stock_fundamentals` 仅 pe/pb/roe 三标量) | **部分收口（DBT-07，2026-10-17）**：`stock_fundamentals` **已灌数** —— 真库 112 行（Tushare `fina_indicator`，600000.SH / 000001.SZ 全期截面）；此前 0 行的真因是**从未执行过灌数**（被「免费额度不可得」的误判挡住）。灌数中逮到并修复真缺陷：`fina_indicator` 会剔除该期全空的列，旧实现按固定索引解包 ⇒ roe 值写进 pe 列；现按响应 `fields` 建列名→索引映射（钉子 `pkg/data/dbt07_normalize_nameindex_test.go`）。⚠️ **深财务纵向（`fundamentals_detail`）的阻塞理由已被 DBT-11 实测订正（2026-10-19）** —— 旧登记「依赖 `ingest.raw` 归档快照，当前无 raw 入库（`EQD-P1-2` 待做）」**两句皆假**：该表今天 12,108 行，自动归档腿早在生产装配里（`pkg/data/tushare.go:220` `call()` 内 → `pkg/data/tushare_raw.go:31 archiveRaw` → `pkg/storage/ingest_raw.go:50 SaveRawIngest`），EQD-P1-2 亦已于 `7b5dd8e`（2026-09-15）结案（门/字典/normalizer/五个纵向因子读方全在册）。真正缺的是两件：① **没有生产者往 `POST /api/ingest/equitydeep` 投 snapshot**；② Tushare 免费权限**给不出**所需 8 个 field_code —— 纯读库取证（0 次 API 调用）：全仓 25 条 `tushare.fina_indicator` 的 `fields` 只有一种形状 `[ts_code, ann_date, end_date, roe, roa, gross_margin]`，与 `total_revenue` / `operating_cost` / `contract_liability` / `ocf_net` / `net_profit_attr` / `total_assets` / `equity_attr` / `inventory` 一个都不对得上，且这 6 名经字典查找全部 miss。写入门已于 2026-10-19 **端到端真跑一次**（真 HTTP 门 → 真库 → PIT 读回，`0 → 5 → 0` 行、零残留；`pkg/data/equitydeep` 覆盖率 0% → 100%）。⇒ 仍**不要**假设深财务纵向字段可用；读方降级语义是 `ErrNoFundamentalsDetail`（`pkg/data/equitydeep/equitydeep.go:67`）。`pe/pb/roe` 三标量已可用，但银行股无 pe （`GetFundamentalsSnapshot` 带 `AND pe IS NOT NULL`，快照 0 行 ≠ 表内无数据）。工单：`docs/TASKS.md` 的 DBT-16（生产者）与 DBT-14（`fetched_at` 契约漂移）。逐表结论见 SPEC「深财务族数据缺口」节 |
| **eastmoney 日线资金流端点本机不可达**（[DATA-10]，2026-10-16） | 端点结论**不变**：`/api/qt/stock/fflow/daykline/get` 这条 path 本机 push2 恒 502、push2his 连接重置（浏览器同结果；同主机 `ulist.np` / `kline` / `fflow/kline` 都 200 —— 不是 UA、不是参数、不是本机 TLS 栈）。但**「表仍是 0 行」已收口（DBT-09，2026-10-19，`1a1bb38`）**：`capital_flow` 链首选换成 Tushare `moneyflow`（真库实测 `code=0` 可得），eastmoney 降为兜底 ⇒ `capital_flow` **0 → 18 行**。⚠️ **口径代价要如实读**：表内数值现在是 **Tushare 的分档成交额**形态（万元 ×10000 在同步边界折成元），`main_net` / `main_buy_amount` / `main_sell_amount` / 两个 ratio / `retail_*` / `close_price` / `change_pct` 本源不给 ⇒ 落 NULL（实测 18/18 全 NULL，不是 0）；两族口径混在同一张表里**靠 `source` 列区分**，任何读方按东财含义解释这些列之前必须先读 `source`。该表目前**零读方**（无 handler / 无因子 / 无前端读它），故刻意**不进** cron 名单（日更 = 把当天整桶免费配额烧在没人读的表上）；用途归 DBT-17。真源腿挂在 build tag `dbt09live`（`cmd/data/dbt09_moneyflow_live_test.go`），`data10live` 那条东财腿仍红在源侧 |
| **~~`parseEastmoneyDate` 解析失败静默回落「今天」~~（DBT-13 ✅ 收口 2026-10-21）** | **已修，不再是需要规避的问题**：`pkg/data/source/eastmoney_sectors_adapter.go` 的日期解析曾在不认识的形状时 `return time.Now().UTC()` —— 把「我读不懂」接管成「日期就是今天」，违反本仓「缺失≠一个看起来合理的值」。2026-10-19 被真源腿撞上实测： datacenter 龙虎榜返回**带时刻**的 `"2026-09-24 00:00:00"`，而它原先只认三种纯日期形状（其中 `"2006-01-02"` 还重复列了一次）⇒ 每一行落库日期都变成当天。DBT-10 已补齐带时刻两种形状、并在 top_list 路径加了「TRADE_DATE 缺失/空串当场失败」+「越界窗口整页失败」两道兜底，**回落本体已收口（DBT-13，2026-10-21）**：`parseEastmoneyDate` → `parseEastmoneyDateStrict`，认不出即 `ok=false`，全仓 `time.Now()` 作为**解析兜底**的形状只剩这一处（多行 grep 取证：`pkg/clock`、`pkg/live/margin`、`cmd/analysis` 那三处是墙钟提供者，不是兜底）；三个调用点全部迁完 —— `fetchTopList` 对「没给日期」「读不懂」分别当场失败（与「窗口越界」三种报文互不相同，各有断言），`juchao_adapter.go` 的 placeholder 调用已删、改按实测形状读 `announcementTime`（真源取证：epoch **毫秒**，`1786723200000` ⇒ 北京时间的 2026-08-15 午夜；折成**交易所日历日**落 UTC 零点，与同族链路一致且不依赖机器时区），兜底腿是 `adjunctUrl` 的日期段，两条都读不懂 ⇒ 拒收该行。**遗留口径（如实登记，不是缺陷）**：`fetchSectorList`（`:134`）与 `fetchLimitUpPool`（`:596`）的 `tradeDate` 取墙上时钟，因为这两个端点是**当日快照**、源不给逐行日期 —— 读这两张表前先知道「日期 = 跑作业那天」是语义设定而非解析结果。判据与取证见 `docs/TASKS.md` DBT-13 结案记录 |

### ODR-011 Multi-Source Integration Risks (CR-48, ODR-012)

| # | Risk | Workaround / Status |
|---|------|---------------------|
| 1 | **mootdx Go SDK 稳定性** — 反编译协议可能随时失效 | `NewMootdxAdapter(nil)` 模式保留 — SDK 未就绪时 service 仍可启动 (`enabled=false` → Fetch 跳过) |
| 2 | **东财反爬应对不完整** — 缺 User-Agent / Referer / 随机延时 | Eastmoney 抓取仅用于低频板块/榜单数据，短线无影响；高频补全见后续 PR |
| 3 | **多源冲突无数值仲裁** — 同 `DataType` 多源返回不同值时无 reconcile 策略 | 当前 "先注册者优先" 仍为 "主备优先级"；数值仲裁 (`pkg/etl/reconcile.go`) 留待 Phase 5 |
| 4 | **实时数据无背压** — 盘中实时通道挤压未处理 | ETL 同步路径 OK；盘中实时需另议背压队列 (`pkg/data/source/mootdx_adapter.go` 后续 PR) |
| 5 | **HealthCheck 慢路径易误入 CI** — `TestInterfaceCompliance` 触发 5s+ 网络调用，CI 必崩 | Health probe 仅暴露在 `/ops/health` 端点；adapter 单元测试用 stub/mock (`pkg/data/source/etl_test.go` 模式) |

---

## 附录：快速启动清单

新会话开始时，按顺序确认：
- [ ] 阅读「1. 项目概述」→ 理解技术栈和服务拓扑
- [ ] 查看「13. 当前状态」→ 了解项目健康度和待办事项
- [ ] 检查「14. 已知问题」→ 避免踩坑
- [ ] 确认「4. 角色与边界」→ 明确工作范围和禁区
- [ ] 如继续旧任务 → 查看 `.session/task-current.md`

### 关键文件速查

| 需要做什么 | 查看哪里 |
|-----------|---------|
| 了解设计原则 | VISION.md（`tellurion-docs:docs/VISION.md`） |
| 查看 API 定义 | [SPEC.md](docs/SPEC.md) |
| 理解系统架构 | [ARCHITECTURE.md](docs/ARCHITECTURE.md) |
| 检查测试策略 | TEST.md（`tellurion-docs:docs/TEST.md`） |
| 查看路线图 | ROADMAP.md（`tellurion-docs:docs/ROADMAP.md`） |
| 了解待办事项 | [TASKS.md](docs/TASKS.md) |
| 查阅架构决策 | ADR.md（`tellurion-docs:docs/ADR.md`） → `docs/adr/` |
| 查阅运营决策 | ADR.md（`tellurion-docs:docs/ADR.md`） → `docs/archive/odr/` |
| **前端设计系统** | **docs/design/index.md（`tellurion-ui:docs/design/index.md`）** |
| 查看页面设计规范 | docs/design/pages/（`tellurion-ui:docs/design/pages/`） |
| 查看组件使用规范 | docs/design/components.md（`tellurion-ui:docs/design/components.md`） |
| 查看视觉规范 | docs/design/visual.md（`tellurion-ui:docs/design/visual.md`） |
| **AI 研究架构** | **ADR-015（`tellurion-docs:docs/adr/adr-015-ai-agent-architecture.md`）** (Go-native agents, ⚠️ 已弃用) |
| **实验室定位 / 三层模型** | **PRODUCT.md（`tellurion-docs:docs/PRODUCT.md`）** (顶层定义) → **ADR-023（`tellurion-docs:docs/adr/adr-023-ai-experimenter-lab.md`）** (架构决策, 取代 ADR-022（`tellurion-docs:docs/archive/superseded-adr/adr-022-unified-research-platform.md`） / ADR-021（`tellurion-docs:docs/archive/superseded-adr/adr-021-equitydeep-research-layer.md`）) |
| **策略执行载体** | **ADR-024（`tellurion-docs:docs/adr/adr-024-expression-as-execution-target.md`）** (表达式, 不是 LLM 生成的代码) |
| **EquityDeep（数据底座）** | `docs/design/equitydeep/` (上游规格 v1.1) → archive/RESEARCH-equitydeep-legacy.md（`tellurion-docs:docs/archive/RESEARCH-equitydeep-legacy.md`） (历史详案) → **ODR-047（`tellurion-docs:docs/archive/odr/odr-047-equitydeep-integration-audit.md`）** (审计) |
| **Hermes Agent 集成** (主路径) | **ODR-046（`tellurion-docs:docs/archive/odr/odr-046-hermes-agent-integration-decision.md`）** → `docs/hermes/` (Skill + config + 验收测试) |
| **Phase 4 实施计划** | **archive/tasks-phase-2.md（`tellurion-docs:docs/archive/tasks-phase-2.md`）** (IMPLEMENTATION_PLAN 已归档) |
| **Phase 4 任务追踪** | **archive/tasks-phase-2.md（`tellurion-docs:docs/archive/tasks-phase-2.md`）** |
| 使用本模板 | AGENTS_TEMPLATE.md（`tellurion-docs:docs/AGENTS_TEMPLATE.md`） |

---
_Last updated: 2026-10-14_
_Source: 基于 AGENTS Template v2.0 迁移，融合 tellurion + Claudeer 最佳实践_
_Migration ODR: odr-005-agents-md-v3-migration (pending creation)_
_Unified Platform Update: 顶层产品重定义 (PRODUCT.md（`tellurion-docs:docs/PRODUCT.md`）) + ~~统一研究平台~~ (ADR-022（`tellurion-docs:docs/archive/superseded-adr/adr-022-unified-research-platform.md`）, 取代 ADR-021（`tellurion-docs:docs/archive/superseded-adr/adr-021-equitydeep-research-layer.md`）) + 重构记录 (ODR-048（`tellurion-docs:docs/archive/odr/odr-048-top-level-product-redefinition.md`）) — 2026-09-15_
_ADR-022 Consistency Update: 下游引用收口 (AGENTS/TASKS/ROADMAP/SPEC — ODR-049（`tellurion-docs:docs/archive/odr/odr-049-adr-022-downstream-consistency.md`）) — 2026-09-15_
_ADR-022 Architecture Reflection: AGENTS.md v3.2 → v3.3 — 补齐四层架构 (L0-L3) 视角、双工作面状态、数据归属分区 (A-E)、Evidence API 入口、目录/schema/容器规划标注、Rule 1 触发表 — 2026-09-15_
_ADR-023/024 Reflection: AGENTS.md v3.3 → v3.4 (AUD-14) — §1 顶层定位改三角色实验室、§2 分层改三层模型 (L1-L3) + 执行载体、§3 目录树校准 (tool.go / docs/archive/odr / 21 工具 / migrations 不执行)、§11 表数口径改「内联 DDL 37 张（唯一执行路径）」、全文 ADR-022 引用收口到 ADR-023/024 — 2026-09-21_
_Phase 4 Update: AI-Native Evolution architecture documented in ADR-015, archive/tasks-phase-2.md (IMPLEMENTATION_PLAN.md 已归档至 archive/)_
_ARCHITECTURE.md 建文件纳入（2026-10-12）：docs/ARCHITECTURE.md 建成（§11 导航与 Rule 1 加表/端口触发器的落地目标，原为死链）；§11 表数口径改为「唯一可靠数字 = 当下 grep 结果」（37 张快照退役，历史数字仅作警示）；docs/README.md 文档入口同步登记。_
_分支约定变更（2026-10-14，ODR-067）：§8.5 规范 3「分支策略」与 §9 Never Do 两处**同步**改判 —— 单人自托管维护期允许直接提交 `main`，补偿门禁写在 §8.5 那条里（提交前测试当场跑 + 一个 commit 一件事 + 新红不得混进既有红）；他人贡献的 PR 与上游仓（`nautilus_trader`）不变。两处一起改是刻意的：本文件刚刚才为「同一个约定写在两处、各改各的」收过一次口（K9 三值口径，见 ui 仓 A-07），不能自己在同一份文档里再犯一次。_
_⚠️ 顺带记录一处**未修**的既有漂移：本文件大量引用的 `docs/PRODUCT.md`、`docs/ADR.md`、`docs/VISION.md`、`docs/adr/`、`docs/archive/odr/` 在**本仓已不存在**（文档拆仓后落在 `tellurion-docs`；`docs/archive/odr/` 现存 0 个文件），只有 `docs/TASKS.md`/`SPEC.md`/`ARCHITECTURE.md`/`E2E_TEST_GUIDE.md`/`TEST.md` 还在。链接批量改指向不属本次改动，已在 core `docs/TASKS.md` 登记。_
