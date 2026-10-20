package testenv

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestDatabaseDSN_EnvContract：环境变量是唯一 DSN 入口，未设置为空串。
func TestDatabaseDSN_EnvContract(t *testing.T) {
	t.Setenv(DatabaseDSNEnv, "postgres://u:p@h:5432/db?sslmode=disable")
	if got := DatabaseDSN(); got != "postgres://u:p@h:5432/db?sslmode=disable" {
		t.Fatalf("DatabaseDSN() = %q; want the env value", got)
	}
	t.Setenv(DatabaseDSNEnv, "")
	if got := DatabaseDSN(); got != "" {
		t.Fatalf("DatabaseDSN() with empty env = %q; want \"\"", got)
	}
}

// TestOpenPool_NoDSN_ReturnsSentinel：空 DSN 必须走哨兵错误（调用方据此
// skip 而非 fail）——这是「未设置 → 跳过」与「设置但连不上 → 失败」的
// 分界点，两侧不可混淆。
func TestOpenPool_NoDSN_ReturnsSentinel(t *testing.T) {
	_, err := openPool(context.Background(), "")
	if !errors.Is(err, ErrNoDatabaseDSN) {
		t.Fatalf("openPool(\"\") error = %v; want ErrNoDatabaseDSN", err)
	}
}

// TestOpenPool_Unreachable_ErrorsLoudly：DSN 已设置但端口不存在时，
// openPool 返回连接错误（非哨兵）——OpenPoolForTest 据此 fail-loud。
func TestOpenPool_Unreachable_ErrorsLoudly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := openPool(ctx, "postgres://postgres:postgres@127.0.0.1:1/nope?sslmode=disable&connect_timeout=1")
	if err == nil {
		t.Fatal("openPool against a closed port = nil error; want a connection error")
	}
	if errors.Is(err, ErrNoDatabaseDSN) {
		t.Fatalf("openPool error %v misclassified as ErrNoDatabaseDSN", err)
	}
}

// TestOpenPool_Reachable_Pings：正腿——TEST_DATABASE_DSN 指向真库时
// openPool 真的连通（本机没设 DSN 就跳过，不伪装绿色）。
func TestOpenPool_Reachable_Pings(t *testing.T) {
	dsn := DatabaseDSN()
	if dsn == "" {
		t.Skipf("%s 未设置，跳过正腿（设后可真跑）", DatabaseDSNEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := openPool(ctx, dsn)
	if err != nil {
		t.Fatalf("openPool against %s: %v", DatabaseDSNEnv, err)
	}
	defer pool.Close()
	var one int
	if err := pool.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("SELECT 1 through the pool: got %d, err %v; want 1, nil", one, err)
	}
}

// TestSkipNoDB_RecordsAndSkips：SkipNoDB 必须（a）真的跳过（子测试报
// SKIP）且（b）进登记册（包末汇总有数可汇）。
func TestSkipNoDB_RecordsAndSkips(t *testing.T) {
	before, _ := DBSkips()
	t.Run("skip-leg", func(t *testing.T) {
		SkipNoDB(t)
		t.Error("unreachable: SkipNoDB 之后不该有代码执行")
	})
	after, byReason := DBSkips()
	if after != before+1 {
		t.Fatalf("DBSkips total = %d; want %d (SkipNoDB 应登记一次)", after, before+1)
	}
	want := DatabaseDSNEnv + " 未设置"
	if byReason[want] == 0 {
		t.Fatalf("DBSkips reasons = %v; want a %q entry", byReason, want)
	}
}

// TestWriteDBSkipSummary：汇总打印纯函数——有跳过才打印，零跳过零输出。
func TestWriteDBSkipSummary(t *testing.T) {
	var buf bytes.Buffer
	writeDBSkipSummary(&buf, 3, map[string]int{DatabaseDSNEnv + " 未设置": 3})
	out := buf.String()
	if !strings.Contains(out, "3 个 DB 用例") || !strings.Contains(out, DatabaseDSNEnv) {
		t.Fatalf("summary = %q; want count and env name", out)
	}

	buf.Reset()
	writeDBSkipSummary(&buf, 0, nil)
	if buf.Len() != 0 {
		t.Fatalf("summary with zero skips = %q; want empty", buf.String())
	}
}

// TestOpenPoolForTest_Subprocess：OpenPoolForTest 包装层的两条腿用子进程
// 实证（t.Skipf/t.Fatalf 会终止当前 goroutine，进程内断言不到）：
//   - 未设置 DSN → 子进程 exit 0（skip）且输出带显式跳过文案；
//   - DSN 指向Closed端口 → 子进程 exit 非 0（fail-loud）且输出点名。
func TestOpenPoolForTest_Subprocess(t *testing.T) {
	if os.Getenv("TESTUTIL_DBENV_CRASHER") == "1" {
		OpenPoolForTest(t)
		t.Error("unreachable: OpenPoolForTest 之后不该有代码执行")
		return
	}
	run := func(name string, extraEnv []string, dropDSN bool) (int, string) {
		env := os.Environ()
		if dropDSN {
			filtered := env[:0]
			for _, kv := range env {
				if !strings.HasPrefix(kv, DatabaseDSNEnv+"=") {
					filtered = append(filtered, kv)
				}
			}
			env = filtered
		}
		env = append(env, "TESTUTIL_DBENV_CRASHER=1")
		env = append(env, extraEnv...)
		cmd := exec.Command(os.Args[0], "-test.run=^TestOpenPoolForTest_Subprocess$", "-test.v")
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("%s: subprocess failed to run: %v", name, err)
		}
		return code, string(out)
	}

	t.Run("no DSN skips loudly-visible", func(t *testing.T) {
		code, out := run("no-dsn", nil, true)
		if code != 0 {
			t.Fatalf("no-DSN subprocess exit = %d; want 0 (skip). output:\n%s", code, out)
		}
		if !strings.Contains(out, "TEST_DATABASE_DSN 未设置") {
			t.Fatalf("no-DSN subprocess output missing explicit skip reason:\n%s", out)
		}
	})

	t.Run("set-but-unreachable fails loud", func(t *testing.T) {
		code, out := run("bad-dsn",
			[]string{DatabaseDSNEnv + "=postgres://postgres:postgres@127.0.0.1:1/nope?sslmode=disable&connect_timeout=1"},
			false)
		if code == 0 {
			t.Fatalf("bad-DSN subprocess exit = 0; want non-zero (fail-loud). output:\n%s", out)
		}
		if !strings.Contains(out, "fail-loud") {
			t.Fatalf("bad-DSN subprocess output missing fail-loud marker:\n%s", out)
		}
	})
}
