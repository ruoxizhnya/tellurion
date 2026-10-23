package wasm

// QT-15 / N4：终止后的恢复语义。
//
// C1 定了终止能力与预算（on_bar_session.go 的 guardedCall），本文件定**恢复**：
// 会话被终止（模块已被 wazero 关闭）后，从最近的状态快照重建实例并重放。
//
// N4 冻结的关键裁决：
//   - 重放分 state_only | apply 两种，**必须显式选择**。state_only 是唯一允许
//     的默认：重放期 emit_signal「成功但不记录」（BarContext.SuppressSignals），
//     只推进策略内部状态，计数 SuppressedSignals；重放到故障 bar 后以 apply
//     重执行该 bar，其信号才是唯一被应用的信号——否则 D+1/D+2 已成交的信号
//     会被第二次应用（重复下单，判据 A 且看起来完全正常）。
//   - apply（回滚引擎侧组合/成交/资金再重放）需要引擎支持事务性回滚，本层
//     不提供，构造时直接拒绝。
//   - 重建次数按 **run** 计（不按 bar 计），超限即 failed。
//   - 终止路径不 finalize（模块已 Close）；finalize 语义只在正常路径成立。
//     run 侧「invalid + session_terminated」的落账归引擎接线（QT-26/QT-30），
//     本层提供稳定 code（CodeSessionTerminated）。

import (
	"context"
	"errors"
	"fmt"

	"github.com/ruoxizhnya/tellurion-contracts/engine"
)

// ReplayMode 是重建重放的两种模式（N4 冻结）。
type ReplayMode string

const (
	// ReplayStateOnly：重放期信号不写入 Signals，只推进状态。唯一允许的默认。
	ReplayStateOnly ReplayMode = "state_only"
	// ReplayApply：回滚引擎侧组合/成交/资金到检查点再重放。需要引擎侧事务性
	// 回滚支持，本层不提供——构造时拒绝，不是静默降级。
	ReplayApply ReplayMode = "apply"
)

// CodeSessionTerminated 是 run 因会话终止而降级时使用的稳定 invalid_reasons
// code（N4 订正：不引入第四值 degraded，映射进冻结三值归引擎侧）。
const CodeSessionTerminated = "session_terminated"

// DefaultMaxRebuilds 是单个 run 的重建次数上限（C1：按 run 计，不按 bar 计）。
const DefaultMaxRebuilds = 3

// barSession 是 supervisor 驱动的会话最小面。*OnBarSession 天然满足；
// 测试用 stub 注入瞬态故障（真实 wasm 的确定性 hang 无法靠重建恢复——
// 状态快照早于故障点，那正是「重建上限耗尽 → failed」的规定行为）。
type barSession interface {
	Initialize(ctx context.Context, params []byte) error
	OnBar(ctx context.Context, bc *BarContext) error
	Finalize(ctx context.Context, bc *BarContext) ([]byte, error)
	Close() error
}

// SessionFactory 按 ctx 造一个新会话（已 Initialize）。重建走同一个工厂。
type SessionFactory func(ctx context.Context) (barSession, error)

// SupervisorConfig 是 supervisor 的构造配置。
type SupervisorConfig struct {
	// Scope 决定默认双层预算（BudgetForScope）。未知值构造即拒（fail-closed）。
	Scope string
	// Budget 覆盖 scope 默认值；零值字段沿用 BudgetForScope 的对应项。
	Budget SessionBudget
	// MaxRebuilds 是整个 run 的重建上限；≤0 用 DefaultMaxRebuilds。
	MaxRebuilds int
	// ReplayMode：零值 = ReplayStateOnly（唯一允许的默认）；ReplayApply 构造即拒。
	ReplayMode ReplayMode
	// SnapshotEvery 是状态快照节奏（每 N 个成功 bar 一次）；≤0 = 每 bar。
	// 重放窗口 = 最近快照到故障 bar 之间的 bar 数，节奏越稀窗口越大。
	SnapshotEvery int
	// Params 传给每个新会话的 initialize。
	Params []byte
}

