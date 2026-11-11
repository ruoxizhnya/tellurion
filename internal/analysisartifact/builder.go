package analysisartifact

// QT-78 · 分析产物构建器。
//
// 输入是「一次 run 的终态」：期末持仓、基准指数、因子缓存与因子收益。所有
// 输出都必须能追溯到真实输入——缺任何一类输入就返回 MissingError，让上层
// **不产出**该类产物（fail-closed），而不是编一份空持仓让归因「算得出」。

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	cdom "github.com/ruoxizhnya/tellurion-contracts/domain"
)

import "tellurion/pkg/domain"

// DefaultFactors 返回缺省参与因子归因的因子清单。
//
// 不在这里抄一份名册：因子清单的唯一权威是
// `tellurion-contracts:domain.BuiltinFactorDefinitions`（契约仓的种子定义）；
// repoguard 的 TestNoBypassOfFactorRegistry 钉住 core 侧不许在库外重建清单。
// 某次 run 是否真的产出某个因子，仍由「取得到 z-score 且有 top_minus_bot」
// 决定 —— 名册在册不等于本次有数。
func DefaultFactors() []cdom.FactorType {
	defs := domain.BuiltinFactorDefinitions
	out := make([]cdom.FactorType, 0, len(defs))
	seen := make(map[cdom.FactorType]bool, len(defs))
	for _, def := range defs {
		if def.Name == "" || seen[def.Name] {
			continue
		}
		seen[def.Name] = true
		out = append(out, def.Name)
	}
	return out
}

// MissingError 表示某类必填输入缺失，产物不得产出。
//
// Field 取值对齐 analytics `docs/api-schema.md §6.3` 的 missing 闭集
// （holdings / holdings.dataset_hash / benchmark_sector_weights /
// benchmark_sector_returns / factor_exposures / factor_exposures.dataset_hash），
// 便于两端在同一命名下对账。
type MissingError struct {
	Field string
}

func (e *MissingError) Error() string {
	return "analysis artifact incomplete: missing " + e.Field
}

func missing(field string) error { return &MissingError{Field: field} }

// 因子收益缺失的两种可判定原因（ADR-038）。
const (
	// FactorReturnsNoWriter：整张 factor_returns 表一行都没有——本栈没有生产写者。
	FactorReturnsNoWriter = "no_writer"
	// FactorReturnsNoRows：表里有行，但本次窗口内这些因子没有行。
	FactorReturnsNoRows = "no_rows"
)

// FactorReturnsUnavailableError 表示因子归因不可产出，并说清**为什么**缺。
//
// 与 MissingError 的分工：MissingError 是「字段没填」，本错误是「数据源不存在 /
// 窗口内无数据」。把两者混成一个 "missing factor_exposures" 会让「本栈从来没有
// 写者」和「这次窗口刚好没数据」无法区分——这正是 ADR-038 D3 要修的问题。
type FactorReturnsUnavailableError struct {
	Reason  string
	Factors []cdom.FactorType
	Start   time.Time
	End     time.Time
}

func (e *FactorReturnsUnavailableError) Error() string {
	names := make([]string, len(e.Factors))
	for i, f := range e.Factors {
		names[i] = string(f)
	}
	hint := "the table has rows, but none for these factors in this window"
	if e.Reason == FactorReturnsNoWriter {
		hint = "the table has no row at all: this stack has no production writer for factor_returns (ADR-038); reads are on-demand, not materialized"
	}
	return fmt.Sprintf("factor attribution unavailable (%s): factors [%s] have no factor_returns in [%s, %s]; %s",
		e.Reason, strings.Join(names, ", "),
		e.Start.Format("2006-01-02"), e.End.Format("2006-01-02"), hint)
}

// FactorReturnProbe 是可选的「表里到底有没有任何行」探针（由 *storage.PostgresStore 满足）。
//
// 不实现时退化为 FactorReturnsNoRows：判不出「无写者」，但至少不会静默产出空产物。
type FactorReturnProbe interface {
	HasFactorReturns(ctx context.Context) (bool, error)
}

