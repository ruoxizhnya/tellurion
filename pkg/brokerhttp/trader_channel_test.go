package brokerhttp

import (
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
		ch, err := resolveChannel(logger, brokerURL)
		require.NoError(t, err)
		assert.Equal(t, ChannelLive, ch)
	})

	t.Run("unknown value", func(t *testing.T) {
		t.Setenv("BROKER_CHANNEL", "paper")
		_, err := resolveChannel(logger, brokerURL)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid")
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
	trader, err := NewTraderFromEnv(zerolog.Nop())
	require.NoError(t, err)
	assert.Equal(t, "broker_http", trader.Name())
}
