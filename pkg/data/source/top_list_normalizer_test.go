package source

// TopListNormalizer（DBT-10 落的第二条生产 Normalizer）的单测。
//
// 这道闸的**全部意义**是「一条脏行不许带崩整批写事务」，所以测试的正身是
// 「坏行进不去 + 好行照常进 + 被丢的条数可数」，而不是「好行长什么样」。
//
// 同时如实钉住它**挡不住**的那一种：上游 parseEastmoneyDate 对坏日期返回
// time.Now() 而非零值，所以畸形日期在这一跳是**看不见**的（详见
// top_list_normalizer.go 文件头的登记）。这里把它写成一条显式断言，
// 是为了让「这道闸覆盖了日期」这个误读当场失败 —— 护栏的边界也要有证据。

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func topListItem(symbol string, tradeTime time.Time, data map[string]interface{}) DataItem {
	return DataItem{Symbol: symbol, TradeTime: tradeTime, Data: data}
}

func topListGoodData() map[string]interface{} {
	return map[string]interface{}{
		"name":        "样例股份",
		"net_buy":     1234.56,
		"buy_amount":  7000.0,
		"sell_amount": 5765.44,
		"turnover":    987654.0,
		"reason":      "日涨幅偏离值达7%的证券",
	}
}

func TestTopListNormalizer_PassesWellFormedRowThroughUnchanged(t *testing.T) {
	day := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	src := topListItem("600519.SH", day, topListGoodData())

	p := TopListNormalizer(src, "eastmoney_toplist", DataTypeTopList)
	require.NotNil(t, p.Data, "形状完好的行不该被丢弃")
	assert.Equal(t, "600519.SH", p.Symbol)
	assert.Equal(t, day, p.TradeTime)
	assert.Equal(t, "eastmoney_toplist", p.Source, "source 列要记真正供数的那扇门")
	assert.Equal(t, DataTypeTopList, p.DataType, "DataType 决定落哪张表，必须透传")

	// 不复制、不改写：Data 就是源那份 map（改动它等于在 Normalizer 里造数据）。
	assert.Equal(t, "样例股份", p.Data["name"])
	assert.Equal(t, 1234.56, p.Data["net_buy"])
	assert.Equal(t, "日涨幅偏离值达7%的证券", p.Data["reason"])

	// 「同一个 map」用写入来证（断言 map 指针相等在 testify 里不可用）：
	// 改到 p.Data 上，src.Data 必须同步变 —— 有副本的话就不会。
	p.Data["__probe__"] = true
	assert.Equal(t, true, src.Data["__probe__"],
		"出现副本就说明有人开始在这里改数据了；不写就不需要副本")
	delete(p.Data, "__probe__")
}

func TestTopListNormalizer_DropsRowsWithoutIdentity(t *testing.T) {
	day := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		item        DataItem
		wantDropped bool
	}{
		"empty_symbol": {
			item:        topListItem("", day, topListGoodData()),
			wantDropped: true,
		},
		"zero_trade_time": {
			item:        topListItem("600519.SH", time.Time{}, topListGoodData()),
			wantDropped: true,
		},
		"missing_name_key": {
			item: topListItem("600519.SH", day, map[string]interface{}{
				"net_buy": 1.0,
			}),
			wantDropped: true,
		},
		"empty_name": {
			item: topListItem("600519.SH", day, map[string]interface{}{
				"name": "", "net_buy": 1.0,
			}),
			wantDropped: true,
		},
		"whitespace_name": {
			item: topListItem("600519.SH", day, map[string]interface{}{
				"name": "   ", "net_buy": 1.0,
			}),
			wantDropped: true,
		},
		"name_wrong_type": {
			// stringFromMap 会把非 string 折成 "%v" 文本，所以真源给数字名不会
			// 走到这条路；这道判据防的是**别的生产者**接同一条链时把 name 写成
			// 非字符串（那既不满足 NOT NULL 的语义，也不该被当成名字）。
			item: topListItem("600519.SH", day, map[string]interface{}{
				"name": 123, "net_buy": 1.0,
			}),
			wantDropped: true,
		},
		"nil_data": {
			item:        DataItem{Symbol: "600519.SH", TradeTime: day, Data: nil},
			wantDropped: true,
		},
		"well_formed": {
			item:        topListItem("600519.SH", day, topListGoodData()),
			wantDropped: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p := TopListNormalizer(tc.item, "eastmoney_toplist", DataTypeTopList)
			if tc.wantDropped {
				assert.Nil(t, p.Data,
					"%s：这种行必须被丢弃 —— 它是 NOT NULL/冲突键列的缺件，留着会把整批写事务一起带崩", name)
				return
			}
			assert.NotNil(t, p.Data, "%s：形状完好的行不许被丢", name)
		})
	}
}

