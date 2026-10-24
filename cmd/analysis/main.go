package main

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/ruoxizhnya/tellurion-contracts/engine"
	"github.com/spf13/viper"
	"tellurion/cmd/analysis/wiring"
	"tellurion/pkg/backtest"
	"tellurion/pkg/bootstrap"
	"tellurion/pkg/compliance"
	"tellurion/pkg/live/reconciliation"
	"tellurion/pkg/observability"
	"tellurion/pkg/strategy"
	_ "tellurion/pkg/strategy/plugins"
)

// httpClient wraps the outbound data-service / ai-service calls.
// （原先还列了 ai-service，该服务于 2026-09-18 删除，见 TASKS P2-5。） Sprint 6 P0-3: HTTPTransport propagates the
// per-request X-Request-ID from the inbound request context to
// downstream calls AND records an observation in
// http_client_requests_total{service="data",status=...}.
//
// AI 拆仓阶段 1：client 本体上收 internal/bootstrap（cmd/ai 共享同一份）。
var httpClient = bootstrap.HTTPClient

// main is the composition root for the analysis service. It wires
// together all services via the builder functions in setup.go and
// orchestrates startup + graceful shutdown. S7-P2-3 (ODR-043): the
// 381-line main() was split into focused builders so this function is
// now a thin orchestrator (~35 lines).
func main() {
	logger := initLogger()
	m := initMetrics(logger)
	v := loadConfig(logger)

	// store 在 Boot 前构造：eventstore 模块复用它的 pool（一个进程一个池）。
	store := initStore(v, logger)

	// D3 因子定义库投影：启动时把内置种子定义幂等 upsert 进
	// factor_definitions（库与内存同源）。该表是治理/审计的持久投影，
	// 读路径在内存注册表——投影失败不阻断启动（kill-switch 同款降级），
	// 但必须吵：投影缺失会让 D4 治理/审计拿到空库。
	syncCtx, syncCancel := context.WithTimeout(context.Background(), 15*time.Second)
	if err := store.SyncFactorDefinitions(syncCtx, domain.BuiltinFactorDefinitions); err != nil {
		logger.Error().Err(err).Msg("factor definitions sync failed; factor_definitions projection may be missing/stale")
	}
	syncCancel()

	// ─── 内核接管装配（K1 切片 3）─────────────────────────────────────
	// engine / riskManager / executionTrader / dataAdapter 的构造与注入
	// 顺序由 BootOrder 决定（eventstore→clock→data-engine→portfolio→
	// risk-engine→exec-engine→strategy-runtime→indicators→exec-algo→msgbus，
	// 见 pkg/kernel/interfaces.go 的冻结契约）；接管前的硬编码顺序
	// （buildBacktestEngine → buildRiskManager → buildExecutionTrader →
	// initStore → Set* 散装注入）就此退役。失败 fail-fast：内核是承重
	// 组件，装配不起来就不该对外提供服务。
	ak, err := assembleKernel(v, store, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("kernel assembly failed")
	}
	engine := ak.Wiring.Engine
	httpProvider := ak.Wiring.Provider
	riskManager := ak.Wiring.RiskManager
	executionTrader := ak.Wiring.ExecutionTrader

	alertManager, alertLoop := buildAlertSystem(v, executionTrader, riskManager, logger)

	authSvc := initAuth(v, store, logger)

	// walk-forward 的每个窗口拿一个**独立装配**的 Engine 实例。
	//
	// 这条**不是**缓存隔离的要求（QT-12 已把引擎级可变运行态搬进 per-run 的
	// runContext，共享单例不再串扰 —— 原来写在这里的理由已经修掉了，别再
	// 把它读成约束）。保留工厂是因为窗口要一份干净的 state store 与桥接组件。
	//
	// 装配面实测（2026-10-23，QT-13 自审时逐 setter 对过）：这份工厂 +
	// setup.go 那层的 SetDataAdapter/SetStore/SetRunObserver，与主引擎在
	// `cmd/analysis/wiring/modules_analysis.go` 里拿到的**是同一组**（NewEngine +
	// Store + DataAdapter + RiskManager + LiveTrader + RunObserver）。
	// 曾经记在这里的「窗口引擎没有 provenance writer / adjStore」是错的：
	// 那两个端口都从 `e.store` 自动派生（`provenanceSink()` /
	// `adjustmentStore()`），而工厂设了 store —— 窗口的 run 今天既写留痕也
	// 冻结复权基准。真正缺的日内取数通道（`SetRouter` / D6 预热）已由
	// QT-64 补齐：主引擎与窗口引擎都经 `wiring.AttachMarketDataRouter`
	// 这同一个装配函数拿到进程内唯一的 Router（`ak.Wiring.Router`）。
	newEngine := func() (*backtest.Engine, error) {
		eng, err := backtest.NewEngine(v, httpProvider, logger)
		if err != nil {
			return nil, err
		}
		eng.SetRiskManager(riskManager)
		eng.SetLiveTrader(executionTrader)
		eng.SetStore(store)
		// QT-64：与主引擎同一条取数通道（D6）。nil Router 由装配函数
		// 显式 Warn，不允许窗口引擎静默退化成「无日内数据」。
		wiring.AttachMarketDataRouter(eng, ak.Wiring.Router, logger)
		return eng, nil
	}

	// DataAdapter 由 data-engine 模块构造（Boot 第 3 位）—— 传进来复用，
	// 不在此重建（否则同一进程两条 adapter 实例 = 双真相）。m 传入用于
	// NT 侧车 round-trip 观测接线（K16 ②）。
	// D7 市场路由表：配置驱动（`kernel_routing.markets`，如 CN: go / US: nautilus），
	// 新市场接入 = 加一条配置，不改路由代码（蓝图 §2.1 热转换语义第 2 条）。
	// 配置节缺失 ⇒ 空表（合法）：不带 market 的存量请求行为不变，带 market 的
	// 请求在请求处理时被拒（400）。畸形配置（空内核名 / 归一后冲突）在装配期
	// 直接 Fatal —— 带病配置不该对外提供服务。
	// DBT-06：这份路由表同时喂同步端点（registerBacktestRoutes）与异步
	// job 创建（JobService.WithMarketRouter）——两条路径共用同一映射实现。
	marketRouter, err := contracts.NewMarketRouter(
		v.GetStringMapString("kernel_routing.markets"))
	if err != nil {
		logger.Fatal().Err(err).Msg("invalid kernel_routing.markets config")
	}
	logger.Info().Strs("markets", marketRouter.Markets()).
		Msg("market routing table assembled (D7) — unregistered markets are refused, never fallen back")

	ds := buildDataServices(store, engine, ak.Wiring.Adapter, logger, newEngine, m, marketRouter)
	strategyDB, pluginLoader := initStrategyAndPlugins(v, store, logger)

	// AI 拆仓阶段 1（ADR-027 §5 第 8 步前置切片）：Copilot / pipeline /
	// explore / tools 四族 handler 随 ai-service 迁出，本服务对这四族
	// 路由改 HTTP 反代（见 handlers_ai_proxy.go），前端零改动。装配侧
	// 相应移除 buildCopilot / buildToolsRegistry / 各 adapter 与
	// gene_pool pools —— 它们现在住在 cmd/ai。

	deps := &ServerDeps{
		Engine:           engine,
		EngineRegistry:   ds.EngineRegistry,
		MarketRouter:     marketRouter,
		JobService:       ds.JobService,
		WFEngine:         ds.WFEngine,
		BatchEngine:      ds.BatchEngine,
		StrategyDB:       strategyDB,
		FactorAttributor: ds.FactorAttributor,
		PluginLoader:     pluginLoader,
		AuthSvc:          authSvc,
		RiskManager:      riskManager,
		ExecutionTrader:  executionTrader,
		EmergencyToken:   v.GetString("trading.emergency_token"),
		Metrics:          m,
		Logger:           logger,
		Viper:            v,
		Store:            store,
	}

	// AUD-29: gin's run mode comes from server.gin_mode and is applied
	// exactly once, here, before the router exists.
	applyGinMode(v, logger)

	// 内核已在上方 Boot（接管装配）——影子启动随切片 3 删除。

	router := buildRouter(authSvc, v, logger)
	registerRoutes(router, deps)
	// REV-23(11)：alertLoop 此前是唯一没有 cancel 路径的后台组件——
	// Start 收 Background ctx，优雅关停停不掉它（loop 只在进程退出时
	// 随之消亡）。对齐 reconWorker 的局部不变量：每个后台组件都有
	// cancel 路径。stopAlert 在 shutdown 段与 stopRecon 相邻调用。
	alertCtx, stopAlert := context.WithCancel(context.Background())
	go alertLoop.Start(alertCtx)

	// K16 ⑤：资金对账 worker 的生产接线（此前 ReconciliationWorker /
	// AlertDispatcher 零生产构造点）。enabled 未配置 → worker 为 nil，
	// /api/reconciliation 路由照常注册、请求 503（与 alerts 路由同一姿态）；
	// 启用时差异逐条经 alert.ReconciliationDispatcher 进告警通道集
	// （log + webhook，与风控告警同通道）。
	reconWorker := buildReconciliation(v, alertManager, executionTrader, logger)
	registerAuxRoutes(router, alertLoop, reconWorker, logger)
	var stopRecon context.CancelFunc
	if reconWorker != nil {
		var reconCtx context.Context
		reconCtx, stopRecon = context.WithCancel(context.Background())
		go reconWorker.Start(reconCtx)
	}

	srv := startHTTPServer(router, v, logger)

	sig := waitForShutdown()
	logger.Info().Str("signal", sig.String()).Msg("Shutdown signal received; beginning graceful drain")

	// 对账 worker 先于内核关停停止：worker tick 可能正持一笔对账，别让它
	// 撞上后面的 store.Close()。
	if stopRecon != nil {
		stopRecon()
	}
	// alertLoop 同理（REV-23(11)）：评估 tick 可能正写历史/派发 webhook，
	// 先停再关连接池。Start 对 cancel 的响应是 select ctx.Done，最多
	// 一个在途评估完成后退出。
	stopAlert()

	// ─── 内核关停（K1 切片 3；时序裁决继承切片 2）─────────────────────
	// 必须早于 gracefulShutdown：后者 phase 4 会 store.Close() 关掉连接池，
	// 而 kernel.shutdown 要落 audit.message_log —— 落库时 pool 必须还活着。
	// 内核内部顺序：先发 kernel.shutdown（msgbus/eventstore 仍运行），再按
	// BootOrder 逆序停（msgbus 最先停、eventstore 最后停）。见 kernel.go 的
	// 「时序裁决」注释与其单测 TestShutdownPublishesBeforeStoppingMsgBus。
	shutdownKernel(ak.Kernel, logger)

	gracefulShutdown(srv, ds.JobService, alertManager, store, logger)
}

