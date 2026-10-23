package backtest

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/spf13/viper"
	"tellurion/pkg/backtest/cache"
	"tellurion/pkg/backtest/metrics"
	apperrors "tellurion/pkg/errors"
	"tellurion/pkg/execalgo"
	"tellurion/pkg/httpclient"
	"tellurion/pkg/live"
	"tellurion/pkg/marketdata"
	"tellurion/pkg/risk"
	"tellurion/pkg/storage"
	"tellurion/pkg/strategy"
)

// Config holds backtest engine configuration.
type Config struct {
	InitialCapital float64 `mapstructure:"initial_capital"`
	CommissionRate float64 `mapstructure:"commission_rate"`
	SlippageRate   float64 `mapstructure:"slippage_rate"`
	RiskFreeRate   float64 `mapstructure:"risk_free_rate"`
	Seed           int64   `mapstructure:"seed"` // Random seed for determinism; 0 = use time-based seed

	// LiquidityFactor（REV-09）：单笔委托量上限 = 当日成交量 × 本系数。
	// 零值回落 0.1（与 tracker 的默认一致，保持既有默认行为不变）；
	// 取值 > 1 会被 clamp 到 1（见 (*Engine).liquidityFactor）。
	LiquidityFactor float64 `mapstructure:"liquidity_factor"`

	// Trading rules.
	//
	// AUD-37: populated from the **top-level** `trading` key (see NewEngine),
	// NOT from `backtest.trading`. The `-` tag is what keeps this field out
	// of the `backtest:` unmarshal: leaving both paths open would make
	// `backtest.trading` a silently-shadowed second entry for the same
	// knobs, because the yaml's top-level block is always present. Go
	// callers of NewEngineWithOptions still set it directly.
	Trading TradingConfig `mapstructure:"-"`
}

// S7-P2-1: TradingConfig, PriceLimitConfig, and defaultTradingConfig()
// moved to pkg/backtest/contracts/contracts.go. They are re-exported
// here via type aliases in aliases.go so all callers
// (backtest.TradingConfig / backtest.defaultTradingConfig()) keep
// working unchanged.

// Engine is the backtesting engine that simulates trading strategies.
type Engine struct {
	mu sync.RWMutex

	// Configuration
	config Config

	// Market data provider (abstracted for testability)
	provider marketdata.Provider

	// External service URLs (for non-market-data calls: risk).
	// strategyServiceURL 已随 OBS-11 删除——策略服务 standby（ADR-012），
	// 信号只从本地注册表产出。
	riskServiceURL string

	// HTTP client for non-market-data service communication (with retry)
	httpClient *httpclient.Client

	// Active backtest states — P1-18 (ADR-020):
	// 旧字段 `backtests map[string]*BacktestState` + `btMu sync.RWMutex` 已
	// 删除。状态现在通过 `StateStore` 接口管理，LRUStateStore 默认
	// capacity = 1000，防止长跑场景下无界增长导致 OOM。外部代码
	// 通过 Engine.StateStore() 访问；旧 API (`GetBacktestResult` 等)
	// 行为完全保留,内部委托到 store.Get。
	//
	// 工作流程：
	//   - RunBacktest  : store.Put(id, state)
	//   - Get*         : store.Get(id)
	//   - 周期性 GC    : store.Evict(keepN)  (可由 caller 调用)
	//   - LRU 自动驱逐 : Put 超过 capacity 时旧条目自动丢弃
	stateStore StateStore

	// Logger
	logger zerolog.Logger

	// L1 in-memory OHLCV cache —— **装配期种子**（QT-12 语义变更）。
	//
	// P1-16 (ADR-020) 拆分为 CacheManager 子组件；QT-12 之前 run 直接 warm
	// 这份，于是并发 run 互相覆盖、且跨 run 单调合并不清理（A7/A10）。
	// 现在每个 run 在 newRunContext 里拷一份自己的，**run 期间没人写这里**。
	// 留下它的理由只有两个：① LoadOHLCVInMemory / CacheManager() 是对外
	// API（测试与 bench 用它注入起点数据）；② 装配期注入的种子正是
	// 「每个 run 从同一份数据出发」的锚。
	//
	// Key: symbol, Value: all OHLCV bars for that symbol (sorted by date).
	// Populated via CacheManager.Warm() or CacheManager.Load().
	// CacheManager.Get() checks this first — zero-latency hit, falls back
	// to provider on miss. Eliminates N×D HTTP calls (N=stocks, D=trading
	// days) during a backtest run.
	cache *CacheManager

	// L1 factor cache —— 同 e.cache，**装配期种子**（QT-12）。run 用的是
	// runContext 里的副本（FactorCacheAccessor.Copy）。
	factor *FactorCacheAccessor

	// In-process risk manager — when set, position sizing, stop-loss, and
	// regime detection are computed locally without HTTP calls to risk-service.
	// Pass nil (default) to fall back to HTTP-based risk service calls.
	riskManager *risk.RiskManager

	// ParallelWorkers controls how many goroutines fetch data concurrently
	// inside each trading day. A value <= 0 means sequential (1 worker).
	parallelWorkers int

	// Optional DataAdapter for event-driven data pipeline with multi-source
	// fallback and runtime source switching. When set, provider reads are
	// routed through the adapter; when nil, the raw provider is used directly.
	dataAdapter *marketdata.DataAdapter

	// Optional PostgresStore for direct factor cache access.
	// When set, the engine pre-loads factor z-scores from DB before backtest,
	// eliminating per-symbol-per-day HTTP/DB queries for multi-factor strategies.
	store *storage.PostgresStore

	// adjStore 是 REV-18 复权基准快照的持久化端口。nil = 未注入（无库 /
	// 既有测试路径），快照完全直通。由 WithAdjustmentAnchorStore 注入；
	// 未注入但 e.store 在位时自动用 storageAnchorStore 适配。
	adjStore AdjustmentAnchorStore

	// provWriter 是 QT-63 的 provenance 写入端口。nil = 跟随 e.store
	// （生产路径：有库就落 quant.run_provenance）；测试可经
	// WithRunProvenanceWriter 注入替身。
	provWriter RunProvenanceWriter

	// provenanceOff = true 时完全不写 provenance（只读 / 演示装配）。
	provenanceOff bool

	// L1 基本面缓存（P2-12）：symbol → 按可用日升序的财报序列。
	//
	// 只在策略声明需要（strategy.FundamentalAware）时预热一次，回测期间只读。
	// 前视偏差不由这里挡 —— 由 OHLCVDataProvider 按每根 K 线的日期切，
	// 所以缓存里存着 asOf 之后的期数也不会泄漏未来。
	//
	// 受 e.mu 保护：Engine 实例是共享的，多个回测可以并发跑。
	fundamentals map[string]strategy.FundamentalSeries

	// L1 上市日历（P2-4）：symbol → 在市区间（上市日 / 摘牌日）。
	//
	// 空 map = 没有这份数据，引擎**不过滤**并如实上报（偏差维会带
	// PoolSourceCurrent 的 blocking）。绝不能反过来假设「没记录 = 一直在市」
	// —— 那正是幸存者偏差本身。
	//
	// **装配期种子**（QT-12）：run 期间读写的是 runContext 里那份副本。
	listing map[string]storage.ListingWindow

	// liveBridge 桥接 backtest 信号到 live/paper trading (P1-17 ADR-020)。
	// 取代旧 `liveTrader live.LiveTrader` 字段；旧 SetLiveTrader /
	// GetLiveTrader / ExecuteSignalViaLiveTrader 等方法保留为
	// backward-compat shim (6 个月) 委托到 bridge。
	// live 包接口见 pkg/live/trader.go。
	liveBridge *LiveBridge

	// executionBridge 桥接 ExecutionService (slippage / commission /
	// 限价单)。取代旧 `executionService ExecutionService` 字段；
	// 旧 SetExecutionService / GetExecutionService 保留为 shim。
	// ExecutionService 接口见 execution.go。
	executionBridge *ExecutionBridge

	// rng is the per-engine *rand.Rand, initialized from config.Seed in
	// NewEngine. Always retained (never discarded) so that any code path
	// needing deterministic randomness — e.g. per-backtest trade IDs,
	// bootstrap sampling in walk-forward, or future stochastic order-filling
	// models — can call e.RNG() to get a stable, replayable stream.
	// When config.Seed is 0 the engine falls back to a time-based seed and
	// the stream is non-replayable (intentional, for production runs).
	rng *rand.Rand

	// signalObserver 是 K5 切片 2 的信号对账钩子（可选）。非 nil 时每个
	// 交易日 getSignals 产出 domain.Signal 后、撮合前回调——把回测侧信号
	// 序列导出来，与 paper 侧（PaperSession.SignalCollector）的信号做
	// ReconcileSignals 比对。nil = 不观察（零行为变化）。
	signalObserver func(date time.Time, signals []domain.Signal)

	// signalProvider 是 K8 · SignalSpec 的可插拔信号供给（可选）。
	// nil = 用 strategy.LocalSignalProvider 默认本地实现（注册表 + 批式/
	// 流式双模式，行为与旧 getSignalsFromLocalStrategy 逐字节一致）。
	// 经 WithSignalProvider 注入远程/自定义实现（阶段 2 起）。
	signalProvider strategy.SignalProvider

	// router 是规范层透明路由（D1 产物，pkg/marketdata）。
	//
	// D6 日内快照预加载的唯一取数通道：warmIntradayIfNeeded 在回测
	// 启动前经它按频率声明取 1m bar；回测期间策略只读快照，运行时
	// 禁止直接查 provider（红线，结构执法见 intraday.go 与
	// internal/repoguard 的 import 隔离护栏）。nil = 未接，
	// IntradayAware 策略用到日内特征时明确失败（不静默当 0）。
	router *marketdata.Router

	// L1 日内分钟快照（D6）：symbol → 按 ts 升序的 1m bar
	// （规范层类型，Frequency/VisibleAt 已由 Router 填好）。
	//
	// 回测启动前填充（warmIntradayIfNeeded 经 Router，或
	// SetIntradaySnapshot 直接注入），回测期间只读；nil = 没有这份
	// 数据，日内特征 provider 恒不可得（缺失≠0，不是 0）。
	//
	// 受 e.mu 保护：Engine 实例是共享的，多个回测可以并发跑。
	// **装配期种子**（QT-12）：run 读写的是 runContext 里的副本。
	intraday map[string][]domain.OHLCV

	// admission 是 run 准入闸（QT-12 ④）：并发上限 + 有界队列 + 内存预算
	// 与降级顺序都在 pkg/backtest/admission.go。nil = 显式关闭
	// （WithRunAdmissionDisabled）——生产路径不该有这种取值。
	admission *runAdmission

	// wasmBytesAccounting 上报 wasm 会话占用的线性内存字节
	// （QT-12 ⑤ 的 backtest_wasm_bytes 生产者）。nil = 今天仓内的真实形态
	// （没有 wasm 信号供给，接线是 QT-26），指标报 0 而不是不报。
	wasmBytesAccounting func() int64
}

