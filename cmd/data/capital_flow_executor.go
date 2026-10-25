// Package main — capital_flow 链的作业层执行器（DATA-10 接线，QT-40 H4 ① 改写路径）。
//
// 主任 2026-10-16 裁决入口 (b)：`POST /api/sync/jobs`（job_type=
// `capital_flow`）→ `sync_jobs` → worker → 本执行器 → `Registry.Fetch`
// （链 `capital_flow:[tushare_moneyflow, eastmoney]`，DBT-09 换了链首选）
// → **投点给 `tellurion-data` 的 `POST /ingest/points`**。
//
// 2026-10-25 的变化只有一处，但它是边界级的：**落库那一跳不再由 core 做**。
// 原先这里是 `source.NewETLPipeline(reg, store)` → `Process` →
// `storage.BulkInsert`（`ON CONFLICT DO UPDATE`），core 于是成了
// `market` / `ingest` 的第二写者。现在 core 保留取数与归一（传输的所有权
// 不动，`tellurion-data:AGENTS.md` §3 禁止那边实现传输），把校验 / 闸门 /
// 去重 / 幂等整个交给对端（`tellurion-data:internal/assembly`）。
// 不走 `handlers_sync.go` / 其他执行器那条 `e.tc`（TushareClient）直连线 ——
// 这条链的意义就是让 Registry 的 fallback 链真的承载生产取数。
//
// 为什么是作业层而不是同步 HTTP 端点（裁决原文的可复核版本）：
// `EastmoneyAdapter.fetchCapitalFlow` 是 `for _, sym := range symbols`
// **逐 symbol 顺序 HTTP**，全池 ≈5900 次串行请求，同步端点必然超时；
// 作业层已经备好 ctx 取消（AUD-49 `SetRunningCanceller`）、SSE 进度、
// 重试与并发上限，无需新写。
//
// 本文件**不再**是 `internal/repoguard/registry_etl_wiring_test.go` 规则 A
// 白名单里的条目：H4 ① 之后 core 生产代码里 `NewETLPipeline` 的引用归零，
// 白名单随之清空（守卫从「只有这两处被批准」变成「一处都不许有」）。
// 想重新接一条 ETL 写链，要新立工单 + 重新备案白名单条目 + 写清为什么
// 本仓又当写者。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ruoxizhnya/tellurion-kit/logging"
	"tellurion/pkg/data/source"
	"tellurion/pkg/sync"
)

const (
	// capitalFlowDefaultPeriod 是 eastmoney adapter 真正服务的那一种 period。
	capitalFlowDefaultPeriod = "daily"
	// capitalFlowDefaultBatchSize 决定一次取数里串多少个 symbol：
	// 它是取数与进度上报的粒度。⚠️ 不再是写事务的粒度（H4 ① 之后落库出了
	// 进程，一个 chunk 内部还要按对端单次上界分片，片与片之间不共享事务）。
	capitalFlowDefaultBatchSize = 25
	// capitalFlowDefaultLookback 是 start/end 都没给时的窗口宽度。
	capitalFlowDefaultLookback = 365 * 24 * time.Hour
)

// capitalFlowExecutor 执行 capital_flow 同步作业。
//
// `write` 是跨仓写面（`pointsPoster`，生产实现 `ingestWriteClient`）而不是
// `storage.BulkInserter`：H4 ① 之后这张表的写入方只有 `tellurion-data`。
// 为 nil（未配 `ingest_write.base_url`）时执行**当场失败**，不回落本地写库 ——
// 把第二写者藏进 fallback 里比它公开更坏。
type capitalFlowExecutor struct {
	reg   *source.Registry
	write pointsPoster
}

// JobType implements sync.JobExecutor.
func (e *capitalFlowExecutor) JobType() sync.JobType { return sync.JobTypeCapitalFlow }

