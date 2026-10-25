package main

// DATA-10 ⑤ 的主证据：一条从**作业层真的走一次**的测试。
//
// 工单原话是「POST /api/sync/jobs（job_type=capital_flow）→ worker executor
// → ETLPipeline.Process → 落库，**不是**直接调 Process 的行为测试」。
// 本文件从 HTTP 门开始，经 JobService → Queue.Dequeue → WorkerPool 的真实派发
// （含 jobProgressReporter 与重试收敛）→ capitalFlowExecutor →
// Registry.Fetch（链 capital_flow:[eastmoney]）→ **投点给写面**。
//
// 2026-10-25（QT-40 H4 ①）改的是这条链的最后一段：落库不再是 core 做的事。
// 于是「逐字段对拍」的对象从**库里的行**换成**投出去的那一批点** —— 这不是
// 把证据打折，而是边界变了之后 core 这一侧唯一可证的形状。行级 no-op、
// 内容比对、由声明锚点推导 `visible_at`，那些判据的主人现在是
// `tellurion-data`（腿在它的 `internal/assembly` 与 `internal/store`）。
// core 要钉死的是三件事：投出去的内容与源响应逐字段一致；投送面上没有
// `visible_at` / `ingest_time`；对端失败怎样落进作业结果。
//
// 两处是替身，理由如实写在这儿：
//   - **作业行（sync_jobs）用内存 store**，写面用 httptest 替身。原因不是省事，是
//     并发安全：`Queue.Dequeue` 取「最老的 pending」，不看 job_type。测试若起
//     真队列，会把**别的 agent 正在排的队**抢过来，用这套（tc 为 nil 的）执行器
//     把别人的作业打失败 —— 那是拿别人的作业给自己作证。
//   - **东财 push2 与 data-service 写面都是 httptest 替身**。工单 ② 写「primary
//     adapter 已启用、接线即灌数」；2026-10-16 实测：`/api/qt/stock/fflow/daykline/get`
//     这条 path 本机不可达（push2 恒 502、push2his 连接重置、浏览器同结果；同主机的
//     ulist.np / kline / fflow/kline 都 200）。真源那条腿在
//     capital_flow_live_test.go（build tag `data10live`），不放进默认套件赌网络。
//     **真跨进程那一腿**（core → 真 data-service → 真库的行）在
//     capital_flow_live_test.go（build tag `data10live`，2026-10-25 起它需要
//     `H4_LIVE_INGEST_URL` 才跑）：默认套件里没有它，因为两个仓各自独立构建，
//     core 的 CI 起不了对端进程 —— 这条缺口在 docs/TASKS.md 的 QT-40 里如实
//     登记，不写成「端到端已验收」。
//
// 探针符号用 888888.SH / 888889.SH —— 不是真实 A 股代码。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	stdsync "sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/data/source"
	"tellurion/pkg/sync"
)

const (
	capitalFlowSymA = "888888.SH"
	capitalFlowSymB = "888889.SH"
)

// capitalFlowKLines 是 push2 daykline 的两行真实形状（CSV，19 段）。
// 字段序见 parseEastmoneyCapitalFlowKLines 的注释。
var capitalFlowKLines = []string{
	// 2026-10-15: super+large+medium+small = 300+400+200+100 = 1000 ⇒ retail_net = -1000
	//             main+retail = 1000-1000 = 0 ⇒ retail_net_ratio 走零守卫留 0
	"2026-10-15,1720.5,1.22,1000,1500,-500,12.5,400,-100,300,600,-200,400,300,-100,200,200,-100,100",
	// 2026-10-16: 净流出，同样让 main+retail = 0
	"2026-10-16,1731.0,0.61,-2500,-1000,-1500,-20.0,-1000,0,-1000,-800,0,-800,-500,0,-500,-200,0,-200",
}

// capitalFlowExpected 是**手算**的期望值，不是从被测代码里取的：
// 让 adapter 自己算一遍再和它自己比，不算证据。
type capitalFlowExpected struct {
	Symbol      string
	TradeDate   string
	MainNet     float64
	SuperNet    float64
	LargeNet    float64
	MediumNet   float64
	SmallNet    float64
	MainRatio   float64
	RetailNet   float64
	RetailRatio float64
	ClosePrice  float64
	ChangePct   float64
}

var capitalFlowExpectedRows = []capitalFlowExpected{
	{capitalFlowSymA, "2026-10-15", 1000, 300, 400, 200, 100, 12.5, -1000, 0, 1720.5, 1.22},
	{capitalFlowSymA, "2026-10-16", -2500, -1000, -800, -500, -200, -20.0, 2500, 0, 1731.0, 0.61},
	{capitalFlowSymB, "2026-10-15", 1000, 300, 400, 200, 100, 12.5, -1000, 0, 1720.5, 1.22},
	{capitalFlowSymB, "2026-10-16", -2500, -1000, -800, -500, -200, -20.0, 2500, 0, 1731.0, 0.61},
}

