package factoranalytics

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	cdom "github.com/ruoxizhnya/tellurion-contracts/domain"

	"tellurion/pkg/domain"
)

type fakeBars struct {
	stocks []domain.Stock
	bars   map[string][]domain.OHLCV
	err    error
}

func (f *fakeBars) GetStocks(ctx context.Context, exchange string) ([]domain.Stock, error) {
	return f.stocks, f.err
}

func (f *fakeBars) BulkLoadOHLCV(ctx context.Context, symbols []string, start, end time.Time) (map[string][]domain.OHLCV, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make(map[string][]domain.OHLCV, len(symbols))
	for _, s := range symbols {
		if series, ok := f.bars[s]; ok {
			out[s] = series
		}
	}
	return out, nil
}

type fakeFundamentals struct {
	rows []domain.FundamentalData
}

func (f *fakeFundamentals) GetFundamentalsSnapshot(ctx context.Context, cutoff time.Time) ([]domain.FundamentalData, error) {
	return f.rows, nil
}

type fakeWriter struct {
	returns []*domain.FactorReturn
	ics     []*domain.ICEntry
}

func (f *fakeWriter) SaveFactorReturnBatch(ctx context.Context, records []*domain.FactorReturn) error {
	f.returns = append(f.returns, records...)
	return nil
}

func (f *fakeWriter) SaveICEntryBatch(ctx context.Context, records []*domain.ICEntry) error {
	f.ics = append(f.ics, records...)
	return nil
}

// buildTrendBars 造一组「日收益随序号单调递增」的行情：序号越大，动量越高、
// 前向收益也越高，因此 momentum 的 IC 应贴近 +1。
func buildTrendBars(symbols int, days int) map[string][]domain.OHLCV {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make(map[string][]domain.OHLCV, symbols)
	for i := 0; i < symbols; i++ {
		symbol := symbolName(i)
		price := 10.0
		daily := 0.001 + 0.0002*float64(i)
		series := make([]domain.OHLCV, 0, days)
		for d := 0; d < days; d++ {
			if d > 0 {
				price *= 1 + daily
			}
			series = append(series, domain.OHLCV{
				Symbol: symbol,
				Date:   start.AddDate(0, 0, d),
				Close:  price,
			})
		}
		out[symbol] = series
	}
	return out
}

func symbolName(i int) string {
	return "S" + string(rune('A'+i/26)) + string(rune('A'+i%26))
}

func stockList(n int) []domain.Stock {
	out := make([]domain.Stock, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, domain.Stock{Symbol: symbolName(i), Status: "listed"})
	}
	return out
}

func baseRequest() Request {
	return Request{
		Start:           time.Date(2026, 1, 26, 0, 0, 0, 0, time.UTC),
		End:             time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC),
		LookbackDays:    20,
		HorizonDays:     5,
		RebalanceDays:   5,
		MinCrossSection: 20,
		Factors:         []cdom.FactorType{cdom.FactorMomentum},
	}
}

func TestComputeMomentumProducesMonotoneQuintilesAndIC(t *testing.T) {
	bars := buildTrendBars(30, 80)
	provider := &fakeBars{bars: bars, stocks: stockList(30)}
	w := &fakeWriter{}
	c := NewComputer(provider, nil, w)

	res, err := c.Compute(context.Background(), baseRequest())
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if res.Universe != 30 {
		t.Fatalf("universe = %d, want 30", res.Universe)
	}
	if len(res.RebalanceDates) == 0 {
		t.Fatal("no rebalance dates")
	}
	if len(res.FactorReturns) != len(res.RebalanceDates)*5 {
		t.Fatalf("factor returns = %d, want %d", len(res.FactorReturns), len(res.RebalanceDates)*5)
	}
	if len(res.ICEntries) != len(res.RebalanceDates) {
		t.Fatalf("ic entries = %d, want %d", len(res.ICEntries), len(res.RebalanceDates))
	}
	if !res.Written {
		t.Fatal("result should be marked written")
	}
	if len(w.returns) != len(res.FactorReturns) || len(w.ics) != len(res.ICEntries) {
		t.Fatalf("writer received %d/%d, want %d/%d", len(w.returns), len(w.ics), len(res.FactorReturns), len(res.ICEntries))
	}

	// 每个再平衡日的五组平均收益必须严格递增，IC 贴近 1。
	byDate := make(map[time.Time]map[int]float64)
	for _, r := range res.FactorReturns {
		if byDate[r.TradeDate] == nil {
			byDate[r.TradeDate] = make(map[int]float64)
		}
		byDate[r.TradeDate][r.Quintile] = r.AvgReturn
	}
	for d, qs := range byDate {
		for q := 2; q <= 5; q++ {
			if qs[q] <= qs[q-1] {
				t.Fatalf("%s: quintile %d avg %.6f <= quintile %d avg %.6f", d, q, qs[q], q-1, qs[q-1])
			}
		}
	}
	for _, entry := range res.ICEntries {
		// 动量与前向收益都随序号单调，但 20 日复利让两者只是单调同序而非
		// 线性，故 Pearson 略小于 1（~0.9999），Spearman 严格 = 1。
		if entry.IC < 0.99 {
			t.Fatalf("IC at %s = %.9f, want > 0.99", entry.TradeDate, entry.IC)
		}
		if math.Abs(entry.TopIC-1) > 1e-9 {
			t.Fatalf("TopIC at %s = %.9f, want 1", entry.TradeDate, entry.TopIC)
		}
		if entry.PValue > 1e-6 {
			t.Fatalf("p-value at %s = %.9f, want ~0", entry.TradeDate, entry.PValue)
		}
	}
}

