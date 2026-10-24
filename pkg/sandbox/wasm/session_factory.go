package wasm

// QT-25：OnBarSessionFactory —— 编译一次 artifact，按需实例化多个独立会话。
//
// symbol scope 的执行模型是「每标的一个实例」。若每个分片都走
// NewOnBarSessionWithBudget，加载门里的 Compile 会被重复执行 N 次 ——
// 5000 个分片就是 5000 次字节码编译（真跑 5000×727 时更离谱）。更重要的是
// 每个会话又会各自 NewWazeroRuntime + InstantiateHostModule，把「实例数」
// 和「运行时数」一起乘上去。
//
// 本工厂把三段一次性工作收敛到 BeginRun：
//
//	Compile（import 白名单 + 族判定）
//	  → ParseStaticDataImage（v2 静态数据区间，只解一次）
//	  → 每个分片 InstantiateModule（独立线性内存）
//
// 每个会话仍然独立实例化、独立内存、独立 host 区校验 —— 隔离语义不变；
// 变的是「编译」和「宿主模块注册」不再按分片重复。

import (
	"context"
	"fmt"
	"sync"

	contracts "github.com/ruoxizhnya/tellurion-contracts/engine"
)

// OnBarSessionFactory 持有一份已编译 artifact 与共享 runtime/sandbox。
//
// 并发：NewSession 可以在多个 goroutine 里并发调用（每个分片一个实例）。
// wazero 允许同一 CompiledModule 并发实例化；共享的是编译产物，不是实例
// 内存。Close 与 NewSession 并发时可能有一个会话失败——调用方在关闭工厂
// 后本就不该再起分片。
type OnBarSessionFactory struct {
	sandbox   *WASMSandbox
	module    CompiledModule
	family    contracts.HostAbiVersion
	dataImage contracts.StaticDataImage

	mu     sync.Mutex
	closed bool
}

// NewOnBarSessionFactory 执行一次加载门（Compile + 族自证 + v2 静态镜像），
// 之后由 NewSession 按分片实例化。
//
// 错误分类与 NewOnBarSessionWithBudget 保持一致：族判定失败 = ContractBreach，
// 静态镜像解析失败 = StrategyFault。
func (s *WASMSandbox) NewOnBarSessionFactory(ctx context.Context, wasmBytes []byte) (*OnBarSessionFactory, error) {
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
	var dataImage contracts.StaticDataImage
	if family == contracts.HostAbiV2 {
		// QT-62：v2 的宿主区判定要拿模块自己的数据段，而 wazero 的
		// CompiledModule 不暴露 Data section ⇒ 从原始字节解一次（纯静态、
		// 不执行、无副作用）。v1 不解析：它的宿主区走 legacy 布局。
		img, perr := ParseStaticDataImage(wasmBytes)
		if perr != nil {
			_ = module.Close(ctx)
			// artifact 静态镜像校验失败 = 策略制品坏了 ⇒ StrategyFault。
			return nil, classifyExecutionError(contracts.ErrStrategyFault, perr)
		}
		dataImage = img
	}
	return &OnBarSessionFactory{
		sandbox:   s,
		module:    module,
		family:    family,
		dataImage: dataImage,
	}, nil
}

// Family 返回 artifact 自证的协议族（与 OnBarSession.Family 同口径）。
func (f *OnBarSessionFactory) Family() contracts.HostAbiVersion { return f.family }

// NewSession 为一个分片实例化独立会话。预算语义与
// NewOnBarSessionWithBudget 相同：PerCall ≤ 0 沿用 sandbox 配置，
// RunDeadline ≤ 0 沿用 DefaultRunDeadline；run 级 deadline 从 ctx 派生。
func (f *OnBarSessionFactory) NewSession(ctx context.Context, budget SessionBudget) (*OnBarSession, error) {
	f.mu.Lock()
	closed := f.closed
	f.mu.Unlock()
	if closed {
		return nil, fmt.Errorf("wasm: session factory is closed")
	}

	inst, err := f.sandbox.InstantiateModule(ctx, f.module)
	if err != nil {
		return nil, err
	}
	diagnostics, err := ValidateLoadedArtifact(inst, f.family, f.dataImage)
	if err != nil {
		_ = inst.Close()
		// artifact 校验失败（宿主区声明缺失等）⇒ StrategyFault（QT-10 ③-②）。
		return nil, classifyExecutionError(contracts.ErrStrategyFault, err)
	}
	var hostDecl contracts.HostRegionDeclaration
	hasHostRegion := false
	if f.family == contracts.HostAbiV2 {
		decl, derr := ReadHostRegion(inst.(*wazeroInstance))
		if derr != nil {
			_ = inst.Close()
			return nil, classifyExecutionError(contracts.ErrStrategyFault, derr)
		}
		hostDecl, hasHostRegion = decl, true
	}
	for _, d := range diagnostics {
		f.sandbox.log.Warn().Str("abi_diagnostic", d).Str("abi_version", f.family.String()).
			Msg("wasm artifact uses the legacy host memory layout (params at offset 0, scratch at 4096) — " +
				"host writes are not covered by a module declaration")
	}

	deadline := budget.RunDeadline
	if deadline <= 0 {
		deadline = DefaultRunDeadline
	}
	// run 级 deadline 派生自传入 ctx（C1：deadline 来自请求 ctx，默认 10min）。
	// symbol scope 的多个分片共享同一个父 ctx 时，拿到的是同一个上限。
	runCtx, runCancel := context.WithTimeout(ctx, deadline)
	return &OnBarSession{
		inst:          inst,
		sandbox:       f.sandbox,
		budget:        budget,
		runCtx:        runCtx,
		runCancel:     runCancel,
		family:        f.family,
		hostDecl:      hostDecl,
		hasHostRegion: hasHostRegion,
	}, nil
}

// Close 释放编译产物。已实例化的会话不受影响（它们各自持有自己的实例）。
// 幂等。
func (f *OnBarSessionFactory) Close(ctx context.Context) error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil
	}
	f.closed = true
	f.mu.Unlock()
	return f.module.Close(ctx)
}
