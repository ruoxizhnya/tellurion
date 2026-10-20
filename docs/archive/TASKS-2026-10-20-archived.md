---
status: archived
last-verified: 2026-10-20
verified-by: 第 3 代台账（DBT-13~18，6 项，全 ⬜）整体归档（2026-10-20 换代）。归档时逐条复核前提：DBT-13（parseEastmoneyDate 回落 time.Now() 仍在 :725；juchao 两处调用仍在 :238/:241）、DBT-14（schema 仍声明 date-time、实现仍只接 2006-01-02）、DBT-17（capital_flow 仍无读方）前提均成立，已迁入第 4 代；DBT-15/16/18 需真库或跨仓动作，原样迁入并标注复核方式。
---

# TASKS — 第 3 代（DBT-13~18）归档

> 归档于 2026-10-20（第 4 代换代）。本代 6 项**全部未完成**，逐条复核前提后**原样迁入** `../TASKS.md`，未改写正文（归档不可变）。
> 迁入去向：DBT-13/14/15/16/17/18 → 第 4 代「存量任务（延续）」节，ID 不变。
> 本代新增的「证伪台账」「收口记录」保留在本文件，第 4 代只引用不复制。

---


# TASKS — 未完成项

> **只放未完成项**，完成即标 ✅（附日期）并移入 `archive/`。历史代：[archive/TASKS-2026-10-14-archived.md](archive/TASKS-2026-10-14-archived.md)、[archive/TASKS-2026-10-16-archived.md](archive/TASKS-2026-10-16-archived.md)（第 1 代 35 项）、[archive/TASKS-2026-10-16-gen2-archived.md](archive/TASKS-2026-10-16-gen2-archived.md)（第 2 代 27 项，追加式滚存 + 2026-10-19 状态标记订正，正文未改）。
> 状态标记：`⬜ 待做` / `🔶 进行中` / `⛔ 阻塞（有未解除前置）` / `✅ 完成（附日期）`
> 每张 task 必须写全六字段（目标/上下文/要求/约束与非目标/验收/边界），判据见 [AGENTS.md §8.5](../AGENTS.md)：**独立 agent 拿着 task + 它显式引用的冻结契约，不需要读其他 task、不需要问人，就能完成并自证。**

---

## 执行顺序总览

| # | 任务 | 级别 | 依赖 / 排序理由 |
|---|---|---|---|
| 1 | DBT-13 `parseEastmoneyDate` 静默回落今天 | P1 | 污染的是**日期字段**本身；DBT-14/16 的落库读数都要先假设它诚实 |
| 2 | DBT-14 snapshot 契约漂移裁决 | P1 | 只改一边、且需主任裁决；与 1 无文件冲突可并行 |
| 3 | DBT-15 `ingest.raw` 历史探针 residue 处置 | P2 | 一行 DELETE + 复核；写库需授权，不依赖代码 |
| 4 | DBT-16 EquityDeep snapshot 生产者 | P2 | 前置：DBT-14 定契约形状；主体在 vault/EquityDeep 侧，core 只有门 |
| 5 | DBT-17 `capital_flow` / `top_list` 读方决策 | P2 | 前置：两表今日已有真数据（18 / 256 行），"零读方"才有得讨论 |
| 6 | DBT-18 mining 真实 Proposer | P3 | 跨仓（AI 层在 `quant-trading-agent`），本仓只留指针 |

**并行指引**：1 与 2 文件域不重叠（`pkg/data/source/eastmoney_sectors_adapter.go` vs `contracts/snapshot.schema.json` + `pkg/data/equitydeep`）；3 是运维动作不是代码；4/5 各自独立。撞文件的按序号串行，一单一 commit。

---

## 一、证伪台账（旧登记与事实不符，已订正；写新工单前先核这里）

> 规矩来源：AGENTS.md §8.5.1——**前提错了不是 agent 的错，是工单的错**。本节只记「谁被证伪、证据是什么」，被订正的原文不改写（归档不可变）。

