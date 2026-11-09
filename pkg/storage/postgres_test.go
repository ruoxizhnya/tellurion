package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"tellurion/pkg/domain"
	"tellurion/pkg/testenv"
)

// SVC-CORE-DATA-RETIRE：本文件原先覆盖 ohlcv_daily_qfq / stocks /
// trading_calendar / index_constituents 的读写，随这四张表一起退役
// （仓储层方法与内联 DDL 均已删除，数据面唯一 owner 移到本仓之外）。
// 剩下的是仍由本仓持有的表：stock_fundamentals 与 fundamentals_detail。

func testStore(t *testing.T) *PostgresStore {
	t.Helper()
	ctx := context.Background()
	dsn := testenv.DatabaseDSN()
	if dsn == "" {
		testenv.SkipNoDB(t)
	}
	store, err := NewPostgresStoreConnected(ctx, dsn)
	if err != nil {
		t.Fatalf("[testdb] TEST_DATABASE_DSN 已设置但连不上（fail-loud）: %v", err)
	}
	return store
}

func TestNewPostgresStoreConnected_Connects(t *testing.T) {
	// SkipIfNoDB convention — if the local Postgres is not running,
	// skip the test rather than fail. Matches testStore() helper pattern.
	store := testStore(t)
	require.NotNil(t, store)
	store.Close()
}

func TestPostgresStore_Ping(t *testing.T) {
	store := testStore(t)
	defer store.Close()

	ctx := context.Background()
	err := store.Ping(ctx)
	assert.NoError(t, err)
}

func TestPostgresStore_DB(t *testing.T) {
	store := testStore(t)
	defer store.Close()

	db := store.DB()
	assert.NotNil(t, db)
}

func parseDate(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t
}

func TestSaveFundamentalData_and_GetFundamentalDataLatest(t *testing.T) {
	store := testStore(t)
	defer store.Close()
	ctx := context.Background()

	pe := 15.0
	fd := &domain.FundamentalData{
		TsCode:    "TEST_FD_001.SH",
		TradeDate: parseDate("2024-03-31"),
		AnnDate:   parseDate("2024-04-15"),
		EndDate:   parseDate("2024-03-31"),
		PE:        &pe,
		PB:        floatPtr(1.2),
		PS:        floatPtr(0.8),
		ROE:       floatPtr(0.12),
	}

	err := store.SaveFundamentalData(ctx, fd)
	require.NoError(t, err)

	result, err := store.GetFundamentalDataLatest(ctx, "TEST_FD_001.SH")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "TEST_FD_001.SH", result.TsCode)
	assert.NotNil(t, result.PE)
	assert.Equal(t, 15.0, *result.PE)

	// Cleanup
	store.DB().Exec(ctx, "DELETE FROM stock_fundamentals WHERE ts_code='TEST_FD_001.SH'")
}

func TestSaveFundamentalDataBatch(t *testing.T) {
	store := testStore(t)
	defer store.Close()
	ctx := context.Background()

	records := []*domain.FundamentalData{
		{TsCode: "TEST_FD_BATCH.SH", TradeDate: parseDate("2024-03-31"), AnnDate: parseDate("2024-04-15"), EndDate: parseDate("2024-03-31")},
		{TsCode: "TEST_FD_BATCH.SH", TradeDate: parseDate("2024-06-30"), AnnDate: parseDate("2024-07-15"), EndDate: parseDate("2024-06-30")},
	}

	err := store.SaveFundamentalDataBatch(ctx, records)
	require.NoError(t, err)

	history, err := store.GetFundamentalDataHistory(ctx, "TEST_FD_BATCH.SH",
		timePtr(parseDate("2024-01-01")), timePtr(parseDate("2024-12-31")))
	require.NoError(t, err)
	assert.Len(t, history, 2)

	// Cleanup
	store.DB().Exec(ctx, "DELETE FROM stock_fundamentals WHERE ts_code='TEST_FD_BATCH.SH'")
}

func TestGetFundamentalDataHistory(t *testing.T) {
	store := testStore(t)
	defer store.Close()
	ctx := context.Background()

	// Use a real symbol with data
	history, err := store.GetFundamentalDataHistory(ctx, "600000.SH",
		timePtr(parseDate("2023-01-01")), timePtr(parseDate("2024-12-31")))
	require.NoError(t, err)
	// Real data might have 4+ quarterly records
	assert.GreaterOrEqual(t, len(history), 0) // just check it runs without error
}

