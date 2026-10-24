package backtest

// QT-31 · M0b：symbol scope 状态机 PoC（分片 + 分层加仓）。
//
// 每个标的一份状态的状态机（Go 内置与 WASM 逐条对应）：
//
//	5 根 bar 动量：close[t] > close[t-5]×1.01 → 多；< close[t-5]×0.99 → 空；否则震荡
//	多头且 cooldown==0：tier+1（最多 3 层），目标 20% / 40% / 60%，加仓后冷却 2 天
//	转空：清仓（目标 0）、tier 归零、冷却 3 天
//	冷却期内只递减计数、不交易；目标权重不变不 emit
//
// WASM 侧只用 get_bar2 + emit_target2、scope=symbol、env-only abi v2；每个分片
// 一个实例，tier / cooldown / lastTargetPct / lastSide 全部放模块 globals
//（跨批恢复的载体就是线性内存镜像）。
//
// 四条腿：
//  1. 小票池真实回测：Go 内置 vs WASM 逐日信号 / 逐笔成交 / 净值逐位一致；
//  2. policy cap：MaxPositionWeight=0.5 使 tier3=0.6 被截断，
//     sizing_clamped_total > 0 且 Go/WASM 计数相同；
//  3. 5000 标的 provider 级分片跑完：峰值实例 / 线性内存 / 批大小不越预算；
//  4. 跨批恢复（奇数天切点）与一次跑完逐条信号一致。

import (
	"bytes"
	"context"
	"math"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
	contracts "github.com/ruoxizhnya/tellurion-contracts/engine"
	"tellurion/pkg/risk"
	"tellurion/pkg/sandbox/wasm"
	"tellurion/pkg/strategy"
)

// ─── 模块 globals / on_bar locals ─────────────────────────────────

const (
	qt31GlobalTier        = 0
	qt31GlobalCooldown    = 1
	qt31GlobalLastTarget  = 2 // 20/40/60/0；初值 -1 = 还没发过目标
	qt31GlobalLastSide    = 3 // 1 多 / 0 震荡 / -1 空
	qt31GlobalParamsPtr   = 4
	qt31GlobalScratchPtr  = 5
	qt31GlobalScratchSize = 6
)

const (
	qt31LocalT     = 0 // on_bar 参数
	qt31LocalSide  = 1 // i32
	qt31LocalPrice = 2 // f64
	qt31LocalPrev  = 3 // f64（close[t-5]）
)

// qt31MemStateBase 是状态镜像在宿主区之前的固定地址（QT-25 的 mem[128]
// 同款口径，不碰 params[4096..8192] 与 scratch[8192..]）。
//
// globals 是 on_bar 的工作寄存器；检查点只能快照**线性内存**（v2 没有
// get_state/set_state，SnapshotMemory 也不含 globals），所以每个 bar 进出
// 都做一次「内存 → globals → 内存」的同步：Initialize 写入初值，on_bar
// 开头装载、改完写回。跨批恢复因此仍然只靠内存镜像。
const qt31MemStateBase = 128

const qt31MemArgAlign2 = 0x02 // align=2, offset=0 的第一字节

func qt31LoadState(a *qt30Asm) {
	a.i32(qt31MemStateBase+0).raw(0x28, qt31MemArgAlign2, 0x00).globalSet(qt31GlobalTier)       // i32.load
	a.i32(qt31MemStateBase+4).raw(0x28, qt31MemArgAlign2, 0x00).globalSet(qt31GlobalCooldown)   //
	a.i32(qt31MemStateBase+8).raw(0x28, qt31MemArgAlign2, 0x00).globalSet(qt31GlobalLastTarget) //
	a.i32(qt31MemStateBase+12).raw(0x28, qt31MemArgAlign2, 0x00).globalSet(qt31GlobalLastSide)  //
}

