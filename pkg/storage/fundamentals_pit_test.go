package storage

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/domain"
)

// 前视偏差（point-in-time）回归测试。
//
// 背景：
//   - tushare `fina_indicator` 返回的是「报告期 end_date + 披露日 ann_date」，
//     接口字段里并没有 trade_date（见 pkg/data/tushare.go:412 的 fields 列表）。
//   - pkg/data/tushare.go:457 的注释写着 "Use end_date as trade_date"，
//     于是报告期截止日被塞进了 trade_date 列。
//   - 如果读取端按 trade_date 过滤，三季报（9/30 截止、10/25 披露）在 9/30
//     当天就对回测可见 —— 典型 look-ahead bias，回测结果会系统性虚高。
//
// 正确规则：一条财务记录的可用日 = available_date（= COALESCE(ann_date, trade_date)，
// P1-4 起是独立的列，所有读取一律用它 —— 见 pkg/storage/fundamentals.go 顶部）
//   - ann_date 有值（fina_indicator 路径）→ 披露日才是可用日
//   - ann_date 为 NULL（daily_basic 路径，PE/PB 本身已是当日口径）→ 用 trade_date

func containsTsCode(rows []domain.FundamentalData, tsCode string) bool {
	for _, r := range rows {
		if r.TsCode == tsCode {
			return true
		}
	}
	return false
}

// TestGetFundamentalsSnapshot_PIT_NotVisibleBeforeAnnouncement 是核心回归用例：
// 披露日之前，任何时点都不得看到该期财务数据。
func TestGetFundamentalsSnapshot_PIT_NotVisibleBeforeAnnouncement(t *testing.T) {
	store := testStore(t)
	defer store.Close()
	ctx := context.Background()

	const tsCode = "TEST_PIT_LOOK.SH"
	defer store.DB().Exec(ctx, "DELETE FROM stock_fundamentals WHERE ts_code=$1", tsCode)

	// 三季报：报告期 9/30 截止，10/25 才对外披露。
	row := &domain.FundamentalData{
		TsCode:    tsCode,
		TradeDate: parseDate("2024-09-30"), // ETL 用 end_date 顶替 trade_date
		AnnDate:   parseDate("2024-10-25"),
		EndDate:   parseDate("2024-09-30"),
		PE:        floatPtr(12.0),
		PB:        floatPtr(1.5),
	}
	require.NoError(t, store.SaveFundamentalData(ctx, row))

	// 9/30：报告期最后一天，但公司尚未披露 —— 不可见。
	onPeriodEnd, err := store.GetFundamentalsSnapshot(ctx, parseDate("2024-09-30"))
	require.NoError(t, err)
	assert.False(t, containsTsCode(onPeriodEnd, tsCode),
		"前视偏差：9/30 是报告期截止日，但财报 10/25 才披露，此刻不该可见")

	// 10/01：报告期已过、仍未披露 —— 依然不可见。
	afterPeriodEnd, err := store.GetFundamentalsSnapshot(ctx, parseDate("2024-10-01"))
	require.NoError(t, err)
	assert.False(t, containsTsCode(afterPeriodEnd, tsCode),
		"前视偏差：10/01 尚未到披露日 10/25，不该可见")

	// 10/24：披露前一天 —— 不可见。
	dayBeforeAnn, err := store.GetFundamentalsSnapshot(ctx, parseDate("2024-10-24"))
	require.NoError(t, err)
	assert.False(t, containsTsCode(dayBeforeAnn, tsCode),
		"前视偏差：10/24 距披露仅一天，仍然不该可见")

	// 10/25：披露当日 —— 可见。
	onAnnDate, err := store.GetFundamentalsSnapshot(ctx, parseDate("2024-10-25"))
	require.NoError(t, err)
	assert.True(t, containsTsCode(onAnnDate, tsCode),
		"10/25 已披露，应当可见")

	// 12/31：披露之后 —— 可见。
	yearEnd, err := store.GetFundamentalsSnapshot(ctx, parseDate("2024-12-31"))
	require.NoError(t, err)
	assert.True(t, containsTsCode(yearEnd, tsCode),
		"12/31 早已披露，应当可见")
}

