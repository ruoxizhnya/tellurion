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

	"github.com/ruoxizhnya/tellurion-contracts/engine"
)

// OnBarSession 是「逐 bar on_bar(t) + finalize」协议的会话，替代旧
// StrategyPluginSession 的 generate_signals 一次性协议。
//
// 用法：
//
//	sess, _ := sandbox.NewOnBarSession(ctx, wasmBytes)
//	defer sess.Close()
//	sess.Initialize(ctx, paramsJSON)
//	bc := &BarContext{Series: series, State: map[string][]byte{}, ...}
//	for t := 0; t < len(series); t++ {
//	    bc.T = int32(t)
//	    sess.OnBar(ctx, bc)          // 内部把 bc 注入 ctx，调 on_bar(t)
//	}
//	signals, _ := sess.Finalize(ctx, bc)  // 冲刷最终信号
type OnBarSession struct {
	inst    Instance
	sandbox *WASMSandbox
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
		return nil, err
	}
	inst, err := s.InstantiateModule(ctx, module)
	if err != nil {
		module.Close(ctx)
		return nil, err
	}
	diagnostics, err := ValidateLoadedArtifact(inst, family)
	if err != nil {
		_ = inst.Close()
		return nil, err
	}
	for _, d := range diagnostics {
		s.log.Warn().Str("abi_diagnostic", d).Str("abi_version", family.String()).
			Msg("wasm artifact uses the legacy host memory layout (params at offset 0, scratch at 4096) — " +
				"host writes are not covered by a module declaration")
	}
	return &OnBarSession{inst: inst, sandbox: s}, nil
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
		return fmt.Errorf("%w: params is %d bytes > MaxParamsBytes=%d (rejected, not truncated)",
			contracts.ErrParamsTooLarge, len(params), contracts.MaxParamsBytes)
	}
	if err := sp.inst.WriteMemory(0, params); err != nil {
		return fmt.Errorf("write params: %w", err)
	}
	results, err := sp.sandbox.CallWithTimeout(ctx, sp.inst, "initialize", 0, uint64(len(params)))
	if err != nil {
		return err
	}
	if len(results) == 0 || results[0] != 0 {
		return fmt.Errorf("initialize returned non-zero: %v", results)
	}
	return nil
}

// OnBar 推进一个 bar：把 bc 注入 ctx，调用 wasm 的 on_bar(t)（t = bc.T）。
//
// 关键：bc 经 context.WithValue 注入，on_bar 内的 get_bar 等 host 函数从 ctx
// 取出（见 host_api.go 的 barContextFrom）。每次调用前调用方应更新 bc.T。
func (sp *OnBarSession) OnBar(ctx context.Context, bc *BarContext) error {
	if bc == nil {
		return errors.New("wasm: OnBar requires non-nil BarContext")
	}
	ctx = withBarContext(ctx, bc)
	_, err := sp.sandbox.CallWithTimeout(ctx, sp.inst, "on_bar", uint64(uint32(bc.T)))
	return err
}

// Finalize 调用 wasm 的 finalize()，返回打包 (ptr,len) 的最终信号（可能是
// pack(0,0) 表示无信号）。bc 注入 ctx 使 finalize 内也能 get_state 冲刷状态。
func (sp *OnBarSession) Finalize(ctx context.Context, bc *BarContext) ([]byte, error) {
	if bc == nil {
		return nil, errors.New("wasm: Finalize requires non-nil BarContext")
	}
	ctx = withBarContext(ctx, bc)
	results, err := sp.sandbox.CallWithTimeout(ctx, sp.inst, "finalize")
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, errors.New("finalize returned no result")
	}
	ptr, length := UnpackPtrLen(results[0])
	if length == 0 {
		return []byte{}, nil
	}
	return sp.inst.ReadMemory(ptr, length)
}

// Close releases the underlying instance.
func (sp *OnBarSession) Close() error {
	return sp.inst.Close()
}
