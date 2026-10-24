package backtest

// QT-26 验收腿：WASM SignalProvider 接线。
//
//  1. 手工 artifact（无需编译服务）能跑通一次真实回测：加载 → 逐 bar →
//     emit_target2 → 成交；
//  2. 同一策略逻辑的 Go 实现与 WASM 实现逐位一致（分层加仓：t=5 目标 5%，
//     t=8 加到 9%）；
//  3. emit_target2 的手数 = floor(|w|×equity/price/lot)×lot，且 policy cap
//     优先（min），截断计入 sizing_clamped_total；
//  4. symbol scope 的 artifact 被明确拒绝（QT-25 未接线）；
//  5. 混用 emit_signal / emit_target2 在同一 bar 同一 symbol 被 trap
//     （host 层用例见 pkg/sandbox/wasm 的 qt26_emit_test.go —— 契约的
//     族互斥让「真 artifact 混导」在 Compile 就失败，运行时混用只能由
//     host 函数直接验证）。

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
	contracts "github.com/ruoxizhnya/tellurion-contracts/engine"
	"tellurion/pkg/strategy"
)

// ─── 手工 artifact 编码（只在测试里用）─────────────────────────────

type qt26FuncType struct {
	params  []byte
	results []byte
}

type qt26Import struct {
	name    string
	typeIdx uint32
}

type qt26Export struct {
	name string
	kind byte
	idx  uint32
}

type qt26Module struct {
	types    []qt26FuncType
	imports  []qt26Import
	funcs    []uint32
	memPages uint32
	globals  []int32
	// globalMutable 与 globals 一一对应（可缺省）：缺省 = 不可变（旧用例的
	// 宿主区声明）。状态机需要的可写 globals 在这里标 true。
	globalMutable []bool
	exports       []qt26Export
	codes         [][]byte
	// funcLocals 与 codes 一一对应（可缺省）：每项是 code section 的 locals
	// 声明原文。缺省 = 0 组（旧用例全是无局部变量的模块）。
	funcLocals [][]byte
}

func (m *qt26Module) encode() []byte {
	var buf []byte
	buf = append(buf, 0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00)

	buf = qt26Section(buf, 1, func(s []byte) []byte {
		s = qt26Uleb(s, uint32(len(m.types)))
		for _, ft := range m.types {
			s = append(s, 0x60)
			s = qt26Uleb(s, uint32(len(ft.params)))
			s = append(s, ft.params...)
			s = qt26Uleb(s, uint32(len(ft.results)))
			s = append(s, ft.results...)
		}
		return s
	})
	buf = qt26Section(buf, 2, func(s []byte) []byte {
		s = qt26Uleb(s, uint32(len(m.imports)))
		for _, imp := range m.imports {
			s = qt26Uleb(s, uint32(len("env")))
			s = append(s, "env"...)
			s = qt26Uleb(s, uint32(len(imp.name)))
			s = append(s, imp.name...)
			s = append(s, 0x00)
			s = qt26Uleb(s, imp.typeIdx)
		}
		return s
	})
	buf = qt26Section(buf, 3, func(s []byte) []byte {
		s = qt26Uleb(s, uint32(len(m.funcs)))
		for _, idx := range m.funcs {
			s = qt26Uleb(s, idx)
		}
		return s
	})
	buf = qt26Section(buf, 5, func(s []byte) []byte {
		s = qt26Uleb(s, 1)
		s = append(s, 0x00)
		return qt26Uleb(s, m.memPages)
	})
	buf = qt26Section(buf, 6, func(s []byte) []byte {
		s = qt26Uleb(s, uint32(len(m.globals)))
		for i, g := range m.globals {
			mut := byte(0x00)
			if i < len(m.globalMutable) && m.globalMutable[i] {
				mut = 0x01
			}
			s = append(s, 0x7f, mut, 0x41)
			s = qt26Sleb(s, int64(g))
			s = append(s, 0x0b)
		}
		return s
	})
	buf = qt26Section(buf, 7, func(s []byte) []byte {
		s = qt26Uleb(s, uint32(len(m.exports)))
		for _, e := range m.exports {
			s = qt26Uleb(s, uint32(len(e.name)))
			s = append(s, e.name...)
			s = append(s, e.kind)
			s = qt26Uleb(s, e.idx)
		}
		return s
	})
	buf = qt26Section(buf, 10, func(s []byte) []byte {
		s = qt26Uleb(s, uint32(len(m.codes)))
		for i, body := range m.codes {
			var locals []byte
			if i < len(m.funcLocals) && len(m.funcLocals[i]) > 0 {
				locals = m.funcLocals[i]
			} else {
				locals = []byte{0x00}
			}
			fn := append(append([]byte{}, locals...), body...)
			s = qt26Uleb(s, uint32(len(fn)))
			s = append(s, fn...)
		}
		return s
	})
	return buf
}

