package wasm

// QT-15 的验收测试：终止能力（C1）+ state_only 重放恢复（N4）。
//
// 覆盖四类证据：
//  1. while(true) 在单次预算内被终止且进程存活（C1 第 1 层，真实 wasm 字节码）；
//  2. 终止后 Initialize/OnBar/Finalize 一律拒绝（C1 第 3 层的 fail-loud）；
//  3. 确定性故障 → 重建上限耗尽 → run failed（N4 的 cap 语义，真实 wasm）；
//  4. 瞬态故障 → 重建 + state_only 重放 → 与无故障基线逐项一致
//     （N4 的恢复验收：信号级逐笔一致 + 压制计数 + 状态恢复连续性）。

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/tellurion-contracts/engine"
)

// wasmHangingFrom 构造一个在 bar t >= fault 时死循环的模块：
//
//	on_bar(t):
//	    if t < fault { return 0 }
//	    loop {}          // 死循环——预算必须能杀掉它
//
// import get_bar 仅为通过加载器的 abi 族判定门（QT-01：不 import 任何 host
// 协议族的模块在 Compile 就被拒）；函数体不调它，死循环是纯 wasm 侧的。
//
// block/loop + br_if 的展开：
//
//	block $exit            ;; 02 40
//	  loop $again          ;; 03 40
//	    local.get 0        ;; t
//	    i32.const fault
//	    i32.ge_s           ;; t >= fault ?
//	    i32.eqz            ;; !(t >= fault) = t < fault ?
//	    br_if $exit        ;; t < fault → 走正常返回
//	    br $again          ;; t >= fault → 永远循环
//	  end                  ;; 0b
//	end                    ;; 0b
//	i64.const 0
func wasmHangingFrom(fault int32) []byte {
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
			{
				opBlock, blockTypeVoid,
				opLoop, blockTypeVoid,
				opLocalGet, 0x00,
				opI32Const, encodeI32ConstByte(fault),
				opI32GeS,
				opI32Eqz,
				opBrIf, 0x01, // br_if $exit（外层 block = label 1）
				opBr, 0x00, // br $again（内层 loop = label 0）
				opEnd, // loop end
				opEnd, // block end
				opI64Const, 0x00,
				opEnd,
			},
			{opI64Const, 0x00, opEnd},
		},
	}
	base := m.funcIndexBase()
	m.export = []wasmExport{
		{name: "initialize", kind: 0, idx: base + 0},
		{name: "on_bar", kind: 0, idx: base + 1},
		{name: "finalize", kind: 0, idx: base + 2},
	}
	return m.encode()
}

// encodeI32ConstByte 把一个小的非负 int32 编成单字节 LEB128（测试只造 0~63 的
// 故障点；大值在此断言失败而非静默编错）。
func encodeI32ConstByte(v int32) byte {
	if v < 0 || v > 63 {
		panic(fmt.Sprintf("encodeI32ConstByte: %d out of single-byte range", v))
	}
	return byte(v)
}

// wasmBusyOnBar 构造一个每 bar 做 iter 次 i32 计数循环的模块（基准用重负载）：
//
//	on_bar(t):
//	    for i := 0; i < iter; i++ {}   // 计数器复用参数 local 0（参数是可变 local）
//	    return i64(t 的终值)           // 循环结果进返回值，防死代码消除
//
// import get_bar 仅为通过加载器的 abi 族判定门；计算是纯 wasm 侧的。
func wasmBusyOnBar(iter int32) []byte {
	// i32.const 的立即数是 SLEB128：uleb 编 10000 会得到 [0x90,0x4E]，0x4E 的
	// bit6=1 被符号扩展成 -6384——循环当场退出且不报错（本仓 builder 注释里
	// 点过名的坑，这里真踩过一次：基准读数「重负载 ≈ 微负载」就是它的症状）。
	iterLeb := appendSleb128(nil, int64(iter))
	body := append([]byte{
		opBlock, blockTypeVoid,
		opLoop, blockTypeVoid,
		opLocalGet, 0x00,
		opI32Const,
	}, iterLeb...)
	body = append(body,
		opI32GeS,
		opBrIf, 0x01,
		opLocalGet, 0x00,
		opI32Const, 0x01,
		opI32Add,
		opLocalSet, 0x00,
		opBr, 0x00,
		opEnd, // loop
		opEnd, // block
		opLocalGet, 0x00,
		opI64ExtendI32U,
		opEnd,
	)
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
			body,
			{opI64Const, 0x00, opEnd},
		},
	}
	base := m.funcIndexBase()
	m.export = []wasmExport{
		{name: "initialize", kind: 0, idx: base + 0},
		{name: "on_bar", kind: 0, idx: base + 1},
		{name: "finalize", kind: 0, idx: base + 2},
	}
	return m.encode()
}

