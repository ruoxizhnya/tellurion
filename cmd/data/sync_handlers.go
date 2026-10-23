// Package main provides HTTP handlers for data synchronization.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"tellurion/pkg/httpserver"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"tellurion/pkg/data"
	"tellurion/pkg/data/source"
	"tellurion/pkg/logging"
	"tellurion/pkg/storage"
	"tellurion/pkg/sync"
)

// maxListJobsLimit 是 list jobs 的 limit 封顶（REV-23(7)）：与同仓既有
// 先例对齐（backtest jobs cap 100）——limit 直接进 SQL LIMIT，无上限
// 意味着一个 ?limit=99999999 会把整表拉进内存。
const maxListJobsLimit = 100

// collectionPayload 构造集合端点的响应体：数组字段 + count。
// DATA-14：encoding/json 把 nil slice 编成 null，而 docs/openapi.yaml 对
// jobs/schedules 声明的是 array —— 空结果必须是 []。消费方（前端 .map
// 最典型）拿到 null 会直接炸。
func collectionPayload[T any](key string, items []T) gin.H {
	if items == nil {
		items = []T{}
	}
	return gin.H{key: items, "count": len(items)}
}

// SyncHandler holds dependencies for sync-related HTTP handlers.
type SyncHandler struct {
	jobService    *sync.JobService
	queue         *sync.Queue
	workerPool    *sync.WorkerPool
	scheduler     *sync.Scheduler
	store         *storage.PostgresStore
	tushareClient *data.TushareClient
	dataCache     *data.DataCache
	// registry 是多源取数链（DATA-10）。经它取数的执行器现在有两个：
	// capital_flow（DATA-10 接的线，DBT-09 换了链首选）与 top_list
	// （DBT-10 接的线）—— 其余执行器仍走 e.tc 直连线，那条路径是 DATA-08
	// 认定的现状，一源一单，不在这里顺手归一。
	registry *source.Registry
}

// NewSyncHandler creates a new SyncHandler.
func NewSyncHandler(store *storage.PostgresStore, tc *data.TushareClient, dc *data.DataCache, registry *source.Registry) *SyncHandler {
	queue := sync.NewQueue(store)
	jobService := sync.NewJobService(store)
	// Wake idle workers when a job becomes pending outside Queue.Enqueue
	// (CreateJob / RetryJob write to the store directly). Without this,
	// workers blocked in WaitForJob never dequeue HTTP-created jobs.
	jobService.SetPendingNotifier(queue.NotifyJobAvailable)
	workerPool := sync.NewWorkerPool(queue, 3)
	// AUD-49: cancelling a running job has to reach the goroutine that is
	// executing it. Settling the row is not enough — the executor keeps going
	// until its context is cancelled, which is why the endpoint used to answer
	// 200 while `processed_items` kept climbing. This is the other half.
	jobService.SetRunningCanceller(workerPool.Cancel)
	scheduler := sync.NewScheduler(store, queue)

	// Register job types for scheduler
	scheduler.RegisterJobType("stocks", sync.JobTypeStocks)
	scheduler.RegisterJobType("ohlcv", sync.JobTypeOHLCV)
	scheduler.RegisterJobType("ohlcv_all", sync.JobTypeOHLCVAll)
	scheduler.RegisterJobType("fundamentals", sync.JobTypeFundamentals)
	scheduler.RegisterJobType("calendar", sync.JobTypeCalendar)
	scheduler.RegisterJobType("dividends", sync.JobTypeDividends)
	scheduler.RegisterJobType("splits", sync.JobTypeSplits)
	// DBT-10 的裁决：top_list **进** cron 名单，capital_flow **不进**。
	// 判据是配额形状，不是数据重要性：
	//   - top_list 一次作业 = 一个交易日窗口的**整市场**翻页，≈1–2 发请求
	//     （页到空为止），日更成本可忽略，且 after_close 语义天然对齐「收盘后跑」；
	//   - capital_flow 逐 symbol 一发，全池 ≈5900 发，免费档 200 req/min
	//     ⇒ 排成日更就是把当天配额整桶烧在一张**目前零读方**的表上
	//     （2026-10-19 grep：无 handler / 无因子 / 无前端读 capital_flow）。
	//     DATA-10 当初把它排除在名单外是刻意的，本单不动它。
	// 名单只影响 `sync_schedules` 的校验（不在名单里的类型排不进 cron），
	// 没有任何 schedule 行存在 ⇒ 加进来不会自动开始跑。
	scheduler.RegisterJobType("top_list", sync.JobTypeTopList)

	return &SyncHandler{
		jobService:    jobService,
		queue:         queue,
		workerPool:    workerPool,
		scheduler:     scheduler,
		store:         store,
		tushareClient: tc,
		dataCache:     dc,
		registry:      registry,
	}
}

// StartWorkerPool starts the background worker pool.
func (h *SyncHandler) StartWorkerPool() {
	h.workerPool.Start()
}

// StopWorkerPool gracefully stops the worker pool.
func (h *SyncHandler) StopWorkerPool() {
	h.workerPool.Stop()
}

// StartScheduler starts the cron scheduler and loads every **active** schedule from
// the store. SYNC-01: this had no caller anywhere in the repository — not in
// production and not in tests — so `sync_schedules` rows with `is_active=true`
// could never fire and `GET /api/sync/scheduler/stats` was permanently 0/0. A `Start()`
// that is correct but never invoked is invisible to both the compiler and behaviour
// tests (a behaviour test calls it itself), which is why
// `cmd/data/wiring_guard_test.go` guards the *call site*.
//
// The error is returned rather than logged-and-swallowed here: the composition root
// decides the blast radius (see main.go).
func (h *SyncHandler) StartScheduler() error {
	return h.scheduler.Start()
}

// StopScheduler stops the cron scheduler and waits for jobs already dispatched to
// finish. Called from the shutdown path so the scheduler cannot create new work
// after HTTP has already drained.
func (h *SyncHandler) StopScheduler() {
	h.scheduler.Stop()
}

