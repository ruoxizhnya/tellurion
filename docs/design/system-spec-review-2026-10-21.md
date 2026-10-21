# 系统规格专项审查（2026-10-21）

> 审查对象：12 仓随仓规格件（`docs/{api-schema,dataflow,db-schema,ARCHITECTURE}.md`，共 48 格）+ core 冻结件（`docs/SPEC.md`、`docs/openapi.yaml`）+ `pkg/storage/postgres.go` 内联 DDL + `docs/design/qt-decoupling/` 服务设计篇 + 各仓 `docs/adr/0001-*`。
> 双判据：**合理性**（自洽 / 与裁决一致 / 可实现 / 无过度设计）+ **错漏**（矛盾 / 缺失 / 漂移 / 悬空引用 / 计数不实 / 与自身 ADR 冲突）。
> 分级：Critical / High / Medium / Low，每条带文件+行号、修复建议、per-repo 落点。本报告只登记问题与建议，**不代裁决**——凡归属/语义需拍板的，只立单不改判。
> 执行方式：5 个并行只读审查（T1 盘点 / T2 API 四方对拍 / T3 dataflow / T4 DB 三方对表 / T5 横切），本报告为合成稿；审查阶段零业务代码改动。

---

## 0. 总评

**总体判断：规格体系质量良好，无 Critical；8 项 High 集中在「缺件定口径、3 份 db-schema 事实性张冠李戴、1 处表归属三方矛盾、1 处接口族虚列、1 处服务计数过时、1 处 dataflow 红线缺位」。X 线占位仓的规格件与设计集逐条对拍全部通过；漂移集中在实装面的 agent / ui 两仓与 tellurion-data 的 dataflow。**

确认成立的基线（实测核实，非客套）：

- 42/48 格实存（core 与 tellurion-docs 各豁免 3 件薄规格——2026-10-21 裁决「豁免+声明」，见 H1）；X 线 6 仓四件套齐全且诚实声明「只有占位 doc.go、端点全部未实现」——占位合理。
- **逐 Bar 路径进程内约束全线合规**：10 份 dataflow 无一处把逐 Bar provider/信号/撮合画成跨进程（risk「每笔 CheckOrder 不走网络」、broker「进程内 port」、agent 进程内引擎均合规）。
- connector 时间语义 fail-closed（`ConnectorDeclaration` 漏声明即拒收）、统一归一化规范层、run 级留痕三 hash（orchestration 的 job_id + provenance）均已成文。
- 指数日线独立 `market.index_daily`、OHLCV 按频率分表口径均与 DDL/裁决合规，10 份 db-schema 与内联 DDL **无列名/类型级漂移**。
- 三值 `status`（completed / invalid / failed）口径统一；`degraded/ok` 仅存于带订正横幅的历史注记，属合规。
- `repo:path` 跨仓引用规范干净：docs（除 archive）无旧仓名残留、无相对路径指他仓。
- 10 份 ADR-0001 的上游引用（ADR-D2/D6/D7/D10/D13/D14/D19–D23/D29 及五份 service-design 节号）逐一核实真实存在；12 份 ARCHITECTURE 的边界图景互相自洽，无边界裁决级冲突。

---

## 1. 覆盖矩阵（T1）

| 仓 | api-schema | dataflow | db-schema | ARCHITECTURE | 成熟度 | 备注 |
|---|---|---|---|---|---|---|
| tellurion (core) | — 豁免 | — 豁免 | — 豁免 | ✅ (319 行) | ARCH 具体 | API 真相即 `openapi.yaml`（AGENTS.md:69 自述）；缺件已裁决豁免+声明（2026-10-21，H1） |
| tellurion-contracts | ✅ | ✅ | ✅ | ✅ | 完整具体 | adr/0001 齐 |
| tellurion-kit | ✅ | ✅ | ✅ | ✅ | 完整具体 | 「零表零业务词」红线明确 |
| tellurion-agent | ✅ | ✅ | ✅ | ✅ | 完整具体 | db-schema 有事实错误（H2） |
| tellurion-ui | ✅ | ✅ | ✅ | ✅ | 完整具体 | api-schema 漂移最重（H6） |
| tellurion-docs | — 豁免 | — 豁免 | — 豁免 | ✅ (960 行) | 大而自标失效横幅 | 缺件已裁决豁免+声明（2026-10-21，H1） |
| tellurion-data | ✅ | ✅ | ✅ | ✅ | 具体+诚实占位 | dataflow 红线缺位（H8） |
| tellurion-strategy | ✅ | ✅ | ✅ | ✅ | 具体+诚实占位 | strategy_state 归属争议（H5） |
| tellurion-risk | ✅ | ✅ | ✅ | ✅ | 具体+诚实占位 | audit_logs 表述错（H4） |
| tellurion-analytics | ✅ | ✅ | ✅ | ✅ | 具体+诚实占位 | — |
| tellurion-orchestration | ✅ | ✅ | ✅ | ✅ | 具体+诚实占位 | strategy_state 归属争议（H5） |
| tellurion-broker | ✅ | ✅ | ✅ | ✅ | 具体+诚实占位 | 目标表当现存（H3） |