func TestComputeValueAndQualityUsePITFundamentals(t *testing.T) {
	provider := &fakeBars{bars: buildTrendBars(30, 80), stocks: stockList(30)}
	rows := make([]domain.FundamentalData, 0, 30)
	for i := 0; i < 30; i++ {
		pe := float64(10 + i) // 1/PE 随序号递减
		roe := float64(i)     // ROE 随序号递增
		rows = append(rows, domain.FundamentalData{TsCode: symbolName(i), PE: &pe, ROE: &roe})
	}
	c := NewComputer(provider, &fakeFundamentals{rows: rows}, nil)

	req := baseRequest()
	req.Factors = []cdom.FactorType{cdom.FactorValue, cdom.FactorQuality}
	res, err := c.Compute(context.Background(), req)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	for _, entry := range res.ICEntries {
		switch entry.FactorName {
		case cdom.FactorValue:
			// 1/PE 与前向收益反向单调；同样因复利而非严格线性，
			// Pearson 约 -0.93 ~ -1，Spearman 严格 = -1。
			if entry.IC > -0.9 {
				t.Fatalf("value IC at %s = %.6f, want <= -0.9", entry.TradeDate, entry.IC)
			}
			if math.Abs(entry.TopIC+1) > 1e-9 {
				t.Fatalf("value TopIC at %s = %.9f, want -1", entry.TradeDate, entry.TopIC)
			}
		case cdom.FactorQuality:
			if entry.IC < 0.99 {
				t.Fatalf("quality IC at %s = %.6f, want >= 0.99", entry.TradeDate, entry.IC)
			}
			if math.Abs(entry.TopIC-1) > 1e-9 {
				t.Fatalf("quality TopIC at %s = %.9f, want 1", entry.TradeDate, entry.TopIC)
			}
		}
	}
}

func TestComputeSkipsWhenCrossSectionTooSmall(t *testing.T) {
	provider := &fakeBars{bars: buildTrendBars(10, 80), stocks: stockList(10)}
	c := NewComputer(provider, nil, nil)
	res, err := c.Compute(context.Background(), baseRequest())
	if err == nil {
		if len(res.RebalanceDates) != 0 {
			t.Fatalf("expected no produced dates, got %d", len(res.RebalanceDates))
		}
		return
	}
	if !errors.Is(err, ErrNoData) {
		t.Fatalf("err = %v, want ErrNoData", err)
	}
}

func TestComputeRejectsUnsupportedFactor(t *testing.T) {
	c := NewComputer(&fakeBars{bars: buildTrendBars(30, 80), stocks: stockList(30)}, nil, nil)
	req := baseRequest()
	req.Factors = []cdom.FactorType{cdom.FactorVolatility}
	if _, err := c.Compute(context.Background(), req); err == nil {
		t.Fatal("expected error for unsupported factor")
	}
}

func TestComputeRequiresProvider(t *testing.T) {
	c := NewComputer(nil, nil, nil)
	if _, err := c.Compute(context.Background(), baseRequest()); err == nil {
		t.Fatal("expected error without provider")
	}
}
