package main

// CORE-LISTING-SEED-UNWIRED 的偏差标记读面：GET /api/listing-windows/status
//
// 为什么需要它：上市日历接不进时，引擎的票池退化为「按今天名单回测历史」
// （pool_source=current，幸存者偏差仍在）。这个退化必须在读面上**看得见**，
// 否则 UI/agent 会把「没接线」误读成「偏差已修好」。本面只报告状态，不参与
// 过滤；引擎本身的空日历行为（不过滤）保持不变。
//
// 读面不挂 RBAC（与其余只读面同姿态）。加载器未配置（data_service.url 为空）
// 时返回 degraded=true + pool_source=current，而不是 404/空对象。

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"tellurion/internal/listingseed"
)

const listingStatusNotConfigured = "listing seed not configured (data_service.url empty)"

// ListingStatusHandler 报告上市日历的接入状态。
type ListingStatusHandler struct {
	loader *listingseed.Loader // nil = 未配置
}

// NewListingStatusHandler 构造读面。loader 为 nil 时明确报未配置。
func NewListingStatusHandler(loader *listingseed.Loader) *ListingStatusHandler {
	return &ListingStatusHandler{loader: loader}
}

// RegisterRoutes wires GET /api/listing-windows/status.
func (h *ListingStatusHandler) RegisterRoutes(router *gin.Engine) {
	router.GET("/api/listing-windows/status", h.getStatus)
}

func (h *ListingStatusHandler) getStatus(c *gin.Context) {
	if h.loader == nil {
		c.JSON(http.StatusOK, listingseed.UnavailableStatus(listingStatusNotConfigured))
		return
	}
	c.JSON(http.StatusOK, h.loader.Status())
}
