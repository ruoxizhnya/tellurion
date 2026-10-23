package backtest

// QT-10 验收⑤：三类执行错误的引擎侧行为。
//
// 三条腿：
//  1. 「每 50 天 trap 一次」的 provider（模拟 wasm trap 归入 ErrStrategyFault）
//     → run **failed**，error/Failures 点名日期 + 类 + 原因；
//  2. 缺一天数据（ErrDataGap）→ run 跑完但 **invalid** + invalid_reasons
//     含 data_gap_skipped，其余交易日照常处理；
//  3. 无法归类的 error → fail-closed（按 ContractBreach 终止 run），
//     不允许退回「只 log + 跳天」。

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/tellurion-contracts/engine"
	"tellurion/pkg/strategy"
)

// qt10Provider 是按调用序注入错误的信号供给（每天调用一次）。
type qt10Provider struct {
	calls  int
	failAt int   // 1-based；0 = 永不失败
	fail   error // 该天返回的错误
}

func (p *qt10Provider) Signals(_ context.Context, _ strategy.Scope, _ time.Time) ([]strategy.Signal, error) {
	p.calls++
	if p.failAt > 0 && p.calls == p.failAt {
		return nil, p.fail
	}
	return nil, nil
}

func qt10Run(t *testing.T, numDays int, prov *qt10Provider) (*BacktestResponse, error, []string) {
	t.Helper()
	eng, _, symbols := buildSyntheticEngine(t, 1, numDays, 1)
	eng.signalProvider = prov
	resp, err := eng.RunBacktest(context.Background(), qsRequest(symbols, numDays))
	return resp, err, symbols
}

// TestQT10_TrapTerminatesRun 每 50 天 trap 一次 ⇒ 第 50 个交易日终止，
// failed 且点名现场（日期/类/原因）。
func TestQT10_TrapTerminatesRun(t *testing.T) {
	const trapDay = 50
	prov := &qt10Provider{
		failAt: trapDay,
		fail:   fmt.Errorf("%w: simulated wasm trap (on_bar)", contracts.ErrStrategyFault),
	}
	resp, err, _ := qt10Run(t, 60, prov)

	require.Error(t, err, "策略故障必须终止 run（QT-10）")
	require.NotNil(t, resp)
	assert.Equal(t, StatusFailed, resp.Status)
	assert.Equal(t, trapDay, prov.calls, "第 %d 天就终止，后面不应继续跑", trapDay)

	wantDate := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC).AddDate(0, 0, trapDay-1).Format("2006-01-02")
	assert.Contains(t, resp.Error, wantDate, "Failures 必须点名失败现场日期")
	assert.Contains(t, resp.Error, "strategy_fault", "Failures 必须点名失败类")
	assert.Contains(t, resp.Error, "simulated wasm trap", "Failures 必须点名原因")
}

// TestQT10_DataGapSkipsDayAndMarksInvalid：数据缺口是唯一可跳天类。
func TestQT10_DataGapSkipsDayAndMarksInvalid(t *testing.T) {
	const gapDay = 3
	prov := &qt10Provider{
		failAt: gapDay,
		fail:   fmt.Errorf("%w: missing 1m bar for 2024-01-04", contracts.ErrDataGap),
	}
	resp, err, _ := qt10Run(t, 6, prov)

	require.NoError(t, err, "数据缺口不终止 run")
	require.NotNil(t, resp)
	assert.Equal(t, StatusInvalid, resp.Status, "被跳过过交易日的运行不是干净观测")
	assert.Contains(t, resp.InvalidReasons, contracts.CodeDataGapSkipped)
	assert.NotContains(t, resp.InvalidReasons, "strategy_fault")
	assert.Equal(t, 6, prov.calls, "跳天之后其余交易日必须照常跑完")
}

// TestQT10_UnclassifiedErrorFailsClosed：无法归类 ⇒ 按 ContractBreach 终止。
func TestQT10_UnclassifiedErrorFailsClosed(t *testing.T) {
	prov := &qt10Provider{failAt: 2, fail: errors.New("some provider-specific failure")}
	resp, err, _ := qt10Run(t, 5, prov)

	require.Error(t, err, "无法归类 = fail-closed（API.md §3.4）")
	require.NotNil(t, resp)
	assert.Equal(t, StatusFailed, resp.Status)
	assert.Contains(t, resp.Error, "contract_breach", "兜底类必须显式可见")
	assert.NotContains(t, resp.InvalidReasons, contracts.CodeDataGapSkipped,
		"非数据缺口不得冒充 data_gap_skipped")
}
