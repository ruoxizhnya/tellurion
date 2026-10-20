package main

// REV-24 回归护栏：批量同步的「切批 → 逐 symbol fetch → 计数」只有一份实现。
//
// 旧状：fundamentals / dividends / splits 三个 job executor + legacy /sync/dividends
// 端点各抄一份同样的循环（四份逐字相同），改口径要改四处；且三个 executor 的
// 日期参数里，dividends/splits 硬编码 ""（调用方指定的区间被静默丢掉）。
//
// 本文件钉住 helper 的三条语义：计数口径、切批边界、取消传播。

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rev24Progress 记录 helper 上报的每一批进度。
type rev24Progress struct {
	reports [][3]int // processed, total, failed
}

func (p *rev24Progress) ReportProgress(processed, total, failed int) {
	p.reports = append(p.reports, [3]int{processed, total, failed})
}

func (p *rev24Progress) ReportError(string) {}

// TestRunBatchedSymbols_CountingSemantics：单 symbol 失败不中断整批，
// 计数按「成功数 / 记录数 / 失败数」三口径分开记。
func TestRunBatchedSymbols_CountingSemantics(t *testing.T) {
	t.Parallel()

	records := map[string]int{"A.SH": 3, "B.SH": 0, "C.SH": 5}
	progress := &rev24Progress{}

	res, err := runBatchedSymbols(context.Background(),
		[]string{"A.SH", "B.SH", "C.SH"}, batchSyncSize, progress,
		func(_ context.Context, symbol string) (int, error) {
			if symbol == "B.SH" {
				return 0, errors.New("upstream 500")
			}
			return records[symbol], nil
		})

	require.NoError(t, err)
	assert.Equal(t, 2, res.StocksSynced, "失败的那只不算同步成功")
	assert.Equal(t, 8, res.RecordsSaved, "只累计成功 symbol 的记录数")
	assert.Equal(t, 1, res.FailedStocks, "失败计数单独记，不吞掉")
}

// TestRunBatchedSymbols_SuccessWithZeroRecordsIsSuccess：抓到 0 条 ≠ 失败。
// 退市/无分红标的会合法返回空集，把它算进 failed 会让「全市场同步」永远
// 报失败。
func TestRunBatchedSymbols_SuccessWithZeroRecordsIsSuccess(t *testing.T) {
	t.Parallel()

	res, err := runBatchedSymbols(context.Background(), []string{"A.SH"}, batchSyncSize, nil,
		func(context.Context, string) (int, error) { return 0, nil })

	require.NoError(t, err)
	assert.Equal(t, 1, res.StocksSynced)
	assert.Equal(t, 0, res.RecordsSaved)
	assert.Equal(t, 0, res.FailedStocks)
}

// TestRunBatchedSymbols_BatchBoundaries：25 个 symbol 按 10 切批，进度只在
// 批末上报（10 / 20 / 25），不是每个 symbol 一次。
func TestRunBatchedSymbols_BatchBoundaries(t *testing.T) {
	t.Parallel()

	symbols := make([]string, 25)
	for i := range symbols {
		symbols[i] = "S.SH"
	}
	progress := &rev24Progress{}

	res, err := runBatchedSymbols(context.Background(), symbols, batchSyncSize, progress,
		func(context.Context, string) (int, error) { return 2, nil })

	require.NoError(t, err)
	assert.Equal(t, 25, res.StocksSynced)
	assert.Equal(t, 50, res.RecordsSaved)
	assert.Equal(t, [][3]int{{10, 25, 0}, {20, 25, 0}, {25, 25, 0}}, progress.reports,
		"批末各报一次，failed 随批累计")
}

// TestRunBatchedSymbols_ZeroBatchSizeFallsBack：调用方传零值（或负值）不能
// 变成死循环，也不能变成「每只一批」。回落 10。
func TestRunBatchedSymbols_ZeroBatchSizeFallsBack(t *testing.T) {
	t.Parallel()

	for _, size := range []int{0, -3} {
		symbols := make([]string, 11)
		progress := &rev24Progress{}
		_, err := runBatchedSymbols(context.Background(), symbols, size, progress,
			func(context.Context, string) (int, error) { return 1, nil })
		require.NoError(t, err)
		assert.Equal(t, [][3]int{{10, 11, 0}, {11, 11, 0}}, progress.reports,
			"batchSize=%d 应回落 batchSyncSize", size)
	}
}

// TestRunBatchedSymbols_CancelledBeforeStart：已取消的 ctx 不应发出任何上游请求。
func TestRunBatchedSymbols_CancelledBeforeStart(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	calls := 0
	_, err := runBatchedSymbols(ctx, []string{"A.SH", "B.SH"}, batchSyncSize, nil,
		func(context.Context, string) (int, error) {
			calls++
			return 1, nil
		})

	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, calls, "取消后不得再打上游")
}

// TestRunBatchedSymbols_PropagatesContextToFetch 是取消语义的**核心**证据：
//
//	fetch 拿到的必须是调用方的 ctx（而不是 Background）——否则 fetch 内部的
//	HTTP 请求不会随取消中止，一个 5900 只的全市场作业在取消后仍会把当前
//	批次打完。同时钉住「批内也检查一次」：取消发生在第 1 只的 fetch 里，
//	第 2 只不该被调用。
func TestRunBatchedSymbols_PropagatesContextToFetch(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		calls     int
		fetchErrs []error
	)
	_, err := runBatchedSymbols(ctx, []string{"A.SH", "B.SH", "C.SH"}, batchSyncSize, nil,
		func(fctx context.Context, _ string) (int, error) {
			calls++
			cancel() // 模拟用户在作业跑到一半时点了取消
			fetchErrs = append(fetchErrs, fctx.Err())
			return 1, nil
		})

	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, calls, "批内取消后不再打后续 symbol")
	require.Len(t, fetchErrs, 1)
	assert.ErrorIs(t, fetchErrs[0], context.Canceled,
		"fetch 收到的 ctx 必须随调用方取消（传 Background 会让这里为 nil）")
}

// TestBatchSyncResult_ToMapShape：JSON 形状与历史响应/job 结果一致。
func TestBatchSyncResult_ToMapShape(t *testing.T) {
	t.Parallel()

	got := batchSyncResult{StocksSynced: 7, RecordsSaved: 42, FailedStocks: 2}.toMap()
	assert.Equal(t, map[string]any{
		"stocks_synced": 7,
		"records_saved": 42,
		"failed_stocks": 2,
	}, got)
}
