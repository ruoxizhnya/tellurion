package source

// TushareMoneyflowAdapter（DBT-09）的单元测试。
//
// 三条主证据不在「adapter 自己」，而在它与链的关系上，所以本文件把它们
// 写全：
//
//   - **失败分类**决定链会不会兜底：传输级失败必须包成
//     ErrUpstreamUnavailable（IsRetryable ⇒ Registry 去试 eastmoney）；
//     参数非法 / 数据质量必须原样上抛（换源重取不会让契约变得符合）。
//     分错一类的后果是静默的：全都算可重试 ⇒ 坏请求被兜底源再打一遍；
//     全都算永久 ⇒ 主用一挂就没人兜底，「Tushare 优先、eastmoney 兜底」
//     那句裁决成为注释里的装饰。
//   - **链顺序** [tushare_moneyflow, eastmoney] 下两员各就各位：Tushare 有数
//     时它供数（source 列写 tushare_moneyflow），Tushare 挂时 eastmoney 供数。
//   - **wire 投影**：本门只产出四档净额 + period，不产出 main_net 等列，
//     缺的档在 Data 里是显式 nil（落库侧据此折 NULL）。
//
// PIT 声明腿（after_close + daily）与 D5 那条同形：把锚点放宽到盘中可见
// 必须让本文件变红。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/tellurion-contracts/domain/market"
	"tellurion/pkg/data"
)

// moneyflowAdapterBody 拼一个完整列头的 moneyflow 响应，items 由用例给定。
func moneyflowAdapterBody(items string) string {
	return `{"code":0,"msg":"","data":{"fields":[` +
		`"ts_code","trade_date",` +
		`"buy_elg_amount","sell_elg_amount",` +
		`"buy_lg_amount","sell_lg_amount",` +
		`"buy_md_amount","sell_md_amount",` +
		`"buy_sm_amount","sell_sm_amount"],` +
		`"items":[` + items + `]}}`
}

// moneyflowTwoSymbolBody 是两票各一日的完整四档响应（单位万元）。
// 600519: elg 100.5−0.5=100 ⇒ 1_000_000 元；lg 50.25−20.25=30 ⇒ 300_000；
//
//	md 10−40=−30 ⇒ −300_000；sm 1−1=0 ⇒ 0（真 0）。
//
// 000001: elg 200−100=100 ⇒ 1_000_000；lg 10−5=5 ⇒ 50_000；md 1−2=−1 ⇒ −10_000；sm 0。
var moneyflowTwoSymbolBody = moneyflowAdapterBody(
	`["600519.SH","20260930",100.5,0.5,50.25,20.25,10.0,40.0,1.0,1.0],` +
		`["000001.SZ","20260930",200.0,100.0,10.0,5.0,1.0,2.0,0.5,0.5]`)

// eastmoneyOneKline 是链上兜底一员能解析的最小日线资金流响应
// （CSV 19 段，字段序见 parseEastmoneyCapitalFlowKLines）。
const eastmoneyOneKline = `{"rc":0,"data":{"code":"600519","name":"x","total":1,` +
	`"klines":["2026-09-30,1720.5,1.22,1000,1500,-500,12.5,400,-100,300,600,-200,400,300,-100,200,200,-100,100"]}}`

// newMoneyflowAdapter 起 httptest 上游并造一个指向它的 adapter（store 传 nil：
// 本门不落库，归档面在这里也不是被测对象）。
//
// 替身**按请求里的 ts_code 过滤行**，这是它必须做的事：本 adapter 逐 symbol
// 一发请求，一个「不管问谁都把整张表答回来」的替身会让行数断言失真
// （两票各一日会答成 4 行），而失真方向恰好是「看起来数据更多」。
// status 非 200 时模拟上游故障。
func newMoneyflowAdapter(t *testing.T, status int, body string) *TushareMoneyflowAdapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte("upstream exploded"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(moneyflowFilterToRequestedTSCode(t, body, r)))
	}))
	t.Cleanup(srv.Close)
	return NewTushareMoneyflowAdapter(data.NewTushareClient("test-token", srv.URL, 0, nil, nil))
}

