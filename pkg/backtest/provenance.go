package backtest

// QT-63 · run 留痕的写入路径。
//
// 定义在 pkg/backtest/contracts/provenance.go（QT-03 冻结），表在
// quant.run_provenance（storage.RunProvenanceRecord）。本文件只负责一件事：
// **把一次真实 run 的输入身份算出来、落一次**。
//
// 三条纪律：
//   - hash 分量取**生效值**，不取请求里的原始字串（N6-5）：FillTiming 走
//     ResolveFillTiming、Kernel 走 EffectiveKernel、费率走 WithDefaults 之后的
//     config —— 否则「跑的是 A、记的是 B」。
//   - 只在日循环前落一次。「输入身份」与这次跑成没跑成无关；失败/无效 run
//     同样留痕（status 的权威是 backtest_jobs，见 QT-63 ④）。
//   - 没有 sink（无库 / 纯内存测试）时整段是 no-op —— 既有测试路径零变化；
//     有 sink 时写不进去 = run 失败，不留「跑完了但没有身份」的结果。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/ruoxizhnya/tellurion-contracts/engine"
	"github.com/ruoxizhnya/tellurion-contracts/fees"
	"tellurion/pkg/risk"
	"tellurion/pkg/storage"
	"tellurion/pkg/strategy"
)

// RunProvenanceWriter 是 quant.run_provenance 的写入端口。
//
// 生产实现是 *storage.PostgresStore（WithStore 注入后自动承接）；
// 测试 / 非 PG 后端经 WithRunProvenanceWriter 注入替身。
type RunProvenanceWriter interface {
	SaveRunProvenance(ctx context.Context, rec *storage.RunProvenanceRecord) error
}

// RiskRulesProvider 是 risk 仓规则快照 hash 的取数口（QT-66 · 裁-10）。
//
// 生产实现是 `cmd/analysis/wiring` 注入的 HTTP 客户端（run 提交时调
// `POST /api/risk/resolve`）；测试注入确定性替身。core **不得**按规则文件
// 自己算 hash —— 规则版本算法与内容寻址的真相在 tellurion-risk。
type RiskRulesProvider interface {
	// ResolveRiskRulesHash 返回本次 run 生效的规则快照 hash。
	//
	// pinnedHash 非空 ⇒ 按锁定 hash 解析（不存在就失败，不回退 latest）；
	// 空串 ⇒ latest。今天 core 的 RunSpec 没有 pin 字段（QT-03 冻结面不含），
	// 引擎侧恒传空串；锁定分支的实现与测试在 HTTP provider 上。
	ResolveRiskRulesHash(ctx context.Context, pinnedHash string) (string, error)
}

// SetRiskRulesProvider 注入 risk 规则快照 hash 的取数口（QT-66 · 裁-10）。
//
// nil = 本部署未接入 risk 服务：provenance 的 risk_rules_hash 记空串并标
// not_configured。装配层（cmd/analysis/wiring）在启动时调用一次。
func (e *Engine) SetRiskRulesProvider(p RiskRulesProvider) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.riskRulesProvider = p
}

// HasRiskRulesProvider 报告规则 hash 取数口是否已接线（装配自证 / 测试用）。
func (e *Engine) HasRiskRulesProvider() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.riskRulesProvider != nil
}

// risk_rules_hash 的两种合法状态。空串只对应 not_configured（裁-11）。
const (
	riskRulesStatusResolved      = "resolved"
	riskRulesStatusNotConfigured = "not_configured"
)

// ErrRiskRulesUnavailable 表示「配置了 risk 服务，但取不到规则快照 hash」。
//
// 裁-11：这类失败一律让 run = failed，错误码 `risk_rules_unavailable`；
// 只有「本部署未接入 risk 服务」才允许空串并记 `not_configured`。
var ErrRiskRulesUnavailable = errors.New("risk_rules_unavailable")

