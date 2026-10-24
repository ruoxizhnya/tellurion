package wasm

// K6 切片 3：协议改造 generate_signals → 逐 bar on_bar(t) + finalize（ADR-029 §3.3）。
//
// 前视漏洞（⑭）：旧协议 generate_signals(bars_ptr, bars_len) 是 host 一次性写
// 入全部 bars，沙箱内代码读 bars[len-1] 就拿到未来数据。新协议改为：
//
//	initialize(params_ptr, params_len) → i32    // 保留
//	on_bar(t: i32) → i64                        // 逐 bar 推进；数据只能经
//	                                            // get_bar(t_offset ≤ 0) 取
//	finalize() → i64                            // 收尾，冲刷最终信号
//
// on_bar(t) 每次由 host 调用，推进「当前 t」；沙箱内取数只能走 get_bar（切片 2
// 已实现 t_offset>0 → trap），故前视物理不可能。finalize 返回打包 (ptr,len) 的
// 最终信号（或 pack(0,0) 表示无信号）。
//
// 状态断点续跑（ADR-029 §3.5 + K6 验收「SaveState/LoadState 续跑一致」）：
// get_state/set_state 让状态由 host 管理。BarContext.State 是状态的载体：
//   - Batch（回测）：host 内存里维护 BarContext，跑完全序列；
//   - Step（实盘）：每 bar 调 on_bar，BarContext.State 序列化持久化，重启后
//     载入继续；
//   一致性属性测试：Batch ≡ Step从零 ≡ Step从持久化恢复（ADR-028 §7 三路）。

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/engine"
)

// ─── 预算与终止（QT-15 / C1 双层预算）────────────────────────────
//
// 预算是双层的：单次调用预算管「一个 bar 卡死」，run 级 deadline 管「每 bar
// 都合法、整体无限」。单次预算 ≠ run 预算，两者缺一不可。

// SessionBudget 是一个 on_bar 会话的双层预算。零值 PerCall 表示沿用 sandbox
// 配置的 MaxExecutionTime（旧行为），RunDeadline ≤ 0 表示沿用 DefaultRunDeadline。
type SessionBudget struct {
	PerCall     time.Duration
	RunDeadline time.Duration
}

const (
	// DefaultMarketCallBudget 是 scope=market 的单次调用预算（C1 冻结值）。
	DefaultMarketCallBudget = 50 * time.Millisecond
	// DefaultSymbolCallBudget 是 scope=symbol 的单次调用预算（C1 冻结值）。
	DefaultSymbolCallBudget = 5 * time.Millisecond
	// DefaultRunDeadline 是 run 级 deadline 的默认值（C1 冻结值：10min）。
	DefaultRunDeadline = 10 * time.Minute
)

// BudgetForScope 按 scope 返回默认双层预算。scope 的合法值取自冻结契约
// contracts.StrategyScope——未知值直接报错，预算是 fail-closed 的一部分，
// 宁可拒绝也不静默套一个「看起来合理」的默认。
func BudgetForScope(scope contracts.StrategyScope) (SessionBudget, error) {
	switch scope {
	case contracts.ScopeMarket:
		return SessionBudget{PerCall: DefaultMarketCallBudget, RunDeadline: DefaultRunDeadline}, nil
	case contracts.ScopeSymbol:
		return SessionBudget{PerCall: DefaultSymbolCallBudget, RunDeadline: DefaultRunDeadline}, nil
	default:
		return SessionBudget{}, fmt.Errorf("wasm: unknown scope %q (want %q or %q)", scope, contracts.ScopeMarket, contracts.ScopeSymbol)
	}
}

// ErrSessionTerminated 是会话被终止（预算耗尽 / run deadline 到期）后的哨兵：
// 模块已被 WithCloseOnContextDone 关闭，实例不可再用，任何后续调用都返回
// 包着本哨兵的错误。恢复路径 = 从最近检查点重建实例并重放（N4 的
// state_only 裁决，见 replay.go 的 SessionSupervisor）。
var ErrSessionTerminated = errors.New("wasm: session terminated")

