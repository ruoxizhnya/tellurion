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
- **异步回测要 worker**：默认栈与 `--profile full` 都**不含** `worker`，`POST /api/backtest`（异步体）
  仍会 202 成功并把作业交给 orchestration，但**没有人领取** ⇒ 作业停在 `pending`。要真正执行必须再开
  `--profile orchestration-worker`：`docker compose --profile full --profile orchestration-worker up -d`。
  提交回执里 `"queued": true` 就是「已排队、等 worker」；`status` 变 `running` 才表示已被领取。
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
| `cmd/worker` | `--profile orchestration-worker`，且必须给 `ORCHESTRATION_URL`（或 `-orchestration-url`） | 直接拒起（它无监听，不能空跑成功）；**没起它**时异步回测停在 `pending`（回执 `queued: true`），不是丢失 |
| `cmd/dualrun` | `--profile tools`，一次性验收门 | 跑完即退；非 0 = 对齐超阈 |
| `cmd/recon` | 一次性对账工具 | 同上 |

> `worker` **不进默认 profile**：它是无监听的执行侧，默认栈与 `full` 都能正常起服务；只有需要异步回测时才加 profile。
> 现象对照：`GET /api/backtest` 里某作业长期 `status=pending` ⇒ 先查有没有 worker（`docker compose --profile orchestration-worker ps`）。

## 2. 探针语义（先分清，再排查）

| 探针 | 判据 | 失败时 | 用途 |
|---|---|---|---|
| `GET /healthz`（`/health`、`/api/health` 为 deprecated 别名） | 进程活着、HTTP 栈没死，**不碰库** | 非 200 = 进程/监听问题 | liveness；库抖**不应**重启 |
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

