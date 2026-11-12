# 部署清单与凭证轮换

本目录是 **core 仓的部署面**（裁-17 ①）：`docker-compose.yml`（仓根）是**唯一**
编排；`k8s/` 清单已于 2026-11-12 退役（`CORE-K8S-EXIT`），只留
[退役说明](k8s/README.md)与 `archive/` 历史件。

## 本地编排：单一文件 + profile（2026-11-11 定案）

本地只留**一个** `docker-compose.yml`，差异全靠 profile（`D-DEPLOY-1 = A` 已撤销）：

| 命令 | 起的服务 |
|---|---|
| `docker compose up -d` | `data-service` :8081 / `broker` :8091 / `analysis-service` :8085 / `ai-service` :8086 |
| `docker compose --profile full up -d` | 默认 4 个 + `risk` :8089 / `strategy` :8090 / `analytics` :8087 / `orchestration` :8088 |
| `--profile orchestration-worker` | `worker`（`cmd/worker`，需 `ORCHESTRATION_URL`） |
| `--profile tools` | `dualrun`（一次性验收门，跑完即退） |
| `--profile nt` | `nt-sidecar`（可选旁路样本） |

- **数据库不在容器里**：PG 用宿主机原生 16.15，服务经 `host.docker.internal:5432`
  连；`docker compose` 不再有 postgres / redis 服务。
- **默认栈 broker 是 mock 通道**：订单永不触达真实柜台。要接真实柜台必须
  `BROKER_CHANNEL=live` **且**对端 `/version` 自报 `live`，否则 `analysis-service`
  在装配期拒起（`pkg/brokerhttp.verifyPeerChannel`）。
- **高额度 e2e**：`RATE_LIMIT_PER_MINUTE=100000 docker compose up -d --force-recreate
  --no-deps analysis-service data-service`（原 `docker-compose.e2e.yml` 覆盖层已删）。
- 跨仓 7 份 `<repo>/deploy/compose.yaml` **不并入、不改动**：它们是各仓自证
  Dockerfile / health 的资产，联调一律用本仓的 profile。

`k8s/` 形态已退役（2026-11-12，`CORE-K8S-EXIT` / D7=a）：历史清单在
`deploy/k8s/archive/`，不参与部署；恢复须另立 ADR，见
[`k8s/README.md`](k8s/README.md)。

## 凭证只经 env 注入

| env | 来源 Secret / key | 使用方 | 用途 |
|---|---|---|---|
| `JWT_SECRET` | `tellurion-jwt` / `jwt-secret` | analysis / data / risk / analytics | 验证入站 JWT（同一把，跨服务互认） |
| `RISK_SERVICE_RESOLVE_TOKEN` | `service-tokens` / `risk-service-resolve-token` | analysis | 出站调 risk `POST /api/risk/resolve` |
| `DATA_SERVICE_TOKEN` | `service-tokens` / `data-service-token` | analytics | 出站调 data `GET /datasets/*` |
| `ANALYTICS_CORE_TOKEN` | `service-tokens` / `analytics-core-token` | analytics | 出站调 core `POST /api/backtest` |

纪律：**只经 env、不落库、不进镜像**。清单里只有 `secretKeyRef`，没有任何
token 真值；Secret 由运维在集群里创建（`kubectl create secret generic ...`）。

三个服务 token 一律用 `service` 角色（裁-17），**不复用 `trader`**：`trader`
同时能调 `POST /api/execution/orders` 与 emergency-flatten，配置泄露就等于
实盘下单权；`service` 在策略层永远进不了交易 / 管理面。

## 轮换周期

| 凭证 | 周期 | 动作 |
|---|---|---|
| `JWT_SECRET` | **每 90 天** | 在 `tellurion-jwt` 写入新值后**同一窗口整体滚动**四个服务（analysis / data / risk / analytics）。只滚一半会让新旧 token 互不认，跨服务调用集体 401。 |
| user access token | 15 分钟（自动） | 无运维动作；refresh token 7 天。 |
| 三个服务 token | **每 30 天** | 用 tellurion-kit 的 `IssueServiceToken(subject, "service", ttl)` 重新签发，更新 `service-tokens` 的三个 key，滚动**消费方**（analysis / analytics）。签发入口归 G2 的 kit，不在 core 复制实现。 |

轮换窗口选择在无回测排程的时段：`JWT_SECRET` 轮换会拒绝所有在途旧 token，
正在跑的 run 不受影响（它们已在进程内），但新的提交会 401 直到所有副本滚完。

## 缺一项不是降级，是阻断

core → risk 的 `POST /api/risk/resolve` 在**配置了但取不到**时按裁-11 让该
run 直接 `failed`（`risk_rules_unavailable`）。所以 `RISK_SERVICE_RESOLVE_TOKEN`
或 risk 的 `JWT_SECRET` 缺失时，回测不是"少一个元数据"，而是全量提交失败。