// MarketProvider 是构建基准侧所需的行情面（由 marketdata.Provider 满足）。
type MarketProvider interface {
	GetIndexConstituents(ctx context.Context, indexCode string) ([]string, error)
	BulkLoadOHLCV(ctx context.Context, symbols []string, start, end time.Time) (map[string][]domain.OHLCV, error)
}

// StockCatalog 是一次取全市场股票静态信息的面（行业 / 市值）。
type StockCatalog interface {
	Stocks(ctx context.Context) ([]domain.Stock, error)
}

// FactorZScores 是因子暴露的读面（由 *backtest.Engine 满足）。
type FactorZScores interface {
	GetFactorZScore(factor cdom.FactorType, date time.Time, symbol string) (float64, bool)
}

// FactorReturnReader 是历史因子收益的读面（由 *storage.PostgresStore 满足）。
type FactorReturnReader interface {
	GetFactorReturns(ctx context.Context, factor cdom.FactorType, start, end time.Time) ([]*domain.FactorReturn, error)
}

// Input 是构建两类产物共用的 run 终态输入。
type Input struct {
	RunID          string
	DatasetHash    string
	StartDate      time.Time
	EndDate        time.Time
	BenchmarkIndex string
	Positions      []domain.Position
	Stocks         map[string]domain.Stock
}

// Builder 负责从 run 终态推导两类产物。
type Builder struct {
	Market        MarketProvider
	ZScores       FactorZScores
	FactorReturns FactorReturnReader
	Factors       []cdom.FactorType
}

// BuildPortfolio 构建 Brinson 归因输入。
func (b *Builder) BuildPortfolio(ctx context.Context, in Input) (*PortfolioSnapshot, error) {
	if in.DatasetHash == "" {
		return nil, missing("holdings.dataset_hash")
	}
	if len(in.Positions) == 0 {
		return nil, missing("holdings")
	}
	if in.BenchmarkIndex == "" {
		return nil, missing("benchmark_sector_weights")
	}

	weights, err := positionWeights(in.Positions)
	if err != nil {
		return nil, err
	}

	constituents, err := b.Market.GetIndexConstituents(ctx, in.BenchmarkIndex)
	if err != nil {
		return nil, fmt.Errorf("get index constituents %s: %w", in.BenchmarkIndex, err)
	}
	if len(constituents) == 0 {
		return nil, missing("benchmark_sector_weights")
	}

	symbols := make([]string, 0, len(in.Positions)+len(constituents))
	seen := make(map[string]bool, len(in.Positions)+len(constituents))
	for _, p := range in.Positions {
		if !seen[p.Symbol] {
			seen[p.Symbol] = true
			symbols = append(symbols, p.Symbol)
		}
	}
	for _, s := range constituents {
		if !seen[s] {
			seen[s] = true
			symbols = append(symbols, s)
		}
	}
	bars, err := b.Market.BulkLoadOHLCV(ctx, symbols, in.StartDate, in.EndDate)
	if err != nil {
		return nil, fmt.Errorf("load ohlcv: %w", err)
	}

	holdings := make([]Holding, 0, len(in.Positions))
	for _, pos := range in.Positions {
		stock, ok := in.Stocks[pos.Symbol]
		if !ok || stock.Industry == "" {
			return nil, missing("holdings.sector:" + pos.Symbol)
		}
		ret, ok := windowReturn(bars[pos.Symbol], in.StartDate, in.EndDate)
		if !ok {
			return nil, missing("holdings.period_return:" + pos.Symbol)
		}
		holdings = append(holdings, Holding{
			Symbol:       pos.Symbol,
			Sector:       stock.Industry,
			Weight:       weights[pos.Symbol],
			PeriodReturn: ret,
		})
	}

	sectorCaps, sectorReturns, totalCap, benchmarkReturn := benchmarkSectors(constituents, in.Stocks, bars, in.StartDate, in.EndDate)
	if len(sectorCaps) == 0 || totalCap <= 0 {
		return nil, missing("benchmark_sector_weights")
	}
	if len(sectorReturns) == 0 {
		return nil, missing("benchmark_sector_returns")
	}

	sectorWeights := make(map[string]float64, len(sectorCaps))
	for sector, cap := range sectorCaps {
		sectorWeights[sector] = cap / totalCap
	}

	return &PortfolioSnapshot{
		Holdings:               holdings,
		BenchmarkSectorWeights: sectorWeights,
		BenchmarkSectorReturns: sectorReturns,
		BenchmarkReturn:        benchmarkReturn,
		DatasetHash:            in.DatasetHash,
	}, nil
}

