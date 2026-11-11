package main

// handlers_proxy_test.go — TASKS.md C8 / CORE-FACTOR-DEAD-PROXY
//
// GET /api/factors/:factor_name 曾把 SPA 的请求转发到 data 的 `/factors/:name`；
// 上游路由已随数据面退役（裁-14），继续转发只会把「能力已退役」伪装成上游
// 404。现在这条路由显式返回 410 `factor_http_surface_retired`，且**不得**再
// 向上游发请求（ADR-037 §5 / ADR-041）。

import (
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
