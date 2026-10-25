package execution

// P1-17 (Sprint 6, ODR-013 CQ-001, ADR-020) / QT-51 (BE-02):
// LiveBridge 独立单元测试 — 不依赖 Engine 全栈构造。
//
// QT-51 起 LiveBridge 面向 execution.ExecutionPort（不再 import pkg/live），
// 因此本文件的替身是 port 实现而不是 live.LiveTrader。
//
// 验证点：
//   1. Set / Get round-trip
//   2. ExecuteSignal port=nil 时返回 (nil, nil)（兼容旧 API）
//   3. ExecuteSignal currentPrice<=0 报 invalid price 错
//   4. ExecuteSignal 市价单 / 限价单分别走 price=0 / price=LimitPrice
//   5. quantity scaling 与强度公式与旧实现 byte-equal
//   6. ExecuteSignals 批量跳过缺价信号
//   7. HealthCheck nil / healthy / unhealthy 三态

import (
	"context"
	"errors"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
)

// fakeExecutionPort — 测试用 ExecutionPort stub，只记录调用、不写 broker。
type fakeExecutionPort struct {
	name        string
	submitCalls []fakeSubmitCall
	submitErr   error
	healthErr   error

	// Optional hook to override the returned OrderResult
	resultHook func(req OrderRequest) *OrderResult
}

type fakeSubmitCall struct {
	Symbol    string
	Direction domain.Direction
	OrderType domain.OrderType
	Quantity  float64
	Price     float64
}

func (f *fakeExecutionPort) SubmitOrder(ctx context.Context, req OrderRequest) (*OrderResult, error) {
	f.submitCalls = append(f.submitCalls, fakeSubmitCall{
		Symbol:    req.Symbol,
		Direction: req.Direction,
		OrderType: req.OrderType,
		Quantity:  req.Quantity,
		Price:     req.Price,
	})
	if f.submitErr != nil {
		return nil, f.submitErr
	}
	if f.resultHook != nil {
		return f.resultHook(req), nil
	}
	return &OrderResult{
		OrderID:   "TEST-" + req.Symbol,
		Symbol:    req.Symbol,
		Direction: req.Direction,
		OrderType: req.OrderType,
		Quantity:  req.Quantity,
		FilledQty: req.Quantity,
		Price:     req.Price,
		Status:    "filled",
	}, nil
}

func (f *fakeExecutionPort) CancelOrder(ctx context.Context, orderID string) error { return nil }
func (f *fakeExecutionPort) GetOrder(ctx context.Context, orderID string) (*OrderResult, error) {
	return nil, nil
}
func (f *fakeExecutionPort) Name() string                          { return f.name }
func (f *fakeExecutionPort) HealthCheck(ctx context.Context) error { return f.healthErr }

// Compile-time check
var _ ExecutionPort = (*fakeExecutionPort)(nil)

// ----- Tests -----

func newTestLiveBridge() *LiveBridge {
	return NewLiveBridge(zerolog.New(nil))
}

func TestLiveBridge_SetGetRoundTrip(t *testing.T) {
	b := newTestLiveBridge()
	assert.Nil(t, b.Get(), "fresh bridge should have nil port")

	port := &fakeExecutionPort{name: "fake"}
	b.Set(port)
	assert.Same(t, port, b.Get(), "Get should return the port we just Set")

	// Setting nil detaches
	b.Set(nil)
	assert.Nil(t, b.Get(), "Set(nil) should detach the port")
}

func TestLiveBridge_ExecuteSignal_NilTraderReturnsNil(t *testing.T) {
	b := newTestLiveBridge()
	signal := domain.Signal{Symbol: "600000.SH", Direction: domain.DirectionLong}
	result, err := b.ExecuteSignal(context.Background(), signal, 10.0)
	require.NoError(t, err)
	assert.Nil(t, result, "nil port must return (nil, nil) per backward compat contract")
}

func TestLiveBridge_ExecuteSignal_InvalidPrice(t *testing.T) {
	b := newTestLiveBridge()
	b.Set(&fakeExecutionPort{name: "fake"})

	signal := domain.Signal{Symbol: "600000.SH", Direction: domain.DirectionLong}
	for _, badPrice := range []float64{0, -0.01, -100} {
		result, err := b.ExecuteSignal(context.Background(), signal, badPrice)
		require.Error(t, err, "price=%.2f must be rejected", badPrice)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "invalid price")
	}
}

func TestLiveBridge_ExecuteSignal_MarketOrder(t *testing.T) {
	port := &fakeExecutionPort{name: "fake"}
	b := newTestLiveBridge()
	b.Set(port)

	signal := domain.Signal{
		Symbol:    "600000.SH",
		Direction: domain.DirectionLong,
		OrderType: domain.OrderTypeMarket,
		Strength:  0.0, // → quantity = 100 default
	}
	result, err := b.ExecuteSignal(context.Background(), signal, 12.5)
	require.NoError(t, err)
	require.NotNil(t, result)

	require.Len(t, port.submitCalls, 1)
	call := port.submitCalls[0]
	assert.Equal(t, "600000.SH", call.Symbol)
	assert.Equal(t, domain.DirectionLong, call.Direction)
	assert.Equal(t, domain.OrderTypeMarket, call.OrderType)
	assert.Equal(t, 0.0, call.Price, "market order must send price=0 to broker")
	assert.Equal(t, 100.0, call.Quantity, "strength=0 must use default lot of 100")
}

