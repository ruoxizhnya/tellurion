package cache

// P1-16 (Sprint 6, ODR-013 CQ-001, ADR-020):
// FactorCacheAccessor 独立单元测试 — 不依赖 Engine / PostgresStore。
//
// 验证点：
//   1. New 后 Len() == 0
//   2. Load 替换底层 map；Load(nil) 清空
//   3. Get 命中 / 未命中 / cache 未初始化 三态
//   4. 并发 Load / Get 不触发 race

import (
	"math"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"

	cdom "github.com/ruoxizhnya/tellurion-contracts/domain"
)

func newTestFactorCache() *FactorCacheAccessor {
	return NewFactorCacheAccessor(zerolog.New(nil))
}

func TestFactorCache_NewIsEmpty(t *testing.T) {
	f := newTestFactorCache()
	assert.Equal(t, 0, f.Len(), "fresh cache should have no factors")
}

func TestFactorCache_LoadReplacesData(t *testing.T) {
	f := newTestFactorCache()
	d := time.Date(2023, 1, 3, 0, 0, 0, 0, time.UTC)
	data := map[cdom.FactorType]map[time.Time]map[string]float64{
		cdom.FactorMomentum: {
			d: {"600000.SH": 1.5},
		},
	}
	f.Load(data)
	assert.Equal(t, 1, f.Len())

	// Load(nil) clears
	f.Load(nil)
	assert.Equal(t, 0, f.Len(), "Load(nil) must reset to empty (Len counts unique factor types)")
}

func TestFactorCache_Get(t *testing.T) {
	f := newTestFactorCache()
	d := time.Date(2023, 1, 3, 0, 0, 0, 0, time.UTC)
	f.Load(map[cdom.FactorType]map[time.Time]map[string]float64{
		cdom.FactorMomentum: {d: {"600000.SH": 1.5}},
	})

	// Hit
	z, ok := f.Get(cdom.FactorMomentum, d, "600000.SH")
	assert.True(t, ok)
	assert.Equal(t, 1.5, z)

	// Symbol miss
	_, ok = f.Get(cdom.FactorMomentum, d, "999999.SH")
	assert.False(t, ok)

	// Date miss
	_, ok = f.Get(cdom.FactorMomentum, d.AddDate(0, 0, 1), "600000.SH")
	assert.False(t, ok)

	// Factor type miss
	_, ok = f.Get(cdom.FactorValue, d, "600000.SH")
	assert.False(t, ok)

	// Uninitialized
	g := newTestFactorCache()
	_, ok = g.Get(cdom.FactorMomentum, d, "600000.SH")
	assert.False(t, ok)
}

func TestFactorCache_ConcurrentLoadGet(t *testing.T) {
	// Smoke test: run with `go test -race` to catch unsynchronized access.
	f := newTestFactorCache()
	d := time.Date(2023, 1, 3, 0, 0, 0, 0, time.UTC)

	done := make(chan struct{}, 2)
	go func() {
		for i := 0; i < 1000; i++ {
			data := map[cdom.FactorType]map[time.Time]map[string]float64{
				cdom.FactorMomentum: {d: {"600000.SH": float64(i)}},
			}
			f.Load(data)
		}
		done <- struct{}{}
	}()
	go func() {
		for i := 0; i < 1000; i++ {
			_, _ = f.Get(cdom.FactorMomentum, d, "600000.SH")
			_ = f.Len()
		}
		done <- struct{}{}
	}()
	<-done
	<-done
}

// P1-C: ComputeQuintile 单元测试。
//
// 验证点：
//   1. 边界值：四个阈值 (-0.843 / -0.253 / 0.253 / 0.843) 处的归属，
//      以及阈值上下 epsilon 的归属。
//   2. AllQuintiles：每个 quintile 一个代表值，确认 1-5 全覆盖。
//   3. Extreme：±Inf / ±1e10 / ±100 等极端值，确认不溢出且恒在 [1,5]。

