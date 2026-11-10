package trading_test

import (
	"math"
	"testing"
	"time"

	cdom "github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/ruoxizhnya/tellurion-contracts/fees"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"tellurion/pkg/backtest/execution"
	"tellurion/pkg/trading"
)

import "tellurion/pkg/domain"

// 本文件在**外部测试包** live_test 里：它要 import pkg/backtest/execution 做
// 成本同构比对，而 pkg/backtest/execution 又 import pkg/trading（live_bridge.go）。
// 若用内部包 live，就会构成 test→backtest/execution→live 的 import cycle。
// 外部测试包 live_test 是独立的包，因此不成环。

const (
	isoSymbol   = "000001.SZ"
	isoRefPrice = 10.0
	isoHigh     = 11.0
	isoLow      = 9.0
	isoADV      = 100000.0
	isoQty      = 10000.0
)

func isoQuote(volume float64) execution.Quote {
	return execution.Quote{
		Symbol: isoSymbol, Open: 10.0, High: isoHigh, Low: isoLow, Close: isoRefPrice,
		Volume: volume, Date: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC),
	}
}

// TestCostIsomorphism_Fixed 是成本同构的**核心正证据（fixed）**：同一笔
// (symbol, side, qty, config)、同一参考价与 ADV，paper 侧 ComputePaperCost
// 与回测 pkg/backtest/execution 算出的成交价/佣金逐位相等（1e-9）。
func TestCostIsomorphism_Fixed(t *testing.T) {
	cfg := domain.DefaultExecutionConfig() // SlippageModel == "fixed"
	require.Equal(t, "fixed", cfg.SlippageModel)

	back := execution.NewBacktestExecutionService(cfg)
	order := domain.Order{Symbol: isoSymbol, Direction: cdom.DirectionLong, OrderType: domain.OrderTypeMarket, Quantity: isoQty}
	quote := isoQuote(isoADV)

	tr, err := back.ExecuteOrder(order, quote)
	require.NoError(t, err)

	pc, err := trading.ComputePaperCost(cfg, order, quote.Close, quote.High, quote.Low, quote.Volume)
	require.NoError(t, err)

	t.Logf("fixed  回测: fill=%.10f commission=%.10f", tr.Price, tr.Commission)
	t.Logf("fixed  paper: fill=%.10f commission=%.10f", pc.FillPrice, pc.Commission)
	assert.InDelta(t, tr.Price, pc.FillPrice, 1e-9, "成交价必须同构")
	assert.InDelta(t, tr.Commission, pc.Commission, 1e-9, "佣金必须同构")

	// 独立解析锚点：两处（paper/回测）都走共享核，若核本身把 fixed 参数
	// 改错，两边会同步变、同构断言仍绿 —— 故再用「文档公式」独立钉死数值，
	// 让成本核被改坏时本用例必红。
	wantFill := isoRefPrice * (1 + fees.FixedSlippageRate)
	wantCommission := wantFill * isoQty * cfg.CommissionRate
	assert.InDelta(t, wantFill, pc.FillPrice, 1e-12, "fixed 买单成交价 == refPrice*(1+FixedSlippageRate)")
	assert.InDelta(t, wantCommission, pc.Commission, 1e-9, "fixed 佣金 == fill*qty*CommissionRate")
}

// TestCostIsomorphism_Impact 是成本同构的**核心正证据（impact）**。
func TestCostIsomorphism_Impact(t *testing.T) {
	cfg := domain.DefaultExecutionConfig()
	cfg.SlippageModel = "impact"
	cfg.ImpactSigma = 0.02        // 日波动率 2%
	cfg.ImpactLiquidityFactor = 0 // 文档默认 1.0

	back := execution.NewBacktestExecutionService(cfg)
	order := domain.Order{Symbol: isoSymbol, Direction: cdom.DirectionLong, OrderType: domain.OrderTypeMarket, Quantity: isoQty}
	quote := isoQuote(isoADV)

	tr, err := back.ExecuteOrder(order, quote)
	require.NoError(t, err)

	pc, err := trading.ComputePaperCost(cfg, order, quote.Close, quote.High, quote.Low, quote.Volume)
	require.NoError(t, err)

	t.Logf("impact 回测: fill=%.10f commission=%.10f", tr.Price, tr.Commission)
	t.Logf("impact paper: fill=%.10f commission=%.10f", pc.FillPrice, pc.Commission)
	assert.InDelta(t, tr.Price, pc.FillPrice, 1e-9, "成交价必须同构")
	assert.InDelta(t, tr.Commission, pc.Commission, 1e-9, "佣金必须同构")
	// 顺带确认 impact 确实生效：成交价高于参考价（买单吃冲击）。
	assert.Greater(t, pc.FillPrice, isoRefPrice)

	// 独立解析锚点：impact = sigma*sqrt(qty/adv)*liquidityFactor(=1.0)。
	// 成本核若把 Sigma 等参数改错，本断言必红（同构断言会因两边同步变而仍绿）。
	impact := cfg.ImpactSigma * math.Sqrt(isoQty/isoADV) * 1.0
	wantFill := isoRefPrice * (1 + impact)
	assert.InDelta(t, wantFill, pc.FillPrice, 1e-12, "impact 买单成交价 == refPrice*(1+sigma*sqrt(qty/adv))")
}