| 旧登记 | 实测结论 | 证据 |
|---|---|---|
| DBT-07：「`fundamentals_detail` 依赖 `ingest.raw` 归档快照，当前**无 raw 入库**（`EQD-P1-2` 待做）」 | **两句皆假** | ① `ingest.raw` 有 **12,108 行**，自动归档腿早在生产装配里：`pkg/data/tushare.go:220`（`call()` 内、`Code==0` 之后）→ `pkg/data/tushare_raw.go:31 archiveRaw` → `pkg/storage/ingest_raw.go:50 SaveRawIngest`，跑任一 Tushare 同步即落快照；② EQD-P1-2 已于 `7b5dd8e`（2026-09-15，v3.30.0）结案，门/字典/normalizer/五个纵向因子读方全在册（`cmd/data/main.go:130`、`cmd/data/setup.go:197`、`pkg/data/equitydeep/snapshot.go:98`、`pkg/data/factor_equitydeep.go:398+`） |
| DBT-07：「`top_list` …**无 producer**」 | **不准确** | `EastmoneyTopListAdapter.fetchTopList` 自 ODR-011 就在册（`pkg/data/source/eastmoney_sectors_adapter.go:440`，注册 `cmd/data/registry_init.go:76`，链 `:262`）；缺的一直是**作业层那一段**（job_type 枚举 + executor + Normalizer + 类型化门 case），已由 DBT-10 补齐。`eastmoney_adapter.go:86-89` 那句「Sprint 2+ will add top_list」是**过期注释** |
| DBT-07 遗留：「`TestAdjustmentBasis_QfqLookAheadLeaksIntoBacktest` 基线既有红，归因需重新取证」 | **不是回归、也不是"前视不漏"——是判据钉错了对象** | 2026-10-19 重跑取证：原判据把「实质性」钉死在 `@1e6` 这一个**敏感度谱之外**的点上（该池 18 只票仅 1 只一手超预算）；扫过翻面点后 `@3.48e4` 差 **3426.87pp**、`@7.50e4` 1026.48pp、`@1.62e5` 25.31pp。已重写为存在式判据并转绿（`a918358`），详见下方收口记录 |
| 台账引用 `7780236`（REV-14）、`7118691`（REV-16）未注明归属仓 | **这两个 hash 在 core 查无此物** | `git cat-file -t 7780236` 在 core 报 "Not a valid object name"；它们是 **`quant-trading-docs` 仓**的提交（`7780236 docs(ARCHITECTURE): 拆仓后状态横幅…（REV-14，docs 段）`、`7118691 docs(adr-012): 关闭 standby 裁决…（REV-16，docs 段）`）。第 2 代其余 15 个 hash 已用 `merge-base --is-ancestor` 逐个验证为 main 祖先。**订正方式**：在归档头部说明，不改写已归档条目正文 |

---

## 二、第 3 代任务

### [1/6] DBT-13 · `parseEastmoneyDate` 解析失败静默回落「今天」[P1] ⬜

- **① 目标**：eastmoney 族任何一行数据，落库日期要么是真值、要么当场失败——**不存在"读不懂就当今天"** 这条路径。
- **② 上下文**（DBT-10 真源腿实测，2026-10-19）：`pkg/data/source/eastmoney_sectors_adapter.go` 的 `parseEastmoneyDate` 末尾 `return time.Now().UTC()`。它原先只认三种纯日期形状（其中 `"2006-01-02"` 还重复列了一次），而 datacenter 的 `RPT_DAILYBILLBOARD_DETAILS` 实际返回 **带时刻**的 `"2026-09-24 00:00:00"` ⇒ 龙虎榜**每一行**都掉进那个回落、落库日期全变成"今天"。DBT-10 已补齐带时刻两种形状并在 top_list 路径上加了「TRADE_DATE 缺失/空串当场失败」（`:506` 附近），但**回落本身仍在**，因为还有两个调用方共用它：`pkg/data/source/juchao_adapter.go:238`（传的是 `adjunctUrl`，注释自陈 placeholder）与 `:241`。把回落改成零值会连带改变巨潮那条链的行为——**不在 DBT-10 的文件边界里，故另立本单**。
- **③ 要求**：① 给"读不懂"一个显式出口（推荐 `parseEastmoneyDateStrict(v) (time.Time, bool)`，非严格版保持现行为不动，逐调用方迁移）；② 迁移 `juchao_adapter.go:238/241` 两处，逐处写明"这里读不懂时应该发生什么"（拒收该行 / 用别的字段兜），不许再用"今天"兜；③ 全仓 grep 其余 `time.Now()` 作为**解析兜底**的同类形状，登记或一并收口；④ 测试：不可解析输入 → 严格版返回 `ok=false`，且真库落库日期等于源给的日期而非当天（用固定历史日期的夹具，别用"今天"）。
- **④ 约束与非目标**：不改各 adapter 的业务语义与字段映射；不动 `ohlcv_daily_qfq` 存量数据；不顺手把 eastmoney 族的 filter/pagination 重构（DBT-10 已收口的部分不许回退）；不改 `internal/repoguard` 白名单形状。
- **⑤ 验收**：`go test ./pkg/data/source/ -count=1` 绿；有一条断言能区分「窗口外日期」与「日期读不懂」两种失败（报文不同、测试各自钉住）；**破坏验证**：把回落改回 `time.Now()` ⇒ 至少一条测试红 ⇒ 还原（判据：文件 sha256 与改前相同）。
- **⑥ 边界**：只做"日期解析失败的处理"这一件事；字段口径、单位换算另单。

