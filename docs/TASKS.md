---
status: active
last-verified: 2026-11-12
verified-by: 2026-11-11 全面审查 W7 台账立单（A5 授权）——§一 新立 12 条 core 六字段单（`CORE-PG-BACKUP-DR` / `CORE-CI-IMAGE-GATE` / `CORE-REDIS-EXIT` / `CORE-MIGRATION-REPLAY-GUARD` / `CORE-K8S-EXIT` / `CORE-DEAD-PROXY-SCREEN-VERIFY` / `CORE-CI-ARCHIVE-LINKS-FIX` / `CORE-DEPLOY-DRIFT-PARAM` / `CORE-RUNNER-FLAKE-RESIDUAL` / `CORE-FEE-ROUNDING-ASSERT` / `CORE-RESULT-RETENTION` / `CORE-REFS-TRAILER-RULE`），新增 §四 5 条跨仓批次伞（`X-DEP-BUMP-CONSUMERS` / `X-AGENTS-DEPENDENCY-TABLE` / `X-CI-RACE-STATICCHECK` / `X-PROBE-NAMING` / `X-LOW-CLEANUP`）；证据坐标引 ODR-068~073（`tellurion-docs:docs/archive/odr/`）；本轮只立单不修复，施工另逐单授权；2026-11-11 DDD 边界审查落单（新增 §三 13 条台账；新立 `CORE-FACTOR-DERIVED-GUARD` / `CORE-FACTOR-DEAD-PROXY` / `CORE-SPEC-FACTOR-ENDPOINTS-STALE` / `CORE-COMPOSE-FACTOR-COMMENT` / `CORE-ANALYSISARTIFACT-RETENTION` / `CORE-MARKET-INDEX-DAILY-EXCEPTION`；跨仓裁决 ADR-038~041）；2026-11-11 `CORE-FACTOR-CACHE-WRITER` 裁决（ADR-037：`factor_cache` 是分析上下文的读模型，owner=core、形态=消费点按需实时，不新增数据库写者；否决 core 预热作业与归 data，四条落地卡点与重开条件已写入任务单，本轮不实现）；2026-11-11 本地编排收敛为单一 `docker-compose.yml`（撤销 QT-79 代裁 `D-DEPLOY-1 = A`，见下方 `CORE-COMPOSE-UNIFY`）：默认 4 服务 + `--profile full` 4 服务、broker 进默认栈、数据库走宿主机原生 16.15、e2e 高额度改命令行 env；2026-11-11 登记 `CORE-RISK-REMOTE-FALLBACK-DEAD`（`risk_service.url` 的三处远程回落恒不可达，本轮只文档化：`config/analysis-service.yaml` 注释 + `docs/SPEC.md` §主要配置段 写实，代码零改动，待裁决删 or 接线）；改写 `CORE-BROKER-STARTUP-PRECONDITION` 的前提（实测三种编排都没给 core 注入 `BROKER_URL`，先后顺序问题排在接线之后）并给 `CORE-BROKER-RUNTIME-RECHECK` 补行号；2026-11-10 data-service 部署面收口（compose 改跨仓镜像 + `DATABASE_URL`/`DATA_MARKET_SCHEMA`，e2e 删死覆盖，k8s configmap/deployment 补 schema 与 DSN，`check_deploy_consistency.py` 新增 5e + 跨仓读取点；checker `exit 0`，四条破坏验证真做真还原；Docker daemon 不可达，未跑容器腿）；2026-11-10 晚 `QT-32` 真资产服务端到端取证（新增 opt-in `cmd/analysis/wiring/strategy_asset_live_test.go`：对真 `assetservice` 进程走 create → source → 登记 artifact → validate → promote(`X-Actor`) → `promotions` 审计读 → `CheckStrategyRef` 放行 `{id}@{v1}` 与 latest；拒绝两条真实路径 = v2 版本 hash 404 `strategy_not_found` / v2 artifact hash 409 `strategy_not_promoted`；`ASSET_SERVICE_URL=http://127.0.0.1:18090 go test ./cmd/analysis/wiring -run TestStrategyAssetGate_LiveAssetService -count=1 -v` PASS，未设 env 时 SKIP；破坏验证把状态位判定临时 `&& false` ⇒ 红，按备份还原本文件 `sha256=E2CBBAC6…712DFF4A` 一致、复跑绿）；2026-11-10 有库腿实测 + `QT-82` 收口（独立 PG 16.15 `:5433` 库 `tellurion_core`：先补 `NewPostgresStore` 运行时迁移、再按序 psql 应用 `migrations/0000000{1,2,3}/up.sql`，`pkg/storage` 11 条有库用例全绿；`pkg/marketdata` 3 条 / `pkg/backtest` 1 条 / `pkg/expression` 1 条原因 `ohlcv_minute`、`market_activity`、`stock_sector_map` 已随数据面退役且全仓无 DDL 而红，裁决 ①a 后新增 `pkg/testenv/market_tables.go` 形状镜像夹具并接入三处测试，5 条全绿（`EXIT=0`），破坏验证删表 + 撤夹具 ⇒ 重回 42P01 红、还原后复绿；`docs/SPEC.md` §数据归属 与 `AGENTS.md` 第 3 条已写跨仓只读表清单）；`QT-32` K11 执行侧闸门（2026-11-10：`strategy_ref` 非空先在资产服务过「`status=promoted` + promote 审计留痕」闸门，同步 handler / worker 旁路 / provenance 身份覆盖三处落地，空 ref 内置路径逐字节不变；三条破坏验证真做真还原、`gofmt -l` 空、`go vet ./...` = 0、`go test ./... -count=1` = 0（ok=70）；strategy 侧读面 `SVC-STRAT-G3-B` 同步 commit `92573b5`）；`QT-32` 收口（2026-11-10 晚：dsl 载体 promote 代裁 B = 不开放无 artifact 载体的版本级 promote，strategy `Promote` 对 builtin/dsl/external 显式 400 `unsupported_carrier` + 两条测试腿 + 破坏验证真做真还原；core 侧闸门无需改动——这三类版本 resolve 恒 404、不会被假放行；`tellurion-strategy` 全套 `gofmt -l` 空 / `go vet` = 0 / `go test ./... -count=1` = 0）；`QT-78` 收口（2026-11-10：真栈真 run `203ae0a198dd02f0530aeb70dfc0c036` 产出 portfolio-snapshot / factor-exposures 两类载荷且 hash 非空、analytics 两条归因恒等式 |Δ|=0、删持仓反证腿真跑出 409 `missing=holdings`；同批修掉「收尾强平清空期末持仓」与「发布期误读引擎级因子缓存」两条生产路径缺陷，`go build/vet/test ./... -count=1` 全 0）；`SVC-CORE-AI-RETIRE` 收口（2026-11-10：七表 DDL/索引/读写实现/启动 seed 清零，当场实测 ①写点 6→0 ②seed 3→0 ③DDL 7→0；core 新增 `internal/repoguard/storage_ai_tables_retired_test.go` 三条反向护栏 + agent 侧缺表尝试，三处破坏验证真做真还原、`go build/vet/test ./... -count=1` 全 0）；`SVC-RISK-G3` 跨仓收口证据（2026-11-10：core 侧新增 `pkg/risk` 新旧限额对拍测试；真 PG + 真 risk 进程的告警投递演练跑出真实行、停服后 outbox 转 `failed/attempts=3` 不静默丢；演练痕迹已清、生产代码零改动）；`SVC-CORE-AN-RETIRE` 收口（2026-11-09：归属裁决 = 保留并冻结；生产侧 import 冻结为 3 条、repoguard 新增/减少均红、破坏验证真做真还原、`go build/vet/test ./... -count=1` 全 0）；`SVC-CORE-RISK-RETIRE` 收口（2026-11-09：14 个 `risk_manager.*` 读点归零、5 个 `alert.*` 阈值改代码常量、`resolve_enabled` 出厂 true、新增 `pkg/risk` 快照换算与 wiring 装配测试、`go build/vet/test ./... -count=1` 全 0）；`SVC-CORE-STRAT-RETIRE` 收口（2026-11-09：三条 grep 生产侧归零 0/0/0、删 DDL / storage 5 方法 / `pkg/strategy/db.go` DB 腿、新建退役护栏 + 两条破坏腿真做真还原、`go build/vet/test ./... -count=1` 全 0）；`SVC-CORE-RUN-RETIRE` WF 半边收口（2026-11-09）；`SVC-CORE-DATA-RETIRE` 收口（2026-11-08：④ 四条 grep 生产侧归零 0/0/0/0、新建退役护栏 + 破坏验证真做真还原、`go build/vet/test ./... -count=1` 全 0；库侧用例因未设 `TEST_DATABASE_DSN` 按既有约定 skip）；`SVC-DEPLOY-01` 收口（2026-11-08：聚合 compose 栈 `up -d --build --wait` EXITCODE=0、7/7 容器 healthy；同轮修 `strategy` 缺 `JWT_SECRET` 的启动闸缺陷；宿主机 PG 占 5432 ⇒ postgres 宿主端口用临时 override 5433，已在正文如实登记）；跨仓补裁 #11~#13 落档（2026-11-08）；QT-77 结案（2026-11-07 晚：真 PG + 真服务补齐风险 resolve 200 / data ingest 200 / 无 token 401 与空 token 破坏腿；同批在 risk 修 PG approve 撞主键缺陷，见 QT-77 ⑥/⑥b/⑦）；QT-51 收口合入（2026-11-07：`task/qt51-execution-port` rebase 到本地 `main@b3f100e` → 唯一冲突 `docs/TASKS.md`（保留 main 侧）→ ff merge `a2e388a`；回滚点 tag `archive/qt51-execution-port`；四道门禁真退出码全 0、`ok`=70 / `FAIL`=0；等价性摘要 `60a2713b…` 与归档报告同值；破坏验证真做真还原）；第五轮·ADR-036 并行服务化 MVP（2026-10-26：六仓=服务、G0/G1/G2 并行、G3 切读串行、旧实现退役总单）；第四轮·目的对齐审计（2026-10-26，见 `docs/design/qt-decoupling/purpose-alignment-audit-2026-10-26.md`；不改既有单状态，只加审计指针）。第 4 代换代（2026-10-20）；**2026-10-20 补分仓批次（ADR-D29 / QT-05~09）与改名批次（ADR-D30 / QT-60）**。来由：ADB-029 abi v2 裁决（D24/D25/D26/D28）+ P0 第二轮深度审查（`docs/design/qt-decoupling/p0-round2-review.md`，N1~N9）。本代 30 张新工单**全部可追溯到设计文档的具体节**（无一条来自猜测）；第 3 代 6 项（DBT-13~18）经前提复核后原样延续。第 3 代正文已归档到 `archive/TASKS-2026-10-20-archived.md`。**2026-10-21 任务台账批量重分布**：QT-05 主单 → `tellurion-contracts`（kit 腿拆 QT-05-B → `tellurion-kit`）、QT-17 → `tellurion-agent`、QT-40~45 + QT-41b → X 线 6 个服务仓（各自 `docs/TASKS.md` 正式六字段单，「占位仓不写正式任务单」旧约定作废）、QT-61 按腿拆分登记（agent/ui 各留本腿记录，core 本文件为完整四腿权威版）；本仓台账收缩为**总序 + 指针**，迁出单正文不镜像。**2026-10-23 B6**：QT-10/11/22 落点已核（只加「落点已核」注记，不改六字段语义），W1 七单仓归属见 `docs/design/qt-decoupling/w1-repo-ownership.md`。**2026-10-23 C1**：QT-07 S2 ①~⑤ 与 QT-09 ①~⑤ 落地（双仓 tag、pin/仓边护栏 18 腿、契约语料 33 条三侧自检、`make contracts-conformance`）；⑥ S3 仍等 QT-50 + QT-17。**2026-10-25 B6-1**：QT-07 ⑥ **S3 结案**——agent 侧已删 `replace tellurion => ../tellurion`（三查：零 import / `go.mod` 无 require 无 `=>` / CI 不 checkout core），core 侧同批清空两条为它开的过期容忍（`check_contract_pin.py` 的 `LOCAL_REPLACE_WHITELIST` 清空 + `check_repo_autonomy.py` 的 `KNOWN_GAPS` R1 行），夹具腿翻成「不在白名单 → 红」；18 腿自证、9 pin 实扫、12 仓全量三查全绿，取证与破坏验证见 QT-07 ⑥ 结案两条。**同日 裁-17**：B6-2「服务间凭证传递」的派工前提经源码实测订正为三条面（只有 `core→risk` 与 `analytics→上游` 是真 401；本仓 `authz` 不管读面；compose 里 `data-service` 未接 `JWT_SECRET`；`tellurion-data` 那四条路径没进策略表 ⇒ 403，token 不解决）—— 0 代码，落 QT-50 未落第 5 条。2026-11-12 `CORE-FACTOR-CACHE-WRITER` 按 ADR-037 D2 实现收口（run 级按需求值 + 门禁只读 run 级物化；`public.factor_cache` 标为退役遗留表，删表与读面清理另立 `CORE-FACTOR-CACHE-RETIRE`）；`CORE-REDIS-EXIT` / `CORE-BACKTEST-RANGE-COVERAGE` / `CORE-BACKTEST-OPS-TEXT` / `CORE-FACTOR-CACHE-RETIRE` 同日收口并归档；2026-11-12 `CORE-MIGRATION-REPLAY-GUARD` 收口（迁移改为「每条一事务 + 账本 `core_schema_migrations`（版本 + 指纹 + 应用时间）」：已应用同指纹跳过，指纹不符或账本领先于代码拒绝启动，存量库首次接管重放幂等 DDL 并记账；集成用例覆盖半途失败、重启续跑、接管与拒绝启动）；2026-11-12 `CORE-CI-ARCHIVE-LINKS-FIX` 收口（归档层 60 条相对链接补层级、2 处失效配置引用加「已删除」注记；`check_doc_links.py --include-archive` 由 60+2 条红转绿，两条破坏验证真做真还原）；2026-11-12 `CORE-STRATEGY-SCREEN-FALLBACK-DEAD-UPSTREAM` 按裁决 B1 收口（`value_screen` 多票删死腿、改为可诊断 `contracts.ErrDataGap` ⇒ 逐日 `data_gap_skipped`、终态 `invalid`；`multi_factor` 只删死腿、控制流不变；`retiredUpstreamAllowlist` 归零 + 4 条正向对照；真验红：改前同一引擎用例 `completed`、改后 `invalid`）。2026-11-12 `CORE-STRATEGY-MULTIFACTOR-PSEUDO-VALUE` 收口（本地价量路径的 `valueScore` 实为价格区间，裁决选 ②：价值维度缺席、quality/momentum 重归一化，Factors/Metadata 标记可追溯）；同批 `CORE-STRATEGY-SCREEN-LOCAL-VALUE` 立单（C 方案，值维度读路径 `pkg/storage/daily_basic.go` 已就位：PIT `trade_date <=` 取最近一条、`pe_ttm`→`pe` 回退且来源可见、市值万元→元单点换算、NULL = 该票当日不参与筛选；映射单测 + 真库反例绿）；2026-11-12 `CORE-STRATEGY-SCREEN-LOCAL-VALUE` 收口并归档（C 方案落地：run 内 daily_basic 快照 + fundamentals ROE，`pe<=`/`pb<=`/`roe>=` 单多票统一生效，零覆盖与 <50% 低覆盖按 B1 报 `ErrDataGap`，provenance 暴露 `pe_source`/覆盖数；commit `1317934`、`de85f71`）；2026-11-12 `CORE-CI-IMAGE-GATE` 收口并归档（CI 新增 `image-build` job 真跑 `docker build -f Dockerfile.service .`，检查器补 compose build 面与 `go build ./cmd/xxx` 目标目录两项静态检查；破坏验证改坏构建目标 ⇒ 检查器 exit 1 + 真构建 exit 1，还原后 SHA256 一致；commit `dd5a98f`）；2026-11-12 `CORE-K8S-EXIT` 收口并归档（7 份清单移入 `deploy/k8s/archive/`，检查器加「活跃层必须零清单」守卫、退役态明说跳过范围；12 仓 grep `kubectl apply`/kustomize/helm 零命中；破坏验证放回一份清单 ⇒ exit 1、删除后 exit 0；commit `480aab9`）；2026-11-12 `CORE-REFS-TRAILER-RULE` 收口并归档（`Refs:`/`Reviewed:` 改可选、任务号可追溯仍为硬约束、不回填历史；取证按实测订正为「少数带 trailer」并写入 AGENTS；commit `cca11ec`）；2026-11-12 `CORE-RESULT-RETENTION` 收口并归档（明细 90 天 / run 级汇总与 provenance 365 天 / 被引用的结果永不删；时间基准 `finished_at`、清理留 `detail_purged_at`、引用判定按真实表且拿不准即保留、运行中跳过、逐 run 事务 + 幂等 + dry-run；commit `f50affe`、`1d07a39`、`5af46cb`）。
---

