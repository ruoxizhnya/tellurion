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

## broker 必须先于 core 可用（启动前置，不是降级项）

`analysis-service` 的 exec-engine 模块在 `Boot` 期**无条件**构造 broker 通道
（`cmd/analysis/kernel_wiring.go` → `pkg/bootstrap.BuildExecutionTrader` →
`pkg/brokerhttp.NewTraderFromEnv`）。所以「broker 没起来 / 不可达」不是少一个
可选依赖，而是 **core 拒起**（fail-closed，进程内撮合已随 `SVC-CORE-BROKER-RETIRE`
删除，没有静默回退）。编排侧不要把这种拒起误判成 core 缺陷。

三类失败原文（排查入口见 [`../docs/runbook.md`](../docs/runbook.md) §4）：

| 形态 | 失败原文（片段） | 含义 / 处置 |
|---|---|---|
| 容器（默认栈 / full） | `wait-for-deps: 等 broker:8091 超过 60s 仍不可达，放弃启动。` | entrypoint 在 `BROKER_URL` 的 host:port 探不通时退出 1；先起 broker（默认栈已 `depends_on: broker: service_healthy`）。超时由 `WAIT_FOR_DEPS_TIMEOUT` 控制 |
| 本机直跑 | `kernel assembly failed error="broker_http: BROKER_URL is required — in-process matching was retired ..."` | 没给 `BROKER_URL`；显式指向 broker 网关 |
| 任一形态 | `broker_http: channel mismatch: BROKER_CHANNEL=live but peer reports "mock" ...` / `cannot verify peer channel via ... — fail-closed` | 声明与对端 `/version` 自报不一致或对端不可达/无 `channel`；改声明或升级/重建 broker 镜像，**不许**放宽成「验证不了就放行」 |

- 两种编排都必须给 `analysis-service` 注入 `BROKER_URL`：默认栈与 `--profile full`
  由 compose 给 `http://broker:8091`；本机直跑按 [`runbook §1.2`](../docs/runbook.md)
  显式给。`worker` 不直连 broker（它经 HTTP 调 core），不受这条约束。
- 正常日志对照：`broker peer channel verified channel=mock endpoint=http://broker:8091/version`
  + `execution channel is MOCK`。缺这两行说明核对没走到，按上表排查。
- 若产品上要支持「只做分析、不下单」的部署形态，那是把 exec-engine 改成可选模块的
  **另一个决定**（须单独立单，并说明不接交易时对账/风控覆盖口子由谁负责），
  不在本编排内隐式支持。

## 跨仓服务地址：默认值面向 Docker Desktop，Linux 需显式指定

`ai-service` 调 strategy 资产服务用 `STRATEGY_ASSET_URL`（写侧全闸，另需
`STRATEGY_ASSET_TOKEN`，见下节）。取值按部署形态分两种：

| 形态 | 取值 | 说明 |
|---|---|---|
| `--profile full`（strategy 是同一 compose 网络里的容器） | `http://strategy:8090`（**默认值**） | compose 服务名；Linux / CI / Docker Desktop 通用 |
| strategy 在宿主机直跑（不进 compose） | **显式** `STRATEGY_ASSET_URL=http://host.docker.internal:8090` | 该主机名面向 Docker Desktop 的本地栈；**Linux 的 Docker 默认不解析它** |

- Compose 单文件**不能**按 profile 切换 `environment`：profile 只控制服务起停，
  不是条件分支。所以不存在"profile 级默认值"，只能"默认值 + 调用方 env 覆盖"。
- `analysis-service` 的 `STRATEGY_ASSET_RESOLVE_URL` 仍是
  `host.docker.internal:8090` 缺省；Linux/CI 上要连 compose 内的 strategy，
  同样需显式设成 `http://strategy:8090`。
- 文件头明确**不预置** `extra_hosts: ["host.docker.internal:host-gateway"]`
  （Docker Desktop 上实测会把它从 IPv4 变 IPv6）。Linux 上确实要走宿主网关时
  按需自加，不要指望默认可用。

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