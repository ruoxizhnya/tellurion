package main

// `/api/market/index` 本地供数的行为腿（SVC-CORE-DATA-RETIRE 退役遗留，
// 2026-11-10）。这些用例注入假 reader，不碰数据库：读 SQL 的语义（窗口函数、
// as-of 回退、NULL 语义）由真库腿另行取证，这里钉的是**契约与分诊**——
// 服务集顺序、symbol 收窄/拒收、date 格式、null 语义、未接库 503。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/storage"
)

// fakeIndexReader 记录收到的 codes/asOf 并回放预设行，便于断言分派。
type fakeIndexReader struct {
	gotCodes []string
	gotAsOf  *time.Time
	bars     []storage.LatestIndexBar
	err      error
}

func (f *fakeIndexReader) LatestIndexBars(_ context.Context, codes []string, asOf *time.Time) ([]storage.LatestIndexBar, error) {
	f.gotCodes = append([]string(nil), codes...)
	f.gotAsOf = asOf
	return f.bars, f.err
}

func newMarketIndexTestRouter(t *testing.T, reader indexDailyReader) *gin.Engine {
	t.Helper()
	// gin 的 mode 由 TestMain 设一次（AUD-12）—— 这里不调 gin.SetMode。
	router := gin.New()
	registerMarketIndexRoutes(router, reader, zerolog.Nop())
	return router
}

func decodeMarketIndex(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var resp struct {
		Indices []map[string]any `json:"indices"`
	}
	require.NoError(t, json.Unmarshal(body, &resp), "body: %s", string(body))
	return resp.Indices
}

func ptr(v float64) *float64 { return &v }

func TestMarketIndex_DefaultServesAllThreeInServiceOrder(t *testing.T) {
	day := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	reader := &fakeIndexReader{bars: []storage.LatestIndexBar{
		// 故意按 code 升序给（与存储层一致），断言输出仍按服务集顺序。
		{Code: "000001.SH", TradeDate: day, Close: 3825.5, Volume: 420_000_000, PrevClose: ptr(3800.25)},
		{Code: "000300.SH", TradeDate: day, Close: 4439.14, Volume: 151_965_787, PrevClose: ptr(4400.0)},
		{Code: "399006.SZ", TradeDate: day, Close: 2500, Volume: 1, PrevClose: ptr(2500)},
	}}
	router := newMarketIndexTestRouter(t, reader)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/market/index", nil))

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	assert.Equal(t, []string{"000300.SH", "000001.SH", "399006.SZ"}, reader.gotCodes,
		"不传 symbol = 服务集三个，且顺序固定")
	assert.Nil(t, reader.gotAsOf, "不传 date = 不设 as-of")
	indices := decodeMarketIndex(t, w.Body.Bytes())
	require.Len(t, indices, 3)
	assert.Equal(t, "000300.SH", indices[0]["code"])
	assert.Equal(t, "沪深300", indices[0]["name"])
	assert.Equal(t, "000001.SH", indices[1]["code"])
	assert.Equal(t, "399006.SZ", indices[2]["code"])
	assert.NotContains(t, w.Body.String(), "updated_at", "契约里没有这个键（DATA-07 已删）")
}

func TestMarketIndex_SymbolNarrowsToThatOne(t *testing.T) {
	reader := &fakeIndexReader{}
	router := newMarketIndexTestRouter(t, reader)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/market/index?symbol=000001.SH", nil))

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	assert.Equal(t, []string{"000001.SH"}, reader.gotCodes)
	indices := decodeMarketIndex(t, w.Body.Bytes())
	assert.Empty(t, indices, "reader 没回行 ⇒ 200 + 空数组，不编零值")
	assert.Contains(t, w.Body.String(), `"indices":[]`, "空结果必须是 [] 而不是 null")
}

func TestMarketIndex_UnknownSymbolIs400NotPassthrough(t *testing.T) {
	reader := &fakeIndexReader{}
	router := newMarketIndexTestRouter(t, reader)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/market/index?symbol=600000.SH", nil))

	assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), "unsupported symbol")
	assert.Contains(t, w.Body.String(), "serves index bars only",
		"理由要指名这个端点只供给什么")
	assert.Nil(t, reader.gotCodes, "坏参数不许碰存储（parse→validate→query）")
}

func TestMarketIndex_BadDateIs400(t *testing.T) {
	for _, bad := range []string{"20240101", "2026-9-22", "2026-09-22T00:00:00Z"} {
		t.Run(bad, func(t *testing.T) {
			reader := &fakeIndexReader{}
			router := newMarketIndexTestRouter(t, reader)

			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/market/index?date="+bad, nil))

			assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
			assert.Contains(t, w.Body.String(), "use YYYY-MM-DD")
			assert.Nil(t, reader.gotCodes)
		})
	}
}

func TestMarketIndex_DateBecomesAsOf(t *testing.T) {
	reader := &fakeIndexReader{}
	router := newMarketIndexTestRouter(t, reader)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/market/index?date=2026-09-22", nil))

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	require.NotNil(t, reader.gotAsOf)
	assert.Equal(t, time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC), reader.gotAsOf.UTC())
}

func TestMarketIndex_NullSemanticsAndPercentScale(t *testing.T) {
	day := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	reader := &fakeIndexReader{bars: []storage.LatestIndexBar{
		// 有 prev_close、turnover 为 NULL：change/pct 算出来，amount 为 null。
		{Code: "000001.SH", TradeDate: day, Close: 3825.5, Volume: 420_000_000, PrevClose: ptr(3800.25)},
		// 无 prev_close：change/pct 必须 null（不是 +0.00%）。
		{Code: "399006.SZ", TradeDate: day, Close: 2500, Volume: 1, Turnover: ptr(530_000_000)},
	}}
	router := newMarketIndexTestRouter(t, reader)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/market/index", nil))

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	indices := decodeMarketIndex(t, w.Body.Bytes())
	require.Len(t, indices, 2, "库里没有行的 000300.SH 不产条目")

	// pct 是百分数刻度：0.6644… 而不是 0.006644（展示端不再乘 100）。
	assert.InDelta(t, 25.25, indices[0]["change"], 1e-9)
	assert.InDelta(t, 0.664436, indices[0]["pct"], 1e-5)
	assert.Nil(t, indices[0]["amount"], "turnover NULL ⇒ amount null，不是 0")

	assert.Nil(t, indices[1]["change"], "无上一期收盘 ⇒ change null")
	assert.Nil(t, indices[1]["pct"])
	assert.InDelta(t, 530_000_000, indices[1]["amount"], 1e-9, "amount = turnover（CNY），展示层换算")
}

func TestMarketIndex_ReaderNilIs503(t *testing.T) {
	router := newMarketIndexTestRouter(t, nil)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/market/index", nil))

	assert.Equal(t, http.StatusServiceUnavailable, w.Code, "body: %s", w.Body.String())
}

func TestMarketIndex_ReaderErrorIs500(t *testing.T) {
	reader := &fakeIndexReader{err: errors.New("boom")}
	router := newMarketIndexTestRouter(t, reader)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/market/index", nil))

	assert.Equal(t, http.StatusInternalServerError, w.Code, "body: %s", w.Body.String())
}
