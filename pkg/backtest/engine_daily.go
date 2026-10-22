package backtest

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-contracts/domain"
	apperrors "tellurion/pkg/errors"
	"tellurion/pkg/execalgo"
)

type stockJob struct {
	symbol    string
	prevClose float64
}

type stockResult struct {
	symbol    string
	stock     domain.Stock
	ohlcvData []domain.OHLCV
	price     float64
	prevClose float64
	limitUp   bool
	limitDown bool
	// suspended 为真表示「末根 bar 不是当日的」——当日停牌（或数据缺口），
	// 由 fetchMarketDataForDay 摘出当日 marketDataCache（REV-09）。
	suspended bool
	err       error
}

type corporateActions struct {
	dividendsByDate map[time.Time][]*domain.Dividend
	splitsByDate    map[time.Time][]*domain.Split
}

func (e *Engine) loadCorporateActions(ctx context.Context, start, end time.Time, logger zerolog.Logger) *corporateActions {
	ca := &corporateActions{
		dividendsByDate: make(map[time.Time][]*domain.Dividend),
		splitsByDate:    make(map[time.Time][]*domain.Split),
	}

	e.mu.RLock()
	store := e.store
	e.mu.RUnlock()

	if store == nil {
		return ca
	}

	if divs, err := store.GetDividendsInRange(ctx, start, end); err == nil {
		for _, d := range divs {
			day := d.PayDate.Truncate(24 * time.Hour)
			ca.dividendsByDate[day] = append(ca.dividendsByDate[day], d)
		}
		logger.Info().Int("dividend_events", len(divs)).Msg("Dividend data loaded")
	}

	if splits, err := store.GetSplitsInRange(ctx, start, end); err == nil {
		for _, s := range splits {
			day := s.TradeDate.Truncate(24 * time.Hour)
			ca.splitsByDate[day] = append(ca.splitsByDate[day], s)
		}
		logger.Info().Int("split_events", len(splits)).Msg("Split data loaded")
	}

	return ca
}

func (e *Engine) fetchMarketDataForDay(
	ctx context.Context,
	stockPool []string,
	params domain.BacktestParams,
	date time.Time,
	prevCloseCache map[string]float64,
	diag *fillRunDiag,
	adj *adjustmentRun,
	logger zerolog.Logger,
) (
	marketDataCache map[string][]domain.OHLCV,
	pricesCache map[string]float64,
	stockCache map[string]domain.Stock,
	updatedPrevClose map[string]float64,
	suspended map[string]bool,
) {
	marketDataCache = make(map[string][]domain.OHLCV)
	pricesCache = make(map[string]float64)
	stockCache = make(map[string]domain.Stock)
	updatedPrevClose = make(map[string]float64)
	suspended = make(map[string]bool)

	for k, v := range prevCloseCache {
		updatedPrevClose[k] = v
	}

	workers := e.parallelWorkers
	if workers <= 0 {
		workers = 1
	}

	jobCh := make(chan stockJob, len(stockPool))
	resultCh := make(chan stockResult, len(stockPool))

	var stockWg sync.WaitGroup
	for w := 0; w < workers; w++ {
		stockWg.Add(1)
		go func() {
			defer stockWg.Done()
			for job := range jobCh {
				res := e.processStockJob(ctx, job, params, date, adj)
				resultCh <- res
			}
		}()
	}

	for _, s := range stockPool {
		jobCh <- stockJob{symbol: s, prevClose: prevCloseCache[s]}
	}
	close(jobCh)

	for i := 0; i < len(stockPool); i++ {
		res := <-resultCh
		if res.err != nil {
			logger.Warn().Err(res.err).Msg("Failed to get stock data")
			continue
		}
		stockCache[res.symbol] = res.stock
		if res.suspended {
			// REV-09：当日停牌 —— 不进 marketDataCache（不产信号、不参与
			// 撮合：信号环靠 marketDataCache 找 todayBar，止损环靠
			// suspended 集合显式跳过），pricesCache 仍记最后成交价供估值。
			diag.recordSuspended(res.symbol)
			suspended[res.symbol] = true
		} else {
			marketDataCache[res.symbol] = res.ohlcvData
		}
		if res.price > 0 {
			pricesCache[res.symbol] = res.price
		}
		if res.prevClose > 0 {
			updatedPrevClose[res.symbol] = res.prevClose
		}
	}
	stockWg.Wait()

	return marketDataCache, pricesCache, stockCache, updatedPrevClose, suspended
}

// stockNamesFor projects the day's stock metadata down to the
// symbol -> display-name table the tracker needs for risk-warning
// detection (AUD-22).
//
// Symbols whose Stock could not be fetched are included with an EMPTY
// name rather than dropped: the tracker treats an empty name as "unknown"
// and logs it, whereas a missing key would be indistinguishable from
// "this backtest has no risk-warning stocks". An empty value keeps that
// distinction visible at the point where it matters.
func stockNamesFor(stockCache map[string]domain.Stock) map[string]string {
	names := make(map[string]string, len(stockCache))
	for symbol, stock := range stockCache {
		names[symbol] = stock.Name
	}
	return names
}

