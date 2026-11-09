package storage

// 指数日线（DATA-12，Migration 057）——market.index_daily 表的写入面。
// 指数日线是 B 类规范化数据（A–E 分区表：B = PG market.*），落独立表而不是
// 塞进 ohlcv_daily_qfq（那张表全是股票，会被票池/因子/NT 导出当股票看见）。
// DDL 的唯一真相在 postgres.go 的 Migration 057 追加段（migrate() 按序执行）。
//
// 写入口径同宏观层/活跃度层：
//   - 批量 Save 走「一个事务 + pgx.Batch」，任一行失败整批回滚；
//   - 幂等用 ON CONFLICT ... DO UPDATE（唯一键 code+trade_date）：同键重导刷新
//     取值与 as_of——上游会重述修正，静默保留旧值等于让指数层悄悄过期。
//
// 校验口径（fail-loud）：空 code / 空 source / 零 trade_date / 零 as_of 一律拒绝；
// close/volume 必填且有限；open/high/low/pre_close/change/pct_chg/turnover 可空
// （NULL = 上游未发布，缺失≠0），非空时必须有限（NaN/Inf 是脏值，拒绝）。

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	apperrors "github.com/ruoxizhnya/tellurion-kit/errors"
	"tellurion/pkg/domain"
)

// saveIndexDailySQL —— 幂等落库一条指数日线。数值列走 EXCLUDED 直传
// （*float64 → NULL/DOUBLE PRECISION）：nil 落 NULL，不折 0。
const saveIndexDailySQL = `INSERT INTO market.index_daily
	(code, trade_date, open, high, low, close, pre_close, change, pct_chg, volume, turnover, source, as_of)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	ON CONFLICT (code, trade_date) DO UPDATE SET
		open      = EXCLUDED.open,
		high      = EXCLUDED.high,
		low       = EXCLUDED.low,
		close     = EXCLUDED.close,
		pre_close = EXCLUDED.pre_close,
		change    = EXCLUDED.change,
		pct_chg   = EXCLUDED.pct_chg,
		volume    = EXCLUDED.volume,
		turnover  = EXCLUDED.turnover,
		source    = EXCLUDED.source,
		as_of     = EXCLUDED.as_of`

// SaveIndexDailyBatch 批量写入指数日线（一个事务）。
func (s *PostgresStore) SaveIndexDailyBatch(ctx context.Context, rows []*domain.IndexDaily) error {
	if len(rows) == 0 {
		return nil
	}
	for _, r := range rows {
		if strings.TrimSpace(r.Code) == "" {
			return apperrors.Wrap(fmt.Errorf("code=%q", r.Code),
				apperrors.ErrCodeInvalidInput, "指数日线缺少身份列（code）", "storage.SaveIndexDailyBatch")
		}
		if strings.TrimSpace(r.Source) == "" {
			return apperrors.Wrap(fmt.Errorf("code=%q", r.Code),
				apperrors.ErrCodeInvalidInput, "指数日线缺少 source 溯源列", "storage.SaveIndexDailyBatch")
		}
		if r.TradeDate.IsZero() {
			return apperrors.Wrap(fmt.Errorf("code=%q", r.Code),
				apperrors.ErrCodeInvalidInput, "指数日线 trade_date 为零值（交易日不可缺）", "storage.SaveIndexDailyBatch")
		}
		if r.AsOf.IsZero() {
			return apperrors.Wrap(fmt.Errorf("code=%q", r.Code),
				apperrors.ErrCodeInvalidInput, "指数日线 as_of 为零值（导入时点不可缺）", "storage.SaveIndexDailyBatch")
		}
		if err := finiteRequired(r.Close, r.Code, "close"); err != nil {
			return err
		}
		if err := finiteRequired(r.Volume, r.Code, "volume"); err != nil {
			return err
		}
		for _, c := range []struct {
			name string
			v    *float64
		}{
			{"open", r.Open}, {"high", r.High}, {"low", r.Low},
			{"pre_close", r.PreClose}, {"change", r.Change},
			{"pct_chg", r.PctChg}, {"turnover", r.Turnover},
		} {
			if err := finiteOrNil(c.v, r.Code, c.name); err != nil {
				return err
			}
		}
	}
	return s.batchExec(ctx, "market.index_daily", len(rows), func(b *pgx.Batch) {
		for _, r := range rows {
			// trade_date 是 DATE 列：传 "YYYY-MM-DD" 字符串，避免 timestamptz→date
			// 隐式转换受会话时区影响而漂移一天（同 SaveMarketActivityBatch）。
			b.Queue(saveIndexDailySQL, r.Code, dateOnly(r.TradeDate),
				r.Open, r.High, r.Low, r.Close, r.PreClose, r.Change, r.PctChg,
				r.Volume, r.Turnover, r.Source, r.AsOf)
		}
	})
}

// finiteRequired 校验「必填数值列」：必须有限（NaN/Inf 是脏值，拒绝）。
func finiteRequired(v float64, code, column string) error {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return apperrors.Wrap(fmt.Errorf("code=%q %s=%v", code, column, v),
			apperrors.ErrCodeInvalidInput, "指数日线数值列必须有限（脏值拒绝）",
			"storage.SaveIndexDailyBatch")
	}
	return nil
}

