package backtest

// QT-13（A11）：复权锚点的**输入集合**由 `(symbol, start_date)` 这个主键决定，
// 而不是由「L1 缓存里当时有什么」决定。
//
// 两条腿各测各的（分开才有牙）：
//   - 切片（clipToRunRange）管「缓存里有别人的数据」：装配期种子塞的是全量
//     序列，QT-12 之前还叠加过跨 run 单调合并 —— 锚点吃整条缓存就会冻出一行
//     后来谁都校验不过的基准；
//   - 摘要自描述（`<hash>@<覆盖到哪天>`）管「主键不记 end，而切片记」：
//     同 start 不同 end 的两次跑共用同一行，不记覆盖范围就会把一次正常的
//     重跑误报成数据损坏（walk-forward 的扩张窗口就是这个形状）。
//
// 端到端那条把**冻结**这一步放在污染缓存下跑（用例 A）：破坏验证 L1 实测表明，
// 只污染「重跑」那一笔是测不出切片缺失的 —— 第 2 条腿（覆盖范围自描述）会先把
// 核对裁回正确范围，两条款等价于互为兜底，所以污染必须发生在写行的那一刻。

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
)

const qt13Sym = "600000.SH"

// qt13Day 把 rev18Start 往后推 n 天（UTC 零点，与 rev18Bars 的造数一致）。
func qt13Day(n int) time.Time { return rev18Start.AddDate(0, 0, n) }

// ─── ① 切片：锚点的输入集合 = 本 run 区间 ─────────────────────────────

func TestClipToRunRange_DropsBarsOutsideIntervalAndSortsOnACopy(t *testing.T) {
	t.Parallel()

	full := rev18Bars(qt13Sym, 10.0, 10.1, 10.2, 10.3, 10.4, 10.5) // 01-02 … 01-07
	// 乱序 + 两端越界：本 run 的区间是 01-04 … 01-05。
	shuffled := []domain.OHLCV{full[5], full[3], full[0], full[4], full[1], full[2]}
	pristine := append([]domain.OHLCV(nil), shuffled...)

	got := clipToRunRange(shuffled, qt13Day(2), qt13Day(3))

	require.Len(t, got, 2, "区间外的 bar 不许进锚点（哪怕缓存里有）")
	assert.Equal(t, qt13Day(2), got[0].Date, "切片必须先排序再裁：缓存里的顺序不保证（种子是调用方直接塞的）")
	assert.Equal(t, qt13Day(3), got[1].Date)
	assert.Exactly(t, pristine, shuffled, "裁切片只在副本上排序，不许就地改调用方拿到的序列")

	assert.Empty(t, clipToRunRange(full, qt13Day(20), qt13Day(25)),
		"区间里一根都没有 ⇒ 空，不许回落到「最近的日子」当区间起点")
	assert.Empty(t, clipToRunRange(nil, qt13Day(0), qt13Day(3)))
}

// 同一个 symbol，缓存里是长序列还是只有本区间的短序列，冻结出来的行必须**一字不差**。
// 这条就是 A11 第二行「首跑时缓存里有别的 run 的旧数据」的判据。
func TestPrepareAdjustmentRun_FrozenRowDoesNotDependOnHowLongTheCacheIs(t *testing.T) {
	t.Parallel()

	start, end := qt13Day(0), qt13Day(2) // 区间 = 3 根 bar
	longCache := rev18Bars(qt13Sym, 10.0, 10.1, 10.2, 10.3, 10.4, 10.5)
	shortCache := longCache[:3]

	freeze := func(data []domain.OHLCV) AdjustmentAnchor {
		t.Helper()
		store := newMemAnchorStore()
		run, err := prepareAdjustmentRun(context.Background(), store, start,
			[]string{qt13Sym},
			func(string) []domain.OHLCV { return clipToRunRange(data, start, end) },
			zerolog.Nop())
		require.NoError(t, err)
		require.Len(t, run.captured, 1)
		return run.captured[0]
	}

	fromLong, fromShort := freeze(longCache), freeze(shortCache)

	assert.Equal(t, fromShort, fromLong,
		"锚点行（含 digest 与覆盖范围）必须由 (symbol, start_date) 决定，不能由缓存里躺着多长的序列决定")
	assert.Equal(t, start, fromLong.AnchorDate, "AnchorDate = 切片首根，不是缓存首根")
	assert.Contains(t, fromLong.SeriesDigest, "@2024-01-04",
		"新冻的行必须自述覆盖到哪一天（本区间最后一根 = 01-04），否则同 start 不同 end 的重跑无法核对")
}

