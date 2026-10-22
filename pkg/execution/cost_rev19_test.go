package execution

// REV-19：fixed 滑点是否真的读配置。
//
// 旧行为：`case "fixed": slippage := fees.FixedSlippageRate` 硬编码 —— 用户在
// `backtest.slippage_rate` 里配什么数字，对**生产路径**（ExecutionService →
// ApplyTrade）的成交价都没有任何影响。paper 侧 MockTrader 默认 0.01%、
// 生产侧硬编码 0.1%，差 10 倍且没人知道。

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/ruoxizhnya/tellurion-contracts/fees"
)

// TestCostModel_FixedSlippageUsesConfiguredRate：配置值真的生效。
func TestCostModel_FixedSlippageUsesConfiguredRate(t *testing.T) {
	t.Parallel()

	cm := NewCostModel(domain.ExecutionConfig{SlippageModel: "fixed", SlippageRate: 0.0005})
	const price = 100.0

	assert.InDelta(t, 100.05, cm.SlippagePrice(price, domain.DirectionLong, 100, 1e6, 101, 99), 1e-9,
		"买入价应为 price×(1+0.0005)")
	assert.InDelta(t, 99.95, cm.SlippagePrice(price, domain.DirectionShort, 100, 1e6, 101, 99), 1e-9,
		"卖出价应为 price×(1-0.0005)")
}

// TestCostModel_FixedSlippageFallsBackWhenUnset：零值回落 0.001（现行为不变）。
//
// 这条是「接线不改变默认行为」的证据：没配 = 与 REV-19 之前完全一致。
func TestCostModel_FixedSlippageFallsBackWhenUnset(t *testing.T) {
	t.Parallel()

	cm := NewCostModel(domain.ExecutionConfig{SlippageModel: "fixed"})
	const price = 100.0
	want := price * (1 + fees.FixedSlippageRate)

	assert.InDelta(t, want, cm.SlippagePrice(price, domain.DirectionLong, 100, 1e6, 101, 99), 1e-9)
	assert.Equal(t, 0.001, fees.FixedSlippageRate, "回落常量本身是 0.1%")
}

// TestCostModel_OtherModelsUnaffected：不得顺手改 variable/impact/none 的语义。
func TestCostModel_OtherModelsUnaffected(t *testing.T) {
	t.Parallel()

	const price = 100.0

	none := NewCostModel(domain.ExecutionConfig{SlippageModel: "none", SlippageRate: 0.05})
	assert.InDelta(t, price, none.SlippagePrice(price, domain.DirectionLong, 100, 1e6, 101, 99), 1e-9,
		"none 模型必须原样返回，配置的 rate 不该渗进来")

	// variable：(high-low)/price × 0.1，与 SlippageRate 无关。
	variable := NewCostModel(domain.ExecutionConfig{SlippageModel: "variable", SlippageRate: 0.05})
	assert.InDelta(t, 100*(1+0.02*0.1),
		variable.SlippagePrice(price, domain.DirectionLong, 100, 1e6, 101, 99), 1e-9)
}
