# 回测结果与 provenance 保留期口径

> 任务：`CORE-RESULT-RETENTION`。本文是保留期判据的唯一口径说明，实现见
> `pkg/storage/run_retention.go`，周期任务见 `cmd/analysis/run_retention_loop.go`。

## 1. 适用表与三档保留期

| 数据 | 载体 | 保留期 | 到期动作 |
|---|---|---|---|
| 明细：权益曲线点、成交明细 | `run.backtest_results.result` 的 `portfolio_values` / `trades` 键 | **90 天** | 从 JSONB 摘除这两个键，置 `detail_purged_at` |
| run 级汇总：收益、回撤、参数、`factor_coverage`、`pe_source` 等 | `run.backtest_results` 行（除上述两键外的全部内容） | **365 天** | 删除整行 |
| provenance（三 hash 身份坐标、数据版本） | `quant.run_provenance` | **365 天** | 与结果行同事务删除 |

三档是**上限**，不是到期必删：被引用的 run 无论多老都保留（见 §3）。

## 2. 时间基准：`finished_at`

判据用 **run 的完成时间 `finished_at`**，不用 `created_at`。

`run.backtest_results` 在 run 终止时写入，长跑 run（例如批量回测跑数小时）的
`created_at` 会显著早于实际完成时间；若用 `created_at` 起算，这些 run 的明细
会提前过期。`finished_at` 由 `SaveBacktestResult` 每次落结论时置为 `now()`，
worker 重试只刷新它、不改 `created_at`。

存量行在迁移时回填 `finished_at = updated_at`。判据统一取
`COALESCE(finished_at, updated_at, created_at)`：即使 `finished_at` 因历史原因
为空，也只会更晚起算（保守）。

## 3. “被引用”的判定

只要下列**真实表**中存在指向该 run 的行，就视为被引用，明细与汇总都不删：

| 表 | 引用列 | 说明 |
|---|---|---|
| `run.run_artifacts` | `run_id` | 编排侧 run 身份/产物指针 |
| `quant.run_analysis_artifacts` | `run_id` | 分析产物载荷 |
| `quant.strategy_state` | `run_id` | 策略状态分片 |
| `audit.message_log` | `run_id` | 审计事件流 |
| `quant.portfolio_snapshot` / `quant.positions` / `quant.risk_events` / `quant.orders` / `quant.fills` / `quant.recon_report` | `run_id` | 内核模块表；运行时存在才检查，当前 core 迁移不创建 |

判定规则：

- 用 `to_regclass` 预检表是否存在；不存在 ⇒ 不算引用。
- 表存在则执行 `SELECT EXISTS (SELECT 1 FROM <table> WHERE run_id = $1)`。
- **探测或查询出错时按“被引用”处理（保留）**，并计入 `skipped_reference_unknown`。
  宁可多留一档，也不删掉别人正指着的 run。
- `quant.run_provenance` 自身不算引用；没有结果行的孤儿 provenance 不自动删除
  （没有 `finished_at` 可判定完成时间，按保守原则保留）。
- `run.batch_runs` / `run.parent_jobs` / `run.walk_forward_reports` 没有 `run_id`
  列，不作为引用来源；不对 JSON 文本做模糊匹配。

## 4. 运行中的 run

候选集只来自 `run.backtest_results`，而结果行只在 run 终止时写入。若编排侧的
`run.backtest_jobs` 表存在，且该 run 的状态不是终态（`pending` / `running` /
`cancelling` 等），则跳过；表不存在时按终态处理（结果行本身即为终态产物）。
状态查询出错时按“运行中”处理（跳过）。

## 5. 为什么是 90 / 365

- **90 天 ≈ 一个季度**：足够覆盖近期的逐点复盘与归因，同时把增长最快的
  时序明细限制在可控范围内。
- **365 天**：覆盖年度复盘与 provenance 追溯，让汇总与溯源在一年内可查。
- 这两个数是工程取舍（够复盘、又不至于无限膨胀），**不引用任何合规条款**。

## 6. 执行语义

1. **顺序**：先明细阶段（摘 JSONB 键），再汇总阶段（删结果行 → 删 provenance
   行）。明细是结果行内部的内容，不存在需要单独删除的“子表”；同一事务内先清
   明细键、再删行。
2. **逐 run 一个事务**：每个 run 独立提交，不做一个大事务锁库。失败时停在已
   提交前缀，已提交的不会回滚，下一次从剩余候选续跑。
3. **幂等**：明细阶段带 `detail_purged_at IS NULL` 守卫；汇总阶段删过的行不再
   出现。同一批跑两次，第二次删除数为 0。
4. **dry-run**：只读扫描，返回逐 run 的候选动作与行数（`trades` /
   `equity_points`），不执行任何写操作。
5. **单批上限**：每阶段每次最多处理 `batch_size` 个 run，避免一次扫描拖住库。
6. **从未有明细 vs 已清理**：failed 的空载荷结果不会被标记 `detail_purged_at`；
   只有真实摘除过明细的行才写 `detail_purged_at`。

## 7. 配置

`config/analysis-service.yaml`：

```yaml
result_retention:
  enabled: true      # 总开关；运维可临时停
  dry_run: false     # true 时只输出候选，不删
  detail_days: 90
  summary_days: 365
  interval: 24h
  batch_size: 100
```

环境变量覆盖（viper `AutomaticEnv`，`.` → `_`）：
`RESULT_RETENTION_ENABLED`、`RESULT_RETENTION_DRY_RUN`、
`RESULT_RETENTION_DETAIL_DAYS`、`RESULT_RETENTION_SUMMARY_DAYS`、
`RESULT_RETENTION_INTERVAL`、`RESULT_RETENTION_BATCH_SIZE`。

未配置时的默认值就是 90 / 365；`enabled` 默认 `true`，`dry_run` 默认 `false`。

## 8. 读路径如何区分

明细被清理后，`GET /api/backtest/{id}/trades` 与 `/equity` 的结果面响应会带
`detail_purged_at` 字段；从未有过明细的 run 不出现该字段。两者在 API 返回里
形状不同，不会把“按策略清理”误读成“这个 run 本来就没有明细”。

## 9. 不在本口径内

- `quant.run_analysis_artifacts` 自身的保留期归 `CORE-ANALYSISARTIFACT-RETENTION`。
- provenance 的永久保留是历史设定，已被 2026-11-12 裁决取代：现在为 365 天，
  但被引用时仍永久保留。
