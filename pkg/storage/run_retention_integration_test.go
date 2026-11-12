//go:build integration
// +build integration

package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CORE-RESULT-RETENTION 边界与语义用例（有库）。
//
// 固定 now，构造 finished_at 恰 89 / 90 / 91 天的三条，断言只有 91 天那条
// 的明细被摘、汇总键还在；91 天但被引用 / 运行中的不动；365 天整行与
// provenance 同事务删除；第二次跑删除数为 0；dry-run 不写库。

const retentionFixturePrefix = "ret-test-"

func retentionNow() time.Time {
	return time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
}

func detailPayload(equityPoints, trades int) map[string]any {
	equity := make([]map[string]any, 0, equityPoints)
	for i := 0; i < equityPoints; i++ {
		equity = append(equity, map[string]any{"date": fmt.Sprintf("2026-01-%02d", i+1), "value": 100 + i})
	}
	tr := make([]map[string]any, 0, trades)
	for i := 0; i < trades; i++ {
		tr = append(tr, map[string]any{"symbol": fmt.Sprintf("S%02d", i), "qty": i + 1})
	}
	return map[string]any{
		"total_return":     0.12,
		"max_drawdown":     0.05,
		"portfolio_values": equity,
		"trades":           tr,
	}
}

func summaryOnlyPayload() map[string]any {
	return map[string]any{"total_return": 0.2, "max_drawdown": 0.08}
}

// ensureRetentionMirrors 建两张「编排/上层真实引用表」的最小形状镜像。
// core 迁移不创建它们；有库测试里建出来，才能验证按真实表判引用/运行中。
func ensureRetentionMirrors(t *testing.T, ctx context.Context, store *PostgresStore) {
	t.Helper()
	_, err := store.pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS run.run_artifacts (run_id text PRIMARY KEY)`)
	require.NoError(t, err)
	_, err = store.pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS run.backtest_jobs (id text PRIMARY KEY, status text NOT NULL)`)
	require.NoError(t, err)
}

func cleanupRetentionFixtures(t *testing.T, ctx context.Context, store *PostgresStore) {
	t.Helper()
	for _, q := range []string{
		`DELETE FROM run.backtest_results WHERE run_id LIKE '` + retentionFixturePrefix + `%'`,
		`DELETE FROM quant.run_provenance WHERE run_id LIKE '` + retentionFixturePrefix + `%'`,
	} {
		_, err := store.pool.Exec(ctx, q)
		require.NoError(t, err)
	}
	// 镜像表可能尚未创建；用 to_regclass 守卫，避免 cleanup 因缺表报错。
	for _, tbl := range []string{"run.run_artifacts", "run.backtest_jobs"} {
		var oid *string
		require.NoError(t, store.pool.QueryRow(ctx, `SELECT to_regclass($1)::text`, tbl).Scan(&oid))
		if oid == nil {
			continue
		}
		col := "run_id"
		if tbl == "run.backtest_jobs" {
			col = "id"
		}
		_, err := store.pool.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE %s LIKE '%s%%'`, tbl, col, retentionFixturePrefix))
		require.NoError(t, err)
	}
}

func insertRetentionResult(t *testing.T, ctx context.Context, store *PostgresStore, runID string, status string, finishedAt time.Time, payload any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	_, err = store.pool.Exec(ctx, `
		INSERT INTO run.backtest_results (run_id, result_status, result, created_at, updated_at, finished_at)
		VALUES ($1, $2, $3::jsonb, $4, $4, $4)`,
		runID, status, string(raw), finishedAt)
	require.NoError(t, err)
}

func insertRetentionProvenance(t *testing.T, ctx context.Context, store *PostgresStore, runID string, createdAt time.Time) {
	t.Helper()
	_, err := store.pool.Exec(ctx, `
		INSERT INTO quant.run_provenance
			(run_id, run_spec_hash, dataset_snapshot_hash, params_hash, kernel, engine_version, fill_timing, created_at)
		VALUES ($1, 'sh', 'dh', 'ph', 'local', 'test', 'close', $2)
		ON CONFLICT (run_id) DO NOTHING`,
		runID, createdAt)
	require.NoError(t, err)
}

func resultHasKey(t *testing.T, ctx context.Context, store *PostgresStore, runID, key string) bool {
	t.Helper()
	var has bool
	err := store.pool.QueryRow(ctx,
		`SELECT result ? $2 FROM run.backtest_results WHERE run_id = $1`, runID, key).Scan(&has)
	require.NoError(t, err)
	return has
}

func detailPurgedAt(t *testing.T, ctx context.Context, store *PostgresStore, runID string) *time.Time {
	t.Helper()
	var v *time.Time
	err := store.pool.QueryRow(ctx,
		`SELECT detail_purged_at FROM run.backtest_results WHERE run_id = $1`, runID).Scan(&v)
	require.NoError(t, err)
	return v
}

func rowExists(t *testing.T, ctx context.Context, store *PostgresStore, table, runID string) bool {
	t.Helper()
	var exists bool
	require.NoError(t, store.pool.QueryRow(ctx,
		fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE run_id = $1)`, table), runID).Scan(&exists))
	return exists
}

