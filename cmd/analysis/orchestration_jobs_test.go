package main

// SVC-ORCH-G3（core 侧）的读面 / 提交面测试。
//
// 三条腿：
//  1. run_spec 只翻本地执行路径真正消费的字段（不多翻，避免新旧路径结果漂移）；
//  2. 读面按「存量 → 结果面 → 任务面」解析，且列表按 id 去重；
//  3. 异步入口在配置了编排服务时提交到任务面（202 + job_id），不再写本地裸表。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-contracts/engine"
	"tellurion/internal/orchestrationworker"
	"tellurion/pkg/backtest"
	"tellurion/pkg/storage"
)

// ─── 桩 ────────────────────────────────────────────────────────────────

type fakeLegacyJobStore struct {
	get     map[string]any
	getErr  error
	list    []map[string]any
	listErr error
}

func (f *fakeLegacyJobStore) CreateBacktestJob(context.Context, map[string]any) error { return nil }
func (f *fakeLegacyJobStore) UpdateJobStarted(context.Context, string) error          { return nil }
func (f *fakeLegacyJobStore) UpdateJobCompleted(context.Context, string, string, []byte) error {
	return nil
}
func (f *fakeLegacyJobStore) UpdateJobFailed(context.Context, string, string) error { return nil }
func (f *fakeLegacyJobStore) DeleteBacktestJob(context.Context, string) error       { return nil }
func (f *fakeLegacyJobStore) GetBacktestJob(_ context.Context, _ string) (map[string]any, error) {
	return f.get, f.getErr
}
func (f *fakeLegacyJobStore) ListBacktestJobs(_ context.Context, _ int) ([]map[string]any, error) {
	return f.list, f.listErr
}

type fakeResultReader struct {
	rec *storage.BacktestResultRecord
	err error
}

func (f *fakeResultReader) GetBacktestResult(_ context.Context, _ string) (*storage.BacktestResultRecord, error) {
	return f.rec, f.err
}

func legacyRow(id string, created time.Time) map[string]any {
	return map[string]any{
		"id": id, "strategy_id": "momentum", "params": []byte("{}"),
		"universe": "600000.SH", "start_date": "2026-01-01", "end_date": "2026-03-01",
		"status": "completed", "result_status": "completed", "created_at": created,
	}
}

// ─── run_spec 映射 ─────────────────────────────────────────────────────

func TestBuildAsyncRunSpecMapsLocalExecutionFields(t *testing.T) {
	req := backtest.CreateJobRequest{
		StrategyID:     "momentum",
		Universe:       "universe: 600000.SH, 000001.SZ ",
		StartDate:      "2026-01-01",
		EndDate:        "2026-03-01",
		InitialCapital: 1_000_000,
		Kernel:         "go",
		CommissionRate: 0.0003,
		SlippageRate:   0.0002,
		Params:         map[string]any{"lookback": 20},
		EngineParams:   map[string]any{"benchmark_index_code": "000300.SH"},
	}

	raw, err := buildAsyncRunSpec(req, nil)
	if err != nil {
		t.Fatalf("buildAsyncRunSpec: %v", err)
	}
	var spec map[string]any
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("spec is not JSON: %v", err)
	}

	if spec["strategy"] != "momentum" || spec["start_date"] != "2026-01-01" || spec["end_date"] != "2026-03-01" {
		t.Fatalf("core fields missing: %v", spec)
	}
	if spec["kernel"] != "go" {
		t.Fatalf("kernel = %v, want go", spec["kernel"])
	}
	if spec["initial_capital"] != float64(1_000_000) {
		t.Fatalf("initial_capital = %v", spec["initial_capital"])
	}
	pool, ok := spec["stock_pool"].([]any)
	if !ok || len(pool) != 2 || pool[0] != "600000.SH" || pool[1] != "000001.SZ" {
		t.Fatalf("stock_pool = %v", spec["stock_pool"])
	}
	ep, ok := spec["engine_params"].(map[string]any)
	if !ok || ep["benchmark_index_code"] != "000300.SH" {
		t.Fatalf("engine_params = %v", spec["engine_params"])
	}
	// 手续费 / 滑点 / 策略参数不在本地异步执行路径的消费面里：多翻会让
	// 新旧路径跑出不同结果，所以 spec 里必须没有它们。
	for _, banned := range []string{"commission_rate", "slippage_rate", "strategy_params", "params"} {
		if _, exists := spec[banned]; exists {
			t.Fatalf("spec must not carry %q (local async path never applies it): %v", banned, spec)
		}
	}
}

func TestBuildAsyncRunSpecMarketRouting(t *testing.T) {
	router, err := contracts.NewMarketRouter(map[string]string{"CN": "go"})
	if err != nil {
		t.Fatalf("NewMarketRouter: %v", err)
	}
	req := backtest.CreateJobRequest{
		StrategyID: "momentum", Universe: "600000.SH",
		StartDate: "2026-01-01", EndDate: "2026-03-01", Market: "CN",
	}
	raw, err := buildAsyncRunSpec(req, router)
	if err != nil {
		t.Fatalf("buildAsyncRunSpec: %v", err)
	}
	var spec map[string]any
	_ = json.Unmarshal(raw, &spec)
	if spec["kernel"] != "go" {
		t.Fatalf("market CN should resolve to kernel go, got %v", spec["kernel"])
	}

	req.Market = "MARS"
	if _, err := buildAsyncRunSpec(req, router); err == nil {
		t.Fatal("unregistered market must be refused (no silent fallback)")
	}
}

// ─── 读面：三源 ────────────────────────────────────────────────────────

