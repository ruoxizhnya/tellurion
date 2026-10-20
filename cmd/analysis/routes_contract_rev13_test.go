package main

// REV-13 契约护栏：cmd/analysis 注册的每条路由 ↔ docs/openapi.yaml 的每个
// path+method，双向对拍。
//
// 为什么需要它：在此之前 openapi 只有「spec 自身可解析 / $ref 可解析 / 有
// responses」三条自检，**没有任何地方把 yaml 和真实路由放在一起看**。结果是
// 双向漂移都没人管——代码里在跑的端点从未登记（/api/health、
// /api/factors/:factor_name、对账 4 条、explore/tools 反代族），yaml 里写着
// 的端点早已不在（退役的 /api/v1/trading/calendar，以及只在 :8081 注册的
// ingest 两条被错记在 :8085 的 servers 下）。照 Swagger UI 打这些路径，
// 一边必 404、一边永远找不到。
//
// 三条设计约束（都有测试钉住）：
//  1. **双向**：代码多一条 → 红；yaml 多一条 → 也红（除非标了宿主或进豁免）。
//  2. **豁免必须有理由且数量固定**：`legacyAliases` / `metaEndpoints` /
//     `proxyFamilies` 三张表的条数与理由由 TestRouteContract_ExemptionTables
//     断言——想靠往表里加一行吞掉真漂移，得先改断言，改不动就是滥用。
//  3. **反代族按族豁免**：gin 的 `Any("/api/x/*path")` 一次注册 9 个方法，
//     yaml 无法逐个方法描述，也没法枚举上游的真实子路径。族级豁免 + yaml 里
//     的族条目（/api/explore、/api/tools、/api/sync/{path}）是这一族的诚实
//     表达方式。

import (
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"tellurion/docs"
	"tellurion/pkg/strategy"
)

// ─── 契约快照 ────────────────────────────────────────────────────────

// routeKey 是「METHOD 路径」的规范键（与 openapi 的方法大小写对齐）。
type routeKey = string

// normalizeGinPath 把 gin 的参数写法换成 OpenAPI 的：`:id` / `*path` → `{id}` / `{path}`。
func normalizeGinPath(p string) string {
	parts := strings.Split(p, "/")
	for i, seg := range parts {
		if strings.HasPrefix(seg, ":") || strings.HasPrefix(seg, "*") {
			parts[i] = "{" + seg[1:] + "}"
		}
	}
	return strings.Join(parts, "/")
}

// contractRouter 装配**与生产一致**的路由表：registerRoutes（主表面）+
// registerAuxRoutes（alerts / reconciliation 第二批接线）。
//
// deps 用 newMinimalDeps（deps_test.go）：所有 handler 的注册期只捕获闭包，
// 服务在请求期才解引用，所以 nil 服务不会 panic。两个例外必须显式给：
//   - PluginLoader 为 nil 时 registerPluginRoutes 整族不注册（会给护栏一份
//     假的「代码里没有插件端点」快照），故给一个空 loader；
//   - alerts / reconciliation 由 registerAuxRoutes 统一接（main 走同一入口）。
func contractRouter(t *testing.T) *gin.Engine {
	t.Helper()

	deps := newMinimalDeps()
	deps.PluginLoader = strategy.NewPluginLoader(strategy.NewRegistry(), zerolog.Nop())

	router := gin.New()
	registerRoutes(router, deps)
	registerAuxRoutes(router, nil, nil, zerolog.Nop())
	return router
}

// registeredRoutes 返回代码侧的全部「METHOD 路径」。
func registeredRoutes(t *testing.T) map[routeKey]bool {
	t.Helper()
	out := map[routeKey]bool{}
	for _, r := range contractRouter(t).Routes() {
		out[r.Method+" "+normalizeGinPath(r.Path)] = true
	}
	return out
}

// documentedRoutes 解析嵌入的 spec，返回代码宿主（默认 servers）上的
// 「METHOD 路径」，外加标了别的宿主的路径（x-served-by）。
func documentedRoutes(t *testing.T) (onHost map[routeKey]bool, otherHost map[string]string) {
	t.Helper()

	var spec struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	require.NoError(t, yaml.Unmarshal(docs.OpenAPISpec, &spec), "docs/openapi.yaml 必须可解析")

	onHost = map[routeKey]bool{}
	otherHost = map[string]string{}
	httpMethods := map[string]bool{
		"get": true, "post": true, "put": true, "patch": true,
		"delete": true, "head": true, "options": true, "trace": true,
	}

	for path, ops := range spec.Paths {
		var hostTag string
		for method, node := range ops {
			if !httpMethods[method] {
				continue
			}
			if node.Kind == yaml.MappingNode {
				for i := 0; i+1 < len(node.Content); i += 2 {
					if node.Content[i].Value == "x-served-by" {
						hostTag = node.Content[i+1].Value
					}
				}
			}
			key := strings.ToUpper(method) + " " + path
			if hostTag != "" {
				otherHost[key] = hostTag
				continue
			}
			onHost[key] = true
		}
	}
	return onHost, otherHost
}

