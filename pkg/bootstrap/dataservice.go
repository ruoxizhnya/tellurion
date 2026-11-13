package bootstrap

// dataservice.go —— CORE-DATA-URL-SINGLE-DEFAULT（2026-11-13）：
// `data_service.url` 的**唯一**解析入口。
//
// 背景：此前有两套缺省值 —— 回测取数侧（本包 BuildBacktestEngine）回落
// `http://localhost:8081`，网关反代侧（cmd/analysis/handlers_proxy.go）回落
// `http://data-service:8081`，cmd/analysis/kernel_wiring.go 又抄了一份
// localhost 版本。正常配置下都不触发，但缺项时会表现为「回测取不到数、
// 同步页却正常」的割裂故障 —— 两个调用点连的不是同一个上游。
//
// 现在的口径（与 broker / risk 的 fail-closed 一致）：**没有隐式默认值**。
// 显式配置（yaml `data_service.url` 或 env `DATA_SERVICE_URL`）是唯一来源；
// 缺失 / 不是绝对 URL 即返回错误，由调用方决定是拒绝启动（装配腿）还是
// 不注册该族路由（网关腿）。生产启动另有一道 `ValidateServiceEndpoint`
// （cmd/analysis/endpoints.go），两者判据一致，不会互相掩盖。

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/viper"
)

// DataServiceURL 解析 data-service 基址。显式配置优先，缺失即错误。
//
// 返回值去掉末尾的 `/`，调用方可以安全地拼 `base + "/path"`。
func DataServiceURL(v *viper.Viper) (string, error) {
	raw := strings.TrimSpace(v.GetString("data_service.url"))
	if raw == "" {
		return "", fmt.Errorf(
			"data_service.url is empty — 回测取数与 /api/sync/* 反代共用这一个地址，没有隐式默认值；" +
				"宿主机直跑设 DATA_SERVICE_URL=http://localhost:<port>，容器内设 http://data-service:<port>")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("data_service.url=%q is not an absolute URL (want http(s)://host:port)", raw)
	}
	return strings.TrimRight(raw, "/"), nil
}
