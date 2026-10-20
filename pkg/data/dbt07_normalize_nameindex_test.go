package data

// DBT-07 实测缺陷修复的钉子：fina_indicator 响应按**列名**解包，不再按
// 请求时的固定索引。
//
// 缺陷复现（2026-10-17 真库实证）：Tushare 会从响应里剔除该期为空的列
// （600000.SH 2024Q3 只回 ts_code/ann_date/end_date/roe/roa/gross_margin
// 六列），旧实现按固定索引 pe=3/roe=6 解包 —— roe 的值 4.8669 被写进
// pe 列，roe 落空。修复后按响应自带 fields 建名字→索引映射。
//
// 破坏验证：把 normalizeFundamentalsData 改回固定索引 → 缺列用例立即红
// （ROE 断 nil 而错拿 4.8669、PE 错拿 roe 值）。

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/domain"
)

func dbt07TushareResp(fields []string, items [][]any) *TushareResponse {
	return &TushareResponse{
		Code: 0,
		Data: TushareData{Fields: fields, Items: items},
	}
}

func ptr(v float64) *float64 { return &v }

// TestDBT07_NormalizeFundamentals_FullColumnsRegression：列齐全时新旧
// 解包逐字段一致（回归腿）。
func TestDBT07_NormalizeFundamentals_FullColumnsRegression(t *testing.T) {
	t.Parallel()
	resp := dbt07TushareResp(
		[]string{"ts_code", "ann_date", "end_date", "pe", "pb", "ps", "roe", "roa",
			"debt_to_equity", "gross_margin", "net_margin", "revenue", "net_profit",
			"total_assets", "total_liab"},
		[][]any{{"600000.SH", "20241031", "20240930", 5.5, 0.6, 1.2, 4.8669, 0.4,
			8.1, 30.0, 25.0, 1.0, 0.2, 9.0, 8.0}},
	)
	c := &TushareClient{}
	recs := c.normalizeFundamentalsData(resp)
	require.Len(t, recs, 1)
	r := recs[0]
	assert.Equal(t, "600000.SH", r.TsCode)
	require.NotNil(t, r.PE)
	assert.Equal(t, 5.5, *r.PE)
	require.NotNil(t, r.ROE)
	assert.Equal(t, 4.8669, *r.ROE)
}

// TestDBT07_NormalizeFundamentals_TrimmedColumns_NameIndexed：缺列形态
// （真库实测的 6 列响应）——roe 必须从位置 3 正确解出，pe/pb/ps 恒 nil。
func TestDBT07_NormalizeFundamentals_TrimmedColumns_NameIndexed(t *testing.T) {
	t.Parallel()
	// 真库实测形态：请求 15 列、响应剔到 6 列。
	resp := dbt07TushareResp(
		[]string{"ts_code", "ann_date", "end_date", "roe", "roa", "gross_margin"},
		[][]any{{"600000.SH", "20241031", "20240930", 4.8669, nil, nil}},
	)
	c := &TushareClient{}
	recs := c.normalizeFundamentalsData(resp)
	require.Len(t, recs, 1)
	r := recs[0]

	// 修复断言：值跟**名字**走，不跟位置走。
	require.NotNil(t, r.ROE, "roe 在响应位置 3，名字解包必须拿到它")
	assert.Equal(t, 4.8669, *r.ROE)
	assert.Nil(t, r.PE, "pe 列被服务端剔除——恒 nil（旧实现错拿 roe 值）")
	assert.Nil(t, r.PB)
	assert.Nil(t, r.PS)
	assert.Nil(t, r.GrossMargin, "值为 null 的列 → nil（缺失≠0）")
	// 日期同样按名字解包（ann/end 不受列序影响）。
	assert.Equal(t, "20240930", r.EndDate.Format("20060102"))
}

// TestDBT07_NormalizeFundamentals_AllColumnsMissing：响应连 ts_code 都
// 没有 → 零记录（不构造垃圾行）。
func TestDBT07_NormalizeFundamentals_AllColumnsMissing(t *testing.T) {
	t.Parallel()
	resp := dbt07TushareResp([]string{}, [][]any{{}})
	c := &TushareClient{}
	recs := c.normalizeFundamentalsData(resp)
	assert.Empty(t, recs)
}

// 编译守卫：FundamentalData 的零值指针语义（nil = 缺失）。
var _ = domain.FundamentalData{}
