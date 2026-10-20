package data

// moneyflow 归一面（pkg/data/tushare_moneyflow.go）的单测。
//
// 三条腿是本文件的重点，其余是配套：
//
//  1. **缺列腿**（DBT-07 那个缺陷的回归形状）：Tushare 会剔除该期**全空**的
//     列，响应里的列序因此不固定。按固定索引解包 = 把整行右移，把某一档的钱
//     写进另一档的列（DBT-07 真库里是 roe 落进 pe）。判据只有缺列这一种输入
//     能让「按名字对位」与「按位置硬取」给出不同答案，所以这里刻意造一个
//     中间列被剔除的响应，并断言剩下三档各就各位。
//  2. **单位腿**：万元 ×10000 在同步边界完成。期望值是**手算**的字面量，不是
//     拿被测代码再算一遍（capital_flow_job_test.go 立的同一条规矩）。
//  3. **缺失≠0 腿**：真 0（买卖恰好相等）必须是 0，缺侧必须是 nil。
//     这两条在同一个断言块里，因为把它们折成同一个值的代码看起来也是「对」的。
//
// 默认套件不碰外网、不碰真库：上游是 httptest 替身。真源那条腿在
// cmd/data/dbt09_moneyflow_live_test.go（build tag `dbt09live`）。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/storage"
)

// moneyflowStubStore 只实现归档面 —— moneyflow 的写入面是 ETL 的 BulkInsert，
// 这里**绝不该**有任何 Save 被调用；嵌 nil TushareStore，误调即 panic，
// 那正是想要的信号（同 activityStubStore 的姿势）。
type moneyflowStubStore struct {
	TushareStore
	archived int
}

func (s *moneyflowStubStore) SaveRawIngest(_ context.Context, _ *storage.RawIngest) error {
	s.archived++
	return nil
}

func moneyflowResp(fields []string, items [][]any) string {
	body, err := json.Marshal(map[string]any{
		"code": 0,
		"msg":  "",
		"data": map[string]any{"fields": fields, "items": items},
	})
	if err != nil {
		panic(err)
	}
	return string(body)
}

