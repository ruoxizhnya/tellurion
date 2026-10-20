package main

// DBT-07 ②：stock_fundamentals 灌数腿（真库 + 真 Tushare 链路）。
//
// 2026-10-17 实测：fina_indicator 接口在当前 token 下**可用**（真库
// 探测留证：600000.SH 20240930 → roe=4.8669），推翻「免费额度不可得」
// 的旧口径。按处置判据「可得 → 走既有同步链路灌数」：装配生产同构的
// fundamentalsExecutor（TushareClient 带 store → FetchFundamentalsData
// 内部 SaveFundamentalDataBatch → stock_fundamentals），灌 2 只票并断言
// 落库。这就是「既有链路可同步」的可重复证明——不新写同步代码。
//
// 双 skip 门槛：TEST_DATABASE_DSN（真库）或 TUSHARE_TOKEN（上游凭据）
// 任一缺失即跳过——本测试是演练腿，不是 CI 必需路径。

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/data"
	"tellurion/pkg/storage"
	"tellurion/pkg/sync"
	"tellurion/pkg/testenv"
)

// dbt07NoopProgress 是 sync.ProgressReporter 的空实现（灌数腿不关心进度）。
type dbt07NoopProgress struct{}

func (p *dbt07NoopProgress) ReportProgress(_, _, _ int) {}
func (p *dbt07NoopProgress) ReportError(_ string)       {}

var _ sync.ProgressReporter = (*dbt07NoopProgress)(nil)

func TestDBT07_FundamentalsSync_FillsStockFundamentals(t *testing.T) {
	dsn := testenv.DatabaseDSN()
	if dsn == "" {
		testenv.SkipNoDB(t)
	}
	token := os.Getenv("TUSHARE_TOKEN")
	if token == "" {
		t.Skip("TUSHARE_TOKEN 未设置——上游凭据缺失，灌数腿跳过")
	}

	store, err := storage.NewPostgresStoreConnected(context.Background(), dsn)
	require.NoError(t, err, "TEST_DATABASE_DSN 已设置但连不上（fail-loud）")
	t.Cleanup(store.Close)
	ctx := context.Background()

	symbols := []string{"600000.SH", "000001.SZ"}
	params, err := json.Marshal(map[string]any{
		"symbols":    symbols,
		"start_date": "20230101",
		"end_date":   "20260101",
	})
	require.NoError(t, err)

	// 灌前基线（重复执行的幂等基准）。
	baseline := dbt07FundamentalsRows(t, dsn, symbols)

	// 生产同构链路：TushareClient（带 store，FetchFundamentalsData 内部
	// SaveFundamentalDataBatch 落 stock_fundamentals）→ fundamentalsExecutor。
	tc := data.NewTushareClient(token, "http://api.tushare.pro", 3, store, nil)
	exec := &fundamentalsExecutor{tc: tc, store: store}
	job := &sync.Job{
		ID:      "dbt07-fill-" + time.Now().UTC().Format("150405"),
		JobType: sync.JobTypeFundamentals,
		Status:  sync.JobStatusRunning,
		Params:  params,
	}
	_, err = exec.Execute(ctx, job, &dbt07NoopProgress{})
	require.NoError(t, err, "fundamentals 同步作业执行失败（fina_indicator 权限/网络？）")

	after := dbt07FundamentalsRows(t, dsn, symbols)
	// 断言语义：链路落库能力 + 幂等。落库走 ON CONFLICT (ts_code, trade_date)
	// DO UPDATE —— 首跑新增（baseline→after 增长），重跑 no-op（相等）。
	// 两种形态都合法；唯一不许出现的是「跑完仍 0 行」（链路空转）或丢行。
	assert.GreaterOrEqual(t, after, baseline, "重灌不得丢行")
	assert.Greater(t, after, 0, "既有链路灌数后 stock_fundamentals 必须有数据（DBT-07 可得→灌数处置）")
	t.Logf("[dbt07 留证] stock_fundamentals rows for %v: baseline=%d after=%d",
		symbols, baseline, after)
}

// dbt07FundamentalsRows 直查 stock_fundamentals 表行数（测试自建连接——
// 不能用 GetFundamentalsSnapshot 当代理：它带 `AND pe IS NOT NULL` 的
// value 因子语义，而 fina_indicator 对银行股不产 pe（实测列剔除形态），
// 快照视角恒 0 行 ≠ 表内没数据）。
func dbt07FundamentalsRows(t *testing.T, dsn string, symbols []string) int {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	defer pool.Close()

	var n int
	err = pool.QueryRow(context.Background(),
		`SELECT count(*) FROM stock_fundamentals WHERE ts_code = ANY($1)`, symbols).Scan(&n)
	require.NoError(t, err)
	return n
}
