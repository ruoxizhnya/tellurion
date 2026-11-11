package main

// 选股读面（2026-11-12 裁）。
//
// 背景：`POST /api/screen` 原先是把 body 原样反代到 data-service 的 `/screen`，
// 而那条路由在 data 的兼容面里从来不存在 ⇒ 404 被网关折成 502，ui「选股」
// 恒走失败态。分析的语义（基本面筛选）属于 core，裁决要求 **core 本地供数**，
// 与 `handlers_market_index.go`（裁决 ⑰）同一姿势：不新增 data 端点，也不
// 「补一行反代」把 dead link 挪个地方。
//
// 响应形状按 `docs/openapi.yaml` / e2e `api-negative.spec.ts` 已冻的写法：
// `{"count": n, "results": [ScreenResult...]}`；`results` 恒为数组（空结果是
// `[]`，不是 `null`）。请求体同时接受 ui 的扁平字段（`{pe_max, pb_max, ...}`）
// 与 domain.ScreenRequest 的 `{filters:{...}, date, limit}` 嵌套写法——两者
// 是同一个筛选语义的两种拼法，二选一不该是调用方的负担。
//
// 未接库时路由照常注册、请求 503（同 market index 的约束）。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"

	"github.com/ruoxizhnya/tellurion-kit/httpserver"
	"tellurion/pkg/domain"
)

const (
	// screenDefaultLimit 与 ui 下拉的默认档一致；上限防止一次筛全市场。
	screenDefaultLimit = 100
	screenMaxLimit     = 500
	// maxScreenBodyBytes：筛选参数只有十几个浮点键，1MiB 已远超需要。
	maxScreenBodyBytes = 1 << 20
)

// screenFundamentalsReader 是本 handler 的唯一读能力（生产实现是
// *storage.PostgresStore），测试可注入。
type screenFundamentalsReader interface {
	ScreenFundamentals(ctx context.Context, filters domain.ScreenFilters, date *time.Time, limit int) ([]domain.ScreenResult, error)
}

// screenResponse 是 `POST /api/screen` 的唯一形状。
type screenResponse struct {
	Count   int                   `json:"count"`
	Results []domain.ScreenResult `json:"results"`
}

// registerScreenRoutes 挂载 POST /api/screen。reader 可为 nil（未接库），
// 此时路由仍注册、请求 503。
func registerScreenRoutes(router *gin.Engine, reader screenFundamentalsReader, logger zerolog.Logger) {
	h := &screenHandler{reader: reader, logger: logger.With().Str("component", "screen-handler").Logger()}
	router.POST("/api/screen", h.handle)
}

type screenHandler struct {
	reader screenFundamentalsReader
	logger zerolog.Logger
}

// screenRequestBody 同时承载扁平与嵌套两种写法。
type screenRequestBody struct {
	domain.ScreenFilters                       // 扁平（ui 现状）
	Filters              *domain.ScreenFilters `json:"filters"`
	Date                 string                `json:"date"`
	Limit                int                   `json:"limit"`
}

// resolve 归一成 (filters, date, limit)。嵌套写法优先；limit 越界钳制。
func (b screenRequestBody) resolve() (domain.ScreenFilters, *time.Time, int, error) {
	filters := b.ScreenFilters
	if b.Filters != nil {
		filters = *b.Filters
	}
	var date *time.Time
	if s := strings.TrimSpace(b.Date); s != "" {
		parsed, err := time.Parse("2006-01-02", s)
		if err != nil {
			return domain.ScreenFilters{}, nil, 0, fmt.Errorf("invalid date %q: use YYYY-MM-DD", s)
		}
		date = &parsed
	}
	limit := b.Limit
	if limit <= 0 {
		limit = screenDefaultLimit
	}
	if limit > screenMaxLimit {
		limit = screenMaxLimit
	}
	return filters, date, limit, nil
}

func (h *screenHandler) handle(c *gin.Context) {
	if h.reader == nil {
		httpserver.Fail(c, http.StatusServiceUnavailable, "screen data layer unavailable")
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxScreenBodyBytes)
	var body screenRequestBody
	// 空 body = 无筛选条件（e2e api-negative 的既定契约），不是非法输入。
	if err := c.ShouldBindJSON(&body); err != nil && !errors.Is(err, io.EOF) {
		httpserver.Fail(c, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	filters, date, limit, err := body.resolve()
	if err != nil {
		httpserver.Fail(c, http.StatusBadRequest, err.Error())
		return
	}

	rows, err := h.reader.ScreenFundamentals(c.Request.Context(), filters, date, limit)
	if err != nil {
		h.logger.Error().Err(err).Msg("screen read failed")
		httpserver.Fail(c, http.StatusInternalServerError, "screen read failed")
		return
	}
	results := make([]domain.ScreenResult, 0, len(rows))
	results = append(results, rows...)
	c.JSON(http.StatusOK, screenResponse{Count: len(results), Results: results})
}
