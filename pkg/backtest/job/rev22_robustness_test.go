package job

// REV-22 健壮性集合的测试（core 仓）。
//
// (1) 取消 job 后 DB 写入必须用未取消的 ctx：此前 RunBacktest 失败路径
//     （恰恰包括「取消导致的失败」）用 jobCtx 写 failed，取消时一起失败 ⇒
//     行卡死 running、失败原因丢失。本测试用「runner 等 jobCtx 取消后返回
//     ctx.Err()」复现真实取消路径，断言 UpdateJobFailed 收到的是 Background。
// (3) 信号失败计数：execAlgoRunDiag.recordSignalFailure 汇总进 logSummary。
// (2)(4) 在 tracker 侧：DropLastRecordedOn / 缺价兜底（tracker 包内测）。

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/backtest/contracts"
)

// rev22CancelRunner 模拟「作业被取消」：阻塞直到 ctx.Done，然后返回 ctx.Err()
// ——这正是 jobCtx 被取消时 RunBacktest 的真实失败形态。
type rev22CancelRunner struct {
	started chan struct{}
}

func (r *rev22CancelRunner) RunBacktest(ctx context.Context, req contracts.BacktestRequest) (*contracts.BacktestResponse, error) {
	close(r.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func newRev22Service(store *mockJobStore, runner contracts.EngineRunner) *JobService {
	// registry 是 assembly-only（禁热插拔），每个用例自建一套。
	registry := contracts.NewEngineRegistry()
	registry.Register(contracts.DefaultKernel, runner)
	return NewJobService(store, registry, zerolog.Nop())
}

func TestREV22_CancelledJobStillLandsFailedRecord(t *testing.T) {
	cancelRunner := &rev22CancelRunner{started: make(chan struct{})}
	store := newMockJobStore()
	store.failCtxErrs = map[string]error{}
	svc := newRev22Service(store, cancelRunner)

	jobID := "rev22-cancel"
	require.NoError(t, store.CreateBacktestJob(context.Background(), map[string]any{
		"id":          jobID,
		"strategy_id": "momentum",
		"universe":    "test",
		"start_date":  "2024-01-02",
		"end_date":    "2024-01-31",
		"status":      "pending",
	}))

	svc.StartJob(context.Background(), jobID)

	// 等 runner 进入 RunBacktest，再取消 jobCtx —— 复现「取消导致失败」。
	<-cancelRunner.started
	if cancel, ok := svc.cancelFuncs.Load(jobID); ok {
		cancel.(context.CancelFunc)()
	} else {
		t.Fatal("cancelFuncs 里没有该 job —— StartJob 未注册取消函数")
	}

	// 等失败路径落地（有真实 goroutine，用 Eventually 不用 sleep 猜时序）。
	assert.Eventually(t, func() bool {
		job, err := store.GetBacktestJob(context.Background(), jobID)
		return err == nil && job["status"] == "failed"
	}, 3*time.Second, 20*time.Millisecond, "取消后作业必须落成 failed（不能卡死 running）")

	job, err := store.GetBacktestJob(context.Background(), jobID)
	require.NoError(t, err)
	assert.Contains(t, job["error_msg"], "context canceled", "失败原因必须持久化")

	// 核心断言：写 DB 的 ctx 不是已取消的 jobCtx。
	assert.NoError(t, store.failCtxErrs[jobID],
		"UpdateJobFailed 必须用未取消的 ctx（Background）——用 jobCtx 会让失败记录一起失败")
}

// TestREV22_JobRunnerErrorStillWritesFailedRecord：非取消失败（普通错误）也
// 必须落 failed 记录——同一个修复点的正腿。
func TestREV22_JobRunnerErrorStillWritesFailedRecord(t *testing.T) {
	store := newMockJobStore()
	store.failCtxErrs = map[string]error{}
	svc := newRev22Service(store, &failRunner{err: errors.New("boom")})

	jobID := "rev22-fail"
	require.NoError(t, store.CreateBacktestJob(context.Background(), map[string]any{
		"id": jobID, "strategy_id": "momentum", "universe": "test",
		"start_date": "2024-01-02", "end_date": "2024-01-31", "status": "pending",
	}))
	svc.StartJob(context.Background(), jobID)

	assert.Eventually(t, func() bool {
		job, err := store.GetBacktestJob(context.Background(), jobID)
		return err == nil && job["status"] == "failed"
	}, 3*time.Second, 20*time.Millisecond)
	assert.NoError(t, store.failCtxErrs[jobID])
}

type failRunner struct{ err error }

func (f *failRunner) RunBacktest(context.Context, contracts.BacktestRequest) (*contracts.BacktestResponse, error) {
	return nil, f.err
}
