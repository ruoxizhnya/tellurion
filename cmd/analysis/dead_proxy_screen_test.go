package main

// CORE-DEAD-PROXY-SCREEN-VERIFY：`8c5272d` 把 `/api/screen` 从「反代 data 的
// `/screen`（该路由从未存在）」改成 core 本地供数之后，用**装配后的路由**打一次
// 真实 HTTP 请求，证明：
//
//  1. 请求不再打到已退役的上游：假上游记录到的路径里没有 `/screen`，且计数为 0；
//  2. 失败时是明确的 fail-closed（未接库 ⇒ 503 + 可读原因），而不是把上游 404
//     透传成「看起来正常的失败」，或把缺数据折成空结果；
//  3. 同批的 `/api/stocks/count`（派生自 `/stocks`）与 `/api/ingest/raw`
//     （410 机器码）也走同一条判据。
//
// 与 handlers_screen_test.go 的分工：那边测 handler 契约（两种请求体拼法、
// 响应形状），这里测**装配**——`registerScreenRoutes` 与 `registerProxyRoutes`
// 挂在同一个 router 上（main.go 的顺序）时，上游计数仍为 0。少了这条，
// 两条路由各自单测都绿，但谁把反代加回去也没人发现。
//
// 修复前的实测（27e0c15，同一假上游）：
//
//	POST /api/screen      => 404 {"error":{"code":"not_found","message":"no route"}}
//	GET  /api/stocks/count=> 404 {"error":{"code":"not_found","message":"no route"}}
//	POST /api/ingest/raw  => 404 404 page not found
//	upstream=[POST /screen GET /stocks/count]

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/domain"
)

// deadProxyProbeRouter 按 main.go 的顺序装配两个路由组，并起一个「除
// `/stocks` 外一律 404」的假 data 服务（模拟已退役的兼容面）。
func deadProxyProbeRouter(t *testing.T, reader screenFundamentalsReader) (*gin.Engine, *[]string) {
	t.Helper()

	paths := &[]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*paths = append(*paths, r.Method+" "+r.URL.Path)
		if r.URL.Path == "/stocks" {
			jsonUpstream(http.StatusOK,
				`{"stocks":[{"status":"active"},{"status":""}],"latest_date":"2024-06-28"}`)(w, r)
			return
		}
		jsonUpstream(http.StatusNotFound, `{"error":{"code":"not_found","message":"no route"}}`)(w, r)
	}))
	t.Cleanup(server.Close)

	v := viper.New()
	v.Set("data_service.url", server.URL)

	router := gin.New()
	registerProxyRoutes(router, server.Client(), v, zerolog.Nop())
	registerScreenRoutes(router, reader, zerolog.Nop())
	return router, paths
}

// TestScreen_LocalSupplyNeverTouchesRetiredUpstream 是本单的主腿：
// 返回 200 且结果来自注入的 reader，上游一个请求都没收到。
func TestScreen_LocalSupplyNeverTouchesRetiredUpstream(t *testing.T) {
	peMax := 18.0
	reader := &stubScreenReader{rows: []domain.ScreenResult{{TsCode: "600519.SH", PE: &peMax}}}
	router, upstream := deadProxyProbeRouter(t, reader)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/screen", strings.NewReader(`{"pe_max":20}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	t.Logf("after: POST /api/screen => %d %s; upstream=%v", w.Code, strings.TrimSpace(w.Body.String()), *upstream)

	var got struct {
		Count   int                   `json:"count"`
		Results []domain.ScreenResult `json:"results"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, 1, got.Count)
	require.Len(t, got.Results, 1)
	assert.Equal(t, "600519.SH", got.Results[0].TsCode)
	assert.Empty(t, *upstream, "选股必须本地供数：上游 /screen 已退役，收到任何请求都是复发")
}

// TestScreen_WithoutDataLayerFailsClosed 钉住失败语义：未接库是 503 + 明确
// 原因，不是一个「count=0」的成功响应，也不是上游 404 透传。
func TestScreen_WithoutDataLayerFailsClosed(t *testing.T) {
	router, upstream := deadProxyProbeRouter(t, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/screen", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "screen data layer unavailable")
	assert.NotContains(t, w.Body.String(), `"count"`, "失败不得伪装成空结果")
	assert.Empty(t, *upstream)
}

// TestComposedRouter_RetiredUpstreamPathsAreNeverDialed 覆盖同批另外两条：
// count 只读 `/stocks`，ingest/raw 根本不向上游发请求。
func TestComposedRouter_RetiredUpstreamPathsAreNeverDialed(t *testing.T) {
	router, upstream := deadProxyProbeRouter(t, &stubScreenReader{})

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/stocks/count", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"count":2`)
	require.Equal(t, []string{"GET /stocks"}, *upstream,
		"count 必须派生自 /stocks；出现 /stocks/count 就是打回了已退役路由")

	w = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/ingest/raw", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusGone, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"code":"ingest_raw_retired"`)
	assert.Equal(t, []string{"GET /stocks"}, *upstream, "退役的 ingest/raw 不得向任何上游发请求")
}