# TASKS — tellurion 未完成项

> **归档说明（2026-11-11）**：本文件已瘦身为「只装未做的事」。2026-11-10 及以前已收口（✅）的任务原文、施工记录、验收输出与破坏验证，已逐字移入 [`docs/archive/tasks-archive-2026-11.md`](archive/tasks-archive-2026-11.md)；该文件顶部有「检索索引」表（ID / 标题 / 收口日期 / commit / 证据链接）。检索：`grep -n "<任务ID或commit>" docs/archive/tasks-archive-2026-11.md`。
>
> 状态：`⬜ 待做` / `🔶 进行中` / `⛔ 阻塞（有未解除前置）` / `⏸ 挂起（外部依赖或已裁决暂不实施；**不计入开放项**，重开须另立单并满足该条「重开条件」）` / `✅ 完成（附日期，随即移入归档）`

## 一、未做 / 挂起 / 外部依赖

> 本节 = 本轮（2026-11-11）台账重整时登记的遗留边界。此前 74 张已收口单的正文与施工记录全部在归档文件里，本文件不再镜像。

### CORE-COMPOSE-UNIFY · 本地编排收敛为单一 compose（撤销 QT-79 代裁 `D-DEPLOY-1 = A`）[P0] ✅（2026-11-11）

> 本条是**裁决记录**，不是遗留项：原代裁写在已归档的 QT-79 正文里，归档不可改写，
> 推翻理由必须留在活跃层可检索，故按六字段保留在此，下次台账归档时随正文移出。

