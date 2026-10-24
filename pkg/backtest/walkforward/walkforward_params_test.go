package walkforward

// QT-68：walk-forward 的 run 级参数覆写必须原样进入**每个窗口的 train 与
// test 两段**回测。两条腿：
//  1. 透传等价：窗口内实际发出的 BacktestRequest 与一个内联调用者在同一
//     日期区间、同一策略、同一参数下会构造的请求逐字段相等 —— 这正是
//     「同参数下 walkforward 与内联策略结果一致」的机制面（引擎收到的输入
//     相同，输出自然相同；引擎本体的同一性由 QT-20 的用例覆盖）。
//  2. 形状闸门：嵌套参数在任何取数之前被拒（store 为 nil 也不会 panic）。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	contracts "github.com/ruoxizhnya/tellurion-contracts/engine"
)

type recordingRunner struct {
	reqs []contracts.BacktestRequest
}

func (r *recordingRunner) RunBacktest(_ context.Context, req contracts.BacktestRequest) (*contracts.BacktestResponse, error) {
	r.reqs = append(r.reqs, req)
	return &contracts.BacktestResponse{
		TotalReturn: float64(len(r.reqs)),
		SharpeRatio: 1.5,
	}, nil
}

func qt68Date(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func TestRunSingleWindow_PropagatesStrategyParamsToBothSegments(t *testing.T) {
	wf := &WalkForwardEngine{}
	runner := &recordingRunner{}

	params := map[string]any{"lookback_days": float64(40)}
	req := WalkForwardRequest{
		Strategy:       "momentum",
		StockPool:      []string{"600000.SH"},
		InitialCapital: 500000,
		RiskFreeRate:   0.02,
		StrategyParams: params,
	}
	win := wfWindow{
		trainStart: qt68Date(2024, 1, 2), trainEnd: qt68Date(2024, 3, 29),
		testStart: qt68Date(2024, 4, 1), testEnd: qt68Date(2024, 6, 28),
	}

	res := wf.runSingleWindow(context.Background(), req, 0, 1, win, runner)
	require.NotNil(t, res)
	require.Len(t, runner.reqs, 2, "一个窗口必须发出 train + test 两段回测")

	// 内联调用者在同一区间、同一策略/参数下会发出的请求：逐字段相等。
	wantTrain := contracts.BacktestRequest{
		Strategy:       "momentum",
		StockPool:      []string{"600000.SH"},
		StartDate:      "2024-01-02",
		EndDate:        "2024-03-29",
		InitialCapital: 500000,
		RiskFreeRate:   0.02,
		StrategyParams: params,
	}
	wantTest := wantTrain
	wantTest.StartDate = "2024-04-01"
	wantTest.EndDate = "2024-06-28"

	assert.Equal(t, wantTrain, runner.reqs[0])
	assert.Equal(t, wantTest, runner.reqs[1])

	// 报告里的两段结果就是这两次（同参数）内联回测的直接换算。
	assert.Equal(t, wf.toBacktestResult(&contracts.BacktestResponse{TotalReturn: 1, SharpeRatio: 1.5}), res.TrainResult)
	assert.Equal(t, wf.toBacktestResult(&contracts.BacktestResponse{TotalReturn: 2, SharpeRatio: 1.5}), res.TestResult)
}

func TestRunWalkForward_RejectsNestedStrategyParamsBeforeTouchingStore(t *testing.T) {
	// store 为 nil：若形状校验发生在取数之后，这里会 panic 而不是返回错误。
	wf := &WalkForwardEngine{}
	_, err := wf.RunWalkForward(context.Background(), WalkForwardRequest{
		Strategy:  "momentum",
		StockPool: []string{"600000.SH"},
		StartDate: "2024-01-01",
		EndDate:   "2024-12-31",
		StrategyParams: map[string]any{
			"lookback_days": map[string]any{"nested": 1},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "strategy_params")
}
