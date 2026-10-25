---
status: active
last-verified: 2026-10-25
verified-by: 三轮裁决合并：第一轮 15 条（§一）+ 第二轮 16 条（§五，2026-10-25 按 c 建议全部拍定）。本文件是**裁决真相源**，各仓台账只留指针不镜像正文。
---

# 跨仓裁决记录 · 2026-10-25

> 配套：[跨仓施工分组方案](cross-repo-dispatch-plan-2026-10-25.md)（分组与派发原文）· [第二轮逐条论证（b）](pending-decisions-recommendations-2026-10-25b.md) · **[终版建议（c）](pending-decisions-recommendations-2026-10-25c.md)**。
> 本文件是**裁决真相源**。各仓台账（`tellurion` / `tellurion-analytics` / `tellurion-data` / `tellurion-strategy` / `tellurion-agent` / `tellurion-orchestration` / `tellurion-broker` / `tellurion-docs` / `tellurion-kit`）只留**一行指针**，**不镜像正文**（双写必然漂移）。
> 取证时刻：2026-10-25。**以实测为准**，台账与实测冲突时本文件与实测同侧。
> **编号分区**：§一 的 ①~⑯ 是**第一轮**编号；§五 的 ①~⑳㉑㉒ 是**第二轮清单（wave2 §5）**的编号。两套编号不同域，引用时必须带「§一 / §五」。

---

## 一、15 条裁决一览

| # | 事项 | 裁决 | 落点（谁改哪个文件） | 状态 |
|---|---|---|---|---|
| ① | `service` token 的分发面与部署清单归属 | **专用 `service` 角色 + 长期 access token**（裁-17 定稿，不复用 `trader`）；**部署清单归 core `deploy/k8s/`** | core：`docs/TASKS.md` QT-71 ③-⑦ + `deploy/k8s/` | ✅ 已落（QT-71，`aaf37bc` / `134b283`） |
| ② | QT-73 五包切包的语义变更 | **已随 QT-73 落地**：只切包不改语义，两条行为差异**如实登记、报协调者**；**不返工**（见 §2.1） | core：`docs/TASKS.md` QT-73 完成记录 | ✅ 已落 |
| ③ | `analysis` 契约包 owner | **不建 `analysis` 包**；DTO 留 analytics 本仓，按 contracts ADR-0001 ①「第二个使用方出现时才迁」 | analytics：`docs/TASKS.md` QT-43 R2 口径 | ✅ 已落（`api-schema.md` §6.4 + 守卫） |
| ④ | QT-32 是否本代范围 | **本代做**，拆两步：先补 promote 审批留痕，再做 AI 侧接线；**AI 生成侧归 agent 仓** | core QT-32 + agent QT-32-A | 🔶 agent 侧施工中；**promote 入口改由 §五 ⑫ 定** |
| ⑤ | `ingest.raw` 残留行处置 | **追认删除已执行**（不可回滚）；**在 main 重做 3 笔纯文档落账**，**不 cherry-pick** `task/g3-data` 的 7 笔 | core：`docs/TASKS.md` DBT-15 + `docs/SPEC.md` | ✅ 已落（DBT-15，164+2 行，12,134→11,968） |
| ⑥ | `fundamentals_detail` 的产出方 | **接受「不可得」成文**；同时把「需要谁产出什么格式」写成**对外产出请求** | core：`docs/TASKS.md` DBT-16 + `docs/SPEC.md` | ✅ 已落（DBT-16） |
| ⑦ | `capital_flow` / `top_list` 归属 | **接受「仅储备、暂无读方」**；`top_list` 的真实状态 = 「在 cron 名单 + 无排程行 = 事实上不日更」 | core：`docs/TASKS.md` DBT-17 + `docs/SPEC.md` | ✅ 已落（DBT-17） |
| ⑧ | `mining.Proposer` 归属 | **归 agent 仓**；core 只保留 `mining.Proposer` 接口作为冻结契约 + 指针 | core：DBT-18 指针；agent：另立六字段单 | ✅ 已落（DBT-18） |
| ⑨ | `visible_at` 生产库回填授权 | **原裁「暂不授权」已被 §五 ⑤ 取代**：改为按表分批，9 张「core 无写者」的 A 集表可授权 | data：`docs/TASKS.md` QT-40 未落项 | 🔁 被 §五 ⑤ 取代 |
| ⑩ | `docs/openapi.yaml` 写入权 | **同意 QT-72 独占写**，并加「生产路由表 ↔ yaml 双向对拍」自检 | core：`docs/TASKS.md` QT-72 ⑤ | ✅ 已落（QT-72） |
| ⑪ | U6 / U8 两条未裁边界 | **U6 暂缓**（本轮不裁）；**U8 现在就裁 = 归 `ai.*`** | `repo-inventory.md` §3.1 + data `db-schema.md` §1.3 | ✅ 已落（U8；U6 标本轮不裁） |
| ⑫ | XTP 柜台环境 | **本轮不投人**；G7 降级为「决策材料 + 触发条件」 | broker：`docs/TASKS.md` QT-45 | ✅ 已落 |
| ⑬ | `producer_pending` 字段名 | **core 不引入**；它是 orchestration 侧自己的响应提示位 | orchestration：QT-44 的 X0 行 | ✅ 已裁；**提示位实现并入 §五 ⑱** |
| ⑭ | promote 闸门的审批人身份 | **已被 §五 ⑫ 取代**：strategy 零鉴权、零调用方，原「取 JWT `sub`」没有承载体 | strategy / core | 🔁 被 §五 ⑫ 取代 |
| ⑮ | QT-73「零语义五包」前提 | **已被 QT-73 落地实测订正**（`clock` 保留代码零引用；`logging`/`httpclient` 两条行为差异如实登记） | core：`docs/TASKS.md` QT-73 完成记录 | ✅ 已落 |
| **⑯** | ⛔ **service token 在 httpadapter 上必然 401** | **已裁：方向 3 —— 按 `claims.Role == "service"` 分流，只豁免 service 角色** | kit（`v0.1.4`）+ risk / data / analytics（升 pin） | ✅ **已闭合（2026-10-25）**：`v0.1.4` 已发版（CI 全绿 + proxy 已确认），三仓 pin 已升且各自 main CI 绿；**端到端真进程腿与 token 配发仍未落**（见 §一 ⑯ 收尾段） |

---

## 二、逐条裁决详情

### ① `service` token 的分发面与部署清单归属

**裁决**
1. 采纳裁-17 定稿：**专用 `service` 角色 + 长期 access token**。**不复用 `trader`**——`trader` 同时能调 `POST /api/execution/orders` 与 emergency-flatten（`tellurion:pkg/auth/auth.go` 的 `CanTrade`），把它复制到 risk/data/analytics 任意一份配置等于复制实盘下单权。
2. **部署清单归 core `deploy/k8s/`**，不在服务仓。同步路径优先转发用户 token；服务 token 只兜后台路径（alertLoop → risk events、未来 worker → core）。

