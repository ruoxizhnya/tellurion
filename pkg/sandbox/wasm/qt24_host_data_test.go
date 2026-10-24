package wasm

// QT-24 验收腿：host 数据模型 v2（Bars / Axis / T）+ finalize 归属。
//
//  1. market scope 取到 5 个字段；
//  2. 停牌/未上市缺 bar 返回 NaN（不用 0 掩盖）；
//  3. 「整段预加载」模拟下 get_series_len2 仍只返回可见长度；
//  4. t_offset 让 T+off < 0 / > 0 都 trap；field_id / symbol_idx 越界 trap；
//  5. symbol scope 的 symbol_idx 必须为 0；
//  6. finalize 发 close 不再静默丢；发 long 被 trap，返回值只是诊断字节。

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/ruoxizhnya/tellurion-contracts/engine"
)

func qt24MarketContext(t *testing.T) *BarContext {
	t.Helper()
	axis := []time.Time{
		qt24Start,
		qt24Start.AddDate(0, 0, 1),
		qt24Start.AddDate(0, 0, 2),
	}
	// A 有完整 3 天；B 第 2 天停牌（缺 bar）；C 从未有数据。
	bars := [][]domain.OHLCV{
		{
			{Date: axis[0], Open: 10, High: 11, Low: 9, Close: 10.5, Volume: 100},
			{Date: axis[1], Open: 11, High: 12, Low: 10, Close: 11.5, Volume: 200},
			{Date: axis[2], Open: 12, High: 13, Low: 11, Close: 12.5, Volume: 300},
		},
		{
			{Date: axis[0], Open: 20, High: 21, Low: 19, Close: 20.5, Volume: 400},
			{Date: axis[2], Open: 22, High: 23, Low: 21, Close: 22.5, Volume: 600},
		},
		nil,
	}
	return &BarContext{
		T:       1,
		Axis:    axis,
		Symbols: []string{"A", "B", "C"},
		Bars:    bars,
		State:   map[string][]byte{},
	}
}

func TestHostGetBar2_MarketScopeFiveFields(t *testing.T) {
	bc := qt24MarketContext(t)
	ctx := withBarContext(context.Background(), bc)

	cases := []struct {
		field contracts.FieldID
		want  float64
	}{
		{contracts.FieldClose, 11.5},
		{contracts.FieldOpen, 11},
		{contracts.FieldHigh, 12},
		{contracts.FieldLow, 10},
		{contracts.FieldVolume, 200},
	}
	for _, tc := range cases {
		got := hostGetBar2(ctx, 0, 0, int32(tc.field))
		assert.InDelta(t, tc.want, got, 1e-9, "field_id=%d", tc.field)
	}

	// 历史偏移：T-1 应返回第 1 天的 bar。
	assert.InDelta(t, 10.5, hostGetBar2(ctx, 0, -1, int32(contracts.FieldClose)), 1e-9)
}

func TestHostGetBar2_SuspendedOrUnlistedReturnsNaN(t *testing.T) {
	bc := qt24MarketContext(t)
	ctx := withBarContext(context.Background(), bc)

	// B 在 T=1（第 2 天）没有 bar —— 停牌，必须 NaN，不能是 0。
	for _, f := range contracts.FieldIDs() {
		got := hostGetBar2(ctx, 1, 0, int32(f))
		assert.True(t, math.IsNaN(got), "停牌标的 field=%d 必须返回 NaN，got %v", f, got)
	}
	// C 从来没有数据。
	assert.True(t, math.IsNaN(hostGetBar2(ctx, 2, 0, int32(contracts.FieldClose))), "未上市标的必须 NaN")
	// 历史日期上 B 有 bar。
	assert.InDelta(t, 20.5, hostGetBar2(ctx, 1, -1, int32(contracts.FieldClose)), 1e-9)
}

func TestHostGetBar2_TrapsOnFutureNegativeOffsetAndBadIndexes(t *testing.T) {
	bc := qt24MarketContext(t)
	ctx := withBarContext(context.Background(), bc)

	assert.Panics(t, func() { hostGetBar2(ctx, 0, 1, int32(contracts.FieldClose)) }, "t_offset>0 必须 trap")
	assert.Panics(t, func() { hostGetBar2(ctx, 0, -5, int32(contracts.FieldClose)) }, "T+off<0 必须 trap")
	assert.Panics(t, func() { hostGetBar2(ctx, 0, 0, 5) }, "field_id 越界必须 trap")
	assert.Panics(t, func() { hostGetBar2(ctx, 9, 0, int32(contracts.FieldClose)) }, "symbol_idx 越界必须 trap")
}