// newMoneyflowStubClient 起一个 httptest 上游，固定回给定响应，并记录
// 每次请求的 api_name / params / fields（参数到达性断言用）。
func newMoneyflowStubClient(t *testing.T, body string, seen *[]map[string]any) *TushareClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			APIName string         `json:"api_name"`
			Params  map[string]any `json:"params"`
			Fields  string         `json:"fields"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if seen != nil {
			*seen = append(*seen, map[string]any{
				"api_name": req.APIName,
				"params":   req.Params,
				"fields":   req.Fields,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return NewTushareClient("test-token", srv.URL, 0, &moneyflowStubStore{}, nil)
}

// 手算夹具（单位：万元 → 期望入库单位：元）
//
//	特大单 100.5 − 0.5 = 100.0 万元  ⇒ 1_000_000 元
//	大   单  50.25 − 20.25 = 30.0 万元 ⇒   300_000 元
//	中   单  10.0 − 40.0 = −30.0 万元  ⇒  −300_000 元
//	小   单   1.0 −  1.0 =    0.0 万元 ⇒        0 元（真 0，不是缺失）
var (
	moneyflowFullFields = []string{
		"ts_code", "trade_date",
		"buy_elg_amount", "sell_elg_amount",
		"buy_lg_amount", "sell_lg_amount",
		"buy_md_amount", "sell_md_amount",
		"buy_sm_amount", "sell_sm_amount",
	}
	moneyflowFullItems = [][]any{
		{"600519.SH", "20260930", 100.5, 0.5, 50.25, 20.25, 10.0, 40.0, 1.0, 1.0},
	}
)

func moneyflowDay(t *testing.T, fields []string, items [][]any) []MoneyflowTierNet {
	t.Helper()
	c := newMoneyflowStubClient(t, moneyflowResp(fields, items), nil)
	rows, err := c.FetchMoneyflowTiers(context.Background(), "600519.SH", "20260930", "20260930")
	require.NoError(t, err)
	return rows
}

// —— 腿 2 + 腿 3：完整响应下的手算对拍（含单位与「真 0 vs 缺失」）——

func TestNormalizeMoneyflow_ValuesAreHandComputedYuanNotWanYuan(t *testing.T) {
	rows := moneyflowDay(t, moneyflowFullFields, moneyflowFullItems)
	require.Len(t, rows, 1)
	r := rows[0]

	assert.Equal(t, "600519.SH", r.Symbol)
	assert.Equal(t, time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC), r.TradeDate)

	require.NotNil(t, r.SuperNet)
	assert.InDelta(t, 1_000_000.0, *r.SuperNet, 1e-6,
		"100.5−0.5 = 100 万元 ⇒ 1_000_000 元：单位换算在同步边界做完，库里是元")
	require.NotNil(t, r.LargeNet)
	assert.InDelta(t, 300_000.0, *r.LargeNet, 1e-6)
	require.NotNil(t, r.MediumNet)
	assert.InDelta(t, -300_000.0, *r.MediumNet, 1e-6, "卖大于买必须是负数，不许被 abs 或截成 0")

	require.NotNil(t, r.SmallNet, "买卖恰好相等是**真 0**，不是缺失 ⇒ 不许落成 NULL")
	assert.InDelta(t, 0.0, *r.SmallNet, 1e-9)
}

// TestNormalizeMoneyflow_TiersNetFromSameTierOnly 钉「不硬凑」的另一半：
// 每档净额只由**本档**买卖决定，不掺其它档。
// 做法是让四档各差一个数量级，任何串档都会让四条断言同时对不上。
func TestNormalizeMoneyflow_TiersNetFromSameTierOnly(t *testing.T) {
	items := [][]any{{
		"000001.SZ", "20261009",
		9_000.0, 1.0, // elg ⇒ 8999 万元
		800.0, 2.0, // lg  ⇒ 798 万元
		70.0, 3.0, // md  ⇒ 67 万元
		6.0, 4.0, // sm  ⇒ 2 万元
	}}
	rows := moneyflowDay(t, moneyflowFullFields, items)
	require.Len(t, rows, 1)
	r := rows[0]

	assert.InDelta(t, 89_990_000.0, derefMoney(t, r.SuperNet), 1e-3, "super_net 只含特大单")
	assert.InDelta(t, 7_980_000.0, derefMoney(t, r.LargeNet), 1e-3, "large_net 只含大单")
	assert.InDelta(t, 670_000.0, derefMoney(t, r.MediumNet), 1e-3, "medium_net 只含中单")
	assert.InDelta(t, 20_000.0, derefMoney(t, r.SmallNet), 1e-3, "small_net 只含小单")
}

func derefMoney(t *testing.T, v *float64) float64 {
	t.Helper()
	require.NotNil(t, v)
	return *v
}

// —— 腿 1：缺列腿（DBT-07 回归形状），本文件的主证据 ——

// TestNormalizeMoneyflow_DroppedTierColumnStillAlignsOthers 是 DBT-07 那个
// 缺陷的回归形状。
//
// Tushare 会把该期**全空**的列整列剔除。这里剔除中间两列（大单买卖），
// 于是响应列序变成 elg, md, sm：
//   - 按列名对位 ⇒ super/md/sm 各就各位，large_net = nil（NULL，可见的缺失）；
//   - 按固定索引硬取 ⇒ md 的钱落进 large_net、sm 的钱落进 medium_net、
//     小单列直接越界 —— 三档错位、一档凭空消失，**这正是 DBT-07 在真库里
//     把 roe 写进 pe 的那次**。
//
// 只有「中间列被剔除」这种输入能让两种实现给出不同答案，所以这条腿必须长这样。
func TestNormalizeMoneyflow_DroppedTierColumnStillAlignsOthers(t *testing.T) {
	fields := []string{
		"ts_code", "trade_date",
		"buy_elg_amount", "sell_elg_amount",
		// buy_lg_amount / sell_lg_amount 被上游剔除（该期全空）
		"buy_md_amount", "sell_md_amount",
		"buy_sm_amount", "sell_sm_amount",
	}
	items := [][]any{{
		"600519.SH", "20260930",
		100.5, 0.5, // elg ⇒ 1_000_000 元
		10.0, 40.0, // md  ⇒ −300_000 元
		1.0, 1.0, // sm  ⇒ 0 元
	}}
	rows := moneyflowDay(t, fields, items)
	require.Len(t, rows, 1, "缺一档不影响其余档入库")
	r := rows[0]

	assert.InDelta(t, 1_000_000.0, derefMoney(t, r.SuperNet), 1e-6, "首列之后的列都右移了，super 仍须是自己的")
	assert.Nil(t, r.LargeNet, "被剔除的那一档是**缺失**，落 NULL；写 0 等于宣称「大单买卖恰好相等」")
	assert.InDelta(t, -300_000.0, derefMoney(t, r.MediumNet), 1e-6,
		"错位实现会把 md 的钱写成 large_net、把 sm 的钱写成 medium_net —— 那两条在这里同时崩")
	assert.NotNil(t, r.SmallNet, "小单列在缺列响应里是第 8 位而不是第 10 位")
	assert.InDelta(t, 0.0, derefMoney(t, r.SmallNet), 1e-9)
}

// TestNormalizeMoneyflow_DroppedIdentityColumnFailsLoud 是缺列腿的反面对照：
// **身份列**被剔除不是「该档没有」，是形状不符契约 ⇒ 当场报错，
// 不返回 0 行让调用方以为这个窗口没有数据。
func TestNormalizeMoneyflow_DroppedIdentityColumnFailsLoud(t *testing.T) {
	for _, gone := range []string{"ts_code", "trade_date"} {
		t.Run(gone, func(t *testing.T) {
			var fields []string
			var item []any
			fields = append(fields, "ts_code", "trade_date")
			item = append(item, "600519.SH", "20260930")
			for _, name := range []string{"buy_elg_amount", "sell_elg_amount"} {
				fields = append(fields, name)
			}
			item = append(item, 100.5, 0.5)
			for _, name := range []string{"buy_lg_amount", "sell_lg_amount"} {
				fields = append(fields, name)
			}
			item = append(item, 50.25, 20.25)
			for _, name := range []string{"buy_md_amount", "sell_md_amount"} {
				fields = append(fields, name)
			}
			item = append(item, 10.0, 40.0)
			for _, name := range []string{"buy_sm_amount", "sell_sm_amount"} {
				fields = append(fields, name)
			}
			item = append(item, 1.0, 1.0)

			// 摘掉要测的那一列（列名与值成对删除，保持响应自洽）。
			for i, f := range fields {
				if f == gone {
					fields = append(append([]string{}, fields[:i]...), fields[i+1:]...)
					item = append(append([]any{}, item[:i]...), item[i+1:]...)
					break
				}
			}
			c := newMoneyflowStubClient(t, moneyflowResp(fields, [][]any{item}), nil)
			_, err := c.FetchMoneyflowTiers(context.Background(), "600519.SH", "20260930", "20260930")
			require.Error(t, err, "身份列 %s 被剔除必须报错，不许当成「无数据」", gone)
			assert.Contains(t, err.Error(), gone, "报错要点名缺的是哪一列")
			assert.Contains(t, err.Error(), "moneyflow")
		})
	}
}

// —— 缺失≠0 的单侧腿 ——

func TestNormalizeMoneyflow_OneSidedTierIsNilNotZero(t *testing.T) {
	// 特大单只有买入侧有值、卖出侧为 null ⇒ 净额不可得。
	// 拿 0 顶替卖出侧 = 宣称「卖出为 0」，那是编数据。
	items := [][]any{{
		"600519.SH", "20260930",
		100.5, nil, // elg 缺一侧
		50.25, 20.25,
		10.0, 40.0,
		1.0, 1.0,
	}}
	rows := moneyflowDay(t, moneyflowFullFields, items)
	require.Len(t, rows, 1)
	r := rows[0]

	assert.Nil(t, r.SuperNet, "买或卖缺一侧 ⇒ 该档净额是缺失，不是把缺的一侧当 0 去减")
	assert.InDelta(t, 300_000.0, derefMoney(t, r.LargeNet), 1e-6, "其余档照常入库")
	assert.InDelta(t, -300_000.0, derefMoney(t, r.MediumNet), 1e-6)
	assert.NotNil(t, r.SmallNet)
}

// —— 脏值 / 全跳过 fail-loud ——

func TestNormalizeMoneyflow_DirtyValueSkipsRowButNotOthers(t *testing.T) {
	items := [][]any{
		{"600519.SH", "20260930", 100.5, 0.5, 50.25, 20.25, 10.0, 40.0, 1.0, 1.0},
		{"000001.SZ", "20260930", 100.5, "N/A", 50.25, 20.25, 10.0, 40.0, 1.0, 1.0}, // 脏：不可解析
	}
	rows := moneyflowDay(t, moneyflowFullFields, items)
	require.Len(t, rows, 1, "脏值那一行不进库，其余行照常")
	assert.Equal(t, "600519.SH", rows[0].Symbol)
}

func TestFetchMoneyflow_AllRowsDirtyFailsLoud(t *testing.T) {
	items := [][]any{
		{"600519.SH", "20260930", 100.5, "N/A", 50.25, 20.25, 10.0, 40.0, 1.0, 1.0},
		{"000001.SZ", "not-a-date", 100.5, 0.5, 50.25, 20.25, 10.0, 40.0, 1.0, 1.0},
	}
	c := newMoneyflowStubClient(t, moneyflowResp(moneyflowFullFields, items), nil)
	rows, err := c.FetchMoneyflowTiers(context.Background(), "600519.SH", "20260930", "20260930")
	require.Error(t, err, "响应有行却一行都归一不出来 ⇒ 上游形状变故，拒绝以「导入 0 行」伪装成功")
	assert.Nil(t, rows)
	assert.Contains(t, err.Error(), "全部被跳过")
	assert.Contains(t, err.Error(), "moneyflow")
}

// TestFetchMoneyflow_EmptyItemsIsLegitimateZero 钉住「合法的空」与「全跳过」
// 的分界：非交易日 / 该票该区间无记录，上游返回 items=[] ⇒ (nil, nil)，
// **不报错**。选「不报错」而不是 fail-loud 的依据：空 items 是 Tushare 对
// 「这段时间没有数据」的正式回答（同 daily_basic 的既有语义与注释），
// 而全跳过是形状变故 —— 两者混在一起就无法区分节假日与上游改格式。
func TestFetchMoneyflow_EmptyItemsIsLegitimateZero(t *testing.T) {
	c := newMoneyflowStubClient(t, moneyflowResp(moneyflowFullFields, [][]any{}), nil)
	rows, err := c.FetchMoneyflowTiers(context.Background(), "600519.SH", "20260101", "20260101")
	require.NoError(t, err, "空 items 是合法答案，不是错误")
	assert.Empty(t, rows)
}

func TestFetchMoneyflow_CodeNonZeroPropagates(t *testing.T) {
	c := newMoneyflowStubClient(t, `{"code":40203,"msg":"没有权限访问该接口","data":{}}`, nil)
	_, err := c.FetchMoneyflowTiers(context.Background(), "600519.SH", "20260930", "20260930")
	require.Error(t, err, "code!=0 必须上抛（同 tushare.go call 的既有契约），不许静默返回空")
	assert.Contains(t, err.Error(), "40203")
}

// —— 参数校验：在**本地**拦下，不浪费一次上游请求 ——

func TestFetchMoneyflow_RejectsBadParamsWithoutCallingUpstream(t *testing.T) {
	for _, tc := range []struct {
		name             string
		code, start, end string
		wants            string
	}{
		{"empty_ts_code", "", "20260930", "20260930", "ts_code"},
		{"bad_start", "600519.SH", "2026-09-30", "20260930", "start_date"},
		{"bad_end", "600519.SH", "20260930", "20261301", "end_date"},
		{"missing_end", "600519.SH", "20260930", "", "end_date"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var seen []map[string]any
			c := newMoneyflowStubClient(t, moneyflowResp(moneyflowFullFields, moneyflowFullItems), &seen)
			_, err := c.FetchMoneyflowTiers(context.Background(), tc.code, tc.start, tc.end)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wants, "报错要点名是哪个参数")
			assert.Empty(t, seen, "参数在本地就该拦下，一次上游请求都不许发（配额是共享的）")
		})
	}
}

func TestFetchMoneyflow_RequestCarriesCodeDatesAndFields(t *testing.T) {
	var seen []map[string]any
	c := newMoneyflowStubClient(t, moneyflowResp(moneyflowFullFields, moneyflowFullItems), &seen)
	_, err := c.FetchMoneyflowTiers(context.Background(), "600519.SH", "20260901", "20260930")
	require.NoError(t, err)
	require.Len(t, seen, 1)

	assert.Equal(t, "moneyflow", seen[0]["api_name"])
	params, _ := seen[0]["params"].(map[string]any)
	require.NotNil(t, params)
	assert.Equal(t, "600519.SH", params["ts_code"], "参数没到达源 = 作业跑的是别的区间")
	assert.Equal(t, "20260901", params["start_date"])
	assert.Equal(t, "20260930", params["end_date"])

	fields, _ := seen[0]["fields"].(string)
	for _, want := range []string{"ts_code", "trade_date", "buy_elg_amount", "sell_sm_amount"} {
		assert.Contains(t, fields, want)
	}
	assert.NotContains(t, fields, "net_mf_amount",
		"net_mf_amount 与四档买卖之和对不上（2026-10-19 实测 20260930：和为 0.00，该列 60958.84），"+
			"口径无从确证 ⇒ 不请求、不映射；请求了就会诱使后人拿它去填 main_net")
}

// TestFetchMoneyflow_ArchivesRawOnce 钉住「归档免费给你、别自己写」这条：
// call 成功后必须已经把原始响应送进 ingest.raw（class A 的唯一证据坐标）。
func TestFetchMoneyflow_ArchivesRawOnce(t *testing.T) {
	stub := &moneyflowStubStore{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(moneyflowResp(moneyflowFullFields, moneyflowFullItems)))
	}))
	t.Cleanup(srv.Close)
	c := NewTushareClient("test-token", srv.URL, 0, stub, nil)

	_, err := c.FetchMoneyflowTiers(context.Background(), "600519.SH", "20260930", "20260930")
	require.NoError(t, err)
	assert.Equal(t, 1, stub.archived, "原始响应必须经 call 自动归档一次（不是 0 次=丢证据，也不是 2 次=双写）")
}

// TestFetchMoneyflow_PersistIsNotTouchedHere 是本条链最容易踩的坑：
// moneyflow 的写入面是 ETL 的 BulkInsert。如果这个导入面也去 Save，
// 同一份数据就各自存了两处（AGENTS.md §9「绝不让同一份数据在两个位置各自存储」）。
func TestFetchMoneyflow_PersistIsNotTouchedHere(t *testing.T) {
	// moneyflowStubStore 只实现 SaveRawIngest；任何别的 Save 都会打到嵌进去的
	// nil TushareStore 上直接 panic —— 用 recover 把「有没有被打到」变成断言。
	panicked := func() (hit bool) {
		defer func() {
			if r := recover(); r != nil {
				hit = true
			}
		}()
		c := newMoneyflowStubClient(t, moneyflowResp(moneyflowFullFields, moneyflowFullItems), nil)
		_, _ = c.FetchMoneyflowTiers(context.Background(), "600519.SH", "20260930", "20260930")
		return false
	}
	assert.False(t, panicked(),
		"导入面不应调用任何 TushareStore 的 Save —— 落库属 ETL，这里落就是双写")
}

// TestProbeMoneyflow_OnlyTouchesTheChokePoint 钉探测面的边界到底在哪：
// 探测**不 normalize、不写行情表**（moneyflowStubStore 只实现 SaveRawIngest，
// 任何别的 Save 打到嵌进去的 nil TushareStore 上会当场 panic，本用例即失败），
// 但它**会**归档 —— 归档闸在 TushareClient.call 那道唯一入口上
// （pkg/data/tushare_raw.go：一次请求的证据坐标由内容哈希决定，
// 探测也是上游真答过一次，答了什么就该可追溯）。
//
// 这条断言写在这儿是因为容易读反：daily_basic 那边注释说「探测不应产生数据
// 副作用」，指的是**行情数据**，不是 ingest.raw。把 archived 断成 0 就是把
// 归档闸当成数据写入一起关掉了。
func TestProbeMoneyflow_OnlyTouchesTheChokePoint(t *testing.T) {
	stub := &moneyflowStubStore{}
	var seen []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			APIName string         `json:"api_name"`
			Params  map[string]any `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		seen = append(seen, map[string]any{"api_name": req.APIName, "params": req.Params})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(moneyflowResp(moneyflowFullFields, moneyflowFullItems)))
	}))
	t.Cleanup(srv.Close)
	c := NewTushareClient("test-token", srv.URL, 0, stub, nil)

	require.NoError(t, c.ProbeMoneyflow(context.Background(), "600519.SH", "20261016", "20261016"))
	require.Len(t, seen, 1, "探测正好一发请求")
	assert.Equal(t, "moneyflow", seen[0]["api_name"])
	params, _ := seen[0]["params"].(map[string]any)
	assert.Equal(t, "20261016", params["start_date"], "探测的日期必须真的到达源，否则探的是另一个窗口")
	assert.Equal(t, 1, stub.archived, "归档发生在 call 闸上，探测也不例外（见本用例注释）")
}
