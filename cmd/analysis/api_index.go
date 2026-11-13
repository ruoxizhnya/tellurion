package main

// api_index.go —— CORE-API-V1-INDEX（2026-11-13 五角审查档 3-3）。
//
// `/api/v1` 旧实现只把 5 条「兼容别名」写死成 []string，机器读它得到的
// 自描述既不完整（`/api/backtest`、`/api/sync/*`、四族 AI 反代全缺），
// 也区分不出哪些是 deprecated 的裸路径。这里改为**从路由注册表生成**：
// 索引与 `router.Routes()` 同源，天然不会广告不存在的路径，新增一族路由
// 不需要再回来补一行。
//
// 分段：
//   - endpoints：现代 API（method + path + 一句话）；
//   - legacy：`registerBacktestLegacyRedirects` 等兼容别名，逐条
//     `deprecated: true`。
//
// 描述按「精确 method+path → 前缀族 → 兜底」三级查找；任一路由都不会
// 因为没写描述而消失。`api_index_test.go` 锁三件事：四类主面都在、
// 索引 ⊆ 实际路由、删一条主面注册即红。

import (
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
)

// apiIndexEntry 是索引里的一条。JSON 字段名保持稳定，UI / agent 可机读。
type apiIndexEntry struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Description string `json:"description"`
	Deprecated  bool   `json:"deprecated"`
}

// apiIndexDescByRoute 是主面的精确描述。key 形如 "POST /api/backtest"。
// 未列出的路由走 apiIndexDescByPrefix，不会因为漏写而消失。
var apiIndexDescByRoute = map[string]string{
	"GET /api/v1":                                       "服务索引（本响应，由路由注册表生成）",
	"GET /health":                                       "存活探针（只看进程活着）",
	"GET /api/health":                                   "存活探针（/api 版本，容器 HEALTHCHECK 用）",
	"GET /readyz":                                       "就绪探针（库连通 + 启动建表到位）",
	"GET /api/readyz":                                   "就绪探针（/api 版本）",
	"GET /metrics":                                      "Prometheus 指标",
	"GET /api/openapi.yaml":                             "OpenAPI 3.0 契约原文",
	"GET /api/docs":                                     "Swagger UI",
	"GET /api/backtest":                                 "列出回测运行（结果面 ∪ 编排任务面）",
	"POST /api/backtest":                                "提交回测或异步任务（同步/异步按请求体判别）",
	"GET /api/backtest/:id":                             "单次运行状态",
	"GET /api/backtest/:id/report":                      "运行报告",
	"GET /api/backtest/:id/trades":                      "成交流水",
	"GET /api/backtest/:id/equity":                      "权益曲线",
	"GET /api/backtest/:id/export/:format":              "导出运行报告",
	"GET /api/backtest/compare":                         "多运行对比",
	"GET /api/backtest/:id/provenance":                  "运行血缘（三 hash ⇄ 结果）",
	"POST /api/batch":                                   "提交批量回测",
	"GET /api/batch/:batch_id":                          "批量回测结果",
	"GET /api/batch/:batch_id/export/:format":           "导出批量回测结果",
	"POST /api/walkforward":                             "前推（walk-forward）分析",
	"POST /api/screen":                                  "选股（本地基本面）",
	"GET /api/market/index":                             "市场指数读数（core 本地供数）",
	"GET /api/fundamental/:symbol":                      "个股基本面（core 本地表）",
	"GET /api/industry/tree":                            "行业树",
	"GET /api/industry/edges":                           "行业关系边",
	"GET /api/industry/vitality":                        "行业活力",
	"GET /api/industry/etf/:etf_code/constituents":      "ETF 成分",
	"GET /api/macro/series":                             "宏观时间序列",
	"GET /api/strategies":                               "策略列表",
	"GET /api/strategies/:id":                           "单个策略详情",
	"GET /api/signals":                                  "信号列表",
	"POST /api/signals":                                 "提交信号（需 trader 角色）",
	"GET /api/ohlcv/:symbol":                            "日线行情（反代 data-service）",
	"POST /api/v1/ohlcv/bulk":                           "批量行情（反代 data-service）",
	"GET /api/stocks/count":                             "股票数量（反代 data-service）",
	"GET /api/factors/:factor_name":                     "因子值（反代 data-service）",
	"GET /api/evidence/:content_hash":                   "证据文件（反代 data-service）",
	"POST /api/ingest/raw":                              "原始数据摄取（反代 data-service）",
	"POST /api/ingest/points":                           "数据点摄取（反代 data-service）",
	"GET /api/listing-windows/status":                   "上市窗口种子状态（取数不可用时如实报 degraded）",
	"GET /api/review/pending":                           "待审清单",
	"POST /api/expression/validate":                     "表达式静态校验",
	"GET /api/expression/catalog":                       "算子 / 字段目录",
	"GET /api/factor-analytics/factors":                 "因子分析目录",
	"GET /api/factor-analytics/factors/:factor/returns": "因子分位收益",
	"GET /api/factor-analytics/factors/:factor/ic":      "因子 IC",
	"POST /api/factor-analytics/compute":                "触发因子分析计算",
	"POST /api/factor/registrations":                    "注册因子",
	"GET /api/plugins":                                  "插件列表",
	"GET /api/plugins/active":                           "已加载插件",
	"GET /api/plugins/:name":                            "单个插件详情",
	"GET /api/plugins/watch-dir":                        "插件热载目录",
	"POST /api/plugins/load":                            "加载插件",
	"POST /api/plugins/:name/unload":                    "卸载插件",
	"POST /api/plugins/:name/reload":                    "重载插件",
	"POST /api/plugins/reload":                          "全量重载",
	"POST /api/plugins/load-all":                        "加载全部插件",
	"POST /api/plugins/watch-dir":                       "启动热载监听",
	"GET /api/datasource/status":                        "数据源状态",
	"GET /api/datasource/health":                        "数据源健康",
	"ANY /api/sync/*path":                               "同步任务队列（反代 data-service）",
	"ANY /api/copilot/*path":                            "AI 副驾（反代 ai-service）",
	"ANY /api/pipeline/*path":                           "AI 管线（反代 ai-service）",
	"ANY /api/explore/*path":                            "AI 探索循环（反代 ai-service）",
	"ANY /api/tools/*path":                              "AI 工具面（反代 ai-service）",
}

