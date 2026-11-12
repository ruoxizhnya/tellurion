package repoguard

// CORE-REDIS-EXIT 反向护栏：Redis 退出是**不可逆**的（2026-11-12）。
//
// 本护栏钉四件事：
//
//  1. 三处 Redis 死代码与 go-redis 依赖不得回归：
//     `pkg/storage/redis.go` / `pkg/storage/types.go`（Cache 接口）/
//     `pkg/trading/redis_order_store.go` / `pkg/marketdata/cached_provider.go`；
//     `go.mod` 与任意 `.go` 都不得再出现 `github.com/redis/go-redis`。
//  2. 部署面不得再宣称 Redis 依赖：`deploy/k8s/redis-deployment.yaml` 不存在，
//     `docker-compose.yml` / `deploy/k8s/configmap.yaml` / `deploy/wait-for-deps.sh`
//     不再出现 `REDIS_URL`。
//  3. 配置面不得留幽灵键：`config/analysis-service.yaml` 无 `redis:` 段。
//  4. 正向对照（防止护栏因文件被删空/改名而假绿）：同一批文件里必须仍能看到
//     `data_service:`、`BROKER_URL` 与 `pgx` —— 它们与 Redis 无关，但同属
//     装配面，文件一旦被掏空这条先红。
//
// 为什么是静态断言：Redis 没有接线方，行为测试跑不出「零消费者」；而
// 文件/依赖/配置键是纯文本事实，在无库、无 Redis 的 CI 环境同样有效。
//
// 破坏验证（2026-11-12 施工时实做）：把 `pkg/storage/redis.go` 以空包文件
// 复活 ⇒ 本护栏红；删除后复绿。`docker-compose.yml` 加回 `REDIS_URL:` ⇒ 红；
// 删除后复绿。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedisRetiredArtifactsStayGone(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	for _, rel := range []string{
		filepath.Join("pkg", "storage", "redis.go"),
		filepath.Join("pkg", "storage", "types.go"),
		filepath.Join("pkg", "trading", "redis_order_store.go"),
		filepath.Join("pkg", "marketdata", "cached_provider.go"),
		filepath.Join("deploy", "k8s", "redis-deployment.yaml"),
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
			t.Errorf("%s 仍存在 —— Redis 三处死代码/清单已随 CORE-REDIS-EXIT 删除，不得复活", rel)
		} else if !os.IsNotExist(err) {
			t.Errorf("无法判定 %s 是否存在：%v", rel, err)
		}
	}
}

func TestNoGoRedisDependencyOrImport(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)

	gomod := readFile(t, filepath.Join(root, "go.mod"))
	if strings.Contains(gomod, "redis/go-redis") {
		t.Error("go.mod 仍依赖 github.com/redis/go-redis —— CORE-REDIS-EXIT 已删除该依赖，不得回归")
	}

	// 排除 repoguard 自身：本文件在断言里含有该字面量，不能自证其罪。
	err := filepath.Walk(filepath.Join(root), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" || info.Name() == "vendor" || info.Name() == "repoguard" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(src), "redis/go-redis") {
			rel, _ := filepath.Rel(root, path)
			t.Errorf("%s 仍 import/require redis/go-redis —— 仓内已无读取点（CORE-REDIS-EXIT）", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("扫描 .go 文件失败：%v", err)
	}
}

func TestDeployAndConfigSurfacesHaveNoRedis(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	files := []string{
		filepath.Join("docker-compose.yml"),
		filepath.Join("deploy", "k8s", "configmap.yaml"),
		filepath.Join("deploy", "wait-for-deps.sh"),
		filepath.Join("config", "analysis-service.yaml"),
	}
	for _, rel := range files {
		src := readFile(t, filepath.Join(root, rel))
		if strings.Contains(stripHashComments(src), "REDIS_URL") {
			t.Errorf("%s 仍有生效的 REDIS_URL 配置 —— 本仓已无读取点，保留只会让部署面继续宣称一个不存在的依赖", rel)
		}
	}

	// 配置段：只认行首的 `redis:`，避免把说明文字里的 “Redis” 误判成幽灵键。
	cfg := readFile(t, filepath.Join(root, "config", "analysis-service.yaml"))
	for i, line := range strings.Split(cfg, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "redis:") {
			t.Errorf("config/analysis-service.yaml:%d 仍有 `redis:` 配置段 —— 键已随 CORE-REDIS-EXIT 删除", i+1)
		}
	}

	// 正向对照：同一批文件必须仍有正常内容，防止「文件被删空」造成假绿。
	if !strings.Contains(readFile(t, filepath.Join(root, "config", "analysis-service.yaml")), "data_service:") {
		t.Error("config/analysis-service.yaml 里找不到 data_service: —— 文件疑似被掏空，本护栏失去意义")
	}
	if !strings.Contains(readFile(t, filepath.Join(root, "deploy", "wait-for-deps.sh")), "BROKER_URL") {
		t.Error("deploy/wait-for-deps.sh 里找不到 BROKER_URL —— 文件疑似被掏空，本护栏失去意义")
	}
	if !strings.Contains(readFile(t, filepath.Join(root, "go.mod")), "pgx/v5") {
		t.Error("go.mod 里找不到 pgx/v5 —— 文件疑似被掏空，本护栏失去意义")
	}
}

// stripHashComments 去掉 YAML / shell 的 `#` 行内注释。注释里写「REDIS_URL 已删除」
// 是历史说明，不是生效配置；不剥掉会把护栏逼成假红（与 canonical_layer_test.go 的
// 注释豁免同一口径）。
func stripHashComments(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}
