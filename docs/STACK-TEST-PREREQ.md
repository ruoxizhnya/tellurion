---
status: active
last-verified: 2026-11-12
verified-by: 2026-11-12 综合测试前起栈验收——用当前 HEAD（`87cc583`）构建 `tellurion-core:head`，以隔离卷 `tellurion-next_next-pgdata` + 独立容器 `tellurion-next-pg-1` 起 `--profile full` 完整栈（9 容器全 healthy，宿主部署库 `tellurion` 未触碰）。逐服务 `/health` 与 `/readyz` 实测；新库 `core_schema_migrations` 首启 85 行、三次启动 `max(applied_at)` 不变（零重放）；执行面 6 条与读面 5 条端点逐个实测（见 §5）。覆盖层落在 `deploy/compose/isolated-test.override.yml`，一键重建命令见 §2。2026-11-12 追补：ai-service 必须用 agent HEAD 重建（旧镜像早于 `787f9ff`，上游 `/api/explore/review/pending` 404 会让 core 聚合退化为 `unavailable`）；重建后上游 200、core `/api/review/pending` 转 `available`，§2 已固化「重建全部服务镜像」与陈旧镜像自检。
---

# 起栈与测试前提

本文件是**综合测试的唯一施工前提**：照着 §2 起栈，按 §5 的表打端点即可，不需要再读 compose 注释。

## 1. 当前可用栈（2026-11-12 实测，正在运行）

- 镜像：`tellurion-core:head`，由当前 HEAD `87cc583` + `Dockerfile.service` 本地构建（image ID `e662461b8ce1`）。
- Compose 项目名：`tellurion-next`（与旧栈 `tellurion` 隔离，容器名前缀 `tellurion-next-`）。
- 数据库：**容器内独立 PG**（`postgres:16-alpine`，库名 `tellurion`，卷 `tellurion-next_next-pgdata`，宿主仅回环发布 `127.0.0.1:15432`）。**不是**宿主部署库。

| 服务 | 容器 | 宿主端口 | 健康路由实测 |
|---|---|---|---|
| analysis-service | `tellurion-next-analysis-service-1` | 8085 | `/health` 200；`/readyz` 200 `{"checks":{"database":"ok","schema":"ok"}}` |
| data-service | `tellurion-next-data-service-1` | 8081 | `/healthz` 200；`/readyz` 200 `{"store":"postgres"}` |
| ai-service | `tellurion-next-ai-service-1` | 8086 | `/health` 200；`/readyz` 200 |
| risk | `tellurion-next-risk-1` | 8089 | `/healthz` 200；`/readyz` 200 |
| strategy | `tellurion-next-strategy-1` | 8090 | `/healthz` 200；`/readyz` 200 `{"store":"postgres"}` |
| analytics | `tellurion-next-analytics-1` | 8087 | `/healthz` 200；`/readyz` 200 `{"store":"postgres"}` |
| orchestration | `tellurion-next-orchestration-1` | 8088 | `/healthz` 200；`/readyz` 200 `{"store":"postgres"}` |
| broker | `tellurion-next-broker-1` | 8091 | `/healthz` 200；`/readyz` **404（按设计无此路由，G4 裁决）** |
| pg（仅测试库） | `tellurion-next-pg-1` | 15432 | `pg_isready` healthy |

## 2. 一键重建 / 一键起栈

```bash
# ① 重建 core 镜像（每次都做；tag 固定 tellurion-core:head）
docker build -f Dockerfile.service -t tellurion-core:head .

# ② 同批重建六个兄弟服务镜像 —— 不能只重建 core！
#    镜像 tag 必须与根 compose 里的一致，否则 compose 会静默沿用旧镜像。
( cd ../tellurion-data          && docker build -t tellurion/data:latest . )
( cd ../tellurion-broker        && docker build -t tellurion-broker:dev . )
( cd ../tellurion-agent         && docker build -t tellurion-ai-service:latest . )
( cd ../tellurion-risk          && docker build -t tellurion-risk:dev . )
( cd ../tellurion-strategy      && docker build -t tellurion-strategy:dev . )
( cd ../tellurion-analytics     && docker build -t tellurion-analytics:dev . )
( cd ../tellurion-orchestration && docker build -t tellurion-orchestration:dev . )

# ③ 让出端口（旧栈指向宿主部署库，必须先停；stop 不删容器，随时可恢复）
docker compose --profile full stop

# ④ 起隔离栈（9 个服务，--wait 等全部 healthy）
#    限流高额度只能从命令行给（override 里写死会被护栏拦下，见 §4.4）
RATE_LIMIT_PER_MINUTE=100000 docker compose -p tellurion-next -f docker-compose.yml \
    -f deploy/compose/isolated-test.override.yml --profile full up -d --wait

# ⑤ 冒烟
curl -s http://127.0.0.1:8085/readyz
```

