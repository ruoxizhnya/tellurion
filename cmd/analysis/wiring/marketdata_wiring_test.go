package wiring

// marketdata_wiring_test.go —— QT-64 的装配断言。
//
// 覆盖两条：
//  1. 同一个装配函数在 nil Router 时**显式 Warn**（装配缺口不许静默），
//     非 nil 时真的接到引擎上（引擎侧日志为证）；
//  2. 主引擎装配路径（StrategyRuntimeModule.Init）确实调用它 —— 单独测
//     Attach 函数只能证明函数本身，证明不了装配点没漏。

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/backtest"
	"tellurion/pkg/live"
	"tellurion/pkg/marketdata"
	"tellurion/pkg/risk"
	"tellurion/pkg/storage"
)

// lockedBuffer 让 zerolog 的并发写入（worker / 日循环）在测试下不竞态。
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newTestEngine(t *testing.T, logger zerolog.Logger) *backtest.Engine {
	t.Helper()
	eng, err := backtest.NewEngine(viper.New(), marketdata.NewInMemoryProvider(), logger)
	require.NoError(t, err)
	return eng
}

// TestNewMarketDataRouter_RequiresStore：构造点不许在缺 store 时静默返回
// nil —— 那会把装配错误推迟到策略跑到日内特征。
func TestNewMarketDataRouter_RequiresStore(t *testing.T) {
	t.Parallel()

	router, err := NewMarketDataRouter(nil)
	require.Error(t, err)
	assert.Nil(t, router)
	assert.Contains(t, err.Error(), "store")
}

// TestAttachMarketDataRouter_NilWarns：摘掉 Router 是显式状态，不是静默省略。
func TestAttachMarketDataRouter_NilWarns(t *testing.T) {
	t.Parallel()

	var buf lockedBuffer
	logger := zerolog.New(&buf)
	eng := newTestEngine(t, logger)

	AttachMarketDataRouter(eng, nil, logger)

	assert.Contains(t, buf.String(), "marketdata.Router 未装配",
		"nil Router 必须显式 Warn（QT-64 ③-③）")
	assert.NotContains(t, buf.String(), "marketdata.Router attached",
		"nil Router 不该打「已接线」")
}

// TestAttachMarketDataRouter_Attaches：非 nil 时引擎侧必须收到（以引擎
// 自己的日志为证 —— 装配函数与引擎共用一个 logger 实例）。
func TestAttachMarketDataRouter_Attaches(t *testing.T) {
	t.Parallel()

	var buf lockedBuffer
	logger := zerolog.New(&buf)
	eng := newTestEngine(t, logger)

	AttachMarketDataRouter(eng, marketdata.NewRouter(nil), logger)

	assert.Contains(t, buf.String(), "marketdata.Router attached",
		"Router 必须真的接到引擎上（D6 预热通道）")
	assert.NotContains(t, buf.String(), "未装配")
}

// TestStrategyRuntimeModule_AttachesRouter：主引擎装配路径带 Router。
func TestStrategyRuntimeModule_AttachesRouter(t *testing.T) {
	t.Parallel()

	var buf lockedBuffer
	logger := zerolog.New(&buf)

	data := NewDataEngineModule(func() (marketdata.Provider, *marketdata.DataAdapter, error) {
		return marketdata.NewHTTPProvider("http://test:8081", logger),
			marketdata.NewDataAdapter(nil, nil, nil, logger), nil
	})
	riskM := NewRiskEngineModule(func() (*risk.RiskManager, error) {
		return risk.NewRiskManager(risk.RiskManagerConfig{}, logger)
	})
	exec := NewExecEngineModule(func() (live.LiveTrader, error) {
		return live.NewMockTrader(live.MockTraderConfig{InitialCash: 1_000_000}, logger), nil
	})
	strat := NewStrategyRuntimeModule(viper.New(), logger, &StrategyRuntimeDeps{
		Store:      &storage.PostgresStore{},
		DataEngine: data,
		RiskEngine: riskM,
		ExecEngine: exec,
		Router:     marketdata.NewRouter(nil),
	})

	ctx := context.Background()
	require.NoError(t, data.Init(ctx))
	require.NoError(t, riskM.Init(ctx))
	require.NoError(t, exec.Init(ctx))
	require.NoError(t, strat.Init(ctx))

	assert.Contains(t, buf.String(), "marketdata.Router attached",
		"strategy-runtime 的装配点必须把 Router 接到引擎（QT-64 ③-②）")
}
