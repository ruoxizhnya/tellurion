package backtest_live

// QT-51（BE-02）：K5 切片 2 的信号对账端到端正证据。
//
// 该测试原本住在 `pkg/backtest/signal_reconcile_e2e_test.go`。QT-51 要求
// `pkg/backtest` 对 `pkg/live` 的 import 为 0，而对账的**被测对象**恰恰是
// 「paper（pkg/live.PaperSession）vs 回测（pkg/backtest.Engine）」这一对，
// 天然要同时认识两侧。因此把它迁到 e2e 层：这是跨域证据，不是引擎包单测。
//
// 读法 A 的最后一环：同一策略、同一批 bar、同一窗口语义，paper 回放与回测
// 两侧独立产出的信号序列，必须**逐条一致**（经 ReconcileSignals 比对，
// 零差异）。
//
// 为什么对「信号」而不是「成交」：信号在执行机制之前。成交差异会被 T+1 /
// 现金约束 / 风控污染（两侧这些机制本就不同），但信号只由「喂给策略的
// bar 序列」决定——若两侧喂序/窗口漂移，信号对账立刻变红。
//
// 隔离边界：本测试的策略**刻意忽略 portfolio**。纯 bar 驱动的信号才是
// 「信号生成机制」的可对账层；依赖 portfolio 的信号差异属于执行机制，
// 不在信号对账范围。

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"testing"
	"time"

	"github.com/rs/zerolog"
	cdom "github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/ruoxizhnya/tellurion-contracts/fees"
	"github.com/ruoxizhnya/tellurion-kit/clock"
	"github.com/ruoxizhnya/tellurion-kit/logging"
	"github.com/spf13/viper"
	"tellurion/pkg/backtest"
	"tellurion/pkg/live"
	"tellurion/pkg/marketdata"
	"tellurion/pkg/risk"
	"tellurion/pkg/strategy"
)

import "tellurion/pkg/domain"

// ─── 回测侧合成夹具（与 pkg/backtest 的基准夹具同源，此处自持一份） ───

func synthOHLCV(symbols []string, days int, start time.Time) map[string][]domain.OHLCV {
	rng := rand.New(rand.NewSource(42))
	out := make(map[string][]domain.OHLCV, len(symbols))
	for _, sym := range symbols {
		bars := make([]domain.OHLCV, 0, days)
		price := 10.0 + rng.Float64()*90.0
		for d := 0; d < days; d++ {
			date := start.AddDate(0, 0, d)
			drift := (rng.Float64() - 0.49) * 0.04
			open := price
			closePrice := open * (1 + drift)
			high := math.Max(open, closePrice) * (1 + rng.Float64()*0.01)
			low := math.Min(open, closePrice) * (1 - rng.Float64()*0.01)
			vol := float64(1_000_000 + rng.Int63n(5_000_000))
			turnover := (open + closePrice) / 2 * vol

			limitUp := rng.Float64() < 0.05
			if limitUp {
				closePrice = math.Round(open*1.10*100) / 100
				high = closePrice
			}

			bars = append(bars, domain.OHLCV{
				Symbol:   sym,
				Date:     date,
				Open:     open,
				High:     high,
				Low:      low,
				Close:    closePrice,
				Volume:   vol,
				Turnover: turnover,
				LimitUp:  limitUp,
			})
			price = closePrice
		}
		out[sym] = bars
	}
	return out
}

func buildSyntheticEngine(t *testing.T, numSymbols, numDays int) (*backtest.Engine, marketdata.Provider, []string) {
	t.Helper()
	start := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	symbols := make([]string, numSymbols)
	for i := 0; i < numSymbols; i++ {
		symbols[i] = fmt.Sprintf("%06d.SH", 600000+i)
	}

	data := synthOHLCV(symbols, numDays, start)

	prov := marketdata.NewInMemoryProvider()
	for sym, bars := range data {
		prov.LoadOHLCV(sym, bars)
	}
	days := make([]time.Time, 0, numDays)
	for d := 0; d < numDays; d++ {
		days = append(days, start.AddDate(0, 0, d))
	}
	prov.SetTradingDays(days)

	stocks := make([]domain.Stock, 0, len(symbols))
	for _, sym := range symbols {
		stocks = append(stocks, domain.Stock{
			Symbol:   sym,
			Name:     "STK-" + sym,
			ListDate: start.AddDate(-1, 0, 0),
		})
	}
	prov.LoadStocks(stocks)

	v := viper.New()
	v.Set("backtest.initial_capital", 1000000.0)
	v.Set("backtest.commission_rate", fees.DefaultCommissionRate)
	v.Set("backtest.slippage_rate", fees.DefaultSlippageRate)
	v.Set("backtest.risk_free_rate", 0.03)
	v.Set("backtest.seed", 42)

	eng, err := backtest.NewEngine(v, prov, logging.WithContext(map[string]any{}))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	eng.LoadOHLCVInMemory(data)
	eng.SetParallelWorkers(1)

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
	}, zerolog.Nop())
	if err != nil {
		t.Fatalf("risk.NewRiskManager: %v", err)
	}
	eng.SetRiskManager(rm)
	return eng, prov, symbols
}

