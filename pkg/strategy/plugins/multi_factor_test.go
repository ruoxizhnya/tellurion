package plugins

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"tellurion/pkg/domain"
	"tellurion/pkg/strategy"
)

func TestMultiFactorParameters(t *testing.T) {
	s := &multiFactorStrategy{
		params: MultiFactorConfig{
			ValueWeight:        0.4,
			QualityWeight:      0.3,
			MomentumWeight:     0.3,
			LookbackDays:       60,
			TopN:               10,
			RebalanceFrequency: "monthly",
		},
	}

	params := s.Parameters()

	expected := map[string]struct {
		def     any
		typeStr string
		min     float64
		max     float64
		desc    string
	}{
		"value_weight":        {0.4, "float", 0.0, 1.0, "Weight for value score (1/PE), normalized"},
		"quality_weight":      {0.3, "float", 0.0, 1.0, "Weight for quality score (ROE), normalized"},
		"momentum_weight":     {0.3, "float", 0.0, 1.0, "Weight for momentum score (return), normalized"},
		"lookback_days":       {60, "int", 5, 250, "Number of days for momentum lookback"},
		"top_n":               {10, "int", 1, 50, "Number of top stocks to buy"},
		"rebalance_frequency": {"monthly", "string", 0, 0, "Rebalance frequency: daily, weekly, monthly"},
	}

	if len(params) != len(expected) {
		t.Fatalf("expected %d parameters, got %d", len(expected), len(params))
	}

	for _, p := range params {
		e, ok := expected[p.Name]
		if !ok {
			t.Fatalf("unexpected parameter: %s", p.Name)
		}
		if p.Type != e.typeStr {
			t.Errorf("[%s] expected type %q, got %q", p.Name, e.typeStr, p.Type)
		}
		if p.Min != e.min {
			t.Errorf("[%s] expected min %v, got %v", p.Name, e.min, p.Min)
		}
		if p.Max != e.max {
			t.Errorf("[%s] expected max %v, got %v", p.Name, e.max, p.Max)
		}
		if p.Description != e.desc {
			t.Errorf("[%s] expected desc %q, got %q", p.Name, e.desc, p.Description)
		}
		switch d := e.def.(type) {
		case float64:
			if p.Default != d {
				t.Errorf("[%s] expected default %v, got %v", p.Name, d, p.Default)
			}
		case int:
			if p.Default != d {
				t.Errorf("[%s] expected default %v, got %v", p.Name, d, p.Default)
			}
		case string:
			if p.Default != d {
				t.Errorf("[%s] expected default %q, got %v", p.Name, d, p.Default)
			}
		}
	}
}

func TestMultiFactorNameDescription(t *testing.T) {
	s := &multiFactorStrategy{}
	if s.Name() != "multi_factor" {
		t.Errorf("expected name 'multi_factor', got %q", s.Name())
	}
	if s.Description() != "Multi-factor: rank stocks by weighted combination of value, quality, and momentum scores" {
		t.Errorf("unexpected description: %q", s.Description())
	}
}

func TestMultiFactorPercentileRank(t *testing.T) {
	tests := []struct {
		name   string
		input  []float64
		output []float64
	}{
		{
			name:   "simple three distinct values",
			input:  []float64{10.0, 20.0, 30.0},
			output: []float64{1.0 / 3.0, 2.0 / 3.0, 1.0},
		},
		{
			name:   "equal values get tied (averaged) rank",
			input:  []float64{1.0, 1.0, 1.0},
			output: []float64{2.0 / 3.0, 2.0 / 3.0, 2.0 / 3.0},
		},
		{
			name:   "two distinct values",
			input:  []float64{5.0, 10.0},
			output: []float64{0.5, 1.0},
		},
		{
			name:   "four values with ties at 20.0",
			input:  []float64{10.0, 20.0, 20.0, 30.0},
			output: []float64{0.25, 0.625, 0.625, 1.0},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ranks := rankPercentile(tc.input)
			if len(ranks) != len(tc.output) {
				t.Fatalf("expected %d ranks, got %d", len(tc.output), len(ranks))
			}
			for i := range tc.output {
				if math.Abs(ranks[i]-tc.output[i]) > 1e-9 {
					t.Errorf("rank[%d]: expected %v, got %v", i, tc.output[i], ranks[i])
				}
			}
		})
	}

	if r := rankPercentile(nil); r != nil {
		t.Errorf("expected nil for nil input, got %v", r)
	}
}

