package main

// This file keeps the analysis-only builders and thin delegation shims
// to internal/bootstrap (AI 拆仓阶段 1：共享装配上收为 bootstrap 包，
// AI 部分随阶段 2 迁往 tellurion-agent 仓）。
//
// 委托 shim 的理由：main.go / deps.go / 测试对这批函数的调用点与断言
// **零改动**（沿用 K1 切片 2「现有装配块一行不改」的同一裁决精神）。
// shim 体内的转发是唯一允许的改动；任何行为差异都必须发生在
// bootstrap 包里并被其测试覆盖。

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-contracts/engine"
	"github.com/spf13/viper"
	"tellurion/cmd/analysis/wiring"
	"tellurion/pkg/alert"
	"tellurion/pkg/auth"
	"tellurion/pkg/authz"
	"tellurion/pkg/backtest"
	"tellurion/pkg/backtest/nautilus"
	"tellurion/pkg/bootstrap"
	"tellurion/pkg/data"
	"tellurion/pkg/live"
	"tellurion/pkg/marketdata"
	"tellurion/pkg/observability"
	"tellurion/pkg/risk"
	"tellurion/pkg/storage"
	"tellurion/pkg/strategy"
)

// ─── 委托 shim（共享 builder 已上收 internal/bootstrap）────────────────

func initMetrics(logger zerolog.Logger) *observability.Metrics {
	return bootstrap.InitMetrics(logger)
}

// loadConfig reads the analysis-service YAML config (bootstrap.LoadConfig
// with the analysis default path).
func loadConfig(logger zerolog.Logger) *viper.Viper {
	return bootstrap.LoadConfig(logger, "config/analysis-service.yaml")
}

func initStore(v *viper.Viper, logger zerolog.Logger) *storage.PostgresStore {
	return bootstrap.InitStore(v, logger)
}

// InsecureExposureLoopbackPublished 别名保留（取值唯一权威在 bootstrap）。
const InsecureExposureLoopbackPublished = bootstrap.InsecureExposureLoopbackPublished

func authExposureOK(exposure string) bool { return bootstrap.AuthExposureOK(exposure) }

func decideAuthStartup(secret string, allowInsecure bool, bindHost string, insecureExposure string) bootstrap.AuthStartup {
	return bootstrap.DecideAuthStartup(secret, allowInsecure, bindHost, insecureExposure)
}

func isLoopbackHost(host string) bool { return bootstrap.IsLoopbackHost(host) }

func initAuth(v *viper.Viper, store *storage.PostgresStore, logger zerolog.Logger) *auth.Service {
	return bootstrap.InitAuth(v, store, logger)
}

func rateLimitPerMinute(v *viper.Viper) int { return bootstrap.RateLimitPerMinute(v) }

func applyGinMode(v *viper.Viper, logger zerolog.Logger) { bootstrap.ApplyGinMode(v, logger) }

func buildRouter(authSvc *auth.Service, v *viper.Viper, logger zerolog.Logger) *gin.Engine {
	router := bootstrap.BuildRouter(authSvc, v, logger)
	// REV-17：变更端点按「端点 × 最低角色」明文表执法（route_policy.go）。
	// 必须在 registerRoutes 之前挂：gin 在**注册路由时**就把当时的 handler
	// 链快照进路由节点，注册之后再 Use 不会生效。
	router.Use(authz.Middleware(authSvc, analysisRoutePolicy))
	return router
}

func startHTTPServer(router *gin.Engine, v *viper.Viper, logger zerolog.Logger) *http.Server {
	return bootstrap.StartHTTPServer(router, v, logger, "Analysis Service")
}

func waitForShutdown() os.Signal { return bootstrap.WaitForShutdown() }

func gracefulShutdown(srv *http.Server, jobService *backtest.JobService, alertManager *alert.AlertManager, store *storage.PostgresStore, logger zerolog.Logger) {
	bootstrap.GracefulShutdown(srv, jobService, alertManager, store, logger)
}

func newRateLimiter(rate int, window time.Duration) *bootstrap.RateLimiter {
	return bootstrap.NewRateLimiter(rate, window)
}

var rateLimitExemptPaths = bootstrap.RateLimitExemptPaths

func isRateLimitExempt(path string) bool { return bootstrap.IsRateLimitExempt(path) }

func initLogger() zerolog.Logger { return bootstrap.InitLogger() }

// ─── analysis 独有 builder（不随 AI 拆仓迁移）─────────────────────────

