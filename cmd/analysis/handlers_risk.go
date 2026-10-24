package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"tellurion/pkg/httpserver"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"tellurion/pkg/risk"
)

// P1-15 (Sprint 6, ODR-021): risk service merged into analysis
// service. The endpoints below are 1:1 byte-compatible with the
// original cmd/risk/main.go routes (HTTP method, path, request body
// shape, response shape). Any existing client that talks to
// `risk-service:8083/{calculate_position,detect_regime,check_stoploss,risk_metrics}`
// can be retargeted at `analysis-service:8085/api/risk/*` with zero
// code change on the client side (paths add an `/api` prefix and
// the path is the same).

// RiskHandler serves risk-related HTTP endpoints from the analysis
// service, backed by an in-process *risk.RiskManager. The risk
// manager is also injected into the backtest engine via
// WithRiskManager (see cmd/analysis/main.go) so the engine never
// makes an HTTP call to itself.
type RiskHandler struct {
	manager *risk.RiskManager
	logger  zerolog.Logger
	// ohlcv 是 detect_regime 粗粒度形状（symbol + 日期区间）的取数面。
	// nil = 未接线：请求该形状时 503 fail-closed，不回退到「没有数据」。
	ohlcv riskOHLCVStore
}

// riskOHLCVStore 是 detect_regime 粗粒度形状的最小取数接口
// （*storage.PostgresStore 满足）。窄接口让 handler 单测不必拖真库。
type riskOHLCVStore interface {
	GetOHLCV(ctx context.Context, symbol string, startDate, endDate time.Time) ([]domain.OHLCV, error)
}

// NewRiskHandler constructs a RiskHandler. The manager MUST be the
// same instance that was injected into the backtest engine so the
// HTTP layer and the engine layer stay in lockstep.
func NewRiskHandler(manager *risk.RiskManager, logger zerolog.Logger) *RiskHandler {
	return &RiskHandler{
		manager: manager,
		logger:  logger.With().Str("component", "risk_handler").Logger(),
	}
}

// WithOHLCVStore 注入 detect_regime 粗粒度形状的取数面（QT-70）。
// 返回 h 便于链式装配；store 为 nil 时保持「未接线 ⇒ 503」。
func (h *RiskHandler) WithOHLCVStore(store riskOHLCVStore) *RiskHandler {
	h.ohlcv = store
	return h
}

// RegisterRoutes wires the risk endpoints under /api/risk on the
// supplied router group. The legacy root-level paths
// (/calculate_position, /detect_regime, /check_stoploss, /risk_metrics)
// are also registered for backward compat — a client retargeted to
// the analysis service at the same port but with the old paths will
// still work.
func (h *RiskHandler) RegisterRoutes(router *gin.Engine) {
	riskGroup := router.Group("/api/risk")
	{
		riskGroup.POST("/calculate_position", h.calculatePosition)
		riskGroup.POST("/detect_regime", h.detectRegime)
		riskGroup.POST("/check_stoploss", h.checkStopLoss)
		riskGroup.GET("/metrics", h.riskMetrics)
	}

	// Legacy compatibility routes — original cmd/risk/main.go
	// registered them at the root, not under /api/risk.
	router.POST("/calculate_position", h.calculatePosition)
	router.POST("/detect_regime", h.detectRegime)
	router.POST("/check_stoploss", h.checkStopLoss)
	router.GET("/risk_metrics", h.riskMetrics)
}

// calculatePositionRequest mirrors the original cmd/risk request
// shape. Signal / Portfolio / Regime / OHLCV are passed through
// to risk.RiskManager.CalculatePosition unchanged.
type calculatePositionRequest struct {
	Signal       domain.Signal        `json:"signal"`
	Portfolio    domain.Portfolio     `json:"portfolio"`
	Regime       *domain.MarketRegime `json:"regime"`
	CurrentPrice float64              `json:"current_price"`
	OHLCV        []domain.OHLCV       `json:"ohlcv,omitempty"`
}

type calculatePositionResponse struct {
	Size       float64 `json:"size"`
	Weight     float64 `json:"weight"`
	StopLoss   float64 `json:"stop_loss"`
	TakeProfit float64 `json:"take_profit"`
	RiskScore  float64 `json:"risk_score"`
}

