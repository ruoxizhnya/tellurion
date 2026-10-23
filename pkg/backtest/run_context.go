package backtest

// runContext 是**一次回测私有的可变状态**（QT-12 · A7 + A10）。
//
// 修的是什么：这些可变的运行态原来挂在 Engine 上，而 Engine 是共享对象 ——
// A run 的 warm 覆盖了 B run 正在读的缓存（A7），L1 OHLCV 缓存跨 run 单调
// 合并且永不清理（A10，慢性 OOM），财报/上市日历/日内快照同理。串扰的后果
// 不是报错而是**结果随并发顺序变**：walk-forward 曾在生产上因此改成
// 「每窗口一个新 Engine」（cmd/analysis/main.go 与 setup.go 的注释），
// 那是绕开共享可变态，不是修掉它。
//
// 边界（口径照任务单 ⑥）：本结构只管「run 级状态隔离 + 内存账」。
// Engine 上那些字段还在，但语义降为**装配期种子**（SetFundamentals /
// LoadOHLCVInMemory / LoadFactorCache / SetIntradaySnapshot /
// SetListingWindows 写的是种子）：每个 run 开工时拷一份自己的，run 期间
// 谁都不写引擎级字段。新增引擎级可变字段由 repoguard 拦
// （internal/repoguard/engine_field_whitelist_test.go）。
//
// 为什么不是「per-run Engine」：Engine 装配要把 store / router / adapter /
// riskManager / executionBridge 全接一遍（cmd/analysis/setup.go 那 60 行），
// 每 run 重建等于把装配期错误推到运行期，且并发 run 会各自打一遍 DB 连接
// 池。种子 + run 作用域状态是同一个不变量的低成本形态。

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"tellurion/pkg/backtest/cache"
	"tellurion/pkg/storage"
	"tellurion/pkg/strategy"

	apperrors "tellurion/pkg/errors"
)

// runContext 承载一个 run 的全部可变运行态。字段一律不导出：只有本包
// 的 run 流程能碰，调用方拿到的是 Engine 的稳定 API。
type runContext struct {
	// e 是只读装配（logger / store / provider / router / config）。
	// run 流程**不得**写它的可变字段 —— 见类型注释里的边界。
	e *Engine

	// cache 是本 run 私有的 L1 OHLCV 缓存。种子来自引擎级注入
	// （LoadOHLCVInMemory / 测试 bench），warm 只写自己这份。
	cache *CacheManager

	// factor 是本 run 私有的因子 z-score 缓存。Copy 而不是共享：
	// Warm 是整体替换语义，共享 map 等于 A run 写进 B run 正在读的那份。
	factor *FactorCacheAccessor

	// mu 护下面三张表：run 内是单线程日循环，但快照方法在导出面上
	// （Snapshot / 注入 provider 的 method value）可能被并发读。
	mu           sync.RWMutex
	fundamentals map[string]strategy.FundamentalSeries
	listing      map[string]storage.ListingWindow
	intraday     map[string][]domain.OHLCV

	// provider 是本 run 的信号供给（QT-11 的 run 级实例缓存在它里面）。
	provider strategy.SignalProvider

	// start / end 是本 run 的回放区间（UTC 零点的交易日）。
	start time.Time
	end   time.Time

	// warmEnd 是**实际**预加载窗口的终点。它等于 end 是常态；内存准入
	// 不够时按降级顺序往回收（QT-12 ④：降 W → 降预加载窗口 → 拒绝）。
	// 收窗口不改结果：缓存未命中的日子退回 provider，取到的是同一份数。
	warmEnd time.Time

	// workers 是本 run 的并行取数度（降级第一档：往 1 收）。
	workers int

	// wantBytes 是准入时记进进程账的估算字节；释放凭证时按同一个数退，
	// 账本才成对。
	wantBytes int64

	// lease 是准入闸发的凭证，run 结束必须释放（RunBacktest 里 defer）。
	lease *admissionLease
}

