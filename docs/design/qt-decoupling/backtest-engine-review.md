# 回测引擎 review 记事本

> **定位**：问题登记本，不是方案。每条只记「现象 + 证据 + 初步方向」，细化与决策留到后面做。
> 建立时间：2026-10-19 ｜ 依据：core 仓源码实测（非测试代码）+ `internal/repoguard` 未接线白名单
> 编号规则：`BE-xx`（回测引擎问题）｜等级：🔴 阻塞 / 🟠 重要 / 🟡 建议 / 📌 待查

---

## 0. 现状速览（已具备，别重复造）

| 能力 | 证据 | 评价 |
|---|---|---|
| 日循环 + 交易日历 | `pkg/backtest/engine.go:842` | ✅ |
| 批式 / 流式双模式策略接口 | `GenerateSignals` 与 `BarHandler`，靠类型系统保证走对分支（`signal_provider.go:148`） | ✅ 设计干净 |
| 双层防前视 | 契约级 `RejectFutureSignals`（`:191`）+ WASM host `get_bar(t_offset≤0)` 越界 trap（`host_api.go:94`） | ✅ 强于多数自研引擎 |
| 可复现性 | 喂 bar 按 symbol 字典序，规避 map 随机序污染递推状态（`:153`） | ✅ |
| 风控前置 | RiskManager verdict 后才进撮合 | ✅ |
| 撮合与结算 | `ExecutionService`（限价/滑点）+ `Tracker` T+1 簿记 + `settlement` | ✅ |
| 成本模型 | `fees`（A 股费率）+ `CostModel` + `marketimpact`（平方根模型，K4 已接线） | ✅ |
| 绩效指标 | `RiskMetrics`：波动率 / Beta / Sharpe / Sortino / MaxDrawdown / VaR95 / CVaR95；`TradeMetrics`：胜率 / 笔数 / 平均持有天数 / Calmar | ✅ 风险类齐 |
| 批量与验证 | `batch` / `walkforward` / `job` | ✅ |
| 工程能力 | 因子预热缓存 `cache`、`BacktestState` 断点、`EngineRegistry` + `MarketRouter` 双引擎 fail-closed、`dualrun` K15 双跑对齐、NT 侧车 | ✅ 少见的自研完备度 |
| 订单类型 | market / limit / stop / trailing（`domain/types.go:37-40`） | ✅ |
| 市场摩擦 | 停牌、成交量约束、复权、分红送股均有文件触及 | ⚠️ 见 BE-11 |

---

## 1. 边界与耦合问题

| ID | 等级 | 现象 | 证据 | 初步方向 |
|---|---|---|---|---|
| BE-01 | 🟠 | **反向依赖**：`pkg/kernel`（框架包）import `pkg/backtest`，装配代码住在框架包内 | `pkg/kernel/modules_analysis.go`；非测试 import 实测 | 装配外移到 `cmd/*/wiring`，kernel 只留框架（已记 ADR-D5） |
| BE-02 | 🟠 | **横向耦合**：回测复用实盘撮合组件，改 `live` 会改回测口径 | `pkg/backtest/engine.go`、`execution/live_bridge.go`、`options.go` 三处 import `pkg/live` | 定义执行 port，回测与实盘各自实现；或明确"同构桥"是有意设计并加回归锁 |
| BE-03 | 🟡 | **编排混入能力层**：`job` / `batch` / `walkforward`（应用编排）与 `Engine` / `execution` / `tracker`（引擎内核）同处 `pkg/backtest` | 子包清单：auction batch cache contracts execution job metrics nautilus reporting state tracker walkforward workers | 编排层上移到应用层，能力层只留内核 + 契约 |
| BE-04 | 🟡 | 策略族被回测**直接**依赖，未走 port | 实测 backtest 的 14 个依赖含 `strategy` | 收敛为 `StrategyRuntime` port |
| BE-14 | 🟡 | 双引擎路由按**名字/市场**而非**能力** | `EngineRegistry` + `MarketRouter` 已有 fail-closed，但无能力声明 | 引擎声明能力集（频率/市场/特性），按"需求 ∩ 能力"路由 |
| BE-17 | 🟠 | **NT 内核的适配器寄居在 Go 内核包内**：`pkg/backtest/nautilus` 是「另一个内核」的适配器，却住在 `pkg/backtest` 里 | 目录结构；NT 侧车是独立进程（`nt-sidecar`，HTTP `/v1/backtest`，Python） | nautilus 应与 Go 内核**平级**挂到「引擎能力」下，由注册表按能力选择；共享的是契约不是实现 |

