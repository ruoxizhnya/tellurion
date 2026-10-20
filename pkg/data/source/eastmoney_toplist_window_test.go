package source

// DBT-10 的两条窗口守卫。
//
// 起因是 2026-10-19 的真源腿（`cmd/data/dbt10_top_list_live_test.go`）：执行器
// 请求 2026-09-21~09-24，库里却落进了 2026-10-19 的整页榜单，作业照报 completed。
// 根因是 filter 的形状 —— 东财 datacenter 认 `(A)(B)` 括号串联，不认 `(A) and (B)`；
// 用后者时它**不报错、整条当没看见**，回按 TRADE_DATE 倒序的第 1 页。真源对照实测：
//
//	filter=(TRADE_DATE>='2026-09-21') and (TRADE_DATE<='2026-09-24')  → 返回 2026-10-19，pages=372
//	filter=(TRADE_DATE>='2026-09-21')(TRADE_DATE<='2026-09-24')       → 返回 2026-09-24，pages=100
//
// 这个形状 **httptest 抓不到**：替身会照你给的条件返回，只有真源装看不见。所以这里
// 补的不是「复现源的行为」，而是把那条与语法无关的兜底钉住 —— 回来的行不在请求
// 窗口内就整页失败，一条都不交出去。静默丢掉窗口外同样不许（库里留洞 = CR-41 家族）。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustDate(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse("2006-01-02", s)
	require.NoError(t, err)
	return d
}

// newTopListAdapterOn 把 adapter 指到替身源上。DataCenterURL 是导出字段，
// fetchTopList 走的就是它（eastmoney_sectors_adapter.go 里 GetJSON(…, a.client.DataCenterURL, …)）。
func newTopListAdapterOn(baseURL string) *EastmoneyTopListAdapter {
	return NewEastmoneyTopListAdapter(&EastmoneyClient{
		BaseURL:       baseURL,
		DataCenterURL: baseURL,
		HTTPClient:    &http.Client{Timeout: 5 * time.Second},
	})
}

func topListBody(rows ...map[string]interface{}) string {
	body, err := json.Marshal(map[string]interface{}{
		"rc":     0,
		"result": map[string]interface{}{"data": rows},
	})
	if err != nil {
		panic(err) // 测试夹具自己拼错，直接炸，不许演成"源返回空"
	}
	return string(body)
}

func topListRow(code, name, tradeDate string) map[string]interface{} {
	return map[string]interface{}{
		"SECURITY_CODE":      code,
		"SECURITY_NAME_ABBR": name,
		"TRADE_DATE":         tradeDate,
		"BILLBOARD_NET_AMT":  -192221507.43,
		"BILLBOARD_BUY_AMT":  460441061.71,
		"BILLBOARD_SELL_AMT": 652662569.14,
		"TURNOVER":           1234567.89,
		"EXPLAIN":            "机构买入，散户卖出",
		"CLOSE_PRICE":        10.5,
		"CHANGE_RATE":        1.2,
	}
}

// 守卫 1：filter 必须按东财认的形状发出去。钉的是**发出去的查询串**，
// 不是替身的返回 —— 因为坏形状的表现恰好是「源返回得一切正常」，
// 只有请求侧能看出区别。
func TestEastmoneyTopListFilterIsConcatenatedNotAndJoined(t *testing.T) {
	t.Parallel()

	var gotFilter string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotFilter = r.URL.Query().Get("filter")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(topListBody(topListRow("000002", "万科A", "2026-09-24 00:00:00"))))
	}))
	defer srv.Close()

	a := newTopListAdapterOn(srv.URL)
	res, err := a.Fetch(context.Background(), FetchRequest{
		DataType:  DataTypeTopList,
		StartDate: mustDate(t, "2026-09-21"),
		EndDate:   mustDate(t, "2026-09-24"),
		Extra:     map[string]interface{}{"page_number": 1, "page_size": 50},
	})
	require.NoError(t, err)
	require.NotNil(t, res)

	assert.Equal(t, `(TRADE_DATE>='2026-09-21')(TRADE_DATE<='2026-09-24')`, gotFilter,
		"东财 datacenter 的多条件语法是括号串联；用 \" and \" 拼它不报错，而是整条 filter "+
			"当没看见、回最新一页（2026-10-19 真源腿实测）。这一条就是那个形状钉子")
	assert.Len(t, res.Items, 1)
}