// TestWhileTrueTerminatesWithinBudget 是 C1 第 1 层的正证据：死循环在单次预算
// 内被杀掉（WithCloseOnContextDone），进程存活，错误归到 ErrSessionTerminated。
// 守卫破坏腿：删掉 WithCloseOnContextDone(true) 后本测试在 watchdog 处红
// （OnBar 永不返回）——「假护栏比没护栏更糟」的破坏验证由 CI 之外的单测腿承担。
func TestWhileTrueTerminatesWithinBudget(t *testing.T) {
	r := newHostRuntime(t)
	sb := NewSandbox(r, Config{})

	sess, err := sb.NewOnBarSessionWithBudget(context.Background(), wasmHangingFrom(0),
		SessionBudget{PerCall: 100 * time.Millisecond})
	require.NoError(t, err)
	defer sess.Close()

	require.NoError(t, sess.Initialize(context.Background(), nil))

	bc := bcWithCloses(make([]float64, 3), 0)
	bc.T = 0

	done := make(chan error, 1)
	go func() { done <- sess.OnBar(context.Background(), bc) }()
	select {
	case err := <-done:
		require.Error(t, err)
		require.ErrorIs(t, err, ErrSessionTerminated, "死循环必须以 ErrSessionTerminated 收场，实际: %v", err)
		assert.ErrorIs(t, err, context.DeadlineExceeded, "错误链上应保留原始原因（预算到期）")
	case <-time.After(5 * time.Second):
		t.Fatal("OnBar 5s 未返回——终止能力失效（WithCloseOnContextDone 被关掉了？）")
	}
	// 进程存活：能继续做一次普通分配并关闭会话（实例已被 wazero 关闭，Close 幂等）。
	assert.NoError(t, sess.Close())
}

// TestTerminatedSessionRefusesCalls 是 C1 第 3 层：终止后任何协议调用都
// fail-loud 返回 ErrSessionTerminated（并点名原始原因），没有静默半截结果。
func TestTerminatedSessionRefusesCalls(t *testing.T) {
	r := newHostRuntime(t)
	sb := NewSandbox(r, Config{})

	sess, err := sb.NewOnBarSessionWithBudget(context.Background(), wasmHangingFrom(0),
		SessionBudget{PerCall: 100 * time.Millisecond})
	require.NoError(t, err)
	defer sess.Close()
	require.NoError(t, sess.Initialize(context.Background(), nil))

	bc := &BarContext{State: map[string][]byte{}, Symbols: []string{"A"}}
	bc.T = 0
	err = sess.OnBar(context.Background(), bc)
	require.ErrorIs(t, err, ErrSessionTerminated)

	// 终止后三类调用全部拒绝。
	bc.T = 1
	assert.ErrorIs(t, sess.OnBar(context.Background(), bc), ErrSessionTerminated, "终止后 OnBar 必须拒绝")
	assert.ErrorIs(t, sess.Initialize(context.Background(), []byte(`{}`)), ErrSessionTerminated, "终止后 Initialize 必须拒绝")
	_, err = sess.Finalize(context.Background(), bc)
	assert.ErrorIs(t, err, ErrSessionTerminated, "终止后 Finalize 必须拒绝")
}

// fakeAliveInstance 是 checkAlive 单测的最小 Instance（调用恒成功——这样
// 「终止后拒绝」只能来自 checkAlive，不能靠 runCtx 取消或模块已关来凑数）。
type fakeAliveInstance struct{ Instance }

