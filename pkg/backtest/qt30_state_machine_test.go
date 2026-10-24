package backtest

// QT-30 · M0：市场级状态机 PoC（Go 内置 vs WASM 逐位一致）。
//
// 状态机（两种实现逐条对应）：
//
//	20 日简单均线 regime：price > MA×1.01 → 牛；price < MA×0.99 → 熊；否则震荡
//	regime 切换 → 冷却 3 天（期间不交易）
//	总仓位闸门：牛 60% / 震荡 30% / 熊 0%（单标的即总仓位）
//	目标仓位变化才 emit_target2（与 Go 版「目标不变不发信号」一致）
//
// WASM 产物是 env-only 的 abi v2 模块（只 import get_bar2 / emit_target2），
// 由测试内的小型汇编器逐条写出——这样 CI 不需要 TinyGo/Docker 也能复现。
// TinyGo 侧的缺口与复现命令见 docs/TASKS.md 的 QT-30 施工记录（如实登记）。

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
	contracts "github.com/ruoxizhnya/tellurion-contracts/engine"
	"tellurion/pkg/risk"
	"tellurion/pkg/strategy"
)

// ─── 极简 wasm 汇编器（只覆盖本用例需要的指令）──────────────────────

type qt30Asm struct{ b []byte }

func (a *qt30Asm) raw(xs ...byte) *qt30Asm { a.b = append(a.b, xs...); return a }

func (a *qt30Asm) uleb(v uint32) *qt30Asm { a.b = qt26Uleb(a.b, v); return a }

func (a *qt30Asm) i32(v int64) *qt30Asm {
	a.b = append(a.b, 0x41) // i32.const
	a.b = qt26Sleb(a.b, v)
	return a
}

func (a *qt30Asm) f64(v float64) *qt30Asm {
	var tmp [8]byte
	binary.LittleEndian.PutUint64(tmp[:], math.Float64bits(v))
	a.b = append(a.b, 0x44) // f64.const
	a.b = append(a.b, tmp[:]...)
	return a
}

func (a *qt30Asm) localGet(i uint32) *qt30Asm  { return a.raw(0x20).uleb(i) }
func (a *qt30Asm) localSet(i uint32) *qt30Asm  { return a.raw(0x21).uleb(i) }
func (a *qt30Asm) globalGet(i uint32) *qt30Asm { return a.raw(0x23).uleb(i) }
func (a *qt30Asm) globalSet(i uint32) *qt30Asm { return a.raw(0x24).uleb(i) }
func (a *qt30Asm) call(i uint32) *qt30Asm      { return a.raw(0x10).uleb(i) }
func (a *qt30Asm) ifVoid() *qt30Asm            { return a.raw(0x04, 0x40) }
func (a *qt30Asm) elseVoid() *qt30Asm          { return a.raw(0x05) }
func (a *qt30Asm) end() *qt30Asm               { return a.raw(0x0b) }
func (a *qt30Asm) br(depth uint32) *qt30Asm    { return a.raw(0x0c).uleb(depth) }
func (a *qt30Asm) brIf(depth uint32) *qt30Asm  { return a.raw(0x0d).uleb(depth) }
func (a *qt30Asm) ret() *qt30Asm               { return a.raw(0x0f) }
func (a *qt30Asm) drop() *qt30Asm              { return a.raw(0x1a) }

// 局部变量编号（on_bar）：
//
//	0 = t（参数）  1 = k  2 = r  3 = 保留  4 = targetPct
//	5 = sum        6 = ma 7 = price
const (
	qt30LocalT         = 0
	qt30LocalK         = 1
	qt30LocalR         = 2
	qt30LocalTargetPct = 4
	qt30LocalSum       = 5
	qt30LocalMA        = 6
	qt30LocalPrice     = 7
)

const (
	qt30GlobalLastRegime = 0
	qt30GlobalCooldown   = 1
	qt30GlobalLastTarget = 2
)