// registerAuxRoutes 是 main() 在 registerRoutes 之外的第二批路由接线：
// alerts（K16 告警历史/统计/强制检查）与 reconciliation（对账 4 端点）。
//
// 为什么单独抽成一个函数：契约护栏（routes_contract_test.go，REV-13）要装配
// **与生产完全一致**的路由表才能和 docs/openapi.yaml 双向对拍。此前这两族
// 直接写在 main() 里，测试路径摸不到 —— 护栏看到的会是一份「少了两个族」的
// 假快照，漂移照样漏。抽出来后 main 与测试调用同一个入口，新增第三族时
// 只要接进这里，护栏自动覆盖。
func registerAuxRoutes(router *gin.Engine, alertLoop *PeriodicAlertLoop, reconWorker *reconciliation.ReconciliationWorker, logger zerolog.Logger) {
	registerAlertRoutes(router, alertLoop)
	NewReconciliationHandler(reconWorker, logger).RegisterRoutes(router)
}

func registerRoutes(router *gin.Engine, deps *ServerDeps) {

	// Legacy HTML UI (cmd/analysis/static/) retired in AUD-33 (2026-09-22,
	// the ③ stage of the AUD-18 staged-retirement ruling). web/ is the only
	// frontend now: nginx serves it on host port 8080 (AUD-32) and it reaches
	// this service only through /api/*.
	//
	// Do NOT re-add a catch-all "/" or a /static mount here. This service is
	// an API; if it also answers "/" with HTML, the two frontends silently
	// diverge again -- and deps_test.go's mustNotHave guard will fire.

	healthHandler := func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":    "healthy",
			"service":   "analysis-service",
			"timestamp": time.Now().Format(time.RFC3339),
		})
	}
	router.GET("/health", healthHandler)
	// /api/health 是同一个探针的 /api 版本。它必须存在：Dockerfile 的
	// HEALTHCHECK 打的就是这个路径，而在此之前后端只注册了 /health ——
	// 探针一直 404，容器从启动起就被判 unhealthy。
	// 另外前端在 dev 下只代理 /api（vite proxy），不挂这个别名前端也探不到。
	router.GET("/api/health", healthHandler)

	// Sprint 6 P0-3: /metrics endpoint exposing the four ADR-017 §1
	// core metrics + Go runtime collectors. Unauthenticated by
	// design — the metrics scraper runs on the same network and
	// ADR-017 §2 (P1-2) will add an authn boundary separately.
	router.GET("/metrics", observability.Handler(deps.Metrics))

	router.GET("/api/v1", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"service": "analysis-service",
			"version": "1.0.0",
			"endpoints": []string{
				"GET  /health",
				"POST /backtest",
				"GET  /backtest/:id/report",
				"GET  /backtest/:id/trades",
				"GET  /backtest/:id/equity",
			},
		})
	})

	// P2-17: OpenAPI 3.0 spec + Swagger UI. The spec is embedded
	// from docs/openapi.yaml (via docs/embed.go) and served at
	// /api/openapi.yaml; the Swagger UI is served at /api/docs.
	registerOpenAPIRoutes(router)

	registerProxyRoutes(router, httpClient, deps.Viper, deps.Logger)
	registerBacktestRoutes(router, deps.Engine, deps.EngineRegistry, deps.JobService, deps.MarketRouter, deps.Logger)
	registerWalkForwardRoutes(router, deps.WFEngine, deps.Logger)
	registerBatchRoutes(router, deps.BatchEngine, deps.Logger)
	registerStrategyRoutes(router, deps.StrategyDB)
	registerDatasourceRoutes(router, deps.Engine)
	registerFactorRoutes(router, deps.FactorAttributor, deps.Logger)
	// QT-50 ③ 第 6 类粗粒度动作：因子登记写入门。未接库（零值 store）时
	// 路由照常注册、请求时 503（newMinimalDeps 约束）。注意先判 pool 再装
	// 接口——nil *PostgresStore 装进接口会变成「非 nil 接口 + nil 指针」，
	// 到 handler 里才 panic。
	var factorRegStore factorRegistrationStore
	if deps.Store != nil && deps.Store.DB() != nil {
		factorRegStore = deps.Store
	}
	NewFactorRegistrationHandler(factorRegStore, deps.AuthSvc, deps.Logger).RegisterRoutes(router)
	// QT-67（D24 第 7 类动作）：交易日历 / 上市窗口只读端点。与上面同款约束
	// ——未接库时路由照常注册、请求时 503，不返回空日历冒充「没有数据」。
	var refDataStore referenceDataStore
	if deps.Store != nil && deps.Store.DB() != nil {
		refDataStore = deps.Store
	}
	NewReferenceDataHandler(refDataStore, deps.Logger).RegisterRoutes(router)
	// QT-69：表达式静态校验 + 算子/字段目录（agent 的 pkg/expression 依赖
	// 交接件）。可用性探测只在池真装配时注入；否则目录字段报 unknown ——
	// 不把「没探测」冒充成「有数据」。与上面同款：nil store 路由照常注册。
	var exprAvailStore expressionAvailabilityStore
	if deps.Store != nil && deps.Store.DB() != nil {
		exprAvailStore = deps.Store
	}
	NewExpressionHandler(exprAvailStore, deps.Logger).RegisterRoutes(router)
	registerPluginRoutes(router, deps.PluginLoader, deps.AuthSvc)
	// AI 拆仓阶段 1（ADR-027 §5 第 8 步前置切片）：copilot / pipeline /
	// explore / tools 四族路由改反代到 ai-service。原进程内注册（registerCopilotRoutes
	// / registerPipelineRoutes / registerExploreRoutes / NewToolsHandler）
	// 已随 handler 文件迁往 cmd/ai。前端与 openapi 契约不变 —— 路径、
	// 宿主、端口全部维持原样。
	registerAIProxyRoutes(router, deps.Viper, deps.Logger)
	registerAuthRoutes(router, deps.AuthSvc, deps.Logger)

	// P1-15 (Sprint 6, ODR-021): risk + execution endpoints
	// absorbed from cmd/risk/main.go + cmd/execution/main.go.
	// Both backends are in-process (risk.RiskManager and
	// live.MockTrader) so the HTTP layer is a thin shim — no
	// service-to-service hop.
	// QT-70：detect_regime 的粗粒度形状（symbol + 日期区间）需要服务端取数；
	// 池未装配时保持 nil ⇒ 请求该形状 503，内联 data 形状不受影响。
	var riskOHLCV riskOHLCVStore
	if deps.Store != nil && deps.Store.DB() != nil {
		riskOHLCV = deps.Store
	}
	NewRiskHandler(deps.RiskManager, deps.Logger).WithOHLCVStore(riskOHLCV).RegisterRoutes(router)
	// P2-3 (ODR-026): pass the emergency-flatten bearer token
	// through to the execution handler. Empty token disables the
	// kill-switch endpoint (returns 503 instead of 404).
	//
	// AUD-02 (ODR-065 H5): WithExecutionAuth gates the order-mutating
	// endpoints (POST /orders, POST /orders/:id/cancel) behind
	// RequireRole(trader, admin) — on both /api/execution/* and the
	// legacy root paths.
	NewExecutionHandler(deps.ExecutionTrader, deps.Logger, deps.EmergencyToken,
		WithExecutionAuth(deps.AuthSvc)).RegisterRoutes(router)

	// P2-4 (ODR-028): investor suitability (compliance) endpoints.
	// The handler is read-only — it does not block order submission
	// in the execution path; the frontend does the precheck before
	// calling POST /api/execution/orders. The default profile is
	// loaded from `trading.default_user_profile.*` in the analysis
	// config; in production this is replaced by a JWT-driven DB
	// lookup (P1-2 + a future `users` table column set).
	defaultProfile := loadDefaultSuitabilityProfile(deps.Viper)
	// P2-6 (ODR-028): large-transaction reporter config from
	// `compliance.reporter.*` viper keys. Defaults are regulatory
	// (2M / 5M) but the operator can override per environment.
	reporterCfg := compliance.LargeTradeConfig{
		SingleThresholdCNY:     deps.Viper.GetFloat64("compliance.reporter.single_threshold_cny"),
		CumulativeThresholdCNY: deps.Viper.GetFloat64("compliance.reporter.cumulative_threshold_cny"),
		OutputPath:             deps.Viper.GetString("compliance.reporter.output_path"),
		AccountWhitelist:       map[string]bool{},
	}
	NewComplianceHandler(deps.Logger, defaultProfile, reporterCfg).RegisterRoutes(router)

	// L0-3 (ADR-022 §5): read-only Evidence API. Resolves a citation's
	// content_hash to its unique archived source response in `ingest.raw`
	// (404 = not ingested). This is the platform's single evidence
	// coordinate — work faces read through it instead of storing copies.
	NewEvidenceHandler(deps.Store, deps.Logger).RegisterRoutes(router)

	// P2-2 (TASKS.md §五 leftover ruling): industry-chain static data layer
	// read API (SW industry tree / supply-chain edges / industry vitality /
	// ETF constituents). Same read-only posture as Evidence — no RBAC.
	// deps.Store 零值（nil pool，未接库）时路由照常注册、请求时 503，
	// registerRoutes 不因零值 panic（deps_test.newMinimalDeps 约束）。
	NewIndustryHandler(deps.Store, deps.Logger).RegisterRoutes(router)

	// P2-1 (TASKS.md §五 leftover ruling): domestic macro indicator series
	// read API (CPI/PPI/PMI/M2/社融/LPR/汇率, quant.macro_series). Same
	// read-only posture — no RBAC, deps.Store 零值时 503。⚠️ 复用 deps.Store
	// 已有字段，不新增 ServerDeps 字段（deps_test.go 钉住字段总数）。
	NewMacroHandler(deps.Store, deps.Logger).RegisterRoutes(router)

	// K8（TASKS K8 ③.4）：外部模型信号 HTTP 写入门 POST /api/signals。
	// 信号表归属 strategy-runtime（pkg/strategy.PGSignalStore），store 缺失
	// （未接库/零值）时传 nil —— 路由照常注册、请求时 503，registerRoutes
	// 不因 deps.Store 零值 panic（deps_test.newMinimalDeps 约束）。
	var signalStore strategy.SignalStore
	if deps.Store != nil && deps.Store.DB() != nil {
		ss, err := strategy.NewPGSignalStore(deps.Store.DB())
		if err != nil {
			deps.Logger.Error().Err(err).Msg("signal store init failed; POST /api/signals will return 503")
		} else {
			signalStore = ss
		}
	}
	NewSignalsHandler(signalStore, deps.Logger, WithSignalsAuth(deps.AuthSvc)).RegisterRoutes(router)
}