// BacktestState is defined in state.go (P1-20) with internal locking
// (mu RWMutex) and a frozen flag. See state.go for the concurrency
// contract and accessor methods (GetStatus / SetStatus / Freeze /
// Snapshot / ...).

// S7-P2-1: BacktestRequest and BacktestResponse moved to
// pkg/backtest/contracts/contracts.go. Re-exported via aliases.go.

// NewEngine creates a new backtest engine.
func NewEngine(v *viper.Viper, provider marketdata.Provider, logger zerolog.Logger) (*Engine, error) {
	config := Config{}
	// AUD-40: viper's Sub returns a **nil** *viper.Viper when the key is
	// absent, and Unmarshal on it panics — the stack bottoms out in
	// viper.(*Viper).AllKeys dereferencing 0x0 — instead of returning an
	// error. A config with no `backtest:` block is legitimate: it should boot
	// on the defaults applied below, not crash the process. UnmarshalKey would
	// also do (it returns nil for a missing key), but Sub is kept because the
	// whole `backtest.*` subtree is one section and reading it as one unit is
	// what makes the intent obvious.
	if sub := v.Sub("backtest"); sub != nil {
		if err := sub.Unmarshal(&config); err != nil {
			return nil, apperrors.Wrap(err, apperrors.ErrCodeInvalidInput, "failed to unmarshal backtest config", "NewEngine")
		}
	}
	// AUD-37: the A-share trading rules live at the TOP LEVEL of the service
	// config (`trading:`), which is also the block cmd/analysis reads for the
	// live execution path. Reading the same block here is what makes the
	// engine and live trading share one source of truth instead of two sets
	// of fee assumptions that happen to agree today.
	//
	// UnmarshalKey returns nil when the key is absent, so a config without a
	// `trading:` block still boots on the defaults below.
	if err := v.UnmarshalKey("trading", &config.Trading); err != nil {
		return nil, apperrors.Wrap(err, apperrors.ErrCodeInvalidInput, "failed to unmarshal trading config", "NewEngine")
	}

	// strategy_service.url 已随 OBS-11 删除（策略信号只走本地注册表）。
	riskServiceURL := v.GetString("risk_service.url")
	if riskServiceURL == "" {
		riskServiceURL = "http://localhost:8083"
	}

	// REV-19：**在执行服务默认值覆盖之前**抓住用户显式配置的滑点。
	//
	// 为什么要抓原始值：下面 `if config.SlippageRate == 0` 会给它填
	// DefaultSlippageRate（0.0001，tracker/paper 口径），此后无法区分
	// 「用户配了 0.0001」与「用户没配」。而执行服务的 fixed 分支默认是
	// fees.FixedSlippageRate（0.001，0.1%）——两个默认值本就不同（见 SPEC
	// 「成本口径」节）。语义取「只有用户显式配置时才让执行服务跟随」，
	// 未配置 → 传 0 → 执行服务回落 0.001（**现行为不变**）。
	explicitSlippageRate := config.SlippageRate

	// Set defaults
	if config.InitialCapital == 0 {
		config.InitialCapital = DefaultInitialCapital
	}
	if config.CommissionRate == 0 {
		config.CommissionRate = DefaultCommissionRate
	}
	if config.SlippageRate == 0 {
		config.SlippageRate = DefaultSlippageRate
	}
	if config.RiskFreeRate == 0 {
		config.RiskFreeRate = DefaultRiskFreeRate
	}

	// AUD-37: fill each unset trading rule from its default — field by
	// field, NOT all-or-nothing. The old guard keyed the whole struct off
	// StampTaxRate, so a partial `trading:` block silently zeroed the rest
	// (and zero is not "unset" for the fee knobs). See
	// TradingConfig.WithDefaults.
	config.Trading = config.Trading.WithDefaults()

	// Initialize random seed for deterministic backtests.
	//
	// Sprint 6 P0-5 (ODR-013): the previous code called
	//     rand.New(rand.NewSource(config.Seed))
	// and threw the *rand.Rand return value away. The global state was
	// re-seeded but nothing in the engine held a handle to the source, so
	// any code that wanted to replay a backtest bit-for-bit had no way to
	// fork off a per-backtest RNG and no test could assert determinism.
	//
	// Now: the engine always owns a *rand.Rand (rng). When config.Seed is
	// non-zero, the engine is fully deterministic — any code that goes
	// through e.RNG() (or a future per-backtest fork) will produce a
	// reproducible sequence. When config.Seed is 0, we still initialize
	// rng but with a time-based seed so production runs remain
	// non-deterministic.
	var rng *rand.Rand
	if config.Seed != 0 {
		rng = rand.New(rand.NewSource(config.Seed))
	} else {
		rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}

	// Initialize default execution service
	execConfig := domain.ExecutionConfig{
		OrderType:      domain.OrderTypeMarket,
		SlippageModel:  "fixed",
		CommissionRate: config.CommissionRate,
		MinCommission:  config.Trading.MinCommission,
		InitialCapital: config.InitialCapital,
		SlippageRate:   explicitSlippageRate, // REV-19
	}
	executionService := NewBacktestExecutionService(execConfig)
	componentLogger := logger.With().Str("component", "backtest_engine").Logger()

	executionBridge := NewExecutionBridge(componentLogger)
	executionBridge.Set(executionService)

	// P1-18 (ADR-020): 默认 LRU 1000 容量,防止长跑场景下无界增长 OOM。
	// 测试或生产可用 WithStateStore(NoopStateStore) 注入自定义实现。
	stateStore := NewLRUStateStore(DefaultStateStoreCapacity)

	eng := &Engine{
		config:          config,
		provider:        provider,
		riskServiceURL:  riskServiceURL,
		httpClient:      httpclient.New("", 30*time.Second, 3),
		logger:          componentLogger,
		cache:           cache.NewCacheManager(componentLogger),
		factor:          cache.NewFactorCacheAccessor(componentLogger),
		stateStore:      stateStore,
		liveBridge:      NewLiveBridge(componentLogger),
		executionBridge: executionBridge,
		rng:             rng,
	}
	// QT-12：准入闸默认开（并发上限 min(GOMAXPROCS,4) + 内存预算）。
	eng.admission = newRunAdmission(admissionFromViper(v), nil)
	return eng, nil
}

// NewEngineWithOptions creates a new Engine from a parsed Config + Provider,
// then applies a list of EngineOption (functional injection).
//
// P1-19 (ADR-020): the new construction path. Preferred for new code:
//
//	eng, err := NewEngineWithOptions(cfg, provider,
//	    WithRiskManager(rm),
//	    WithDataAdapter(adapter),
//	    WithLiveTrader(mockTrader),
//	)
//
// Backward compat: NewEngine(v, provider, logger) remains as a thin
// wrapper that parses config from viper and calls NewEngineWithOptions
// (no EngineOptions). Caller-controlled options should use this new path.
func NewEngineWithOptions(cfg Config, provider marketdata.Provider, opts ...EngineOption) (*Engine, error) {
	if provider == nil {
		return nil, apperrors.New(apperrors.ErrCodeInvalidInput, "provider is required").WithOperation("NewEngineWithOptions")
	}

	// Apply defaults identical to NewEngine so callers passing a partial
	// Config get the same behavior.
	if cfg.InitialCapital == 0 {
		cfg.InitialCapital = DefaultInitialCapital
	}
	if cfg.CommissionRate == 0 {
		cfg.CommissionRate = DefaultCommissionRate
	}
	if cfg.SlippageRate == 0 {
		cfg.SlippageRate = DefaultSlippageRate
	}
	if cfg.RiskFreeRate == 0 {
		cfg.RiskFreeRate = DefaultRiskFreeRate
	}
	// AUD-37: field-by-field defaults, mirroring NewEngine.
	cfg.Trading = cfg.Trading.WithDefaults()

	// Initialize random seed (mirrors NewEngine; P0-5).
	var rng *rand.Rand
	if cfg.Seed != 0 {
		rng = rand.New(rand.NewSource(cfg.Seed))
	} else {
		rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}

	componentLogger := zerolog.New(nil).With().Str("component", "backtest_engine").Logger()

	// Default ExecutionService (mirrors NewEngine).
	execConfig := domain.ExecutionConfig{
		OrderType:      domain.OrderTypeMarket,
		SlippageModel:  "fixed",
		CommissionRate: cfg.CommissionRate,
		MinCommission:  cfg.Trading.MinCommission,
		InitialCapital: cfg.InitialCapital,
	}
	executionBridge := NewExecutionBridge(componentLogger)
	executionBridge.Set(NewBacktestExecutionService(execConfig))

	// P1-18 (ADR-020): 默认 LRU StateStore;通过 WithStateStore 可覆盖
	// (例如测试用 NoopStateStore,生产用 PersistentStateStore)。
	stateStore := NewLRUStateStore(DefaultStateStoreCapacity)

	eng := &Engine{
		config:          cfg,
		provider:        provider,
		httpClient:      httpclient.New("", 30*time.Second, 3),
		logger:          componentLogger,
		cache:           cache.NewCacheManager(componentLogger),
		factor:          cache.NewFactorCacheAccessor(componentLogger),
		stateStore:      stateStore,
		liveBridge:      NewLiveBridge(componentLogger),
		executionBridge: executionBridge,
		rng:             rng,
	}
	// QT-12：默认档准入；WithRunObserver / WithRunAdmissionDisabled 覆盖。
	eng.admission = newRunAdmission(defaultAdmissionConfig(), nil)
	applyOptions(eng, opts)
	return eng, nil
}

