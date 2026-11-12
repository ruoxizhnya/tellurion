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

// setValueInputs 注入一组按 symbol 指定的估值 / ROE 快照。
// 取值 0 表示该字段 NULL 或缺失（用于反例），正数表示可比值。
func setValueInputs(
	s *valueScreeningStrategy, asOf time.Time,
	pe, pb, roe map[string]float64,
) {
	s.SetValuationReader(func(_ []string) map[string]domain.DailyBasicValuation {
		out := make(map[string]domain.DailyBasicValuation, len(pe))
		for symbol, v := range pe {
			val := domain.DailyBasicValuation{TsCode: symbol, TradeDate: asOf}
			if v > 0 {
				val.PE = floatPtr(v)
				val.PESource = domain.DailyBasicPESourceTTM
			}
			if p := pb[symbol]; p > 0 {
				val.PB = floatPtr(p)
				val.PBSource = domain.DailyBasicPBSourceReported
			}
			out[symbol] = val
		}
		return out
	})
	f := make(map[string]strategy.FundamentalSeries, len(roe))
	// 财报可用日取决策日前一天：测试里的 asOf 是「最新 bar 日期」的近似，
	// 落在同一天或更早都必须可用（PIT 口径是 available_date <= 决策日）。
	avail := asOf.AddDate(0, 0, -1)
	for symbol, v := range roe {
		if v == 0 {
			continue
		}
		val := v
		f[symbol] = strategy.FundamentalSeries{{Symbol: symbol, Date: avail, ROE: &val}}
	}
	s.SetFundamentals(f)
}

func floatPtr(v float64) *float64 { return &v }

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

	// 3 票：全部通过价值条件，用例只验证动量排名。
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
	setValueInputs(s, now,
		map[string]float64{"A": 10, "B": 12, "C": 15},
		map[string]float64{"A": 1, "B": 1.2, "C": 1.5},
		map[string]float64{"A": 0.2, "B": 0.2, "C": 0.2})

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
			setValueInputs(s, now,
				map[string]float64{"S00": 10, "S01": 12, "S02": 15},
				map[string]float64{"S00": 1, "S01": 1.2, "S02": 1.5},
				map[string]float64{"S00": 0.2, "S01": 0.2, "S02": 0.2})

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
	setValueInputs(s, now,
		map[string]float64{"A": 10, "B": 12, "C": 15},
		map[string]float64{"A": 1, "B": 1.2, "C": 1.5},
		map[string]float64{"A": 0.2, "B": 0.2, "C": 0.2})

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
		"daily_basic", // 缺哪张表
		"估值读取器未装配",    // 缺什么输入
		"均未生效",        // pe/pb/roe 三条条件全未生效
		"策略输入",        // 定性：策略输入不可得
		"不是行情数据缺失",    // 与行情缺失区分
		"不能退回纯动量选股",   // 说明没有假结果
		"pe<=25",      // 实际门槛值可读
		"pb<=2",
		"roe>=0.12",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected diagnostic message to contain %q, got:\n%s", want, msg)
		}
	}
}

