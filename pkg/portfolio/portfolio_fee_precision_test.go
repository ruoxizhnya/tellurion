package portfolio

// CORE-FEE-ROUNDING-ASSERT（D9=a）：费用算术的分精度断言，**不改 float64 类型**。
//
// 现场：费用三件套（佣金 / 过户费 / 印花税）与总额都是 float64，`ComputeFees`
// 不做任何取整；此前的用例要么用 `InDelta(…, 0.001)` 比总额，要么只断言单项
// 公式，**没有任何判据能发现「某一项被悄悄舍入到分」这类一分钱级漂移**。
//
// 本文件补三类断言：
//  1. 分项之和 ≡ 总额（逐位相等），且总额 ≡ 闭式公式的独立重算；
//  2. 分项本身 ≡ `tradeValue × 费率` 的逐位值 —— 任何 Round/Trunc 到分都会破；
//  3. 最低佣金是**恰好** MinCommission（不是 4.9999999），且小额单也走它。
//
// 另带「夹具自检」：对抗样例必须真的能区分「未取整」与「截断到分」，
// 否则这条用例只是在验证自己写了个常数。

import (
	"math"
	"testing"

	"github.com/ruoxizhnya/tellurion-contracts/fees"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func truncToCents(v float64) float64 { return math.Trunc(v*100) / 100 }

// TestComputeFees_ComponentsSumToTotalBitExactly：Total() 必须逐位等于三项之和，
// 且等于用费率独立重算的闭式。Total() 若被改成「取整后再相加」，这条先红。
func TestComputeFees_ComponentsSumToTotalBitExactly(t *testing.T) {
	f := fees.DefaultAShareFees()

	for _, tc := range []struct {
		name     string
		value    float64
		isSell   bool
		wantSum  float64
		wantComm float64
	}{
		{"小额买入走最低佣金", 1000, false, f.MinCommission + 1000*f.TransferFeeRate, f.MinCommission},
		{"整额卖出", 100_000, true, 100_000*f.CommissionRate + 100_000*f.TransferFeeRate + 100_000*f.StampTaxRate, 100_000 * f.CommissionRate},
		{"零头卖出", 33335, true, 33335*f.CommissionRate + 33335*f.TransferFeeRate + 33335*f.StampTaxRate, 33335 * f.CommissionRate},
		{"一分钱买入", 0.01, false, f.MinCommission + 0.01*f.TransferFeeRate, f.MinCommission},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			fb := ComputeFees(tc.value, tc.isSell, f)

			assert.Equal(t, fb.Commission+fb.TransferFee+fb.StampTax, fb.Total(),
				"Total() 必须逐位等于三项之和（不许在总额上单独取整）")
			assert.Equal(t, tc.wantSum, fb.Total(), "总额必须等于闭式重算值")
			assert.Equal(t, tc.wantComm, fb.Commission, "佣金不得被舍入到分")

			// 分项逐位等于「成交额 × 费率」，唯一例外是最低佣金兜底。
			if tc.value*f.CommissionRate >= f.MinCommission {
				assert.Equal(t, tc.value*f.CommissionRate, fb.Commission,
					"佣金必须逐位等于 notional×rate —— 出现 10.00 而闭式是 10.0005 即为取整漂移")
			} else {
				assert.Equal(t, f.MinCommission, fb.Commission, "低于下限时必须**恰好**是 MinCommission")
			}
			assert.Equal(t, tc.value*f.TransferFeeRate, fb.TransferFee, "过户费不得被舍入到分")
			if tc.isSell {
				assert.Equal(t, tc.value*f.StampTaxRate, fb.StampTax, "印花税不得被舍入到分")
			} else {
				assert.Zero(t, fb.StampTax, "买入不得收印花税")
			}
		})
	}
}

// TestComputeFees_HalfCentBoundaryIsDetectable 用一个「差一分钱」的对抗样例，
// 并**自检它能区分未取整与截断**：把三项各自截断到分后，总额至少差 1 分。
// 没有这条自检，夹具可能挑了个怎么取整都一样的数，用例就退化成常数断言。
func TestComputeFees_HalfCentBoundaryIsDetectable(t *testing.T) {
	f := fees.DefaultAShareFees()

	// 33335 元卖出：佣金 10.0005、过户费 0.33335、印花税 16.6675。
	const tradeValue = 33335.0
	fb := ComputeFees(tradeValue, true, f)

	truncated := truncToCents(fb.Commission) + truncToCents(fb.TransferFee) + truncToCents(fb.StampTax)

	// 夹具自检：这个数必须真的在一分钱量级上可区分截断（差 1.1 分）。
	// 四舍五入只挪 0.14 分，但下面逐位相等的断言照样能抓到——只要分项变了就红。
	require.Greater(t, math.Abs(fb.Total()-truncated), 0.01,
		"夹具失效：截断到分与未取整的差不足 1 分，这条用例抓不住一分钱漂移")

	// 实现必须保持未取整的分项（期望值按与生产相同的算式推导，逐位可比）。
	assert.Equal(t, tradeValue*f.CommissionRate, fb.Commission,
		"佣金被取了分：10.0005 变成 10.00")
	assert.Equal(t, tradeValue*f.TransferFeeRate, fb.TransferFee,
		"过户费被取了分：0.33335 变成 0.33")
	assert.Equal(t, tradeValue*f.StampTaxRate, fb.StampTax,
		"印花税被取了分：16.6675 变成 16.66")
	assert.Equal(t, tradeValue*f.CommissionRate+tradeValue*f.TransferFeeRate+tradeValue*f.StampTaxRate,
		fb.Total(), "总额被取了分：27.00135 变成 26.99 或 27.00")
}

// TestComputeFees_NeverQuantizesAcrossRange 对一段确定性的成交额区间逐点比对
// 「分项 ≡ 闭式」，任何一处 Round/Trunc（哪怕只在某个分支）都会红。
func TestComputeFees_NeverQuantizesAcrossRange(t *testing.T) {
	f := fees.DefaultAShareFees()

	for i := 1; i <= 500; i++ {
		value := float64(i) * 137.13
		fb := ComputeFees(value, true, f)

		wantComm := math.Max(value*f.CommissionRate, f.MinCommission)
		assert.Equal(t, wantComm, fb.Commission, "tradeValue=%v 的佣金被改写", value)
		assert.Equal(t, value*f.TransferFeeRate, fb.TransferFee, "tradeValue=%v 的过户费被改写", value)
		assert.Equal(t, value*f.StampTaxRate, fb.StampTax, "tradeValue=%v 的印花税被改写", value)
		assert.Equal(t, wantComm+value*f.TransferFeeRate+value*f.StampTaxRate, fb.Total(),
			"tradeValue=%v 的总额与分项和对不上", value)
	}
}

// TestComputeFees_NonPositiveValueIsExactlyZero 钉住 fail-safe 分支不产生负费用，
// 也不产生 -0.0 这种会让「差一分钱」比较失真的值。
func TestComputeFees_NonPositiveValueIsExactlyZero(t *testing.T) {
	f := fees.DefaultAShareFees()

	for _, value := range []float64{0, -1, -100000} {
		fb := ComputeFees(value, true, f)
		assert.Zero(t, fb.Commission)
		assert.Zero(t, fb.TransferFee)
		assert.Zero(t, fb.StampTax)
		assert.False(t, math.Signbit(fb.Total()), "tradeValue=%v 产生 -0.0", value)
	}
}