// 执法点：prepare 收到「起点早于 run 区间」的序列 ⇒ 当场红。
// 上一行的纪律如果只写在注释里，下一个调用点就会再把越界数据喂进来。
func TestPrepareAdjustmentRun_RejectsSeriesStartingBeforeRunRange(t *testing.T) {
	t.Parallel()

	// 序列从 01-02 起，但本 run 的区间起点是 01-04 —— 典型的「忘了切片」。
	_, err := prepareAdjustmentRun(context.Background(), newMemAnchorStore(), qt13Day(2),
		[]string{qt13Sym},
		func(string) []domain.OHLCV { return rev18Bars(qt13Sym, 10.0, 10.1, 10.2) },
		zerolog.Nop())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "切片", "报错要点名「先按区间切片」这条纪律，不是泛泛的 invalid input")
}

// ─── ② 摘要自描述：同 start 不同 end 共用一行 ─────────────────────────

func TestSeriesDigest_RangeSuffixRoundTrip(t *testing.T) {
	t.Parallel()

	hash, covered := "1a2b3c4d5e6f7a8b", qt13Day(5)
	s := encodeSeriesDigest(hash, covered)
	require.Contains(t, s, "@")

	gotHash, gotCovered, ok := decodeSeriesDigest(s)
	require.True(t, ok)
	assert.Equal(t, hash, gotHash)
	assert.Equal(t, covered.UTC().Format("2006-01-02"), gotCovered.UTC().Format("2006-01-02"))

	// 无后缀 = 历史行，覆盖范围未知（沿用旧口径，不清理存量）。
	h, zero, ok := decodeSeriesDigest("digest-v1")
	assert.True(t, ok)
	assert.Equal(t, "digest-v1", h)
	assert.True(t, zero.IsZero())

	// 后缀在但读不懂 ⇒ ok=false：不能把「我解析不了」当成「旧格式」放过去。
	_, _, ok = decodeSeriesDigest("abcdef@not-a-date")
	assert.False(t, ok)

	assert.Equal(t, hash, encodeSeriesDigest(hash, time.Time{}), "零值范围退回无后缀形状")
}

func TestPrepareAdjustmentRun_UnreadableDigestSuffixFailsLoud(t *testing.T) {
	t.Parallel()

	store := newMemAnchorStore()
	require.NoError(t, store.SaveAdjustmentAnchors(context.Background(), qt13Day(0),
		[]AdjustmentAnchor{{Symbol: qt13Sym, AnchorDate: qt13Day(0), AnchorClose: 10.0,
			SeriesDigest: "deadbeef@2024-13-45"}}))

	_, err := prepareAdjustmentRun(context.Background(), store, qt13Day(0),
		[]string{qt13Sym},
		func(string) []domain.OHLCV { return rev18Bars(qt13Sym, 10.0, 10.1, 10.2) },
		zerolog.Nop())

	require.ErrorIs(t, err, ErrAdjustmentDigestShape)
}

