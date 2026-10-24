package wasm

// QT-26 的 host 层验收腿：目标权重收集 / 非法值 trap / 两套 sizing 混用 trap。
//
// 契约的「族互斥」让一个真 artifact 不可能同时 import emit_signal（v1）与
// emit_target2（v2）—— 混导会在 Compile 阶段被拒。因此运行时混用只能用
// host 函数直接验证；这条守卫仍然必要：将来若把 emit_signal 提升为 v2
// 共用名，混用就会从「不可能」变成「可能」。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/tellurion-contracts/engine"
)

func qt26EmitContext() (*BarContext, context.Context) {
	bc := &BarContext{
		T:       0,
		Symbols: []string{"A", "B"},
		Bars:    barsForCloses([]float64{10}),
		State:   map[string][]byte{},
	}
	return bc, withBarContext(context.Background(), bc)
}

func TestHostEmitTarget2_CollectsAndValidates(t *testing.T) {
	bc, ctx := qt26EmitContext()
	hostEmitTarget2(ctx, 0, 0.5)
	require.Len(t, bc.Targets, 1)
	assert.Equal(t, int32(0), bc.Targets[0].SymbolIdx)
	assert.Equal(t, 0.5, bc.Targets[0].Weight)

	bc.resetEmissionBar()
	assert.Panics(t, func() { hostEmitTarget2(ctx, 0, 1.5) }, "weight 越界必须 trap")
	bc.resetEmissionBar()
	assert.Panics(t, func() { hostEmitTarget2(ctx, 0, 2) }, "symbol_idx 越界必须 trap")
}

func TestHostEmitTarget2_FinalizeOnlyClearOrHold(t *testing.T) {
	bc, ctx := qt26EmitContext()
	bc.inFinalize = true

	bc.resetEmissionBar()
	hostEmitTarget2(ctx, 0, 0)  // 清仓：允许
	hostEmitTarget2(ctx, 1, -1) // 不动：允许
	require.Len(t, bc.Targets, 2)

	bc.resetEmissionBar()
	assert.Panics(t, func() { hostEmitTarget2(ctx, 0, 0.5) }, "finalize 里开新仓必须 trap")
}

func TestHostEmitSourcesMixed_Traps(t *testing.T) {
	bc, ctx := qt26EmitContext()

	hostEmitSignal(ctx, 0, int32(contracts.ActionLong), 1.0)
	assert.Panics(t, func() { hostEmitTarget2(ctx, 0, 0.5) },
		"同 bar 同 symbol 先 emit_signal 再 emit_target2 必须 trap")

	bc.resetEmissionBar()
	hostEmitTarget2(ctx, 0, 0.5)
	assert.Panics(t, func() { hostEmitSignal(ctx, 0, int32(contracts.ActionLong), 1.0) },
		"反方向混用同样必须 trap")
}

func TestHostEmitSignal_ValidatesActionStrengthAndSymbol(t *testing.T) {
	bc, ctx := qt26EmitContext()

	assert.Panics(t, func() { hostEmitSignal(ctx, 0, 9, 1.0) }, "action 枚举外必须 trap")
	bc.resetEmissionBar()
	assert.Panics(t, func() { hostEmitSignal(ctx, 0, int32(contracts.ActionLong), 1.5) }, "strength>1 必须 trap")
	bc.resetEmissionBar()
	assert.Panics(t, func() { hostEmitSignal(ctx, 5, int32(contracts.ActionLong), 1.0) }, "symbol_idx 越界必须 trap")

	bc.resetEmissionBar()
	hostEmitSignal(ctx, 0, int32(contracts.ActionClose), 1.0)
	require.Len(t, bc.Signals, 1)
}