**启动 WARN（不是错误，进程继续）—— `reason=loopback_gateway`**：
`BROKER_CHANNEL=live` 且 `BROKER_URL` 指向回环地址时出现。生产/准生产出现 ⇒
先查 `BROKER_URL` 是否误抄了 dev env；单机自托管（core 与 broker 同机）属预期，
无需处理。**不要**因此改成 `BROKER_CHANNEL=mock` 绕过——通道性质仍以
`/version` 对拍为准。判据与告警规则见 [live-trading.md §11.1](live-trading.md#111-loopback-live-告警broker-live-loopback-guard2026-11-13)。

**容器形态：`wait-for-deps.sh` 在进程启动前就退出**（装配期还没到）：

```text
wait-for-deps: 等 broker:8091 超过 60s 仍不可达，放弃启动。
wait-for-deps: 依赖不可达 —— 宿主 PG 或 broker 起了吗？
wait-for-deps: broker 由 docker compose 的默认栈提供（--profile full 不影响它）。
```

含义：`BROKER_URL` 的 host:port 探不通（broker 没起 / 未 healthy / 名字写错），
entrypoint 直接 `exit 1`。这与 §4 上表的装配期错误是**两个阶段**：先在网络层等不到，
连 `kernel assembly failed` 都到不了。处置 = 先起 broker；超时阈值用
`WAIT_FOR_DEPS_TIMEOUT`（秒，默认 60，最小实践值 3 只用于验证消息本身）。

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
| `/healthz` 200、`/readyz` 503 | `checks.database` 原文 | §3 数据库 |
| 写接口 503 `auth_unavailable` | 启动日志 `auth:` 行 | §5 鉴权三态 |
| `/api/sync/*`、行情读 503 | data-service 是否 healthy | `tellurion-data` 的 runbook |
| `/api/copilot|pipeline|explore|tools` 503 | ai-service 是否 healthy；agent 是否可达 | `tellurion-agent` 的 runbook |
| 下单相关失败 | `broker peer channel verified` 是否出现；通道声明 | §4；broker 仓 runbook |
| 风控限额与 risk 不一致 | `risk_policy_source` 是哪一态 | §4 末尾 |
| 回测结果 `DATA_QUALITY / 409` 点名因子缺失 | 因子缓存/派生表是否有写者 | [`TASKS.md`](TASKS.md) `CORE-FACTOR-CACHE-WRITER` / ADR-037 |
| 回测 409 `trading calendar does not cover start_date/end_date ...` | 日历覆盖到哪天（错误里已点名缺失段，如 `2024-02-01..2024-03-29`） | 补同步：data 仓 `POST /api/sync/jobs` 且 `job_type=trading_calendar`，显式给 `start_date`/`end_date`；覆盖到位后重跑。**引擎不会静默截断**——`end_date` 必须与权益曲线末点一致 |

## 7. 备份与恢复（DR 演练）

### 7.1 口径（写死，不做二选一）

- **备份形态**：**整库** `pg_dump -Fc`（custom format），覆盖本机 `tellurion` 库的**全部 10 个 schema**
  （`ai` / `analysis` / `audit` / `ingest` / `market` / `public` / `quant` / `risk` / `run` / `strategy`）。
  不按 schema 拆——同一库里的 `risk` / `strategy` / `run` 等 schema 与 `public` 有跨 schema 引用，
  拆开备份会造出一套恢复顺序问题，收益为零。
- **恢复演练**：必须恢复到**独立 `_test` 库**（默认 `tellurion_dr_test`），**绝不允许**覆盖 `tellurion`。
- **凭据**：只从 `PGPASSWORD` 环境变量读取，不写入脚本、命令历史或仓库文件。
- **版本**：`pg_dump` / `pg_restore` 主版本必须 **≥** 服务端主版本（本机同为 16）。

### 7.2 一键演练（含逐表行数对拍）

```powershell
$env:PGPASSWORD = '<本机 postgres 口令>'
powershell -NoProfile -ExecutionPolicy Bypass -File tools/pg_backup_drill.ps1
# 保留演练库供人工核查：加 -KeepTestDatabase
# 其他库/实例（仍是本机）：-Database <name> -TestDatabase <name>_dr -Port 5432
```

脚本做六件事，任一步失败即 `EXIT≠0`：

1. `pg_dump -Fc` 整库 → 临时目录（不入仓）；
2. 把 dump 转纯文本，按 `COPY` 块统计**备份当时**的逐表行数（用来隔离演练窗口内的线上写入）；
3. `DROP/CREATE` 独立 `_test` 库 → `pg_restore --no-owner --no-privileges --exit-on-error`；
4. 结构计数对拍：schema / 表 / 索引 / 序列 / 约束五组数必须两库相等；
5. 逐表行数对拍：以第 2 步的备份快照为基准，恢复库每表行数必须相等；
6. 默认删演练库与中间 `.sql`（dump 文件保留，位置打印在 `DUMP_OK` 行）。

### 7.3 2026-11-12 真跑证据

```text
DUMP_OK path=...\tellurion_dr_20261112015916.dump size=311549
DUMP_TABLES=82
STRUCTURE_MATCH schemas=10 tables=82 indexes=187 sequences=28 constraints=178
ROWCOUNTS_MATCH dump_tables=82 restored_tables=82
CLEANUP test_db_dropped=tellurion_dr_test
EXIT=0
```

破坏验证（两条，均真做真还原）：

1. **漏 schema**（任务 ⑤ 点名的判据）：恢复后临时 `DROP SCHEMA analysis CASCADE` →

```text
STRUCTURE_MISMATCH src=schemas=10 tables=82 indexes=187 sequences=28 constraints=178
                    dst=schemas=9 tables=77 indexes=180 sequences=28 constraints=162
EXIT=1
```

2. **漏行**：恢复后临时删掉 `market.stocks` 一行 →

```text
ROWCOUNTS_MISMATCH count=1
  market.stocks dump=5580 restored=5579
EXIT=1
```

删掉注入后复跑 `STRUCTURE_MATCH` + `ROWCOUNTS_MATCH`、`EXIT=0`；脚本 `sha256=1DA5963F…70B75AB`
与注入前逐字节一致（`Get-FileHash` 复核）。

> 演练期间线上写入会让「实时源库行数」与「备份快照行数」不同（实测 `run.backtest_jobs` 在 dump 后
> 从 5 涨到 6）。**对拍基准必须是 dump 里的 COPY 行数**，不是跑完之后再查源库——否则会误判成丢数据。

### 7.4 不可重建数据（丢失即不可恢复，必须随备份保真）

| 数据 | 为什么不可重建 |
|---|---|
| `strategy.strategy_definitions` / `strategy_versions` / `strategy_artifact_blobs` / `strategy_build_jobs` / `strategy_promotions` | 用户上传的策略源码/artifact 与 promote 审计，仓里没有副本 |
| `risk.rule_defs` / `rule_revisions` / `rule_snapshots` / `audit_logs` / `risk_events` | 限额规则的修订历史与风控事件是业务事实 |
| `run.backtest_results` / `run_provenance` / `run_artifacts` / `walk_forward_reports` / `batch_runs` / `parent_jobs` / `backtest_jobs` | 复现实验依赖当时的数据快照与参数，事后无法从代码重算同一结果 |
| `analysis.reports` / `attribution` / `mc_runs` / `sensitivity_runs` | 同上，属于已发布的分析产物 |
| `ai.experiments` / `factor_hypothesis` / `factor_genes` / `strategy_genes` / `generation_chain` / `research_*` / `ai_costs` | 实验与生成链的留痕不可重放 |
| `public.users` | 鉴权主体的唯一真相 |
| `public.factor_registrations` / `factor_registration_audit` / `factor_definitions` | 因子注册与冻结定义（部分由代码 seed，但注册/审计历史不在代码里） |
| `audit.message_log` / `public.audit_logs` | 审计留痕按定义不可重建 |
| `ingest.raw` | 原始归档；重新拉取要消耗第三方配额且源可能已变，按不可恢复对待 |
| `ingest.sync_jobs` / `sync_schedules` | 同步历史与排程配置 |
| `public.fundamentals_detail` | 当前**没有生产者**（`CORE-FUNDAMENTALS-PRODUCER` 系缺口），删了就是永久 0 行 |
| `quant.external_signals` / `alert_outbox` / `strategy_state` | 外部信号、待投递告警与运行态 |
| `quant.industry_vitality` / `macro_series` / `supply_chain_edge` / `sw_industry_tree` / `etf_constituent` | 外部参考数据，仓内无完整生成脚本 |
| `public.backtest_adjustment_anchors` | 回测复权锚点，与历史 run 绑定 |

### 7.5 可重建数据（丢失后可重算/重同步，但有时延与配额代价）

| 数据 | 重建方式 | 代价 |
|---|---|---|
| `market.*`（`stocks` / `ohlcv_daily_qfq` / `trading_calendar` / `index_*` / `top_list` / `capital_flow` / `fundamentals` 相关等） | 由 `tellurion-data` 重新同步 Tushare | 第三方配额 + 时间；历史越久越贵 |
| `public.stock_fundamentals` | 同上 | 同上 |
| `public.factor_cache` | **已删除（`CORE-FACTOR-CACHE-RETIRE`，Migration 066 `DROP TABLE`）**：不得重建、不得写入；因子值由 core 消费点按需重算（ADR-037 D2，物化到 run 级内存） | — |
| `factor_returns` / `ic_analysis` | 由 core 消费点按需重算（ADR-038） | CPU 时间；两表当前无写者 |
| `run.worker_registry` | worker 心跳重建 | 分钟级 |
| `public.data_migrations` / `ingest.data_migrations` | 可重建迁移台账 | 历史记录丢失 |

> `market.*` 虽「可重建」，但生产环境不要把它当作可随意丢弃——重建一次等于把配额与时间成本再付一遍，
> 且源侧修订会导致与历史结果不可比。备份时**不做**按表裁剪。

### 7.6 恢复后的最小复核

1. `python tools/check_deploy_consistency.py`、`go build ./...`（确认代码侧 DSN/版本口径没被恢复动作改动）；
2. 起默认栈，`/readyz` 200；
3. 抽查三张不可重建表：`select count(*) from strategy.strategy_definitions;`、`run.run_provenance`、
   `risk.rule_revisions`，与备份前记录比对；
4. 抽查一条历史 run 的 `run_provenance` 能按 `run_id` 读回，且 `backtest_results` 行数与其一致。

## 8. 排查边界

- 本文件只覆盖 **core 自身**的起停与装配/就绪判决。
- broker 通道性质、risk 快照内容、data 的 503、orchestration 的任务状态，分别看对应服务仓的 runbook。
- 启动期 fail-closed 行为**不允许**为了「先起来」而临时放宽；要改形态就按 `docs/TASKS.md` 的条目走裁决。
