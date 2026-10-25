package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/spf13/viper"

	"tellurion/pkg/httpserver"
)

// maxBulkOHLCVBodyBytes 是 POST /api/v1/ohlcv/bulk 的请求体上限。一个
// symbol 列表（全市场约 5k 只 × ~12 字节）远低于这个量；设上限是为了不让
// 网关成为无界缓冲。
const maxBulkOHLCVBodyBytes = 1 << 20

// registerProxyRoutes wires the analysis→data forwarding surface. The viper
// instance is the loaded analysis config (not the global one) so that
// data_service.url is actually read — the global viper was never populated
// by loadConfig, which meant the hardcoded docker hostname silently won
// (ODR-062 S-B wiring fix).
func registerProxyRoutes(router *gin.Engine, httpClient *http.Client, v *viper.Viper, logger zerolog.Logger) {
	dataServiceURL := v.GetString("data_service.url")
	if dataServiceURL == "" {
		dataServiceURL = "http://data-service:8081"
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

	router.POST("/api/screen", func(c *gin.Context) {
		var reqBody map[string]interface{}
		if err := json.NewDecoder(c.Request.Body).Decode(&reqBody); err != nil {
			httpserver.Fail(c, http.StatusBadRequest, "invalid request body")
			return
		}
		bodyBytes, _ := json.Marshal(reqBody)
		proxyRequest(c, http.MethodPost, dataServiceURL+"/screen", bytes.NewReader(bodyBytes))
	})

	router.GET("/api/stocks/count", func(c *gin.Context) {
		proxyRequest(c, http.MethodGet, dataServiceURL+"/stocks/count", nil)
	})

	// DATA-11: /market/index 的两个选择器参数原来在这里就被丢了 —— 目标 URL 是
	// 拼出来的常量，不带 query string，所以 SPA 发的 ?symbol=&date= 永远到不了
	// 数据服务（登记工单 ② 只说了 handler 不读参数，实际拦在外层：网关改完
	// 数据服务才会收到）。转发口径与下面的 /api/factors/:name 一致：只搬这两个
	// 已知选择器，其余参数留在这里，让 L0 继续对「这条端点认什么」负责。
	router.GET("/api/market/index", func(c *gin.Context) {
		query := url.Values{}
		if symbol := c.Query("symbol"); symbol != "" {
			query.Set("symbol", symbol)
		}
		if date := c.Query("date"); date != "" {
			query.Set("date", date)
		}
		dataURL := dataServiceURL + "/market/index"
		if encoded := query.Encode(); encoded != "" {
			dataURL += "?" + encoded
		}
		proxyRequest(c, http.MethodGet, dataURL, nil)
	})

	// TASKS.md P5-3: the citation-coordinate read. GET /api/factors/:name is
	// the factor_cache read whose rows carry the ADR-022 §5 five-tuple
	// {source, dataset, key, as_of, content_hash} (cmd/data/handlers_factor.go).
	// The L0 route has no /api prefix (/factors/:factor_name, cmd/data/main.go),
	// so this facade both supplies the prefix the SPA uses and forwards
	// symbol/date verbatim. Status is passed through unchanged because 404 is
	// the first-class "no such factor_cache row" answer, and a hash-only
	// citation (response never archived) must reach the SPA intact rather than
	// being rewritten into an error here.
	router.GET("/api/factors/:factor_name", func(c *gin.Context) {
		query := url.Values{}
		if symbol := c.Query("symbol"); symbol != "" {
			query.Set("symbol", symbol)
		}
		if date := c.Query("date"); date != "" {
			query.Set("date", date)
		}
		dataURL := fmt.Sprintf("%s/factors/%s", dataServiceURL, url.PathEscape(c.Param("factor_name")))
		if encoded := query.Encode(); encoded != "" {
			dataURL += "?" + encoded
		}
		proxyRequest(c, http.MethodGet, dataURL, nil)
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
