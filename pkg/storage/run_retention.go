package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// CORE-RESULT-RETENTION：回测结果面与 provenance 的保留期清理。
//
// 口径见 docs/result-retention.md：
//   - 明细（权益曲线点 / 成交明细）90 天：从 result JSONB 摘除
//     portfolio_values / trades，并在结果行上留 detail_purged_at；
//   - run 级汇总与 provenance 365 天：结果行与 provenance 行同事务删除；
//   - 被引用的 run 永不删；运行中的 run 永不删；
//   - 逐 run 一个事务、先明细后汇总、幂等、可 dry-run。
//
// 明细不是独立子表：它存在 run.backtest_results.result 的两个 JSONB 键里，
// 所以「先子表后主表」在这里表现为「先摘明细键（同一行内的子内容），再删
// provenance 身份行」；没有需要单独删除的明细表。

// RunRetentionSchemaDDL 是 CORE-RESULT-RETENTION 的新增 schema。
//
// ⚠️ 必须追加在 allMigrationStatements() 的**最末尾**：迁移版本 = 完整语句
// 序列的下标 + 1，向既有数组中间插入会让存量库的账本指纹全部错位（
// CORE-MIGRATION-REPLAY-GUARD）。这里只做幂等 ALTER / 回填 / 表达式索引。
var RunRetentionSchemaDDL = []string{
	`ALTER TABLE run.backtest_results ADD COLUMN IF NOT EXISTS finished_at TIMESTAMPTZ`,
	`ALTER TABLE run.backtest_results ADD COLUMN IF NOT EXISTS detail_purged_at TIMESTAMPTZ`,
	// 存量行没有 finished_at：用 updated_at（最后一次写结论的时间）回填，
	// 比 created_at 更接近真实完成时刻。只回填一次，之后由写入路径维护。
	`UPDATE run.backtest_results SET finished_at = updated_at WHERE finished_at IS NULL`,
	// 保留期扫描按 COALESCE(finished_at, updated_at) 排序 / 过滤；表达式索引
	// 让「取最老的 N 条」不必全表排序。
	`CREATE INDEX IF NOT EXISTS idx_backtest_results_finished_at
		ON run.backtest_results ((COALESCE(finished_at, updated_at)))`,
}

const (
	// DefaultRetentionDetailDays 是明细默认保留期（天）。
	DefaultRetentionDetailDays = 90
	// DefaultRetentionSummaryDays 是 run 级汇总与 provenance 默认保留期（天）。
	DefaultRetentionSummaryDays = 365
	// DefaultRetentionBatchSize 是每阶段单次最多处理的 run 数。
	DefaultRetentionBatchSize = 100
)

// RetentionConfig 是一次清理的参数。零值即默认：明细 90 天、汇总 365 天、
// 单批 100、非 dry-run、时间取 time.Now。
type RetentionConfig struct {
	DetailDays  int
	SummaryDays int
	BatchSize   int
	DryRun      bool
	// Now 可注入固定时钟（测试边界用）；为空时取 time.Now。
	Now func() time.Time
}

