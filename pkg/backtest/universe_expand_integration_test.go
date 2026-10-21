package backtest

// DATA-01 的验收护栏（接真库，无库时 skip —— **跳过不是通过**）。
//
// 钉两件事：
//  1. `universe: csi300` 这类人类写法经 normalizeIndexCode 走 IndexCode
//     展开，能拿到真实票池（不再恒空）；
//  2. 破坏验证（改坏 → 红 → 还原 → 绿）：清空 index_constituents 后，
//     同一条路径**必须 fail-loud**（返回 error 且票池为 nil），不许静默返回
//     空数组 —— 那正是 OBS-01 拦的「0 交易却标成功」的入口形态。
//
// 「还原」由 t.Cleanup 保证：无论断言是否失败都会把成分写回并复验一次，
// 所以这条测试不会给库留下空表。

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"tellurion/pkg/domain"
	"tellurion/pkg/storage"
)

func universeDSN(t *testing.T) string {
	t.Helper()
	// QT-61：这两条用例要真库里有 csi300 成分快照（2024-10-31~2026-09-30）才谈得上判据。
	// 它原先用硬编码 DSN 默认值去连 CI 提供的 postgres service（空库、无迁移），于是
	// require.NoError 因表不存在直接红 —— 而「红」混在噪声里没人看（正是 QT-61 要消灭的）。
	// 改成像其余 DB 测试那样显式 opt-in：默认 SKIP（在 CI 汇总里点名「未覆盖」，非静默忽略），
	// 只有 TEST_UNIVERSE=1 且 DSN 指向已灌成分的库时才真正执行。
	if os.Getenv("TEST_UNIVERSE") != "1" {
		t.Skip("[test-universe] 未覆盖：csi300 universe 展开/破坏验证需真库灌成分快照。" +
			"设 TEST_UNIVERSE=1 并把 DATABASE_URL/DATABASE_* 指向已灌 csi300 的库才执行（见 docs/TASKS.md QT-61）。")
	}
	port := 5432
	if p, err := strconv.Atoi(os.Getenv("DATABASE_PORT")); err == nil && p > 0 {
		port = p
	}
	host := os.Getenv("DATABASE_HOST")
	if host == "" {
		host = "localhost"
	}
	user := os.Getenv("DATABASE_USER")
	if user == "" {
		user = "postgres"
	}
	pass := os.Getenv("DATABASE_PASSWORD")
	if pass == "" {
		pass = "postgres"
	}
	name := os.Getenv("DATABASE_NAME")
	if name == "" {
		name = "tellurion"
	}
	dsn, err := storage.BuildDSN(storage.DatabaseConfig{
		URL: os.Getenv("DATABASE_URL"), Host: host, Port: port,
		User: user, Password: pass, Name: name, SSLMode: "disable",
	})
	if err != nil {
		t.Skipf("跳过 DATA-01 展开护栏：库连接配置不可用（%v）", err)
	}
	return dsn
}

// newProbeEngine 构造只带 store 与 logger 的最小 Engine
// （resolveStockPool 只用到这两个字段）。
func newProbeEngine(t *testing.T, dsn string) (*Engine, *storage.PostgresStore) {
	t.Helper()
	ctx := context.Background()
	store, err := storage.NewPostgresStoreConnected(ctx, dsn)
	if err != nil {
		t.Skipf("跳过 DATA-01 展开护栏：库连不上（%v）", err)
	}
	t.Cleanup(store.Close)

	e := &Engine{logger: zerolog.Nop()}
	e.SetStore(store)
	return e, store
}

// TestResolveStockPool_UniverseCsi300Expands 验收 1：universe:csi300 走 IndexCode 展开成真票池。
func TestResolveStockPool_UniverseCsi300Expands(t *testing.T) {
	dsn := universeDSN(t)
	e, _ := newProbeEngine(t, dsn)
	ctx := context.Background()

	// 起始日落在已灌快照区间内（最早 2024-10-31，最新 2026-09-30）
	start := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)

	pool, err := e.resolveStockPool(ctx, BacktestRequest{
		IndexCode: "csi300", // 探索链路 YAML 的默认写法
	}, start)

	require.NoError(t, err, "有成分数据时不应报错")
	require.NotEmpty(t, pool, "universe:csi300 必须展开出真实票池（DATA-01 ①目标）")
	t.Logf("universe:csi300 → 票池 %d 只（样例 %v…）", len(pool), pool[:minInt(3, len(pool))])

	pool2, err := e.resolveStockPool(ctx, BacktestRequest{IndexCode: "000300.SH"}, start)
	require.NoError(t, err)
	assert.Equal(t, len(pool), len(pool2), "别名写法与库内代码必须展开出同一票池")
}

// TestResolveStockPool_FailsLoudWhenConstituentsEmpty 是破坏验证。
func TestResolveStockPool_FailsLoudWhenConstituentsEmpty(t *testing.T) {
	dsn := universeDSN(t)
	e, store := newProbeEngine(t, dsn)
	ctx := context.Background()
	start := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)

	// 前置：此刻必须有数据，否则「空」分不清是没灌还是被清空
	before, err := e.resolveStockPool(ctx, BacktestRequest{IndexCode: "csi300"}, start)
	require.NoError(t, err)
	require.NotEmpty(t, before, "前置失败：表本来就是空的，这条测试证明不了任何东西")

	// 备份 → Cleanup 里原样写回（还原）
	backup, err := store.GetIndexConstituents(ctx, "000300.SH")
	require.NoError(t, err)
	require.NotEmpty(t, backup)
	t.Cleanup(func() {
		ptrs := make([]*domain.IndexConstituent, len(backup))
		for i := range backup {
			c := backup[i]
			ptrs[i] = &c
		}
		if err := store.SaveIndexConstituentBatch(context.Background(), ptrs); err != nil {
			t.Errorf("还原失败（index_constituents 可能仍是空的）：%v", err)
			return
		}
		// 还原后必须重新展开成功 —— 这就是「还原绿」
		restored, err := e.resolveStockPool(context.Background(), BacktestRequest{IndexCode: "csi300"}, start)
		if err != nil || len(restored) == 0 {
			t.Errorf("还原后未恢复绿：err=%v pool=%d", err, len(restored))
			return
		}
		t.Logf("还原完成：票池恢复为 %d 只", len(restored))
	})

	// 改坏：清空该指数的成分行
	pool2, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool2.Close()
	_, err = pool2.Exec(ctx, `DELETE FROM index_constituents WHERE index_code = '000300.SH'`)
	require.NoError(t, err)

	pool, err := e.resolveStockPool(ctx, BacktestRequest{IndexCode: "csi300"}, start)
	assert.Error(t, err, "成分被清空 ⇒ 必须 fail-loud 报错，不许静默返回空票池")
	assert.Nil(t, pool, "fail-loud 时不返回票池")
	if err != nil {
		t.Logf("fail-loud 报错：%v", err)
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
