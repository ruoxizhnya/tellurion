---
status: evergreen
last-verified: 2026-11-12
verified-by: 2026-11-12 `CORE-REDIS-EXIT`：删除 `redis-deployment.yaml`，按实测重数清单为 7 个 YAML 文件 / 11 个对象（Deployment×3、Service×4、ConfigMap/Ingress/Namespace/StatefulSet 各 1），并把两处口径不一致改为两处；2026-11-11 阶段 B core 对齐（B5-4）：按清单实测逐项核对 kind / 镜像 / 副本数 / 端口；同日补 PG 主版本核对（本机运行实例 16.15 实测、k8s 已对齐 16、生产版本未知）与 `replicas: 2` 的进程内状态核实
---

> **归档说明（2026-11-12，`CORE-K8S-EXIT`）**：本文件与同目录 7 份 YAML 已随
> k8s 形态退役整体移入 `deploy/k8s/archive/`，内容保持退役前的原样，**不再
> 参与任何部署**。退役理由、消费者取证与重新采用流程见
> [`../README.md`](../README.md)。

# `deploy/k8s/` —— **跨仓部署资产**，不是本仓的构建产物

> 这批清单暂由 core 托管（G-05 裁决：先托管、写明跨界与迁出条件），但它描述的是
> **整套栈**，不是 core 一个仓库的部署形态。改之前先读下面的「迁出条件」。

## 清单（7 个文件，11 个对象）

| 文件 | 内容 | 镜像来源 |
|---|---|---|
| `namespace.yaml` | Namespace `tellurion` | — |
| `configmap.yaml` | 应用 env：`DATABASE_*` / `DATA_SERVICE_URL` / `DATA_MARKET_SCHEMA` / `LOGGING_LEVEL`（`REDIS_URL` 与 Redis 依赖已于 2026-11-12 随 `CORE-REDIS-EXIT` 彻底删除） | — |
| `analysis-deployment.yaml` | Deployment（`replicas: 2`）+ Service `analysis-service:8085`；探针 `/api/health` | **本仓**：`tellurion/analysis:latest` |
| `data-deployment.yaml` | Deployment + Service `data-service:8081`；`DATABASE_URL` 由 configmap 四键 + Secret 密码拼出 | 跨仓：`tellurion/data:latest`（`tellurion-data`） |
| `web-deployment.yaml` | Deployment + Service `web:8080`（`targetPort: http`） | 跨仓：`tellurion/web:latest`（`tellurion-ui`） |
| `postgres-statefulset.yaml` | Service `postgres:5432`（headless）+ StatefulSet（`replicas: 1`，`postgres:16-alpine`）；`POSTGRES_USER` / `POSTGRES_DB` 用 `configMapKeyRef` 从 `DATABASE_USER` / `DATABASE_DATABASE` 翻译。逐项事实见下一节 | 官方镜像 |
| `ingress.yaml` | Ingress `tellurion.local` → `web:8080` | — |

`broker` / `risk` / `strategy` / `analytics` / `orchestration` **不在**这些清单里 ——
它们各自成仓。**本地**统一栈（仓根 `docker-compose.yml` 的默认 profile）已把
broker 接进 core；**k8s 形态还没有**：既无 broker Deployment，也没有
`BROKER_*` 注入，走 k8s 部署前必须先补这一层（`CORE-BROKER-STARTUP-PRECONDITION`
③⑤）。

## PG StatefulSet 逐项事实（2026-11-11 对照 `postgres-statefulset.yaml`）

| 项 | 实际值 | 位置 |
|---|---|---|
| 对象 | headless Service（`clusterIP: None`）+ StatefulSet，`replicas: 1`，`serviceName: postgres` | `postgres-statefulset.yaml` L1-15 / L22-23 |
| 镜像 | `postgres:16-alpine`，`imagePullPolicy: IfNotPresent` | L41-42 |
| `PGDATA` | **未设置**，用官方镜像默认 `/var/lib/postgresql/data` | 全文无 `PGDATA` |
| initdb 参数 | **无**：没有 `POSTGRES_INITDB_ARGS`，也没有 `command` / `args` 覆盖；首启 initdb 完全由官方镜像 entrypoint 执行 | 全文 |
| `pg_hba` / 认证 | **仓库内没有任何 `pg_hba.conf` 资产**，也没有 `POSTGRES_HOST_AUTH_METHOD`；主机认证方式由官方镜像 entrypoint 依 `POSTGRES_PASSWORD` 推导，本仓不做定制 | 全文 |
| env（共 3 个） | `POSTGRES_DB` ← configmap `DATABASE_DATABASE`；`POSTGRES_USER` ← configmap `DATABASE_USER`；`POSTGRES_PASSWORD` ← Secret `postgres-credentials` / `password`。**容器侧名 ≠ configmap 键**（AUD-39），别「顺手统一」 | L44-62 |
| 探针 | readiness + liveness 都是 exec `pg_isready -U postgres -d tellurion`（initialDelay 10/30、period 10/30、timeout 5）。**用户名与库名写死**，与 configmap 的 `DATABASE_USER` / `DATABASE_DATABASE` 隐式耦合；`pg_isready` 只判「服务器是否接受连接」、不校验凭据与库名，所以改 configmap 不会立刻把探针打挂，但探针文案会与真实配置漂移 | L63-71 / L72-80 |
| 存储 | `volumeClaimTemplates`：`data`，`ReadWriteOnce`，`20Gi`，**未指定 `storageClassName`**（吃集群默认 SC）；挂到 `/var/lib/postgresql/data`，`subPath: postgres` ⇒ PGDATA 实际是 PVC 下的 `postgres/` 子目录，备份 / 升级的对象是它，不是 PVC 根 | L83-96 / L58-61 |
| 资源 | requests 250m / 512Mi，limits 1000m / 2Gi | L81-82 |
| 其它 | 无 `securityContext`、无自定义 `strategy`、无 PodDisruptionBudget、无备份侧车 | 全文 |

