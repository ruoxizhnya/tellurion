package backtest

// 复权基准快照（REV-18）——「同区间重跑逐位一致」的数据层保证。
//
// 问题：`ohlcv_daily_qfq` 存的是**已前复权**的价格，前复权的基准是「同步时
// 区间内最新交易日的累计因子」（见 pkg/data/tushare.go 的 normalizeDailyOHLCV）。
// 于是同一段历史在今年同步和明年同步得到的 qfq 序列**整体差一个常数倍**：
// 没有任何代码变更，回测收益就变了。系统对外卖的是确定性（seed / 定序 /
// engine_reproducibility_test），而那条测试跑的是内存 fixture，天然测不到
// 这种「真实数据路径上的基准漂移」。
//
// 解法（不动同步链路、不动存量数据）：
//   - **起跑时**（warmCache 之后，L1 已有全区间数据）对每个 symbol 一次性
//     处理：库里已有冻结基准 → 按锚点日收盘算 ratio 并**校验全序列 digest**；
//     没有 → 以区间首根 bar 冻结新基准（digest 一并冻结）；
//   - 日环内（processStockJob）只做定比缩放：ratio 是常数，对任意窗口逐 bar
//     乘 + 量化，结果与全区间归一化的对应前缀逐位相同；
//   - 价格量化到 1e-6（远小于 A 股最小报价单位 0.01），使「缩放回基准」在
//     float64 上可逆 —— 两次运行的输入逐位相同，输出自然逐位相同；
//   - **为什么 digest 必须在起跑时一次校验、而不是日环内逐日核对**：引擎的
//     取数窗口是「区间起点 → 当日」的**增长窗口**，冻结的是全序列；逐日
//     核对会让第二天的 digest 必然对不上（窗口比冻结时多一根）→ 把正常
//     数据全丢掉（实测 0 成交）。全区间只在 warmCache 里出现过一次，那里
//     才是校验点。
//   - 校验失败（锚点日缺失 / digest 不一致 = 改价或补行）→ **fail loud**：
//     宁可让运维知道数据变了，也不给一条看起来一样的曲线。

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"tellurion/pkg/domain"
	"tellurion/pkg/storage"
)

// priceQuantum 缩放后价格的量化步长。1e-6 远小于 A 股 0.01 的最小报价单位，
// 只用于抹掉 float64 缩放的尾数，不改变任何决策相关精度。
const priceQuantum = 1e-6

var (
	// ErrAdjustmentAnchorMissing：库里冻结了该 symbol 的基准，但本次取到的
	// 区间数据里没有基准日那根 bar（数据被回填/删行）。不能猜，报错。
	ErrAdjustmentAnchorMissing = errors.New("adjustment snapshot: anchor bar missing from the loaded range")

	// ErrAdjustmentDrift：缩放后序列摘要与冻结值不一致 —— 换基准能精确还原，
	// 还原不出来说明数据本身被改过（改价/补行）。不静默继续。
	ErrAdjustmentDrift = errors.New("adjustment snapshot: series drifted beyond a pure rebasing")
)

// AdjustmentAnchor 是一个 symbol 在某段回测区间起点的复权基准。
type AdjustmentAnchor struct {
	Symbol       string    `json:"symbol"`
	AnchorDate   time.Time `json:"anchor_date"`
	AnchorClose  float64   `json:"anchor_close"`
	SeriesDigest string    `json:"series_digest"`
}

// AdjustmentAnchorStore 是 anchor 的持久化端口。生产实现是
// *storage.PostgresStore（表 backtest_adjustment_anchors）；测试用内存实现。
//
// 两件事必须由实现保证：
//   - Save 是**首写者胜**（ON CONFLICT DO NOTHING）：基准一旦冻结就不可被后来
//     的运行改写，否则「冻结」形同虚设；
//   - Load 只按 (start_date, symbols) 查，不猜默认值。
type AdjustmentAnchorStore interface {
	LoadAdjustmentAnchors(ctx context.Context, start time.Time, symbols []string) (map[string]AdjustmentAnchor, error)
	SaveAdjustmentAnchors(ctx context.Context, start time.Time, anchors []AdjustmentAnchor) error
}

