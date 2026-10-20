//go:build dbt10live

package main

// DBT-10 的真源腿：top_list（龙虎榜）第一次接进作业层之后，拿真源 + 真库走一次。
//
//	go test ./cmd/data/ -tags=dbt10live -run TestDBT10TopListLive -count=1 -v
//
// 与 `data10live` / `dbt09live` 同一个判断：可达性与上游作息不归本仓决定，
// 挂进 CI 等于把「东财今天出不出榜」变成每天的红。但这条链**今天可达**
// （datacenter-web.eastmoney.com/api/data/v1/get 实测 200），所以这条腿是
// 能跑绿的那一条 —— 它挡的是「httptest 替身全绿、真源一行没有」那一族。
//
// 钉的是五件事，其中三条与「源侧返回值形状」有关，因此不能靠读码替代：
//  1. 报成功就必须真落了行数（DATA-06 那一族）；
//  2. 交易所后缀：源给的是 6 位裸代码，落库必须是带后缀的规范形态，
//     且后缀与代码段一致（'6'/'9' → .SH，其余 → .SZ）。DBT-10 派工前怀疑
//     过这里（`eastmoney_sectors_adapter.go:492-499`），真库读数是唯一能
//     把它从「怀疑」变成「结论」的东西；
//  3. 窗口由**请求**决定而不是由源决定：落了库的 trade_date 必须都在
//     请求窗口内。源是分页榜单、默认排序第 1 页 —— 窗口不守就是 CR-41
//     静默截断的另一个方向；
//  4. `net_buy` 与 `buy_amount`/`sell_amount` 的量纲关系（同源三列，
//     换算错会当场崩）；
//  5. `name` 不许是空串 —— DDL 的 NOT NULL 挡得住 NULL 挡不住空串，
//     normalizer 那关（top_list_normalizer.go:53）在真源上再核一次。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/logging"
	"tellurion/pkg/storage"
	"tellurion/pkg/sync"
)

const dbt10WindowDays = 3

type dbt10Row struct {
	TradeDate  time.Time
	Symbol     string
	Name       string
	NetBuy     *float64
	BuyAmount  *float64
	SellAmount *float64
	Turnover   *float64
	Reason     *string
	Source     string
}