**依据（实测）**
- core `deploy/k8s/` 实测 8 份清单；`docker-compose.yml` 实测 3 个服务（`data-service` / `analysis-service` / `ai-service`），`JWT_SECRET` 只出现在 analysis（`:160`）与 ai（`:228`）⇒ **data-service 今天没有 secret**。
- **risk / analytics / orchestration / broker / data 五仓的 `AGENTS.md` 里 `deploy` / `部署清单` / `k8s` / `Dockerfile` 全部零命中** ⇒ 服务仓今天没有自己的部署面。
- core `AGENTS.md` 的目录结构把 `deploy/` 归 core。

**落地动作（QT-71 开工时）**
1. core `deploy/k8s/` 新增 `risk-deployment.yaml` / `analytics-deployment.yaml`；`data-deployment.yaml` 已存在，补 secret 即可。
2. `JWT_SECRET` 与三个服务 token（`RISK_SERVICE_RESOLVE_TOKEN` / `DATA_SERVICE_TOKEN` / `ANALYTICS_CORE_TOKEN`）一律 `secretKeyRef` 注入，**只经 env、不落库、不进镜像**。
3. `core/docker-compose.yml` 同步给三个服务补 `JWT_SECRET`。
4. **轮换周期写进部署文档**（本条 ③-⑦ 的原文要求）。

**代价**：core → risk 的 `POST /api/risk/resolve` 按裁-11 口径在**配置了但取不到**时会让该 run 直接 `failed`（`risk_rules_unavailable`）⇒ 部署清单缺失不是降级，是全量阻断。

---

### ②⑮ QT-73 五包切包 —— 已随 QT-73 落地，**不返工**

**裁决**：**接受 QT-73 的实际落地形态**（五包一次切完、两条行为差异如实登记），**不按「拆成一期 2 包」返工**。理由：QT-73 已于 2026-10-25 落（`170f09a`），实测「五个目录不存在 + 全仓绿 + 破坏验证红→还原→绿」；拆回去要重做一遍同型工作，**收益为零、风险不为零**（拆包会引入第二轮 import 路径改动，而第一轮刚被一次完整的 build/vet/test 验证过）。

**三条前提的实测订正（已写进 QT-73 完成记录）**
- **`clock` 的改名**：`core/pkg/clock/interfaces.go` 常量名是 `ModeBacktest`，kit 是 `ModeDeterministic`。**实测 core 内 8 处 `clock.ModeBacktest` 引用全部位于被删除的 `pkg/clock/*_test.go` 内**（`clock_impl_test.go` 4 处 + `interfaces_compliance_test.go` 4 处），**保留代码零引用** ⇒ 无需在调用点改名。**派发原文「必须一并把调用点改成 `ModeDeterministic`」的前提不成立。**
- **`logging` 的行为差异**：core 原 `logger.go` 恒 `SetGlobalLevel(zerolog.DebugLevel)`（kit 已按配置）⇒ 切包后日志级别行为改变。**只切包、不修旧口径；已登记、报协调者。**
- **`httpclient` 的行为差异**：core 无 `newStatusError`，kit 有 ⇒ 4xx 从「静默当成功」变为返回 `*apperrors.AppError`。**breaking；只切包、不改任何调用点的 `err != nil` 判断；已登记、报协调者。**

**import 点数（实测，一字不差）**：`errors` 32 + `decimal` 1 + `clock` 27 + `logging` 26 + `httpclient` 4 = **90 处**（`origin/main` 上 87 个文件、90 次出现）。切包后目录外 **84 个文件**改 import 路径（另 3 个含旧 import 的文件在被删目录内，随目录一起删）。

---

### ③ `analysis` 契约包 owner —— **不建新包**

**裁决**：`analysis` DTO **继续留在 analytics 本仓**（`internal/analysis/model.go` + `internal/analysis/upstream_contract.go` 的 `UpstreamContractVersion = "v1"`），**不进 contracts**。触发迁移的条件写死为：**core 或 orchestration 出现 import analytics DTO 的 Go 代码**（HTTP 交互不算）。

**依据（实测）**
- contracts `docs/adr/0001-契约只增不破且零实现.md` 决策 ① 原文：「**本仓只放被 ≥2 个仓使用的类型 / 接口 / 错误 / 常量**」。
- 实测 core 对 `tellurion-analytics` 的 import = **0 处**；core 内 26 处 `PortfolioSnapshot` / `FactorExposure` 命中**全是 core 自己的私有符号**（`pkg/alert/detectors.go` / `manager.go` / `pkg/live/paper_session.go` 的 `portfolioSnapshot`），core 内 `AnalysisReport` **零命中**。
- contracts 仓当前包清单实测：`dataprotocol` / `domain` / `engine` / `fees` ⇒ **没有 `analysis` 包**。
- orchestration 与 analytics 的交互**只经 HTTP**（`internal/upstream/sensitivity.go` 走 `POST /api/runs/parents`）⇒ **不是 Go 类型依赖**。

**代价**：若将来真出现第二个 Go 使用方，再按 ADR-0001 ① 迁；**迁移时端口方法集与校验语义不变**（沿用 QT-43 施工 ③ 的原话）。

---

### ④ QT-32 是否在本代范围 —— **本代做**

**裁决**
1. **本代做**。QT-32 ② 原文：「若产品侧改判『G6/M2 不在本代范围』，则删除本单并在执行顺序总览显式声明划出，**不得沉默**」——本轮**不划出**，故本单继续在册。
2. **拆两步**：**第一步补 promote 闸门的审批留痕**（见 ⑭），**第二步做 AI 侧接线**。
3. **AI 生成侧归 agent 仓**（QT-32 ④ 已定「不把 AI 编排逻辑搬进 core」；ADR-023 同条）。

**依据（实测）**
- QT-32 ③ 的三条依赖**已全绿**：QT-41b ✅、QT-41 ✅、QT-03 ✅。
- strategy 仓 promote 端点**已存在**：`internal/httpapi/api.go:46` `POST /api/strategies/{id}/versions/{hash}/promote`；服务层 `internal/asset/service.go:466` `Promote` 带状态机 `StatusValidated → StatusPromoted` 与 `cache.InvalidateLatest(id)` 同刻失效。
- agent 仓 AI 能力**已具备**：`pkg/ai/` 下 16 个包（`pipeline` / `evolution` / `gene_pool` / `validator` / `validation` / `search` …）。
- **缺的是闸门留痕，不是管线**：strategy 全仓 `approved_by|approver|audit` = **0 命中**。

**落地动作**：agent 仓 QT-32-A 已立单（`tellurion-agent:docs/TASKS.md`），施工中；core QT-32 保留为主单，等 agent 侧收口后结案。

---

### ⑤ `ingest.raw` 残留行处置 —— **追认 + 重做落账，不 cherry-pick**

**裁决（三段）**
1. **追认**：删除**已经发生且不可回滚**，把它作为既成事实写进 main 的台账与 SPEC。
2. **不 cherry-pick** `task/g3-data` 的 7 笔 —— 实测会撞（见下）。改为**在 main 上重做 3 笔纯文档落账**（DBT-15 / DBT-16 / DBT-17）。
3. **代码那 4 笔全部作废**：main 已有自己的等价或更新实现。

