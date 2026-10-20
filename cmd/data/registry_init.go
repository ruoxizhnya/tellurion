// Package main — registry construction for the data service.
//
// buildDataSourceRegistry wires every DataSourceAdapter into a single
// source.Registry, then configures the fallback chains. Adapters that
// require external configuration (API keys, transports) are constructed
// defensively: a missing dependency yields a disabled adapter rather
// than a startup failure.
//
// DATA-08（2026-10-16，主任裁决「停用路线」）到 DATA-10（同日裁决入口 (b)）：
// 这个 Registry 服务的**不再只有观测端点** —— `capital_flow:[eastmoney]` 这一条
// 链已经接进作业层（capitalFlowExecutor → ETLPipeline → Registry.Fetch →
// BulkInsert 写 capital_flow 表）。观测端点本身仍是只读的
// （/api/datasource/registry/status|health|chains，见 registry_handlers.go，
// 规则 B 钉着这里不许出现 .Fetch）。
//
// 其余链仍然没有任何生产路径走 Fetch：日线 / 分钟 / 实时 / 境外 / 板块 / 公告…
// 的数据同步直连 TushareClient，且日线链上 mootdx 以 nil transport 注册
// ⇒ Enabled()=false、eastmoney 只声明 capital_flow。那几族的接线各是一单、
// 各自要先过「源单位实测 → 同步边界换算 → Normalizer → 与 sync_jobs 的关系」。
// 状态由 internal/repoguard/registry_etl_wiring_test.go 机械钉住：生产代码出现
// 白名单之外的 ETLPipeline 构造点、或观测面出现 .Fetch，当场红。
package main

import (
	"os"
	"strings"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"

	"tellurion/pkg/data"
	"tellurion/pkg/data/source"
)

