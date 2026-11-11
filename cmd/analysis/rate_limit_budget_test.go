package main

// AUD-58 的窄测试：**证明那个旋钮真的有效**。
//
// 为什么需要这一条：AUD-58 的落地方式是「给 e2e 栈显式设一个高额度」
// （`RATE_LIMIT_PER_MINUTE`）。2026-11-11 单一 compose 定案后不再有
// docker-compose.e2e.yml 覆盖层，高额度改由命令行 env 覆盖表达。如果这个 env
// 覆盖根本没接上，e2e 会继续拿到 429，而**看起来**问题已经处理过了 ——
// 命令里明明写着 high。那就是一个安慰剂。
//
// 四条腿，各自独立（故意写成互不依赖，因为「兜底值恰好等于配置值」会让
// 「读了配置」和「用了兜底」长得一模一样 —— 一种会假绿的形状）：
//
//	A. rateLimitPerMinute 真的是 viper 的函数，不是常量；
//	B. 仓库里 shipped 的 yaml 给出一个**真实的**限额（且不许被调高 —— 见下）；
//	C. 那条 shipped 值确实流过 rateLimitPerMinute；
//	D. env 覆盖压过 yaml（e2e 高额度姿势的唯一支点）。
//
// 另一半：**默认必须仍是一个真实的限额**。否则「把默认值调高」会成为修 AUD-58
// 的捷径 —— 那等于把限流从本地开发里静默删掉，比拿到 429 更糟（429 至少看得见）。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 测试工作目录是 cmd/analysis，配置在仓库根的 config/ 下。
func shippedAnalysisConfig(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "config", "analysis-service.yaml"))
	require.NoError(t, err)
	return p
}

// wiredLikeLoadConfig 复刻 loadConfig 里与限流有关的那几行装配。
// 刻意不调 loadConfig 本身：它会 logger.Fatal，还会改 zerolog 的全局级别。
func wiredLikeLoadConfig(t *testing.T) *viper.Viper {
	t.Helper()
	v := viper.New()
	v.SetConfigFile(shippedAnalysisConfig(t))
	v.SetConfigType("yaml")
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	require.NoError(t, v.ReadInConfig())
	return v
}

// shippedRateLimit 直接读 yaml，不经 viper 的 env 层。
func shippedRateLimit(t *testing.T) int {
	t.Helper()
	raw := viper.New()
	raw.SetConfigFile(shippedAnalysisConfig(t))
	raw.SetConfigType("yaml")
	require.NoError(t, raw.ReadInConfig())
	return raw.GetInt("rate_limit.per_minute")
}

// A. 它是 viper 的函数，不是一个常量。
// 没有这条，「读配置」与「回落到写死的 100」在测试里长得一模一样 ——
// 而后者意味着改 yaml 毫无效果。
func TestRateLimitPerMinute_IsAFunctionOfViper(t *testing.T) {
	v := viper.New()
	v.Set("rate_limit.per_minute", 42)

	assert.Equal(t, 42, rateLimitPerMinute(v),
		"没读 viper 的 rate_limit.per_minute —— 配置成了装饰")
}

// B. shipped 值必须是一个真实的限额，且不许被调高。
func TestRateLimitPerMinute_ShippedDefaultIsStillARealLimit(t *testing.T) {
	shipped := shippedRateLimit(t)

	require.Greater(t, shipped, 0,
		"config/analysis-service.yaml 必须给出一个正的限额（0 或负数等于关掉限流）")

	// 上界不是审美判据，是**反捷径**判据：AUD-58 不能靠「把默认调高」来修。
	// 真要让整套 e2e 跑得动，走命令行 env 覆盖（`RATE_LIMIT_PER_MINUTE=100000`）。
	assert.LessOrEqual(t, shipped, 600,
		"默认限额被调大了 —— 那是把限流从本地开发里静默删掉。"+
			"抬高额度必须是 e2e 专用命令行 env 覆盖，不是改默认值（AUD-58）")
}

