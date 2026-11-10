---
status: active
last-verified: 2026-10-26
verified-by: 第三批分组方案实测取证（12 仓 git 快照 + 全量未完成标题扫描 + 逐条裁决落地核查 + 关键源码行）
---

> **状态：部分结论已被取代（2026-10-26 二次实测）**
> - **K5「`Dockerfile.service` 仍 build `cmd/strategy`」已证伪**：该 build/COPY 行已删，现只 build `./cmd/data` / `./cmd/analysis`（`Dockerfile.service:24-25`）；本文件保留原判断作记录，勿照此派工。
> - 「六仓 ≠ 六进程」「X 线暂停」等口径已被 **ADR-036（2026-10-26 Accepted）** 取代；派发以 ADR-036 与 [`purpose-alignment-audit-2026-10-26.md`](purpose-alignment-audit-2026-10-26.md) §九 的复测订正为准。

# 第三批跨仓施工分组方案（2026-10-26）

> **本批的定位**：wave1 解决「从哪开始」，wave2 解决「已裁项怎么派」；本批解决**「裁决已下、代码没动的那些怎么真的动起来」**。
> 与前两批一样：**组间可并行，组内串行**；每单可独立 checkout 施工。
> **本批不写任何业务代码**，只给可派发的六字段与决策材料。
>
> **⚠️ 2026-10-26 后记（主任裁决）**：本文 §5 的「暂停 X 新动作 / 撤回容器清单 / compose 只跑 core」是当时的建议；同日主任选择**平台优先、并行 MVP**，见 `tellurion-docs:docs/adr/adr-036-service-topology-and-readiness-gate.md`。**本文的现状取证仍有效；处置建议以 ADR-036 与各仓 `SVC-*-G0/G1/G3` 为准。**

## §0 取证口径（先说清哪些是实测、哪些没做）

- **实测（本轮）**：12 仓 `git status --porcelain` / `git rev-list --count origin/main..HEAD` / `git tag` / `git branch` / `git worktree list`；12 仓 `docs/TASKS.md` 全量未完成标题扫描；各单关键 `repo:path:line` 逐条打开核对；`go.mod` 的 contracts/kit pin；`Dockerfile.service`、`docker-compose.yml`、`tools/check_deploy_consistency.py`、`config/analysis-service.yaml`。
- **未做（原始轮，如实）**：本轮静态取证时本机 **Go 不在 PATH**、**Docker daemon 不可达**，未跑 `build/vet/test`、未起服务、未连库、未跑 Docker。本批所有「验收」都写成**在有 Go 的环境里可机械判定**的判据，**不是**原始轮已通过的结论。
- **2026-10-26 复测（同日，主任授权）**：Go **1.25.14** 已恢复；Docker **Engine 29.8.0** 可达。已实测 **10 仓 `go build ./...` EXIT=0**、六服务 + core + ai 镜像全部构建成功、聚合栈 **5/6 healthy**（`analytics /healthz` 404，已由记录 #4 修复中）。**未跑**：全量 `go vet` / `go test`、真库集成腿、ui e2e。裁决见 `cross-repo-decision-record-2026-10-26.md`。
- **状态口径**：`⬜` 待做 / `🔶` 进行中 / `⛔` 有未解除前置。凡「裁决已下但代码未动」一律记 `⬜`，不记 `🔶`。

### 0.1 十二仓当前读数（2026-10-26）

| 仓 | 远端对齐 | 未完成标题 | 备注 |
|---|---|---|---|
| tellurion | 0 ahead / 0 behind | **QT-32 ⬜（:903）**、**QT-51 🔶（:1226）** | ~~工作区有第四轮审计的 6 改 + 1 新增（未提交）~~ → **2026-10-26 再核：那批已整体提交（`4f44dd6` / `49cfdd5` / `7735ab8` 等），工作树干净**；QT-51 段行号已漂过 `:1226` → `:1275` → **`:1425`**（本批落账时再测）——**两小时内漂两次**，表内行号随并发提交漂移，引用前先 `grep -n '^### QT-51' docs/TASKS.md`，别照抄 |
| tellurion-contracts | 0/0 | 无 | `risk` 包**不存在**（②未开工） |
| tellurion-kit | 0/0 | KT-11 🔶（:182） | **`v0.1.4` 已打 tag 并 push**（台账仍写「待发版」） |
| tellurion-data | 0/0 | QT-40 🔶（:22） | ⑰ 未开工 |
| tellurion-strategy | 0/0 | 无 | 零鉴权、零 kit 依赖（⑫ 未开工） |
| tellurion-risk | 0/0 | QT-42 🔶（:24）、**QT-74 ⬜（:447）** | kit 已升 `v0.1.4` |
| tellurion-analytics | 0/0 | QT-43 🔶（:22） | kit 已升 `v0.1.4` |
| tellurion-orchestration | 0/0 | QT-44 🔶（:22） | 无 kit/contracts 依赖；只有 artifact **读**面 |
| tellurion-broker | 0/0 | QT-45 🔶（:22） | R10 未修 |
| tellurion-agent | 0/0 | 无 | QT-32-A 已 ✅；QT-32-B 标「待裁」 |
| tellurion-ui | 0/0 | AUD-61 🔶（:27）、DATA-14 ⬜（:229） | AUD-61 ① 需三服务 |
| tellurion-docs | 0/0 | 无 | DOC-10 已结案（第四轮审计 §七） |

> ⚠️ **本表是 2026-10-26 上午快照**；落账后各仓已被并发施工方推进（G0 已落、QT-74 已落、risk 领先 5 笔）。**当前读数以各仓 `git log` 与 `docs/TASKS.md` 为准**，不要用本表判断「谁还没开工」。

### 0.2 本批**能**开工与**不能**开工的分界

- **能**：22 条裁决里凡是「已拍定 + 落点在单仓 + 不需要外部条件」的，全部进本批（K1~K9、K11）。
- **不能**（只出决策材料）：第四轮目的对齐审计 §六 的 A~F（新提出、未裁决）。~~以及需要 Go 环境复跑门禁才能合入的 K10~~ → **K10 已于 2026-10-26 改判「可派」**：Go 1.25.14 在本机可用（只是不在默认 Git Bash 的 PATH，需显式 export），前置不再是外部条件。
- **明确不动**：QT-45 / XTP（⑦ 已裁「本轮不投人」）；`task/g3-data` 遗留（㉑ 已发生，仅需追认）。

---

## §1 总览表

> 「前置」列写的是**必须已经为真**的事实，不是「最好先做」。「组内串行」指同一仓的多个 K 组不能同时动同一批文件。

