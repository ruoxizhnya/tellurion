# Runbook — tellurion（core）

> 本文件是 core 起停与「起不来 / `readyz` 503 / 装配期拒起」的**唯一固定排查入口**。
> 只写「现象 → 先看什么 → 常见原因 → 对应日志原文」，不复制 `docs/openapi.yaml`、
> `config/` 的键值清单与 `docs/SPEC.md` 的 schema 内容。
>
> 服务的权责边界看 [`../SERVICE.md`](../SERVICE.md)；任务与裁决看 [`TASKS.md`](TASKS.md)；
> 部署形态与镜像来源看 [`../deploy/README.md`](../deploy/README.md)。

## 1. 起停

### 1.1 本地统一栈（推荐）

```bash
# 前置：宿主机原生 PostgreSQL 16.15 已在 5432；compose 不提供 postgres 服务。
docker compose up -d                 # 默认栈：data-service / broker / analysis-service / ai-service
docker compose --profile full up -d  # 另加 risk / strategy / analytics / orchestration
docker compose ps                    # 看 health；analysis-service 应为 healthy
docker compose logs -f analysis-service
docker compose down                  # 保留数据卷；本机数据在宿主 PG，不受影响
```

- 默认栈是 **mock 通道**：`BROKER_CHANNEL=mock` + `BROKER_ALLOW_MOCK=true`，订单永不触达真实柜台。
- `docker compose up` 前若 broker 镜像旧于源码（`/version` 无 `channel` 字段），core 会拒起——先
  `docker compose build broker`（镜像名见 compose）再 `up`。
- 镜像来自平级仓 `tellurion-data` / `tellurion-agent` / `tellurion-broker`，本仓不构建它们。

### 1.2 本机直跑（不走 compose）

```bash
DATABASE_PASSWORD='<宿主 postgres 口令>' JWT_SECRET='<32+ 字节随机值>' \
BROKER_URL=http://127.0.0.1:8091 BROKER_SERVICE_TOKEN='<网关凭证>' BROKER_ACCOUNT_ID=mock \
BROKER_CHANNEL=mock BROKER_ALLOW_MOCK=true \
RISK_SERVICE_RESOLVE_ENABLED=false SERVER_HOST=127.0.0.1 SERVER_PORT=18085 \
  go run ./cmd/analysis
```

- 本机直跑的 broker 要自己拉起（`tellurion-broker` 的 `cmd/brokergateway`），否则装配期拒起。
- 密钥/口令只走环境变量，不入库文件；`.env.example` 只给键名与语义。

### 1.3 编排 worker 与一次性工具

| 进程 | 启动条件 | 坏了的后果 |
|---|---|---|
| `cmd/worker` | 必须给 `ORCHESTRATION_URL`（或 `-orchestration-url`） | 直接拒起（它无监听，不能空跑成功） |
| `cmd/dualrun` | `--profile tools`，一次性验收门 | 跑完即退；非 0 = 对齐超阈 |
| `cmd/recon` | 一次性对账工具 | 同上 |

## 2. 探针语义（先分清，再排查）

| 探针 | 判据 | 失败时 | 用途 |
|---|---|---|---|
| `GET /health`（同 `/api/health`） | 进程活着、HTTP 栈没死，**不碰库** | 非 200 = 进程/监听问题 | liveness；库抖**不应**重启 |
| `GET /readyz`（同 `/api/readyz`） | 连接池 ping 通 **且**启动 DDL 的关键表存在，2s 预算 | 503 `{"status":"not_ready","checks":{"database":"..."}}` | readiness；摘流量，不重启 |

`/readyz` 503 的 `checks.database` 原文分两类：

- `storage: connection pool is not configured` —— 未接库的运行形态；
- `storage: schema check <表> failed: ...` / `storage: required relation <表> is missing` —— 库通了但启动建表没跑完（`migrate()` 半途失败或被跳过）。

