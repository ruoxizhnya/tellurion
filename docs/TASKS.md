---
status: active
last-verified: 2026-11-13
verified-by: 2026-11-13 五角审查立单 → 同日施工收口 10 条并归档（档 3 四条 / 档 4 四条 / 档 5 两条，正文与施工记录逐字移入 `archive` §二十六；`CORE-S1-EXIT-EVIDENCE` 按主任裁决落 ⏸；`X-COVERAGE-FLOOR` / `X-CI-BASELINE-PIN` / `X-PROBE-CANONICAL-HEALTHZ` 标注 core 腿已落、留伞待对侧仓）；2026-11-13 五角审查立单（只立单不施工）——档 2（产品设计）新立 `CORE-S1-EXIT-EVIDENCE` ⬜ 与 `CORE-STOCKSTATE-DISPOSITION` ⏸；档 3（实现）新立 `CORE-REPOGUARD-HANDLER-WIRED` / `CORE-DATA-URL-SINGLE-DEFAULT` / `CORE-API-V1-INDEX` / `CORE-BACKTEST-REQUEST-UNION` ⬜、`CORE-BACKTEST-ENGINE-SPLIT` / `CORE-DEAD-PACKAGE-DISPOSITION` ⏸，并新增 §四 跨仓伞 `X-PROBE-CANONICAL-HEALTHZ`；档 4（测试）新立 `CORE-E2E-FAIL-LOUD` / `CORE-INTEGRATION-TAG-CI` / `CORE-COVERAGE-FLOOR` / `CORE-TEST-HERMETIC` ⬜、`CORE-TEST-EVIDENCE-VERSIONED` ⏸，§四 伞 `X-COVERAGE-FLOOR` / `X-CI-BASELINE-PIN` / `X-CI-WINDOWS`；档 5（文档与代码一致性）新立 `CORE-OPENAPI-MEASUREMENT-INDEX` / `CORE-CONTRACTS-DIR-NAMING`；此前：2026-11-11 全面审查 W7 台账立单（A5 授权）——§一 新立 12 条 core 六字段单（`CORE-PG-BACKUP-DR` / `CORE-CI-IMAGE-GATE` / `CORE-REDIS-EXIT` / `CORE-MIGRATION-REPLAY-GUARD` / `CORE-K8S-EXIT` / `CORE-DEAD-PROXY-SCREEN-VERIFY` / `CORE-CI-ARCHIVE-LINKS-FIX` / `CORE-DEPLOY-DRIFT-PARAM` / `CORE-RUNNER-FLAKE-RESIDUAL` / `CORE-FEE-ROUNDING-ASSERT` / `CORE-RESULT-RETENTION` / `CORE-REFS-TRAILER-RULE`），新增 §四 5 条跨仓批次伞（`X-DEP-BUMP-CONSUMERS` / `X-AGENTS-DEPENDENCY-TABLE` / `X-CI-RACE-STATICCHECK` / `X-PROBE-NAMING` / `X-LOW-CLEANUP`）；证据坐标引 ODR-068~073（`tellurion-docs:docs/archive/odr/`）；本轮只立单不修复，施工另逐单授权；2026-11-11 DDD 边界审查落单（新增 §三 13 条台账；新立 `CORE-FACTOR-DERIVED-GUARD` / `CORE-FACTOR-DEAD-PROXY` / `CORE-SPEC-FACTOR-ENDPOINTS-STALE` / `CORE-COMPOSE-FACTOR-COMMENT` / `CORE-ANALYSISARTIFACT-RETENTION` / `CORE-MARKET-INDEX-DAILY-EXCEPTION`；跨仓裁决 ADR-038~041）；2026-11-11 `CORE-FACTOR-CACHE-WRITER` 裁决（ADR-037：`factor_cache` 是分析上下文的读模型，owner=core、形态=消费点按需实时，不新增数据库写者；否决 core 预热作业与归 data，四条落地卡点与重开条件已写入任务单，本轮不实现）；2026-11-11 本地编排收敛为单一 `docker-compose.yml`（撤销 QT-79 代裁 `D-DEPLOY-1 = A`，见归档 §二十三 `CORE-COMPOSE-UNIFY`）：默认 4 服务 + `--profile full` 4 服务、broker 进默认栈、数据库走宿主机原生 16.15、e2e 高额度改命令行 env；2026-11-11 登记 `CORE-RISK-REMOTE-FALLBACK-DEAD`（`risk_service.url` 的三处远程回落恒不可达，本轮只文档化：`config/analysis-service.yaml` 注释 + `docs/SPEC.md` §主要配置段 写实，代码零改动，待裁决删 or 接线）；改写 `CORE-BROKER-STARTUP-PRECONDITION` 的前提（实测三种编排都没给 core 注入 `BROKER_URL`，先后顺序问题排在接线之后）并给 `CORE-BROKER-RUNTIME-RECHECK` 补行号；2026-11-10 data-service 部署面收口（compose 改跨仓镜像 + `DATABASE_URL`/`DATA_MARKET_SCHEMA`，e2e 删死覆盖，k8s configmap/deployment 补 schema 与 DSN，`check_deploy_consistency.py` 新增 5e + 跨仓读取点；checker `exit 0`，四条破坏验证真做真还原；Docker daemon 不可达，未跑容器腿）；2026-11-10 晚 `QT-32` 真资产服务端到端取证（新增 opt-in `cmd/analysis/wiring/strategy_asset_live_test.go`：对真 `assetservice` 进程走 create → source → 登记 artifact → validate → promote(`X-Actor`) → `promotions` 审计读 → `CheckStrategyRef` 放行 `{id}@{v1}` 与 latest；拒绝两条真实路径 = v2 版本 hash 404 `strategy_not_found` / v2 artifact hash 409 `strategy_not_promoted`；`ASSET_SERVICE_URL=http://127.0.0.1:18090 go test ./cmd/analysis/wiring -run TestStrategyAssetGate_LiveAssetService -count=1 -v` PASS，未设 env 时 SKIP；破坏验证把状态位判定临时 `&& false` ⇒ 红，按备份还原本文件 `sha256=E2CBBAC6…712DFF4A` 一致、复跑绿）；2026-11-10 有库腿实测 + `QT-82` 收口（独立 PG 16.15 `:5433` 库 `tellurion_core`：先补 `NewPostgresStore` 运行时迁移、再按序 psql 应用 `migrations/0000000{1,2,3}/up.sql`，`pkg/storage` 11 条有库用例全绿；`pkg/marketdata` 3 条 / `pkg/backtest` 1 条 / `pkg/expression` 1 条原因 `ohlcv_minute`、`market_activity`、`stock_sector_map` 已随数据面退役且全仓无 DDL 而红，裁决 ①a 后新增 `pkg/testenv/market_tables.go` 形状镜像夹具并接入三处测试，5 条全绿（`EXIT=0`），破坏验证删表 + 撤夹具 ⇒ 重回 42P01 红、还原后复绿；`docs/SPEC.md` §数据归属 与 `AGENTS.md` 第 3 条已写跨仓只读表清单）；`QT-32` K11 执行侧闸门（2026-11-10：`strategy_ref` 非空先在资产服务过「`status=promoted` + promote 审计留痕」闸门，同步 handler / worker 旁路 / provenance 身份覆盖三处落地，空 ref 内置路径逐字节不变；三条破坏验证真做真还原、`gofmt -l` 空、`go vet ./...` = 0、`go test ./... -count=1` = 0（ok=70）；strategy 侧读面 `SVC-STRAT-G3-B` 同步 commit `92573b5`）；`QT-32` 收口（2026-11-10 晚：dsl 载体 promote 代裁 B = 不开放无 artifact 载体的版本级 promote，strategy `Promote` 对 builtin/dsl/external 显式 400 `unsupported_carrier` + 两条测试腿 + 破坏验证真做真还原；core 侧闸门无需改动——这三类版本 resolve 恒 404、不会被假放行；`tellurion-strategy` 全套 `gofmt -l` 空 / `go vet` = 0 / `go test ./... -count=1` = 0）；`QT-78` 收口（2026-11-10：真栈真 run `203ae0a198dd02f0530aeb70dfc0c036` 产出 portfolio-snapshot / factor-exposures 两类载荷且 hash 非空、analytics 两条归因恒等式 |Δ|=0、删持仓反证腿真跑出 409 `missing=holdings`；同批修掉「收尾强平清空期末持仓」与「发布期误读引擎级因子缓存」两条生产路径缺陷，`go build/vet/test ./... -count=1` 全 0）；`SVC-CORE-AI-RETIRE` 收口（2026-11-10：七表 DDL/索引/读写实现/启动 seed 清零，当场实测 ①写点 6→0 ②seed 3→0 ③DDL 7→0；core 新增 `internal/repoguard/storage_ai_tables_retired_test.go` 三条反向护栏 + agent 侧缺表尝试，三处破坏验证真做真还原、`go build/vet/test ./... -count=1` 全 0）；`SVC-RISK-G3` 跨仓收口证据（2026-11-10：core 侧新增 `pkg/risk` 新旧限额对拍测试；真 PG + 真 risk 进程的告警投递演练跑出真实行、停服后 outbox 转 `failed/attempts=3` 不静默丢；演练痕迹已清、生产代码零改动）；`SVC-CORE-AN-RETIRE` 收口（2026-11-09：归属裁决 = 保留并冻结；生产侧 import 冻结为 3 条、repoguard 新增/减少均红、破坏验证真做真还原、`go build/vet/test ./... -count=1` 全 0）；`SVC-CORE-RISK-RETIRE` 收口（2026-11-09：14 个 `risk_manager.*` 读点归零、5 个 `alert.*` 阈值改代码常量、`resolve_enabled` 出厂 true、新增 `pkg/risk` 快照换算与 wiring 装配测试、`go build/vet/test ./... -count=1` 全 0）；`SVC-CORE-STRAT-RETIRE` 收口（2026-11-09：三条 grep 生产侧归零 0/0/0、删 DDL / storage 5 方法 / `pkg/strategy/db.go` DB 腿、新建退役护栏 + 两条破坏腿真做真还原、`go build/vet/test ./... -count=1` 全 0）；`SVC-CORE-RUN-RETIRE` WF 半边收口（2026-11-09）；`SVC-CORE-DATA-RETIRE` 收口（2026-11-08：④ 四条 grep 生产侧归零 0/0/0/0、新建退役护栏 + 破坏验证真做真还原、`go build/vet/test ./... -count=1` 全 0；库侧用例因未设 `TEST_DATABASE_DSN` 按既有约定 skip）；`SVC-DEPLOY-01` 收口（2026-11-08：聚合 compose 栈 `up -d --build --wait` EXITCODE=0、7/7 容器 healthy；同轮修 `strategy` 缺 `JWT_SECRET` 的启动闸缺陷；宿主机 PG 占 5432 ⇒ postgres 宿主端口用临时 override 5433，已在正文如实登记）；跨仓补裁 #11~#13 落档（2026-11-08）；QT-77 结案（2026-11-07 晚：真 PG + 真服务补齐风险 resolve 200 / data ingest 200 / 无 token 401 与空 token 破坏腿；同批在 risk 修 PG approve 撞主键缺陷，见 QT-77 ⑥/⑥b/⑦）；QT-51 收口合入（2026-11-07：`task/qt51-execution-port` rebase 到本地 `main@b3f100e` → 唯一冲突 `docs/TASKS.md`（保留 main 侧）→ ff merge `a2e388a`；回滚点 tag `archive/qt51-execution-port`；四道门禁真退出码全 0、`ok`=70 / `FAIL`=0；等价性摘要 `60a2713b…` 与归档报告同值；破坏验证真做真还原）；第五轮·ADR-036 并行服务化 MVP（2026-10-26：六仓=服务、G0/G1/G2 并行、G3 切读串行、旧实现退役总单）；第四轮·目的对齐审计（2026-10-26，见 `docs/design/qt-decoupling/purpose-alignment-audit-2026-10-26.md`；不改既有单状态，只加审计指针）。第 4 代换代（2026-10-20）；**2026-10-20 补分仓批次（ADR-D29 / QT-05~09）与改名批次（ADR-D30 / QT-60）**。来由：ADB-029 abi v2 裁决（D24/D25/D26/D28）+ P0 第二轮深度审查（`docs/design/qt-decoupling/p0-round2-review.md`，N1~N9）。本代 30 张新工单**全部可追溯到设计文档的具体节**（无一条来自猜测）；第 3 代 6 项（DBT-13~18）经前提复核后原样延续。第 3 代正文已归档到 `archive/TASKS-2026-10-20-archived.md`。**2026-10-21 任务台账批量重分布**：QT-05 主单 → `tellurion-contracts`（kit 腿拆 QT-05-B → `tellurion-kit`）、QT-17 → `tellurion-agent`、QT-40~45 + QT-41b → X 线 6 个服务仓（各自 `docs/TASKS.md` 正式六字段单，「占位仓不写正式任务单」旧约定作废）、QT-61 按腿拆分登记（agent/ui 各留本腿记录，core 本文件为完整四腿权威版）；本仓台账收缩为**总序 + 指针**，迁出单正文不镜像。**2026-10-23 B6**：QT-10/11/22 落点已核（只加「落点已核」注记，不改六字段语义），W1 七单仓归属见 `docs/design/qt-decoupling/w1-repo-ownership.md`。**2026-10-23 C1**：QT-07 S2 ①~⑤ 与 QT-09 ①~⑤ 落地（双仓 tag、pin/仓边护栏 18 腿、契约语料 33 条三侧自检、`make contracts-conformance`）；⑥ S3 仍等 QT-50 + QT-17。**2026-10-25 B6-1**：QT-07 ⑥ **S3 结案**——agent 侧已删 `replace tellurion => ../tellurion`（三查：零 import / `go.mod` 无 require 无 `=>` / CI 不 checkout core），core 侧同批清空两条为它开的过期容忍（`check_contract_pin.py` 的 `LOCAL_REPLACE_WHITELIST` 清空 + `check_repo_autonomy.py` 的 `KNOWN_GAPS` R1 行），夹具腿翻成「不在白名单 → 红」；18 腿自证、9 pin 实扫、12 仓全量三查全绿，取证与破坏验证见 QT-07 ⑥ 结案两条。**同日 裁-17**：B6-2「服务间凭证传递」的派工前提经源码实测订正为三条面（只有 `core→risk` 与 `analytics→上游` 是真 401；本仓 `authz` 不管读面；compose 里 `data-service` 未接 `JWT_SECRET`；`tellurion-data` 那四条路径没进策略表 ⇒ 403，token 不解决）—— 0 代码，落 QT-50 未落第 5 条。2026-11-12 `CORE-FACTOR-CACHE-WRITER` 按 ADR-037 D2 实现收口（run 级按需求值 + 门禁只读 run 级物化；`public.factor_cache` 标为退役遗留表，删表与读面清理另立 `CORE-FACTOR-CACHE-RETIRE`）；`CORE-REDIS-EXIT` / `CORE-BACKTEST-RANGE-COVERAGE` / `CORE-BACKTEST-OPS-TEXT` / `CORE-FACTOR-CACHE-RETIRE` 同日收口并归档；2026-11-12 `CORE-MIGRATION-REPLAY-GUARD` 收口（迁移改为「每条一事务 + 账本 `core_schema_migrations`（版本 + 指纹 + 应用时间）」：已应用同指纹跳过，指纹不符或账本领先于代码拒绝启动，存量库首次接管重放幂等 DDL 并记账；集成用例覆盖半途失败、重启续跑、接管与拒绝启动）；2026-11-12 `CORE-CI-ARCHIVE-LINKS-FIX` 收口（归档层 60 条相对链接补层级、2 处失效配置引用加「已删除」注记；`check_doc_links.py --include-archive` 由 60+2 条红转绿，两条破坏验证真做真还原）；2026-11-12 `CORE-STRATEGY-SCREEN-FALLBACK-DEAD-UPSTREAM` 按裁决 B1 收口（`value_screen` 多票删死腿、改为可诊断 `contracts.ErrDataGap` ⇒ 逐日 `data_gap_skipped`、终态 `invalid`；`multi_factor` 只删死腿、控制流不变；`retiredUpstreamAllowlist` 归零 + 4 条正向对照；真验红：改前同一引擎用例 `completed`、改后 `invalid`）。2026-11-12 `CORE-STRATEGY-MULTIFACTOR-PSEUDO-VALUE` 收口（本地价量路径的 `valueScore` 实为价格区间，裁决选 ②：价值维度缺席、quality/momentum 重归一化，Factors/Metadata 标记可追溯）；同批 `CORE-STRATEGY-SCREEN-LOCAL-VALUE` 立单（C 方案，值维度读路径 `pkg/storage/daily_basic.go` 已就位：PIT `trade_date <=` 取最近一条、`pe_ttm`→`pe` 回退且来源可见、市值万元→元单点换算、NULL = 该票当日不参与筛选；映射单测 + 真库反例绿）；2026-11-12 `CORE-STRATEGY-SCREEN-LOCAL-VALUE` 收口并归档（C 方案落地：run 内 daily_basic 快照 + fundamentals ROE，`pe<=`/`pb<=`/`roe>=` 单多票统一生效，零覆盖与 <50% 低覆盖按 B1 报 `ErrDataGap`，provenance 暴露 `pe_source`/覆盖数；commit `1317934`、`de85f71`）；2026-11-12 `CORE-CI-IMAGE-GATE` 收口并归档（CI 新增 `image-build` job 真跑 `docker build -f Dockerfile.service .`，检查器补 compose build 面与 `go build ./cmd/xxx` 目标目录两项静态检查；破坏验证改坏构建目标 ⇒ 检查器 exit 1 + 真构建 exit 1，还原后 SHA256 一致；commit `dd5a98f`）；2026-11-12 `CORE-K8S-EXIT` 收口并归档（7 份清单移入 `deploy/k8s/archive/`，检查器加「活跃层必须零清单」守卫、退役态明说跳过范围；12 仓 grep `kubectl apply`/kustomize/helm 零命中；破坏验证放回一份清单 ⇒ exit 1、删除后 exit 0；commit `480aab9`）；2026-11-12 `CORE-REFS-TRAILER-RULE` 收口并归档（`Refs:`/`Reviewed:` 改可选、任务号可追溯仍为硬约束、不回填历史；取证按实测订正为「少数带 trailer」并写入 AGENTS；commit `cca11ec`）；2026-11-12 `CORE-RESULT-RETENTION` 收口并归档（明细 90 天 / run 级汇总与 provenance 365 天 / 被引用的结果永不删；时间基准 `finished_at`、清理留 `detail_purged_at`、引用判定按真实表且拿不准即保留、运行中跳过、逐 run 事务 + 幂等 + dry-run；commit `f50affe`、`1d07a39`、`5af46cb`）；2026-11-12 `CORE-RISK-REMOTE-FALLBACK-DEAD` 收口并归档（裁决 = 删：删三处恒不可达的 HTTP 回落与 `risk_service.url`，无 RiskManager 即 fail-closed；AST 护栏 + 三条 fail-closed 行为腿、破坏验证真做真还原；commit `5729395`）；同日 `CORE-RISK-CREDENTIAL-PATH` 三态鉴权真跑取证（无 token 401 / viewer 403 / service 200，隔离实例、凭证不入库；commit `a916a51`）；`X-PROBE-NAMING` 收口归档（data 摘 `/health` 别名后 e2e 复验全绿）；`CORE-ANALYSISARTIFACT-RETENTION` 复核 T1 未触发（commit `ee9a8cf`）；台账口径订正：挂起 23 条中 core/risk 两条 CREDENTIAL-PATH 为同一缺口两侧；2026-11-12 `CORE-AUDIT-FULLSTACK-2026-11` 全栈运行时验收（复用运行中的 `--profile full` 栈：8/8 健康、开放态鉴权、数据面读写、momentum / multi_factor / value_screening 三策略 completed、UI 四路由渲染通过；4 项未覆盖各有原因，W1~W7 未完成，保持 🔶）；2026-11-12 `CORE-BROKER-STARTUP-PRECONDITION` 收口归档（文档腿补齐：deploy README 启动前置 + 三类失败原文、runbook 补 `wait-for-deps` entrypoint 阶段；k8s 腿随 `CORE-K8S-EXIT` 退役消失；隔离容器真跑 broker 不可达 ⇒ exit 1 且点名依赖）；2026-11-12 四条 X 伞逐仓只读复核：`X-DEP-BUMP-CONSUMERS`（kit pin **6 仓**均为 `v0.5.0`；broker **未引入 kit**、不是 pin 落后；7 仓 pgx `v5.11.0` / go `1.26.11` + `govulncheck` 门，本轮独立复跑 EXIT=0）、`X-AGENTS-DEPENDENCY-TABLE`（6 仓 AGENTS §4 与 `go.mod` 一致；broker 未引入 kit）与 `X-LOW-CLEANUP`（5/5 腿）关账归档；`X-CI-RACE-STATICCHECK` 收口归档（7/7 腿：CI 步骤 8/8 + ui 构建门；AGENTS §2 覆盖面 data `8bc33e8` / strategy `4839b54` / risk `b79de84` / orchestration `37349eb` / analytics `6f05d20` / contracts `4fdcb53` / broker `984646e`，逐仓只读核对与各自 `ci.yml` 一致）。2026-11-12 新登记 `CORE-BENCH-CALENDAR-FIXTURE`（⏸：`BenchmarkBacktest` 缺交易日历、一跑即 fail；重开 = 提供 fixture 或改用合成日历）；同日 W3 回测审计实跑后新登记 `CORE-LISTING-SEED-UNWIRED`（⏸：上市日历种子无生产调用者 + 偏差标记未实现，幸存者偏差修正在真实路径未生效）与 `CORE-CI-DB-LEGS-SKIPPED`（⏸：CI Test job 无 PG/DSN，PIT 与迁移账本用例长期 skip）。2026-11-12 台账卫生批次：活跃层 8 条已完成项（`CORE-COMPOSE-UNIFY` / `CORE-STRATEGY-MULTIFACTOR-PSEUDO-VALUE` / `CORE-FACTOR-DERIVED-GUARD` / `CORE-FACTOR-DEAD-PROXY` / `CORE-SPEC-FACTOR-ENDPOINTS-STALE` / `CORE-COMPOSE-FACTOR-COMMENT` / `CORE-RISK-CREDENTIAL-PATH` / `OPS-X1D-PROD-MIGRATION`）原文逐字移入归档 §二十三并补索引行；同批把共享内存/竞态硬化的 4 个提交（`5e8b433` / `fac3a3f` / `b25fb6a` / `4054f52`）补登为伞条目 `CORE-MEMORY-HARDENING-2026-11`，验证证据坐标见该条（CI run `43225020965` / `43226869498`）。2026-11-12 `CORE-FACTOR-ANALYTICS-SURFACE` 收口并归档（B-05 后端腿：`factor_returns` / `ic_analysis` 的生产者 + 横截面聚合读面 `/api/factor-analytics/*` 4 条；不复活 ADR-041 D1 禁止的 per-symbol 求值面；真库 `ON CONFLICT` 幂等与 summary 用例 PASS）。2026-11-12 `CORE-SIGNAL-FEED-READ` 收口并归档（A-09 后端腿：`GET /api/signals` 倒序信号流，读能力缺失 503 不伪装空流；真库 `ListRecent` 用例 PASS）。2026-11-12 `CORE-REVIEW-PENDING-AGGREGATE` 收口并归档（A-04 断言 5 / G1：`GET /api/review/pending` 多源聚合，任一源未知即 `pending:null` — 不画恒 0 角标；agent 侧 `GET /api/explore/review/pending` 契约定稿写在 openapi 与归档条目）。2026-11-12 `CORE-BENCH-CALENDAR-FIXTURE` 收口并归档（按 fixture 日期合成交易日历，`BenchmarkBacktest` 恢复 `88979200 ns/op` + PASS；新增 `TestBenchmarkTradingDaysMatchFixture` 钉住口径）。2026-11-12 `CORE-LISTING-SEED-UNWIRED` 收口并归档（data `72b96aa` 的 `GET /api/v1/stocks/listing-windows` 接线：新增 `internal/listingseed`，启动期与 walk-forward 窗口引擎都注入 `SetListingWindows`；失败**不合成**窗口、快照 `degraded` + `pool_source=current`；新增 `GET /api/listing-windows/status` 偏差标记读面）。2026-11-12 `CORE-CI-DB-LEGS-SKIPPED` 收口并归档（CI Test job 设 `TEST_DATABASE_DSN` + `tools/ci/applymigrations` 显式迁移步，PIT / 迁移账本用例不再 skip；补齐 `ohlcv_daily_qfq` / `market.stocks` 外仓表镜像；隔离 PG 上全套件 `EXIT=0`、总量覆盖率 72.5%，破坏验证改公告日判定即红）。2026-11-12 W5 起栈追加验收（隔离卷/独立库：新镜像 `tellurion-core:w5-20261112` 首启 ledger 85 行 = 代码 85 条、二启 digest 不变零重放；结论已记入 ODR-072 追加节，详见 §五）。2026-11-12 台账口径按活跃层实际条目重算为 **18 条**（§一 5 + §三 13），`CORE-CI-DB-LEGS-SKIPPED` 正文「没有 services: postgres」的过时描述已订正。
---

