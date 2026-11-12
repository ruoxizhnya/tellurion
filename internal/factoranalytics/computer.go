// Package factoranalytics 计算横截面因子收益与 IC，并写入 factor_returns /
// ic_analysis 两张表。
//
// 背景（B-05 / ADR-038）：两表归 core，但历史上只有空的 batch saver、没有
// 生产者，也没有读面。本包补的正是「生产者」这一半：用日线收盘价与 PIT
// 基本面快照，算出分位收益（quintile returns）与信息系数（IC）。
//
// 口径（写死并在单测里钉住，避免口径漂移）：
//   - 因子值：momentum = close[t]/close[t-lookback]-1；value = 1/PE（PE>0 才
//     参与，值越大越便宜）；quality = ROE（越大越好）。
//   - 前向收益：fwd = close[t+horizon]/close[t]-1。
//   - 分位：按因子值升序排名后等分为 5 组，quintile 5 = 因子值最高组。
//     每组的 avg_return 是组内前向收益均值；cumulative_return 是按再平衡
//     日期序列的滚动复利（仅用于展示趋势，区间重叠时不代表可交易净值）。
//   - IC：Pearson 相关；top_ic 存 Spearman 秩相关（对异常值更稳健）；
//     p_value 由 Pearson t 统计量做正态近似（双尾）。
//
// 只使用 available_date <= t 的基本面（PIT），不看 ann_date 之后才可见的数据。
package factoranalytics

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	cdom "github.com/ruoxizhnya/tellurion-contracts/domain"

	"tellurion/pkg/domain"
)

const (
	DefaultLookbackDays    = 20
	DefaultHorizonDays     = 5
	DefaultRebalanceDays   = 5
	DefaultMinCrossSection = 20
	// MaxSymbols 单次计算的票池上限：再大不会更快，只会让一次请求把数据
	// 服务的 bulk 预算吃光。
	MaxSymbols = 2000
	// MaxRangeDays 单次计算的最大自然日跨度（两年）。
	MaxRangeDays = 730
)

// ErrNoData 表示输入区间内凑不出任何有效再平衡日（无行情或无足够票池）。
var ErrNoData = errors.New("no data for factor computation in the requested range")

// BarProvider 是取数面（生产由 marketdata.Provider 实现）。
type BarProvider interface {
	GetStocks(ctx context.Context, exchange string) ([]domain.Stock, error)
	BulkLoadOHLCV(ctx context.Context, symbols []string, start, end time.Time) (map[string][]domain.OHLCV, error)
}

// FundamentalsReader 是 PIT 基本面快照（生产由 storage.PostgresStore 实现）。
type FundamentalsReader interface {
	GetFundamentalsSnapshot(ctx context.Context, cutoff time.Time) ([]domain.FundamentalData, error)
}

// Writer 是两张表的写入口（生产由 storage.PostgresStore 实现）。
type Writer interface {
	SaveFactorReturnBatch(ctx context.Context, records []*domain.FactorReturn) error
	SaveICEntryBatch(ctx context.Context, records []*domain.ICEntry) error
}

// SupportedFactors 返回本计算器能算的因子：momentum / value / quality。
//
// 这是**能力子集声明**，不是因子名册重建：完整名册的唯一权威仍是
// pkg/domain/factor_def_seed.go 的种子定义；本切片只覆盖具备横截面聚合
// 口径（因子值 + 前向收益）的三条，其余名册成员没有这里的口径。
func SupportedFactors() []cdom.FactorType {
	return []cdom.FactorType{cdom.FactorMomentum, cdom.FactorValue, cdom.FactorQuality}
}

// Request 是一次计算请求。
type Request struct {
	Start time.Time
	End   time.Time
	// Symbols 为空 = 用全部在市股票（GetStocks("")）。
	Symbols []string
	// Factors 为空 = momentum / value / quality 三条。
	Factors []cdom.FactorType

	LookbackDays    int
	HorizonDays     int
	RebalanceDays   int
	MinCrossSection int
}

// SkippedDate 记录某因子在某日被跳过的原因（票池不足等）。
type SkippedDate struct {
	TradeDate time.Time       `json:"trade_date"`
	Factor    cdom.FactorType `json:"factor"`
	Reason    string          `json:"reason"`
	Pairs     int             `json:"pairs"`
}

// Result 是一次计算的产出。
type Result struct {
	Universe       int                    `json:"universe"`
	Symbols        []string               `json:"symbols,omitempty"`
	RebalanceDates []time.Time            `json:"rebalance_dates"`
	FactorReturns  []*domain.FactorReturn `json:"-"`
	ICEntries      []*domain.ICEntry      `json:"-"`
	Skipped        []SkippedDate          `json:"skipped,omitempty"`
	FactorCount    int                    `json:"factor_count"`
	Written        bool                   `json:"written"`
}

