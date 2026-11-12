package bootstrap

// QT-50 ③-④：BuildRouter 在 insecure（无鉴权）模式下必须挂**请求级**回环门。
// 启动期 DecideAuthStartup 只判绑定网卡，管不到运行期来源。

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"

	"tellurion/pkg/auth"
)

func guardRouter(t *testing.T, cfg map[string]any) *gin.Engine {
	t.Helper()
	v := viper.New()
	for k, val := range cfg {
		v.Set(k, val)
	}
	// 显式 open-access（allow-all 替身）。零值 Config 是 unavailable，不是
	// 放开——见 TestBuildRouter_UnavailableGatesWrites。
	svc := auth.NewService(nil, auth.Config{OpenAccess: true})
	r := BuildRouter(svc, v, zerolog.Nop())
	r.GET("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })
	return r
}

func probe(r *gin.Engine, remote string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.RemoteAddr = remote
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// 监听回环 + 无鉴权 ⇒ 逐请求门生效：本机放行，非本机 403。
func TestBuildRouter_InsecureLoopbackGatesPerRequest(t *testing.T) {
	t.Parallel()

	r := guardRouter(t, map[string]any{"server.host": "127.0.0.1"})

	assert.Equal(t, http.StatusOK, probe(r, "127.0.0.1:4321").Code)
	assert.Equal(t, http.StatusOK, probe(r, "[::1]:4321").Code)

	w := probe(r, "192.0.2.7:4321")
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "non-loopback")
}

// 容器 loopback-published 形态（bind 0.0.0.0）不能走请求级门：容器内
// RemoteAddr 是发布层网关地址，逐请求拒绝会把本地栈打挂。暴露面由静态
// 护栏 + 运行时 netstat 保证（见 InsecureExposureLoopbackPublished）。
func TestBuildRouter_LoopbackPublishedSkipsRequestGate(t *testing.T) {
	t.Parallel()

	r := guardRouter(t, map[string]any{
		"server.host":            "0.0.0.0",
		"auth.allow_insecure":    true,
		"auth.insecure_exposure": InsecureExposureLoopbackPublished,
	})

	assert.Equal(t, http.StatusOK, probe(r, "172.18.0.1:4321").Code)
}

// 鉴权开启时 JWT/RBAC 是门，不再叠请求级回环限制（否则远程合法客户端
// 全被 403，JWT 白配）。
func TestBuildRouter_AuthEnabledDoesNotGateLoopback(t *testing.T) {
	t.Parallel()

	v := viper.New()
	v.Set("server.host", "0.0.0.0")
	svc := auth.NewService(nil, auth.Config{JWTSecret: []byte("qt50-loopback")})
	r := BuildRouter(svc, v, zerolog.Nop())
	r.GET("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.POST("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })

	// 无 token 被 JWT 中间件拦下（401），而不是被回环门拦下（403）。
	w := probe(r, "192.0.2.7:4321")
	assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
}

// 第三态 + 非回环绑定（容器必须绑 0.0.0.0）：显式声明后写端点必须真正走到
// 业务 handler，而不是被启动闸/回环门拦下。这是主任 2026-11-12 修正的用例：
// 「非回环绑定 + 显式声明 → 能起且写端点走业务」。
func TestBuildRouter_DeclaredNonLoopback_WriteReachesHandler(t *testing.T) {
	t.Parallel()

	v := viper.New()
	v.Set("server.host", "0.0.0.0")
	v.Set("auth.allow_insecure", true)                         // 显式声明
	svc := auth.NewService(nil, auth.Config{OpenAccess: true}) // 启动裁决产出的第三态
	r := BuildRouter(svc, v, zerolog.Nop())

	called := false
	r.POST("/api/signals", func(c *gin.Context) {
		called = true
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req := httptest.NewRequest(http.MethodPost, "/api/signals", nil)
	req.RemoteAddr = "172.18.0.1:4321" // 来自容器网关，非回环
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.True(t, called, "显式声明的第三态下写端点必须走到业务 handler")
}

// 第二态（unavailable）：BuildRouter 必须挂 auth 中间件，写面 503、读面公开，
// 且不能落到 insecure 的逐请求回环门（否则容器 loopback-published 形态会被
// 网关地址全部 403）。
func TestBuildRouter_UnavailableGatesWrites(t *testing.T) {
	t.Parallel()

	v := viper.New()
	// 容器形态：绑定 0.0.0.0 + 发布层声明回环。
	v.Set("server.host", "0.0.0.0")
	v.Set("auth.insecure_exposure", InsecureExposureLoopbackPublished)
	svc := auth.NewService(nil, auth.Config{}) // 零值 = unavailable
	r := BuildRouter(svc, v, zerolog.Nop())
	r.GET("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.POST("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })

	read := probe(r, "172.18.0.1:4321")
	assert.Equal(t, http.StatusOK, read.Code, "降级态读端点必须保持公开: %s", read.Body.String())

	req := httptest.NewRequest(http.MethodPost, "/probe", nil)
	req.RemoteAddr = "172.18.0.1:4321"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), `"code":"`+auth.CodeAuthUnavailable+`"`)
}