func (e *Engine) processStockJob(
	ctx context.Context,
	job stockJob,
	params domain.BacktestParams,
	date time.Time,
	adj *adjustmentRun,
) stockResult {
	res := stockResult{symbol: job.symbol}

	if stock, err := e.getStock(ctx, job.symbol); err == nil {
		res.stock = stock
	}

	ohlcvData, err := e.getOHLCV(ctx, job.symbol, params.StartDate, date)
	if err != nil {
		res.err = apperrors.Wrapf(err, apperrors.ErrCodeDataQuality, "fetch_ohlcv", "symbol %s failed to fetch OHLCV", job.symbol)
		return res
	}
	// REV-18：qfq 复权基准快照 —— 取数后立刻把序列对齐到本区间冻结的基准
	// 刻度（同区间重跑逐位一致）。res 的 price/prevClose 都在这之后从
	// ohlcvData 计算，口径天然一致。adj 为 nil / disabled 时是直通。
	ohlcvData, err = adj.normalize(job.symbol, ohlcvData)
	if err != nil {
		res.err = apperrors.Wrapf(err, apperrors.ErrCodeDataQuality, "adjustment_snapshot", "symbol %s failed to rebase qfq series", job.symbol)
		return res
	}
	res.ohlcvData = ohlcvData

	// REV-09：停牌校验。getOHLCV(start, date) 的末根 bar 必须是**当日**的
	// bar；不是则当日停牌（或数据缺口），末根是停牌前旧 bar。旧代码把它当
	// 「今日 bar」：信号在旧数据上产生、按旧 close 成交、限价单用旧 bar
	// 区间判触及——全是虚假成交。这里只打标记，由 fetchMarketDataForDay
	// 摘出当日 marketDataCache；pricesCache 仍保留最后成交价（停牌持仓照旧
	// 按最后成交价盯市，不因停牌改变估值口径）。
	if len(ohlcvData) > 0 {
		lastBarDate := ohlcvData[len(ohlcvData)-1].Date.Truncate(24 * time.Hour)
		res.suspended = !lastBarDate.Equal(date.Truncate(24 * time.Hour))
	}

	limitUp, limitDown := false, false
	limitPrice := 0.0
	tradeDays := 0
	stockName := res.stock.Name
	if !res.stock.ListDate.IsZero() {
		tradeDays = int(date.Sub(res.stock.ListDate).Hours() / 24 / 7 * 5)
	}

	prevClose := job.prevClose
	if len(ohlcvData) >= 2 {
		if prevClose <= 0 {
			prevClose = ohlcvData[len(ohlcvData)-2].Close
		}
		if prevClose > 0 {
			// AUD-07 (ODR-065 H2): board-aware, date-aware limit rate.
			// The previous inline if/else knew only Normal/ST/New and
			// applied 10% to ChiNext/STAR/BSE symbols. See
			// pricelimit.go for the precedence rules.
			limitRate := resolvePriceLimit(
				PriceLimitInput{
					Symbol:    job.symbol,
					Name:      stockName,
					TradeDays: tradeDays,
					AsOf:      date,
				},
				PriceLimitConfigValues{
					Normal:       e.config.Trading.PriceLimit.Normal,
					ST:           e.config.Trading.PriceLimit.ST,
					STBefore:     e.config.Trading.PriceLimit.STBefore,
					New:          e.config.Trading.PriceLimit.New,
					NewStockDays: e.config.Trading.NewStockDays,
				},
			)
			todayBar := ohlcvData[len(ohlcvData)-1]
			// AUD-07: round the limit prices to the cent before
			// comparing. Exchanges publish limit prices at 0.01 tick,
			// so an unrounded bound can flip the verdict on a bar that
			// closes exactly at the limit.
			upperLimit, lowerLimit := LimitPrices(prevClose, limitRate)
			limitUp = todayBar.Close >= upperLimit
			limitDown = todayBar.Close <= lowerLimit
			if limitUp {
				limitPrice = upperLimit
			} else if limitDown {
				limitPrice = lowerLimit
			}
			todayBar.LimitUp = limitUp
			todayBar.LimitDown = limitDown
			ohlcvData[len(ohlcvData)-1] = todayBar
			res.ohlcvData = ohlcvData
			if limitUp || limitDown {
				res.prevClose = todayBar.Close
			} else {
				res.prevClose = ohlcvData[len(ohlcvData)-1].Close
			}
		}
	}

	if len(ohlcvData) > 0 {
		if limitPrice > 0 {
			res.price = limitPrice
		} else {
			res.price = ohlcvData[len(ohlcvData)-1].Close
		}
	}
	res.limitUp = limitUp
	res.limitDown = limitDown

	return res
}

// openPricesForSignals 把「成交那一天的开盘价」投影成 next_open 用的可成交价表
// （QT-02 · §8.1：T 日收盘决策 → T+1 开盘成交，含滑点由 tracker 照旧负责）。
//
// 读的是 marketDataCache 的末根 bar，而末根恒是**当日**的：停牌 symbol 早在
// REV-09 就被摘出这张表（fetchMarketDataForDay 不放它进来），所以这里取不到
// 停牌前的旧开盘价 —— 那正是旧代码在 same_close 路径上踩过的虚假成交。
//
// 只按**有待成交信号的 symbol** 建表，不是按整个票池：一个没人下单的票池成员
// 缺开盘价不是事件，把它计进诊断会让「这次 run 拒了几笔委托」这个数说谎。
// 开盘价缺失（脏 bar：Open ≤ 0）⇒ 不进表 ⇒ executeSignalTrade 拿到 0 价直接
// 放弃这笔委托（既有语义），这里记一次拒绝。**不退回收盘价成交**：同一份
// 留痕里混两种口径，比少一笔成交坏得多。
func openPricesForSignals(
	signals []domain.Signal,
	marketDataCache map[string][]domain.OHLCV,
	diag *fillRunDiag,
) map[string]float64 {
	opens := make(map[string]float64, len(signals))
	for _, sig := range signals {
		if _, done := opens[sig.Symbol]; done {
			continue // 同 symbol 多条信号只取一次价（也不重复计数）
		}
		// 停牌兜底腿：suspended 的 symbol 已在 processSignalsAndExecuteTrades
		// 里被跳过，这里取不到当日 bar，自然走「无可用价 ⇒ 拒单」。
		bars := marketDataCache[sig.Symbol]
		if len(bars) == 0 {
			diag.recordNoOpenPrice()
			opens[sig.Symbol] = 0
			continue
		}
		if open := bars[len(bars)-1].Open; open > 0 {
			opens[sig.Symbol] = open
			continue
		}
		diag.recordNoOpenPrice()
		opens[sig.Symbol] = 0
	}
	return opens
}

