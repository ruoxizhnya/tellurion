# QT-51 · 回测/实盘执行语义分离（BE-02 设计）

> 状态：**已裁决并落地**（2026-10-26）｜ 归属：core · 回测引擎域 ｜ 台账：`docs/TASKS.md` QT-51
> 输入：[backtest-engine-review.md](backtest-engine-review.md) BE-02（`:33` / `:145`）· [backtest-extraction.md](backtest-extraction.md) `:46` / `:92`
> 边界：只做**执行语义**边界；通道网关（券商 API / 订单生命周期 / 对账 / 熔断）是 QT-45，不在本单。

---

## 0. 结论（一句话）

**切断 `pkg/backtest → pkg/live` 的实现依赖，保留两者的语义同构。**
引擎只认一个出站契约 `execution.ExecutionPort`；`live.LiveTrader` 的适配层住在**装配层**（`cmd/analysis/wiring`），是唯一同时认识两侧类型的地方。回测主循环的撮合继续走 `execution.ExecutionService` + `tracker`，**不再**被 `pkg/live` 的任何改动影响。

回答 `backtest-engine-review.md:145` 的未决问题：
> 回测/实盘共用执行语义，是「同构桥」还是该拆？

**裁：语义同构、实现分家。** 同构的是撮合口径（滑点/费用/T+1/量约束），由测试锁住；不同构的是代码依赖——回测不持实盘通道。

---

## 1. 实测耦合面（不是泛泛「解耦」）

### 1.1 生产代码（3 个文件，全部在本单切掉）

| 文件 | 用到的 `pkg/live` 符号 | 处置 |
|---|---|---|
| `pkg/backtest/engine.go:25,2266,2293,2334,2343` | `live.LiveTrader`、`live.OrderResult` | 签名换 `execution.ExecutionPort` / `execution.OrderResult` |
| `pkg/backtest/options.go:35,127` | `live.LiveTrader` | `WithLiveTrader(execution.ExecutionPort)` |
| `pkg/backtest/execution/live_bridge.go:37,52,66,79,99,169,175` | `live.LiveTrader`、`live.OrderResult` | 改持 port；结果类型换成 port DTO |

### 1.2 测试代码（3 个文件）

| 文件 | 符号 | 处置 |
|---|---|---|
| `pkg/backtest/fakes_test.go` | `live.LiveTrader` / `OrderResult` / `PositionInfo` / `AccountInfo` / `EmergencyFlattenResult` | **删除**（全仓无测试引用这两个 fake） |
| `pkg/backtest/execution/live_bridge_test.go` | 同上 | 改写为 port 替身 |
| `pkg/backtest/signal_reconcile_e2e_test.go` | `live.NewMockTrader` / `NewPaperSession` / `PaperSessionConfig` / `ReconcileSignals` / `SignalDiscrepancy` | **迁出**到 `e2e/backtest_live/`（K5 信号对账本质是跨域 e2e，本就不该在引擎包里） |

### 1.3 逐符号命中（本单实测，全 `pkg/backtest` 树）

| 符号 | 命中 | 落在哪 |
|---|---|---|
| `live.LiveTrader` | 12 | `engine.go` / `options.go` / `live_bridge.go` + 两个 fake |
| `live.OrderResult` | 13 | 同上 + 两个 fake |
| `live.EmergencyFlattenResult` | 4 | 仅 test fake（为满足接口） |
| `live.ReconcileSignals` | 3 | 仅 K5 对账 e2e |
| `live.PositionInfo` / `live.AccountInfo` | 各 2 | 仅 test fake |
| `live.NewPaperSession` / `PaperSessionConfig` | 各 2 | 仅 K5 对账 e2e |
| `live.NewMockTrader` / `MockTraderConfig` | 各 2 | 仅 K5 对账 e2e |
| `live.SignalDiscrepancy` | 1 | 仅 K5 对账 e2e |
| `live.PaperSession`（含 `PaperSessionConfig`/`NewPaperSession` 前缀） | 3 | 仅 K5 对账 e2e |