// riskRulesResolution 是一次 run 的规则 hash 取值结果。
type riskRulesResolution struct {
	Hash   string
	Status string
}

// resolveRiskRulesForRun 调 provider 取本次 run 的规则快照 hash。
//
// 失败口径（裁-11）：
//   - provider 未注入 ⇒ not_configured（唯一允许空串的分支）；
//   - provider 报错 / 返回空串 ⇒ ErrRiskRulesUnavailable，run 失败。
func (e *Engine) resolveRiskRulesForRun(ctx context.Context) (riskRulesResolution, error) {
	e.mu.RLock()
	p := e.riskRulesProvider
	e.mu.RUnlock()

	if p == nil {
		return riskRulesResolution{Status: riskRulesStatusNotConfigured}, nil
	}

	// pin 今天恒为空：RunSpec 里还没有「锁定规则 hash」的字段（QT-03 冻结面）。
	// provider 的锁定分支已实现并有单测，接字段是未来的契约变更。
	hash, err := p.ResolveRiskRulesHash(ctx, "")
	if err != nil {
		return riskRulesResolution{}, fmt.Errorf("%w: %v", ErrRiskRulesUnavailable, err)
	}
	if strings.TrimSpace(hash) == "" {
		// 「接了但返回空」不是「没接入」：空串的合法含义只有 not_configured，
		// 这里静默放行会让两次 run 一个空、一个有值却都算成功。
		return riskRulesResolution{}, fmt.Errorf("%w: provider returned an empty hash", ErrRiskRulesUnavailable)
	}
	return riskRulesResolution{Hash: hash, Status: riskRulesStatusResolved}, nil
}

// 本引擎当前**固定**的执行口径。它们是常量而不是配置项：写在这里是为了让
// hash 里出现的是引擎真实的行为，而不是「目前恰好如此」的猜测。变成可配置的
// 那天，这里必须跟着改成读生效配置。
const (
	provenanceFrequency      = "1d"
	provenanceTPlus          = "t+1"
	provenancePriceLimitRule = "ashare_board_limits"

	// 复权口径：qfq 是本引擎消费的数据刻度（ohlcv_daily_qfq）。pinned 表示
	// REV-18 的区间起点基准快照生效；unpinned 表示没有锚点存储（无库 / 测试
	// fixture），此时数据刻度不受锚点约束 —— 两者必须 hash 不同。
	adjustmentBasisPinned   = "qfq+snapshot@range_start"
	adjustmentBasisUnpinned = "qfq+unpinned"
)

// provenanceSink 解析本次 run 的写入端口。
//
// 解析放在 run 时而不是构造时，选项的应用顺序就不影响结果：
// WithStore 与 WithRunProvenanceWriter / WithoutRunProvenance 谁先谁后都一样。
func (e *Engine) provenanceSink() RunProvenanceWriter {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if e.provenanceOff {
		return nil
	}
	if e.provWriter != nil {
		return e.provWriter
	}
	if e.store == nil {
		// Typed-nil guard：*storage.PostgresStore(nil) 塞进接口不是 nil。
		return nil
	}
	return e.store
}

