package backtest

// QT-63 的验收腿：run 留痕真的接进了执行路径。
//
// 三条要证的事：
//  1. **同输入 ⇒ 同身份**：同一份生效输入跑两次，run_spec_hash /
//     dataset_snapshot_hash / params_hash 完全一致（「同参复现」的判据从
//     三个字段名变成可查的事实）。
//  2. **改任一决定结果的输入 ⇒ 身份变**：N6 点名的八项（费率 / 滑点 / 票池 /
//     日历 / 复权 / 内核 / FillTiming / seed）逐项单独改一处，其余不动。
//  3. **写入是一次、失败是硬失败**：每 run 恰好落一次；sink 写不进去时 run
//     不得被当成完成 —— 一份查不到输入的结果比没有结果更危险。
//
// 走的是真实 RunBacktest（不是直接调 builder）：留痕是执行路径的一部分，
// 只在单测里算 hash 证明不了它接上了。

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/ruoxizhnya/tellurion-contracts/engine"
	"github.com/ruoxizhnya/tellurion-contracts/fees"
	"tellurion/pkg/marketdata"
	"tellurion/pkg/risk"
	"tellurion/pkg/storage"
	"tellurion/pkg/strategy"
)

// ───────────────────────── 替身 ─────────────────────────

// provenanceCapture 是 RunProvenanceWriter 的测试替身：记录每次写入。
type provenanceCapture struct {
	mu    sync.Mutex
	recs  []storage.RunProvenanceRecord
	err   error
	calls int
}

func (c *provenanceCapture) SaveRunProvenance(_ context.Context, rec *storage.RunProvenanceRecord) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.err != nil {
		return c.err
	}
	c.recs = append(c.recs, *rec)
	return nil
}

func (c *provenanceCapture) last(t *testing.T) storage.RunProvenanceRecord {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	require.NotEmpty(t, c.recs, "provenance 没有被写入")
	return c.recs[len(c.recs)-1]
}

func (c *provenanceCapture) all() []storage.RunProvenanceRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]storage.RunProvenanceRecord(nil), c.recs...)
}

// qt63ProbeName 是夹具策略的注册名。参数留痕与成交腿共用同一个探针：
// 它声明了三个不同类型的默认参数，并在第 16 个交易日吐一条买入信号 ——
// 有成交，回测才会是 completed（零成交按 OBS-01 是 invalid），
// 而「输入身份」的断言不该建立在一份 invalid 结果上。
const qt63ProbeName = "qt63-provenance-probe"

// 探针只在第 probeFireIdx 天发信号：ATR 等风控输入已有足够历史，
// 且 next_open 在次日成交时仍在区间内。
const qt63ProbeFireIdx = 15

type qt63ProbeStrategy struct {
	*strategy.BaseStrategy
}

func (s *qt63ProbeStrategy) Parameters() []strategy.Parameter {
	return []strategy.Parameter{
		{Name: "lookback", Type: "int", Default: 20},
		{Name: "top_n", Type: "int", Default: 5},
		{Name: "rebalance", Type: "string", Default: "weekly"},
	}
}

func (s *qt63ProbeStrategy) Weight(strategy.Signal, float64) float64 { return 0.05 }

func (s *qt63ProbeStrategy) GenerateSignals(_ context.Context, bars map[string][]domain.OHLCV, _ *domain.Portfolio) ([]strategy.Signal, error) {
	series := bars["600000.SH"]
	if len(series) == 0 {
		return nil, nil
	}
	last := series[len(series)-1]
	if !last.Date.Equal(provenanceTestStart.AddDate(0, 0, qt63ProbeFireIdx)) {
		return nil, nil
	}
	return []strategy.Signal{{
		Symbol:    "600000.SH",
		Action:    "buy",
		Direction: domain.DirectionLong,
		Strength:  1.0,
		Date:      last.Date,
	}}, nil
}

var qt63ProbeOnce sync.Once

// registerQT63Probe 注册夹具策略（幂等：同进程重复跑测试不重复注册）。
func registerQT63Probe(t *testing.T) {
	t.Helper()
	qt63ProbeOnce.Do(func() {
		probe := &qt63ProbeStrategy{
			BaseStrategy: strategy.NewBaseStrategy(qt63ProbeName, "QT-63 provenance 探针"),
		}
		require.NoError(t, strategy.GlobalRegisterFactory(func() strategy.Strategy { return probe }))
	})
}

// ───────────────────────── 夹具 ─────────────────────────

var provenanceTestStart = time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)

