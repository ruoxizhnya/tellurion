package main

import (
	"fmt"
	"strings"

	"tellurion/pkg/httpserver"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"tellurion/pkg/domain"
	"tellurion/pkg/storage"
)

func healthHandler(store *storage.PostgresStore, cache storage.Cache) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()

		if err := store.Ping(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unhealthy", "error": "database"})
			return
		}
		if err := cache.Ping(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unhealthy", "error": "cache"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"status": "healthy"})
	}
}

func listStocksHandler(store *storage.PostgresStore, cache storage.Cache) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		exchange := c.Query("exchange")

		// Check cache first
		if cached, err := cache.GetCachedStocks(ctx, exchange); err == nil && cached != nil {
			c.JSON(http.StatusOK, gin.H{"stocks": cached, "source": "cache"})
			return
		}

		stocks, err := store.GetStocks(ctx, exchange)
		if err != nil {
			httpserver.Error(c, http.StatusInternalServerError, err)
			return
		}

		// Cache the result
		if len(stocks) > 0 {
			cache.CacheStocks(ctx, exchange, stocks)
		}

		c.JSON(http.StatusOK, gin.H{"stocks": stocks, "source": "database"})
	}
}

func getStockHandler(store *storage.PostgresStore, cache storage.Cache) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		symbol := c.Param("symbol")

		// Check cache first
		if cached, err := cache.GetCachedStock(ctx, symbol); err == nil && cached != nil {
			c.JSON(http.StatusOK, gin.H{"stock": cached, "source": "cache"})
			return
		}

		stock, err := store.GetStock(ctx, symbol)
		if err != nil {
			httpserver.Error(c, http.StatusInternalServerError, err)
			return
		}
		if stock == nil {
			httpserver.Fail(c, http.StatusNotFound, "stock not found")
			return
		}

		// Cache the result
		cache.CacheStock(ctx, stock)

		c.JSON(http.StatusOK, gin.H{"stock": stock, "source": "database"})
	}
}

func stocksCountHandler(store *storage.PostgresStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()

		byStatus, err := store.StockStatusCounts(ctx)
		if err != nil {
			httpserver.Wrap(c, http.StatusInternalServerError, err, "failed to count stocks: ")
			return
		}

		// DATA-03：这个读数原来在此处就地写 SQL，把 date 扫进 `*string`
		// （pgx 收到的目标是 `**string`）并**丢弃** Scan 的 error —— 查询本身成功、
		// 值也回来了，是类型映射不进去，于是 `latest_date` 在任何库里都没出现过，
		// 而 SPEC 与前端类型都声明了它。现在：读失败 ⇒ 500；库里没有行情 ⇒ 显式 null。
		//
		// 用 FailCause 而不是 Wrap：Wrap 在 5xx 上把 message 整个换成通用文案
		// （pkg/httpserver/errors.go:115-124），本 handler 因此会出现三个字节相同的
		// 500 响应体，调用方分不清挂在哪一步 —— 与 DATA-03「别把几种成因压成一个形状」
		// 同一把尺子。FailCause 的文案是写死的静态句，cause 只进日志，不外泄。
		latest, err := store.LatestQuoteDate(ctx)
		if err != nil {
			httpserver.FailCause(c, http.StatusInternalServerError,
				"failed to read latest quote date", err)
			return
		}
		var latestDate *string
		if latest != nil {
			formatted := latest.Format("2006-01-02")
			latestDate = &formatted
		}

		c.JSON(http.StatusOK, stockCountPayload(byStatus, latestDate))
	}
}

// stockCountPayload 把 GROUP BY status 的分组计数摊成 /stocks/count 的响应体（DATA-02），
// 并带上最新行情日期（DATA-03）。
//
// count 由各档**求和**导出，不是第二次 COUNT(*)：同一趟查询、同一个真相，顺带消掉
// 「两次查询之间被并发同步改动，于是各档加起来不等于总数」这个窗口 —— 一个写着
// "含已退市"的总数和一份对不上的拆分，比少一个字段更糟。
//
// nil 折成空 map：Go 里 nil map 序列化成 `null`，而前端按对象读，`null` 会被当成
// "这个字段不存在"而不是"票池是空的"。
//
// latestDate 为 nil 时序列化成**显式 null**，而不是把键删掉：契约声明了这个字段，
// 「库里还没有行情」就该是一个能被当场质问的读数，不是缺席 —— 缺席把「没实现」
// 「读失败」「表是空的」三种情形压成同一个形状，正是 DATA-03 的缺陷。
func stockCountPayload(byStatus map[string]int64, latestDate *string) gin.H {
	out := byStatus
	if out == nil {
		out = map[string]int64{}
	}
	total := int64(0)
	for _, n := range out {
		total += n
	}
	return gin.H{"count": total, "by_status": out, "latest_date": latestDate}
}

