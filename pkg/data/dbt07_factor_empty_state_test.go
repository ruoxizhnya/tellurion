package data

// DBT-07 ③：value/quality 因子在 fundamentals 空态下的行为钉子。
//
// 处置结论（2026-10-17 实测）：stock_fundamentals 长期为 0 行（此前口径
// 「Tushare 免费额度不可得」——2026-10-17 复测 fina_indicator 接口**可用**，
// 灌数腿见 cmd/data/dbt07_fundamentals_fill_test.go）。空态期间 value
// （1/pe，公式型）与 quality（roe，叶子型）不得崩、不得写垃圾行：
//
//   - 现行语义 = **跳过**（不 fail-loud）：空快照下 ComputeFactorByName
//     返回 nil 错误、factor_cache 零写入、服务端 Warn 一条（saveFormulaFactor
//     的 "No factor values computed" 分支）。选股/组合侧看到的仍是
//     factor_cache 无该因子行 = 缺失，与「缺失≠0」的全局口径一致。
//   - 对照腿：fundamentals 有数据时同一条计算路径产出非空 entries——
//     证明「跳过」是数据驱动，不是计算路径坏了（不然空态绿、有数也绿
//     才算钉住了行为差异）。
//
// 破坏验证：把 saveFormulaFactor 的空 rawValues 分支改成 fail（返回
// error），本测试的空态腿立即红；改回跳过即绿（开发期已实测）。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
)

func dbt07Date() time.Time { return time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC) }

// TestDBT07_ValueQuality_EmptyFundamentals_SkipsNotFails 钉住空态语义：
// 两因子都返回 nil 错误、零落库。
func TestDBT07_ValueQuality_EmptyFundamentals_SkipsNotFails(t *testing.T) {
	t.Parallel()

	for _, name := range []domain.FactorType{domain.FactorValue, domain.FactorQuality} {
		store := &d3MockStore{
			ohlcv: closeSeries("600000.SH", dbt07Date(), 40, 10, 0.1),
			// fundamentals 刻意为空 —— stock_fundamentals 0 行的空态。
		}
		fc := NewFactorComputer(store)

		err := fc.ComputeFactorByName(context.Background(), name, dbt07Date())
		require.NoError(t, err, "%s 在 fundamentals 空态下不得报错（跳过语义）", name)
		assert.Empty(t, store.saved,
			"%s 空态下不得写任何 factor_cache 行（缺失≠垃圾）", name)
	}
}

// TestDBT07_ValueQuality_WithData_ProducesEntries 对照腿：同一条计算
// 路径在有数据时产出非空 entries —— 「跳过」是数据驱动的行为。
func TestDBT07_ValueQuality_WithData_ProducesEntries(t *testing.T) {
	t.Parallel()

	pe := 20.0
	roe := 12.5
	store := &d3MockStore{
		ohlcv: closeSeries("600000.SH", dbt07Date(), 40, 10, 0.1),
		fundamentals: []domain.FundamentalData{
			{TsCode: "600000.SH", PE: &pe, ROE: &roe},
		},
	}
	fc := NewFactorComputer(store)

	for _, name := range []domain.FactorType{domain.FactorValue, domain.FactorQuality} {
		store.saved = nil
		err := fc.ComputeFactorByName(context.Background(), name, dbt07Date())
		require.NoError(t, err, "%s 有数据时应正常计算", name)
		assert.NotEmpty(t, store.saved, "%s 有数据时应产出 factor_cache 行", name)
	}
}
