---
status: active
last-verified: 2026-10-25
verified-by: 第三轮全量复核（12 仓 git 快照 + 6 份台账 + 关键代码抽查；只读取证，未跑 Go / 未起服务 / 未写库）
---

# 跨仓施工审计 · 第三轮（2026-10-25 复核）

> 目的：在第二轮 16 条裁决全部落定后，重新做一次首次任务（分组审计）。回答三件事：**现在到哪了 / 账实是否一致 / 下一步先做什么**。
> 六段格式沿用首次任务：总览表 · 分组理由 · 派发块 · 阶段 A/B · 裁决状态 · 账实不符。
> 裁决正文：[裁决记录 §五](cross-repo-decision-record-2026-10-25.md)；建议依据：[c](pending-decisions-recommendations-2026-10-25c.md)。

---

## 一、总览表（组 | 仓 | 组内串行顺序 | 前置 | 可与谁并行）

| 组 | 仓 | 组内串行顺序 | 前置 | 可与谁并行 | 状态 |
|---|---|---|---|---|---|
| **G0 环境闸门** | 本机 / CI | 装 Go 1.25（或推分支跑 CI）→ 所有代码验收 | 无 | 与所有「只写不验」的工作并行 | ⛔ 本机 `go` 不在 PATH、Docker daemon 不可达 |
| **G1 kit 发版** | kit → risk/data/analytics（→ orchestration/agent） | 提交 4 文件 → `gofmt/build/vet/test` → `v0.1.4`（矩阵+CHANGELOG+tag）→ 三仓升 pin | G0 | G2 / G3 / G4a / G6 / G8 的文档部分 | 🔶 代码已改、未提交未验证 |
| **G2 风控链** | risk → contracts → core | `QT-74` 词典 → `contracts/risk` 包（`v0.7.0`）→ core 消费（`bundle` → `RiskPolicy`） | G0；三步严格串行 | G1 / G3 / G4a / G6 | ⛔ 卡在 QT-74（⬜ 未开工） |
| **G3 引擎语义** | core | QT-51 分支 rebase → 门禁 → 合入 → 删零调用 shim（另单） | G0 | G2 / G4a / G6 / G8 | 🔶 分支 `1 ahead / 11 behind`，未合入 |
| **G4 数据面** | data → core → 运维 | G4a data 撤认领 `index_daily`（可先做）→ G4b core 停 3 张 A 集写 → G4c 物理 `SET SCHEMA` → G4d 运维回填 | G4b 需 data 先有 3 张表的写端点；G4d 是运维动作 | G4a 与 G2/G3/G6 并行；G4b 之后才 G4c | 🔶 DDL/写面/两条作业已落；物理搬迁与生产回填未做 |
| **G5 编排 X0** | orchestration → core | R5 鉴权（kit）→ artifact 写端点 → core worker 回传三 hash | G1（service token 可用） | G7 可并行（都等 G1） | 🔶 队列/父作业/worker 面已落；X0 生产者与 R5 未做 |
| **G6 读面语义** | core（+ ui/openapi） | `/report` 结论态放行 → openapi → UI 错误处理 → e2e | G0 | G1 / G2 / G3 / G4a | 🔶 三态线在 analytics 已备好，core 未发码 |
| **G7 策略闸门** | core → strategy | core 网关/路由/`X-Actor` → strategy 三端点接 kit 校验 → 审计落库 | G1 | G5 | 🔶 promote 端点已有，但零鉴权、零调用方 |
| **G8 UI / docs / broker** | docs / ui / broker | DOC-10（无依赖）· AUD-61 无栈子集 → 真栈 · broker R10 → 保留期 | docs 无；ui 要 Docker；broker 要 Go | 三者互不依赖 | 🔶 DOC-10 ⬜；UI 16 条 fixme；R10 未修 |
| **G9 清仓** | core worktrees | 确认 g3 文档已落 → 删 g3 / a7-core / `b63-verify` | 主任授权（不可逆） | 任意时机 | ⛔ 待授权 |

### 1.1 各工作流的「已落 / 剩余」细账

