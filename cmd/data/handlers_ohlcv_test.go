package main

// DATA-13：`/ohlcv` 的日期格式原来被 pkg/data 的 parseDate 吞掉（`t, _ := time.Parse`），
// ISO 写法塌成零值日期 ⇒ 200 `{"ohlcv":null}`，把「我不认这个格式」显示成
// 「这段时间没有行情」。本文件钉两件事：
//   1. `invalidDateParam` 的接受集（纯函数腿，零 DB / 零 Redis）；
//   2. handler 在**进库之前**就 400，且失败响应里不再出现 `{"ohlcv":null` 这个形状
//      （`getOHLCVHandler` / `bulkOHLCVHandler` / `warmCacheHandler` 三条入口）。
//
// 用 `data.NewDataCache(nil, nil)` 构造：坏格式的请求在校验那一步就返回，**不会**
// 触到 nil cache/store；若哪天有人把校验挪到进库之后，这条会以 panic 的形式红 ——
// 那正是「先校验再进库」的顺序证明。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/data"
)

func TestInvalidDateParam(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		value  string
		accept bool
	}{
		{name: "标准 YYYYMMDD", value: "20240101", accept: true},
		{name: "年末", value: "20241231", accept: true},
		{name: "闰日", value: "20240229", accept: true},
		{name: "ISO 连字符：拒", value: "2024-01-01", accept: false},
		{name: "月日不补零：拒", value: "202411", accept: false},
		{name: "7 位缺一日：拒", value: "2024011", accept: false},
		{name: "带时刻后缀：拒", value: "20240101T00:00:00Z", accept: false},
		{name: "超范围月日：拒", value: "20241345", accept: false},
		{name: "非数字：拒", value: "昨天", accept: false},
		{name: "空串：拒", value: "", accept: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg := invalidDateParam("start_date", tc.value)
			if tc.accept {
				assert.Empty(t, msg, "%q 应被接受", tc.value)
				return
			}
			assert.Contains(t, msg, "invalid start_date format, use YYYYMMDD",
				"400 文案必须点名参数与格式；实得：%q", msg)
		})
	}
}

// TestGetOHLCVHandler_InvalidDateFormatFailsLoud 是 DATA-13 的主行为腿：
// 坏格式 ⇒ 400 且文案点名 YYYYMMDD，且响应体里不再出现 `"ohlcv"` 键
// （旧形状 200 `{"ohlcv":null}` 在失败路径上就此消失）。
//
// gin 的 mode 由本包 TestMain 设一次（AUD-28/AUD-29）—— 别在这里调 gin.SetMode。
func TestGetOHLCVHandler_InvalidDateFormatFailsLoud(t *testing.T) {
	router := gin.New()
	router.GET("/ohlcv/:symbol", getOHLCVHandler(data.NewDataCache(nil, nil)))

	cases := []struct{ start, end string }{
		{"2024-01-01", "20241231"},
		{"20240101", "2024-12-31"},
		{"2024011", "20241231"},
		{"20240101", "20240101T00:00:00Z"},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet,
			"/ohlcv/600000.SH?start_date="+tc.start+"&end_date="+tc.end, nil)
		router.ServeHTTP(w, req)

		require.Equal(t, http.StatusBadRequest, w.Code,
			"start=%q end=%q 必须 400（旧形状会静默 200 {\"ohlcv\":null}）；实际 body：%s",
			tc.start, tc.end, w.Body.String())
		assert.Contains(t, w.Body.String(), "invalid",
			"文案要点名坏参数；实际 body：%s", w.Body.String())
		assert.Contains(t, w.Body.String(), "use YYYYMMDD",
			"文案要点名唯一接受的格式；实际 body：%s", w.Body.String())
		assert.False(t, strings.Contains(w.Body.String(), `"ohlcv"`),
			"失败响应不得带半份成功数据（连 null 都不行）；实际 body：%s", w.Body.String())
	}
}

// TestGetOHLCVHandler_MissingDatesFailsLoud 保留既有语义：缺参仍是 400。
func TestGetOHLCVHandler_MissingDatesFailsLoud(t *testing.T) {
	router := gin.New()
	router.GET("/ohlcv/:symbol", getOHLCVHandler(data.NewDataCache(nil, nil)))

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ohlcv/600000.SH", nil))

	assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), "required", "body: %s", w.Body.String())
}

// TestBulkOHLCVHandler_InvalidDateFormatFailsLoud 钉同一族入口：bulk 也吃
// start_date/end_date，坏格式同样必须 400，不能也静默塌成空读数。
func TestBulkOHLCVHandler_InvalidDateFormatFailsLoud(t *testing.T) {
	router := gin.New()
	router.POST("/api/v1/ohlcv/bulk", bulkOHLCVHandler(data.NewDataCache(nil, nil)))

	body := `{"symbols":["600000.SH"],"start_date":"2024-01-01","end_date":"20241231"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ohlcv/bulk", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), "invalid start_date format, use YYYYMMDD",
		"body: %s", w.Body.String())
}

// TestWarmCacheHandler_InvalidDateFormatFailsLoud 第三族入口：缓存预热同样只认 YYYYMMDD。
func TestWarmCacheHandler_InvalidDateFormatFailsLoud(t *testing.T) {
	router := gin.New()
	router.POST("/api/v1/cache/warm", warmCacheHandler(data.NewDataCache(nil, nil)))

	body := `{"symbols":["600000.SH"],"start_date":"20240101","end_date":"2024-12-31"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/cache/warm", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), "invalid end_date format, use YYYYMMDD",
		"body: %s", w.Body.String())
}
