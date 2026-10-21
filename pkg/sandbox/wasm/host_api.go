package wasm

// K6 切片 2：host API 白名单 —— 前视隔离是唯一可行机制（ADR-029 §3）。
//
// 核心原则：不靠审阅代码保证安全，靠**能力隔离**。WASM 实例只能调用宿主
// 显式导入的函数；没有 import 的能力它物理上做不到。本节实现：
//
//  1. host 导入函数白名单：模块名固定 "env"，函数名与签名由
//     `pkg/backtest/contracts` 的 abi 契约表冻结（QT-01）。v1 家族 8 个
//     （get_bar / get_series_len / get_symbol_count / get_cross_section /
//     get_state / set_state / emit_signal / log）全部**已实现**；v2 家族 4 个
//     （get_bar2 / get_series_len2 / get_date2 / emit_target2）**已注册但函数体
//     待接线**（见 host_api_v2_pending.go：调用即 trap，点名 QT-24/QT-26）。
//     注册而不实现是为了让 v2 artifact 能走完「实例化 → 宿主区校验」这段
//     加载路径，从而本单的 fail-closed 门可被真实字节码验证，而不是只能造假输入。
//  2. `get_bar(t_offset, field_id)` 在 `t_offset > 0` 时**直接 panic**（wazero
//     把 panic 转成 wasm trap），使「读未来数据」物理上不可能。get_cross_section
//     同样 trap。
//  3. 刻意**不提供**文件/网络/时钟/随机源/goroutine/进程/环境变量 ——
//     缺这些正是安全性的来源（ADR-029 §3.2 末）。
//
// 数据模型（per-symbol 实例）：ADR-029 §3 的 get_bar 签名**没有 symbol 参数**，
// 因此一个 WASM 实例绑定「当前 symbol 的序列」（BarContext.Series），get_bar
// 操作它；跨 symbol 用 get_cross_section(t_offset, field_id, idx)（BarContext.CrossSection
// + Symbols）。
//
// 「当前回放上下文」通过 context.WithValue 注入：调用 on_bar(t) 前 host 把
// *BarContext 塞进 ctx，host 函数从 ctx 取出（wazero 的 host 函数 ctx 是调用链
// 透传的，见 internal/wasm/gofunc.go 的 reflectGoModuleFunction.Call）。

import (
	"context"
	"fmt"
	"math"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"tellurion/pkg/backtest/contracts"
)

// HostModuleName 是 host 导入函数的模块名，wasm 二进制 import 它。
const HostModuleName = "env"

// host 函数的导出名一律取自 abi 契约（contracts.HostFn*）——本包不再自带一份
// 名字表。理由与 QT-01 ① 同源：名字/签名/语义只有一处真相，两边各写一份必然漂移。

// hostCtxKey 是 context 中注入 *BarContext 的私有键。
type hostCtxKey struct{}

// BarContext 是 host 函数可见的「当前回放上下文」。
//
// 数据模型（per-symbol 实例）：一个 WASM 实例绑定一个 symbol 的序列。
//   - T        ：当前回放的 bar 索引（从 0 起）。[0, T] 是已可见历史 + 当前
//     bar，[T+1, ...] 是未来，get_bar 不可达（t_offset > 0 trap）。
//   - Series   ：当前 symbol 的逐 bar close 序列（get_bar / get_series_len 操作它）。
//   - CrossSection：全市场横截面（t → symbol → close），get_cross_section 用。
//   - Symbols  ：有序 symbol 列表（get_symbol_count / get_cross_section idx 的
//     确定性序）。
//   - State    ：宿主管理的持久化状态（get_state/set_state 载体；切片 3 的
//     SaveState/LoadState 续跑一致性依赖它）。
//   - Signals  ：emit_signal 收集的信号（host 可即时校验）。
type BarContext struct {
	T            int32
	Series       []float64
	CrossSection map[int32]map[string]float64
	Symbols      []string
	State        map[string][]byte
	Signals      []EmittedSignal
}

// EmittedSignal 是 wasm 经 emit_signal 发出的信号。
type EmittedSignal struct {
	SymbolIdx int32
	Action    int32
	Strength  float64
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
// 分裂。v2 四个函数的体待 QT-24/QT-26 接线，现在调用即 trap。
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

// hostGetBar 实现 get_bar(t_offset i32, field_id i32) → f64。
// 取当前 symbol 在 t+T 的 close。t_offset > 0 直接 trap；field_id 本切片恒取
// close（SeriesSpec 接线属后续切片，见注释）。
func hostGetBar(ctx context.Context, tOffset, fieldID int32) float64 {
	if tOffset > 0 {
		trapFuture(tOffset)
	}
	bc := barContextFrom(ctx)
	if bc == nil {
		trapNoContext(contracts.HostFnGetBar)
	}
	_ = fieldID // 本切片 field_id 未接线 SeriesSpec，恒取 close
	idx := bc.T + tOffset
	if idx < 0 || int32(len(bc.Series)) <= idx {
		return math.NaN() // 历史越界/无数据 → NaN（策略自行判断「不存在」）
	}
	return bc.Series[idx]
}

// hostGetSeriesLen 实现 get_series_len() → i32：当前 symbol 到当前 bar 的长度。
func hostGetSeriesLen(ctx context.Context) int32 {
	bc := barContextFrom(ctx)
	if bc == nil {
		trapNoContext(contracts.HostFnGetSeriesLen)
	}
	return int32(len(bc.Series))
}

// hostGetSymbolCount 实现 get_symbol_count() → i32：全市场 symbol 数。
func hostGetSymbolCount(ctx context.Context) int32 {
	bc := barContextFrom(ctx)
	if bc == nil {
		trapNoContext(contracts.HostFnGetSymbolCount)
	}
	return int32(len(bc.Symbols))
}

// hostGetCrossSection 实现 get_cross_section(t_offset i32, field_id i32, idx i32) → f64。
// 取「当前 t + t_offset」横截面上第 idx 个 symbol 的 close。t_offset > 0 trap。
func hostGetCrossSection(ctx context.Context, tOffset, fieldID, idx int32) float64 {
	if tOffset > 0 {
		trapFuture(tOffset)
	}
	bc := barContextFrom(ctx)
	if bc == nil {
		trapNoContext(contracts.HostFnGetCrossSection)
	}
	_ = fieldID
	t := bc.T + tOffset
	if t < 0 || idx < 0 || int32(len(bc.Symbols)) <= idx {
		return math.NaN()
	}
	sym := bc.Symbols[idx]
	row, ok := bc.CrossSection[t]
	if !ok {
		return math.NaN()
	}
	if v, ok := row[sym]; ok {
		return v
	}
	return math.NaN()
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
	if !m.Memory().Write(4096, val) {
		panic("wasm: get_state write out of range")
	}
	return PackPtrLen(4096, uint32(len(val)))
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
	v := make([]byte, len(val))
	copy(v, val)
	bc.State[string(key)] = v
}

// hostEmitSignal 实现 emit_signal(symbol_idx i32, action i32, strength f64) → void。
func hostEmitSignal(ctx context.Context, symbolIdx, action int32, strength float64) {
	bc := barContextFrom(ctx)
	if bc == nil {
		trapNoContext(contracts.HostFnEmitSignal)
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
