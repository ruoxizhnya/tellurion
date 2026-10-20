package backtest

// REV-06（裁决 a，2026-10-16 主任）：qfq 数据源下除权除息不双重计算。
//
// 背景：引擎消费的日线是前复权序列（ohlcv_daily_qfq，除权跳空已按构造平滑），
// 而旧代码每日仍对持仓执行 ProcessDividend（贷现金）/ ProcessSplit（加股数）
// —— 分红贷两次、拆股市值翻倍，回测收益被系统性高估（普通 A 股年化 2-3pct
// 股息，大比例送股个股可达翻倍级）。
//
// 本文件是 (a) 落地的护栏：注入 qfq 数据源 + 除权事件，断言除权日前后
// positionsValue + cash 不变；并附「关开改账」两跑的虚高幅度量化（修复效果
// 的可执行证据）。
//
// 口径说明：GetTotalValue = 初始现金 + 持仓市值（扣费后），所以护栏断言
// 的是**增量**（除权日前后总价值一分不动），量化用的是「单日虚高收益率」
// = 旧口径除权日增量 / 除权日前持仓市值。

import (
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"tellurion/pkg/backtest/contracts"
	"tellurion/pkg/backtest/tracker"
	"tellurion/pkg/domain"
)

// rev06Symbol / rev06QfqPrice 是护栏 fixture 的独享标的与 qfq 价。
// qfq 的关键性质：除权日价格**连续**（10 送 5 + 每股派 0.1 的除权跳空已
// 被摊进历史价），所以除权日前后 qfq 价都是 10.0。
const (
	rev06Symbol   = "600000.SH"
	rev06QfqPrice = 10.0
)

var (
	rev06Day1 = time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC) // 建仓日
	rev06Day2 = time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC) // 除权除息日
)

// rev06QfqPrices 是除权日前后的 qfq 价格表（连续，不跌）。
func rev06QfqPrices() map[string]float64 {
	return map[string]float64{rev06Symbol: rev06QfqPrice}
}

// rev06Tracker 建一个「1000 股 @ qfq 10.0」的 tracker（除权日前一夜的状态）。
func rev06Tracker(t *testing.T) *tracker.Tracker {
	t.Helper()
	tr := tracker.NewTracker(1_000_000, 0.0003, 0.001, contracts.DefaultTradingConfig(), zerolog.Nop())
	if _, err := tr.ExecuteTrade(rev06Symbol, domain.DirectionLong, 1000, rev06QfqPrice, rev06Day1, nil); err != nil {
		t.Fatalf("fixture 建仓失败: %v", err)
	}
	return tr
}

// rev06CorporateActions 构造除权日的分红 + 拆股事件（10 送 5，每股派 0.1）。
func rev06CorporateActions(withSplit, withDividend bool) *corporateActions {
	ca := &corporateActions{
		dividendsByDate: make(map[time.Time][]*domain.Dividend),
		splitsByDate:    make(map[time.Time][]*domain.Split),
	}
	if withDividend {
		ca.dividendsByDate[rev06Day2] = append(ca.dividendsByDate[rev06Day2], &domain.Dividend{
			Symbol:  rev06Symbol,
			DivAmt:  0.1,
			PayDate: rev06Day2,
		})
	}
	if withSplit {
		ca.splitsByDate[rev06Day2] = append(ca.splitsByDate[rev06Day2], &domain.Split{
			Symbol:       rev06Symbol,
			TradeDate:    rev06Day2,
			StkDivRatio:  0.5, // 10 送 5（每 10 股送 5 股）
			CashDivRatio: 0,
		})
	}
	return ca
}

// TestProcessCorporateActions_QfqSplitAndDividend_TotalValueUnchanged 是
// REV-06 的核心护栏：qfq 数据源 + 拆股 + 分红，除权日前后
// positionsValue + cash **一分不动**。旧口径在此 fixture 上会把总价值抬高
// 5,100（单日 +51%，见量化测试）。
func TestProcessCorporateActions_QfqSplitAndDividend_TotalValueUnchanged(t *testing.T) {
	tr := rev06Tracker(t)
	state := &BacktestState{ID: "rev06-guard", Tracker: tr}
	eng := &Engine{}

	before := tr.GetTotalValue(rev06QfqPrices())
	cashBefore := tr.GetCash()
	posBefore, ok := tr.GetPosition(rev06Symbol)
	require.True(t, ok)
	require.InDelta(t, 1000.0, posBefore.Quantity, 1e-9, "fixture 前置：1000 股")

	splits := eng.processCorporateActions(state, rev06CorporateActions(true, true), rev06Day2, zerolog.Nop())

	// 持仓与现金一分不动。
	pos, ok := tr.GetPosition(rev06Symbol)
	require.True(t, ok)
	assert.InDelta(t, 1000.0, pos.Quantity, 1e-9, "qfq 口径下拆股不得改股数")
	assert.InDelta(t, cashBefore, tr.GetCash(), 1e-9, "qfq 口径下分红不得贷现金")
	after := tr.GetTotalValue(rev06QfqPrices())
	assert.InDelta(t, before, after, 1e-9, "除权日前后总价值必须连续（qfq 已平滑除权跳空）")

	// K18 语义保留：拆股 symbol 仍进返回列表（父单取消照旧）。
	assert.Equal(t, []string{rev06Symbol}, splits)
}

