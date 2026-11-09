// K10 · handler 层的内核路由验收腿（POST /api/backtest 两条分支）。
//
// 覆盖任务验收的 HTTP 维度：
//  1. 正证据（同步）：注册 fake → kernel=fake 走 fake，200 响应带 fake
//     marker，且经 SaveSyncResult 写结果面（同一条结论面路径）；
//  2. 反证腿（同步）：kernel=nautilus（未注册）→ 400 点名内核名，
//     且**零产物** —— 没有任何内核回执（证明没有静默回退 go）；
//  3. 回归腿（同步）：不带 kernel → 请求穿过路由进入 go 引擎（以引擎
//     的「日历未同步」409 收场为证），与 K10 前行为一致；
//  4. 参数维度 fail-closed（异步）：engine_params 未知名 → 400 点名，
//     与同步路径同一道 ValidateRunSpec 闸门（K9 原则不放松）。
//  5. 异步入口（SVC-ORCH-G3 收尾腿）：未配 ORCHESTRATION_URL → 503 ——
//     core 的本地队列已退役，没有第二真相可回退。
//
// 破坏验证（开发期实测）：把 EngineRegistry.Resolve 改成「未知名回退
// go」，腿 2 的 400/零产物断言立刻红（已还原）。

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/tellurion-contracts/engine"
	"tellurion/pkg/backtest"
)

// kernelFakeRunner 带 marker 的 EngineRunner stub —— 产物响应写 marker，
// 用于证明「这次是 fake 跑的，不是 go」。
type kernelFakeRunner struct {
	mu    sync.Mutex
	reqs  []contracts.BacktestRequest
	calls int
}

func (f *kernelFakeRunner) RunBacktest(ctx context.Context, req contracts.BacktestRequest) (*contracts.BacktestResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.reqs = append(f.reqs, req)
	return &contracts.BacktestResponse{
		ID:       "bt-fake",
		Status:   contracts.StatusCompleted,
		Strategy: "from-fake",
	}, nil
}

func (f *kernelFakeRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// memJobStore 是 backtest.JobStore 的只读内存实现（handler 层测试专用）。
// 读路径返回深拷贝，避免与被测 goroutine 共享可变 map（P0-6 的教训）。
type memJobStore struct {
	mu    sync.Mutex
	jobs  map[string]map[string]any
	order []string
}

func newMemJobStore() *memJobStore {
	return &memJobStore{jobs: make(map[string]map[string]any)}
}

func cloneJobMapForTest(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		switch x := v.(type) {
		case map[string]any:
			dst[k] = cloneJobMapForTest(x)
		case []byte:
			cp := make([]byte, len(x))
			copy(cp, x)
			dst[k] = cp
		case time.Time:
			dst[k] = x
		case *time.Time:
			if x != nil {
				t := *x
				dst[k] = &t
			}
		default:
			dst[k] = v
		}
	}
	return dst
}

func (m *memJobStore) GetBacktestJob(ctx context.Context, jobID string) (map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[jobID]
	if !ok {
		return nil, nil
	}
	return cloneJobMapForTest(j), nil
}

func (m *memJobStore) ListBacktestJobs(ctx context.Context, limit int) ([]map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []map[string]any
	for i := len(m.order) - 1; i >= 0 && len(out) < limit; i-- {
		if j, ok := m.jobs[m.order[i]]; ok {
			out = append(out, cloneJobMapForTest(j))
		}
	}
	return out, nil
}

func (m *memJobStore) put(id string, row map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.jobs[id]; !exists {
		m.order = append(m.order, id)
	}
	m.jobs[id] = cloneJobMapForTest(row)
}

func (m *memJobStore) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.jobs)
}

// memResultFace 把同步结论写进 memJobStore（代替 run.backtest_results），
// 让「同步回测经 SaveSyncResult 落库」这条断言在内存里仍可观测。
type memResultFace struct{ store *memJobStore }

func (f memResultFace) SaveBacktestResult(_ context.Context, runID, resultStatus string, result []byte) error {
	status := "completed"
	if resultStatus == "failed" {
		status = "failed"
	}
	f.store.put(runID, map[string]any{
		"id":            runID,
		"strategy_id":   "",
		"universe":      "",
		"start_date":    "",
		"end_date":      "",
		"params":        []byte("{}"),
		"status":        status,
		"result_status": resultStatus,
		"result":        result,
	})
	return nil
}

// newBacktestKernelRouter 装配：go 内核 = 内存引擎（newReadPathEngine），
// fake 内核 = kernelFakeRunner；JobService 只有读面 + 结果面写口。
func newBacktestKernelRouter(t *testing.T) (*gin.Engine, *memJobStore, *kernelFakeRunner) {
	t.Helper()
	eng := newReadPathEngine(t)
	store := newMemJobStore()
	fake := &kernelFakeRunner{}
	reg := contracts.NewEngineRegistry()
	reg.Register(contracts.DefaultKernel, eng)
	reg.Register("fake", fake)
	jobSvc := backtest.NewJobService(store, zerolog.Nop(),
		backtest.WithResultFace(memResultFace{store: store}))
	r := gin.New()
	registerBacktestRoutes(r, eng, reg, jobSvc, nil, nil, nil, zerolog.Nop())
	return r, store, fake
}