# TASKS — tellurion 未完成项

> **归档说明（2026-11-11）**：本文件已瘦身为「只装未做的事」。2026-11-10 及以前已收口（✅）的任务原文、施工记录、验收输出与破坏验证，已逐字移入 [`docs/archive/tasks-archive-2026-11.md`](archive/tasks-archive-2026-11.md)；该文件顶部有「检索索引」表（ID / 标题 / 收口日期 / commit / 证据链接）。检索：`grep -n "<任务ID或commit>" docs/archive/tasks-archive-2026-11.md`。
>
> 状态：`⬜ 待做` / `🔶 进行中` / `⛔ 阻塞（有未解除前置）` / `⏸ 挂起（外部依赖或已裁决暂不实施；**不计入开放项**，重开须另立单并满足该条「重开条件」）` / `✅ 完成（附日期，随即移入归档）`

> **台账口径订正（2026-11-12 复核查出）**：
> - **挂起计数 18 条**（2026-11-12 只读复核后，按**活跃层实际条目逐条计数**，不再用「加减推算」）：§一 5 条（`CORE-ANALYSISARTIFACT-RETENTION` / `CORE-BROKER-RUNTIME-RECHECK` / `CORE-RISK-SNAPSHOT-RELOAD` / `CORE-MARKET-INDEX-DAILY-EXCEPTION` 不改 / `CORE-AUDIT-FULLSTACK-2026-11` 🔶；`CORE-DATA-MIGRATIONS-EXIT` 已标 ✅ 作废，不计）+ §三 13 条 = 18。此前写 23 是错的：处置稿收口的 8 条从未从计数里减掉，`CORE-BENCH-CALENDAR-FIXTURE` 又漏加，两处误差叠在旧数上。
> - `CORE-RISK-REMOTE-FALLBACK-DEAD` 旧写「本轮只文档化、代码零改动、待裁决」容易被读成「什么都没做」；准确口径是**文档化已落、等的是删/接线裁决**。2026-11-12 裁决 = 删，已收口并归档（commit `5729395`）。
> - `CORE-RISK-CREDENTIAL-PATH` 的验收从「200/401/401」订正为「200/401/403」（无凭证 401、凭证有效但角色不足 403）；真跑取证见归档（commit `a916a51`）。
> - `X-PROBE-NAMING` 收口归档：data 已摘 `/health` 别名，core 侧 e2e 复验全绿。
> - `CORE-ANALYSISARTIFACT-RETENTION` 复核：ADR-039 T1 **未触发**，保持「等 T1」挂起（commit `ee9a8cf`）。