**依据（实测）**
- **删除已执行**（`task/g3-data` 分支 DBT-15 施工记录原文）：`DELETE FROM ingest.raw WHERE payload ? 'probe' AND source='akshare';` **影响 164 行**、`DELETE FROM ingest.raw WHERE source LIKE 'unit-test%';` **影响 2 行**；删后 `total=11,968 / probe=0 / unit=0 / tushare=11,968`。
- **计数与 main 台账不符**：main 台账写「**155 行**」「预期 **12,108 → 11,951**」；实测是 **164 行**、`12,134 → 11,968`。
- **cherry-pick 会撞**：`git merge-tree --write-tree main 49a97c7` ⇒ **EXIT=1**，`CONFLICT (content): Merge conflict in cmd/data/sync_handlers.go`；三笔 docs commit 的 `git apply --check --3way` **全部失败**（`docs/SPEC.md:229` / `docs/TASKS.md:1114` / `AGENTS.md:868` 均 patch failed）。
- **main 已有等价或更新的实现**：DATA-14 —— main `cmd/data/sync_handlers.go:34` `collectionPayload[T]`（泛型）vs g3 `cmd/data/response_json.go`（257 行另一套）⇒ g3 那套作废；DBT-14 —— main `pkg/data/equitydeep/equitydeep.go:132` `parseFetchedAt`（裁-16 口径）vs g3 `eb33d8d`（另一套）⇒ 口径不同，g3 那笔作废。

**落地动作**：在 main 上新开一笔**只动 `docs/TASKS.md` + `docs/SPEC.md` + `AGENTS.md`** 的落账 commit；DBT-15 写「经授权按锚点精确删除 164+2 行，删后 11,968、probe=0、unit=0、tushare 未动」。

---

### ⑥ `fundamentals_detail` 的产出方 —— **接受「不可得」成文**

**裁决**：接受「不可得」这个**结论**（它是对的），同时把 g3 分支的**「产出请求」段落一起搬进 SPEC** —— 它已把「需要谁产出什么格式」写成了可执行的请求（ndjson 每行的必填形状、`fetched_at` 必须 RFC 3339）。

**依据（实测）**
- `task/g3-data` DBT-16 落账原文：「**`fundamentals_detail` 今天仍是 0 行** —— 本单收的是『状态与请求都成文』，不是『有数了』」。
- g3 取证链完整：真 HTTP 门 → 真库 → PIT 读回 `0 → 5 → 0` 零残留（`1764598`）；Tushare 免费权限 25 条 raw 的 `fields` 只有 `[ts_code, ann_date, end_date, roe, roa, gross_margin]` 一种形状，与因子需要的 8 个 field_code **一个都不对得上**，6 名经字典查找全部 miss。
- **main `docs/SPEC.md:1876` 那行已被证伪**：仍写着「依赖 `ingest.raw` 归档快照，当前无 raw 入库 ⇒ 恒 0 行」，而 `ingest.raw` 实际 **12,108 行**、自动归档腿早在生产装配里（`pkg/data/tushare.go:220` → `pkg/data/tushare_raw.go:31` → `pkg/storage/ingest_raw.go:50`）。

**落地动作**：改写 `docs/SPEC.md:1876` 那行（按 g3 的「阻塞理由证伪并改写」口径）+ 追加「`fundamentals_detail` 的产出方」一节 + `AGENTS.md` 那条「基本面深度不足」同步。

---

### ⑦ `capital_flow` / `top_list` 归属 —— **接受「仅储备」**

**裁决**：两张都登记为「**仅储备、暂无读方**」；**不接读方、不建排程**。并按 g3 的口径订正落账。

**依据（实测）**
- g3 段落原文：「理由就是 ④ 那句『读方必须服务某个研究问题』—— 本轮没有这样的问题，所以不为了『表里有数』造一个读方。」
- **g3 自带一条台账原文没有的订正（比原文更准）**：「工单 ② 写『`top_list` 则**进了** cron 名单』，代码那半对，但它带的推论（⇒ 所以在日更）**不成立** —— 名单只影响 `sync_schedules` 的校验，**没有任何 schedule 行存在就不会跑**。今天的真实状态是『**在名单里 + 无排程行 = 事实上不日更**』」。
- 实测印证：`cmd/data/sync_handlers.go:91` `scheduler.RegisterJobType("top_list", sync.JobTypeTopList)` **确实在**（`cmd/data/top_list_job_test.go:651` 有测试钉住）；`capital_flow` 确不在名单。main `docs/SPEC.md:1886` 记着「该表**零读方**……刻意不进 cron 名单」。
- g3 的行数实测：`capital_flow` **18 行**（`source` 单一 = `tushare_moneyflow`，`main_net` / `close_price` 等 18/18 全 NULL）；`top_list` **256 行**，`trade_date` 跨 `2026-09-21 .. 09-24`（⇒ 已停更约一个月）。main SPEC 记的也是 18 / 256 ⇒ **行数一致**。

**落地动作**：落账三件事 —— ① 两表登记「仅储备、暂无读方」；② **写清 `top_list` 的真实状态是「在 cron 名单 + 无排程行 = 事实上不日更」**（订正台账 ② 的推论）；③ 保留两族的 NULL 语义与「按 `source` 列区分口径」的警告。

---

### ⑧ `mining.Proposer` 归属 —— **归 agent 仓**

**裁决**：归 agent 仓。core 只保留 `mining.Proposer` 接口作为**冻结契约** + 在 core 跨仓任务索引加一条指针（**不镜像正文**）。

**依据**：DBT-18 ② 原文已给结论（「2026-10-09 AI 层整体迁往……⇒ **本单主体不在 core**」）；③ ② 原文给了归 agent 时的处置；④ 原文「core 不引入 LLM SDK；**不在本仓实现第二个编排者（ADR-023 禁）**」。

**落地动作**：core `docs/TASKS.md` DBT-18 标 ✅ 并留指针；agent 仓立自己的六字段单（**含 LLM/MCTS Proposer 的实现位置**）。

---

### ⑨ `visible_at` 生产库回填 —— **暂不授权**

**裁决**：**暂不授权**。回填是**写生产库 12 张 A 集表**的操作，且：
1. **core 第二写者仍在**（`pkg/data/source/etl.go` 存在、第 68 行注释写着 `ON CONFLICT upsert`；`cmd/data/registry_init.go` 实测 11 处 `RegisterJobType`）⇒ 回填与并发写入可能打架。
2. **回填后 PIT 门立即生效**：历史上 `visible_at` 为 NULL 的行会**整批拒收**（台账原文「未回填的行会被 PIT 整批拒收」）⇒ 在第二写者没停的前提下，这等于把数据面切到一个未验证的状态。
3. 台账原文：「**生产 D2 前序红线：D2 未完成期间读路径不许按 `visible_at` 过滤**」。

**落地动作**：把「生产回填」登记为**运维动作**而非 agent 动作；前置写死为「core 停第二写者 → 回填 → 才允许读路径按 `visible_at` 过滤」。回填脚本的护栏已就绪且 fail-closed（`internal/schema/backfill_pg_test.go` 用 `-tags backfill`，只认 `DATA_TEST_DSN` 且库名须 `_test`，生产 DSN 与生产库名自动 skip）。