### [2/6] DBT-14 · EquityDeep snapshot 契约漂移：`fetched_at` 声明 date-time、实现只收日历日 [P1] ⬜

- **① 目标**：契约（`snapshot.schema.json`）与实现（`pkg/data/equitydeep`）对 `fetched_at` 的形状**只有说法一种**，且该说法被测试钉住。
- **② 上下文**（DBT-11 实测逮到，2026-10-19，已用测试钉住现状未改任何一边）：`contracts/snapshot.schema.json` 把 `fetched_at` 声明为 `format: date-time`，而实现只收 `YYYY-MM-DD`（`pkg/data/equitydeep/equitydeep.go:102`）——**严格按契约发 date-time，整条请求 400**。钉子是 `pkg/data/equitydeep` 里新增的 `TestNormalize_ContractDateLayoutIsCalendarDateOnly`。同处另一条较弱的事实：`DecodeSnapshot` 不校验 schema 的 `required` 列表，靠 `Normalize` 兜住（也已钉）。
- **③ 要求**：① **先由主任裁决改哪一边**（改实现以吃 date-time / 改契约声明成 `format: date` / 两者都吃但落库归一）；② 按裁决落代码 + 更新契约文件或实现；③ 同步 SPEC 的 citation/snapshot 形状节（`docs/SPEC.md` 的 EquityDeep 摄入契约处）；④ 若选"两者都吃"，必须明确 PIT 语义不受影响：`ann_date` 才是 PIT 锚点，`fetched_at` 只是同键多版本的排序位。
- **④ 约束与非目标**：不改 `ann_date` 的 PIT 语义（`GetFundamentalsDetailAsOf` 强制 `ann_date<=$1` 是本表存在的理由）；不动 `field_dictionary.yaml` 的白名单内容（raw_names 变更属兼容性决策，文件头已注明需与 `evals/data_quality/spot_check.py` 同步）；不改 `pkg/domain` 字段（`pkg/domain/spec_drift_test.go:190` 会双向钉 SPEC 锚点）。
- **⑤ 验收**：契约与实现各有一条断言且互不矛盾（同一形状在两侧结论一致）；`go test ./pkg/data/equitydeep/ ./cmd/data/ -count=1` 绿；**破坏验证**：按裁决后不该接受的形状发一条请求 ⇒ 必须被拒且原因可检索。
- **⑥ 边界**：只做 `fetched_at` 这一处形状漂移；其余 schema/实现不一致的发现列进本单正文附列，不顺手改。

### [3/6] DBT-15 · `ingest.raw` 里 155 行历史探针假归档的处置 [P2] ⬜

