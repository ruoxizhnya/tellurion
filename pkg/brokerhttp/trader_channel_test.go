package brokerhttp

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 内存网关的边界：通道性质必须显式声明，mock 必须双键确认。
// 目的不是限制 dev 用法，而是让「生产里静默跑在内存撮合上」在启动期就失败。
func TestResolveChannelRefusesSilentMock(t *testing.T) {
	const brokerURL = "http://127.0.0.1:8091"
	logger := zerolog.Nop()

	t.Run("missing channel", func(t *testing.T) {
		t.Setenv("BROKER_CHANNEL", "")
		t.Setenv("BROKER_ALLOW_MOCK", "")
		_, err := resolveChannel(logger, brokerURL)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "BROKER_CHANNEL is required")
	})

	t.Run("mock without allow", func(t *testing.T) {
		t.Setenv("BROKER_CHANNEL", "mock")
		t.Setenv("BROKER_ALLOW_MOCK", "")
		_, err := resolveChannel(logger, brokerURL)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "BROKER_ALLOW_MOCK=true")
	})

	t.Run("mock with allow", func(t *testing.T) {
		t.Setenv("BROKER_CHANNEL", "MOCK")
		t.Setenv("BROKER_ALLOW_MOCK", "TRUE")
		ch, err := resolveChannel(logger, brokerURL)
		require.NoError(t, err)
		assert.Equal(t, ChannelMock, ch)
	})

	t.Run("live loopback only warns", func(t *testing.T) {
		t.Setenv("BROKER_CHANNEL", "live")
		t.Setenv("BROKER_ALLOW_MOCK", "")

		var buf bytes.Buffer
		warnLogger := zerolog.New(&buf)
		ch, err := resolveChannel(warnLogger, brokerURL)
		require.NoError(t, err)
		assert.Equal(t, ChannelLive, ch)

		// BROKER-LIVE-LOOPBACK-GUARD：告警必须带结构化 reason，部署侧才能
		// 按 reason=loopback_gateway 检索/告警；不带 reason 的 WARN 等于噪声。
		line := buf.String()
		require.Contains(t, line, "loopback_gateway")
		require.Contains(t, line, `"level":"warn"`)
		require.Contains(t, line, `"channel":"live"`)
	})

	t.Run("live non-loopback stays quiet", func(t *testing.T) {
		t.Setenv("BROKER_CHANNEL", "live")
		t.Setenv("BROKER_ALLOW_MOCK", "")

		var buf bytes.Buffer
		ch, err := resolveChannel(zerolog.New(&buf), "http://broker.example.com:8091")
		require.NoError(t, err)
		assert.Equal(t, ChannelLive, ch)
		assert.Empty(t, buf.String(), "只有回环才告警；正常远端网关不应制造 WARN 噪声")
	})

	t.Run("unknown value", func(t *testing.T) {
		t.Setenv("BROKER_CHANNEL", "paper")
		_, err := resolveChannel(logger, brokerURL)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid")
	})
}

// 对端自报的通道性质是 live 声明的唯一证据：不一致或无法验证都必须拒绝。
func TestVerifyPeerChannelIsFailClosed(t *testing.T) {
	logger := zerolog.Nop()
	newPeer := func(t *testing.T, body string, status int) *httptest.Server {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/version" {
				http.NotFound(w, r)
				return
			}
			if status != http.StatusOK {
				w.WriteHeader(status)
				return
			}
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(srv.Close)
		return srv
	}

	t.Run("match", func(t *testing.T) {
		srv := newPeer(t, `{"service":"brokergateway","channel":"mock"}`, http.StatusOK)
		require.NoError(t, verifyPeerChannel(logger, srv.URL, ChannelMock))
	})

	t.Run("live declared but peer is mock", func(t *testing.T) {
		srv := newPeer(t, `{"service":"brokergateway","channel":"mock"}`, http.StatusOK)
		err := verifyPeerChannel(logger, srv.URL, ChannelLive)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "channel mismatch")
		assert.Contains(t, err.Error(), `peer reports "mock"`)
	})

	t.Run("peer does not expose version", func(t *testing.T) {
		srv := newPeer(t, "", http.StatusNotFound)
		err := verifyPeerChannel(logger, srv.URL, ChannelLive)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "HTTP 404")
	})

	t.Run("peer omits channel", func(t *testing.T) {
		srv := newPeer(t, `{"service":"brokergateway"}`, http.StatusOK)
		err := verifyPeerChannel(logger, srv.URL, ChannelMock)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "does not report")
	})

	t.Run("peer unreachable", func(t *testing.T) {
		srv := newPeer(t, "", http.StatusOK)
		addr := srv.URL
		srv.Close()
		err := verifyPeerChannel(logger, addr, ChannelMock)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cannot verify peer channel")
	})
}

// 装配入口：缺 BROKER_CHANNEL 不得静默成功。
func TestNewTraderFromEnvRequiresChannel(t *testing.T) {
	t.Setenv("BROKER_URL", "http://127.0.0.1:8091")
	t.Setenv("BROKER_SERVICE_TOKEN", "tok")
	t.Setenv("BROKER_ACCOUNT_ID", "acc")
	t.Setenv("BROKER_CHANNEL", "")
	_, err := NewTraderFromEnv(zerolog.Nop())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BROKER_CHANNEL is required")

	t.Setenv("BROKER_CHANNEL", "mock")
	t.Setenv("BROKER_ALLOW_MOCK", "true")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			_, _ = w.Write([]byte(`{"service":"brokergateway","channel":"mock"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	t.Setenv("BROKER_URL", srv.URL)
	trader, err := NewTraderFromEnv(zerolog.Nop())
	require.NoError(t, err)
	assert.Equal(t, "broker_http", trader.Name())
}