| # | 组 | 仓 | 一句话内容 | 前置 | 需要 Go/Docker？ | 现在的状态 |
|---|---|---|---|---|---|---|
| **K1** | 规则参数词典 | risk | QT-74：四类规则 `params` 的字段名/单位/写侧校验落文档 + 夹具改小数 | 无（单仓自足） | Go（CI） | ⬜ 可立即派 |
| **K2** | 撤认领 `index_daily` | data | ⑰：删重复 DDL / C 集 / 豁免行 / 归属行，加「本仓不建该表」护栏 | 无 | Go（CI） | ⬜ 可立即派 |
| **K3** | 幂等错误映射 + 回执保留期 | broker | ㉒R10 两处映射改 `ErrIdempotencyStoreUnavailable`；⑨ 加 `reserved_at` 部分索引 + 90 天清理（只删 `ack IS NOT NULL`） | 无 | Go（CI） | ⬜ 可立即派 |
| **K4** | `/report` 结论态放行 | core | ⑲：`job.Status != "completed"` 的 4 处 404 改成三态（不存在 404 / 未完成 409 / 失败 409）+ openapi 同步 | 无 | Go（CI） | ⬜ 可立即派 |
| **K5** | 部署路径两个真 bug | core | `Dockerfile.service` 删已删除的 `./cmd/strategy`；`check_deploy_consistency.py` 解析 `BindEnv` 别名（当前假阳性） | 无 | Docker（可选）+ Go | ✅ 已落（2026-10-26，`605f6f4` 前身：checker exit 0 + 破坏验证） |
| **K6** | testid 窄例外 | ui | ⑳：`NavTiles.vue` / `MarketMetrics.vue` / `AppSidebar.vue` 只加 `data-testid`，单独 commit | 无 | Node | ⬜ 可立即派 |
| **K7** | artifact 写端点 + R5 | orchestration | ⑱：引 kit + 端点级鉴权 + `POST /api/runs/{id}/artifacts`（service 角色） | 无（kit `v0.1.4` 已发） | Go（CI） | ⬜ 可立即派 |
| **K8** | promote 闸门鉴权与留痕 | strategy | ⑫：引 kit + 三端点 service-token + 审批人取 core 网关 `X-Actor` + 落审计 | 无（kit `v0.1.4` 已发） | Go（CI） | ⬜ 可立即派 |
| **K9** | `contracts/risk` 包 | contracts | ②：新建 `risk` 包，wire 类型 + 冻结枚举 + hash 纯函数 + 4 哨兵，**编码逐字节搬运** | **K1 完成**（词典先定形） | Go（CI，要发 `v0.7.0`） | ⛔ 等 K1 |
| **K10** | QT-51 分支收口 | core | ①：从 `task/qt51-execution-port` 拉新 worktree → rebase 到 main → 复跑门禁 → 合入（冲突面实测：仅 `docs/TASKS.md` 一处真冲突，Go 侧零冲突，见 §K10） | **无**（Go 1.25.14 在本机可用，只是不在默认 PATH；见 §5 外部条件） | **必须 Go**（显式 PATH）+ CI（`-race`） | 🔶 可派（环境已解除） |
| **K11** | 未过 promote 产物被回测路径拒绝 | core | QT-32 ⑤ 执行侧腿：AI 产物未 promote ⇒ 回测路径 fail-closed | **K8 完成** + K10 合入 | Go（CI） | ⛔ 等 K8 |

**同仓串行关系（硬）**：core 的 K4 → K5 → K10 → K11 **必须依次**（K10 要独占 worktree；K11 会碰 `pkg/backtest` 与 K10 同面）。其余各组**跨仓，互不相干**。

---

## §2 分组说明（为什么是这些组、为什么不是那些）

### 2.1 分组的依据不是「单号相邻」，是三条实测边

1. **`market.index_daily` 的两套 DDL 是同一张表两个定义**（core `pkg/storage/postgres.go:1180` Migration 057 vs data `internal/schema/market.go:301`）。⑰ 已裁 owner = core，故 K2 是**单仓删除 + 撤认领**，不需要跨仓协调——但**必须先于**任何 A 集物理搬迁（D4）。
2. **risk 的 `params` 今天没有词典**（`canonicalParams` 只校验「非空 JSON、≤64 KiB」，`internal/rule/model.go:150` 不看键名），而 core 侧消费面是 14 个标量（`core:pkg/risk/manager.go:13-28`）。**没有词典就没有值得迁的契约形状**——这是 K1 → K9 的强制顺序。
3. **strategy 今天零鉴权、零 kit 依赖**（12 条路由全裸）；core 全仓 `"/promote"` 零命中。⑫ 裁决给了承载体（面 B1），而 kit `v0.1.4` 已把 service token 的 401 修掉 ⇒ K8 的**两个前置都已在为真**，可以派。

### 2.2 为什么把 K5（部署路径）单独列一组，而不是塞进「文档/杂项」

它是**两个真 bug**，不是文档漂移：

- `Dockerfile.service:26` 仍 `go build ./cmd/strategy`，而 core 的 `cmd/` 只有 `analysis` / `data` / `dualrun` / `worker`（实测目录列表）⇒ compose 的两份镜像**今天构建不出来**。
- `tools/check_deploy_consistency.py:203-207` 的 `_GO_KEY_CALL` 只取 `BindEnv` **第一个参数**，而读点在 `pkg/bootstrap/config.go:25` 的第二/第三个别名上 ⇒ **假阳性**。假护栏比没护栏更糟。

这两条都能在本机**不改任何产品行为**地修掉并验证（`python tools/check_deploy_consistency.py` 退出码可本机复跑）。

### 2.3 本批**故意不派**的

| 不派 | 原因 |
|---|---|
| QT-45 / XTP P1~P5 | ⑦ 已裁「本轮不投人」；四处零 SDK |
| AUD-61 ① 批（e2e 阶段一取红清单） | 要三服务起齐（原始轮记的 Docker daemon 不可达**已于 2026-10-26 解除**；仍缺前端栈本身）⇒ 外部条件，见 §5 |
| DATA-14（空列表 `null`） | 已迁 core，本仓只留指针；真实落点未核实到具体单，**不凭记忆派** |
| QT-40 / QT-42 / QT-43 / QT-44 的其余未落项 | 各自有未裁的**方向性**问题（见 §5 A）、或前置是别人的单；不硬塞进本批 |
| 任何 A 集表物理搬迁（D4） | 前置是「core 的 A 集写点全部归一或停写」，不是「停掉两个 executor」（data `docs/db-schema.md:79` 已如实登记） |
| `task/g3-data` 恢复 | ㉑ 已发生且结论零损失（DBT-15~18 均 ✅）；只差追认 |