// newRunContext 建一个 run 作用域的状态包，并从引擎级种子拷一份起点。
//
// 参数 params 是本 run 的回放区间；workers 取引擎配置（<=0 归一成 1，
// 与 fetchMarketDataForDay 的既有口径一致）。
func newRunContext(e *Engine, params domain.BacktestParams) *runContext {
	e.mu.RLock()
	seedCache := e.cache
	seedFactor := e.factor
	seedFundamentals := e.fundamentals
	seedListing := e.listing
	seedIntraday := e.intraday
	seedWorkers := e.parallelWorkers
	e.mu.RUnlock()

	workers := seedWorkers
	if workers <= 0 {
		workers = 1
	}

	rc := &runContext{
		e:            e,
		cache:        cache.NewCacheManager(e.logger),
		fundamentals: seedFundamentals,
		listing:      seedListing,
		intraday:     seedIntraday,
		start:        params.StartDate,
		end:          params.EndDate,
		warmEnd:      params.EndDate,
		workers:      workers,
	}
	if seedFactor != nil {
		rc.factor = seedFactor.Copy()
	} else {
		rc.factor = cache.NewFactorCacheAccessor(e.logger)
	}
	if seedCache != nil {
		if snap := seedCache.Snapshot(); len(snap) > 0 {
			// Load 浅拷贝外层 map，bar 切片本体共享（只读约定）——
			// run 内合并只换 map 里的值，不会写回种子的底层数组。
			rc.cache.Load(snap)
		}
	}
	rc.provider = rc.newSignalProvider()
	return rc
}

// ─── 预热（全部写本 run，不写引擎）───────────────────────────────────

// warmCache 把本 run 的 L1 OHLCV 预加载进**自己**的缓存。
//
// fast-path 跳过、bulk fetch、排序与快照发布都在 CacheManager 里
// （P1-16 ADR-020 的拆分），本方法只负责「用的是 rc 的区间与 rc 的缓存」。
func (rc *runContext) warmCache(ctx context.Context, symbols []string) error {
	return rc.cache.Warm(ctx, symbols, rc.start, rc.warmEnd, rc.e.effectiveProvider)
}

// warmFactorCache 预热因子 z-score。
//
// Typed-nil 守卫照旧：e.store 是具体指针类型，直接塞进接口参数会造出
// 非 nil 的 nil 接口（Go 的 typed-nil 陷阱），Warm 会解引用空接收者。
func (rc *runContext) warmFactorCache(ctx context.Context) error {
	rc.e.mu.RLock()
	store := rc.e.store
	rc.e.mu.RUnlock()
	if store == nil {
		return nil
	}
	return rc.factor.Warm(ctx, rc.start, rc.warmEnd, store)
}

// warmFundamentalsIfNeeded 只在策略声明要财报时预热一次（P2-12）。
// 三个「不」的原文口径见 engine.go 同名注释的历史版本，语义未变：
// 策略不要就不查、只查一次、失败不阻断 —— 变的只有「写给谁」。
func (rc *runContext) warmFundamentalsIfNeeded(ctx context.Context, strategyName string, symbols []string, asOf time.Time) error {
	// QT-11：类型探测走 NewInstance（执行路径不许摸注册表里的共享实例）。
	strat, err := strategy.DefaultRegistry.NewInstance(strategyName)
	if err != nil {
		return nil // 本地注册表没有该策略（未加载/拼错）：不预热
	}
	if _, ok := strat.(strategy.FundamentalAware); !ok {
		return nil // 价量策略：不付这份查询成本
	}

	rc.e.mu.RLock()
	store := rc.e.store
	rc.e.mu.RUnlock()
	if store == nil {
		return nil // 没有 DB：策略会在用到 pe/pb 时明确报错
	}

	records, err := store.GetFundamentalsPITBulk(ctx, symbols, asOf)
	if err != nil {
		return err
	}

	rc.mu.Lock()
	rc.fundamentals = records
	rc.mu.Unlock()

	rc.e.logger.Info().
		Int("symbols", len(symbols)).
		Int("stocks_with_fundamentals", len(records)).
		Time("as_of", asOf).
		Msg("Fundamentals warm-up completed")
	return nil
}

