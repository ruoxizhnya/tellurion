package backtest

// QT-23 的验收腿：RunUniverse（run 级稳定票池）。
//
// 三条要证的事：
//  1. RunUniverse = sort(dedup(StockPool ∪ IndexCode 区间成分并集))，run 前
//     一次算完；
//  2. 连续 5 个交易日 Universe 有增有减、且有停牌 ⇒ RunUniverse 与
//     symbol_idx 映射不变；
//  3. RunUniverse 相同 ⇒ dataset/run_spec hash 相同（与请求里的输入顺序无关）。

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"tellurion/pkg/marketdata"
	"tellurion/pkg/storage"
	"tellurion/pkg/strategy"
)

// ─── 1. 冻结公式 ───────────────────────────────────────────────────

type fakeIndexReader struct {
	byDay map[string][]string
	errs  map[string]error
}

func (f *fakeIndexReader) GetIndexConstituentsByDate(_ context.Context, _ string, date time.Time) ([]string, error) {
	key := date.Format("2006-01-02")
	if err := f.errs[key]; err != nil {
		return nil, err
	}
	return f.byDay[key], nil
}

func TestUnionRunUniverse_SortedDedupAndIndexUnion(t *testing.T) {
	d0 := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	days := []time.Time{d0, d0.AddDate(0, 0, 1), d0.AddDate(0, 0, 2)}
	reader := &fakeIndexReader{
		byDay: map[string][]string{
			"2024-01-02": {"C", "A"},
			"2024-01-03": {"D"},
			// 2024-01-04 读失败：该日成分不并入，但不阻断。
		},
		errs: map[string]error{"2024-01-04": errors.New("db down")},
	}

	got := unionRunUniverse(context.Background(), []string{"B", "A", "A"}, "000300.SH", days, reader, zerolog.Nop())
	assert.Equal(t, []string{"A", "B", "C", "D"}, got,
		"必须 sort(dedup(StockPool ∪ 区间成分并集))；读失败的日期不阻断")
}

func TestUnionRunUniverse_NoIndexCodeUsesPoolOnly(t *testing.T) {
	got := unionRunUniverse(context.Background(), []string{"c", "a", "b"}, "", nil, nil, zerolog.Nop())
	assert.Equal(t, []string{"a", "b", "c"}, got)
}

// ─── 2. 5 日增减 + 停牌：RunUniverse / symbol_idx 不变 ──────────────

type qt23ScopeRecorder struct {
	mu     sync.Mutex
	scopes []strategy.Scope
}

func (r *qt23ScopeRecorder) Signals(_ context.Context, scope strategy.Scope, _ time.Time) ([]strategy.Signal, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	scope.RunUniverse = append([]string(nil), scope.RunUniverse...)
	scope.Universe = append([]string(nil), scope.Universe...)
	r.scopes = append(r.scopes, scope)
	return nil, nil
}

func (r *qt23ScopeRecorder) snapshot() []strategy.Scope {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]strategy.Scope(nil), r.scopes...)
}

