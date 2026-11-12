package plugins

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	contracts "github.com/ruoxizhnya/tellurion-contracts/engine"
	"tellurion/pkg/domain"
	"tellurion/pkg/strategy"
)

func TestValueScreenParameters(t *testing.T) {
	s := &valueScreeningStrategy{
		params: ValueScreeningConfig{
			PEMax:              30.0,
			PBMax:              3.0,
			ROEMin:             0.1,
			MomentumDays:       60,
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
		"pe_max":              {30.0, "float", 1.0, 100.0, "Maximum PE ratio to include"},
		"pb_max":              {3.0, "float", 0.1, 20.0, "Maximum PB ratio to include"},
		"roe_min":             {0.1, "float", -1.0, 1.0, "Minimum ROE to include (e.g., 0.1 = 10%)"},
		"momentum_days":       {60, "int", 5, 250, "Number of days for momentum lookback"},
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

func TestValueScreenNameDescription(t *testing.T) {
	s := &valueScreeningStrategy{}
	if s.Name() != "value_screening" {
		t.Errorf("expected name 'value_screening', got %q", s.Name())
	}
	if s.Description() != "Value screening: filter stocks by PE, PB, ROE criteria then rank by momentum" {
		t.Errorf("unexpected description: %q", s.Description())
	}
}

func TestValueScreenMomentumRanking(t *testing.T) {
	now := time.Now()
	basePrice := 100.0
	momentumDays := 60

	makeBars := func(symbol string, endPrice float64) []domain.OHLCV {
		startPrice := basePrice
		bars := make([]domain.OHLCV, momentumDays+2)
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

	// 3 票走本地动量路径（多票价值筛选已是 ErrDataGap，见
	// TestValueScreenMultiStockFailsWithDiagnosableDataGap）。
	bars := map[string][]domain.OHLCV{
		"A": makeBars("A", basePrice*1.10),
		"B": makeBars("B", basePrice*1.05),
		"C": makeBars("C", basePrice*1.01),
	}

	s := &valueScreeningStrategy{
		params: ValueScreeningConfig{
			PEMax:              30.0,
			PBMax:              3.0,
			ROEMin:             0.05,
			MomentumDays:       momentumDays,
			TopN:               2,
			RebalanceFrequency: "daily",
		},
	}

	portfolio := &domain.Portfolio{
		UpdatedAt: now,
		Positions: map[string]domain.Position{},
	}

	signals, err := s.GenerateSignals(context.Background(), bars, portfolio)
	if err != nil {
		t.Fatalf("GenerateSignals failed: %v", err)
	}

	buySymbols := make(map[string]bool)
	for _, sig := range signals {
		if sig.Action == "buy" {
			buySymbols[sig.Symbol] = true
		}
	}

	if !buySymbols["A"] {
		t.Error("expected stock A (highest momentum) to get buy signal")
	}
	if !buySymbols["B"] {
		t.Error("expected stock B (second highest momentum) to get buy signal")
	}
	if buySymbols["C"] {
		t.Error("stock C (lowest momentum) should NOT get buy signal when topN=2")
	}

	var strengthA, strengthB float64
	for _, sig := range signals {
		if sig.Symbol == "A" {
			strengthA = sig.Strength
		}
		if sig.Symbol == "B" {
			strengthB = sig.Strength
		}
	}
	if strengthA <= strengthB {
		t.Errorf("expected A's strength (%v) > B's (%v) since A has higher momentum", strengthA, strengthB)
	}
}

func TestValueScreenTopN(t *testing.T) {
	momentumDays := 60
	now := time.Now()
	basePrice := 100.0

	makeBars := func(symbol string, endPrice float64) []domain.OHLCV {
		startPrice := basePrice
		bars := make([]domain.OHLCV, momentumDays+2)
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
		{"topN=1 from 3 stocks", 1, 1},
		{"topN=2 from 3 stocks", 2, 2},
		{"topN=3 from 3 stocks", 3, 3},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bars := map[string][]domain.OHLCV{}
			for i := 0; i < 3; i++ {
				ret := 1.0 + float64(3-i)*0.01
				bars[fmt.Sprintf("S%02d", i)] = makeBars(fmt.Sprintf("S%02d", i), basePrice*ret)
			}

			s := &valueScreeningStrategy{
				params: ValueScreeningConfig{
					PEMax:              30.0,
					PBMax:              3.0,
					ROEMin:             0.01,
					MomentumDays:       momentumDays,
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

func TestValueScreenSellSignal(t *testing.T) {
	momentumDays := 60
	now := time.Now()
	basePrice := 100.0
	makeBars := func(symbol string, endPrice float64) []domain.OHLCV {
		startPrice := basePrice
		bars := make([]domain.OHLCV, momentumDays+2)
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

	bars := map[string][]domain.OHLCV{
		"A": makeBars("A", basePrice*1.10),
		"B": makeBars("B", basePrice*1.05),
		"C": makeBars("C", basePrice*1.01),
	}

	portfolio := &domain.Portfolio{
		UpdatedAt: now,
		Positions: map[string]domain.Position{
			"A": {Symbol: "A"},
			"B": {Symbol: "B"},
			"C": {Symbol: "C"},
		},
	}

	s := &valueScreeningStrategy{
		params: ValueScreeningConfig{
			PEMax:              30.0,
			PBMax:              3.0,
			ROEMin:             0.05,
			MomentumDays:       momentumDays,
			TopN:               2,
			RebalanceFrequency: "daily",
		},
	}

	signals, err := s.GenerateSignals(context.Background(), bars, portfolio)
	if err != nil {
		t.Fatalf("GenerateSignals failed: %v", err)
	}

	sellSymbols := make(map[string]bool)
	for _, sig := range signals {
		if sig.Action == "sell" {
			sellSymbols[sig.Symbol] = true
		}
	}

	if !sellSymbols["C"] {
		t.Error("expected stock C (out of top N) to get sell signal")
	}
	if sellSymbols["A"] {
		t.Error("stock A (in top N) should NOT get sell signal")
	}
	if sellSymbols["B"] {
		t.Error("stock B (in top N) should NOT get sell signal")
	}
}

// TestValueScreenMultiStockFailsWithDiagnosableDataGap 锁定 B1 语义：多票模式的
// 价值筛选输入不可得时，必须返回可诊断的 ErrDataGap（引擎记为 data_gap_skipped、
// 终态 invalid），而不是静默退回纯动量选股产出"看起来有数"的假结果。
func TestValueScreenMultiStockFailsWithDiagnosableDataGap(t *testing.T) {
	now := time.Now()
	basePrice := 100.0
	momentumDays := 60
	makeBars := func(symbol string, endPrice float64) []domain.OHLCV {
		startPrice := basePrice
		bars := make([]domain.OHLCV, momentumDays+2)
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

	bars := map[string][]domain.OHLCV{
		"A": makeBars("A", basePrice*1.10),
		"B": makeBars("B", basePrice*1.05),
		"C": makeBars("C", basePrice*1.01),
		"D": makeBars("D", basePrice*1.03),
	}

	s := &valueScreeningStrategy{
		params: ValueScreeningConfig{
			PEMax:              25.0,
			PBMax:              2.0,
			ROEMin:             0.12,
			MomentumDays:       momentumDays,
			TopN:               2,
			RebalanceFrequency: "daily",
		},
	}

	portfolio := &domain.Portfolio{UpdatedAt: now, Positions: map[string]domain.Position{}}
	signals, err := s.GenerateSignals(context.Background(), bars, portfolio)

	if err == nil {
		t.Fatalf("expected ErrDataGap for multi-stock value screening, got nil error with %d signals", len(signals))
	}
	if signals != nil {
		t.Errorf("expected no signals alongside ErrDataGap, got %+v", signals)
	}
	if !errors.Is(err, contracts.ErrDataGap) {
		t.Fatalf("expected errors.Is(err, contracts.ErrDataGap), got %v", err)
	}

	msg := err.Error()
	for _, want := range []string{
		"market.stock_fundamentals", // 缺哪张表
		"pe",                        // 缺哪个字段
		"pb",
		"roe",
		"策略输入",      // 定性：策略输入不可得
		"不是行情数据缺失",  // 与行情缺失区分
		"一条都未生效",    // 哪条筛选条件未生效
		"不能退回纯动量选股", // 说明没有假结果
		"pe<=25",    // 实际生效值可读
		"pb<=2",
		"roe>=0.12",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected diagnostic message to contain %q, got:\n%s", want, msg)
		}
	}
}

func TestValueScreenEmptyBars(t *testing.T) {
	s := &valueScreeningStrategy{}
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

func TestValueScreenZeroMomentum(t *testing.T) {
	now := time.Now()
	price := 100.0
	momentumDays := 60

	makeFlatBars := func(symbol string) []domain.OHLCV {
		bars := make([]domain.OHLCV, momentumDays+2)
		for i := range bars {
			bars[i] = domain.OHLCV{
				Symbol: symbol,
				Date:   now.AddDate(0, 0, -len(bars)+i),
				Close:  price,
			}
		}
		return bars
	}

	bars := map[string][]domain.OHLCV{
		"A": makeFlatBars("A"),
		"B": makeFlatBars("B"),
	}

	s := &valueScreeningStrategy{
		params: ValueScreeningConfig{
			PEMax:              30.0,
			PBMax:              3.0,
			ROEMin:             0.05,
			MomentumDays:       momentumDays,
			TopN:               10,
			RebalanceFrequency: "daily",
		},
	}

	portfolio := &domain.Portfolio{UpdatedAt: now, Positions: map[string]domain.Position{}}
	signals, err := s.GenerateSignals(context.Background(), bars, portfolio)
	if err != nil {
		t.Fatalf("GenerateSignals failed: %v", err)
	}

	for _, sig := range signals {
		if sig.Action == "buy" && sig.Strength <= 0 {
			t.Errorf("stock %s has zero/negative momentum, should not get buy signal", sig.Symbol)
		}
	}
}

func TestValueScreenImplementsStrategyInterface(t *testing.T) {
	var iface interface{} = &valueScreeningStrategy{}
	_, ok := iface.(strategy.Strategy)
	if !ok {
		t.Error("valueScreeningStrategy does not implement strategy.Strategy")
	}
}

func TestValueScreenAutoRegister(t *testing.T) {
	// Save and restore original registry to avoid affecting other tests
	origRegistry := strategy.DefaultRegistry
	defer func() { strategy.DefaultRegistry = origRegistry }()
	strategy.DefaultRegistry = strategy.NewRegistry()

	s := &valueScreeningStrategy{
		params: ValueScreeningConfig{
			PEMax:              30.0,
			PBMax:              3.0,
			ROEMin:             0.1,
			MomentumDays:       60,
			TopN:               10,
			RebalanceFrequency: "monthly",
		},
	}
	if err := strategy.GlobalRegisterFactory(func() strategy.Strategy { return s }); err != nil {
		t.Fatalf("failed to register value_screening: %v", err)
	}

	got, err := strategy.GlobalGet("value_screening")
	if err != nil {
		t.Fatalf("value_screening not found in registry: %v", err)
	}
	if got.Name() != "value_screening" {
		t.Errorf("expected name 'value_screening', got %q", got.Name())
	}
}
