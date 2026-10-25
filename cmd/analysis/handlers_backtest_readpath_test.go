package main

// OBS-01 切片 2 的 handler 层行为腿：GET /api/backtest/:id/report 必须
// **透传**引擎内存态里的真实状态（含 "invalid"）并附上 invalid_reasons，
// 而不是像缺陷前那样硬编码 status="completed"。
//
// 为什么能在**没有真库**的情况下建出「带内存态的引擎」：Engine.StateStore()
// 是导出的，且 NewEngineWithOptions / NewNoopStateStore /
// marketdata.NewInMemoryProvider 也都有导出构造器 —— 直接 Put 一个 "invalid"
// 状态即可命中 in-memory 读路径，无需跑真实回测（真库只读是别处的约束，
// 这里干脆不碰库）。
//
// 这条腿钉住的正是缺陷本体：把 Engine.ResponseFromState（K9 后的唯一
// state→response 透传点，buildResponseFromState 是其薄包装）里的
// `Status: status` 改回 `Status: "completed"`（或把闸门收回只认
// "completed"），这里立刻红。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/ruoxizhnya/tellurion-contracts/engine"
	"tellurion/pkg/backtest"
	"tellurion/pkg/marketdata"
)

// newReadPathEngine 构造一个带 NoopStateStore 的内存引擎（不触库、不跑回测）。
func newReadPathEngine(t *testing.T) *backtest.Engine {
	t.Helper()
	eng, err := backtest.NewEngineWithOptions(
		backtest.Config{},
		marketdata.NewInMemoryProvider(),
		backtest.WithStateStore(backtest.NewNoopStateStore()),
		backtest.WithLogger(zerolog.Nop()),
	)
	require.NoError(t, err)
	return eng
}

// putBacktestState 把一个终态（completed / invalid）直接塞进引擎的 state store。
func putBacktestState(eng *backtest.Engine, id, status string, reasons []string) {
	day := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	eng.StateStore().Put(id, &backtest.BacktestState{
		ID:     id,
		Status: status,
		Params: domain.BacktestParams{StrategyName: "momentum", StartDate: day, EndDate: day},
		Result: &domain.BacktestResult{
			StartDate:      day,
			EndDate:        day,
			InvalidReasons: reasons,
		},
	})
}

func getReport(t *testing.T, eng *backtest.Engine, id string) *httptest.ResponseRecorder {
	t.Helper()
	r := gin.New()
	// K10：注册表装配 go 内核（读路径不涉路由，但 registerBacktestRoutes
	// 签名要求传入注册表）。
	reg := contracts.NewEngineRegistry()
	reg.Register(contracts.DefaultKernel, eng)
	// jobService 传 nil：这两条腿都命中内存读路径，不碰 job 服务。
	registerBacktestRoutes(r, eng, reg, nil, nil, nil, zerolog.Nop())

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/backtest/"+id+"/report", nil))
	return w
}

// 核心腿：无效运行在 GET /:id/report 上必须 status=invalid + invalid_reasons，
// 且可读（200）—— 不能是硬编码的 completed，也不能是 404。
func TestBacktestReport_TransmitsInvalidStatus(t *testing.T) {
	eng := newReadPathEngine(t)
	reasons := []string{"empty_universe", "zero_trades", "garbage_metric"}
	putBacktestState(eng, "bt-invalid", "invalid", reasons)

	w := getReport(t, eng, "bt-invalid")
	require.Equal(t, http.StatusOK, w.Code, "无效运行必须可读（200），实际 body=%s", w.Body.String())

	var got struct {
		ID             string   `json:"id"`
		Status         string   `json:"status"`
		InvalidReasons []string `json:"invalid_reasons"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got), "响应不是 JSON: %s", w.Body.String())
	assert.Equal(t, "invalid", got.Status, "GET report 必须透传 invalid，不能硬编码 completed")
	assert.Equal(t, reasons, got.InvalidReasons, "响应必须带上 InvalidReasons")
	assert.Equal(t, "bt-invalid", got.ID)
}

// 反证腿：有效运行在 GET /:id/report 上仍 status=completed、无 invalid_reasons
// —— 正常路径行为一字不变。
func TestBacktestReport_CompletedStaysCompleted(t *testing.T) {
	eng := newReadPathEngine(t)
	putBacktestState(eng, "bt-ok", "completed", nil)

	w := getReport(t, eng, "bt-ok")
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

	var got struct {
		Status         string   `json:"status"`
		InvalidReasons []string `json:"invalid_reasons"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "completed", got.Status, "正常路径行为必须与改前一致")
	assert.Empty(t, got.InvalidReasons, "有效运行不得带 invalid_reasons")
}