---

## §3 派发原文（每组的六字段）

> 每组可直接整段粘给一个 agent。**不写时间估计**；「验收」全部是可机械判定的退出码或断言。

### K1 · risk QT-74 规则参数词典

- **① 目标**：把四类规则各自的 `params` 字段清单（名称/类型/单位/取值域/必填）冻结成**唯一真相源**，使 core 能逐字段映射而不用猜单位。
- **② 落点**：`tellurion-risk:docs/db-schema.md`（`rule_snapshots.rules[].params` 节）、`docs/api-schema.md`（规则 create/update 输入口径）、`docs/dataflow.md`（scope 消费口径）、`internal/rule/model.go`（写侧校验）、`internal/rule/snapshot_test.go`（夹具 `:144` 的 `{"max_pct":10}` 改为 `{"max_position_weight":0.10}`）。
- **③ 要求**：① 两张文档同一份词典、同名同序；② `Create`/`Update` 按 `type` 校验键集合与取值（未知键 / 缺必填 / 类型不符 / `NaN` / `Inf` / `fast_ma_period >= slow_ma_period` → `ErrInvalidParams` → 400 `invalid_params`）；③ **读侧不重校键集合**（`ParseBundle` / `SnapshotHash` 不得因词典增键而拒绝历史快照）；④ `scope` 口径写明「一期只消费 `global`，其余三档可见但不参与裁决」，不许静默忽略；⑤ `risk.limits` 的 `limit_key` **二选一**（冻结 + 词典引用，或明确「遗留空表、无生产者」）。
- **④ 约束**：**不改** `SnapshotHash` / `canonicalParams` 的编码算法（改 = 全历史 hash 作废）；**不**迁 contracts；**不**碰 core 任何文件；**不**给 `bundle` / `snapshots/{hash}` 开新档位；不改四类 `RuleType` 取值集。
- **⑤ 验收**：① 词典逐字段可查，无「待定」；② 每条校验有 fail-closed 测试（未知键 / 缺必填 / 单位形态错 / `NaN` / `fast>=slow`）；③ 历史快照兼容腿：词典之前的 `params` 原文调 `ParseBundle` 仍成功；④ `gofmt -l .` 空 + `go build/vet ./...` 退出 0 + `go test ./... -count=1` 绿。**破坏验证**：删一个必填键但留夹具 ⇒ 「未知键即拒」必须红；`max_position_weight` 改 `10`（百分数形态）⇒ 取值域校验必须红；两处逐字节还原后复绿。
- **⑥ 边界**：只到「本仓说清形状 + 写侧守住」。迁 contracts（K9）、core 侧 bundle→限额映射、`risk.limits` 端点都在本单之外。

### K2 · data 撤销对 `market.index_daily` 的认领

- **① 目标**：让 `market.index_daily` **只有一个 owner（core）和一个建表定义**，消除「谁先启动谁定形」的静默分歧。
- **② 落点（实测行）**：`internal/schema/market.go:301`（`CREATE TABLE IF NOT EXISTS market.index_daily`）+ `:317`（同名索引）；`internal/schema/schema.go:47`（C 集条目 `"market.index_daily"`）；`internal/schema/visible_at.go:54`（豁免行）；`internal/schema/schema_test.go:69`（断言它在豁免集里）；`docs/db-schema.md:25`（把自己列为该表归属）+ `:151`（C 集理由里的实测漏项段）。
- **③ 要求**：① 删 DDL 与索引、删 C 集条目、删豁免行及其断言；② `docs/db-schema.md` 把该表**移出**本仓表清单，并写明「owner = core，`pkg/storage/index_daily.go:31` 是唯一写者，core Migration 057 是唯一定义」；③ 新增一条护栏测试：**本仓迁移语句里不出现 `market.index_daily`**（防以后再被加回来）。
- **④ 约束**：**不**删 core 侧的 Migration 057 与 `pkg/storage/index_daily.go`（那是 owner）；**不**动其他 A 集表的 DDL；**不**做 `SET SCHEMA` 物理搬迁（D4 另有前置）；**不**改 `ingest.raw` 相关。
- **⑤ 验收**：① 全仓 `grep "market.index_daily"` 只剩 `docs/db-schema.md` 的「不归本仓」说明与护栏测试；② `go build/vet/test ./... -count=1` 绿；③ 破坏验证：把 DDL 加回 `market.go` ⇒ 新护栏必须红，还原后绿；④ `internal/schema` 的迁移语句条数减少且无其他表受影响（用测试断言条数或表名集合）。
- **⑥ 边界**：只做「撤认领 + 删重复定义 + 上护栏」。core 侧 schema 与写入路径一概不碰。

### K3 · broker R10 错误映射 + `order_acks` 保留期

- **① 目标**：幂等存储读失败不再被报成「通道断」；回执表有界增长（90 天，且只清已完成条目）。
- **② 落点（实测行）**：`internal/gateway/mock.go:358` 与 `:415`（两处 `fmt.Errorf("%w: %v", ErrChannelDown, err)` 实际是 store 读失败）；`internal/ackstore/ddl.go`（`broker.order_acks` 现为 `PRIMARY KEY (account_id, client_order_id)` + 可空 `ack` + `reserved_at` + `updated_at`，**无 `reserved_at` 索引**）。
- **③ 要求**：① 两处映射改 `ErrIdempotencyStoreUnavailable`（12 哨兵之一，已在 `errors.go`）；② 加 `reserved_at` 的部分索引（只索引 `ack IS NULL` 的未决行，供清理作业扫描）；③ 保留期清理：**只删 `ack IS NOT NULL` 且 `updated_at` 早于 90 天**的行，未决行永不删；④ 清理逻辑有可注入的时钟与批次上限（不在库里跑无限 `DELETE`）。
- **④ 约束**：**不改** 12 个 wire code 的语义与数量；**不**把清理做成 cron 之外的东西（不引 MQ / 不引外部调度）；**不**动 `request_fingerprint` 的唯一性与幂等语义；R9（`Kernels` 领取过滤）按 ㉒ 暂缓到 X6c，**本单不碰**。
- **⑤ 验收**：① 两条单测分别断言「store 读失败 → `errors.Is(err, ErrIdempotencyStoreUnavailable)`」且**不**是 `ErrChannelDown`；② DDL 断言索引存在且谓词为 `ack IS NULL`；③ 清理测试：构造 91 天前已完成 / 91 天前未决 / 89 天前已完成三行，断言只删第一行；④ 破坏验证：把任一映射改回 `ErrChannelDown` ⇒ 对应用例红；把清理条件去掉 `ack IS NOT NULL` ⇒ 未决行被删的用例红；⑤ `go build/vet/test ./... -count=1` 绿。
- **⑥ 边界**：只做错误映射与回执表生命周期。网关的通道实现（XTP）一概不碰。

