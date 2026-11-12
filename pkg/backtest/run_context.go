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

	cdom "github.com/ruoxizhnya/tellurion-contracts/domain"
	"tellurion/pkg/backtest/cache"
	"tellurion/pkg/strategy"

	apperrors "github.com/ruoxizhnya/tellurion-kit/errors"
)

import "tellurion/pkg/domain"

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
	listing      map[string]ListingWindow
	intraday     map[string][]domain.OHLCV
	// dailyBasic 是本 run 的逐日估值快照（symbol → 按 trade_date 升序）。
	// 只在策略声明 ValuationAware 时预热一次；回测期间按回放日做 PIT 截取，
	// 不做运行时查询（蓝图 §4.3 红线）。
	dailyBasic map[string][]domain.DailyBasicValuation

	// provider 是本 run 的信号供给（QT-11 的 run 级实例缓存在它里面）。
	provider strategy.SignalProvider

	// runUniverse 是本 run 稳定的票池超集（QT-23）：run 前一次算完，
	// 期间只增不减。日循环只读，不写。空 = 尚未解析（直接调 runBacktestInternal
	// 的测试路径），此时回落到入场票池。
	runUniverse []string

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
	seedDailyBasic := e.dailyBasic
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
		dailyBasic:   seedDailyBasic,
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
	rc.provider = rc.newSignalProvider(params.StrategyName)
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

// factorCoverage 返回必需因子中「run 级物化缓存仍无条目」的那些。
//
// **唯一判据入口**：闸只认 run 级物化结果（按需求值后合并的那份 run 私有
// 缓存）。`public.factor_cache` 已删除（CORE-FACTOR-CACHE-RETIRE），任何
// 「去库里看有没有因子」的判断都会把一张不存在的表当成「管线坏了」——
// 破坏验证就是把这个函数改回读 DB，用例必须变红。
func (rc *runContext) factorCoverage(required []cdom.FactorType) []cdom.FactorType {
	return rc.factor.MissingFactors(required)
}

// ensureRequiredFactors 是「因子覆盖度闸」：策略声明的必需因子**整体**
// 一条物化结果都没有时拒跑。
//
// 为什么必须在这里拦（2026-11-11 真栈实测）：策略读到的全是「不存在」时，
// generateSignalsFromFactorCache 逐票 continue，最后以 invalid/zero_trades
// 收场 —— 那个结论看起来像「策略不赚钱」，实际是「没有因子」。缺因子必须
// 响亮失败，不能伪装成一份空的回测。
//
// 2026-11-12 改口径：判据从「库里的 factor_cache 有没有条目」改为「本 run
// 按需求值后有没有物化结果」。这张表已退役（ADR-037 修订），看它等于
// 让门禁永远看到空表 —— 内存物化做好了多因子照样 409。
//
// 判据的三条边界（都来自任务单）：
//   - 只看**条目有无**，不看 z-score 取值：某因子本期全为 0 是正常数据；
//   - 只拦「全部缺失」：部分因子可用时按可用因子加权，是策略既有语义；
//   - 与窗口长短无关：这里不判断预热期够不够（那是 invalid_reasons 的事）。
func (rc *runContext) ensureRequiredFactors(strategyName string) error {
	strat, err := strategy.DefaultRegistry.NewInstance(strategyName)
	if err != nil {
		// 本地注册表没有该策略（未加载 / 拼错）：执行路径随后会明确报错，
		// 这里不越俎代庖。
		return nil
	}
	fd, ok := strat.(strategy.FactorDependent)
	if !ok {
		return nil // 价量策略：不依赖因子缓存，不做任何覆盖度核对
	}
	required := fd.RequiredFactors()
	if len(required) == 0 {
		return nil
	}

	missing := rc.factorCoverage(required)
	if len(missing) == 0 {
		return nil
	}
	if len(missing) < len(required) {
		names := make([]string, 0, len(missing))
		for _, f := range missing {
			names = append(names, string(f))
		}
		rc.e.logger.Warn().
			Str("strategy", strategyName).
			Strs("missing_factors", names).
			Msg("部分必需因子无缓存 —— 按可用因子加权继续（只有全部缺失才拒跑）")
		return nil
	}
	return errRequiredFactorsUnavailable(strategyName, missing, rc.start, rc.warmEnd)
}

