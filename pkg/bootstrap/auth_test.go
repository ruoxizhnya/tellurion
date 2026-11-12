package bootstrap_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"tellurion/pkg/bootstrap"
)

// P0-4（三态口径，2026-11-12 主任修正）：
//  1. 有 secret → 全闸；
//  2. 无 secret + 无显式声明 + 回环绑定 → 降级启动（写面 503）；
//  3. 无 secret + 显式声明（AUTH_INSECURE=true 或 exposure=loopback-published）
//     → 放开，**绑定地址不限**（容器必须绑 0.0.0.0），WARN 披露风险；
//  4. 无 secret + 无声明 + 非回环绑定 → 拒绝启动。
// 本文件把四条腿逐条钉住。

func TestDecideAuthStartup_SecretPresent_Enabled(t *testing.T) {
	d := bootstrap.DecideAuthStartup("s3cret", false, "0.0.0.0", "")
	assert.False(t, d.Refuse)
	assert.Equal(t, []byte("s3cret"), d.Secret)
	assert.False(t, d.Insecure)
	assert.False(t, d.Unavailable)
}

func TestDecideAuthStartup_NoSecretLoopback_Unavailable(t *testing.T) {
	d := bootstrap.DecideAuthStartup("", false, "127.0.0.1", "")
	assert.False(t, d.Refuse, "回环绑定下进程应能起，降级为 unavailable")
	assert.True(t, d.Unavailable, "缺 secret 且未显式声明时必须降级，不能 open-access")
	assert.False(t, d.Insecure, "降级态绝不能同时被标成显式放开")
	assert.Empty(t, d.Secret)
}

func TestDecideAuthStartup_WhitespaceSecret_Unavailable(t *testing.T) {
	// 只有空白字符的密钥等于没有密钥（YAML 里 `jwt_secret: "  "` 很容易出现）。
	d := bootstrap.DecideAuthStartup("   ", false, "127.0.0.1", "")
	assert.True(t, d.Unavailable)
	assert.False(t, d.Refuse)
}

func TestDecideAuthStartup_NoSecretOffLoopback_Refuses(t *testing.T) {
	// 非 loopback 且没有任何显式声明：既无鉴权、也没有「这是回环暴露」的承诺，
	// 拒绝启动（fail closed）。
	for _, host := range []string{"0.0.0.0", "192.168.1.10", "", "[::]"} {
		d := bootstrap.DecideAuthStartup("", false, host, "")
		assert.True(t, d.Refuse, "非 loopback 且无显式声明时必须拒绝启动: %q", host)
		assert.Contains(t, d.Reason, "JWT_SECRET", "拒绝原因必须给出可操作的修复指引")
		assert.Empty(t, d.Secret)
	}
}

func TestDecideAuthStartup_InsecureOnLoopback_Allowed(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "localhost", "127.0.1.5", "::1"} {
		d := bootstrap.DecideAuthStartup("", true, host, "")
		assert.False(t, d.Refuse, "loopback 上的显式声明应放行: %s", host)
		assert.True(t, d.Insecure)
		assert.Empty(t, d.Secret)
	}
}

// TestDecideAuthStartup_DeclaredOffLoopback_Opens 是主任新增的要求：
// 非回环绑定 + 显式声明 → 能起且放开（容器里 core 绑 0.0.0.0，不能拒启）。
// 风险由 InitAuth 的 WARN 披露，见 auth_log_test.go。
func TestDecideAuthStartup_DeclaredOffLoopback_Opens(t *testing.T) {
	for _, host := range []string{"0.0.0.0", "192.168.1.10", "", "[::]"} {
		d := bootstrap.DecideAuthStartup("", true, host, "")
		assert.False(t, d.Refuse, "显式声明 AUTH_INSECURE=true 后任何绑定都应放行: %q", host)
		assert.True(t, d.Insecure)
		assert.False(t, d.Unavailable)
	}
}

func TestDecideAuthStartup_SecretWinsOverInsecure(t *testing.T) {
	// 配了密钥就是开了鉴权，不该再被标成 insecure。
	d := bootstrap.DecideAuthStartup("s3cret", true, "0.0.0.0", "")
	assert.False(t, d.Refuse)
	assert.False(t, d.Insecure)
}

// ── 显式声明（2026-09-25 新增 / 2026-11-12 修正）──────────────────────
//
// 两种声明任一即可：AUTH_INSECURE=true 或 AUTH_INSECURE_EXPOSURE=loopback-published。
// 声明后绑定地址不限（容器必须 0.0.0.0）。声明必须精确：拼错一律按没声明处理。

func TestDecideAuthStartup_ExposureAloneOffLoopback_Opens(t *testing.T) {
	d := bootstrap.DecideAuthStartup("", false, "0.0.0.0", bootstrap.InsecureExposureLoopbackPublished)
	assert.False(t, d.Refuse, "exposure 声明本身就是显式声明，允许任何绑定放开")
	assert.True(t, d.Insecure)
	assert.False(t, d.Unavailable)
	assert.Empty(t, d.Secret)
}

func TestDecideAuthStartup_ContainerExposureTypo_Refuses(t *testing.T) {
	// 反证腿：声明必须逐字匹配。拼错/大小写/近义词/布尔值一律 fail closed ——
	// 这一条掉了，等于这个开关可以靠一个笔误打开。这里不叠加 AUTH_INSECURE，
	// 否则单独的 AUTH_INSECURE=true 就会让用例失去意义。
	for _, exposure := range []string{
		"", "loopback", "loopback-only", "published", "true", "1",
		"LOOPBACK-PUBLISHED", "Loopback-Published", "loopback_published",
	} {
		d := bootstrap.DecideAuthStartup("", false, "0.0.0.0", exposure)
		assert.True(t, d.Refuse, "exposure 声明不精确且无其他声明时必须拒绝: %q", exposure)
		assert.Contains(t, d.Reason, "AUTH_INSECURE_EXPOSURE",
			"拒绝原因必须告诉 operator 正确的声明名")
	}
}

func TestIsLoopbackHost(t *testing.T) {
	assert.True(t, bootstrap.IsLoopbackHost("127.0.0.1"))
	assert.True(t, bootstrap.IsLoopbackHost("127.255.255.254"))
	assert.True(t, bootstrap.IsLoopbackHost("localhost"))
	assert.True(t, bootstrap.IsLoopbackHost("::1"))
	assert.True(t, bootstrap.IsLoopbackHost("[::1]"))

	assert.False(t, bootstrap.IsLoopbackHost("0.0.0.0"))
	assert.False(t, bootstrap.IsLoopbackHost("10.0.0.5"))
	assert.False(t, bootstrap.IsLoopbackHost("192.168.0.1"))
	assert.False(t, bootstrap.IsLoopbackHost(""))
	assert.False(t, bootstrap.IsLoopbackHost("localhost.example.com"))
}
