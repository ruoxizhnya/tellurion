# 裁决建议（逐项）· 2026-10-25

> 配套文件：[跨仓施工分组方案 2026-10-25](cross-repo-dispatch-plan-2026-10-25.md) 第 5 节的 15 条待裁决项。
> 快照时刻：2026-10-25 11:10 之后（证据取自同批实测）。
> 每条给：**建议 → 依据（实测 file:line / 命令输出）→ 若采纳的动作 → 若不采纳的代价**。

---

## 摘要（一页看完）

| # | 事项 | 建议 | 一句话理由 |
|---|---|---|---|
| ① | service token 分发面 | **采纳裁-17 定稿**：专用 `service` 角色 + 长期 token；**部署清单落 core `deploy/k8s/`** | core 是唯一有 `deploy/` 的仓；服务仓零部署资产（实测 5 仓 AGENTS 零命中） |
| ②⑮ | QT-73 五包语义冲突 | **拆分执行**：一期只切 `errors`/`decimal`；`clock`/`logging`/`httpclient` **推到二期** | 三包实测不是「零语义」，硬切会让 QT-73 验收的「全绿」失去意义 |
| ③ | `analysis` 契约包 owner | **不建新包**；DTO 留在 analytics 本仓，**只在「第二个使用方出现」时迁** | contracts ADR-0001 ①：只放被 ≥2 仓使用的类型；core 对 analytics import = 0 |
| ④ | QT-32 是否本代做 | **本代做，但拆两步**：先补 promote 审批留痕，再接线 | 三条依赖已全绿；缺的是闸门留痕，不是管线 |
| ⑤ | `ingest.raw` 残留处置 | **追认删除已执行**（不可回滚），**单独重做 3 笔落账 commit **，不 cherry-pick 那 7 笔 | cherry-pick 实测会撞 `cmd/data/sync_handlers.go`（merge-tree EXIT=1） |
| ⑥ | `fundamentals_detail` 产出方 | **接受「不可得」成文**，但**把请求升级为对外产出协议** | g3 分支的取证扎实（`0 → 5 → 0` 端到端、8 个 field_code 全 miss） |
| ⑦ | `capital_flow`/`top_list` 归属 | **接受「仅储备」**，并**按 g3 的口径订正落账**（`top_list` 在 cron 名单但无排程行） | g3 段落已自带一条口径订正，比台账原文更准 |
| ⑧ | `mining.Proposer` 归属 | **归 agent 仓**，core 只留接口指针 | 台账 ② 已给结论；ADR-023 明禁 core 造第二个编排者 |
| ⑨ | `visible_at` 生产回填授权 | **暂不授权**，等 core 停第二写者先落 | 回填是写生产库；且 PIT 门今天就是按「未回填」设计的 |
| ⑩ | `openapi.yaml` 写入权 | **同意 QT-72 独占写**，但**加一条自检**：写完跑双向对拍 | QT-72 是阶段 B 单，撞车面已最小 |
| ⑪ | U6 / U8 | **U6 暂缓**（不进本轮）；**U8 现在就裁** = 归 `ai.*` | U8 会影响 G6 的 schema 清单，越早裁越省返工 |
| ⑫ | XTP 柜台环境 | **不投人**；G7 降级为「决策材料 + 触发条件」 | 仓内零 SDK（实测），外部条件不归本轮 |
| ⑬ | `producer_pending` 字段名 | **允许 QT-72 自定并两侧同步**；**删掉「等编排」这层** | orchestration 三态已用 `provenance_not_ready`（实测），`producer_pending` 是多余的第四态 |
| ⑭ | promote 审批人身份 | **审批人取 JWT claims（`sub`）**，闸门落在 **agent 仓**、strategy 侧只做状态机 | 现有 `compile_log` 追加日志无法回答「谁批的」 |
| ⑮ | （同 ②） | — | — |

---

## ① service token 的分发面

**建议**：采纳裁-17 已定稿的**专用 `service` 角色 + 长期 access token**（不要复用 `trader`）；**部署清单写在 core 仓 `deploy/k8s/`**，因为那是唯一有部署资产的仓。

