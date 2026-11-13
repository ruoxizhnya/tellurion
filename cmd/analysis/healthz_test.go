package main

// healthz_test.go —— X-PROBE-CANONICAL-HEALTHZ 的 core 腿。
//
// 三条事实必须同时成立：
//  1. `/healthz` 返回 200，字段与 `/health` 同构（status + service + timestamp）；
//  2. `/health` 与 `/api/health` 仍然可用（滚动升级期间旧编排还在打它们），
//     但带 `Deprecation: true` + Warning 头 —— 兼容不是静默的；
//  3. 探针不走限流（豁免名单的硬编码断言在 rate_limit_exempt_test.go）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func getProbe(t *testing.T, path string) (*httptest.ResponseRecorder, map[string]string) {
	t.Helper()
	router := contractRouter(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	router.ServeHTTP(w, req)

	var body map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return w, body
}

func TestHealthzIsCanonicalProbe(t *testing.T) {
	t.Parallel()

	canonicalResp, canonical := getProbe(t, "/healthz")
	require.Equal(t, http.StatusOK, canonicalResp.Code)
	assert.Equal(t, "healthy", canonical["status"])
	assert.Equal(t, "analysis-service", canonical["service"])
	assert.NotEmpty(t, canonical["timestamp"])
	assert.Empty(t, canonicalResp.Header().Get("Deprecation"), "规范探针不应被标 deprecated")

	// 别名与规范名的 body 同构：同样三个键，status/service 取值一致。
	for _, alias := range []string{"/health", "/api/health"} {
		resp, body := getProbe(t, alias)
		require.Equal(t, http.StatusOK, resp.Code, "%s 在兼容期内必须仍可用", alias)
		assert.Equal(t, canonical["status"], body["status"])
		assert.Equal(t, canonical["service"], body["service"])
		assert.NotEmpty(t, body["timestamp"])
		assert.Len(t, body, len(canonical), "%s 与 /healthz 的字段集合必须同构", alias)

		assert.Equal(t, "true", resp.Header().Get("Deprecation"),
			"%s 是 deprecated 别名，必须显式标注而不是静默保留", alias)
		assert.Contains(t, resp.Header().Get("Warning"), "/healthz",
			"%s 的 Warning 要指向规范名", alias)
	}
}
