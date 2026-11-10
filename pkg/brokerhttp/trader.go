package brokerhttp

// SVC-CORE-BROKER-RETIRE 步骤 B：core 运行域的 `trading.LiveTrader` 实现。
//
// 它是 core 唯一的真实交易通道：`/api/execution/*`、止损巡检、
// 对账、证券状态与紧急清仓全部经这里落到 tellurion-broker。进程内
// 撮合实现已随本单删除，`BROKER_URL` 缺失即装配失败（fail-loud），
// 不再有静默回退。
//
// 与 Port 共用同一份地址 / 鉴权 / client_order_id / 报文映射；差别只在
// 形状：Port 面向上游回测引擎的 ExecutionPort，Trader 面向 core 的运维面。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/rs/zerolog"
	broker "github.com/ruoxizhnya/tellurion-contracts/broker"
	cdom "github.com/ruoxizhnya/tellurion-contracts/domain"

	"tellurion/pkg/backtest/execution"
	"tellurion/pkg/domain"
	"tellurion/pkg/trading"
)

// Trader 是 broker HTTP 通道的 `trading.LiveTrader` 实现。
type Trader struct {
	port *BrokerHTTPPort
}

// NewTrader 构造 broker 通道；参数非法即返回错误（不做半装配）。
func NewTrader(opts BrokerHTTPOptions) (*Trader, error) {
	port, err := NewBrokerHTTPPort(opts)
	if err != nil {
		return nil, err
	}
	return &Trader{port: port}, nil
}

// NewTraderFromEnv 是 core 的默认装配入口。
//
// `BROKER_URL` 必填：SVC-CORE-BROKER-RETIRE 之后进程内撮合已删除，
// 缺失即返回错误，由调用方 fail-loud。`BROKER_SERVICE_TOKEN` 与
// `BROKER_ACCOUNT_ID` 同样必填。
func NewTraderFromEnv(logger zerolog.Logger) (*Trader, error) {
	base := strings.TrimSpace(os.Getenv("BROKER_URL"))
	if base == "" {
		return nil, fmt.Errorf("broker_http: BROKER_URL is required — in-process matching was retired by SVC-CORE-BROKER-RETIRE; configure the broker HTTP gateway instead")
	}
	trader, err := NewTrader(BrokerHTTPOptions{
		BaseURL:      base,
		ServiceToken: os.Getenv("BROKER_SERVICE_TOKEN"),
		AccountID:    os.Getenv("BROKER_ACCOUNT_ID"),
	})
	if err != nil {
		return nil, err
	}
	logger.Info().
		Str("url", base).
		Str("account_id", trader.port.accountID).
		Msg("execution channel: broker HTTP gateway (SVC-CORE-BROKER-RETIRE)")
	return trader, nil
}

// Name 返回实现名。
func (t *Trader) Name() string { return t.port.Name() }

// HealthCheck 探 broker 服务的 `/healthz`。
func (t *Trader) HealthCheck(ctx context.Context) error { return t.port.HealthCheck(ctx) }

// SubmitOrder 提交一笔订单，并把 port DTO 转回运行域 DTO。
func (t *Trader) SubmitOrder(ctx context.Context, symbol string, direction cdom.Direction,
	orderType domain.OrderType, quantity, price float64) (*trading.OrderResult, error) {
	res, err := t.port.SubmitOrder(ctx, execution.OrderRequest{
		Symbol:    symbol,
		Direction: direction,
		OrderType: orderType,
		Quantity:  quantity,
		Price:     price,
	})
	if err != nil {
		return nil, err
	}
	return fromPortResult(res), nil
}

// CancelOrder 撤单。
func (t *Trader) CancelOrder(ctx context.Context, orderID string) error {
	return t.port.CancelOrder(ctx, orderID)
}

// GetOrder 查单。
func (t *Trader) GetOrder(ctx context.Context, orderID string) (*trading.OrderResult, error) {
	res, err := t.port.GetOrder(ctx, orderID)
	if err != nil {
		return nil, err
	}
	return fromPortResult(res), nil
}

// GetPositions 读柜台持仓并还原成 core 符号口径。
func (t *Trader) GetPositions(ctx context.Context) ([]trading.PositionInfo, error) {
	var positions []broker.Position
	if err := t.getJSON(ctx, "/api/broker/accounts/"+url.PathEscape(t.port.accountID)+"/positions", &positions); err != nil {
		return nil, err
	}
	out := make([]trading.PositionInfo, 0, len(positions))
	for _, p := range positions {
		info := trading.PositionInfo{
			Symbol:       coreSymbol(p.Symbol),
			Quantity:     float64(p.Quantity),
			AvailableQty: float64(p.AvailableQuantity),
			AvgCost:      p.AvgCost,
			MarketValue:  p.MarketValue,
			// 不可用数量 = T+1 冻结的当日买入；柜台不单独回传该字段，
			// 只能由「总量 − 可用量」推出。
			QuantityToday:     float64(p.Quantity - p.AvailableQuantity),
			QuantityYesterday: float64(p.AvailableQuantity),
		}
		if p.Quantity > 0 {
			info.CurrentPrice = p.MarketValue / float64(p.Quantity)
		}
		info.UnrealizedPnL = p.MarketValue - p.AvgCost*float64(p.Quantity)
		out = append(out, info)
	}
	return out, nil
}