| 工作流 | 已落（有代码/文档证据） | 剩余（未落，不写成已完成） |
|---|---|---|
| 契约底座 | contracts `v0.6.0`（远端 tag 已推）、kit `v0.1.3`（远端 tag 已推）；core/data/agent 钉 contracts `v0.6.0` | kit `v0.1.4`（⑬）；contracts `risk` 包（②，等 QT-74）；strategy 仍钉 `v0.5.0`、agent 仍钉 kit `v0.1.0`（非错，但矩阵要登记） |
| core 引擎/内核 | QT-63 provenance、QT-71 service 角色 + 10 份 k8s 清单、QT-72 provenance 读面、QT-73 五包、QT-42 outbox、QT-40 H4 ①、`cmd/worker` | QT-51 合入；⑲ `/report` 结论态；⑰ `index_daily` 注释与 data 撤认领；G7 网关 |
| 数据面 X1 | 23 张表 DDL、A 集三集合、`visible_at` 回填套件（仅测试库）、`POST /ingest/points`、`assembly.Writer`、两条作业改投 data | 9 张表生产回填（已授权，运维执行）；3 张 A 集表停 core 写；物理 `SET SCHEMA`；`index_daily` 两套 DDL；`ingest.raw` 仍 core 写 |
| 风控 X3 | 规则 4 端点、快照 3 端点、事件/熔断/监控 3 端点、鉴权策略表、`risk_rules_hash` 生产者（core QT-66） | **QT-74 词典 ⬜**；`bundle` → `ParseBundle` → `RiskPolicy` 未接；② 迁 contracts 未做 |
| 分析 X4 | 8 端点、鉴权、R1 持久读面换面、R2 owner、出站 token 客户端、sensitivity 回填 | ⑲ core 读面三态；⑬ kit 升 pin；真 core 进程 invalid 行 E2E 未跑 |
| 编排 X5 | 队列/注册/父作业 2 端点/子 run 入队/worker 8 路由/`run_artifacts` 读面/core worker | X0 生产者（⑱）；R5 鉴权；`producer_pending` 提示位 |
| 策略 X2 | 资产服务 10+2 端点、TinyGo 编译链、promote 状态机、缓存失效 | promote 审批留痕与鉴权（⑫ B1） |
| 交易 X6 | 四 port、Mock、对账纯函数、`broker.order_acks`、申报数量纯函数、X6c 决策材料 | X6c 全量适配器 ⛔（P1~P5）；R10 修复；保留期清理作业；R9 待 X6c |
| AI | QT-32-A（生成链留痕 + promote 闸门）✅ | ⑩ 载体默认 `dsl` 的落地（agent 后续单） |
| UI | 重构已并入、套件 186 条中 16 条 fixme | ① 批 16 条；⑳ testid 窄例外；视觉基线重生成 |
| 文档 | DOC-06 ✅、DOC-04 链接检查 ✅、DOC-07 巡检 ✅ | DOC-10（§7 整块订正 + README:40） |

---

## 二、分组理由（为什么这样串/并）

1. **G1 是所有跨仓鉴权的前置**：risk/data/analytics 都钉 kit `v0.1.1`，service token 必然 401；G5（orchestration service 角色）和 G7（strategy 三端点校验）都要等 `v0.1.4`。G1 内部必须串行：提交 → 门禁 → tag → 三个消费仓升 pin，不能先发 tag 后补测试。
2. **G2 是唯一的长链**：`QT-74`（词典）→ contracts `risk` 包 → core 消费，三步不可颠倒。词典未冻结就迁契约 = 把「猜字段名/单位」搬进契约层；先迁后补词典会让 core 消费代码按错误形状写死。
3. **G3 与 G2 零文件交集**：QT-51 只动 `pkg/backtest` / `cmd/analysis/wiring` / repoguard；G2 动 risk/contracts/core 的 risk 消费面。可并行。
4. **G4a 与 G4b/c 必须拆开**：`index_daily` 的两套 DDL 是「先建表者定形」的结构冲突，**与第二写者无关**，可以立刻单独修；物理搬迁必须等 3 张 A 集表停写，否则 core 的裸表名写点会落到 `public` 的新同名表上，造出两份真相。
5. **G5/G7 都等 G1，但彼此无交集**：orchestration 的 worker 面与 strategy 的 promote 面互不引用，可并行；两者都要先有 service token 的验证方。
6. **G6 是单点小改**：`/report` 一处判据 + openapi/UI/e2e 同步；不依赖任何新契约，可与 G2/G3 并行。
7. **G8 三条互不依赖**：DOC-10 是纯文档；UI 只差运行环境；broker R10 是两处错误映射。放在同一组只因为都不进主链。

---

## 三、派发块（下一批可直接开工的 5 张）

### D1 · kit `v0.1.4` 收尾（G1，⑬）

**目标**：service token 不再被 `UserIDInt64()` 拒成 401，且用户 token 的校验不放松。

