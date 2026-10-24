package wasm

// K6 切片 2 + QT-24：host API 白名单 —— 前视隔离是唯一可行机制（ADR-029 §3）。
//
// 核心原则：不靠审阅代码保证安全，靠**能力隔离**。WASM 实例只能调用宿主
// 显式导入的函数；没有 import 的能力它物理上做不到。本节实现：
//
//  1. host 导入函数白名单：模块名固定 "env"，函数名与签名由
//     `pkg/backtest/contracts` 的 abi 契约表冻结（QT-01）。v1 家族 8 个
//     （get_bar / get_series_len / get_symbol_count / get_cross_section /
//     get_state / set_state / emit_signal / log）全部已实现；v2 家族 4 个
//     （get_bar2 / get_series_len2 / get_date2 / emit_target2）中数据面三个
//     由 QT-24 接线，emit_target2 由 QT-26 接线。
//  2. `get_bar(t_offset, field_id)` / `get_bar2(...)` 在 `t_offset > 0` 时
//     **直接 panic**（wazero 把 panic 转成 wasm trap），使「读未来数据」
//     物理上不可能。get_cross_section 同样 trap。
//  3. 刻意**不提供**文件/网络/时钟/随机源/goroutine/进程/环境变量 ——
//     缺这些正是安全性的来源（ADR-029 §3.2 末）。
//
// 数据模型（QT-24 · adr-029-amendment §7.1 冻结）：
//
//	{T, Axis, Symbols, Bars, State, Signals(+Targets)}
//
//   - `Axis` 是**唯一日历源**（Engine.getTradingDays 的返回值），market scope
//     必填、symbol scope 为 nil。T 是 Axis 的下标。
//   - `Bars[symbol_idx]` 直接引用 provider 手上的 `Scope.MarketData[symbol]`
//     累积切片（零拷贝）；整段序列永不进 wasm 内存，取数仍按坐标。
//   - `symbol_idx` 只能由 `Scope.RunUniverse`（run 级稳定）派生；缺 bar
//     （停牌/未上市）返回 NaN，禁止用 0 掩盖。
//   - `CrossSection` 已删除：它只是 close 的第二份真相，横截面可由
//     `Axis[T] + Bars` 求出。
//
// v1 兼容：v1 artifact 一律按 symbol scope 执行，`get_bar` 仍只读 close，
// 行为与旧实现一致（内部改从 `Bars[0]` 取，不再有 `Series`）。

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/ruoxizhnya/tellurion-contracts/engine"
)

// HostModuleName 是 host 导入函数的模块名，wasm 二进制 import 它。
const HostModuleName = "env"

// host 函数的导出名一律取自 abi 契约（contracts.HostFn*）——本包不再自带一份
// 名字表。理由与 QT-01 ① 同源：名字/签名/语义只有一处真相，两边各写一份必然漂移。

// hostCtxKey 是 context 中注入 *BarContext 的私有键。
type hostCtxKey struct{}

