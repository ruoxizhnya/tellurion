package backtest

// QT-21 的验收腿：信号顺序规范化 + 重复语义。
//
// 三条要证的事：
//  1. 同一批信号无论来源顺序如何（map 遍历 / 并发），规范化后逐位一致；
//  2. 重复语义 = close 优先 + 同向 strength 求和 clamp 到 [0,1]，
//     不同方向 / 不同 bar 不合并；
//  3. 引擎执行路径真的调用了它（观测钩子看到的是规范化后的序列），
//     且 20 次同输入逐位一致。

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"tellurion/pkg/strategy"
	"tellurion/pkg/strategy/examples"
)

var qt21Day = time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)

// qt21BaseBatch 是一组「已含重复」的信号，用来验证排序与合并的组合行为。
func qt21BaseBatch() []domain.Signal {
	return []domain.Signal{
		{Symbol: "600003.SH", Date: qt21Day, Direction: domain.DirectionLong, Strength: 0.30, CompositeScore: 0.30},
		{Symbol: "600001.SH", Date: qt21Day, Direction: domain.DirectionLong, Strength: 0.20, CompositeScore: 0.20},
		{Symbol: "600002.SH", Date: qt21Day, Direction: domain.DirectionShort, Strength: 0.90, CompositeScore: 0.95},
		{Symbol: "600001.SH", Date: qt21Day, Direction: domain.DirectionLong, Strength: 0.70, CompositeScore: 0.75},
		{Symbol: "600002.SH", Date: qt21Day, Direction: domain.DirectionLong, Strength: 0.90, CompositeScore: 0.95},
		{Symbol: "600004.SH", Date: qt21Day, Direction: domain.DirectionClose, Strength: 0.40, CompositeScore: 0.40},
		{Symbol: "600004.SH", Date: qt21Day, Direction: domain.DirectionLong, Strength: 0.80, CompositeScore: 0.80},
	}
}

// TestNormalizeSignalSequence_TwentyShufflesIdentical 是「同输入 20 次信号序
// 完全一致」的直接证据：同一批信号用 20 个不同随机种子打乱，规范化输出
// 必须逐位相同。
func TestNormalizeSignalSequence_TwentyShufflesIdentical(t *testing.T) {
	base := qt21BaseBatch()

	want := normalizeSignalSequence(base, nil)
	require.NotEmpty(t, want)

	for seed := int64(0); seed < 20; seed++ {
		in := append([]domain.Signal(nil), base...)
		rng := rand.New(rand.NewSource(seed))
		rng.Shuffle(len(in), func(i, j int) { in[i], in[j] = in[j], in[i] })

		got := normalizeSignalSequence(in, nil)
		require.Equal(t, qt21Fingerprints(want), qt21Fingerprints(got),
			"seed=%d 的乱序输入规范化后与基准不一致", seed)

		// 排序口径自证：按冻结的四键逐对检查。
		for i := 1; i < len(got); i++ {
			if signalOrderLess(got[i], got[i-1]) {
				t.Fatalf("输出未按 (symbol, direction, strength desc, composite desc) 排序：%s 在 %s 之后",
					qt21Fingerprint(got[i]), qt21Fingerprint(got[i-1]))
			}
		}
	}
}

func TestMergeSameBarSignals_CloseWins(t *testing.T) {
	diag := newFillRunDiag()
	in := []domain.Signal{
		{Symbol: "A", Date: qt21Day, Direction: domain.DirectionLong, Strength: 0.80, CompositeScore: 0.80, LimitPrice: 99},
		{Symbol: "A", Date: qt21Day, Direction: domain.DirectionClose, Strength: 0.30, CompositeScore: 0.40, LimitPrice: 10},
	}
	out := normalizeSignalSequence(in, diag)

	require.Len(t, out, 1)
	assert.Equal(t, domain.DirectionClose, out[0].Direction, "close 必须优先于 long")
	assert.Equal(t, 0.30, out[0].Strength, "只剩一条 close 时保留原 strength")
	assert.Equal(t, 10.0, out[0].LimitPrice, "合并保留 close 那条的下单参数")
	assert.Equal(t, 1, diag.duplicateSignalsMerged)
}

func TestMergeSameBarSignals_SameDirectionSumClamped(t *testing.T) {
	diag := newFillRunDiag()
	in := []domain.Signal{
		{Symbol: "A", Date: qt21Day, Direction: domain.DirectionLong, Strength: 0.60, CompositeScore: 0.55},
		{Symbol: "A", Date: qt21Day, Direction: domain.DirectionLong, Strength: 0.70, CompositeScore: 0.90},
	}
	out := normalizeSignalSequence(in, diag)

	require.Len(t, out, 1)
	assert.Equal(t, domain.DirectionLong, out[0].Direction)
	assert.Equal(t, 1.0, out[0].Strength, "0.6+0.7 必须 clamp 到 1.0")
	assert.Equal(t, 0.90, out[0].CompositeScore, "CompositeScore 取组内最大")
	assert.Equal(t, 1, diag.duplicateSignalsMerged)
}