**动作**
1. 在**有 Go 的环境**：`cd tellurion-kit && gofmt -l . && go build ./... && go vet ./... && go test ./auth/... -count=1`。
2. 通过后提交工作区 4 个文件（`auth/claims.go` / `auth/httpadapter/httpadapter.go` / `auth/httpadapter/httpadapter_test.go` / `auth/middleware.go`）。
3. 发版三件套：README 兼容矩阵加 `v0.1.4` 行 + `CHANGELOG.md` 一节 + `git tag -a v0.1.4`；`go run ./tools/check_release_matrix`。
4. risk / data / analytics 各 `go get github.com/ruoxizhnya/tellurion-kit@v0.1.4 && go mod tidy`；核 `check_contract_pin`。

**验收**：kit 全绿 + tag 在 main 祖先上；三仓 `go build ./...` 绿；一条「`IssueServiceToken("service:risk",…)` 签出的 token 能被 `httpadapter.Authenticate` 接受」的用例在 kit 里真实通过。

**禁止**：未跑门禁就提交/打 tag；改用户 token 的 int64 校验；顺手改 `auth` 的其他行为。

---

### D2 · risk `QT-74` 参数词典（G2 第一步）

**目标**：四类规则的 `params` 字段名/类型/单位/取值域/必填性**唯一定义**，core 可逐字段映射而不猜。

**上下文**：台账 `tellurion-risk:docs/TASKS.md` QT-74 已把 14 项词典表写好；`SnapshotRule.Params` 是 `json.RawMessage`，写侧只校验「非空 JSON 对象、≤64 KiB」（`internal/rule/model.go:150`），不看键名。

**动作**
1. 词典进 `docs/db-schema.md`（`rule_snapshots.rules[].params`）+ `docs/api-schema.md`（create/update 输入口径），两处同名同序。
2. 写侧按 `type` 严格校验键集合/单位/取值；未知键、缺必填、`NaN/Inf`、`fast>=slow` → `ErrInvalidParams` / 400。
3. **读侧不重校键集合**：`ParseBundle` / `SnapshotHash` 不因词典新增键而拒绝历史快照（否则历史 hash 集体不可读）。
4. 夹具改小数键名：`internal/rule/snapshot_test.go:144` 的 `{"max_pct":10}` → `{"max_position_weight":0.10}`。
5. `risk.limits` 二选一：冻结 `limit_key` 并给词典引用，或在 `db-schema.md` §1 明确「遗留空表、无生产者」。
6. 变更规则成文：加字段=MINOR；**改单位=breaking + 升域标签 v2**。
7. `scope` 消费口径写进 `docs/dataflow.md`：一期 core 只消费 `global`，其余三档可见但不参与裁决。

**验收**：两处文档逐字段可查、无「待定」；写侧非法键/单位用例全红后转绿；历史快照读回用例不因新词典变红；夹具已改。

**边界**：只在本仓落词典；**不迁 contracts、不改 core**（那是下一张）。

---

### D3 · QT-51 收口（G3）

**目标**：`pkg/backtest` 与 `pkg/live` 解耦，回测行为逐位不变。

**动作**
1. `git rebase main` 分支 `task/qt51-execution-port`（当前 1 ahead / 11 behind）；解 `engine.go` / `aliases.go` / 文档冲突。
2. 有 Go 的环境复跑：`gofmt -l .` / `go build ./...` / `go vet ./...` / `go test ./... -count=1`；单跑 `internal/repoguard`、`pkg/backtest/execution`、`cmd/analysis/wiring`。
3. 在裁决记录 §五 ① 写「已合入」+ 日期；`backtest-engine-review.md:145` 勾选。
4. 另立小单：删 5 个零调用 shim（先 grep 确认仍为 0）。

**验收**：护栏测试证明 `pkg/backtest` 零 import `pkg/live`；等价性报告在**合并后的 HEAD** 重跑；`cmd/analysis` 装配路径仍能注入 trader。

**禁止**：把分支自带的旧基线等价性报告当合并证据；为合入顺手改回测数值逻辑。

---

### D4 · data 撤认领 `market.index_daily`（G4a，⑰）

**目标**：一张表只有一个 DDL 定义（core Migration 057）。

