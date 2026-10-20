//go:build data10live

package main

// DATA-10 的真源腿。默认套件**不跑**它（build tag `data10live`），
// 跑它 = 主动要求拿东财真取数来对质：
//
//	go test ./cmd/data/ -tags=data10live -run TestCapitalFlowLive -count=1 -v
//
// 为什么不进默认套件：这条 path 的可达性不归本仓决定（ODR-011 风险表第 2
// 条就是这个家族），把它挂进 CI 等于把「东财今天理不理我们」变成每天的红。
// 但它也不能没有 —— DATA-08 时代这条链只有 httptest 替身证据，「已启用」
// 是代码事实不是运行事实；这条腿就是那道缺口上写的字。
//
// 2026-10-16 首跑实测读数（如实入账，不是「跑过了」）：
//   /api/qt/stock/fflow/daykline/get ⇒ push2 恒 502、push2his 连接重置、
//   浏览器同结果；同主机的 ulist.np / kline / fflow/kline 三条都 200。
//   所以这条腿今天是**红**的，红在源侧，不是红在接线侧。
//
// 两条断言都与上游状态无关，因此不随网络好坏而腐烂：
//  1. 报成功就必须真落了行数（`err == nil ⇒ persisted > 0`）—— 静默空表
//     是 DATA-06 那一族，这条腿把它挡在真源上；
//  2. 落了库的行，主力净额与该日成交额必须同量纲（|main_net| ≤ 3×turnover）。
//     这就是「源单位实测」：换算错 100/1000 倍会当场崩这条，而崩在真库读数上
//     比崩在替身上有分量得多。

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/logging"
	"tellurion/pkg/sync"
)

const (
	liveCapitalFlowSymbol   = "600519.SH"
	liveCapitalFlowLookback = 10 * 24 * time.Hour
)

func TestCapitalFlowLiveSourceRunsThroughJobExecutor(t *testing.T) {
	store := capitalFlowStore(t)

	// 窗口取库里真有日线的最近几天：没有对照读数，单位这条断言就是空转。
	type ohlcvRow struct {
		TradeDate time.Time
		Turnover  float64
	}
	rows, err := store.DB().Query(context.Background(),
		`SELECT trade_date, turnover FROM ohlcv_daily_qfq
		 WHERE symbol = $1 ORDER BY trade_date DESC LIMIT 10`, liveCapitalFlowSymbol)
	require.NoError(t, err)
	var reference []ohlcvRow
	for rows.Next() {
		var r ohlcvRow
		require.NoError(t, rows.Scan(&r.TradeDate, &r.Turnover))
		reference = append(reference, r)
	}
	rows.Close()
	require.NoError(t, rows.Err())
	if len(reference) == 0 {
		t.Fatalf("前置不成立：%s 在 ohlcv_daily_qfq 没有日线读数，单位对照无从谈起", liveCapitalFlowSymbol)
	}
	end := reference[0].TradeDate.UTC()
	start := end.Add(-liveCapitalFlowLookback)

	// 执行器直接用**生产链**（buildDataSourceRegistry），不指向任何替身 ——
	// 这才是「真源走一次」。tushare 传 nil：本条只走 capital_flow，
	// 日线/分钟那几族 adapter 以 Enabled()=false 注册，链上用不到。
	productionReg := buildDataSourceRegistry(nil, logging.Logger)
	exec := &capitalFlowExecutor{reg: productionReg, persist: store}

	job := &sync.Job{
		ID:      "data10-live",
		JobType: sync.JobTypeCapitalFlow,
		Status:  sync.JobStatusRunning,
		// 窗口钉在库里真有日线的那几天：没有对照读数，单位那条断言就是空转。
		Params: json.RawMessage(`{"symbols":["` + liveCapitalFlowSymbol + `"],` +
			`"start_date":"` + start.Format("20060102") + `",` +
			`"end_date":"` + end.Format("20060102") + `"}`),
		MaxRetries: 3,
	}
	rec := &recordingReporter{}
	res, err := exec.Execute(context.Background(), job, rec)

	if err != nil {
		// 源不可达时的诚实出路：把上游原话抖出来，并断言它是一条**源侧**错误。
		t.Logf("真源取数失败（本机当前状态）：%v", err)
		assert.Contains(t, err.Error(), "capital_flow", "失败必须点名是这条链，不是别处：%v", err)
		assert.NotContains(t, err.Error(), "nil registry", "不许把装配问题误报成源问题")
		t.Fatalf("DATA-10 真源腿未拿到数据，接线侧的其余断言无从执行：%v", err)
	}
	require.NotNil(t, res)

	got := res.(map[string]any)
	persisted, _ := got["persisted"].(int)
	assert.Greater(t, persisted, 0,
		"Execute 报了成功却没落一行 ⇒ 这就是 DATA-06 那一族在作业层的复发")

	// —— 单位实测：与同日成交额同量纲 ——
	readBack := readCapitalFlowRows(t, store)
	require.NotEmpty(t, readBack, "落库后必须能读回来")
	turnoverByDate := map[string]float64{}
	for _, r := range reference {
		turnoverByDate[r.TradeDate.UTC().Format("2006-01-02")] = r.Turnover
	}
	compared := 0
	for _, r := range readBack {
		if r.Symbol != liveCapitalFlowSymbol || r.MainNet == nil {
			continue
		}
		key := r.TradeDate.UTC().Format("2006-01-02")
		tv, ok := turnoverByDate[key]
		if !ok || tv <= 0 {
			continue
		}
		compared++
		assert.LessOrEqual(t, absFloat(*r.MainNet), 3*tv,
			"%s 主力净额 %v 与该日成交额 %v 不同量纲 —— 单位口径在这里被真库读数质问"+
				"（错 100/1000 倍会直接崩这条）", key, *r.MainNet, tv)
	}
	assert.Greater(t, compared, 0,
		"一条对照都没做成 ⇒ 这条腿是空转，别把它当证据（capital_flow 与日线没有共同交易日？）")

	assert.Equal(t, "eastmoney", readBack[0].Source, "source 列要如实记链上真正供数的适配器")
	// period 钉的是执行器归一出来的那一种，不是 DataType 名。
	//
	// ⚠️ 这一条原本写的是 `source.DataTypeCapitalFlow`（= "capital_flow"），
	// 与今天的代码不符：`capitalFlowExecutor` 把 period 归一成
	// `capitalFlowDefaultPeriod = "daily"`（cmd/data/capital_flow_executor.go:36
	// 与 normalizeCapitalFlowPeriod）再传给链上任何一员。这条腿从没执行到过那个
	// 断言 —— 东财那条 path 本机不可达，执行在 Execute 处就 Fatal 了（见文件头
	// 的 2026-10-16 首跑读数）。DBT-09（2026-10-19）接上 Tushare 腿后，
	// `dbt09_moneyflow_live_test.go` 用真库读数把 `period` 实测成了 "daily"，
	// 这里的错期望才暴露出来。`period` 是冲突键 (symbol, trade_date, period)
	// 的一部分，两个源各写一种就会让同一天并排落两行、读侧拿到重复且无声。
	assert.Equal(t, capitalFlowDefaultPeriod, readBack[0].Period,
		"period 必须由执行器归一，不许由各 adapter 自己填")
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
