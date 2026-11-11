---
status: active
last-verified: 2026-11-11
verified-by: 2026-11-11 登记 `CORE-RISK-REMOTE-FALLBACK-DEAD`（`risk_service.url` 的三处远程回落恒不可达，本轮只文档化：`config/analysis-service.yaml` 注释 + `docs/SPEC.md` §主要配置段 写实，代码零改动，待裁决删 or 接线）；改写 `CORE-BROKER-STARTUP-PRECONDITION` 的前提（实测三种编排都没给 core 注入 `BROKER_URL`，先后顺序问题排在接线之后）并给 `CORE-BROKER-RUNTIME-RECHECK` 补行号；2026-11-10 data-service 部署面收口（compose 改跨仓镜像 + `DATABASE_URL`/`DATA_MARKET_SCHEMA`，e2e 删死覆盖，k8s configmap/deployment 补 schema 与 DSN，`check_deploy_consistency.py` 新增 5e + 跨仓读取点；checker `exit 0`，四条破坏验证真做真还原；Docker daemon 不可达，未跑容器腿）；2026-11-10 晚 `QT-32` 真资产服务端到端取证（新增 opt-in `cmd/analysis/wiring/strategy_asset_live_test.go`：对真 `assetservice` 进程走 create → source → 登记 artifact → validate → promote(`X-Actor`) → `promotions` 审计读 → `CheckStrategyRef` 放行 `{id}@{v1}` 与 latest；拒绝两条真实路径 = v2 版本 hash 404 `strategy_not_found` / v2 artifact hash 409 `strategy_not_promoted`；`ASSET_SERVICE_URL=http://127.0.0.1:18090 go test ./cmd/analysis/wiring -run TestStrategyAssetGate_LiveAssetService -count=1 -v` PASS，未设 env 时 SKIP；破坏验证把状态位判定临时 `&& false` ⇒ 红，按备份还原本文件 `sha256=E2CBBAC6…712DFF4A` 一致、复跑绿）；2026-11-10 有库腿实测 + `QT-82` 收口（独立 PG 16.15 `:5433` 库 `tellurion_core`：先补 `NewPostgresStore` 运行时迁移、再按序 psql 应用 `migrations/0000000{1,2,3}/up.sql`，`pkg/storage` 11 条有库用例全绿；`pkg/marketdata` 3 条 / `pkg/backtest` 1 条 / `pkg/expression` 1 条原因 `ohlcv_minute`、`market_activity`、`stock_sector_map` 已随数据面退役且全仓无 DDL 而红，裁决 ①a 后新增 `pkg/testenv/market_tables.go` 形状镜像夹具并接入三处测试，5 条全绿（`EXIT=0`），破坏验证删表 + 撤夹具 ⇒ 重回 42P01 红、还原后复绿；`docs/SPEC.md` §数据归属 与 `AGENTS.md` 第 3 条已写跨仓只读表清单）；`QT-32` K11 执行侧闸门（2026-11-10：`strategy_ref` 非空先在资产服务过「`status=promoted` + promote 审计留痕」闸门，同步 handler / worker 旁路 / provenance 身份覆盖三处落地，空 ref 内置路径逐字节不变；三条破坏验证真做真还原、`gofmt -l` 空、`go vet ./...` = 0、`go test ./... -count=1` = 0（ok=70）；strategy 侧读面 `SVC-STRAT-G3-B` 同步 commit `92573b5`）；`QT-32` 收口（2026-11-10 晚：dsl 载体 promote 代裁 B = 不开放无 artifact 载体的版本级 promote，strategy `Promote` 对 builtin/dsl/external 显式 400 `unsupported_carrier` + 两条测试腿 + 破坏验证真做真还原；core 侧闸门无需改动——这三类版本 resolve 恒 404、不会被假放行；`tellurion-strategy` 全套 `gofmt -l` 空 / `go vet` = 0 / `go test ./... -count=1` = 0）；`QT-78` 收口（2026-11-10：真栈真 run `203ae0a198dd02f0530aeb70dfc0c036` 产出 portfolio-snapshot / factor-exposures 两类载荷且 hash 非空、analytics 两条归因恒等式 |Δ|=0、删持仓反证腿真跑出 409 `missing=holdings`；同批修掉「收尾强平清空期末持仓」与「发布期误读引擎级因子缓存」两条生产路径缺陷，`go build/vet/test ./... -count=1` 全 0）；`SVC-CORE-AI-RETIRE` 收口（2026-11-10：七表 DDL/索引/读写实现/启动 seed 清零，当场实测 ①写点 6→0 ②seed 3→0 ③DDL 7→0；core 新增 `internal/repoguard/storage_ai_tables_retired_test.go` 三条反向护栏 + agent 侧缺表尝试，三处破坏验证真做真还原、`go build/vet/test ./... -count=1` 全 0）；`SVC-RISK-G3` 跨仓收口证据（2026-11-10：core 侧新增 `pkg/risk` 新旧限额对拍测试；真 PG + 真 risk 进程的告警投递演练跑出真实行、停服后 outbox 转 `failed/attempts=3` 不静默丢；演练痕迹已清、生产代码零改动）；`SVC-CORE-AN-RETIRE` 收口（2026-11-09：归属裁决 = 保留并冻结；生产侧 import 冻结为 3 条、repoguard 新增/减少均红、破坏验证真做真还原、`go build/vet/test ./... -count=1` 全 0）；`SVC-CORE-RISK-RETIRE` 收口（2026-11-09：14 个 `risk_manager.*` 读点归零、5 个 `alert.*` 阈值改代码常量、`resolve_enabled` 出厂 true、新增 `pkg/risk` 快照换算与 wiring 装配测试、`go build/vet/test ./... -count=1` 全 0）；`SVC-CORE-STRAT-RETIRE` 收口（2026-11-09：三条 grep 生产侧归零 0/0/0、删 DDL / storage 5 方法 / `pkg/strategy/db.go` DB 腿、新建退役护栏 + 两条破坏腿真做真还原、`go build/vet/test ./... -count=1` 全 0）；`SVC-CORE-RUN-RETIRE` WF 半边收口（2026-11-09）；`SVC-CORE-DATA-RETIRE` 收口（2026-11-08：④ 四条 grep 生产侧归零 0/0/0/0、新建退役护栏 + 破坏验证真做真还原、`go build/vet/test ./... -count=1` 全 0；库侧用例因未设 `TEST_DATABASE_DSN` 按既有约定 skip）；`SVC-DEPLOY-01` 收口（2026-11-08：聚合 compose 栈 `up -d --build --wait` EXITCODE=0、7/7 容器 healthy；同轮修 `strategy` 缺 `JWT_SECRET` 的启动闸缺陷；宿主机 PG 占 5432 ⇒ postgres 宿主端口用临时 override 5433，已在正文如实登记）；跨仓补裁 #11~#13 落档（2026-11-08）；QT-77 结案（2026-11-07 晚：真 PG + 真服务补齐风险 resolve 200 / data ingest 200 / 无 token 401 与空 token 破坏腿；同批在 risk 修 PG approve 撞主键缺陷，见 QT-77 ⑥/⑥b/⑦）；QT-51 收口合入（2026-11-07：`task/qt51-execution-port` rebase 到本地 `main@b3f100e` → 唯一冲突 `docs/TASKS.md`（保留 main 侧）→ ff merge `a2e388a`；回滚点 tag `archive/qt51-execution-port`；四道门禁真退出码全 0、`ok`=70 / `FAIL`=0；等价性摘要 `60a2713b…` 与归档报告同值；破坏验证真做真还原）；第五轮·ADR-036 并行服务化 MVP（2026-10-26：六仓=服务、G0/G1/G2 并行、G3 切读串行、旧实现退役总单）；第四轮·目的对齐审计（2026-10-26，见 `docs/design/qt-decoupling/purpose-alignment-audit-2026-10-26.md`；不改既有单状态，只加审计指针）。第 4 代换代（2026-10-20）；**2026-10-20 补分仓批次（ADR-D29 / QT-05~09）与改名批次（ADR-D30 / QT-60）**。来由：ADB-029 abi v2 裁决（D24/D25/D26/D28）+ P0 第二轮深度审查（`docs/design/qt-decoupling/p0-round2-review.md`，N1~N9）。本代 30 张新工单**全部可追溯到设计文档的具体节**（无一条来自猜测）；第 3 代 6 项（DBT-13~18）经前提复核后原样延续。第 3 代正文已归档到 `archive/TASKS-2026-10-20-archived.md`。**2026-10-21 任务台账批量重分布**：QT-05 主单 → `tellurion-contracts`（kit 腿拆 QT-05-B → `tellurion-kit`）、QT-17 → `tellurion-agent`、QT-40~45 + QT-41b → X 线 6 个服务仓（各自 `docs/TASKS.md` 正式六字段单，「占位仓不写正式任务单」旧约定作废）、QT-61 按腿拆分登记（agent/ui 各留本腿记录，core 本文件为完整四腿权威版）；本仓台账收缩为**总序 + 指针**，迁出单正文不镜像。**2026-10-23 B6**：QT-10/11/22 落点已核（只加「落点已核」注记，不改六字段语义），W1 七单仓归属见 `docs/design/qt-decoupling/w1-repo-ownership.md`。**2026-10-23 C1**：QT-07 S2 ①~⑤ 与 QT-09 ①~⑤ 落地（双仓 tag、pin/仓边护栏 18 腿、契约语料 33 条三侧自检、`make contracts-conformance`）；⑥ S3 仍等 QT-50 + QT-17。**2026-10-25 B6-1**：QT-07 ⑥ **S3 结案**——agent 侧已删 `replace tellurion => ../tellurion`（三查：零 import / `go.mod` 无 require 无 `=>` / CI 不 checkout core），core 侧同批清空两条为它开的过期容忍（`check_contract_pin.py` 的 `LOCAL_REPLACE_WHITELIST` 清空 + `check_repo_autonomy.py` 的 `KNOWN_GAPS` R1 行），夹具腿翻成「不在白名单 → 红」；18 腿自证、9 pin 实扫、12 仓全量三查全绿，取证与破坏验证见 QT-07 ⑥ 结案两条。**同日 裁-17**：B6-2「服务间凭证传递」的派工前提经源码实测订正为三条面（只有 `core→risk` 与 `analytics→上游` 是真 401；本仓 `authz` 不管读面；compose 里 `data-service` 未接 `JWT_SECRET`；`tellurion-data` 那四条路径没进策略表 ⇒ 403，token 不解决）—— 0 代码，落 QT-50 未落第 5 条。
---