> ⚠️ `pkg/backtest` 树里 `grep -i "live.sh"` 的 5 处命中是 **`strings.Contains` 之类的 `strings` 调用**（`LIVE.SH` 大小写不敏感匹配），**不是** `pkg/live` 的符号。真实耦合面比裸 grep 少。

---

## 2. `ExecutionPort` 方法集

定义在 `pkg/backtest/execution/port.go`（与 `LiveBridge` 同包，避免引擎包再长出第二套类型）。

```go
type ExecutionPort interface {
    // 下单：市价 price=0；限价 price=限价。
    SubmitOrder(ctx context.Context, req OrderRequest) (*OrderResult, error)
    // 撤单：已成交/已撤/不存在由实现返回错误。
    CancelOrder(ctx context.Context, orderID string) error
    // 成交回报：按 OrderID 查询当前状态与成交明细。
    GetOrder(ctx context.Context, orderID string) (*OrderResult, error)
    Name() string
    HealthCheck(ctx context.Context) error
}
```

**为什么只有这 5 个**：

- **下单** = `SubmitOrder`；**成交回报** = `SubmitOrder` 返回的 `OrderResult` + `GetOrder`；**撮合**是实现内部语义，不进接口——回测侧撮合在 `ExecutionService`/`tracker`，实盘侧在券商/`MockTrader`，接口只约定「一手请求 → 一手结果」。
- **不进接口**：`GetPositions` / `GetAccount` / `EmergencyFlatten`。引擎运行期一个都不调（实测 `pkg/backtest` 对它们的引用只存在于 test fake）；仓位/账户/紧急平仓是**运维面**，留在 `live.LiveTrader` 与 HTTP handler，塞进引擎出站 port 会把 port 长成第二个 God Object。
- `OrderRequest` / `OrderResult` 是**回测域自有 DTO**，字段覆盖引擎实际消费的全部信息（含 `FillPrice` / `Fee` / `SubmittedAt` / `Message`），不是 `live.OrderResult` 的别名。

---

## 3. 两个实现各住哪

| 侧 | 实现 | 落点 | 是否新增 |
|---|---|---|---|
| **实盘 / paper** | `live.LiveTrader`（`MockTrader` / 真券商） | `pkg/live`（**实现语义不动**，属 QT-45 通道网关域） | 否 |
| ↳ 适配 | `wiring.NewLiveTraderPort` / `AttachLiveTrader`（`live.LiveTrader` → `execution.ExecutionPort`） | `cmd/analysis/wiring/live_trader_port.go` | **是** |
| **回测** | `execution.BacktestExecutionService` + `tracker.Tracker`（撮合/费用/T+1/量约束） | `pkg/backtest/execution` + `pkg/backtest/tracker` | 否（保持现有语义） |

**回测侧为什么不实现这个 port**：`ExecutionPort` 是**出站通道**（把信号发到引擎之外）。回测主循环的成交发生在引擎内部，走 `ExecutionService` + `tracker`，不经过 port。硬造一个 `SimulatedPort` 包装回测撮合，等于在引擎里再开一条平行成交路径——这正是 `backtest-engine-review.md` BE-28 / 双层量约束一直在防的「second truth」。所以回测侧的「实现」是**既有内核语义**，port 为 nil 即纯模拟（行为与今天逐位一致）。

> 若将来做实盘驱动持有引擎（`backtest-extraction.md:92` 的方向），由实盘驱动注入 `ExecutionPort` 即可；回测驱动继续用 nil，不需要第二套 port 实现。

---

## 4. 回测侧适配层放哪

`pkg/backtest/execution/live_bridge.go` 是**唯一**的「信号 → 出站请求」翻译层：

```
domain.Signal + currentPrice
        │  LiveBridge.ExecuteSignal（orderType/price/quantity 口径不变）
        ▼
execution.OrderRequest
        │  ExecutionPort.SubmitOrder
        ▼
execution.OrderResult  ──► 日志 / 调用方
```