func TestHostGetBar2_SymbolScopeRequiresSymbolZero(t *testing.T) {
	bc := &BarContext{
		T:       1,
		Symbols: []string{"A"},
		Bars: [][]domain.OHLCV{{
			{Date: qt24Start, Close: 10},
			{Date: qt24Start.AddDate(0, 0, 1), Close: 11},
		}},
		State: map[string][]byte{},
	}
	ctx := withBarContext(context.Background(), bc)

	assert.InDelta(t, 11.0, hostGetBar2(ctx, 0, 0, int32(contracts.FieldClose)), 1e-9)
	assert.Panics(t, func() { hostGetBar2(ctx, 1, 0, int32(contracts.FieldClose)) },
		"symbol scope 下 symbol_idx 必须为 0")
}

func TestHostGetSeriesLen2_ReturnsVisibleLengthOnly(t *testing.T) {
	bc := qt24MarketContext(t)
	// 模拟 X1 dataset snapshot：Axis 与 Bars 都含 5 天（含未来 bar）。
	for i := 3; i <= 4; i++ {
		day := qt24Start.AddDate(0, 0, i)
		bc.Axis = append(bc.Axis, day)
		bc.Bars[0] = append(bc.Bars[0], domain.OHLCV{Date: day, Close: 13.5 + float64(i-3)})
	}
	ctx := withBarContext(context.Background(), bc)

	// T=1 ⇒ 可见日期 ≤ 第 2 天 ⇒ 2 根，而不是 len(Bars[0])=5。
	assert.Equal(t, int32(2), hostGetSeriesLen2(ctx, 0), "get_series_len2 必须只算可见长度")
	bc.T = 3
	assert.Equal(t, int32(4), hostGetSeriesLen2(ctx, 0), "T=3 ⇒ 可见 4 根")
	// B 停牌日：可见 1 根（只有第 1 天）。
	bc.T = 1
	assert.Equal(t, int32(1), hostGetSeriesLen2(ctx, 1))
}

func TestHostGetSeriesLen2_SymbolScopeNeverExposesFuture(t *testing.T) {
	bc := &BarContext{
		T:       1,
		Symbols: []string{"A"},
		Bars: [][]domain.OHLCV{{
			{Date: qt24Start, Close: 10},
			{Date: qt24Start.AddDate(0, 0, 1), Close: 11},
			{Date: qt24Start.AddDate(0, 0, 2), Close: 12}, // 未来
		}},
		State: map[string][]byte{},
	}
	ctx := withBarContext(context.Background(), bc)
	assert.Equal(t, int32(2), hostGetSeriesLen2(ctx, 0), "symbol scope 的可见长度 = min(len, T+1)")
}

func TestHostGetDate2_MarketAndSymbolScope(t *testing.T) {
	bc := qt24MarketContext(t)
	ctx := withBarContext(context.Background(), bc)
	assert.Equal(t, int32(20240103), hostGetDate2(ctx), "Axis[T] = 2024-01-03")

	sym := &BarContext{
		T:       0,
		Symbols: []string{"A"},
		Bars:    [][]domain.OHLCV{{{Date: qt24Start, Close: 10}}},
		State:   map[string][]byte{},
	}
	assert.Equal(t, int32(20240102), hostGetDate2(withBarContext(context.Background(), sym)))
	assert.Panics(t, func() { hostGetDate2(withBarContext(context.Background(), &BarContext{T: 9})) },
		"无回放上下文必须 trap")
}

func TestHostGetBar_V1StillCloseOnly(t *testing.T) {
	bc := &BarContext{
		T: 1,
		Bars: [][]domain.OHLCV{{
			{Date: qt24Start, Open: 1, High: 9, Low: 0, Close: 5, Volume: 100},
			{Date: qt24Start.AddDate(0, 0, 1), Open: 2, High: 10, Low: 1, Close: 6, Volume: 200},
		}},
		State: map[string][]byte{},
	}
	ctx := withBarContext(context.Background(), bc)
	// 即使 field_id=4（volume），v1 也恒取 close —— 行为不变。
	assert.InDelta(t, 6.0, hostGetBar(ctx, 0, int32(contracts.FieldVolume)), 1e-9)
	assert.Equal(t, int32(2), hostGetSeriesLen(ctx))
}

