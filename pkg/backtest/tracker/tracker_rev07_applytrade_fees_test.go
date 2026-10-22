package tracker

// REV-07 对拍测试：生产路径（ExecuteOrder → ApplyTrade）与 ExecuteTrade
// （内存路径）在**同场景**下三项费用（佣金/过户费/印花税）逐分相等。
//
// 背景：ApplyTrade 曾原样采信 ExecutionService 只设了 Commission 的 trade，
// TransferFee/StampTax 恒零 ⇒ 生产路径过户费、印花税全免，而 ExecuteTrade
// 与 MockTrader 照常收——两条路径成本口径断裂，方向永远是回测高估收益。
// 修复：ApplyTrade 的 Long/Short 分支改用 portfolio.ComputeFees 重算并覆盖
// trade 费用字段（与 Close 分支、ExecuteTrade 同构）。
//
// 口径：两侧滑点都置零（tracker slippageRate=0 + fixed 模型 rate=0），
// 保证两路径同一成交价、同一 tradeValue，费用差异只可能来自口径本身。

import (
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/ruoxizhnya/tellurion-contracts/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"tellurion/pkg/backtest/execution"
)

const (
	rev07Symbol = "600000.SH"
	rev07Qty    = 1000.0
	rev07Price  = 10.0
)

var rev07TS = time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC) // 2023-08-28 后：印花税 0.05% 分段

func rev07Tracker(t *testing.T) *Tracker {
	t.Helper()
	// 滑点置零：两路径同一成交价，费用对拍才只反映口径差异。
	return NewTracker(1_000_000, 0.00025, 0, contracts.DefaultTradingConfig(), zerolog.Nop())
}

func rev07ExecService() *execution.BacktestExecutionService {
	// "none" 模型不加滑点（pkg/execution SlippagePrice 的显式分支），
	// 与 tracker slippageRate=0 对齐：两路径同一成交价。
	return execution.NewBacktestExecutionService(domain.ExecutionConfig{
		SlippageModel: "none",
	})
}

func rev07Quote() execution.Quote {
	return execution.Quote{
		Symbol: rev07Symbol,
		Open:   rev07Price, High: rev07Price, Low: rev07Price, Close: rev07Price,
		Volume: 1_000_000,
		Date:   rev07TS,
	}
}

// rev07ApplyTradePath 走生产路径：ExecuteOrder → ApplyTrade，返回落账后的 trade。
func rev07ApplyTradePath(t *testing.T, tr *Tracker, direction domain.Direction) *domain.Trade {
	t.Helper()
	svc := rev07ExecService()
	order := domain.Order{
		Symbol:    rev07Symbol,
		Direction: direction,
		OrderType: domain.OrderTypeMarket,
		Quantity:  rev07Qty,
	}
	svcTrade, err := svc.ExecuteOrder(order, rev07Quote())
	require.NoError(t, err)
	require.InDelta(t, rev07Price, svcTrade.Price, 1e-9, "零滑点：成交价应为报价原值")
	applied, err := tr.ApplyTrade(svcTrade)
	require.NoError(t, err)
	return applied
}

// TestApplyTrade_FeesMatchExecuteTrade_Buy：买入对拍——佣金/过户费/印花税
// 逐分相等，且过户费非零（修复点：此前恒零）。
func TestApplyTrade_FeesMatchExecuteTrade_Buy(t *testing.T) {
	trA := rev07Tracker(t)
	tradeA, err := trA.ExecuteTrade(rev07Symbol, domain.DirectionLong, rev07Qty, rev07Price, rev07TS, nil)
	require.NoError(t, err)

	trB := rev07Tracker(t)
	tradeB := rev07ApplyTradePath(t, trB, domain.DirectionLong)

	assert.InDelta(t, tradeA.Commission, tradeB.Commission, 1e-9, "佣金应逐分相等")
	assert.InDelta(t, tradeA.TransferFee, tradeB.TransferFee, 1e-9, "过户费应逐分相等")
	assert.InDelta(t, tradeA.StampTax, tradeB.StampTax, 1e-9, "印花税应逐分相等（买入两侧都为零）")
	assert.Zero(t, tradeB.StampTax, "买入不收印花税")
	assert.Greater(t, tradeB.TransferFee, 0.0, "过户费必须收——REV-07 修复点，此前恒零")
	assert.Greater(t, tradeB.Commission, 0.0, "佣金必须收")

	// 现金变动也相等（费用同构 ⇒ 现金收付同构）。
	assert.InDelta(t, trA.GetCash(), trB.GetCash(), 1e-9, "买入后现金应逐分相等")
}

