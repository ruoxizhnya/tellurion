package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"tellurion/pkg/testenv"
)

// TestBacktestResultsDDLSyncedWithMigrate 钉住「migrate() 实际执行的语句 ≡ 包级
// BacktestResultsSchemaDDL」——本测试不需要真库。
//
// 破坏腿：改包级 DDL 而不改 migrate() 的追加（或反之）→ 本测试红。
func TestBacktestResultsDDLSyncedWithMigrate(t *testing.T) {
	t.Parallel()

	stmts := migrationStatements()
	seg, start := findMigrationSegment(t, stmts, BacktestResultsSchemaDDL)
	if len(seg) != len(BacktestResultsSchemaDDL) {
		t.Fatalf("Migration 063 段长度 = %d, want %d", len(seg), len(BacktestResultsSchemaDDL))
	}
	for i := range BacktestResultsSchemaDDL {
		if normalizeWS(seg[i]) != normalizeWS(BacktestResultsSchemaDDL[i]) {
			t.Fatalf("migrate() 第 %d 条（Migration 063 段）与 BacktestResultsSchemaDDL 不一致", start+i+1)
		}
	}
}

// TestBacktestResultsDDLHasThreeValueCheck 钉住 K9 结论轴：result_status 只允许
// completed / invalid / failed，不允许混入队列生命周期状态（pending / running /
// cancelled 归 orchestration 的 run.backtest_jobs）。
func TestBacktestResultsDDLHasThreeValueCheck(t *testing.T) {
	t.Parallel()

	var ddl string
	for _, stmt := range BacktestResultsSchemaDDL {
		if strings.Contains(stmt, "CREATE TABLE IF NOT EXISTS run.backtest_results") {
			ddl = stmt
		}
	}
	if ddl == "" {
		t.Fatal("BacktestResultsSchemaDDL 应建 run.backtest_results 表")
	}
	for _, want := range []string{"'completed'", "'invalid'", "'failed'"} {
		if !strings.Contains(ddl, want) {
			t.Errorf("result_status CHECK 缺 %s：\n%s", want, ddl)
		}
	}
	for _, forbidden := range []string{"'pending'", "'running'", "'cancelled'"} {
		if strings.Contains(ddl, forbidden) {
			t.Errorf("result_status CHECK 混入队列状态 %s（应归 orchestration）", forbidden)
		}
	}
}

// run.backtest_results（SVC-ORCH-G3 结果面）的库侧测试：真 PG 直连、数据自清理、
// 无库（TEST_DATABASE_DSN 未设置）则显式 Skip。

func newBacktestResultsTestStore(t *testing.T) *PostgresStore {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dsn := testenv.DatabaseDSN()
	if dsn == "" {
		testenv.SkipNoDB(t)
	}
	store, err := NewPostgresStoreConnected(ctx, dsn)
	if err != nil {
		t.Fatalf("[testdb] TEST_DATABASE_DSN 已设置但连不上（fail-loud）: %v", err)
	}
	clean := func(c context.Context) {
		_, _ = store.pool.Exec(c, `DELETE FROM run.backtest_results WHERE run_id LIKE 'test-br-%'`)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		clean(cleanupCtx)
	})
	if _, err := store.pool.Exec(ctx, `SELECT 1 FROM run.backtest_results LIMIT 1`); err != nil {
		t.Skipf("run.backtest_results unavailable (run migrations first): %v", err)
	}
	return store
}