// loadListingCalendar 预热上市日历（P2-4）。取不到不报错 —— 没有日历只是
// 「治不了幸存者偏差」，而 rc.listing 保持种子值（可能是调用方注入的那份）。
func (rc *runContext) loadListingCalendar(ctx context.Context) {
	rc.e.mu.RLock()
	store := rc.e.store
	rc.e.mu.RUnlock()
	if store == nil {
		return
	}

	windows, err := store.GetListingWindows(ctx)
	if err != nil {
		rc.e.logger.Warn().Err(err).Msg(
			"Listing calendar load failed — 无法按日在市过滤，池子仍是当前上市名单（幸存者偏差未修）")
		return
	}
	if len(windows) == 0 {
		rc.e.logger.Warn().Msg(
			"stocks 表为空 — 无法按日在市过滤，池子仍是当前上市名单（幸存者偏差未修）")
		return
	}

	delisted := 0
	for _, w := range windows {
		if w.Delist != nil {
			delisted++
		}
	}
	rc.mu.Lock()
	rc.listing = windows
	rc.mu.Unlock()

	rc.e.logger.Info().
		Int("stocks", len(windows)).
		Int("delisted", delisted).
		Msg("Listing calendar loaded")
}

// warmIntradayIfNeeded 预热日内分钟快照（D6；范式同 warmFundamentalsIfNeeded）。
// 三个「不」不变：策略不要就不查、只走 Router、失败不阻断。
func (rc *runContext) warmIntradayIfNeeded(ctx context.Context, strategyName string, symbols []string, start, end time.Time) error {
	strat, err := strategy.DefaultRegistry.NewInstance(strategyName)
	if err != nil {
		return nil // 本地注册表没有该策略（未加载/拼错）：与日内预热无关
	}
	if _, ok := strat.(strategy.IntradayAware); !ok {
		return nil // 纯日线策略：不付这份成本
	}

	rc.e.mu.RLock()
	router := rc.e.router
	rc.e.mu.RUnlock()
	if router == nil {
		// 装配缺口不许静默（QT-64 ③-③）：策略明确要日内特征，而进程里
		// 没有取数通道 —— 这时既没有快照也不会预热，必须当场说清楚，
		// 而不是等策略跑到特征时才发现「怎么全是不可得」。策略侧随后
		// 会因 nil 供给明确失败（见 intradayFeatureProvider）。
		rc.e.logger.Warn().
			Str("strategy", strategyName).
			Msg("marketdata.Router 未装配 —— D6 日内快照不会预热；该 IntradayAware 策略将明确失败（缺失≠0，不静默产出全零信号）")
		return nil
	}

	// 交易日区间 → 取数区间：引擎的 start/end 是 UTC 零点的交易日，而 1m
	// 路由按 ts BETWEEN 取数（D1 口径）——展开到 [start, end+1d) 才覆盖
	// 每个交易日 09:30 CST 之后的全部分钟 bar。
	endExclusive := end.AddDate(0, 0, 1)

	snapshot, totalBars, err := loadIntradaySnapshot(ctx, router, symbols, start, endExclusive)
	if err != nil {
		return err
	}

	rc.mu.Lock()
	rc.intraday = snapshot
	rc.mu.Unlock()

	rc.e.logger.Info().
		Int("symbols", len(snapshot)).
		Int("minute_bars", totalBars).
		Time("start", start).
		Time("end", end).
		Msg("Intraday snapshot warm-up completed (D6)")
	return nil
}

// ─── 读路径（本 run 私有）────────────────────────────────────────────

// getOHLCV 读本 run 的 L1 缓存，未命中退回 provider。
func (rc *runContext) getOHLCV(ctx context.Context, symbol string, start, end time.Time) ([]domain.OHLCV, error) {
	return rc.cache.Get(ctx, symbol, start, end, rc.e.effectiveProvider)
}

// peekOHLCV 只读缓存、不打 provider（calculatePosition 与复权锚点用）。
func (rc *runContext) peekOHLCV(symbol string) []domain.OHLCV {
	return rc.cache.Peek(symbol)
}

// fundamentalsSnapshot 返回本 run 的财报缓存（不复制：每交易日对齐一次，
// 几万条复制一遍不值得；调用方只读）。
func (rc *runContext) fundamentalsSnapshot() map[string]strategy.FundamentalSeries {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	return rc.fundamentals
}

