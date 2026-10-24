package backtest

// QT-20 的验收腿：run 覆写参数真正生效、可追溯、未知键 400。
//
// 四条要证的事：
//  1. 生效参数 = manifest 默认 ⊕ run 覆写（浅合并），未知键拒绝且点名；
//  2. 未知键在 RunBacktest 入口变成 INVALID_INPUT ⇒ HTTP 400；
//  3. 同策略不同参数**并发**两笔各自生效（不串参数）；
//  4. provenance 的 params_json / params_hash 记的是同一份生效值，
//     改参数 ⇒ params_hash 变。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"tellurion/pkg/httpserver"
	"tellurion/pkg/strategy"
)

const qt20ProbeName = "qt20-params-probe"

// qt20Probe 用 fire_idx 决定「第几天发买入信号」，让测试能从成交日期
// 反推「这一笔 run 用的是哪一份参数」。weight 用来区分同一天的两笔。
type qt20Probe struct {
	*strategy.BaseStrategy
	fireIdx int
	weight  float64
}

func (s *qt20Probe) Parameters() []strategy.Parameter {
	return []strategy.Parameter{
		{Name: "fire_idx", Type: "int", Default: 3, Min: 1, Max: 100},
		{Name: "weight", Type: "float", Default: 0.05, Min: 0, Max: 0.2},
	}
}

func (s *qt20Probe) Configure(params map[string]any) error {
	if v, ok := params["fire_idx"]; ok && v != nil {
		f, ok := numericParamValue(v)
		if !ok {
			return fmt.Errorf("fire_idx must be numeric, got %T", v)
		}
		s.fireIdx = int(f)
	}
	if v, ok := params["weight"]; ok && v != nil {
		f, ok := numericParamValue(v)
		if !ok {
			return fmt.Errorf("weight must be numeric, got %T", v)
		}
		s.weight = f
	}
	return nil
}

func (s *qt20Probe) Weight(strategy.Signal, float64) float64 { return s.weight }

func (s *qt20Probe) GenerateSignals(_ context.Context, bars map[string][]domain.OHLCV, _ *domain.Portfolio) ([]strategy.Signal, error) {
	series := bars["600000.SH"]
	if len(series) == 0 {
		return nil, nil
	}
	fire := series[0].Date.AddDate(0, 0, s.fireIdx)
	last := series[len(series)-1]
	if !last.Date.Equal(fire) {
		return nil, nil
	}
	return []strategy.Signal{{
		Symbol:    "600000.SH",
		Action:    "buy",
		Direction: domain.DirectionLong,
		Strength:  s.weight,
		Date:      last.Date,
	}}, nil
}

var qt20ProbeOnce sync.Once

func registerQT20Probe(t *testing.T) {
	t.Helper()
	qt20ProbeOnce.Do(func() {
		require.NoError(t, strategy.GlobalRegisterFactory(func() strategy.Strategy {
			return &qt20Probe{
				BaseStrategy: strategy.NewBaseStrategy(qt20ProbeName, "QT-20 参数投递探针"),
			}
		}))
	})
}

func TestResolveStrategyParams_MergesDefaultsAndOverrides(t *testing.T) {
	registerQT20Probe(t)

	eff, err := ResolveStrategyParams(qt20ProbeName, map[string]any{
		"fire_idx": float64(7),
		"weight":   0.12,
	})
	require.NoError(t, err)
	assert.Equal(t, float64(7), eff["fire_idx"], "覆写值生效")
	assert.Equal(t, 0.12, eff["weight"])

	// 只覆写一个键时，另一个键仍是 manifest 默认值。
	eff, err = ResolveStrategyParams(qt20ProbeName, map[string]any{"fire_idx": float64(5)})
	require.NoError(t, err)
	assert.Equal(t, 0.05, eff["weight"], "未覆写的键必须保留默认值（浅合并）")
}

func TestResolveStrategyParams_UnknownKeyRejectedByName(t *testing.T) {
	registerQT20Probe(t)

	_, err := ResolveStrategyParams(qt20ProbeName, map[string]any{
		"fire_idx":  float64(5),
		"fire_idxx": 1,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fire_idxx", "错误必须点名未知键")
	assert.Contains(t, err.Error(), qt20ProbeName, "错误必须点名策略")
	assert.Contains(t, err.Error(), "rejected, not ignored")
}

func TestResolveStrategyParams_TypeAndRangeValidated(t *testing.T) {
	registerQT20Probe(t)

	// 类型：int 参数给小数 ⇒ 拒。
	_, err := ResolveStrategyParams(qt20ProbeName, map[string]any{"fire_idx": 2.5})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "integer")

	// 范围：超出 manifest 声明的 Max ⇒ 拒并点名上界。
	_, err = ResolveStrategyParams(qt20ProbeName, map[string]any{"weight": 0.5})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "maximum")
}

func TestRunBacktest_UnknownStrategyParamIsInvalidInput400(t *testing.T) {
	registerQT20Probe(t)
	sc := provenanceBaseline()
	sc.pool = []string{"600000.SH"}

	prov := provenanceProvider(t, sc.pool, sc.days)
	eng, err := NewEngine(provenanceViper(sc.seed, sc.commission, sc.slippage, sc.stampTax), prov, zerolog.Nop())
	require.NoError(t, err)
	applyOptions(eng, provenanceBaseOptions(t))

	_, runErr := eng.RunBacktest(context.Background(), BacktestRequest{
		Strategy:       qt20ProbeName,
		StockPool:      sc.pool,
		StartDate:      provenanceTestStart.Format("2006-01-02"),
		EndDate:        provenanceTestStart.AddDate(0, 0, sc.days-1).Format("2006-01-02"),
		StrategyParams: map[string]any{"nope": float64(1)},
	})
	require.Error(t, runErr, "未知参数必须拒绝，不能静默忽略")
	assert.Contains(t, runErr.Error(), "nope")
	assert.Equal(t, http.StatusBadRequest, httpserver.StatusForAppError(runErr),
		"INVALID_INPUT 必须在 HTTP 边界映射成 400")
}

