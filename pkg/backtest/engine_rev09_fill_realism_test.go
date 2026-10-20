package backtest

// REV-09 护栏测试：回测成交真实性——停牌日不再按旧价静默成交；超出当日
// 成交量可容纳度的委托被截断（partial 语义），两类拦截都进 run 级诊断。
//
// 旧行为（缺陷）：
//   - processStockJob 取 ohlcvData[len-1] 当「今日 bar」，从不校验日期——
//     停牌日该 bar 是停牌前旧 bar：信号在旧数据上产生、按旧 close 成交、
//     限价单用旧区间判触及；
//   - 流动性上限只在 Tracker.ExecuteTrade 且 dayBar != nil 时生效，生产路径
//     （ExecutionService → ApplyTrade）与 ClosePosition/止损全部绕过它——
//     想买 100 万股、当日只成交 10% 也全额成交。
//
// 新口径：末根 bar 日期 ≠ 回测日 ⇒ 该 symbol 当日摘出 marketDataCache
// （不产信号、不参与撮合、不触发止盈止损），价格仍进 pricesCache 供估值；
// 下单量超过「当日成交量 × liquidity_factor」⇒ 截断并记 partial 到 fillRunDiag。

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"tellurion/pkg/backtest/contracts"
	"tellurion/pkg/backtest/tracker"
	"tellurion/pkg/domain"
	"tellurion/pkg/marketdata"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	rev09Day1 = time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	rev09Day2 = time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
)

func rev09Bar(symbol, date string, volume float64) domain.OHLCV {
	return domain.OHLCV{
		Symbol: symbol,
		Date:   mustDate(date),
		Open:   10, High: 10.2, Low: 9.8, Close: 10,
		Volume: volume, Turnover: 10 * volume,
	}
}

func mustDate(s string) time.Time {
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return d
}

func rev09NewEngine(t *testing.T, prov marketdata.Provider) *Engine {
	t.Helper()
	v := viper.New()
	v.Set("backtest.initial_capital", 1_000_000.0)
	v.Set("backtest.commission_rate", 0.0003)
	v.Set("backtest.slippage_rate", 0.0)
	eng, err := NewEngine(v, prov, zerolog.Nop())
	require.NoError(t, err)
	return eng
}

// TestProcessStockJob_FlagsSuspendedOnStaleLastBar：末根 bar 是前一日 ⇒
// 当日停牌；末根 bar 正是当日 ⇒ 正常。
func TestProcessStockJob_FlagsSuspendedOnStaleLastBar(t *testing.T) {
	prov := marketdata.NewInMemoryProvider()
	prov.LoadOHLCV("600000.SH", []domain.OHLCV{
		rev09Bar("600000.SH", "2024-01-02", 1_000_000),
	})
	eng := rev09NewEngine(t, prov)
	params := domain.BacktestParams{StartDate: rev09Day1}

	resDay1 := eng.processStockJob(context.Background(), stockJob{symbol: "600000.SH"}, params, rev09Day1, nil)
	assert.False(t, resDay1.suspended, "day1 的末根 bar 就是 day1 ⇒ 不停牌")
	assert.InDelta(t, 10.0, resDay1.price, 1e-9)

	resDay2 := eng.processStockJob(context.Background(), stockJob{symbol: "600000.SH"}, params, rev09Day2, nil)
	assert.True(t, resDay2.suspended, "day2 缺 bar，末根是 day1 旧 bar ⇒ 停牌")
}

// TestFetchMarketDataForDay_ExcludesSuspendedAndCounts：停牌股摘出当日
// marketDataCache（不产信号/不参与撮合），但保留 pricesCache 的最后成交价
// （停牌持仓照旧盯市）；停牌计数进 run 级诊断。
func TestFetchMarketDataForDay_ExcludesSuspendedAndCounts(t *testing.T) {
	prov := marketdata.NewInMemoryProvider()
	prov.LoadOHLCV("STALE.SH", []domain.OHLCV{
		rev09Bar("STALE.SH", "2024-01-02", 1_000_000), // day2 停牌
	})
	prov.LoadOHLCV("LIVE.SH", []domain.OHLCV{
		rev09Bar("LIVE.SH", "2024-01-02", 1_000_000),
		rev09Bar("LIVE.SH", "2024-01-03", 1_000_000),
	})
	prov.SetTradingDays([]time.Time{rev09Day1, rev09Day2})
	eng := rev09NewEngine(t, prov)
	diag := newFillRunDiag()

	mdc, pc, _, _, suspended := eng.fetchMarketDataForDay(
		context.Background(), []string{"STALE.SH", "LIVE.SH"},
		domain.BacktestParams{StartDate: rev09Day1}, rev09Day2, nil, diag, nil, zerolog.Nop(),
	)

	assert.NotContains(t, mdc, "STALE.SH", "停牌股不得进当日 marketDataCache（否则信号环会按旧 bar 交易）")
	assert.Contains(t, mdc, "LIVE.SH")
	assert.True(t, suspended["STALE.SH"])
	assert.InDelta(t, 10.0, pc["STALE.SH"], 1e-9, "停牌股仍按最后成交价估值（估值口径不因停牌改变）")
	assert.Equal(t, 1, diag.suspendedTotal)
	assert.Equal(t, 1, diag.suspendedDays["STALE.SH"])
}

