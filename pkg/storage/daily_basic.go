package storage

// 个股日频估值读路径（CORE-STRATEGY-SCREEN-LOCAL-VALUE）。
//
// 数据归属：`market.daily_basic` 的唯一写者是 `tellurion-data`（Tushare
// daily_basic 导入，DDL 见 tellurion-data internal/schema/market.go）。
// core 只读，且必须显式限定 schema —— 本仓不设 search_path，裸表名会先命中
// public（不存在或为空），静默退化成「没有估值数据」。同 fundamentals.go 的
// 裁决，表名做成常量供护栏测试断言。
//
// 口径（与 value 因子的定义对齐）：
//   - PIT：取 `trade_date <= 决策日` 的最近一条，绝不使用未来数据；
//   - PE：`pe_ttm` 优先，缺失回退 `pe`；两个都缺 = NULL；
//   - PB：表里没有 pb_ttm（Tushare daily_basic 只有 pb），因此「同理」在这里
//     退化为「直接读 pb，NULL 即不可比」，不存在回退列，不臆造；
//   - 单位：`total_mv` / `circ_mv` 上游单位是**万元**，本入口统一换算成**元**，
//     换算只发生在 newDailyBasicValuation 一处；
//   - NULL 语义：亏损股 pe_ttm 为 NULL（不是 0）。本入口**保留 NULL**并标记
//     来源为空，由消费方把「该票当日不参与筛选」当作语义 —— 绝不能当成
//     「极便宜」或 0 处理。

import (
	"context"
	"database/sql"
	"time"

	apperrors "github.com/ruoxizhnya/tellurion-kit/errors"
	"tellurion/pkg/domain"
)

// dailyBasicPITTable 是估值读取的显式限定表名（归属 tellurion-data）。
const dailyBasicPITTable = "market.daily_basic"

// dailyBasicPITBulkSQL 一次取多只票在 asOf 当天或之前最近一日的估值。
//
// DISTINCT ON + ORDER BY 决定「最近一条」；trade_date <= $2 是 PIT 上界。
// 抽成常量让静态护栏能断言表名与上界条件，避免有人把 `<=` 改成 `=` 或
// 把表名改回裸名。
const dailyBasicPITBulkSQL = `
		SELECT DISTINCT ON (ts_code)
			ts_code, trade_date, pe_ttm, pe, pb, total_mv, circ_mv
		FROM ` + dailyBasicPITTable + `
		WHERE ts_code = ANY($1) AND trade_date <= $2
		ORDER BY ts_code, trade_date DESC
	`

// dailyBasicHistorySQL 取 [start, end] 区间内的逐日估值，按票、日期升序。
//
// run 预热一次取完区间（回测期间策略只读 run 内快照，运行时查询 provider
// 是红线，蓝图 §4.3）。决策日当天的行取不取由消费方按
// `trade_date <= 决策日` 的既有 PIT 口径判断；这里只提供原始区间。
const dailyBasicHistorySQL = `
		SELECT ts_code, trade_date, pe_ttm, pe, pb, total_mv, circ_mv
		FROM ` + dailyBasicPITTable + `
		WHERE ts_code = ANY($1) AND trade_date >= $2 AND trade_date <= $3
		ORDER BY ts_code, trade_date ASC
	`

// dailyBasicMVWanToYuan 是市值单位换算系数：上游万元 → 元。
// 换算只在本文件的 newDailyBasicValuation 使用，调用点不得再乘一次。
const dailyBasicMVWanToYuan = 1e4

// newDailyBasicValuation 把一条库行映射成估值快照，集中处理三件事：
// pe_ttm→pe 回退（记录来源）、市值万元→元换算、NULL 保留。
func newDailyBasicValuation(
	tsCode string,
	tradeDate time.Time,
	peTTM, pe, pb, totalMV, circMV *float64,
) domain.DailyBasicValuation {
	v := domain.DailyBasicValuation{
		TsCode:    tsCode,
		TradeDate: tradeDate,
		PB:        pb,
		TotalMV:   dailyBasicMVToYuan(totalMV),
		CircMV:    dailyBasicMVToYuan(circMV),
	}
	switch {
	case peTTM != nil:
		v.PE = peTTM
		v.PESource = domain.DailyBasicPESourceTTM
	case pe != nil:
		v.PE = pe
		v.PESource = domain.DailyBasicPESourceReported
	}
	if pb != nil {
		v.PBSource = domain.DailyBasicPBSourceReported
	}
	return v
}

