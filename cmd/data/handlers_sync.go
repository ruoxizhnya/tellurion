package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"tellurion/pkg/httpserver"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ruoxizhnya/tellurion-kit/logging"
	"tellurion/pkg/data"
	"tellurion/pkg/storage"
)

func getIndexConstituentsHandler(tc *data.TushareClient, store *storage.PostgresStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		indexCode := c.Param("code")
		date := c.Query("date")

		// Validate supported indexes
		if indexCode != "000300.SH" && indexCode != "000500.SH" && indexCode != "000852.SH" {
			httpserver.Fail(c, http.StatusBadRequest, "unsupported index code, supported: 000300.SH (CSI 300), 000500.SH (CSI 500), 000852.SH (CSI 800)")
			return
		}

		constituents, err := tc.GetIndexConstituents(ctx, indexCode, date)
		if err != nil {
			httpserver.Error(c, http.StatusInternalServerError, err)
			return
		}

		c.JSON(http.StatusOK, gin.H{"index_code": indexCode, "constituents": constituents})
	}
}

// syncIndexConstituentsHandler fetches index constituents from Tushare and saves to DB.
// POST /sync/index-constituents/:index_code
func syncIndexConstituentsHandler(tc *data.TushareClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		indexCode := c.Param("index_code")

		// Validate supported indexes
		if indexCode != "000300.SH" && indexCode != "000500.SH" && indexCode != "000852.SH" {
			httpserver.Fail(c, http.StatusBadRequest, "unsupported index code, supported: 000300.SH (CSI 300), 000500.SH (CSI 500), 000852.SH (CSI 800)")
			return
		}

		// Fetch latest constituents (no specific date = latest).
		// DATA-06：FetchIndexConstituents 现在把落库 error 上抛（旧形态只 Warn
		// 后返回成功）。两种失败都回 5xx，但按哨兵区分「哪一步」的静态文案 ——
		// 内部原因按本仓 5xx 口径只进日志（httpserver.Error 的 cause），不进响应体。
		constituents, err := tc.FetchIndexConstituents(ctx, indexCode, "")
		if err != nil {
			if errors.Is(err, data.ErrIndexConstituentPersistFailed) {
				httpserver.FailCause(c, http.StatusInternalServerError,
					"index constituents fetched but failed to persist", err)
				return
			}
			httpserver.Error(c, http.StatusInternalServerError, err)
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"message":         "index constituents synced successfully",
			"index_code":      indexCode,
			"fetched_count":   len(constituents),
			"persisted_count": len(constituents),
		})
	}
}

// syncIndexDailyHandler fetches index daily bars from Tushare and saves to DB.
// POST /sync/index-daily/:index_code
//
// DATA-12：指数日线同步入口（HTTP 层，仿 DATA-06 的 index-constituents 形态）。
// 校验用 isMarketIndexCode（与 /market/index 的服务集同一份 marketIndexCodes，
// 不另抄代码清单）。start_date / end_date 可选（YYYYMMDD；不传 = 不限，由
// tushare 返回该指数全部/首个分页）。
//
// 落库失败经 data.ErrIndexDailyPersistFailed 哨兵区分环节：两种失败都回 5xx，
// 但静态文案不同（「取到了但没落库」与「取数失败」不压成同一形状），内部原因
// 按本仓 5xx 口径只进日志（httpserver.Error/FailCause 的 cause），不进响应体。
func syncIndexDailyHandler(tc *data.TushareClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		indexCode := c.Param("index_code")

		if !isMarketIndexCode(indexCode) {
			httpserver.Fail(c, http.StatusBadRequest,
				fmt.Sprintf("unsupported index code, supported: %s", strings.Join(marketIndexCodes, ", ")))
			return
		}

		rows, err := tc.FetchIndexDaily(ctx, indexCode, c.Query("start_date"), c.Query("end_date"))
		if err != nil {
			if errors.Is(err, data.ErrIndexDailyPersistFailed) {
				httpserver.FailCause(c, http.StatusInternalServerError,
					"index daily fetched but failed to persist", err)
				return
			}
			httpserver.Error(c, http.StatusInternalServerError, err)
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"message":         "index daily synced successfully",
			"index_code":      indexCode,
			"fetched_count":   len(rows),
			"persisted_count": len(rows),
		})
	}
}

