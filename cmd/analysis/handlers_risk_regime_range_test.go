package main

// QT-70：detect_regime 的粗粒度形状（symbol + 日期区间，服务端取数）。
//
// 验收点：
//  1. 粗粒度请求按 symbol/区间取数并返回 regime；
//  2. 三个字段缺一 / 日期坏 / start>end ⇒ 400 点名；
//  3. 取数面未接线 ⇒ 503（不退回「data field is required」这种误导性 400）；
//  4. 区间内无行 ⇒ 404（不拿空序列编一份看似合理的 regime）；
//  5. 内联 data 仍优先（存量契约不变），且不会触发取数。
//
// 破坏验证：把 detectRegimeByRange 的 `len(rows)==0` 分支删掉 ⇒ 检测器报
// 「insufficient data」→ 500，第 4 条红；逐字节还原后绿。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeRegimeStore struct {
	calls  int
	symbol string
	start  time.Time
	end    time.Time
	rows   []domain.OHLCV
	err    error
}

func (f *fakeRegimeStore) GetOHLCV(_ context.Context, symbol string, startDate, endDate time.Time) ([]domain.OHLCV, error) {
	f.calls++
	f.symbol = symbol
	f.start = startDate
	f.end = endDate
	return f.rows, f.err
}

func regimeRows(n int) []domain.OHLCV {
	rows := make([]domain.OHLCV, 0, n)
	base := time.Date(1990, 1, 2, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		close := 100 + float64(i)*0.5
		rows = append(rows, domain.OHLCV{
			Symbol: "600000.SH",
			Date:   base.AddDate(0, 0, i),
			Open:   close - 0.2,
			High:   close + 0.5,
			Low:    close - 0.5,
			Close:  close,
			Volume: 1_000_000,
		})
	}
	return rows
}

func postRegime(t *testing.T, h *RiskHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	router := gin.New()
	h.RegisterRoutes(router)
	req := httptest.NewRequest(http.MethodPost, "/api/risk/detect_regime", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestDetectRegime_CoarseShapeFetchesBars(t *testing.T) {
	store := &fakeRegimeStore{rows: regimeRows(80)}
	h := newTestRiskHandler(t).WithOHLCVStore(store)

	w := postRegime(t, h, `{"symbol":"600000.SH","start_date":"1990-01-01","end_date":"1990-06-30"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var regime domain.MarketRegime
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &regime))
	assert.NotEmpty(t, regime.Trend)
	assert.NotEmpty(t, regime.Volatility)

	assert.Equal(t, 1, store.calls)
	assert.Equal(t, "600000.SH", store.symbol)
	assert.Equal(t, "1990-01-01", store.start.Format("2006-01-02"))
	assert.Equal(t, "1990-06-30", store.end.Format("2006-01-02"))
}

func TestDetectRegime_CoarseShapeValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"缺日期", `{"symbol":"600000.SH"}`, "start_date"},
		{"缺 symbol", `{"start_date":"1990-01-01","end_date":"1990-06-30"}`, "symbol"},
		{"坏 start", `{"symbol":"600000.SH","start_date":"1990/01/01","end_date":"1990-06-30"}`, "start_date"},
		{"坏 end", `{"symbol":"600000.SH","start_date":"1990-01-01","end_date":"1990/06/30"}`, "end_date"},
		{"区间倒置", `{"symbol":"600000.SH","start_date":"1990-06-30","end_date":"1990-01-01"}`, "start_date"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestRiskHandler(t).WithOHLCVStore(&fakeRegimeStore{rows: regimeRows(80)})
			w := postRegime(t, h, tc.body)
			assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), tc.want)
		})
	}
}

func TestDetectRegime_CoarseShapeFailClosed(t *testing.T) {
	t.Run("取数面未接线", func(t *testing.T) {
		h := newTestRiskHandler(t) // 不注入 store
		w := postRegime(t, h, `{"symbol":"600000.SH","start_date":"1990-01-01","end_date":"1990-06-30"}`)
		assert.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
		assert.Contains(t, w.Body.String(), "unavailable")
	})
	t.Run("区间内无行", func(t *testing.T) {
		h := newTestRiskHandler(t).WithOHLCVStore(&fakeRegimeStore{rows: nil})
		w := postRegime(t, h, `{"symbol":"600000.SH","start_date":"1990-01-01","end_date":"1990-06-30"}`)
		assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
		assert.Contains(t, w.Body.String(), "no OHLCV rows")
	})
}

func TestDetectRegime_InlineDataStillWins(t *testing.T) {
	store := &fakeRegimeStore{rows: regimeRows(80)}
	h := newTestRiskHandler(t).WithOHLCVStore(store)

	inline, err := json.Marshal(map[string]any{"data": regimeRows(30)})
	require.NoError(t, err)
	w := postRegime(t, h, string(inline))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Zero(t, store.calls, "内联 data 优先，粗粒度取数不应被触发")
}