// TestApplyTrade_FeesMatchExecuteTrade_ShortOpen：卖空开仓对拍——卖出侧
// 印花税 + 过户费逐分相等，且印花税非零（修复点：此前被 _ = trade.StampTax
// 丢弃）。
func TestApplyTrade_FeesMatchExecuteTrade_ShortOpen(t *testing.T) {
	trA := rev07Tracker(t)
	tradeA, err := trA.ExecuteTrade(rev07Symbol, domain.DirectionShort, rev07Qty, rev07Price, rev07TS, nil)
	require.NoError(t, err)

	trB := rev07Tracker(t)
	tradeB := rev07ApplyTradePath(t, trB, domain.DirectionShort)

	assert.InDelta(t, tradeA.Commission, tradeB.Commission, 1e-9, "佣金应逐分相等")
	assert.InDelta(t, tradeA.TransferFee, tradeB.TransferFee, 1e-9, "过户费应逐分相等")
	assert.InDelta(t, tradeA.StampTax, tradeB.StampTax, 1e-9, "印花税应逐分相等")
	assert.Greater(t, tradeB.StampTax, 0.0, "卖空开仓必须收印花税——REV-07 修复点，此前全免")
	assert.Greater(t, tradeB.TransferFee, 0.0, "过户费必须收")

	// 现金变动相等（卖空开仓是现金流入，费用压低流入额）。
	assert.InDelta(t, trA.GetCash(), trB.GetCash(), 1e-9, "卖空开仓后现金应逐分相等")

	// 持仓口径也一致（空头 -1000）。
	posA, ok := trA.GetPosition(rev07Symbol)
	require.True(t, ok)
	posB, ok := trB.GetPosition(rev07Symbol)
	require.True(t, ok)
	assert.InDelta(t, posA.Quantity, posB.Quantity, 1e-9)
}

// TestApplyTrade_Fees_StampTaxDateSegmentation 顺带钉住分段税率在两条
// 路径一致（AUD-20 的 asOf 语义不得因 REV-07 回归）：2023-08-28 前的卖出
// 按 0.1% 收，两路径仍逐分相等。
func TestApplyTrade_Fees_StampTaxDateSegmentation(t *testing.T) {
	preCut := time.Date(2023, 1, 3, 0, 0, 0, 0, time.UTC)

	trA := rev07Tracker(t)
	tradeA, err := trA.ExecuteTrade(rev07Symbol, domain.DirectionShort, rev07Qty, rev07Price, preCut, nil)
	require.NoError(t, err)

	trB := rev07Tracker(t)
	svc := rev07ExecService()
	svcTrade, err := svc.ExecuteOrder(domain.Order{
		Symbol: rev07Symbol, Direction: domain.DirectionShort,
		OrderType: domain.OrderTypeMarket, Quantity: rev07Qty,
	}, execution.Quote{Symbol: rev07Symbol, Open: rev07Price, High: rev07Price, Low: rev07Price, Close: rev07Price, Volume: 1e6, Date: preCut})
	require.NoError(t, err)
	tradeB, err := trB.ApplyTrade(svcTrade)
	require.NoError(t, err)

	assert.InDelta(t, tradeA.StampTax, tradeB.StampTax, 1e-9, "分段前卖出印花税应逐分相等")
	assert.InDelta(t, 10.0, tradeB.StampTax, 1e-9, "2023-08-28 前应按 0.1% 收（10,000 × 0.1%）")
}