## 一、未做 / 挂起 / 外部依赖

> 本节 = 本轮（2026-11-11）台账重整时登记的遗留边界。此前 74 张已收口单的正文与施工记录全部在归档文件里，本文件不再镜像。

### CORE-ANALYSISARTIFACT-RETENTION · `internal/analysisartifact` 生产分析产物，是否属本仓 [P2] ⏸ 等 T1（当前未触发）

> **状态（2026-11-12 复核）**：ADR-039 T1 **未触发**，判据「core 报告出口归零」不成立 ——
> `GET /api/backtest/:id/report` 仍由 core 产出（`cmd/analysis/handlers_backtest.go:371`
> 调 `reporting.RenderHTML`），`internal/analysisartifact` 仍在组装并向 analytics 发布产物。
> 本条是**等 T1 的挂起**（不是含糊的「暂缓」）：T1 触发前不施工、不迁组装。

- **① 目标**：记录「core 在生产分析产物」这一上下文渗出的**保留决定与退出条件**，防止它继续无声扩张。
- **② 上下文**：`internal/analysisartifact/builder.go` 从 core 的 run 终态 + `factor_returns` + 因子 z-score 组装 `PortfolioSnapshot`/`FactorExposures`，再发布给 analytics（`publisher.go:198-236`）。DDD 审查 C6 判它是「分析上下文的渗出」；但产物输入全在 core 进程内、analytics 又不回写，迁走会制造跨仓取数。
- **③ 要求**：**本轮不改代码**。保留在 core，条件：① 只组装 core 已持有的 run 终态，不做新计算；② 不新增对外读面（analytics 仍通过现有 artifact 指针消费）；③ 产物形状变更必须同步 analytics 的消费侧（`internal/analysis` 的 reader）。
- **④ 约束与非目标**：不迁 analytics、不新增 `analysis.*` 写者；不据此扩产新产物类型。
- **⑤ 验收**：本条无施工；以 ADR-039 T1 是否触发作为退出信号（2026-11-12 复核：未触发）。
- **⑥ 边界**：产物组装与发布；指标公式归 core `pkg/backtest/metrics`（ADR-039）。
- **重开条件**：**等 T1（当前未触发）** —— ADR-039 T1 触发（core 报告出口归零、analytics 成为唯一报告产出面）⇒ 届时再把组装迁往 analytics 并新立 ADR。