**依据（实测）**：
- core `deploy/k8s/` 实测 8 份清单：`analysis-deployment.yaml` / `data-deployment.yaml` / `web-deployment.yaml` / `postgres-statefulset.yaml` / `redis-deployment.yaml` / `configmap.yaml` / `ingress.yaml` / `namespace.yaml`。
- `docker-compose.yml` 实测 3 个服务：`data-service`（:62）/ `analysis-service`（:115）/ `ai-service`（:213）；`JWT_SECRET` 只出现在 `:160`（analysis）与 `:228`（ai）⇒ **data-service 今天没有 secret**。
- **risk / analytics / orchestration / broker / data 五仓的 AGENTS.md 里 `deploy` / `部署清单` / `k8s` / `Dockerfile` 全部零命中** ⇒ 服务仓今天**没有自己的部署面**。
- core `AGENTS.md:232` 目录结构把 `deploy/` 归 core。

**若采纳的动作**：
1. 在 core `deploy/k8s/` 新增 `risk-deployment.yaml` / `analytics-deployment.yaml`（`data-deployment.yaml` 已存在，补 secret 即可）。
2. `JWT_SECRET` 从 `secretKeyRef` 注入（与 `postgres-credentials` 同形），**三个服务 token 各自一个 secret key**（`RISK_SERVICE_RESOLVE_TOKEN` / `DATA_SERVICE_TOKEN` / `ANALYTICS_CORE_TOKEN`），**只经 env 注入、不落库、不进镜像**。
3. `core/docker-compose.yml` 同步给三个服务补 `JWT_SECRET`。

**若不采纳的代价**：core → risk 的 `POST /api/risk/resolve` 按裁-11 口径会让**每一个 run 直接 `failed`**（`risk_rules_unavailable`）——不是降级，是全量阻断。

---

## ②⑮ QT-73 的「零语义五包」与实测冲突

**建议**：**拆分执行**。一期只切 **`errors` + `decimal`**（真零语义）；**`clock` / `logging` / `httpclient` 推到二期**，与 `statistics` / `httpserver` 一起做（届时 kit 的 KT-10 已补齐缺口）。**不要**为了凑「五包」硬切。

**依据（实测）**：

| 包 | core | kit | 判定 |
|---|---|---|---|
| `errors` | `errors.go` 4806 B `sha256=D0A2C645…` | `errors.go` 4806 B `sha256=D0A2C645…` | ✅ **字节相同** |
| `decimal` | `decimal.go` 17702 B `sha256=589480F3…` | `decimal.go` 17702 B `sha256=589480F3…` | ✅ **主文件字节相同**（测试文件不同、kit 多一个 `div_rounding_test.go`） |
| `clock` | `pkg/clock/interfaces.go` 7037 B `sha256=AF3E8AD2E0`，常量 `ModeBacktest`（`interfaces.go:30`） | `clock/clock.go` 6847 B `sha256=161A700110`，常量 `ModeDeterministic`（`clock.go:28`） | ❌ **改名**：core 内 `clock.ModeBacktest` 有 **8 处**引用 |
| `logging` | `pkg/logging/logger.go` 1151 B，第 35 行仍 `SetGlobalLevel(zerolog.DebugLevel)` | `logging/logger.go` 1540 B，第 22 行 `SetGlobalLevel(parseLevel(level))` | ❌ **行为差异**（KT-07 修过的那条缺陷，core 侧仍在） |
| `httpclient` | `pkg/httpclient/client.go` 3829 B，**无** `newStatusError` | `httpclient/client.go` 5582 B，`newStatusError` 在 `:167` | ❌ **行为差异**（KT-04 修过的「4xx 静默当成功」，core 侧仍在） |

**为什么建议拆，而不是「接受行为变更」**：
- `httpclient` 那条**看着安全**（core 侧 `pkg/data/tushare.go` `err != nil` 28 处、`pkg/backtest/engine.go` 46 处、`pkg/marketdata/http_provider.go` 19 处，调用点确实都先判 err），但 QT-73 ④ 自己写的是「**不许顺手改语义（超时、重试次数、日志字段）**」——`httpclient` 的重试次数与 body 重建行为**也在变**（kit 每轮重建 reader，core 旧版复用 reader）⇒ 这不是「顺手」，是**另一张单**。
- `logging` 那条会**改变全仓日志级别**（恒 Debug → 按配置），影响面比 QT-73 大得多。
- `clock` 那条虽然只是改名，但 core 内 8 处 `ModeBacktest` 引用分布在 `pkg/backtest` / `pkg/live` 等执行路径上，改名 + 切包两件事叠在一起，出错时**分不清是改名错还是切包错**。

