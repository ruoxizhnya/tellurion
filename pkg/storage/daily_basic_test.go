package storage

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ──────────────────────────────────────────────────────────────────────
// 映射层单测（零 DB）：回退可见、NULL 语义、万元→元单点换算
// ──────────────────────────────────────────────────────────────────────

// TestDailyBasicValuation_PETTMPreferredOverPE 锁定第一优先级：pe_ttm 有值就用它。
func TestDailyBasicValuation_PETTMPreferredOverPE(t *testing.T) {
	v := newDailyBasicValuation("000001.SZ", parseDate("2024-01-05"),
		floatPtr(12.5), floatPtr(99.0), floatPtr(1.1),
		floatPtr(10000), floatPtr(8000))

	require.NotNil(t, v.PE)
	assert.Equal(t, 12.5, *v.PE)
	assert.Equal(t, DailyBasicPESourceTTM, v.PESource, "pe_ttm 存在时不得回退")
	assert.True(t, v.HasComparablePE())
}

// TestDailyBasicValuation_PETTMMissingFallsBackToPE 是反例 ①：
// pe_ttm 为 NULL、pe 有值 → 必须走回退，且 PESource 能看出来。
func TestDailyBasicValuation_PETTMMissingFallsBackToPE(t *testing.T) {
	v := newDailyBasicValuation("000002.SZ", parseDate("2024-01-05"),
		nil, floatPtr(8.8), floatPtr(0.9),
		floatPtr(10000), floatPtr(8000))

	require.NotNil(t, v.PE)
	assert.Equal(t, 8.8, *v.PE)
	assert.Equal(t, DailyBasicPESourceReported, v.PESource,
		"回退到 pe 必须能从 provenance 看出来，否则事后分不清用的是哪个口径")
	assert.True(t, v.HasComparablePE())
}

// TestDailyBasicValuation_NullPEAndPETTM_StockDoesNotParticipateInThatDaysScreen
// 是反例 ②：两列都 NULL（亏损股）。
//
// 选定语义：**该票当日不参与筛选**（不是 0、不是极便宜、不是被永久排除）。
// 若把它当 0 或极小值，它会稳定排到价值榜第一。
func TestDailyBasicValuation_NullPEAndPETTM_StockDoesNotParticipateInThatDaysScreen(t *testing.T) {
	v := newDailyBasicValuation("601012.SH", parseDate("2024-01-05"),
		nil, nil, floatPtr(2.2), floatPtr(5000), floatPtr(4000))

	assert.Nil(t, v.PE, "两列皆 NULL 时 PE 必须保持 nil")
	assert.Equal(t, "", v.PESource, "没有可用口径时来源为空")
	assert.False(t, v.HasComparablePE(), "NULL PE 的票当日不参与价值筛选")
	if v.PE != nil {
		t.Errorf("NULL 不能被折成数值：%v", *v.PE)
	}
}

// TestDailyBasicValuation_NullPBKeepsSourceEmpty 覆盖 pb 的 NULL 语义。
func TestDailyBasicValuation_NullPBKeepsSourceEmpty(t *testing.T) {
	v := newDailyBasicValuation("000001.SZ", parseDate("2024-01-05"),
		floatPtr(12.5), floatPtr(12.0), nil, nil, nil)

	assert.Nil(t, v.PB)
	assert.Equal(t, "", v.PBSource)
	assert.False(t, v.HasComparablePB())
	assert.Nil(t, v.TotalMV)
	assert.Nil(t, v.CircMV)
}

// TestDailyBasicValuation_MarketValueWanToYuan 证明市值只在一处从万元换算成元。
func TestDailyBasicValuation_MarketValueWanToYuan(t *testing.T) {
	v := newDailyBasicValuation("000001.SZ", parseDate("2024-01-05"),
		floatPtr(12.5), floatPtr(12.0), floatPtr(1.1),
		floatPtr(12345.6), floatPtr(6000))

	require.NotNil(t, v.TotalMV)
	require.NotNil(t, v.CircMV)
	assert.Equal(t, 12345.6*1e4, *v.TotalMV)
	assert.Equal(t, 6000*1e4, *v.CircMV)
}

// ──────────────────────────────────────────────────────────────────────
// 静态护栏：表名与 PIT 上界不许漂
// ──────────────────────────────────────────────────────────────────────

func TestDailyBasicPITQueryTargetsMarketSchema(t *testing.T) {
	t.Parallel()

	if !strings.Contains(dailyBasicPITBulkSQL, "FROM market.daily_basic") {
		t.Errorf("未显式限定 market schema —— 裸表名会随 search_path 落到不存在的 public：%q", dailyBasicPITBulkSQL)
	}
	if strings.Contains(dailyBasicPITBulkSQL, "FROM daily_basic") {
		t.Errorf("仍出现裸表名 daily_basic：%q", dailyBasicPITBulkSQL)
	}
	if !strings.Contains(dailyBasicPITBulkSQL, "trade_date <= $2") {
		t.Errorf("PIT 上界缺失：必须取 trade_date <= 决策日的最近一条，不许用未来数据：%q", dailyBasicPITBulkSQL)
	}
}

// ──────────────────────────────────────────────────────────────────────
// 有库腿：真实 SQL 证明 PIT + 回退 + NULL
// ──────────────────────────────────────────────────────────────────────

