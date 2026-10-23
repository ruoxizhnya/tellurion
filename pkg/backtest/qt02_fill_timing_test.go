package backtest

// QT-02 · 成交时点口径落到引擎日循环的护栏。
//
// 三个层次的证据，缺一层就留缺口：
//  1. 取价投影（openPricesForSignals）—— 纯函数，钉住「用成交日开盘价、拿不到
//     就拒单并计数，绝不退回收盘价」；
//  2. 两条口径的**整轮回测**行为 —— 同一份行情、同一个策略，same_close 在决策日
//     按收盘成交，next_open 在次日按开盘成交。只有这一层能证明「口径真的改变了
//     成交」，第 1 层证明的只是取价函数自己没写错；
//  3. 引擎入口的必填裁决 —— 绕过请求解析点（自己构造 params）的路径拿到空口径
//     必须当场失败，而不是回落默认。
//
// 夹具的三条刻意：
//   - 逐日涨幅压在 10% 以内：超过限幅会让 processStockJob 把成交价换成涨停价
//     （REV-09 既有行为），那时断言测的是限价口径而不是成交时点；
//   - 开盘价 ≠ 收盘价：两者相等就根本区分不出两条口径；
//   - 滑点设 0：成交价等于取到的价，可以精确断言而不是给一个容差带。
//
// 风控必须是**进程内** RiskManager：没有它时 calculatePosition 会去打外部
// risk-service 的 HTTP（一次连接超时就 7 秒），失败后 size=0 ⇒ 一单不成交 ⇒
// 两条口径「都没有成交」，测试会绿得毫无意义。

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"tellurion/pkg/marketdata"
	"tellurion/pkg/risk"
	"tellurion/pkg/strategy"
)

const (
	qt02Symbol      = "600000.SH"
	qt02BarCount    = 30
	qt02DecisionIdx = 20 // 决策日：same_close 在这天成交，next_open 在下一天
	qt02LastIdx     = qt02BarCount - 1
)

var qt02Start = mustDate("2024-01-02")

// qt02BarAt 是第 i 天的确定性 bar：开 10 + 0.1i，收 +0.05。
// 逐日变化 < 1%，不会撞涨跌停。
func qt02BarAt(i int) domain.OHLCV {
	open := 10 + 0.1*float64(i)
	close := open + 0.05
	return domain.OHLCV{
		Symbol:   qt02Symbol,
		Date:     qt02Day(i),
		Open:     open,
		High:     close + 0.05,
		Low:      open - 0.05,
		Close:    close,
		Volume:   1_000_000,
		Turnover: close * 1_000_000,
	}
}

func qt02Day(i int) time.Time { return qt02Start.AddDate(0, 0, i) }

// qt02CloseAt / qt02OpenAt 把「期望成交价」写回它来自哪根 bar，让断言读起来
// 是口径而不是魔数（数值本身在下面各测试里另有字面量核对）。
func qt02CloseAt(i int) float64 { return qt02BarAt(i).Close }
func qt02OpenAt(i int) float64  { return qt02BarAt(i).Open }

func qt02Fixture() marketdata.Provider {
	prov := marketdata.NewInMemoryProvider()
	bars := make([]domain.OHLCV, 0, qt02BarCount)
	days := make([]time.Time, 0, qt02BarCount)
	for i := 0; i < qt02BarCount; i++ {
		bars = append(bars, qt02BarAt(i))
		days = append(days, qt02Day(i))
	}
	prov.LoadOHLCV(qt02Symbol, bars)
	prov.SetTradingDays(days)
	prov.LoadStocks([]domain.Stock{{Symbol: qt02Symbol, Name: "QT02 测试票", ListDate: mustDate("2020-01-02")}})
	return prov
}

// qt02RiskManager 装进程内风控（没有它 calculatePosition 会打外部 HTTP）。
func qt02RiskManager(t *testing.T) *risk.RiskManager {
	t.Helper()
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
		FastMAPeriod:        10,
		SlowMAPeriod:        20,
		RegimeVolLookback:   20,
	}, zerolog.Nop())
	require.NoError(t, err)
	return rm
}

// qt02Engine 造一台「跑得动、也不碰网络」的引擎：滑点 0 + 进程内风控。
func qt02Engine(t *testing.T) *Engine {
	t.Helper()
	eng := rev09NewEngine(t, qt02Fixture())
	eng.SetRiskManager(qt02RiskManager(t))
	return eng
}

// qt02Probe 是无状态探针策略：只在 wantIdx 那一天吐一条 Long。
//
// 「只吐一天」是刻意的：每天重发的策略（momentum 那种）在两条口径下都有成交，
// 看不出成交日被平移；只有一天的信号才能把「决策日 ≠ 成交日」单独量出来。
// Date 必须写实，因为引擎会丢掉 sig.Date 晚于回放日的信号（防前视）。
type qt02Probe struct {
	name    string
	wantIdx int
	fired   bool
}

