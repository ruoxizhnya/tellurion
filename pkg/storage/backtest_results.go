package storage

// SVC-ORCH-G3 · run.backtest_results 的读写。
//
// 分工（docs/design/qt-decoupling/data-service-contract-and-ownership.md §表）：
//   - 任务面（pending / running / cancelled、租约、优先级）归 orchestration 的
//     `run.backtest_jobs`；
//   - 结果面（回测结论与载荷）归 core 的 `run.backtest_results`，**单一写者 = core**，
//     orchestration 只经 API 拿 `result_uri` 指针。
//
// 因此本表只存「结论轴」`result_status`（K9 三值）与结果载荷，不存队列生命周期
// 状态。run_id 与 orchestration 的 job_id 同值，两侧靠它对齐。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrBacktestResultNotFound 表示该 run 尚无结果行（消费侧应 fail-closed）。
var ErrBacktestResultNotFound = errors.New("storage: backtest result not found")

// BacktestResultRecord 是 run.backtest_results 的一行。
type BacktestResultRecord struct {
	RunID        string          `json:"run_id"`
	ResultStatus string          `json:"result_status"`
	Result       json.RawMessage `json:"result,omitempty"`
	ErrorMessage string          `json:"error_message,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

// BacktestResultsSchemaDDL 建表 DDL —— migrate() 的 Migration 063 执行的就是这组语句。
var BacktestResultsSchemaDDL = []string{
	`CREATE SCHEMA IF NOT EXISTS run`,
	`CREATE TABLE IF NOT EXISTS run.backtest_results (
		run_id        VARCHAR(64)  PRIMARY KEY,
		result_status VARCHAR(20)  NOT NULL,
		result        JSONB,
		error_message TEXT,
		created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
		updated_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
		CONSTRAINT backtest_results_result_status_check
			CHECK (result_status IN ('completed','invalid','failed'))
	)`,
	`CREATE INDEX IF NOT EXISTS idx_backtest_results_result_status
		ON run.backtest_results(result_status)`,
}

// BacktestResultsBackfillSQL 把存量 `backtest_jobs` 的终态行搬进结果面
// （migrate() 的 Migration 064）。只搬结论已定的行，输入幂等：
// ON CONFLICT DO NOTHING 让结果面已有的新写不被旧行覆盖。
//
// 迁移只追加、不改写 009 / 059；旧表保留到「删本地队列」腿，读面仍先看旧行。
var BacktestResultsBackfillSQL = []string{
	// completed / invalid：状态轴 completed，结论轴沿用 K9 值，载荷必须有。
	`INSERT INTO run.backtest_results (run_id, result_status, result, error_message, created_at, updated_at)
	 SELECT id, result_status, result, COALESCE(error_message, ''),
	        COALESCE(created_at, now()), COALESCE(completed_at, created_at, now())
	 FROM backtest_jobs
	 WHERE status = 'completed'
	   AND result_status IN ('completed', 'invalid')
	   AND result IS NOT NULL
	 ON CONFLICT (run_id) DO NOTHING`,
	// failed：无结果载荷，error_message 是唯一证据。
	`INSERT INTO run.backtest_results (run_id, result_status, result, error_message, created_at, updated_at)
	 SELECT id, 'failed', NULL, COALESCE(error_message, ''),
	        COALESCE(created_at, now()), COALESCE(completed_at, created_at, now())
	 FROM backtest_jobs
	 WHERE status = 'failed'
	   AND result_status = 'failed'
	 ON CONFLICT (run_id) DO NOTHING`,
}

// validBacktestResultStatus 是 K9 三值：completed / invalid / failed。
func validBacktestResultStatus(status string) bool {
	switch status {
	case "completed", "invalid", "failed":
		return true
	default:
		return false
	}
}

// SaveBacktestResult 写入 / 覆盖一条结果行。
//
// 语义：同 run_id 重写即覆盖结论文与载荷（worker 重试或重跑），`created_at`
// 保留首次写入时间，`updated_at` 刷新。`completed` / `invalid` 必须带载荷；
// `failed` 允许载荷为空并携带 error_message。
func (s *PostgresStore) SaveBacktestResult(ctx context.Context, rec *BacktestResultRecord) (*BacktestResultRecord, error) {
	if rec == nil {
		return nil, fmt.Errorf("SaveBacktestResult: nil record")
	}
	if rec.RunID == "" {
		return nil, fmt.Errorf("SaveBacktestResult: run_id is required")
	}
	if !validBacktestResultStatus(rec.ResultStatus) {
		return nil, fmt.Errorf("SaveBacktestResult: invalid result_status %q", rec.ResultStatus)
	}
	var payload any
	if len(rec.Result) > 0 {
		if !json.Valid(rec.Result) {
			return nil, fmt.Errorf("SaveBacktestResult: result is not valid JSON")
		}
		payload = string(rec.Result)
	} else if rec.ResultStatus != "failed" {
		return nil, fmt.Errorf("SaveBacktestResult: result is required for result_status %q", rec.ResultStatus)
	}

	const q = `
		INSERT INTO run.backtest_results (run_id, result_status, result, error_message)
		VALUES ($1,$2,$3::jsonb,$4)
		ON CONFLICT (run_id) DO UPDATE SET
			result_status = EXCLUDED.result_status,
			result        = EXCLUDED.result,
			error_message = EXCLUDED.error_message,
			updated_at    = now()
		RETURNING run_id, result_status, result, COALESCE(error_message,''), created_at, updated_at
	`
	var out BacktestResultRecord
	err := s.pool.QueryRow(ctx, q, rec.RunID, rec.ResultStatus, payload, rec.ErrorMessage).Scan(
		&out.RunID, &out.ResultStatus, &out.Result, &out.ErrorMessage, &out.CreatedAt, &out.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("save backtest result for %s: %w", rec.RunID, err)
	}
	return &out, nil
}

// GetBacktestResult 读回一条结果行；不存在时返回 ErrBacktestResultNotFound。
func (s *PostgresStore) GetBacktestResult(ctx context.Context, runID string) (*BacktestResultRecord, error) {
	const q = `
		SELECT run_id, result_status, result, COALESCE(error_message,''), created_at, updated_at
		FROM run.backtest_results WHERE run_id = $1
	`
	var out BacktestResultRecord
	err := s.pool.QueryRow(ctx, q, runID).Scan(
		&out.RunID, &out.ResultStatus, &out.Result, &out.ErrorMessage, &out.CreatedAt, &out.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: run_id %q", ErrBacktestResultNotFound, runID)
		}
		return nil, fmt.Errorf("get backtest result for %s: %w", runID, err)
	}
	return &out, nil
}

// ListBacktestResults 按 created_at 降序读回结果面作业（最多 limit 条）。
//
// 列表页用它补齐「没有任务行」的作业：同步回测与迁移前的存量只存在于结果
// 面，任务面列表里看不到它们。
func (s *PostgresStore) ListBacktestResults(ctx context.Context, limit int) ([]*BacktestResultRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	const q = `
		SELECT run_id, result_status, result, COALESCE(error_message,''), created_at, updated_at
		FROM run.backtest_results ORDER BY created_at DESC LIMIT $1
	`
	rows, err := s.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("list backtest results: %w", err)
	}
	defer rows.Close()

	out := make([]*BacktestResultRecord, 0, limit)
	for rows.Next() {
		var rec BacktestResultRecord
		if err := rows.Scan(&rec.RunID, &rec.ResultStatus, &rec.Result, &rec.ErrorMessage, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan backtest result: %w", err)
		}
		out = append(out, &rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate backtest results: %w", err)
	}
	return out, nil
}