// syncSectorsHandler 由 stocks.industry 派生 sectors 与 stock_sector_map。
// POST /sync/sectors
//
// DATA-01：东财板块出口实测 502，板块表改由库内已有的行业字段物化。
// 不新拉外网数据，因此不需要 Tushare token（token 缺失时照样能灌）。
func syncSectorsHandler(store *storage.PostgresStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()

		result, err := data.SyncSectorsFromStocks(ctx, store, time.Now())
		if err != nil {
			httpserver.Error(c, http.StatusInternalServerError, err)
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"message":  "sectors synced successfully",
			"sectors":  result.Sectors,
			"mappings": result.Mappings,
			"skipped":  result.Skipped,
			"source":   result.Source,
		})
	}
}

// REV-23(10)：包级 syncStocksHandler / syncAllOHLCVHandler /
// syncFundamentalsHandler 与两个配套请求类型已删除——路由实际注册的是
// SyncHandler 的方法版（main.go: /sync/stocks 等 → syncHandler.*Handler，
// 见 sync_handlers.go），这三个包级函数自 handlers 拆分起零调用（其中
// ohlcv/all 版还内嵌 context.Background goroutine 模板）。参考实现
// 若需要可从 git 历史取（本次删除前的任一提交）。

type syncOHLCVRequest struct {
	Symbols   []string `json:"symbols"`
	StartDate string   `json:"start_date"`
	EndDate   string   `json:"end_date"`
}

func syncOHLCVHandler(tc *data.TushareClient, store *storage.PostgresStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		var req syncOHLCVRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			httpserver.Error(c, http.StatusBadRequest, err)
			return
		}

		if len(req.Symbols) == 0 {
			// Fall back to all stocks from DB
			allStocks, err := store.GetAllStocks(ctx)
			if err != nil {
				httpserver.Wrap(c, http.StatusInternalServerError, err, "failed to fetch stocks from DB: ")
				return
			}
			for _, s := range allStocks {
				req.Symbols = append(req.Symbols, s.Symbol)
			}
			logging.Logger.Info().Int("count", len(req.Symbols)).Msg("No symbols provided, fetched all from DB")
		}

		totalCount := 0
		for _, symbol := range req.Symbols {
			logging.Logger.Info().Str("symbol", symbol).Str("start", req.StartDate).Str("end", req.EndDate).Msg("fetching OHLCV")
			ohlcv, err := tc.FetchDailyOHLCV(ctx, symbol, req.StartDate, req.EndDate)
			logging.Logger.Info().Err(err).Str("symbol", symbol).Int("count", len(ohlcv)).Msg("OHLCV fetch result")
			if err != nil {
				logging.Logger.Warn().Err(err).Str("symbol", symbol).Msg("Failed to sync OHLCV")
				continue
			}
			totalCount += len(ohlcv)
		}

		c.JSON(http.StatusOK, gin.H{
			"message": "OHLCV synced successfully",
			"count":   totalCount,
		})
	}
}

type syncFundamentalRequest struct {
	Symbols []string `json:"symbols"`
	Date    string   `json:"date"` // YYYYMMDD — if provided, fetch for that specific date; otherwise fetch recent
}

// syncFundamentalHandler fetches fundamental data for the requested
// symbols on a single date. POST /sync/fundamental
func syncFundamentalHandler(tc *data.TushareClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		var req syncFundamentalRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			httpserver.Error(c, http.StatusBadRequest, err)
			return
		}

		if len(req.Symbols) == 0 {
			httpserver.Fail(c, http.StatusBadRequest, "symbols array is required")
			return
		}
		if req.Date == "" {
			httpserver.Fail(c, http.StatusBadRequest, "date is required")
			return
		}

		totalCount := 0
		for _, symbol := range req.Symbols {
			fundamentals, err := tc.FetchFundamentals(ctx, symbol, req.Date)
			if err != nil {
				logging.Logger.Warn().Err(err).Str("symbol", symbol).Msg("Failed to sync fundamentals")
				continue
			}
			totalCount += len(fundamentals)
		}

		c.JSON(http.StatusOK, gin.H{
			"message": "Fundamentals synced successfully",
			"count":   totalCount,
		})
	}
}

// REV-23(10)：包级 syncFundamentalsHandler（bulk 变体）已删除——路由
// 注册的是 SyncHandler.syncFundamentalsHandler 方法版（sync_handlers.go），
// 这个包级函数是拆分前遗留的参考实现，零调用。

