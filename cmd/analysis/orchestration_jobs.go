package main

// SVC-ORCH-G3（core 侧）：异步任务面在编排服务，结论面在 core。
//
// 分工（docs/design/qt-decoupling/data-service-contract-and-ownership.md:41）：
//   - 任务面：编排服务 run.backtest_jobs（创建 / 状态 / 取消）
//   - 结果面：core run.backtest_results（单一写者 = worker）
//
// 收尾腿（2026-11-09）：core 的 quant.backtest_jobs 整表退役，本文件不再有
// 「存量行」读源，也不再有写面转发。JobStore 只剩两条读源：
//
//  1. 结果面（已完成 / 无效 / 失败的结论与载荷）；
//  2. 任务面（pending / running 的状态，以及 run_spec 里的策略元数据）。
//
// 未配置 ORCHESTRATION_URL 时本文件不生效（tasks == nil）：异步创建入口
// 返回 503（任务面没接就没有第二真相可写），读面仍可用结果面。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-contracts/engine"
	"tellurion/internal/orchestrationworker"
	"tellurion/pkg/storage"
)

// newOrchestrationTaskClient 按环境变量构造编排任务面客户端。
//
// 未配置 ORCHESTRATION_URL = 本部署没接编排，返回 nil：异步创建入口 503
// （core 已没有本地队列可回退）。凭证与 cmd/worker 同名同义：
// ORCHESTRATION_SERVICE_TOKEN 为空则不带 Authorization（本地回环 +
// open-access 部署的形态）。
func newOrchestrationTaskClient(logger zerolog.Logger) *orchestrationworker.Client {
	url := strings.TrimSpace(os.Getenv("ORCHESTRATION_URL"))
	if url == "" {
		logger.Warn().Msg("ORCHESTRATION_URL not set — async backtest creation is disabled (503); core no longer has a local queue (SVC-ORCH-G3)")
		return nil
	}
	client := orchestrationworker.NewClient(url, nil)
	client.Token = strings.TrimSpace(os.Getenv("ORCHESTRATION_SERVICE_TOKEN"))
	logger.Info().
		Str("url", url).
		Bool("authenticated", client.Token != "").
		Msg("orchestration task client configured (async entry → run service)")
	return client
}

// backtestResultReader 是结果面的只读口（*storage.PostgresStore 满足）。
type backtestResultReader interface {
	GetBacktestResult(ctx context.Context, runID string) (*storage.BacktestResultRecord, error)
	ListBacktestResults(ctx context.Context, limit int) ([]*storage.BacktestResultRecord, error)
}

// analysisResultFace 把 *storage.PostgresStore 适配成 backtest.ResultFaceWriter
// （SVC-ORCH-G3）：同步回测的结论写 run.backtest_results。
type analysisResultFace struct {
	store *storage.PostgresStore
}

func (a analysisResultFace) SaveBacktestResult(ctx context.Context, runID, resultStatus string, result []byte) error {
	rec := &storage.BacktestResultRecord{RunID: runID, ResultStatus: resultStatus}
	if len(result) > 0 {
		rec.Result = json.RawMessage(result)
	}
	_, err := a.store.SaveBacktestResult(ctx, rec)
	return err
}

// orchestrationJobStore 把「结果面 + 任务面」拼成 JobService 需要的 JobStore。
type orchestrationJobStore struct {
	results backtestResultReader
	tasks   *orchestrationworker.Client
	logger  zerolog.Logger
}

func newOrchestrationJobStore(results backtestResultReader, tasks *orchestrationworker.Client, logger zerolog.Logger) *orchestrationJobStore {
	return &orchestrationJobStore{
		results: results,
		tasks:   tasks,
		logger:  logger.With().Str("component", "orchestration_job_store").Logger(),
	}
}

// GetBacktestJob 按「结果面 → 任务面」的顺序解析一条作业。
//
// 已完成的作业命中结果面（core 的真相）；pending / running 只存在于任务面。
func (s *orchestrationJobStore) GetBacktestJob(ctx context.Context, jobID string) (map[string]any, error) {
	rec, err := s.results.GetBacktestResult(ctx, jobID)
	if err != nil && !errors.Is(err, storage.ErrBacktestResultNotFound) {
		return nil, err
	}
	if rec != nil {
		return jobMapFromResult(rec), nil
	}

	if s.tasks == nil {
		return nil, nil
	}
	state, err := s.tasks.Job(ctx, jobID)
	if err != nil {
		if isOrchestrationNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("orchestration job %s: %w", jobID, err)
	}
	return jobMapFromTaskState(state), nil
}