---

### ⑩ `docs/openapi.yaml` 写入权 —— **同意 QT-72 独占写**

**裁决**：**同意 QT-72 独占写 `docs/openapi.yaml`**，并**加一条自检**：写完必须跑既有的「生产路由表 ↔ yaml 双向对拍」（core 已有该机制）。

**依据**：台账 DATA-14 ⑦ 的「G2 唯一写者」约束**当时确实存在**；但 **G2 组的写者已交还**（G13 协调/发版收口 ✅ 2026-10-24）⇒ 当前无活跃写者；QT-72 已被排在阶段 B（等 QT-71 改完 `route_policy.go` 之后）⇒ 撞车面已最小；`check_doc_links.py` 实跑 EXIT=0 ⇒ yaml 不是链接检查的活跃层文件。

**落地动作**：QT-72 ⑤ 验收加一条「`python tools/check_doc_links.py` 退出 0 + 生产路由表 ↔ yaml 双向对拍绿」。

---

### ⑪ U6 / U8 —— **U6 暂缓，U8 现在就裁**

**裁决**
- **U6（reduction-service 的仓位）= 本轮不裁**，在 `repo-inventory.md` §3.1 标「本轮不裁」。理由：台账倾向已是「先作 `tellurion-data` 内的独立包」，**不阻塞本轮任何单**。
- **U8（`factor_hypothesis` 的 owner）= 现在就裁 = 归 `ai.*`**。理由：台账原文已给倾向（「它是 ADR-023 的实验日志，不是仪器域数据」）；且 U8 **正好命中 G6 的 X1d schema 清单**（「影响 QT-40 X1d 的 schema 归属清单，与 QT-17 的 `ai.*` 表名单（二者会各自把它写进自己的迁移）」）⇒ 越晚裁越要写两次。

**落地动作**：U8 落一条裁决记录 —— `repo-inventory.md` §3.1 的 U8 行改判 + `tellurion-data:docs/db-schema.md` §1.3 **撤认领行** + `tellurion-agent` 的 `ai.*` 表名单加一行。

---

### ⑫ XTP 柜台环境 —— **本轮不投人**

**裁决**：G7 降级为「**决策材料 + 触发条件**」，只出材料不出施工步骤。

**依据（实测）**：broker 仓 `internal/gateway/xtp.go` 实测**零 SDK import**（`import` 段只有标准库 + 本仓包）；全 workspace 4 层深度内 grep `xtptrader|xtp-api|github.com/xtp` 只命中 broker **自己的** `mock.go` / `ports.go` / `xtp.go`，**无第三方 SDK**。台账原文：「X6c 全量适配器……需要真实 XTP SDK 与柜台环境，**仍从零未做**」。

**落地动作（已落）**：broker 台账 QT-45 施工 ⑤ 已记「本组不具备开工条件，交付的是决策材料」+「X6c 判 ⛔ 阻塞，不是 🔶 待做」+ 触发条件清单。

---

### ⑬ `producer_pending` —— **归 orchestration 侧，core QT-72 不引入**

**裁决**
1. **core QT-72 的三态锚点 = orchestration 的三个 wire code**（`provenance_not_ready` / `provenance_not_found` / `provenance_missing`）——**不变**。
2. **`producer_pending` 不进 core QT-72**。它是 **orchestration 侧自己的响应提示位**（表达「这个端点的生产者还没接线，所以查不到 ≠ run 不存在」），归 `tellurion-orchestration` 仓；**core 的端点没有这个问题**（core 的生产者就是 core 引擎，永远有写者）。
3. core QT-72 ② 那句「orchestration 端点保持 `producer_pending`」改写为「三态锚点 = orchestration 的三码；core 端点**不依赖** orchestration 进程，也不引入 orchestration 侧的提示位」。

**依据（实测）**
- core QT-72 ② 原文：「**裁-19 定稿**：读面落 core；`run_artifacts` 重新定义为『worker 写的投影』，**core 不做双写**」⇒ **core 的读面与 orchestration 的 worker 投影是两个东西**；core 的端点直接读 `quant.run_provenance ⋈ backtest_jobs`（**QT-63 的表，core 自己的**）。
- orchestration 侧三态实测在 `internal/httpapi/provenance.go`：`:76-78` 注释写明 409 `provenance_not_ready` / 404 `provenance_not_found` / 500 `provenance_missing`；`:113-130` 是对应实现。
- **`producer_pending` 的准确身份（实测，2026-10-25）**：orchestration 全仓**代码零命中**，但**台账两处在册** —— `tellurion-orchestration:docs/TASKS.md:121`（裁-19 定稿）原文：「本仓 `GET /api/runs/{id}/provenance` 与父 GET 的三 hash 字段保留（施工 ⑨ 成果不回滚），但**文档/响应须让运维看出 `producer_pending`**——『查不到』不等于『run 不存在』」；`:166` 的施工序行同义。
  ⇒ **它不是第四个状态码**（不与三码竞争），而是**orchestration 端点的一个待实现提示位**。**本裁决把它归回 orchestration 侧**，不去 core 引入。

**落地动作**：core QT-72 ② 按上面第 3 条改写；orchestration 侧实现该提示位（**归 orchestration 台账 QT-44 的 X0 行**，不在 core 施工）。

---

### ⑭ promote 闸门的审批人身份 —— **取 JWT claims 的 `sub`**

> **⚠️ 2026-10-25 18:45 实测订正（本条的前提被推翻，裁决从「可施工」降级为「只出决策材料」）**
> 原文保留（不删改历史），但**落地前提已被证伪**：
> - **strategy 仓零鉴权** —— 全仓 `grep "JWT_SECRET|Bearer |Authorization|RequireRole|Roles\b"` = **0 命中**；**没有**任何 auth / role / guard / middleware 命名的 `.go` 文件；`go.mod` 只有 contracts `v0.5.0`、**没有 `tellurion-kit`** ⇒ kit 的 `auth` 包**根本没进这个仓**；`internal/httpapi/api.go:42-56` 的 **12 条路由全部无保护**（含 `POST /api/strategies/{id}/versions/{hash}/promote` 与 `/internal/*` 四条）。
> - **也没有调用方** —— core 全仓 `grep "/promote"` = **0 命中** ⇒ 今天既没有鉴权、也没有人调它。
> ⇒ **「从 JWT claims 取审批人」在 strategy 仓今天没有承载体**。本条改为**只出决策材料**（三个可选面 + 各自代价），由协调者裁「审批人从哪来」后再谈施工。见 [第二批分组方案 §5 ⑫](cross-repo-dispatch-plan-wave2-2026-10-25.md)。

**裁决**
1. **审批人身份取 JWT claims 的 `sub`**（沿用 core 既有先例：risk 仓的 `actorOf` 从 claims 取、缺失退 subject；core `pkg/auth` 有 `sub` claim）。**—— ⚠️ 此条的前提在 strategy 仓不成立，见上方订正。**
2. **闸门落在 agent 仓**（AI 生成侧；审批是人的动作）；**strategy 侧只保留状态机**（`Promote` 端点已在，但**要加一个必填的审批人入参**并落进 `compile_log` 或新表）。
3. **不许 caller 自报身份** —— 沿用 risk 仓 `Actor` 字段用 `json:"-"` 防自报的做法，或直接从 JWT claims 取。