// provenanceBaseOptions 是每个夹具都要的装配：in-process 风控（nil 会让
// detectRegime 去连 :8083，测试里等到超时）、静默日志、固定信号源。
func provenanceBaseOptions(t *testing.T) []EngineOption {
	t.Helper()
	rm, err := risk.NewRiskManager(risk.RiskManagerConfig{
		TargetVolatility:    0.15,
		MaxPositionWeight:   0.10,
		MinPositionWeight:   0.01,
		ATRPeriod:           14,
		BaseMultiplier:      2.0,
		BullMultiplier:      1.5,
		BearMultiplier:      3.0,
		SidewaysMultiplier:  2.0,
		TakeProfitMult:      3.0,
		VolLookbackDays:     60,
		AnnualizationFactor: math.Sqrt(252),
		FastMAPeriod:        50,
		SlowMAPeriod:        200,
		RegimeVolLookback:   120,
	}, zerolog.Nop())
	require.NoError(t, err)
	return []EngineOption{
		WithLogger(zerolog.Nop()),
		WithRiskManager(rm),
	}
}

func provenanceViper(seed int64, commission, slippage, stampTax float64) *viper.Viper {
	v := viper.New()
	v.Set("backtest.initial_capital", 1_000_000.0)
	v.Set("backtest.commission_rate", commission)
	v.Set("backtest.slippage_rate", slippage)
	v.Set("backtest.risk_free_rate", 0.03)
	v.Set("backtest.seed", seed)
	v.Set("trading.stamp_tax_rate", stampTax)
	return v
}

func provenanceProvider(t *testing.T, symbols []string, days int) marketdata.Provider {
	t.Helper()
	prov := marketdata.NewInMemoryProvider()
	bars := synthOHLCV(symbols, days, provenanceTestStart)
	for sym, b := range bars {
		prov.LoadOHLCV(sym, b)
	}
	tradingDays := make([]time.Time, 0, days)
	for d := 0; d < days; d++ {
		tradingDays = append(tradingDays, provenanceTestStart.AddDate(0, 0, d))
	}
	prov.SetTradingDays(tradingDays)
	stocks := make([]domain.Stock, 0, len(symbols))
	for _, sym := range symbols {
		stocks = append(stocks, domain.Stock{Symbol: sym, Name: "QT63 " + sym, ListDate: provenanceTestStart.AddDate(-1, 0, 0)})
	}
	prov.LoadStocks(stocks)
	return prov
}

// provenanceScenario 是一个完整的 run 夹具。零值 = 基线。
type provenanceScenario struct {
	seed       int64   // 0 → 用 42
	commission float64 // 0 → 默认
	slippage   float64 // 0 → 默认
	stampTax   float64 // 0 → 默认
	days       int     // 0 → 20
	pool       []string
	kernel     string
	fillTiming string
	anchors    AdjustmentAnchorStore
	writer     RunProvenanceWriter
	engineOpts []EngineOption
}

func provenanceBaseline() provenanceScenario {
	return provenanceScenario{
		seed:       42,
		commission: fees.DefaultCommissionRate,
		slippage:   fees.DefaultSlippageRate,
		stampTax:   fees.DefaultStampTaxRate,
		days:       20,
		pool:       []string{"600000.SH", "600001.SH"},
	}
}

// run 执行一次真实回测并返回写入的 provenance 记录。
func (sc provenanceScenario) run(t *testing.T, runID string) (storage.RunProvenanceRecord, *BacktestResponse) {
	t.Helper()
	registerQT63Probe(t)

	days := sc.days
	if days == 0 {
		days = 20
	}
	seed := sc.seed
	if seed == 0 {
		seed = 42
	}
	commission := sc.commission
	if commission == 0 {
		commission = fees.DefaultCommissionRate
	}
	slippage := sc.slippage
	if slippage == 0 {
		slippage = fees.DefaultSlippageRate
	}
	stampTax := sc.stampTax
	if stampTax == 0 {
		stampTax = fees.DefaultStampTaxRate
	}

	prov := provenanceProvider(t, sc.pool, days)
	opts := provenanceBaseOptions(t)
	if sc.anchors != nil {
		opts = append(opts, WithAdjustmentAnchorStore(sc.anchors))
	}
	if sc.writer != nil {
		opts = append(opts, WithRunProvenanceWriter(sc.writer))
	}
	opts = append(opts, sc.engineOpts...)

	eng, err := NewEngine(provenanceViper(seed, commission, slippage, stampTax), prov, zerolog.Nop())
	require.NoError(t, err)
	applyOptions(eng, opts)

	resp, err := eng.RunBacktest(context.Background(), BacktestRequest{
		Strategy:       qt63ProbeName,
		StockPool:      sc.pool,
		StartDate:      provenanceTestStart.Format("2006-01-02"),
		EndDate:        provenanceTestStart.AddDate(0, 0, days-1).Format("2006-01-02"),
		InitialCapital: 1_000_000,
		RiskFreeRate:   0.03,
		Kernel:         sc.kernel,
		FillTiming:     sc.fillTiming,
		RunID:          runID,
	})
	require.NoError(t, err, "基线 run 不应失败")
	require.Equal(t, StatusCompleted, resp.Status)

	capture, ok := sc.writer.(*provenanceCapture)
	if sc.writer == nil {
		// 没有 sink：只需证明 run 照常完成（既有路径零变化）。
		return storage.RunProvenanceRecord{}, resp
	}
	require.True(t, ok, "场景必须注入 *provenanceCapture")
	return capture.last(t), resp
}

