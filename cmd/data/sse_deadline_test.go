package main

// REV-10：SSE 进度流必须活过服务级 WriteTimeout。
//
// http.Server.WriteTimeout 是读完请求头后给裸连接设的**绝对**写截止——
// 不是「每次写之间的空闲超时」。data 服务的全局值是 30s，而 SSE 的使命是
// 按 2s ticker 跟随跑几分钟的作业：不清截止就必然在 30s 整被服务器掐断。
//
// 本测试把 30s 按比例缩到 2s（服务级），断言流能活过 4.5s 且读到多个
// progress 帧——这是机制级的真实复现（真 http.Server + 真 handler），
// 不依赖整栈起服务。

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	stdsync "sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/sync"
)

// rev10JobStore 是最小 JobStore：ID 为 "rev10" 的作业在 holdUntil 前一直
// running，之后转 completed（SSE handler 只读 jobService.GetJob）。
type rev10JobStore struct {
	mu        stdsync.Mutex
	holdUntil time.Time
}

func (s *rev10JobStore) CreateSyncJob(context.Context, *sync.Job) error { return nil }

func (s *rev10JobStore) GetSyncJob(_ context.Context, id string) (*sync.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := sync.JobStatusRunning
	if time.Now().After(s.holdUntil) {
		status = sync.JobStatusCompleted
	}
	return &sync.Job{ID: id, Status: status, CreatedAt: time.Now()}, nil
}

func (s *rev10JobStore) UpdateSyncJobIfStatus(context.Context, *sync.Job, ...sync.JobStatus) (bool, error) {
	return false, nil
}
func (s *rev10JobStore) ListSyncJobs(context.Context, sync.JobStatus, int) ([]*sync.Job, error) {
	return nil, nil
}
func (s *rev10JobStore) ListSyncJobsByType(context.Context, sync.JobType, int) ([]*sync.Job, error) {
	return nil, nil
}
func (s *rev10JobStore) DeleteSyncJob(context.Context, string) error { return nil }

func TestSSEProgress_SurvivesServerWriteTimeout(t *testing.T) {
	store := &rev10JobStore{holdUntil: time.Now().Add(30 * time.Second)}
	h := &SyncHandler{jobService: sync.NewJobService(store)}

	r := gin.New()
	r.GET("/api/sync/jobs/:id/progress", h.sseProgressHandler)

	srv := httptest.NewUnstartedServer(r)
	srv.Config.WriteTimeout = 2 * time.Second // 远小于流的观察窗口（4.5s）
	srv.Start()
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/sync/jobs/rev10/progress", nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "text/event-stream")

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err, "SSE 连接建立失败")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	progress := 0
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "event:progress") {
			progress++
		}
		if time.Since(start) >= 4500*time.Millisecond && progress >= 2 {
			break
		}
	}
	elapsed := time.Since(start)

	assert.GreaterOrEqual(t, elapsed, 4500*time.Millisecond,
		"流在 %.2fs 就结束了——服务级 WriteTimeout(2s) 把 SSE 掐断了（REV-10 回归）", elapsed.Seconds())
	assert.GreaterOrEqual(t, progress, 2, "应至少收到 2 个 progress 帧（2s ticker）")
	assert.NoError(t, sc.Err(), "中途断开会被读成 EOF/连接错误")
}