**若采纳的动作**：把 QT-73 的 ③-① 改为「**只切 `errors` + `decimal`**，另立 QT-73b 一期补三包（`clock` 改名 + `logging`/`httpclient` 行为对齐）」；③-④ 约束改写为「**本单不许改任何行为**」；⑤ 验收改为「`grep -rn '"tellurion/pkg/\(errors\|decimal\)"'` 零命中 + 两个目录不存在 + 全仓绿」。

**若不采纳的代价**：QT-73 ⑤ 的验收是「全仓测试绿 + 破坏验证红」——在**行为已经变了**的前提下，绿不能证明「只是切了包」，破坏验证也定位不到是切包错还是语义错。**这单会变成一笔「说不清改了什么」的 commit**，违反台账自己定的「不许把部分实现写成已完成」。

---

## ③ `analysis` 契约包 owner 与形状

**建议**：**本轮不建 `analysis` 包**。DTO 继续留在 analytics 本仓（`internal/analysis/model.go`），**按 contracts ADR-0001 ① 的判据——「出现第二个使用方时才迁」**——届时再一起迁。

**依据（实测）**：
- contracts `docs/adr/0001-契约只增不破且零实现.md:5` 决策 ① 原文：「**本仓只放被 ≥2 个仓使用的类型 / 接口 / 错误 / 常量**」。
- 实测 core 对 `tellurion-analytics` 的 import = **0 处**；其余 7 仓对 `AnalysisReport` / `PortfolioSnapshot` / `FactorExposure` 的命中：core 的 26 处**全部是 core 自己的同名私有符号**（`pkg/alert/detectors.go` / `manager.go` / `pkg/live/paper_session.go` 的 `portfolioSnapshot`），**不是 analytics 的 DTO**（实测 core 内 `AnalysisReport` **零命中**）。
- contracts 仓当前包清单实测：`dataprotocol` / `domain` / `engine` / `fees` ⇒ **没有 `analysis` 包**。

**若采纳的动作**：analytics 的 R2 单改为「**冻结本仓 DTO 的 wire 形状（`upstream_contract.go` 的 `UpstreamContractVersion = "v1"` 已落）+ 登记第二个使用方触发条件**」；触发条件写死为「**core 或 orchestration 出现 import analytics DTO 的 Go 代码**」——注意 orchestration 的 sensitivity 父作业目前**只经 HTTP** 交互（实测 `internal/upstream/sensitivity.go` 走 `POST /api/runs/parents`），**不是 Go 类型依赖**，**不构成第二个使用方**。

**若不采纳的代价**：contracts 会多一个只有 1 个使用方的包，**直接违反它自己 ADR 0001 ①**；而且 analytics 的 DTO 是**分析结果**（C 类派生计算），按 ADR-0004 的口径，派生计算的 wire 归 `openapi.yaml` 而非 contracts（core 的 `contracts/dto` 是 `oapi-codegen` 生成物，QT-50 ③ 已登记「contracts v0.3.0 无 `dto` 包」）。

---

## ④ QT-32 是否在本代范围

**建议**：**本代做，但拆成两步**：**第一步补 promote 闸门的审批留痕（见 ⑭）**，**第二步做 AI 侧接线**。归属：**AI 生成侧归 agent 仓**（QT-32 ④ 已定「不搬进 core」）。

**依据（实测）**：
- QT-32 ③ 的三条依赖**已全绿**：QT-41b ✅（2026-10-23 首批实装 + 真 TinyGo 0.34.0 已接）、QT-41 ✅（2026-10-24 G4 收口）、QT-03 ✅。
- strategy 仓 promote 端点**已存在**：`internal/httpapi/api.go:46` `POST /api/strategies/{id}/versions/{hash}/promote`；服务层 `internal/asset/service.go:466` `Promote` 带状态机 `StatusValidated → StatusPromoted` 与 `cache.InvalidateLatest(id)` 同刻失效。
- agent 仓 AI 能力**已具备**：`pkg/ai/` 下有 `pipeline` / `evolution` / `gene_pool` / `validator` / `validation` / `search` 等 16 个包。
- **缺的是闸门留痕**，不是管线：strategy 全仓 `approved_by|approver|audit` = **0 命中**；`compile_log` 里只有硬编码的 `"promoted"`。

