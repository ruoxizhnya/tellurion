# QT-04 · 重复信号存量扫描

> 由 `pkg/backtest/qt04_duplicate_signal_scan_test.go`（build tag `qt04scan`）生成的**只读**扫描报告。
> 台账：`docs/TASKS.md` QT-04；裁决表：`docs/design/qt-decoupling/p0-round2-review.md` §8.3。
> 生成时刻：2026-10-22 11:19 UTC。**这份文件的数只在与上文命令同一环境下可复现**（universe / 区间 / 引擎装配都固定；`fill_timing` 显式钉 `next_open`）。

## 结论（§8.3 的分支）

- **分支 = `merge`**。判据是纯函数 `qt04DecideBranch`：只看 status=completed 的组合里
  「有害重复组」（同 bar 同 symbol ≥2 条且 action 可区分）的总数 —— 实测 **4658** 组，跨 42 个组合。
- 存量**有**可区分的同 bar 重复 ⇒ §8.3 步 3：**先合并**，trap 留作下一版开关 ——
  `close` 优先于 `long/short`；同向多条按 strength 求和并 clamp 到 [0,1]；
  合并计数 `duplicate_signals_merged` 进 run 级诊断。
- 直接上 trap 会让下面「有害重复 > 0」那批策略**整场回测当场失败**（判据 B）。
- **能测的策略 12 个 / 名单 14 个**：2 个整场 0 信号。这些是输入缺失
  （见「输入覆盖」），不是策略行为 —— 它们对「零重复」的贡献是 **0 证据**，不是证据。
- **被 map 覆盖掉的条数 = 4658 / 总信号 30902（15.07%）**；其中「可区分」的重复组 4658 个、
  含方向冲突（建仓 vs 平仓，或 long vs short）的 4658 个。
- **同参复跑（复跑腿已执行）：重复组里「最后一条」换人 = 0 组，成员集合本身变了 = 1969 组，
  成员相同但组内顺序变了 = 0 组，跨标的序变（条数相同的交易日）= 95 个。**
  读法：**重复对内部的先后是稳的**（换人 0、变序 0 —— 两条来自同一轮迭代里
  相邻的两个 append），不稳定的是**这一对当天到底存不存在**（变集合 1969 组）。
  所以 A12 里「哪条生效不确定」在这批存量上不是主症状；主症状是「配对本身随持仓状态漂」，
  而持仓由当天的执行顺序影响。
  跨标的序变（条数相同的交易日里顺序不同）= 95：同一天里「谁先下单」的序在漂（A8）。它是上一条的入口 ——
  现金与持仓随执行先后改变，第二天策略要不要发平仓腿就跟着变。
- **一个有害重复组实际下出几笔单（same_close 对照，1756 个组）：两笔以上 884、一笔 872、零笔 0。**
  这条是拿来核对 A12 原文「map 覆盖 ⇒ 最后一条生效」的：覆盖发生在
  `positionSizes[signal.Symbol]`（仓位尺寸），而订单循环 `for _, signal := range validSignals`
  走的是信号切片本身。上一行的分布就是两种说法的判据 —— 见「成交层对照」。
- **这份报告对「有重复的策略」是一次抽样，不是逐位可复现的基准**：同参复跑的信号数
  最大偏离 **36 条**（td_sequential / W1 2024-01-02..2024-07-31（首跑 4466 → 复跑 4430））。表里的绝对数字要和这条一起读。没出现重复的组合不在这里复跑
  （它们的 ±0 由 QT-02 报告的复跑断言覆盖）。

## 按策略汇总（全区间合计）

判据分两列是刻意的：`重复组` 数的是「同 bar 同 symbol 多条」，`有害重复组` 只数其中
action 可区分的（两条完全相同的信号互相覆盖 = 白刷 map，不改变任何订单）。

