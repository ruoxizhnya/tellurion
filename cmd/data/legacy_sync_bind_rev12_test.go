package main

// REV-12 回归护栏：legacy `/sync/*` 端点不再把绑定错误静默吞成默认值。
//
// 旧行为：六处 `if err := c.ShouldBindJSON(&req); err != nil { /* 用默认 */ }`
// 把 err 整个丢弃——一次手滑 POST（畸形 JSON、字段类型错）就会创建
// **全市场**同步作业（≈5900 只逐只调 Tushare 付费配额），还返回 202。
//
// 新行为：errors.Is(err, io.EOF)（空 body）才走默认；其余绑定错误 400 点名。
//
// 说明：400 腿不需要 DB（在碰 store/jobService 之前就返回）；202 腿用真实
// store 读票池（TEST_DATABASE_DSN，未设则跳过——与仓内 DB 测试同一纪律），
// 写侧用内存 JobStore，不往开发库落作业行。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	stdSync "sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/storage"
	"tellurion/pkg/sync"
	"tellurion/pkg/testenv"
)

// rev12JobStore 是内存 JobStore：CreateJob 只落内存，不碰开发库。
type rev12JobStore struct {
	mu   stdSync.Mutex
	jobs map[string]*sync.Job
}

func newRev12JobStore() *rev12JobStore {
	return &rev12JobStore{jobs: make(map[string]*sync.Job)}
}

func (s *rev12JobStore) CreateSyncJob(_ context.Context, job *sync.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.ID] = job
	return nil
}
func (s *rev12JobStore) GetSyncJob(_ context.Context, id string) (*sync.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.jobs[id], nil
}
func (s *rev12JobStore) UpdateSyncJobIfStatus(context.Context, *sync.Job, ...sync.JobStatus) (bool, error) {
	return true, nil
}
func (s *rev12JobStore) ListSyncJobs(context.Context, sync.JobStatus, int) ([]*sync.Job, error) {
	return nil, nil
}
func (s *rev12JobStore) ListSyncJobsByType(context.Context, sync.JobType, int) ([]*sync.Job, error) {
	return nil, nil
}
func (s *rev12JobStore) DeleteSyncJob(context.Context, string) error { return nil }

// rev12Routes 注册六个 legacy 端点到裸 router（不套 auth：鉴权不在本单范围）。
func rev12Routes(t *testing.T, h *SyncHandler) *gin.Engine {
	t.Helper()
	r := gin.New()
	r.POST("/sync/stocks", h.syncStocksHandler)
	r.POST("/sync/ohlcv/all", h.syncAllOHLCVHandler)
	r.POST("/sync/fundamentals", h.syncFundamentalsHandler)
	r.POST("/sync/calendar", h.syncCalendarHandler)
	r.POST("/sync/dividends", h.syncDividendsHandler)
	r.POST("/sync/splits", h.syncSplitsHandler)
	return r
}

func rev12Post(r *gin.Engine, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

var rev12Paths = []string{
	"/sync/stocks", "/sync/ohlcv/all", "/sync/fundamentals",
	"/sync/calendar", "/sync/dividends", "/sync/splits",
}

// TestLegacySync_RejectsMalformedBody：畸形 JSON 与字段类型错 → 400（六处）。
// store 为 nil 也不会 panic —— 证明 400 发生在任何资源访问之前。
func TestLegacySync_RejectsMalformedBody(t *testing.T) {
	h := &SyncHandler{jobService: sync.NewJobService(newRev12JobStore())}
	r := rev12Routes(t, h)

	// 类型错的身体要打在**该端点真实存在**的字段上（未知字段会被 JSON 绑定
	// 忽略，那样构造不出绑定错误——这是绑定语义，不是缺陷）。
	typeErrBody := map[string]string{
		"/sync/stocks":       `{"exchange": 123}`, // Exchange string
		"/sync/ohlcv/all":    `{"symbols": 123}`,  // Symbols []string
		"/sync/fundamentals": `{"symbols": 123}`,
		"/sync/calendar":     `{"exchange": 123}`, // Exchange string
		"/sync/dividends":    `{"symbols": 123}`,
		"/sync/splits":       `{"symbols": 123}`,
	}
	for _, path := range rev12Paths {
		t.Run(path+" 畸形 JSON", func(t *testing.T) {
			w := rev12Post(r, path, `{"symbols":`)
			assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		})
		t.Run(path+" 字段类型错", func(t *testing.T) {
			w := rev12Post(r, path, typeErrBody[path])
			assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		})
	}
}

// TestLegacySync_EmptyBodyUsesDefaults：空 body 仍走默认并创建作业（202）——
// 兼容性保证：本次收紧不得改变空 body 的既有语义。
func TestLegacySync_EmptyBodyUsesDefaults(t *testing.T) {
	store := rev12Store(t) // 无 DSN 时 skip
	jobStore := newRev12JobStore()
	h := &SyncHandler{jobService: sync.NewJobService(jobStore), store: store}
	r := rev12Routes(t, h)

	for _, path := range rev12Paths {
		t.Run(path, func(t *testing.T) {
			w := rev12Post(r, path, "")
			assert.Equal(t, http.StatusAccepted, w.Code, w.Body.String())

			var resp struct {
				JobID string `json:"job_id"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			require.NotEmpty(t, resp.JobID)
			jobStore.mu.Lock()
			job, ok := jobStore.jobs[resp.JobID]
			jobStore.mu.Unlock()
			require.True(t, ok, "作业必须经 jobService 创建")
			assert.Equal(t, sync.JobStatusPending, job.Status)
		})
	}

	// 空 body 的 stocks 默认 ListStatus 必须是 "L"（旧语义保留）。
	w := rev12Post(r, "/sync/stocks", "")
	var resp struct {
		JobID string `json:"job_id"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	jobStore.mu.Lock()
	job := jobStore.jobs[resp.JobID]
	jobStore.mu.Unlock()
	var params sync.StocksSyncParams
	require.NoError(t, json.Unmarshal(job.Params, &params))
	assert.Equal(t, "L", params.ListStatus)
}

// rev12Store 连真库读票池；未设 TEST_DATABASE_DSN 时显式跳过。
func rev12Store(t *testing.T) *storage.PostgresStore {
	t.Helper()
	dsn := testenv.DatabaseDSN()
	if dsn == "" {
		testenv.SkipNoDB(t)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := storage.NewPostgresStoreConnected(ctx, dsn)
	if err != nil {
		t.Fatalf("[testdb] TEST_DATABASE_DSN 已设置但连不上（fail-loud）: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}