**若采纳的动作**：agent 仓立 QT-32 六字段单（**台账 QT-32 ③-⑥ 明写「其台账另立单」，实测 agent 台账里没有**）；单里显式引用 `tellurion-strategy:internal/httpapi/api.go:46` 与 `tellurion-strategy:cmd/compiler`（**冻结契约的路径与节号**）。

**若不采纳的代价**：QT-32 ② 原文写着「若产品侧改判『G6/M2 不在本代范围』，则删除本单并在执行顺序总览显式声明划出，**不得沉默**」⇒ **不做也要在总览里显式划出**，不能像现在这样既不删也不做、静默挂着。

---

## ⑤ `ingest.raw` 残留行处置

**建议**：**三段处理**——
1. **追认**：删除**已经发生**（不可回滚），把它作为既成事实写进 main 的台账与 SPEC。
2. **不 cherry-pick 那 7 笔**：改为**在 main 上重做 3 笔纯文档 commit**（DBT-15 / DBT-16 / DBT-17 的落账）。
3. **代码那 4 笔（DATA-14 ×2 + DBT-14 ×1 + G3 决策材料）全部作废**：main 已有自己的等价实现。

**依据（实测）**：
- **删除已执行**（g3 分支 `docs/TASKS.md` DBT-15 施工记录原文）：两条 DELETE 实跑，`DELETE FROM ingest.raw WHERE payload ? 'probe' AND source='akshare';` **影响 164 行**、`DELETE FROM ingest.raw WHERE source LIKE 'unit-test%';` **影响 2 行**；删后 `total=11,968 / probe=0 / unit=0 / tushare=11,968`。
- **计数与 main 台账不符**：main `docs/TASKS.md:1773` 标题写「**155 行**」、③-③ 写「预期 **12,108 → 11,951**」；g3 实测是 **164 行**、`12,134 → 11,968`（g3 分支自己标注了「③-③ 当年预报的 `12,108 → 11,951` 是 2026-10-19 的快照；本轮按**今天的实数**执行」）。
- **cherry-pick 会撞**：实测 `git merge-tree --write-tree main 49a97c7` ⇒ **EXIT=1**，`CONFLICT (content): Merge conflict in cmd/data/sync_handlers.go`。三笔 docs commit 的 `git apply --check --3way` **全部失败**（`docs/SPEC.md:229` / `docs/TASKS.md:1114` / `AGENTS.md:868` 均 patch failed）⇒ **patch 层面也应用不上**。
- **main 已有等价或更新的实现**：
  - DATA-14：main `cmd/data/sync_handlers.go:34` `collectionPayload[T]`（泛型）vs g3 `cmd/data/response_json.go`（257 行的另一套）⇒ **两套并存**，g3 那套作废。
  - DBT-14：main `pkg/data/equitydeep/equitydeep.go:132` `parseFetchedAt`（裁-16 口径：date-only 归一为 00:00Z 日标记）vs g3 `eb33d8d`（另一套「改吃 RFC 3339，契约不动」）⇒ **口径不同**，g3 那笔作废。

**若采纳的动作**：在 main 上新开一笔**只动 `docs/TASKS.md` + `docs/SPEC.md` + `AGENTS.md`** 的落账 commit，内容取 g3 分支的**结论**（不是 patch）：DBT-15 写「经授权按锚点精确删除 164+2 行，删后 11,968、probe=0、unit=0、tushare 未动」；DBT-16 / DBT-17 按 ⑥⑦ 处理。同时**把 g3 worktree 与 `task/g3-data` 分支标为「已结案，可删」**（先确认 `task/g3-data` 已 push 或确认无需保留）。

**若不采纳的代价**：main 台账继续说「DBT-15 未执行任何 DELETE / TRUNCATE」，而**库里那 166 行已经没了** ⇒ 台账与事实直接矛盾，且**没人知道是谁删的、依据哪次授权**。

---

## ⑥ `fundamentals_detail` 的产出方

