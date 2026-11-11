package main

// handlers_proxy_test.go — TASKS.md C8 / CORE-FACTOR-DEAD-PROXY
//
// GET /api/factors/:factor_name 曾把 SPA 的请求转发到 data 的 `/factors/:name`；
// 上游路由已随数据面退役（裁-14），继续转发只会把「能力已退役」伪装成上游
// 404。现在这条路由显式返回 410 `factor_http_surface_retired`，且**不得**再
// 向上游发请求（ADR-037 §5 / ADR-041）。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newProxyTestRouter wires registerProxyRoutes against a fake L0 data service
// and returns the router plus every request the upstream received.
func newProxyTestRouter(t *testing.T, upstream http.HandlerFunc) (*gin.Engine, *[]*http.Request) {
	t.Helper()

	received := &[]*http.Request{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*received = append(*received, r)
		upstream(w, r)
	}))
	t.Cleanup(server.Close)

	v := viper.New()
	v.Set("data_service.url", server.URL)

	router := gin.New()
	registerProxyRoutes(router, server.Client(), v, zerolog.Nop())
	return router, received
}

func jsonUpstream(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

// TestFactorCitationProxy_Retired_Returns410WithoutUpstream 是 C8 的主腿：
// 路径仍被注册（openapi 与真实路由一一对应），但请求一律 410 + 机器码，
// 且上游收到 **0** 次请求——证明这里没有悄悄恢复反代。
func TestFactorCitationProxy_Retired_Returns410WithoutUpstream(t *testing.T) {
	router, received := newProxyTestRouter(t, jsonUpstream(http.StatusOK, `{"unexpected":true}`))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/factors/momentum?symbol=000001.SZ&date=20260105", nil)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusGone, w.Code, "body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), `"code":"factor_http_surface_retired"`)
	assert.Contains(t, w.Body.String(), "ADR-041")
	assert.Empty(t, *received, "退役路由不得向上游发请求")
}

// TestFactorCitationProxy_Retired_IgnoresSelectors 钉住「不再解析/转发 query」：
// 带不带 symbol/date 都一样是退役响应，上游仍为 0 次。
func TestFactorCitationProxy_Retired_IgnoresSelectors(t *testing.T) {
	router, received := newProxyTestRouter(t, jsonUpstream(http.StatusOK, `{"unexpected":true}`))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/factors/momentum", nil)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusGone, w.Code, "body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), `"code":"factor_http_surface_retired"`)
	assert.Empty(t, *received)
}

// TestStockCount_DerivedFromStocksReadFace 钉住 2026-11-12 裁的派生口径：
// 网关**不再**打 `/stocks/count`（data 从未有该路由），而是读 `GET /stocks`
// 后聚合 count/by_status，并原样带出 latest_date。
func TestStockCount_DerivedFromStocksReadFace(t *testing.T) {
	var gotPath string
	router, received := newProxyTestRouter(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		jsonUpstream(http.StatusOK, `{"stocks":[{"status":"active"},{"status":"active"},{"status":"delisted"},{"status":""}],"latest_date":"2024-06-28"}`)(w, r)
	})

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/stocks/count", nil))

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, *received, 1)
	assert.Equal(t, "/stocks", gotPath, "派生读面必须是 /stocks，不是 /stocks/count")

	var got struct {
		Count      int              `json:"count"`
		ByStatus   map[string]int64 `json:"by_status"`
		LatestDate *string          `json:"latest_date"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, 4, got.Count)
	assert.Equal(t, int64(2), got.ByStatus["active"])
	assert.Equal(t, int64(1), got.ByStatus["delisted"])
	assert.Equal(t, int64(1), got.ByStatus["unknown"], "空 status 折成 unknown（DATA-02 口径）")
	require.NotNil(t, got.LatestDate)
	assert.Equal(t, "2024-06-28", *got.LatestDate)
}

// TestStockCount_UpstreamFailureIs5xx 钉住「读失败不折成 0」：票池读挂时
// 必须回 5xx，而不是一个看起来正常的 count=0。
func TestStockCount_UpstreamFailureIs5xx(t *testing.T) {
	router, _ := newProxyTestRouter(t, jsonUpstream(http.StatusServiceUnavailable, `{"error":{"code":"dependency_unavailable"}}`))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/stocks/count", nil))
	assert.Equal(t, http.StatusBadGateway, w.Code, w.Body.String())
}

// TestIngestRaw_Retired 钉住退役壳：410 + 机器码，且上游收到 0 次请求。
func TestIngestRaw_Retired(t *testing.T) {
	router, received := newProxyTestRouter(t, jsonUpstream(http.StatusOK, `{"unexpected":true}`))
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/ingest/raw", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusGone, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"code":"ingest_raw_retired"`)
	assert.Empty(t, *received, "退役路由不得向上游发请求")
}

// TestIngestPoints_ProxiesToTheRealWriteDoor 钉住对齐后的真实路径：
// `/api/ingest/points` → data `/ingest/points`，body 与凭证原样透传。
func TestIngestPoints_ProxiesToTheRealWriteDoor(t *testing.T) {
	var gotPath, gotBody, gotAuth string
	router, _ := newProxyTestRouter(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		buf := new(strings.Builder)
		_, _ = io.Copy(buf, r.Body)
		gotBody = buf.String()
		jsonUpstream(http.StatusOK, `{"accepted":1}`)(w, r)
	})

	payload := `{"points":[{"symbol":"600519.SH"}]}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/ingest/points", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer trader-token")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "/ingest/points", gotPath)
	assert.Equal(t, payload, gotBody)
	assert.Equal(t, "Bearer trader-token", gotAuth)
}

// TestProxyForwardsAuthorization 是裁-15 的核心侧守卫：数据服务上鉴权后，
// proxyRequest 必须把入站 Authorization 原样带给上游。否则 readonly-post
// （POST /api/v1/ohlcv/bulk）会在网关这一层被上游 401，而调用方明明持有凭证。
func TestProxyForwardsAuthorization(t *testing.T) {
	router, received := newProxyTestRouter(t, jsonUpstream(http.StatusOK, `{"bars":[]}`))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ohlcv/bulk",
		strings.NewReader(`{"symbols":["000001.SZ"],"start_date":"20260101","end_date":"20260105"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer trader-token")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	require.Len(t, *received, 1)
	assert.Equal(t, "Bearer trader-token", (*received)[0].Header.Get("Authorization"),
		"proxyRequest 必须透传入站凭证，否则数据服务会 401")
}

// TestProtectedRouteThroughProxyDoesNotLoseCredentials 用一个「无凭证即 401」
// 的假上游模拟已接线鉴权的数据服务：同一请求带凭证时不应被代理弄丢而 401。
func TestProtectedRouteThroughProxyDoesNotLoseCredentials(t *testing.T) {
	router, _ := newProxyTestRouter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer trader-token" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"unauthenticated","message":"authentication required"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"bars":[]}`))
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ohlcv/bulk",
		strings.NewReader(`{"symbols":["000001.SZ"],"start_date":"20260101","end_date":"20260105"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer trader-token")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code, "受保护路由经代理不得 401；body: %s", w.Body.String())
}