func (e *Engine) SetDataAdapter(adapter *marketdata.DataAdapter) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.dataAdapter = adapter
	if adapter != nil {
		e.logger.Info().Str("source", adapter.Primary()).Msg("DataAdapter attached to engine")
	}
}

// RNG returns the engine's *rand.Rand. The stream is fully deterministic
// when NewEngine was called with a non-zero Config.Seed; otherwise it is
// seeded from time.Now() and is non-replayable.
//
// Callers that need a stable, byte-equal stream across processes
// (regression tests, golden fixtures, property tests) MUST construct the
// engine with a fixed seed. Callers that need a per-backtest sub-stream
// should fork from this one with rand.New(rand.NewSource(e.RNG().Int63()))
// — that way the top-level seed is the only thing that needs to be
// recorded in test fixtures.
//
// Concurrency: *rand.Rand methods are safe for concurrent use (Go std
// documents this since 1.0), so callers may share e.RNG() across
// goroutines without extra synchronization.
func (e *Engine) RNG() *rand.Rand {
	return e.rng
}

// SetSignalObserver 注册 K5 切片 2 的信号对账观察钩子（可选）。传 nil 取消。
//
// 观察点在 runBacktestInternal 的日循环内：getSignals 产出 domain.Signal 后、
// processSignalsAndExecuteTrades 之前。此刻信号尚未受风控/持仓抵扣/资金约束
// 影响，是与 paper 侧 PaperSession.SignalCollector 同构的那一层——正是信号
// 对账（ReconcileSignals）要比对的输入。默认 nil，生产路径零行为变化。
func (e *Engine) SetSignalObserver(observer func(date time.Time, signals []domain.Signal)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.signalObserver = observer
}

func (e *Engine) SetStore(store *storage.PostgresStore) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.store = store
	if store != nil {
		e.logger.Info().Msg("PostgresStore attached to engine — factor cache warming enabled")
	}
}

// SetRunObserver 把准入/缓存账的观察面挂到本引擎的准入闸（QT-12 ⑤）。
//
// 为什么是 setter 不是 option：装配方（cmd/analysis）先建引擎、再建
// metrics，顺序反过来的话 option 路径拿不到 m。行为与 WithRunObserver
// 完全一致，共用同一条闸内落点（runAdmission.attachObserver）。
//
// ⚠️ 一个如实登记的口径限制：**闸是按引擎算的**。一个进程里有两个引擎
// （walk-forward 的每窗口工厂就是第二个），runs_active 会被后发布者覆盖、
// 内存预算也各算各的 —— 进程总账比单看数字宽松。多引擎共享一份进程级
// 准入账是 QT-12 的遗留项，见 docs/TASKS.md。
func (e *Engine) SetRunObserver(ob RunObserver) {
	e.mu.Lock()
	gate := e.admission
	e.mu.Unlock()
	if gate == nil {
		e.logger.Warn().Msg("SetRunObserver 被忽略：准入闸已通过 WithRunAdmissionDisabled 关闭")
		return
	}
	gate.attachObserver(ob)
}

func (e *Engine) DataAdapter() *marketdata.DataAdapter {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.dataAdapter
}

func (e *Engine) SwitchDataSource(ctx context.Context, name string, p marketdata.Provider) error {
	e.mu.RLock()
	adapter := e.dataAdapter
	e.mu.RUnlock()

	if adapter == nil {
		return fmt.Errorf("no DataAdapter set — call SetDataAdapter first")
	}
	if err := adapter.SetPrimary(name, p); err != nil {
		return err
	}
	e.logger.Info().Str("new_source", name).Msg("Data source switched via engine")
	return nil
}

func (e *Engine) effectiveProvider() marketdata.Provider {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.dataAdapter != nil {
		return e.dataAdapter
	}
	return e.provider
}

// RunBacktest executes a backtest with the given parameters.
func (e *Engine) RunBacktest(ctx context.Context, req BacktestRequest) (*BacktestResponse, error) {
	startDate, endDate, err := e.parseBacktestDateRange(ctx, req)
	if err != nil {
		return nil, err
	}

	initialCapital := req.InitialCapital
	if initialCapital <= 0 {
		initialCapital = e.config.InitialCapital
	}
	riskFreeRate := req.RiskFreeRate
	if riskFreeRate <= 0 {
		riskFreeRate = e.config.RiskFreeRate
	}

	stockPool, err := e.resolveStockPool(ctx, req, startDate)
	if err != nil {
		return nil, err
	}

	// QT-63：进程内调用方（job 服务）可以用 RunID 把身份对齐到 backtest_jobs.id；
	// 没给就自己生成。对外 HTTP 请求无法设置它（DTO 上是 json:"-"）。
	backtestID := req.RunID
	if backtestID == "" {
		backtestID = uuid.New().String()
	}
	state := e.newBacktestState(backtestID, req, stockPool, startDate, endDate, initialCapital, riskFreeRate)
	e.stateStore.Put(backtestID, state)

	result, err := e.runBacktestInternal(ctx, state)
	if err != nil {
		state.SetStatus(StatusFailed)
		state.SetError(err)
		state.Freeze()
		return &BacktestResponse{
			ID:        backtestID,
			Status:    StatusFailed,
			Error:     err.Error(),
			StartedAt: state.StartedAt.Format(time.RFC3339),
		}, err
	}

	state.SetResult(result)
	state.SetCompletedAt(time.Now())

	// OBS-01（切片 2）：无效运行在 state store 里同样不得被写成「成功」。
	// result.InvalidReasons 由 runBacktestInternal 在返回前算好并挂上（见
	// 本函数下方返回前的裁定段），这里据它决定状态：无效 → "invalid"，
	// 有效 → "completed"（正常路径行为一字不变）。读路径
	// （GetBacktestResult / 两个 GET 端点）据此才能不再把同一份结果说成
	// completed —— 那正是 OBS-01 读路径的另一半。
	stateStatus := StatusCompleted
	if len(result.InvalidReasons) > 0 {
		stateStatus = StatusInvalid
	}
	state.SetStatus(stateStatus)
	state.Freeze()

	return e.buildBacktestResponse(backtestID, req, state, result, initialCapital), nil
}

// parseBacktestDateRange parses and validates the request date range and
// verifies the trading calendar has data for it.
func (e *Engine) parseBacktestDateRange(ctx context.Context, req BacktestRequest) (time.Time, time.Time, error) {
	startDate, err := time.Parse("2006-01-02", req.StartDate)
	if err != nil {
		return time.Time{}, time.Time{}, apperrors.Wrap(err, apperrors.ErrCodeInvalidInput, "invalid start_date format: "+req.StartDate, "RunBacktest")
	}
	endDate, err := time.Parse("2006-01-02", req.EndDate)
	if err != nil {
		return time.Time{}, time.Time{}, apperrors.Wrap(err, apperrors.ErrCodeInvalidInput, "invalid end_date format: "+req.EndDate, "RunBacktest")
	}

	hasCalendar, err := e.checkCalendarExists(ctx, startDate, endDate)
	if err != nil {
		e.logger.Warn().Err(err).Msg("Calendar check error, proceeding anyway")
	} else if !hasCalendar {
		return time.Time{}, time.Time{}, errTradingCalendarNotSynced()
	}
	return startDate, endDate, nil
}

// errTradingCalendarNotSynced 是「库里没有交易日历」这个前置失败的**唯一构造点**。
//
// 分类是 DATA_QUALITY 而不是 INVALID_INPUT：请求本身没有任何问题，缺的是
// **库里的数据**。定成 INVALID_INPUT 等于对调用方说「你传错了」，而它该收到的是
// 「当前数据状态不满足前置」—— 两者的 HTTP 状态也不同（409 vs 400），
// 映射见 internal/httpserver.StatusForAppError（AUD-60：这条原先被拍平成 500）。
//
// 收敛成函数是为了让它**可被单测钉住**：Engine 的 store 是具体类型
// （`*storage.PostgresStore`）不是接口，所以「走到这个分支」需要真库；
// 但「这个分支给出的分类是什么」不需要 —— 见 errTradingCalendarNotSynced_test.go
// 与真环境 A/B（请求一个库里没有日历的区间应得 409）。
func errTradingCalendarNotSynced() error {
	return apperrors.New(apperrors.ErrCodeDataQuality,
		"trading calendar not synced, please run POST /sync/calendar first (with exchange 'SSE' or 'both')").
		WithOperation("RunBacktest")
}

// resolveStockPool returns the stock pool from the request, expanding index
// constituents when an IndexCode is supplied and StockPool is empty.
func (e *Engine) resolveStockPool(ctx context.Context, req BacktestRequest, startDate time.Time) ([]string, error) {
	stockPool := req.StockPool
	// DATA-01：调用方可能传 `csi300` 这类人类写法，库里存的是 `000300.SH`。
	// 口径翻译在这里做（见 index_alias.go），未命中的原样透传 ⇒ 查不到 ⇒ 空票池 ⇒
	// 既有 fail-loud 语义接管。
	indexCode := normalizeIndexCode(req.IndexCode)
	if len(stockPool) == 0 && indexCode != "" {
		e.mu.RLock()
		st := e.store
		e.mu.RUnlock()
		if st != nil {
			constituents, err := st.GetIndexConstituentsByDate(ctx, indexCode, startDate)
			if err != nil {
				e.logger.Warn().Err(err).Str("index_code", indexCode).Msg("Failed to load index constituents")
			} else if len(constituents) > 0 {
				stockPool = constituents
				e.logger.Info().Str("index_code", indexCode).Int("constituents", len(constituents)).Msg("Using index constituents as stock pool")
			}
		}
	}
	if len(stockPool) == 0 {
		return nil, apperrors.New(apperrors.ErrCodeInvalidInput, "stock_pool or index_code is required").WithOperation("RunBacktest")
	}
	return stockPool, nil
}

