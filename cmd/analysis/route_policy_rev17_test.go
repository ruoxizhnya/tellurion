package main

// REV-17 守卫：analysis 的变更端点必须在策略表里**逐条表态**，且执法与表一致。
//
// 两条腿：
//  1. 静态腿（TestRoutePolicy_CoversEveryMutatingRoute）：路由表 ≡ 策略表，
//     双向对拍。新加一个 POST/PUT/PATCH/DELETE 却没进表 → 红；表里写了不存在的
//     路由 → 也红（防表变成一厢情愿的清单）。
//  2. 行为腿（TestRoutePolicy_ViewerIsForbidden）：真 router + 真中间件 + 真
//     JWT，viewer 打变更端点得 403，"no policy entry" 这条 fail-closed 兜底不
//     会被触发。行为腿证明「表真的被读」，静态腿证明「表是完整的」——缺任何
//     一条都会退化成纸面规则。

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/auth"
	"tellurion/pkg/authz"
	"tellurion/pkg/bootstrap"
	"tellurion/pkg/strategy"
)

// policyRouter 装配**与生产一致**的路由 + 鉴权 + 策略执法（顺序：
// auth.Middleware → authz.Middleware → 业务路由）。
//
// 与 contractRouter 的区别：那个不挂鉴权（契约对拍不需要），这个必须挂——
// 本文件测的就是鉴权链本身。
func policyRouter(t *testing.T, svc *auth.Service) *gin.Engine {
	t.Helper()

	deps := newMinimalDeps()
	deps.PluginLoader = strategy.NewPluginLoader(strategy.NewRegistry(), zerolog.Nop())
	deps.AuthSvc = svc

	r := gin.New()
	r.Use(svc.Middleware())
	r.Use(authz.Middleware(svc, analysisRoutePolicy))
	registerRoutes(r, deps)
	registerAuxRoutes(r, nil, nil, zerolog.Nop())
	return r
}

// TestRoutePolicy_CoversEveryMutatingRoute：双向对拍。
func TestRoutePolicy_CoversEveryMutatingRoute(t *testing.T) {
	t.Parallel()

	// 路由表：用不挂鉴权的装配（它注册的集合与生产相同，只是少了中间件）。
	router := contractRouter(t)
	registered := map[string]bool{}
	for _, rt := range router.Routes() {
		if authz.IsMutating(rt.Method) {
			registered[rt.Method+" "+rt.Path] = true
		}
	}

	policy := analysisRoutePolicy.Entries()

	var missing, extra []string
	for k := range registered {
		if _, ok := policy[k]; !ok {
			missing = append(missing, k)
		}
	}
	for k := range policy {
		if !registered[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)

	assert.Empty(t, missing,
		"以下变更端点没有在 cmd/analysis/route_policy.go 里表态（新加端点必须决定谁能用）：\n%s",
		strings.Join(missing, "\n"))
	assert.Empty(t, extra,
		"以下策略表条目在路由表里不存在（端点了/改名了，表得跟着改）：\n%s",
		strings.Join(extra, "\n"))

	// 档位只能是三选一，且同一路由不得重复登记（重复 = 谁生效说不清）。
	for k, tier := range policy {
		assert.Contains(t, []string{"public", "admin", "trader", "readonly-post"}, tier,
			"%s 的档位非法：%s", k, tier)
	}
	total := len(analysisRoutePolicy.Public) + len(analysisRoutePolicy.Admin) +
		len(analysisRoutePolicy.Trader) + len(analysisRoutePolicy.ReadOnlyPOST)
	assert.Equal(t, total, len(policy), "策略表里有重复条目（同一路由登记了两次）")
}

// TestRoutePolicy_ReadRoutesAreNotInTable：读端点不该出现在变更策略表里
// ——它们由 auth.Middleware 的全局认证覆盖，本包不管（否则表会膨胀成路由副本）。
func TestRoutePolicy_ReadRoutesAreNotInTable(t *testing.T) {
	t.Parallel()

	for k := range analysisRoutePolicy.Entries() {
		method := strings.SplitN(k, " ", 2)[0]
		assert.True(t, authz.IsMutating(method), "%s 不是变更方法，不该进变更策略表", k)
	}
}

// TestRoutePolicy_ViewerIsForbidden：行为腿——viewer 打 trader/admin 端点必须 403。
//
// 取样覆盖四类：普通变更（batch）、管理面（plugin load）、执行（orders）、
// legacy 别名（/orders）。这些此前 viewer 都能打到（除 orders 已挂角色）。
func TestRoutePolicy_ViewerIsForbidden(t *testing.T) {
	t.Parallel()

	svc := auth.NewService(nil, auth.Config{JWTSecret: []byte("rev17-test")})
	require.True(t, svc.Enabled())
	r := policyRouter(t, svc)

	viewer := tokenFor(t, svc, auth.RoleViewer)
	trader := tokenFor(t, svc, auth.RoleTrader)
	admin := tokenFor(t, svc, auth.RoleAdmin)

	cases := []struct {
		name  string
		path  string
		body  string
		allow string // 谁可以通过鉴权层（不含 handler 的 4xx）
	}{
		{"批量回测（trader 档）", "/api/batch", `{"tasks":[]}`, string(auth.RoleTrader)},
		{"插件加载（admin 档）", "/api/plugins/load", `{"path":"x.so"}`, string(auth.RoleAdmin)},
		{"下单（trader 档）", "/api/execution/orders", `{}`, string(auth.RoleTrader)},
		{"legacy 下单（trader 档）", "/orders", `{}`, string(auth.RoleTrader)},
	}

	for _, tc := range cases {
		t.Run(tc.name+" viewer=403", func(t *testing.T) {
			w := policyPost(r, tc.path, tc.body, viewer)
			assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), "no authorization policy",
				"403 应该来自角色不足，而不是「策略表缺条目」")
		})
		t.Run(tc.name+" 允许角色不被鉴权层拦", func(t *testing.T) {
			var tok string
			switch tc.allow {
			case string(auth.RoleAdmin):
				tok = admin
			default:
				tok = trader
			}
			w := policyPost(r, tc.path, tc.body, tok)
			assert.NotEqual(t, http.StatusForbidden, w.Code,
				"角色足够的请求不该被鉴权层拒：%s", w.Body.String())
		})
	}

	// admin 也必须能过 trader 档（档位是「最低角色」而不是白名单）。
	t.Run("admin 通过 trader 档", func(t *testing.T) {
		w := policyPost(r, "/api/batch", `{"tasks":[]}`, admin)
		assert.NotEqual(t, http.StatusForbidden, w.Code)
	})
}