// ---- 源侧替身 ----

type stubPush2 struct {
	mu      stdsync.Mutex
	status  int
	queries []string
	paths   []string
}

func (s *stubPush2) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.queries = append(s.queries, r.URL.RawQuery)
		s.paths = append(s.paths, r.URL.Path)
		status := s.status
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte("upstream exploded"))
			return
		}
		body := map[string]any{"rc": 0, "data": map[string]any{
			"code": "888888", "name": "DATA-10 stub", "total": len(capitalFlowKLines),
			"klines": capitalFlowKLines,
		}}
		_ = json.NewEncoder(w).Encode(body)
	}
}

func (s *stubPush2) seen() (queries, paths []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.queries...), append([]string(nil), s.paths...)
}

// newCapitalFlowRegistry 造一条与生产 configureFallbackChains 同形的链：
// capital_flow 只有 eastmoney 一个成员，BaseURL 指向替身。
func newCapitalFlowRegistry(t *testing.T, baseURL string) *source.Registry {
	t.Helper()
	reg := source.NewRegistry()
	client := &source.EastmoneyClient{
		BaseURL:    baseURL,
		HTTPClient: &http.Client{Timeout: 5 * time.Second},
	}
	require.NoError(t, reg.Register(source.NewEastmoneyAdapter(client)))
	reg.SetChain(source.DataTypeCapitalFlow, []string{"eastmoney"})
	return reg
}

// ---- 作业行替身（理由见文件头）----

type memJobStore struct {
	mu   stdsync.Mutex
	jobs []*sync.Job
}

func (s *memJobStore) CreateSyncJob(_ context.Context, job *sync.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs = append(s.jobs, job.Clone())
	return nil
}

func (s *memJobStore) GetSyncJob(_ context.Context, jobID string) (*sync.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if j.ID == jobID {
			return j.Clone(), nil
		}
	}
	return nil, fmt.Errorf("job %s not found", jobID)
}

// UpdateSyncJobIfStatus 复刻真 store 的条件写语义（AUD-49 靠的就是它）：
// 当前状态不在 from 里就不落、返回 false。替身把这条写反，整组测试白跑。
func (s *memJobStore) UpdateSyncJobIfStatus(_ context.Context, job *sync.Job, from ...sync.JobStatus) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, j := range s.jobs {
		if j.ID != job.ID {
			continue
		}
		ok := false
		for _, want := range from {
			if j.Status == want {
				ok = true
				break
			}
		}
		if !ok {
			return false, nil
		}
		s.jobs[i] = job.Clone()
		return true, nil
	}
	return false, fmt.Errorf("job %s not found", job.ID)
}