// TestGetFundamentalsSnapshot_PIT_NullAnnDateFallsBackToTradeDate 保护 daily_basic 路径：
// 该路径不写 ann_date，数值本身已是当日口径，必须回退到 trade_date，否则会误伤。
func TestGetFundamentalsSnapshot_PIT_NullAnnDateFallsBackToTradeDate(t *testing.T) {
	store := testStore(t)
	defer store.Close()
	ctx := context.Background()

	const tsCode = "TEST_PIT_NULLANN.SH"
	defer store.DB().Exec(ctx, "DELETE FROM stock_fundamentals WHERE ts_code=$1", tsCode)

	// 模拟 daily_basic 写入（SaveFundamentalBatch 的路径）：ann_date 为 NULL，
	// trade_date 是真实交易日，PE 已是该日收盘口径。
	_, err := store.DB().Exec(ctx, `
		INSERT INTO stock_fundamentals (ts_code, trade_date, end_date, pe, pb)
		VALUES ($1, $2, $2, 12.0, 1.5)
		ON CONFLICT (ts_code, trade_date) DO UPDATE SET pe = EXCLUDED.pe`,
		tsCode, parseDate("2024-09-30"))
	require.NoError(t, err)

	got, err := store.GetFundamentalsSnapshot(ctx, parseDate("2024-10-01"))
	require.NoError(t, err)
	assert.True(t, containsTsCode(got, tsCode),
		"ann_date 为 NULL 时必须回退到 trade_date，否则会误伤 daily_basic 路径")

	// 早于 trade_date 依然不可见。
	before, err := store.GetFundamentalsSnapshot(ctx, parseDate("2024-09-29"))
	require.NoError(t, err)
	assert.False(t, containsTsCode(before, tsCode),
		"早于 trade_date 不应可见")
}

// TestGetFundamentals_PIT_NotVisibleBeforeAnnouncement 覆盖单标的查询接口。
func TestGetFundamentals_PIT_NotVisibleBeforeAnnouncement(t *testing.T) {
	store := testStore(t)
	defer store.Close()
	ctx := context.Background()

	const tsCode = "TEST_PIT_SINGLE.SH"
	defer store.DB().Exec(ctx, "DELETE FROM stock_fundamentals WHERE ts_code=$1", tsCode)

	row := &domain.FundamentalData{
		TsCode:    tsCode,
		TradeDate: parseDate("2024-09-30"),
		AnnDate:   parseDate("2024-10-25"),
		EndDate:   parseDate("2024-09-30"),
		PE:        floatPtr(12.0),
	}
	require.NoError(t, store.SaveFundamentalData(ctx, row))

	before, err := store.GetFundamentals(ctx, tsCode, parseDate("2024-10-01"))
	require.NoError(t, err)
	assert.Empty(t, before, "前视偏差：单标查询在披露日前不应返回记录")

	after, err := store.GetFundamentals(ctx, tsCode, parseDate("2024-10-25"))
	require.NoError(t, err)
	assert.Len(t, after, 1, "披露日之后应返回该条记录")
}

// TestGetFundamentalsSnapshot_PIT_PicksLatestDisclosedPeriod 验证：
// 同一时点若有多期已披露，应取最近一期，而不是报告期最大的一期。
func TestGetFundamentalsSnapshot_PIT_PicksLatestDisclosedPeriod(t *testing.T) {
	store := testStore(t)
	defer store.Close()
	ctx := context.Background()

	const tsCode = "TEST_PIT_MULTI.SH"
	defer store.DB().Exec(ctx, "DELETE FROM stock_fundamentals WHERE ts_code=$1", tsCode)

	peQ2, peQ3 := 10.0, 20.0
	require.NoError(t, store.SaveFundamentalData(ctx, &domain.FundamentalData{
		TsCode:    tsCode,
		TradeDate: parseDate("2024-06-30"),
		AnnDate:   parseDate("2024-08-20"),
		EndDate:   parseDate("2024-06-30"),
		PE:        &peQ2,
	}))
	require.NoError(t, store.SaveFundamentalData(ctx, &domain.FundamentalData{
		TsCode:    tsCode,
		TradeDate: parseDate("2024-09-30"),
		AnnDate:   parseDate("2024-10-25"),
		EndDate:   parseDate("2024-09-30"),
		PE:        &peQ3,
	}))

	// 10/01：只有半年报（8/20 披露）可见，三季报尚未披露。
	got, err := store.GetFundamentalsSnapshot(ctx, parseDate("2024-10-01"))
	require.NoError(t, err)
	var found *domain.FundamentalData
	for i := range got {
		if got[i].TsCode == tsCode {
			found = &got[i]
			break
		}
	}
	require.NotNil(t, found, "10/01 应能看到 8/20 已披露的半年报")
	require.NotNil(t, found.PE)
	assert.Equal(t, 10.0, *found.PE, "10/01 应取半年报 PE=10，而不是未披露的三季报 PE=20")

	// 10/25 之后：应更新为三季报。
	later, err := store.GetFundamentalsSnapshot(ctx, parseDate("2024-12-31"))
	require.NoError(t, err)
	var latest *domain.FundamentalData
	for i := range later {
		if later[i].TsCode == tsCode {
			latest = &later[i]
			break
		}
	}
	require.NotNil(t, latest)
	require.NotNil(t, latest.PE)
	assert.Equal(t, 20.0, *latest.PE, "12/31 应取最近披露的三季报 PE=20")
}