### K4 · core `/report` 结论态放行（R1 读面语义）

- **① 目标**：调用方能区分「不存在」「没跑完」「跑失败」，不再三者同一个 404。
- **② 落点（实测行）**：`cmd/analysis/handlers_backtest.go` 的 `:192`、`:233`、`:266`、`:383` 四处 `"backtest not found or not completed"`（**四**处，不是两处）；`docs/openapi.yaml` 的 `/api/backtest/{id}/report` 响应码；`cmd/analysis/handlers_openapi.go` 若有静态描述同步。
- **③ 要求**：① 语义：`job == nil` → 404 `run_not_found`；`Status` 为 pending/running（及任何非终态）→ **409 `run_result_incomplete`**；`failed` → **409 `run_result_failed`**；`completed`（含 `result_status = invalid` 的 K9 三值）→ 200；② 错误码名与 analytics 既有三态**同一个字符串**（analytics `internal/analysis/errors.go:11-13` 已备好），core 不得自造第二套命名；③ 四个端点（report / trades / equity / export）**同一口径**，不允许只改 report。
- **④ 约束**：**不改** `jobService.GetJob` 的返回形状；**不**改 K9 三值（`completed` / `invalid` / `failed`）的存储语义；**不**把 `invalid` 当失败；**不**新增端点。
- **⑤ 验收**：① 表驱动测试覆盖 6 种输入（nil / pending / running / failed / completed / completed+invalid），逐条断言状态码与 `code` 字符串；② 破坏验证：把 `failed` 分支改回 404 ⇒ 对应用例红；把 `running` 判成 200 ⇒ 红；③ `docs/openapi_test.go` 与 `handlers_openapi_test.go` 绿（说明文档与实现同步）；④ `gofmt -l .` 空 + `go build/vet/test ./... -count=1` 绿。
- **⑥ 边界**：只改 core 的读面语义与 openapi。analytics 的既有码不动；UI 的展示另单（K6 不含）。

> **✅ 结案（2026-11-10）**：
> - **前置**：kit 新增 `httpserver.FailCode(c, status, code, message)` 并发 `v0.3.0`（只增不改；`codeForStatus` 派生不出三态粒度），core 升 pin 到 `v0.3.0`。
> - **实现**：`cmd/analysis/handlers_backtest.go` 新增 `failRunReadSurface`，report / trades / equity / export 四处统一走它 —— nil → 404 `run_not_found`；`failed`（或 `completed` + `result_status=failed`）→ 409 `run_result_failed`；其余非 `completed` → 409 `run_result_incomplete`；`completed`（含 `invalid`）→ 200。export 用 `errRunReadSurface` 提前返回，修掉「写完 404 又渲染 200 HTML」的旧隐患。
> - **文档**：`docs/openapi.yaml` 四端点加 404/409 + 新增 `RunNotFound` / `RunResultNotReady` 响应组件。
> - **测试**：`cmd/analysis/handlers_backtest_readsurface_test.go` 表驱动 6 输入 × 4 端点 = 24 腿，逐条断言状态码与 `code`；`go test ./... -count=1` = 0（含 `docs/openapi_test.go`）。
> - **破坏验证**：failed 分支改回 404 ⇒ 4 条 failed 腿红；running 分支判成 200 ⇒ 4 条 running 腿红；两次均按备份还原、`sha256` 一致、复跑绿。
> - **消费方**：`tellurion-analytics` 同批按契约 code 映射（`internal/upstream.ResultReader`）：409 两码 → `IncompleteError` / `FailedError`，未知 409 fail-closed 成 `dependency_unavailable`；3 条新用例 + 破坏验证见该仓 `docs/TASKS.md` QT-43 施工记录。

### K5 · core 部署路径两个真 bug

- **① 目标**：`docker compose up -d` 不再因为构建一个**已删除的目录**而失败；`check_deploy_consistency.py` 不再对 `BindEnv` 别名报假阳性。
- **② 落点（实测行）**：`Dockerfile.service:26`（`go build -o /app/strategy-service ./cmd/strategy`）；`cmd/` 实测只有 `analysis` / `data` / `dualrun` / `worker`；`tools/check_deploy_consistency.py:203-207`（`_GO_KEY_CALL` 正则只捕获第一个字符串参数）；读点证据 `pkg/bootstrap/config.go:25`（`v.BindEnv("auth.jwt_secret", "JWT_SECRET", "AUTH_JWT_SECRET")`）。
- **③ 要求**：① 删掉 `Dockerfile.service` 里 `cmd/strategy` 那一行（**不改**另外两行的产物名与镜像结构）；② `check_deploy_consistency.py` 解析 `BindEnv` 的**全部**字符串参数（含 `Get*` 与 `os.Getenv` 的既有形态不变）；③ 若 CI 有该步骤，保持其位置不变。
- **④ 约束**：**不**把六个服务仓纳入 compose（那是 §5-C 的裁决，不在本单）；**不**改 compose 的服务名与端口；**不**动 `deploy/k8s/` 的任何清单；**不**新写 Dockerfile。
- **⑤ 验收**：① `docker build -f Dockerfile.service .` 退出 0（有 Docker 的环境；无 Docker 时以「`go build ./cmd/analysis ./cmd/data` 均退出 0」代替并如实注明）；② `python tools/check_deploy_consistency.py` 退出 **0**；③ **破坏验证**：把 `BindEnv` 的别名参数删到只剩第一个 ⇒ 该脚本必须重新退出 1（证明它不再假阳性，是真的在读）；逐字节还原后退出 0；④ compose 配置语法校验（`docker compose config -q`，有 Docker 时）。
  **本轮实测基线（2026-10-26，可复跑）**：修前 `python tools/check_deploy_consistency.py` 退出 **1**，报恰好两条 —— `analysis-deployment.yaml` / `data-deployment.yaml` 注入的 `JWT_SECRET`「全仓没有任何读取点」。**已修（2026-10-26，裁决 #10）**：新增 `_GO_BINDENV_CALL` 收别名实参 ⇒ 复跑 **exit 0**；破坏验证（删 `config.go:25` 的别名）复现两条、exit 1，还原后 exit 0。另 #5 已删 `risk-` / `analytics-deployment.yaml`，k8s 清单 10 → 8 份。