func TestMultiFactorCompositeScore(t *testing.T) {
	now := time.Now()
	basePrice := 100.0
	lookback := 60
	makeBars := func(symbol string, endPrice float64) []domain.OHLCV {
		startPrice := basePrice
		bars := make([]domain.OHLCV, lookback+2)
		for i := range bars {
			ratio := float64(i) / float64(len(bars)-1)
			bars[i] = domain.OHLCV{
				Symbol: symbol,
				Date:   now.AddDate(0, 0, -len(bars)+i),
				Close:  startPrice + (endPrice-startPrice)*ratio,
			}
		}
		return bars
	}

	// 4 票 + 未注入 factorReader：走本地价量路径（原先这条路会打 /screen）。
	bars := map[string][]domain.OHLCV{
		"A": makeBars("A", basePrice*1.10),
		"B": makeBars("B", basePrice*1.05),
		"C": makeBars("C", basePrice*1.01),
		"D": makeBars("D", basePrice*1.03),
	}

	s := &multiFactorStrategy{
		params: MultiFactorConfig{
			ValueWeight:        0.4,
			QualityWeight:      0.3,
			MomentumWeight:     0.3,
			LookbackDays:       lookback,
			TopN:               10,
			RebalanceFrequency: "daily",
		},
	}

	portfolio := &domain.Portfolio{UpdatedAt: now, Positions: map[string]domain.Position{}}
	signals, err := s.GenerateSignals(context.Background(), bars, portfolio)
	if err != nil {
		t.Fatalf("GenerateSignals failed: %v", err)
	}

	if len(signals) == 0 {
		t.Fatal("expected at least one buy signal")
	}

	// 原用例靠假 /screen 返回的 PE/ROE 决定排名；该路径已删除，这里只校验
	// 本地价量路径的结构性不变量：全部 4 票出买入信号、强度为正、按复合分降序。
	seen := map[string]bool{}
	for i, sig := range signals {
		seen[sig.Symbol] = true
		if sig.Strength <= 0 {
			t.Errorf("expected positive composite score for %s, got %v", sig.Symbol, sig.Strength)
		}
		if i > 0 && signals[i-1].Strength < sig.Strength {
			t.Errorf("signals not sorted by descending composite score: %v then %v", signals[i-1].Strength, sig.Strength)
		}
	}
	for _, want := range []string{"A", "B", "C", "D"} {
		if !seen[want] {
			t.Errorf("expected buy signal for %s, got %+v", want, signals)
		}
	}
}

func TestMultiFactorTopN(t *testing.T) {
	now := time.Now()
	basePrice := 100.0
	lookback := 60

	makeBars := func(symbol string, endPrice float64) []domain.OHLCV {
		startPrice := basePrice
		bars := make([]domain.OHLCV, lookback+2)
		for i := range bars {
			ratio := float64(i) / float64(len(bars)-1)
			bars[i] = domain.OHLCV{
				Symbol: symbol,
				Date:   now.AddDate(0, 0, -len(bars)+i),
				Close:  startPrice + (endPrice-startPrice)*ratio,
			}
		}
		return bars
	}

	tests := []struct {
		name     string
		topN     int
		wantBuys int
	}{
		{"topN=3 from 10 stocks", 3, 3},
		{"topN=5 from 10 stocks", 5, 5},
		{"topN=1 from 10 stocks", 1, 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bars := map[string][]domain.OHLCV{}
			for i := 0; i < 10; i++ {
				ret := 1.0 + float64(10-i)*0.01
				bars[fmt.Sprintf("S%02d", i)] = makeBars(fmt.Sprintf("S%02d", i), basePrice*ret)
			}

			s := &multiFactorStrategy{
				params: MultiFactorConfig{
					ValueWeight:        0.4,
					QualityWeight:      0.3,
					MomentumWeight:     0.3,
					LookbackDays:       lookback,
					TopN:               tc.topN,
					RebalanceFrequency: "daily",
				},
			}

			portfolio := &domain.Portfolio{UpdatedAt: now, Positions: map[string]domain.Position{}}
			signals, err := s.GenerateSignals(context.Background(), bars, portfolio)
			if err != nil {
				t.Fatalf("GenerateSignals failed: %v", err)
			}

			buyCount := 0
			for _, sig := range signals {
				if sig.Action == "buy" {
					buyCount++
				}
			}
			if buyCount != tc.wantBuys {
				t.Errorf("expected %d buy signals, got %d (signals: %+v)", tc.wantBuys, buyCount, signals)
			}
		})
	}
}

func TestMultiFactorEmptyBars(t *testing.T) {
	s := &multiFactorStrategy{}
	signals, err := s.GenerateSignals(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("expected no error with nil bars, got %v", err)
	}
	if signals != nil {
		t.Errorf("expected nil signals for nil bars, got %v", signals)
	}

	signals, err = s.GenerateSignals(context.Background(), map[string][]domain.OHLCV{}, nil)
	if err != nil {
		t.Fatalf("expected no error with empty bars, got %v", err)
	}
	if signals != nil {
		t.Errorf("expected nil signals for empty bars, got %v", signals)
	}
}

func TestMultiFactorImplementsStrategyInterface(t *testing.T) {
	var iface interface{} = &multiFactorStrategy{}
	_, ok := iface.(strategy.Strategy)
	if !ok {
		t.Error("multiFactorStrategy does not implement strategy.Strategy")
	}
}

func TestMultiFactorAutoRegister(t *testing.T) {
	// Save and restore original registry to avoid affecting other tests
	origRegistry := strategy.DefaultRegistry
	defer func() { strategy.DefaultRegistry = origRegistry }()
	strategy.DefaultRegistry = strategy.NewRegistry()

	s := &multiFactorStrategy{
		params: MultiFactorConfig{
			ValueWeight:        0.4,
			QualityWeight:      0.3,
			MomentumWeight:     0.3,
			LookbackDays:       60,
			TopN:               10,
			RebalanceFrequency: "monthly",
		},
	}
	if err := strategy.GlobalRegisterFactory(func() strategy.Strategy { return s }); err != nil {
		t.Fatalf("failed to register multi_factor: %v", err)
	}

	got, err := strategy.GlobalGet("multi_factor")
	if err != nil {
		t.Fatalf("multi_factor not found in registry: %v", err)
	}
	if got.Name() != "multi_factor" {
		t.Errorf("expected name 'multi_factor', got %q", got.Name())
	}
}

var _ strategy.Strategy = (*multiFactorStrategy)(nil)
var _ strategy.Strategy = (*valueScreeningStrategy)(nil)
