package job

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-contracts/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// OBS-01 / B6-3：无效运行的结论是 invalid，不是生命周期失败 —— 行写
// status=completed + result_status=invalid，结果体（含 invalid_reasons）保留。

// invalidRunner 返回一次「无效运行」的响应（空票池 / 0 成交）。
type invalidRunner struct{}

func (invalidRunner) RunBacktest(ctx context.Context, req contracts.BacktestRequest) (*contracts.BacktestResponse, error) {
	return &contracts.BacktestResponse{
		Status:          "invalid",
		InvalidReasons:  []string{"empty_universe", "zero_trades"},
		UniverseMaxSize: intPtr(0),
		TotalTrades:     0,
	}, nil
}

func waitJobSettled(t *testing.T, store *mockJobStore, jobID string) map[string]any {
	t.Helper()
	var got map[string]any
	require.Eventually(t, func() bool {
		m, err := store.GetBacktestJob(context.Background(), jobID)
		if err != nil || m == nil {
			return false
		}
		s, _ := m["status"].(string)
		if s == "completed" || s == "failed" {
			got = m
			return true
		}
		return false
	}, 3*time.Second, 10*time.Millisecond, "作业未在预期时间内收尾")
	return got
}

func TestJobService_InvalidRunPersistsAsInvalidResult(t *testing.T) {
	store := newMockJobStore()
	registry := contracts.NewEngineRegistry()
	registry.Register(contracts.DefaultKernel, invalidRunner{})
	svc := NewJobService(store, registry, zerolog.Nop())

	job, err := svc.CreateJob(context.Background(), CreateJobRequest{
		StrategyID: "momentum",
		Universe:   "CSI300",
		StartDate:  "2024-01-02",
		EndDate:    "2024-02-02",
	})
	require.NoError(t, err)

	got := waitJobSettled(t, store, job.ID)
	assert.Equal(t, "completed", got["status"], "invalid 的队列生命周期仍是 completed")
	assert.Equal(t, "invalid", got["result_status"], "结果结论必须落 invalid")
	if msg, ok := got["error_msg"].(string); ok {
		assert.Empty(t, msg, "invalid 不是引擎故障，error 必空")
	}

	raw, _ := got["result"].([]byte)
	require.NotEmpty(t, raw, "invalid 不得丢弃结果体（下游要按三值读 status）")
	var body map[string]any
	require.NoError(t, json.Unmarshal(raw, &body))
	assert.Equal(t, "invalid", body["status"], "结果体 status 与 result_status 同源")
	assert.NotEmpty(t, body["invalid_reasons"], "原因必须随结果体保留")
}

func TestJobService_ValidRunStillCompleted(t *testing.T) {
	store := newMockJobStore()
	registry := contracts.NewEngineRegistry()
	registry.Register(contracts.DefaultKernel, &fakeRunner{})
	svc := NewJobService(store, registry, zerolog.Nop())

	job, err := svc.CreateJob(context.Background(), CreateJobRequest{
		StrategyID: "momentum",
		Universe:   "600000.SH,600001.SH",
		StartDate:  "2020-01-01",
		EndDate:    "2023-12-31",
	})
	require.NoError(t, err)

	got := waitJobSettled(t, store, job.ID)
	assert.Equal(t, "completed", got["status"], "正常作业照旧 completed —— 护栏不误伤")
	assert.Equal(t, "completed", got["result_status"], "有效结果的结论是 completed")
}

// intPtr 返回 int 的指针（OBS-01 审查修复：UniverseMaxSize 改为 *int，
// nil = 未评估、0 = 明确为空 —— 未知不等于空）。
func intPtr(v int) *int { return &v }