func qt31StoreState(a *qt30Asm) {
	a.i32(qt31MemStateBase+0).globalGet(qt31GlobalTier).raw(0x36, qt31MemArgAlign2, 0x00)       // i32.store
	a.i32(qt31MemStateBase+4).globalGet(qt31GlobalCooldown).raw(0x36, qt31MemArgAlign2, 0x00)   //
	a.i32(qt31MemStateBase+8).globalGet(qt31GlobalLastTarget).raw(0x36, qt31MemArgAlign2, 0x00) //
	a.i32(qt31MemStateBase+12).globalGet(qt31GlobalLastSide).raw(0x36, qt31MemArgAlign2, 0x00)  //
}

// qt31OnBar 写出一段状态机 on_bar 的字节码（对照 Go 版逐条看）。
func qt31OnBar() []byte {
	a := &qt30Asm{}

	// 从线性内存装载状态（跨批恢复后 globals 是初值，内存才是真相）。
	qt31LoadState(a)

	// if t < 5 { return 0 } —— 5 根 bar 动量需要 close[t-5]。
	a.localGet(qt31LocalT).i32(5).raw(0x48).ifVoid() // i32.lt_s
	a.raw(0x42, 0x00).ret()                          // i64.const 0
	a.end()

	// if cooldown > 0 { cooldown--; 写回; return 0 } —— 冷却期内不交易。
	a.globalGet(qt31GlobalCooldown).i32(0).raw(0x4a).ifVoid() // i32.gt_s
	a.globalGet(qt31GlobalCooldown).i32(1).raw(0x6b).globalSet(qt31GlobalCooldown)
	qt31StoreState(a)
	a.raw(0x42, 0x00).ret()
	a.end()

	// price = get_bar2(0,0,close); prev = get_bar2(0,-5,close)
	a.i32(0).i32(0).i32(0).call(0).localSet(qt31LocalPrice)
	a.i32(0).i32(-5).i32(0).call(0).localSet(qt31LocalPrev)

	// side = 0
	a.i32(0).localSet(qt31LocalSide)

	// if price > prev*1.01 { side=1 } else if price < prev*0.99 { side=-1 }
	a.localGet(qt31LocalPrice).localGet(qt31LocalPrev).f64(1.01).raw(0xa2).raw(0x64).ifVoid() // f64.mul / f64.gt
	a.i32(1).localSet(qt31LocalSide)
	a.elseVoid()
	a.localGet(qt31LocalPrice).localGet(qt31LocalPrev).f64(0.99).raw(0xa2).raw(0x63).ifVoid() // f64.lt
	a.i32(-1).localSet(qt31LocalSide)
	a.end()
	a.end()

	// if side == -1 {
	//   if lastSide != -1 {                       // 只在「转空」那一次动作
	//     lastSide = -1
	//     if lastTarget != 0 { tier=0; lastTarget=0; emit_target2(0, 0.0) }
	//     cooldown = 3
	//   }
	//   return 0
	// }
	a.localGet(qt31LocalSide).i32(-1).raw(0x46).ifVoid() // i32.eq
	a.globalGet(qt31GlobalLastSide).i32(-1).raw(0x46).ifVoid()
	a.elseVoid()
	a.i32(-1).globalSet(qt31GlobalLastSide)
	a.globalGet(qt31GlobalLastTarget).i32(0).raw(0x46).ifVoid()
	a.elseVoid()
	a.i32(0).globalSet(qt31GlobalTier)
	a.i32(0).globalSet(qt31GlobalLastTarget)
	a.i32(0)
	a.f64(0)
	a.call(1) // emit_target2(0, 0.0) = 清仓
	a.end()
	a.i32(3).globalSet(qt31GlobalCooldown)
	a.end()
	qt31StoreState(a)
	a.raw(0x42, 0x00).ret()
	a.end()

	// lastSide = side
	a.localGet(qt31LocalSide).globalSet(qt31GlobalLastSide)

	// if side == 1 && tier < 3 {
	//   tier++
	//   cooldown = 2
	//   lastTarget = tier*20
	//   emit_target2(0, tier*20/100)
	// }
	a.localGet(qt31LocalSide).i32(1).raw(0x46).ifVoid()
	a.globalGet(qt31GlobalTier).i32(3).raw(0x48).ifVoid() // i32.lt_s
	a.globalGet(qt31GlobalTier).i32(1).raw(0x6a).globalSet(qt31GlobalTier)
	a.i32(2).globalSet(qt31GlobalCooldown)
	a.globalGet(qt31GlobalTier).i32(20).raw(0x6c).globalSet(qt31GlobalLastTarget) // i32.mul
	a.i32(0)
	a.globalGet(qt31GlobalTier).raw(0xb7) // f64.convert_i32_s
	a.f64(20).raw(0xa2)                   // f64.mul
	a.f64(100).raw(0xa3)                  // f64.div
	a.call(1)
	a.end()
	a.end()

	qt31StoreState(a)
	a.raw(0x42, 0x00) // i64.const 0
	a.end()
	return a.b
}

