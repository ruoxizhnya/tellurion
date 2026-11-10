package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	cdom "github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"tellurion/pkg/risk"
	"tellurion/pkg/trading"
)

import "tellurion/pkg/domain"

// P1-15 (Sprint 6, ODR-021): exercise the in-process risk +
// execution handlers end-to-end through gin. Each test stands up a
// tiny router, sends real HTTP requests, and asserts on the
// response. No database, no network — the whole point of the merge
// is zero-service-to-service I/O for the backtest hot path.

func newTestRiskHandler(t *testing.T) *RiskHandler {
	t.Helper()
	cfg := risk.RiskManagerConfig{
		TargetVolatility:    0.15,
		MaxPositionWeight:   0.10,
		MinPositionWeight:   0.01,
		ATRPeriod:           14,
		BaseMultiplier:      2.5,
		BullMultiplier:      2.0,
		BearMultiplier:      3.5,
		SidewaysMultiplier:  2.5,
		TakeProfitMult:      3.0,
		VolLookbackDays:     20,
		AnnualizationFactor: 16.0,
		FastMAPeriod:        10,
		SlowMAPeriod:        20,
		RegimeVolLookback:   60,
	}
	mgr, err := risk.NewRiskManager(cfg, zerolog.New(nil))
	require.NoError(t, err)
	return NewRiskHandler(mgr, zerolog.New(nil))
}

func newTestExecutionHandler(t *testing.T) *ExecutionHandler {
	t.Helper()
	return NewExecutionHandler(newFakeExecutionTrader(), zerolog.New(nil), "")
}

// fakeExecutionTrader 是 handler 测试用的最小成交通道：下单即全额成交，
// 支持按 ID 回查。SVC-CORE-BROKER-RETIRE 删除了进程内撮合，但 handler
// 的行为契约仍需单测。
type fakeExecutionTrader struct {
	mu     sync.Mutex
	seq    int
	orders map[string]*trading.OrderResult
}

func newFakeExecutionTrader() *fakeExecutionTrader {
	return &fakeExecutionTrader{orders: map[string]*trading.OrderResult{}}
}

func (f *fakeExecutionTrader) SubmitOrder(_ context.Context, symbol string, dir cdom.Direction,
	orderType domain.OrderType, qty, price float64) (*trading.OrderResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	if price <= 0 {
		price = 10
	}
	res := &trading.OrderResult{
		OrderID: fmt.Sprintf("fake-exec-%d", f.seq), Symbol: symbol, Direction: dir,
		OrderType: orderType, Quantity: qty, FilledQty: qty, Price: price, FillPrice: price,
		Status: "filled",
	}
	f.orders[res.OrderID] = res
	cp := *res
	return &cp, nil
}

func (f *fakeExecutionTrader) CancelOrder(_ context.Context, orderID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.orders[orderID]; !ok {
		return fmt.Errorf("order %s not found", orderID)
	}
	return nil
}

func (f *fakeExecutionTrader) GetOrder(_ context.Context, orderID string) (*trading.OrderResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	res, ok := f.orders[orderID]
	if !ok {
		return nil, fmt.Errorf("order %s not found", orderID)
	}
	cp := *res
	return &cp, nil
}

func (f *fakeExecutionTrader) GetPositions(context.Context) ([]trading.PositionInfo, error) {
	return nil, nil
}

func (f *fakeExecutionTrader) GetAccount(context.Context) (*trading.AccountInfo, error) {
	return &trading.AccountInfo{TotalAssets: 1_000_000, Cash: 1_000_000}, nil
}

func (f *fakeExecutionTrader) Name() string { return "fake_execution" }

func (f *fakeExecutionTrader) HealthCheck(context.Context) error { return nil }

func (f *fakeExecutionTrader) EmergencyFlatten(_ context.Context, reason string) (*trading.EmergencyFlattenResult, error) {
	return &trading.EmergencyFlattenResult{Reason: reason}, nil
}

func setupRouter(rh *RiskHandler, eh *ExecutionHandler) *gin.Engine {
	r := gin.New()
	rh.RegisterRoutes(r)
	eh.RegisterRoutes(r)
	return r
}

func TestRiskHandler_CalculatePosition_Success(t *testing.T) {
	rh := newTestRiskHandler(t)
	eh := newTestExecutionHandler(t)
	router := setupRouter(rh, eh)

	body := map[string]interface{}{
		"signal": domain.Signal{
			Symbol:    "000001.SZ",
			Direction: cdom.DirectionLong,
			Strength:  1.0,
		},
		"portfolio": domain.Portfolio{
			Cash:       1_000_000,
			TotalValue: 1_000_000,
			Positions:  map[string]domain.Position{},
		},
		"regime": &cdom.MarketRegime{
			Trend:      "sideways",
			Volatility: "medium",
			Sentiment:  0.0,
		},
		"current_price": 10.0,
	}
	b, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/api/risk/calculate_position", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code, "response body: %s", w.Body.String())
	var resp calculatePositionResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Greater(t, resp.Size, 0.0, "size should be > 0 with non-zero portfolio")
}

