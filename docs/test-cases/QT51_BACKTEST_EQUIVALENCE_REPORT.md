# QT-51 回测结果等价性报告（BE-02）

> 日期：2026-10-26 ｜ 分支：`task/qt51-execution-port`（主工作区）｜ 原始基线：`main@170f09a` ｜ 复取基线：`origin/main@5b9dc11`
> 目的：证明「切断 `pkg/backtest → pkg/live`」**没有改变回测口径**——同一策略、同一输入，
> 改动前后结果逐位一致。不是「测试跑绿了就算」。

---

## 1. 口径

| 项 | 取值 |
|---|---|
| 策略 | `examples.NewMomentumStrategy()`，`lookback_days=20` / `top_n=5` / `max_positions=5` / `rebalance_frequency=daily` |
| 数据 | 合成夹具：20 symbols × 120 交易日（`synthOHLCV`，`rand.NewSource(42)`） |
| 引擎 | `NewEngine` + 同款 risk manager，`parallelWorkers=1`，`seed=42` |
| 请求 | `2024-01-02 ~ 2024-04-30`，初始资金 100 万，risk-free 3% |
| 摘要 | SHA-256，覆盖：8 个标量指标（float64 取 IEEE-754 位模式）+ 逐点净值曲线 + 逐笔成交（symbol/direction/qty/price/commission/timestamp；**排除**随机成交 ID） |

**为什么要位模式**：十进制格式化会把 1 ulp 的漂移藏起来；`math.Float64bits` 直接比字节。

---

## 2. 采集方式

- **改动前**：在基线代码（`main@170f09a`，三处 import `pkg/live` 仍在）上跑一个临时探针
  （`pkg/backtest/qt51_baseline_test.go`，验证后删除），打印 `QT51_DIGEST=<sha256>`。
- **改动后**：同一摘要算法沉淀为常驻行为锁
  `pkg/backtest/qt51_equivalence_test.go`（跑两遍：不挂 port / 挂「一调用就报错」的
  哨兵 port），并断言两次摘要相同、port 调用数为 0。

两边的输入构造、策略参数、摘要字段逐字一致。

---

## 3. 结果

| 运行 | `QT51_DIGEST` | trades | portfolio points | port calls |
|---|---|---|---|---|
| 改动前（基线 `170f09a`，第 1 次） | `60a2713b4ccdb32ff5d30d50a9863d47152cf41d1979901b324c3d3dc54b2bd1` | 70 | 120 | — |
| 改动前（基线 `170f09a`，第 2 次） | `60a2713b4ccdb32ff5d30d50a9863d47152cf41d1979901b324c3d3dc54b2bd1` | 70 | 120 | — |
| 改动后（不挂 port） | `60a2713b4ccdb32ff5d30d50a9863d47152cf41d1979901b324c3d3dc54b2bd1` | 70 | 120 | 0 |
| 改动后（挂哨兵 port） | `60a2713b4ccdb32ff5d30d50a9863d47152cf41d1979901b324c3d3dc54b2bd1` | 70 | 120 | 0 |

**结论：四次运行同一摘要，逐位一致。** 挂不挂实盘通道对回测结果没有任何影响——
因为回测主循环根本不调用出站 port（`port_calls=0` 是行为锁的一部分）。

### 真实输出（改动前）

```
$ go test ./pkg/backtest -run TestQT51BaselineDigest -count=2 -v
    qt51_baseline_test.go:88: QT51_DIGEST=60a2713b4ccdb32ff5d30d50a9863d47152cf41d1979901b324c3d3dc54b2bd1 trades=70 pv=120
    qt51_baseline_test.go:88: QT51_DIGEST=60a2713b4ccdb32ff5d30d50a9863d47152cf41d1979901b324c3d3dc54b2bd1 trades=70 pv=120
ok    tellurion/pkg/backtest
```

### 真实输出（改动后）

