package wiring

// SVC-BROKER-G3（ADR-036 G3）：执行端口的第二实现 —— 直接调
// tellurion-broker 的 G1 HTTP 面（`/api/broker/orders`）。
//
// 边界：
//   - 只实现 `execution.ExecutionPort` 的五个方法（下单 / 撤单 / 查单 /
//     身份 / 健康）。持仓、结算、紧急清仓仍归 core 运行域与本进程的
//     `trading.LiveTrader`（运维面），不进回测引擎的出站端口。
//   - wire 形状的唯一 owner 是 `tellurion-contracts/broker`（CT-05）：
//     本文件只做 `execution` DTO ⇄ 契约类型的映射，不复制报文结构。
//   - 订单真相仍在本仓；broker 只做通道适配与幂等映射。
//
// 开关：`BROKER_URL` 为空 ⇒ 返回 nil（装配层回退旧的 `trading.LiveTrader`，
// 可回滚）；非空则 `BROKER_SERVICE_TOKEN` 与 `BROKER_ACCOUNT_ID` 均必填，
// 缺一即装配错误（fail-closed，不猜默认账户）。

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	broker "github.com/ruoxizhnya/tellurion-contracts/broker"
	cdom "github.com/ruoxizhnya/tellurion-contracts/domain"

	"tellurion/pkg/backtest/execution"
	"tellurion/pkg/domain"
)

// brokerMetaCacheLimit 是 client_order_id → 原始请求元数据的本地缓存上限。
//
// 查单报文（OrderState）只带成交数量/均价，不带方向、委托类型与提交价；
// 这些字段只能从下单时记下来。缓存是**尽力而为**：进程重启或超限淘汰后
// 查单仍返回可用的成交事实（数量/FilledQty/FillPrice/状态），方向等字段
// 退化为零值 —— 不因此让查单失败，也不假装知道。
const brokerMetaCacheLimit = 4096

// brokerHealthPath 是 broker 服务的就绪探针（cmd/brokergateway 的 G0 面）。
const brokerHealthPath = "/healthz"

// BrokerHTTPOptions 是 BrokerHTTPPort 的装配参数。
type BrokerHTTPOptions struct {
	// BaseURL 是 broker 服务根地址，如 `http://127.0.0.1:8091`。
	BaseURL string
	// ServiceToken 是写侧鉴权串（`Authorization: Bearer <token>`）。
	ServiceToken string
	// AccountID 是下单账户。
	AccountID string
	// HTTPClient 可选；nil 用默认客户端（10s 超时）。
	HTTPClient *http.Client
	// NewClientOrderID 可选；nil 用 `core-<16 字节 hex>`。
	NewClientOrderID func() (string, error)
	// Now 可选；nil 用 time.Now。
	Now func() time.Time
}

// BrokerHTTPPort 把 broker HTTP 面适配成回测引擎的 ExecutionPort。
type BrokerHTTPPort struct {
	baseURL    string
	token      string
	accountID  string
	client     *http.Client
	newOrderID func() (string, error)
	now        func() time.Time

	mu    sync.Mutex
	meta  map[string]brokerOrderMeta
	order []string // FIFO：淘汰顺序
}

// brokerOrderMeta 是下单时记下的、查单报文里没有的原始意图字段。
type brokerOrderMeta struct {
	symbol    string
	direction cdom.Direction
	orderType domain.OrderType
	quantity  float64
	price     float64
}

