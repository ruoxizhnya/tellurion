package backtest

// REV-18：复权基准快照的纯逻辑测试。
//
// 核心场景对应生产缺陷：同一段历史，第一次同步得到 qfq 序列 S；后来同步
// 范围推进，基准换成更晚的交易日 ⇒ 整条曲线乘了一个常数 k（这里用 0.5，
// 是 2 的幂 —— 浮点缩放精确可逆，能让「逐位一致」的断言不被浮点尾数污染；
// 生产上的 k 不是 2 的幂，由 1e-6 量化兜底，见 adjustment_snapshot.go）。

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
)

var rev18Start = time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)

func rev18Bars(symbol string, closes ...float64) []domain.OHLCV {
	out := make([]domain.OHLCV, len(closes))
	for i, c := range closes {
		out[i] = domain.OHLCV{
			Symbol: symbol,
			Date:   rev18Start.AddDate(0, 0, i),
			Open:   c, High: c * 1.01, Low: c * 0.99, Close: c,
			Volume: 1_000_000, Turnover: c * 1_000_000,
		}
	}
	return out
}

// memAnchorStore 是 AdjustmentAnchorStore 的内存实现（测试用）。
type memAnchorStore struct {
	mu    sync.Mutex
	data  map[string]map[string]AdjustmentAnchor // start(YYYY-MM-DD) → symbol → anchor
	saves int
}

func newMemAnchorStore() *memAnchorStore {
	return &memAnchorStore{data: map[string]map[string]AdjustmentAnchor{}}
}

func (m *memAnchorStore) LoadAdjustmentAnchors(_ context.Context, start time.Time, symbols []string) (map[string]AdjustmentAnchor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]AdjustmentAnchor, len(symbols))
	for sym, a := range m.data[start.Format("2006-01-02")] {
		for _, s := range symbols {
			if s == sym {
				out[sym] = a
				break
			}
		}
	}
	return out, nil
}

func (m *memAnchorStore) SaveAdjustmentAnchors(_ context.Context, start time.Time, anchors []AdjustmentAnchor) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saves++
	key := start.Format("2006-01-02")
	if m.data[key] == nil {
		m.data[key] = map[string]AdjustmentAnchor{}
	}
	// 首写者胜：已存在的 symbol 不被改写。
	for _, a := range anchors {
		if _, ok := m.data[key][a.Symbol]; !ok {
			m.data[key][a.Symbol] = a
		}
	}
	return nil
}

func rev18Prepare(t *testing.T, store *memAnchorStore, symbols []string, full func(string) []domain.OHLCV) *adjustmentRun {
	t.Helper()
	run, err := prepareAdjustmentRun(context.Background(), store, rev18Start, symbols, full, zerolog.Nop())
	require.NoError(t, err)
	return run
}

func TestAdjustmentRun_FirstSightCapturesRangeStartAnchor(t *testing.T) {
	t.Parallel()

	store := newMemAnchorStore()
	bars := rev18Bars("600000.SH", 10.0, 10.2, 10.1)
	run := rev18Prepare(t, store, []string{"600000.SH"},
		func(string) []domain.OHLCV { return bars })

	require.Equal(t, 1, len(run.captured), "应新采集 1 个 anchor")
	a := run.captured[0]
	assert.Equal(t, "600000.SH", a.Symbol)
	assert.Equal(t, rev18Start, a.AnchorDate)
	assert.InDelta(t, 10.0, a.AnchorClose, 1e-12)
	assert.NotEmpty(t, a.SeriesDigest, "冻结必须带全序列 digest")

	// 首跑：ratio = 1，但取数窗口仍要过 1e-6 量化网格。
	got, err := run.normalize("600000.SH", bars[:1])
	require.NoError(t, err)
	assert.Exactly(t, quantizeBars(bars[:1]), got)
}

func TestAdjustmentRun_RebaseRestoresFrozenScaleExactly(t *testing.T) {
	t.Parallel()

	// 首跑：S = [10.0, 10.2, 10.1]，冻结 anchor=10.0@01-02 + 全序列 digest。
	store := newMemAnchorStore()
	original := rev18Bars("600000.SH", 10.0, 10.2, 10.1)
	run1 := rev18Prepare(t, store, []string{"600000.SH"}, func(string) []domain.OHLCV { return original })
	require.NoError(t, store.SaveAdjustmentAnchors(context.Background(), rev18Start, run1.captured))
	run1.flush(context.Background(), zerolog.Nop())

	// 模拟重同步：整条曲线 ×0.5（前复权基准换成了更晚的交易日）。
	// 用 rev18ScaleAll：只缩放价格四件套 —— 成交量/成交额不随复权基准变化
	// （normalizeDailyOHLCV 明确「不乘复权 scale」），造数必须尊重这一点。
	drifted := rev18ScaleBars(original, 0.5)

	run2 := rev18Prepare(t, store, []string{"600000.SH"}, func(string) []domain.OHLCV { return drifted })
	assert.Equal(t, 1, run2.rescaled, "重跑应识别出基准漂移并缩放")
	run2.flush(context.Background(), zerolog.Nop())

	// 增长窗口逐日缩放，必须与首跑的全序列归一化逐位一致。
	norm1, err := run1.normalize("600000.SH", original)
	require.NoError(t, err)
	for cut := 1; cut <= len(drifted); cut++ {
		got, err := run2.normalize("600000.SH", drifted[:cut])
		require.NoError(t, err, "cut=%d", cut)
		assert.Exactly(t, norm1[:cut], got, "cut=%d：窗口前缀必须与首跑归一化逐位相同（REV-18 红线）", cut)
	}
}