---

## 2. 能力缺口

| ID | 等级 | 缺口 | 证据 | 初步方向 |
|---|---|---|---|---|
| BE-05 | 🔴 | **基准对比与业绩归因缺失**（回测内无实现） | 回测族 grep `benchmark/attribution` 仅 `engine.go:1059` 一行注释；归因实现在 `pkg/data/factor_attribution.go`（回测外） | 新增分析能力层：Benchmark + Brinson/因子归因，纯函数，可独立为能力 |
| BE-06 | 🔴 | **参数敏感性 / 蒙特卡洛 / bootstrap 置信区间全无** | `pkg/backtest` 与 `pkg/statistics` grep 零命中 | 同上，分析层补齐 |
| BE-12 | 🟡 | `RiskMetrics` 只有风险类字段，无**年化/总收益/换手率/信息比率** | `pkg/domain/types.go` RiskMetrics 结构体 | 待核收益字段是否落在 `BacktestResult`（见 BE-11 系列待查） |
| BE-13 | 🟡 | 报表层 `reporting` 在包内（compare / correlation / export），但**分析层薄弱** | 子包 `reporting` | 报表与分析分层：分析出指标，报表只做呈现与导出 |

---

## 3. 未接线零件（实现已就位，缺装配）

来源：`internal/repoguard/package_wiring_test.go` 未接线白名单（该护栏会在包被接线时报 stale）。

| ID | 等级 | 零件 | 白名单原文 | 初步方向 |
|---|---|---|---|---|
| BE-07 | 🟠 | `backtest/auction` 集合竞价撮合 | 「P1-6 集合竞价撮合（9:15-9:25 / 14:57-15:00），**待回测引擎接线**」 | **优先接**：直接影响 A 股开盘/收盘撮合真实性 |
| BE-08 | 🟠 | `live/margin` 融资融券 | 「融资融券账户管理 / 可融券判定，待实盘链路接线」 | 接线后可支持做空/杠杆回测 |
| BE-09 | 🟠 | `strategy/options` 期权（BS / 二叉树） | 「期权策略，待策略层接线」 | 接线后可回测期权 |
| — | — | `sandbox/wasm` | 「P2-27 WASM 沙箱，**待策略插件执行接线**」 | 已登记在架构线（先资产服务，后接线），见 ADR-D6 |

---

## 4. 待查（本轮只做了文件命中统计，未读实现细节）

| ID | 现象 | 需要查什么 |
|---|---|---|
| BE-10 | `Engine` 持有 `httpClient`（`engine.go:75`，构造于 `:330` / `:406`） | 引擎内核出现 HTTP 客户端——确认用途（NT 侧车？数据源？）。若为内核直连外部，应下沉到适配层 |
| BE-11 | 停牌（6 文件）、成交量（6 文件）、复权（5 文件）、分红送股（5 文件）、涨跌停（2 文件）**仅统计到文件命中** | 逐个读实现，判断"有触及"还是"已完备"；特别看涨跌停只有 2 处是否覆盖完整 |
| BE-15 | `BacktestResult` 字段全集未读 | 确认收益类/换手/年化是否已有，避免重复实现 BE-12 |
| BE-16 | `pkg/backtest/workers` 未查 | 并行能力现状（批量回测是否真并行） |

---

## 5. 「不属于回测引擎」的东西（回答：core 里哪些本不该算回测引擎）

### 5.1 回测族**内部**不属于引擎内核的部分

| 子包 | 性质 | 归属建议 |
|---|---|---|
| `job` / `batch` / `walkforward` | **应用编排**（任务、批量、验证流程） | 上移到应用层 |
| `reporting` | **分析呈现**（compare / correlation / export） | 归分析层 |
| `nautilus` | **外部引擎适配器** | 保留在适配层（它本来就是"另一个引擎"的桥） |
| `workers` | 并行/执行基建 | 视 BE-16 结论定 |
| `cache` | 数据供给（因子预热） | 可留引擎侧，也可归数据层 |
| `state` | 引擎状态与断点 | 属引擎 |
| `execution` / `tracker` / `metrics` / `auction` / `contracts` | 内核 | 属引擎 |

