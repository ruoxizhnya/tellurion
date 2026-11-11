package main

// handlers_ai_proxy_test.go —— AI 反代的「首次调用才解析」行为（2026-11-11）。
//
// Boot 期不再预检 ai_service.url（见 endpoints.go），因此这里补上调用期契约：
// 主机名解析失败时返回 503，且正文点名 AI_SERVICE_URL 与主机名，不能只说
// "ai-service unreachable" 让人猜是没起还是写错了。
//
// 用真实 httptest.Server 而不是 ResponseRecorder：ReverseProxy 会调用
// CloseNotify，ResponseRecorder 不实现该接口（gin 会 panic）。

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/spf13/viper"
)

func TestAIProxy_UnresolvableHostFailsOnFirstCallNamingEnv(t *testing.T) {
	gin.SetMode(gin.TestMode)
	v := viper.New()
	v.Set("ai_service.url", "http://ai-service-that-does-not-exist.invalid:8086")

	router := gin.New()
	registerAIProxyRoutes(router, v, zerolog.Nop())
	srv := httptest.NewServer(router)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/tools")
	if err != nil {
		t.Fatalf("call gateway: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d: %s", resp.StatusCode, body)
	}
	for _, want := range []string{"AI_SERVICE_URL", "ai-service-that-does-not-exist.invalid"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("body should name %q, got %s", want, body)
		}
	}
}
