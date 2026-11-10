package trading

// paper_fake_test.go —— paper 侧测试用的最小交易通道替身。
//
// SVC-CORE-BROKER-RETIRE 删除了进程内撮合实现（mock_trader.go），
// 但 PaperSession / exec-algo / 成本同构这些核心算法仍在，不能连测试
// 一起退役。这里提供**只够算法断言**的确定性替身：
//   - 市价/限价单按当根 bar 的 Close 立即全额成交，不模拟滑点（成本
//     同构由 paper_cost_test 的纯函数腿覆盖）；
//   - 记录持仓与可用量，`AdvanceDay` 做 T+1 滚动；
//   - 现金按成交额扣减，卖出前校验可用量。
//
// 它不是生产实现，也不替代 broker HTTP 通道；名字带 fake 以免误用。

import (
	"context"
	"fmt"
	"time"

	cdom "github.com/ruoxizhnya/tellurion-contracts/domain"

	"tellurion/pkg/domain"
)

type fakePaperTrader struct {
	cash      float64
	positions map[string]*tradingPosition
	barFn     func(string) (Quote, bool)
	seq       int
	cfg       domain.ExecutionConfig
}

type tradingPosition struct {
	qty       float64
	available float64
	avgCost   float64
}

func newFakePaperTrader(cash float64) *fakePaperTrader {
	return &fakePaperTrader{
		cash:      cash,
		positions: map[string]*tradingPosition{},
		// 与 K17/K18 夹具原参数一致：impact 成本核 + A 股费用口径。
		cfg: domain.ExecutionConfig{
			SlippageModel:         "impact",
			ImpactSigma:           0.05,
			ImpactLiquidityFactor: 1.0,
			CommissionRate:        0.0003,
			MinCommission:         5,
		},
	}
}

// SetCurrentBarProvider 满足 CurrentBarSource。
func (f *fakePaperTrader) SetCurrentBarProvider(fn func(string) (Quote, bool)) { f.barFn = fn }

// AdvanceDay 做 T+1 滚动：当日买入全部转为可用。
func (f *fakePaperTrader) AdvanceDay() {
	for _, p := range f.positions {
		p.available = p.qty
	}
}

// GetCash 返回可用现金。
func (f *fakePaperTrader) GetCash() float64 { return f.cash }

func (f *fakePaperTrader) Name() string                      { return "fake_paper" }
func (f *fakePaperTrader) HealthCheck(context.Context) error { return nil }

func (f *fakePaperTrader) SubmitOrder(_ context.Context, symbol string, direction cdom.Direction,
	orderType domain.OrderType, quantity, price float64) (*OrderResult, error) {
	if quantity <= 0 {
		return nil, fmt.Errorf("fake paper: quantity must be positive")
	}
	bar, hasBar := Quote{}, false
	if f.barFn != nil {
		bar, hasBar = f.barFn(symbol)
	}

	filled := quantity
	// 卖单按「可用量」部分成交：与柜台/券商的实际行为一致，也让
	// K18「外部改仓后中止子单」的夹具能走通（当日买入不可卖）。
	if direction == cdom.DirectionClose || direction == cdom.DirectionShort {
		pos := f.positions[symbol]
		avail := 0.0
		if pos != nil {
			avail = pos.available
		}
		if avail <= 0 {
			return nil, fmt.Errorf("fake paper: no available shares for %s", symbol)
		}
		if filled > avail {
			filled = avail
		}
	}

	px, fee := price, 0.0
	if orderType == domain.OrderTypeMarket {
		if !hasBar || bar.Close <= 0 {
			return nil, fmt.Errorf("fake paper: no current bar for %s", symbol)
		}
		cost, err := ComputePaperCost(f.cfg, domain.Order{
			Symbol:    symbol,
			Direction: direction,
			OrderType: domain.OrderTypeMarket,
			Quantity:  filled,
		}, bar.Close, bar.High, bar.Low, float64(bar.Volume))
		if err != nil {
			return nil, err
		}
		px, fee = cost.FillPrice, cost.Commission
	} else if px <= 0 {
		if !hasBar || bar.Close <= 0 {
			return nil, fmt.Errorf("fake paper: no current bar for %s", symbol)
		}
		px = bar.Close
	}

	pos := f.positions[symbol]
	f.seq++
	res := &OrderResult{
		OrderID:     fmt.Sprintf("fake-%d", f.seq),
		Symbol:      symbol,
		Direction:   direction,
		OrderType:   orderType,
		Quantity:    quantity,
		FilledQty:   filled,
		Price:       px,
		FillPrice:   px,
		Fee:         fee,
		Status:      "filled",
		SubmittedAt: time.Now().UTC(),
	}
	switch direction {
	case cdom.DirectionLong:
		gross := filled * px
		if gross+fee > f.cash {
			return nil, fmt.Errorf("fake paper: insufficient cash: need %.2f have %.2f", gross+fee, f.cash)
		}
		f.cash -= gross + fee
		if pos == nil {
			pos = &tradingPosition{}
			f.positions[symbol] = pos
		}
		total := pos.avgCost*pos.qty + gross
		pos.qty += filled
		pos.avgCost = total / pos.qty
	case cdom.DirectionClose, cdom.DirectionShort:
		pos.available -= filled
		pos.qty -= filled
		f.cash += filled*px - fee
		if pos.qty == 0 {
			delete(f.positions, symbol)
		}
	default:
		return nil, fmt.Errorf("fake paper: unsupported direction %q", direction)
	}
	return res, nil
}

func (f *fakePaperTrader) CancelOrder(context.Context, string) error {
	return fmt.Errorf("fake paper: CancelOrder not supported")
}

func (f *fakePaperTrader) GetOrder(context.Context, string) (*OrderResult, error) {
	return nil, fmt.Errorf("fake paper: GetOrder not supported")
}

func (f *fakePaperTrader) GetPositions(context.Context) ([]PositionInfo, error) {
	out := make([]PositionInfo, 0, len(f.positions))
	for sym, p := range f.positions {
		info := PositionInfo{
			Symbol:            sym,
			Quantity:          p.qty,
			AvailableQty:      p.available,
			AvgCost:           p.avgCost,
			QuantityToday:     p.qty - p.available,
			QuantityYesterday: p.available,
		}
		if f.barFn != nil {
			if q, ok := f.barFn(sym); ok {
				info.CurrentPrice = q.Close
				info.MarketValue = q.Close * p.qty
				info.UnrealizedPnL = (q.Close - p.avgCost) * p.qty
			}
		}
		out = append(out, info)
	}
	return out, nil
}

func (f *fakePaperTrader) GetAccount(context.Context) (*AccountInfo, error) {
	acct := &AccountInfo{Cash: f.cash}
	for sym, p := range f.positions {
		if f.barFn != nil {
			if q, ok := f.barFn(sym); ok {
				acct.MarketValue += q.Close * p.qty
				acct.UnrealizedPnL += (q.Close - p.avgCost) * p.qty
			}
		}
	}
	acct.TotalAssets = acct.Cash + acct.MarketValue
	return acct, nil
}

func (f *fakePaperTrader) EmergencyFlatten(context.Context, string) (*EmergencyFlattenResult, error) {
	return &EmergencyFlattenResult{}, nil
}

var _ LiveTrader = (*fakePaperTrader)(nil)
var _ CurrentBarSource = (*fakePaperTrader)(nil)