func TestScreenFundamentals_Integration(t *testing.T) {
	store := testStore(t)
	defer store.Close()
	ctx := context.Background()

	// Insert test data first
	pe := 12.0
	roe := 0.15
	fd := &domain.FundamentalData{
		TsCode:    "TEST_SCREEN_001.SH",
		TradeDate: parseDate("2024-03-31"),
		AnnDate:   parseDate("2024-04-15"),
		EndDate:   parseDate("2024-03-31"),
		PE:        &pe,
		ROE:       &roe,
	}
	store.SaveFundamentalData(ctx, fd)

	// Run screen
	peMax := 20.0
	roeMin := 0.10
	filters := domain.ScreenFilters{PE_max: &peMax, ROE_min: &roeMin}
	results, err := store.ScreenFundamentals(ctx, filters, nil, 10)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(results), 1)

	// Cleanup
	store.DB().Exec(ctx, "DELETE FROM stock_fundamentals WHERE ts_code='TEST_SCREEN_001.SH'")
}

func floatPtr(v float64) *float64 { return &v }

func timePtr(t time.Time) *time.Time { return &t }

// ──────────────────────────────────────────────────────────────────────
// fundamentals_detail DDL parity — 契约 C1 (TASKS.md EQD-P1-1)
// ──────────────────────────────────────────────────────────────────────

// The frozen contract (contracts/fundamentals_detail.schema.sql) declares three
// copies of this DDL that must agree:
//
//	contracts/fundamentals_detail.schema.sql       — 契约副本（分歧时以此为准）
//	docs/migrations/022_equitydeep_fundamentals.sql — 文档副本
//	pkg/storage/postgres.go inline migrate()       — 实际执行路径
//
// Drift here is silent: only the inline copy runs, so a stale contract or docs
// copy would describe a table that does not exist. ODR-052 mitigated this with
// a header comment and explicitly recorded the missing automated check; the
// tests below are that check. Paths are relative to this package directory.

// fundamentalsDetailCopy is one of the three same-source DDL copies.
type fundamentalsDetailCopy struct {
	label string
	path  string
}

var fundamentalsDetailCopies = []fundamentalsDetailCopy{
	{"契约副本", filepath.Join("..", "..", "contracts", "fundamentals_detail.schema.sql")},
	{"文档副本", filepath.Join("..", "..", "docs", "migrations", "022_equitydeep_fundamentals.sql")},
	{"执行路径", filepath.Join("..", "..", "pkg", "storage", "postgres.go")},
}

// TestFundamentalsDetailSchema_ThreeCopiesAgree fails when any of the three
// copies diverges in column set, types, nullability, primary key or index.
func TestFundamentalsDetailSchema_ThreeCopiesAgree(t *testing.T) {
	var wantTable, wantIndex, wantLabel string
	for _, c := range fundamentalsDetailCopies {
		src := readRepoFile(t, c.path)
		table := normalizeDDL(extractFundamentalsDetailTable(t, c.path, src))
		index := normalizeDDL(extractFundamentalsDetailIndex(t, c.path, src))
		require.NotEmpty(t, table, "%s: extracted table DDL is empty", c.label)
		require.NotEmpty(t, index, "%s: extracted index DDL is empty", c.label)

		if wantTable == "" {
			wantTable, wantIndex, wantLabel = table, index, c.label
			continue
		}
		assert.Equal(t, wantTable, table,
			"%s 与 %s 的 fundamentals_detail 建表 DDL 已漂移；以契约副本为准并同时修正三处", c.label, wantLabel)
		assert.Equal(t, wantIndex, index,
			"%s 与 %s 的 idx_fund_detail_lookup 定义已漂移；以契约副本为准并同时修正三处", c.label, wantLabel)
	}
}