**建议**：**接受「不可得」成文**（结论正确），但**把 g3 的「产出请求」段落一起搬进 SPEC**——它已把「需要谁产出什么格式」写成了可执行的请求（ndjson 每行的必填形状、`fetched_at` 必须 RFC 3339）。

**依据（实测）**：
- g3 分支 DBT-16 落账原文：「**`fundamentals_detail` 今天仍是 0 行** —— 本单收的是『状态与请求都成文』，不是『有数了』」。
- g3 的取证链完整：真 HTTP 门 → 真库 → PIT 读回 `0 → 5 → 0` 零残留（`1764598`）；Tushare 免费权限的 25 条 raw 的 `fields` 只有 `[ts_code, ann_date, end_date, roe, roa, gross_margin]` 一種形状，与因子需要的 8 个 field_code（`total_revenue` / `operating_cost` / `contract_liability` / `ocf_net` / `net_profit_attr` / `total_assets` / `equity_attr` / `inventory`）**一个都不对得上**，6 名经字典查找全部 miss。
- main 的 SPEC.md 现状：`:1876` 那行仍是「❌ **不可得 → 登记**：依赖 `ingest.raw` 归档快照，当前无 raw 入库 ⇒ 恒 0 行」——**这句在 g3 分支里已被证伪**（`ingest.raw` 实际有 12,108 行、自动归档腿早在生产装配里）。

**若采纳的动作**：把 main `docs/SPEC.md:1876` 那行按 g3 的结论改写（**「阻塞理由证伪并改写」**）+ 追加「`fundamentals_detail` 的产出方」一节；同时把 `AGENTS.md` 那条「基本面深度不足」同步。

**若不采纳的代价**：main 的 SPEC 继续留着一句**已被证伪的阻塞理由**，下一个人会照着它以为「等 raw 入库就行」，而 raw 早就在入库了 ⇒ **调查方向错误**。

---

## ⑦ `capital_flow` / `top_list` 归属

**建议**：**接受「仅储备」**，并**按 g3 的口径订正落账**——g3 段落自带一条台账原文没有的订正，比原文更准。

**依据（实测）**：
- g3 段落原文：「裁决 = **两张都登记为「仅储备、暂无读方」**：不接读方、不建排程。理由就是 ④ 那句『读方必须服务某个研究问题』—— 本轮没有这样的问题，所以不为了『表里有数』造一个读方。」
- **g3 自带一条口径订正**：「工单 ② 写『`top_list` 则**进了** cron 名单』，代码那半对，但它带的推论（⇒ 所以在日更）**不成立** —— `sync_handlers.go:78-79` 那条注释自己写明**名单只影响 `sync_schedules` 的校验，没有任何 schedule 行存在就不会跑**。今天的真实状态是『**在名单里 + 无排程行 = 事实上不日更**』」。
- **实测印证**：`cmd/data/sync_handlers.go:91` `scheduler.RegisterJobType("top_list", sync.JobTypeTopList)` **确实在**（且 `cmd/data/top_list_job_test.go:651` 有测试钉住）；`capital_flow` 确不在名单。main 的 SPEC `:1886` 也记着「该表**零读方**（无 handler / 无因子 / 无前端）⇒ 刻意不进 cron 名单」。
- **g3 的行数实测**：`capital_flow` **18 行**（`source` 单一 = `tushare_moneyflow`，18/18 全 NULL 那些列）；`top_list` **256 行**，`trade_date` 跨 `2026-09-21 .. 09-24`（⇒ 已停更约一个月）。main 的 SPEC `:1886/:1887` 记的也是 18 / 256 ⇒ **行数一致**。

**若采纳的动作**：落账三件事：① 两表登记「仅储备、暂无读方」；② **写清 `top_list` 的真实状态是「在 cron 名单 + 无排程行 = 事实上不日更」**（订正台账 ② 的推论）；③ 保留两族的 NULL 语义与「按 `source` 列区分口径」的警告。

**若不采纳的代价**：台账 ② 继续写「`top_list` 进了 cron 名单」⇒ 下一个人会以为它在日更、进而以为数据是新的（实际停在 09-24）。

---

## ⑧ `mining.Proposer` 归属

