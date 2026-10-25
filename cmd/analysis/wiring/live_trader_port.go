package wiring

// QT-51（BE-02）：实盘/paper 通道 → 回测引擎执行端口的适配层。
//
// 这是本仓**唯一**同时认识 `live.LiveTrader` 与
// `pkg/backtest/execution.ExecutionPort` 的位置：
//   - `pkg/backtest` 侧结构护栏禁止 import `pkg/live`；
//   - `pkg/live` 是 QT-45 通道网关域，不改实现语义；
//   - 装配层（本包）负责把两者接上。
//
// 结构护栏：`internal/repoguard/backtest_live_isolation_test.go`。

import (
	"context"
	"fmt"

	"tellurion/pkg/backtest"
	"tellurion/pkg/backtest/execution"
	"tellurion/pkg/live"
)

// liveTraderPort 把 live.LiveTrader 适配成 execution.ExecutionPort。
//
// 只转译测到的方法集：下单 / 撤单 / 成交回报 / 身份 / 健康。
// 仓位、账户、紧急平仓不在回测引擎的出站端口内（运维面直接消费
// live.LiveTrader）。
type liveTraderPort struct {
	trader live.LiveTrader
}

// NewLiveTraderPort 包装一个 live.LiveTrader；trader 为 nil 时返回 nil port
// （引擎侧语义 = 关闭出站路径，走纯模拟）。
func NewLiveTraderPort(trader live.LiveTrader) execution.ExecutionPort {
	if trader == nil {
		return nil
	}
	return &liveTraderPort{trader: trader}
}

// AttachLiveTrader 是装配层注入实盘通道的唯一入口：
// 先适配成 ExecutionPort，再交给 Engine.SetLiveTrader。
//
// 引擎为 nil 时静默返回（装配错误由上游 fail-fast 负责）。
func AttachLiveTrader(engine *backtest.Engine, trader live.LiveTrader) {
	if engine == nil {
		return
	}
	engine.SetLiveTrader(NewLiveTraderPort(trader))
}

func (p *liveTraderPort) SubmitOrder(ctx context.Context, req execution.OrderRequest) (*execution.OrderResult, error) {
	if p.trader == nil {
		return nil, fmt.Errorf("live trader port: no trader attached")
	}
	res, err := p.trader.SubmitOrder(ctx, req.Symbol, req.Direction, req.OrderType, req.Quantity, req.Price)
	if err != nil {
		return nil, err
	}
	return fromLiveOrderResult(res), nil
}

func (p *liveTraderPort) CancelOrder(ctx context.Context, orderID string) error {
	if p.trader == nil {
		return fmt.Errorf("live trader port: no trader attached")
	}
	return p.trader.CancelOrder(ctx, orderID)
}

func (p *liveTraderPort) GetOrder(ctx context.Context, orderID string) (*execution.OrderResult, error) {
	if p.trader == nil {
		return nil, fmt.Errorf("live trader port: no trader attached")
	}
	res, err := p.trader.GetOrder(ctx, orderID)
	if err != nil {
		return nil, err
	}
	return fromLiveOrderResult(res), nil
}

func (p *liveTraderPort) Name() string {
	if p.trader == nil {
		return "live_trader_port(unattached)"
	}
	return p.trader.Name()
}

func (p *liveTraderPort) HealthCheck(ctx context.Context) error {
	if p.trader == nil {
		return fmt.Errorf("live trader port: no trader attached")
	}
	return p.trader.HealthCheck(ctx)
}

// fromLiveOrderResult 把实盘结果值对象转成回测域 DTO。
// nil 进 nil 出（实盘实现可能返回 (nil, nil)）。
func fromLiveOrderResult(res *live.OrderResult) *execution.OrderResult {
	if res == nil {
		return nil
	}
	return &execution.OrderResult{
		OrderID:     res.OrderID,
		Symbol:      res.Symbol,
		Direction:   res.Direction,
		OrderType:   res.OrderType,
		Quantity:    res.Quantity,
		FilledQty:   res.FilledQty,
		Price:       res.Price,
		Status:      res.Status,
		SubmittedAt: res.SubmittedAt,
		Message:     res.Message,
		FillPrice:   res.FillPrice,
		Fee:         res.Fee,
	}
}

// 编译期断言：适配器满足引擎端口。
var _ execution.ExecutionPort = (*liveTraderPort)(nil)
