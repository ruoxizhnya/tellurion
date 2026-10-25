# 跨仓施工分组方案（组间可并行、组内串行）

> 快照时刻：**2026-10-25 11:10**（本地实测）。
> 取证方式：12 仓 `git status --porcelain` / `git rev-list --count origin/main..HEAD`（**已 `git fetch` 全部 12 仓**）、`git tag`、`go.mod` pin 实扫、契约仓符号 grep、撞车点文件实测。
> 真相源优先级：实测 > `docs/TASKS.md` > `AGENTS.md`。冲突已在第 6 节列出。
> **本文件不含任何业务代码**，只做分组与派发。

---

## 第 1 节 · 总览表

| 组 | 仓 | 组内串行顺序（单号） | 前置（等哪个组的哪个具体步骤） | 可与谁并行 |
|---|---|---|---|---|
| **G1** | `tellurion`（core） | QT-73 → QT-71 → QT-72 | QT-73 无外部前置；QT-71 等 **G2 的 KT-09 打出 `v0.1.2`**；QT-72 等 **G1 内 QT-71**（共用 `cmd/analysis/route_policy.go` 与 `pkg/authz` 档位表） | G1 与 G2/G3/G4/G5/G6/G7 全程并行（不同仓 / 不同文件域） |
| **G2** | `tellurion-kit` | KT-09 → KT-10 | KT-09 无前置（**QT-71 的硬前置**）；KT-10 无前置，但**不许与 KT-09 混发 tag**（台账 KT-10 ③-④） | G2 与 G1/G3/G4/G5/G6/G7 全程并行 |
| **G3** | `tellurion-orchestration` | QT-44 收尾第 ① 腿：子 run 入队（`parent_job_id` 写入方）→ ② worker 执行 | ① 需要 `TradingCalendar` 只读 port（core `QT-67` ✅ 已落 `2a4b076`，**已解除**）；② 的「派活契约」= ① 的产物 | G3 与 G1/G2/G4/G5/G6/G7 并行；**不与 G1 的 QT-72 串行**（见第 6 节 §6.7：两者同属 X0，但裁-19 已把读面判给 core） |
| **G4** | `tellurion-risk` | QT-42 收尾第 ① 腿：`bundle` → `ParseBundle` → 构建 `RiskPolicy`（core 侧消费接线）——**⛔ 2026-10-25 H3 核对：主体未开工，卡在新增前置 `QT-74`** | 依赖 **G1 的 QT-71**（service token 要先能签发、risk 的 `resolve`/`events` 策略表要先加 `"service"` 档位）。**实测两条前置都不成立**：① QT-71 本仓 ①~③ 仍 ⬜（`pkg/auth` 无 `RoleService`）；② **新增硬前置 `QT-74`**——四类规则的 `params` 字段名 / 单位在源头未冻结，core 只能猜单位 ⇒ 见第 5 节 ⑯。risk 侧档位核对已 ✅（`87c6aa9`，零代码改动，门禁 EXIT=0） | G4 与 G1/G2/G3/G5/G6/G7 并行；**不与 G3 串行** |
| **G5** | `tellurion-analytics` | QT-43 收尾第 ① 腿：R1 读面换 K9-correct 持久读面 → ② R2 契约化（`analysis` 包进 contracts） | ① 依赖 **core B6-3 ✅ 已落**（`8537ffd`，`backtest_jobs.result_status`）——**前置已解除**；② 依赖 **G2 的 contracts 发版**（`analysis` 包尚未存在，实测 contracts 仓只有 `dataprotocol/domain/engine/fees`） | G5 与 G1/G2/G3/G4/G6/G7 并行 |
| **G6** | `tellurion-data` | QT-40 收尾第 ① 腿：写面装配接线（`ETLPipeline` 挂同步作业）→ ② 物理 `SET SCHEMA` 搬迁 | ① 前置 = **core 先停第二写者**（core `pkg/data/source/etl.go` 仍在、`ON CONFLICT` upsert 仍在，实测在册）⇒ **需 G1 协调**；② 前置 = ① 完成 | G6 与 G1/G2/G3/G4/G5/G7 并行（写面域与 core 引擎域零重叠） |
| **G7** | `tellurion-broker` | QT-45 收尾第 ① 腿：X6c 全量适配器（需真实 XTP SDK + 柜台环境） | **外部环境前置，本组不具备开工条件** ⇒ 只出决策材料（见第 5 节） | — |
| **G8** | `tellurion-agent` | QT-32 的 AI 侧接线（生成 → 编译 → promote 闸门） | QT-32 依赖 **QT-41b ✅** + **QT-41 ✅** + **QT-03 ✅** 三条已全绿 ⇒ 前置已解除；**但 QT-32 本体是跨仓单**，须协调者先裁「AI 生成侧归 agent 还是 core」（见第 5 节 §5.4） | G8 与 G1~G7 并行 |
| **G9** | `tellurion-ui` | AUD-61 阶段二 ①（结构漂移 16 条） | 前置 = `feat/design-tokens-v2-palette-sweep` 分支合并。**实测该分支只存在于旧仓 `quant-trading-ui`（local + origin），`tellurion-ui` 与 origin 均无此分支** ⇒ 前置未解除 | G9 与 G1~G8 并行（但本组被外部前置阻塞） |
| **G10** | `tellurion-docs` | DOC-06（PRODUCT/VISION 重写） | 依赖**产品口径三问**（台账 DOC-06 ②）⇒ **待裁决**，只出决策材料 | G10 与 G1~G9 并行（但本组被裁决阻塞） |

---

## 第 2 节 · 分组说明

### G1 · core（`tellurion`）

**组内必须串行的原因（撞车点实测）**

| 文件 | 被哪几单碰 | 各自只许动的范围 |
|---|---|---|
| `cmd/analysis/route_policy.go` | QT-71 / QT-72 | QT-71：`Service` 第五档 + `POST /api/backtest` 登记；QT-72：`GET /api/backtest/{id}/provenance` 登记 |
| `pkg/authz/policy.go` | QT-71 | `Policy` 增 `Service []string` 第五档、`Middleware` 增一分档、`Entries()` 出 `"service"` |
| `pkg/auth/auth.go` | QT-71 | `RoleService` 进 `IsValid()`；`CanTrade()`/`CanAdmin()` 恒 false |
| `pkg/errors` `pkg/decimal` `pkg/clock` `pkg/logging` `pkg/httpclient`（5 个目录，90 处 import） | QT-73 | 整目录删除 + import 路径改写；**不许改语义** |
| `pkg/storage/postgres.go` | QT-73（间接：`pkg/storage/index_daily.go` / `macro.go` / `market_activity.go` / `supply_chain.go` 用 `apperrors`） | 只改 import 路径 |
| `cmd/analysis/handlers_backtest.go` | QT-72 | 新增 `GET /api/backtest/{id}/provenance` 端点 |

**组内可立刻做的**：**QT-73**（core 切 kit 一期五包）——零外部前置，core `go.mod` 今天**还没有** `tellurion-kit` 的 require（实测 `grep tellurion-kit go.mod` 零命中），所以第一步就是加 require + 改 90 处 import。
**组内在等外部产物的**：**QT-71** 等 G2 的 kit `v0.1.2`（`IssueServiceToken` 落地并推 tag）；**QT-72** 等 QT-71 先把 `route_policy.go` 的档位表改完（同一文件）。

### G2 · kit（`tellurion-kit`）

**组内串行原因**：KT-09 与 KT-10 **不许混发 tag**（台账 KT-10 ③-④ 原文：「单独打 tag（`v0.1.3` 或二期同批），不与 KT-09 混发」），且 kit 的 `tools/check_release_matrix` 会核 CHANGELOG ↔ tag 一致性 ⇒ 同一仓两笔发版不能同时进行。
**可立刻做的**：KT-09（`IssueServiceToken`，`v0.1.2`）。**这是整个方案里唯一的硬关键路径**——QT-71 的 ③-⑥ 明写「签发入口见 kit KT-09（`v0.1.2`）」。
**在等外部产物的**：无。

### G3 · orchestration（`tellurion-orchestration`）

**组内串行原因（数据依赖，非撞车）**：子 run 入队是 worker 执行的**输入**（worker 领到的 job 的 `parent_job_id` 由入队方写），所以「入队 → worker」必须有序。两步同仓同包（`internal/queue` + `internal/pgstore`），文件域重叠。
**可立刻做的**：① 子 run 入队（`parent_job_id` 的写入方）——前置 `TradingCalendar` 只读 port 已由 core `QT-67` ✅ 落（`2a4b076`），**已解除**。
**在等外部产物的**：② worker 执行需要 core 侧把「派活契约」定死（`Claim` 已在 `internal/queue/ports.go`，`ClaimByKernel` 已落两个实现）。

### G4 · risk（`tellurion-risk`）

**组内串行原因（跨仓数据依赖）**：`bundle` → `ParseBundle` → 构建进程内 `RiskPolicy` 是一条链，core 侧消费 `ParseBundle` 时必须先有 service token 可用（否则 `POST /api/risk/resolve` 401 ⇒ 按裁-11 口径该 run 直接 `failed` + `risk_rules_unavailable`）。
**可立刻做的**：risk 仓**自己**的策略表改动不需要等 core——但**必须等 G1 的 QT-71** 定下 `service` 角色的 token 形状，否则 role 名与 env key 会各写各的。
**在等外部产物的**：`ParseBundle` 在 core 侧的零调用点（实测：core 全仓 `grep ParseBundle` = **0 命中**）⇒ 这条腿**主体在 core**，risk 仓只提供 `Snapshot`/`SnapshotRule`/`Bundle` 三类型（实测在 `internal/rule/snapshot.go:24/37/121`）。

**2026-10-25 H3 核对结论（派发块 H3 执行前的源码核对，改判本组的开工条件）**：
- **根本阻塞 = 规则参数词典不存在**，不是「core 少写了几行」。risk 的 `SnapshotRule.Params` 是 opaque JSONB，四类规则**没有成文字段名 / 单位**（risk 自证夹具的形状就是 `{"pct":2.5}` / `{"max_pct":10}`，`tellurion-risk:internal/rule/snapshot_test.go:143-144`）；core 的限额面是 14 个标量（`tellurion:pkg/risk/manager.go:13-28` ← `risk_manager.*`）。`max_pct = 10` 按小数读是 10 倍净值、按百分数读是 10%，**差两个数量级**，而「在 core 猜」正是 H3 自己的禁区。`risk.limits` 也补不上（`tellurion-risk:docs/db-schema.md` §1：只建表，读写路径未冻结）。⇒ 新增前置 **`QT-74`**（词典在源头冻结，正文在 risk 台账）。
- **开工顺序改判**：**`QT-74` → `Snapshot`/`Bundle` 迁 contracts 发版 → core 消费**。本节第 3 节 G4 原文「迁 contracts 是下一张单，不是本单」据此继续成立；**本批不发 contracts 版本**（词典未定就迁 = 零外部消费方的 DTO 升格成契约，与 `service-dto-migration-decision-material.md` §0 对 QT-45/QT-43 判「不迁」同一条理由）。
- **一处口径可省**：core 取 bundle **不需要**给 `GET /snapshots/{hash}/bundle` 开 `service` 档位——`resolve` 的响应已整包内嵌快照（`tellurion-risk:internal/httpapi/api.go:127-130`），而 `resolve` 已开 `service`。risk 台账未落项第 5 条的「若 core 直接拉 bundle 需新开一单」条件**不触发**。
- **另两条外部缺口**：① QT-71 本仓侧仍 ⬜（`pkg/auth` grep `RoleService` = 0 命中）⇒ H3 的「同仓串行于 H1」这一句自身不成立；② 生产 `service` token 的 subject 非数字会被 kit 的 `httpadapter.go:139-141` 判 401，**且这条跨仓缝两侧测试都没覆盖**（kit adapter 测试里 `IssueServiceToken` 零命中；risk 的夹具走 `IssueTokens(1, …)` 数字 subject）⇒ 见第 5 节 ⑯ 与 risk 台账已报的那条。

