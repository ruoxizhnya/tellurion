package backtest

// 因子按需求值（ADR-037 D2 / D3，2026-11-12 重开 CORE-FACTOR-CACHE-WRITER）。
//
// 背景：`public.factor_cache` 有表无写者，`multi_factor` 在本栈里读不到任何
// 横截面因子，只能 409 收场。ADR-037 的裁决是「因子值 owner = core、形态 =
// 消费点按需实时计算、物化到 run 级内存」，且**不新增数据库写者**。本文件
// 就是那条裁决的实现：
//
//   - 策略声明的必需因子在 run 级缓存里仍缺时，用本 run 的行情 + 财报
//     现场算出横截面 z-score，写进 `rc.factor`（run 私有，不跨 run 共享）；
//   - 算不出来（缺输入、缺内核）就什么也不写 —— 覆盖度闸随后 fail-closed；
//   - **不写 `public.factor_cache`**：横截面 z-score 依赖本次 run 的票池，
//     写共享表会污染别的 run，而且会留下永远不知道是否过期的缓存。
//
// 为什么在 run setup 一次算完，而不是每个消费点临时算：日循环里同一
// (因子, 日期) 会被多只票各读一次，按需算等于同一天的截面被重复构造
// N 次；setup 物化一次既满足「同一 run 内可复现」，也让重复请求变成
// 一次 map 查表（见 MissingFactors 的空结果早退）。

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	cdom "github.com/ruoxizhnya/tellurion-contracts/domain"
	"tellurion/pkg/domain"
	expr "tellurion/pkg/expression"
	"tellurion/pkg/strategy"
	strategicexpr "tellurion/pkg/strategy/expression"
)

// factorDeriveWarmupDays 是派生窗口向前多取的自然日数。
//
// 必需因子里 Longest Lookback 是 30 自然日（momentum 的 ts_delay(close,20)
// 与活跃度的 ts_mean(...,20)），45 天留出节假日与停牌余量。取不足只会让
// 窗口前段派生不出值（其后自然算出），不会伪造数据。
const factorDeriveWarmupDays = 45

// factorFundamentalInputs 是「取数要走财报序列」的输入字段名。
//
// 与 `pkg/strategy/expression` 的 fundamentalSeries 支持的字段保持同集；
// 这里只需判断「要不要预热财报」，字段本身的取值仍由 provider 负责。
var factorFundamentalInputs = map[string]bool{
	"pe": true, "pb": true, "ps": true,
	"roe": true, "roa": true,
	"revenue": true, "profit": true,
}

// requiredFactorTypes 返回策略声明的必需因子。未知策略 / 非因子策略返回 nil。
func requiredFactorTypes(strategyName string) ([]cdom.FactorType, error) {
	strat, err := strategy.DefaultRegistry.NewInstance(strategyName)
	if err != nil {
		// 注册表没有该策略（未加载 / 拼错）：执行路径随后会明确报错，
		// 派生与覆盖度闸都不越俎代庖。
		return nil, err
	}
	fd, ok := strat.(strategy.FactorDependent)
	if !ok {
		return nil, nil
	}
	return fd.RequiredFactors(), nil
}