// fillPlan 是「这一批委托按哪一口径成交」的两个派生量（QT-02）。
//
// 为什么打包成一个值而不是两个参数：它们**必须同源**。成交价表与「多老的信号
// 还允许成交」是同一件事的两面 —— next_open 用成交日的开盘价，那它放行的信号
// 就必然是上一个回放日决策的；两者若由调用方各传一次，就能凑出「按昨天的信号
// + 今天的收盘价成交」这种两条口径都不像的混合体。
type fillPlan struct {
	// prices 是成交可用价表。same_close = 当日收盘可成交价（既有形状，与
	// pricesCache 是同一张表）；next_open = 成交日开盘价。
	// 查不到价 ⇒ 该笔委托被拒（executeSignalTrade 的 price <= 0 出口），
	// 不退回别的价成交。
	prices map[string]float64

	// minSignalDate 是**允许成交的最老**信号日期。
	//   - same_close：= 回放的这一天（既有语义：昨天及更早的信号不成交）；
	//   - next_open：= 上一个回放日。决策日与成交日相差一个 bar 是这一口径
	//     的定义本身，若仍按「必须等于今天」过滤，昨天的订单会在撮合前被
	//     全部丢掉 —— 那正是本单第一版实现踩出来的缺口。
	minSignalDate time.Time
}

// sameClosePlan 将当日收盘可成交价与当日新鲜度打包（既有行为）。
func sameClosePlan(pricesCache map[string]float64, date time.Time) fillPlan {
	return fillPlan{prices: pricesCache, minSignalDate: date}
}

func (e *Engine) processSignalsAndExecuteTrades(
	ctx context.Context,
	state *BacktestState,
	signals []domain.Signal,
	marketDataCache map[string][]domain.OHLCV,
	pricesCache map[string]float64,
	plan fillPlan,
	regime *domain.MarketRegime,
	date time.Time,
	algo *execalgo.Dispatcher,
	execAlgoDiag *execAlgoRunDiag,
	suspended map[string]bool,
	fillDiag *fillRunDiag,
	logger zerolog.Logger,
) {
	if len(signals) == 0 {
		return
	}

	validSignals := make([]domain.Signal, 0, len(signals))
	for _, sig := range signals {
		// REV-09 兜底：停牌 symbol 当日不得成交。正常路径上停牌股已被摘出
		// marketDataCache（策略看不到它），但策略若从自有状态吐出该 symbol
		// 的信号，这里必须拦住——否则又会按 pricesCache 里的旧价成交。
		if suspended[sig.Symbol] {
			fillDiag.recordSuspended(sig.Symbol)
			logger.Info().
				Str("symbol", sig.Symbol).
				Time("date", date).
				Msg("Signal skipped: symbol suspended (no bar for today)")
			continue
		}
		if !sig.Date.Before(plan.minSignalDate) {
			validSignals = append(validSignals, sig)
		}
	}
	if len(validSignals) == 0 {
		return
	}

	portfolio := state.Tracker.GetPortfolio(pricesCache)

	positionSizes, err := e.calculatePositionsBatch(ctx, validSignals, portfolio, regime, pricesCache, marketDataCache)
	if err != nil {
		logger.Warn().Err(err).Msg("Batch position sizing failed, falling back to per-signal")
		e.processSignalsFallback(ctx, state, validSignals, marketDataCache, pricesCache, plan, regime, date, algo, execAlgoDiag, suspended, fillDiag, logger)
		return
	}

	for _, signal := range validSignals {
		var todayBar *domain.OHLCV
		if ohlcvData, ok := marketDataCache[signal.Symbol]; ok && len(ohlcvData) > 0 {
			todayBar = &ohlcvData[len(ohlcvData)-1]
			if todayBar.LimitUp && (signal.Direction == domain.DirectionLong || signal.Direction == domain.DirectionShort) {
				logger.Info().
					Str("symbol", signal.Symbol).
					Str("direction", string(signal.Direction)).
					Time("date", date).
					Msg("Trade blocked: stock hit limit-up (涨停), cannot buy")
				continue
			}
			if todayBar.LimitDown && signal.Direction == domain.DirectionClose {
				logger.Info().
					Str("symbol", signal.Symbol).
					Time("date", date).
					Msg("Trade blocked: stock hit limit-down (跌停), cannot sell")
				continue
			}
		}

		execOpts := &OrderExecutionOpts{
			OrderType:  signal.OrderType,
			LimitPrice: signal.LimitPrice,
			DayBar:     todayBar,
		}

		positionSize, hasSize := positionSizes[signal.Symbol]
		if !hasSize || positionSize.Size <= 0 {
			continue
		}

		targetQty := positionSize.Size

		tp, exists := state.TargetPositions[signal.Symbol]
		if !exists {
			tp = &domain.TargetPosition{
				Symbol:      signal.Symbol,
				TargetQty:   0,
				ActualQty:   0,
				PendingQty:  0,
				LastUpdated: date,
			}
			state.TargetPositions[signal.Symbol] = tp
		}

		// 先把策略今天的目标落账，再算实际要下的量 —— 这样即使下面 skip
		// （已达标 / 已超配），`TargetPosition` 记的也是**今天**的目标（AUD-55）。
		tp.TargetQty = targetQty
		tp.LastUpdated = date

		effectiveTarget := e.computeEffectiveTarget(state, tp, targetQty, signal.Direction, date, logger)
		if effectiveTarget < 0 {
			continue
		}

		if effectiveTarget > 0 {
			e.executeSignalTrade(ctx, state, algo, execAlgoDiag, fillDiag, signal, effectiveTarget, plan.prices, date, execOpts, tp, logger)
		}
	}
}

