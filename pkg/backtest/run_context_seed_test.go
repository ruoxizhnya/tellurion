package backtest

// QT-12 验收 · 共享 Engine 的跨 run 串扰消失。
//
// 历史 bug 的形状（不是假想的，是 walk-forward 当年被迫「每窗口一个新
// Engine」的真实原因）：两个 run 共用一份 Engine，B 的 Warm 是**整体替换**
// 语义（f.cache = combined），于是覆盖了 A 正在读取的那份 —— A 拿到的是
// 别的日期区间的因子，不报错、只是结果随并发顺序变。
//
// 修法是把可变运行态搬进 per-run 的 runContext。本文件钉的就是这个不变量：
// **一个 run 写不进另一个 run，也写不回引擎**。
//
// 为什么这些用例是确定性的、而不是「开两个 goroutine 祈祷撞上」：
// 串扰的根因是**共享对象**，不是调度时机 —— 把「B 先写、A 后读」这个顺序
// 直接摆出来，就比并发对撞更强（它必然执行到那两条指令，且不依赖 -race；
// Windows 上没有 race detector，靠对撞的测试在这里等于没测）。

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"tellurion/pkg/marketdata"
	"tellurion/pkg/storage"
	"tellurion/pkg/strategy"
)

func seedDay() time.Time { return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) }

func barsFor(symbol string, days int, start time.Time) []domain.OHLCV {
	out := make([]domain.OHLCV, days)
	for i := 0; i < days; i++ {
		out[i] = domain.OHLCV{
			Symbol: symbol,
			Date:   start.AddDate(0, 0, i),
			Open:   10, High: 11, Low: 9, Close: 10 + float64(i)/10,
			Volume: 1000,
		}
	}
	return out
}

// 因子缓存的「整体替换」串扰是当年最危险的一条：写进去的是另一个窗口的
// 日期区间，读出来却完全像正常数据。
func TestRunContext_FactorCacheSeedIsCopiedNotShared(t *testing.T) {
	eng := newTestEngine(t)
	day := seedDay()
	eng.LoadFactorCache(map[domain.FactorType]map[time.Time]map[string]float64{
		domain.FactorMomentum: {day: {"600000.SH": 1.5}},
	})

	rcA := newTestRun(t, eng)
	rcB := newTestRun(t, eng)

	// B 拿到的是同一条读数（种子真的传下来了）……
	z, ok := rcB.factorZScore(domain.FactorMomentum, day, "600000.SH")
	require.True(t, ok, "装配期种子必须进本 run")
	assert.InDelta(t, 1.5, z, 1e-9)

	// ……然后 B 用自己的数据 Warm（整体替换语义），A 正在读的那份不能动。
	rcB.factor.Load(map[domain.FactorType]map[time.Time]map[string]float64{
		domain.FactorMomentum: {day: {"600000.SH": 99.0}},
	})

	zA, okA := rcA.factorZScore(domain.FactorMomentum, day, "600000.SH")
	require.True(t, okA, "A run 的因子读数被 B run 覆盖 —— QT-12 的隔离失效")
	assert.InDelta(t, 1.5, zA, 1e-9, "A 读到 99.0 等于读到别的 run 的数据")

	// 引擎那份种子也不许被任何一侧改写：它是装配期对象，
	// run 期间「谁都不写引擎」是本单的不变量。
	zSeed, okSeed := eng.GetFactorZScore(domain.FactorMomentum, day, "600000.SH")
	require.True(t, okSeed)
	assert.InDelta(t, 1.5, zSeed, 1e-9, "run 写回了引擎级种子")
}

// B 先预热、A 后读 —— 历史上就是这个交错顺序把 A 的数据换掉的。
func TestRunContext_OHLCCacheWarmDoesNotReachOtherRun(t *testing.T) {
	prov := marketdata.NewInMemoryProvider()
	startA := seedDay()
	startB := seedDay().AddDate(0, 0, 40)
	prov.LoadOHLCV("600000.SH", append(barsFor("600000.SH", 10, startA), barsFor("600000.SH", 10, startB)...))

	eng := newTestEngineWithProvider(t, prov)

	rcA := newTestRunSpan(t, eng, domain.BacktestParams{StartDate: startA, EndDate: startA.AddDate(0, 0, 9)})
	rcB := newTestRunSpan(t, eng, domain.BacktestParams{StartDate: startB, EndDate: startB.AddDate(0, 0, 9)})

	require.NoError(t, rcB.warmCache(context.Background(), []string{"600000.SH"}), "B 先预热")
	require.NoError(t, rcA.warmCache(context.Background(), []string{"600000.SH"}), "A 后预热")

	// A 的缓存里只该有 A 那 10 天：B 的数据混进来（合并语义下区间变宽）
	// 说明两个 run 共用了一份账。
	barsA := rcA.peekOHLCV("600000.SH")
	require.Len(t, barsA, 10, "A run 的 L1 缓存混进了别的 run 的 bar")
	assert.Equal(t, startA, barsA[0].Date)

	barsB := rcB.peekOHLCV("600000.SH")
	require.Len(t, barsB, 10)
	assert.Equal(t, startB, barsB[0].Date)

	// 引擎级缓存仍是空的：run 期间没有任何路径写回装配期对象。
	assert.Empty(t, eng.CacheManager().Snapshot(),
		"run 把数据写回了引擎（QT-12 的「run 不写 Engine」不变量破了）")
}

