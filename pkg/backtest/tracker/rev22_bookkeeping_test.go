package tracker

// REV-22-(2)(4)：权益曲线簿记的健壮性测试。
//
// (2) DropLastRecordedOn：日循环含最后一天 + 强平后重记同一天——不先丢旧
//     条目，权益曲线末日出现两个观测（Sharpe/波动率多计一个点）。
// (4) RecordDailyValue 缺价兜底：取不到价的持仓用最后已知价估值，而不是
//     从当日权益里无痕消失（曲线无痕跳水）。

import (
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/ruoxizhnya/tellurion-contracts/engine"
)

func rev22Tracker(t *testing.T) *Tracker {
	t.Helper()
	return NewTracker(1_000_000, 0.0003, 0.0001, contracts.DefaultTradingConfig(), zerolog.Nop())
}

func TestREV22_DropLastRecordedOn_RemovesDuplicateDay(t *testing.T) {
	t.Parallel()

	tr := rev22Tracker(t)
	d1 := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	d3 := time.Date(2024, 1, 4, 0, 0, 0, 0, time.UTC)
	prices := map[string]float64{}

	tr.RecordDailyValue(d1, prices)
	tr.RecordDailyValue(d2, prices)
	tr.RecordDailyValue(d3, prices)
	require.Len(t, tr.GetPortfolioValues(), 3)

	// 末日重记前先丢旧条目。
	require.True(t, tr.DropLastRecordedOn(d3), "末条日期匹配，应丢弃")
	tr.RecordDailyValue(d3, prices)

	values := tr.GetPortfolioValues()
	require.Len(t, values, 3, "重记后仍是 3 个观测——不因强平重记而多出重复日")
	assert.Equal(t, d3, values[len(values)-1].Date)

	// 日期不匹配时是 no-op（不误删）。
	assert.False(t, tr.DropLastRecordedOn(d1), "末条不是 d1，不该丢")
	assert.Len(t, tr.GetPortfolioValues(), 3)
}

func TestREV22_RecordDailyValue_MissingPriceFallsBackToLastKnown(t *testing.T) {
	t.Parallel()

	tr := rev22Tracker(t)
	sym := "600000.SH"

	// 建多头持仓：ExecuteTrade 1000 股 @10。
	_, err := tr.ExecuteTrade(sym, domain.DirectionLong, 1000, 10.0,
		time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC), nil)
	require.NoError(t, err)

	// 次日价格正常，CurrentPrice 更新为 11。
	tr.RecordDailyValue(time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC), map[string]float64{sym: 11.0})
	withPrice := tr.GetPortfolioValues()[len(tr.GetPortfolioValues())-1]
	// 现金含手续费扣减，不手算精确值；持仓市值 = 1000 × 11 必须在场。
	assert.InDelta(t, 11_000, withPrice.Positions, 1e-9)

	// 第三日 pricesCache 缺该 symbol（停牌/摘牌缺口）：旧代码会把 11_000 的
	// 持仓价值整个从权益里抹掉（曲线无痕跳水）；修复后用最后已知价 11 兜底。
	withoutPrice := tr.RecordDailyValue(time.Date(2024, 1, 4, 0, 0, 0, 0, time.UTC), map[string]float64{})
	assert.InDelta(t, withPrice.TotalValue, withoutPrice.TotalValue, 1e-9,
		"缺价日必须按最后已知价估值——权益不能无痕跳水（REV-22-(4)）")
	assert.InDelta(t, 11.0, withoutPrice.Positions/1000, 1e-9)
}

func TestREV22_ApplyTradeClose_BooksPartialFill(t *testing.T) {
	t.Parallel()

	tr := rev22Tracker(t)
	sym := "600000.SH"
	day1 := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	day2 := day1.AddDate(0, 0, 1)

	// T+1：day1 买入 1000 股；day2 卖出（T+1 可卖 1000，这里请求 400 = 部分平仓）。
	_, err := tr.ExecuteTrade(sym, domain.DirectionLong, 1000, 10.0, day1, nil)
	require.NoError(t, err)
	tr.AdvanceDay(day1) // T+1：隔日后昨日仓位才可卖

	closed, err := tr.ApplyTrade(domain.Trade{
		Symbol: sym, Direction: domain.DirectionClose,
		Quantity: 400, Price: 11.0, Timestamp: day2,
	})
	require.NoError(t, err)
	require.NotNil(t, closed)

	// REV-22-(5)：Close 是即时撮合，实际成交 400 —— FilledQty/PendingQty
	// 必须如实簿记（ExecuteTrade 的开仓路径早就这么做了，Close 路径漏了）。
	assert.Exactly(t, 400.0, closed.FilledQty, "实际成交量必须进 FilledQty")
	assert.Exactly(t, 0.0, closed.PendingQty, "Close 无未成交剩余")
	assert.Exactly(t, 400.0, closed.Quantity)
}