**依据（实测）**：strategy 全仓 `approved_by|approver|audit` = **0 命中** ⇒ **无审批人字段、无独立审计表**；`internal/asset/pgstore.go:393` `Promote(...)` 的 `log` 参数在 `service.go` 调用处**硬编码为 `"promoted"`** ⇒ 落进 `strategy.strategy_artifacts.compile_log` 的只有「结论」，**没有「谁批的」**。

**落地动作**
1. strategy 侧 `Promote` 端点增**必填**审批人入参 + 落库（`compile_log` 追加，或新开 `strategy.strategy_promotions` 审计表，留痕三件套：**审批人 / 时间 / 结论**）。**—— ⚠️ 前置：先裁「审批人从哪来」（面 A / B / C）。**
2. agent 仓 QT-32-A 把「promote 人工闸门」写成必经步骤；验收含「未过 promote 的产物被回测路径拒绝（fail-closed 用例）」。**—— ✅ 已落**（`tellurion-agent:docs/TASKS.md` 的 QT-32-A）。

**代价**：③-② 要求的「闸门记录进审计，审批人/时间/结论留痕」**只落三分之一**（今天只有「结论」的一部分）⇒ AI 产物的生产责任链断在「谁批的」这一环，日后无法追责。**且闭环的最后一环缺的不是「审批留痕」，是「谁来调它 + 调它的人可信吗」**（core 侧 0 调用方 + strategy 侧 0 鉴权）。

### ⑯ ⛔ **service token 在 httpadapter 上必然 401**（2026-10-25 18:45 实测新发现，需裁决）

> **这是本轮实测逮到的真缺陷，不是台账问题**：裁-17 的 `service` 角色方案在 kit 侧签得出 token，但在服务侧**解析不过去**。

**实测（kit 仓 `v0.1.1`，risk / data / analytics 三仓都在用这个版本）**
- `tellurion-kit:auth/service_token.go:26` 的 `IssueServiceToken(subject, role string, ttl time.Duration)` **允许任意非空 subject**（`:30` 只查空/空白；`:42` `Username: subject`、`:46` `Subject: subject` 原样签进 claims）。
- kit 自带测试正是这么用的：`auth/service_token_test.go:16` `IssueServiceToken("service:risk", "service", ttl)`、`:41` `IssueServiceToken("service:data", …)`。
- 但 `tellurion-kit:auth/httpadapter/httpadapter.go:139-141`：`if _, err := claims.UserIDInt64(); err != nil { return nil, "malformed token subject", ErrUnauthenticated }`。
- 而 `auth/claims.go:29` 的 `UserIDInt64()` 做的是 `strconv.ParseInt(c.Subject, 10, 64)` ⇒ **`subject = "service:risk"` 解析失败 ⇒ 401**。
- `auth/middleware.go:63` 与 `auth/auth.go:201` 也各有一处 `UserIDInt64()`。

⇒ **后果**：任何按 KT-09 签出来的 service token，送到 risk / data / analytics 的 net/http 适配层都会拿到 **401 `malformed token subject`** ⇒ **裁-17 的整条服务间链路今天走不通**。

**为什么两条测试都绿**（这是本缺陷最该记住的一层）
- kit 的 `service_token_test.go` **只验签发侧**（`require.NoError` + `require.NotEmpty`），**没有把 token 送进 `httpadapter.Authenticate`** ⇒ 签得出就算过。
- risk 的 `TestPolicy_*` 走的是 `IssueTokens(1, …)` 的**数字 subject**（`tellurion-risk:internal/httpapi/auth_test.go:55`）⇒ 数字能过 `ParseInt`，**夹具绿不等于生产链路绿**。

**需你裁（三个方向，本轮我不替你选）**
1. **放宽 `httpadapter` 的 subject 校验** —— 承认「service 主体不是用户 id」；**代价**：`httpadapter` 是三个服务仓共用的鉴权面，改它要发 kit 新 tag（`v0.1.4`）且 risk/data/analytics 三仓**同步升 pin**（今天都钉在 `v0.1.1`）。
2. **让 `IssueServiceToken` 强制 subject 为数字** —— **代价**：与「service 主体要可读」（`service:risk` 这种）冲突；且会与 core 的 `pkg/auth` 的 `RoleService` 主体口径分叉（core 自己怎么签还没定）。
3. **在 `httpadapter` 里按 `claims.Role` 分流** —— 用户 token 走 `UserIDInt64()`，service token 走另一条解析。**代价**：`httpadapter` 是共用的 ⇒ 与方向 1 同样要发版 + 三仓升 pin。

**⚠️ 无论选哪个方向，kit 的 `service_token_test.go` 必须补一条「签出来的 token 能被 `httpadapter.Authenticate` 接受」的用例** —— 这条缺了，同类缺陷还会再犯。

**落地位置**：kit 仓（发版）+ risk / data / analytics 三仓（升 pin）。**本轮不安排 agent 施工，等裁决。**

> **✅ 2026-10-25 已按「方向 3（按 role 分流）」动手修了代码，但未发版**
>
> **改动（kit 仓 4 个文件）**：`auth/claims.go` 新增 `const ServiceRole = "service"`（注释写明**不是角色枚举**，守住 KT-09 ③-④）；`auth/httpadapter/httpadapter.go` 的 `authenticate()` 在 `Kind != access` 之后、`UserIDInt64()` 之前加 `if claims.Role == auth.ServiceRole { return claims, "", nil }`；`auth/middleware.go`（gin 变体）同样分支，**不设 `CtxUserID`**（不伪造 user id，审计侧看到"无用户"而非用户 0）；`auth/httpadapter/httpadapter_test.go` 新增 `TestAuthenticate_ServiceTokenFromIssueServiceToken`（用真 `IssueServiceToken` 签的 token 喂进 `Authenticate`，并断言非 service 角色 + 非数字 subject **仍 401**）。
>
> **为什么选方向 3**：它最小 —— 只豁免 service 角色，用户 token 的 int64 校验**原样保留**（方向 1「放宽校验」会让非 service 的非数字 subject 也通过，方向 2「强制数字」与"service 主体要可读"及 core 的 `RoleService` 口径冲突）。实现**逐行对照 core 已上线并有钉子测试的同一逻辑**（`tellurion:pkg/auth/middleware.go:68-81` + `middleware_test.go:164`）。
>
> **⚠️ 未验证（如实登记，不写成已验证）**：**本机没有 Go 工具链**（`go` / `gofmt` 不在 PATH，全盘无 `go.exe`；Docker daemon 亦不可达）⇒ `go build` / `go vet` / `go test` **一条都没跑**。因此**没有打 tag、没有升三个消费仓的 pin** —— 不把未经编译的代码发成版本。
>
> **收尾清单（需在有 Go 的环境执行）**：
> ```
> cd tellurion-kit && gofmt -l . && go build ./... && go vet ./... && go test ./auth/... -count=1
> git tag -a v0.1.4 -m "fix(auth): service token 的 subject 不再被 int64 校验拒掉"
> # 三个消费仓各一次：go get github.com/ruoxizhnya/tellurion-kit@v0.1.4 && go mod tidy
> ```
> 完整建议见 [`pending-decisions-recommendations-2026-10-25b.md`](pending-decisions-recommendations-2026-10-25b.md) §0。

