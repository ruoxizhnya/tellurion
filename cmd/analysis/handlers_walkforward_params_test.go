package main

// QT-68 行为腿：POST /api/walkforward 的 strategy_params 非法时必须在
// **启动任何窗口之前** 400，并点名出错的键。用 nil store 的引擎证明请求
// 没有走到取数（走到了会 panic，而不是干净地回 400）。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"tellurion/pkg/backtest"
)

func TestRunWalkForwardHandler_InvalidStrategyParamsIs400NamingKey(t *testing.T) {
	router := gin.New()
	registerWalkForwardRoutes(router, backtest.NewWalkForwardEngine(nil, nil, zerolog.Nop()), zerolog.Nop())

	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "未知键",
			body: `{"strategy":"momentum","stock_pool":["600000.SH"],"start_date":"2024-01-01","end_date":"2024-12-31","strategy_params":{"nope":1}}`,
			want: "nope",
		},
		{
			name: "类型不符",
			body: `{"strategy":"momentum","stock_pool":["600000.SH"],"start_date":"2024-01-01","end_date":"2024-12-31","strategy_params":{"lookback_days":"twenty"}}`,
			want: "lookback_days",
		},
		{
			name: "范围越界",
			body: `{"strategy":"momentum","stock_pool":["600000.SH"],"start_date":"2024-01-01","end_date":"2024-12-31","strategy_params":{"lookback_days":1}}`,
			want: "lookback_days",
		},
		{
			name: "嵌套形状",
			body: `{"strategy":"momentum","stock_pool":["600000.SH"],"start_date":"2024-01-01","end_date":"2024-12-31","strategy_params":{"lookback_days":{"nested":1}}}`,
			want: "strategy_params",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/walkforward", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), tc.want,
				"400 必须点名出错的键（静默吞掉会让调用方以为参数生效了）")
		})
	}
}