// OnBarSession 是「逐 bar on_bar(t) + finalize」协议的会话，替代旧
// StrategyPluginSession 的 generate_signals 一次性协议。
//
// 用法：
//
//	sess, _ := sandbox.NewOnBarSession(ctx, wasmBytes)
//	defer sess.Close()
//	sess.Initialize(ctx, paramsJSON)
//	bc := &BarContext{Axis: axis, Symbols: runUniverse, Bars: bars, State: map[string][]byte{}, ...}
//	for t := 0; t < len(series); t++ {
//	    bc.T = int32(t)
//	    sess.OnBar(ctx, bc)          // 内部把 bc 注入 ctx，调 on_bar(t)
//	}
//	signals, _ := sess.Finalize(ctx, bc)  // 冲刷最终信号
//
// 终止语义（QT-15 ③）：任何一次调用把双层预算耗尽，模块连同执行一起被
// wazero 关闭，会话进入 terminated——后续 Initialize/OnBar/Finalize 一律
// fail-loud 返回 ErrSessionTerminated，不再有机会静默返回半截结果。
type OnBarSession struct {
	inst    Instance
	sandbox *WASMSandbox

	budget    SessionBudget
	runCtx    context.Context
	runCancel context.CancelFunc

	mu         sync.Mutex
	terminated bool
	termErr    error
}

// NewOnBarSession compiles wasmBytes and instantiates an on_bar-protocol session.
// 调用方必须先 InstantiateHostModule（见 WazeroRuntime.InstantiateHostModule）。
//
// QT-01 的加载门在这条路径上串起来（时序即契约）：
//
//	Compile（白名单 + 族互斥 + 签名比对）
//	  → DetectHostAbiFamily（import section 自证，**先于**实例化）
//	  → Instantiate
//	  → ValidateLoadedArtifact（v2 强制宿主区声明；v1 走 legacy 分支 + warning）
//	  → Initialize（params ≤ MaxParamsBytes）
//
// v1 artifact 的行为在这条链上零变化：族判定得到 v1，ValidateLoadedArtifact 只回
// 一条 legacy_host_layout 诊断。v2 artifact 若没声明宿主区，就会在这里被拒——
// 而不是等到 host 把 params 写进模块自己的数据段。
func (s *WASMSandbox) NewOnBarSession(ctx context.Context, wasmBytes []byte) (*OnBarSession, error) {
	return s.NewOnBarSessionWithBudget(ctx, wasmBytes, SessionBudget{})
}

// NewOnBarSessionWithBudget 带**双层预算**构造会话（QT-15 ②）。budget.PerCall
// ≤ 0 沿用 sandbox 配置；budget.RunDeadline ≤ 0 沿用 DefaultRunDeadline。run
// 级 deadline 从本调用起算——它管的是「整个 run 的墙钟」，不是单次调用。
func (s *WASMSandbox) NewOnBarSessionWithBudget(ctx context.Context, wasmBytes []byte, budget SessionBudget) (*OnBarSession, error) {
	module, err := s.Compile(ctx, wasmBytes)
	if err != nil {
		return nil, err
	}
	abiMod, ok := module.(AbiModule)
	if !ok {
		_ = module.Close(ctx)
		return nil, fmt.Errorf("wasm: runtime %T does not expose the import section, so the abi family cannot be self-proved", s.runtime)
	}
	family, err := DetectHostAbiFamily(abiMod)
	if err != nil {
		_ = module.Close(ctx)
		// abi 族判定失败 = 跨边界约定不符（QT-10 ③-②）⇒ ContractBreach。
		return nil, classifyExecutionError(contracts.ErrContractBreach, err)
	}
	inst, err := s.InstantiateModule(ctx, module)
	if err != nil {
		module.Close(ctx)
		return nil, err
	}
	// QT-62：v2 的宿主区判定要拿模块自己的数据段，而 wazero 的 CompiledModule
	// 不暴露 Data section ⇒ 从原始字节解一次（纯静态、不执行、无副作用）。
	// v1 不解析：它的宿主区走 legacy 布局，声明校验整体跳过。
	var dataImage contracts.StaticDataImage
	if family == contracts.HostAbiV2 {
		img, perr := ParseStaticDataImage(wasmBytes)
		if perr != nil {
			_ = inst.Close()
			// artifact 静态镜像校验失败 = 策略制品坏了 ⇒ StrategyFault。
			return nil, classifyExecutionError(contracts.ErrStrategyFault, perr)
		}
		dataImage = img
	}
	diagnostics, err := ValidateLoadedArtifact(inst, family, dataImage)
	if err != nil {
		_ = inst.Close()
		// artifact 校验失败（宿主区声明缺失等）⇒ StrategyFault（QT-10 ③-②）。
		return nil, classifyExecutionError(contracts.ErrStrategyFault, err)
	}
	for _, d := range diagnostics {
		s.log.Warn().Str("abi_diagnostic", d).Str("abi_version", family.String()).
			Msg("wasm artifact uses the legacy host memory layout (params at offset 0, scratch at 4096) — " +
				"host writes are not covered by a module declaration")
	}
	deadline := budget.RunDeadline
	if deadline <= 0 {
		deadline = DefaultRunDeadline
	}
	// run 级 deadline 派生自请求 ctx（C1：deadline 来自请求 ctx，默认 10min）。
	// 调用方取消 ctx 同样会把模块关闭——那也是一次终止，不是普通错误。
	runCtx, runCancel := context.WithTimeout(ctx, deadline)
	return &OnBarSession{inst: inst, sandbox: s, budget: budget, runCtx: runCtx, runCancel: runCancel}, nil
}

