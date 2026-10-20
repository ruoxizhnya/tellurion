package backtest

// REV-19 装配腿：`NewEngine` 必须把**用户显式配置**的 slippage_rate 透传给
// 执行服务；未配置时传 0（执行服务回落 0.001，现行为不变）。
//
// 为什么这条不能用行为断言：配置值 0.0005 与默认 0.001 的成交价差异（每边
// 0.05 元/100 元）会被后续整手量化、风控权重、费用计算层层吸收，从净值曲线
// 反推「配置生效了没有」既脆又难读。装配置本身是结构事实，直接读它。

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/backtest/execution"
	"tellurion/pkg/logging"
)

// newEngineWithSlippage 造一个只关心滑点装配的引擎（其余配置取默认）。
func newEngineWithSlippage(t *testing.T, rate float64) *Engine {
	t.Helper()
	v := viper.New()
	if rate > 0 {
		v.Set("backtest.slippage_rate", rate)
	}
	// 复用 engine_accessors_test.go 的 mockProvider（空实现，本测试不取数）。
	eng, err := NewEngine(v, &mockProvider{name: "rev19"}, logging.WithContext(map[string]any{}))
	require.NoError(t, err)
	return eng
}

// TestNewEngine_WiresExplicitSlippageRate：显式配置 → 执行服务拿到该值。
func TestNewEngine_WiresExplicitSlippageRate(t *testing.T) {
	t.Parallel()

	eng := newEngineWithSlippage(t, 0.0005)

	svc, ok := eng.ExecutionBridge().Get().(*execution.BacktestExecutionService)
	require.True(t, ok, "默认执行服务应是 *execution.BacktestExecutionService")
	assert.Equal(t, 0.0005, svc.Config().SlippageRate,
		"用户在 backtest.slippage_rate 配的值必须传到执行服务（REV-19 之前这里恒为 0，即永远用硬编码 0.001）")
}

// TestNewEngine_UnsetSlippageRateStaysZero：未配置 → 传 0 → 执行服务回落 0.001。
//
// 关键细节：`backtest.Config.SlippageRate` 的默认值是 0.0001（tracker/paper
// 口径），而执行服务的 fixed 分支默认是 0.001（0.1%）。如果把默认值也透传，
// 生产滑点会从 0.1% 静默变成 0.01% —— 那不是本单要做的事。所以 NewEngine
// 抓的是**默认值覆盖之前**的原始配置。
func TestNewEngine_UnsetSlippageRateStaysZero(t *testing.T) {
	t.Parallel()

	eng := newEngineWithSlippage(t, 0)

	svc, ok := eng.ExecutionBridge().Get().(*execution.BacktestExecutionService)
	require.True(t, ok)
	assert.Zero(t, svc.Config().SlippageRate,
		"未配置时必须传 0，让执行服务回落 0.001（保持 REV-19 之前的默认行为）")

	// 引擎自己的 tracker 口径不受影响：仍是 DefaultSlippageRate。
	assert.Equal(t, DefaultSlippageRate, eng.config.SlippageRate,
		"backtest.Config 的默认口径（0.0001）不该被本单改动")
}