// BarContext 是 host 函数可见的「当前回放上下文」（QT-24 冻结形态）。
type BarContext struct {
	// T 是当前下标：market scope = Axis 的下标；symbol scope = 该标的自有
	// 序列的下标。
	T int32
	// Axis 是共同交易日轴（升序、去重）。market scope 必填；symbol scope 为 nil。
	Axis []time.Time
	// Symbols 是稳定顺序的票池（RunUniverse），symbol_idx = 下标。
	Symbols []string
	// Bars 与 Symbols 等长；元素直接引用 provider 的累积行情（只读约定）。
	Bars [][]domain.OHLCV
	// State 是宿主管理的持久化状态（get_state/set_state 载体）。
	State map[string][]byte
	// Signals 是 emit_signal 收集的增量信号。
	Signals []EmittedSignal
	// Targets 是 emit_target2 收集的目标权重（QT-26）。两套 sizing 同 bar
	// 同 symbol 混用会 trap。
	Targets []EmittedTarget

	// SuppressSignals / SuppressedSignals：state_only 重放开关（N4 / QT-15 ④）。
	SuppressSignals   bool
	SuppressedSignals int

	// inFinalize：finalize() 期间为 true。此时只允许 close / 清仓，
	// 发 long/short 会 trap（A5/§2.2-14：禁止用最后已知价开新仓）。
	inFinalize bool

	// ScratchPtr / ScratchSize 是 v2 artifact 声明的 state 返回 scratch
	// （QT-26）。0 表示 legacy 布局（固定 4096）。
	ScratchPtr  uint32
	ScratchSize uint32

	// ─── 延迟索引（QT-24 ④：不做每次二分）───────────────────────────
	axisDays []int32
	barDays  [][]int32
	barIndex []map[int32]int

	// ─── 单 bar 发射记账（QT-26）───────────────────────────────────
	lastEmissionBar int32
	emissionsInBar  int
	emitSources     map[int32]contracts.EmitSource
}

// EmittedSignal 是 wasm 经 emit_signal 发出的信号。
type EmittedSignal struct {
	SymbolIdx int32
	Action    int32
	Strength  float64
}

// EmittedTarget 是 wasm 经 emit_target2 发出的目标权重（QT-26）。
type EmittedTarget struct {
	SymbolIdx int32
	Weight    float64
}

// withBarContext 把 *BarContext 注入 ctx，供 host 函数取出。
func withBarContext(ctx context.Context, bc *BarContext) context.Context {
	return context.WithValue(ctx, hostCtxKey{}, bc)
}

// barContextFrom 从 ctx 取 *BarContext；nil 表示调用方漏注入或越权直调，
// host 函数必须 fail-loud（panic → trap），不静默降级。
func barContextFrom(ctx context.Context) *BarContext {
	bc, _ := ctx.Value(hostCtxKey{}).(*BarContext)
	return bc
}

// trapFuture 是「读未来数据」的统一 trap：直接 panic，wazero 转成 wasm trap。
func trapFuture(tOffset int32) {
	panic(fmt.Sprintf("wasm: future access (t_offset=%d > 0)", tOffset))
}

// trapNoContext 是「host 函数在无 BarContext 时被调」的 fail-loud。
func trapNoContext(fn string) {
	panic(fmt.Sprintf("wasm: %s called without BarContext (host injection missing)", fn))
}

// InstantiateHostModule 在 runtime 上实例化 host 白名单模块 "env"。
// 必须在实例化任何依赖它的 wasm 策略模块**之前**调用。
//
// v1 + v2 全部 12 个名字**一次注册**（不同名，无冲突）——这是 §2.3 增量命名方案
// 的前提：Runtime 是进程级单例（D14），按版本分两套注册会逼出双 Runtime、编译缓存
// 分裂。
func (r *WazeroRuntime) InstantiateHostModule(ctx context.Context) error {
	_, err := configureHostModule(r.rt.NewHostModuleBuilder(HostModuleName)).Instantiate(ctx)
	return err
}