- **陈旧镜像自检**（比时间，不比 tag）：每仓 `git log -1 --format='%h %cI'` 的提交时间必须早于对应镜像的 `docker images --format '{{.Repository}}:{{.Tag}} {{.CreatedAt}}'`。2026-11-12 实测过一次教训：只重建 core 时 ai-service 仍是 01:44 的旧镜像，而 agent 的 `787f9ff`（`GET /api/explore/review/pending`）是 16:25 提交——上游 404，core 的 `/api/review/pending` 就退化成 `ai_explore: unavailable`，看起来像"真缺实现"。
- 镜像 tag 变了（或 image ID 变了）compose 才会自动重建容器；拿不准就在 ④ 后加 `--force-recreate`。
- 覆盖层只做三件事：加独立 PG、把**八个**有库服务的 DSN 指过去（含 `orchestration-worker`）、把 core 与 worker 换成 HEAD 镜像并放大限流/打开 kill-switch。**它必须叠加在根 `docker-compose.yml` 上**，不是第二份部署形态。
- **worker 是第 8 个 DB 消费者**：根 compose 默认 `DATABASE_HOST=host.docker.internal`（连宿主部署库）；覆盖层必须把它改到 `pg`。漏改的后果不是「跑不通」，而是**结果写进了另一个库**——完成态 run 在 core 读面全 500/404，同时污染宿主部署库。异步用例起 worker 用：
  ```bash
  docker compose -p tellurion-next -f docker-compose.yml \
      -f deploy/compose/isolated-test.override.yml \
      --profile full --profile orchestration-worker up -d --wait worker
  ```
- 删库重来：`docker compose -p tellurion-next -f docker-compose.yml -f deploy/compose/isolated-test.override.yml down -v`（`-v` 会删测试卷，不会碰部署库）。

## 3. 迁移账本验收（本栈实测）

| 项 | 结果 |
|---|---|
| 首启行数 | `count=85`，`min(version)=1`，`max(version)=85`，`max(applied_at)=2026-11-12 16:02:05.260989+00` |
| 与代码条数 | 一致（当前 `allMigrationStatements()` = **85**） |
| 二次/三次启动 | 仍 `85` 行、`max(applied_at)` 不变 ⇒ **零重放**（重启与重建容器各验一次） |

```bash
docker exec tellurion-next-pg-1 psql -U postgres -d tellurion -tAc \
  "select count(*),min(version),max(version),max(applied_at) from core_schema_migrations;"
```

- 迁移日志是 debug 级，`info` 下不输出「Applying migration」；**判定口径用账本时间戳**，不要用日志行数。
- 宿主部署库 `tellurion` **继续保持无账本 + 旧镜像**（本轮刻意不动）；下次用该 HEAD 镜像起正式栈即完成一次性接管。

## 4. 必需 env 与已知坑

### 4.1 数据库

- core / ai：「四键」`DATABASE_HOST` / `DATABASE_PORT` / `DATABASE_USER` / `DATABASE_PASSWORD` / `DATABASE_DATABASE`；覆盖层填 `pg:5432`。
- orchestration-worker：与 core 同「四键」——它写 core 的结果面与留痕面，**必须与 core 同库**。覆盖层填 `pg:5432`；漏改就会写进宿主部署库。worker 没有 HTTP 面，健康判据是「注册成功 + 能领到活」（`docker logs` 里有 claim 记录、编排 `GET /api/runs/jobs?status=running` 能看到它）。
- data：`DATABASE_URL`；risk / strategy / analytics / orchestration：各自的 `*_DATABASE_URL` / `*_DSN`。

### 4.2 装配期硬前置（缺一即拒起）

- `BROKER_URL` + `BROKER_CHANNEL=mock` + `BROKER_ALLOW_MOCK=true` + `BROKER_SERVICE_TOKEN`：启动时 GET `{BROKER_URL}/version` 对拍通道，对不上直接 fatal。broker 必须在栈里（默认 profile 已含）。
- **`RISK_SERVICE_RESOLVE_ENABLED=false`**：不显式给会尝试 POST `http://localhost:8089/api/risk/resolve`（容器里 localhost 是自己）然后拒起。要真快照才置 `true`，并保证 `RISK_SERVICE_RESOLVE_URL=http://risk:8089` 可达。
- `AUTH_INSECURE=true`（或 `AUTH_INSECURE_EXPOSURE=loopback-published`）：容器绑 `0.0.0.0`，无 secret 时必须显式声明才放行。
- 容器内地址一律用 **compose 服务名**：`DATA_SERVICE_URL=http://data-service:8081`、`AI_SERVICE_URL=http://ai-service:8086`、`STRATEGY_ASSET_URL=http://strategy:8090`；只有「访问宿主机上的东西」才用 `host.docker.internal`。