> **⚠️ 2026-10-25 二次实测（上一条「并发 WIP」注记的归属订正）**：kit 工作区的那 4 个未提交文件（`auth/claims.go` / `auth/httpadapter/httpadapter.go` / `auth/httpadapter/httpadapter_test.go` / `auth/middleware.go`，`git diff --stat` = 4 文件 / +65 行、0 删除）**就是上面「方向 3」这一笔改动本身**，不是第三方的 WIP。
> ⇒ 现状口径统一为：**代码已改、未验证（本机无 Go）、未提交、未发版（无 `v0.1.4`）、三个消费仓仍钉 `v0.1.1`**。
> ⇒ 待你处理的只剩三步：**跑门禁 → 提交 + 打 `v0.1.4` → 三仓升 pin**。**在此之前「service token 仍然 401」这个线上事实没有改变**（kit `v0.1.1` 未动）。

> **✅ 2026-10-25 收尾完成 —— 上面两段的现状口径已被本段取代：`v0.1.4` 已发版、三仓 pin 已升。**
> 三步的执行与取证（**证据全部来自 GitHub Actions 退出码，不是本机复跑**——本机仍无 Go 工具链，这是上面那条登记的环境事实，没变）：
>
> 1. **kit 侧**：PR `tellurion-kit#1` rebase 合并 → `main = 8077c36`，main 的 run `40235174365` 全步骤 success，其中
>    `ok github.com/ruoxizhnya/tellurion-kit/auth 0.976s`、`ok …/auth/httpadapter 0.008s`（**新增的
>    `TestAuthenticate_ServiceTokenFromIssueServiceToken` 就在后一条里**，即「签出的 token 能被 `Authenticate` 接受」这条缺过的用例已补上并跑绿）。
>    annotated tag `v0.1.4` 已 push；`proxy.golang.org/…/@v/v0.1.4.info` 返回 `Version=v0.1.4 / Ref=refs/tags/v0.1.4 / Hash=8077c36c006bc497a65a65ad4f322f931b52e0ef`。
>    发版三件套齐：`CHANGELOG.md` 的 `[v0.1.4]` 节（**该输入的处置记为破坏性**：原来 401 的 `role=service` 非数字 subject 现在放行）、README 兼容矩阵 `v0.1` 行、tag。
> 2. **三消费仓升 pin**：risk PR #1（`6b6690f`）、data PR #1（`9f66ced`）、analytics PR #1（`e0a47d9`）各自 CI 全步骤 success 后 rebase 合并，
>    **合并后 main 的 push run 同样 success**（risk `40236132772` / data `40236236884` / analytics `40236240650`）。
>    三条 Build 日志各有一行 `go: downloading github.com/ruoxizhnya/tellurion-kit v0.1.4` —— 这是 `go.sum` 两行填对（取自 `sum.golang.org`）的正证据，填错当场红。
>    `v0.1.4/go.mod` 的 hash 与 `v0.1.1` **逐字节相同** ⇒ 三个仓的依赖图未变，`go.sum` 只换两条 version-keyed 行。
> 3. **文档订正随各仓同一笔提交**：risk / data / analytics 各自把「不升 kit / 仍钉 `v0.1.1`」的旧文案标为作废（data 的 ADR-0005、analytics 的 ADR-0004 各加追加节；**不改写原决策其余口径**），
>    并写明**各自守卫用例证明的仍是策略表、不是生产链路**——用例用的还是数字 subject + `role=service`。
>
> **仍然为真的两个「未」**（别把这条闭合读成「服务间链路已通」）：
> - `JWT_SECRET` 与调用方 `service` token 的**配发**属 core QT-71（部署清单腿），未落；
> - **「core 真进程 → 服务真进程 → `sub=service:<repo>`」这条端到端腿仍未跑过**——今天的证据是「kit 的 `Authenticate` 接受该形状」+「三仓 CI 在 `v0.1.4` 下全绿」，不是进程间真调用。
>
> **core 自身不受这条影响**：core `go.mod` 仍 `require tellurion-kit v0.1.1`（QT-73 只切了五包，`auth` 不在其中）；core 的 `pkg/auth` 早就为 service token 开了 `RoleService` 特例，无需升 pin。

---

## 三、给各 agent 的三条通用纪律（本次裁决带来的）

1. **不许把「部分实现」写成「已完成」**。本轮 15 条裁决里，②⑮⑫ 三条是「已落但前提被证伪」——**如实登记前提被证伪的事实**，而不是把结论圆过去。
2. **跨仓结论要在两边对齐**。③（不建 `analysis` 包）与 ⑬（`producer_pending` 归 orchestration 侧）都涉及两个仓：**一边定、两边同步**，否则会造出 wire 分叉。
3. **台账只留指针，裁决正文在 core 的本文件**。本文件是唯一真相源，各仓台账不镜像正文（双写必然漂移）。

---

## 四、裁决时刻的 12 仓状态快照（2026-10-25 12:00，实测 `git fetch` 后）

> 这一节是**裁决依据的一部分**——多条裁决的「前置已解除/仍未落」判据就来自这张表。**它是一个时点快照，会过期**；过期后请按各仓台账的现状走，不要引用本节的数字当现状。

| 仓 | ahead `origin/main..HEAD` | 工作区 | 与本轮裁决的关系 |
|---|---|---|---|
| `tellurion`（core） | **4** | 脏 | QT-73 已落（`170f09a`）+ QT-42 二批 outbox 三笔；QT-71 本仓那半（①~③+⑦）**未落** |
| `tellurion-contracts` | 1 | 干净 | `072d471`（QT-07 ⑥ 结案口径）；本轮无改动 |
| `tellurion-kit` | 0 | 干净 | **KT-09 已落并推 `v0.1.2`（`fab6188`）**、**KT-10 已落并推 `v0.1.3`（`356dda4`）** ⇒ **QT-71 的外部前置解除** |
| `tellurion-data` | 1 | 脏 | `33b68d1`（裁-17 加 `service` 角色）+ 本轮文档改动 |
| `tellurion-strategy` | 0 | 脏 | 本轮加 QT-32 闸门腿指针 |
| `tellurion-risk` | 2 | 脏 | `87c6aa9`（加 `service` 角色）+ `8145c5a`（outbox 欠账闭合） |
| `tellurion-analytics` | 2 | 脏 | `36a4e3e`（R1 换面）+ `ba53d39`（依赖账校准）+ 本轮 R2 owner 裁决 |
| `tellurion-orchestration` | 1 | 脏 | `99d23d8`（子 run 入队）+ 本轮 ⑬ 归属订正 |
| `tellurion-broker` | 3 | 脏 | G7 决策材料三笔（X6c 判 ⛔） |
| `tellurion-agent` | 0 | **脏 23** | QT-32-A 施工中（并发 agent 的 WIP，**本轮不碰其代码**） |
| `tellurion-ui` | 1 | 干净 | `89017eb`（AUD-61 ① 前置已成立） |
| `tellurion-docs` | 2 | 干净 | DOC-06 结案 + DOC-10 新立 |

