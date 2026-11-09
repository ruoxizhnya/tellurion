package wiring

// QT-51：实盘适配层测试。
//
// 守住三件事：
//  1. live.OrderResult → execution.OrderResult 字段不丢（含 FillPrice/Fee）；
//  2. 请求参数原样下发（symbol/direction/orderType/qty/price）；
//  3. AttachLiveTrader 注入后，引擎经 ExecuteSignalViaLiveTrader 能打到
//     真实 live.LiveTrader（适配层不是摆设）。

import (
	"context"
	"errors"
	"testing"

	cdom "github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/backtest"
	"tellurion/pkg/backtest/execution"
	"tellurion/pkg/live"
	"tellurion/pkg/marketdata"
)

import "tellurion/pkg/domain"

type fakeTrader struct {
	lastSymbol    string
	lastDirection cdom.Direction
	lastOrderType domain.OrderType
	lastQty       float64
	lastPrice     float64
	submitErr     error
	healthErr     error
	name          string
	order         *live.OrderResult
}

func (f *fakeTrader) SubmitOrder(ctx context.Context, symbol string, dir cdom.Direction, orderType domain.OrderType, qty, price float64) (*live.OrderResult, error) {
	f.lastSymbol, f.lastDirection, f.lastOrderType = symbol, dir, orderType
	f.lastQty, f.lastPrice = qty, price
	if f.submitErr != nil {
		return nil, f.submitErr
	}
	if f.order != nil {
		return f.order, nil
	}
	return &live.OrderResult{
		OrderID: "LIVE-1", Symbol: symbol, Direction: dir, OrderType: orderType,
		Quantity: qty, FilledQty: qty, Price: price, Status: "filled",
		FillPrice: price * 1.001, Fee: 5,
	}, nil
}
func (f *fakeTrader) CancelOrder(ctx context.Context, orderID string) error { return nil }
func (f *fakeTrader) GetOrder(ctx context.Context, orderID string) (*live.OrderResult, error) {
	return f.order, nil
}
func (f *fakeTrader) GetPositions(ctx context.Context) ([]live.PositionInfo, error) { return nil, nil }
func (f *fakeTrader) GetAccount(ctx context.Context) (*live.AccountInfo, error)     { return nil, nil }
func (f *fakeTrader) Name() string                                                  { return f.name }
func (f *fakeTrader) HealthCheck(ctx context.Context) error                         { return f.healthErr }
func (f *fakeTrader) EmergencyFlatten(ctx context.Context, reason string) (*live.EmergencyFlattenResult, error) {
	return &live.EmergencyFlattenResult{Reason: reason}, nil
}

var _ live.LiveTrader = (*fakeTrader)(nil)

func TestNewLiveTraderPort_NilTraderReturnsNilPort(t *testing.T) {
	assert.Nil(t, NewLiveTraderPort(nil), "nil trader 必须映射成 nil port（引擎侧 = 关闭出站路径）")
}

func TestLiveTraderPort_SubmitOrder_MapsBothWays(t *testing.T) {
	trader := &fakeTrader{name: "fake-trader"}
	port := NewLiveTraderPort(trader)
	require.NotNil(t, port)
	assert.Equal(t, "fake-trader", port.Name())

	res, err := port.SubmitOrder(context.Background(), execution.OrderRequest{
		Symbol:    "600000.SH",
		Direction: cdom.DirectionLong,
		OrderType: domain.OrderTypeLimit,
		Quantity:  500,
		Price:     11.5,
	})
	require.NoError(t, err)
	require.NotNil(t, res)

	// 请求原样下发
	assert.Equal(t, "600000.SH", trader.lastSymbol)
	assert.Equal(t, cdom.DirectionLong, trader.lastDirection)
	assert.Equal(t, domain.OrderTypeLimit, trader.lastOrderType)
	assert.Equal(t, 500.0, trader.lastQty)
	assert.Equal(t, 11.5, trader.lastPrice)

	// 结果字段不丢
	assert.Equal(t, "LIVE-1", res.OrderID)
	assert.Equal(t, "filled", res.Status)
	assert.Equal(t, 500.0, res.FilledQty)
	assert.InDelta(t, 11.5*1.001, res.FillPrice, 1e-12)
	assert.Equal(t, 5.0, res.Fee)
}

func TestLiveTraderPort_ErrorAndHealthPassthrough(t *testing.T) {
	wantErr := errors.New("boom")
	trader := &fakeTrader{name: "fake", submitErr: wantErr, healthErr: wantErr}
	port := NewLiveTraderPort(trader)

	_, err := port.SubmitOrder(context.Background(), execution.OrderRequest{Symbol: "600000.SH", Quantity: 100})
	assert.ErrorIs(t, err, wantErr)
	assert.ErrorIs(t, port.HealthCheck(context.Background()), wantErr)
}

func TestAttachLiveTrader_InjectsWorkingPortIntoEngine(t *testing.T) {
	trader := &fakeTrader{name: "fake-trader"}
	eng, err := backtest.NewEngineWithOptions(backtest.Config{Seed: 1}, marketdata.NewInMemoryProvider())
	require.NoError(t, err)

	AttachLiveTrader(eng, trader)
	attached := eng.GetLiveTrader()
	require.NotNil(t, attached, "AttachLiveTrader 之后引擎必须持有 port")

	res, err := eng.ExecuteSignalViaLiveTrader(context.Background(), domain.Signal{
		Symbol:    "600000.SH",
		Direction: cdom.DirectionLong,
		OrderType: domain.OrderTypeMarket,
		Strength:  0.5,
	}, 10.0)
	require.NoError(t, err)
	require.NotNil(t, res)

	assert.Equal(t, "600000.SH", trader.lastSymbol, "引擎信号必须经适配层打到 live.LiveTrader")
	assert.Equal(t, 0.0, trader.lastPrice, "市价单 price 必须为 0")
	assert.Equal(t, 500.0, trader.lastQty, "quantity 口径必须与旧实现一致")
}

func TestAttachLiveTrader_NilEngineOrTraderIsSafe(t *testing.T) {
	AttachLiveTrader(nil, &fakeTrader{name: "fake"}) // 不 panic
	eng, err := backtest.NewEngineWithOptions(backtest.Config{Seed: 1}, marketdata.NewInMemoryProvider())
	require.NoError(t, err)
	AttachLiveTrader(eng, nil)
	assert.Nil(t, eng.GetLiveTrader(), "nil trader 必须解挂，而不是挂一个空壳 port")
}
