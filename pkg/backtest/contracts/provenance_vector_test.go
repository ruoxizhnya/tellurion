package contracts

// QT-03 ⑤ 的向量测试：**改任一决定性输入 ⇒ hash 必变；不改 ⇒ 必不变**。
//
// 这套测试是留痕体系的牙齿。没有它，「三份 hash」只是三个字段名 —— 实现里漏掉
// 一个分量、或者把某个分量编成了不稳定的字节，都不会有任何信号。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	day1 = time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	day2 = time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)
)

// baselineRunSpec 是向量测试的起点：每个用例只改**一个**分量，其余逐字节不动。
//
// 「只改一个」是这套测试的方法论 —— 一次改两处然后观察到 hash 变了，什么也没证明。
func baselineRunSpec() RunSpecInput {
	return RunSpecInput{
		Start:          day1,
		End:            day2,
		InitialCapital: 1000000,
		RiskFreeRate:   0.02,

		StrategyArtifactHash:    "a1b2c3d4e5f60718293a4b5c6d7e8f9011223344556677889900aabbccddeeff",
		StrategyVersionHash:     "00112233445566778899aabbccddeeff0123456789abcdef0123456789abcdef",
		StrategyParamsEffective: map[string]any{"lookback": float64(20), "top_n": float64(30), "rebalance": "monthly"},

		StockPool: []string{"600000.SH", "000001.SZ"},
		IndexCode: "000300.SH",
		Universe:  []string{"600000.SH", "000001.SZ", "300750.SZ"},

		Frequency:  "1d",
		FillTiming: "next_open",

		Kernel:        "go",
		EngineVersion: "0.1.0",

		Fees:           map[string]float64{"stamp_tax": 0.0005, "commission": 0.00025, "min_commission": 5},
		Slippage:       0.001,
		CostModel:      "asw",
		LotSize:        100,
		TPlus:          "t+1",
		PriceLimitRule: "main_board_10pct",

		RiskRulesHash:       "ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100",
		DatasetSnapshotHash: baselineDatasetSnapshot().Hash(),

		AdjustmentAnchorDigests: []string{"600000.SH|2024-01-02|d1", "000001.SZ|2024-01-02|d2"},

		Seed: 0,
	}
}

func baselineDatasetSnapshot() DatasetSnapshotInput {
	return DatasetSnapshotInput{
		Universe:        []string{"600000.SH", "000001.SZ", "300750.SZ"},
		Window:          DateRange{Start: day1, End: day2},
		Frequency:       "1d",
		DataVersion:     "tushare-2026-10-20",
		Calendar:        []time.Time{day1, day1.AddDate(0, 0, 1), day1.AddDate(0, 0, 2)},
		AdjustmentBasis: "qfq",
	}
}

func mustRunHash(t *testing.T, in RunSpecInput) string {
	t.Helper()
	h, err := in.Hash()
	require.NoError(t, err)
	require.Len(t, h, 64)
	return h
}