// reconcileProbeStrategy 是纯 bar 驱动的批式探针：对每个 symbol，若最新
// close 高于上一根 close 则发 buy（strength = 收益率）。**不读 portfolio**，
// 保证两侧信号只由 bar 序列决定——这是信号对账能对上的前提。
type reconcileProbeStrategy struct {
	name string
}

func newReconcileProbe(name string) *reconcileProbeStrategy {
	return &reconcileProbeStrategy{name: name}
}

func (s *reconcileProbeStrategy) Name() string                     { return s.name }
func (s *reconcileProbeStrategy) Description() string              { return "K5 signal-reconcile probe" }
func (s *reconcileProbeStrategy) Parameters() []strategy.Parameter { return nil }
func (s *reconcileProbeStrategy) Configure(map[string]interface{}) error {
	return nil
}
func (s *reconcileProbeStrategy) Cleanup()                                {}
func (s *reconcileProbeStrategy) Weight(strategy.Signal, float64) float64 { return 1 }

func (s *reconcileProbeStrategy) GenerateSignals(_ context.Context, bars map[string][]domain.OHLCV, _ *domain.Portfolio) ([]strategy.Signal, error) {
	syms := make([]string, 0, len(bars))
	for sym := range bars {
		syms = append(syms, sym)
	}
	sort.Strings(syms)

	var out []strategy.Signal
	for _, sym := range syms {
		hist := bars[sym]
		if len(hist) < 2 {
			continue
		}
		prev, cur := hist[len(hist)-2], hist[len(hist)-1]
		if prev.Close <= 0 || cur.Close <= prev.Close {
			continue
		}
		ret := cur.Close/prev.Close - 1
		out = append(out, strategy.Signal{
			Symbol:    sym,
			Action:    "buy",
			Strength:  ret,
			Price:     cur.Close,
			Date:      cur.Date,
			Direction: cdom.DirectionLong,
		})
	}
	return out, nil
}

// TestSignalReconcile_PaperVsBacktest 是信号对账的核心正证据：同一批合成
// bar，paper 与回测两侧独立跑，信号序列经 ReconcileSignals 零差异。
func TestSignalReconcile_PaperVsBacktest(t *testing.T) {
	// ── 回测侧：buildSyntheticEngine 造 engine + provider + symbols ──
	eng, prov, symbols := buildSyntheticEngine(t, 6, 30)

	backtestStrat := newReconcileProbe("k5_reconcile_probe_a")
	if err := strategy.GlobalRegisterFactory(func() strategy.Strategy { return backtestStrat }); err != nil {
		t.Fatalf("注册回测策略失败：%v", err)
	}

	var backtestSignals []domain.Signal
	eng.SetSignalObserver(func(_ time.Time, signals []domain.Signal) {
		backtestSignals = append(backtestSignals, signals...)
	})

	resp, err := eng.RunBacktest(context.Background(), backtest.BacktestRequest{
		Strategy:       backtestStrat.Name(),
		StockPool:      symbols,
		StartDate:      "2024-01-02",
		EndDate:        "2024-01-31",
		InitialCapital: 1_000_000,
		RiskFreeRate:   0.03,
	})
	if err != nil {
		t.Fatalf("回测失败：%v", err)
	}
	if resp.Status != "completed" {
		t.Fatalf("回测未完成：status=%s err=%s", resp.Status, resp.Error)
	}

	// ── paper 侧：同一个 provider，同一批 symbols，同窗口 ──
	paperStrat := newReconcileProbe("k5_reconcile_probe_paper") // 独立实例，无共享状态
	trader := live.NewMockTrader(live.MockTraderConfig{
		InitialCash:   1_000_000,
		SlippageModel: "fixed",
	}, zerolog.Nop())

	start := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	end := time.Date(2024, 1, 31, 0, 0, 0, 0, time.UTC)

	var paperSignals []domain.Signal
	sess, err := live.NewPaperSession(live.PaperSessionConfig{
		Provider: prov,
		Clock:    clock.NewVirtualClock(start),
		Trader:   trader,
		Logger:   zerolog.Nop(),
		SignalCollector: func(_ time.Time, signals []domain.Signal) {
			paperSignals = append(paperSignals, signals...)
		},
	})
	if err != nil {
		t.Fatalf("构造 paper session 失败：%v", err)
	}
	if _, err := sess.Run(context.Background(), symbols, start, end, paperStrat); err != nil {
		t.Fatalf("paper 回放失败：%v", err)
	}

	// ── 正证据：两侧信号必须都非空，否则「一致」是空洞的 ──
	if len(backtestSignals) == 0 {
		t.Fatal("回测侧未产出信号，对账断言无意义")
	}
	if len(paperSignals) == 0 {
		t.Fatal("paper 侧未产出信号，对账断言无意义")
	}

	diff := live.ReconcileSignals(paperSignals, backtestSignals, live.DefaultSignalReconcileConfig())
	if len(diff) != 0 {
		t.Fatalf("paper 与回测信号不一致（%d 条差异）：\n%s",
			len(diff), diffStrings(diff))
	}
	t.Logf("信号对账通过：paper %d 条、回测 %d 条，逐条一致", len(paperSignals), len(backtestSignals))
}