真相源层级：API 真相 = `tellurion:docs/openapi.yaml`；DB 真相 = `tellurion:pkg/storage/postgres.go` 内联 DDL；冻结契约 = `tellurion:docs/SPEC.md`（ResultSpec/RunSpec/SignalSpec）。**计数实况**：`postgres.go` 内 52 条 `CREATE TABLE`（含 1 条件备份表 `ohlcv_daily_qfq_bak_054` → 51 常驻）、`CREATE SCHEMA` 仅建 4 个不同名（ingest/research/market/quant，quant ×3 重复语句）；另有仓外 DDL：`pkg/eventstore/schema.go`（audit schema + `audit.message_log`）、`pkg/strategy/state_store.go`（`quant.strategy_state`）、`pkg/strategy/signal_store.go`（`quant.external_signals` 重复建表）。**全库实数 = 6 schema（+audit）/ 53 常驻表**，「6 schema/52 表」系巧合凑数，口径不实（M 级）。

---

## 2. Critical

无。

---

## 3. High

### H1 · core 与 tellurion-docs 缺 3 件薄规格，「每仓四件套」口径未定〔已裁决 2026-10-21：豁免+声明〕
- 证据：`tellurion:docs/`、`tellurion-docs:docs/` 均无 `api-schema.md`/`dataflow.md`/`db-schema.md`；`tellurion-docs:docs/REPO-MAP.md:70` 称「每仓都有 docs/{db-schema,api-schema,dataflow}.md」；`tellurion-docs:docs/SERVICE-INDEX.md` 对 core 标「—」由 SPEC 承载（有意留白）。两口径矛盾。
- 修复建议：二选一并成文——① core/docs 补三件薄规格，正文只做指针（指向 openapi.yaml / SPEC.md / postgres.go）；② 在 REPO-MAP.md:70 明确「知识库仓与 core 由真相源直接承载，豁免四件套」。**落点：tellurion / tellurion-docs**。
- **裁决（2026-10-21，用户）**：选②「豁免+声明」——core 与 tellurion-docs 豁免三件薄规格与 `docs/adr/0001-*` 边界裁决单（一并结 L3），API / DB / 流程真相直接由 `tellurion:docs/openapi.yaml` / `tellurion:pkg/storage/postgres.go` / `tellurion:docs/SPEC.md` 承载；声明落地 `tellurion-docs:docs/REPO-MAP.md`「每仓文档入口」。

### H2 · tellurion-agent db-schema 的 schema/DDL 归属张冠李戴
- 证据：`tellurion-agent:docs/db-schema.md:11,21-24`——称实验/研究/基因表「住在 core 的 quant schema」「DDL 在 experiments.go/research.go」「基因表见 migrations/023」。实测：三族表在 public / research schema，DDL 唯一在 `tellurion:pkg/storage/postgres.go:424/930/954`；`migrations/*.sql` 不执行（与该文 §4 自述矛盾）。
- 修复建议：按 postgres.go 实名实 schema 回填，删 migrations 引用。**落点：tellurion-agent**。

### H3 · tellurion-broker db-schema 把目标表当现存事实，且自相矛盾
- 证据：`tellurion-broker:docs/db-schema.md:11,21,51,58`——把 `quant.orders/fills/positions/portfolio_snapshot` 当现存；DDL 无此表，`tellurion:docs/SPEC.md:2025-2033` 明标「尚未实施」。§4/§5 称归属「run 或 exec 待裁」与 §1「归 quant」自相矛盾。
- 修复建议：统一为「目标表·未建」口径，消解 §1 与 §4/§5 矛盾。**落点：tellurion-broker**。

