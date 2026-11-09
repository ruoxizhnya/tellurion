package job

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-contracts/engine"
)

// JobService 是 core 侧作业的**只读面 + 同步结果写面**。
//
// SVC-ORCH-G3 收尾腿（2026-11-09）：本地异步队列（CreateJob / StartJob /
// CancelJob / Shutdown / CleanupStaleRunning）整条退役——异步任务的生命周期
// 归编排服务的 run.backtest_jobs 所有，core 不再持有第二条任务真相。本类型
// 只剩两件事：
//   - 读：GET /api/backtest/{id} 与作业列表（数据源由装配方的 JobStore 决定：
//     结果面 + 任务面，不再有本地队列表）；
//   - 写：同步回测的结论写 core 结果面 run.backtest_results（SaveSyncResult）。
type JobService struct {
	store JobStore
	// resultFace 是 core 结果面 run.backtest_results 的最小写口。必需依赖：
	// 为空时 SaveSyncResult fail-closed（不再回退到已退役的 backtest_jobs）。
	resultFace ResultFaceWriter
	logger     zerolog.Logger
}

// JobStore 是读面需要的最小存储子集（SVC-ORCH-G3 收尾腿后只剩读）。
// Uses plain types to avoid import cycles with pkg/storage.
type JobStore interface {
	GetBacktestJob(ctx context.Context, jobID string) (map[string]any, error)
	ListBacktestJobs(ctx context.Context, limit int) ([]map[string]any, error)
}

// JobRecord holds the raw fields of a backtest job as stored in the DB.
// The Params and Result fields are stored as JSON bytes.
type JobRecord struct {
	ID           string
	StrategyID   string
	Params       []byte // JSONB
	Universe     string
	StartDate    string
	EndDate      string
	Status       string
	ResultStatus string // K9 三值：completed / invalid / failed；pending/running 为空
	Result       []byte // JSONB
	ErrorMsg     string
	CreatedAt    time.Time
	StartedAt    *time.Time
	CompletedAt  *time.Time
}

// mapToJobRecord converts a map back to JobRecord.
func mapToJobRecord(m map[string]any) *JobRecord {
	j := &JobRecord{
		ID:         m["id"].(string),
		StrategyID: m["strategy_id"].(string),
		Universe:   m["universe"].(string),
		StartDate:  m["start_date"].(string),
		EndDate:    m["end_date"].(string),
		Status:     m["status"].(string),
	}
	if v, ok := m["params"].([]byte); ok {
		j.Params = v
	}
	if v, ok := m["result"].([]byte); ok {
		j.Result = v
	}
	if v, ok := m["result_status"].(string); ok {
		j.ResultStatus = v
	}
	if v, ok := m["error_msg"].(string); ok {
		j.ErrorMsg = v
	}
	if v, ok := m["created_at"].(time.Time); ok {
		j.CreatedAt = v
	}
	if v, ok := m["started_at"].(time.Time); ok {
		j.StartedAt = &v
	}
	if v, ok := m["completed_at"].(time.Time); ok {
		j.CompletedAt = &v
	}
	return j
}