**建议**：**归 agent 仓**。core 只保留 `mining.Proposer` 接口作为**冻结契约** + 在 core 跨仓任务索引加一条指针（**不镜像正文**）。

**依据**：
- DBT-18 ② 原文已给结论：「2026-10-09 AI 层整体迁往 `quant-trading-agent` 仓（今 `tellurion-agent`）……**⇒ 本单主体不在 core**」。
- DBT-18 ③ ② 原文：「**若归 agent 仓**，本仓只保留 `mining.Proposer` 接口作为冻结契约 + 在本文件『跨仓任务索引』加一条指针（不镜像正文）」。
- DBT-18 ④：「core 不引入 LLM SDK；**不在本仓实现第二个编排者（ADR-023 禁）**」。

**若采纳的动作**：core `docs/TASKS.md` 的 DBT-18 标 ✅ 并留指针；agent 仓立自己的六字段单（**含 LLM/MTSC Proposer 的实现位置**）。

**若不采纳的代价**：core 会出现第二个编排者，**直接违反 ADR-023**（QT-32 ④ 也重申了同一条）。

---

## ⑨ `visible_at` 生产库回填授权

**建议**：**暂不授权**。等 **core 停第二写者**先落，再一次性授权回填。

**依据（实测）**：
- data 台账原文：「**`visible_at` 生产未回填**：D2 已**只在测试库**经 tagged 套件真跑……**生产库回填仍须显式启动开关或运维执行，未做**。未回填的行会被 PIT 整批拒收（**生产 D2 前序红线：D2 未完成期间读路径不许按 `visible_at` 过滤**）」。
- core 侧第二写者**仍在**：core `pkg/data/source/etl.go` 存在（155 行），第 68 行注释写着 `ON CONFLICT upsert`；`cmd/data/registry_init.go` 实测有 **11 处** `RegisterJobType` ⇒ 同步作业层仍完整在 core。
- 回填脚本的护栏**已就绪且 fail-closed**：`internal/schema/backfill_pg_test.go` 用 `-tags backfill` 标签，只认 `DATA_TEST_DSN` 且库名须 `_test`，生产 DSN 与生产库名**自动 skip**（实测破坏验证腿：「关掉 DSN 的 `_test` 后缀守卫 ⇒ 生产 DSN 被放行 ⇒ 红」）。

**若采纳（暂不授权）的动作**：把「生产回填」登记为**运维动作**而非 agent 动作，并写明前置：core 停第二写者 → 回填 → 才允许读路径按 `visible_at` 过滤。

**若现在就授权的代价**：回填会**改写 12 张 A 集表的生产数据**，而第二写者还在写这两张 schema ⇒ 回填与并发写入可能打架；且**回填后 PIT 门立即生效**，历史上 `visible_at` 为 NULL 的行会**整批拒收**——在没有第二写者停写的前提下，这等于把数据面切到一个未验证的状态。

---

## ⑩ `docs/openapi.yaml` 的写入权

**建议**：**同意 QT-72 独占写 `docs/openapi.yaml`**，并**加一条自检**：写完必须跑既有的「生产路由表 ↔ yaml 双向对拍」（core 已有该机制，QT-50 ③ 提到「纳入既有『生产路由表与 yaml 双向对拍』机制（D7 单一真相源规则）」）。

**依据**：
- 台账 DATA-14 ⑦ 原文：「未改 `docs/openapi.yaml`（已声明数组，**改它会撞 G2 唯一写者**）」⇒ 「G2 唯一写者」这个约束**当时确实存在**。
- **本轮实测**：QT-72 已被排在**阶段 B**（等 QT-71 改完 `route_policy.go` 之后），且 core 的 `docs/TASKS.md` 里 **G2 组的写者已交还**（G13 协调/发版收口 ✅ 2026-10-24）⇒ **当前没有活跃的 G2 写者**。
- `check_doc_links.py` 实跑 **EXIT=0**（扫 50 个 Markdown，零坏链）⇒ yaml 不是链接检查的活跃层文件。

**若采纳的动作**：QT-72 验收加一条「`python tools/check_doc_links.py` 退出 0 + 生产路由表 ↔ yaml 双向对拍绿」。

**若不采纳的代价**：QT-72 ③-⑤ 要求「openapi 登记端点与三态」但没写权 ⇒ agent 可能改了却不知道撞谁，或干脆不登记 ⇒ **wire 真相源与代码分叉**。