func TestRunBacktest_RunParamsEffectivePerConcurrentRun(t *testing.T) {
	registerQT20Probe(t)
	sc := provenanceBaseline()
	sc.pool = []string{"600000.SH"}
	sc.days = 20

	prov := provenanceProvider(t, sc.pool, sc.days)
	eng, err := NewEngine(provenanceViper(sc.seed, sc.commission, sc.slippage, sc.stampTax), prov, zerolog.Nop())
	require.NoError(t, err)
	applyOptions(eng, provenanceBaseOptions(t))

	run := func(fireIdx int, weight float64) (*BacktestResponse, error) {
		return eng.RunBacktest(context.Background(), BacktestRequest{
			Strategy:  qt20ProbeName,
			StockPool: sc.pool,
			StartDate: provenanceTestStart.Format("2006-01-02"),
			EndDate:   provenanceTestStart.AddDate(0, 0, sc.days-1).Format("2006-01-02"),
			// same_close：信号当天成交，成交日期可直接反推 fire_idx。
			FillTiming: FillTimingSameClose.String(),
			StrategyParams: map[string]any{
				"fire_idx": float64(fireIdx),
				"weight":   weight,
			},
		})
	}

	// 停牌/涨停会拦下买腿；从合成数据里挑两个「非涨停」的日子，
	// 否则测试会把风控拦截误读成「参数没生效」。
	bars := synthOHLCV(sc.pool, sc.days, provenanceTestStart)["600000.SH"]
	var fireDays []int
	for i, b := range bars {
		if i >= 3 && !b.LimitUp {
			fireDays = append(fireDays, i)
			if len(fireDays) == 2 {
				break
			}
		}
	}
	require.Len(t, fireDays, 2, "夹具需要两个非涨停交易日")

	type result struct {
		fire int
		resp *BacktestResponse
		err  error
	}
	ch := make(chan result, 2)
	go func() {
		resp, err := run(fireDays[0], 0.05)
		ch <- result{fire: fireDays[0], resp: resp, err: err}
	}()
	go func() {
		resp, err := run(fireDays[1], 0.08)
		ch <- result{fire: fireDays[1], resp: resp, err: err}
	}()

	for i := 0; i < 2; i++ {
		r := <-ch
		require.NoError(t, r.err)
		wantDate := provenanceTestStart.AddDate(0, 0, r.fire)
		var buyDates []time.Time
		for _, tr := range r.resp.Trades {
			if tr.Direction == domain.DirectionLong {
				buyDates = append(buyDates, tr.Timestamp)
			}
		}
		require.Len(t, buyDates, 1, "fire_idx=%d 应恰好产生一笔买入", r.fire)
		assert.True(t, buyDates[0].Equal(wantDate),
			"fire_idx=%d 的买入应发生在 %s，实际 %s —— 两笔并发 run 串了参数",
			r.fire, wantDate.Format("2006-01-02"), buyDates[0].Format("2006-01-02"))
	}
}

func TestRunProvenance_ParamsOverrideIsRestorableAndHashMoves(t *testing.T) {
	registerQT20Probe(t)
	sc := provenanceBaseline()
	sc.pool = []string{"600000.SH"}

	run := func(overrides map[string]any) (string, []byte) {
		t.Helper()
		capture := &provenanceCapture{}
		prov := provenanceProvider(t, sc.pool, sc.days)
		eng, err := NewEngine(provenanceViper(sc.seed, sc.commission, sc.slippage, sc.stampTax), prov, zerolog.Nop())
		require.NoError(t, err)
		applyOptions(eng, append(provenanceBaseOptions(t), WithRunProvenanceWriter(capture)))

		_, err = eng.RunBacktest(context.Background(), BacktestRequest{
			Strategy:       qt20ProbeName,
			StockPool:      sc.pool,
			StartDate:      provenanceTestStart.Format("2006-01-02"),
			EndDate:        provenanceTestStart.AddDate(0, 0, sc.days-1).Format("2006-01-02"),
			FillTiming:     FillTimingSameClose.String(),
			StrategyParams: overrides,
		})
		require.NoError(t, err)
		rec := capture.last(t)
		return rec.ParamsHash, rec.ParamsJSON
	}

	hashA, jsonA := run(map[string]any{"fire_idx": float64(4)})
	hashB, jsonB := run(map[string]any{"fire_idx": float64(9)})
	assert.NotEqual(t, hashA, hashB, "改参数 ⇒ params_hash 必须变")

	var paramsA, paramsB map[string]any
	require.NoError(t, json.Unmarshal(jsonA, &paramsA))
	require.NoError(t, json.Unmarshal(jsonB, &paramsB))
	assert.Equal(t, float64(4), paramsA["fire_idx"], "history 能还原生效参数")
	assert.Equal(t, 0.05, paramsA["weight"], "未覆写键仍是默认值")
	assert.Equal(t, float64(9), paramsB["fire_idx"])
}