// qt30OnBar 写出一段状态机 on_bar 的字节码（对照 Go 版逐条看）。
func qt30OnBar() []byte {
	a := &qt30Asm{}

	// if t < 20 { return 0 }
	a.localGet(qt30LocalT).i32(20).raw(0x48).ifVoid() // i32.lt_s
	a.raw(0x42, 0x00).ret()                           // i64.const 0
	a.end()

	// sum = 0; k = 0
	a.f64(0).localSet(qt30LocalSum)
	a.i32(0).localSet(qt30LocalK)

	// block { loop { if k >= 20 break; sum += get_bar2(0,-k,close); k++ } }
	a.raw(0x02, 0x40)                                // block
	a.raw(0x03, 0x40)                                // loop
	a.localGet(qt30LocalK).i32(20).raw(0x4e).brIf(1) // i32.ge_s
	a.localGet(qt30LocalSum)
	a.i32(0)                                // symbol_idx
	a.i32(0).localGet(qt30LocalK).raw(0x6b) // -k
	a.i32(0)                                // field = close
	a.call(0)                               // get_bar2
	a.raw(0xa0)                             // f64.add
	a.localSet(qt30LocalSum)
	a.localGet(qt30LocalK).i32(1).raw(0x6a).localSet(qt30LocalK)
	a.br(0)
	a.end()
	a.end()

	// ma = sum / 20
	a.localGet(qt30LocalSum).f64(20).raw(0xa3).localSet(qt30LocalMA) // f64.div

	// price = get_bar2(0,0,close)
	a.i32(0).i32(0).i32(0).call(0).localSet(qt30LocalPrice)

	// r = 0
	a.i32(0).localSet(qt30LocalR)

	// if price > ma*1.01 { r = 1 } else if price < ma*0.99 { r = -1 }
	a.localGet(qt30LocalPrice).localGet(qt30LocalMA).f64(1.01).raw(0xa2).raw(0x64) // f64.gt
	a.ifVoid()
	a.i32(1).localSet(qt30LocalR)
	a.elseVoid()
	a.localGet(qt30LocalPrice).localGet(qt30LocalMA).f64(0.99).raw(0xa2).raw(0x63) // f64.lt
	a.ifVoid()
	a.i32(-1).localSet(qt30LocalR)
	a.end()
	a.end()

	// if r != lastRegime { lastRegime = r; cooldown = 3 } else if cooldown > 0 { cooldown-- }
	a.localGet(qt30LocalR).globalGet(qt30GlobalLastRegime).raw(0x47).ifVoid() // i32.ne
	a.localGet(qt30LocalR).globalSet(qt30GlobalLastRegime)
	a.i32(3).globalSet(qt30GlobalCooldown)
	a.elseVoid()
	a.globalGet(qt30GlobalCooldown).i32(0).raw(0x4a).ifVoid() // i32.gt_s
	a.globalGet(qt30GlobalCooldown).i32(1).raw(0x6b).globalSet(qt30GlobalCooldown)
	a.end()
	a.end()

	// if cooldown > 0 { return 0 }
	a.globalGet(qt30GlobalCooldown).i32(0).raw(0x4a).ifVoid()
	a.raw(0x42, 0x00).ret()
	a.end()

	// targetPct = 0; if r == 1 { 60 } else if r == 0 { 30 }
	a.i32(0).localSet(qt30LocalTargetPct)
	a.localGet(qt30LocalR).i32(1).raw(0x46).ifVoid() // i32.eq
	a.i32(60).localSet(qt30LocalTargetPct)
	a.elseVoid()
	a.localGet(qt30LocalR).i32(0).raw(0x46).ifVoid()
	a.i32(30).localSet(qt30LocalTargetPct)
	a.end()
	a.end()

	// if targetPct != lastTargetPct { emit_target2(0, targetPct/100); lastTargetPct = targetPct }
	a.localGet(qt30LocalTargetPct).globalGet(qt30GlobalLastTarget).raw(0x47).ifVoid()
	a.i32(0)
	a.localGet(qt30LocalTargetPct).raw(0xb7) // f64.convert_i32_s
	a.f64(100).raw(0xa3)                     // f64.div
	a.call(1)                                // emit_target2
	a.localGet(qt30LocalTargetPct).globalSet(qt30GlobalLastTarget)
	a.end()

	a.raw(0x42, 0x00) // i64.const 0
	a.end()
	return a.b
}