// qt31Initialize 把状态镜像写成初值：lastTarget=-1（还没发过目标）、其余 0。
// provider 在 RestoreMemory 之前调用 initialize；恢复时整段内存被快照覆盖。
func qt31Initialize() []byte {
	a := &qt30Asm{}
	a.i32(qt31MemStateBase+0).i32(0).raw(0x36, qt31MemArgAlign2, 0x00)
	a.i32(qt31MemStateBase+4).i32(0).raw(0x36, qt31MemArgAlign2, 0x00)
	a.i32(qt31MemStateBase+8).i32(-1).raw(0x36, qt31MemArgAlign2, 0x00)
	a.i32(qt31MemStateBase+12).i32(0).raw(0x36, qt31MemArgAlign2, 0x00)
	a.i32(0)
	a.end()
	return a.b
}

// qt31Module 造一个 abi v2 symbol-scope 模块：状态全在可写 globals。
func qt31Module(onBar []byte) []byte {
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
		globals:       []int32{0, 0, -1, 0, 4096, 8192, 65536},
		globalMutable: []bool{true, true, true, true, false, false, false},
		exports: []qt26Export{
			{name: "initialize", kind: 0, idx: 2},
			{name: "on_bar", kind: 0, idx: 3},
			{name: "finalize", kind: 0, idx: 4},
			{name: contracts.ExportHostParamsPtr, kind: 3, idx: qt31GlobalParamsPtr},
			{name: contracts.ExportHostScratchPtr, kind: 3, idx: qt31GlobalScratchPtr},
			{name: contracts.ExportHostScratchSize, kind: 3, idx: qt31GlobalScratchSize},
		},
		codes: [][]byte{
			qt31Initialize(),
			onBar,
			{0x42, 0x00, 0x0b}, // finalize → 0
		},
		// on_bar locals：1×i32（side）+ 2×f64（price/prev）。
		funcLocals: [][]byte{nil, {0x02, 0x01, 0x7f, 0x02, 0x7c}, nil},
	}
	return m.encode()
}

func qt31StateMachineArtifact() []byte {
	return qt31Module(qt31OnBar())
}

// ─── Go 内置对照实现（逐条对应上面 wasm 字节码）────────────────────

const (
	qt31GoName   = "qt31-symbol-layered-go"
	qt31WASMName = "qt31-symbol-layered-wasm"
)

type qt31SymbolState struct {
	tier          int
	cooldown      int
	lastTargetPct int
	lastSide      int
}

type qt31GoProbe struct {
	*strategy.BaseStrategy
	states map[string]*qt31SymbolState
}

func newQT31GoProbe() strategy.Strategy {
	return &qt31GoProbe{
		BaseStrategy: strategy.NewBaseStrategy(qt31GoName, "QT-31 Go 内置 symbol 状态机（动量 + 冷却 + 三层加仓）"),
		states:       make(map[string]*qt31SymbolState),
	}
}

func (s *qt31GoProbe) Parameters() []strategy.Parameter { return nil }
func (s *qt31GoProbe) Configure(map[string]any) error   { return nil }
func (s *qt31GoProbe) Weight(strategy.Signal, float64) float64 {
	return 0
}

func (s *qt31GoProbe) stateFor(sym string) *qt31SymbolState {
	st, ok := s.states[sym]
	if !ok {
		// 与 wasm 的 global init 对齐：lastTarget=-1（还没发过目标），其余为 0。
		st = &qt31SymbolState{lastTargetPct: -1}
		s.states[sym] = st
	}
	return st
}

