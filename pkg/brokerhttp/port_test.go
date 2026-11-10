package brokerhttp

// SVC-BROKER-G3 的验收测试：Port 的报文映射、错误码透传，以及 Trader
// （core 运行域通道）的持仓 / 账户 / 紧急清仓映射。
//
// 为什么默认用 wire-faithful double：
//   - core 与 tellurion-broker 是两个 Go module，没有 go.work，core **不能**
//     import broker 的 internal/gateway；跨仓只能走 HTTP。
//   - 单元测试里起真 broker 进程需要外部二进制 / 端口编排，不属于单仓门禁。
//     故本文件的 double **按 docs/api-schema.md §1.5 与 broker Mock 的语义**
//     实现携带 token 的 POST/GET/DELETE 与「受理 ≠ 成交」规则。
//   - 真进程腿为 opt-in：BROKER_PARITY_URL 未设置时 skip。
//
// 与进程内撮合的对拍腿已随 SVC-CORE-BROKER-RETIRE 删除（被对拍对象已退役，
// 结论见 core docs/TASKS.md 该单步骤 B 记录）。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	broker "github.com/ruoxizhnya/tellurion-contracts/broker"
	cdom "github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/backtest/execution"
	"tellurion/pkg/domain"
)

// ─── wire-faithful broker double ────────────────────────────────────

type fakeBroker struct {
	token  string
	prices map[string]float64

	mu           sync.Mutex
	seq          int
	states       map[string]broker.OrderState
	acks         map[string]broker.OrderAck
	fingerprints map[string]string
	requests     []broker.OrderRequest
	posts        int
	gets         int
}

func newFakeBroker(t *testing.T, token string, prices map[string]float64) (*httptest.Server, *fakeBroker) {
	t.Helper()
	f := &fakeBroker{
		token:        token,
		prices:       prices,
		states:       make(map[string]broker.OrderState),
		acks:         make(map[string]broker.OrderAck),
		fingerprints: make(map[string]string),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("POST /api/broker/orders", f.handlePlace)
	mux.HandleFunc("GET /api/broker/orders/{id}", f.handleQuery)
	mux.HandleFunc("DELETE /api/broker/orders/{id}", f.handleCancel)
	mux.HandleFunc("GET /api/broker/accounts/{accountID}/positions", f.handlePositions)
	mux.HandleFunc("GET /api/broker/accounts/{accountID}/account", f.handleAccount)
	mux.HandleFunc("POST /api/broker/emergency-flatten", f.handleFlatten)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, f
}

func (f *fakeBroker) handlePlace(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.posts++
	f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		writeFakeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid service token")
		return
	}
	var req broker.OrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeFakeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	fp := fakeFingerprint(req)
	if prev, ok := f.fingerprints[req.ClientOrderID]; ok {
		if prev != fp {
			writeFakeError(w, http.StatusConflict, "duplicate_order", "client_order_id reused with a different payload")
			return
		}
		ack := f.acks[req.ClientOrderID]
		ack.IdempotentReplay = true
		writeFakeJSON(w, http.StatusOK, ack)
		return
	}
	f.fingerprints[req.ClientOrderID] = fp

	now := time.Now().UTC()
	f.seq++
	state := broker.OrderState{
		ClientOrderID:     req.ClientOrderID,
		BrokerOrderID:     fmt.Sprintf("MOCK-%06d", f.seq),
		Status:            broker.OrderAccepted,
		RemainingQuantity: req.Quantity,
		LastUpdate:        now,
	}
	ack := broker.OrderAck{
		ClientOrderID: req.ClientOrderID,
		BrokerOrderID: state.BrokerOrderID,
		Status:        broker.OrderAccepted,
		AcceptedAt:    now,
	}
	// 与 broker Mock 一致：市价单有价格才立即全量成交，限价单停在受理。
	if req.OrderType == broker.OrderMarket {
		if px, ok := f.prices[req.Symbol]; ok && px > 0 {
			state.Status = broker.OrderFilled
			state.FilledQuantity = req.Quantity
			state.RemainingQuantity = 0
			state.AvgFillPrice = px
			ack.Status = broker.OrderFilled
		}
	}
	f.states[req.ClientOrderID] = state
	f.acks[req.ClientOrderID] = ack
	writeFakeJSON(w, http.StatusOK, ack)
}

