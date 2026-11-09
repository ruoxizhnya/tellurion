package job

// SVC-ORCH-G3 收尾腿（2026-11-09）：本地异步队列退役后，本包只剩「读面 +
// 同步结果写面」两条腿，测试也只钉这两条：
//   - 读：GetJob / ListJobs 对 JobStore 的解析与默认 limit；
//   - 写：SaveSyncResult 只写 core 结果面，未装配结果面时 fail-closed。

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-contracts/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockJobStore struct {
	jobs  map[string]map[string]any
	order []string
}

func newMockJobStore() *mockJobStore {
	return &mockJobStore{jobs: make(map[string]map[string]any)}
}

func (m *mockJobStore) GetBacktestJob(_ context.Context, jobID string) (map[string]any, error) {
	j, ok := m.jobs[jobID]
	if !ok {
		return nil, nil
	}
	return j, nil
}

func (m *mockJobStore) ListBacktestJobs(_ context.Context, limit int) ([]map[string]any, error) {
	var out []map[string]any
	for i := len(m.order) - 1; i >= 0 && len(out) < limit; i-- {
		if j, ok := m.jobs[m.order[i]]; ok {
			out = append(out, j)
		}
	}
	return out, nil
}

// jobRow 是读面 map 形状的一行（与 storage 适配层同形）。
func jobRow(id string) map[string]any {
	return map[string]any{
		"id":            id,
		"strategy_id":   "momentum",
		"params":        []byte("{}"),
		"universe":      "600000.SH",
		"start_date":    "2026-01-01",
		"end_date":      "2026-03-01",
		"status":        "completed",
		"result_status": "completed",
		"result":        []byte(`{"total_return":0.1}`),
	}
}

type fakeResultFace struct {
	id      string
	status  string
	payload []byte
	err     error
}

func (f *fakeResultFace) SaveBacktestResult(_ context.Context, runID, resultStatus string, result []byte) error {
	f.id, f.status, f.payload = runID, resultStatus, result
	return f.err
}

func newTestJobService(t *testing.T, opts ...JobServiceOption) (*JobService, *mockJobStore) {
	t.Helper()
	store := newMockJobStore()
	return NewJobService(store, zerolog.Nop(), opts...), store
}

func TestJobService_RequiresStore(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewJobService(nil) must panic (read face without a store is a wiring error)")
		}
	}()
	NewJobService(nil, zerolog.Nop())
}

func TestJobService_GetJob(t *testing.T) {
	svc, store := newTestJobService(t)
	store.jobs["j1"] = jobRow("j1")
	store.order = []string{"j1"}

	got, err := svc.GetJob(context.Background(), "j1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "j1", got.ID)
	assert.Equal(t, "momentum", got.StrategyID)
	assert.Equal(t, "completed", got.Status)
	assert.JSONEq(t, `{"total_return":0.1}`, string(got.Result))
}

func TestJobService_GetJob_NotFound(t *testing.T) {
	svc, _ := newTestJobService(t)
	got, err := svc.GetJob(context.Background(), "missing")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestJobService_ListJobs(t *testing.T) {
	svc, store := newTestJobService(t)
	store.jobs["j1"] = jobRow("j1")
	store.jobs["j2"] = jobRow("j2")
	store.order = []string{"j1", "j2"}

	jobs, err := svc.ListJobs(context.Background(), 0)
	require.NoError(t, err)
	require.Len(t, jobs, 2)
	// 默认 limit=20 且按倒序（最近优先）。
	assert.Equal(t, "j2", jobs[0].ID)
	assert.Equal(t, "j1", jobs[1].ID)
}

func TestJobService_ListJobs_DefaultLimit(t *testing.T) {
	svc, store := newTestJobService(t)
	store.jobs["only"] = jobRow("only")
	store.order = []string{"only"}

	jobs, err := svc.ListJobs(context.Background(), 0)
	require.NoError(t, err)
	assert.Len(t, jobs, 1)
}

func TestJobService_SaveSyncResult_WritesResultFace(t *testing.T) {
	face := &fakeResultFace{}
	svc, _ := newTestJobService(t, WithResultFace(face))

	resp := &contracts.BacktestResponse{
		ID: "sync-1", Strategy: "momentum", Status: contracts.StatusCompleted,
	}
	require.NoError(t, svc.SaveSyncResult(context.Background(), resp))
	assert.Equal(t, "sync-1", face.id)
	assert.Equal(t, "completed", face.status)
	var stored contracts.BacktestResponse
	require.NoError(t, json.Unmarshal(face.payload, &stored))
	assert.Equal(t, "sync-1", stored.ID)
}

func TestJobService_SaveSyncResult_InvalidKeepsResultStatus(t *testing.T) {
	face := &fakeResultFace{}
	svc, _ := newTestJobService(t, WithResultFace(face))

	resp := &contracts.BacktestResponse{ID: "sync-2", Status: contracts.StatusInvalid}
	require.NoError(t, svc.SaveSyncResult(context.Background(), resp))
	assert.Equal(t, "invalid", face.status)
}

func TestJobService_SaveSyncResult_RequiresResultFace(t *testing.T) {
	svc, _ := newTestJobService(t)
	err := svc.SaveSyncResult(context.Background(), &contracts.BacktestResponse{ID: "sync-3"})
	require.Error(t, err, "结果面未装配必须 fail-closed，不得回退到已退役的本地队列")
	assert.Contains(t, err.Error(), "result face not configured")
}

func TestJobService_SaveSyncResult_RejectsEmptyID(t *testing.T) {
	svc, _ := newTestJobService(t, WithResultFace(&fakeResultFace{}))
	err := svc.SaveSyncResult(context.Background(), &contracts.BacktestResponse{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty id")
}

func TestJobService_SaveSyncResult_PropagatesStoreError(t *testing.T) {
	face := &fakeResultFace{err: errors.New("db down")}
	svc, _ := newTestJobService(t, WithResultFace(face))
	err := svc.SaveSyncResult(context.Background(), &contracts.BacktestResponse{ID: "sync-4"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "db down")
}