// moneyflowFilterToRequestedTSCode 把固定响应里属于请求票的行挑出来。
// 解不出请求参数或结构不符时原样返回（宁可不滤，也不要在替身里造出一条
// 与真源不同的错误路径）。
func moneyflowFilterToRequestedTSCode(t *testing.T, body string, r *http.Request) string {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	if err != nil || len(raw) == 0 {
		return body
	}
	var req struct {
		Params struct {
			TSCode string `json:"ts_code"`
		} `json:"params"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.Params.TSCode == "" {
		return body
	}

	var resp struct {
		Code int `json:"code"`
		Data struct {
			Fields []string `json:"fields"`
			Items  [][]any  `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return body
	}
	kept := make([][]any, 0, len(resp.Data.Items))
	for _, item := range resp.Data.Items {
		if len(item) > 0 && item[0] == req.Params.TSCode {
			kept = append(kept, item)
		}
	}
	resp.Data.Items = kept
	out, err := json.Marshal(resp)
	if err != nil {
		return body
	}
	return string(out)
}

func moneyflowReq(symbols []string, start, end time.Time, period string) FetchRequest {
	return FetchRequest{
		DataType:  DataTypeCapitalFlow,
		Symbols:   symbols,
		StartDate: start,
		EndDate:   end,
		Period:    period,
	}
}

// —— 注册契约（fail-closed 那一族）——

func TestTushareMoneyflowAdapter_NilClientRegistersDisabled(t *testing.T) {
	a := NewTushareMoneyflowAdapter(nil)
	assert.False(t, a.Enabled(), "nil client（token 未配置）→ disabled 降级，不阻断启动")
	assert.Equal(t, "tushare_moneyflow", a.Name())
	assert.Equal(t, AdapterTypeHTTP, a.Type())
	assert.Equal(t, []string{DataTypeCapitalFlow}, a.SupportedTypes())

	_, err := a.Fetch(context.Background(), moneyflowReq([]string{"600519.SH"}, d5Day(2026, 10, 14), d5Day(2026, 10, 14), ""))
	require.Error(t, err, "nil client 必须 fail-loud，不许返回空响应冒充「这个窗口没数据」")
	assert.Contains(t, err.Error(), "nil client")
}

func TestTushareMoneyflowAdapter_DeclarationsAreAfterCloseDaily(t *testing.T) {
	a := NewTushareMoneyflowAdapter(nil)
	decls := a.Declarations()
	require.Len(t, decls, 1, "SupportedTypes 只有一个成员，声明也必须只有一条")
	assert.Equal(t, DataTypeCapitalFlow, decls[0].DataType)
	assert.Equal(t, market.VisibleAfterClose, decls[0].TimeSemantics,
		"分档成交额由全日逐笔归类聚合，收盘前不存在；放宽到盘中可见就是把前视放回来")
	assert.Equal(t, VisibleFieldTradeTime, decls[0].VisibleField)
	assert.Equal(t, market.FreqSpecDaily, decls[0].Frequency)

	// 这条声明必须能被 Registry 接受（漏声明 = fail-closed 拒收 = 启动时那条门消失）。
	reg := NewRegistry()
	require.NoError(t, reg.Register(a), "声明不合法会被 Registry fail-closed 拒收")
}

func TestTushareMoneyflowAdapter_SchemaOnlyListsWhatItProduces(t *testing.T) {
	a := NewTushareMoneyflowAdapter(nil)
	schema, err := a.Schema(DataTypeCapitalFlow)
	require.NoError(t, err)
	assert.Equal(t, DataTypeCapitalFlow, schema.DataType)

	names := map[string]SchemaField{}
	for _, f := range schema.Fields {
		names[f.Name] = f
	}
	for _, want := range []string{"period", "super_net", "large_net", "medium_net", "small_net"} {
		_, ok := names[want]
		assert.True(t, ok, "wire schema 缺 %s", want)
	}
	// 反证腿：这些列本源不给，声明它们就等于宣称能供上。
	for _, absent := range []string{"main_net", "main_buy_amount", "main_sell_amount",
		"main_net_ratio", "retail_net", "retail_net_ratio", "close_price", "change_pct",
		"net_mf_amount"} {
		_, ok := names[absent]
		assert.False(t, ok, "wire schema 不该声明 %s：moneyflow 不产出它，声明了就会有人拿它当可得列去算因子", absent)
	}
	for _, numeric := range []string{"super_net", "large_net", "medium_net", "small_net"} {
		assert.Equal(t, "yuan", names[numeric].Unit,
			"单位声明必须是 yuan：万元→元的换算在同步边界（pkg/data）做完了，wire 到这儿就是元")
	}

	_, err = a.Schema(DataTypeTopList)
	require.Error(t, err, "不服务的类型要 ErrUnsupported")
	assert.True(t, errors.Is(err, ErrUnsupported))
}

// —— Fetch 请求形状 ——