func TestHostGetCrossSection_FromAxisAndBars(t *testing.T) {
	bc := qt24MarketContext(t)
	ctx := withBarContext(context.Background(), bc)
	bc.T = 0
	assert.InDelta(t, 20.5, hostGetCrossSection(ctx, 0, int32(contracts.FieldClose), 1), 1e-9)
	assert.False(t, math.IsNaN(hostGetCrossSection(ctx, 0, 0, 1)), "有 bar 时不是 NaN")

	// T=1 时 B 停牌：横截面缺 bar ⇒ NaN。
	bc.T = 1
	assert.True(t, math.IsNaN(hostGetCrossSection(ctx, 0, 0, 1)))

	// symbol scope（Axis 为空）没有横截面。
	sym := &BarContext{T: 0, Symbols: []string{"A"}, Bars: barsForCloses([]float64{1}), State: map[string][]byte{}}
	assert.True(t, math.IsNaN(hostGetCrossSection(withBarContext(context.Background(), sym), 0, 0, 0)))
}

// ─── finalize 归属 ────────────────────────────────────────────────

// wasmFinalizeEmitter 构造一个 v1 artifact：on_bar 什么都不做，finalize
// 发一条 emit_signal(action, strength)。
func wasmFinalizeEmitter(action int32, strength float64) []byte {
	m := &wasmModule{
		types: []wasmFuncType{
			{params: []byte{valI32, valI32, valF64}},                  // emit_signal
			{params: []byte{valI32, valI32}, results: []byte{valI32}}, // initialize
			{params: []byte{valI32}, results: []byte{valI64}},         // on_bar
			{results: []byte{valI64}},                                 // finalize
		},
		imports: []wasmImport{{module: HostModuleName, name: contracts.HostFnEmitSignal, typeIdx: 0}},
		funcs:   []uint32{1, 2, 3},
		memory:  &wasmMemory{min: 1},
		codes: [][]byte{
			{opI32Const, 0x00, opEnd}, // initialize → 0
			{opI64Const, 0x00, opEnd}, // on_bar → 0
			append([]byte{opI32Const, 0x00, opI32Const, byte(action)}, appendF64Const(nil, strength)...),
		},
	}
	// finalize body: emit_signal(...) ; i64.const 0 ; end
	m.codes[2] = append(m.codes[2], opCall, 0x00, opI64Const, 0x00, opEnd)
	m.export = []wasmExport{
		{name: "initialize", kind: expKindFunc, idx: m.funcIndexBase() + 0},
		{name: "on_bar", kind: expKindFunc, idx: m.funcIndexBase() + 1},
		{name: "finalize", kind: expKindFunc, idx: m.funcIndexBase() + 2},
	}
	return m.encode()
}

func TestFinalize_CloseSignalIsCollectedNotDropped(t *testing.T) {
	r := newHostRuntime(t)
	sb := NewSandbox(r, Config{})
	sess, err := sb.NewOnBarSession(context.Background(), wasmFinalizeEmitter(int32(contracts.ActionClose), 1.0))
	require.NoError(t, err)
	defer sess.Close()
	require.NoError(t, sess.Initialize(context.Background(), nil))

	bc := bcWithCloses([]float64{10, 11}, 1)
	bc.Symbols = []string{"A"}
	require.NoError(t, sess.OnBar(context.Background(), bc))
	require.Empty(t, bc.Signals, "on_bar 不发信号")

	diag, err := sess.Finalize(context.Background(), bc)
	require.NoError(t, err)
	assert.Empty(t, diag, "finalize 返回值是诊断字节，不是信号通道")
	require.Len(t, bc.Signals, 1, "finalize 里的 close 必须留在 bc.Signals，不能静默丢")
	assert.Equal(t, int32(contracts.ActionClose), bc.Signals[0].Action)
	assert.Equal(t, 1.0, bc.Signals[0].Strength)
}

func TestFinalize_LongSignalTraps(t *testing.T) {
	r := newHostRuntime(t)
	sb := NewSandbox(r, Config{})
	sess, err := sb.NewOnBarSession(context.Background(), wasmFinalizeEmitter(int32(contracts.ActionLong), 1.0))
	require.NoError(t, err)
	defer sess.Close()
	require.NoError(t, sess.Initialize(context.Background(), nil))

	bc := bcWithCloses([]float64{10, 11}, 1)
	bc.Symbols = []string{"A"}
	require.NoError(t, sess.OnBar(context.Background(), bc))
	_, err = sess.Finalize(context.Background(), bc)
	require.Error(t, err, "finalize 里发 long 必须 trap")
	assert.Contains(t, err.Error(), "finalize")
}