// dailyBasicMVToYuan 是市值单位换算的**单一点**：万元 → 元。
func dailyBasicMVToYuan(v *float64) *float64 {
	if v == nil {
		return nil
	}
	converted := *v * dailyBasicMVWanToYuan
	return &converted
}

// GetDailyBasicValuations 批量读取每只票在 asOf 当天或之前最近一条估值。
//
// 返回 map 只包含库里有行的票：key 不存在 = 「该票截至 asOf 没有任何估值
// 数据」；key 存在但 PE/PB 为 nil = 「当天有行但该列 NULL，不可比」。两者
// 诊断含义不同，消费方不应混为一谈。
func (s *PostgresStore) GetDailyBasicValuations(
	ctx context.Context, symbols []string, asOf time.Time,
) (map[string]domain.DailyBasicValuation, error) {
	if len(symbols) == 0 {
		return nil, nil
	}
	// DATE 列传 "YYYY-MM-DD" 字符串，避免 timestamptz→date 的会话时区偏移
	// （同 LatestIndexBars / GetIndexDailyRange）。
	rows, err := s.pool.Query(ctx, dailyBasicPITBulkSQL, symbols, dateOnly(asOf))
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.ErrCodeInternal,
			"daily_basic 估值 PIT 查询失败", "storage.GetDailyBasicValuations")
	}
	defer rows.Close()

	out := make(map[string]domain.DailyBasicValuation, len(symbols))
	for rows.Next() {
		v, err := scanDailyBasicRow(rows)
		if err != nil {
			return nil, err
		}
		out[v.TsCode] = v
	}
	if err := rows.Err(); err != nil {
		return nil, apperrors.Wrap(err, apperrors.ErrCodeInternal,
			"daily_basic 估值结果集迭代失败", "storage.GetDailyBasicValuations")
	}
	return out, nil
}

// GetDailyBasicHistory 批量读取 [start, end] 区间内的逐日估值（升序）。
//
// 返回 map 只含区间内有行的票。用于 run 预热：策略在回测期间只读这份
// run 内快照，按 `trade_date <= 决策日` 取最近一条，不做运行时查询。
func (s *PostgresStore) GetDailyBasicHistory(
	ctx context.Context, symbols []string, start, end time.Time,
) (map[string][]domain.DailyBasicValuation, error) {
	if len(symbols) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, dailyBasicHistorySQL, symbols, dateOnly(start), dateOnly(end))
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.ErrCodeInternal,
			"daily_basic 估值区间查询失败", "storage.GetDailyBasicHistory")
	}
	defer rows.Close()

	out := make(map[string][]domain.DailyBasicValuation, len(symbols))
	for rows.Next() {
		v, err := scanDailyBasicRow(rows)
		if err != nil {
			return nil, err
		}
		out[v.TsCode] = append(out[v.TsCode], v)
	}
	if err := rows.Err(); err != nil {
		return nil, apperrors.Wrap(err, apperrors.ErrCodeInternal,
			"daily_basic 估值区间结果集迭代失败", "storage.GetDailyBasicHistory")
	}
	return out, nil
}

// scanDailyBasicRow 扫描一行 daily_basic；NULL → nil（缺失≠0）。
func scanDailyBasicRow(rows interface {
	Scan(dest ...any) error
}) (domain.DailyBasicValuation, error) {
	var (
		tsCode    string
		tradeDate time.Time
		peTTM     sql.NullFloat64
		pe        sql.NullFloat64
		pb        sql.NullFloat64
		totalMV   sql.NullFloat64
		circMV    sql.NullFloat64
	)
	if err := rows.Scan(&tsCode, &tradeDate, &peTTM, &pe, &pb, &totalMV, &circMV); err != nil {
		return domain.DailyBasicValuation{}, apperrors.Wrap(err, apperrors.ErrCodeInternal,
			"daily_basic 估值结果行扫描失败", "storage.scanDailyBasicRow")
	}
	return newDailyBasicValuation(tsCode, tradeDate,
		nullFloatPtr(peTTM), nullFloatPtr(pe), nullFloatPtr(pb),
		nullFloatPtr(totalMV), nullFloatPtr(circMV)), nil
}

// nullFloatPtr 把 sql.NullFloat64 转成指针：NULL → nil（缺失≠0）。
func nullFloatPtr(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	f := v.Float64
	return &f
}