// buildAlertSystem constructs the AlertManager + PeriodicAlertLoop
// (P2 alert, ODR-025) from viper config under alert.*.
//
// 返回 error 只来自 QT-42 X3c 的告警回传装配（显式启用但配置不合法）：
// 调用方（main.go）据此 fail-fast，不让「risk 侧 alerts 恒空集」成为
// 一次坏配置的静默后果。
func buildAlertSystem(v *viper.Viper, executionTrader live.LiveTrader, riskManager *risk.RiskManager, logger zerolog.Logger) (*alert.AlertManager, *PeriodicAlertLoop, error) {
	alertCfg := alert.AlertManagerConfig{
		MaxPositionWeight: v.GetFloat64("alert.max_position_weight"),
		MaxSectorWeight:   v.GetFloat64("alert.max_sector_weight"),
		MaxDrawdown:       v.GetFloat64("alert.max_drawdown"),
		DailyLossLimit:    v.GetFloat64("alert.daily_loss_limit"),
		FailureRateLimit:  v.GetFloat64("alert.failure_rate_limit"),
		WebhookURL:        v.GetString("alert.webhook_url"),
		WebhookTimeout:    time.Duration(v.GetInt("alert.webhook_timeout_sec")) * time.Second,
	}
	if alertCfg.WebhookTimeout == 0 {
		alertCfg.WebhookTimeout = 5 * time.Second
	}
	recorder := alert.NewRecorderChannel(v.GetInt("alert.recorder_capacity"))
	alertManager := alert.NewAlertManager(alertCfg, logger)
	alertManager.AddChannel(recorder)

	// QT-42 X3c 的 core 腿：detector / Fire 出来的告警按 `POST /api/risk/events`
	// 回传给 risk 服务（risk 的 `monitor/alerts` 与 `monitor/evaluate` 只读
	// `risk.risk_events`，没有这条腿就恒是空集）。挂在 recorder 之后 ⇒ 与
	// LogChannel / WebhookChannel 共用同一次 dispatch，不需要第二条循环。
	if err := wiring.AttachRiskEventsFromConfig(v, alertManager, logger); err != nil {
		return nil, nil, err
	}

	alertLoopCfg := PeriodicAlertConfig{
		Interval:     time.Duration(v.GetInt("alert.interval_sec")) * time.Second,
		HistoryLimit: v.GetInt("alert.history_limit"),
		Enabled:      v.GetBool("alert.enabled"),
	}
	if alertLoopCfg.Interval == 0 {
		alertLoopCfg.Interval = 5 * time.Minute
	}
	if alertLoopCfg.HistoryLimit == 0 {
		alertLoopCfg.HistoryLimit = 100
	}
	alertHistory := NewAlertHistory(alertLoopCfg.HistoryLimit)
	alertLoop := NewPeriodicAlertLoop(alertLoopCfg, alertManager, executionTrader, riskManager, alertHistory, logger)
	logger.Info().
		Bool("enabled", alertLoopCfg.Enabled).
		Dur("interval", alertLoopCfg.Interval).
		Int("history_limit", alertLoopCfg.HistoryLimit).
		Int("recorder_capacity", recorder.Len()).
		Msg("AlertManager + PeriodicAlertLoop attached in-process (P2 alert)")
	return alertManager, alertLoop, nil
}

// dataServices bundles the data-layer services that depend on the
// Postgres store and backtest engine.
type dataServices struct {
	DataAdapter      *marketdata.DataAdapter
	JobService       *backtest.JobService
	WFEngine         *backtest.WalkForwardEngine
	BatchEngine      *backtest.BatchEngine
	FactorAttributor *data.FactorAttributor

	// EngineRegistry（K10）—— 内核路由注册表。装配期注册 go 内核
	// （现有引擎，默认名）；同步 handler / 异步 job / batch 执行期经
	// Resolve 取 runner。walk-forward 走 newEngine 工厂而不是注册表里
	// 那份共享实例：拿的是每窗口一份干净装配（state store / 桥接组件）。
	// 这**不是**缓存隔离的要求 —— QT-12 起可变运行态住在 per-run 的
	// runContext 里，共享实例不会串扰（见 buildDataServices）。
	EngineRegistry *contracts.EngineRegistry
}