### 5.2 core 里与回测**无关**的整块模块（不要混进回测边界）

实盘链路 `live`（部分被回测借用，见 BE-02）· 执行算法 `execalgo` · 数据摄取与同步 `data` / `sync` · 合规 `compliance` · 告警 `alert` · 挖掘 `mining` · 存储 `storage`（共享基础设施，非回测专有）· 因子与表达 `expression` / `indicator`（被回测用但独立）· 风控 `risk`（被回测用但独立能力）· 组合与成本 `portfolio` / `settlement` / `fees`（共享能力）· 行情规范层 `marketdata`。

**判据**：凡「实盘也要用 / 数据服务也要用」的，都不是回测引擎的一部分——它们是**共享能力**，回测只是消费者之一。

---

## 6. 策略加载外化（2026-10-19 补登，详见 `strategy-loading-externalization.md`）

| ID | 等级 | 问题 | 方向 |
|---|---|---|---|
| BE-18 | 🔴 | `.so` 通道在 **Windows 上不可用**：Go `src/plugin/plugin_dlopen.go` 的 build tag 仅 `(linux&&cgo)\|\|(darwin&&cgo)\|\|(freebsd&&cgo)`，其余平台落到 `plugin_stubs.go`，`plugin.Open` 返回 `plugin: not implemented`。本机（Windows）无法验证热加载 | 外化主通道改走 WASM；`.so` 退为"仅 Linux/Docker 的可信同源通道" |
| BE-19 | 🟠 | **无编译链路**：`cmd/analysis/generated_strategies/` 为空目录，全仓唯一 `go build -buildmode=plugin` 出现在 `pkg/strategy/plugins/example_plugin.go:6` 的注释里；agent 仓同样零命中 ⇒ ADR-027 §2.6.1「编译在 ai-service」**未落地** | 建独立编译服务（锁 toolchain），产出内容寻址 artifact（`.wasm` + hash + abi_version） |
| BE-20 | 🟠 | 回测结果**未记录策略 artifact 的 content_hash**（现只有 `PluginInfo.GitHash`，且仅覆盖 `.so`） | 结果落到 `strategy_ref{id, hash}`，否则 K15 双跑对齐失去可复现前提 |
| BE-21 | 🔴 | **策略实例跨 run 共享**：`Registry.Get` 返回注册时的同一实例（`registry.go:51-61`），`SignalStrategy.cursor` 等状态跨 run 存活；并发两笔即互污染 | 注册改**构造函数**，每次 run 取新实例；否则"回测可复现"与 K15 对拍都不成立 |
| BE-22 | 🔴 | **策略参数从未在执行路径生效**：`BacktestRequest` 无 params 字段；`ReloadFromDB`/`ConfigureStrategy` 零生产调用点 ⇒ 内置策略恒用编译期默认值 | 加 `StrategyParams` 进 RunSpec（未知名拒绝）+ 生效参数落运行记录；WASM `initialize(params)` 的前置 |
| BE-23 | 🔴 | **共享 Engine 在 run 内替换引擎级缓存**：`warmFactorCache`（engine.go:783）/ `warmFundamentalsIfNeeded`（:790→`e.fundamentals = records` :1245）在每个 run 开始时替换共享字段，而 HTTP handler 共享同一 Engine。`main.go` 的 `newEngine` 注释已承认"共享单例会让并发窗口互相污染"，但只对 walk-forward 生效 | run 级缓存下沉到 per-run context（或 Engine per-run）；禁止靠加锁串行化掩盖 |
| BE-24 | 🔴 | **信号顺序未规范化**：engine 只排序 `tradingDays`，不排序 signals；`examples/value_momentum.go:366` 直接 `range map` ⇒ 顺序随机；现金/仓位约束下结果不可复现，Go vs WASM 顺序天然不同 | 应用前按 `(Symbol, Direction, Strength, CompositeScore)` **稳定排序**，口径写进 SignalSpec |
| BE-25 | 🔴 | **检查点状态无 schema/artifact 版本**：`State map[string][]byte` 裸存，换 artifact 后恢复旧检查点会按新结构误解释 | state 加信封 `{state_schema_version, artifact_hash, abi_version}`，恢复时校验 fail-closed |
| BE-26 | 🔴 | **L1 OHLCV 缓存跨 run 单调合并、永不清理**：`RunBacktest:775` 每 run warm，`CacheManager.Warm` 是 `mergeBars`，`LoadOHLCVInMemory(nil)` 无生产调用点；注释却写"per-backtest-lifecycle" | 缓存下沉 per-run，或字节预算 + LRU；按条数算容量不成立 |
| BE-27 | 🔴 | **复权锚点 digest 覆盖"合并后全序列"**：`fullBars = cache.Peek`（合并缓存），`AnchorDate = bars[0].Date`、digest 覆盖整条切片 ⇒ 长区间跑过之后，重启再跑短区间会 digest 不匹配 → **run 直接失败** | `fullBars` 先按 `[Start, End]` 切片再算；加"长→短→重启→短"回归 |
| BE-28 | 🔴 | **同 bar 同 symbol 多信号未定义**：两条建仓路径都是 `map[symbol]` 覆盖 = 最后一条生效，顺序未定 | 冻结为 trap（推荐）或显式合并规则，写进 SignalSpec |
| BE-29 | 🔴 | **`Scope.Universe` 逐日变化**（`engine.go:916` 传 `eligibleUniverse(date)` 结果），与 symbol_idx 稳定性冲突 | 新增 run 级 `RunUniverse`；`Universe` 降级为当日可交易名单 |
| BE-30 | 🟠 | **`symbol` scope 无实例并发/内存预算**：设计只写"N/run 分片"，5000 实例 × 64MB 会 OOM | 分片 + 信号量 + 单 run 预算（512 MiB 默认），超预算降并发不失败 |