func (e *Engine) processSignalsFallback(
	ctx context.Context,
	state *BacktestState,
	signals []domain.Signal,
	marketDataCache map[string][]domain.OHLCV,
	pricesCache map[string]float64,
	plan fillPlan,
	regime *domain.MarketRegime,
	date time.Time,
	algo *execalgo.Dispatcher,
	execAlgoDiag *execAlgoRunDiag,
	suspended map[string]bool,
	fillDiag *fillRunDiag,
	logger zerolog.Logger,
) {
	for _, signal := range signals {
		// REV-09 兜底：停牌 symbol 当日不得成交（同 processSignalsAndExecuteTrades）。
		if suspended[signal.Symbol] {
			fillDiag.recordSuspended(signal.Symbol)
			continue
		}
		var todayBar *domain.OHLCV
		if ohlcvData, ok := marketDataCache[signal.Symbol]; ok && len(ohlcvData) > 0 {
			todayBar = &ohlcvData[len(ohlcvData)-1]
			if todayBar.LimitUp && (signal.Direction == domain.DirectionLong || signal.Direction == domain.DirectionShort) {
				continue
			}
			if todayBar.LimitDown && signal.Direction == domain.DirectionClose {
				continue
			}
		}
		execOpts := &OrderExecutionOpts{
			OrderType:  signal.OrderType,
			LimitPrice: signal.LimitPrice,
			DayBar:     todayBar,
		}
		portfolio := state.Tracker.GetPortfolio(pricesCache)
		currentPrice := pricesCache[signal.Symbol]
		positionSize, err := e.calculatePosition(ctx, signal, portfolio, regime, currentPrice)
		if err != nil || positionSize.Size <= 0 {
			continue
		}
		targetQty := positionSize.Size
		tp, exists := state.TargetPositions[signal.Symbol]
		if !exists {
			tp = &domain.TargetPosition{
				Symbol:      signal.Symbol,
				TargetQty:   0,
				ActualQty:   0,
				PendingQty:  0,
				LastUpdated: date,
			}
			state.TargetPositions[signal.Symbol] = tp
		}
		tp.TargetQty = targetQty
		tp.LastUpdated = date

		effectiveTarget := e.computeEffectiveTarget(state, tp, targetQty, signal.Direction, date, logger)
		if effectiveTarget < 0 {
			continue
		}

		if effectiveTarget > 0 {
			e.executeSignalTrade(ctx, state, algo, execAlgoDiag, fillDiag, signal, effectiveTarget, plan.prices, date, execOpts, tp, logger)
		}
	}
}

// computeEffectiveTarget 决定一条 Long / Short 信号**实际**要下多少量。
//
// AUD-55（AUD-53 的根因）：原实现只在 `tp.PendingQty > 0` 时才做
// 「目标 − 已持」抵扣，而 `PendingQty` 是**上一次成交后写下的缓存值** ——
// 恰好达标时它等于 0、超配时它小于 0，两种情况下抵扣都不生效。配上一个
// **无状态**策略（`momentum` 每天对 top-N 重发全量 `Long`、从不读持仓），
// 后果是**每天重发一次全量买单、每天被 `insufficient cash` 拒一次**。
// 这就是 AUD-53「回测结果随价格水平 / 资金量级漂移 20 个百分点」的通道。
//
// 修法有两半，缺一不可：
//
//  1. 抵扣**无条件**执行 —— 不再看 `PendingQty` 的符号。
//  2. 已持仓**每次实时问 tracker**，不读 `tp.ActualQty` 这个缓存。
//     能改持仓的路径不止 `executeSignalTrade`：止损 / 止盈平仓、拆股、
//     退市强平都直接落在 tracker 上。逐一在每处补同步是「靠记得」，
//     漏一处就退化回原 bug；读真值则结构上不可能漏（AUD-50 的同一教训）。
//
// 返回值 < 0 表示「本条信号不产生委托」（已达标或已超配）。
func (e *Engine) computeEffectiveTarget(
	state *BacktestState,
	tp *domain.TargetPosition,
	targetQty float64,
	direction domain.Direction,
	date time.Time,
	logger zerolog.Logger,
) float64 {
	// Close / Hold 不走「目标 − 已持」：Close 是全平（`executeSignalTrade`
	// 直接调 `Tracker.ClosePosition`，不看这个数），Hold 是空信号。
	if direction != domain.DirectionLong && direction != domain.DirectionShort {
		return targetQty
	}

	held := e.heldQty(state, tp.Symbol)
	effectiveTarget := targetQty - held
	if effectiveTarget <= 0 {
		logger.Info().
			Str("symbol", tp.Symbol).
			Float64("actual_qty", held).
			Float64("pending_qty", tp.PendingQty).
			Float64("new_target", targetQty).
			Time("date", date).
			Msg("Signal skipped: already at or above target")
		return -1
	}
	if effectiveTarget < targetQty {
		logger.Info().
			Str("symbol", tp.Symbol).
			Float64("actual_qty", held).
			Float64("pending_qty", tp.PendingQty).
			Float64("new_target", targetQty).
			Float64("effective_target", effectiveTarget).
			Time("date", date).
			Msg("Adjusted target: netting actual owned qty")
	}
	return effectiveTarget
}

// heldQty 读 tracker 里的**真实**持仓量，负数（空头）按 0 处理 ——
// 本函数只服务多头 / 空头的**加仓差额**，不承担反向平仓的推理。
func (e *Engine) heldQty(state *BacktestState, symbol string) float64 {
	if state == nil || state.Tracker == nil {
		return 0
	}
	pos, ok := state.Tracker.GetPosition(symbol)
	if !ok || pos == nil || pos.Quantity < 0 {
		return 0
	}
	return pos.Quantity
}

