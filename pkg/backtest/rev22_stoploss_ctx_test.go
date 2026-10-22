package backtest

// REV-22-(8)：止损环的取消语义。旧代码 processStopLosses 内部用
// context.Background() 做止损检查——作业已取消仍可能撮入止损成交。
// 修复后检查用 run ctx、事件循环逐事件检查 ctx.Err()。
//
// 断言策略刻意「端到端」：不管取消发生在检查阶段（checkStopLossesWithATR
// 报错 → 无事件）还是事件循环中（逐事件 break），可观测结果都必须是
// 「取消后没有新成交进入成交流水」。

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
)

func TestREV22_StopLossLoop_RespectsCancellation(t *testing.T) {
	if testing.Short() {
		t.Skip("needs engine fixture, skipped in -short")
	}

	const nSymbols, nDays = 5, 30
	start := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	symbols := make([]string, nSymbols)
	for i := range symbols {
		symbols[i] = "60000" + string(rune('0'+i)) + ".SH"
	}
	data := rev18SynthOHLCV(symbols, nDays, start)
	days := make([]time.Time, 0, nDays)
	for d := 0; d < nDays; d++ {
		days = append(days, start.AddDate(0, 0, d))
	}

	eng := buildRev18Engine(t, data, symbols, days, nil)

	req := BacktestRequest{Strategy: "momentum", StockPool: symbols}
	state := eng.newBacktestState("rev22-stoploss", req, symbols, start, days[nDays-1], 1_000_000, 0.03)
	eng.stateStore.Put(state.ID, state)

	// 塞一个「该止损」的持仓与价格（价格相对成本大跌；是否真触发取决于
	// 风控配置——断言只盯取消语义：ctx 已取消 ⇒ 无新成交）。
	sym := symbols[0]
	_, err := state.Tracker.ExecuteTrade(sym, domain.DirectionLong, 100, 10.0, start, nil)
	require.NoError(t, err)

	prices := map[string]float64{sym: 10.0}
	mdc := map[string][]domain.OHLCV{
		sym: {{
			Symbol: sym, Date: start,
			Open: 10, High: 10.2, Low: 9.0, Close: 10.0, Volume: 1e6,
		}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 先取消再进止损环——最苛刻的时序

	before := len(state.Tracker.GetTrades()) // setup 的开仓成交不算
	eng.processStopLosses(ctx, state, nil, prices, mdc, nil, start, eng.logger)

	assert.Equal(t, before, len(state.Tracker.GetTrades()),
		"已取消的 run 不得再撮入任何止损/止盈成交（REV-22-(8)）")
}

// TestREV22_StopLossLoop_NoBackgroundCtx：源码守卫——processStopLosses 的
// 函数体内不得再出现 context.Background()。
//
// 为什么需要这条：上面的行为测试依赖风控配置真的产生止损事件，而 fixture
// 的 rm 不触发（破坏验证实证：改回 Background 后行为测试仍绿 = 空转）。
// 源码守卫不依赖触发条件，直接钉住「取消语义一致」这个修复本体。
// context.Background 在引擎里只允许出现在「写持久记录」的场景（job.go 的
// 既有正确写法），撮合路径出现即为回归。
func TestREV22_StopLossLoop_NoBackgroundCtx(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("engine_daily.go")
	require.NoError(t, err)

	// 取 processStopLosses 函数体（从签名到下一个顶级 func）。
	src := string(raw)
	start := strings.Index(src, "func (e *Engine) processStopLosses(")
	require.GreaterOrEqual(t, start, 0)
	rest := src[start:]
	end := strings.Index(rest[1:], "\nfunc ")
	if end >= 0 {
		rest = rest[:end+1]
	}
	assert.NotContains(t, rest, "context.Background()",
		"processStopLosses 不得用 Background ctx 做止损检查/撮合（取消语义必须一致，REV-22-(8)）")
}