### CORE-MARKET-INDEX-DAILY-EXCEPTION · `market.index_daily` 的跨 schema 建表权 [P3] 不改（已裁决）

- **① 目标**：登记该例外**不改**的理由，避免下轮审查重复提出。
- **② 上下文**：`market.index_daily` 的 `CREATE TABLE` 在 core（`pkg/storage/postgres.go`），owner = core 是第三轮裁决 ⑰ 的结果；`internal/repoguard/storage_market_tables_retired_test.go:18,79` 把它钉成机器判据（其余 `market.*` 在 core 零建表，违者红）。本例外只覆盖 `index_daily` 的建表权；`index_constituents` 的写者归 data（`tellurion-data:DATA-INDEX-CONSTITUENTS` 已于 2026-11-12 收口），与本条无关。
- **③ 要求**：无施工；若将来 data 接管该表，必须先改裁决 ⑰ 与 guard test，再迁 DDL。
- **④ 约束与非目标**：不因「DDD 看起来不纯」而迁表；不新增第二张 core 拥有的 `market.*` 表。
- **⑤ 验收**：guard test 持续绿即可。
- **⑥ 边界**：DDL 归属与 guard test。
- **重开条件**：data 侧正式承接指数日频（含回填与读面切换）并新立 ADR。

### CORE-BROKER-RUNTIME-RECHECK · broker 通道性质只在启动期核对一次 [P1] ⏸

- **① 目标**：运行期对端被替换 / 重启成 mock 时，core 能发现并拒绝下单，而不是沿用启动期那一次核对结论。
- **② 上下文**：`pkg/brokerhttp/trader.go:122` 定义 `verifyPeerChannel`，`:177` 在构造期调一次（读对端 `/version` 的 `channel` 并与 `BROKER_CHANNEL` 比对，不一致 / 不可达 / 无 `channel` 一律拒绝启动），此后整个进程生命周期不再复校；`pkg/brokerhttp/trader_channel_test.go` 只覆盖构造期四条分支。今天把 broker 换成 mock 实例再重启，core 不会知道。
- **③ 要求**：① 在「下单前」或「固定周期」二者中定死一种复核时机并写明理由；② 失败语义与启动期同口径（fail-closed，绝不 fake success）；③ 复核失败要有可观测计数，不能只留一行日志。
- **④ 约束与非目标**：不改启动期核对与 `BROKER_CHANNEL` 契约；不引入服务发现；不做多副本。
- **⑤ 验收**：真进程把对端换成另一 channel 的替身后，下一笔下单被拒且错误码与启动期一致；破坏验证 = 去掉复核 ⇒ 用例红。
- **⑥ 边界**：只改 core 消费面；对端 `/version` 的内容归 `tellurion-broker`。
- **重开条件**：交易面进入「多环境共用一套 core」，或 `X6c` 真钱通道重开（真钱场景下运行期换端不可接受）。

### CORE-RISK-SNAPSHOT-RELOAD · risk 快照只在启动期加载一次，无热更新 [P1] ⏸

- **① 目标**：运行期新增 / 调整审批规则后，core 的进程内风控能生效，不必重启。
- **② 上下文**：`cmd/analysis/wiring/risk_rules.go` 在装配期把 risk 快照换算进 `pkg/risk`（`SVC-CORE-RISK-RETIRE` 收口时落的快照换算与 wiring 测试），运行期不再拉取；`config/analysis-service.yaml` 的 `risk_service.resolve_enabled` 也只控制启动期那一次。改风控 = 重启 core。
- **③ 要求**：① 定死刷新触发（定时 / risk 推送 / 审批通过后回调）与失败时的降级口径（继续用旧快照并告警，不是清空）；② 快照切换必须原子，不得出现半份规则；③ 刷新失败不得静默。
- **④ 约束与非目标**：不把 risk 的裁决权搬回 core；不改 `pkg/risk` 的快照格式（那是 `contracts/risk` 的冻结形状）。
- **⑤ 验收**：真栈里改一条规则 → 不重启 core → 下一次判定按新规则执行；破坏验证 = 关闭刷新通道 ⇒ 新规不生效且告警可见。
- **⑥ 边界**：规则真相源归 `tellurion-risk`；本条只做 core 侧的消费与刷新。
- **重开条件**：出现「不重启就要改风控」的运维需求，或 risk 侧提供版本化快照的推送端点。

### CORE-AUDIT-FULLSTACK-2026-11 · 12 仓全面审查（产品→DDD→整洁度→最佳实践，W0~W7）[P1] 🔶

- **① 目标**：按主任批准的计划完成 12 仓全维度审查，产出逐项判定表（P-1…X-3，每项带证据坐标），发现全部走 ODR（自 068 起编）+ 各仓六字段立单；Critical/High 当场修复或当场立单。
- **② 上下文**：计划文件 = `C:\Users\ruoxi\.qoder-cn\plans\gentle-creek-tench.md`（主任已批，**不落仓**）；14 个工作流 / 风险分层 T0~T3 / 波次 W0~W7。去重基准 = 本文件 §一 开放项与 ⏸、§三 DDD C1~C13、ODR-043/065 闭项、第四轮目的对齐审计；工作件在 `C:\Users\ruoxi\.qoder-cn\plans\audit-w0\dedup.md`（仓外，非报告文件）。
- **③ 要求**：① 每波开工前按仓规立单，一 task 一 commit；② 真退出码 + 证据分级 E1~E4，环境降级须标注；③ 护栏破坏验证用绝对数值断言 + 「调大阈值」腿，改坏/写操作/起服务先拿主任授权；④ 派工前提先证伪；⑤ 每波做完停下核对，结论反转处放最前面。
- **④ 约束与非目标**：不写独立 Report .md；已裁例外不重报（只验钉住护栏）；⏸/⛔ 只核解冻条件；本单只做审查与立案，**代码修复另立单**，不在审查 commit 里混修复。
- **⑤ 验收**：逐项判定表齐（通过/不通过/已裁例外 + 证据坐标）；真跑项全 E1/E2；发现全部进 ODR/台账或归入已裁例外；主任逐波 checkpoint 签收 + W7 总验收。
- **⑥ 边界**：审查证据、ODR 立案、各仓立单；修复施工归各修复单。