// TestValueScreenZeroCoverageReportsGap 锁定要求 1：某一天全票池都没有可比 PE
// 时，这是数据缺口而不是「今天没有候选」。必须 ErrDataGap，消息点名日期、
// 缺的字段、覆盖数与总数；绝不能静默返回空选。
func TestValueScreenZeroCoverageReportsGap(t *testing.T) {
	now := time.Now()
	basePrice := 100.0
	momentumDays := 60
	makeBars := func(symbol string, endPrice float64) []domain.OHLCV {
		bars := make([]domain.OHLCV, momentumDays+2)
		for i := range bars {
			ratio := float64(i) / float64(len(bars)-1)
			bars[i] = domain.OHLCV{
				Symbol: symbol,
				Date:   now.AddDate(0, 0, -len(bars)+i),
				Close:  basePrice + (endPrice-basePrice)*ratio,
			}
		}
		return bars
	}
	bars := map[string][]domain.OHLCV{
		"A": makeBars("A", basePrice*1.10),
		"B": makeBars("B", basePrice*1.05),
	}

	s := &valueScreeningStrategy{
		params: ValueScreeningConfig{
			PEMax: 30, PBMax: 3, ROEMin: 0.05,
			MomentumDays: momentumDays, TopN: 2, RebalanceFrequency: "daily",
		},
	}
	// 读取器已装配，但当日一个可比字段都没有（全 NULL）。
	s.SetValuationReader(func(symbols []string) map[string]domain.DailyBasicValuation {
		out := make(map[string]domain.DailyBasicValuation, len(symbols))
		for _, sym := range symbols {
			out[sym] = domain.DailyBasicValuation{TsCode: sym, TradeDate: now}
		}
		return out
	})

	signals, err := s.GenerateSignals(context.Background(), bars,
		&domain.Portfolio{UpdatedAt: now, Positions: map[string]domain.Position{}})

	if !errors.Is(err, contracts.ErrDataGap) {
		t.Fatalf("expected ErrDataGap on zero coverage, got err=%v signals=%+v", err, signals)
	}
	if signals != nil {
		t.Errorf("expected no signals alongside ErrDataGap, got %+v", signals)
	}
	msg := err.Error()
	screenDay := bars["A"][len(bars["A"])-1].Date.Format("2006-01-02")
	for _, want := range []string{
		screenDay,            // 点名哪一天
		"market.daily_basic", // 点名哪张表
		"pe/pe_ttm",          // 点名缺哪个字段
		"0/2",                // 覆盖了几只票
		"pe<=30",             // 未生效的条件
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected zero-coverage gap message to contain %q, got:\n%s", want, msg)
		}
	}
}

// TestValueScreenLowCoverageReportsGap 锁定阈值口径：覆盖率 > 0 但低于
// valueCoverageMinRatio（50%）同样报缺口。1/20 只票有可比 PE 只是「数据最全
// 的那只」，不是可用横截面，与零覆盖同属 B1 假成功。
func TestValueScreenLowCoverageReportsGap(t *testing.T) {
	now := time.Now()
	momentumDays := 60
	const n = 4 // 1/4 = 25% < 50%
	bars := make(map[string][]domain.OHLCV, n)
	for i := 0; i < n; i++ {
		sym := fmt.Sprintf("S%d", i)
		series := make([]domain.OHLCV, momentumDays+2)
		for j := range series {
			series[j] = domain.OHLCV{
				Symbol: sym,
				Date:   now.AddDate(0, 0, -len(series)+j),
				Close:  100 + float64(i) + float64(j)*0.1,
			}
		}
		bars[sym] = series
	}

	s := &valueScreeningStrategy{
		params: ValueScreeningConfig{
			PEMax: 30, PBMax: 3, ROEMin: 0.05,
			MomentumDays: momentumDays, TopN: 4, RebalanceFrequency: "daily",
		},
	}
	setValueInputs(s, now,
		map[string]float64{"S0": 10}, // 只有 1/4 有可比 PE
		map[string]float64{"S0": 1, "S1": 1, "S2": 1, "S3": 1},
		map[string]float64{"S0": 0.2, "S1": 0.2, "S2": 0.2, "S3": 0.2})

	signals, err := s.GenerateSignals(context.Background(), bars,
		&domain.Portfolio{UpdatedAt: now, Positions: map[string]domain.Position{}})

	if !errors.Is(err, contracts.ErrDataGap) {
		t.Fatalf("expected ErrDataGap on low coverage (1/%d), got err=%v signals=%+v", n, err, signals)
	}
	if signals != nil {
		t.Errorf("expected no signals alongside ErrDataGap, got %+v", signals)
	}
	msg := err.Error()
	for _, want := range []string{"1/4", "最低要求 50%", "pe<=30"} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected low-coverage gap message to contain %q, got:\n%s", want, msg)
		}
	}
}

