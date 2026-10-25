package main

// QT-72 · X0 读面的 HTTP 腿（内存实现，无库）。
//
// 钉住五件事：
//   - 三 hash + result_status 可读，且 result_status 是 K9 结论轴（invalid
//     不是 failed）；
//   - 15 个 wire 键与 orchestration `provenanceView` 逐字一致，可空列显式 null；
//   - 三态 wire code：409 provenance_not_ready / 404 provenance_not_found /
//     500 provenance_missing；job 不存在是 404 job_not_found；
//   - 未接线读面 503（装配缺失，不冒充三态）；
//   - 端点只读（fake 只收到一次读调用）。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/storage"
)

type fakeProvenanceReader struct {
	row    *storage.BacktestJobProvenance
	err    error
	gotIDs []string
}

func (f *fakeProvenanceReader) GetBacktestJobProvenance(_ context.Context, runID string) (*storage.BacktestJobProvenance, error) {
	f.gotIDs = append(f.gotIDs, runID)
	return f.row, f.err
}

func provenanceTestRouter(reader backtestProvenanceReader) *gin.Engine {
	r := gin.New()
	api := r.Group("/api/backtest")
	registerBacktestProvenanceRoute(api, reader, zerolog.Nop())
	return r
}

func provenanceGet(t *testing.T, r *gin.Engine, id string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/backtest/"+id+"/provenance", nil))
	return w
}

func provenanceSampleRow() *storage.BacktestJobProvenance {
	created := time.Date(2026, 10, 25, 8, 0, 0, 0, time.UTC)
	return &storage.BacktestJobProvenance{
		JobStatus:    "completed",
		ResultStatus: "invalid",
		Provenance: &storage.RunProvenanceRecord{
			RunID:               "bt-1",
			RunSpecHash:         "runspec-hash",
			DatasetSnapshotHash: "dataset-hash",
			ArtifactHash:        "",
			VersionHash:         "version-hash",
			StrategyRefInput:    "momentum@v1",
			ParamsJSON:          []byte(`{"lookback":20}`),
			ParamsHash:          "params-hash",
			Seed:                7,
			Kernel:              "go",
			EngineVersion:       "engine-1.0",
			FillTiming:          "next_open",
			RiskRulesHash:       "",
			CreatedAt:           created,
		},
	}
}

// provenanceWireKeys 是 orchestration `internal/httpapi/provenance.go` 的
// `provenanceView` 的 15 个 json 键（逐字）。两侧读同一个概念，形状不得漂移。
var provenanceWireKeys = []string{
	"run_id",
	"run_spec_hash",
	"dataset_snapshot_hash",
	"risk_rules_hash",
	"strategy_ref",
	"artifact_uri",
	"artifact_hash",
	"version_hash",
	"params_hash",
	"seed",
	"kernel",
	"engine_version",
	"fill_timing",
	"result_status",
	"created_at",
}

func TestProvenance_ReturnsThreeHashesAndK9Conclusion(t *testing.T) {
	t.Parallel()

	reader := &fakeProvenanceReader{row: provenanceSampleRow()}
	w := provenanceGet(t, provenanceTestRouter(reader), "bt-1")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, []string{"bt-1"}, reader.gotIDs, "读面必须只读一次、按 id 读")

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))
	require.Len(t, raw, len(provenanceWireKeys), "DTO 只该有 15 个键：%s", w.Body.String())
	for _, key := range provenanceWireKeys {
		assert.Contains(t, raw, key, "缺 wire 键 %s（与 orchestration provenanceView 对拍）", key)
	}
	assert.NotContains(t, raw, "status", "provenance 是独立 DTO，不带生命周期 status")

	// 三 hash + 结论轴。
	assert.JSONEq(t, `"runspec-hash"`, string(raw["run_spec_hash"]))
	assert.JSONEq(t, `"dataset-hash"`, string(raw["dataset_snapshot_hash"]))
	assert.JSONEq(t, `"version-hash"`, string(raw["version_hash"]))
	assert.JSONEq(t, `"invalid"`, string(raw["result_status"]),
		"invalid 是 K9 结论，必须原样透出（不是 failed）")
	assert.JSONEq(t, `7`, string(raw["seed"]))
	assert.JSONEq(t, `"momentum@v1"`, string(raw["strategy_ref"]))

	// 可空列显式 null，不省略键。
	for _, key := range []string{"artifact_uri", "artifact_hash"} {
		assert.JSONEq(t, `null`, string(raw[key]), "%s 必须显式 null", key)
	}
	// 空串 = 未登记，不是 null（与 orchestration 同口径）。
	assert.JSONEq(t, `""`, string(raw["risk_rules_hash"]))
}

func TestProvenance_NotReadyIs409(t *testing.T) {
	t.Parallel()

	reader := &fakeProvenanceReader{row: &storage.BacktestJobProvenance{JobStatus: "running"}}
	w := provenanceGet(t, provenanceTestRouter(reader), "bt-running")
	assertProvenanceCode(t, w, http.StatusConflict, "provenance_not_ready")
}

func TestProvenance_TerminalWithoutRowSplits404And500(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		jobStatus    string
		resultStatus string
		status       int
		code         string
	}{
		{"failed never produced provenance", "failed", "failed", http.StatusNotFound, "provenance_not_found"},
		{"cancelled never produced provenance", "cancelled", "", http.StatusNotFound, "provenance_not_found"},
		{"completed lost its provenance", "completed", "completed", http.StatusInternalServerError, "provenance_missing"},
		{"invalid lost its provenance", "completed", "invalid", http.StatusInternalServerError, "provenance_missing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reader := &fakeProvenanceReader{row: &storage.BacktestJobProvenance{
				JobStatus: tc.jobStatus, ResultStatus: tc.resultStatus,
			}}
			w := provenanceGet(t, provenanceTestRouter(reader), "bt-x")
			assertProvenanceCode(t, w, tc.status, tc.code)
		})
	}
}

func TestProvenance_UnknownJobIs404(t *testing.T) {
	t.Parallel()

	w := provenanceGet(t, provenanceTestRouter(&fakeProvenanceReader{}), "nope")
	assertProvenanceCode(t, w, http.StatusNotFound, "job_not_found")
}

func TestProvenance_ReaderErrorIs500(t *testing.T) {
	t.Parallel()

	reader := &fakeProvenanceReader{err: errors.New("db down")}
	w := provenanceGet(t, provenanceTestRouter(reader), "bt-1")
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Contains(t, w.Body.String(), `"code":"internal"`)
	assert.NotContains(t, w.Body.String(), "db down", "5xx 不得把内部原因回给客户端")
}

func TestProvenance_UnwiredIs503(t *testing.T) {
	t.Parallel()

	w := provenanceGet(t, provenanceTestRouter(nil), "bt-1")
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), `"code":"unavailable"`)
}

func assertProvenanceCode(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	require.Equal(t, status, w.Code, w.Body.String())
	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, code, body.Code, "三态必须沿用 orchestration 的 wire code：%s", w.Body.String())
	assert.NotEmpty(t, body.Error)
}
