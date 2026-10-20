package authz

// REV-17 单测：策略表的档位语义 + Middleware 的执法行为。
//
// 这一层是被两个服务的守卫测试引用的公共部件，所以它自己的语义必须钉死：
// 档位错了，两个服务的矩阵测试都会「绿得毫无意义」。

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/auth"
)

func testPolicy() Policy {
	return Policy{
		Public:       []string{"POST /api/auth/login"},
		Admin:        []string{"POST /api/admin/thing"},
		Trader:       []string{"POST /api/orders", "DELETE /api/orders/:id"},
		ReadOnlyPOST: []string{"POST /api/screen"},
	}
}

// testRouter 造一个「鉴权 + 执法 + 若干路由」的最小 router。
func testRouter(svc *auth.Service, p Policy) *gin.Engine {
	r := gin.New()
	r.Use(svc.Middleware())
	r.Use(Middleware(svc, p))
	register := func(method, path string) {
		r.Handle(method, path, func(c *gin.Context) { c.Status(http.StatusOK) })
	}
	register(http.MethodPost, "/api/auth/login")
	register(http.MethodPost, "/api/admin/thing")
	register(http.MethodPost, "/api/orders")
	register(http.MethodDelete, "/api/orders/:id")
	register(http.MethodPost, "/api/screen")
	register(http.MethodGet, "/api/orders")
	// 故意注册一条**不在策略表里**的变更端点，用来验 fail-closed。
	register(http.MethodPost, "/api/unclassified")
	return r
}

func enabledService(t *testing.T) *auth.Service {
	t.Helper()
	svc := auth.NewService(nil, auth.Config{JWTSecret: []byte("authz-test")})
	require.True(t, svc.Enabled())
	return svc
}

func tokenForRole(t *testing.T, svc *auth.Service, role auth.Role) string {
	t.Helper()
	tok, _, err := svc.IssueTokens(&auth.User{ID: 1, Username: string(role), Role: role})
	require.NoError(t, err)
	return tok
}

func do(r *gin.Engine, method, path, token string) int {
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

// TestMiddleware_AuthDisabledIsNoOp：dev/CI 的 open-access 模式必须不变。
func TestMiddleware_AuthDisabledIsNoOp(t *testing.T) {
	t.Parallel()

	disabled := auth.NewService(nil, auth.Config{})
	require.False(t, disabled.Enabled())
	r := testRouter(disabled, testPolicy())

	assert.Equal(t, http.StatusOK, do(r, http.MethodPost, "/api/orders", ""))
	assert.Equal(t, http.StatusOK, do(r, http.MethodPost, "/api/unclassified", ""),
		"鉴权关闭时连「表里没有」也不该拦——否则开发态全废")
	// nil 服务同样 no-op（data-service 的测试直接传 nil）。
	r2 := gin.New()
	r2.Use(Middleware(nil, testPolicy()))
	r2.POST("/api/orders", func(c *gin.Context) { c.Status(http.StatusOK) })
	assert.Equal(t, http.StatusOK, do(r2, http.MethodPost, "/api/orders", ""))
}

// TestMiddleware_RoleTiers：四档语义逐条钉住。
func TestMiddleware_RoleTiers(t *testing.T) {
	t.Parallel()

	svc := enabledService(t)
	r := testRouter(svc, testPolicy())
	viewer := tokenForRole(t, svc, auth.RoleViewer)
	trader := tokenForRole(t, svc, auth.RoleTrader)
	admin := tokenForRole(t, svc, auth.RoleAdmin)

	cases := []struct {
		name   string
		method string
		path   string
		token  string
		want   int
	}{
		{"读端点无需角色（只要认证）", http.MethodGet, "/api/orders", viewer, http.StatusOK},
		{"Public 变更端点放行", http.MethodPost, "/api/auth/login", "", http.StatusOK},
		{"Trader 档：viewer 拒绝", http.MethodPost, "/api/orders", viewer, http.StatusForbidden},
		{"Trader 档：trader 通过", http.MethodPost, "/api/orders", trader, http.StatusOK},
		{"Trader 档：admin 也通过（最低角色而非白名单）", http.MethodPost, "/api/orders", admin, http.StatusOK},
		{"路径参数按模板匹配", http.MethodDelete, "/api/orders/42", trader, http.StatusOK},
		{"Admin 档：trader 拒绝", http.MethodPost, "/api/admin/thing", trader, http.StatusForbidden},
		{"Admin 档：admin 通过", http.MethodPost, "/api/admin/thing", admin, http.StatusOK},
		{"ReadOnlyPOST：viewer 通过", http.MethodPost, "/api/screen", viewer, http.StatusOK},
		{"ReadOnlyPOST：无 token 401", http.MethodPost, "/api/screen", "", http.StatusUnauthorized},
		{"变更端点无 token 401", http.MethodPost, "/api/orders", "", http.StatusUnauthorized},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, do(r, tc.method, tc.path, tc.token))
		})
	}
}

// TestMiddleware_UnknownMutatingRouteFailsClosed：策略表缺条目 → 403 并点名，
// 绝不静默放行。这是守卫测试之外的第二层兜底。
func TestMiddleware_UnknownMutatingRouteFailsClosed(t *testing.T) {
	t.Parallel()

	svc := enabledService(t)
	r := testRouter(svc, testPolicy())
	admin := tokenForRole(t, svc, auth.RoleAdmin)

	req := httptest.NewRequest(http.MethodPost, "/api/unclassified", nil)
	req.Header.Set("Authorization", "Bearer "+admin)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code, "连 admin 也不该放过没表态的端点")
	assert.Contains(t, w.Body.String(), "no authorization policy")
}

// TestPolicy_Role：档位查询本身（Entires 与 Role 必须一致）。
func TestPolicy_Role(t *testing.T) {
	t.Parallel()

	p := testPolicy()

	if role, ok := p.Role("POST /api/auth/login"); !ok || role != "" {
		t.Fatalf("Public 档应返回空角色：role=%q ok=%v", role, ok)
	}
	if role, ok := p.Role("POST /api/admin/thing"); !ok || role != auth.RoleAdmin {
		t.Fatalf("Admin 档错误：role=%q ok=%v", role, ok)
	}
	if role, ok := p.Role("POST /api/orders"); !ok || role != auth.RoleTrader {
		t.Fatalf("Trader 档错误：role=%q ok=%v", role, ok)
	}
	if role, ok := p.Role("POST /api/screen"); !ok || role != RoleAnyAuthenticated() {
		t.Fatalf("ReadOnlyPOST 档错误：role=%q ok=%v", role, ok)
	}
	if _, ok := p.Role("POST /api/nope"); ok {
		t.Fatal("未登记的路由不该返回 ok")
	}

	assert.Len(t, p.Entries(), 5)
	assert.Equal(t, "readonly-post", p.Entries()["POST /api/screen"])
}

// TestIsMutating：HEAD/OPTIONS/TRACE 不算变更（gin 的 Any 会一并注册它们）。
func TestIsMutating(t *testing.T) {
	t.Parallel()

	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		assert.True(t, IsMutating(m), m)
	}
	for _, m := range []string{"GET", "HEAD", "OPTIONS", "TRACE"} {
		assert.False(t, IsMutating(m), m)
	}
}