// marketIndexCodes 与中文名是「市场概览」的读数面（DATA-07）。
// 名字由 core 给，前端不再各自维护一份映射（同一概念两处真相）。
//
// DATA-11：这个集合同时是 `?symbol=` 的**服务集** —— 界面下拉、SPEC 与护栏
// 都以它为准，别在别处再抄一份代码清单。
var (
	marketIndexCodes = []string{"000300.SH", "000001.SH", "399006.SZ"}
	marketIndexNames = map[string]string{
		"000300.SH": "沪深300",
		"000001.SH": "上证指数",
		"399006.SZ": "创业板指",
	}
)

// isMarketIndexCode 报告 code 是否在服务集内。同一份 marketIndexCodes 同时是
// `?symbol=` 的收窄集（DATA-11）与 `POST /sync/index-daily/:index_code` 的校验集
// （DATA-12）——别在别处再抄一份代码清单。
func isMarketIndexCode(code string) bool {
	for _, c := range marketIndexCodes {
		if c == code {
			return true
		}
	}
	return false
}

// marketIndexDateLayout 是 `?date=` 唯一接受的格式（与 ui 的 n-date-picker
// `value-format="yyyy-MM-dd"` 和 SPEC 已登记的样例一致）。
//
// 不接受 `20240101`：这条端点本来就只认一种格式，两种都能进就得再回答
// 「同时给时听谁的」，那是为不存在的调用方造的分支。`/screen` 用 YYYYMMDD
// 是它自己的历史形状，本单不动（④ 约束：不改既有响应/参数口径）。
const marketIndexDateLayout = "2006-01-02"

// maxScreenLimit 是 fundamentals 筛选的 limit 封顶（REV-23(7)）：对齐
// 同仓先例（alerts cap 1000）。
const maxScreenLimit = 1000

// parseIndexSelectors 把 `?symbol` / `?date` 摊成库侧读数请求（DATA-11 (a1)）。
//
// 返回的第三个值是 400 文案，空串表示参数可用 —— 不是 error：这些句子会原样出现在
// 响应体里（`httpserver.Fail` 的注释：别把**内部**细节拼进去）。symbol 那条例外，
// 回显调用方自己发的那个值（`000001` 与 `000001.SH` 得分得开），与本包
// `httpserver.Error` 在 4xx 上照原样回 err 文案是同一口径；date 那条不回显。
//
// 三条语义：
//   - `symbol` 为空 ⇒ 服务集全部代码（DATA-07 的默认读数面，调用方不变）。
//   - `symbol` 给了但不在服务集 ⇒ 400。**不透传**：指数日线只从 `market.index_daily`
//     （DATA-12）取数、只认服务集内的代码，透传等于把 `600000.SH` 当指数卖出去
//     （`name` 还会是空串），而「你问的这个这里不供给」与「这个指数没有日线」
//     是两件事，压成空数组就分不开了。
//   - `date` 为空 ⇒ 最新一期；给了 ⇒ 该日或之前最近一期。⚠️ 这是「最近一根 bar」，
//     **不是 PIT 快照**（无摄取时刻/公告日过滤），SPEC §1 同口径写明。
func parseIndexSelectors(symbol, date string) (codes []string, asOf *time.Time, paramError string) {
	if symbol != "" {
		found := false
		for _, code := range marketIndexCodes {
			if code == symbol {
				found = true
				break
			}
		}
		if !found {
			return nil, nil, fmt.Sprintf(
				"unsupported symbol %q: this endpoint serves index bars only (%s)",
				symbol, strings.Join(marketIndexCodes, ", "))
		}
		codes = []string{symbol}
	} else {
		codes = marketIndexCodes
	}

	if date != "" {
		t, err := time.Parse(marketIndexDateLayout, date)
		if err != nil {
			// 文案不带用户给的值：上面已经点名了它不合法，格式说清楚就够。
			return nil, nil, "invalid date format, use YYYY-MM-DD"
		}
		// 这个值必须是 **UTC 的零点**：pgx 把 time.Time 编码成 date 时取的是
		// 该值**自己时区**的 Year/Month/Day（pgtype/date.go 的 encode 腿），不做
		// 任何换算。time.Parse 用只含日期的 layout 解析出来的正是 UTC 零点，
		// 所以宿主时区是 +2 还是 -5 都落在同一天；换成带偏移的时刻就会偏一天，
		// 而偏一天这种错在界面上完全看不出来（读数照样像数）。
		// 上面那条测试断言连时刻带时区一起核，就是钉这件事。
		asOf = &t
	}
	return codes, asOf, ""
}