// TestComputeQuintile_Boundaries 验证四个阈值边界处及边界上下 epsilon 的
// quintile 归属。规范定义 (P1-C)：
//
//	q1: z < -0.843
//	q2: -0.843 <= z < -0.253
//	q3: -0.253 <= z < 0.253
//	q4: 0.253 <= z < 0.843
//	q5: z >= 0.843
//
// 即每个阈值本身归入"上侧" quintile（左闭右开，最末段左闭）。
func TestComputeQuintile_Boundaries(t *testing.T) {
	f := newTestFactorCache()
	const eps = 1e-9

	cases := []struct {
		name string
		z    float64
		want int
	}{
		// 阈值 -0.843 (q1/q2 分界)
		{"just_below_-0.843", -0.843 - eps, 1},
		{"at_-0.843", -0.843, 2},
		{"just_above_-0.843", -0.843 + eps, 2},

		// 阈值 -0.253 (q2/q3 分界)
		{"just_below_-0.253", -0.253 - eps, 2},
		{"at_-0.253", -0.253, 3},
		{"just_above_-0.253", -0.253 + eps, 3},

		// 阈值 0.253 (q3/q4 分界)
		{"just_below_0.253", 0.253 - eps, 3},
		{"at_0.253", 0.253, 4},
		{"just_above_0.253", 0.253 + eps, 4},

		// 阈值 0.843 (q4/q5 分界)
		{"just_below_0.843", 0.843 - eps, 4},
		{"at_0.843", 0.843, 5},
		{"just_above_0.843", 0.843 + eps, 5},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := f.ComputeQuintile(tc.z)
			assert.Equal(t, tc.want, got, "z=%.12f", tc.z)
			assert.GreaterOrEqual(t, got, 1, "quintile must be >= 1")
			assert.LessOrEqual(t, got, 5, "quintile must be <= 5")
		})
	}
}

// TestComputeQuintile_AllQuintiles 确认五个 quintile 都能被产生，每个
// quintile 取一个远离边界的代表值，避免与边界测试重叠。
func TestComputeQuintile_AllQuintiles(t *testing.T) {
	f := newTestFactorCache()

	cases := []struct {
		name string
		z    float64
		want int
	}{
		{"q1_deep_bottom", -1.5, 1},
		{"q2_lower_mid", -0.5, 2},
		{"q3_center", 0.0, 3},
		{"q4_upper_mid", 0.5, 4},
		{"q5_deep_top", 1.5, 5},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := f.ComputeQuintile(tc.z)
			assert.Equal(t, tc.want, got, "z=%.4f", tc.z)
		})
	}

	// 汇总断言：五个 quintile 全部出现（无空洞）。
	seen := make(map[int]bool, 5)
	for _, tc := range cases {
		seen[f.ComputeQuintile(tc.z)] = true
	}
	for q := 1; q <= 5; q++ {
		assert.True(t, seen[q], "quintile %d never produced", q)
	}
}

// TestComputeQuintile_Extreme 验证极端值（±Inf、超大数量级）不会导致
// 越界或异常，且恒落入两端的 quintile (q1 / q5)。微小正负数落在 q3。
func TestComputeQuintile_Extreme(t *testing.T) {
	f := newTestFactorCache()

	cases := []struct {
		name string
		z    float64
		want int
	}{
		{"pos_inf", math.Inf(1), 5},
		{"neg_inf", math.Inf(-1), 1},
		{"huge_pos_1e10", 1e10, 5},
		{"huge_neg_1e10", -1e10, 1},
		{"large_pos_100", 100.0, 5},
		{"large_neg_100", -100.0, 1},
		{"max_float64", math.MaxFloat64, 5},
		{"neg_max_float64", -math.MaxFloat64, 1},
		// 极小正/负数绝对值远小于 0.253，落 q3。
		{"tiny_pos", math.SmallestNonzeroFloat64, 3},
		{"tiny_neg", -math.SmallestNonzeroFloat64, 3},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := f.ComputeQuintile(tc.z)
			assert.Equal(t, tc.want, got, "z=%v", tc.z)
			assert.GreaterOrEqual(t, got, 1, "quintile must be >= 1 (z=%v)", tc.z)
			assert.LessOrEqual(t, got, 5, "quintile must be <= 5 (z=%v)", tc.z)
		})
	}
}