// factorsNeedFundamentals 返回这些因子声明里用到的财报输入字段（升序、去重）。
// 返回值非空即表示预派生需要财报数据。
func factorsNeedFundamentals(factors []cdom.FactorType) []string {
	seen := make(map[string]bool)
	for _, f := range factors {
		def, ok := builtinFactorDef(f)
		if !ok {
			continue
		}
		for _, field := range def.InputFields {
			if factorFundamentalInputs[field] {
				seen[field] = true
			}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for field := range seen {
		out = append(out, field)
	}
	sort.Strings(out)
	return out
}

// builtinFactorDef 在冻结种子里查因子定义。
//
// 只认 `domain.BuiltinFactorDefinitions`（core 的事实源）；库里的定义快照
// 归管理面，本包不读，避免为了「按需求值」再造一条 DB 依赖。
func builtinFactorDef(f cdom.FactorType) (domain.FactorDefinition, bool) {
	for _, def := range domain.BuiltinFactorDefinitions {
		if def.Name == f {
			return def, true
		}
	}
	return domain.FactorDefinition{}, false
}

// derivableFactorDef 判断定义能否由表达式引擎现场求值。
//
// leaf / formula 可以；code（Go 出口已随 pkg/data 退役）与 pending（名册在册
// 但从未定义数学含义）**不可派生** —— 必须漏下去让覆盖度闸 fail-closed，
// 绝不能凭空造一个数学定义充数。
func derivableFactorDef(def domain.FactorDefinition) bool {
	if def.Formula == "" {
		return false
	}
	return def.Kernel == domain.KernelLeaf || def.Kernel == domain.KernelFormula
}

// deriveMissingFactors 把 run 级缓存里仍缺的必需因子现场算出来并物化。
//
// 返回本次**实际派生出的因子数**。第二次调用同一 run 时必需因子已全部在位，
// `MissingFactors` 返回空 → 立即返回 0，不再计算（幂等）。
//
// 任何失败都不阻断 run：打 Warn 并返回当前已派生的部分；覆盖度闸随后决定
// 是放行（部分因子可用，策略按可用因子加权）还是 409（全部缺失）。
func (rc *runContext) deriveMissingFactors(
	ctx context.Context, strategyName string, symbols []string, axis []time.Time,
) (int, error) {
	required, err := requiredFactorTypes(strategyName)
	if err != nil || len(required) == 0 {
		return 0, nil
	}

	missing := rc.factor.MissingFactors(required)
	if len(missing) == 0 {
		return 0, nil // 已物化：不重复计算
	}

	derivable := make([]domain.FactorDefinition, 0, len(missing))
	var skipped []string
	for _, f := range missing {
		def, ok := builtinFactorDef(f)
		if !ok || !derivableFactorDef(def) {
			skipped = append(skipped, string(f))
			continue
		}
		derivable = append(derivable, def)
	}
	if len(skipped) > 0 {
		rc.e.logger.Warn().
			Str("strategy", strategyName).
			Strs("factors", skipped).
			Msg("因子内核为 code/pending 或无公式，无法按需求值 —— 保持 fail-closed（缺失≠造数）")
	}
	if len(derivable) == 0 {
		return 0, nil
	}

	// 回看窗口：ts_delay / ts_mean 要跨过 run 起点，否则前 20 个交易日
	// 全是不足窗口的 NaN —— 那会让一个正常因子看起来「有但没值」。
	warmStart := rc.start.AddDate(0, 0, -factorDeriveWarmupDays)
	if err := rc.cache.Warm(ctx, symbols, warmStart, rc.warmEnd, rc.e.effectiveProvider); err != nil {
		rc.e.logger.Warn().Err(err).Msg("派生因子取数失败 —— 缺行情时该因子不会产出（不会伪造）")
	}

	bars := rc.cache.Snapshot()
	if len(bars) == 0 {
		rc.e.logger.Warn().Str("strategy", strategyName).
			Msg("派生因子时 L1 行情为空 —— 无法计算（覆盖度闸将按缺失处理）")
		return 0, nil
	}

	provider := strategicexpr.NewOHLCVDataProviderWithFundamentals(bars, rc.factorFundamentalRecords())
	evaluator := expr.NewEvaluator(provider)

	// 日期键用 axis 里的**精确 time.Time 值**：日循环读 z-score 时传的就是
	// tradingDays 里的元素，map 键必须与它相等（time.Time 的 == 比较包含
	// location 与 wall clock，bar.Date 与 axis 值不保证逐字相同）。
	dayKey := make(map[string]time.Time, len(axis))
	for _, day := range axis {
		dayKey[day.Format("2006-01-02")] = day
	}

	merged := make(map[cdom.FactorType]map[time.Time]map[string]float64, len(derivable))
	for _, def := range derivable {
		byDate, derr := deriveFactorCrossSections(evaluator, def, bars, dayKey)
		if derr != nil {
			rc.e.logger.Warn().Str("factor", string(def.Name)).Err(derr).
				Msg("因子按需求值失败 —— 保持缺失（不伪造）")
			continue
		}
		if len(byDate) == 0 {
			rc.e.logger.Warn().Str("factor", string(def.Name)).
				Msg("因子按需求值无有效截面（输入缺失或横截面不足 2 只）—— 保持缺失")
			continue
		}
		merged[def.Name] = byDate
	}
	if len(merged) == 0 {
		return 0, nil
	}

	rc.factor.Merge(merged)
	names := make([]string, 0, len(merged))
	for f := range merged {
		names = append(names, string(f))
	}
	sort.Strings(names)
	rc.e.logger.Info().
		Str("strategy", strategyName).
		Strs("factors", names).
		Int("symbols", len(symbols)).
		Msg("按需求值完成：因子 z-score 已物化到本 run 内存（ADR-037 D2，不写库）")
	return len(merged), nil
}

// deriveFactorCrossSections 求一个因子的值并按日做横截面 z-score。
//
// 口径与 `pkg/expression` 的 cs_zscore 一致（总体标准差；std==0 → 不产出），
// 只额外要求「当日有效样本 >= 2」：单只票的截面没有「相对强弱」可言，
// 算出来的 0 是伪信号。
func deriveFactorCrossSections(
	evaluator *expr.Evaluator,
	def domain.FactorDefinition,
	bars map[string][]domain.OHLCV,
	dayKey map[string]time.Time,
) (map[time.Time]map[string]float64, error) {
	parsed, err := expr.NewParser().Parse(def.Formula)
	if err != nil {
		return nil, fmt.Errorf("parse formula %q: %w", def.Formula, err)
	}
	raw, err := evaluator.Evaluate(parsed.AST, 0)
	if err != nil {
		return nil, fmt.Errorf("evaluate formula %q: %w", def.Formula, err)
	}

	var valid map[string][]bool
	if def.Validity != "" {
		vp, perr := expr.NewParser().Parse(def.Validity)
		if perr != nil {
			return nil, fmt.Errorf("parse validity %q: %w", def.Validity, perr)
		}
		mask, merr := evaluator.Evaluate(vp.AST, 0)
		if merr != nil {
			return nil, fmt.Errorf("evaluate validity %q: %w", def.Validity, merr)
		}
		valid = make(map[string][]bool, len(mask))
		for sym, series := range mask {
			m := make([]bool, len(series))
			for i, v := range series {
				m[i] = !math.IsNaN(v) && v != 0
			}
			valid[sym] = m
		}
	}

	byDay := make(map[string]*derivedDay)
	for sym, series := range raw {
		symBars := bars[sym]
		mask := valid[sym]
		for i, v := range series {
			if i >= len(symBars) {
				break
			}
			if math.IsNaN(v) || math.IsInf(v, 0) {
				continue
			}
			if mask != nil && (i >= len(mask) || !mask[i]) {
				continue
			}
			barDate := symBars[i].Date
			k := barDate.Format("2006-01-02")
			day := byDay[k]
			if day == nil {
				day = &derivedDay{values: make(map[string]float64), keys: make(map[time.Time]struct{})}
				byDay[k] = day
			}
			day.values[sym] = v
			day.keys[barDate] = struct{}{}
		}
	}

	out := make(map[time.Time]map[string]float64, len(byDay))
	for k, day := range byDay {
		z, ok := zScoreCrossSection(day.values)
		if !ok {
			continue
		}
		// 同一份截面挂到「bar 自己的日期」与「交易日轴上的日期」两种键上：
		// 按日循环读 z-score 时传的是后者，按 bar 最新日期筛选（策略侧）读的是
		// 前者；time.Time 的 == 比较包含 location，两种来源不保证逐字相同。
		for key := range day.keys {
			out[key] = z
		}
		if axisKey, ok := dayKey[k]; ok {
			out[axisKey] = z
		}
	}
	return out, nil
}

// derivedDay 是同一天（按日历日归组）的横截面累加器。
//
// 为什么要按 Format("2006-01-02") 归组而不是直接拿 time.Time 当 map 键：
// 不同标的的 bar 日期可能来自不同时区/扫描路径（DB vs provider），
// time.Time 的 == 包含 location —— 直接当键会把同一天拆成两个 n=1 的
// 假截面，z-score 全部消失。日期字符串是唯一可靠的分组口径。
type derivedDay struct {
	values map[string]float64
	keys   map[time.Time]struct{}
}

// zScoreCrossSection 对一天的有效样本做横截面 z-score。
//
// 与 `pkg/expression` 的 csZScore 同口径：总体标准差（除以 n），std == 0
// 时整列无值。少于 2 个样本直接不产出 —— 单点截面标准化没有意义。
func zScoreCrossSection(raw map[string]float64) (map[string]float64, bool) {
	n := 0
	sum := 0.0
	for _, v := range raw {
		if math.IsNaN(v) {
			continue
		}
		sum += v
		n++
	}
	if n < 2 {
		return nil, false
	}
	mean := sum / float64(n)

	sumSq := 0.0
	for _, v := range raw {
		if math.IsNaN(v) {
			continue
		}
		d := v - mean
		sumSq += d * d
	}
	std := math.Sqrt(sumSq / float64(n))
	if std == 0 {
		return nil, false
	}

	out := make(map[string]float64, n)
	for s, v := range raw {
		if math.IsNaN(v) {
			continue
		}
		out[s] = (v - mean) / std
	}
	return out, true
}

// factorFundamentalRecords 把 run 级财报快照转成 provider 要的 map 形态。
//
// `strategy.FundamentalSeries` 是 `[]domain.Fundamental` 的**别名**（不是
// 新类型），元素无需转换；这里只换外层 map 的类型，且不复制序列本体。
func (rc *runContext) factorFundamentalRecords() map[string][]domain.Fundamental {
	snapshot := rc.fundamentalsSnapshot()
	if len(snapshot) == 0 {
		return nil
	}
	out := make(map[string][]domain.Fundamental, len(snapshot))
	for sym, series := range snapshot {
		out[sym] = series
	}
	return out
}
