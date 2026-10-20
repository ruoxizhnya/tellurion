package main

// REV-08（安全）回归护栏：插件族的**变更端点**仅 admin 可达，且路径逃逸 →
// 400（报错点名目录）。
//
// 这些测试打的是真实装配（registerPluginRoutes + auth.Service 中间件），
// 与 handlers_rbac_test.go 的 execution 端点同一套路：
//   - 去掉 admin 组 → viewer/trader 的 403 测试变 200/500 → 红；
//   - 去掉 ErrPluginPathEscape→400 映射 → 逃逸测试变 500 → 红；
//   - 把 watch-dir 留在全局认证 → viewer 改锚点的测试变 200 → 红。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/auth"
	"tellurion/pkg/strategy"
)

// rev08Router 起一个与生产同形的路由：全局 auth 中间件 + 插件族路由
// （admin 组），并返回一个已配置插件目录的 loader。
func rev08Router(t *testing.T) (*gin.Engine, *auth.Service) {
	t.Helper()
	svc := auth.NewService(nil, auth.Config{JWTSecret: []byte("test")})
	loader := strategy.NewPluginLoader(strategy.NewRegistry(), zerolog.Nop())
	require.NoError(t, loader.SetWatchDir(t.TempDir()))

	r := gin.New()
	r.Use(svc.Middleware())
	registerPluginRoutes(r, loader, svc)
	return r, svc
}

func rev08Post(t *testing.T, r *gin.Engine, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestPluginRoutes_ViewerForbidden：viewer 调 /api/plugins/load → 403
// （修复前：全局中间件只要求任意已认证角色，200/500 都会走到 plugin.Open）。
func TestPluginRoutes_ViewerForbidden(t *testing.T) {
	t.Parallel()
	r, svc := rev08Router(t)
	token := tokenFor(t, svc, auth.RoleViewer)

	w := rev08Post(t, r, "/api/plugins/load", `{"path":"x.so"}`, token)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}

// TestPluginRoutes_TraderForbidden：trader（非 admin）同样 403。
func TestPluginRoutes_TraderForbidden(t *testing.T) {
	t.Parallel()
	r, svc := rev08Router(t)
	token := tokenFor(t, svc, auth.RoleTrader)

	w := rev08Post(t, r, "/api/plugins/load", `{"path":"x.so"}`, token)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}

// TestPluginRoutes_WatchDirAdminOnly：watch-dir 是路径校验的锚点，必须与
// load 同级把关——viewer 改锚点 = 绕过前缀约束。
func TestPluginRoutes_WatchDirAdminOnly(t *testing.T) {
	t.Parallel()
	r, svc := rev08Router(t)
	token := tokenFor(t, svc, auth.RoleViewer)

	w := rev08Post(t, r, "/api/plugins/watch-dir", `{"dir":"/"}`, token)
	assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}

// TestPluginRoutes_AdminPathEscape_400：admin 提交 ../ 逃逸 → 400 且报错
// 点名配置目录（不是 500）。
func TestPluginRoutes_AdminPathEscape_400(t *testing.T) {
	t.Parallel()
	r, svc := rev08Router(t)
	token := tokenFor(t, svc, auth.RoleAdmin)

	w := rev08Post(t, r, "/api/plugins/load", `{"path":"../evil.so"}`, token)
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "escapes the configured plugin directory")
}

// TestPluginRoutes_AdminOutsideAbsolute_400：目录外绝对路径同样 400。
func TestPluginRoutes_AdminOutsideAbsolute_400(t *testing.T) {
	t.Parallel()
	r, svc := rev08Router(t)
	token := tokenFor(t, svc, auth.RoleAdmin)

	outside := filepath.Join(t.TempDir(), "evil.so")
	body, err := json.Marshal(map[string]string{"path": outside})
	require.NoError(t, err)
	w := rev08Post(t, r, "/api/plugins/load", string(body), token)
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

// TestPluginRoutes_AdminInsidePathPassesGates：admin + 目录内路径 ⇒ 通过
// 鉴权与路径两道关（后续失败于文件不存在 / 非法 .so，不是 403/400-escape）。
func TestPluginRoutes_AdminInsidePathPassesGates(t *testing.T) {
	t.Parallel()
	watch := t.TempDir()
	svc := auth.NewService(nil, auth.Config{JWTSecret: []byte("test")})
	loader := strategy.NewPluginLoader(strategy.NewRegistry(), zerolog.Nop())
	require.NoError(t, loader.SetWatchDir(watch))
	r := gin.New()
	r.Use(svc.Middleware())
	registerPluginRoutes(r, loader, svc)
	token := tokenFor(t, svc, auth.RoleAdmin)

	// 目录内用**绝对路径**：相对路径按进程 CWD 解析，天然落在目录之外
	// （那也是 REV-08 契约的一部分：只认解析后的绝对路径）。
	inside, err := json.Marshal(map[string]string{"path": filepath.Join(watch, "not-there.so")})
	require.NoError(t, err)
	w := rev08Post(t, r, "/api/plugins/load", string(inside), token)
	assert.NotEqual(t, http.StatusForbidden, w.Code, "admin 不得被 403")
	assert.NotEqual(t, http.StatusBadRequest, w.Code, "目录内路径不得被判逃逸: %s", w.Body.String())
}

// TestPluginRoutes_ViewerCanStillRead：读端点保持全局认证（任意已认证角色
// 可读元数据）——鉴权收紧不得误伤读路径。
func TestPluginRoutes_ViewerCanStillRead(t *testing.T) {
	t.Parallel()
	r, svc := rev08Router(t)
	token := tokenFor(t, svc, auth.RoleViewer)

	req := httptest.NewRequest(http.MethodGet, "/api/plugins", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
}