---

## 7. 商用回测引擎的参考组件构成（对照基线）

```
数据层 ──▶ 数据馈送 DataFeed ──▶ 事件循环 Clock ──▶ 策略 Strategy
（行情/基本面/行业/基准/          （多频率对齐·           （bar/tick 驱动·        （信号·状态递推）
 公司行为·生存者偏差）              前复权·公司行为）        交易日历·warmup）
                                                                  │
                                                                  ▼
风控与约束 ──▶ 组合构建 Portfolio Construction ──▶ 订单管理 OMS ──▶ 经纪商仿真 Broker Sim
（仓位/行业/黑名单/合规）  （目标权重·仓位模型·再平衡）   （类型·有效期·拆单）   （成交价·量约束·滑点·
                                                                                 手续费·借贷·涨跌停）
                                                                  │
                                                                  ▼
                     组合会计 Accounting ──▶ 绩效与分析 Analytics ──▶ 报表 Reporting
                     （现金·持仓·成本基础·T+1） （指标·基准·归因·敏感性·蒙特卡洛） （曲线·交易明细·导出）

横向支撑：可复现（种子·数据版本·策略版本·留痕）· 缓存 · 并行 · 断点续跑 · 多引擎路由
```

对照结论：**你们的引擎在"事件循环 → 策略 → OMS → 经纪商仿真 → 会计 → 指标"这条主链上完整且质量高；缺口集中在"数据层→Feed 的公司行为/生存者偏差（待查）"与末端的"基准/归因/敏感性/蒙特卡洛"。**

---

## 8. 待细化 backlog（回头逐条展开）

- [ ] BE-05 / BE-06：分析层设计（基准数据源怎么来？归因按 Brinson 还是因子？敏感性扫参数网格？）
- [ ] BE-07：auction 接线方案（撮合规则、与现有 ExecutionService 的关系）
- [ ] BE-02：回测/实盘共用执行语义，是"同构桥"还是该拆？（影响回测口径稳定性）
- [ ] BE-03：编排层上移的落点（应用层还是独立 orchestration 模块）
- [ ] BE-11：市场摩擦五项的真实完整度
- [ ] BE-10：Engine 内 httpClient 的用途与去留
- [ ] BE-14：引擎能力声明的字段设计
