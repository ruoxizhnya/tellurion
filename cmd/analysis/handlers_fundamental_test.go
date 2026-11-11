package main

// handlers_fundamental_test.go — 归属裁决（2026-11-12）
//
// 钉住三条口径：
//  1. 空表/无记录是业务空态 → 404 `fundamental_not_found`，不是 500；
//  2. 有记录 → 200 `{fundamental: {...}}`（保留数据服务退役前的形状）；
//  3. 未接库 → 503，未注册 → 400（坏日期）。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"tellurion/pkg/domain"
	"tellurion/pkg/storage"
)

type fakeFundamentalReader struct {
	gotSymbol string
	gotDate   time.Time
	record    *domain.Fundamental
	err       error
}

func (f *fakeFundamentalReader) GetFundamental(_ context.Context, symbol string, date time.Time) (*domain.Fundamental, error) {
	f.gotSymbol = symbol
	f.gotDate = date
	return f.record, f.err
}

func newFundamentalServer(reader fundamentalReader) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	registerFundamentalRoutes(router, reader)
	return router
}

func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	// httpserver 的错误体是扁平的：{"error": "<文案>", "code": "<机器码>"}。
	var payload struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(body, &payload), "body: %s", body)
	return payload.Code
}

// TestGetFundamental_EmptyTableIs404 是主腿：没有记录必须回 404 机器码，
// 不能把「没同步过」冒成 500。
func TestGetFundamental_EmptyTableIs404(t *testing.T) {
	reader := &fakeFundamentalReader{err: storage.ErrFundamentalNotFound}
	router := newFundamentalServer(reader)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/fundamental/600519.SH?date=20240628", nil))

	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	assert.Equal(t, "fundamental_not_found", errorCode(t, w.Body.Bytes()))
	assert.Equal(t, "600519.SH", reader.gotSymbol)
	assert.Equal(t, time.Date(2024, 6, 28, 0, 0, 0, 0, time.UTC), reader.gotDate)
}

// TestGetFundamental_WithDataIs200 钉住有数据时的形状与字段。
func TestGetFundamental_WithDataIs200(t *testing.T) {
	pe := 17.5
	reader := &fakeFundamentalReader{record: &domain.Fundamental{
		Symbol: "600519.SH",
		Date:   time.Date(2024, 6, 28, 0, 0, 0, 0, time.UTC),
		PE:     &pe,
	}}
	router := newFundamentalServer(reader)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/fundamental/600519.SH?date=2024-06-28", nil))

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var payload struct {
		Fundamental struct {
			Symbol string   `json:"symbol"`
			PE     *float64 `json:"pe"`
		} `json:"fundamental"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	assert.Equal(t, "600519.SH", payload.Fundamental.Symbol)
	require.NotNil(t, payload.Fundamental.PE)
	assert.Equal(t, 17.5, *payload.Fundamental.PE)
}

// TestGetFundamental_UnwiredIs503 钉住「未接库」与「空表」是两回事。
func TestGetFundamental_UnwiredIs503(t *testing.T) {
	router := newFundamentalServer(nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/fundamental/600519.SH", nil))

	require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
	assert.Equal(t, "dependency_unavailable", errorCode(t, w.Body.Bytes()))
}

// TestGetFundamental_BadDateIs400 钉住日期解析错误不会被当成空表。
func TestGetFundamental_BadDateIs400(t *testing.T) {
	router := newFundamentalServer(&fakeFundamentalReader{})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/fundamental/600519.SH?date=2024/06/28", nil))

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Equal(t, "invalid_parameter", errorCode(t, w.Body.Bytes()))
}