func (s *memJobStore) ListSyncJobs(_ context.Context, status sync.JobStatus, limit int) ([]*sync.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*sync.Job, 0, limit)
	for _, j := range s.jobs {
		if j.Status != status {
			continue
		}
		// 未到点的重试不能被取走（真 store 用 scheduled_at 过滤）。
		if j.ScheduledAt != nil && j.ScheduledAt.After(time.Now()) {
			continue
		}
		out = append(out, j.Clone())
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *memJobStore) ListSyncJobsByType(_ context.Context, jobType sync.JobType, limit int) ([]*sync.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*sync.Job, 0, limit)
	for _, j := range s.jobs {
		if j.JobType == jobType {
			out = append(out, j.Clone())
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (s *memJobStore) DeleteSyncJob(_ context.Context, jobID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, j := range s.jobs {
		if j.ID == jobID {
			s.jobs = append(s.jobs[:i], s.jobs[i+1:]...)
			return nil
		}
	}
	return nil
}

// ---- 作业层门面 ----

type capitalFlowHarness struct {
	handler *SyncHandler
	jobs    *memJobStore
	router  *gin.Engine
	stub    *stubPush2
}

// newCapitalFlowHarness 的装配顺序与 main.go / NewSyncHandler 一致
// （notifier → canceller → 执行器注册），只是把作业行 store 换成内存替身、
// 把链上的源指向替身。写面那一半由 write 参数决定：本文件传 httptest
// 的 data-service 替身（经真 `ingestWriteClient`），诚实性腿传接口替身。
func newCapitalFlowHarness(t *testing.T, write pointsPoster, stubStatus int) *capitalFlowHarness {
	t.Helper()

	stub := &stubPush2{status: stubStatus}
	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)

	jobs := &memJobStore{}
	queue := sync.NewQueue(jobs)
	jobService := sync.NewJobService(jobs)
	jobService.SetPendingNotifier(queue.NotifyJobAvailable)
	pool := sync.NewWorkerPool(queue, 1)
	jobService.SetRunningCanceller(pool.Cancel)

	registry := newCapitalFlowRegistry(t, srv.URL)
	pool.RegisterExecutor(&capitalFlowExecutor{reg: registry, write: write})

	h := &SyncHandler{jobService: jobService, queue: queue, workerPool: pool, registry: registry, ingestWrite: write}
	router := gin.New()
	router.POST("/api/sync/jobs", h.createJobHandler)

	return &capitalFlowHarness{handler: h, jobs: jobs, router: router, stub: stub}
}

// ---- data-service 写端点的替身 ----

// stubIngestService 记录**收到的原始报文**，而不是解码后的 Go 值。
//
// 这是刻意的：本单最可能出的错就是「core 以为自己没投 visible_at，实际投了」
// —— 只有原始报文能证明投送面上到底有什么。回执默认把 persisted 报成
// 「收到的点数」，要失败形状就改 status/code。
type stubIngestService struct {
	mu            stdsync.Mutex
	rawBodies     []string
	auth          []string
	status        int
	code          string
	message       string
	skipped       int
	failOnRequest int // >0 时只有第 N 片（1 起）改答 status，其余照常收下 —— 「分片部分成功」的形状
}

func (s *stubIngestService) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(r.Body)
		s.mu.Lock()
		s.rawBodies = append(s.rawBodies, body.String())
		s.auth = append(s.auth, r.Header.Get("Authorization"))
		status, code, message, skipped := s.status, s.code, s.message, s.skipped
		if s.failOnRequest > 0 {
			// 只让那一片失败：跨仓之后一个 chunk 内部还要分片，片与片之间不共享
			// 事务，所以「前一片已落、这一片被拒」是常态形状，替身必须能演它。
			if len(s.rawBodies) != s.failOnRequest {
				status = http.StatusOK
			} else if status == 0 {
				status = http.StatusBadGateway
			}
		}
		s.mu.Unlock()
		if status == 0 {
			status = http.StatusOK
		}

		if status != http.StatusOK {
			if code == "" {
				code = "internal"
			}
			if message == "" {
				message = "stub refusal"
			}
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(ingestWireError{Error: struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}{Code: code, Message: message}})
			return
		}

		var req ingestWireRequest
		var received int
		if err := json.Unmarshal(body.Bytes(), &req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_parameter","message":"stub: bad json"}}`))
			return
		}
		received = len(req.Points)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ingestWireResult{
			DataType: req.DataType, Received: received, Persisted: received - skipped, Skipped: skipped,
		})
	})
}

// seen 返回收到过的原始报文与 Authorization 头（一一对应）。
func (s *stubIngestService) seen() ([]string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.rawBodies...), append([]string(nil), s.auth...)
}

// batches 返回解码后的批次（逐字段对拍用）。
func (s *stubIngestService) batches(t *testing.T) []ingestWireRequest {
	t.Helper()
	raw, _ := s.seen()
	out := make([]ingestWireRequest, 0, len(raw))
	for i, body := range raw {
		var req ingestWireRequest
		require.NoError(t, json.Unmarshal([]byte(body), &req), "第 %d 片报文不是合法 JSON", i)
		out = append(out, req)
	}
	return out
}

// ingestPosterFor 用替身装出一条真客户端（于是分片、请求头、解码、错误分诊
// 这些代码路径都在测试覆盖里，而不是被一个接口替身跳过）。
func ingestPosterFor(t *testing.T, svc *stubIngestService, maxPoints int) pointsPoster {
	srv := httptest.NewServer(svc.handler())
	t.Cleanup(srv.Close)
	return newIngestWriteClient(ingestWriteConfig{
		BaseURL:   srv.URL,
		Token:     "test-service-token",
		Timeout:   5 * time.Second,
		MaxPoints: maxPoints,
		Client:    srv.Client(),
	})
}

// pointsFromBatch 把批次里的点按 symbol|trade_date 排好，方便逐字段定位。
func pointsFromBatch(batch ingestWireRequest) map[string]ingestWirePoint {
	out := map[string]ingestWirePoint{}
	for _, p := range batch.Points {
		out[p.Symbol+"|"+p.TradeTime.UTC().Format("2006-01-02")] = p
	}
	return out
}

func (h *capitalFlowHarness) createJob(t *testing.T, params map[string]any) (int, string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"type": "capital_flow", "params": params})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/sync/jobs", bytes.NewReader(body)))
	return w.Code, w.Body.String()
}

func (h *capitalFlowHarness) waitJobSettled(t *testing.T, jobID string) *sync.Job {
	t.Helper()
	var last *sync.Job
	require.Eventually(t, func() bool {
		j, err := h.jobs.GetSyncJob(context.Background(), jobID)
		if err != nil {
			return false
		}
		last = j
		return j.IsTerminal()
	}, 15*time.Second, 20*time.Millisecond, "作业没有在期限内落定")
	return last
}

func (h *capitalFlowHarness) executor(t *testing.T) sync.JobExecutor {
	t.Helper()
	exec, ok := h.handler.workerPool.GetExecutor(sync.JobTypeCapitalFlow)
	require.True(t, ok, "执行器必须注册在作业层上")
	return exec
}

