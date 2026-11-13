package main

// api_index_test.go —— CORE-API-V1-INDEX 的契约面。
//
// 索引从 router.Routes() 生成，所以「不广告不存在的路径」由构造方式保证；
// 这里补三条生成方式保证不了的事：
//  1. 四类主面（backtest / sync / explore / copilot）真的出现在索引里；
//  2. legacy 兼容别名单独成段且逐条 deprecated；
//  3. 每条都有非空一句话。
// 破坏验证：删掉 `registerBacktestRoutes(...)` 一行 ⇒ 第 1 条红。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// indexKeys 把条目压成 "METHOD path"，便于集合断言。
func indexKeys(entries []apiIndexEntry) map[string]apiIndexEntry {
	out := make(map[string]apiIndexEntry, len(entries))
	for _, e := range entries {
		out[e.Method+" "+e.Path] = e
	}
	return out
}

func TestAPIV1IndexCoversMainSurface(t *testing.T) {
	t.Parallel()

	router := contractRouter(t)
	modern, legacy := buildAPIV1Index(router.Routes())
	got := indexKeys(modern)

	// 四类主面 + 探针/契约元端点：这些是机器读 /api/v1 时最该看到的。
	for _, key := range []string{
		"POST /api/backtest",
		"GET /api/backtest/:id/report",
		"ANY /api/sync/*path",
		"ANY /api/explore/*path",
		"ANY /api/copilot/*path",
		"ANY /api/pipeline/*path",
		"ANY /api/tools/*path",
		"GET /healthz",
		"GET /api/openapi.yaml",
		"POST /api/screen",
		"GET /api/ohlcv/:symbol",
	} {
		entry, ok := got[key]
		assert.True(t, ok, "主面端点 %q 未出现在 /api/v1 索引里", key)
		assert.NotEmpty(t, entry.Description, "%q 的描述不应为空", key)
		assert.False(t, entry.Deprecated, "%q 属于现代 API，不应标 deprecated", key)
	}

	// legacy 段：6 条 backtest 别名 + risk/execution 根路径必须全部 deprecated。
	legacyGot := indexKeys(legacy)
	for _, key := range []string{
		"GET /health", "GET /api/health",
		"GET /backtest", "POST /backtest",
		"GET /backtest/:id",
		"GET /backtest/:id/report",
		"GET /backtest/:id/trades",
		"GET /backtest/:id/equity",
		"POST /orders",
		"GET /positions",
		"POST /calculate_position",
		"GET /risk_metrics",
	} {
		entry, ok := legacyGot[key]
		require.True(t, ok, "legacy 别名 %q 未进入 legacy 段", key)
		assert.True(t, entry.Deprecated, "legacy 别名 %q 必须标 deprecated", key)
		assert.NotEmpty(t, entry.Description)
	}

	for _, e := range append(append([]apiIndexEntry{}, modern...), legacy...) {
		assert.NotEmpty(t, e.Description, "%s %s 缺少一句话描述", e.Method, e.Path)
	}
}

// TestAPIV1IndexIsSubsetOfRegisteredRoutes 钉住「索引 ⊆ 实际路由」：
// 索引里出现的每个 (method, path) 都必须能在路由表里找到，反之不要求
// （索引是主面自描述，不是全量路由 dump 的替代）。
func TestAPIV1IndexIsSubsetOfRegisteredRoutes(t *testing.T) {
	t.Parallel()

	router := contractRouter(t)
	registered := map[string]bool{}
	registeredPaths := map[string]bool{}
	for _, r := range router.Routes() {
		registered[r.Method+" "+r.Path] = true
		registeredPaths[r.Path] = true
	}
	modern, legacy := buildAPIV1Index(router.Routes())
	for _, e := range append(append([]apiIndexEntry{}, modern...), legacy...) {
		if e.Method == "ANY" {
			// gin 的 Any 在路由表里展开成 9 个方法，索引按族合并成一条。
			assert.True(t, registeredPaths[e.Path],
				"索引广告了未注册的 ANY %s", e.Path)
			continue
		}
		assert.True(t, registered[e.Method+" "+e.Path],
			"索引广告了未注册的 %s %s", e.Method, e.Path)
	}
}

// TestAPIV1HandlerShape 走真实 HTTP 路径：序列化字段名与分段结构不能变
// （UI / agent 机读）。
func TestAPIV1HandlerShape(t *testing.T) {
	t.Parallel()

	router := contractRouter(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1", nil)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var body struct {
		Service   string          `json:"service"`
		Version   string          `json:"version"`
		Source    string          `json:"source"`
		Endpoints []apiIndexEntry `json:"endpoints"`
		Legacy    []apiIndexEntry `json:"legacy"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "analysis-service", body.Service)
	assert.NotEmpty(t, body.Version)
	assert.Equal(t, "router.Routes()", body.Source)
	assert.NotEmpty(t, body.Endpoints)
	assert.NotEmpty(t, body.Legacy)
	assert.Less(t, len(body.Endpoints), 1000, "索引规模异常 —— 生成口径可能把方法展开写炸了")
}