// buildDataServices constructs the data adapter, job/walk-forward/batch
// engines, and factor attributor from the store and engine. The HTTP
// provider is reused for the DataAdapter fallback path.
//
// newEngine 为每个 walk-forward 窗口构造一个独立的 Engine。**QT-12 之后
// 这不再是防缓存串扰的要求**：引擎级可变运行态（OHLCV / 因子 / 基本面 /
// 上市日历）已经搬进一个 run 一份的 runContext，共享 Engine 只共享装配。
// 工厂保留下来的理由是「每窗口一份干净装配」（state store / 桥接组件），
// 传 nil 退回共享 engine 从此是合法装配，不是应急。
func buildDataServices(
	store *storage.PostgresStore,
	engine *backtest.Engine,
	dataAdapter *marketdata.DataAdapter,
	logger zerolog.Logger,
	newEngine func() (*backtest.Engine, error),
	m *observability.Metrics,
	marketRouter *contracts.MarketRouter,
) *dataServices {
	// K1 切片 3：DataAdapter 由 data-engine 模块构造（Boot 第 3 位）并经
	// 参数传入 —— 本函数不再自建（同一进程两条 adapter = 双真相）。
	engine.SetDataAdapter(dataAdapter)

	// QT-12 ⑤ 监控接线：准入闸的读数（backtest_runs_active /
	// backtest_runs_queued / backtest_cache_bytes{kind} /
	// backtest_wasm_bytes）经观察面落到 Prometheus。
	//
	// 编译期断言：*observability.Metrics 必须实现 backtest.RunObserver
	// —— 接口由消费方（pkg/backtest）定义，它不反向依赖 pkg/observability，
	// 范式与上面 K16 ② 的 nautilus.RunObserver 一致。断言必须用具体类型：
	// nil 指针同样满足接口，写成变量断言才挡得住签名漂移。
	var _ backtest.RunObserver = (*observability.Metrics)(nil)
	engine.SetRunObserver(m)

	// QT-25 ③：symbol scope 分片的三项指标（wasm_instances_live /
	// wasm_bytes_live / wasm_batch_size）走同一条「消费方定义窄接口」范式。
	var _ backtest.WASMShardObserver = (*observability.Metrics)(nil)
	engine.SetWASMShardObserver(m)

	// K10：执行器注册表。装配期注册 go 内核（现有实现，默认名）——
	// 「默认选中」来自 RunSpec.kernel 空值时的 EffectiveKernel 解析，
	// 不来自注册表预设。运行期不改（无热插拔）。
	engineRegistry := contracts.NewEngineRegistry()
	engineRegistry.Register(contracts.DefaultKernel, engine)

	// K13：注册 NT 侧车内核（名 "nautilus"）。侧车是独立 Python 进程，只经
	// 回环、只经 EngineRunner 契约交互（ADR-031 D5）——进程隔离，不直连
	// 数据库，Go 侧不留任何 NT 依赖。
	//
	// K16 ④ 生产路由开关：nautilus **默认不注册**（$NT_SIDECAR_ENABLED
	// 缺省 = go-only）。显式开启后才注册——这是生产放量的控制点：默认
	// 形态下 kernel="nautilus" 的请求在 Resolve 处 fail-closed（点名内核名
	// + 已注册集合，绝不静默回退 go 内核，FR-1.3）；开启后可用性失败在
	// 执行期显式透出为 failed（runner 不吞错）。地址来自 $NT_SIDECAR_URL
	// （缺省 DefaultURL 127.0.0.1:9100）。开关值非法（打错字）在装配期
	// 直接报错退出，不静默当关闭。
	//
	// K16 ② 监控接线：runner 带 observability.Metrics 观察器——每次侧车
	// round trip 记 nt_sidecar_calls_total{outcome} + nt_sidecar_call_duration_
	// seconds{outcome}（延迟/失败率可抓）。编译期断言：Metrics 必须实现
	// nautilus.RunObserver（消费方定义的窄接口，nautilus 不反向依赖
	// observability）；nil 指针同样满足接口，故为断言须用具体类型断言。
	var _ nautilus.RunObserver = (*observability.Metrics)(nil)
	if ntEnabled, err := nautilus.KernelEnabledFromEnv(); err != nil {
		logger.Fatal().Err(err).Msg("invalid NT_SIDECAR_ENABLED")
	} else if ntEnabled {
		engineRegistry.Register(nautilus.KernelName, nautilus.NewObservedRunner(nautilus.URLFromEnv(), nil, m))
		logger.Info().Str("url", nautilus.URLFromEnv()).Msg("NT sidecar kernel registered (NT_SIDECAR_ENABLED)")
	} else {
		logger.Info().Msg("NT sidecar kernel not registered (production default: go-only; set NT_SIDECAR_ENABLED=1 to opt in)")
	}

	// JobService 改持注册表：kernel 是请求级路由键，执行期才经 Resolve
	// 解析（K10）。logger 显式传入，不再从 engine 内部提取。
	// DBT-06：同一路由表注入 JobService——market 在**创建期**解析落定
	// 进 params["kernel"]，worker 执行期只消费已定路由（与同步端点共用
	// contracts.ResolveRunTarget 单一映射实现）。
	jobService := backtest.NewJobService(store, engineRegistry, logger,
		backtest.WithMarketRouter(marketRouter))
	logger.Info().Msg("Job service initialized")

	// AUD-50: repair backtest rows left in `running` by a previous process.
	//
	// The same repair already runs on the way down (gracefulShutdown), but that
	// only covers an orderly SIGTERM. CleanupStaleRunning's own doc comment has
	// always said it is "also useful as a recovery tool after a hard process
	// crash (kill -9, OOM, etc.) — call it on startup to repair stale rows from
	// the previous run" — and no startup caller ever existed, so the
	// recommended path was never taken. This is that call.
	//
	// Safe here because nothing is running yet: the HTTP server has not started
	// and no job has been accepted, so a `running` row can only be a leftover.
	// Unlike the sync queue this cannot be made structural by hanging it off the
	// worker pool — backtest jobs run on their own goroutines, there is no
	// central dequeue to hook — so the "startup only" precondition is written
	// down instead of enforced. Calling it while jobs are in flight would fail
	// rows that are still working.
	//
	// internal/repoguard pins this call site to
	// cmd/analysis/setup.go:buildDataServices — moving it breaks the guard.
	if n, err := jobService.CleanupStaleRunning(context.Background()); err != nil {
		logger.Error().Err(err).
			Msg("Stale-running cleanup failed; an interrupted backtest may stay 'running'")
	} else if n > 0 {
		logger.Warn().Int("recovered", n).
			Msg("Recovered backtest jobs left 'running' by a previous process")
	}

	wfEngine := backtest.NewWalkForwardEngine(func() (*backtest.Engine, error) {
		if newEngine == nil {
			return engine, nil
		}
		eng, err := newEngine()
		if err != nil {
			return nil, err
		}
		// 新引擎必须挂上 dataAdapter，否则多源回退路径不生效。
		eng.SetDataAdapter(dataAdapter)
		eng.SetStore(store)
		// 每窗口引擎有自己的一份准入闸，观察面必须同样挂上 ——
		// 否则 walk-forward 的 run 在指标上是隐形的（主引擎的读数只算主引擎）。
		eng.SetRunObserver(m)
		eng.SetWASMShardObserver(m)
		return eng, nil
	}, store, logger)
	logger.Info().Msg("Walk-forward engine initialized")

	// batch 经同一注册表取 go 内核 runner（K10）：batch 任务间无引擎级
	// 缓存隔离要求，共享注册实例即正确形态。装配期不变量：go 内核刚
	// 注册成功，Resolve 不可能失败 —— 真失败了说明注册表被运行期
	// 改动（明令禁止），此时不应对外提供服务。
	goRunner, err := engineRegistry.Resolve(contracts.DefaultKernel)
	if err != nil {
		logger.Fatal().Err(err).Msg("engine registry lost the default go kernel — refusing to serve")
	}
	batchEngine := backtest.NewBatchEngine(goRunner, wfEngine, backtest.DefaultBatchConfig(), logger)
	logger.Info().Msg("Batch engine initialized")

	factorAttributor := data.NewFactorAttributor(store)
	logger.Info().Msg("Factor attribution service initialized")

	return &dataServices{
		DataAdapter:      dataAdapter,
		JobService:       jobService,
		WFEngine:         wfEngine,
		BatchEngine:      batchEngine,
		FactorAttributor: factorAttributor,
		EngineRegistry:   engineRegistry,
	}
}