// reconcileTargetPosition 把 `TargetPosition` 与 tracker 的真实持仓对齐。
//
// 供**引擎外**改持仓的路径调用：止损 / 止盈平仓（本文件 `processStopLosses`）、
// 拆股（`ProcessSplit`）、退市强平（`forceCloseDelisted`）。它只负责那份
// **对外可见的账**（回测结果 / 日志），不参与下单决策 —— 决策侧已经实时读
// tracker，见 `computeEffectiveTarget`。
func (e *Engine) reconcileTargetPosition(
	state *BacktestState,
	symbol string,
	date time.Time,
	logger zerolog.Logger,
) {
	tp, ok := state.TargetPositions[symbol]
	if !ok {
		return
	}
	held := e.heldQty(state, symbol)
	if held == tp.ActualQty {
		return
	}
	logger.Info().
		Str("symbol", symbol).
		Float64("stale_actual_qty", tp.ActualQty).
		Float64("actual_qty", held).
		Float64("target_qty", tp.TargetQty).
		Time("date", date).
		Msg("Target position reconciled with tracker holdings")
	tp.ActualQty = held
	tp.PendingQty = tp.TargetQty - tp.ActualQty
	tp.LastUpdated = date
}

func (e *Engine) executeSignalTrade(
	ctx context.Context,
	state *BacktestState,
	algo *execalgo.Dispatcher,
	execAlgoDiag *execAlgoRunDiag,
	fillDiag *fillRunDiag,
	signal domain.Signal,
	effectiveTarget float64,
	// fillPrices 是按本次 FillTiming 选定的成交可用价表（QT-02）：same_close =
	// 当日收盘可成交价（既有形状），next_open = 成交日开盘价。查不到价 ⇒
	// 这笔委托被拒（price <= 0 的既有出口），不退回别的价成交。
	fillPrices map[string]float64,
	date time.Time,
	execOpts *OrderExecutionOpts,
	tp *domain.TargetPosition,
	logger zerolog.Logger,
) {
	price := fillPrices[signal.Symbol]
	if price <= 0 {
		return
	}

	// REV-09：流动性上限下沉到生产共用层。tracker.ExecuteTrade 只在
	// dayBar != nil 时才有「当日成交量 × 10%」上限，而生产路径
	// （ExecutionService → ApplyTrade）、ClosePosition 与止损平仓全部绕过它
	// ——想买 100 万股、当日只成交 10% 也全额成交。这里在**下单前**按
	// 当日成交量的 liquidityFactor 截断委托量：截断进 run 级诊断
	// （partial 语义），当日买不满的部分由次日的目标-持仓差自然续上。
	if execOpts != nil && execOpts.DayBar != nil && execOpts.DayBar.Volume > 0 {
		maxQty := execOpts.DayBar.Volume * e.liquidityFactor()
		if maxQty > 0 && effectiveTarget > maxQty {
			logger.Info().
				Str("symbol", signal.Symbol).
				Float64("requested_qty", effectiveTarget).
				Float64("day_volume", execOpts.DayBar.Volume).
				Float64("liquidity_factor", e.liquidityFactor()).
				Float64("filled_qty", maxQty).
				Time("date", date).
				Msg("Order truncated by daily-volume liquidity cap (partial)")
			fillDiag.recordTruncation(signal.Symbol, effectiveTarget, maxQty)
			effectiveTarget = maxQty
		}
	}

	// K17：指定了执行算法的信号走拆单路径——挂父单到派发器，按 bar 到点
	// 逐片撮合（processExecAlgoChildren），不当场整单成交。未指定
	// （ExecAlgo == ""）→ 下方原路径逐字节不变。
	if algo != nil {
		attached, err := attachExecAlgoSignal(ctx, algo, signal, effectiveTarget, state.ID, date)
		if err != nil {
			// K17-R1：挂载失败不再只是「记一条易被淹没的 Warn」——分类
			// 计入运行诊断（会话结束挂进 result.InvalidReasons，run 判
			// invalid），该信号被拒不下单。「单信号失败不中断整场」保留。
			kind := execalgo.ClassifyAttachError(err)
			execAlgoDiag.record(kind, execAlgoParentID(date, signal.Symbol, signal.Direction))
			logger.Warn().Err(err).
				Str("symbol", signal.Symbol).Str("exec_algo", signal.ExecAlgo).
				Str("failure_kind", string(kind)).
				Msg("Failed to attach exec-algo parent order — signal rejected and counted in run diagnostics")
			tp.PendingQty = tp.TargetQty - tp.ActualQty
			tp.LastUpdated = date
			return
		}
		if attached {
			return
		}
	}

	var trade *domain.Trade
	var err error

	// Check if ExecutionService is available for more realistic execution
	// P1-17 (ADR-020): read via ExecutionBridge (the live component)
	execSvc := e.executionBridge.Get()

	useExecutionService := execSvc != nil && signal.Direction != domain.DirectionHold

	switch signal.Direction {
	case domain.DirectionLong:
		if useExecutionService {
			trade, err = e.executeViaExecutionService(state, signal, effectiveTarget, price, date, execOpts, logger)
		} else {
			trade, err = state.Tracker.ExecuteTrade(
				signal.Symbol,
				domain.DirectionLong,
				effectiveTarget,
				price,
				date,
				execOpts,
			)
		}
		if err != nil {
			logger.Warn().
				Str("symbol", signal.Symbol).
				Err(err).
				Msg("Failed to execute long trade")
			tp.PendingQty = tp.TargetQty - tp.ActualQty
			tp.LastUpdated = date
			return
		}

	case domain.DirectionShort:
		if useExecutionService {
			trade, err = e.executeViaExecutionService(state, signal, effectiveTarget, price, date, execOpts, logger)
		} else {
			trade, err = state.Tracker.ExecuteTrade(
				signal.Symbol,
				domain.DirectionShort,
				effectiveTarget,
				price,
				date,
				execOpts,
			)
		}
		if err != nil {
			logger.Warn().
				Str("symbol", signal.Symbol).
				Err(err).
				Msg("Failed to execute short trade")
			tp.PendingQty = tp.TargetQty - tp.ActualQty
			tp.LastUpdated = date
			return
		}

	case domain.DirectionClose:
		if useExecutionService {
			trade, err = e.executeViaExecutionService(state, signal, effectiveTarget, price, date, execOpts, logger)
		} else {
			trade, err = state.Tracker.ClosePosition(signal.Symbol, price, date)
		}
		if err != nil {
			logger.Warn().
				Str("symbol", signal.Symbol).
				Err(err).
				Msg("Failed to close position")
			return
		}
		tp.PendingQty = 0
		tp.TargetQty = 0
		tp.ActualQty = 0
		tp.LastUpdated = date
		if trade != nil {
			trade.PendingQty = 0
		}
		delete(state.TargetPositions, signal.Symbol)
		return

	case domain.DirectionHold:
		return
	}

	if trade != nil {
		e.updateTargetPositionAfterTrade(state, trade, signal, tp, date, logger)
	}
}