// RegisterExecutors registers all job executors.
func (h *SyncHandler) RegisterExecutors() {
	h.workerPool.RegisterExecutor(&stocksExecutor{tc: h.tushareClient, store: h.store})
	h.workerPool.RegisterExecutor(&ohlcvExecutor{tc: h.tushareClient, store: h.store})
	h.workerPool.RegisterExecutor(&ohlcvAllExecutor{tc: h.tushareClient, store: h.store})
	h.workerPool.RegisterExecutor(&fundamentalsExecutor{tc: h.tushareClient, store: h.store})
	h.workerPool.RegisterExecutor(&calendarExecutor{tc: h.tushareClient, store: h.store})
	h.workerPool.RegisterExecutor(&dividendsExecutor{tc: h.tushareClient, store: h.store})
	h.workerPool.RegisterExecutor(&splitsExecutor{tc: h.tushareClient, store: h.store})
	// DATA-10：第一条接进作业层的多源链。registry 为 nil 时执行器会在
	// Registry.Fetch 处 fail-loud（`etl: nil registry`），作业如实失败。
	h.workerPool.RegisterExecutor(&capitalFlowExecutor{reg: h.registry, persist: h.store})
	// DBT-10：第二条接进作业层的多源链（top_list: [eastmoney_toplist]）。
	// 同一条 nil-registry 纪律：装配漏了 Registry 就当页失败、作业如实失败。
	h.workerPool.RegisterExecutor(&topListExecutor{reg: h.registry, persist: h.store})
}

// ---- Legacy Sync Endpoints (transformed to job creation) ----

// syncStocksHandler creates a job to sync stocks.
func (h *SyncHandler) syncStocksHandler(c *gin.Context) {
	ctx := c.Request.Context()
	var req sync.StocksSyncParams
	if err := c.ShouldBindJSON(&req); err != nil {
		// REV-12：只有「空 body」才走默认值；畸形 JSON / 字段类型错 一律 400。
		// 旧代码把 err 整个丢弃 —— 一次手滑 POST 就会创建全市场同步作业。
		if !errors.Is(err, io.EOF) {
			httpserver.Error(c, http.StatusBadRequest, err)
			return
		}
		req.ListStatus = "L"
	}

	job, err := h.jobService.CreateJob(ctx, sync.JobTypeStocks, req)
	if err != nil {
		httpserver.Error(c, http.StatusInternalServerError, err)
		return
	}

	c.JSON(http.StatusAccepted, gin.H{
		"message": "stocks sync job created",
		"job_id":  job.ID,
		"status":  job.Status,
	})
}

// syncOHLCVHandler creates a job to sync OHLCV data.
func (h *SyncHandler) syncOHLCVHandler(c *gin.Context) {
	ctx := c.Request.Context()
	var req sync.OHLCVSyncParams
	if err := c.ShouldBindJSON(&req); err != nil {
		httpserver.Error(c, http.StatusBadRequest, err)
		return
	}

	if len(req.Symbols) == 0 {
		allStocks, err := h.store.GetAllStocks(ctx)
		if err != nil {
			httpserver.Wrap(c, http.StatusInternalServerError, err, "failed to fetch stocks from DB: ")
			return
		}
		for _, s := range allStocks {
			req.Symbols = append(req.Symbols, s.Symbol)
		}
	}

	job, err := h.jobService.CreateJob(ctx, sync.JobTypeOHLCV, req)
	if err != nil {
		httpserver.Error(c, http.StatusInternalServerError, err)
		return
	}

	c.JSON(http.StatusAccepted, gin.H{
		"message": "OHLCV sync job created",
		"job_id":  job.ID,
		"status":  job.Status,
		"symbols": len(req.Symbols),
	})
}

// syncAllOHLCVHandler creates a job to sync all OHLCV data.
func (h *SyncHandler) syncAllOHLCVHandler(c *gin.Context) {
	ctx := c.Request.Context()
	var req sync.OHLCVSyncParams
	if err := c.ShouldBindJSON(&req); err != nil {
		// REV-12：空 body 走默认（全市场）；畸形 body 400，不静默转默认。
		if !errors.Is(err, io.EOF) {
			httpserver.Error(c, http.StatusBadRequest, err)
			return
		}
	}
	if req.BatchSize <= 0 {
		req.BatchSize = 10
	}
	if req.EndDate == "" {
		req.EndDate = time.Now().Format("20060102")
	}
	if req.StartDate == "" {
		req.StartDate = time.Now().AddDate(-1, 0, 0).Format("20060102")
	}

	job, err := h.jobService.CreateJob(ctx, sync.JobTypeOHLCVAll, req)
	if err != nil {
		httpserver.Error(c, http.StatusInternalServerError, err)
		return
	}

	c.JSON(http.StatusAccepted, gin.H{
		"message":    "bulk OHLCV sync job created",
		"job_id":     job.ID,
		"status":     job.Status,
		"start_date": req.StartDate,
		"end_date":   req.EndDate,
	})
}

// syncFundamentalsHandler creates a job to sync fundamentals data.
func (h *SyncHandler) syncFundamentalsHandler(c *gin.Context) {
	ctx := c.Request.Context()
	var req sync.FundamentalSyncParams
	if err := c.ShouldBindJSON(&req); err != nil {
		// REV-12：空 body 走默认（全市场）；畸形 body 400。
		if !errors.Is(err, io.EOF) {
			httpserver.Error(c, http.StatusBadRequest, err)
			return
		}
	}

	if len(req.Symbols) == 0 {
		allStocks, err := h.store.GetAllStocks(ctx)
		if err != nil {
			httpserver.Wrap(c, http.StatusInternalServerError, err, "failed to fetch stocks from DB: ")
			return
		}
		for _, s := range allStocks {
			req.Symbols = append(req.Symbols, s.Symbol)
		}
	}

	job, err := h.jobService.CreateJob(ctx, sync.JobTypeFundamentals, req)
	if err != nil {
		httpserver.Error(c, http.StatusInternalServerError, err)
		return
	}

	c.JSON(http.StatusAccepted, gin.H{
		"message": "fundamentals sync job created",
		"job_id":  job.ID,
		"status":  job.Status,
		"symbols": len(req.Symbols),
	})
}

// syncCalendarHandler creates a job to sync trading calendar.
func (h *SyncHandler) syncCalendarHandler(c *gin.Context) {
	ctx := c.Request.Context()
	var req sync.CalendarSyncParams
	if err := c.ShouldBindJSON(&req); err != nil {
		// REV-12：与五个全市场端点同一缺陷形态（err 整个丢弃）顺带收口。
		if !errors.Is(err, io.EOF) {
			httpserver.Error(c, http.StatusBadRequest, err)
			return
		}
		req.StartDate = time.Now().AddDate(-1, 0, 0).Format("20060102")
		req.EndDate = time.Now().Format("20060102")
	}
	if req.Exchange == "" {
		req.Exchange = "both"
	}

	job, err := h.jobService.CreateJob(ctx, sync.JobTypeCalendar, req)
	if err != nil {
		httpserver.Error(c, http.StatusInternalServerError, err)
		return
	}

	c.JSON(http.StatusAccepted, gin.H{
		"message": "calendar sync job created",
		"job_id":  job.ID,
		"status":  job.Status,
	})
}