// TestRunSpecHash_ChangesOnEveryDecidingInput 是「8 个必变分量」的验收腿，
// 并且比 8 个更多：RunSpecInput 的**每一个字段**都必须有牙。
//
// N6 点名的八项是 费率 / 滑点 / 票池 / 日历 / 复权基准 / 内核 / FillTiming / seed。
// 其余字段一并覆盖 —— 半套向量测试比没有更危险，因为它会让人以为整套都验过了。
func TestRunSpecHash_ChangesOnEveryDecidingInput(t *testing.T) {
	base := mustRunHash(t, baselineRunSpec())

	calendarShift := baselineDatasetSnapshot()
	calendarShift.Calendar = append(calendarShift.Calendar, day1.AddDate(0, 0, 3))
	calendarOnlyBase := baselineRunSpec()
	calendarOnlyBase.DatasetSnapshotHash = calendarShift.Hash()

	tests := []struct {
		name  string
		label string // N6 点名的八项标 "N6"，其余标 "extra"
		mut   func(*RunSpecInput)
	}{
		{"fees rate", "N6", func(in *RunSpecInput) { in.Fees["stamp_tax"] = 0.001 }},
		{"fees new item", "N6", func(in *RunSpecInput) { in.Fees["transfer_fee"] = 0.00001 }},
		{"fees emptied", "N6", func(in *RunSpecInput) { in.Fees = map[string]float64{} }},
		{"slippage", "N6", func(in *RunSpecInput) { in.Slippage = 0.002 }},
		{"stock_pool append", "N6", func(in *RunSpecInput) { in.StockPool = append(append([]string{}, in.StockPool...), "600519.SH") }},
		{"stock_pool remove", "N6", func(in *RunSpecInput) { in.StockPool = []string{"600000.SH"} }},
		{"calendar changed", "N6", func(in *RunSpecInput) { in.DatasetSnapshotHash = calendarShift.Hash() }},
		{"adjustment basis", "N6", func(in *RunSpecInput) { in.AdjustmentAnchorDigests = []string{"600000.SH|2024-01-02|CHANGED"} }},
		{"kernel", "N6", func(in *RunSpecInput) { in.Kernel = "nautilus" }},
		{"fill_timing", "N6", func(in *RunSpecInput) { in.FillTiming = "same_close" }},
		{"seed", "N6", func(in *RunSpecInput) { in.Seed = 42 }},

		{"start", "extra", func(in *RunSpecInput) { in.Start = day1.AddDate(0, 0, 1) }},
		{"end", "extra", func(in *RunSpecInput) { in.End = day2.AddDate(0, 0, -1) }},
		{"initial_capital", "extra", func(in *RunSpecInput) { in.InitialCapital = 1000001 }},
		{"risk_free_rate", "extra", func(in *RunSpecInput) { in.RiskFreeRate = 0.0201 }},
		{"artifact_hash", "extra", func(in *RunSpecInput) { in.StrategyArtifactHash = "b" + in.StrategyArtifactHash[1:] }},
		{"version_hash", "extra", func(in *RunSpecInput) { in.StrategyVersionHash = "1" + in.StrategyVersionHash[1:] }},
		{"params value", "extra", func(in *RunSpecInput) { in.StrategyParamsEffective["lookback"] = float64(21) }},
		{"params new key", "extra", func(in *RunSpecInput) { in.StrategyParamsEffective["extra"] = true }},
		{"params nil vs empty", "extra", func(in *RunSpecInput) { in.StrategyParamsEffective = nil }},
		{"index_code", "extra", func(in *RunSpecInput) { in.IndexCode = "000905.SH" }},
		{"universe append", "extra", func(in *RunSpecInput) { in.Universe = append(append([]string{}, in.Universe...), "688981.SH") }},
		{"frequency", "extra", func(in *RunSpecInput) { in.Frequency = "1m" }},
		{"engine_version", "extra", func(in *RunSpecInput) { in.EngineVersion = "0.2.0" }},
		{"cost_model", "extra", func(in *RunSpecInput) { in.CostModel = "square_root" }},
		{"lot_size", "extra", func(in *RunSpecInput) { in.LotSize = 200 }},
		{"t_plus", "extra", func(in *RunSpecInput) { in.TPlus = "t+0" }},
		{"price_limit_rule", "extra", func(in *RunSpecInput) { in.PriceLimitRule = "chinext_20pct" }},
		{"risk_rules_hash", "extra", func(in *RunSpecInput) { in.RiskRulesHash = "0" + in.RiskRulesHash[1:] }},
		{"dataset_snapshot_hash", "extra", func(in *RunSpecInput) { in.DatasetSnapshotHash = "ab" + in.DatasetSnapshotHash[2:] }},
	}

	n6 := 0
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mutated := baselineRunSpec()
			tc.mut(&mutated)
			got := mustRunHash(t, mutated)
			assert.NotEqual(t, base, got,
				"改了这个分量而 run_spec_hash 不变 = 这个分量的变化不会被任何留痕发现（N6 要防的病）")
			if tc.label == "N6" {
				n6++
			}
		})
	}
	assert.GreaterOrEqual(t, n6, 8, "N6 点名的八个必变分量必须全部在册")

	// 日历那条要单独再钉一次：只动日历、OHLCV 一个都不变时，run_spec_hash 也必须变。
	// 这条是 §7.1 把 Axis 塞进 hash 的全部理由。
	noChange := mustRunHash(t, calendarOnlyBase)
	assert.NotEqual(t, base, noChange, "日历多一天而 hash 不变 → 同 hash 不同结果无法解释")
}

