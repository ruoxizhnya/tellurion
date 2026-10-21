package source

// DBT-13 的适配器层「日期保真」腿。
//
// 这张单要判定的是一件事：eastmoney / juchao 族的**每一行**，落库日期要么是
// 源给的真值，要么当场失败 —— 不许有「我读不懂所以日期是今天」这条路径。
//
// 本文件刻意只走**现有入口**（`adapter.Fetch` + httptest 替身），不引用任何
// 新符号。这样同一份断言能跑在改之前（必须真红，取证见 docs/TASKS.md 的
// DBT-13 结案记录）也能跑在改之后（转绿），红得有名，而不是靠编译失败演红。
//
// 断言形状用「固定历史日期的夹具 ⇒ 出来的日期必须就是那个日期」，不用
// 「不等于零值」—— 回落塌成 time.Now() 时后者照样过，前者必红。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// topListFetchErr 用一条夹具行驱动一次真实 Fetch，返回错误报文（无错返回空串）。
// omitKey 为 true 时把夹具行的 TRADE_DATE 整个删掉，用来演「源没给这个键」；
// end 是请求窗口的上界 —— 放到「今天」才能让落进哪道闸只取决于日期本身。
func topListFetchErr(t *testing.T, tradeDate interface{}, omitKey bool, end time.Time) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		row := topListRow("000002", "万科A", "2026-09-22 00:00:00")
		if omitKey {
			delete(row, "TRADE_DATE")
		} else {
			row["TRADE_DATE"] = tradeDate
		}
		_, _ = w.Write([]byte(topListBody(row)))
	}))
	defer srv.Close()

	_, err := newTopListAdapterOn(srv.URL).Fetch(context.Background(), FetchRequest{
		DataType:  DataTypeTopList,
		StartDate: mustDate(t, "2026-09-21"),
		EndDate:   end,
	})
	if err != nil {
		return err.Error()
	}
	return ""
}

// DBT-13 ⑤ 要的「三种失败各有报文」：缺失 / 读不懂 / 窗口外各由不同的闸报，
// 报文两两不同。混了就没人能从一条错误日志知道该去修哪一头。
//
// 前两条把窗口上界放到「今天」：夹具日期读不懂时旧实现会折成今天，
// 而今天落在窗口**内** ⇒ 只有源侧这道拦差能拦住它。窗口外那条反过来用
// 一条读得懂、但晚于上界的行，确保报文的出处是窗口闸而不是解析闸。
func TestDBT13EastmoneyTopList_ThreeFailureModesReportDistinctly(t *testing.T) {
	t.Parallel()

	openEnd := time.Now().UTC()
	absent := topListFetchErr(t, nil, true, openEnd)
	unreadable := topListFetchErr(t, "22/09/2026", false, openEnd)
	outWindow := topListFetchErr(t, "2026-10-19 00:00:00", false, mustDate(t, "2026-09-24"))

	for name, got := range map[string]string{"absent": absent, "unreadable": unreadable, "out_of_window": outWindow} {
		require.NotEmpty(t, got, "%s：这种形状必须失败，静默收下就是把脏行写进库", name)
	}
	assert.Contains(t, absent, "has no TRADE_DATE", "「源没给日期」由缺失那道闸报")
	assert.Contains(t, unreadable, "is not a date this parser can read",
		"读不懂必须由 parseEastmoneyDateStrict 的拦差报，不许蹭 checkTopListWindow 的零值兜底 —— "+
			"两道闸报文相同时，删掉新加的那道也没人会红（2026-10-21 破坏验证实测踩到）")
	assert.Contains(t, outWindow, "outside the requested window", "窗口外由 checkTopListWindow 报")

	assert.NotEqual(t, absent, unreadable, "「没给」与「给了但读不懂」是两件事，报文不许共用")
	assert.NotEqual(t, unreadable, outWindow, "「读不懂」不许被报成「窗口越界」")
}

// 用**包含今天**的请求窗口，所以旧实现的「读不懂就当今天」不会被窗口守卫拦下 ——
// 那正是这条要抓的形状：一行日期不可读的榜单被静默收下，trade_date 落今天，
// 撞上冲突键 (今天, symbol) 就把当天真数据覆盖掉。
func TestDBT13EastmoneyTopList_RefusesTradeDateItCannotRead(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(topListBody(topListRow("000002", "万科A", "22/09/2026"))))
	}))
	defer srv.Close()

	a := newTopListAdapterOn(srv.URL)
	res, err := a.Fetch(context.Background(), FetchRequest{
		DataType:  DataTypeTopList,
		StartDate: mustDate(t, "2026-09-21"),
		EndDate:   time.Now().UTC(),
	})
	require.Error(t, err,
		"读不懂的 TRADE_DATE 必须当场失败；静默收下 = 把历史榜单写成今天那一行")
	require.Nil(t, res, "拦下时不许把行交出去")

	// 报文必须点名「解析读不懂」，而不是别的原因 —— 否则有人把这道拦差删掉、
	// 让下游零值兜底代劳时，这条测试毫无反应（同日破坏验证实测）。
	assert.Contains(t, err.Error(), "is not a date this parser can read")
	assert.NotContains(t, err.Error(), "outside the requested window",
		"日期读不懂不该被报成窗口越界 —— 那是两件事，混了就没人去修解析")
	assert.Contains(t, err.Error(), "000002", "错误要点名是哪一行，否则还得人回源翻整页")
}