// 长区间先跑并冻结，短区间后跑：不能报错，且价格刻度必须与冻结时逐位一致。
func TestVerifyAgainstFrozen_ShorterRerunPinsScaleWithoutDigestCheck(t *testing.T) {
	t.Parallel()

	store := newMemAnchorStore()
	long := rev18Bars(qt13Sym, 10.0, 10.2, 10.1, 9.8, 9.9)
	runLong := rev18Prepare(t, store, []string{qt13Sym}, func(string) []domain.OHLCV { return long })
	require.NoError(t, store.SaveAdjustmentAnchors(context.Background(), qt13Day(0), runLong.captured))

	// 短区间重跑（同 start、end 更短），并且数据整体重同步过一次（×0.5）。
	short := rev18ScaleBars(long[:2], 0.5)
	runShort, err := prepareAdjustmentRun(context.Background(), store, qt13Day(0),
		[]string{qt13Sym}, func(string) []domain.OHLCV { return short }, zerolog.Nop())
	require.NoError(t, err, "区间更短不是数据损坏，不该把这笔 run 判死")
	assert.Equal(t, 1, runShort.unverified, "没核对过就要计数，别让「跑通了」被读成「核对过了」")

	got, err := runShort.normalize(qt13Sym, short)
	require.NoError(t, err)
	frozen, err := runLong.normalize(qt13Sym, long[:2])
	require.NoError(t, err)
	assert.Exactly(t, frozen, got, "短区间重跑的刻度必须与冻结时逐位一致（ratio 只依赖锚点日）")
}

// 反向：短区间先冻结、长区间后跑 ⇒ 交集就是冻结声明的那段，照常核对。
// 核对范围之外的改动这一行管不着（那属数据版本坐标系，QT-40），如实写明。
func TestVerifyAgainstFrozen_LongerRerunChecksTheDeclaredRangeOnly(t *testing.T) {
	t.Parallel()

	store := newMemAnchorStore()
	short := rev18Bars(qt13Sym, 10.0, 10.2, 10.1)
	runShort := rev18Prepare(t, store, []string{qt13Sym}, func(string) []domain.OHLCV { return short })
	require.NoError(t, store.SaveAdjustmentAnchors(context.Background(), qt13Day(0), runShort.captured))

	// ① 长区间重跑、数据没动 ⇒ 交集内核对通过。
	long := rev18Bars(qt13Sym, 10.0, 10.2, 10.1, 9.8, 9.9)
	runLong, err := prepareAdjustmentRun(context.Background(), store, qt13Day(0),
		[]string{qt13Sym}, func(string) []domain.OHLCV { return long }, zerolog.Nop())
	require.NoError(t, err)
	assert.Equal(t, 0, runLong.unverified, "区间覆盖到了声明的范围 = 真核对过")

	// ② 冻结范围内改价（次日 10.2 → 9.9）⇒ 必须红。
	correctedInside := rev18Bars(qt13Sym, 10.0, 9.9, 10.1, 9.8, 9.9)
	_, err = prepareAdjustmentRun(context.Background(), store, qt13Day(0),
		[]string{qt13Sym}, func(string) []domain.OHLCV { return correctedInside }, zerolog.Nop())
	require.ErrorIs(t, err, ErrAdjustmentDrift)

	// ③ 只在冻结范围**之外**补行 ⇒ 这一行核对不到（前缀哈希不可分解）。
	//    这不是放宽：冻结行只对它声明的那段负责，越界那段从来没有过基准。
	appended := rev18Bars(qt13Sym, 10.0, 10.2, 10.1, 999.9)
	_, err = prepareAdjustmentRun(context.Background(), store, qt13Day(0),
		[]string{qt13Sym}, func(string) []domain.OHLCV { return appended }, zerolog.Nop())
	require.NoError(t, err, "覆盖范围之外的新行不该被当成漂移（否则任何增量同步都跑不动）")
}

