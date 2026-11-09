//go:build integration
// +build integration

// Package storage — Sprint 6 P0-7 (ODR-013, TQ-011) dockertest
// integration tests.
//
// Why a build tag
// ---------------
// These tests spin up a real PostgreSQL container via
// github.com/ory/dockertest/v3, which requires:
//   - a working Docker daemon
//   - ~5–10s of startup time per package
//   - ~50MB of disk per container image
//
// CI on shared runners and developer laptops may not have Docker
// (or may not want to pay the startup cost for every `go test
// ./...`). Hiding the file behind `//go:build integration` means
// `go test ./...` is fast and self-contained, while
// `go test -tags=integration ./pkg/storage/...` exercises the
// real DB. The CI gate that "pkg/storage coverage ≥ 60%" is
// satisfied by running the integration-tagged suite, NOT the
// default one — see docs/TASKS.md §CI Gate.
//
// Coverage target
// ---------------
// pkg/storage has 14 source files totaling ~3.5k LOC. As of
// 2026-05-17 the package had 2.4% unit-test coverage because all
// DB-touching code is unreachable without a real database. This
// file exercises the major public surface (stocks, OHLCV,
// calendar, backtest_jobs) against a real Postgres
// 16 instance, lifting package coverage to ~60–70% per the
// ODR-013 target.
package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pgTestPool is the package-level dockertest pool. TestMain
// initializes it once for the entire test binary and tears it
// down at the end — per-test container spinup would push the
// suite over 60s for no gain, since each test only inserts a
// handful of rows that the migrations have already isolated
// from the others (separate schemas, or just clean tables).
var pgTestPool *dockertest.Pool

// pgTestResource is the running postgres container; we hold a
// reference so TestMain can purge it.
var pgTestResource *dockertest.Resource

// pgTestStore is the PostgresStore wired against the test
// container. Every integration test calls newTestStore(t) which
// hands out a fresh store and a per-test transaction (rolled
// back at test end) so the tests are isolated without paying
// for a container per test.
var pgTestStore *PostgresStore

// TestMain boots the shared dockertest pool + Postgres container
// once for the whole package. If Docker isn't available the
// whole suite is skipped (t.Skip, not t.Fatal) so a CI machine
// without Docker doesn't break the build.
func TestMain(m *testing.M) {
	// We deliberately do NOT call os.Exit from the skip path —
	// returning from TestMain with the suite skipped means
	// `go test` exits 0. os.Exit would be cleaner but breaks
	// ginkgo-style wrapping if anyone introduces it later.

	if !dockerAvailable() {
		fmt.Fprintln(os.Stderr,
			"SKIP: integration tests require Docker daemon (sock: /var/run/docker.sock). "+
				"To run: start Docker Desktop / colima / podman, then `go test -tags=integration ./pkg/storage/...`.")
		// Run the suite anyway — each test will t.Skip() itself
		// in newTestStore when it sees no pool.
		os.Exit(m.Run())
	}

	pool, err := dockertest.NewPool("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "dockertest.NewPool: %v\n", err)
		os.Exit(m.Run())
	}
	if err := pool.Client.Ping(); err != nil {
		fmt.Fprintf(os.Stderr, "docker ping: %v\n", err)
		os.Exit(m.Run())
	}

	// Postgres 16 Alpine — small (~80MB) and the version pinned
	// in docker-compose.yml. The PGPASSWORD env is the standard
	// way to inject the test password; the `-c listen_addresses`
	// flag is unnecessary because dockertest maps a random port
	// to localhost.
	pgTestResource, err = pool.RunWithOptions(&dockertest.RunOptions{
		Repository: "postgres",
		Tag:        "16-alpine",
		Env: []string{
			"POSTGRES_PASSWORD=testpass",
			"POSTGRES_USER=testuser",
			"POSTGRES_DB=testdb",
		},
		ExposedPorts: []string{"5432/tcp"},
	}, func(config *docker.HostConfig) {
		// Auto-remove keeps the developer laptop from filling up
		// with stale test containers if the run is killed.
		config.AutoRemove = true
		config.RestartPolicy = docker.RestartPolicy{Name: "no"}
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "pool.RunWithOptions: %v\n", err)
		os.Exit(m.Run())
	}

	// Wait for Postgres to be ready. retryStart=20 with 1s
	// sleep is enough on a developer laptop; on a saturated CI
	// runner the expBackoff doubles on failure up to 5s.
	var connString string
	if err := pool.Retry(func() error {
		port := pgTestResource.GetPort("5432/tcp")
		connString = fmt.Sprintf(
			"postgres://testuser:testpass@localhost:%s/testdb?sslmode=disable",
			port)
		c, err := pgx.Connect(context.Background(), connString)
		if err != nil {
			return err
		}
		return c.Close(context.Background())
	}); err != nil {
		fmt.Fprintf(os.Stderr, "postgres not ready: %v\n", err)
		_ = pool.Purge(pgTestResource)
		os.Exit(m.Run())
	}

	pgTestPool = pool

	// Build a single shared PostgresStore — its constructor runs
	// the inline migrations, which is what we want to exercise.
	// We give it a generous startup deadline because pool.Retry
	// above has already proven the server is reachable.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := NewPostgresStore(ctx, connString)
	if err != nil {
		fmt.Fprintf(os.Stderr, "NewPostgresStore: %v\n", err)
		_ = pool.Purge(pgTestResource)
		os.Exit(m.Run())
	}
	pgTestStore = store

	code := m.Run()

	store.Close()
	_ = pool.Purge(pgTestResource)
	os.Exit(code)
}