// NewBrokerHTTPPort 构造 HTTP 执行端口；参数非法即返回错误（不做半装配）。
func NewBrokerHTTPPort(opts BrokerHTTPOptions) (*BrokerHTTPPort, error) {
	base := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if base == "" {
		return nil, fmt.Errorf("broker_http_port: BaseURL is required")
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("broker_http_port: invalid BaseURL %q: %w", opts.BaseURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("broker_http_port: BaseURL must be http(s), got %q", opts.BaseURL)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("broker_http_port: BaseURL has no host: %q", opts.BaseURL)
	}
	token := strings.TrimSpace(opts.ServiceToken)
	if token == "" {
		return nil, fmt.Errorf("broker_http_port: ServiceToken is required (BROKER_SERVICE_TOKEN)")
	}
	account := strings.TrimSpace(opts.AccountID)
	if account == "" {
		return nil, fmt.Errorf("broker_http_port: AccountID is required (BROKER_ACCOUNT_ID)")
	}

	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	newOrderID := opts.NewClientOrderID
	if newOrderID == nil {
		newOrderID = defaultClientOrderID
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &BrokerHTTPPort{
		baseURL:    base,
		token:      token,
		accountID:  account,
		client:     client,
		newOrderID: newOrderID,
		now:        now,
		meta:       make(map[string]brokerOrderMeta),
	}, nil
}

// NewBrokerHTTPPortFromEnv 按环境变量构造端口。
//
// `BROKER_URL` 未配置 = 本部署没切 broker 通道，返回 (nil, nil)，装配层
// 回退旧实现（默认路径，可回滚）。其余两键在 URL 已配置时必填。
func NewBrokerHTTPPortFromEnv(logger zerolog.Logger) (*BrokerHTTPPort, error) {
	base := strings.TrimSpace(os.Getenv("BROKER_URL"))
	if base == "" {
		return nil, nil
	}
	port, err := NewBrokerHTTPPort(BrokerHTTPOptions{
		BaseURL:      base,
		ServiceToken: os.Getenv("BROKER_SERVICE_TOKEN"),
		AccountID:    os.Getenv("BROKER_ACCOUNT_ID"),
	})
	if err != nil {
		return nil, err
	}
	logger.Info().
		Str("url", base).
		Str("account_id", port.accountID).
		Msg("execution port switched to broker HTTP gateway (SVC-BROKER-G3)")
	return port, nil
}

// Name 返回实现名。
func (p *BrokerHTTPPort) Name() string { return "broker_http" }

// HealthCheck 探 broker 服务的 `/healthz`。2xx 即健康，其余为错误。
func (p *BrokerHTTPPort) HealthCheck(ctx context.Context) error {
	status, body, err := p.do(ctx, http.MethodGet, brokerHealthPath, nil, false)
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return fmt.Errorf("broker_http: healthz returned %d: %s", status, strings.TrimSpace(string(body)))
	}
	return nil
}

// SubmitOrder 提交一笔订单。
//
// 回执是「受理 ≠ 成交」：`accepted` 映射成 port 的 `pending`；filled /
// partially_filled 回执会再查一次订单状态取真实成交量与成交均价（回执本身不带）。
// 契约拒单（409/400 等）返回 error，错误串里保留 wire code。
func (p *BrokerHTTPPort) SubmitOrder(ctx context.Context, req execution.OrderRequest) (*execution.OrderResult, error) {
	symbol, err := brokerSymbol(req.Symbol)
	if err != nil {
		return nil, err
	}
	side, err := brokerSide(req.Direction)
	if err != nil {
		return nil, err
	}
	orderType, err := brokerOrderType(req.OrderType)
	if err != nil {
		return nil, err
	}
	quantity, err := brokerQuantity(req.Quantity)
	if err != nil {
		return nil, err
	}
	limitPrice := 0.0
	switch orderType {
	case broker.OrderLimit:
		if req.Price <= 0 {
			return nil, fmt.Errorf("broker_http: limit order for %s requires a positive price, got %.4f", req.Symbol, req.Price)
		}
		limitPrice = req.Price
	case broker.OrderMarket:
		if req.Price != 0 {
			return nil, fmt.Errorf("broker_http: market order for %s must have price 0 (execution port contract), got %.4f", req.Symbol, req.Price)
		}
	}

	clientOrderID, err := p.newOrderID()
	if err != nil {
		return nil, fmt.Errorf("broker_http: generate client_order_id: %w", err)
	}
	wireReq := broker.OrderRequest{
		ClientOrderID: clientOrderID,
		AccountID:     p.accountID,
		Symbol:        symbol,
		Side:          side,
		OrderType:     orderType,
		Quantity:      quantity,
		LimitPrice:    limitPrice,
	}

	p.remember(clientOrderID, brokerOrderMeta{
		symbol:    req.Symbol,
		direction: req.Direction,
		orderType: req.OrderType,
		quantity:  req.Quantity,
		price:     req.Price,
	})

	var ack broker.OrderAck
	status, body, err := p.do(ctx, http.MethodPost, "/api/broker/orders", wireReq, true)
	if err != nil {
		return nil, err
	}
	if status < 200 || status > 299 {
		return nil, p.orderError("place order", status, body)
	}
	if err := json.Unmarshal(body, &ack); err != nil {
		return nil, fmt.Errorf("broker_http: decode OrderAck: %w", err)
	}

	result := &execution.OrderResult{
		OrderID:     ack.ClientOrderID,
		Symbol:      req.Symbol,
		Direction:   req.Direction,
		OrderType:   req.OrderType,
		Quantity:    req.Quantity,
		Price:       req.Price,
		Status:      portStatus(ack.Status),
		SubmittedAt: ack.AcceptedAt,
		Message:     ackMessage(ack),
	}
	if ack.Status == broker.OrderFilled || ack.Status == broker.OrderPartiallyFilled {
		p.applyFillDetail(ctx, result, ack.Status, orderType, req.Price, limitPrice)
	}
	return result, nil
}

// GetOrder 按 client_order_id 查订单当前状态。
func (p *BrokerHTTPPort) GetOrder(ctx context.Context, orderID string) (*execution.OrderResult, error) {
	if strings.TrimSpace(orderID) == "" {
		return nil, fmt.Errorf("broker_http: order id is required")
	}
	state, err := p.queryOrder(ctx, orderID)
	if err != nil {
		return nil, err
	}
	result := p.resultFromState(state)
	return result, nil
}

// CancelOrder 撤单。已成交 / 已撤 / 不存在由 broker 返回契约错误（保留 code）。
func (p *BrokerHTTPPort) CancelOrder(ctx context.Context, orderID string) error {
	if strings.TrimSpace(orderID) == "" {
		return fmt.Errorf("broker_http: order id is required")
	}
	status, body, err := p.do(ctx, http.MethodDelete, "/api/broker/orders/"+url.PathEscape(orderID), nil, true)
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return p.orderError("cancel order", status, body)
	}
	var state broker.OrderState
	if err := json.Unmarshal(body, &state); err != nil {
		return fmt.Errorf("broker_http: decode OrderState: %w", err)
	}
	if state.Status == broker.OrderFilled {
		return fmt.Errorf("broker_http: order %s is already filled", orderID)
	}
	return nil
}

