package backtest

// QT-11 · 引擎侧的 per-run 实例取证。
//
// A2 缺陷（p0-remediation-design.md）在引擎层的可观察形式：同一策略连续跑
// 两笔，第二笔继承第一笔的递推状态 —— 净值曲线照样「正常」，但两次结果不
// 一样，且没人能从曲线上看出第二笔是从第 41 根 bar 开始算的。
//
// 两条腿：
//   - 连续两笔（各自引擎）逐位一致，且每笔各拿到一份从零开始数的新实例；
//   - 并发两笔（**同一个共享 Engine**、不同票池）互不串：没有任何一份实例
//     同时见到两个票池的标的。
//
// 破坏验证（改坏 → 变红 → 还原，实测见 TASKS.md 施工记录）：
//   - Registry.NewInstance 改成返回 entry.meta（注册期那份）→ 本文件两条腿
//     与 pkg/strategy 的 provider 实例测试同时红；
//   - Engine.newRunSignalProvider 改成返回引擎级共享 provider → 本文件两条
//     腿红 —— 那正是拆仓前「共享 Engine 串状态」注释所指的位置。

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/stretchr/testify/require"
	"tellurion/pkg/strategy"
)

// prProbe 把状态写在实例上：每根 bar 计数 +1，计到 3 的倍数时买一次。
// 实例若跨 run 存活，第二笔的「第 3 根」落在完全不同的日期上。
type prProbe struct {
	name  string
	count map[string]int
	syms  map[string]struct{}
	out   []strategy.Signal
}

func newPRProbe(name string) *prProbe {
	return &prProbe{name: name, count: map[string]int{}, syms: map[string]struct{}{}}
}

func (p *prProbe) Name() string                            { return p.name }
func (p *prProbe) Description() string                     { return "per-run instance probe" }
func (p *prProbe) Parameters() []strategy.Parameter        { return nil }
func (p *prProbe) Configure(map[string]interface{}) error  { return nil }
func (p *prProbe) Cleanup()                                {}
func (p *prProbe) Weight(strategy.Signal, float64) float64 { return 1 }
func (p *prProbe) GenerateSignals(context.Context, map[string][]domain.OHLCV, *domain.Portfolio) ([]strategy.Signal, error) {
	return nil, nil
}

// Warmup 声明 1：与 eqStreamStrategy 同形，引擎不设 warmup 门（裁决见
// signal_provider.go 注释），这里只为把「本实例见过几根 bar」如实留在计数里。
func (p *prProbe) Warmup() int { return 1 }

func (p *prProbe) OnBar(_ context.Context, bar domain.OHLCV) error {
	p.count[bar.Symbol]++
	p.syms[bar.Symbol] = struct{}{}
	if p.count[bar.Symbol]%3 == 0 {
		p.out = append(p.out, strategy.Signal{
			Symbol: bar.Symbol, Action: "buy", Date: bar.Date, Price: bar.Close, Strength: 1,
		})
	}
	return nil
}

func (p *prProbe) Signals() []strategy.Signal {
	out := p.out
	p.out = nil
	return out
}

func (p *prProbe) SaveState() ([]byte, error) { return nil, nil }
func (p *prProbe) LoadState([]byte) error     { return nil }

func (p *prProbe) totalBars() int {
	n := 0
	for _, c := range p.count {
		n += c
	}
	return n
}

var (
	_ strategy.Strategy   = (*prProbe)(nil)
	_ strategy.BarHandler = (*prProbe)(nil)
)

// prTracker 是注册工厂 + 实例登记簿。并发两笔会同时调工厂，所以它自己加锁。
//
// built 的第 0 项是**注册期原型**（RegisterFactory 要构造一次才能拿到名字），
// 从第 1 项起才是各 run 经 NewInstance 拿到的实例。
type prTracker struct {
	mu    sync.Mutex
	name  string
	built []*prProbe
}