// TestProcessCorporateActions_QfqDividendOnly_TotalValueUnchanged 是分红
// 单腿护栏（更常见的场景）：只派现不送股，总价值同样不动、现金不抬高。
func TestProcessCorporateActions_QfqDividendOnly_TotalValueUnchanged(t *testing.T) {
	tr := rev06Tracker(t)
	state := &BacktestState{ID: "rev06-div", Tracker: tr}
	eng := &Engine{}

	before := tr.GetTotalValue(rev06QfqPrices())
	cashBefore := tr.GetCash()
	eng.processCorporateActions(state, rev06CorporateActions(false, true), rev06Day2, zerolog.Nop())

	assert.InDelta(t, before, tr.GetTotalValue(rev06QfqPrices()), 1e-9)
	assert.InDelta(t, cashBefore, tr.GetCash(), 1e-9, "现金也不得被分红抬高")
}

// TestProcessCorporateActions_UnheldSplitStillReportedForK18 钉住与旧口径
// 一致的控制流：无持仓的 split 事件仍进 splitSymbols（旧代码里 ProcessSplit
// 对无持仓 no-op 但返回 nil，因此旧代码也收录）——K18 父单取消语义不因
// 本单收窄。
func TestProcessCorporateActions_UnheldSplitStillReportedForK18(t *testing.T) {
	tr := tracker.NewTracker(1_000_000, 0.0003, 0.001, contracts.DefaultTradingConfig(), zerolog.Nop())
	state := &BacktestState{ID: "rev06-unheld", Tracker: tr}
	eng := &Engine{}

	ca := rev06CorporateActions(true, false)
	splits := eng.processCorporateActions(state, ca, rev06Day2, zerolog.Nop())

	assert.Equal(t, []string{rev06Symbol}, splits)
	assert.InDelta(t, 1_000_000.0, tr.GetTotalValue(map[string]float64{}), 1e-9, "无持仓时什么都不该发生")
}

// TestProcessCorporateActions_TargetPositionNotDrifted 钉住 reconcile 路径：
// TargetPositions 已对齐时，记录-only 处理不得把它改歪（股数没变，
// ActualQty 必须保持）。
func TestProcessCorporateActions_TargetPositionNotDrifted(t *testing.T) {
	tr := rev06Tracker(t)
	state := &BacktestState{
		ID:      "rev06-tp",
		Tracker: tr,
		TargetPositions: map[string]*domain.TargetPosition{
			rev06Symbol: {Symbol: rev06Symbol, TargetQty: 1000, ActualQty: 1000, LastUpdated: rev06Day1},
		},
	}
	eng := &Engine{}

	eng.processCorporateActions(state, rev06CorporateActions(true, true), rev06Day2, zerolog.Nop())

	tp := state.TargetPositions[rev06Symbol]
	assert.InDelta(t, 1000.0, tp.ActualQty, 1e-9, "股数未变，ActualQty 不得漂移")
	assert.InDelta(t, 0.0, tp.PendingQty, 1e-9)
}

// TestREV06_DoubleCountQuantification 是「前后对比」的可执行版：同一
// fixture，旧口径（直接调 tracker 的 ProcessDividend/ProcessSplit 改账）
// 与新口径（processCorporateActions 只记录）两跑，量化虚高幅度。
// 断言把幅度数值钉死——这就是修复效果与「已交付价值」的度量。
func TestREV06_DoubleCountQuantification(t *testing.T) {
	// 旧口径：与旧 processCorporateActions 同序（先分红后拆股）。
	runOld := func(withSplit bool) float64 {
		tr := rev06Tracker(t)
		if err := tr.ProcessDividend(rev06Symbol, domain.Dividend{Symbol: rev06Symbol, DivAmt: 0.1, PayDate: rev06Day2}); err != nil {
			t.Fatalf("ProcessDividend: %v", err)
		}
		if withSplit {
			if err := tr.ProcessSplit(rev06Symbol, domain.Split{Symbol: rev06Symbol, TradeDate: rev06Day2, StkDivRatio: 0.5}); err != nil {
				t.Fatalf("ProcessSplit: %v", err)
			}
		}
		return tr.GetTotalValue(rev06QfqPrices())
	}
	// 新口径：只记录不改账。
	runNew := func(withSplit bool) float64 {
		tr := rev06Tracker(t)
		state := &BacktestState{ID: "rev06-quant", Tracker: tr}
		(&Engine{}).processCorporateActions(state, rev06CorporateActions(withSplit, true), rev06Day2, zerolog.Nop())
		return tr.GetTotalValue(rev06QfqPrices())
	}

	// 除权日前持仓市值（qfq 真值的基数）。
	posValue := 1000.0 * rev06QfqPrice

	t.Run("拆股+分红：旧口径单日 +51.0%", func(t *testing.T) {
		before := rev06Tracker(t).GetTotalValue(rev06QfqPrices())
		oldTotal := runOld(true)
		newTotal := runNew(true)

		// 旧口径增量 = 送 500 股市值 5,000 + 分红贷现 100 = 5,100。
		assert.InDelta(t, 5100.0, oldTotal-before, 1e-6, "旧口径：送股市值 5,000 + 分红贷现 100")
		// 新口径增量 = 0（总价值连续）。
		assert.InDelta(t, 0.0, newTotal-before, 1e-9, "新口径：除权日零虚高")
		assert.InDelta(t, 0.51, (oldTotal-newTotal)/posValue, 1e-9, "单日虚高 51.0%")
	})

	t.Run("只分红：旧口径单日 +1.0%", func(t *testing.T) {
		before := rev06Tracker(t).GetTotalValue(rev06QfqPrices())
		oldTotal := runOld(false)
		newTotal := runNew(false)

		// 旧口径增量 = 分红贷现 100。
		assert.InDelta(t, 100.0, oldTotal-before, 1e-6, "旧口径：分红贷现 100")
		assert.InDelta(t, 0.0, newTotal-before, 1e-9)
		assert.InDelta(t, 0.01, (oldTotal-newTotal)/posValue, 1e-9, "单日虚高 1.0%（年化 2-3pct 量级的单次事件形态）")
	})
}
