package orchestrationworker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-contracts/engine"
)

// ---------- spec ----------

func TestAnchorResolver_FullSpecAndRunIDAlignment(t *testing.T) {
	r := &AnchorResolver{}
	spec := json.RawMessage(`{
		"strategy":"momentum","stock_pool":["600000.SH"],
		"start_date":"2024-01-02","end_date":"2024-12-31","kernel":"go"
	}`)
	req, err := r.Resolve(context.Background(), "run-42", spec)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if req.Strategy != "momentum" || req.StartDate != "2024-01-02" || len(req.StockPool) != 1 {
		t.Fatalf("req = %+v", req)
	}
	if req.RunID != "run-42" {
		t.Fatalf("run id = %q, want the orchestration run id (result identity)", req.RunID)
	}
}

func TestAnchorResolver_ResolvesRunIDAnchorAndOverlaysParams(t *testing.T) {
	var gotID string
	r := &AnchorResolver{LoadBase: func(_ context.Context, runID string) (*BaseRun, error) {
		gotID = runID
		return &BaseRun{
			Strategy:  "expression_template",
			StockPool: []string{"600000.SH"},
			StartDate: "2024-01-02",
			EndDate:   "2024-06-28",
			Kernel:    "go",
		}, nil
	}}
	req, err := r.Resolve(context.Background(), "child-1", json.RawMessage(`{
		"run_id":"base-run","strategy_params":{"lookback":20}
	}`))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if gotID != "base-run" {
		t.Fatalf("loader got %q, want base-run", gotID)
	}
	if req.Strategy != "expression_template" || req.EndDate != "2024-06-28" || req.Kernel != "go" {
		t.Fatalf("req = %+v", req)
	}
	if req.StrategyParams["lookback"] != float64(20) {
		t.Fatalf("strategy_params = %#v", req.StrategyParams)
	}
	if req.RunID != "child-1" {
		t.Fatalf("run id = %q, want child-1", req.RunID)
	}
}

func TestAnchorResolver_RejectsSpecThatIsNeitherFullNorAnchor(t *testing.T) {
	r := &AnchorResolver{}
	if _, err := r.Resolve(context.Background(), "run-1", json.RawMessage(`{"strategy":"m"}`)); err == nil {
		t.Fatal("Resolve accepted a spec without dates and without run_id")
	}
	if _, err := r.Resolve(context.Background(), "run-1", json.RawMessage(`{"run_id":"base"}`)); err == nil {
		t.Fatal("Resolve accepted an anchor without a base loader")
	}
}

func TestParseBaseRun_ExtractsReservedParams(t *testing.T) {
	row := map[string]any{
		"strategy_id": "expression_template",
		"universe":    "600000.SH, 000001.SZ",
		"start_date":  "2024-01-02",
		"end_date":    "2024-06-28",
		"params":      []byte(`{"initial_capital":100000,"kernel":"go","engine_params":{"x":1}}`),
	}
	base, err := ParseBaseRun(row)
	if err != nil {
		t.Fatalf("ParseBaseRun: %v", err)
	}
	if base.Strategy != "expression_template" || base.InitialCapital != 100000 || base.Kernel != "go" {
		t.Fatalf("base = %+v", base)
	}
	if len(base.StockPool) != 2 || base.StockPool[1] != "000001.SZ" {
		t.Fatalf("stock pool = %#v", base.StockPool)
	}
	if base.EngineParams["x"] != float64(1) {
		t.Fatalf("engine params = %#v", base.EngineParams)
	}

	bad := map[string]any{
		"strategy_id": "m", "universe": "all",
		"start_date": "2024-01-02", "end_date": "2024-06-28",
		"params": []byte(`{"kernel":123}`),
	}
	if _, err := ParseBaseRun(bad); err == nil {
		t.Fatal("ParseBaseRun accepted a malformed reserved key")
	}
}

// ---------- executor ----------

type fakeRunner struct {
	resp  *contracts.BacktestResponse
	err   error
	block bool
	got   contracts.BacktestRequest
}

func (f *fakeRunner) RunBacktest(ctx context.Context, req contracts.BacktestRequest) (*contracts.BacktestResponse, error) {
	f.got = req
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.resp, f.err
}

type fakeSink struct {
	saved []*contracts.BacktestResponse
	err   error
}

func (s *fakeSink) Save(_ context.Context, resp *contracts.BacktestResponse) error {
	if s.err != nil {
		return s.err
	}
	s.saved = append(s.saved, resp)
	return nil
}

