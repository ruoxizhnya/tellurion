# QT-52 · 市场摩擦事实核实（BE-11 结案）

> **性质**：只读核实，不改代码。结论以「完备 / 部分 / 缺失」三档给出，每条带证据行号。
> **口径**：`engine_daily.go` 的日线回放路径（生产路径：`RunBacktest` → PG provider → `ohlcv_daily_qfq`）。
> 行号为本次核实时的快照；代码移动后以符号名为准（函数名比行号稳）。
> **对应台账**：`docs/TASKS.md` QT-52；问题登记本 `backtest-engine-review.md` 的 BE-11 已由本文件替换。
> 核实日期：2026-10-22。

---

## 0. 结论总表

| # | 摩擦 | 结论 | 一句话理由 | 方向性影响 |
|---|---|---|---|---|
| 1 | 停牌 | **部分** | 停牌日不成交已做对，但「停牌」与「上游缺行」共用同一判据，且没有停牌日历 | 真停牌：中性；数据缺口被当停牌：低估换手（不会凭空造收益） |
| 2 | 成交量约束 | **部分** | 整日成交量 × 系数截断委托量，但零成交量日不截断、日内分布不可见 | **高估**可成交容量 |
| 3 | 复权 | **完备**（限前复权口径） | qfq 由 `daily + adj_factor` 自算，REV-18 冻结基准 + digest + 漂移 fail-loud | 中性（口径内可复现） |
| 4 | 分红送股 | **部分** | 事件已加载并记录，但 qfq 口径下**刻意不**改现金/股数；5 类公司行为引擎零生产调用方 | 总收益中性；现金账与零股**低估**摩擦 |
| 5 | 涨跌停 | **部分** | 档位（板块/ST/日期分段）结构完整，但新股规则已知错、ST 名称可能缺失 | **高估**（漏判即放行本不可成交的委托） |
| 6 | 集合竞价 | **缺失** | `pkg/backtest/auction` 能力完整，**零生产导入**，仍在未接线白名单 | **高估**开盘/收盘可执行性 |

**优先级建议**（判据：方向性影响为「高估」且可由本仓修复的排前面）：

| 优先级 | 事项 | 归属 |
|---|---|---|
| P1 | 涨跌停的 ST 元数据缺失时不得静默按 10% 放行（应 fail-loud 或取保守档） | 另立单（QT-52 只核实） |
| P1 | 零成交量日（一字板）不做流动性截断 → 全额成交 | 另立单 |
| P2 | `auction` 最小接线：只做「开盘可成交性」三态判定，默认关 | BE-07 → 本文件 §6 给方案 |
| P2 | 新股涨跌停规则（阈值与首日无限制） | 另立单 |
| P3 | 分红现金入账 / 配股 / 增发 / 原始价口径 | 取决于是否需要现金约束型策略 |
| P3 | 日内成交量分布 | 依赖分钟数据（本库 `ohlcv_minute` 0 行） |

---

## 1. 停牌 —— 部分

### 已实现（读实现，不是文件命中）

| 行为 | 证据 |
|---|---|
| 判定：取数窗口的**末根 bar 日期 ≠ 回放日** ⇒ 当日停牌 | `pkg/backtest/engine_daily.go:202-205`（`res.suspended = !lastBarDate.Equal(date.Truncate(24h))`） |
| 停牌股**不进**当日 `marketDataCache`（不产信号、不参与撮合） | `engine_daily.go:129-137` |
| 但保留最后成交价进 `pricesCache`（停牌持仓照旧盯市） | `engine_daily.go:138-140`、`:196-201` 注释 |
| 策略若从自有状态吐出停牌 symbol 的信号 ⇒ 拒单（第二道兜底） | `engine_daily.go:365-375`（批量路径）、`:471-475`（回退路径） |
| 止损/止盈在停牌日**顺延**（不按停牌前旧价平仓） | `engine_daily.go:940-950` |
| `next_open` 口径下停牌股取不到开盘价 ⇒ 拒单，不退回收盘价 | `engine_daily.go:300-307` |
| 停牌跳过计数进 run 级诊断（`suspended_symbol_days` / `suspended_symbols`） | `pkg/backtest/fill_diag.go:49-59`、`:89-103` |

### 缺口

