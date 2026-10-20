package storage

// DBT-01 / DBT-02 的测试。
//
// DBT-01：ohlcv_daily mapper 的幻影列（source/ingest_time——真库
// ohlcv_daily_qfq 没有这两列，ETL 每行 INSERT 必报 42703）。三层防护：
//  1. 形态单测（无 DB）：mapper 列集与真实 DDL 列集逐列一致、PK 正确、
//     禁含幻影列——加回任一幻影列即红；
//  2. 真库集成：一行日线经 BulkInsert 落库成功（修复前每行必失败）。
//
// DBT-02：fundamentals 快照 tiebreak。ETL 行 end_date 为 NULL，PG 的
// `DESC` 默认 NULLS FIRST ⇒ 多期同 available_date 时选错期。裁决 (b)：
// 查询显式 NULLS LAST。真库集成：同 available_date 两行（一 NULL 一非
// NULL），断言快照选非 NULL 期。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/testenv"
)

// ohlcvDailyRealColumns 是 ohlcv_daily_qfq 的真实列集（双重真相源核对产物：
// 2026-10-17 information_schema 实测 == pkg/storage/postgres.go 内联 DDL）。
// 有意**不**包含 created_at（DEFAULT 惯例不写，与 mapper 口径一致）。
var ohlcvDailyRealColumns = []string{
	"symbol", "trade_date", "open", "high", "low", "close",
	"volume", "turnover", "trade_days",
}

func TestDBT01_OhlcvDailyMapper_MatchesRealDDL(t *testing.T) {
	t.Parallel()

	m := NewTableMapper()
	cols := m.columns["ohlcv_daily"]

	// ① 禁含幻影列（历史幻影名单 + 通用 created_at/生成列惯例）。
	phantoms := []string{"source", "ingest_time", "created_at", "available_date"}
	for _, ph := range phantoms {
		for _, c := range cols {
			assert.NotEqual(t, ph, c,
				"mapper 列集含 %q——ohlcv_daily_qfq 真库没有该列，ETL 每行 INSERT 必失败（DBT-01）", ph)
		}
	}

	// ② 列集与真实 DDL 一致（集合相等，顺序无关但数量必须正好）。
	assert.ElementsMatch(t, ohlcvDailyRealColumns, cols,
		"mapper 列集必须与 ohlcv_daily_qfq 真实 DDL 逐列对齐（DBT-01）")

	// ③ PK 正确。
	assert.Equal(t, []string{"symbol", "trade_date"}, m.pk["ohlcv_daily"])
}

func TestDBT01_BulkInsertDailyRow_LandsInRealDB(t *testing.T) {
	dsn := testenv.DatabaseDSN()
	if dsn == "" {
		testenv.SkipNoDB(t)
	}
	store, err := NewPostgresStoreConnected(context.Background(), dsn)
	if err != nil {
		t.Fatalf("[testdb] TEST_DATABASE_DSN 已设置但连不上（fail-loud）: %v", err)
	}
	t.Cleanup(store.Close)
	ctx := context.Background()

	sym := "DBT01TEST.SH"
	t.Cleanup(func() {
		_, _ = store.pool.Exec(ctx, `DELETE FROM ohlcv_daily_qfq WHERE symbol = $1`, sym)
	})

	day := time.Date(2099, 1, 2, 0, 0, 0, 0, time.UTC)
	points := []UnifiedDataPoint{{
		Symbol:    sym,
		TradeTime: day,
		Source:    "etl-test",
		DataType:  "ohlcv_daily",
		Data: map[string]interface{}{
			"symbol": sym, "trade_date": day,
			"open": 10.0, "high": 10.5, "low": 9.8, "close": 10.2,
			"volume": 1000000.0, "turnover": 10200000.0, "trade_days": 1.0,
		},
	}}

	inserted, failed, err := store.BulkInsert(ctx, "ohlcv_daily", points)
	require.NoError(t, err, "修复前：幻影列 source/ingest_time 让每行 INSERT 必报 42703")
	assert.Zero(t, failed)
	assert.Equal(t, 1, inserted)

	// 落库值核对。
	var closePx float64
	err = store.pool.QueryRow(ctx,
		`SELECT close FROM ohlcv_daily_qfq WHERE symbol = $1 AND trade_date = $2`,
		sym, day).Scan(&closePx)
	require.NoError(t, err)
	assert.InDelta(t, 10.2, closePx, 1e-9)
}