// ───────────────────────── 1. 同输入 ⇒ 同身份 ─────────────────────────

func TestRunProvenance_SameInputSameIdentity(t *testing.T) {
	capture := &provenanceCapture{}
	sc := provenanceBaseline()
	sc.writer = capture

	a, _ := sc.run(t, "qt63-run-a")
	b, _ := sc.run(t, "qt63-run-b")

	assert.Equal(t, "qt63-run-a", a.RunID)
	assert.Equal(t, "qt63-run-b", b.RunID, "RunID 必须原样落到 provenance（join 锚点）")
	assert.Equal(t, a.RunSpecHash, b.RunSpecHash, "同一份生效输入必须得到同一个 run_spec_hash")
	assert.Equal(t, a.DatasetSnapshotHash, b.DatasetSnapshotHash)
	assert.Equal(t, a.ParamsHash, b.ParamsHash)
	assert.Len(t, a.RunSpecHash, 64)
	assert.Len(t, a.DatasetSnapshotHash, 64)
	assert.Len(t, a.ParamsHash, 64)

	assert.Equal(t, 2, capture.calls, "每个 run 恰好写一次 provenance")
}

// ───────────────────────── 2. 改一处 ⇒ 身份变 ─────────────────────────

func TestRunProvenance_HashMovesWithEveryDecidingKnob(t *testing.T) {
	baseCapture := &provenanceCapture{}
	baseScenario := provenanceBaseline()
	baseScenario.writer = baseCapture
	base, _ := baseScenario.run(t, "qt63-base")
	baseHash := base.RunSpecHash

	// 复权口径：有锚点存储（pinned）vs 无（unpinned）。两者的价格刻度
	// 约束不同，必须 hash 不同。
	anchorCapture := &provenanceCapture{}
	anchorScenario := provenanceBaseline()
	anchorScenario.writer = anchorCapture
	anchorScenario.anchors = newMemAnchorStore()
	anchored, _ := anchorScenario.run(t, "qt63-anchors")
	require.NotEqual(t, baseHash, anchored.RunSpecHash, "复权口径/锚点不进 hash")
	require.NotEqual(t, base.DatasetSnapshotHash, anchored.DatasetSnapshotHash,
		"adjustment_basis 是 dataset 身份的一部分")

	cases := []struct {
		name  string
		apply func(*provenanceScenario)
		check func(t *testing.T, base, got storage.RunProvenanceRecord)
	}{
		{
			name:  "费率（印花税）",
			apply: func(sc *provenanceScenario) { sc.stampTax = fees.DefaultStampTaxRateBefore },
			check: func(t *testing.T, base, got storage.RunProvenanceRecord) {
				assert.NotEqual(t, base.RunSpecHash, got.RunSpecHash)
			},
		},
		{
			name:  "滑点",
			apply: func(sc *provenanceScenario) { sc.slippage = 0.0005 },
			check: func(t *testing.T, base, got storage.RunProvenanceRecord) {
				assert.NotEqual(t, base.RunSpecHash, got.RunSpecHash)
			},
		},
		{
			name:  "票池",
			apply: func(sc *provenanceScenario) { sc.pool = []string{"600000.SH"} },
			check: func(t *testing.T, base, got storage.RunProvenanceRecord) {
				assert.NotEqual(t, base.RunSpecHash, got.RunSpecHash)
				assert.NotEqual(t, base.DatasetSnapshotHash, got.DatasetSnapshotHash)
			},
		},
		{
			name:  "日历",
			apply: func(sc *provenanceScenario) { sc.days = 19 },
			check: func(t *testing.T, base, got storage.RunProvenanceRecord) {
				assert.NotEqual(t, base.RunSpecHash, got.RunSpecHash)
				assert.NotEqual(t, base.DatasetSnapshotHash, got.DatasetSnapshotHash)
			},
		},
		{
			name:  "内核",
			apply: func(sc *provenanceScenario) { sc.kernel = "nautilus" },
			check: func(t *testing.T, base, got storage.RunProvenanceRecord) {
				assert.NotEqual(t, base.RunSpecHash, got.RunSpecHash)
				assert.Equal(t, "nautilus", got.Kernel)
			},
		},
		{
			name:  "FillTiming",
			apply: func(sc *provenanceScenario) { sc.fillTiming = contracts.FillTimingSameClose.String() },
			check: func(t *testing.T, base, got storage.RunProvenanceRecord) {
				assert.NotEqual(t, base.RunSpecHash, got.RunSpecHash)
				assert.Equal(t, contracts.FillTimingSameClose.String(), got.FillTiming)
			},
		},
		{
			name:  "seed",
			apply: func(sc *provenanceScenario) { sc.seed = 7 },
			check: func(t *testing.T, base, got storage.RunProvenanceRecord) {
				assert.NotEqual(t, base.RunSpecHash, got.RunSpecHash)
				assert.Equal(t, int64(7), got.Seed)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := &provenanceCapture{}
			sc := provenanceBaseline()
			sc.writer = capture
			tc.apply(&sc)
			got, _ := sc.run(t, "qt63-"+tc.name)
			tc.check(t, base, got)
		})
	}
}