- **① 目标**：全工作区本地编排收敛为仓根**单一** `docker-compose.yml`，形态差异用 profile 与命令行 env 表达，消除三份 compose 各自演进导致的口径漂移。
- **② 上下文（改判）**：2026-11-11 主任指令——**本地编排必须单一文件，避免三份各说各话**。据此推翻 2026-11-07 的代裁 `D-DEPLOY-1 = A`（「core compose 与 `docker-compose.services.yml` 各自是单一 owner 的完整栈、互不合并」）。原代裁的顾虑是 **8081 双发布**：聚合栈的 `data` 与 core 的 `data-service` 会同时发布同一端口，且项目名被聚合栈的 `name:` 覆盖。新方案**只保留 `data-service` 一份**，聚合栈的 `data` 服务删除 ⇒ 双发布与项目名覆盖两个前提一并消失。数据库同时改走宿主机原生 PostgreSQL 16.15（compose 不放 postgres 服务）：本机那份 16.15 是唯一数据源，再起容器 PG 会出现两份数据并抢 5432 —— 旧聚合栈「根本起不来」的病根正是这个。
- **③ 要求**：① 默认（无 profile）= `data-service` / `broker` / `analysis-service` / `ai-service`，开箱即起；`full` = `risk` / `strategy` / `analytics` / `orchestration`；`orchestration-worker` / `tools` / `nt` 各自保留；② `analysis-service` 用 `${BROKER_URL:-http://broker:8091}` + `BROKER_CHANNEL=mock` + `BROKER_ALLOW_MOCK=true` 装配，并 `depends_on: broker: service_healthy`；③ `deploy/wait-for-deps.sh` 从 `BROKER_URL` 推导 host:port，首次启动不靠重启碰运气；④ e2e 高额度改命令行 env 覆盖（`docker-compose.e2e.yml` 删除）；⑤ 文件头写死「默认栈是 mock 通道，订单永不触达真实柜台；改 `BROKER_CHANNEL=live` 且对端 `/version` 自报 live，否则装配期拒起」。
- **④ 约束与非目标**：跨仓 7 份 `<repo>/deploy/compose.yaml` **不动**（各仓自证 Dockerfile / health 的资产，不属于本地统一栈）；不改 `pkg/brokerhttp` 的 fail-closed；不引入服务发现；k8s 形态不在本轮范围。
- **⑤ 验收**：`docker compose config` 与 `docker compose --profile full config` 均 exit 0；`--services` 展开默认 4 项 / `full` 8 项；`python tools/check_deploy_consistency.py`、`python tools/check_doc_links.py`、`go build/vet/test ./... -count=1` 全绿。容器腿（冷启动 `up --wait`）并入 `CORE-BROKER-STARTUP-PRECONDITION` 的验收，不在本条重复计。
- **⑥ 边界**：本仓编排与文档；broker 镜像、`/version` 的 `channel` 与探针归 `tellurion-broker`；七个服务仓各自 `deploy/` 下的 compose 文件归各仓自持。
- **施工记录（2026-11-11 真冷启动）· 合并暴露的三个启动期缺陷**：单文件不是把三份文件拼起来就完事，抹平差异时逐一暴露了下面三条，**都属于同一类坑**（某份文件里存在、合并时被另一份覆盖或漏抄）：
  1. **`data-service` 漏 `AUTH_INSECURE_EXPOSURE`**：其余七个服务都有，只有它没有；容器绑 `0.0.0.0` 且无 `JWT_SECRET` 时启动闸 fail-closed，`data-service` 第一个退出（`up` 直接 `dependency failed`）。修法 = 补 `${AUTH_INSECURE_EXPOSURE:-loopback-published}`；加之前核过八个服务的 `ports:` **全部**已是 `127.0.0.1:` 前缀，声明与事实一致，不是用 env 让检查闭嘴。
  2. **ai ↔ analysis 双向 Boot 期解析环**：`ai-service` `depends_on analysis-service: service_healthy`，而 `analysis` 在 Boot 期硬校验 `ai_service.url` 的主机名；ai 尚未创建 → Docker DNS 不注册 → analysis 解析超时拒起 → ai 永远等不到 analysis healthy。修法 = 把 `ai_service.url` 降级为**首次反代调用时解析**（可选下游不应成为 core 的启动门；失败返回点名 `AI_SERVICE_URL` 与主机名的 503，不静默跳过），并恢复 ai 单向等 analysis。数据面 / risk / strategy 资产的 Boot 期硬校验保留，有单测钉住（`cmd/analysis/endpoints_test.go`、`handlers_ai_proxy_test.go`）。
  3. **`RISK_SERVICE_RESOLVE_URL` 未注入且 `resolve_enabled` 出厂 true**：容器里的 `localhost:8089` 是 analysis 自己，`risk-engine Init` 拒起。修法 = 默认 `RISK_SERVICE_RESOLVE_ENABLED=false`（内置限额兜底，启动日志 `risk_policy_source=builtin_default`）+ 注入 `RISK_SERVICE_RESOLVE_URL=http://risk:8089`；full 里显式 `RISK_SERVICE_RESOLVE_ENABLED=true` 时 `wait-for-deps.sh` 按该 URL 等 risk 就绪。**fail-closed 语义不变**：开着就必须取到，取不到即拒起。
  - 另有一条**非配置缺陷**：本机 `tellurion-broker:dev` 镜像旧于源码（`/version` 没有 `channel`），core 按契约拒起；重建镜像后 `broker peer channel verified`。冷启动前必须先 `build`。
  - 验收：`--profile full` 连续三次 `down → up -d --wait` 全绿（八服务 healthy）；默认栈日志 `risk_policy_source=builtin_default`；`RISK_SERVICE_RESOLVE_ENABLED=true` 时日志 `risk_policy_source=risk_snapshot`（`risk_rules_hash=84a95d7d…`）；八服务探针全 200（strategy 是 `/healthz` + `/readyz`，`/health` 不存在）。