// TestRunSpecHash_StableForEquivalentInputs 是「不必变」的三条腿。
func TestRunSpecHash_StableForEquivalentInputs(t *testing.T) {
	base := mustRunHash(t, baselineRunSpec())

	// ① 同一份输入重算两次 —— 最基础的一条，确定性不成立后面全不用谈。
	assert.Equal(t, base, mustRunHash(t, baselineRunSpec()))

	// ② map 的遍历顺序无关（Go 的 map 迭代是随机化的，参数来自 JSON 时顺序不可控）。
	ordered := baselineRunSpec()
	ordered.StrategyParamsEffective = map[string]any{"rebalance": "monthly", "top_n": float64(30), "lookback": float64(20)}
	assert.Equal(t, base, mustRunHash(t, ordered), "参数 map 的键序不该改变身份")

	// ③ 票池与 universe 是**集合**：顺序不同、重复项，都不构成第二个输入。
	reordered := baselineRunSpec()
	reordered.StockPool = []string{"000001.SZ", "600000.SH"}
	reordered.Universe = []string{"300750.SZ", "600000.SH", "000001.SZ"}
	assert.Equal(t, base, mustRunHash(t, reordered), "票池顺序不该改变 run_spec_hash")

	dup := baselineRunSpec()
	dup.StockPool = []string{"600000.SH", "000001.SZ", "600000.SH"}
	dup.Universe = []string{"600000.SH", "000001.SZ", "300750.SZ", "000001.SZ"}
	dup.AdjustmentAnchorDigests = []string{"600000.SH|2024-01-02|d1", "000001.SZ|2024-01-02|d2", "600000.SH|2024-01-02|d1"}
	assert.Equal(t, base, mustRunHash(t, dup), "重复项不该被当成新输入")

	// ④ -0 与 0 是同一个数（两个 hash 会让「同参复现」莫名其妙地失败）。
	negZero := baselineRunSpec()
	negZero.Slippage = math.Copysign(0, -1)
	zero := baselineRunSpec()
	zero.Slippage = 0
	assert.Equal(t, mustRunHash(t, zero), mustRunHash(t, negZero), "-0 与 0 必须同一个 hash")

	// ⑤ 日期的时刻与时区不参与（回测区间是交易日，不是时刻）。
	timed := baselineRunSpec()
	timed.Start = time.Date(2024, 1, 2, 15, 30, 0, 0, time.FixedZone("CST", 8*3600))
	assert.Equal(t, base, mustRunHash(t, timed), "同一个日历日不因时刻/时区而成为两个输入")
}

