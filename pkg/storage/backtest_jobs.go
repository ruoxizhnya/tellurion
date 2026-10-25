package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// CreateBacktestJob inserts a new backtest job.
func (s *PostgresStore) CreateBacktestJob(ctx context.Context, job map[string]any) error {
	query := `
		INSERT INTO backtest_jobs (id, strategy_id, params, universe, start_date, end_date, status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())
	`
	_, err := s.pool.Exec(ctx, query,
		job["id"], job["strategy_id"], job["params"],
		job["universe"], job["start_date"], job["end_date"], job["status"],
	)
	if err != nil {
		return fmt.Errorf("failed to create backtest job: %w", err)
	}
	return nil
}

// UpdateJobStarted marks a job as running.
func (s *PostgresStore) UpdateJobStarted(ctx context.Context, jobID string) error {
	query := `UPDATE backtest_jobs SET status = 'running', started_at = NOW() WHERE id = $1`
	_, err := s.pool.Exec(ctx, query, jobID)
	if err != nil {
		return fmt.Errorf("failed to update job started: %w", err)
	}
	return nil
}

// UpdateJobCompleted marks a job as completed with a result and its K9 result
// status (completed / invalid). The lifecycle status stays 'completed' for both
// — invalid is a result conclusion, not a lifecycle failure (B6-3).
func (s *PostgresStore) UpdateJobCompleted(ctx context.Context, jobID, resultStatus string, result []byte) error {
	query := `UPDATE backtest_jobs SET status = 'completed', result_status = $2, result = $3, completed_at = NOW() WHERE id = $1`
	_, err := s.pool.Exec(ctx, query, jobID, resultStatus, result)
	if err != nil {
		return fmt.Errorf("failed to update job completed: %w", err)
	}
	return nil
}

// UpdateJobFailed marks a job as failed with an error message. A failed run
// that produced no ResultSpec keeps result_status = 'failed' (K9), never NULL
// on the lifecycle row written by this path.
func (s *PostgresStore) UpdateJobFailed(ctx context.Context, jobID string, errMsg string) error {
	query := `UPDATE backtest_jobs SET status = 'failed', result_status = 'failed', error_message = $2, completed_at = NOW() WHERE id = $1`
	_, err := s.pool.Exec(ctx, query, jobID, errMsg)
	if err != nil {
		return fmt.Errorf("failed to update job failed: %w", err)
	}
	return nil
}

// GetBacktestJob retrieves a single backtest job by ID as map[string]any.
func (s *PostgresStore) GetBacktestJob(ctx context.Context, jobID string) (map[string]any, error) {
	query := `
		SELECT id, strategy_id, params, universe,
		       start_date::text, end_date::text,
		       status, result_status, result, error_message, created_at, started_at, completed_at
		FROM backtest_jobs WHERE id = $1
	`
	var id, strategyID, universe, startDate, endDate, status string
	var params, result []byte
	var resultStatus, errorMsg *string
	var createdAt time.Time
	var startedAt, completedAt *time.Time

	err := s.pool.QueryRow(ctx, query, jobID).Scan(
		&id, &strategyID, &params, &universe, &startDate, &endDate,
		&status, &resultStatus, &result, &errorMsg, &createdAt, &startedAt, &completedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get backtest job: %w", err)
	}

	m := map[string]any{
		"id":          id,
		"strategy_id": strategyID,
		"params":      params,
		"universe":    universe,
		"start_date":  startDate,
		"end_date":    endDate,
		"status":      status,
		"result":      result,
		"created_at":  createdAt,
	}
	if resultStatus != nil {
		m["result_status"] = *resultStatus
	}
	if errorMsg != nil {
		m["error_msg"] = *errorMsg
	}
	if startedAt != nil {
		m["started_at"] = *startedAt
	}
	if completedAt != nil {
		m["completed_at"] = *completedAt
	}
	return m, nil
}