// qt30Module 造一个 abi v2 market-scope 模块（宿主区声明齐全）。
func qt30Module(onBar []byte, locals []byte) []byte {
	m := &qt26Module{
		types: []qt26FuncType{
			{params: []byte{0x7f, 0x7f, 0x7f}, results: []byte{0x7c}}, // get_bar2
			{params: []byte{0x7f, 0x7c}},                              // emit_target2
			{params: []byte{0x7f, 0x7f}, results: []byte{0x7f}},       // initialize
			{params: []byte{0x7f}, results: []byte{0x7e}},             // on_bar
			{results: []byte{0x7e}},                                   // finalize
		},
		imports: []qt26Import{
			{name: contracts.HostFnGetBar2, typeIdx: 0},
			{name: contracts.HostFnEmitTarget2, typeIdx: 1},
		},
		funcs:         []uint32{2, 3, 4},
		memPages:      2,
		globals:       []int32{0, 0, -1, 4096, 8192, 65536},
		globalMutable: []bool{true, true, true, false, false, false},
		exports: []qt26Export{
			{name: "initialize", kind: 0, idx: 2},
			{name: "on_bar", kind: 0, idx: 3},
			{name: "finalize", kind: 0, idx: 4},
			{name: contracts.ExportHostParamsPtr, kind: 3, idx: 3},
			{name: contracts.ExportHostScratchPtr, kind: 3, idx: 4},
			{name: contracts.ExportHostScratchSize, kind: 3, idx: 5},
		},
		codes: [][]byte{
			{0x41, 0x00, 0x0b}, // initialize → 0
			onBar,
			{0x42, 0x00, 0x0b}, // finalize → 0
		},
		funcLocals: [][]byte{nil, locals, nil},
	}
	return m.encode()
}

// qt30StateMachineArtifact：状态机（局部变量：4×i32 + 3×f64）。
func qt30StateMachineArtifact() []byte {
	return qt30Module(qt30OnBar(), []byte{0x02, 0x04, 0x7f, 0x03, 0x7c})
}

// qt30InfiniteArtifact：on_bar 里 while(true)（W1 的端到端终止验证）。
func qt30InfiniteArtifact() []byte {
	onBar := []byte{
		0x03, 0x40, // loop
		0x0c, 0x00, // br 0
		0x0b,       // end
		0x42, 0x00, // i64.const 0（不可达，保持函数形状合法）
		0x0b, // end
	}
	return qt30Module(onBar, nil)
}

// ─── Go 内置对照实现（逐条对应上面 wasm 字节码）────────────────────

const (
	qt30GoName       = "qt30-state-machine-go"
	qt30WASMName     = "qt30-state-machine-wasm"
	qt30InfiniteName = "qt30-while-true-wasm"
)

type qt30GoProbe struct {
	*strategy.BaseStrategy
	lastRegime    int
	cooldown      int
	lastTargetPct int
}

func newQT30GoProbe() strategy.Strategy {
	return &qt30GoProbe{
		BaseStrategy: strategy.NewBaseStrategy(qt30GoName, "QT-30 Go 内置状态机（regime + 冷却 + 总仓位闸门）"),
		// 与 wasm 的 global init -1 对齐：0% 也是要发一次 close 的目标。
		lastTargetPct: -1,
	}
}

func (s *qt30GoProbe) Parameters() []strategy.Parameter { return nil }
func (s *qt30GoProbe) Configure(map[string]any) error   { return nil }
func (s *qt30GoProbe) Weight(strategy.Signal, float64) float64 {
	return 0
}

func (s *qt30GoProbe) GenerateSignals(_ context.Context, bars map[string][]domain.OHLCV, _ *domain.Portfolio) ([]strategy.Signal, error) {
	series := bars["600000.SH"]
	t := len(series) - 1
	if t < 20 {
		return nil, nil
	}
	sum := 0.0
	for k := 0; k < 20; k++ {
		sum += series[t-k].Close
	}
	ma := sum / 20.0
	price := series[t].Close

	r := 0
	if price > ma*1.01 {
		r = 1
	} else if price < ma*0.99 {
		r = -1
	}
	if r != s.lastRegime {
		s.lastRegime = r
		s.cooldown = 3
	} else if s.cooldown > 0 {
		s.cooldown--
	}
	if s.cooldown > 0 {
		return nil, nil
	}

	targetPct := 0
	if r == 1 {
		targetPct = 60
	} else if r == 0 {
		targetPct = 30
	}
	if targetPct == s.lastTargetPct {
		return nil, nil
	}
	s.lastTargetPct = targetPct

	w := float64(targetPct) / 100.0
	dir := domain.DirectionLong
	if w == 0 {
		dir = domain.DirectionClose
	}
	return []strategy.Signal{{
		Symbol:    "600000.SH",
		Direction: dir,
		Strength:  math.Abs(w),
		Date:      series[t].Date,
		Metadata: map[string]interface{}{
			signalMetaTargetWeight: w,
			signalMetaSizingSource: sizingSourceEmitTarget2,
		},
	}}, nil
}