// configureHostModule 把契约表的 12 个函数挂到 host 模块构建器上。
//
// 抽出来是为了让「注册表 × 契约表」能被测试直接对账（见 abi_check_test.go 的
// TestHostModuleRegistration_MatchesContractTable）——没有这一步，「契约里 12 个、
// 运行时只注册 8 个」这种缺口就只能靠人肉比对。
func configureHostModule(b wazero.HostModuleBuilder) wazero.HostModuleBuilder {
	return b.
		NewFunctionBuilder().WithFunc(hostGetBar).Export(contracts.HostFnGetBar).
		NewFunctionBuilder().WithFunc(hostGetSeriesLen).Export(contracts.HostFnGetSeriesLen).
		NewFunctionBuilder().WithFunc(hostGetSymbolCount).Export(contracts.HostFnGetSymbolCount).
		NewFunctionBuilder().WithFunc(hostGetCrossSection).Export(contracts.HostFnGetCrossSection).
		NewFunctionBuilder().WithFunc(hostGetState).Export(contracts.HostFnGetState).
		NewFunctionBuilder().WithFunc(hostSetState).Export(contracts.HostFnSetState).
		NewFunctionBuilder().WithFunc(hostEmitSignal).Export(contracts.HostFnEmitSignal).
		NewFunctionBuilder().WithFunc(hostLog).Export(contracts.HostFnLog).
		NewFunctionBuilder().WithFunc(hostGetBar2).Export(contracts.HostFnGetBar2).
		NewFunctionBuilder().WithFunc(hostGetSeriesLen2).Export(contracts.HostFnGetSeriesLen2).
		NewFunctionBuilder().WithFunc(hostGetDate2).Export(contracts.HostFnGetDate2).
		NewFunctionBuilder().WithFunc(hostEmitTarget2).Export(contracts.HostFnEmitTarget2)
}

// ─── 数据模型辅助 ───────────────────────────────────────────────────

// marketScope 判定：Axis 非空即 market scope（§7.1）。
func (bc *BarContext) marketScope() bool { return len(bc.Axis) > 0 }

// ensureIndexes 建「日期 → bar 下标」的预建索引（QT-24 ④）。
// 幂等；Bars 追加后调用方应把 barDays/barIndex 置 nil 再触发重建。
func (bc *BarContext) ensureIndexes() {
	if len(bc.axisDays) != len(bc.Axis) {
		bc.axisDays = make([]int32, len(bc.Axis))
		for i, d := range bc.Axis {
			bc.axisDays[i] = dayKey(d)
		}
	}
	if len(bc.barIndex) == len(bc.Bars) && len(bc.barDays) == len(bc.Bars) {
		// 缓存只在「每个 symbol 的可见 bar 数没变」时有效。provider 逐日
		// 追加 Bars 是常态，用长度判定即可（不比较内容，避免每 bar O(n)）。
		stale := false
		for i := range bc.Bars {
			if len(bc.barDays[i]) != len(bc.Bars[i]) {
				stale = true
				break
			}
		}
		if !stale {
			return
		}
	}
	bc.barDays = make([][]int32, len(bc.Bars))
	bc.barIndex = make([]map[int32]int, len(bc.Bars))
	for i, bars := range bc.Bars {
		days := make([]int32, len(bars))
		idx := make(map[int32]int, len(bars))
		for j, bar := range bars {
			k := dayKey(bar.Date)
			days[j] = k
			if _, ok := idx[k]; !ok {
				idx[k] = j
			}
		}
		bc.barDays[i] = days
		bc.barIndex[i] = idx
	}
}

// dayKey 把日期折成 YYYYMMDD（UTC），与 get_date2 的口径一致。
func dayKey(t time.Time) int32 {
	u := t.UTC()
	return int32(u.Year()*10000 + int(u.Month())*100 + u.Day())
}

// barAt 返回 (symbol_idx, 绝对下标) 的 bar；不存在（越界/停牌/未上市）返回 false。
func (bc *BarContext) barAt(symbolIdx, idx int32) (*domain.OHLCV, bool) {
	if symbolIdx < 0 || int(symbolIdx) >= len(bc.Bars) {
		return nil, false
	}
	if idx < 0 {
		return nil, false
	}
	if bc.marketScope() {
		bc.ensureIndexes()
		if int(idx) >= len(bc.axisDays) {
			return nil, false
		}
		day := bc.axisDays[idx]
		at, ok := bc.barIndex[symbolIdx][day]
		if !ok {
			return nil, false
		}
		return &bc.Bars[symbolIdx][at], true
	}
	bars := bc.Bars[symbolIdx]
	if int(idx) >= len(bars) {
		return nil, false
	}
	return &bars[idx], true
}

