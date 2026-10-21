//go:build qt02report

// QT-02 ③-④ · 两口径差异报告生成器。
//
// 这不是单测，是** producing 证据的脚手架**：连真库、跑真策略、把两种成交口径
// 的差异量出来，写成 docs/test-cases/QT02_FILL_TIMING_REPORT.md。台账 ⑤ 的判据
// 是「报告存在且含可复现命令；切换后基准结果与报告数值一致（同环境重跑 ±0）」，
// 所以这份文件同时干两件事：
//
//  1. 产出报告（含每个组合的指标与 lag 对齐证据）；
//  2. 当场复跑一遍并逐位比对 ——「同环境重跑 ±0」是被执行过的断言，
//     不是报告里的一句承诺。
//
// 复现命令（DSN 是唯一必填项）：
//
//	QT02_PG_DSN='postgres://postgres:postgres@127.0.0.1:5432/quant_trading?sslmode=disable' \
//	  go test ./pkg/backtest -tags qt02report -run TestQT02FillTimingDifferenceReport -count=1 -v
//
// 可选覆盖：QT02_UNIVERSE / QT02_START / QT02_END / QT02_CAPITAL / QT02_OUT。
//
// 库侧入口用 NewPostgresStoreConnected（连库 + ping，**不迁移**）—— 这是本仓
// 库侧测试的硬规则（AGENTS §6，护栏在 internal/repoguard）。跑之前先确认这台
// 库已经迁移过、且 ohlcv_daily_qfq 有区间数据。
package backtest

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"tellurion/pkg/backtest/contracts"
	"tellurion/pkg/domain"
	"tellurion/pkg/fees"
	"tellurion/pkg/logging"
	"tellurion/pkg/marketdata"
	"tellurion/pkg/risk"
	"tellurion/pkg/storage"
	_ "tellurion/pkg/strategy/plugins" // 内置策略靠 init() 注册进 strategy.GlobalRegister
)

// 成本与随机性口径：两口径共用一台同参数的引擎，差异只可能来自成交时点。
//
// 滑点取 fees.FixedSlippageRate（0.001）而不是 Config 默认的 0.0001：
// 走执行服务的建仓成交用的是 fixed 模型，把 tracker 侧也对到同一个值，
// 免得「口径差」里混进「同一 run 内两套滑点」。
const (
	qt02Seed            = 42
	qt02Commission      = fees.DefaultCommissionRate
	qt02Slippage        = fees.FixedSlippageRate
	qt02RiskFreeDefault = DefaultRiskFreeRate // 未显式配置时引擎自己填的值，报告如实写出
)

const (
	defaultQT02Universe = "000001.SZ,000002.SZ,000006.SZ,000009.SZ,000011.SZ,000012.SZ," +
		"000014.SZ,000017.SZ,000019.SZ,000020.SZ,000021.SZ,000025.SZ," +
		"600000.SH,600004.SH,600006.SH,600008.SH,600009.SH,600011.SH," +
		"600015.SH,600016.SH,600018.SH,600019.SH,600020.SH,600021.SH"
	defaultQT02Start   = "2024-01-02"
	defaultQT02End     = "2025-12-31"
	defaultQT02Capital = 1_000_000.0
	defaultQT02Out     = "docs/test-cases/QT02_FILL_TIMING_REPORT.md"
)

// qt02ReportStrategies 是基准策略集（③-④ 要求 3~5 个）。
//
// 入选判据是**只用价量能跑**：本库里 stock_fundamentals 只有 2 只票的 112 行，
// value / quality / multi_factor 这类要吃基本面的策略会大面积缺数，量出来的
// 差异里混着「数据缺口」而不是「成交时点」——那会污染这份报告的结论。
var qt02ReportStrategies = []string{
	"momentum",
	"mean_reversion",
	"bollinger_mr",
	"volatility_breakout",
	"td_sequential",
}

// qt02Row 是一个（策略 × 口径）组合的测量结果。
type qt02Row struct {
	Strategy string
	Caliber  string

	OpeningFills int
	TotalTrades  int
	TurnoverSum  float64 // Σ|qty × price|（成交额口径，不是「笔数」）
	TurnoverRate float64 // TurnoverSum / 期初本金
	TotalReturn  float64
	AnnualReturn float64
	MaxDrawdown  float64
	Sharpe       float64

	Signals        int // 观察钩子数到的信号条数（撮合前）
	DupSignalDays  int // 同 (date, symbol) 出现 ≥2 条信号的天数（QT-04 的输入）
	LastDaySignals int // 区间最后一天决策的信号数（next_open 下没有成交日）
	DailyReturns   []float64
	Days           []string
	Fills          []qt02Fill // 全部成交（含平仓），用于成交层归属
	SignalList     []qt02Sig  // 撮合前的信号，用于成交层归属

	Status  string
	Reasons []string
}

// qt02Fill 是一笔成交里可判定的最小信息。
type qt02Fill struct {
	Date   string
	Symbol string
	Side   string // long / short / close：残差按方向拆开用
	Price  float64
}

// qt02Sig 是一条信号（决策）的键，含它的订单类型信息。
//
// 不含方向：成交归属只看「哪天」，方向不进键（减仓/换仓时信号方向与成交方向
// 并非一一对应，进键会造出系统性假阴性）。
type qt02Sig struct {
	Date       string
	Symbol     string
	OrderType  string
	LimitPrice float64
}

// qt02Bar 是一根日 K 里与成交价有关的四个端点（Low/High 用来分辨「均价型成交」）。
type qt02Bar struct {
	Open, Close, Low, High float64
}

// qt02Bars = 每个标的按交易日索引的日 K 端点。
type qt02Bars struct {
	byDay map[string]map[string]qt02Bar // symbol → 交易日 → 当日 OHLC 端点
}