// RunStats 是一次 Run 的结果明细。
type RunStats struct {
	// Rebuilds 是本 run 实际发生的重建次数（超 MaxRebuilds 即失败）。
	Rebuilds int
	// SuppressedSignals 是 state_only 重放期被压制的 emit 次数
	// （N4 的 replay_suppressed_signals；异常放大告警的观测面归引擎接线）。
	SuppressedSignals int
	// TerminatedAtBar 是最后一次故障 bar；-1 = 本 run 从未终止。
	TerminatedAtBar int
	// BarsCompleted 是成功推进的 bar 数（含重放窗口内被重新执行的 bar）。
	BarsCompleted int
	// FinalOutput 是正常路径 finalize() 的返回（终止路径不 finalize）。
	FinalOutput []byte
}

// SessionSupervisor 驱动「逐 bar on_bar + 终止重建重放」的完整 run。
type SessionSupervisor struct {
	cfg           SupervisorConfig
	maxRebuilds   int
	snapshotEvery int
	factory       SessionFactory
}

// NewSessionSupervisor 校验配置并构造带默认工厂的 supervisor。
func NewSessionSupervisor(s *WASMSandbox, wasmBytes []byte, cfg SupervisorConfig) (*SessionSupervisor, error) {
	mode := cfg.ReplayMode
	if mode == "" {
		mode = ReplayStateOnly
	}
	if mode != ReplayStateOnly {
		return nil, fmt.Errorf("wasm: replay_mode %q is rejected by this layer: apply requires engine-side transactional rollback of portfolio/fills (N4); only %q is supported here", mode, ReplayStateOnly)
	}
	budget, err := resolveBudget(cfg)
	if err != nil {
		return nil, err
	}
	maxRebuilds := cfg.MaxRebuilds
	if maxRebuilds <= 0 {
		maxRebuilds = DefaultMaxRebuilds
	}
	snapshotEvery := cfg.SnapshotEvery
	if snapshotEvery <= 0 {
		snapshotEvery = 1
	}
	params := cfg.Params
	factory := func(ctx context.Context) (barSession, error) {
		sess, err := s.NewOnBarSessionWithBudget(ctx, wasmBytes, budget)
		if err != nil {
			return nil, err
		}
		if err := sess.Initialize(ctx, params); err != nil {
			_ = sess.Close()
			return nil, err
		}
		return sess, nil
	}
	return &SessionSupervisor{cfg: cfg, maxRebuilds: maxRebuilds, snapshotEvery: snapshotEvery, factory: factory}, nil
}

// resolveBudget 把 scope 默认值与显式覆盖合成最终预算。
func resolveBudget(cfg SupervisorConfig) (SessionBudget, error) {
	def, err := BudgetForScope(scopeOf(cfg.Scope))
	if err != nil {
		return SessionBudget{}, err
	}
	out := def
	if cfg.Budget.PerCall > 0 {
		out.PerCall = cfg.Budget.PerCall
	}
	if cfg.Budget.RunDeadline > 0 {
		out.RunDeadline = cfg.Budget.RunDeadline
	}
	return out, nil
}

// scopeOf 把字符串折成契约 StrategyScope（未知值原样透传给 Valid 校验）。
func scopeOf(s string) contracts.StrategyScope { return contracts.StrategyScope(s) }

// SetSessionFactory 注入自定义工厂（测试用瞬态故障 stub；生产走默认工厂）。
func (sv *SessionSupervisor) SetSessionFactory(f SessionFactory) { sv.factory = f }