- **① 目标**：证据表 `ingest.raw` 里没有**明知是假**的归档行；或它们的存在被成文承认并解释为何无害。
- **② 上下文**：`cmd/data/handlers_ingest_test.go` 的归档测试每跑一次往真库落一条探针假数据（payload 含 `probe` 键、`source=akshare`、`dataset=akshare.stock_financial_abstract`，与真表用过的名字相同），历史上从不删——库里已积 **155 行**（`select source, count(*) … where payload ? 'probe'` 实测：akshare 155/155）。另有 2 行 `source=unit-test*`。该测试的**自清理已于 2026-10-19 修好**（`e23ddec`，按本 run 的 content_hash 精确删 + 读回复核），但**既有 residue 不是我今天写的，删别人写进共享库的行需要单独授权**，故只登记不擅动。DBT-11 那次「`fina_indicator` 到底返回哪些字段」是**纯读这张表**做的取证——脏读数风险的形状就是这样一个具体例子。
- **③ 要求**：① 由主任决定：删除 / 保留并成文标注为演示 residue；② 若删，语句已备好，按可识别锚点精确删，不用全表 DELETE：`DELETE FROM ingest.raw WHERE payload ? 'probe' AND source='akshare';` 与 `DELETE FROM ingest.raw WHERE source LIKE 'unit-test%';`；③ 删前后各读一次 `count(*)`（预期 12,108 → 11,951）并留证；④ 把处置结论写进 SPEC 的 `ingest.raw` 节（A 类原始源响应，唯一归属）。
- **④ 约束与非目标**：**绝不做** `TRUNCATE` 或无条件 DELETE；不碰 `tushare.*` 那 11,932 行真归档（它们是证据 API 的返回源，且被 DBT-11 的取证读过）；不改测试去"绕过"这个问题（测试侧已修）。
- **⑤ 验收**：处置结论成文（SPEC 一句话）+ 若删则前后读数与 `count(*) filter (where payload ? 'probe')=0` 的复核。
- **⑥ 边界**：只处理 `ingest.raw` 的 residue；其他表的测试残留若顺带发现，登记不处理。

### [4/6] DBT-16 · EquityDeep snapshot 的**生产者**（`fundamentals_detail` 唯一可行灌数路径）[P2] ⬜

- **① 目标**：`fundamentals_detail` 有真实数据来源，或明确成文「在本仓可及的源之内不可得，需要谁产出什么」。
- **② 上下文**（DBT-11 实测，2026-10-19）：门与读方都验证过了（真 HTTP 门 → 真库 → PIT 读回，`0 → 5 → 0` 行无残留，`1764598`）。缺的是**没人往门里投 ndjson**：`DecodeSnapshot` 的生产唯一调用点在 `cmd/data/handlers_equitydeep_ingest.go:116`，等的是外部 EquityDeep 产出。而 **Tushare 免费权限给不出所需字段**——纯读库取证（0 次 API 调用）：`tushare.fina_indicator` 全仓 25 条 raw 的 `fields` 只有一种形状 `[ts_code, ann_date, end_date, roe, roa, gross_margin]`，与因子需要的 `total_revenue / operating_cost / contract_liability / ocf_net / net_profit_attr / total_assets / equity_attr / inventory`（`pkg/data/equitydeep/equitydeep.go:41-49`）一个都不对得上，这 6 个名字经字典查找全部 miss。⚠️ **本单不许用编造数据灌数**：那五个因子会直接读到假财务数值。
- **③ 要求**：① 先定产出方——EquityDeep 侧（vault 研究产出的投影）还是别的可及源；② 若要 core 提供**投递工具**（把一份已合法的 snapshot 文件推到门上，含 `POST /api/ingest/raw` 先归档再引用 content_hash 的两步顺序），实现放 `cmd/data` 子命令，形状照 `mining` 子命令（`cmd/data/mining_cmd.go`，DBT-04 的先例）；③ 数据到位后跑一次真灌数并留前后读数；④ 拿不到就成文「不可得 + 需要谁产出什么格式」，并把那五个因子的降级语义（`ErrNoFundamentalsDetail`，`pkg/data/equitydeep/equitydeep.go:67`）在 SPEC 与 AGENTS §14 说清。
- **④ 约束与非目标**：不改字典白名单内容去"迁就"某个源；不改因子数学定义（D3 冻结）；不在 core 侧实现 EquityDeep 研究逻辑（ADR-023：EquityDeep 是**数据底座**，不是第二个 agent）；不新增外部依赖不报批。
- **⑤ 验收**：要么 `fundamentals_detail` 有真数据且有读方能读到（前后行数 + 一条 PIT 读回留证），要么「不可得」成文且 SPEC/AGENTS 两处口径一致。
- **⑥ 边界**：只做"数据从哪来"；`capital_flow`/`top_list` 的读方是 DBT-17。

