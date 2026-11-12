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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	contracts "github.com/ruoxizhnya/tellurion-contracts/engine"
	"tellurion/pkg/domain"
	"tellurion/pkg/strategy"
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

// TestValueScreeningMultiStockRunSelectsWithInjectedValueInputs 是上一条的对偶：
// 注入每日可比的 daily_basic 快照 + ROE 后，多票价值筛选必须真的产出成交，
// 且 run 正常 completed —— 证明「价值维度参与」不是靠缺口兜底，而是能跑通。
func TestValueScreeningMultiStockRunSelectsWithInjectedValueInputs(t *testing.T) {
	const numDays = 90
	eng, _, symbols := buildSyntheticEngine(t, 5, numDays, 1)

	start := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	pe, pb, roe := 10.0, 1.0, 0.2
	snapshot := make(map[string][]domain.DailyBasicValuation, len(symbols))
	for _, sym := range symbols {
		series := make([]domain.DailyBasicValuation, 0, numDays)
		for d := 0; d < numDays; d++ {
			p, b := pe, pb
			series = append(series, domain.DailyBasicValuation{
				TsCode: sym, TradeDate: start.AddDate(0, 0, d),
				PE: &p, PESource: domain.DailyBasicPESourceTTM,
				PB: &b, PBSource: domain.DailyBasicPBSourceReported,
			})
		}
		snapshot[sym] = series
	}
	eng.SetDailyBasicValuations(snapshot)

	fundamentals := make(map[string]strategy.FundamentalSeries, len(symbols))
	for _, sym := range symbols {
		r := roe
		fundamentals[sym] = strategy.FundamentalSeries{
			{Symbol: sym, Date: start.AddDate(0, 0, -1), ROE: &r},
		}
	}
	eng.SetFundamentals(fundamentals)

	resp, err := eng.RunBacktest(context.Background(), BacktestRequest{
		Strategy:       "value_screening",
		StrategyParams: map[string]any{"rebalance_frequency": "daily", "top_n": 3},
		StockPool:      symbols,
		StartDate:      "2024-01-02",
		EndDate:        "2024-03-31",
		InitialCapital: 1_000_000,
		RiskFreeRate:   0.03,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Equal(t, StatusCompleted, resp.Status,
		"价值输入齐全时 run 必须 completed，invalid=%v", resp.InvalidReasons)
	assert.NotContains(t, resp.InvalidReasons, contracts.CodeDataGapSkipped,
		"输入齐全时不应产生 data_gap_skipped")
	assert.Greater(t, resp.TotalTrades, 0,
		"价值条件全部满足且有动量时应该真的成交，而不是空跑")
}