# TASKS — tellurion 未完成项

> **归档说明（2026-11-11）**：本文件已瘦身为「只装未做的事」。2026-11-10 及以前已收口（✅）的任务原文、施工记录、验收输出与破坏验证，已逐字移入 [`docs/archive/tasks-archive-2026-11.md`](archive/tasks-archive-2026-11.md)；该文件顶部有「检索索引」表（ID / 标题 / 收口日期 / commit / 证据链接）。检索：`grep -n "<任务ID或commit>" docs/archive/tasks-archive-2026-11.md`。
>
> 状态：`⬜ 待做` / `🔶 进行中` / `⛔ 阻塞（有未解除前置）` / `⏸ 挂起（外部依赖或已裁决暂不实施；**不计入开放项**，重开须另立单并满足该条「重开条件」）` / `✅ 完成（附日期，随即移入归档）`

## 一、未做 / 挂起 / 外部依赖

> 本节 = 本轮（2026-11-11）台账重整时登记的遗留边界。此前 74 张已收口单的正文与施工记录全部在归档文件里，本文件不再镜像。

### CORE-BROKER-RUNTIME-RECHECK · broker 通道性质只在启动期核对一次 [P1] ⏸

- **① 目标**：运行期对端被替换 / 重启成 mock 时，core 能发现并拒绝下单，而不是沿用启动期那一次核对结论。
- **② 上下文**：`pkg/brokerhttp/trader.go:122` 定义 `verifyPeerChannel`，`:177` 在构造期调一次（读对端 `/version` 的 `channel` 并与 `BROKER_CHANNEL` 比对，不一致 / 不可达 / 无 `channel` 一律拒绝启动），此后整个进程生命周期不再复校；`pkg/brokerhttp/trader_channel_test.go` 只覆盖构造期四条分支。今天把 broker 换成 mock 实例再重启，core 不会知道。
- **③ 要求**：① 在「下单前」或「固定周期」二者中定死一种复核时机并写明理由；② 失败语义与启动期同口径（fail-closed，绝不 fake success）；③ 复核失败要有可观测计数，不能只留一行日志。
- **④ 约束与非目标**：不改启动期核对与 `BROKER_CHANNEL` 契约；不引入服务发现；不做多副本。
- **⑤ 验收**：真进程把对端换成另一 channel 的替身后，下一笔下单被拒且错误码与启动期一致；破坏验证 = 去掉复核 ⇒ 用例红。
- **⑥ 边界**：只改 core 消费面；对端 `/version` 的内容归 `tellurion-broker`。
- **重开条件**：交易面进入「多环境共用一套 core」，或 `X6c` 真钱通道重开（真钱场景下运行期换端不可接受）。