// 反证腿：源给了能读的日期时，交出去的必须是**源那一天**，不是今天。
// 这条在改之前也过（DBT-10 已补过带时刻的两种形状），留着是为了证明
// 收口「读不懂」没有把「读得懂」那条路一起关掉。
func TestDBT13EastmoneyTopList_KeepsReadableTradeDate(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(topListBody(topListRow("000002", "万科A", "2026-09-22 00:00:00"))))
	}))
	defer srv.Close()

	a := newTopListAdapterOn(srv.URL)
	res, err := a.Fetch(context.Background(), FetchRequest{
		DataType:  DataTypeTopList,
		StartDate: mustDate(t, "2026-09-21"),
		EndDate:   time.Now().UTC(),
	})
	require.NoError(t, err)
	require.Len(t, res.Items, 1)
	assert.Equal(t, mustDate(t, "2026-09-22"), res.Items[0].TradeTime.UTC().Truncate(24*time.Hour),
		"读得懂的日期必须原样交出（时刻为 00:00:00 ⇒ 截到日）")
}

// --- juchao：公告日期 ---

// 真源取证（2026-08-15 那期公告，2026-10-21 向 cninfo 发一条真实查询拿到的响应形状）：
//
//	"announcementTime": 1786723200000,                      // epoch **毫秒**
//	"adjunctUrl": "finalpage/2026-08-15/1225475868.PDF",     // 日期段就是公告日
//
// 旧实现把 announcementTime 交给只认日历日形状的 parseEastmoneyDate：13 位数字
// 走不进「8 位才是 YYYYMMDD」那条分支 ⇒ 落到末尾的 time.Now().UTC() ⇒
// **每一条公告的日期都是「今天跑作业那天」**。本文件的两条 juchao 断言就是抓这个。
const juchaoFixtureEpochMS = 1786723200000 // ⇒ 2026-08-15T00:00:00Z

func juchaoRow(id, title string, annTime interface{}, adjunctURL string) map[string]interface{} {
	return map[string]interface{}{
		"announcementId":    id,
		"announcementTitle": title,
		"announcementTime":  annTime,
		"adjunctUrl":        adjunctURL,
		"adjunctType":       "PDF",
	}
}

// newJuchaoAdapterOn 把 adapter 的两条 HTTP 腿都指到替身上：
// /new/data/szse_stock.json（orgId 映射，ensureOrgIDMap 要）与
// /new/hisAnnouncement/query（公告查询本体）。baseURL 是包内字段，所以不必改导出面。
func newJuchaoAdapterOn(t *testing.T, announcementBody string) *JuchaoAdapter {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/new/data/szse_stock.json"):
			_, _ = w.Write([]byte(`{"stockList":[{"code":"600519","orgId":"gssh0600519","zwjc":"贵州茅台"}]}`))
		case strings.HasSuffix(r.URL.Path, "/new/hisAnnouncement/query"):
			_, _ = w.Write([]byte(announcementBody))
		default:
			t.Errorf("juchao 替身收到预期外的路径 %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	a := NewJuchaoAdapter()
	a.baseURL = srv.URL
	a.httpClient = srv.Client()
	return a
}

func TestDBT13Juchao_AnnouncementDateComesFromSourceNotToday(t *testing.T) {
	t.Parallel()

	body, err := json.Marshal(map[string]interface{}{
		"totalAnnouncement": 3,
		"announcements": []map[string]interface{}{
			// 只有 epoch 毫秒可用。
			juchaoRow("1001", "A 公告", juchaoFixtureEpochMS, "finalpage/2026-08-15/1001.PDF"),
			// announcementTime 缺失 ⇒ 用 adjunctUrl 的日期段兜（DBT-13 ③-② 明写的「别的字段」）。
			juchaoRow("1002", "B 公告", nil, "finalpage/2025-03-04/1002.PDF"),
			// 两处都读不懂 ⇒ 这一行必须被拒收，不许写成今天。
			juchaoRow("1003", "C 公告", nil, ""),
		},
	})
	require.NoError(t, err)

	a := newJuchaoAdapterOn(t, string(body))
	res, err := a.Fetch(context.Background(), FetchRequest{
		DataType: DataTypeAnnounce,
		Symbols:  []string{"600519.SH"},
	})
	require.NoError(t, err)
	require.Len(t, res.Items, 2,
		"两处日期都读不懂的那行必须被拒收 —— 它今天的下场是带着「今天」的日期被写进库")

	today := time.Now().UTC().Format("2006-01-02")
	want := []string{"2026-08-15", "2025-03-04"}
	for i, it := range res.Items {
		got := it.TradeTime.UTC().Format("2006-01-02")
		assert.Equal(t, want[i], got, "%s：落库日期必须是源给的日期", it.Symbol)
		assert.NotEqual(t, today, got, "%s：等于「今天」就说明解析又回落了", it.Symbol)
	}
	assert.Equal(t, "600519.SH", res.Items[0].Symbol)
}

// 量级自检：源哪天改口径给成 epoch **秒**（10 位）时，不许悄悄算出 1970 年。
// 判据是「年份落在 2000~2100 之外就当读不懂」—— 把单位错读折成一行可解释的
// 缺失，比折成一行 1970 年的公告好收拾得多。
func TestDBT13Juchao_RejectsWrongEpochScaleInsteadOfInventing1970(t *testing.T) {
	t.Parallel()

	body, err := json.Marshal(map[string]interface{}{
		"announcements": []map[string]interface{}{
			juchaoRow("1004", "D 公告", float64(juchaoFixtureEpochMS/1000), ""), // 秒，不是毫秒
		},
	})
	require.NoError(t, err)

	a := newJuchaoAdapterOn(t, string(body))
	res, err := a.Fetch(context.Background(), FetchRequest{
		DataType: DataTypeAnnounce,
		Symbols:  []string{"600519.SH"},
	})
	require.NoError(t, err)
	assert.Empty(t, res.Items,
		"秒级 epoch 不是本链认得的形状，必须当读不懂处理；收下它就等于写一行 1970 年的公告")
}
