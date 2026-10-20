// Package authz 把「端点 × 最低角色」变成一张**明文表**，并据此执法。
//
// 为什么需要它（REV-17）：analysis 与 data 两个服务的鉴权粒度此前不一致——
// analysis 只有 3 处挂了角色（execution orders、plugin 变更、auth admin），
// 其余变更端点（策略 CRUD、batch、walkforward、compliance、alerts force-check、
// reconciliation run…）只要「任意已认证角色」，viewer 通行；data-service 则
// **完全没有认证**（`POST /api/ingest/raw` 这条证据链唯一写门是裸的）。
//
// 本包不做「按路由逐个挂中间件」那种写法——那会把同一份路由表抄两遍，漏一条
// 也没人发现。取而代之：
//
//  1. 每个服务在 `route_policy.go` 里维护一张 `Policy`：**逐条**列出所有变更
//     端点及其最低角色（Public / Admin / Trader 三档，每档都写理由）；
//  2. 本包的 Middleware 在 `auth.Middleware()` 之后全局挂一次，按
//     `c.FullPath()` 查表执法；
//  3. 每个服务的守卫测试断言「路由表 ≡ 策略表」（双向），新加一个变更端点
//     却没在表里表态 → 测试红。
//
// **规则（2026-10-16 主任裁决）**：所有 POST/PUT/PATCH/DELETE 至少 trader；
// 管理面 admin；登录/刷新/首个管理员引导这三条是 Public。
//
// 失败模式刻意选了 fail-closed：变更端点若在策略表里查不到，直接 403 并点名
// 「no policy entry」——守卫测试本该在 CI 就拦住这种情况，运行时再兜一层。
package authz

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"tellurion/pkg/auth"
)

// Policy 是一个服务的「端点 × 最低角色」明文表。
//
// 三个字段都是完整路由键（`"METHOD /path/with/:param"`，与 gin 的
// `RouteInfo.Path` 同形）。同一条路由**只能**出现在一个档位里——守卫测试会查重。
type Policy struct {
	// Public：变更端点里不需要「变更角色」的，仅登录类入口。
	// 它们同时也在 pkg/auth 的 publicPaths 白名单里（无 token 可达）。
	Public []string
	// Admin：管理面（用户管理、插件加载/卸载/重载、watch-dir 等）。
	Admin []string
	// Trader：其余全部变更端点——下单、回测、同步触发、策略 CRUD、配置变更。
	Trader []string
	// ReadOnlyPOST：**POST 但语义是只读查询**的端点（筛选器、批量读行情、
	// 只算不写的风控计算）。规则原文是「所有 POST/PUT/DELETE 至少 trader」，
	// 但把「用 POST 承载查询 DSL」的端点也算成写，会让 viewer 这个
	// read-only 角色连选股都做不了——那是功能回退，不是安全收紧。
	// 本档位要求「任意已认证角色」，并且**必须写理由**（守卫测试查非空）。
	ReadOnlyPOST []string
}

// Role 返回某个路由键要求的最低角色，以及它是否在表里。
func (p Policy) Role(key string) (auth.Role, bool) {
	for _, k := range p.Public {
		if k == key {
			return "", true
		}
	}
	for _, k := range p.Admin {
		if k == key {
			return auth.RoleAdmin, true
		}
	}
	for _, k := range p.Trader {
		if k == key {
			return auth.RoleTrader, true
		}
	}
	for _, k := range p.ReadOnlyPOST {
		if k == key {
			// 只读 POST：任意已认证角色。用 "" 会和 Public 混淆，故用一个
			// 哨兵值区分——Middleware 认它，测试也认它。
			return roleAnyAuthenticated, true
		}
	}
	return "", false
}

// roleAnyAuthenticated 是 ReadOnlyPOST 档位的哨兵：要求「已认证」而不限定角色。
// 用空字符串会与 Public（完全放行）撞语义，所以单独给一个不可派发的值。
const roleAnyAuthenticated auth.Role = "any-authenticated"

// RoleAnyAuthenticated 暴露哨兵给守卫测试（判断档位用）。
func RoleAnyAuthenticated() auth.Role { return roleAnyAuthenticated }

// Entries 返回整张表的「键 → 档位」，供守卫测试做双向对拍。
func (p Policy) Entries() map[string]string {
	out := make(map[string]string, len(p.Public)+len(p.Admin)+len(p.Trader)+len(p.ReadOnlyPOST))
	for _, k := range p.Public {
		out[k] = "public"
	}
	for _, k := range p.Admin {
		out[k] = "admin"
	}
	for _, k := range p.Trader {
		out[k] = "trader"
	}
	for _, k := range p.ReadOnlyPOST {
		out[k] = "readonly-post"
	}
	return out
}

// IsMutating 判断方法是否属于「变更」类。
//
// HEAD/OPTIONS 不算（gin 的 `Any` 会把它们一并注册，但它们不改变状态）；
// TRACE 也不是——真正的变更面就是这四个。
func IsMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// Middleware 按 Policy 执法。**必须挂在 auth.Service.Middleware() 之后**——
// 它依赖 Middleware 写进 context 的角色。
//
// 语义：
//   - 鉴权关闭（svc == nil 或 !Enabled）→ no-op：开发/CI/e2e 的 open-access
//     模式不变（与 Service.RequireRole 同一裁决）；
//   - 读端点（GET/HEAD/OPTIONS…）→ 直接放行，本包只管变更面；
//   - 变更端点 → 查表：Public 放行；Admin 要求 admin；Trader 要求
//     trader 或 admin；**查不到 → 403 并点名**（fail-closed）。
func Middleware(svc *auth.Service, p Policy) gin.HandlerFunc {
	return func(c *gin.Context) {
		if svc == nil || !svc.Enabled() {
			c.Next()
			return
		}
		if !IsMutating(c.Request.Method) {
			c.Next()
			return
		}

		// 用路由模板而不是原始 URL：后者带真实 id，无法查表。
		// 未匹配到路由时 FullPath() 为空（404 由 gin 兜底），无需在这里拦。
		fullPath := c.FullPath()
		if fullPath == "" {
			c.Next()
			return
		}
		key := c.Request.Method + " " + fullPath

		minRole, ok := p.Role(key)
		if !ok {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": "no authorization policy for this endpoint",
				"route": key,
				"hint":  "在服务的 route_policy.go 里为这条路由表态（public / admin / trader）",
			})
			return
		}
		if minRole == "" { // Public
			c.Next()
			return
		}

		role, ok := auth.RoleFromContext(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "no authenticated user (authz.Middleware placed before auth.Middleware?)",
			})
			return
		}
		switch minRole {
		case roleAnyAuthenticated:
			// 已认证即可（角色已由上一步 RoleFromContext 证明存在）。
			c.Next()
		case auth.RoleAdmin:
			auth.RequireRole(auth.RoleAdmin)(c)
		case auth.RoleTrader:
			auth.RequireRole(auth.RoleTrader, auth.RoleAdmin)(c)
		default:
			// 未知档位 = 策略表写错了。fail-closed 并点名角色，别静默放行。
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error":    "unknown role tier in policy",
				"route":    key,
				"min_role": string(minRole),
				"caller":   string(role),
			})
		}
	}
}