func postBacktestKernel(t *testing.T, r *gin.Engine, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/backtest", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

// 腿 1（正证据 · 同步）：kernel=fake → 走 fake，响应带 marker 且落结果面。
func TestBacktestPost_FakeKernelRunsAndPersists(t *testing.T) {
	r, store, fake := newBacktestKernelRouter(t)

	w := postBacktestKernel(t, r, `{"strategy":"momentum","stock_pool":["600000.SH"],"start_date":"2024-01-02","end_date":"2024-02-02","kernel":"fake"}`)

	require.Equal(t, http.StatusOK, w.Code, "fake 内核的同步请求必须 200, body=%s", w.Body.String())
	assert.Contains(t, w.Body.String(), "from-fake", "响应必须是 fake 的产物")
	assert.Equal(t, 1, fake.callCount(), "fake runner 必须被调用一次")

	// 落库同 schema：经 SaveSyncResult 持久化，result JSON 带 fake marker。
	require.Equal(t, 1, store.count(), "fake 结果必须写进结果面")
	stored, err := store.GetBacktestJob(context.Background(), "bt-fake")
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "completed", stored["status"])
	resultJSON, _ := stored["result"].([]byte)
	assert.Contains(t, string(resultJSON), "from-fake", "落库的必须是 fake 的产物")
}

// 腿 2（反证腿 · 同步）：kernel=nautilus（未注册）→ 400 点名内核名，
// 零产物 —— 证明没有静默回退 go。
func TestBacktestPost_UnknownKernelFailClosed(t *testing.T) {
	r, store, fake := newBacktestKernelRouter(t)

	w := postBacktestKernel(t, r, `{"strategy":"momentum","stock_pool":["600000.SH"],"start_date":"2024-01-02","end_date":"2024-02-02","kernel":"nautilus"}`)

	require.Equal(t, http.StatusBadRequest, w.Code, "未注册内核必须 400, body=%s", w.Body.String())
	assert.Contains(t, w.Body.String(), "nautilus", "错误必须点名被拒的内核名")
	assert.Contains(t, w.Body.String(), "not registered", "错误必须说明未注册")

	// 反证本体：零产物 + fake 零调用。若路由回退 go，这里会出现一个
	// go 引擎产物。
	assert.Equal(t, 0, store.count(), "被拒请求不得留下任何内核产物")
	assert.Equal(t, 0, fake.callCount(), "fake runner 与本腿无关")
}

// 腿 3（回归腿 · 同步）：不带 kernel → 经 EffectiveKernel 解析到 go，
// 请求穿过路由进入引擎（内存引擎无日历 → 409），与 K10 前行为一致。
func TestBacktestPost_NoKernelStillReachesGoEngine(t *testing.T) {
	r, store, _ := newBacktestKernelRouter(t)

	w := postBacktestKernel(t, r, `{"strategy":"momentum","stock_pool":["600000.SH"],"start_date":"2024-01-02","end_date":"2024-02-02"}`)

	// 409 = 引擎的「日历未同步」分类错误 —— 请求确实穿过了路由闸门
	// 进入了 go 引擎（而不是被路由层以 400 拦死）。
	require.Equal(t, http.StatusConflict, w.Code, "不带 kernel 必须到达 go 引擎（日历错误 409）, body=%s", w.Body.String())
	assert.Equal(t, 0, store.count(), "引擎执行失败不得落库（SaveSyncResult 只在成功路径）")
}

// 腿 4（参数维度 · 异步）：engine_params 未知名 → 400 点名，与同步路径
// 同一道 ValidateRunSpec 闸门 —— K9 的 "rejected, not ignored" 原则在
// 内核路由接通后不放松。校验发生在任务面装配检查之前，故 tasks=nil 也可测。
func TestBacktestPost_AsyncRejectsUnknownEngineParams(t *testing.T) {
	r, _, _ := newBacktestKernelRouter(t)

	for _, tc := range []struct{ name, body, wantKey string }{
		{
			"go kernel unknown param",
			`{"strategy_id":"momentum","universe":"hs300","start_date":"2024-01-02","end_date":"2024-02-02","kernel":"go","engine_params":{"not_a_param":1}}`,
			"not_a_param",
		},
		{
			"fake kernel accepts no params",
			`{"strategy_id":"momentum","universe":"hs300","start_date":"2024-01-02","end_date":"2024-02-02","kernel":"fake","engine_params":{"anything":1}}`,
			"anything",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := postBacktestKernel(t, r, tc.body)
			require.Equal(t, http.StatusBadRequest, w.Code, "未知名 engine_params 必须 400, body=%s", w.Body.String())
			assert.Contains(t, w.Body.String(), tc.wantKey, "错误必须点名未知键")
			assert.Contains(t, w.Body.String(), "rejected, not ignored", "必须保持 fail-closed 拒绝措辞")
		})
	}
}

// 腿 5（异步入口 · SVC-ORCH-G3 收尾腿）：未配编排服务 → 503。
// 本地队列已退役，core 不再有「先塞本地队列」的回退路径。
func TestBacktestPost_AsyncWithoutOrchestrationIs503(t *testing.T) {
	r, store, _ := newBacktestKernelRouter(t)

	w := postBacktestKernel(t, r, `{"strategy_id":"momentum","universe":"600000.SH","start_date":"2024-01-02","end_date":"2024-02-02"}`)

	require.Equal(t, http.StatusServiceUnavailable, w.Code, "异步创建缺任务面必须 503, body=%s", w.Body.String())
	assert.Contains(t, w.Body.String(), "ORCHESTRATION_URL", "503 必须点名缺的配置")
	assert.Equal(t, 0, store.count(), "503 不得留下任何作业行")
}