func TestMergeSameBarSignals_OppositeDirectionsNotMerged(t *testing.T) {
	diag := newFillRunDiag()
	in := []domain.Signal{
		{Symbol: "A", Date: qt21Day, Direction: domain.DirectionLong, Strength: 0.60},
		{Symbol: "A", Date: qt21Day, Direction: domain.DirectionShort, Strength: 0.50},
	}
	out := normalizeSignalSequence(in, diag)

	require.Len(t, out, 2, "long 与 short 是两条相反意图，不许合并")
	assert.Equal(t, 0, diag.duplicateSignalsMerged)
}

func TestMergeSameBarSignals_DifferentBarsNotMerged(t *testing.T) {
	diag := newFillRunDiag()
	in := []domain.Signal{
		{Symbol: "A", Date: qt21Day, Direction: domain.DirectionLong, Strength: 0.60},
		{Symbol: "A", Date: qt21Day.AddDate(0, 0, 1), Direction: domain.DirectionLong, Strength: 0.50},
	}
	out := normalizeSignalSequence(in, diag)

	require.Len(t, out, 2, "不同 bar 的信号绝不合并")
	assert.Equal(t, 0, diag.duplicateSignalsMerged)
}

// ─── 引擎执行路径：20 次同输入逐位一致 ─────────────────────────────

const qt21ProbeName = "qt21-map-order-probe"

// qt21MapOrderProbe 故意从 map 遍历发信号（Go 的 map 顺序每次不同），
// 并在同一 bar 上制造重复 —— 正是 A8/A12 的形态。
type qt21MapOrderProbe struct {
	*strategy.BaseStrategy
}

func (s *qt21MapOrderProbe) Name() string { return qt21ProbeName }

func (s *qt21MapOrderProbe) Parameters() []strategy.Parameter { return nil }

func (s *qt21MapOrderProbe) Configure(map[string]any) error { return nil }

func (s *qt21MapOrderProbe) Weight(strategy.Signal, float64) float64 { return 0.02 }

func (s *qt21MapOrderProbe) GenerateSignals(_ context.Context, bars map[string][]domain.OHLCV, _ *domain.Portfolio) ([]strategy.Signal, error) {
	if len(bars) < 3 {
		return nil, nil
	}
	var out []strategy.Signal
	for sym, series := range bars { // ← 非确定顺序：本单要消灭的就是它
		last := series[len(series)-1]
		out = append(out,
			strategy.Signal{Symbol: sym, Action: "buy", Direction: domain.DirectionLong, Strength: 0.3, Date: last.Date},
			strategy.Signal{Symbol: sym, Action: "buy", Direction: domain.DirectionLong, Strength: 0.4, Date: last.Date},
			strategy.Signal{Symbol: sym, Action: "sell", Direction: domain.DirectionClose, Strength: 0.2, Date: last.Date},
		)
	}
	return out, nil
}

var qt21ProbeOnce sync.Once

func registerQT21Probe(t *testing.T) {
	t.Helper()
	qt21ProbeOnce.Do(func() {
		require.NoError(t, strategy.GlobalRegisterFactory(func() strategy.Strategy {
			return &qt21MapOrderProbe{BaseStrategy: strategy.NewBaseStrategy(qt21ProbeName, "QT-21 顺序探针")}
		}))
	})
}

func TestEngineSignalSequence_TwentyRunsIdentical(t *testing.T) {
	registerQT21Probe(t)
	const days = 10
	symbols := []string{"600000.SH", "600001.SH", "600002.SH"}

	var baseline []string
	for run := 0; run < 20; run++ {
		prov := provenanceProvider(t, symbols, days)
		eng, err := NewEngine(provenanceViper(42, 0.0003, 0.001, 0.001), prov, zerolog.Nop())
		require.NoError(t, err)
		applyOptions(eng, provenanceBaseOptions(t))

		var observed []string
		eng.SetSignalObserver(func(date time.Time, signals []domain.Signal) {
			observed = append(observed, date.Format("2006-01-02")+">"+strings.Join(qt21Fingerprints(signals), ","))
		})

		_, err = eng.RunBacktest(context.Background(), BacktestRequest{
			Strategy:       qt21ProbeName,
			StockPool:      symbols,
			StartDate:      provenanceTestStart.Format("2006-01-02"),
			EndDate:        provenanceTestStart.AddDate(0, 0, days-1).Format("2006-01-02"),
			InitialCapital: 1_000_000,
			RiskFreeRate:   0.03,
		})
		require.NoError(t, err)

		if run == 0 {
			baseline = observed
			require.NotEmpty(t, baseline)
			// close 优先：每个 symbol 的 3 条重复信号合并成 1 条 close。
			for _, line := range baseline {
				parts := strings.SplitN(line, ">", 2)
				if parts[1] == "" {
					continue
				}
				require.Len(t, strings.Split(parts[1], ","), len(symbols),
					"每个 symbol 应合并成一条信号：%s", line)
				require.Contains(t, parts[1], ":close:", "line=%s", line)
			}
			continue
		}
		require.Equal(t, baseline, observed, "第 %d 次运行的规范化信号序列与基准不一致", run)
	}
}