// buildDataSourceRegistry constructs the multi-source registry used by
// the data service. Order of registration matters only insofar as it
// determines the default fallback chain for any data type that more
// than one adapter claims to support; this function then overrides the
// chains explicitly via registry.SetChain to make the priority
// deterministic.
//
// Adapters that need live external configuration (mootdx SDK, Alpha
// Vantage key, etc.) are constructed defensively — if the configuration
// is missing the adapter is registered as disabled. This keeps the
// service running in degraded mode instead of failing at startup.
//
// DATA-10（2026-10-16）：本函数的产物现在被**两处**消费 —— 观测端点，
// 以及作业层的 capital_flow 执行器（`main.go` 把它经 `registerRoutes` 交给
// `NewSyncHandler`）。除 `capital_flow` 外其余链仍无生产调用方；链配置保留
// 是为了让 /api/datasource/registry/* 展示真实意图，不是为了宣称降级
// 能力已生效；两者的区别见 configureFallbackChains 的设计注释与
// docs/SPEC.md「Adapter registry & fallback chain」节。
func buildDataSourceRegistry(tushareClient *data.TushareClient, logger zerolog.Logger) *source.Registry {
	reg := source.NewRegistry()

	// 1. Tushare (existing, refactored as adapter).
	// NewTushareAdapter handles a nil client by setting Enabled()=false,
	// so the registry safely skips it at Fetch time. No outer nil check
	// needed — that would just be code duplication.
	if err := reg.Register(source.NewTushareAdapter(tushareClient)); err != nil {
		logger.Warn().Err(err).Msg("failed to register tushare adapter")
	}

	// 2. Eastmoney push2 — capital flow only.
	// The HTTP client is created unconditionally; the adapter is
	// enabled by default. To disable at runtime, set
	// DATA_DISABLE_EASTMONEY=1 in the environment.
	emClient := source.NewEastmoneyClient()
	if !envDisabled("DATA_DISABLE_EASTMONEY") {
		if err := reg.Register(source.NewEastmoneyAdapter(emClient)); err != nil {
			logger.Warn().Err(err).Msg("failed to register eastmoney adapter")
		}
		if err := reg.Register(source.NewEastmoneySectorsAdapter(emClient)); err != nil {
			logger.Warn().Err(err).Msg("failed to register eastmoney_sectors adapter")
		}
		if err := reg.Register(source.NewEastmoneyTopListAdapter(emClient)); err != nil {
			logger.Warn().Err(err).Msg("failed to register eastmoney_toplist adapter")
		}
	}

	// 3. mootdx (realtime/1min/5min). Requires the mootdx Go SDK
	// (github.com/qmaru/go-mootdx) which is not yet in go.mod. We
	// pass a nil transport so the adapter is registered as disabled
	// (Enabled()==false → Registry.Fetch skips it). When the SDK is
	// added, replace nil with a real transport wrapper.
	mootdxAdapter := source.NewMootdxAdapter(nil)
	if !envDisabled("DATA_DISABLE_MOOTDX") {
		if err := reg.Register(mootdxAdapter); err != nil {
			logger.Warn().Err(err).Msg("failed to register mootdx adapter")
		}
	}

	// 4. Juchao (announcements). No credentials required.
	if !envDisabled("DATA_DISABLE_JUCHAO") {
		if err := reg.Register(source.NewJuchaoAdapter()); err != nil {
			logger.Warn().Err(err).Msg("failed to register juchao adapter")
		}
	}

	// 5. Xueqiu (hot search / news). No credentials required.
	if !envDisabled("DATA_DISABLE_XUEQIU") {
		if err := reg.Register(source.NewXueqiuAdapter()); err != nil {
			logger.Warn().Err(err).Msg("failed to register xueqiu adapter")
		}
	}

	// 6. Alpha Vantage — requires ALPHA_VANTAGE_KEY. Without it the
	// adapter is constructed as disabled; it is still registered so
	// that /api/datasource/registry reports the slot as "configured
	// but not active".
	//
	// CR-54 (ODR-012): the precedence between env and viper was
	// previously silent. Operators setting ALPHA_VANTAGE_KEY in the
	// environment would be confused when their config file's
	// alpha_vantage.api_key silently overrode it. We now log a
	// warning whenever both sources are set, regardless of whether
	// the values agree, so the precedence is auditable. (Viper wins
	// in both cases because it is the more specific config layer;
	// the warning is informational, not a config conflict error.)
	envAlphaKey := strings.TrimSpace(os.Getenv("ALPHA_VANTAGE_KEY"))
	viperAlphaKey := ""
	if viper.IsSet("alpha_vantage.api_key") {
		viperAlphaKey = strings.TrimSpace(viper.GetString("alpha_vantage.api_key"))
	}
	alphaKey := envAlphaKey
	if viperAlphaKey != "" {
		alphaKey = viperAlphaKey
	}
	if envAlphaKey != "" && viperAlphaKey != "" && envAlphaKey != viperAlphaKey {
		logger.Warn().
			Str("env_value", maskSecret(envAlphaKey)).
			Str("viper_value", maskSecret(viperAlphaKey)).
			Msg("ALPHA_VANTAGE_KEY is set in BOTH env and viper config with different values; viper wins (config-file takes precedence over env). Verify this is intended.")
	} else if envAlphaKey != "" && viperAlphaKey != "" {
		logger.Info().Msg("ALPHA_VANTAGE_KEY set in both env and viper config with the same value; viper wins silently (no functional difference)")
	}
	if !envDisabled("DATA_DISABLE_ALPHA_VANTAGE") {
		if err := reg.Register(source.NewAlphaVantageAdapter(alphaKey)); err != nil {
			logger.Warn().Err(err).Msg("failed to register alpha_vantage adapter")
		}
	}

	// 7. Yahoo Finance — public API, enabled by default. Rate limit
	// is conservative (60 req/min via the adapter's RateLimit()).
	if !envDisabled("DATA_DISABLE_YAHOO") {
		if err := reg.Register(source.NewYahooFinanceAdapter()); err != nil {
			logger.Warn().Err(err).Msg("failed to register yahoo_finance adapter")
		}
	}

	// 8. Tushare macro (P2-1) — domestic macro indicators
	// (CPI/PPI/PMI/M2/TSF/LPR/USDCNY). Reuses the same TushareClient
	// (TUSHARE_TOKEN, 0 new keys) but registers under its own name so it
	// carries its own kill switch: DATA_DISABLE_TUSHARE_MACRO stops macro
	// imports without touching the market-data chain. A nil client
	// (token not configured) registers it as disabled (Enabled()==false),
	// same degraded-mode posture as the other adapters.
	if !envDisabled("DATA_DISABLE_TUSHARE_MACRO") {
		if err := reg.Register(source.NewTushareMacroAdapter(tushareClient)); err != nil {
			logger.Warn().Err(err).Msg("failed to register tushare_macro adapter")
		}
	}

	// 9. Tushare daily_basic (D5) — per-stock daily market activity
	// (turnover rate family, market_activity table). Reuses the same
	// TushareClient (TUSHARE_TOKEN, 0 new keys) with its own kill switch:
	// DATA_DISABLE_TUSHARE_DAILY_BASIC stops activity imports without
	// touching the OHLCV chain. Declaration is after_close (blueprint
	// §4.2 high-risk PIT scenario — the D5 sabotage test pins it).
	// A nil client (token not configured) registers it as disabled,
	// same degraded-mode posture as the other adapters.
	if !envDisabled("DATA_DISABLE_TUSHARE_DAILY_BASIC") {
		if err := reg.Register(source.NewTushareDailyBasicAdapter(tushareClient)); err != nil {
			logger.Warn().Err(err).Msg("failed to register tushare_daily_basic adapter")
		}
	}

	// 10. Tushare moneyflow (DBT-09) — per-stock tiered capital flow
	// (capital_flow table). Same TushareClient / TUSHARE_TOKEN, 0 new keys,
	// own kill switch DATA_DISABLE_TUSHARE_MONEYFLOW. It is the **primary**
	// member of the capital_flow chain (see configureFallbackChains): the
	// eastmoney daykline path it now falls back behind has been unreachable
	// from this host since DATA-10, so availability — not source prestige —
	// set the order. A nil client registers it disabled, which leaves the
	// chain to eastmoney and the job honestly failing there, exactly as
	// before this leg existed.
	if !envDisabled("DATA_DISABLE_TUSHARE_MONEYFLOW") {
		if err := reg.Register(source.NewTushareMoneyflowAdapter(tushareClient)); err != nil {
			logger.Warn().Err(err).Msg("failed to register tushare_moneyflow adapter")
		}
	}

	// Configure explicit fallback chains. The default chain built by
	// Registry.Register appends adapters in registration order, but
	// for the most important data types we want a precise priority.
	configureFallbackChains(reg)

	logger.Info().
		Int("adapters", len(reg.ListAdapters())).
		Int("chains", len(reg.ListChains())).
		Strs("names", reg.ListAdapters()).
		Msg("multi-source data registry initialized")

	return reg
}

