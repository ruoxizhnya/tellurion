package storage

// REV-18：qfq 复权基准快照的持久化（pkg/backtest.AdjustmentAnchorStore 的
// 生产实现）。表结构见 postgres.go 的 backtest_adjustment_anchors DDL。
//
// 语义要点：
//   - **首写者胜**：Save 用 ON CONFLICT DO NOTHING。基准是「该 symbol 在该
//     区间起点第一次被取数时的锚点」，一旦冻结就不可被后来的运行改写——
//     否则「冻结」形同虚设，重跑确定性无从谈起。
//   - Load 不猜默认值：库里没有就返回缺失，由调用方决定是否首次采集。

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// LoadAdjustmentAnchors 读出 start_date 起的、给定 symbol 集合内已冻结的基准。
// 返回 map 以 symbol 为键；没冻结过的 symbol 不在其中。
func (s *PostgresStore) LoadAdjustmentAnchors(
	ctx context.Context,
	start time.Time,
	symbols []string,
) (map[string]AdjustmentAnchor, error) {
	if len(symbols) == 0 {
		return map[string]AdjustmentAnchor{}, nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT symbol, anchor_date, anchor_close, series_digest
		FROM backtest_adjustment_anchors
		WHERE start_date = $1 AND symbol = ANY($2)`,
		start, symbols)
	if err != nil {
		// 表不存在（42P01）= 部署还没跑到新 DDL：优雅降级为「无冻结基准」。
		// 复权快照是增强特性，不能让回测因它挂掉；下一次 migrate 建表后
		// 自动开始冻结。用 pgcode 判别，不做字符串比对。
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P01" {
			return map[string]AdjustmentAnchor{}, nil
		}
		return nil, fmt.Errorf("query adjustment anchors for start %s: %w", start.Format("2006-01-02"), err)
	}
	defer rows.Close()

	out := make(map[string]AdjustmentAnchor, len(symbols))
	for rows.Next() {
		var a AdjustmentAnchor
		if err := rows.Scan(&a.Symbol, &a.AnchorDate, &a.AnchorClose, &a.SeriesDigest); err != nil {
			return nil, fmt.Errorf("scan adjustment anchor: %w", err)
		}
		a.AnchorDate = a.AnchorDate.UTC()
		out[a.Symbol] = a
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate adjustment anchors: %w", err)
	}
	return out, nil
}

// AdjustmentAnchor 是 pkg/backtest.AdjustmentAnchor 的存储侧别名（同形结构，
// 避免 storage 反向依赖 backtest 包）。
type AdjustmentAnchor struct {
	Symbol       string
	AnchorDate   time.Time
	AnchorClose  float64
	SeriesDigest string
}

// SaveAdjustmentAnchors 批量冻结基准（首写者胜）。同一批里既有已冻结又有
// 新采集时，已冻结的保持原值。
func (s *PostgresStore) SaveAdjustmentAnchors(ctx context.Context, start time.Time, anchors []AdjustmentAnchor) error {
	if len(anchors) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	for _, a := range anchors {
		batch.Queue(`
			INSERT INTO backtest_adjustment_anchors (symbol, start_date, anchor_date, anchor_close, series_digest)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (symbol, start_date) DO NOTHING`,
			a.Symbol, start, a.AnchorDate, a.AnchorClose, a.SeriesDigest)
	}
	if err := s.pool.SendBatch(ctx, batch).Close(); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P01" {
			// 表不存在：静默跳过（同 Load 的降级口径），下次 migrate 后生效。
			return nil
		}
		return fmt.Errorf("save %d adjustment anchors for start %s: %w", len(anchors), start.Format("2006-01-02"), err)
	}
	return nil
}
