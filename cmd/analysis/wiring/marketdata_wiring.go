package kernel

// marketdata_wiring.go —— D6 日内预热的取数通道装配（QT-64）。
//
// 修的是什么：`backtest.Engine.SetRouter` 与 `WithRouter` 在 QT-64 之前
// **零生产调用点**，全仓唯一的 `marketdata.NewRouter` 在 `cmd/data` 那条
// 挖矿 CLI 上 —— 于是所有回测路径的 `warmIntradayIfNeeded` 都看到
// `router == nil`，静默不预热。`IntradayAware` 策略拿不到分钟快照，
// 表现成「特征恒不可得」，没有任何一层报错。
//
// 本文件把「构造」与「接线」各收成一处：
//
//  1. `NewMarketDataRouter` —— 进程内**只有一个**实例，复用 store 的连接
//     池（一个进程一个池，ADR-031 资源归属裁决同款），不新开池；
//  2. `AttachMarketDataRouter` —— 主引擎（`StrategyRuntimeModule.Init`）
//     与 walk-forward 的窗口引擎（`cmd/analysis` 的 newEngine 工厂）都经
//     这**同一个装配函数**拿到 Router。两处各写一遍 SetRouter 就是第二
//     真相：漏一处的那条路径又会回到静默不预热。
//
// 装配缺失不许静默（③-③）：nil Router 在这里显式 Warn；策略侧则由
// `runContext.intradayFeatureProvider` 返回 nil 供给（LocalSignalProvider
// 的「nil 不注入」契约），策略用到日内特征时明确失败，而不是静默全零。

import (
	"fmt"

	"github.com/rs/zerolog"
	"tellurion/pkg/backtest"
	"tellurion/pkg/marketdata"
	"tellurion/pkg/storage"
)

// NewMarketDataRouter 构造进程内唯一的规范层 Router。
//
// 连接池归 store（调用方），本函数不新开池 —— 这是 ③-① 的「复用
// store.DB()」口径。store 为 nil 直接报错：静默返回 nil Router 会把
// 装配错误推迟到策略跑到日内特征时才被发现。
func NewMarketDataRouter(store *storage.PostgresStore) (*marketdata.Router, error) {
	if store == nil {
		return nil, fmt.Errorf(
			"kernel: marketdata.Router 需要 store（复用其连接池，一个进程一个池），但 store 为 nil —— 装配传参缺失")
	}
	return marketdata.NewRouter(store.DB()), nil
}

// AttachMarketDataRouter 是 D6 取数通道的唯一装配函数。
//
// 主引擎与 walk-forward 窗口引擎都调它：同一份 Router 实例、同一条
// 接线判据。nil Router 显式 Warn（缺装配 = 显式状态，不是静默省略），
// 策略侧的行为见文件头。
func AttachMarketDataRouter(eng *backtest.Engine, router *marketdata.Router, logger zerolog.Logger) {
	if eng == nil {
		return
	}
	if router == nil {
		logger.Warn().Msg(
			"marketdata.Router 未装配 —— D6 日内快照不会预热；IntradayAware 策略会明确失败（缺失≠0，不静默产出全零信号）")
		eng.SetRouter(nil)
		return
	}
	eng.SetRouter(router)
}
