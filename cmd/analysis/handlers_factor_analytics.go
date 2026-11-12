package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"

	cdom "github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/ruoxizhnya/tellurion-kit/httpserver"

	"tellurion/internal/factoranalytics"
	"tellurion/pkg/domain"
	"tellurion/pkg/storage"
)

// B-05：因子分析读面 + 计算触发。
//
// 为什么新增 /api/factor-analytics/* 而不是复活 /api/factor/returns/:factor：
// ADR-041 D1 明确「不提供 per-symbol 的因子求值面」，e2e 也钉住
// `POST /api/factors/compute` 必须 404、`GET /api/factors/:factor_name` 必须
// 410。本组端点只暴露**横截面聚合结果**（分位收益 / IC / 因子列表），
// 不提供任何单票因子值，因此与那条裁决不冲突；路径独立也避免 e2e 误判。
const (
	factorAnalyticsDefaultWindow = 90 * 24 * time.Hour
	factorAnalyticsMaxWindow     = 730 * 24 * time.Hour
	factorAnalyticsComputeBudget = 10 * time.Minute
)

type factorAnalyticsStore interface {
	ListFactorAnalyticsSummaries(ctx context.Context) ([]storage.FactorAnalyticsSummary, error)
	GetFactorReturns(ctx context.Context, factor cdom.FactorType, startDate, endDate time.Time) ([]*domain.FactorReturn, error)
	GetICEntries(ctx context.Context, factor cdom.FactorType, startDate, endDate time.Time) ([]*domain.ICEntry, error)
}

// FactorAnalyticsHandler serves the B-05 factor analytics read surface and the
// computation trigger.
type FactorAnalyticsHandler struct {
	store    factorAnalyticsStore
	computer *factoranalytics.Computer
	logger   zerolog.Logger
}

// NewFactorAnalyticsHandler 构造读面。store 可为 nil（零值 ServerDeps 下路由
// 照常注册、请求时 503）；computer 可为 nil（只读模式，触发计算返回 503）。
func NewFactorAnalyticsHandler(store factorAnalyticsStore, computer *factoranalytics.Computer, logger zerolog.Logger) *FactorAnalyticsHandler {
	return &FactorAnalyticsHandler{
		store:    store,
		computer: computer,
		logger:   logger.With().Str("component", "factor_analytics_handler").Logger(),
	}
}

// RegisterRoutes wires the factor analytics surface.
func (h *FactorAnalyticsHandler) RegisterRoutes(router *gin.Engine) {
	router.GET("/api/factor-analytics/factors", h.listFactors)
	router.GET("/api/factor-analytics/factors/:factor/returns", h.factorReturns)
	router.GET("/api/factor-analytics/factors/:factor/ic", h.factorIC)
	router.POST("/api/factor-analytics/compute", h.compute)
}

type factorAnalyticsSummaryResponse struct {
	FactorName   cdom.FactorType `json:"factor_name"`
	Observations int             `json:"observations"`
	StartDate    string          `json:"start_date"`
	EndDate      string          `json:"end_date"`
	MeanIC       float64         `json:"mean_ic"`
	LatestIC     float64         `json:"latest_ic"`
}

func (h *FactorAnalyticsHandler) listFactors(c *gin.Context) {
	if h.store == nil {
		httpserver.Fail(c, http.StatusServiceUnavailable, "factor analytics store is not configured")
		return
	}
	rows, err := h.store.ListFactorAnalyticsSummaries(c.Request.Context())
	if err != nil {
		h.logger.Error().Err(err).Msg("list factor analytics summaries")
		httpserver.Fail(c, http.StatusInternalServerError, "failed to list factor analytics")
		return
	}
	out := make([]factorAnalyticsSummaryResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, factorAnalyticsSummaryResponse{
			FactorName:   r.FactorName,
			Observations: r.Observations,
			StartDate:    r.StartDate.Format("2006-01-02"),
			EndDate:      r.EndDate.Format("2006-01-02"),
			MeanIC:       r.MeanIC,
			LatestIC:     r.LatestIC,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"factors":   out,
		"supported": supportedFactorNames(),
	})
}

func (h *FactorAnalyticsHandler) factorReturns(c *gin.Context) {
	factor, ok := h.parseFactor(c)
	if !ok {
		return
	}
	start, end, ok := parseAnalyticsWindow(c)
	if !ok {
		return
	}
	rows, err := h.store.GetFactorReturns(c.Request.Context(), factor, start, end)
	if err != nil {
		h.logger.Error().Err(err).Str("factor", string(factor)).Msg("get factor returns")
		httpserver.Fail(c, http.StatusInternalServerError, "failed to read factor returns")
		return
	}
	if rows == nil {
		rows = []*domain.FactorReturn{}
	}
	c.JSON(http.StatusOK, gin.H{
		"factor_name": factor,
		"start_date":  start.Format("2006-01-02"),
		"end_date":    end.Format("2006-01-02"),
		"points":      rows,
	})
}

func (h *FactorAnalyticsHandler) factorIC(c *gin.Context) {
	factor, ok := h.parseFactor(c)
	if !ok {
		return
	}
	start, end, ok := parseAnalyticsWindow(c)
	if !ok {
		return
	}
	rows, err := h.store.GetICEntries(c.Request.Context(), factor, start, end)
	if err != nil {
		h.logger.Error().Err(err).Str("factor", string(factor)).Msg("get factor ic")
		httpserver.Fail(c, http.StatusInternalServerError, "failed to read factor ic")
		return
	}
	if rows == nil {
		rows = []*domain.ICEntry{}
	}
	c.JSON(http.StatusOK, gin.H{
		"factor_name": factor,
		"start_date":  start.Format("2006-01-02"),
		"end_date":    end.Format("2006-01-02"),
		"points":      rows,
	})
}