// TestValueMomentum_TwentyRunsBitwiseIdentical 对齐 QT-21 ⑤ 的原文验收。
// 如实登记：`value_momentum.GenerateSignals` 今天是 stub（返回空信号集），
// 所以这条腿只能证明「整轮回测的产出逐位一致」，不能证明该策略的信号
// 顺序；真正的顺序证据在 qt21MapOrderProbe 那条腿上。
func TestValueMomentum_TwentyRunsBitwiseIdentical(t *testing.T) {
	qmOnce.Do(func() {
		require.NoError(t, strategy.GlobalRegisterFactory(func() strategy.Strategy {
			return examples.NewValueMomentumStrategy()
		}))
	})

	const days = 10
	symbols := []string{"600000.SH", "600001.SH"}

	var baseline *BacktestResponse
	for run := 0; run < 20; run++ {
		prov := provenanceProvider(t, symbols, days)
		eng, err := NewEngine(provenanceViper(42, 0.0003, 0.001, 0.001), prov, zerolog.Nop())
		require.NoError(t, err)
		applyOptions(eng, provenanceBaseOptions(t))

		resp, err := eng.RunBacktest(context.Background(), BacktestRequest{
			Strategy:       "value_momentum",
			StockPool:      symbols,
			StartDate:      provenanceTestStart.Format("2006-01-02"),
			EndDate:        provenanceTestStart.AddDate(0, 0, days-1).Format("2006-01-02"),
			InitialCapital: 1_000_000,
			RiskFreeRate:   0.03,
		})
		require.NoError(t, err)
		if baseline == nil {
			baseline = resp
			continue
		}
		assert.Equal(t, baseline.Status, resp.Status)
		assert.Equal(t, baseline.TotalReturn, resp.TotalReturn)
		assert.Equal(t, baseline.TotalTrades, resp.TotalTrades)
		require.Len(t, resp.PortfolioValues, len(baseline.PortfolioValues))
		for i := range resp.PortfolioValues {
			require.Equal(t, baseline.PortfolioValues[i].TotalValue, resp.PortfolioValues[i].TotalValue,
				"第 %d 次运行净值第 %d 点不一致", run, i)
		}
	}
}

var qmOnce sync.Once

// ─── 旧序 vs 新序差异报告（归档到 docs/test-cases/QT21_SIGNAL_ORDER_DIFF.md）──

// TestQT21_OldVsNewOrderDiffReport 用一份固定的「旧序」批次（模拟 map 遍历
// 出来的顺序）与规范化后的新序对比，打印可归档的差异计数。
func TestQT21_OldVsNewOrderDiffReport(t *testing.T) {
	oldOrder := qt21BaseBatch()
	newOrder := normalizeSignalSequence(oldOrder, nil)

	moved := 0
	n := len(oldOrder)
	if len(newOrder) < n {
		n = len(newOrder)
	}
	for i := 0; i < n; i++ {
		if qt21Fingerprint(oldOrder[i]) != qt21Fingerprint(newOrder[i]) {
			moved++
		}
	}

	diag := newFillRunDiag()
	_ = normalizeSignalSequence(oldOrder, diag)

	report := fmt.Sprintf(
		"input=%d output=%d duplicates_merged=%d positions_changed=%d\nold=%v\nnew=%v",
		len(oldOrder), len(newOrder), diag.duplicateSignalsMerged, moved,
		qt21Fingerprints(oldOrder), qt21Fingerprints(newOrder))
	t.Logf("QT-21 old vs new order:\n%s", report)

	assert.Equal(t, 2, diag.duplicateSignalsMerged, "夹具应合并掉 2 条（600001 同向、600004 close+long）")
	assert.Less(t, len(newOrder), len(oldOrder))
}

func qt21Fingerprints(signals []domain.Signal) []string {
	out := make([]string, len(signals))
	for i, sig := range signals {
		out[i] = qt21Fingerprint(sig)
	}
	return out
}

func qt21Fingerprint(sig domain.Signal) string {
	return fmt.Sprintf("%s:%s:%.4f:%.4f", sig.Symbol, sig.Direction, sig.Strength, sig.CompositeScore)
}