1. **「停牌」与「上游缺行」不可区分**。判据只是「末根 bar 不是今天」，两者共用。真停牌时这是对的；上游漏同步时同样被静默当成停牌 —— 结果是**少交易且不报警**（诊断里只记 `suspended`，不记「是数据缺口」）。
2. **没有独立停牌日历**。`domain.Stock.Status` 声明了 `"suspended"` 取值（`pkg/domain/market/types.go:51`），但全仓**没有任何生产者**写入它（grep 零命中）。数据层已知停牌日会「有 adj_factor、没有 daily 行」（`pkg/data/tushare.go:337-338`），但没人把它物化成一张日历表。
3. 因此做不了「复牌首日」「连续停牌 N 日」「停牌期间不调仓」这类规则。

### 方向性影响

- 真停牌处理是**正确方向**：REV-09 之前按停牌前旧价成交会凭空造收益，现在不会 —— 这一半已收口。
- 剩余风险只有一个方向：**数据缺口被当成停牌** ⇒ 少交易。它不是「高估收益」，而是「悄悄少做」；对策略比较同样有害（同一策略在不同数据完整度下结果不可比）。

---

## 2. 成交量约束 —— 部分

### 已实现

| 行为 | 证据 |
|---|---|
| 下单前按 `当日 Volume × liquidityFactor` 截断委托量，记 run 级诊断 | `pkg/backtest/engine_daily.go:653-667`（`recordTruncation`） |
| 系数默认 0.1、>1 clamp 到 1、≤0 回落默认 | `pkg/backtest/fill_diag.go:110-119`、`engine.go:36-39` |
| Tracker 内部另有一道同样的上限（`dayBar != nil` 时才生效） | `pkg/backtest/tracker/tracker.go:346-358`、默认值 `:92` |
| 当日成交量同时作为**冲击成本**的 ADV 代理（平方根模型） | `pkg/backtest/execution/execution.go:131-137`、`engine_daily.go:825-835` |
| 截断后当日买不满的部分，次日按「目标持仓 − 实际持仓」自然续上 | `engine_daily.go:647-652` 注释（这是 partial 语义的来源） |

### 缺口

1. **零成交量日不截断**。条件是 `execOpts.DayBar.Volume > 0`（`:653`）。一字板（Volume=0）当天，允许成交的方向（买跌停、卖涨停）会**全额成交**。真实市场里那天根本没有对手量。
2. **只有「整日总容量」，没有日内分布**。exec-algo 拆单（TWAP/VWAP）的每一片都只能拿整日量做上限，无法表达「10:00 前只有 X 手」。这依赖分钟数据，而本库 `ohlcv_minute` 为 0 行。
3. 流动性系数是**常数**（0.1 或配置值），不随市值/换手率变化；小盘股的 10% 与大盘股的 10% 不是一个人性化的约束。

### 方向性影响

**高估**：可成交容量偏大，尤其是一字板与流动性差的小票。方向单一的偏差（不会低估），因此对「策略容量」类结论要打折读。

---

## 3. 复权 —— 完备（限前复权口径）

### 已实现

| 行为 | 证据 |
|---|---|
| qfq = `daily`（不复权）× 当日 `adj_factor` ÷ 窗口内最新因子，自算而非买 `stk_factor_pro` | `pkg/data/tushare.go:322-372`（口径注释在 `:330-338`） |
| 停牌日 `adj_factor` 会补齐而 `daily` 没有行 ⇒ 以 daily 为准逐日取因子，取不到跳过（不补 1） | `pkg/data/tushare.go:337-338`、`:374-379` |
| REV-18 冻结区间基准：anchor = 区间首根 bar，ratio = anchorClose / 当日锚点收盘 | `pkg/backtest/adjustment_snapshot.go:114-187` |
| 基准不可被后来者改写（首写者胜），序列 digest 校验，漂移 fail-loud | 同上 `:236-254`、`:189-210`（`ErrAdjustmentDrift` / `ErrAdjustmentAnchorMissing`） |
| 日环内只做「乘常数 + 量化到 1e-6」，与全区间归一化逐位一致 | `adjustment_snapshot.go:212-231`、`:256-288` |
| 引擎**不做**第二遍现金/股数改账（qfq 已含），避免双重计算 | `engine_daily.go:988-1001`（REV-06 裁决） |

### 边界（不是缺口，但必须一起读）

1. **只有前复权**：回测不接受后复权或不复权序列。需要原始价口径的策略（如按公告价做事件研究）今天做不了。
2. **跨区间不可比**：qfq 的基准是「本区间首根 bar」，不同 `StartDate` 的两次 run 价格刻度不同；同区间重跑逐位一致（REV-18 保证的就是这件事）。
3. 分红/拆股在 qfq 下不单独入账（见第 4 节），因此「现金余额」不是真实券商现金。

