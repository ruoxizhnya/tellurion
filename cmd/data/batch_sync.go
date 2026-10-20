package main

import (
	"context"

	"tellurion/pkg/logging"
	"tellurion/pkg/sync"
)

// batchSyncSize 是逐 symbol 取数的批次大小。
//
// 10 这个数字的来源是 Tushare 的限频（约 200 请求/分钟）：一次批内 10 个
// symbol 之间不留 sleep，靠批与批之间的 progress 上报自然拉开间隔。四个
// 同步入口（fundamentals / dividends / splits 的 job executor，以及 legacy
// 的同步 dividends 端点）此前各自抄了一份同样的循环，改口径时要改四处。
const batchSyncSize = 10

// batchFetcher 抓取单个 symbol 的记录条数。返回的 int 只用于计数，不参与
// 业务判断——「抓到 0 条」与「抓取失败」是两件事，前者算成功。
type batchFetcher func(ctx context.Context, symbol string) (int, error)

// batchSyncResult 是批量取数的计数口径，四个入口共用（REV-24）。
//
// 三个字段名就是历史响应体里的 JSON key，job 结果与 legacy 端点响应都靠
// toMap 保持形状不变。
type batchSyncResult struct {
	StocksSynced int
	RecordsSaved int
	FailedStocks int
}

// toMap 还原同步结果的历史 JSON 形状（job 结果与 legacy 端点共用）。
func (r batchSyncResult) toMap() map[string]any {
	return map[string]any{
		"stocks_synced": r.StocksSynced,
		"records_saved": r.RecordsSaved,
		"failed_stocks": r.FailedStocks,
	}
}

// runBatchedSymbols 按 batchSize 切批、逐 symbol 调用 fetch，累计计数。
//
// 语义（四个入口此前各自实现、口径必须保持一致，故集中在这里钉住）：
//   - 单 symbol 失败 → FailedStocks++ 并**继续**后续 symbol（一次上游抖动
//     不该让其余 5899 只全部跳过）；失败明细由 fetch 自己记日志。
//   - 单 symbol 成功 → StocksSynced++，RecordsSaved += 条数（含 0 条）。
//   - 每批结束上报一次进度（progress 可为 nil：legacy 端点无 job 可上报）。
//   - ctx 取消 → 立即返回 ctx.Err()，不再发新的上游请求；已累计的计数随
//     错误一起丢弃（调用方只看 error，task 会被标记 cancelled/failed）。
//
// batchSize <= 0 时按 batchSyncSize 处理，避免调用方传零值导致死循环。
func runBatchedSymbols(
	ctx context.Context,
	symbols []string,
	batchSize int,
	progress sync.ProgressReporter,
	fetch batchFetcher,
) (batchSyncResult, error) {
	var res batchSyncResult
	if batchSize <= 0 {
		batchSize = batchSyncSize
	}

	for i := 0; i < len(symbols); i += batchSize {
		if err := ctx.Err(); err != nil {
			return res, err
		}

		end := i + batchSize
		if end > len(symbols) {
			end = len(symbols)
		}

		for _, symbol := range symbols[i:end] {
			// 批内也查一次：一个批只有 10 个 symbol，但 fetch 本身可能要几秒；
			// 取消后还把这批发完等于给上游多打 10 次无意义的请求。
			if err := ctx.Err(); err != nil {
				return res, err
			}

			records, err := fetch(ctx, symbol)
			if err != nil {
				res.FailedStocks++
				continue
			}
			res.StocksSynced++
			res.RecordsSaved += records
		}

		logging.Logger.Debug().
			Int("batch", (i/batchSize)+1).
			Int("progress", end).
			Int("total", len(symbols)).
			Int("failed", res.FailedStocks).
			Msg("Batched symbol sync progress")

		if progress != nil {
			progress.ReportProgress(end, len(symbols), res.FailedStocks)
		}
	}

	return res, nil
}