// intradaySnapshot 返回本 run 的日内快照（只读语义同上）。
func (rc *runContext) intradaySnapshot() map[string][]domain.OHLCV {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	return rc.intraday
}

// listingSnapshot 返回本 run 的上市日历（种子注入的那份，或
// loadListingCalendar 从库里读回的那份）。
func (rc *runContext) listingSnapshot() map[string]storage.ListingWindow {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	return rc.listing
}

// factorZScore 读本 run 的因子缓存。Engine.GetFactorZScore 是引擎级
// 种子的访问器（外部 API 不变），run 流程一律走这里。
func (rc *runContext) factorZScore(factor domain.FactorType, date time.Time, symbol string) (float64, bool) {
	return rc.factor.Get(factor, date, symbol)
}

// eligibleUniverse 返回 date 当天真正参与回测的股票集合（P2-4）。
// 两个来源取并集与「没有日历就原样返回」的口径都在 engine 侧注释里，
// 本方法只把 listing 的来源换成 run 私有那份。
func (rc *runContext) eligibleUniverse(pool []string, date time.Time, held map[string]bool) []string {
	listing := rc.listingSnapshot()
	if len(listing) == 0 {
		return pool
	}

	out := make([]string, 0, len(pool))
	for _, s := range pool {
		w, known := listing[s]
		switch {
		case !known:
			// 日历里查不到这只票 —— 不知道就放进去了？不。
			// 宁可漏也不能凭空造：留它在池子里等于假设「它一直在市」，
			// 那正是要修的偏差本身。但要如实记一笔，否则池子悄悄缩水。
			rc.e.logger.Debug().Str("symbol", s).Time("date", date).
				Msg("Symbol absent from listing calendar — excluded from universe")
		case w.IsListed(date):
			out = append(out, s)
		case held[s]:
			out = append(out, s) // 已退市但仍持仓：留着等强平
		}
	}
	return out
}

// intradayFeatureProvider 构造「绑定回放日」的日内特征供给（引擎每个
// 交易日调一次，供 LocalSignalProvider 注入 IntradayAware 策略）。
// 按日绑定的理由见 intraday.go 的原注释：只暴露当日，结构性挡住偷看。
//
// 快照为空时返回 nil（QT-64）：LocalSignalProvider 的契约是「nil 供给
// 不注入」—— 策略拿不到供给就会明确失败。此前这里恒返回一个非 nil 的
// 空快照供给，策略侧「特征不可得」被读成「今天没有信号」，表现成静默
// 全零信号；装配缺 Router 时尤其如此。
func (rc *runContext) intradayFeatureProvider(day time.Time) strategy.IntradayFeatureProvider {
	snapshot := rc.intradaySnapshot()
	if len(snapshot) == 0 {
		return nil
	}
	return &snapshotIntradayProvider{
		snapshot: snapshot,
		dayStart: shanghaiDayStart(day),
		dayEnd:   shanghaiDayStart(day).AddDate(0, 0, 1),
	}
}

// newSignalProvider 为本 run 建信号供给（QT-11 的 run 级实例缓存住在里面）。
//
// 注入自定义 provider 时原样返回那个对象：它的会话由 beginProviderSession
// 随 run 开关（QT-14），本方法只负责选出对象。
func (rc *runContext) newSignalProvider() strategy.SignalProvider {
	if injected := rc.e.signalProvider; injected != nil {
		return injected
	}
	p := strategy.NewLocalSignalProvider(nil)
	// factor/fundamentals 绑的是 **run 私有的**读法（QT-12）：
	// 引擎级 method value 会让本 run 读到别的 run 写进引擎的那份。
	p.SetFactorReader(rc.factorZScore)
	p.SetFundamentalsProvider(rc.fundamentalsSnapshot)
	return p
}