### 方向性影响

口径内中性：价格序列可复现、无双重计算。边界之外（原始价、现金账）才有偏差。

---

## 4. 分红送股 —— 部分

### 已实现

| 行为 | 证据 |
|---|---|
| 区间内分红/拆股事件一次加载，按日期索引 | `pkg/backtest/engine_daily.go:35-66` |
| 除权日只**记录**事件（有持仓才记），并取消该 symbol 的活跃 exec-algo 父单 | `engine_daily.go:1002-1040` |
| 原始价口径的改账语义存在：`ProcessDividend` 贷现金、`ProcessSplit` 加股数 | `pkg/backtest/tracker/tracker.go:822-880` |
| 5 类公司行为 + 执行顺序（Split→Bonus→Rights→Cash→Placement）已实现 | `pkg/domain/corporate_action.go:399-578` |

### 缺口

1. **`ActionEngine` 零生产调用方**（实测：`pkg/`、`cmd/` 内除定义文件外零命中）。能力在、测试绿、但引擎不走它。
2. **qfq 口径下现金分红不入现金账、送转不加股数**（REV-06 的刻意选择）。这是**对的**（否则双重计算），但代价是：任何依赖「现金余额」的策略/约束（如「现金不足不能加仓」「分红再投资」）在回测里看到的现金都是不含分红的。
3. **配股 / 增发 / 分红税**完全没有进入回测路径。
4. 送股产生的**零股**在 qfq 口径下不存在（股数不变），因此整手取整逻辑从未被这类事件压测过。

### 方向性影响

- 总收益：**中性**（qfq 已把除权跳空抹平，收益通过价格体现）。
- 现金账 / 仓位整数性：**低估**摩擦（零股、配股缴款、红利税都不会发生）。

---

## 5. 涨跌停 —— 部分

### 已实现

| 行为 | 证据 |
|---|---|
| 档位解析：主板 10% / 创业板·科创板 20% / 北交所 30% / ETF·LOF·债 10% | `pkg/marketdata/board.go:75-110` |
| 301（注册制创业板）、689（CDR）已补；ST 判定改为前缀匹配（`*ST`/`SST`/`S*ST`） | `board.go:144-230`、`pkg/backtest/pricelimit.go:157-180` |
| ST 档位**按板块**：主板 5%→10%（2026-07-06 起），创业/科创 ST 保持 20%，北交所 30% | `pricelimit.go:39-46`、`:118-144` |
| 限价价按 0.01 取整再比较（避免临界日误判） | `pricelimit.go:182-203`、`engine_daily.go:241-245` |
| 判定后写回 bar（`LimitUp/LimitDown`）并把基准价换成整分位限价 | `engine_daily.go:245-263` |
| 拦截：涨停不买（long/short）、跌停不卖（close）；**允许**买跌停、卖涨停 | `engine_daily.go:393-412`（批量）、`:476-485`（回退） |
| `next_open` 下拦截读的是**成交日**的 bar，不是决策日 | `engine.go:1002-1004` 注释与实现 |

### 缺口

1. **新股规则已知错误，且是刻意保留的**：`TradeDays` 由「自然日 / 7 × 5」估算（不扣节假日），阈值用配置 `NewStockDays`（默认 60）而非「上市前 5 个交易日无限制」，且 `New` 档位本身也不是「无限制」。见 `pricelimit.go:66-73`、`:108-113` 的自陈。
2. **ST 名称可能缺失**：`IsRiskWarningName` 依赖 `stocks.name`；名称取不到时 `stockNamesFor` 给空串，判定为「非 ST」⇒ 按 10% 而非 5% 处理。对一个涨 6% 的 ST 股，引擎会认为「没到涨停」并放行成交 —— 而在现实里它已经封板。`engine_daily.go:150-158`（空名称保留）+ `pricelimit.go:118-144`。
3. **只在日线级判定**：集合竞价开盘即封板（一字板）无法与「盘中触及」区分；`pkg/live/price_cage.go` 是**实盘申报笼子**（±2%，且未接线），不是回测撮合规则。
4. 债券/ETF 用统一 10%，未按品种细分（可接受，但要知道）。

### 专答：「涨跌停只有 2 处」是否覆盖完整？

**「2 处」是文件命中统计的产物，不是实现规模。** 实际分三层：