// executeViaExecutionService executes a trade through the ExecutionService.
// This provides more realistic execution with configurable slippage and commission models.
func (e *Engine) executeViaExecutionService(
	state *BacktestState,
	signal domain.Signal,
	quantity float64,
	price float64,
	date time.Time,
	execOpts *OrderExecutionOpts,
	logger zerolog.Logger,
) (*domain.Trade, error) {
	// P1-17 (ADR-020): read via ExecutionBridge.
	execSvc := e.executionBridge.Get()

	if execSvc == nil {
		return nil, fmt.Errorf("execution service not available")
	}

	// Build order
	order := domain.Order{
		Symbol:    signal.Symbol,
		Direction: signal.Direction,
		Quantity:  quantity,
		Timestamp: date,
	}

	// Set order type and limit price
	if execOpts != nil {
		order.OrderType = execOpts.OrderType
		order.LimitPrice = execOpts.LimitPrice
	} else {
		order.OrderType = domain.OrderTypeMarket
	}

	// Build quote from available data
	quote := Quote{
		Symbol: signal.Symbol,
		Close:  price,
		Date:   date,
	}

	// If we have OHLCV data, use it for more accurate execution
	//
	// ⚠️ **不覆盖 quote.Close**（QT-02 修的正是这一条）。这行原来写的是
	// `quote.Close = execOpts.DayBar.Close`，于是引擎按 FillTiming 选定的成交价
	// 在这里被当日的**收盘价**静默替换掉：ExecuteOrder 的市单分支以
	// quote.Close 为基准价，next_open 传进来的开盘价根本到不了撮合 ——
	// 「实现了口径」而线上一个字都没变，是典型的假实现。
	// quote.Close 在这里的语义是「这笔成交的基准价」，不是「今天的收盘价」；
	// 当日区间（High/Low）与成交量照常进 quote，供滑点/冲击模型用。
	if execOpts != nil && execOpts.DayBar != nil {
		quote.Open = execOpts.DayBar.Open
		quote.High = execOpts.DayBar.High
		quote.Low = execOpts.DayBar.Low
		quote.Volume = execOpts.DayBar.Volume
	}

	// Execute through service
	trade, err := execSvc.ExecuteOrder(order, quote)
	if err != nil {
		return nil, err
	}

	// Apply trade to tracker (update cash and positions)
	trackerTrade, err := state.Tracker.ApplyTrade(trade)
	if err != nil {
		return nil, err
	}

	logger.Debug().
		Str("symbol", signal.Symbol).
		Str("direction", string(signal.Direction)).
		Float64("qty", trade.Quantity).
		Float64("price", trade.Price).
		Float64("commission", trade.Commission).
		Str("slippage_model", execSvc.GetSlippageModel()).
		Msg("Trade executed via ExecutionService")

	return trackerTrade, nil
}

func (e *Engine) updateTargetPositionAfterTrade(
	state *BacktestState,
	trade *domain.Trade,
	signal domain.Signal,
	tp *domain.TargetPosition,
	date time.Time,
	logger zerolog.Logger,
) {
	if signal.Direction == domain.DirectionLong || signal.Direction == domain.DirectionShort {
		tp.ActualQty += trade.Quantity
		tp.PendingQty = tp.TargetQty - tp.ActualQty
		trade.PendingQty = tp.PendingQty
		if tp.PendingQty > 0 {
			logger.Info().
				Str("symbol", signal.Symbol).
				Float64("target_qty", tp.TargetQty).
				Float64("actual_qty", tp.ActualQty).
				Float64("pending_qty", tp.PendingQty).
				Time("date", date).
				Msg("Partial fill: target vs actual gap recorded")
		}
	} else if signal.Direction == domain.DirectionClose {
		tp.PendingQty = 0
		tp.TargetQty = 0
		tp.ActualQty = 0
		trade.PendingQty = 0
	}
	tp.LastUpdated = date

	if tp.PendingQty <= 0 && tp.TargetQty <= 0 {
		delete(state.TargetPositions, signal.Symbol)
	}
}