### 4.3 认证三态取法（以 analysis-service 为准）

| 态 | 条件 | 结果 | 本栈 |
|---|---|---|---|
| 1 | 有 `JWT_SECRET` | 写面要 token + 角色（trader/admin） | — |
| 2 | 无 secret、无声明、回环绑定 | 进程起，写面 503 `auth_unavailable` | 容器形态不可得 |
| 3 | 无 secret + 显式声明（`AUTH_INSECURE=true` 或 `AUTH_INSECURE_EXPOSURE=loopback-published`） | 放开写面（WARN 披露） | **本栈默认**，UI 测试直接用 |
| 4 | 无 secret、无声明、非回环绑定 | 拒起 | — |

签 service token：`JWT_SECRET=... go run ./tools/issue_service_token.go -sub service:analysis -ttl 720h`。

### 4.4 测试专用开关（起栈命令行给）

- `RATE_LIMIT_PER_MINUTE=100000`：默认 100，UI/综合测试高频打面必 429。**只能放在起栈命令行**（§2 ④）——写进 override 会被 `TestCompose_DefaultRateLimitIs100AndOverridable` 判红：部署面写死高额度会让 env 覆盖变成安慰剂。重启用例若重新 `up`，记得带上这个前缀。
- `TRADING_EMERGENCY_TOKEN=dev-only-emergency-token`：不设时 `POST /api/execution/emergency-flatten` 恒 503（按设计）；该值仅限本机 mock 栈，由覆盖层给。

## 5. 综合测试端点矩阵（2026-11-12 实测，空库 + mock 通道）

| 方法 | 路径 | 实测 | 说明 |
|---|---|---|---|
| GET | `/api/execution/orders` | 200 | `{"count":N,"orders":[...]}` |
| GET | `/api/execution/orders/:id` | 200 | 未命中回落 trader 查询 |
| GET | `/api/execution/positions` | 200 | `{"count":0,"positions":[]}` |
| GET | `/api/execution/account` | 200 | `total_assets=100000000 cash=100000000` |
| POST | `/api/execution/orders` | **201** | body `{"symbol":"600000","side":"long","type":"market","quantity":100}` → `order_id=core-...` |
| POST | `/api/execution/orders/:id/cancel` | 200 | `{"status":"cancelled"}`（mock 通道 pending 单可撤） |
| POST | `/api/execution/emergency-flatten` | 200 | 需 `Authorization: Bearer dev-only-emergency-token` + body `{"reason":...,"confirmation_token":...}`；无 token = 401，token 不符 = 403 |
| GET | `/api/signals` | 200 | `{"signals":[],"count":0,"limit":100}` |
| GET | `/api/review/pending` | 200 | `{"pending":0,"as_of":"...","sources":[{"name":"ai_explore","status":"available","pending":0}]}` —— 前提是 ai-service 用 agent HEAD 重建（上游 `GET /api/explore/review/pending`）；旧镜像会 404 并让该源退化为 `unavailable` |
| GET | `/api/factor-analytics/factors` | 200 | `{"factors":[],"supported":["momentum","value","quality"]}` |
| GET | `/api/factor-analytics/factors/{factor}/returns` | 200 | 空库返回 `points:[]` + `start_date/end_date`（默认 90 天窗口） |
| GET | `/api/factor-analytics/factors/{factor}/ic` | 200 | 同上 |

- 空心数据（`[]`）是**空库的正常结果**，不是接口不可用；判可用看状态码与响应形状。
- 限流放大后仍建议 UI 测试用例之间复用连接，避免瞬时打爆。

## 6. 已知限制

- `broker` 无 `/readyz`（G4 已裁不补）；就绪判据用 `/healthz` + core 的 `readyz`。
- `GET /api/review/pending` 的 `ai_explore` 源依赖 ai-service 镜像与 agent HEAD 一致：旧镜像（早于 agent `787f9ff`）没有 `/api/explore/review/pending`，该源退化 `unavailable`。2026-11-12 实测踩过这条，已固化为 §2 ② 的必做步。
- 隔离栈数据全部在 `tellurion-next_next-pgdata`，`down -v` 即回到零；部署库不受影响。

## 7. 恢复旧栈

```bash
# 新栈与旧栈抢同一组宿主端口：先停新栈，再启旧栈
docker compose -p tellurion-next -f docker-compose.yml \
    -f deploy/compose/isolated-test.override.yml --profile full stop
# 旧栈（项目 tellurion，指向宿主部署库）仍存在，只是 stop
docker compose --profile full start
```
