# Quant Lab (tellurion)

> **A股多因子量化交易系统** — Go 后端（analysis / data / ai）+ PostgreSQL + Redis
>
> 本文每条声明都可以用一条命令核实；不可核实的细节（端口 / 表数 / 文件清单）
> 一律指向「怎么查」，不抄数字。核对日期：**2026-10-17**。

## 系统拓扑

```
             ┌──────────────────────────────────────────┐
             │  Vue 3 SPA（独立仓 tellurion-ui）      │
             │  nginx 静态托管，宿主 :8080                │
             └───────────────────┬──────────────────────┘
                                 │ 只走 /api/*
                                 ▼
┌────────────────────────────────────────────────────────────────┐
│ analysis-service  :8085   ← API 网关 + 业务主体                  │
│   回测 / 因子 / 策略 CRUD / 风控 / 执行簿记 / 合规 / 对账 / 告警    │
│   反代：/api/sync/* → :8081 ；/api/{copilot,pipeline,explore,   │
│          tools} → :8086 （ai-service）                          │
└───────┬───────────────────────────────────┬────────────────────┘
        │                                   │
        ▼                                   ▼
┌───────────────────────┐         ┌──────────────────────────────┐
│ data-service  :8081   │         │ ai-service  :8086            │
│ Tushare 同步 / OHLCV  │         │ 独立仓 tellurion-agent    │
│ 选股 / 行情路由 / ingest│         │ 因子发现 / 策略生成 / Copilot  │
└───────┬───────────────┘         └──────────────┬───────────────┘
        │                                        │
        └────────────┬───────────────────────────┘
                     ▼
        ┌────────────────────────────┐
        │ PostgreSQL :5432   Redis :6379 │   ← 宿主机 / 外部托管
        └────────────────────────────┘
```

`cmd/dualrun` 是双跑对齐实验的编排工具，不是常驻服务。（此前的
`cmd/strategy`（`:8082`）standby 服务已于 REV-16 删除，ADR-012 关闭。）

端口清单**以配置为准**，不要引用本文：

```bash
grep -rn "port:" config/*.yaml          # 各服务监听端口
grep -n "ports:" -A2 docker-compose.yml # compose 发布端口
```

## 快速开始

前置：**Go 1.25**、**Docker**、**宿主机 PostgreSQL 17.x 与 Redis 7**（compose
只编服务，不编数据库）、**ai-service 所在仓**（Copilot / pipeline / explore /
tools 四个族路由由它服务；不起它则这四个族返回 503，其余功能不受影响）。

```bash
# 1) 起宿主机 PG + Redis（compose 不提供，需自备；连接串见 config/*.yaml 的 database.*）
psql  "postgres://postgres:postgres@localhost:5432/quant_trading?sslmode=disable" -c '\dt'

# 2) 克隆 AI 服务（与 core 平级）
git clone <tellurion-agent 仓地址> ../tellurion-agent

# 3) 起后端三服务
docker compose up -d                 # data-service :8081 / analysis-service :8085 / ai-service :8086

# 4) 冒烟
curl -s localhost:8085/health | jq
curl -s localhost:8085/api/v1 | jq

# 5) 前端：独立仓 tellurion-ui（Vue 3 + Vite + Naive UI），nginx 托管于 :8080
git clone <tellurion-ui 仓地址> ../tellurion-ui
```

`web/` 目录在本仓只可能残留**本地构建产物**（不跟踪、不是源码）；前端源码
与 dev server 都在 `tellurion-ui` 仓。

测试与本地栈：

```bash
go build ./...
TEST_DATABASE_DSN="postgres://postgres:postgres@localhost:5432/quant_trading?sslmode=disable" \
  go test ./... -count=1            # 不设 TEST_DATABASE_DSN 时 DB 用例显式跳过并汇总
make test                           # = -count=1 -race -coverprofile + 覆盖率门禁
bash tools/local-stack.sh           # 起/停本地栈并断言端口
```