**动作**
1. 删 `tellurion-data:internal/schema/market.go:301` 的建表与索引（`:317`）。
2. 删 `internal/schema/schema.go:47` 的 `"market.index_daily"`。
3. 删 `internal/schema/visible_at.go:54` 的 C 集豁免条目。
4. 改 `docs/db-schema.md:25` / `:151`：owner = core（DATA-12），本仓不认领、不迁、不回填。
5. 加守卫：data 的 `internal/schema` 源码不得再出现 `CREATE TABLE.*market.index_daily`；core 侧在 `pkg/storage/postgres.go:1160` 注释写明「data 已撤认领，本处唯一 DDL」。
6. 同批改测试：`internal/schema/schema_test.go:69` 的 exempt 断言、`visible_at` C 集条目的测试预期、`TableNames()` 计数（23 → 22）。

**验收**：`go build/vet/test` 绿；守卫破坏验证（把 DDL 加回 ⇒ 红 → 还原 ⇒ 绿）；data 文档不再声称拥有该表。

**代价**：data 部署不再建该表 ⇒ 依赖 core 先迁移；data 仓对该表零读写，故无运行时影响。

---

### D5 · core `/report` 结论态放行（G6，⑲）

**目标**：调用方能区分「不存在 / 没跑完 / 跑失败 / 跑完无效」。

**动作**
1. `cmd/analysis/handlers_backtest.go:191` 的 `job == nil || job.Status != "completed"` 拆成：
   - `job == nil` → 404 `run_not_found`；
   - `job.Status` ∈ queued/running/pending → 409 `run_result_incomplete`；
   - `job.Status == failed` → 409 `run_result_failed`；
   - `completed` → 走现有 `result_status` 三值。
2. openapi `/api/backtest/{id}/report` 补 409 两种 code；UI `web/src/api/backtest.ts` 的 `/report` 错误处理补 409；e2e 补两条（running → 409、nonexistent → 404）。
3. 单测三条：pending/running 409、failed 409、不存在 404。
4. 如实登记：Migration 059 已把历史 invalid 回填成 completed，**信息不可恢复**，不反推。

**验收**：analytics 的 `ResultReader` 能把 409 两码映射到 `ErrRunIncomplete` / `FailedError`（已有线），不再把失败读成 not_found。

**禁止**：把 404 改成 200 兜底；解析错误串。

---

## 四、阶段 A / 阶段 B

### 阶段 A —— 现在就能开始（无需新的外部输入）

| 动作 | 依赖 | 能走到哪 |
|---|---|---|
| **G0：装 Go 1.25**，或推一个验证分支跑 CI（`on: pull_request` 已配） | 无 | 解锁全部代码验收 |
| D2 risk QT-74 词典（写代码+文档） | G0 才能验收 | 代码可先写，验收等 G0 |
| D3 QT-51 rebase | git 可在无 Go 下做；验收等 G0 | 冲突可先解 |
| D4 data `index_daily` 撤认领 | G0 才能跑测试 | 代码+文档可先写 |
| D5 core 读面三态 | G0 才能跑测试 | 代码+openapi 可先写 |
| DOC-10 §7 整块订正 | 无 | **可完整交付**（纯文档） |
| H4 9 张表的逐表取证 + 回填语句 | 无（只读） | **可完整交付**给运维 |
| UI 无栈子集（`typecheck/lint/vitest`）+ 3 组件 testid | Node 可用 | 可完整交付 |

### 阶段 B —— 等具体产物

| 被解锁 | 等谁 | 具体产物 |
|---|---|---|
| G5 orchestration R5 + artifact 端点 | G1 | kit `v0.1.4` 已发 + orchestration 能验 service token |
| G7 strategy 三端点校验 + core 网关 | G1 | 同上；core 能签/带 service token |
| G1 三个消费仓升 pin | G1 发版 | `v0.1.4` tag + 矩阵 |
| G2 contracts `risk` 包 | D2 | QT-74 词典两处文档 + 写侧用例绿 |
| G2 core 消费 | contracts `v0.7.0` | `contracts/risk` 出现 + 对拍腿绿 |
| G4b core 停 3 张 A 集写 | data 写端点 | `POST /ingest/points` 能覆盖 `ohlcv_daily_qfq` / `sectors` / `stock_sector_map` |
| G4c 物理搬迁 | G4b + G4d | core 写点归零 + 9 张表回填完成 |
| G4d 生产回填 | 运维 | 逐表取证 + 授权（⑤ 已给 9 张） |
| UI ① 批 16 条 | Docker/CI | 5173/8081/8085 + PG/Redis 起齐 |

---

## 五、裁决状态

