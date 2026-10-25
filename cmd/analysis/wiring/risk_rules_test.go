package wiring

// QT-66 的 provider 单测：HTTP 形状、错误口径与装配开关。
//
// 这里不跑整轮回测（那在 pkg/backtest 的 QT-66 腿上）；本文件只回答
// 「请求长什么样、失败怎么报、开关关了会不会悄悄接上」。

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/backtest"
	"tellurion/pkg/marketdata"
)

const qt66Hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type qt66Server struct {
	mu      sync.Mutex
	method  string
	path    string
	body    string
	headers http.Header
}

func (s *qt66Server) handler(status int, resp string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r.Body)
		s.mu.Lock()
		s.method, s.path, s.body = r.Method, r.URL.Path, buf.String()
		s.headers = r.Header.Clone()
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(resp))
	}
}

func (s *qt66Server) captured() (method, path, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.method, s.path, s.body
}

func (s *qt66Server) capturedAuth() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.headers.Get("Authorization")
}

// TestHTTPRiskRulesProvider_SendsServiceToken：risk 开了 secret 后，
// 不带 Bearer 的 resolve 恒 401（policy 表是 trader|admin）⇒ 每个 run failed。
func TestHTTPRiskRulesProvider_SendsServiceToken(t *testing.T) {
	srv := &qt66Server{}
	ts := httptest.NewServer(srv.handler(http.StatusOK, `{"risk_rules_hash":"`+qt66Hash+`"}`))
	defer ts.Close()

	p, err := NewHTTPRiskRulesProviderWithToken(ts.URL, " svc-token ", time.Second)
	require.NoError(t, err)
	_, err = p.ResolveRiskRulesHash(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, "Bearer svc-token", srv.capturedAuth(), "token 必须去掉首尾空白后进 Bearer")
}

func TestHTTPRiskRulesProvider_OmitsTokenWhenEmpty(t *testing.T) {
	srv := &qt66Server{}
	ts := httptest.NewServer(srv.handler(http.StatusOK, `{"risk_rules_hash":"`+qt66Hash+`"}`))
	defer ts.Close()

	p, err := NewHTTPRiskRulesProvider(ts.URL, time.Second)
	require.NoError(t, err)
	_, err = p.ResolveRiskRulesHash(context.Background(), "")
	require.NoError(t, err)
	assert.Empty(t, srv.capturedAuth(), "空 token 不许发 Authorization（回环免密形态）")
}

func TestHTTPRiskRulesProvider_SendsLatestByDefault(t *testing.T) {
	srv := &qt66Server{}
	ts := httptest.NewServer(srv.handler(http.StatusOK, `{"risk_rules_hash":"`+qt66Hash+`","snapshot":{}}`))
	defer ts.Close()

	p, err := NewHTTPRiskRulesProvider(ts.URL+"/", 2*time.Second)
	require.NoError(t, err)

	got, err := p.ResolveRiskRulesHash(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, qt66Hash, got)

	method, path, body := srv.captured()
	assert.Equal(t, http.MethodPost, method)
	assert.Equal(t, "/api/risk/resolve", path)
	var req map[string]string
	require.NoError(t, json.Unmarshal([]byte(body), &req))
	assert.Equal(t, map[string]string{"ruleset": "latest"}, req, "空 pin 必须走 latest")
}

func TestHTTPRiskRulesProvider_SendsPinnedHash(t *testing.T) {
	srv := &qt66Server{}
	ts := httptest.NewServer(srv.handler(http.StatusOK, `{"risk_rules_hash":"`+qt66Hash+`"}`))
	defer ts.Close()

	p, err := NewHTTPRiskRulesProvider(ts.URL, time.Second)
	require.NoError(t, err)

	_, err = p.ResolveRiskRulesHash(context.Background(), "  "+qt66Hash+"  ")
	require.NoError(t, err)

	_, _, body := srv.captured()
	var req map[string]string
	require.NoError(t, json.Unmarshal([]byte(body), &req))
	assert.Equal(t, map[string]string{"hash": qt66Hash}, req, "钉 hash 走锁定，且去掉首尾空白")
}