func (f fakeAliveInstance) WriteMemory(uint32, []byte) error          { return nil }
func (f fakeAliveInstance) ReadMemory(uint32, uint32) ([]byte, error) { return []byte{}, nil }
func (f fakeAliveInstance) MemorySize() uint32                        { return 65536 }
func (f fakeAliveInstance) Call(context.Context, string, ...uint64) ([]uint64, error) {
	return []uint64{0}, nil
}
func (f fakeAliveInstance) Close() error { return nil }

// TestCheckAliveGuardsTerminatedSession 直接钉住 QT-15 ③ 的守门：terminated
// 会话在 runCtx 仍然健康、实例仍可调用的前提下，三类协议调用都必须被
// checkAlive 拒绝（破坏腿：删掉 checkAlive 调用 → 本测试红，调用会真跑）。
func TestCheckAliveGuardsTerminatedSession(t *testing.T) {
	r := newHostRuntime(t)
	sb := NewSandbox(r, Config{})
	sp := &OnBarSession{
		inst:      fakeAliveInstance{},
		sandbox:   sb,
		budget:    SessionBudget{PerCall: time.Second},
		runCtx:    context.Background(),
		runCancel: func() {},
	}
	cause := errors.New("budget exhausted at bar 7")
	sp.terminate(cause)

	bc := &BarContext{State: map[string][]byte{}}
	err := sp.OnBar(context.Background(), bc)
	require.ErrorIs(t, err, ErrSessionTerminated)
	assert.ErrorIs(t, err, cause, "拒绝信息应点名原始终止原因")
	assert.ErrorIs(t, sp.Initialize(context.Background(), []byte(`{}`)), ErrSessionTerminated)
	_, err = sp.Finalize(context.Background(), bc)
	assert.ErrorIs(t, err, ErrSessionTerminated)
}

// TestBudgetForScope 钉住 C1 冻结的默认预算与 fail-closed 的未知 scope。
func TestBudgetForScope(t *testing.T) {
	b, err := BudgetForScope(contracts.ScopeMarket)
	require.NoError(t, err)
	assert.Equal(t, DefaultMarketCallBudget, b.PerCall)
	assert.Equal(t, DefaultRunDeadline, b.RunDeadline)

	b, err = BudgetForScope(contracts.ScopeSymbol)
	require.NoError(t, err)
	assert.Equal(t, DefaultSymbolCallBudget, b.PerCall)
	assert.Equal(t, DefaultRunDeadline, b.RunDeadline)

	_, err = BudgetForScope(contracts.StrategyScope("cross"))
	require.Error(t, err, "未知 scope 必须 fail-closed")
}

// TestHostEmitSuppression 钉住 N4 的压制语义：SuppressSignals=true 时
// emit_signal「成功但不记录」，只递增计数。
func TestHostEmitSuppression(t *testing.T) {
	bc := &BarContext{State: map[string][]byte{}, Symbols: []string{"A"}}
	ctx := withBarContext(context.Background(), bc)

	bc.SuppressSignals = true
	hostEmitSignal(ctx, 0, 1, 1.0)
	hostEmitSignal(ctx, 0, 1, 1.0)
	assert.Empty(t, bc.Signals, "压制期信号不得写入 Signals")
	assert.Equal(t, 2, bc.SuppressedSignals, "压制计数应递增")

	bc.SuppressSignals = false
	bc.resetEmissionBar()
	hostEmitSignal(ctx, 0, 1, 1.0)
	assert.Len(t, bc.Signals, 1, "恢复 apply 后信号照常记录")
	assert.Equal(t, 2, bc.SuppressedSignals, "apply 期的 emit 不计入压制")
}

