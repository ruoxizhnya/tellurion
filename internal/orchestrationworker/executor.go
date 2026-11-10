package orchestrationworker

// 执行与结果落库：worker 只做三件事——经 EngineRunner 跑一条请求、把结果写进
// core 的结果面 `run.backtest_results`、给出一个可回传给编排侧的结果指针。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-contracts/engine"

	"tellurion/pkg/backtest"
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
	// Face 是结果面的直接写口，只用于**失败留痕**（Sink 只接
	// completed / invalid 的载荷）。见 recordFailure。
	Face ResultFaceStore
	// StrategyGate 在真正执行前校验 strategy_ref（QT-32 K11）：非空引用必须
	// 过资产服务的 promoted + 审批留痕闸门。返回的 identity 放进 ctx，供引擎
	// 的 provenance 记录资产服务签发的两个 hash。nil + 非空引用 = 拒绝
	// （fail-closed）；内置策略路径不解析、不联网。
	StrategyGate func(ctx context.Context, rawRef string) (*backtest.StrategyAssetIdentity, error)
	Log          zerolog.Logger
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

	// QT-32 K11：非空 strategy_ref 必须先过资产服务的 promote 闸门。拒绝
	// 折成 failed（与内核未注册同一条路），不进入引擎、不落结果。
	if strings.TrimSpace(req.StrategyRef) != "" {
		if e.StrategyGate == nil {
			msg := "strategy_ref requires the strategy asset gate, but this worker has none configured"
			e.recordFailure(ctx, req.RunID, msg)
			return Outcome{ResultStatus: string(contracts.StatusFailed), Error: msg}, nil
		}
		identity, gateErr := e.StrategyGate(ctx, req.StrategyRef)
		if gateErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return Outcome{}, ctxErr
			}
			msg := "strategy asset gate: " + gateErr.Error()
			e.recordFailure(ctx, req.RunID, msg)
			return Outcome{ResultStatus: string(contracts.StatusFailed), Error: msg}, nil
		}
		if identity != nil {
			e.Log.Info().
				Str("run_id", req.RunID).
				Str("strategy_id", identity.StrategyID).
				Str("version_hash", identity.VersionHash).
				Str("artifact_hash", identity.ArtifactHash).
				Msg("strategy_ref passed the asset promote gate")
			ctx = backtest.WithStrategyAssetIdentity(ctx, *identity)
		}
	}

	runner, err := e.RunnerFor(req.EffectiveKernel())
	if err != nil {
		msg := fmt.Sprintf("kernel %q: %v", req.EffectiveKernel(), err)
		e.recordFailure(ctx, req.RunID, msg)
		return Outcome{ResultStatus: string(contracts.StatusFailed), Error: msg}, nil
	}
	resp, err := runner.RunBacktest(ctx, req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Outcome{}, ctxErr
		}
		e.recordFailure(ctx, req.RunID, err.Error())
		return Outcome{ResultStatus: string(contracts.StatusFailed), Error: err.Error()}, nil
	}
	if resp == nil {
		e.recordFailure(ctx, req.RunID, "engine returned no response")
		return Outcome{ResultStatus: string(contracts.StatusFailed), Error: "engine returned no response"}, nil
	}
	if resp.Status == contracts.StatusFailed {
		msg := resp.Error
		if msg == "" {
			msg = "engine returned status=failed without an error message"
		}
		e.recordFailure(ctx, resp.ID, msg)
		return Outcome{ResultStatus: string(contracts.StatusFailed), Error: msg}, nil
	}
	if resp.Status != contracts.StatusCompleted && resp.Status != contracts.StatusInvalid {
		msg := fmt.Sprintf("engine returned unknown status %q", resp.Status)
		e.recordFailure(ctx, resp.ID, msg)
		return Outcome{ResultStatus: string(contracts.StatusFailed), Error: msg}, nil
	}
	if e.Sink != nil {
		if err := e.Sink.Save(ctx, resp); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return Outcome{}, ctxErr
			}
			msg := fmt.Sprintf("persist result %s: %v", resp.ID, err)
			e.recordFailure(ctx, resp.ID, msg)
			return Outcome{ResultStatus: string(contracts.StatusFailed), Error: msg}, nil
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

// recordFailure 把业务失败写进结果面的 failed 行（无载荷，error_message 由
// 结果面记录；核心结论轴只区分三值，不存失败原因全文）。
//
// 为什么失败也要留痕：core 的 provenance 三态判据是结果面的 result_status
// ——「无结果行」= 未收尾（409），「failed」= 跑过但没留痕（404）。不写这一行，
// 失败的 run 在 core 侧会永远看起来还在跑。
//
// 不写的两种情况：Face 未装配（装配错误，不制造第二条真相）、runID 为空
// （无法归因）。worker 自身的 abort（ctx 取消 / 租约丢失）**不调用**本函数：
// 那种 run 会由编排侧重排，写 failed 会把可重试的中断钉成终态。
func (e *Executor) recordFailure(ctx context.Context, runID, msg string) {
	if e.Face == nil || strings.TrimSpace(runID) == "" {
		return
	}
	if err := e.Face.SaveBacktestResult(ctx, runID, string(contracts.StatusFailed), nil); err != nil {
		e.Log.Warn().Str("run_id", runID).Err(err).
			Msg("failed result row not written; this run's provenance will read as not-ready")
	}
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
// 结果面（run.backtest_results）是唯一真相：core 的任务面遗留表
// backtest_jobs 已退役（Migration 065 DROP TABLE），worker 也不再有第二条
// 写路径。失败结论走 Executor.recordFailure（无载荷的 failed 行）。
type ResultStoreSink struct {
	Store ResultFaceStore
}

// Save 见 ResultSink。本 sink 只接受 completed / invalid（failed 无载荷，
// 由 Executor.recordFailure 直写）。
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