// newBacktestState constructs the initial BacktestState for a run.
func (e *Engine) newBacktestState(backtestID string, req BacktestRequest, stockPool []string, startDate, endDate time.Time, initialCapital, riskFreeRate float64) *BacktestState {
	return &BacktestState{
		ID:     backtestID,
		Status: "running",
		Params: domain.BacktestParams{
			StrategyName:   req.Strategy,
			StockPool:      stockPool,
			StartDate:      startDate,
			EndDate:        endDate,
			InitialCapital: initialCapital,
			RiskFreeRate:   riskFreeRate,
			// FillTiming 在这**唯一**一处把请求声明折成生效值（默认语义见
			// contracts.EffectiveFillTiming）；日循环入口再严格校验它拿到的是
			// 已知口径。野值在这里原样透传，好让入口报出「你声明的那个不认识」，
			// 而不是在这里被静默换成默认。
			FillTiming: req.EffectiveFillTiming().String(),
			// Kernel 同理取生效值：provenance 记的是**实际执行核**（N6-5），
			// 不是请求里那一串字。
			Kernel: req.EffectiveKernel(),
			// 归一化后的指数代码（票池已由它展开进 StockPool）；provenance
			// 用它记「票池从哪来」。
			IndexCode: normalizeIndexCode(req.IndexCode),
		},
		StartedAt: time.Now(),
		Tracker: NewTracker(
			initialCapital,
			e.config.CommissionRate,
			e.config.SlippageRate,
			e.config.Trading,
			e.logger,
		),
		TargetPositions: make(map[string]*domain.TargetPosition),
	}
}

// buildBacktestResponse assembles the success BacktestResponse from the
// completed state and result.
func (e *Engine) buildBacktestResponse(backtestID string, req BacktestRequest, state *BacktestState, result *domain.BacktestResult, initialCapital float64) *BacktestResponse {
	resp := &BacktestResponse{
		ID:              backtestID,
		Status:          StatusCompleted,
		Strategy:        req.Strategy,
		StrategyGitHash: lookupStrategyGitHash(req.Strategy),
		StartDate:       req.StartDate,
		EndDate:         req.EndDate,
		TotalReturn:     result.TotalReturn,
		AnnualReturn:    result.AnnualReturn,
		SharpeRatio:     result.SharpeRatio,
		SortinoRatio:    result.SortinoRatio,
		MaxDrawdown:     result.MaxDrawdown,
		MaxDrawdownDate: result.MaxDrawdownDate.Format("2006-01-02"),
		WinRate:         result.WinRate,
		TotalTrades:     result.TotalTrades,
		WinTrades:       result.WinTrades,
		LoseTrades:      result.LoseTrades,
		AvgHoldingDays:  result.AvgHoldingDays,
		CalmarRatio:     result.CalmarRatio,
		StartedAt:       state.StartedAt.Format(time.RFC3339),
		CompletedAt:     state.GetCompletedAt().Format(time.RFC3339),
		PortfolioValues: result.PortfolioValues,
		Trades:          result.Trades,
		StockPool:       req.StockPool,
		InitialCapital:  initialCapital,
		UniverseMaxSize: result.UniverseMaxSize,
		// QT-02：口径与偏差都取**实际跑的那次**（state.Params 由 newBacktestState
		// 写入、日循环入口校验过），不取 req.FillTiming 原值 —— 后者可能是空串
		// （没声明），把「没声明」写成「没口径」会和实际发生的收盘成交矛盾。
		FillTiming: state.Params.FillTiming,
		KnownBias:  FillTiming(state.Params.FillTiming).KnownBiases(),
	}

	// OBS-01：无效运行在响应里显式区别于「完成」—— Status=invalid 且带上
	// 原因 Code。调用方（报告层 / 自动化层 / API）据此走失败或无效路径，
	// 而不是把它当成一次成功观测。Error 保持只给真实错误（不装无效）。
	if len(result.InvalidReasons) > 0 {
		resp.Status = StatusInvalid
		resp.InvalidReasons = result.InvalidReasons
	}

	return resp
}

// ResponseFromState reconstructs the canonical BacktestResponse（ResultSpec）
// for a backtest whose state is still resident in the in-memory StateStore.
//
// K9 —— 为什么这是**唯一一处** state→response 透传点：
// GET /:id/report、lookupBacktestResponse（GET /:id/export/:format）与多策略
// 对比（reporting.NewCompareResolver）原先各自内联一份逐字段重建，且都把
// 闸门写成 `status == "completed"`、把 Status 硬编码成 "completed"。写路径
// （buildBacktestResponse）学会说 "invalid" 之后，读路径仍会把同一份结果说成
// completed（或把无效运行挡在门外、UI 读不到原因）。上提到 Engine 上是
// 因为 reporting 包 import 不了 cmd/analysis —— 收敛后状态与 InvalidReasons
// 的透传只有一处实现，读路径不可能再各自漂移。
//
// 返回 (resp, true) 表示内存里有可读结果（"completed" 或 "invalid"）；
// (zero, false) 表示内存里没有 / 状态不可读，调用方应退回 DB job 路径。
// "invalid" 必须放行 —— 否则「为什么无效」对 UI 不可见（与
// GetBacktestResult 的闸门是同一裁决）。
func (e *Engine) ResponseFromState(backtestID string) (BacktestResponse, bool) {
	status, err := e.GetBacktestStatus(backtestID)
	if err != nil || (status != StatusCompleted && status != StatusInvalid) {
		return BacktestResponse{}, false
	}
	result, err := e.GetBacktestResult(backtestID)
	if err != nil || result == nil {
		return BacktestResponse{}, false
	}
	params, _ := e.GetBacktestParams(backtestID)
	return BacktestResponse{
		ID:              backtestID,
		Status:          status,
		Strategy:        params.StrategyName,
		StartDate:       result.StartDate.Format("2006-01-02"),
		EndDate:         result.EndDate.Format("2006-01-02"),
		TotalReturn:     result.TotalReturn,
		AnnualReturn:    result.AnnualReturn,
		SharpeRatio:     result.SharpeRatio,
		SortinoRatio:    result.SortinoRatio,
		MaxDrawdown:     result.MaxDrawdown,
		MaxDrawdownDate: result.MaxDrawdownDate.Format("2006-01-02"),
		WinRate:         result.WinRate,
		TotalTrades:     result.TotalTrades,
		WinTrades:       result.WinTrades,
		LoseTrades:      result.LoseTrades,
		AvgHoldingDays:  result.AvgHoldingDays,
		CalmarRatio:     result.CalmarRatio,
		StockPool:       params.StockPool,
		InitialCapital:  params.InitialCapital,
		PortfolioValues: result.PortfolioValues,
		Trades:          result.Trades,
		InvalidReasons:  result.InvalidReasons,
		UniverseMaxSize: result.UniverseMaxSize,
		// QT-02：与写路径同源（都读 params），读路径不许自己再推断一次口径。
		// 历史 run 的 params 没有这个键 ⇒ 空 ⇒ JSON 里不出现 fill_timing ⇒
		// 读方看到的是「未知」，而不是被补出来的 same_close。
		FillTiming: params.FillTiming,
		KnownBias:  FillTiming(params.FillTiming).KnownBiases(),
	}, true
}

// lookupStrategyGitHash returns the short git hash of the currently-loaded
// strategy plugin with the given name, or "" if the plugin loader is not
// initialized or the strategy is not loaded as a plugin.
//
// REV-16：曾提到的「由外部 strategy-service 提供策略」这条路径已随该服务删除
// 而消失（ADR-012 关闭）——现在策略只有「本地注册表 / 插件」两种来源。
//
// This supports the VISION.md requirement "Track which strategy version
// ran which backtest": the hash is stamped onto every BacktestResponse
// so that downstream tooling can correlate a backtest result with the
// exact source code that produced it.
func lookupStrategyGitHash(strategyName string) string {
	if strategy.GlobalPluginLoader == nil {
		return ""
	}
	info, err := strategy.GlobalPluginLoader.Get(strategyName)
	if err != nil || info == nil {
		return ""
	}
	return info.GitHash
}