```
$ go test ./pkg/backtest -run TestQT51_BacktestResultIndependentOfExecutionPort -count=2 -v
    qt51_equivalence_test.go:145: QT51_DIGEST=60a2713b4ccdb32ff5d30d50a9863d47152cf41d1979901b324c3d3dc54b2bd1 trades=70 pv=120 port_calls=0
--- PASS: TestQT51_BacktestResultIndependentOfExecutionPort (0.43s)
    qt51_equivalence_test.go:145: QT51_DIGEST=60a2713b4ccdb32ff5d30d50a9863d47152cf41d1979901b324c3d3dc54b2bd1 trades=70 pv=120 port_calls=0
--- PASS: TestQT51_BacktestResultIndependentOfExecutionPort (0.23s)
ok    tellurion/pkg/backtest
```

### 复取（rebase 到 `origin/main@5b9dc11` 后，2026-10-26）

```
$ go test ./pkg/backtest -run TestQT51_BacktestResultIndependentOfExecutionPort -count=2 -v
=== RUN   TestQT51_BacktestResultIndependentOfExecutionPort
    qt51_equivalence_test.go:145: QT51_DIGEST=60a2713b4ccdb32ff5d30d50a9863d47152cf41d1979901b324c3d3dc54b2bd1 trades=70 pv=120 port_calls=0
--- PASS: TestQT51_BacktestResultIndependentOfExecutionPort (0.09s)
=== RUN   TestQT51_BacktestResultIndependentOfExecutionPort
    qt51_equivalence_test.go:145: QT51_DIGEST=60a2713b4ccdb32ff5d30d50a9863d47152cf41d1979901b324c3d3dc54b2bd1 trades=70 pv=120 port_calls=0
--- PASS: TestQT51_BacktestResultIndependentOfExecutionPort (0.05s)
PASS
ok    tellurion/pkg/backtest  0.216s
```

摘要与旧基线的四次运行**逐字相同** ⇒ rebase 没有改变回测口径。

---

## 4. 结构护栏的破坏验证（真实输出）

**改坏**（按派发原文直接改生产文件，不是临时文件）：在
`pkg/backtest/execution/live_bridge.go` 的 import 块加一行 `_ "tellurion/pkg/live"`
（原文件 SHA-256 = `9ED2F927EE074FC7BC0927ED375EB100E87BF3248729D8FD4DD5A347000BDEE2`）：

```
$ go test ./internal/repoguard -run TestBacktestPackage_CannotImportLive -count=1
--- FAIL: TestBacktestPackage_CannotImportLive (0.01s)
    backtest_live_isolation_test.go:69:
          Error:        Should be empty, but was [pkg/backtest/execution/live_bridge.go -> tellurion/pkg/live]
          Messages:     QT-51（BE-02）：pkg/backtest 全树不许 import pkg/live —— ...
FAIL
FAIL  tellurion/internal/repoguard  0.057s
MUT_EXIT=1
```

**逐字节还原**（从备份复制回，SHA-256 前后相同）：

```
NOW=9ED2F927EE074FC7BC0927ED375EB100E87BF3248729D8FD4DD5A347000BDEE2
BAK=9ED2F927EE074FC7BC0927ED375EB100E87BF3248729D8FD4DD5A347000BDEE2
BYTE_IDENTICAL=True
ok    tellurion/internal/repoguard  0.060s
RESTORED_EXIT=0
```

---

## 5. 边界（这份报告证明了什么 / 没证明什么）

**证明**：

- 改动前后同一策略回测结果逐位一致（摘要相同，含 70 笔成交与 120 点净值）。
- 挂载执行 port 不改变回测结果，且回测路径不调用 port。
- `pkg/backtest` 全树对 `pkg/live` 的 import 为 0（护栏红/绿双向验证）。

**没证明**（不在本单验收内）：

- paper 侧的成交语义（T+1/费用/滑点）与回测逐笔一致——本单只保**信号**对账
  （`e2e/backtest_live/` 继续跑 K5 对账），成交对账是另一个口径。
- `live.LiveTrader` 自身行为未改动（属 QT-45 通道网关域），因此无需（也不应）
  在本单做"改 live"实验；结构护栏已经让"回测受 live 改动影响"在编译期不可能发生。