func (c RetentionConfig) normalized() RetentionConfig {
	if c.DetailDays <= 0 {
		c.DetailDays = DefaultRetentionDetailDays
	}
	if c.SummaryDays <= 0 {
		c.SummaryDays = DefaultRetentionSummaryDays
	}
	if c.BatchSize <= 0 {
		c.BatchSize = DefaultRetentionBatchSize
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// RunRetentionAction 是一个 run 的候选 / 已执行动作。
type RunRetentionAction struct {
	RunID        string    `json:"run_id"`
	FinishedAt   time.Time `json:"finished_at"`
	Action       string    `json:"action"` // purge_detail / delete_summary
	Trades       int       `json:"trades,omitempty"`
	EquityPoints int       `json:"equity_points,omitempty"`
	SkipReason   string    `json:"skip_reason,omitempty"`
}

// RetentionReport 是一次清理（含 dry-run）的结果。
//
// DetailRuns / SummaryRuns 只统计**实际执行**（非 dry-run）的 run 数；
// dry-run 时 Actions 里给出「将要做的事」，计数仍可用于预演。
type RetentionReport struct {
	DryRun             bool                 `json:"dry_run"`
	Now                time.Time            `json:"now"`
	DetailCutoff       time.Time            `json:"detail_cutoff"`
	SummaryCutoff      time.Time            `json:"summary_cutoff"`
	DetailRuns         int                  `json:"detail_runs"`
	SummaryRuns        int                  `json:"summary_runs"`
	DetailTrades       int                  `json:"detail_trades"`
	DetailEquityPoints int                  `json:"detail_equity_points"`
	Skipped            int                  `json:"skipped"`
	Actions            []RunRetentionAction `json:"actions,omitempty"`
}

// runReferenceTable 是一张「指向 run」的真实引用表。
type runReferenceTable struct {
	table  string
	column string
}

// runReferenceTables 是保留期判据里的引用来源，全部是真实表 + 真实列。
//
// 保守原则：表不存在 ⇒ 不算引用；探测或查询出错 ⇒ 调用方按被引用处理
// （保留），绝不在不确定时删。quant.run_provenance 自身不算引用。
var runReferenceTables = []runReferenceTable{
	{"run.run_artifacts", "run_id"},
	{"quant.run_analysis_artifacts", "run_id"},
	{"quant.strategy_state", "run_id"},
	{"audit.message_log", "run_id"},
	// 内核模块表：契约样本里有、core 迁移当前不创建；存在才检查。
	{"quant.portfolio_snapshot", "run_id"},
	{"quant.positions", "run_id"},
	{"quant.risk_events", "run_id"},
	{"quant.orders", "run_id"},
	{"quant.fills", "run_id"},
	{"quant.recon_report", "run_id"},
}

// ReapBacktestResults 执行一轮保留期清理。
//
// 失败语义：逐 run 提交，遇到错误立即返回已提交前缀的 report 与 error；
// 下一次调用从剩余候选继续（不会半删同一个 run）。
func (s *PostgresStore) ReapBacktestResults(ctx context.Context, cfg RetentionConfig) (*RetentionReport, error) {
	cfg = cfg.normalized()
	now := cfg.Now()
	report := &RetentionReport{
		DryRun:        cfg.DryRun,
		Now:           now,
		DetailCutoff:  now.AddDate(0, 0, -cfg.DetailDays),
		SummaryCutoff: now.AddDate(0, 0, -cfg.SummaryDays),
	}

	detailCandidates, err := s.retentionDetailCandidates(ctx, report.DetailCutoff, cfg.BatchSize)
	if err != nil {
		return report, err
	}
	for _, c := range detailCandidates {
		action := RunRetentionAction{
			RunID:        c.runID,
			FinishedAt:   c.finishedAt,
			Action:       "purge_detail",
			Trades:       c.trades,
			EquityPoints: c.equityPoints,
		}
		skip, err := s.retentionSkipReason(ctx, c.runID)
		if err != nil {
			// 查询出错：保守保留。记录原因，继续下一条。
			action.SkipReason = "probe_error: " + err.Error()
			report.Skipped++
			report.Actions = append(report.Actions, action)
			continue
		}
		if skip != "" {
			action.SkipReason = skip
			report.Skipped++
			report.Actions = append(report.Actions, action)
			continue
		}
		if !cfg.DryRun {
			if err := s.purgeRunDetail(ctx, c.runID); err != nil {
				return report, err
			}
		}
		report.DetailRuns++
		report.DetailTrades += c.trades
		report.DetailEquityPoints += c.equityPoints
		report.Actions = append(report.Actions, action)
	}

	summaryCandidates, err := s.retentionSummaryCandidates(ctx, report.SummaryCutoff, cfg.BatchSize)
	if err != nil {
		return report, err
	}
	for _, c := range summaryCandidates {
		action := RunRetentionAction{
			RunID:      c.runID,
			FinishedAt: c.finishedAt,
			Action:     "delete_summary",
		}
		skip, err := s.retentionSkipReason(ctx, c.runID)
		if err != nil {
			action.SkipReason = "probe_error: " + err.Error()
			report.Skipped++
			report.Actions = append(report.Actions, action)
			continue
		}
		if skip != "" {
			action.SkipReason = skip
			report.Skipped++
			report.Actions = append(report.Actions, action)
			continue
		}
		if !cfg.DryRun {
			if err := s.deleteRunSummary(ctx, c.runID); err != nil {
				return report, err
			}
		}
		report.SummaryRuns++
		report.Actions = append(report.Actions, action)
	}
	return report, nil
}

type retentionCandidate struct {
	runID        string
	finishedAt   time.Time
	trades       int
	equityPoints int
}

// retentionDetailCandidates 取「仍有明细且已过明细保留期」的 run。
//
// 只挑 result 里真的还有 portfolio_values / trades 的行：failed 的空载荷
// 结果从未有过明细，不能被标记成「已按策略清理」。
func (s *PostgresStore) retentionDetailCandidates(ctx context.Context, cutoff time.Time, limit int) ([]retentionCandidate, error) {
	const q = `
		SELECT run_id,
		       COALESCE(finished_at, updated_at, created_at),
		       CASE WHEN jsonb_typeof(result->'trades') = 'array'
		            THEN jsonb_array_length(result->'trades') ELSE 0 END,
		       CASE WHEN jsonb_typeof(result->'portfolio_values') = 'array'
		            THEN jsonb_array_length(result->'portfolio_values') ELSE 0 END
		FROM run.backtest_results
		WHERE COALESCE(finished_at, updated_at, created_at) < $1
		  AND detail_purged_at IS NULL
		  AND result IS NOT NULL
		  AND (result ? 'portfolio_values' OR result ? 'trades')
		ORDER BY COALESCE(finished_at, updated_at, created_at), run_id
		LIMIT $2`
	rows, err := s.pool.Query(ctx, q, cutoff, limit)
	if err != nil {
		return nil, fmt.Errorf("retention detail candidates: %w", err)
	}
	defer rows.Close()

	var out []retentionCandidate
	for rows.Next() {
		var c retentionCandidate
		if err := rows.Scan(&c.runID, &c.finishedAt, &c.trades, &c.equityPoints); err != nil {
			return nil, fmt.Errorf("scan retention detail candidate: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate retention detail candidates: %w", err)
	}
	return out, nil
}

// retentionSummaryCandidates 取「已过汇总保留期」的 run（无论明细是否已清）。
func (s *PostgresStore) retentionSummaryCandidates(ctx context.Context, cutoff time.Time, limit int) ([]retentionCandidate, error) {
	const q = `
		SELECT run_id, COALESCE(finished_at, updated_at, created_at)
		FROM run.backtest_results
		WHERE COALESCE(finished_at, updated_at, created_at) < $1
		ORDER BY COALESCE(finished_at, updated_at, created_at), run_id
		LIMIT $2`
	rows, err := s.pool.Query(ctx, q, cutoff, limit)
	if err != nil {
		return nil, fmt.Errorf("retention summary candidates: %w", err)
	}
	defer rows.Close()

	var out []retentionCandidate
	for rows.Next() {
		var c retentionCandidate
		if err := rows.Scan(&c.runID, &c.finishedAt); err != nil {
			return nil, fmt.Errorf("scan retention summary candidate: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate retention summary candidates: %w", err)
	}
	return out, nil
}

// retentionSkipReason 返回跳过原因；空串表示可以处理。
//
// 顺序：运行中优先（状态未知也要保留），再查引用。任何探测错误都由调用方
// 按「保留」处理。
func (s *PostgresStore) retentionSkipReason(ctx context.Context, runID string) (string, error) {
	active, err := s.runIsStillActive(ctx, runID)
	if err != nil {
		return "", err
	}
	if active {
		return "run_active", nil
	}
	referenced, err := s.runIsReferenced(ctx, runID)
	if err != nil {
		return "", err
	}
	if referenced {
		return "referenced", nil
	}
	return "", nil
}

// runIsStillActive 查编排侧任务面 run.backtest_jobs（存在才查）。
//
// 结果行只在 run 终止时写入，因此：
//   - run.backtest_jobs 不存在 ⇒ 结果行即为终态，不活跃；
//   - 存在但无该 run 行 ⇒ 同步回测，不活跃；
//   - 状态非终态 ⇒ 活跃（跳过）；未知状态按活跃处理。
func (s *PostgresStore) runIsStillActive(ctx context.Context, runID string) (bool, error) {
	exists, err := relationExists(ctx, s.pool, "run.backtest_jobs")
	if err != nil {
		return true, err
	}
	if !exists {
		return false, nil
	}
	var status string
	err = s.pool.QueryRow(ctx, `SELECT status FROM run.backtest_jobs WHERE id = $1`, runID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return true, fmt.Errorf("status of %s: %w", runID, err)
	}
	switch status {
	case "completed", "failed", "cancelled", "canceled", "invalid":
		return false, nil
	default:
		return true, nil
	}
}

// runIsReferenced 按真实引用表检查是否有别的记录指向该 run。
func (s *PostgresStore) runIsReferenced(ctx context.Context, runID string) (bool, error) {
	for _, rt := range runReferenceTables {
		exists, err := relationExists(ctx, s.pool, rt.table)
		if err != nil {
			return true, err
		}
		if !exists {
			continue
		}
		q := fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE %s = $1)`, rt.table, rt.column)
		var found bool
		if err := s.pool.QueryRow(ctx, q, runID).Scan(&found); err != nil {
			return true, fmt.Errorf("reference probe %s: %w", rt.table, err)
		}
		if found {
			return true, nil
		}
	}
	return false, nil
}

func relationExists(ctx context.Context, pool interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, table string) (bool, error) {
	var oid *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass($1)::text`, table).Scan(&oid); err != nil {
		return false, fmt.Errorf("to_regclass %s: %w", table, err)
	}
	return oid != nil, nil
}

// purgeRunDetail 在**一个事务**里摘除明细键并留痕。
func (s *PostgresStore) purgeRunDetail(ctx context.Context, runID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin purge detail %s: %w", runID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const q = `
		UPDATE run.backtest_results
		SET result = result - 'portfolio_values' - 'trades',
		    detail_purged_at = now(),
		    updated_at = now()
		WHERE run_id = $1
		  AND detail_purged_at IS NULL
		  AND result IS NOT NULL
		  AND (result ? 'portfolio_values' OR result ? 'trades')`
	if _, err := tx.Exec(ctx, q, runID); err != nil {
		return fmt.Errorf("purge detail %s: %w", runID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit purge detail %s: %w", runID, err)
	}
	return nil
}

// deleteRunSummary 在**一个事务**里删结果行（含汇总）与 provenance 行。
func (s *PostgresStore) deleteRunSummary(ctx context.Context, runID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete summary %s: %w", runID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM run.backtest_results WHERE run_id = $1`, runID); err != nil {
		return fmt.Errorf("delete result %s: %w", runID, err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM quant.run_provenance WHERE run_id = $1`, runID); err != nil {
		return fmt.Errorf("delete provenance %s: %w", runID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete summary %s: %w", runID, err)
	}
	return nil
}