// loadDefaultSuitabilityProfile reads the suitability profile from
// the analysis-service viper config under `trading.default_user_profile.*`.
// Missing keys resolve to zero values — a zero-valued profile
// represents the most conservative "default-reject" stance (no asset,
// no experience, no risk level → nothing passes for restricted boards).
func loadDefaultSuitabilityProfile(v *viper.Viper) compliance.SuitabilityProfile {
	p := compliance.SuitabilityProfile{
		UserID:           v.GetString("trading.default_user_profile.user_id"),
		AssetDailyAvgCNY: v.GetFloat64("trading.default_user_profile.asset_daily_avg_cny"),
		RiskLevel:        compliance.RiskLevel(v.GetInt("trading.default_user_profile.risk_level")),
		BoardsEnabled:    v.GetStringSlice("trading.default_user_profile.boards_enabled"),
	}
	if firstTrade := v.GetString("trading.default_user_profile.first_trade_at"); firstTrade != "" {
		if t, err := time.Parse(time.RFC3339, firstTrade); err == nil {
			p.FirstTradeAt = t
		}
	}
	if rte := v.GetString("trading.default_user_profile.risk_test_expired_at"); rte != "" {
		if t, err := time.Parse(time.RFC3339, rte); err == nil {
			p.RiskTestExpiredAt = t
		}
	}
	return p
}