// adjustmentRun 是一次回测运行内的基准上下文。**per-run，不挂在 Engine 上**：
// Engine 会被并发回测共享（REV-11 的教训），任何 run 级状态都必须随 state 走。
type adjustmentRun struct {
	store AdjustmentAnchorStore
	start time.Time

	mu sync.Mutex
	// enabled=false 表示无 store（无库环境 / 既有测试路径）：所有方法都退化成
	// 直通，行为与 REV-18 之前逐位一致。
	enabled bool
	// ratios：symbol → 缩放比。prepare 阶段一次算定，日环内只读。
	ratios map[string]float64
	// captured：本次新采集、flush 时落库的基准。
	captured []AdjustmentAnchor
	rescaled int
	skipped  int // 暖机没拿到全区间数据的 symbol 数（不钉，如实计数）
}

// prepareAdjustmentRun 在 warmCache 之后调用：对每个 symbol 一次性完成
// 「读回冻结基准 → 校验 digest → 算 ratio」或「采集新基准」。
//
// fullBars 取自 L1 cache 的**全区间**序列（cache.Peek）。暖机失败的 symbol
// 没有全区间数据 → 不钉不校验（skipped 计数 + 日志），该 symbol 保持
// 「当前刻度直通」——如实降级，不假装钉住了。
func prepareAdjustmentRun(
	ctx context.Context,
	store AdjustmentAnchorStore,
	start time.Time,
	symbols []string,
	fullBars func(symbol string) []domain.OHLCV,
	logger zerolog.Logger,
) (*adjustmentRun, error) {
	if store == nil {
		return &adjustmentRun{enabled: false}, nil
	}

	persisted, err := store.LoadAdjustmentAnchors(ctx, start, symbols)
	if err != nil {
		return nil, fmt.Errorf("load adjustment anchors for %s: %w", start.Format("2006-01-02"), err)
	}

	adj := &adjustmentRun{
		store:   store,
		start:   start,
		enabled: true,
		ratios:  make(map[string]float64, len(symbols)),
	}

	for _, sym := range symbols {
		bars := fullBars(sym)
		if len(bars) == 0 {
			adj.skipped++
			continue
		}
		if anchor, ok := persisted[sym]; ok {
			ratio, err := verifyAgainstFrozen(sym, bars, anchor)
			if err != nil {
				return nil, err
			}
			adj.ratios[sym] = ratio
			if ratio != 1 {
				adj.rescaled++
			}
			continue
		}
		// 新基准：区间首根 bar + 归一化（=量化）后的全序列 digest。
		//
		// AnchorClose 刻意**不量化**（存原始 float64）：ratio =
		// anchorClose/当前锚点收盘，锚点若先量化，ratio 就带上
		// ~半量化格/锚点价的相对误差（锚点 10 元时 ≈5e-8），对 40 元的价
		// 是 2e-6 的绝对差 —— 超过 1e-6 量化半格，约一半 bar 会落到错误的
		// 网格点上，digest 校验必炸（实测）。存原始值，ratio 才是干净的。
		a := AdjustmentAnchor{
			Symbol:       sym,
			AnchorDate:   bars[0].Date.Truncate(24 * time.Hour),
			AnchorClose:  bars[0].Close,
			SeriesDigest: seriesDigest(quantizeBars(bars)),
		}
		if a.AnchorClose <= 0 {
			return nil, fmt.Errorf("symbol %s: first bar close is not positive (%.6f)", sym, bars[0].Close)
		}
		adj.captured = append(adj.captured, a)
		adj.ratios[sym] = 1
	}

	logger.Info().
		Str("base_start", start.Format("2006-01-02")).
		Int("symbols", len(symbols)).
		Int("frozen_anchors_loaded", len(persisted)).
		Int("newly_captured", len(adj.captured)).
		Int("rebased_symbols", adj.rescaled).
		Int("skipped_no_full_series", adj.skipped).
		Msg("Adjustment snapshot prepared: qfq base pinned to range start (REV-18)")

	return adj, nil
}

