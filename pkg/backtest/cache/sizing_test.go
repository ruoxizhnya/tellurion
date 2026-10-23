package cache

// QT-12 ① · 缓存字节可计量。
//
// 这组测试钉的是「准入闸拿去做判断的那个数是不是同一个口径算出来的」。
// Bytes() 不是 profiler，它是一份**估算**；估算本身可以是粗的，但两条
// 必须先于取数、一条来自取数之后的路（EstimateOHLCVBytes / OHLCVMapBytes）
// 不能各自漂移 —— 漂移的方向恰好是「两边都宣称自己没超预算」，那是准入闸
// 最坏的失效形态：闸看着在工作，实际从不拦。

import (
	"reflect"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sizingBars(symbol string, n int) []domain.OHLCV {
	out := make([]domain.OHLCV, n)
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		out[i] = domain.OHLCV{Symbol: symbol, Date: base.AddDate(0, 0, i), Close: 10.0}
	}
	return out
}

func TestCacheManagerBytes_EmptyIsZero(t *testing.T) {
	cm := NewCacheManager(zerolog.Nop())
	assert.Equal(t, int64(0), cm.Bytes(), "空缓存的账必须是 0，不能是负数或一个未初始化的大数")
	assert.Equal(t, int64(0), cm.Bars())
}

func TestCacheManagerBytes_GrowsWithLoad(t *testing.T) {
	cm := NewCacheManager(zerolog.Nop())
	before := cm.Bytes()

	data := map[string][]domain.OHLCV{
		"600000.SH": sizingBars("600000.SH", 250),
		"000001.SZ": sizingBars("000001.SZ", 250),
	}
	cm.Load(data)

	after := cm.Bytes()
	assert.Greater(t, after, before, "注入数据后账要涨")
	assert.Equal(t, OHLCVMapBytes(data), after,
		"Bytes() 必须与 OHLCVMapBytes 同一个函数算出来 —— 两处各写一遍迟早分叉")
	assert.Equal(t, int64(500), cm.Bars())

	// 清理语义也要如实：Load(nil) 之后账回到 0（run 结束不留尾账）。
	cm.Load(nil)
	assert.Equal(t, int64(0), cm.Bytes())
	assert.Equal(t, int64(0), cm.Bars())
}

// 两条路的口径互锁：先验估算（取数之前）与实测估算（取数之后）的差
// 必须**恰好**是 symbol 字符串本体那份字节。钉住这条等式，任何一侧
// 改动常数都会当场红，而不是悄悄让预算偏 5%。
func TestEstimateOHLCVBytesMatchesLoadedCache(t *testing.T) {
	symbols := []string{"600000.SH", "000001.SZ", "601398.SH"}
	const days = 120

	cm := NewCacheManager(zerolog.Nop())
	data := make(map[string][]domain.OHLCV, len(symbols))
	for _, s := range symbols {
		data[s] = sizingBars(s, days)
	}
	cm.Load(data)

	actual := cm.Bytes()
	estimate := EstimateOHLCVBytes(int64(len(symbols)), days)

	var nameBytes int64
	for _, s := range symbols {
		nameBytes += int64(len(s))
	}
	assert.Equal(t, estimate+nameBytes, actual,
		"实测 = 先验 + symbol 字符串本体；对不上说明两个估算函数的口径分叉了")
	assert.Greater(t, estimate, int64(0), "先验估算在正数输入下不该是 0（那等于闸永不生效）")
}

func TestEstimateOHLCVBytesDegenerateInputs(t *testing.T) {
	// 0 / 负数输入必须是 0：准入拿它做「装不装得下」的判断，
	// 一个负的估算会让 budget 检查恒真（谁都装得下）。
	assert.Equal(t, int64(0), EstimateOHLCVBytes(0, 100))
	assert.Equal(t, int64(0), EstimateOHLCVBytes(100, 0))
	assert.Equal(t, int64(0), EstimateOHLCVBytes(-5, 100))
	assert.Equal(t, int64(0), EstimateOHLCVBytes(100, -1))
}

// slice header 是按 cap 计的，不是 len：warm 之后底层数组可能有富余容量，
// 按 len 计会让账比真实占用小（估小了才会撞破预算，方向危险）。
func TestOHLCVMapBytesCountsCapacityNotLength(t *testing.T) {
	full := sizingBars("600000.SH", 10)
	trimmed := full[:5] // 同一个底层数组，len 减半、cap 不变

	assert.Equal(t, OHLCVMapBytes(map[string][]domain.OHLCV{"600000.SH": full}),
		OHLCVMapBytes(map[string][]domain.OHLCV{"600000.SH": trimmed}),
		"账按 cap 计：切片头砍半不改变底层数组占用")
}

