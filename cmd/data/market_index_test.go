package main

// DATA-07 (a2)：`GET /market/index` 的响应从来没有 volume/amount —— 缺的是**通道**，
// 不是数据（ui 那两格因此恒 '-'）。DATA-07 当时能借的 `volume`（股）/`turnover`（CNY）
// 在 `ohlcv_daily_qfq` 里，而三个指数代码在那张表 0 行；**DATA-12** 之后指数日线走
// 独立表 `market.index_daily`（同一单位口径），本文件的通道腿与单位口径不变。
//
// 本文件钉五层，各自守什么写清（DATA-02 自审的教训：护栏不靠叙述自证）：
//   1. `marketIndexPayload` 纯映射：键名、**单位原样透出**（不 ×100 / 不 ÷1e8，
//      DATA-04/05 已把库里口径定成股 / CNY，换算一次就有两处真相）、
//      NULL turnover 读成 null 而不是 0、无 prev close 时 change/pct 为 null
//      （「缺失不等于 0%」——把无从算起的涨跌幅显示成 +0.00% 是假数字）。
//   2. HTTP 面通道：请求真的把 store 的读数摊出去，且库里没有指数行时如实回
//      `{"indices":[]}` —— 不编造条目（工单 ② 实测：三个指数代码 0 行）。
//      DATA-12 灌数后同一条腿改钉「有行时条目带齐 volume/amount 七键」。
//   3. 读失败要响：库侧失败 ⇒ 500，不是回一份缺字段的 200。
//   4. （DATA-11 (a1)）参数分派：`?symbol` 收窄到服务集内的那一个代码、
//      服务集外 ⇒ 400，`?date` 下传成 as-of、格式不认 ⇒ 400。
//      `TestParseIndexSelectors` 钉分派，`Test*_FailsLoud` 钉 HTTP 面那两声 400。
//   5. （DATA-11）条目 ⊆ 点名的那个代码：`TestMarketIndexHandler_SymbolNarrowsEntries`
//      该库同步过指数日线时有牙口（点单代码最多回那一条），未同步过时循环不执行。
//      这条与 4/2 合起来才等于工单 ⑤ 那句「只回那一条」，本文件没有任何一条腿
//      单独声称它守得住 —— 合成证据在隔离库的 curl 两腿上（见工单完成记录）。
//
// 端到端「响应 == 库逐字段」的真值腿在隔离库上做（一次性 PG 5433 + 自建指数行 +
// curl），因为它要的是**指数代码**有行，而共享开发库里那些代码 0 行，插进去会
// 让并发跑的因子/票池读数把指数当股票看见。库侧逐字段腿见
// pkg/storage/market_readout_test.go。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/storage"
)

func TestMarketIndexPayload_PassesUnitsThroughAndKeepsNull(t *testing.T) {
	t.Parallel()

	amount := 375461676.1428
	bars := []storage.MarketIndexBar{
		{Code: "000300.SH", Close: 4439.1443, Volume: 151965787, Turnover: &amount, PrevClose: ptrF(4400.5)},
		{Code: "000001.SH", Close: 3000, Volume: 200000000, Turnover: nil, PrevClose: nil},
	}

	raw, err := json.Marshal(marketIndexPayload(bars))
	require.NoError(t, err)

	var body struct {
		Indices []struct {
			Code   string   `json:"code"`
			Name   string   `json:"name"`
			Close  float64  `json:"close"`
			Change *float64 `json:"change"`
			Pct    *float64 `json:"pct"`
			Volume float64  `json:"volume"`
			Amount *float64 `json:"amount"`
		} `json:"indices"`
	}
	require.NoError(t, json.Unmarshal(raw, &body), string(raw))
	require.Len(t, body.Indices, 2)

	first := body.Indices[0]
	assert.Equal(t, "000300.SH", first.Code)
	assert.Equal(t, "沪深300", first.Name, "代码→中文名由 core 给，前端不再各自维护一份")
	assert.InDelta(t, 4439.1443, first.Close, 1e-9)
	// 单位原样：库里的股 / CNY 直接是响应的 volume / amount。
	assert.InDelta(t, 151965787, first.Volume, 1e-6,
		"volume 必须原样透出（股）；除以 1e8 或乘 100 都是第二处换算，DATA-04/05 已定口径")
	require.NotNil(t, first.Amount, "turnover 有值时 amount 必须给出去（这正是 DATA-07 缺的通道）")
	assert.InDelta(t, amount, *first.Amount, 1e-6,
		"amount 必须原样透出 CNY；换算成亿元是展示层的事（ui format.ts 的 fmtAmount 自己除 1e8）")
	require.NotNil(t, first.Change)
	assert.InDelta(t, 38.6443, *first.Change, 1e-6)
	require.NotNil(t, first.Pct)
	assert.InDelta(t, (4439.1443-4400.5)/4400.5*100, *first.Pct, 1e-9)

	second := body.Indices[1]
	assert.Nil(t, second.Amount,
		"turnover 为 NULL 时 amount 必须是 null，不能读成 0 —— 0 会被界面显示成「成交额 0 元」这个假数字")
	assert.Nil(t, second.Change, "没有上一期收盘时涨跌幅无从算起，必须是 null 而不是 0（+0.00% 是假数字）")
	assert.Nil(t, second.Pct)
	assert.InDelta(t, 200000000, second.Volume, 1e-6)
}