func (p *qt02Probe) Name() string                            { return p.name }
func (p *qt02Probe) Description() string                     { return "QT-02 成交时点探针" }
func (p *qt02Probe) Parameters() []strategy.Parameter        { return nil }
func (p *qt02Probe) Configure(map[string]any) error          { return nil }
func (p *qt02Probe) Weight(strategy.Signal, float64) float64 { return 0.05 }
func (p *qt02Probe) Cleanup()                                {}

func (p *qt02Probe) GenerateSignals(_ context.Context, bars map[string][]domain.OHLCV, _ *domain.Portfolio) ([]strategy.Signal, error) {
	series := bars[qt02Symbol]
	if len(series) == 0 {
		return nil, nil
	}
	last := series[len(series)-1]
	if !last.Date.Equal(qt02Day(p.wantIdx)) || p.fired {
		return nil, nil
	}
	p.fired = true
	return []strategy.Signal{{
		Symbol:    qt02Symbol,
		Action:    "buy",
		Direction: domain.DirectionLong,
		Strength:  1.0,
		Date:      last.Date,
	}}, nil
}

// qt02FillFactor 是成交价的滑点系数。
//
// 测试里 backtest.slippage_rate 设 0，但 ExecutionService 拿的是**原始配置值**
// （engine.go 的 explicitSlippageRate），零值会落到它自己的默认 fixed 滑点
// 0.001（fees.FixedSlippageRate）。本单不动滑点，所以断言按「基准价 ×1.001」
// 写：基准价才是被测对象（开盘 vs 收盘），系数是既有的成本核。
// 哪天这个默认值变了，这几条断言会当场红 —— 那是应该知道的变更，不是脆弱。
const qt02FillFactor = 1 + 0.001

// qt02OpeningTrades 只看**建仓**笔。
//
// 回测收尾会强平所有持仓（forceCloseAllPositions ⇒ 一条 DirectionClose 的
// trade），它按最后一天的持仓盯市价成交，与本次口径无关。把强平算进「成交
// 笔数」会让断言变成「2 笔 vs 0 笔」这种看不出机制的形状；建仓笔数才是
// 口径作用的地方。
func qt02OpeningTrades(resp *BacktestResponse) []domain.Trade {
	var out []domain.Trade
	for _, tr := range resp.Trades {
		if tr.Direction == domain.DirectionLong || tr.Direction == domain.DirectionShort {
			out = append(out, tr)
		}
	}
	return out
}

// qt02Run 跑一轮回测并断言「这笔信号确实驱动了一笔建仓委托」——
// wantOpens 由调用方给，因为「最后一天没有明天」那种形态本来就该是 0 笔。
func qt02Run(t *testing.T, name, fillTiming string, wantIdx, wantOpens int) *BacktestResponse {
	t.Helper()
	eng := qt02Engine(t)
	probe := &qt02Probe{name: name, wantIdx: wantIdx}
	require.NoError(t, strategy.GlobalRegisterFactory(func() strategy.Strategy { return probe }), "注册探针策略失败")

	resp, err := eng.RunBacktest(context.Background(), BacktestRequest{
		Strategy:       name,
		StockPool:      []string{qt02Symbol},
		StartDate:      qt02Day(0).Format("2006-01-02"),
		EndDate:        qt02Day(qt02LastIdx).Format("2006-01-02"),
		InitialCapital: 1_000_000,
		FillTiming:     fillTiming,
	})
	require.NoError(t, err, "回测不应失败")

	requires := require.New(t)
	requires.Len(qt02OpeningTrades(resp), wantOpens,
		"建仓笔数不符（status=%s invalid=%v trades=%+v）—— 先确认这一笔信号有没有被撮合到",
		resp.Status, resp.InvalidReasons, resp.Trades)
	return resp
}

// ── 第 1 层：取价投影 ─────────────────────────────────────────────

func TestQT02_OpenPrices_FillsAtOpenNotClose(t *testing.T) {
	mdc := map[string][]domain.OHLCV{
		qt02Symbol: {qt02BarAt(qt02DecisionIdx), qt02BarAt(qt02DecisionIdx + 1)},
	}
	diag := newFillRunDiag()

	got := openPricesForSignals([]domain.Signal{{Symbol: qt02Symbol, Direction: domain.DirectionLong}}, mdc, diag)

	assert.InDelta(t, qt02OpenAt(qt02DecisionIdx+1), got[qt02Symbol], 1e-9,
		"next_open 的可成交价 = 成交日末根 bar 的开盘价")
	assert.NotEqual(t, qt02CloseAt(qt02DecisionIdx), got[qt02Symbol], "不得是决策日的收盘价")
	assert.Zero(t, diag.noOpenPriceSkips, "拿得到开盘价时不该计数")
}