> **施工记录（2026-11-12，全栈运行时验收，E1 实跑/复用运行中栈）**：
> **环境**：Docker Desktop，复用已运行的 `--profile full` 栈（未重建），8 容器全 healthy ——
> `data-service` :8081 / `broker` :8091 / `analysis-service` :8085 / `ai-service` :8086 /
> `risk` :8089 / `strategy` :8090 / `analytics` :8087 / `orchestration` :8088（全部只发布在
> `127.0.0.1`）。宿主机 PG 16.15 :5432，UI dev server :5173。
> **通过**：
> 1. **服务健康** 8/8 探针 200 —— analysis/ai `GET /health`；data/broker/risk/analytics/orchestration
>    `GET /healthz`；strategy `GET /healthz` + `GET /readyz`（`{"status":"ready","store":"postgres"}`）。
> 2. **鉴权姿态** 默认栈 `JWT_SECRET` 空 + `AUTH_INSECURE_EXPOSURE=loopback-published`（ADR-026 第三态）：
>    受保护写面按设计放开 —— `POST /api/backtest` 无 token 200、伪造 token 同样 200（该形态不验签）；
>    `POST /api/execution/orders` 无 token 201。带 secret 的三态（service 200 / viewer 403 / 无 token 401）
>    已由 `CORE-RISK-CREDENTIAL-PATH` 隔离实例取证归档，本轮不重复起隔离栈。
> 3. **数据面读** `GET /datasets/resolve?symbols=600519.SH&from=2024-01-01&to=2024-03-31&freq=1d&adjustment=qfq&as_of=2024-04-01`
>    → 200，返回 `dataset_hash=69272fd6…` 与 metadata（`data_version=1d:2025-12-31+n=9780`）；
>    `GET /api/ohlcv/600519.SH?start_date=20240101&end_date=20240131` → 200（22 根日线）。
>    （注：旧参数名 `start/end` 会 400 `start_date and end_date required`，是契约内行为，不是缺陷。）
> 4. **数据面写** `POST /ingest/points`（`ohlcv_daily`，600519.SH / 2026-01-15 全 OHLCV 字段）→ 200
>    `{"received":1,"persisted":1,"skipped":0}`；**验收后已删除该行**，`select count(*)` 复核 0，无残留。
>    反例观察：缺 `open` 等必填字段时 500 `null value in column "open" ... violates not-null`（库层拒绝，未静默写半行）。
> 5. **回测链路** `POST /api/backtest` 三策略全部 `completed`：`momentum`（2024-01-02~02-02，1 票，return 0.000025）；
>    `multi_factor`（2024-01-02~03-29，3 票，13 笔，return 0.000946）；`value_screening`（同区间，3 票，6 笔，
>    return -0.000153）。读面 `GET /api/backtest/{id}` 200、`GET /api/backtest/{id}/report` 200（14,631 字节 HTML）。
> 6. **执行写面** `POST /api/execution/orders`（market mock）→ 201，`broker_order_id=MOCK-000017`；
>    `GET /api/execution/account` 200（broker 通道实为 mock，与 `BROKER_CHANNEL=mock` 一致）。
> 7. **UI 关键路径** :5173 首页、`/backtest`、`/screener`、`/strategy-lab` 均渲染出页面主体
>    （标题分别为 控制台 / 回测引擎 / 选股与市场 / 策略库），console 无 JS 异常、network 无 4xx/5xx。
> **失败**：无。
> **未覆盖 + 原因**：
> 1. **带 `JWT_SECRET` 的全闸形态**未在本轮重跑 —— 复用栈是开放态；三态证据在
>    `CORE-RISK-CREDENTIAL-PATH` 归档（隔离实例，2026-11-12），重复起隔离栈只会增加写窗口。
> 2. **`-race` 竞态门**未跑 —— 本机无 gcc（W0 已登记），一律以 CI 为准（E2）。
> 3. **UI 表单内提交回测**未做 —— UI 本轮只验加载/导航；回测提交链路已在 API 层真跑，
>    页面表单交互留 W6 UI 波次。
> 4. **外部行情同步（tushare 拉取）**未跑 —— 会写入行情表并依赖外部配额，不属"复用运行中栈"的验收范围。
> **结论**：全栈运行时验收通过（7 项通过 / 0 失败 / 4 项未覆盖且各有原因）；
> **本条 W1~W7 审查波次仍未完成**，保持 🔶。

- **施工记录（W0 基线，2026-11-11）**：12 仓工作树全干净（in-place = 干净 checkout），HEAD 锚定：core `27e0c15` / agent `8ace9b9` / analytics `cc440f0` / broker `fbaa9fe` / contracts `fb19870` / data `2fdf48b` / docs `975a403` / kit `7d7c3a8` / orchestration `ab2ee94` / risk `693f590` / strategy `03a5d91` / ui `90d0a2c`。**基线红名单 = 空**：10 Go 仓 gofmt/build/vet/test 全 EXIT=0；contracts 5 护栏 + conformance 全 0；kit 2 护栏全 0；core 4 Python 检查器全 0；docs 3 检查器 + 38 条自测全 0（guide_refs 首跑 GBK 假红，UTF-8 复跑绿）；ui lint/typecheck/vitest 全 0（**945 条 eslint warnings 不红**，T-1/K-7 候选发现留 W6 立案；vitest 38 文件全过）。agent 两条历史红（QT-17 ③ 端点字面量）已随数据面退役消解（grep 零命中）。DB 依赖腿无 DSN 按设计 skip，留 W3/W5/W6；-race 本机无 gcc，只认 CI。**W0-3 C2 核实**：research_tool vault 镜像回落有契约授权（legacy §3.3/§3.5 + agent db-schema.md:73 + hermes tools yaml:445），不构成违规；治理观察一条（C2 规范文本住归档层）留 W1 G-4。日志：`C:\Users\ruoxi\.qoder-cn\plans\audit-w0\<repo>.log`。

#### 2026-11-13 五角审查 · 档 2（产品设计）

> 来源：`tellurion-docs:docs/reviews/2026-11-13/01-product-design.md`。本档每条先判性质（能力已具备未暴露 / 承诺写高 / 不该做 / 真缺功能），再决定立单或挂起；需主任拍板的标 ⏸ 并写重开条件。

### CORE-S1-EXIT-EVIDENCE · S1 出口整链验收：无人干预连续 100 次实验 [P1] ⏸ 前置不成立（等隔离全栈 + LLM 配额）

> 档 2（产品设计）· 来源：01-product-design P1-1 · 顺序 2-1 · 依赖：agent 探索循环、data 数据面、risk、orchestration；开工前先证伪 LLM 配额与数据覆盖

- **① 目标**：用可复现证据证明 S1 出口——无人干预连续跑完 100 次实验，产出逐轮终态、失败分类与可追溯 run id。
- **② 上下文**：`ROADMAP.md:18` 明写"今天尚未达成"；core/agent 台账均无整链连续运行记录，现有证据只到单轮与三策略全栈验收（本文件 `CORE-AUDIT-FULLSTACK-2026-11` 施工记录）。瓶颈可能是 LLM 配额、沙箱、数据缺口或稳定性，未验证前不得假设。
- **③ 要求**：① 先做 10 次连续预跑，逐轮记录终态 / 耗时 / 失败原因；任一失败先定位，再决定是否扩到 100；② 用 opt-in 集成用例或脚本驱动，不把 100 轮塞进单测；每轮留 run id、策略、数据集 hash；③ 结果按「completed / invalid / 环境失败」三类出表，环境失败单列、不计入成功率；④ 证据按版本化/CI artifact 口径留存（与档 4 `CORE-TEST-EVIDENCE-VERSIONED` 一致）。
- **④ 约束与非目标**：不用 mock 冒充 LLM 或数据；不把失败轮静默重试到成功；不改 S1 出口定义；不借本条顺手改探索算法。
- **⑤ 验收**：100 次连续运行完成并附三类结果表；成功率与失败原因可复核；至少一条破坏验证（如断掉 LLM key ⇒ 明确环境失败，而非假 `completed`）。
- **⑥ 边界**：验收驱动与证据归 core；agent 探索循环的缺陷在 agent 另立单，不在本条修。
- **前置**：若 10 次预跑暴露阻塞缺陷，本条转 ⛔ 并先修缺陷；环境不满足时如实记"未覆盖 + 原因"，不得改判为通过。
- **状态（2026-11-13 主任裁决）**：**⏸ 挂起**。本条的前提在现状下不成立，且不得为关账
  借用/消耗正在运行的 `tellurion-next` 栈（那是唯一活跃全栈，且 LLM 凭证只注入其容器内）。
  证据：① 可用的**隔离**全栈不存在（本地 8085/8765/8086 未起，`docker ps` 无隔离实例；
  `tellurion-next` 禁碰）；② 100 次真实实验需要 LLM 配额，本单④ 明确「不用 mock 冒充
  LLM」，无配额即无法产生合法证据；③ 无授权消耗。三项都不是 core 代码缺陷，
  因此不转 ⛔（没有待修的阻塞缺陷），保持 ⏸、不计入开放项。
- **重开条件（主任原话）**：用户提供**可用的隔离全栈**（含 **LLM 配额**）并**授权消耗**；
  满足后另立施工单，先 10 次预跑再决定是否扩到 100，按 ③ 的三类结果表留证。

> `CORE-STOCKSTATE-DISPOSITION` 已按主任裁决 **B 退役**（2026-11-13），原文与施工记录见 [归档 §二十七](archive/tasks-archive-2026-11.md)。

#### 2026-11-13 五角审查 · 档 3（实现）

