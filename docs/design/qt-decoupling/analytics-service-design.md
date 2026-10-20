# 分析/报表服务设计（X4 · Analytics Service）

> ADR-D21 草案 · 2026-10-19 · 关联 ADR-D18（拆分排序「性价比最高」）、BE-05/BE-06（缺口登记）
> 性质：设计固化，未实施

## 1. 服务定位

**回测结果的分析与呈现层。纯函数 + 只读数据，天然无状态、可水平扩容。**

| | 内容 |
|---|---|
| **管** | 绩效指标、基准对比、业绩归因、参数敏感性、蒙特卡洛/鲁棒性、策略相关性、报表导出 |
| **不管** | 回测执行、信号生成、数据供给、风控裁决 |

**为什么它排在拆分前列（ADR-D18 判定「性价比最高」）**：
1. 正好补 BE-05/BE-06 缺口（基准/归因/敏感性/蒙特卡洛在回测族内**零实现**，`engine.go:1059` 只有一行注释）；
2. 纯函数层，不动引擎内核，天然满足「不在内循环热路径」硬闸门；
3. **报表需求变了不用动引擎**——这正是"廉价拓展"目标的直接兑现。

## 2. 现状零件（实测）

| 零件 | 位置 | 说明 |
|---|---|---|
| `CalculateMetrics` → `domain.RiskMetrics` | `pkg/backtest/metrics/performance.go:13` | 波动/Beta/Sharpe/Sortino/MDD/VaR95/CVaR95 |
| `CalculateTradeMetrics` | 同上 `:54` | 胜率/笔数/平均持有天数 |
| `CalculateReturns` / `MaxDrawdown` / `Sharpe` / `Sortino` / `Calmar` | 同上 `:90-208` | 纯函数，可直接搬 |
| `CompareResolver` / `CompareReport`（多策略对比） | `pkg/backtest/reporting/compare.go` | 含 invalid/missing/best 细节 |
| `CalculateStrategyCorrelation` → `CorrelationMatrix` | `pkg/backtest/reporting/correlation.go:40` | 策略相关性 |
| HTML 报表导出 `HTMLReportOptions` | `pkg/backtest/reporting/export.go` | 呈现层 |
| 因子归因（回测外） | `pkg/data/factor_attribution.go` | 因子侧 IC/收益归因，`ic_analysis` / `factor_returns` 表已有 |

**缺口（本服务要新建的）**：

| # | 缺口 | 证据 |
|---|---|---|
| BE-05 | **基准对比**（相对基准超额、跟踪误差、信息比率） | 回测族内零实现 |
| BE-06 | **业绩归因**（Brinson / 因子归因接进回测结果）、**参数敏感性**、**蒙特卡洛/bootstrap** | 全族 grep 零命中 |
| — | 收益类指标进统一结果 schema（年化/总收益/换手/IR 目前不在 `RiskMetrics`） | BE-16 待查项一并收口 |

## 3. 数据归属（schema = `analysis`，DDL 唯一 owner）

| 表 | 内容 | 现状 |
|---|---|---|
| `analysis.reports` | 分析任务结果（run_id → 报告 JSON/HTML） | 待建 |
| `analysis.benchmarks` | 基准序列登记（基准代码、来源、hash） | 待建（基准行情本体归数据服务，此处只登记引用） |
| `analysis.sensitivity_runs` | 参数敏感性网格结果 | 待建 |
| `analysis.mc_runs` | 蒙特卡洛/bootstrap 批次（seed 留痕！） | 待建 |
| `analysis.attribution` | 归因结果（Brinson/因子） | 待建，与 `ic_analysis`/`factor_returns`（quant）区分：那是因子研究侧，这是回测结果归因 |

**只读输入**：回测结果（`run`/内核输出）、基准行情（数据服务）、因子值（数据服务）。**本服务不写它们的表。**

**确定性红线**：蒙特卡洛/bootstrap 必须**种子化留痕**（沿用 `engine.rng` 的 Seed 治理，ADR-D15）——`mc_runs.seed` 必填，否则同参分析不可复现。

## 4. API 面

```
POST /api/analysis/metrics          {run_id}                     → 绩效指标全集（含收益类补齐）
POST /api/analysis/benchmark        {run_id, benchmark_code}     → 超额/IR/跟踪误差/滚动对比
POST /api/analysis/attribution      {run_id, method: brinson|factor} → 归因表
POST /api/analysis/sensitivity      {run_id, param_grid}         → 敏感性矩阵（异步 job）
POST /api/analysis/montecarlo       {run_id, n, seed}            → 分布/置信区间（异步 job）
POST /api/analysis/correlation      {run_ids[]}                  → 相关性矩阵
GET  /api/analysis/reports/{id}                                  → 报告（JSON / HTML）
GET  /api/analysis/reports/{id}/export?format=html|csv|pdf
```

- 全部**只读**语义（POST 只是"提交计算"，不改上游数据）；
- 敏感性/蒙特卡洛是重计算 → **异步 job**（复用 X5 编排的 job 机制，或本服务自建轻量队列）；
- 与内核边界：只按 `run_id` 拉结果，**零 import `pkg/backtest` 引擎**（只依赖结果 DTO）。

## 5. 与内核的边界

```go
// contracts
type ResultReader interface {
    GetResult(ctx, runID string) (*BacktestResult, error)   // 明细走分页/artefact（ADR-D7 粒度纪律）
    GetEquityCurve(ctx, runID string) ([]PortfolioValue, error)
}
type BenchmarkReader interface {
    GetSeries(ctx, code string, start, end time.Time) ([]OHLCV, error) // 数据服务的只读视图
}
```

分析服务依赖这两个 port，**不依赖引擎**。引擎输出侧只需保证结果落库/落 artefact 可读。

## 6. 为什么这样拆是划算的（对照缺口）

| 报表/分析需求变化 | 现状（混在回测里） | 目标（独立服务） |
|---|---|---|
| 加一个新指标（如 Sortino 修正） | 改引擎，重新发内核 | 改本服务，引擎零改动 |
| 加基准对比 | 改引擎（BE-05 阻塞中） | 本服务纯函数补齐 |
| 报表格式/导出变化 | 牵动引擎发版 | 只改 export 层 |
| 批量跑敏感性（重计算） | 抢回测算力 | 本服务扩容，互不干扰 |

## 7. 分期

| 阶段 | 内容 | 验收 |
|---|---|---|
| A0 | metrics 函数迁入 + 收益类指标补齐（解 BE-16） | 指标口径与现状逐位一致（dualrun 对拍） |
| A1 | **基准对比**（BE-05） | 任一回测能出相对基准报告 |
| A2 | **归因 + 敏感性 + 蒙特卡洛**（BE-06） | seed 留痕可复现；敏感性异步跑 |
| A3 | 报表导出统一（HTML/CSV）+ 相关性矩阵 | compare/correlation 从 backtest/reporting 全量迁出 |

## 8. 风险

| 风险 | 兜底 |
|---|---|
| 指标口径迁移漂移 | A0 阶段逐位对拍（旧 vs 新实现同输入） |
| 蒙特卡洛不可复现 | seed 必填 + 结果留痕 |
| 结果传输量大（bar 级明细） | 摘要默认 + 明细分页/artefact（ADR-D7 已定） |
| 与因子归因（`pkg/data`）职责重叠 | 边界：因子侧=研究期归因；本服务=**回测结果**归因，接口分开 |
