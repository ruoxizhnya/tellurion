---
service-card: 1
repo: tellurion
kind: application
lifecycle: production
owner: ruoxizhnya
impact: blocking

requires:
  - tellurion-contracts
  - tellurion-kit
required-by: []
calls:
  - tellurion-data
  - tellurion-agent
  - tellurion-broker
  - tellurion-risk
  - tellurion-strategy
  - tellurion-orchestration
called-by:
  - tellurion-ui
  - tellurion-agent
  - tellurion-analytics

truth:
  readme: README.md
  agents: AGENTS.md
  architecture: docs/ARCHITECTURE.md
  api: docs/openapi.yaml
  db: docs/SPEC.md
  dataflow: null
  runbook: docs/runbook.md
  tasks: docs/TASKS.md

status: evergreen
last-verified: 2026-11-11
verified-by: 2026-11-11 实读 README / AGENTS / ARCHITECTURE / SPEC / openapi.yaml / docker-compose.yml / go.mod；端口、端点数量、表数量均未写入
---

# 服务卡：tellurion

> **一句话**：运行时真相的唯一承载者——回测引擎、策略运行时、撮合、组合结算、仪器域存储都在进程内。
>
> `application` · `production` · `blocking` · `ruoxizhnya`

## 1. 这是什么

本仓是系统的核心应用，既是前端与 agent 的唯一 HTTP 前门，也是热路径唯一的执行载体：回测、策略运行时、撮合、组合与结算、同步风控裁决全部在进程内完成，不出进程。

**它拥有**：`public` / `quant` / `audit` 的运行期 schema 与 DDL 主路径（`pkg/storage/postgres.go` 的 `migrationStatements()`）；`market.index_daily` 的唯一例外建表权；订单 / 成交 / 持仓 / 组合快照的真相。
**它不拥有**：`market` / `ingest` 的写权（归 `tellurion-data`）、`run.*`（归 `tellurion-orchestration`）、`risk.*`、`strategy.*`、`analysis.*`。

## 2. 它不做什么

- 不做页面与前端测试：前端在 `tellurion-ui`，在本仓新建 `web/` 视为边界错误。
- 不做 AI 探索、生成、pipeline 与工具注册表：这些在 `tellurion-agent`；本仓对 AI 四族路由只做纯反代。
- 不写行情与原始归档：core 对 `market` 只有只读裸表，写权归 `tellurion-data`；也不接受「替调用方取数」的写入口。
- 不提供独立「因子服务」：因子求值在消费点 in-process 完成，不新增跨服务因子接口。
- 不执行 `migrations/` 与 `docs/migrations/` 下的 `.sql`：加表只改 `migrationStatements()`。

## 3. 与谁相连

### 调用它（入边）

| 来源 | 协议 | 用来做什么 | 本仓挂了会怎样 |
|---|---|---|---|
| `tellurion-ui` | HTTP | 全部 `/api/*` 业务操作 | 前端整体不可用；页面报错或空白 |
| `tellurion-agent` | HTTP | 回测 / walk-forward / 表达式 / 因子相关工具调用 | AI 实验循环的能力层失败；agent 本身存活 |
| `tellurion-analytics` | HTTP | 读取回测报告与持仓 / 因子暴露 artifact | analytics 作业以「输入缺失」失败，不产生报告 |

### 它调用（出边）

| 目标 | 协议 | 用来做什么 | 目标挂了会怎样 |
|---|---|---|---|
| `tellurion-data` | HTTP | 行情读面、数据集解析、同步作业管理 | 行情读与同步路径降级或 503；进程不受影响 |
| `tellurion-agent` | HTTP | AI 四族路由纯反代（copilot / pipeline / explore / tools） | 对应路由 503；交易与回测主链路不受影响 |
| `tellurion-broker` | HTTP | 下单 / 撤单 / 账户查询（`pkg/brokerhttp`） | 装配期拒起（`BROKER_URL` 必填且需核对通道性质） |
| `tellurion-risk` | HTTP | 启动期与 run 提交时取规则快照 | 开启 resolve 时拒起或 run 失败；关闭时用内置限额兜底 |
| `tellurion-strategy` | HTTP | `strategy_ref` 非空时解析策略资产 | fail-closed：解析失败即拒绝执行该策略 |
| `tellurion-orchestration` | HTTP | worker 领 run、回报状态 | 编排 worker 无活可领；同步回测主链路不受影响 |
| PostgreSQL | PG | 全部运行期 schema 的读写与启动迁移 | 启动失败或 `/readyz` 503；不回落成空数据成功 |