// queryOrder 拉取并解析 OrderState。
func (p *BrokerHTTPPort) queryOrder(ctx context.Context, orderID string) (broker.OrderState, error) {
	var state broker.OrderState
	status, body, err := p.do(ctx, http.MethodGet, "/api/broker/orders/"+url.PathEscape(orderID), nil, false)
	if err != nil {
		return state, err
	}
	if status < 200 || status > 299 {
		return state, p.orderError("query order", status, body)
	}
	if err := json.Unmarshal(body, &state); err != nil {
		return state, fmt.Errorf("broker_http: decode OrderState: %w", err)
	}
	return state, nil
}

// applyFillDetail 用一次查单补齐回执里没有的成交量与成交均价。
//
// 查单失败**不回滚已提交的订单**：订单已经进了通道，失败只把「成交明细
// 不可用」写进 Message，调用方仍能看到订单本身与受理状态。
func (p *BrokerHTTPPort) applyFillDetail(ctx context.Context, result *execution.OrderResult, ackStatus broker.OrderStatus,
	orderType broker.OrderType, price, limitPrice float64) {
	state, err := p.queryOrder(ctx, result.OrderID)
	if err != nil {
		if result.Message == "" {
			result.Message = "fill detail unavailable: " + err.Error()
		} else {
			result.Message += "; fill detail unavailable: " + err.Error()
		}
		if ackStatus == broker.OrderFilled {
			result.FilledQty = result.Quantity
		}
		if orderType == broker.OrderLimit {
			result.FillPrice = limitPrice
		} else {
			result.FillPrice = price
		}
		return
	}
	result.FilledQty = float64(state.FilledQuantity)
	result.FillPrice = state.AvgFillPrice
	if detail := stateMessage(state); detail != "" {
		if result.Message == "" {
			result.Message = detail
		} else {
			result.Message += "; " + detail
		}
	}
}