### CORE-BROKER-STARTUP-PRECONDITION · broker 不可达时 core 拒起（部署前置）[P1] ⏸

- **① 目标**：把「core 起得来」与「broker 先就绪」的先后关系写成部署约束，避免编排侧误判为 core 缺陷。
- **② 上下文（2026-11-11 前提改写）**：原单假设「编排里已接 broker、只差启动依赖」，实测**不成立**。`cmd/analysis` 的 exec-engine 模块在 `Boot` 期无条件构造 broker 通道（`cmd/analysis/kernel_wiring.go:130` → `pkg/bootstrap.BuildExecutionTrader` → `pkg/brokerhttp.NewTraderFromEnv`），`BROKER_URL` 缺失即 `kernel assembly failed`、服务拒起；但**没有任何自带编排注入它**：core `docker-compose.yml` 5 个 service 无 broker、无 `BROKER_URL`；`docker-compose.services.yml` 起了 broker（`127.0.0.1:8091:8091`，仅回环），analysis 侧却既无 `BROKER_URL` 也无 `depends_on: broker`；`deploy/k8s/` 8 份清单无 broker。也就是说：今天「按仓库自带编排直接起」的每种形态都要求人工先注入四个 `BROKER_*` env 并自备网关，先后顺序问题排在「接线」之后。
- **③ 要求**：①（前置）先在**一种**形态里真接上 broker：显式注入 `BROKER_URL` / `BROKER_CHANNEL` / `BROKER_SERVICE_TOKEN` / `BROKER_ACCOUNT_ID` + 启动依赖（`depends_on` / initContainer / 重试退避），让 `up --wait` 一次成功；② `deploy/README.md` 与 runbook 写明失败时的原文（`kernel assembly failed` → `BROKER_URL is required` / `broker peer channel` 两类）；③ 保留 fail-closed，不改「先起后连」；④ 若产品上要支持「只做分析、不下单」的部署形态，那是**另一个决定**（把 exec-engine 改成可选模块），须单独立单，并说明不接交易时对账 / 风控覆盖口子由谁负责。
- **④ 约束与非目标**：不改 `pkg/brokerhttp` 的 fail-closed 行为；不引入跨服务的就绪协商。
- **⑤ 验收**：冷启动整套栈 `up --wait` 一次成功、core 重启计数为 0；破坏验证 = 拔掉 broker 或反序起，core 必失败且日志点名 `BROKER_URL` / `/version`。
- **⑥ 边界**：部署资产归本仓；broker 侧镜像、`/version` 的 `channel` 与探针归 `tellurion-broker`。
- **重开条件**：出现第一个需要 core 真下单的部署形态（X6c 真钱通道或 mock 演练），或决定「接入 broker」与「exec-engine 可选化」两条路线中的任一条。