### H4 · tellurion-risk 的 `risk.audit_logs` 迁移表述混淆两种审计
- 证据：`tellurion-risk:docs/db-schema.md:23`——称「从现 audit_logs 迁入」，但现 `audit_logs` 是 ADR-017 鉴权访问审计（user_id/endpoint/method/status_code，`postgres.go:335`），与「规则/审批操作审计」语义与归属均不同。
- 修复建议：拆表或改表述（不得暗示复用鉴权审计表）。**落点：tellurion-risk**。

### H5 · `strategy_state` 归属三方矛盾，需裁决（H1 冻结契约面，勿代裁）
- 证据：`tellurion-orchestration:docs/db-schema.md:9,26` 称唯一写者、迁入 `run`；K0 冻结契约（kernel_modules.schema.sql、`state_store.go:92`）归 strategy-runtime 模块；现实由 core 写 `quant.strategy_state`。
- 修复建议：**先裁决归属再迁**——裁决前不改任何一方正文；裁决后按 per-repo 同步三处并走 H1 评审（涉冻结契约）。**落点：tellurion / tellurion-orchestration / tellurion-strategy（立单待裁）**。

### H6 · tellurion-ui api-schema 的接口族虚列/错列
- 证据：`tellurion-ui:docs/api-schema.md:27`——`sync.ts→/api/data` 族在 openapi.yaml 与 `web/src/api/sync.ts` 双双不存在（实际调 `/api/sync/jobs|schedules|workers|scheduler/stats` + `/api/datasource/*`）。
- 修复建议：改为实际两族。**落点：tellurion-ui**。

### H7 · core ARCHITECTURE 的服务计数过时
- 证据：`tellurion:docs/ARCHITECTURE.md:20`——「compose 只编排 4 个 Go 服务」，与同文件 REV-16（:34 strategy-service 已删）及 docker-compose.yml 实测 3 服务（data/analysis/ai）矛盾。
- 修复建议：改为「3 个」。**落点：tellurion**。

### H8 · tellurion-data dataflow 缺作业层触发链与红线
- 证据：`tellurion-data:docs/dataflow.md:35-39`——写路径只写「更新 sync_jobs/sync_schedules 水位」，未画作业层触发链（`POST /api/sync/jobs`→sync_jobs→worker→executor→ETL，SPEC DATA-10/2356），未写「同步 HTTP 端点不得承载无界批量」红线（SPEC DATA-12/2489）。
- 修复建议：写路径补作业层边 + 红线一句。**落点：tellurion-data**。

---

## 4. Medium

### 引用与真相源卫生
- **M1** `tellurion:docs/SPEC.md:2332,2698,2863`、`tellurion:docs/TASKS.md:629` — 引用「core DATA-13」4 处不带归档路径；该单正文在 `tellurion:docs/archive/TASKS-2026-10-16-archived.md:788`（✅ 2026-10-16，按完成即归档约定不算丢失）— 4 处引用补 archive 指针 — tellurion。
- **M2** `tellurion-ui:docs/api-schema.md:46`、`docs/dataflow.md:40` — 悬空引用 OPS-01（2026-10-14 已改判迁出，archive:1030 有案）— 改指 archive 条目或 AUD-61 — tellurion-ui。
- **M3** `tellurion:pkg/eventstore/schema.go:40`、`pkg/strategy/state_store.go:54`、`signal_store.go:35` — postgres.go 之外另有 DDL（含 `quant.external_signals` 重复建表），「唯一 DDL 真相」声明不实 — 收编进 postgres.go 或修订声明（涉 schema 面，走 H1 评审）— tellurion。