func TestQT02_OpenPrices_MissingOrDirtyBarRejectsInsteadOfFallingBackToClose(t *testing.T) {
	// 三种「成交日没有可用开盘价」的形状。共同裁决：拒单 + 计数，
	// **不退回收盘价成交** —— 同一份留痕里混两种口径比少一笔成交坏得多。
	cases := []struct {
		name string
		mdc  map[string][]domain.OHLCV
	}{
		{"当日无 bar（停牌股已被 REV-09 摘出）", map[string][]domain.OHLCV{}},
		{"bar 序列为空", map[string][]domain.OHLCV{qt02Symbol: {}}},
		// 脏 bar：有当日 bar 但开盘价缺失（源给 0 / 缺字段）。收盘价照常，
		// 所以「退回收盘价」在这里随手就能写成 —— 这条用例就是拦住它。
		{"开盘价缺失（脏 bar）", map[string][]domain.OHLCV{
			qt02Symbol: {{Symbol: qt02Symbol, Date: qt02Day(1), Open: 0, Close: 11.20, Volume: 1_000_000}},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diag := newFillRunDiag()
			got := openPricesForSignals([]domain.Signal{{Symbol: qt02Symbol, Direction: domain.DirectionLong}}, tc.mdc, diag)

			assert.LessOrEqual(t, got[qt02Symbol], float64(0), "被拒的 symbol 不得拿到任何可成交价（尤其不能是收盘价）")
			assert.Equal(t, 1, diag.noOpenPriceSkips, "被拒的委托必须计数，否则这份留痕看不出少成交了几笔")
		})
	}
}

func TestQT02_OpenPrices_CountsOncePerSymbolNotPerSignal(t *testing.T) {
	// 同一 symbol 同日两条信号（重复信号语义属 QT-04）：价只取一次、拒单也只
	// 该计一次，否则诊断里的「拒了几笔」会被重复信号放大。
	diag := newFillRunDiag()
	got := openPricesForSignals([]domain.Signal{
		{Symbol: "GONE.SH", Direction: domain.DirectionLong},
		{Symbol: "GONE.SH", Direction: domain.DirectionClose},
	}, map[string][]domain.OHLCV{}, diag)

	assert.Len(t, got, 1, "同 symbol 只出一个键")
	assert.Equal(t, 1, diag.noOpenPriceSkips, "重复信号不得把一次拒单计成两次")
}

// ── 第 2 层：整轮回测的口径行为 ───────────────────────────────────

func TestQT02_SameClose_FillsOnDecisionDayAtClose(t *testing.T) {
	// 既有行为（§8.1 兼容行）：T 日收盘决策 + T 日收盘成交。
	resp := qt02Run(t, "qt02-same-close", "same_close", qt02DecisionIdx, 1)

	trade := qt02OpeningTrades(resp)[0]
	assert.WithinDuration(t, qt02Day(qt02DecisionIdx), trade.Timestamp, time.Hour, "same_close 的成交日 = 决策日")
	assert.InDelta(t, 12.05*qt02FillFactor, trade.Price, 1e-9,
		"成交价基准 = 决策日收盘 12.05（10 + 0.1×20 + 0.05），再乘既有 fixed 滑点")

	assert.Equal(t, "same_close", resp.FillTiming, "结果必须自带实际生效的口径")
	assert.Equal(t, []string{"same_close_fill"}, resp.KnownBias, "收盘成交必须自标已知偏差（§8.1 兼容行）")
}

func TestQT02_NextOpen_FillsOnNextDayAtOpen(t *testing.T) {
	// 终态口径（§8.1）：T 日收盘决策 → T+1 开盘成交。本单的命门：它证明
	// 「口径」真的改变了成交，而不只是在响应里多写一个字段。
	resp := qt02Run(t, "qt02-next-open", "next_open", qt02DecisionIdx, 1)

	trade := qt02OpeningTrades(resp)[0]
	assert.WithinDuration(t, qt02Day(qt02DecisionIdx+1), trade.Timestamp, time.Hour,
		"next_open 的成交日必须是决策日的下一个交易日，不是决策日当天")
	assert.InDelta(t, 12.10*qt02FillFactor, trade.Price, 1e-9,
		"成交价基准 = 成交日开盘 12.10（10 + 0.1×21）—— 这一条同时拦住两件事："+
			"退化回收盘成交、以及被 ExecutionService 用 DayBar.Close 覆盖掉引擎选定的价")
	assert.NotEqual(t, qt02CloseAt(qt02DecisionIdx)*qt02FillFactor, trade.Price,
		"next_open 结果里不得混进一笔收盘成交")

	assert.Equal(t, "next_open", resp.FillTiming)
	assert.Empty(t, resp.KnownBias, "next_open 是终态口径，不该标 same_close_fill")
}

