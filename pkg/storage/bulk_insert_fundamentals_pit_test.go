package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// D2-R2：ETL 落库路径（BulkInsert）的财报 PIT 完整性。
//
// 背景（TASKS D2-R2，源自 D2-R1 ② 复核发现）：
//   - tushare 财报 connector 的 DataItem.TradeTime = 报告期截止日（end_date），
//     真实披露日经 D2-R1 起透出在 Data["ann_date"]（*time.Time）。
//   - stock_fundamentals.available_date 是生成列
//     COALESCE(ann_date, trade_date)。ETL 路径的列集此前不含 ann_date
//     ⇒ 走 ETL 落库的财报行 ann_date 恒 NULL，available_date 退化成报告期
//     截止日——三季报（9/30 截止、10/25 披露）在 9/30 即可见（前视洞）。
//   - 主任 2026-10-16 裁决 (a1)：补列集（而非 fail-loud 拒收），让生成列
//     自动正确；仅当 ann_date 真缺失时才回落 trade_date（与生产路径
//     SaveFundamentalBatch 同形）。
//
// 本文件钉住四件事：
//   1. buildInsertArgs 对 ann_date 显式判空（typed nil / 零值 / 错类型）；
//   2. mapper 的 fundamentals 列集含 ann_date、不含幻影列；
//   3. 真库端到端：经 BulkInsert 落库的财报行 available_date = 披露日，
//      ann_date 缺失时落 NULL 并回落 trade_date；
//   4. 批量语义：ann_date 错类型的行被跳过并计数，同批好行照常落库。

// ─── 零 DB 单测：buildInsertArgs 的 ann_date 判空语义 ─────────────────────

func fundamentalsCols() (cols, pk []string, numeric []string) {
	m := NewTableMapper()
	return m.columns["fundamentals"], m.pk["fundamentals"], m.numericColumns["fundamentals"]
}

func annDateArgIndex(t *testing.T) int {
	t.Helper()
	cols, _, _ := fundamentalsCols()
	for i, c := range cols {
		if c == "ann_date" {
			return i
		}
	}
	t.Fatal("columns[fundamentals] 不含 ann_date —— D2-R2 的列集补齐被回退了？")
	return -1
}

func TestBuildInsertArgs_FundamentalsAnnDate_TypedNilBecomesNull(t *testing.T) {
	// typed nil：interface 持 *time.Time(nil) ≠ nil，default 分支的
	// `raw == nil` 判不出来——必须显式判空（D2-R2 实现陷阱）。
	cols, pk, numeric := fundamentalsCols()
	var typedNil *time.Time
	p := UnifiedDataPoint{
		Symbol:    "000001.SZ",
		TradeTime: parseDate("2024-09-30"),
		Source:    "tushare",
		Data:      map[string]interface{}{"ts_code": "000001.SZ", "ann_date": typedNil},
	}
	args, err := buildInsertArgs(p, cols, pk, numeric)
	require.NoError(t, err)

	i := annDateArgIndex(t)
	assert.Nil(t, args[i],
		"typed nil *time.Time 必须落 NULL——漏给 pgx 是隐式依赖驱动内部实现，零值更会把 0001-01-01 写进 DATE 列")
}

func TestBuildInsertArgs_FundamentalsAnnDate_MissingKeyBecomesNull(t *testing.T) {
	cols, pk, numeric := fundamentalsCols()
	p := UnifiedDataPoint{
		Symbol:    "000001.SZ",
		TradeTime: parseDate("2024-09-30"),
		Data:      map[string]interface{}{"ts_code": "000001.SZ"},
	}
	args, err := buildInsertArgs(p, cols, pk, numeric)
	require.NoError(t, err)

	i := annDateArgIndex(t)
	assert.Nil(t, args[i], "键缺失（上游未披露）必须落 NULL")
}