### G5 · analytics（`tellurion-analytics`）

**组内串行原因**：R1（读面）与 R2（契约化）改的是同一批 DTO 的消费侧——R1 换持久读面会改 `internal/upstream/upstream.go` 的调用与错误语义，R2 把本地 DTO 镜像迁走会改 `internal/analysis/model.go` + `upstream_contract.go`，两者都要跑同一批 golden JSON 断言（`TestUpstreamContractWireShape`）⇒ 必须有序，否则断言互相拆台。
**可立刻做的**：R1 读面——core `B6-3` 已于 `8537ffd` 落地（`backtest_jobs.result_status` 三值持久读面 + `invalid` 行 report 返 200 不再 404）⇒ **前置已解除**。
**在等外部产物的**：R2 需要 contracts 发布 `analysis` 包——实测 contracts 仓当前只有 `dataprotocol` / `domain` / `engine` / `fees` 四个业务包，**`analysis` 包不存在**。

### G6 · data（`tellurion-data`）

**组内串行原因（数据依赖）**：物理 `SET SCHEMA` 搬迁必须先停掉 core 侧的第二写者，否则搬一次两边各写一次 ⇒ 写面装配 → 物理搬迁有序。
**可立刻做的**：无独立可开工项——两条未落项都指向 core（**跨仓腿，台账明确写「物理 `SET SCHEMA` 搬迁未做」且「core 侧同步腿仍在写 `market`/`ingest`」**）。
**在等外部产物的**：等 core 停第二写者。

### G7 · broker（`tellurion-broker`）

X6c 需要**真实 XTP SDK 与柜台环境**，仓内无 SDK 无法验证 ⇒ 本组不具备开工条件，只出决策材料。

### G8 · agent（`tellurion-agent`）

QT-32 的 AI 侧接线。台账 QT-32 ③-⑤ 原文：「生成质量与实验编排属 agent 仓（其台账另立单）」，且 QT-32 ④「不把 AI 编排逻辑搬进 core（ADR-023）」⇒ 主体在 agent。但 **agent 仓台账里没有 QT-32 的六字段条目**（实测 `grep QT-32 tellurion-agent/docs/TASKS.md` 只有一处「QT-32 继续等待」的旧记录）⇒ 须先由协调者在 agent 仓立单。

### G9 · ui（`tellurion-ui`）

AUD-61 阶段二 ①（16 条结构漂移）。台账原文：「等 `feat/design-tokens-v2-palette-sweep` 合并」——实测该分支**只在旧仓 `quant-trading-ui`**（本地 + origin 都有），`tellurion-ui` 本地与 origin 均无 ⇒ 前置未解除。

### G10 · docs（`tellurion-docs`）

DOC-06 PRODUCT/VISION 重写，依赖产品口径三问 ⇒ 待裁决，只出决策材料。

---

## 第 3 节 · 派发原文

### 派发块 G1（core）

> **派发原文**：在 `tellurion` 仓按顺序完成 `QT-73` → `QT-71` → `QT-72`。
>
> 1. **`QT-73` · core 横切包切 kit 一期**：
>    - 在 `tellurion/go.mod` 增加 `github.com/ruoxizhnya/tellurion-kit v0.1.1`（**实测当前零命中**，不要写成已有）；跑 `go mod tidy`。
>    - 全仓改 import 路径，**逐处保留既有别名**（`apperrors "tellurion/pkg/errors"` → `apperrors "github.com/ruoxizhnya/tellurion-kit/errors"`），**不许逐处改别名名**。
>    - **只改这 5 个包的 import**：`errors` / `decimal` / `clock` / `logging` / `httpclient`。**不许动** `statistics`（core 20 处）、`httpserver`（core 34 处）、`observability`（6 处）、`auth`（23 处）、`authz`（8 处）。
>    - 删除 `pkg/errors/`、`pkg/decimal/`、`pkg/clock/`、`pkg/logging/`、`pkg/httpclient/` **五个目录，不留转发壳**。
>    - **三处已证伪的「字节相同」前提，必须按实际处理**（见第 6 节 §6.4）：
>      - `clock`：`core/pkg/clock/interfaces.go` 的常量名是 `ModeBacktest`，kit 是 `ModeDeterministic`（实测 core 内 `clock.ModeBacktest` 有 **8 处**引用）⇒ 切包时**必须一并把调用点改成 `ModeDeterministic`**，否则编译红。这是**改名**，不是纯 import 替换。
>      - `logging`：`core/pkg/logging/logger.go` 仍有 `zerolog.SetGlobalLevel(zerolog.DebugLevel)`（第 35 行，kit 已修，见 kit `logging/logger.go:22`）⇒ 切到 kit 后**日志级别行为会变**（从「恒 Debug」变「按配置」）。**这是语义变更，不在本单许可范围** ⇒ **只切包、不修 core 侧注释里的旧口径**；把行为差异如实写进 `docs/TASKS.md` QT-73 的施工记录，**报给协调者**，由协调者决定是否另立单同步 `pkg/logging` 的旧行为。
>      - `httpclient`：core 的 `client.go` **没有** kit 的 `newStatusError`（实测 kit `httpclient/client.go:167` 有、core 无）⇒ 切到 kit 后 **4xx 从「静默当成功」变成返回 `*apperrors.AppError`**（这正是 KT-04 修的那条）。**这是 breaking 语义变更** ⇒ 同上，**只切包、如实登记、报协调者**，不许在本单里改任何调用点的 `err != nil` 判断。
>    - **关键约束**：`pkg/backtest/engine.go` / `engine_daily.go` / `admission.go` 用 `apperrors`；`pkg/storage/{index_daily,macro,market_activity,supply_chain}.go` 用 `apperrors`；`pkg/storage/{postgres,redis}.go` 用 `logging`；`cmd/analysis/kernel_wiring.go` 用 `clock`；`cmd/data/*.go` 共 11 个文件命中这 5 包 ⇒ **这批文件是本单的主战场**。
>    - **验收**：`grep -rn '"tellurion/pkg/\(errors\|decimal\|clock\|logging\|httpclient\)"'` 零命中（Windows 下用 `Select-String`）；五个目录不存在；`go build ./...` / `go vet ./...` / `go test ./... -count=1` 全绿；`python tools/check_contract_pin.py` 退出 0（**必须是 10 个 pin**——实测现在是 9 个，加了 kit require 后 core 才有第一个 kit pin）。
>    - **破坏验证**：把 `pkg/backtest/engine.go` 里某处 `apperrors.Wrap` 改成 `fmt.Errorf` ⇒ 对应错误分级测试必须红；逐字节还原后绿（判据：文件 sha256 与改前相同）。
>    - **跨仓腿**：kit 的 `v0.1.1` tag **已存在且远端可拉**（实测 `git ls-remote --tags` ⇒ `refs/tags/v0.1.1`），不需等任何人。若 `go mod tidy` 拉不到，报协调者（AGENTS 记明「私有仓拉取前提」需 `GOPRIVATE` + 凭证，本仓现状全 public）。
>
> 2. **`QT-71` · 服务间凭证 `service` 角色**（**前置：G2 的 KT-09 已推 `v0.1.2`；没推到就停下回报，不许自己实现签发**）：
>    - `pkg/auth/auth.go`：新增 `RoleService Role = "service"`；**必须加进 `IsValid()` 的返回集合**（实测当前集合是 `{viewer, trader, admin}` 三值，不加则签出的 token 在解析期即被判非法）；`CanTrade()` 恒 false、`CanAdmin()` 恒 false。
>    - `pkg/authz/policy.go`：`Policy` 增第五档 `Service []string`；`Middleware` 增一分档；`Entries()` 出 `"service"`。
>    - `cmd/analysis/route_policy.go`：**只在 `POST /api/backtest` 登记 `service`**（实测该表当前是 Public / Admin / Trader / ReadOnlyPOST 四档）。**禁止**把 `service` 写进 `Trader` 或 `Admin` 列表。
>    - 部署清单：把 `JWT_SECRET` 与三个服务 token env（`RISK_SERVICE_RESOLVE_TOKEN` / `DATA_SERVICE_TOKEN` / `ANALYTICS_CORE_TOKEN`）打到点位；token 只经 env 注入、不落库、不进镜像。
>    - **验收**：service token 调 `POST /api/risk/resolve`、`GET /datasets/*`、`POST /api/backtest` 均 200；同一 token 调 `POST /api/execution/orders` **403**；无 token 401；viewer token 调 resolve 仍 403；`RoleService.CanTrade()==false` 守卫测试；`route_policy.go` 的 service 登记行数 == 1。
>    - **破坏验证**：把 `service` 加进 `Trader` 列表 ⇒ 「service token 调下单端点 403」这条用例必须红。
>    - **跨仓腿**：risk 的 `POST /api/risk/resolve` / `POST /api/risk/events` 两条 Roles 加 `"service"`（**`GET /api/risk/audit` 不加**）——**在 `tellurion-risk` 仓改，报协调者**；data 的 `GET /datasets/resolve` / `GET /datasets/{hash}` Roles 加 `"service"`——**在 `tellurion-data` 仓改，报协调者**。
>
> 3. **`QT-72` · X0 三 hash 对外读面落 core**（**前置：QT-71 已把 `route_policy.go` 档位表改完**）：
>    - 新增 `GET /api/backtest/{id}/provenance`，读 `quant.run_provenance ⋈ backtest_jobs`。
>    - `result_status` **只从 `backtest_jobs` 取**（实测该列在 `pkg/storage/backtest_jobs.go:40-53` 的 `UpdateJobCompleted` / 失败路径已在写，**不要往 provenance 表加列**）。
>    - 三态逐项对齐 orchestration 的 `provenanceView`：非终态 409 `provenance_not_ready`；终态 `failed`/`cancelled` 无行 404 `provenance_not_found`；`completed`/`invalid` 无行 500 `provenance_missing`（**`invalid` 不是 `failed`**）。
>    - 可空列显式出 `null`，不省略键。
>    - 在 `cmd/analysis/route_policy.go` 登记该 GET 端点（**QT-71 已改过同一文件，接手前先 `git log -1 -- cmd/analysis/route_policy.go` 确认基线**）。
>    - `docs/openapi.yaml` 登记端点与三态。
>    - **验收**：真 run 返回三 hash + `result_status`；四个三态用例；可空列 `null`；DTO 字段名与 orchestration `provenanceView` **逐字**对齐（orchestration 侧在 `internal/httpapi/provenance.go` + `internal/httpapi/parents.go`）。
>    - **破坏验证**：把 `result_status` 的来源从 `backtest_jobs` 换成 provenance 的任意 hash 列 ⇒ 结论轴用例红；还原后绿。
>    - **跨仓腿**：不动 orchestration 代码。
>
> **验收（组级）**：`gofmt -l .` 空；`go build ./...` / `go vet ./...` / `go test ./... -count=1` 全绿；`python tools/check_contract_pin.py --self-test` **18 条腿** GREEN + 实扫 GREEN；`python tools/check_repo_autonomy.py` 全绿。
>
> **禁区**：不许写 `replace`；不许提交 `go.work` / `go.work.sum`；不许顺手改 `statistics` / `httpserver` / `observability` / `auth` / `authz`；不许在 QT-73 里修 logging/httpclient 的语义差异（只登记、报协调者）；不许在 QT-71 里自造 token 签发（等 kit `v0.1.2`）；不许改 `docs/openapi.yaml` 之外的跨仓 wire 真相源。
>
> **跨仓腿汇总**：① kit `v0.1.2`（等 G2）→ ② risk 策略表加 `service`（改 risk 仓）→ ③ data 策略表加 `service`（改 data 仓）→ ④ analytics 出站 token（`-core-token` / `-data-token` 已落，不在本组）→ ⑤ logging/httpclient 语义差异登记给协调者。

