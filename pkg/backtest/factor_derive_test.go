package backtest

// ADR-037 D2 按需求值的验收用例（CORE-FACTOR-CACHE-WRITER 重开）。
//
// 这些用例要证明的是四件事，对应任务单的四条口径：
//   1. 空 factor_cache 时必需因子能现场算出来并物化到 run 级缓存；
//   2. 同一 run 内第二次调用不再重算（物化命中）；
//   3. 截面不足（n<2）与不可派生（code/pending）都不伪造数据；
//   4. 覆盖度门禁读的是 run 级物化结果，而不是已退役的 public.factor_cache。

import (
	"context"
	"math"
	"testing"
	"time"

	cdom "github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/stretchr/testify/require"
	"tellurion/pkg/domain"
	"tellurion/pkg/marketdata"
	"tellurion/pkg/strategy"
)

// ─── 夹具 ──────────────────────────────────────────────────────────────

// deriveProvider 只返回给定交易日轴内的行情，且**不**复刻 inmemory
// provider「窗口为空就返回全部」的兜底 —— 那会让覆盖度校验看不到真实缺口。
type deriveProvider struct {
	mockProvider
	bars map[string][]domain.OHLCV
	days []time.Time
}

func (p *deriveProvider) GetTradingDays(_ context.Context, start, end time.Time) ([]time.Time, error) {
	out := make([]time.Time, 0, len(p.days))
	for _, d := range p.days {
		if !d.Before(start) && !d.After(end) {
			out = append(out, d)
		}
	}
	return out, nil
}

func (p *deriveProvider) CheckCalendarExists(ctx context.Context, start, end time.Time) (bool, error) {
	days, err := p.GetTradingDays(ctx, start, end)
	return len(days) > 0, err
}

func (p *deriveProvider) GetOHLCV(ctx context.Context, symbol string, start, end time.Time) ([]domain.OHLCV, error) {
	all, err := p.BulkLoadOHLCV(ctx, []string{symbol}, start, end)
	if err != nil {
		return nil, err
	}
	return all[symbol], nil
}

func (p *deriveProvider) BulkLoadOHLCV(_ context.Context, symbols []string, start, end time.Time) (map[string][]domain.OHLCV, error) {
	out := make(map[string][]domain.OHLCV, len(symbols))
	for _, sym := range symbols {
		for _, b := range p.bars[sym] {
			if b.Date.Before(start) || b.Date.After(end) {
				continue
			}
			out[sym] = append(out[sym], b)
		}
	}
	return out, nil
}

// deriveBars 造三条斜率不同的单调序列：momentum 的排序稳定，z-score 可区分。
func deriveBars(symbols []string, axis []time.Time) map[string][]domain.OHLCV {
	out := make(map[string][]domain.OHLCV, len(symbols))
	for i, sym := range symbols {
		price := 10.0 + float64(i)*3
		drift := 0.002 + 0.004*float64(i)
		bars := make([]domain.OHLCV, 0, len(axis))
		for _, d := range axis {
			next := price * (1 + drift)
			bars = append(bars, domain.OHLCV{
				Symbol:   sym,
				Date:     d,
				Open:     price,
				High:     next * 1.01,
				Low:      price * 0.99,
				Close:    next,
				Volume:   1_000_000,
				Turnover: next * 1_000_000,
			})
			price = next
		}
		out[sym] = bars
	}
	return out
}

// deriveFundamentals 给每只票一条 PIT 记录：PE/ROE 均为正，且彼此不同。
func deriveFundamentals(symbols []string, asOf time.Time) map[string]strategy.FundamentalSeries {
	out := make(map[string]strategy.FundamentalSeries, len(symbols))
	for i, sym := range symbols {
		pe := 8.0 + float64(i)*4
		roe := 0.08 + 0.03*float64(i)
		out[sym] = strategy.FundamentalSeries{{
			Symbol: sym,
			Date:   asOf,
			PE:     &pe,
			ROE:    &roe,
		}}
	}
	return out
}

