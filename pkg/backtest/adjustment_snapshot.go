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
//   - **起跑时**（warmCache 之后，本 run 的 L1 已有区间数据）对每个 symbol
//     一次性处理：库里已有冻结基准 → 按锚点日收盘算 ratio 并**校验区间序列
//     digest**；没有 → 以区间首根 bar 冻结新基准（digest 一并冻结）；
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
//
// QT-13（A11）——「digest 覆盖的是缓存，不是区间」：
//
// L1 里某个 symbol 的序列**可能比本 run 的区间长**：装配期种子
// （`LoadOHLCVInMemory`，今天只有测试与 bench 在调，生产零调用点）塞的是
// 调用方手里的全量数据；QT-12 之前还叠加过「跨 run 单调合并」—— 那条向量随
// 缓存变成 run 私有已经关掉，但**那时冻进库的行是这么写下的**。锚点若直接吃
// 整条缓存序列，冻结出来的 AnchorDate 会早于区间起点、digest 会覆盖本 run
// 根本不会回放的日子 —— 而主键是 `(symbol, start_date)`，不含 end。于是
// 下一笔同 start 的 run 拿自己区间内的序列去核对，必然对不上；首写者胜又让
// 这个错误**永久生效**，报错还长得像「数据被改过」。修法是两件事一起：
//  1. 序列先按 `[params.StartDate, params.EndDate]` 切片，锚点与 digest 都
//     只吃这份切片 ⇒ 输入集合由主键的 start 决定，不再由缓存决定；
//  2. 切片的上界由 run 决定、主键却不记 end ⇒ 把「这份 digest 覆盖到哪天」
//     写进摘要串本身（`<hash>@<YYYY-MM-DD>`）。同 start 不同 end 的重跑按
//     记录自己声明的范围核对；本 run 区间更短时核对不了（哈希不可分解），
//     就只钉刻度、如实计数，而不是把一次正常的重跑判成数据损坏。
//
// 两条里**第 2 条是今天生产上活的**：窗口与用户重跑都会出现「同 start 不同
// end」（`walkforward.go:455-462` 的算术：`step | TrainDays` 时，第 i 个窗口
// 的 test 段起点正好是第 i+TrainDays/step 个窗口的 train 段起点，而两段终点
// 不同），而 walk-forward 确实在用锚点 —— 端口从 `e.store` 自动派生，工厂
// 给了 store。第 1 条今天主要是把「越界输入」钉成运行期当场报错的形状
// （`prepareAdjustmentRun` 的那道不变量）。

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
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

	// ErrAdjustmentDigestShape：已冻结行的 series_digest 后缀读不懂。
	// 不能把「我解析不了」当成「旧格式（无后缀）」放过去：那会让这一行
	// 按整段区间的旧口径去核对，报错指向一个不是原因的原因。
	ErrAdjustmentDigestShape = errors.New("adjustment snapshot: frozen series_digest has an unreadable range suffix")
)

// 摘要串的自描述格式（QT-13 ③）：`<hash>` = 覆盖范围未知（历史行，沿用旧
// 口径核对整个取数区间）；`<hash>@<YYYY-MM-DD>` = 这份 hash 覆盖到该日（含）。
//
// 为什么把范围写进这一列而不是新加一列：QT-13 不是带 DDL 的单（W1 的 DDL
// 单是 QT-16），而 `series_digest` 本来就是自由文本（TEXT NOT NULL，默认空串）；
// 摘要本体是 16 位十六进制（FNV-1a）不含 `@`，所以这个形状可以无损解析。
// 存量行不清理、不重写（任务单 ④），解析不出来就当场失败。
const (
	digestRangeSep    = "@"
	digestRangeLayout = "2006-01-02"
)

// encodeSeriesDigest 把「覆盖到哪天」挂到摘要后面。coveredUntil 为零值时
// 退回无后缀形状（等价于「范围未知」，与历史行同一口径）。
func encodeSeriesDigest(hash string, coveredUntil time.Time) string {
	if coveredUntil.IsZero() {
		return hash
	}
	return hash + digestRangeSep + coveredUntil.UTC().Format(digestRangeLayout)
}

// decodeSeriesDigest 拆出摘要本体与其覆盖上界（含）。coveredUntil 为零值 =
// 范围未知。后缀存在却解析不了 ⇒ ok=false，调用方必须报错而不是猜。
func decodeSeriesDigest(s string) (hash string, coveredUntil time.Time, ok bool) {
	i := strings.LastIndex(s, digestRangeSep)
	if i < 0 {
		return s, time.Time{}, true
	}
	t, err := time.Parse(digestRangeLayout, s[i+1:])
	if err != nil {
		return "", time.Time{}, false
	}
	return s[:i], t.UTC(), true
}