> 来源：`tellurion-docs:docs/reviews/2026-11-13/02-implementation.md`。以下均为只读静态审查确认的实测项；`stockstate` 的去留已在档 2 立单，本档只补护栏与分叉收敛。

> 本轮（2026-11-13）档 3 已完成 4 条（`CORE-REPOGUARD-HANDLER-WIRED` / `CORE-DATA-URL-SINGLE-DEFAULT` / `CORE-API-V1-INDEX` / `CORE-BACKTEST-REQUEST-UNION`），原文与施工记录已逐字移入 [归档 §二十六](archive/tasks-archive-2026-11.md)。

### CORE-BACKTEST-ENGINE-SPLIT · 回测引擎超大文件切分（登记不实施）[P2] ⏸

> 档 3（实现）· 来源：02-implementation P2-5 · 顺序 3-5 · 依赖：无

- **① 目标**：降低 `pkg/backtest` 撮合核心的单文件影响面（本轮只登记，不施工）。
- **② 上下文**：`pkg/backtest/engine.go` 2237 行 + `engine_daily.go` 1128 行；`pkg/backtest` 下测试文件 70+ 个（单文件最大 1341 行）。测试密度是优点，但引擎核心体量让局部改动的影响面难以静态评估。
- **③ 要求（重开时）**：先补 characterization tests，再按"纯撮合 / 日频编排 / 数据访问"切分；禁止纯格式化搬迁。
- **④ 约束与非目标**：不改变撮合、对账、滑点、涨跌停语义；不为切分而切分。
- **⑤ 验收（重开时）**：切分后全量测试与基准同量级（`BenchmarkBacktest` 约 88979200 ns/op 不得显著劣化）。
- **⑥ 边界**：`tellurion:pkg/backtest`。
- **重开条件**：下一次需要同时改 `engine.go` 与 `engine_daily.go` 的撮合/日频语义，或新增交易所口径时。

> `CORE-DEAD-PACKAGE-DISPOSITION` 已按主任裁决 **工具 9 包保留 / 交易三包退役**（2026-11-13），原文与施工记录见 [归档 §二十七](archive/tasks-archive-2026-11.md)。

#### 2026-11-13 五角审查 · 档 4（测试）

> 来源：`tellurion-docs:docs/reviews/2026-11-13/03-testing.md`。core 的 CI DB 腿已于 2026-11-12 收口（见归档 `CORE-CI-DB-LEGS-SKIPPED`），本档不再重复立 DB 腿；7 个服务仓的 DB 腿与覆盖率门在各仓立单。

> 本轮（2026-11-13）档 4 已完成 4 条（`CORE-E2E-FAIL-LOUD` / `CORE-INTEGRATION-TAG-CI` / `CORE-COVERAGE-FLOOR` / `CORE-TEST-HERMETIC`），原文与施工记录已逐字移入 [归档 §二十六](archive/tasks-archive-2026-11.md)。

### CORE-TEST-EVIDENCE-VERSIONED · 测试证据进入版本控制或 CI artifact [P2] ⏸ 待主任裁决

> 档 4（测试）· 来源：03-testing P2-5 · 顺序 4-5 · 依赖：主任裁决 A/B

- **① 目标**：审查/验收所依赖的测试证据不再只活在仓外工作目录。
- **② 上下文**：`reports/` 在 core 为 0 tracked 文件，全栈验收证据写在仓外 `.qoder-cn/plans/` 与 untracked `reports/`；换机器/清理后不可复核。审查建议"纳入版本控制（或改为 CI artifact）"。
- **③ 要求**：建议 **B CI artifact + 仓内摘要**：完整日志/覆盖率上传 artifact，仓内只提交结论摘要与 run 链接；备选 **A 提交 `reports/`**（仓体积与噪声需评估）。
- **④ 约束与非目标**：不提交大文件/二进制；不把证据塞进业务目录；不改测试本身。
- **⑤ 验收**：B——CI 有 artifact 上传且保留期明确，仓内有可追溯摘要；A——`reports/` 有索引与保留策略。破坏验证 = 删除摘要链接 ⇒ 文档检查红。
- **⑥ 边界**：core CI + `reports/` + 文档；跨仓证据归各仓。
- **重开条件**：主任裁决 A 或 B（建议 B）。

#### 2026-11-13 五角审查 · 档 5（文档与代码一致性）

> 本轮（2026-11-13）档 5 已完成 2 条（`CORE-OPENAPI-MEASUREMENT-INDEX` / `CORE-CONTRACTS-DIR-NAMING`），原文与施工记录已逐字移入 [归档 §二十六](archive/tasks-archive-2026-11.md)。

## 二、跨仓指针（不在本仓，勿双写）

| 事项 | 去哪 |
|---|---|
| 六服务各自的就绪门、挂起项与重开条件 | `tellurion-data` / `tellurion-strategy` / `tellurion-risk` / `tellurion-analytics` / `tellurion-orchestration` / `tellurion-broker` 的 `docs/TASKS.md` |
| 契约形状与工具层 | `tellurion-contracts:docs/TASKS.md` / `tellurion-kit:docs/TASKS.md` |
| 文档、ADR 与 NT 侧车升级 | `tellurion-docs:docs/TASKS.md` |
| agent / ui 本仓任务 | 各自 `docs/TASKS.md` |

> 旧的「八、跨仓任务索引」逐条状态快照（多数已过期）随本轮重整移入归档，**状态以各仓台账为准**。

## 三、DDD 边界审查 13 条冲突台账（2026-11-11）

> 来源：`tellurion-docs:docs/design/ddd-boundary-review-2026-11-11.md` §4。**每条都有落点**：要么对应一个六字段任务，要么写明「不改的理由」或「判定不存在」。状态以本表为准；各仓任务细节看对应仓台账。

| # | 冲突（一句话） | 处置 | 落点 | 状态 |
|---|---|---|---|---|
| C1 | agent 三个因子工具打向 core 不存在的端点 | 改代码：显式「未提供」，不补端点 | 跨仓 ADR-041；`tellurion-agent:AGENT-FACTOR-TOOLS-UNAVAILABLE` | ✅ 2026-11-11 |
| C2 | data `db-schema` 的 SPEC-R-02 把因子域 owner 写成 data | 改文档（已改） | `tellurion-data:DATA-FACTOR-OWNER-DOC`（`docs/db-schema.md` §1.3/SPEC-R-02 + `docs/ARCHITECTURE.md` §2.1；收口记录在该仓 `docs/archive/tasks-archive-2026-11.md`） | ✅ 2026-11-11 |
| C3 | `factor_returns`/`ic_analysis` 孤儿表、产物组装静默跳过 | 裁决归 core + 改代码（说明缺失原因） | 跨仓 ADR-038；`CORE-FACTOR-DERIVED-GUARD`（见归档 §二十三） | ✅ 2026-11-11 |
| C4 | core 与 analytics 指标双实现、无权威条款 | 新建 ADR + 漂移检测 | 跨仓 ADR-039；`tellurion-analytics:ANALYTICS-METRIC-DRIFT` | ✅ 2026-11-11（真实 run artifact 腿 ⏸，见 Analytics 归档重开条件） |
| C5 | risk 自持 `Snapshot`，与 `contracts/risk` 双定义 | 改代码：别名引用 contracts | 跨仓 ADR-040；`tellurion-risk:RISK-CONTRACTS-SNAPSHOT-MIGRATION` | ✅ 2026-11-11（真栈 hash 对拍腿 ⏸，见 Risk 归档重开条件） |
| C6 | core `internal/analysisartifact` 生产分析产物 | 保留 + 记退出条件（本轮不改代码） | `CORE-ANALYSISARTIFACT-RETENTION` | ⏸ 挂起 |
| C7 | core ↔ agent 运行期「双向调用」的文档口径 | 改文档：core→agent 是 HTTP 反代，无领域语义 | `tellurion-docs:DOC-15`（`DOC-CORE-AGENT-PROXY-NOTE`，`docs/REPO-MAP.md` 依赖图脚注；收口记录在该仓 `docs/archive/tasks-archive-2026-11.md`） | ✅ 2026-11-11 |
| C8 | `GET /api/factors/:name` 死链（反代 data 已撤路由） | 改代码：显式退役 | `CORE-FACTOR-DEAD-PROXY`（见归档 §二十三）；UI 消费方登记为 `tellurion-ui:UI-FACTOR-CITATION-RETIRED` | ✅ 2026-11-11 |
| C9 | compose/agent 注释称 `/api/factor/*` 已注册 | 改注释（事实订正） | `CORE-COMPOSE-FACTOR-COMMENT`（见归档 §二十三）+ agent 同批 | ✅ 2026-11-11 |
| C10 | `analysis.benchmarks` 零写者（K7） | 不改（维持挂起，重开=出现写消费者） | `tellurion-analytics` K7 条目 | ⏸ 已挂起 |
| C11 | contracts 版本列 v0.9.0 与 `go.mod` 实测 v0.10.0 不一致 | 改文档（已改） | `tellurion-docs:DOC-16`（`DOC-REPO-MAP-VERSION-CORRECTION`，`docs/REPO-MAP.md` 版本列与推进顺序；收口记录在该仓 `docs/archive/tasks-archive-2026-11.md`） | ✅ 2026-11-11 |
| C12 | `market.index_daily` 由 core 建表（跨 schema 例外） | 不改（第三轮裁决 ⑰ + guard test 已固化） | `CORE-MARKET-INDEX-DAILY-EXCEPTION` | 不改（已裁） |
| C13 | `public.factor_cache` 无写者、表未收尾 | 裁决 + 按需求值实现；表删除另立单 | 跨仓 ADR-037 §6；`CORE-FACTOR-CACHE-WRITER` ✅ 2026-11-12（见归档 §十三）；`CORE-FACTOR-CACHE-RETIRE` ✅ 2026-11-12（删表，见归档 §十五） | ✅ 2026-11-12（实现 + 删表） |