| 策略 | 交易日 | 信号 | 唯一键 | 重复组 | 有害重复组 | 含方向冲突 | 覆盖条数 | 重复率 | 0 信号 | 复跑换人 |
|---|---|---|---|---|---|---|---|---|---|---|
| bollinger_mr | 425 | 1254 | 1254 | 0 | 0 | 0 | 0 | 0.00% | 否 | — |
| convertible_bond | 425 | 0 | 0 | 0 | 0 | 0 | 0 | 0.00% | 是 | — |
| event_driven | 425 | 383 | 383 | 0 | 0 | 0 | 0 | 0.00% | 否 | — |
| mean_reversion | 425 | 177 | 177 | 0 | 0 | 0 | 0 | 0.00% | 否 | — |
| momentum | 425 | 1727 | 1727 | 0 | 0 | 0 | 0 | 0.00% | 否 | — |
| multi_factor | 425 | 201 | 201 | 0 | 0 | 0 | 0 | 0.00% | 否 | — |
| quality | 425 | 2405 | 2405 | 0 | 0 | 0 | 0 | 0.00% | 否 | — |
| risk_parity | 425 | 5880 | 5880 | 0 | 0 | 0 | 0 | 0.00% | 否 | — |
| sentiment | 425 | 0 | 0 | 0 | 0 | 0 | 0 | 0.00% | 是 | — |
| td_sequential | 425 | 13573 | 8915 | 4658 | 4658 | 4658 | 4658 | 34.32% | 否 | 0 |
| value | 425 | 2576 | 2576 | 0 | 0 | 0 | 0 | 0.00% | 否 | — |
| value_screening | 425 | 57 | 57 | 0 | 0 | 0 | 0 | 0.00% | 否 | — |
| volatility_breakout | 425 | 623 | 623 | 0 | 0 | 0 | 0 | 0.00% | 否 | — |
| volume_price_trend | 425 | 2046 | 2046 | 0 | 0 | 0 | 0 | 0.00% | 否 | — |

## 逐区间

| 策略 | 区间 | 交易日 | 信号 | 重复组 | 有害 | 方向冲突 | 覆盖条数 | 重复率 | 复跑换人 | 复跑变集合 | 复跑变序 | 复跑跨标的序变 | status |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| momentum | W1 2024-01-02..2024-07-31 | 140 | 576 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | completed |
| mean_reversion | W1 2024-01-02..2024-07-31 | 140 | 55 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | completed |
| multi_factor | W1 2024-01-02..2024-07-31 | 140 | 61 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | completed |
| value | W1 2024-01-02..2024-07-31 | 140 | 846 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | completed |
| quality | W1 2024-01-02..2024-07-31 | 140 | 753 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | completed |
| value_screening | W1 2024-01-02..2024-07-31 | 140 | 19 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | completed |
| risk_parity | W1 2024-01-02..2024-07-31 | 140 | 1920 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | completed |
| convertible_bond | W1 2024-01-02..2024-07-31 | 140 | 0 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | invalid |
| event_driven | W1 2024-01-02..2024-07-31 | 140 | 98 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | completed |
| sentiment | W1 2024-01-02..2024-07-31 | 140 | 0 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | invalid |
| td_sequential | W1 2024-01-02..2024-07-31 | 140 | 4466 | 1547 | 1547 | 1547 | 1547 | 34.64% | 0 | 671 | 0 | 32 | completed |
| bollinger_mr | W1 2024-01-02..2024-07-31 | 140 | 462 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | completed |
| volume_price_trend | W1 2024-01-02..2024-07-31 | 140 | 709 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | completed |
| volatility_breakout | W1 2024-01-02..2024-07-31 | 140 | 218 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | completed |
| momentum | W2 2024-08-01..2025-02-28 | 138 | 517 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | completed |
| mean_reversion | W2 2024-08-01..2025-02-28 | 138 | 72 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | completed |
| multi_factor | W2 2024-08-01..2025-02-28 | 138 | 0 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | invalid |
| value | W2 2024-08-01..2025-02-28 | 138 | 865 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | completed |
| quality | W2 2024-08-01..2025-02-28 | 138 | 812 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | completed |
| value_screening | W2 2024-08-01..2025-02-28 | 138 | 0 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | invalid |
| risk_parity | W2 2024-08-01..2025-02-28 | 138 | 1872 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | completed |
| convertible_bond | W2 2024-08-01..2025-02-28 | 138 | 0 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | invalid |
| event_driven | W2 2024-08-01..2025-02-28 | 138 | 162 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | completed |
| sentiment | W2 2024-08-01..2025-02-28 | 138 | 0 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | invalid |
| td_sequential | W2 2024-08-01..2025-02-28 | 138 | 4335 | 1404 | 1404 | 1404 | 1404 | 32.39% | 0 | 646 | 0 | 26 | completed |
| bollinger_mr | W2 2024-08-01..2025-02-28 | 138 | 416 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | completed |
| volume_price_trend | W2 2024-08-01..2025-02-28 | 138 | 650 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | completed |
| volatility_breakout | W2 2024-08-01..2025-02-28 | 138 | 144 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | completed |
| momentum | W3 2025-03-03..2025-09-30 | 147 | 634 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | completed |
| mean_reversion | W3 2025-03-03..2025-09-30 | 147 | 50 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | completed |
| multi_factor | W3 2025-03-03..2025-09-30 | 147 | 140 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | completed |
| value | W3 2025-03-03..2025-09-30 | 147 | 865 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | completed |
| quality | W3 2025-03-03..2025-09-30 | 147 | 840 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | completed |
| value_screening | W3 2025-03-03..2025-09-30 | 147 | 38 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | completed |
| risk_parity | W3 2025-03-03..2025-09-30 | 147 | 2088 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | completed |
| convertible_bond | W3 2025-03-03..2025-09-30 | 147 | 0 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | invalid |
| event_driven | W3 2025-03-03..2025-09-30 | 147 | 123 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | completed |
| sentiment | W3 2025-03-03..2025-09-30 | 147 | 0 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | invalid |
| td_sequential | W3 2025-03-03..2025-09-30 | 147 | 4772 | 1707 | 1707 | 1707 | 1707 | 35.77% | 0 | 652 | 0 | 37 | completed |
| bollinger_mr | W3 2025-03-03..2025-09-30 | 147 | 376 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | completed |
| volume_price_trend | W3 2025-03-03..2025-09-30 | 147 | 687 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | completed |
| volatility_breakout | W3 2025-03-03..2025-09-30 | 147 | 261 | 0 | 0 | 0 | 0 | 0.00% | — | — | — | — | completed |