---

### 派发块 G2（kit）

> **派发原文**：在 `tellurion-kit` 仓按顺序完成 `KT-09` → `KT-10`。
>
> 1. **`KT-09` · `IssueServiceToken`**：
>    - 在 `tellurion-kit/auth/` 新增签发入口（`*TokenService` 方法或包级函数，取最小侵入）：签名 `IssueServiceToken(subject, role string, ttl time.Duration) (string, error)`。
>    - **只签 access token**（`kind=access`），**不产生 refresh token**。
>    - 拒绝空 `subject` / 空 `role` / `ttl <= 0`（三条各一条用例）。
>    - 角色按调用方传入的**字符串**写入 claims，**不在 kit 新增角色枚举**。
>    - 文档写明轮换与分发责任在使用方（core QT-71）。
>    - **验收**：`go test ./auth/... -count=1` 绿；签出的 token 经 `Parse` 得到 `kind=access` + 传入 role + 传入 subject；TTL 到期后 `Parse` 报过期；空参数报错；`go run ./tools/check_scope` 退出 0（无业务词）；`go run ./tools/check_release_matrix` 退出 0。
>    - **打 tag**：`git tag -a v0.1.2 -m "v0.1.2: feat ..."` 并 push（annotated tag，必须在 `main` 上）。
>    - **破坏验证**：把 `kind` 写成 `"refresh"` ⇒ `kind==access` 那条断言必须红。
>    - **跨仓腿**：分发、部署清单、服务侧角色登记**不在本仓**（归 core QT-71）。
>
> 2. **`KT-10` · 二期切包前置**：
>    - 搬 `LogReturns` / `AnnualizeVolatility` 连同单测到 `tellurion-kit/statistics/`；**导出签名与 core 逐字一致**（core 在 `pkg/statistics/`，实测 core 侧 `statistics` import 点 20 处）。
>    - 评估 `LoopbackOnly`（gin 中间件）与 `IsLoopbackRemoteAddr`：**若 kit 的依赖白名单不允许 gin，则只搬 `IsLoopbackRemoteAddr`**，core 保留 gin 包装，并在 core 二期台账写明。
>    - **单独打 tag**（`v0.1.3`），**不与 KT-09 混发**。
>    - **验收**：kit 版与 core 版同函数在表驱动 + 随机向量上输出**逐位一致**；`go run ./tools/check_scope` 退出 0。
>
> **验收（组级）**：`gofmt -l .` 空；`go build ./...` / `go vet ./...` / `go test ./... -count=1` 全绿；`go run ./tools/check_release_matrix` 退出 0。
>
> **禁区**：不许改 `IssueTokens` 现有语义；不许做吊销列表 / 服务注册表；不许把 token 落盘；不许动一期五包；不许把 KT-09 与 KT-10 混发 tag；**不许把 `clock` 的 `ModeDeterministic` 改名回 `ModeBacktest`**（core 的 8 处调用点靠这个名字对齐）。
>
> **跨仓腿**：core QT-71 等本组 `v0.1.2`；core 二期切包等本组 `v0.1.3`。

---

### 派发块 G3（orchestration）

> **派发原文**：在 `tellurion-orchestration` 仓按顺序完成 `QT-44` 的两条收尾腿。
>
> 1. **子 run 入队（`parent_job_id` 的写入方）**：
>    - 落点：`internal/queue`（`backtest_jobs.parent_job_id` 的写入）+ `internal/pgstore`（对应 INSERT 列）+ `internal/parents`（提交父作业时派生子 run 并入队）。
>    - **实测现状**：`GET /api/runs/parents/{id}` 的 `children[]` **今天通常返回空数组**（台账施工 ⑧ 未落项原文：「子 run 入队（`parent_job_id` 的写入方）与 worker 执行；`GET` 的 `children` 因此通常为空」）。本腿要把这个空数组填上。
>    - **约束**：`plan_canonical` / `plan_hash` 身份口径**不动**（台账施工 ⑦：「父记录的身份是 `plan_hash`，不是幂等键」）；**不许塞 `request_json`**（JSONB 规范化数字文本会让同一次提交算出两个身份）。
>    - 幂等：同 `plan_hash` ⇒ 同一父 id；同键换计划 ⇒ 409 `idempotency_conflict`。
>    - **验收**：提交 batch 父作业后 `GET /api/runs/parents/{id}` 的 `children[]` **非空**且逐子带 `status`（队列生命周期）与 `result_status`（K9）两轴；子 run 的 `parent_job_id` 落库正确；同键重放 200 `idempotent_replay`。
>    - **破坏验证**：把 `parent_job_id` 的写入短路（`&& false`）⇒ 「children 非空」用例红（台账施工 ⑧ 已用同型腿证明该用例有牙）。
>
> 2. **worker 执行**：
>    - 依赖：① 的派活契约。**注意台账施工 ④ 的诚实登记**：「`ClaimByKernel` **没有生产消费方**——本仓没有 worker 进程（引擎在 core，红线禁止 import）」。
>    - 装配期必须做**能力断言**：不提供 `ClaimByKernel` 的注册表**不许带着「以为会回收」启动**（台账施工 ④ 取舍 6 的代价登记）。
>    - `kernels` 空数组必须归一成 NULL（两条用例已钉）。
>    - **验收**：worker 注册 → 心跳 → `Claim` 领到子 run → 写回状态 → 父汇总 `ApplyBatchSummary` 三值计数正确；`ReapOffline` 把超时 worker 判 offline；draining worker 不领新活。
>
> **验收（组级）**：`gofmt -l .` 空；`go build ./...` / `go vet ./...` / `go test ./... -count=1` 全绿；`ORCHESTRATION_TEST_DSN='postgres://postgres:postgres@127.0.0.1:5432/tellurion_orchestration_test?sslmode=disable' go test ./internal/pgstore -count=1` 绿（无 DSN 时 PG 腿 skip，**skip 不算通过，须如实登记**）。
>
> **禁区**：**本仓零 import core**（QT-44 ⑤-① 是本单验收项，**不许 import `tellurion/pkg/backtest`**）；不改 `EngineRunner` 契约；不做 K8s；不搬 `cmd/dualrun`；**不许改 `plan_canonical` / `plan_hash` 身份口径**；鉴权仍按裁-15 缓做（不新增 JWT 面）。
>
> **跨仓腿**：① 的 `TradingCalendar` 只读 port 已在 core 落（`QT-67` ✅ `2a4b076`），**本仓只消费、不改 core**；`run_artifacts` 的**生产者**仍是 core 引擎（**本仓零 import core 意味着本仓不能代替它**），**报协调者**。

---

### 派发块 G4（risk）

> **派发原文**：在 `tellurion-risk` 仓完成 `QT-42` 收尾第 ① 腿（core 侧消费接线）。
>
> 1. **risk 仓侧（可做，零外部前置）**：在 `internal/httpapi/policy.go` 的 `riskPolicy` 表里，给 `POST /api/risk/resolve` 与 `POST /api/risk/events` 的 `Roles` 加 `"service"`；**`GET /api/risk/audit` 不加**。
>    - **约束**：角色是 any-of、kit 不做角色排序 ⇒ **每档都要把更高角色显式列进来**，所以 `"service"` 必须与 `"trader"` / `"admin` 并列写，不能替换。
>    - **验收**：`TestRouteTableEqualsPolicyTable` 绿（route 13 ≡ policy 13，改完条数仍相等）；新增用例「service token 调 resolve 200 / 调 audit 403」。
>    - **破坏验证**：从 `riskPolicy` 删掉 `POST /api/risk/events` 条目 ⇒ 守卫红（route 13 vs policy 12）。
> 2. **core 侧消费接线（主体在 core，**报协调者、不在本仓改**）**：core 拿 `bundle` → `ParseBundle` → 构建进程内 `RiskPolicy`。
>    - **实测现状**：core 全仓 `grep ParseBundle` = **0 命中**；core 的限额仍来自 `config/analysis-service.yaml` 的 `risk_manager.*`。
>    - **本仓只提供**：`Snapshot` / `SnapshotRule` / `Bundle` 三类型（`internal/rule/snapshot.go:24/37/121`）+ `ParseBundle`（bytes → Bundle 纯函数，双头 derive 校验，不符即 `ErrSnapshotMismatch` fail-closed）。
>    - **边界**：台账 QT-42 ④ 明写「`ParseBundle` 的 Go 类型出现第二个使用方（core）后，`Snapshot` / `SnapshotRule` 按 ADR-0001 ① 迁 contracts」⇒ **迁 contracts 是下一张单，不是本单**。
>
> **验收（组级）**：`gofmt -l .` 空；`go build ./...` / `go vet ./...` / `go test ./... -count=1` 全绿；`go run ./tools/check_release_matrix` 退出 0。
>
> **禁区**：不许动 `riskPolicy` 之外的端点档位（`/audit` 不加 `service`）；不许把 `service` 加进 `approve` / `rules` 管理面；不许在本仓实现 core 的 `RiskPolicy` 装配（跨仓腿报协调者）；不许动 `rule_deprecated` 的不可达状态（台账登记为未落项 7，不在本单）。
>
> **跨仓腿**：core 侧 `ParseBundle` 消费（**core 仓，主体在 core QT-71 之后**）；`Snapshot`/`SnapshotRule` 迁 contracts（下一张单）。

---

### 派发块 G5（analytics）

