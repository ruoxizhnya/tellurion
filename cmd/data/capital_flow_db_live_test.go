//go:build data10live || dbt09live || dbt10live

package main

// 这一段是三条 live 腿共用的真库**读回**夹具（tag data10live / dbt09live / dbt10live）。
//
// 它原来住在 capital_flow_job_test.go 里，H4 ① 之后单拎出来挂在建 tag 上：
// 默认构建的作业层测试不再读库 —— 写面出了进程，core 这一侧能证的形状变成
// 「投出去的那一批点逐字段对得上」。留着就得让 pkg/storage 与 pkg/testenv
// 陪着一个默认套件里没人调的函数一起进测试二进制。
//
// 读回判据本身没变：按探针 symbol 收窄的 SELECT，不是全表扫；连不上库时
// testenv.SkipNoDB 显式跳过（设了 DSN 却连不上则 fail-loud）。

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"tellurion/pkg/storage"
	"tellurion/pkg/testenv"
)

func capitalFlowStore(t *testing.T) *storage.PostgresStore {
	t.Helper()
	dsn := testenv.DatabaseDSN()
	if dsn == "" {
		testenv.SkipNoDB(t)
	}
	store, err := storage.NewPostgresStoreConnected(context.Background(), dsn)
	if err != nil {
		t.Fatalf("[testdb] TEST_DATABASE_DSN 已设置但连不上（fail-loud）: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

type capitalFlowRow struct {
	Symbol      string
	TradeDate   time.Time
	Period      string
	MainNet     *float64
	SuperNet    *float64
	LargeNet    *float64
	MediumNet   *float64
	SmallNet    *float64
	MainRatio   *float64
	RetailNet   *float64
	RetailRatio *float64
	ClosePrice  *float64
	ChangePct   *float64
	MainBuy     *float64
	MainSell    *float64
	Source      string
	IngestTime  time.Time
}

// readCapitalFlowRows 按 symbol 白名单读回。原来它写死成探针那两票
// （888888.SH / 888889.SH），而真源腿跑的是 600519.SH —— 那个组合永远读回 0 行，
// 于是 `require.NotEmpty(readBack)` 必红。这条腿自 2026-10-16 起从没执行到过
// 那一行（东财 path 在 Execute 处就 Fatal 了），所以洞一直没现形。
// 改成逐调用方给 symbol，是为了让「读回为空」重新成为一个有意义的失败。
func readCapitalFlowRows(t *testing.T, store *storage.PostgresStore, symbols ...string) []capitalFlowRow {
	t.Helper()
	require.NotEmpty(t, symbols, "读回必须按 symbol 收窄，绝不写全表扫描 —— 共享开发库")
	rows, err := store.DB().Query(context.Background(),
		`SELECT symbol, trade_date, period, main_net, super_net, large_net, medium_net, small_net,
		        main_net_ratio, retail_net, retail_net_ratio, close_price, change_pct,
		        main_buy_amount, main_sell_amount, source, ingest_time
		 FROM capital_flow WHERE symbol = ANY($1) ORDER BY symbol, trade_date`,
		symbols)
	require.NoError(t, err)
	defer rows.Close()

	var out []capitalFlowRow
	for rows.Next() {
		var r capitalFlowRow
		require.NoError(t, rows.Scan(&r.Symbol, &r.TradeDate, &r.Period, &r.MainNet, &r.SuperNet,
			&r.LargeNet, &r.MediumNet, &r.SmallNet, &r.MainRatio, &r.RetailNet, &r.RetailRatio,
			&r.ClosePrice, &r.ChangePct, &r.MainBuy, &r.MainSell, &r.Source, &r.IngestTime))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

// liveIngestPoster 给三条真源腿装出**真的**跨仓写面客户端（H4 ① 之后 core 不落库，
// 这三条腿于是才是「真源 → 真作业 → 真 data-service → 真库」的一整条 ——
// 读回仍然由测试自己查，那是读，不是写）。
//
// 缺 `H4_LIVE_INGEST_URL` 时显式 skip：与 testenv.SkipNoDB 同一条纪律 ——
// 外部条件不在就是「这条腿没跑」，不是「这条腿过了」。
// token 走 `INGEST_WRITE_TOKEN`，与部署里给 core 的那一份是同一个键；
// 对端档位表只开 service / admin（`tellurion-data:docs/api-schema.md` §1）。
func liveIngestPoster(t *testing.T) pointsPoster {
	t.Helper()
	base := strings.TrimSpace(os.Getenv("H4_LIVE_INGEST_URL"))
	if base == "" {
		t.Skip("H4_LIVE_INGEST_URL 未设置：写面在另一个进程里，这条跨进程腿没有对端可投")
	}
	client := newIngestWriteClient(ingestWriteConfig{
		BaseURL: base,
		Token:   strings.TrimSpace(os.Getenv("INGEST_WRITE_TOKEN")),
		Timeout: 30 * time.Second,
	})
	require.NotNil(t, client, "H4_LIVE_INGEST_URL 非空却没造出客户端（装配自证失败）")
	return client
}