// recordRunProvenance 计算并写入一次 run 的输入身份。
//
// 调用点：日循环之前（tradingDays 与复权锚点都已就位）。失败 = run 失败：
// 一份查不到输入的结果，比没有结果更危险。
func (e *Engine) recordRunProvenance(
	ctx context.Context,
	state *BacktestState,
	tradingDays []time.Time,
	runUniverse []string,
	adj *adjustmentRun,
	logger zerolog.Logger,
) error {
	sink := e.provenanceSink()
	if sink == nil {
		return nil
	}

	// QT-66：规则快照 hash 在 run 提交时取一次（裁-10）。取值失败 = run 失败：
	// 记一个空的、或者拿旧规则顶替的 hash，都会让「同 hash 不同结果」无声发生。
	riskRules, err := e.resolveRiskRulesForRun(ctx)
	if err != nil {
		logger.Error().Err(err).Str("risk_rules_status", "unavailable").
			Msg("risk rules resolve failed — refusing to run without a rules identity")
		return err
	}

	rec, err := e.buildRunProvenance(state, tradingDays, runUniverse, adj, riskRules.Hash)
	if err != nil {
		return err
	}
	if err := sink.SaveRunProvenance(ctx, rec); err != nil {
		return err
	}

	logger.Info().
		Str("run_spec_hash", rec.RunSpecHash).
		Str("dataset_snapshot_hash", rec.DatasetSnapshotHash).
		Str("version_hash", rec.VersionHash).
		Str("risk_rules_hash", rec.RiskRulesHash).
		Str("risk_rules_status", riskRules.Status).
		Str("kernel", rec.Kernel).
		Msg("Run provenance recorded")
	return nil
}

// buildRunProvenance 把一次 run 的生效输入折成 RunProvenanceRecord。
//
// 单独拆出来是为了让「改一个分量 ⇒ hash 变」可以被直接测（不必每次起整轮回测）。
func (e *Engine) buildRunProvenance(
	state *BacktestState,
	tradingDays []time.Time,
	runUniverse []string,
	adj *adjustmentRun,
	riskRulesHash string,
) (*storage.RunProvenanceRecord, error) {
	params := state.Params

	fillTiming, err := ResolveFillTiming(params.FillTiming)
	if err != nil {
		return nil, fmt.Errorf("run provenance: %w", err)
	}

	effParams, paramsJSON, err := effectiveStrategyParams(params.StrategyName, params.Params)
	if err != nil {
		return nil, err
	}
	paramsHash, err := contracts.ParamsHash(effParams)
	if err != nil {
		return nil, fmt.Errorf("run provenance: params_hash: %w", err)
	}

	digests := adj.anchorDigests()
	adjustmentBasis := adjustmentBasisUnpinned
	if adj != nil && adj.enabled {
		// 口径名 + 锚点集合摘要都进 dataset 身份：同一区间换一组冻结基准，
		// 价格刻度就换了，dataset_snapshot_hash 必须跟着变。
		adjustmentBasis = adjustmentBasisPinned + ";anchors=" + contracts.AdjustmentAnchorDigestsHash(digests)
	}

	datasetHash := contracts.DatasetSnapshotInput{
		// QT-23：票池身份用 **RunUniverse**（排序去重后的稳定超集），不是
		// 请求里的原始列表。否则同一份区间换一个输入顺序就会得到两个
		// dataset 身份，而「同票池 ⇔ 同 hash」正是 §2.6 的推论。
		Universe: runUniverse,
		Window: contracts.DateRange{
			Start: params.StartDate,
			End:   params.EndDate,
		},
		Frequency: provenanceFrequency,
		// 数据版本（供应商批次 / 表水位）今天没有坐标系可填（QT-40 的
		// aggregator 落点）。空串是「未登记」，不是「版本为空」—— 它让
		// dataset hash 覆盖不到原始数据是否被回填，这一条如实登记在
		// QT-63 的完成记录里，不在这里编一个假版本号。
		DataVersion:     "",
		Calendar:        tradingDays,
		AdjustmentBasis: adjustmentBasis,
	}.Hash()

	slippage, costModel := e.effectiveExecutionCost()

	spec := contracts.RunSpecInput{
		Start:          params.StartDate,
		End:            params.EndDate,
		InitialCapital: params.InitialCapital,
		RiskFreeRate:   params.RiskFreeRate,

		// 内置策略的载体是编译进二进制的 Go 代码：没有 wasm / 插件字节码，
		// 也就没有 artifact_hash（N7：执行真相为空 = 无独立字节码可锁定）。
		// version_hash 用插件 loader 的 git hash（内置策略拿不到，留空）。
		// QT-41b 的编译管线落地后，这两列由 artifact 登记填实。
		StrategyArtifactHash:    "",
		StrategyVersionHash:     lookupStrategyGitHash(params.StrategyName),
		StrategyParamsEffective: effParams,

		// QT-23：RunSpec 的票池同样记 **RunUniverse**（索引来源）。原始请求
		// 列表的「从哪来」由 IndexCode 保留；记录语义票池才能保证
		// 「同一 RunUniverse ⇒ 同一 run_spec_hash」。
		StockPool: runUniverse,
		IndexCode: params.IndexCode,
		Universe:  runUniverse,

		Frequency:  provenanceFrequency,
		FillTiming: fillTiming,

		Kernel:        params.Kernel,
		EngineVersion: engineVersion(),

		Fees:           e.effectiveFeeRules(),
		Slippage:       slippage,
		CostModel:      costModel,
		LotSize:        risk.LotSize,
		TPlus:          provenanceTPlus,
		PriceLimitRule: provenancePriceLimitRule,

		// QT-66（裁-11）：run 起始时刻生效的规则快照 hash，来源只有
		// tellurion-risk。空串只剩一种合法含义 —— 本部署未接入 risk 服务
		// （status = not_configured）；「接了但取不到」已在
		// recordRunProvenance 里以 ErrRiskRulesUnavailable 终止本次 run。
		RiskRulesHash:       riskRulesHash,
		DatasetSnapshotHash: datasetHash,

		AdjustmentAnchorDigests: digests,

		Seed: e.config.Seed,
	}

	runSpecHash, err := spec.Hash()
	if err != nil {
		return nil, fmt.Errorf("run provenance: run_spec_hash: %w", err)
	}

	return &storage.RunProvenanceRecord{
		RunID:               state.ID,
		RunSpecHash:         runSpecHash,
		DatasetSnapshotHash: datasetHash,
		ArtifactHash:        spec.StrategyArtifactHash,
		VersionHash:         spec.StrategyVersionHash,
		StrategyRefInput:    contracts.StrategyRef{ID: params.StrategyName}.String(),
		ParamsJSON:          paramsJSON,
		ParamsHash:          paramsHash,
		Seed:                spec.Seed,
		Kernel:              spec.Kernel,
		EngineVersion:       spec.EngineVersion,
		FillTiming:          fillTiming.String(),
		RiskRulesHash:       spec.RiskRulesHash,
	}, nil
}