// Run 驱动 nBars 个 bar（bc.T = 0..nBars-1）。
//
// 终止恢复路径（N4 state_only）：
//
//	快照边界 b（状态 = 第 b bar 成功后）→ 第 t bar 终止
//	  → 丢弃第 t bar 的 emit（调用报错 = 引擎未消费）
//	  → 重建 → 恢复 b 的状态 → 重放 [b+1, t-1]（suppress，只推进状态）
//	  → 第 t bar 以 apply 重执行（其信号是唯一新增的应用）
//
// 重放期再次终止走同一条路径（重建计数继续累加，窗口起点不变）。
// 非终止错误（普通 trap）不重建，fail-loud 直接返回。
func (sv *SessionSupervisor) Run(ctx context.Context, bc *BarContext, nBars int) (*RunStats, error) {
	if bc == nil {
		return nil, errors.New("wasm: supervisor Run requires non-nil BarContext")
	}
	if bc.State == nil {
		bc.State = map[string][]byte{}
	}
	stats := &RunStats{TerminatedAtBar: -1}
	sess, err := sv.factory(ctx)
	if err != nil {
		return stats, err
	}
	snapshot := cloneState(bc.State)
	snapshotBar := -1
	faultBar := -1 // >=0 表示 [snapshotBar+1, faultBar) 处于重放窗口
	for t := 0; t < nBars; {
		bc.T = int32(t)
		replaying := faultBar >= 0 && t < faultBar
		bc.SuppressSignals = replaying
		sigStart := len(bc.Signals)
		err := sess.OnBar(ctx, bc)
		if err == nil {
			if replaying {
				t++
				continue
			}
			if faultBar == t {
				// 故障 bar 重执行成功，重放窗口收口：本窗口的压制计数入账。
				faultBar = -1
				stats.SuppressedSignals += bc.SuppressedSignals
				bc.SuppressedSignals = 0
			}
			stats.BarsCompleted++
			if t-snapshotBar >= sv.snapshotEvery {
				snapshotBar = t
				snapshot = cloneState(bc.State)
			}
			t++
			continue
		}
		// 失败 bar 内已发出的信号随调用一起作废（引擎没消费过它们）。
		bc.Signals = bc.Signals[:sigStart]
		if !errors.Is(err, ErrSessionTerminated) {
			_ = sess.Close()
			return stats, err
		}
		if faultBar < 0 {
			faultBar = t
			stats.TerminatedAtBar = t
		}
		_ = sess.Close()
		// 重建次数按 run 计（N4）：stats.Rebuilds 是「已用重建次数」，达到
		// 上限后第 (上限+1) 次终止不再重建，直接 fail-closed。
		if stats.Rebuilds >= sv.maxRebuilds {
			return stats, fmt.Errorf("%w (%s): rebuild cap %d exceeded at bar %d — run failed",
				ErrSessionTerminated, CodeSessionTerminated, sv.maxRebuilds, t)
		}
		stats.Rebuilds++
		sess, err = sv.factory(ctx)
		if err != nil {
			return stats, err
		}
		restoreState(bc, snapshot)
		bc.SuppressedSignals = 0
		t = snapshotBar + 1
	}
	// 正常路径才 finalize（N4：终止路径模块已 Close，finalize 物理不可调）。
	if faultBar >= 0 {
		// 理论不可达：faultBar 只有在窗口收口或重建失败时保持 >=0，
		// 而重建失败已在上面 return。防御性 fail-loud，不静默。
		_ = sess.Close()
		return stats, fmt.Errorf("%w: replay window did not close (fault bar %d)", ErrSessionTerminated, faultBar)
	}
	out, err := sess.Finalize(ctx, bc)
	if err != nil {
		_ = sess.Close()
		return stats, err
	}
	stats.FinalOutput = out
	if err := sess.Close(); err != nil {
		return stats, err
	}
	return stats, nil
}

// cloneState 深拷贝状态快照（快照必须是值拷贝，否则重建恢复拿回的是被污染的现场）。
func cloneState(state map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(state))
	for k, v := range state {
		out[k] = append([]byte(nil), v...)
	}
	return out
}

// restoreState 把快照恢复进 bc（整体替换，重建后的状态 = 快照那一刻的状态）。
func restoreState(bc *BarContext, snapshot map[string][]byte) {
	bc.State = make(map[string][]byte, len(snapshot))
	for k, v := range snapshot {
		bc.State[k] = append([]byte(nil), v...)
	}
}