## PG 主版本：统一到 16（k8s 已对齐，本轮不换 tag）

| 落点 | 版本 | 证据 |
|---|---|---|
| 本机**在跑**的实例 | **16.15** | Windows 服务 `postgresql-x64-16`，`-D "C:\Program Files\PostgreSQL\16\data"`，`data\PG_VERSION` = `16`，`:5432` 正在监听；`psql --version` / `pg_ctl --version` 均为 16.15 |
| 本机便携二进制（`tools/local-infra.sh` 默认指向 `.workbuddy/binaries/postgres`） | 17.5 | `postgres --version`；**当前未运行**，5432 被上面的 16 服务占用，两者不能同时起 |
| core CI | `postgres:16` ×2 | `.github/workflows/ci.yml` L26、L210 |
| 本仓 `docker-compose.yml` | **无 postgres 服务** | 2026-11-11 起数据库走宿主机原生 16.15，容器经 `host.docker.internal:5432` 连（原 `docker-compose.services.yml` 的 `postgres:16-alpine` 随文件删除） |
| `deploy/k8s/` | `postgres:16-alpine` | `postgres-statefulset.yaml` L41 |
| `tellurion-agent/deploy/compose.yaml` | `postgres:17.5-alpine` | L12（**跨仓，唯一用 17 的部署资产**；本仓不改，仅登记） |
| 生产 | **未知** | 已查：core 全部 compose / `deploy/k8s/` / `.github/workflows/`、`README.md`、`docs/ARCHITECTURE.md`、`docs/SPEC.md`、`docs/TASKS.md`、`docs/archive/*`、`docs/migrations/*`、`tellurion-docs`、`tellurion-agent/deploy/compose.yaml` —— **没有任何「生产 PG 版本」的声明**，`17.5` 只出现在历史注释与归档里 |

**结论**：

1. 统一到 **PostgreSQL 16**（主版本 16）。依据：本机在跑实例是 16.15，core CI 两个 job
   与 k8s 的实际镜像都是 16。本地 compose 已不含 PG 容器（走宿主机原生 16.15）。
   **k8s 的 `postgres:16-alpine` 已经对齐，本轮不改镜像 tag。**
2. **生产版本未知 ⇒ 挂起待确认，不猜、不挑一个版本填进去。** 确认后若生产不是 16，
   要么把生产升到 16，要么把本仓 CI / k8s 一起改到生产主版本 ——
   全栈只能有一个主版本，且必须走下一节的升级流程，不能只改 tag。
3. 本轮已把 `docker-compose.yml` 文件头与 `tools/check_deploy_consistency.py` 里
   「PostgreSQL 17.5」的旧口径改成实测事实（16.15 运行中；17.5 是未运行的便携二进制）。

## 主版本升级必须走升级流程 —— **换 tag ≠ 升级**

> **这条写死**：`postgres:17` ↔ `postgres:16` **任一方向**直接改镜像 tag 都会起不来。
> PG 主版本之间数据目录格式不兼容，新版本二进制拒绝启动旧数据目录
> （`FATAL: database files are incompatible with server` /
> `The data directory was initialized by PostgreSQL version 16, which is not compatible with this version 17.x`），
> 降级同理。换 tag 的结果是 Pod 进 `CrashLoopBackOff`，数据目录原封不动 ——
> 不是「升级完成」，是「根本没起来」。

另外本 StatefulSet 的 PGDATA 在 PVC 的 `postgres/` 子目录（`subPath: postgres`），
备份 / 迁移要取 `<PVC>/postgres`，不是 PVC 根目录。

### 方案 A（推荐）：逻辑 dump / restore

1. 记录源版本并留档：
   ```bash
   kubectl -n tellurion exec postgres-0 -- psql -U postgres -d tellurion -tAc "select version()"
   ```
2. 停写：`kubectl -n tellurion scale deploy/analysis-service deploy/data-service --replicas=0`
3. 全库逻辑备份（角色 + 库 + 数据；本清单未装扩展，`pg_dumpall` 足够）：
   ```bash
   kubectl -n tellurion exec postgres-0 -- pg_dumpall -U postgres > tellurion-pg16-$(date +%Y%m%d).sql
   ```