## 功能状态

| 模块 | 实况 | 状态 |
|------|------|------|
| **回测引擎** | 日频/分钟频、T+1、涨跌停、停牌不成交、流动性截断、费用三件套 | ✅ |
| **因子分析** | IC/IR、分组收益、归因；因子定义库 + `factor_cache` | ✅ |
| **选股器** | 多条件筛选（PE/PB/市值/换手等）；结果为空可能是数据缺口而非故障 | ✅ |
| **策略实验室** | 策略 CRUD + 参数；对比分析与调优仍是半成品 | 🔄 |
| **Copilot / AI 研究** | **handler 已迁 ai-service（:8086）**，analysis 只做反代 | ✅（依赖 ai-service） |
| **执行层** | `MockTrader` 模拟盘 + 订单生命周期 + 费用/印花税；**真实券商未接** | ✅ 模拟 / 🔲 实盘 |
| **风控 / 合规 / 对账** | 仓位计算、regime、止损；合规报告；资金对账 worker | ✅ |

## 文档索引

契约与实现说明在本仓；**愿景 / 路线图 / ADR / 测试规范**在 `tellurion-docs` 仓。

| 文档 | 位置 | 内容 |
|------|------|------|
| [SPEC](docs/SPEC.md) | 本仓 | 技术规格、API 语义、数据模型、口径裁决 |
| [ARCHITECTURE](docs/ARCHITECTURE.md) | 本仓 | 拓扑、端口、schema、缓存、引擎细节 |
| [openapi.yaml](docs/openapi.yaml) | 本仓 | **契约单一事实源**（`go:embed` 成 `/api/openapi.yaml`） |
| [TASKS](docs/TASKS.md) | 本仓 | 当前批次任务台账（完成项移入 `docs/archive/`） |
| VISION / ROADMAP / ADR / TEST | `tellurion-docs` 仓 `docs/` | 设计原则、里程碑、架构决策记录、测试规范 |

## 技术栈

| 层 | 选型 |
|---|---|
| 语言 | **Go 1.25.0**（`go.mod`） |
| HTTP | Gin v1 + `net/http`；SSE 直通（`FlushInterval = -1`） |
| 数据库访问 | **pgx v5**（`pgxpool`）+ 手写 SQL；**无 ORM**（仓内零 GORM 依赖） |
| 缓存 / 事件 | go-redis v9；PG 版事件总线（`pkg/msgbus`）+ 事件存储 |
| 数据库 | PostgreSQL 17.x（宿主机），`ohlcv_daily_qfq` 为 TimescaleDB hypertable |
| 前端 | 独立仓 `tellurion-ui`：Vue 3 + TypeScript + Vite + Naive UI + Pinia |
| 可观测 | Prometheus `/metrics`、结构化 zerolog、请求 ID 透传 |
| 基础设施 | Docker Compose（仅服务）、K8s 清单（`deploy/k8s/`）、Playwright E2E（`e2e/`） |

## 项目结构

```
tellurion/
├── cmd/
│   ├── analysis/        # API 网关 + 回测/因子/策略/风控/执行/合规/对账/告警（:8085）
│   ├── data/            # 数据服务：Tushare 同步、行情路由、选股、ingest（:8081）
│   └── dualrun/         # 双跑对齐实验编排（工具，非服务）
├── pkg/                 # 核心库：backtest / data / domain / storage / strategy / risk / live / …
├── docs/                # SPEC / ARCHITECTURE / TASKS / openapi.yaml / migrations 镜像 / design
├── e2e/                 # Playwright 端到端用例
├── config/              # 各服务 yaml 配置（端口、DB、限流、告警阈值）
├── deploy/              # k8s 清单与依赖等待脚本
├── tools/               # 本地栈脚本、文档死链检查、覆盖率门禁、双跑工具
└── docker-compose.yml   # 服务编排（不含 PG / Redis / 前端）
```

## License

MIT