// TestProcessSignalsAndExecuteTrades_SkipsSuspendedSignal 兜底腿：即使策略
// 吐出停牌 symbol 的信号（自有状态），也不得成交，且计数可见。
func TestProcessSignalsAndExecuteTrades_SkipsSuspendedSignal(t *testing.T) {
	prov := marketdata.NewInMemoryProvider()
	eng := rev09NewEngine(t, prov)

	tr := tracker.NewTracker(1_000_000, 0.0003, 0, contracts.DefaultTradingConfig(), zerolog.Nop())
	state := &BacktestState{ID: "rev09-skip", Tracker: tr, TargetPositions: map[string]*domain.TargetPosition{}}
	diag := newFillRunDiag()

	eng.processSignalsAndExecuteTrades(
		context.Background(), state,
		[]domain.Signal{{Symbol: "STALE.SH", Direction: domain.DirectionLong, Date: rev09Day2}},
		map[string][]domain.OHLCV{}, map[string]float64{"STALE.SH": 10},
		nil, rev09Day2, nil, newExecAlgoRunDiag(),
		map[string]bool{"STALE.SH": true}, diag, zerolog.Nop(),
	)

	_, hasPosition := tr.GetPosition("STALE.SH")
	assert.False(t, hasPosition, "停牌 symbol 不得成交")
	assert.Equal(t, 1, diag.suspendedTotal, "停牌跳过计数必须可见")
}

// TestExecuteSignalTrade_TruncatesByDailyVolume：委托量 > 当日成交量 ×
// liquidity_factor ⇒ 截断到上限，截断进 run 级诊断（partial 语义）。
func TestExecuteSignalTrade_TruncatesByDailyVolume(t *testing.T) {
	prov := marketdata.NewInMemoryProvider()
	eng := rev09NewEngine(t, prov)

	tr := tracker.NewTracker(1_000_000, 0.0003, 0, contracts.DefaultTradingConfig(), zerolog.Nop())
	state := &BacktestState{ID: "rev09-trunc", Tracker: tr, TargetPositions: map[string]*domain.TargetPosition{}}
	diag := newFillRunDiag()

	bar := rev09Bar("600000.SH", "2024-01-03", 10_000) // 当日成交量 1 万股 → 上限 1000 股
	tp := &domain.TargetPosition{Symbol: "600000.SH", TargetQty: 10_000, LastUpdated: rev09Day2}

	eng.executeSignalTrade(
		context.Background(), state, nil, newExecAlgoRunDiag(), diag,
		domain.Signal{Symbol: "600000.SH", Direction: domain.DirectionLong, Date: rev09Day2},
		10_000, map[string]float64{"600000.SH": 10}, rev09Day2,
		&OrderExecutionOpts{OrderType: domain.OrderTypeMarket, DayBar: &bar}, tp, zerolog.Nop(),
	)

	pos, ok := tr.GetPosition("600000.SH")
	require.True(t, ok)
	assert.InDelta(t, 1_000.0, pos.Quantity, 1e-9, "上限 = 当日成交量 10,000 × 0.1")
	assert.Equal(t, 1, diag.truncations, "截断计数必须可见（partial 语义）")
	assert.InDelta(t, 9_000.0, diag.truncatedQty, 1e-9, "被截掉的股数 = 10,000 − 1,000")
	assert.NotEmpty(t, diag.truncSample, "首个截断现场必须留样")
}

// TestLiquidityFactor_ConfigFallbackAndClamp：零值回落 0.1（既有默认行为不
// 变）、(0,1] 原样、>1 clamp 到 1。
func TestLiquidityFactor_ConfigFallbackAndClamp(t *testing.T) {
	cases := []struct {
		name string
		in   float64
		want float64
	}{
		{"零值回落默认", 0, 0.1},
		{"负值回落默认", -0.2, 0.1},
		{"正常值原样", 0.5, 0.5},
		{"边界 1 原样", 1, 1},
		{"超过 1 被 clamp", 2, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng := &Engine{config: Config{LiquidityFactor: tc.in}}
			assert.InDelta(t, tc.want, eng.liquidityFactor(), 1e-12)
		})
	}
}
