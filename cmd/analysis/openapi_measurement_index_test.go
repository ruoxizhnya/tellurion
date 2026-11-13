package main

// CORE-OPENAPI-MEASUREMENT-INDEX（2026-11-13）的索引护栏。
//
// 缺陷现场：core 只有 `ANY /api/pipeline/*path` 反代族，三个测量端点的
// **实现**在 tellurion-agent（cmd/ai/handlers_pipeline.go:166-168），core 的
// openapi 索引里一条都没有。UI（pipeline.ts / reviewDesk）正在经 core 调它们，
// 而只读 openapi 的人会以为测量面不存在 —— 不是契约冲突，是索引漏登。
//
// 护栏钉三件事：
//  1. 三条 METHOD+path 必须在 `paths` 里，且标 `x-served-by: agent`；
//  2. 索引条目**不得复制 schema**（无 requestBody、responses 下无 content）——
//     复制就会与 tellurion-agent:docs/api-schema.md 静默漂移；
//  3. 反代族真在（`/api/pipeline/{path}` 的任意方法已注册），否则索引指向
//     一个不可达的路径。
//
// 删掉任一条 ⇒ 用例红（⑤ 的验收方式）。

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"tellurion/docs"
)

func TestOpenAPIMeasurementIndexEntries(t *testing.T) {
	var spec struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	require.NoError(t, yaml.Unmarshal(docs.OpenAPISpec, &spec), "docs/openapi.yaml 必须可解析")

	// 反代族：gin 的 `Any("/api/pipeline/*path")` 展开成 9 个方法。
	routes := registeredRoutes(t)
	for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
		require.Truef(t, routes[m+" /api/pipeline/{path}"],
			"core 没有 /api/pipeline/*path 反代族（%s）—— 索引里的测量端点将不可达", m)
	}

	entries := []struct{ method, path string }{
		{"post", "/api/pipeline/adoption"},
		{"get", "/api/pipeline/calibration"},
		{"get", "/api/pipeline/experiments/count"},
	}
	for _, e := range entries {
		ops, ok := spec.Paths[e.path]
		require.Truef(t, ok, "openapi 索引缺 %s %s —— 测量面经 core 反代可达，必须登记", strings.ToUpper(e.method), e.path)

		node, ok := ops[e.method]
		require.Truef(t, ok, "openapi 的 %s 缺 %s 方法", e.path, strings.ToUpper(e.method))
		require.Equal(t, yaml.MappingNode, node.Kind)

		fields := map[string]*yaml.Node{}
		for i := 0; i+1 < len(node.Content); i += 2 {
			fields[node.Content[i].Value] = node.Content[i+1]
		}

		servedBy, ok := fields["x-served-by"]
		require.Truef(t, ok, "%s %s 缺 x-served-by: agent —— 它不是 core 的实现", strings.ToUpper(e.method), e.path)
		require.Equal(t, "agent", servedBy.Value)

		// 不复制 schema：索引只写路径/方法/转发结果两档。
		require.NotContains(t, fields, "requestBody", "%s %s 复制了请求 schema —— 唯一权威在 tellurion-agent:docs/api-schema.md", strings.ToUpper(e.method), e.path)
		if resp, ok := fields["responses"]; ok {
			for i := 0; i+1 < len(resp.Content); i += 2 {
				code := resp.Content[i+1]
				for j := 0; j+1 < len(code.Content); j += 2 {
					require.NotEqual(t, "content", code.Content[j].Value,
						"%s %s 的 responses 带着 body schema —— 索引不得复制 agent 的定义", strings.ToUpper(e.method), e.path)
				}
			}
		}
	}
}
