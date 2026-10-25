package main

// handlers_proxy_test.go — TASKS.md P5-3 (citation coordinates at the gateway)
//
// The analysis gateway is the only door the SPA has into L0 (ADR-022 §3, one
// direction only). GET /api/factors/:factor_name must therefore (a) add the
// /api prefix the SPA uses onto the prefix-less L0 route, (b) forward the
// symbol/date selectors, and (c) pass the L0 status/body through untouched —
// a hash-only citation is a first-class answer, not something to rewrite.

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

// TestMarketIndexProxy_ForwardsSelectors 是 DATA-11 的一条独立腿：
// `/api/market/index` 原来把目标 URL 拼成常量，**query string 在这一层就被丢了** ——
// 数据服务侧把 symbol/date 接住之后，SPA 仍然什么都收不到，界面照样是装饰件。
// SPEC §2 原来写的「转发是原样透传，问题在数据服务侧」据此证伪并改正。
func TestMarketIndexProxy_ForwardsSelectors(t *testing.T) {
	router, received := newProxyTestRouter(t, jsonUpstream(http.StatusOK, `{"indices":[]}`))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/market/index?symbol=000001.SH&date=2026-09-22", nil)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	require.Len(t, *received, 1)
	assert.Equal(t, "/market/index", (*received)[0].URL.Path)
	assert.Equal(t, "000001.SH", (*received)[0].URL.Query().Get("symbol"),
		"symbol 必须到得了数据服务，否则 DATA-11 的参数化只做到网关这一层")
	assert.Equal(t, "2026-09-22", (*received)[0].URL.Query().Get("date"),
		"date 同理；as-of 读数丢在网关就等于丢在链路上")
}

// TestMarketIndexProxy_OmitsEmptyQuery 对照腿：没给参数就不要拼出 `?` 的空 query，
// 让 L0 继续对「不传 = 三个都给的默认读数面」负责。
func TestMarketIndexProxy_OmitsEmptyQuery(t *testing.T) {
	router, received := newProxyTestRouter(t, jsonUpstream(http.StatusOK, `{"indices":[]}`))

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/market/index", nil))

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	require.Len(t, *received, 1)
	assert.Empty(t, (*received)[0].URL.RawQuery,
		"没给选择器时不该凭空造 query（空 symbol= 会被 L0 当成有效值还是空值是它的判断）")
}

// TestMarketIndexProxy_PassesSelectorErrorThrough 钉「坏参数的话得活着走到浏览器」：
// 数据服务对服务集外的 symbol 回 400 + 指名理由，网关必须原样转状态码和文案 ——
// 折叠成 200 + 空数组就是「界面说今天没行情」那条老路，折叠成 502 又会被 ui 说成
// 「这次请求没成功」。
func TestMarketIndexProxy_PassesSelectorErrorThrough(t *testing.T) {
	body := `{"error":{"code":"bad_request","message":"unsupported symbol \"600000.SH\": this endpoint serves index bars only (000300.SH, 000001.SH, 399006.SZ)"}}`
	router, _ := newProxyTestRouter(t, jsonUpstream(http.StatusBadRequest, body))

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/market/index?symbol=600000.SH", nil))

	assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), "unsupported symbol",
		"400 的理由必须原样透出，网关不 launder 成通用句；实际 body：%s", w.Body.String())
}

// TestFactorCitationProxy_ForwardsSelectorsToPrefixlessL0Route pins the path
// shape: the SPA asks for /api/factors/<name>, L0 serves /factors/<name>.
func TestFactorCitationProxy_ForwardsSelectorsToPrefixlessL0Route(t *testing.T) {
	router, received := newProxyTestRouter(t, jsonUpstream(http.StatusOK, `{
		"id": 1,
		"symbol": "000001.SZ",
		"factor_name": "momentum",
		"raw_value": 1.25,
		"citation": [{"content_hash": "aa", "source": "tushare", "dataset": "daily"}]
	}`))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/factors/momentum?symbol=000001.SZ&date=20260105", nil)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	require.Len(t, *received, 1)
	assert.Equal(t, "/factors/momentum", (*received)[0].URL.Path)
	assert.Equal(t, "000001.SZ", (*received)[0].URL.Query().Get("symbol"))
	assert.Equal(t, "20260105", (*received)[0].URL.Query().Get("date"))
	assert.Contains(t, w.Body.String(), `"source":"tushare"`)
}

// TestFactorCitationProxy_OmitsEmptyQuery verifies selectors are forwarded
// only when present, so L0 keeps ownership of the required-param errors.
func TestFactorCitationProxy_OmitsEmptyQuery(t *testing.T) {
	router, received := newProxyTestRouter(t, jsonUpstream(http.StatusBadRequest, `{"error":"symbol query param is required"}`))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/factors/momentum", nil)
	router.ServeHTTP(w, req)

	require.Len(t, *received, 1)
	assert.Equal(t, "/factors/momentum", (*received)[0].URL.Path)
	assert.Empty(t, (*received)[0].URL.RawQuery)
	// L0's own validation answer travels through unchanged.
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "symbol query param is required")
}

// TestFactorCitationProxy_PassesNotFoundThrough: a row-less lookup is a
// first-class 404 answer, and the gateway must not launder it into a 5xx.
func TestFactorCitationProxy_PassesNotFoundThrough(t *testing.T) {
	router, _ := newProxyTestRouter(t, jsonUpstream(http.StatusNotFound, `{"error":"factor cache entry not found"}`))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/factors/momentum?symbol=000001.SZ&date=20260105", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "factor cache entry not found")
}

// TestFactorCitationProxy_KeepsHashOnlyTuple: when the source response was
// never archived the tuple carries only content_hash — the absence of the
// other four fields is the signal, so nothing may be padded in transit.
func TestFactorCitationProxy_KeepsHashOnlyTuple(t *testing.T) {
	router, _ := newProxyTestRouter(t, jsonUpstream(http.StatusOK, `{
		"symbol": "000001.SZ",
		"factor_name": "momentum",
		"citation": [{"content_hash": "bb"}]
	}`))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/factors/momentum?symbol=000001.SZ&date=20260105", nil)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"content_hash":"bb"`)
	assert.NotContains(t, w.Body.String(), `"source"`)
	assert.NotContains(t, w.Body.String(), `"dataset"`)
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