// TestValueScreenHasComparableDataButNoCandidate 是「零覆盖必须报 gap」的对偶：
// 可比数据齐全、只是三条条件筛完没有票时，允许正常的零候选（无错误无信号）。
func TestValueScreenHasComparableDataButNoCandidate(t *testing.T) {
	now := time.Now()
	momentumDays := 60
	makeBars := func(symbol string) []domain.OHLCV {
		bars := make([]domain.OHLCV, momentumDays+2)
		for i := range bars {
			bars[i] = domain.OHLCV{
				Symbol: symbol, Date: now.AddDate(0, 0, -len(bars)+i),
				Close: 100 + float64(i),
			}
		}
		return bars
	}
	bars := map[string][]domain.OHLCV{"A": makeBars("A"), "B": makeBars("B")}

	s := &valueScreeningStrategy{
		params: ValueScreeningConfig{
			PEMax: 5, PBMax: 1, ROEMin: 0.5, // 全部票都不满足
			MomentumDays: momentumDays, TopN: 2, RebalanceFrequency: "daily",
		},
	}
	setValueInputs(s, now,
		map[string]float64{"A": 10, "B": 12}, // 可比，但都 > pe_max
		map[string]float64{"A": 2, "B": 2},
		map[string]float64{"A": 0.1, "B": 0.1})

	signals, err := s.GenerateSignals(context.Background(), bars,
		&domain.Portfolio{UpdatedAt: now, Positions: map[string]domain.Position{}})
	if err != nil {
		t.Fatalf("comparable data with zero candidates must not be a data gap, got %v", err)
	}
	for _, sig := range signals {
		if sig.Action == "buy" {
			t.Errorf("expected zero buy signals, got %+v", sig)
		}
	}
}

// valueScreenFixture 构造 3 只票的统一夹具，供条件生效测试复用。
func valueScreenFixture(t *testing.T, now time.Time) (map[string][]domain.OHLCV, *valueScreeningStrategy) {
	t.Helper()
	momentumDays := 60
	makeBars := func(symbol string, endPrice float64) []domain.OHLCV {
		bars := make([]domain.OHLCV, momentumDays+2)
		for i := range bars {
			ratio := float64(i) / float64(len(bars)-1)
			bars[i] = domain.OHLCV{
				Symbol: symbol,
				Date:   now.AddDate(0, 0, -len(bars)+i),
				Close:  100 + (endPrice-100)*ratio,
			}
		}
		return bars
	}
	bars := map[string][]domain.OHLCV{
		"A": makeBars("A", 110), // 动量最高
		"B": makeBars("B", 105),
		"C": makeBars("C", 101),
	}
	s := &valueScreeningStrategy{
		params: ValueScreeningConfig{
			PEMax: 30, PBMax: 3, ROEMin: 0.05,
			MomentumDays: momentumDays, TopN: 3, RebalanceFrequency: "daily",
		},
	}
	return bars, s
}

func buySymbolsOf(signals []strategy.Signal) map[string]bool {
	out := map[string]bool{}
	for _, sig := range signals {
		if sig.Action == "buy" {
			out[sig.Symbol] = true
		}
	}
	return out
}

// TestValueScreenPEConditionChangesSelection 证明 pe<= 真的生效：
// 收紧 pe_max 会把 PE 较高的票排除，选股结果随之改变。
func TestValueScreenPEConditionChangesSelection(t *testing.T) {
	now := time.Now()
	bars, s := valueScreenFixture(t, now)
	setValueInputs(s, now,
		map[string]float64{"A": 10, "B": 20, "C": 30},
		map[string]float64{"A": 1, "B": 1, "C": 1},
		map[string]float64{"A": 0.2, "B": 0.2, "C": 0.2})

	loose, err := s.GenerateSignals(context.Background(), bars,
		&domain.Portfolio{UpdatedAt: now, Positions: map[string]domain.Position{}})
	if err != nil {
		t.Fatalf("loose pe_max: %v", err)
	}
	if got := buySymbolsOf(loose); !got["A"] || !got["B"] || !got["C"] {
		t.Fatalf("pe_max=30 should include A/B/C, got %v", got)
	}

	s.params.PEMax = 15 // 只剩 A（PE=10）
	tight, err := s.GenerateSignals(context.Background(), bars,
		&domain.Portfolio{UpdatedAt: now, Positions: map[string]domain.Position{}})
	if err != nil {
		t.Fatalf("tight pe_max: %v", err)
	}
	got := buySymbolsOf(tight)
	if !got["A"] || got["B"] || got["C"] {
		t.Fatalf("pe_max=15 should only select A, got %v", got)
	}
}