// seedStoredJob 落一条已收尾的作业（走 JobStore 的写路径，模拟同步/异步
// 两条路径落库后的形态），返回 router。
func seedStoredJob(t *testing.T, id, resultStatus, body string) (*gin.Engine, *memJobStore) {
	t.Helper()
	eng := newReadPathEngine(t)
	store := newMemJobStore()
	reg := contracts.NewEngineRegistry()
	reg.Register(contracts.DefaultKernel, eng)
	svc := backtest.NewJobService(store, reg, zerolog.Nop())

	require.NoError(t, store.CreateBacktestJob(context.Background(), map[string]any{
		"id":          id,
		"strategy_id": "momentum",
		"params":      []byte(`{}`),
		"universe":    "CSI300",
		"start_date":  "2024-01-02",
		"end_date":    "2024-02-02",
		"status":      "pending",
	}))
	require.NoError(t, store.UpdateJobStarted(context.Background(), id))
	require.NoError(t, store.UpdateJobCompleted(context.Background(), id, resultStatus, []byte(body)))

	r := gin.New()
	registerBacktestRoutes(r, eng, reg, svc, nil, nil, zerolog.Nop())
	return r, store
}

// B6-3 核心腿（持久读面）：内存态里没有这次运行、库里有 invalid 行时，
// GET /:id/report 必须 200 + status=invalid + reasons —— 不得因为
// status != "completed" 落 404（那是把 invalid 读成 failed 的旧行为）。
func TestBacktestReport_StoredInvalidRowStaysReadable(t *testing.T) {
	const id = "bt-stored-invalid"
	// 结果体故意不带 status：三值的唯一权威是持久列的 result_status，
	// 回落到结果体会在这里露馅（读成 completed）。
	r, _ := seedStoredJob(t, id, "invalid",
		`{"id":"bt-stored-invalid","invalid_reasons":["no_trades"]}`)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/backtest/"+id+"/report", nil))
	require.Equal(t, http.StatusOK, w.Code, "invalid 行必须可读（200），body=%s", w.Body.String())

	var got struct {
		Status         string   `json:"status"`
		InvalidReasons []string `json:"invalid_reasons"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got), "响应不是 JSON: %s", w.Body.String())
	assert.Equal(t, "invalid", got.Status, "report 的 status 必须与 result_status 同源")
	assert.Equal(t, []string{"no_trades"}, got.InvalidReasons, "结果体不得被丢")
}

// B6-3 反证腿：GET /api/backtest/:id 两轴都可见 —— status=completed 是
// 队列生命周期，result_status=invalid 是结果结论，合并其一都是回归。
func TestBacktestGetJob_ExposesBothAxes(t *testing.T) {
	const id = "bt-stored-invalid"
	r, _ := seedStoredJob(t, id, "invalid",
		`{"id":"bt-stored-invalid","status":"invalid","invalid_reasons":["no_trades"]}`)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/backtest/"+id, nil))
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

	var got struct {
		Status       string `json:"status"`
		ResultStatus string `json:"result_status"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "completed", got.Status, "invalid 的队列生命周期仍是 completed")
	assert.Equal(t, "invalid", got.ResultStatus, "结果结论必须可见")
}