func TestRunRetention_BoundariesAndSemantics(t *testing.T) {
	store, ctx := setUp(t)
	cleanupRetentionFixtures(t, ctx, store)
	t.Cleanup(func() { cleanupRetentionFixtures(t, context.Background(), store) })
	ensureRetentionMirrors(t, ctx, store)

	now := retentionNow()
	day := 24 * time.Hour

	// 明细边界：89 / 90 / 91 天。
	insertRetentionResult(t, ctx, store, retentionFixturePrefix+"89", "completed", now.Add(-89*day), detailPayload(3, 2))
	insertRetentionResult(t, ctx, store, retentionFixturePrefix+"90", "completed", now.Add(-90*day), detailPayload(3, 2))
	insertRetentionResult(t, ctx, store, retentionFixturePrefix+"91", "completed", now.Add(-91*day), detailPayload(3, 2))
	// 91 天但被引用 / 运行中。
	insertRetentionResult(t, ctx, store, retentionFixturePrefix+"91-ref", "completed", now.Add(-91*day), detailPayload(3, 2))
	insertRetentionResult(t, ctx, store, retentionFixturePrefix+"91-run", "completed", now.Add(-91*day), detailPayload(3, 2))
	// 从未有过明细（failed 空载荷）。
	insertRetentionResult(t, ctx, store, retentionFixturePrefix+"never-detail", "failed", now.Add(-91*day), map[string]any{"error": "boom"})
	// 365 天档：一条纯汇总、一条被引用；另有一条更老且仍带明细，验证先明细后整行。
	// 用 366/367 天而不是恰好 365 天：判据是严格小于，恰 365 天属于边界内保留。
	insertRetentionResult(t, ctx, store, retentionFixturePrefix+"365", "completed", now.Add(-366*day), summaryOnlyPayload())
	insertRetentionResult(t, ctx, store, retentionFixturePrefix+"365-ref", "completed", now.Add(-366*day), summaryOnlyPayload())
	insertRetentionResult(t, ctx, store, retentionFixturePrefix+"366-detail", "completed", now.Add(-367*day), detailPayload(2, 1))

	for _, id := range []string{"91", "366-detail"} {
		insertRetentionProvenance(t, ctx, store, retentionFixturePrefix+id, now.Add(-366*day))
	}

	_, err := store.pool.Exec(ctx, `INSERT INTO run.run_artifacts (run_id) VALUES ($1), ($2)`,
		retentionFixturePrefix+"91-ref", retentionFixturePrefix+"365-ref")
	require.NoError(t, err)
	_, err = store.pool.Exec(ctx, `INSERT INTO run.backtest_jobs (id, status) VALUES ($1, 'running')`,
		retentionFixturePrefix+"91-run")
	require.NoError(t, err)

	cfg := RetentionConfig{Now: func() time.Time { return now }}
	report, err := store.ReapBacktestResults(ctx, cfg)
	require.NoError(t, err)

	// 边界断言：只有 91 天那条被清；89 / 90 天明细不动。
	assert.True(t, resultHasKey(t, ctx, store, retentionFixturePrefix+"89", "portfolio_values"), "89 天明细必须保留")
	assert.True(t, resultHasKey(t, ctx, store, retentionFixturePrefix+"90", "portfolio_values"), "恰 90 天明细必须保留（严格小于才清）")
	assert.Nil(t, detailPurgedAt(t, ctx, store, retentionFixturePrefix+"89"))
	assert.Nil(t, detailPurgedAt(t, ctx, store, retentionFixturePrefix+"90"))
	assert.False(t, resultHasKey(t, ctx, store, retentionFixturePrefix+"91", "portfolio_values"), "91 天明细必须被清")
	assert.False(t, resultHasKey(t, ctx, store, retentionFixturePrefix+"91", "trades"), "91 天成交明细必须被清")
	assert.NotNil(t, detailPurgedAt(t, ctx, store, retentionFixturePrefix+"91"), "清理必须留痕")
	assert.True(t, resultHasKey(t, ctx, store, retentionFixturePrefix+"91", "total_return"), "汇总键必须保留")

	// 被引用 / 运行中的 91 天 run 明细不动。
	assert.True(t, resultHasKey(t, ctx, store, retentionFixturePrefix+"91-ref", "portfolio_values"), "被引用的 run 不得清明细")
	assert.True(t, resultHasKey(t, ctx, store, retentionFixturePrefix+"91-run", "portfolio_values"), "运行中的 run 不得清明细")

	// 从未有过明细的 failed run 不能被标记为「已清理」。
	assert.Nil(t, detailPurgedAt(t, ctx, store, retentionFixturePrefix+"never-detail"))

	// 365 天档：整行 + provenance 删除；被引用的保留。
	assert.False(t, rowExists(t, ctx, store, "run.backtest_results", retentionFixturePrefix+"365"))
	assert.False(t, rowExists(t, ctx, store, "quant.run_provenance", retentionFixturePrefix+"365"))
	assert.True(t, rowExists(t, ctx, store, "run.backtest_results", retentionFixturePrefix+"365-ref"))

	// 366 天带明细：先清明细，再整行删除。
	assert.False(t, rowExists(t, ctx, store, "run.backtest_results", retentionFixturePrefix+"366-detail"))
	assert.False(t, rowExists(t, ctx, store, "quant.run_provenance", retentionFixturePrefix+"366-detail"))

	// 91 天的 provenance 还在（只到明细档，没到 365 天）。
	assert.True(t, rowExists(t, ctx, store, "quant.run_provenance", retentionFixturePrefix+"91"))

	assert.Equal(t, 2, report.DetailRuns, "明细阶段应处理 91 与 366-detail")
	assert.Equal(t, 2, report.SummaryRuns, "汇总阶段应处理 365 与 366-detail")
	assert.Equal(t, 3, report.Skipped, "91-ref / 91-run / 365-ref 三条被跳过")
	assert.Equal(t, 3, report.DetailTrades, "91 天 2 笔 + 366 天 1 笔")
	assert.Equal(t, 5, report.DetailEquityPoints, "91 天 3 点 + 366 天 2 点")

	// 读路径能区分：GetBacktestResult 读出 detail_purged_at。
	rec, err := store.GetBacktestResult(ctx, retentionFixturePrefix+"91")
	require.NoError(t, err)
	require.NotNil(t, rec.DetailPurgedAt)
	assert.NotNil(t, rec.FinishedAt, "finished_at 应已回填/写入")

	// 幂等：同一批第二次必须 0 删除。
	second, err := store.ReapBacktestResults(ctx, cfg)
	require.NoError(t, err)
	assert.Equal(t, 0, second.DetailRuns, "第二次明细删除数必须为 0")
	assert.Equal(t, 0, second.SummaryRuns, "第二次汇总删除数必须为 0")

	t.Logf("boundary report: detail_runs=%d summary_runs=%d skipped=%d trades=%d equity=%d",
		report.DetailRuns, report.SummaryRuns, report.Skipped, report.DetailTrades, report.DetailEquityPoints)
	t.Logf("second pass: detail_runs=%d summary_runs=%d", second.DetailRuns, second.SummaryRuns)
}

