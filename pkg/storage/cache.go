package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/ruoxizhnya/tellurion-contracts/domain"
)

// ── Factor Cache ──────────────────────────────────────────────────────────────

// SaveFactorCacheBatch saves multiple factor cache entries in a batch.
func (s *PostgresStore) SaveFactorCacheBatch(ctx context.Context, entries []*domain.FactorCacheEntry) error {
	if len(entries) == 0 {
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	batch := &pgx.Batch{}
	for _, e := range entries {
		// Empty citation is stored as '[]' — the explicit "no A→B chain
		// established" marker (ODR-061), never NULL.
		citation := e.Citation
		if len(citation) == 0 {
			citation = json.RawMessage("[]")
		}
		batch.Queue(`
			INSERT INTO factor_cache (symbol, trade_date, factor_name, raw_value, z_score, percentile, citation)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (symbol, trade_date, factor_name) DO UPDATE SET
				raw_value = EXCLUDED.raw_value,
				z_score = EXCLUDED.z_score,
				percentile = EXCLUDED.percentile,
				citation = EXCLUDED.citation
		`, e.Symbol, e.TradeDate, e.FactorName, e.RawValue, e.ZScore, e.Percentile, citation)
	}

	results := tx.SendBatch(ctx, batch)

	for i := 0; i < len(entries); i++ {
		if _, err := results.Exec(); err != nil {
			results.Close()
			return fmt.Errorf("batch factor_cache insert failed at index %d: %w", i, err)
		}
	}

	// pgx v5: the batch results must be closed before the transaction can be
	// committed — Commit on a connection with an open batch fails with
	// "conn busy" (found by the ODR-061 runtime acceptance, 2026-09-16).
	if err := results.Close(); err != nil {
		return fmt.Errorf("failed to close batch results: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	s.logger.Info().Int("count", len(entries)).Msg("Batch factor_cache saved")
	return nil
}

// GetFactorCache retrieves a single factor cache entry.
func (s *PostgresStore) GetFactorCache(ctx context.Context, symbol string, date time.Time, factor domain.FactorType) (*domain.FactorCacheEntry, error) {
	query := `
		SELECT id, symbol, trade_date, factor_name, raw_value, z_score, percentile, citation
		FROM factor_cache
		WHERE symbol = $1 AND trade_date = $2 AND factor_name = $3
	`
	var e domain.FactorCacheEntry
	err := s.pool.QueryRow(ctx, query, symbol, date, factor).Scan(
		&e.ID, &e.Symbol, &e.TradeDate, &e.FactorName, &e.RawValue, &e.ZScore, &e.Percentile, &e.Citation,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get factor cache: %w", err)
	}
	return &e, nil
}

// GetFactorCacheRange retrieves factor cache entries for a factor within a date range.
func (s *PostgresStore) GetFactorCacheRange(ctx context.Context, factor domain.FactorType, startDate, endDate time.Time) ([]*domain.FactorCacheEntry, error) {
	query := `
		SELECT id, symbol, trade_date, factor_name, raw_value, z_score, percentile, citation
		FROM factor_cache
		WHERE factor_name = $1 AND trade_date >= $2 AND trade_date <= $3
		ORDER BY trade_date ASC, symbol ASC
	`
	rows, err := s.pool.Query(ctx, query, factor, startDate, endDate)
	if err != nil {
		return nil, fmt.Errorf("failed to query factor cache range: %w", err)
	}
	defer rows.Close()

	var results []*domain.FactorCacheEntry
	for rows.Next() {
		var e domain.FactorCacheEntry
		if err := rows.Scan(&e.ID, &e.Symbol, &e.TradeDate, &e.FactorName, &e.RawValue, &e.ZScore, &e.Percentile, &e.Citation); err != nil {
			return nil, fmt.Errorf("failed to scan factor cache row: %w", err)
		}
		results = append(results, &e)
	}

	return results, rows.Err()
}

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

// GetFactorReturns retrieves factor returns for a factor within a date range.
func (s *PostgresStore) GetFactorReturns(ctx context.Context, factor domain.FactorType, startDate, endDate time.Time) ([]*domain.FactorReturn, error) {
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
func (s *PostgresStore) GetICEntries(ctx context.Context, factor domain.FactorType, startDate, endDate time.Time) ([]*domain.ICEntry, error) {
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