// C. shipped 值确实流过 rateLimitPerMinute。
func TestRateLimitPerMinute_UsesShippedConfigValue(t *testing.T) {
	// 清掉覆盖，确保读到的是 yaml（viper 默认把空 env 当作「没设」）。
	t.Setenv("RATE_LIMIT_PER_MINUTE", "")

	assert.Equal(t, shippedRateLimit(t), rateLimitPerMinute(wiredLikeLoadConfig(t)),
		"没有 env 覆盖时必须用配置文件里的值")
}

// D. env 覆盖必须真的生效 —— 这是 e2e 高额度姿势的唯一支点。
// 同时这条也钉住了「**env 压过 yaml**」这个优先级（viper 的 env 层在 config 之上）。
func TestRateLimitPerMinute_EnvOverrideActuallyTakesEffect(t *testing.T) {
	t.Setenv("RATE_LIMIT_PER_MINUTE", "100000")

	v := wiredLikeLoadConfig(t)
	shipped := shippedRateLimit(t)
	require.NotEqual(t, 100000, shipped, "前提：覆盖值与 shipped 值不同，否则这条腿测不出东西")

	assert.Equal(t, 100000, rateLimitPerMinute(v),
		"RATE_LIMIT_PER_MINUTE 没接上：e2e 的「高额度」姿势就只是个安慰剂，"+
			"问题会看起来已经处理过，实际仍在 429")
}

// E. 最后一块：**默认值必须是 100、且必须可被 env 覆盖**。
//
// 2026-11-11 单一 compose 定案后，e2e 不再有覆盖层文件：高额度改由命令行
// `RATE_LIMIT_PER_MINUTE=100000 docker compose up ...` 表达。所以这条腿钉两件事：
//  1. 主文件里的默认值是 `${RATE_LIMIT_PER_MINUTE:-100}`（不是死值）；
//  2. 部署面（*.yml / *.yaml）里**不得**再出现把 100000 写死的赋值 ——
//     一旦硬编码，env 覆盖就会失效而测试仍绿。
//
// data-service **有意不接这个旋钮**：2026-11-10（`8fd415d`）起它的镜像由
// `tellurion-data` 仓发布，限流是数据仓内的固定档（50 rps / 突发 200），**不读**
// `RATE_LIMIT_PER_MINUTE` —— 补回去只会得到一条看起来可调、实际不生效的配置。
func TestCompose_DefaultRateLimitIs100AndOverridable(t *testing.T) {
	composePath, err := filepath.Abs(filepath.Join("..", "..", "docker-compose.yml"))
	require.NoError(t, err)

	raw := viper.New()
	raw.SetConfigFile(composePath)
	raw.SetConfigType("yaml")
	require.NoError(t, raw.ReadInConfig(), "docker-compose.yml 必须存在且可解析")

	assert.Equal(t, "${RATE_LIMIT_PER_MINUTE:-100}",
		raw.GetString("services.analysis-service.environment.RATE_LIMIT_PER_MINUTE"),
		"默认限流必须是可覆盖的 100（`${RATE_LIMIT_PER_MINUTE:-100}`），不能写死")

	assert.False(t, raw.IsSet("services.data-service.environment.RATE_LIMIT_PER_MINUTE"),
		"data-service 不该接 RATE_LIMIT_PER_MINUTE：它的限流是 tellurion-data 仓内的固定档")

	// 只查「YAML 赋值」形态的硬编码；文档与命令行示例里的 `=100000` 不算 ——
	// 那个值正是必须能被覆盖的东西，禁掉它等于禁掉 e2e 姿势。
	hardcoded := regexp.MustCompile(`(?m)^\s*(?:RATE_LIMIT_PER_MINUTE|rate_limit_per_minute)\s*:\s*["']?100000\b`)
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)

	var offenders []string
	require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "reports", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".yml" && ext != ".yaml" {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		if hardcoded.Match(body) {
			rel, _ := filepath.Rel(root, path)
			offenders = append(offenders, rel)
		}
		return nil
	}))
	assert.Empty(t, offenders,
		"部署面里出现了写死的 100000：env 覆盖会失效。e2e 高额度请用命令行 RATE_LIMIT_PER_MINUTE=100000")
}