- **⑥ 边界**：只修这两处。部署形态该不该纳入六服务，留给裁决。

### K6 · ui 三个组件的 `data-testid` 窄例外

- **① 目标**：让 e2e 有三处**稳定的语义锚**，不再靠类名/文案对齐。
- **② 落点（⑳ 已裁定的三个组件）**：`web/src/**/NavTiles.vue`、`MarketMetrics.vue`、`AppSidebar.vue`（以 UI 台账 `docs/TASKS.md` 里登记的路径为准）。
- **③ 要求**：**只加** `data-testid` 属性；命名用 `组件名-用途` 的短横线形式，同一个可交互元素只加一个；**单独一个 commit**（不与 e2e 对齐混提）。
- **④ 约束**：**不改** 类名 / DOM 结构 / 文案 / 行为 / CSS；**不动**其他组件；**不**趁机删 `test.fixme`（那属 AUD-61 阶段二，要有真栈才能验证）；**不**改 `web/src` 之外的任何文件。
- **⑤ 验收**：① `npm run typecheck` 与 `npm run lint` 退出 0；② `npm run test`（vitest）绿；③ `git diff` 逐行只有新增属性；④ 破坏验证：随便改一个类名 ⇒ 若有依赖该类的既有测试必须红（证明这不是「改了什么都不红」的空动作），还原后绿。
- **⑥ 边界**：只做属性增量。e2e 真栈与非 testid 的 13 条漂移都不在本单。

### K7 · orchestration `run_artifacts` 写端点 + R5 鉴权

- **① 目标**：让 `run_artifacts` 有**生产者入口**——worker 不再只能靠别人代写库；同时给该面（以及 worker 面）一个可验的身份。
- **② 落点（实测）**：`internal/httpapi/api.go`（`WithArtifacts` 已装配**读**面，`:98` 注册 provenance 路由）、`internal/httpapi/provenance.go`、`internal/queue`（`ArtifactStore.SaveArtifact` 已有两个实现，今天**只被测试与编译期断言消费**）、`internal/pgstore/artifacts.go`、`go.mod`（今天只有 pgx）。
- **③ 要求**：① 引 `tellurion-kit v0.1.4`（**只引 `auth` + `auth/httpadapter`**）与 `tellurion-contracts`（若需要 DTO；不需要就不引）；② 新增 `POST /api/runs/{id}/artifacts`，接受三 hash（`run_spec_hash` / `dataset_snapshot_hash` / `artifact_hash`）+ uri + 可选 seed/version_hash，**幂等**（同 run 同身份 → 200且不重写；身份漂移 → 409，禁止静默覆盖）；③ policy 表：artifact 写端点只开 `service` 角色；未装配 Authorizer 时 **503 拒绝**（沿用 risk 仓的 fail-closed 口径）；④ 明确 `producer_pending` **不是**第四种结论态（DR⑬：该提示位归本仓，core 不得引入），如需对外表达，只作为**响应提示字段**而非 `result_status` 取值。
- **④ 约束**：**不**允许 core 直写本仓的 `run_artifacts` 表（DDL owner 唯一）；**不**改 `run.backtest_jobs` 的状态语义；**不**改 jobs 4 端点的档位（裁-15「jobs 面暂不做鉴权」仍有效，本单只覆盖 artifact 面 + worker 面）；**不**动 `internal/queue` 的 port 形状（只在 HTTP 层加写入口）。
- **⑤ 验收**：① 新端点三态错误（404 未知 run / 409 身份漂移 / 503 未装配鉴权）各一条用例；② 幂等腿：同请求两次 → 库内一行；③ 鉴权腿：无 token → 401、非 service 角色 → 403、service → 200（httptest 层自证，**不是**真进程）；④ `gofmt -l .` 空 + `go build/vet/test ./... -count=1` 绿；⑤ 破坏验证：把 artfifact 写端点的角色检查去掉 ⇒ 403 用例必须红。
- **⑥ 边界**：只到「本仓有写入口 + 有身份」。core 侧 worker 回传三 hash 是下一单（跨仓），本单不做；部署清单与 token 配发仍属 core QT-71。

### K8 · strategy promote 闸门：鉴权 + 审批人留痕

- **① 目标**：AI 产物进生产回测的闸门**可验身份、可追责**——补上今天缺的「谁批的」。
- **② 落点（实测）**：`go.mod`（**零 kit 依赖**，12 条路由全裸）；`internal/httpapi/api.go`（路由表 `:42-56`；`POST /api/strategies/{id}/versions/{hash}/promote` 在 `:46`）；`internal/asset/service.go:466`（`Promote` 状态机 `StatusValidated → StatusPromoted`）；`internal/asset/pgstore.go:393`（`Promote(...)` 的 `log` 参数在调用处硬编码 `"promoted"`）。
- **③ 要求**：① 引 `tellurion-kit v0.1.4`（`auth` + `auth/httpadapter`）；② **只对三条端点**（promote 与两条 internal/内部写面，清单以本仓台账 §9 为准，**不得顺手扩到读面**）要求 `service` 角色 + 调用方身份；未装配 Authorizer → 503；③ 审批人**不自报**：从 core 网关转发的 `X-Actor` 读取（⑫ 面 B1），本仓不新增用户态登录；`X-Actor` 缺失 → 400 或 401（**二选一并写进 API 文档**，不许空串落库）；④ 审批落库留三件套：**审批人 / 时间 / 结论**（追加式，`compile_log` 或新表，二选一并写进 `db-schema.md`）；⑤ 端点默认**只监听回环/集群内地址**（⑫ 原文），不得默认 `0.0.0.0`。
- **④ 约束**：**不**在 core 侧新增 promote 客户端（core 走网关 + `X-Actor`，转发在本单之外）；**不**放宽状态机（`Validated → Promoted` 不变）；**不**改 `artifact_hash` / `version_hash` 的算法；**不**实现用户态 RBAC。
- **⑤ 验收**：① 鉴权三态用例（无 token 401 / 非 service 403 / service 且带 `X-Actor` 200）；② 审批人留痕断言：promote 后 `compile_log`（或新表）里有非空审批人 + 时间 + 结论，**且**「不带 `X-Actor`」的请求不产生任何行；③ 破坏验证：把 `X-Actor` 读取改成硬编码常量 ⇒ 留痕用例红；把角色检查去掉 ⇒ 403 用例红；④ `gofmt -l .` 空 + `go build/vet/test ./... -count=1` 绿；⑤ 监听默认值有测试或启动期断言。
- **⑥ 边界**：只到「闸门可验身份 + 可追责」。core 的执行侧拒绝腿是 K11；网关的转发实现是 core 的另一单。