// syncDividendsHandler creates a job to sync dividends data.
func (h *SyncHandler) syncDividendsHandler(c *gin.Context) {
	ctx := c.Request.Context()
	var req syncDividendsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// REV-12：空 body 走默认（全市场）；畸形 body 400。
		if !errors.Is(err, io.EOF) {
			httpserver.Error(c, http.StatusBadRequest, err)
			return
		}
	}

	params := sync.FundamentalSyncParams{
		Symbols:   req.Symbols,
		StartDate: req.StartDate,
		EndDate:   req.EndDate,
	}
	if len(params.Symbols) == 0 {
		allStocks, err := h.store.GetAllStocks(ctx)
		if err != nil {
			httpserver.Wrap(c, http.StatusInternalServerError, err, "failed to fetch stocks from DB: ")
			return
		}
		for _, s := range allStocks {
			params.Symbols = append(params.Symbols, s.Symbol)
		}
	}

	job, err := h.jobService.CreateJob(ctx, sync.JobTypeDividends, params)
	if err != nil {
		httpserver.Error(c, http.StatusInternalServerError, err)
		return
	}

	c.JSON(http.StatusAccepted, gin.H{
		"message": "dividends sync job created",
		"job_id":  job.ID,
		"status":  job.Status,
		"symbols": len(params.Symbols),
	})
}

// syncSplitsHandler creates a job to sync splits data.
func (h *SyncHandler) syncSplitsHandler(c *gin.Context) {
	ctx := c.Request.Context()
	var req syncSplitsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// REV-12：空 body 走默认（全市场）；畸形 body 400。
		if !errors.Is(err, io.EOF) {
			httpserver.Error(c, http.StatusBadRequest, err)
			return
		}
	}

	params := sync.FundamentalSyncParams{
		Symbols:   req.Symbols,
		StartDate: req.StartDate,
		EndDate:   req.EndDate,
	}
	if len(params.Symbols) == 0 {
		allStocks, err := h.store.GetAllStocks(ctx)
		if err != nil {
			httpserver.Wrap(c, http.StatusInternalServerError, err, "failed to fetch stocks from DB: ")
			return
		}
		for _, s := range allStocks {
			params.Symbols = append(params.Symbols, s.Symbol)
		}
	}

	job, err := h.jobService.CreateJob(ctx, sync.JobTypeSplits, params)
	if err != nil {
		httpserver.Error(c, http.StatusInternalServerError, err)
		return
	}

	c.JSON(http.StatusAccepted, gin.H{
		"message": "splits sync job created",
		"job_id":  job.ID,
		"status":  job.Status,
		"symbols": len(params.Symbols),
	})
}

// ---- New REST API Endpoints ----

// listJobsHandler returns a list of sync jobs.
func (h *SyncHandler) listJobsHandler(c *gin.Context) {
	ctx := c.Request.Context()
	status := c.Query("status")
	limitStr := c.Query("limit")

	limit := 20
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 && n <= maxListJobsLimit {
			limit = n
		}
	}

	var jobs []*sync.Job
	var err error

	if status != "" {
		jobs, err = h.jobService.ListJobs(ctx, sync.JobStatus(status), limit)
	} else {
		jobs, err = h.jobService.ListJobs(ctx, "", limit)
	}

	if err != nil {
		httpserver.Error(c, http.StatusInternalServerError, err)
		return
	}

	// DATA-14：空结果必须是 []，不能是 null（见 collectionPayload）。
	c.JSON(http.StatusOK, collectionPayload("jobs", jobs))
}

// getJobHandler returns a single sync job by ID.
func (h *SyncHandler) getJobHandler(c *gin.Context) {
	ctx := c.Request.Context()
	jobID := c.Param("id")

	job, err := h.jobService.GetJob(ctx, jobID)
	if err != nil {
		httpserver.Error(c, http.StatusInternalServerError, err)
		return
	}
	if job == nil {
		httpserver.Fail(c, http.StatusNotFound, "job not found")
		return
	}

	c.JSON(http.StatusOK, job)
}

// cancelJobHandler cancels a sync job.
func (h *SyncHandler) cancelJobHandler(c *gin.Context) {
	ctx := c.Request.Context()
	jobID := c.Param("id")

	if err := h.jobService.CancelJob(ctx, jobID); err != nil {
		httpserver.Error(c, http.StatusBadRequest, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "job cancelled", "job_id": jobID})
}

// retryJobHandler retries a failed sync job.
func (h *SyncHandler) retryJobHandler(c *gin.Context) {
	ctx := c.Request.Context()
	jobID := c.Param("id")

	job, err := h.jobService.RetryJob(ctx, jobID)
	if err != nil {
		httpserver.Error(c, http.StatusBadRequest, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "job queued for retry", "job_id": job.ID, "status": job.Status})
}

// getWorkerStatsHandler returns worker pool statistics.
func (h *SyncHandler) getWorkerStatsHandler(c *gin.Context) {
	stats := h.workerPool.Stats()
	c.JSON(http.StatusOK, stats)
}

