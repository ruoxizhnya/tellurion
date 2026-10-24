package backtest

// REV-09：回测成交真实性的 run 级诊断。
//
// 两类拦截都是**正确行为**（不是缺陷），所以不进 invalid_reasons——把它们
// 塞进「无效运行」会把一次正常的、含停牌股的长期回测判成 invalid。但计数
// 必须可见，模式照抄 execAlgoRunDiag（per-run 累加器 + 末尾 logSummary +
// nil-safe）：日志汇总给运维，单测直接断言内部字段给 CI。
//
// 两类计数：
//   - 停牌跳过：末根 bar 日期 ≠ 当日 ⇒ 该 symbol 当日不产信号、不参与撮合、
//     不触发止盈止损（旧行为是按停牌前旧价静默成交）；
//   - 流动性截断：下单量被「当日成交量 × liquidityFactor」截断（partial
//     语义）——旧行为是超出当日可容纳量的委托全额成交。

import (
	"fmt"

	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-contracts/domain"
)

// defaultLiquidityFactor 是未配置时的流动性系数：当日成交量的 10%。
// 与 tracker 内 liquidityFactor 的默认值同源（REV-09 ④：不动
// ExecuteTrade 的既有 dayBar 路径，但默认值必须一致，避免两条路径两个默认）。
const defaultLiquidityFactor = 0.1

// fillRunDiag 是一次回测内「成交真实性拦截」的累加器。
type fillRunDiag struct {
	suspendedDays  map[string]int // symbol -> 被判定停牌的天数
	suspendedTotal int            // 停牌跳过的 symbol·日 总数
	truncations    int            // 流动性截断次数
	truncatedQty   float64        // 累计被截掉的股数
	truncSample    string         // 首个截断现场（诊断点名）

	// QT-02 · next_open 专属两类计数（same_close 恒为 0）：
	//   - noOpenPriceSkips：成交日拿不到可用开盘价（脏 bar / 缺字段）而被拒的
	//     委托数。拒而不是退回收盘价成交 —— 同一份留痕里混两种口径比少一笔
	//     成交更坏。
	//   - pendingAtRunEnd：区间最后一天收盘决策、没有次日可成交的信号数。
	noOpenPriceSkips int
	pendingAtRunEnd  int

	// QT-21：同 bar 同 symbol 的重复信号被合并掉的条数。
	// 合并本身是正确行为（QT-04 结论），但要可见 —— 「今天少了两条意图」
	// 不能只存在于策略作者的脑子里。
	duplicateSignalsMerged int

	// QT-26：emit_target2 的目标权重 sizing 诊断。
	//   - sizingByTarget：走目标权重路径的信号数；
	//   - sizingClamped：目标手数被 RiskPolicy cap 截断的次数（§8.2）。
	sizingByTarget int
	sizingClamped  int
}

func newFillRunDiag() *fillRunDiag {
	return &fillRunDiag{suspendedDays: make(map[string]int)}
}

// recordSuspended 记一次「symbol 在某日因停牌被跳过」。
func (d *fillRunDiag) recordSuspended(symbol string) {
	if d == nil { // nil-safe：与 execAlgoRunDiag 同款（零值可用的诊断器）
		return
	}
	if d.suspendedDays == nil {
		d.suspendedDays = make(map[string]int)
	}
	d.suspendedDays[symbol]++
	d.suspendedTotal++
}

// recordTruncation 记一次流动性截断（requested > filled）。
func (d *fillRunDiag) recordTruncation(symbol string, requested, filled float64) {
	if d == nil {
		return
	}
	d.truncations++
	d.truncatedQty += requested - filled
	if d.truncSample == "" {
		d.truncSample = fmt.Sprintf("%s requested=%.0f filled=%.0f", symbol, requested, filled)
	}
}

// recordNoOpenPrice 记一次「next_open 口径下成交日没有可用开盘价 ⇒ 该委托被拒」。
func (d *fillRunDiag) recordNoOpenPrice() {
	if d == nil {
		return
	}
	d.noOpenPriceSkips++
}

// recordPendingAtRunEnd 记一次「区间结束时仍未成交的 next_open 信号」。
func (d *fillRunDiag) recordPendingAtRunEnd(pending []domain.Signal) {
	if d == nil {
		return
	}
	d.pendingAtRunEnd += len(pending)
}

// recordDuplicateSignalsMerged 记 n 条重复信号被合并（QT-21）。
func (d *fillRunDiag) recordDuplicateSignalsMerged(n int) {
	if d == nil || n <= 0 {
		return
	}
	d.duplicateSignalsMerged += n
}

// recordTargetSizing 记一次 emit_target2 的 sizing；clamped=true 表示
// 目标手数被 policy cap 截断（QT-26）。
func (d *fillRunDiag) recordTargetSizing(clamped bool) {
	if d == nil {
		return
	}
	d.sizingByTarget++
	if clamped {
		d.sizingClamped++
	}
}

// logSummary 在回测收尾时打一行汇总（无拦截则不打扰日志）。
func (d *fillRunDiag) logSummary(logger zerolog.Logger) {
	if d == nil || (d.suspendedTotal == 0 && d.truncations == 0 && d.noOpenPriceSkips == 0 &&
		d.pendingAtRunEnd == 0 && d.duplicateSignalsMerged == 0 && d.sizingByTarget == 0) {
		return
	}
	logger.Info().
		Int("suspended_symbol_days", d.suspendedTotal).
		Int("suspended_symbols", len(d.suspendedDays)).
		Int("liquidity_truncations", d.truncations).
		Float64("truncated_qty_total", d.truncatedQty).
		Int("next_open_no_fill_price", d.noOpenPriceSkips).
		Int("next_open_unfilled_at_run_end", d.pendingAtRunEnd).
		Int("duplicate_signals_merged", d.duplicateSignalsMerged).
		Int("sizing_source_emit_target2", d.sizingByTarget).
		Int("sizing_clamped_total", d.sizingClamped).
		Str("first_truncation", d.truncSample).
		Msg("Fill realism (REV-09 / QT-02 / QT-21 / QT-26): suspended symbol-days skipped, orders capped by daily volume × liquidity_factor, next_open rejects recorded, duplicate signals merged, emit_target2 sizing clamped by policy")
}

// liquidityFactor 返回配置的流动性系数：
//   - 零值 / 负数 → defaultLiquidityFactor（0.1，保持既有默认行为）；
//   - > 1 → clamp 到 1（允许超过当日全部成交量的配置是无意义的，
//     但不在启动期 fail——配置错误不该让回测起不来）；
//   - (0,1] 原样使用。
func (e *Engine) liquidityFactor() float64 {
	f := e.config.LiquidityFactor
	if f <= 0 {
		return defaultLiquidityFactor
	}
	if f > 1 {
		return 1
	}
	return f
}
