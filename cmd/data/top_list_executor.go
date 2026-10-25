// Package main — top_list（龙虎榜）链的作业层执行器（DBT-10 接线）。
//
// 形状与 DATA-10 的 capitalFlowExecutor 同一条（`POST /api/sync/jobs` →
// `sync_jobs` → worker → 本执行器 → `ETLPipeline.Process` → `Registry.Fetch`
// （链 `top_list: [eastmoney_toplist]`）→ `BulkInsert` 落 `top_list` 表），
// 但**三处必须不同**，每处都是源自己的形状逼出来的，不是风格差异：
//
//  1. **没有 symbols**。`EastmoneyTopListAdapter.fetchTopList` 是按交易日的
//     整市场榜单（filter 只带 TRADE_DATE），完全不读 `req.Symbols`。给它配一个
//     symbols 参数就是配一个「写了却不生效」的旋钮（CR-39 chain drift 家族）。
//     取数量也因此与池子大小无关 —— capital_flow 那个「逐 symbol 顺序 HTTP ⇒
//     全池 ≈5900 次」的超时理由在这里不成立。
//  2. **按页翻页，而不是按 symbol 分批**。同一个源的 `pageSize` 是一页的行数上界，
//     一页取不完窗口就是截断。翻页停在「某页返回 0 行」，走到 `maxPages` 预算
//     上限仍有数据 ⇒ **报错**，不静默丢掉剩下的页（CR-41 治的就是「lmt 与窗口
//     不符 ⇒ 静默截断」，这里是它的同形）。
//  3. **单页失败即中止整窗**。capital_flow 那边「一个坏 symbol 不该让 5900 个
//     的池子白跑」，所以逐 chunk 容错；这边页是**有序整体**，跳过第 3 页继续
//     第 4 页等于在库里留一个没人知道的洞 —— 榜单的完整性是按页构成的。
//
// 窗口必填（start_date / end_date 都要给，见 sync.TopListSyncParams 的注释）：
// 不给窗口时上游按自己的默认排序返回第 1 页，「作业 completed 但库里只有最近
// 100 条」就是 CR-41 换个入口重演。
//
// 2026-10-25（QT-40 H4 ①）：落库那一跳改投 `tellurion-data` 的
// `POST /ingest/points`，core 不再对 `top_list` 表 `ON CONFLICT DO UPDATE`。
// 取数与归一仍在 core（传输所有权不动）。
//
// 本文件**不再**是 `internal/repoguard/registry_etl_wiring_test.go` 规则 A
// 白名单里的条目：H4 ① 之后 core 生产代码里 `NewETLPipeline` 引用归零，
// 白名单同批清空。重新接一条 ETL 写链要新立工单 + 重新备案 + 写清理由。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"tellurion/pkg/data/source"
	"tellurion/pkg/sync"
)

const (
	// topListDefaultPageSize 是一页的行数。取 100 = 适配器自己的默认值
	// （eastmoney_sectors_adapter.go fetchTopList 里 `pageSize := "100"`）——
	// 不猜一个更大的数，源侧是否接受更大的 pageSize 未实测。
	topListDefaultPageSize = 100
	// topListMaxPageSize 是 page_size 参数的上界（同默认值）。给了更大的数
	// 会被**夹到这个值并如实写进 result**，不是静默改写：一次请求拿多少行由
	// 源决定，本仓没有实测过它接受更大的页。
	topListMaxPageSize = 100
	// topListDefaultMaxPages 是翻页预算（不是已知总页数）。单日龙虎榜 ~70–150
	// 行 ⇒ 1–2 页；一年窗口按每天 100 行上界估也在几十页内，20 页够日常回补，
	// 超出就是「该分片了」的信号。
	topListDefaultMaxPages = 20
	// topListMaxSpanDays 是窗口宽度上限。与链上 capital_flow 的
	// moneyflowMaxSpanDays 同一条理由（无界大范围请求 = 不可预期的配额消耗），
	// 也够住「补一个月榜单」这类日常场景。
	topListMaxSpanDays = 400
)

