// Package plugins contains built-in strategy implementations.
package plugins

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	cdom "github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/ruoxizhnya/tellurion-kit/statistics"
	"tellurion/pkg/strategy"
)

import "tellurion/pkg/domain"

// MultiFactorConfig holds configuration for the multi-factor strategy.
type MultiFactorConfig struct {
	ValueWeight        float64
	QualityWeight      float64
	MomentumWeight     float64
	LookbackDays       int
	RebalanceFrequency string
	TopN               int
}

type rankedStock struct {
	symbol         string
	compositeScore float64
	qualityScore   float64
	momentumScore  float64
}

// renormalizeWithoutValue 把 value 权重从分母里去掉，返回 quality/momentum
// 两个分量的有效权重（和恒为 1）。
//
// 本地价量路径没有基本面输入：这里原先用「回看窗口内的价格分位」冒充
// value 分，再按 value_weight（默认 0.4）加权 —— 等于把 4 成权重压在一个
// 纯价格区间指标上，和 value_screen 里那处「标着价值、算的是价格」同类，
// 只是藏得更深（Signal 不暴露分量，事后看不出来）。价值维度缺席时正确
// 的做法是让它不参与加权、其余权重重新归一化，而不是用价格顶名。
func renormalizeWithoutValue(qw, mw float64) (float64, float64) {
	total := qw + mw
	if total <= 0 {
		// 用户把 quality/momentum 都关掉了：没有可归一的分量，
		// 不要凭空给 quality 造一个权重。
		return 0, 0
	}
	return qw / total, mw / total
}

// multiFactorStrategy implements a weighted multi-factor ranking strategy.
// Factors: value (1/PE), quality (ROE), momentum (price return).
type multiFactorStrategy struct {
	*strategy.BaseStrategy
	params MultiFactorConfig

	factorReader strategy.FactorZScoreReader
}

