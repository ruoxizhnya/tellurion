// Package plugins contains built-in strategy implementations.
package plugins

import (
	"context"
	"fmt"
	"sort"
	"time"

	contracts "github.com/ruoxizhnya/tellurion-contracts/engine"
	"tellurion/pkg/domain"
	"tellurion/pkg/strategy"
)

// ValueScreeningConfig holds configuration for the value screening strategy.
type ValueScreeningConfig struct {
	PEMax              float64
	PBMax              float64
	ROEMin             float64
	MomentumDays       int
	TopN               int
	RebalanceFrequency string
}

// valueCoverageMinRatio 是当日「可比覆盖」的最低可接受比例。
//
// 覆盖率 0 必报数据缺口；覆盖率 > 0 但低于本比例同样报缺口。理由：价值
// 筛选是横截面策略，若 20 只票里只有 1 只有可比 PE，选出来的是「数据最全
// 的那只」而不是「池子里最便宜的」，与静默空选同属假成功 —— 这正是 B1
// 要消灭的失效模式。50% 允许亏损股（pe_ttm 为 NULL）等正常缺失占少数。
const valueCoverageMinRatio = 0.5

// valueScreeningStrategy screens stocks by value metrics (PE, PB, ROE)
// and ranks filtered stocks by price momentum.
type valueScreeningStrategy struct {
	*strategy.BaseStrategy
	params ValueScreeningConfig
	// valuationReader 是引擎按回放日注入的 PIT 估值读取器
	// （market.daily_basic，trade_date <= 决策日）。nil = 未装配。
	valuationReader strategy.ValuationReader
	// fundamentals 是 run 内财报快照（available_date 升序），ROE 条件用它。
	fundamentals map[string]strategy.FundamentalSeries
}

// Configure sets the strategy parameters.
func (s *valueScreeningStrategy) Configure(params map[string]any) error {
	s.Lock()
	defer s.Unlock()
	if v, ok := params["pe_max"]; ok {
		if val, ok := parseFloatParam(v); ok {
			s.params.PEMax = val
		}
	}
	if v, ok := params["pb_max"]; ok {
		if val, ok := parseFloatParam(v); ok {
			s.params.PBMax = val
		}
	}
	if v, ok := params["roe_min"]; ok {
		if val, ok := parseFloatParam(v); ok {
			s.params.ROEMin = val
		}
	}
	if v, ok := params["momentum_days"]; ok {
		if val, ok := parseIntParam(v); ok {
			s.params.MomentumDays = val
		}
	}
	if v, ok := params["top_n"]; ok {
		if val, ok := parseIntParam(v); ok {
			s.params.TopN = val
		}
	}
	if v, ok := params["rebalance_frequency"]; ok {
		if val, ok := parseStringParam(v); ok {
			s.params.RebalanceFrequency = val
		}
	}
	return nil
}

// Weight returns position weight based on signal strength and composite score.
// For value screening: weight proportional to strength (capped at 0.05).
func (s *valueScreeningStrategy) Weight(signal strategy.Signal, portfolioValue float64) float64 {
	baseWeight := 1.0 / float64(s.params.TopN)
	weight := signal.Strength * baseWeight
	if weight > 0.05 {
		weight = 0.05
	}
	if weight < 0.01 {
		weight = 0.01
	}
	return weight
}

// SetValuationReader 实现 strategy.ValuationAware：接收按回放日绑定的
// PIT 估值读取器（引擎在预热后经 LocalSignalProvider 注入）。
func (s *valueScreeningStrategy) SetValuationReader(reader strategy.ValuationReader) {
	s.valuationReader = reader
}

// SetFundamentals 实现 strategy.FundamentalAware：接收 run 内财报快照，
// ROE 条件从最近一期（可用日 <= 决策日）读取。
func (s *valueScreeningStrategy) SetFundamentals(records map[string]strategy.FundamentalSeries) {
	s.fundamentals = records
}

// Cleanup releases resources.
func (s *valueScreeningStrategy) Cleanup() {
	s.params = ValueScreeningConfig{}
	s.valuationReader = nil
	s.fundamentals = nil
}

// Name returns the strategy name.
func (s *valueScreeningStrategy) Name() string {
	return "value_screening"
}

// Description returns the strategy description.
func (s *valueScreeningStrategy) Description() string {
	return "Value screening: filter stocks by PE, PB, ROE criteria then rank by momentum"
}

