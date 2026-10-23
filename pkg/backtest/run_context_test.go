package backtest

import (
	"testing"

	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
	"tellurion/pkg/marketdata"
)

// 单测 run 作用域状态的脚手架（QT-12）。
//
// 为什么需要它：warm / 读路径从 Engine 搬到了 runContext，而 runContext 的
// 正常入口是 RunBacktest —— 单测不该为了测「财报缓存放哪」去跑一场完整回测。
// 这里直接建一个 run 状态包，区间与引擎装配都按用例给的参数来。
//
// 准入闸一律关掉：这些用例测的是「状态归谁、读到哪一份」，不是排队；
// 留着闸等于让每个用例先抢一个槽位，还会让「降级改了 warmEnd」这类
// 与本用例无关的分支混进断言。闸本身的行为在 admission 自己的测试里测。

// newTestRun 用默认（空）区间建一个 run 状态包。
//
// 适用于不关心回放区间的用例（eligibleUniverse / 快照读 / 预热跳过）。
// 需要区间的用例走 newTestRunSpan。
func newTestRun(t *testing.T, eng *Engine) *runContext {
	t.Helper()
	return newTestRunSpan(t, eng, domain.BacktestParams{})
}

// newTestRunSpan 建一个把准入闸关掉、区间为 params 的 run 状态包。
//
// t.Cleanup 里挂 rc.close()：把「run 收尾必释放凭证」这条不变量交给
// 测试框架兜着，用例忘了 defer 也不会让后续用例撞在残留槽位上。
func newTestRunSpan(t *testing.T, eng *Engine, params domain.BacktestParams) *runContext {
	t.Helper()
	eng.admission = nil
	rc := newRunContext(eng, params)
	t.Cleanup(rc.close)
	return rc
}

// newTestEngineWithProvider 建一台挂着指定取数源的引擎（默认档准入闸在场）。
//
// 与 newTestEngine 的差别只有一个：能不能真的取到 bar。准入与缓存账的用例
// 需要 L1 缓存里有东西可计量，所以要走这条。
func newTestEngineWithProvider(t *testing.T, prov marketdata.Provider) *Engine {
	t.Helper()
	v := viper.New()
	v.Set("backtest.initial_capital", 1_000_000.0)
	v.Set("backtest.commission_rate", 0.0003)
	v.Set("backtest.slippage_rate", 0.0001)
	v.Set("trading.stamp_tax_rate", 0.001)
	v.Set("trading.min_commission", 5.0)
	eng, err := NewEngine(v, prov, zerolog.Nop())
	require.NoError(t, err)
	return eng
}

// recordingObserver 是 RunObserver 的测试替身（QT-12 ⑤ 的读数面）。
//
// 它记录**每一次**写入而不是最后一次：runs_active 这类量要断言的正是
// 「抢到槽位时报了 1、还回去时报了 0」这条轨迹，只看末值会漏掉
// 「从来没人报过」和「报了但报反了」两种失效。
type recordingObserver struct {
	runsActive []int
	runsQueued []int
	cacheBytes map[string][]int64
	wasmBytes  []int64
}

func newRecordingObserver() *recordingObserver {
	return &recordingObserver{cacheBytes: map[string][]int64{}}
}

func (o *recordingObserver) SetRunsActive(v float64) { o.runsActive = append(o.runsActive, int(v)) }
func (o *recordingObserver) SetRunsQueued(v float64) { o.runsQueued = append(o.runsQueued, int(v)) }
func (o *recordingObserver) SetCacheBytes(kind string, v float64) {
	o.cacheBytes[kind] = append(o.cacheBytes[kind], int64(v))
}
func (o *recordingObserver) SetWasmBytes(v float64) { o.wasmBytes = append(o.wasmBytes, int64(v)) }

// lastCacheBytes 返回某 kind 的最后一次读数；从没报过则返回 0 并由
// seenCacheBytes 那条断言负责区分「报 0」与「没报」。
func (o *recordingObserver) lastCacheBytes(kind string) int64 {
	vals := o.cacheBytes[kind]
	if len(vals) == 0 {
		return 0
	}
	return vals[len(vals)-1]
}

func (o *recordingObserver) seenCacheBytes(kind string) bool {
	return len(o.cacheBytes[kind]) > 0
}
