package bootstrap

// endpoints.go —— 出站服务地址的启动期体检。
//
// 背景（2026-11-11）：config/*.yaml 曾是「混口径」的 —— DB/Redis 是宿主机
// 视角（localhost），而 data_service.url / strategy_asset.resolve_url 抄的是
// 容器服务名。宿主机直跑时那些名字解析不到，但失败点被推迟到第一条业务
// 请求，日志里只有 "no such host" / "connection refused"，既不知道是哪一项
// 配置、也不知道该填什么。
//
// 现在 yaml 统一为宿主机视角，容器由 compose/k8s 的 env 覆盖。本函数在启动
// 期把「解不出来的主机名」提前变成一条点名 env 的配置错误。

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// endpointResolveTimeout 是单个主机名的解析预算。启动期最多几项，串行也够快；
// 超时按「解析失败」处理（配置错误不该无限等）。
const endpointResolveTimeout = 2 * time.Second

// ValidateServiceEndpoint 校验一个出站服务地址：
//
//   - 空串 ⇒ 报错（该键没有可用默认值时必须显式给）；
//   - 不是带 scheme 的绝对 URL、或缺 host ⇒ 报错；
//   - 回环 / IP 字面量 ⇒ 直接通过（不做 DNS）；
//   - 其它主机名 ⇒ 解析一次。解析失败通常意味着「容器视角的地址被用在了
//     宿主机上」，错误信息同时给出 env 名与宿主机默认值。
//
// 它**不**探测端口可达性：被调服务可能晚于本进程启动，那是「暂时不可用」，
// 不是「配置错」。这里只区分这两类，避免把正常的启动竞争判成致命错误。
func ValidateServiceEndpoint(key, envKey, rawURL string) error {
	raw := strings.TrimSpace(rawURL)
	if raw == "" {
		return fmt.Errorf("%s is empty — set %s (container: service DNS, host: http://localhost:<port>)",
			key, envKey)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("%s=%q is not an absolute URL (want http(s)://host:port); set %s",
			key, raw, envKey)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%s=%q has no host; set %s", key, raw, envKey)
	}
	if isLoopbackOrIP(host) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), endpointResolveTimeout)
	defer cancel()
	if _, err := net.DefaultResolver.LookupHost(ctx, host); err != nil {
		return fmt.Errorf(
			"%s points at %q, whose host %q does not resolve here: %v. "+
				"Compose service names only work inside the container network — "+
				"for a host-side run set %s=http://localhost:<port>",
			key, raw, host, err, envKey)
	}
	return nil
}

// isLoopbackOrIP 报告 host 是否为本机回环或 IP 字面量（不需要 DNS）。
func isLoopbackOrIP(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil
}
