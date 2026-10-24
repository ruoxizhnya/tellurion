package main

// handlers_reference_data.go —— 交易日历 / 上市窗口只读端点（QT-67）。
//
// 为什么需要这一族：D24 的粗粒度动作清单原本只有六类，agent 要判断
// 「这轮回测的池子是不是按日在市过滤的」时，只能 import core 的
// `pkg/storage` 直接读库（`tellurion-agent:cmd/ai/handlers_explore.go`）。
// 那条 import 是 agent 侧最后一处 core 实现包依赖，也是删 `replace` 的
// 前置。2026-10-24 用户确认把它补成 D24 **第 7 类**动作：
//
//   - 日历按区间查：`GET  /api/v1/trading-calendar`
//   - 上市窗口按标的查：`POST /api/v1/listing-windows`
//
// 两条都**只读**（不写任何表）。上市窗口用 POST 只因参数是集合（与
// `POST /api/v1/ohlcv/bulk` 同一取向）：一次请求承载整批 symbol，
// **不是** per-symbol 端点 —— D24 禁 per-symbol / per-bar / per-order
// 远程调用，`agent_coarse_actions_test.go` 的粒度腿会挡回退。
//
// 存储未装配（`deps.Store == nil` 或连接池为 nil）时 **503 fail-closed**，
// 不返回「空日历 / 空窗口」—— 把「读不到」显示成「没有数据」会让偏差维
// 把一笔真债当成已还（同 `stocks.go` GetListingWindows 的注释口径）。

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"

	"tellurion/pkg/httpserver"
	"tellurion/pkg/storage"
)

// referenceDataStore 是只读仪器域数据的最小接口（*storage.PostgresStore 满足）。
// 窄接口让 handler 单测不必拖真库。
type referenceDataStore interface {
	GetTradingCalendar(ctx context.Context, startDate, endDate time.Time) ([]storage.TradingCalendarEntry, error)
	GetListingWindows(ctx context.Context) (map[string]storage.ListingWindow, error)
}

// ReferenceDataHandler serves the two read-only reference-data endpoints.
type ReferenceDataHandler struct {
	store  referenceDataStore
	logger zerolog.Logger
}

// NewReferenceDataHandler 构造只读 handler。store 可为 nil（零值 ServerDeps
// 下路由照常注册，请求时 503）——与 signals / factor registration 同款约束。
func NewReferenceDataHandler(store referenceDataStore, logger zerolog.Logger) *ReferenceDataHandler {
	return &ReferenceDataHandler{
		store:  store,
		logger: logger.With().Str("component", "reference_data_handler").Logger(),
	}
}

// RegisterRoutes wires the read-only reference-data endpoints.
func (h *ReferenceDataHandler) RegisterRoutes(router *gin.Engine) {
	router.GET("/api/v1/trading-calendar", h.tradingCalendar)
	router.POST("/api/v1/listing-windows", h.listingWindows)
}

// ─── 交易日历 ────────────────────────────────────────────────────────

// tradingCalendarEntryDTO 是单行日历。日期按仓库约定用 `YYYY-MM-DD`
// （openapi info 的 Conventions），不是 RFC3339。
type tradingCalendarEntryDTO struct {
	TradeDate    string `json:"trade_date"`
	Exchange     string `json:"exchange"`
	IsTradingDay bool   `json:"is_trading_day"`
}

// tradingCalendarResponse 是 `GET /api/v1/trading-calendar` 的 200 形状。
// 与 docs/openapi.yaml 的 TradingCalendarResponse 由
// handlers_reference_data_test.go 逐字段对拍。
type tradingCalendarResponse struct {
	StartDate string                    `json:"start_date"`
	EndDate   string                    `json:"end_date"`
	Count     int                       `json:"count"`
	Entries   []tradingCalendarEntryDTO `json:"entries"`
}