func TestAdjustmentRun_RebaseMissingAnchorDateFailsLoud(t *testing.T) {
	t.Parallel()

	store := newMemAnchorStore()
	run1 := rev18Prepare(t, store, []string{"600000.SH"},
		func(string) []domain.OHLCV { return rev18Bars("600000.SH", 10.0, 10.2) })
	require.NoError(t, store.SaveAdjustmentAnchors(context.Background(), rev18Start, run1.captured))

	// 重跑时首日那根 bar 没了（回填/删行，序列从 01-03 开始）——
	// 不是纯换基准，必须红。
	later := rev18Bars("600000.SH", 10.2, 10.1)
	for i := range later {
		later[i].Date = later[i].Date.AddDate(0, 0, 1)
	}
	_, err := prepareAdjustmentRun(context.Background(), store, rev18Start,
		[]string{"600000.SH"}, func(string) []domain.OHLCV { return later }, zerolog.Nop())
	require.ErrorIs(t, err, ErrAdjustmentAnchorMissing)
}

func TestAdjustmentRun_PriceCorrectionFailsLoud(t *testing.T) {
	t.Parallel()

	store := newMemAnchorStore()
	run1 := rev18Prepare(t, store, []string{"600000.SH"},
		func(string) []domain.OHLCV { return rev18Bars("600000.SH", 10.0, 10.2, 10.1) })
	require.NoError(t, store.SaveAdjustmentAnchors(context.Background(), rev18Start, run1.captured))

	// 数据修正：锚点日价格没动（10.0，缩放比 = 1），但次日收盘从 10.2 改成
	// 9.9 —— 换基准解释不了这个，digest 必然对不上，fail loud。
	_, err := prepareAdjustmentRun(context.Background(), store, rev18Start,
		[]string{"600000.SH"},
		func(string) []domain.OHLCV { return rev18Bars("600000.SH", 10.0, 9.9, 10.1) },
		zerolog.Nop())
	require.ErrorIs(t, err, ErrAdjustmentDrift)
}

func TestAdjustmentRun_WarmMissSymbolPassesThrough(t *testing.T) {
	t.Parallel()

	store := newMemAnchorStore()
	// 暖机失败：fullBars 返回空 → 该 symbol 不钉，直通（量化对齐网格）。
	run := rev18Prepare(t, store, []string{"600000.SH"},
		func(string) []domain.OHLCV { return nil })
	assert.Equal(t, 1, run.skipped)
	assert.Empty(t, run.captured)

	bars := rev18Bars("600000.SH", 10.123456789, 10.2)
	got, err := run.normalize("600000.SH", bars)
	require.NoError(t, err)
	assert.Exactly(t, quantizeBars(bars), got, "未钉住的 symbol 直通但走同一量化网格")
}

func TestAdjustmentRun_DisabledIsIdentity(t *testing.T) {
	t.Parallel()

	bars := rev18Bars("600000.SH", 10.123456789, 10.2)
	run := &adjustmentRun{enabled: false} // 无 store：直通
	got, err := run.normalize("600000.SH", bars)
	require.NoError(t, err)
	assert.Exactly(t, bars, got, "disabled 快照必须零改动（无库环境行为不变）")
}

func TestAdjustmentRun_ConcurrentNormalizeStable(t *testing.T) {
	t.Parallel()

	store := newMemAnchorStore()
	drifted := rev18ScaleBars(rev18Bars("600000.SH", 10.0, 10.2, 10.1), 0.5)
	run1 := rev18Prepare(t, store, []string{"600000.SH"},
		func(string) []domain.OHLCV { return rev18Bars("600000.SH", 10.0, 10.2, 10.1) })
	require.NoError(t, store.SaveAdjustmentAnchors(context.Background(), rev18Start, run1.captured))
	run2 := rev18Prepare(t, store, []string{"600000.SH"}, func(string) []domain.OHLCV { return drifted })

	want, err := run2.normalize("600000.SH", drifted)
	require.NoError(t, err)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := run2.normalize("600000.SH", drifted)
			assert.NoError(t, err)
			assert.Exactly(t, want, got, "并发归一化结果必须与串行一致（fetchMarketDataForDay 是并行的）")
		}()
	}
	wg.Wait()
}

func TestQuantizePrice_SnapshotsToGrid(t *testing.T) {
	t.Parallel()

	assert.InDelta(t, 10.5, quantizePrice(10.5), 1e-15)
	assert.InDelta(t, 10.123457, quantizePrice(10.123456789), 1e-12)
	assert.Exactly(t, 0.0, quantizePrice(0))
}