4. 用**新 PVC** 起目标主版本：改 `image` 到 `postgres:17-alpine`，并换掉 StatefulSet
   名或挂一块新卷 —— **绝不复用旧 PVC**（旧数据目录正是新版本拒绝启动的对象）。
5. 恢复：
   ```bash
   kubectl -n tellurion exec -i postgres-0 -- psql -U postgres < tellurion-pg16-YYYYMMDD.sql
   ```
6. 校验：关键表行数对拍（`public.users` / `audit.message_log` /
   `run.backtest_results` / `quant.strategy_state` / `market.*`）、`pg_isready`、
   应用 `GET /readyz` 返回 200。
7. 回滚：旧 PVC 与旧 StatefulSet **保持不动**，改回旧 tag / 旧 PVC 即可（这正是
   「用新 PVC」的原因）。

### 方案 B：`pg_upgrade`（原地，快但默认不可回退）

官方 `postgres:*` 镜像只带一个主版本的二进制，`pg_upgrade` 需要**同时**有旧 / 新两个
`bin` 目录，通常用 `tianon/postgres-upgrade` 一类镜像或临时 Job 完成：

```bash
# 伪代码：同一 Pod 内挂新旧两个 bin + 同一数据卷
pg_upgrade --check \
  --old-bindir /usr/lib/postgresql/16/bin --old-datadir /var/lib/postgresql/data/postgres \
  --new-bindir /usr/lib/postgresql/17/bin --new-datadir /var/lib/postgresql/data/postgres-new
# --check 通过后再去掉 --check 执行
```

要点：先跑 `--check`；**不要用 `--link`**（硬链接会就地改写旧文件，旧集群不可回退）；
升级后 `vacuumdb --all --analyze-in-stages`；旧数据目录保留到验收通过再删。

### 演练门槛

- 演练必须在**一次性 PVC + 一次性命名空间**上做，禁止拿生产 PVC 首次尝试；
- 演练记录写回本节（日期 / 源版本 / 目标版本 / 方案 / 行数对拍 / 回滚耗时）；
- **当前状态：无任何演练记录。** 主版本变更前必须先补这一节。

## 两处口径不一致（核实结论，不是笔误）

1. **PG 在集群内 vs「PG 不在容器内」**：`docs/ARCHITECTURE.md` 的「都不在容器内」
   指**本地统一栈（`docker-compose.yml`）**——2026-11-11 起它连 postgres 服务都没有，
   全部经 `host.docker.internal:5432` 连宿主机原生 16.15；k8s 形态自带 `postgres`
   StatefulSet（本目录）。两种形态的备份 / 升级 / 扩容责任完全不同，别把一句话
   套到两处。
2. **PG 主版本**：见上一节 —— 本机在跑 16.15、CI / k8s 都是 16，
   **k8s 已对齐，无需换 tag**；生产版本**未知**，已挂起待确认。此前「本地与生产
   口径是 17.x」的说法经核实不成立：17.5 只是未运行的便携二进制与历史注释。

## `analysis-deployment.yaml` 的 `replicas: 2`：已核实，**当前不安全**

这不是「未验证的容量假设」，而是有明确证据的重复执行：

| 证据 | 位置 |
|---|---|
| 每个进程都启动 PeriodicAlertLoop | `cmd/analysis/main.go:185`（`go alertLoop.Start(alertCtx)`），循环体 `alert_loop.go:261` 用 `time.NewTicker` |
| 告警历史是**进程内** ring buffer，不跨副本共享 | `alert_loop.go:50-59`（`AlertHistory` 结构 + `mu sync.RWMutex`） |
| 每个进程都跑 risk-events relay ticker | `cmd/analysis/wiring/risk_events.go:319`（`time.NewTicker(s.relayEvery)`） |
| 全仓无 advisory lock / leader election | grep `advisory_lock` / `pg_try_advisory` / `leader` → 0 命中 |

⇒ 两个副本会**各算一遍、各派发一遍**同一批告警（重复通知），且
`GET /alerts/history` 返回哪一份取决于命中哪个 Pod（结果随负载均衡漂移）；
SSE 与指标同理是进程内状态。

**处置建议（待裁决，本轮不改）**：要么把 `replicas` 收到 1 并在清单里写明「扩副本前
必须先加 leader election / advisory lock」，要么先给 alertLoop 与 relay 加跨副本互斥
再扩。当前保持 2 属于**已知重复派发**状态，别当作已验证的容量结论。

## 迁出条件（满足任一即把清单搬走，不保留双份）

1. `tellurion-ui` 与 `tellurion-data` 各自维护自己的 Deployment / Service 片段，镜像
   tag 由各自 CI 决定；
2. 出现独立部署仓或 Helm chart；
3. PG 改由托管服务提供（届时它不再是「跨仓资产」，而是环境外部依赖）。

在达成之前，改本目录必须同时跑：

```bash
python tools/check_deploy_consistency.py   # compose ↔ k8s 的端口 / env 口径护栏
```