// ensureDailyBasicFixture 建出与 tellurion-data 同形的 market.daily_basic。
// 表归属 data 仓；core 测试只在自己连的库里补夹具，已存在则 no-op。
func ensureDailyBasicFixture(t *testing.T, store *PostgresStore) {
	t.Helper()
	ctx := context.Background()
	if _, err := store.DB().Exec(ctx, `CREATE SCHEMA IF NOT EXISTS market`); err != nil {
		t.Fatalf("[testdb] 建 market schema 失败: %v", err)
	}
	if _, err := store.DB().Exec(ctx, `CREATE TABLE IF NOT EXISTS market.daily_basic (
		ts_code    VARCHAR(20) NOT NULL,
		trade_date DATE NOT NULL,
		pe         DOUBLE PRECISION,
		pe_ttm     DOUBLE PRECISION,
		pb         DOUBLE PRECISION,
		ps         DOUBLE PRECISION,
		ps_ttm     DOUBLE PRECISION,
		dv_ratio   DOUBLE PRECISION,
		total_mv   DOUBLE PRECISION,
		circ_mv    DOUBLE PRECISION,
		CONSTRAINT pk_daily_basic PRIMARY KEY (ts_code, trade_date)
	)`); err != nil {
		t.Fatalf("[testdb] 建 market.daily_basic 夹具失败: %v", err)
	}
}

func TestGetDailyBasicValuations_PITFallbackAndNull(t *testing.T) {
	store := testStore(t)
	defer store.Close()
	ctx := context.Background()
	ensureDailyBasicFixture(t, store)

	const (
		withTTM  = "TEST_DB_VAL_TTM.SZ"
		lossMake = "TEST_DB_VAL_LOSS.SH"
	)
	defer store.DB().Exec(ctx, "DELETE FROM market.daily_basic WHERE ts_code = ANY($1)",
		[]string{withTTM, lossMake})

	// withTTM：01-02 有 pe_ttm；01-05 只有 pe（回退用）；01-10 是未来行（PIT 必须挡住）。
	_, err := store.DB().Exec(ctx, `
		INSERT INTO market.daily_basic (ts_code, trade_date, pe, pe_ttm, pb, total_mv, circ_mv)
		VALUES
			($1, $2, 10.0, 12.0, 1.5, 10000, 8000),
			($1, $3, 20.0, NULL, 2.0, 11000, 9000),
			($1, $4,  5.0,  6.0, 0.8, 12000, 9500)`,
		withTTM, parseDate("2024-01-02"), parseDate("2024-01-05"), parseDate("2024-01-10"))
	require.NoError(t, err)

	// lossMake：01-05 两列 PE 都是 NULL（亏损股），pb 正常。
	_, err = store.DB().Exec(ctx, `
		INSERT INTO market.daily_basic (ts_code, trade_date, pe, pe_ttm, pb, total_mv, circ_mv)
		VALUES ($1, $2, NULL, NULL, 2.2, 5000, 4000)`,
		lossMake, parseDate("2024-01-05"))
	require.NoError(t, err)

	// 决策日 01-04：取 01-02 的 pe_ttm，不许看到 01-05/01-10。
	before, err := store.GetDailyBasicValuations(ctx, []string{withTTM, lossMake}, parseDate("2024-01-04"))
	require.NoError(t, err)
	require.Contains(t, before, withTTM)
	assert.Equal(t, parseDate("2024-01-02"), before[withTTM].TradeDate, "PIT：只能取决策日及之前最近一条")
	require.NotNil(t, before[withTTM].PE)
	assert.Equal(t, 12.0, *before[withTTM].PE)
	assert.Equal(t, DailyBasicPESourceTTM, before[withTTM].PESource)
	assert.NotContains(t, before, lossMake, "决策日之前没有行的票不应出现（缺失≠NULL 行）")

	// 决策日 01-05：withTTM 的 pe_ttm 为 NULL → 回退 pe=20；lossMake 两列都 NULL。
	onDay, err := store.GetDailyBasicValuations(ctx, []string{withTTM, lossMake}, parseDate("2024-01-05"))
	require.NoError(t, err)

	require.Contains(t, onDay, withTTM)
	require.NotNil(t, onDay[withTTM].PE)
	assert.Equal(t, 20.0, *onDay[withTTM].PE, "pe_ttm 缺失必须回退 pe")
	assert.Equal(t, DailyBasicPESourceReported, onDay[withTTM].PESource, "回退必须可见")

	require.Contains(t, onDay, lossMake, "有行但 PE 为 NULL 的票要能返回，供调用方区分「不可比」")
	assert.Nil(t, onDay[lossMake].PE)
	assert.False(t, onDay[lossMake].HasComparablePE(), "NULL PE = 该票当日不参与筛选")
	require.NotNil(t, onDay[lossMake].PB)

	// 单位：市值万元 → 元。
	require.NotNil(t, onDay[withTTM].TotalMV)
	assert.Equal(t, 11000*1e4, *onDay[withTTM].TotalMV)

	// 决策日 01-10：才允许看到 01-10 的 6.0（未来数据在更早的决策日不可见已由上面覆盖）。
	latest, err := store.GetDailyBasicValuations(ctx, []string{withTTM}, parseDate("2024-01-10"))
	require.NoError(t, err)
	require.NotNil(t, latest[withTTM].PE)
	assert.Equal(t, 6.0, *latest[withTTM].PE)
	assert.Equal(t, parseDate("2024-01-10"), latest[withTTM].TradeDate)
}
