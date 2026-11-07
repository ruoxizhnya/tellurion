//go:build ignore
// +build ignore

// issue_service_token 是 QT-77 的**唯一签发入口**：调用 tellurion-kit 的
// `TokenService.IssueServiceToken`，不自己拼 JWT、不自己定 claim 形状。
//
// 用法（secret 只从 env 读；不进 argv、不进日志、不出现在退出码里）：
//
//	JWT_SECRET=... go run tools/issue_service_token.go -sub service:analysis -ttl 720h
//
// stdout 只输出 token 本身（方便 `$(...)` 捕获）；其余信息一律走 stderr。
// 签发失败以退出码 2 结束，且不会回显 secret。
//
// 为什么 subject 必须是 `service:<repo>`：裁-17 把服务身份定死成这个前缀，
// kit 的 middleware / httpadapter 只对 `role=service` 豁免「数字 subject」
// 校验；签一个 `service:` 之外的主体却仍是 service 角色，等于绕开那道豁免的
// 命名前提。这里当场拒绝，而不是签出来让别人 401。
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	kitauth "github.com/ruoxizhnya/tellurion-kit/auth"
)

func main() {
	sub := flag.String("sub", "", "服务主体，必须是 service:<repo>（例：service:analysis）")
	ttl := flag.Duration("ttl", 30*24*time.Hour, "有效期（部署口径：服务 token 30 天，见 deploy/README.md）")
	secretEnv := flag.String("secret-env", "JWT_SECRET", "存放共享 JWT secret 的环境变量名")
	issuer := flag.String("issuer", "tellurion", "iss（签发方）")
	flag.Parse()

	subject := strings.TrimSpace(*sub)
	if !strings.HasPrefix(subject, "service:") || strings.TrimSpace(strings.TrimPrefix(subject, "service:")) == "" {
		fail("-sub 必须是 service:<repo>，收到 %q", *sub)
	}
	if *ttl <= 0 {
		fail("-ttl 必须为正，收到 %s", *ttl)
	}
	secret := strings.TrimSpace(os.Getenv(*secretEnv))
	if secret == "" {
		// 明确点名变量名，但不打印任何值；也不回落到配置文件 —— 签发是运维动作，
		// 不允许「碰巧读了某个 yaml」这种来源。
		fail("环境变量 %s 为空：签发必须显式注入共享 JWT secret", *secretEnv)
	}

	svc := kitauth.NewTokenService(kitauth.Config{
		JWTSecret: []byte(secret),
		Issuer:    *issuer,
	})
	token, err := svc.IssueServiceToken(subject, kitauth.ServiceRole, *ttl)
	if err != nil {
		fail("签发失败: %v", err)
	}

	fmt.Fprintf(os.Stderr, "issued sub=%s role=%s ttl=%s iss=%s (secret from %s)\n",
		subject, kitauth.ServiceRole, ttl.String(), *issuer, *secretEnv)
	fmt.Println(token)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "issue_service_token: "+format+"\n", args...)
	os.Exit(2)
}