// 财报 / 上市日历 / 日内快照同理：A 预热到的，B 读不到；引擎读不到。
func TestRunContext_PerRunSnapshotsAreNotShared(t *testing.T) {
	eng := newTestEngine(t)

	rcA := newTestRun(t, eng)
	rcB := newTestRun(t, eng)

	pe := 12.5
	rcA.mu.Lock()
	rcA.fundamentals = map[string]strategy.FundamentalSeries{
		"600519.SH": {{Symbol: "600519.SH", Date: seedDay(), PE: &pe}},
	}
	delist := seedDay().AddDate(1, 0, 0)
	rcA.listing = map[string]storage.ListingWindow{"600519.SH": {List: seedDay(), Delist: &delist}}
	rcA.intraday = map[string][]domain.OHLCV{"600519.SH": barsFor("600519.SH", 3, seedDay())}
	rcA.mu.Unlock()

	assert.Nil(t, rcB.fundamentalsSnapshot(), "B run 读到了 A run 的财报")
	assert.Nil(t, rcB.intradaySnapshot(), "B run 读到了 A run 的日内快照")
	assert.Len(t, rcB.listingSnapshot(), 0, "B run 读到了 A run 的上市日历")

	// 引擎侧同样是「写不回去」：这些字段现在是装配期种子。
	assert.Len(t, eng.fundamentals, 0)
	assert.Len(t, eng.listing, 0)
	assert.Len(t, eng.intraday, 0)
}

// 真跑一场回测的两笔并发：结果必须与各自串行逐位一致。
//
// 这一条是「不回归」的粗筛，不声称自己有牙：仓内没有 store，两个 run 的
// 财报/因子都来自同一份装配期种子，因此**不会**产生可观测的分歧 —— 历史上
// 那个「B 覆盖 A 正在读的因子」分歧需要真库预热才能重现。有牙的那两条腿在
// 本文件上面（B 先 Warm、A 后读的确定性交错 + 因子整体替换），
// 它们才是 QT-12 的判据；这条只保证并发跑不动坏、不串持仓。
func TestEngine_ConcurrentRunsOnSharedEngineMatchSoloResults(t *testing.T) {
	prov := marketdata.NewInMemoryProvider()
	start := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	symbols := []string{"600000.SH", "000001.SZ", "600519.SH"}
	days := make([]time.Time, 0, 70)
	for d := 0; d < 70; d++ {
		days = append(days, start.AddDate(0, 0, d))
	}
	for _, sym := range symbols {
		prov.LoadOHLCV(sym, barsFor(sym, 70, start))
	}
	prov.SetTradingDays(days)
	stocks := make([]domain.Stock, 0, len(symbols))
	for _, sym := range symbols {
		stocks = append(stocks, domain.Stock{Symbol: sym, Name: "STK-" + sym, ListDate: start.AddDate(-1, 0, 0)})
	}
	prov.LoadStocks(stocks)

	// 刻意**不**调 LoadOHLCVInMemory：那样等于把全量数据塞进装配期种子，
	// 每个 run 一开工就拿到别人的区间，本单要测的「各自预热各自那份」就没了。
	// 风控必须是 in-process 的（d6RiskManager）—— 否则 detectRegime 会逐日
	// POST 风控服务，测试环境里每天挂 30 秒（第一次实跑就是这样卡死的）。
	eng, err := NewEngine(viperD6(), prov, zerolog.Nop())
	require.NoError(t, err)
	eng.SetRiskManager(d6RiskManager(t))
	eng.SetParallelWorkers(2)

	reqLong := BacktestRequest{
		Strategy: "momentum", StockPool: symbols,
		StartDate: "2024-01-02", EndDate: "2024-02-29",
		InitialCapital: 1_000_000, RiskFreeRate: 0.03,
	}
	reqShort := BacktestRequest{
		Strategy: "momentum", StockPool: symbols,
		StartDate: "2024-01-02", EndDate: "2024-01-21",
		InitialCapital: 1_000_000, RiskFreeRate: 0.03,
	}

	ctx := context.Background()
	soloLong, err := eng.RunBacktest(ctx, reqLong)
	require.NoError(t, err)
	soloShort, err := eng.RunBacktest(ctx, reqShort)
	require.NoError(t, err)

	// 同一台引擎、两个重叠区间，这次并发跑。
	var concurrentLong, concurrentShort *BacktestResponse
	var errLong, errShort error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		concurrentLong, errLong = eng.RunBacktest(ctx, reqLong)
	}()
	go func() {
		defer wg.Done()
		concurrentShort, errShort = eng.RunBacktest(ctx, reqShort)
	}()
	wg.Wait()
	require.NoError(t, errLong)
	require.NoError(t, errShort)

	assertResponsesIdentical(t, soloLong, concurrentLong, "长区间")
	assertResponsesIdentical(t, soloShort, concurrentShort, "短区间")

	// 并发跑完之后，引擎那份装配期对象必须还是干净的（run 写不回引擎）。
	assert.Empty(t, eng.CacheManager().Snapshot(), "并发 run 把 bar 写进了引擎级 L1 缓存")
}