func jobWithParams(id, params string) *sync.Job {
	return &sync.Job{
		ID:         id,
		JobType:    sync.JobTypeCapitalFlow,
		Status:     sync.JobStatusRunning,
		Params:     json.RawMessage(params),
		MaxRetries: 3,
	}
}

// ---- 主证据：作业层真的走一次 + 投出去的点逐字段对拍 ----

// wireValue 取载荷里的数值列；缺列即 fail（「源没给」与「给了 0」必须是两件事）。
func wireValue(t *testing.T, p ingestWirePoint, column string) float64 {
	t.Helper()
	raw, ok := p.Data[column]
	require.True(t, ok, "%s %s：载荷里没有列 %q（键名漂了就是投了一列没人认的值）",
		p.Symbol, p.TradeTime.Format("2006-01-02"), column)
	v, ok := raw.(float64)
	require.True(t, ok, "%s %s：列 %q 不是数值，实得 %T", p.Symbol, p.TradeTime.Format("2006-01-02"), column, raw)
	return v
}

// TestCapitalFlowJobLayerPostsPointsMatchingSourceResponse 是 DATA-10 ⑤ 那条
// 主证据在 H4 ① 之后的形态：一条作业真的经 worker 派发跑完，最后投出去的
// 每一列都与替身源响应对得上 —— 逐字段，不是数行数。
//
// 另外钉三条只有跨仓之后才存在的判据：
//   - 投送面上**没有** `visible_at` / `ingest_time`（对端按未知字段 400）；
//   - `source` 是链上真正供数的那个适配器名（换源在对端是内容变化，会 409）；
//   - 结果里 `persisted_via` 说明这批不是本进程写的。
func TestCapitalFlowJobLayerPostsPointsMatchingSourceResponse(t *testing.T) {
	svc := &stubIngestService{}
	h := newCapitalFlowHarness(t, ingestPosterFor(t, svc, ingestWriteHardMaxPoints), http.StatusOK)
	h.handler.StartWorkerPool()
	t.Cleanup(h.handler.StopWorkerPool)

	code, bodyText := h.createJob(t, map[string]any{
		"symbols":    []string{capitalFlowSymA, capitalFlowSymB},
		"start_date": "20261015",
		"end_date":   "20261016",
	})
	require.Equal(t, http.StatusAccepted, code, "作业门必须 202，实际响应：%s", bodyText)

	var created struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
	}
	require.NoError(t, json.Unmarshal([]byte(bodyText), &created))
	require.NotEmpty(t, created.JobID)

	settled := h.waitJobSettled(t, created.JobID)
	require.Equal(t, sync.JobStatusCompleted, settled.Status,
		"作业应成功落定；error_message=%q result=%s", settled.ErrorMessage, string(settled.Result))

	var result struct {
		Persisted    int    `json:"persisted"`
		Fetched      int    `json:"fetched"`
		Succeeded    int    `json:"succeeded"`
		Period       string `json:"period"`
		PersistedVia string `json:"persisted_via"`
	}
	require.NoError(t, json.Unmarshal(settled.Result, &result))
	assert.Equal(t, 4, result.Persisted, "2 个 symbol × 2 个交易日")
	assert.Equal(t, 4, result.Fetched)
	assert.Equal(t, 1, result.Succeeded)
	assert.Equal(t, "daily", result.Period)
	assert.Equal(t, "data-service /ingest/points", result.PersistedVia,
		"落库在哪一侧必须是结果里看得见的事实，否则 persisted 会被读成 core 写的")

	// —— 投出去的几个请求：一片一次 ——
	raw, auth := svc.seen()
	require.Len(t, raw, 1, "4 个点在一门上界内 ⇒ 只该有一片")
	for _, header := range auth {
		assert.Equal(t, "Bearer test-service-token", header, "服务间凭证不带上，对端必然 401")
	}
	// 投送面排除服务器独占字段：这两个键出现即说明投影漏了字段（对端会整批 400）。
	assert.NotContains(t, raw[0], "visible_at", "可见时刻由对端按声明锚点推导，不接受投送")
	assert.NotContains(t, raw[0], "ingest_time", "入库时刻由对端时钟填，不接受投送")

	batches := svc.batches(t)
	require.Len(t, batches, 1)
	require.Equal(t, source.DataTypeCapitalFlow, batches[0].DataType)
	byKey := pointsFromBatch(batches[0])
	require.Len(t, byKey, len(capitalFlowExpectedRows), "投出去的点数必须等于期望行数")

	for _, want := range capitalFlowExpectedRows {
		key := want.Symbol + "|" + want.TradeDate
		p, ok := byKey[key]
		require.True(t, ok, "没投过 %s 这个坐标（投出的是 %v）", key, keysOfPoints(byKey))

		label := "点 " + key
		assert.Equal(t, want.Symbol, p.Symbol, label)
		assert.Equal(t, want.TradeDate, p.TradeTime.UTC().Format("2006-01-02"), label+" trade_time")
		assert.Equal(t, "eastmoney", p.Source, label+" source 必须是链上真正供数的适配器名")
		assert.Equal(t, source.DataTypeCapitalFlow, p.DataType, label+" data_type")
		assert.Equal(t, "daily", p.Data["period"], label+" period")

		assert.InDelta(t, want.MainNet, wireValue(t, p, "main_net"), 1e-9, label)
		assert.InDelta(t, want.SuperNet, wireValue(t, p, "super_net"), 1e-9, label)
		assert.InDelta(t, want.LargeNet, wireValue(t, p, "large_net"), 1e-9, label)
		assert.InDelta(t, want.MediumNet, wireValue(t, p, "medium_net"), 1e-9, label)
		assert.InDelta(t, want.SmallNet, wireValue(t, p, "small_net"), 1e-9, label)
		assert.InDelta(t, want.MainRatio, wireValue(t, p, "main_net_ratio"), 1e-9, label)
		assert.InDelta(t, want.RetailNet, wireValue(t, p, "retail_net"), 1e-9,
			label+" retail_net = −(super+large+medium+small)，手算得来，不是拿代码再算一遍")
		assert.InDelta(t, want.RetailRatio, wireValue(t, p, "retail_net_ratio"), 1e-9,
			label+" main+retail=0 ⇒ 零守卫留 0")
		assert.InDelta(t, want.ClosePrice, wireValue(t, p, "close_price"), 1e-9, label)
		assert.InDelta(t, want.ChangePct, wireValue(t, p, "change_pct"), 1e-9, label)

		// 「不做换算」与「缺失 ≠ 0」在这一跳同样要钉住：若有人在同步边界乘了
		// 100/1000（DATA-04 那个坑换个方向重演），上面的 delta 全崩；
		// 而源没给的列**不许被折成 0 投出去**（对端会把 0 当真实内容比对）。
		assert.NotContains(t, p.Data, "main_buy_amount", label+"：wire schema 没给 ⇒ 键都不许出现")
		assert.NotContains(t, p.Data, "main_sell_amount", label+"：同上")
	}

	// 上游请求形状：adapter 逐 symbol 发请求，两个票各一发；窗口必须带上。
	queries, paths := h.stub.seen()
	assert.Len(t, queries, 2, "两个 symbol 应各发一次上游请求")
	secids := map[string]bool{}
	for _, q := range queries {
		assert.Contains(t, q, "beg=20261015", "窗口没传到源侧，lmt 就还是猜的：%s", q)
		assert.Contains(t, q, "end=20261016", "同上：%s", q)
		for _, s := range []string{"secid=1.888888", "secid=1.888889"} {
			if bytes.Contains([]byte(q), []byte(s)) {
				secids[s] = true
			}
		}
	}
	assert.Len(t, secids, 2, "secid 折错就是把票取成别的票：%v", queries)
	for _, p := range paths {
		assert.Equal(t, "/api/qt/stock/fflow/daykline/get", p)
	}
}

