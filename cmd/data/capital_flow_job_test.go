package main

// DATA-10 ⑤ 的主证据：一条从**作业层真的走一次**的测试。
//
// 工单原话是「POST /api/sync/jobs（job_type=capital_flow）→ worker executor
// → ETLPipeline.Process → 落库，**不是**直接调 Process 的行为测试」。
// 本文件从 HTTP 门开始，经 JobService → Queue.Dequeue → WorkerPool 的真实派发
// （含 jobProgressReporter 与重试收敛）→ capitalFlowExecutor → NewETLPipeline →
// Registry.Fetch（链 capital_flow:[eastmoney]）→ BulkInsert，落的是**真库**
// `quant_trading.capital_flow`，落完再逐字段读回与源响应比对。
//
// 两处是替身，理由如实写在这儿：
//   - **作业行（sync_jobs）用内存 store**，行情落库用真库。原因不是省事，是
//     并发安全：`Queue.Dequeue` 取「最老的 pending」，不看 job_type。测试若起
//     真队列，会把**别的 agent 正在排的队**抢过来，用这套（tc 为 nil 的）执行器
//     把别人的作业打失败 —— 那是拿别人的作业给自己作证。内存 store 让队列里
//     只有本测试的作业，真库那一半（落库与读数）没有打折。
//   - **东财 push2 用 httptest 替身**。工单 ② 写「primary adapter 已启用、
//     接线即灌数」；2026-10-16 实测：`/api/qt/stock/fflow/daykline/get` 这条
//     path 本机不可达（push2 恒 502、push2his 连接重置、浏览器同结果；同主机的
//     ulist.np / kline / fflow/kline 都 200）。真源那条腿在
//     capital_flow_live_test.go（build tag `data10live`），不放进默认套件赌网络。
//
// 落库符号用 888888.SH / 888889.SH —— 不是真实 A 股代码；测试前后各删一次并
// 断言清零，不给共享开发库留野数据。

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
	"tellurion/pkg/storage"
	"tellurion/pkg/sync"
	"tellurion/pkg/testenv"
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
// 把链上的源指向替身。落库那一半由 persist 参数决定，本文件传真库 store。
func newCapitalFlowHarness(t *testing.T, persist storage.BulkInserter, stubStatus int) *capitalFlowHarness {
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
	pool.RegisterExecutor(&capitalFlowExecutor{reg: registry, persist: persist})

	h := &SyncHandler{jobService: jobService, queue: queue, workerPool: pool, registry: registry}
	router := gin.New()
	router.POST("/api/sync/jobs", h.createJobHandler)

	return &capitalFlowHarness{handler: h, jobs: jobs, router: router, stub: stub}
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

// ---- 真库读写 ----

func capitalFlowStore(t *testing.T) *storage.PostgresStore {
	t.Helper()
	dsn := testenv.DatabaseDSN()
	if dsn == "" {
		testenv.SkipNoDB(t)
	}
	store, err := storage.NewPostgresStoreConnected(context.Background(), dsn)
	if err != nil {
		t.Fatalf("[testdb] TEST_DATABASE_DSN 已设置但连不上（fail-loud）: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

// cleanCapitalFlowProbes 只按探针 symbol 收窄，绝不写全表 DELETE —— 共享开发库。
func cleanCapitalFlowProbes(t *testing.T, store *storage.PostgresStore) {
	t.Helper()
	_, err := store.DB().Exec(context.Background(),
		`DELETE FROM capital_flow WHERE symbol IN ($1, $2)`, capitalFlowSymA, capitalFlowSymB)
	require.NoError(t, err)
}

type capitalFlowRow struct {
	Symbol      string
	TradeDate   time.Time
	Period      string
	MainNet     *float64
	SuperNet    *float64
	LargeNet    *float64
	MediumNet   *float64
	SmallNet    *float64
	MainRatio   *float64
	RetailNet   *float64
	RetailRatio *float64
	ClosePrice  *float64
	ChangePct   *float64
	MainBuy     *float64
	MainSell    *float64
	Source      string
	IngestTime  time.Time
}

func readCapitalFlowRows(t *testing.T, store *storage.PostgresStore) []capitalFlowRow {
	t.Helper()
	rows, err := store.DB().Query(context.Background(),
		`SELECT symbol, trade_date, period, main_net, super_net, large_net, medium_net, small_net,
		        main_net_ratio, retail_net, retail_net_ratio, close_price, change_pct,
		        main_buy_amount, main_sell_amount, source, ingest_time
		 FROM capital_flow WHERE symbol IN ($1, $2) ORDER BY symbol, trade_date`,
		capitalFlowSymA, capitalFlowSymB)
	require.NoError(t, err)
	defer rows.Close()

	var out []capitalFlowRow
	for rows.Next() {
		var r capitalFlowRow
		require.NoError(t, rows.Scan(&r.Symbol, &r.TradeDate, &r.Period, &r.MainNet, &r.SuperNet,
			&r.LargeNet, &r.MediumNet, &r.SmallNet, &r.MainRatio, &r.RetailNet, &r.RetailRatio,
			&r.ClosePrice, &r.ChangePct, &r.MainBuy, &r.MainSell, &r.Source, &r.IngestTime))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

// ---- 主证据：作业层真的走一次 + 落库逐字段对拍 ----

func TestCapitalFlowJobLayerPersistsRowsMatchingSourceResponse(t *testing.T) {
	store := capitalFlowStore(t)
	cleanCapitalFlowProbes(t, store)
	t.Cleanup(func() { cleanCapitalFlowProbes(t, store) })

	// 前置：探针 symbol 必须干净，否则「读回 4 行」可能数到别人的残留。
	require.Empty(t, readCapitalFlowRows(t, store), "前置失败：探针 symbol 上还有残留行")

	h := newCapitalFlowHarness(t, store, http.StatusOK)
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
		Persisted int    `json:"persisted"`
		Fetched   int    `json:"fetched"`
		Succeeded int    `json:"succeeded"`
		Period    string `json:"period"`
	}
	require.NoError(t, json.Unmarshal(settled.Result, &result))
	assert.Equal(t, 4, result.Persisted, "2 个 symbol × 2 个交易日")
	assert.Equal(t, 4, result.Fetched)
	assert.Equal(t, 1, result.Succeeded)
	assert.Equal(t, "daily", result.Period)

	// —— 落库后抽样比对：逐字段 ——
	got := readCapitalFlowRows(t, store)
	require.Len(t, got, len(capitalFlowExpectedRows), "读回行数必须等于期望行数")

	for i, want := range capitalFlowExpectedRows {
		r := got[i]
		label := fmt.Sprintf("行 %d (%s %s)", i, want.Symbol, want.TradeDate)

		assert.Equal(t, want.Symbol, r.Symbol, label)
		assert.Equal(t, want.TradeDate, r.TradeDate.UTC().Format("2006-01-02"), label+" trade_date")
		assert.Equal(t, "daily", r.Period, label+" period")
		assert.Equal(t, "eastmoney", r.Source, label+" source 必须是链上真正供数的适配器名")

		assert.InDelta(t, want.MainNet, derefFloat(t, r.MainNet, label+" main_net"), 1e-9, label)
		assert.InDelta(t, want.SuperNet, derefFloat(t, r.SuperNet, label+" super_net"), 1e-9, label)
		assert.InDelta(t, want.LargeNet, derefFloat(t, r.LargeNet, label+" large_net"), 1e-9, label)
		assert.InDelta(t, want.MediumNet, derefFloat(t, r.MediumNet, label+" medium_net"), 1e-9, label)
		assert.InDelta(t, want.SmallNet, derefFloat(t, r.SmallNet, label+" small_net"), 1e-9, label)
		assert.InDelta(t, want.MainRatio, derefFloat(t, r.MainRatio, label+" main_net_ratio"), 1e-9, label)
		assert.InDelta(t, want.RetailNet, derefFloat(t, r.RetailNet, label+" retail_net"), 1e-9,
			label+" retail_net = −(super+large+medium+small)，手算得来，不是拿代码再算一遍")
		assert.InDelta(t, want.RetailRatio, derefFloat(t, r.RetailRatio, label+" retail_net_ratio"), 1e-9,
			label+" main+retail=0 ⇒ 零守卫留 0")
		assert.InDelta(t, want.ClosePrice, derefFloat(t, r.ClosePrice, label+" close_price"), 1e-9, label)
		assert.InDelta(t, want.ChangePct, derefFloat(t, r.ChangePct, label+" change_pct"), 1e-9, label)

		// 「不做换算」在这里被钉住：库里就是源给的元。若有人在同步边界乘了
		// 100/1000（DATA-04 那个坑换个方向重演），上面每一条 delta 断言同时崩。
		assert.Nil(t, r.MainBuy, label+" main_buy_amount：wire schema 没给 ⇒ NULL，不许折成 0")
		assert.Nil(t, r.MainSell, label+" main_sell_amount：同上")

		assert.False(t, r.IngestTime.IsZero(), label+" ingest_time 由 pipeline 盖章，必须非零")
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

	// 收尾：清完探针行后表必须回到「本测试没来过」的状态。
	require.NoError(t, store.DB().QueryRow(context.Background(),
		`SELECT count(*) FROM capital_flow WHERE symbol IN ($1,$2)`, capitalFlowSymA, capitalFlowSymB,
	).Scan(new(int)))
}

func derefFloat(t *testing.T, v *float64, label string) float64 {
	t.Helper()
	require.NotNil(t, v, "%s 不该是 NULL", label)
	return *v
}

// TestCapitalFlowJobLayerRerunIsIdempotent 钉「同一作业再跑一次不产生重复行」。
// upsert 是这条链敢反复跑的前提。
func TestCapitalFlowJobLayerRerunIsIdempotent(t *testing.T) {
	store := capitalFlowStore(t)
	cleanCapitalFlowProbes(t, store)
	t.Cleanup(func() { cleanCapitalFlowProbes(t, store) })

	params := map[string]any{
		"symbols":    []string{capitalFlowSymA},
		"start_date": "20261015",
		"end_date":   "20261016",
	}

	h := newCapitalFlowHarness(t, store, http.StatusOK)
	h.handler.StartWorkerPool()
	t.Cleanup(h.handler.StopWorkerPool)

	for run := 1; run <= 2; run++ {
		code, bodyText := h.createJob(t, params)
		require.Equal(t, http.StatusAccepted, code, "第 %d 次：门要 202，响应 %s", run, bodyText)
		var created struct {
			JobID string `json:"job_id"`
		}
		require.NoError(t, json.Unmarshal([]byte(bodyText), &created))
		settled := h.waitJobSettled(t, created.JobID)
		require.Equal(t, sync.JobStatusCompleted, settled.Status, "第 %d 次跑失败：%s", run, settled.ErrorMessage)

		// 无论第几轮，行数恒为 2（两个交易日），不是 4 / 6 / 8。
		got := readCapitalFlowRows(t, store)
		assert.Len(t, got, 2, "第 %d 次重跑后行数变了 ⇒ upsert 幂等没成立", run)
	}
}

// ---- 诚实性腿（DATA-06 家族在作业层的同形）----

type failingBulkInserter struct {
	calls int
}

func (f *failingBulkInserter) BulkInsert(context.Context, string, []storage.UnifiedDataPoint) (int, int, error) {
	f.calls++
	return 0, 0, fmt.Errorf("boom: capital_flow unreachable")
}

type countingBulkInserter struct {
	mu        stdsync.Mutex
	persisted int
}

func (n *countingBulkInserter) BulkInsert(_ context.Context, _ string, points []storage.UnifiedDataPoint) (int, int, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.persisted += len(points)
	return len(points), 0, nil
}

// TestCapitalFlowExecutorFailsWhenNothingPersists：源能取到、但一行都没落进去时，
// 作业不许以 completed 收口。执行器本来就收 storage.BulkInserter（CR-21），
// 替身是经接口注入的，生产码没有为测试让路。
func TestCapitalFlowExecutorFailsWhenNothingPersists(t *testing.T) {
	failing := &failingBulkInserter{}
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

// TestCapitalFlowExecutorFailsWhenUpstreamIsDown 是这条链今天真源的样子：
// 上游非 2xx ⇒ 链失败 ⇒ 作业失败，且原因留在 error 里（不是静默空表）。
func TestCapitalFlowExecutorFailsWhenUpstreamIsDown(t *testing.T) {
	h := newCapitalFlowHarness(t, &countingBulkInserter{}, http.StatusBadGateway)

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
	h := newCapitalFlowHarness(t, &countingBulkInserter{}, http.StatusOK)

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
	h := newCapitalFlowHarness(t, &countingBulkInserter{}, http.StatusOK)

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
	h := newCapitalFlowHarness(t, &countingBulkInserter{}, http.StatusOK)

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
	h := newCapitalFlowHarness(t, &countingBulkInserter{}, http.StatusOK)

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
	h := newCapitalFlowHarness(t, &countingBulkInserter{}, http.StatusOK)

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

	h := NewSyncHandler(nil, nil, nil, reg)
	require.NotNil(t, h)
	h.RegisterExecutors()

	assert.True(t, h.workerPool.HasExecutor(sync.JobTypeCapitalFlow),
		"NewSyncHandler 必须把 capital_flow 执行器注册进 worker pool")
	assert.Same(t, reg, h.registry, "compose root 传进来的 Registry 必须就是执行器用的那一个")

	// main.go 必须把真正构建出来的 Registry 递给路由，而不是半路换成 nil ——
	// 那会让每次作业都 fail-loud 在 `etl: nil registry`，接了等于没接。
	mainSrc := readSource(t, "main.go")
	require.Contains(t, mainSrc, "registerRoutes(router, store, cache, tushareClient, dataCache, dataSourceRegistry,",
		"main 没把 dataSourceRegistry 接进路由")
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
	h := newCapitalFlowHarness(t, &countingBulkInserter{}, http.StatusOK)

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