### CORE-STRATEGY-MULTIFACTOR-PSEUDO-VALUE · `multi_factor` 本地价量路径的「value」分量实为价格区间 [P1] ✅（2026-11-12）

> **裁决记录（2026-11-12，commit `d73a6fb`）**：复核确认 `generateSignalsFromPriceData`
> 存在一个名为 `valueScore`、实际由回看窗口内价格分位（`1-(end-min)/(max-min)`）
> 算出的分量，并按 `value_weight`（默认 0.4）计入复合分 —— 与 `value_screen`
> 的「标着价值、算的是价格」同类，只是藏在复合分里、Signal 不暴露分量，更难发现。
>
> 两个出口中选 **②（价值维度缺席时不参与加权、权重重归一化）**，理由：
> ① 该分量本质是价格位置指标，改名为 `price_range_score` 并保留 0.4 权重，
> 等于把「价格位置」合法化成与价值无关的主要因子，噪音照旧；② `RequiredFactors`
> 已声明 value 为必需因子，缓存为空时更应显式声明价值维度缺席，而不是用价格冒名。
> 落地：删 `valueScore` 字段与计算（不留别名）；`quality/momentum` 经
> `renormalizeWithoutValue` 重新归一化；本地路径的 buy 信号写入
> `Factors`（quality/momentum，无 value）与 `Metadata`（`value_dimension=absent`、
> `weights_renormalized=true`、requested/effective weights），使「这次排序用没用
> 价值条件」事后可判断。测试：`TestMultiFactorPricePathExcludesValueDimension`、
> `TestMultiFactorPricePathRenormalizesComposite`。

- **① 目标**：消除复合分中「名叫 value、实为价格」的伪分量，并让价值缺席可观测。
- **② 上下文**：`pkg/strategy/plugins/multi_factor.go` 的本地价量腿（无基本面输入时
  的唯一路径）原用价格分位充当 value；缓存腿用的是真实 z-score，两者口径不一致。
- **③ 要求**：本地路径只保留 quality/momentum；权重重新归一化；provenance 写明缺席与重分配。
- **④ 约束与非目标**：不恢复任何已删路由；不改变缓存腿的 z-score 口径；不新增别名。
- **⑤ 验收**：两条新用例绿；`gofmt -l` 空、`go vet`/`go test ./pkg/strategy/plugins` 绿。
- **⑥ 边界**：`multi_factor` 策略本体；`value_screen` 的本地实现另立
  `CORE-STRATEGY-SCREEN-LOCAL-VALUE`。
- **重开条件**：若将来引入真实基本面输入到本地路径，应重新评估是否恢复 value 分量（需新口径定义，不得复用价格分位）。

### CORE-FACTOR-DERIVED-GUARD · 因子派生表零写者时，产物组装必须说清「为什么缺」 [P1] ✅

> **施工记录（2026-11-11）**：`internal/analysisartifact` 新增 `FactorReturnsUnavailableError`（`reason` = `no_writer` / `no_rows`，携带因子名与窗口）与可选探针 `FactorReturnProbe`；`pkg/storage.PostgresStore.HasFactorReturns` 用一条 `EXISTS` 判定表是否为空。全部因子缺失 ⇒ 返回该错误（原因可判定、文案点名缺哪些因子并指向 ADR-038）；部分缺失 ⇒ 产物照常产出，但 `FactorExposures.MissingFactors` 点名缺失因子（`omitempty`，加法字段，analytics 侧用非严格解码，不受影响）。探针失败按 `no_rows` 降级，不伪装成 `no_writer`。测试：5 条（无探针/no_writer/有行/no_rows/探针报错/部分缺失）。破坏验证：临时改回 `missing("factor_exposures")` ⇒ 3 条用例红，还原复绿。`ic_analysis` 零读者的事实保留在代码注释，删除另立单。

- **① 目标**：`analysisartifact` 在 `factor_returns` 无写者、`GetFactorReturns` 恒空时，产出**可判定的缺失原因**（点名「本仓无因子收益写者，见 ADR-038」），而不是笼统的 `missing factor_exposures`；逐因子缺失不再静默跳过。
- **② 上下文**：`internal/analysisartifact/builder.go:205-212` 逐因子取收益，`latestTopMinusBot` 为空就 `continue`；全部为空时在 `:219-221` 报 `missing("factor_exposures")`（`MissingError` 文案只写字段名，`builder.go:51-53`）。写方 `pkg/storage/cache.go:121` 的 `SaveFactorReturnBatch` **零调用**，`ic_analysis` 读写皆零调用（`cache.go:194,239`）。跨仓裁决见 ADR-038（两表归 core、沿用按需求值、不新增写者）。今天是「必然缺」而不是「偶尔缺」，错误文案却把两者混为一谈。
- **③ 要求**（实施后修正）：① 全缺失时区分两种整表原因——`no_writer`（表一行都没有）/ `no_rows`（表有行但本窗口没有），用 `FactorReturnsUnavailableError` 承载 `reason` + `factors[]` + 指向 ADR-038 的文案；② 部分缺失不升级为错误，改为在 `FactorExposures.MissingFactors` 里**逐因子点名**，保证「不再静默」的同时不丢可用因子；③ `ic_analysis` 零读者的事实写进代码注释，删除另立单（本单不动 DDL）；④ 补测试：无写者 ⇒ 错误含 `no_writer` 与因子名；有行 ⇒ 正常组装；破坏验证：把错误改回 `missing("factor_exposures")` ⇒ 新用例红。
- **④ 约束与非目标**：**不补写者**、不改 `factor_returns`/`ic_analysis` 表结构、不改因子收益算法；不改 analytics 侧。
- **⑤ 验收**：`gofmt -l` 空、`go build ./...`、`go vet ./...`、`go test ./... -count=1` 全绿；新增用例覆盖两种整表原因 + 部分缺失 + 探针失败；破坏验证真做真还原。
- **⑥ 边界**：`internal/analysisartifact` 与 `pkg/storage` 的只读注释/表清单；DDL 删除不在本单。
- **重开条件**：本条无外部前置。若将来按需实时计算落地（ADR-037 D2），本单的 `no_writer` 分支应随之删除并在同单更新用例。

### CORE-FACTOR-DEAD-PROXY · `GET /api/factors/:factor_name` 转发到 data 已撤路由 [P2] ✅