func keysOfPoints(byKey map[string]ingestWirePoint) []string {
	out := make([]string, 0, len(byKey))
	for k := range byKey {
		out = append(out, k)
	}
	return out
}

// TestCapitalFlowJobLayerRerunPostsIdenticalBatches 钉「反复跑这条作业，投出去
// 的字节逐次一致」。
//
// 老的形状是「重跑两次后行数仍是 2」，那钉的是 core 侧 upsert 的幂等。
// H4 ① 之后幂等在对端（`ON CONFLICT DO NOTHING` + 内容比对），core 无法证明
// 它 —— 能证明的是这一侧**不会制造假冲突**：载荷里没有时钟、没有随机量，
// 两次跑出去必须逐字节相同。这一点若破了，对端每次重跑都会 409，
// 而那看起来像「上游改了历史数据」。
//
// 行级 no-op 的证据在 `tellurion-data:internal/store` 的 Writer 套件；
// 跨进程那一腿在 capital_flow_live_test.go（tag `data10live`）。
func TestCapitalFlowJobLayerRerunPostsIdenticalBatches(t *testing.T) {
	svc := &stubIngestService{}
	h := newCapitalFlowHarness(t, ingestPosterFor(t, svc, ingestWriteHardMaxPoints), http.StatusOK)
	h.handler.StartWorkerPool()
	t.Cleanup(h.handler.StopWorkerPool)

	params := map[string]any{
		"symbols":    []string{capitalFlowSymA},
		"start_date": "20261015",
		"end_date":   "20261016",
	}

	var first string
	for run := 1; run <= 2; run++ {
		code, bodyText := h.createJob(t, params)
		require.Equal(t, http.StatusAccepted, code, "第 %d 次：门要 202，响应 %s", run, bodyText)
		var created struct {
			JobID string `json:"job_id"`
		}
		require.NoError(t, json.Unmarshal([]byte(bodyText), &created))
		settled := h.waitJobSettled(t, created.JobID)
		require.Equal(t, sync.JobStatusCompleted, settled.Status, "第 %d 次跑失败：%s", run, settled.ErrorMessage)

		raw, _ := svc.seen()
		require.Len(t, raw, run, "第 %d 次跑应该又多投一片", run)
		if run == 1 {
			first = raw[0]
			continue
		}
		assert.Equal(t, first, raw[run-1], "两次投送逐字节不一致 ⇒ 对端每次重跑都会撞到假冲突")
	}
}