复跑四列都在同一件事的四个切面上：「换人」= 重复组里**最后一条**（map 覆盖后生效的那条）
的 action 变了；「变集合」= 该 (date,symbol) 的键消失或条数不同；「变序」= 成员完全相同但组内
顺序不同；「跨标的序变」= 信号条数相同、但当天切片里标的出现顺序变了的日子数
（= A8 说的那个未排序的序；条数变了的日子不在这一列，它们已经落在「变集合」里）。
「—」= 该组合没有重复，复跑腿无需触发。

## 重复样例（每条都要能定位到源码）

`#seq` 是当日信号切片里的下标 = `positionSizes[symbol]` 被覆盖的先后，**仓位取最后一条**。
注意这不等于「只有最后一条下单」—— 订单循环走的是切片本身，几笔单见「成交层对照」。

- **td_sequential / W1 2024-01-02..2024-07-31 / 2024-01-25 000001.SZ** —— 同日同标的 2 条，2 种可区分 action：
  - `#6 long`（strength=0.4667 composite=0.4667 order=market limit=8.6769）
  - `#7 close`（strength=0.4667 composite=0.4667 order=market limit=8.6769）
- **td_sequential / W1 2024-01-02..2024-07-31 / 2024-01-26 000001.SZ** —— 同日同标的 2 条，2 种可区分 action：
  - `#12 long`（strength=0.4667 composite=0.4667 order=market limit=8.7865）
  - `#13 close`（strength=0.5333 composite=0.5333 order=market limit=8.7865）
- **td_sequential / W1 2024-01-02..2024-07-31 / 2024-01-26 000012.SZ** —— 同日同标的 2 条，2 种可区分 action：
  - `#6 long`（strength=0.5333 composite=0.5333 order=market limit=5.2289）
  - `#7 close`（strength=0.4667 composite=0.4667 order=market limit=5.2289）
- **td_sequential / W1 2024-01-02..2024-07-31 / 2024-01-29 000001.SZ** —— 同日同标的 2 条，2 种可区分 action：
  - `#12 long`（strength=0.4667 composite=0.4667 order=market limit=8.8596）
  - `#13 close`（strength=1.0000 composite=1.0000 order=market limit=8.8596）
- **td_sequential / W1 2024-01-02..2024-07-31 / 2024-01-29 000006.SZ** —— 同日同标的 2 条，2 种可区分 action：
  - `#3 long`（strength=0.5333 composite=0.5333 order=market limit=4.7300）
  - `#4 close`（strength=0.4667 composite=0.4667 order=market limit=4.7300）
- **td_sequential / W1 2024-01-02..2024-07-31 / 2024-01-29 000009.SZ** —— 同日同标的 2 条，2 种可区分 action：
  - `#5 long`（strength=1.0000 composite=1.0000 order=market limit=11.8431）
  - `#6 close`（strength=0.4667 composite=0.4667 order=market limit=11.8431）