func syncCalendarHandler(tc *data.TushareClient, store *storage.PostgresStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req syncCalendarRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			// Use defaults if no body provided
			req.StartDate = time.Now().AddDate(-1, 0, 0).Format("20060102")
			req.EndDate = time.Now().Format("20060102")
		}

		if req.Exchange == "" {
			req.Exchange = "both"
		}
		if req.StartDate == "" {
			req.StartDate = time.Now().AddDate(-1, 0, 0).Format("20060102")
		}
		if req.EndDate == "" {
			req.EndDate = time.Now().Format("20060102")
		}

		if _, err := time.Parse("20060102", req.StartDate); err != nil {
			httpserver.Fail(c, http.StatusBadRequest, "invalid start_date format, use YYYYMMDD")
			return
		}
		if _, err := time.Parse("20060102", req.EndDate); err != nil {
			httpserver.Fail(c, http.StatusBadRequest, "invalid end_date format, use YYYYMMDD")
			return
		}

		startFormatted := fmt.Sprintf("%s-%s-%s", req.StartDate[:4], req.StartDate[4:6], req.StartDate[6:8])
		endFormatted := fmt.Sprintf("%s-%s-%s", req.EndDate[:4], req.EndDate[4:6], req.EndDate[6:8])

		ctx := c.Request.Context()

		// Determine which exchanges to sync
		exchanges := []string{req.Exchange}
		if req.Exchange == "both" {
			exchanges = []string{"SSE", "SZSE"}
		}

		var allEntries []storage.TradingCalendarEntry
		exchangeResults := make(map[string]struct {
			count    int
			trading  int
			holidays int
		})

		for _, exchange := range exchanges {
			entries, err := tc.FetchTradingCalendar(ctx, exchange, startFormatted, endFormatted)
			if err != nil {
				// REV-23(1)：Tushare 客户端错误可能带 token/URL——客户端拿
				// 静态文案（exchange 指明是哪个市场，不含 cause），原文进日志。
				httpserver.FailCause(c, http.StatusInternalServerError,
					fmt.Sprintf("failed to fetch %s calendar from Tushare", exchange), err)
				return
			}

			tradingCount := 0
			for _, e := range entries {
				if e.IsTradingDay {
					tradingCount++
				}
			}

			exchangeResults[exchange] = struct {
				count    int
				trading  int
				holidays int
			}{
				count:    len(entries),
				trading:  tradingCount,
				holidays: len(entries) - tradingCount,
			}

			allEntries = append(allEntries, entries...)
		}

		if len(allEntries) == 0 {
			c.JSON(http.StatusOK, gin.H{"message": "no calendar entries returned from Tushare", "count": 0})
			return
		}

		// Save all entries to database in one batch
		domainEntries := make([]*storage.TradingCalendarEntry, len(allEntries))
		for i := range allEntries {
			domainEntries[i] = &allEntries[i]
		}
		if err := store.SaveTradingCalendarBatch(ctx, domainEntries); err != nil {
			httpserver.Wrap(c, http.StatusInternalServerError, err, "failed to save calendar: ")
			return
		}

		totalCount := 0
		totalTrading := 0
		for _, r := range exchangeResults {
			totalCount += r.count
			totalTrading += r.trading
		}

		c.JSON(http.StatusOK, gin.H{
			"message":          "calendar synced successfully",
			"count":            totalCount,
			"trading_days":     totalTrading,
			"holidays":         totalCount - totalTrading,
			"start_date":       startFormatted,
			"end_date":         endFormatted,
			"exchanges_synced": exchanges,
			"by_exchange":      exchangeResults,
		})
	}
}

// warmCacheRequest is the POST body for the cache warm endpoint.
type warmCacheRequest struct {
	Symbols   []string `json:"symbols"`
	StartDate string   `json:"start_date"` // YYYYMMDD
	EndDate   string   `json:"end_date"`   // YYYYMMDD
}