type factorAnalyticsComputeRequest struct {
	Start           string   `json:"start"`
	End             string   `json:"end"`
	Symbols         []string `json:"symbols"`
	Factors         []string `json:"factors"`
	LookbackDays    int      `json:"lookback_days"`
	HorizonDays     int      `json:"horizon_days"`
	RebalanceDays   int      `json:"rebalance_days"`
	MinCrossSection int      `json:"min_cross_section"`
}

func (h *FactorAnalyticsHandler) compute(c *gin.Context) {
	if h.computer == nil {
		httpserver.Fail(c, http.StatusServiceUnavailable, "factor analytics computation is not configured")
		return
	}
	var req factorAnalyticsComputeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpserver.Fail(c, http.StatusBadRequest, "invalid request body")
		return
	}

	now := time.Now().UTC()
	end, err := parseDateOr(req.End, now)
	if err != nil {
		httpserver.Fail(c, http.StatusBadRequest, err.Error())
		return
	}
	start, err := parseDateOr(req.Start, end.Add(-factorAnalyticsDefaultWindow))
	if err != nil {
		httpserver.Fail(c, http.StatusBadRequest, err.Error())
		return
	}
	if end.Before(start) || end.Sub(start) > factorAnalyticsMaxWindow {
		httpserver.Fail(c, http.StatusBadRequest, "invalid date window")
		return
	}

	factors := make([]cdom.FactorType, 0, len(req.Factors))
	for _, raw := range req.Factors {
		factors = append(factors, cdom.FactorType(raw))
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), factorAnalyticsComputeBudget)
	defer cancel()

	res, err := h.computer.Compute(ctx, factoranalytics.Request{
		Start:           start,
		End:             end,
		Symbols:         req.Symbols,
		Factors:         factors,
		LookbackDays:    req.LookbackDays,
		HorizonDays:     req.HorizonDays,
		RebalanceDays:   req.RebalanceDays,
		MinCrossSection: req.MinCrossSection,
	})
	if err != nil {
		switch {
		case errors.Is(err, factoranalytics.ErrNoData):
			httpserver.Fail(c, http.StatusUnprocessableEntity, err.Error())
		case errors.Is(err, context.DeadlineExceeded):
			httpserver.Fail(c, http.StatusGatewayTimeout, "factor computation timed out")
		default:
			h.logger.Error().Err(err).Msg("compute factor analytics")
			httpserver.Fail(c, http.StatusBadRequest, err.Error())
		}
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"written":         res.Written,
		"universe":        res.Universe,
		"factor_count":    res.FactorCount,
		"rebalance_dates": len(res.RebalanceDates),
		"factor_returns":  len(res.FactorReturns),
		"ic_entries":      len(res.ICEntries),
		"skipped":         res.Skipped,
		"start_date":      start.Format("2006-01-02"),
		"end_date":        end.Format("2006-01-02"),
		"last_trade_date": lastDate(res.RebalanceDates),
	})
}

func (h *FactorAnalyticsHandler) parseFactor(c *gin.Context) (cdom.FactorType, bool) {
	if h.store == nil {
		httpserver.Fail(c, http.StatusServiceUnavailable, "factor analytics store is not configured")
		return "", false
	}
	factor := cdom.FactorType(c.Param("factor"))
	switch factor {
	case cdom.FactorMomentum, cdom.FactorValue, cdom.FactorQuality:
		return factor, true
	default:
		httpserver.Fail(c, http.StatusBadRequest, "unsupported factor: "+string(factor))
		return "", false
	}
}

func parseAnalyticsWindow(c *gin.Context) (time.Time, time.Time, bool) {
	now := time.Now().UTC()
	end, err := parseDateOr(c.Query("end"), now)
	if err != nil {
		httpserver.Fail(c, http.StatusBadRequest, err.Error())
		return time.Time{}, time.Time{}, false
	}
	start, err := parseDateOr(c.Query("start"), end.Add(-factorAnalyticsDefaultWindow))
	if err != nil {
		httpserver.Fail(c, http.StatusBadRequest, err.Error())
		return time.Time{}, time.Time{}, false
	}
	if end.Before(start) || end.Sub(start) > factorAnalyticsMaxWindow {
		httpserver.Fail(c, http.StatusBadRequest, "invalid date window")
		return time.Time{}, time.Time{}, false
	}
	return start, end, true
}

// parseDateOr 解析 YYYY-MM-DD；空串用默认值，格式错误返回错误（不静默改窗口）。
func parseDateOr(raw string, fallback time.Time) (time.Time, error) {
	if raw == "" {
		return fallback, nil
	}
	t, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid date %q: want YYYY-MM-DD", raw)
	}
	return t, nil
}

func supportedFactorNames() []string {
	factors := factoranalytics.SupportedFactors()
	out := make([]string, 0, len(factors))
	for _, f := range factors {
		out = append(out, string(f))
	}
	return out
}

func lastDate(dates []time.Time) string {
	if len(dates) == 0 {
		return ""
	}
	return dates[len(dates)-1].Format("2006-01-02")
}