func TestQT02_CaliberPair_DifferenceIsOneBar(t *testing.T) {
	// 两条口径跑同一份行情，差异必须**只**出现在成交的那一天、那一个价上。
	// 这一条是差异报告（docs/test-cases/QT02_FILL_TIMING_REPORT.md）在单测
	// 尺度上的同构断言：报告解释宏观差异，这条钉住机制。
	same := qt02Run(t, "qt02-pair-same", "same_close", qt02DecisionIdx, 1)
	next := qt02Run(t, "qt02-pair-next", "next_open", qt02DecisionIdx, 1)

	sameOpen, nextOpen := qt02OpeningTrades(same)[0], qt02OpeningTrades(next)[0]
	assert.True(t, nextOpen.Timestamp.After(sameOpen.Timestamp),
		"next_open 的成交时刻必须晚于 same_close（平移一个 bar）")
	assert.NotEqual(t, sameOpen.Price, nextOpen.Price,
		"两口径成交价必须不同，否则这份夹具根本没在测成交时点")
	assert.Equal(t, sameOpen.Quantity, nextOpen.Quantity,
		"仓位口径不因成交时点改变（QT-02 ④：不动仓位/撮合内部）")
	assert.NotEqual(t, same.FillTiming, next.FillTiming)
}

func TestQT02_NextOpen_LastDaySignalHasNoFillDay(t *testing.T) {
	// 区间最后一天收盘决策的信号没有次日可成交 ⇒ 不成交（next_open 的语义
	// 后果，不是缺陷）；same_close 同一份行情必须成交。
	for _, tc := range []struct {
		name       string
		fill       string
		wantTrades int
	}{
		{"same_close 当日成交", "same_close", 1},
		{"next_open 没有明天", "next_open", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := qt02Run(t, "qt02-edge-"+tc.fill, tc.fill, qt02LastIdx, tc.wantTrades)
			assert.Equal(t, tc.fill, resp.FillTiming, "即使一笔不成交，结果也要如实记下口径")
		})
	}
}

func TestQT02_DiagRecordsUnfilledPendingAtRunEnd(t *testing.T) {
	// run 级诊断不外露在 ResultSpec 里，日志与单测读它 —— 这里给计数一条
	// 直接证据（跨天累加，收尾那条「有多少信号没有明天」才不会说谎）。
	diag := newFillRunDiag()
	diag.recordPendingAtRunEnd([]domain.Signal{{Symbol: "A.SH"}, {Symbol: "B.SH"}})
	diag.recordPendingAtRunEnd([]domain.Signal{{Symbol: "C.SH"}})
	assert.Equal(t, 3, diag.pendingAtRunEnd)
}

// ── 第 3 层：引擎入口的必填裁决 ───────────────────────────────────

func TestQT02_EngineRejectsUndeclaredOrUnknownCaliber(t *testing.T) {
	// 绕过请求解析点（自己构造 params）的两条路：空值 = 口径未知、野值 =
	// 口径不认识。两者都必须在**任何成交发生之前**失败，绝不回落默认。
	for _, tc := range []struct {
		name    string
		raw     string
		wantErr error
	}{
		{"空声明", "", ErrFillTimingMissing},
		{"野声明", "close", ErrFillTimingUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng := qt02Engine(t)
			state := &BacktestState{
				ID: "qt02-undeclared",
				Params: domain.BacktestParams{
					StrategyName: "qt02-none",
					StockPool:    []string{qt02Symbol},
					StartDate:    qt02Day(0),
					EndDate:      qt02Day(qt02LastIdx),
					FillTiming:   tc.raw, // 刻意不给口径
				},
				Tracker:         NewTracker(1_000_000, 0.0003, 0, defaultTradingConfig(), zerolog.Nop()),
				TargetPositions: map[string]*domain.TargetPosition{},
			}

			_, err := eng.runBacktestInternal(context.Background(), state)
			require.Error(t, err, "口径未知却跑完了回测 —— 引擎在猜")
			assert.ErrorIs(t, err, tc.wantErr)
			assert.Empty(t, state.Tracker.GetTrades(), "拒绝必须发生在任何成交之前")
		})
	}
}

func TestQT02_RunBacktestDefaultsAtParsePointNotInTheEngine(t *testing.T) {
	// 请求没声明 ⇒ 走 DefaultFillTiming（唯一允许的默认点 = 请求解析点），
	// 且响应把生效值写出来。这一条守的是「默认发生在解析点，不在引擎里」。
	resp := qt02Run(t, "qt02-unspecified", "", qt02DecisionIdx, 1)

	assert.Equal(t, DefaultFillTiming.String(), resp.FillTiming,
		"未声明的请求应落到 DefaultFillTiming，而不是空值或引擎自己猜的值")
	assert.Equal(t, DefaultFillTiming.KnownBiases(), resp.KnownBias)
}
