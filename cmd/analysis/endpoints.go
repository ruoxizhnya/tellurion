package main

// endpoints.go —— 进程启动期的出站地址体检（配置键 ↔ env 覆盖名 ↔ 默认值）。
//
// 口号：**配置错误就该在启动时炸，而不是在第一条业务请求上炸。**
// yaml 是宿主机视角（localhost），容器由 compose/k8s env 覆盖；这里把
// 「宿主机上留了容器 DNS」「该给地址却没给」统一变成一条点名键与 env 的
// 致命错误。远端服务暂时没起**不**在这里判死（那属于就绪探针 / 运行时降级）。

import (
	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"tellurion/pkg/bootstrap"
)

// validateOutboundEndpoints 按「是否启用」逐项校验出站地址。
//
// 只校验真正会被读的项：strategy_asset / risk_service 的地址在开关关闭时
// 没有读取点，强制要求它们等于给开发环境制造噪声。
func validateOutboundEndpoints(v *viper.Viper, logger zerolog.Logger) {
	checks := []struct {
		key    string
		envKey string
		value  string
	}{
		// 取数通道：宿主机默认 localhost:8081，容器由 DATA_SERVICE_URL 覆盖。
		{key: "data_service.url", envKey: "DATA_SERVICE_URL", value: v.GetString("data_service.url")},
		// AI 四族反代目标：空值时代码缺省 localhost:8086，这里按解析后的
		// 有效值校验（避免「yaml 没写但代码有默认」被判成缺配）。
		{key: "ai_service.url", envKey: "AI_SERVICE_URL", value: aiServiceURL(v)},
	}
	if v.GetBool("strategy_asset.resolve_enabled") {
		checks = append(checks, struct {
			key    string
			envKey string
			value  string
		}{"strategy_asset.resolve_url", "STRATEGY_ASSET_RESOLVE_URL", v.GetString("strategy_asset.resolve_url")})
	}
	if v.GetBool("risk_service.resolve_enabled") {
		checks = append(checks, struct {
			key    string
			envKey string
			value  string
		}{"risk_service.resolve_url", "RISK_SERVICE_RESOLVE_URL", v.GetString("risk_service.resolve_url")})
	}
	if v.GetBool("risk_service.events_enabled") {
		checks = append(checks, struct {
			key    string
			envKey string
			value  string
		}{"risk_service.events_url", "RISK_SERVICE_EVENTS_URL", v.GetString("risk_service.events_url")})
	}

	for _, c := range checks {
		if err := bootstrap.ValidateServiceEndpoint(c.key, c.envKey, c.value); err != nil {
			logger.Fatal().Err(err).Msg("outbound endpoint configuration invalid")
		}
		logger.Debug().Str("key", c.key).Str("url", c.value).Msg("outbound endpoint ok")
	}
}
