package bootstrap

// loopback.go —— insecure（无鉴权）模式的请求级回环门（QT-50 ③-④）。
//
// 为什么需要它：`bootstrap.DecideAuthStartup` 只在**启动期**看绑定地址
// （以及容器发布层的显式声明）。它保证「0.0.0.0 + 无鉴权」不会悄悄起来，
// 但管不到运行期的来源——监听 127.0.0.1 时，任何本机进程（包括被攻陷的
// 同机服务、SSH 端口转发、错误的代理配置）都能直接打管理面。open-access
// 的口径是 **loopback-only**，那就必须逐请求判来源，而不是只判 bind host。
//
// 判据只看 `Request.RemoteAddr`，**不看** `X-Forwarded-For` / `ClientIP()`：
// 那些头是客户端可伪造的；在无鉴权模式下信任它们等于把门交给攻击者。
//
// QT-75：判据 `IsLoopbackRemoteAddr` 随 `pkg/httpserver` 整包切到
// `tellurion-kit/httpserver`（KT-10 / 裁-18）；按 kit 的分工约定，gin 包装
// 属应用层策略，留在 core —— 本文件即那层包装，不再回 core 建横切包。
//
// ⚠️ 容器形态（bind 0.0.0.0 + `AUTH_INSECURE_EXPOSURE=loopback-published`）
// 不走本中间件：容器内的 RemoteAddr 是发布层网关地址（不是回环），逐请求
// 拒绝会直接把本地栈打挂。那一形态的暴露面由静态护栏
// （`tools/check_deploy_consistency.py` 3c）与运行时 netstat 断言
// （`tools/local-stack.sh`）保证——见 `bootstrap.InsecureExposureLoopbackPublished`。

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/ruoxizhnya/tellurion-kit/httpserver"
)

// loopbackOnly 拒绝非回环来源的请求（fail-closed）。
// 未认证的本机来源放行；其余一律 403，错误体带 `non-loopback` 字样，
// 便于日志与前端按 code/message 检索。
func loopbackOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		if httpserver.IsLoopbackRemoteAddr(c.Request.RemoteAddr) {
			c.Next()
			return
		}
		httpserver.Fail(c, http.StatusForbidden,
			"insecure mode: non-loopback request source rejected — open-access is loopback-only")
		c.Abort()
	}
}
