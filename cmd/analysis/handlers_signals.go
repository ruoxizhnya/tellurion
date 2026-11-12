package main

// K8（TASKS K8 ③.4）：外部模型信号 HTTP 写入门。
//
// 端点：
//   POST /api/signals —— 批量写入 ExternalSignal（外部 ML 模型进程外产出，
//   经此门注入内核；消费侧是 strategy.SignalStrategy，按 as_of 防前视拉取）。
//   GET  /api/signals —— 外部信号流读面（A-09）：倒序、可过滤、限量，供 UI
//   纸交易观测台展示最近注入的信号；不带 RBAC（只读面）。
//
// 防御口径（fail-loud，与 pkg/strategy 契约同一份口径）：
//   - ValidateExternalSignal 逐条校验（带索引 400，脏行不落库）；
//   - 未来 as_of 拒绝 400 —— strategy.DayOf 归一后与今天比较，与
//     RejectFutureSignals / SignalStore.ListFor 是同一「未来信号物理不可见」
//     语义（写入门拒绝是第一道闸，契约守卫是第二道）；
//   - SignalContentHash 幂等去重在 SignalStore.Save（ON CONFLICT DO NOTHING），
//     重复注入不重复落库，客户端可安全重试。
//
// 敏感写操作：requireTrader（trader/admin）——与 ExecutionHandler 的
// AUD-02 同款 RBAC 模式。
//
// handler 样板沿用 handlers_evidence.go 的 "pattern B"（struct + functional
// options + RegisterRoutes）。

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"

	"github.com/ruoxizhnya/tellurion-kit/httpserver"
	"tellurion/pkg/auth"
	"tellurion/pkg/strategy"
)

// SignalsHandler serves POST /api/signals (write door) and GET /api/signals
// (observatory feed, A-09).
type SignalsHandler struct {
	store   strategy.SignalStore
	feed    strategy.SignalFeedStore // nil = 后端不支持翻页读（GET 返回 503）
	authSvc *auth.Service
	logger  zerolog.Logger
}

// SignalsHandlerOption configures a SignalsHandler.
type SignalsHandlerOption func(*SignalsHandler)

// WithSignalsAuth enables RBAC on the signal write door (trader or admin;
// viewer gets 403). When svc is nil or disabled this is a no-op
// (open-access mode) — same contract as WithExecutionAuth.
func WithSignalsAuth(svc *auth.Service) SignalsHandlerOption {
	return func(h *SignalsHandler) { h.authSvc = svc }
}