## 3. 数据库：连不上 / 迁移没执行

**现象**：进程起不来（`Fatal`），或进程起来了但 `/readyz` 恒 503。

先看什么：

1. `psql "<DSN>" -c 'select 1'` 是否通（DSN 从 `config/analysis-service.yaml` 的 `database.*` 拼，容器内由 compose env 覆盖）。
2. `psql "<DSN>" -c '\dt public.*'` 看表在不在。
3. 启动日志里第一条 `Fatal`。

常见原因与原文：

| 日志原文 | 原因 | 处置 |
|---|---|---|
| `invalid database configuration` | `database.*` 缺必填（口令为空是最常见的一种） | 按 README「本机直跑」表补齐 env；不是等到第一次查询才报 `password authentication failed` |
| `Failed to initialize postgres store` | TCP 不通 / 认证失败 / 库不存在 | 查宿主 PG 是否在 5432、库名与账号；容器内主机名必须是 `host.docker.internal` |
| `failed to run migrations: ...` | 启动 DDL 中途失败（权限、对象冲突、半途状态） | 看 `...` 里的第一条 SQL 错误；**不要**手工跳步，先确认库不是被别的版本改过 |

> 启动迁移是 `pkg/storage/postgres.go` 的 `migrationStatements()` 顺序执行；`migrations/*.sql` 与
> `docs/migrations/*.sql` **不会被执行**。迁移问题不要靠跑 .sql 修。

## 4. 装配期拒起（`kernel assembly failed`）

`cmd/analysis/main.go` 在 `assembleKernel` 返回错误时输出：

```text
FATAL ... kernel assembly failed error="<具体原因>"
```

`<具体原因>` 就是分类依据，逐类处置：

| `error=` 原文（片段） | 原因 | 处置 |
|---|---|---|
| `broker_http: BROKER_URL is required — in-process matching was retired ...` | 没给 `BROKER_URL`；进程内撮合已退役，没有静默回退 | 给 `BROKER_URL`；compose 默认栈已注入 `http://broker:8091` |
| `broker_http: BROKER_CHANNEL is required (mock|live) ...` | 没声明通道性质 | 显式给 `BROKER_CHANNEL`；core 从 URL 看不出对端是内存网关 |
| `broker_http: BROKER_CHANNEL=mock is refused without BROKER_ALLOW_MOCK=true ...` | 声明 mock 但没双键确认 | 加 `BROKER_ALLOW_MOCK=true`（仅 dev/test） |
| `broker_http: BROKER_CHANNEL="x" is invalid (allowed: mock|live)` | 拼写错误 | 改成 `mock` 或 `live` |
| `broker_http: cannot verify peer channel via <endpoint>: ... — fail-closed` | 对端不可达 / 非 2xx / 无 `/version` | 先起 broker；旧镜像缺 `channel` 时重建镜像 |
| `broker_http: channel mismatch: BROKER_CHANNEL=live but peer reports "mock" ...` | 声明与对端自报不一致 | 改声明或改对端；**不许**放宽成「无法验证就放行」 |
| `risk ...` 快照类错误 | `RISK_SERVICE_RESOLVE_ENABLED=true` 时取不到 approved 规则 | 起 risk 并确认库里有 approved 规则；或显式改 `false` 用内置限额 |
| `strategy ...` 资产类错误 | 策略资产服务不可用且策略用了 `strategy_ref` | 起 strategy；或改用内置策略 |

**正常日志（对照用）**：

```text
INFO ... broker peer channel verified channel=mock endpoint=http://broker:8091/version
WARN ... execution channel is MOCK (in-memory gateway) — orders never reach a real counter ...
WARN ... risk limits are NOT loaded from tellurion-risk (resolve_enabled=false) — using built-in defaults; ... risk_policy_source=builtin_default
INFO ... risk limits loaded from tellurion-risk snapshot (SVC-CORE-RISK-RETIRE) ... risk_policy_source=risk_snapshot ... risk_rules_hash=...
```