func TestOrchestrationJobStoreGetPrefersLegacyRow(t *testing.T) {
	store := newOrchestrationJobStore(
		&fakeLegacyJobStore{get: legacyRow("r1", time.Now())},
		&fakeResultReader{err: storage.ErrBacktestResultNotFound},
		nil, zerolog.Nop(),
	)
	got, err := store.GetBacktestJob(context.Background(), "r1")
	if err != nil || got == nil || got["id"] != "r1" {
		t.Fatalf("legacy row must win: %v %v", got, err)
	}
}

func TestOrchestrationJobStoreGetFallsBackToResultFace(t *testing.T) {
	rec := &storage.BacktestResultRecord{
		RunID: "run-9", ResultStatus: "invalid",
		Result:       json.RawMessage(`{"strategy":"size","start_date":"2026-02-01","end_date":"2026-03-01","stock_pool":["600000.SH"]}`),
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
		ErrorMessage: "",
	}
	store := newOrchestrationJobStore(
		&fakeLegacyJobStore{},
		&fakeResultReader{rec: rec},
		nil, zerolog.Nop(),
	)
	got, err := store.GetBacktestJob(context.Background(), "run-9")
	if err != nil {
		t.Fatalf("GetBacktestJob: %v", err)
	}
	if got["status"] != "completed" || got["result_status"] != "invalid" {
		t.Fatalf("result face mapping wrong: %v", got)
	}
	if got["strategy_id"] != "size" || got["universe"] != "600000.SH" {
		t.Fatalf("payload fields not projected: %v", got)
	}
}

func TestOrchestrationJobStoreGetFallsBackToTaskFace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/runs/jobs/run-1" || r.Method != http.MethodGet {
			t.Errorf("unexpected call %s %s", r.Method, r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"job_id":"run-1","status":"running","cancel_requested":false}`)
	}))
	defer srv.Close()

	store := newOrchestrationJobStore(
		&fakeLegacyJobStore{},
		&fakeResultReader{err: storage.ErrBacktestResultNotFound},
		orchestrationworker.NewClient(srv.URL, srv.Client()), zerolog.Nop(),
	)
	got, err := store.GetBacktestJob(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("GetBacktestJob: %v", err)
	}
	if got == nil || got["status"] != "running" {
		t.Fatalf("task face status lost: %v", got)
	}
}

func TestOrchestrationJobStoreGetUnknownTaskIsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"job not found","code":"not_found"}`)
	}))
	defer srv.Close()

	store := newOrchestrationJobStore(
		&fakeLegacyJobStore{},
		&fakeResultReader{err: storage.ErrBacktestResultNotFound},
		orchestrationworker.NewClient(srv.URL, srv.Client()), zerolog.Nop(),
	)
	got, err := store.GetBacktestJob(context.Background(), "missing")
	if err != nil || got != nil {
		t.Fatalf("unknown job should be (nil, nil), got %v %v", got, err)
	}
}

func TestOrchestrationJobStoreListMergesAndDedupes(t *testing.T) {
	now := time.Now()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"total":2,"jobs":[
			{"job_id":"r1","status":"completed","created_at":"`+now.Add(-time.Minute).Format(time.RFC3339)+`"},
			{"job_id":"r2","status":"pending","created_at":"`+now.Format(time.RFC3339)+`"}]}`)
	}))
	defer srv.Close()

	store := newOrchestrationJobStore(
		&fakeLegacyJobStore{list: []map[string]any{legacyRow("r1", now.Add(-time.Minute))}},
		&fakeResultReader{err: storage.ErrBacktestResultNotFound},
		orchestrationworker.NewClient(srv.URL, srv.Client()), zerolog.Nop(),
	)
	got, err := store.ListBacktestJobs(context.Background(), 20)
	if err != nil {
		t.Fatalf("ListBacktestJobs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 rows after dedupe, got %d: %v", len(got), got)
	}
	ids := []string{got[0]["id"].(string), got[1]["id"].(string)}
	sort.Strings(ids)
	if ids[0] != "r1" || ids[1] != "r2" {
		t.Fatalf("ids = %v", ids)
	}
	if got[0]["id"] != "r2" {
		t.Fatalf("list must be created_at desc (r2 newest), got %v first", got[0]["id"])
	}
}

// ─── 异步入口：提交到任务面 ─────────────────────────────────────────────

func TestAsyncEntrySubmitsToOrchestration(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/runs/jobs" {
			t.Errorf("unexpected call %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = io.WriteString(w, `{"job_id":"job-7","status":"pending","idempotent_replay":false}`)
	}))
	defer srv.Close()

	eng := &backtest.Engine{}
	reg := contracts.NewEngineRegistry()
	reg.Register(contracts.DefaultKernel, eng)
	r := gin.New()
	registerBacktestRoutes(r, eng, reg, nil, nil, orchestrationworker.NewClient(srv.URL, srv.Client()), nil, zerolog.Nop())

	body := `{"strategy_id":"momentum","universe":"600000.SH","start_date":"2026-01-01","end_date":"2026-03-01","initial_capital":500000}`
	req := httptest.NewRequest(http.MethodPost, "/api/backtest", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["job_id"] != "job-7" || resp["status"] != "pending" {
		t.Fatalf("response = %v", resp)
	}

	if gotBody["idempotency_key"] == "" || gotBody["idempotency_key"] == nil {
		t.Fatalf("idempotency_key missing: %v", gotBody)
	}
	spec, ok := gotBody["run_spec"].(map[string]any)
	if !ok {
		t.Fatalf("run_spec not an object: %v", gotBody["run_spec"])
	}
	if spec["strategy"] != "momentum" || spec["initial_capital"] != float64(500000) {
		t.Fatalf("run_spec = %v", spec)
	}
	if _, exists := spec["stock_pool"]; !exists {
		t.Fatalf("run_spec missing stock_pool: %v", spec)
	}
}
