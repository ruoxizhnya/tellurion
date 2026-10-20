package main

// REV-23(1)(8)(9)：HTTP 响应出口类修复的行为测试。
//
// 覆盖三件事：
//  1. 5xx 响应体不含内部错误原文（emergency-flatten 与 alerts force-check
//     两个 handler，fake trader 注入带可识别标记的内部错误）；
//  2. batch CSV 导出不再谎报 content-type（该分支如实回 501）；
//  3. 插件 load-all 的状态码与结果挂钩（全失败 → 500，不再是恒 200）。
//
// SSE 流与 calendar handler 的同型纪律由 cmd/data 的源码守卫执法
// （handlers_sync_rev23_test.go）——那两处的失败注入需要真 DB/Tushare
// 依赖，行为测试的成本高于守卫收益。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/alert"
	"tellurion/pkg/backtest"
	"tellurion/pkg/domain"
	"tellurion/pkg/live"
	"tellurion/pkg/strategy"
)

// rev23LeakMarker 是 fake trader 返回的内部错误标记。断言要求它**不**
// 出现在任何 5xx 响应体里——若有人把出口改回 err.Error() 原文回传，
// 本标记就会泄漏进 body，测试变红（破坏验证：把 (1) 的修复还原成
// "detail": err.Error() 即红）。
const rev23LeakMarker = "pgx-scan secret-dsn postgres://user:hunter2@db.internal/prod"

// rev23FailingTrader 是最小 LiveTrader：EmergencyFlatten / GetPositions
// 恒返回带 rev23LeakMarker 的错误；其余方法不应被这些测试触到（触到即
// panic，比静默返回零值更容易暴露测试自身的越界）。
type rev23FailingTrader struct {
	mu sync.Mutex
}

func (t *rev23FailingTrader) SubmitOrder(_ context.Context, _ string, _ domain.Direction, _ domain.OrderType, _ float64, _ float64) (*live.OrderResult, error) {
	panic("rev23FailingTrader: SubmitOrder not expected in these tests")
}
func (t *rev23FailingTrader) CancelOrder(_ context.Context, _ string) error {
	panic("rev23FailingTrader: CancelOrder not expected in these tests")
}
func (t *rev23FailingTrader) GetOrder(_ context.Context, _ string) (*live.OrderResult, error) {
	panic("rev23FailingTrader: GetOrder not expected in these tests")
}
func (t *rev23FailingTrader) GetPositions(_ context.Context) ([]live.PositionInfo, error) {
	return nil, errors.New(rev23LeakMarker)
}
func (t *rev23FailingTrader) GetAccount(_ context.Context) (*live.AccountInfo, error) {
	panic("rev23FailingTrader: GetAccount not expected in these tests")
}
func (t *rev23FailingTrader) Name() string { return "rev23_failing" }
func (t *rev23FailingTrader) HealthCheck(_ context.Context) error {
	return errors.New(rev23LeakMarker)
}
func (t *rev23FailingTrader) EmergencyFlatten(_ context.Context, _ string) (*live.EmergencyFlattenResult, error) {
	return nil, errors.New(rev23LeakMarker)
}

var _ live.LiveTrader = (*rev23FailingTrader)(nil)