// TestValueScreenPBConditionChangesSelection 证明 pb<= 真的生效。
func TestValueScreenPBConditionChangesSelection(t *testing.T) {
	now := time.Now()
	bars, s := valueScreenFixture(t, now)
	setValueInputs(s, now,
		map[string]float64{"A": 10, "B": 10, "C": 10},
		map[string]float64{"A": 1, "B": 2, "C": 3},
		map[string]float64{"A": 0.2, "B": 0.2, "C": 0.2})

	loose, err := s.GenerateSignals(context.Background(), bars,
		&domain.Portfolio{UpdatedAt: now, Positions: map[string]domain.Position{}})
	if err != nil {
		t.Fatalf("loose pb_max: %v", err)
	}
	if got := buySymbolsOf(loose); !got["A"] || !got["B"] || !got["C"] {
		t.Fatalf("pb_max=3 should include A/B/C, got %v", got)
	}

	s.params.PBMax = 1.5
	tight, err := s.GenerateSignals(context.Background(), bars,
		&domain.Portfolio{UpdatedAt: now, Positions: map[string]domain.Position{}})
	if err != nil {
		t.Fatalf("tight pb_max: %v", err)
	}
	got := buySymbolsOf(tight)
	if !got["A"] || got["B"] || got["C"] {
		t.Fatalf("pb_max=1.5 should only select A, got %v", got)
	}
}

// TestValueScreenROEConditionChangesSelection 证明 roe>= 真的生效。
func TestValueScreenROEConditionChangesSelection(t *testing.T) {
	now := time.Now()
	bars, s := valueScreenFixture(t, now)
	setValueInputs(s, now,
		map[string]float64{"A": 10, "B": 10, "C": 10},
		map[string]float64{"A": 1, "B": 1, "C": 1},
		map[string]float64{"A": 0.30, "B": 0.20, "C": 0.10})

	loose, err := s.GenerateSignals(context.Background(), bars,
		&domain.Portfolio{UpdatedAt: now, Positions: map[string]domain.Position{}})
	if err != nil {
		t.Fatalf("loose roe_min: %v", err)
	}
	if got := buySymbolsOf(loose); !got["A"] || !got["B"] || !got["C"] {
		t.Fatalf("roe_min=0.05 should include A/B/C, got %v", got)
	}

	s.params.ROEMin = 0.25
	tight, err := s.GenerateSignals(context.Background(), bars,
		&domain.Portfolio{UpdatedAt: now, Positions: map[string]domain.Position{}})
	if err != nil {
		t.Fatalf("tight roe_min: %v", err)
	}
	got := buySymbolsOf(tight)
	if !got["A"] || got["B"] || got["C"] {
		t.Fatalf("roe_min=0.25 should only select A, got %v", got)
	}
}

// TestValueScreenProvenanceReportsPESourceAndCoverage 锁定要求 4：
// 信号必须能复盘价值维度是否参与、用的 pe_ttm 还是回退 pe、当日可比覆盖数。
func TestValueScreenProvenanceReportsPESourceAndCoverage(t *testing.T) {
	now := time.Now()
	bars, s := valueScreenFixture(t, now)

	// A 用 pe_ttm；B 缺 pe_ttm（NULL）回退 pe；C 全有。
	s.SetValuationReader(func(symbols []string) map[string]domain.DailyBasicValuation {
		out := map[string]domain.DailyBasicValuation{}
		for _, sym := range symbols {
			pe := 10.0
			pb := 1.0
			src := domain.DailyBasicPESourceTTM
			if sym == "B" {
				src = domain.DailyBasicPESourceReported
			}
			out[sym] = domain.DailyBasicValuation{
				TsCode: sym, TradeDate: now.AddDate(0, 0, -1),
				PE: &pe, PESource: src, PB: &pb, PBSource: domain.DailyBasicPBSourceReported,
			}
		}
		return out
	})
	setValueInputsForROEOnly(s, now, map[string]float64{"A": 0.2, "B": 0.2, "C": 0.2})

	signals, err := s.GenerateSignals(context.Background(), bars,
		&domain.Portfolio{UpdatedAt: now, Positions: map[string]domain.Position{}})
	if err != nil {
		t.Fatalf("GenerateSignals: %v", err)
	}

	bySymbol := map[string]strategy.Signal{}
	for _, sig := range signals {
		if sig.Action == "buy" {
			bySymbol[sig.Symbol] = sig
		}
	}
	for _, sym := range []string{"A", "B", "C"} {
		sig, ok := bySymbol[sym]
		if !ok {
			t.Fatalf("expected buy signal for %s, got %+v", sym, bySymbol)
		}
		if sig.Metadata["value_dimension"] != "local_screen" {
			t.Errorf("%s: expected value_dimension=local_screen, got %v", sym, sig.Metadata["value_dimension"])
		}
		if sig.Metadata["pe_covered"] != 3 || sig.Metadata["pb_covered"] != 3 || sig.Metadata["roe_covered"] != 3 {
			t.Errorf("%s: expected coverage 3/3/3, got pe=%v pb=%v roe=%v",
				sym, sig.Metadata["pe_covered"], sig.Metadata["pb_covered"], sig.Metadata["roe_covered"])
		}
		if sig.Metadata["valuation_trade_date"] != now.AddDate(0, 0, -1).Format("2006-01-02") {
			t.Errorf("%s: expected valuation_trade_date to be yesterday, got %v",
				sym, sig.Metadata["valuation_trade_date"])
		}
	}

	if got := bySymbol["A"].Metadata["pe_source"]; got != domain.DailyBasicPESourceTTM {
		t.Errorf("A: expected pe_source=pe_ttm, got %v", got)
	}
	if got := bySymbol["A"].Metadata["pe_ttm_fallback"]; got != false {
		t.Errorf("A: expected pe_ttm_fallback=false, got %v", got)
	}
	if got := bySymbol["B"].Metadata["pe_source"]; got != domain.DailyBasicPESourceReported {
		t.Errorf("B: expected pe_source=pe (fallback), got %v", got)
	}
	if got := bySymbol["B"].Metadata["pe_ttm_fallback"]; got != true {
		t.Errorf("B: expected pe_ttm_fallback=true, got %v", got)
	}
}