// NewSignalsHandler constructs a SignalsHandler backed by store.
//
// store 可为 nil：registerRoutes 必须容忍零值 ServerDeps 不 panic
// （deps_test.newMinimalDeps 约束），store 缺失推迟到请求时 503 ——
// 路由照常注册，探针/路由表不因未接库而漂移。
func NewSignalsHandler(store strategy.SignalStore, logger zerolog.Logger, opts ...SignalsHandlerOption) *SignalsHandler {
	h := &SignalsHandler{
		store:  store,
		logger: logger.With().Str("component", "signals_handler").Logger(),
	}
	if feed, ok := store.(strategy.SignalFeedStore); ok {
		h.feed = feed
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// RegisterRoutes wires POST /api/signals and GET /api/signals.
//
// GET 是只读观测面（A-09）：UI 纸交易观测台的「外部信号流」。读面不进
// route_policy 表（那张表只管变更端点），也不挂 RBAC —— 与其余读面同姿态。
func (h *SignalsHandler) RegisterRoutes(router *gin.Engine) {
	router.POST("/api/signals", h.requireTrader(), h.createSignals)
	router.GET("/api/signals", h.listSignals)
}

// requireTrader returns the RBAC middleware for the signal write door,
// or a no-op middleware when auth is not wired/enabled（与
// ExecutionHandler.requireTrader 同款：handler 链形状跨配置一致）.
func (h *SignalsHandler) requireTrader() gin.HandlerFunc {
	if h.authSvc == nil {
		return func(c *gin.Context) { c.Next() }
	}
	return h.authSvc.RequireRole(auth.RoleTrader, auth.RoleAdmin)
}

// createSignalsRequest is the batch write body: {"signals": [...]}.
type createSignalsRequest struct {
	Signals []strategy.ExternalSignal `json:"signals"`
}

// createSignalsResponse reports how many signals were accepted.
// 幂等去重在存储层（content_hash ON CONFLICT DO NOTHING），accepted 计
// 「通过校验并提交」的条数；重复注入同批也报 accepted（未新增行）。
type createSignalsResponse struct {
	Accepted int `json:"accepted"`
}

// createSignals handles POST /api/signals.
//
// 校验先于落库：全批校验通过才开始 Save，避免「半批脏写」——单条校验失败
// 即 400 整批拒绝，客户端修正后整批重发（幂等去重保证重发安全）。
func (h *SignalsHandler) createSignals(c *gin.Context) {
	if h.store == nil {
		httpserver.Fail(c, http.StatusServiceUnavailable, "signal store unavailable")
		return
	}

	var req createSignalsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpserver.Fail(c, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if len(req.Signals) == 0 {
		httpserver.Fail(c, http.StatusBadRequest, "signals batch is empty")
		return
	}

	now := time.Now()
	for i, sig := range req.Signals {
		if err := strategy.ValidateExternalSignal(sig); err != nil {
			httpserver.Fail(c, http.StatusBadRequest, fmt.Sprintf("signals[%d]: %v", i, err))
			return
		}
		// 未来 as_of 拒绝（fail-loud）：DayOf 口径与 RejectFutureSignals
		// / SignalStore.ListFor 一致 —— 「未来」的判定只有一份。
		if strategy.DayOf(sig.AsOf).After(strategy.DayOf(now)) {
			httpserver.Fail(c, http.StatusBadRequest,
				fmt.Sprintf("signals[%d]: as_of %s 是未来日期（未来信号物理不可见）",
					i, sig.AsOf.UTC().Format(time.RFC3339)))
			return
		}
	}

	for i, sig := range req.Signals {
		if err := h.store.Save(c.Request.Context(), sig); err != nil {
			httpserver.Fail(c, http.StatusInternalServerError,
				fmt.Sprintf("signals[%d]: save failed: %v", i, err))
			return
		}
	}

	h.logger.Info().Int("accepted", len(req.Signals)).Msg("external signals accepted")
	c.JSON(http.StatusCreated, createSignalsResponse{Accepted: len(req.Signals)})
}

// listSignalsResponse 是 GET /api/signals 的响应：倒序信号流 + 实际生效的
// limit（后端会 clamp 到上限，回显给 UI 便于分页对齐）。
type listSignalsResponse struct {
	Signals []strategy.ExternalSignal `json:"signals"`
	Count   int                       `json:"count"`
	Limit   int                       `json:"limit"`
}

// listSignals handles GET /api/signals (A-09 observatory feed).
//
// 参数：model_id / symbol 可选过滤，limit 默认 100（上限 500，后端硬 clamp），
// up_to 可选（YYYY-MM-DD，默认今天）——「as_of <= up_to」与消费侧 ListFor
// 同一防前视边界，观测台也看不到未来信号。
//
// 空流返回 200 + 空数组（空是合法结果）；后端不具备翻页读能力时 503，
// 明确告知「读面未接」，而不是伪装成空流。
func (h *SignalsHandler) listSignals(c *gin.Context) {
	if h.feed == nil {
		httpserver.Fail(c, http.StatusServiceUnavailable, "signal feed unavailable")
		return
	}

	limit := 100
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			httpserver.Fail(c, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = parsed
	}

	upTo := time.Now()
	if raw := c.Query("up_to"); raw != "" {
		parsed, err := time.Parse("2006-01-02", raw)
		if err != nil {
			httpserver.Fail(c, http.StatusBadRequest, "up_to must be YYYY-MM-DD")
			return
		}
		upTo = parsed
	}

	signals, err := h.feed.ListRecent(c.Request.Context(), c.Query("model_id"), c.Query("symbol"), upTo, limit)
	if err != nil {
		h.logger.Error().Err(err).Msg("external signal feed query failed")
		httpserver.Fail(c, http.StatusInternalServerError, "signal feed query failed")
		return
	}
	if signals == nil {
		signals = []strategy.ExternalSignal{}
	}

	c.JSON(http.StatusOK, listSignalsResponse{
		Signals: signals,
		Count:   len(signals),
		Limit:   limit,
	})
}