### CORE-RISK-SNAPSHOT-RELOAD · risk 快照只在启动期加载一次，无热更新 [P1] ⏸

- **① 目标**：运行期新增 / 调整审批规则后，core 的进程内风控能生效，不必重启。
- **② 上下文**：`cmd/analysis/wiring/risk_rules.go` 在装配期把 risk 快照换算进 `pkg/risk`（`SVC-CORE-RISK-RETIRE` 收口时落的快照换算与 wiring 测试），运行期不再拉取；`config/analysis-service.yaml` 的 `risk_service.resolve_enabled` 也只控制启动期那一次。改风控 = 重启 core。
- **③ 要求**：① 定死刷新触发（定时 / risk 推送 / 审批通过后回调）与失败时的降级口径（继续用旧快照并告警，不是清空）；② 快照切换必须原子，不得出现半份规则；③ 刷新失败不得静默。
- **④ 约束与非目标**：不把 risk 的裁决权搬回 core；不改 `pkg/risk` 的快照格式（那是 `contracts/risk` 的冻结形状）。
- **⑤ 验收**：真栈里改一条规则 → 不重启 core → 下一次判定按新规则执行；破坏验证 = 关闭刷新通道 ⇒ 新规不生效且告警可见。
- **⑥ 边界**：规则真相源归 `tellurion-risk`；本条只做 core 侧的消费与刷新。
- **重开条件**：出现「不重启就要改风控」的运维需求，或 risk 侧提供版本化快照的推送端点。