func (f *fakeBroker) handleQuery(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	state, ok := f.states[r.PathValue("id")]
	if !ok {
		writeFakeError(w, http.StatusNotFound, "order_not_found", "unknown client_order_id")
		return
	}
	writeFakeJSON(w, http.StatusOK, state)
}

func (f *fakeBroker) handleCancel(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		writeFakeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid service token")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	id := r.PathValue("id")
	state, ok := f.states[id]
	if !ok {
		writeFakeError(w, http.StatusNotFound, "order_not_found", "unknown client_order_id")
		return
	}
	if state.Status == broker.OrderFilled || state.Status == broker.OrderCancelled {
		writeFakeError(w, http.StatusConflict, "order_not_cancelable", "order is "+string(state.Status))
		return
	}
	state.Status = broker.OrderCancelled
	state.RemainingQuantity = 0
	state.LastUpdate = time.Now().UTC()
	f.states[id] = state
	writeFakeJSON(w, http.StatusOK, state)
}

func (f *fakeBroker) handlePositions(w http.ResponseWriter, _ *http.Request) {
	writeFakeJSON(w, http.StatusOK, []broker.Position{
		{AccountID: "acc-mock", Symbol: "000001", Quantity: 500, AvailableQuantity: 300, AvgCost: 10, MarketValue: 6250},
		{AccountID: "acc-mock", Symbol: "600000", Quantity: 200, AvailableQuantity: 200, AvgCost: 9, MarketValue: 1640},
	})
}

func (f *fakeBroker) handleAccount(w http.ResponseWriter, _ *http.Request) {
	writeFakeJSON(w, http.StatusOK, broker.AccountSnapshot{
		AccountID:   "acc-mock",
		TotalAssets: 100_000,
		Cash:        broker.Cash{Total: 92_110, Available: 91_110},
		Positions: []broker.Position{
			{AccountID: "acc-mock", Symbol: "000001", Quantity: 500, AvailableQuantity: 300, AvgCost: 10, MarketValue: 6250},
			{AccountID: "acc-mock", Symbol: "600000", Quantity: 200, AvailableQuantity: 200, AvgCost: 9, MarketValue: 1640},
		},
	})
}

func (f *fakeBroker) handleFlatten(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		writeFakeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid service token")
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeFakeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	at := time.Now().UTC()
	writeFakeJSON(w, http.StatusOK, broker.EmergencyFlattenResult{
		Sold: []broker.FlattenedPosition{
			{AccountID: "acc-mock", Symbol: "000001", Quantity: 500, ClientOrderID: "flatten:000001:1:500",
				AvgFillPrice: 12.5, FilledAt: at},
		},
		Skipped:     []broker.SkippedPosition{{AccountID: "acc-mock", Symbol: "600000", Quantity: 200, Reason: "not confirmed"}},
		SoldTotal:   1,
		StartedAt:   at,
		CompletedAt: at,
		Reason:      body.Reason,
	})
}

func fakeFingerprint(req broker.OrderRequest) string {
	raw, _ := json.Marshal(req)
	return string(raw)
}

func writeFakeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeFakeError(w http.ResponseWriter, status int, code, message string) {
	writeFakeJSON(w, status, map[string]string{"code": code, "message": message})
}

func (f *fakeBroker) recordedRequests() []broker.OrderRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]broker.OrderRequest(nil), f.requests...)
}

func (f *fakeBroker) postCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.posts
}

func (f *fakeBroker) getCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gets
}

// ─── 构造辅助 ────────────────────────────────────────────────────────

