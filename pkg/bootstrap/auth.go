package bootstrap

import (
	"fmt"
	"net"
	"strings"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"tellurion/pkg/auth"
	"tellurion/pkg/storage"
)

// InsecureExposureLoopbackPublished 是 auth.allow_insecure_exposure
// （env: AUTH_INSECURE_EXPOSURE）唯一承认的取值。
//
// 它存在的理由：`server.host` 描述的是**进程绑定的网卡**，不是**这个实例
// 真实的对外可达性**。在容器里这两者会脱钩 —— 容器必须绑 0.0.0.0 才能让
// 发布端口把流量转进来，但「谁能连上」完全由 `ports:` 的映射决定。
// 于是「容器内绑定 0.0.0.0 + 只发布到 127.0.0.1」这一组合，对外可达性
// 等价于 loopback，却会被原来的 isLoopbackHost 判成「暴露到局域网」而拒绝。
//
// 所以这里**不是删掉那道门，而是换一个判据**：operator 明确声明
// 「本实例的对外可达性由发布层限定为 loopback」。声明本身不做安全保证 ——
// 保证由两处机器校验给出，声明只是让「有人主动承诺过」这件事在配置里可见：
//
//  1. 静态：tools/check_deploy_consistency.py 的检查 3c —— 只要
//     AUTH_INSECURE 为真，**每一个** app 服务的**每一条** ports 映射都必须带
//     127.0.0.1: 前缀；反之只要有非回环映射，就必须给 JWT_SECRET。
//  2. 运行时：tools/local-stack.sh 读宿主机真实 netstat，断言 8080/8081/
//     8081/8085/8086 只监听回环（与 AUD-13 对 PG/Redis 的做法同源；8082 的
//     strategy-service 已于 REV-16 删除，不再在清单里）。
//
// 取值写错（拼错、空、写一半）一律按「没声明」处理 → 拒绝启动，fail closed。
const InsecureExposureLoopbackPublished = "loopback-published"

// AuthExposureOK 报告 exposure 声明是否被承认。
func AuthExposureOK(exposure string) bool {
	return strings.TrimSpace(exposure) == InsecureExposureLoopbackPublished
}

// AuthStartup 是启动期对鉴权配置的裁决结果。做成纯值是为了可测 ——
// 真正的 os.Exit 只在 InitAuth 里发生一次，测试测 DecideAuthStartup 即可。
type AuthStartup struct {
	Secret   []byte // 非空 = 全闸 JWT
	Insecure bool   // 明确处于「无鉴权且显式放开」模式（第三态）
	// Unavailable 是第二态：进程能起，但写面 / 受保护端点一律 503
	// auth_unavailable。它与 Insecure 互斥，Secret 非空时两者都为 false。
	Unavailable bool
	Refuse      bool   // 拒绝启动
	Reason      string // 拒绝原因（必须给出可操作的修复指引）
}

// DecideAuthStartup 裁决启动期的鉴权配置（P0-4，三态口径）。
//
// 三态（六仓统一；本仓只负责 service 侧）：
//
//  1. 有 JWT secret → 全闸。写端点必须带 token + 角色。
//  2. 无 secret + **没有显式声明** + 回环绑定 → 进程启动，但写面 / 受保护
//     端点一律 503 `auth_unavailable`，读端点保持公开。缺 secret 不再意味着
//     静默开放。
//  3. 无 secret + **有显式声明** → 放开（显式 open access），**绑定地址不限**。
//     显式声明有两种，任一即可：auth.allow_insecure=true（AUTH_INSECURE）或
//     auth.insecure_exposure=loopback-published（AUTH_INSECURE_EXPOSURE）。
//     容器里 core 必须绑 0.0.0.0，所以第 3 态绝不能因绑定地址拒绝启动；风险
//     由 InitAuth 的 WARN 披露（写面 / 角色校验无鉴权、绑定地址、非回环时点名
//     「已对外暴露」）。
//
// 无 secret + 无声明 + 非回环绑定 → 拒绝启动（fail closed）：既没有鉴权、又
// 没有任何「这是回环暴露」的承诺，0.0.0.0 上的写面等于把下单接口开给局域网。
//
// 注意 AUTH_INSECURE 的语义：它只是「显式声明」的一种，不再是每个请求都会
// 重新查的中间件开关。中间件是否放行完全由本裁决产出的 AuthStartup 决定
// （见 pkg/auth.Config.OpenAccess），启动日志因此能指认当前进程处于哪一态。
func DecideAuthStartup(secret string, allowInsecure bool, bindHost string, insecureExposure string) AuthStartup {
	if s := strings.TrimSpace(secret); s != "" {
		return AuthStartup{Secret: []byte(s)}
	}
	// 显式声明：AUTH_INSECURE=true 或 AUTH_INSECURE_EXPOSURE=loopback-published。
	// 两者任一都表示 operator 主动承诺「我知道这个实例没有鉴权」。
	declared := allowInsecure || AuthExposureOK(insecureExposure)
	if declared {
		return AuthStartup{Insecure: true}
	}
	if IsLoopbackHost(bindHost) {
		// 第二态：安全降级。读面可用，写面由 Middleware / authz 统一 503。
		return AuthStartup{Unavailable: true}
	}
	return AuthStartup{
		Refuse: true,
		Reason: fmt.Sprintf("auth: no JWT secret and no explicit declaration — server.host=%q is not "+
			"loopback, so an unauthenticated write surface would be reachable from the network. "+
			"Refusing to start. Set JWT_SECRET to enable authentication; or, for a local-only "+
			"instance, bind 127.0.0.1 (state 2: degraded, writes 503); or explicitly opt in to "+
			"open access with AUTH_INSECURE=true or AUTH_INSECURE_EXPOSURE=%s (state 3: WARN, any bind)",
			bindHost, InsecureExposureLoopbackPublished),
	}
}

