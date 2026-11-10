---
status: active
last-verified: 2026-11-10
verified-by: SVC-RETIRE-INDEX 七条退役线总索引（2026-11-07：判据逐字取自 `core-governance-decision-materials-2026-10-26.md` §4.1~§4.7，**基线当场重跑取当前值**并逐条登记与 2026-10-26 的差额；只读取证，0 代码删除、0 DDL 改动、0 服务仓改动。2026-11-10：补给 broker 线归零复跑 —— `SVC-CORE-BROKER-RETIRE` 施工完成，`tellurion/pkg/live` 与 `NewPostgresOrderStore` 均归零，见 §3.1）
---

# 旧实现退役总索引（SVC-RETIRE-INDEX）

> **本文件是索引，不是工单**：只回答「每条退役线删什么、判据怎么机械执行、怎么回滚、谁的单」。
> 执行还在各条 `SVC-CORE-*-RETIRE`（core `docs/TASKS.md` §六之二）与各仓 `SVC-*-G3`。
> **不执行删除**，**不包含 `DROP INDEX`**（材料 §3.2 P3 未裁：core 是否允许写 `DROP INDEX` 仍待主任拍板）。
>
> 判据来源：[`core-governance-decision-materials-2026-10-26.md`](core-governance-decision-materials-2026-10-26.md) §4.1~§4.7；
> 组内固定顺序见该文件 §5.1 = `DEPLOY-01 → DUAL-01 → RETIRE-INDEX → DATA → RUN → STRAT → RISK → AN → BROKER → AI`。
> 计数口径：`git grep ... -- '*.go' ':!*_test.go'`（生产侧，**含注释**）；归零断言必须再加注释过滤
> `| Where-Object { $_ -notmatch ':\s*//' }`，否则「删掉注释」也能假绿。

## 1. 七行总表

| # | 退役线 | owner（执行单） | 删除对象 | 停写判据（当前 → 目标 0） | 回滚 |
|---|---|---|---|---|---|
| 1 | data | `SVC-CORE-DATA-RETIRE`（core，串行第 4） | core 版 21 张 market/ingest 表 DDL 与索引（`market.index_daily` 按裁决 ⑰ 除外）；`pkg/data`；`cmd/data` 同步腿；`pkg/storage/{bulk_insert,ohlcv,stocks,calendar,sector,cache,ingest_raw,sync_jobs,index_daily,market_activity}.go`；`Dockerfile.service` 的 `./cmd/data` 腿 | ① 写点 **22** ② `NewETLPipeline` **7** ③ `.BulkInsert(` **1** ④ `tellurion/pkg/data` **29 行 / 24 文件** | `git revert` 单笔 + 回滚 tag；DDL 只增不改 ⇒ 旧语句删掉不删旧库表，读方切回直连即可 |
| 2 | run | `SVC-CORE-RUN-RETIRE`（core，串行第 5） | `backtest_jobs` / `walk_forward_reports` DDL + `pkg/storage/{backtest_jobs,cache}.go` job/WF 段 + `pkg/backtest/{job,walkforward}` 落库腿（`backtest_adjustment_anchors` 保留） | ① 写点 **7** ② 读点 **7** ③ `pkg/backtest/**` 的 `*storage.PostgresStore` **30** | 同上；`Engine.SetStore` 已存在 ⇒ 回滚 = 恢复 DB 注入 |
| 3 | strategy | `SVC-CORE-STRAT-RETIRE`（core，串行第 6） | `strategies` 表 DDL + `pkg/storage/strategies.go` + `pkg/strategy/db.go` DB 腿（`quant.strategy_state` / `quant.external_signals` 保留） | ① 写点 **2** ② 读点 **5** ③ `SaveStrategyConfig` 等 4 符号 **15 行** | 同上；本地 registry 保留 ⇒ 回滚 = DB 腿改回 `pkg/storage` |
| 4 | risk | `SVC-CORE-RISK-RETIRE`（core，串行第 7；**非表退役**） | `config/analysis-service.yaml` 的 `alert.*` / `risk_manager.*` 作为**限额真相**的那一半；检测器保留（U7） | ① `alert.*` 读取 **10** ② `risk_manager.*` 读取 **14** ③ `resolve_enabled` 仍为 `false`（`config/analysis-service.yaml:122`） | 无 DDL；回滚 = `risk_service.resolve_enabled` 改回 `false` + 配置兜底恢复为真相 |
| 5 | analytics | `SVC-CORE-AN-RETIRE`（core，串行第 8；归属二选一） | 要么 `pkg/backtest/{metrics,reporting}` 两包，要么「保留 + 冻结引用面」 | `tellurion/pkg/backtest/(metrics\|reporting)` **3**（迁移 → 0；冻结 → 恰好 3 且新增即红） | 迁移结局 = revert 两包；冻结结局 = 清空 repoguard 允许清单 |
| 6 | broker | `SVC-CORE-BROKER-RETIRE`（core，串行第 9；**表名为空**）✅ 2026-11-10 | `pkg/live` 的 MockTrader / `postgres_order_store.go` / `simulated_broker.go` / `broker/xtp` / `margin/` / `reconciliation/` / `stockstate/`（后三者随包改名为 `pkg/trading/*`，按「无对应 HTTP 面」不删） | ① `"tellurion/pkg/live"` **18 行 / 16 文件** ⇒ **0**（2026-11-10）② 子包 **5** ⇒ **3**（`margin` / `reconciliation` / `stockstate`）③ `NewPostgresOrderStore` 生产调用 **0** | revert + 恢复 port 注入；QT-51 的 `ExecutionPort` 在装配层，回滚不碰引擎；旧路径由 `internal/repoguard/legacy_live_import_test.go` 设禁（import 或目录复活即红） |
| 7 | ai | `SVC-CORE-AI-RETIRE`（core，串行第 10；2026-11-10 迁入本节正文） | 7 张表 core DDL/索引 + `pkg/storage/{experiments,factor_hypothesis,research}.go` + `SeedBuiltinFactorHypotheses` 启动 seed + `pkg/mining` AI 落库腿（后者已随 `SVC-CORE-DATA-RETIRE` 消失） | ① 写点 **6** ② `SeedBuiltinFactorHypotheses` **3** ③ 7 表 `CREATE TABLE` **7**；2026-11-10 复跑 = **0 / 0 / 0** | revert + 重启即恢复 seed；agent 侧 `ai.*` 数据不回写 core；历史行未删，只删 DDL 与读写实现 |

