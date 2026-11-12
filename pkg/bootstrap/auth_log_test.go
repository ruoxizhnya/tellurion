package bootstrap_test

import (
	"bytes"
	"testing"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"tellurion/pkg/auth"
	"tellurion/pkg/bootstrap"
)

// TestLogAuthPosture_OpenAccessOffLoopback_WarnsExposed 钉住第三态的 WARN 内容：
// 非回环绑定 + 显式声明 → 允许放开，但日志必须同时写出「写面 / 角色校验无鉴权」、
// 绑定地址，以及「已对外暴露」。这几句话是事后审计唯一能指着看的证据。
func TestLogAuthPosture_OpenAccessOffLoopback_WarnsExposed(t *testing.T) {
	var buf bytes.Buffer
	v := viper.New()
	v.Set("server.host", "0.0.0.0")
	v.Set("auth.allow_insecure", true)
	v.Set("auth.insecure_exposure", bootstrap.InsecureExposureLoopbackPublished)
	svc := auth.NewService(nil, auth.Config{OpenAccess: true})

	bootstrap.LogAuthPosture(zerolog.New(&buf), svc, v)
	out := buf.String()

	assert.Contains(t, out, `"level":"warn"`)
	assert.Contains(t, out, "写面与角色校验无鉴权", "WARN 必须点名写面/角色校验无鉴权")
	assert.Contains(t, out, "0.0.0.0", "WARN 必须写出绑定地址")
	assert.Contains(t, out, "已对外暴露", "非回环绑定必须点名已对外暴露")
	assert.Contains(t, out, `"exposed_beyond_loopback":true`)
}

// TestLogAuthPosture_OpenAccessLoopback_NoExposureClaim：回环绑定时同样 WARN，
// 但不得谎称「已对外暴露」。
func TestLogAuthPosture_OpenAccessLoopback_NoExposureClaim(t *testing.T) {
	var buf bytes.Buffer
	v := viper.New()
	v.Set("server.host", "127.0.0.1")
	v.Set("auth.allow_insecure", true)
	svc := auth.NewService(nil, auth.Config{OpenAccess: true})

	bootstrap.LogAuthPosture(zerolog.New(&buf), svc, v)
	out := buf.String()

	assert.Contains(t, out, `"level":"warn"`)
	assert.Contains(t, out, "写面与角色校验无鉴权")
	assert.Contains(t, out, "127.0.0.1")
	assert.NotContains(t, out, "已对外暴露")
}

// TestLogAuthPosture_Unavailable_WarnsDegraded：第二态不能静默，否则运维会
// 以为写接口只是坏了。
func TestLogAuthPosture_Unavailable_WarnsDegraded(t *testing.T) {
	var buf bytes.Buffer
	v := viper.New()
	v.Set("server.host", "127.0.0.1")
	svc := auth.NewService(nil, auth.Config{})

	bootstrap.LogAuthPosture(zerolog.New(&buf), svc, v)
	out := buf.String()

	assert.Contains(t, out, `"level":"warn"`)
	assert.Contains(t, out, `"state":"unavailable"`)
	assert.Contains(t, out, "auth_unavailable")
}