> `risk_policy_source=builtin_default` 是**默认栈的正常形态**，不是缺陷；要在真栈用 snapshot 规则，才设
> `RISK_SERVICE_RESOLVE_ENABLED=true`（届时 `wait-for-deps.sh` 会等风险服务就绪，取不到即拒起）。

## 5. 鉴权三态（写入 401/503 先看这里）

启动期一次裁决，日志会点名当前态：

| 态 | 条件 | 日志 | 行为 |
|---|---|---|---|
| 1 全闸 | 有 `JWT_SECRET` / `AUTH_JWT_SECRET` | `auth: JWT enabled (P1-2)` | 写端点要 token + 角色 |
| 2 安全降级 | 无 secret、无声明、**回环绑定** | `auth: no JWT secret and no explicit declaration — starting DEGRADED: write and protected endpoints return 503 auth_unavailable, reads stay public. ...` | 读面公开；写面/受保护端点 503 `auth_unavailable` |
| 3 显式开放 | 无 secret + `AUTH_INSECURE=true` 或 `AUTH_INSECURE_EXPOSURE=loopback-published` | `auth: INSECURE open-access — 当前写面与角色校验无鉴权 ... bind=...`（非回环额外 `已对外暴露`） | 全部放行；**启动 WARN 是事后审计证据** |
| 拒启 | 无 secret、无声明、**非回环绑定** | `auth: no JWT secret and no explicit declaration — server.host="..." is not loopback, ... Refusing to start. ...` | 拒绝启动 |

常见误判：

- 容器里拿到的是第 3 态（必须绑 `0.0.0.0`），不是第 2 态——compose 已注入 `AUTH_INSECURE_EXPOSURE=loopback-published`，
  且八个服务的 `ports:` 全部 `127.0.0.1:` 前缀，声明与事实一致。
- 写端点 503 `auth_unavailable` 不是「服务坏了」，是第 2 态；要写就配 `JWT_SECRET`。
- 拼错声明名（如 `loopback_published`）按「没声明」处理，不要靠猜。

## 6. 常见现象速查

| 现象 | 先看 | 去处 |
|---|---|---|
| 容器 `dependency failed` / core 第一个退出 | `docker compose logs analysis-service` 第一条 `Fatal` | §4 装配期拒起 |
| `/health` 200、`/readyz` 503 | `checks.database` 原文 | §3 数据库 |
| 写接口 503 `auth_unavailable` | 启动日志 `auth:` 行 | §5 鉴权三态 |
| `/api/sync/*`、行情读 503 | data-service 是否 healthy | `tellurion-data` 的 runbook |
| `/api/copilot|pipeline|explore|tools` 503 | ai-service 是否 healthy；agent 是否可达 | `tellurion-agent` 的 runbook |
| 下单相关失败 | `broker peer channel verified` 是否出现；通道声明 | §4；broker 仓 runbook |
| 风控限额与 risk 不一致 | `risk_policy_source` 是哪一态 | §4 末尾 |
| 回测结果 `DATA_QUALITY / 409` 点名因子缺失 | 因子缓存/派生表是否有写者 | [`TASKS.md`](TASKS.md) `CORE-FACTOR-CACHE-WRITER` / ADR-037 |

## 7. 备份与恢复

全 schema 备份与恢复演练的步骤、证据口径由 [`TASKS.md`](TASKS.md) 的 `CORE-PG-BACKUP-DR` 落地，落地后补入本节
（含逐表行数对拍与「不可重建数据」清单）。

## 8. 排查边界

- 本文件只覆盖 **core 自身**的起停与装配/就绪判决。
- broker 通道性质、risk 快照内容、data 的 503、orchestration 的任务状态，分别看对应服务仓的 runbook。
- 启动期 fail-closed 行为**不允许**为了「先起来」而临时放宽；要改形态就按 `docs/TASKS.md` 的条目走裁决。