**判定「不存在」的条目**：无。13 条全部有代码或文档证据，无一条因前提不成立而关单。

**清单外的顺手发现**：只登记、不实现；本轮未新增此类条目（如后续发现，追加到本表末尾并标注「清单外」）。

## 四、跨仓批次（X-，本仓为协调伞）

> 这些是**跨多仓的协调批次**，不是 core 单仓施工。本仓台账是总序伞（docs 仓 AGENTS §4 禁止施工单进 docs），故登记在此；**每仓的实际改动落该仓自己的 commit**，按单号串行。批次内各仓的子项在各仓台账以指针引用本批，不双写正文。

### X-PROBE-CANONICAL-HEALTHZ · 探针名统一为 `/healthz`（core + agent）[P2] 🔶 core 腿 ✅ 2026-11-13／agent 腿待发

> 档 3（实现）· 来源：02-implementation P2-1 · 顺序 3-7 · 依赖：无；agent 腿见 `tellurion-agent:docs/TASKS.md` → `AGENT-PROBE-HEALTHZ`

- **① 目标**：12 仓探针名只有一个规范：`/healthz`；`/health` 在兼容期内保留为 deprecated 别名。
- **② 上下文**：data 已按已收口的 `X-PROBE-NAMING` 统一为 `/healthz` 并删除 `/health`（`tellurion-data/internal/httpapi/server.go:121-126`）；core 仍只有 `GET /health`（`cmd/analysis/main.go:309`），agent 仍只有 `GET /health`（`tellurion-agent/cmd/ai/main.go:128`，免鉴权白名单写死 `/health` 于 `cmd/ai/lifecycle.go:143`）；`docker-compose.yml` 按各自实际路径分别配置（core `:274` `/health`、data `:160` `/healthz`），所以今天不坏，只是跨仓不统一。
- **③ 要求**：① core 增加 `GET /healthz`，`/health` 保留 deprecated 别名一个发布周期；② 同步 core 的 rate-limit 豁免测试（`cmd/analysis/rate_limit_exempt_test.go:45`）、compose healthcheck、e2e 探针断言、`SERVICE.md`；③ 下一周期删除别名（删除单另立）。
- **④ 约束与非目标**：不一次性删除 `/health`（避免滚动升级断探针）；不改 `/readyz` 语义；其余 10 仓只核验不改。
- **⑤ 验收**：core `/healthz` 200 且内容与 `/health` 同构；compose 健康检查全绿；e2e 探针断言改用 `/healthz`；`grep` 活跃文档无"core 只有 /health"表述。
- **⑥ 边界**：core + agent 施工；data 已符合。

> **施工记录（2026-11-13，E1，core 腿）**：`cmd/analysis/main.go` 新增
> `GET /healthz`（规范名）；`/health` 与 `/api/health` 保留为 deprecated 别名，
> 同一 handler、响应体同构，附 `Deprecation: true` + `Warning: 299` 头。
> 同步改动：`pkg/bootstrap/server.go` 限流豁免名单加 `/healthz`（硬编码断言同步
> 更新，并新增行为腿）、`cmd/analysis/Dockerfile` 与 `docker-compose.yml`
> （analysis-service）HEALTHCHECK 改 `/healthz`、`e2e/tests/integration_test.go`
> 四处探针断言改 `/healthz`、`SERVICE.md` / `docs/runbook.md` /
> `docs/STACK-TEST-PREREQ.md` / `docs/openapi.yaml`（新增 `/healthz`，
> `/health` 与 `/api/health` 标 `deprecated: true`）同提交更新。
> 结果：`go test ./cmd/analysis ./pkg/bootstrap ./e2e/guard -count=1` 全 PASS。
> 破坏验证：注释掉 `router.GET("/healthz", …)` ⇒ `TestHealthzIsCanonicalProbe`
> FAIL（404）；还原后复跑 PASS。
> **对侧仓待办（未跨仓改动）**：agent 需新增 `GET /healthz` 并把
> `cmd/ai/lifecycle.go` 免鉴权白名单改为 `/healthz`（`/health` 留别名）；
> agent 落地后 core `docker-compose.yml` 的 ai-service 健康检查（`:355`）才可切
> `/healthz` —— 现在切会把未升级的 agent 判 unhealthy，故本腿保持 `/health`。

### X-COVERAGE-FLOOR · 12 仓覆盖率门统一抬到实测低 5 点 [P2] 🔶 core 腿 ✅ 2026-11-13／其余仓待各自施工

> 档 4（测试）· 来源：03-testing P2-2 · 顺序 4-3 · 依赖：各仓先测后设，互不阻塞

- **① 目标**：每个有测试的仓都有能拦住回退的覆盖率门；无门的仓补门。
- **② 上下文**：core 总量门 52% vs 实测 72.5%；contracts/kit 自述"75% 门槛无承接者"；7 个服务仓未见覆盖率门；UI `package.json` 无 `test:coverage`。
- **③ 要求**：逐仓落腿——core `CORE-COVERAGE-FLOOR`；contracts `CT-COVERAGE-FLOOR`；kit `KIT-COVERAGE-FLOOR`；服务仓随各自 `*-CI-GATES`；ui `UI-CI-COVERAGE`。统一口径：门槛 = 本仓最新实测 - 5 点，先测后设，不猜数。
- **④ 约束与非目标**：不靠删测试/空壳测试抬覆盖；不设跨仓统一百分比（各仓基线不同）；不阻塞于 core。
- **⑤ 验收**：12 仓中每个有测试的仓都有门；每仓至少一次"删测试 ⇒ 红 / 还原 ⇒ 绿"的破坏验证记录。
- **⑥ 边界**：各仓自己的 CI；伞只做口径与对账。

> **core 腿施工记录（2026-11-13，E1 实跑，commit `2f2b790`）**：
> 隔离 PG16（容器 `:55433`，迁移后 `-p 1` 全量）实测 total **72.6%** / `pkg/backtest`
> **82.4%**，按「实测 − 5 点」把 `.github/workflows/ci.yml` 与 `Makefile` 同步改为
> `-total-min=67`、`-pkg-min=tellurion/pkg/backtest=77`；新增
> `internal/repoguard/coverage_floor_test.go` 冻结这组数字并断言 CI/Makefile/AGENTS.md
> 三处逐字一致。门禁有效性证据：把 `pkg/storage`+`cmd/analysis`+`pkg/marketdata`
> 的已覆盖语句清零（total 59.4%）⇒ 旧阈值 52/65 PASS（不执法）、新阈值 67/77
> FAIL「差 7.6 个百分点」。**⑤ 的「删一个测试文件即红」在 core 不成立**（单文件
> 占比远小于 5 点余量，移走 `pkg/backtest/qt04_duplicate_signal_scan_test.go` 后
> 仍 82.4%），已改用「成片回退」验证并如实登记。
> **对侧仓要求（回报给各仓）**：contracts `CT-COVERAGE-FLOOR`、kit
> `KIT-COVERAGE-FLOOR`、ui `UI-CI-COVERAGE` 及各服务仓 `*-CI-GATES` 按同一口径
> （本仓最新实测 − 5 点，先测后设）各自落腿；core 侧**不代改**。

### X-CI-BASELINE-PIN · CI 工具链版本与执行次数收敛 [P3] 🔶（core 腿已落，其余仓待各自施工）

> 档 4（测试）· 来源：03-testing P3-1 · 顺序 4-6 · 依赖：无

- **① 目标**：10 个 Go 仓的 staticcheck 版本一致、agent 有 gofmt 门、避免同一套件跑两遍。
- **② 上下文**：data/risk/strategy 固定 `staticcheck@v0.8.2`，core/agent 等用 `@latest`；agent CI 无 `gofmt -l` 门（本地可能带格式漂移）；部分仓先全量 `go test` 再 `-race` 全量，重复执行。
- **③ 要求**：① 选定一个已在 CI 验证过的 staticcheck 版本并写入 10 仓 CI（施工时先确认当前可用版本，不凭空指定）；② agent 加 `gofmt -l` 门（非空即红）；③ 把"全量 + 全量 -race"改为"`-short` 全量 + `-race` 关键包/全量二选一"，并写明理由。
- **④ 约束与非目标**：不引入新工具；不改业务代码；不在本条改 Windows runner（另见 `X-CI-WINDOWS`）。
- **⑤ 验收**：10 仓 CI 的 staticcheck 版本一致；agent 故意留一处未格式化 ⇒ 红；CI 总时长不高于现状（贴前后对比）。
- **⑥ 边界**：各仓 CI；伞只做口径与对账。