// createJobHandler creates a sync job of any registered type — the typed
// door behind POST /api/sync/jobs (SPEC.md:1323, ODR-062 S-B). Unlike the
// legacy /sync/* endpoints above it, params are validated at the door so a
// mistyped body surfaces as 400 instead of an async job that can only fail
// later. One deliberate exception: an explicitly empty symbol list is
// accepted and no-ops (or fails) during execution, matching the legacy
// semantics.
func (h *SyncHandler) createJobHandler(c *gin.Context) {
	ctx := c.Request.Context()
	var req struct {
		Type   string          `json:"type"`
		Params json.RawMessage `json:"params"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httpserver.Error(c, http.StatusBadRequest, err)
		return
	}
	if req.Type == "" {
		httpserver.Fail(c, http.StatusBadRequest, "type is required")
		return
	}
	// params may be absent — treat as an empty object.
	if len(req.Params) == 0 || string(req.Params) == "null" {
		req.Params = json.RawMessage("{}")
	}

	var job *sync.Job
	var err error

	switch sync.JobType(req.Type) {
	case sync.JobTypeStocks:
		var params sync.StocksSyncParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			httpserver.Error(c, http.StatusBadRequest, err)
			return
		}
		if params.ListStatus == "" {
			params.ListStatus = "L"
		}
		job, err = h.jobService.CreateJob(ctx, sync.JobTypeStocks, params)

	case sync.JobTypeOHLCV:
		var params sync.OHLCVSyncParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			httpserver.Error(c, http.StatusBadRequest, err)
			return
		}
		// The typed door requires an explicit symbol list (possibly empty —
		// it then no-ops during execution); whole-market sync has its own
		// job type (ohlcv_all). A missing key (vs an empty array) is
		// rejected so a mistyped body fails fast.
		if !jsonHasKey(req.Params, "symbols") {
			httpserver.Fail(c, http.StatusBadRequest, `params.symbols is required (use type "ohlcv_all" for whole-market sync)`)
			return
		}
		if !validDateRange(params.StartDate, params.EndDate) {
			httpserver.Fail(c, http.StatusBadRequest, "params.start_date/end_date must be YYYYMMDD or YYYY-MM-DD")
			return
		}
		job, err = h.jobService.CreateJob(ctx, sync.JobTypeOHLCV, params)

	case sync.JobTypeOHLCVAll:
		var params sync.OHLCVSyncParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			httpserver.Error(c, http.StatusBadRequest, err)
			return
		}
		if params.BatchSize <= 0 {
			params.BatchSize = 10
		}
		if params.EndDate == "" {
			params.EndDate = time.Now().Format("20060102")
		}
		if params.StartDate == "" {
			params.StartDate = time.Now().AddDate(-1, 0, 0).Format("20060102")
		}
		if !validDateRange(params.StartDate, params.EndDate) {
			httpserver.Fail(c, http.StatusBadRequest, "params.start_date/end_date must be YYYYMMDD or YYYY-MM-DD")
			return
		}
		job, err = h.jobService.CreateJob(ctx, sync.JobTypeOHLCVAll, params)

	case sync.JobTypeFundamentals:
		var params sync.FundamentalSyncParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			httpserver.Error(c, http.StatusBadRequest, err)
			return
		}
		if !jsonHasKey(req.Params, "symbols") {
			httpserver.Fail(c, http.StatusBadRequest, "params.symbols is required")
			return
		}
		job, err = h.jobService.CreateJob(ctx, sync.JobTypeFundamentals, params)

	case sync.JobTypeCalendar:
		var params sync.CalendarSyncParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			httpserver.Error(c, http.StatusBadRequest, err)
			return
		}
		if params.Exchange == "" {
			params.Exchange = "both"
		}
		if params.StartDate == "" {
			params.StartDate = time.Now().AddDate(-1, 0, 0).Format("20060102")
		}
		if params.EndDate == "" {
			params.EndDate = time.Now().Format("20060102")
		}
		if !validDateRange(params.StartDate, params.EndDate) {
			httpserver.Fail(c, http.StatusBadRequest, "params.start_date/end_date must be YYYYMMDD or YYYY-MM-DD")
			return
		}
		job, err = h.jobService.CreateJob(ctx, sync.JobTypeCalendar, params)

	case sync.JobTypeDividends, sync.JobTypeSplits:
		var params struct {
			Symbols []string `json:"symbols"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			httpserver.Error(c, http.StatusBadRequest, err)
			return
		}
		if !jsonHasKey(req.Params, "symbols") {
			httpserver.Fail(c, http.StatusBadRequest, "params.symbols is required")
			return
		}
		job, err = h.jobService.CreateJob(ctx, sync.JobType(req.Type), sync.FundamentalSyncParams{Symbols: params.Symbols})

	case sync.JobTypeCapitalFlow:
		var params sync.CapitalFlowSyncParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			httpserver.Error(c, http.StatusBadRequest, err)
			return
		}
		// symbols 必须显式给出（与 ohlcv / fundamentals 那道门同形）：缺键 = 400，
		// 给了但是空数组 = 也 400。理由在执行器那边写着的同一条 ——
		// 逐 symbol 顺序 HTTP 的源，「默认全池」等于把一次误发请求
		// 变成 ≈5900 次上游调用。
		if !jsonHasKey(req.Params, "symbols") {
			httpserver.Fail(c, http.StatusBadRequest, "params.symbols is required")
			return
		}
		if len(params.Symbols) == 0 {
			httpserver.Fail(c, http.StatusBadRequest, "params.symbols must not be empty")
			return
		}
		if !validDateRange(params.StartDate, params.EndDate) {
			httpserver.Fail(c, http.StatusBadRequest, "params.start_date/end_date must be YYYYMMDD or YYYY-MM-DD")
			return
		}
		// period 在门上也拦一次：执行器会拒它，但那时作业行已经建好了 ——
		// 与其让人去 job 详情里找原因，不如 400 当场说清。
		if _, err := normalizeCapitalFlowPeriod(params.Period); err != nil {
			httpserver.Fail(c, http.StatusBadRequest, err.Error())
			return
		}
		job, err = h.jobService.CreateJob(ctx, sync.JobTypeCapitalFlow, params)

	case sync.JobTypeTopList:
		var params sync.TopListSyncParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			httpserver.Error(c, http.StatusBadRequest, err)
			return
		}
		// symbols 不是这一族的形状：链上唯一一员按交易日起整市场榜单，
		// 完全不读 req.Symbols。留一个「写了却不生效」的旋钮比不给更糟
		// （CR-39 chain drift 家族），所以在这里点名拒掉。
		if jsonHasKey(req.Params, "symbols") {
			httpserver.Fail(c, http.StatusBadRequest, "top_list has no params.symbols — the source serves whole-market boards per trade date (filter on the read side instead)")
			return
		}
		// 窗口判据**复用执行器那一个**（topListWindow），不在门上再写一份：
		// 两份判据会漂移，而漂移的方向永远是门比执行器松 —— 于是建出一条
		// 注定失败的作业行，人去 job 详情里找原因。这里要的正是「当场 400 说清」，
		// 所以调它、把它的 error 原样报出去（含「两端必填」「跨度上限」两条）。
		if _, _, err := topListWindow(params.StartDate, params.EndDate); err != nil {
			httpserver.Fail(c, http.StatusBadRequest, err.Error())
			return
		}
		if params.PageSize < 0 || params.MaxPages < 0 {
			httpserver.Fail(c, http.StatusBadRequest, "params.page_size / params.max_pages must be positive (0 or absent uses the executor defaults)")
			return
		}
		job, err = h.jobService.CreateJob(ctx, sync.JobTypeTopList, params)

	default:
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("unknown job type %q; supported: stocks, ohlcv, ohlcv_all, fundamentals, calendar, dividends, splits, capital_flow, top_list", req.Type),
		})
		return
	}

	if err != nil {
		httpserver.Error(c, http.StatusInternalServerError, err)
		return
	}

	c.JSON(http.StatusAccepted, gin.H{
		"message": req.Type + " sync job created",
		"job_id":  job.ID,
		"status":  job.Status,
	})
}