// warmCacheHandler pre-fetches OHLCV data for the given stock universe into Redis.
// This is called by the backtest engine before running a backtest to ensure
// all required data is cached and the backtest loop hits Redis instead of PostgreSQL.
func syncDividendsHandler(tc *data.TushareClient, store *storage.PostgresStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		var req syncDividendsRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			// If no body, use empty request (sync all stocks)
			req = syncDividendsRequest{}
		}

		var symbols []string
		if len(req.Symbols) > 0 {
			symbols = req.Symbols
		} else {
			// Fetch all stocks from DB
			allStocks, err := store.GetAllStocks(ctx)
			if err != nil {
				httpserver.Wrap(c, http.StatusInternalServerError, err, "failed to fetch stocks from DB: ")
				return
			}
			for _, s := range allStocks {
				symbols = append(symbols, s.Symbol)
			}
			logging.Logger.Info().Int("count", len(symbols)).Msg("No symbols provided, fetched all from DB for dividend sync")
		}

		if len(symbols) == 0 {
			httpserver.Fail(c, http.StatusBadRequest, "no stocks found in DB. Run POST /sync/stocks first.")
			return
		}

		// 第四份同构实现收敛到 runBatchedSymbols（REV-24）：切批 / 计数 /
		// 取消语义与三个 job executor 共用一份代码，改口径只改一处。
		// 本端点同步执行、无 job 可上报，progress 传 nil。
		res, err := runBatchedSymbols(ctx, symbols, batchSyncSize, nil,
			func(ctx context.Context, symbol string) (int, error) {
				records, err := tc.FetchDividends(ctx, symbol, req.StartDate, req.EndDate)
				if err != nil {
					logging.Logger.Warn().Err(err).Str("symbol", symbol).Msg("Failed to sync dividends")
					return 0, err
				}
				return len(records), nil
			})
		if err != nil {
			// runBatchedSymbols 只在 ctx 结束时返回错误：客户端断开 → 408，
			// 服务端截止 → 504。响应写不出去也不影响语义（连接已断）。
			status := http.StatusRequestTimeout
			if errors.Is(err, context.DeadlineExceeded) {
				status = http.StatusGatewayTimeout
			}
			httpserver.Error(c, status, err)
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"message":       "Dividends synced successfully",
			"stocks_synced": res.StocksSynced,
			"records_saved": res.RecordsSaved,
			"failed_stocks": res.FailedStocks,
		})
	}
}

// ---- Split Sync Handlers ----

type syncSplitsRequest struct {
	Symbols   []string `json:"symbols"`
	StartDate string   `json:"start_date"` // YYYYMMDD
	EndDate   string   `json:"end_date"`   // YYYYMMDD
}

// syncSplitsHandler syncs split/rights-issue data from Tushare for the given symbols (or all stocks if not specified).
// POST /sync/splits
func syncSplitsHandler(tc *data.TushareClient, store *storage.PostgresStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		var req syncSplitsRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			req = syncSplitsRequest{}
		}

		var symbols []string
		if len(req.Symbols) > 0 {
			symbols = req.Symbols
		} else {
			allStocks, err := store.GetAllStocks(ctx)
			if err != nil {
				httpserver.Wrap(c, http.StatusInternalServerError, err, "failed to fetch stocks from DB: ")
				return
			}
			for _, s := range allStocks {
				symbols = append(symbols, s.Symbol)
			}
			logging.Logger.Info().Int("count", len(symbols)).Msg("No symbols provided, fetched all from DB for split sync")
		}

		if len(symbols) == 0 {
			httpserver.Fail(c, http.StatusBadRequest, "no stocks found in DB. Run POST /sync/stocks first.")
			return
		}

		totalRecords := 0
		totalSynced := 0
		totalFailed := 0

		batchSize := 10
		for i := 0; i < len(symbols); i += batchSize {
			end := i + batchSize
			if end > len(symbols) {
				end = len(symbols)
			}
			batch := symbols[i:end]

			for _, symbol := range batch {
				records, err := tc.FetchSplits(ctx, symbol, req.StartDate, req.EndDate)
				if err != nil {
					totalFailed++
					logging.Logger.Warn().Err(err).Str("symbol", symbol).Msg("Failed to sync splits")
					continue
				}
				totalSynced++
				totalRecords += len(records)
			}

			logging.Logger.Info().
				Int("batch", (i/batchSize)+1).
				Int("progress", end).
				Int("total", len(symbols)).
				Msg("Split sync batch complete")
		}

		c.JSON(http.StatusOK, gin.H{
			"message":       "Splits synced successfully",
			"stocks_synced": totalSynced,
			"records_saved": totalRecords,
			"failed_stocks": totalFailed,
		})
	}
}

// ---- Factor Cache Handlers ----

// getFactorHandler retrieves a factor z-score from the cache.
// GET /factors/:factor_name?symbol=...&date=...