// ResolveStrategyParams 计算一次 run 的**生效策略参数** = 定义默认 ⊕ run 覆写。
//
// 三条口径（QT-20 · A3 / p0-remediation-design §A3）：
//   - 浅合并：run 只覆写同名键，未覆写的键保留 manifest 声明的默认值。
//     **不写空 map**：默认值同样决定结果，删掉它会让「同 hash 不同结果」。
//   - 未知键拒绝并点名（与 contracts 的 EngineParams 同口径）：静默吞掉一个
//     拼错的参数名，调用方会以为参数生效了并拿这次结果下结论。拒绝在
//     RunBacktest 入口发生 ⇒ HTTP 映射 400。
//   - 类型 / 范围校验用策略清单 `Parameters()`（manifest.params[]）：清单只
//     声明了 Name / Type / Default / Min / Max；Min/Max 同时为 0 视为未设界。
//
// 策略不在注册表（自定义 SignalProvider / 拼错的名字）时没有 manifest 可校验：
// 覆写原样返回，由 provider 自行解释；run 是否可用由后续执行路径裁定。
// 返回的 map 是**新分配**的，调用方可安全写进本 run 的状态。
func ResolveStrategyParams(name string, overrides map[string]any) (map[string]any, error) {
	eff := make(map[string]any, len(overrides))

	info, err := strategy.GlobalDescribe(name)
	if err != nil {
		for k, v := range overrides {
			eff[k] = v
		}
		return eff, nil
	}

	declared := make(map[string]strategy.Parameter, len(info.Parameters))
	for _, p := range info.Parameters {
		declared[p.Name] = p
		eff[p.Name] = p.Default
	}
	if len(overrides) == 0 {
		return eff, nil
	}

	keys := make([]string, 0, len(overrides))
	for k := range overrides {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	unknown := make([]string, 0)
	for _, k := range keys {
		if _, ok := declared[k]; !ok {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf(
			"strategy_params contains unknown key(s) [%s] for strategy %q: unknown strategy params are rejected, not ignored (known: %s)",
			strings.Join(unknown, ", "), name, knownStrategyParamNames(declared))
	}

	for _, k := range keys {
		v := overrides[k]
		if err := validateStrategyParamValue(declared[k], v); err != nil {
			return nil, err
		}
		eff[k] = v
	}
	return eff, nil
}

// knownStrategyParamNames 把清单键排成一句可读的「已知参数」提示。
func knownStrategyParamNames(declared map[string]strategy.Parameter) string {
	if len(declared) == 0 {
		return "(none)"
	}
	names := make([]string, 0, len(declared))
	for n := range declared {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// validateStrategyParamValue 按清单声明的类型 / 范围校验一个覆写值。
//
// 类型名与 strategy.Parameter.Type 的既有约定一致（int / float / string /
// bool）；object / array / 空类型不在本函数判 —— 嵌套形状已由 contracts 的
// ValidateRunSpec 拒绝，这里不建第二份口径。
func validateStrategyParamValue(p strategy.Parameter, v any) error {
	if v == nil {
		return nil // nil = 该键无值；合并后保留默认语义由消费方决定
	}

	typ := strings.ToLower(strings.TrimSpace(p.Type))
	var num float64
	isNum := false
	switch typ {
	case "int":
		f, ok := numericParamValue(v)
		if !ok || f != math.Trunc(f) {
			return fmt.Errorf("strategy_params[%q] must be an integer for type %q, got %T(%v)", p.Name, p.Type, v, v)
		}
		num, isNum = f, true
	case "float", "number":
		f, ok := numericParamValue(v)
		if !ok {
			return fmt.Errorf("strategy_params[%q] must be a number for type %q, got %T(%v)", p.Name, p.Type, v, v)
		}
		num, isNum = f, true
	case "string":
		if _, ok := v.(string); !ok {
			return fmt.Errorf("strategy_params[%q] must be a string for type %q, got %T(%v)", p.Name, p.Type, v, v)
		}
	case "bool":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("strategy_params[%q] must be a bool for type %q, got %T(%v)", p.Name, p.Type, v, v)
		}
	}

	// Min/Max 同时为 0 = 清单没有声明边界（零值不可区分「下界是 0」与
	// 「没设下界」，所以这里按「至少声明了一侧才校验」处理）。
	if isNum && (p.Min != 0 || p.Max != 0) {
		if p.Min != 0 && num < p.Min {
			return fmt.Errorf("strategy_params[%q] = %v is below the declared minimum %v", p.Name, num, p.Min)
		}
		if p.Max != 0 && num > p.Max {
			return fmt.Errorf("strategy_params[%q] = %v exceeds the declared maximum %v", p.Name, num, p.Max)
		}
	}
	return nil
}

// numericParamValue 把任意 Go 数值宽度折成 float64（HTTP 给 float64，
// 进程内调用方可能直接给 int / int64）。
func numericParamValue(v any) (float64, bool) {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(rv.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return float64(rv.Uint()), true
	case reflect.Float32, reflect.Float64:
		return rv.Float(), true
	default:
		return 0, false
	}
}

// effectiveStrategyParams 返回策略的生效参数 = 定义默认 ⊕ run 覆写，并附带
// 写进 provenance 的 canonical JSON。
//
// 合并与校验的唯一实现在 ResolveStrategyParams；这里只多做一步序列化，
// 让「落库的那份 JSON」与「算 hash 的那份 map」必然同源。
//
// 策略不在注册表（注册表 miss / 自定义 SignalProvider）且无覆写时返回空 map：
// 引擎看不到它的参数，只能如实记「未登记」。
func effectiveStrategyParams(name string, overrides map[string]any) (map[string]any, []byte, error) {
	eff, err := ResolveStrategyParams(name, overrides)
	if err != nil {
		return nil, nil, fmt.Errorf("run provenance: %w", err)
	}

	raw, err := json.Marshal(eff)
	if err != nil {
		return nil, nil, fmt.Errorf("run provenance: marshal effective params: %w", err)
	}
	return eff, raw, nil
}

// effectiveExecutionCost 读**实际在被撮合路径使用**的滑点费率与模型。
//
// 主链路是 ExecutionService（executeViaExecutionService），它构造时的
// SlippageRate 才是成交价里真正用到的那个；零值在成本核里回落
// fees.FixedSlippageRate。读不到 Config（注入了不暴露配置的实现）时退回
// engine config —— 那是 tracker 直连路径的口径，并已按默认值填过。
func (e *Engine) effectiveExecutionCost() (rate float64, model string) {
	svc := e.executionBridge.Get()
	if svc == nil {
		return e.config.SlippageRate, "fixed"
	}
	model = svc.GetSlippageModel()
	if model == "" {
		model = "fixed"
	}
	if withConfig, ok := svc.(interface{ Config() domain.ExecutionConfig }); ok {
		rate = withConfig.Config().SlippageRate
		if rate == 0 {
			rate = fees.FixedSlippageRate
		}
		return rate, model
	}
	return e.config.SlippageRate, model
}

// effectiveFeeRules 是进入 run_spec_hash 的「成本与交易规则」标量集合。
//
// 用 RunSpecInput.Fees 这个 map 装：canonical 里 map 按 key 排序，加一项
// 不改其它项的编码。名单刻意包含涨跌停档位、新股天数、流动性上限与冲击
// 参数 —— 它们不是费率，但和费率一样直接决定成交价，漏一个就是一条
// 「同 hash 不同结果」。
func (e *Engine) effectiveFeeRules() map[string]float64 {
	trading := e.config.Trading
	rules := map[string]float64{
		"commission":            e.config.CommissionRate,
		"min_commission":        trading.MinCommission,
		"stamp_tax":             trading.StampTaxRate,
		"stamp_tax_before":      trading.StampTaxRateBefore,
		"transfer_fee":          trading.TransferFeeRate,
		"price_limit_normal":    trading.PriceLimit.Normal,
		"price_limit_st":        trading.PriceLimit.ST,
		"price_limit_st_before": trading.PriceLimit.STBefore,
		"price_limit_new":       trading.PriceLimit.New,
		"new_stock_days":        float64(trading.NewStockDays),
		"liquidity_factor":      e.liquidityFactor(),
	}
	if svc := e.executionBridge.Get(); svc != nil {
		if withConfig, ok := svc.(interface{ Config() domain.ExecutionConfig }); ok {
			cfg := withConfig.Config()
			rules["impact_sigma"] = cfg.ImpactSigma
			rules["impact_liquidity_factor"] = cfg.ImpactLiquidityFactor
		}
	}
	return rules
}

// engineVersion 是「实际执行核」的版本标签（N6-5）。
//
// 优先取 Go 构建信息里的 vcs.revision（go build 在 git 工作区里默认嵌入；
// 带未提交改动则加 -dirty 后缀）。没有 VCS 信息（例如某些容器构建）时退回
// 模块版本，再退回 "devel" —— 此时两个不同提交可能共享同一个标签，这是
// 如实记录的已知边界，而不是可以假装解决的。
func engineVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}

	var rev string
	dirty := false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev != "" {
		if dirty {
			return rev + "-dirty"
		}
		return rev
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	return "devel"
}