// TestDatasetSnapshotHash_UniqueDefinition 钉住 N6-2 的六个分量各自的牙。
func TestDatasetSnapshotHash_UniqueDefinition(t *testing.T) {
	base := baselineDatasetSnapshot().Hash()
	require.Len(t, base, 64)
	assert.Equal(t, base, baselineDatasetSnapshot().Hash(), "同一快照必须重算同一个 hash")

	mutate := func(name string, f func(*DatasetSnapshotInput)) {
		in := baselineDatasetSnapshot()
		f(&in)
		assert.NotEqualf(t, base, in.Hash(), "%s 变了而 dataset_snapshot_hash 不变", name)
	}
	mutate("universe", func(in *DatasetSnapshotInput) { in.Universe = append(in.Universe, "600519.SH") })
	mutate("window_start", func(in *DatasetSnapshotInput) { in.Window.Start = day1.AddDate(0, 0, 1) })
	mutate("window_end", func(in *DatasetSnapshotInput) { in.Window.End = day2.AddDate(0, 0, -1) })
	mutate("frequency", func(in *DatasetSnapshotInput) { in.Frequency = "1m" })
	mutate("data_version", func(in *DatasetSnapshotInput) { in.DataVersion = "tushare-2026-10-21" })
	mutate("calendar_hash", func(in *DatasetSnapshotInput) { in.Calendar = in.Calendar[:2] })
	mutate("adjustment_basis", func(in *DatasetSnapshotInput) { in.AdjustmentBasis = "hfq" })

	// 顺序对 calendar 是语义（轴的顺序就是 T 的推进顺序），对 universe 不是。
	reorderedCal := baselineDatasetSnapshot()
	reorderedCal.Calendar = []time.Time{reorderedCal.Calendar[1], reorderedCal.Calendar[0], reorderedCal.Calendar[2]}
	assert.NotEqual(t, base, reorderedCal.Hash(), "交易日轴换了顺序就是换了轴")

	reorderedUniverse := baselineDatasetSnapshot()
	reorderedUniverse.Universe = []string{"300750.SZ", "000001.SZ", "600000.SH"}
	assert.Equal(t, base, reorderedUniverse.Hash(), "票池是集合，顺序不该改身份")
}

// TestHashDomainsAreDistinct 是域标签的腿：同样的值编进两个不同域必须得到两个 hash。
//
// 没有域标签，一个 dataset_snapshot_hash 可以被冒充成 run_spec_hash（反之亦然），
// 而两者在库里都是「64 位 hex」，谁都看不出来。
func TestHashDomainsAreDistinct(t *testing.T) {
	same := []string{"a", "b"}
	assert.NotEqual(t, UniverseHash(same), CalendarHash(nil))
	assert.NotEqual(t, UniverseHash(same), AdjustmentAnchorDigestsHash(same))

	runHash := mustRunHash(t, baselineRunSpec())
	dsHash := baselineDatasetSnapshot().Hash()
	assert.NotEqual(t, runHash, dsHash)
	assert.NotEqual(t, runHash, UniverseHash(same))
}

// TestArtifactHash_IsPlainSha256 钉 interop 口径：字节码 hash 必须是**裸 sha256**，
// 不能带我们的域标签 —— 编译服务与外部工具要能各自独立算出同一个值。
func TestArtifactHash_IsPlainSha256(t *testing.T) {
	assert.Equal(t,
		"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", // sha256("")
		ArtifactHash(nil), "空字节码的 sha256 是公开常量，对不上就说明加进了别的东西")

	// "abc" 的 sha256 同样是公开值。
	assert.Equal(t,
		"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
		ArtifactHash([]byte("abc")))

	// 与标准库逐字节一致（不依赖我们的帧结构）。
	wasm := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	sum := sha256.Sum256(wasm)
	assert.Equal(t, hex.EncodeToString(sum[:]), ArtifactHash(wasm))
}

// TestCanonical_RejectsUnencodableValues 是 fail-closed 腿：编不出来就报错，
// 绝不「跳过那个字段」。跳过 = 改了它 hash 也不变 = 留痕会撒谎。
func TestCanonical_RejectsUnencodableValues(t *testing.T) {
	type nested struct{ A int }

	bad := []struct {
		name  string
		value any
	}{
		{"struct", nested{A: 1}},
		{"func", func() {}},
		{"chan", make(chan int)},
		{"json.Number", json.Number("20")}, // UseNumber() 的真产物，不许被当字符串编进去
		{"struct inside map", map[string]any{"x": nested{}}},
		{"nan inside", map[string]any{"x": math.NaN()}},
		{"slice of struct", []any{nested{}}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			in := baselineRunSpec()
			in.StrategyParamsEffective = map[string]any{"bad": tc.value}
			_, err := in.Hash()
			require.Error(t, err, "不可编码的值必须报错")
			assert.ErrorIs(t, err, ErrCanonicalUnsupported)
		})
	}

	// 顶层 Float 也一样：NaN/Inf 不是数，是「出错了」的信号。
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		in := baselineRunSpec()
		in.Slippage = v
		_, err := in.Hash()
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrCanonicalUnsupported)
	}
}

