package backtest

// REV-18 端到端对拍：同区间重跑，数据整体漂移（模拟前复权基准换成更晚的
// 交易日），带快照的两跑必须逐位一致。
//
// 与 engine_reproducibility_test.go（P0-5，内存 fixture 同数据两跑）的分工：
// 那条守「引擎确定性」，这条守「真实数据路径上的 qfq 基准漂移」—— 复现性
// 测试跑内存 fixture 天然测不到它（REV-18 的立项理由）。
//
// 破坏验证腿在 _sabotage 子测试：把 resolve 改成无视冻结基准（=「实时最新
// 因子」的旧行为）→ 漂移后重跑结果必然不同 → 红。见本文件底部的说明。

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/ruoxizhnya/tellurion-contracts/fees"
	"tellurion/pkg/marketdata"
	"tellurion/pkg/risk"
	"tellurion/pkg/strategy"
	"tellurion/pkg/strategy/examples"
)

// rev18ScaleBars 把一个 symbol 的四个价格字段乘 k —— 精确模拟「前复权基准
// 换成更晚日」的效果（整条曲线乘常数；成交量/成交额不动，与
// normalizeDailyOHLCV「amount 不乘复权 scale」的口径一致）。
func rev18ScaleBars(bars []domain.OHLCV, k float64) []domain.OHLCV {
	scaled := make([]domain.OHLCV, len(bars))
	copy(scaled, bars)
	for i := range scaled {
		scaled[i].Open *= k
		scaled[i].High *= k
		scaled[i].Low *= k
		scaled[i].Close *= k
	}
	return scaled
}

// rev18ScaleAll 是 rev18ScaleBars 的全 universe 版本。
func rev18ScaleAll(data map[string][]domain.OHLCV, k float64) map[string][]domain.OHLCV {
	out := make(map[string][]domain.OHLCV, len(data))
	for sym, bars := range data {
		out[sym] = rev18ScaleBars(bars, k)
	}
	return out
}

// buildRev18Engine 复刻 buildSyntheticEngine 的最小配方，但 (a) 行情由调用方
// 给（可注入漂移后的数据），(b) 挂 REV-18 的 anchor store。
func buildRev18Engine(t *testing.T, data map[string][]domain.OHLCV, symbols []string, days []time.Time, store *memAnchorStore) *Engine {
	t.Helper()

	prov := marketdata.NewInMemoryProvider()
	for sym, bars := range data {
		prov.LoadOHLCV(sym, bars)
	}
	prov.SetTradingDays(days)
	stocks := make([]domain.Stock, 0, len(symbols))
	for _, sym := range symbols {
		stocks = append(stocks, domain.Stock{
			Symbol:   sym,
			Name:     "STK-" + sym,
			ListDate: days[0].AddDate(-1, 0, 0),
		})
	}
	prov.LoadStocks(stocks)

	v := viper.New()
	v.Set("backtest.initial_capital", 1000000.0)
	v.Set("backtest.commission_rate", fees.DefaultCommissionRate)
	v.Set("backtest.slippage_rate", fees.DefaultSlippageRate)
	v.Set("backtest.risk_free_rate", 0.03)
	v.Set("backtest.seed", 42)

	eng, err := NewEngineWithOptions(Config{}, prov,
		WithAdjustmentAnchorStore(store))
	if err != nil {
		t.Fatalf("NewEngineWithOptions: %v", err)
	}
	eng.LoadOHLCVInMemory(data)
	eng.SetParallelWorkers(1)

	rm, err := risk.NewRiskManager(risk.RiskManagerConfig{
		TargetVolatility:    0.15,
		MaxPositionWeight:   0.10,
		MinPositionWeight:   0.01,
		ATRPeriod:           14,
		BaseMultiplier:      2.0,
		BullMultiplier:      1.5,
		BearMultiplier:      3.0,
		SidewaysMultiplier:  2.0,
		TakeProfitMult:      3.0,
		VolLookbackDays:     60,
		AnnualizationFactor: math.Sqrt(252),
		FastMAPeriod:        50,
		SlowMAPeriod:        200,
		RegimeVolLookback:   120,
	}, zerolog.Nop())
	if err != nil {
		t.Fatalf("risk.NewRiskManager: %v", err)
	}
	eng.SetRiskManager(rm)
	return eng
}

// registerRev18Momentum 把 momentum 策略注册进全局 registry 并配置显式参数。
//
// 两个坑（都是实测踩出来的）：
//   - 零值参数的策略不会产出任何信号（0 成交 → OBS-01 判 invalid），必须
//     Configure 显式给 lookback/top_n/rebalance_frequency；
//   - 全局 registry 是单例、跨测试共享：已注册时取出**重新 Configure**，
//     不算错。为避免与其它测试的 Configure 窗口交错（weekly/daily 互踩），
//     本测试**不开 t.Parallel**。
func registerRev18Momentum(t *testing.T) {
	t.Helper()

	params := map[string]interface{}{
		"lookback_days":       20,
		"top_n":               5,
		"max_positions":       5,
		"rebalance_frequency": "daily",
	}
	ms := examples.NewMomentumStrategy()
	if err := strategy.GlobalRegisterFactory(func() strategy.Strategy { return ms }); err != nil {
		existing, getErr := strategy.DefaultRegistry.Get("momentum")
		if getErr != nil {
			t.Fatalf("momentum 既注册不上也取不出：register=%v get=%v", err, getErr)
		}
		cfg, ok := existing.(strategy.Configurable)
		if !ok {
			t.Fatal("已注册的 momentum 不可配置")
		}
		if err := cfg.Configure(params); err != nil {
			t.Fatalf("reconfigure momentum: %v", err)
		}
		return
	}
	if err := ms.Configure(params); err != nil {
		t.Fatalf("configure momentum: %v", err)
	}
}