func deriveFixture(t *testing.T) (*Engine, *runContext, []string, []time.Time) {
	t.Helper()
	symbols := []string{"600001.SH", "600002.SH", "600003.SH"}
	axis := weekdayDays(mustDate("2024-01-02"), mustDate("2024-03-29"))
	prov := &deriveProvider{bars: deriveBars(symbols, axis), days: axis}
	eng := newTestEngineWithProvider(t, prov)
	params := domain.BacktestParams{
		StrategyName: "multi_factor",
		StockPool:    symbols,
		StartDate:    axis[0],
		EndDate:      axis[len(axis)-1],
	}
	rc := newTestRunSpan(t, eng, params)
	eng.SetFundamentals(deriveFundamentals(symbols, axis[0].AddDate(0, 0, -5)))
	rc.mu.Lock()
	rc.fundamentals = eng.fundamentals
	rc.mu.Unlock()
	return eng, rc, symbols, axis
}

// ─── 1. 空缓存 ⇒ 现场算出三个必需因子 ────────────────────────────────

func TestDeriveMissingFactors_FillsValueQualityMomentum(t *testing.T) {
	_, rc, symbols, axis := deriveFixture(t)
	require.Zero(t, rc.factor.Entries(), "前提：因子缓存必须是空的（public.factor_cache 退役后恒空）")

	derived, err := rc.deriveMissingFactors(context.Background(), "multi_factor", symbols, axis)
	require.NoError(t, err)
	require.Equal(t, 3, derived, "value/quality/momentum 三个 leaf/formula 因子都应派生")

	required, err := requiredFactorTypes("multi_factor")
	require.NoError(t, err)
	require.Empty(t, rc.factorCoverage(required), "派生后不应再有缺失因子")

	// 三个因子都能按日循环的日期轴精确命中。
	last := axis[len(axis)-1]
	for _, f := range []cdom.FactorType{cdom.FactorValue, cdom.FactorQuality, cdom.FactorMomentum} {
		for _, sym := range symbols {
			z, ok := rc.factorZScore(f, last, sym)
			require.True(t, ok, "因子 %s 在 %s / %s 上应有 z-score", f, last.Format("2006-01-02"), sym)
			require.False(t, math.IsNaN(z) || math.IsInf(z, 0), "z-score 必须是有限值")
		}
	}

	// 同一天的横截面 z-score 均值应为 0（标准化的定义）。
	sum := 0.0
	for _, sym := range symbols {
		z, ok := rc.factorZScore(cdom.FactorMomentum, last, sym)
		require.True(t, ok)
		sum += z
	}
	require.InDelta(t, 0, sum, 1e-9, "同一截面的 z-score 之和必须为 0")
}

// ─── 2. 同一 run 第二次调用不重算 ────────────────────────────────────

func TestDeriveMissingFactors_SecondCallDoesNotRecompute(t *testing.T) {
	_, rc, symbols, axis := deriveFixture(t)

	first, err := rc.deriveMissingFactors(context.Background(), "multi_factor", symbols, axis)
	require.NoError(t, err)
	require.Equal(t, 3, first)
	entriesAfterFirst := rc.factor.Entries()

	second, err := rc.deriveMissingFactors(context.Background(), "multi_factor", symbols, axis)
	require.NoError(t, err)
	require.Zero(t, second, "必需因子已全部物化，第二次调用不得再算（物化命中）")
	require.Equal(t, entriesAfterFirst, rc.factor.Entries(), "物化命中不得改变缓存条目")
}

// ─── 3. 截面不足 ⇒ 不产出 ────────────────────────────────────────────

func TestDeriveMissingFactors_SingleSymbolNotDerived(t *testing.T) {
	symbols := []string{"600001.SH"}
	axis := weekdayDays(mustDate("2024-01-02"), mustDate("2024-03-29"))
	prov := &deriveProvider{bars: deriveBars(symbols, axis), days: axis}
	eng := newTestEngineWithProvider(t, prov)
	params := domain.BacktestParams{
		StrategyName: "multi_factor",
		StockPool:    symbols,
		StartDate:    axis[0],
		EndDate:      axis[len(axis)-1],
	}
	rc := newTestRunSpan(t, eng, params)
	eng.SetFundamentals(deriveFundamentals(symbols, axis[0].AddDate(0, 0, -5)))
	rc.mu.Lock()
	rc.fundamentals = eng.fundamentals
	rc.mu.Unlock()

	derived, err := rc.deriveMissingFactors(context.Background(), "multi_factor", symbols, axis)
	require.NoError(t, err)
	require.Zero(t, derived, "单标的截面 n=1，标准化没有意义，不得产出")
	require.Zero(t, rc.factor.Entries())
}