// verifyAgainstFrozen 校验「当前全区间序列」能否由冻结基准精确还原：
// 锚点日必须有 bar（算得出 ratio），且按 ratio 缩放后的 digest 与冻结值一致。
// 返回算得的 ratio。
func verifyAgainstFrozen(symbol string, bars []domain.OHLCV, anchor AdjustmentAnchor) (float64, error) {
	cur, ok := closeOnDate(bars, anchor.AnchorDate)
	if !ok {
		return 0, fmt.Errorf("%w (symbol %s, anchor date %s)",
			ErrAdjustmentAnchorMissing, symbol, anchor.AnchorDate.Format("2006-01-02"))
	}
	if cur <= 0 {
		return 0, fmt.Errorf("symbol %s: anchor-date close is not positive (%.6f)", symbol, cur)
	}
	ratio := anchor.AnchorClose / cur

	// 换基准 = 整条曲线乘常数：缩放回冻结刻度后 digest 必须逐位一致；
	// 改价/补行还原不出来。digest 吃量化网格，浮点尾数不掺和。
	if digest := seriesDigest(rebaseBars(bars, ratio)); digest != anchor.SeriesDigest {
		return 0, fmt.Errorf("%w (symbol %s): data was corrected/added, not merely rebased",
			ErrAdjustmentDrift, symbol)
	}
	return ratio, nil
}

// normalize 把一次取数窗口对齐到冻结刻度。ratio 在 prepare 阶段已算定，
// 这里只做「乘 + 量化」——对增长窗口逐 bar 操作，结果与全区间归一化的
// 对应前缀逐位相同。无 ratio（未钉住的 symbol）直通。
func (r *adjustmentRun) normalize(symbol string, bars []domain.OHLCV) ([]domain.OHLCV, error) {
	if r == nil || !r.enabled || len(bars) == 0 {
		return bars, nil
	}

	r.mu.Lock()
	ratio, ok := r.ratios[symbol]
	r.mu.Unlock()
	if !ok {
		// prepare 时没见到全区间数据（暖机失败）：直通 + 量化（与钉住路径
		// 的网格一致，混合场景下口径不分裂）。
		return quantizeBars(bars), nil
	}
	if ratio == 1 {
		return quantizeBars(bars), nil
	}
	return rebaseBars(bars, ratio), nil
}

// flush 把本次新采集的基准落库（首写者胜）。运行失败时也应调用：基准是数据
// 的客观属性，与这次策略跑得好不好无关。
func (r *adjustmentRun) flush(ctx context.Context, logger zerolog.Logger) {
	if r == nil || !r.enabled {
		return
	}

	r.mu.Lock()
	captured := append([]AdjustmentAnchor(nil), r.captured...)
	r.mu.Unlock()

	if len(captured) > 0 {
		if err := r.store.SaveAdjustmentAnchors(ctx, r.start, captured); err != nil {
			logger.Warn().Err(err).Int("anchors", len(captured)).
				Msg("Failed to persist adjustment anchors — reruns may rebuild them (reproducibility not guaranteed for this range)")
		} else {
			logger.Info().Int("anchors", len(captured)).Str("base_start", r.start.Format("2006-01-02")).
				Msg("Adjustment anchors frozen for this range")
		}
	}
}

// rebaseBars 按 ratio 缩放价格四件套并量化。Volume/Turnover/TradeDays 是
// 「股 / 金额 / 天数」，不随复权基准变化。
func rebaseBars(bars []domain.OHLCV, ratio float64) []domain.OHLCV {
	out := make([]domain.OHLCV, len(bars))
	copy(out, bars)
	for i := range out {
		out[i].Open = quantizePrice(out[i].Open * ratio)
		out[i].High = quantizePrice(out[i].High * ratio)
		out[i].Low = quantizePrice(out[i].Low * ratio)
		out[i].Close = quantizePrice(out[i].Close * ratio)
	}
	return out
}

// quantizePrice 把价格对齐到 priceQuantum 网格。
func quantizePrice(v float64) float64 {
	if v == 0 {
		return 0
	}
	return math.Round(v/priceQuantum) * priceQuantum
}

func quantizeBars(bars []domain.OHLCV) []domain.OHLCV {
	out := make([]domain.OHLCV, len(bars))
	copy(out, bars)
	for i := range out {
		out[i].Open = quantizePrice(out[i].Open)
		out[i].High = quantizePrice(out[i].High)
		out[i].Low = quantizePrice(out[i].Low)
		out[i].Close = quantizePrice(out[i].Close)
	}
	return out
}