> 反向护栏（每行的「改坏应红」）：1 → 新建 `internal/repoguard/storage_market_tables_retired_test.go`；2 → `pkg/backtest/**` 禁 import `tellurion/pkg/storage`；3 → `pkg/strategy/**` 禁出现 `strategies`；4 → 契约侧 `Bundle → ParseBundle` 固定向量（`tellurion-contracts:risk/snapshot_test.go`）；5 → 3 条 import 快照断言；6 → 全仓禁 `import "tellurion/pkg/live"`；7 → **core** `internal/repoguard/storage_ai_tables_retired_test.go`（七表零 DDL / 三文件不存在 / 零写点）+ **agent** `pkg/aistore/schema_core_drift_test.go` 的 core 缺表断言（原逐列对拍已随 core DDL 退役翻面，2026-11-10）。

## 2. 单号 ↔ 各仓 G3 双向指针

| 退役线 | core 执行单 | 服务仓侧 | G3 单 TODAY 状态 |
|---|---|---|---|
| data | `SVC-CORE-DATA-RETIRE` | `tellurion-data` | `SVC-DATA-G3` ⬜ |
| run | `SVC-CORE-RUN-RETIRE` | `tellurion-orchestration` | `SVC-ORCH-G3` ⛔（等 B 组退役单） |
| strategy | `SVC-CORE-STRAT-RETIRE` | `tellurion-strategy` | `SVC-STRAT-G3` ⛔（前置未开） |
| risk | `SVC-CORE-RISK-RETIRE` | `tellurion-risk` | `SVC-RISK-G3` ⬜ |
| analytics | `SVC-CORE-AN-RETIRE` | `tellurion-analytics` | `SVC-AN-G3` ⛔（无消费方） |
| broker | `SVC-CORE-BROKER-RETIRE` | `tellurion-broker` | `SVC-BROKER-G3` ✅（2026-11-10：core 零调用已满足，步骤 1~4 全落） |
| ai | `SVC-CORE-AI-RETIRE` | `tellurion-agent` | `SVC-CORE-AI-RETIRE` ✅（2026-11-10，core 执行；agent 侧只留指针 + 停写验收） |

> **AI 线的 R4 偏差已按代裁解除（待主任追认）**：删除对象全在 core，故正文迁入 core `docs/TASKS.md` §六之二 串行第 10（2026-11-10）；
> agent 台账改为指针 + 本仓停写验收，不再双写正文。

## 3. 判据复跑（2026-11-07，本机真实输出）

抽样 2 条（⑤ 要求）：

| # | 命令 | 今天结果 | 退出码 |
|---|---|---|---|
| 1 | data ① 写点 grep（22 表名，完整式见 §1 与材料 §4.1） | **22 行**（含注释 `pkg/storage/sync_jobs.go:81`） | **0** |
| 2 | `git grep -n 'tellurion/pkg/live' -- '*.go' ':!*_test.go'` | **18 行 / 16 文件** | **0** |

其余五条同日复跑，与材料 2026-10-26 基线的差额（**如实登记，不硬凑**）：

