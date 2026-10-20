//go:build dbt09live

package main

// DBT-09 的真源腿：capital_flow 的链首选换成 Tushare `moneyflow` 之后，
// 拿真源 + 真库走一次，把「接线即灌数」从替身证据升级成真库读数。
//
//	go test ./cmd/data/ -tags=dbt09live -run TestDBT09MoneyflowLive -count=1 -v
//
// 为什么不进默认套件（沿用 `data10live` 那条的判断）：可达性与配额不归本仓
// 决定，挂进 CI 等于把「上游今天理不理我们」变成每天的红。
//
// 这条腿要质问的是三件事，都与上游好坏无关，因此不会随网络腐烂：
//  1. 报成功就必须真落了行数（DATA-06 那一族在作业层的复发）；
//  2. `source` 列如实记录真正供数的适配器 —— DBT-09 把首选换成了
//     tushare_moneyflow，这条断言就是那次换源在真库上的读数；
//  3. 分档净额与该日成交额同量纲（单位实测：万元 ×10000 在同步边界折成元，
//     错 100/1000 倍会当场崩这条），且**本源不给的列必须 NULL 而不是 0**
//     —— 这是本仓「缺失≠0」口径在真库上的取证，不是替身上的自我确认。

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/data"
	"tellurion/pkg/logging"
	"tellurion/pkg/storage"
	"tellurion/pkg/sync"
)

const (
	dbt09LookbackDays = 10
	// dbt09Query 只读这条腿真正要质问的列（其余分档列由 httptest 腿覆盖）。
	// 对照用的 turnover 来自 ohlcv_daily_qfq —— 独立于本条链的第二个读数源，
	// 否则「同量纲」就退化成了自证。
	dbt09Query = `SELECT c.symbol, c.trade_date, c.period, c.source,
	                 c.main_net, c.retail_net, c.close_price, c.super_net,
	                 (SELECT o.turnover FROM ohlcv_daily_qfq o
	                  WHERE o.symbol = c.symbol AND o.trade_date = c.trade_date) AS ref_turnover
	              FROM capital_flow c
	              WHERE c.symbol = ANY($1) AND c.trade_date >= $2
	              ORDER BY c.symbol, c.trade_date`
)

func TestDBT09MoneyflowLiveFillsCapitalFlow(t *testing.T) {
	token := os.Getenv("TUSHARE_TOKEN")
	if token == "" {
		t.Skip("DBT-09 真源腿需要 TUSHARE_TOKEN（免费档即可，moneyflow 在册）")
	}
	store := capitalFlowStore(t)
	ctx := context.Background()

	// 窗口钉在库里真有日线的最近若干日：单位那条对照需要一个独立的读数源。
	symbols := dbt09SymbolsWithBars(t, ctx, store)
	require.NotEmpty(t, symbols, "前置不成立：ohlcv_daily_qfq 里没有可用票，单位对照无从谈起")

	end := referenceTradeDate(t, ctx, store, symbols[0])
	start := end.AddDate(0, 0, -dbt09LookbackDays)

	// 走**生产链**：buildDataSourceRegistry + 真 TushareClient。store 传进去，
	// 于是 call() 的归档腿（tushare.go:220）顺带给 ingest.raw 落真快照 ——
	// 这条腿的副产品正是 DBT-11 需要的证据形态。
	client := data.NewTushareClient(token, "https://api.tushare.pro", 3, store, nil)
	reg := buildDataSourceRegistry(client, logging.Logger)
	exec := &capitalFlowExecutor{reg: reg, persist: store}

	params, err := json.Marshal(map[string]any{
		"symbols":    symbols,
		"start_date": start.Format("20060102"),
		"end_date":   end.Format("20060102"),
	})
	require.NoError(t, err)
	job := &sync.Job{
		ID:         "dbt09-live",
		JobType:    sync.JobTypeCapitalFlow,
		Status:     sync.JobStatusRunning,
		Params:     params,
		MaxRetries: 3,
	}

	res, err := exec.Execute(ctx, job, &recordingReporter{})
	if err != nil {
		// 失败必须是**源侧**的失败，且要能被看见（DATA-10 那条腿立的规矩）。
		t.Logf("真源取数失败：%v", err)
		assert.NotContains(t, err.Error(), "nil registry", "不许把装配问题误报成源问题")
		t.Fatalf("DBT-09 真源腿未拿到数据，其余断言无从执行：%v", err)
	}
	got := res.(map[string]any)
	persisted, _ := got["persisted"].(int)
	assert.Greater(t, persisted, 0,
		"Execute 报成功却没落一行 ⇒ 静默空表（DATA-06 那一族）在作业层复发")

	rows := dbt09ReadBack(t, ctx, store, symbols, start)
	require.NotEmpty(t, rows, "落库后必须能读回来")

	// 断言 2：谁供的数就写谁的源名。
	for _, r := range rows {
		assert.Equal(t, "tushare_moneyflow", r.source,
			"DBT-09 之后链首选是 tushare_moneyflow；source 列记成别的东西说明链没按登记那样排")
	}
	// 断言 2b：`period` 必须是执行器归一出来的那一种（capitalFlowDefaultPeriod
	// = "daily"，见 cmd/data/capital_flow_executor.go:36 与
	// normalizeCapitalFlowPeriod）。
	//
	// ⚠️ 这里刻意**不**沿用 `data10live` 那条老腿的期望值
	// （`capital_flow_live_test.go:131` 断言 period == source.DataTypeCapitalFlow
	// = "capital_flow"）。那条期望与今天的代码不符：执行器把 period 归一成
	// "daily" 再传给链上任何一员，两员因此写同一个值。老腿因为源侧不可达、
	// 在 Execute 处就 Fatal 了，所以这个错期望从没被执行到过 —— 已按实测登记。
	//
	// 为什么这条值得钉：`period` 是冲突键 (symbol, trade_date, period) 的一部分。
	// 两个源若各写一种 period，同一天的数据就不会 upsert 而是**并排落两行**，
	// 读侧拿到重复行且没有任何报错 —— 这正是下面那条唯一性断言要挡的。
	for _, r := range rows {
		assert.Equal(t, "daily", r.period,
			"period 必须由执行器归一，不许由各 adapter 自己填 —— 它是冲突键")
	}
	seen := make(map[string]int, len(rows))
	for _, r := range rows {
		seen[r.symbol+"|"+r.tradeDate.Format("2006-01-02")]++
	}
	for k, n := range seen {
		assert.Equal(t, 1, n, "%s 在表里有 %d 行 ⇒ 冲突键没起作用，读侧会拿到重复行", k, n)
	}

	// 断言 3a：给不出的列必须是 NULL，不许是 0（缺失≠0）。
	for _, r := range rows {
		assert.Nil(t, r.mainNet, "%s %s 主力净额本源不给，落 0 就是把缺失当读数", r.symbol, r.tradeDate)
		assert.Nil(t, r.retailNet, "%s %s 小单净额（东财口径族）本源不给", r.symbol, r.tradeDate)
		assert.Nil(t, r.closePrice, "%s %s 收盘价本源不给", r.symbol, r.tradeDate)
	}

	// 断言 3b：分档净额与同日成交额同量纲。
	compared := 0
	for _, r := range rows {
		if r.refTurnover == nil || *r.refTurnover <= 0 || r.superNet == nil {
			continue
		}
		compared++
		assert.LessOrEqual(t, dbt09Abs(*r.superNet), 3*dbt09Abs(*r.refTurnover),
			"%s %s 的 super_net=%v 与同日成交额 %v 不同量纲 —— 万元→元的换算在这里被真库读数质问",
			r.symbol, r.tradeDate, *r.superNet, *r.refTurnover)
	}
	assert.Greater(t, compared, 0,
		"一条单位对照都没做成 ⇒ 这条腿是空转，别把它当证据")

	t.Logf("DBT-09 真源腿：persisted=%d 读回=%d 单位对照=%d 窗口=%s~%s 票=%v",
		persisted, len(rows), compared, start.Format("2006-01-02"), end.Format("2006-01-02"), symbols)
}