// apiIndexPrefixDesc 是长尾族的整族描述（按前缀从长到短匹配）。
var apiIndexPrefixDesc = []struct{ prefix, desc string }{
	{"/api/backtest", "回测运行与结果读面"},
	{"/api/batch", "批量回测"},
	{"/api/walkforward", "前推分析"},
	{"/api/execution", "交易执行（broker 通道）"},
	{"/api/compliance", "投资者适当性"},
	{"/api/reconciliation", "对账"},
	{"/api/alerts", "告警"},
	{"/api/risk", "风险计算"},
	{"/api/stock-state", "个股状态"},
	{"/api/runs", "运行产物读面"},
	{"/api/auth", "认证与用户管理"},
	{"/api/factor-analytics", "因子分析"},
	{"/api/factor", "因子登记"},
	{"/api/plugins", "插件管理"},
	{"/api/industry", "行业"},
	{"/api/macro", "宏观"},
	{"/api/strategies", "策略"},
	{"/api/signals", "信号"},
	{"/api/sync", "同步任务队列（反代 data-service）"},
	{"/api/copilot", "AI 副驾（反代 ai-service）"},
	{"/api/pipeline", "AI 管线（反代 ai-service）"},
	{"/api/explore", "AI 探索循环（反代 ai-service）"},
	{"/api/tools", "AI 工具面（反代 ai-service）"},
}

// apiIndexLegacyPaths 是兼容别名的路径集合：它们真实注册、真实可用，
// 但语义上已由 `/api/...` 取代（registerBacktestLegacyRedirects 内部
// 重写到 /api/backtest；risk / execution 的根路径同理）。
var apiIndexLegacyPaths = map[string]bool{
	"/backtest":            true,
	"/backtest/:id":        true,
	"/backtest/:id/report": true,
	"/backtest/:id/trades": true,
	"/backtest/:id/equity": true,
	"/orders":              true,
	"/orders/:id":          true,
	"/orders/:id/cancel":   true,
	"/positions":           true,
	"/account":             true,
	"/calculate_position":  true,
	"/detect_regime":       true,
	"/check_stoploss":      true,
	"/risk_metrics":        true,
}

// buildAPIV1Index 从已注册路由生成两段索引。ANY 族（gin 的 Any 会展开成
// 9 个方法）合并成一条 "ANY"，避免索引里同一路径刷屏。
func buildAPIV1Index(routes gin.RoutesInfo) (modern, legacy []apiIndexEntry) {
	methodsByPath := map[string]map[string]bool{}
	var paths []string
	for _, r := range routes {
		if _, ok := methodsByPath[r.Path]; !ok {
			methodsByPath[r.Path] = map[string]bool{}
			paths = append(paths, r.Path)
		}
		methodsByPath[r.Path][r.Method] = true
	}

	for _, path := range paths {
		methods := methodsByPath[path]
		entries := make([]apiIndexEntry, 0, len(methods))
		if len(methods) >= 5 {
			entries = append(entries, apiIndexEntry{Method: "ANY", Path: path})
		} else {
			for m := range methods {
				entries = append(entries, apiIndexEntry{Method: m, Path: path})
			}
		}
		for _, e := range entries {
			e.Description = apiIndexDescription(e.Method, e.Path)
			e.Deprecated = apiIndexLegacyPaths[e.Path]
			if e.Deprecated {
				e.Description = "deprecated 兼容别名 → " + e.Description
				legacy = append(legacy, e)
				continue
			}
			modern = append(modern, e)
		}
	}

	less := func(list []apiIndexEntry) {
		sort.Slice(list, func(i, j int) bool {
			if list[i].Path != list[j].Path {
				return list[i].Path < list[j].Path
			}
			return list[i].Method < list[j].Method
		})
	}
	less(modern)
	less(legacy)
	return modern, legacy
}

func apiIndexDescription(method, path string) string {
	if d, ok := apiIndexDescByRoute[method+" "+path]; ok {
		return d
	}
	for _, p := range apiIndexPrefixDesc {
		if strings.HasPrefix(path, p.prefix) {
			return p.desc
		}
	}
	return "端点（详见 /api/openapi.yaml）"
}

// apiV1IndexHandler 每次请求现算：索引永远等于此刻的路由表，
// 不存在「启动时快照 vs 后注册路由」的漂移。
func apiV1IndexHandler(router *gin.Engine) gin.HandlerFunc {
	return func(c *gin.Context) {
		modern, legacy := buildAPIV1Index(router.Routes())
		if legacy == nil {
			legacy = []apiIndexEntry{}
		}
		c.JSON(http.StatusOK, gin.H{
			"service":   "analysis-service",
			"version":   "1.0.0",
			"source":    "router.Routes()",
			"endpoints": modern,
			"legacy":    legacy,
		})
	}
}