// Execute implements sync.JobExecutor.
//
// 三条口径值得单独说，因为它们都是本仓登记过的缺陷家族：
//
//  1. **一段都没成功 ⇒ 返回 error**，不让作业以 `completed` 收口。
//     DATA-06 治的是「落库失败只 Warn，端点照样报成功」；这里是它在作业层
//     的同形：全池取数一个 chunk 都没落进去却报 completed，界面上就是一条
//     绿色的空成功。失败原因经 %w 留在 error 里，进 `sync_jobs.error_message`。
//  2. **单个 chunk 失败不中止整池**：一个坏 symbol 不该让 5900 个的池子白跑
//     （与 `ohlcvAllExecutor` 一致），失败数进 `failed_chunks` 也经
//     `progress.ReportError` 进 `failed_items`，两头都能看见。⚠️ H4 之后这条
//     的边界要重读一次：一个 chunk 内部还要按对端上界分片，**分片之间不共享
//     事务**，所以「这一 chunk 失败」可能是「前两片已落、第三片被 409 拒」。
//     persisted 因此是「已就位」而不是「本 chunk 全落」。
//  3. **ctx 取消直接上抛 `ctx.Err()`**：worker 对已 done 的 ctx 不写状态
//     （AUD-49/50 的既有语义），行留给 stale-running 收尸器。
func (e *capitalFlowExecutor) Execute(ctx context.Context, job *sync.Job, progress sync.ProgressReporter) (any, error) {
	var params sync.CapitalFlowSyncParams
	if err := json.Unmarshal(job.Params, &params); err != nil {
		return nil, fmt.Errorf("capital_flow: invalid params: %w", err)
	}
	period, err := normalizeCapitalFlowPeriod(params.Period)
	if err != nil {
		return nil, err
	}
	start, end, err := capitalFlowWindow(params.StartDate, params.EndDate)
	if err != nil {
		return nil, err
	}
	if len(params.Symbols) == 0 {
		// 空的 symbols 不是「无事可做」，而是「这条作业不会写任何东西」。
		// 让它报 completed 等于给界面递一条假的绿色。
		return nil, fmt.Errorf("capital_flow: symbols is required and must not be empty")
	}

	batchSize := params.BatchSize
	if batchSize <= 0 {
		batchSize = capitalFlowDefaultBatchSize
	}

	var attempted, succeeded, failedChunks int
	var fetched, persisted, skipped int
	var lastErr error

	for i := 0; i < len(params.Symbols); i += batchSize {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		stop := i + batchSize
		if stop > len(params.Symbols) {
			stop = len(params.Symbols)
		}
		chunk := params.Symbols[i:stop]
		attempted++

		res, err := runIngestStep(ctx, e.reg, e.write, source.FetchRequest{
			DataType:  source.DataTypeCapitalFlow,
			Symbols:   chunk,
			Period:    period,
			StartDate: start,
			EndDate:   end,
		}, source.CapitalFlowNormalizer)
		if err != nil {
			failedChunks++
			lastErr = err
			// 计数照收：写面失败的那个 chunk 可能已经落了前几片
			// （片与片不共享事务），丢掉这些数会让 persisted 系统性少报，
			// 而取数失败的 chunk 在这里 res 全零，收不出假数。
			fetched += res.Fetched
			persisted += res.Persisted
			skipped += res.Skipped
			logging.Logger.Warn().Err(err).
				Int("chunk_start", i).
				Int("chunk_size", len(chunk)).
				Int("persisted_so_far", persisted).
				Msg("capital_flow chunk failed")
			progress.ReportError(err.Error())
			progress.ReportProgress(stop, len(params.Symbols), failedChunks)
			continue
		}
		succeeded++
		fetched += res.Fetched
		persisted += res.Persisted
		skipped += res.Skipped
		progress.ReportProgress(stop, len(params.Symbols), failedChunks)
	}

	if succeeded == 0 {
		if persisted > 0 {
			// 「全失败」不等于「什么都没落」：分片之间不共享事务，前几片真的落了。
			// 这句话必须写在 error 里，否则运维会按「无事发生」去重跑，
			// 而对端会把重跑判成内容冲突（409）。
			return nil, fmt.Errorf("capital_flow: all %d chunk(s) failed, but %d row(s) already landed (shards are not one transaction) — read the 409/5xx before retrying: %w",
				attempted, persisted, lastErr)
		}
		return nil, fmt.Errorf("capital_flow: all %d chunk(s) failed, nothing persisted: %w", attempted, lastErr)
	}

	return map[string]any{
		"data_type":     source.DataTypeCapitalFlow,
		"period":        period,
		"window_start":  formatCapitalFlowDate(start),
		"window_end":    formatCapitalFlowDate(end),
		"symbols":       len(params.Symbols),
		"batch_size":    batchSize,
		"chunks":        attempted,
		"succeeded":     succeeded,
		"failed_chunks": failedChunks,
		"fetched":       fetched,
		"persisted":     persisted,
		// Skipped 的出处在 H4 之后分成两侧，读的人必须能分清：
		//   - core 这一侧：归一化返回 nil 载荷的点（runIngestStep）；
		//   - 对端那一侧：`assembly.Write` 的 ValidatePoints 丢弃 + 幂等坐标
		//     重复 + `store.BulkInsert` 的坏载荷单行折跳，三条都由对端计入
		//     回执的 skipped 后原样带回来。
		// 别把它读成「丢的数据不要紧」，也别读成「core 筛掉的」——两侧混在这一个数里。
		"skipped": skipped,
		// 落库在哪一侧，现在是结果里必须看得见的事实：不写这行，
		// persisted 会被读成「core 写的」，而排查 409 时要找的服务在另一个进程里。
		"persisted_via": "data-service /ingest/points",
	}, nil
}

