package backtest

// QT-51（BE-02）行为锁：**挂不挂实盘通道，回测结果逐位相同**。
//
// 结构护栏（internal/repoguard/backtest_live_isolation_test.go）保证
// `pkg/backtest` 不再 import `pkg/live`；本文件补行为腿：
//
//  1. 同一策略、同一输入跑两遍——一遍不挂 port，一遍挂一个
//     「任何方法被调用就计数并报错」的 port；
//  2. 两次结果（指标 + 净值曲线 + 成交序列）的 SHA-256 摘要必须相同；
//  3. port 的调用计数必须为 0——回测主循环根本不碰出站通道。
//
// 这三条合起来就是"改 live 不改回测结果"的可执行证据。

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"tellurion/pkg/strategy"
	"tellurion/pkg/strategy/examples"
)

// qt51ForbiddenPort 是"一旦被调用就证明串线"的哨兵 port。
type qt51ForbiddenPort struct {
	calls int64
}

func (p *qt51ForbiddenPort) SubmitOrder(ctx context.Context, req OrderRequest) (*OrderResult, error) {
	atomic.AddInt64(&p.calls, 1)
	return nil, fmt.Errorf("QT-51: 回测主循环不应调用 ExecutionPort.SubmitOrder")
}

func (p *qt51ForbiddenPort) CancelOrder(ctx context.Context, orderID string) error {
	atomic.AddInt64(&p.calls, 1)
	return fmt.Errorf("QT-51: 回测主循环不应调用 ExecutionPort.CancelOrder")
}

func (p *qt51ForbiddenPort) GetOrder(ctx context.Context, orderID string) (*OrderResult, error) {
	atomic.AddInt64(&p.calls, 1)
	return nil, fmt.Errorf("QT-51: 回测主循环不应调用 ExecutionPort.GetOrder")
}

func (p *qt51ForbiddenPort) Name() string                          { return "qt51-forbidden" }
func (p *qt51ForbiddenPort) HealthCheck(ctx context.Context) error { return nil }

func (p *qt51ForbiddenPort) Calls() int64 { return atomic.LoadInt64(&p.calls) }

// qt51Digest 对回测结果做逐位摘要：所有 float 取 IEEE-754 位模式，
// 避免十进制格式化掩盖 1 ulp 的漂移。
func qt51Digest(resp *BacktestResponse) string {
	h := sha256.New()
	f := func(name string, v float64) {
		fmt.Fprintf(h, "%s=%016x\n", name, math.Float64bits(v))
	}
	f("TotalReturn", resp.TotalReturn)
	f("AnnualReturn", resp.AnnualReturn)
	f("SharpeRatio", resp.SharpeRatio)
	f("SortinoRatio", resp.SortinoRatio)
	f("MaxDrawdown", resp.MaxDrawdown)
	f("WinRate", resp.WinRate)
	f("AvgHoldingDays", resp.AvgHoldingDays)
	f("CalmarRatio", resp.CalmarRatio)
	fmt.Fprintf(h, "TotalTrades=%d\n", resp.TotalTrades)
	for _, p := range resp.PortfolioValues {
		fmt.Fprintf(h, "pv=%s/%016x\n", p.Date.UTC().Format("2006-01-02T15:04:05Z"), math.Float64bits(p.TotalValue))
	}
	for _, tr := range resp.Trades {
		fmt.Fprintf(h, "tr=%s/%s/%016x/%016x/%016x/%s\n",
			tr.Symbol, string(tr.Direction), math.Float64bits(tr.Quantity),
			math.Float64bits(tr.Price), math.Float64bits(tr.Commission),
			tr.Timestamp.UTC().Format("2006-01-02T15:04:05Z"))
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// runQT51Momentum 用固定输入跑一遍 momentum 回测。
func runQT51Momentum(t *testing.T, eng *Engine, symbols []string) *BacktestResponse {
	t.Helper()
	const nDays = 120

	ms := examples.NewMomentumStrategy()
	params := map[string]interface{}{
		"lookback_days":       20,
		"top_n":               5,
		"max_positions":       5,
		"rebalance_frequency": "daily",
	}
	if err := ms.Configure(params); err != nil {
		t.Fatalf("configure momentum: %v", err)
	}
	if err := strategy.GlobalRegisterFactory(func() strategy.Strategy { return ms }); err != nil {
		existing, getErr := strategy.DefaultRegistry.Get("momentum")
		if getErr != nil {
			t.Fatalf("register/get momentum: %v / %v", err, getErr)
		}
		cfg, ok := existing.(strategy.Configurable)
		if !ok {
			t.Fatal("momentum not configurable")
		}
		if err := cfg.Configure(params); err != nil {
			t.Fatalf("reconfigure momentum: %v", err)
		}
	}

	start := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	resp, err := eng.RunBacktest(context.Background(), BacktestRequest{
		Strategy:       "momentum",
		StockPool:      symbols,
		StartDate:      start.Format("2006-01-02"),
		EndDate:        start.AddDate(0, 0, nDays-1).Format("2006-01-02"),
		InitialCapital: 1_000_000,
		RiskFreeRate:   0.03,
	})
	if err != nil {
		t.Fatalf("backtest: %v", err)
	}
	if resp.Status != "completed" {
		t.Fatalf("status=%s err=%s", resp.Status, resp.Error)
	}
	return resp
}

// TestQT51_BacktestResultIndependentOfExecutionPort 是行为锁。
func TestQT51_BacktestResultIndependentOfExecutionPort(t *testing.T) {
	eng, _, symbols := buildSyntheticEngine(t, 20, 120, 1)

	withoutPort := runQT51Momentum(t, eng, symbols)

	port := &qt51ForbiddenPort{}
	eng.SetLiveTrader(port)
	withPort := runQT51Momentum(t, eng, symbols)

	d1, d2 := qt51Digest(withoutPort), qt51Digest(withPort)
	if d1 != d2 {
		t.Fatalf("QT-51 行为锁破了：挂 port 前后回测摘要不同\n  no-port   = %s\n  with-port = %s", d1, d2)
	}
	if got := port.Calls(); got != 0 {
		t.Fatalf("QT-51 行为锁破了：回测主循环调用了 ExecutionPort %d 次 —— 出站通道不该参与撮合", got)
	}
	t.Logf("QT51_DIGEST=%s trades=%d pv=%d port_calls=0", d1, withoutPort.TotalTrades, len(withoutPort.PortfolioValues))
}