// beginProviderSession 随 run 开关 provider 会话（QT-14 ②③）。
//
// 规则（p0-remediation-design.md A1）：
//   - provider 实现 SessionLifecycle ⇒ BeginRun，返回的收尾函数在 run 退出
//     时 EndRun（错误路径同样走到，调用方挂 defer）；
//   - provider 声明 Stateful 却没有生命周期 ⇒ 当场失败 —— 这是装配期校验
//     （strategy.ValidateSessionLifecycle）之外的第二道门：绕过构造函数
//     塞进来的 stateful provider 不能静默跑；
//   - 顺序 run 同样走 Begin/End，不做「复用会话」优化（A1 规则 5）。
//
// 返回的收尾函数把 EndRun 的错误原样上交：End 失败 = finalize/Close 没做成，
// 结果不可信 —— 调用方（runBacktestInternal）在本 run 尚无更早错误时把它
// 接管为 run 失败。
func (rc *runContext) beginProviderSession(ctx context.Context, state *BacktestState) (func() error, error) {
	p := rc.provider
	meta := strategy.RunMeta{
		RunID:    state.ID,
		Strategy: state.Params.StrategyName,
		// Params 通路归 QT-20（defaults ⊕ 覆写）；本单先留位，如实为空。
		Params:   nil,
		Start:    state.Params.StartDate,
		End:      state.Params.EndDate,
		Universe: state.Params.StockPool,
	}
	if lc, ok := p.(strategy.SessionLifecycle); ok {
		if err := lc.BeginRun(ctx, meta); err != nil {
			return nil, err
		}
		runID := meta.RunID
		return func() error { return lc.EndRun(ctx, runID) }, nil
	}
	if sf, ok := p.(strategy.Stateful); ok && sf.Stateful() {
		return nil, apperrors.New(apperrors.ErrCodeInternal,
			fmt.Sprintf("signal provider %T declares Stateful() but does not implement SessionLifecycle — 有状态 provider 必须实现 BeginRun/EndRun（QT-14 ④：不做可选接口 + 静默降级）；装配期校验是第一道门，这里是纵深防御的第二道", p)).WithOperation("beginProviderSession")
	}
	return func() error { return nil }, nil
}

// ─── 内存账（QT-12 ③④⑤ 的可计量面）─────────────────────────────────

// ohlcvBytes 是本 run L1 OHLCV 缓存的估算字节。
func (rc *runContext) ohlcvBytes() int64 { return rc.cache.Bytes() }

// factorBytes 是本 run 因子缓存的估算字节。
func (rc *runContext) factorBytes() int64 { return rc.factor.Bytes() }

// cacheBytes 是本 run 名下**全部**可计量缓存的估算字节之和。
//
// 口径：L1 OHLCV + 因子 + 日内快照 + 财报。上市日历按行计（每标的一条
// 两个时间戳，量级是标的数 × 32B，相对前三项可忽略），刻意不计 ——
// 宁可少算这一项，也不要为了凑数引入一个没有出处的大小。
func (rc *runContext) cacheBytes() int64 {
	rc.mu.RLock()
	intraday := rc.intraday
	fundamentals := rc.fundamentals
	rc.mu.RUnlock()

	total := rc.cache.Bytes() + rc.factor.Bytes() + cache.OHLCVMapBytes(intraday)
	// 财报走 cache 包的同一组估算常数（三种数据形态必须一个口径），
	// 上市日历按行计的量级相对前三项可忽略，刻意不计 —— 见 cacheBytes 注释。
	total += cache.FundamentalsBytes(fundamentals)
	return total
}

// bars 是本 run L1 缓存里的 bar 总条数（准入估算的第二维）。
func (rc *runContext) bars() int64 { return rc.cache.Bars() }

// publishCacheMetrics 把本 run 的缓存账记进进程台账并报一次
// （QT-12 ⑤ 的 backtest_cache_bytes）。
// 准入闸关闭或没接观察者时静默 —— 两条都在 publishCacheBytes 里判过。
func (rc *runContext) publishCacheMetrics() {
	rc.e.admission.publishCacheBytes(rc)
}

// close 是 run 的收尾：从台账撤下本 run 那份账（顺带重报余量），
// 再把准入凭证还回去。
// RunBacktest 用 `defer rc.close()` 挂着，错误路径同样走到 —— 槽位漏还一次
// 就是永久少一格，台账漏撤一次就是看板上永久多出一份早该释放的字节。
func (rc *runContext) close() {
	rc.e.admission.retireRun(rc)
	rc.lease.Release(rc.wantBytes)
}
