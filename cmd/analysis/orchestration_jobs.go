package main

// SVC-ORCH-G3（core 侧）：异步任务面切到编排服务，结果面留在 core。
//
// 分工（docs/design/qt-decoupling/data-service-contract-and-ownership.md:41）：
//   - 任务面：编排服务 run.backtest_jobs（创建 / 状态 / 取消）
//   - 结果面：core run.backtest_results（单一写者 = worker）
//
// 本文件做两件事：
//  1. 把 core analysis 的读面拼成一个 JobStore：先存量 backtest_jobs（过渡期），
//     再结果面，最后任务面 —— 三条读源都在，切读期间新旧作业同时可读；
//  2. 把异步入口的 CreateJobRequest 翻成编排的 run_spec。
//
// 未配置 ORCHESTRATION_URL 时本文件不生效（tasks == nil），异步入口维持本地
// 旧路径；这是「部署还没接编排」的显式形态，不是静默回退。

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
	"tellurion/pkg/backtest"
	"tellurion/pkg/storage"
)

// newOrchestrationTaskClient 按环境变量构造编排任务面客户端。
//
// 未配置 ORCHESTRATION_URL = 本部署还没接编排，返回 nil（异步入口维持本地
// 旧路径）。凭证与 cmd/worker 同名同义：ORCHESTRATION_SERVICE_TOKEN 为空则
// 不带 Authorization（本地回环 + open-access 部署的形态）。
func newOrchestrationTaskClient(logger zerolog.Logger) *orchestrationworker.Client {
	url := strings.TrimSpace(os.Getenv("ORCHESTRATION_URL"))
	if url == "" {
		logger.Warn().Msg("ORCHESTRATION_URL not set — async backtests stay on the local job path (SVC-ORCH-G3 pending deploy)")
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
// （SVC-ORCH-G3）：同步回测的结论写 run.backtest_results，不再插
// backtest_jobs。接口用原始类型表达，故此处做一次薄转换。
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

// orchestrationJobStore 把「任务面 + 结果面 + 存量行」拼成 JobService 需要的 JobStore。
//
// 写方法（Create/Update/Delete）原样落到存量表：本类型只在过渡期存在，写面
// 退役由 SVC-CORE-RUN-RETIRE 的删本地队列腿收口。
type orchestrationJobStore struct {
	legacy  backtest.JobStore
	results backtestResultReader
	tasks   *orchestrationworker.Client
	logger  zerolog.Logger
}

func newOrchestrationJobStore(legacy backtest.JobStore, results backtestResultReader, tasks *orchestrationworker.Client, logger zerolog.Logger) *orchestrationJobStore {
	return &orchestrationJobStore{
		legacy:  legacy,
		results: results,
		tasks:   tasks,
		logger:  logger.With().Str("component", "orchestration_job_store").Logger(),
	}
}

// ─── 写面：过渡期仍走存量表 ─────────────────────────────────────────────

func (s *orchestrationJobStore) CreateBacktestJob(ctx context.Context, job map[string]any) error {
	return s.legacy.CreateBacktestJob(ctx, job)
}

func (s *orchestrationJobStore) UpdateJobStarted(ctx context.Context, jobID string) error {
	return s.legacy.UpdateJobStarted(ctx, jobID)
}

func (s *orchestrationJobStore) UpdateJobCompleted(ctx context.Context, jobID, resultStatus string, result []byte) error {
	return s.legacy.UpdateJobCompleted(ctx, jobID, resultStatus, result)
}

func (s *orchestrationJobStore) UpdateJobFailed(ctx context.Context, jobID string, errMsg string) error {
	return s.legacy.UpdateJobFailed(ctx, jobID, errMsg)
}

func (s *orchestrationJobStore) DeleteBacktestJob(ctx context.Context, jobID string) error {
	return s.legacy.DeleteBacktestJob(ctx, jobID)
}

// ─── 读面：存量 → 结果面 → 任务面 ───────────────────────────────────────

// GetBacktestJob 按「存量行 → 结果面 → 任务面」的顺序解析一条作业。
//
// 已完成的作业命中结果面（core 的真相）；pending / running 只存在于任务面。
func (s *orchestrationJobStore) GetBacktestJob(ctx context.Context, jobID string) (map[string]any, error) {
	legacy, err := s.legacy.GetBacktestJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if legacy != nil {
		return legacy, nil
	}

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

// ListBacktestJobs 合并存量行、结果面行与任务面列表（按 id 去重、created_at
// 降序）。任务面 / 结果面任一不可用时只降级该源，不把整页打成 500。
func (s *orchestrationJobStore) ListBacktestJobs(ctx context.Context, limit int) ([]map[string]any, error) {
	legacy, err := s.legacy.ListBacktestJobs(ctx, limit)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool, len(legacy)+limit)
	out := make([]map[string]any, 0, len(legacy)+limit)
	for _, m := range legacy {
		if id, ok := m["id"].(string); ok {
			seen[id] = true
		}
		out = append(out, m)
	}

	// 结果面：同步回测与已迁移的存量没有任务行，只能从这里补进列表。
	results, err := s.results.ListBacktestResults(ctx, limit)
	if err != nil {
		s.logger.Warn().Err(err).Msg("result face list unavailable; serving legacy rows only")
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
		page, err := s.tasks.List(ctx, "", "", limit)
		if err != nil {
			s.logger.Warn().Err(err).Msg("orchestration list unavailable; serving local rows only")
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

// jobMapFromTaskState 把任务面的最小视图翻成 JobStore 的 map 形状。
func jobMapFromTaskState(state orchestrationworker.JobState) map[string]any {
	m := map[string]any{
		"id":          state.JobID,
		"strategy_id": "",
		"universe":    "",
		"start_date":  "",
		"end_date":    "",
		"status":      state.Status,
		"error_msg":   state.Error,
	}
	if state.ResultStatus != nil {
		m["result_status"] = *state.ResultStatus
	}
	return m
}

// jobMapFromTaskView 把任务面列表视图翻成 JobStore 的 map 形状。
func jobMapFromTaskView(view orchestrationworker.JobView) map[string]any {
	m := map[string]any{
		"id":          view.JobID,
		"strategy_id": "",
		"universe":    "",
		"start_date":  "",
		"end_date":    "",
		"status":      view.Status,
		"error_msg":   view.Error,
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

// buildAsyncRunSpec 把异步入口的 CreateJobRequest 翻成编排的 run_spec。
//
// 只翻本地执行路径真正消费的字段（strategy / stock_pool / 区间 / 初始资金 /
// kernel / engine_params）：多翻字段会让同一条请求在新旧路径跑出不同结果。
func buildAsyncRunSpec(req backtest.CreateJobRequest, marketRouter *contracts.MarketRouter) ([]byte, error) {
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

// specStockPool 与 pkg/backtest/job 的 parseUniverse 同义（"all" / "universe:"
// 前缀 / 逗号分隔），保证新旧路径的标的集合一致。
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
