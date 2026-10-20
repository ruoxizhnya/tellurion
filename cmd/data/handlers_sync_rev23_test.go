package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestREV23_RateLimiterCleanupExpired 钉住 (3)：过期 visitor 被删、
// 未过期保留——visitors map 不再只增不消（对齐 bootstrap 同款实现的
// cleanup 行为）。
func TestREV23_RateLimiterCleanupExpired(t *testing.T) {
	t.Parallel()
	rl := newRateLimiter(5, time.Minute)

	rl.mu.Lock()
	rl.visitors["1.2.3.4"] = &visitorInfo{count: 1, resetAt: time.Now().Add(time.Minute)}
	rl.visitors["5.6.7.8"] = &visitorInfo{count: 1, resetAt: time.Now().Add(-time.Second)}
	rl.mu.Unlock()

	rl.cleanupExpired(time.Now())

	rl.mu.Lock()
	defer rl.mu.Unlock()
	_, live := rl.visitors["1.2.3.4"]
	_, gone := rl.visitors["5.6.7.8"]
	assert.True(t, live, "未过期的 visitor 必须保留")
	assert.False(t, gone, "resetAt 已过的 visitor 必须被清掉（REV-23(3)）")
}

// REV-23(1)：data 侧 5xx/SSE 出口的源码守卫。
//
// sseProgressHandler 与 syncCalendarHandler 的失败注入需要真 DB/Tushare
// 依赖（SyncHandler 的 jobService / TushareClient 都是具体类型），行为
// 测试的成本高于守卫收益——参照 pkg/backtest 的 rev22 守卫先例，直接
// 钉住「错误原文不进响应/SSE 流」这个修复本体：
//
//   - SSE 事件体不得出现 err.Error()（GetJob 的失败可能裹 DB/SQL 细节）；
//   - calendar handler 的 500 腿不得用 Failf 把 err 拼进文案（Failf 是
//     4xx 面向用户的格式化出口，5xx 的 cause 只进日志，见
//     pkg/httpserver/errors.go 的 FailCause）。
//
// 破坏验证：把 sseProgressHandler 的 SSEvent 改回 err.Error() 原文回传
// → 本守卫红。
func TestREV23_NoInternalErrorLeakInSSEAndCalendar(t *testing.T) {
	t.Parallel()

	t.Run("sseProgressHandler does not echo err.Error into the stream", func(t *testing.T) {
		t.Parallel()
		src := functionBodyFor(t, "sync_handlers.go", "func (h *SyncHandler) sseProgressHandler(")
		assert.NotContains(t, src, `gin.H{"error": err.Error()}`,
			"SSE 错误事件不得回传内部错误原文（REV-23(1)），固定文案 + 日志侧 cause")
	})

	t.Run("syncCalendarHandler 500 does not format err into the message", func(t *testing.T) {
		t.Parallel()
		src := functionBodyFor(t, "handlers_sync.go", "func syncCalendarHandler(")
		assert.NotContains(t, src, "Failf(c, http.StatusInternalServerError",
			"calendar 500 腿不得用 Failf 把 Tushare 错误拼进响应文案（REV-23(1)），用 FailCause")
	})
}

// TestREV23_DetailGuards：纪律性源码守卫——(7) 封顶常量在位、(10) 死
// handler 签名不得复活（analysis 侧 reportDaily 的守卫在
// cmd/analysis/handlers_rev23_details_test.go）。
func TestREV23_DetailGuards(t *testing.T) {
	t.Parallel()

	t.Run("list jobs limit is capped", func(t *testing.T) {
		t.Parallel()
		src := functionBodyFor(t, "sync_handlers.go",
			"func (h *SyncHandler) listJobsHandler(")
		assert.Contains(t, src, "maxListJobsLimit",
			"list jobs 的 limit 必须封顶（REV-23(7)），limit 直接进 SQL LIMIT")
	})

	t.Run("fundamentals screen limit is capped", func(t *testing.T) {
		t.Parallel()
		src := functionBodyFor(t, "handlers_stocks.go",
			"func screenStocksHandler(")
		assert.Contains(t, src, "maxScreenLimit",
			"fundamentals 筛选的 limit 必须封顶（REV-23(7)）")
	})

	t.Run("dead package-level sync handlers stay deleted", func(t *testing.T) {
		t.Parallel()
		raw, err := os.ReadFile("handlers_sync.go")
		require.NoError(t, err)
		src := string(raw)
		for _, sig := range []string{
			"func syncStocksHandler(",
			"func syncAllOHLCVHandler(",
			"func syncFundamentalsHandler(tc *data.TushareClient, store *storage.PostgresStore) gin.HandlerFunc {",
		} {
			assert.NotContains(t, src, sig,
				"包级死 handler 不得复活（REV-23(10)）——路由用的是 SyncHandler 方法版")
		}
	})
}

// functionBodyFor 从 srcFile 的源码里取出从 funcSig 开始、到下一个顶级
// func 为止的函数体（rev22 守卫同款取法）。
func functionBodyFor(t *testing.T, srcFile, funcSig string) string {
	t.Helper()
	raw, err := os.ReadFile(srcFile)
	require.NoError(t, err)

	src := string(raw)
	start := strings.Index(src, funcSig)
	require.GreaterOrEqual(t, start, 0, "function %q not found in %s", funcSig, srcFile)

	rest := src[start:]
	if end := strings.Index(rest[1:], "\nfunc "); end >= 0 {
		rest = rest[:end+1]
	}
	return rest
}