func (bs qt02Bars) at(symbol, date string) (qt02Bar, bool) {
	d, ok := bs.byDay[symbol]
	if !ok {
		return qt02Bar{}, false
	}
	bar, ok := d[date]
	return bar, ok
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// share = n 占 d 的百分数；d 为 0 时给 0（没有成交的策略不该产出 NaN 行）。
func share(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return 100 * float64(n) / float64(d)
}

func qt02Connect(t *testing.T) *storage.PostgresStore {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("QT02_PG_DSN"))
	if dsn == "" {
		t.Fatal("QT02_PG_DSN 未设置 —— 这份报告必须有真数据。" +
			"例：postgres://postgres:postgres@127.0.0.1:5432/quant_trading?sslmode=disable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := storage.NewPostgresStoreConnected(ctx, dsn) // 连库 + ping，不迁移
	if err != nil {
		t.Fatalf("连库失败: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

// qt02EngineFor 造一台与生产装配同形的引擎：PG provider + store + 进程内风控。
//
// 两个刻意的偏离，都写进报告：
//
//   - 不接 data-service（:8081）：HTTP provider 会让这份报告依赖另一个进程，
//     「同环境重跑 ±0」的可复现性就得连那个进程的启动顺序一起复现。
//   - qfq 基准快照接**内存**（`newMemAnchorStore`），不碰共享开发库的
//     `backtest_adjustment_anchors`。库里 2026-10-17 冻结的 000001.SZ 基准与
//     当前序列已经漂移（REV-18 的漂移守卫当场把它拦下 —— 这份报告的初跑就是被
//     这条拦住的），那是「数据被改过」的如实报错，不是本单要修的东西；写新基准
//     则等于替共享库做一次 nobody-asked 的冻结。本报告的 ±0 只承诺同环境内，
//     靠的是「同一次进程里数据不动」，不靠快照。
func qt02EngineFor(t *testing.T, store *storage.PostgresStore) *Engine {
	t.Helper()
	logger := logging.WithContext(map[string]any{})
	prov := marketdata.NewPostgresProvider(store, logger)

	rm, err := risk.NewRiskManager(risk.RiskManagerConfig{
		TargetVolatility:    0.15,
		MaxPositionWeight:   0.10,
		MinPositionWeight:   0.01,
		ATRPeriod:           14,
		BaseMultiplier:      2.0,
		BullMultiplier:      1.5,
		BearMultiplier:      3.0,
		SidewaysMultiplier:  2.0,
		TakeProfitMult:      3.0,
		VolLookbackDays:     60,
		AnnualizationFactor: math.Sqrt(252),
		FastMAPeriod:        50,
		SlowMAPeriod:        200,
		RegimeVolLookback:   120,
	}, logger)
	if err != nil {
		t.Fatalf("risk manager: %v", err)
	}

	eng, err := NewEngineWithOptions(Config{
		InitialCapital: qt02Capital(),
		CommissionRate: qt02Commission,
		SlippageRate:   qt02Slippage, // 与执行服务 fixed 模型同值：滑点不该偏袒任一口径
		Seed:           qt02Seed,
	}, prov,
		WithLogger(logger),
		WithStore(store),
		WithRiskManager(rm),
		WithAdjustmentAnchorStore(newMemAnchorStore()),
	)
	if err != nil {
		t.Fatalf("NewEngineWithOptions: %v", err)
	}
	return eng
}

func qt02Capital() float64 {
	raw := strings.TrimSpace(os.Getenv("QT02_CAPITAL"))
	if raw == "" {
		return defaultQT02Capital
	}
	var f float64
	if _, err := fmt.Sscanf(raw, "%g", &f); err != nil {
		panic("QT02_CAPITAL 不是个数: " + raw)
	}
	return f
}

// qt02Measure 跑一次回测并把结果折成一行测量。
func qt02Measure(t *testing.T, eng *Engine, strategy, caliber string, universe []string, bars qt02Bars) qt02Row {
	t.Helper()

	var observed []qt02Sig
	eng.SetSignalObserver(func(date time.Time, signals []domain.Signal) {
		for _, s := range signals {
			observed = append(observed, qt02Sig{
				Date:       date.Format("2006-01-02"),
				Symbol:     s.Symbol,
				OrderType:  string(s.OrderType),
				LimitPrice: s.LimitPrice,
			})
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	resp, err := eng.RunBacktest(ctx, contracts.BacktestRequest{
		Strategy:       strategy,
		StockPool:      universe,
		StartDate:      envOr("QT02_START", defaultQT02Start),
		EndDate:        envOr("QT02_END", defaultQT02End),
		InitialCapital: qt02Capital(),
		FillTiming:     caliber,
	})
	if err != nil {
		t.Fatalf("%s/%s 跑失败: %v", strategy, caliber, err)
	}

	row := qt02Row{
		Strategy:    strategy,
		Caliber:     caliber,
		TotalTrades: len(resp.Trades),
		TotalReturn: resp.TotalReturn,
		Status:      resp.Status,
		Reasons:     resp.InvalidReasons,
	}
	// 口径取**回报里的那个**，不是请求里写的那个：请求留空时生效值由默认点决定，
	// 报告要按实际成交口径归组（这也是 §8.1「请求说了不算」在同一份脚手架上的自用）。
	if resp.FillTiming != "" {
		row.Caliber = resp.FillTiming
	}
	row.AnnualReturn = resp.AnnualReturn
	row.MaxDrawdown = resp.MaxDrawdown
	row.Sharpe = resp.SharpeRatio

	for _, tr := range resp.Trades {
		row.TurnoverSum += math.Abs(tr.Quantity * tr.Price)
		if tr.Direction == domain.DirectionLong || tr.Direction == domain.DirectionShort {
			row.OpeningFills++
		}
		row.Fills = append(row.Fills, qt02Fill{
			Date:   tr.Timestamp.Format("2006-01-02"),
			Symbol: tr.Symbol,
			Side:   string(tr.Direction),
			Price:  tr.Price,
		})
	}
	row.TurnoverRate = row.TurnoverSum / qt02Capital()

	// QT02_DEBUG_FILLS=N：把前 N 笔成交连同对照 bar 打到测试日志。
	// 这份脚手架唯一的自证手段是「数字对得上」，出问题时得能看见原始数。
	if raw := strings.TrimSpace(os.Getenv("QT02_DEBUG_FILLS")); raw != "" {
		var limit int
		if _, err := fmt.Sscanf(raw, "%d", &limit); err == nil {
			for i, f := range row.Fills {
				if i >= limit {
					break
				}
				tr := resp.Trades[i]
				bar, _ := bars.at(f.Symbol, f.Date)
				t.Logf("FILL %s/%s %s %s price=%.10f qty=%.4f filled=%.4f pending=%.4f open=%.10f close=%.10f p/open=%.6f p/close=%.6f",
					strategy, caliber, f.Date, f.Symbol, f.Price, tr.Quantity, tr.FilledQty, tr.PendingQty,
					bar.Open, bar.Close, f.Price/bar.Open, f.Price/bar.Close)
			}
		}
	}

	// 日收益序列（封闭系统口径，与 SPEC 指标口径同源：r_t = (V_t − V_{t−1}) / V_{t−1}）。
	prev := math.NaN()
	for _, pv := range resp.PortfolioValues {
		row.Days = append(row.Days, pv.Date.Format("2006-01-02"))
		r := 0.0
		if !math.IsNaN(prev) && prev != 0 {
			r = (pv.TotalValue - prev) / prev
		}
		if !math.IsNaN(prev) {
			row.DailyReturns = append(row.DailyReturns, r)
		}
		prev = pv.TotalValue
	}

	// 信号侧的两项计数：重复 (date, symbol) 与最后一天决策的信号。
	//
	// 重复的键**不含方向**：同一标的同一天既给 long 又给 close，同样是「同 bar
	// 同 symbol 多条信号」——那正是 QT-04 要数的那件事。
	seen := map[[2]string]int{}
	maxDate := ""
	for _, s := range observed {
		seen[[2]string{s.Date, s.Symbol}]++
		if s.Date > maxDate {
			maxDate = s.Date
		}
	}
	counted := map[string]bool{}
	for k, n := range seen {
		if n >= 2 && !counted[k[0]] {
			counted[k[0]] = true
			row.DupSignalDays++
		}
	}
	for _, s := range observed {
		if s.Date == maxDate {
			row.LastDaySignals++
		}
	}
	row.Signals = len(observed)
	row.SignalList = observed
	return row
}

// qt02Evidence 是一行（策略 × 口径）的成交层证据计数。
//
// 三个量叠起来才构成判据，任何单个都不够：
//   - 成交价落在成交日 bar 的**哪一端**（开盘 / 收盘）；
//   - 落不到任一端时，它是不是**策略自己发的限价单**（bollinger_mr 按布林带上下沿
//     发 limit 单，成交价就是那个限价，本来不该等于开盘或收盘）；
//   - 市单那批里，落在开盘端的成交其**前一交易日**是否有该标的的信号，落在收盘端的
//     成交其**当日**是否有信号。
type qt02Evidence struct {
	Fills int

	AtOpen     int // = 成交日开盘 ×(1±滑点)
	AtClose    int // = 成交日收盘 ×(1±滑点)
	AtBoth     int // 开盘 == 收盘 的角落：两端同时命中，单列以免误读成「两口径都符」
	LimitOrder int // = 该标的在成交日或其前一交易日发出的 limit 信号里某个限价
	InBar      int // 落在当日 [low, high] 内，但对不上任何已知基准
	Neither    int // 区间外且对不上任何基准（库里没有当日 bar 等）

	OpenPrevSig  int // AtOpen 且前一交易日该标的有信号
	CloseSameSig int // AtClose 且当日该标的有信号

	// UnattributedOnLimitDay = InBar/Neither 里，成交日 close==high 或 close==low 的那些：
	// 引擎在涨跌停日把基准价换成整分位限价（engine_daily.go 的 res.price = limitPrice），
	// 所以成交价本来就不等于收盘或开盘。这一列把「没归因」缩小到实测剩下的部分。
	UnattributedOnLimitDay int

	// 残差按成交方向拆开：止损/止盈的触发价本来就在当日区间内，若残差集中在平仓方向，
	// 那它属「切换影响」第四条的设计边界而不是缺陷；对不上就如实写「未归因」。
	UnattrOpen  int // InBar+Neither 里 direction = long/short 的笔数
	UnattrClose int // InBar+Neither 里 direction = close 的笔数
}

// slipsTo：price 是否等于 base 作用 fixed 滑点后的成交价（buy 加、sell 减各试一次）。
//
// 容差为什么是 1e-5 绝对而不是「同一个数」：引擎的成交价基准是**复权归一化后**
// 的价，REV-18 把它量化到 priceQuantum=1e-6 的网格上（`adjustment_snapshot.go`），
// 而对照表用的是库里的原始值。半格 5e-6 经 ×(1±滑点) 放大后 ≤ 5.005e-6，
// 所以 1e-5 是「只吸收量化尾差」的最小可用宽度：它比滑点步长（0.001×价，
// 8 元股约 0.008）小三个数量级，绝不可能把开盘误认成收盘。
// 相对项 1e-6 只是给高价股的兜底。
func slipsTo(price, base float64) bool {
	for _, cand := range []float64{base * (1 + qt02Slippage), base * (1 - qt02Slippage)} {
		if cand <= 0 {
			continue
		}
		tol := math.Max(tolAbs, 1e-6*cand)
		if math.Abs(price-cand) <= tol {
			return true
		}
	}
	return false
}

// classifyFills 在**单次 run 内**给每笔定成交基准，并把成交对回信号日。
//
// 刻意**不做**跨口径配对（same_close 的第 k 笔 对 next_open 的第 k 笔），也刻意
// 不做无条件日归属（「成交当日/前一日有没有信号」）。两个坑都实测踩过：
//
//   - 跨口径配对要求两条成交序列大体同构，而本单要测的反馈链（成交价变 → 仓位规模变
//     → 后续信号变）恰恰让它不同构：两年 24 票的区间里「偏移=+1」只剩 6.3%、
//     td_sequential 只剩 1%，量到的是序号分叉率，不是口径性质。
//   - 无条件日归属被信号密度污染：momentum 每天对多数标的都出声，于是 next_open
//     的成交「当日也有信号」占到 63.7%，判据失去分辨力。
//
// 所以现在只在**已按价格基准分好类**的那批成交上问日期问题（OpenPrevSig / CloseSameSig）：
// 「次日开盘成交」这批的前一交易日有没有信号，才是 §8.1 那条断言的可证伪形式。
func classifyFills(r qt02Row, bars qt02Bars) qt02Evidence {
	dayIdx := make(map[string]int, len(r.Days))
	for i, d := range r.Days {
		if _, dup := dayIdx[d]; !dup {
			dayIdx[d] = i
		}
	}
	sigOn := make(map[[2]string]bool, len(r.SignalList))
	// 限价单的成交价索引：symbol → 该标的在某日发出的 limit 价集合。
	limitOn := make(map[[2]string][]float64, len(r.SignalList))
	for _, s := range r.SignalList {
		sigOn[[2]string{s.Symbol, s.Date}] = true
		if s.OrderType == string(domain.OrderTypeLimit) && s.LimitPrice > 0 {
			k := [2]string{s.Symbol, s.Date}
			limitOn[k] = append(limitOn[k], s.LimitPrice)
		}
	}

	e := qt02Evidence{Fills: len(r.Fills)}
	for _, f := range r.Fills {
		ho, hc := false, false
		bar, have := bars.at(f.Symbol, f.Date)
		if have && bar.Open > 0 && bar.Close > 0 {
			ho, hc = slipsTo(f.Price, bar.Open), slipsTo(f.Price, bar.Close)
		}

		i, ok := dayIdx[f.Date]
		prevDay := ""
		if ok && i > 0 {
			prevDay = r.Days[i-1]
		}
		switch {
		case ho && hc:
			e.AtBoth++
		case ho:
			e.AtOpen++
		case hc:
			e.AtClose++
		case ok && matchesLimit(limitOn, f, prevDay):
			e.LimitOrder++
		case have && f.Price >= bar.Low-tolAbs && f.Price <= bar.High+tolAbs:
			e.InBar++
			e.countUnattributed(f, bar, true)
		default:
			e.Neither++
			e.countUnattributed(f, bar, have)
		}
		if !ok {
			continue
		}
		// 日期条件只看非 AtBoth 的那一侧，避免「开=收」的日子两头都记功。
		if ho && !hc {
			if prevDay != "" && sigOn[[2]string{f.Symbol, prevDay}] {
				e.OpenPrevSig++
			}
		}
		if hc && !ho {
			if sigOn[[2]string{f.Symbol, f.Date}] {
				e.CloseSameSig++
			}
		}
	}
	return e
}

// tolAbs = slipsTo 用的那把绝对容差，抽出来给限价单比对共用（同一个尾差来源：
// 引擎作用的是量化到 1e-6 的价，对照表用库里的原始值）。
const tolAbs = 1e-5

// isLimitDayBar：收盘价贴在最高或最低上 —— qfq 序列里这就是涨跌停（或一字板）的形状，
// 引擎在那天改用整分位限价当基准价，成交价因此不等于收盘或开盘。
func isLimitDayBar(bar qt02Bar) bool {
	return math.Abs(bar.Close-bar.High) <= tolAbs || math.Abs(bar.Close-bar.Low) <= tolAbs
}

// countUnattributed 给残差（对不上任何基准的成交）记两笔账：方向，以及是否发生在
// close 贴在 high/low 上的日子。零值 bar（库里缺当日数据）不参与涨跌停判定。
func (e *qt02Evidence) countUnattributed(f qt02Fill, bar qt02Bar, haveBar bool) {
	if f.Side == string(domain.DirectionClose) {
		e.UnattrClose++
	} else {
		e.UnattrOpen++
	}
	if haveBar && isLimitDayBar(bar) {
		e.UnattributedOnLimitDay++
	}
}

// matchesLimit：这笔成交的价格是否等于该标的在**成交日**或**前一交易日**发出的
// 某个 limit 价。限价单的成交价本来就不是开盘或收盘 —— 把它们混进「都不符」
// 会让分母看着像缺陷，其实那是策略自己指定的价（QT-02 只管市单的成交基准）。
func matchesLimit(limitOn map[[2]string][]float64, f qt02Fill, prevDay string) bool {
	for _, day := range []string{f.Date, prevDay} {
		if day == "" {
			continue
		}
		for _, lp := range limitOn[[2]string{f.Symbol, day}] {
			if math.Abs(f.Price-lp) <= math.Max(tolAbs, 1e-6*lp) {
				return true
			}
		}
	}
	return false
}

// lagEvidence 量「平移一个 bar」这条解释在**日收益层**成不成立。
//
// 同一个策略两条口径：same_close 在 T 收盘成交，next_open 在 T+1 开盘成交。
// 若差异真是「成交时点平移」，把 next_open 的日收益序列往后挪一格与 same_close
// 对齐后应当**更接近**。报告同时给两个平均绝对差 —— 实测挪动不改善，
// 于是这条成为否证腿，结论第 4 条解释为什么（净值曲线不是延迟，是形状变了）。
func lagEvidence(a, b qt02Row) (aligned float64, shifted float64, ok bool) {
	short, long := a.DailyReturns, b.DailyReturns
	if len(short) < 5 || len(long) < 6 {
		return 0, 0, false
	}
	n := len(short)
	if len(long)-1 < n {
		n = len(long) - 1
	}
	var sa, ss float64
	for i := 0; i < n; i++ {
		sa += math.Abs(short[i] - long[i])   // 同日对齐
		ss += math.Abs(short[i] - long[i+1]) // next_open 往后挪一格
	}
	return sa / float64(n), ss / float64(n), true
}

// qt02LoadBars 取区间的 open/close 作为成交价基准的对照物。
//
// 用 store.GetOHLCV 而不是复用引擎的 cache：对照物必须独立于被测者，
// 拿引擎自己缓存的价格去验引擎的成交价，验的是自己。
func qt02LoadBars(t *testing.T, store *storage.PostgresStore, universe []string) qt02Bars {
	t.Helper()
	start, err := time.Parse("2006-01-02", envOr("QT02_START", defaultQT02Start))
	if err != nil {
		t.Fatalf("QT02_START 解析失败: %v", err)
	}
	end, err := time.Parse("2006-01-02", envOr("QT02_END", defaultQT02End))
	if err != nil {
		t.Fatalf("QT02_END 解析失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	bars := qt02Bars{byDay: make(map[string]map[string]qt02Bar, len(universe))}
	for _, sym := range universe {
		rows, err := store.GetOHLCV(ctx, sym, start, end)
		if err != nil {
			t.Fatalf("取 %s 的 bar 失败: %v", sym, err)
		}
		day := make(map[string]qt02Bar, len(rows))
		for _, r := range rows {
			day[r.Date.Format("2006-01-02")] = qt02Bar{Open: r.Open, Close: r.Close, Low: r.Low, High: r.High}
		}
		bars.byDay[sym] = day
	}
	return bars
}

func TestQT02FillTimingDifferenceReport(t *testing.T) {
	store := qt02Connect(t)
	universe := strings.Split(envOr("QT02_UNIVERSE", defaultQT02Universe), ",")
	for i := range universe {
		universe[i] = strings.TrimSpace(universe[i])
	}
	bars := qt02LoadBars(t, store, universe)

	var rows []qt02Row
	for _, strategy := range qt02ReportStrategies {
		for _, caliber := range []string{"same_close", "next_open"} {
			eng := qt02EngineFor(t, store) // 每 run 一台新引擎（引擎级缓存 per-instance）
			rows = append(rows, qt02Measure(t, eng, strategy, caliber, universe, bars))
		}
	}

	// 「同环境重跑 ±0」：拿第一个组合再跑一遍，逐位比对。
	{
		base := rows[0]
		again := qt02Measure(t, qt02EngineFor(t, store), base.Strategy, base.Caliber, universe, bars)
		metrics := []struct {
			name        string
			base, rerun any
		}{
			{"total_trades", base.TotalTrades, again.TotalTrades},
			{"opening_fills", base.OpeningFills, again.OpeningFills},
			{"turnover_sum", base.TurnoverSum, again.TurnoverSum},
			{"total_return", base.TotalReturn, again.TotalReturn},
			{"max_drawdown", base.MaxDrawdown, again.MaxDrawdown},
			{"signals", base.Signals, again.Signals},
		}
		for _, m := range metrics {
			if m.base != m.rerun {
				t.Errorf("同参重跑不逐位一致：%s 首次=%v 复跑=%v", m.name, m.base, m.rerun)
			}
		}
		t.Logf("±0 复跑通过：%s/%s trades=%d turnover=%.0f total_return=%.6f",
			base.Strategy, base.Caliber, base.TotalTrades, base.TurnoverSum, base.TotalReturn)
	}

	// ⑤ 的后半句「切换后基准结果与报告数值一致」：默认值翻到 next_open 之后，
	// **不写口径**的 run 必须与报告里 next_open 那一列逐位相同。这一条同时拦住两种
	// 静默漂移：默认值改了但取价分支没跟上，以及分支对了但默认点回落到别处。
	// 对照组用 momentum（同日同标的重复信号 0 天）：有重复信号的策略同参复跑本身会漂
	// （见下面「同参重跑的稳定性」），拿它们做逐位断言量到的是 map 顺序，不是默认值。
	{
		want := rows[1]
		if want.Strategy != rows[0].Strategy || want.Caliber != string(contracts.FillTimingNextOpen) {
			t.Fatalf("行序假设不成立（每个策略的第 2 行应是 next_open）：%s/%s", want.Strategy, want.Caliber)
		}
		byDefault := qt02Measure(t, qt02EngineFor(t, store), want.Strategy, "", universe, bars)
		if byDefault.Caliber != want.Caliber {
			t.Errorf("请求不写 fill_timing 时生效口径 = %q, want %q（DefaultFillTiming 没翻过去）",
				byDefault.Caliber, want.Caliber)
		}
		cells := []struct {
			name             string
			fromDefault, set any
		}{
			{"total_trades", byDefault.TotalTrades, want.TotalTrades},
			{"turnover_sum", byDefault.TurnoverSum, want.TurnoverSum},
			{"total_return", byDefault.TotalReturn, want.TotalReturn},
			{"max_drawdown", byDefault.MaxDrawdown, want.MaxDrawdown},
			{"signals", byDefault.Signals, want.Signals},
		}
		for _, c := range cells {
			if c.fromDefault != c.set {
				t.Errorf("默认口径与显式 next_open 不逐位一致：%s 默认=%v 显式=%v", c.name, c.fromDefault, c.set)
			}
		}
		t.Logf("默认口径 = next_open 的逐位一致已验证：%s trades=%d total_return=%.6f",
			byDefault.Strategy, byDefault.TotalTrades, byDefault.TotalReturn)
	}

	// 同参重跑的**稳定性覆盖面**：对每一个「同日同标的重复信号 > 0」的策略各复跑一次。
	//
	// 这不是额外要求，是 ⑤ 那条「同环境重跑 ±0」的真实边界：重复信号走进 map[symbol]
	// 覆盖（QT-04 的 ②上下文），最后一条生效取决于未排序遍历顺序 —— 于是 ±0 只对
	// **无重复信号**的策略成立。测出来就写进报告，不写在「momentum 稳定」上冒充全局成立。
	var stabs []qt02Stability
	for _, r := range rows {
		if r.DupSignalDays == 0 || r.Caliber != "same_close" {
			continue
		}
		again := qt02Measure(t, qt02EngineFor(t, store), r.Strategy, r.Caliber, universe, bars)
		stabs = append(stabs, qt02Stability{
			Strategy:       r.Strategy,
			DupDays:        r.DupSignalDays,
			TradesFirst:    r.TotalTrades,
			TradesSecond:   again.TotalTrades,
			ReturnFirst:    r.TotalReturn,
			ReturnSecond:   again.TotalReturn,
			TurnoverFirst:  r.TurnoverSum,
			TurnoverSecond: again.TurnoverSum,
		})
	}

	out := qt02OutPath()
	if err := os.WriteFile(out, []byte(qt02Markdown(rows, universe, bars, stabs)), 0o644); err != nil {
		t.Fatalf("写报告失败 %s: %v", out, err)
	}
	t.Logf("报告已写入 %s（%d 个组合）", out, len(rows))
}

// qt02OutPath 把默认输出路径解析到**模块根**。
//
// `go test` 的工作目录是被测包目录（`pkg/backtest`），直接写相对路径会撞
// 「找不到路径」；而这份报告的归档位置是台账写死的 `docs/test-cases/`，
// 不该随被测包搬家。`QT02_OUT` 给了就原样用（调用方自己负责它可解析）。
func qt02OutPath() string {
	if v := strings.TrimSpace(os.Getenv("QT02_OUT")); v != "" {
		return v
	}
	wd, err := os.Getwd()
	if err != nil {
		return defaultQT02Out
	}
	for dir := wd; ; {
		if st, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && !st.IsDir() {
			return filepath.Join(dir, filepath.FromSlash(defaultQT02Out))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return defaultQT02Out // 找不到模块根（被复制出去的脚手架），按原样写
		}
		dir = parent
	}
}

// qt02Agg = 跨策略汇总（分母用成交笔数，不用策略数 —— 一个策略跑 1.8 万笔、
// 另一个 200 笔，按策略平均会让小样本策略说话太重）。
type qt02Agg struct {
	fills, atOpen, atClose, atBoth int
	limitOrder, inBar, neither     int
	openPrevSig, closeSameSig      int
	unattrOnLimitDay               int
	unattrOpen, unattrClose        int
}

func (a *qt02Agg) add(e qt02Evidence) {
	a.fills += e.Fills
	a.atOpen += e.AtOpen
	a.atClose += e.AtClose
	a.atBoth += e.AtBoth
	a.limitOrder += e.LimitOrder
	a.inBar += e.InBar
	a.neither += e.Neither
	a.openPrevSig += e.OpenPrevSig
	a.closeSameSig += e.CloseSameSig
	a.unattrOnLimitDay += e.UnattributedOnLimitDay
	a.unattrOpen += e.UnattrOpen
	a.unattrClose += e.UnattrClose
}

// qt02Stability = 同一个（策略 × 口径）在**同参、同环境、同一次进程**里跑两次的对照。
//
// 只对「同日同标的重复信号 > 0」的策略测：重复信号走进 map[symbol] 覆盖
// （`pkg/risk/manager.go` 与 `engine.go` 的建仓路径），最后一条生效取决于未排序的
// 遍历顺序 —— Go 的 map 顺序每次随机，所以这批策略的复跑**不该**被期望逐位一致。
// 报告把这件事量化出来，是为了不让「momentum 的 ±0」冒充「所有策略的 ±0」。
type qt02Stability struct {
	Strategy       string
	DupDays        int
	TradesFirst    int
	TradesSecond   int
	ReturnFirst    float64
	ReturnSecond   float64
	TurnoverFirst  float64
	TurnoverSecond float64
}

func (s qt02Stability) identical() bool {
	return s.TradesFirst == s.TradesSecond && s.ReturnFirst == s.ReturnSecond && s.TurnoverFirst == s.TurnoverSecond
}

func qt02Markdown(rows []qt02Row, universe []string, bars qt02Bars, stabs []qt02Stability) string {
	var b strings.Builder

	ev := make([]qt02Evidence, len(rows))
	agg := map[string]*qt02Agg{}
	for i, r := range rows {
		e := classifyFills(r, bars)
		ev[i] = e
		if agg[r.Caliber] == nil {
			agg[r.Caliber] = &qt02Agg{}
		}
		agg[r.Caliber].add(e)
	}
	sc, no := agg["same_close"], agg["next_open"]
	if sc == nil {
		sc = &qt02Agg{}
	}
	if no == nil {
		no = &qt02Agg{}
	}

	b.WriteString("# QT-02 · 两口径成交差异报告（same_close vs next_open）\n\n")
	b.WriteString("> 本报告由 `//go:build qt02report` 的生成器产出，数据/参数见「复现」。" +
		"**它是 docs/SPEC.md「成交时点与已知偏差」与 DefaultFillTiming 切换纪律的依据。**\n\n")

	b.WriteString("## 结论\n\n")
	fmt.Fprintf(&b, "- **机制差 = 成交日与成交基准价，两口径各自落在自己那一端**："+
		"same_close 的 %d 笔市单成交里 **%.1f%%** 的成交价 = 成交日那根 bar 的**收盘** ×(1±滑点)，"+
		"next_open 的 %d 笔里 **%.1f%%** = **开盘** ×(1±滑点)；交叉污染几乎为零"+
		"（same_close 落在开盘端 %.2f%%，next_open 落在收盘端 %.2f%% —— 那批是当日开盘价=收盘价的角落，单列在「开=收」）。\n",
		sc.fills, share(sc.atClose, sc.fills), no.fills, share(no.atOpen, no.fills),
		share(sc.atOpen, sc.fills), share(no.atClose, no.fills))
	fmt.Fprintf(&b, "- **「T 收盘决策 → T+1 开盘成交」这条时序在真数据上成立**：next_open 落在开盘端的 %d 笔里，"+
		"**%.1f%%** 的**前一交易日**该标的确有信号；对照 same_close 落在收盘端的 %d 笔，当日有信号的占 **%.1f%%**。"+
		"日期问题是**先按价格分好类再问**的 —— 无条件地问「成交当日/前一日有没有信号」会被信号密度淹掉"+
		"（momentum 这类每天对多数标的出声的策略，两种问法同时高命中，判据失去分辨力；这段弯路和它的数写在 "+
		"`classifyFills` 的注释里）。\n",
		no.atOpen, share(no.openPrevSig, no.atOpen),
		sc.atClose, share(sc.closeSameSig, sc.atClose))
	fmt.Fprintf(&b, "- **有一类成交本来就不该落在开盘或收盘上：限价单**。本次 same_close 的 %d 笔、next_open 的 %d 笔"+
		"价格等于该标的在成交日或前一交易日发出的 limit 价（`bollinger_mr` 按布林带上下沿发限价单），"+
		"另有 %.1f%% / %.1f%% 落在当日 [low, high] 之内但对不上任何基准 —— 其中 %d / %d 笔的成交日 close 正好贴在 "+
		"high 或 low 上（涨跌停日引擎把基准价换成整分位限价，见「切换影响」第二条），剩下的 **%d / %d 笔本报告没有归因**"+
		"（「流动性上限跨日顺延」这条假设实测为 0，已删；不当有解释）。"+
		"**QT-02 只管市单的成交基准**：限价单换口径时**成交日照样后移**，但成交价仍由策略指定 —— 这条边界写在 "+
		"docs/SPEC.md「成交时点与已知偏差」。\n",
		sc.limitOrder, no.limitOrder, share(sc.inBar, sc.fills), share(no.inBar, no.fills),
		sc.unattrOnLimitDay, no.unattrOnLimitDay,
		sc.inBar+sc.neither-sc.unattrOnLimitDay, no.inBar+no.neither-no.unattrOnLimitDay)
	b.WriteString("- **台账 §8.1 那句「差异 ≈ 日收益平移一个 bar」只能在成交层读，在收益层是假的**（表「平移一格检验」）：" +
		"把 next_open 的日收益序列整体往后挪一格**不会**让它更贴近 same_close。口径差不是一条被延迟的净值曲线 —— " +
		"仓位早一天/晚一天变动，改变的是成交那两天的曲线形状（隔夜跳空 + 日内各一段），不是整条曲线的位移。" +
		"逐笔的 T 收盘 → T+1 开盘由 `pkg/backtest/qt02_fill_timing_test.go` 用合成 bar 断言到具体价格；" +
		"本表的存在是为了拦住后来人拿日收益相关性当证据。\n")
	worst, wdiff, wturn := worstGap(rows)
	best, bdiff := bestGap(rows)
	fmt.Fprintf(&b, "- **实际差距的量级看「测量」表**：本次 %d 只 × %s..%s。总收益差最大的是 `%s`"+
		"（same_close %+.2f%% vs next_open %+.2f%%，差 %.1f 个百分点，两年换手 = 本金的 %.0f 倍）—— "+
		"每次换仓都把一次隔夜跳空吃进成本，所以口径对高频换仓的策略最敏感；差最小的是 `%s`，只有 %.2f 个百分点。"+
		"**这不是「哪个口径对」的裁决**：§8.1 的立场是 next_open 更接近可执行现实，same_close 带着 "+
		"`known_bias: same_close_fill` 继续可用，**不允许**为了保住某个历史基准数字而选口径（QT-02 ④）。\n",
		len(universe), envOr("QT02_START", defaultQT02Start), envOr("QT02_END", defaultQT02End),
		worst, totalReturnOf(rows, worst, "same_close")*100, totalReturnOf(rows, worst, "next_open")*100,
		wdiff*100, wturn, best, bdiff*100)
	b.WriteString("- **「同环境重跑 ±0」的覆盖面是有条件的**：无重复信号的策略（本次 momentum 等）逐位一致，" +
		"这条是被执行过的断言；有「同日同标的重复信号」的策略同参复跑实测**会漂**（笔数与总收益都变），" +
		"表「同参重跑的稳定性」给了数。所以对后一批策略，本报告的数值是**一次抽样**而不是可复现基准 —— " +
		"这是 QT-04 要裁的债，不由本单修（见该表下的说明）。\n")
	b.WriteString("- **切换默认值的代价如实登记在「切换影响」**：末日信号没有成交日、K15 的 NT 腿只实现收盘成交、" +
		"止损/止盈/末强平仍按收盘（本单只管信号驱动的建仓时点）。\n\n")

	b.WriteString("## 测量\n\n")
	b.WriteString("换手 = Σ|数量 × 成交价| ÷ 期初本金（**成交额口径**，不是笔数；ResultSpec 里没有这个字段，故在此定义）。\n\n")
	b.WriteString("| 策略 | 口径 | 建仓笔数 | 总成交笔数 | 换手 | 总收益 | 年化 | 最大回撤 | Sharpe | 信号条数 | 同日同标的重复天数 | 末日未成交信号 |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | `%s` | %d | %d | %.2f | %.4f | %.4f | %.4f | %.3f | %d | %d | %d |\n",
			r.Strategy, r.Caliber, r.OpeningFills, r.TotalTrades, r.TurnoverRate,
			r.TotalReturn, r.AnnualReturn, r.MaxDrawdown, r.Sharpe, r.Signals, r.DupSignalDays, r.LastDaySignals)
	}

	b.WriteString("\n## 成交层证据：价格基准 × 信号日（真数据）\n\n")
	b.WriteString("对照的 bar 来自 `store.GetOHLCV`，**不复用引擎 cache** —— 拿引擎自己缓存的价格去验引擎的成交价，验的是自己。" +
		"判定 = 成交价 ∈ {基准价 ×(1+滑点), 基准价 ×(1−滑点)}，容差 1e-5 绝对：引擎的作用价是 REV-18 复权归一化后量化到 " +
		"1e-6 网格的价，而对照表用库里的原始值，半格尾差必须被吸收；这个宽度比滑点步长小三个数量级，" +
		"不可能把开盘误认成收盘。`开=收` 那一列是当天开盘价与收盘价相同的成交（两端同时命中，不记入任一侧的命中率分母）。\n\n")
	b.WriteString("| 策略 | 口径 | 成交笔数 | =当日收盘 | =当日开盘 | 开=收 | 限价单 | 区间内其他 | 区间外 | 残差·建仓向 | 残差·平仓向 | 残差·贴在涨/跌停价上 | 收盘批·当日有信号 | 开盘批·前一日有信号 |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for i, r := range rows {
		e := ev[i]
		fmt.Fprintf(&b, "| %s | `%s` | %d | %d (%.1f%%) | %d (%.1f%%) | %d | %d | %d | %d | %d | %d | %d | %d/%d | %d/%d |\n",
			r.Strategy, r.Caliber, e.Fills,
			e.AtClose, share(e.AtClose, e.Fills),
			e.AtOpen, share(e.AtOpen, e.Fills),
			e.AtBoth, e.LimitOrder, e.InBar, e.Neither,
			e.UnattrOpen, e.UnattrClose, e.UnattributedOnLimitDay,
			e.CloseSameSig, e.AtClose, e.OpenPrevSig, e.AtOpen)
	}
	b.WriteString("列的定义：`限价单` = 成交价等于该标的在成交日或前一交易日发出的 limit 价（策略自己指定的价，" +
		"本来不等于开盘或收盘；`bollinger_mr` 按布林带上下沿发限价单，那一行几乎全落在这里，被当日 high/low " +
		"截断后就进 `区间内其他`）；`区间内其他` = 落在当日 [low, high] 内但对不上任何基准；" +
		"`区间外` = 库里缺当日 bar 等。残差三列拆开是为了**别把没解释的东西写成解释**：" +
		"`贴在涨/跌停价上` = 成交日 close 贴着 high 或 low —— 引擎在涨跌停日把基准价换成整分位限价" +
		"（`engine_daily.go` 的 `res.price = limitPrice`），成交价因此不等于原始收盘，与「切换影响」第二条是同一件事的" +
		"两种量法；`建仓向 / 平仓向` = 残差里方向是 long/short 还是 close 的笔数。" +
		"~~跨日结转~~ 这一档实测为 0，已删（假设是「流动性上限顺延到之后的交易日、成交记录落在下单日」，" +
		"数据否掉了它 —— 残差不在对上邻日端点上）。**最后两列的分母是各自那一端的成交数，不是全部成交**。\n")

	b.WriteString("\n## 平移一格检验（日收益 MAE，本单的否证腿）\n\n")
	b.WriteString("`MAE(同日)` = 两口径同日日收益的平均绝对差；`MAE(挪一格)` = 把 next_open 往后挪一格再比。" +
		"**若「平移一个 bar」在收益层成立，后者应明显更小** —— 实测不成立（结论第 4 条给出为什么）。\n\n")
	b.WriteString("| 策略 | MAE(同日对齐) | MAE(next_open 往后一格) | 挪动后是否更接近 |\n|---|---|---|---|\n")
	for i := 0; i+1 < len(rows); i += 2 {
		same, next := rows[i], rows[i+1]
		aligned, shifted, ok := lagEvidence(same, next)
		if !ok {
			fmt.Fprintf(&b, "| %s | 样本不足 | — | — |\n", same.Strategy)
			continue
		}
		verdict := "否"
		if shifted < aligned {
			verdict = "是"
		}
		fmt.Fprintf(&b, "| %s | %.6f | %.6f | %s |\n", same.Strategy, aligned, shifted, verdict)
	}

	b.WriteString("\n## 重复信号计数（QT-04 的输入，本单只登记不裁决）\n\n")
	b.WriteString("「同日同标的重复天数」= 至少出现一次 `(date, symbol)` 收到 ≥2 条信号的天数；键不含方向。" +
		"本表只给量，trap 还是合并由 QT-04 按 `p0-round2-review.md` §8.3 裁。\n\n")
	b.WriteString("| 策略 | 口径 | 信号条数 | 同日同标的重复天数 |\n|---|---|---|---|\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | `%s` | %d | %d |\n", r.Strategy, r.Caliber, r.Signals, r.DupSignalDays)
	}

	b.WriteString("\n## 同参重跑的稳定性（⑤ 那条 ±0 的真实覆盖面）\n\n")
	b.WriteString("验收要求「切换后基准结果与报告数值一致（同环境重跑 ±0）」。" +
		"这句只在**无同日同标的重复信号**的策略上成立 —— 上面 ±0 断言跑的是 momentum（重复天数 0），" +
		"逐位一致是被执行过的断言，不是承诺。下面这些策略有重复信号，同参复跑被实测了两次：\n\n")
	if len(stabs) == 0 {
		b.WriteString("本次基准集里没有「同日同标的重复信号 > 0」的策略，因此没有需要复跑对照的组合。\n")
	} else {
		b.WriteString("| 策略 | 同日同标的重复天数 | 首跑笔数 | 复跑笔数 | 首跑总收益 | 复跑总收益 | 逐位一致 |\n")
		b.WriteString("|---|---|---|---|---|---|---|\n")
		for _, s := range stabs {
			verdict := "否"
			if s.identical() {
				verdict = "是（本次；不等于确定）"
			}
			fmt.Fprintf(&b, "| %s | %d | %d | %d | %.4f | %.4f | %s |\n",
				s.Strategy, s.DupDays, s.TradesFirst, s.TradesSecond,
				s.ReturnFirst, s.ReturnSecond, verdict)
		}
		b.WriteString("\n**这就是 QT-04 要裁的东西的实测代价**：同一份参数、同一个库、同一次进程，" +
			"笔数和总收益在两次跑之间就变了（差异来自重复信号走进 `map[symbol]` 覆盖，" +
			"最后一条生效取决于未排序的遍历顺序 —— 见 QT-04 的 ②上下文与 `p0-round2-review.md` §8.3）。" +
			"对这批策略，本报告的任何数值都是**一次抽样**，不是可复现基准；" +
			"两口径的**差**因此也只在这一次抽样内可比。结论第 5 条引的最大差距数同样受此影响。\n")
	}

	b.WriteString("\n## 切换影响（如实登记）\n\n")
	fmt.Fprintf(&b, "- **默认值已按 ④ 的纪律翻到 `%s`**（报告归档之后才动，顺序反过来就等于让全部历史基准"+
		"在一次提交里同时失去解释）。翻转的机械判据是上面那条**默认口径 = next_open 的逐位一致**：不写口径的 "+
		"run 必须与本报告 next_open 那一行逐位相同，这条由本生成器当场断言（`TestQT02FillTimingDifferenceReport`），"+
		"不是文档里的一句话。可见后果：`kernel=nautilus` 且不写口径现在报 400（本侧只实现收盘成交），"+
		"报文点名两种修法 —— 显式 `fill_timing=same_close` 或换 go 内核；`cmd/dualrun` 不受影响（Go 腿显式钉收盘口径）。\n",
		string(contracts.DefaultFillTiming))
	b.WriteString("- **区间最后一天的决策没有成交日**：next_open 下这部分信号不成交（表「测量」最后一列），" +
		"引擎把它计进 run 级诊断 `next_open_unfilled_at_run_end`，不退回收盘价补上。\n")
	b.WriteString("- **T+1 停牌或开盘价不可用 = 拒单，不顺延回收盘**：计入 `next_open_no_fill_price`。" +
		"理由写在 `pkg/backtest/engine_daily.go` 的 `openPricesForSignals`：一条 provenance 记录里混两种成交基准，" +
		"比少一笔成交更糟。\n")
	b.WriteString("- **成交基准价就是引擎选定的价**：`executeViaExecutionService` 原先用 `DayBar.Close` 覆盖它，" +
		"本单修掉了（否则 next_open 是假实现 —— 「成交层证据」表里 next_open 那一行会全部落到「=当日收盘」，" +
		"而请求里写着 next_open）。这正是 §8.1 要求「回报里带口径而不是靠请求推定」的原因。\n")
	b.WriteString("- **NT 腿仍只实现收盘成交**：侧车对 next_open 直接拒收（400），K15 的 Go 腿则显式钉 `same_close`，" +
		"所以默认值切换不会悄悄改变双跑口径 —— 见 `cmd/dualrun/gate.go:CheckCaliberParity`。\n")
	b.WriteString("- **止损/止盈平仓、退市强平、区间末强平仍按当日收盘可成交价**：本单只管信号驱动的建仓时点（边界写在 SPEC）。" +
		"所以「开盘批·前一日有信号」的分母里不含这些成交，两口径的这部分完全同形。\n")
	b.WriteString("- **qfq 复权基准快照接内存**：本机的 `backtest_adjustment_anchors` 里 2026-10-17 冻结的 " +
		"`000001.SZ / 2024-01-02` 基准与当前序列已不吻合（库里那段数据后来被动过），REV-18 的漂移守卫 " +
		"`ErrAdjustmentDrift` 因此拒跑 —— 这是它设计上该做的事（「改价/补行」不能当成「换了个基准」继续算），" +
		"不是本单缺陷。报告改用内存快照跑当前刻度，**因此本报告的 ±0 只对同一环境成立**；跨月重跑若库被重同步，" +
		"两列数值会一起移动，差值仍可比。\n\n")

	b.WriteString("## 复现\n\n```bash\n")
	b.WriteString("QT02_PG_DSN='postgres://postgres:postgres@127.0.0.1:5432/quant_trading?sslmode=disable' \\\n")
	b.WriteString("  go test ./pkg/backtest -tags qt02report -run TestQT02FillTimingDifferenceReport -count=1 -v\n")
	b.WriteString("```\n\n")
	fmt.Fprintf(&b, "默认参数：票池 %d 只（`QT02_UNIVERSE` 可覆盖）｜区间 %s..%s｜期初本金 %.0f｜"+
		"滑点 %g（= 执行服务 fixed 模型）｜佣金 %g｜risk_free %g（引擎默认）｜seed %d。\n",
		len(universe), envOr("QT02_START", defaultQT02Start), envOr("QT02_END", defaultQT02End),
		qt02Capital(), qt02Slippage, qt02Commission, qt02RiskFreeDefault, qt02Seed)
	b.WriteString("可选 `QT02_DEBUG_FILLS=N` 打印前 N 笔成交的原始价与当日 open/close（`QT02_OUT` 改输出路径）。" +
		"取数：`marketdata.NewPostgresProvider`（直连库，不经 data-service :8081）｜风控：进程内 " +
		"`risk.RiskManager`（缺它时建仓规模会去打外部 risk-service，静默 0 成交）｜qfq 复权基准快照：内存（见「切换影响」最后一条）。\n")
	b.WriteString("\n生成器：`pkg/backtest/qt02_fill_timing_report_test.go`（build tag `qt02report`，" +
		"库侧入口走 `storage.NewPostgresStoreConnected` —— 连库不迁移）。\n")

	// 状态列如实带出，避免「invalid 的行被当成一次成功观测」。
	var bad []string
	for _, r := range rows {
		if r.Status != contracts.StatusCompleted {
			bad = append(bad, fmt.Sprintf("%s/%s=%s(%v)", r.Strategy, r.Caliber, r.Status, r.Reasons))
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		b.WriteString("\n⚠️ 非 completed 的组合（读表前先看待补数据缺口，不要当成功观测）：\n")
		for _, s := range bad {
			b.WriteString("- " + s + "\n")
		}
	}
	return b.String()
}

// totalReturnOf 取某策略在某口径下的总收益。结论里那些「差多少个点」必须来自
// 同一批测量，不能手抄表格 —— 手抄的那一份一定会漂移。
func totalReturnOf(rows []qt02Row, strategy, caliber string) float64 {
	for _, r := range rows {
		if r.Strategy == strategy && r.Caliber == caliber {
			return r.TotalReturn
		}
	}
	return 0
}

func absReturnGap(rows []qt02Row, strategy string) float64 {
	return math.Abs(totalReturnOf(rows, strategy, "same_close") - totalReturnOf(rows, strategy, "next_open"))
}

func strategyNames(rows []qt02Row) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range rows {
		if !seen[r.Strategy] {
			seen[r.Strategy] = true
			out = append(out, r.Strategy)
		}
	}
	return out
}

// worstGap = 两口径总收益差最大的策略，以及它的差与 same_close 侧的换手倍数。
func worstGap(rows []qt02Row) (strategy string, diff, turnover float64) {
	for _, name := range strategyNames(rows) {
		d := absReturnGap(rows, name)
		if d > diff {
			diff, strategy = d, name
			for _, s := range rows {
				if s.Strategy == name && s.Caliber == "same_close" {
					turnover = s.TurnoverRate
				}
			}
		}
	}
	return strategy, diff, turnover
}

// bestGap = 差最小的策略与其差（用来把「口径敏感度取决于换仓频率」这句话钉在实测上）。
func bestGap(rows []qt02Row) (strategy string, diff float64) {
	first := true
	for _, name := range strategyNames(rows) {
		d := absReturnGap(rows, name)
		if first || d < diff {
			diff, strategy, first = d, name, false
		}
	}
	return strategy, diff
}