### 四.1 `tellurion-g3-data` 的处置（§6.1 结案）

**它不是第 13 个仓** —— 实测 `tellurion-g3-data/.git` 是 `gitdir: C:/Users/ruoxi/Workspace/tellurion/.git/worktrees/tellurion-g3-data`，即 **core 的一个 git worktree**，分支 `task/g3-data`（`git worktree list` 可证）。core 仓另有 `task/a7-core` 分支。

**该分支 7 笔 commit 的处置（按 ⑤ 的裁决）**：

| commit | 内容 | 处置 |
|---|---|---|
| `ff36b4c` / `7e17c12` | DATA-14 空列表编码 | **作废** —— main 已有 `cmd/data/sync_handlers.go:34` 的 `collectionPayload[T]`（泛型），与 g3 的 `cmd/data/response_json.go` 是两套实现 |
| `d25084c` | G3 决策材料 | **作废**（决策已在本文件 §二 落地） |
| `eb33d8d` | DBT-14 裁决 A | **作废** —— main 已有裁-16 的 `pkg/data/equitydeep/equitydeep.go:132` `parseFetchedAt`（**口径不同**：裁-16 接受 date-only 归一为 00:00Z 日标记） |
| `49a97c7` / `3aabda8` / `17b62f3` | DBT-15 / 16 / 17 **纯文档落账** | **取结论、不取 patch** —— 三笔在 main 上**重做**（实测 cherry-pick 会撞：`git merge-tree --write-tree main 49a97c7` EXIT=1，`CONFLICT in cmd/data/sync_handlers.go`；三笔的 `git apply --check --3way` 全部 patch failed） |

**该分支的物理处置（待执行）**：确认 7 笔结论已全部进 main 后，`git worktree remove` + 删 `task/g3-data` 分支。**本轮不执行**——它涉及删 worktree，属不可逆操作，等协调者确认。

> **🔁 状态订正（2026-10-25 晚）：上面「待执行」已不成立 —— worktree 与分支都已被删除（非本轮授权）。**
> 实测：`tellurion-g3-data` 目录不存在、`task/g3-data` 不在 `git branch`、`git worktree list` 只剩 main。
> **但结论无损失**：7 笔 commit 对象经 `git cat-file -t` 逐个核验**仍返回 `commit`**（未 GC）；DBT-15/16/17/18 在 `docs/TASKS.md` **均已 ✅**。
> ⇒ 只剩「追认删除」这一件事（可选：趁未 GC 打 `archive/g3-data` 指向 `17b62f3` 留档）。详见 [`pending-decisions-recommendations-2026-10-25c.md`](pending-decisions-recommendations-2026-10-25c.md) ㉑ 的状态订正。

### 四.2 core 工作区的并发 WIP（§6.3 结案）

2026-10-25 11:00 实测 core 工作区有 11 项未提交改动（`cmd/analysis/{main,setup}.go`、`cmd/analysis/wiring/risk_events*.go`、`config/analysis-service.yaml`、`docs/TASKS.md`、`pkg/storage/{integration_test,postgres}.go` + 3 个 `pkg/storage/alert_outbox*.go` 新文件）—— 那是 **QT-42 X3c 二批「告警回传改成持久 outbox」**的施工。
**实测更新**：该批**已提交**（`a53fe95` / `0cb8e9f` / `aa729f2`）⇒ core 工作区**不再有那 11 项**。
⇒ **本轮所有 core 侧文档改动都发生在干净基线上**（除本轮自己的改动外无他人 WIP 混入）。

---

## 五、第二轮清单的 16 条裁决（2026-10-25，按 c 的建议全部拍定）

> **编号**：本节的 ①~⑳㉑㉒ 是 [第二批分组方案 §5](cross-repo-dispatch-plan-wave2-2026-10-25.md) 的编号，**不是** §一 的编号。引用时带「§五」。
> **依据与代价**：逐条论证在 [终版建议（c）](pending-decisions-recommendations-2026-10-25c.md)；本节只写**裁决 / 落点 / 状态**。
> 拍定时间：2026-10-25（主任口径「全部按建议」）。