> **派发原文**：在 `tellurion-analytics` 仓按顺序完成 `QT-43` 收尾的 R1 → R2 两腿。
>
> 1. **R1 · 换 K9-correct 持久读面**：
>    - 落点：`internal/upstream/upstream.go` 的 `ResultReader` 适配器。
>    - **core 侧前置已解除**：core `B6-3`（commit `8537ffd`）已落 `backtest_jobs.result_status` 三值持久读面，`invalid` 行 report **返 200 不再 404**。本腿把适配器从「同步结果」切到「持久读面」，**端口方法集与 DTO 不变**。
>    - **约束**：**缺 `status` 一律 409 `run_result_incomplete`**，**绝不解析错误串**（台账施工 ② 的原话；`upstream.go:69-71` 注释已写明）。
>    - **验收**：异步 `invalid` run 的结果能被读出且三值正确；缺 `status` 仍 409。
>    - **破坏验证**：把 `result_status` 的三值映射改错一档 ⇒ 对应映射用例红。
>
> 2. **R2 · artifact 契约化**：
>    - **前置**：contracts 发布 `analysis` 包（**实测 contracts 仓当前只有 `dataprotocol` / `domain` / `engine` / `fees`，`analysis` 包不存在**）。
>    - **没发布就停下回报**（台账原话：「R2 的 owner / 形状未裁，DTO 仍是本地临时镜像，冻结后迁 contracts」；**owner 未定 ⇒ 先要裁决，见第 5 节 §5.3**）。
>    - 本腿内容：本地 DTO 镜像迁走，**端口方法集与校验语义不变**；`internal/analysis/upstream_contract.go` 的 `UpstreamContractVersion = "v1"` 与三个校验器语义不动。
>    - **验收**：`TestUpstreamContractWireShape` golden JSON 往返仍绿；`TestUpstreamContractArtifactFailClosed` 逐项钉必填。
>
> **验收（组级）**：`gofmt -l .` 空；`go build ./...` / `go vet ./...` / `go test ./... -count=1` 全绿；`ANALYTICS_TEST_DSN='postgres://postgres:postgres@127.0.0.1:5432/tellurion_analytics_test?sslmode=disable' go test ./... -count=1` 绿（无 DSN 时 PG 腿 skip，**skip 不算通过**）。
>
> **禁区**：不许做 PDF 导出（R5 已裁为**非目标**）；不许把 `AuthStatus` 改回 `"unwired"`（当前是 `"enforced"`，W3 已翻转，`TestAuthEnforcedCurrentState` 是**可执行锚点**、改写而非删除）；不许动 `internal/analysis/sensitivity.go` 的 fail-closed 语义（父 `total` ≠ 本地展开点数 ⇒ 报告 `failed`、不写 points）；不许在本单里造 `analysis` 契约包（那是 contracts 仓的事）。
>
> **跨仓腿**：contracts `analysis` 包（**前置未到，停下回报**）；R1 的 core 端点 URL/路径由 core 侧定（core `B6-3` 已落，**只换 URL/路径，端口与 DTO 不变**）。

---

### 派发块 G6（data）

> **派发原文**：在 `tellurion-data` 仓完成 `QT-40` 收尾两腿。
>
> 1. **写面装配接线**：把 `internal/ingest.ETLPipeline` + `internal/store.BulkInsert` 挂到本仓的同步作业。
>    - **前置（跨仓）**：core 侧同步作业层（`sync_jobs` → worker → ETL）仍在 core，`pkg/data/source/etl.go` **仍在且仍用 `ON CONFLICT` upsert**（实测 `pkg/data/source/etl.go` 存在，155 行，第 68 行注释写着 upsert）。**本腿的前置是 core 先停第二写者** ⇒ 未解除前**只出接线方案，不落代码**。
>    - **约束**：`BulkInsert` **只开 A 集**（B/C 集拒写）；事务前 `visible_at` 判零整批拒收；同主键同内容 no-op、异内容 `ErrContentConflict` 整批回滚。
>    - **验收**：写入路径的表只出现 A 集；缺 `visible_at` 的批次**整批不落**（一行都不写）。
>    - **破坏验证**：把 `BulkInsert` 的 `visible_at` 判零短路（`if false && p.VisibleAt.IsZero()`）⇒ `TestWriter_RejectsWholeBatchWithoutVisibleAt` FAIL（台账已用同型腿证明有牙）。
>
> 2. **物理 `SET SCHEMA` 搬迁**：把 `market` / `ingest` 的表物理迁到目标 schema。
>    - **前置**：① 完成 + core 第二写者停写。
>    - **约束**：**禁 `SET visible_at = trade_date`**（已有测试钉住）；**禁在 `Statements()` 里静默 `ALTER TABLE ... SET SCHEMA`**（实测 `internal/schema/schema.go:12` 明写这条禁令）。
>    - **验收**：迁移前后 `TableNames()` 可对账；A/B/C 三集合分类不变。
>
> **验收（组级）**：`gofmt -l .` 空；`go build ./...` / `go vet ./...` / `go test ./... -count=1` 全绿；`DATA_TEST_DSN='postgres://postgres:postgres@127.0.0.1:5432/tellurion_data_test?sslmode=disable' go test -tags backfill ./internal/schema -count=1` 绿（**生产 DSN 与非 `_test` 库名自动 skip**）。
>
> **禁区**：**不许重新引入 `/factors/{name}@{hash}` 路由**（裁-14 已撤，守卫 `TestFactorRouteIsRemoved` 钉住，**常驻 404 比没有路由更坏**）；不许 `SET visible_at = trade_date`；不许在本仓实现传输（Tushare / 东财 / Mootdx 的 HTTP/SDK 客户端**按 `Transports` 注入，传输实现仍在 core**）；不许动 `internal/ingest/point.go` 的别名（已收编 contracts `v0.6.0`，`type UnifiedDataPoint = dataprotocol.UnifiedDataPoint`）；不许把 `DataType*` 改回本地字面量（守卫 `TestDataTypeConstantsMatchContract`）。
>
> **跨仓腿**：core 停第二写者（**core 仓，跨仓**）；传输实现留在 core（**本仓不改 core**）；`visible_at` **生产库回填未做**（只做过测试库）⇒ 生产回填属运维执行，**需授权**，见第 5 节。

---

### 派发块 G8（agent）

> **派发原文**：**先不要写代码** —— 在 `tellurion-agent` 仓为 `QT-32` 立六字段单（agent 侧），再按单施工。
>
> 1. **立单**：在 `tellurion-agent/docs/TASKS.md` 写 QT-32 的 agent 侧正式六字段条目（目标 / 上下文 / 要求 / 约束与非目标 / 验收 / 边界）。台账 QT-32 ③-⑥ 原文：「生成质量与实验编排属 agent 仓（其台账另立单）」。
>    - **必须显式引用的冻结契约**：QT-32 ③-① 的编译管线在 `tellurion-strategy:cmd/compiler`（QT-41b ✅ 已落，QT-41c ✅ 跨进程闭环已落）；promote 闸门在 `tellurion-strategy:internal/httpapi/api.go:46`（`POST /api/strategies/{id}/versions/{hash}/promote` **实测已存在**）。
>    - **六字段不许写「参考相关实现」**（AGENTS §8.5）。
> 2. **AI 侧护栏沿用本仓既有 AST/白名单**（QT-32 ③-⑤ 原文：「不新造第二套」）。
> 3. **验收**：未过 promote 的 AI 产物被回测路径拒绝（fail-closed 用例）；三路一致性（Batch / Step 从零 / Step 从断点）+ dualrun K15 对拍通过；破坏验证：绕过闸门直接登记 ⇒ 有测试红。
>
> **验收（组级）**：`gofmt -l .` 空；`go build ./...` / `go vet ./...` / `go test ./... -count=1` 全绿；`go test ./cmd/ai/... -race -count=1`（**并发面必带 `-race`**）。
>
> **禁区**：不许把 AI 编排逻辑搬进 core（ADR-023）；**不许省略人工闸门做「自动 promote」**（档 3 是远期，不在本单）；不为 AI 产物放宽 `ValidateImports` / 预算 / fail-closed 任何一条；不许改 `pkg/ai/validator` / `pkg/ai/validation` 的既有白名单。
>
> **跨仓腿**：编译管线在 `tellurion-strategy` 仓（**只调用，不改**）；promote 端点在 `tellurion-strategy` 仓（**只调用，不改**）；`strategy_ref` 的解析在 core（**跨仓腿，报协调者**）。

---

## 第 4 节 · 阶段划分（消除歧义）

> **「组间并行」指的是「开工时刻可并行」，不是「组内每一步都不用等别人」。**
> 组内每一步仍可能等另一组的产物；本节把「等谁」写死。

### 阶段 A —— 现在就能全部并行开工，组间零等待

| 组 | 开工时刻可做的第一步 | 为什么现在就能开 |
|---|---|---|
| **G1** | `QT-73`（core 切 kit 一期五包） | core `go.mod` 零 kit require；kit `v0.1.1` tag **远端已存在**（实测 `git ls-remote --tags`）⇒ 零等待 |
| **G2** | `KT-09`（`IssueServiceToken` → `v0.1.2`） | 纯新增函数，无跨仓前置 |
| **G3** | 子 run 入队（`parent_job_id` 写入方） | `TradingCalendar` 只读 port 已由 core `QT-67` ✅ 落（`2a4b076`）⇒ 零等待 |
| **G5** | R1 读面换 K9-correct 持久读面 | core `B6-3` ✅ 已落（`8537ffd`）⇒ 零等待 |
| **G6** | 写面装配的**接线方案**（不落代码） | 方案设计零前置，落代码的前置（core 停第二写者）不在阶段 A |
| **G8** | QT-32 的 agent 侧**立单**（不写代码） | 立单零前置；施工零前置（三条依赖已全绿） |

**阶段 A 的并行硬约束**：G1 与 G6 同属 core 域但**文件域零重叠**（G1 动 `pkg/{errors,decimal,clock,logging,httpclient}` + 90 处 import；G6 动 `tellurion-data` 仓的 `internal/ingest` / `internal/store` / `internal/schema`）⇒ **可并行**，但 G6 的落代码动作被 core 侧卡住（见阶段 B）。
**G3 与 G5 都读 core 的 HTTP 端点**（`GET /api/runs/{id}` / `GET /api/backtest/{id}/report`）⇒ **只读消费、不改 core**，可并行。

### 阶段 B —— 等某个组的某个产物之后才解锁

| 被解锁的步骤 | 等谁 | 等的具体产物（可机械核对） |
|---|---|---|
| **G1 · `QT-71`** | **G2 · `KT-09`** | kit 的 **`v0.1.2`** tag（`git ls-remote --tags origin v0.1.2` 有输出）+ `auth.IssueServiceToken` 存在。**没有它 core 无权自造签发** |
| **G1 · `QT-72`** | **G1 · `QT-71`** | `cmd/analysis/route_policy.go` 的档位表已被 QT-71 改完（同一文件，接手前先 `git log -1 -- cmd/analysis/route_policy.go`） |
| **G4 · core 侧 `ParseBundle` 消费** | **G1 · `QT-71`** | `service` token 能签发 + risk 的 `resolve`/`events` 策略表加了 `"service"` ⇒ 否则 core 调 resolve 401 ⇒ 按裁-11 该 run 直接 `failed` |
| **G5 · R2 契约化** | **contracts 发版** | contracts 仓出现 **`analysis` 包**（实测今天没有；只有 `dataprotocol`/`domain`/`engine`/`fees`）+ 一个 tag |
| **G6 · 写面装配落代码** | **core 停第二写者**（跨仓） | core `pkg/data/source/etl.go` 的旧写路径删掉 / 停写；台账原文：「core 侧同步腿仍在写 `market` / `ingest`，且仍用 `DO UPDATE`」 |
| **G6 · 物理 `SET SCHEMA`** | **G6 · 写面装配** | 写面已接到本仓作业层 + core 不再写这两张 schema |
| **G1 · core 二期切包** | **G2 · `KT-10`** | kit **`v0.1.3`**（`LogReturns` / `AnnualizeVolatility` / `IsLoopbackRemoteAddr`） |
| **G8 · QT-32 施工** | **协调者裁归属** | QT-32 单本身写明：「若产品侧改判『G6/M2 不在本代范围』，则删除本单并在执行顺序总览显式声明划出，**不得沉默**」 |

---

## 第 5 节 · 待你裁决清单