> 不写 URL、端口、超时。地址见 `docker-compose.yml` / `config/analysis-service.yaml`；接口形状见 `docs/openapi.yaml`。

## 4. 运行时身份

- **常驻进程**：`cmd/analysis`（唯一 HTTP 监听）；`cmd/worker` 是编排 worker，无监听；`cmd/dualrun` / `cmd/recon` 是一次性对齐与对账工具，不是服务。
- **存活/就绪语义**：
  - `GET /healthz`：进程存活，**不碰库**，库抖不触发重启。本仓规范探针名
    （X-PROBE-CANONICAL-HEALTHZ，12 仓统一）；`GET /health` 与
    `GET /api/health` 是兼容期内的 deprecated 别名，响应体同构并带
    `Deprecation: true` 头，下一周期删除。
  - `GET /readyz`：DB 可 ping **且**启动 DDL 的关键表存在；任一不满足即 503。
- **部署真相**：`docker-compose.yml`（默认栈与 `full` profile）、`Dockerfile.service`、`config/analysis-service.yaml`、`deploy/`；端口不在此卡。

## 5. 数据与契约归属

| 维度 | 归属 | 真相源 |
|---|---|---|
| DB schema | `public` / `quant` / `audit` 写权与 DDL；`market.index_daily` 例外；其余 `market.*` 只读 | `docs/SPEC.md`、`docs/ARCHITECTURE.md` §3 |
| 跨仓 DTO / 契约 | 形状归 `tellurion-contracts`；本仓只消费，`pkg/domain` 仅保留 core-only 形状 | `tellurion-contracts:docs/API.md` |
| 对外 HTTP | 主业务 API + AI 四族反代 + 数据面转发 | `docs/openapi.yaml` |

> **未裁事项**：`factor_returns` / `ic_analysis` 的归属（写者与读面）尚未裁决，见 `tellurion-docs:docs/design/ddd-boundary-review-2026-11-11.md` C3/D1；本卡不替它定 owner。`factor_cache` 已按 ADR-037 定为 core 的分析读模型，不是第二写者。

## 6. 坏了怎么办

- **影响面**：`blocking`——前端全部业务、agent 的能力调用、编排 worker 同时失效；已落库数据仍可被其他仓直读的部分不受影响。启动期风控 / 策略资产 / broker 装配是 fail-closed，缺依赖即拒起。
- **先确认（按顺序）**：
  1. `GET /healthz` 是否 200；不是则看进程日志与启动参数。
  2. `GET /readyz` 是否 200；503 时看 DB 连通与启动迁移是否执行完。
  3. 日志里是否出现 `kernel assembly failed` / `BROKER_URL is required` / `risk_policy_source` / `broker peer channel` 这几类启动期判决。
- **升级路径与 runbook**：`owner` → 跨仓争议 → 本仓 `docs/TASKS.md`；起停与逐类失败的原文、处置步骤见 [`docs/runbook.md`](docs/runbook.md)。

## 7. 5 分钟后去哪

| 我想…… | 读这个 |
|---|---|
| 知道它承诺什么接口 | `docs/openapi.yaml` |
| 知道它拥有哪些表、哪些表已迁出 | `docs/SPEC.md`、`docs/ARCHITECTURE.md` §3 |
| 知道运行期 schema 怎么建 | `pkg/storage/postgres.go` 的 `migrationStatements()` |
| 起停 / 装配失败怎么查 | [`docs/runbook.md`](docs/runbook.md) |
| 当前在做什么 / 架构决策 | `docs/TASKS.md`、`docs/ARCHITECTURE.md`、`tellurion-docs:docs/adr/` |
