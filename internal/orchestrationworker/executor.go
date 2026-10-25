package orchestrationworker

// 执行与结果落库：worker 只做三件事——经 EngineRunner 跑一条请求、把结果写进
// core 的 `backtest_jobs`、给出一个可回传给编排侧的结果指针。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/engine"
)

// RunnerResolver 按内核名解析执行器（生产实现 = contracts.EngineRegistry.Resolve）。
type RunnerResolver func(kernel string) (contracts.EngineRunner, error)

// ResultSink 把一次成功执行的结果落进 core 的持久面。
//
// 它必须与 `GET /api/backtest/{id}` / provenance 的读面同源：结果落不下去
// 就不能把 run 报成 completed（analytics 会按 run_id 去读每个子结果）。
type ResultSink interface {
	Save(ctx context.Context, resp *contracts.BacktestResponse) error
}

// Outcome 是执行结论（编排侧要的三个值）。
type Outcome struct {
	ResultStatus string // completed / invalid / failed
	ResultURI    string // 结果指针；failed 时为空
	Error        string // failed 时的原因
}

// Executor 用 core 的引擎执行 RunSpec。
type Executor struct {
	RunnerFor RunnerResolver
	Sink      ResultSink
	Timeout   time.Duration
}

// Execute 执行一条请求。引擎错误、结果落库失败、未知状态一律折成 failed——
// worker 不把失败伪装成 completed（那会让父汇总与 analytics 都拿到假结论）。
//
// 返回 error 只表示「worker 自身没能完成这次执行」（如 context 取消），
// 与业务失败（Outcome.ResultStatus == failed）分开。
func (e *Executor) Execute(ctx context.Context, req contracts.BacktestRequest) (Outcome, error) {
	if e.RunnerFor == nil {
		return Outcome{}, fmt.Errorf("orchestrationworker: no runner resolver configured")
	}
	if e.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.Timeout)
		defer cancel()
	}

	runner, err := e.RunnerFor(req.EffectiveKernel())
	if err != nil {
		return Outcome{ResultStatus: string(contracts.StatusFailed), Error: fmt.Sprintf("kernel %q: %v", req.EffectiveKernel(), err)}, nil
	}
	resp, err := runner.RunBacktest(ctx, req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Outcome{}, ctxErr
		}
		return Outcome{ResultStatus: string(contracts.StatusFailed), Error: err.Error()}, nil
	}
	if resp == nil {
		return Outcome{ResultStatus: string(contracts.StatusFailed), Error: "engine returned no response"}, nil
	}
	if resp.Status == contracts.StatusFailed {
		msg := resp.Error
		if msg == "" {
			msg = "engine returned status=failed without an error message"
		}
		return Outcome{ResultStatus: string(contracts.StatusFailed), Error: msg}, nil
	}
	if resp.Status != contracts.StatusCompleted && resp.Status != contracts.StatusInvalid {
		return Outcome{
			ResultStatus: string(contracts.StatusFailed),
			Error:        fmt.Sprintf("engine returned unknown status %q", resp.Status),
		}, nil
	}
	if e.Sink != nil {
		if err := e.Sink.Save(ctx, resp); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return Outcome{}, ctxErr
			}
			return Outcome{
				ResultStatus: string(contracts.StatusFailed),
				Error:        fmt.Sprintf("persist result %s: %v", resp.ID, err),
			}, nil
		}
	}
	return Outcome{
		ResultStatus: resp.Status,
		ResultURI:    ResultURI(resp.ID),
	}, nil
}

// ResultURI 是回传给编排侧的结果指针。形状是 core 自己的（编排侧只存不解释）；
// 读方按 run_id 走 core 的 `/api/backtest/{id}`，不解析这个串。
func ResultURI(backtestID string) string {
	return "backtest:" + backtestID
}

// JobStore 是落结果需要的最小存储面（由 *storage.PostgresStore 满足）。
type JobStore interface {
	GetBacktestJob(ctx context.Context, jobID string) (map[string]any, error)
	CreateBacktestJob(ctx context.Context, job map[string]any) error
	UpdateJobStarted(ctx context.Context, jobID string) error
	UpdateJobCompleted(ctx context.Context, jobID, resultStatus string, result []byte) error
}

// JobStoreSink 把结果写成 core 的一条 `backtest_jobs` 行（id = run_id）。
//
// 重复执行（租约被回收后重跑）走 update 分支，不撞主键：结果内容会被本次
// 重跑覆盖——以最后一次成功执行为准，与 orchestration 的 attempts 语义一致。
type JobStoreSink struct {
	Store JobStore
}

// Save 见 ResultSink。先建行（不存在时）再写 completed，与旧同步路径的
// `SaveSyncResult` 同一形状。
func (s *JobStoreSink) Save(ctx context.Context, resp *contracts.BacktestResponse) error {
	if s.Store == nil {
		return fmt.Errorf("orchestrationworker: no job store configured")
	}
	if resp == nil {
		return fmt.Errorf("orchestrationworker: nil response")
	}
	if strings.TrimSpace(resp.ID) == "" {
		return fmt.Errorf("orchestrationworker: response has an empty id")
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}
	existing, err := s.Store.GetBacktestJob(ctx, resp.ID)
	if err != nil {
		return fmt.Errorf("look up result row: %w", err)
	}
	if existing == nil {
		job := map[string]any{
			"id":          resp.ID,
			"strategy_id": resp.Strategy,
			"params":      []byte("{}"),
			"universe":    strings.Join(resp.StockPool, ","),
			"start_date":  resp.StartDate,
			"end_date":    resp.EndDate,
			"status":      "pending",
		}
		if err := s.Store.CreateBacktestJob(ctx, job); err != nil {
			return fmt.Errorf("create result row: %w", err)
		}
		if err := s.Store.UpdateJobStarted(ctx, resp.ID); err != nil {
			return fmt.Errorf("mark result row started: %w", err)
		}
	}
	resultStatus := resp.Status
	if resultStatus != contracts.StatusCompleted && resultStatus != contracts.StatusInvalid {
		resultStatus = contracts.StatusFailed
	}
	if err := s.Store.UpdateJobCompleted(ctx, resp.ID, resultStatus, raw); err != nil {
		return fmt.Errorf("write result row: %w", err)
	}
	return nil
}