### CORE-RISK-CREDENTIAL-PATH · 带凭证路径未取证（`JWT_SECRET` + `RISK_SERVICE_RESOLVE_TOKEN`）[P1] ⏸

- **① 目标**：把 core → risk 的**带鉴权**调用链真正跑通一次，而不是只验证回环开放形态。
- **② 上下文**：`QT-77` 已把凭证来源、签发与分发成文并落部署，`internal/repoguard/qt71_service_credentials_test.go` 只钉住装配形状；端到端跑过的两条路径都是「回环开放 / 空 token」。`config/analysis-service.yaml` 的 `risk_service.resolve_token` 与 `JWT_SECRET` 同时非空的组合，**生产形态从未真跑**。
- **③ 要求**：① 用真 `JWT_SECRET` + 真 `RISK_SERVICE_RESOLVE_TOKEN` 起 core 与 risk，跑一次 resolve 200；② 再跑一次错 token 401、空 token 401 的破坏腿；③ 把三段真实命令与退出码写进 `docs/runbook`/`docs/TASKS` 归档口径。
- **④ 约束与非目标**：不改鉴权实现与策略表；不把 token 写进仓库（用环境变量）。
- **⑤ 验收**：三条真实 HTTP 状态码（200 / 401 / 401）与日志原文；破坏验证 = 换错 token ⇒ 401 且 core 不以成功掩盖。
- **⑥ 边界**：core 侧装配归本仓；risk 侧 token 校验归 `tellurion-risk`（同一未取证项在其台账留有对应条目）。
- **重开条件**：任何一次真实部署验收；或风控从「默认关」转为「默认开」。

### CORE-RISK-REMOTE-FALLBACK-DEAD · `risk_service.url` 的三处远程回落恒不可用 [P2] ⏸