// GetAccount 读账户快照。`RealizedPnL` 不在柜台快照里，置 0（如实缺省，
// 不用未实现盈亏冒充）；`MarketValue` 取快照持仓市值合计。
func (t *Trader) GetAccount(ctx context.Context) (*trading.AccountInfo, error) {
	var snap broker.AccountSnapshot
	if err := t.getJSON(ctx, "/api/broker/accounts/"+url.PathEscape(t.port.accountID)+"/account", &snap); err != nil {
		return nil, err
	}
	var marketValue, unrealized float64
	for _, p := range snap.Positions {
		marketValue += p.MarketValue
		unrealized += p.MarketValue - p.AvgCost*float64(p.Quantity)
	}
	return &trading.AccountInfo{
		TotalAssets:   snap.TotalAssets,
		Cash:          snap.Cash.Total,
		MarketValue:   marketValue,
		UnrealizedPnL: unrealized,
		UpdatedAt:     snap.AsOf,
	}, nil
}

// EmergencyFlatten 通过 broker 的 kill-switch 面清仓。
//
// broker 只回报「已确认成交」的持仓；未确认的一律在 Skipped 里，
// 本实现不补写、不猜测。
func (t *Trader) EmergencyFlatten(ctx context.Context, reason string) (*trading.EmergencyFlattenResult, error) {
	body := struct {
		Reason string `json:"reason"`
	}{Reason: reason}
	var res broker.EmergencyFlattenResult
	status, raw, err := t.port.do(ctx, http.MethodPost, "/api/broker/emergency-flatten", body, true)
	if err != nil {
		return nil, err
	}
	if status < 200 || status > 299 {
		return nil, t.port.orderError("emergency flatten", status, raw)
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("broker_http: decode EmergencyFlattenResult: %w", err)
	}
	out := &trading.EmergencyFlattenResult{
		Sold:        make([]trading.EmergencyFlattenOrder, 0, len(res.Sold)),
		Skipped:     make([]trading.EmergencyFlattenSkip, 0, len(res.Skipped)),
		StartedAt:   res.StartedAt,
		CompletedAt: res.CompletedAt,
		Reason:      res.Reason,
	}
	for _, s := range res.Sold {
		gross := float64(s.Quantity) * s.AvgFillPrice
		// broker 通道不回传单笔费用；这里记毛成交额，费用口径以 fills 查询为准。
		out.Sold = append(out.Sold, trading.EmergencyFlattenOrder{
			Symbol:      coreSymbol(s.Symbol),
			OrderID:     s.ClientOrderID,
			Quantity:    float64(s.Quantity),
			FillPrice:   s.AvgFillPrice,
			NetProceeds: gross,
			// broker 的 kill-switch 按持仓全量卖出，不受 T+1 可用量限制。
			BypassedT1:  true,
			SubmittedAt: s.FilledAt,
		})
		out.SoldTotal += gross
	}
	for _, s := range res.Skipped {
		out.Skipped = append(out.Skipped, trading.EmergencyFlattenSkip{
			Symbol:   coreSymbol(s.Symbol),
			Quantity: float64(s.Quantity),
			Reason:   s.Reason,
		})
	}
	return out, nil
}

// getJSON 读侧 GET（读面不需要 service token，与 G1 契约一致）。
func (t *Trader) getJSON(ctx context.Context, path string, dst any) error {
	status, raw, err := t.port.do(ctx, http.MethodGet, path, nil, false)
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return t.port.orderError("query "+path, status, raw)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("broker_http: decode %s: %w", path, err)
	}
	return nil
}

// fromPortResult 把 ExecutionPort DTO 转成运行域 DTO。
func fromPortResult(res *execution.OrderResult) *trading.OrderResult {
	if res == nil {
		return nil
	}
	return &trading.OrderResult{
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
	}
}

// coreSymbol 把 broker 的 6 位代码还原成 core 的 `code.EXCHANGE` 口径。
// 带后缀的输入原样返回（幂等）。
func coreSymbol(code string) string {
	s := strings.TrimSpace(code)
	if s == "" || strings.Contains(s, ".") {
		return s
	}
	switch s[0] {
	case '6', '5', '9':
		return s + ".SH"
	case '0', '1', '2', '3':
		return s + ".SZ"
	case '4', '8':
		return s + ".BJ"
	default:
		return s
	}
}

// 编译期断言：Trader 满足 core 运行域的交易通道接口。
var _ trading.LiveTrader = (*Trader)(nil)
