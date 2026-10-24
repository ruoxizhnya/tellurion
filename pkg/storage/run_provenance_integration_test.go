//go:build integration

package storage

// QT-63 ⑤ 的落库腿：真库上建表、写入、读回，并用
// `quant.run_provenance ⋈ backtest_jobs` 取回「输入身份 + 状态」。
//
// 单测（pkg/backtest）证明的是「引擎算出的身份对」；这里证明「这份身份
// 真的能落到 PG 上、且 status 仍只有 backtest_jobs 一个权威」。

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func qt63Record(runID string) *RunProvenanceRecord {
	return &RunProvenanceRecord{
		RunID:               runID,
		RunSpecHash:         strings.Repeat("a", 64),
		DatasetSnapshotHash: strings.Repeat("b", 64),
		ArtifactHash:        "",
		VersionHash:         "plugin-git-hash",
		StrategyRefInput:    "momentum",
		ParamsJSON:          []byte(`{"lookback":20,"top_n":5}`),
		ParamsHash:          strings.Repeat("c", 64),
		Seed:                42,
		Kernel:              "go",
		EngineVersion:       "test-revision",
		FillTiming:          "next_open",
		RiskRulesHash:       "",
	}
}

func TestIntegration_RunProvenance_RoundTripAndJoin(t *testing.T) {
	store, ctx := setUp(t)
	const runID = "qt63-join-run-1"

	t.Cleanup(func() {
		_, _ = store.pool.Exec(ctx, `DELETE FROM quant.run_provenance WHERE run_id = $1`, runID)
	})

	require.NoError(t, store.CreateBacktestJob(ctx, map[string]any{
		"id":          runID,
		"strategy_id": "momentum",
		"params":      `{"backtest_id":"` + runID + `"}`,
		"universe":    "600000.SH",
		"start_date":  "2024-01-02",
		"end_date":    "2024-06-28",
		"status":      "completed",
	}))

	rec := qt63Record(runID)
	require.NoError(t, store.SaveRunProvenance(ctx, rec))

	got, err := store.GetRunProvenance(ctx, runID)
	require.NoError(t, err)
	require.NotNil(t, got, "写入后必须读得回来")
	assert.Equal(t, rec.RunSpecHash, got.RunSpecHash)
	assert.Equal(t, rec.DatasetSnapshotHash, got.DatasetSnapshotHash)
	assert.Equal(t, rec.ParamsHash, got.ParamsHash)
	// JSONB 会按自己的规则重排/加空格：比语义，不比字节。
	assert.JSONEq(t, string(rec.ParamsJSON), string(got.ParamsJSON))
	assert.Equal(t, rec.Seed, got.Seed)
	assert.Equal(t, rec.Kernel, got.Kernel)
	assert.Equal(t, rec.EngineVersion, got.EngineVersion)
	assert.Equal(t, rec.FillTiming, got.FillTiming)
	assert.Equal(t, rec.StrategyRefInput, got.StrategyRefInput)
	assert.False(t, got.CreatedAt.IsZero(), "created_at 由 DB 默认值填")

	// ⑤ 的验收查询：provenance 不存 status，状态经 join 取。
	var (
		status      string
		runSpecHash string
		seed        int64
	)
	err = store.pool.QueryRow(ctx, `
		SELECT b.status, r.run_spec_hash, r.seed
		FROM quant.run_provenance r
		JOIN backtest_jobs b ON b.id = r.run_id
		WHERE r.run_id = $1`, runID).Scan(&status, &runSpecHash, &seed)
	require.NoError(t, err, "run_provenance ⋈ backtest_jobs 必须能查到")
	assert.Equal(t, "completed", status)
	assert.Equal(t, rec.RunSpecHash, runSpecHash)
	assert.Equal(t, int64(42), seed)
}

// TestIntegration_RunProvenance_RiskRulesHashRoundTrip 是 QT-66 的真 PG 腿：
// 非空 risk_rules_hash 必须原样落库、经 join 读回；空串（not_configured）
// 读回仍是空串（库里的 NULL 被 COALESCE 折回 ”）。
func TestIntegration_RunProvenance_RiskRulesHashRoundTrip(t *testing.T) {
	store, ctx := setUp(t)
	const (
		runID       = "qt66-risk-hash-run"
		emptyRunID  = "qt66-risk-empty-run"
		riskHashHex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	)

	t.Cleanup(func() {
		_, _ = store.pool.Exec(ctx, `DELETE FROM quant.run_provenance WHERE run_id = ANY($1)`,
			[]string{runID, emptyRunID})
	})

	for _, id := range []string{runID, emptyRunID} {
		require.NoError(t, store.CreateBacktestJob(ctx, map[string]any{
			"id":          id,
			"strategy_id": "momentum",
			"params":      `{"backtest_id":"` + id + `"}`,
			"universe":    "600000.SH",
			"start_date":  "2024-01-02",
			"end_date":    "2024-06-28",
			"status":      "completed",
		}))
	}

	filled := qt63Record(runID)
	filled.RiskRulesHash = riskHashHex
	require.NoError(t, store.SaveRunProvenance(ctx, filled))

	empty := qt63Record(emptyRunID)
	empty.RiskRulesHash = ""
	require.NoError(t, store.SaveRunProvenance(ctx, empty))

	got, err := store.GetRunProvenance(ctx, runID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, riskHashHex, got.RiskRulesHash, "非空规则快照 hash 必须原样读回")

	gotEmpty, err := store.GetRunProvenance(ctx, emptyRunID)
	require.NoError(t, err)
	require.NotNil(t, gotEmpty)
	assert.Empty(t, gotEmpty.RiskRulesHash, "not_configured 的空串读回仍是空串")

	var joined string
	err = store.pool.QueryRow(ctx, `
		SELECT r.risk_rules_hash
		FROM quant.run_provenance r
		JOIN backtest_jobs b ON b.id = r.run_id
		WHERE r.run_id = $1`, runID).Scan(&joined)
	require.NoError(t, err)
	assert.Equal(t, riskHashHex, joined)
}

func TestIntegration_RunProvenance_ConflictFailsLoud(t *testing.T) {
	store, ctx := setUp(t)
	const runID = "qt63-conflict-run"

	t.Cleanup(func() {
		_, _ = store.pool.Exec(ctx, `DELETE FROM quant.run_provenance WHERE run_id = $1`, runID)
	})

	rec := qt63Record(runID)
	require.NoError(t, store.SaveRunProvenance(ctx, rec))

	// 同一份身份重复写入：允许（进程重试 / 断点续写）。
	require.NoError(t, store.SaveRunProvenance(ctx, qt63Record(runID)))

	// 同一个 run_id + 不同的身份：不是「重复插入」，是这个 id 已经属于别人。
	drifted := qt63Record(runID)
	drifted.RunSpecHash = strings.Repeat("d", 64)
	err := store.SaveRunProvenance(ctx, drifted)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRunProvenanceConflict),
		"身份漂移必须 fail-loud，实际错误：%v", err)

	// 冲突写入不得覆盖原记录。
	got, err := store.GetRunProvenance(ctx, runID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, strings.Repeat("a", 64), got.RunSpecHash)
}