- **① 目标**：给 `pkg/backtest/engine.go` 里「进程内 RiskManager 为空 → 回落到 HTTP risk 服务」这条路径一个明确归宿：删掉，或接到真实的 tellurion-risk 端点。不让它继续以「配置里留着、读点也在、却永远走不通」的形态存在。
- **② 上下文**：`engine.go` 在三处用 `e.riskServiceURL` 拼 URL（`/detect_regime`、`/calculate_position`、`/check_stoploss`）；该字段来自 `risk_service.url`，键为空时缺省 `http://localhost:8083`。三个 `if rm == nil` 分支在 `cmd/analysis` 与 `cmd/worker` 里**不可达**——两个入口装配期都无条件 `SetRiskManager(bootstrap.BuildRiskManager(...))`。即使人为置空：8083 上没有任何服务（tellurion-risk 默认 `127.0.0.1:8089`，且只提供 `/api/risk/*`，**不提供**这三个路径）；只有把 url 写成 `:8085` 才会打到 core 自己的同名 legacy 别名，即被进程内实现取代的同一段逻辑。结论：**活读、恒不可达、且没有正确对端**。
- **③ 要求**：① 先定方向 —— 删（连 `riskServiceURL` 字段与 8083 缺省一起删，`config/*.yaml` 的 `risk_service.url` 一并退役）或接线（先要有这三条端点的契约，`contracts/risk` 里今天没有）；② 删除方向须补护栏：三处回落 grep 归零 + 一条反向测试钉住「不许回来」；③ 接线方向须写清端口与前缀口径，不许再出现「指向 analysis 自己 :8085」这种绕回自身的配置。
- **④ 约束与非目标**：本轮**只文档化**（`config/analysis-service.yaml` 注释 + `docs/SPEC.md` §主要配置段），不动代码、不改 `pkg/risk` 的进程内风控、不动 `tellurion-risk` 的路由。
- **⑤ 验收**：删除方向 = 三处回落与 `risk_service.url` 读点 grep 归零、`go build/vet/test ./... -count=1` 全绿、破坏验证（把回落加回来 ⇒ 护栏红并真还原）；接线方向 = 真 risk 进程跑通三条端点 200 且有契约测试。
- **⑥ 边界**：core 侧消费面归本仓；端点形状若新增归 `tellurion-risk` + `tellurion-contracts`。
- **重开条件**：一次明确裁决（删 or 接线）；或有人要部署一个真提供这三条路径的旧 risk 服务。

### CORE-DATA-MIGRATIONS-EXIT · `public.data_migrations` 过渡态的退出条件 [P2] ⏸

- **① 目标**：给 `public.data_migrations` 这个过渡保留物一个可判定的退出条件，避免它变成永久真相。
- **② 上下文**：数据面退役（`SVC-CORE-DATA-RETIRE`，2026-11-08）后该表仍留在 `public` 作为迁移账的过渡记录；退出条件此前只写在退役单正文里，未成正式条目。
- **③ 要求**：① 写明「谁不再读它 / 门禁怎么证明零读者」；② 退出动作 = 归档账本 + 删表 + 迁移重放仍绿；③ 若长期保留，改为显式「永久只读账本」并写进 `docs/db-schema.md`。
- **④ 约束与非目标**：不在 `X1d` 生产搬迁完成前删；不改已应用的迁移文件。
- **⑤ 验收**：退出时 `grep` 生产侧读点为 0、真库重放两次形状不变、`db-schema.md` 同步。
- **⑥ 边界**：本仓 schema 生命周期；物理搬迁动作归运维（见 `OPS-X1D-PROD-MIGRATION`）。
- **重开条件**：X1d 生产搬迁完成且观察一个发布周期。

### OPS-X1D-PROD-MIGRATION · X1d 物理搬迁 + `visible_at` 回填（运维动作，本机已验证）[P1] ⏸

- **① 目标**：把已验证通过的 X1d 全流程在**生产库**执行一次。
- **② 上下文**：本机 scratch 实例已完整重放（68 条 DDL → 22 表反向搬 public + 合成历史行 → 0001 迁移 22 移 → verify 22/22 → 回填 12 条哨兵归 0 → 幂等复跑 22 skip → `-tags backfill` 套件 7/7），证据在 `tellurion-data:docs/archive/x1d-local-replay-2026-11-10.md`。**生产未动**，执行者是运维、不是 agent。
- **③ 要求**：① 按本机同一顺序执行并留每步退出码；② 回填后跑一次行数与 `visible_at` 读数核对；③ 失败回滚点与验收口径先写进变更单。
- **④ 约束与非目标**：agent 不执行生产写；不在本仓改 DDL。
- **⑤ 验收**：生产库 verify 22/22、回填 12 条哨兵归 0、`visible_at` 无 NULL 残留。
- **⑥ 边界**：运维/生产动作；schema 定义归 `tellurion-data`。
- **重开条件**：生产变更窗口 + 本机重放证据复核（已有）。