func qt26Section(buf []byte, id byte, build func([]byte) []byte) []byte {
	content := build(nil)
	buf = append(buf, id)
	buf = qt26Uleb(buf, uint32(len(content)))
	return append(buf, content...)
}

func qt26Uleb(b []byte, v uint32) []byte {
	for {
		c := byte(v & 0x7f)
		v >>= 7
		if v == 0 {
			return append(b, c)
		}
		b = append(b, c|0x80)
	}
}

func qt26Sleb(b []byte, v int64) []byte {
	for {
		c := byte(v & 0x7f)
		v >>= 7
		done := (v == 0 && c&0x40 == 0) || (v == -1 && c&0x40 != 0)
		if done {
			return append(b, c)
		}
		b = append(b, c|0x80)
	}
}

func qt26F64(b []byte, v float64) []byte {
	var tmp [8]byte
	binary.LittleEndian.PutUint64(tmp[:], math.Float64bits(v))
	b = append(b, 0x44)
	return append(b, tmp[:]...)
}

// qt26LayeredArtifact：v2 market scope 手工 artifact。
//
// imports: get_bar2(i32,i32,i32)->f64 (idx 0), emit_target2(i32,f64)->void (idx 1)
// exports: initialize / on_bar / finalize + 三个宿主区 global
//
// on_bar(t)：
//
//	if t >= 8      → emit_target2(0, 0.09)
//	else if t >=5  → emit_target2(0, 0.05)
//	else           → 不动
//
// finalize 不发信号（期末强平由引擎负责，两边一致）。
func qt26LayeredArtifact() []byte {
	const (
		opLocalGet = 0x20
		opI32Const = 0x41
		opI64Const = 0x42
		opCall     = 0x10
		opDrop     = 0x1a
		opIf       = 0x04
		opElse     = 0x05
		opEnd      = 0x0b
		opI32GeS   = 0x4e
		blockVoid  = 0x40
	)

	onBar := []byte{
		// get_bar2(0,0,0); drop —— 证明真实回测里数据面可达（结果不参与决策）。
		opI32Const, 0x00, opI32Const, 0x00, opI32Const, 0x00, opCall, 0x00, opDrop,
		// if t >= 8
		opLocalGet, 0x00, opI32Const, 0x08, opI32GeS,
		opIf, blockVoid,
		opI32Const, 0x00,
	}
	onBar = qt26F64(onBar, 0.09)
	onBar = append(onBar, opCall, 0x01, opElse)
	// else if t >= 5
	onBar = append(onBar, opLocalGet, 0x00, opI32Const, 0x05, opI32GeS, opIf, blockVoid, opI32Const, 0x00)
	onBar = qt26F64(onBar, 0.05)
	onBar = append(onBar, opCall, 0x01, opElse, opEnd, opEnd)
	onBar = append(onBar, opI64Const, 0x00, opEnd)

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
		funcs:    []uint32{2, 3, 4},
		memPages: 2, // 128 KiB：容纳 params[4096,8192) + scratch[8192,73728)
		globals:  []int32{4096, 8192, 65536},
		exports: []qt26Export{
			{name: "initialize", kind: 0, idx: 2},
			{name: "on_bar", kind: 0, idx: 3},
			{name: "finalize", kind: 0, idx: 4},
			{name: contracts.ExportHostParamsPtr, kind: 3, idx: 0},
			{name: contracts.ExportHostScratchPtr, kind: 3, idx: 1},
			{name: contracts.ExportHostScratchSize, kind: 3, idx: 2},
		},
		codes: [][]byte{
			{opI32Const, 0x00, opEnd},
			onBar,
			{opI64Const, 0x00, opEnd},
		},
	}
	return m.encode()
}

// ─── Go 对照实现（同一分层加仓逻辑）────────────────────────────────

const qt26GoName = "qt26-layered-go"
const qt26WasmName = "qt26-layered-wasm"

type qt26GoProbe struct{ *strategy.BaseStrategy }

func (s *qt26GoProbe) Parameters() []strategy.Parameter { return nil }
func (s *qt26GoProbe) Configure(map[string]any) error   { return nil }
func (s *qt26GoProbe) Weight(strategy.Signal, float64) float64 {
	return 0
}