`Engine` 只持 `*LiveBridge`；`Engine.SetLiveTrader` / `WithLiveTrader` / `ExecuteSignal*ViaLiveTrader` 全部改成 port 类型（薄 shim 语义不变）。**装配层**负责把 `live.LiveTrader` 包成 port 再注入。

---

## 5. §5-③ 的取舍：是否保留「同构桥」

选项与裁决：

| 选项 | 含义 | 裁决 |
|---|---|---|
| A. 保留同构桥（引擎直接 import live，加回归锁） | 改动最小，但依赖方向仍是「回测 → 实盘」，`pkg/backtest` 永远拖着实盘通道 | ❌ |
| B. 切断依赖 + 保留语义同构（本单） | 引擎只认 port；装配层做适配；同构口径由测试锁住 | ✅ |
| C. 连语义也分家（两套撮合实现各自演进） | 回测与 paper 的滑点/费用口径会漂移（K5 对账失效） | ❌ |

**B 的具体锁**（不是口头承诺）：

1. **结构锁**：`internal/repoguard/backtest_live_isolation_test.go` 扫 `pkg/backtest` 全树（含测试）的 import，出现 `pkg/live` 即红；同时正向断言适配层文件存在且是唯一合法的「双侧都认识」的位置。
2. **行为锁**：`pkg/backtest/qt51_equivalence_test.go` 在同一策略/同一输入下跑两遍——一遍不挂 port，一遍挂一个「任何方法被调用就报错」的 port——回测结果字节摘要必须相同，且 port 调用计数必须为 0。**改 live 不可能改回测结果**，因为回测路径根本不碰它。
3. **同构锁**：K5 的 paper↔backtest 信号对账 e2e 迁到 `e2e/backtest_live/` 后继续跑，口径漂移仍会红。
4. **归档**：改动前后同一策略的 SHA-256 摘要与指标逐项比对，见 [QT51_BACKTEST_EQUIVALENCE_REPORT.md](../../test-cases/QT51_BACKTEST_EQUIVALENCE_REPORT.md)。

---

## 6. 改动清单

| 类型 | 文件 |
|---|---|
| 新增 | `pkg/backtest/execution/port.go`（port + DTO） |
| 新增 | `cmd/analysis/wiring/live_trader_port.go`（实盘适配层 + `AttachLiveTrader`） |
| 新增 | `internal/repoguard/backtest_live_isolation_test.go` |
| 新增 | `pkg/backtest/qt51_equivalence_test.go` |
| 迁移 | `pkg/backtest/signal_reconcile_e2e_test.go` → `e2e/backtest_live/signal_reconcile_test.go` |
| 修改 | `pkg/backtest/{engine.go, options.go, aliases.go, execution/live_bridge.go}` + 对应测试 |
| 修改 | `cmd/analysis/{main.go, wiring/modules_analysis.go}`（改走 `wiring.AttachLiveTrader`） |
| 删除 | `pkg/backtest/fakes_test.go`（无引用的 live fake） |
| 文档 | 本文件、`backtest-engine-review.md` BE-02、`ARCHITECTURE.md`、`docs/TASKS.md`、等价性报告 |

**非目标（逐条）**：不动 `Order`/`Trade` 值对象；不动 T+1 簿记；不引入 MQ；不改 `pkg/live` 的实现语义；不改 `paper_session.go` 的 `generateSignals`（w1 所有权表 §5 已登记 paper 侧单独立单）。

---

## 7. 验收口径

- `pkg/backtest` 全树对 `pkg/live` 的 import = **0**（护栏执法）。
- 同一策略回测结果在改动前后**逐位一致**（归档摘要 + 指标比对）。
- 破坏验证：把适配层改回「引擎直接 import live」⇒ 护栏腿红；还原 ⇒ 绿。
- `gofmt -l .` 空；`go build ./...` / `go vet ./...` / `go test ./... -count=1` 全绿。