// Computer 是因子分析计算器。
type Computer struct {
	bars         BarProvider
	fundamentals FundamentalsReader
	writer       Writer
}

// NewComputer 构造计算器。writer 可为 nil（只计算不落库，用于预览/测试）。
func NewComputer(bars BarProvider, fundamentals FundamentalsReader, writer Writer) *Computer {
	return &Computer{bars: bars, fundamentals: fundamentals, writer: writer}
}

// Compute 执行一次计算；writer 非 nil 时先计算再整体落库。
func (c *Computer) Compute(ctx context.Context, req Request) (*Result, error) {
	if c == nil || c.bars == nil {
		return nil, errors.New("factor analytics: market data provider is not configured")
	}
	r, err := normalize(req)
	if err != nil {
		return nil, err
	}

	symbols, err := c.resolveUniverse(ctx, r)
	if err != nil {
		return nil, err
	}
	if len(symbols) == 0 {
		return nil, ErrNoData
	}

	// 多取 lookback/horizon 的日历缓冲，避免把交易日当成自然日。
	lookbackPad := time.Duration(r.LookbackDays*2+10) * 24 * time.Hour
	horizonPad := time.Duration(r.HorizonDays*2+10) * 24 * time.Hour
	loadStart := r.Start.Add(-lookbackPad)
	loadEnd := r.End.Add(horizonPad)

	bars, err := c.bars.BulkLoadOHLCV(ctx, symbols, loadStart, loadEnd)
	if err != nil {
		return nil, fmt.Errorf("factor analytics: load bars: %w", err)
	}

	closes := make(map[string]map[time.Time]float64, len(bars))
	axisSet := make(map[time.Time]struct{}, 512)
	for symbol, series := range bars {
		m := make(map[time.Time]float64, len(series))
		for _, bar := range series {
			d := dateOf(bar.Date)
			if bar.Close <= 0 {
				continue
			}
			m[d] = bar.Close
			axisSet[d] = struct{}{}
		}
		if len(m) > 0 {
			closes[symbol] = m
		}
	}
	if len(closes) == 0 {
		return nil, ErrNoData
	}

	axis := make([]time.Time, 0, len(axisSet))
	for d := range axisSet {
		axis = append(axis, d)
	}
	sort.Slice(axis, func(i, j int) bool { return axis[i].Before(axis[j]) })

	var (
		results        []*domain.FactorReturn
		ices           []*domain.ICEntry
		skipped        []SkippedDate
		rebalanceDates []time.Time
		// 每个因子 × 分位的滚动累计收益，跨再平衡日复利。
		cum = make(map[cdom.FactorType]map[int]float64, len(r.Factors))
	)

	for _, f := range r.Factors {
		cum[f] = map[int]float64{1: 0, 2: 0, 3: 0, 4: 0, 5: 0}
	}

	fundCache := make(map[time.Time]map[string]domain.FundamentalData)
	needFundamentals := hasFactor(r.Factors, cdom.FactorValue) || hasFactor(r.Factors, cdom.FactorQuality)

	for i := r.LookbackDays; i+r.HorizonDays < len(axis); i += r.RebalanceDays {
		t := axis[i]
		if t.Before(dateOf(r.Start)) || t.After(dateOf(r.End)) {
			continue
		}
		baseDate := axis[i-r.LookbackDays]
		fwdDate := axis[i+r.HorizonDays]

		var snapshot map[string]domain.FundamentalData
		if needFundamentals {
			snapshot, err = c.snapshotAt(ctx, t, fundCache)
			if err != nil {
				return nil, err
			}
		}

		dayProduced := false
		for _, factor := range r.Factors {
			pairs := make([]pair, 0, len(closes))
			for symbol, series := range closes {
				closeT, okT := series[t]
				closeBase, okB := series[baseDate]
				closeFwd, okF := series[fwdDate]
				if !okT || !okB || !okF || closeT <= 0 {
					continue
				}
				value, ok := factorValue(factor, closeT, closeBase, snapshot[symbol])
				if !ok {
					continue
				}
				pairs = append(pairs, pair{symbol: symbol, value: value, forward: closeFwd/closeT - 1})
			}
			if len(pairs) < r.MinCrossSection {
				skipped = append(skipped, SkippedDate{
					TradeDate: t, Factor: factor,
					Reason: "cross_section_too_small", Pairs: len(pairs),
				})
				continue
			}
			sort.Slice(pairs, func(a, b int) bool {
				if pairs[a].value == pairs[b].value {
					return pairs[a].symbol < pairs[b].symbol
				}
				return pairs[a].value < pairs[b].value
			})

			buckets := make([][]pair, 5)
			n := len(pairs)
			for idx, p := range pairs {
				q := idx * 5 / n
				if q > 4 {
					q = 4
				}
				buckets[q] = append(buckets[q], p)
			}

			dayProduced = true
			avgs := make([]float64, 5)
			qs := make([]float64, 5)
			var values, forwards []float64
			for _, p := range pairs {
				values = append(values, p.value)
				forwards = append(forwards, p.forward)
			}
			for qi := 0; qi < 5; qi++ {
				quintile := qi + 1
				var sum float64
				for _, p := range buckets[qi] {
					sum += p.forward
				}
				avg := 0.0
				if len(buckets[qi]) > 0 {
					avg = sum / float64(len(buckets[qi]))
				}
				avgs[qi] = avg
				cum[factor][quintile] = (1+cum[factor][quintile])*(1+avg) - 1
				qs[qi] = cum[factor][quintile]
			}
			topMinusBot := avgs[4] - avgs[0]
			for qi := 0; qi < 5; qi++ {
				results = append(results, &domain.FactorReturn{
					FactorName:       factor,
					TradeDate:        t,
					Quintile:         qi + 1,
					AvgReturn:        avgs[qi],
					CumulativeReturn: qs[qi],
					TopMinusBot:      topMinusBot,
				})
			}

			ic := pearson(values, forwards)
			ices = append(ices, &domain.ICEntry{
				FactorName: factor,
				TradeDate:  t,
				IC:         ic,
				PValue:     pValue(ic, n),
				TopIC:      spearman(values, forwards),
			})
		}
		if dayProduced {
			rebalanceDates = append(rebalanceDates, t)
		}
	}

	if len(results) == 0 {
		return nil, ErrNoData
	}

	res := &Result{
		Universe:       len(closes),
		Symbols:        symbols,
		RebalanceDates: rebalanceDates,
		FactorReturns:  results,
		ICEntries:      ices,
		Skipped:        skipped,
		FactorCount:    len(r.Factors),
	}

	if c.writer != nil {
		if err := c.writer.SaveFactorReturnBatch(ctx, results); err != nil {
			return nil, fmt.Errorf("factor analytics: save factor returns: %w", err)
		}
		if err := c.writer.SaveICEntryBatch(ctx, ices); err != nil {
			return nil, fmt.Errorf("factor analytics: save ic entries: %w", err)
		}
		res.Written = true
	}
	return res, nil
}