func (tr *prTracker) factory() strategy.Strategy {
	p := newPRProbe(tr.name)
	tr.mu.Lock()
	tr.built = append(tr.built, p)
	tr.mu.Unlock()
	return p
}

// runInstances 返回**真跑过 bar** 的实例（即各 run 经 provider 拿到的那些）。
//
// 刻意按「见过 bar」筛而不是按下标：注册期原型与两处预热探针
// （warmFundamentalsIfNeeded / warmIntradayIfNeeded 各 NewInstance 一次做
// 能力探测）都不喂 bar。判据本身不变 —— 每笔 run 一份、每份只喂自己的量。
func (tr *prTracker) runInstances() []*prProbe {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	var out []*prProbe
	for _, p := range tr.built {
		if p.totalBars() > 0 {
			out = append(out, p)
		}
	}
	return out
}

// constructed 保留在注释里说明的口径：本测试只关心「真跑过 bar 的实例」，
// 注册期原型与两处能力探测探针都不喂 bar，因此不参与断言。

const (
	prStartDay = "2024-01-02"
	prDays     = 20
)

// prRequest 是两笔腿共用的请求形状（日期终点按 nDays 算，避免区间与数据不齐）。
func prRequest(name string, pool []string, nDays int) BacktestRequest {
	end := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC).AddDate(0, 0, nDays-1)
	return BacktestRequest{
		Strategy:       name,
		StockPool:      pool,
		StartDate:      prStartDay,
		EndDate:        end.Format("2006-01-02"),
		InitialCapital: 1_000_000,
		RiskFreeRate:   0.03,
	}
}

// prAssertEqualResults 逐位比对两笔结果（口径同 engine_reproducibility_test.go
// 的可复现断言；成交 ID 可能自带随机成分，不参与比对）。
func prAssertEqualResults(t *testing.T, tag string, a, b *BacktestResponse) {
	t.Helper()

	for _, s := range []struct {
		name string
		x, y float64
	}{
		{"TotalReturn", a.TotalReturn, b.TotalReturn},
		{"AnnualReturn", a.AnnualReturn, b.AnnualReturn},
		{"SharpeRatio", a.SharpeRatio, b.SharpeRatio},
		{"MaxDrawdown", a.MaxDrawdown, b.MaxDrawdown},
	} {
		require.Equal(t, s.y, s.x, "%s：%s 不可复现（第一笔 vs 第二笔）", tag, s.name)
	}
	require.Equal(t, a.TotalTrades, b.TotalTrades, "%s：成交笔数不可复现", tag)
	require.Len(t, b.PortfolioValues, len(a.PortfolioValues), "%s：净值曲线长度不同", tag)
	for i := range a.PortfolioValues {
		require.Equal(t, b.PortfolioValues[i].TotalValue, a.PortfolioValues[i].TotalValue,
			"%s：净值第 %d 点不可复现", tag, i)
	}
	require.Len(t, b.Trades, len(a.Trades), "%s：成交条数不同", tag)
	for i := range a.Trades {
		require.Equal(t, b.Trades[i].Symbol, a.Trades[i].Symbol, "%s：第 %d 笔标的不同", tag, i)
		require.Equal(t, b.Trades[i].Quantity, a.Trades[i].Quantity, "%s：第 %d 笔数量不同", tag, i)
		require.Equal(t, b.Trades[i].Price, a.Trades[i].Price, "%s：第 %d 笔价格不同", tag, i)
	}
}

func prRunCompleted(t *testing.T, eng *Engine, name string, pool []string, nDays int) *BacktestResponse {
	t.Helper()
	resp, err := eng.RunBacktest(context.Background(), prRequest(name, pool, nDays))
	require.NoError(t, err)
	require.Equal(t, "completed", resp.Status, "status=%s err=%s", resp.Status, resp.Error)
	return resp
}