// Configure sets the strategy parameters.
func (s *multiFactorStrategy) Configure(params map[string]any) error {
	s.Lock()
	defer s.Unlock()
	if v, ok := params["value_weight"]; ok {
		if val, ok := parseFloatParam(v); ok {
			s.params.ValueWeight = val
		}
	}
	if v, ok := params["quality_weight"]; ok {
		if val, ok := parseFloatParam(v); ok {
			s.params.QualityWeight = val
		}
	}
	if v, ok := params["momentum_weight"]; ok {
		if val, ok := parseFloatParam(v); ok {
			s.params.MomentumWeight = val
		}
	}
	if v, ok := params["lookback_days"]; ok {
		if val, ok := parseIntParam(v); ok {
			s.params.LookbackDays = val
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

// Weight returns position weight based on composite score.
// For multi-factor: weight proportional to composite score (capped at 0.05).
func (s *multiFactorStrategy) Weight(signal strategy.Signal, portfolioValue float64) float64 {
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

// Cleanup releases resources.
func (s *multiFactorStrategy) Cleanup() {
	s.params = MultiFactorConfig{}
	s.factorReader = nil
}

// SetFactorCache injects a pre-computed factor z-score reader.
// When set, GenerateSignals reads z-scores from this cache (zero-latency)
// instead of falling back to the local price-data computation on each
// rebalance day. Pass nil to clear.
func (s *multiFactorStrategy) SetFactorCache(reader strategy.FactorZScoreReader) {
	s.factorReader = reader
}

// RequiredFactors 声明本策略读的三个横截面因子（见 generateSignalsFromFactorCache）。
// 引擎据此在预热后核对覆盖度：三个因子在回测区间内一条缓存都没有时拒跑 ——
// 那种情况下 generateSignalsFromFactorCache 会全部落空，最后以
// invalid/zero_trades 收场，看起来像「策略不赚钱」，实际是「没有因子」。
func (s *multiFactorStrategy) RequiredFactors() []cdom.FactorType {
	return []cdom.FactorType{cdom.FactorValue, cdom.FactorQuality, cdom.FactorMomentum}
}

// Name returns the strategy name.
func (s *multiFactorStrategy) Name() string {
	return "multi_factor"
}

// Description returns the strategy description.
func (s *multiFactorStrategy) Description() string {
	return "Multi-factor: rank stocks by weighted combination of value, quality, and momentum scores"
}

// Parameters returns the configurable parameters.
func (s *multiFactorStrategy) Parameters() []strategy.Parameter {
	return []strategy.Parameter{
		{
			Name:        "value_weight",
			Type:        "float",
			Default:     0.4,
			Description: "Weight for value score (1/PE), normalized",
			Min:         0.0,
			Max:         1.0,
		},
		{
			Name:        "quality_weight",
			Type:        "float",
			Default:     0.3,
			Description: "Weight for quality score (ROE), normalized",
			Min:         0.0,
			Max:         1.0,
		},
		{
			Name:        "momentum_weight",
			Type:        "float",
			Default:     0.3,
			Description: "Weight for momentum score (return), normalized",
			Min:         0.0,
			Max:         1.0,
		},
		{
			Name:        "lookback_days",
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

// rankPercentile sorts the input values and returns each element's percentile rank.
// Tied values receive the same percentile rank (average of their positions).
// Uses a stable sort so that equal values preserve original relative order.
func rankPercentile(values []float64) []float64 {
	n := len(values)
	if n == 0 {
		return nil
	}

	// Create index-value pairs and sort by value (stable sort for tie-breaking)
	type iv struct {
		idx   int
		value float64
	}
	pairs := make([]iv, n)
	for i, v := range values {
		pairs[i] = iv{idx: i, value: v}
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		return pairs[i].value < pairs[j].value
	})

	// Assign percentile ranks, grouping ties and assigning the average rank
	ranks := make([]float64, n)
	i := 0
	for i < n {
		// Find the range of equal values
		j := i + 1
		for j < n && pairs[j].value == pairs[i].value {
			j++
		}
		// Assign average rank (in 0-1 range) to all tied items
		avgRank := 0.0
		for k := i; k < j; k++ {
			avgRank += float64(k+1) / float64(n)
		}
		avgRank /= float64(j - i)
		for k := i; k < j; k++ {
			ranks[pairs[k].idx] = avgRank
		}
		i = j
	}

	return ranks
}

// GenerateSignals generates buy/sell signals using multi-factor ranking.
func (s *multiFactorStrategy) GenerateSignals(
	ctx context.Context,
	bars map[string][]domain.OHLCV,
	portfolio *domain.Portfolio,
) ([]strategy.Signal, error) {
	if len(bars) == 0 {
		return nil, nil
	}

	// Apply defaults
	vw := s.params.ValueWeight
	if vw <= 0 {
		vw = 0.4
	}
	qw := s.params.QualityWeight
	if qw <= 0 {
		qw = 0.3
	}
	mw := s.params.MomentumWeight
	if mw <= 0 {
		mw = 0.3
	}
	lookback := s.params.LookbackDays
	if lookback <= 0 {
		lookback = 60
	}
	// For single/few stocks, use smaller lookback to ensure signals are generated
	if len(bars) <= 3 && lookback > 20 {
		lookback = 20
	}
	topN := s.params.TopN
	if topN <= 0 {
		topN = 10
	}

	// Normalize weights to sum to 1
	totalWeight := vw + qw + mw
	if totalWeight <= 0 {
		totalWeight = 1.0
	}
	vw /= totalWeight
	qw /= totalWeight
	mw /= totalWeight

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
			// 行情为空、组合也没有日期：既没东西可算，也无从判断调仓日。
			// 不用 time.Now() —— 墙钟会让回测结果随运行日期漂移（P1-12）。
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
		sellSignals, err := s.generateSellSignals(bars, portfolio, lookback)
		if err != nil {
			return nil, fmt.Errorf("multi-factor sell signal generation failed: %w", err)
		}
		return sellSignals, nil
	}

	// 因子排序来源只有两条：run 级 z-score 缓存（生产路径恒注入 reader），
	// 或本地价量计算。原先还有一条「>3 票走 data 服务 /screen」的腿 ——
	// 该路由从未在 tellurion-data 注册，调用必然失败后被静默吞掉并回落到
	// 价量计算，等价于从未生效，只多打一次注定失败的请求。
	var ranked []rankedStock
	// valueAbsent 标记本次排序是否走本地价量路径（无基本面输入）。
	// 走这条路时 value 维度不参与加权，provenance 必须能把这件事说清楚，
	// 否则事后无法判断某个信号到底用没用价值条件。
	valueAbsent := false

	if s.factorReader != nil {
		ranked = s.generateSignalsFromFactorCache(bars, screenDate, vw, qw, mw)
		// If factor cache returns no results (e.g., cache is empty), fallback to price data
		if len(ranked) == 0 {
			ranked = s.generateSignalsFromPriceData(bars, lookback, qw, mw)
			valueAbsent = true
		}
	} else {
		ranked = s.generateSignalsFromPriceData(bars, lookback, qw, mw)
		valueAbsent = true
	}

	// Sort by composite score descending
	sort.Slice(ranked, func(i, j int) bool {
		return ranked[i].compositeScore > ranked[j].compositeScore
	})

	// Generate buy signals for top N
	n := len(ranked)
	if n > topN {
		n = topN
	}

	var signals []strategy.Signal
	topNSymbols := make(map[string]bool)
	effQ, effM := renormalizeWithoutValue(qw, mw)
	for i := 0; i < n; i++ {
		// Require at least some fundamental score
		if ranked[i].compositeScore <= 0 {
			continue
		}

		price := getLatestPrice(bars[ranked[i].symbol])

		sig := strategy.Signal{
			Symbol:   ranked[i].symbol,
			Action:   "buy",
			Strength: ranked[i].compositeScore,
			Price:    price,
			Date:     screenDate,
		}
		if valueAbsent {
			// 本地价量路径：只有 quality/momentum 两个分量参与排序。
			// Factors 写实际参与的分量（不含 value），Metadata 写明缺席原因
			// 与重分配后的有效权重 —— 这是事后判断「这个信号用没用价值
			// 条件」的唯一出口。
			sig.Factors = map[string]float64{
				"quality":  ranked[i].qualityScore,
				"momentum": math.Max(ranked[i].momentumScore, 0),
			}
			sig.Metadata = map[string]interface{}{
				"factor_source":        "price_data",
				"value_dimension":      "absent",
				"value_absent_reason":  "no fundamental input on local price/volume path",
				"weights_renormalized": true,
				"requested_weights": map[string]float64{
					"value": vw, "quality": qw, "momentum": mw,
				},
				"effective_weights": map[string]float64{
					"quality": effQ, "momentum": effM,
				},
			}
		}
		signals = append(signals, sig)
		topNSymbols[ranked[i].symbol] = true
	}

	// Generate sell signals for held positions not in top N
	if portfolio != nil {
		for symbol := range portfolio.Positions {
			if !topNSymbols[symbol] {
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

	return signals, nil
}

// generateSignalsFromFactorCache is the fast path: reads pre-computed z-scores from L1 cache.
// No HTTP calls, no percentile computation — z-scores are already normalized.
func (s *multiFactorStrategy) generateSignalsFromFactorCache(
	bars map[string][]domain.OHLCV,
	screenDate time.Time,
	vw, qw, mw float64,
) []rankedStock {
	var ranked []rankedStock
	for symbol := range bars {
		valZ, valOk := s.factorReader(cdom.FactorValue, screenDate, symbol)
		quaZ, quaOk := s.factorReader(cdom.FactorQuality, screenDate, symbol)
		momZ, momOk := s.factorReader(cdom.FactorMomentum, screenDate, symbol)

		factorsAvailable := 0
		if valOk {
			factorsAvailable++
		}
		if quaOk {
			factorsAvailable++
		}
		if momOk {
			factorsAvailable++
		}
		if factorsAvailable == 0 {
			continue
		}

		composite := 0.0
		totalW := 0.0
		if valOk {
			composite += vw * valZ
			totalW += vw
		}
		if quaOk {
			composite += qw * quaZ
			totalW += qw
		}
		if momOk {
			composite += mw * momZ
			totalW += mw
		}
		if totalW > 0 {
			composite /= totalW
		}

		ranked = append(ranked, rankedStock{
			symbol:         symbol,
			compositeScore: composite,
			qualityScore:   quaZ,
			momentumScore:  momZ,
		})
	}

	// Sort by composite score descending
	sort.Slice(ranked, func(i, j int) bool {
		return ranked[i].compositeScore > ranked[j].compositeScore
	})

	return ranked
}

// generateSignalsFromPriceData computes multi-factor scores purely from price/volume data.
// Used as fallback when the factor cache is unavailable or for single-stock mode.
//
// 这里**没有 value 分量**：没有基本面输入时，价值维度不参与加权，
// quality/momentum 权重在内部重新归一化（见 renormalizeWithoutValue）。
// 入参因此不再接收 value 权重。
func (s *multiFactorStrategy) generateSignalsFromPriceData(
	bars map[string][]domain.OHLCV,
	lookback int,
	qw, mw float64,
) []rankedStock {
	qw, mw = renormalizeWithoutValue(qw, mw)

	var ranked []rankedStock
	for symbol, data := range bars {
		if len(data) < lookback+1 {
			continue
		}
		// Sort by date ascending using shared utility
		sorted := sortOHLCV(data)

		endIdx := len(sorted) - 1
		startPrice := sorted[endIdx-lookback].Close
		endPrice := sorted[endIdx].Close
		if startPrice <= 0 {
			continue
		}

		// Momentum: price return over lookback
		momentumScore := (endPrice - startPrice) / startPrice

		// Quality: consistency of upward movement (low volatility + positive trend)
		closes := make([]float64, lookback+1)
		for i := endIdx - lookback; i <= endIdx; i++ {
			closes[i-(endIdx-lookback)] = sorted[i].Close
		}
		avgPrice := statistics.Mean(closes)
		stdDev := statistics.PopulationStdDev(closes)
		cv := stdDev / avgPrice
		qualityScore := 1.0 / (1.0 + cv*10.0) // Lower CV = higher quality
		if momentumScore > 0 {
			qualityScore *= (1.0 + momentumScore)
		}

		// Composite score: only reward positive momentum, ignore negative momentum
		// Use math.Max to ensure negative momentum does not artificially boost score
		composite := qw*qualityScore + mw*math.Max(momentumScore, 0)
		if composite < 0.1 {
			composite = 0.1
		}

		ranked = append(ranked, rankedStock{
			symbol:         symbol,
			compositeScore: composite,
			qualityScore:   qualityScore,
			momentumScore:  momentumScore,
		})
	}
	return ranked
}

// generateSellSignals generates sell signals for positions with negative momentum.
func (s *multiFactorStrategy) generateSellSignals(
	bars map[string][]domain.OHLCV,
	portfolio *domain.Portfolio,
	lookback int,
) ([]strategy.Signal, error) {
	if portfolio == nil {
		return nil, nil
	}

	var signals []strategy.Signal
	for symbol := range portfolio.Positions {
		data, ok := bars[symbol]
		if !ok || len(data) < lookback+1 {
			continue
		}

		// Sort by date ascending using shared utility
		sorted := sortOHLCV(data)

		endIdx := len(sorted) - 1
		if endIdx < lookback {
			continue
		}

		startPrice := sorted[endIdx-lookback].Close
		endPrice := sorted[endIdx].Close
		if startPrice <= 0 {
			continue
		}

		momentum := (endPrice - startPrice) / startPrice
		if momentum < 0 {
			signals = append(signals, strategy.Signal{
				Symbol:   symbol,
				Action:   "sell",
				Strength: math.Min(-momentum, 1.0),
				Price:    endPrice,
			})
		}
	}

	return signals, nil
}

func newMultiFactorStrategy() strategy.Strategy {
	s := &multiFactorStrategy{
		BaseStrategy: strategy.NewBaseStrategy("multi_factor", "Multi-factor: rank stocks by weighted combination of value, quality, and momentum scores"),
		params: MultiFactorConfig{
			ValueWeight:        0.4,
			QualityWeight:      0.3,
			MomentumWeight:     0.3,
			LookbackDays:       60,
			TopN:               10,
			RebalanceFrequency: "monthly",
		},
	}
	return s
}

func init() { registerFactory(newMultiFactorStrategy) }