type pair struct {
	symbol  string
	value   float64
	forward float64
}

func (c *Computer) resolveUniverse(ctx context.Context, r Request) ([]string, error) {
	symbols := append([]string(nil), r.Symbols...)
	if len(symbols) == 0 {
		stocks, err := c.bars.GetStocks(ctx, "")
		if err != nil {
			return nil, fmt.Errorf("factor analytics: load universe: %w", err)
		}
		start := dateOf(r.Start)
		for _, s := range stocks {
			if s.Symbol == "" || s.Status == "delisted" {
				continue
			}
			if s.DelistDate != nil && s.DelistDate.Before(start) {
				continue
			}
			symbols = append(symbols, s.Symbol)
		}
	}
	sort.Strings(symbols)
	out := symbols[:0]
	for _, s := range symbols {
		if s == "" {
			continue
		}
		if len(out) > 0 && out[len(out)-1] == s {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

func (c *Computer) snapshotAt(ctx context.Context, t time.Time, cache map[time.Time]map[string]domain.FundamentalData) (map[string]domain.FundamentalData, error) {
	if m, ok := cache[t]; ok {
		return m, nil
	}
	if c.fundamentals == nil {
		return nil, errors.New("factor analytics: fundamentals reader is not configured")
	}
	rows, err := c.fundamentals.GetFundamentalsSnapshot(ctx, t)
	if err != nil {
		return nil, fmt.Errorf("factor analytics: load fundamentals at %s: %w", t.Format("2006-01-02"), err)
	}
	m := make(map[string]domain.FundamentalData, len(rows))
	for _, f := range rows {
		m[f.TsCode] = f
	}
	cache[t] = m
	return m, nil
}

// factorValue 返回因子值；ok=false 表示该票在该日不参与该因子。
func factorValue(f cdom.FactorType, closeT, closeBase float64, fund domain.FundamentalData) (float64, bool) {
	switch f {
	case cdom.FactorMomentum:
		if closeBase <= 0 {
			return 0, false
		}
		return closeT/closeBase - 1, true
	case cdom.FactorValue:
		if fund.PE == nil || *fund.PE <= 0 {
			return 0, false
		}
		return 1 / *fund.PE, true
	case cdom.FactorQuality:
		if fund.ROE == nil {
			return 0, false
		}
		return *fund.ROE, true
	default:
		return 0, false
	}
}

func normalize(r Request) (Request, error) {
	if r.Start.IsZero() || r.End.IsZero() {
		return r, errors.New("factor analytics: start and end are required")
	}
	r.Start = dateOf(r.Start)
	r.End = dateOf(r.End)
	if r.End.Before(r.Start) {
		return r, errors.New("factor analytics: end must not be before start")
	}
	if r.End.Sub(r.Start) > time.Duration(MaxRangeDays)*24*time.Hour {
		return r, fmt.Errorf("factor analytics: range too large (max %d days)", MaxRangeDays)
	}
	if r.LookbackDays <= 0 {
		r.LookbackDays = DefaultLookbackDays
	}
	if r.HorizonDays <= 0 {
		r.HorizonDays = DefaultHorizonDays
	}
	if r.RebalanceDays <= 0 {
		r.RebalanceDays = r.HorizonDays
	}
	if r.MinCrossSection <= 0 {
		r.MinCrossSection = DefaultMinCrossSection
	}
	if r.LookbackDays > 250 || r.HorizonDays > 60 || r.RebalanceDays > 60 {
		return r, errors.New("factor analytics: lookback/horizon/rebalance out of range")
	}
	if len(r.Factors) == 0 {
		r.Factors = SupportedFactors()
	}
	for _, f := range r.Factors {
		if !hasFactor(SupportedFactors(), f) {
			return r, fmt.Errorf("factor analytics: unsupported factor %q", f)
		}
	}
	if len(r.Symbols) > MaxSymbols {
		return r, fmt.Errorf("factor analytics: too many symbols (%d > %d)", len(r.Symbols), MaxSymbols)
	}
	return r, nil
}

func hasFactor(fs []cdom.FactorType, want cdom.FactorType) bool {
	for _, f := range fs {
		if f == want {
			return true
		}
	}
	return false
}

func dateOf(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// pearson 返回 Pearson 相关系数；样本不足或方差为零时返回 0。
func pearson(xs, ys []float64) float64 {
	n := len(xs)
	if n < 2 || n != len(ys) {
		return 0
	}
	var sx, sy float64
	for i := 0; i < n; i++ {
		sx += xs[i]
		sy += ys[i]
	}
	mx, my := sx/float64(n), sy/float64(n)
	var cov, vx, vy float64
	for i := 0; i < n; i++ {
		dx, dy := xs[i]-mx, ys[i]-my
		cov += dx * dy
		vx += dx * dx
		vy += dy * dy
	}
	if vx == 0 || vy == 0 {
		return 0
	}
	r := cov / math.Sqrt(vx*vy)
	if r > 1 {
		r = 1
	} else if r < -1 {
		r = -1
	}
	return r
}

// spearman 返回秩相关（并列取平均秩）。
func spearman(xs, ys []float64) float64 {
	n := len(xs)
	if n < 2 || n != len(ys) {
		return 0
	}
	return pearson(ranks(xs), ranks(ys))
}

func ranks(vals []float64) []float64 {
	type idx struct {
		i int
		v float64
	}
	order := make([]idx, len(vals))
	for i, v := range vals {
		order[i] = idx{i: i, v: v}
	}
	sort.Slice(order, func(a, b int) bool { return order[a].v < order[b].v })
	out := make([]float64, len(vals))
	for i := 0; i < len(order); {
		j := i
		for j+1 < len(order) && order[j+1].v == order[i].v {
			j++
		}
		avgRank := float64(i+j)/2 + 1
		for k := i; k <= j; k++ {
			out[order[k].i] = avgRank
		}
		i = j + 1
	}
	return out
}

// pValue 由 Pearson r 的 t 统计量做双尾正态近似：t = r*sqrt((n-2)/(1-r^2))。
func pValue(r float64, n int) float64 {
	if n < 3 || math.Abs(r) >= 1 {
		if math.Abs(r) >= 1 && n >= 3 {
			return 0
		}
		return 1
	}
	t := r * math.Sqrt(float64(n-2)/(1-r*r))
	return math.Erfc(math.Abs(t) / math.Sqrt2)
}