func TestDBT10TopListLiveFillsTopList(t *testing.T) {
	store := capitalFlowStore(t)
	ctx := context.Background()

	// 右端取库里最近一个真有日线的交易日：把作业派给一个源侧还没出数的日子，
	// 失败是日历问题不是接线问题，那条腿会假红。
	end := dbt10ReferenceDate(t, ctx, store)
	start := end.AddDate(0, 0, -dbt10WindowDays)

	// 生产链，tushare 传 nil —— top_list 这条链上只有 eastmoney_toplist 一员。
	reg := buildDataSourceRegistry(nil, logging.Logger)
	exec := &topListExecutor{reg: reg, persist: store}

	params, err := json.Marshal(map[string]any{
		"start_date": start.Format("20060102"),
		"end_date":   end.Format("20060102"),
	})
	require.NoError(t, err)
	job := &sync.Job{
		ID:         "dbt10-live",
		JobType:    sync.JobTypeTopList,
		Status:     sync.JobStatusRunning,
		Params:     params,
		MaxRetries: 3,
	}

	res, err := exec.Execute(ctx, job, &recordingReporter{})
	if err != nil {
		t.Logf("真源取数失败：%v", err)
		assert.NotContains(t, err.Error(), "nil registry", "不许把装配问题误报成源问题")
		t.Fatalf("DBT-10 真源腿未拿到数据，其余断言无从执行：%v", err)
	}
	got := res.(map[string]any)
	persisted, _ := got["persisted"].(int)
	assert.Greater(t, persisted, 0,
		"Execute 报成功却没落一行 ⇒ 静默空表（DATA-06 那一族）在作业层复发")

	rows := dbt10ReadBack(t, ctx, store, start, end)
	require.NotEmpty(t, rows, "落库后必须能读回来")

	suffixChecked := 0
	ratioChecked := 0
	for _, r := range rows {
		// 断言 2：规范形态 + 后缀与代码段一致。
		code, suffix, ok := strings.Cut(r.Symbol, ".")
		require.True(t, ok, "%q 不是带交易所后缀的规范形态（源给裸代码，翻译层必须补上）", r.Symbol)
		switch suffix {
		case "SH", "SZ", "BJ":
			if suffix == "SH" {
				assert.Contains(t, []string{"6", "9"}, string(code[0]),
					"%s 后缀 .SH 与代码段 %s 不符", r.Symbol, code)
			}
		default:
			t.Errorf("%q 的后缀 %q 不是本仓认识的交易所段", r.Symbol, suffix)
		}
		suffixChecked++

		// 断言 3：窗口由请求决定。
		assert.False(t, r.TradeDate.Before(start.Truncate(24*time.Hour)),
			"%s 落在请求窗口 %s~%s 之外 ⇒ 窗口是源在决定，不是执行器",
			r.Symbol, start.Format("2006-01-02"), end.Format("2006-01-02"))

		// 断言 4：同源三列的量纲关系。
		if r.NetBuy != nil && r.BuyAmount != nil && r.SellAmount != nil {
			ratioChecked++
			assert.LessOrEqual(t, abs64(*r.NetBuy), abs64(*r.BuyAmount)+abs64(*r.SellAmount),
				"%s %s net_buy=%v 超出 buy(%v)+sell(%v) ⇒ 三者不同量纲或符号口径错了",
				r.Symbol, r.TradeDate.Format("2006-01-02"),
				*r.NetBuy, *r.BuyAmount, *r.SellAmount)
		}

		// 断言 5：NOT NULL 挡不住空串。
		assert.NotEmpty(t, strings.TrimSpace(r.Name), "%s 票面名为空串", r.Symbol)

		assert.Equal(t, "eastmoney_toplist", r.Source,
			"source 列必须如实记供数的那一员")
	}
	assert.Greater(t, suffixChecked, 0, "一条后缀都没查 ⇒ 这条腿是空转")
	assert.Greater(t, ratioChecked, 0,
		"没有一行同时给出 net_buy/buy/sell ⇒ 量纲那条断言今天没被执行到，别把它当证据")

	t.Logf("DBT-10 真源腿：persisted=%d 读回=%d 后缀核对=%d 量纲核对=%d 窗口=%s~%s",
		persisted, len(rows), suffixChecked, ratioChecked,
		start.Format("2006-01-02"), end.Format("2006-01-02"))
}

// dbt10ReferenceDate 取库里最近一个真有日线的交易日（全池 max，不绑单只票 ——
// 龙虎榜是整市场榜单，需要的是一个**大概率出过榜**的日子）。
func dbt10ReferenceDate(t *testing.T, ctx context.Context, store *storage.PostgresStore) time.Time {
	t.Helper()
	var d time.Time
	err := store.DB().QueryRow(ctx, `SELECT max(trade_date) FROM ohlcv_daily_qfq`).Scan(&d)
	require.NoError(t, err)
	return d.UTC()
}

func dbt10ReadBack(t *testing.T, ctx context.Context, store *storage.PostgresStore, start, end time.Time) []dbt10Row {
	t.Helper()
	rows, err := store.DB().Query(ctx,
		`SELECT trade_date, symbol, name, net_buy, buy_amount, sell_amount,
		        turnover, reason, source
		 FROM top_list WHERE trade_date BETWEEN $1 AND $2
		 ORDER BY trade_date DESC, symbol LIMIT 200`,
		start, end)
	require.NoError(t, err)
	defer rows.Close()
	var out []dbt10Row
	for rows.Next() {
		var r dbt10Row
		require.NoError(t, rows.Scan(&r.TradeDate, &r.Symbol, &r.Name,
			&r.NetBuy, &r.BuyAmount, &r.SellAmount, &r.Turnover, &r.Reason, &r.Source))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

func abs64(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