### API 规格漂移（实装面）
- **M4** `tellurion-ui:docs/api-schema.md:25` — `factor.ts→/api/factor` 指错族，代码打 `/api/factors/{factor_name}`（factor.ts:20）— 改 `/api/factors` — tellurion-ui。
- **M5** `tellurion-ui:docs/api-schema.md:23` — 列 `/api/runs`（无消费代码、openapi 无，系 orchestration 目标契约超前登记），漏登记实际调用的 `/api/ohlcv/{symbol}`（backtest.ts:53）— 区分现状/目标并补漏 — tellurion-ui。
- **M6** `tellurion-ui:docs/api-schema.md:47` — 「5xx 展示错误码与 trace id」：错误形状只有 `{error,code}`（openapi `schemas/Error`、`pkg/httpserver/errors.go:163`），无 trace id — 删该词或先入契约（涉契约面，删词为默认）— tellurion-ui。
- **M7** `tellurion-agent:docs/api-schema.md:42`、`tellurion-ui:docs/api-schema.md:37` — SSE 事件名 `close`/`error` 虚列（`handlers_explore.go:439-455` 只 emit `progress`/`complete`；ui 侧无 explore SSE 消费）— 补实现或删事件名 — tellurion-agent / tellurion-ui。
- **M8** `tellurion-agent:docs/api-schema.md:29` — 运维面列根级 `/stats`，`main.go` 只注册 `/health`、`/metrics`（全仓仅 `/api/copilot/stats`）— 删或改指 copilot 族 — tellurion-agent。
- **M9** `tellurion-kit:docs/api-schema.md:15-29` — 12 包表全部未落地（仓内仅 doc.go）却无「实现状态」标注，与 contracts 状态列、X 线占位 banner 口径不一致 — 加状态列 — tellurion-kit。
- **M10** 10 份 api-schema.md — 空集合编码语义（DATA-04：`null` vs `[]`）全员缺位登记（互不矛盾但集体缺失；ui 的 `?? []` 防御亦未入消费面）— DATA-04 定形后各仓补一节 — 各仓（挂 core DATA-04 统一驱动）。

### 命名与路径自洽
- **M11** `tellurion-contracts:docs/api-schema.md:28` — 「D29 §3.2 写成 BrokerPort」节号错，实为 `repo-topology-and-naming.md` §3.3(:122) — 改 §3.3 — tellurion-contracts。
- **M12** `tellurion-broker:docs/api-schema.md:62`、`tellurion-contracts:docs/dataflow.md:15` — 用废止命名 `BrokerPort`，与「以 BrokerGateway 为准」裁决矛盾 — 统一 BrokerGateway — tellurion-broker / tellurion-contracts。
- **M13** `tellurion-orchestration:docs/dataflow.md:31` — `POST /workers/{id}/drain` 与本仓 api-schema.md:41 `POST /api/runs/workers/{id}/drain` 不一致 — 按 api-schema 统一 — tellurion-orchestration。
- **M14** `tellurion-orchestration:docs/dataflow.md:20` — 双内核未成文：RunSpec.Kernel 路由与 NT 侧车（/v1/backtest）在 10 份 dataflow 零出现 — 补内核路由节点或声明由 core SPEC 承载 — tellurion-orchestration。

### dataflow 口径缺位（tellurion-data 集中）
- **M15** `tellurion-data:docs/dataflow.md:22` — 因子定义拉取路径全集未成文（factor_definitions 无对外面；db-schema.md:107 的 core 直读过渡第二路径亦未上图，违反「跨仓交换必能在图上找到一条边」自设规则）— 补因子边 + 直读收敛标注 — tellurion-data。
- **M16** `tellurion-data:docs/dataflow.md:22,31` — 缺 OHLCV 按频率分表与 15m 由 1m 聚合、不物化口径（SPEC:411-413/1065）；本仓 README:45 把「改聚合口径」路由至此文 — 补分表/聚合口径 — tellurion-data。
- **M17** `tellurion-data:docs/dataflow.md:46` — 财报防前视未写死锚点=披露日（ann_date）≠报告期截止日（SPEC:308/597 已冻结）— 补锚点口径 — tellurion-data。

### DB 归属与冻结面标注
- **M18** `tellurion-contracts:docs/db-schema.md:22` — BacktestResult/Trade 落点「owner = orchestration / core」双主，与 orchestration:36、归属表 §1 三处不一致 — 定唯一写者（涉归属，与 H5 一并裁决或立单）— tellurion-contracts。
- **M19** `tellurion-orchestration:docs/db-schema.md:9 vs 18-24` — 头部认领 `strategy_state` 归属表却漏列；`run_artifacts`（dataset_snapshot_hash 等）属 ResultSpec 冻结面未标「变更须走评审」— 补行 + 冻结标注 — tellurion-orchestration。
- **M20** `tellurion-contracts:docs/db-schema.md:23` — ResultSpec 形状未标注 SPEC.md 冻结面/正式变更评审义务 — 补标注 — tellurion-contracts。
- **M21** `tellurion-data:docs/db-schema.md:27` — `backtest_adjustment_anchors` 列为本仓行情单写者，实写者是 core 回测取数路径（`pkg/backtest/adjustment_snapshot.go`），ADR-D19 归属表无此条 — 归属待裁（立单）— tellurion-data。

