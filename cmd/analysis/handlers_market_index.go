package main

// 市场指数读数（SVC-CORE-DATA-RETIRE 的退役遗留，2026-11-10）。
//
// 背景：`GET /api/market/index` 原先是 cmd/data 的 handler（`marketIndexHandler`
// + pkg/storage 的 LatestIndexBars）。数据面退役时 handler 随 cmd/data 一起
// 删除，而网关仍把它反代到 data-service 的 `/market/index` —— 那条路由在
// data 的兼容面里不存在 ⇒ 404 → 502，ui「市场概览」恒走「读数失败」空态。
// 裁决 ⑰ 明定 `market.index_daily` 的 owner 是 core，所以这条读链由 core
// 本地供数，不再反代（候选 A；B = data 补兼容端点与 ⑰ 相反，未采用）。
//
// 响应形状 / 单位口径照 DATA-07 + DATA-11 已冻的契约，一个字不动：
//   - `{"indices":[{code,name,close,change,pct,volume,amount}]}`
//   - volume 股、amount = turnover（CNY）、pct 是**百分数刻度**（1.23 == +1.23%，
//     展示端不要再乘 100）
//   - 库里没有行的 code 不产条目（不编零值 close）；turnover 为 NULL ⇒
//     `amount: null`；无上一期收盘 ⇒ `change/pct: null`（不是 +0.00%）
//   - symbol 不传 = 服务集三个；给服务集外的 symbol 一律 400（不透传：个股代码
//     不是指数，压成 `[]` 会把「这里不供给」和「这个指数没有日线」混成一件事）
//   - date 只认 `YYYY-MM-DD`（与 ui n-date-picker 的 value-format、SPEC 样例一致），
//     语义是「该日或之前最近一根 bar」，**不是 PIT 快照**
//
// 未接库时路由照常注册、请求 503（deps_test.newMinimalDeps 的约束）。

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"

	"github.com/ruoxizhnya/tellurion-kit/httpserver"
	"tellurion/pkg/storage"
)

// marketIndexServices 是服务集：code → 展示名。顺序即默认响应顺序
// （DATA-07 定下的三个；399001.SZ 深证成指不在其中，ui 下拉已按此收口）。
var marketIndexServices = []struct {
	Code string
	Name string
}{
	{"000300.SH", "沪深300"},
	{"000001.SH", "上证指数"},
	{"399006.SZ", "创业板指"},
}

// marketIndexServiceSet 是上面那个切片的名字集合，用于 400 判定与文案。
var marketIndexServiceSet = func() map[string]string {
	m := make(map[string]string, len(marketIndexServices))
	for _, s := range marketIndexServices {
		m[s.Code] = s.Name
	}
	return m
}()

// indexDailyReader 是本 handler 要的唯一读能力，便于测试注入（生产实现是
// *storage.PostgresStore）。
type indexDailyReader interface {
	LatestIndexBars(ctx context.Context, codes []string, asOf *time.Time) ([]storage.LatestIndexBar, error)
}

// marketIndexEntry 是响应里的一条读数。指针字段 = 可为 null 的列
// （缺失≠0，同 DATA-07 的口径）。
type marketIndexEntry struct {
	Code   string   `json:"code"`
	Name   string   `json:"name"`
	Close  float64  `json:"close"`
	Change *float64 `json:"change"`
	Pct    *float64 `json:"pct"`
	Volume float64  `json:"volume"`
	Amount *float64 `json:"amount"`
}

// marketIndexResponse 是响应的唯一形状。indices 恒为数组：空结果是 200 + []，
// 不是 null（严格客户端会直接炸）。
type marketIndexResponse struct {
	Indices []marketIndexEntry `json:"indices"`
}

// registerMarketIndexRoutes 挂载 GET /api/market/index。reader 可以为 nil
// （未接库），此时路由仍注册、请求 503。
func registerMarketIndexRoutes(router *gin.Engine, reader indexDailyReader, logger zerolog.Logger) {
	h := &marketIndexHandler{reader: reader, logger: logger.With().Str("component", "market-index-handler").Logger()}
	router.GET("/api/market/index", h.handle)
}

type marketIndexHandler struct {
	reader indexDailyReader
	logger zerolog.Logger
}

// parseMarketIndexSelectors 把 query 选择器翻成 (codes, asOf)。坏参数在碰
// 数据库之前就 400（同 handlers_macro.go 的 parse→validate→query 顺序）。
func parseMarketIndexSelectors(symbol, date string) ([]string, *time.Time, error) {
	codes := make([]string, 0, len(marketIndexServices))
	if symbol == "" {
		for _, s := range marketIndexServices {
			codes = append(codes, s.Code)
		}
	} else {
		if _, ok := marketIndexServiceSet[symbol]; !ok {
			return nil, nil, fmt.Errorf(
				"unsupported symbol %q: this endpoint serves index bars only (000300.SH, 000001.SH, 399006.SZ)", symbol)
		}
		codes = append(codes, symbol)
	}
	if date == "" {
		return codes, nil, nil
	}
	// 只认定宽 YYYY-MM-DD：time.Parse 会拒 `2026-9-22` 与带时刻的串，
	// 与 ui 的 value-format / SPEC 样例一致（DATA-11 实测口径）。
	asOf, err := time.Parse("2006-01-02", date)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid date %q: use YYYY-MM-DD", date)
	}
	return codes, &asOf, nil
}

// handle 是 GET /api/market/index 的入口。
func (h *marketIndexHandler) handle(c *gin.Context) {
	if h.reader == nil {
		httpserver.Fail(c, http.StatusServiceUnavailable, "market index data layer unavailable")
		return
	}
	codes, asOf, err := parseMarketIndexSelectors(c.Query("symbol"), c.Query("date"))
	if err != nil {
		httpserver.Fail(c, http.StatusBadRequest, err.Error())
		return
	}
	bars, err := h.reader.LatestIndexBars(c.Request.Context(), codes, asOf)
	if err != nil {
		h.logger.Error().Err(err).Msg("market index read failed")
		httpserver.Fail(c, http.StatusInternalServerError, "market index read failed")
		return
	}
	byCode := make(map[string]storage.LatestIndexBar, len(bars))
	for _, b := range bars {
		byCode[b.Code] = b
	}
	indices := make([]marketIndexEntry, 0, len(bars))
	// 按服务集顺序输出（不按库返回的 code 升序），保证默认三格的排列稳定。
	for _, s := range marketIndexServices {
		b, ok := byCode[s.Code]
		if !ok {
			continue
		}
		e := marketIndexEntry{
			Code:   s.Code,
			Name:   s.Name,
			Close:  b.Close,
			Volume: b.Volume,
			Amount: b.Turnover,
		}
		if b.PrevClose != nil && *b.PrevClose != 0 {
			change := b.Close - *b.PrevClose
			pct := change / *b.PrevClose * 100
			e.Change = &change
			e.Pct = &pct
		}
		indices = append(indices, e)
	}
	c.JSON(http.StatusOK, marketIndexResponse{Indices: indices})
}
