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
	"fmt"
	"runtime/debug"
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
	adj *adjustmentRun,
	logger zerolog.Logger,
) error {
	sink := e.provenanceSink()
	if sink == nil {
		return nil
	}

	rec, err := e.buildRunProvenance(state, tradingDays, adj)
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
	adj *adjustmentRun,
) (*storage.RunProvenanceRecord, error) {
	params := state.Params

	fillTiming, err := ResolveFillTiming(params.FillTiming)
	if err != nil {
		return nil, fmt.Errorf("run provenance: %w", err)
	}

	effParams, paramsJSON, err := effectiveStrategyParams(params.StrategyName)
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
		Universe: params.StockPool,
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

		StockPool: params.StockPool,
		IndexCode: params.IndexCode,
		Universe:  params.StockPool,

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

		// QT-42 之前没有风控规则版本化，填不出真实值。空串 = 未登记。
		RiskRulesHash:       "",
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

// effectiveStrategyParams 返回策略的生效参数 = 定义默认 ⊕ run 覆写。
//
// 今天没有 run 级覆写（BacktestRequest 尚无 StrategyParams，那是 QT-21 的
// 字段），所以生效值就是注册表里该策略声明的默认值。**不写空 map**：
// 默认值同样决定结果 —— 改了某个策略的默认 lookback，行为就变了，
// 这时 hash 必须变，否则「同 hash 不同结果」。
//
// 策略不在注册表（注册表 miss / 自定义 SignalProvider）时返回空 map：
// 引擎看不到它的参数，只能如实记「未登记」。
func effectiveStrategyParams(name string) (map[string]any, []byte, error) {
	eff := make(map[string]any)
	// QT-11：要的是「声明的默认参数」这份元数据，用 Describe 拿——
	// GlobalGet 的返回值是一个可被拿去算信号的实例，执行路径不该碰它。
	if info, err := strategy.GlobalDescribe(name); err == nil {
		for _, p := range info.Parameters {
			eff[p.Name] = p.Default
		}
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
