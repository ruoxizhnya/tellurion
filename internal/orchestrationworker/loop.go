package orchestrationworker

// worker 循环：注册 → 周期心跳 → 领取 → 执行（期间续租 / 观察取消）→ 上报。
//
// 一次只跑一条 job：单 worker 并发由编排侧的多 worker 完成，进程内不再叠
// goroutine 池——引擎本身是重负载，叠并发只会让租约与内存同时吃紧。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
)

// Options 是循环的时间参数。零值取默认。
type Options struct {
	// HeartbeatEvery 是心跳与续租周期（默认 20s；必须短于编排侧租约）。
	HeartbeatEvery time.Duration
	// PollEvery 是无活时的轮询间隔（默认 2s）。
	PollEvery time.Duration
}

// Loop 是 worker 的主循环。
type Loop struct {
	Client   *Client
	Resolver SpecResolver
	Executor *Executor
	WorkerID string
	Kernels  []string
	Opts     Options
	Log      zerolog.Logger
}

// NewLoop 构造循环并做装配期校验（缺依赖直接 panic：装配错误不该等到第一次领取）。
func NewLoop(client *Client, resolver SpecResolver, executor *Executor, workerID string, kernels []string, opts Options, log zerolog.Logger) *Loop {
	if client == nil {
		panic("orchestrationworker: nil client")
	}
	if resolver == nil {
		panic("orchestrationworker: nil spec resolver")
	}
	if executor == nil {
		panic("orchestrationworker: nil executor")
	}
	if strings.TrimSpace(workerID) == "" {
		panic("orchestrationworker: empty worker id")
	}
	if opts.HeartbeatEvery <= 0 {
		opts.HeartbeatEvery = 20 * time.Second
	}
	if opts.PollEvery <= 0 {
		opts.PollEvery = 2 * time.Second
	}
	return &Loop{
		Client:   client,
		Resolver: resolver,
		Executor: executor,
		WorkerID: workerID,
		Kernels:  append([]string(nil), kernels...),
		Opts:     opts,
		Log:      log,
	}
}

// Run 注册并循环领取，直到 ctx 取消。退出时尽力 drain（draining 不再领新活，
// 心跳停后由编排侧的心跳回收标 offline）。
func (l *Loop) Run(ctx context.Context) error {
	if _, err := l.Client.Register(ctx, l.WorkerID, l.Kernels); err != nil {
		return fmt.Errorf("register worker: %w", err)
	}
	l.Log.Info().Str("worker_id", l.WorkerID).Strs("kernels", l.Kernels).Msg("orchestration worker registered")

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	hbErr := make(chan error, 1)
	go l.heartbeatLoop(runCtx, hbErr)

	defer l.drain()

	for {
		select {
		case <-runCtx.Done():
			return nil
		case err := <-hbErr:
			return fmt.Errorf("heartbeat: %w", err)
		default:
		}

		worked, err := l.Step(runCtx)
		if err != nil {
			if errors.Is(err, ErrWorkerDraining) || errors.Is(err, ErrWorkerNotFound) {
				return err
			}
			l.Log.Error().Err(err).Msg("claim failed")
		}
		if worked {
			continue
		}
		if !sleepCtx(runCtx, l.Opts.PollEvery) {
			return nil
		}
	}
}

// Step 领取并执行至多一条 job。worked=false 表示队列当前为空。
func (l *Loop) Step(ctx context.Context) (bool, error) {
	job, err := l.Client.Claim(ctx, l.WorkerID)
	if err != nil {
		return false, err
	}
	if job == nil {
		return false, nil
	}
	l.execute(ctx, job)
	return true, nil
}