func TestTushareMoneyflowAdapter_FetchRequiresSymbolsAndWindow(t *testing.T) {
	a := newMoneyflowAdapter(t, http.StatusOK, moneyflowTwoSymbolBody)

	_, err := a.Fetch(context.Background(), moneyflowReq(nil, d5Day(2026, 10, 14), d5Day(2026, 10, 14), ""))
	require.Error(t, err, "symbols 为空必须报错：本门逐 symbol 一发请求，「默认全池」= 5900 次调用")
	assert.Contains(t, err.Error(), "symbols required")

	_, err = a.Fetch(context.Background(), moneyflowReq([]string{"600519.SH"}, time.Time{}, time.Time{}, ""))
	require.Error(t, err, "不给窗口 = 让源决定行数（隐藏默认值），必须拒")
	assert.Contains(t, err.Error(), "required")

	_, err = a.Fetch(context.Background(), moneyflowReq(
		[]string{"600519.SH"}, d5Day(2020, 1, 1), d5Day(2026, 10, 14), ""))
	require.Error(t, err, "跨度超上限必须报错而不是悄悄拉爆配额")
	assert.Contains(t, err.Error(), "exceeds")

	_, err = a.Fetch(context.Background(), moneyflowReq(
		[]string{"600519.SH"}, d5Day(2026, 10, 14), d5Day(2026, 10, 13), ""))
	require.Error(t, err, "窗口倒置（end 早于 start）不许当成空区间静默跑过去")
	assert.Contains(t, err.Error(), "before")
}

func TestTushareMoneyflowAdapter_FetchRejectsOtherDataTypes(t *testing.T) {
	a := newMoneyflowAdapter(t, http.StatusOK, moneyflowTwoSymbolBody)
	_, err := a.Fetch(context.Background(), FetchRequest{
		DataType:  DataTypeTopList,
		StartDate: d5Day(2026, 10, 14),
		EndDate:   d5Day(2026, 10, 14),
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUnsupported), "不服务的类型必须落到 ErrUnsupported，好让链把它算作「不该走这条路」")
}

func TestTushareMoneyflowAdapter_FetchProjectsItemsAndDefaultsPeriod(t *testing.T) {
	a := newMoneyflowAdapter(t, http.StatusOK, moneyflowTwoSymbolBody)

	resp, err := a.Fetch(context.Background(), moneyflowReq(
		[]string{"600519.SH", "000001.SZ"}, d5Day(2026, 9, 30), d5Day(2026, 9, 30), ""))
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "tushare_moneyflow", resp.Source, "Source 必须是真正供数的门名，落库的 source 列据此写")
	require.Len(t, resp.Items, 2, "两票各一日")

	bySymbol := map[string]DataItem{}
	for _, it := range resp.Items {
		bySymbol[it.Symbol] = it
	}
	first, ok := bySymbol["600519.SH"]
	require.True(t, ok)
	assert.Equal(t, time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC), first.TradeTime,
		"TradeTime 是 capital_flow 冲突键的第二列与 after_close 锚点的载体")
	assert.Equal(t, "daily", first.Data["period"], "空 period 折成 daily：period 是 (symbol,trade_date,period) 冲突键的第三列，不许留空")

	// 手算：期望值不来自被测代码。
	assert.InDelta(t, 1_000_000.0, first.Data["super_net"], 1e-6)
	assert.InDelta(t, 300_000.0, first.Data["large_net"], 1e-6)
	assert.InDelta(t, -300_000.0, first.Data["medium_net"], 1e-6)
	assert.InDelta(t, 0.0, first.Data["small_net"], 1e-9)
	second := bySymbol["000001.SZ"]
	assert.InDelta(t, 1_000_000.0, second.Data["super_net"], 1e-6, "200−100=100 万元")
	assert.InDelta(t, 50_000.0, second.Data["large_net"], 1e-6, "10−5=5 万元")
	assert.InDelta(t, -10_000.0, second.Data["medium_net"], 1e-6, "1−2=−1 万元")
	assert.InDelta(t, 0.0, second.Data["small_net"], 1e-9)

	for _, sym := range []string{"600519.SH", "000001.SZ"} {
		it := bySymbol[sym]
		for _, col := range []string{"super_net", "large_net", "medium_net", "small_net"} {
			_, present := it.Data[col]
			assert.True(t, present, "%s 的 %s 键必须显式存在（缺的档写 nil，不是不写键）", sym, col)
		}
		// 本门不产出的列不许出现在 wire 里。
		for _, col := range []string{"main_net", "retail_net", "close_price", "change_pct"} {
			_, present := it.Data[col]
			assert.False(t, present, "wire 里不该有 %s：本门不产出，出现就是有人在这里补造了数据", col)
		}
	}

	// 显式 period 透传（与链上另一员同形）。
	resp2, err := a.Fetch(context.Background(), moneyflowReq(
		[]string{"600519.SH"}, d5Day(2026, 9, 30), d5Day(2026, 9, 30), "daily"))
	require.NoError(t, err)
	require.Len(t, resp2.Items, 1)
	assert.Equal(t, "daily", resp2.Items[0].Data["period"])
}

