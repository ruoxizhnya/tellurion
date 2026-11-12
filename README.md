# Quant Lab (tellurion)

> 5 分钟了解本服务：[SERVICE.md](SERVICE.md)

> **A股多因子量化交易系统** — Go 后端（analysis 网关 + 回测 / 风控 / 执行）+ PostgreSQL；
> data / ai / ui 各自成仓（`tellurion-data` / `tellurion-agent` / `tellurion-ui`）
>
> 本文每条声明都可以用一条命令核实；不可核实的细节（端口 / 表数 / 文件清单）
> 一律指向「怎么查」，不抄数字。核对日期：**2026-11-11**。

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
│ 独立仓 tellurion-data │         │                              │
└───────┬───────────────┘         └──────────────┬───────────────┘
        │                                        │
        └────────────┬───────────────────────────┘
                     ▼
        ┌────────────────────────────┐
        │ PostgreSQL :5432            │   ← 宿主机原生 / 外部托管（唯一硬依赖）
        └────────────────────────────┘
```

> **Redis 已不在拓扑里**：缓存面随数据面退役（2026-11-10），本仓零读取点。
> 历史设计见 [ARCHITECTURE.md](docs/ARCHITECTURE.md) §4。

`cmd/dualrun` 是双跑对齐实验的编排工具，不是常驻服务。（此前的
`cmd/strategy`（`:8082`）standby 服务已于 REV-16 删除，ADR-012 关闭。）

端口清单**以配置为准**，不要引用本文：

```bash
grep -rn "port:" config/*.yaml          # 各服务监听端口
grep -n "ports:" -A2 docker-compose.yml # compose 发布端口
```

## 快速开始

**运行前置（启动期 fail-closed，缺一条就起不来）**：

- **PostgreSQL**（宿主机原生或任意可达实例）—— 今天**唯一**的基础设施硬依赖；
  `config/analysis-service.yaml` 是宿主机视角，容器内由 compose 的 env 覆盖。
- **Redis 不是前置**：缓存面已退役（2026-11-10），本仓零读取点；不用装、不用连。
- **`DATA_SERVICE_URL`（若注入）必须是绝对 URL 且主机名可解析**：启动期
  `pkg/bootstrap.ValidateServiceEndpoint` 会解析它，宿主机上误留容器 DNS
  （`http://data-service:8081`）会**启动即报错**并被点名。
- **`cmd/worker` 没有 `ORCHESTRATION_URL`（或 `-orchestration-url`）直接拒起**
  （它无监听、也没法「空跑成功」）。
- **`BROKER_URL` 是必填**（装配期无条件构造执行通道）：compose 默认栈已经
  提供 broker（`http://broker:8091`，mock 通道），本机直跑需自己拉起网关。
  启动期核对 `GET <BROKER_URL>/version` 的 `channel`，不可达 / 非 2xx / 缺字段
  一律拒绝启动。

还需要两个**平级 clone** 的仓（镜像来自它们，不是本仓构建产物）：`tellurion-data`
（不起则 `/api/sync/*` 与行情读路径失败）与 `tellurion-agent`（不起则
copilot / pipeline / explore / tools 四个族返回 503，其余功能不受影响）。

```bash
# 1) 起 PG（compose 不提供，需自备；连接串见 config/analysis-service.yaml 的 database.*）
psql "postgres://postgres:postgres@localhost:5432/tellurion?sslmode=disable" -c '\dt'

# 2) 克隆两个服务仓（与 core 平级）
git clone <tellurion-data 仓地址>  ../tellurion-data
git clone <tellurion-agent 仓地址> ../tellurion-agent

# 3) 起默认栈（4 个 service：data-service :8081 / broker :8091 /
#    analysis-service :8085 / ai-service :8086；broker 是 mock 通道）
docker compose up -d
#    可选 profile：--profile full（risk :8089 / strategy :8090 /
#                  analytics :8087 / orchestration :8088）
#                  --profile orchestration-worker（cmd/worker，需 ORCHESTRATION_URL）
#                  --profile tools（cmd/dualrun，一次性验收门，跑完即退）
#                  --profile nt（nt-sidecar，可选旁路样本）

# 4) 冒烟
curl -s localhost:8085/health | jq
curl -s localhost:8085/api/v1 | jq

# 5) 前端：独立仓 tellurion-ui（Vue 3 + Vite + Naive UI），nginx 托管于 :8080
git clone <tellurion-ui 仓地址> ../tellurion-ui
```

`web/` 目录在本仓只可能残留**本地构建产物**（不跟踪、不是源码）；前端源码
与 dev server 都在 `tellurion-ui` 仓。

### 本机直跑（不走 compose，宿主 PG）

`config/*.yaml` 里的口令与密钥一律留空（入库文件不放真值），**缺项即启动期
拒绝**——不会带着空口令拖到第一次查询才报 `password authentication failed`：

| env | 作用 | 缺失后果 |
|---|---|---|
| `DATABASE_PASSWORD` | PG 口令（`database.*` 逐字段拼装；给了 `DATABASE_URL` 则后者优先） | `invalid database configuration` |
| `JWT_SECRET`（或显式声明 `AUTH_INSECURE=true` / `AUTH_INSECURE_EXPOSURE=loopback-published`） | 鉴权三态：有密钥全闸；无密钥且未显式声明 → 回环绑定下降级启动（写面 `503 auth_unavailable`、读面公开）；显式声明 → **任何绑定**都放开（启动 WARN 点名写面 / 角色无鉴权与绑定地址，非回环附「已对外暴露」） | 无密钥且未声明 + 非回环绑定 → 拒绝启动 |
| `BROKER_URL` / `BROKER_SERVICE_TOKEN` / `BROKER_ACCOUNT_ID` | 执行引擎（进程内撮合已退役） | `kernel assembly failed` |
| `BROKER_CHANNEL`（`mock` / `live`）+ `BROKER_ALLOW_MOCK=true`（仅 `mock` 需要） | 执行通道性质声明：`mock` = 内存网关，`live` = 真实柜台 | 缺失/非法/`mock` 未双键确认 → `kernel assembly failed` |
| `RISK_SERVICE_RESOLVE_ENABLED=false`，或 risk 在线且库里有 approved 规则 | 14 个风控限额 | 取不到快照即拒绝启动 |

```bash
# analysis-service（风险快照未接入时用内置限额；端口用 SERVER_PORT 覆盖）
DATABASE_PASSWORD='<宿主 postgres 口令>' JWT_SECRET='<32+ 字节随机值>' \
BROKER_URL=http://127.0.0.1:8091 BROKER_SERVICE_TOKEN='<网关凭证>' BROKER_ACCOUNT_ID=mock \
BROKER_CHANNEL=mock BROKER_ALLOW_MOCK=true \
RISK_SERVICE_RESOLVE_ENABLED=false SERVER_HOST=127.0.0.1 SERVER_PORT=18085 \
  go run ./cmd/analysis

curl -sS http://127.0.0.1:18085/health   # 存活：不碰库，库抖也不重启
curl -sS http://127.0.0.1:18085/readyz   # 就绪：DB ping + 启动 DDL 关键表
```

⚠️ **`cmd/brokergateway` 是内存网关**（`tellurion-broker` 当前只有 Mock 通道，
真实柜台适配仍受 X6c 阻塞）：订单不会到任何真实柜台。core 从 `BROKER_URL`
**看不出**对端是内存还是真实柜台，所以通道性质必须显式声明——
`BROKER_CHANNEL=mock` 还要求同时给 `BROKER_ALLOW_MOCK=true`，启动日志会有
一条 `execution channel is MOCK ...` 的 WARN。少了任一键都**启动失败**，
不会静默地对着内存撮合跑真钱；接真实柜台时改成 `BROKER_CHANNEL=live`。

声明之后还要**对得上对端**：core 启动期会读 `GET <BROKER_URL>/version` 的
只读字段 `channel`。声明 `live` 而网关自报 `mock` → 直接拒绝启动；端点不可达、
非 2xx 或没带 `channel` 同样拒绝（fail-closed——这是唯一的可验证来源，
接受「无法验证」等于让 `live` 重新变成空头承诺）。因此升级 core 前先升级
`tellurion-broker` 到带 `channel` 的版本。

服务间地址**不需要逐个覆盖**：`config/analysis-service.yaml` 是宿主机视角，
`data_service.url` / `ai_service.url` / `strategy_asset.resolve_url` /
`risk_service.resolve_url` 默认都是 `http://localhost:<port>`；容器内由
compose / k8s 的 env 覆盖成服务名。启动期会解析这些主机名（
`pkg/bootstrap.ValidateServiceEndpoint`）：宿主机上误留容器 DNS（例如
`http://data-service:8081`）会**启动即报错**并点名该注入 `DATA_SERVICE_URL`，
不会拖到第一条业务请求才 `no such host`。完整键 ↔ env ↔ 默认值映射见 yaml
的「地址口径」注释块。

⚠️ 同段里的 `risk_service.url` **不在此列**：它是 `pkg/backtest/engine.go` 的远程
回落（`riskManager == nil` 时拼 `/detect_regime` 等三条），今天恒不可达，也不是
risk 的地址；risk 的实际地址是 `risk_service.resolve_url`（默认 `:8089`）。
详见 [SPEC.md](docs/SPEC.md) §主要配置段与 TASKS `CORE-RISK-REMOTE-FALLBACK-DEAD`。

ai-service（`tellurion-agent` 仓）同样只从 env 取 `DATABASE_PASSWORD` 与
`JWT_SECRET`，探针是 `/health`（存活）与 `/readyz`（就绪，校验 `ai.*` 迁移）；
服务地址口径与 core 一致，见该仓 README。

测试与本地栈：

```bash
go build ./...
TEST_DATABASE_DSN="postgres://postgres:postgres@localhost:5432/tellurion?sslmode=disable" \
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
| **执行层** | `pkg/brokerhttp` 通道（broker HTTP；无 `BROKER_URL` 即装配失败）+ 订单生命周期；费用/印花税在 core 运行域记账；券商适配归 `tellurion-broker` | ✅ 通道 / 🔲 真实券商 |
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
| 缓存 / 事件 | ~~go-redis v9~~（`RedisCache` 零调用方，缓存面已退役，见 ARCHITECTURE §4）；PG 版事件总线（`pkg/msgbus`）+ 事件存储 |
| 数据库 | PostgreSQL（宿主机原生或外部托管）；本仓 `migrate()` **不建 hypertable** —— 行情表 owner = `tellurion-data` |
| 前端 | 独立仓 `tellurion-ui`：Vue 3 + TypeScript + Vite + Naive UI + Pinia |
| 可观测 | Prometheus `/metrics`、结构化 zerolog、请求 ID 透传 |
| 基础设施 | Docker Compose（仅服务）、K8s 清单（`deploy/k8s/`，**跨仓部署资产**，见其 README）、Playwright E2E（`e2e/`） |

## 项目结构

```
tellurion/
├── cmd/
│   ├── analysis/        # API 网关 + 回测/因子/策略/风控/执行/合规/对账/告警（:8085）
│   ├── worker/          # 编排 worker：无监听，缺 ORCHESTRATION_URL 即拒绝启动
│   ├── dualrun/         # 双跑对齐实验编排（工具，非服务）
│   └── recon/           # 表级 writer 对拍（QT-81，工具，非服务）
├── pkg/                 # 核心库：backtest / marketdata / storage / strategy / risk / trading / …（数据管道与 AI 分别在各仓）
├── docs/                # SPEC / ARCHITECTURE / TASKS / openapi.yaml / migrations 镜像 / design
├── e2e/                 # Playwright 端到端用例
├── config/              # 唯一运行期 yaml：analysis-service.yaml（data / ai 的配置已随拆仓移出）
├── deploy/              # k8s 清单（跨仓部署资产，见 deploy/k8s/README.md）与依赖等待脚本
├── tools/               # 本地栈脚本、文档死链检查、覆盖率门禁、双跑工具
└── docker-compose.yml   # 服务编排（不含 PG / Redis / 前端）
```

## License

MIT
