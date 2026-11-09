package orchestrationworker

// 执行与结果落库：worker 只做三件事——经 EngineRunner 跑一条请求、把结果写进
// core 的 `backtest_jobs`、给出一个可回传给编排侧的结果指针。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"
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

// ArtifactPublisher 是 run 终态后的产物发布钩子（QT-78）。
//
// 产物缺失只影响分析面（消费侧 fail-closed），不改变 run 的完成结论；实现方
// 自身负责 fail-closed 与日志。
type ArtifactPublisher interface {
	Publish(ctx context.Context, resp *contracts.BacktestResponse) error
}

// Executor 用 core 的引擎执行 RunSpec。
type Executor struct {
	RunnerFor RunnerResolver
	Sink      ResultSink
	Timeout   time.Duration
	Publisher ArtifactPublisher
	Log       zerolog.Logger
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
	// 产物发布在结果落库之后：结果先可用，分析产物缺失只降级分析面。
	if e.Publisher != nil {
		if err := e.Publisher.Publish(ctx, resp); err != nil {
			e.Log.Warn().Str("run_id", resp.ID).Err(err).Msg("analysis artifact publish failed")
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

// ResultFaceStore 是 core 结果面（`run.backtest_results`）的最小写面。
//
// 分工（`SVC-ORCH-G3`）：任务面归 orchestration 的 `run.backtest_jobs`，
// 结果面归 core、单一写者是 worker。接口用原始类型表达，不把 storage 的
// 数据结构带进执行器契约。
type ResultFaceStore interface {
	SaveBacktestResult(ctx context.Context, runID, resultStatus string, result []byte) error
}

// ResultStoreSink 把 completed / invalid 的载荷写进 core 的结果面。
//
// 结果面（run.backtest_results）是唯一真相：worker 停写任务面遗留表
// backtest_jobs（存量行由 Migration 064 回填到结果面，读面仍可回退到旧行）。
type ResultStoreSink struct {
	Store ResultFaceStore
}

// Save 见 ResultSink。失败结论不落结果面时（failed 无载荷）由调用方走
// Outcome 失败腿，本 sink 只接受 completed / invalid。
func (s *ResultStoreSink) Save(ctx context.Context, resp *contracts.BacktestResponse) error {
	if s.Store == nil {
		return fmt.Errorf("orchestrationworker: no result face store configured")
	}
	if resp == nil {
		return fmt.Errorf("orchestrationworker: nil response")
	}
	if strings.TrimSpace(resp.ID) == "" {
		return fmt.Errorf("orchestrationworker: response has an empty id")
	}
	if resp.Status != contracts.StatusCompleted && resp.Status != contracts.StatusInvalid {
		return fmt.Errorf("orchestrationworker: result face only accepts completed/invalid, got %q", resp.Status)
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}
	if err := s.Store.SaveBacktestResult(ctx, resp.ID, resp.Status, raw); err != nil {
		return fmt.Errorf("write result face: %w", err)
	}
	return nil
}

// MultiSink 顺序执行多个 sink，任一失败即返回。
//
// 只用于过渡期双写（结果面 + 任务面遗留表），不是通用编排设施。
type MultiSink []ResultSink

// Save 见 ResultSink。
func (m MultiSink) Save(ctx context.Context, resp *contracts.BacktestResponse) error {
	for _, sink := range m {
		if sink == nil {
			continue
		}
		if err := sink.Save(ctx, resp); err != nil {
			return err
		}
	}
	return nil
}