// ListBacktestJobs returns recent backtest jobs as []map[string]any.
func (s *PostgresStore) ListBacktestJobs(ctx context.Context, limit int) ([]map[string]any, error) {
	query := `
		SELECT id, strategy_id, params, universe,
		       start_date::text, end_date::text,
		       status, result_status, result, error_message, created_at, started_at, completed_at
		FROM backtest_jobs
		ORDER BY created_at DESC
		LIMIT $1
	`
	rows, err := s.pool.Query(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list backtest jobs: %w", err)
	}
	defer rows.Close()

	var results []map[string]any
	for rows.Next() {
		var id, strategyID, universe, startDate, endDate, status string
		var params, result []byte
		var resultStatus, errorMsg *string
		var createdAt time.Time
		var startedAt, completedAt *time.Time
		if err := rows.Scan(
			&id, &strategyID, &params, &universe, &startDate, &endDate,
			&status, &resultStatus, &result, &errorMsg, &createdAt, &startedAt, &completedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan backtest job row: %w", err)
		}
		m := map[string]any{
			"id":          id,
			"strategy_id": strategyID,
			"params":      params,
			"universe":    universe,
			"start_date":  startDate,
			"end_date":    endDate,
			"status":      status,
			"result":      result,
			"created_at":  createdAt,
		}
		if resultStatus != nil {
			m["result_status"] = *resultStatus
		}
		if errorMsg != nil {
			m["error_msg"] = *errorMsg
		}
		if startedAt != nil {
			m["started_at"] = *startedAt
		}
		if completedAt != nil {
			m["completed_at"] = *completedAt
		}
		results = append(results, m)
	}
	return results, rows.Err()
}

// QT-72 · X0 三 hash 对外读面（core 侧）。
//
// 一次读 `quant.run_provenance ⋈ backtest_jobs`：输入身份（三 hash / 生效值）
// 来自留痕表，**结论轴（status / result_status）只从 backtest_jobs 取**。
// 留痕表没有 status 列，也不许把某个 hash 列当结论用 —— 那会让「跑完但无效」
// 与「根本没跑完」在对外响应里混成一个值。
//
// 可空性：LEFT JOIN 的右侧整行为 NULL 时 Provenance == nil；ResultStatus 空串
// 表示 `backtest_jobs.result_status` 为 NULL（非终态）。
type BacktestJobProvenance struct {
	JobStatus    string
	ResultStatus string
	Provenance   *RunProvenanceRecord
}

// provenanceResultStatusExpr 是结论轴的唯一表达式。
//
// 破坏验证的锚点：把它换成 provenance 的任意 hash 列（例如 r.run_spec_hash），
// TestQT72_ProvenanceResultStatusComesFromJobs 必须红。
const provenanceResultStatusExpr = "COALESCE(b.result_status, '')"

const backtestProvenanceSelectSQL = `
	SELECT b.status,
	       ` + provenanceResultStatusExpr + `,
	       r.run_id,
	       COALESCE(r.run_spec_hash, ''),
	       COALESCE(r.dataset_snapshot_hash, ''),
	       COALESCE(r.artifact_hash, ''),
	       COALESCE(r.version_hash, ''),
	       COALESCE(r.strategy_ref_input, ''),
	       COALESCE(r.params_json, '{}'::jsonb),
	       COALESCE(r.params_hash, ''),
	       COALESCE(r.seed, 0),
	       COALESCE(r.kernel, ''),
	       COALESCE(r.engine_version, ''),
	       COALESCE(r.fill_timing, ''),
	       COALESCE(r.risk_rules_hash, ''),
	       r.created_at
	FROM backtest_jobs b
	LEFT JOIN quant.run_provenance r ON r.run_id = b.id
	WHERE b.id = $1
`

// GetBacktestJobProvenance 读一条 run 的「输入身份 + 结论」。
// job 不存在 → (nil, nil)；job 在但留痕不在 → Provenance == nil。
func (s *PostgresStore) GetBacktestJobProvenance(ctx context.Context, runID string) (*BacktestJobProvenance, error) {
	var (
		out       BacktestJobProvenance
		runIDp    *string
		rec       RunProvenanceRecord
		createdAt *time.Time
	)
	err := s.pool.QueryRow(ctx, backtestProvenanceSelectSQL, runID).Scan(
		&out.JobStatus, &out.ResultStatus,
		&runIDp,
		&rec.RunSpecHash, &rec.DatasetSnapshotHash, &rec.ArtifactHash, &rec.VersionHash,
		&rec.StrategyRefInput, &rec.ParamsJSON, &rec.ParamsHash, &rec.Seed,
		&rec.Kernel, &rec.EngineVersion, &rec.FillTiming, &rec.RiskRulesHash,
		&createdAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get backtest provenance for %s: %w", runID, err)
	}
	if runIDp != nil {
		rec.RunID = *runIDp
		if createdAt != nil {
			rec.CreatedAt = *createdAt
		}
		out.Provenance = &rec
	}
	return &out, nil
}

// DeleteBacktestJob deletes a pending job by ID.
func (s *PostgresStore) DeleteBacktestJob(ctx context.Context, jobID string) error {
	query := `DELETE FROM backtest_jobs WHERE id = $1 AND status = 'pending'`
	result, err := s.pool.Exec(ctx, query, jobID)
	if err != nil {
		return fmt.Errorf("failed to delete backtest job: %w", err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("job not found or not pending")
	}
	return nil
}
