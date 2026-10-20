package main

// REV-11 回归护栏：批量回测接口的并发安全与资源上界。
//
// 旧缺陷：handler 每请求先 batchEngine.SetConfig（裸写共享字段）再 Run
//（裸读同一字段）——两个并发请求 = data race + 配置串味；且 concurrency
// 由客户端直控无上限、tasks 数量无上限、Run 无 ctx 超时。
//
// 修复：Run 收 per-request config（引擎无状态）；handler 封顶（并发 16 /
// 任务 100）并加 30min ctx。本文件钉三件事：并发双请求不串味不竞态（配合
// -race 跑）、超限 400、边界值放行。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/backtest"
	"tellurion/pkg/backtest/contracts"
)

// rev11Runner 是最小 EngineRunner：立即返回 completed，记录调用次数。
type rev11Runner struct {
	mu    sync.Mutex
	calls int
}

func (r *rev11Runner) RunBacktest(ctx context.Context, req contracts.BacktestRequest) (*contracts.BacktestResponse, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	return &contracts.BacktestResponse{ID: req.Strategy, Status: contracts.StatusCompleted}, nil
}

func (r *rev11Runner) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func rev11Router(t *testing.T) (*gin.Engine, *rev11Runner) {
	t.Helper()
	runner := &rev11Runner{}
	be := backtest.NewBatchEngine(runner, nil, backtest.DefaultBatchConfig(), zerolog.Nop())
	r := gin.New()
	registerBatchRoutes(r, be, zerolog.Nop())
	return r, runner
}

func rev11Body(t *testing.T, tasks int, concurrency int) string {
	t.Helper()
	taskList := make([]map[string]any, 0, tasks)
	for i := 0; i < tasks; i++ {
		taskList = append(taskList, map[string]any{
			"id": fmt.Sprintf("t%d", i), "strategy": "momentum",
			"start_date": "2024-01-02", "end_date": "2024-03-01", "capital": 1e6,
		})
	}
	body, err := json.Marshal(map[string]any{
		"tasks":  taskList,
		"config": map[string]any{"concurrency": concurrency},
	})
	require.NoError(t, err)
	return string(body)
}

func rev11Post(r *gin.Engine, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/batch", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestBatchHandler_ConcurrentRequestsDoNotShareConfig：两个并发请求用不同
// concurrency，各自的结果只含自己的任务——这是「后到请求改掉前一个请求
// 正在用的配置」的结构性反证；配合 -race 跑时，旧的 SetConfig/Run 共享
// 字段写法会报 DATA RACE（本测试的作用之一就是让那条路径不可回退）。
func TestBatchHandler_ConcurrentRequestsDoNotShareConfig(t *testing.T) {
	r, runner := rev11Router(t)

	type resp struct {
		code   int
		total  int
		status string
	}
	results := make([]resp, 2)
	var wg sync.WaitGroup
	cases := []struct {
		tasks       int
		concurrency int
	}{{3, 1}, {7, 8}}

	for i, tc := range cases {
		wg.Add(1)
		go func(i int, tasks, concurrency int) {
			defer wg.Done()
			w := rev11Post(r, rev11Body(t, tasks, concurrency))
			var report struct {
				TotalTasks int    `json:"total_tasks"`
				Status     string `json:"status"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &report)
			results[i] = resp{code: w.Code, total: report.TotalTasks, status: report.Status}
		}(i, tc.tasks, tc.concurrency)
	}
	wg.Wait()

	for i, tc := range cases {
		assert.Equal(t, http.StatusOK, results[i].code, "请求 %d 应成功", i)
		assert.Equal(t, tc.tasks, results[i].total, "请求 %d 的报告必须只含自己的任务（配置/结果不得串味）", i)
		assert.Equal(t, "completed", results[i].status)
	}
	assert.Equal(t, 10, runner.callCount(), "两次请求共 3+7 个任务，各跑一次")
}

// TestBatchHandler_RejectsOverLimit：超限 400，边界值放行。
func TestBatchHandler_RejectsOverLimit(t *testing.T) {
	r, _ := rev11Router(t)

	t.Run("并发超限 17 → 400", func(t *testing.T) {
		w := rev11Post(r, rev11Body(t, 1, maxBatchConcurrency+1))
		assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		assert.Contains(t, w.Body.String(), "concurrency too high")
	})

	t.Run("任务数超限 101 → 400", func(t *testing.T) {
		w := rev11Post(r, rev11Body(t, maxBatchTasks+1, 1))
		assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		assert.Contains(t, w.Body.String(), "too many tasks")
	})

	t.Run("边界值放行：并发 16 / 任务 100", func(t *testing.T) {
		w := rev11Post(r, rev11Body(t, maxBatchTasks, maxBatchConcurrency))
		assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
	})
}