### [5/6] DBT-17 · `capital_flow` / `top_list` 零读方——接上还是明确为数据储备 [P2] ⬜

- **① 目标**：两张今天已有真数据的表，有一个**说得出口的用途**：要么被读（因子/API/前端任一处），要么成文声明为"仅储备、暂无读方"并解释为何值得占配额。
- **② 上下文**：DBT-09/10 之后 `capital_flow` 18 行、`top_list` 256 行（真库实测，`1a1bb38`）。接线期间的 grep 结论是 **`capital_flow` 无任何读方**（无 handler / 无因子 / 无前端读它），这也是 DBT-09 决定**不**把它放进 cron 名单的理由——"每天烧一整桶免费配额喂一张没人读的表"。`top_list` 则**进了** cron 名单（整市场按日翻页，1–2 发请求，日更成本可忽略）。另需注意 DBT-09 的口径代价已登记：表内数值现在是 **Tushare 分档成交额**形态，`main_net`/`main_buy_amount`/`main_sell_amount`/两个 ratio/`retail_*`/`close_price`/`change_pct` 本源不给 ⇒ 落 NULL，**两族口径混在同一张表里靠 `source` 列区分**，任何读方按东财含义解释这些列之前必须先读 `source`。
- **③ 要求**：① 决定用途（候选：作为横截面因子进 `pkg/data/factor*.go` / 只进 API 读端点给人看 / 明确"储备"并停止日更讨论）；② 若接因子，走 D3 冻结的定义流程 + `factor_cache` 归属（Redis，C 类派生计算）；③ 无论哪种，SPEC 的数据缺口表要把两表的「行数/源/口径/读方现状」写全，NULL 列的含义写清（缺失≠0）。
- **④ 约束与非目标**：不为"有数据"而造读方（读方必须服务某个研究问题）；不改四契约签名（ADR-031）；不接新源（DBT-16/§14 的 D5 样板另议）。
- **⑤ 验收**：结论成文（SPEC 表格 + 一条 ODR 或台账段说明裁决理由）；若接了读方则有该读方的测试与一次真实读数；若判"储备"则 `scheduler.RegisterJobType` 与 cron 名单的状态与结论一致。
- **⑥ 边界**：只做"谁来读/要不要日更"；数据获取本身（DBT-09/10）已收口。

### [6/6] DBT-18 · mining 的真实 Proposer（LLM/MCTS 候选生成）[P3] ⬜（跨仓，本仓只留指针）

- **① 目标**：`cmd/data mining` 的候选来源从 JSON 文件升级为真实候选生成器，或明确该能力归属 `quant-trading-agent` 仓。
- **② 上下文**：DBT-04 落地时用的是 `fileProposer`（候选 JSON 文件 → `mining.Candidate`），完成记录里写明「真实 LLM/MCTS Proposer 仍属另立工单」，且其非目标已排除「不接 AI 挖掘消费方（D8 治理层之外）」。2026-10-09 AI 层整体迁往 `quant-trading-agent` 仓（ADR-027 §5 第 8 步前置切片），`pkg/ai` + 四族 AI handler 现在住那边，依赖方向单向 agent→core（repoguard 钉住）。⇒ 本单**主体不在 core**。
- **③ 要求**：① 由主任裁定归属；② 若归 agent 仓，本仓只保留 `mining.Proposer` 接口作为冻结契约 + 在本文件「跨仓任务索引」加一条指针（不镜像正文）；③ 若要求 core 提供候选投递 API，则写清端点与鉴权（走 `route_policy` 现有策略）。
- **④ 约束与非目标**：core 不引入 LLM SDK；不在本仓实现第二个编排者（ADR-023 禁）；不改 `Pipeline` 签名。
- **⑤ 验收**：归属结论成文 + 指针登记；跨仓那半在其归属仓台账里写全六字段。
- **⑥ 边界**：只做归属与契约指针；实现细节属 agent 仓。

---

## 三、本代收口记录（第 2 代遗留的兑现，2026-10-19）

