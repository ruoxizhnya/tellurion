package backtest

// QT-14 · 引擎侧的 provider 会话取证。
//
// 引擎的纪律（p0-remediation-design.md A1）：拿到 backtestID 之后 BeginRun、
// defer EndRun（错误路径同样走到）；顺序 run 也走 Begin/End；有状态 provider
// 没实现生命周期时**两道门**都拦 —— 装配期拒绝构造，运行期（绕过装配塞进来
// 的）当场失败。
//
// 破坏验证（改坏 → 变红 → 还原，实测见 TASKS.md QT-14 施工记录）：
//   - runBacktestInternal 里的 Begin/End 钩子删掉 →
//     TestEngine_ProviderSessionOpenedAndClosedPerRun 红（begin/end 清单空），
//     且默认 LocalSignalProvider 路径全线红（Signals 无会话即拒绝）——
//     这道钩子的牙是全仓级的；
//   - NewEngineWithOptions 的装配门删掉 →
//     TestEngine_RejectsStatefulProviderWithoutLifecycleAtAssembly 红；
//   - beginProviderSession 的运行期 Stateful 分支删掉 →
//     TestEngine_StatefulProviderWithoutLifecycleFailsAtRun 红；
//   - EndRun 错误不上交（defer 里吞掉）→ TestEngine_EndRunFailureFailsTheRun 红。

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"tellurion/pkg/strategy"
)

// qsProbe 是自带生命周期的会话探针 provider：记录 Begin/End 序列与入参，
// 信号恒为空（run 照常 completed，不依赖任何策略注册）。
type qsProbe struct {
	mu       sync.Mutex
	begins   []strategy.RunMeta
	ends     []string
	beginErr error
	endErr   error
}

func (p *qsProbe) Signals(context.Context, strategy.Scope, time.Time) ([]strategy.Signal, error) {
	return nil, nil
}
func (p *qsProbe) Stateful() bool { return true }

func (p *qsProbe) BeginRun(_ context.Context, meta strategy.RunMeta) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.beginErr != nil {
		return p.beginErr
	}
	p.begins = append(p.begins, meta)
	return nil
}

func (p *qsProbe) EndRun(_ context.Context, runID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.endErr != nil {
		return p.endErr
	}
	p.ends = append(p.ends, runID)
	return nil
}

func (p *qsProbe) snapshot() ([]strategy.RunMeta, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]strategy.RunMeta(nil), p.begins...), append([]string(nil), p.ends...)
}

// qsStatefulNoLifecycle 只有 SignalProvider + Stateful：装配期必须拒绝的形状。
type qsStatefulNoLifecycle struct{}

func (qsStatefulNoLifecycle) Signals(context.Context, strategy.Scope, time.Time) ([]strategy.Signal, error) {
	return nil, nil
}
func (qsStatefulNoLifecycle) Stateful() bool { return true }

// qsRequest 是最小可跑请求（合成数据引擎 + 空信号 provider，无需注册策略）。
func qsRequest(pool []string, nDays int) BacktestRequest {
	end := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC).AddDate(0, 0, nDays-1)
	return BacktestRequest{
		Strategy:       "qs_no_strategy_needed",
		StockPool:      pool,
		StartDate:      "2024-01-02",
		EndDate:        end.Format("2006-01-02"),
		InitialCapital: 1_000_000,
		RiskFreeRate:   0.03,
	}
}

func TestEngine_RejectsStatefulProviderWithoutLifecycleAtAssembly(t *testing.T) {
	eng, _, symbols := buildSyntheticEngine(t, 2, 5, 1)
	_ = symbols

	// 经 NewEngineWithOptions 重放同一份装配，只把 provider 换成违约形状。
	eng2, err := NewEngineWithOptions(Config{InitialCapital: 1_000_000}, eng.provider,
		WithSignalProvider(qsStatefulNoLifecycle{}))
	require.Error(t, err, "Stateful 却没有 SessionLifecycle 的 provider 必须在装配期被拒（QT-14 ⑤）")
	require.Nil(t, eng2, "拒绝构造：不得交出半装配的引擎")
	require.Contains(t, err.Error(), "SessionLifecycle", "错误要点名缺的东西")
}