// Parameters returns the configurable parameters.
func (s *valueScreeningStrategy) Parameters() []strategy.Parameter {
	return []strategy.Parameter{
		{
			Name:        "pe_max",
			Type:        "float",
			Default:     30.0,
			Description: "Maximum PE ratio to include",
			Min:         1.0,
			Max:         100.0,
		},
		{
			Name:        "pb_max",
			Type:        "float",
			Default:     3.0,
			Description: "Maximum PB ratio to include",
			Min:         0.1,
			Max:         20.0,
		},
		{
			Name:        "roe_min",
			Type:        "float",
			Default:     0.1,
			Description: "Minimum ROE to include (e.g., 0.1 = 10%)",
			Min:         -1.0,
			Max:         1.0,
		},
		{
			Name:        "momentum_days",
			Type:        "int",
			Default:     60,
			Description: "Number of days for momentum lookback",
			Min:         5,
			Max:         250,
		},
		{
			Name:        "top_n",
			Type:        "int",
			Default:     10,
			Description: "Number of top stocks to buy",
			Min:         1,
			Max:         50,
		},
		{
			Name:        "rebalance_frequency",
			Type:        "string",
			Default:     "monthly",
			Description: "Rebalance frequency: daily, weekly, monthly",
		},
	}
}

// GenerateSignals generates buy/sell signals based on value screening + momentum ranking.
func (s *valueScreeningStrategy) GenerateSignals(
	ctx context.Context,
	bars map[string][]domain.OHLCV,
	portfolio *domain.Portfolio,
) ([]strategy.Signal, error) {
	if len(bars) == 0 {
		return nil, nil
	}

	// Apply defaults
	peMax := s.params.PEMax
	if peMax <= 0 {
		peMax = 30
	}
	pbMax := s.params.PBMax
	if pbMax <= 0 {
		pbMax = 3
	}
	roeMin := s.params.ROEMin
	if roeMin == 0 {
		roeMin = 0.1 // default 10%
	}
	momentumDays := s.params.MomentumDays
	if momentumDays <= 0 {
		momentumDays = 60
	}
	// For single/few stocks, use smaller lookback to ensure signals are generated
	if len(bars) <= 3 && momentumDays > 20 {
		momentumDays = 20
	}
	topN := s.params.TopN
	if topN <= 0 {
		topN = 10
	}

	// Determine the date for screening and rebalance check
	// Use the latest date from bars data (for backtesting) or portfolio.UpdatedAt or now
	var screenDate time.Time
	for _, data := range bars {
		if len(data) > 0 {
			latest := data[len(data)-1].Date
			if latest.After(screenDate) {
				screenDate = latest
			}
		}
	}
	if screenDate.IsZero() {
		if portfolio != nil && !portfolio.UpdatedAt.IsZero() {
			screenDate = portfolio.UpdatedAt
		} else {
			// 同 multi_factor：没有日期就不发信号，不用墙钟（P1-12）。
			return nil, nil
		}
	}

	// Check rebalance frequency
	// For single/few stocks, always rebalance to ensure signals are generated
	rebalanceFreq := s.params.RebalanceFrequency
	if len(bars) <= 3 {
		rebalanceFreq = "daily"
	}
	if !strategy.IsRebalanceDay(screenDate, rebalanceFreq) {
		// Not a rebalance day: return sell signals only for positions that
		// should be exited (momentum turned negative), no new buys
		sellSignals, err := s.generateSellSignals(bars, portfolio, momentumDays)
		if err != nil {
			return nil, fmt.Errorf("value screen sell signal generation failed: %w", err)
		}
		return sellSignals, nil
	}

	// 逐票计算动量（仅行情面，不含价值判断）。价值条件在下面统一筛选，
	// 单票/多票走同一条路径 —— 不再有「票少就跳过价值条件」的旁路。
	type stockMomentum struct {
		symbol   string
		momentum float64
	}
	var momResults []stockMomentum
	universe := make([]string, 0, len(bars))
	for symbol, data := range bars {
		if len(data) < momentumDays+1 {
			continue
		}
		sorted := sortOHLCV(data)

		endIdx := len(sorted) - 1
		if endIdx < momentumDays {
			continue
		}

		startPrice := sorted[endIdx-momentumDays].Close
		endPrice := sorted[endIdx].Close
		if startPrice <= 0 {
			continue
		}

		momResults = append(momResults, stockMomentum{
			symbol:   symbol,
			momentum: (endPrice - startPrice) / startPrice,
		})
		universe = append(universe, symbol)
	}
	if len(universe) == 0 {
		// 没有任何票有足够行情历史：这是行情面问题，不是价值维度缺口；
		// 引擎的行情预热会先报。这里保持「无候选」。
		return nil, nil
	}

	valuations, cov, err := s.loadValuations(universe, screenDate, peMax, pbMax, roeMin)
	if err != nil {
		return nil, err
	}

	// 价值筛选：pe<= / pb<= / roe>= 三条同时生效。
	// 不可比（NULL、非正）的票当日不参与，而不是被当成最便宜。
	candidates := make([]stockMomentum, 0, len(momResults))
	for _, m := range momResults {
		v, ok := valuations[m.symbol]
		if !ok {
			continue
		}
		if v.PE == nil || *v.PE <= 0 || *v.PE > peMax {
			continue
		}
		if v.PB == nil || *v.PB <= 0 || *v.PB > pbMax {
			continue
		}
		roe, ok := latestROEOnOrBefore(s.fundamentals[m.symbol], screenDate)
		if !ok || roe < roeMin {
			continue
		}
		candidates = append(candidates, m)
	}

	// Sort by momentum descending (highest return = rank 1)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].momentum > candidates[j].momentum
	})

	// Generate buy signals for top N with positive momentum
	n := len(candidates)
	if n > topN {
		n = topN
	}

	var signals []strategy.Signal
	for i := 0; i < n; i++ {
		// Use absolute momentum for strength, but allow negative momentum stocks
		// to be considered (they may be value opportunities)
		strength := candidates[i].momentum
		if strength < 0 {
			strength = -strength * 0.5 // Negative momentum gets lower strength
		}
		if strength <= 0 {
			continue
		}

		symbol := candidates[i].symbol
		v := valuations[symbol]
		roe, _ := latestROEOnOrBefore(s.fundamentals[symbol], screenDate)
		price := getLatestPrice(bars[symbol])

		// provenance：价值维度参与了、用的是什么 PE 口径、当日可比覆盖多少。
		// 这些字段必须能复盘「条件是否真的生效」，否则下次静默失效仍查不出来。
		signals = append(signals, strategy.Signal{
			Symbol:   symbol,
			Action:   "buy",
			Strength: strength,
			Price:    price,
			Date:     screenDate,
			Factors: map[string]float64{
				"pe":       *v.PE,
				"pb":       *v.PB,
				"roe":      roe,
				"momentum": candidates[i].momentum,
			},
			Metadata: map[string]interface{}{
				"value_dimension":      "local_screen",
				"pe_source":            v.PESource,
				"pe_ttm_fallback":      v.PESource == domain.DailyBasicPESourceReported,
				"valuation_trade_date": v.TradeDate.Format("2006-01-02"),
				"universe":             cov.Universe,
				"pe_covered":           cov.PE,
				"pb_covered":           cov.PB,
				"roe_covered":          cov.ROE,
				"thresholds": map[string]float64{
					"pe_max": peMax, "pb_max": pbMax, "roe_min": roeMin,
				},
			},
		})
	}

	// Generate sell signals for held positions not in top N
	if portfolio != nil {
		for symbol := range portfolio.Positions {
			// Check if this position is still in our top N
			inTopN := false
			for i := 0; i < n; i++ {
				if candidates[i].symbol == symbol {
					inTopN = true
					break
				}
			}
			// Also sell if it didn't pass screening
			if !inTopN {
				// Not in top momentum stocks, sell
				if _, ok := bars[symbol]; ok {
					price := getLatestPrice(bars[symbol])
					signals = append(signals, strategy.Signal{
						Symbol:   symbol,
						Action:   "sell",
						Strength: 1.0,
						Price:    price,
						Date:     screenDate,
					})
				}
			}
		}
	}

	return signals, nil
}