| 层 | 位置 | 内容 |
|---|---|---|
| 档位 | `pkg/marketdata/board.go:75-110` + `pkg/backtest/pricelimit.go:108-155` | 板块 × ST × 日期分段 × 新股 |
| 判定 | `engine_daily.go:207-263` | 取整、比较、写标志、替换基准价 |
| 拦截 | `engine_daily.go:393-412`、`:476-485` | 两套信号路径各 4 个条件 |

结论：**结构覆盖完整**（四个维度都在），**不完整的是参数与元数据** —— 新股阈值/档位（P2）与 ST 名称可得性（P1）。这两处都不会报错，只会安静地放行。

### 方向性影响

**高估**：漏判涨跌停 ⇒ 本不可成交的委托成交。ST 名称缺失是最现实的一条（数据面只有部分票有名称时，偏差集中在这些票上）。

---

## 6. 集合竞价 —— 缺失

### 现状

| 事实 | 证据 |
|---|---|
| 能力完整：时段机（9:15-9:25 / 14:57-15:00）+ 最大成交量撮合 + 平局按 anchor 取近 + 比例分配 | `pkg/backtest/auction/auction.go:44-110`、`:136-412` |
| **零生产导入**：`pkg/`、`cmd/` 内除自身包外无 `backtest/auction` 引用 | 实测 grep；白名单 `internal/repoguard/package_wiring_test.go:75` |
| 引擎的成交价直接取日线 `Open`/`Close`（含 QT-02 的 `next_open`） | `engine_daily.go:278-310`、`:825-835` |

### 接线方案（与 `ExecutionService` 的关系）

**定位**：auction 是**可成交性/价格发现器**，不是第二个 `ExecutionService`。
- `ExecutionService` 的契约是「一笔 order + 一根 quote → 一笔 trade」；
- auction 的输入是「一侧或多侧委托簿」，输出是理论清算价与分配结果。

两者不是替代关系，接点是**取价之前**：先判定「开盘集合竞价能不能成交、能成交多少」，再交给现有执行路径定价与计费。

**最小接线（建议另立单，默认关）**：

1. 触发条件收窄到两种：① 成交口径 = `next_open` 且成交日 `open` 已贴涨/跌停价；② 策略显式要求「开盘竞价成交」。
2. 用 `CallAuctionMatcher` 只做**三态判定**，不做价格发现（本库无逐档 LOB，价格仍取日线 open）：
   - `filled_at_open`：有对手量，按开盘价成交；
   - `partial_at_open`：对手量 = `bar.Volume × liquidityFactor` 中属于该方向的部分，按比例成交；
   - `no_fill`：一字板/无对手量 ⇒ 拒单（进 `fill_diag`，不静默）。
3. 三态计数进 `fillRunDiag`，与 `suspended`/`truncation` 同级，运行末尾汇总。
4. **不改** `ExecutionService` 接口，也不改现有基准（默认关 = 行为零变化）。

**明确不做**：日内 tick 级竞价复现（无数据）、把收盘竞价从日线里切出来（14:57-15:00 在日线上不可分）、用 auction 反转录「历史开盘价」。

### 方向性影响

未接线时：开盘价被当成人人可成交的价格 ⇒ **高估**可执行性，尤其一字板与大幅跳空。接线后只影响「开盘成交」这一条腿，不改收盘成交。

---

## 7. 复现方法

```bash
# 六类的实现面（只读）
grep -rn "suspended" pkg/backtest/engine_daily.go pkg/backtest/fill_diag.go
grep -rn "liquidityFactor\|Volume \* e.liquidityFactor" pkg/backtest/
grep -rn "adj_factor\|SeriesDigest\|ErrAdjustmentDrift" pkg/data/tushare.go pkg/backtest/adjustment_snapshot.go
grep -rn "processCorporateActions\|ProcessDividend\|ProcessSplit" pkg/backtest/ pkg/domain/
grep -rn "resolvePriceLimit\|LimitPrices\|LimitUp" pkg/backtest/ pkg/marketdata/
grep -rn "backtest/auction" pkg/ cmd/ internal/repoguard/

# 「ActionEngine 零生产调用方」与「auction 零导入」两条事实的复核
grep -rn "ActionEngine" pkg/ cmd/ --include=*.go | grep -v "_test.go"
grep -rn "backtest/auction" pkg/ cmd/ --include=*.go | grep -v "_test.go"
```

---

## 8. 与 BE-11 的关系

本文件就是 `backtest-engine-review.md` BE-11「市场摩擦五项的真实完整度」的结案证据：该行已由「仅统计到文件命中」替换为结论指针。本单**不改任何代码**；第 0 节的 P1/P2 项按台账另立单。