### K9 · contracts 新建 `risk` 包（②）

- **① 目标**：把规则的 **wire 形状 + 冻结枚举 + hash 纯函数**变成跨仓契约，同时保证**历史 `risk_rules_hash` 逐字节不变**。
- **② 落点**：新建 `tellurion-contracts/risk/`（包名 `risk`）；迁入 `Snapshot` / `SnapshotRule`、`Scope` / `RuleType` 及冻结词表、`DomainSnapshotHash` / `MaxParamsBytes`、hash 纯函数（做成 `Snapshot.Hash()`，与 `engine/provenance.go:78` 的 `StrategyVersionInput.Hash()` 同形）、4 个哨兵（`ErrSnapshotMismatch` / `ErrInvalidRuleType` / `ErrInvalidScope` / `ErrInvalidParams`）；`docs/API.md` §4 登记区同步；`tools/check_api_surface` 的 `api-surface.txt` 更新。
- **③ 要求**：① **逐字节搬运** risk 的现有编码（`internal/rule/snapshot.go:57-100` 的手写长度前缀、`writeField`、按 `rule_id` 排序、`strconv` 形态、`canonicalParams`），**禁止**换成 contracts 的 `NewCanonical`；② 固定向量测试（输入 → 期望 hex 硬编码）；③ 与 risk 仓的**对拍腿**：同一组 `SnapshotRule` 两侧各算一次，断言 hex 相同（照抄 `provenance_vector_test.go` 形态）；④ risk 侧改为**别名/转发**，调用点零改；`Bundle` / `ParseBundle` / `ForScope` **不迁**；⑤ 发 MINOR（建议 `v0.7.0`）。
- **④ 约束**：**不**改编码算法（改 = 全部历史 hash 作废，要 ADR 级裁决）；**不**迁 `Bundle`（有未导出索引字段）与 `ParseBundle`（decode + 默认值填充，属策略）；**不**把 `risk` 并入 `engine` 包；**不**在 contracts 里读时钟 / 连库 / 读环境变量；**不**动 risk 的 Store / HTTP。
- **⑤ 验收**：① 固定向量测试绿；② 对拍腿绿（两侧 hex 相同）；③ `go test ./... -count=1` 绿 + `tools/check_api_surface` 绿；④ 破坏验证：把编码里的排序去掉 ⇒ 向量测试必须红；把某个字段的长度前缀改成明文分隔 ⇒ 对拍腿必须红；⑤ risk 仓在**不升 pin**的情况下切到别名后仍全绿（这一条在 risk 侧验收，跨仓时由 K9 交付说明给出步骤）。
- **⑥ 边界**：只做「契约落形状 + 对拍」。core 侧消费（`bundle → RiskPolicy`）与 risk 的 Store/HTTP 都不在本单。

### K10 · core QT-51 分支收口（环境前置已解除；剩 rebase + 复跑门禁 + 重跑等价性报告）

- **① 目标**：把 `_ ExecutionPort` 的拆 port 形态合入 main，使 `pkg/backtest` 对 `pkg/live` 的生产 import 归零。
- **② 落点（2026-10-26 再复测）**：分支 `task/qt51-execution-port`（tip `e015a83`，**branch-only 1 / main-only 10**（本批落账后现跑；这两个数每推一笔 main 就变，见 §6 第 3 行），**不在 origin**——`git ls-remote origin refs/heads/task/qt51*` 0 命中），**24 个文件**；main 侧今天仍有 **6 个文件** import `pkg/live`（生产 3 / 测试 3，实测 `git grep -l` 六条路径）。裁决在 `cross-repo-decision-record-2026-10-25.md` §五 ①。
- **③ 要求**：① `git worktree add` 从该分支拉一个**新** worktree（旧 `tellurion-qt51` 目录已不存在，**不要**复用路径）；② rebase 到 main——**冲突面实测（同日订正，原写「解 `engine.go` / `aliases.go` / 文档三处」不成立）**：merge-base `5b9dc11`，两侧改动交集 = **3 个文件且全是文档**（`docs/TASKS.md` / `docs/ARCHITECTURE.md` / `docs/design/qt-decoupling/README.md`），**Go 侧零冲突**；`git merge-tree --write-tree main task/qt51-execution-port` 的真冲突**只有 `docs/TASKS.md` 一处**，另两处自动合并；③ 复跑 `gofmt -l .` / `go build ./...` / `go vet ./...` / `go test ./... -count=1`，并单跑 `internal/repoguard`、`pkg/backtest/execution`、`cmd/analysis/wiring` 三组——⚠️ 本机 Go 1.25.14 **可用但不在默认 Git Bash 的 PATH**（`command -v go` 空），先 `export PATH="/c/Users/ruoxi/AppData/Local/Programs/go1.25.14/bin:$PATH"` 再跑，`-race` 本机跑不了（`CGO_ENABLED=0`、无 C 编译器），这条腿留 CI；④ **等价性报告必须重跑**——分支自带的那份是旧基线上跑的，不能当合入后证据；⑤ 合入后另立小单删 5 个零调用 shim（先 grep 仍为 0）；`SetLiveTrader` 保留。
- **④ 约束**：**不**按 wave2-b 的旧方案重做（「删 shim + LiveBridge 外移」已作废）；**不**新增 `ExecutionPort` 之外的抽象；**不**改回测撮合行为（`pkg/backtest/execution` 的 `ExecutionService` 不动）；**不**在无 Go 的环境宣告完成。
- **⑤ 验收**：① 上述四条门禁的**真实退出码为 0**（贴输出，不接受自述）；② `pkg/backtest` 对 `pkg/live` 的生产 import = 0，且有 `internal/repoguard` 的护栏测试钉住；③ 同一策略回测结果与 main **逐位一致**（等价性报告重跑后的新数字）；④ `task/qt51-execution-port` 合入后可从本地与远端删除（合入即已收敛）。
- **⑥ 边界**：只做执行语义边界与合入。通道网关（QT-45）与实盘链路不碰。

### K11 · core QT-32 ⑤ 执行侧腿：未 promote 的产物被回测路径拒绝

