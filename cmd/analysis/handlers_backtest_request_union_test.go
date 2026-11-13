package main

// handlers_backtest_request_union_test.go —— CORE-BACKTEST-REQUEST-UNION 的 HTTP 腿。
//
// 覆盖三件本次收口的事：
//  1. 混用 sync / async 字段 ⇒ 400 并点名冲突字段（旧实现静默走异步）；
//  2. 半套异步体（只有 strategy_id）⇒ 400 点名 universe，而不是掉进
//     legacy 分支报 "strategy required"；
//  3. legacy 合法请求仍然走同步分支，响应 body 与显式 mode=sync 时逐字节
//     一致，只在响应头多出 Deprecation / Warning 标注。
// 破坏验证：把 classifyBacktestRequest 退回「strategy_id && universe 同时非空
// 才走异步」的字段探测 ⇒ 用例 1、2 立刻红。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBacktestPost_RejectsMixedEnvelopes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		body     string
		wantSubs []string
	}{
		{
			name:     "legacy strategy plus async strategy_id",
			body:     `{"strategy":"momentum","strategy_id":"s-1","universe":"csi300","start_date":"2024-01-01","end_date":"2024-02-01"}`,
			wantSubs: []string{"ambiguous", "strategy", "strategy_id", "universe"},
		},
		{
			name:     "legacy stock_pool plus async universe",
			body:     `{"stock_pool":["600000.SH"],"universe":"csi300","start_date":"2024-01-01","end_date":"2024-02-01"}`,
			wantSubs: []string{"ambiguous", "stock_pool", "universe"},
		},
		{
			name:     "only strategy_id",
			body:     `{"strategy_id":"s-1","start_date":"2024-01-01","end_date":"2024-02-01"}`,
			wantSubs: []string{"universe", "missing"},
		},
		{
			name:     "mode async without fields",
			body:     `{"mode":"async","start_date":"2024-01-01","end_date":"2024-02-01"}`,
			wantSubs: []string{"strategy_id", "missing"},
		},
		{
			name:     "mode sync with async fields",
			body:     `{"mode":"sync","strategy_id":"s-1","universe":"csi300"}`,
			wantSubs: []string{"mode=sync", "strategy_id"},
		},
		{
			name:     "mode async with legacy fields",
			body:     `{"mode":"async","strategy":"momentum","start_date":"2024-01-01","end_date":"2024-02-01"}`,
			wantSubs: []string{"mode=async", "strategy"},
		},
		{
			name:     "unknown mode",
			body:     `{"mode":"turbo","strategy":"momentum"}`,
			wantSubs: []string{"turbo", "not supported"},
		},
		{
			name:     "body is not an object",
			body:     `["strategy"]`,
			wantSubs: []string{"JSON object"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := postBacktest(t, tc.body)
			require.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
			body := w.Body.String()
			for _, sub := range tc.wantSubs {
				assert.Contains(t, body, sub, "400 文案必须点名事实：%s", tc.name)
			}
		})
	}
}

// TestBacktestPost_LegacyEnvelopeUnchanged 钉住兼容周期内 legacy 信封的
// body 不变：同一份请求带不带 `mode:"sync"`，响应体必须逐字节一致，
// 只有响应头多 Deprecation / Warning。
func TestBacktestPost_LegacyEnvelopeUnchanged(t *testing.T) {
	t.Parallel()

	// 用不可解析日期把请求停在引擎入口之前的 400 分支：不需要真库，
	// 但已经足够证明「走的是同步信封」。
	const legacy = `{"strategy":"momentum","stock_pool":["600000.SH"],"start_date":"2024-13-99","end_date":"2024-01-31"}`
	const explicitSync = `{"mode":"sync","strategy":"momentum","stock_pool":["600000.SH"],"start_date":"2024-13-99","end_date":"2024-01-31"}`

	implicitResp := postBacktest(t, legacy)
	explicitResp := postBacktest(t, explicitSync)

	require.Equal(t, http.StatusBadRequest, implicitResp.Code)
	require.Equal(t, implicitResp.Code, explicitResp.Code)
	assert.Equal(t, implicitResp.Body.String(), explicitResp.Body.String(),
		"legacy 信封的响应体在兼容周期内必须逐字节不变")
	assert.Contains(t, implicitResp.Body.String(), "start_date",
		"legacy 请求仍应走同步分支（错误来自日期解析，而不是 strategy required）")

	for _, w := range []*httptest.ResponseRecorder{implicitResp, explicitResp} {
		assert.Equal(t, "true", w.Header().Get("Deprecation"))
		assert.Contains(t, w.Header().Get("Warning"), "legacy sync envelope is deprecated")
	}
}

// TestBacktestPost_ExplicitAsyncReachesOrchestration 证明显式 mode=async
// 真的进了异步分支（未配编排 → 503，而不是 legacy 的 400 strategy required）。
func TestBacktestPost_ExplicitAsyncReachesOrchestration(t *testing.T) {
	t.Parallel()

	w := postBacktest(t, `{"mode":"async","strategy_id":"s-1","universe":"csi300","start_date":"2024-01-01","end_date":"2024-02-01"}`)
	require.Equal(t, http.StatusServiceUnavailable, w.Code, "body: %s", w.Body.String())
	assert.NotContains(t, strings.ToLower(w.Body.String()), "strategy required")
	assert.Empty(t, w.Header().Get("Deprecation"), "异步分支不应被标成 legacy 信封")
}