func TestTushareMoneyflowAdapter_FetchKeepsMissingTierVisible(t *testing.T) {
	// 上游把大单两列整列剔除（该期全空）。
	body := `{"code":0,"msg":"","data":{"fields":[` +
		`"ts_code","trade_date",` +
		`"buy_elg_amount","sell_elg_amount",` +
		`"buy_md_amount","sell_md_amount",` +
		`"buy_sm_amount","sell_sm_amount"],` +
		`"items":[["600519.SH","20260930",100.5,0.5,10.0,40.0,1.0,1.0]]}}`
	a := newMoneyflowAdapter(t, http.StatusOK, body)

	resp, err := a.Fetch(context.Background(), moneyflowReq(
		[]string{"600519.SH"}, d5Day(2026, 9, 30), d5Day(2026, 9, 30), ""))
	require.NoError(t, err)
	require.Len(t, resp.Items, 1)
	it := resp.Items[0]

	assert.Nil(t, it.Data["large_net"], "被剔除的一档在 wire 上是显式 nil ⇒ 落库 NULL；折 0 就是宣称该档买卖恰好相等")
	assert.InDelta(t, 1_000_000.0, it.Data["super_net"], 1e-6, "其余档仍按列名对位")
	assert.InDelta(t, -300_000.0, it.Data["medium_net"], 1e-6)
}

// —— 失败分类：决定链会不会兜底 ——

func TestTushareMoneyflowAdapter_TransportFailureIsRetryableForFallback(t *testing.T) {
	a := newMoneyflowAdapter(t, http.StatusInternalServerError, "")

	_, err := a.Fetch(context.Background(), moneyflowReq(
		[]string{"600519.SH"}, d5Day(2026, 9, 30), d5Day(2026, 9, 30), ""))
	require.Error(t, err)
	assert.True(t, IsRetryable(err),
		"上游 5xx 必须归入 ErrUpstreamUnavailable ⇒ Registry 才会去试 eastmoney；"+
			"分类错就等于「Tushare 优先、eastmoney 兜底」那条裁决没落地：%v", err)
	assert.True(t, errors.Is(err, ErrUpstreamUnavailable))
	assert.Contains(t, err.Error(), "tushare_moneyflow capital_flow 600519.SH", "失败要点名到票")
}

func TestTushareMoneyflowAdapter_DataQualityFailureIsNotRetryable(t *testing.T) {
	// 身份列缺失 = 形状不符契约。换一条腿重取同一区间不会让契约变得符合，
	// 所以它**不该**触发兜底（那只会把同一个坏请求再打一遍）。
	a := newMoneyflowAdapter(t, http.StatusOK,
		`{"code":0,"msg":"","data":{"fields":["buy_elg_amount"],"items":[[1.0]]}}`)

	_, err := a.Fetch(context.Background(), moneyflowReq(
		[]string{"600519.SH"}, d5Day(2026, 9, 30), d5Day(2026, 9, 30), ""))
	require.Error(t, err)
	assert.False(t, IsRetryable(err),
		"数据质量/契约不符必须原样上抛，不许伪装成「换源再试」：%v", err)
	assert.Contains(t, err.Error(), "ts_code")
}

// —— 链：顺序真的决定谁供数 ——