// BuildFactors 构建因子归因输入。
func (b *Builder) BuildFactors(ctx context.Context, in Input) (*FactorExposures, error) {
	if in.DatasetHash == "" {
		return nil, missing("factor_exposures.dataset_hash")
	}
	if len(in.Positions) == 0 {
		return nil, missing("factor_exposures")
	}
	weights, err := positionWeights(in.Positions)
	if err != nil {
		return nil, missing("factor_exposures")
	}

	factors := b.Factors
	if len(factors) == 0 {
		factors = DefaultFactors()
	}

	out := make([]FactorExposure, 0, len(factors))
	missingFactors := make([]cdom.FactorType, 0, len(factors))
	for _, factor := range factors {
		exposure, ok := b.portfolioExposure(factor, in.EndDate, in.Positions, weights)
		if !ok {
			missingFactors = append(missingFactors, factor)
			continue
		}
		returns, err := b.FactorReturns.GetFactorReturns(ctx, factor, in.StartDate, in.EndDate)
		if err != nil {
			return nil, fmt.Errorf("get factor returns %s: %w", factor, err)
		}
		factorReturn, ok := latestTopMinusBot(returns)
		if !ok {
			missingFactors = append(missingFactors, factor)
			continue
		}
		out = append(out, FactorExposure{
			Name:         string(factor),
			Exposure:     exposure,
			FactorReturn: factorReturn,
		})
	}
	if len(out) == 0 {
		return nil, b.factorReturnsUnavailable(ctx, missingFactors, in)
	}
	// 部分缺失不再静默：产物里点名缺哪些因子，消费方可以据此判断归因覆盖度。
	return &FactorExposures{Factors: out, MissingFactors: factorNames(missingFactors), DatasetHash: in.DatasetHash}, nil
}

// factorReturnsUnavailable 判定「全部因子都无收益」的原因：表空 = 无写者；表非空 = 窗口无行。
func (b *Builder) factorReturnsUnavailable(ctx context.Context, factors []cdom.FactorType, in Input) error {
	reason := FactorReturnsNoRows
	if probe, ok := b.FactorReturns.(FactorReturnProbe); ok {
		hasAny, err := probe.HasFactorReturns(ctx)
		// 探针失败不掩盖主错误：按 no_rows 上报，至少不会把「查询失败」伪装成「无写者」。
		if err == nil && !hasAny {
			reason = FactorReturnsNoWriter
		}
	}
	return &FactorReturnsUnavailableError{Reason: reason, Factors: factors, Start: in.StartDate, End: in.EndDate}
}

func factorNames(factors []cdom.FactorType) []string {
	if len(factors) == 0 {
		return nil
	}
	out := make([]string, len(factors))
	for i, f := range factors {
		out[i] = string(f)
	}
	return out
}

// portfolioExposure 返回持仓市值加权因子 z-score；一个可得暴露都没有时返回 false。
func (b *Builder) portfolioExposure(factor cdom.FactorType, date time.Time, positions []domain.Position, weights map[string]float64) (float64, bool) {
	var sum float64
	var covered bool
	for _, pos := range positions {
		z, ok := b.ZScores.GetFactorZScore(factor, date, pos.Symbol)
		if !ok {
			continue
		}
		sum += weights[pos.Symbol] * z
		covered = true
	}
	return sum, covered
}

