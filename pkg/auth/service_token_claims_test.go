package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kitauth "github.com/ruoxizhnya/tellurion-kit/auth"
)

// TestServiceTokenClaims_AreTheOnesTheDeploymentReliesOn 钉住 QT-77 的那条跨仓缝：
// 运维用 `tools/issue_service_token.go`（消费 kit `IssueServiceToken`）签出来的
// token，落到 core / risk / data 的校验侧时，**角色必须正好是 `service`、主体必须
// 正好是 `service:<repo>`、kind 必须是 access**。
//
// 这不是重复 kit 的签发用例：kit 会独立发版，本仓的部署清单与 route_policy 是
// 按这三个值写的；kit 改了 claim 形状而 pin 的本仓没跟，这里先红，而不是上线后
// 每个服务间调用 401。
func TestServiceTokenClaims_AreTheOnesTheDeploymentReliesOn(t *testing.T) {
	svc := kitauth.NewTokenService(kitauth.Config{
		JWTSecret: []byte("qt77-test-secret-not-used-in-deployments"),
		Issuer:    "tellurion",
	})
	token, err := svc.IssueServiceToken("service:analysis", kitauth.ServiceRole, 30*24*time.Hour)
	require.NoError(t, err)
	require.NotEmpty(t, token)

	claims, err := svc.Parse(token)
	require.NoError(t, err)
	assert.Equal(t, "service:analysis", claims.Subject, "sub 必须是 service:<repo>（裁-17）")
	assert.Equal(t, kitauth.ServiceRole, claims.Role, "role 必须是 service —— 其他角色在服务端 403 或 401")
	assert.Equal(t, "service:analysis", claims.Username, "审计需要非空 actor；kit 把 username 镜像成 subject")
	assert.Equal(t, kitauth.KindAccess, claims.Kind, "服务 token 只有 access 半边，没有 refresh")
	assert.Equal(t, "tellurion", claims.Issuer)
}
