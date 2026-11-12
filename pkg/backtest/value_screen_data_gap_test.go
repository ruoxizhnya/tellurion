package backtest

// CORE-STRATEGY-SCREEN-FALLBACK-DEAD-UPSTREAM 裁决（B1）的引擎级验收：
// 多票 value_screening 的价值筛选输入不可得 ⇒ 每个调仓日返回
// contracts.ErrDataGap ⇒ 引擎跳过该天并记 data_gap_skipped ⇒ run 跑完但
// 终态 **invalid**（不是 completed，也不是 failed）。
//
// 改前行为（真验红）：多票模式打的是 data 服务的 `POST /screen`，该路由从未
// 注册，请求失败后被静默吞掉并退回纯动量选股 —— run 以 completed 收场，
// 但价值条件一条都没生效。那份「成功」是假结果。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	contracts "github.com/ruoxizhnya/tellurion-contracts/engine"
	_ "tellurion/pkg/strategy/plugins" // 内置策略靠 init() 注册
)

func TestValueScreeningMultiStockRunIsInvalidWithDataGap(t *testing.T) {
	// 5 票 > 3 ⇒ 多票分支；90 个交易日（覆盖 60 日动量回看窗）、每日调仓。
	// 天数必须足够长：改前代码在 /screen 失败后靠这段行情「退回纯动量」，
	// 历史窗口不足会连假信号都出不来（zero_trades），那样就验不出
	// 「以前看似成功」的原始症状。
	eng, _, symbols := buildSyntheticEngine(t, 5, 90, 1)
	req := BacktestRequest{
		Strategy:       "value_screening",
		StrategyParams: map[string]any{"rebalance_frequency": "daily"},
		StockPool:      symbols,
		StartDate:      "2024-01-02",
		EndDate:        "2024-03-31",
		InitialCapital: 1_000_000,
		RiskFreeRate:   0.03,
	}

	resp, err := eng.RunBacktest(context.Background(), req)
	require.NoError(t, err, "数据缺口不终止 run（B1：跑完但终态 invalid）")
	require.NotNil(t, resp)

	assert.Equal(t, StatusInvalid, resp.Status,
		"多票价值筛选输入不可得的 run 不能被当作 completed")
	assert.Contains(t, resp.InvalidReasons, contracts.CodeDataGapSkipped,
		"每个因输入不可得被跳过的交易日都必须汇总成 data_gap_skipped")
	assert.NotEqual(t, StatusFailed, resp.Status,
		"策略输入不可得是数据缺口类，不是策略故障")
}
