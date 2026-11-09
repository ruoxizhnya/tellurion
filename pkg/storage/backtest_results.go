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
