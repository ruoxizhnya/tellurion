package data

// DATA-12 的单位校准腿（纯函数，不碰网络/库）：钉死 index_daily 的
// vol 手 → 股（×100）、amount 千元 → CNY（×1000）两条换算。
//
// 断言用到的两列取自 2026-10-16 真库实测的那一行（沪深300 2026-09-24）：
// 源值 vol=151965787.0、amount=375461676.1428 ⇒ 规范层 15,196,578,700 股 /
// 375,461,676,142.8 元。其余 OHLC 列是自洽的合成占位值（close=4530、pre_close=4510、
// change=20、pct_chg=0.44：20/4510=0.44%），**不是**真行读数、也不参与断言。
// 破坏验证「摘掉换算」时，量级断言（×100 / ×1000）会红。

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// indexDailyResp 造一个 index_daily 响应：字段顺序按 tushare 常见形态。
func indexDailyResp(items [][]any) *TushareResponse {
	return &TushareResponse{
		Data: TushareData{
			Fields: []string{"ts_code", "trade_date", "open", "high", "low", "close",
				"pre_close", "change", "pct_chg", "vol", "amount"},
			Items: items,
		},
	}
}

func TestNormalizeIndexDaily_UnitCalibration(t *testing.T) {
	c := &TushareClient{}
	resp := indexDailyResp([][]any{
		// ts_code, trade_date, open, high, low, close, pre_close, change, pct_chg, vol(手), amount(千元)
		{"000300.SH", "20260924", 4500.0, 4550.0, 4480.0, 4530.0, 4510.0, 20.0, 0.44, 151965787.0, 375461676.1428},
	})

	rows, skipped, err := c.normalizeIndexDaily(resp, "tushare", time.Unix(0, 0).UTC())
	require.NoError(t, err)
	require.Equal(t, 0, skipped)
	require.Len(t, rows, 1)

	got := rows[0]
	assert.Equal(t, "000300.SH", got.Code)
	assert.Equal(t, "2026-09-24", got.TradeDate.Format("2006-01-02"))
	// vol：手 → 股，×sharesPerLot(100)。摘掉换算 ⇒ 实得 1.5e8，量级断言红。
	assert.InDelta(t, 15196578700.0, got.Volume, 1.0)
	// amount：千元 → CNY，×cnyPerAmountUnit(1000)。摘掉换算 ⇒ 实得 3.7e8，量级断言红。
	require.NotNil(t, got.Turnover)
	assert.InDelta(t, 375461676142.8, *got.Turnover, 1.0)
	assert.Equal(t, "tushare", got.Source)
}

func TestNormalizeIndexDaily_MissingRequiredColumnFailsLoud(t *testing.T) {
	c := &TushareClient{}
	// 缺 close / vol 列：整批报错，不静默产出全 0 的假 bar。
	resp := &TushareResponse{
		Data: TushareData{
			Fields: []string{"ts_code", "trade_date", "open"},
			Items:  [][]any{{"000300.SH", "20260924", 4500.0}},
		},
	}
	_, _, err := c.normalizeIndexDaily(resp, "tushare", time.Unix(0, 0).UTC())
	require.Error(t, err, "缺必需列（close/vol）必须整批报错")
}

func TestNormalizeIndexDaily_SkipsRowsMissingCloseOrVol(t *testing.T) {
	c := &TushareClient{}
	resp := indexDailyResp([][]any{
		// 缺 close（nil）——跳过并计数。
		{"000300.SH", "20260924", 4500.0, 4550.0, 4480.0, nil, 4510.0, 20.0, 0.44, 100.0, 200.0},
		// 缺 vol（nil）——跳过并计数。
		{"000300.SH", "20260923", 4500.0, 4550.0, 4480.0, 4530.0, 4510.0, 20.0, 0.44, nil, 200.0},
		// 正常行——入库。
		{"000300.SH", "20260922", 4500.0, 4550.0, 4480.0, 4530.0, 4510.0, 20.0, 0.44, 100.0, 200.0},
	})

	rows, skipped, err := c.normalizeIndexDaily(resp, "tushare", time.Unix(0, 0).UTC())
	require.NoError(t, err)
	assert.Equal(t, 2, skipped, "缺 close / 缺 vol 的行必须被跳过并计数")
	require.Len(t, rows, 1)
	assert.Equal(t, "2026-09-22", rows[0].TradeDate.Format("2006-01-02"))
}