### ARCHITECTURE / REPO-MAP 横切
- **M22** `tellurion-docs:docs/ARCHITECTURE.md:212,746` — 「strategy-service :8082 备用」仍在端口表与目录结构，与 REV-16（2026-10-17 删除）矛盾 — 删行或加失效注记 — tellurion-docs。
- **M23** `tellurion-docs:docs/ARCHITECTURE.md:1043-1051` — 称「cmd/ai :8086 已删」，与 core/agent ARCHITECTURE（ai-service :8086 复活、源码 tellurion-agent）矛盾 — 加失效横幅/改指针 — tellurion-docs。
- **M24** `tellurion-data:docs/ARCHITECTURE.md:49` — 箭头方向按全仓图例读作「data 调四方」，与本文件 :22 及 REPO-MAP:18 相反 — 反转箭头或注明「供数方向」— tellurion-data。
- **M25** `tellurion-docs:docs/REPO-MAP.md:31-34` — 依赖图「core ←(HTTP)← 六服务」方向与各服务对外面表述冲突 — 统一箭头图例重画 — tellurion-docs。
- **M26** `tellurion:docs/ARCHITECTURE.md:83-88` — §3 分区只列 4 schema，缺 market/audit — 补 — tellurion。
- **M27** `tellurion:docs/ARCHITECTURE.md:157/203/214/232/257/322` — 「ADR-031 D1/D2/D3/D5/D6/D8」借号（实为 `data-factor-unification.md` 决议序列，与 ADR-031 本体 D1–D5 同名异指）— 引用改指 `tellurion-docs:docs/design/data-factor-unification.md Dn` — tellurion。
- **M28** `tellurion:docs/design/qt-decoupling/strategy-loading-externalization.md:4` — 「ADR-027 §833」把行号写成节号 — 改 §2.6.1 — tellurion。

---

## 5. Low

- **L1** `tellurion:docs/TASKS.md:636`、`SPEC.md` 多处 — DATA-01/02/03/05/07/11 等引用同 M1，统一补 archive 指针 — tellurion。
- **L2** `tellurion-docs:docs/TASKS.md:10`、`adr-025/026:6` — 用 GitHub URL 指 core TASKS.md，非 `repo:path` 格式 — 改 `tellurion:docs/TASKS.md` — tellurion-docs。
- **L3** tellurion、tellurion-docs — 无 `docs/adr/0001-*` 边界裁决单（其余 10 仓有）— 补或声明豁免〔已裁决 2026-10-21：豁免+声明，与 H1 同批〕— 两仓。
- **L4** `tellurion-agent:docs/db-schema.md:66` — 表名待 QT-17 回填（有意留待定项）— 开工时回填 — tellurion-agent。
- **L5** `tellurion-data:docs/db-schema.md:45` — 认领 factor_registrations/audit 却漏 `factor_mining_records`（同族治理账本，postgres.go:1073）— 补认领或改归属 — tellurion-data。
- **L6** `tellurion-analytics:docs/db-schema.md:21` — `quant.ic_analysis`/`quant.factor_returns` 前缀错（实为 public 无前缀）— 去前缀 — tellurion-analytics。
- **L7** `tellurion:docs/design/qt-decoupling/project-engineering-file.md:17` 等 — 「52 表/6 schema·已核实」口径不实（见 §1 计数实况）；factor_hypothesis、factor_returns、ic_analysis、factor_mining_records、quant.external_signals、users 六表在 10 份归属表中无主 — 修口径、补认领 — tellurion。
- **L8** `tellurion-ui:docs/dataflow.md:29` — 「提交回测→job_id」未入主链路图 — 补边或注明经 analysis 反代 — tellurion-ui。
- **L9** `tellurion-data:docs/dataflow.md:20,31,53` — 「market/ingest 真相源」以目标态当现状（现多数表落 public，X1d 才迁）；过滤时点同文两写（`current_bar_time` vs `as_of`）；读路径标「热」未声明仅供 run 前快照、禁回测循环内调用（SPEC:1305 红线）— 标注现状/目标、统一写法、补红线 — tellurion-data。
- **L10** `tellurion-docs:docs/REPO-MAP.md:70` — 「每仓都有…」与 core/docs 实际缺件矛盾（见 H1）— 修表述〔已修 2026-10-21：REPO-MAP「每仓文档入口」已改豁免口径〕— tellurion-docs。
- **L11** `tellurion-contracts:docs/adr/0001-契约只增不破且零实现.md:5` 及各仓 AGENTS.md「禁止 replace 指向仓外」— 与 agent 仓「replace ../tellurion 唯一过渡例外」冲突（REPO-MAP:16/43 已认过渡态）— 补例外指针 — tellurion-contracts。
- **L12** `tellurion-contracts:docs/ARCHITECTURE.md:18` — 「被所有仓依赖」与 ui「无 Go 依赖」、docs 纯文档仓不符 — 改「被所有 Go 仓依赖」— tellurion-contracts。
- **L13** `tellurion:docs/design/qt-decoupling/refactor-plan-review-2026-10-20.md:239` — 「degraded 仅剩否定措辞」自检结论与 `p0-round2-review.md:178` 原文残留（带订正横幅）不符 — 修自检措辞 — tellurion。