func TestRunUniverse_StableAcrossFiveDaysWithSuspension(t *testing.T) {
	const days = 5
	start := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)

	// 请求顺序故意打乱；RunUniverse 必须自行排序。
	pool := []string{"600003.SH", "600000.SH", "600002.SH", "600001.SH"}
	alive, enter, exit, susp := "600000.SH", "600001.SH", "600002.SH", "600003.SH"

	prov := marketdata.NewInMemoryProvider()
	for _, sym := range pool {
		bars := synthOHLCV([]string{sym}, days, start)[sym]
		if sym == susp {
			// 摘掉第 3 天（index 2）的 bar ⇒ 当天末根不是当日 = 停牌。
			bars = append(append([]domain.OHLCV(nil), bars[:2]...), bars[3:]...)
		}
		prov.LoadOHLCV(sym, bars)
	}
	tradingDays := make([]time.Time, days)
	for i := 0; i < days; i++ {
		tradingDays[i] = start.AddDate(0, 0, i)
	}
	prov.SetTradingDays(tradingDays)
	prov.LoadStocks([]domain.Stock{
		{Symbol: alive, Name: "ALIVE", ListDate: start.AddDate(-1, 0, 0)},
		{Symbol: enter, Name: "ENTER", ListDate: start.AddDate(0, 0, 2)},
		{Symbol: exit, Name: "EXIT", ListDate: start.AddDate(-1, 0, 0)},
		{Symbol: susp, Name: "SUSP", ListDate: start.AddDate(-1, 0, 0)},
	})

	delist := start.AddDate(0, 0, 3)
	eng, err := NewEngine(provenanceViper(42, 0.0003, 0.001, 0.001), prov, zerolog.Nop())
	require.NoError(t, err)
	rec := &qt23ScopeRecorder{}
	applyOptions(eng, append(provenanceBaseOptions(t), WithSignalProvider(rec)))
	eng.SetListingWindows(map[string]storage.ListingWindow{
		alive: {List: start.AddDate(-1, 0, 0)},
		enter: {List: start.AddDate(0, 0, 2)},
		exit:  {List: start.AddDate(-1, 0, 0), Delist: &delist},
		susp:  {List: start.AddDate(-1, 0, 0)},
	})

	_, err = eng.RunBacktest(context.Background(), BacktestRequest{
		Strategy:       "qt23-no-signal",
		StockPool:      pool,
		StartDate:      start.Format("2006-01-02"),
		EndDate:        start.AddDate(0, 0, days-1).Format("2006-01-02"),
		InitialCapital: 1_000_000,
		RiskFreeRate:   0.03,
	})
	require.NoError(t, err)

	scopes := rec.snapshot()
	require.Len(t, scopes, days, "每个交易日都应拿到一次 Scope")

	wantRunUniverse := []string{"600000.SH", "600001.SH", "600002.SH", "600003.SH"}
	indexOf := func(universe []string) map[string]int {
		m := make(map[string]int, len(universe))
		for i, sym := range universe {
			m[sym] = i
		}
		return m
	}
	wantIndex := indexOf(wantRunUniverse)

	for i, sc := range scopes {
		assert.Equal(t, wantRunUniverse, sc.RunUniverse,
			"第 %d 天 RunUniverse 必须逐位不变（停牌/调出不得重排）", i)
		assert.Equal(t, wantIndex, indexOf(sc.RunUniverse),
			"第 %d 天 symbol_idx 映射必须不变", i)
	}

	// 当日名单（Scope.Universe）确实在增减：逐日列出来自证。
	gotDaily := make([][]string, len(scopes))
	for i, sc := range scopes {
		gotDaily[i] = sc.Universe
	}
	assert.Equal(t, []string{"600000.SH", "600002.SH", "600003.SH"}, gotDaily[0])
	assert.Equal(t, []string{"600000.SH", "600002.SH", "600003.SH"}, gotDaily[1])
	assert.Equal(t, []string{"600000.SH", "600001.SH", "600002.SH"}, gotDaily[2],
		"600001 上市、600003 停牌 —— 当日名单变了")
	assert.Equal(t, []string{"600000.SH", "600001.SH", "600002.SH", "600003.SH"}, gotDaily[3])
	assert.Equal(t, []string{"600000.SH", "600001.SH", "600003.SH"}, gotDaily[4],
		"600002 摘牌，当日名单又变了")

	// 停牌只从当日名单消失，仍留在 RunUniverse 且序号不变。
	assert.NotContains(t, gotDaily[2], susp)
	assert.Contains(t, scopes[2].RunUniverse, susp)
	assert.Equal(t, wantIndex[susp], indexOf(scopes[2].RunUniverse)[susp])
}

func TestTradableUniverse_DropsSuspendedPreservingOrder(t *testing.T) {
	in := []string{"A", "B", "C", "D"}
	got := tradableUniverse(in, map[string]bool{"B": true, "D": true})
	assert.Equal(t, []string{"A", "C"}, got)
	assert.Equal(t, []string{"A", "B", "C", "D"}, tradableUniverse(in, nil),
		"没有停牌时必须原样返回（零分配）")
}

// ─── 3. 同一 RunUniverse ⇒ 同一 dataset / run_spec hash ─────────────

func TestRunProvenance_SameRunUniverseSameHashRegardlessOfInputOrder(t *testing.T) {
	registerQT63Probe(t)

	const days = 20
	canonical := []string{"600000.SH", "600001.SH"}
	prov := provenanceProvider(t, canonical, days) // 数据固定，与请求顺序无关

	run := func(pool []string, runID string) storage.RunProvenanceRecord {
		t.Helper()
		capture := &provenanceCapture{}
		eng, err := NewEngine(provenanceViper(42, 0.0003, 0.001, 0.001), prov, zerolog.Nop())
		require.NoError(t, err)
		applyOptions(eng, append(provenanceBaseOptions(t), WithRunProvenanceWriter(capture)))

		resp, err := eng.RunBacktest(context.Background(), BacktestRequest{
			Strategy:       qt63ProbeName,
			StockPool:      pool,
			StartDate:      provenanceTestStart.Format("2006-01-02"),
			EndDate:        provenanceTestStart.AddDate(0, 0, days-1).Format("2006-01-02"),
			InitialCapital: 1_000_000,
			RiskFreeRate:   0.03,
			RunID:          runID,
		})
		require.NoError(t, err)
		require.Equal(t, StatusCompleted, resp.Status)
		return capture.last(t)
	}

	a := run(canonical, "qt23-order-a")
	b := run([]string{"600001.SH", "600000.SH"}, "qt23-order-b")

	assert.Equal(t, a.DatasetSnapshotHash, b.DatasetSnapshotHash,
		"同一 RunUniverse（只是请求顺序不同）必须得到同一个 dataset_snapshot_hash")
	assert.Equal(t, a.RunSpecHash, b.RunSpecHash,
		"run_spec_hash 同样由 RunUniverse 决定，不受输入顺序影响")
	assert.Equal(t, a.ParamsHash, b.ParamsHash)
}