- **第一轮 15 条**：①~⑮ 全部已落/已裁；⑯ 已裁方向 3，待发版（见裁决记录 §一）。
- **第二轮 16 条**：①~⑳㉑㉒ 全部已裁（裁决记录 §五）；其中 ⑪ 无需改代码、㉑ 待授权、⑥⑦ 等外部条件、⑬ 待 Go 环境。
- **不再有待裁项**；剩下的都是**施工顺序**与**外部条件**。

---

## 六、账实不符（本轮新发现 / 仍未收口的）

| # | 台账或计划写的 | 实测 | 处置 |
|---|---|---|---|
| 1 | 上一版把 ②「迁 contracts」列为可立即开工 | **risk `QT-74` 仍 ⬜**（`tellurion-risk:docs/TASKS.md:443`），词典未冻结 | ② 记为 ⛔ 等 QT-74；已写进裁决记录 §五 ② |
| 2 | 各完成记录写「`go build/vet/test` 全绿」 | 本机 `go` 不在 PATH、Docker 不可达 ⇒ 本轮**一条都没复跑** | 记为「记录绿，未复核」；G0 是总闸门 |
| 3 | 未强调 core 的推送状态 | core **15 笔未推**（QT-71/72/73/74、H4、worker、alert outbox、本轮文档），CI 从未见过 | 推分支/PR 或本地装 Go 后跑；在此之前不得把本地绿当线上绿 |
| 4 | kit 工作区 4 个改动曾被记成「并发 WIP」 | 那 4 个文件**就是方向 3 的改动本身**（我方的），仍未提交 | 已订正为「代码已改、未验证、未发版」 |
| 5 | data 与 core 各自建 `market.index_daily` | **两套 DDL 形状不同**（core 有 `id`/`UNIQUE`/`VARCHAR(64)`/`CHECK`；data 是复合 PK/`VARCHAR(32)`/`volume DEFAULT 0`），先建表者定形 | ⑰ 已裁：data 撤认领；D4 |
| 6 | `SYSTEM-DESIGN.md` §7 写「六仓只有占位 doc.go」「X1~X6 未触发」；`docs/README.md:40` 写 QT-60「未启动」 | 均过期（六仓已实装；S-1 已 ✅） | DOC-10 |
| 7 | orchestration 台账「鉴权 R5 按裁-15 缓做」 | 与 §五 ⑱「先做 R5」冲突：新端点要可验写者身份 | ⑱ 对该端点**覆盖**裁-15 的「暂不做」；需在 orchestration 台账写明「R5 只覆盖 worker 面 + artifact 端点，jobs 4 端点仍按裁-15」 |
| 8 | 分支清理 | `task/a7-core` **已并入 main**（可删）；`task/g3-data` 7 ahead / 62 behind（已裁作废）；`task/qt51` 1 ahead / 11 behind；临时 worktree `b63-verify`（detached） | ㉑ 待授权；a7-core 与 b63 可同批清 |
| 9 | 版本矩阵的 pin 口径 | strategy 仍 `contracts v0.5.0`、agent 仍 `kit v0.1.0`，其余 `v0.6.0` / `v0.1.1` | 不是错，但要在各仓 AGENTS 的依赖表与 contracts/kit 兼容矩阵里写明「为什么是这个版本」 |
| 10 | analytics 台账 frontmatter 的 `last-verified: 2026-10-26` | 今天是 2026-10-25（未来日期） | 下次改该文件时订正；不影响结论 |

---

## 七、下一步（按顺序，一句话版）

1. **先解 G0**：装 Go 1.25 或推验证分支跑 CI——否则下面每一条代码都只能写、不能验收。
2. **G1**：kit 门禁 → 提交 → `v0.1.4` → 三仓升 pin（解锁 service token 全链）。
3. **G2**：risk `QT-74` 词典 → `contracts/risk` `v0.7.0` → core 消费（最长链，越早开越好）。
4. **G3**：QT-51 rebase → 门禁 → 合入（与 G2 并行）。
5. **G4a**：data 撤认领 `index_daily`（结构冲突优先于搬迁）。
6. **G6**：core `/report` 三态（小改，解锁 analytics 的失败语义）。
7. **G5/G7**（等 G1）：orchestration R5 + artifact 端点；strategy 三端点校验 + core 网关。
8. **G8**：DOC-10 可立即做；UI 无栈子集可立即做；broker R10 等 G0。
9. **G4d/UI e2e/XTP**：等运维授权、Docker、外部 SDK。

> **本轮未做**：未改业务代码、未跑测试、未提交 kit 的 4 个改动、未删除任何 worktree/分支、未推送。