// setValueInputsForROEOnly 只注入 ROE（估值由自定义读取器提供）。
func setValueInputsForROEOnly(s *valueScreeningStrategy, asOf time.Time, roe map[string]float64) {
	f := make(map[string]strategy.FundamentalSeries, len(roe))
	avail := asOf.AddDate(0, 0, -1)
	for symbol, v := range roe {
		val := v
		f[symbol] = strategy.FundamentalSeries{{Symbol: symbol, Date: avail, ROE: &val}}
	}
	s.SetFundamentals(f)
}

// TestValueScreenPINoFutureData 证明 PIT 口径：估值读取器不得返回决策日之后
// 的记录，ROE 只用可用日 <= 决策日的最新一期（未来财报不参与）。
func TestValueScreenPINoFutureData(t *testing.T) {
	now := time.Now()
	bars, s := valueScreenFixture(t, now)
	setValueInputs(s, now,
		map[string]float64{"A": 10, "B": 10, "C": 10},
		map[string]float64{"A": 1, "B": 1, "C": 1},
		map[string]float64{})

	// C 的最近一期是未来财报（ROE 很高），决策日不可用；可用日 <= 决策日
	// 的那一期 ROE=0.01，低于门槛 —— C 必须被排除，不能吃未来数据。
	past := 0.01
	future := 0.99
	s.SetFundamentals(map[string]strategy.FundamentalSeries{
		"A": {{Symbol: "A", Date: now.AddDate(0, 0, -30), ROE: floatPtr(0.2)}},
		"B": {{Symbol: "B", Date: now.AddDate(0, 0, -30), ROE: floatPtr(0.2)}},
		"C": {
			{Symbol: "C", Date: now.AddDate(0, 0, -30), ROE: &past},
			{Symbol: "C", Date: now.AddDate(0, 0, 5), ROE: &future},
		},
	})

	signals, err := s.GenerateSignals(context.Background(), bars,
		&domain.Portfolio{UpdatedAt: now, Positions: map[string]domain.Position{}})
	if err != nil {
		t.Fatalf("GenerateSignals: %v", err)
	}
	got := buySymbolsOf(signals)
	if got["C"] {
		t.Errorf("C must be excluded: only its future report has ROE>=min (look-ahead leak)")
	}
	if !got["A"] || !got["B"] {
		t.Errorf("expected A and B selected, got %v", got)
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
	setValueInputs(s, now,
		map[string]float64{"A": 10, "B": 12},
		map[string]float64{"A": 1, "B": 1.2},
		map[string]float64{"A": 0.2, "B": 0.2})

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