// wildcardPrefix 返回 `/a/b/{path}` 形式的通配前缀 `/a/b`；不是通配则返回 ""。
func wildcardPrefix(path string) string {
	idx := strings.Index(path, "/{path}")
	if idx < 0 {
		return ""
	}
	return path[:idx]
}

// coveredByWildcard 判断 (method, path) 是否被某条已注册的通配路由覆盖。
func coveredByWildcard(routes map[routeKey]bool, method, path string) bool {
	for key := range routes {
		sp := strings.SplitN(key, " ", 2)
		if len(sp) != 2 || sp[0] != method {
			continue
		}
		prefix := wildcardPrefix(sp[1])
		if prefix == "" {
			continue
		}
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

// ─── 豁免表（每条都有理由；条数有断言）────────────────────────────────

// legacyAlias 是根路径上的历史别名（AUD-02 / AUD-33 保留的兼容层）。
type legacyAlias struct {
	method string
	path   string
	reason string
}

// legacyAliases 逐条列出，**不用前缀规则**——前缀规则会把将来的新漂移一起吞掉。
var legacyAliases = []legacyAlias{
	{"GET", "/account", "AUD-02 legacy alias of GET /api/execution/account（前端旧版与运维脚本仍在用）"},
	{"GET", "/positions", "AUD-02 legacy alias of GET /api/execution/positions"},
	{"GET", "/orders", "AUD-02 legacy alias of GET /api/execution/orders"},
	{"GET", "/orders/{id}", "AUD-02 legacy alias of GET /api/execution/orders/:id"},
	{"POST", "/orders", "AUD-02 legacy alias of POST /api/execution/orders（与 /api 版同一 RequireRole 闸门）"},
	{"POST", "/orders/{id}/cancel", "AUD-02 legacy alias of POST /api/execution/orders/:id/cancel"},
	{"GET", "/risk_metrics", "AUD-02 legacy alias of GET /api/risk/metrics"},
	{"POST", "/calculate_position", "AUD-02 legacy alias of POST /api/risk/calculate_position"},
	{"POST", "/check_stoploss", "AUD-02 legacy alias of POST /api/risk/check_stoploss"},
	{"POST", "/detect_regime", "AUD-02 legacy alias of POST /api/risk/detect_regime"},
	{"GET", "/backtest", "AUD-33 legacy alias of GET /api/backtest（HTML UI 已退役，别名保留一版）"},
	{"POST", "/backtest", "AUD-33 legacy alias of POST /api/backtest"},
	{"GET", "/backtest/{id}", "AUD-33 legacy alias of GET /api/backtest/:id"},
	{"GET", "/backtest/{id}/report", "AUD-33 legacy alias of GET /api/backtest/:id/report"},
	{"GET", "/backtest/{id}/trades", "AUD-33 legacy alias of GET /api/backtest/:id/trades"},
	{"GET", "/backtest/{id}/equity", "AUD-33 legacy alias of GET /api/backtest/:id/equity"},
}

// metaEndpoints 是服务自身的元端点（不是业务 API，不进契约）。
var metaEndpoints = []legacyAlias{
	{"GET", "/api/docs", "Swagger UI 页面本身——文档的载体，不是被文档化的 API"},
	{"GET", "/api/openapi.yaml", "spec 的下载地址——同上，自指条目没有契约价值"},
}

// proxyFamily 是一族反代通配路由：代码用 gin.Any 注册 9 个方法，上游子路径
// 不可枚举，yaml 用族条目（如 /api/sync/{path}）表达。
type proxyFamily struct {
	prefix string
	reason string
}

var proxyFamilies = []proxyFamily{
	{"/api/copilot", "ai-service 反代族（ADR-027 §5 阶段 1）：Any 通配，子路径由上游 copilot 决定"},
	{"/api/pipeline", "ai-service 反代族：Any 通配，子路径由上游 pipeline 决定"},
	{"/api/explore", "ai-service 反代族：Any 通配，探索观察台的子路径由上游决定"},
	{"/api/tools", "ai-service 反代族：Any 通配，工具注册表的子路径由上游决定"},
	{"/api/sync", "data-service 网关族：Any 通配 + SSE 直通，/sync/* 全部端点与 job 进度流都走这里"},
}

// exemptCodeRoute 判断一条代码路由是否落在豁免规则内，返回命中的理由。
func exemptCodeRoute(method, path string) (string, bool) {
	for _, a := range legacyAliases {
		if a.method == method && a.path == path {
			return a.reason, true
		}
	}
	for _, m := range metaEndpoints {
		if m.method == method && m.path == path {
			return m.reason, true
		}
	}
	for _, f := range proxyFamilies {
		if path == f.prefix || strings.HasPrefix(path, f.prefix+"/") {
			return f.reason, true
		}
	}
	return "", false
}

// ─── 断言 ────────────────────────────────────────────────────────────

// TestRouteContract_CodeToSpec：代码里注册的每条路由，必须在 yaml 里有对应
// 条目（或命中带理由的豁免）。
func TestRouteContract_CodeToSpec(t *testing.T) {
	t.Parallel()

	routes := registeredRoutes(t)
	documented, _ := documentedRoutes(t)

	var missing []string
	for key := range routes {
		if documented[key] {
			continue
		}
		sp := strings.SplitN(key, " ", 2)
		if _, ok := exemptCodeRoute(sp[0], sp[1]); ok {
			continue
		}
		missing = append(missing, key)
	}
	sort.Strings(missing)

	assert.Empty(t, missing,
		"以下路由已在代码中注册，但 docs/openapi.yaml 没有登记，且不在豁免清单里。\n"+
			"处理方式：补登记到 yaml；确属兼容别名/元端点则加进对应豁免表并写明理由。\n%s",
		strings.Join(missing, "\n"))
}

// TestRouteContract_SpecToCode：yaml 里的每个 path+method，必须在代码里真的
// 注册（或标了别的宿主 / 被反代族覆盖）。
func TestRouteContract_SpecToCode(t *testing.T) {
	t.Parallel()

	routes := registeredRoutes(t)
	documented, otherHost := documentedRoutes(t)

	var ghosts []string
	for key := range documented {
		if routes[key] {
			continue
		}
		sp := strings.SplitN(key, " ", 2)
		if coveredByWildcard(routes, sp[0], sp[1]) {
			continue
		}
		ghosts = append(ghosts, key)
	}
	sort.Strings(ghosts)

	assert.Empty(t, ghosts,
		"以下条目在 docs/openapi.yaml 里登记了，但代码里没有对应路由（幽灵端点）。\n"+
			"处理方式：退役的删掉；属于别的服务的加 `x-served-by: <host>`；"+
			"经反代族转发的会被通配路由自动覆盖。\n%s",
		strings.Join(ghosts, "\n"))

	// 宿主声明只允许出现在 data-service 的 ingest 两条上——否则
	// `x-served-by` 就成了绕过双向对拍的万能后门。
	require.Len(t, otherHost, 2, "x-served-by 只应有 2 条（ingest/raw、ingest/equitydeep）")
	for key, host := range otherHost {
		assert.Equal(t, "data-service", host, "%s 的宿主声明应为 data-service", key)
		assert.Contains(t, []string{"POST /api/ingest/raw", "POST /api/ingest/equitydeep"}, key,
			"只有 ingest 两条允许声明别的宿主（它们只在 :8081 注册）")
	}
}

// TestRouteContract_ExemptionTables：豁免表自身的完整性。
//
// 这一条是「防豁免清单被滥用」的那道闸：想靠往表里加一条吞掉真漂移，
// 必须先改这里的期望条数——那是一次显式、可 review 的动作。
func TestRouteContract_ExemptionTables(t *testing.T) {
	t.Parallel()

	assert.Len(t, legacyAliases, 16, "legacy 别名应为 16 条（AUD-02/AUD-33 冻结的一版）")
	assert.Len(t, metaEndpoints, 2, "元端点应为 2 条（/api/docs 与 /api/openapi.yaml）")
	assert.Len(t, proxyFamilies, 5, "反代族应为 5 族（copilot/pipeline/explore/tools/sync）")

	for _, a := range legacyAliases {
		assert.NotEmpty(t, a.reason, "legacy 别名 %s %s 缺理由", a.method, a.path)
		assert.NotContains(t, a.path, "/api/", "legacy 别名必须是根路径别名：%s", a.path)
	}
	for _, m := range metaEndpoints {
		assert.NotEmpty(t, m.reason, "元端点 %s %s 缺理由", m.method, m.path)
	}
	for _, f := range proxyFamilies {
		assert.NotEmpty(t, f.reason, "反代族 %s 缺理由", f.prefix)
	}

	// 豁免表不得覆盖已登记的端点（否则会把真漂移一起吞掉）。
	documented, _ := documentedRoutes(t)
	for _, a := range legacyAliases {
		assert.False(t, documented[a.method+" "+a.path],
			"%s %s 已在 yaml 登记，不应再进豁免表", a.method, a.path)
	}
}

// TestRouteContract_WildcardCoverage 钉住「反代族覆盖」这条判定本身：
// 族内子路径算覆盖、族外前缀相似的路径（/api/syncx）不算。
func TestRouteContract_WildcardCoverage(t *testing.T) {
	t.Parallel()

	routes := map[routeKey]bool{
		"GET /api/sync/{path}": true,
	}
	assert.True(t, coveredByWildcard(routes, "GET", "/api/sync/jobs"), "族内子路径应算覆盖")
	assert.False(t, coveredByWildcard(routes, "GET", "/api/syncx/jobs"), "同前缀兄弟目录不算覆盖")
	assert.False(t, coveredByWildcard(routes, "POST", "/api/sync/jobs"), "方法不同不算覆盖")
	assert.False(t, coveredByWildcard(map[routeKey]bool{"GET /api/sync/jobs": true}, "GET", "/api/sync/jobs"),
		"非通配路由不做前缀匹配")
}
