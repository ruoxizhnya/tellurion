# 目标 module 拓扑与数据归属（2026-10-19 第三轮）

> 用户要求：不要解释"为什么不做"，给一个**合理的架构**。
> 两个裁决：**ADR-D9 module 拓扑**（kit / contracts / core / agent）、**ADR-D10 数据归属重划**。
> 前置：[project-engineering-file.md](project-engineering-file.md) · [brainstorm-2026-10-19.md](brainstorm-2026-10-19.md) · [protocol-and-data-boundary.md](protocol-and-data-boundary.md)

## 修正声明

上一轮我说「agent 复用 core 的 store 句柄是合理的（能力复用）」——**那是迁就现状，不是合理架构**。按 DDD 与 ADR-027 自己的判据重判后，该说法作废，以 ADR-D10 为准。

## ADR-D9：四 module 拓扑（依赖单向无环）

> ⚠️ **module 数量已被 ADR-D24 修正为五（`domain` 独立成 module）**；依赖方向、kit 准入判据与其余内容仍成立，以 D24 为准。

- 场景：共用工具与契约散落在 core 里，导致 agent 必须依赖整个 core。
- 决策：拆成四个 Go module，依赖方向严格单向：

```
                     ┌──────────────────────────────────────┐
                     │  tellurion-kit   （零业务依赖）    │
                     │  logging · errors · id · decimal      │
                     │  statistics · httpclient · httpserver  │
                     │  auth · authz · observability · config  │
                     │  sandbox(staticcheck / runner)         │
                     └───▲────────────▲─────────────▲────────┘
                         │            │             │
        ┌────────────────┘            │             └────────────────┐
        │                             │                              │
┌───────┴─────────────┐     ┌─────────┴──────────┐      ┌────────────┴───────────┐
│ quant-trading-      │     │  quant-trading-    │      │  tellurion-agent   │
│ contracts（零依赖）  │◀────│  core（仪器 / 能力） │◀─────│  （实验员 / 应用）      │
│                     │ 实现 │                    │ 能力 │                        │
│ · port 接口          │     │ backtest · strategy │ 调用 │ loop · pipeline · search│
│ · 边界 DTO（生成）    │     │ risk · execalgo     │      │ gene_pool · validation  │
│ · 哨兵错误/fail-closed│     │ marketdata · storage │      │ tools · copilot         │
└───────▲─────────────┘     │ expression · domain  │      └────────────┬───────────┘
        │                    │ mining · sandbox/wasm│                   │
        └────────────────────┴──────────────────────┘───────────────────┘
                         agent 只依赖 contracts + kit + core 的能力接口
```

| Module | 职责 | 允许依赖 | 禁止 |
|---|---|---|---|
| `tellurion-kit` | 横切工具、零业务概念 | 无（仅第三方） | **禁止任何业务概念进入**（参考库 §7：common 膨胀 = 新耦合中心） |
| `tellurion-contracts` | 接口 + 边界 DTO + 哨兵错误 | 无 | 禁止实现、禁止业务规则 |
| `quant-trading-core` | 仪器/能力：回测、策略运行时、风控、行情、因子、存储、挖掘（**D24 后不再包含 domain**） | kit · domain · contracts | 禁止依赖 agent；禁止被应用层直接 import 实现包 |
| `tellurion-agent` | 实验员：实验循环、基因池、验证、工具编排、Copilot | **kit · domain · contracts（编译期）+ HTTP → core（运行期，粗粒度）** | 禁止直连 core 的表；禁止 import core 实现包；**禁止 per-bar / 逐笔粒度远程调用** |

**依赖不变量（护栏）**：`kit ← domain ← contracts ← core`；`agent → kit/domain/contracts`（编译期）+ `HTTP → core`（运行期粗粒度）。任何反向边命中即红。**（D24 修正：原 `kit ← contracts ← core ← agent` 已废）**

### kit 的准入判据（两条同时满足）

1. **零业务概念**——不出现"回测/策略/订单/因子/行情"等术语；
2. **被 ≥2 个部署单元使用**（core 与 agent 都用）。

按此判据的裁决（实测依赖）：