// ─── 4. 不可派生因子保持缺失（不伪造）────────────────────────────────

func TestDeriveMissingFactors_NonDerivableFactorStaysMissing(t *testing.T) {
	def, ok := builtinFactorDef(cdom.FactorSize)
	require.True(t, ok, "size 应在冻结种子里")
	require.False(t, derivableFactorDef(def), "size 的 kernel 是 pending，不得被当作可派生")

	mom, ok := builtinFactorDef(cdom.FactorMomentum)
	require.True(t, ok)
	require.True(t, derivableFactorDef(mom), "momentum 是 formula，应可派生")
}

// ─── 5. 覆盖度闸读 run 级物化结果，不读退役的 DB 表 ─────────────────

// emptyFactorStore 模拟「public.factor_cache 是空表」这一事实：
// 它的读取恒返回空。门禁若回退到看这张表，本用例就必须变红。
type emptyFactorStore struct {
	Storage
}

func (s *emptyFactorStore) GetFactorCacheRange(
	_ context.Context, _ cdom.FactorType, _, _ time.Time,
) ([]*domain.FactorCacheEntry, error) {
	return nil, nil
}

func TestFactorGateReadsRunLevelNotDB(t *testing.T) {
	eng := newTestEngine(t)
	eng.store = &emptyFactorStore{}
	seedFactors(eng, 1.0, cdom.FactorMomentum, cdom.FactorQuality, cdom.FactorValue)

	rc := newTestRunSpan(t, eng, domain.BacktestParams{StrategyName: "multi_factor"})
	require.Zero(t, len(rc.factorCoverage([]cdom.FactorType{cdom.FactorValue})),
		"run 级缓存有条目 ⇒ 门禁必须放行，哪怕 DB 表为空")
	require.NoError(t, rc.ensureRequiredFactors("multi_factor"),
		"门禁不得去看已退役的 public.factor_cache")
}

// ─── 6. 全链路：空 factor_cache + multi_factor ⇒ completed 且成交 ────

func TestRunBacktest_MultiFactorOnDemandProducesTrades(t *testing.T) {
	const days = 70
	symbols := []string{"600001.SH", "600002.SH", "600003.SH"}
	start := mustDate("2024-01-02")

	prov := marketdata.NewInMemoryProvider()
	for sym, bars := range synthOHLCV(symbols, days, start) {
		prov.LoadOHLCV(sym, bars)
	}
	axis := make([]time.Time, 0, days)
	stocks := make([]domain.Stock, 0, len(symbols))
	for d := 0; d < days; d++ {
		axis = append(axis, start.AddDate(0, 0, d))
	}
	prov.SetTradingDays(axis)
	for _, sym := range symbols {
		stocks = append(stocks, domain.Stock{Symbol: sym, Name: "OND " + sym, ListDate: start.AddDate(-1, 0, 0)})
	}
	prov.LoadStocks(stocks)

	eng := newTestEngineWithProvider(t, prov)
	eng.admission = nil
	// 本地风控：没有它，calculatePosition 会走 HTTP 到 risk 服务（测试里没有），
	// 整条 run 会挂在重试上直到超时。
	applyOptions(eng, provenanceBaseOptions(t))
	eng.SetFundamentals(deriveFundamentals(symbols, start.AddDate(0, 0, -10)))

	resp, err := eng.RunBacktest(context.Background(), BacktestRequest{
		Strategy:       "multi_factor",
		StockPool:      symbols,
		StartDate:      start.AddDate(0, 0, 20).Format("2006-01-02"),
		EndDate:        start.AddDate(0, 0, days-1).Format("2006-01-02"),
		InitialCapital: 1_000_000,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, StatusCompleted, resp.Status, "invalid_reasons=%v", resp.InvalidReasons)
	require.Greater(t, resp.TotalTrades, 0,
		"按需求值给了因子，multi_factor 必须真的下单（否则等于退回 409 的零成交）")
}