// configureFallbackChains sets the per-data-type priority used by
// Registry.Fetch. The first element is the primary; subsequent
// elements are tried on retryable failure.
//
// DATA-08（2026-10-16 裁决「停用路线」）→ DATA-10（同日裁决入口 (b)）之后，
// 读这些链要先知道它们各自的状态：**只有 `capital_flow` 有了生产调用方**
// （作业层 capitalFlowExecutor → ETLPipeline.Process → Registry.Fetch）。
// 其余每条链描述的仍是**设计意图**，由 /api/datasource/registry/chains 如实
// 展示，不是「降级已生效」的宣称 —— 那几族的数据同步仍直连 TushareClient
// （handlers_sync.go / sync_handlers.go 里其余执行器）。
// 「哪条链接了线」由 internal/repoguard/registry_etl_wiring_test.go 的白名单
// 逐条钉住（文件 + 形态 + 理由），不是靠这段注释自述。
//
// Design notes:
//
//   - Daily OHLCV: tushare (authoritative, with full history) is
//     primary. mootdx is the secondary for symbols tushare cannot
//     serve. eastmoney is the tertiary.
//     ⚠️ 这条设计意图**目前没有落地**（DATA-05 2026-10-15 核对）：mootdx 在
//     下面以 nil transport 注册 ⇒ Enabled()=false 被 Registry.Fetch 跳过；
//     eastmoney 只声明 capital_flow，Fetch(ohlcv_daily) 直接 ErrUnsupported。
//     所以日线链今天是「tushare 一条腿 + 两条不可达的备胎」，而不是三级降级。
//     把任一条腿接上前，必须先实测它的 volume/amount 单位并在同步边界换算到
//     规范层（股 / CNY）—— 见 cmd/data/ohlc_units_guard_test.go 与 docs/TASKS.md DATA-05。
//
//   - Realtime quote: mootdx is primary (5-level depth, low latency).
//     Eastmoney push2 is the fallback when the SDK is unavailable.
//     ⚠️ 这条备胎关系今天同样不成立（DATA-05 核对）：eastmoney 只声明
//     capital_flow，Fetch(realtime_quote) 直接 ErrUnsupported —— 链上留着
//     它是 chain drift 的形状，Registry.Fetch 会 loud-skip（CR-39）。
//
//   - Capital flow: two members, ordered by **availability**.
//     `tushare_moneyflow` is primary, `eastmoney` the fallback.
//     DATA-10（2026-10-16）first wired this chain to the job layer with
//     eastmoney as its only member; that path (`/api/qt/stock/fflow/daykline/get`)
//     has been unreachable from this host ever since (push2 502 / push2his
//     connection reset — AGENTS.md §14 [DATA-10]), so the table stayed at 0
//     rows. DBT-09（2026-10-19，主任裁决）adds the Tushare `moneyflow` leg and
//     puts it **first**, because "先注册者优先" here means primary/backup
//     priority: an unreachable member in front makes the job fail every time.
//     ⚠️ 代价要如实读：链首选一旦换了源，`capital_flow` 表里的数值口径就是
//     **Tushare 的分档成交额**（super/large/medium/small_net 四档净额，
//     万元 ×10000 在同步边界折成元），不再是东财的净流入时序。main_net /
//     main_buy_amount / main_sell_amount / 两个 ratio / retail_* / close_price /
//     change_pct 本源不给 ⇒ 落 NULL（见 pkg/data/tushare_moneyflow.go 的映射表）。
//     两族口径因此**混在同一张表里，靠 source 列区分**；读方按东财含义解释
//     这些列之前必须先读 source。
//     ✅ 这条链仍是唯一有生产调用方的链 —— 作业层 `capital_flow` job_type 经
//     capitalFlowExecutor 走 Registry.Fetch 落 `capital_flow` 表。
//     真源腿在 `cmd/data/dbt09_moneyflow_live_test.go`（tag `dbt09live`）。
//
//   - Sectors / top_list: dedicated eastmoney adapters.
//
//   - Global OHLCV: yahoo is primary (rich history, no key needed);
//     alpha_vantage is the secondary.
func configureFallbackChains(reg *source.Registry) {
	reg.SetChain(source.DataTypeOHLCDaily, []string{
		"tushare",
		"mootdx",
		"eastmoney",
	})
	reg.SetChain(source.DataTypeOHLCMinute, []string{
		"mootdx",
		"eastmoney",
	})
	reg.SetChain(source.DataTypeRealtime, []string{
		"mootdx",
		"eastmoney",
	})
	reg.SetChain(source.DataTypeCapitalFlow, []string{
		"tushare_moneyflow",
		"eastmoney",
	})
	reg.SetChain(source.DataTypeFundamental, []string{
		"tushare",
	})
	reg.SetChain(source.DataTypeSectors, []string{
		"eastmoney_sectors",
	})
	reg.SetChain(source.DataTypeStockSector, []string{
		"eastmoney_sectors",
	})
	reg.SetChain(source.DataTypeTopList, []string{
		"eastmoney_toplist",
	})
	reg.SetChain(source.DataTypeLimitUpPool, []string{
		"eastmoney_toplist",
	})
	reg.SetChain(source.DataTypeAnnounce, []string{
		"juchao",
	})
	reg.SetChain(source.DataTypeNews, []string{
		"xueqiu",
	})
	reg.SetChain(source.DataTypeHotSearch, []string{
		"xueqiu",
	})
	reg.SetChain(source.DataTypeGlobalOHLCV, []string{
		"yahoo_finance",
		"alpha_vantage",
	})
	// Macro (P2-1): tushare_macro is the only first-class source today
	// (FRED / World Bank / Alpha Vantage are explicitly out of scope for
	// the first version — they would each add a new key).
	reg.SetChain(source.DataTypeMacro, []string{
		"tushare_macro",
	})
	// Market activity (D5): tushare_daily_basic is the only first-class
	// source (the D5 sample source). Chain holds one entry by design —
	// multi-source fallback for activity data is a later one-source-one-task.
	reg.SetChain(source.DataTypeDailyBasic, []string{
		"tushare_daily_basic",
	})
}

// envDisabled returns true if the named environment variable is set
// to a truthy value ("1", "true", "yes", "on", case-insensitive).
// Used as a kill switch for individual adapters in production.
func envDisabled(name string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// maskSecret returns a redacted form of a secret for log output.
// Keeps the first 4 and last 2 characters visible (if available) so
// operators can still tell "they are the same value" vs "completely
// different values" while not leaking the key material. Strings
// shorter than 8 characters are fully redacted.
//
// Used by CR-54 (ODR-012) to log ALPHA_VANTAGE_KEY precedence
// warnings without putting the actual key in stdout/journald.
func maskSecret(s string) string {
	if len(s) <= 8 {
		return "***"
	}
	return s[:4] + "***" + s[len(s)-2:]
}
