---
status: evergreen
last-verified: 2026-11-11
verified-by: 2026-11-11 阶段 B core 对齐（B5-4）：按清单实测逐项核对 kind / 镜像 / 副本数 / 端口，并记录三处口径不一致
---

# `deploy/k8s/` —— **跨仓部署资产**，不是本仓的构建产物

> 这批清单暂由 core 托管（G-05 裁决：先托管、写明跨界与迁出条件），但它描述的是
> **整套栈**，不是 core 一个仓库的部署形态。改之前先读下面的「迁出条件」。

## 清单（8 个文件，10 个对象）

| 文件 | 内容 | 镜像来源 |
|---|---|---|
| `namespace.yaml` | Namespace `tellurion` | — |
| `configmap.yaml` | 应用 env：`DATABASE_*` / `DATA_SERVICE_URL` / `DATA_MARKET_SCHEMA` / `LOGGING_LEVEL`（`REDIS_URL` 已于 2026-11-10 删除） | — |
| `analysis-deployment.yaml` | Deployment（`replicas: 2`）+ Service `analysis-service:8085`；探针 `/api/health` | **本仓**：`tellurion/analysis:latest` |
| `data-deployment.yaml` | Deployment + Service `data-service:8081`；`DATABASE_URL` 由 configmap 四键 + Secret 密码拼出 | 跨仓：`tellurion/data:latest`（`tellurion-data`） |
| `web-deployment.yaml` | Deployment + Service `web:8080`（`targetPort: http`） | 跨仓：`tellurion/web:latest`（`tellurion-ui`） |
| `postgres-statefulset.yaml` | Service `postgres:5432` + StatefulSet（`postgres:16-alpine`）；`POSTGRES_USER` / `POSTGRES_DB` 用 `configMapKeyRef` 从 `DATABASE_USER` / `DATABASE_DATABASE` 翻译 | 官方镜像 |
| `redis-deployment.yaml` | Deployment + Service `redis:6379` | 官方 `redis:7-alpine` |
| `ingress.yaml` | Ingress `tellurion.local` → `web:8080` | — |

`broker` / `risk` / `strategy` / `analytics` / `orchestration` **不在**这些清单里 ——
它们各自成仓，也没有任何编排资产把 core 接到 broker（见 TASKS
`CORE-BROKER-STARTUP-PRECONDITION` 的前提改写）。

## 三处口径不一致（**不是笔误**，需要裁决）

1. **Redis 部署了但没人读**：`redis-deployment.yaml` 仍在，而 `configmap.yaml` 已按
   `SVC-CORE-DATA-RETIRE` 删掉 `REDIS_URL`（2026-11-10）—— 集群里会白跑一个 Redis。
   删或留取决于缓存是否接线回来（`docs/ARCHITECTURE.md` §4）。
2. **PG 在集群内 vs「PG 不在容器内」**：`docs/ARCHITECTURE.md` 那句话只对
   `docker-compose.yml` 成立；k8s 形态自带 StatefulSet，两种形态的备份 / 升级 /
   扩容责任完全不同。
3. **PG 主版本**：StatefulSet 用 `postgres:16-alpine`，而本地与生产口径是 17.x ——
   16→17 的 `pg_upgrade` 没有任何演练记录，上生产前必须先统一。

另有一处未核实的假设：`analysis-deployment.yaml` 的 `replicas: 2`。进程内风控快照、
SSE 连接与指标都在单进程里，多副本是否有隐形单副本假设（例如只会命中其中一个 Pod
的长连接）**没有验证过**，别把 2 当作已验证的容量结论。

## 迁出条件（满足任一即把清单搬走，不保留双份）

1. `tellurion-ui` 与 `tellurion-data` 各自维护自己的 Deployment / Service 片段，镜像
   tag 由各自 CI 决定；
2. 出现独立部署仓或 Helm chart；
3. PG / Redis 改由托管服务提供（届时它们不再是「跨仓资产」，而是环境外部依赖）。

在达成之前，改本目录必须同时跑：

```bash
python tools/check_deploy_consistency.py   # compose ↔ k8s 的端口 / env 口径护栏
```