- **td_sequential / W2 2024-08-01..2025-02-28 / 2024-08-30 000011.SZ** —— 同日同标的 2 条，2 种可区分 action：
  - `#5 long`（strength=1.0000 composite=1.0000 order=market limit=7.5897）
  - `#6 close`（strength=0.4667 composite=0.4667 order=market limit=7.5897）
- **td_sequential / W2 2024-08-01..2025-02-28 / 2024-09-02 000006.SZ** —— 同日同标的 2 条，2 种可区分 action：
  - `#19 long`（strength=1.0000 composite=1.0000 order=market limit=3.7900）
  - `#20 close`（strength=0.4667 composite=0.4667 order=market limit=3.7900）
- **td_sequential / W2 2024-08-01..2025-02-28 / 2024-09-02 000011.SZ** —— 同日同标的 2 条，2 种可区分 action：
  - `#21 long`（strength=1.0000 composite=1.0000 order=market limit=7.3803）
  - `#22 close`（strength=0.5333 composite=0.5333 order=market limit=7.3803）
- **td_sequential / W2 2024-08-01..2025-02-28 / 2024-09-02 000025.SZ** —— 同日同标的 2 条，2 种可区分 action：
  - `#25 long`（strength=1.0000 composite=1.0000 order=market limit=13.5578）
  - `#26 close`（strength=0.4667 composite=0.4667 order=market limit=13.5578）
- **td_sequential / W2 2024-08-01..2025-02-28 / 2024-09-02 600000.SH** —— 同日同标的 2 条，2 种可区分 action：
  - `#11 long`（strength=1.0000 composite=1.0000 order=market limit=7.7757）
  - `#12 close`（strength=1.0000 composite=1.0000 order=market limit=7.7757）
- **td_sequential / W2 2024-08-01..2025-02-28 / 2024-09-03 000006.SZ** —— 同日同标的 2 条，2 种可区分 action：
  - `#9 long`（strength=1.0000 composite=1.0000 order=market limit=4.1700）
  - `#10 close`（strength=0.5333 composite=0.5333 order=market limit=4.1700）
- **td_sequential / W3 2025-03-03..2025-09-30 / 2025-03-26 600009.SH** —— 同日同标的 2 条，2 种可区分 action：
  - `#2 long`（strength=0.4667 composite=0.4667 order=market limit=31.4341）
  - `#3 close`（strength=0.4667 composite=0.4667 order=market limit=31.4341）
- **td_sequential / W3 2025-03-03..2025-09-30 / 2025-03-27 600004.SH** —— 同日同标的 2 条，2 种可区分 action：
  - `#5 long`（strength=0.5333 composite=0.5333 order=market limit=8.6566）
  - `#6 close`（strength=0.4667 composite=0.4667 order=market limit=8.6566）
- **td_sequential / W3 2025-03-03..2025-09-30 / 2025-03-27 600009.SH** —— 同日同标的 2 条，2 种可区分 action：
  - `#2 long`（strength=0.4667 composite=0.4667 order=market limit=31.4437）
  - `#3 close`（strength=0.5333 composite=0.5333 order=market limit=31.4437）
- **td_sequential / W3 2025-03-03..2025-09-30 / 2025-03-28 000014.SZ** —— 同日同标的 2 条，2 种可区分 action：
  - `#7 long`（strength=0.5333 composite=0.5333 order=market limit=11.3300）
  - `#8 close`（strength=0.5333 composite=0.5333 order=market limit=11.3300）
- **td_sequential / W3 2025-03-03..2025-09-30 / 2025-03-28 600000.SH** —— 同日同标的 2 条，2 种可区分 action：
  - `#0 long`（strength=0.5333 composite=0.5333 order=market limit=9.6756）
  - `#1 close`（strength=0.4667 composite=0.4667 order=market limit=9.6756）
- **td_sequential / W3 2025-03-03..2025-09-30 / 2025-03-28 600004.SH** —— 同日同标的 2 条，2 种可区分 action：
  - `#15 long`（strength=0.5333 composite=0.5333 order=market limit=8.6281）
  - `#16 close`（strength=0.5333 composite=0.5333 order=market limit=8.6281）

### 发射点（现场从源码定位，不是手写清单）

扫描器按注册名在 `pkg/strategy/plugins/` 里找到策略源文件，列出其中每一条 `Action:` 发射语句。
同一天同一只票被发两条，在「循环每只票走一遍」的写法里至少要两个发射语句 —— 下面的
行号就是「该改哪儿」的位置。**行号会随代码移动**，所以它由扫描器现读，不由人记。

