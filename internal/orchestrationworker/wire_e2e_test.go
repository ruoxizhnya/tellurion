package orchestrationworker_test

// 跨仓 wire 验收：core 的 worker 客户端 ↔ 真 orchestration 进程 ↔ 真 PG。
//
// 它证明的是「协议兼容 + 父汇总真实写回」，不是引擎正确性——执行器是替身
// （真引擎要 data 服务与真实行情，属另一层）。与 analytics 的 sensitivity E2E
// 互补：那边证明本仓侧消费，这边证明 core 侧生产。
//
// 前置（缺一即 skip，不是通过）：
//   - ORCHESTRATION_TEST_DSN：orchestration 的测试库（进程启动即迁移）；
//   - ORCHESTRATION_REPO：orchestration 仓路径（默认 ../tellurion-orchestration）。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-contracts/engine"
	"tellurion/internal/orchestrationworker"
)

func TestWireWithRealOrchestrationProcess(t *testing.T) {
	dsn := os.Getenv("ORCHESTRATION_TEST_DSN")
	if dsn == "" {
		t.Skip("ORCHESTRATION_TEST_DSN is not set; skipping real orchestration wire test")
	}
	repo := os.Getenv("ORCHESTRATION_REPO")
	if repo == "" {
		repo = filepath.Join("..", "..", "..", "tellurion-orchestration")
	}
	if _, err := os.Stat(filepath.Join(repo, "cmd", "orchestrationservice")); err != nil {
		t.Skipf("orchestration repo not found at %s: %v", repo, err)
	}

	base := startOrchestration(t, repo, dsn)
	parentID := submitParent(t, base)

	client := orchestrationworker.NewClient(base, &http.Client{Timeout: 10 * time.Second})
	if _, err := client.Register(context.Background(), "core-worker-e2e", []string{"go"}); err != nil {
		t.Fatalf("register: %v", err)
	}

	executor := &orchestrationworker.Executor{
		RunnerFor: func(string) (contracts.EngineRunner, error) { return echoRunner{}, nil },
		// Sink 为 nil：本用例只验协议与父汇总；结果落库由单元测试与
		// 生产装配（JobStoreSink）负责。
	}
	loop := orchestrationworker.NewLoop(client, &orchestrationworker.AnchorResolver{}, executor,
		"core-worker-e2e", []string{"go"},
		orchestrationworker.Options{HeartbeatEvery: 50 * time.Millisecond, PollEvery: 10 * time.Millisecond},
		zerolog.Nop())

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ran := 0
	for {
		worked, err := loop.Step(ctx)
		if err != nil {
			t.Fatalf("step: %v", err)
		}
		if !worked {
			break
		}
		ran++
		if ran > 10 {
			t.Fatal("worker kept claiming without draining the parent")
		}
	}
	if ran != 2 {
		t.Fatalf("worker ran %d children, want 2", ran)
	}

	page := getParent(t, base, parentID)
	if page.Status != "completed" || page.Counts.Completed != 2 {
		t.Fatalf("parent = %+v", page)
	}
	if len(page.Summary.Tasks) != 2 || page.Summary.Tasks[1].ResultStatus != "completed" {
		t.Fatalf("summary = %+v", page.Summary)
	}
}

type echoRunner struct{}

func (echoRunner) RunBacktest(_ context.Context, req contracts.BacktestRequest) (*contracts.BacktestResponse, error) {
	return &contracts.BacktestResponse{ID: req.RunID, Status: contracts.StatusCompleted, Strategy: req.Strategy}, nil
}

type parentPage struct {
	ParentJobID string `json:"parent_job_id"`
	Status      string `json:"status"`
	Counts      struct {
		Completed int `json:"completed"`
		Invalid   int `json:"invalid"`
		Failed    int `json:"failed"`
		Pending   int `json:"pending"`
		Settled   int `json:"settled"`
	} `json:"counts"`
	Summary struct {
		Tasks []struct {
			RunID        string `json:"run_id"`
			ResultStatus string `json:"result_status"`
		} `json:"Tasks"`
	} `json:"summary"`
}

func submitParent(t *testing.T, base string) string {
	t.Helper()
	body := fmt.Sprintf(`{
		"idempotency_key":"core-worker-e2e-%d",
		"kind":"batch",
		"batch":{"base_run_spec":{"strategy":"momentum","stock_pool":["600000.SH"],
			"start_date":"2024-01-02","end_date":"2024-12-31","kernel":"go"},
			"grid":{"lookback":[10,20]}}}`,
		time.Now().UnixNano())
	resp, err := http.Post(base+"/api/runs/parents", "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("submit parent: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("submit parent = %d %s", resp.StatusCode, raw)
	}
	var out struct {
		ParentJobID string `json:"parent_job_id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.ParentJobID == "" {
		t.Fatalf("decode parent: %v (%s)", err, raw)
	}
	return out.ParentJobID
}

func getParent(t *testing.T, base, id string) parentPage {
	t.Helper()
	resp, err := http.Get(base + "/api/runs/parents/" + id)
	if err != nil {
		t.Fatalf("get parent: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get parent = %d %s", resp.StatusCode, raw)
	}
	var out parentPage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode parent: %v (%s)", err, raw)
	}
	return out
}

func startOrchestration(t *testing.T, repo, dsn string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "orchestrationservice")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, "./cmd/orchestrationservice")
	build.Dir = repo
	var out bytes.Buffer
	build.Stdout, build.Stderr = &out, &out
	if err := build.Run(); err != nil {
		t.Fatalf("build orchestration: %v\n%s", err, out.String())
	}

	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, bin, "-dsn", dsn, "-addr", addr, "-reap-interval", "0")
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start orchestration: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("orchestration logs:\n%s", logs.String())
		}
	})

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return "http://" + addr
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("orchestration did not become healthy on %s:\n%s", addr, logs.String())
	return ""
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}