// ---- 诚实性腿（DATA-06 家族在作业层的同形）----

// failingPoster 是写面的失败替身：投出去就被拒。
type failingPoster struct {
	calls int
}

func (f *failingPoster) PostPoints(context.Context, string, []source.UnifiedDataPoint) (int, int, error) {
	f.calls++
	return 0, 0, fmt.Errorf("boom: data-service unreachable")
}

// countingPoster 是「都收下」的替身，回执口径与对端一致（persisted = 已就位）。
type countingPoster struct {
	mu        stdsync.Mutex
	persisted int
	calls     int
}

func (n *countingPoster) PostPoints(_ context.Context, _ string, points []source.UnifiedDataPoint) (int, int, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.persisted += len(points)
	n.calls++
	return len(points), 0, nil
}

// TestCapitalFlowExecutorFailsWhenNothingPersists：源能取到、但一行都没落进去时，
// 作业不许以 completed 收口。执行器本来就收 pointsPoster（H4 ① 之后是跨仓写面），
// 替身是经接口注入的，生产码没有为测试让路。
func TestCapitalFlowExecutorFailsWhenNothingPersists(t *testing.T) {
	failing := &failingPoster{}
	h := newCapitalFlowHarness(t, failing, http.StatusOK)

	rec := &recordingReporter{}
	res, err := h.executor(t).Execute(context.Background(),
		jobWithParams("data10-persist-fail", `{"symbols":["888888.SH"],"start_date":"20261015","end_date":"20261016"}`), rec)

	require.Error(t, err, "取到了但一行没落 ⇒ 必须报错，不能交一个看起来成功的 result")
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "nothing persisted")
	assert.Equal(t, 1, failing.calls, "错误确实来自落库那一跳，不是前置校验拦的")
	assert.GreaterOrEqual(t, rec.errorCalls, 1, "失败要经 progress 上报，界面才看得见")
}

// 一个 chunk 内部**第 2 片被拒** ⇒ 作业仍以 error 收口，但已落的那一片必须
// 算进 persisted，而且 error 要明说「已有 N 行落了」。
//
// 这条存在的理由是 H4 ① 之后新出现的形状：老实现「落库失败 = 这个事务一行没落」
// 是真的，跨仓之后片与片不共享事务，于是变成真的「前一片已落」。把它报成
// 「什么都没发生」，读的人就会直接重跑 —— 而对端把重跑判成 409（同主键不同内容），
// 那次 409 看起来像数据事故，其实是计数少报引来的人为动作。
func TestCapitalFlowExecutorReportsRowsAlreadyLandedWhenAShardFails(t *testing.T) {
	svc := &stubIngestService{failOnRequest: 2}
	h := newCapitalFlowHarness(t, ingestPosterFor(t, svc, 1), http.StatusOK)

	rec := &recordingReporter{}
	res, err := h.executor(t).Execute(context.Background(),
		jobWithParams("h4-partial-shard", `{"symbols":["888888.SH"],"start_date":"20261015","end_date":"20261016"}`), rec)

	require.Error(t, err, "有一片被拒 ⇒ 作业不许以 completed 收口")
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "already landed",
		"error 要说清已有行落了，否则人会按「无事发生」去重跑：%v", err)
	assert.Contains(t, err.Error(), "1 row(s)", "已落的行数要写在报文里，不是让人去数：%v", err)
	assert.GreaterOrEqual(t, rec.errorCalls, 1, "失败要经 progress 上报，界面才看得见")
}

// TestCapitalFlowExecutorFailsWhenUpstreamIsDown 是这条链今天真源的样子：
// 上游非 2xx ⇒ 链失败 ⇒ 作业失败，且原因留在 error 里（不是静默空表）。
func TestCapitalFlowExecutorFailsWhenUpstreamIsDown(t *testing.T) {
	h := newCapitalFlowHarness(t, &countingPoster{}, http.StatusBadGateway)

	rec := &recordingReporter{}
	res, err := h.executor(t).Execute(context.Background(),
		jobWithParams("data10-upstream-502", `{"symbols":["888888.SH"],"start_date":"20261015","end_date":"20261016"}`), rec)

	require.Error(t, err, "上游 502 时作业不许报成功（本机真源今天就是这个形状）")
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "502", "失败原因要留在 error 里进 error_message 给人看：%v", err)
	assert.GreaterOrEqual(t, rec.errorCalls, 1)
}