| 包 | core 内依赖 | 进 kit？ | 理由 |
|---|---|---|---|
| statistics · decimal · errors · logging · id · observability | 零依赖 | ✅ | 纯工具/数学/可观测 |
| httpclient | logging | ✅ | 随 logging 一起进 |
| httpserver | errors | ✅ | gin 骨架，非业务 |
| auth · authz | authz→auth | ✅ | 认证授权是横切 |
| clock | 自身子包 | ✅ | 时间语义原语 |
| sandbox/staticcheck · sandbox/runner | — | ✅ | 编译期静态门与进程沙箱，AI 侧与入库侧共用 |
| **fees · marketimpact** | 零依赖 | ❌ **留 core** | **零依赖 ≠ 该进 kit**：它们是 A 股费率与冲击模型，是**业务规则**不是工具 |
| ~~domain~~ | fees | **✅ 独立 module（ADR-D24）** | 领域值对象；需先解 `domain → fees` 耦合（fees 仍留 core） |
| sandbox/wasm (wazero runtime) | — | ❌ 留 core | 需逐 bar 喂数据，必须在引擎进程内（ADR-027 §833） |

## ADR-D10：数据归属重划（回答「agent 为什么要拿 core 的数据」）

- 场景：agent 通过 core 的 `*PostgresStore` 读写实验/研究/基因池数据。
- 实测证据（core 侧非测试文件）：

| 数据 | core 侧命中 | 性质 |
|---|---|---|
| `Experiment` / `ExperimentMetrics` | 仅 `pkg/storage/experiments.go`（定义） | **零业务消费** |
| `ResearchQuestion` / `ResearchProfile` / `ResearchConclusion` | 仅 `pkg/storage/research.go`（定义） | **零业务消费** |
| `factor_genes` / `strategy_genes` | 仅 `pkg/storage/postgres.go`（DDL） | **零业务消费** |

- 决策：按 ADR-027 判据（「AI 层每次都要用、而 core 层零引用 ⇒ 归 AI 层」），**这些数据全部归 agent**：
  - agent **自建 schema**（建议 `ai.*`：`ai.experiments`、`ai.experiment_metrics`、`ai.research_*`、`ai.factor_genes`、`ai.strategy_genes`），**DDL 归属与 migrate 由 agent 持有**；
  - core 侧逐步移除这些定义（先标 deprecated，迁移后删）；
  - **agent 不再借 core 的 store 句柄写数据**——它可以用 kit 提供的连接能力建自己的 store。**（⚠️ 待裁决 K：kit 目前没有 DB 包；需新增 `kit/db` 或明确允许 agent 直接用 pgx，否则此条落不了地）**
- agent 与 core 的数据交互只剩两条合法通道：
  1. **能力调用**：调 core 的 port（回测 / 行情 / 因子求值），拿**计算结果**；
  2. **只读视图**：需要读 core 域数据（回测结果、因子值）时，走 core 暴露的**只读 read model / port**，不直接摸表。
- 代价：一次性 schema 迁移 + agent 自建 migrate；本地栈无停机压力，但要做数据校验对拍。
- 状态：**提议**

### 归属总表（按域划分）

| 域 | 数据 | Owner |
|---|---|---|
| 仪器域（core） | 行情 OHLCV · 回测结果与状态 · 策略定义与状态 · 订单/成交 · 因子定义与登记 · 挖掘留痕 · audit | **core** |
| 实验域（agent） | 实验与指标 · 研究问题/画像/结论 · 基因池（因子基因/策略基因）· 实验日志 | **agent** |
| 横切（kit） | 不持有任何表 | — |

## 迁移路径（分期，绞杀者模式）

**P0 · 抽 kit 与 contracts**（⚠️ 具体分期以 [ADR-D29](repo-topology-and-naming.md) §7 的 S0~S3 为准；**S1 形态已由 [ADR-D31](project-engineering-file.md) 改为「代码直接落两个新仓 + `require` 伪版本」**，不再先在本仓内建子 module）
1. 建 `tellurion-kit`：搬 statistics/decimal/errors/logging/id/httpclient/httpserver/auth/authz/observability/clock/sandbox(staticcheck,runner)；core 与 agent 改为依赖 kit。
2. 建 `tellurion-contracts`：放 port 接口 + 哨兵错误；DTO 由 `openapi.yaml` 生成（ADR-D7）。
3. 护栏：`kit ← contracts ← core ← agent` 反向边即红。