// TestRoutePolicy_PublicRoutesReachableWithoutToken：登录类入口不要求 token
// （auth.Middleware 的 publicPaths 白名单负责放行，策略表只声明档位）。
func TestRoutePolicy_PublicRoutesReachableWithoutToken(t *testing.T) {
	t.Parallel()

	svc := auth.NewService(nil, auth.Config{JWTSecret: []byte("rev17-test")})
	r := policyRouter(t, svc)

	for _, path := range []string{"/api/auth/login", "/api/auth/refresh", "/api/auth/bootstrap"} {
		w := policyPost(r, path, `{}`, "")
		assert.NotEqual(t, http.StatusUnauthorized, w.Code,
			"%s 必须无 token 可达（否则没人能拿到第一个 token）：%s", path, w.Body.String())
		assert.NotEqual(t, http.StatusForbidden, w.Code, path)
	}
}

// policyPost 发一个 JSON POST（可选 Bearer）。
func policyPost(r *gin.Engine, path, body, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestRoutePolicy_ProductionRouterMountsPolicy：**装配腿**——`buildRouter`
// 真的把 authz 中间件挂上了。
//
// 为什么必须有这一条：上面几个用例用的是测试自己拼的链路（policyRouter），
// 只能证明「表能工作」，不能证明「生产装配挂了它」。若有人删掉 setup.go 里
// 那行 `router.Use(authz.Middleware(...))`，其余测试全绿而线上裸奔。
//
// 为什么用**结构断言**而不是发请求：bootstrap.BuildRouter 还挂了
// AuditMiddleware，而它在 auth pool 为 nil 时会 panic（见
// auth_public_paths_test.go 的说明）——发一个变更请求就需要真库。装配这件事
// 本身与库无关，比 handler 链长度即可：buildRouter 必须比 bootstrap.BuildRouter
// 恰好多一个全局 handler。
func TestRoutePolicy_ProductionRouterMountsPolicy(t *testing.T) {
	t.Parallel()

	svc := auth.NewService(nil, auth.Config{JWTSecret: []byte("rev17-wiring")})
	v := viper.New()
	logger := zerolog.Nop()

	base := bootstrap.BuildRouter(svc, v, logger)
	withPolicy := buildRouter(svc, v, logger)

	assert.Equal(t, len(base.Handlers)+1, len(withPolicy.Handlers),
		"buildRouter 必须比 bootstrap.BuildRouter 多挂一个 authz.Middleware；"+
			"少了它，viewer 就能起批量回测、改策略、触发同步")
}