// runBacktestInternal contains the core backtest loop.
func (e *Engine) runBacktestInternal(ctx context.Context, state *BacktestState) (*domain.BacktestResult, error) {
	params := state.Params

	// QT-02：成交口径是「这份结果能不能和别的比」的判据，引擎不猜。
	// params.FillTiming 正常由请求解析点写入（newBacktestState ← EffectiveFillTiming）。
	// 走到这里还是空值或野值，说明调用方绕过了那道解析（自己构造 params 的
	// 内部路径、或 QT-02 之前落盘的历史快照被续跑）—— 当场失败，不回落默认：
	// 悄悄按收盘成交一份「没声明口径」的回测，正是 §8.1 要把 ErrFillTimingMissing
	// 立成必填的原因。
	// QT-12：本 run 的可变状态（L1 缓存 / 因子 / 财报 / 上市日历 / 日内快照 /
	// 信号供给 / 准入档位）全在 runContext 里，Engine 上的同名字段只是装配期
	// 种子。run 期间没有任何一条路径写引擎级字段。
	rc := newRunContext(e, params)
	// 收尾一律挂上，不等准入分支：lease 为 nil（闸关闭 / 还没抢到）时
	// Release 是 no-op，而台账撤账（retireRun）必须无条件发生 ——
	// 把它写在 if 里面，等于「闸关着的时候 run 的缓存账永远留在看板上」。
	defer rc.close()

	if e.admission != nil {
		lease, err := e.admission.acquire(ctx)
		if err != nil {
			return nil, err
		}
		rc.lease = lease
		if err := rc.applyAdmission(params.StockPool); err != nil {
			return nil, err
		}
	}

	fillTiming, err := ResolveFillTiming(params.FillTiming)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.ErrCodeInvalidInput,
			"fill_timing is required before the run can execute (known: next_open | same_close)", "RunBacktest")
	}

	logger := e.logger.With().
		Str("backtest_id", state.ID).
		Str("strategy", params.StrategyName).
		Str("fill_timing", fillTiming.String()).
		Time("start_date", params.StartDate).
		Time("end_date", params.EndDate).
		Logger()

	logger.Info().Msg("Starting backtest")

	warmCtx, warmCancel := context.WithTimeout(ctx, 2*time.Minute)
	if err := rc.warmCache(warmCtx, params.StockPool); err != nil {
		warmCancel()
		logger.Warn().Err(err).Msg("Cache warm-up failed — continuing without pre-cached data")
	} else {
		warmCancel()
		logger.Info().Msg("Cache warm-up completed")
	}

	if err := rc.warmFactorCache(ctx); err != nil {
		logger.Warn().Err(err).Msg("Factor cache warm-up failed — strategies will fall back to HTTP computation")
	} else if rc.factor.Len() > 0 {
		logger.Info().Msg("Factor cache warm-up completed")
	}

	// P2-12：只有声明了要财报的策略才会真的去查。
	if err := rc.warmFundamentalsIfNeeded(ctx, params.StrategyName, params.StockPool, params.EndDate); err != nil {
		logger.Warn().Err(err).Msg("Fundamentals warm-up failed — 用了 pe/pb/roe 的表达式会明确失败（不会静默当 0）")
	}

	// D6：只有声明了要日内特征的策略才会真的去预热（同财报范式）。
	// 快照在启动前一次取完全区间；回测期间策略只读快照 —— 运行时
	// 查询 provider 是红线（蓝图 §4.3）。
	if err := rc.warmIntradayIfNeeded(ctx, params.StrategyName, params.StockPool, params.StartDate, params.EndDate); err != nil {
		logger.Warn().Err(err).Msg("Intraday snapshot warm-up failed — 日内特征策略会明确失败（不会静默当 0）")
	}

	// QT-12 ⑤：预热收尾时报一次缓存账（backtest_cache_bytes{kind}）。
	rc.publishCacheMetrics()

	// REV-18：qfq 复权基准快照 —— 起跑时读回本区间已冻结的基准（没有则本次
	// 首建并落库）。必须**先于**任何取数发生，否则第一次取数就会以「当前刻度」
	// 自建基准，重跑无从对齐。defer flush 把本次新采集的基准落库（首写者胜），
	// 失败路径也照常冻结：基准是数据的客观属性，与这次策略跑得如何无关。
	adj, err := e.newAdjustmentRunForRun(ctx, rc, params)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.ErrCodeInternal, "failed to load adjustment anchors", "RunBacktest")
	}
	defer adj.flush(ctx, logger)

	// P2-4：上市日历。拿不到就放弃按日在市过滤，并由偏差维如实记上。
	rc.loadListingCalendar(ctx)

	tradingDays, err := e.getTradingDays(ctx, params.StartDate, params.EndDate)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.ErrCodeInternal, "failed to get trading days", "RunBacktest")
	}

	if len(tradingDays) == 0 {
		return nil, apperrors.New(apperrors.ErrCodeDataQuality, "no trading days found in range").WithOperation("RunBacktest")
	}

	logger.Info().Int("trading_days", len(tradingDays)).Msg("Retrieved trading days")

	// QT-63（③-②）：日历与复权锚点都就位后、日循环之前，一次性算出生效输入的
	// 身份并落库。留痕的是**输入**，所以失败/无效 run 同样落（status 的权威
	// 是 backtest_jobs，provenance 表不存状态）。没有 sink 时是 no-op。
	if err := e.recordRunProvenance(ctx, state, tradingDays, adj, logger); err != nil {
		return nil, apperrors.Wrap(err, apperrors.ErrCodeInternal,
			"failed to record run provenance", "RunBacktest")
	}

	ca := e.loadCorporateActions(ctx, params.StartDate, params.EndDate, logger)
	prevCloseCache := make(map[string]float64)
	var pricesCache map[string]float64

	// OBS-01：记录整轮回测里「票池规模」的最大值（逐日）。它不参与任何
	// 交易逻辑，只是把「票池是不是一直为空」如实带出日循环 —— 引擎不新增
	// 跨服务的解析逻辑，空票池的裁定交给 contracts.CheckValidity。
	maxUniverseSize := 0

	// K17：exec-algo 派发器（本 run 私有——Engine 实例可被多个回测并发
	// 共享，父单运行态绝不能跨 run 串。零算法单时它是空转的，默认路径
	// 数值零变化）。见 pkg/backtest/exec_algo.go。
	algoDispatch := execalgo.NewDispatcher()
	// K17-R1：挂载失败运行诊断（本 run 私有，与派发器同生命周期）。
	execAlgoDiag := newExecAlgoRunDiag()
	fillDiag := newFillRunDiag() // REV-09：停牌跳过 / 流动性截断的 run 级计数

	// QT-02 · next_open：T 日收盘决策的信号在 T+1 开盘成交，所以「待成交」
	// 要跨一天活着。same_close 分支不读它（下面的 else 就是既有调用）。
	var pendingSignals []domain.Signal

	for i, date := range tradingDays {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		if i%20 == 0 {
			logger.Debug().
				Int("day", i).
				Int("total", len(tradingDays)).
				Str("date", date.Format("2006-01-02")).
				Msg("Processing day")
		}

		// P2-4：池子按当天在市名单过滤，外加「还持仓的」（含已退市待平仓）。
		held := heldSymbols(state.Tracker)
		universe := rc.eligibleUniverse(params.StockPool, date, held)

		// OBS-01：票池规模统计 —— 见 maxUniverseSize 声明处。
		if n := len(universe); n > maxUniverseSize {
			maxUniverseSize = n
		}

		marketDataCache, pricesCache, stockCache, updatedPrevClose, suspended := e.fetchMarketDataForDay(
			ctx, rc, universe, params, date, prevCloseCache, fillDiag, adj, logger,
		)
		prevCloseCache = updatedPrevClose

		// AUD-22: hand the day's stock metadata to the tracker so it can
		// apply the risk-warning (ST-family) daily buy cap.
		//
		// Refreshed every day, not set once at construction: a stock can be
		// placed under or released from a risk warning at any time, so a
		// one-shot table would apply today's ST list to a 2015 backtest.
		//
		// This is the ONLY population point for that table. If it is ever
		// removed, Tracker.enforceRiskWarningDailyBuy degrades to a no-op
		// that only logs a warning — see its doc comment, and
		// TestEngine_DayLoopPopulatesTrackerStockNames (riskwarning_wiring_test.go),
		// the structural guard that goes red when this Tracker.SetStockNames
		// call disappears. (That guard parses the AST on purpose: a text
		// grep for "SetStockNames" would also match this sentence and the
		// one above it, so a text-based "exactly one" assertion would fail
		// even in the correct state.)
		state.Tracker.SetStockNames(stockNamesFor(stockCache))

		// 摘牌之后还留在账上的持仓，趁这天处理掉。退市当天的价格通常还在
		// （退市整理期），取不到就退回最后一次已知价 —— 与回测末尾的强平
		// 同一套兜底逻辑，但发生在真正摘牌的时候而不是最后一天。
		//
		// K18：摘牌强平是「引擎外改仓」——对每个真被平掉的 symbol 取消其
		// 活跃 exec-algo 父单（剩余未报送子单丢弃），否则后续天的子单会把
		// 已平的仓位买回。
		for _, sym := range rc.forceCloseDelisted(state, pricesCache, date, logger) {
			cancelExecAlgoParentsForSymbol(algoDispatch, sym, cancelReasonDelisted, logger)
		}

		// P1-12：把「回测的今天」交给 tracker，必须在生成信号之前。
		// 策略据此判断今天是不是调仓日 —— 没有它，策略只能退回墙钟，
		// weekly / monthly 的成交就取决于你周几跑（详见 TASKS.md P1-12）。
		state.Tracker.SetAsOf(date)

		regime, err := e.detectRegime(ctx, marketDataCache)
		if err != nil {
			logger.Warn().Err(err).Msg("Failed to detect market regime, using default")
			regime = &domain.MarketRegime{
				Trend:      "sideways",
				Volatility: "medium",
				Sentiment:  0.0,
				Timestamp:  date,
			}
		}

		signals, err := e.getSignals(ctx, rc, params.StrategyName, universe, marketDataCache, date, state.Tracker)
		if err != nil {
			// REV-22-(3)：信号失败当天交易凭空消失，只留一条 Warn 不够——
			// 计数进 run 级诊断（复用 execAlgoRunDiag 通道），logSummary 汇总。
			// 不进 invalid_reasons：单日策略故障不否定整段回测的有效性，
			// 但必须可见。
			execAlgoDiag.recordSignalFailure(date, err)
			logger.Warn().
				Time("date", date).
				Err(err).
				Msg("Failed to get signals, skipping day")
		}

		// K5 切片 2：信号对账观察钩子（撮合前，信号尚未受风控/持仓抵扣
		// 影响，与 paper 侧 PaperSession.SignalCollector 同构）。
		if e.signalObserver != nil {
			e.signalObserver(date, signals)
		}

		// QT-02 · 两条口径在同一个调用点上分叉，其余环节共用：
		//   - same_close（既有行为）：今天的信号 + 今天的收盘可成交价 + 只认今天
		//     决策的信号 —— 与改动前逐字节相同；
		//   - next_open（§8.1 终态）：**昨天收盘**决策的信号今天按开盘价成交，
		//     新鲜度门随之挪到上一个回放日（fillPlan.minSignalDate），今天的
		//     信号进待成交队列等明天。
		// 估值与仓位仍读 pricesCache（当日收盘可成交价）—— 本单只改「哪一天、
		// 什么价成交」。涨跌停/停牌拦截读的是成交那一天的 bar（下面的
		// marketDataCache 就是今天），所以 REV-09 那套判定作用在实际成交日。
		plan := sameClosePlan(pricesCache, date)
		fillSignals := signals
		if fillTiming == FillTimingNextOpen {
			earliestDecision := date
			if i > 0 {
				earliestDecision = tradingDays[i-1]
			}
			fillSignals = pendingSignals
			plan = fillPlan{
				prices:        openPricesForSignals(fillSignals, marketDataCache, fillDiag),
				minSignalDate: earliestDecision,
			}
			pendingSignals = signals
		}
		e.processSignalsAndExecuteTrades(ctx, rc, state, fillSignals, marketDataCache, pricesCache, plan, regime, date, algoDispatch, execAlgoDiag, suspended, fillDiag, logger)

		// K17：exec-algo 到点子单送撮合（在信号处理之后：新挂父单的首片
		// SubmitAt = 信号当日，当日到点即发）。无活跃父单时零开销返回。
		e.processExecAlgoChildren(ctx, state, algoDispatch, marketDataCache, date, logger)

		// K18：止损/止盈走 processStopLosses 的成交分支内联取消（见该函数）；
		// 拆股改仓在这里按其返回的受影响 symbol 取消活跃父单。
		e.processStopLosses(ctx, state, algoDispatch, pricesCache, marketDataCache, suspended, date, logger)

		for _, sym := range e.processCorporateActions(state, ca, date, logger) {
			cancelExecAlgoParentsForSymbol(algoDispatch, sym, cancelReasonSplit, logger)
		}

		state.Tracker.RecordDailyValue(date, pricesCache)
		state.Tracker.AdvanceDay(date)

		for _, symbol := range universe {
			if ohlcvData, ok := marketDataCache[symbol]; ok && len(ohlcvData) > 0 {
				todayBar := ohlcvData[len(ohlcvData)-1]
				if todayBar.LimitUp {
					prevCloseCache[symbol] = todayBar.Close
				} else if todayBar.Close > 0 {
					prevCloseCache[symbol] = todayBar.Close
				}
			}
		}
	}

	lastTradingDay := tradingDays[len(tradingDays)-1]

	// QT-02 · next_open 的收尾账：区间最后一天收盘决策的信号，**没有明天**
	// 可以按开盘成交了。这里是如实计数，不是把它们折到最后一天的收盘价上
	// ——那会让一份 next_open 的结果里混进一笔 same_close 的成交。
	if fillTiming == FillTimingNextOpen {
		fillDiag.recordPendingAtRunEnd(pendingSignals)
	}

	// K17：会话结束裁决 —— 撤销未报完父单的剩余子单（显式裁决与理由见
	// pkg/backtest/exec_algo.go 的 finalizeExecAlgo）。必须在日循环之后、
	// forceCloseAllPositions 之前：撤销只是记账告警，不产生新成交。
	finalizeExecAlgo(algoDispatch, logger)

	e.forceCloseAllPositions(state, pricesCache, lastTradingDay, logger)

	// REV-22-(2)：日循环本身包含最后一天（已在循环内 RecordDailyValue 过），
	// 强平又会改变组合价值——重记前必须先丢掉同日期的旧条目，否则权益曲线
	// 末日出现两个观测，Sharpe / 波动率被多计一个点，前端出现重复点。
	state.Tracker.DropLastRecordedOn(lastTradingDay)
	state.Tracker.RecordDailyValue(lastTradingDay, pricesCache)

	portfolioValues := state.Tracker.GetPortfolioValues()
	trades := state.Tracker.GetTrades()

	result := metrics.GenerateBacktestResult(
		portfolioValues,
		trades,
		params.RiskFreeRate,
		params.StartDate,
		params.EndDate,
		params.InitialCapital,
	)

	// OBS-01：把票池统计挂到结果上（供验证器 / 报告层读取），并跑一次
	// 「有效运行」裁定。裁定只挂在结果上，**不改变正常路径** —— 有效的
	// 回测依旧 Status=completed、nil error；无效的也只是多一组 Code。
	// 指针：nil = 未评估。引擎一定填值（哪怕是 0 = 明确为空），
	// 所以这里取地址即可 —— 见 domain.BacktestResult.UniverseMaxSize 注记。
	result.UniverseMaxSize = &maxUniverseSize
	result.InvalidReasons = InvalidReasonCodes(CheckValidity(&result))

	// K17-R1：exec-algo 挂载失败 —— 信号被拒不是「忽略」。四类失败已在
	// 日循环里分类计数，这里把计数汇总进日志、并把两条稳定 Code 挂进
	// **既有** invalid_reasons 通道（不新增 ResultSpec 字段），run 状态
	// 随之为 invalid。两条 Code 见 contracts/validity.go。
	result.InvalidReasons = append(result.InvalidReasons, execAlgoDiag.invalidReasonCodes()...)
	execAlgoDiag.logSummary(logger)
	fillDiag.logSummary(logger) // REV-09：停牌/截断计数（正常行为，不进 invalid_reasons）

	if len(result.InvalidReasons) > 0 {
		// 无效运行：不再无条件报「Backtest completed」。原因如实带出，
		// 让日志读者一眼看出「这次不算成功，不算观测」。
		logger.Warn().
			Float64("total_return", result.TotalReturn).
			Float64("sharpe_ratio", result.SharpeRatio).
			Float64("max_drawdown", result.MaxDrawdown).
			Int("total_trades", result.TotalTrades).
			Int("universe_max_size", maxUniverseSize).
			Strs("invalid_reasons", result.InvalidReasons).
			Msg("Backtest produced an invalid run — result marked Status=invalid")
	} else {
		logger.Info().
			Float64("total_return", result.TotalReturn).
			Float64("sharpe_ratio", result.SharpeRatio).
			Float64("max_drawdown", result.MaxDrawdown).
			Int("total_trades", result.TotalTrades).
			Msg("Backtest completed")
	}

	return &result, nil
}

