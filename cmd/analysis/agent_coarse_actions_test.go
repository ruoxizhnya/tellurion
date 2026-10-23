package main

// QT-50 ②③⑥ 护栏：agent ↔ core 的六类粗粒度动作**代码声明 = openapi 登记 =
// 生产路由**，且粒度、鉴权与 fail-closed 行为都可机械核对。
//
// 三条腿：
//  1. 声明 ↔ openapi：`cmd/analysis/agent_actions.go` 的每条动作，在
//     `docs/openapi.yaml` 对应 operation 上必须有同值的
//     `x-agent-coarse-action`；反向（yaml 标了、代码没声明）也红。
//  2. 声明 ↔ 路由：每条动作必须是生产路由表里真实注册的 METHOD+路径
//     （参数写法换成 openapi 的 `{x}` 后比较）。
//  3. 粒度：不得出现通配路由；不得出现 per-bar / per-order / per-symbol
//     的路径参数；「行情查询」必须是批量端点而不是 `/api/ohlcv/{symbol}`。
//
// 行为腿另测鉴权：无 token → 401；越权 → 403 且**不是**「策略表缺条目」；
// 允许角色能穿过鉴权层。破坏验证：删掉 route_policy.go 里
// `POST /api/factor/registrations` 那一行 → 行为腿红（admin 变 403），
// 还原后绿。

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"tellurion/docs"
	"tellurion/pkg/auth"
	"tellurion/pkg/authz"
)

// coarseActionCount 是 D24 允许的动作数。第 7 类（交易日历 / 上市窗口查询）
// 待用户裁-04 确认，未确认前**不得**出现在本表 —— 这条断言就是那道闸。
const coarseActionCount = 6

// openapiCoarseActions 返回 yaml 里标了 x-agent-coarse-action 的
// "METHOD 路径" → 动作名。
func openapiCoarseActions(t *testing.T) map[string]string {
	t.Helper()

	var spec struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	require.NoError(t, yaml.Unmarshal(docs.OpenAPISpec, &spec), "docs/openapi.yaml 必须可解析")

	httpMethods := map[string]bool{
		"get": true, "post": true, "put": true, "patch": true,
		"delete": true, "head": true, "options": true, "trace": true,
	}
	out := map[string]string{}
	for path, ops := range spec.Paths {
		for method, node := range ops {
			if !httpMethods[method] || node.Kind != yaml.MappingNode {
				continue
			}
			for i := 0; i+1 < len(node.Content); i += 2 {
				if node.Content[i].Value == "x-agent-coarse-action" {
					out[strings.ToUpper(method)+" "+path] = node.Content[i+1].Value
				}
			}
		}
	}
	return out
}

func registeredRouteKeys(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, r := range contractRouter(t).Routes() {
		out[r.Method+" "+normalizeGinPath(r.Path)] = true
	}
	return out
}

// TestAgentCoarseActions_DeclareAndDocumentExactlyTheSameSet 双向对拍。
func TestAgentCoarseActions_DeclareAndDocumentExactlyTheSameSet(t *testing.T) {
	t.Parallel()

	require.Len(t, agentCoarseActions, coarseActionCount,
		"D24 只允许六类粗粒度动作；第 7 类等裁-04，加表要连带改本断言并留裁决记录")

	documented := openapiCoarseActions(t)
	registered := registeredRouteKeys(t)

	seenNames := map[string]bool{}
	var declared, missingInSpec, missingInCode, notRouted []string

	for _, action := range agentCoarseActions {
		key := action.Method + " " + normalizeGinPath(action.Route)

		if seenNames[action.Name] {
			t.Errorf("动作名 %q 重复 —— openapi 的扩展值必须唯一", action.Name)
		}
		seenNames[action.Name] = true
		declared = append(declared, key)

		if got, ok := documented[key]; !ok {
			missingInSpec = append(missingInSpec, key)
		} else if got != action.Name {
			t.Errorf("%s 的 x-agent-coarse-action = %q，代码声明的是 %q", key, got, action.Name)
		}
		if !registered[key] {
			notRouted = append(notRouted, key)
		}
	}

	for key, name := range documented {
		if !seenNames[name] {
			missingInCode = append(missingInCode, key+" → "+name)
		}
		if !slices.Contains(declared, key) {
			missingInCode = append(missingInCode, key+" 标了 "+name+" 但不在 agent_actions.go 的路径上")
		}
	}

	sort.Strings(missingInSpec)
	sort.Strings(missingInCode)
	sort.Strings(notRouted)

	assert.Empty(t, missingInSpec, "这些动作没有在 docs/openapi.yaml 上打 x-agent-coarse-action：\n%s",
		strings.Join(missingInSpec, "\n"))
	assert.Empty(t, missingInCode, "yaml 里标了 agent 粗粒度动作，但代码没有声明（或路径不一致）：\n%s",
		strings.Join(missingInCode, "\n"))
	assert.Empty(t, notRouted, "声明的动作在生产路由表里不存在（端点了/改路径了）：\n%s",
		strings.Join(notRouted, "\n"))
}

