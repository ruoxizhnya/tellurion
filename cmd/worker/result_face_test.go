package main

// 结果面写口的真库验收（SVC-ORCH-G3）：worker 的双写腿里，新腿必须真的
// 落到 core 的 `run.backtest_results`，而不是只在替身里成立。

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"tellurion/pkg/storage"
	"tellurion/pkg/testenv"
)

func TestResultFaceStoreWritesRunBacktestResults(t *testing.T) {
	dsn := testenv.DatabaseDSN()
	if dsn == "" {
		testenv.SkipNoDB(t)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store, err := storage.NewPostgresStoreConnected(ctx, dsn)
	if err != nil {
		t.Fatalf("[testdb] TEST_DATABASE_DSN 已设置但连不上（fail-loud）: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	if _, err := store.DB().Exec(ctx, `SELECT 1 FROM run.backtest_results LIMIT 1`); err != nil {
		t.Skipf("run.backtest_results unavailable (run migrations first): %v", err)
	}
	runID := "test-worker-result-face"
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = store.DB().Exec(cleanupCtx, `DELETE FROM run.backtest_results WHERE run_id = $1`, runID)
	})

	face := resultFaceStore{store: store}
	payload := []byte(`{"id":"` + runID + `","status":"completed","strategy":"momentum"}`)
	if err := face.SaveBacktestResult(ctx, runID, "completed", payload); err != nil {
		t.Fatalf("SaveBacktestResult: %v", err)
	}

	rec, err := store.GetBacktestResult(ctx, runID)
	if err != nil {
		t.Fatalf("GetBacktestResult: %v", err)
	}
	if rec.ResultStatus != "completed" {
		t.Fatalf("result_status = %q, want completed", rec.ResultStatus)
	}
	if !json.Valid(rec.Result) {
		t.Fatalf("stored payload is not valid JSON: %s", rec.Result)
	}
	var decoded map[string]any
	if err := json.Unmarshal(rec.Result, &decoded); err != nil {
		t.Fatalf("decode stored payload: %v", err)
	}
	if decoded["id"] != runID || decoded["strategy"] != "momentum" {
		t.Fatalf("payload drifted: %v", decoded)
	}
}