- **① 目标**：让「人工闸门」不只存在于 agent 仓的生产者侧，在**回测路径**上也 fail-closed。
- **② 落点（待 K8 交付后定稿）**：`cmd/analysis` 的 `strategy_ref` 解析路径 + `pkg/strategy`（本仓今天持有 `strategies` 表与本地注册表）；拒绝点必须在**执行之前**（不能跑完再判）。
- **③ 要求**：① 当 `strategy_ref` 指向的资产 `status != promoted`（或审批留痕缺失）→ **4xx 且点名原因**，不静默回退到内置策略（「rejected, not ignored」）；② 内置策略与已 promote 资产行为逐字节不变；③ 与 K8 的留痕字段对齐（同一次审批同一条记录，不重造第二套审批态）。
- **④ 约束**：**不**搬 AI 编排进 core（ADR-023）；**不**新增审批端点（审批面在 strategy）；**不**放宽 `ValidateImports` / 预算 / fail-closed 任何一条；**不**改 K9 三值。
- **⑤ 验收**：① 未 promote → 4xx、已 promote → 200 两条用例；② 破坏验证：把拒绝分支改成放行 ⇒ 用例红；③ `gofmt -l .` 空 + `go build/vet/test ./... -count=1` 绿；④ 与 K8 的审批记录做一次真实链路冒烟（**需真进程**，若环境不具备则如实标「未跑」）。
- **⑥ 边界**：只做回测路径的拒绝腿。agent 侧的生成链与 strategy 的审批面都已在前序单内。

---

## §4 阶段划分

> 阶段编号只表达**依赖顺序**，不含时间估计。同一阶段内的组可以同时派给不同 agent。

### 阶段 1（可立即派，6 组并行，5 个仓）

| 组 | 仓 | 冲突面 |
|---|---|---|
| K1 | risk | 独占本仓，无并行单 |
| K2 | data | 独占本仓，无并行单 |
| K3 | broker | 独占本仓，无并行单 |
| K4 → K5 | core | **同仓串行**：先 K4（`cmd/analysis` + `docs/openapi.yaml`），后 K5（`Dockerfile.service` + `tools/`）。两者无文件交集，但同仓提交分开，便于回滚 |
| K6 | ui | 独占本仓（只动 3 个 vue 文件） |

### 阶段 2（三组并行，各有已满足的前置）

| 组 | 前置 | 说明 |
|---|---|---|
| K9 | **K1 完成** | 词典不冻结就不迁契约形状（顺序不可颠倒） |
| K7 | 无（kit `v0.1.4` 已发版） | 与 K1/K2/K3 无交集，可提前到阶段 1 并行 |
| K8 | 无（kit `v0.1.4` 已发版） | 与 K7 不同仓，可同时 |

> K7 / K8 其实**不依赖阶段 1 的任何产出**，只是本批把它们排在阶段 2 以便「先清低风险的单仓单」；若要提速，可并入阶段 1，**唯一代价**是同时开工的仓从 5 个变成 7 个。

### 阶段 3（core 收口，严格串行）

| 组 | 前置 | 说明 |
|---|---|---|
| K10 | ~~**有 Go 的环境**（本机无）~~ → **已解除**：Go 1.25.14 本机可用，默认 Git Bash PATH 里没有它，需显式 `export PATH="…\go1.25.14\bin:$PATH"` | rebase + 复跑门禁 + 重跑等价性报告；建议独占 worktree |
| K11 | **K10 合入 + K8 完成** | 拒绝腿落在 `pkg/backtest` / `pkg/strategy`，与 K10 同面，必须在其后 |

> **core 的四组顺序是硬的**：K4 → K5 →（K10，环境前置已解除）→ K11。任何两组同时动 core，都会让 `gofmt`/CI 的「这次红是谁造成的」失去答案。

---

## §5 待你裁决清单（本批新提出，**不代裁**）

> 来源：第四轮目的对齐审计 §六 A~F。按纪律：**未立单不得开工**，本节只给材料与建议，不是任务。
>
> **✅ 已全部裁决（2026-10-26，主任口径「全部按建议」）**：A / C 由 ADR-036 处置；B / D / E 按建议执行；F 的剩余项并入 contracts 发版批次。正文见 `cross-repo-decision-record-2026-10-26.md` §一 / §二。下表保留当时的建议原文，不删历史。

| # | 事项 | 为什么必须你拍 | 我的建议 | 代价 |
|---|---|---|---|---|
| **A** | X 线既定事实怎么处理：补 ADR 承认 / 撤回进程与清单 / 维持但冻结 | 这是方向改判；六仓已有 `cmd/*service` 与 risk/analytics 两份 k8s 清单，但只有 data 一条链真接线 | **维持但冻结 + 补一条窄 ADR**：ADR 只承认「data 读链已在跑」，明写「进程存在 ≠ 已接入」；撤回 `analytics` 的 k8s 清单与 `risk`/`analytics` 的非回环监听默认值；**不投人做接线**，直到 S1 出口 | 撤回清单会让「随时可拆」这句话少一半证据；补 ADR 承认既定事实等于承认跳了阶梯 |
| **B** | 双 owner 表清单与逐表四步计划 | 「哪些表允许暂时双写」是资源分配，不是实现细节 | **只做 market 簇**（⑰ 已裁、K2 已在做），**其余簇先出只读清单**（owner / 当前写者 / 读方 / 对拍方式 / 停写条件五列），不做全量四步计划 | 清单本身不产生解耦；但比「以为只有一个 owner」安全 |
| **C** | 部署路径归谁：compose 只跑 core 两进程，还是纳入六服务 | 影响运维与「12 仓」的实际含义 | **compose 只跑 core 两进程**（K5 的最小修复即此），六服务入 compose 等 A 裁决 | `data-service` 这个名字在 compose 里仍指 core 的 `cmd/data`，与「拆仓」叙事不一致——需在 deploy 文档写明 |
| **D** | 产品 S1 出口与工程 X 线的排序 | 资源只有一份；S1 出口「无人干预连续跑完 100 次实验」未达 | **暂停 X 线新增动作**，只允许收口**已裁**的项（即本批 K1~K11）；新单等 S1 出口 | 已知的 5 簇双 owner 会继续存在；但本批已覆盖其中 market 簇 |
| **E** | 旧仓 `quant-trading` / `quant-trading-agent` archive | 不可逆动作 | **不在本批**：先打 `archive/*` tag，等 S1 出口后再删 | 旧仓残留会让「12 仓」的清点长期多两项噪声 |

**外部条件（2026-10-26 已解除）**