// ─── P1-4：路径 A 的 ann_date 与 available_date 生成列 ─────────────────
//
// P0-1 的 COALESCE(ann_date, trade_date) 只是把洞盖住了：路径 A
//（FetchFundamentals → normalizeFundamentals）压根不写 ann_date，COALESCE
// 对它退化成报告期截止日，前视偏差照旧。

// TestSaveFundamentalBatch_PathA_WritesAnnDate：路径 A 必须把披露日写进去。
//
// 修之前，同一份 API 响应经两个归一化函数处理后结果不同：一个丢 ann_date，
// 一个不丢。丢的那个让整行数据提前约一个月可见。
func TestSaveFundamentalBatch_PathA_WritesAnnDate(t *testing.T) {
	store := testStore(t)
	defer store.Close()
	ctx := context.Background()

	const tsCode = "TEST_PATHA.SH"
	defer store.DB().Exec(ctx, "DELETE FROM stock_fundamentals WHERE ts_code=$1", tsCode)

	periodEnd := parseDate("2024-09-30")
	announced := parseDate("2024-10-25")
	require.NoError(t, store.SaveFundamentalBatch(ctx, []*domain.Fundamental{
		{
			Symbol:  tsCode,
			Date:    periodEnd, // 路径 A 的 Date 是报告期截止日（既有行为）
			AnnDate: &announced,
			PE:      float64PtrOf(12),
		},
	}))

	var available, ann interface{}
	err := store.DB().QueryRow(ctx, `
		SELECT available_date, ann_date FROM stock_fundamentals WHERE ts_code=$1`, tsCode).
		Scan(&available, &ann)
	require.NoError(t, err)
	require.NotNil(t, ann, "路径 A 必须把 ann_date 写进去 —— 不写就等于让数据提前可见")
	require.NotNil(t, available)

	// 关键断言：可用日是披露日，**不是**报告期截止日。
	assert.Equal(t, announced.Format("2006-01-02"), fmtTime(available),
		"可用日必须是披露日；若是报告期截止日，三季报在 9/30 就可见了")

	// 截止日当天不可见，披露后才可见。
	snapBefore, err := store.GetFundamentalsSnapshot(ctx, parseDate("2024-10-01"))
	require.NoError(t, err)
	assert.False(t, containsTsCode(snapBefore, tsCode),
		"10/01（披露前）不应看到 9/30 截止的三季报")

	snapAfter, err := store.GetFundamentalsSnapshot(ctx, parseDate("2024-10-26"))
	require.NoError(t, err)
	assert.True(t, containsTsCode(snapAfter, tsCode), "10/26（披露后）应能看到")
}

