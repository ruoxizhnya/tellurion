package main

// REV-17 守卫：data-service 的变更端点必须在策略表里逐条表态，且鉴权真的生效。
//
// 这个服务的写门此前是**完全裸奔**的：`buildRouter` 只有 Recovery/CORS/限流/
// 日志，`POST /api/ingest/raw`（证据链唯一写门）与 `POST /sync/ohlcv/all`
// （一次 ≈5900 只逐只调 Tushare）任何能连到 :8081 的人都能打。
//
// 三条腿：
//  1. 静态腿（CoversEveryMutatingRoute）：源码里的变更端点注册 ↔ 策略表双向对拍。
//     新加端点没表态 → 红；表里写了不存在的路由 → 也红。
//     **为什么不取 router.Routes()**：`registerRoutes` 会启动 worker pool
//     （`WorkerPool.Start` → `CleanupStaleRunning` 需要真 DB），拿真路由表就得
//     连库；本护栏要能在没有 DB 的机器上跑，所以退化为源码级对拍（组前缀 +
//     注册行两次扫描，见 sourceMutatingRoutes）。
//  2. 行为腿（RoleGates）：真中间件 + 真 JWT + 真策略表，挂最小 router——
//     证明「表真的被读、档位真的生效」。
//  3. 开发态腿：鉴权关闭时全放行（open-access 模式不变）。

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/auth"
	"tellurion/pkg/authz"
)

var (
	// api := r.Group("/api/sync") —— 组变量到前缀的映射
	groupRe = regexp.MustCompile(`(\w+)\s*:?=\s*\w+\.Group\("([^"]*)"\)`)
	// api.POST("/jobs", handler) —— 变更端点注册（相对组前缀）
	routeRe = regexp.MustCompile(`(\w+)\.(POST|PUT|PATCH|DELETE)\("([^"]*)"`)
)

// sourceMutatingRoutes 扫 cmd/data/*.go（非测试）里的变更端点，返回
// `"METHOD /full/path"` 集合。路径参数保留 `:id` 形态，与策略表同形。
func sourceMutatingRoutes(t *testing.T) map[string]bool {
	t.Helper()

	out := map[string]bool{}
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)

	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, readErr := os.ReadFile(f)
		require.NoError(t, readErr)
		src := string(raw)

		prefix := map[string]string{"r": "", "router": ""}
		for _, m := range groupRe.FindAllStringSubmatch(src, -1) {
			prefix[m[1]] = m[2]
		}
		for _, m := range routeRe.FindAllStringSubmatch(src, -1) {
			out[m[2]+" "+prefix[m[1]]+m[3]] = true
		}
	}
	return out
}

// TestDataRoutePolicy_CoversEveryMutatingRoute：源码注册 ↔ 策略表 双向对拍。
func TestDataRoutePolicy_CoversEveryMutatingRoute(t *testing.T) {
	t.Parallel()

	registered := sourceMutatingRoutes(t)
	require.NotEmpty(t, registered, "没扫到任何注册行——正则坏了，本测试会变成假绿")

	policy := dataRoutePolicy.Entries()
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
		"以下 data-service 变更端点没有在 cmd/data/route_policy.go 里表态：\n%s",
		strings.Join(missing, "\n"))
	assert.Empty(t, extra,
		"以下策略表条目在源码里找不到对应的注册行（端点了/改名了，表得跟着改）：\n%s",
		strings.Join(extra, "\n"))

	total := len(dataRoutePolicy.Public) + len(dataRoutePolicy.Admin) +
		len(dataRoutePolicy.Trader) + len(dataRoutePolicy.ReadOnlyPOST)
	assert.Equal(t, total, len(policy), "策略表里有重复条目（同一路由登记了两次）")
}

// TestDataRoutePolicy_SourceScanIsSane：静态腿的正则自检。
//
// 正则写歪时如果只扫到**部分**集合，静态腿会静默漏报（requre.NotEmpty 挡不住
// 「少扫了三条」）。这里钉住几个代表端点：带组前缀的、带路径参数的、根路径的。
func TestDataRoutePolicy_SourceScanIsSane(t *testing.T) {
	t.Parallel()

	got := sourceMutatingRoutes(t)
	for _, want := range []string{
		"POST /api/ingest/raw",                      // 根 router 注册
		"POST /sync/ohlcv/all",                      // 根 router 注册（多段路径）
		"POST /api/sync/schedules",                  // 组前缀 /api/sync
		"PUT /api/sync/schedules/:id",               // 组前缀 + 路径参数
		"DELETE /api/sync/schedules/:id",            // 同上，不同方法
		"POST /sync/index-constituents/:index_code", // 路径参数带下划线
	} {
		assert.True(t, got[want], "源码扫描应包含 %s", want)
	}
}