| 线 | 指标 | 材料基线 | 今天 | 差额说明 |
|---|---|---|---|---|
| data | ① | 23 | **22** | 材料注明 23 = 22 活写点 + 1 注释；今天 22 里仍含该注释 ⇒ **活写点少 1**，本轮未逐行核对是哪一处（可能是已切 HTTP 的 `capital_flow` / `top_list` 段），归零断言以今天 22 为起点 |
| data | ②③④ | 7 / 1 / 29 行 24 文件 | **7 / 1 / 29 行 24 文件** | 一致 |
| run | ①② | 7 / 7 | **7 / 7** | 一致 |
| run | ③ | 材料只列 `engine.go:135` / `:511` 两处字段与签名 | **`*storage.PostgresStore` 30 处** | 材料是「关键行」不是全量计数；30 是全仓 `pkg/backtest/**` 出现次数 |
| strategy | ①② | 2 / 5 | **2 / 5** | 一致 |
| strategy | ③ | 19 行 | **15 行** | 少 4 行，本轮未逐行核对；以今天 15 为起点 |
| risk | ① | 11 行 | **10 行** | 少 1 行（`alert.enabled` 走 `GetBool`，不在材料原模式内）；②14 行一致 |
| risk | ③ | 1（值 `false`） | **`config/analysis-service.yaml:122` = `false`**；全仓 `resolve_enabled` 8 处（读取 2、报错文案 2、测试 3、注释 1） | 材料只指配置文件那一处，不矛盾 |
| analytics | — | 3 | **3** | 一致 |
| broker | ① | 20 行 / 18 文件 | **18 行 / 16 文件** | 差额可解释：QT-51 已把 `pkg/backtest` 的 `pkg/live` import 摘掉（`engine.go` 与 `execution/live_bridge.go`），与 QT-51 施工记录一致 |
| broker | ② | 5 | **5** | 一致 |
| ai | ①②③ | 6 / 3 / 7 | **6 / 3 / 7**（2026-11-07）；**2026-11-10 退役后复跑 = 0 / 0 / 0**（①写点 ②seed ③七表 DDL；`pkg/mining` 的 DB 语句与 `pkg/storage` import 亦为 0） | 一致；退役单施工见 core `docs/TASKS.md` §六之二 串行第 10 |

### 3.1 broker 线归零复跑（2026-11-10，本机真实输出）

`SVC-CORE-BROKER-RETIRE` 步骤 A~D 施工完成后按本索引判据复跑：

| 指标 | 2026-11-07 基线 | 2026-11-10 | 命令 / 依据 |
|---|---|---|---|
| ① `tellurion/pkg/live` import | 18 行 / 16 文件 | **0** | `git grep -n 'tellurion/pkg/live' -- '*.go'` 无命中（EXIT=1，属正常）；`pkg/live` 目录不存在 |
| ② `pkg/trading` 子包 | 5（`margin` / `reconciliation` / `stockstate` / `broker` / `broker/xtp`） | **3**（`margin` / `reconciliation` / `stockstate`） | `Get-ChildItem -Directory pkg\trading`；`broker` 整目录已删 |
| ③ `NewPostgresOrderStore` 生产调用 | 0 | **0** | 全仓 `*.go` `Select-String` 计数 = 0 |

> 边界偏差（已登记、待主任追认）：broker HTTP 面没有融资融券 / 对账 / 证券状态端点，故 `margin` / `reconciliation` / `stockstate` **随包改名保留**，不删；删除对象以「通道实现」为界，不以「子包」为界。

## 4. 边界与未决

- **不包含 `DROP INDEX`**：core 102 条 `CREATE INDEX` / 95 个索引名的退役判据在材料 §3，`P3`（是否允许 core 写 `DROP INDEX`）**未裁**，本索引不代裁；约束背书索引（PK / UNIQUE）只能随表删，不得单独 DROP。
- **不覆盖动态拼表名**：material §2.4 的 9 张表在 core 无字面 SQL，写点为零不等于不写；停写验收必须以运行/对拍为准（`QT-81` 的 `cmd/recon`）。
- **表级对拍未完成前不得停写**：`cmd/recon` 代码侧已落（QT-81 🔶，真库腿未执行），除 ai 簇外的「对拍方式」列仍为「无」。
- **本文件不执行删除**，也不改 `pkg/storage/postgres.go`；七条**全部触**该文件 ⇒ 严格串行，一单一次编辑。
- 各条「旧路径零调用」的 ✅ 判定在各自执行单，不在本索引。

## 5. 自检（可复现）

| 判据 | 期望 | 今天 |
|---|---|---|
| 七条齐全 | 7 行，owner / 删除对象 / 判据 / 回滚 四列非空 | 是 |
| 各仓 G3 反查本单 | 六个服务仓 + agent 的 G3 单都指向 `SVC-CORE-*-RETIRE` | 是（`tellurion-data:docs/TASKS.md`、`tellurion-orchestration:44/55`、`tellurion-strategy:45`、`tellurion-risk:80`、`tellurion-analytics:47`、`tellurion-broker:44/55`、`tellurion-agent:183`） |
| 抽样 2 条判据 | 退出码 0（有命中即 0；归零后 git grep 无命中返回 1，属正常） | 2026-11-07：22 行 / 0；18 行 16 文件 / 0。2026-11-10 复跑：data 22 行 / 0；broker **0 行 / 0**（已归零，见 §3.1） |
| 与材料 §4 不矛盾 | 差额逐条写明 | 是（§3） |
