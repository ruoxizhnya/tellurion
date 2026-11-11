package main

// `POST /api/screen` 本地供数的契约测试（2026-11-12 裁）。
//
// 钉住三件事：请求体两种拼法（扁平 ui / 嵌套 domain）都能被接受、响应形状
// 是 `{count, results}` 且 results 恒为数组、未接库时 fail-closed 503。

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/domain"
)

type stubScreenReader struct {
	gotFilters domain.ScreenFilters
	gotDate    *time.Time
	gotLimit   int
	rows       []domain.ScreenResult
	err        error
}

func (s *stubScreenReader) ScreenFundamentals(_ context.Context, filters domain.ScreenFilters, date *time.Time, limit int) ([]domain.ScreenResult, error) {
	s.gotFilters, s.gotDate, s.gotLimit = filters, date, limit
	return s.rows, s.err
}

func newScreenTestRouter(reader screenFundamentalsReader) *gin.Engine {
	router := gin.New()
	registerScreenRoutes(router, reader, zerolog.Nop())
	return router
}

func TestScreen_FlatBodyIsAcceptedAndResponseIsCountResults(t *testing.T) {
	peMax := 20.0
	reader := &stubScreenReader{rows: []domain.ScreenResult{
		{TsCode: "600519.SH", PE: &peMax},
	}}
	router := newScreenTestRouter(reader)

	body := []byte(`{"pe_max":20,"roe_min":10,"limit":10}`)
	req := httptest.NewRequest(http.MethodPost, "/api/screen", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NotNil(t, reader.gotFilters.PE_max)
	assert.Equal(t, 20.0, *reader.gotFilters.PE_max)
	require.NotNil(t, reader.gotFilters.ROE_min)
	assert.Equal(t, 10.0, *reader.gotFilters.ROE_min)
	assert.Equal(t, 10, reader.gotLimit)

	var got struct {
		Count   int                   `json:"count"`
		Results []domain.ScreenResult `json:"results"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, 1, got.Count)
	require.Len(t, got.Results, 1)
	assert.Equal(t, "600519.SH", got.Results[0].TsCode)
}

func TestScreen_NestedFiltersAndDateAreAccepted(t *testing.T) {
	reader := &stubScreenReader{}
	router := newScreenTestRouter(reader)

	body := []byte(`{"filters":{"pb_max":3},"date":"2026-11-02","limit":0}`)
	req := httptest.NewRequest(http.MethodPost, "/api/screen", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NotNil(t, reader.gotFilters.PB_max)
	assert.Equal(t, 3.0, *reader.gotFilters.PB_max)
	require.NotNil(t, reader.gotDate)
	assert.Equal(t, "2026-11-02", reader.gotDate.Format("2006-01-02"))
	// limit=0（缺省）⇒ 默认 100，不是「不限」。
	assert.Equal(t, screenDefaultLimit, reader.gotLimit)
}

func TestScreen_EmptyBodyAppliesNoFilters(t *testing.T) {
	reader := &stubScreenReader{}
	router := newScreenTestRouter(reader)

	req := httptest.NewRequest(http.MethodPost, "/api/screen", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, screenDefaultLimit, reader.gotLimit)
	assert.Nil(t, reader.gotFilters.PE_max)

	// results 恒为数组：空结果是 []，不是 null（严格客户端会直接炸）。
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))
	assert.Equal(t, "[]", string(raw["results"]))
	assert.Equal(t, "0", string(raw["count"]))
}

func TestScreen_InvalidDateIs400(t *testing.T) {
	reader := &stubScreenReader{}
	router := newScreenTestRouter(reader)

	body := []byte(`{"date":"2026/11/02"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/screen", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

func TestScreen_NilReaderFailsClosed(t *testing.T) {
	router := newScreenTestRouter(nil)
	req := httptest.NewRequest(http.MethodPost, "/api/screen", bytes.NewReader([]byte(`{}`)))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
}
