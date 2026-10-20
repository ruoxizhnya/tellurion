package main

import (
	"context"
	"fmt"
	"tellurion/pkg/httpserver"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"tellurion/pkg/data"
	"tellurion/pkg/domain"
)

// dataDateLayout 是 /ohlcv 与缓存预热这批端点接受的日期格式（DATA-13）：tushare 源的
// YYYYMMDD。与 handlers_factor.go、handlers_sync.go 的既有校验同形（同一把尺子）。
const dataDateLayout = "20060102"

// invalidDateParam 校验一个 YYYYMMDD 日期参数，返回 400 文案（空串 = 合法）。
//
// DATA-13：这些端点的日期原来被原样丢给 pkg/data 的 parseDate（`t, _ := time.Parse(...)`
// 吞错），ISO 写法解析成零值日期、查询范围塌成 [0001-01-01, 0001-01-01]，于是 200 回
// `{"ohlcv":null}` —— 把「我不认这个格式」显示成「这段时间没有行情」。在进库前先
// fail-loud，坏格式 ⇒ 400 且文案点名要 YYYYMMDD。`2024-01-01` 与 `20240101` 分得开，
// 不再两条路都通向空读数。
func invalidDateParam(name, value string) string {
	if _, err := time.Parse(dataDateLayout, value); err != nil {
		return fmt.Sprintf("invalid %s format, use YYYYMMDD", name)
	}
	return ""
}

func getOHLCVHandler(dc *data.DataCache) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		symbol := c.Param("symbol")
		startDateStr := c.Query("start_date")
		endDateStr := c.Query("end_date")

		if startDateStr == "" || endDateStr == "" {
			httpserver.Fail(c, http.StatusBadRequest, "start_date and end_date query params required (YYYYMMDD)")
			return
		}

		// DATA-13：格式先校验再进库 —— 不认的写法不能静默塌成空读数（见 invalidDateParam）。
		if msg := invalidDateParam("start_date", startDateStr); msg != "" {
			httpserver.Fail(c, http.StatusBadRequest, msg)
			return
		}
		if msg := invalidDateParam("end_date", endDateStr); msg != "" {
			httpserver.Fail(c, http.StatusBadRequest, msg)
			return
		}

		// Use DataCache for cache-aside access — same key format as cache warm endpoint
		ohlcv, err := dc.GetOHLCV(ctx, symbol, startDateStr, endDateStr)
		if err != nil {
			httpserver.Error(c, http.StatusInternalServerError, err)
			return
		}

		c.JSON(http.StatusOK, gin.H{"ohlcv": ohlcv})
	}
}

func bulkOHLCVHandler(dc *data.DataCache) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()

		var req struct {
			Symbols   []string `json:"symbols"`
			StartDate string   `json:"start_date"`
			EndDate   string   `json:"end_date"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			httpserver.Wrap(c, http.StatusBadRequest, err, "invalid request body: ")
			return
		}
		if len(req.Symbols) == 0 {
			httpserver.Fail(c, http.StatusBadRequest, "symbols is required")
			return
		}

		// DATA-13：给了日期就必须是 YYYYMMDD（空值保持既有的「可不传」语义不变）。
		if msg := invalidDateParam("start_date", req.StartDate); req.StartDate != "" && msg != "" {
			httpserver.Fail(c, http.StatusBadRequest, msg)
			return
		}
		if msg := invalidDateParam("end_date", req.EndDate); req.EndDate != "" && msg != "" {
			httpserver.Fail(c, http.StatusBadRequest, msg)
			return
		}

		// Fetch all symbols in parallel via DataCache (Redis → PostgreSQL fallback)
		type result struct {
			Symbol string         `json:"symbol"`
			OHLCV  []domain.OHLCV `json:"ohlcv"`
			Error  string         `json:"error,omitempty"`
		}
		results := make([]result, len(req.Symbols))

		var wg sync.WaitGroup
		var mu sync.Mutex
		for i, symbol := range req.Symbols {
			wg.Add(1)
			go func(idx int, sym string) {
				defer wg.Done()
				ohlcv, err := dc.GetOHLCV(ctx, sym, req.StartDate, req.EndDate)
				r := result{Symbol: sym}
				if err != nil {
					r.Error = err.Error()
				} else {
					r.OHLCV = ohlcv
				}
				mu.Lock()
				results[idx] = r
				mu.Unlock()
			}(i, symbol)
		}
		wg.Wait()

		c.JSON(http.StatusOK, gin.H{"results": results})
	}
}
func warmCacheHandler(dc *data.DataCache) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req warmCacheRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			httpserver.Error(c, http.StatusBadRequest, err)
			return
		}
		if len(req.Symbols) == 0 {
			httpserver.Fail(c, http.StatusBadRequest, "symbols array is required")
			return
		}
		if req.StartDate == "" || req.EndDate == "" {
			httpserver.Fail(c, http.StatusBadRequest, "start_date and end_date are required (YYYYMMDD)")
			return
		}
		// DATA-13：同 getOHLCVHandler，坏格式先 400，不进库。
		if msg := invalidDateParam("start_date", req.StartDate); msg != "" {
			httpserver.Fail(c, http.StatusBadRequest, msg)
			return
		}
		if msg := invalidDateParam("end_date", req.EndDate); msg != "" {
			httpserver.Fail(c, http.StatusBadRequest, msg)
			return
		}

		ctx := c.Request.Context()
		if err := dc.WarmCache(ctx, req.Symbols, req.StartDate, req.EndDate); err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				httpserver.Fail(c, http.StatusGatewayTimeout, "cache warm-up timed out")
				return
			}
			httpserver.Error(c, http.StatusInternalServerError, err)
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"message":    "cache warmed successfully",
			"symbols":    len(req.Symbols),
			"start_date": req.StartDate,
			"end_date":   req.EndDate,
		})
	}
}

// ---- Dividend Sync Handlers ----

type syncDividendsRequest struct {
	Symbols   []string `json:"symbols"`
	StartDate string   `json:"start_date"` // YYYYMMDD
	EndDate   string   `json:"end_date"`   // YYYYMMDD
}

// syncDividendsHandler syncs dividend data from Tushare for the given symbols (or all stocks if not specified).
// POST /sync/dividends
