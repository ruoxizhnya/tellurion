package httpserver

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
// ⚠️ 容器形态（bind 0.0.0.0 + `AUTH_INSECURE_EXPOSURE=loopback-published`）
// 不走本中间件：容器内的 RemoteAddr 是发布层网关地址（不是回环），逐请求
// 拒绝会直接把本地栈打挂。那一形态的暴露面由静态护栏
// （`tools/check_deploy_consistency.py` 3c）与运行时 netstat 断言
// （`tools/local-stack.sh`）保证——见 `bootstrap.InsecureExposureLoopbackPublished`。

import (
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// LoopbackOnly 拒绝非回环来源的请求（fail-closed）。
//
// 未认证的本机来源放行；其余一律 403，错误体带 `non-loopback` 字样，
// 便于日志与前端按 code/message 检索。
func LoopbackOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		if IsLoopbackRemoteAddr(c.Request.RemoteAddr) {
			c.Next()
			return
		}
		Fail(c, http.StatusForbidden,
			"insecure mode: non-loopback request source rejected — open-access is loopback-only")
		c.Abort()
	}
}

// IsLoopbackRemoteAddr 报告 `host:port` 形式的来源地址是否为本机回环。
//
// 认得的写法：`127.0.0.1:5000`、`[::1]:5000`、`localhost:5000`，以及不带
// 端口的 `127.0.0.1` / `::1`。空串、解析失败、非回环 IP 一律 false
// （fail-closed；不猜）。
func IsLoopbackRemoteAddr(remoteAddr string) bool {
	host := strings.TrimSpace(remoteAddr)
	if host == "" {
		return false
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	// IPv6：去方括号与 zone（fe80::1%eth0 不是回环，但 ::1%lo 是）。
	host = strings.Trim(host, "[]")
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