// checkCalendarExists verifies the trading calendar has entries for the given range.
func (e *Engine) checkCalendarExists(ctx context.Context, start, end time.Time) (bool, error) {
	e.mu.RLock()
	store := e.store
	e.mu.RUnlock()

	if store != nil {
		days, err := store.GetTradingDates(ctx, start, end)
		if err != nil {
			e.logger.Warn().Err(err).Msg("Store calendar check failed, falling back to provider")
		} else {
			return len(days) > 0, nil
		}
	}

	ok, err := e.effectiveProvider().CheckCalendarExists(ctx, start, end)
	if err != nil {
		e.logger.Warn().Err(err).Msg("Provider calendar check failed")
		return false, nil
	}
	return ok, nil
}

// SetFundamentals 让调用方直接注入财报数据，跳过预热（P2-12）。
//
// 给测试和「没有 Postgres 也想跑估值策略」的场景留的门：数据从哪来由调用
// 方负责，格式要求见 strategy.FundamentalSeries（Date 必须是可用日）。
func (e *Engine) SetFundamentals(records map[string]strategy.FundamentalSeries) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.fundamentals = records
}

// SetListingWindows 让调用方直接注入上市日历（P2-4），跳过预热。
//
// 给测试和「stocks 表在别处」的场景留的门。传 nil = 没有这份数据，
// 引擎会放弃过滤并如实上报（见 listing 字段注释）。
func (e *Engine) SetListingWindows(windows map[string]storage.ListingWindow) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.listing = windows
}

// ListingWindows 返回当前使用的上市日历（并发安全，只读）。
func (e *Engine) ListingWindows() map[string]storage.ListingWindow {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.listing
}

// heldSymbols 取出当前还持仓的股票。
//
// universe 过滤要用到它：已退市但还持仓的票不能从池子里消失，否则永远
// 平不掉仓。只统计数量非零的 —— 数量为 0 的残留条目不该撑开 universe。
func heldSymbols(tracker *Tracker) map[string]bool {
	out := make(map[string]bool)
	for symbol, pos := range tracker.GetAllPositions() {
		if abs(pos.Quantity) > 1e-8 {
			out[symbol] = true
		}
	}
	return out
}

// getTradingDays retrieves trading days from data service.
func (e *Engine) getTradingDays(ctx context.Context, start, end time.Time) ([]time.Time, error) {
	days, err := e.effectiveProvider().GetTradingDays(ctx, start, end)
	if err != nil {
		return nil, err
	}

	sort.Slice(days, func(i, j int) bool {
		return days[i].Before(days[j])
	})

	return days, nil
}

// dateRangeBounds was promoted to DateRangeBounds (exported) in
// pkg/backtest/cache.go (P1-16 ADR-020). The unexported alias below is
// kept for any in-package callers that still reference the old name.
func dateRangeBounds(bars []domain.OHLCV, start, end time.Time) (int, int) {
	return cache.DateRangeBounds(bars, start, end)
}

// detectRegime detects market regime using risk service.
func (e *Engine) detectRegime(ctx context.Context, marketData map[string][]domain.OHLCV) (*domain.MarketRegime, error) {
	// 拼接顺序必须确定：allData 的顺序会进到 DetectRegime 的统计里
	// （浮点求和、均线），进而改变仓位倍数的判断 —— 顺序一变，同一天
	// 买卖的数量就不同，回测不可复现（P1-14）。
	var allData []domain.OHLCV
	for _, sym := range sortedKeys(marketData) {
		allData = append(allData, marketData[sym]...)
	}

	if len(allData) < 20 {
		return &domain.MarketRegime{
			Trend:      "sideways",
			Volatility: "medium",
			Sentiment:  0.0,
			// 同上：不用墙钟。取不到就留零值 —— 假日期比没日期更危险。
			Timestamp: strategy.LatestBarDate(marketData),
		}, nil
	}

	e.mu.RLock()
	rm := e.riskManager
	e.mu.RUnlock()

	if rm != nil {
		regime, err := rm.DetectRegime(ctx, allData)
		if err != nil {
			return nil, apperrors.Wrap(err, apperrors.ErrCodeInternal, "in-process regime detection failed", "detectRegime")
		}
		return regime, nil
	}

	url := fmt.Sprintf("%s/detect_regime", e.riskServiceURL)

	reqBody := struct {
		Data []domain.OHLCV `json:"data"`
	}{Data: allData}

	resp, err := e.httpClient.Post(ctx, url, reqBody)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, apperrors.Unavailable("risk", fmt.Errorf("HTTP %d", resp.StatusCode))
	}

	var regime domain.MarketRegime
	if err := json.Unmarshal(resp.Body, &regime); err != nil {
		return nil, err
	}

	return &regime, nil
}