// topListExecutor 执行 top_list（龙虎榜）同步作业。
//
// `write` 是跨仓写面（`pointsPoster`）—— 与 capitalFlowExecutor 同一条口径：
// H4 ① 之后 core 不再是这张表的写者，为 nil 时执行当场失败，不回落本地写库。
type topListExecutor struct {
	reg   *source.Registry
	write pointsPoster
}

// JobType implements sync.JobExecutor.
func (e *topListExecutor) JobType() sync.JobType { return sync.JobTypeTopList }

// Execute implements sync.JobExecutor.
//
// 三条口径，与 capitalFlowExecutor 的三条同族但判据不同：
//
//  1. **DATA-06 那一族在这里由结构本身挡掉**：capital_flow 那边逐 chunk 容错，
//     所以要显式判「succeeded==0 ⇒ error」（全池一个都没落却报 completed）。
//     本执行器任一页失败当场中止整窗（见文件头第 3 点），于是「取到了却一行
//     没落进去」这条静默成功的路径**根本不存在**。
//     与之相对：窗口内确实没有榜单记录（整窗休市）时源返回 0 行，作业以
//     persisted=0 成功收口 —— 那不是假绿，是源给的答案，`result.fetched=0`
//     与上面那种失败在 error_message 里可分辨。
//  2. **翻页预算用尽仍有数据 ⇒ error**（截断闸门，见文件头第 2 点）。
//  3. **ctx 取消直接上抛 `ctx.Err()`**：worker 对已 done 的 ctx 不写状态
//     （AUD-49/50 的既有语义），行留给 stale-running 收尸器。
func (e *topListExecutor) Execute(ctx context.Context, job *sync.Job, progress sync.ProgressReporter) (any, error) {
	var params sync.TopListSyncParams
	if err := json.Unmarshal(job.Params, &params); err != nil {
		return nil, fmt.Errorf("top_list: invalid params: %w", err)
	}
	start, end, err := topListWindow(params.StartDate, params.EndDate)
	if err != nil {
		return nil, err
	}

	pageSize := params.PageSize
	if pageSize <= 0 {
		pageSize = topListDefaultPageSize
	}
	if pageSize > topListMaxPageSize {
		// 夹住而不是拒绝：拒绝会让调用方以为 101 是个非法意图，而真实原因
		// 是「源侧更大的页没实测过」。夹到的值写进 result.pageSize，可见。
		pageSize = topListMaxPageSize
	}
	maxPages := params.MaxPages
	if maxPages <= 0 {
		maxPages = topListDefaultMaxPages
	}

	var pages, fetched, persisted, skipped int
	for page := 1; page <= maxPages; page++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		res, err := runIngestStep(ctx, e.reg, e.write, source.FetchRequest{
			DataType:  source.DataTypeTopList,
			StartDate: start,
			EndDate:   end,
			Extra: map[string]interface{}{
				"page_number": page,
				"page_size":   pageSize,
			},
		}, source.TopListNormalizer)
		if err != nil {
			// 单页失败即中止：跳过一页就是在库里留一个洞（见文件头第 3 点）。
			// 报文里的 persisted 要含**这一页已落的片**：H4 ① 之后一页内部还分片，
			// 只报上一页的数会少报，而「少报的 persisted」与「静默丢数据」
			// 在读侧长得一样。
			return nil, fmt.Errorf("top_list: page %d of window %s..%s failed after %d row(s) persisted: %w",
				page, formatCapitalFlowDate(start), formatCapitalFlowDate(end), persisted+res.Persisted, err)
		}
		pages++
		fetched += res.Fetched
		persisted += res.Persisted
		skipped += res.Skipped
		// 进度按**翻页预算**报，不是按完成度：预算不等于已知总页数（源的
		// 总行数只有翻到空页才知道）。别把它读成「干完了」—— 干完的判据是
		// 空页或 error，两条都在下面的 result / error 里。
		progress.ReportProgress(pages, maxPages, 0)

		if res.Fetched == 0 {
			// 空页 = 窗口取尽。这是唯一的正常终点。
			return topListResult(pages, fetched, persisted, skipped, pageSize, maxPages, start, end), nil
		}
	}

	// 走到这里：预算用尽，而**最后一页是满的**（每页 Fetched>0），说明窗口
	// 还没取尽。静默收工 = 只落前 maxPages 页却报 completed，正是 CR-41。
	return nil, fmt.Errorf(
		"top_list: window %s..%s not exhausted after %d page(s) of %d rows (%d row(s) persisted so far) — raise params.max_pages or shard the window",
		formatCapitalFlowDate(start), formatCapitalFlowDate(end), pages, pageSize, persisted)
}