// TestSignalReconcile_ClockDivergenceDetected 是破坏验证的核心反证腿：
// paper 用错误的起始时钟（晚于首根 bar）会导致时间倒退、Replay fail-loud；
// 更直接的「不同输入 → 对账报差异」用截断窗口证明——paper 少喂一个交易日，
// 信号序列立刻与回测不一致，ReconcileSignals 必须报差异。
func TestSignalReconcile_ClockDivergenceDetected(t *testing.T) {
	eng, prov, symbols := buildSyntheticEngine(t, 6, 30)

	backtestStrat := newReconcileProbe("k5_reconcile_probe_b")
	if err := strategy.GlobalRegisterFactory(func() strategy.Strategy { return backtestStrat }); err != nil {
		t.Fatalf("注册回测策略失败：%v", err)
	}
	var backtestSignals []domain.Signal
	eng.SetSignalObserver(func(_ time.Time, signals []domain.Signal) {
		backtestSignals = append(backtestSignals, signals...)
	})
	resp, err := eng.RunBacktest(context.Background(), backtest.BacktestRequest{
		Strategy:       backtestStrat.Name(),
		StockPool:      symbols,
		StartDate:      "2024-01-02",
		EndDate:        "2024-01-31",
		InitialCapital: 1_000_000,
		RiskFreeRate:   0.03,
	})
	if err != nil || resp.Status != "completed" {
		t.Fatalf("回测失败：%v / status=%s", err, resp.Status)
	}

	// paper 侧故意把 end 提前一天 → 少喂最后一个交易日 → 信号必然缺失。
	paperStrat := newReconcileProbe("k5_reconcile_probe_paper_b")
	trader := live.NewMockTrader(live.MockTraderConfig{InitialCash: 1_000_000, SlippageModel: "fixed"}, zerolog.Nop())
	var paperSignals []domain.Signal
	sess, err := live.NewPaperSession(live.PaperSessionConfig{
		Provider: prov,
		Clock:    clock.NewVirtualClock(time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)),
		Trader:   trader,
		Logger:   zerolog.Nop(),
		SignalCollector: func(_ time.Time, signals []domain.Signal) {
			paperSignals = append(paperSignals, signals...)
		},
	})
	if err != nil {
		t.Fatalf("构造 paper session 失败：%v", err)
	}
	truncatedEnd := time.Date(2024, 1, 30, 0, 0, 0, 0, time.UTC)
	if _, err := sess.Run(context.Background(), symbols,
		time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC), truncatedEnd, paperStrat); err != nil {
		t.Fatalf("paper 回放失败：%v", err)
	}

	diff := live.ReconcileSignals(paperSignals, backtestSignals, live.DefaultSignalReconcileConfig())
	if len(diff) == 0 {
		t.Fatal("paper 截断窗口后信号应与回测不同，但 ReconcileSignals 报零差异 —— 对账器失效")
	}
	t.Logf("破坏验证通过：截断窗口导致 %d 条信号差异被对账器点名", len(diff))
}

func diffStrings(diff []live.SignalDiscrepancy) string {
	s := ""
	for i, d := range diff {
		if i > 0 {
			s += "\n"
		}
		s += d.String()
	}
	return s
}
