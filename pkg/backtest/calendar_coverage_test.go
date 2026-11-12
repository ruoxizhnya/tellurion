package backtest

// 回测区间首尾覆盖校验（2026-11-12）。
//
// 真栈缺陷：`GetTradingDays` 只返回窗口内**存在**的交易日，日历只同步到
// 2024-01-31、请求到 2024-03-29 时，引擎拿 22 天跑完却把 end_date 报成
// 03-29 —— 权益曲线与 end_date 不一致，且没有任何提示。裁决 (a)：这种情况
// 直接 409 拒跑并点名缺失的那一段，而不是静默截断后报 completed。
//
// 这组用例用一个「只返回窗口内真实交易日」的 provider 替身，刻意**不复刻**
// inmemory provider 的「窗口为空就返回全部」兜底 —— 那条兜底会让覆盖度探测
// 永远非空，正好屏蔽掉要修的失效点。

import (
	"context"
	"errors"
	"testing"
	"time"

	apperrors "github.com/ruoxizhnya/tellurion-kit/errors"
	"github.com/stretchr/testify/require"
	"tellurion/pkg/marketdata"
)

// calendarWindowProvider 只返回落在 [start, end] 内的交易日。
type calendarWindowProvider struct {
	marketdata.Provider
	days []time.Time
}

func (p *calendarWindowProvider) GetTradingDays(_ context.Context, start, end time.Time) ([]time.Time, error) {
	out := make([]time.Time, 0, len(p.days))
	for _, d := range p.days {
		if !d.Before(start) && !d.After(end) {
			out = append(out, d)
		}
	}
	return out, nil
}

func (p *calendarWindowProvider) CheckCalendarExists(ctx context.Context, start, end time.Time) (bool, error) {
	days, err := p.GetTradingDays(ctx, start, end)
	return len(days) > 0, err
}

// weekdayDays 造 [start, end] 内的工作日序列（测试日历不模拟法定节假日）。
func weekdayDays(start, end time.Time) []time.Time {
	var out []time.Time
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			continue
		}
		out = append(out, d)
	}
	return out
}

func newCalendarEngine(t *testing.T, days []time.Time) *Engine {
	t.Helper()
	prov := &calendarWindowProvider{Provider: marketdata.NewInMemoryProvider(), days: days}
	eng := newTestEngineWithProvider(t, prov)
	eng.admission = nil
	return eng
}

// 复现实测缺陷：日历覆盖到 2024-01-31，请求到 2024-03-29。
// 断言不是「跑完了但少几天」，而是**根本不许报 completed**。
func TestRunBacktest_ShortCalendarTailIsRejectedNotCompleted(t *testing.T) {
	eng := newCalendarEngine(t, weekdayDays(mustDate("2024-01-02"), mustDate("2024-01-31")))

	resp, err := eng.RunBacktest(context.Background(), BacktestRequest{
		Strategy:       "momentum",
		StockPool:      []string{"600000.SH"},
		StartDate:      "2024-01-02",
		EndDate:        "2024-03-29",
		InitialCapital: 1_000_000,
	})
	require.Error(t, err, "区间尾部没被日历覆盖时必须拒跑，不能报 completed")

	var appErr *apperrors.AppError
	require.True(t, errors.As(err, &appErr), "错误必须是带分类的 AppError：%v", err)
	require.Equal(t, apperrors.ErrCodeDataQuality, appErr.Code)

	msg := err.Error()
	require.Contains(t, msg, "does not cover end_date 2024-03-29")
	require.Contains(t, msg, "last covered trading day in the requested window is 2024-01-31")
	require.Contains(t, msg, "2024-02-01..2024-03-29", "错误必须点名缺失的那一段")

	if resp != nil {
		require.Equal(t, StatusFailed, resp.Status, "拒跑信封只能是 failed")
		require.Empty(t, resp.PortfolioValues, "拒跑时不得产出任何权益点")
	}
}

// 头部也要管：日历从 2024-02-01 才开始，而请求从 2024-01-02 起。
func TestVerifyCalendarCoverage_HeadTruncationRejected(t *testing.T) {
	days := weekdayDays(mustDate("2024-02-01"), mustDate("2024-03-29"))
	eng := newCalendarEngine(t, days)

	err := eng.verifyCalendarCoverage(context.Background(),
		mustDate("2024-01-02"), mustDate("2024-03-29"), days)
	require.Error(t, err)

	var appErr *apperrors.AppError
	require.True(t, errors.As(err, &appErr))
	require.Equal(t, apperrors.ErrCodeDataQuality, appErr.Code)

	msg := err.Error()
	require.Contains(t, msg, "does not cover start_date 2024-01-02")
	require.Contains(t, msg, "first covered trading day in the requested window is 2024-02-01")
	require.Contains(t, msg, "2024-01-02..2024-01-31")
}

// 正向对照：区间被完整覆盖时必须放行。
func TestVerifyCalendarCoverage_FullRangePasses(t *testing.T) {
	days := weekdayDays(mustDate("2024-01-02"), mustDate("2024-03-29"))
	eng := newCalendarEngine(t, days)

	require.NoError(t, eng.verifyCalendarCoverage(context.Background(),
		mustDate("2024-01-02"), mustDate("2024-03-29"), days))
}

// 不能误伤：请求的 end_date 落在周末/节假日，但日历一直覆盖到它之后时，
// 尾部探测窗口里仍有交易日，必须放行（这正是用 30 天窗口而非固定天数差的理由）。
func TestVerifyCalendarCoverage_EndOnNonTradingDayPasses(t *testing.T) {
	days := weekdayDays(mustDate("2024-01-02"), mustDate("2024-04-30"))
	eng := newCalendarEngine(t, days)

	// 2024-03-30 是周六；请求的 end_date 不是交易日，但日历覆盖到 4 月。
	require.NoError(t, eng.verifyCalendarCoverage(context.Background(),
		mustDate("2024-01-02"), mustDate("2024-03-30"), days))
}