// calculatePosition computes position size for a single signal.
// Equivalent to POST /calculate_position on the legacy risk service.
func (h *RiskHandler) calculatePosition(c *gin.Context) {
	var req calculatePositionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpserver.Error(c, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	ps, err := h.manager.CalculatePosition(ctx, req.Signal, &req.Portfolio, req.Regime, req.CurrentPrice, req.OHLCV)
	if err != nil {
		h.logger.Warn().Err(err).Str("symbol", req.Signal.Symbol).Msg("calculate_position failed")
		httpserver.Error(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, calculatePositionResponse{
		Size:       ps.Size,
		Weight:     ps.Weight,
		StopLoss:   ps.StopLoss,
		TakeProfit: ps.TakeProfit,
		RiskScore:  ps.RiskScore,
	})
}

// detectRegimeRequest accepts two shapes:
//
//  1. inline `{"data": [OHLCV...]}` — the backtest engine path, unchanged;
//  2. coarse `{"symbol": "...", "start_date": "...", "end_date": "..."}` —
//     QT-70: the handler fetches the bars server-side (qfq daily), so agent
//     callers do not have to round-trip 200+ rows through the prompt.
//
// Inline wins when both are present (it is the pre-existing contract).
type detectRegimeRequest struct {
	Data      []domain.OHLCV `json:"data"`
	Symbol    string         `json:"symbol"`
	StartDate string         `json:"start_date"`
	EndDate   string         `json:"end_date"`
}

func (h *RiskHandler) detectRegime(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	var req detectRegimeRequest
	bindErr := c.ShouldBindJSON(&req)

	// ① 存量形状：内联 OHLCV（回测引擎与旧客户端）。
	if bindErr == nil && len(req.Data) > 0 {
		regime, err := h.manager.DetectRegime(ctx, req.Data)
		if err != nil {
			httpserver.Error(c, http.StatusInternalServerError, err)
			return
		}
		c.JSON(http.StatusOK, regime)
		return
	}

	// ② QT-70 粗粒度形状：symbol + 日期区间，服务端取数。
	if bindErr == nil && (strings.TrimSpace(req.Symbol) != "" || req.StartDate != "" || req.EndDate != "") {
		h.detectRegimeByRange(c, ctx, req)
		return
	}

	if bindErr != nil && !errors.Is(bindErr, io.EOF) {
		httpserver.Fail(c, http.StatusBadRequest, "invalid request body: "+bindErr.Error())
		return
	}

	// 两种形状都没给全：明确点名可选形状，不猜。
	c.JSON(http.StatusBadRequest, gin.H{
		"error": "provide either data (inline OHLCV) or symbol + start_date + end_date (YYYY-MM-DD)",
	})
}

// detectRegimeByRange 实现 QT-70 的粗粒度形状：服务端按 symbol + 区间取
// 前复权日线，再交给进程内 RiskManager 判定 regime。取数面未接线 ⇒ 503
// （fail-closed）；区间内无行 ⇒ 404（「没有数据」不是一份看似合理的 regime）。
func (h *RiskHandler) detectRegimeByRange(c *gin.Context, ctx context.Context, req detectRegimeRequest) {
	symbol := strings.TrimSpace(req.Symbol)
	if symbol == "" || req.StartDate == "" || req.EndDate == "" {
		httpserver.Fail(c, http.StatusBadRequest,
			"symbol, start_date and end_date are all required for the coarse shape (YYYY-MM-DD)")
		return
	}
	startDate, err := time.Parse("2006-01-02", req.StartDate)
	if err != nil {
		httpserver.Fail(c, http.StatusBadRequest, "invalid start_date: use YYYY-MM-DD")
		return
	}
	endDate, err := time.Parse("2006-01-02", req.EndDate)
	if err != nil {
		httpserver.Fail(c, http.StatusBadRequest, "invalid end_date: use YYYY-MM-DD")
		return
	}
	if startDate.After(endDate) {
		httpserver.Fail(c, http.StatusBadRequest, "start_date must not be after end_date")
		return
	}
	if h.ohlcv == nil {
		httpserver.Fail(c, http.StatusServiceUnavailable,
			"OHLCV store unavailable for detect_regime range shape; send inline data instead")
		return
	}

	rows, err := h.ohlcv.GetOHLCV(ctx, symbol, startDate, endDate)
	if err != nil {
		h.logger.Error().Err(err).Str("symbol", symbol).
			Str("start_date", req.StartDate).Str("end_date", req.EndDate).
			Msg("detect_regime range query failed")
		httpserver.FailCause(c, http.StatusInternalServerError, "failed to load OHLCV for detect_regime", err)
		return
	}
	if len(rows) == 0 {
		httpserver.Failf(c, http.StatusNotFound,
			"no OHLCV rows for symbol %q between %s and %s", symbol, req.StartDate, req.EndDate)
		return
	}

	regime, err := h.manager.DetectRegime(ctx, rows)
	if err != nil {
		httpserver.Error(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, regime)
}

// checkStopLossRequest mirrors the original cmd/risk request shape.
type checkStopLossRequest struct {
	Positions []domain.Position  `json:"positions"`
	Prices    map[string]float64 `json:"prices"`
}

func (h *RiskHandler) checkStopLoss(c *gin.Context) {
	var req checkStopLossRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpserver.Error(c, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	events, err := h.manager.CheckStopLoss(ctx, req.Positions, req.Prices)
	if err != nil {
		httpserver.Error(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"events": events,
		"count":  len(events),
	})
}

type riskMetricsResponse struct {
	PortfolioVolatility float64   `json:"portfolio_volatility"`
	TargetVolatility    float64   `json:"target_volatility"`
	MaxPositionWeight   float64   `json:"max_position_weight"`
	MinPositionWeight   float64   `json:"min_position_weight"`
	Timestamp           time.Time `json:"timestamp"`
}

func (h *RiskHandler) riskMetrics(c *gin.Context) {
	cfg := h.manager.GetConfig()
	c.JSON(http.StatusOK, riskMetricsResponse{
		PortfolioVolatility: cfg.TargetVolatility,
		TargetVolatility:    cfg.TargetVolatility,
		MaxPositionWeight:   cfg.MaxPositionWeight,
		MinPositionWeight:   cfg.MinPositionWeight,
		Timestamp:           time.Now().UTC(),
	})
}