func (s *qt26GoProbe) GenerateSignals(_ context.Context, bars map[string][]domain.OHLCV, _ *domain.Portfolio) ([]strategy.Signal, error) {
	series := bars["600000.SH"]
	if len(series) == 0 {
		return nil, nil
	}
	t := len(series) - 1
	var w float64
	switch {
	case t >= 8:
		w = 0.09
	case t >= 5:
		w = 0.05
	default:
		return nil, nil
	}
	return []strategy.Signal{{
		Symbol:    "600000.SH",
		Direction: domain.DirectionLong,
		Strength:  w,
		Date:      series[len(series)-1].Date,
		Metadata: map[string]interface{}{
			signalMetaTargetWeight: w,
			signalMetaSizingSource: sizingSourceEmitTarget2,
		},
	}}, nil
}

var qt26Once sync.Once

func registerQT26Probe(t *testing.T) {
	t.Helper()
	qt26Once.Do(func() {
		require.NoError(t, strategy.GlobalRegisterFactory(func() strategy.Strategy {
			return &qt26GoProbe{BaseStrategy: strategy.NewBaseStrategy(qt26GoName, "QT-26 Go 对照")}
		}))
	})
}

// ─── 验收 ─────────────────────────────────────────────────────────

const qt26Days = 12

func runQT26(t *testing.T, strategyName string, withWasm bool, logSink *bytes.Buffer) *BacktestResponse {
	t.Helper()
	prov := provenanceProvider(t, []string{"600000.SH"}, qt26Days)
	eng, err := NewEngine(provenanceViper(42, 0.0003, 0.001, 0.001), prov, zerolog.Nop())
	require.NoError(t, err)

	opts := provenanceBaseOptions(t)
	opts = append(opts, WithLogger(zerolog.New(logSink)))
	if withWasm {
		opts = append(opts, WithWASMArtifacts(WASMArtifact{
			Name:  strategyName,
			Bytes: qt26LayeredArtifact(),
			ABI:   contracts.HostAbiV2,
			Scope: contracts.ScopeMarket,
		}))
	}
	applyOptions(eng, opts)

	resp, err := eng.RunBacktest(context.Background(), BacktestRequest{
		Strategy:  strategyName,
		StockPool: []string{"600000.SH"},
		StartDate: provenanceTestStart.Format("2006-01-02"),
		EndDate:   provenanceTestStart.AddDate(0, 0, qt26Days-1).Format("2006-01-02"),
		// same_close：分层日当天成交，便于按日期断言。
		FillTiming:     FillTimingSameClose.String(),
		InitialCapital: 1_000_000,
		RiskFreeRate:   0.03,
	})
	require.NoError(t, err)
	return resp
}

func TestWASMProvider_RealBacktestFromArtifactMatchesGo(t *testing.T) {
	registerQT26Probe(t)

	var goLog bytes.Buffer
	goResp := runQT26(t, qt26GoName, false, &goLog)
	require.Equal(t, StatusCompleted, goResp.Status)

	var wasmLog bytes.Buffer
	wasmResp := runQT26(t, qt26WasmName, true, &wasmLog)
	require.Equal(t, StatusCompleted, wasmResp.Status)

	// 分层加仓至少两笔买入：t=5（第 6 天）与 t=8（第 9 天）。
	expectedLayerDays := []time.Time{
		provenanceTestStart.AddDate(0, 0, 5),
		provenanceTestStart.AddDate(0, 0, 8),
	}
	for _, resp := range []*BacktestResponse{goResp, wasmResp} {
		var buys []domain.Trade
		for _, tr := range resp.Trades {
			if tr.Direction == domain.DirectionLong {
				buys = append(buys, tr)
			}
		}
		require.GreaterOrEqual(t, len(buys), 2, "分层加仓至少两笔买入")
		assert.Equal(t, expectedLayerDays[0], buys[0].Timestamp)
		assert.Equal(t, expectedLayerDays[1], buys[1].Timestamp)
	}

	// 逐位一致：成交（日期/方向/数量/价格）与净值序列。
	require.Len(t, wasmResp.Trades, len(goResp.Trades), "Go/WASM 成交笔数一致")
	for i := range goResp.Trades {
		g, w := goResp.Trades[i], wasmResp.Trades[i]
		assert.Equal(t, g.Timestamp, w.Timestamp, "trade[%d].timestamp", i)
		assert.Equal(t, g.Symbol, w.Symbol, "trade[%d].symbol", i)
		assert.Equal(t, g.Direction, w.Direction, "trade[%d].direction", i)
		assert.Equal(t, g.Quantity, w.Quantity, "trade[%d].quantity", i)
		assert.Equal(t, g.Price, w.Price, "trade[%d].price", i)
	}
	require.Len(t, wasmResp.PortfolioValues, len(goResp.PortfolioValues))
	for i := range goResp.PortfolioValues {
		assert.Equal(t, goResp.PortfolioValues[i].TotalValue, wasmResp.PortfolioValues[i].TotalValue,
			"净值第 %d 点不一致", i)
	}

	// sizing 来源与 cap 计数可见（run 级诊断）。
	assert.Contains(t, wasmLog.String(), "sizing_source_emit_target2")
	assert.Contains(t, wasmLog.String(), "sizing_clamped_total")
}