func assertResponsesIdentical(t *testing.T, want, got *BacktestResponse, label string) {
	t.Helper()
	require.NotNil(t, want)
	require.NotNil(t, got)
	assert.InDelta(t, want.TotalReturn, got.TotalReturn, 1e-12, "%s 总收益随并发顺序变了", label)
	assert.Equal(t, want.TotalTrades, got.TotalTrades, "%s 成交笔数变了", label)
	require.Len(t, got.PortfolioValues, len(want.PortfolioValues), "%s 净值点位数变了", label)
	for i := range want.PortfolioValues {
		assert.Equal(t, want.PortfolioValues[i].Date, got.PortfolioValues[i].Date)
		assert.InDelta(t, want.PortfolioValues[i].TotalValue, got.PortfolioValues[i].TotalValue, 1e-9,
			"%s 净值第 %d 点漂移", label, i)
	}
}

// run 状态包按 run 计：并发两笔各自的账互不相干。
//
// 「各自串行逐位一致」在内存态这一层的直接版本：两笔同时在跑时，每一笔看到的
// 预热结果都只该是自己那份。这条有牙（把 rc.cache 换成共享的那一步会当场红）。
func TestRunContext_ConcurrentWarmIsIndependentPerRun(t *testing.T) {
	prov := marketdata.NewInMemoryProvider()
	start := seedDay()
	all := barsFor("600000.SH", 90, start)
	prov.LoadOHLCV("600000.SH", all)

	eng := newTestEngineWithProvider(t, prov)

	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	warmed := make(chan int64, 2)
	windows := []domain.BacktestParams{
		{StartDate: start, EndDate: start.AddDate(0, 0, 29)},
		{StartDate: start.AddDate(0, 0, 30), EndDate: start.AddDate(0, 0, 59)},
	}
	started := make(chan struct{})
	for _, w := range windows {
		wg.Add(1)
		go func(p domain.BacktestParams) {
			defer wg.Done()
			rc := newRunContext(eng, p)
			defer rc.close()
			<-started
			if err := rc.warmCache(context.Background(), []string{"600000.SH"}); err != nil {
				errCh <- err
				return
			}
			bars := rc.peekOHLCV("600000.SH")
			if len(bars) != 30 {
				errCh <- fmt.Errorf("本 run 预热到 %d 根 bar，期望 30（区间 %s~%s）",
					len(bars), p.StartDate.Format("01-02"), p.EndDate.Format("01-02"))
				return
			}
			warmed <- int64(len(bars))
		}(w)
	}
	close(started)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("并发预热失败：%v", err)
	}
	assert.Len(t, warmed, 2, "两个 run 都要各自预热成功")
}

// 注入的种子里，财报/上市/日内这三份是**只读引用**（量级小、run 内不写）。
// 这条把口径钉住：将来谁想给它们加写路径，必须先想清楚跨 run 可见性。
func TestRunContext_ReadOnlySeedsAreSharedButNeverWritten(t *testing.T) {
	eng := newTestEngine(t)
	pe := 9.0
	eng.SetFundamentals(map[string]strategy.FundamentalSeries{
		"A": {{Symbol: "A", Date: seedDay(), PE: &pe}},
	})
	windows := testWindows()
	eng.SetListingWindows(windows)

	rc := newTestRun(t, eng)
	assert.Equal(t, windows, rc.listingSnapshot(), "种子要传下来")
	assert.Len(t, rc.fundamentalsSnapshot(), 1)

	// eligibleUniverse 读 run 那份：与引擎那份一致，但不经由引擎。
	got := rc.eligibleUniverse([]string{"ALIVE", "GONE", "FUTURE"}, date("2020-06-01"), nil)
	assert.Equal(t, []string{"ALIVE", "GONE"}, got)
}