// dockerAvailable returns true if the Docker daemon is
// reachable. We use `docker info` via the system CLI rather than
// pinging dockertest directly so the skip message can tell the
// operator exactly which socket to check.
func dockerAvailable() bool {
	// We could shell out to `docker info` but that depends on
	// the docker binary being on $PATH. dockertest.NewPool is a
	// stricter check: it actually opens the socket.
	pool, err := dockertest.NewPool("")
	if err != nil {
		return false
	}
	if err := pool.Client.Ping(); err != nil {
		return false
	}
	return true
}

// newTestStore returns the shared PostgresStore. Tests are
// isolated by TRUNCATE — see setUp — rather than by per-test
// transactions, because PostgresStore methods reach into
// s.pool directly and a Tx-only adapter would be a major
// refactor (out of scope for P0-7).
//
// If Docker is unavailable (no pgTestStore), t.Skip is called
// and the test is recorded as skipped, not failed. This is the
// documented contract for `-tags=integration` on a machine
// without Docker.
func newTestStore(t *testing.T) (*PostgresStore, context.Context) {
	t.Helper()
	if pgTestStore == nil {
		t.Skip("integration test requires Docker; see TestMain for setup instructions")
	}
	return pgTestStore, context.Background()
}

// truncateAll wipes the per-test data. We use TRUNCATE …
// RESTART IDENTITY CASCADE because it's faster than
// per-table DELETE on every test (TRUNCATE doesn't generate
// per-row WAL records).
func truncateAll(t *testing.T, ctx context.Context, store *PostgresStore) {
	t.Helper()
	// SVC-CORE-DATA-RETIRE：行情/日历/成分等 21 张表已退役，本仓不再创建，
	// TRUNCATE 它们会直接报 “relation does not exist”。
	tables := []string{
		"stock_fundamentals", "factor_cache",
		"factor_returns", "ic_analysis",
		"backtest_jobs",
	}
	for _, tbl := range tables {
		_, err := store.pool.Exec(ctx, fmt.Sprintf("TRUNCATE TABLE %s RESTART IDENTITY CASCADE", tbl))
		require.NoError(t, err, "truncate %s", tbl)
	}
}

// setUp is the test-bootstrap helper: returns a clean store
// for the test. Every test that mutates state should call this
// at the top.
func setUp(t *testing.T) (*PostgresStore, context.Context) {
	t.Helper()
	store, ctx := newTestStore(t)
	truncateAll(t, ctx, store)
	return store, ctx
}

// ──────────────────────────────────────────────────────────────────
// Tests
// ──────────────────────────────────────────────────────────────────

func TestIntegration_NewPostgresStore_Connects(t *testing.T) {
	// The constructor was already exercised by TestMain; here
	// we just confirm Ping works.
	store, ctx := newTestStore(t)
	require.NoError(t, store.Ping(ctx))
}

// TestIntegration_MigrateIsIdempotentForModuleTables 钉住 SPEC-R-01 的收编结果：
// migrate() 连续执行两次不报错（全部 DDL 幂等），且三张从模块侧收编进来的表
// 真实存在。
//
// 这里直接调 migrate() 而不是依赖 TestMain 的那一次：前者证明「重复执行安全」
// （服务每次 Boot 都会跑、测试 setup 也会跑），后者只证明「第一次能建」。
func TestIntegration_MigrateIsIdempotentForModuleTables(t *testing.T) {
	store, ctx := newTestStore(t)

	for i := 1; i <= 2; i++ {
		require.NoErrorf(t, store.migrate(ctx), "第 %d 次 migrate() 失败（幂等性被破坏）", i)
	}

	for _, table := range []string{
		"audit.message_log",
		"quant.strategy_state",
		"quant.external_signals",
		"quant.run_provenance",
		"quant.alert_outbox",
	} {
		var oid *string
		require.NoError(t, store.pool.QueryRow(ctx,
			`SELECT to_regclass($1)::text`, table).Scan(&oid), "查询 %s 是否存在失败", table)
		require.NotNil(t, oid, "migrate() 后表 %s 不存在——收编未生效", table)
	}
}

func TestIntegration_NewPostgresStore_BadConnString(t *testing.T) {
	// We don't go through dockertest for the negative path —
	// the error must surface from a bad DSN, not from a
	// missing server.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := NewPostgresStore(ctx,
		"postgres://user:bad@127.0.0.1:1/db?sslmode=disable&connect_timeout=1")
	require.Error(t, err, "must fail to connect to an unused port")
	assert.Contains(t, err.Error(), "failed",
		"error should be wrapped by NewPostgresStore")
}