func TestEngine_REV18_SameRangeRerunBitIdenticalAcrossQfqResync(t *testing.T) {
	if testing.Short() {
		t.Skip("full-run test, skipped in -short")
	}
	// 刻意不 t.Parallel：全局 momentum 单例的 Configure 窗口不能与其它测试交错。

	// momentum 注册（与 engine_rebalance_date_test.go 同款防御：全局单例
	// 已注册就取出重配，不算错）。不注册的话 registry 取不到策略，引擎
	// 静默空跑 0 成交 —— 结果会被 OBS-01 判 invalid。
	const nSymbols, nDays = 20, 252
	registerRev18Momentum(t)

	start := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	symbols := make([]string, nSymbols)
	for i := range symbols {
		symbols[i] = fmt.Sprintf("%06d.SH", 600000+i)
	}
	data := rev18SynthOHLCV(symbols, nDays, start)
	days := make([]time.Time, 0, nDays)
	for d := 0; d < nDays; d++ {
		days = append(days, start.AddDate(0, 0, d))
	}

	store := newMemAnchorStore()
	req := BacktestRequest{
		Strategy:       "momentum",
		StockPool:      symbols,
		StartDate:      start.Format("2006-01-02"),
		EndDate:        days[nDays-1].Format("2006-01-02"),
		InitialCapital: 1000000.0,
		RiskFreeRate:   0.03,
	}

	// ① 首跑：原始刻度。跑完 anchor 冻结在 store 里。
	resp1, err := buildRev18Engine(t, data, symbols, days, store).RunBacktest(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, resp1)
	require.Greater(t, resp1.TotalTrades, 0, "fixture 必须有成交，否则对拍无意义")
	require.NotEmpty(t, resp1.PortfolioValues)
	require.Greater(t, store.saves, 0, "首跑必须冻结过 anchor")

	// ② 模拟重同步：前复权基准换成更晚的交易日 ⇒ 整条曲线 ×0.5。
	//    k=0.5 是 2 的幂：浮点缩放精确可逆，「逐位一致」不被尾数污染。
	drifted := rev18ScaleAll(data, 0.5)

	// ③ 重跑：漂移后的数据 + 同一份冻结基准。
	resp2, err := buildRev18Engine(t, drifted, symbols, days, store).RunBacktest(context.Background(), req)
	require.NoError(t, err)

	assert.Exactly(t, resp1.PortfolioValues, resp2.PortfolioValues,
		"同区间重跑（qfq 基准整体漂移 ×0.5）净值曲线必须逐位一致（REV-18 红线）")

	// Trade.ID 是纳秒时间戳（tracker 既有行为，两次运行必然不同）——
	// 剥离后逐位比对语义字段（方向/数量/价格/费用/日期）。
	stripIDs := func(ts []domain.Trade) []domain.Trade {
		out := make([]domain.Trade, len(ts))
		copy(out, ts)
		for i := range out {
			out[i].ID = ""
		}
		return out
	}
	assert.Exactly(t, stripIDs(resp1.Trades), stripIDs(resp2.Trades),
		"成交序列必须逐位一致（ID 除外——那是墙钟，不是行情口径）")
	assert.Equal(t, len(resp1.Trades), len(resp2.Trades))
	assert.Exactly(t, resp1.TotalReturn, resp2.TotalReturn)
}

// rev18SynthOHLCV 与 engine_synthetic_bench_test.go 的 synthOHLCV 同源
// （rng seed 42），抽到这里是为了给本测试注入可漂移的数据。**两份必须保持
// 一致**：本测试依赖「同一 seed 生成同一序列」来做漂移前后的对拍。
func rev18SynthOHLCV(symbols []string, days int, start time.Time) map[string][]domain.OHLCV {
	rng := rand.New(rand.NewSource(42))
	out := make(map[string][]domain.OHLCV, len(symbols))
	for _, sym := range symbols {
		bars := make([]domain.OHLCV, 0, days)
		price := 10.0 + rng.Float64()*90.0
		for d := 0; d < days; d++ {
			date := start.AddDate(0, 0, d)
			drift := (rng.Float64() - 0.49) * 0.04
			open := price
			close := open * (1 + drift)
			high := math.Max(open, close) * (1 + rng.Float64()*0.01)
			low := math.Min(open, close) * (1 - rng.Float64()*0.01)
			vol := float64(1_000_000 + rng.Int63n(5_000_000))
			turnover := (open + close) / 2 * vol

			limitUp := rng.Float64() < 0.05
			if limitUp {
				close = math.Round(open*1.10*100) / 100
				high = close
			}

			bars = append(bars, domain.OHLCV{
				Symbol: sym, Date: date,
				Open: open, High: high, Low: low, Close: close,
				Volume: vol, Turnover: turnover, LimitUp: limitUp,
			})
			price = close
		}
		out[sym] = bars
	}
	return out
}

// 说明（破坏验证）：本测试的红腿要求把 adjustment_snapshot.go 的 resolve()
// 改成无视冻结基准（anchor lookup 恒 false，等价于「基准 = 实时最新因子」的
// 旧行为）——此时 ③ 的重跑会按 ×0.5 的漂移刻度直接跑，PortfolioValues 与
// ① 逐位不同，测试红；还原后转绿。执行记录见 TASKS.md 的 REV-18 完成记录。