func newBrokerPortForTest(t *testing.T, srv *httptest.Server, opts ...func(*BrokerHTTPOptions)) *BrokerHTTPPort {
	t.Helper()
	o := BrokerHTTPOptions{BaseURL: srv.URL, ServiceToken: "service-token", AccountID: "acc-mock"}
	for _, opt := range opts {
		opt(&o)
	}
	port, err := NewBrokerHTTPPort(o)
	require.NoError(t, err)
	return port
}

func fixedClientOrderID(id string) func(*BrokerHTTPOptions) {
	return func(o *BrokerHTTPOptions) {
		o.NewClientOrderID = func() (string, error) { return id, nil }
	}
}

// ─── 报文映射 ────────────────────────────────────────────────────────

func TestBrokerHTTPPortSubmitMarketOrderMapsFilled(t *testing.T) {
	srv, fake := newFakeBroker(t, "service-token", map[string]float64{"000001": 12.5})
	port := newBrokerPortForTest(t, srv, fixedClientOrderID("core-fixed-1"))

	res, err := port.SubmitOrder(context.Background(), execution.OrderRequest{
		Symbol:    "000001.SZ",
		Direction: cdom.DirectionLong,
		OrderType: domain.OrderTypeMarket,
		Quantity:  300,
	})
	require.NoError(t, err)
	require.NotNil(t, res)

	// port DTO 保留 core 口径（带交易所后缀）。
	assert.Equal(t, "core-fixed-1", res.OrderID)
	assert.Equal(t, "000001.SZ", res.Symbol)
	assert.Equal(t, cdom.DirectionLong, res.Direction)
	assert.Equal(t, domain.OrderTypeMarket, res.OrderType)
	assert.Equal(t, 300.0, res.Quantity)
	assert.Equal(t, "filled", res.Status)
	assert.Equal(t, 300.0, res.FilledQty)
	assert.Equal(t, 12.5, res.FillPrice)
	assert.Contains(t, res.Message, "broker_order_id=MOCK-000001")
	assert.False(t, res.SubmittedAt.IsZero())

	// wire 侧：6 位代码、buy、market、整股、无 limit_price。
	reqs := fake.recordedRequests()
	require.Len(t, reqs, 1)
	assert.Equal(t, "core-fixed-1", reqs[0].ClientOrderID)
	assert.Equal(t, "acc-mock", reqs[0].AccountID)
	assert.Equal(t, "000001", reqs[0].Symbol)
	assert.Equal(t, broker.SideBuy, reqs[0].Side)
	assert.Equal(t, broker.OrderMarket, reqs[0].OrderType)
	assert.Equal(t, 300, reqs[0].Quantity)
	assert.Zero(t, reqs[0].LimitPrice)

	// 成交明细来自一次查单（回执本身不带成交量/价）。
	assert.Equal(t, 1, fake.getCount())
}

func TestBrokerHTTPPortLimitOrderStaysPending(t *testing.T) {
	srv, fake := newFakeBroker(t, "service-token", nil)
	port := newBrokerPortForTest(t, srv, fixedClientOrderID("core-limit-1"))

	res, err := port.SubmitOrder(context.Background(), execution.OrderRequest{
		Symbol:    "600000.SH",
		Direction: cdom.DirectionClose,
		OrderType: domain.OrderTypeLimit,
		Quantity:  100,
		Price:     9.9,
	})
	require.NoError(t, err)
	assert.Equal(t, "pending", res.Status, "accepted 必须映射成 pending，不得伪造成交")
	assert.Zero(t, res.FilledQty)
	assert.Zero(t, res.FillPrice)

	reqs := fake.recordedRequests()
	require.Len(t, reqs, 1)
	assert.Equal(t, broker.SideSell, reqs[0].Side)
	assert.Equal(t, broker.OrderLimit, reqs[0].OrderType)
	assert.Equal(t, 9.9, reqs[0].LimitPrice)
	assert.Equal(t, 0, fake.getCount(), "受理回执不应触发成交明细查单")
}