// normalizeCapitalFlowPeriod 把 period 收成 adapter 真正服务的那一种。
//
// 这不是保守，是不让库里写进假标签：
// `fetchCapitalFlowForSymbol` 把 period 只当作 **存储列的值** 透传，
// 请求参数恒为 `klt=1`（日线）。所以「接受 5d」的实际效果是把日线数据
// 贴上 5 日标签、以 (symbol, trade_date, "5d") 为主键写进去 ——
// 一次写两条不同 period 的同一天数据，谁都不能从表里看出它们是同一条。
// 真要做 5d/10d/60d 聚合，那是 adapter 的活，属另一单（本单 ⑥ 一源一单）。
func normalizeCapitalFlowPeriod(period string) (string, error) {
	if period == "" {
		return capitalFlowDefaultPeriod, nil
	}
	if period != capitalFlowDefaultPeriod {
		return "", fmt.Errorf("capital_flow: period %q is not served — the eastmoney adapter always requests daily bars (klt=1), only %q is accepted",
			period, capitalFlowDefaultPeriod)
	}
	return period, nil
}

// capitalFlowWindow 解析并补齐取数窗口。
//
// 空窗口不是「无边界」：不给 beg/end 时上游按自己的默认 lmt 返回，
// 行数由源决定而不是由请求决定。所以缺省补「近一年」，让每次作业的
// 工作量是可预期的（CR-41 治的就是「lmt 与窗口不符 ⇒ 静默截断」）。
func capitalFlowWindow(startStr, endStr string) (time.Time, time.Time, error) {
	start, err := parseCapitalFlowDate(startStr)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	end, err := parseCapitalFlowDate(endStr)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if end.IsZero() {
		end = time.Now().UTC()
	}
	if start.IsZero() {
		start = end.Add(-capitalFlowDefaultLookback)
	}
	if end.Before(start) {
		return time.Time{}, time.Time{}, fmt.Errorf("capital_flow: end_date %s is before start_date %s",
			formatCapitalFlowDate(end), formatCapitalFlowDate(start))
	}
	return start, end, nil
}

// parseCapitalFlowDate 接受仓里通行的两种格式（YYYYMMDD / YYYY-MM-DD），
// 空串返回零值时间（= 未给，交给 capitalFlowWindow 补）。
// 先过 validDate（sync_handlers.go）再解析：格式错的当场点名，
// 不要落到 time.Parse 的 Go 语法报错上去。
func parseCapitalFlowDate(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	if !validDate(v) {
		return time.Time{}, fmt.Errorf("capital_flow: date %q must be YYYYMMDD or YYYY-MM-DD", v)
	}
	for _, layout := range []string{"20060102", "2006-01-02"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("capital_flow: date %q must be YYYYMMDD or YYYY-MM-DD", v)
}

func formatCapitalFlowDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02")
}