// stubFaultySession 是 supervisor 的故障注入 stub（瞬态故障无法用真实 wasm
// 表达——真实死循环每次重建都会在同一 bar 复发，那正是「上限耗尽 → failed」
// 的规定行为，由 TestSupervisorRebuildCapExhausted 用真实模块覆盖）。
//
// 语义：每个 bar emit 一个信号（遵守 SuppressSignals，模拟 host 行为），
// 状态键 "seen" 追加当前 t（可验证重建从快照恢复的连续性）；t == faultAt 且
// 未处于重放时返回终止错误（模拟「先 emit 再卡死」——失败 bar 的 emit 必须被
// supervisor 丢弃）。
type stubFaultySession struct {
	faultAt int32 // <0 = 无故障（基线会话）
	closed  bool
}

func (s *stubFaultySession) Initialize(_ context.Context, _ []byte) error { return nil }

func (s *stubFaultySession) OnBar(_ context.Context, bc *BarContext) error {
	if bc.SuppressSignals {
		bc.SuppressedSignals++
	} else {
		bc.Signals = append(bc.Signals, EmittedSignal{SymbolIdx: 0, Action: 1, Strength: 1.0})
	}
	bc.State["seen"] = append(bc.State["seen"], byte(bc.T))
	if !bc.SuppressSignals && bc.T == s.faultAt {
		return fmt.Errorf("%w: injected at bar %d", ErrSessionTerminated, bc.T)
	}
	return nil
}

func (s *stubFaultySession) Finalize(_ context.Context, _ *BarContext) ([]byte, error) {
	return []byte{}, nil
}

func (s *stubFaultySession) Close() error {
	s.closed = true
	return nil
}

// TestSupervisorRebuildCapExhausted 是 N4 cap 语义的正证据（真实 wasm）：
// 确定性死循环每次重建都在同一 bar 复发 → 重建 DefaultMaxRebuilds 次后 run
// failed，错误点名 ErrSessionTerminated 与稳定 code。
func TestSupervisorRebuildCapExhausted(t *testing.T) {
	r := newHostRuntime(t)
	sb := NewSandbox(r, Config{})

	sv, err := NewSessionSupervisor(sb, wasmHangingFrom(3), SupervisorConfig{
		Scope:  string(contracts.ScopeMarket),
		Budget: SessionBudget{PerCall: 100 * time.Millisecond},
	})
	require.NoError(t, err)

	bc := bcWithCloses(make([]float64, 6), 0)
	stats, err := sv.Run(context.Background(), bc, 6)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrSessionTerminated)
	assert.Contains(t, err.Error(), CodeSessionTerminated, "错误应点名稳定 invalid_reasons code")
	assert.Equal(t, DefaultMaxRebuilds, stats.Rebuilds, "重建次数按 run 计，到上限即失败")
	assert.Equal(t, 3, stats.TerminatedAtBar, "故障 bar 应被记录")
	assert.Equal(t, 3, stats.BarsCompleted, "只有故障前的 bar 完成过")
}