// IsLoopbackHost 报告 host 是否只监听本机。空 host 视为非 loopback —
// gin 绑 ":port" 等价于 0.0.0.0，fail closed 更安全。
func IsLoopbackHost(host string) bool {
	h := strings.TrimSpace(host)
	if h == "" {
		return false
	}
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(h, "[]"))
	return ip != nil && ip.IsLoopback()
}

// InitAuth 构造 JWT + RBAC auth service（P1-2, ADR-017 §2）并执行三态
// 启动裁决：有 secret 全闸；无 secret 时要么安全降级（写面 503），要么
// 在显式 loopback 暴露下放开（WARN）。非回环暴露直接拒绝启动。
//
// 密钥来源（按优先级）：JWT_SECRET env → AUTH_JWT_SECRET env →
// auth.jwt_secret 配置项。YAML 是入库的，生产一律走 env。
func InitAuth(v *viper.Viper, store *storage.PostgresStore, logger zerolog.Logger) *auth.Service {
	d := DecideAuthStartup(
		v.GetString("auth.jwt_secret"),
		v.GetBool("auth.allow_insecure"),
		v.GetString("server.host"),
		v.GetString("auth.insecure_exposure"),
	)
	if d.Refuse {
		logger.Fatal().Msg(d.Reason)
	}

	authSvc := auth.NewService(store.DB(), auth.Config{
		JWTSecret:       d.Secret,
		OpenAccess:      d.Insecure,
		AccessTokenTTL:  v.GetDuration("auth.access_token_ttl"),
		RefreshTokenTTL: v.GetDuration("auth.refresh_token_ttl"),
		Issuer:          v.GetString("auth.issuer"),
	})
	LogAuthPosture(logger, authSvc, v)
	return authSvc
}

// LogAuthPosture 在启动日志里指认当前进程处于哪一态。抽成独立函数是为了可测：
// 第三态的 WARN 必须同时写出「写面 / 角色校验无鉴权」与「绑定地址」，绑非回环
// 时还要点名「已对外暴露」—— 这几句话是事后审计唯一能指着看的证据。
//
// 它只读 Service 的启动期结论（Enabled / OpenAccess），**不会**在每个请求上
// 重新读 AUTH_INSECURE：中间件放行与否完全由 DecideAuthStartup 决定。
func LogAuthPosture(logger zerolog.Logger, svc *auth.Service, v *viper.Viper) {
	switch {
	case svc.Enabled():
		logger.Info().
			Int("access_ttl_sec", int(svc.AccessTTL().Seconds())).
			Msg("auth: JWT enabled (P1-2)")
	case svc.OpenAccess():
		// 第三态：显式声明过的无鉴权。日志是唯一的「事后可审计」证据，
		// 必须同时写出「写面 / 角色校验无鉴权」与「绑定地址」；绑非回环时
		// 还要点名「已对外暴露」，否则这条 WARN 在容器里看起来和本地一样。
		host := v.GetString("server.host")
		msg := fmt.Sprintf("auth: INSECURE open-access — 当前写面与角色校验无鉴权 "+
			"(write endpoints and role enforcement have NO authentication); bind=%q", host)
		ev := logger.Warn().
			Str("state", "open-access").
			Str("bind", host).
			Bool("allow_insecure", v.GetBool("auth.allow_insecure")).
			Str("exposure", strings.TrimSpace(v.GetString("auth.insecure_exposure")))
		if !IsLoopbackHost(host) {
			msg += fmt.Sprintf(" — 绑定地址非回环（%s），已对外暴露 (exposed beyond loopback)", host)
			ev = ev.Bool("exposed_beyond_loopback", true)
		}
		ev.Msg(msg)
	default:
		// 第二态：安全降级。不能静默，否则运维会以为写接口只是坏了。
		logger.Warn().
			Str("state", "unavailable").
			Str("bind", v.GetString("server.host")).
			Msg("auth: no JWT secret and no explicit declaration — starting DEGRADED: " +
				"write and protected endpoints return 503 auth_unavailable, reads stay public. " +
				"Set JWT_SECRET to enable authentication; local open access requires " +
				"AUTH_INSECURE=true or AUTH_INSECURE_EXPOSURE=loopback-published")
	}
}