// 历史行（无后缀）行为不变：同区间重跑照旧核对，改价照旧红。
// 任务单 ④ 明确不清理存量行，所以这条是「不退化」的判据，不是新功能。
func TestVerifyAgainstFrozen_LegacyRowWithoutSuffixKeepsOldSemantics(t *testing.T) {
	t.Parallel()

	bars := rev18Bars(qt13Sym, 10.0, 10.2, 10.1)
	legacy := AdjustmentAnchor{
		Symbol: qt13Sym, AnchorDate: qt13Day(0), AnchorClose: 10.0,
		SeriesDigest: seriesDigest(quantizeBars(bars)), // 旧形状：纯 hash，无 @日期
	}
	store := newMemAnchorStore()
	require.NoError(t, store.SaveAdjustmentAnchors(context.Background(), qt13Day(0),
		[]AdjustmentAnchor{legacy}))

	sameRange, err := prepareAdjustmentRun(context.Background(), store, qt13Day(0),
		[]string{qt13Sym}, func(string) []domain.OHLCV { return bars }, zerolog.Nop())
	require.NoError(t, err)
	assert.Equal(t, 0, sameRange.unverified, "无后缀 = 范围未知，按整个取数区间核对（旧口径）")

	_, err = prepareAdjustmentRun(context.Background(), store, qt13Day(0),
		[]string{qt13Sym},
		func(string) []domain.OHLCV { return rev18Bars(qt13Sym, 10.0, 9.9, 10.1) },
		zerolog.Nop())
	require.ErrorIs(t, err, ErrAdjustmentDrift)
}

// QT-13 之前由「整条合并缓存」冻下的行，锚点日会早于区间起点。这一类行今天
// 仍然会红（存量账，④ 不清理），但报错必须点名怎么修 —— 让人去查数据是否
// 损坏，是在浪费一整次事故复盘。
func TestVerifyAgainstFrozen_PoisonedLegacyRowNamesTheRemedy(t *testing.T) {
	t.Parallel()

	store := newMemAnchorStore()
	require.NoError(t, store.SaveAdjustmentAnchors(context.Background(), qt13Day(2),
		[]AdjustmentAnchor{{Symbol: qt13Sym, AnchorDate: qt13Day(0), AnchorClose: 10.0,
			SeriesDigest: "abc123"}}))

	_, err := prepareAdjustmentRun(context.Background(), store, qt13Day(2),
		[]string{qt13Sym},
		// 本 run 的数据是**正确裁过**的（首根 = 01-04）；那行的锚点日却是
		// 01-02 —— 老代码拿整条合并缓存冻的键，正是这一形状。
		func(string) []domain.OHLCV {
			return clipToRunRange(rev18Bars(qt13Sym, 10.0, 10.1, 10.2, 10.3, 10.4), qt13Day(2), qt13Day(4))
		},
		zerolog.Nop())

	require.ErrorIs(t, err, ErrAdjustmentAnchorMissing)
	assert.Contains(t, err.Error(), "删掉该行重跑", "报错要给出可执行的修法，不是只说锚点缺失")
}