// sortedKeys 返回 map 的键，按字典序排好。
//
// 持仓、价格这类 map 的遍历顺序是随机的，而下游（止损检查、风险管理器
// 内部的求和）对顺序敏感：同一天先平 A 还是先平 B，成交序列就不同。
// 要复现一份回测，就得先定序（P1-14，与 tracker 包里的同名 helper 同理）。
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// getSignals retrieves trading signals via the SignalProvider 契约（K8 ·
// SignalSpec）。默认供给是 strategy.LocalSignalProvider——注册表 miss 时
// fail-loud 的 OBS-11 口径与批式/流式双模式逻辑都在那边，行为与旧实现
// 逐字节一致。
//
// sigProvider 来自本 run 的状态包（见 newRunContext）：本方法自己不新建，
// 否则每交易日换一份策略实例、流式状态当天就丢。rc 在 run 流程里恒非 nil
// —— 它是 RunBacktest 第一件事建出来的那个对象。
func (e *Engine) getSignals(ctx context.Context, rc *runContext, strategyName string, stockPool []string, marketData map[string][]domain.OHLCV, date time.Time, tracker *Tracker) ([]domain.Signal, error) {
	// 组合快照提前到调用方计算（GetPortfolio 纯读、可提前调用）。tracker
	// 为 nil（如注册表 miss 的装配错误路径）时 Portfolio 留 nil，流式路径
	// 本就不用它。
	var portfolio *domain.Portfolio
	if tracker != nil {
		portfolio = tracker.GetPortfolio(extractLatestPrices(marketData))
	}

	// 信号供给是本 run 的（QT-11），日内特征按当日绑定（D6）：
	// 逐日改写一个跨 run 共享的 provider 会把上一笔的窗口带进这一笔。
	// 注入过自定义 provider（QT-14 的会话对象）时**不动它** —— 那个对象
	// 不属于本 run，逐日 Set 回去正是 QT-11 当初要堵的形状。
	sigProvider := rc.provider
	if rc.e.signalProvider == nil {
		if local, ok := sigProvider.(*strategy.LocalSignalProvider); ok {
			local.SetIntradayProvider(rc.intradayFeatureProvider(date))
		}
	}

	signals, err := sigProvider.Signals(ctx, strategy.Scope{
		Strategy:   strategyName,
		Universe:   stockPool,
		MarketData: marketData,
		Portfolio:  portfolio,
	}, date)
	if err != nil {
		return nil, err
	}
	domainSignals := convertStrategySignals(signals, date)

	e.logger.Debug().
		Str("strategy", strategyName).
		Int("signals", len(domainSignals)).
		Msg("Generated signals from local registry")
	return domainSignals, nil
}

// extractLatestPrices builds a symbol → latest close price map from market data.
func extractLatestPrices(marketData map[string][]domain.OHLCV) map[string]float64 {
	prices := make(map[string]float64)
	for sym, bars := range marketData {
		if len(bars) > 0 {
			prices[sym] = bars[len(bars)-1].Close
		}
	}
	return prices
}

// convertStrategySignals converts strategy plugin signals into domain Signals,
// filtering out hold actions and normalizing direction / order type / price.
func convertStrategySignals(signals []strategy.Signal, defaultDate time.Time) []domain.Signal {
	domainSignals := make([]domain.Signal, 0, len(signals))
	for _, s := range signals {
		if s.Action == "hold" {
			continue
		}
		dir := resolveDirection(s)
		if dir == "" {
			continue
		}

		sigDate := defaultDate
		if d, ok := s.Date.(time.Time); ok && !d.IsZero() {
			sigDate = d
		}

		factors := s.Factors
		if factors == nil {
			factors = make(map[string]float64)
		}
		metadata := s.Metadata
		if metadata == nil {
			metadata = make(map[string]interface{})
		}

		limitPrice := s.LimitPrice
		if limitPrice == 0 {
			limitPrice = s.Price
		}
		orderType := s.OrderType
		if orderType == "" {
			orderType = domain.OrderTypeMarket
		}

		domainSignals = append(domainSignals, domain.Signal{
			Symbol:         s.Symbol,
			Date:           sigDate,
			Direction:      dir,
			Strength:       s.Strength,
			Factors:        factors,
			Metadata:       metadata,
			LimitPrice:     limitPrice,
			OrderType:      orderType,
			CompositeScore: s.Strength,
			// K17：执行算法指定原样透传（零值 = 未指定 = 默认路径不变）。
			ExecAlgo:    s.ExecAlgo,
			ExecSlices:  s.ExecSlices,
			ExecProfile: s.ExecProfile,
		})
	}
	return domainSignals
}

// resolveDirection maps a strategy signal's Action/Direction to a domain Direction.
// Returns empty string when the signal should be skipped.
func resolveDirection(s strategy.Signal) domain.Direction {
	dir := s.Direction
	if dir != "" && dir != domain.DirectionHold {
		return dir
	}
	if s.Action == "buy" {
		return domain.DirectionLong
	}
	if s.Action == "sell" {
		return domain.DirectionClose
	}
	return ""
}

// calculatePosition calculates position size using risk service.
// When an in-process RiskManager is set (via SetRiskManager), computes locally
// without HTTP calls. Otherwise falls back to the external risk-service.
func (e *Engine) calculatePosition(ctx context.Context, rc *runContext, signal domain.Signal, portfolio *domain.Portfolio, regime *domain.MarketRegime, currentPrice float64) (domain.PositionSize, error) {
	e.mu.RLock()
	rm := e.riskManager
	e.mu.RUnlock()

	if rm != nil {
		// P1-16 (ADR-020): read cached snapshot via CacheManager.Peek
		// (peek-only — no provider fallback; calculatePosition is a
		// best-effort read of already-warmed data).
		ohlcv := rc.peekOHLCV(signal.Symbol)
		pos, err := rm.CalculatePosition(ctx, signal, portfolio, regime, currentPrice, ohlcv)
		if err != nil {
			return domain.PositionSize{}, apperrors.Wrap(err, apperrors.ErrCodeInternal, "in-process position calculation failed", "calculatePosition")
		}
		return pos, nil
	}

	url := fmt.Sprintf("%s/calculate_position", e.riskServiceURL)

	reqBody := struct {
		Signal       domain.Signal       `json:"signal"`
		Portfolio    domain.Portfolio    `json:"portfolio"`
		Regime       domain.MarketRegime `json:"regime"`
		CurrentPrice float64             `json:"current_price"`
	}{
		Signal:       signal,
		Portfolio:    *portfolio,
		Regime:       *regime,
		CurrentPrice: currentPrice,
	}

	resp, err := e.httpClient.Post(ctx, url, reqBody)
	if err != nil {
		return domain.PositionSize{}, err
	}

	if resp.StatusCode != http.StatusOK {
		return domain.PositionSize{}, apperrors.Unavailable("risk", fmt.Errorf("HTTP %d", resp.StatusCode))
	}

	var positionSize domain.PositionSize
	if err := json.Unmarshal(resp.Body, &positionSize); err != nil {
		return domain.PositionSize{}, err
	}

	return positionSize, nil
}

// calculatePositionsBatch calculates position sizes for multiple signals at once.
// Uses the RiskManager's batch method when available for better performance.
func (e *Engine) calculatePositionsBatch(
	ctx context.Context,
	rc *runContext,
	signals []domain.Signal,
	portfolio *domain.Portfolio,
	regime *domain.MarketRegime,
	pricesCache map[string]float64,
	marketDataCache map[string][]domain.OHLCV,
) (map[string]domain.PositionSize, error) {
	e.mu.RLock()
	rm := e.riskManager
	e.mu.RUnlock()

	if rm != nil {
		return rm.CalculatePositionsBatch(ctx, signals, portfolio, regime, pricesCache, marketDataCache)
	}

	results := make(map[string]domain.PositionSize, len(signals))
	for _, sig := range signals {
		ps, err := e.calculatePosition(ctx, rc, sig, portfolio, regime, pricesCache[sig.Symbol])
		if err != nil {
			continue
		}
		results[sig.Symbol] = ps
	}
	return results, nil
}

// checkStopLosses checks and triggers stop losses using risk service.
// When an in-process RiskManager is set (via SetRiskManager), computes locally
// without HTTP calls. Otherwise falls back to the external risk-service.
func (e *Engine) checkStopLosses(ctx context.Context, tracker *Tracker, prices map[string]float64) ([]domain.StopLossEvent, error) {
	positions := tracker.GetAllPositions()
	if len(positions) == 0 {
		return nil, nil
	}

	e.mu.RLock()
	rm := e.riskManager
	e.mu.RUnlock()

	if rm != nil {
		// 持仓来自 map，遍历顺序随机 —— 定序后再交给风险管理器（P1-14）。
		var positionsList []domain.Position
		for _, sym := range sortedKeys(positions) {
			positionsList = append(positionsList, positions[sym])
		}
		events, err := rm.CheckStopLoss(ctx, positionsList, prices)
		if err != nil {
			return nil, apperrors.Wrap(err, apperrors.ErrCodeInternal, "in-process stop loss check failed", "checkStopLosses")
		}
		return events, nil
	}

	url := fmt.Sprintf("%s/check_stoploss", e.riskServiceURL)

	// Convert positions map to slice
	var positionsList []domain.Position
	for _, sym := range sortedKeys(positions) {
		positionsList = append(positionsList, positions[sym])
	}

	reqBody := struct {
		Positions map[string]float64 `json:"prices"`
	}{
		Positions: prices,
	}

	resp, err := e.httpClient.Post(ctx, url, reqBody)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, apperrors.Unavailable("risk", fmt.Errorf("HTTP %d", resp.StatusCode))
	}

	var events struct {
		Events []domain.StopLossEvent `json:"events"`
	}
	if err := json.Unmarshal(resp.Body, &events); err != nil {
		return nil, err
	}

	return events.Events, nil
}

// checkStopLossesWithATR checks stop losses using pre-computed ATR data.
// This avoids redundant ATR calculation inside the stop loss checker.
func (e *Engine) checkStopLossesWithATR(ctx context.Context, tracker *Tracker, prices map[string]float64, precomputedATR map[string]float64) ([]domain.StopLossEvent, error) {
	positions := tracker.GetAllPositions()
	if len(positions) == 0 {
		return nil, nil
	}

	e.mu.RLock()
	rm := e.riskManager
	e.mu.RUnlock()

	if rm != nil {
		// 持仓来自 map，遍历顺序随机 —— 定序后再交给风险管理器（P1-14）。
		var positionsList []domain.Position
		for _, sym := range sortedKeys(positions) {
			positionsList = append(positionsList, positions[sym])
		}
		regime := risk.InferRegimeFromMarket(positionsList, prices)
		events, err := rm.GetStopLossChecker().CheckStopLossWithRegime(ctx, positionsList, prices, precomputedATR, regime)
		if err != nil {
			return nil, apperrors.Wrap(err, apperrors.ErrCodeInternal, "in-process stop loss check with ATR failed", "checkStopLossesWithATR")
		}
		return events, nil
	}

	return e.checkStopLosses(ctx, tracker, prices)
}