// run 收尾必须把内存账还回去：连续 50 笔不同票池之后进程账要回到 0。
// 漏一次就是慢性泄漏（槽位少一格 / 台账多一条），且只会越跑越明显。
//
// ⚠️ 验收 ⑤-2 原样写的是「连续 50 个不同票池 run 后 RSS 回落基线 ±10%」。
// 这条测的是**账本**（committed / ledger / 指标余量），不是 RSS：单测里拿
// runtime.MemStats 断言 ±10% 会随 GC 时机飘（堆回落发生在 GC 之后，不在
// close() 里），那种断言红了也说明不了泄漏。账本归零是 RSS 回落的必要条件，
// 也是这里唯一能确定的判据；真实的 RSS 曲线仍需在长时程压测里看，
// 已按缺口登记在 docs/TASKS.md 的 QT-12 进度里。
func TestRunContext_AccountedBytesReturnToBaseline(t *testing.T) {
	prov := marketdata.NewInMemoryProvider()
	const poolCount = 50
	symbols := make([]string, 0, poolCount)
	for i := 0; i < poolCount; i++ {
		sym := fmt.Sprintf("60%04d.SH", i)
		prov.LoadOHLCV(sym, barsFor(sym, 20, seedDay()))
		symbols = append(symbols, sym)
	}

	eng := newTestEngineWithProvider(t, prov)
	rec := newRecordingObserver()
	eng.SetRunObserver(rec)
	require.NotNil(t, eng.admission, "默认装配就该有准入闸")

	start := seedDay()
	params := domain.BacktestParams{StartDate: start, EndDate: start.AddDate(0, 0, 19)}

	require.Equal(t, int64(0), eng.admission.committed, "起点：进程账必须是 0")

	for i := 0; i < poolCount; i++ {
		// 按 RunBacktest 的真实顺序走一遍：建状态包 → 抢准入凭证 → 记内存账
		// → 预热 → 收尾。少任何一步都测不到那条泄漏路径（凭证没拿到就去
		// applyAdmission 记的账，close 时无从退还）。
		rc := newRunContext(eng, params)
		lease, err := eng.admission.acquire(context.Background())
		require.NoError(t, err, "第 %d 笔抢槽位失败", i)
		rc.lease = lease
		pool := []string{symbols[i]}
		require.NoError(t, rc.applyAdmission(pool))
		require.NoError(t, rc.warmCache(context.Background(), pool))
		rc.publishCacheMetrics()
		rc.close()
	}

	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	t.Logf("50 笔 run 收尾后的堆读数（只作观察，不断言）：HeapAlloc=%d KiB", before.HeapAlloc/1024)

	assert.Equal(t, int64(0), eng.admission.committed,
		"run 全部收尾后进程内存账没归零 —— 凭证/账本漏还，跑久了就是 OOM 或饿死")
	assert.Len(t, eng.admission.ledger, 0, "缓存台账里还留着已结束 run 的条目")
	assert.Empty(t, eng.admission.slots, "槽位没全部还回来")

	// 观察者侧同样要归零：漏了 retireRun，看板会永远停在最后一笔的大小。
	require.True(t, rec.seenCacheBytes("ohlcv"), "预热后的缓存账必须报过一次（否则指标是空的）")
	assert.Greater(t, rec.cacheBytes["ohlcv"][0], int64(0), "报上去的该是有内容的一份账")
	assert.Equal(t, int64(0), rec.lastCacheBytes("ohlcv"),
		"backtest_cache_bytes 没收敛回 0 —— 内存已释放，指标还在报占用")
	require.NotEmpty(t, rec.runsActive)
	// 挂上观察者那一刻就报一次（attachObserver 里的 publishCounts）：
	// 序列若从 run 结束才开始存在，看板上「空白」和「没接线」就分不出来。
	assert.Equal(t, 0, rec.runsActive[0], "装配期就该有一条 0 的读数")
	assert.Contains(t, rec.runsActive, 1, "抢到槽位的那一刻必须报出在跑数 = 1")
	assert.Equal(t, 0, rec.runsActive[len(rec.runsActive)-1], "runs_active 末值必须是 0")
	// wasm 没注入取数源 ⇒ 报 0，而不是不报（序列存在才看得出它是空的）。
	assert.True(t, len(rec.wasmBytes) > 0, "wasm 账要报，值可以是 0")
	assert.Equal(t, int64(0), rec.wasmBytes[len(rec.wasmBytes)-1])
}