// TestAvailableDate_IsGeneratedColumn：available_date 必须是生成列。
//
// 为什么非得是生成列：普通列要靠每个写入函数记得填，而任何绕过写入函数的
// 路径（手工 INSERT、修数、别的 ETL）都会留下一行 available_date 为 NULL
// 的数据 —— 它随后从所有按可用日过滤的查询里**凭空消失**，而且看不出为什么。
//
// 这个测试防的是它被悄悄换回普通列（例如某次改动把 GENERATED 写漏了，
// 而 ADD COLUMN IF NOT EXISTS 会因为列已存在而静默跳过）。
func TestAvailableDate_IsGeneratedColumn(t *testing.T) {
	store := testStore(t)
	defer store.Close()
	ctx := context.Background()

	var generation string
	err := store.DB().QueryRow(ctx, `
		SELECT coalesce(generation_expression, '') FROM information_schema.columns
		WHERE table_name='stock_fundamentals' AND column_name='available_date'`).Scan(&generation)
	require.NoError(t, err)
	require.NotEmpty(t, generation,
		"available_date 必须是生成列 —— 普通列会让绕过写入函数的路径留下 NULL，数据凭空消失")
	assert.Contains(t, generation, "ann_date")

	// 端到端验证：裸 INSERT（完全不提 available_date），它照样有值。
	const tsCode = "TEST_GENCOL.SH"
	defer store.DB().Exec(ctx, "DELETE FROM stock_fundamentals WHERE ts_code=$1", tsCode)

	_, err = store.DB().Exec(ctx, `
		INSERT INTO stock_fundamentals (ts_code, trade_date, end_date, pe, pb)
		VALUES ($1, $2, $2, 12.0, 1.5)`, tsCode, parseDate("2024-09-30"))
	require.NoError(t, err)

	var available interface{}
	require.NoError(t, store.DB().QueryRow(ctx, `
		SELECT available_date FROM stock_fundamentals WHERE ts_code=$1`, tsCode).Scan(&available))
	assert.Equal(t, "2024-09-30", fmtTime(available),
		"裸 INSERT 也应该拿到可用日（生成列自动算）")
}

// ─── 2026-11-12：PIT 读口切到 data 仓的 market.stock_fundamentals ─────────
//
// 背景：`stock_fundamentals` 的唯一写者已移到 tellurion-data（ADR-0012），
// 物理表是 `market.stock_fundamentals`；core 的 `public.stock_fundamentals`
// 写点随 `pkg/data` 退役、实测 0 行。PIT 查询此前裸写表名，按 search_path
// 落到 public 空表上 —— 返回**空结果而不是报错**，回测静默退化成「无财报」。

// TestFundamentalsPITQueriesTargetMarketSchema 是零 DB 的静态护栏：
// 两条 PIT 查询必须显式读 `market.stock_fundamentals`，不得再出现裸表名。
func TestFundamentalsPITQueriesTargetMarketSchema(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"GetFundamentalsPIT":     fundamentalsPITSQL,
		"GetFundamentalsPITBulk": fundamentalsPITBulkSQL,
	}
	for name, query := range cases {
		if !strings.Contains(query, "FROM market.stock_fundamentals") {
			t.Errorf("%s 未显式限定 market schema —— 裸表名会随 search_path 落到空表 public：%q", name, query)
		}
		if strings.Contains(query, "FROM stock_fundamentals") {
			t.Errorf("%s 仍出现裸表名 stock_fundamentals：%q", name, query)
		}
	}
}