// clipToRunRange 把序列裁到 [start, end]（含），只喂给锚点做锚定日与 digest。
//
// 缓存里的 bar 顺序不保证（装配期种子是调用方直接塞进来的，`Load` 不排序），
// 所以先在**副本**上按日期排序再二分 —— 不写回缓存的底层数组，那会让别的
// run 看到被就地改过的序列。
func clipToRunRange(bars []domain.OHLCV, start, end time.Time) []domain.OHLCV {
	if len(bars) == 0 {
		return nil
	}
	sorted := make([]domain.OHLCV, len(bars))
	copy(sorted, bars)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Date.Before(sorted[j].Date) })

	lo, hi := dateRangeBounds(sorted, start, end)
	if lo > hi || hi >= len(sorted) {
		return nil
	}
	return sorted[lo : hi+1]
}

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
	// digests：本次 run **实际使用**的锚点序列摘要（QT-63 进 run_spec_hash）。
	// persisted 与 newly captured 都收 —— 两者都决定了本 run 的价格刻度。
	digests  []string
	rescaled int
	skipped  int // 暖机没拿到全区间数据的 symbol 数（不钉，如实计数）
	// unverified：本 run 的区间比冻结记录声明的覆盖范围**短**，摘要无法
	// 分段核对（哈希不可分解）。刻度照钉（ratio 只依赖锚点日），检查照跳过，
	// 数量如实报出来 —— 别让「跑通了」被读成「核对过了」。
	unverified int
}

// anchorDigests 返回本次 run 实际使用的锚点摘要（无 store 时为空）。
func (a *adjustmentRun) anchorDigests() []string {
	if a == nil {
		return nil
	}
	return a.digests
}

// prepareAdjustmentRun 在 warmCache 之后调用：对每个 symbol 一次性完成
// 「读回冻结基准 → 校验 digest → 算 ratio」或「采集新基准」。
//
// fullBars 给的是**本 run 区间内**的序列（调用方已按 [StartDate, EndDate]
// 切片，见 clipToRunRange）。暖机失败的 symbol 没有数据 → 不钉不校验
// （skipped 计数 + 日志），该 symbol 保持「当前刻度直通」——如实降级，
// 不假装钉住了。
//
// start 同时是主键里的 `start_date` 与切片的下界。序列首根早于 start 说明
// 调用方没裁（QT-13 的原始缺陷形状：锚点吃到了区间之外的缓存），当场报错
// 而不是冻出一个后来必然校验失败的行 —— 这一行代码是那条纪律的执法点。
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
		if first := bars[0].Date.Truncate(24 * time.Hour); first.Before(start.Truncate(24 * time.Hour)) {
			return nil, fmt.Errorf(
				"adjustment snapshot: symbol %s 的序列起点 %s 早于 run 区间起点 %s —— 锚点的输入集合必须由 (symbol, start_date) 决定，喂进来之前先按区间切片（QT-13）",
				sym, first.Format("2006-01-02"), start.Format("2006-01-02"))
		}
		if anchor, ok := persisted[sym]; ok {
			ratio, verified, err := verifyAgainstFrozen(sym, start, bars, anchor)
			if err != nil {
				return nil, err
			}
			adj.ratios[sym] = ratio
			adj.digests = append(adj.digests, anchor.SeriesDigest)
			if !verified {
				adj.unverified++
			}
			if ratio != 1 {
				adj.rescaled++
			}
			continue
		}
		// 新基准：区间首根 bar + 归一化（=量化）后的**本区间**序列 digest，
		// 并记下这份摘要覆盖到哪天（主键不含 end，覆盖范围得自描述）。
		//
		// AnchorClose 刻意**不量化**（存原始 float64）：ratio =
		// anchorClose/当前锚点收盘，锚点若先量化，ratio 就带上
		// ~半量化格/锚点价的相对误差（锚点 10 元时 ≈5e-8），对 40 元的价
		// 是 2e-6 的绝对差 —— 超过 1e-6 量化半格，约一半 bar 会落到错误的
		// 网格点上，digest 校验必炸（实测）。存原始值，ratio 才是干净的。
		a := AdjustmentAnchor{
			Symbol:      sym,
			AnchorDate:  bars[0].Date.Truncate(24 * time.Hour),
			AnchorClose: bars[0].Close,
			SeriesDigest: encodeSeriesDigest(
				seriesDigest(quantizeBars(bars)),
				bars[len(bars)-1].Date.Truncate(24*time.Hour)),
		}
		if a.AnchorClose <= 0 {
			return nil, fmt.Errorf("symbol %s: first bar close is not positive (%.6f)", sym, bars[0].Close)
		}
		adj.captured = append(adj.captured, a)
		adj.digests = append(adj.digests, a.SeriesDigest)
		adj.ratios[sym] = 1
	}

	logger.Info().
		Str("base_start", start.Format("2006-01-02")).
		Int("symbols", len(symbols)).
		Int("frozen_anchors_loaded", len(persisted)).
		Int("newly_captured", len(adj.captured)).
		Int("rebased_symbols", adj.rescaled).
		Int("skipped_no_full_series", adj.skipped).
		Int("digest_unverified_narrower_range", adj.unverified).
		Msg("Adjustment snapshot prepared: qfq base pinned to range start (REV-18)")

	return adj, nil
}