// TestCanonical_OrderingIsByConstruction 钉住「编码顺序由调用点写死，与字段声明顺序无关」。
//
// 这条为什么值得测：如果哪天有人把 Hash() 里的写入顺序调整了，历史 run_spec_hash
// 会全部失效 —— 而 GoldenVector 会在改动的第一时间红。
func TestCanonical_OrderingIsByConstruction(t *testing.T) {
	a := NewCanonical("test.domain").Str("x", "1").Str("y", "2").MustHex()
	b := NewCanonical("test.domain").Str("y", "2").Str("x", "1").MustHex()
	assert.NotEqual(t, a, b, "写入顺序参与编码（顺序就是契约的一部分）")

	// 键名参与编码：漏写一个字段与写空值是两件事。
	withKey := NewCanonical("test.domain").Str("x", "1").MustHex()
	empty := NewCanonical("test.domain").Str("x", "").MustHex()
	absent := NewCanonical("test.domain").MustHex()
	assert.NotEqual(t, withKey, empty)
	assert.NotEqual(t, empty, absent)
}

// TestCanonical_LengthPrefixIsUnambiguous 防止「分隔符注入」：
// 值里带 ; 或 \x00 时，两个不同输入不许编出同一串字节。
func TestCanonical_LengthPrefixIsUnambiguous(t *testing.T) {
	x := NewCanonical("test.domain").Str("k", "a;b").MustHex()
	y := NewCanonical("test.domain").Str("k", "a").MustHex()
	assert.NotEqual(t, x, y)

	// 键名里带分隔符也不许与别的键混淆。
	p := NewCanonical("test.domain").Str("a", "bc").MustHex()
	q := NewCanonical("test.domain").Str("ab", "c").MustHex()
	assert.NotEqual(t, p, q)

	// 数值 20 与字符串 "20" 是两个不同输入（kind 前缀区分）。
	i20 := NewCanonical("test.domain").Int("k", 20).MustHex()
	s20 := NewCanonical("test.domain").Str("k", "20").MustHex()
	assert.NotEqual(t, i20, s20)
	f20 := NewCanonical("test.domain").Value("k", float64(20)).MustHex()
	assert.Equal(t, i20, f20, "JSON 数字落到 float64，与 Int(20) 必须是同一个输入（否则同参复现会假失败）")

	// float32 升 float64 编：整值仍是同一个输入，但 0.1 带着 float32 的精度痕迹，
	// 与 float64(0.1) 不是同一个数 —— 精度参与身份，这里刻意不抹平。
	assert.Equal(t, i20, NewCanonical("test.domain").Value("k", float32(20)).MustHex())
	assert.NotEqual(t,
		NewCanonical("test.domain").Value("k", float32(0.1)).MustHex(),
		NewCanonical("test.domain").Value("k", 0.1).MustHex(),
		"float32(0.1) 与 float64(0.1) 是两个不同的数，不许编成同一个 hash")
}

// ─── strategy_ref 三形态 ────────────────────────────────────

