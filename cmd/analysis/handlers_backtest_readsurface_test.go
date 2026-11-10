package main

// K4 / 裁决 ⑲：读面三态（不存在 / 没跑完 / 跑失败）的 handler 层行为腿。
//
// 四个端点（report / trades / equity / export）必须同一口径：
//
//	job == nil                → 404 run_not_found
//	pending / running         → 409 run_result_incomplete
//	failed                    → 409 run_result_failed
//	completed（含 invalid）    → 200
//
// code 与 analytics 既有三态同名，不允许 core 自造第二套命名。
// 破坏验证：把 failed 分支改回 404（或把 running 判成 200），对应用例立即红。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	contracts "github.com/ruoxizhnya/tellurion-contracts/engine"
	"tellurion/pkg/backtest"
)

const k4RunID = "bt-k4"

// newReadSurfaceRouter 用内存 JobStore 装配四个读面端点。
// exists=false 表示库里没有这条作业（只有一条路径会写 404）。
func newReadSurfaceRouter(t *testing.T, exists bool, status, resultStatus, body string) *gin.Engine {
	t.Helper()
	eng := newReadPathEngine(t)
	store := newMemJobStore()
	reg := contracts.NewEngineRegistry()
	reg.Register(contracts.DefaultKernel, eng)
	svc := backtest.NewJobService(store, zerolog.Nop())

	if exists {
		store.put(k4RunID, map[string]any{
			"id":            k4RunID,
			"strategy_id":   "momentum",
			"params":        []byte(`{}`),
			"universe":      "CSI300",
			"start_date":    "2024-01-02",
			"end_date":      "2024-02-02",
			"status":        status,
			"result_status": resultStatus,
			"result":        []byte(body),
			"created_at":    time.Date(2024, 2, 2, 0, 0, 0, 0, time.UTC),
		})
	}

	r := gin.New()
	registerBacktestRoutes(r, eng, reg, svc, nil, nil, nil, zerolog.Nop(), nil)
	return r
}

func TestBacktestReadSurface_ThreeStates(t *testing.T) {
	completedBody := `{"id":"` + k4RunID + `","status":"completed","trades":[],"equity_curve":[]}`
	invalidBody := `{"id":"` + k4RunID + `","invalid_reasons":["no_trades"],"trades":[],"equity_curve":[]}`

	cases := []struct {
		name         string
		exists       bool
		status       string
		resultStatus string
		body         string
		wantStatus   int
		wantCode     string
	}{
		{"missing", false, "", "", "", http.StatusNotFound, "run_not_found"},
		{"pending", true, "pending", "", "", http.StatusConflict, "run_result_incomplete"},
		{"running", true, "running", "", "", http.StatusConflict, "run_result_incomplete"},
		{"failed", true, "failed", "failed", "", http.StatusConflict, "run_result_failed"},
		{"completed", true, "completed", "completed", completedBody, http.StatusOK, ""},
		{"completed_invalid", true, "completed", "invalid", invalidBody, http.StatusOK, ""},
	}

	paths := []string{"report", "trades", "equity", "export/html"}
	for _, tc := range cases {
		tc := tc
		for _, p := range paths {
			p := p
			t.Run(tc.name+"/"+p, func(t *testing.T) {
				r := newReadSurfaceRouter(t, tc.exists, tc.status, tc.resultStatus, tc.body)
				w := httptest.NewRecorder()
				r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/backtest/"+k4RunID+"/"+p, nil))

				require.Equal(t, tc.wantStatus, w.Code, "body=%s", w.Body.String())
				if tc.wantStatus == http.StatusOK && p == "export/html" {
					// 成功路径导出的是 HTML，不是 JSON；只断言确有内容。
					assert.NotEmpty(t, w.Body.String())
					return
				}
				var got struct {
					Code string `json:"code"`
				}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got), "响应不是 JSON: %s", w.Body.String())
				assert.Equal(t, tc.wantCode, got.Code, "status=%d 时必须回 code=%q", tc.wantStatus, tc.wantCode)
			})
		}
	}
}