- **td_sequential**（有害重复 4658 组）：
  - `pkg/strategy/plugins/new_strategies.go:104` — `Action:   "buy",`
  - `pkg/strategy/plugins/new_strategies.go:120` — `Action:   "sell",`

## 成交层对照：一个有害重复组实际下了几笔单

口径换成 `same_close`（决策日 == 成交日，(date,symbol) 才能与成交流水精确 join）。
这一腿不为量重复率，只为核对 A12 的原文。覆盖发生在 `positionSizes[signal.Symbol]`（**仓位尺寸**），
而 `engine_daily.go` 的订单循环走的是信号切片本身 ⇒ 「最后一条生效」与「两条都下单、
共用最后一条的仓位」是两种不同的语义，而合并规则要按哪一种写，只能看流水。

| 策略 | 区间 | 有害重复组 | ≥2 笔 | 恰好 1 笔 | 0 笔 |
|---|---|---|---|---|---|
| td_sequential | W1 2024-01-02..2024-07-31 | 1756 | 884 | 872 | 0 |

注意这一行的「有害重复组」与上面主表**不可直接比较**：这里是 `same_close` 口径下的
同一区间（决策日 == 收盘成交日才能逐日 join），而主表钉的是 `next_open`。口径换了 → 持仓路径
换了 → 策略要不要发平仓腿也跟着换。这个差本身就是「重复对随状态漂」的又一次观测。

样例（信号 → 成交，同 (date,symbol)）：

- **td_sequential**：2024-03-18 600000.SH：信号 2 条 [long/st=1.0000/cs=1.0000/market | close/st=1.0000/cs=1.0000/market] → 成交 2 笔 [long×100@6.34 | close×16400@6.33]
- **td_sequential**：2024-04-25 600004.SH：信号 2 条 [long/st=1.0000/cs=1.0000/market | close/st=1.0000/cs=1.0000/market] → 成交 1 笔 [close×100@9.88]
- **td_sequential**：2024-04-25 000021.SZ：信号 2 条 [long/st=1.0000/cs=1.0000/market | close/st=1.0000/cs=1.0000/market] → 成交 2 笔 [long×400@13.15 | close×6300@13.13]
- **td_sequential**：2024-05-10 600020.SH：信号 2 条 [long/st=1.0000/cs=1.0000/market | close/st=1.0000/cs=1.0000/market] → 成交 1 笔 [close×25800@3.45]
- **td_sequential**：2024-02-20 600021.SH：信号 2 条 [long/st=1.0000/cs=1.0000/market | close/st=1.0000/cs=1.0000/market] → 成交 1 笔 [close×700@7.67]
- **td_sequential**：2024-04-22 600006.SH：信号 2 条 [long/st=1.0000/cs=1.0000/market | close/st=1.0000/cs=1.0000/market] → 成交 1 笔 [close×100@7.13]

**同日既建仓又平仓（往返）的组数 = 881**，涉及成交额 77484545、该组内全部成交流水的费用合计 47939.06（费用口径 = 佣金 + 印花税 + 过户费，直接从流水加，不估）。这就是「两条都下单」的现状代价：它不是「哪条生效」的哲学问题，是同一笔头寸一天内进出一趟。

## 重复的方向组合（§8.3 两支规则各自的覆盖面）

§8.3 的合并规则有两支：`close` 优先（处理方向冲突）、同向按 strength 求和并 clamp
（处理同向多条）。哪一支真会触发，按下面的组合数决定 —— 写一条永不触发的规则
进契约就是装饰。

| 策略 | 区间 | 方向组合 | 组数 | 同向多条里 strength 不同 |
|---|---|---|---|---|
| td_sequential | W1 2024-01-02..2024-07-31 | `close+long` | 1547 | 0 |
| td_sequential | W2 2024-08-01..2025-02-28 | `close+long` | 1404 | 0 |
| td_sequential | W3 2025-03-03..2025-09-30 | `close+long` | 1707 | 0 |

全量组合（跨策略跨区间）：`close+long` = 4658 组。同向多条且 strength 不同的组数 = 0。

⇒ 这批存量里 **只有 `close` 优先那一支会被触发**：全部有害重复都是「建仓 + 平仓」同现，
没有「同向两条但强度不同」。求和 clamp 那一支在现网是**未 exercising** 的 —— 仍然要写进契约
（否则新策略一撞就又是歧义），但它的验收得靠合成用例，不能靠这批存量。