func (e *Engine) processStopLosses(
	ctx context.Context,
	state *BacktestState,
	d *execalgo.Dispatcher,
	pricesCache map[string]float64,
	marketDataCache map[string][]domain.OHLCV,
	suspended map[string]bool,
	date time.Time,
	logger zerolog.Logger,
) {
	e.mu.RLock()
	rm := e.riskManager
	e.mu.RUnlock()

	if rm == nil {
		return
	}

	slChecker := rm.GetStopLossChecker()

	var precomputedATR map[string]float64
	if slChecker != nil && len(marketDataCache) > 0 {
		precomputedATR, _ = slChecker.ATRFromOHLCV(marketDataCache)
	}

	// REV-22-(8)：止损检查与撮合环都吃 run ctx——旧代码这里用裸
	// Background ctx，取消语义不一致：作业已取消仍可能撮入止损成交。
	// 检查与成交之间的窗口也在循环内逐事件检查（见下）。
	stopLossEvents, err := e.checkStopLossesWithATR(ctx, state.Tracker, pricesCache, precomputedATR)
	if err != nil {
		logger.Warn().Err(err).Msg("Failed to check stop losses")
	}

	for _, event := range stopLossEvents {
		// REV-22-(8)：取消后不再撮入新的止损/止盈成交（每次迭代都检查——
		// 一次 check 与逐笔成交之间可能跨越取消点）。
		if ctx.Err() != nil {
			logger.Info().Time("date", date).Err(ctx.Err()).
				Msg("Run cancelled — remaining stop-loss/take-profit events dropped")
			break
		}
		if event.Type != "stop_loss" && event.Type != "take_profit" {
			continue
		}
		// REV-09：停牌当日不触发止盈止损——event.Price 来自 pricesCache 的
		// 最后成交价（停牌前旧价），按它平仓同样是「旧价静默成交」。
		// 顺延到复牌日再判（复牌后价格回到当日 bar，判定自然重新成立）。
		if suspended[event.Symbol] {
			logger.Info().
				Str("symbol", event.Symbol).
				Str("type", event.Type).
				Time("date", date).
				Msg("Stop-loss/take-profit deferred: symbol suspended (stale price, no trading today)")
			continue
		}
		_, err := state.Tracker.ExecuteTrade(
			event.Symbol,
			domain.DirectionClose,
			event.Quantity,
			event.Price,
			date,
			nil,
		)
		if err != nil {
			logger.Warn().
				Str("symbol", event.Symbol).
				Str("type", event.Type).
				Err(err).
				Msg("Failed to execute stop loss")
			continue
		}
		// AUD-55 的耦合点：止损 / 止盈是**引擎外**平仓 —— 它直接落在 tracker 上，
		// 不经过 `executeSignalTrade` / `updateTargetPositionAfterTrade`。
		// 不在这里对齐，`TargetPosition` 那份账就会停在旧持仓上。
		e.reconcileTargetPosition(state, event.Symbol, date, logger)

		// K18：止损/止盈平仓是「引擎外改仓」——该 symbol 若有活跃 exec-algo
		// 父单，其剩余未报送子单必须中止，否则第 k+1.. 天的子单会把刚被风控
		// 平掉的仓位重新买回。取消只针对未报送片，已成交部分保留（不回滚），
		// 故与上面的 reconcile 无冲突（reconcile 读的是已落账的真实持仓）。
		reason := cancelReasonStopLoss
		if event.Type == "take_profit" {
			reason = cancelReasonTakeProfit
		}
		cancelExecAlgoParentsForSymbol(d, event.Symbol, reason, logger)
	}
}

// processCorporateActions 记录当日除权除息与拆股事件，返回受拆股影响的
// symbol 列表 —— 调用方（日循环）据此取消这些 symbol 的活跃 exec-algo
// 父单（K18）。
//
// REV-06（裁决 a，2026-10-16 主任）：引擎消费的日线是**前复权**序列
// （`ohlcv_daily_qfq`；qfq = raw × 当日因子 / 区间内最新因子，除权跳空已按
// 构造平滑）。在此口径下对持仓再做现金/股数改账就是**双重计算**：
//   - 分红：qfq 价格在除息日不跌（跌幅已摊进历史价），现金又贷一次 →
//     总价值虚高 qty×DivAmt；
//   - 拆股：qfq 价格不因拆股变化，股数却加倍 → 持仓市值当场翻倍。
//
// 因此本函数对持仓**不做任何现金/股数变更**，只把事件记入日志（除权事件
// 在报告面的唯一现存通道；报告字段化另立）。原始价口径的改账语义留在
// tracker.ProcessDividend / tracker.ProcessSplit（ExecuteTrade 内存路径
// 专用，非 qfq 生产口径，其单测已显式标记）。
//
// K18 语义原样保留：splitSymbols 返回值与 reconcileTargetPosition 调用不变
// （拆股 symbol 的活跃父单剩余片照常取消，由信号路径按新口径重排）。
func (e *Engine) processCorporateActions(
	state *BacktestState,
	ca *corporateActions,
	date time.Time,
	logger zerolog.Logger,
) []string {
	truncatedDate := date.Truncate(24 * time.Hour)
	if divs, ok := ca.dividendsByDate[truncatedDate]; ok {
		for _, d := range divs {
			// 只记录**有持仓**的 symbol：与旧口径 ProcessDividend 对无持仓
			// no-op 的有效范围一致，避免全市场分红事件把日志冲爆。
			if e.heldQty(state, d.Symbol) <= 0 {
				continue
			}
			logger.Info().
				Str("symbol", d.Symbol).
				Float64("div_amt", d.DivAmt).
				Time("pay_date", d.PayDate).
				Msg("Ex-dividend recorded; qfq series already reflects it, no cash credit (REV-06)")
		}
	}
	var splitSymbols []string
	if splits, ok := ca.splitsByDate[truncatedDate]; ok {
		for _, s := range splits {
			// 与旧口径一致：ProcessSplit 对无持仓本是 no-op 但返回 nil，
			// 旧代码因此把所有当日 split 都计入 splitSymbols —— K18 取消
			// 语义照旧，不过滤。
			logger.Info().
				Str("symbol", s.Symbol).
				Float64("stk_div_ratio", s.StkDivRatio).
				Float64("cash_div_ratio", s.CashDivRatio).
				Time("trade_date", s.TradeDate).
				Msg("Split recorded; qfq series already reflects it, no quantity change (REV-06)")
			e.reconcileTargetPosition(state, s.Symbol, truncatedDate, logger)
			splitSymbols = append(splitSymbols, s.Symbol)
		}
	}
	return splitSymbols
}

