package main

// endpoints_test.go —— 出站地址体检的「必查 / 不查」边界（2026-11-11）。
//
// 这组用例锁住一条行为变更：`ai_service.url` 是可选下游，**不**在 Boot 期
// 解析；data-service / risk / strategy 资产仍是启动必需的，硬校验不能被误删。
// AI 的失败在首次反代调用时才暴露，且必须点名 env 与主机名（另一条用例在
// handlers_ai_proxy_test.go）。

import (
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"
)

// unresolvable 是 RFC 2606 保留的 .invalid 域名：任何解析器都必须判失败。
const unresolvable = "http://service-name-that-does-not-exist.invalid:9999"

func TestValidateOutboundEndpoints_AIIsDeferredOthersStayHard(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(v *viper.Viper)
		wantErr string // 空 = 期望通过
	}{
		{
			name: "ai 不可解析不阻断 Boot",
			setup: func(v *viper.Viper) {
				v.Set("data_service.url", "http://localhost:8081")
				v.Set("ai_service.url", unresolvable)
			},
		},
		{
			name: "data 不可解析仍然硬失败",
			setup: func(v *viper.Viper) {
				v.Set("data_service.url", unresolvable)
			},
			wantErr: "DATA_SERVICE_URL",
		},
		{
			name: "risk resolve 开启时不可解析硬失败",
			setup: func(v *viper.Viper) {
				v.Set("data_service.url", "http://localhost:8081")
				v.Set("risk_service.resolve_enabled", true)
				v.Set("risk_service.resolve_url", unresolvable)
			},
			wantErr: "RISK_SERVICE_RESOLVE_URL",
		},
		{
			name: "risk resolve 关闭时不校验其地址",
			setup: func(v *viper.Viper) {
				v.Set("data_service.url", "http://localhost:8081")
				v.Set("risk_service.resolve_enabled", false)
				v.Set("risk_service.resolve_url", unresolvable)
			},
		},
		{
			name: "risk events 开启时不可解析硬失败",
			setup: func(v *viper.Viper) {
				v.Set("data_service.url", "http://localhost:8081")
				v.Set("risk_service.events_enabled", true)
				v.Set("risk_service.events_url", unresolvable)
			},
			wantErr: "RISK_SERVICE_EVENTS_URL",
		},
		{
			name: "strategy 资产 resolve 开启时不可解析硬失败",
			setup: func(v *viper.Viper) {
				v.Set("data_service.url", "http://localhost:8081")
				v.Set("strategy_asset.resolve_enabled", true)
				v.Set("strategy_asset.resolve_url", unresolvable)
			},
			wantErr: "STRATEGY_ASSET_RESOLVE_URL",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := viper.New()
			tc.setup(v)
			err := validateOutboundEndpoints(v, zerolog.Nop())
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want ok, got error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error naming %s, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error should name %s, got %q", tc.wantErr, err.Error())
			}
		})
	}
}