type dbt09Row struct {
	symbol      string
	tradeDate   time.Time
	period      string
	source      string
	mainNet     *float64
	retailNet   *float64
	closePrice  *float64
	superNet    *float64
	refTurnover *float64
}

// dbt09SymbolsWithBars 取库里日线最深的两只票：窗口必须有一个**独立可读**的
// turnover 作对照，随手报一个代码名会让单位那条断言空转。
func dbt09SymbolsWithBars(t *testing.T, ctx context.Context, store *storage.PostgresStore) []string {
	t.Helper()
	rows, err := store.DB().Query(ctx,
		`SELECT symbol FROM ohlcv_daily_qfq
		 GROUP BY symbol HAVING count(*) > 500 ORDER BY symbol LIMIT 2`)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		out = append(out, s)
	}
	require.NoError(t, rows.Err())
	return out
}

// referenceTradeDate 取库里最近一个真有日线的交易日 —— 用它当窗口右端，
// 免得把作业派给一个源侧根本还没出数的日子（那失败是日历问题不是接线问题）。
func referenceTradeDate(t *testing.T, ctx context.Context, store *storage.PostgresStore, symbol string) time.Time {
	t.Helper()
	var d time.Time
	err := store.DB().QueryRow(ctx,
		`SELECT max(trade_date) FROM ohlcv_daily_qfq WHERE symbol = $1`, symbol).Scan(&d)
	require.NoError(t, err)
	return d.UTC()
}

func dbt09ReadBack(t *testing.T, ctx context.Context, store *storage.PostgresStore, symbols []string, start time.Time) []dbt09Row {
	t.Helper()
	rows, err := store.DB().Query(ctx, dbt09Query, symbols, start)
	require.NoError(t, err)
	defer rows.Close()
	var out []dbt09Row
	for rows.Next() {
		var r dbt09Row
		require.NoError(t, rows.Scan(&r.symbol, &r.tradeDate, &r.period, &r.source,
			&r.mainNet, &r.retailNet, &r.closePrice, &r.superNet, &r.refTurnover))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

// dbt09Abs 自带一份，不复用 capital_flow_live_test.go 里的 absFloat —— 那个文件
// 挂在 `data10live` tag 下，用 `dbt09live` 跑时它根本不参与编译（实测：vet 报
// undefined）。两条真源腿各自自持，比共享一个受 tag 支配的 helper 诚实。
func dbt09Abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