var qt30Once sync.Once

func registerQT30Probe(t *testing.T) {
	t.Helper()
	qt30Once.Do(func() {
		require.NoError(t, strategy.GlobalRegisterFactory(newQT30GoProbe))
	})
}

// ─── 夹具 ─────────────────────────────────────────────────────────

const qt30Days = 727 // M0 的验收口径：2 年 ≈ 727 个交易日

// qt30RiskOptions 给总仓位闸门留出空间（默认风控把单票权重压在 10%，
// 那样 60% 的目标永远落不到成交上，测不出三层目标）。
func qt30RiskOptions(t *testing.T) []EngineOption {
	t.Helper()
	rm, err := risk.NewRiskManager(risk.RiskManagerConfig{
		TargetVolatility:    0.15,
		MaxPositionWeight:   0.8,
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
	return []EngineOption{WithLogger(zerolog.Nop()), WithRiskManager(rm)}
}

type qt30SignalDay struct {
	Date    time.Time
	Signals []domain.Signal
}

func runQT30(t *testing.T, strategyName string, artifact *WASMArtifact, days int) (*BacktestResponse, []qt30SignalDay) {
	t.Helper()
	prov := provenanceProvider(t, []string{"600000.SH"}, days)
	eng, err := NewEngine(provenanceViper(7, 0.0003, 0.001, 0.001), prov, zerolog.Nop())
	require.NoError(t, err)

	opts := qt30RiskOptions(t)
	if artifact != nil {
		opts = append(opts, WithWASMArtifacts(*artifact))
	}
	applyOptions(eng, opts)

	var observed []qt30SignalDay
	eng.SetSignalObserver(func(date time.Time, signals []domain.Signal) {
		cp := make([]domain.Signal, len(signals))
		copy(cp, signals)
		observed = append(observed, qt30SignalDay{Date: date, Signals: cp})
	})

	resp, err := eng.RunBacktest(context.Background(), BacktestRequest{
		Strategy:       strategyName,
		StockPool:      []string{"600000.SH"},
		StartDate:      provenanceTestStart.Format("2006-01-02"),
		EndDate:        provenanceTestStart.AddDate(0, 0, days-1).Format("2006-01-02"),
		FillTiming:     FillTimingSameClose.String(),
		InitialCapital: 1_000_000,
		RiskFreeRate:   0.03,
	})
	require.NoError(t, err, "strategy=%s 应跑完", strategyName)
	return resp, observed
}

func qt30TargetWeight(s domain.Signal) (float64, bool) {
	v, ok := s.Metadata[signalMetaTargetWeight]
	if !ok {
		return 0, false
	}
	w, ok := v.(float64)
	return w, ok
}

// ─── 验收：Go 内置 vs WASM 逐位一致 ───────────────────────────────

func TestQT30_GoVsWASMStateMachineParity(t *testing.T) {
	registerQT30Probe(t)

	goResp, goSignals := runQT30(t, qt30GoName, nil, qt30Days)
	wasmResp, wasmSignals := runQT30(t, qt30WASMName, &WASMArtifact{
		Name:  qt30WASMName,
		Bytes: qt30StateMachineArtifact(),
		ABI:   contracts.HostAbiV2,
		Scope: contracts.ScopeMarket,
	}, qt30Days)

	require.Equal(t, StatusCompleted, goResp.Status)
	require.Equal(t, StatusCompleted, wasmResp.Status)

	// 727 次 on_bar：引擎每个交易日调用一次 provider，观察钩子必须收到 727 天。
	require.Len(t, goSignals, qt30Days, "Go 版必须跑满 727 个交易日")
	require.Len(t, wasmSignals, qt30Days, "WASM 版必须跑满 727 次 on_bar")

	// 信号序列逐位一致（方向 / 强度 / 目标权重）。
	for i := range goSignals {
		g, w := goSignals[i], wasmSignals[i]
		require.Equal(t, g.Date, w.Date, "day[%d] date", i)
		require.Len(t, w.Signals, len(g.Signals), "day[%d] 信号条数", i)
		for k := range g.Signals {
			assert.Equal(t, g.Signals[k].Symbol, w.Signals[k].Symbol, "day[%d] signal[%d].symbol", i, k)
			assert.Equal(t, g.Signals[k].Direction, w.Signals[k].Direction, "day[%d] signal[%d].direction", i, k)
			assert.Equal(t, g.Signals[k].Strength, w.Signals[k].Strength, "day[%d] signal[%d].strength", i, k)
			gw, gok := qt30TargetWeight(g.Signals[k])
			ww, wok := qt30TargetWeight(w.Signals[k])
			assert.Equal(t, gok, wok, "day[%d] signal[%d] target_weight 存在性", i, k)
			assert.Equal(t, gw, ww, "day[%d] signal[%d].target_weight", i, k)
		}
	}

	// 成交逐笔一致：日期 / 标的 / 方向 / 数量 / 价格。
	require.Len(t, wasmResp.Trades, len(goResp.Trades), "成交笔数一致")
	require.Greater(t, len(goResp.Trades), 0, "状态机必须真的成交（否则「逐位一致」没有意义）")
	for i := range goResp.Trades {
		g, w := goResp.Trades[i], wasmResp.Trades[i]
		assert.Equal(t, g.Timestamp, w.Timestamp, "trade[%d].timestamp", i)
		assert.Equal(t, g.Symbol, w.Symbol, "trade[%d].symbol", i)
		assert.Equal(t, g.Direction, w.Direction, "trade[%d].direction", i)
		assert.Equal(t, g.Quantity, w.Quantity, "trade[%d].quantity", i)
		assert.Equal(t, g.Price, w.Price, "trade[%d].price", i)
	}

	// 净值序列逐位一致。
	require.Len(t, wasmResp.PortfolioValues, len(goResp.PortfolioValues), "净值序列长度一致")
	for i := range goResp.PortfolioValues {
		assert.Equal(t, goResp.PortfolioValues[i].TotalValue, wasmResp.PortfolioValues[i].TotalValue,
			"净值第 %d 点不一致", i)
	}

	// 三层目标都真的出现过（60% / 30% / 0%），证明 regime + 闸门在工作。
	targets := map[float64]int{}
	for _, day := range goSignals {
		for _, s := range day.Signals {
			if w, ok := qt30TargetWeight(s); ok {
				targets[w]++
			}
		}
	}
	assert.Greater(t, targets[0.6], 0, "必须出现牛市 60% 目标")
	assert.Greater(t, targets[0.3], 0, "必须出现震荡 30% 目标")
	assert.Greater(t, targets[0.0], 0, "必须出现熊市清仓（0%）目标")
}

// ─── 验收：while(true) 被预算终止且 run failed ────────────────────

func TestQT30_WhileTrueTerminatedByBudget(t *testing.T) {
	prov := provenanceProvider(t, []string{"600000.SH"}, 10)
	eng, err := NewEngine(provenanceViper(7, 0.0003, 0.001, 0.001), prov, zerolog.Nop())
	require.NoError(t, err)
	applyOptions(eng, append(qt30RiskOptions(t), WithWASMArtifacts(WASMArtifact{
		Name:  qt30InfiniteName,
		Bytes: qt30InfiniteArtifact(),
		ABI:   contracts.HostAbiV2,
		Scope: contracts.ScopeMarket,
	})))

	started := time.Now()
	resp, err := eng.RunBacktest(context.Background(), BacktestRequest{
		Strategy:       qt30InfiniteName,
		StockPool:      []string{"600000.SH"},
		StartDate:      provenanceTestStart.Format("2006-01-02"),
		EndDate:        provenanceTestStart.AddDate(0, 0, 9).Format("2006-01-02"),
		FillTiming:     FillTimingSameClose.String(),
		InitialCapital: 1_000_000,
		RiskFreeRate:   0.03,
	})
	elapsed := time.Since(started)

	require.Error(t, err, "while(true) 必须让 run 失败，不能返回半个结果")
	require.NotNil(t, resp)
	assert.Equal(t, StatusFailed, resp.Status)
	assert.True(t, errors.Is(err, contracts.ErrStrategyFault), "预算终止必须归类为 StrategyFault（QT-10）")
	assert.Contains(t, err.Error(), "session terminated")
	assert.Less(t, elapsed, 10*time.Second, "必须被单次调用预算终止；挂死说明 WithCloseOnContextDone 没生效")
}
