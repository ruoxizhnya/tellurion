package repoguard

// QT-79 部署护栏：`Dockerfile.service` 的**构建产物集合**与**COPY 集合**必须
// 一一对应，且每个产物都有真实存在的 `./cmd/<dir>` 源包。
//
// 为什么需要它：`Dockerfile.service` 历史上两次腐坏（ODR-021 删 `cmd/risk`、
// REV-16 删 `cmd/strategy`）都是「build 了但没 COPY」或「COPY 了但源已删」，
// 编译器与 `go build ./...` 都不检查这两件事，只有真 `docker build` 才炸，
// 而 CI 不构建镜像 ⇒ 长期静默。QT-79 把材料 §1.5 的 V7 判据固化成测试，
// 任何一侧加减二进制都必须同批改另一侧。
//
// 判据（与 QT-79 ⑤ 原式一致）：
//  1. `-o /app/<name>` 的 `<name>` 集合 == `COPY --from=builder /app/<name>` 的集合；
//  2. 每条 build 行里的 `./cmd/<dir>` 必须存在；
//  3. `docker-compose.yml` 里 `command: ["/app/<name>"]` 引用的二进制必须在产物集合里
//     （防「compose 定义了服务但镜像里没有这个二进制」）。

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
)

var (
	buildOutPattern = regexp.MustCompile(`go build -o /app/([a-zA-Z0-9_-]+) \./cmd/([a-zA-Z0-9_/-]+)`)
	copyPattern     = regexp.MustCompile(`COPY --from=builder /app/([a-zA-Z0-9_-]+) `)
	composeCmd      = regexp.MustCompile(`command: \["/app/([a-zA-Z0-9_-]+)"`)
)

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("解析仓根失败: %v", err)
	}
	return root
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s 失败: %v", path, err)
	}
	return string(b)
}

func setOf(matches [][]string) map[string]bool {
	out := map[string]bool{}
	for _, m := range matches {
		out[m[1]] = true
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestDeployManifest_BuildAndCopyMatch(t *testing.T) {
	root := repoRoot(t)
	dockerfile := readFile(t, filepath.Join(root, "Dockerfile.service"))

	builds := buildOutPattern.FindAllStringSubmatch(dockerfile, -1)
	copies := copyPattern.FindAllStringSubmatch(dockerfile, -1)

	if len(builds) == 0 {
		t.Fatal("Dockerfile.service 里没有解析到任何 `go build -o /app/<name> ./cmd/<dir>`")
	}
	built := setOf(builds)
	copied := setOf(copies)

	for name, ok := range built {
		if !ok {
			continue
		}
		if !copied[name] {
			t.Errorf("构建了 /app/%s 但没有对应的 COPY --from=builder 行（旧 bug 形态：build 了但没进镜像）", name)
		}
	}
	for name := range copied {
		if !built[name] {
			t.Errorf("COPY 了 /app/%s 但 plan 段没有构建它（旧 bug 形态：COPY 了不存在的产物）", name)
		}
	}

	for _, m := range builds {
		dir := filepath.Join(root, "cmd", filepath.FromSlash(m[2]))
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Errorf("构建目标 ./cmd/%s 不存在（删目录时必须同批删 build/COPY 行）", m[2])
		}
	}

	t.Logf("构建产物 %v；COPY 集合 %v", sortedKeys(built), sortedKeys(copied))
}

// 跨仓 context 的例外：这些命令的二进制由**别的仓的 Dockerfile** 构建，
// 不在本仓 Dockerfile.service 的产物集合里。新增例外必须写明构建来源。
var externalBinaries = map[string]string{
	"ai-service": "../tellurion-agent 的 Dockerfile（跨仓 context，本仓不构建）",
}

func TestDeployManifest_ComposeCommandsExistInImage(t *testing.T) {
	root := repoRoot(t)
	dockerfile := readFile(t, filepath.Join(root, "Dockerfile.service"))
	compose := readFile(t, filepath.Join(root, "docker-compose.yml"))

	built := setOf(buildOutPattern.FindAllStringSubmatch(dockerfile, -1))
	for _, m := range composeCmd.FindAllStringSubmatch(compose, -1) {
		if built[m[1]] {
			continue
		}
		if reason, ok := externalBinaries[m[1]]; ok {
			t.Logf("/app/%s 由外部构建：%s", m[1], reason)
			continue
		}
		t.Errorf("docker-compose.yml 的 command 引用了 /app/%s，但 Dockerfile.service 不构建它", m[1])
	}
}