| 事项 | 卡住哪个单 | 需要你给什么 |
|---|---|---|
| **① `service` token 的分发面**（QT-71 前置） | **QT-71**（core）、**G4**（risk 的 core 侧消费） | 台账 QT-71 ③-⑦ 原文：「部署清单把 `JWT_SECRET` 与各服务 token env（`RISK_SERVICE_RESOLVE_TOKEN` / `DATA_SERVICE_TOKEN` / `ANALYTICS_CORE_TOKEN`）打到点位」。**实测今天没有任何清单给 risk / data / analytics 配 `JWT_SECRET`**（core compose 只给 analysis / ai；k8s 只有 analysis / data / web / pg / redis 五份）⇒ **需你拍：这三份部署清单由谁写、写进哪个仓** |
| **② logging / httpclient 切包后的语义变更**（QT-73 隐含） | **QT-73**（core）、**G2**（kit 二期） | 实测：kit `logging` 已修「`Init` 的 level 被无条件覆盖为 Debug」（KT-07），core `pkg/logging/logger.go:35` **仍是** `SetGlobalLevel(zerolog.DebugLevel)`；kit `httpclient` 已修「4xx 静默当成功」（KT-04），core 副本**仍无** `newStatusError`。⇒ 切包后 core 行为会变。**需你拍：接受这两条行为变更（并在台账改口径），还是先在 core 侧对齐旧行为再切**。QT-73 ④ 原文「不许顺手改语义」与这个事实**冲突**，必须裁 |
| **③ `analysis` 契约包的 owner 与形状** | **G5 · R2**（analytics）、**contracts 仓** | 台账原文：「R2 artifact 的 owner 与最终形状：owner 未定、未迁 `tellurion-contracts`」。**需你拍：owner 是 analytics 还是 contracts；DTO 落 contracts 的哪个包**（现候选：`analysis` 新包，还是并入既有 `engine`） |
| **④ QT-32 是否在本代范围** | **G8**（agent）、**QT-32**（core） | QT-32 ② 原文：「若产品侧改判『G6/M2 不在本代范围』，则删除本单并在执行顺序总览显式声明划出，**不得沉默**」。**需你答：本代做 / 不做**。另需裁「AI 生成侧归 agent 还是 core」——QT-32 ④ 已定「不搬进 core」，但 agent 台账里**没有** QT-32 的六字段条目 |
| **⑤ `ingest.raw` 残留行处置** | **DBT-15**（core，本仓只留指针） | ⚠️ **实测已发生事实**：`tellurion-g3-data` 工作树（core 的一个 **git worktree**，分支 `task/g3-data`）里 DBT-15 已标 **✅ 2026-10-23「授权后删除」**，并留了取证：两条 DELETE 实跑、`total 12,134 → 11,968`、`probe=0`、`unit-test=0`。**但该分支未合入 main**（`git merge-base --is-ancestor task/g3-data main` 退出 1）。⇒ **需你裁：这些已执行的删除要不要追认进 main 的台账；要不要把该分支的 DBT-15/16/17 落账 cherry-pick 回 main**（详见第 6 节 §6.1） |
| **⑥ `fundamentals_detail` 的产出方** | **DBT-16**（core） | 同上分支已标 ✅「裁决 = 成文『不可得』」，写进了 `docs/SPEC.md`。**需你拍：接受「不可得」成文这个结论**，还是仍要求 EquityDeep 侧出产出协议 |
| **⑦ `capital_flow` / `top_list` 归属** | **DBT-17**（core） | 同上分支已标 ✅「裁决 = 两表登记『仅储备』」。**需你拍：接受「仅储备」**（则 cron 名单与 `scheduler.RegisterJobType` 的状态须一致），还是接读方 |
| **⑧ `mining.Proposer` 归属** | **DBT-18**（core → agent 跨仓） | DBT-18 ③-① 原文：「**由主任裁定归属**」。台账 ② 已给出结论：「本单**主体不在 core**」，③ 说「若归 agent 仓，本仓只保留 `mining.Proposer` 接口作为冻结契约 + 在跨仓任务索引加一条指针」。**需你答：归 agent（则 core 只留指针）还是要 core 提供候选投递 API（则要写端点与鉴权）** |
| **⑨ `visible_at` 生产库回填授权** | **G6**（data）、`quant.market` / `quant.ingest` 全部 12 张 A 集表 | 台账原文：「**生产库回填未做**……生产回填仍须显式启动开关或运维执行」。回填是**写生产库**的操作。**需你给：是否授权 + 由谁执行**（本方案**不安排**任何 agent 去写生产库） |
| **⑩ `docs/openapi.yaml` 的写入权** | **QT-72**（core）、DATA-14 同族 | 台账 DATA-14 ⑦ 原文：「未改 `docs/openapi.yaml`（已声明数组，**改它会撞 G2 唯一写者**）」。QT-72 ③-⑤ 要求登记新端点。**需你确认：QT-72 可以独占写 `docs/openapi.yaml` 一小段时间**（本方案已把它排成阶段 B，只在 QT-71 之后开工，撞车面最小） |
| **⑪ U6 / U8 两条未裁边界** | X 线六仓容器升格（**不卡本轮任何单**） | `repo-inventory.md` §3.1：**U6**（reduction-service 仓位）、**U8**（`factor_hypothesis` owner）仍开放；**U7 已于 2026-10-24 裁为「拆分」**（台账已更新）。**需你拍 U6 / U8**（本轮不阻塞，但会影响 G6 的 schema 归属清单） |
| **⑫ XTP 柜台环境** | **G7**（broker X6c） | 台账原文：「需要真实 XTP SDK 与柜台环境，仍从零未做」。**需你给：是否有 SDK 与测试柜台**。没有 ⇒ G7 只能出决策材料，不出施工步骤 |
| **⑬ `producer_pending` 字段名** | **QT-72**（core）、**G3**（orchestration） | core QT-72 ② 写「orchestration 端点保持 `producer_pending`」，**实测 orchestration 仓 0 命中**。**需你给：该状态的准确字段名/位置**（或允许 QT-72 自定并同步两侧） |
| **⑭ promote 闸门的审批人身份** | **G8 / QT-32**（agent）、`tellurion-strategy` | QT-32 ③-② 要「审批人/时间/结论留痕」，实测 strategy 侧只有 `compile_log` 追加 `"promoted"`，**无审批人字段、无独立审计表**。**需你给：审批人身份从哪来**（JWT claim？core 转发？service token？）—— 决定闸门落在 strategy 还是 agent |
| **⑮ QT-73 的「零语义五包」与实测冲突** | **G1 · QT-73**、**G2** | 实测：`errors` + `decimal` **字节相同** ✅；但 `clock` 是**改名**（`ModeBacktest`→`ModeDeterministic`，core 内 8 处引用）、`logging` 与 `httpclient` 是**行为差异**（core 侧仍是 kit 修过的旧缺陷）。QT-73 ④「不许顺手改语义」与「只切包」**互相矛盾**。**需你拍：接受这两条行为变更（并改台账口径），还是先在 core 侧对齐旧行为再切** |
| **⑯ 规则参数词典的三处语义（QT-74 的前置）** | **QT-74**（risk）、**G4 / H3**（core 消费半边） | 2026-10-25 H3 核对新增。QT-74 已按 core 实际消费的 14 项给出 1:1 提案（键名同 `risk_manager.*`、权重一律**小数**），但三条是**领域口径、不是工程岔路**：**需你拍**：① **单位**——`max_position_weight: 0.10` 定成「组合净值的小数占比」可以吗（本仓夹具今天的 `max_pct: 10` 是百分数形态，两种读法差两个数量级，定一个就得改另一个）；② **scope 消费面**——一期 core 只读 `global`、`account`/`strategy`/`symbol` 三档「下发但本 run 不参与裁决」并显式登记，还是要求回测也按 scope 生效（后者要先给回测一个账户/策略身份来源，是另一张单）；③ **`risk.limits` 的处置**——冻结它的 `limit_key` 词典，还是成文「限额只走 `rule_snapshots`，`risk.limits` 是无生产者的遗留空表」。这条已在 risk 台账里重复登记 5 次未收口 |

---

## 第 6 节 · 账实不符

> 每条给证据（命令输出或 file:line）。**以实测为准**。
>
> **📌 本节状态（2026-10-25 12:00 更新）**：下列 §6.1~§6.15 的**发现全部已按 [裁决记录](cross-repo-decision-record-2026-10-25.md) 处置**，各条末尾标了处置结果：
> §6.1 ✅ 已处置（DBT-15/16/17 在 main 重做落账 + 7 笔 commit 逐笔判处置）· §6.2 ✅ 已快照 · §6.3 ✅ 已收口（11 项 WIP 已提交）· §6.4 ✅ 已订正 · §6.5 ✅ 记账正确 · §6.6 ✅ 已快照 · §6.7 ✅ 已订正 · §6.8 ✅ 已订正 · §6.9 ✅ 已订正 · §6.10 ✅ 已归位 · §6.11 ✅ 已立单 · §6.12 ✅ 已登记 · §6.13 ✅ 已订正（四仓 AGENTS）· §6.14 ✅ 已补测 · §6.15 ✅ 已裁决（⑬⑭⑮）。
> **本节保留原始发现与证据**（不删改历史记录），供追溯「这些不一致是怎么发现、怎么结的」。

### §6.1 ⚠️ 最重要：`tellurion-g3-data` 不是第 13 个仓，是 core 的 git worktree，且带着 7 个**未合并、未推送**的 commit

**证据**：

```
$ git -C tellurion-g3-data/.git        # 实际是一个 gitdir 指针文件
gitdir: C:/Users/ruoxi/Workspace/tellurion/.git/worktrees/tellurion-g3-data

$ git -C tellurion worktree list
C:/Users/ruoxi/Workspace/tellurion            ab62b74 [main]
C:/Users/ruoxi/AppData/Local/Temp/b63-verify  83a2efd (detached HEAD)
C:/Users/ruoxi/Workspace/tellurion-g3-data    17b62f3 [task/g3-data]

$ git -C tellurion log --oneline main..task/g3-data
17b62f3 docs(data): DBT-17 落账 —— capital_flow / top_list 登记为数据储备
3aabda8 docs(equitydeep): DBT-16 落账 —— fundamentals_detail 成文「不可得」+ 产出请求
49a97c7 docs(ingest): DBT-15 落账 —— ingest.raw 残行按锚点删除的处置与取证
eb33d8d feat(equitydeep): DBT-14 裁决 A —— fetched_at 改吃 RFC 3339，契约不动
7e17c12 perf(data/json): DATA-14 自审订正——切片懒复制 + 指针分支省分配
d25084c docs(tasks): G3 决策材料——DBT-14/15/16/17 取证 + DBT-18 归属指针
ff36b4c fix(data/json): DATA-14 空列表编成 [] 而不是 null

$ git -C tellurion merge-base --is-ancestor task/g3-data main ; echo $?
1                                    # 未合入 main
$ git -C tellurion rev-list --count task/g3-data..main
46                                   # main 比它新 46 个 commit
$ git -C tellurion-g3-data rev-list --count origin/main..HEAD
7                                    # 7 个 commit 未推 origin
```

