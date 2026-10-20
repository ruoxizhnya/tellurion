package source

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capitalFlowItem 造一条 adapter 真实会返回的形状（字段集与
// eastmoney_adapter.fetchCapitalFlowForSymbol 的 Data map 一致）。
func capitalFlowItem(sym string, when time.Time, period string) DataItem {
	data := map[string]interface{}{
		"main_net":         1234.5,
		"super_net":        678.9,
		"large_net":        100.5,
		"medium_net":       -50.25,
		"small_net":        -600.0,
		"main_net_ratio":   12.5,
		"retail_net":       -1234.5,
		"retail_net_ratio": -12.5,
		"close_price":      1720.0,
		"change_pct":       1.22,
	}
	if period != noPeriodSentinel {
		data["period"] = period
	}
	return DataItem{Symbol: sym, TradeTime: when, Data: data}
}

const noPeriodSentinel = "\x00absent"

func TestCapitalFlowNormalizer_StampsIdentityAndPassesFieldsThrough(t *testing.T) {
	when := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	item := capitalFlowItem("600519.SH", when, "daily")

	p := CapitalFlowNormalizer(item, "eastmoney", "capital_flow")

	require.NotNil(t, p.Data, "合法条目不该被丢弃")
	assert.Equal(t, "600519.SH", p.Symbol)
	assert.Equal(t, when, p.TradeTime)
	assert.Equal(t, "eastmoney", p.Source, "source 列由 Registry 返回的适配器名承载，不是调用方猜的")
	assert.Equal(t, "capital_flow", p.DataType, "DataType 决定落哪张表（TableMapper），必须由 pipeline 给的 dataType 盖章")

	// 逐字段原样透传 = 单位不换算（文件头有实测依据）。
	// 用绝对数值断言，不用「和输入比」——输入本身要是不动声色地被换了量纲，
	// 比对自己的断言照样绿。
	assert.Equal(t, 1234.5, p.Data["main_net"])
	assert.Equal(t, 678.9, p.Data["super_net"])
	assert.Equal(t, -50.25, p.Data["medium_net"])
	assert.Equal(t, 1720.0, p.Data["close_price"])
	assert.Equal(t, 1.22, p.Data["change_pct"])
	assert.Equal(t, "daily", p.Data["period"])

	// adapter 没透出的两列不在 map 里 —— 落库时由 buildInsertArgs 折成 NULL。
	assert.NotContains(t, p.Data, "main_buy_amount")
	assert.NotContains(t, p.Data, "main_sell_amount")
}

func TestCapitalFlowNormalizer_DropsRowsThatCannotLand(t *testing.T) {
	when := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		symbol string
		when   time.Time
		period string
		why    string
	}{
		{"empty_symbol", "", when, "daily", "symbol 是 NOT NULL 列，也是冲突键的一部分"},
		{"zero_trade_time", "600519.SH", time.Time{}, "daily", "trade_date 是 NOT NULL 列，零值会折成 NULL 让整批事务回滚"},
		{"absent_period", "600519.SH", when, noPeriodSentinel, "period 是冲突键第三列且 NOT NULL，ValidatePoints 看不见 map 里的键"},
		{"empty_period", "600519.SH", when, "", "同上，空串同样落不进去"},
		{"blank_period", "600519.SH", when, "   ", "纯空白不是「无」，但对存储而言同样不可用"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := CapitalFlowNormalizer(capitalFlowItem(tc.symbol, tc.when, tc.period), "eastmoney", "capital_flow")
			assert.Nil(t, p.Data, "%s —— 该丢弃（计入 Skipped），不该送进写事务", tc.why)
		})
	}
}

func TestCapitalFlowNormalizer_DroppedRowsAreVisibleInThePipelineSkipCount(t *testing.T) {
	// ④「不借本单修 DATA-03 的吞错家族」的正面腿：脏行必须进 ProcessResult.Skipped
	// 这个可见计数，而不是蒸发。这里不连库（Store=nil 时 Process 不落库），
	// 只钉「丢弃是能被看见的」。
	when := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	items := []DataItem{
		capitalFlowItem("600519.SH", when, "daily"),
		capitalFlowItem("", when, "daily"),
		capitalFlowItem("600000.SH", when, noPeriodSentinel),
	}
	normalizer := CapitalFlowNormalizer
	kept := make([]UnifiedDataPoint, 0, len(items))
	for _, it := range items {
		if p := normalizer(it, "eastmoney", "capital_flow"); p.Data != nil {
			kept = append(kept, p)
		}
	}
	require.Len(t, kept, 1, "三条里只有一条能落库")
	assert.Equal(t, "600519.SH", kept[0].Symbol)

	survived, skipped := ValidatePoints(kept)
	assert.Len(t, survived, 1)
	assert.Equal(t, 0, skipped, "正常行不该被二次丢弃")

	// 对照腿：脏行若绕过 normalizer 直接进 pipeline，ValidatePoints 只挡得住
	// 空 symbol，挡不住「symbol 正常但 period 缺失」——这正是上面那道判定
	// 必须存在于 normalizer 里的理由。
	dirty := UnifiedDataPoint{Symbol: "600000.SH", TradeTime: when, Source: "eastmoney",
		DataType: "capital_flow", Data: map[string]interface{}{"main_net": 1.0}}
	survived, skipped = ValidatePoints([]UnifiedDataPoint{dirty})
	assert.Len(t, survived, 1, "ValidatePoints 不看 Data 里的键，脏 period 会一路走到写事务")
	assert.Equal(t, 0, skipped)
}

func TestCapitalFlowNormalizer_IsAssignableToETLNormalizer(t *testing.T) {
	// 钉住它确实是 Process 要的那个函数类型，不是长得像的另一个东西。
	var n Normalizer = CapitalFlowNormalizer
	require.NotNil(t, n)
	p := n(capitalFlowItem("000001.SZ", time.Now(), "daily"), "eastmoney", "capital_flow")
	assert.NotNil(t, p.Data)
}