## 二、跨仓指针（不在本仓，勿双写）

| 事项 | 去哪 |
|---|---|
| 六服务各自的就绪门、挂起项与重开条件 | `tellurion-data` / `tellurion-strategy` / `tellurion-risk` / `tellurion-analytics` / `tellurion-orchestration` / `tellurion-broker` 的 `docs/TASKS.md` |
| 契约形状与工具层 | `tellurion-contracts:docs/TASKS.md` / `tellurion-kit:docs/TASKS.md` |
| 文档、ADR 与 NT 侧车升级 | `tellurion-docs:docs/TASKS.md` |
| agent / ui 本仓任务 | 各自 `docs/TASKS.md` |

> 旧的「八、跨仓任务索引」逐条状态快照（多数已过期）随本轮重整移入归档，**状态以各仓台账为准**。

## 维护本文件

- 新增 task 必须按 [AGENTS.md §8.5](../AGENTS.md) 写全六字段；**六字段里必须显式引用冻结契约的路径与节号**（本代 wave-0 的冻结文本在 `docs/design/qt-decoupling/p0-round2-review.md` §8、`p0-remediation-design.md`、`adr-029-amendment-host-api-v2.md`）。
- 完成即标 ✅（附日期）并移入 `archive/`；本文件只留未完成项。换代时整篇加时间戳归档（`archive/TASKS-YYYY-MM-DD-archived.md`），旧代正文不改写。
- **派工前必须先证伪工单前提**（读码核对 → 不符就停下回报 + 给拆法，不硬做、不扩范围）。
- **执行顺序（偏序，非全序；refactor-plan-review M4）**：硬链 **S-1(QT-60 ✅) → QT-61 → S0（QT-06 → QT-18 → QT-08）→ W0 → S1(QT-05)**；S2 段（QT-17 → QT-07 → QT-19 → QT-09）须在 W0 + QT-05 全绿后；**W1 不依赖 S2**——W0 后即可开工，且 **仅 QT-17 可与 W1 并行**（QT-07/09/19 不与 W1 抢档）；W2 在 W1 后、W3 在 W2 后。同波次内撞文件的按 QT 序号串行，一单一 commit。**W0 完成前不开 W1 的代码单**——否则 artifact 与基准都要重做。
- **冻结 schema 变更评审（硬规则；refactor-plan-review H1）**：凡触碰 `ResultSpec` / `RunSpec` / `SignalSpec` / `docs/SPEC.md` 冻结字段的单，六字段内必须列「**schema 变更评审项 + 需同步更新的 SPEC 节号**」，并按固定顺序执行：**先触发合规测试红 → 更新 SPEC 文本 → 走正式变更评审记录 → 才许合入**；未列评审项的单一律不得改冻结面（含「顺手加一列/加一字段」）。
- **合入门禁（硬约束，不是建议）**：⓪ ~~QT-60 未完成前 QT-06 不得开工~~ → **已满足（QT-60 于 2026-10-20 完成）**，tag 现在可以打在新仓；新增 ⓪' **QT-61 的「gofmt + doc-links」两项绿之前，QT-06 的「两仓 CI 绿」验收不可达**——要么先做 QT-61，要么把 QT-06 验收改成「本次改动相关的 job 绿 + 其余 known-fail 登记」；① **QT-18 未合入 main 前，QT-01 不得合入**——第一个 breaking 就在 QT-01 的 abi v2，护栏不在就会漏；② QT-05 未合入前，QT-07 不得开工；③ QT-07/QT-09 未绿前，不得在任何仓新增跨仓 `replace`（D29 §9 C1）。
- **新需求优先于存量未完成任务**；与旧任务重叠的可合并或直接删除，跨仓迁移的任务条目保留可追溯指针。
- 归档文件的状态标记若与自身完成记录矛盾，**只在文件头加订正说明**，条目正文与标题的历史措辞不改写。