func TestBrokerHTTPPortRejectsInvalidIntentBeforeSending(t *testing.T) {
	srv, fake := newFakeBroker(t, "service-token", nil)
	port := newBrokerPortForTest(t, srv)

	cases := []struct {
		name string
		req  execution.OrderRequest
	}{
		{"fractional quantity", execution.OrderRequest{Symbol: "000001.SZ", Direction: cdom.DirectionLong, OrderType: domain.OrderTypeMarket, Quantity: 100.5}},
		{"zero quantity", execution.OrderRequest{Symbol: "000001.SZ", Direction: cdom.DirectionLong, OrderType: domain.OrderTypeMarket, Quantity: 0}},
		{"market with price", execution.OrderRequest{Symbol: "000001.SZ", Direction: cdom.DirectionLong, OrderType: domain.OrderTypeMarket, Quantity: 100, Price: 10}},
		{"limit without price", execution.OrderRequest{Symbol: "000001.SZ", Direction: cdom.DirectionLong, OrderType: domain.OrderTypeLimit, Quantity: 100}},
		{"short direction", execution.OrderRequest{Symbol: "000001.SZ", Direction: cdom.DirectionShort, OrderType: domain.OrderTypeMarket, Quantity: 100}},
		{"unsupported order type", execution.OrderRequest{Symbol: "000001.SZ", Direction: cdom.DirectionLong, OrderType: domain.OrderType("stop"), Quantity: 100}},
		{"empty symbol", execution.OrderRequest{Direction: cdom.DirectionLong, OrderType: domain.OrderTypeMarket, Quantity: 100}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := port.SubmitOrder(context.Background(), tc.req)
			require.Error(t, err)
		})
	}
	assert.Zero(t, fake.postCount(), "非法意图必须在本进程拒绝，不得发出半成品订单")
}

func TestBrokerHTTPPortGetOrderRestoresIntentFromCache(t *testing.T) {
	srv, _ := newFakeBroker(t, "service-token", nil)
	port := newBrokerPortForTest(t, srv, fixedClientOrderID("core-limit-2"))

	_, err := port.SubmitOrder(context.Background(), execution.OrderRequest{
		Symbol:    "600000.SH",
		Direction: cdom.DirectionClose,
		OrderType: domain.OrderTypeLimit,
		Quantity:  200,
		Price:     9.9,
	})
	require.NoError(t, err)

	got, err := port.GetOrder(context.Background(), "core-limit-2")
	require.NoError(t, err)
	assert.Equal(t, "core-limit-2", got.OrderID)
	assert.Equal(t, "600000.SH", got.Symbol, "查单报文不带原始 symbol，靠本地缓存还原")
	assert.Equal(t, cdom.DirectionClose, got.Direction)
	assert.Equal(t, domain.OrderTypeLimit, got.OrderType)
	assert.Equal(t, 200.0, got.Quantity)
	assert.Equal(t, "pending", got.Status)
}