func TestFundamentalsBytesCountsSeries(t *testing.T) {
	empty := FundamentalsBytes(map[string][]domain.Fundamental{})
	assert.Equal(t, int64(0), empty)

	data := map[string][]domain.Fundamental{
		"600000.SH": {{Symbol: "600000.SH", PE: ptr(12.5)}},
	}
	withOne := FundamentalsBytes(data)
	require.Greater(t, withOne, int64(0))

	// 每条记录的宽度来自**类型本身**（测试侧独立用 reflect 取一次，与
	// 实现同源但不同处）：domain.Fundamental 加字段时这里会跟着涨。
	// 如果实现里那个宽度被手写成一个数，加字段后账会偏小 —— 而偏小的
	// 方向是「预算没超」，正是准入闸最危险的失效侧。
	width := int64(reflect.TypeOf(domain.Fundamental{}).Size())
	assert.GreaterOrEqual(t, withOne, width+sliceHeaderBytes+mapEntryBytes+int64(len("600000.SH")),
		"财报账至少 = 记录宽度 + slice header + map 条目 + symbol 本体")
}

func TestFactorCacheBytesTracksLoadedEntries(t *testing.T) {
	f := NewFactorCacheAccessor(zerolog.Nop())
	assert.Equal(t, int64(0), f.Bytes())

	data := map[domain.FactorType]map[time.Time]map[string]float64{
		domain.FactorMomentum: {
			time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC): {"600000.SH": 1.5, "000001.SZ": -0.5},
			time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC): {"600000.SH": 1.6},
		},
	}
	f.Load(data)
	assert.Greater(t, f.Bytes(), int64(0), "z-score 注入后因子账要涨")

	before := f.Bytes()
	f.Load(nil)
	assert.Equal(t, int64(0), f.Bytes(), "清理后必须归零")
	assert.Greater(t, before, int64(0))
}

// Copy 是本 run 拿到私有因子账的那一步。
//
// 判据为什么是「调用方就地改原始 map」而不是「Copy 之后再 Load 一份」：
// Load 是整体替换语义，替换谁都不会碰到对方的内层 map —— 那种写法对浅拷贝
// 也照样绿（今天就被这么骗过一次）。而 Load(data) 存的就是 data 本身，
// 所以浅拷贝共享的内层 map 是**可达**的：拿着原始 map 的人一写，
// 所有 run 的读数一起变。这才是要防的那件事。
func TestFactorCacheCopyIsDeep(t *testing.T) {
	day := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	other := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	source := map[domain.FactorType]map[time.Time]map[string]float64{
		domain.FactorMomentum: {day: {"600000.SH": 1.5}},
	}

	seed := NewFactorCacheAccessor(zerolog.Nop())
	seed.Load(source)
	shot := seed.Copy()

	z, ok := shot.Get(domain.FactorMomentum, day, "600000.SH")
	require.True(t, ok, "Copy 必须带上种子里的全部读数")
	assert.InDelta(t, 1.5, z, 1e-9)

	// ① 最内层共享：原始 map 就地改一格 → Copy 的那格不许跟着变。
	source[domain.FactorMomentum][day]["600000.SH"] = 99.0
	zAfter, _ := shot.Get(domain.FactorMomentum, day, "600000.SH")
	assert.InDelta(t, 1.5, zAfter, 1e-9,
		"改原始 map 把 Copy 也改了 —— 内层 map 是共享的，QT-12 的隔离没做到")

	// ② 中间层共享：给原始 map 加一个日期 → Copy 里不许凭空多出来。
	source[domain.FactorMomentum][other] = map[string]float64{"600000.SH": 3.0}
	_, okExtra := shot.Get(domain.FactorMomentum, other, "600000.SH")
	assert.False(t, okExtra, "Copy 的按日期那层也在共享 —— 同一份 map 两个 run 一起长")

	// ③ 外层共享：给原始 map 加一个因子 → 同样不许漏进来。
	source[domain.FactorValue] = map[time.Time]map[string]float64{day: {"600000.SH": 7.0}}
	_, okFactor := shot.Get(domain.FactorValue, day, "600000.SH")
	assert.False(t, okFactor, "Copy 的最外层也在共享")

	// 反向也要成立：写 Copy 不能回染种子（引擎那份装配期数据）。
	shot.Load(map[domain.FactorType]map[time.Time]map[string]float64{
		domain.FactorMomentum: {day: {"600000.SH": -1.0}},
	})
	zSeed, okSeed := seed.Get(domain.FactorMomentum, day, "600000.SH")
	require.True(t, okSeed)
	assert.InDelta(t, 99.0, zSeed, 1e-9, "run 的 Load 回染了引擎级种子")
}

func TestFactorCacheEntriesCountsSymbolDateCells(t *testing.T) {
	f := NewFactorCacheAccessor(zerolog.Nop())
	assert.Equal(t, int64(0), f.Entries())

	f.Load(map[domain.FactorType]map[time.Time]map[string]float64{
		domain.FactorMomentum: {
			time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC): {"600000.SH": 1.5, "000001.SZ": -0.5},
			time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC): {"600000.SH": 1.6},
		},
	})
	assert.Equal(t, int64(3), f.Entries(), "三格 (factor,date,symbol) 值")
}

func ptr(f float64) *float64 { return &f }