func TestBuildInsertArgs_FundamentalsAnnDate_ZeroValueBecomesNull(t *testing.T) {
	// 零值时间不是披露日，是"没填"——写成 0001-01-01 会让 COALESCE 拿到
	// 公元前的值，整行从所有按 available_date 过滤的查询里消失
	// （migration 025 清过一批这种脏数据，见 postgres.go 迁移注释）。
	cols, pk, numeric := fundamentalsCols()
	zero := time.Time{}
	for name, v := range map[string]interface{}{
		"value":   zero,
		"pointer": &zero,
	} {
		t.Run(name, func(t *testing.T) {
			p := UnifiedDataPoint{
				Symbol:    "000001.SZ",
				TradeTime: parseDate("2024-09-30"),
				Data:      map[string]interface{}{"ts_code": "000001.SZ", "ann_date": v},
			}
			args, err := buildInsertArgs(p, cols, pk, numeric)
			require.NoError(t, err)
			assert.Nil(t, args[annDateArgIndex(t)], "零值 ann_date 必须落 NULL")
		})
	}
}

func TestBuildInsertArgs_FundamentalsAnnDate_ValuePassesThrough(t *testing.T) {
	cols, pk, numeric := fundamentalsCols()
	announced := parseDate("2024-10-25")
	p := UnifiedDataPoint{
		Symbol:    "000001.SZ",
		TradeTime: parseDate("2024-09-30"),
		Data:      map[string]interface{}{"ts_code": "000001.SZ", "ann_date": &announced},
	}
	args, err := buildInsertArgs(p, cols, pk, numeric)
	require.NoError(t, err)

	got, ok := args[annDateArgIndex(t)].(time.Time)
	require.True(t, ok, "ann_date arg 应为 time.Time，got %T", args[annDateArgIndex(t)])
	assert.Equal(t, announced, got, "非 nil 披露日必须原值通过（解引用，不折零）")
}

func TestBuildInsertArgs_FundamentalsAnnDate_WrongTypeFailsLoud(t *testing.T) {
	// 接线错误（string/float 冒充披露日）必须 fail-loud：行被跳过并计数，
	// 而不是把垃圾静默写进 DATE 列（与 numericColumns 强转失败同形）。
	cols, pk, numeric := fundamentalsCols()
	for _, bad := range []interface{}{"2024-10-25", 20241025.0} {
		p := UnifiedDataPoint{
			Symbol:    "000001.SZ",
			TradeTime: parseDate("2024-09-30"),
			Data:      map[string]interface{}{"ts_code": "000001.SZ", "ann_date": bad},
		}
		_, err := buildInsertArgs(p, cols, pk, numeric)
		assert.Error(t, err, "ann_date=%v (%T) 应报错", bad, bad)
	}
}

// ─── 零 DB 单测：mapper 列集形态 ───────────────────────────────────────────

func TestNewTableMapper_FundamentalsColumns_Shape(t *testing.T) {
	m := NewTableMapper()
	cols := m.columns["fundamentals"]
	require.NotEmpty(t, cols, "测试本身失效：fundamentals 列集为空")

	set := map[string]bool{}
	for _, c := range cols {
		set[c] = true
	}

	assert.True(t, set["ann_date"],
		"D2-R2：fundamentals 列集必须含 ann_date，否则 available_date 退化成报告期截止日（前视）")

	// 幻影列：真库 stock_fundamentals 没有 source/ingest_time
	// （2026-10-16 information_schema 实测，内联 DDL 同样没有）——写上
	// 每一行 INSERT 都 "column does not exist"，ETL 落库路径从未在真库
	// 跑通过。available_date 是生成列，写了直接报错。
	for _, forbidden := range []string{"source", "ingest_time", "available_date", "created_at"} {
		assert.False(t, set[forbidden],
			"fundamentals 列集不得含 %q（真库无此列 / 生成列不许写）", forbidden)
	}

	// PK 必须与真库 UNIQUE(ts_code, trade_date) 一致，upsert 才有着落。
	assert.Equal(t, []string{"ts_code", "trade_date"}, m.pk["fundamentals"])
}

// ─── 真库集成测试：端到端读回 ─────────────────────────────────────────────

