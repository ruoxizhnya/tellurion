package bootstrap

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"tellurion/pkg/backtest"
	"tellurion/pkg/brokerhttp"
	"tellurion/pkg/marketdata"
	"tellurion/pkg/risk"
	"tellurion/pkg/storage"
	"tellurion/pkg/trading"
)

// BuildBacktestEngine 构造回测引擎与 HTTP 行情 provider。HTTP provider
// 单独返回，供 DataAdapter 的多源回退路径复用。
//
// fundamentals 非 nil 时，基本面读口切回 core 自己的 public.stock_fundamentals
// （归属裁决 2026-11-12）；其余行情能力仍走数据服务。
//
// CORE-DATA-URL-SINGLE-DEFAULT：data-service 地址经 DataServiceURL 解析 ——
// 与网关反代（cmd/analysis/handlers_proxy.go）共用同一入口，缺配置即返回
// 错误（fail-closed），不再有本包私有的 localhost 回落。
func BuildBacktestEngine(v *viper.Viper, fundamentals marketdata.FundamentalQuerier, logger zerolog.Logger) (*backtest.Engine, marketdata.Provider, error) {
	dataServiceURL, err := DataServiceURL(v)
	if err != nil {
		return nil, nil, err
	}
	// data 服务已上鉴权闸（裁-15）：配了 secret 的部署必须带服务间凭证，
	// 否则取数全线 401。token 为空时不发头（回环 + 无 secret 的开发态）。
	httpProvider := marketdata.NewHTTPProviderWithToken(dataServiceURL, v.GetString("data_service.token"), logger)
	provider := marketdata.NewLocalFundamentalProvider(httpProvider, fundamentals)
	engine, err := backtest.NewEngine(v, provider, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize backtest engine: %w", err)
	}
	return engine, provider, nil
}

// BuildRiskManager 从显式配置构造进程内风控管理器（P1-15，ODR-021 合并形态），
// 同时作为 tools 门面 get_market_regime 的 RegimeDetectorClient。
//
// 配置来源**只有一处**（SVC-CORE-RISK-RETIRE，2026-11-09）：调用方传
// `risk.Policy.Config` —— analysis / worker 都经 `wiring.LoadRiskPolicy` 从
// tellurion-risk 的规则快照取限额；未接入（`resolve_enabled=false`）时那是
// `risk.BuiltinPolicy()` 的代码常量。本函数不再读 `risk_manager.*`
// （`config/*.yaml` 里的同名块已删，见 docs/TASKS.md 本单 ⑤④）。
func BuildRiskManager(cfg risk.RiskManagerConfig, logger zerolog.Logger) *risk.RiskManager {
	rm, err := risk.NewRiskManager(cfg, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to initialize in-process risk manager (P1-15)")
	}
	logger.Info().Msg("risk manager attached to backtest engine in-process (P1-15)")
	return rm
}

// BuildExecutionTrader 构造 core 唯一的真实交易通道：broker HTTP 网关
// （SVC-CORE-BROKER-RETIRE 步骤 B）。
//
// 进程内撮合（MockTrader）已删除：`BROKER_URL` 缺失即返回装配错误，
// 由调用方 fail-loud；不再有静默回退到内存撮合的分支。
func BuildExecutionTrader(logger zerolog.Logger) (trading.LiveTrader, error) {
	return brokerhttp.NewTraderFromEnv(logger)
}

// InitStore 构造 Postgres store（DSN 组装与连接失败均 fail-loud）。
func InitStore(v *viper.Viper, logger zerolog.Logger) *storage.PostgresStore {
	dbURL, err := storage.BuildDSN(storage.DatabaseConfig{
		URL:      v.GetString("database.url"),
		Host:     v.GetString("database.host"),
		Port:     v.GetInt("database.port"),
		User:     v.GetString("database.user"),
		Password: v.GetString("database.password"),
		Name:     v.GetString("database.database"),
		SSLMode:  v.GetString("database.sslmode"),
	})
	if err != nil {
		logger.Fatal().Err(err).Msg("invalid database configuration")
	}
	store, err := storage.NewPostgresStore(context.Background(), dbURL)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to initialize postgres store")
	}
	return store
}
