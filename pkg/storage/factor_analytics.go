package storage

import (
	"context"
	"fmt"
	"time"

	cdom "github.com/ruoxizhnya/tellurion-contracts/domain"
)

// FactorAnalyticsSummary 是 B-05 读面「因子列表」的一行：某个因子已落库的
// 观测覆盖范围与 IC 概览。
type FactorAnalyticsSummary struct {
	FactorName   cdom.FactorType `json:"factor_name"`
	Observations int             `json:"observations"`
	StartDate    time.Time       `json:"start_date"`
	EndDate      time.Time       `json:"end_date"`
	MeanIC       float64         `json:"mean_ic"`
	LatestIC     float64         `json:"latest_ic"`
}

// ListFactorAnalyticsSummaries 返回每个已落库因子的覆盖范围与 IC 概览。
// 只读 factor_returns / ic_analysis 两张派生表，不触行情。
func (s *PostgresStore) ListFactorAnalyticsSummaries(ctx context.Context) ([]FactorAnalyticsSummary, error) {
	query := `
		SELECT fr.factor_name,
		       COUNT(DISTINCT fr.trade_date)::int AS observations,
		       MIN(fr.trade_date),
		       MAX(fr.trade_date),
		       COALESCE((SELECT AVG(ic) FROM ic_analysis i WHERE i.factor_name = fr.factor_name), 0),
		       COALESCE((SELECT i.ic FROM ic_analysis i
		                 WHERE i.factor_name = fr.factor_name
		                 ORDER BY i.trade_date DESC LIMIT 1), 0)
		FROM factor_returns fr
		GROUP BY fr.factor_name
		ORDER BY fr.factor_name
	`
	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to query factor analytics summaries: %w", err)
	}
	defer rows.Close()

	var out []FactorAnalyticsSummary
	for rows.Next() {
		var item FactorAnalyticsSummary
		if err := rows.Scan(
			&item.FactorName, &item.Observations,
			&item.StartDate, &item.EndDate,
			&item.MeanIC, &item.LatestIC,
		); err != nil {
			return nil, fmt.Errorf("failed to scan factor analytics summary: %w", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate factor analytics summaries: %w", err)
	}
	return out, nil
}