// TestEngine_SequentialRunsGetSeparateInstances 验收「连续两笔逐位一致」，
// 并且带一条正证据：两笔的实例各自从零开始数 bar（不是同一份、也不是
// 一份被喂了两遍）。
func TestEngine_SequentialRunsGetSeparateInstances(t *testing.T) {
	const name = "qt11_sequential_probe"

	tr := &prTracker{name: name}
	require.NoError(t, strategy.GlobalRegisterFactory(tr.factory))

	// 第一笔（全新引擎 + 合成数据，同种子 ⇒ 输入相同）。
	engA, _, symbolsA := buildSyntheticEngine(t, 3, prDays, 1)
	first := prRunCompleted(t, engA, name, symbolsA, prDays)

	// 第二笔：同输入再跑一次。
	engB, _, symbolsB := buildSyntheticEngine(t, 3, prDays, 1)
	second := prRunCompleted(t, engB, name, symbolsB, prDays)

	require.NotEmpty(t, first.Trades, "探针必须真下过单，否则「两次一致」是空洞的")
	prAssertEqualResults(t, "连续两笔", first, second)

	inst := tr.runInstances()
	require.Len(t, inst, 2, "两笔 run 必须各拿一份新实例（拿到同一份 = A2 缺陷回来了）")
	require.NotSame(t, inst[0], inst[1])

	// 残留状态判据：每笔都只喂了 3 标的 × prDays 根。跨 run 共享的话，
	// 第二笔会是 6×prDays（曲线照旧「漂亮」，所以必须这样直接数）。
	for i, p := range inst {
		require.Equal(t, 3*prDays, p.totalBars(),
			"第 %d 笔的实例累计 bar 数不等于本笔应有量 = 它带着别处的残留状态", i)
	}
}

// TestEngine_ConcurrentRunsDoNotShareInstances 验收「并发两笔不串」：
// 两笔跑在**同一个** Engine 上、票池互不相交。不变量与创建顺序无关 ——
// 没有任何一份实例同时见过两个票池里的标的。
func TestEngine_ConcurrentRunsDoNotShareInstances(t *testing.T) {
	const (
		name  = "qt11_concurrent_probe"
		nDays = 15
	)

	tr := &prTracker{name: name}
	require.NoError(t, strategy.GlobalRegisterFactory(tr.factory))

	eng, _, symbols := buildSyntheticEngine(t, 6, nDays, 1)
	poolA, poolB := symbols[:3], symbols[3:]

	var wg sync.WaitGroup
	resp := make([]*BacktestResponse, 2)
	for i, pool := range [][]string{poolA, poolB} {
		wg.Add(1)
		go func(i int, pool []string) {
			defer wg.Done()
			r, err := eng.RunBacktest(context.Background(), prRequest(name, pool, nDays))
			require.NoError(t, err)
			require.Equal(t, "completed", r.Status, "status=%s err=%s", r.Status, r.Error)
			resp[i] = r
		}(i, pool)
	}
	wg.Wait()
	require.NotNil(t, resp[0])
	require.NotNil(t, resp[1])

	inst := tr.runInstances()
	require.Len(t, inst, 2, "并发两笔各要一份新实例")

	inPool := func(pool []string, sym string) bool {
		for _, s := range pool {
			if s == sym {
				return true
			}
		}
		return false
	}
	for i, p := range inst {
		a, b := 0, 0
		for sym := range p.syms {
			if inPool(poolA, sym) {
				a++
			}
			if inPool(poolB, sym) {
				b++
			}
		}
		require.NotZero(t, p.totalBars(), "第 %d 份实例一根 bar 都没见到 —— 这笔没真跑", i)
		require.False(t, a > 0 && b > 0,
			"第 %d 份实例同时见到了两个票池（A 侧 %d 个 / B 侧 %d 个）= 并发两笔串状态", i, a, b)
		require.Equal(t, 3, a+b, "第 %d 份实例应当只属于一个票池（3 个标的）", i)
		require.Equal(t, 3*nDays, p.totalBars(), "第 %d 份实例的 bar 数不属于单独一笔 run", i)
	}
}
