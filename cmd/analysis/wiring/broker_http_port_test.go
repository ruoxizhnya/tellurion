package wiring

// SVC-BROKER-G3 的验收测试：BrokerHTTPPort 的报文映射、错误码透传，以及
// 与旧实现（live.MockTrader）的**同批对拍**。
//
// 对拍为什么用 wire-faithful double 而不是真 broker 进程：
//   - core 与 tellurion-broker 是两个 Go module，没有 go.work，core **不能**
//     import broker 的 internal/gateway；跨仓只能走 HTTP。
//   - 单元测试里起真 broker 进程需要外部二进制 / 端口编排，不属于单仓门禁。
//     故本文件的 double **按 docs/api-schema.md §1.5 与 broker Mock 的语义**
//     实现携带 token 的 POST/GET/DELETE 与「受理 ≠ 成交」规则；真进程对拍
//     未跑，已在 broker/docs/TASKS.md 与本仓 docs/TASKS.md 如实登记。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	"tellurion/pkg/live"
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

func TestNewBrokerHTTPPortFromEnv(t *testing.T) {
	t.Setenv("BROKER_URL", "")
	port, err := NewBrokerHTTPPortFromEnv(zerolog.Nop())
	require.NoError(t, err)
	assert.Nil(t, port, "未配置 BROKER_URL = 不切通道（回退旧实现）")

	t.Setenv("BROKER_URL", "http://127.0.0.1:8091")
	t.Setenv("BROKER_SERVICE_TOKEN", "")
	t.Setenv("BROKER_ACCOUNT_ID", "")
	_, err = NewBrokerHTTPPortFromEnv(zerolog.Nop())
	require.Error(t, err, "配了 URL 却缺 token/账户必须装配失败（fail-closed）")

	t.Setenv("BROKER_SERVICE_TOKEN", "tok")
	t.Setenv("BROKER_ACCOUNT_ID", "acc")
	port, err = NewBrokerHTTPPortFromEnv(zerolog.Nop())
	require.NoError(t, err)
	require.NotNil(t, port)
	assert.Equal(t, "broker_http", port.Name())

	t.Setenv("BROKER_URL", "ftp://127.0.0.1:8091")
	_, err = NewBrokerHTTPPortFromEnv(zerolog.Nop())
	require.Error(t, err, "非 http(s) 地址必须拒绝")
}

// ─── 同批对拍：旧 MockTrader vs broker HTTP ─────────────────────────

// TestBrokerHTTPPortParityWithMockTrader 把同一批订单分别送进旧实现与新
// 通道，逐笔比对**订单意图与成交事实**。
//
// 口径（与 docs/TASKS.md 登记一致）：
//   - 一致项：symbol / direction / order_type / quantity / 状态（filled）/
//     FilledQty，以及「成交价 = 通道参考价」—— broker Mock 按参考价成交。
//   - 已知差异（wire 不携带，不是缺陷）：旧 MockTrader 在 core 内再叠加
//     滑点与费用（FillPrice 带滑点、Fee > 0）；broker 报文只有
//     `avg_fill_price`、没有费用字段。费用仍由 core 运行域自算。
//   - 本对拍只覆盖市价买单：旧 MockTrader 的卖出受 T+1 约束、broker Mock
//     不建模 T+1；限价单在 broker 侧是「受理 ≠ 成交」，旧实现是立即成交。
//     这两类差异在 TASKS 里如实登记，不在此伪装成一致。
func TestBrokerHTTPPortParityWithMockTrader(t *testing.T) {
	prices := map[string]float64{
		"000001": 12.5,
		"600000": 8.2,
		"002594": 30.1,
	}
	srv, _ := newFakeBroker(t, "service-token", prices)
	port := newBrokerPortForTest(t, srv)

	const slippage = 0.0005
	mock := live.NewMockTrader(live.MockTraderConfig{
		InitialCash:   1_000_000,
		SlippageRate:  slippage,
		PriceProvider: func(symbol string) float64 { return prices[brokerSymbolOrSelf(t, symbol)] },
	}, zerolog.Nop())

	batch := []execution.OrderRequest{
		{Symbol: "000001.SZ", Direction: cdom.DirectionLong, OrderType: domain.OrderTypeMarket, Quantity: 100},
		{Symbol: "600000.SH", Direction: cdom.DirectionLong, OrderType: domain.OrderTypeMarket, Quantity: 300},
		{Symbol: "002594.SZ", Direction: cdom.DirectionLong, OrderType: domain.OrderTypeMarket, Quantity: 200},
	}
	for _, req := range batch {
		ref := prices[brokerSymbolOrSelf(t, req.Symbol)]

		oldRes, err := mock.SubmitOrder(context.Background(), req.Symbol, req.Direction, req.OrderType, req.Quantity, req.Price)
		require.NoError(t, err, "旧实现下单失败: %s", req.Symbol)

		newRes, err := port.SubmitOrder(context.Background(), req)
		require.NoError(t, err, "broker HTTP 下单失败: %s", req.Symbol)

		// ① 订单意图逐字段一致。
		assert.Equal(t, oldRes.Symbol, newRes.Symbol)
		assert.Equal(t, oldRes.Direction, newRes.Direction)
		assert.Equal(t, oldRes.OrderType, newRes.OrderType)
		assert.Equal(t, oldRes.Quantity, newRes.Quantity)

		// ② 成交事实一致。
		assert.Equal(t, "filled", oldRes.Status)
		assert.Equal(t, "filled", newRes.Status)
		assert.Equal(t, oldRes.FilledQty, newRes.FilledQty)

		// ③ 成交价：新通道 = 参考价；旧实现 = 参考价叠加滑点（已知差异，钉住口径）。
		assert.InDelta(t, ref, newRes.FillPrice, 1e-9, "broker Mock 按参考价成交")
		assert.InDelta(t, ref*(1+slippage), oldRes.FillPrice, 1e-9, "旧 MockTrader 叠加固定滑点")

		// ④ 费用：wire 不带；旧实现有。登记为已知差异。
		assert.Zero(t, newRes.Fee, "broker wire 无费用字段")
		assert.Positive(t, oldRes.Fee, "旧实现自算费用")

		t.Logf("parity %-10s qty=%.0f status=%s/%s fill=%.4f/%.4f fee=%.2f/0",
			req.Symbol, req.Quantity, oldRes.Status, newRes.Status, oldRes.FillPrice, newRes.FillPrice, oldRes.Fee)
	}
}

// brokerSymbolOrSelf 把 `000001.SZ` 归一成 double 的价格表键（测试辅助）。
func brokerSymbolOrSelf(t *testing.T, symbol string) string {
	t.Helper()
	s, err := brokerSymbol(symbol)
	require.NoError(t, err)
	return s
}