// topListResult 组装作业 result。字段名与 capital_flow 那条同族（fetched /
// persisted / skipped）以便读侧一套代码看两条链，多出来的是分页链特有的
// pages / page_size / max_pages。
//
// 这里**没有** truncated_pages 之类的布尔：截断从来不是被记录下来的状态，
// 而是被上报的失败（Execute 走到预算上限就 return error）。留一个恒为 false
// 的字段只会让人以为存在「成功但截断」这条路。
func topListResult(pages, fetched, persisted, skipped, pageSize, maxPages int, start, end time.Time) map[string]any {
	return map[string]any{
		"data_type":    source.DataTypeTopList,
		"window_start": formatCapitalFlowDate(start),
		"window_end":   formatCapitalFlowDate(end),
		"pages":        pages,
		"page_size":    pageSize,
		"max_pages":    maxPages,
		"fetched":      fetched,
		"persisted":    persisted,
		// Skipped 的出处与 capital_flow 那条同：core 侧只计「归一化返回 nil 载荷」，
		// 其余（ValidatePoints / 幂等坐标重复 / BulkInsert 单行折跳）都在
		// `tellurion-data` 一侧计好后随回执带回来。见 capital_flow_executor.go
		// 同名字段的注释。
		"skipped": skipped,
		// 落库在哪一侧：与 capital_flow 同一条理由，persisted 不是 core 写的。
		"persisted_via": "data-service /ingest/points",
	}
}

// topListWindow 解析并校验取数窗口：**两端都必须显式给出**。
//
// 与 capitalFlowWindow 的差别是刻意的：那边允许缺省补「近一年」，因为它的
// 窗口只影响逐 symbol 请求的 beg/end 行数；这边的窗口直接决定要不要翻页，
// 缺省一个窗口 = 替调用方决定「取哪些天」，而源在无 filter 时给的是它自己的
// 默认排序第 1 页。所以这里不补默认值，缺哪个点哪个名。
func topListWindow(startStr, endStr string) (time.Time, time.Time, error) {
	if startStr == "" || endStr == "" {
		return time.Time{}, time.Time{}, fmt.Errorf(
			"top_list: start_date and end_date are both required (YYYYMMDD or YYYY-MM-DD) — an unbounded request returns the source's own default first page (CR-41 silent truncation)")
	}
	start, err := parseCapitalFlowDate(startStr)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	end, err := parseCapitalFlowDate(endStr)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if end.Before(start) {
		return time.Time{}, time.Time{}, fmt.Errorf("top_list: end_date %s is before start_date %s",
			formatCapitalFlowDate(end), formatCapitalFlowDate(start))
	}
	if span := int(end.Sub(start).Hours()/24) + 1; span > topListMaxSpanDays {
		return time.Time{}, time.Time{}, fmt.Errorf(
			"top_list: window %s..%s spans %d calendar days, above the %d-day limit (shard the request)",
			formatCapitalFlowDate(start), formatCapitalFlowDate(end), span, topListMaxSpanDays)
	}
	return start, end, nil
}