// resultFromState 把查单报文还原成 port DTO（原始意图字段来自本地缓存）。
func (p *BrokerHTTPPort) resultFromState(state broker.OrderState) *execution.OrderResult {
	meta, ok := p.lookup(state.ClientOrderID)
	result := &execution.OrderResult{
		OrderID:     state.ClientOrderID,
		Quantity:    float64(state.FilledQuantity + state.RemainingQuantity),
		FilledQty:   float64(state.FilledQuantity),
		Price:       meta.price,
		Status:      portStatus(state.Status),
		SubmittedAt: state.LastUpdate,
		FillPrice:   state.AvgFillPrice,
		Message:     stateMessage(state),
	}
	if ok {
		result.Symbol = meta.symbol
		result.Direction = meta.direction
		result.OrderType = meta.orderType
		result.Quantity = meta.quantity
		result.Price = meta.price
	}
	return result
}

// do 发一次 JSON 请求。body 为 nil 时不写请求体；needToken 决定是否带
// service token（读侧查单按 G1 契约不需要）。返回原始状态码与响应字节。
func (p *BrokerHTTPPort) do(ctx context.Context, method, path string, body any, needToken bool) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("broker_http: encode request: %w", err)
		}
		reader = bytes.NewReader(raw)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, reader)
	if err != nil {
		return 0, nil, fmt.Errorf("broker_http: build request: %w", err)
	}
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	if needToken {
		httpReq.Header.Set("Authorization", "Bearer "+p.token)
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return 0, nil, fmt.Errorf("broker_http: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("broker_http: read response: %w", err)
	}
	return resp.StatusCode, raw, nil
}

// brokerErrorBody 是 G1 的错误信封（只取本端口消费的字段）。
type brokerErrorBody struct {
	Code    string           `json:"code"`
	Message string           `json:"message"`
	Ack     *broker.OrderAck `json:"ack,omitempty"`
}

// orderError 把非 2xx 响应翻成错误；优先保留 wire code（可判定、可回放）。
func (p *BrokerHTTPPort) orderError(action string, status int, body []byte) error {
	var eb brokerErrorBody
	if err := json.Unmarshal(body, &eb); err == nil && (eb.Code != "" || eb.Message != "") {
		return fmt.Errorf("broker_http: %s rejected (HTTP %d, code=%s): %s", action, status, eb.Code, eb.Message)
	}
	return fmt.Errorf("broker_http: %s failed (HTTP %d): %s", action, status, strings.TrimSpace(string(body)))
}

// ─── 映射函数 ────────────────────────────────────────────────────────

// brokerSymbol 去掉交易所后缀：core 用 `000001.SZ`，broker 报文用 6 位代码。
func brokerSymbol(symbol string) (string, error) {
	s := strings.TrimSpace(symbol)
	if s == "" {
		return "", fmt.Errorf("broker_http: symbol is required")
	}
	if i := strings.IndexByte(s, '.'); i > 0 {
		s = s[:i]
	}
	if s == "" {
		return "", fmt.Errorf("broker_http: invalid symbol %q", symbol)
	}
	return s, nil
}

// brokerSide 把 core 的方向映射成两值买卖；`short` 在 A 股通道不可表达。
func brokerSide(d cdom.Direction) (broker.Side, error) {
	switch d {
	case cdom.DirectionLong:
		return broker.SideBuy, nil
	case cdom.DirectionClose:
		return broker.SideSell, nil
	default:
		return "", fmt.Errorf("broker_http: direction %q cannot be expressed on a long-only A-share channel", d)
	}
}

