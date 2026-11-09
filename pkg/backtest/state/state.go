package state

import (
	"sync"
	"time"

	cdom "github.com/ruoxizhnya/tellurion-contracts/domain"
	"tellurion/pkg/backtest/tracker"
)

import "tellurion/pkg/domain"

// BacktestState holds the state of a backtest run.
//
// Concurrency model (P1-20, ODR-013 Sprint 6):
//   - The Engine's btMu protects the `backtests` map (registration / lookup).
//   - Each BacktestState has its own mu RWMutex protecting its mutable fields
//     (Status / Result / Error / CompletedAt) and a frozen flag.
//   - Read paths (GetBacktestResult / Status / Trades / Equity / Params) take
//     state.mu.RLock() to safely observe a coherent snapshot.
//   - Write paths (Status / Result / Error / CompletedAt updates) take
//     state.mu.Lock() and are rejected if the state is already frozen.
//   - Once a backtest finishes (success or failure), Freeze() is called and
//     the state becomes immutable. This both prevents accidental mutation
//     from late callbacks and unlocks a fast read path.
//
// Pre-freeze write paths (only the running backtest goroutine mutates a
// single state at a time) are serialized through state.mu.Lock(); the
// race detector then has a single owner for any field write, eliminating
// the "Status written by goroutine A, read by goroutine B" race that
// the old map-pointer model could not catch.
type BacktestState struct {
	mu     sync.RWMutex
	frozen bool

	// ID is set once at construction and never changes. Safe to read
	// without locking (immutable after construction).
	ID string

	// Status is one of "running", "completed", "failed". Mutated by
	// the backtest goroutine and observed by API handlers. Protected
	// by mu.
	Status string

	// Params is set at construction and never changes. The struct
	// is value-copied by getter callers, so the snapshot is safe to
	// hand out without further locking. The slice fields inside
	// (StockPool) are not mutated after construction.
	Params domain.BacktestParams

	// Result is non-nil only after Status == "completed". Protected
	// by mu.
	Result *cdom.BacktestResult

	// Tracker has its own internal mutex. Calling its methods is safe
	// from multiple goroutines; the BacktestState-level mu is not
	// held during Tracker access. The Tracker pointer itself never
	// changes after construction.
	Tracker *tracker.Tracker

	// StartedAt is set at construction and never changes.
	StartedAt time.Time

	// CompletedAt is set when the backtest finishes (success or failure).
	// Protected by mu.
	CompletedAt time.Time

	// Error is set on failure; nil otherwise. Protected by mu.
	Error error

	// TargetPositions is only mutated by the running backtest goroutine.
	// It is not read by API handlers, so the BacktestState-level mu is
	// not required for these accesses. Future code that exposes
	// TargetPositions to readers should add locking here.
	TargetPositions map[string]*domain.TargetPosition

	// TerminalPositions 是**强制平仓之前**的期末持仓快照（QT-78）。
	//
	// 回测收尾会强平所有持仓以结算收益，强平后 Tracker 里一条都不剩；
	// 而分析产物（Brinson / 因子归因）要的是「最后一个交易日收盘时仍
	// 持有的仓位」。快照在强平前抄一份，权重口径是快照里的末日收盘市值。
	//
	// Protected by mu.
	TerminalPositions []domain.Position

	// terminalPositionsSet 区分「快照确实为空（策略末日空仓）」与
	// 「还没有快照（运行中 / 没走强平收尾的旧路径）」。前者仍然是一个
	// 真实观测，不能被当成「没有」，否则读面会退回 Tracker 的空表。
	terminalPositionsSet bool
}

// SetStatus atomically updates the status. No-op if the state is frozen.
// Returns the previous status so callers can detect transitions.
func (s *BacktestState) SetStatus(status string) (prev string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.frozen {
		return s.Status, false
	}
	prev = s.Status
	s.Status = status
	return prev, true
}

// SetResult atomically sets the result. No-op if frozen.
func (s *BacktestState) SetResult(r *cdom.BacktestResult) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.frozen {
		return false
	}
	s.Result = r
	return true
}

// SetError atomically sets the error. No-op if frozen.
func (s *BacktestState) SetError(err error) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.frozen {
		return false
	}
	s.Error = err
	return true
}

// SetTerminalPositions 记录强平前的期末持仓快照（QT-78）。
// 入参被复制一份保存，调用方后续修改自己的切片不会影响快照。No-op if frozen.
func (s *BacktestState) SetTerminalPositions(positions []domain.Position) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.frozen {
		return false
	}
	snapshot := make([]domain.Position, len(positions))
	copy(snapshot, positions)
	s.TerminalPositions = snapshot
	s.terminalPositionsSet = true
	return true
}

// GetTerminalPositions 返回期末持仓快照的副本。
// ok=false 表示该 run 没有记录过快照（运行中，或没走强平收尾的旧路径）。
func (s *BacktestState) GetTerminalPositions() ([]domain.Position, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.terminalPositionsSet {
		return nil, false
	}
	out := make([]domain.Position, len(s.TerminalPositions))
	copy(out, s.TerminalPositions)
	return out, true
}

// SetCompletedAt atomically sets the completion timestamp. No-op if frozen.
func (s *BacktestState) SetCompletedAt(t time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.frozen {
		return false
	}
	s.CompletedAt = t
	return true
}

// GetStatus safely reads the current status.
func (s *BacktestState) GetStatus() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Status
}

// GetResult safely reads the result pointer. May be nil if the
// backtest is still running or failed.
func (s *BacktestState) GetResult() *cdom.BacktestResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Result
}

// GetError safely reads the error. Nil if the backtest succeeded.
func (s *BacktestState) GetError() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Error
}

// GetCompletedAt safely reads the completion timestamp.
// Zero value if the backtest is still running.
func (s *BacktestState) GetCompletedAt() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.CompletedAt
}

// IsCompleted reports whether the backtest has finished (success or failure).
// Safe to call concurrently.
func (s *BacktestState) IsCompleted() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.frozen
}

// IsFrozen is an alias for IsCompleted that emphasizes the immutability
// semantics. After Freeze, the state never changes again.
func (s *BacktestState) IsFrozen() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.frozen
}

// Freeze makes the state immutable. Called once when the backtest
// finishes (success or failure). All subsequent Set* calls become
// no-ops, and IsFrozen / IsCompleted return true. Safe to call
// multiple times (idempotent).
func (s *BacktestState) Freeze() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frozen = true
}

// BacktestStateSnapshot is a value copy of all read-only fields, taken
// atomically. Use Snapshot() to read multiple fields coherently without
// holding the lock for the duration of the consumer's work.
type BacktestStateSnapshot struct {
	ID          string
	Status      string
	Params      domain.BacktestParams
	Result      *cdom.BacktestResult
	StartedAt   time.Time
	CompletedAt time.Time
	Error       error
	Frozen      bool
}

// Snapshot returns an atomic, value-copied view of the state. Safe to
// call concurrently; the returned struct is decoupled from the live
// state (mutations after Snapshot returns are not visible in the
// returned copy).
func (s *BacktestState) Snapshot() BacktestStateSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return BacktestStateSnapshot{
		ID:          s.ID,
		Status:      s.Status,
		Params:      s.Params,
		Result:      s.Result,
		StartedAt:   s.StartedAt,
		CompletedAt: s.CompletedAt,
		Error:       s.Error,
		Frozen:      s.frozen,
	}
}