func TestDBT02_FundamentalsTiebreak_PrefersKnownEndDate(t *testing.T) {
	dsn := testenv.DatabaseDSN()
	if dsn == "" {
		testenv.SkipNoDB(t)
	}
	store, err := NewPostgresStoreConnected(context.Background(), dsn)
	if err != nil {
		t.Fatalf("[testdb] TEST_DATABASE_DSN 已设置但连不上（fail-loud）: %v", err)
	}
	t.Cleanup(store.Close)
	ctx := context.Background()

	sym := "DBT02TEST.SH"
	annDate := time.Date(2099, 2, 1, 0, 0, 0, 0, time.UTC)  // 同一披露日 ⇒ 同 available_date
	tradeQ3 := time.Date(2098, 9, 30, 0, 0, 0, 0, time.UTC) // Q3 报告期（生产行：end_date 已知）
	tradeQ2 := time.Date(2098, 6, 30, 0, 0, 0, 0, time.UTC) // Q2 报告期（ETL 行：end_date NULL）

	// 直接 SQL 造行（绕过 wire——这正是要测的场景：ETL 落的行 end_date NULL）。
	// 先清后清各一次：上次失败运行可能残留（PK (ts_code, trade_date) 冲突
	// 会把本次 Exec 炸掉，报因失真）。
	_, _ = store.pool.Exec(ctx, `DELETE FROM stock_fundamentals WHERE ts_code = $1`, sym)
	t.Cleanup(func() {
		_, _ = store.pool.Exec(ctx, `DELETE FROM stock_fundamentals WHERE ts_code = $1`, sym)
	})
	var nullEnd *time.Time // typed nil：pgx 编码为 NULL
	// 两条独立 Exec：多行 VALUES 的参数类型推断在 pgx 里对 NULL 参数有
	// 歧义（"Parameters must be numerical"），拆开后各行走目标列推断。
	_, err = store.pool.Exec(ctx, `
		INSERT INTO stock_fundamentals (ts_code, trade_date, ann_date, end_date, pe)
		VALUES ($1, $2, $3, $4, 10.0)`, // ETL 形态：end_date NULL
		sym, tradeQ2, annDate, nullEnd)
	require.NoError(t, err)
	_, err = store.pool.Exec(ctx, `
		INSERT INTO stock_fundamentals (ts_code, trade_date, ann_date, end_date, pe)
		VALUES ($1, $2, $3, $4, 20.0)`, // 生产形态：end_date 已知（Q3）
		sym, tradeQ3, annDate, tradeQ3)
	require.NoError(t, err)

	// asOf 取披露日当天：两行同 available_date，tiebreak 全靠 end_date。
	// 修复前（NULLS FIRST）：NULL end_date 的 ETL 行被选中 ⇒ pe=10（错期）。
	// 修复后（NULLS LAST）：有期信息的 Q3 行先选 ⇒ pe=20。
	got, err := store.GetFundamental(ctx, sym, annDate)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.PE, "被选中的行必须有 PE")
	// 注意：InDelta 对 *float64 会报 "Parameters must be numerical"（testify
	// 的数值参数检查），必须解引用——这个报错有误导性，特此留痕。
	assert.InDelta(t, 20.0, *got.PE, 1e-9,
		"多期同可用日必须选 end_date 已知（最新报告期）的行——NULLS LAST（DBT-02）")
}
