package repoguard

// QT-71（裁-17）部署清单静态护栏。
//
// 钉三件事：
//  1. `JWT_SECRET` 与三个服务 token **只经 secretKeyRef 注入**，清单里
//     没有 `value:` 形式的真值（只允许从 Secret 取）；
//  2. 四个部署清单（analysis / data / risk / analytics）都拿到 JWT_SECRET——
//     少一个就等于该服务在开鉴权的集群里裸奔；
//  3. 三个出站 token 各自打到它真正的消费方（analysis → risk resolve；
//     analytics → core / data）。
//
// 为什么需要静态腿：这些 YAML 不被任何 Go 代码加载，`go build` 永远看不见
// 漏掉的 env；k8s 也不会因为少一项而拒绝启动——只会在运行时 401 / failed。
//
// 缺口（如实登记）：本护栏不校验 Secret 对象是否存在（cluster-side），
// 也不校验轮换是否真的发生——那是运维动作。轮换周期写在 deploy/README.md。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type k8sSecretKeyRef struct {
	Name string `yaml:"name"`
	Key  string `yaml:"key"`
}

type k8sEnvVar struct {
	Name      string  `yaml:"name"`
	Value     *string `yaml:"value"`
	ValueFrom *struct {
		SecretKeyRef *k8sSecretKeyRef `yaml:"secretKeyRef"`
	} `yaml:"valueFrom"`
}

type k8sDeployment struct {
	Kind string `yaml:"kind"`
	Spec struct {
		Template struct {
			Spec struct {
				Containers []struct {
					Env []k8sEnvVar `yaml:"env"`
				} `yaml:"containers"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

// readDeploymentEnvs 解析一份 Deployment，返回 env 名 → 变量。
func readDeploymentEnvs(t *testing.T, path string) (map[string]k8sEnvVar, string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "读不到部署清单 %s", path)

	var d k8sDeployment
	require.NoError(t, yaml.Unmarshal(raw, &d), "%s 不是可解析的 YAML", path)
	require.Equal(t, "Deployment", d.Kind, "%s 的第一份文档应为 Deployment", path)
	require.Len(t, d.Spec.Template.Spec.Containers, 1, "%s 应只有一个应用容器", path)

	out := map[string]k8sEnvVar{}
	for _, e := range d.Spec.Template.Spec.Containers[0].Env {
		out[e.Name] = e
	}
	return out, string(raw)
}

// assertSecretInjected 断言 env 存在、且来自 secretKeyRef（不是明文 value）。
func assertSecretInjected(t *testing.T, path string, envs map[string]k8sEnvVar, name string) {
	t.Helper()
	e, ok := envs[name]
	require.True(t, ok, "%s 缺 env %s：该服务会静默落到 open-access / 无凭证", path, name)
	assert.Nil(t, e.Value, "%s 的 %s 不许写明文 value（只许 secretKeyRef）", path, name)
	require.NotNil(t, e.ValueFrom, "%s 的 %s 必须用 valueFrom", path, name)
	require.NotNil(t, e.ValueFrom.SecretKeyRef, "%s 的 %s 必须用 secretKeyRef", path, name)
	assert.NotEmpty(t, e.ValueFrom.SecretKeyRef.Name, "%s 的 %s 缺 Secret 名", path, name)
	assert.NotEmpty(t, e.ValueFrom.SecretKeyRef.Key, "%s 的 %s 缺 Secret key", path, name)
}

func TestQT71_ServiceCredentialsAreSecretRefs(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..", "deploy", "k8s")
	deployments := map[string]string{
		"analysis":  filepath.Join(root, "analysis-deployment.yaml"),
		"data":      filepath.Join(root, "data-deployment.yaml"),
		"risk":      filepath.Join(root, "risk-deployment.yaml"),
		"analytics": filepath.Join(root, "analytics-deployment.yaml"),
	}

	envs := map[string]map[string]k8sEnvVar{}
	for svc, path := range deployments {
		e, raw := readDeploymentEnvs(t, path)
		envs[svc] = e

		// 四个服务都必须能验证入站 token。
		assertSecretInjected(t, path, e, "JWT_SECRET")

		// 令牌真值的粗筛：JWT 的 base64url 头恒以 eyJ 开头。
		assert.NotContains(t, strings.ToLower(raw), "eyj",
			"%s 里出现了疑似 JWT 真值 —— token 只许经 Secret 注入", path)
	}

	// 三个出站服务 token 各自的真实消费方。
	assertSecretInjected(t, deployments["analysis"], envs["analysis"], "RISK_SERVICE_RESOLVE_TOKEN")
	assertSecretInjected(t, deployments["analytics"], envs["analytics"], "ANALYTICS_CORE_TOKEN")
	assertSecretInjected(t, deployments["analytics"], envs["analytics"], "DATA_SERVICE_TOKEN")

	// 反向：risk 是**只验证**的一方，不许持有出站 token。
	for _, name := range []string{"RISK_SERVICE_RESOLVE_TOKEN", "ANALYTICS_CORE_TOKEN", "DATA_SERVICE_TOKEN"} {
		_, has := envs["risk"][name]
		assert.False(t, has, "risk 不该持有出站 token %s", name)
	}
}

// TestQT71_ComposeGivesEveryServiceJWTSecret：compose 的三个应用服务都必须
// 显式给 JWT_SECRET。此前 data-service 没有这一项（实测），而 analysis / ai
// 都有 —— 少一项时 :8081 的写门在开鉴权部署里仍是裸的。
func TestQT71_ComposeGivesEveryServiceJWTSecret(t *testing.T) {
	t.Parallel()

	path := filepath.Join("..", "..", "docker-compose.yml")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	var doc struct {
		Services map[string]struct {
			Environment map[string]string `yaml:"environment"`
		} `yaml:"services"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &doc), "docker-compose.yml 必须可解析")

	for _, svc := range []string{"data-service", "analysis-service", "ai-service"} {
		s, ok := doc.Services[svc]
		require.True(t, ok, "docker-compose.yml 缺服务 %s", svc)
		_, has := s.Environment["JWT_SECRET"]
		assert.True(t, has, "docker-compose.yml 的 %s 缺 JWT_SECRET", svc)
	}
	assert.NotContains(t, strings.ToLower(string(raw)), "eyj",
		"docker-compose.yml 里出现了疑似 JWT 真值 —— token 只许经 env 传入")
}