- **K10**：~~一台有 Go 的机器~~ → **已解除**：Go 1.25.14 在本机，PATH 已修；`go build ./...` 已跑通。剩余是 `vet` / `test` 与 rebase，见记录 #7。
- **AUD-61 ①**：~~Docker daemon 不可达~~ → **已解除**：Docker Engine 29.8.0 可达；:5173 / :8081 / :8085 实测空闲。剩 ui e2e 本身未跑。
- **K9 发版**：仍等 contracts `risk` 包落 main；之后的 `v0.7.0` 与升 pin 见记录 #6。

**已闭环、从待裁清单里划掉的**

- ~~F · kit `v0.1.4` 发版与三仓升 pin~~ → **已发生**：`v0.1.4` tag 已打并 push；risk / data / analytics 的 `go.mod` 实测均为 `v0.1.4`。

---

## §6 账实不符（本轮实测 vs 台账/审计原文）

> 规矩同前：**原文照留，追加订正块**，不删历史。

| # | 账上/文档里写的 | 实测 | 处置 |
|---|---|---|---|
| 1 | kit `docs/TASKS.md:182` 曾写 KT-11「🔶 进行中（**待 CI 门禁与发版**）」，施工序 S2.7 也把「打 `v0.1.4`」列为待办 | **本轮实测时已被并发施工方结案**：`f2c5605 docs(tasks): KT-11 结案 —— v0.1.4 已发版、三仓已升 pin`，当前该行已是 `✅ 完成（2026-10-25，已发 v0.1.4）`；`git tag` 有 `v0.1.4`；三仓 `go.mod` 已 `v0.1.4` | **无需处置**（已闭合）。留此行为证：本批取证时点早于该结案，避免把「已修」再修一遍 |
| 2 | core `docs/TASKS.md` QT-51 段：「分支自述已裁决并落地，但 **main 的裁决记录没有这条裁决** ⇒ 合入前需主任确认」 | `cross-repo-decision-record-2026-10-25.md:371` **已有** ① 「QT-51 拆 port …以分支为基线收口」 | **已修（本篇写入时同步）**：该段追加 2026-10-26 订正块，标题「待复取 + 裁决」改为「裁决已下，待复取」 |
| 3 | core QT-51 标题「实现已在未合入分支」+ `:1226`/`:1275`/`:1425`（行号本身也在漂） | 分支仍在（`task/qt51-execution-port`），branch-only 恒 **1**（`e015a83`）；main-only 同一天先后读到 **12 → 3 → 7 → 10**（每次都写「刚刚的实测」，每次都在下一笔提交后过期） | **别记数，记命令**：`git rev-list --left-right --count main...task/qt51-execution-port`（输出 `main-only / branch-only`）。本批落账后 = `10 1`，main `0617f54`；**其中 7→10 正是本批三笔文档提交推的**。稳定的只有两条：「branch-only = 1」与「不在 origin（`ls-remote` 0 命中）」；`docs/TASKS.md` 的 QT-51 段行号同理，引用前 `grep -n '^### QT-51'` |
| 4 | 第四轮审计 §一：「除 data 读链外**没有一条**真正进入生产回路」 | core **有** `POST /api/risk/resolve` 调用点（`cmd/analysis/wiring/risk_rules.go:45`），但开关 `risk_service.resolve_enabled: **false**`（`config/analysis-service.yaml:122`）、`events_enabled: false`（`:143`） | 两句话**并不冲突**，但必须写清口径：**「代码已接线、部署默认关」** ≠ 「没有调用方」。建议在审计 §一 补半句，否则下一次会被读成「risk 生产者不存在」 |
| 5 | orchestration `AGENTS.md` §4：「kit 未引入（**远端最新 `v0.1.3`**）」 | kit 远端已有 `v0.1.4`（tag 已 push） | **已修（2026-10-26）**：`tellurion-orchestration:AGENTS.md:57` 与 `tellurion-broker:AGENTS.md:53` 已改为 `v0.1.4`；两仓仍未引入，这一半不变 |
| 6 | data `docs/db-schema.md:25` 把 `market.index_daily` 列为本仓表 | 该表 owner 已裁为 core（⑰）；core 侧写点在 `tellurion:pkg/storage/index_daily.go:31`、定义在 core Migration 057 | K2 同时改 `:25` 与 `:151`；**:79 的「core 仍是写者」一句是对的，保留** |
| 7 | core QT-32 段：「本单 ⑤ 在『审批人从哪来』被裁之前**不可施工**」 | ⑫ 已裁（面 B1：core 网关 + `X-Actor`；strategy 三端点 service-token）；且 kit `v0.1.4` 已使 service token 可用 | **已修（本篇写入时同步）**：该段追加 2026-10-26 订正块——前提解除，⑤ 拆为 K8（strategy 侧）+ K11（core 执行侧）；`⬜` 状态不变 |
| 8 | 第四轮审计 P2-2：「core 工作区有 **3 份未提交**审计文档改动」 | 实测 **6 个已修改 + 1 个未跟踪**（新增本轮审计文件本身 + `AGENTS.md` / `docs/TASKS.md` / `README.md`） | **已整体提交（2026-10-26）**：`4f44dd6`（AGENTS / README / 审计 / wave3）+ `49cfdd5`（B 组材料）+ 本笔（裁决记录 + 五份订正）；工作树干净 |
| 9 | 第四轮审计 §三 P0-2 表中「编排：core 仍写 `backtest_jobs`；编排写 `run.backtest_jobs`」 | 与 orchestration 台账一致（X5b/X5c 已落，O3 未做），**无冲突** | 无需处置；列出以证明本轮逐条核对过、不是照抄 |
| 10 | core `docs/TASKS.md` 标题行「QT-32 ⬜」 | agent 仓 `QT-32-A` 已 ✅（范围 ①~⑥），但 **core 主单 ⑤ 的执行侧腿未做**、且 K8 未做 | `⬜` **正确**，不改；§5-D 的排序裁决会决定它何时开工 |

---

## 附录：本批派发前的一次性检查（给协调者）

1. **确认没有别人在做同一件事**：`git log --oneline -5` 在目标仓里若有同一单的施工提交，先读再派（本批写于 2026-10-26，快照见 §0.1）。
2. **确认工作区干净**：core 今天有 6 改 + 1 新增未提交；派 K4/K5/K10/K11 前先让那批文档落地提交，否则冲突会混在一起。
3. **确认分支基点**：K10 必须在 `task/qt51-execution-port` 的 tip 上拉 worktree（2026-10-26 复测 tip = `e015a83`；旧记 `7c89aae` 已过期），不要从 main 重新写一遍。
4. **每单交付必须附**：真实门禁退出码 + 破坏验证（改坏 → 红 → 逐字节还原 → 绿）+ 改动的 `repo:path:line` 清单。**不接受自述。**