// TestCapitalFlowExecutorStopsOnContextCancel 钉 ctx 取消真的挡在源前面。
func TestCapitalFlowExecutorStopsOnContextCancel(t *testing.T) {
	h := newCapitalFlowHarness(t, &countingPoster{}, http.StatusOK)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 起跑前就取消：第一个 chunk 之前就该返回

	rec := &recordingReporter{}
	res, err := h.executor(t).Execute(ctx,
		jobWithParams("data10-cancel", `{"symbols":["888888.SH","888889.SH"],"start_date":"20261015","end_date":"20261016"}`), rec)

	assert.ErrorIs(t, err, context.Canceled, "取消要如实上抛 ctx.Err()（worker 据此不写状态，AUD-49/50）")
	assert.Nil(t, res)
	queries, _ := h.stub.seen()
	assert.Empty(t, queries, "已取消的作业不该再去打源")
}

// ---- 门（typed door）的校验腿 ----

func TestCapitalFlowJobDoorRejectsBadRequests(t *testing.T) {
	h := newCapitalFlowHarness(t, &countingPoster{}, http.StatusOK)

	tests := []struct {
		name   string
		params map[string]any
		wants  string
	}{
		{"missing_symbols", map[string]any{"start_date": "20261015"}, "params.symbols is required"},
		{"empty_symbols", map[string]any{"symbols": []string{}}, "must not be empty"},
		// 两种通行格式之外的日期：8 位但月份 13。
		{"bad_date", map[string]any{"symbols": []string{capitalFlowSymA}, "start_date": "20261301"}, "YYYYMMDD"},
		{"bad_period", map[string]any{"symbols": []string{capitalFlowSymA}, "period": "5d"}, "daily"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, bodyText := h.createJob(t, tc.params)
			assert.Equal(t, http.StatusBadRequest, code, "响应：%s", bodyText)
			assert.Contains(t, bodyText, tc.wants)
		})
	}
}

// TestCapitalFlowJobDoorAcceptsCapitalFlowType 是负例的对照腿：
// 门里现在真的认这个名字 —— 不认的话上面所有负例都会以 400 假绿。
func TestCapitalFlowJobDoorAcceptsCapitalFlowType(t *testing.T) {
	h := newCapitalFlowHarness(t, &countingPoster{}, http.StatusOK)

	code, bodyText := h.createJob(t, map[string]any{"symbols": []string{capitalFlowSymA}})
	require.Equal(t, http.StatusAccepted, code, bodyText)

	// 对照：未知类型仍被拒，且 supported 名单里带上了 capital_flow。
	body, err := json.Marshal(map[string]any{"type": "not_a_job_type", "params": map[string]any{}})
	require.NoError(t, err)
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/sync/jobs", bytes.NewReader(body)))
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "capital_flow",
		"拒绝文案里的 supported 名单要跟着门一起改，否则界面在撒谎")
}

// TestCapitalFlowExecutorRejectsPeriodsTheAdapterDoesNotServe 钉执行器这一层的
// period 判定本身（门拦得住 400，但作业行也能被别的入口直接建出来）。
//
// 依据是源码事实：fetchCapitalFlowForSymbol 把 period 当**存储列的值**透传，
// 请求参数恒为 `klt := 1`（日线）。所以接受 "5d" 的效果是把日线数据贴上 5 日
// 标签、以 (symbol, trade_date, "5d") 为主键写进库 —— 同一天会有两行看起来
// 不同其实同源的数据。报错里必须说出 klt=1，否则下一个人只会把它改回放行。
func TestCapitalFlowExecutorRejectsPeriodsTheAdapterDoesNotServe(t *testing.T) {
	h := newCapitalFlowHarness(t, &countingPoster{}, http.StatusOK)

	for _, period := range []string{"5d", "10d", "60d"} {
		rec := &recordingReporter{}
		res, err := h.executor(t).Execute(context.Background(),
			jobWithParams("data10-period-"+period,
				fmt.Sprintf(`{"symbols":[%q],"period":%q}`, capitalFlowSymA, period)), rec)

		require.Error(t, err, "period=%q 必须拒收（adapter 只服务日线）", period)
		assert.Nil(t, res)
		assert.Contains(t, err.Error(), "klt=1", "报错要给出理由：%v", err)
	}

	// 反证腿：空 period 是「用默认值」，不是第二次决策 —— 必须放行并折成 daily。
	rec := &recordingReporter{}
	res, err := h.executor(t).Execute(context.Background(),
		jobWithParams("data10-period-default", fmt.Sprintf(`{"symbols":[%q]}`, capitalFlowSymA)), rec)
	require.NoError(t, err, "空 period 该折成 daily 并放行：%v", err)
	require.NotNil(t, res)
	assert.Equal(t, "daily", res.(map[string]any)["period"])
}