| # | 事项 | 裁决 | 落点 | 状态 |
|---|---|---|---|---|
| ① | QT-51 拆 port 还是同构桥 | **拆 port**，以分支 `task/qt51-execution-port`（`7c89aae`）为基线收口；**不再执行** b 的「删 shim + `LiveBridge` 外移」；5 个零调用 shim 合入后另立小单删 | core：QT-51 + `backtest-engine-review.md:145` 勾选 | ⬜ 待收口（rebase + 有 Go 的环境复跑门禁） |
| ② | risk 快照迁 contracts | **新建 `contracts/risk` 包**：迁 `Snapshot` / `SnapshotRule` / `Scope` / `RuleType` + 冻结词表 + hash 纯函数 + 4 个哨兵；**`Bundle` / `ParseBundle` / `ForScope` 留 risk**；**编码逐字节搬运，禁止换 Canonical**；必须带固定向量 + 跨仓对拍腿。**前置 = risk `QT-74`（参数词典）先落**——先有冻结的 `params` 形状，才有值得迁的契约 | contracts（`v0.7.0`）+ risk（首个 contracts pin）+ core | ⛔ 等 risk QT-74（当前 ⬜） |
| ⑤ | 生产 `visible_at` 回填 | **只授权 9 张「core 无写者」的 A 集表**：`ohlcv_minute` / `realtime_quote` / `capital_flow` / `top_list` / `limit_up_pool` / `announcements` / `news` / `hot_search` / `global_ohlcv`；`ohlcv_daily_qfq` / `sectors` / `stock_sector_map` 等 core 停写后再批；`market.index_daily` 不在 A 集、不回填；**执行者是运维** | data：QT-40 未落项 + core：QT-40 记录 | ⬜ 待运维执行（agent 只交取证与语句） |
| ⑥ | UI 三服务栈怎么起 | **两轨**：无栈的 `typecheck/lint/vitest` 现在做；e2e 等 Docker Desktop（或 CI）；视觉基线在本机重生成 | ui：AUD-61 ① 批 | ⛔ 等外部条件（Docker/CI） |
| ⑦ | XTP 柜台环境 | **保持 ⛔，本轮不投人、不排期**；不做无 SDK 的假实现 | broker：QT-45 | ✅ 已裁（维持 ⛔） |
| ⑧ | DOC-10 范围 | **§7 整块按实仓订正**：阶梯表（S-1~S3、X1~X6）+ 三条过渡态 + 一行状态表；外加 `docs/README.md:40` 的 QT-60 行；**§8 以后与阶梯表的设计理由不动**；W0 三腿先核再改 | docs：DOC-10 | ⬜ 待施工 |
| ⑨ | `broker.order_acks` 保留期 | **90 天**；只删 `ack IS NOT NULL`；先加 `(updated_at) WHERE ack IS NOT NULL` 部分索引；单批 ≤ 5000 行；90 天登记为**暂定假设**（柜台可查窗口未知，若更长改 180） | broker：`db-schema.md` §1.1 + 清理作业 | ⬜ 待施工 |
| ⑩ | AI 生成物载体 | **默认 `carrier="dsl"`**；仅 L3 才 `wasm`；**`carrier="so"` 删除**；`artifact_hash` 用 `StrategyVersionInput{Carrier:"dsl"}` 产出 | agent：QT-32-A 后续单 + strategy 资产服务 | ⬜ 待施工 |
| ⑪ | R3 的 RunSpec 形状 | **终态 = 执行侧装配**（core `cmd/worker` 用 `run_id` 锚点从 `backtest_jobs` 回源）；analytics 的 `{run_id}` 是终态，不是 interim | core：`internal/orchestrationworker/spec.go`；analytics：QT-43 未落项关闭 | ✅ 已裁（无需改代码） |
| ⑫ | promote 审批人从哪来 | **面 B1**：core 作唯一对外入口（新增 strategy client + 转发路由，`route_policy` = trader/admin；从 JWT `sub` 写受信 `X-Actor`，用 service token 调 strategy）；strategy **只对 `/promote` + 两条 `/internal/*`** 引 kit service-token 校验；`X-Actor` 缺失 = 400 `actor_required`；端口仅集群内/回环 | core（QT-32）+ strategy（QT-32 闸门腿） | ⬜ 待施工（取代 §一 ⑭） |
| ⑬ | service token 401 | **方向 3**：`claims.Role == "service"` 分流，只豁免 service 角色；**收尾四步已全部执行**：门禁（CI）→ 提交 → `v0.1.4` 三件套 → risk/data/analytics 升 pin | kit（`v0.1.4`）+ risk / data / analytics | ✅ **已闭合（2026-10-25）**：kit PR #1 rebase 合并（`main = 8077c36`）+ main run `40235174365` 全步骤绿 + tag `v0.1.4` 已 push 且 proxy 已确认；三仓 PR 各自查全步骤绿后合并、合并后 main push run 也绿。**仍未落**：QT-71 的 token 配发腿 + 真进程端到端腿 |
| ⑰ | `index_daily` 归属 + 第二写者 | **owner = core**（Migration 057 是唯一 DDL）；**data 撤认领**：删 `internal/schema/market.go:301` 的建表/索引、`TableNames()` 条目、`visible_at.go:54` 的 C 集条目、`db-schema.md:25/151` 的认领，并加守卫；A 集物理 `SET SCHEMA` 等 3 张 core 停写后再做 | core：Migration 057 注释；data：schema + 文档 | ⬜ 待施工（两套 DDL 是**先建表者定形**，优先于搬迁） |
| ⑱ | X0 生产者契约 | **orchestration 新增 artifact 写端点**（service 角色），**worker 调它、不直写库**；幂等沿用 `SaveArtifact`；**前置 = orchestration 先接 R5 鉴权**（kit + policy） | orchestration：QT-44 的 X0 行；core：`cmd/worker` / `internal/orchestrationworker` | ⬜ 待施工 |
| ⑲ | R1 读面语义 | **结论态放行**：404 只给「不存在」；queued/running → 409 `run_result_incomplete`；failed → 409 `run_result_failed`；completed+invalid 维持 200 + `status="invalid"`；Migration 059 已丢失的历史 invalid 信息登记为不可恢复 | core：`cmd/analysis/handlers_backtest.go:191` + openapi + ui 错误处理 | ⬜ 待施工 |
| ⑳ | AUD-61 testid 边界 | **允许窄例外**：只给 `NavTiles.vue` / `MarketMetrics.vue` / `AppSidebar.vue` 加 `data-testid`（纯增量属性，不改类/结构/文案），单独 commit；工单 ④ 按此修订 | ui：AUD-61 ① 批 | ⬜ 待施工 |
| ㉑ | g3 worktree / 分支处置 | 确认 DBT-15/16/17 三笔文档结论已在 main（core 台账已 ✅）后，**删除 worktree + `task/g3-data` 分支**；删除前如需留痕先打 `archive/g3-data` tag | core：worktree / branch | ⛔ 待主任授权（不可逆） |
| ㉒ | broker R9 / R10 | **R10 现在修**：`mock.go:415`（QueryOrder）与 `:358`（CancelOrder）的映射读失败从 `ErrChannelDown` 改为 `ErrIdempotencyStoreUnavailable`（哨兵与 wire 码已存在），两处同口径 + 各一条用例；**R9 暂缓到 X6c**，三条差异写进 `x6c-xtp-readiness.md` §8 验收清单 | broker：`internal/gateway/mock.go` + 文档 | ⬜ 待施工 |

### 五.1 关键前提（施工前必读）

- **① 的分支不在 origin**：`task/qt51-execution-port` 本地 `1 ahead / 9 behind main`，worktree 在 `C:/Users/ruoxi/Workspace/tellurion-qt51`。合入前必须 rebase 并在**有 Go 的环境**重跑全量门禁；分支自带的等价性报告是旧基线产物，不能当合并后的证据。
- **⑰ 不是文档问题**：core `pkg/storage/postgres.go:1180` 与 data `internal/schema/market.go:301` 的 `index_daily` DDL 形状不同（`id`/`UNIQUE`/`VARCHAR(64)`/`CHECK` vs 复合 PK/`VARCHAR(32)`/`volume DEFAULT 0`），两个 `CREATE IF NOT EXISTS` 谁先跑谁定形。**data 必须删掉自己那份**。
- **⑲ 是既有端点的状态码语义变更**：openapi / UI 的 `/report` 错误处理 / e2e 要同笔改；analytics 的 wire 码已经就绪。
- **⑫ 的服务 token 必须有验证方**：strategy 今天没有 kit，也没有任何鉴权。若只加 `X-Actor` 不加校验，token 是装饰；所以三端点必须接 kit `auth/httpadapter`，其余 9 条靠端口隔离，并在 `api-schema.md` 写明。**前置已解除（2026-10-25）**：`auth/httpadapter` 在 kit `v0.1.4` 里能接受 `sub=service:<repo>`，strategy 引 kit 时直接 pin `v0.1.4`，不必再等 subject 缺口。
- **⑬ 已闭合（2026-10-25），但「链路已通」这句话仍然不许写**：`v0.1.4` 改的是**机制能否解析这种 token**，不是**生产上有没有这种 token**。`JWT_SECRET` / 调用方 token 的配发属 core QT-71（未落），真进程端到端腿一条都没跑过——三仓守卫用例用的仍是数字 subject + `role=service`。

---

## 维护本文件

- **新增裁决**：写全六字段（事项 / 裁决 / 依据实测 / 落地动作 / 代价 / 落点），并在各仓台账留一行指针。
- **裁决被推翻**：**改本文件，不改历史记录**（保留「原裁决 → 推翻理由」的对照），并同步各仓台账。
- **引用格式**：跨仓引用写 `repo:path`（如 `tellurion:docs/TASKS.md`），不做可点击相对链接。
