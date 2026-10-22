// ⚠️ 口径声明（REV-06，2026-10-17）：本文件所有用例钉的是**原始价（raw）**
// 口径的除权改账语义。tracker.ProcessSplit / tracker.ProcessDividend 是
// ExecuteTrade 内存路径（原始价数据源 / paper）的专用语义，**不是 qfq 生产
// 口径**——引擎日循环（qfq 数据源）已不再调用它们改账：前复权序列按构造
// 平滑了除权跳空，再改账即双重计算（裁决 a）。裁决与护栏测试见
// pkg/backtest/engine_daily.go 的 processCorporateActions 与
// pkg/backtest/engine_rev06_qfq_corporate_actions_test.go。
package tracker

import (
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/ruoxizhnya/tellurion-contracts/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTracker_ProcessSplit_StockDividend —— 原始价口径：10 送 5，股数 1000→1500、
// 均价摊薄。仅 ExecuteTrade 内存路径专用，非 qfq 生产口径（见文件头）。
func TestTracker_ProcessSplit_StockDividend(t *testing.T) {
	logger := zerolog.Nop()
	tracker := NewTracker(1000000, 0.0003, 0.001, contracts.DefaultTradingConfig(), logger)

	symbol := "600010.SH"
	day := time.Date(2023, 1, 2, 0, 0, 0, 0, time.UTC)

	tracker.ExecuteTrade(symbol, domain.DirectionLong, 1000, 10.0, day, nil)

	cashBefore := tracker.GetCash()
	pos, exists := tracker.GetPosition(symbol)
	require.True(t, exists)
	assert.Equal(t, 1000.0, pos.Quantity)
	assert.InDelta(t, 10.0, pos.AvgCost, 0.05)

	split := domain.Split{
		Symbol:       symbol,
		TradeDate:    day,
		StkDivRatio:  0.5,
		CashDivRatio: 0.0,
	}
	err := tracker.ProcessSplit(symbol, split)
	require.NoError(t, err)

	pos, exists = tracker.GetPosition(symbol)
	require.True(t, exists)
	assert.Equal(t, 1500.0, pos.Quantity)
	assert.InDelta(t, 6.667, pos.AvgCost, 0.05)
	assert.Equal(t, cashBefore, tracker.GetCash())
}

// TestTracker_ProcessSplit_CashDividend —— 原始价口径：现金分红贷现金。
// 仅 ExecuteTrade 内存路径专用，非 qfq 生产口径（见文件头）。
func TestTracker_ProcessSplit_CashDividend(t *testing.T) {
	logger := zerolog.Nop()
	tracker := NewTracker(1000000, 0.0003, 0.001, contracts.DefaultTradingConfig(), logger)

	symbol := "600011.SH"
	day := time.Date(2023, 1, 2, 0, 0, 0, 0, time.UTC)

	tracker.ExecuteTrade(symbol, domain.DirectionLong, 1000, 10.0, day, nil)

	cashBefore := tracker.GetCash()

	split := domain.Split{
		Symbol:       symbol,
		TradeDate:    day,
		StkDivRatio:  0.0,
		CashDivRatio: 0.3,
	}
	err := tracker.ProcessSplit(symbol, split)
	require.NoError(t, err)

	expectedCashCredit := 1000.0 * 0.3
	assert.InDelta(t, cashBefore+expectedCashCredit, tracker.GetCash(), 1.0)

	pos, exists := tracker.GetPosition(symbol)
	require.True(t, exists)
	assert.Equal(t, 1000.0, pos.Quantity)
}

// TestTracker_ProcessSplit_BothDividends —— 原始价口径：送股+现金分红同时改账。
// 仅 ExecuteTrade 内存路径专用，非 qfq 生产口径（见文件头）。
func TestTracker_ProcessSplit_BothDividends(t *testing.T) {
	logger := zerolog.Nop()
	tracker := NewTracker(1000000, 0.0003, 0.001, contracts.DefaultTradingConfig(), logger)

	symbol := "600012.SH"
	day := time.Date(2023, 1, 2, 0, 0, 0, 0, time.UTC)

	tracker.ExecuteTrade(symbol, domain.DirectionLong, 1000, 10.0, day, nil)

	cashBefore := tracker.GetCash()

	split := domain.Split{
		Symbol:       symbol,
		TradeDate:    day,
		StkDivRatio:  0.2,
		CashDivRatio: 0.5,
	}
	err := tracker.ProcessSplit(symbol, split)
	require.NoError(t, err)

	pos, exists := tracker.GetPosition(symbol)
	require.True(t, exists)
	assert.Equal(t, 1200.0, pos.Quantity)

	expectedCashCredit := 1000.0 * 0.5
	assert.InDelta(t, cashBefore+expectedCashCredit, tracker.GetCash(), 1.0)
}

func TestTracker_ProcessSplit_NoPosition(t *testing.T) {
	logger := zerolog.Nop()
	tracker := NewTracker(1000000, 0.0003, 0.001, contracts.DefaultTradingConfig(), logger)

	split := domain.Split{
		Symbol:       "NOPOSITION.SH",
		TradeDate:    time.Now(),
		StkDivRatio:  0.5,
		CashDivRatio: 0.3,
	}
	err := tracker.ProcessSplit("NOPOSITION.SH", split)
	assert.NoError(t, err)
}

func TestTracker_GetTotalValue(t *testing.T) {
	logger := zerolog.Nop()
	tracker := NewTracker(1000000, 0.0003, 0.001, contracts.DefaultTradingConfig(), logger)

	symbol := "600013.SH"
	day := time.Date(2023, 1, 2, 0, 0, 0, 0, time.UTC)

	total := tracker.GetTotalValue(map[string]float64{})
	assert.Equal(t, 1000000.0, total)

	tracker.ExecuteTrade(symbol, domain.DirectionLong, 500, 20.0, day, nil)

	prices := map[string]float64{symbol: 25.0}
	total = tracker.GetTotalValue(prices)
	cashAfterBuy := tracker.GetCash()
	assert.InDelta(t, cashAfterBuy+500*25.0, total, 1.0)
}