// TestGetFundamentalsPIT_ReadsMarketSchema 是行为腿：只往 market 写，PIT 入口
// 必须读得到；public 那张空表不参与。切 schema 之前这条是红的（读 public ⇒ 0 行）。
func TestGetFundamentalsPIT_ReadsMarketSchema(t *testing.T) {
	store := testStore(t)
	defer store.Close()
	ctx := context.Background()

	if _, err := store.DB().Exec(ctx, `CREATE SCHEMA IF NOT EXISTS market`); err != nil {
		t.Fatalf("[testdb] 建 market schema 失败: %v", err)
	}
	// 形状逐字镜像 tellurion-data:internal/schema/market.go 的 market.stock_fundamentals：
	// 无 id / created_at，available_date 是生成列。
	if _, err := store.DB().Exec(ctx, `CREATE TABLE IF NOT EXISTS market.stock_fundamentals (
		ts_code VARCHAR(20) NOT NULL,
		trade_date DATE NOT NULL,
		ann_date DATE,
		end_date DATE,
		pe DOUBLE PRECISION,
		pb DOUBLE PRECISION,
		ps DOUBLE PRECISION,
		roe DOUBLE PRECISION,
		roa DOUBLE PRECISION,
		debt_to_equity DOUBLE PRECISION,
		gross_margin DOUBLE PRECISION,
		net_margin DOUBLE PRECISION,
		revenue DOUBLE PRECISION,
		net_profit DOUBLE PRECISION,
		total_assets DOUBLE PRECISION,
		total_liab DOUBLE PRECISION,
		available_date DATE GENERATED ALWAYS AS (COALESCE(ann_date, trade_date)) STORED,
		CONSTRAINT pk_stock_fundamentals PRIMARY KEY (ts_code, trade_date)
	)`); err != nil {
		t.Fatalf("[testdb] 建 market.stock_fundamentals 夹具失败: %v", err)
	}

	const tsCode = "TEST_PIT_MARKET.SH"
	defer store.DB().Exec(ctx, "DELETE FROM market.stock_fundamentals WHERE ts_code=$1", tsCode)

	// 三季报：报告期 9/30 截止、10/25 披露 —— 可用日必须是 10/25。
	_, err := store.DB().Exec(ctx, `
		INSERT INTO market.stock_fundamentals (ts_code, trade_date, ann_date, end_date, pe, pb, roe)
		VALUES ($1, $2, $3, $2, 12.0, 1.5, 8.0)`,
		tsCode, parseDate("2024-09-30"), parseDate("2024-10-25"))
	require.NoError(t, err)

	before, err := store.GetFundamentalsPIT(ctx, tsCode, parseDate("2024-10-24"))
	require.NoError(t, err)
	assert.Empty(t, before, "披露前不可见；若这里为空是因为读错表，说明 schema 又漂回 public 了")

	got, err := store.GetFundamentalsPIT(ctx, tsCode, parseDate("2024-10-25"))
	require.NoError(t, err)
	require.Len(t, got, 1, "market.stock_fundamentals 唯一写者已就绪，PIT 必须读到它")
	assert.Equal(t, tsCode, got[0].Symbol)
	assert.Equal(t, parseDate("2024-10-25"), got[0].Date, "Date 必须是可用日（available_date）")
	require.NotNil(t, got[0].PE)
	assert.Equal(t, 12.0, *got[0].PE)

	bulk, err := store.GetFundamentalsPITBulk(ctx, []string{tsCode}, parseDate("2024-10-25"))
	require.NoError(t, err)
	require.Len(t, bulk[tsCode], 1, "回测预热走的是 Bulk 入口，同样必须读到 market 表")
}

// TestSaveFundamentalData_ZeroAnnDateBecomesNull：零值公告日必须写成 NULL。
//
// domain.FundamentalData.AnnDate 是 time.Time 而非指针，缺字段时是零值。
// 原样写库会让 COALESCE 拿到 0001-01-01 —— 整行数据从此不可见，
// 而且看不出为什么少了它。
func TestSaveFundamentalData_ZeroAnnDateBecomesNull(t *testing.T) {
	store := testStore(t)
	defer store.Close()
	ctx := context.Background()

	const tsCode = "TEST_ZEROANN.SH"
	defer store.DB().Exec(ctx, "DELETE FROM stock_fundamentals WHERE ts_code=$1", tsCode)

	require.NoError(t, store.SaveFundamentalData(ctx, &domain.FundamentalData{
		TsCode:    tsCode,
		TradeDate: parseDate("2024-09-30"),
		EndDate:   parseDate("2024-09-30"),
		PE:        float64PtrOf(12),
	}))

	var ann interface{}
	require.NoError(t, store.DB().QueryRow(ctx, `
		SELECT ann_date FROM stock_fundamentals WHERE ts_code=$1`, tsCode).Scan(&ann))
	assert.Nil(t, ann, "零值 ann_date 必须写成 NULL，否则 COALESCE 会拿到 0001-01-01")

	// 而且回退到 trade_date 之后这条数据应当是可见的。
	snap, err := store.GetFundamentalsSnapshot(ctx, parseDate("2024-10-01"))
	require.NoError(t, err)
	assert.True(t, containsTsCode(snap, tsCode))
}

func fmtTime(v interface{}) string {
	t, ok := v.(time.Time)
	if !ok {
		return ""
	}
	return t.Format("2006-01-02")
}

func float64PtrOf(v float64) *float64 { return &v }
