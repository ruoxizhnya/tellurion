package bootstrap

import (
	"context"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"tellurion/pkg/backtest"
	"tellurion/pkg/domain"
	"tellurion/pkg/marketdata"
	"tellurion/pkg/risk"
	"tellurion/pkg/storage"
	"tellurion/pkg/trading"
)

// BuildBacktestEngine 构造回测引擎与 HTTP 行情 provider。HTTP provider
// 单独返回，供 DataAdapter 的多源回退路径复用。
func BuildBacktestEngine(v *viper.Viper, logger zerolog.Logger) (*backtest.Engine, marketdata.Provider) {
	dataServiceURL := v.GetString("data_service.url")
	if dataServiceURL == "" {
		dataServiceURL = "http://localhost:8081"
	}
	// data 服务已上鉴权闸（裁-15）：配了 secret 的部署必须带服务间凭证，
	// 否则取数全线 401。token 为空时不发头（回环 + 无 secret 的开发态）。
	httpProvider := marketdata.NewHTTPProviderWithToken(dataServiceURL, v.GetString("data_service.token"), logger)
	engine, err := backtest.NewEngine(v, httpProvider, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to initialize backtest engine")
	}
	return engine, httpProvider
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

// BuildExecutionTrader 构造进程内 MockTrader（P1-15, ODR-021）。
// analysis 与 ai 共享同一构造 —— 两边跑出来的回测必须逐项一致。
func BuildExecutionTrader(v *viper.Viper, logger zerolog.Logger) trading.LiveTrader {
	execConfig := domain.ExecutionConfig{
		OrderType:      domain.OrderTypeMarket,
		SlippageModel:  "fixed",
		CommissionRate: v.GetFloat64("backtest.commission_rate"),
		MinCommission:  v.GetFloat64("trading.min_commission"),
		InitialCapital: v.GetFloat64("backtest.initial_capital"),
	}
	trader := trading.NewMockTrader(trading.MockTraderConfig{
		InitialCash:    execConfig.InitialCapital,
		CommissionRate: execConfig.CommissionRate,
		StampTaxRate:   v.GetFloat64("trading.stamp_tax_rate"),
		SlippageRate:   v.GetFloat64("backtest.slippage_rate"),
	}, logger)
	logger.Info().Msg("execution trader attached to backtest engine in-process (P1-15)")
	return trader
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