func TestRunRetention_DryRunDoesNotWrite(t *testing.T) {
	store, ctx := setUp(t)
	cleanupRetentionFixtures(t, ctx, store)
	t.Cleanup(func() { cleanupRetentionFixtures(t, context.Background(), store) })

	now := retentionNow()
	insertRetentionResult(t, ctx, store, retentionFixturePrefix+"dry-91", "completed", now.Add(-91*24*time.Hour), detailPayload(4, 2))

	report, err := store.ReapBacktestResults(ctx, RetentionConfig{
		DryRun: true,
		Now:    func() time.Time { return now },
	})
	require.NoError(t, err)

	assert.True(t, report.DryRun)
	assert.Equal(t, 1, report.DetailRuns, "dry-run 应报出 1 条将清理的 run")
	assert.Equal(t, 2, report.DetailTrades)
	assert.Equal(t, 4, report.DetailEquityPoints)
	require.Len(t, report.Actions, 1)
	assert.Equal(t, "purge_detail", report.Actions[0].Action)

	// 库中未发生任何写操作。
	assert.True(t, resultHasKey(t, ctx, store, retentionFixturePrefix+"dry-91", "portfolio_values"))
	assert.Nil(t, detailPurgedAt(t, ctx, store, retentionFixturePrefix+"dry-91"))

	out, _ := json.MarshalIndent(report, "", "  ")
	t.Logf("dry-run sample:\n%s", string(out))
}

func TestRunRetention_SaveSetsFinishedAt(t *testing.T) {
	store, ctx := setUp(t)
	cleanupRetentionFixtures(t, ctx, store)
	t.Cleanup(func() { cleanupRetentionFixtures(t, context.Background(), store) })

	raw, err := json.Marshal(detailPayload(1, 1))
	require.NoError(t, err)
	saved, err := store.SaveBacktestResult(ctx, &BacktestResultRecord{
		RunID:        retentionFixturePrefix + "save",
		ResultStatus: "completed",
		Result:       raw,
	})
	require.NoError(t, err)
	require.NotNil(t, saved.FinishedAt, "SaveBacktestResult 必须写 finished_at")
	assert.Nil(t, saved.DetailPurgedAt, "新结果行不应有清理痕迹")
}
