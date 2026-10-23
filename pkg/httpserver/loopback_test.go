package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// TestIsLoopbackRemoteAddr 钉住判据本身：回环放行、非回环拒绝、
// 不可解析一律拒绝（fail-closed）。
func TestIsLoopbackRemoteAddr(t *testing.T) {
	t.Parallel()

	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:5000", true},
		{"127.1.2.3:1", true},
		{"[::1]:8080", true},
		{"localhost:8080", true},
		{"127.0.0.1", true},
		{"::1", true},
		{"[::1]", true},
		{"192.168.1.10:5000", false},
		{"10.0.0.1:5000", false},
		{"0.0.0.0:5000", false},
		{"[fe80::1%eth0]:5000", false},
		{"example.com:80", false},
		{"", false},
		{"not-an-addr", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.addr, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, IsLoopbackRemoteAddr(tc.addr))
		})
	}
}

// TestLoopbackOnly_Middleware 是行为腿：本机来源穿过，非本机 403，
// 且**伪造 X-Forwarded-For 不能洗白**（只看 RemoteAddr）。
func TestLoopbackOnly_Middleware(t *testing.T) {
	t.Parallel()

	r := gin.New()
	r.Use(LoopbackOnly())
	r.GET("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })

	do := func(remote, xff string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		req.RemoteAddr = remote
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	w := do("127.0.0.1:4321", "")
	assert.Equal(t, http.StatusOK, w.Code)

	w = do("[::1]:4321", "")
	assert.Equal(t, http.StatusOK, w.Code)

	w = do("192.0.2.7:4321", "")
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "non-loopback")

	w = do("192.0.2.7:4321", "127.0.0.1")
	assert.Equal(t, http.StatusForbidden, w.Code,
		"X-Forwarded-For 可伪造，insecure 模式下不得据此放行")
}