// ─── ③ 端到端：任务单的三种情形 ──────────────────────────────────────
//
// A. **冻结时就带着污染的缓存**：装配期种子里躺着 252 天全量序列，本 run 只
//
//	回放第 60…179 天 —— 锚点必须落在第 60 天，digest 只覆盖到第 179 天；
//
// B. 干净装配（种子只有本区间）重跑同一参数；
// C. 「重启」等价物 + 数据在两次跑之间**重同步过**（qfq 基准整体 ×0.5）：
//
//	刻度没钉住的话净值曲线会整体换个数量级，所以这一笔给「逐位一致」上牙。
//
// 三条之外还跑第 ④ 条：同 start 的**更长**区间（walk-forward 的扩张窗口形状）。
// 主键只有 start_date，区间变长不是数据损坏 —— 修之前这里会红。
func TestEngine_REV18_ContaminatedFreezeThenCleanThenRestartIdentical(t *testing.T) {
	if testing.Short() {
		t.Skip("full-run test, skipped in -short")
	}
	registerRev18Momentum(t)

	const nSymbols, nTotalDays, offset, nRunDays = 20, 252, 60, 120
	base := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	symbols := make([]string, nSymbols)
	for i := range symbols {
		symbols[i] = fmt.Sprintf("%06d.SH", 600000+i)
	}
	// 同一 seed 逐 symbol 顺序生成 ⇒ 任意区间都是全序列的连续切片。
	full := rev18SynthOHLCV(symbols, nTotalDays, base)
	fullDays := rev18Days(base, nTotalDays)
	inRange := make(map[string][]domain.OHLCV, nSymbols)
	for sym, bars := range full {
		inRange[sym] = bars[offset : offset+nRunDays]
	}
	runStart, runEnd := fullDays[offset], fullDays[offset+nRunDays-1]

	req := BacktestRequest{
		Strategy:       "momentum",
		StockPool:      symbols,
		StartDate:      runStart.Format("2006-01-02"),
		EndDate:        runEnd.Format("2006-01-02"),
		InitialCapital: 1000000.0,
		RiskFreeRate:   0.03,
	}
	days := fullDays[offset : offset+nRunDays]

	store := newMemAnchorStore()

	// A. 污染缓存里冻结基准。
	respA, err := buildRev18Engine(t, full, symbols, days, store).RunBacktest(context.Background(), req)
	require.NoError(t, err, "种子里有区间外的 bar 不是数据损坏，不该把这笔 run 判死")
	require.Greater(t, respA.TotalTrades, 0, "fixture 必须有成交，否则对拍无意义")

	key := runStart.Format("2006-01-02")
	for _, sym := range symbols {
		a := store.data[key][sym]
		require.NotEmpty(t, a.Symbol, "%s 的锚点必须已冻结", sym)
		assert.Equal(t, runStart.UTC().Format("2006-01-02"), a.AnchorDate.UTC().Format("2006-01-02"),
			"AnchorDate = 切片首根（第 %d 天），不是缓存首根（第 0 天）", offset)
		assert.Contains(t, a.SeriesDigest, "@"+runEnd.Format("2006-01-02"),
			"digest 覆盖上界 = 本 run 区间最后一天，不是缓存里那条长序列的末尾")
	}

	// B. 干净重跑。
	respB, err := buildRev18Engine(t, inRange, symbols, days, store).RunBacktest(context.Background(), req)
	require.NoError(t, err)

	// C. 重启 + 重同步（整条曲线 ×0.5）。
	respC, err := buildRev18Engine(t, rev18ScaleAll(inRange, 0.5), symbols, days, store).
		RunBacktest(context.Background(), req)
	require.NoError(t, err)

	assert.Exactly(t, respA.PortfolioValues, respB.PortfolioValues, "A vs B 净值逐位一致")
	assert.Exactly(t, respA.PortfolioValues, respC.PortfolioValues, "A vs C（重启 + 重同步）净值逐位一致")
	assert.Exactly(t, respA.TotalReturn, respC.TotalReturn)
	assert.Equal(t, respA.TotalTrades, respB.TotalTrades)

	// ④ 同 start 的更长区间重跑（扩张窗口）：按冻结行声明的范围核对，必须跑得动。
	reqLong := req
	reqLong.EndDate = fullDays[len(fullDays)-1].Format("2006-01-02")
	respLong, err := buildRev18Engine(t, full, symbols, fullDays[offset:], store).
		RunBacktest(context.Background(), reqLong)
	require.NoError(t, err, "同 start 的更长区间重跑必须通过（按冻结行声明的范围核对）")

	// 同一份数据、空白 store 跑同一长区间：锚点这套记账只钉刻度，不改数。
	respLongFresh, err := buildRev18Engine(t, full, symbols, fullDays[offset:], newMemAnchorStore()).
		RunBacktest(context.Background(), reqLong)
	require.NoError(t, err)
	assert.Exactly(t, respLongFresh.PortfolioValues, respLong.PortfolioValues,
		"同一份数据的长区间结果不能因为锚点行是新冻的还是核对过的而变化")
}

// rev18Days 造连续「交易日」列表（与 rev18SynthOHLCV 的逐日造数一致）。
func rev18Days(start time.Time, n int) []time.Time {
	days := make([]time.Time, 0, n)
	for d := 0; d < n; d++ {
		days = append(days, start.AddDate(0, 0, d))
	}
	return days
}
