package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	cdom "github.com/ruoxizhnya/tellurion-contracts/domain"
)

import "tellurion/pkg/domain"

// ── Factor Returns ────────────────────────────────────────────────────────────

// SaveFactorReturnBatch saves multiple factor return records in a batch.
func (s *PostgresStore) SaveFactorReturnBatch(ctx context.Context, records []*domain.FactorReturn) error {
	if len(records) == 0 {
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	batch := &pgx.Batch{}
	for _, r := range records {
		batch.Queue(`
			INSERT INTO factor_returns (factor_name, trade_date, quintile, avg_return, cumulative_return, top_minus_bot)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (factor_name, trade_date, quintile) DO UPDATE SET
				avg_return = EXCLUDED.avg_return,
				cumulative_return = EXCLUDED.cumulative_return,
				top_minus_bot = EXCLUDED.top_minus_bot
		`, r.FactorName, r.TradeDate, r.Quintile, r.AvgReturn, r.CumulativeReturn, r.TopMinusBot)
	}

	results := tx.SendBatch(ctx, batch)

	for i := 0; i < len(records); i++ {
		if _, err := results.Exec(); err != nil {
			results.Close()
			return fmt.Errorf("batch factor_returns insert failed at index %d: %w", i, err)
		}
	}

	if err := results.Close(); err != nil {
		return fmt.Errorf("failed to close batch results: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	s.logger.Info().Int("count", len(records)).Msg("Batch factor_returns saved")
	return nil
}

// HasFactorReturns 报告 factor_returns 表里是否**有任何行**。
//
// 用途：产物组装在「全部因子都取不到收益」时区分两种原因——表空 = 本栈没有
// 生产写者（ADR-038），表非空 = 本次窗口没数据。只做一次 EXISTS，不拉数据。
func (s *PostgresStore) HasFactorReturns(ctx context.Context) (bool, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM factor_returns)`).Scan(&exists); err != nil {
		return false, fmt.Errorf("probe factor_returns: %w", err)
	}
	return exists, nil
}

// GetFactorReturns retrieves factor returns for a factor within a date range.
func (s *PostgresStore) GetFactorReturns(ctx context.Context, factor cdom.FactorType, startDate, endDate time.Time) ([]*domain.FactorReturn, error) {
	query := `
		SELECT id, factor_name, trade_date, quintile, avg_return, cumulative_return, top_minus_bot
		FROM factor_returns
		WHERE factor_name = $1 AND trade_date >= $2 AND trade_date <= $3
		ORDER BY trade_date ASC, quintile ASC
	`
	rows, err := s.pool.Query(ctx, query, factor, startDate, endDate)
	if err != nil {
		return nil, fmt.Errorf("failed to query factor_returns: %w", err)
	}
	defer rows.Close()

	var results []*domain.FactorReturn
	for rows.Next() {
		var r domain.FactorReturn
		if err := rows.Scan(&r.ID, &r.FactorName, &r.TradeDate, &r.Quintile, &r.AvgReturn, &r.CumulativeReturn, &r.TopMinusBot); err != nil {
			return nil, fmt.Errorf("failed to scan factor_return row: %w", err)
		}
		results = append(results, &r)
	}

	return results, rows.Err()
}

// ── IC Analysis ───────────────────────────────────────────────────────────────

// SaveICEntryBatch saves multiple IC analysis records in a batch.
func (s *PostgresStore) SaveICEntryBatch(ctx context.Context, records []*domain.ICEntry) error {
	if len(records) == 0 {
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	batch := &pgx.Batch{}
	for _, r := range records {
		batch.Queue(`
			INSERT INTO ic_analysis (factor_name, trade_date, ic, p_value, top_ic)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (factor_name, trade_date) DO UPDATE SET
				ic = EXCLUDED.ic,
				p_value = EXCLUDED.p_value,
				top_ic = EXCLUDED.top_ic
		`, r.FactorName, r.TradeDate, r.IC, r.PValue, r.TopIC)
	}

	results := tx.SendBatch(ctx, batch)

	for i := 0; i < len(records); i++ {
		if _, err := results.Exec(); err != nil {
			results.Close()
			return fmt.Errorf("batch ic_analysis insert failed at index %d: %w", i, err)
		}
	}

	if err := results.Close(); err != nil {
		return fmt.Errorf("failed to close batch results: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	s.logger.Info().Int("count", len(records)).Msg("Batch ic_analysis saved")
	return nil
}

// GetICEntries retrieves IC entries for a factor within a date range.
func (s *PostgresStore) GetICEntries(ctx context.Context, factor cdom.FactorType, startDate, endDate time.Time) ([]*domain.ICEntry, error) {
	query := `
		SELECT id, factor_name, trade_date, ic, p_value, top_ic
		FROM ic_analysis
		WHERE factor_name = $1 AND trade_date >= $2 AND trade_date <= $3
		ORDER BY trade_date ASC
	`
	rows, err := s.pool.Query(ctx, query, factor, startDate, endDate)
	if err != nil {
		return nil, fmt.Errorf("failed to query ic_analysis: %w", err)
	}
	defer rows.Close()

	var results []*domain.ICEntry
	for rows.Next() {
		var r domain.ICEntry
		if err := rows.Scan(&r.ID, &r.FactorName, &r.TradeDate, &r.IC, &r.PValue, &r.TopIC); err != nil {
			return nil, fmt.Errorf("failed to scan ic_entry row: %w", err)
		}
		results = append(results, &r)
	}

	return results, rows.Err()
}

// SVC-CORE-DATA-RETIRE（2026-11-08）：dividends / index_constituents / splits
// 的读写面随数据面退役（owner = 数据服务仓）；本文件只保留 core 自有的
// factor_cache / factor_returns / ic_analysis 段。
//
// SVC-CORE-RUN-RETIRE（2026-11-09）：walk_forward_reports 的读写段已退役
// （owner = orchestration 的 run.walk_forward_reports）。WF 报告不再落库，
// POST /api/walkforward 只做计算并直接返回。