---

## ⑪ U6 / U8 两条未裁边界

**建议**：**U6 暂缓**（不进本轮任何单）；**U8 现在就裁 = 归 `ai.*`**，并同步撤 `tellurion-data` 的认领行。

**依据（实测）**：
- `repo-inventory.md` §3.1 原文：U6「倾向**先作 `tellurion-data` 内的独立包**，做文本降维时再升格；**代价**：§2.6.2 的『资源特征不同 / 失败可隔离』拿不到」；U8「三方相反：ADR-027 §4 B 类 = ai-service；SPEC-R-02（2026-10-21 ✅）= tellurion-data；QT-17 ⑥ =『按 QT-40』。**倾向归 `ai.*`**（它是 ADR-023 的实验日志，不是仪器域数据）」。
- U8 的影响面：台账原文「影响 QT-40 X1d 的 schema 归属清单，与 QT-17 的 `ai.*` 表名单（二者会各自把它写进自己的迁移）」⇒ **正好命中 G6（data）的 X1d**。
- **U7 已于 2026-10-24 裁为「拆分」**（risk 台账文首已同步）⇒ 不需要再动。

**若采纳的动作**：U8 落一条裁决记录（`repo-inventory.md` §3.1 表 + `tellurion-data:docs/db-schema.md` §1.3 撤认领行 + `tellurion-agent` 的 `ai.*` 表名单加一行）；U6 在 §3.1 标「本轮不裁」。

**若不采纳的代价**：U8 不裁 ⇒ G6 的 X1d schema 清单**可能要写两次**（data 与 agent 各自写进自己的迁移）⇒ 迁移时对撞。

---

## ⑫ XTP 柜台环境

**建议**：**本轮不投人**。G7 降级为「**决策材料 + 触发条件**」，只出材料不出施工步骤。

**依据（实测）**：
- broker 仓 `internal/gateway/xtp.go` 实测**零 SDK import**（`import` 段只有标准库 + 本仓包；`:16` 的 `ErrAssemblyIncomplete` 是自己的哨兵）⇒ 仓内**没有 XTP SDK**，无法实现「从零接 SDK」。
- 全 workspace 4 层深度内 grep `xtptrader|xtp-api|github.com/xtp` 只命中 broker **自己的** `mock.go` / `ports.go` / `xtp.go`（自研骨架），**无第三方 SDK**。
- broker 台账原文：「**XTP 全量适配器接入这份规则（归 X6c）**」「**仍是 🔶**：X6c 全量适配器（下单 / 撤单 / 查单 / 回报 / 心跳 / 重连 / 限流）需要真实 XTP SDK 与柜台环境，**仍从零未做**」。
- 台账已有 X6d 的触发式处置先例：「**X6d 裁决：暂缓**（2026-10-23）—— 仓内文档明写它是触发式选项（多账户共用通道 / 通道级高可用），当前单账户单实例，**触发条件未到**」。

**若采纳的动作**：G7 只出「X6c 的接口对表 + SDK 选型 + 触发条件」材料，**登记在 broker 台账 §二 跨仓指针**，不进任何施工组。

**若投人的代价**：会做一个**永远无法验证**的实现（无 SDK、无柜台）⇒ 产出的是无法自证的代码，违反「验收必须可复现」。

---

## ⑬ `producer_pending` 字段名

**建议**：**允许 QT-72 自定并两侧同步**；并且**删掉「等编排」这一层**——`producer_pending` 这个第四态是多余的。

**依据（实测）**：
- core QT-72 ② 原文：「orchestration 已有 `GET /api/runs/{id}/provenance` + `ArtifactStore`，但**生产者未落**……**裁-19 定稿**：读面落 core；`run_artifacts` 重新定义为『worker 写的投影』，**core 不做双写**」⇒ **core 的读面与 orchestration 的 worker 投影是两个东西**，core 的端点直接读 `quant.run_provenance ⋈ backtest_jobs`（**QT-63 的表，core 自己的**）。
- orchestration 侧的三态实测在 `internal/httpapi/provenance.go`：`:76-78` 注释写明三态 = 409 `provenance_not_ready` / 404 `provenance_not_found` / 500 `provenance_missing`；`:113-130` 是对应实现。**没有第四态**。
- 实测 orchestration 全仓 `producer_pending` = **0 命中** ⇒ 这个字面量**今天在任何仓都不存在**。

