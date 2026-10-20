package main

// REV-23(2)(3)(11)：map 与后台组件类修复的测试。
//
//   - (2) orders 缓存封顶：超限按 FIFO 逐出最旧，缓存不再只增不消；
//   - (11) alertLoop 的 cancel 装配：Start 对 ctx cancel 的退出语义已有
//     TestPeriodicAlertLoop_Start_StopsOnContextCancel 钉住，这里补的是
//     main.go 装配腿的守卫——alertLoop 必须收 cancel-able ctx，且
//     shutdown 段必须调用 stopAlert（代码走查的自动化形式）。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExecutionHandler_OrdersCacheEvictsOldest 钉住 (2)：下单超过
// maxCachedOrders 后，缓存条数封顶且最早的单被逐出（getOrder 走缓存
// miss 会回落 trader——这里只断言缓存行为本身）。
func TestExecutionHandler_OrdersCacheEvictsOldest(t *testing.T) {
	eh := newTestExecutionHandler(t)
	r := gin.New()
	eh.RegisterRoutes(r)

	postOrder := func() string {
		body, _ := json.Marshal(map[string]interface{}{
			"symbol": "000001.SZ", "side": "long", "type": "market",
			// 1 股 × 10 元：1002 单占用 ~1 万现金，远低于 MockTrader 的
			// 100 万初始资金——避免_cash 不足把测试变成下单失败测试。
			"quantity": 1, "price": 10.0,
		})
		req := httptest.NewRequest(http.MethodPost, "/api/execution/orders", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
		var created struct {
			OrderID string `json:"order_id"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
		require.NotEmpty(t, created.OrderID)
		return created.OrderID
	}

	// 前 maxCachedOrders+2 单，记下最旧的两单 ID。
	total := maxCachedOrders + 2
	ids := make([]string, 0, total)
	for i := 0; i < total; i++ {
		ids = append(ids, postOrder())
	}

	eh.mu.RLock()
	size := len(eh.orders)
	_, oldestStillCached := eh.orders[ids[0]]
	_, secondStillCached := eh.orders[ids[1]]
	_, newestCached := eh.orders[ids[total-1]]
	eh.mu.RUnlock()

	assert.Equal(t, maxCachedOrders, size,
		"orders 缓存必须封顶在 maxCachedOrders（REV-23(2)）")
	assert.False(t, oldestStillCached, "最旧的单应被 FIFO 逐出")
	assert.False(t, secondStillCached, "次旧的单应被 FIFO 逐出")
	assert.True(t, newestCached, "最新的单必须在缓存内")
}

// TestREV23_MainWiresAlertLoopCancellation 钉住 (11) 的装配腿：main.go
// 必须给 alertLoop 一个 cancel-able ctx（WithCancel），且 shutdown 段
// 调用 stopAlert。破坏验证：删掉 stopAlert() 调用 → 本守卫红。
func TestREV23_MainWiresAlertLoopCancellation(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("main.go")
	require.NoError(t, err)
	src := string(raw)

	assert.Contains(t, src, "alertCtx, stopAlert := context.WithCancel(",
		"alertLoop 必须收 cancel-able ctx（REV-23(11)），不得直接 Background")
	assert.Contains(t, src, "stopAlert()",
		"shutdown 段必须调用 stopAlert()（与 stopRecon 相邻，REV-23(11)）")
}
