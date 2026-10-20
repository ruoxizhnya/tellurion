package main

// REV-23(4)(5)(6)(7)(10)：死代码与细节类的测试。
//
//   - (5) backtest 异步 job 端点的请求体上限：>32MiB → 400；
//   - (6) proxy 出站调用的两个统一：请求 ctx 传播（客户端取消即停）、
//     ohlcv 转发的路径段与 query 显式编码；
//   - (4)(7)(10) 的纪律性断言（假超时不得回来、limit 封顶常量在位、
//     死 handler 签名不得复活）用源码守卫——它们的失败注入要么需要真
//     store/jobService，要么本就是「代码形态」约束，守卫是最经济的执法。

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBacktestJobBodySizeCapped 钉住 (5)：超大 body 不再被裸 io.ReadAll
// 全量吞下——MaxBytesReader 在 32MiB 处截断，handler 回 400。
func TestBacktestJobBodySizeCapped(t *testing.T) {
	router, _, _ := newBacktestKernelRouter(t)

	// 33 MiB 的 JSON（合法 shape、超限体积）。
	big := `{"strategy_id":"x","universe":["000001.SZ"],"kernel":"` +
		strings.Repeat("a", 33<<20) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/backtest", bytes.NewReader([]byte(big)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code,
		"超限 body 必须 400（REV-23(5)），不得全量读入内存")
}

// TestProxy_PropagatesClientCancel 钉住 (6a)：入站请求取消后，出站调用
// 的 ctx 必须同时取消（此前 GET/POST 快捷分支不带请求 ctx——客户端断开
// 后出站仍在跑）。
func TestProxy_PropagatesClientCancel(t *testing.T) {
	upstreamCtxDone := make(chan struct{}, 1)
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // upstream 挂住，直到出站 ctx 被取消
		upstreamCtxDone <- struct{}{}
	})
	router, _ := newProxyTestRouter(t, upstream)

	reqCtx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/stocks/count", nil).WithContext(reqCtx)

	done := make(chan struct{})
	go func() {
		router.ServeHTTP(httptest.NewRecorder(), req)
		close(done)
	}()

	// 给出站调用一点时间到达 upstream，然后取消入站请求。
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-upstreamCtxDone:
		// 出站 ctx 感知到了取消 —— ctx 传播成立（REV-23(6)）。
	case <-time.After(2 * time.Second):
		t.Fatal("入站请求取消后出站调用未取消（ctx 未传播，REV-23(6)）")
	}
	_ = done
}

// TestProxyOhlcv_EncodesPathAndQuery 钉住 (6b)：ohlcv 转发的路径段与
// query 显式编码——含空格的 symbol 到 upstream 必须是 %20 而非裸空格。
func TestProxyOhlcv_EncodesPathAndQuery(t *testing.T) {
	var gotPath string
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		w.WriteHeader(http.StatusOK)
	})
	router, _ := newProxyTestRouter(t, upstream)

	// 入站请求的 path 段天然是编码形态（%20）；gin param 解码回 "60 00.SH"，
	// handler 必须 PathEscape 回去再转发。
	req := httptest.NewRequest(http.MethodGet,
		"/api/ohlcv/60%2000.SH?start_date=20240102&end_date=20241231", nil)
	router.ServeHTTP(httptest.NewRecorder(), req)

	assert.NotContains(t, gotPath, " 60 00.SH",
		"路径段不得裸拼（REV-23(6)）")
	assert.Contains(t, gotPath, "60%2000.SH")
	assert.Contains(t, gotPath, "start_date=20240102")
}

// TestREV23_DetailGuards：纪律性源码守卫——(4) 假超时不得回来、(7) 封顶
// 常量在位、(10) 死 handler 签名不得复活。
// （data 侧的 listJobs/screen 封顶与死 handler 守卫在
// cmd/data/handlers_sync_rev23_test.go——各自守各自包的文件。）
func TestREV23_DetailGuards(t *testing.T) {
	t.Parallel()

	t.Run("reportDaily has no fake timeout", func(t *testing.T) {
		t.Parallel()
		src := rev23FuncBody(t, "handlers_compliance.go",
			"func (h *ComplianceHandler) reportDaily(")
		assert.NotContains(t, src, "WithTimeout",
			"reportDaily 不得建 WriteReport 不接收的假超时 ctx（REV-23(4)）；要加超时先让 WriteReport 收 ctx")
	})
}

// rev23FuncBody 从 srcFile 取出从 funcSig 到下一个顶级 func 的函数体。
func rev23FuncBody(t *testing.T, srcFile, funcSig string) string {
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
