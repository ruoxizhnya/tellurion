package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/spf13/viper"

	"github.com/ruoxizhnya/tellurion-kit/httpserver"
	"tellurion/pkg/bootstrap"
)

// maxBulkOHLCVBodyBytes 是 POST /api/v1/ohlcv/bulk 的请求体上限。一个
// symbol 列表（全市场约 5k 只 × ~12 字节）远低于这个量；设上限是为了不让
// 网关成为无界缓冲。
const maxBulkOHLCVBodyBytes = 1 << 20

// maxIngestPointsBodyBytes 与 data 的投点上限对齐（8 MiB）：网关不额外放宽，
// 也不提前截断——截断由数据服务按自己的口子判 413。
const maxIngestPointsBodyBytes = 8 << 20

// registerProxyRoutes wires the analysis→data forwarding surface. The viper
// instance is the loaded analysis config (not the global one) so that
// data_service.url is actually read — the global viper was never populated
// by loadConfig, which meant the hardcoded docker hostname silently won
// (ODR-062 S-B wiring fix).
func registerProxyRoutes(router *gin.Engine, httpClient *http.Client, v *viper.Viper, logger zerolog.Logger) {
	// CORE-DATA-URL-SINGLE-DEFAULT（2026-11-13）：地址只从 bootstrap.DataServiceURL
	// 来 —— 与回测取数（pkg/bootstrap、kernel_wiring 的行情 provider）共用同一
	// 解析结果。此前这里私有的 `http://data-service:8081` 回落与回测侧的
	// `http://localhost:8081` 是两套默认，缺配置时两族请求会打到不同上游。
	// 生产启动已由 validateOutboundEndpoints 硬校验该键（缺项即拒绝启动），
	// 所以这里解析失败只可能是测试构造的零配置；此时不注册路由（不静默
	// 指向一个猜的地址），并留一条 error 说明。
	dataServiceURL, urlErr := bootstrap.DataServiceURL(v)
	if urlErr != nil {
		logger.Error().Err(urlErr).Msg("data proxy routes not registered: data_service.url unresolved")
		return
	}

	// ODR-062 (S-B): gateway proxy for the L0 sync-job management API
	// (/api/sync/jobs*, /api/sync/workers, /api/sync/schedules*) — the
	// contract documented in SPEC.md:1321-1334, consumed by the SPA
	// (web/src/api/sync.ts) and the e2e data-sync suites. Uses a streaming
	// ReverseProxy rather than proxyRequest because the family includes an
	// SSE endpoint (GET /api/sync/jobs/:id/progress) that must not be
	// buffered.
	target, err := url.Parse(dataServiceURL)
	if err != nil {
		logger.Error().Err(err).Str("url", dataServiceURL).Msg("invalid data_service.url; data proxy routes not registered")
		return
	}
	syncProxy := httputil.NewSingleHostReverseProxy(target)
	syncProxy.FlushInterval = -1 // flush every write immediately — required for SSE
	router.Any("/api/sync/*path", func(c *gin.Context) {
		// REV-10：网关自身的 WriteTimeout（60s）同样是**绝对**写截止，
		// 长作业的 SSE 经这里转发到一半也会被网关掐断。只对 SSE 请求
		//（progress 端点或 Accept: text/event-stream）清掉本连接的写截止；
		// 其余转发请求保持全局超时。
		if isSSERequest(c) {
			if err := http.NewResponseController(c.Writer).SetWriteDeadline(time.Time{}); err != nil {
				logger.Warn().Err(err).Msg("SSE proxy: failed to clear write deadline; stream may be cut by server WriteTimeout")
			}
		}
		syncProxy.ServeHTTP(c.Writer, c.Request)
	})

	proxyRequest := func(c *gin.Context, method, targetURL string, body io.Reader) {
		// REV-23(6)：出站请求统一走 NewRequestWithContext——此前 GET/POST
		// 两个快捷分支不带请求 ctx，客户端断开后出站调用仍在跑；default
		// 分支却带了（同一函数两套标准）。body 非 nil 时按 JSON 发。
		req, reqErr := http.NewRequestWithContext(c.Request.Context(), method, targetURL, body)
		if reqErr != nil {
			httpserver.Fail(c, http.StatusBadGateway, "failed to create proxy request")
			return
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		// 裁-15：数据服务已上鉴权，代理必须把入站凭证原样带给上游——
		// `/api/sync/*` 的 ReverseProxy 本来就透传，这里补齐 proxyRequest 分支。
		// 不重新签发、不降级：没有凭证就不带，让上游按自己的策略回 401/403。
		if auth := c.GetHeader("Authorization"); auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			httpserver.Wrap(c, http.StatusBadGateway, err, "data service unavailable: ")
			return
		}
		defer resp.Body.Close()
		var result map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			httpserver.Fail(c, http.StatusBadGateway, "invalid response from data service")
			return
		}
		c.JSON(resp.StatusCode, result)
	}

	// ADR-0010（2026-11-12）：证据坐标的权威读面在数据服务仓的归档面
	//（`ingest.raw` 的唯一写者 = data）。本进程只注册读反代，不持有第二份
	// 证据存储，也不提供写入（写面直连 data 的 `/api/ingest/raw`）。
	// 用 proxyRequest 而非 ReverseProxy：单条 JSON、无 SSE，且上游 404
	//（未摄取）必须原样透给证据页，不能被网关改写成 502。
	router.GET("/api/evidence/:content_hash", func(c *gin.Context) {
		hash := c.Param("content_hash")
		proxyRequest(c, http.MethodGet, dataServiceURL+"/api/evidence/"+url.PathEscape(hash), nil)
	})

	router.GET("/api/ohlcv/:symbol", func(c *gin.Context) {
		symbol := c.Param("symbol")
		startDate := c.Query("start_date")
		endDate := c.Query("end_date")
		if startDate == "" || endDate == "" {
			httpserver.Fail(c, http.StatusBadRequest, "start_date and end_date required (YYYYMMDD)")
			return
		}
		// REV-23(6)：路径段与 query 都显式编码——此前 fmt.Sprintf 裸拼，
		// symbol/query 里的特殊字符（空格、&、#）会被静默错传；同文件
		// /market/index 却用 url.Values（两套标准）。对齐后者。
		q := url.Values{}
		q.Set("start_date", startDate)
		q.Set("end_date", endDate)
		dataURL := dataServiceURL + "/ohlcv/" + url.PathEscape(symbol) + "?" + q.Encode()
		proxyRequest(c, http.MethodGet, dataURL, nil)
	})

	router.POST("/api/v1/ohlcv/bulk", func(c *gin.Context) {
		// QT-50 ③ 的「行情查询」粗粒度动作：多 symbol × 日期区间在**一次**
		// 请求内取回。刻意不把 agent 引导到上面的 per-symbol 端点 ——
		// D24 禁止 per-symbol 远程调用，一个包含 N 个 symbol 的研究循环
		// 若逐 symbol 打 HTTP，就是 N 次跨进程往返。
		//
		// 转发口径与其它反代一致：只搬这条端点认识的三个键，其余留在这里
		// 让 data-service 对「这条端点认什么」负责。
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBulkOHLCVBodyBytes)
		var req struct {
			Symbols   []string `json:"symbols"`
			StartDate string   `json:"start_date"`
			EndDate   string   `json:"end_date"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			httpserver.Fail(c, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		if len(req.Symbols) == 0 {
			httpserver.Fail(c, http.StatusBadRequest, "symbols is required")
			return
		}
		body, err := json.Marshal(req)
		if err != nil {
			httpserver.Fail(c, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		proxyRequest(c, http.MethodPost, dataServiceURL+"/api/v1/ohlcv/bulk", bytes.NewReader(body))
	})

	// `POST /api/screen` 与 `GET /api/stocks/count` 不再反代（2026-11-12 裁）：
	//   - screen 的分析语义归 core，由 `handlers_screen.go` 本地供数；
	//   - count 由 data 已有读面 `GET /stocks` 派生，不新增 `/stocks/count`。
	// 两者都在本文件的 registerProxyRoutes 之外装配，理由见各自文件头。

	// `POST /api/ingest/raw` 承载的能力已随 `ingest.raw` 退役（ADR-0008 /
	// 本仓 ADR-022 的收口）。data 侧唯一写面是 `POST /ingest/points`，本仓
	// 把它作为网关入口原样转发；**不为旧路径名造第二个写入口**。
	//
	// 保留路由注册（而不是删掉变 gin 404）：让 openapi 的 path 与真实路由
	// 一一对应，旧调用方拿到可判定的 410 + 机器码（与 `/api/factors/:name`
	// 同一处置）。
	router.POST("/api/ingest/raw", func(c *gin.Context) {
		httpserver.FailCode(c, http.StatusGone, "ingest_raw_retired",
			"POST /api/ingest/raw is retired with ingest.raw (ADR-0008); the data service's only write door is POST /api/ingest/points")
	})

	router.POST("/api/ingest/points", func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxIngestPointsBodyBytes)
		req, reqErr := http.NewRequestWithContext(c.Request.Context(), http.MethodPost,
			dataServiceURL+"/ingest/points", c.Request.Body)
		if reqErr != nil {
			httpserver.Fail(c, http.StatusBadGateway, "failed to create proxy request")
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if auth := c.GetHeader("Authorization"); auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			httpserver.Wrap(c, http.StatusBadGateway, err, "data service unavailable: ")
			return
		}
		defer resp.Body.Close()
		var result map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			httpserver.Fail(c, http.StatusBadGateway, "invalid response from data service")
			return
		}
		c.JSON(resp.StatusCode, result)
	})

	// `GET /api/stocks/count`：由 data 的 `GET /stocks` 派生（2026-11-12 裁）。
	// 不新增 `/stocks/count` 端点——count 与 by_status 是同一份票池的聚合，
	// `latest_date` 已由 data 的 `/stocks` 响应带上。读失败回 5xx，不折成 0。
	router.GET("/api/stocks/count", func(c *gin.Context) {
		req, reqErr := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, dataServiceURL+"/stocks", nil)
		if reqErr != nil {
			httpserver.Fail(c, http.StatusBadGateway, "failed to create proxy request")
			return
		}
		if auth := c.GetHeader("Authorization"); auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			httpserver.Wrap(c, http.StatusBadGateway, err, "data service unavailable: ")
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			httpserver.Fail(c, http.StatusBadGateway, "data service /stocks returned "+resp.Status)
			return
		}
		var upstream struct {
			Stocks []struct {
				Status string `json:"status"`
			} `json:"stocks"`
			LatestDate *string `json:"latest_date"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&upstream); err != nil {
			httpserver.Fail(c, http.StatusBadGateway, "invalid response from data service")
			return
		}
		byStatus := make(map[string]int64, 4)
		for _, s := range upstream.Stocks {
			status := s.Status
			if status == "" {
				// 该列可空：NULL/空串折成 unknown 一档（DATA-02 已冻口径）。
				status = "unknown"
			}
			byStatus[status]++
		}
		c.JSON(http.StatusOK, gin.H{
			"count":       len(upstream.Stocks),
			"by_status":   byStatus,
			"latest_date": upstream.LatestDate,
		})
	})

	// C8 / CORE-FACTOR-DEAD-PROXY：这条反代已显式退役。
	//
	// 背景：上游 data `/factors/:name` 早被裁-14 撤掉（`tellurion-data` 原文
	// 「不要重新引入」），继续反代只会把「能力已退役」伪装成上游 404，调用方
	// 无法区分「因子不存在」与「读面不存在」。ADR-037 §5 已把死链记为已知
	// 后果，ADR-041 又裁「不新造因子求值端点」——所以这里不再转发，直接回
	// 可判定的 410 + 机器码。
	//
	// 保留路由注册（而不是删掉变 gin 404）：让 `docs/openapi.yaml` 的 path 与
	// 真实路由继续一一对应，也让旧调用方拿到明确原因。
	router.GET("/api/factors/:factor_name", func(c *gin.Context) {
		httpserver.FailCode(c, http.StatusGone, "factor_http_surface_retired",
			"GET /api/factors/:factor_name is retired: the factor_cache read surface was removed with the data plane (ADR-037 §5), and no factor evaluation endpoint is provided (ADR-041)")
	})

	// ODR-062 (S-C): removed as dead code (zero consumers across web/src,
	// e2e, static pages and Go internals — Go side reaches L0 directly):
	//   POST /api/sync/calendar, POST /sync/calendar (mirror),
	//   GET  /api/v1/trading/calendar.
	// The 4 legacy no-prefix mirrors (/ohlcv/:symbol, /screen, /stocks/count,
	// /market/index) that used to sit here are gone as well — AUD-33
	// (2026-09-22) retired the legacy pages they existed for. They were never
	// anything but a convenience for cmd/analysis/static/*.html; the SPA calls
	// the /api-prefixed routes above (web/src/api/market.ts).
	// deps_test.go asserts these paths stay unregistered.
}

// isSSERequest 判断一个请求是否要按 SSE 长连接对待（REV-10）：
// 用 Accept 头或 progress 端点路径判定，宁可多清一次写截止（影响仅限
// 该连接），不把 SSE 漏进全局超时。
func isSSERequest(c *gin.Context) bool {
	if strings.Contains(c.GetHeader("Accept"), "text/event-stream") {
		return true
	}
	return strings.HasSuffix(c.Request.URL.Path, "/progress")
}