// policyTestRouter 用**真实策略表 + 真实中间件**挂一组桩路由。
//
// 为什么用桩而不是 registerRoutes：后者会启动 worker pool（需要真 DB）。
// 本腿要证明的是「策略表被读、档位生效」，桩路由足够；路由注册的完整性由
// 静态腿覆盖。
func policyTestRouter(svc *auth.Service) *gin.Engine {
	r := gin.New()
	if svc != nil && svc.Enabled() {
		r.Use(svc.Middleware())
	}
	r.Use(authz.Middleware(svc, dataRoutePolicy))

	stub := func(c *gin.Context) { c.Status(http.StatusOK) }
	// 取样覆盖四档：写门（trader）、排程（admin）、只读 POST、读端点。
	r.POST("/api/ingest/raw", stub)
	r.POST("/sync/ohlcv/all", stub)
	r.POST("/api/sync/jobs", stub)
	r.POST("/api/sync/schedules", stub)
	r.DELETE("/api/sync/schedules/:id", stub)
	r.POST("/screen", stub)
	r.GET("/api/sync/jobs", stub)
	return r
}

// TestDataRoutePolicy_RoleGates：无 token 401 / viewer 403 / trader 过闸 /
// 排程管理仅 admin / 只读 POST 放行 viewer。
func TestDataRoutePolicy_RoleGates(t *testing.T) {
	t.Parallel()

	svc := auth.NewService(nil, auth.Config{JWTSecret: []byte("rev17-data")})
	require.True(t, svc.Enabled())
	r := policyTestRouter(svc)

	viewer := dataToken(t, svc, auth.RoleViewer)
	trader := dataToken(t, svc, auth.RoleTrader)
	admin := dataToken(t, svc, auth.RoleAdmin)

	cases := []struct {
		name, method, path, token string
		want                      int
	}{
		{"证据链写门 无 token", http.MethodPost, "/api/ingest/raw", "", http.StatusUnauthorized},
		{"证据链写门 viewer", http.MethodPost, "/api/ingest/raw", viewer, http.StatusForbidden},
		{"证据链写门 trader", http.MethodPost, "/api/ingest/raw", trader, http.StatusOK},
		{"全市场同步 viewer", http.MethodPost, "/sync/ohlcv/all", viewer, http.StatusForbidden},
		{"全市场同步 trader", http.MethodPost, "/sync/ohlcv/all", trader, http.StatusOK},
		{"作业创建 trader", http.MethodPost, "/api/sync/jobs", trader, http.StatusOK},
		{"排程新增 trader=403（管理面）", http.MethodPost, "/api/sync/schedules", trader, http.StatusForbidden},
		{"排程新增 admin", http.MethodPost, "/api/sync/schedules", admin, http.StatusOK},
		{"排程删除 admin", http.MethodDelete, "/api/sync/schedules/7", admin, http.StatusOK},
		{"选股 viewer（只读 POST）", http.MethodPost, "/screen", viewer, http.StatusOK},
		{"读端点 viewer", http.MethodGet, "/api/sync/jobs", viewer, http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, dataDo(r, tc.method, tc.path, tc.token))
		})
	}
}

// TestDataRoutePolicy_AuthDisabledKeepsDevOpen：开发/CI 的 open-access 不变。
func TestDataRoutePolicy_AuthDisabledKeepsDevOpen(t *testing.T) {
	t.Parallel()

	r := policyTestRouter(nil)
	assert.Equal(t, http.StatusOK, dataDo(r, http.MethodPost, "/api/ingest/raw", ""),
		"鉴权关闭时必须放行（否则本地开发与 e2e 全废）")
}

func dataToken(t *testing.T, svc *auth.Service, role auth.Role) string {
	t.Helper()
	tok, _, err := svc.IssueTokens(&auth.User{ID: 1, Username: string(role), Role: role})
	require.NoError(t, err)
	return tok
}

func dataDo(r *gin.Engine, method, path, token string) int {
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

// TestDataRoutePolicy_ProductionRouterMountsPolicy：**装配腿**——`buildRouter`
// 真的挂上了认证 + 策略执法。
//
// 上面几个用例用测试自己拼的链路，只能证明"表能工作"；这一条证明"生产装配挂了它"。
// 若有人删掉 setup.go 里那两行 `router.Use(...)`，其余测试全绿而 :8081 裸奔。
func TestDataRoutePolicy_ProductionRouterMountsPolicy(t *testing.T) {
	t.Parallel()

	svc := auth.NewService(nil, auth.Config{JWTSecret: []byte("rev17-wiring")})
	r := buildRouter(svc)
	r.POST("/api/ingest/raw", func(c *gin.Context) { c.Status(http.StatusOK) })

	assert.Equal(t, http.StatusUnauthorized, dataDo(r, http.MethodPost, "/api/ingest/raw", ""),
		"buildRouter 必须挂 auth.Middleware（否则写门无 token 也能打）")
	viewer := dataToken(t, svc, auth.RoleViewer)
	trader := dataToken(t, svc, auth.RoleTrader)
	assert.Equal(t, http.StatusForbidden, dataDo(r, http.MethodPost, "/api/ingest/raw", viewer))
	assert.Equal(t, http.StatusOK, dataDo(r, http.MethodPost, "/api/ingest/raw", trader))
}