---

## 6. 已知线索核实结论

1. **「6 schema / 52 表」计数**：口径不实。实数见 §1 计数实况（postgres.go 5 schema/52 条 CREATE TABLE[51 常驻+1 备份]，加 audit schema 与 `audit.message_log`、`quant.strategy_state` 两张仓外表 → 全库 6 schema/53 常驻表；`signal_store.go` 另有重复建表语句）。
2. **DATA-13 悬空引用**：属实但非丢失——单已 ✅（2026-10-16）归档于 `tellurion:docs/archive/TASKS-2026-10-16-archived.md:788`，4 处活文档引用未带 archive 路径（M1，属指针卫生）。
3. **DATA-04 空集合契约族**：各仓 api-schema 全员缺位登记（M10），无相互矛盾；待 DATA-04 定形后统一补节。

---

## 7. 修复落点汇总（per-repo）

| 落点仓 | 条目 | 性质 |
|---|---|---|
| tellurion | H1、H7、M1、M3、M26、M28、L1、L3、L7、L13 | 客观订正为主；M3 涉 schema 面走 H1 评审 |
| tellurion-docs | H1、M22、M23、M25、L2、L3、L10 | 客观订正 |
| tellurion-agent | H2、M7、M8、L4 | 客观订正 |
| tellurion-ui | H6、M2、M4、M5、M6、M7、L8 | 客观订正 |
| tellurion-data | H8、M15、M16、M17、M21、**M24**、L5、L9 | M21 归属待裁（立单不改判）；M24 系 §7 建表时遗漏、2026-10-21 对账补入 |
| tellurion-broker | H3、M12 | 客观订正 |
| tellurion-risk | H4 | 客观订正 |
| tellurion-orchestration | M13、M14、M19 | M19 strategy_state 行与 H5 挂钩 |
| tellurion-contracts | M11、M12、M18、M20、L11、L12 | M18 归属待裁（立单不改判） |
| tellurion-strategy | H5 | 立单待裁 |
| tellurion-kit | M9 | 客观订正 |
| tellurion-analytics | L6 | 客观订正 |
| 各仓 | M10 | 挂 core DATA-04 统一驱动，暂不各自改 |

**需用户裁决项（已全部裁决 2026-10-21，用户拍板；落地见 §8）**：H1 缺件口径=「豁免+声明」（连带结 L3）；H5 `strategy_state` 归属=「归 strategy-runtime」；M18 BacktestResult/Trade 写者=「core 单一写者（orchestration 只读）」；M21 `backtest_adjustment_anchors`=「划归 core 回测域」。上表「待裁」字样以 §8 裁决记录为准。
**冻结 schema 面（走 H1 变更评审，勿直接改）**：M3 DDL 收编、M19/M20 冻结标注类（标注本身不改 schema，可直接补文案）。

---

## 8. 处置记录（2026-10-21，修复执行完毕）