**P1 · 数据归属重划**
4. agent 建 `ai.*` schema 与自己的 store（用 kit 的连接/migrate 能力）；
5. 双写期 → 数据对拍 → 切读 → 停写 core 侧旧表；
6. core 侧 `experiments.go` / `research.go` 相关定义标 deprecated。

**P2 · 能力面收敛**
7. agent 对 core 的依赖收敛为「仅 port + 领域类型」；禁止 import `pkg/storage`；
8. 护栏：agent 侧禁止出现 core 表常量与非 `ai.*` schema 的写语句。

## 风险

| # | 风险 | 兜底 |
|---|---|---|
| 1 | kit 变"垃圾桶"，业务概念渗入 | 准入两条判据 + AST 护栏扫描 kit 内禁词（backtest/strategy/order/factor…） |
| 2 | schema 迁移丢数据 | 双写 + 行数/校验和对拍后再切 |
| 3 | agent 自建 store 后与 core 的 migrate 冲突（52 表 vs ai 表） | DDL 归属按 schema 隔离，各自 migrate 自己的 schema |
| 4 | 一次性搬运 import 面太大 | 按包逐个搬（每搬一个跑一次 `go build ./...` + repoguard 全绿） |


---

## ADR-D24：用户裁决落档——五 module 拓扑 + agent↔core HTTP 粗粒度（修正 D9，细化 D1）

> ⚠️ **第 1 条（`domain` 独立为第 5 个 module）已被 [ADR-D29](repo-topology-and-naming.md) §5 替代**：`domain` 并入 `tellurion-contracts` 仓的子包，module 数由 5 回落为 4（`kit → contracts → core`）。理由：`pkg/domain` 被 core 306 文件 / agent 48 文件使用，本质是契约不是独立模块。**第 2、3 条不变。**（2026-10-20 用户确认此替代）

- 场景：2026-10-19 用户对三个议题拍板，其中第 1、3 条与 D9/D1 的早期口径不一致，必须落档以免定稿时钉死过时结论。
- 决策（用户裁决，非本草案自拟）：
  1. **`domain` 独立为第 5 个 module `quant-trading-domain`**（零业务依赖；依赖方向 `kit ← domain ← contracts ← core`；agent 编译期只依赖 kit/domain/contracts）。D9 的「四 module、domain 留 core」作废。
  2. **agent ↔ core 走 HTTP，但只限粗粒度**：允许——提交回测（RunSpec 进/结果摘要出）、批量回测、因子求值查询、行情查询、策略定义拉取、因子登记；**禁止**——任何 per-bar / per-symbol / per-order 粒度的远程调用；策略执行、风控裁决、撮合永远在 core 进程内。
  3. **策略线顺序**：先做策略资产服务（定义/版本/artifact），再接线 WASM 运行时（顺序落档，避免与 D12~D16 的先后混淆）。
- 理由：date-loop 完全发生在 core 内部，agent 要的是「跑一次回测」这一粗粒度动作；HTTP 边界因此不影响内循环性能，且 agent 与 core 可独立部署演进。D1 的「不拆 HTTP 服务」只约束**内核内部模块之间**（strategy/risk/backtest），不约束 agent↔core 这条应用边界。
- 代价/后果：① `domain → fees` 必须先解耦，否则 domain 无法下沉；② 回测结果为 bar 级序列，跨 HTTP 需「摘要默认 + 明细分页/artefact」；③ 双跑对齐从「同进程装配」改为「同一 HTTP 端点」，可比性由接口保证；④ agent 与 core 需各自可启动（本地开发要同时起两进程）。
- 状态：**已裁决（用户 2026-10-19）**，本篇 D9 与 project-engineering-file 的 D1 均已加指向；D24 为正式升格的候选第一条。