// NewJobService creates a new JobService.
//
// 读面 store 必需；结果面经 WithResultFace 注入（装配方必须注入，否则
// SaveSyncResult 拒绝写入）。
func NewJobService(store JobStore, logger zerolog.Logger, opts ...JobServiceOption) *JobService {
	if store == nil {
		panic("job: NewJobService requires a non-nil JobStore")
	}
	s := &JobService{
		store:  store,
		logger: logger.With().Str("component", "job_service").Logger(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

// JobServiceOption 配置 JobService 的可选依赖。
type JobServiceOption func(*JobService)

// ResultFaceWriter 是 core 结果面（run.backtest_results）的最小写口
// （SVC-ORCH-G3）。接口用原始类型表达，job 包不依赖 storage 的数据结构。
type ResultFaceWriter interface {
	SaveBacktestResult(ctx context.Context, runID, resultStatus string, result []byte) error
}

// WithResultFace 注入 core 结果面写口（同步回测结论的唯一归属）。
func WithResultFace(w ResultFaceWriter) JobServiceOption {
	return func(s *JobService) {
		s.resultFace = w
	}
}

// Job is the public API response for a job.
type Job struct {
	ID           string          `json:"id"`
	StrategyID   string          `json:"strategy_id"`
	Params       json.RawMessage `json:"params"`
	Universe     string          `json:"universe"`
	StartDate    string          `json:"start_date"`
	EndDate      string          `json:"end_date"`
	Status       string          `json:"status"`
	ResultStatus string          `json:"result_status,omitempty"`
	Result       json.RawMessage `json:"result,omitempty"`
	Error        string          `json:"error,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	StartedAt    *time.Time      `json:"started_at,omitempty"`
	CompletedAt  *time.Time      `json:"completed_at,omitempty"`
}

// GetJob retrieves the current state of a job.
func (s *JobService) GetJob(ctx context.Context, jobID string) (*Job, error) {
	recordMap, err := s.store.GetBacktestJob(ctx, jobID)
	if err != nil {
		return nil, fmt.Errorf("failed to get job: %w", err)
	}
	if recordMap == nil {
		return nil, nil
	}
	return recordToJob(mapToJobRecord(recordMap)), nil
}

// ListJobs returns the most recent jobs.
func (s *JobService) ListJobs(ctx context.Context, limit int) ([]*Job, error) {
	if limit <= 0 {
		limit = 20
	}
	records, err := s.store.ListBacktestJobs(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list jobs: %w", err)
	}
	jobs := make([]*Job, len(records))
	for i, r := range records {
		jobs[i] = recordToJob(mapToJobRecord(r))
	}
	return jobs, nil
}

// SaveSyncResult persists a completed synchronous backtest result.
//
// 同步回测没有任务生命周期，只有结论：写 core 结果面 run.backtest_results
// （SVC-ORCH-G3）。结果面未装配 ⇒ fail-closed 报错，**不**回退到已退役的
// 本地队列表——静默回退会让「结果写到了哪里」变成部署偶然。
func (s *JobService) SaveSyncResult(ctx context.Context, resp *contracts.BacktestResponse) error {
	if resp == nil {
		return fmt.Errorf("nil backtest result")
	}
	if s.resultFace == nil {
		return fmt.Errorf("job: result face not configured (WithResultFace); refusing to persist a sync result")
	}
	if resp.ID == "" {
		return fmt.Errorf("backtest result has an empty id")
	}
	resultJSON, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("failed to marshal result: %w", err)
	}
	if err := s.resultFace.SaveBacktestResult(ctx, resp.ID, resultStatusOf(resp.Status), resultJSON); err != nil {
		return fmt.Errorf("failed to persist result face: %w", err)
	}
	return nil
}

// recordToJob converts an internal JobRecord to a public Job.
func recordToJob(r *JobRecord) *Job {
	return &Job{
		ID:           r.ID,
		StrategyID:   r.StrategyID,
		Params:       r.Params,
		Universe:     r.Universe,
		StartDate:    r.StartDate,
		EndDate:      r.EndDate,
		Status:       r.Status,
		ResultStatus: r.ResultStatus,
		Result:       r.Result,
		Error:        r.ErrorMsg,
		CreatedAt:    r.CreatedAt,
		StartedAt:    r.StartedAt,
		CompletedAt:  r.CompletedAt,
	}
}

// resultStatusOf 把 ResultSpec.Status 映射成结果行上的 K9 result_status。
// 只有 completed / invalid 会走到这里（失败行由调用方以 failed 直接写）。
// 未知/空值按 completed 处理：它们是「跑完且有结果体」的调用方契约，不能因为
// 内核少填一个字段就把有效结果读成失败。
func resultStatusOf(respStatus string) string {
	if respStatus == contracts.StatusInvalid {
		return contracts.StatusInvalid
	}
	return contracts.StatusCompleted
}