// valueCoverage 是某一决策日价值维度的可比覆盖。
type valueCoverage struct {
	Universe int
	PE       int
	PB       int
	ROE      int
}

// loadValuations 读取当日 PIT 估值并校验可比覆盖。
//
// 0 覆盖或低于 valueCoverageMinRatio 都返回 contract 层可识别的
// ErrDataGap，消息点名日期、表/字段、覆盖率与未生效的条件。只有
// 「有可比数据但筛完确实没有票」才是正常的零候选。
func (s *valueScreeningStrategy) loadValuations(
	universe []string, screenDate time.Time, peMax, pbMax, roeMin float64,
) (map[string]domain.DailyBasicValuation, valueCoverage, error) {
	cov := valueCoverage{Universe: len(universe)}
	if s.valuationReader == nil {
		return nil, cov, fmt.Errorf(
			"value_screening 本日 %s 价值筛选输入不可得（策略输入缺失，不是行情数据缺失）："+
				"daily_basic 估值读取器未装配；条件 pe<=%g / pb<=%g / roe>=%g 均未生效，"+
				"不能退回纯动量选股：%w",
			screenDate.Format("2006-01-02"), peMax, pbMax, roeMin, contracts.ErrDataGap)
	}

	valuations := s.valuationReader(universe)

	for _, symbol := range universe {
		v, ok := valuations[symbol]
		if ok && v.PE != nil && *v.PE > 0 {
			cov.PE++
		}
		if ok && v.PB != nil && *v.PB > 0 {
			cov.PB++
		}
		if _, ok := latestROEOnOrBefore(s.fundamentals[symbol], screenDate); ok {
			cov.ROE++
		}
	}
	total := len(universe)

	if err := coverageGap(screenDate, "market.daily_basic", "pe/pe_ttm",
		fmt.Sprintf("pe<=%g", peMax), cov.PE, total); err != nil {
		return nil, cov, err
	}
	if err := coverageGap(screenDate, "market.daily_basic", "pb",
		fmt.Sprintf("pb<=%g", pbMax), cov.PB, total); err != nil {
		return nil, cov, err
	}
	if err := coverageGap(screenDate, "market.stock_fundamentals", "roe",
		fmt.Sprintf("roe>=%g", roeMin), cov.ROE, total); err != nil {
		return nil, cov, err
	}
	return valuations, cov, nil
}