> **施工记录（2026-11-11）**：
> - **代码**：`cmd/analysis/handlers_proxy.go` 删除反代实现，改为 `httpserver.FailCode(c, 410, "factor_http_surface_retired", ...)`；路由仍注册（保持 openapi↔路由一一对应），文案指向 ADR-037 §5 / ADR-041。
> - **测试**：原 4 条转发用例替换为 `TestFactorCitationProxy_Retired_Returns410WithoutUpstream` / `_IgnoresSelectors`；假上游计数器断言 **0 次请求**，响应含机器码。
> - **文档**：`docs/openapi.yaml` 该 path 改为 410 + 退役说明（operationId `getFactorCacheRowRetired`）；`docs/SPEC.md` 从现役块移到 `Removed`，并在 REV-13 段落补注退役。
> - **门禁（真实退出码）**：`gofmt -l` 空；`go build ./...` / `go vet ./...` / `go test ./... -count=1` 全绿；`python tools/check_doc_links.py` exit 0。
> - **破坏验证（真做真还原）**：临时把 handler 恢复成 `proxyRequest(..., dataURL, nil)` ⇒ 两条退役用例 **FAIL**（状态 200 而非 410、上游计数 1）；还原后 PASS。
> - **消费者检查**：`web/src` 与 `scripts/` grep `api/factors/` 仅命中 SPEC/测试注释，无 UI/脚本调用方。

- **① 目标**：让这条路由**明确退役**（返回可判定的 `410/404` + 机器码），而不是把请求转发到一个已删除的上游路由、再把上游的 404 原样透传。
- **② 上下文**：`cmd/analysis/handlers_proxy.go:158-180` 把 `GET /api/factors/:factor_name` 反代到 data `/factors/:name`；data 已在裁-14 撤掉该路由（`tellurion-data/internal/httpapi/server.go:183-186` 原文「不要重新引入」）。ADR-037 §5 已把这条死链登记为已知后果；ADR-041 又把「因子求值面」判为未提供。当前对外语义是「404 但来源不明」——调用方无法区分「因子不存在」与「能力已退役」。
- **③ 要求**：① 删除反代实现，改为显式退役响应（机器码如 `factor_http_surface_retired`，文案指向 ADR-037）；② `docs/openapi.yaml` 与 `docs/SPEC.md` 同批标注退役；③ 补用例：请求该路径 ⇒ 返回退役码，且**不发出上游请求**（用假 data 地址 + 计数证明）；④ 检查是否有 UI/脚本消费者——有则先登记再退役。
- **④ 约束与非目标**：不把该路由改指新计算面（ADR-041 已裁「不新造端点」）；不动 `/api/factor/registrations`。
- **⑤ 验收**：`gofmt -l` 空、`go build ./...`、`go vet ./...`、`go test ./... -count=1` 全绿；退役码用例 + 零上游请求断言；破坏验证：恢复反代 ⇒ 用例红。
- **⑥ 边界**：core 路由与 openapi/SPEC 的三处同步；data 仓不改。
- **重开条件**：若将来 core 真的提供按需求值的因子读面（需先有 ADR），本路由可重新设计为真实读面，而不是恢复反代。

### CORE-SPEC-FACTOR-ENDPOINTS-STALE · `docs/SPEC.md` 因子端点节列了从未实现的 5 条路由 [P2] ✅

> **施工记录（2026-11-11）**：`docs/SPEC.md` 的 Factor Analysis 节只保留 `POST /api/factor/registrations` 为现役；5 条陈旧路由逐条标为「历史契约 / 当前未实现」并指向 ADR-041；同节顺带订正 Agent 粗粒度动作表（`query_factor_evaluation` 2026-11-08 已撤销、现役 7 条），以及 REV-13 段落对退役反代的注释。未改 `docs/openapi.yaml`（它本来就没有这 5 条）。`python tools/check_doc_links.py` exit 0；`grep -n 'compute-returns\|compute-ic\|factor/returns\|factor/ic\|factor/list' docs/SPEC.md` 仅剩历史标注。

- **① 目标**：把 `SPEC.md` 的因子分析端点节从「现行契约」改为「历史契约 / 当前未实现」，消除「文档说有、代码没有」的传播源。
- **② 上下文**：`docs/SPEC.md:3197-3210` 列 `POST /api/factor/compute-returns`、`/compute-ic`、`GET /api/factor/returns/:factor`、`/ic/:factor`、`/api/factor/list`；实测 `cmd/analysis` 只注册 `POST /api/factor/registrations`（`handlers_factor_registration.go:65`）与 `GET /api/factors/:factor_name`（`handlers_proxy.go:167`），`docs/openapi.yaml` 同样只有两条。agent 的端点契约测试正是因为这段陈旧文档才把 `compute-returns` 当成「core 已有」。
- **③ 要求**：① 该节逐条标注「历史契约 / 当前未实现」并指向 ADR-041；② 若 `SPEC.md` 有 TOC 或端点总量数字，同批订正；③ 不改 openapi（它本来就没这些路由）。
- **④ 约束与非目标**：只改文档事实，不实现端点、不改路由表。
- **⑤ 验收**：`python tools/check_doc_links.py` exit 0；`docs/SPEC.md` grep 不到把 5 条端点写成现役的表述。
- **⑥ 边界**：`docs/SPEC.md` 因子分析节；`docs/openapi.yaml` 不动。
- **重开条件**：core 真实现任一端点时必须先有 ADR 与契约，再回填为现行。

### CORE-COMPOSE-FACTOR-COMMENT · compose 注释称 `/api/factor/*` 已注册 [P3] ✅

> **施工记录（2026-11-11）**：`docker-compose.yml:324-328` 的注释改为「三个因子工具在当前栈上不可用（core 无 compute/evaluate，ADR-041）；URL 保留仅用于将来接线」。agent 侧同类注释（`cmd/ai/main.go:100-103`）由 `AGENT-FACTOR-TOOLS-UNAVAILABLE` 同批改。仅注释，无 env/依赖/端口变化。

- **① 目标**：订正 `docker-compose.yml` 里的错误事实注释，避免下一位读者据此认为 core 已有因子求值面。
- **② 上下文**：compose 与 `tellurion-agent:cmd/ai/main.go:100-101` 的注释称「`/api/factor/*` 只在 core 注册」；实测 core 只有 registrations + 死链反代（ADR-041 §2）。该注释是 agent 侧把 `compute-returns` 当成现役的直接诱因之一。
- **③ 要求**：注释改为「core 只注册 `/api/factor/registrations`；`GET /api/factors/:name` 已退役为 410（C8 / ADR-037 §5）；求值面未提供（ADR-041）」。
- **④ 约束与非目标**：只改注释，不改任何 env / 依赖 / 端口。
- **⑤ 验收**：`docker compose config` 仍可解析（无行为变化）；grep 不到「`/api/factor/*` 只在 core 注册」。
- **⑥ 边界**：`docker-compose.yml` 注释；agent 仓的同类注释由 `AGENT-FACTOR-TOOLS-UNAVAILABLE` 同批改。
- **重开条件**：无（纯文档事实订正）。

### CORE-ANALYSISARTIFACT-RETENTION · `internal/analysisartifact` 生产分析产物，是否属本仓 [P2] ⏸

- **① 目标**：记录「core 在生产分析产物」这一上下文渗出的**保留决定与退出条件**，防止它继续无声扩张。
- **② 上下文**：`internal/analysisartifact/builder.go` 从 core 的 run 终态 + `factor_returns` + 因子 z-score 组装 `PortfolioSnapshot`/`FactorExposures`，再发布给 analytics（`publisher.go:198-236`）。DDD 审查 C6 判它是「分析上下文的渗出」；但产物输入全在 core 进程内、analytics 又不回写，迁走会制造跨仓取数。
- **③ 要求**：**本轮不改代码**。保留在 core，条件：① 只组装 core 已持有的 run 终态，不做新计算；② 不新增对外读面（analytics 仍通过现有 artifact 指针消费）；③ 产物形状变更必须同步 analytics 的消费侧（`internal/analysis` 的 reader）。
- **④ 约束与非目标**：不迁 analytics、不新增 `analysis.*` 写者；不据此扩产新产物类型。
- **⑤ 验收**：本条无施工；以 ADR-039 T1 是否触发作为退出信号。
- **⑥ 边界**：产物组装与发布；指标公式归 core `pkg/backtest/metrics`（ADR-039）。
- **重开条件**：ADR-039 T1 触发（core 报告出口归零、analytics 成为唯一报告产出面）⇒ 届时再把组装迁往 analytics 并新立 ADR。