// TestAgentCoarseActions_AreCoarseGrain 粒度判据：不许通配、不许 per-*
// 路径参数，行情查询必须是批量端点。
func TestAgentCoarseActions_AreCoarseGrain(t *testing.T) {
	t.Parallel()

	// per-bar / per-symbol / per-order 粒度的路径参数黑名单。`factor` 不在其中：
	// 因子名是研究对象，不是 bar/订单。
	forbiddenParams := []string{"{bar}", "{date}", "{day}", "{order}", "{order_id}", "{trade_id}", "{symbol}"}

	for _, action := range agentCoarseActions {
		path := normalizeGinPath(action.Route)

		assert.NotContains(t, path, "*", "%s: 粗粒度动作不允许通配路由（会把未登记的子路径也放进来）", action.Name)
		for _, p := range forbiddenParams {
			assert.NotContains(t, strings.ToLower(path), p,
				"%s: 路径 %s 含 per-item 参数 %s —— D24 禁止 per-bar / per-symbol / per-order 远程调用",
				action.Name, path, p)
		}
	}

	market := findAction(t, "query_market_data")
	assert.Equal(t, http.MethodPost, market.Method)
	assert.Equal(t, "/api/v1/ohlcv/bulk", market.Route,
		"行情查询必须是一次请求取整批；per-symbol 的 GET /api/ohlcv/{symbol} 不是粗粒度动作")
}

// TestAgentCoarseActions_RoutePolicyTiers 把「敏感写必须 admin、纯读不得被
// 抬成 trader」钉在策略表上（档位口径以 route_policy.go 为准，这里防漂移）。
func TestAgentCoarseActions_RoutePolicyTiers(t *testing.T) {
	t.Parallel()

	reg := findAction(t, "register_factor")
	role, ok := analysisRoutePolicy.Role(reg.Method + " " + reg.Route)
	require.True(t, ok, "POST /api/factor/registrations 必须在 route_policy.go 里表态")
	assert.Equal(t, auth.RoleAdmin, role, "因子登记改变后续回测可引用的定义集合，必须是 admin 档")

	bulk := findAction(t, "query_market_data")
	role, ok = analysisRoutePolicy.Role(bulk.Method + " " + bulk.Route)
	require.True(t, ok, "POST /api/v1/ohlcv/bulk 必须在 route_policy.go 里表态")
	assert.Equal(t, authz.RoleAnyAuthenticated(), role, "批量行情查询是纯读，不得被抬成 trader 档")
}

// TestAgentCoarseActions_FailClosed 行为腿：真 router + 真 JWT + 真策略执法。
//
// 无 token 打任何一条 → 401；viewer/trader 打 admin 档 → 403（且**不是**
// 「策略表缺条目」）；admin 能穿过鉴权层（到 handler 拿 503，因为最小装配
// 没有库）。删掉策略表里那一行会让最后一条变 403 —— 这就是破坏验证的落点。
func TestAgentCoarseActions_FailClosed(t *testing.T) {
	t.Parallel()

	svc := auth.NewService(nil, auth.Config{JWTSecret: []byte("qt50-coarse")})
	require.True(t, svc.Enabled())
	r := policyRouter(t, svc)

	for _, action := range agentCoarseActions {
		name, method, path := action.Name, action.Method, action.Route
		t.Run(name+" 无 token=401", func(t *testing.T) {
			w := request(r, method, concretePath(path), "", "")
			assert.Equal(t, http.StatusUnauthorized, w.Code,
				"未认证请求必须被 auth.Middleware 拦下：%s", w.Body.String())
		})
	}

	reg := findAction(t, "register_factor")
	regPath := concretePath(reg.Route)
	viewer := tokenFor(t, svc, auth.RoleViewer)
	trader := tokenFor(t, svc, auth.RoleTrader)
	admin := tokenFor(t, svc, auth.RoleAdmin)

	t.Run("viewer 打因子登记=403 且不是策略表缺条目", func(t *testing.T) {
		w := request(r, reg.Method, regPath, viewer, `{}`)
		assert.Equal(t, http.StatusForbidden, w.Code)
		assert.NotContains(t, w.Body.String(), "no authorization policy",
			"403 必须来自角色不足；出现「策略表缺条目」说明登记被删了或没表态")
	})

	t.Run("trader 打因子登记=403", func(t *testing.T) {
		w := request(r, reg.Method, regPath, trader, `{}`)
		assert.Equal(t, http.StatusForbidden, w.Code)
		assert.NotContains(t, w.Body.String(), "no authorization policy")
	})

	t.Run("admin 穿过鉴权层", func(t *testing.T) {
		w := request(r, reg.Method, regPath, admin, `{}`)
		assert.Equal(t, http.StatusServiceUnavailable, w.Code,
			"admin 必须通过 auth + authz；最小装配没有库，handler 应回 503。body=%s", w.Body.String())
	})
}

func findAction(t *testing.T, name string) agentCoarseAction {
	t.Helper()
	for _, a := range agentCoarseActions {
		if a.Name == name {
			return a
		}
	}
	t.Fatalf("agentCoarseActions 里没有 %q", name)
	return agentCoarseAction{}
}

// concretePath 把 gin 参数换成具体值，便于发真请求（只用于本案的动作集，
// 它们唯一的参数是因子名）。
func concretePath(route string) string {
	return strings.NewReplacer(":factor", "momentum").Replace(route)
}

func request(r *gin.Engine, method, path, token, body string) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}