// ───────────────────────── 3. 写入语义 ─────────────────────────

func TestRunProvenance_WriteFailureFailsTheRun(t *testing.T) {
	capture := &provenanceCapture{err: storage.ErrRunProvenanceConflict}
	sc := provenanceBaseline()
	sc.writer = capture
	registerQT63Probe(t)

	prov := provenanceProvider(t, sc.pool, sc.days)
	eng, err := NewEngine(provenanceViper(sc.seed, sc.commission, sc.slippage, sc.stampTax), prov, zerolog.Nop())
	require.NoError(t, err)
	applyOptions(eng, append(provenanceBaseOptions(t), WithRunProvenanceWriter(capture)))

	resp, runErr := eng.RunBacktest(context.Background(), BacktestRequest{
		Strategy:  "qt63-provenance-probe",
		StockPool: sc.pool,
		StartDate: provenanceTestStart.Format("2006-01-02"),
		EndDate:   provenanceTestStart.AddDate(0, 0, sc.days-1).Format("2006-01-02"),
		RunID:     "qt63-conflict",
	})

	require.Error(t, runErr, "留痕写不进去时 run 必须失败（不留无身份的结果）")
	assert.ErrorIs(t, runErr, storage.ErrRunProvenanceConflict)
	require.NotNil(t, resp)
	assert.Equal(t, StatusFailed, resp.Status)
	assert.Contains(t, resp.Error, "run provenance")
}

func TestRunProvenance_NoSinkIsNoop(t *testing.T) {
	sc := provenanceBaseline()
	// 不注入 writer、也没有 store：引擎照常跑完，不因为「没地方留痕」而失败。
	_, resp := sc.run(t, "qt63-no-sink")
	require.Equal(t, StatusCompleted, resp.Status)
}

func TestRunProvenance_RunIDAlignment(t *testing.T) {
	capture := &provenanceCapture{}
	sc := provenanceBaseline()
	sc.writer = capture
	rec, _ := sc.run(t, "backtest-job-42")
	assert.Equal(t, "backtest-job-42", rec.RunID,
		"job 路径的 run_id 必须等于 backtest_jobs.id，join 才成立")
}

// ───────────────────────── 4. 生效参数 ─────────────────────────

func TestRunProvenance_RecordsDeclaredParameterDefaults(t *testing.T) {
	registerQT63Probe(t)

	capture := &provenanceCapture{}
	sc := provenanceBaseline()
	sc.writer = capture

	prov := provenanceProvider(t, sc.pool, sc.days)
	eng, err := NewEngine(provenanceViper(sc.seed, sc.commission, sc.slippage, sc.stampTax), prov, zerolog.Nop())
	require.NoError(t, err)
	applyOptions(eng, append(provenanceBaseOptions(t), WithRunProvenanceWriter(capture)))

	_, err = eng.RunBacktest(context.Background(), BacktestRequest{
		Strategy:  qt63ProbeName,
		StockPool: sc.pool,
		StartDate: provenanceTestStart.Format("2006-01-02"),
		EndDate:   provenanceTestStart.AddDate(0, 0, sc.days-1).Format("2006-01-02"),
		RunID:     "qt63-params",
	})
	require.NoError(t, err)

	rec := capture.last(t)
	assert.Equal(t, qt63ProbeName, rec.StrategyRefInput, "today 的请求只有 latest 形态：id 不带 @hash")

	var params map[string]any
	require.NoError(t, json.Unmarshal(rec.ParamsJSON, &params))
	assert.Equal(t, map[string]any{
		"lookback":  float64(20),
		"top_n":     float64(5),
		"rebalance": "weekly",
	}, params)

	wantHash, err := contracts.ParamsHash(map[string]any{
		"lookback":  20,
		"top_n":     5,
		"rebalance": "weekly",
	})
	require.NoError(t, err)
	assert.Equal(t, wantHash, rec.ParamsHash,
		"params_hash 必须由生效参数算出（int 与 float64 同身份）")
}