### CORE-MARKET-INDEX-DAILY-EXCEPTION · `market.index_daily` 的跨 schema 建表权 [P3] 不改（已裁决）

- **① 目标**：登记该例外**不改**的理由，避免下轮审查重复提出。
- **② 上下文**：`market.index_daily` 的 `CREATE TABLE` 在 core（`pkg/storage/postgres.go`），owner = core 是第三轮裁决 ⑰ 的结果；`internal/repoguard/storage_market_tables_retired_test.go:18,79` 把它钉成机器判据（其余 `market.*` 在 core 零建表，违者红）。
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

### CORE-BROKER-STARTUP-PRECONDITION · broker 不可达时 core 拒起（部署前置）[P1] 🔶

- **① 目标**：把「core 起得来」与「broker 先就绪」的先后关系写成部署约束，避免编排侧误判为 core 缺陷。
- **② 上下文（2026-11-11 三次前提改写）**：`cmd/analysis` 的 exec-engine 模块在 `Boot` 期无条件构造 broker 通道（`cmd/analysis/kernel_wiring.go:130` → `pkg/bootstrap.BuildExecutionTrader` → `pkg/brokerhttp.NewTraderFromEnv`），`BROKER_URL` 缺失即 `kernel assembly failed`、服务拒起。原前提「实测三种编排都没给 core 注入 `BROKER_URL`」**已不成立**：core `docker-compose.yml` 的**默认栈**已含 `broker`（`127.0.0.1:8091:8091`）并给 `analysis-service` 注入 `BROKER_URL=http://broker:8091` / `BROKER_CHANNEL=mock` / `BROKER_ALLOW_MOCK=true` / `BROKER_SERVICE_TOKEN` / `BROKER_ACCOUNT_ID`，且 `depends_on: broker: service_healthy`（`CORE-COMPOSE-UNIFY`）；`docker-compose.services.yml` 已并入并删除。**剩余缺口**：① ~~容器腿冷启动未真跑~~ **2026-11-11 已补：`--profile full` 连续三次 `down → up -d --wait` 全绿**；② `deploy/k8s/` 8 份清单仍无 broker、也无 `BROKER_*` 注入；③ `deploy/README.md` 与 runbook 尚未写失败原文。
- **③ 要求**：①（前置，**已于 2026-11-11 完成**：单一 compose 默认栈真接上 broker + 启动依赖 + `wait-for-deps.sh` 等 `BROKER_URL`）；② `deploy/README.md` 与 runbook 写明失败时的原文（`kernel assembly failed` → `BROKER_URL is required` / `broker peer channel` 两类）；③ 保留 fail-closed，不改「先起后连」；④ 若产品上要支持「只做分析、不下单」的部署形态，那是**另一个决定**（把 exec-engine 改成可选模块），须单独立单，并说明不接交易时对账 / 风控覆盖口子由谁负责；⑤ k8s 形态接 broker 另立部署单（本单只管本仓编排资产）。
- **④ 约束与非目标**：不改 `pkg/brokerhttp` 的 fail-closed 行为；不引入跨服务的就绪协商。
- **⑤ 验收**：冷启动整套栈 `up --wait` 一次成功、core 重启计数为 0 —— **2026-11-11 实测三次全绿**，启动日志含 `broker peer channel verified`（`channel=mock`）与 `execution channel is MOCK`；破坏验证（拔掉 broker 或反序起，core 必失败且日志点名 `BROKER_URL` / `/version`）**尚未做**。
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
- **审查复核（2026-11-11，不另立单）**：全面审查 W2 独立复核「三处 `rm == nil` 回落恒不可达 + 缺省 `:8083` 无对端（今 risk 在 `:8089`）」属实，与本条前提一致；见 ODR-069。去重：不新建任务，以本条为唯一落点。

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

### CORE-AUDIT-FULLSTACK-2026-11 · 12 仓全面审查（产品→DDD→整洁度→最佳实践，W0~W7）[P1] 🔶

- **① 目标**：按主任批准的计划完成 12 仓全维度审查，产出逐项判定表（P-1…X-3，每项带证据坐标），发现全部走 ODR（自 068 起编）+ 各仓六字段立单；Critical/High 当场修复或当场立单。
- **② 上下文**：计划文件 = `C:\Users\ruoxi\.qoder-cn\plans\gentle-creek-tench.md`（主任已批，**不落仓**）；14 个工作流 / 风险分层 T0~T3 / 波次 W0~W7。去重基准 = 本文件 §一 开放项与 ⏸、§三 DDD C1~C13、ODR-043/065 闭项、第四轮目的对齐审计；工作件在 `C:\Users\ruoxi\.qoder-cn\plans\audit-w0\dedup.md`（仓外，非报告文件）。
- **③ 要求**：① 每波开工前按仓规立单，一 task 一 commit；② 真退出码 + 证据分级 E1~E4，环境降级须标注；③ 护栏破坏验证用绝对数值断言 + 「调大阈值」腿，改坏/写操作/起服务先拿主任授权；④ 派工前提先证伪；⑤ 每波做完停下核对，结论反转处放最前面。
- **④ 约束与非目标**：不写独立 Report .md；已裁例外不重报（只验钉住护栏）；⏸/⛔ 只核解冻条件；本单只做审查与立案，**代码修复另立单**，不在审查 commit 里混修复。
- **⑤ 验收**：逐项判定表齐（通过/不通过/已裁例外 + 证据坐标）；真跑项全 E1/E2；发现全部进 ODR/台账或归入已裁例外；主任逐波 checkpoint 签收 + W7 总验收。
- **⑥ 边界**：审查证据、ODR 立案、各仓立单；修复施工归各修复单。
- **施工记录（W0 基线，2026-11-11）**：12 仓工作树全干净（in-place = 干净 checkout），HEAD 锚定：core `27e0c15` / agent `8ace9b9` / analytics `cc440f0` / broker `fbaa9fe` / contracts `fb19870` / data `2fdf48b` / docs `975a403` / kit `7d7c3a8` / orchestration `ab2ee94` / risk `693f590` / strategy `03a5d91` / ui `90d0a2c`。**基线红名单 = 空**：10 Go 仓 gofmt/build/vet/test 全 EXIT=0；contracts 5 护栏 + conformance 全 0；kit 2 护栏全 0；core 4 Python 检查器全 0；docs 3 检查器 + 38 条自测全 0（guide_refs 首跑 GBK 假红，UTF-8 复跑绿）；ui lint/typecheck/vitest 全 0（**945 条 eslint warnings 不红**，T-1/K-7 候选发现留 W6 立案；vitest 38 文件全过）。agent 两条历史红（QT-17 ③ 端点字面量）已随数据面退役消解（grep 零命中）。DB 依赖腿无 DSN 按设计 skip，留 W3/W5/W6；-race 本机无 gcc，只认 CI。**W0-3 C2 核实**：research_tool vault 镜像回落有契约授权（legacy §3.3/§3.5 + agent db-schema.md:73 + hermes tools yaml:445），不构成违规；治理观察一条（C2 规范文本住归档层）留 W1 G-4。日志：`C:\Users\ruoxi\.qoder-cn\plans\audit-w0\<repo>.log`。

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
| C3 | `factor_returns`/`ic_analysis` 孤儿表、产物组装静默跳过 | 裁决归 core + 改代码（说明缺失原因） | 跨仓 ADR-038；`CORE-FACTOR-DERIVED-GUARD` | ✅ 2026-11-11 |
| C4 | core 与 analytics 指标双实现、无权威条款 | 新建 ADR + 漂移检测 | 跨仓 ADR-039；`tellurion-analytics:ANALYTICS-METRIC-DRIFT` | ✅ 2026-11-11（真实 run artifact 腿 ⏸，见 Analytics 归档重开条件） |
| C5 | risk 自持 `Snapshot`，与 `contracts/risk` 双定义 | 改代码：别名引用 contracts | 跨仓 ADR-040；`tellurion-risk:RISK-CONTRACTS-SNAPSHOT-MIGRATION` | ✅ 2026-11-11（真栈 hash 对拍腿 ⏸，见 Risk 归档重开条件） |
| C6 | core `internal/analysisartifact` 生产分析产物 | 保留 + 记退出条件（本轮不改代码） | `CORE-ANALYSISARTIFACT-RETENTION` | ⏸ 挂起 |
| C7 | core ↔ agent 运行期「双向调用」的文档口径 | 改文档：core→agent 是 HTTP 反代，无领域语义 | `tellurion-docs:DOC-15`（`DOC-CORE-AGENT-PROXY-NOTE`，`docs/REPO-MAP.md` 依赖图脚注；收口记录在该仓 `docs/archive/tasks-archive-2026-11.md`） | ✅ 2026-11-11 |
| C8 | `GET /api/factors/:name` 死链（反代 data 已撤路由） | 改代码：显式退役 | `CORE-FACTOR-DEAD-PROXY`；UI 消费方登记为 `tellurion-ui:UI-FACTOR-CITATION-RETIRED` | ✅ 2026-11-11 |
| C9 | compose/agent 注释称 `/api/factor/*` 已注册 | 改注释（事实订正） | `CORE-COMPOSE-FACTOR-COMMENT` + agent 同批 | ✅ 2026-11-11 |
| C10 | `analysis.benchmarks` 零写者（K7） | 不改（维持挂起，重开=出现写消费者） | `tellurion-analytics` K7 条目 | ⏸ 已挂起 |
| C11 | contracts 版本列 v0.9.0 与 `go.mod` 实测 v0.10.0 不一致 | 改文档（已改） | `tellurion-docs:DOC-16`（`DOC-REPO-MAP-VERSION-CORRECTION`，`docs/REPO-MAP.md` 版本列与推进顺序；收口记录在该仓 `docs/archive/tasks-archive-2026-11.md`） | ✅ 2026-11-11 |
| C12 | `market.index_daily` 由 core 建表（跨 schema 例外） | 不改（第三轮裁决 ⑰ + guard test 已固化） | `CORE-MARKET-INDEX-DAILY-EXCEPTION` | 不改（已裁） |
| C13 | `public.factor_cache` 无写者、表未收尾 | 裁决 + 按需求值实现；表删除另立单 | 跨仓 ADR-037 §6；`CORE-FACTOR-CACHE-WRITER` ✅ 2026-11-12（见归档 §十三）；`CORE-FACTOR-CACHE-RETIRE` ✅ 2026-11-12（删表，见归档 §十五） | ✅ 2026-11-12（实现 + 删表） |