// TestEmergencyFlatten_5xxBodyHasNoInternalError 钉住 (1a)：kill-switch
// 失败时响应体只有静态文案，trader 的内部错误原文不进 body。
func TestEmergencyFlatten_5xxBodyHasNoInternalError(t *testing.T) {

	h := NewExecutionHandler(&rev23FailingTrader{}, zerolog.New(nil), "kill-token")
	r := gin.New()
	h.RegisterRoutes(r)

	body := `{"confirmation_token":"kill-token","reason":"test"}`
	req := httptest.NewRequest(http.MethodPost, "/api/execution/emergency-flatten", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer kill-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	assert.NotContains(t, w.Body.String(), rev23LeakMarker,
		"5xx 响应体不得包含内部错误原文（REV-23(1)）")
	assert.Contains(t, w.Body.String(), "trader failed to flatten")
}

// TestAlertForceCheck_5xxBodyHasNoInternalError 钉住 (1b)：评估失败时
// 响应体只有静态文案。失败注入点 = buildSnapshot 的 GetPositions。
func TestAlertForceCheck_5xxBodyHasNoInternalError(t *testing.T) {

	loop := NewPeriodicAlertLoop(
		PeriodicAlertConfig{Enabled: true, Interval: time.Minute, HistoryLimit: 10},
		alert.NewAlertManager(alert.AlertManagerConfig{}, zerolog.New(nil)),
		&rev23FailingTrader{}, nil, nil, zerolog.New(nil),
	)

	r := gin.New()
	registerAlertRoutes(r, loop)

	req := httptest.NewRequest(http.MethodPost, "/api/alerts/force-check", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	assert.NotContains(t, w.Body.String(), rev23LeakMarker,
		"5xx 响应体不得包含内部错误原文（REV-23(1)）")
	assert.Contains(t, w.Body.String(), "alert evaluation failed")
}

// TestBatchCSVExport_501NoCSVContentType 钉住 (8)：CSV 分支不再回
// 200 + text/csv 头 + JSON 体，而是如实 501。
func TestBatchCSVExport_501NoCSVContentType(t *testing.T) {

	runner := &rev11Runner{}
	be := backtest.NewBatchEngine(runner, nil, backtest.DefaultBatchConfig(), zerolog.Nop())
	r := gin.New()
	registerBatchRoutes(r, be, zerolog.Nop())

	// 先走真实 Run 路径造一份已存在的 batch report（CSV 分支在
	// GetReport 成功之后才可达）。
	req := httptest.NewRequest(http.MethodPost, "/api/batch", strings.NewReader(rev11Body(t, 1, 1)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var runResp struct {
		BatchID string `json:"batch_id"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &runResp))
	require.NotEmpty(t, runResp.BatchID)

	// CSV 导出：期望 501，且响应头不是 text/csv。
	req = httptest.NewRequest(http.MethodGet, "/api/batch/"+runResp.BatchID+"/export/csv", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotImplemented, w.Code)
	assert.NotContains(t, w.Header().Get("Content-Type"), "text/csv",
		"501 腿不得谎报 text/csv（REV-23(8)）")
}

// TestPluginLoadAll_StatusTracksResult 钉住 (9)：全失败 → 500 且 body
// 不含插件错误原文；空目录（loaded=0, errs=0）→ 200。
// （207 部分失败腿需要一个可加载的真插件 fixture，成本高于其逻辑
// 复杂度——switch 三分支中其余两腿已测，207 分支靠代码走查。）
func TestPluginLoadAll_StatusTracksResult(t *testing.T) {

	t.Run("all failures return 500 without leaking cause", func(t *testing.T) {
		loader := strategy.NewPluginLoader(strategy.NewRegistry(), zerolog.Nop())
		watch := t.TempDir()
		require.NoError(t, loader.SetWatchDir(watch))
		// 目录里放一个假 .so：plugin.Open 在加载阶段失败 ⇒
		// LoadAll 返回 errs>0、loaded=0 ⇒ 500。
		broken := filepath.Join(watch, "broken.so")
		require.NoError(t, os.WriteFile(broken, []byte("not a real plugin"), 0o644))

		r := gin.New()
		registerPluginRoutes(r, loader, nil) // nil svc：不挂 RBAC（rev08 已覆盖鉴权腿）

		req := httptest.NewRequest(http.MethodPost, "/api/plugins/load-all", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		require.Equal(t, http.StatusInternalServerError, w.Code)
		assert.NotContains(t, w.Body.String(), "not a real plugin",
			"全失败 500 腿不回传插件错误原文（REV-23(9)）")
		assert.Contains(t, w.Body.String(), "failed to load any plugin")
	})

	t.Run("empty directory still 200", func(t *testing.T) {
		loader := strategy.NewPluginLoader(strategy.NewRegistry(), zerolog.Nop())
		require.NoError(t, loader.SetWatchDir(t.TempDir()))

		r := gin.New()
		registerPluginRoutes(r, loader, nil)

		req := httptest.NewRequest(http.MethodPost, "/api/plugins/load-all", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		require.Equal(t, http.StatusOK, w.Code)
	})
}