// initStrategyAndPlugins constructs the StrategyDB and PluginLoader,
// seeds built-in strategies, and auto-loads plugins from the configured
// directory if plugins.directory is set.
func initStrategyAndPlugins(v *viper.Viper, store *storage.PostgresStore, logger zerolog.Logger) (*strategy.StrategyDB, *strategy.PluginLoader) {
	strategyDB := strategy.NewStrategyDB(store)
	if err := store.SeedStrategies(context.Background()); err != nil {
		logger.Warn().Err(err).Msg("failed to seed built-in strategies")
	} else {
		logger.Info().Msg("strategy DB seeded")
	}

	strategy.InitPluginLoader(strategy.DefaultRegistry, logger)
	pluginLoader := strategy.GlobalPluginLoader
	logger.Info().Msg("Plugin loader initialized")

	pluginDir := v.GetString("plugins.directory")
	if pluginDir != "" {
		if err := pluginLoader.SetWatchDir(pluginDir); err != nil {
			logger.Warn().Err(err).Str("dir", pluginDir).Msg("Failed to set plugin watch directory")
		} else {
			loaded, errs := pluginLoader.LoadAll()
			if len(errs) > 0 {
				logger.Warn().Int("errors", len(errs)).Msg("Some plugins failed to load")
			}
			logger.Info().Int("count", len(loaded)).Str("dir", pluginDir).Msg("Plugins auto-loaded")
		}
	}
	return strategyDB, pluginLoader
}