// jsonHasKey reports whether the raw JSON object contains the key. Used to
// distinguish "field absent" (reject) from "field present but empty"
// (accept; no-op during execution).
func jsonHasKey(raw json.RawMessage, key string) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	_, ok := m[key]
	return ok
}

// validDateRange validates an optional (start, end) date pair. Both formats
// in use across the repo are accepted: YYYYMMDD (tushare native) and
// YYYY-MM-DD (normalized by pkg/data.formatDate). Empty means "not provided".
func validDateRange(start, end string) bool {
	return validDate(start) && validDate(end)
}

func validDate(s string) bool {
	if s == "" {
		return true
	}
	if len(s) == 8 {
		_, err := time.Parse("20060102", s)
		return err == nil
	}
	if len(s) == 10 && s[4] == '-' && s[7] == '-' {
		_, err := time.Parse("2006-01-02", s)
		return err == nil
	}
	return false
}

// ---- Schedule API Endpoints ----

// createScheduleHandler creates a new sync schedule.
func (h *SyncHandler) createScheduleHandler(c *gin.Context) {
	ctx := c.Request.Context()
	var req sync.Schedule
	if err := c.ShouldBindJSON(&req); err != nil {
		httpserver.Error(c, http.StatusBadRequest, err)
		return
	}

	if req.Name == "" || req.CronExpression == "" || req.JobType == "" {
		httpserver.Fail(c, http.StatusBadRequest, "name, cron_expression, and job_type are required")
		return
	}

	if err := h.scheduler.CreateSchedule(ctx, &req); err != nil {
		if errors.Is(err, sync.ErrInvalidCron) {
			httpserver.Error(c, http.StatusBadRequest, err)
			return
		}
		httpserver.Error(c, http.StatusInternalServerError, err)
		return
	}

	c.JSON(http.StatusCreated, req)
}

// listSchedulesHandler returns all sync schedules.
func (h *SyncHandler) listSchedulesHandler(c *gin.Context) {
	ctx := c.Request.Context()
	activeOnly := c.Query("active_only") == "true"

	schedules, err := h.scheduler.ListSchedules(ctx, activeOnly)
	if err != nil {
		httpserver.Error(c, http.StatusInternalServerError, err)
		return
	}

	// DATA-14：同 jobs —— 空结果返回 []，不返回 null。
	c.JSON(http.StatusOK, collectionPayload("schedules", schedules))
}

// getScheduleHandler returns a single schedule by ID.
func (h *SyncHandler) getScheduleHandler(c *gin.Context) {
	ctx := c.Request.Context()
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		httpserver.Fail(c, http.StatusBadRequest, "invalid schedule ID")
		return
	}

	schedule, err := h.scheduler.GetSchedule(ctx, id)
	if err != nil {
		httpserver.Error(c, http.StatusInternalServerError, err)
		return
	}
	if schedule == nil {
		httpserver.Fail(c, http.StatusNotFound, "schedule not found")
		return
	}

	c.JSON(http.StatusOK, schedule)
}

// updateScheduleHandler updates an existing schedule.
func (h *SyncHandler) updateScheduleHandler(c *gin.Context) {
	ctx := c.Request.Context()
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		httpserver.Fail(c, http.StatusBadRequest, "invalid schedule ID")
		return
	}

	var req sync.Schedule
	if err := c.ShouldBindJSON(&req); err != nil {
		httpserver.Error(c, http.StatusBadRequest, err)
		return
	}
	req.ID = id

	if err := h.scheduler.UpdateSchedule(ctx, &req); err != nil {
		if errors.Is(err, sync.ErrInvalidCron) {
			httpserver.Error(c, http.StatusBadRequest, err)
			return
		}
		httpserver.Error(c, http.StatusInternalServerError, err)
		return
	}

	c.JSON(http.StatusOK, req)
}

// deleteScheduleHandler deletes a schedule by ID.
func (h *SyncHandler) deleteScheduleHandler(c *gin.Context) {
	ctx := c.Request.Context()
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		httpserver.Fail(c, http.StatusBadRequest, "invalid schedule ID")
		return
	}

	if err := h.scheduler.DeleteSchedule(ctx, id); err != nil {
		httpserver.Error(c, http.StatusInternalServerError, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "schedule deleted", "id": id})
}

