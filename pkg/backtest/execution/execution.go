package execution

import (
	"fmt"
	"time"

	"tellurion/pkg/domain"
	costexec "tellurion/pkg/execution"
)

// ExecutionService handles order execution in backtests.
// This abstraction allows the backtest engine to work with both
// simulated and (eventually) live execution.
type ExecutionService interface {
	// ExecuteOrder processes an order and returns the resulting trade
	ExecuteOrder(order domain.Order, quote Quote) (domain.Trade, error)

	// GetSlippageModel returns the current slippage model name
	GetSlippageModel() string

	// SetSlippageModel sets the slippage model
	SetSlippageModel(model string)
}

// Quote represents a price quote for execution
type Quote struct {
	Symbol string
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume float64
	Date   time.Time
}

// BacktestExecutionService implements ExecutionService for backtesting
type BacktestExecutionService struct {
	config        domain.ExecutionConfig
	slippageModel string
}

// NewBacktestExecutionService creates a new backtest execution service
func NewBacktestExecutionService(config domain.ExecutionConfig) *BacktestExecutionService {
	return &BacktestExecutionService{
		config:        config,
		slippageModel: config.SlippageModel,
	}
}

// ExecuteOrder executes an order against a quote
func (s *BacktestExecutionService) ExecuteOrder(order domain.Order, quote Quote) (domain.Trade, error) {
	if order.Quantity <= 0 {
		return domain.Trade{}, fmt.Errorf("invalid order quantity: %f", order.Quantity)
	}

	// Determine execution price based on order type and direction
	var executionPrice float64

	switch order.OrderType {
	case domain.OrderTypeMarket:
		executionPrice = s.applySlippage(quote.Close, order.Direction, quote, order.Quantity)
	case domain.OrderTypeLimit:
		if order.LimitPrice <= 0 {
			return domain.Trade{}, fmt.Errorf("invalid limit price: %f", order.LimitPrice)
		}
		// For buy orders, execute if limit price >= low
		// For sell orders, execute if limit price <= high
		if order.Direction == domain.DirectionLong && order.LimitPrice >= quote.Low {
			executionPrice = min(order.LimitPrice, quote.High)
		} else if order.Direction == domain.DirectionShort && order.LimitPrice <= quote.High {
			executionPrice = max(order.LimitPrice, quote.Low)
		} else {
			return domain.Trade{}, fmt.Errorf("limit price not reached")
		}
	default:
		return domain.Trade{}, fmt.Errorf("unsupported order type: %s", order.OrderType)
	}

	if executionPrice <= 0 {
		return domain.Trade{}, fmt.Errorf("invalid execution price: %f", executionPrice)
	}

	// Calculate commission
	amount := executionPrice * order.Quantity
	commission := s.calculateCommission(amount)

	trade := domain.Trade{
		ID:         generateTradeID(),
		Symbol:     order.Symbol,
		Direction:  order.Direction,
		Quantity:   order.Quantity,
		Price:      executionPrice,
		Commission: commission,
		Timestamp:  quote.Date,
	}

	return trade, nil
}

// GetSlippageModel returns the current slippage model
func (s *BacktestExecutionService) GetSlippageModel() string {
	return s.slippageModel
}

// Config 返回构造时的执行配置（只读快照）。
//
// REV-19 加它是为了让「NewEngine 是否把用户的 slippage_rate 透传进来」可被
// 断言 —— 装配类缺陷（配置读到了但没传下去）在行为层很难观察：默认值 0.001
// 与配置值 0.0005 的成交价差异会被后续风控/整手逻辑吸收掉。返回结构体副本，
// 调用方改不动内部状态。
func (s *BacktestExecutionService) Config() domain.ExecutionConfig {
	return s.config
}

// SetSlippageModel sets the slippage model
func (s *BacktestExecutionService) SetSlippageModel(model string) {
	s.slippageModel = model
}

// applySlippage applies slippage to the execution price.
//
// orderQty was added by K4 so the "impact" branch can size impact against
// the order's participation in volume. The existing "fixed" / "variable" /
// "none" branches ignore it and are byte-for-byte unchanged.
//
// K5 切片 1：函数体改为委托 pkg/execution 的**共享成本核**——paper 侧
// （pkg/live）走的是同一段代码，回测与 paper 的滑点公式从此不可能漂移。
// 这里把可变的 s.slippageModel 覆盖进 config 再构造 core，以保留
// SetSlippageModel 的既有语义（s.slippageModel 可能已不同于
// config.SlippageModel）。数值行为逐位不变。
func (s *BacktestExecutionService) applySlippage(price float64, direction domain.Direction, quote Quote, orderQty float64) float64 {
	cfg := s.config
	cfg.SlippageModel = s.slippageModel
	core := costexec.NewCostModel(cfg)
	// quote.Volume 作 ADV 代理（K4 裁决，见 pkg/execution 的 SlippagePrice）。
	return core.SlippagePrice(price, direction, orderQty, quote.Volume, quote.High, quote.Low)
}

// calculateCommission calculates trading commission.
//
// K5 切片 1：委托 pkg/execution 的共享成本核——回测与 paper 用同一条
// max(notional*CommissionRate, MinCommission) 公式。
func (s *BacktestExecutionService) calculateCommission(amount float64) float64 {
	return costexec.NewCostModel(s.config).Commission(amount)
}

// generateTradeID 生成成交流水 ID。
//
// REV-22-(9) 成文登记：ID 用纳秒墙钟（time.Now().UnixNano()），**不**进
// 复现比对范围——它与策略/数据/撮合逻辑无关，只承载「这条流水在数据库里
// 的唯一键」职责。同区间重跑逐位一致的承诺（REV-18）比对的是 PortfolioValues
// 与 Trade 的语义字段（方向/数量/价格/费用/日期），见
// engine_rev18_reproducibility_test.go 的 stripIDs。若将来需要确定性 ID
// （如审计要求同输入同 ID），改法 = run 级序号 `fmt.Sprintf("TRD-%s-%d",
// backtestID, seq)`，需要把 run 身份传进执行服务——当前的执行服务是无状态
// 的（REV-11），不为 ID 引入状态。
func generateTradeID() string {
	return fmt.Sprintf("TRD-%d", time.Now().UnixNano())
}

// min and max are provided by Go 1.21+ builtins (P0-10).