// checkAlive 在每次协议调用前守门：terminated 会话禁止任何调用（QT-15 ③）。
// 模块已被 wazero 关闭，再调只会拿到误导性的底层错误；这里直接点名哨兵。
func (sp *OnBarSession) checkAlive() error {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.terminated {
		// %w 链上带原始终止原因：调用方可以 errors.Is 追到根因。
		// QT-10 ③-②：终止（预算耗尽 / deadline）是策略执行故障 ⇒ StrategyFault，
		// 引擎据此终止 run 而不是跳天。
		return classifyExecutionError(contracts.ErrStrategyFault,
			fmt.Errorf("%w: %w", ErrSessionTerminated, sp.termErr))
	}
	return nil
}

// terminate 把会话标记为终止。幂等：首次写入的错误胜出。
func (sp *OnBarSession) terminate(cause error) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if !sp.terminated {
		sp.terminated = true
		sp.termErr = cause
	}
}

// guardedCall 是协议调用的统一入口：先守门（terminated 即拒），再给单次调用
// 套预算（PerCall ≤ 0 沿用 sandbox 的 MaxExecutionTime），最后把「ctx 到期导致
// 模块被关」归类为 ErrSessionTerminated（wazero 的 sys.ExitError 会用
// errors.Is 匹配 context.DeadlineExceeded / context.Canceled，普通 trap 不会
// 走这条——trap 不关模块，照常作为普通错误返回）。
func (sp *OnBarSession) guardedCall(ctx context.Context, invoke func(context.Context) error) error {
	if err := sp.checkAlive(); err != nil {
		return err
	}
	perCall := sp.budget.PerCall
	if perCall <= 0 {
		perCall = sp.sandbox.config.MaxExecutionTime
	}
	callCtx, cancel := context.WithTimeout(sp.runCtx, perCall)
	defer cancel()
	if err := invoke(callCtx); err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			sp.terminate(err)
			sp.runCancel()
			return classifyExecutionError(contracts.ErrStrategyFault,
				fmt.Errorf("%w: %w", ErrSessionTerminated, err))
		}
		// 普通 trap（含 wasm 侧 unreachable / divide-by-zero 等）也是策略
		// 自己跑坏了 ⇒ StrategyFault（QT-10 ③-②），不是数据缺口。
		return classifyExecutionError(contracts.ErrStrategyFault, err)
	}
	return nil
}

