package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/brokerhttp"
)

// F4：POST /api/execution/orders 在上游不可用时的 HTTP 语义。
// 端到端走真实 brokerhttp.Trader（不起容器），验证分类标签能被 HTTP 层
// 正确翻译：不可达 / 5xx → 503，业务拒绝 → 400 rejected。

func newOrderRouter(t *testing.T, brokerURL string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	trader, err := brokerhttp.NewTrader(brokerhttp.BrokerHTTPOptions{
		BaseURL:      brokerURL,
		ServiceToken: "svc-test",
		AccountID:    "acc-test",
	})
	require.NoError(t, err)
	h := NewExecutionHandler(trader, zerolog.Nop(), "")
	router := gin.New()
	router.POST("/api/execution/orders", h.createOrder)
	return router
}

func postTestOrder(t *testing.T, router *gin.Engine) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"symbol":"600000.SH","side":"long","type":"market","quantity":100}`
	req := httptest.NewRequest(http.MethodPost, "/api/execution/orders", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestCreateOrder_Broker5xxReturns503(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":"internal","message":"matcher exploded"}`))
	}))
	defer srv.Close()

	w := postTestOrder(t, newOrderRouter(t, srv.URL))
	require.Equal(t, http.StatusServiceUnavailable, w.Code, "上游 5xx 必须是 503，不是 400")
	require.Contains(t, w.Body.String(), "broker unavailable")
	require.NotContains(t, w.Body.String(), "matcher exploded", "上游原文只进日志，不回给客户端")
}

func TestCreateOrder_BrokerUnreachableReturns503(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close() // 连接被拒，等价于 broker 掉线 / DNS 不可达

	w := postTestOrder(t, newOrderRouter(t, base))
	require.Equal(t, http.StatusServiceUnavailable, w.Code, "不可达必须是 503，不是 400 rejected")
}

func TestCreateOrder_BrokerBusinessRejectionStays400(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":"insufficient_cash","message":"not enough buying power"}`))
	}))
	defer srv.Close()

	w := postTestOrder(t, newOrderRouter(t, srv.URL))
	require.Equal(t, http.StatusBadRequest, w.Code, "broker 的业务拒绝仍走 400")
	require.Contains(t, w.Body.String(), `"status":"rejected"`)
}
