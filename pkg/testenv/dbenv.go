package testenv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DatabaseDSNEnv 是全仓 DB 测试唯一的 DSN 环境变量（REV-21）。
//
// 它取代了历史上并存的三种约定：`QUANT_TEST_DSN`（pkg/eventstore、
// pkg/msgbus、pkg/kernel、pkg/expression、pkg/strategy、pkg/backtest/obs01）、
// `TEST_DB_DSN`（cmd/analysis、pkg/auth）与 21 个测试文件里各写一遍的
// 硬编码字面量 `postgres://postgres:postgres@localhost:5432/tellurion`。
//
// 语义（fail-loud 与显式 skip 的分界）：
//   - 未设置：DB 测试跳过，且跳过被登记，包末由 TestMain 汇总打印——
//     「绿」不再等于「验过」，无库机器上的绿是看得见的绿。
//   - 已设置但连不上：**失败**（t.Fatalf）。显式配置的 DSN 不通 = 环境坏，
//     静默跳过正是本契约要消灭的行为。
const DatabaseDSNEnv = "TEST_DATABASE_DSN"

// ErrNoDatabaseDSN 表示未设置 DSN（openPool 的哨兵，调用方用 errors.Is 判别）。
var ErrNoDatabaseDSN = errors.New("TEST_DATABASE_DSN 未设置")

// DatabaseDSN 返回配置的 DSN；未设置时返回空串。
func DatabaseDSN() string { return os.Getenv(DatabaseDSNEnv) }

// openPool 是 OpenPoolForTest 的纯逻辑核心（不碰 *testing.T，便于单测）。
// 空 DSN → ErrNoDatabaseDSN；连不上 → 包装后的连接错误。
func openPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	if dsn == "" {
		return nil, ErrNoDatabaseDSN
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.New: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return pool, nil
}

// ─── 跳过登记与包末汇总 ────────────────────────────────────────────────

var (
	skipMu     sync.Mutex
	skipCounts = map[string]int{} // reason -> 次数
)

func recordSkip(reason string) {
	skipMu.Lock()
	defer skipMu.Unlock()
	skipCounts[reason]++
}

// DBSkips 返回本测试二进制至今的 DB 跳过登记快照（总数 + 按原因计数）。
// 供 RunWithDBSummary 与单测使用。
func DBSkips() (int, map[string]int) {
	skipMu.Lock()
	defer skipMu.Unlock()
	byReason := make(map[string]int, len(skipCounts))
	total := 0
	for reason, n := range skipCounts {
		byReason[reason] = n
		total += n
	}
	return total, byReason
}

// SkipNoDB 登记并跳过当前测试。
//
// ⚠️ 内部调用 t.Skipf（runtime.Goexit）：**调用方不得在其后写任何代码**，
// 调用点必须是 `if dsn == "" { testenv.SkipNoDB(t); return }` 这种形态，
// 且放在获取 DB 资源之前。
func SkipNoDB(t *testing.T) {
	t.Helper()
	recordSkip(DatabaseDSNEnv + " 未设置")
	total, _ := DBSkips()
	t.Skipf("[testdb] 跳过：%s 未设置（本测试二进制累计跳过 %d 个 DB 用例）。"+
		"设 %s 指向真库后重跑，本用例才会真正执行。",
		DatabaseDSNEnv, total, DatabaseDSNEnv)
}

// OpenPoolForTest 为 DB 测试打开一个真库连接池：
//   - 未设置 TEST_DATABASE_DSN → SkipNoDB（登记 + 显式跳过）；
//   - 已设置但连不上 → t.Fatalf（fail-loud）；
//   - 成功 → 注册 t.Cleanup(pool.Close) 并返回。
func OpenPoolForTest(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := openPool(ctx, DatabaseDSN())
	switch {
	case errors.Is(err, ErrNoDatabaseDSN):
		SkipNoDB(t)
		return nil // 不可达：SkipNoDB 已 Goexit
	case err != nil:
		t.Fatalf("[testdb] %s 已设置但连不上（fail-loud：显式配置的 DSN 不通=环境坏，不静默跳过）: %v",
			DatabaseDSNEnv, err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// writeDBSkipSummary 把跳过汇总写进 w（纯函数，便于单测）。
func writeDBSkipSummary(w io.Writer, total int, byReason map[string]int) {
	if total <= 0 {
		return
	}
	fmt.Fprintf(w, "[testdb] %s 未设置：本测试二进制 %d 个 DB 用例被跳过（明细：%v）。"+
		"设 %s 指向真库（本地：postgres://postgres:postgres@localhost:5432/tellurion?sslmode=disable）即可真正执行。\n",
		DatabaseDSNEnv, total, byReason, DatabaseDSNEnv)
}

// RunWithDBSummary 包装 testing.M.Run，供各包 TestMain 调用：
// 跑完套件后，若有 DB 跳过登记则向 stderr 打印汇总。返回 m.Run() 的退出码。
//
// 用法：
//
//	func TestMain(m *testing.M) { os.Exit(testenv.RunWithDBSummary(m)) }
func RunWithDBSummary(m *testing.M) int {
	code := m.Run()
	total, byReason := DBSkips()
	writeDBSkipSummary(os.Stderr, total, byReason)
	return code
}