// TestSupervisorStateOnlyReplayRecovers 是 N4 恢复验收的核心正证据（stub 注入
// 瞬态故障）：重建 1 次、重放窗口 [0,2] 被压制（快照只取到初始态——
// SnapshotEvery=4 让窗口跨 3 个 bar，真正走一遍压制路径）、故障 bar 3 以
// apply 重执行，最终信号序列与无故障基线**逐项一致**，状态恢复无重复无缺失。
func TestSupervisorStateOnlyReplayRecovers(t *testing.T) {
	runBaseline := func(t *testing.T) (*BarContext, *RunStats) {
		sv := &SessionSupervisor{maxRebuilds: DefaultMaxRebuilds, snapshotEvery: 4}
		sv.SetSessionFactory(func(_ context.Context) (barSession, error) {
			return &stubFaultySession{faultAt: -1}, nil
		})
		bc := &BarContext{State: map[string][]byte{}}
		stats, err := sv.Run(context.Background(), bc, 6)
		require.NoError(t, err)
		return bc, stats
	}

	baseline, baseStats := runBaseline(t)

	sv := &SessionSupervisor{maxRebuilds: DefaultMaxRebuilds, snapshotEvery: 4}
	var sessions []*stubFaultySession
	sv.SetSessionFactory(func(_ context.Context) (barSession, error) {
		// 零值 faultAt=0 会误伤后续会话，故健康会话显式 faultAt=-1。
		s := &stubFaultySession{faultAt: -1}
		if len(sessions) == 0 {
			s.faultAt = 3 // 只有第一个会话带瞬态故障；重建后恢复健康
		}
		sessions = append(sessions, s)
		return s, nil
	})

	faulty := &BarContext{State: map[string][]byte{}}
	stats, err := sv.Run(context.Background(), faulty, 6)
	require.NoError(t, err)

	assert.Equal(t, 1, stats.Rebuilds)
	assert.Equal(t, 3, stats.TerminatedAtBar)
	assert.Equal(t, 3, stats.SuppressedSignals, "重放窗口 [0,2] 每窗口压制 3 个 emit")
	assert.Equal(t, baseStats.BarsCompleted, stats.BarsCompleted)

	// 信号级逐笔一致（N4 的验收判据：不是"接近"，是逐项一致）。
	assert.Equal(t, baseline.Signals, faulty.Signals, "恢复 run 与无故障基线的信号必须逐项一致")

	// 状态连续性：重建从快照恢复，"seen" 无重复无缺失。
	assert.Equal(t, baseline.State["seen"], faulty.State["seen"], "状态恢复后推进序列必须与基线一致")

	// 会话生命周期：被终止的首个会话由 supervisor 即时关闭；健康会话由
	// Run 的正常收尾关闭（finalize 之后）。
	require.Len(t, sessions, 2)
	assert.True(t, sessions[0].closed, "被终止的首个会话应被关闭")
	assert.True(t, sessions[1].closed, "健康会话在 Run 正常收尾时关闭")
}

// TestSupervisorRejectsApplyMode 钉住 N4：apply 模式在本层构造即拒
// （需要引擎侧事务性回滚，不是静默降级）。
func TestSupervisorRejectsApplyMode(t *testing.T) {
	r := newHostRuntime(t)
	sb := NewSandbox(r, Config{})
	_, err := NewSessionSupervisor(sb, wasmHangingFrom(0), SupervisorConfig{
		Scope:      string(contracts.ScopeMarket),
		ReplayMode: ReplayApply,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), string(ReplayApply), "拒绝信息应点名 apply 模式")
	assert.Contains(t, err.Error(), "transactional rollback", "拒绝信息应点名缺引擎侧事务性回滚")
}

// TestSupervisorRejectsUnknownScope 钉住预算的 fail-closed：未知 scope 构造即拒。
func TestSupervisorRejectsUnknownScope(t *testing.T) {
	r := newHostRuntime(t)
	sb := NewSandbox(r, Config{})
	_, err := NewSessionSupervisor(sb, wasmHangingFrom(0), SupervisorConfig{Scope: "cross"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cross")
}

// TestSupervisorFailLoudOnPlainTrap 钉住非终止错误不重建：普通 trap 直接
// fail-loud，Rebuilds 保持 0。
func TestSupervisorFailLoudOnPlainTrap(t *testing.T) {
	wantErr := errors.New("plain trap")
	sv := &SessionSupervisor{maxRebuilds: DefaultMaxRebuilds, snapshotEvery: 1}
	sv.SetSessionFactory(func(_ context.Context) (barSession, error) {
		return &stubPlainTrap{err: wantErr}, nil
	})
	bc := &BarContext{State: map[string][]byte{}}
	stats, err := sv.Run(context.Background(), bc, 6)
	require.ErrorIs(t, err, wantErr)
	assert.Zero(t, stats.Rebuilds, "普通 trap 不是终止，不得触发重建")
	assert.Equal(t, -1, stats.TerminatedAtBar, "普通 trap 不记故障 bar")
}

// stubPlainTrap 每次OnBar 返回一个与终止无关的普通错误。
type stubPlainTrap struct{ err error }

func (s *stubPlainTrap) Initialize(_ context.Context, _ []byte) error { return nil }
func (s *stubPlainTrap) OnBar(_ context.Context, _ *BarContext) error { return s.err }
func (s *stubPlainTrap) Finalize(_ context.Context, _ *BarContext) ([]byte, error) {
	return nil, nil
}
func (s *stubPlainTrap) Close() error { return nil }