func marketIndexHandler(store *storage.PostgresStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()

		// DATA-11 (a1)：`?symbol` / `?date` 原来**根本没人读**，handler 恒遍历
		// marketIndexCodes ⇒ ui 的选指数下拉与日期选择器是装饰件（控件存在但不影响
		// 读数）。现在两者都下传：symbol 收窄到服务集内的那一个代码，
		// date 走 as-of 取该日或之前最近一期。
		codes, asOf, paramError := parseIndexSelectors(c.Query("symbol"), c.Query("date"))
		if paramError != "" {
			httpserver.Fail(c, http.StatusBadRequest, paramError)
			return
		}

		// DATA-07：原形状是每个代码两趟查询（最新收盘 + 上一期收盘），且两趟的
		// error 都被丢进 `if err == nil` 的分支里 —— 读失败与「这个指数没数据」
		// 同样表现为「这一条不出现」。现在一趟取回，读失败就是 500。
		//
		// DATA-07 当时库里这三个代码 0 行（工单 ② 实测），响应只能是诚实的
		// `{"indices":[]}`；指数日线已由 **DATA-12** 落到独立表 `market.index_daily`
		// 并经 `POST /sync/index-daily/:index_code` 灌数，故现在同步过的库里这里
		// 会回真条目。没同步过时仍是诚实的空数组。
		bars, err := store.LatestIndexBars(ctx, codes, asOf)
		if err != nil {
			// FailCause 而非 Wrap：见上面 stocksCountHandler 里同一条注释。
			httpserver.FailCause(c, http.StatusInternalServerError,
				"failed to read index bars", err)
			return
		}

		byCode := make(map[string]storage.MarketIndexBar, len(bars))
		for _, bar := range bars {
			byCode[bar.Code] = bar
		}
		ordered := make([]storage.MarketIndexBar, 0, len(bars))
		for _, code := range codes {
			if bar, ok := byCode[code]; ok {
				ordered = append(ordered, bar)
			}
		}

		c.JSON(http.StatusOK, marketIndexPayload(ordered))
	}
}

// marketIndexPayload 把库侧读数摊成 /market/index 的响应（DATA-07）。
//
// 单位一律原样透出：volume 是股（DATA-04）、amount 是 CNY（DATA-05），
// 换算（亿 / 万）是展示层的事，在这里除一次就有了第二处真相。
//
// change/pct 在没有上一期收盘时是 null 而不是 0 —— 把「无从算起」显示成
// 「+0.00%」是假数字；amount 在 turnover 为 NULL 时同理。
//
// pct 是**百分数刻度**（1.23 表示 +1.23%），沿用该字段改动前的语义，
// 展示端不要再乘一次 100。
func marketIndexPayload(bars []storage.MarketIndexBar) gin.H {
	entries := make([]gin.H, 0, len(bars))
	for _, bar := range bars {
		var change, pct *float64
		if bar.PrevClose != nil && *bar.PrevClose > 0 {
			delta := bar.Close - *bar.PrevClose
			change = &delta
			rate := delta / *bar.PrevClose * 100
			pct = &rate
		}
		entries = append(entries, gin.H{
			"code":   bar.Code,
			"name":   marketIndexNames[bar.Code],
			"close":  bar.Close,
			"change": change,
			"pct":    pct,
			"volume": bar.Volume,
			"amount": bar.Turnover,
		})
	}
	return gin.H{"indices": entries}
}
func screenStocksHandler(store *storage.PostgresStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		var req domain.ScreenRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			httpserver.Error(c, http.StatusBadRequest, err)
			return
		}

		var date *time.Time
		if req.Date != "" {
			t, err := time.Parse("20060102", req.Date)
			if err != nil {
				httpserver.Fail(c, http.StatusBadRequest, "invalid date format, use YYYYMMDD")
				return
			}
			date = &t
		}

		limit := req.Limit
		if limit <= 0 {
			limit = 100
		}
		// REV-23(7)：封顶对齐同仓先例（alerts cap 1000）——筛选条件全开
		// 时全表扫描的行数不该由调用方说了算。
		if limit > maxScreenLimit {
			limit = maxScreenLimit
		}

		results, err := store.ScreenFundamentals(ctx, req.Filters, date, limit)
		if err != nil {
			httpserver.Error(c, http.StatusInternalServerError, err)
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"count":   len(results),
			"results": results,
		})
	}
}

// ---- End Fundamental Data Handlers ----

func getTradingCalendarHandler(store *storage.PostgresStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		startStr := c.Query("start")
		endStr := c.Query("end")

		if startStr == "" || endStr == "" {
			httpserver.Fail(c, http.StatusBadRequest, "start and end query params required")
			return
		}

		startDate, err := time.Parse("2006-01-02", startStr)
		if err != nil {
			httpserver.Fail(c, http.StatusBadRequest, "invalid start date format, use YYYY-MM-DD")
			return
		}
		endDate, err := time.Parse("2006-01-02", endStr)
		if err != nil {
			httpserver.Fail(c, http.StatusBadRequest, "invalid end date format, use YYYY-MM-DD")
			return
		}

		days, err := store.GetTradingDates(ctx, startDate, endDate)
		if err != nil {
			httpserver.Error(c, http.StatusInternalServerError, err)
			return
		}

		dayStrs := make([]string, len(days))
		for i, d := range days {
			dayStrs[i] = d.Format("2006-01-02")
		}
		c.JSON(http.StatusOK, gin.H{"trading_days": dayStrs})
	}
}

type syncCalendarRequest struct {
	StartDate string `json:"start_date"`
	EndDate   string `json:"end_date"`
	Exchange  string `json:"exchange"` // "SSE", "SZSE", or "both" (default: "both")
}
