package main

// handlers_fundamental.go — 归属裁决（2026-11-12）
//
// `stock_fundamentals` 是 core 自己的表：DDL 在 pkg/storage/postgres.go，写者
// 与读者都在本仓，`screen` / 回测 PIT 也直接读它。历史上 core 的行情 provider
// 却把 `/fundamental/{symbol}` 打到数据服务，而数据服务的实现指向
// `market.stock_fundamentals` —— 那张表从未建立，于是「没同步过」被冒成 500。
//
// 裁决：归 core。读面读回 `public.stock_fundamentals`；数据服务删掉那条
// market 读路径。空表/无记录是**业务空态**，用 404 `fundamental_not_found`
// 表达；表真的缺由 `/readyz` 的就绪检查暴露。

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ruoxizhnya/tellurion-kit/httpserver"
	"tellurion/pkg/domain"
	"tellurion/pkg/storage"
)

// fundamentalReader 是本 handler 的唯一读能力（生产实现是
// *storage.PostgresStore），测试可注入。
type fundamentalReader interface {
	GetFundamental(ctx context.Context, symbol string, date time.Time) (*domain.Fundamental, error)
}

// fundamentalResponse 与数据服务退役前的读面形状保持一致：
// {"fundamental": {...}}，便于既有调用方无感切换。
type fundamentalResponse struct {
	Fundamental *domain.Fundamental `json:"fundamental"`
}

// registerFundamentalRoutes 挂载 GET /api/fundamental/:symbol。
// reader 可为 nil（未接库），此时路由仍注册、请求 503。
func registerFundamentalRoutes(router *gin.Engine, reader fundamentalReader) {
	router.GET("/api/fundamental/:symbol", func(c *gin.Context) {
		if reader == nil {
			httpserver.FailCode(c, http.StatusServiceUnavailable, "dependency_unavailable",
				"fundamental store is not wired")
			return
		}

		symbol := strings.TrimSpace(c.Param("symbol"))
		if symbol == "" {
			httpserver.FailCode(c, http.StatusBadRequest, "invalid_parameter", "symbol is required")
			return
		}

		asOf, err := parseFundamentalDate(c.Query("date"))
		if err != nil {
			httpserver.FailCode(c, http.StatusBadRequest, "invalid_parameter", err.Error())
			return
		}

		f, err := reader.GetFundamental(c.Request.Context(), symbol, asOf)
		switch {
		case errors.Is(err, storage.ErrFundamentalNotFound):
			httpserver.FailCode(c, http.StatusNotFound, "fundamental_not_found",
				"no fundamental record for "+symbol+" on or before "+asOf.Format("2006-01-02"))
		case err != nil:
			httpserver.FailCode(c, http.StatusInternalServerError, "internal_error", err.Error())
		default:
			c.JSON(http.StatusOK, fundamentalResponse{Fundamental: f})
		}
	})
}

// parseFundamentalDate 接受 YYYY-MM-DD 与 YYYYMMDD（数据服务退役面的旧格式）；
// 为空时按「今天」取最近一期。
func parseFundamentalDate(raw string) (time.Time, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Now(), nil
	}
	for _, layout := range []string{"2006-01-02", "20060102"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errors.New("invalid date " + s + ": use YYYY-MM-DD or YYYYMMDD")
}