func TestBacktestResultRoundTripAndUpsert(t *testing.T) {
	store := newBacktestResultsTestStore(t)
	ctx := context.Background()
	runID := fmt.Sprintf("test-br-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = store.pool.Exec(ctx, `DELETE FROM run.backtest_results WHERE run_id = $1`, runID)
	})

	first := &BacktestResultRecord{
		RunID:        runID,
		ResultStatus: "completed",
		Result:       json.RawMessage(`{"metrics":{"total_return":0.12,"sharpe":1.4},"trades":[]}`),
	}
	saved, err := store.SaveBacktestResult(ctx, first)
	if err != nil {
		t.Fatalf("SaveBacktestResult(completed): %v", err)
	}
	if saved.ResultStatus != "completed" || saved.CreatedAt.IsZero() || saved.UpdatedAt.IsZero() {
		t.Fatalf("saved = %+v", saved)
	}

	got, err := store.GetBacktestResult(ctx, runID)
	if err != nil {
		t.Fatalf("GetBacktestResult: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(got.Result, &payload); err != nil {
		t.Fatalf("result payload: %v", err)
	}
	metrics, _ := payload["metrics"].(map[string]any)
	if metrics["sharpe"] != 1.4 {
		t.Fatalf("payload round-trip = %s", got.Result)
	}
	if !got.CreatedAt.Equal(saved.CreatedAt) {
		t.Fatalf("created_at changed on read: %v vs %v", got.CreatedAt, saved.CreatedAt)
	}

	// 同 run 重写：结论与载荷覆盖，created_at 保留首次写入。
	second := &BacktestResultRecord{
		RunID:        runID,
		ResultStatus: "invalid",
		Result:       json.RawMessage(`{"reason":"price limit gap"}`),
	}
	updated, err := store.SaveBacktestResult(ctx, second)
	if err != nil {
		t.Fatalf("SaveBacktestResult(invalid): %v", err)
	}
	if updated.CreatedAt.Sub(saved.CreatedAt).Abs() > time.Second {
		t.Fatalf("created_at should be preserved, got %v vs %v", updated.CreatedAt, saved.CreatedAt)
	}
	if updated.UpdatedAt.Before(saved.UpdatedAt) {
		t.Fatalf("updated_at went backwards: %v < %v", updated.UpdatedAt, saved.UpdatedAt)
	}
	got, err = store.GetBacktestResult(ctx, runID)
	if err != nil {
		t.Fatalf("GetBacktestResult after upsert: %v", err)
	}
	if got.ResultStatus != "invalid" {
		t.Fatalf("result_status = %q, want invalid", got.ResultStatus)
	}
}

func TestBacktestResultFailedWithoutPayload(t *testing.T) {
	store := newBacktestResultsTestStore(t)
	ctx := context.Background()
	runID := fmt.Sprintf("test-br-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = store.pool.Exec(ctx, `DELETE FROM run.backtest_results WHERE run_id = $1`, runID)
	})

	saved, err := store.SaveBacktestResult(ctx, &BacktestResultRecord{
		RunID:        runID,
		ResultStatus: "failed",
		ErrorMessage: "engine panic: out of memory",
	})
	if err != nil {
		t.Fatalf("SaveBacktestResult(failed): %v", err)
	}
	if saved.ErrorMessage != "engine panic: out of memory" || len(saved.Result) != 0 {
		t.Fatalf("saved = %+v", saved)
	}
	got, err := store.GetBacktestResult(ctx, runID)
	if err != nil {
		t.Fatalf("GetBacktestResult: %v", err)
	}
	if got.ResultStatus != "failed" || got.ErrorMessage == "" || len(got.Result) != 0 {
		t.Fatalf("got = %+v", got)
	}
}

func TestBacktestResultValidation(t *testing.T) {
	store := newBacktestResultsTestStore(t)
	ctx := context.Background()

	cases := []struct {
		name string
		rec  *BacktestResultRecord
	}{
		{"nil", nil},
		{"empty run id", &BacktestResultRecord{ResultStatus: "completed", Result: json.RawMessage(`{}`)}},
		{"bad status", &BacktestResultRecord{RunID: "test-br-x", ResultStatus: "done", Result: json.RawMessage(`{}`)}},
		{"completed without payload", &BacktestResultRecord{RunID: "test-br-x", ResultStatus: "completed"}},
		{"invalid payload", &BacktestResultRecord{RunID: "test-br-x", ResultStatus: "completed", Result: json.RawMessage(`{`)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := store.SaveBacktestResult(ctx, c.rec); err == nil {
				t.Fatalf("SaveBacktestResult(%s) = nil error, want error", c.name)
			}
		})
	}
}

func TestBacktestResultNotFound(t *testing.T) {
	store := newBacktestResultsTestStore(t)
	_, err := store.GetBacktestResult(context.Background(), "test-br-does-not-exist")
	if !errors.Is(err, ErrBacktestResultNotFound) {
		t.Fatalf("error = %v, want ErrBacktestResultNotFound", err)
	}
}