// coverageGap 生成可诊断的数据缺口：日期、表、字段、覆盖数与未生效条件
// 全部点名。覆盖率 0 与「低覆盖」用同一口径（< valueCoverageMinRatio），
// 避免把「1/20 只票有数据」当成可用的横截面。
func coverageGap(
	screenDate time.Time, table, field, condition string, covered, total int,
) error {
	if total == 0 {
		return nil
	}
	if float64(covered)/float64(total) >= valueCoverageMinRatio {
		return nil
	}
	return fmt.Errorf(
		"value_screening 本日 %s 价值筛选输入覆盖不足（策略输入缺失，不是行情数据缺失）："+
			"%s.%s 可比覆盖 %d/%d（最低要求 %d%%），条件 %s 未生效；"+
			"不能退回纯动量选股：%w",
		screenDate.Format("2006-01-02"), table, field, covered, total,
		int(valueCoverageMinRatio*100), condition, contracts.ErrDataGap)
}

// latestROEOnOrBefore 取可用日 <= asOf 的最近一期财报的 ROE。
//
// 最近一期若 ROE 为 NULL（未披露/缺字段），不再向前找更老的一期 —— 用旧
// ROE 会掩盖「最新数据缺失」，与本策略的缺口口径冲突；返回 false 表示
// 该票当日 ROE 不可比。
func latestROEOnOrBefore(series strategy.FundamentalSeries, asOf time.Time) (float64, bool) {
	day := strategy.DayOf(asOf)
	for i := len(series) - 1; i >= 0; i-- {
		if strategy.DayOf(series[i].Date).After(day) {
			continue
		}
		if series[i].ROE == nil {
			return 0, false
		}
		return *series[i].ROE, true
	}
	return 0, false
}

// generateSellSignals generates sell signals for positions with negative momentum.
// Called on non-rebalance days.
func (s *valueScreeningStrategy) generateSellSignals(
	bars map[string][]domain.OHLCV,
	portfolio *domain.Portfolio,
	momentumDays int,
) ([]strategy.Signal, error) {
	if portfolio == nil {
		return nil, nil
	}

	var signals []strategy.Signal
	for symbol := range portfolio.Positions {
		data, ok := bars[symbol]
		if !ok || len(data) < momentumDays+1 {
			continue
		}

		// Sort by date ascending using shared utility
		sorted := sortOHLCV(data)

		endIdx := len(sorted) - 1
		if endIdx < momentumDays {
			continue
		}

		startPrice := sorted[endIdx-momentumDays].Close
		endPrice := sorted[endIdx].Close
		if startPrice <= 0 {
			continue
		}

		momentum := (endPrice - startPrice) / startPrice
		if momentum < 0 {
			// Negative momentum: exit the position
			signals = append(signals, strategy.Signal{
				Symbol:   symbol,
				Action:   "sell",
				Strength: -momentum,
				Price:    endPrice,
			})
		}
	}

	return signals, nil
}

func newValueScreeningStrategy() strategy.Strategy {
	s := &valueScreeningStrategy{
		BaseStrategy: strategy.NewBaseStrategy("value_screening", "Value screening: filter stocks by PE, PB, ROE criteria then rank by momentum"),
		params: ValueScreeningConfig{
			PEMax:              30.0,
			PBMax:              3.0,
			ROEMin:             0.1,
			MomentumDays:       60,
			TopN:               10,
			RebalanceFrequency: "monthly",
		},
	}
	return s
}

func init() { registerFactory(newValueScreeningStrategy) }