> 五项全部由**派工单**驱动（DBT-08~12），完成判据是「可机械执行的读数」，不是主观"做完了"。环境事实先记一条：本机 PG/Docker 引擎当时都**没在跑**，栈由 `tools/local-infra.sh` 的姿势（沙箱里必须直接 exec，`nohup &` 会被回收）拉起后才拿到全部真库证据。

| 单 | 交付 | commit | 关键读数 |
|---|---|---|---|
| **DBT-09** | `capital_flow` 链首选换成 Tushare `moneyflow`（eastmoney 降兜底），fetch/normalize/落库 + ETL 白名单第二条 permit | `1a1bb38` | `capital_flow` **0 → 18 行**（000001.SZ/000002.SZ × 2026-09-14~09-24）；`source=tushare_moneyflow`；本源给不出的列实测 **18/18 全 NULL**（`main_net`/`close_price`），非 0 |
| **DBT-10** | `top_list` 接进作业层（job_type + params + executor + Normalizer + 类型化门 case）；顺带修两处真源腿逮到的缺陷 | `1a1bb38` | `top_list` **0 → 256 行**（2026-09-21~09-24）；后缀核对 200 行、`net_buy ≤ buy+sell` 量纲核对 200 行。**修的两处**：① filter 语法——东财认 `(A)(B)` 串联，不认 `(A) and (B)`，后者它**不报错、整条当没看见**（真源对照：and 版返回 2026-10-19/pages=372，串联版返回 2026-09-24/pages=100）⇒ 窗口外三周的数据照样落库、作业照样 completed；加了一道与语法无关的兜底 `checkTopListWindow`（越界一行都不交出）。② `parseEastmoneyDate` 不认源实际返回的 `"2026-09-24 00:00:00"` ⇒ 每行都掉进"回落今天"，落库日期全成今天（补齐形状；回落本体仍在，见 DBT-13） |
| **DBT-11** | EquityDeep 写入门端到端真跑一次 + `pkg/data/equitydeep` 单测补齐（**生产代码零改动**） | `1764598` | `fundamentals_detail` 跑前 **0** → 跑中 **5** → 清理后 **0**（一行演示数据都不留）；PIT 三条读数（`as_of` 早于较晚 `ann_date` 读不到、同一 report_period 两次公告不被覆盖、`DISTINCT ON` 取最新 `fetched_at`）；`pkg/data/equitydeep` 覆盖率 **0% → 100.0%**（ODR-055 的最高优先遗留）；深财务字段不可得=纯读库取证（0 次 API）；另逮 DBT-14 那条契约漂移 |
| **DBT-12** | 挖掘登记表接上 PostgreSQL（起跑 `Load→NewRegistrationBookFrom`、裁决后按 (名,哈希) 差分行写穿）；**零新表零新 store**——migration 053/055 与 store 方法本就在册，只是生产零调用方 | `7130479` | 三表改前 0/0/0 → 实跑 1/2/1 → 清理后 0/0/0；真库腿跑**两轮**证明跨运行存活（第二轮 0 admitted、行数不变）；顺带修 `factor_mining_records.sandbox_hash` 是 `CHAR(64)` 导致"没凭证"读成 64 个空格（加 `BTRIM`）；补了此前**根本没有测试文件**的 `pkg/storage/mining_test.go`（6 条） |
| **DBT-08** | `TestAdjustmentBasis_QfqLookAheadLeaksIntoBacktest` 重新取证并把判据改成存在式 | `a918358` | 40.70s 绿（真库 + 真 token）；见下方取证数 |

**DBT-08 取证数（真库 18 只票池，2026-10-19）**：原判据钉死的 `@1e6=2.6475pp`、`@1e8=4.3498pp` 都落在翻面点谱（`3.48e4 / 7.50e4 / 1.62e5`）之外，所以读起来像"差异退化到取整噪声"；扫过谱之后 `@3.48e4` 差 **3426.87pp**（qfq −17.21%/282 笔 vs hfq +3409.66%/129 笔）、`@7.50e4` 1026.48pp、`@1.62e5` 25.31pp。结论：**前视照漏，且比当年记的 21.6pp 大两个数量级**——漏的是"你的票池能不能成交"，随资金逼近一手阈值而放大。谱低端两者都顶到天花板（挤出 15 vs 16 只/池 18），所以「per-symbol 比均匀更狠」的程度关系挪到**成分改写层**执法（实测在 `1.622e5`：+3 vs +1），不留在会随点翻转的收益层。量级读数不外推为收益预期。