// GetBacktestResult retrieves the result of a completed backtest.
func (e *Engine) GetBacktestResult(backtestID string) (*domain.BacktestResult, error) {
	state, ok := e.stateStore.Get(backtestID)
	if !ok || state == nil {
		return nil, apperrors.NotFound("backtest", backtestID)
	}

	status := state.GetStatus()
	// OBS-01（切片 2）：同时放行 "invalid" —— 无效运行同样已经产出了结果，
	// 必须可读。否则「为什么无效」（result.InvalidReasons）在 UI / 报告层
	// 不可见，调用方只会拿到一个 409，把「跑完了但结果无效」误报成
	// 「还没跑完」。running / failed 仍拒绝（它们没有可读结果）。
	if status != "completed" && status != "invalid" {
		return nil, apperrors.New(apperrors.ErrCodeConflict, fmt.Sprintf("backtest not completed: %s", status)).WithOperation("GetBacktestResult")
	}

	return state.GetResult(), nil
}

// GetBacktestTrades retrieves trades for a backtest.
func (e *Engine) GetBacktestTrades(backtestID string) ([]domain.Trade, error) {
	state, ok := e.stateStore.Get(backtestID)
	if !ok || state == nil {
		return nil, apperrors.NotFound("backtest", backtestID)
	}

	return state.Tracker.GetTrades(), nil
}

// GetBacktestEquity retrieves equity curve for a backtest.
func (e *Engine) GetBacktestEquity(backtestID string) ([]domain.PortfolioValue, error) {
	state, ok := e.stateStore.Get(backtestID)
	if !ok || state == nil {
		return nil, apperrors.NotFound("backtest", backtestID)
	}

	return state.Tracker.GetEquityCurve(), nil
}

// GetBacktestStatus returns the status of a backtest.
func (e *Engine) GetBacktestStatus(backtestID string) (string, error) {
	state, ok := e.stateStore.Get(backtestID)
	if !ok || state == nil {
		return "", apperrors.NotFound("backtest", backtestID)
	}

	return state.GetStatus(), nil
}

func (e *Engine) GetBacktestParams(backtestID string) (domain.BacktestParams, error) {
	state, ok := e.stateStore.Get(backtestID)
	if !ok || state == nil {
		return domain.BacktestParams{}, apperrors.NotFound("backtest", backtestID)
	}

	return state.Params, nil
}

// LoadOHLCVInMemory directly populates the L1 in-memory OHLCV cache.
// The map key is symbol; each slice is sorted by date ascending.
// After calling this, getOHLCV returns cached data instantly (L1 hit).
// Pass nil to clear the L1 cache (forces provider fallback on next getOHLCV).
//
// P1-16 (ADR-020): thin shim to cache.Load().
func (e *Engine) LoadOHLCVInMemory(data map[string][]domain.OHLCV) {
	e.cache.Load(data)
}

// CacheManager returns the underlying CacheManager sub-component (P1-16).
// Prefer this accessor in new code over the LoadOHLCVInMemory shim.
func (e *Engine) CacheManager() *CacheManager {
	return e.cache
}

// LoadFactorCache loads pre-computed factor z-scores into the L1 factor cache.
// The input is typically from storage.GetFactorCacheRange() or data.LoadFactorCacheIntoMap().
// After calling this, GetFactorZScore returns cached z-scores instantly (L1 hit).
// Pass nil to clear the factor cache.
//
// P1-16 (ADR-020): thin shim to factor.Load().
func (e *Engine) LoadFactorCache(data map[domain.FactorType]map[time.Time]map[string]float64) {
	e.factor.Load(data)
}

// FactorCache returns the underlying FactorCacheAccessor sub-component (P1-16).
// Prefer this accessor in new code over the LoadFactorCache shim.
func (e *Engine) FactorCache() *FactorCacheAccessor {
	return e.factor
}

// GetFactorZScore returns the pre-computed z-score for a given factor, date, and symbol.
// Returns (0, false) if the factor cache is not loaded or the entry doesn't exist.
//
// P1-16 (ADR-020): thin shim to factor.Get().
func (e *Engine) GetFactorZScore(factor domain.FactorType, date time.Time, symbol string) (float64, bool) {
	return e.factor.Get(factor, date, symbol)
}

// SetRiskManager injects an in-process risk manager.
// When set, calculatePosition/checkStopLosses/detectRegime use local computation
// (zero HTTP latency) instead of calling the external risk-service.
// Pass nil to clear and fall back to HTTP-based risk service calls.
func (e *Engine) SetRiskManager(rm *risk.RiskManager) {
	e.mu.Lock()
	e.riskManager = rm
	e.mu.Unlock()
}

// GetRiskManager returns the currently attached in-process RiskManager (P1-19).
// Returns nil if not set.
func (e *Engine) GetRiskManager() *risk.RiskManager {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.riskManager
}

// SetParallelWorkers sets the number of concurrent workers for per-stock data fetching.
// Must be called before RunBacktest. Values <= 0 mean sequential (no parallelism).
func (e *Engine) SetParallelWorkers(n int) {
	e.parallelWorkers = n
}

// getStock retrieves stock info (Name, ListDate) from the data service.
func (e *Engine) getStock(ctx context.Context, symbol string) (domain.Stock, error) {
	return e.effectiveProvider().GetStock(ctx, symbol)
}

// hasSTPrefix returns true if the stock name starts with an ST-family
// risk-warning prefix ("ST", "*ST", "SST", "S*ST").
//
// AUD-08 (ODR-065 H3): this used to be a broken inline check —
//
//	prefix := name[:2]
//	return prefix == "ST" || prefix == "*ST" || prefix == "SST" || prefix == "S*ST"
//
// — where the last three alternatives are 3–4 characters long and can
// never equal a 2-character slice, so only "ST" ever matched. The
// condition looked like it handled all four forms and in fact handled
// one. The logic now lives in IsRiskWarningName (pricelimit.go), which
// the price-limit resolver also uses.
//
// Kept as a thin wrapper because existing call sites and tests refer
// to this name.
func hasSTPrefix(name string) bool {
	return IsRiskWarningName(name)
}

// SetLiveTrader injects a LiveTrader for bridging backtest signals to live/paper trading.
// When set, the engine can optionally execute signals through the trader instead of
// only simulating internally via Tracker. This is the primary hook for transitioning
// from backtest → paper trading → live trading.
// Pass nil to disable live trading and use pure simulation.
//
// P1-17 (ADR-020): thin shim to liveBridge.Set(). Retained for
// backward compatibility (6 months); new code should use
// e.liveBridge().Set() or EngineOption WithLiveTrader (P1-19).
func (e *Engine) SetLiveTrader(trader live.LiveTrader) {
	e.liveBridge.Set(trader)
}

// SetExecutionService injects a custom ExecutionService for order execution.
// When set, the engine uses this service for all trade execution instead of
// Tracker's built-in logic. Pass nil to fall back to Tracker execution.
//
// P1-17 (ADR-020): thin shim to executionBridge.Set(). Retained for
// backward compatibility (6 months); new code should use
// e.executionBridge().Set() or EngineOption WithExecutionService (P1-19).
func (e *Engine) SetExecutionService(svc ExecutionService) {
	e.executionBridge.Set(svc)
}

// GetExecutionService returns the current ExecutionService.
//
// P1-17 (ADR-020): thin shim to executionBridge.Get(). Retained for
// backward compatibility.
func (e *Engine) GetExecutionService() ExecutionService {
	return e.executionBridge.Get()
}

// GetLiveTrader returns the currently attached LiveTrader, or nil if none.
//
// P1-17 (ADR-020): thin shim to liveBridge.Get(). Retained for
// backward compatibility.
func (e *Engine) GetLiveTrader() live.LiveTrader {
	return e.liveBridge.Get()
}

// LiveBridge returns the underlying LiveBridge sub-component (P1-17).
// Prefer this accessor in new code over the SetLiveTrader shim; the
// shim is kept for backward compat only.
func (e *Engine) LiveBridge() *LiveBridge {
	return e.liveBridge
}

// ExecutionBridge returns the underlying ExecutionBridge sub-component (P1-17).
// Prefer this accessor in new code over the SetExecutionService shim.
func (e *Engine) ExecutionBridge() *ExecutionBridge {
	return e.executionBridge
}

// StateStore returns the underlying StateStore (P1-18).
// The returned store is safe for concurrent use; callers may invoke
// Get / Put / Delete / Len / Evict directly. Typical use is for
// observability (e.g. "how many backtests are tracked?") or for
// out-of-band GC sweeps.
func (e *Engine) StateStore() StateStore {
	return e.stateStore
}

// EvictStates performs a bulk eviction of older backtest states, keeping
// at most keepN entries. keepN=0 evicts all. Returns the number of
// states removed. Useful for periodic GC under bursty batch load.
func (e *Engine) EvictStates(keepN int) int {
	return e.stateStore.Evict(keepN)
}

// ExecuteSignalViaLiveTrader executes a single trading signal through the attached LiveTrader.
// This is the bridge between backtest signal generation and live/paper order execution.
// Returns the order result or nil if no LiveTrader is attached.
// The signal's Direction determines buy (DirectionLong) or sell (DirectionClose).
//
// P1-17 (ADR-020): thin shim to liveBridge.ExecuteSignal(). All order
// type / price / quantity logic now lives in LiveBridge; behavior is
// byte-equivalent to the previous in-line implementation.
func (e *Engine) ExecuteSignalViaLiveTrader(ctx context.Context, signal domain.Signal, currentPrice float64) (*live.OrderResult, error) {
	return e.liveBridge.ExecuteSignal(ctx, signal, currentPrice)
}

// ExecuteSignalsViaLiveTrader executes multiple signals through the LiveTrader in batch.
// This is useful for daily rebalancing where multiple signals are generated at once.
// Returns a map of symbol → order result for successful executions.
//
// P1-17 (ADR-020): thin shim to liveBridge.ExecuteSignals().
func (e *Engine) ExecuteSignalsViaLiveTrader(ctx context.Context, signals []domain.Signal, prices map[string]float64) map[string]*live.OrderResult {
	return e.liveBridge.ExecuteSignals(ctx, signals, prices)
}

// HealthCheckLiveTrader checks the health of the attached LiveTrader.
// Returns nil if healthy or no trader attached, error otherwise.
//
// P1-17 (ADR-020): thin shim to liveBridge.HealthCheck().
func (e *Engine) HealthCheckLiveTrader(ctx context.Context) error {
	return e.liveBridge.HealthCheck(ctx)
}