// warmFundamentalsIfNeeded 只在「策略声明要财报」或「必需因子的输入字段
// 命中财报」时预热一次（P2-12；2026-11-12 扩到因子派生）。
//
// 为什么扩：ADR-037 D2 的按需求值要现场算 value(1/pe) 与 quality(roe)，
// 而 multi_factor 本身不是 FundamentalAware —— 只按策略声明判断，派生
// 时 provider 会因「没有财报数据」明确报错，因子照旧缺失。三个「不」的
// 原文口径不变：不需要就不查、只查一次、失败不阻断。
//
// 数据源（2026-11-12，跨仓 ADR-0012）：`market.stock_fundamentals` —— 唯一写者
// 是 tellurion-data，core 只读；`public.stock_fundamentals` 写点随 pkg/data
// 退役、实测 0 行。PIT 判据仍是 `available_date`（data 侧生成列
// `COALESCE(ann_date, trade_date)`），「披露前不可见」的语义不变。
func (rc *runContext) warmFundamentalsIfNeeded(ctx context.Context, strategyName string, symbols []string, asOf time.Time) error {
	// QT-11：类型探测走 NewInstance（执行路径不许摸注册表里的共享实例）。
	strat, err := strategy.DefaultRegistry.NewInstance(strategyName)
	if err != nil {
		return nil // 本地注册表没有该策略（未加载/拼错）：不预热
	}
	needFundamentals := false
	if _, ok := strat.(strategy.FundamentalAware); ok {
		needFundamentals = true
	}
	if !needFundamentals {
		if fd, ok := strat.(strategy.FactorDependent); ok {
			needFundamentals = len(factorsNeedFundamentals(fd.RequiredFactors())) > 0
		}
	}
	if !needFundamentals {
		return nil // 价量策略且因子不依赖财报：不付这份查询成本
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

// warmValuationsIfNeeded 预热 run 内逐日估值快照（CORE-STRATEGY-SCREEN-LOCAL-VALUE）。
//
// 范式同 warmFundamentalsIfNeeded：策略不要就不查、只查一次、失败不阻断
// （run 流程会打 Warn；策略用到价值条件时以 ErrDataGap 明确失败，不静默
// 退回纯动量）。
//
// 数据源是 `market.daily_basic`（写者归 tellurion-data）。窗口取
// `[start, end]`，另取 start 之前最近一条 —— 首个决策日若恰好是周末/停牌，
// 按 `trade_date <= 决策日` 的 PIT 口径仍要有可用估值，否则会造出假的
// 零覆盖缺口。
func (rc *runContext) warmValuationsIfNeeded(
	ctx context.Context, strategyName string, symbols []string, start, end time.Time,
) error {
	strat, err := strategy.DefaultRegistry.NewInstance(strategyName)
	if err != nil {
		return nil // 本地注册表没有该策略：与估值预热无关
	}
	if _, ok := strat.(strategy.ValuationAware); !ok {
		return nil // 不用价值维度的策略：不付这份查询成本
	}

	rc.mu.RLock()
	seeded := rc.dailyBasic
	rc.mu.RUnlock()
	if len(seeded) > 0 {
		rc.e.logger.Info().
			Int("symbols", len(seeded)).
			Msg("DailyBasic valuation snapshot already seeded — skip warm-up")
		return nil
	}

	rc.e.mu.RLock()
	store := rc.e.store
	rc.e.mu.RUnlock()
	if store == nil {
		// 装配缺口不许静默（同 intraday 范式）：策略明确要价值维度，而
		// 进程里没有 DB —— 没有快照可预热，value_screen 随后会报数据缺口。
		rc.e.logger.Warn().
			Str("strategy", strategyName).
			Msg("store 未装配 —— 估值快照不会预热；价值筛选将明确报数据缺口（缺失≠0，不静默空选）")
		return nil
	}

	prior, err := store.GetDailyBasicValuations(ctx, symbols, start.AddDate(0, 0, -1))
	if err != nil {
		return err
	}
	history, err := store.GetDailyBasicHistory(ctx, symbols, start, end)
	if err != nil {
		return err
	}

	merged := mergeDailyBasicSnapshot(prior, history)
	rc.mu.Lock()
	rc.dailyBasic = merged
	rc.mu.Unlock()

	rows := 0
	for _, series := range merged {
		rows += len(series)
	}
	rc.e.logger.Info().
		Int("symbols", len(symbols)).
		Int("stocks_with_valuations", len(merged)).
		Int("valuation_rows", rows).
		Time("start", start).
		Time("end", end).
		Msg("DailyBasic valuation snapshot warm-up completed")
	return nil
}

// mergeDailyBasicSnapshot 把「start 之前最近一条」与区间内逐日行拼成
// 按日期升序的序列。区间行本身已升序（storage 契约）。
func mergeDailyBasicSnapshot(
	prior map[string]domain.DailyBasicValuation,
	history map[string][]domain.DailyBasicValuation,
) map[string][]domain.DailyBasicValuation {
	out := make(map[string][]domain.DailyBasicValuation, len(history)+len(prior))
	for symbol, rows := range history {
		out[symbol] = rows
	}
	for symbol, p := range prior {
		rows := out[symbol]
		// prior 严格早于 start；若区间首行与它同日（理论上不会），以区间行为准。
		if len(rows) > 0 && !rows[0].TradeDate.After(p.TradeDate) {
			continue
		}
		merged := make([]domain.DailyBasicValuation, 0, len(rows)+1)
		merged = append(merged, p)
		merged = append(merged, rows...)
		out[symbol] = merged
	}
	return out
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

// dailyBasicSnapshot 返回本 run 的逐日估值快照（只读语义同上）。
func (rc *runContext) dailyBasicSnapshot() map[string][]domain.DailyBasicValuation {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	return rc.dailyBasic
}

// intradaySnapshot 返回本 run 的日内快照（只读语义同上）。
func (rc *runContext) intradaySnapshot() map[string][]domain.OHLCV {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	return rc.intraday
}

// listingSnapshot 返回本 run 的上市日历（装配层种子注入的那份）。
func (rc *runContext) listingSnapshot() map[string]ListingWindow {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	return rc.listing
}

// factorZScore 读本 run 的因子缓存。Engine.GetFactorZScore 是引擎级
// 种子的访问器（外部 API 不变），run 流程一律走这里。
func (rc *runContext) factorZScore(factor cdom.FactorType, date time.Time, symbol string) (float64, bool) {
	return rc.factor.Get(factor, date, symbol)
}

// setRunUniverse 冻结本 run 的稳定票池（QT-23）。只在日循环之前调用一次。
func (rc *runContext) setRunUniverse(universe []string) {
	rc.runUniverse = universe
}

// stableUniverse 返回索引来源：已冻结的 RunUniverse；未冻结（测试直接调
// runBacktestInternal）时回落到入场票池。日循环里所有「按天过滤」都以它
// 为输入，保证输出顺序是 RunUniverse 的子序列、索引不随当日名单漂移。
func (rc *runContext) stableUniverse(fallback []string) []string {
	if len(rc.runUniverse) > 0 {
		return rc.runUniverse
	}
	return fallback
}

// eligibleUniverse 返回 date 当天真正参与回测的股票集合（P2-4）。
// 两个来源取并集与「没有日历就原样返回」的口径都在 engine 侧注释里，
// 本方法只把 listing 的来源换成 run 私有那份。
//
// QT-23：传入的 pool 应当是 **RunUniverse**（稳定、排序后）。输出保持
// 输入顺序 ⇒ 当日名单永远是 RunUniverse 的子序列；停牌/退市只让票从
// 子序列里消失，不会让其它票的序号前移。
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
func (rc *runContext) newSignalProvider(strategyName string) strategy.SignalProvider {
	// QT-26：显式注入了同名 WASM 制品 ⇒ 走 WASM provider。制品是装配期
	// 只读配置，命中即选；本地注册表不再参与该 run 的信号生成。
	rc.e.mu.RLock()
	artifact, hasArtifact := rc.e.wasmArtifacts[strategyName]
	injected := rc.e.signalProvider
	budget := rc.e.wasmShardBudget
	observer := rc.e.wasmShardObserver
	rc.e.mu.RUnlock()
	if hasArtifact {
		// QT-25：分片并发默认跟随本 run 的取数并行度（configured_workers）；
		// 预算由 WithWASMShardBudget 覆盖，零值走 512 MiB / 64 MiB 默认。
		if budget.Workers <= 0 {
			budget.Workers = rc.workers
		}
		return newWASMSignalProvider(artifact, wasmShardConfig{Budget: budget, Observer: observer}, rc.e.logger)
	}
	if injected != nil {
		return injected
	}
	p := strategy.NewLocalSignalProvider(nil)
	// factor/fundamentals 绑的是 **run 私有的**读法（QT-12）：
	// 引擎级 method value 会让本 run 读到别的 run 写进引擎的那份。
	p.SetFactorReader(rc.factorZScore)
	p.SetFundamentalsProvider(rc.fundamentalsSnapshot)
	p.SetValuationProvider(rc.dailyBasicSnapshot)
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
func (rc *runContext) beginProviderSession(ctx context.Context, state *BacktestState, axis []time.Time) (func() error, error) {
	p := rc.provider
	meta := strategy.RunMeta{
		RunID:    state.ID,
		Strategy: state.Params.StrategyName,
		// QT-20：生效参数（默认 ⊕ 覆写）随 run 身份一次性交给 provider。
		// 这是**本 run 私有**的 map：state 由 RunBacktest 每笔新建，并发 run
		// 各持一份，provider 拿到的是自己的那份而不是共享引用。
		Params: state.Params.Params,
		Start:  state.Params.StartDate,
		End:    state.Params.EndDate,
		// QT-23/QT-26：WASM 的 symbol_idx 只能由 RunUniverse 派生。
		Universe:    state.Params.StockPool,
		RunUniverse: rc.stableUniverse(state.Params.StockPool),
		Axis:        axis,
	}
	// QT-25：symbol scope 的分片跑完一个再换下一个，需要整段行情。数据来自
	// 本 run 的 L1 缓存快照（引擎已在 runUniverse 冻结后预热 RunUniverse），
	// 只读引用；其余 provider 不填充，行为零变化。
	if fp, ok := p.(strategy.FullMarketDataProvider); ok && fp.NeedsFullMarketData() {
		meta.MarketData = rc.cache.Snapshot()
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