// newTwoMemberChain 造一条与生产 configureFallbackChains 同形的两员链：
// tushare 指向给定替身，eastmoney 指向另一个替身。
func newTwoMemberChain(t *testing.T, tushareStatus int, tushareBody string, eastmoneyStatus int, eastmoneyBody string) *Registry {
	t.Helper()

	tsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if tushareStatus != http.StatusOK {
			w.WriteHeader(tushareStatus)
			_, _ = w.Write([]byte("tushare down"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(tushareBody))
	}))
	t.Cleanup(tsSrv.Close)

	emSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if eastmoneyStatus != http.StatusOK {
			w.WriteHeader(eastmoneyStatus)
			_, _ = w.Write([]byte("push2 502"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(eastmoneyBody))
	}))
	t.Cleanup(emSrv.Close)

	reg := NewRegistry()
	require.NoError(t, reg.Register(NewTushareMoneyflowAdapter(
		data.NewTushareClient("test-token", tsSrv.URL, 0, nil, nil))))
	require.NoError(t, reg.Register(NewEastmoneyAdapter(&EastmoneyClient{
		BaseURL:    emSrv.URL,
		HTTPClient: &http.Client{Timeout: 5 * time.Second},
	})))
	reg.SetChain(DataTypeCapitalFlow, []string{"tushare_moneyflow", "eastmoney"})
	return reg
}

func TestCapitalFlowChain_TushareIsPrimary(t *testing.T) {
	oneRow := moneyflowAdapterBody(`["600519.SH","20260930",100.5,0.5,50.25,20.25,10.0,40.0,1.0,1.0]`)
	reg := newTwoMemberChain(t,
		http.StatusOK, oneRow,
		http.StatusOK, eastmoneyOneKline)

	resp, err := reg.Fetch(context.Background(), moneyflowReq(
		[]string{"600519.SH"}, d5Day(2026, 9, 30), d5Day(2026, 9, 30), ""))
	require.NoError(t, err)
	assert.Equal(t, "tushare_moneyflow", resp.Source,
		"链首选必须真的供数 —— source 列写的是它，读侧据此判断口径（Tushare 分档 vs 东财净流入）")
	require.Len(t, resp.Items, 1, "一票一日就是 1 行")
	assert.InDelta(t, 1_000_000.0, resp.Items[0].Data["super_net"], 1e-6,
		"到手的就是 Tushare 的四档口径（元），不是兜底腿的数")
}

func TestCapitalFlowChain_EastmoneyServesWhenTushareIsDown(t *testing.T) {
	// 兜底腿：Tushare 5xx ⇒ eastmoney 供数。这条存在才有「兜底」二字，
	// 否则链上第二员只是注释里的装饰。
	reg := newTwoMemberChain(t,
		http.StatusInternalServerError, "",
		http.StatusOK, eastmoneyOneKline)

	resp, err := reg.Fetch(context.Background(), moneyflowReq(
		[]string{"600519.SH"}, d5Day(2026, 9, 30), d5Day(2026, 9, 30), ""))
	require.NoError(t, err, "Tushare 挂了必须能落到 eastmoney，而不是整条链失败")
	assert.Equal(t, "eastmoney", resp.Source)
}

func TestCapitalFlowChain_BothDownFailsLoudWithTriedList(t *testing.T) {
	reg := newTwoMemberChain(t, http.StatusInternalServerError, "", http.StatusBadGateway, "")

	_, err := reg.Fetch(context.Background(), moneyflowReq(
		[]string{"600519.SH"}, d5Day(2026, 9, 30), d5Day(2026, 9, 30), ""))
	require.Error(t, err, "两员都失败 ⇒ 作业如实失败，不许静默空表")
	assert.Contains(t, err.Error(), "tushare_moneyflow", "error 里要看得见试过谁：%v", err)
}

// TestTushareMoneyflowAdapter_HealthCheckClassification 钉探测的失败分类：
// 探测失败要算 ErrUpstreamUnavailable（观测端点据此报降级），而不是永久错误。
func TestTushareMoneyflowAdapter_HealthCheckClassification(t *testing.T) {
	down := newMoneyflowAdapter(t, http.StatusInternalServerError, "")
	err := down.HealthCheck(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUpstreamUnavailable), "探测失败要点名是上游不可得：%v", err)

	ok := newMoneyflowAdapter(t, http.StatusOK, moneyflowAdapterBody(``))
	assert.NoError(t, ok.HealthCheck(context.Background()), "上游答了就算连通（哪怕 0 行：非交易日是合法答案）")

	none := NewTushareMoneyflowAdapter(nil)
	require.Error(t, none.HealthCheck(context.Background()), "nil client 的探测必须失败而不是假装健康")
}

func TestTushareMoneyflowAdapter_RateLimitAndString(t *testing.T) {
	a := NewTushareMoneyflowAdapter(nil)
	assert.Equal(t, 200, a.RateLimit().RequestsPerMinute,
		"与 TushareClient 自身的闸值同数：写大了等于宣称有一个并不存在的配额")
	assert.Contains(t, a.String(), "tushare_moneyflow")
}
