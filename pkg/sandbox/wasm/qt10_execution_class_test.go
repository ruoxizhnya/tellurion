package wasm

// QT-10 ③-②：wasm 宿主侧的错误归类。
//
// 判据：trap / 预算终止 / artifact 校验失败 ⇒ ErrStrategyFault；
// abi 不符 / params 超限 / host 调用缺上下文 ⇒ ErrContractBreach。
// 判定只用 errors.Is（contracts API.md §3）。

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/tellurion-contracts/engine"
)

// wasmTrapOnBar 构造一个 on_bar 立即执行 unreachable 的模块（普通 trap，
// 不关模块）。import get_bar 仅为通过 abi 族判定门。
func wasmTrapOnBar() []byte {
	m := &wasmModule{
		types: []wasmFuncType{
			{params: []byte{valI32, valI32}, results: []byte{valF64}}, // get_bar
			{params: []byte{valI32, valI32}, results: []byte{valI32}}, // initialize
			{params: []byte{valI32}, results: []byte{valI64}},         // on_bar(t)
			{results: []byte{valI64}},                                 // finalize
		},
		imports: []wasmImport{{module: "env", name: "get_bar", typeIdx: 0}},
		funcs:   []uint32{1, 2, 3},
		memory:  &wasmMemory{min: 1},
		codes: [][]byte{
			{opI32Const, 0x00, opEnd},
			{0x00, opEnd}, // unreachable → trap
			{opI64Const, 0x00, opEnd},
		},
	}
	m.export = []wasmExport{
		{name: "initialize", kind: 0, idx: m.funcIndexBase() + 0},
		{name: "on_bar", kind: 0, idx: m.funcIndexBase() + 1},
		{name: "finalize", kind: 0, idx: m.funcIndexBase() + 2},
	}
	return m.encode()
}

func TestQT10_WasmTrapIsStrategyFault(t *testing.T) {
	r := newHostRuntime(t)
	sb := NewSandbox(r, Config{})

	sess, err := sb.NewOnBarSession(context.Background(), wasmTrapOnBar())
	require.NoError(t, err)
	defer sess.Close()

	err = sess.OnBar(context.Background(), bcWithCloses([]float64{10}, 0))
	require.Error(t, err, "unreachable 必须 trap")
	assert.True(t, errors.Is(err, contracts.ErrStrategyFault), "trap 是策略故障，got: %v", err)
	assert.False(t, errors.Is(err, contracts.ErrDataGap), "trap 不得被当成数据缺口跳天")
}

func TestQT10_WasmBudgetTerminationIsStrategyFault(t *testing.T) {
	r := newHostRuntime(t)
	sb := NewSandbox(r, Config{})

	sess, err := sb.NewOnBarSessionWithBudget(context.Background(), wasmHangingFrom(0),
		SessionBudget{PerCall: 5 * time.Millisecond, RunDeadline: time.Minute})
	require.NoError(t, err)
	defer sess.Close()

	err = sess.OnBar(context.Background(), bcWithCloses([]float64{10}, 0))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSessionTerminated), "预算终止必须保留原哨兵（QT-15），got: %v", err)
	assert.True(t, errors.Is(err, contracts.ErrStrategyFault), "预算超限是策略故障，got: %v", err)
}

func TestQT10_WasmParamsTooLargeIsContractBreach(t *testing.T) {
	r := newHostRuntime(t)
	sb := NewSandbox(r, Config{})

	sess, err := sb.NewOnBarSession(context.Background(), wasmAccumulator())
	require.NoError(t, err)
	defer sess.Close()

	err = sess.Initialize(context.Background(), make([]byte, contracts.MaxParamsBytes+1))
	require.Error(t, err)
	assert.True(t, errors.Is(err, contracts.ErrParamsTooLarge), "原哨兵必须保链")
	assert.True(t, errors.Is(err, contracts.ErrContractBreach), "参数超限是契约违规，got: %v", err)
}

func TestQT10_WasmNilBarContextIsContractBreach(t *testing.T) {
	r := newHostRuntime(t)
	sb := NewSandbox(r, Config{})

	sess, err := sb.NewOnBarSession(context.Background(), wasmAccumulator())
	require.NoError(t, err)
	defer sess.Close()

	err = sess.OnBar(context.Background(), nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, contracts.ErrContractBreach), "host 调用缺上下文是契约违规，got: %v", err)
}