// ListBacktestJobs 合并结果面行与任务面列表（按 id 去重、created_at 降序）。
// 任一源不可用时只降级该源，不把整页打成 500。
func (s *orchestrationJobStore) ListBacktestJobs(ctx context.Context, limit int) ([]map[string]any, error) {
	out := make([]map[string]any, 0, limit)
	seen := make(map[string]bool, limit)

	// 结果面：同步回测与已完成的异步回测没有任务行，只能从这里补进列表。
	results, err := s.results.ListBacktestResults(ctx, limit)
	if err != nil {
		s.logger.Warn().Err(err).Msg("result face list unavailable; serving task-face rows only")
	} else {
		for _, rec := range results {
			if seen[rec.RunID] {
				continue
			}
			seen[rec.RunID] = true
			out = append(out, jobMapFromResult(rec))
		}
	}

	if s.tasks != nil {
		// include_spec=1：pending / running 的策略元数据只在任务面的
		// run_spec 里（结果面要跑完才有载荷）。
		page, err := s.tasks.List(ctx, "", "", limit, true)
		if err != nil {
			s.logger.Warn().Err(err).Msg("orchestration list unavailable; serving result-face rows only")
		} else {
			for _, view := range page.Jobs {
				if seen[view.JobID] {
					continue
				}
				seen[view.JobID] = true
				out = append(out, jobMapFromTaskView(view))
			}
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		return mapCreatedAt(out[i]).After(mapCreatedAt(out[j]))
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ─── 映射 ──────────────────────────────────────────────────────────────

// jobMapFromResult 把结果面一行翻成 JobStore 的 map 形状。
//
// 结果面只存 completed / invalid / failed：前两者在作业面都是 status=completed
// （result_status 区分三值），failed 是 status=failed。
func jobMapFromResult(rec *storage.BacktestResultRecord) map[string]any {
	var payload struct {
		Strategy  string   `json:"strategy"`
		StartDate string   `json:"start_date"`
		EndDate   string   `json:"end_date"`
		StockPool []string `json:"stock_pool"`
	}
	_ = json.Unmarshal(rec.Result, &payload)

	status := "completed"
	if rec.ResultStatus == "failed" {
		status = "failed"
	}
	return map[string]any{
		"id":            rec.RunID,
		"strategy_id":   payload.Strategy,
		"universe":      strings.Join(payload.StockPool, ","),
		"start_date":    payload.StartDate,
		"end_date":      payload.EndDate,
		"status":        status,
		"result_status": rec.ResultStatus,
		"result":        []byte(rec.Result),
		"error_msg":     rec.ErrorMessage,
		"created_at":    rec.CreatedAt,
		"completed_at":  rec.UpdatedAt,
	}
}

// runSpecMeta 是任务面 run_spec 里作业列表需要的策略元数据。
type runSpecMeta struct {
	Strategy  string   `json:"strategy"`
	StockPool []string `json:"stock_pool"`
	StartDate string   `json:"start_date"`
	EndDate   string   `json:"end_date"`
}

// applyRunSpecMeta 从 canonical RunSpec 补策略元数据到作业行。
// 未带 spec（列表未请求 include_spec）或 spec 不合形时保持空值，不报错。
func applyRunSpecMeta(m map[string]any, spec json.RawMessage) {
	if len(spec) == 0 {
		return
	}
	var meta runSpecMeta
	if err := json.Unmarshal(spec, &meta); err != nil {
		return
	}
	m["strategy_id"] = meta.Strategy
	m["universe"] = strings.Join(meta.StockPool, ",")
	m["start_date"] = meta.StartDate
	m["end_date"] = meta.EndDate
}

// jobMapFromTaskState 把任务面的最小视图翻成 JobStore 的 map 形状。
func jobMapFromTaskState(state orchestrationworker.JobState) map[string]any {
	m := map[string]any{
		"id":        state.JobID,
		"status":    state.Status,
		"error_msg": state.Error,
	}
	if state.ResultStatus != nil {
		m["result_status"] = *state.ResultStatus
	}
	applyRunSpecMeta(m, state.RunSpec)
	return m
}

// jobMapFromTaskView 把任务面列表视图翻成 JobStore 的 map 形状。
func jobMapFromTaskView(view orchestrationworker.JobView) map[string]any {
	m := map[string]any{
		"id":        view.JobID,
		"status":    view.Status,
		"error_msg": view.Error,
	}
	if view.ResultStatus != nil {
		m["result_status"] = *view.ResultStatus
	}
	if view.CreatedAt != nil {
		m["created_at"] = *view.CreatedAt
	}
	if view.StartedAt != nil {
		m["started_at"] = *view.StartedAt
	}
	if view.CompletedAt != nil {
		m["completed_at"] = *view.CompletedAt
	}
	applyRunSpecMeta(m, view.RunSpec)
	return m
}

func mapCreatedAt(m map[string]any) time.Time {
	if t, ok := m["created_at"].(time.Time); ok {
		return t
	}
	return time.Time{}
}

func isOrchestrationNotFound(err error) bool {
	var apiErr *orchestrationworker.APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == 404
}

// ─── 异步入口 → run_spec ───────────────────────────────────────────────

// buildAsyncRunSpec 把异步入口的请求翻成编排的 run_spec。
//
// 只翻执行路径真正消费的字段（strategy / stock_pool / 区间 / 初始资金 /
// kernel / engine_params）：多翻字段会让同一条请求在不同版本跑出不同结果。
func buildAsyncRunSpec(req asyncBacktestRequest, marketRouter *contracts.MarketRouter) ([]byte, error) {
	kernel := req.Kernel
	if req.Market != "" {
		resolved, err := contracts.ResolveRunTarget(marketRouter, req.Market, req.Kernel)
		if err != nil {
			return nil, err
		}
		kernel = resolved
	}

	spec := map[string]any{
		"strategy":   req.StrategyID,
		"stock_pool": specStockPool(req.Universe),
		"start_date": req.StartDate,
		"end_date":   req.EndDate,
	}
	if kernel != "" {
		spec["kernel"] = kernel
	}
	if req.InitialCapital > 0 {
		spec["initial_capital"] = req.InitialCapital
	}
	engineParams := req.EngineParams
	if len(engineParams) == 0 {
		if v, ok := req.Params["engine_params"].(map[string]any); ok {
			engineParams = v
		}
	}
	if len(engineParams) > 0 {
		spec["engine_params"] = engineParams
	}
	return json.Marshal(spec)
}

// specStockPool 与 worker 的 parseUniverse 同义（"all" / "universe:"
// 前缀 / 逗号分隔），保证创建期与执行期的标的集合一致。
func specStockPool(universe string) []string {
	if universe == "" || universe == "all" {
		return nil
	}
	universe = strings.TrimPrefix(universe, "universe:")
	parts := strings.Split(universe, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
