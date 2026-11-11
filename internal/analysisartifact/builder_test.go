package analysisartifact

// QT-78：构建器的形状与 fail-closed 语义。

import (
	"context"
	"errors"
	"testing"
	"time"

	cdom "github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

import "tellurion/pkg/domain"

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

type fakeZScores map[string]map[cdom.FactorType]float64

func (f fakeZScores) GetFactorZScore(factor cdom.FactorType, date time.Time, symbol string) (float64, bool) {
	byFactor, ok := f[symbol]
	if !ok {
		return 0, false
	}
	z, ok := byFactor[factor]
	return z, ok
}

type fakeFactorReturns map[cdom.FactorType][]*domain.FactorReturn

func (f fakeFactorReturns) GetFactorReturns(ctx context.Context, factor cdom.FactorType, start, end time.Time) ([]*domain.FactorReturn, error) {
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
		Factors: []cdom.FactorType{cdom.FactorMomentum},
		ZScores: fakeZScores{
			"A": {cdom.FactorMomentum: 1.0},
			"B": {cdom.FactorMomentum: -1.0},
		},
		FactorReturns: fakeFactorReturns{
			cdom.FactorMomentum: {
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

// probeFactorReturns 在 fake 基础上实现 FactorReturnProbe（表里有没有任何行）。
type probeFactorReturns struct {
	fakeFactorReturns
	hasAny bool
	err    error
}

func (f probeFactorReturns) HasFactorReturns(ctx context.Context) (bool, error) {
	return f.hasAny, f.err
}

func TestBuildFactors_AllMissing_NoProbe_ReportsNoRows(t *testing.T) {
	t.Parallel()

	in, _ := portfolioFixture()
	b := &Builder{
		Factors:       []cdom.FactorType{cdom.FactorMomentum},
		ZScores:       fakeZScores{},
		FactorReturns: fakeFactorReturns{},
	}

	_, err := b.BuildFactors(context.Background(), in)
	var unavailable *FactorReturnsUnavailableError
	require.True(t, errors.As(err, &unavailable), "want FactorReturnsUnavailableError, got %v", err)
	assert.Equal(t, FactorReturnsNoRows, unavailable.Reason)
	assert.Equal(t, []cdom.FactorType{cdom.FactorMomentum}, unavailable.Factors)
	assert.Contains(t, err.Error(), "momentum")
}

func TestBuildFactors_NoWriter_EmptyTable_ReportsNoWriter(t *testing.T) {
	t.Parallel()

	in, _ := portfolioFixture()
	b := &Builder{
		Factors: []cdom.FactorType{cdom.FactorMomentum},
		ZScores: fakeZScores{
			"A": {cdom.FactorMomentum: 1.0},
		},
		FactorReturns: probeFactorReturns{hasAny: false},
	}

	_, err := b.BuildFactors(context.Background(), in)
	var unavailable *FactorReturnsUnavailableError
	require.True(t, errors.As(err, &unavailable), "want FactorReturnsUnavailableError, got %v", err)
	assert.Equal(t, FactorReturnsNoWriter, unavailable.Reason)
	assert.Contains(t, err.Error(), "no production writer")
	assert.Contains(t, err.Error(), "ADR-038")
}

func TestBuildFactors_TableHasRows_ReportsNoRows(t *testing.T) {
	t.Parallel()

	in, _ := portfolioFixture()
	b := &Builder{
		Factors:       []cdom.FactorType{cdom.FactorMomentum},
		ZScores:       fakeZScores{"A": {cdom.FactorMomentum: 1.0}},
		FactorReturns: probeFactorReturns{hasAny: true},
	}

	_, err := b.BuildFactors(context.Background(), in)
	var unavailable *FactorReturnsUnavailableError
	require.True(t, errors.As(err, &unavailable), "want FactorReturnsUnavailableError, got %v", err)
	assert.Equal(t, FactorReturnsNoRows, unavailable.Reason)
	assert.NotContains(t, err.Error(), "no production writer")
}

func TestBuildFactors_ProbeError_DegradesToNoRows(t *testing.T) {
	t.Parallel()

	in, _ := portfolioFixture()
	b := &Builder{
		Factors:       []cdom.FactorType{cdom.FactorMomentum},
		ZScores:       fakeZScores{"A": {cdom.FactorMomentum: 1.0}},
		FactorReturns: probeFactorReturns{err: errors.New("db down")},
	}

	_, err := b.BuildFactors(context.Background(), in)
	var unavailable *FactorReturnsUnavailableError
	require.True(t, errors.As(err, &unavailable), "want FactorReturnsUnavailableError, got %v", err)
	assert.Equal(t, FactorReturnsNoRows, unavailable.Reason, "探针失败不得被伪装成 no_writer")
}

func TestBuildFactors_PartialMissing_NamesMissingFactors(t *testing.T) {
	t.Parallel()

	in, _ := portfolioFixture()
	b := &Builder{
		Factors: []cdom.FactorType{cdom.FactorMomentum, cdom.FactorValue},
		ZScores: fakeZScores{
			"A": {cdom.FactorMomentum: 1.0},
			"B": {cdom.FactorMomentum: -1.0},
		},
		FactorReturns: fakeFactorReturns{
			cdom.FactorMomentum: {
				{TradeDate: time.Date(2024, 1, 31, 0, 0, 0, 0, time.UTC), TopMinusBot: 0.05},
			},
		},
	}

	out, err := b.BuildFactors(context.Background(), in)
	require.NoError(t, err, "部分缺失不应让整类产物缺席")
	require.Len(t, out.Factors, 1)
	assert.Equal(t, "momentum", out.Factors[0].Name)
	assert.Equal(t, []string{"value"}, out.MissingFactors, "缺失因子必须点名，不许静默跳过")
}
