//go:build integration
// +build integration

package backtest

// QT-64 验收⑤：真 PG + 1m bar 的集成腿。
//
// 两条腿共用同一份日线与同一个策略名（同一策略的两个实例）：
//   - 正腿：挂上 `marketdata.NewRouter(真连接池)`，跑一次 IntradayAware
//     策略 —— 日志必须出现 `Intraday snapshot warm-up completed (D6)`，
//     且 `minute_bars > 0`（分钟数据真的从库里取到了）、有成交；
//   - 反腿：摘掉 Router —— 同一策略**明确失败**（策略拿不到供给就报错，
//     引擎记 `Failed to get signals, skipping day`），成交为 0，且**不
//     出现**预热完成日志。这是「缺失≠0、不静默全零信号」的执行体。
//
// DB 契约沿 REV-21：`TEST_DATABASE_DSN` 未设置 → 显式跳过；设置但连不上
// → fail-loud。分钟表 `ohlcv_minute` 由迁移创建（与 pkg/marketdata 的
// router 集成测试同一前提）。

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"tellurion/pkg/marketdata"
	"tellurion/pkg/strategy"
	"tellurion/pkg/testenv"
)

// qt64Buffer 让 zerolog 的并发写入在测试下不竞态。
type qt64Buffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *qt64Buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *qt64Buffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// qt64Engine 与 d6Engine 同构，唯一差别是 logger 可注入（要断言日志）。
func qt64Engine(t *testing.T, daily map[string][]domain.OHLCV, logger zerolog.Logger) *Engine {
	t.Helper()

	prov := marketdata.NewInMemoryProvider()
	for sym, bars := range daily {
		prov.LoadOHLCV(sym, bars)
	}
	prov.SetTradingDays([]time.Time{d6Day, d6Day.AddDate(0, 0, 1), d6Day.AddDate(0, 0, 2)})
	prov.LoadStocks([]domain.Stock{
		{Symbol: "600519.SH", Name: "贵州茅台", ListDate: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)},
		{Symbol: "000001.SZ", Name: "平安银行", ListDate: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)},
	})

	eng, err := NewEngine(viperD6(), prov, logger)
	require.NoError(t, err)
	eng.LoadOHLCVInMemory(daily)
	eng.SetRiskManager(d6RiskManager(t))
	eng.SetParallelWorkers(1)
	return eng
}

func TestQT64_RouterWiring_Integration(t *testing.T) {
	const sym = "600519.SH"
	const stratName = "qt64_router_integration"

	pool := testenv.OpenPoolForTest(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, "DELETE FROM ohlcv_minute WHERE symbol = $1", sym); err != nil {
		t.Fatalf("清理 ohlcv_minute 失败（表是否由迁移创建？）: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM ohlcv_minute WHERE symbol = $1", sym)
	})

	// 01-02 开盘半小时：首开 10.00 → 末收 10.50，街动量 +0.05（> 阈值 0.02）。
	minuteBars := d6OpenHalfHourBars(sym)
	for _, b := range minuteBars {
		_, err := pool.Exec(ctx, `
			INSERT INTO ohlcv_minute (symbol, ts, open, high, low, close, volume, amount)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			sym, b.Date, b.Open, b.High, b.Low, b.Close, int64(b.Volume), b.Open*float64(b.Volume))
		require.NoError(t, err)
	}

	daily := map[string][]domain.OHLCV{
		sym: {
			d6DailyBar(sym, d6Day, 10.00, 10.60, 9.98, 10.50),
			d6DailyBar(sym, d6Day.AddDate(0, 0, 1), 10.50, 10.55, 10.20, 10.22),
			d6DailyBar(sym, d6Day.AddDate(0, 0, 2), 10.22, 10.30, 10.10, 10.15),
		},
	}

	require.NoError(t, strategy.DefaultRegistry.RegisterFactory(func() strategy.Strategy {
		return newIntradayMomentumStrategy(stratName, 0.02)
	}))

	req := BacktestRequest{
		Strategy:       stratName,
		StockPool:      []string{sym},
		StartDate:      "2024-01-02",
		EndDate:        "2024-01-04",
		InitialCapital: 1000000,
		RiskFreeRate:   0.03,
	}

	// ── 正腿：Router 挂上，D6 预热真的取到分钟数据 ──────────────────
	var withRouter qt64Buffer
	loggerWith := zerolog.New(&withRouter)
	engWith := qt64Engine(t, daily, loggerWith)
	engWith.SetRouter(marketdata.NewRouter(pool))

	respWith, err := engWith.RunBacktest(ctx, req)
	require.NoError(t, err)
	logsWith := withRouter.String()
	assert.Contains(t, logsWith, "Intraday snapshot warm-up completed (D6)",
		"接了 Router 就必须真的跑预热（D6 通道）")
	assert.Regexp(t, `"minute_bars":([1-9][0-9]*)`, logsWith,
		"预热取到的分钟 bar 数必须 > 0（真 PG 里灌了 30 根）")
	assert.NotZero(t, respWith.TotalTrades,
		"拿到日内特征后样例策略应产生成交（否则特征根本没到策略）")

	// ── 反腿：摘掉 Router，同一策略明确失败（不是静默全零信号）──────
	// QT-10 起策略故障（ErrStrategyFault）终止 run：Status=failed + error
	// 点名日期/类/原因；不会再走「跳天继续」的老路。
	var noRouter qt64Buffer
	loggerWithout := zerolog.New(&noRouter)
	engWithout := qt64Engine(t, daily, loggerWithout)

	respWithout, err := engWithout.RunBacktest(ctx, req)
	require.Error(t, err, "策略故障必须终止 run（QT-10）")
	require.NotNil(t, respWithout)
	assert.Equal(t, StatusFailed, respWithout.Status)
	assert.Zero(t, respWithout.TotalTrades, "没有日内供给就不得凭空成交")
	assert.Contains(t, respWithout.Error, "2024-01-02", "Failures 必须点名失败现场日期")
	assert.Contains(t, respWithout.Error, "strategy_fault", "Failures 必须点名失败类")
	logsWithout := noRouter.String()
	assert.Contains(t, logsWithout, "no IntradayFeatureProvider injected",
		"失败原因必须点名「没有日内供给」（缺失≠0）")
	assert.NotContains(t, logsWithout, "Intraday snapshot warm-up completed (D6)",
		"没有 Router 不该有预热完成日志")
}
