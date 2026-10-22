package metrics

// REV-22-(5)(7) 的测试。
//
// (7) 胜负判定扣全费用：旧代码只扣 Commission，卖侧印花税（0.05%/0.1% 分段）
// 与过户费不扣 ⇒ 「毛亏净盈」的单被记成赢，胜率虚高。构造一腿毛利 +8 元、
// 全费用 -10 元的平仓，断言 it 计为亏损。
// (5) 在 tracker 包测（ApplyTrade 的 Close 分支）。

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
)

func rev22CloseLong(price, qty float64, commission, transferFee, stampTax float64, ts time.Time) domain.Trade {
	return domain.Trade{
		Symbol:      "600000.SH",
		Direction:   domain.DirectionClose,
		Quantity:    qty,
		Price:       price,
		Commission:  commission,
		TransferFee: transferFee,
		StampTax:    stampTax,
		Timestamp:   ts,
	}
}

func TestREV22_WinLoss_UsesFullFees(t *testing.T) {
	ts := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)

	// 开多 1000 股 @10.00，平仓 @10.01：毛利 = +10 元；
	// 佣金 5 + 过户费 0.1 + 印花税 5.05 ≈ 10.15 元 ⇒ 净亏。
	trades := []domain.Trade{
		{
			Symbol: "600000.SH", Direction: domain.DirectionLong,
			Quantity: 1000, Price: 10.00, Commission: 3.0, TransferFee: 0.1,
			Timestamp: ts,
		},
		rev22CloseLong(10.01, 1000, 5.0, 0.1, 5.05, ts.Add(24*time.Hour)),
	}

	m := CalculateTradeMetrics(trades, nil)
	assert.Zero(t, m.WinTrades, "毛利 +10 但全费用 ~10.15 ⇒ 必须记为亏损（旧代码只扣佣金会误记为赢）")
	assert.Equal(t, 1, m.LoseTrades)
}

func TestREV22_WinLoss_NetWinStillWins(t *testing.T) {
	ts := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)

	// 毛利 +100，费用 ~10 ⇒ 净赢，正向腿不能误伤。
	trades := []domain.Trade{
		{
			Symbol: "600000.SH", Direction: domain.DirectionLong,
			Quantity: 1000, Price: 10.00, Commission: 3.0, TransferFee: 0.1,
			Timestamp: ts,
		},
		rev22CloseLong(10.10, 1000, 5.0, 0.1, 5.05, ts.Add(24*time.Hour)),
	}

	m := CalculateTradeMetrics(trades, nil)
	assert.Equal(t, 1, m.WinTrades)
	assert.Zero(t, m.LoseTrades)
}