// verifyAgainstFrozen 校验「本 run 区间内的序列」能否由冻结基准精确还原。
// 返回 (ratio, 是否真核对过, error)。
//
// 核对的序列取**冻结记录自己声明的覆盖范围**与**本 run 区间**的交集：主键是
// (symbol, start_date)，end 由每笔 run 自己决定，所以「同 start 不同 end」
// 共用同一行；拿区间长度不同的两份序列直接比摘要，会把一次正常的重跑误报成
// 数据损坏（QT-13 ③）。
//   - 本 run 更长（或等长）⇒ 交集就是记录声明的那段，照常核对，改价/补行仍红；
//   - 本 run 更短 ⇒ 哈希不可分解，核对不了：ratio 只依赖锚点日，刻度照钉，
//     检查如实计数（unverified）。
//   - 历史行没有范围后缀 ⇒ 沿用旧口径（按本 run 整个区间核对），行为不变；
//     这类行若当年是按更长区间冻的，重跑仍会红 —— 那是存量账，不在本单清理。
func verifyAgainstFrozen(
	symbol string,
	runStart time.Time,
	bars []domain.OHLCV,
	anchor AdjustmentAnchor,
) (float64, bool, error) {
	if a := anchor.AnchorDate.Truncate(24 * time.Hour); a.Before(runStart.Truncate(24 * time.Hour)) {
		return 0, false, fmt.Errorf("%w (symbol %s): 冻结的锚点日 %s 早于本 run 区间起点 %s —— 这一行多半是 QT-13 之前用整条合并缓存冻的（锚点吃到了区间之外的 bar），删掉该行重跑即可按本区间重新冻结",
			ErrAdjustmentAnchorMissing, symbol, a.Format("2006-01-02"), runStart.Format("2006-01-02"))
	}
	cur, ok := closeOnDate(bars, anchor.AnchorDate)
	if !ok {
		return 0, false, fmt.Errorf("%w (symbol %s, anchor date %s)",
			ErrAdjustmentAnchorMissing, symbol, anchor.AnchorDate.Format("2006-01-02"))
	}
	if cur <= 0 {
		return 0, false, fmt.Errorf("symbol %s: anchor-date close is not positive (%.6f)", symbol, cur)
	}
	ratio := anchor.AnchorClose / cur

	hash, coveredUntil, shapeOK := decodeSeriesDigest(anchor.SeriesDigest)
	if !shapeOK {
		return 0, false, fmt.Errorf("%w (symbol %s): %q",
			ErrAdjustmentDigestShape, symbol, anchor.SeriesDigest)
	}

	checkSet := bars
	rangeLabel := "整个取数区间（该行没记覆盖范围）"
	if !coveredUntil.IsZero() {
		rangeLabel = "截至 " + coveredUntil.Format("2006-01-02")
		last := bars[len(bars)-1].Date.Truncate(24 * time.Hour)
		if coveredUntil.After(last) {
			return ratio, false, nil // 本 run 区间更短：核对不了，刻度照钉
		}
		_, hi := dateRangeBounds(bars, time.Time{}, coveredUntil)
		if hi < 0 {
			return ratio, false, nil // 一条都取不到 = 同样无法核对（锚点日已在上文校过）
		}
		checkSet = bars[:hi+1]
	}

	// 换基准 = 整条曲线乘常数：缩放回冻结刻度后 digest 必须逐位一致；
	// 改价/补行还原不出来。digest 吃量化网格，浮点尾数不掺和。
	if digest := seriesDigest(rebaseBars(checkSet, ratio)); digest != hash {
		return 0, false, fmt.Errorf("%w (symbol %s): 核对范围【%s】内的数据被修正/补行过，不是单纯换了复权基准",
			ErrAdjustmentDrift, symbol, rangeLabel)
	}
	return ratio, true, nil
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

// newAdjustmentRunForRun 在 warmCache 之后调用：从本 run 的 L1 取序列完成
// 基准准备。e.store 未注入（无库 / 既有测试路径）时返回 disabled 上下文，
// 行为与 REV-18 之前逐位一致。
//
// 取出来的序列**必须先按 [StartDate, EndDate] 切片**（QT-13 ①）：L1 里可能有
// 装配期种子塞进来的、本 run 根本不会回放的 bar，而锚点的身份是
// (symbol, start_date) —— 输入集合由缓存决定而不是由主键决定，就会冻出一行
// 后来谁都校验不过的基准（见文件头的 A11 说明）。
func (e *Engine) newAdjustmentRunForRun(ctx context.Context, rc *runContext, params domain.BacktestParams) (*adjustmentRun, error) {
	store := e.adjustmentStore()
	if store == nil {
		return &adjustmentRun{enabled: false}, nil
	}

	// 排序让 ANY($2) 的入参与遍历顺序确定（可复现纪律）。
	symbols := append([]string(nil), params.StockPool...)
	sort.Strings(symbols)

	return prepareAdjustmentRun(ctx, store, params.StartDate, symbols,
		func(symbol string) []domain.OHLCV {
			return clipToRunRange(rc.peekOHLCV(symbol), params.StartDate, params.EndDate)
		}, e.logger)
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