// TestBulkInsert_Fundamentals_AvailableDateEqualsDisclosureDay 是 D2-R2 的
// 核心验收：经 ETL 落库的财报行，available_date 必须是披露日（= 生成列
// COALESCE(ann_date, trade_date) 在 ann_date 已落库时的正确值），且
// ≥ 报告期截止日；披露前的时点不可见。
func TestBulkInsert_Fundamentals_AvailableDateEqualsDisclosureDay(t *testing.T) {
	store := testStore(t)
	defer store.Close()
	ctx := context.Background()

	const tsCode = "TEST_ETL_PIT.SH"
	defer store.DB().Exec(ctx, "DELETE FROM stock_fundamentals WHERE ts_code=$1", tsCode)

	periodEnd := parseDate("2024-09-30") // 报告期截止日（ETL 的 trade_date）
	announced := parseDate("2024-10-25") // 披露日（Data["ann_date"]）

	persisted, skipped, err := store.BulkInsert(ctx, "fundamentals", []UnifiedDataPoint{{
		Symbol:     tsCode,
		TradeTime:  periodEnd,
		Source:     "tushare",
		DataType:   "fundamentals",
		IngestTime: time.Now().UTC(),
		Data: map[string]interface{}{
			"ts_code":  tsCode,
			"pe":       12.5,
			"ann_date": &announced,
		},
	}})
	require.NoError(t, err, "BulkInsert 落库财报行失败——列集与真库 schema 可能又不一致了")
	assert.Equal(t, 1, persisted, "应落库 1 行")
	assert.Equal(t, 0, skipped, "不应跳过任何行")

	var ann, available interface{}
	require.NoError(t, store.DB().QueryRow(ctx, `
		SELECT ann_date, available_date FROM stock_fundamentals WHERE ts_code=$1`, tsCode).
		Scan(&ann, &available))

	require.NotNil(t, ann, "ann_date 必须落库——不写就等于让数据提前可见")
	assert.Equal(t, "2024-10-25", fmtTime(ann), "ann_date 应为披露日")

	require.NotNil(t, available)
	assert.Equal(t, "2024-10-25", fmtTime(available),
		"available_date 必须是披露日；若是报告期截止日，三季报在 9/30 就可见了（前视）")
	assert.False(t, parseTime(available).Before(periodEnd),
		"available_date 不得早于报告期截止日")

	// PIT 端到端：披露前不可见，披露后可见。
	snapBefore, err := store.GetFundamentalsSnapshot(ctx, parseDate("2024-10-01"))
	require.NoError(t, err)
	assert.False(t, containsTsCode(snapBefore, tsCode),
		"10/01（披露前）不应看到 9/30 截止的三季报")

	snapAfter, err := store.GetFundamentalsSnapshot(ctx, parseDate("2024-10-26"))
	require.NoError(t, err)
	assert.True(t, containsTsCode(snapAfter, tsCode), "10/26（披露后）应能看到")
}

// TestBulkInsert_Fundamentals_NilAnnDateLandsNull 钉住 (a1) 的另一半：
// ann_date 缺失（typed nil 或键缺失）时落 NULL、available_date 回落
// trade_date——与生产路径 SaveFundamentalBatch 同形；不 fail-loud 拒收
// 整批，也不用 trade_date 冒充披露日。
func TestBulkInsert_Fundamentals_NilAnnDateLandsNull(t *testing.T) {
	store := testStore(t)
	defer store.Close()
	ctx := context.Background()

	periodEnd := parseDate("2024-09-30")

	cases := []struct {
		name   string
		tsCode string
		data   map[string]interface{}
	}{
		{
			// tushare connector 的 wire 形态：键在、值是 typed nil
			name:   "typed_nil",
			tsCode: "TEST_ETL_NILANN.SH",
			data: map[string]interface{}{
				"ts_code": "TEST_ETL_NILANN.SH", "pe": 10.0,
				"ann_date": (*time.Time)(nil),
			},
		},
		{
			// 更粗的接线：压根没这个键
			name:   "missing_key",
			tsCode: "TEST_ETL_NOANN.SH",
			data: map[string]interface{}{
				"ts_code": "TEST_ETL_NOANN.SH", "pe": 10.0,
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer store.DB().Exec(ctx, "DELETE FROM stock_fundamentals WHERE ts_code=$1", c.tsCode)

			persisted, skipped, err := store.BulkInsert(ctx, "fundamentals", []UnifiedDataPoint{{
				Symbol:     c.tsCode,
				TradeTime:  periodEnd,
				Source:     "tushare",
				DataType:   "fundamentals",
				IngestTime: time.Now().UTC(),
				Data:       c.data,
			}})
			require.NoError(t, err, "%s：ann_date 缺失不应让整批失败", c.name)
			assert.Equal(t, 1, persisted)
			assert.Equal(t, 0, skipped)

			var ann, available interface{}
			require.NoError(t, store.DB().QueryRow(ctx, `
				SELECT ann_date, available_date FROM stock_fundamentals WHERE ts_code=$1`, c.tsCode).
				Scan(&ann, &available))
			assert.Nil(t, ann, "%s：ann_date 缺失必须落 NULL（不是 0001-01-01，也不是 trade_date 冒充）", c.name)
			require.NotNil(t, available)
			assert.Equal(t, "2024-09-30", fmtTime(available),
				"%s：ann_date 为 NULL 时 available_date 回落 trade_date（与生产路径同形）", c.name)

			// 回落之后数据可见（daily_basic 口径的行不能被误伤）。
			snap, err := store.GetFundamentalsSnapshot(ctx, parseDate("2024-10-01"))
			require.NoError(t, err)
			assert.True(t, containsTsCode(snap, c.tsCode),
				"%s：ann_date NULL 回落后应可见，否则误伤", c.name)
		})
	}
}