**台账写了但实测不成立的**：
- core `docs/TASKS.md` 的 DBT-15 / DBT-16 / DBT-17 标题行**仍是 ⬜ 待做**（实测 `docs/TASKS.md:1707 / 1716 / 1725`），而 `tellurion-g3-data/docs/TASKS.md` 里三张单**已是 ✅ 2026-10-23**（实测 `tellurion-g3-data/docs/TASKS.md:1117 / 1206 / 1277`）。
- core `docs/TASKS.md` DBT-13 结案段原文：「**2026-10-25 订正**：裁-16（DBT-14）已裁决并施工完成（本单 ✅）；其余四单仍为 ⬜ —— **材料不等于裁决**；**DBT-15 未执行任何 `DELETE` / `TRUNCATE`**」（实测 `docs/TASKS.md:1651` 附近）。

**但实测有、台账没记的**：
- **`DELETE` 已经执行过了。** `task/g3-data` 分支上的 DBT-15 施工记录写着：「两条语句与 ③-② 备好的**逐字一致**（临时执行器跑完即删，不进仓）」「`DELETE FROM ingest.raw WHERE payload ? 'probe' AND source='akshare';` — 影响 **164 行**」「`DELETE FROM ingest.raw WHERE source LIKE 'unit-test%';` — 影响 **2 行**」「`total=11,968 / probe=0 / unit=0 / tushare=11,968`」。
- 该分支上的 DBT-16 已写进 `docs/SPEC.md`（「`fundamentals_detail` 的产出方（DBT-16 裁决：先成文「不可得」）」一节）。
- 该分支上的 DBT-17 已把两表登记为「仅储备」。
- 该分支的 DBT-14 是**另一套实现**（`eb33d8d`「改吃 RFC 3339，契约不动」），与 main 上的裁-16 实现（`pkg/data/equitydeep/equitydeep.go:132` 的 `parseFetchedAt`）**不是同一份** ⇒ cherry-pick 会撞。

**处置**：**§5 的 ⑤⑥⑦ 三条裁决项因此不是「还没做」，而是「做在了一个未合并的分支上，且其中一条动了共享库的数据」**。在裁 ⑤⑥⑦ 之前，**不许把这 7 个 commit 合进 main**（会与 main 的 46 个 commit 大面积撞车，实测 diff 触及 139 个文件 / `-14869` 行）。

> **✅ 已处置（2026-10-25 12:00）**：⑤⑥⑦ 已按 [裁决记录](cross-repo-decision-record-2026-10-25.md) §二 落定并在 **main 上重做落账**：
> - **DBT-15** ⇒ core `docs/TASKS.md` 标 ✅（删除已执行并追认，含 164+2 行 / `12,134 → 11,968` 的实测读数与 fail-closed 授权判据）+ `docs/SPEC.md` 新增「`ingest.raw` 里只放真归档」一节；
> - **DBT-16** ⇒ 标 ✅（成文「不可得」+ 产出请求），`docs/SPEC.md` 新增「`fundamentals_detail` 的产出方」一节 + **订正 `:1876` 那行已被证伪的阻塞理由**；
> - **DBT-17** ⇒ 标 ✅（两表登记「仅储备」），`docs/SPEC.md` 新增「`capital_flow` / `top_list` 的用途」一节（含「`top_list` 在 cron 名单 + 无排程行 = 事实上不日更」的口径订正）；
> - **DBT-18** ⇒ 标 ✅（归属 = agent 仓），core 跨仓任务索引加指针、agent 台账加 DBT-18 条目。
> **7 笔 commit 的逐笔处置**见 [裁决记录 §四.1](cross-repo-decision-record-2026-10-25.md)：4 笔代码作废（main 已有等价或口径不同的实现）、3 笔纯文档**取结论不取 patch**（实测 cherry-pick 会撞 `cmd/data/sync_handlers.go`）。
> **该 worktree 的物理删除（`git worktree remove` + 删 `task/g3-data` 分支）本轮未执行** —— 属不可逆操作，等协调者确认。

### §6.2 12 仓「未推 commit 数」实测（**已 `git fetch` 全部 12 仓**）

> **✅ 已处置（2026-10-25 12:00）**：本表是 11:10 的快照，已被 [裁决记录 §四](cross-repo-decision-record-2026-10-25.md) 的 12:00 快照取代（core 4 / contracts 1 / kit 0 / data 1 / strategy 0 / risk 2 / analytics 2 / orchestration 1 / broker 3 / agent 0 / ui 1 / docs 2）。**kit 的 `v0.1.2` / `v0.1.3` 已推 ⇒ QT-71 外部前置解除。**

| 仓 | ahead `origin/main..HEAD` | 工作区 | 备注 |
|---|---|---|---|
| `tellurion` | **0** | **脏，11 项** | 见 §6.3 |
| `tellurion-contracts` | **1** | 干净 | commit `072d471`「docs(agents): QT-07 ⑥ 结案 —— 收回『agent 仓外 replace』的过渡例外口径」 |
| `tellurion-kit` | **0** | 干净 | HEAD `7280ce6`（KT-09/KT-10 立单） |
| `tellurion-data` | 0 | 干净 | |
| `tellurion-strategy` | 0 | 干净 | |
| `tellurion-risk` | 0 | 干净 | |
| `tellurion-analytics` | 0 | 干净 | |
| `tellurion-orchestration` | 0 | 干净 | |
| `tellurion-broker` | 0 | 干净 | |
| `tellurion-agent` | 0 | 干净 | |
| `tellurion-ui` | 0 | 干净 | |
| `tellurion-docs` | 0 | 干净 | |

**台账写了但实测不成立**：core `docs/TASKS.md` 的 QT-07 ⑥ 结案记录（实测 `docs/TASKS.md` QT-07 段）称「agent 侧删 `replace tellurion => ../tellurion`……core 侧同批清空两条为它开的过期容忍」——**这些都已落地并推送**（core HEAD `7b20aa8` == `origin/main`，ahead 0）⇒ **这条不是欠账，是已完成**。

### §6.3 ⚠️ core 工作区**不干净**（11 项），且与本方案 G1 的文件域**部分重叠**

> **✅ 已处置（2026-10-25 12:00）**：实测那 11 项**已提交**（`a53fe95` / `0cb8e9f` / `aa729f2` = QT-42 X3c 二批「告警回传改成持久 outbox」）⇒ core 工作区**不再有那批 WIP**，G1 可安全开工。详见 [裁决记录 §四.2](cross-repo-decision-record-2026-10-25.md)。

```
$ git -C tellurion status --porcelain
 M cmd/analysis/main.go
 M cmd/analysis/setup.go
 M cmd/analysis/wiring/risk_events.go
 M cmd/analysis/wiring/risk_events_test.go
 M config/analysis-service.yaml
 M docs/TASKS.md
 M pkg/storage/integration_test.go
 M pkg/storage/postgres.go
?? pkg/storage/alert_outbox.go
?? pkg/storage/alert_outbox_integration_test.go
?? pkg/storage/alert_outbox_test.go
```

**台账没记的**：core `docs/TASKS.md` 里 **QT-42 X3c 的「交付语义：outbox，不是内存队列（二批）」这一段是工作区未提交的改动**（实测 `git diff -- docs/TASKS.md` 显示这段是 `+` 行、`-` 行是旧的「内存队列 + 无持久化 outbox」口径）。
⇒ **含义**：core 的 outbox 改造（`quant.alert_outbox` / Migration 060 / `pkg/storage/alert_outbox.go`）**还没提交**。这与 G1 的 `QT-72` 撞 `pkg/storage/postgres.go`、与 G1 的 `QT-71` 撞 `cmd/analysis/main.go` / `setup.go`。
⇒ **派发前必须先由协调者把这批改动提交或丢弃**，否则 G1 的两个 agent 一开工就与别人的 WIP 撞车。**本方案不安排任何 agent 去处置这批 WIP**（属「别人正在写的活」）。

### §6.4 QT-73 的前提「五包字节相同 / 导出面逐项相同」——**部分被证伪**

> **✅ 已处置（2026-10-25 12:00）**：QT-73 已落（`170f09a`），其完成记录 ② **逐条订正了这三条前提**（`clock` 的 8 处引用全在被删目录内 ⇒ 保留代码零引用；`logging` / `httpclient` 两条行为差异如实登记并报协调者）。本条 §6.4 的「`clock` 是改名 ⇒ 切包时必须一并改调用点」这半句**已被实测推翻**。裁决见 [裁决记录 ②⑮](cross-repo-decision-record-2026-10-25.md)。

QT-73 ② 原文：「`errors`（4806/4806 字节）与 `decimal`（17702）与 kit **字节相同**；`clock` / `logging` / `httpclient` 导出面逐项相同」。

**实测**：

| 包 | core | kit | 结论 |
|---|---|---|---|
| `errors` | `errors.go` 4806 B，`sha256=D0A2C645…` | `errors.go` 4806 B，`sha256=D0A2C645…` | ✅ 字节相同（kit 多一个 `errors_test.go`） |
| `decimal` | `decimal.go` 17702 B，`sha256=589480F3…` | `decimal.go` 17702 B，`sha256=589480F3…` | ✅ 主文件字节相同（`decimal_test.go` 不同：core 14650 B vs kit 12858 B，kit 多一个 `div_rounding_test.go`） |
| `clock` | `pkg/clock/interfaces.go` 7037 B，`sha256=AF3E8AD2E0` | `clock/clock.go` 6847 B，`sha256=161A700110` | ❌ **文件名与字节都不同**；**常量名不同**：core `ModeBacktest`（`interfaces.go:30`）vs kit `ModeDeterministic`（`clock.go:28`）；core 内 `clock.ModeBacktest` 有 **8 处**引用 |
| `logging` | `pkg/logging/logger.go` 1151 B，`sha256=E7098D070B` | `logging/logger.go` 1540 B，`sha256=87B25B47AA` | ❌ 字节不同；**行为不同**：core `logger.go:35` 仍有 `zerolog.SetGlobalLevel(zerolog.DebugLevel)`，kit `logger.go:22` 是 `zerolog.SetGlobalLevel(parseLevel(level))` |
| `httpclient` | `pkg/httpclient/client.go` 3829 B，`sha256=567D94B466` | `httpclient/client.go` 5582 B，`sha256=374E576293` | ❌ 字节不同；**行为不同**：kit 多出 `newStatusError`（`client.go:167`），core 副本**没有** ⇒ kit 修过的「4xx 静默当成功」（KT-04）在 core 侧仍在 |

**结论**：QT-73 ② 的「零语义五包」**只对 `errors` + `decimal` 成立**；`clock` 是**改名**（不是纯 import 替换），`logging` / `httpclient` 是**行为差异**。QT-73 ④「不许顺手改语义」与「不许顺手改语义 ⇒ 只切包并登记」这两条**互相矛盾**，已列入 §5 ② 待裁。

### §6.5 QT-73 ② 的 import 点数：**实测与台账完全一致**（✅ 记账正确）

> **✅ 已处置（2026-10-25 12:00）**：QT-73 已落（`170f09a`），实测切包后「旧 import 字符串」在 `.go` 里只剩 5 处**字符串名单**（`internal/repoguard/package_wiring_test.go` 的 `retiredPackages` 复活即红护栏），真 import 零命中。台账 §6.5 的 90 处 / 87 文件经复核**一字不差**。

QT-73 ② 原文：「core 侧 import 点数 `errors` 32、`decimal` 1、`clock` 27、`logging` 26、`httpclient` 4，合计 **90** 处」。

**实测**（全仓 `*.go` grep `"tellurion/pkg/<pkg>"`）：`errors=32`、`decimal=1`、`clock=27`、`logging=26`、`httpclient=4` ⇒ **90，一字不差** ✅。
二期：`statistics=20`、`httpserver=34` ⇒ 与 QT-73 ② / KT-10 ② 的「20 处 / 34 处」一致 ✅。