// Initialize calls the module's "initialize" export with JSON params.
// Returns nil on success (the module returns 0).
//
// QT-01 / A6：params 上限是**契约**不是建议。超限一律拒绝，不截断——截断等于
// 给策略一份残缺 JSON，表现是「参数没生效」这种最难查的静默错；而旧实现里
// params > 4096 还会被后续 state 写入直接盖住尾巴（A6 点名的病）。
//
// 写入地址目前仍是 legacy 的绝对偏移 0；abi v2 应按模块声明的
// __host_params_ptr 写并用 contracts.ValidateParamsWrite 复核可用区间，
// 那一步属 QT-26。上限本身与地址无关，所以现在就能生效。
func (sp *OnBarSession) Initialize(ctx context.Context, params []byte) error {
	if len(params) > contracts.MaxParamsBytes {
		// 参数超限是跨边界的约定被违反（A6）⇒ ContractBreach，不是策略跑坏。
		return classifyExecutionError(contracts.ErrContractBreach, fmt.Errorf("%w: params is %d bytes > MaxParamsBytes=%d (rejected, not truncated)",
			contracts.ErrParamsTooLarge, len(params), contracts.MaxParamsBytes))
	}
	if err := sp.inst.WriteMemory(0, params); err != nil {
		return fmt.Errorf("write params: %w", err)
	}
	return sp.guardedCall(ctx, func(c context.Context) error {
		results, err := sp.sandbox.CallWithTimeout(c, sp.inst, "initialize", 0, uint64(len(params)))
		if err != nil {
			return err
		}
		if len(results) == 0 || results[0] != 0 {
			return fmt.Errorf("initialize returned non-zero: %v", results)
		}
		return nil
	})
}

// OnBar 推进一个 bar：把 bc 注入 ctx，调用 wasm 的 on_bar(t)（t = bc.T）。
//
// 关键：bc 经 context.WithValue 注入，on_bar 内的 get_bar 等 host 函数从 ctx
// 取出（见 host_api.go 的 barContextFrom）。每次调用前调用方应更新 bc.T。
func (sp *OnBarSession) OnBar(ctx context.Context, bc *BarContext) error {
	if bc == nil {
		return classifyExecutionError(contracts.ErrContractBreach,
			errors.New("wasm: OnBar requires non-nil BarContext"))
	}
	// 单 bar 发射预算按「一次 on_bar 调用」重置（QT-26）：重放与重新执行
	// 是两次调用，各自受一次 MaxSignalsPerBar 约束。
	bc.resetEmissionBar()
	return sp.guardedCall(ctx, func(c context.Context) error {
		_, err := sp.sandbox.CallWithTimeout(withBarContext(c, bc), sp.inst, "on_bar", uint64(uint32(bc.T)))
		return err
	})
}

// Finalize 调用 wasm 的 finalize()。
//
// QT-24 之后语义写死（§2.2-14）：
//   - finalize 期间 emit 的信号归**最后一个交易日**，由调用方在期末强平
//     **之前**应用；它们落在 `bc.Signals` / `bc.Targets`，**不是**返回值。
//   - 返回值是**诊断字节**（可选的人类可读诊断），不能当信号通道用 ——
//     旧注释把它写成「最终信号」正是平仓信号被静默丢掉的来源。
//   - finalize 里发 long/short 会被 host trap（只允许 close / 清仓）。
//
// bc 注入 ctx 使 finalize 内也能 get_state 冲刷状态。
func (sp *OnBarSession) Finalize(ctx context.Context, bc *BarContext) ([]byte, error) {
	if bc == nil {
		return nil, classifyExecutionError(contracts.ErrContractBreach,
			errors.New("wasm: Finalize requires non-nil BarContext"))
	}
	bc.inFinalize = true
	defer func() { bc.inFinalize = false }()
	var out []byte
	err := sp.guardedCall(ctx, func(c context.Context) error {
		results, err := sp.sandbox.CallWithTimeout(withBarContext(c, bc), sp.inst, "finalize")
		if err != nil {
			return err
		}
		if len(results) == 0 {
			return errors.New("finalize returned no result")
		}
		ptr, length := UnpackPtrLen(results[0])
		if length == 0 {
			out = []byte{}
			return nil
		}
		read, err := sp.inst.ReadMemory(ptr, length)
		if err != nil {
			return err
		}
		out = read
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Close releases the underlying instance. Close 不走 checkAlive：关闭一个已被
// wazero 关掉的模块是幂等清理，终止会话同样需要能被释放。run 级 ctx 一并取消。
func (sp *OnBarSession) Close() error {
	sp.runCancel()
	return sp.inst.Close()
}