> **core 腿施工记录（2026-11-13，E1 实跑）**：
> - **③① 的前提在 core 已被现状证伪**：② 写「core/agent 等用 `staticcheck@latest`」，
>   但 core 的 CI **根本没有 staticcheck 步**（`grep -rn staticcheck .github/ Makefile`
>   零命中；历史条目 X-CI-RACE-STATICCHECK 也只给 6 个服务仓 + agent + contracts 补，
>   明文「core/kit 的 CI 未被改动」）。实测 `go run staticcheck@v0.8.2 ./...` 在 core
>   报出**成片** U1000/S1009/SA4006/SA1019/ST1005（示例：`pkg/strategy/registry.go`
>   5 条、`pkg/strategy/examples/value_momentum.go` 6 条、`pkg/trading/order_store.go`、
>   `pkg/sync/worker.go` 的 kit 弃用符 …）。按 ④「不改业务代码」，**本单不新装
>   这道门** —— 装了就是当场红，修它要动业务代码，超出本条范围。core 若要接这条
>   staticcheck 腿，需另开一单：先清 finding（或引 baseline 机制）再接门。
> - **core 能落的部分 = 工具版本钉死**：`.github/workflows/ci.yml` 的 Govulncheck
>   由 `govulncheck@latest` 改为 `@v1.9.0`（施工时 `go list -m -versions
>   golang.org/x/vuln` 确认的当前最高可用版；本仓实测 `./...` EXIT=0、0 个可达漏洞）。
> - **③③ 在 core 无需改动**：core 一直是「一次全量 `go test ./... -count=1 -race`」，
>   不存在「先全量再 -race 全量」的重复执行（`Test` 步只有一条 `go test ./...`）。
>   护栏把这一形态钉住，防止将来被改成两遍。
> - **新增护栏** `internal/repoguard/ci_tool_pin_test.go:TestCIToolVersionsArePinned`：
>   run 里不得出现任何 `@latest`；`govulncheck@vX.Y.Z` 必须存在；`go-version`
>   不得是 `stable`/`latest`/`1.26.x`；`go test ./...` 必须恰好出现 1 次且带 `-race`。
> - 破坏验证：把 `@v1.9.0` 改回 `@latest` ⇒ 护栏 FAIL 并点名 `govulncheck@latest`；
>   逐字节还原（`SHA256=994AE72D…3DE87` 一致）⇒ `go test ./internal/repoguard` 全 PASS。
> - **对侧仓要求（回报给各仓）**：agent 仓需 ①加 `gofmt -l` 非空即红门（③②）；
>   ② 若 agent 的 CI 沿用 `staticcheck@latest`，统一钉到已在 data/risk/strategy/
>   contracts CI 上验证过的 `v0.8.2`；orchestration/broker 现为 `@latest`（见归档
>   X-CI-RACE-STATICCHECK 对账表），同批收敛。core 侧**不代改**。

### X-CI-WINDOWS · Windows runner 是否纳入 CI [P3] ⏸ 待主任裁决

> 档 4（测试）· 来源：03-testing P3-1 · 顺序 4-7 · 依赖：主任裁决 + runner 成本

- **① 目标**：决定 Windows 专属代码是否有 CI 覆盖。
- **② 上下文**：全仓 CI 只在 Ubuntu 跑；存在 Windows 专属分支（路径分隔符、进程管理）但无 runner 验证。
- **③ 要求**：建议只给 core 加一条 Windows job（build + vet + 单元测试，不跑 DB/race），而非 10 仓全量铺开。
- **④ 约束与非目标**：不改变 Linux 主门；不引入自托管 runner。
- **⑤ 验收**：core Windows job 绿；能捕获一次 Windows 路径回归（破坏验证）。
- **⑥ 边界**：core CI；是否推广到其他仓另行评估。
- **重开条件**：主任批准 runner 成本与范围（建议仅 core）。

## 五、W5 起栈追加验收（2026-11-12）

> 目的：验证 `CORE-MIGRATION-REPLAY-GUARD` 的迁移账本在**新镜像 + 空库**上的真实行为（首启记账完整、二次启动零重放），**不复用部署库数据**。

- **隔离方式**：独立网络 `w5net` + 独立卷 `w5-pgdata` + 独立容器 `w5-pg`（`postgres:16-alpine`，库名 `tellurion`）；analysis-service 以 `w5-analysis-1` 单跑。全程不指向宿主机 5432 上的部署库，验收后容器/卷/网络全部删除。
- **镜像**：`tellurion-core:w5-20261112`（`sha256:c745594c1c70...`，由 `Dockerfile.service` 本地构建）。
- **启动前提**（缺一即 fatal，实测踩过）：`BROKER_URL` + `BROKER_CHANNEL=mock` + `BROKER_ALLOW_MOCK=true` + `BROKER_SERVICE_TOKEN`（装配期 GET 对端 `/version` 对拍）、`AUTH_INSECURE=true` + `AUTH_INSECURE_EXPOSURE=loopback-published`、**`RISK_SERVICE_RESOLVE_ENABLED=false`**（否则尝试 POST `http://localhost:8089/api/risk/resolve` 后退出）。
- **首启（空库）**：
  - `select count(*), min(version), max(version), max(applied_at) from core_schema_migrations` → `85|1|85|2026-11-12 15:24:00.889372+00`；与代码侧 `allMigrationStatements()` 条数 **85** 一致（版本 1..85 连续、无空洞、无重复）。
  - 账本指纹摘要 `md5(string_agg(version||':'||checksum, ',' order by version))` = `81dde09be08d9935678748fb789bb0a6`（用于二启对拍）。
  - `/health` → `{"service":"analysis-service","status":"healthy",...}`；日志 `fatal` 命中 0。
- **二启（`docker restart w5-analysis-1`，同卷同库）**：
  - 账本仍 `85|1|85`、`max(applied_at)` 不变、指纹摘要仍 `81dde09b…`；
  - 日志 `Applying migration` 命中 **0** 次、`Analysis Service starting` 2 次、`fatal` 0 次 ⇒ **零重放**；且不是靠整段跳过迁移换来的（账本读取与指纹校验照常执行）。
- **结论**：新镜像首启的账本行数 = 代码实际迁移数，二次启动幂等不重放，`CORE-MIGRATION-REPLAY-GUARD` 在真实镜像形态下成立。同一组证据写入 `tellurion-docs:docs/archive/odr/odr-072-audit-wave5-security-reliability-deps.md` 的「追加验收（2026-11-12）」节。

## 维护本文件

- 新增 task 必须按 [AGENTS.md §8.5](../AGENTS.md) 写全六字段；**六字段里必须显式引用冻结契约的路径与节号**（本代 wave-0 的冻结文本在 `docs/design/qt-decoupling/p0-round2-review.md` §8、`p0-remediation-design.md`、`adr-029-amendment-host-api-v2.md`）。
- 完成即标 ✅（附日期）并移入 `archive/`；本文件只留未完成项。换代时整篇加时间戳归档（`archive/TASKS-YYYY-MM-DD-archived.md`），旧代正文不改写。
- **派工前必须先证伪工单前提**（读码核对 → 不符就停下回报 + 给拆法，不硬做、不扩范围）。
- **执行顺序（偏序，非全序；refactor-plan-review M4）**：硬链 **S-1(QT-60 ✅) → QT-61 → S0（QT-06 → QT-18 → QT-08）→ W0 → S1(QT-05)**；S2 段（QT-17 → QT-07 → QT-19 → QT-09）须在 W0 + QT-05 全绿后；**W1 不依赖 S2**——W0 后即可开工，且 **仅 QT-17 可与 W1 并行**（QT-07/09/19 不与 W1 抢档）；W2 在 W1 后、W3 在 W2 后。同波次内撞文件的按 QT 序号串行，一单一 commit。**W0 完成前不开 W1 的代码单**——否则 artifact 与基准都要重做。
- **冻结 schema 变更评审（硬规则；refactor-plan-review H1）**：凡触碰 `ResultSpec` / `RunSpec` / `SignalSpec` / `docs/SPEC.md` 冻结字段的单，六字段内必须列「**schema 变更评审项 + 需同步更新的 SPEC 节号**」，并按固定顺序执行：**先触发合规测试红 → 更新 SPEC 文本 → 走正式变更评审记录 → 才许合入**；未列评审项的单一律不得改冻结面（含「顺手加一列/加一字段」）。
- **合入门禁（硬约束，不是建议）**：⓪ ~~QT-60 未完成前 QT-06 不得开工~~ → **已满足（QT-60 于 2026-10-20 完成）**，tag 现在可以打在新仓；新增 ⓪' **QT-61 的「gofmt + doc-links」两项绿之前，QT-06 的「两仓 CI 绿」验收不可达**——要么先做 QT-61，要么把 QT-06 验收改成「本次改动相关的 job 绿 + 其余 known-fail 登记」；① **QT-18 未合入 main 前，QT-01 不得合入**——第一个 breaking 就在 QT-01 的 abi v2，护栏不在就会漏；② QT-05 未合入前，QT-07 不得开工；③ QT-07/QT-09 未绿前，不得在任何仓新增跨仓 `replace`（D29 §9 C1）。
- **新需求优先于存量未完成任务**；与旧任务重叠的可合并或直接删除，跨仓迁移的任务条目保留可追溯指针。
- 归档文件的状态标记若与自身完成记录矛盾，**只在文件头加订正说明**，条目正文与标题的历史措辞不改写。