func TestBrokerHTTPPortCancelOrderAndWireCodes(t *testing.T) {
	srv, _ := newFakeBroker(t, "service-token", nil)
	port := newBrokerPortForTest(t, srv, fixedClientOrderID("core-cancel-1"))

	_, err := port.SubmitOrder(context.Background(), execution.OrderRequest{
		Symbol:    "600000.SH",
		Direction: cdom.DirectionLong,
		OrderType: domain.OrderTypeLimit,
		Quantity:  100,
		Price:     9.9,
	})
	require.NoError(t, err)
	require.NoError(t, port.CancelOrder(context.Background(), "core-cancel-1"))

	err = port.CancelOrder(context.Background(), "core-cancel-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "order_not_cancelable")

	err = port.CancelOrder(context.Background(), "core-missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "order_not_found")

	err = port.CancelOrder(context.Background(), " ")
	require.Error(t, err)
}

func TestBrokerHTTPPortPropagatesDuplicateOrderCode(t *testing.T) {
	srv, _ := newFakeBroker(t, "service-token", nil)
	port := newBrokerPortForTest(t, srv, fixedClientOrderID("core-dup-1"))

	_, err := port.SubmitOrder(context.Background(), execution.OrderRequest{
		Symbol: "600000.SH", Direction: cdom.DirectionLong,
		OrderType: domain.OrderTypeLimit, Quantity: 100, Price: 9.9,
	})
	require.NoError(t, err)

	_, err = port.SubmitOrder(context.Background(), execution.OrderRequest{
		Symbol: "600000.SH", Direction: cdom.DirectionLong,
		OrderType: domain.OrderTypeLimit, Quantity: 200, Price: 9.9,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate_order",
		"同一 client_order_id 不同载荷必须被 broker 拒绝且错误码原样透传")
}

func TestBrokerHTTPPortWrongTokenSurfacesUnauthorized(t *testing.T) {
	srv, _ := newFakeBroker(t, "service-token", nil)
	port := newBrokerPortForTest(t, srv, func(o *BrokerHTTPOptions) { o.ServiceToken = "wrong" })

	_, err := port.SubmitOrder(context.Background(), execution.OrderRequest{
		Symbol: "600000.SH", Direction: cdom.DirectionLong,
		OrderType: domain.OrderTypeLimit, Quantity: 100, Price: 9.9,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unauthorized")
}

func TestBrokerHTTPPortHealthCheck(t *testing.T) {
	srv, _ := newFakeBroker(t, "service-token", nil)
	port := newBrokerPortForTest(t, srv)
	require.NoError(t, port.HealthCheck(context.Background()))

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"down"}`))
	}))
	defer down.Close()
	downPort := newBrokerPortForTest(t, down)
	require.Error(t, downPort.HealthCheck(context.Background()))
}

// ─── Trader（core 运行域通道）───────────────────────────────────────

func TestNewTraderFromEnvFailsLoudWithoutBrokerURL(t *testing.T) {
	t.Setenv("BROKER_URL", "")
	_, err := NewTraderFromEnv(zerolog.Nop())
	require.Error(t, err, "SVC-CORE-BROKER-RETIRE 之后无 BROKER_URL 必须装配失败")
	assert.Contains(t, err.Error(), "BROKER_URL is required")

	t.Setenv("BROKER_URL", "http://127.0.0.1:8091")
	t.Setenv("BROKER_SERVICE_TOKEN", "")
	t.Setenv("BROKER_ACCOUNT_ID", "")
	_, err = NewTraderFromEnv(zerolog.Nop())
	require.Error(t, err, "缺 token/账户必须装配失败（fail-closed）")

	t.Setenv("BROKER_SERVICE_TOKEN", "tok")
	t.Setenv("BROKER_ACCOUNT_ID", "acc")
	t.Setenv("BROKER_CHANNEL", "mock")
	t.Setenv("BROKER_ALLOW_MOCK", "true")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			_, _ = w.Write([]byte(`{"service":"brokergateway","channel":"mock"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	t.Setenv("BROKER_URL", srv.URL)
	trader, err := NewTraderFromEnv(zerolog.Nop())
	require.NoError(t, err)
	assert.Equal(t, "broker_http", trader.Name())

	t.Setenv("BROKER_URL", "ftp://127.0.0.1:8091")
	_, err = NewTraderFromEnv(zerolog.Nop())
	require.Error(t, err, "非 http(s) 地址必须拒绝")
}

func TestTraderPositionsAccountAndFlattenMapping(t *testing.T) {
	srv, _ := newFakeBroker(t, "service-token", nil)
	trader, err := NewTrader(BrokerHTTPOptions{
		BaseURL: srv.URL, ServiceToken: "service-token", AccountID: "acc-mock",
	})
	require.NoError(t, err)

	ctx := context.Background()
	positions, err := trader.GetPositions(ctx)
	require.NoError(t, err)
	require.Len(t, positions, 2)
	assert.Equal(t, "000001.SZ", positions[0].Symbol)
	assert.InDelta(t, 500, positions[0].Quantity, 1e-9)
	assert.InDelta(t, 300, positions[0].AvailableQty, 1e-9)
	assert.InDelta(t, 200, positions[0].QuantityToday, 1e-9)
	assert.InDelta(t, 300, positions[0].QuantityYesterday, 1e-9)
	assert.InDelta(t, 12.5, positions[0].CurrentPrice, 1e-9)
	assert.InDelta(t, 1250, positions[0].UnrealizedPnL, 1e-9)
	assert.Equal(t, "600000.SH", positions[1].Symbol)

	account, err := trader.GetAccount(ctx)
	require.NoError(t, err)
	assert.InDelta(t, 100_000, account.TotalAssets, 1e-9)
	assert.InDelta(t, 92_110, account.Cash, 1e-9)
	assert.InDelta(t, 7_890, account.MarketValue, 1e-9)
	assert.InDelta(t, 1_090, account.UnrealizedPnL, 1e-9)
	assert.Zero(t, account.RealizedPnL, "柜台快照不含已实现盈亏，如实缺省")

	flat, err := trader.EmergencyFlatten(ctx, "drill")
	require.NoError(t, err)
	require.Len(t, flat.Sold, 1)
	require.Len(t, flat.Skipped, 1)
	assert.Equal(t, "000001.SZ", flat.Sold[0].Symbol)
	assert.InDelta(t, 500, flat.Sold[0].Quantity, 1e-9)
	assert.InDelta(t, 12.5, flat.Sold[0].FillPrice, 1e-9)
	assert.InDelta(t, 6_250, flat.Sold[0].NetProceeds, 1e-9)
	assert.True(t, flat.Sold[0].BypassedT1)
	assert.InDelta(t, 6_250, flat.SoldTotal, 1e-9)
	assert.Equal(t, "600000.SH", flat.Skipped[0].Symbol)
	assert.Equal(t, "drill", flat.Reason)
}

func TestTraderSubmitOrderMapsToLiveResult(t *testing.T) {
	srv, _ := newFakeBroker(t, "service-token", map[string]float64{"000001": 12.5})
	trader, err := NewTrader(BrokerHTTPOptions{
		BaseURL: srv.URL, ServiceToken: "service-token", AccountID: "acc-mock",
	})
	require.NoError(t, err)

	res, err := trader.SubmitOrder(context.Background(), "000001.SZ",
		cdom.DirectionLong, domain.OrderTypeMarket, 100, 0)
	require.NoError(t, err)
	assert.Equal(t, "filled", res.Status)
	assert.InDelta(t, 100, res.FilledQty, 1e-9)
	assert.InDelta(t, 12.5, res.FillPrice, 1e-9)
}

// TestTraderAgainstRealBrokerProcess 是 opt-in 真进程腿：未设
// BROKER_PARITY_URL 时 skip。起进程步骤见 broker:docs/TASKS.md。
func TestTraderAgainstRealBrokerProcess(t *testing.T) {
	baseURL := os.Getenv("BROKER_PARITY_URL")
	if baseURL == "" {
		t.Skip("BROKER_PARITY_URL 未设置：真 broker 进程腿是 opt-in")
	}
	trader, err := NewTrader(BrokerHTTPOptions{
		BaseURL:      baseURL,
		ServiceToken: os.Getenv("BROKER_PARITY_TOKEN"),
		AccountID:    os.Getenv("BROKER_PARITY_ACCOUNT"),
	})
	require.NoError(t, err)
	require.NoError(t, trader.HealthCheck(context.Background()), "真 broker 进程 /healthz 必须可用")

	res, err := trader.SubmitOrder(context.Background(), "000001.SZ",
		cdom.DirectionLong, domain.OrderTypeMarket, 100, 0)
	require.NoError(t, err)
	assert.Equal(t, "filled", res.Status, "真进程腿要求 Mock 已配参考价并成交")
	assert.InDelta(t, 100, res.FilledQty, 1e-9)
}