// TestBulkInsert_Fundamentals_BadAnnDateRowSkippedOthersPersist 钉住
// fail-loud 的批量语义：ann_date 错类型（string/float 冒充披露日）的行在
// 入队前就被 buildInsertArgs 拦下——跳过并计数，同批好行照常落库；
// 不把垃圾静默写进 DATE 列，也不让整批失败。
func TestBulkInsert_Fundamentals_BadAnnDateRowSkippedOthersPersist(t *testing.T) {
	store := testStore(t)
	defer store.Close()
	ctx := context.Background()

	const badCode = "TEST_ETL_BADANN.SH"
	const goodCode = "TEST_ETL_GOODANN.SH"
	defer store.DB().Exec(ctx, "DELETE FROM stock_fundamentals WHERE ts_code=$1", badCode)
	defer store.DB().Exec(ctx, "DELETE FROM stock_fundamentals WHERE ts_code=$1", goodCode)

	announced := parseDate("2024-10-25")
	persisted, skipped, err := store.BulkInsert(ctx, "fundamentals", []UnifiedDataPoint{
		{
			Symbol:     badCode,
			TradeTime:  parseDate("2024-09-30"),
			Source:     "tushare",
			DataType:   "fundamentals",
			IngestTime: time.Now().UTC(),
			Data: map[string]interface{}{
				"ts_code": badCode, "pe": 10.0,
				"ann_date": "2024-10-25", // string 冒充披露日 → fail-loud
			},
		},
		{
			Symbol:     goodCode,
			TradeTime:  parseDate("2024-09-30"),
			Source:     "tushare",
			DataType:   "fundamentals",
			IngestTime: time.Now().UTC(),
			Data: map[string]interface{}{
				"ts_code": goodCode, "pe": 10.0,
				"ann_date": &announced,
			},
		},
	})
	require.NoError(t, err, "一行坏数据不应让整批失败")
	assert.Equal(t, 1, persisted, "好行应落库")
	assert.Equal(t, 1, skipped, "坏行应被跳过并计数")

	// 坏行没有落库（不是写了一行垃圾）。
	var n int
	require.NoError(t, store.DB().QueryRow(ctx,
		"SELECT count(*) FROM stock_fundamentals WHERE ts_code=$1", badCode).Scan(&n))
	assert.Equal(t, 0, n, "ann_date 错类型的行不得落库")

	// 好行照常：available_date = 披露日。
	var available interface{}
	require.NoError(t, store.DB().QueryRow(ctx,
		"SELECT available_date FROM stock_fundamentals WHERE ts_code=$1", goodCode).Scan(&available))
	assert.Equal(t, "2024-10-25", fmtTime(available), "好行的 available_date 应为披露日")
}

func parseTime(v interface{}) time.Time {
	ts, ok := v.(time.Time)
	if !ok {
		return time.Time{}
	}
	return ts
}