**若采纳的动作**：QT-72 直接沿用 orchestration 的**三态**（`provenance_not_ready` / `provenance_not_found` / `provenance_missing`）作为 DTO 对拍锚点，**不引入 `producer_pending`**；core 的端点**不依赖** orchestration 进程（它是 core 自己的进程内读面）。

**若不采纳的代价**：QT-72 施工时找不到锚点 ⇒ 要么自造一个两边都不认的第四态（wire 分叉），要么停下来反复问。

---

## ⑭ promote 闸门的审批人身份

**建议**：**审批人取 JWT claims 的 `sub`**，**闸门落在 agent 仓**（agent 是 AI 生成侧、审批是人的动作），**strategy 侧只保留状态机**（`Promote` 端点已存在，但**要加一个必填的审批人入参**并落进 `compile_log` 或新表）。

**依据（实测）**：
- strategy 侧现状：`internal/httpapi/api.go:46` 端点在、`internal/asset/service.go:466` `Promote` 在、状态机 `StatusValidated → StatusPromoted` 在、`cache.InvalidateLatest(id)` 同刻失效在。
- **但审计维度缺两项**：strategy 全仓 `approved_by|approver|audit` = **0 命中** ⇒ **无审批人字段、无独立审计表**；`internal/asset/pgstore.go:393` `Promote(...)` 的 `log` 参数在 `service.go` 调用处**硬编码为 `"promoted"`** ⇒ 落进 `strategy.strategy_artifacts.compile_log` 的只有「结论」，**没有「谁批的」**。
- core 的既有先例：**审计 actor 从 claims 取**（risk 仓的 `actorOf` 从 claims 取、缺失退 subject；core 的 `pkg/auth` 有 `sub` claim）。

**若采纳的动作**：
1. strategy 侧 `Promote` 端点增**必填**的审批人入参（**不许 caller 自报身份** —— 沿用 risk 仓 `Actor` 字段用 `json:"-"` 防自报的做法，或直接从 JWT claims 取）。
2. 落库：进 `compile_log`（追加式，已有机制）或新开一张 `strategy.strategy_promotions` 审计表（**留痕三件套：审批人 / 时间 / 结论**）。
3. agent 仓 QT-32 单里把「promote 人工闸门」写成**必经步骤**，且验收含「未过 promote 的 AI 产物被回测路径拒绝（fail-closed 用例）」。

**若不采纳的代价**：QT-32 ③-② 要求的「闸门记录进审计，审批人/时间/结论留痕」**只落三分之一** ⇒ AI 产物的生产责任链断在「谁批的」这一环，日后无法追责。

---

## 附：按「要不要你现在拍」排序

**必须现在拍（否则卡住阶段 A/B 的开工）**：
- **②⑮** QT-73 拆不拆（一期 2 包 vs 5 包）—— 不拍，G1 不知道怎么做第一步。
- **⑤** DBT-15 追认 + 重做落账 —— 不拍，main 台账继续与事实矛盾。

**开工前拍（卡某一组的第一步）**：
- **③** analysis DTO 归属（卡 G5 的 R2）
- **④⑭** QT-32 范围 + 审批人身份（卡 G8 的立单与施工）
- **⑬** `producer_pending`（卡 G1 的 QT-72）

**本轮内拍（不卡开工，但本轮该定）**：
- **①** 部署清单归属（卡 G1 的 QT-71 ③-⑦ 与 G4）
- **⑦** DBT-17 落账口径（卡 G6 的 schema 清单与 ⑪ 的 U8）
- **⑥** DBT-16 结论（与 ⑤ 同批落账）
- **⑩** openapi.yaml 写权（卡 G1 的 QT-72 验收）
- **⑨** 生产回填授权（**建议明确「暂不授权」**，避免被当成默认已批）

**可以不拍（本轮不阻塞）**：
- **⑧** DBT-18 归属（有明确倾向，照做即可）
- **⑪** U6（建议标「本轮不裁」）
- **⑫** XTP 环境（建议标「本轮不投人」）