// ───────────────────────── 5. dataset 身份的构成 ─────────────────────────

func TestRunProvenance_DatasetIdentityMatchesFrozenDefinition(t *testing.T) {
	capture := &provenanceCapture{}
	sc := provenanceBaseline()
	sc.writer = capture
	rec, _ := sc.run(t, "qt63-dataset")

	// 引擎的交易日轴 = 区间内每个日历日（本夹具的工作日排列）。
	days := make([]time.Time, 0, sc.days)
	for d := 0; d < sc.days; d++ {
		days = append(days, provenanceTestStart.AddDate(0, 0, d))
	}
	want := contracts.DatasetSnapshotInput{
		Universe:    sc.pool,
		Window:      contracts.DateRange{Start: provenanceTestStart, End: provenanceTestStart.AddDate(0, 0, sc.days-1)},
		Frequency:   "1d",
		DataVersion: "",
		Calendar:    days,
		// 无锚点存储 ⇒ unpinned。
		AdjustmentBasis: adjustmentBasisUnpinned,
	}.Hash()

	assert.Equal(t, want, rec.DatasetSnapshotHash)
	assert.Equal(t, "go", rec.Kernel)
	assert.Equal(t, contracts.FillTimingNextOpen.String(), rec.FillTiming, "空 fill_timing 的生效值是默认口径")
}

// ───────────────────────── 6. bundle 完整性 ─────────────────────────

// TestRunProvenance_RecordShapeIsComplete 钉住 ⑦ 的列清单：少一列就是
// 「结果能查到，但答不出当初用的什么」。空值列也必须出现（空串 = 未登记，
// 不是「没记」）。
func TestRunProvenance_RecordShapeIsComplete(t *testing.T) {
	capture := &provenanceCapture{}
	sc := provenanceBaseline()
	sc.writer = capture
	rec, _ := sc.run(t, "qt63-shape")

	assert.NotEmpty(t, rec.RunID)
	assert.NotEmpty(t, rec.RunSpecHash)
	assert.NotEmpty(t, rec.DatasetSnapshotHash)
	assert.NotEmpty(t, rec.ParamsHash)
	assert.NotEmpty(t, rec.Kernel)
	assert.NotEmpty(t, rec.EngineVersion)
	assert.NotEmpty(t, rec.FillTiming)
	assert.NotEmpty(t, rec.StrategyRefInput)
	assert.Equal(t, int64(42), rec.Seed)
	// 内置策略没有独立字节码：artifact_hash / version_hash 如实为空。
	assert.Empty(t, rec.ArtifactHash)
	assert.Empty(t, rec.RiskRulesHash)

	// ParamsJSON 必须是合法 JSON（DB 列是 JSONB，非法内容会被拒）。
	assert.True(t, json.Valid(rec.ParamsJSON))
}

// TestEffectiveFeeRules_CoversEveryCostKnob 防「漏一个标量」：这些键少了
// 任何一个，改那个配置就不会改 hash。
func TestEffectiveFeeRules_CoversEveryCostKnob(t *testing.T) {
	eng := newTestEngine(t)
	rules := eng.effectiveFeeRules()

	want := []string{
		"commission", "min_commission", "stamp_tax", "stamp_tax_before",
		"transfer_fee", "price_limit_normal", "price_limit_st",
		"price_limit_st_before", "price_limit_new", "new_stock_days",
		"liquidity_factor", "impact_sigma", "impact_liquidity_factor",
	}
	got := make([]string, 0, len(rules))
	for k := range rules {
		got = append(got, k)
	}
	sort.Strings(got)
	sortedWant := append([]string(nil), want...)
	sort.Strings(sortedWant)
	assert.Equal(t, sortedWant, got)
}

// TestEngineVersion_NonEmpty 是 N6-5 的最低线：实际执行核必须有个可读标签。
func TestEngineVersion_NonEmpty(t *testing.T) {
	assert.NotEmpty(t, engineVersion())
}