// TestFundamentalsDetailSchema_FrozenSemantics pins the properties the contract
// calls non-negotiable, so an edit applied consistently to all three copies but
// semantically wrong still fails:
//   - ann_date NOT NULL      → PIT alignment; without it factor computation
//     would read results before their announcement (look-ahead bias)
//   - fetched_at in the PK   → restatements coexist instead of overwriting
//   - snapshot_uri NOT NULL  → every number is traceable to its snapshot
func TestFundamentalsDetailSchema_FrozenSemantics(t *testing.T) {
	contract := fundamentalsDetailCopies[0]
	ddl := normalizeDDL(extractFundamentalsDetailTable(t, contract.path, readRepoFile(t, contract.path)))

	assert.Contains(t, ddl, "ann_date DATE NOT NULL")
	assert.Contains(t, ddl, "fetched_at TIMESTAMPTZ NOT NULL")
	assert.Contains(t, ddl, "snapshot_uri TEXT NOT NULL")
	assert.Contains(t, ddl, "PRIMARY KEY (ts_code, end_date, ann_date, field_code, fetched_at)")
	// value is the only nullable column: a missing reading must stay
	// distinguishable from a reading of zero.
	assert.Contains(t, ddl, "value NUMERIC(24,4),")

	// The look-up index is part of the contract as well: factor computation
	// reads the latest ann_date-aware reading per (ts_code, field_code).
	index := normalizeDDL(extractFundamentalsDetailIndex(t, contract.path, readRepoFile(t, contract.path)))
	assert.Contains(t, index, "ON fundamentals_detail (ts_code, field_code, end_date DESC)")
}

// TestFundamentalsDetailSchema_AppliedByMigrate checks that the inline
// migration's table is really present in a live database.
//
// DATA-09 改了这条断言的证据形状：testStore() 现在走**不迁移**入口，所以本用例
// 证明的是「已迁移的开发库里这张表在」，而不再顺带触发 migrate()。「migrate() 真能
// 把它建出来」那一半属于显式迁移路径，由 integration tag 下的一次性容器套件
// （pkg/storage/integration_test.go）负责 —— 那里才是在空库上跑构造。
// 连不上 Postgres 仍然 skip，与本包其余部分一致。
func TestFundamentalsDetailSchema_AppliedByMigrate(t *testing.T) {
	store := testStore(t)
	defer store.Close()

	var nullable string
	err := store.DB().QueryRow(context.Background(),
		`SELECT is_nullable FROM information_schema.columns
		 WHERE table_name = 'fundamentals_detail' AND column_name = 'ann_date'`).Scan(&nullable)
	require.NoError(t, err,
		"fundamentals_detail.ann_date 不在库里 —— 本用例不再触发 migrate()（DATA-09）；"+
			"这是一次没迁移过的库？起一次服务或跑 tagged 集成套件")
	assert.Equal(t, "NO", nullable, "ann_date must be NOT NULL for PIT alignment")
}

// readRepoFile reads a path relative to the package directory (the working
// directory Go uses for tests).
func readRepoFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err, "cannot read %s", path)
	return string(b)
}

// extractFundamentalsDetailTable returns the table's column list, from the
// opening paren through the closing paren of its primary key.
func extractFundamentalsDetailTable(t *testing.T, path, src string) string {
	t.Helper()
	return extractParenGroup(t, path, src,
		"CREATE TABLE IF NOT EXISTS fundamentals_detail (", "PRIMARY KEY")
}

// extractFundamentalsDetailIndex returns the look-up index statement.
func extractFundamentalsDetailIndex(t *testing.T, path, src string) string {
	t.Helper()
	return extractParenGroup(t, path, src,
		"CREATE INDEX IF NOT EXISTS idx_fund_detail_lookup", "end_date DESC")
}

// extractParenGroup returns the text after startMarker up to and including the
// closing paren of the group that anchor sits in. anchor must come after any
// nested parens of that group (e.g. NUMERIC(24,4), or the PRIMARY KEY clause
// that CONTRACT keeps last), so it identifies the terminating paren unambiguously
// whether the source is a .sql file or a Go raw string literal.
func extractParenGroup(t *testing.T, path, src, startMarker, anchor string) string {
	t.Helper()
	start := strings.Index(src, startMarker)
	require.GreaterOrEqual(t, start, 0, "%s: %q not found", path, startMarker)
	rest := src[start+len(startMarker):]

	at := strings.Index(rest, anchor)
	require.GreaterOrEqual(t, at, 0, "%s: %q not found", path, anchor)
	close := strings.Index(rest[at:], ")")
	require.GreaterOrEqual(t, close, 0, "%s: %q group unterminated", path, anchor)

	return rest[:at+close+1]
}

// normalizeDDL strips SQL line comments and collapses whitespace so the three
// copies compare by content rather than by their differing alignment.
func normalizeDDL(ddl string) string {
	var b strings.Builder
	for _, line := range strings.Split(ddl, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteString(" ")
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
