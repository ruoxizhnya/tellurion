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
	"github.com/ruoxizhnya/tellurion-contracts/run"
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

func TestParseBaseRunSpec_ReadsCanonicalSpec(t *testing.T) {
	raw := json.RawMessage(`{"strategy":"expression_template","stock_pool":["600000.SH","000001.SZ"],` +
		`"start_date":"2024-01-02","end_date":"2024-06-28","initial_capital":100000,` +
		`"kernel":"go","engine_params":{"x":1}}`)
	base, err := ParseBaseRunSpec(raw)
	if err != nil {
		t.Fatalf("ParseBaseRunSpec: %v", err)
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

	// 锚点不能再当 base（递归）与空 spec 都必须当场拒绝。
	if _, err := ParseBaseRunSpec(json.RawMessage(`{"run_id":"base"}`)); err == nil {
		t.Fatal("ParseBaseRunSpec accepted a run_id-only anchor as base")
	}
	if _, err := ParseBaseRunSpec(nil); err == nil {
		t.Fatal("ParseBaseRunSpec accepted an empty spec")
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

func TestExecutor_WritesFailedRowOnBusinessFailure(t *testing.T) {
	face := &fakeResultFace{}
	runner := &fakeRunner{err: errors.New("boom")}
	exec := &Executor{
		RunnerFor: func(string) (contracts.EngineRunner, error) { return runner, nil },
		Face:      face,
		Timeout:   2 * time.Second,
	}
	out, err := exec.Execute(context.Background(), contracts.BacktestRequest{Strategy: "m", RunID: "run-7"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.ResultStatus != contracts.StatusFailed {
		t.Fatalf("outcome = %+v", out)
	}
	// 失败必须留痕：没有 failed 行，core 的 provenance 会把失败的 run 读成
	// 「未收尾」（409），永远等不到终态。
	if len(face.ids) != 1 || face.ids[0] != "run-7" || face.status[0] != "failed" {
		t.Fatalf("failed row = ids %v status %v", face.ids, face.status)
	}
	if len(face.bodies[0]) != 0 {
		t.Fatalf("failed row must carry no payload, got %q", face.bodies[0])
	}
}

func TestExecutor_AbortDoesNotWriteFailedRow(t *testing.T) {
	face := &fakeResultFace{}
	runner := &fakeRunner{block: true}
	exec := &Executor{
		RunnerFor: func(string) (contracts.EngineRunner, error) { return runner, nil },
		Face:      face,
		Timeout:   2 * time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // worker 自身 abort（关停 / 租约丢失）—— 编排侧会重排，不钉终态
	if _, err := exec.Execute(ctx, contracts.BacktestRequest{Strategy: "m", RunID: "run-8"}); err == nil {
		t.Fatal("aborted execution must surface an error")
	}
	if len(face.ids) != 0 {
		t.Fatalf("abort must not write a failed row: %v", face.ids)
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

// ---------- 结果面 sink（SVC-ORCH-G3） ----------

type fakeResultFace struct {
	ids    []string
	status []string
	bodies [][]byte
	failOn string
}

func (f *fakeResultFace) SaveBacktestResult(_ context.Context, runID, status string, result []byte) error {
	if f.failOn != "" && f.failOn == runID {
		return errors.New("result face unavailable")
	}
	f.ids = append(f.ids, runID)
	f.status = append(f.status, status)
	f.bodies = append(f.bodies, result)
	return nil
}

func TestResultStoreSink_WritesCompletedAndInvalid(t *testing.T) {
	face := &fakeResultFace{}
	sink := &ResultStoreSink{Store: face}

	resp := &contracts.BacktestResponse{ID: "run-9", Status: contracts.StatusCompleted, Strategy: "momentum"}
	if err := sink.Save(context.Background(), resp); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(face.ids) != 1 || face.ids[0] != "run-9" || face.status[0] != "completed" {
		t.Fatalf("ids=%v status=%v", face.ids, face.status)
	}
	var decoded map[string]any
	if err := json.Unmarshal(face.bodies[0], &decoded); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	if decoded["id"] != "run-9" || decoded["strategy"] != "momentum" {
		t.Fatalf("payload lost fields: %v", decoded)
	}

	// invalid 是 K9 的「跑完但无效」，同样要落结果面。
	resp.Status = contracts.StatusInvalid
	if err := sink.Save(context.Background(), resp); err != nil {
		t.Fatalf("Save invalid: %v", err)
	}
	if face.status[1] != "invalid" {
		t.Fatalf("second status = %q", face.status[1])
	}
}

func TestResultStoreSink_RejectsFailedAndBadInput(t *testing.T) {
	face := &fakeResultFace{}
	sink := &ResultStoreSink{Store: face}

	if err := sink.Save(context.Background(), &contracts.BacktestResponse{ID: "r", Status: contracts.StatusFailed}); err == nil {
		t.Fatal("failed status must not be written to the result face (no payload)")
	}
	if err := sink.Save(context.Background(), nil); err == nil {
		t.Fatal("nil response must be rejected")
	}
	if err := sink.Save(context.Background(), &contracts.BacktestResponse{ID: "  ", Status: contracts.StatusCompleted}); err == nil {
		t.Fatal("empty id must be rejected")
	}
	if len(face.ids) != 0 {
		t.Fatalf("rejected inputs must not reach the store: %v", face.ids)
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
	jobs       []*run.ClaimView
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
	fake := &fakeOrchestration{jobs: []*run.ClaimView{{
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
	fake := &fakeOrchestration{jobs: []*run.ClaimView{{
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
		jobs:      []*run.ClaimView{{RunID: "child-1", RunSpec: json.RawMessage(`{"run_id":"base"}`)}},
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
		jobs:       []*run.ClaimView{{RunID: "child-1", RunSpec: json.RawMessage(`{"run_id":"base"}`)}},
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
		jobs:      []*run.ClaimView{{RunID: "child-1", RunSpec: json.RawMessage(`{"run_id":"base"}`)}},
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