// currentDate 返回当前 bar 的日期（UTC 零点）。
func (bc *BarContext) currentDate() (time.Time, bool) {
	if bc.marketScope() {
		if bc.T < 0 || int(bc.T) >= len(bc.Axis) {
			return time.Time{}, false
		}
		return bc.Axis[bc.T], true
	}
	if len(bc.Bars) == 0 || bc.T < 0 || int(bc.T) >= len(bc.Bars[0]) {
		return time.Time{}, false
	}
	return bc.Bars[0][bc.T].Date, true
}

// visibleSeriesLen 返回**可见长度**：日期 ≤ Axis[T] 的 bar 数（§7.1 硬约束）。
// symbol scope 没有 Axis，可见长度 = min(len, T+1) —— 同样不暴露未来 bar。
func (bc *BarContext) visibleSeriesLen(symbolIdx int32) int32 {
	if symbolIdx < 0 || int(symbolIdx) >= len(bc.Bars) {
		return 0
	}
	if !bc.marketScope() {
		n := len(bc.Bars[symbolIdx])
		if int(bc.T)+1 < n {
			return int32(maxInt(0, int(bc.T)+1))
		}
		return int32(n)
	}
	bc.ensureIndexes()
	if bc.T < 0 || int(bc.T) >= len(bc.axisDays) {
		return 0
	}
	limit := bc.axisDays[bc.T]
	days := bc.barDays[symbolIdx]
	return int32(sort.Search(len(days), func(i int) bool { return days[i] > limit }))
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ohlcvField 按冻结的 FieldID 枚举取字段；非法枚举 → trap（不返回 0）。
func ohlcvField(bar *domain.OHLCV, fieldID int32) float64 {
	f := contracts.FieldID(fieldID)
	if !f.Valid() {
		panic(fmt.Sprintf("wasm: field_id=%d is outside the frozen enum {0..4}", fieldID))
	}
	switch f {
	case contracts.FieldClose:
		return bar.Close
	case contracts.FieldOpen:
		return bar.Open
	case contracts.FieldHigh:
		return bar.High
	case contracts.FieldLow:
		return bar.Low
	case contracts.FieldVolume:
		return bar.Volume
	default:
		panic(fmt.Sprintf("wasm: field_id=%d is outside the frozen enum {0..4}", fieldID))
	}
}

// resetEmissionBar 清空单 bar 发射记账。OnBarSession.OnBar 每次调用前重置：
// 预算是「一次 on_bar 调用」的上限，不是「同一 T 的多次重放合计」——
// state_only 重放 + 重新执行是两次调用，各自受一次上限约束。
func (bc *BarContext) resetEmissionBar() {
	bc.lastEmissionBar = bc.T
	bc.emissionsInBar = 0
	bc.emitSources = nil
}

// beginEmission 在每次 emit 前做单 bar 预算 / 混用检查（QT-26）。
func (bc *BarContext) beginEmission(symbolIdx int32, source contracts.EmitSource) {
	if symbolIdx < 0 || int(symbolIdx) >= len(bc.Symbols) {
		panic(fmt.Sprintf("wasm: symbol_idx=%d out of range [0,%d)", symbolIdx, len(bc.Symbols)))
	}
	if bc.lastEmissionBar != bc.T || bc.emitSources == nil {
		bc.lastEmissionBar = bc.T
		bc.emissionsInBar = 0
		bc.emitSources = make(map[int32]contracts.EmitSource, 1)
	}
	if err := contracts.CheckSignalBudget(bc.emissionsInBar+1, int32(len(bc.Symbols))); err != nil {
		panic("wasm: " + err.Error())
	}
	if prev, ok := bc.emitSources[symbolIdx]; ok && prev != source {
		panic(fmt.Sprintf("wasm: symbol_idx=%d received both %s and %s in the same bar: %s",
			symbolIdx, prev, source, contracts.ErrEmitSourcesMixed))
	}
	bc.emitSources[symbolIdx] = source
	bc.emissionsInBar++
}

// ─── v1 家族 ───────────────────────────────────────────────────────

// hostGetBar 实现 get_bar(t_offset i32, field_id i32) → f64。
//
// v1 语义不变：只读 close；symbol scope ⇒ Bars[0][T+t_offset]。t_offset > 0
// 直接 trap；历史越界/无数据返回 NaN。
func hostGetBar(ctx context.Context, tOffset, fieldID int32) float64 {
	if tOffset > 0 {
		trapFuture(tOffset)
	}
	bc := barContextFrom(ctx)
	if bc == nil {
		trapNoContext(contracts.HostFnGetBar)
	}
	_ = fieldID // v1 冻结语义：恒取 close
	if len(bc.Bars) == 0 {
		return math.NaN()
	}
	idx := bc.T + tOffset
	if idx < 0 || int(idx) >= len(bc.Bars[0]) {
		return math.NaN()
	}
	return bc.Bars[0][idx].Close
}

// hostGetSeriesLen 实现 get_series_len() → i32：v1 的序列长度，行为不变。
func hostGetSeriesLen(ctx context.Context) int32 {
	bc := barContextFrom(ctx)
	if bc == nil {
		trapNoContext(contracts.HostFnGetSeriesLen)
	}
	if len(bc.Bars) == 0 {
		return 0
	}
	return int32(len(bc.Bars[0]))
}

// hostGetSymbolCount 实现 get_symbol_count() → i32：全市场 symbol 数。
func hostGetSymbolCount(ctx context.Context) int32 {
	bc := barContextFrom(ctx)
	if bc == nil {
		trapNoContext(contracts.HostFnGetSymbolCount)
	}
	return int32(len(bc.Symbols))
}

// hostGetCrossSection 实现 get_cross_section(t_offset, field_id, idx) → f64。
//
// v1 行为保留：只读 close；symbol scope（Axis 为空）没有横截面 ⇒ NaN。
// market scope 下从 Axis + Bars 求值，不再有第二份 CrossSection 真相。
func hostGetCrossSection(ctx context.Context, tOffset, fieldID, idx int32) float64 {
	if tOffset > 0 {
		trapFuture(tOffset)
	}
	bc := barContextFrom(ctx)
	if bc == nil {
		trapNoContext(contracts.HostFnGetCrossSection)
	}
	_ = fieldID
	if !bc.marketScope() || idx < 0 || int(idx) >= len(bc.Bars) {
		return math.NaN()
	}
	bar, ok := bc.barAt(idx, bc.T+tOffset)
	if !ok {
		return math.NaN()
	}
	return bar.Close
}

// hostGetState 实现 get_state(key_ptr i32, key_len i32) → i64（打包 ptr|len）。
// 读宿主管理的持久化状态；key 不存在返回 pack(0,0)。
func hostGetState(ctx context.Context, m api.Module, keyPtr, keyLen uint32) uint64 {
	bc := barContextFrom(ctx)
	if bc == nil {
		trapNoContext(contracts.HostFnGetState)
	}
	key, ok := m.Memory().Read(keyPtr, keyLen)
	if !ok {
		panic("wasm: get_state key out of range")
	}
	val, exists := bc.State[string(key)]
	if !exists {
		return PackPtrLen(0, 0)
	}
	if len(val) > contracts.MaxStateValueBytes {
		panic(fmt.Sprintf("wasm: state value %d bytes > MaxStateValueBytes=%d", len(val), contracts.MaxStateValueBytes))
	}
	dest := bc.ScratchPtr
	if dest == 0 {
		dest = 4096 // v1 legacy 布局
	}
	if bc.ScratchSize > 0 && uint32(len(val)) > bc.ScratchSize {
		panic(fmt.Sprintf("wasm: state value %d bytes > declared scratch %d", len(val), bc.ScratchSize))
	}
	if !m.Memory().Write(dest, val) {
		panic("wasm: get_state write out of range")
	}
	return PackPtrLen(dest, uint32(len(val)))
}

// hostSetState 实现 set_state(key_ptr, key_len, val_ptr, val_len) → void。
func hostSetState(ctx context.Context, m api.Module, keyPtr, keyLen, valPtr, valLen uint32) {
	bc := barContextFrom(ctx)
	if bc == nil {
		trapNoContext(contracts.HostFnSetState)
	}
	key, ok := m.Memory().Read(keyPtr, keyLen)
	if !ok {
		panic("wasm: set_state key out of range")
	}
	val, ok := m.Memory().Read(valPtr, valLen)
	if !ok {
		panic("wasm: set_state value out of range")
	}
	keyCount := len(bc.State)
	nextBytes := stateBytes(bc.State) + len(val)
	if old, exists := bc.State[string(key)]; exists {
		nextBytes -= len(old)
	} else {
		keyCount++
	}
	if err := contracts.CheckStateBudget(keyCount, nextBytes); err != nil {
		panic("wasm: " + err.Error())
	}
	v := make([]byte, len(val))
	copy(v, val)
	bc.State[string(key)] = v
}

func stateBytes(state map[string][]byte) int {
	total := 0
	for k, v := range state {
		total += len(k) + len(v)
	}
	return total
}

// hostEmitSignal 实现 emit_signal(symbol_idx i32, action i32, strength f64) → void。
//
// 校验（A5）：symbol 越界 / action 枚举 / strength finite∈[0,1] 全部 trap。
// finalize 期间发 long/short → trap（只允许 close/hold）。
// SuppressSignals=true 时（state_only 重放，N4）信号「成功但不记录」。
func hostEmitSignal(ctx context.Context, symbolIdx, action int32, strength float64) {
	bc := barContextFrom(ctx)
	if bc == nil {
		trapNoContext(contracts.HostFnEmitSignal)
	}
	act := contracts.Action(action)
	if !act.Valid() {
		panic(fmt.Sprintf("wasm: emit_signal action=%d is outside the frozen enum {0,1,2,3}: %s",
			action, contracts.ErrActionIllegal))
	}
	if err := contracts.ValidateStrength(strength); err != nil {
		panic("wasm: " + err.Error())
	}
	if bc.inFinalize && (act == contracts.ActionLong || act == contracts.ActionShort) {
		panic(fmt.Sprintf("wasm: emit_signal action=%d is not allowed during finalize (only close/hold; §2.2-14)", action))
	}
	bc.beginEmission(symbolIdx, contracts.EmitSourceSignal)
	if bc.SuppressSignals {
		bc.SuppressedSignals++
		return
	}
	if act == contracts.ActionHold {
		return
	}
	bc.Signals = append(bc.Signals, EmittedSignal{
		SymbolIdx: symbolIdx,
		Action:    action,
		Strength:  strength,
	})
}

// hostLog 实现 log(level i32, msg_ptr i32, msg_len i32) → void。
// 刻意空实现：沙箱内日志不落盘、不外发（ADR-029 §3.2 无文件/网络）。
func hostLog(_ context.Context, _ api.Module, _, _, _ uint32) {}

// ─── v2 家族（QT-24 数据面）───────────────────────────────────────

// hostGetBar2 实现 get_bar2(symbol_idx i32, t_offset i32, field_id i32) → f64。
//
// market：date := Axis[T+off] → Bars[sym] 该日 bar；symbol：Bars[0][T+off]
// 且 symbol_idx 必须为 0。缺 bar 返回 NaN（停牌/未上市不得用 0 掩盖）。
func hostGetBar2(ctx context.Context, symbolIdx, tOffset, fieldID int32) float64 {
	if tOffset > 0 {
		trapFuture(tOffset)
	}
	bc := barContextFrom(ctx)
	if bc == nil {
		trapNoContext(contracts.HostFnGetBar2)
	}
	if !contracts.FieldID(fieldID).Valid() {
		panic(fmt.Sprintf("wasm: field_id=%d is outside the frozen enum {0..4}", fieldID))
	}
	if symbolIdx < 0 || int(symbolIdx) >= len(bc.Symbols) {
		panic(fmt.Sprintf("wasm: symbol_idx=%d out of range [0,%d)", symbolIdx, len(bc.Symbols)))
	}
	if !bc.marketScope() && symbolIdx != 0 {
		panic(fmt.Sprintf("wasm: symbol scope requires symbol_idx=0, got %d", symbolIdx))
	}
	idx := bc.T + tOffset
	if idx < 0 {
		panic(fmt.Sprintf("wasm: get_bar2 t_offset=%d makes T+t_offset negative", tOffset))
	}
	bar, ok := bc.barAt(symbolIdx, idx)
	if !ok {
		return math.NaN()
	}
	return ohlcvField(bar, fieldID)
}

// hostGetSeriesLen2 实现 get_series_len2(symbol_idx i32) → i32。
// 返回**可见长度**（§7.1），不是 len(Bars[sym])。
func hostGetSeriesLen2(ctx context.Context, symbolIdx int32) int32 {
	bc := barContextFrom(ctx)
	if bc == nil {
		trapNoContext(contracts.HostFnGetSeriesLen2)
	}
	if symbolIdx < 0 || int(symbolIdx) >= len(bc.Symbols) {
		panic(fmt.Sprintf("wasm: symbol_idx=%d out of range [0,%d)", symbolIdx, len(bc.Symbols)))
	}
	if !bc.marketScope() && symbolIdx != 0 {
		panic(fmt.Sprintf("wasm: symbol scope requires symbol_idx=0, got %d", symbolIdx))
	}
	return bc.visibleSeriesLen(symbolIdx)
}

// hostGetDate2 实现 get_date2() → i32（YYYYMMDD，UTC）。
func hostGetDate2(ctx context.Context) int32 {
	bc := barContextFrom(ctx)
	if bc == nil {
		trapNoContext(contracts.HostFnGetDate2)
	}
	date, ok := bc.currentDate()
	if !ok {
		panic(fmt.Sprintf("wasm: %s called outside the replay axis (T=%d)", contracts.HostFnGetDate2, bc.T))
	}
	return dayKey(date)
}

// hostEmitTarget2 实现 emit_target2(symbol_idx i32, weight f64) → void。
//
// weight 语义冻结（§8.2）：-1 = 不动、0 = 清仓、正数 = 目标多头权重 ∈ (0,1]。
// 非 finite / 越界 → trap。finalize 期间只允许 0（清仓）与 -1（不动）：
// 用最后已知价开新仓是前视/自欺，trap 掉。
//
// 与 emit_signal 同 bar 同 symbol 混用 → trap（两套 sizing 不许打架）。
func hostEmitTarget2(ctx context.Context, symbolIdx int32, weight float64) {
	bc := barContextFrom(ctx)
	if bc == nil {
		trapNoContext(contracts.HostFnEmitTarget2)
	}
	if err := contracts.ValidateTargetWeight(weight); err != nil {
		panic("wasm: " + err.Error())
	}
	if bc.inFinalize && weight != 0 && weight != -1 {
		panic(fmt.Sprintf("wasm: emit_target2 weight=%v is not allowed during finalize (only 0=clear / -1=hold; §2.2-14)", weight))
	}
	bc.beginEmission(symbolIdx, contracts.EmitSourceTarget)
	if bc.SuppressSignals {
		bc.SuppressedSignals++
		return
	}
	bc.Targets = append(bc.Targets, EmittedTarget{SymbolIdx: symbolIdx, Weight: weight})
}
