package analysisartifact

// QT-78：构建器的形状与 fail-closed 语义。

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeMarket struct {
	constituents []string
	bars         map[string][]domain.OHLCV
}

func (f *fakeMarket) GetIndexConstituents(ctx context.Context, indexCode string) ([]string, error) {
	return f.constituents, nil
}

func (f *fakeMarket) BulkLoadOHLCV(ctx context.Context, symbols []string, start, end time.Time) (map[string][]domain.OHLCV, error) {
	return f.bars, nil
}

type fakeZScores map[string]map[domain.FactorType]float64

func (f fakeZScores) GetFactorZScore(factor domain.FactorType, date time.Time, symbol string) (float64, bool) {
	byFactor, ok := f[symbol]
	if !ok {
		return 0, false
	}
	z, ok := byFactor[factor]
	return z, ok
}

type fakeFactorReturns map[domain.FactorType][]*domain.FactorReturn

func (f fakeFactorReturns) GetFactorReturns(ctx context.Context, factor domain.FactorType, start, end time.Time) ([]*domain.FactorReturn, error) {
	return f[factor], nil
}

func bar(day int, close float64) domain.OHLCV {
	return domain.OHLCV{Date: time.Date(2024, 1, day, 0, 0, 0, 0, time.UTC), Close: close}
}

// portfolioFixture 是「两只持仓 + 三只基准成分」的最小完整输入。
//
// 行业市值：tech = A(1000) + C(1000) = 2000，fin = B(500)，总市值 2500。
// 区间收益：A +10%，B -5%，C +10%（首末收盘）。
func portfolioFixture() (Input, *fakeMarket) {
	in := Input{
		RunID:          "run-1",
		DatasetHash:    "ds-1",
		StartDate:      time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC),
		EndDate:        time.Date(2024, 1, 31, 0, 0, 0, 0, time.UTC),
		BenchmarkIndex: "000300.SH",
		Positions: []domain.Position{
			{Symbol: "A", MarketValue: 600},
			{Symbol: "B", MarketValue: 400},
		},
		Stocks: map[string]domain.Stock{
			"A": {Symbol: "A", Industry: "tech", MarketCap: 1000},
			"B": {Symbol: "B", Industry: "fin", MarketCap: 500},
			"C": {Symbol: "C", Industry: "tech", MarketCap: 1000},
		},
	}
	market := &fakeMarket{
		constituents: []string{"A", "B", "C"},
		bars: map[string][]domain.OHLCV{
			"A": {bar(2, 10), bar(31, 11)},
			"B": {bar(2, 20), bar(31, 19)},
			"C": {bar(2, 30), bar(31, 33)},
		},
	}
	return in, market
}

func TestBuildPortfolio_Shape(t *testing.T) {
	t.Parallel()

	in, market := portfolioFixture()
	b := &Builder{Market: market}
	snap, err := b.BuildPortfolio(context.Background(), in)
	require.NoError(t, err)

	require.Len(t, snap.Holdings, 2)
	assert.Equal(t, "A", snap.Holdings[0].Symbol)
	assert.Equal(t, "tech", snap.Holdings[0].Sector)
	assert.InDelta(t, 0.6, snap.Holdings[0].Weight, 1e-9)
	assert.InDelta(t, 0.1, snap.Holdings[0].PeriodReturn, 1e-9)
	assert.Equal(t, "B", snap.Holdings[1].Symbol)
	assert.InDelta(t, 0.4, snap.Holdings[1].Weight, 1e-9)
	assert.InDelta(t, -0.05, snap.Holdings[1].PeriodReturn, 1e-9)

	assert.InDelta(t, 0.8, snap.BenchmarkSectorWeights["tech"], 1e-9)
	assert.InDelta(t, 0.2, snap.BenchmarkSectorWeights["fin"], 1e-9)
	assert.InDelta(t, 0.1, snap.BenchmarkSectorReturns["tech"], 1e-9)
	assert.InDelta(t, -0.05, snap.BenchmarkSectorReturns["fin"], 1e-9)
	assert.InDelta(t, 0.07, snap.BenchmarkReturn, 1e-9)
	assert.Equal(t, "ds-1", snap.DatasetHash)
}

func TestBuildPortfolio_FailClosed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(*Input, *fakeMarket)
		want   string
	}{
		{
			name:   "缺 dataset_hash",
			mutate: func(in *Input, _ *fakeMarket) { in.DatasetHash = "" },
			want:   "holdings.dataset_hash",
		},
		{
			name:   "无持仓",
			mutate: func(in *Input, _ *fakeMarket) { in.Positions = nil },
			want:   "holdings",
		},
		{
			name:   "无基准成分",
			mutate: func(_ *Input, m *fakeMarket) { m.constituents = nil },
			want:   "benchmark_sector_weights",
		},
		{
			name:   "持仓缺行业",
			mutate: func(in *Input, _ *fakeMarket) { delete(in.Stocks, "B") },
			want:   "holdings.sector:B",
		},
		{
			name: "持仓缺区间收益",
			mutate: func(_ *Input, m *fakeMarket) {
				m.bars["B"] = []domain.OHLCV{bar(2, 20)}
			},
			want: "holdings.period_return:B",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in, market := portfolioFixture()
			tc.mutate(&in, market)

			_, err := (&Builder{Market: market}).BuildPortfolio(context.Background(), in)
			var missingErr *MissingError
			require.True(t, errors.As(err, &missingErr), "want MissingError, got %v", err)
			assert.Equal(t, tc.want, missingErr.Field)
		})
	}
}

func TestBuildFactors_Shape(t *testing.T) {
	t.Parallel()

	in, _ := portfolioFixture()
	b := &Builder{
		Factors: []domain.FactorType{domain.FactorMomentum},
		ZScores: fakeZScores{
			"A": {domain.FactorMomentum: 1.0},
			"B": {domain.FactorMomentum: -1.0},
		},
		FactorReturns: fakeFactorReturns{
			domain.FactorMomentum: {
				{TradeDate: time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC), TopMinusBot: 0.03},
				{TradeDate: time.Date(2024, 1, 31, 0, 0, 0, 0, time.UTC), TopMinusBot: 0.05},
			},
		},
	}

	out, err := b.BuildFactors(context.Background(), in)
	require.NoError(t, err)
	require.Len(t, out.Factors, 1)
	assert.Equal(t, "momentum", out.Factors[0].Name)
	assert.InDelta(t, 0.2, out.Factors[0].Exposure, 1e-9) // 0.6*1 + 0.4*(-1)
	assert.InDelta(t, 0.05, out.Factors[0].FactorReturn, 1e-9)
	assert.Equal(t, "ds-1", out.DatasetHash)
}

func TestBuildFactors_FailClosed(t *testing.T) {
	t.Parallel()

	in, _ := portfolioFixture()
	b := &Builder{
		Factors:       []domain.FactorType{domain.FactorMomentum},
		ZScores:       fakeZScores{},
		FactorReturns: fakeFactorReturns{},
	}

	_, err := b.BuildFactors(context.Background(), in)
	var missingErr *MissingError
	require.True(t, errors.As(err, &missingErr), "want MissingError, got %v", err)
	assert.Equal(t, "factor_exposures", missingErr.Field)
}