### §6.6 各仓 `go.mod` 对 contracts / kit 的 pin —— **全部是 tag，零伪版本**（台账正确）

> **✅ 已处置（2026-10-25 12:00）**：pin 表已被 12:00 快照取代。**新增一条**：core 现在有 kit pin `v0.1.1`（QT-73 加的）⇒ `check_contract_pin.py` 实跑 **10 个 pin 全 GREEN**。kit 远端已到 **`v0.1.3`**。

```
core        contracts v0.6.0          kit (无 require)
contracts   (自身)                    —
kit         (自身)                    —
data        contracts v0.6.0          kit v0.1.1
strategy    contracts v0.5.0          kit (无)
risk        (无)                      kit v0.1.1
analytics   (无)                      kit v0.1.1
orchestration (无)                    (无)
broker      (无)                      (无)
agent       contracts v0.6.0          kit v0.1.0
ui / docs   (非 Go 仓)
```

**远端 tag 实测**（`git ls-remote --tags`）：
- contracts：`v0.1.0` `v0.1.1` `v0.2.0` `v0.3.0` `v0.4.0` `v0.5.0` `v0.6.0` —— **7 个 tag 全部真实存在且已推** ✅
- kit：`v0.1.0` `v0.1.1` —— **2 个 tag 全部真实存在且已推** ✅（**`v0.1.2` 不存在**，⇒ KT-09 必须打）

**⚠️ 一处版本不齐**（台账没点名的）：`agent` pin kit **`v0.1.0`**，而 `data` / `risk` / `analytics` pin kit **`v0.1.1`**。`v0.1.1` 就是 KT-08 的 `auth/httpadapter` + 启动闸。⇒ agent 仓若要用 net/http 适配，**得先升到 `v0.1.1`**；今天 agent 不用它（实测 agent 全仓 `kit/auth` 零命中）⇒ 不阻塞本轮，但**是 QT-71 之后的一颗雷**（若 agent 侧要接 service token）。
**pin 护栏实测**：`python tools/check_contract_pin.py` ⇒ **9 个 pin 全是目标仓 main 的祖先，退出 0，GREEN** ✅（4 个仓无 pin 跳过）。

### §6.7 QT-44 的 X0 与 QT-72 的 X0 —— **裁-19 已把读面改判给 core，orchestration 侧不再是读面 owner**

> **✅ 已处置（2026-10-25 12:00）**：core QT-72 ② 已按 [裁决 ⑬](cross-repo-decision-record-2026-10-25.md) 改写（三态锚点 = orchestration 的三码；core 端点不依赖 orchestration 进程）；orchestration 台账 QT-44 的 X0 行已加 ⑬ 归属订正（`producer_pending` = **orchestration 侧待实现的提示位**，不是第四个状态码）。

- orchestration 台账施工 ⑨ 末（实测 `tellurion-orchestration/docs/TASKS.md` 施工 ⑨ 段）写：「`run_artifacts` 的**生产者**（core 引擎在日循环前写留痕；本仓零 import core，不能代替）」「**未落（如实）**：① `run_artifacts` 的**生产者**（core 引擎在日循环前写留痕……）」。
- core 的 QT-72 ② 原文（实测 `tellurion/docs/TASKS.md:1558-1565`）：「**裁-19 定稿**：读面落 core；`run_artifacts` 重新定义为『worker 写的投影』，**core 不做双写**」。
- ⇒ **两处台账口径已被裁-19 改判**，orchestration 侧的「producer」表述需要同步订正（它现在等的是 worker，不是 core 引擎）。**本方案已把 G3 与 G1 的 QT-72 判为可并行**（两者文件域零重叠、裁决已定）。

### §6.8 「某符号还没进契约」的抽样证伪 —— **台账正确，契约已落**

> **✅ 已处置（2026-10-25 12:00）**：`w1-repo-ownership.md` §2.4 的「仍**没有**」已加订正块（三个符号实测都在）。

| 台账声称 | 实测 | 结论 |
|---|---|---|
| QT-22 契约腿「`engine/provenance.go` 追加 `StrategyVersionInput` + `Hash()` / `MustHash()`」 | `tellurion-contracts/engine/provenance.go:65` `type StrategyVersionInput struct`、`:78` `func (in StrategyVersionInput) Hash()`、`:90` `MustHash()` | ✅ 存在 |
| QT-22 域标签 `DomainStrategyVersionHash` | `tellurion-contracts/engine/canonical.go:59` `= "tellurion.strategy_version_hash.v1"` | ✅ 存在 |
| QT-16 域标签 `DomainRunIdentityHash`（w1 所有权表 §2.5 裁决） | `tellurion-contracts/engine/canonical.go:66` `= "tellurion.run_identity_hash.v1"` | ✅ 存在 |
| QT-40 X1a 契约腿「contracts 新增 `dataprotocol` 包」 | `tellurion-contracts/dataprotocol/dataprotocol.go` + `dataprotocol_test.go` 存在 | ✅ 存在 |
| core 侧 `pkg/storage/bulk_insert.go` 的 `UnifiedDataPoint` 已改契约别名 | `pkg/storage/bulk_insert.go:10` `import "github.com/ruoxizhnya/tellurion-contracts/dataprotocol"`、`:20` `type UnifiedDataPoint = dataprotocol.UnifiedDataPoint` | ✅ 存在（**别名**、非镜像） |
| w1 所有权表 §2.4「contracts 的 `engine/provenance.go` 仍**没有** `StrategyVersionInput` / `DomainStrategyVersionHash`」（2026-10-24 实测口径） | **已证伪**：两个符号都在（`provenance.go:65` / `canonical.go:59`） | ⛔ **该所有权表 §2.4 的「现状」段已过期**，须订正 |

### §6.9 w1 所有权表（`w1-repo-ownership.md`）本身与实测的差异

> **✅ 已处置（2026-10-25 12:00）**：`w1-repo-ownership.md` §2.4 的「现状」段**已加订正块**（`StrategyVersionInput` / `DomainStrategyVersionHash` / `DomainRunIdentityHash` 均已存在；「改域标签会让历史 hash 失效必须升 v1→v2」仍有效）。

该文件是本任务指定的必读真相源之一，但它是 **2026-10-23 定盘**的快照，几条关键前提已被后续施工推翻：

| w1 所有权表写的 | 实测 | 状态 |
|---|---|---|
| §2.1「QT-10 落 `contracts/engine/execution_errors.go`（新建）」 | 需另核（本次未 grep 该文件） | **未核实** |
| §2.2「QT-11 的 14 个注册点」 | 同上 | **未核实**（该表 §2.2 末尾自己已订正为 15 处） |
| §2.4「现状（2026-10-24 实测）：contracts 的 `engine/provenance.go` 仍**没有** `StrategyVersionInput` / `DomainStrategyVersionHash`」 | 两个符号都在（见 §6.8） | ⛔ **已证伪** |
| §3 撞车点表「`pkg/backtest/engine.go` 10/12/14/15」 | W1 七单**已全部 ✅**（core 台账执行顺序总览） | 已过期（历史撞车点，当前施工用） |
| §1「总账：W1 七单中 5 单纯 core」 | 与现况一致 | ✅ 仍成立 |

⇒ **本方案的分组不采用 w1 所有权表的撞车表作为当前串行依据**（它描述的是 W1 历史施工期）；当前撞车点以本文件 §2 节实测表为准。

### §6.10 台账里「未落项」原文照抄（不写成已完成）

> **✅ 已处置（2026-10-25 12:00）**：各条未落项**仍原样在册**（未假装完成），但已按裁决更新状态者已更新：DBT-15/16/17/18 由 ⬜ 改 ✅（结论落账）；data / risk / analytics / orchestration / broker / agent / ui / docs 各条未落项**一字未改**（仍是未落）。

按要求，**部分实现不等于完成**。以下各仓台账明写的未落项，本方案**原样保留**，未把它们写成已完成：

| 仓 / 单 | 台账原文未落项 |
|---|---|
| `tellurion-data` QT-40 | 「**第二写者未消灭**：core 侧同步腿仍在写 `market` / `ingest`；本仓只能先立 owner（DDL）与写闸门。物理 `SET SCHEMA` 搬迁（D4）未做」「**`visible_at` 生产未回填**……生产库回填仍须显式启动开关或运维执行，**未做**」「**传输未搬**：Tushare / 东财 / Mootdx 的 HTTP/SDK 客户端与 `ingest.raw` 归档仍在 core」「**写面代码已落、装配未接**：`ETLPipeline` 与 `store.BulkInsert` 在本仓……但**同步作业层仍在 core**」 |
| `tellurion-risk` QT-42 | 「**core 侧消费接线**：⬜ 「拿 bundle 进 `ParseBundle` 构建 `RiskPolicy`」仍未接（core 全仓 `ParseBundle` 零调用点）」「**熔断执行方与 ACK**：`circuit_orders.status` 今天只写 `applied`」「**外部推送通道**（webhook / 消息平台）与节流：本切片只做 HTTP 报表，不主动推送」「`risk.limits` 的读写路径与端点：表已建，读取口径未冻结」「**审计读取无 HTTP 端点**」「`rule_deprecated` 错误码不可达」「**core 调本仓的 token 分发（跨仓）**」「`POST /events` 不落审计行 / `risk_events` 无 actor 列」「生产侧投递**不满足「不允许丢」**……**无 outbox**」「真实投递腿只在 core 侧用 `httptest` 自证；『core 真进程 → risk 真进程 → `monitor/alerts` 读出非空』这条端到端**仍未跑过**」 |
| `tellurion-analytics` QT-43 | 「**执行侧（worker）仍未落地**：父汇总由执行侧调用 `parents.ApplyBatchSummary` 产生；本仓只读。W8 的真进程 E2E 用 SQL 模拟该写回，**不代表 worker 已存在**」「**`base_run_spec` 只有 `run_id`**」「**R1 的 K9-correct 持久读面**：核心在 core 侧……本仓仍是 interim 适配器」「**R2 artifact 的 owner 与最终形状**：owner 未定、**未迁 `tellurion-contracts`**」「**token 分发（跨仓）**」「PDF：非目标，不做」 |
| `tellurion-orchestration` QT-44 | 「**`run_artifacts` 的生产者**（core 引擎在日循环前写留痕；本仓零 import core，不能代替）」「**worker 进程与 worker 端点**」「**子 run 入队**（`parent_job_id` 的写入方，父 GET 的 `children` 因此通常为空）」「鉴权（R5 / 裁-15 缓做）」 |
| `tellurion-broker` QT-45 | 「**XTP 全量适配器接入这份规则（归 X6c）**」「core `risk.ValidateOrderQuantity` 退役与 alias（跨仓腿，本仓不改 core）」「**对账留痕进 risk（X3）未接**」「**core 侧旧 `LiveTrader` / `Broker` 未退役（R4）**」「`broker.order_acks` 的**保留期未定**」「**X6d 裁决：暂缓**」 |
| `tellurion-strategy` QT-41/41b/41c | 三单均 ✅；未落项为「`storage='url'`、`carrier=external` hash 语义、core 侧 Mount 门仍是非目标/跨仓」 |
| `tellurion-agent` | QT-17 ✅ / S3 ✅ / QT-65 ✅；**无 QT-32 的六字段条目**（实测 §6.11） |
| `tellurion-ui` AUD-61 | 「阶段二 ①（⬜ 待做）：结构漂移/视觉基线 16 条，等 `feat/design-tokens-v2-palette-sweep` 合并」；**该分支只存在于旧仓 `quant-trading-ui`** |
| `tellurion-docs` DOC-06 | 「🔶 提纲已成，待口径」——**待口径三问**未答 |