func (s *qt31GoProbe) GenerateSignals(_ context.Context, bars map[string][]domain.OHLCV, _ *domain.Portfolio) ([]strategy.Signal, error) {
	// 按 symbol 字典序产出：与分片合并顺序（RunUniverse 升序）一致。
	symbols := make([]string, 0, len(bars))
	for sym := range bars {
		symbols = append(symbols, sym)
	}
	sort.Strings(symbols)

	var out []strategy.Signal
	for _, sym := range symbols {
		series := bars[sym]
		if len(series) == 0 {
			continue
		}
		t := len(series) - 1
		if t < 5 {
			continue
		}
		st := s.stateFor(sym)
		if st.cooldown > 0 {
			st.cooldown--
			continue
		}
		price := series[t].Close
		prev := series[t-5].Close

		side := 0
		if price > prev*1.01 {
			side = 1
		} else if price < prev*0.99 {
			side = -1
		}

		if side == -1 {
			if st.lastSide != -1 { // 只在「转空」那一次动作
				st.lastSide = -1
				if st.lastTargetPct != 0 {
					st.tier = 0
					st.lastTargetPct = 0
					out = append(out, qt31TargetSignal(sym, 0, series[t].Date))
				}
				st.cooldown = 3
			}
			continue
		}
		st.lastSide = side
		if side == 1 && st.tier < 3 {
			st.tier++
			st.cooldown = 2
			targetPct := st.tier * 20
			st.lastTargetPct = targetPct
			out = append(out, qt31TargetSignal(sym, float64(targetPct)/100.0, series[t].Date))
		}
	}
	return out, nil
}

func qt31TargetSignal(sym string, w float64, date time.Time) strategy.Signal {
	dir := domain.DirectionLong
	if w == 0 {
		dir = domain.DirectionClose
	}
	return strategy.Signal{
		Symbol:    sym,
		Direction: dir,
		Strength:  math.Abs(w),
		Date:      date,
		Metadata: map[string]interface{}{
			signalMetaTargetWeight: w,
			signalMetaSizingSource: sizingSourceEmitTarget2,
		},
	}
}

var qt31Once sync.Once

func registerQT31Probe(t *testing.T) {
	t.Helper()
	qt31Once.Do(func() {
		require.NoError(t, strategy.GlobalRegisterFactory(newQT31GoProbe))
	})
}

// ─── 夹具 ─────────────────────────────────────────────────────────

// qt31Universe 是 20 个确定性标的（排序后与 RunUniverse 的稳定序一致）。
func qt31Universe(n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, "6000"+pad2(i)+".SH")
	}
	return out
}

func pad2(i int) string {
	if i < 10 {
		return "0" + strconv.Itoa(i)
	}
	return strconv.Itoa(i)
}

// qt31Meta 用与真实回测相同的确定性合成行情（provenanceProvider /
// synthOHLCV 同源，seed=42）。
//
// marketData 必须由调用方**只生成一次**并跨分段共享：synthOHLCV 的 RNG 是按
// symbol 顺序串联的（前一个 symbol 消耗的随机数取决于生成天数），若分段跑
// 按各段的 axis 长度重新生成，第二个标的拿到的就是另一条随机流，恢复对拍
// 会退化成「两段喂了不同行情」。分段时多出来的尾部由 clipSeries 按
// meta.Start/End 裁掉。
func qt31Meta(runID, strategyName string, symbols []string, axis []time.Time, marketData map[string][]domain.OHLCV) strategy.RunMeta {
	return strategy.RunMeta{
		RunID:       runID,
		Strategy:    strategyName,
		Start:       axis[0],
		End:         axis[len(axis)-1],
		Universe:    symbols,
		RunUniverse: symbols,
		Axis:        axis,
		MarketData:  marketData,
	}
}

func qt31Provider(artifact []byte, cfg wasmShardConfig) *wasmSignalProvider {
	return newWASMSignalProvider(WASMArtifact{
		Name:  qt31WASMName,
		Bytes: artifact,
		ABI:   contracts.HostAbiV2,
		Scope: contracts.ScopeSymbol,
	}, cfg, zerolog.Nop())
}