// 守卫 2：源无视窗口时，一行都不许交出去。替身在这里演的是**真源那个坏行为**
// —— 收了 filter 却回窗口外的日期。
func TestEastmoneyTopListRefusesRowsOutsideRequestedWindow(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(topListBody(
			topListRow("000002", "万科A", "2026-09-22 00:00:00"),  // 窗口内
			topListRow("000011", "深物业A", "2026-10-19 00:00:00"), // 窗口外（真源就是这么回的）
		)))
	}))
	defer srv.Close()

	a := newTopListAdapterOn(srv.URL)
	_, err := a.Fetch(context.Background(), FetchRequest{
		DataType:  DataTypeTopList,
		StartDate: mustDate(t, "2026-09-21"),
		EndDate:   mustDate(t, "2026-09-24"),
	})
	require.Error(t, err, "窗口外的行必须整页失败 —— 静默收下就是 2026-10-19 那次事故的重演")
	assert.Contains(t, err.Error(), "outside the requested window")
	assert.Contains(t, err.Error(), "2026-10-19", "错误必须点名是哪一天越界，否则还得人去猜")
}

// 守卫 2b：日期不可解析同样 fail-loud，不许落一条"日期未知"的行。
func TestEastmoneyTopListRefusesUnparseableTradeDate(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(topListBody(topListRow("000002", "万科A", ""))))
	}))
	defer srv.Close()

	a := newTopListAdapterOn(srv.URL)
	_, err := a.Fetch(context.Background(), FetchRequest{
		DataType:  DataTypeTopList,
		StartDate: mustDate(t, "2026-09-21"),
		EndDate:   mustDate(t, "2026-09-24"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no TRADE_DATE")
}

// 守卫 2c：两端都必须给。少给一端**不会**退化成"那端无上界"—— Fetch 的
// 校验层直接拒（实测报文：`EndDate 0001-01-01 before StartDate 2026-09-21`）。
// 这条与执行器那道门（topListWindow：两端必填）是同一个契约的两处落点，
// 钉在这里是为了让"少写一个日期"不会变成一条静默取全量的作业。
func TestEastmoneyTopListRequiresBothWindowBounds(t *testing.T) {
	t.Parallel()

	called := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(topListBody(topListRow("000002", "万科A", "2026-10-19 00:00:00"))))
	}))
	defer srv.Close()

	a := newTopListAdapterOn(srv.URL)
	_, err := a.Fetch(context.Background(), FetchRequest{
		DataType:  DataTypeTopList,
		StartDate: mustDate(t, "2026-09-21"),
	})
	require.Error(t, err, "只给下界必须被拒 —— 否则它就退化成「无界取全量」，正是这次事故的另一半")
	assert.Equal(t, 0, called, "校验层拦下时不许已经把请求发出去")
}

// 守卫 2d：后缀与代码段一致 —— 这是真源腿上那条断言的替身版，
// 好处是不论源侧作息都能跑。
func TestEastmoneyTopListSymbolSuffixMatchesCodeSegment(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(topListBody(
			topListRow("600519", "贵州茅台", "2026-09-22 00:00:00"),
			topListRow("000002", "万科A", "2026-09-22 00:00:00"),
			topListRow("300750", "宁德时代", "2026-09-22 00:00:00"),
			topListRow("830799", "艾融软件", "2026-09-22 00:00:00"),
		)))
	}))
	defer srv.Close()

	a := newTopListAdapterOn(srv.URL)
	res, err := a.Fetch(context.Background(), FetchRequest{
		DataType:  DataTypeTopList,
		StartDate: mustDate(t, "2026-09-21"),
		EndDate:   mustDate(t, "2026-09-24"),
	})
	require.NoError(t, err)
	require.Len(t, res.Items, 4)
	want := map[string]string{
		"600519": "600519.SH",
		"000002": "000002.SZ",
		"300750": "300750.SZ",
		"830799": "830799.SZ",
	}
	for _, it := range res.Items {
		code := it.Symbol[:6]
		assert.Equal(t, want[code], it.Symbol,
			"%s 的后缀不对：'6'/'9' 段 → .SH，其余 → .SZ（eastmoney_sectors_adapter.go 的补全分支）", code)
	}
}
