package orchestrationworker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 任务面（SVC-ORCH-G3 的 core 客户端腿）：提交 / 列表 / 取消。
// 三处都带 service token，且 run_spec 原样透传。

func newControlClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &Client{BaseURL: srv.URL, HTTP: srv.Client(), Token: "svc-token"}
}

func TestSubmitSendsRawRunSpecAndDecodesReplay(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	var gotBody map[string]json.RawMessage
	client := newControlClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"job_id":"run-1","status":"pending","result_status":null}`))
	})

	out, err := client.Submit(context.Background(), "idem-1", 7, []byte(`{"strategy":"momentum","stock_pool":["600000.SH"]}`))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/runs/jobs" {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}
	if gotAuth != "Bearer svc-token" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if string(gotBody["idempotency_key"]) != `"idem-1"` {
		t.Fatalf("idempotency_key = %s", gotBody["idempotency_key"])
	}
	if string(gotBody["priority"]) != "7" {
		t.Fatalf("priority = %s", gotBody["priority"])
	}
	if string(gotBody["run_spec"]) != `{"strategy":"momentum","stock_pool":["600000.SH"]}` {
		t.Fatalf("run_spec not passed through: %s", gotBody["run_spec"])
	}
	if out.JobID != "run-1" || out.Status != "pending" || out.IdempotentReplay {
		t.Fatalf("result = %+v", out)
	}
	if out.ResultStatus != nil {
		t.Fatalf("result_status = %v, want nil", *out.ResultStatus)
	}
}

func TestSubmitReplayFlag(t *testing.T) {
	client := newControlClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"job_id":"run-1","status":"pending","result_status":null,"idempotent_replay":true}`))
	})
	out, err := client.Submit(context.Background(), "idem-1", 0, []byte(`{}`))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !out.IdempotentReplay {
		t.Fatalf("idempotent_replay = false, want true")
	}
}

func TestSubmitRejectsEmptyRunSpecBeforeRequest(t *testing.T) {
	called := false
	client := newControlClient(t, func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	if _, err := client.Submit(context.Background(), "idem-1", 0, nil); err == nil {
		t.Fatal("Submit(nil) = nil error, want error")
	}
	if called {
		t.Fatal("Submit(nil) reached the server")
	}
}

func TestListEncodesFilters(t *testing.T) {
	var gotQuery string
	client := newControlClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jobs":[{"job_id":"run-1","status":"completed","result_status":"completed","result_uri":"backtest_results:run-1","run_spec_hash":"abc"}],"total":1}`))
	})
	page, err := client.List(context.Background(), "completed", "completed", 25)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !strings.Contains(gotQuery, "status=completed") ||
		!strings.Contains(gotQuery, "result_status=completed") ||
		!strings.Contains(gotQuery, "limit=25") {
		t.Fatalf("query = %q", gotQuery)
	}
	if page.Total != 1 || len(page.Jobs) != 1 || page.Jobs[0].ResultURI != "backtest_results:run-1" {
		t.Fatalf("page = %+v", page)
	}
}

func TestListOmitsEmptyFilters(t *testing.T) {
	var gotQuery string
	client := newControlClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jobs":[],"total":0}`))
	})
	if _, err := client.List(context.Background(), "", "", 0); err != nil {
		t.Fatalf("List: %v", err)
	}
	if gotQuery != "" {
		t.Fatalf("query = %q, want empty", gotQuery)
	}
}

func TestCancelUsesDeleteAndDecodesStatus(t *testing.T) {
	var gotMethod, gotPath string
	client := newControlClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"job_id":"run-1","status":"cancelled","result_status":null}`))
	})
	out, err := client.Cancel(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/api/runs/jobs/run-1" {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}
	if out.Status != "cancelled" {
		t.Fatalf("status = %q", out.Status)
	}
}

func TestCancelMapsJobNotFound(t *testing.T) {
	client := newControlClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"job not found","code":"job_not_found"}`))
	})
	if _, err := client.Cancel(context.Background(), "missing"); err == nil {
		t.Fatal("Cancel(missing) = nil error")
	}
}
