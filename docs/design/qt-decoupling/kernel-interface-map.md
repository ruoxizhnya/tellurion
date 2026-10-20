# Go 内核接口面清单（输入 / 输出）

> 用途：契约外置与"引擎摘出"的输入。只记**接口与 schema**，不记实现。
> 建立：2026-10-19 ｜ 依据：`pkg/backtest/engine.go`、`pkg/backtest/contracts/`、`pkg/strategy/interfaces.go`、`pkg/domain/`

---

## 一、输入面（5 类入口）

### 1. 行情数据 —— 注入 Provider

| 项 | 内容 |
|---|---|
| 入口 | 构造注入 `NewEngine(v *viper.Viper, provider marketdata.Provider, logger)`；可换源 `SetDataAdapter(*marketdata.DataAdapter)`、运行时 `SwitchDataSource(ctx, name, provider)` |
| 接口 | `marketdata.Provider`：`GetOHLCV` / `BulkLoadOHLCV` / `GetTradingDays` / `GetIndexConstituents` / `GetFundamental` / `GetLatestPrice` / `GetStocks` / `GetStock` / `CheckConnectivity` / `CheckCalendarExists` |
| 数据 schema | `domain.OHLCV`：`Symbol` · `Date`（bar 周期起点）· `Open/High/Low/Close` · `Volume`（**单位=股**，规范层口径） |
| 频率 | 1d / 1m / 15m，由规范层 Router 聚合 |
| 票池来源 | `StockPool []string` 直接给，或 `IndexCode` 经 `GetIndexConstituents` 解析 |

### 2. 因子 / 基本面 / 日内特征 —— 按能力注入策略

| 项 | 内容 |
|---|---|
| 基本面 | `SetFundamentals(map[string]strategy.FundamentalSeries)` |
| 因子 | 经 `FactorZScoreReader` 注入（策略实现 `FactorAware` 才拿到） |
| 日内 | 经 `IntradayFeatureProvider` 注入（策略实现 `IntradayAware` 才拿到，D6） |
| 纪律 | 供给为 nil 时**不注入**，策略用到特征要明确失败，不塞空 provider 假装有数据 |

### 3. 运行参数 —— `contracts.BacktestRequest`（RunSpec）

> **四契约术语对齐（ADR-031 D1，2026-10-19 审查补）**：ADR-031 冻结的内核边界四契约为 **RunSpec / SignalSpec / ResultSpec / EngineRunner**。本清单的对应关系：运行参数 = RunSpec（`BacktestRequest`+`TradingConfig`）；**信号面 = SignalSpec**（`domain.Signal` + K7 `quant.external_signals` + `SignalProvider`，即下文入口 4 与链路内 Signal）；输出 = ResultSpec（`BacktestResponse`/`domain.BacktestResult`）；执行入口 = EngineRunner（`contracts.BacktestRunner`）。

字段：`Strategy` · `StockPool []string` · `IndexCode` · `StartDate` / `EndDate` · `InitialCapital` · `RiskFreeRate` · `Kernel`（路由键，空=默认 `go`，**默认值不回填**）· `EngineParams map[string]any`（**未知名拒绝而非忽略**）· `Signals`（K15 目标权重流，可选：`symbol/weight/as_of`）

### 4. 策略 —— 三种进来方式

| 方式 | 机制 | 现状 |
|---|---|---|
| 编译进二进制 | `pkg/strategy/plugins/*.go` 的 `init()` → `GlobalRegister` → `DefaultRegistry` | ✅ 生产主力（14 个自注册） |
| `.so` 热加载 | `PluginLoader`，导出符号固定为 `"Strategy"` | ✅ 生产（依赖 `plugins.directory`） |
| WASM | 未接线（repoguard 白名单） | ❌ |

策略契约（组合接口）：
```
Strategy = StrategyCore(Name/Description)
         + Configurable(Parameters/Configure)
         + SignalGenerator(GenerateSignals/Weight)
         + ResourceManaged(Cleanup)
流式额外实现 BarHandler(OnBar / Signals / Warmup)
```
参数来源：注册对象 + `Configure()`。**DB `strategies` 表的 `params` JSON 只是管理面元数据，执行路径不查它。**

### 5. 风控 —— **外部注入，不在策略里写**

| 层 | 机制 | 强制性 |
|---|---|---|
| **内核风控（主）** | `SetRiskManager(*risk.RiskManager)`，由外部注入；覆盖止损/止盈、regime、VaR、波动率、手数等 | **强制**，统一口径 |
| 策略内风控（可选） | 表达式策略的 `RiskController` + `RiskConfig`：在**权重层**做 per-position cap / max open positions 裁剪 | 策略作者自选，**不是系统防线** |

其他注入点（完整清单）：
`SetExecutionService(ExecutionService)` 撮合 · `SetStore(*storage.PostgresStore)` 存储 · `SetParallelWorkers(n)` 并行 · `SetSignalObserver(func(date, signals))` 观测 · `SetListingWindows(map[string]storage.ListingWindow)` 上市窗口 · `SetLiveTrader(live.LiveTrader)` 实盘通道（**BE-02 待切**）

---

## 二、输出面

### 主输出 `domain.BacktestResult`

| 分组 | 字段 |
|---|---|
| 收益 | `TotalReturn` · `AnnualReturn` |
| 风险 | `SharpeRatio` · `SortinoRatio` · `MaxDrawdown` · `MaxDrawdownDate` · `CalmarRatio` |
| 交易 | `WinRate` · `TotalTrades` · `WinTrades` · `LoseTrades` · `AvgHoldingDays` |
| 明细 | `PortfolioValues []PortfolioValue`（资金曲线）· `Trades []Trade`（成交明细） |
| 元信息 | `StartDate` / `EndDate` · `UniverseMaxSize *int`（nil=未评估，0=明确空）· `InvalidReasons []string`（`CheckValidity` 裁定，空=有效） |

另有 `domain.RiskMetrics`（Volatility / Beta / VaR95 / CVaR95 …）与 `metrics.TradeMetrics`。
契约层输出：`contracts.BacktestResponse`（`EngineRunner` 用，含 ResultSpec）。

### 中间产物（链路内）

`domain.Signal`（`Symbol` · `Date` · `Direction` · `Strength` · `CompositeScore` · `Factors map` · `Metadata map` · `LimitPrice` · `OrderType` · 执行算法指定）
→ 风控 verdict → `ExecutionService` 撮合 → `Tracker` 簿记（T+1）→ 组合与资金曲线。

### 内核外

报表 `reporting`（compare / correlation / export）—— 属呈现层，不在内核（BE-13）。

---

## 三、一句话接口面总结

```
输入：Provider(行情) + Fundamentals/Factor/Intraday(特征) + BacktestRequest(参数)
    + Strategy(代码+参数，注册表解析) + RiskManager(风控) + ExecutionService(撮合) + Store
输出：BacktestResult(收益/风险/交易/资金曲线/成交明细) + 链路内 Signal→verdict→成交→簿记
不归内核：编排（job/batch/walkforward）· 报表 · 分析（基准/归因/敏感性）
```
