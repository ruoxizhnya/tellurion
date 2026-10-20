package walkforward

// REV-20：名实相符的执法测试。
//
// ① 日志 total 修正：此前 runSingleWindow 的 `Int("total", windowIdx)` 把
//    窗口总数打成了下标（第 3 个窗口的 total=2），读日志的人无法知道一共
//    几个窗口。这里用捕获 buffer 断言 total == 窗口总数。
// ② 同参数事实钉住：train/test 两个请求除日期外必须完全相同——这是
//    「rolling-origin 分段回测，非参数优化」这一文档语义的结构性证据；
//    若将来有人真的在 train 段加了优化，这条会红，强制回来改文档。

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/backtest/contracts"
)

// rev20FakeRunner 记录收到的请求并返回固定 Sharpe。
type rev20FakeRunner struct {
	reqs []contracts.BacktestRequest
}

func (r *rev20FakeRunner) RunBacktest(_ context.Context, req contracts.BacktestRequest) (*contracts.BacktestResponse, error) {
	r.reqs = append(r.reqs, req)
	// 用区间起点区分两段：train 段 Sharpe=2.0，test 段 Sharpe=1.0。
	sharpe := 2.0
	if req.StartDate == "2024-02-01" {
		sharpe = 1.0
	}
	return &contracts.BacktestResponse{
		Status:      contracts.StatusCompleted,
		SharpeRatio: sharpe,
	}, nil
}

func rev20Window() wfWindow {
	return wfWindow{
		trainStart: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		trainEnd:   time.Date(2024, 1, 31, 0, 0, 0, 0, time.UTC),
		testStart:  time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC),
		testEnd:    time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC),
	}
}

func TestREV20_WindowLog_TotalIsWindowCount(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	wf := NewWalkForwardEngine(func() (contracts.EngineRunner, error) {
		return &rev20FakeRunner{}, nil
	}, nil, zerolog.New(&buf))

	const totalWindows = 7
	req := WalkForwardRequest{Strategy: "momentum", StockPool: []string{"600000.SH"}}

	res := wf.runSingleWindow(context.Background(), req, 2 /* 第 3 个窗口 */, totalWindows,
		rev20Window(), &rev20FakeRunner{})
	require.NotNil(t, res)

	logged := buf.String()
	assert.Contains(t, logged, `"total":7`,
		"日志 total 必须是窗口总数；旧的 Int(\"total\", windowIdx) 会打出 2（下标）")
	assert.NotContains(t, logged, `"total":2,`,
		"不允许再出现把下标当 total 的回归")
}

func TestREV20_TrainAndTestAreSameStrategySameParams(t *testing.T) {
	t.Parallel()

	wf := NewWalkForwardEngine(func() (contracts.EngineRunner, error) {
		return &rev20FakeRunner{}, nil
	}, nil, zerolog.Nop())

	runner := &rev20FakeRunner{}
	res := wf.runSingleWindow(context.Background(),
		WalkForwardRequest{Strategy: "momentum", StockPool: []string{"600000.SH", "000001.SZ"}},
		0, 1, rev20Window(), runner)
	require.NotNil(t, res)
	require.Len(t, runner.reqs, 2, "一个窗口 = train + test 两次回测")

	train, test := runner.reqs[0], runner.reqs[1]
	assert.Equal(t, train.Strategy, test.Strategy, "同策略")
	assert.Equal(t, train.StockPool, test.StockPool, "同票池")
	assert.Exactly(t, train.InitialCapital, test.InitialCapital, "同初始资金")
	assert.Exactly(t, train.RiskFreeRate, test.RiskFreeRate)
	assert.NotEqual(t, train.StartDate, test.StartDate, "两段日期不同（分段回测的本义）")
	// BacktestRequest 除日期区间外的字段逐一相等 = 「同策略同参数各跑一段」
	// 的结构性证据（没有优化步骤可改参数——REV-20 文档语义）。

	// oosVsTrain = test/train 的比值语义（1.0/2.0）。
	assert.InDelta(t, 0.5, res.OOSvsTrain, 1e-12)
}

func TestREV20_DocNamesRollingOrigin(t *testing.T) {
	t.Parallel()

	// 文档一致性守卫：源码里 RunWalkForward 的 doc 必须按 rolling-origin
	// 事实表述（REV-20 ③）。读源文件而非反射——这就是文档测试。
	raw, err := os.ReadFile("walkforward.go")
	require.NoError(t, err)
	assert.True(t, strings.Contains(string(raw), "rolling-origin segmented backtest"),
		"RunWalkForward doc 必须声明 rolling-origin 分段回测语义")
	assert.False(t, strings.Contains(string(raw), "optimize / calibrate"),
		"不得再宣称存在 optimize/calibrate 步骤（当前实现没有）")
}