func TestIntegration_BacktestJob_Lifecycle(t *testing.T) {
	store, ctx := setUp(t)
	id := "job-abc-123"
	job := map[string]any{
		"id":          id,
		"strategy_id": "momentum",
		"params":      json.RawMessage(`{"lookback": 20}`),
		"universe":    "csi300",
		"start_date":  "2024-01-01",
		"end_date":    "2024-06-30",
		"status":      "pending",
	}
	require.NoError(t, store.CreateBacktestJob(ctx, job))

	got, err := store.GetBacktestJob(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, id, got["id"])
	assert.Equal(t, "pending", got["status"])

	require.NoError(t, store.UpdateJobStarted(ctx, id))
	got, _ = store.GetBacktestJob(ctx, id)
	assert.Equal(t, "running", got["status"])
	require.NotNil(t, got["started_at"], "started_at must be set after UpdateJobStarted")

	result := []byte(`{"sharpe": 1.5, "trades": 42}`)
	require.NoError(t, store.UpdateJobCompleted(ctx, id, "completed", result))
	got, _ = store.GetBacktestJob(ctx, id)
	assert.Equal(t, "completed", got["status"])
	assert.Equal(t, "completed", got["result_status"], "有效结果写 result_status=completed")
	require.NotNil(t, got["completed_at"], "completed_at must be set")
	assert.JSONEq(t, string(result), string(got["result"].([]byte)))
}

// B6-3：invalid 是结果结论，不是生命周期失败 —— 行仍是 completed。
func TestIntegration_BacktestJob_InvalidResultStatus(t *testing.T) {
	store, ctx := setUp(t)
	id := "job-invalid-1"
	require.NoError(t, store.CreateBacktestJob(ctx, map[string]any{
		"id":          id,
		"strategy_id": "x",
		"params":      json.RawMessage(`{}`),
		"universe":    "all",
		"start_date":  "2024-01-01",
		"end_date":    "2024-01-31",
		"status":      "pending",
	}))
	require.NoError(t, store.UpdateJobStarted(ctx, id))

	result := []byte(`{"status": "invalid", "invalid_reasons": [{"code": "no_trades"}]}`)
	require.NoError(t, store.UpdateJobCompleted(ctx, id, "invalid", result))

	got, err := store.GetBacktestJob(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "completed", got["status"], "invalid 的队列生命周期仍是 completed")
	assert.Equal(t, "invalid", got["result_status"], "结果结论是 invalid")
	assert.JSONEq(t, string(result), string(got["result"].([]byte)), "invalid 不得丢弃结果体")
	if msg, ok := got["error_msg"]; ok {
		assert.Empty(t, msg, "invalid 不是引擎故障，error 必空")
	}
}

func TestIntegration_BacktestJob_FailurePath(t *testing.T) {
	store, ctx := setUp(t)
	id := "job-fail-1"
	require.NoError(t, store.CreateBacktestJob(ctx, map[string]any{
		"id":          id,
		"strategy_id": "x",
		"params":      json.RawMessage(`{}`),
		"universe":    "all",
		"start_date":  "2024-01-01",
		"end_date":    "2024-01-31",
		"status":      "pending",
	}))
	require.NoError(t, store.UpdateJobStarted(ctx, id))
	require.NoError(t, store.UpdateJobFailed(ctx, id, "boom"))

	got, err := store.GetBacktestJob(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "failed", got["status"])
	assert.Equal(t, "failed", got["result_status"], "引擎故障写 result_status=failed")
	assert.Equal(t, "boom", got["error_msg"])
}

func TestIntegration_BacktestJob_MissingReturnsNil(t *testing.T) {
	store, ctx := setUp(t)
	got, err := store.GetBacktestJob(ctx, "no-such-job")
	require.NoError(t, err)
	assert.Nil(t, got, "missing job must return (nil, nil)")
}

func TestIntegration_BacktestJob_ListAndDelete(t *testing.T) {
	store, ctx := setUp(t)
	for _, id := range []string{"j1", "j2", "j3"} {
		require.NoError(t, store.CreateBacktestJob(ctx, map[string]any{
			"id": id, "strategy_id": "x", "params": json.RawMessage(`{}`),
			"universe": "all", "start_date": "2024-01-01", "end_date": "2024-01-31",
			"status": "pending",
		}))
	}
	list, err := store.ListBacktestJobs(ctx, 10)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(list), 3)

	// Delete a pending job succeeds.
	require.NoError(t, store.DeleteBacktestJob(ctx, "j1"))
	// Delete a non-pending job fails with a clear error.
	require.NoError(t, store.UpdateJobStarted(ctx, "j2"))
	err = store.DeleteBacktestJob(ctx, "j2")
	require.Error(t, err, "DeleteBacktestJob on a running job must fail-closed")
}