// brokerOrderType 只允许市价 / 限价（broker 契约两值）。
func brokerOrderType(t domain.OrderType) (broker.OrderType, error) {
	switch t {
	case domain.OrderTypeMarket:
		return broker.OrderMarket, nil
	case domain.OrderTypeLimit:
		return broker.OrderLimit, nil
	default:
		return "", fmt.Errorf("broker_http: unsupported order type %q (broker wire accepts market/limit only)", t)
	}
}

// brokerQuantity 把股数转成正整数（broker 契约是整股 `int`）。
func brokerQuantity(q float64) (int, error) {
	if q <= 0 {
		return 0, fmt.Errorf("broker_http: quantity must be positive, got %.4f", q)
	}
	if math.Trunc(q) != q {
		return 0, fmt.Errorf("broker_http: quantity must be a whole number of shares, got %.4f", q)
	}
	if q > float64(math.MaxInt32) {
		return 0, fmt.Errorf("broker_http: quantity %.0f is out of range", q)
	}
	return int(q), nil
}

// portStatus 把 broker 八值映射成 port 的文档状态集。
//
// `accepted`（已受理未成交）⇒ `pending`；`partially_filled` ⇒ `partial`。
// `unknown` **原样透传**：它是「未决」，伪装成 pending 会让调用方误以为
// 还可以等，伪装成 rejected 会诱发重发 —— 两者都错。见 docs/api-schema.md §2.1。
func portStatus(s broker.OrderStatus) string {
	switch s {
	case broker.OrderPending, broker.OrderAccepted:
		return "pending"
	case broker.OrderPartiallyFilled:
		return "partial"
	case broker.OrderFilled:
		return "filled"
	case broker.OrderCancelled:
		return "cancelled"
	case broker.OrderRejected:
		return "rejected"
	case broker.OrderExpired:
		return "expired"
	case broker.OrderUnknown:
		return "unknown"
	default:
		return string(s)
	}
}

func ackMessage(ack broker.OrderAck) string {
	parts := make([]string, 0, 4)
	if ack.BrokerOrderID != "" {
		parts = append(parts, "broker_order_id="+ack.BrokerOrderID)
	}
	if ack.IdempotentReplay {
		parts = append(parts, "idempotent_replay=true")
	}
	if ack.Code != "" {
		parts = append(parts, "code="+ack.Code)
	}
	if ack.Reason != "" {
		parts = append(parts, "reason="+ack.Reason)
	}
	return strings.Join(parts, " ")
}

func stateMessage(state broker.OrderState) string {
	parts := make([]string, 0, 3)
	if state.BrokerOrderID != "" {
		parts = append(parts, "broker_order_id="+state.BrokerOrderID)
	}
	if state.Code != "" {
		parts = append(parts, "code="+state.Code)
	}
	if state.Reason != "" {
		parts = append(parts, "reason="+state.Reason)
	}
	return strings.Join(parts, " ")
}

// ─── client_order_id 与本地元数据缓存 ───────────────────────────────

// defaultClientOrderID 生成 `core-<16 字节 hex>`。调用方生成、重试不变是
// 契约要求 —— 本端口每次 SubmitOrder 只生成一次，重试由调用方复用同一 ID。
func defaultClientOrderID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return "core-" + hex.EncodeToString(buf[:]), nil
}

func (p *BrokerHTTPPort) remember(id string, meta brokerOrderMeta) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.meta[id]; !exists {
		for len(p.order) >= brokerMetaCacheLimit {
			oldest := p.order[0]
			p.order = p.order[1:]
			delete(p.meta, oldest)
		}
		p.order = append(p.order, id)
	}
	p.meta[id] = meta
}

func (p *BrokerHTTPPort) lookup(id string) (brokerOrderMeta, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	meta, ok := p.meta[id]
	return meta, ok
}

// 编译期断言：适配器满足引擎端口。
var _ execution.ExecutionPort = (*BrokerHTTPPort)(nil)