func (h *ReferenceDataHandler) tradingCalendar(c *gin.Context) {
	if h.store == nil {
		httpserver.Fail(c, http.StatusServiceUnavailable, "trading calendar store unavailable")
		return
	}

	startStr := strings.TrimSpace(c.Query("start"))
	endStr := strings.TrimSpace(c.Query("end"))
	if startStr == "" || endStr == "" {
		httpserver.Fail(c, http.StatusBadRequest, "start and end query params required (YYYY-MM-DD)")
		return
	}
	startDate, err := time.Parse("2006-01-02", startStr)
	if err != nil {
		httpserver.Fail(c, http.StatusBadRequest, "invalid start: use YYYY-MM-DD")
		return
	}
	endDate, err := time.Parse("2006-01-02", endStr)
	if err != nil {
		httpserver.Fail(c, http.StatusBadRequest, "invalid end: use YYYY-MM-DD")
		return
	}
	if startDate.After(endDate) {
		httpserver.Fail(c, http.StatusBadRequest, "start must not be after end")
		return
	}

	entries, err := h.store.GetTradingCalendar(c.Request.Context(), startDate, endDate)
	if err != nil {
		h.logger.Error().Err(err).Str("start", startStr).Str("end", endStr).
			Msg("query trading calendar failed")
		httpserver.Error(c, http.StatusInternalServerError, err)
		return
	}

	out := make([]tradingCalendarEntryDTO, 0, len(entries))
	for _, e := range entries {
		out = append(out, tradingCalendarEntryDTO{
			TradeDate:    e.TradeDate.Format("2006-01-02"),
			Exchange:     e.Exchange,
			IsTradingDay: e.IsTradingDay,
		})
	}

	c.JSON(http.StatusOK, tradingCalendarResponse{
		StartDate: startStr,
		EndDate:   endStr,
		Count:     len(out),
		Entries:   out,
	})
}

// ─── 上市窗口 ────────────────────────────────────────────────────────

// listingWindowsRequest 是请求体。`symbols` 为空 = 全部标的；
// 带值时只返回这些标的的窗口（未知标的**省略**，由调用方按请求集对差）。
type listingWindowsRequest struct {
	Symbols []string `json:"symbols"`
}

// listingWindowDTO 是单个标的的上市窗口。`list_date` / `delist_date`
// 在库里为 NULL 时输出 `null`（未知 ≠ 一个看起来合理的日期）。
type listingWindowDTO struct {
	Symbol     string  `json:"symbol"`
	ListDate   *string `json:"list_date"`
	DelistDate *string `json:"delist_date"`
}

// listingWindowsResponse 是 `POST /api/v1/listing-windows` 的 200 形状。
// 与 docs/openapi.yaml 的 ListingWindowsResponse 由
// handlers_reference_data_test.go 逐字段对拍。
type listingWindowsResponse struct {
	Count   int                `json:"count"`
	Windows []listingWindowDTO `json:"windows"`
}

func (h *ReferenceDataHandler) listingWindows(c *gin.Context) {
	if h.store == nil {
		httpserver.Fail(c, http.StatusServiceUnavailable, "listing window store unavailable")
		return
	}

	var req listingWindowsRequest
	// 空 body 等价于 `{}`（= 全部标的）；非法 JSON 仍是 400。
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		httpserver.Fail(c, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	wanted := make(map[string]bool, len(req.Symbols))
	for i, sym := range req.Symbols {
		sym = strings.TrimSpace(sym)
		if sym == "" {
			httpserver.Failf(c, http.StatusBadRequest, "symbols[%d] is empty", i)
			return
		}
		wanted[sym] = true
	}

	all, err := h.store.GetListingWindows(c.Request.Context())
	if err != nil {
		h.logger.Error().Err(err).Msg("query listing windows failed")
		httpserver.Error(c, http.StatusInternalServerError, err)
		return
	}

	symbols := make([]string, 0, len(all))
	for sym := range all {
		if len(wanted) > 0 && !wanted[sym] {
			continue
		}
		symbols = append(symbols, sym)
	}
	// 稳定顺序：map 迭代顺序随机，响应必须可对拍、可 diff。
	sort.Strings(symbols)

	out := make([]listingWindowDTO, 0, len(symbols))
	for _, sym := range symbols {
		w := all[sym]
		out = append(out, listingWindowDTO{
			Symbol:     sym,
			ListDate:   datePtr(w.List),
			DelistDate: datePtrOf(w.Delist),
		})
	}

	c.JSON(http.StatusOK, listingWindowsResponse{
		Count:   len(out),
		Windows: out,
	})
}

// datePtr 把零值时间输出成 null（库里 list_date 可空）。
func datePtr(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.Format("2006-01-02")
	return &s
}

func datePtrOf(t *time.Time) *string {
	if t == nil {
		return nil
	}
	return datePtr(*t)
}