// positionWeights 把期末持仓归一成权重。
//
// 优先用市值；市值全为 0（例如轨迹未回填）时退到持仓自带的 Weight。
// 两者都拿不到正权重 → MissingError：权重是 Brinson 的分母，不能拿 0 冒充。
func positionWeights(positions []domain.Position) (map[string]float64, error) {
	total := 0.0
	for _, p := range positions {
		if p.MarketValue > 0 {
			total += p.MarketValue
		}
	}
	useWeight := total <= 0
	if useWeight {
		for _, p := range positions {
			if p.Weight > 0 {
				total += p.Weight
			}
		}
	}
	if total <= 0 {
		return nil, missing("holdings.weight")
	}

	out := make(map[string]float64, len(positions))
	for _, p := range positions {
		if useWeight {
			out[p.Symbol] = p.Weight / total
		} else {
			out[p.Symbol] = p.MarketValue / total
		}
	}
	return out, nil
}

// benchmarkSectors 汇总基准的行业市值、行业收益与总市值。
func benchmarkSectors(constituents []string, stocks map[string]domain.Stock, bars map[string][]domain.OHLCV, start, end time.Time) (sectorCaps, sectorReturns map[string]float64, totalCap, benchmarkReturn float64) {
	sectorCaps = make(map[string]float64)
	sectorReturns = make(map[string]float64)
	sectorReturnCaps := make(map[string]float64)

	seen := make(map[string]bool, len(constituents))
	for _, code := range constituents {
		if seen[code] {
			continue
		}
		seen[code] = true

		stock, ok := stocks[code]
		if !ok || stock.Industry == "" || stock.MarketCap <= 0 {
			continue
		}
		sectorCaps[stock.Industry] += stock.MarketCap
		totalCap += stock.MarketCap

		ret, ok := windowReturn(bars[code], start, end)
		if !ok {
			continue
		}
		sectorReturns[stock.Industry] += stock.MarketCap * ret
		sectorReturnCaps[stock.Industry] += stock.MarketCap
	}

	for sector, capSum := range sectorReturnCaps {
		if capSum > 0 {
			sectorReturns[sector] /= capSum
		}
	}

	// 基准总收益：只对「有区间收益」的成分按市值加权。
	var retCap float64
	for _, code := range constituents {
		stock, ok := stocks[code]
		if !ok || stock.MarketCap <= 0 {
			continue
		}
		ret, ok := windowReturn(bars[code], start, end)
		if !ok {
			continue
		}
		benchmarkReturn += stock.MarketCap * ret
		retCap += stock.MarketCap
	}
	if retCap > 0 {
		benchmarkReturn /= retCap
	}
	return sectorCaps, sectorReturns, totalCap, benchmarkReturn
}

// windowReturn 返回 [start, end] 内首末收盘的区间收益。
func windowReturn(bars []domain.OHLCV, start, end time.Time) (float64, bool) {
	if len(bars) < 2 {
		return 0, false
	}
	sorted := make([]domain.OHLCV, len(bars))
	copy(sorted, bars)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Date.Before(sorted[j].Date) })

	var first, last domain.OHLCV
	var haveFirst, haveLast bool
	for _, bar := range sorted {
		if bar.Date.Before(start) || bar.Date.After(end) {
			continue
		}
		if !haveFirst {
			first, haveFirst = bar, true
		}
		last, haveLast = bar, true
	}
	if !haveFirst || !haveLast || first.Close <= 0 || last.Date.Equal(first.Date) {
		return 0, false
	}
	return last.Close/first.Close - 1, true
}

// latestTopMinusBot 取区间末的 top_minus_bot（同一交易日 5 个分位行的值相同）。
func latestTopMinusBot(returns []*domain.FactorReturn) (float64, bool) {
	var latest *domain.FactorReturn
	for _, r := range returns {
		if r == nil {
			continue
		}
		if latest == nil || r.TradeDate.After(latest.TradeDate) {
			latest = r
		}
	}
	if latest == nil {
		return 0, false
	}
	return latest.TopMinusBot, true
}