> 执行方式：per-repo 分 3 批派工（每条**先验证是否属实、属实才修**，不属实留证据跳过），只改规格文档、不动业务代码与各仓 `docs/TASKS.md`（台账由主 agent 收口）、不改 `docs/archive/`（历史不改写）。

**四项裁决落地（2026-10-21 用户拍板，均已成文）**：
1. **H1/L3 =「豁免+声明」**：core 与 tellurion-docs 豁免三件薄规格与 `docs/adr/0001-*`，真相源直接承载；声明落地 `tellurion-docs:docs/REPO-MAP.md`「每仓文档入口」+ README + `docs/SYSTEM-DESIGN.md` 括注 + `docs/TASKS.md` DOC-07 豁免注记。
2. **H5 =「归 strategy-runtime」**：`quant.strategy_state` 归 strategy-runtime 模块（K0 冻结契约 `contracts/kernel_modules.schema.sql`），core 继续写、编排非写者；订正横幅落地 `tellurion:docs/design/qt-decoupling/project-engineering-file.md`（ADR-D19）与 orchestration 6 处、contracts/data 归属表。
3. **M18 =「core 单一写者」**：回测结果面（`BacktestResult`/`Trade`）core 单一写者、orchestration 只读；落地 `tellurion-contracts:docs/db-schema.md` 归属行 + ADR-D19 订正横幅。
4. **M21 =「划归 core 回测域」**：`backtest_adjustment_anchors` 归 core 回测域；落地 `tellurion-data:docs/db-schema.md`（摘除认领 + 不认领注）+ ADR-D19 订正横幅。

**「先验证后修」的留存记录（不属实/合理跳过，未改）**：
- `tellurion:docs/SPEC.md:3138` 的「ADR-031 D1」系**真**引用（`tellurion-docs:docs/adr/adr-031` D1 冻结契约表含 kernel 路由字段），非 M27 借号残留——未改。
- M12 行号漂移：`BrokerPort` 实际命中在 `tellurion-broker:docs/ARCHITECTURE.md:48`（审查所引 `api-schema.md:62` 不实），按实况修。
- L4（agent 表名待 QT-17 回填）系有意待定项，合理保留。

**立单登记（3 单，`tellurion:docs/TASKS.md` §九）**：SPEC-R-01（M3：3 处模块内自建表是否收编进 `migrate()`，涉 schema 面走 H1 评审）、SPEC-R-02（L7：factor_hypothesis / factor_returns / ic_analysis / quant.external_signals / users 五张无主表认领；factor_mining_records 已由 data 认领）、SPEC-R-03（DATA-04 新旧同号歧义换号）。M10（空集合语义节）挂现役 DATA-04 定形后统一补节，各仓暂不预写。

**提交纪律**：本批 12 仓修复按仓原子提交（core 先行）；上批遗留 7 文件（QT-08 指针 ×5 + DATA-04 迁移 ×2）先行独立归口提交，不与本批混提。

**全量对账补修（2026-10-21，49 条逐条对账后收尾）**：对账口径为报告 49 条逐条三态核对（✅已修 / ➖合理保留 / ❌未修），结论 46+ ✅、L4 ➖（有意待定项）。对账查出 3 处遗漏并已补修：
1. **M24 ❌→✅**：`tellurion-data:docs/ARCHITECTURE.md:49` 箭头方向当时漏修（§7 落点表建表时亦漏列 M24，均已补）——按 REPO-MAP 箭头图例反转为「四方 ──(HTTP)──► data」并加图例注。
2. **M28 同源残留 ⚠️→✅**：主修（strategy-loading-externalization.md:4）已改 §2.6.1，但「ADR-027 §833」（行号误作节号）在 5 个设计篇另有 6 处残留（strategy-loading-externalization.md:19/:100、backtest-engine-review.md:103、brainstorm-2026-10-19.md:13、strategy-asset-service-api.md:144、target-module-topology.md:69）——统一改 §2.6.1（已验证 ADR-027 `:1010` §2.6.1 真实存在、`:833` 系「六处变化表」行号）。
3. **L2 尾巴 ⚠️→✅**：`tellurion-docs:docs/TASKS.md:10` 措辞「core `docs/TASKS.md`」补改 `tellurion:docs/TASKS.md`（adr-025/026 半批此前已改）；保留完整 URL 作链接目标，兼顾该仓 AGENTS.md「跨仓引用 = 完整 URL」自设约定。