// toggleScheduleHandler activates or deactivates a schedule.
func (h *SyncHandler) toggleScheduleHandler(c *gin.Context) {
	ctx := c.Request.Context()
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		httpserver.Fail(c, http.StatusBadRequest, "invalid schedule ID")
		return
	}

	var req struct {
		Active bool `json:"active"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httpserver.Error(c, http.StatusBadRequest, err)
		return
	}

	if err := h.scheduler.ToggleSchedule(ctx, id, req.Active); err != nil {
		httpserver.Error(c, http.StatusInternalServerError, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "schedule updated", "id": id, "active": req.Active})
}

// runScheduleNowHandler manually triggers a schedule to run immediately.
func (h *SyncHandler) runScheduleNowHandler(c *gin.Context) {
	ctx := c.Request.Context()
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		httpserver.Fail(c, http.StatusBadRequest, "invalid schedule ID")
		return
	}

	job, err := h.scheduler.RunScheduleNow(ctx, id)
	if err != nil {
		httpserver.Error(c, http.StatusInternalServerError, err)
		return
	}

	c.JSON(http.StatusAccepted, gin.H{"message": "schedule triggered", "job_id": job.ID, "status": job.Status})
}

// getSchedulerStatsHandler returns scheduler statistics.
func (h *SyncHandler) getSchedulerStatsHandler(c *gin.Context) {
	stats := h.scheduler.Stats()
	c.JSON(http.StatusOK, stats)
}

// ---- SSE Progress Endpoint ----

// sseProgressHandler streams job progress updates via Server-Sent Events.
//
// REV-10：进入本 handler 先**清掉这一条连接的写截止**。
// http.Server.WriteTimeout（data 服务 30s）是读完请求头后给裸连接设的
// **绝对**写截止——handler 每次 Flush 都受它约束。SSE 的使命却是按 2s
// ticker 跟随跑几分钟的作业（ohlcv_all），30s 到点必被服务器单方面掐断，
// 前端进度条只能反复重连。只清这一条连接；全局 WriteTimeout 保留（防慢
// 客户端占连接），其余路由行为不变。
func (h *SyncHandler) sseProgressHandler(c *gin.Context) {
	ctx := c.Request.Context()
	jobID := c.Param("id")

	// ResponseController 会沿 gin responseWriter 的 Unwrap 链拿到真正的
	// http.ResponseWriter；零值 deadline = 不做写超时。
	if err := http.NewResponseController(c.Writer).SetWriteDeadline(time.Time{}); err != nil {
		// 不支持 SetWriteDeadline 的包装器：如实记一条 warn（此时流仍会被
		// 服务级 WriteTimeout 截断，不能让失败静默）。
		logging.Logger.Warn().Err(err).Msg("SSE: failed to clear write deadline; stream may be cut by server WriteTimeout")
	}

	// Set SSE headers
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	// Verify job exists
	job, err := h.jobService.GetJob(ctx, jobID)
	if err != nil {
		// REV-23(1)：SSE 错误事件同样不回传内部错误原文——GetJob 的失败
		// 可能裹着 DB 连接/SQL 细节，客户端只拿固定文案；细节进日志。
		logging.Logger.Error().Err(err).Str("job_id", jobID).Msg("SSE: job lookup failed")
		c.SSEvent("error", gin.H{"error": "job lookup failed"})
		return
	}
	if job == nil {
		c.SSEvent("error", gin.H{"error": "job not found"})
		return
	}

	// Send initial state
	c.SSEvent("progress", job)
	c.Writer.Flush()

	// Poll for updates every 2 seconds
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.SSEvent("close", gin.H{"message": "connection closed"})
			return
		case <-ticker.C:
			job, err := h.jobService.GetJob(ctx, jobID)
			if err != nil {
				logging.Logger.Error().Err(err).Str("job_id", jobID).Msg("SSE: job poll failed")
				c.SSEvent("error", gin.H{"error": "job lookup failed"})
				c.Writer.Flush()
				continue
			}
			if job == nil {
				c.SSEvent("error", gin.H{"error": "job not found"})
				c.Writer.Flush()
				return
			}

			c.SSEvent("progress", job)
			c.Writer.Flush()

			// Stop if job is in terminal state
			if job.IsTerminal() {
				c.SSEvent("complete", job)
				c.Writer.Flush()
				return
			}
		}
	}
}

// ---- Job Executors ----

// stocksExecutor executes stock sync jobs.
type stocksExecutor struct {
	tc    *data.TushareClient
	store *storage.PostgresStore
}

func (e *stocksExecutor) JobType() sync.JobType {
	return sync.JobTypeStocks
}

func (e *stocksExecutor) Execute(ctx context.Context, job *sync.Job, progress sync.ProgressReporter) (any, error) {
	var params sync.StocksSyncParams
	if err := json.Unmarshal(job.Params, &params); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	// P2-4：一次同步拿全「在市 + 退市 + 暂停上市」三档。
	//
	// tushare stock_basic 的 list_status 一次只接受一档，而**只同步 L（在市）
	// 就是幸存者偏差的物理成因** —— 回测 2020 年时，2021 年退市的票根本
	// 不在库里，于是它最惨的那段行情永远不会出现在任何回测结果中。
	// "ALL" 与逗号分隔展开成 L/D/P 三次拉取，结果合并落库。
	statuses := expandListStatus(params.ListStatus)

	total := 0
	perStatus := make(map[string]int, len(statuses))
	for _, s := range statuses {
		stocks, err := e.tc.FetchStocks(ctx, params.Exchange, s)
		if err != nil {
			return nil, fmt.Errorf("fetch stocks (list_status=%s): %w", s, err)
		}
		total += len(stocks)
		perStatus[s] = len(stocks)
	}

	return map[string]any{
		"count":      total,
		"per_status": perStatus,
		// 明示这一批是否含退市票 —— 下游判断池子有没有幸存者偏差要看它。
		"includes_delisted": len(statuses) > 1,
	}, nil
}

// expandListStatus 把同步参数里的 list_status 展开成 tushare 能接受的单档列表。
//
//   - "" / "L" → 只在市（现状，保留默认行为）
//   - "ALL"    → L 上市 + D 退市 + P 暂停上市
//   - "L,P"    → 按逗号原样展开
func expandListStatus(v string) []string {
	switch strings.ToUpper(strings.TrimSpace(v)) {
	case "":
		return []string{"L"}
	case "ALL":
		return []string{"L", "D", "P"}
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.ToUpper(strings.TrimSpace(p)); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{"L"}
	}
	return out
}

// ohlcvExecutor executes OHLCV sync jobs.
type ohlcvExecutor struct {
	tc    *data.TushareClient
	store *storage.PostgresStore
}

func (e *ohlcvExecutor) JobType() sync.JobType {
	return sync.JobTypeOHLCV
}

func (e *ohlcvExecutor) Execute(ctx context.Context, job *sync.Job, progress sync.ProgressReporter) (any, error) {
	var params sync.OHLCVSyncParams
	if err := json.Unmarshal(job.Params, &params); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	totalCount := 0
	for i, symbol := range params.Symbols {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		logging.Logger.Info().Str("symbol", symbol).Str("start", params.StartDate).Str("end", params.EndDate).Msg("fetching OHLCV")
		ohlcv, err := e.tc.FetchDailyOHLCV(ctx, symbol, params.StartDate, params.EndDate)
		if err != nil {
			logging.Logger.Warn().Err(err).Str("symbol", symbol).Msg("Failed to sync OHLCV")
			progress.ReportError(err.Error())
			continue
		}
		totalCount += len(ohlcv)
		progress.ReportProgress(i+1, len(params.Symbols), job.FailedItems)
	}

	return map[string]any{"count": totalCount, "symbols": len(params.Symbols)}, nil
}

// ohlcvAllExecutor executes bulk OHLCV sync jobs.
type ohlcvAllExecutor struct {
	tc    *data.TushareClient
	store *storage.PostgresStore
}

func (e *ohlcvAllExecutor) JobType() sync.JobType {
	return sync.JobTypeOHLCVAll
}

func (e *ohlcvAllExecutor) Execute(ctx context.Context, job *sync.Job, progress sync.ProgressReporter) (any, error) {
	var params sync.OHLCVSyncParams
	if err := json.Unmarshal(job.Params, &params); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	stocks, err := e.store.GetAllStocks(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch stocks: %w", err)
	}

	totalSynced := 0
	totalSkipped := 0
	totalFailed := 0

	for i := 0; i < len(stocks); i += params.BatchSize {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		end := i + params.BatchSize
		if end > len(stocks) {
			end = len(stocks)
		}
		batch := stocks[i:end]

		for _, stock := range batch {
			if params.SkipExisting {
				hasData, err := e.store.HasOHLCVData(ctx, stock.Symbol)
				if err != nil {
					logging.Logger.Warn().Err(err).Str("symbol", stock.Symbol).Msg("Error checking OHLCV data")
				}
				if hasData {
					totalSkipped++
					continue
				}
			}

			ohlcv, err := e.tc.FetchDailyOHLCV(ctx, stock.Symbol, params.StartDate, params.EndDate)
			if err != nil {
				totalFailed++
				logging.Logger.Warn().Err(err).Str("symbol", stock.Symbol).Msg("Failed to sync OHLCV")
				continue
			}
			totalSynced += len(ohlcv)
		}

		progress.ReportProgress(end, len(stocks), totalFailed)
	}

	return map[string]any{
		"total_stocks":   len(stocks),
		"records_synced": totalSynced,
		"skipped":        totalSkipped,
		"failed":         totalFailed,
	}, nil
}

// fundamentalsExecutor executes fundamentals sync jobs.
// fundamentalsFetcher 是 fundamentalsExecutor 只依赖的取数能力。
//
// 抽成接口而不是直接持 *data.TushareClient，是为了让「参数真的到达 fetch」
// 能被单测钉住（REV-24）：真客户端要网络与 Tushare 配额，具体类型又无法
// 注入假实现。*data.TushareClient 天然满足此接口，生产装配零改动。
type fundamentalsFetcher interface {
	FetchFundamentalsData(ctx context.Context, symbol, startDate, endDate string) ([]domain.FundamentalData, error)
}

type fundamentalsExecutor struct {
	tc    fundamentalsFetcher
	store *storage.PostgresStore
}

func (e *fundamentalsExecutor) JobType() sync.JobType {
	return sync.JobTypeFundamentals
}

func (e *fundamentalsExecutor) Execute(ctx context.Context, job *sync.Job, progress sync.ProgressReporter) (any, error) {
	var params sync.FundamentalSyncParams
	if err := json.Unmarshal(job.Params, &params); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	res, err := runBatchedSymbols(ctx, params.Symbols, batchSyncSize, progress,
		func(ctx context.Context, symbol string) (int, error) {
			records, err := e.tc.FetchFundamentalsData(ctx, symbol, params.Date, params.Date)
			if err != nil {
				logging.Logger.Warn().Err(err).Str("symbol", symbol).Msg("Failed to sync fundamentals")
				return 0, err
			}
			return len(records), nil
		})
	if err != nil {
		return nil, err
	}
	return res.toMap(), nil
}

// calendarCoverageTolerance 是「Tushare 返回的区间」与「请求的区间」之间允许的偏差。
//
// 为什么允许偏差而不是要求边界逐日对齐：`trade_cal` 返回的是**自然日**（含休市日），
// 实测正常时边界与请求完全一致；容差只用来兜住「请求边界落在已发布日历之外」
// （例如请求了尚未公布的年尾）这种正当情形。所以容差取小 —— 它要能抓住
// AUD-59 那种「整整少 269 天」的错区间，而不是给它留活路。
const calendarCoverageTolerance = 7 * 24 * time.Hour

// parseSyncDate 解析同步作业的日期参数，**两种格式都认**。
//
// 两种都必须认，因为 `validDateRange` 与它给出的报错文案
// `params.start_date/end_date must be YYYYMMDD or YYYY-MM-DD` 就是这么承诺的。
// 「承诺的格式集合」大于「实际支持的格式集合」正是 AUD-59：原日历执行器按 8 位
// 定长硬切 `[:4]`/`[4:6]`/`[6:8]`，于是 `2022-01-01` 被切成 `2022--01-`，
// Tushare 视作非法日期、回落到它自己的默认区间 —— **作业报 completed，
// 落库的却是错的日历**，全程零错误零告警。
func parseSyncDate(raw string) (time.Time, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Time{}, errors.New("date is required (YYYYMMDD or YYYY-MM-DD)")
	}
	for _, layout := range []string{"20060102", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse date %q: expected YYYYMMDD or YYYY-MM-DD", raw)
}

// calendarFetchWindow 把作业参数解析成**唯一**的取数窗口。
//
// 「唯一」是承重的：取数用它，覆盖校验也用它。只要两处读的是同一组 time.Time，
// 就不可能再出现「请求 A 区间、校验 B 区间」这种脱钩 —— 而那正是复查 AUD-59 时
// 最容易再犯的形态（把硬切换个写法塞回来，校验却照着原始参数做，于是永远绿）。
func calendarFetchWindow(params sync.CalendarSyncParams) (start, end time.Time, err error) {
	start, err = parseSyncDate(params.StartDate)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("start_date: %w", err)
	}
	end, err = parseSyncDate(params.EndDate)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("end_date: %w", err)
	}
	if end.Before(start) {
		return time.Time{}, time.Time{}, fmt.Errorf("end_date %s is before start_date %s",
			end.Format("2006-01-02"), start.Format("2006-01-02"))
	}
	return start, end, nil
}

// calendarFetchArgs 返回**恰好**要传给 FetchTradingCalendar 的两个参数。
//
// 两个参数由**解析后的时间**派生，绝不在原始字符串上切片 —— 见 AUD-59。
// 单测用「带首尾空格的合法日期」把这条性质钉死：原始串不是 `20230101`，
// 但只要参数是从解析结果格式化的，取数参数就必然规范化为 `20230101`。
func calendarFetchArgs(params sync.CalendarSyncParams) (start, end time.Time, startArg, endArg string, err error) {
	start, end, err = calendarFetchWindow(params)
	if err != nil {
		return time.Time{}, time.Time{}, "", "", err
	}
	return start, end, start.Format("20060102"), end.Format("20060102"), nil
}

// validateCalendarCoverage 断言「取回来的日历真的覆盖了请求的区间」。
//
// 为什么需要它：AUD-59 里作业报 `completed`、`count: 2922`，**零错误零告警**，
// 而库里只有请求区间的约五分之四 —— 一个「成功」的同步写进了错的数据。
// 回测只检查「区间内有没有交易日」（pkg/backtest/engine.go），**不检查区间覆盖
// 是否完整**，所以这种错日历会一路喂到回测里，而且从任何读数上都看不出来。
func validateCalendarCoverage(exchange string, reqStart, reqEnd time.Time, entries []storage.TradingCalendarEntry) error {
	if len(entries) == 0 {
		return fmt.Errorf("%s: tushare returned no calendar entries for %s..%s — "+
			"a sync that writes nothing must not report success",
			exchange, reqStart.Format("2006-01-02"), reqEnd.Format("2006-01-02"))
	}

	lo, hi := entries[0].TradeDate, entries[0].TradeDate
	for i := range entries {
		d := entries[i].TradeDate
		if d.Before(lo) {
			lo = d
		}
		if d.After(hi) {
			hi = d
		}
	}

	if lo.After(reqStart.Add(calendarCoverageTolerance)) || hi.Before(reqEnd.Add(-calendarCoverageTolerance)) {
		return fmt.Errorf("%s: returned calendar %s..%s does not cover the requested %s..%s — "+
			"refusing to persist a partial range; check the date format (YYYYMMDD vs YYYY-MM-DD)",
			exchange,
			lo.Format("2006-01-02"), hi.Format("2006-01-02"),
			reqStart.Format("2006-01-02"), reqEnd.Format("2006-01-02"))
	}
	return nil
}

// calendarExecutor executes calendar sync jobs.
type calendarExecutor struct {
	tc    *data.TushareClient
	store *storage.PostgresStore
}

func (e *calendarExecutor) JobType() sync.JobType {
	return sync.JobTypeCalendar
}

func (e *calendarExecutor) Execute(ctx context.Context, job *sync.Job, progress sync.ProgressReporter) (any, error) {
	var params sync.CalendarSyncParams
	if err := json.Unmarshal(job.Params, &params); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	// 取数窗口解析一次、两处共用（取数 + 覆盖校验）—— 见 calendarFetchArgs 的注释。
	start, end, startArg, endArg, err := calendarFetchArgs(params)
	if err != nil {
		return nil, err
	}

	exchanges := []string{params.Exchange}
	if params.Exchange == "both" {
		exchanges = []string{"SSE", "SZSE"}
	}

	var allEntries []storage.TradingCalendarEntry
	for _, exchange := range exchanges {
		entries, err := e.tc.FetchTradingCalendar(ctx, exchange, startArg, endArg)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch %s calendar: %w", exchange, err)
		}
		// 先校验、后落库：区间不对宁可让作业失败，也不能写进去（AUD-59）。
		if err := validateCalendarCoverage(exchange, start, end, entries); err != nil {
			return nil, err
		}
		allEntries = append(allEntries, entries...)
	}

	domainEntries := make([]*storage.TradingCalendarEntry, len(allEntries))
	for i := range allEntries {
		domainEntries[i] = &allEntries[i]
	}
	if err := e.store.SaveTradingCalendarBatch(ctx, domainEntries); err != nil {
		return nil, fmt.Errorf("failed to save calendar: %w", err)
	}

	return map[string]any{"count": len(allEntries)}, nil
}

// dividendsExecutor executes dividends sync jobs.
// dividendsFetcher 是 dividendsExecutor 只依赖的取数能力（理由同 fundamentalsFetcher）。
type dividendsFetcher interface {
	FetchDividends(ctx context.Context, symbol, startDate, endDate string) ([]domain.Dividend, error)
}

type dividendsExecutor struct {
	tc    dividendsFetcher
	store *storage.PostgresStore
}

func (e *dividendsExecutor) JobType() sync.JobType {
	return sync.JobTypeDividends
}

func (e *dividendsExecutor) Execute(ctx context.Context, job *sync.Job, progress sync.ProgressReporter) (any, error) {
	var params sync.FundamentalSyncParams
	if err := json.Unmarshal(job.Params, &params); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	// REV-24：params 的日期区间真的传下去（此前硬编码 ""，调用方指定的区间
	// 被静默丢掉，实际永远拉全历史）。
	res, err := runBatchedSymbols(ctx, params.Symbols, batchSyncSize, progress,
		func(ctx context.Context, symbol string) (int, error) {
			records, err := e.tc.FetchDividends(ctx, symbol, params.StartDate, params.EndDate)
			if err != nil {
				logging.Logger.Warn().Err(err).Str("symbol", symbol).Msg("Failed to sync dividends")
				return 0, err
			}
			return len(records), nil
		})
	if err != nil {
		return nil, err
	}
	return res.toMap(), nil
}

// splitsExecutor executes splits sync jobs.
// splitsFetcher 是 splitsExecutor 只依赖的取数能力（理由同 fundamentalsFetcher）。
type splitsFetcher interface {
	FetchSplits(ctx context.Context, symbol, startDate, endDate string) ([]domain.Split, error)
}

type splitsExecutor struct {
	tc    splitsFetcher
	store *storage.PostgresStore
}

func (e *splitsExecutor) JobType() sync.JobType {
	return sync.JobTypeSplits
}

func (e *splitsExecutor) Execute(ctx context.Context, job *sync.Job, progress sync.ProgressReporter) (any, error) {
	var params sync.FundamentalSyncParams
	if err := json.Unmarshal(job.Params, &params); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	// REV-24：日期区间贯通（同 dividendsExecutor）。
	res, err := runBatchedSymbols(ctx, params.Symbols, batchSyncSize, progress,
		func(ctx context.Context, symbol string) (int, error) {
			records, err := e.tc.FetchSplits(ctx, symbol, params.StartDate, params.EndDate)
			if err != nil {
				logging.Logger.Warn().Err(err).Str("symbol", symbol).Msg("Failed to sync splits")
				return 0, err
			}
			return len(records), nil
		})
	if err != nil {
		return nil, err
	}
	return res.toMap(), nil
}