// TestCapitalFlowExecutorRejectsInvertedWindow 钉窗口方向：end 早于 start 时
// 不许把「倒过来的区间」当成空区间静默跑过去。
func TestCapitalFlowExecutorRejectsInvertedWindow(t *testing.T) {
	h := newCapitalFlowHarness(t, &countingPoster{}, http.StatusOK)

	_, err := h.executor(t).Execute(context.Background(),
		jobWithParams("data10-inverted",
			fmt.Sprintf(`{"symbols":[%q],"start_date":"20261016","end_date":"20261015"}`, capitalFlowSymA)),
		&recordingReporter{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "before start_date")

	queries, _ := h.stub.seen()
	assert.Empty(t, queries, "窗口非法时一发请求都不该发出去")
}

// TestNewSyncHandlerRegistersCapitalFlowExecutor 钉住生产装配点本身。
//
// 上面的测试都用手搭的 harness（为了不去动共享队列），于是「NewSyncHandler
// 有没有注册这个执行器、有没有把 Registry 传进去」这两件事本身没了证据 ——
// 而那正是 SYNC-01 那一族「写得对但没人调」的形状。
func TestNewSyncHandlerRegistersCapitalFlowExecutor(t *testing.T) {
	// 不调 gin.SetMode —— AUD-29 只允许它在 pkg/httpserver/ginmode.go 出现，
	// 守卫扫的是全仓 _test.go（本包其余测试同样靠 gin.New() 直接跑）。
	reg := source.NewRegistry()
	poster := &countingPoster{}

	h := NewSyncHandler(nil, nil, nil, reg, poster)
	require.NotNil(t, h)
	h.RegisterExecutors()

	assert.True(t, h.workerPool.HasExecutor(sync.JobTypeCapitalFlow),
		"NewSyncHandler 必须把 capital_flow 执行器注册进 worker pool")
	assert.Same(t, reg, h.registry, "compose root 传进来的 Registry 必须就是执行器用的那一个")

	// H4 ① 之后这条同样要钉到执行器身上：handler 收了写面却没往下传，
	// 每次作业都会 fail-loud 在「写面未配置」——接了等于没接（同 SYNC-01 家族）。
	exec, ok := h.workerPool.GetExecutor(sync.JobTypeCapitalFlow)
	require.True(t, ok)
	capitalExec, ok := exec.(*capitalFlowExecutor)
	require.True(t, ok, "注册的必须是 capitalFlowExecutor")
	assert.Same(t, poster, capitalExec.write, "compose root 传进来的写面必须就是执行器用的那一个")

	// main.go 必须把真正构建出来的 Registry 与写面客户端递给路由，而不是半路
	// 换成 nil —— 那会让每次作业都 fail-loud，接了等于没接。
	mainSrc := readSource(t, "main.go")
	require.Contains(t, mainSrc, "registerRoutes(router, store, cache, tushareClient, dataCache, dataSourceRegistry,",
		"main 没把 dataSourceRegistry 接进路由")
	require.Contains(t, mainSrc, "NewSyncHandler(store, tc, dc, registry, newIngestWritePoster())",
		"main 必须把写面客户端接进 SyncHandler；漏了它，capital_flow / top_list 两条作业一律失败")
}

// ---- 进度替身 ----

type recordingReporter struct {
	progressCalls int
	errorCalls    int
	lastProcessed int
	lastTotal     int
}

func (r *recordingReporter) ReportProgress(processed, total, failed int) {
	r.progressCalls++
	r.lastProcessed = processed
	r.lastTotal = total
}

func (r *recordingReporter) ReportError(string) { r.errorCalls++ }

// TestCapitalFlowExecutorReportsProgressPerChunk 钉进度真的按 chunk 上报：
// 4 个 symbol、batch_size=2 ⇒ 两 chunk ⇒ 两次 ReportProgress，total 恒为 4。
func TestCapitalFlowExecutorReportsProgressPerChunk(t *testing.T) {
	h := newCapitalFlowHarness(t, &countingPoster{}, http.StatusOK)

	job := jobWithParams("data10-progress",
		fmt.Sprintf(`{"symbols":[%q,%q,%q,%q],"batch_size":2,"start_date":"20261015","end_date":"20261016"}`,
			capitalFlowSymA, capitalFlowSymB, "888890.SH", "888891.SH"))
	rec := &recordingReporter{}
	res, err := h.executor(t).Execute(context.Background(), job, rec)

	require.NoError(t, err, "进度腿的作业不该失败：%v", res)
	assert.Equal(t, 2, rec.progressCalls, "两个 chunk 各上报一次")
	assert.Equal(t, 4, rec.lastTotal)
	assert.Equal(t, 4, rec.lastProcessed, "最后一跳必须报满")
}