// closeOnDate 找某个交易日（按日截断）的收盘价。
func closeOnDate(bars []domain.OHLCV, date time.Time) (float64, bool) {
	want := date.Truncate(24 * time.Hour)
	for _, b := range bars {
		if b.Date.Truncate(24 * time.Hour).Equal(want) {
			return b.Close, true
		}
	}
	return 0, false
}

// seriesDigest 对归一化后的序列算稳定摘要：FNV-1a over
// (date_unix, open, high, low, close)，价格取量化网格值。日期先排序（防御
// 上游乱序），使 digest 与输入顺序无关。
func seriesDigest(bars []domain.OHLCV) string {
	sorted := make([]domain.OHLCV, len(bars))
	copy(sorted, bars)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Date.Before(sorted[j].Date) })

	h := fnv.New64a()
	for _, b := range sorted {
		fmt.Fprintf(h, "%d|%.6f|%.6f|%.6f|%.6f\n",
			b.Date.Truncate(24*time.Hour).Unix(),
			quantizePrice(b.Open), quantizePrice(b.High),
			quantizePrice(b.Low), quantizePrice(b.Close))
	}
	return strconv.FormatUint(h.Sum64(), 16)
}

// ───────────────────────── Engine 装配（REV-18） ─────────────────────────

// newAdjustmentRunForRun 在 warmCache 之后调用：从 L1 取全区间序列完成
// 基准准备。e.store 未注入（无库 / 既有测试路径）时返回 disabled 上下文，
// 行为与 REV-18 之前逐位一致。
func (e *Engine) newAdjustmentRunForRun(ctx context.Context, params domain.BacktestParams) (*adjustmentRun, error) {
	store := e.adjustmentStore()
	if store == nil {
		return &adjustmentRun{enabled: false}, nil
	}

	// 排序让 ANY($2) 的入参与遍历顺序确定（可复现纪律）。
	symbols := append([]string(nil), params.StockPool...)
	sort.Strings(symbols)

	return prepareAdjustmentRun(ctx, store, params.StartDate, symbols,
		func(symbol string) []domain.OHLCV { return e.cache.Peek(symbol) }, e.logger)
}

// adjustmentStore 把具体指针 e.store 适配成快照端口。
// Typed-nil guard（同 warmFactorCache）：e.store 是 *storage.PostgresStore
// 具体指针，直接塞进接口参数会造出 non-nil interface（Go 的 typed-nil 陷阱）。
func (e *Engine) adjustmentStore() AdjustmentAnchorStore {
	e.mu.RLock()
	custom, store := e.adjStore, e.store
	e.mu.RUnlock()

	if custom != nil {
		return custom
	}
	if store == nil {
		return nil
	}
	return storageAnchorStore{store: store}
}

// storageAnchorStore 把 *storage.PostgresStore 适配成 AdjustmentAnchorStore，
// 并做 backtest↔storage 两侧 anchor 结构的转换（storage 不反向依赖 backtest）。
type storageAnchorStore struct {
	store *storage.PostgresStore
}

func (s storageAnchorStore) LoadAdjustmentAnchors(
	ctx context.Context,
	start time.Time,
	symbols []string,
) (map[string]AdjustmentAnchor, error) {
	got, err := s.store.LoadAdjustmentAnchors(ctx, start, symbols)
	if err != nil {
		return nil, err
	}
	out := make(map[string]AdjustmentAnchor, len(got))
	for sym, a := range got {
		out[sym] = AdjustmentAnchor{
			Symbol: a.Symbol, AnchorDate: a.AnchorDate,
			AnchorClose: a.AnchorClose, SeriesDigest: a.SeriesDigest,
		}
	}
	return out, nil
}

func (s storageAnchorStore) SaveAdjustmentAnchors(ctx context.Context, start time.Time, anchors []AdjustmentAnchor) error {
	if len(anchors) == 0 {
		return nil
	}
	converted := make([]storage.AdjustmentAnchor, len(anchors))
	for i, a := range anchors {
		converted[i] = storage.AdjustmentAnchor{
			Symbol: a.Symbol, AnchorDate: a.AnchorDate,
			AnchorClose: a.AnchorClose, SeriesDigest: a.SeriesDigest,
		}
	}
	return s.store.SaveAdjustmentAnchors(ctx, start, converted)
}