// TestTopListNormalizer_DropIsCountedNotSilent 钉住「丢弃是可见计数」这条：
// ProcessResult.Skipped 靠的就是 Data==nil 这个信号。批里坏一行，好行必须
// 照常活下来 —— 否则一条脏数据等于毁掉那一天整张榜单。
func TestTopListNormalizer_DropIsCountedNotSilent(t *testing.T) {
	day := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	items := []DataItem{
		topListItem("600001.SH", day, topListGoodData()),
		topListItem("", day, topListGoodData()),                               // 无身份
		topListItem("600003.SH", time.Time{}, topListGoodData()),              // 无日期
		topListItem("600004.SH", day, map[string]interface{}{"net_buy": 1.0}), // 无名字
		topListItem("600005.SH", day, topListGoodData()),
	}

	var kept int
	for _, it := range items {
		if p := TopListNormalizer(it, "eastmoney_toplist", DataTypeTopList); p.Data != nil {
			kept++
			// 每一行都要带上自己的 symbol 与日期（map 复用会让所有行同键，
			// 那是 DeduplicateWithCount 之后仍然会撞的一类脏装配）。
			assert.Equal(t, it.Symbol, p.Symbol)
		}
	}
	assert.Equal(t, 2, kept, "5 行里 3 行缺件 ⇒ 恰好 2 行入库，坏行不牵连好行")
}

// TestTopListNormalizer_CannotCatchUpstreamBadDate 是本护栏的**边界声明**。
//
// 上游 parseEastmoneyDate(eastmoney_sectors_adapter.go:661) 对畸形/缺失
// TRADE_DATE 返回 time.Now().UTC()，不是零值。所以「日期坏掉」在这一跳
// 长得跟「今天的榜单」一模一样：TradeTime 非零 ⇒ 闸门放行，落库的
// trade_date 是今天。
//
// 这条断言不是为了证明它坏（那是另一单的活），而是为了**阻止误读**：
// 谁要是把上面的 zero_trade_time 判据理解成「畸形日期已被挡住」，
// 这条测试会当场红。改动 parseEastmoneyDate 让它返回零值时，
// 这条会失败并强迫改的人回来更新这里的登记。
func TestTopListNormalizer_CannotCatchUpstreamBadDate(t *testing.T) {
	// 模拟上游把坏日期折成「现在」之后的产物 —— 这正是链上真实会看到的形状。
	malformedButNonZero := DataItem{
		Symbol:    "600519.SH",
		TradeTime: time.Now().UTC(),
		Data:      topListGoodData(),
	}
	p := TopListNormalizer(malformedButNonZero, "eastmoney_toplist", DataTypeTopList)
	assert.NotNil(t, p.Data,
		"如实记录：非零但错误的日期会被放行。真正的洞在 parseEastmoneyDate 返回 time.Now() 那行，"+
			"不在这道闸 —— 别把这道闸读成日期校验")
	assert.False(t, p.TradeTime.IsZero())
}

// TestTopListNormalizer_DoesNotInventColumns 钉「不硬凑」：
// 适配器在 Data 里放了 close_price / change_pct，而 top_list 的 TableMapper
// 列集**不写**这两列（DDL 有、mapper 无）。Normalizer 不去帮忙把它们
// 挪进别的列 —— 谁该落库由列集决定，不由谁手里有值决定。
func TestTopListNormalizer_DoesNotInventColumns(t *testing.T) {
	day := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	withExtras := topListItem("600519.SH", day, map[string]interface{}{
		"name":         "样例股份",
		"net_buy":      1.0,
		"close_price":  17.5,
		"change_pct":   2.3,
		"explain":      "原始说明",
		"data_version": 7,
	})
	p := TopListNormalizer(withExtras, "eastmoney_toplist", DataTypeTopList)
	require.NotNil(t, p.Data)

	assert.Equal(t, 17.5, p.Data["close_price"], "原样透传，不改名")
	assert.Equal(t, 2.3, p.Data["change_pct"], "原样透传，不改名")
	assert.Equal(t, "原始说明", p.Data["explain"], "不把它搬去 reason（那是两个不同的源字段）")
	_, hasReason := p.Data["reason"]
	assert.False(t, hasReason, "源没给 reason 就不许由 explain/别的列拼一个出来")
}