func TestLiveBridge_ExecuteSignal_LimitOrder(t *testing.T) {
	port := &fakeExecutionPort{name: "fake"}
	b := newTestLiveBridge()
	b.Set(port)

	signal := domain.Signal{
		Symbol:     "600000.SH",
		Direction:  domain.DirectionLong,
		OrderType:  domain.OrderTypeLimit,
		LimitPrice: 11.5,
		Strength:   0.5, // → quantity = 100 * max(1.0, 0.5*10) = 100 * 5.0 = 500
	}
	result, err := b.ExecuteSignal(context.Background(), signal, 12.0)
	require.NoError(t, err)
	require.NotNil(t, result)

	require.Len(t, port.submitCalls, 1)
	call := port.submitCalls[0]
	assert.Equal(t, domain.OrderTypeLimit, call.OrderType)
	assert.Equal(t, 11.5, call.Price, "limit order must use LimitPrice, not currentPrice")
	assert.Equal(t, 500.0, call.Quantity, "strength=0.5 must scale to 100*5.0=500 lots")
}

func TestLiveBridge_ExecuteSignal_LimitPriceFallback(t *testing.T) {
	port := &fakeExecutionPort{name: "fake"}
	b := newTestLiveBridge()
	b.Set(port)

	// Limit order with no explicit LimitPrice → fallback to currentPrice
	signal := domain.Signal{
		Symbol:    "600000.SH",
		Direction: domain.DirectionLong,
		OrderType: domain.OrderTypeLimit,
		// LimitPrice == 0
	}
	_, err := b.ExecuteSignal(context.Background(), signal, 9.5)
	require.NoError(t, err)
	require.Len(t, port.submitCalls, 1)
	assert.Equal(t, 9.5, port.submitCalls[0].Price, "missing LimitPrice should fallback to currentPrice")
}

func TestLiveBridge_ExecuteSignal_QuantityScalingCap(t *testing.T) {
	port := &fakeExecutionPort{name: "fake"}
	b := newTestLiveBridge()
	b.Set(port)

	// strength=200 → scaled = 200*10 = 2000 → quantity = 200000, but cap to 10000
	signal := domain.Signal{
		Symbol:    "600000.SH",
		Direction: domain.DirectionLong,
		Strength:  200.0,
	}
	_, err := b.ExecuteSignal(context.Background(), signal, 10.0)
	require.NoError(t, err)
	require.Len(t, port.submitCalls, 1)
	assert.Equal(t, 10000.0, port.submitCalls[0].Quantity, "quantity must be capped at 10000")
}

func TestLiveBridge_ExecuteSignal_SubmitError(t *testing.T) {
	port := &fakeExecutionPort{
		name:      "fake",
		submitErr: errors.New("broker rejected"),
	}
	b := newTestLiveBridge()
	b.Set(port)

	signal := domain.Signal{Symbol: "600000.SH", Direction: domain.DirectionLong}
	result, err := b.ExecuteSignal(context.Background(), signal, 10.0)
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "broker rejected")
}

func TestLiveBridge_ExecuteSignals_BatchWithMissingPrices(t *testing.T) {
	port := &fakeExecutionPort{name: "fake"}
	b := newTestLiveBridge()
	b.Set(port)

	signals := []domain.Signal{
		{Symbol: "600000.SH", Direction: domain.DirectionLong},
		{Symbol: "600001.SH", Direction: domain.DirectionLong},
		{Symbol: "600002.SH", Direction: domain.DirectionLong}, // missing price → skip
		{Symbol: "600003.SH", Direction: domain.DirectionLong}, // price=0 → skip
	}
	prices := map[string]float64{
		"600000.SH": 10.0,
		"600001.SH": 20.0,
		"600003.SH": 0.0,
		// 600002.SH absent
	}

	results := b.ExecuteSignals(context.Background(), signals, prices)
	require.NotNil(t, results)
	assert.Len(t, results, 2, "only 2 signals have valid prices; should skip the rest")
	assert.Contains(t, results, "600000.SH")
	assert.Contains(t, results, "600001.SH")
	assert.NotContains(t, results, "600002.SH")
	assert.NotContains(t, results, "600003.SH")
}

func TestLiveBridge_ExecuteSignals_NilTraderReturnsNil(t *testing.T) {
	b := newTestLiveBridge()
	signals := []domain.Signal{{Symbol: "600000.SH", Direction: domain.DirectionLong}}
	results := b.ExecuteSignals(context.Background(), signals, map[string]float64{"600000.SH": 10.0})
	assert.Nil(t, results, "nil port must return nil map per backward compat")
}

func TestLiveBridge_HealthCheck_NilTrader(t *testing.T) {
	b := newTestLiveBridge()
	err := b.HealthCheck(context.Background())
	assert.NoError(t, err, "nil port should be treated as 'no health check needed'")
}

func TestLiveBridge_HealthCheck_DelegatesToTrader(t *testing.T) {
	port := &fakeExecutionPort{name: "fake", healthErr: errors.New("broker disconnected")}
	b := newTestLiveBridge()
	b.Set(port)

	err := b.HealthCheck(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "broker disconnected")
}
