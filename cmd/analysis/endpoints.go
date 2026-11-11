package main

// endpoints.go —— 进程启动期的出站地址体检（配置键 ↔ env 覆盖名 ↔ 默认值）。
//
// 口号：**配置错误就该在启动时炸，而不是在第一条业务请求上炸。**
// yaml 是宿主机视角（localhost），容器由 compose/k8s env 覆盖；这里把
// 「宿主机上留了容器 DNS」「该给地址却没给」统一变成一条点名键与 env 的
// 配置错误。远端服务暂时没起**不**在这里判死（那属于就绪探针 / 运行时降级）。
//
// 例外（2026-11-11）：`ai_service.url` 是**可选下游**，不参加启动期解析。
// 它此前被硬校验，导致 analysis 与 ai-service 互相等对方的 DNS（ai 必须
// 后于 analysis 启动，analysis 又要求 ai 已注册），冷启动必然死锁。AI 不是
// core 的承重依赖，地址在**首次反代调用**时解析，失败返回点名 env 与主机名
// 的 503（见 handlers_ai_proxy.go），而不是把整个进程挡在启动门外。
// 数据面 / risk / strategy 资产仍是启动必需的，硬校验保持原样。

import (
	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"tellurion/pkg/bootstrap"
)

// validateOutboundEndpoints 按「是否启用」逐项校验出站地址，返回第一条错误。
//
// 只校验真正会被读的项：strategy_asset / risk_service 的地址在开关关闭时
// 没有读取点，强制要求它们等于给开发环境制造噪声。
func validateOutboundEndpoints(v *viper.Viper, logger zerolog.Logger) error {
	checks := []struct {
		key    string
		envKey string
		value  string
	}{
		// 取数通道：宿主机默认 localhost:8081，容器由 DATA_SERVICE_URL 覆盖。
		{key: "data_service.url", envKey: "DATA_SERVICE_URL", value: v.GetString("data_service.url")},
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
			return err
		}
		logger.Debug().Str("key", c.key).Str("url", c.value).Msg("outbound endpoint ok")
	}
	return nil
}
