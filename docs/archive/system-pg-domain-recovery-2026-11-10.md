# 系统 PG 整库重建后各域表恢复核对（2026-11-10）

> 背景：2026-11-10 把系统实例（`postgresql-x64-16`，`127.0.0.1:5432`）的 `tellurion` 库整库
> DROP + 重建，只按 tellurion-data 的 DDL 初始化了 `market` / `ingest`（见
> `tellurion-data:docs/archive/system-pg-init-2026-11-10.md`）。本文件核对其它域的表
> **能不能随各自服务启动自动重建**，并把实际执行与结果留证。
>
> 边界：只动本机 `tellurion` 库；远端/生产零动作；6 个 `tellurion_*` 测试库未动。

## 1. 结论（按三类分）

| 域 / schema | owner 与 DDL 入口 | 分类 | 本库结果 |
|---|---|---|---|
| `public.*`（`users` / `audit_logs` / `stock_fundamentals` / `factor_cache` / `factor_returns` / `ic_analysis` / `fundamentals_detail` / `backtest_adjustment_anchors` / `data_migrations` / `factor_definitions` / `factor_registrations` / `factor_registration_audit` / `factor_mining_records`） | core `pkg/storage/postgres.go` 的 `migrationStatements()`，`storage.NewPostgresStore` 启动时执行 | **服务启动即建表** | 已重建 13 张 |
| `quant.*`（`strategy_state` / `external_signals` / `sw_industry_tree` / `supply_chain_edge` / `industry_vitality` / `etf_constituent` / `macro_series` / `run_provenance` / `run_analysis_artifacts` / `alert_outbox`） | 同上（含包级追加数组） | **服务启动即建表** | 已重建 10 张 |
| `audit.message_log` | core `pkg/storage/postgres.go:715` | **服务启动即建表** | 已重建 1 张 |
| `run.backtest_results` | core `pkg/storage/backtest_results.go`（追加进 `migrationStatements()`） | **服务启动即建表** | 已重建 1 张 |
| `market.index_daily` | core（裁决 ⑰：owner = core，不属 data 仓） | **服务启动即建表** | 已重建 1 张 |
| `ai.*`（`experiments` / `research_profile` / `research_conclusion` / `research_question` / `factor_genes` / `strategy_genes` / `factor_hypothesis` / `generation_chain`） | agent `pkg/aistore/schema.go`，`aistore.NewStore` → `Migrate` 启动时执行 | **服务启动即建表** | 已重建 8 张（含 `ai.research_*` 3 张） |
| `research.*`（`profile` / `conclusion` / `question`） | **已废弃**：core 的 DDL 与 5 条索引已随 `SVC-CORE-AI-RETIRE`（2026-11-10）删除，见 `pkg/storage/postgres.go:687-688`；owner 改为 agent 的 `ai.research_*` | **已废弃、本就不该存在** | **不重建**（依据如上；成功承接者为 `ai.research_*`，已建） |
| `run.*`（`backtest_jobs` / `run_artifacts` / `batch_runs` / `walk_forward_reports` / `worker_registry` / `parent_jobs`） | orchestration `internal/queue/ddl.go`，`pgstore.Store.Migrate` 启动时执行 | **服务启动即建表**（但**不属本库**） | 未在本库建：删除前的 `tellurion` 库没有这 6 张表（只有 core 的 `run.backtest_results`）；其 DSN 见 `tellurion-orchestration:README.md`（`tellurion`）与 `docs/runbook.md`（`tellurion_orchestration`），按各自装配库为准 |
| `analysis.*` / `asset.*` / `risk.*` / `broker.*` | analytics `internal/analysis/ddl.go`、strategy `internal/asset`、risk `cmd/riskservice`、broker `internal/ackstore/ddl.go`，均启动时 `Migrate` | **服务启动即建表**（但**不属本库**） | 未在本库建：删除前的 `tellurion` 库没有这些 schema；各自 DSN 指向独立库（`tellurion_analytics_test` / `tellurion_strategy_test` / `tellurion_risk_test` / `tellurion_broker_test` 等） |

**没有发现「完全没有建表逻辑」的域**；也不需要新增迁移脚本。

## 2. 实际执行的命令与输出

按各服务的**启动路径**执行建表 DDL（临时程序调入口函数，跑完即删，不入库）：

| 步 | 命令 | 输出 |
|---|---|---|
| core | `MIGRATE_DSN=postgres://postgres:postgres@127.0.0.1:5432/tellurion?sslmode=disable go run ./tmp_core_migrate`（内部 `storage.NewPostgresStore`） | `CORE_MIGRATE_OK`，`EXIT=0` |
| agent | `MIGRATE_DSN=… go run ./tmp_ai_migrate`（内部 `aistore.Migrate`） | `AGENT_MIGRATE_OK`，`EXIT=0` |
| 幂等复跑 | 上面两条各再跑一次 | 均 `EXIT=0`，各 schema 表数不变 |

## 3. 复核结果（真实查询）

```
-- 复跑后
ai|8
audit|1
ingest|4
market|19        -- 18 张 data + core 的 market.index_daily
public|13
quant|10
run|1
-- 无 research schema（按废弃口径不建）
```

与删除前的旧库对照：

- 旧库非空表里属于 core 的 4 张（`public.factor_definitions`=12、`public.factor_hypothesis`=12、`public.backtest_adjustment_anchors`=2、`public.data_migrations`=2）——前两者中 `factor_hypothesis` 的承接者是 `ai.factor_hypothesis`（已建，本次只跑 `Migrate` 未跑 `SeedBuiltins`，故行数 0）；其余随启动 DDL 重建。
- `public.ohlcv_daily_qfq` **没有**被 core 重建：它的 owner 已随数据面退役转给 data 仓（现为 `market.ohlcv_daily_qfq`），符合新结构。
- `public.factor_hypothesis` **没有**被 core 重建：owner 已改判 agent 的 `ai.factor_hypothesis`，符合 `SVC-CORE-AI-RETIRE`。

## 4. 遗留风险

1. `tellurion-agent/config/ai-service.yaml` 的 `database.password` 为空串；本机 pg_hba 是 `scram-sha-256`，**按该配置直接起 ai 服务会在连库阶段失败**（本轮用带口令的 DSN 验证迁移路径）。空口令是否为环境变量注入尚未核实。
2. core 启动 DDL 仍会建 `public.data_migrations`（旧哨兵表），与 data 仓的 `ingest.data_migrations` 并存——属 X1d 物理搬迁（D4）未完成的过渡态，不是本轮新问题。
3. 本文件只验证了**建表路径**（入口函数），未启动完整 core / agent 服务进程；「服务能否整体起来」未做端到端验证。
4. orchestration / analytics / strategy / risk / broker 的 `run.*` / `analysis.*` / `asset.*` / `risk.*` / `broker.*` 未在本库执行（本库删除前也没有这些 schema），其自动重建能力为代码级结论，未跑真库腿。