func newExecutor(runner contracts.EngineRunner, sink ResultSink) *Executor {
	return &Executor{
		RunnerFor: func(string) (contracts.EngineRunner, error) { return runner, nil },
		Sink:      sink,
		Timeout:   2 * time.Second,
	}
}

func TestExecutor_CompletedPersistsAndReturnsURI(t *testing.T) {
	runner := &fakeRunner{resp: &contracts.BacktestResponse{ID: "bt-1", Status: contracts.StatusCompleted}}
	sink := &fakeSink{}
	out, err := newExecutor(runner, sink).Execute(context.Background(), contracts.BacktestRequest{Strategy: "m", RunID: "run-1"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.ResultStatus != contracts.StatusCompleted || out.ResultURI != "backtest:bt-1" {
		t.Fatalf("outcome = %+v", out)
	}
	if len(sink.saved) != 1 {
		t.Fatalf("sink saved %d results", len(sink.saved))
	}
	if runner.got.RunID != "run-1" {
		t.Fatalf("runner got run id %q", runner.got.RunID)
	}
}

func TestExecutor_FailuresNeverLookCompleted(t *testing.T) {
	cases := []struct {
		name   string
		runner *fakeRunner
		sink   ResultSink
	}{
		{"engine error", &fakeRunner{err: errors.New("boom")}, &fakeSink{}},
		{"engine failed status", &fakeRunner{resp: &contracts.BacktestResponse{ID: "bt", Status: contracts.StatusFailed, Error: "bad"}}, &fakeSink{}},
		{"unknown status", &fakeRunner{resp: &contracts.BacktestResponse{ID: "bt", Status: "weird"}}, &fakeSink{}},
		{"sink error", &fakeRunner{resp: &contracts.BacktestResponse{ID: "bt", Status: contracts.StatusCompleted}}, &fakeSink{err: errors.New("db down")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := newExecutor(tc.runner, tc.sink).Execute(context.Background(), contracts.BacktestRequest{Strategy: "m"})
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if out.ResultStatus != contracts.StatusFailed || out.Error == "" {
				t.Fatalf("outcome = %+v, want failed with a message", out)
			}
		})
	}
}

func TestExecutor_InvalidIsNotFailed(t *testing.T) {
	runner := &fakeRunner{resp: &contracts.BacktestResponse{ID: "bt", Status: contracts.StatusInvalid}}
	out, err := newExecutor(runner, &fakeSink{}).Execute(context.Background(), contracts.BacktestRequest{Strategy: "m"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.ResultStatus != contracts.StatusInvalid || out.Error != "" {
		t.Fatalf("outcome = %+v", out)
	}
}

type fakeJobStore struct {
	rows      map[string]map[string]any
	created   int
	completed []string
}

func (s *fakeJobStore) GetBacktestJob(_ context.Context, id string) (map[string]any, error) {
	return s.rows[id], nil
}
func (s *fakeJobStore) CreateBacktestJob(_ context.Context, job map[string]any) error {
	s.created++
	s.rows[job["id"].(string)] = job
	return nil
}
func (s *fakeJobStore) UpdateJobStarted(context.Context, string) error { return nil }
func (s *fakeJobStore) UpdateJobCompleted(_ context.Context, id, status string, _ []byte) error {
	s.completed = append(s.completed, id+":"+status)
	return nil
}

func TestJobStoreSink_CreatesThenUpdates(t *testing.T) {
	store := &fakeJobStore{rows: map[string]map[string]any{}}
	sink := &JobStoreSink{Store: store}
	resp := &contracts.BacktestResponse{ID: "bt-1", Status: contracts.StatusInvalid, Strategy: "m"}

	if err := sink.Save(context.Background(), resp); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	if store.created != 1 || len(store.completed) != 1 || store.completed[0] != "bt-1:invalid" {
		t.Fatalf("created=%d completed=%v", store.created, store.completed)
	}

	// 重跑（租约被回收）走 update，不撞主键。
	resp.Status = contracts.StatusCompleted
	if err := sink.Save(context.Background(), resp); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	if store.created != 1 || len(store.completed) != 2 || store.completed[1] != "bt-1:completed" {
		t.Fatalf("created=%d completed=%v", store.created, store.completed)
	}
}

// ---------- loop ----------

type reported struct {
	workerID string
	status   string
	result   string
	uri      string
	errMsg   string
}

type fakeOrchestration struct {
	mu         sync.Mutex
	workerID   string
	kernels    []string
	jobs       []*ClaimedJob
	cancelIDs  map[string]bool
	heartbeats int
	drains     int
	reports    []reported
	acks       []string
	leaseFault error // 非 nil 时续租返回 409 lease_lost
}

func (f *fakeOrchestration) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	writeJSON := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	apiError := func(status int, code, msg string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": msg, "code": code})
	}

	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/runs/workers":
		var req struct {
			WorkerID string   `json:"worker_id"`
			Kernels  []string `json:"kernels"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.workerID, f.kernels = req.WorkerID, req.Kernels
		writeJSON(map[string]any{"worker_id": req.WorkerID, "status": "active", "kernels": req.Kernels})

	case strings.HasSuffix(r.URL.Path, "/heartbeat"):
		f.heartbeats++
		writeJSON(map[string]any{"worker_id": f.workerID, "status": "active"})

	case strings.HasSuffix(r.URL.Path, "/claim"):
		if len(f.jobs) == 0 {
			writeJSON(map[string]any{"job": nil})
			return
		}
		job := f.jobs[0]
		f.jobs = f.jobs[1:]
		if f.cancelIDs[job.RunID] {
			job.CancelRequested = true
		}
		writeJSON(map[string]any{"job": job})

	case strings.HasSuffix(r.URL.Path, "/lease"):
		if f.leaseFault != nil {
			apiError(http.StatusConflict, "lease_lost", "lease expired")
			return
		}
		writeJSON(map[string]any{"run_id": "run", "lease_until": time.Now().Add(time.Minute).UTC()})

	case strings.HasSuffix(r.URL.Path, "/cancel-ack"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/runs/jobs/"), "/cancel-ack")
		f.acks = append(f.acks, id)
		writeJSON(map[string]any{"job_id": id, "status": "cancelled"})

	case strings.HasSuffix(r.URL.Path, "/result"):
		var req struct {
			WorkerID     string `json:"worker_id"`
			Status       string `json:"status"`
			ResultStatus string `json:"result_status"`
			ResultURI    string `json:"result_uri"`
			Error        string `json:"error"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/runs/jobs/"), "/result")
		f.reports = append(f.reports, reported{
			workerID: req.WorkerID, status: req.Status, result: req.ResultStatus,
			uri: req.ResultURI, errMsg: req.Error,
		})
		writeJSON(map[string]any{"job_id": id, "status": req.Status})

	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/runs/jobs/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/runs/jobs/")
		writeJSON(map[string]any{
			"job_id": id, "status": "running", "cancel_requested": f.cancelIDs[id],
		})

	case strings.HasSuffix(r.URL.Path, "/drain"):
		f.drains++
		writeJSON(map[string]any{"worker_id": f.workerID, "status": "draining"})

	default:
		http.NotFound(w, r)
	}
}

func newLoopForTest(t *testing.T, fake *fakeOrchestration, runner contracts.EngineRunner, sink ResultSink, every time.Duration) *Loop {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	resolver := &AnchorResolver{
		LoadBase: func(_ context.Context, runID string) (*BaseRun, error) {
			return &BaseRun{Strategy: "m", StartDate: "2024-01-02", EndDate: "2024-12-31"}, nil
		},
	}
	return NewLoop(NewClient(srv.URL, srv.Client()), resolver, newExecutor(runner, sink), "w1", nil,
		Options{HeartbeatEvery: every, PollEvery: 10 * time.Millisecond}, zerolog.Nop())
}

func TestLoop_Step_CompletesAndReports(t *testing.T) {
	fake := &fakeOrchestration{jobs: []*ClaimedJob{{
		RunID:   "child-1",
		RunSpec: json.RawMessage(`{"run_id":"base","strategy_params":{"lookback":10}}`),
	}}}
	runner := &fakeRunner{resp: &contracts.BacktestResponse{ID: "child-1", Status: contracts.StatusCompleted}}
	sink := &fakeSink{}
	loop := newLoopForTest(t, fake, runner, sink, 50*time.Millisecond)

	worked, err := loop.Step(context.Background())
	if err != nil || !worked {
		t.Fatalf("Step = %v/%v", worked, err)
	}
	if runner.got.Strategy != "m" || runner.got.RunID != "child-1" {
		t.Fatalf("runner got %+v", runner.got)
	}
	if len(fake.reports) != 1 {
		t.Fatalf("reports = %+v", fake.reports)
	}
	rep := fake.reports[0]
	if rep.workerID != "w1" || rep.status != "completed" || rep.result != "completed" || rep.uri != "backtest:child-1" {
		t.Fatalf("report = %+v", rep)
	}
}

func TestLoop_Step_EngineFailureReportsFailed(t *testing.T) {
	fake := &fakeOrchestration{jobs: []*ClaimedJob{{
		RunID: "child-1", RunSpec: json.RawMessage(`{"run_id":"base"}`),
	}}}
	loop := newLoopForTest(t, fake, &fakeRunner{err: errors.New("boom")}, &fakeSink{}, 50*time.Millisecond)

	if _, err := loop.Step(context.Background()); err != nil {
		t.Fatalf("Step: %v", err)
	}
	if len(fake.reports) != 1 || fake.reports[0].status != "failed" || !strings.Contains(fake.reports[0].errMsg, "boom") {
		t.Fatalf("reports = %+v", fake.reports)
	}
}

func TestLoop_Step_CancelRequestedAcksWithoutExecuting(t *testing.T) {
	fake := &fakeOrchestration{
		jobs:      []*ClaimedJob{{RunID: "child-1", RunSpec: json.RawMessage(`{"run_id":"base"}`)}},
		cancelIDs: map[string]bool{"child-1": true},
	}
	runner := &fakeRunner{resp: &contracts.BacktestResponse{ID: "child-1", Status: contracts.StatusCompleted}}
	loop := newLoopForTest(t, fake, runner, &fakeSink{}, 50*time.Millisecond)

	if _, err := loop.Step(context.Background()); err != nil {
		t.Fatalf("Step: %v", err)
	}
	if len(fake.acks) != 1 || fake.acks[0] != "child-1" {
		t.Fatalf("acks = %+v", fake.acks)
	}
	if runner.got.Strategy != "" {
		t.Fatal("cancelled job must not reach the engine")
	}
	if len(fake.reports) != 0 {
		t.Fatalf("cancelled job must not be reported as a result: %+v", fake.reports)
	}
}

func TestLoop_Step_LeaseLostDropsOutcome(t *testing.T) {
	fake := &fakeOrchestration{
		jobs:       []*ClaimedJob{{RunID: "child-1", RunSpec: json.RawMessage(`{"run_id":"base"}`)}},
		leaseFault: errors.New("lost"),
	}
	runner := &fakeRunner{block: true}
	loop := newLoopForTest(t, fake, runner, &fakeSink{}, 10*time.Millisecond)

	start := time.Now()
	if _, err := loop.Step(context.Background()); err != nil {
		t.Fatalf("Step: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("Step did not stop after lease loss")
	}
	if len(fake.reports) != 0 {
		t.Fatalf("outcome written after lease loss: %+v", fake.reports)
	}
}

func TestLoop_Step_CancelDuringRunAcks(t *testing.T) {
	fake := &fakeOrchestration{
		jobs:      []*ClaimedJob{{RunID: "child-1", RunSpec: json.RawMessage(`{"run_id":"base"}`)}},
		cancelIDs: map[string]bool{"child-1": true},
	}
	// claim 时还没取消（模拟「跑起来之后运维才点取消」）：先把它藏起来，
	// 由 Job 轮询看到。
	fake.cancelIDs = map[string]bool{}
	runner := &fakeRunner{block: true}
	loop := newLoopForTest(t, fake, runner, &fakeSink{}, 10*time.Millisecond)
	// 第一次 Job 轮询之前打开取消。
	go func() {
		time.Sleep(15 * time.Millisecond)
		fake.mu.Lock()
		fake.cancelIDs["child-1"] = true
		fake.mu.Unlock()
	}()

	if _, err := loop.Step(context.Background()); err != nil {
		t.Fatalf("Step: %v", err)
	}
	if len(fake.acks) != 1 {
		t.Fatalf("acks = %+v", fake.acks)
	}
	if len(fake.reports) != 0 {
		t.Fatalf("cancelled run reported as a result: %+v", fake.reports)
	}
}

func TestLoop_Step_NoWorkReturnsFalse(t *testing.T) {
	fake := &fakeOrchestration{}
	loop := newLoopForTest(t, fake, &fakeRunner{}, &fakeSink{}, 50*time.Millisecond)
	worked, err := loop.Step(context.Background())
	if err != nil || worked {
		t.Fatalf("Step = %v/%v, want false/nil", worked, err)
	}
}

func TestClient_MapsWireCodesToSentinels(t *testing.T) {
	fake := &fakeOrchestration{leaseFault: errors.New("lost")}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	c := NewClient(srv.URL, srv.Client())

	err := c.RenewLease(context.Background(), "run-1", "w1")
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("err = %v, want ErrLeaseLost", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "lease_lost" {
		t.Fatalf("err = %#v", err)
	}
}

func TestClient_ClaimReturnsNilWhenQueueEmpty(t *testing.T) {
	fake := &fakeOrchestration{}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	job, err := NewClient(srv.URL, srv.Client()).Claim(context.Background(), "w1")
	if err != nil || job != nil {
		t.Fatalf("Claim = %+v / %v", job, err)
	}
}