### §6.11 agent 仓缺 QT-32 的六字段条目（实测）

> **✅ 已处置（2026-10-25 12:00）**：agent 仓已立 **`QT-32-A`（agent 腿）· AI 生成链留痕 + promote 闸门的生产者侧 fail-closed** 🔶 施工中（`tellurion-agent:docs/TASKS.md`）。

QT-32 ③-⑥ 原文：「生成质量与实验编排属 agent 仓（**其台账另立单**）」。
**实测**：`tellurion-agent/docs/TASKS.md` 全文只有一处提到 QT-32，且是旧记录（`:284`「`QT-32`（依赖 G1-QT-26 / G5-QT-41b）继续等待」）——**不是六字段条目**。
⇒ **G8 的第一步是「立单」，不是「施工」**。

### §6.12 risk / analytics / orchestration / broker 四仓的 contracts pin —— **零 contracts 依赖**

> **✅ 已处置（2026-10-25 12:00）**：四仓 AGENTS 的版本注记已订正（见 §6.13）；「零 contracts 依赖」这半句**仍然成立**（实测 import = 0）。

实测 import 计数：

```
tellurion          contracts=472   kit=0
tellurion-data     contracts=43    kit=7
tellurion-strategy contracts=16    kit=0
tellurion-risk     contracts=0     kit=6
tellurion-analytics contracts=0    kit=6
tellurion-orchestration contracts=0 kit=0
tellurion-broker   contracts=0     kit=0
tellurion-agent    contracts=67    kit=29
```

⇒ risk / analytics / orchestration / broker **四个仓今天零 contracts 依赖**，所以「契约先发版再升 pin」这条前置**对它们不成立**（它们不 import contracts）。台账 AGENTS 里写的「未引入（最新 `v0.3.0`）」——**实测 contracts 最新是 `v0.6.0`** ⇒ 四仓 AGENTS 的版本注记**过期了一个大版本**，已记入 §6.13。

### §6.13 四仓 AGENTS.md 的 contracts 版本注记过期

> **✅ 已处置（2026-10-25 12:00）**：risk / orchestration / broker 三仓 AGENTS 的「最新 `v0.3.0`」**已改为「远端最新 `v0.6.0`」**并附实测注记；analytics 早已由 `ba53d39` 校准。

`tellurion-risk` / `tellurion-analytics` / `tellurion-orchestration` / `tellurion-broker` 四仓的 `AGENTS.md` §「依赖的契约版本」表都写「`tellurion-contracts` | 未引入（**最新 `v0.3.0`**）」。
**实测**：contracts 最新 tag 是 **`v0.6.0`**（`git ls-remote --tags` ⇒ `refs/tags/v0.6.0`），且 `v0.4.0` / `v0.5.0` / `v0.6.0` 三个 tag 都真实存在。
⇒ 四仓 AGENTS 的「最新版本」注记**落后 3 个版本**，但「未引入」这半句是对的（实测 import = 0）。**不阻塞施工**，但发版说明与 AGENTS 一致性护栏会受影响。

### §6.14 补测：六项「未核实」现已全部核实

> **✅ 已处置（2026-10-25 12:00）**：六项补测结论**全部已落进对应台账**：① `execution_errors.go` 三个哨兵存在（台账正确）；② 15 处注册点已落（`NewInstance` 49 处引用）；③ `kernel_modules.schema.sql` **在 core 仓内**（`tellurion/contracts/`）——已在 §6.14 表中登记；④ `producer_pending` 归属已裁（⑬）；⑤ promote 审批人缺口已登记（⑭）；⑥ `PaperSession` 平行实现仍在（w1 表 §5 的漂移风险成立，该单至今未立）。

| 项 | 怎么核 | 实测结果 |
|---|---|---|
| w1 所有权表 §2.1 的 `contracts/engine/execution_errors.go` 是否真存在 | `grep -n "ErrDataGap\|ErrStrategyFault\|ErrContractBreach" tellurion-contracts/engine/execution_errors.go` | ✅ **存在**（`tellurion-contracts/engine/execution_errors.go`）：`:27` `ErrDataGap`、`:34` `ErrStrategyFault`、`:42` `ErrContractBreach`。台账正确 |
| w1 所有权表 §2.2 的注册点是否都在 | `grep -rn "NewInstance" tellurion/pkg/strategy/` | ✅ **49 处** `NewInstance` 引用（`pkg/strategy` 全树）⇒ 15 处注册点已按表 §2.2 末尾的订正落地 |
| `contracts/kernel_modules.schema.sql` 的 PK 是否已按 QT-16 改 | `grep -n "shard_key\|PRIMARY KEY" …` | ⚠️ **该文件不在 contracts 仓** —— 实测路径是 **`tellurion/contracts/kernel_modules.schema.sql`（core 仓内）**，不是 `tellurion-contracts/kernel_modules.schema.sql`（**不存在**）。**w1 所有权表 §1 表里写的是「`contracts/kernel_modules.schema.sql`（core 内 K0 冻结资产，必改）」——「core 内」是对的**；但它不在 contracts 仓的顶层文件清单里（实测 contracts 仓顶层只有 `.gitattributes` / `.gitignore` / `AGENTS.md` / `CHANGELOG.md` / `doc.go` / `go.mod` / `go.sum` / `LICENSE` / `README.md`）⇒ **该文件是 core 仓的冻结资产，不是 contracts 仓的**。**这直接影响 QT-16 的 DDL 归属判断**：任何 agent 若按「contracts 仓的冻结 schema」去找会扑空 |
| orchestration 的 `producer_pending` 字段是否真在 wire 上 | `grep -rn "producer_pending" tellurion-orchestration/` | ⚠️ **0 命中**。core QT-72 ② 说「orchestration 端点保持 `producer_pending`」——**该字面量今天在 orchestration 仓不存在**。⇒ 要么它是**待实现的语义约定**、要么在别的名字下。**QT-72 施工时必须先核这个字段到底叫什么**（见 §5 追加项 ⑬） |
| QT-32 的 promote 闸门在 strategy 侧是否已有审计留痕 | `grep -rn "promote" tellurion-strategy/internal/asset/` | ⚠️ **端点与状态机都在，但「审批人/时间/结论」三类留痕缺两项**：<br>· 端点：`tellurion-strategy/internal/httpapi/api.go:46` `POST /api/strategies/{id}/versions/{hash}/promote` ✅<br>· 服务：`internal/asset/service.go:466` `func (s *Service) Promote(ctx, id, versionHash, toolchain)` ✅，状态机 `StatusValidated → StatusPromoted` ✅，并有 `cache.InvalidateLatest(id)` 的同刻失效 ✅（注释自述「F 裁决」）<br>· 持久：`internal/asset/pgstore.go:393` `Promote(...)` → `updateStatus(..., log)`，**写入 `strategy.strategy_artifacts.compile_log`**（追加式），`log` 参数当前硬编码为 `"promoted"`（`service.go` 调用处）<br>· ❌ **全仓 `grep approved_by\|approver\|audit` = 0 命中** ⇒ **没有审批人字段、没有独立审计表**<br>⇒ **QT-32 ③-② 明写「闸门记录进审计，审批人/时间/结论留痕」——今天只落了「结论」的一部分（`compile_log` 追加 `"promoted"`），「审批人」与独立「审计」两条未落**。这是 QT-32 施工时**必须补**的洞，不是「已完成」 |
| `pkg/live` 的 `PaperSession` 平行实现是否仍存在（w1 所有权表 §5 提到的漂移风险） | `grep -n "Registry.Get\|generateSignals" tellurion/pkg/live/paper_session.go` | ⚠️ **平行实现仍在，且比 w1 表描述的更完整**：`pkg/live/paper_session.go:240` 调 `s.generateSignals(ctx, strat, window, todays)`；`:314-316` 的 `generateSignals` 注释写「按策略形态选执行模式（与回测 LocalSignalProvider.Signals 同一判定顺序：优先流式 BarHandler，否则批式 GenerateSignals）」；`:333` `return strat.GenerateSignals(ctx, window, s.portfolioSnapshot(ctx))`。⇒ **「回测侧改 per-run 实例后，paper 侧实例来源若仍走 `Registry.Get` 就会重现共享实例串状态」这条漂移风险仍然成立**（w1 所有权表 §5 明写「paper 侧单独立单，不在 14 里顺手改」——**该单至今未立**）。⚠️ 附带修正：w1 表 §5 写「`PaperSession.Run(..., strat strategy.Strategy)` 收的是策略实例」，实测 `:240` 的调用形态与之相符，但表里给的行号 `:316-334` 与实测 `:314-333` 有 2 行漂移 |

### §6.15 追加待裁决项（补测后新增）

> **✅ 已处置（2026-10-25 12:00）**：⑬⑭⑮ 三条**已裁决并落文档**（见 [裁决记录](cross-repo-decision-record-2026-10-25.md) ⑬ / ⑭ / ⑮）。

| 事项 | 卡住哪个单 | 需要你给什么 |
|---|---|---|
| **⑬ orchestration 端点的 `producer_pending` 字面量不存在** | **QT-72**（core）、**G3**（orchestration） | core QT-72 ② 原文「orchestration 端点保持 `producer_pending`」，但**实测 orchestration 仓 `producer_pending` = 0 命中**。**需你给：这个状态的准确字段名/位置**（或确认它是待实现的语义、允许 QT-72 自己定并同步两侧）。**QT-72 施工前必须先核**，否则 DTO 对拍无锚点 |
| **⑭ promote 闸门的「审批人」与「独立审计」两条未落** | **G8 / QT-32**（agent）、`tellurion-strategy` | QT-32 ③-② 要求「审批人/时间/结论留痕」；实测 strategy 侧只有 `compile_log` 追加 `"promoted"`，**无审批人字段、无独立审计表**。**需你给：审批人身份从哪来**（JWT claim？core 侧转发？service token？）—— 这决定闸门落在 strategy 仓还是 agent 仓 |

---

## 附：分组速查（可直接贴给各 agent）

```
阶段 A（现在就能全部开工，组间零等待）
  G1 core   : QT-73（切 kit 一期五包）
  G2 kit    : KT-09（IssueServiceToken → v0.1.2）
  G3 orch   : 子 run 入队（parent_job_id 写入方）
  G5 analytics: R1 读面换 K9-correct 持久读面
  G6 data   : 写面装配接线方案（不落代码）
  G8 agent  : QT-32 立单

阶段 B（等具体产物）
  core QT-71   等 kit v0.1.2（KT-09）
  core QT-72   等 core QT-71（route_policy.go 同一文件）
  risk ParseBundle 消费  等 core QT-71（service token + risk 策略表）
  analytics R2  等 contracts 发 analysis 包
  data 写面落代码      等 core 停第二写者
  data SET SCHEMA      等 data 写面装配
  core 二期切包        等 kit v0.1.3（KT-10）
  agent QT-32 施工     等协调者裁归属

纯裁决组（不进任何施工组）
  G7  broker  = XTP SDK + 柜台环境（外部）
  G9  ui      = design-tokens 分支合并（分支只在旧仓）
  G10 docs    = 产品口径三问
```