**判定「不存在」的条目**：无。13 条全部有代码或文档证据，无一条因前提不成立而关单。

**清单外的顺手发现**：只登记、不实现；本轮未新增此类条目（如后续发现，追加到本表末尾并标注「清单外」）。

## 四、跨仓批次（X-，本仓为协调伞）

> 这些是**跨多仓的协调批次**，不是 core 单仓施工。本仓台账是总序伞（docs 仓 AGENTS §4 禁止施工单进 docs），故登记在此；**每仓的实际改动落该仓自己的 commit**，按单号串行。批次内各仓的子项在各仓台账以指针引用本批，不双写正文。

### X-DEP-BUMP-CONSUMERS · 7 消费仓 bump kit pin + govulncheck 门（W5-F1 连带）[P0] ⬜

- **① 目标**：kit 安全 bump（`KIT-DEP-SECURITY-BUMP`）发版后，7 个消费仓把 kit pin 升到安全版并各补一条 `govulncheck` CI 门，让 GO-2026-5004（pgx SQLi）等漏洞在消费侧真闭合。
- **② 上下文**：审查发现 W5-F1（ODR-072）+ 裁决 D1=b（pgx 一步收敛 v5.11.0）。GO-2026-5004 影响 7 仓（core/agent/data/strategy/risk/orch/broker），fixed v5.9.2；x/net×4（fixed v0.60.0）8 仓、x/text×2 9 仓。kit 是依赖底座，bump 后消费仓须跟升 pin 才真生效。前置 = `KIT-DEP-SECURITY-BUMP` + `BROKER-DEP-BUMP` + `UI-NPM-AUDIT-FIX`。
- **③ 要求**：① kit 发安全版后，7 消费仓 `go get tellurion-kit@vX` + `go mod tidy`（只升 tag，禁 replace）；② 各仓 CI 增 `govulncheck ./...` 门；③ 各仓 AGENTS §4 依赖表同批更新（与 `X-AGENTS-DEPENDENCY-TABLE` 联动）；④ pgx 收敛到 v5.11.0 一步到位（D1=b），登记三档并存的代价。
- **④ 约束与非目标**：不手改版本号、不加 replace；不在本批顺手升无关依赖；analytics 已 v5.11.0 安全，不重复 bump。
- **⑤ 验收**：7 仓 `govulncheck` 真跑 EXIT=0（E2）；破坏验证 = 临时 pin 回旧版 ⇒ govulncheck 红。各仓 `go build/vet/test` 绿。
- **⑥ 边界**：协调伞在 core；各仓 pin/CI/AGENTS 改动落各仓 commit。前置 kit 发版归 `KIT-DEP-SECURITY-BUMP`。
- **core 腿（2026-11-12 已完成，见 [归档](archive/tasks-archive-2026-11.md#X-DEP-BUMP-CONSUMERS)）**：`go.mod` kit `v0.5.0` / pgx `v5.11.0` / `go 1.26.11`，CI 增 `govulncheck` 门并三处 `setup-go` 对齐；破坏验证（pgx 回 v5.7.3 ⇒ GO-2026-5004 红）真做真还原。**其余 6 仓仍开放**。
- **owner**：待定
- **重开条件**：`KIT-DEP-SECURITY-BUMP` 发版（tag 推远端 + proxy 可解析）。

### X-AGENTS-DEPENDENCY-TABLE · 6 仓 AGENTS §4 依赖表与 go.mod 实测对齐（W1-F4）[P2] ⬜

- **① 目标**：把 6 仓 AGENTS §4 的契约/工具版本列订正到 `go.mod` 实测值，消除「文档说 v0.9.0、实际 v0.10.0」的传播源。
- **② 上下文**：审查发现 W1-F4（ODR-068）。六仓 AGENTS §4 写 contracts `v0.9.0` vs `go.mod` 实测 `v0.10.0`；core AGENTS:20 写 kit `v0.1.4` vs `go.mod:16` `v0.3.0`；broker AGENTS kit 列待核；core AGENTS:152/:862 的 `:8081` 归属句需订正（data-service 由 tellurion-data 提供）。risk `go.mod:35` indirect 问题归 `RISK-GOMOD-DIRECT`。
- **③ 要求**：① 6 仓 AGENTS §4 contracts 列改 `v0.10.0`（以各仓 go.mod 实测为准，逐仓核）；② core kit 列改 `v0.3.0`；③ core :8081 归属句订正；④ 与 `X-DEP-BUMP-CONSUMERS` 撞 AGENTS §4 时按单号串行。
- **④ 约束与非目标**：只改文档版本列，不改 go.mod（go.mod 是真相）；不逐仓抄同一版本号（各仓实测可能不同）。
- **⑤ 验收**：各仓 AGENTS §4 与 go.mod 逐行一致；`check_doc_links.py`（docs 仓）绿。
- **⑥ 边界**：协调伞在 core；各仓 AGENTS 改动落各仓 commit。
- **core 腿（2026-11-12 已完成，见 [归档](archive/tasks-archive-2026-11.md#X-AGENTS-DEPENDENCY-TABLE)）**：contracts 列 `v0.10.0`、kit 列 `v0.5.0`（随 bump 取实测值）、`:8081` 归属句三处订正。**其余 5 仓仍开放**。
- **owner**：待定

### X-CI-RACE-STATICCHECK · 6 服务仓 + agent + contracts CI 补 -race/staticcheck（W6 连带）[P2] ⬜

- **① 目标**：让 6 服务仓 + agent + contracts 的 CI 真跑 `-race` 与 `staticcheck`，对齐 core/kit 已有的竞态门，消除「本机无 gcc 跑不动、CI 也没装」的覆盖空洞。
- **② 上下文**：审查发现 W6（ODR-073）。core/kit CI 已有 `-race`（**不要动**）；6 服务仓 + agent + contracts 的 CI 缺 `-race`/`staticcheck`；ui 缺 build 步骤门。本机无 gcc，`-race` 结论一律以 CI 为准（E2）。
- **③ 要求**：① 6 服务仓 + agent + contracts CI 增 `-race`（CGO_ENABLED=1）+ `staticcheck` 步骤；② ui CI 增 `npm run build` 门；③ core/kit 已有 `-race` 不重复加；④ 各仓 AGENTS §2 测试命令同批注明 -race 覆盖面。
- **④ 约束与非目标**：不改 core/kit 的 CI；不缩小 -race 覆盖面（全仓所有包）；不拿本地绿当 -race 证据。
- **⑤ 验收**：各仓 CI `-race`/`staticcheck` 步骤真跑（E2）；破坏验证 = 注入一处竞态/静态错误 ⇒ 该步红。
- **⑥ 边界**：协调伞在 core；各仓 CI 改动落各仓 commit。
- **owner**：待定

### X-PROBE-NAMING · data `/health`→`/healthz` 统一探针命名（D12=a）[P3] ⬜

- **① 目标**：把 data 的 `/health` 统一为 `/healthz`（对齐其余服务仓），并同批改所有消费者，消除探针命名分叉。
- **② 上下文**：审查发现 W6（ODR-073）+ 裁决 D12=a。data 用 `/health`，其余服务仓用 `/healthz`；compose/k8s/core 反代多处消费 data `/health`。改名是 breaking，须同批改消费者。
- **③ 要求**：① data 增 `/healthz`（保留 `/health` 过渡或直删，二选一写死）；② grep 全工作区 data `/health` 消费者同批改；③ data AGENTS/api-schema/runbook 同批；④ 与 `CORE-K8S-EXIT`（data-deployment.yaml 探针）联动。
- **④ 约束与非目标**：不改其余服务仓探针；不静默删 `/health` 而漏改消费者（先 grep 全量）。
- **⑤ 验收**：data `/healthz` 真栈 200（E1）；grep 无悬空 `/health` 消费；破坏验证 = 漏改一处消费者 ⇒ 该处探针红。
- **⑥ 边界**：协调伞在 core；data 改名落 data commit，消费者落各仓 commit。
- **owner**：待定

### X-LOW-CLEANUP · 低优先整洁度残留打包（W6/W2 清单外顺手发现）[P3] ⬜

- **① 目标**：把审查中登记的 5 处低优先整洁度残留一次清掉，各落各仓 commit，不混进功能单。
- **② 上下文**：审查发现 W6/W2（ODR-073/069）清单外顺手发现：① strategy Dockerfile 退役名残留；② contracts `fees` 死注释；③ agent `/api/health` 豁免注册口径；④ 分页 offset 登记缺失；⑤ ui openapi「不复制」守卫。各条证据坐标见对应 ODR 发现摘要。
- **③ 要求**：① strategy 删 Dockerfile 退役 cmd 名；② contracts 删 fees 死注释（不改字段）；③ agent 核 `/api/health` 豁免注册并订正；④ 补分页 offset 登记；⑤ ui 补「openapi 不复制」守卫（grep 护栏）。各条独立 commit。
- **④ 约束与非目标**：不顺手扩到功能改动；contracts 不改冻结字段；ui 不复制 openapi。
- **⑤ 验收**：各仓 build/test 绿；⑤ 的守卫破坏验证 = 复制一份 openapi ⇒ 护栏红。
- **⑥ 边界**：协调伞在 core；5 处各落各仓 commit。
- **owner**：待定

## 维护本文件

- 新增 task 必须按 [AGENTS.md §8.5](../AGENTS.md) 写全六字段；**六字段里必须显式引用冻结契约的路径与节号**（本代 wave-0 的冻结文本在 `docs/design/qt-decoupling/p0-round2-review.md` §8、`p0-remediation-design.md`、`adr-029-amendment-host-api-v2.md`）。
- 完成即标 ✅（附日期）并移入 `archive/`；本文件只留未完成项。换代时整篇加时间戳归档（`archive/TASKS-YYYY-MM-DD-archived.md`），旧代正文不改写。
- **派工前必须先证伪工单前提**（读码核对 → 不符就停下回报 + 给拆法，不硬做、不扩范围）。
- **执行顺序（偏序，非全序；refactor-plan-review M4）**：硬链 **S-1(QT-60 ✅) → QT-61 → S0（QT-06 → QT-18 → QT-08）→ W0 → S1(QT-05)**；S2 段（QT-17 → QT-07 → QT-19 → QT-09）须在 W0 + QT-05 全绿后；**W1 不依赖 S2**——W0 后即可开工，且 **仅 QT-17 可与 W1 并行**（QT-07/09/19 不与 W1 抢档）；W2 在 W1 后、W3 在 W2 后。同波次内撞文件的按 QT 序号串行，一单一 commit。**W0 完成前不开 W1 的代码单**——否则 artifact 与基准都要重做。
- **冻结 schema 变更评审（硬规则；refactor-plan-review H1）**：凡触碰 `ResultSpec` / `RunSpec` / `SignalSpec` / `docs/SPEC.md` 冻结字段的单，六字段内必须列「**schema 变更评审项 + 需同步更新的 SPEC 节号**」，并按固定顺序执行：**先触发合规测试红 → 更新 SPEC 文本 → 走正式变更评审记录 → 才许合入**；未列评审项的单一律不得改冻结面（含「顺手加一列/加一字段」）。
- **合入门禁（硬约束，不是建议）**：⓪ ~~QT-60 未完成前 QT-06 不得开工~~ → **已满足（QT-60 于 2026-10-20 完成）**，tag 现在可以打在新仓；新增 ⓪' **QT-61 的「gofmt + doc-links」两项绿之前，QT-06 的「两仓 CI 绿」验收不可达**——要么先做 QT-61，要么把 QT-06 验收改成「本次改动相关的 job 绿 + 其余 known-fail 登记」；① **QT-18 未合入 main 前，QT-01 不得合入**——第一个 breaking 就在 QT-01 的 abi v2，护栏不在就会漏；② QT-05 未合入前，QT-07 不得开工；③ QT-07/QT-09 未绿前，不得在任何仓新增跨仓 `replace`（D29 §9 C1）。
- **新需求优先于存量未完成任务**；与旧任务重叠的可合并或直接删除，跨仓迁移的任务条目保留可追溯指针。
- 归档文件的状态标记若与自身完成记录矛盾，**只在文件头加订正说明**，条目正文与标题的历史措辞不改写。