func (e *Engine) forceCloseAllPositions(
	state *BacktestState,
	pricesCache map[string]float64,
	lastTradingDay time.Time,
	logger zerolog.Logger,
) {
	// 强平顺序也要定序：先平哪只影响当天的现金与成交序列（P1-14）。
	openPositions := state.Tracker.GetAllPositions()
	for _, symbol := range sortedKeys(openPositions) {
		pos := openPositions[symbol]
		if abs(pos.Quantity) > 1e-8 {
			price, priceExists := pricesCache[symbol]
			if !priceExists || price <= 0 {
				if pos.CurrentPrice > 0 {
					price = pos.CurrentPrice
					logger.Info().
						Str("symbol", symbol).
						Float64("qty", pos.Quantity).
						Float64("fallback_price", price).
						Time("date", lastTradingDay).
						Msg("Using current price as fallback for force close")
				} else {
					logger.Warn().
						Str("symbol", symbol).
						Float64("qty", pos.Quantity).
						Time("date", lastTradingDay).
						Msg("Skipping force close: no price data for symbol at backtest end")
					continue
				}
			}
			closeTrade, err := state.Tracker.ClosePosition(symbol, price, lastTradingDay)
			if err != nil {
				logger.Warn().
					Str("symbol", symbol).
					Err(err).
					Time("date", lastTradingDay).
					Msg("Failed to force close position at backtest end")
			} else if closeTrade != nil {
				logger.Info().
					Str("symbol", symbol).
					Float64("qty", closeTrade.Quantity).
					Float64("price", closeTrade.Price).
					Time("date", lastTradingDay).
					Msg("Force closed position at backtest end")
			}
		}
	}
}

// forceCloseDelisted 平掉「已摘牌却还留在账上」的持仓（P2-4）。
//
// 为什么必须做：一旦把已退市的票从 universe 里剔掉，市场数据里就没有它了，
// 价格拿不到、止损触发不了，这笔持仓会一直挂到回测结束才被末尾的强平兜住
// —— 中间几十上百个交易日的资金占用和损益全被抹平，等于凭空造收益。
// 真实情况是：摘牌之后这笔钱要么按退市整理期的价格收回，要么血本无归，
// 总之不是继续持有。
//
// 价格优先级：当天价格 → 持仓里最后一次已知价 → 都取不到就跳过并告警。
// 最后一种情况宁可留着（并留下 warn）也不用 0 去平 —— 用 0 平仓等于
// 凭空抹掉一笔资产，账面上看不出来。
//
// 摘牌当天仍算在市（ListingWindow.IsListed 用 After 判断），所以真正触发
// 的是摘牌后的第一个交易日，那时通常已无行情，走 CurrentPrice 兜底。
//
// 返回**实际强平落账**的 symbol 列表 —— 调用方（日循环）据此取消这些
// symbol 的活跃 exec-algo 父单（K18：摘牌强平是「引擎外改仓」）。
func (e *Engine) forceCloseDelisted(
	state *BacktestState,
	pricesCache map[string]float64,
	date time.Time,
	logger zerolog.Logger,
) []string {
	listing := e.ListingWindows()
	if len(listing) == 0 {
		return nil // 没有上市日历，无从判断谁退市了
	}

	var closedSymbols []string
	positions := state.Tracker.GetAllPositions()
	// 定序：先平哪只影响当天的现金与成交序列（P1-14）。
	for _, symbol := range sortedKeys(positions) {
		pos := positions[symbol]
		if abs(pos.Quantity) <= 1e-8 {
			continue
		}
		w, known := listing[symbol]
		if !known || w.IsListed(date) {
			continue
		}

		price, priceExists := pricesCache[symbol]
		if !priceExists || price <= 0 {
			if pos.CurrentPrice > 0 {
				price = pos.CurrentPrice
			} else {
				logger.Warn().
					Str("symbol", symbol).
					Float64("qty", pos.Quantity).
					Time("date", date).
					Msg("Skipping delisted force close: no price available (position left open)")
				continue
			}
		}

		trade, err := state.Tracker.ClosePosition(symbol, price, date)
		if err != nil {
			logger.Warn().Str("symbol", symbol).Err(err).Time("date", date).
				Msg("Failed to force close delisted position")
			continue
		}
		if trade != nil {
			logger.Info().
				Str("symbol", symbol).
				Float64("qty", trade.Quantity).
				Float64("price", trade.Price).
				Time("date", date).
				Msg("Force closed delisted position")
			// 只有真落账（有成交）才计入「外部改仓」——见下方取消钩子。
			closedSymbols = append(closedSymbols, symbol)
		}
		// 摘牌强平同样是「引擎外改持仓」（AUD-55）：票已经从 universe 里消失，
		// 不会再有信号，但那份 `TargetPosition` 账得跟着清掉。
		e.reconcileTargetPosition(state, symbol, date, logger)
	}
	return closedSymbols
}

// abs returns the absolute value of x. Local copy — tracker/ and metrics/
// each have their own (S7-P2-1: tracker.go moved to tracker/ subpackage).
func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