> 观测但本单不裁决：样例里 market 单的 `limit_price` 并非 0（域里 `LimitPrice` 的注释写
  `0 for market orders`）。合并规则若哪天要把 LimitPrice 计入「可区分」判据，得先决定这个字段
  在 market 单上到底算什么 —— 那是 QT-21/QT-52 的事，不在本单。

## ⚠️ 非 completed 的组合（这些不是重复率证据）

invalid_reasons 原样抄自回执，不做解释性改写：

- convertible_bond/W1 2024-01-02..2024-07-31 = invalid（invalid_reasons=[zero_trades]）
- sentiment/W1 2024-01-02..2024-07-31 = invalid（invalid_reasons=[zero_trades]）
- multi_factor/W2 2024-08-01..2025-02-28 = invalid（invalid_reasons=[zero_trades]）
- value_screening/W2 2024-08-01..2025-02-28 = invalid（invalid_reasons=[zero_trades]）
- convertible_bond/W2 2024-08-01..2025-02-28 = invalid（invalid_reasons=[zero_trades]）
- sentiment/W2 2024-08-01..2025-02-28 = invalid（invalid_reasons=[zero_trades]）
- convertible_bond/W3 2025-03-03..2025-09-30 = invalid（invalid_reasons=[zero_trades]）
- sentiment/W3 2025-03-03..2025-09-30 = invalid（invalid_reasons=[zero_trades]）

## 输入覆盖（为什么有的策略 0 信号）

本库实测行数（`psql` 直读；扫描器只读，不写任何东西）：

| 表 | 行数 | 说明 |
|---|---|---|
| `ohlcv_daily_qfq` | 3,926,510 | 5,671 只，2023-09-25..2026-09-24 |
| `stock_fundamentals` | **112** | 只有 **2 只票**（000001.SZ / 600000.SH） |
| `fundamentals_detail` | 0 | 深财务纵向无生产者 |
| `news` / `announcements` / `hot_search` / `limit_up_pool` | 0 | 舆情、事件类输入缺失 |
| `ohlcv_minute` | 0 | 日内特征输入缺失 |
| `capital_flow` / `top_list` / `sectors` / `stock_sector_map` | 18 / 256 / 110 / 5,575 | 稀疏 |

⇒ **本次整场 0 信号的策略**（见「按策略汇总」的「0 信号」列）与**跑成 invalid 的组合**
（见上一节的 invalid_reasons）不是「策略没想法」，而是**没有输入**：本库没有可转债表、
`news`/`hot_search`/`limit_up_pool` 0 行、`ohlcv_minute` 0 行、`stock_fundamentals` 只覆盖
2 只票。

反过来也要同一把尺：`value` 在基本面残缺时每天仍发上百条信号 —— 它给 PE 找的是
**价格波动率与趋势的代理**（`pkg/strategy/plugins/value.go` 的 `valueScore := 1.0/(1.0+cv) + …`），
本来就不读基本面表。「信号很多」不等于「输入很足」。

「零重复」与「不可测」必须分开读：前者是证据，后者是空白。§8.3 的分支由上面那条
纯函数按 completed 组合里的有害重复组数决定，不由这里的话题决定。

## 复现

```bash
QT04_PG_DSN='postgres://postgres:postgres@127.0.0.1:5432/quant_trading?sslmode=disable' \
  go test ./pkg/backtest -tags qt04scan -run TestQT04DuplicateSignalScan -count=1 -v
```

- universe（24 只，与 QT-02 同一份，两份报告要能互引）：000001.SZ, 000002.SZ, 000006.SZ, 000009.SZ, 000011.SZ, 000012.SZ, 000014.SZ, 000017.SZ, 000019.SZ, 000020.SZ, 000021.SZ, 000025.SZ, 600000.SH, 600004.SH, 600006.SH, 600008.SH, 600009.SH, 600011.SH, 600015.SH, 600016.SH, 600018.SH, 600019.SH, 600020.SH, 600021.SH
- 区间：W1 2024-01-02..2024-07-31；W2 2024-08-01..2025-02-28；W3 2025-03-03..2025-09-30
- 引擎装配与 QT-02 生成器同形（PG provider + store + 进程内风控 + 内存 qfq 基准），`fill_timing` 显式钉 `next_open`，Seed=42，成本用 `fees.DefaultCommissionRate` + `fees.FixedSlippageRate`。
- 库侧入口 `storage.NewPostgresStoreConnected` —— 连库**不迁移**（本仓硬规则，护栏在 internal/repoguard）。