// qt31RiskOptions 把 policy cap 设在 0.5：tier3=0.6 必然被截断；目标
// TargetVolatility 调到 0.45，让 tier1/tier2 尽量落在 cap 之下（三层加仓在
// 成交层也可见，而不是第一层就被 policy 吃满）。
func qt31RiskOptions(t *testing.T) []EngineOption {
	t.Helper()
	rm, err := risk.NewRiskManager(risk.RiskManagerConfig{
		TargetVolatility:    0.45,
		MaxPositionWeight:   0.5,
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

type qt31SignalDay struct {
	Date    time.Time
	Signals []domain.Signal
}

func qt31TargetWeight(s domain.Signal) (float64, bool) {
	return qt31MetadataTargetWeight(s.Metadata)
}

func qt31StrategyTargetWeight(s strategy.Signal) (float64, bool) {
	return qt31MetadataTargetWeight(s.Metadata)
}

func qt31MetadataTargetWeight(md map[string]interface{}) (float64, bool) {
	if md == nil {
		return 0, false
	}
	v, ok := md[signalMetaTargetWeight]
	if !ok {
		return 0, false
	}
	w, ok := v.(float64)
	return w, ok
}

// runQT31 跑一次真实回测（same_close），返回结果与逐日信号观察序列。
func runQT31(t *testing.T, name string, artifact *WASMArtifact, pool []string, days int, logs *bytes.Buffer) (*BacktestResponse, []qt31SignalDay) {
	t.Helper()
	prov := provenanceProvider(t, pool, days)
	eng, err := NewEngine(provenanceViper(7, 0.0003, 0.001, 0.001), prov, zerolog.Nop())
	require.NoError(t, err)

	opts := qt31RiskOptions(t)
	opts = append(opts, WithLogger(zerolog.New(logs)))
	if artifact != nil {
		opts = append(opts,
			WithWASMArtifacts(*artifact),
			WithWASMShardBudget(wasmShardTestBudget(4)),
		)
	}
	applyOptions(eng, opts)

	var observed []qt31SignalDay
	eng.SetSignalObserver(func(date time.Time, signals []domain.Signal) {
		cp := make([]domain.Signal, len(signals))
		copy(cp, signals)
		observed = append(observed, qt31SignalDay{Date: date, Signals: cp})
	})

	started := time.Now()
	resp, err := eng.RunBacktest(context.Background(), BacktestRequest{
		Strategy:       name,
		StockPool:      pool,
		StartDate:      provenanceTestStart.Format("2006-01-02"),
		EndDate:        provenanceTestStart.AddDate(0, 0, days-1).Format("2006-01-02"),
		FillTiming:     FillTimingSameClose.String(),
		InitialCapital: 1_000_000,
		RiskFreeRate:   0.03,
	})
	elapsed := time.Since(started)
	require.NoError(t, err, "strategy=%s 应跑完", name)
	t.Logf("QT-31 %s: %d symbols × %d days, elapsed=%s, trades=%d, status=%s",
		name, len(pool), days, elapsed, len(resp.Trades), resp.Status)
	return resp, observed
}

// ─── 验收 1/3：真实回测 Go 内置 vs WASM 逐位一致 + policy cap ──────

func TestQT31_GoVsWASMSymbolScopeStateMachineParity(t *testing.T) {
	registerQT31Probe(t)

	const (
		days    = 200
		symbols = 20
	)
	pool := qt31Universe(symbols)

	var goLog bytes.Buffer
	goResp, goSignals := runQT31(t, qt31GoName, nil, pool, days, &goLog)

	var wasmLog bytes.Buffer
	wasmResp, wasmSignals := runQT31(t, qt31WASMName, &WASMArtifact{
		Name:  qt31WASMName,
		Bytes: qt31StateMachineArtifact(),
		ABI:   contracts.HostAbiV2,
		Scope: contracts.ScopeSymbol,
	}, pool, days, &wasmLog)

	require.Equal(t, StatusCompleted, goResp.Status)
	require.Equal(t, StatusCompleted, wasmResp.Status)

	// 逐日信号逐位一致（date/symbol/direction/strength/target_weight 存在性与取值）。
	require.Len(t, goSignals, days, "Go 版必须跑满每个交易日")
	require.Len(t, wasmSignals, days, "WASM 版必须跑满每个交易日")
	for i := range goSignals {
		g, w := goSignals[i], wasmSignals[i]
		require.Equal(t, g.Date, w.Date, "day[%d] date", i)
		require.Len(t, w.Signals, len(g.Signals), "day[%d] 信号条数", i)
		for k := range g.Signals {
			assert.Equal(t, g.Signals[k].Symbol, w.Signals[k].Symbol, "day[%d] signal[%d].symbol", i, k)
			assert.Equal(t, g.Signals[k].Direction, w.Signals[k].Direction, "day[%d] signal[%d].direction", i, k)
			assert.Equal(t, g.Signals[k].Strength, w.Signals[k].Strength, "day[%d] signal[%d].strength", i, k)
			gw, gok := qt31TargetWeight(g.Signals[k])
			ww, wok := qt31TargetWeight(w.Signals[k])
			assert.Equal(t, gok, wok, "day[%d] signal[%d] target_weight 存在性", i, k)
			assert.Equal(t, gw, ww, "day[%d] signal[%d].target_weight", i, k)
		}
	}

	// 成交逐笔一致：timestamp / symbol / direction / quantity / price。
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

	// 净值序列逐点一致。
	require.Len(t, wasmResp.PortfolioValues, len(goResp.PortfolioValues), "净值序列长度一致")
	for i := range goResp.PortfolioValues {
		assert.Equal(t, goResp.PortfolioValues[i].TotalValue, wasmResp.PortfolioValues[i].TotalValue,
			"净值第 %d 点不一致", i)
	}

	// 三层目标与清仓都真的出现（不是一直空仓的假一致）。
	targets := map[float64]int{}
	for _, day := range goSignals {
		for _, s := range day.Signals {
			if w, ok := qt31TargetWeight(s); ok {
				targets[w]++
			}
		}
	}
	assert.Greater(t, targets[0.2], 0, "必须出现 tier1 的 20% 目标")
	assert.Greater(t, targets[0.4], 0, "必须出现 tier2 的 40% 目标")
	assert.Greater(t, targets[0.6], 0, "必须出现 tier3 的 60% 目标")
	assert.Greater(t, targets[0.0], 0, "必须出现转空清仓（0%）目标")
	t.Logf("QT-31 target distribution: 0.2=%d 0.4=%d 0.6=%d 0.0=%d",
		targets[0.2], targets[0.4], targets[0.6], targets[0.0])

	// policy cap：两版日志的 sizing_clamped_total 都必须 > 0 且相同。
	require.Contains(t, goLog.String(), "sizing_clamped_total")
	require.Contains(t, wasmLog.String(), "sizing_clamped_total")
	goClamped := qt31LoggedClamps(t, &goLog)
	wasmClamped := qt31LoggedClamps(t, &wasmLog)
	assert.Greater(t, goClamped, 0, "cap=0.5 下 tier3=0.6 必须被截断")
	assert.Equal(t, goClamped, wasmClamped, "Go/WASM 的 sizing_clamped_total 必须相同")
	t.Logf("QT-31 sizing_clamped_total: go=%d wasm=%d", goClamped, wasmClamped)
}

var qt31ClampRE = regexp.MustCompile(`"sizing_clamped_total":(\d+)`)

func qt31LoggedClamps(t *testing.T, buf *bytes.Buffer) int {
	t.Helper()
	m := qt31ClampRE.FindStringSubmatch(buf.String())
	require.NotNil(t, m, "回测日志必须含 sizing_clamped_total 字段")
	n, err := strconv.Atoi(m[1])
	require.NoError(t, err)
	return n
}

// TestQT31_TargetWeightTiersClampedAtPolicyCap 是 §8.2 / QT-26 口径的直接
// 单测：同一份 equity/price 下，目标 0.2/0.4 不被 cap 截断，目标 0.6 在
// policy cap 0.5 下被截断成 0.5 的手数。
func TestQT31_TargetWeightTiersClampedAtPolicyCap(t *testing.T) {
	eng := &Engine{}
	diag := newFillRunDiag()
	portfolio := &domain.Portfolio{TotalValue: 1_000_000}
	const price = 10.0
	capQty := 0.5 * portfolio.TotalValue / price // policy cap = 50000 股

	cases := []struct {
		target float64
		want   float64
		clamp  bool
	}{
		{0.2, 20000, false},
		{0.4, 40000, false},
		{0.6, 50000, true},
	}
	for _, tc := range cases {
		sig := domain.Signal{
			Symbol:    "600000.SH",
			Direction: domain.DirectionLong,
			Metadata:  map[string]any{signalMetaTargetWeight: tc.target},
		}
		ps := eng.applyTargetWeightSizing(domain.PositionSize{Size: capQty}, sig, portfolio, price, diag)
		assert.Equal(t, tc.want, ps.Size, "target=%v", tc.target)
	}
	assert.Equal(t, 3, diag.sizingByTarget)
	assert.Equal(t, 1, diag.sizingClamped, "只有 tier3（0.6）在 cap 0.5 下被截断")
}

// ─── 验收 2：5000 标的 provider 级分片跑完、峰值不越预算 ────────────

// qt31ScaleMeta 造 5000 标的的确定性行情：每天 +0.5%，到 t=5 时 5 根 bar
// 动量必然 >1%，每个分片都恰好发一次 tier1（0.2）。
func qt31ScaleMeta(runID string, symbols []string, days int) strategy.RunMeta {
	axis := qt25Axis(provenanceTestStart, days)
	md := make(map[string][]domain.OHLCV, len(symbols))
	for i, sym := range symbols {
		price := 10.0 + float64(i%7)
		bars := make([]domain.OHLCV, 0, days)
		for d, date := range axis {
			if d > 0 {
				price *= 1.005
			}
			bars = append(bars, domain.OHLCV{
				Symbol: sym, Date: date,
				Open: price, High: price * 1.01, Low: price * 0.99, Close: price,
				Volume: 1_000_000,
			})
		}
		md[sym] = bars
	}
	return strategy.RunMeta{
		RunID:       runID,
		Strategy:    "qt31-scale",
		Start:       axis[0],
		End:         axis[len(axis)-1],
		Universe:    symbols,
		RunUniverse: symbols,
		Axis:        axis,
		MarketData:  md,
	}
}

func TestQT31_5000SymbolsWithinShardBudget(t *testing.T) {
	const (
		symbols = 5000
		days    = 6 // t=5 是最后一根 bar：每个分片一次 tier1 目标
		workers = 4
	)
	universe := make([]string, 0, symbols)
	for i := 0; i < symbols; i++ {
		universe = append(universe, "A"+pad4(i)+".SZ")
	}
	meta := qt31ScaleMeta("qt31-5000", universe, days)

	p := qt31Provider(qt31StateMachineArtifact(), wasmShardConfig{
		Budget: wasmShardTestBudget(workers),
	})

	started := time.Now()
	require.NoError(t, p.BeginRun(context.Background(), meta))
	t.Cleanup(func() { _ = p.EndRun(context.Background(), meta.RunID) })
	elapsed := time.Since(started)

	// 每个分片在最后一根 bar 发一条 0.2 目标 ⇒ 5000 条。少一条就是某个分片没跑。
	assert.Equal(t, symbols, countSignals(p.buffered), "5000 个分片都必须在 t=5 产出 tier1")
	assert.Equal(t, workers, p.stats.BatchSize, "wasm_batch_size = 生效的 W")
	assert.LessOrEqual(t, p.stats.PeakInstances, workers, "同时存活的实例数不得超过预算允许的 W")
	assert.Less(t, p.stats.PeakBytes, int64(workers)*int64(wasm.DefaultMaxMemory),
		"线性内存峰值必须落在 W × 单实例上限之内")
	t.Logf("QT-31 5000 symbols: elapsed=%s peak_instances=%d peak_bytes=%d batch_size=%d signals=%d",
		elapsed, p.stats.PeakInstances, p.stats.PeakBytes, p.stats.BatchSize, countSignals(p.buffered))
}

func pad4(i int) string {
	s := strconv.Itoa(i)
	for len(s) < 4 {
		s = "0" + s
	}
	return s
}

// ─── 验收 4：跨批恢复（奇数天切点）与一次跑完一致 ───────────────────

func qt31CopyBuffered(src map[int64][]strategy.Signal) map[int64][]strategy.Signal {
	out := make(map[int64][]strategy.Signal, len(src))
	for day, sigs := range src {
		out[day] = append([]strategy.Signal(nil), sigs...)
	}
	return out
}

func qt31SignalDays(m map[int64][]strategy.Signal) []int64 {
	days := make([]int64, 0, len(m))
	for day := range m {
		days = append(days, day)
	}
	sort.Slice(days, func(i, j int) bool { return days[i] < days[j] })
	return days
}

func TestQT31_ResumeAcrossBatchesMatchesSingleRun(t *testing.T) {
	symbols := []string{"600000.SH", "600001.SH"}
	axis := qt25Axis(provenanceTestStart, 60)
	// 一次生成全区间行情，分段共享（否则 synthOHLCV 的串联 RNG 会让分段
	// 拿到不同行情；见 qt31Meta 注释）。
	marketData := synthOHLCV(symbols, len(axis), provenanceTestStart)
	full := qt31Meta("qt31-full", qt31WASMName, symbols, axis, marketData)

	// 一次跑完（参照）。
	ref := qt31Provider(qt31StateMachineArtifact(), wasmShardConfig{
		Budget:          wasmShardTestBudget(2),
		CheckpointEvery: 1,
	})
	qt25Run(t, ref, full)
	refSignals := qt31CopyBuffered(ref.buffered)
	require.Greater(t, countSignals(refSignals), 0, "参照跑必须真的产生信号，否则恢复对拍没有意义")

	// 第一段：跑到第 7 天结束（奇数切点，暴露 tier/cooldown 未恢复）。
	cut := 6
	part1Meta := qt31Meta("qt31-part1", qt31WASMName, symbols, axis[:cut+1], marketData)
	part1 := qt31Provider(qt31StateMachineArtifact(), wasmShardConfig{
		Budget:          wasmShardTestBudget(2),
		CheckpointEvery: 1,
	})
	qt25Run(t, part1, part1Meta)

	resume := make(map[string]shardCheckpoint, len(symbols))
	for _, sym := range symbols {
		require.Contains(t, part1.checks, sym, "第一段结束必须留下分片检查点")
		cp := part1.checks[sym]
		assert.Equal(t, axis[cut], cp.Ts, "检查点必须落在日期边界")
		resume[sym] = cp
	}

	// 第二段：从检查点恢复（内存镜像里含 tier/cooldown/lastTarget/lastSide）。
	part2 := qt31Provider(qt31StateMachineArtifact(), wasmShardConfig{
		Budget:          wasmShardTestBudget(2),
		CheckpointEvery: 1,
		Resume:          resume,
	})
	qt25Run(t, part2, full)

	got := qt31CopyBuffered(part1.buffered)
	for day, sigs := range part2.buffered {
		got[day] = append(got[day], sigs...)
	}

	t.Logf("QT-31 resume signal days: ref=%v got=%v", qt31SignalDays(refSignals), qt31SignalDays(got))
	require.Len(t, got, len(refSignals), "拼接后的信号交易日数应与一次跑完相同")
	for day, want := range refSignals {
		require.Len(t, got[day], len(want), "day=%d 信号条数", day)
		for i := range want {
			g := got[day][i]
			assert.Equal(t, want[i].Symbol, g.Symbol, "day=%d signal[%d].symbol", day, i)
			assert.Equal(t, want[i].Direction, g.Direction, "day=%d signal[%d].direction", day, i)
			assert.Equal(t, want[i].Strength, g.Strength, "day=%d signal[%d].strength", day, i)
			ww, wok := qt31StrategyTargetWeight(want[i])
			gw, gok := qt31StrategyTargetWeight(g)
			assert.Equal(t, wok, gok, "day=%d signal[%d] target_weight 存在性", day, i)
			assert.Equal(t, ww, gw, "day=%d signal[%d].target_weight", day, i)
		}
	}
}