func TestEngine_StatefulProviderWithoutLifecycleFailsAtRun(t *testing.T) {
	// 绕过装配门：字段直塞（模拟下一个注入点忘了走 NewEngineWithOptions）。
	// 运行期第二道门必须兜住 —— 有状态没生命周期，跑起来就失败，绝不静默。
	eng, _, symbols := buildSyntheticEngine(t, 2, 5, 1)
	eng.signalProvider = qsStatefulNoLifecycle{}

	resp, err := eng.RunBacktest(context.Background(), qsRequest(symbols, 5))
	require.Error(t, err, "运行期遇到 stateful-no-lifecycle 必须失败")
	require.NotNil(t, resp)
	require.Equal(t, StatusFailed, resp.Status)
	require.Contains(t, err.Error(), "SessionLifecycle", "错误要点名缺的东西")
}

func TestEngine_ProviderSessionOpenedAndClosedPerRun(t *testing.T) {
	probe := &qsProbe{}
	eng, _, symbols := buildSyntheticEngine(t, 3, 6, 1)
	eng.signalProvider = probe

	first, err := eng.RunBacktest(context.Background(), qsRequest(symbols, 6))
	require.NoError(t, err)
	// 探针恒回空信号 ⇒ 零成交 ⇒ 按 OBS-01 口径判 invalid（不是 failed）。
	// 本测试关心的是「run 走完了会话钩子」，不是成交。
	require.NotEqual(t, StatusFailed, first.Status, "status=%s err=%s", first.Status, first.Error)

	begins, ends := probe.snapshot()
	require.Len(t, begins, 1, "一笔 run 恰好一次 BeginRun")
	require.Len(t, ends, 1, "一笔 run 恰好一次 EndRun（defer：错误路径同样走到）")

	// RunMeta 必须携带 run 身份与输入快照 —— 会话的信号归属全靠它。
	meta := begins[0]
	require.Equal(t, first.ID, meta.RunID, "RunID = 引擎的 backtestID")
	require.Equal(t, "qs_no_strategy_needed", meta.Strategy)
	require.Equal(t, symbols, meta.Universe)
	wantStart, err0 := time.Parse("2006-01-02", "2024-01-02")
	require.NoError(t, err0)
	wantEnd, err1 := time.Parse("2006-01-02", "2024-01-07")
	require.NoError(t, err1)
	require.True(t, meta.Start.Equal(wantStart), "RunMeta.Start=%s", meta.Start)
	require.True(t, meta.End.Equal(wantEnd), "RunMeta.End=%s", meta.End)
	require.Equal(t, first.ID, ends[0], "EndRun 收到的 runID 与 BeginRun 一致")

	// 顺序第二笔（A1 规则 5：不做「复用会话」优化）。
	second, err := eng.RunBacktest(context.Background(), qsRequest(symbols, 6))
	require.NoError(t, err)
	require.NotEqual(t, StatusFailed, second.Status, "status=%s err=%s", second.Status, second.Error)

	begins, ends = probe.snapshot()
	require.Len(t, begins, 2, "顺序 run 也必须重新 BeginRun")
	require.Len(t, ends, 2, "顺序 run 也必须重新 EndRun")
	require.Equal(t, second.ID, begins[1].RunID)
	require.Equal(t, second.ID, ends[1])
}

func TestEngine_BeginRunFailureFailsTheRun(t *testing.T) {
	probe := &qsProbe{beginErr: errors.New("session cap exceeded (fail-closed, no queueing)")}
	eng, _, symbols := buildSyntheticEngine(t, 2, 5, 1)
	eng.signalProvider = probe

	resp, err := eng.RunBacktest(context.Background(), qsRequest(symbols, 5))
	require.Error(t, err, "BeginRun 失败 = run 没开起来，不得当成功返回")
	require.NotNil(t, resp)
	require.Equal(t, StatusFailed, resp.Status)
	require.Contains(t, err.Error(), "fail-closed", "错误原样上交，不吞")
}

func TestEngine_EndRunFailureFailsTheRun(t *testing.T) {
	probe := &qsProbe{endErr: fmt.Errorf("finalize failed: session state unwritable")}
	eng, _, symbols := buildSyntheticEngine(t, 2, 5, 1)
	eng.signalProvider = probe

	resp, err := eng.RunBacktest(context.Background(), qsRequest(symbols, 5))
	// 回测循环本身成功（空信号照常跑完），但 EndRun 没做成 —— finalize/Close
	// 失败的结果不可信，run 必须以失败收场，而不是「跑通了」。
	require.Error(t, err, "EndRun 失败必须接管 run 结果（QT-14 fail-closed）")
	require.NotNil(t, resp)
	require.Equal(t, StatusFailed, resp.Status)
	require.Contains(t, err.Error(), "finalize failed", "错误原样上交，不吞")
}