// TestMarketIndexHandler_ChannelAndHonestEmpty 走 HTTP 面：通道在（`indices` 恒为数组、
// 每条读数带齐七键），库里没有指数行时如实回空数组（工单 ② 的实测形状：`{"indices":[]}`）；
// DATA-12 灌数后同一组断言改在非空条目上执行（见下面循环）。
func TestMarketIndexHandler_ChannelAndHonestEmpty(t *testing.T) {
	store := stocksCountTestStore(t)
	t.Cleanup(store.Close)

	router := gin.New()
	router.GET("/market/index", marketIndexHandler(store))

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/market/index", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var body struct {
		Indices []map[string]json.RawMessage `json:"indices"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())

	// 键恒在，且 indices 为数组不是 null（ui api-health.spec.ts 也断 Array）。
	require.NotNil(t, body.Indices, "indices 必须序列化成数组（空也是 []，不是 null）")

	// DATA-12 前这三个指数代码 0 行 ⇒ 空数组是**诚实读数**，不是缺陷；
	// 灌数后条目必须带 volume/amount 这两个键 —— 本循环两种情况都钉：
	// 空集不执行（诚实空），非空逐条核通道。
	for _, entry := range body.Indices {
		for _, key := range []string{"code", "name", "close", "change", "pct", "volume", "amount"} {
			_, ok := entry[key]
			assert.True(t, ok, "每个指数读数必须带 %q 键（DATA-07 的通道）；实际条目：%v", key, entry)
		}
	}
}

// TestMarketIndexHandler_ReadFailureFailsLoud 钉失败面：库侧读失败 ⇒ 500，
// 不回一份「indices 缺席所以界面空白」的 200。
//
// ⚠️ 分工：本条守的是「handler 不把库侧 error 折成空数组」，以及「失败响应点名哪一步」。
// 它抓不到单位换算错误（那是纯映射腿的事），也抓不到「prev close 查询单独失败」
// （同一条 SQL 一趟取回，结构上不存在半途状态）。
func TestMarketIndexHandler_ReadFailureFailsLoud(t *testing.T) {
	store := stocksCountTestStore(t)

	router := gin.New()
	router.GET("/market/index", marketIndexHandler(store))

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/market/index", nil).WithContext(cancelled)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code,
		"读失败必须回 500；回 200 加空数组就是把「读失败」显示成「今天没有行情」；实际 body：%s", w.Body.String())

	// 光有 500 还不够：`Wrap` 在 5xx 上会把文案整个换成通用句
	// （pkg/httpserver/errors.go:115-124），那样这条端点与 /stocks/count 的失败响应
	// 就是同一个字节串，调用方分不清挂在哪一步 —— 与本单「几种成因不许压成同一个形状」
	// 同一把尺子。所以 handler 走 FailCause，本条钉文案里有这一步的名字。
	assert.Contains(t, w.Body.String(), "failed to read index bars",
		"失败响应要点名是哪一步（SPEC §1 与 openapi 的 '500' 都登记了这条）；实际 body：%s", w.Body.String())
	assert.NotContains(t, w.Body.String(), "indices",
		"失败响应不得带半份成功数据（哪怕是空数组）；实际 body：%s", w.Body.String())
}

func ptrF(v float64) *float64 { return &v }

// ---- DATA-11 (a1)：`?symbol` / `?date` 必须真的下传 ----

// TestParseIndexSelectors 钉参数分派本身。这是「传 symbol=000001.SH 只回那一条」
// 的前一半（后一半是库侧过滤腿 `pkg/storage` 的 TestLatestIndexBars_OnlyRequestedCodesAreRead，
// 两边合起来才等于端到端；HTTP 面那格在共享库里是空读数，见下面的诚实边界）。
//
// 每条断言都问「参数有没有改变传给库的东西」，不问「响应长什么样」——
// 后者在指数 0 行的库里恒等，等于什么都没钉。
func TestParseIndexSelectors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		symbol          string
		date            string
		wantCodes       []string
		wantAsOf        string // "" = nil；否则 YYYY-MM-DD
		wantParamErrHas string // "" = 必须无错
	}{
		{
			name:      "两个都不给 = DATA-07 的默认读数面",
			wantCodes: []string{"000300.SH", "000001.SH", "399006.SZ"},
		},
		{
			name:      "点一个代码就只收窄到那一个",
			symbol:    "000001.SH",
			wantCodes: []string{"000001.SH"},
		},
		{
			name:      "服务集里的第三个同样单给",
			symbol:    "399006.SZ",
			wantCodes: []string{"399006.SZ"},
		},
		{
			name:      "日期下传成 as-of",
			date:      "2026-09-22",
			wantCodes: []string{"000300.SH", "000001.SH", "399006.SZ"},
			wantAsOf:  "2026-09-22",
		},
		{
			name:      "symbol 与 date 同时给",
			symbol:    "000300.SH",
			date:      "2024-01-01",
			wantCodes: []string{"000300.SH"},
			wantAsOf:  "2024-01-01",
		},
		{
			// 这一条是「不许透传」的钉子：600000.SH 在 ohlcv_daily_qfq 里**有行**，
			// 透传就等于把一只股票当指数卖出去（name 还会是空串）。
			name:            "个股代码：拒，不透传",
			symbol:          "600000.SH",
			wantParamErrHas: "unsupported symbol",
		},
		{
			// ui 下拉里原来挂着它，而 core 从来不供给（工单 ②）—— 本条把它钉成
			// 明确拒绝，而不是「回空数组，界面显示成今天没有行情」。
			name:            "深证成指：服务集外，明确拒",
			symbol:          "399001.SZ",
			wantParamErrHas: "unsupported symbol",
		},
		{
			name:            "少了交易所后缀也算拒",
			symbol:          "000001",
			wantParamErrHas: "unsupported symbol",
		},
		{
			name:            "日期格式错：拒，不静默当成没给",
			symbol:          "000001.SH",
			date:            "20240101",
			wantParamErrHas: "invalid date format",
		},
		{
			name:            "月/日单数字也拒（Go 的 2006-01-02 是定宽，不放水）",
			date:            "2026-9-22",
			wantParamErrHas: "invalid date format",
		},
		{
			name:            "带时刻后缀也拒（extra text 不是「近似可用」）",
			date:            "2026-09-22T00:00:00Z",
			wantParamErrHas: "invalid date format",
		},
		{
			name:            "日期是垃圾：拒",
			date:            "昨天",
			wantParamErrHas: "invalid date format",
		},
		{
			name:            "两个都错时先报 symbol（一次说清一件事）",
			symbol:          "600000.SH",
			date:            "20240101",
			wantParamErrHas: "unsupported symbol",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			codes, asOf, paramErr := parseIndexSelectors(tc.symbol, tc.date)

			if tc.wantParamErrHas != "" {
				assert.Contains(t, paramErr, tc.wantParamErrHas, "400 文案要点名是哪一类参数不对")
				assert.Nil(t, codes, "参数不合法时不得给出可用的 codes")
				assert.Nil(t, asOf)
				return
			}
			require.Empty(t, paramErr, "合法参数不该有 400 文案：%v", tc)
			assert.Equal(t, tc.wantCodes, codes,
				"传给库的代码集合必须正好等于参数决定的那一个（恒回三个＝本单要关的缺陷本体）")

			if tc.wantAsOf == "" {
				assert.Nil(t, asOf, "没给 date 时不得下传任何日期")
				return
			}
			require.NotNil(t, asOf, "给了 date 必须下传，不能在这里丢掉")
			want, perr := time.Parse("2006-01-02", tc.wantAsOf)
			require.NoError(t, perr, "用例本身写错了期望日期")
			assert.True(t, want.Equal(*asOf),
				"下传的那一天必须就是调用方给的那一天，连时刻带时区一起（偏移会把 `<=` 错一天）；期望 %s 实得 %s",
				want.Format(time.RFC3339), asOf.Format(time.RFC3339))
		})
	}
}

// TestParseIndexSelectors_RejectsEchoNothingInternal 钉 400 文案的边界：
// 回显调用方自己发的那个 symbol（分得开 000001 与 000001.SH），
// 但不带服务集之外的任何内部细节；同时把「允许清单」从同一份 marketIndexCodes
// 生成，避免文案与代码各说各话。
func TestParseIndexSelectors_RejectsEchoNothingInternal(t *testing.T) {
	t.Parallel()

	_, _, paramErr := parseIndexSelectors("600000.SH", "")
	require.NotEmpty(t, paramErr)
	assert.Contains(t, paramErr, "600000.SH", "调用方要能看见自己被拒的是哪个值")
	for _, code := range marketIndexCodes {
		assert.Contains(t, paramErr, code, "拒绝理由里要给出这一条端点实际供给的代码")
	}
	assert.NotContains(t, paramErr, "ohlcv_daily_qfq", "400 文案不外泄物理表名（canonical 层护栏的口径）")
}

// TestMarketIndexHandler_UnknownSymbolFailsLoud 是 HTTP 面上**非空转**的一条：
// `600000.SH` 在共享开发库的 ohlcv_daily_qfq 里有真行，旧形状会照样回 200 +
// 三个指数代码（空数组），新形状必须在进库之前就回 400。
//
// ⚠️ 分工：这条钉「坏 symbol ⇒ 400 且不碰库」；它钉不了「好 symbol ⇒ 只回那一条」，
// 那一半由 TestParseIndexSelectors（分派）+ TestLatestIndexBars_OnlyRequestedCodesAreRead
// （过滤）+ TestMarketIndexHandler_SymbolNarrowsEntries（同步过指数日线的库上有牙口）
// 合起来守。
func TestMarketIndexHandler_UnknownSymbolFailsLoud(t *testing.T) {
	store := stocksCountTestStore(t)

	router := gin.New()
	router.GET("/market/index", marketIndexHandler(store))

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/market/index?symbol=600000.SH", nil))

	assert.Equal(t, http.StatusBadRequest, w.Code,
		"服务集外的 symbol 必须 400，不能回 200 + 空数组把「这里不供给」说成「没有读数」；实际 body：%s", w.Body.String())
	assert.NotContains(t, w.Body.String(), `"indices"`,
		"400 响应不得带半份成功数据；实际 body：%s", w.Body.String())
	assert.Contains(t, w.Body.String(), "unsupported symbol", "失败响应要点名是哪一类问题；实际 body：%s", w.Body.String())
}

// TestMarketIndexHandler_BadDateFailsLoud 同上，另一类坏参数：
// 20240101 是这条端点**不认**的格式（认 YYYY-MM-DD）。静默忽略 date
// 就等于回到 DATA-11 的起点 —— 参数存在但不影响读数。
func TestMarketIndexHandler_BadDateFailsLoud(t *testing.T) {
	store := stocksCountTestStore(t)

	router := gin.New()
	router.GET("/market/index", marketIndexHandler(store))

	for _, raw := range []string{"20240101", "2024-13-45", "not-a-date"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/market/index?date="+raw, nil))
		assert.Equal(t, http.StatusBadRequest, w.Code,
			"date=%q 必须 400（格式不认就当没给＝参数不生效的老形状）；实际 body：%s", raw, w.Body.String())
		assert.Contains(t, w.Body.String(), "invalid date format", "body: %s", w.Body.String())
	}
}

// TestMarketIndexHandler_SymbolNarrowsEntries 钉住「条目 ⊆ 点名的那个代码」这个
// 形状契约。**该库同步过指数日线（DATA-12）时**它有牙口：点单代码最多回那一条，
// 若有人把参数分派改回恒回三个代码，这条会红。库侧 0 行时循环不执行（空转如实写明，
// 不假装它证明了过滤）。
//
// 与之配对的两条是 TestParseIndexSelectors 与
// TestMarketIndexHandler_UnknownSymbolFailsLoud（任意数据状态下都有牙口）。
func TestMarketIndexHandler_SymbolNarrowsEntries(t *testing.T) {
	store := stocksCountTestStore(t)

	router := gin.New()
	router.GET("/market/index", marketIndexHandler(store))

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/market/index?symbol=000001.SH", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var body struct {
		Indices []struct {
			Code string `json:"code"`
		} `json:"indices"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
	require.NotNil(t, body.Indices, "indices 恒为数组（空也是 []，不是 null）")

	for _, entry := range body.Indices {
		assert.Equal(t, "000001.SH", entry.Code,
			"点了 symbol 就不该带出别的指数；实际条目：%s（该库 0 行时这条不执行，见函数注释）", entry.Code)
	}
	assert.LessOrEqual(t, len(body.Indices), 1,
		"点名单代码最多一条读数（DATA-12 灌数之后仍然成立）；实际：%d 条", len(body.Indices))
}