func (l *Loop) execute(ctx context.Context, job *ClaimedJob) {
	if job.CancelRequested {
		l.ackCancel(ctx, job.RunID)
		return
	}

	req, err := l.Resolver.Resolve(ctx, job.RunID, job.RunSpec)
	if err != nil {
		l.reportFailed(ctx, job.RunID, "resolve run spec: "+err.Error())
		return
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var cancelled, leaseLost atomic.Bool
	go l.monitor(runCtx, cancel, job.RunID, &cancelled, &leaseLost)

	outcome, execErr := l.Executor.Execute(runCtx, req)

	switch {
	case leaseLost.Load():
		// 租约已丢：本 worker 写的任何结论都会覆盖别人的终态。保持沉默，
		// 由 ReapExpired 把行收回后重派。
		l.Log.Warn().Str("run_id", job.RunID).Msg("lease lost; dropping this run's outcome")
		return
	case cancelled.Load():
		l.ackCancel(ctx, job.RunID)
		return
	case execErr != nil:
		if ctx.Err() != nil {
			// 进程在退出：不写结论，交给租约回收。
			return
		}
		l.reportFailed(ctx, job.RunID, execErr.Error())
		return
	}

	if outcome.ResultStatus == "failed" {
		l.reportFailed(ctx, job.RunID, outcome.Error)
		return
	}
	l.reportCompleted(ctx, job.RunID, outcome)
}

// monitor 在执期间续租并观察 cancel_requested。任何一项命中都取消执行上下文。
func (l *Loop) monitor(ctx context.Context, cancel context.CancelFunc, runID string, cancelled, leaseLost *atomic.Bool) {
	t := time.NewTicker(l.Opts.HeartbeatEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := l.Client.RenewLease(ctx, runID, l.WorkerID); err != nil {
				if errors.Is(err, ErrLeaseLost) || errors.Is(err, ErrJobNotFound) {
					leaseLost.Store(true)
					cancel()
					return
				}
				l.Log.Warn().Err(err).Str("run_id", runID).Msg("renew lease failed")
				continue
			}
			state, err := l.Client.Job(ctx, runID)
			if err != nil {
				if errors.Is(err, ErrJobNotFound) {
					leaseLost.Store(true)
					cancel()
					return
				}
				l.Log.Warn().Err(err).Str("run_id", runID).Msg("poll job state failed")
				continue
			}
			if state.CancelRequested {
				cancelled.Store(true)
				cancel()
				return
			}
		}
	}
}

func (l *Loop) reportCompleted(ctx context.Context, runID string, outcome Outcome) {
	l.retry(ctx, runID, func() error {
		return l.Client.Complete(ctx, runID, l.WorkerID, outcome.ResultStatus, outcome.ResultURI)
	})
}

func (l *Loop) reportFailed(ctx context.Context, runID, msg string) {
	if strings.TrimSpace(msg) == "" {
		msg = "worker reported failure without a message"
	}
	l.retry(ctx, runID, func() error {
		return l.Client.Fail(ctx, runID, l.WorkerID, msg)
	})
}

// retry 对上报做有限重试。租约丢失就放弃（别人已经接管）；其余错误重试三次
// 后只记日志——行会在租约过期后被重派，结论不会凭空消失。
func (l *Loop) retry(ctx context.Context, runID string, fn func() error) {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return
		}
		if err := fn(); err == nil {
			return
		} else {
			last = err
			if errors.Is(err, ErrLeaseLost) || errors.Is(err, ErrJobNotFound) {
				l.Log.Warn().Err(err).Str("run_id", runID).Msg("report abandoned: lease no longer ours")
				return
			}
		}
		if !sleepCtx(ctx, time.Duration(attempt+1)*200*time.Millisecond) {
			return
		}
	}
	l.Log.Error().Err(last).Str("run_id", runID).Msg("report failed after retries")
}

func (l *Loop) ackCancel(ctx context.Context, runID string) {
	if err := l.Client.AckCancel(ctx, runID, l.WorkerID); err != nil &&
		!errors.Is(err, ErrLeaseLost) && !errors.Is(err, ErrJobNotFound) {
		l.Log.Warn().Err(err).Str("run_id", runID).Msg("ack cancel failed")
	}
}

func (l *Loop) heartbeatLoop(ctx context.Context, errCh chan<- error) {
	t := time.NewTicker(l.Opts.HeartbeatEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := l.Client.Heartbeat(ctx, l.WorkerID); err != nil {
				if errors.Is(err, ErrWorkerNotFound) {
					errCh <- err
					return
				}
				l.Log.Warn().Err(err).Msg("heartbeat failed")
			}
		}
	}
}

// drain 在退出时摘除自己。用独立的短超时：调用方的 ctx 此时可能已取消。
func (l *Loop) drain() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := l.Client.Drain(ctx, l.WorkerID); err != nil &&
		!errors.Is(err, ErrWorkerNotFound) {
		l.Log.Warn().Err(err).Msg("drain on shutdown failed")
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