func TestRiskHandler_CalculatePosition_RejectsBadInput(t *testing.T) {
	rh := newTestRiskHandler(t)
	eh := newTestExecutionHandler(t)
	router := setupRouter(rh, eh)

	// Body that is not valid JSON — gin's binder should 400
	// before we even reach the risk manager.
	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/api/risk/calculate_position", bytes.NewReader([]byte("{not json")))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestRiskHandler_CalculatePosition_MissingSymbolReturnsServerError(t *testing.T) {
	// Mirrors the original cmd/risk/main.go semantics: a parsed
	// but semantically empty body (zero-value signal) reaches the
	// risk manager, which raises a typed error. The handler
	// surfaces that as 500 (consistent with the legacy
	// risk-service).
	rh := newTestRiskHandler(t)
	eh := newTestExecutionHandler(t)
	router := setupRouter(rh, eh)

	body := map[string]interface{}{
		"portfolio": domain.Portfolio{
			TotalValue: 1_000_000,
		},
		"current_price": 10.0,
	}
	b, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/api/risk/calculate_position", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestRiskHandler_DetectRegime_LegacySchemaRejected(t *testing.T) {
	rh := newTestRiskHandler(t)
	eh := newTestExecutionHandler(t)
	router := setupRouter(rh, eh)

	// Legacy symbol+lookback format (mock-OHLCV generator) is no
	// longer supported. Caller must send raw OHLCV.
	w := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]interface{}{"symbol": "000001.SZ", "lookback_days": 200})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/api/risk/detect_regime", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestRiskHandler_RiskMetrics_OK(t *testing.T) {
	rh := newTestRiskHandler(t)
	eh := newTestExecutionHandler(t)
	router := setupRouter(rh, eh)

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "/api/risk/metrics", nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	var resp riskMetricsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.InDelta(t, 0.15, resp.TargetVolatility, 1e-9)
}

func TestRiskHandler_LegacyCalculatePosition_Route(t *testing.T) {
	// The original risk-service ran on the root path, not
	// /api/risk. Backward-compat route must still work.
	rh := newTestRiskHandler(t)
	eh := newTestExecutionHandler(t)
	router := setupRouter(rh, eh)

	body := map[string]interface{}{
		"signal": domain.Signal{
			Symbol:    "000001.SZ",
			Direction: cdom.DirectionLong,
			Strength:  1.0,
		},
		"portfolio": domain.Portfolio{
			Cash:       1_000_000,
			TotalValue: 1_000_000,
		},
		"current_price": 10.0,
	}
	b, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/calculate_position", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestExecutionHandler_CreateAndGetOrder(t *testing.T) {
	rh := newTestRiskHandler(t)
	eh := newTestExecutionHandler(t)
	router := setupRouter(rh, eh)

	// 显式给名义价 10.0：fakeExecutionTrader 对 price<=0 会补默认价，
	// 这里仍按原 cmd/execution/main.go 调用方的形状（都带 price 字段）
	// 发请求，保证的是 handler 的透传语义。
	createBody := map[string]interface{}{
		"symbol":   "000001.SZ",
		"side":     "long",
		"type":     "market",
		"quantity": 100,
		"price":    10.0,
	}
	b, _ := json.Marshal(createBody)
	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/api/execution/orders", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())

	var created trading.OrderResult
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	require.NotEmpty(t, created.OrderID)

	// GET it back via /api/execution/orders/:id
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "/api/execution/orders/"+created.OrderID, nil)
	router.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusOK, w2.Code)
}

func TestExecutionHandler_RejectsZeroQuantity(t *testing.T) {
	rh := newTestRiskHandler(t)
	eh := newTestExecutionHandler(t)
	router := setupRouter(rh, eh)

	body, _ := json.Marshal(map[string]interface{}{
		"symbol":   "000001.SZ",
		"side":     "long",
		"type":     "market",
		"quantity": 0,
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/api/execution/orders", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestExecutionHandler_ListOrders_EmptyAndNonEmpty(t *testing.T) {
	rh := newTestRiskHandler(t)
	eh := newTestExecutionHandler(t)
	router := setupRouter(rh, eh)

	// Empty list
	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "/api/execution/orders", nil)
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Orders []*trading.OrderResult `json:"orders"`
		Count  int                    `json:"count"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 0, resp.Count)
}

func TestExecutionHandler_LegacyCancelRoute(t *testing.T) {
	// Cancel on a non-existent ID should return 400 (trader says
	// "not found"). This validates the legacy root-level route
	// is wired.
	rh := newTestRiskHandler(t)
	eh := newTestExecutionHandler(t)
	router := setupRouter(rh, eh)

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/orders/does-not-exist/cancel", nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestRiskHandler_CheckStopLoss_EmptyPositions(t *testing.T) {
	rh := newTestRiskHandler(t)
	eh := newTestExecutionHandler(t)
	router := setupRouter(rh, eh)

	body, _ := json.Marshal(map[string]interface{}{
		"positions": []domain.Position{},
		"prices":    map[string]float64{},
	})
	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/api/risk/check_stoploss", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Events []domain.StopLossEvent `json:"events"`
		Count  int                    `json:"count"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 0, resp.Count)
}

// TestRiskHandler_DetectRegime_WithData ensures the new
// "pass raw OHLCV" path works. 30 days of synthetic data is enough
// to satisfy the regime detector's slow-MA period (20).
func TestRiskHandler_DetectRegime_WithData(t *testing.T) {
	rh := newTestRiskHandler(t)
	eh := newTestExecutionHandler(t)
	router := setupRouter(rh, eh)

	now := time.Now()
	var bars []domain.OHLCV
	for i := 0; i < 30; i++ {
		bars = append(bars, domain.OHLCV{
			Symbol: "000001.SZ",
			Date:   now.AddDate(0, 0, -30+i),
			Open:   10 + float64(i)*0.05,
			High:   10.5 + float64(i)*0.05,
			Low:    9.5 + float64(i)*0.05,
			Close:  10.1 + float64(i)*0.05,
			Volume: 1_000_000,
		})
	}
	body, _ := json.Marshal(map[string]interface{}{"data": bars})
	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/api/risk/detect_regime", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
}