// TestWASMProvider_SymbolScopeRunsThroughShards 钉 QT-25 的接线：同一个
// 分层加仓 artifact 以 scope=symbol 跑完，成交与 market scope 版本逐位一致
// （单标的时两种实例模型在语义上等价；分片细节见 qt25_* 用例）。
func TestWASMProvider_SymbolScopeRunsThroughShards(t *testing.T) {
	prov := provenanceProvider(t, []string{"600000.SH"}, qt26Days)
	eng, err := NewEngine(provenanceViper(42, 0.0003, 0.001, 0.001), prov, zerolog.Nop())
	require.NoError(t, err)
	applyOptions(eng, append(provenanceBaseOptions(t),
		WithWASMArtifacts(WASMArtifact{
			Name:  qt26WasmName,
			Bytes: qt26LayeredArtifact(),
			ABI:   contracts.HostAbiV2,
			Scope: contracts.ScopeSymbol,
		}),
		WithWASMShardBudget(wasmShardTestBudget(2)),
	))

	resp, err := eng.RunBacktest(context.Background(), BacktestRequest{
		Strategy:       qt26WasmName,
		StockPool:      []string{"600000.SH"},
		StartDate:      provenanceTestStart.Format("2006-01-02"),
		EndDate:        provenanceTestStart.AddDate(0, 0, qt26Days-1).Format("2006-01-02"),
		FillTiming:     FillTimingSameClose.String(),
		InitialCapital: 1_000_000,
		RiskFreeRate:   0.03,
	})
	require.NoError(t, err, "QT-25 起 symbol scope 已接线，不应再 fail-closed")
	require.Equal(t, StatusCompleted, resp.Status)
	var buys []domain.Trade
	for _, tr := range resp.Trades {
		if tr.Direction == domain.DirectionLong {
			buys = append(buys, tr)
		}
	}
	require.GreaterOrEqual(t, len(buys), 2, "分层加仓在 symbol scope 下同样成交")
}

// TestTargetWeightSizing_FormulaAndCap 是 sizing 公式与 policy cap 的纯函数腿。
func TestTargetWeightSizing_FormulaAndCap(t *testing.T) {
	eng := &Engine{}
	diag := newFillRunDiag()
	portfolio := &domain.Portfolio{TotalValue: 1_000_000}
	sig := domain.Signal{
		Symbol:    "600000.SH",
		Direction: domain.DirectionLong,
		Metadata:  map[string]any{signalMetaTargetWeight: 0.05},
	}

	// 目标 5% × 100 万 / 10 元 = 5000 股；policy 给 8000 ⇒ 取 5000，不算截断。
	ps := eng.applyTargetWeightSizing(domain.PositionSize{Size: 8000}, sig, portfolio, 10, diag)
	assert.Equal(t, 5000.0, ps.Size)
	assert.Equal(t, 1, diag.sizingByTarget)
	assert.Equal(t, 0, diag.sizingClamped)

	// policy 只给 3000 ⇒ cap 优先，取 3000，计一次截断。
	ps = eng.applyTargetWeightSizing(domain.PositionSize{Size: 3000}, sig, portfolio, 10, diag)
	assert.Equal(t, 3000.0, ps.Size)
	assert.Equal(t, 1, diag.sizingClamped)

	// 手数取整：3% × 100 万 / 33 元 = 909.09 → floor(9.09 lot)=900。
	sig.Metadata[signalMetaTargetWeight] = 0.03
	ps = eng.applyTargetWeightSizing(domain.PositionSize{Size: 9999}, sig, portfolio, 33, diag)
	assert.Equal(t, 900.0, ps.Size)
}