func TestHTTPRiskRulesProvider_ErrorsAreLoud(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantSubstr string
	}{
		{"no approved rules", http.StatusNotFound, `{"error":"no approved rules","code":"no_approved_rules"}`, "no_approved_rules"},
		{"snapshot not found", http.StatusNotFound, `{"error":"missing","code":"snapshot_not_found"}`, "snapshot_not_found"},
		{"5xx", http.StatusInternalServerError, `boom`, "status 500"},
		{"empty hash", http.StatusOK, `{"risk_rules_hash":""}`, "not 64 lowercase hex"},
		{"uppercase hash", http.StatusOK, `{"risk_rules_hash":"` + strings.ToUpper(qt66Hash) + `"}`, "not 64 lowercase hex"},
		{"bad json", http.StatusOK, `{`, "decode response"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := &qt66Server{}
			ts := httptest.NewServer(srv.handler(tc.status, tc.body))
			defer ts.Close()

			p, err := NewHTTPRiskRulesProvider(ts.URL, time.Second)
			require.NoError(t, err)

			_, err = p.ResolveRiskRulesHash(context.Background(), "")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantSubstr)
		})
	}
}

func TestHTTPRiskRulesProvider_TimesOut(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{"risk_rules_hash":"` + qt66Hash + `"}`))
	}))
	defer ts.Close()

	p, err := NewHTTPRiskRulesProvider(ts.URL, 20*time.Millisecond)
	require.NoError(t, err)

	_, err = p.ResolveRiskRulesHash(context.Background(), "")
	require.Error(t, err, "超时必须失败，不许拿空串继续")
}

func TestNewHTTPRiskRulesProvider_RejectsBadBaseURL(t *testing.T) {
	for _, base := range []string{"", "localhost:8083", "ftp://example.com", "http://"} {
		_, err := NewHTTPRiskRulesProvider(base, time.Second)
		assert.Error(t, err, "base URL %q 必须被拒", base)
	}
}

func newQT66Engine(t *testing.T) *backtest.Engine {
	t.Helper()
	eng, err := backtest.NewEngine(viper.New(), marketdata.NewInMemoryProvider(), zerolog.Nop())
	require.NoError(t, err)
	return eng
}

func TestAttachRiskRulesFromConfig_DisabledMeansNotConfigured(t *testing.T) {
	var logs strings.Builder
	logger := zerolog.New(&logs)

	eng := newQT66Engine(t)
	require.NoError(t, AttachRiskRulesFromConfig(eng, viper.New(), logger))

	assert.False(t, eng.HasRiskRulesProvider(), "未启用时不许悄悄接一个 provider")
	assert.Contains(t, logs.String(), "not_configured")
}

func TestAttachRiskRulesFromConfig_EnabledAttaches(t *testing.T) {
	v := viper.New()
	v.Set("risk_service.resolve_enabled", true)
	v.Set("risk_service.resolve_url", "http://127.0.0.1:8086")
	v.Set("risk_service.resolve_timeout", "3s")

	eng := newQT66Engine(t)
	require.NoError(t, AttachRiskRulesFromConfig(eng, v, zerolog.Nop()))
	assert.True(t, eng.HasRiskRulesProvider())
}

// TestAttachRiskRulesFromConfig_MissingResolveURLFailsFast：resolve_url 为空
// 时**不**回落到 risk_service.url（那一项指向 analysis 自己 :8085，没有
// /api/risk/resolve）；必须在装配期拒，而不是等第一个 run 404。
func TestAttachRiskRulesFromConfig_MissingResolveURLFailsFast(t *testing.T) {
	v := viper.New()
	v.Set("risk_service.resolve_enabled", true)
	v.Set("risk_service.url", "http://localhost:8085")

	eng := newQT66Engine(t)
	err := AttachRiskRulesFromConfig(eng, v, zerolog.Nop())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolve_url")
	assert.False(t, eng.HasRiskRulesProvider())
}

func TestAttachRiskRulesFromConfig_EnabledWithBadConfigFailsFast(t *testing.T) {
	v := viper.New()
	v.Set("risk_service.resolve_enabled", true)
	v.Set("risk_service.resolve_url", "::::")

	eng := newQT66Engine(t)
	err := AttachRiskRulesFromConfig(eng, v, zerolog.Nop())
	require.Error(t, err, "显式启用而配置不合法必须在装配期失败，不能推迟到 run")
	assert.False(t, eng.HasRiskRulesProvider())
}
