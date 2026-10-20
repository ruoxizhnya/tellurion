# 协议分层与数据归属边界（Q1 追问，2026-10-19）

> 追问原文：「如果是 DTO，为什么不用 openapi 直接再造，openapi 就是协议；如果是 schema 数据，为什么他会暴露给其他服务」
> 配套：[project-engineering-file.md](project-engineering-file.md) · [brainstorm-2026-10-19.md](brainstorm-2026-10-19.md)

## ADR-D7：协议分层 —— contracts（进程内）与 OpenAPI（跨进程）是两条边界，不是二选一

- 场景：既然契约里有 DTO，直接用 OpenAPI 生成不就好了？为什么要另立 contracts module？
- 决策：**两条边两种协议，各管一段**。
  - **跨进程（wire）** → OpenAPI 为真相源（**已有**：`docs/openapi.yaml` 141 KB，由 analysis 经 `docs/embed.go` 直出 `/api/openapi.yaml` + Swagger UI `/api/docs`，且有"生产路由表与 yaml 双向对拍"机制）。
  - **进程内（编译期）** → contracts module 为真相源（含 OpenAPI 表达不了的三类东西）。
- 理由：OpenAPI 只能描述**数据形状**，描述不了下面三样，而这正是当前 agent→core 这条边的实质：
  1. **行为/方法契约**——`BacktestRunner.RunBacktest(ctx, ...)` 这类接口。OpenAPI 描述 endpoint，描述不了「接口 + 调用语义 + 上下文/取消语义」。
  2. **fail-closed 语义与哨兵错误**——`ErrMarketNotRegistered` / `ErrKernelNotRegistered`，要求 `errors.Is` 判定、禁止字符串匹配。这是**行为约定**，不是字段形状。
  3. **进程内调用的零序列化成本**——agent 现在同进程调用 core 能力，**根本没有 wire**。此时用 OpenAPI 再造一份 DTO = 强迫这条边走 HTTP，把库调用降级为 RPC（多一次序列化 + 网络失败模式 + 部署复杂度）。
- 代价：两套协议需要明确的"单一真相源"规则（见下），否则漂移。
- 状态：**提议**

### 单一真相源规则（防两套协议漂移）

| 内容 | 真相源 | 生成方式 |
|---|---|---|
| 跨进程字段（HTTP 请求/响应） | `docs/openapi.yaml`（现状，不动） | `oapi-codegen` 生成到 `contracts/dto`（generated，禁手改） |
| 进程内接口与 port | `contracts`（手写） | 手写，**不许**由 OpenAPI 反向生成 |
| 哨兵错误与 fail-closed 语义 | `contracts`（手写） | 手写 |
| 将来真拆进程时的 endpoint 定义 | 由 `contracts` 的 interface 派生 | 接口 → endpoint，生成 OpenAPI 片段 |

**判据**：**这条边现在真的跨进程吗？** 是 → 走 OpenAPI；否 → 走 contracts。不要给一条进程内的边预先发明 wire 协议。
推论：**现在不该为 agent→core 造 OpenAPI DTO**；等真拆进程那天，contracts 里的 interface 正好是生成 OpenAPI 的源（先有接口，后有 endpoint）。

## ADR-D8：表归属边界 —— 复用存储能力 ≠ 共享表

> ⚠️ **本文两处已被 ADR-D10（`target-module-topology.md`）修正，以 D10 为准**：① 「agent 继续用 core 的 store 句柄是合理的」**作废**——agent 应自建 store（用 kit 的连接能力），不再借 core 的 store 句柄；② agent 自有数据的 schema 归属以 **`ai.*`**（D10 口径）为准，本文的 `research.ai_*` 表述作废。D8 的其余部分（表归属唯一、写路径走 port、护栏兜底）仍然成立。

- 场景（用户质疑成立）：schema/表数据不该暴露给别的服务，那 agent 为什么能用 core 的 storage 写表？
- 现状实测（agent 仓非测试代码）：
  - 用 core 的领域/存储类型：`storage.Experiment` / `ExperimentMetrics` / `ExperimentUpdate` / `ResearchQuestion` / `ResearchProfile` / `ResearchConclusion` / `TradingCalendar` / `DatasetSplitTrain`、`PostgresStore`。
  - 有自己的表：`INSERT INTO factor_genes`、`INSERT INTO strategy_genes`（gene_pool 自有资产）。
  - **已做对的一处**：`cmd/ai/main.go` 里 agent 自己定义了 `pipeline.ExperimentSink` 接口，`store` 作为实现注入（`if store != nil { expSink = store }`，注释还专门防了"nil 塞进接口变非 nil 接口值"的坑）。**这是依赖倒置的现成样板**——agent 声明"我要什么能力"，core 提供实现。
- 决策：把「存储能力」与「表归属」拆开处置：
  - ~~**存储能力（连接 / migrate / 查询 / 事务）= 仪器，可复用** → agent 继续用 core 的 store 句柄是合理的，不是耦合。~~ **（⚠️ 此句已由 ADR-D10 作废：agent 自建 store，只用 kit 的连接能力；上方 banner 为准）**
  - **表归属 = 边界，不可共享** → 每张表（或每个 schema）只有一个 owner，其他方只读或经 port 写。
- 落地要求：
  1. 用现有 schema 划分 owner：`public`（行情/回测/策略/用户）· `ingest.raw`（原始归档）· `quant.*`（派生）· `research.*`（研究投影）· `market.*` · `audit`（message_log）。agent 的自有资产（gene_pool / 实验日志 / 研究结论）归入 `research.ai_*` 或独立 schema，**由 agent 持有 DDL 归属**。
  2. 凡是 agent 要写 **core 归属**的表（如 `Experiment` 系列、回测结果），必须走 core 暴露的 port（现状 `pipeline.ExperimentSink` 已经是这个形状，把它规范化为 contracts 里的 `ExperimentSinkPort`）。
  3. 补 repoguard 护栏（AST，同 `canonical_layer_test.go` 风格）：**禁止 agent 侧直接写非 `research.ai_*` schema 的表**，命中即红。
- 理由：共享数据库而各自直写 = 分布式单体雏形；复用连接而各守表归属，才是可演进的边界。
- 代价：需要梳理现有表的 owner 清单并写进档案（一次性工作量）。
- 状态：**提议**

## 一句话回答两个问题

1. **为什么不用 OpenAPI 直接再造？** 因为那条边现在不跨进程，OpenAPI 是 wire 协议，造了就是给一条进程内调用凭空发明网络边界；且 OpenAPI 管不了行为契约与 fail-closed 语义。等真拆进程时，OpenAPI 就是 contracts 的下游产物——**顺序是「先有接口，后有 endpoint」**。
2. **schema 为什么会暴露给别的服务？** 它**不该**暴露。当前暴露的是「存储能力」（合理），但顺带把「对 core 全表的写能力」也给了出去（不合理）。解法是**按 schema 分 owner + 写路径走 port + 护栏兜底**——你已经在 `pipeline.ExperimentSink` 上做对了一半，把它规范化即可。