**两条顺带订正（都是"从没被执行到过"的断言）**：
- `cmd/data/capital_flow_live_test.go` 断言 `period == source.DataTypeCapitalFlow`("capital_flow")，而执行器把它归一成 `capitalFlowDefaultPeriod`("daily")（`cmd/data/capital_flow_executor.go:36`）。该腿因源不可达一直在 `Execute` 处 Fatal，从未跑到那行。`period` 是冲突键 `(symbol, trade_date, period)` 的一部分，两源各写一种会让同一天并排落两行、读侧拿到重复且无声。已订正（`1a1bb38` 内）。
- `docs/TASKS.md` 引用 `7780236`/`7118691` 未注明是 docs 仓提交——见「证伪台账」末行。

**一条纪律缺口（如实自陈）**：本仓 `main` 在被另一会话的在飞改动（`pkg/backtest/exec_algo.go` 删了 29 行，而 `engine.go:922` 与它自己的 `rev22_signal_diag_test.go` 还在调 `recordSignalFailure`）弄到**编译不过**的中途，我没有去还原他人改动，改为在 `dbt-closeout` worktree（基线 `adeb2cf`）里完成全部验证后 `--ff-only` 合回。**代价**：那 5 笔 commit 的"整仓绿"读数来自 worktree，不是 main 工作树；main 恢复可构建之后应再跑一次 `go test ./...` 确认（本单未做，属 DBT-13+ 之前的一次常规收尾）。

---

## 四、跨仓任务索引（不在本仓，勿双写）

> 本仓台账只登记 core 自己的任务；实现主体在别仓的，这里只留可追溯指针。
>
> - **quant-trading-agent 仓**（`docs/TASKS.md`）：`OBS-01-R2-A` ⬜（2026-10-16 裁决 (b)，core 零改动；2026-10-19 复核仍 ⬜）；两项随 AI 拆仓迁出的原 core 项（`pkg/ai/yaml/generator.go` 默认回测区间与成分快照矛盾、现场装配未注册策略）——**DBT-18 属这一族**，本仓只留 `mining.Proposer` 契约指针。
> - **quant-trading-ui 仓**：`docs/TASKS.md` 的 `AUD-61` ⬜（2026-10-19 复核仍在册，该仓另有 4 处 ⬜）；`docs/design/stitch_quantlab_ux_redesign/TASKS.md` 的 `A-05` ✅、`A-08` ✅。
> - **quant-trading-docs 仓**：**NT v2.0.0 正式版升级**（`docs/guides/nt-sidecar-operations.md` 预案）——唯一外部等待项；正式版发布后按预案收口，双跑对齐回归为验收门。DBT-03 的运维指南段已在此仓（`7780236`/`7118691` 这两个 hash 即属该仓，见证伪台账）。

---

## 维护本文件

- 新增 task 必须按 [AGENTS.md §8.5](../AGENTS.md) 写全六字段（目标/上下文/要求/约束与非目标/验收/边界）。
- 完成即标 ✅（附日期）并移入 `archive/`；本文件只留未完成项。
- **派工前必须先证伪工单前提**（第 3 代的来由就是第 2 代 ✅ 记录正文里的"另立"，而其中三条登记已被源码/真库证伪）：读码核对 → 不符就停下回报 + 给拆法，不硬做、不扩范围。
- 大任务开工前，若六字段不够细，先补细再派工——**独立可运行判据达不到 = task 没写好，不是 agent 的问题**。
- **执行顺序**：按 [NN/6] 序号推进；撞文件的串行，一单一 commit。
- **新需求优先于存量未完成任务**；与旧任务重叠的可合并或直接删除，跨仓迁移的任务条目保留可追溯指针（不直接删原条目）。
- 归档文件的状态标记若与自身完成记录矛盾（第 2 代归档有 5 处这样），**只在文件头加订正说明**，条目正文与标题的历史措辞不改写——AGENTS.md §4 允许修的是死链，不是判断。