func TestParseStrategyRef(t *testing.T) {
	artifact := "a1b2c3d4e5f60718293a4b5c6d7e8f9011223344556677889900aabbccddeeff"
	version := "00112233445566778899aabbccddeeff0123456789abcdef0123456789abcdef"

	// 三种形态（N7-3）。
	latest, err := ParseStrategyRef("momentum")
	require.NoError(t, err)
	assert.Equal(t, StrategyRef{ID: "momentum"}, latest)
	assert.True(t, latest.IsLatest())

	byArtifact, err := ParseStrategyRef("momentum@" + artifact)
	require.NoError(t, err)
	assert.False(t, byArtifact.IsLatest())
	assert.Equal(t, artifact, byArtifact.Hash)

	// 往返：解析 → 序列化必须逐字节回到原串。
	for _, raw := range []string{"momentum", "momentum@" + artifact, "momentum@" + version} {
		ref, err := ParseStrategyRef(raw)
		require.NoError(t, err)
		assert.Equal(t, raw, ref.String(), "strategy_ref 必须可无损往返")
	}

	for _, bad := range []string{
		"", "  ", "@", "@a1b2",
		"momentum@", "momentum@short", "momentum@" + artifact + "0",
		"momentum@" + artifact[:63] + "Z",
		"momentum@" + "A1" + artifact[2:], // 大写：hash 的字节才是身份，不容两个串冒充同一个引用
		"a@b@c",
	} {
		_, err := ParseStrategyRef(bad)
		if bad == "@" || bad == "  " || bad == "" {
			require.Errorf(t, err, "%q must be rejected", bad)
			continue
		}
		require.Errorf(t, err, "%q must be rejected", bad)
		assert.ErrorIsf(t, err, ErrStrategyRefMalformed, "%q", bad)
	}
}

// TestStrategyRef_HashKindIsUndecidable 把 N7 的一个**真实限制**钉成测试。
//
// artifact_hash 与 version_hash 都是 sha256 的 64 位 hex，光看引用串分不出谁是谁。
// 解析器不猜 —— 由 resolve 侧查表决定。所以结果必须同时记
// `{strategy_ref_input, version_hash, artifact_hash}`（N7-5），否则「当初用的哪个
// 引用」这个问题无法回答。这条测试存在的意义是：**别指望在字符串层面把它分清**。
func TestStrategyRef_HashKindIsUndecidable(t *testing.T) {
	const artifact, version = "a1b2c3d4e5f60718293a4b5c6d7e8f9011223344556677889900aabbccddeeff",
		"00112233445566778899aabbccddeeff0123456789abcdef0123456789abcdef"

	a, err := ParseStrategyRef("momentum@" + artifact)
	require.NoError(t, err)
	v, err := ParseStrategyRef("momentum@" + version)
	require.NoError(t, err)

	// 两个引用形状完全同构：解析器给不出「这是 artifact 还是 version」的判别。
	assert.Equal(t, a.ID, v.ID)
	assert.False(t, a.IsLatest())
	assert.False(t, v.IsLatest())
	assert.Len(t, a.Hash, len(v.Hash))
	assert.NotEqual(t, a.Hash, v.Hash, "两个不同 hash 当然不同；相同形状才是这里要钉的东西")
}

// ─── 黄金向量（防编码漂移）──────────────────────────────────

// TestGoldenVectors_RunSpecHash 把一份固定输入的最终 hash 写死成字面量。
//
// 这是整套体系唯一能同时抓住「有人改了编码顺序 / 帧结构 / 浮点表示 / 域标签」的
// 测试 —— 上面那些「改一个分量 ⇒ hash 变」的用例对这些改动**完全不敏感**
// （它们只比两个 hash 是否相等，两边一起变就看不出来）。
//
// 下面两个值由 2026-10-21 的实现产出并逐字节固定（同机连跑 3 次 + 跨 -count 一致）。
// **改它们必须走变更评审**：那等于宣布所有历史 run_spec_hash 作废。
func TestGoldenVectors_RunSpecHash(t *testing.T) {
	golden := mustRunHash(t, baselineRunSpec())
	assert.Equal(t, "d7ea72c64600660be1af01f203734fbd278e558f3a9b62294e707a4db78cef8f", golden,
		"run_spec_hash 黄金向量变了 —— 编码漂移会让全部历史留痕失效")

	assert.Equal(t, "3fa92d73d559fc5d7ebd75ffa95a2f993b1e3fed29483419faf37d243b078b85",
		baselineDatasetSnapshot().Hash(), "dataset_snapshot_hash 黄金向量变了")
}