// finiteOrNil 校验「可空数值列」：nil = 缺失（合法，落 NULL）；非 nil 必须
// 有限（NaN/Inf 是脏值，拒绝——缺失≠0，脏值≠缺失）。
//
// SVC-CORE-DATA-RETIRE（2026-11-08）：原在 market_activity.go（随数据面退役）。
// index_daily 仍在 core（裁决 ⑰），故把它留在本文件继续服务指数日线写入。
func finiteOrNil(v *float64, code, column string) error {
	if v == nil {
		return nil
	}
	if math.IsNaN(*v) || math.IsInf(*v, 0) {
		return apperrors.Wrap(fmt.Errorf("code=%q %s=%v", code, column, *v),
			apperrors.ErrCodeInvalidInput, "指数日线数值列必须有限（nil=缺失，脏值拒绝）",
			"storage.SaveIndexDailyBatch")
	}
	return nil
}

// LatestIndexBar 是 `/api/market/index` 的读形状：某个指数在 as-of 之前
// 最近一根 bar，外加**那一根的前一期收盘**（用来算涨跌幅）。
//
// 为什么把 prev_close 一起读回来而不是让 handler 再查一次：两次查询之间
// 可能刚好插入新 bar，close 与 baseline 就会来自不同期——同一张卡片上的
// 涨跌幅用错基准，界面上完全看不出来（DATA-11 记录过这个坑）。
// 指针语义照旧：Turnover/PrevClose 为 nil = 上游没发布，不是 0。
type LatestIndexBar struct {
	Code      string
	TradeDate time.Time
	Close     float64
	Volume    float64
	Turnover  *float64
	PrevClose *float64
}

// LatestIndexBars 读每个 code 在 asOf（nil = 不限）当天或之前最近一根 bar，
// 并带回它的前一期收盘。窗口函数一趟取回两期，顺序确定（按 code 升序）。
//
// ⚠️ 这是「最近一根 bar」，**不是 PIT 快照**：可见锚点只有 trade_date，
// 没有公告日/摄取时刻过滤；与 D2 线那套 available_date/as_of 口径无关。
// 库里没有行的 code 不产条目（不编零值）；先于全部行也不产。
func (s *PostgresStore) LatestIndexBars(ctx context.Context, codes []string, asOf *time.Time) ([]LatestIndexBar, error) {
	if len(codes) == 0 {
		return nil, nil
	}
	const query = `WITH ranked AS (
		SELECT code, trade_date, close, volume, turnover,
		       ROW_NUMBER() OVER (PARTITION BY code ORDER BY trade_date DESC) AS rn
		FROM market.index_daily
		WHERE code = ANY($1) AND ($2::date IS NULL OR trade_date <= $2)
	)
	SELECT a.code, a.trade_date, a.close, a.volume, a.turnover, b.close
	FROM ranked a
	LEFT JOIN ranked b ON b.code = a.code AND b.rn = 2
	WHERE a.rn = 1
	ORDER BY a.code`
	var asOfArg any
	if asOf != nil {
		// 同 SaveIndexDailyBatch：DATE 列传 "YYYY-MM-DD" 字符串，避免时区偏移。
		asOfArg = dateOnly(*asOf)
	}
	rows, err := s.pool.Query(ctx, query, codes, asOfArg)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.ErrCodeInternal,
			"指数日线最近一期查询失败", "storage.LatestIndexBars")
	}
	defer rows.Close()

	var out []LatestIndexBar
	for rows.Next() {
		var b LatestIndexBar
		if err := rows.Scan(&b.Code, &b.TradeDate, &b.Close, &b.Volume, &b.Turnover, &b.PrevClose); err != nil {
			return nil, apperrors.Wrap(err, apperrors.ErrCodeInternal,
				"指数日线最近一期结果行扫描失败", "storage.LatestIndexBars")
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, apperrors.Wrap(err, apperrors.ErrCodeInternal,
			"指数日线最近一期结果集迭代失败", "storage.LatestIndexBars")
	}
	return out, nil
}

// GetIndexDailyRange 按交易日闭区间 [start, end] 读一个指数的日线，按 trade_date
// 升序返回（确定性序列）。空结果是合法答案。供隔离库/集成腿核对单位与幂等。
func (s *PostgresStore) GetIndexDailyRange(ctx context.Context, code string, start, end time.Time) ([]domain.IndexDaily, error) {
	if end.Before(start) {
		return nil, apperrors.New(apperrors.ErrCodeInvalidInput, fmt.Sprintf(
			"指数日线查询 end(%s) 早于 start(%s)", dateOnly(end), dateOnly(start)))
	}
	const query = `SELECT code, trade_date, open, high, low, close, pre_close, change, pct_chg, volume, turnover, source, as_of
		FROM market.index_daily
		WHERE code = $1 AND trade_date >= $2 AND trade_date <= $3
		ORDER BY trade_date`
	rows, err := s.pool.Query(ctx, query, code, dateOnly(start), dateOnly(end))
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.ErrCodeInternal,
			"指数日线区间查询失败", "storage.GetIndexDailyRange")
	}
	defer rows.Close()

	var out []domain.IndexDaily
	for rows.Next() {
		var d domain.IndexDaily
		if err := rows.Scan(&d.Code, &d.TradeDate, &d.Open, &d.High, &d.Low, &d.Close,
			&d.PreClose, &d.Change, &d.PctChg, &d.Volume, &d.Turnover, &d.Source, &d.AsOf); err != nil {
			return nil, apperrors.Wrap(err, apperrors.ErrCodeInternal,
				"指数日线结果行扫描失败", "storage.GetIndexDailyRange")
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, apperrors.Wrap(err, apperrors.ErrCodeInternal,
			"指数日线结果集迭代失败", "storage.GetIndexDailyRange")
	}
	return out, nil
}
