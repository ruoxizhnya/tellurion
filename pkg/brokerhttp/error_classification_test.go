package brokerhttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	cdom "github.com/ruoxizhnya/tellurion-contracts/domain"
	apperrors "github.com/ruoxizhnya/tellurion-kit/errors"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/backtest/execution"
	"tellurion/pkg/domain"
)

// F4：broker 侧不可用必须带上游类别（UNAVAILABLE / TIMEOUT），
// 不能与「这笔请求被拒」共用同一种无类别错误 —— 否则 HTTP 层只能
// 一律回 400 rejected，把「通道挂了」误报成「参数不对」。

func newClassificationPort(t *testing.T, baseURL string) *BrokerHTTPPort {
	t.Helper()
	p, err := NewBrokerHTTPPort(BrokerHTTPOptions{
		BaseURL:      baseURL,
		ServiceToken: "svc-test",
		AccountID:    "acc-test",
		HTTPClient:   &http.Client{},
	})
	require.NoError(t, err)
	return p
}

func submitMarketOrder(p *BrokerHTTPPort) error {
	_, err := p.SubmitOrder(context.Background(), execution.OrderRequest{
		Symbol:    "600000.SH",
		Direction: cdom.DirectionLong,
		OrderType: domain.OrderTypeMarket,
		Quantity:  100,
	})
	return err
}

func TestSubmitOrder_Upstream5xxIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"code":"matcher_offline","message":"matching engine offline"}`))
	}))
	defer srv.Close()

	err := submitMarketOrder(newClassificationPort(t, srv.URL))
	require.Error(t, err)
	var appErr *apperrors.AppError
	require.ErrorAs(t, err, &appErr, "5xx 必须带 AppError 类别")
	require.Equal(t, apperrors.ErrCodeUnavailable, appErr.Code)
}

func TestSubmitOrder_TransportFailureIsUnavailable(t *testing.T) {
	// 关闭的 httptest server 等于「连接被拒」，与 DNS no such host 同类：
	// 传输层失败，不是参数错误。
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	base := srv.URL
	srv.Close()

	err := submitMarketOrder(newClassificationPort(t, base))
	require.Error(t, err)
	var appErr *apperrors.AppError
	require.ErrorAs(t, err, &appErr, "传输层失败必须带 AppError 类别")
	require.Equal(t, apperrors.ErrCodeUnavailable, appErr.Code)
}

func TestSubmitOrder_Business4xxStaysPlainError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"code":"insufficient_cash","message":"not enough buying power"}`))
	}))
	defer srv.Close()

	err := submitMarketOrder(newClassificationPort(t, srv.URL))
	require.Error(t, err)
	var appErr *apperrors.AppError
	require.NotErrorAs(t, err, &appErr, "4xx 是业务拒绝，不该被标成上游不可用")
}
