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
	// 无密钥 = 鉴权关闭（insecure 模式）。
	svc := auth.NewService(nil, auth.Config{})
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

	// 无 token 被 JWT 中间件拦下（401），而不是被回环门拦下（403）。
	w := probe(r, "192.0.2.7:4321")
	assert.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
}
