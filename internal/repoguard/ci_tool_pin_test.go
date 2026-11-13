package repoguard

// X-CI-BASELINE-PIN core 腿（2026-11-13）的结构护栏：
//  1. CI 里的工具版本必须是**具体版本**，不许 `@latest` —— 否则同一条没人改过
//     的流水线会随上游发版自行变红/变绿，基线也就无声漂移；
//  2. Go 工具链版本同理，不许 `stable` / `latest` / 带 `x` 的浮动写法；
//  3. 全套件（`go test ./...`）只跑一次且带 `-race`：core 原本就是这一形态，
//     X-CI-BASELINE-PIN ③③ 的「先全量再 -race 全量」重复执行在 core 不成立，
//     本护栏防止将来被改成两遍。

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	// 任何 `pkg@latest` 形式的工具调用（go run / go install）。
	unpinnedToolRe = regexp.MustCompile(`[\w./-]+@latest\b`)
	// govulncheck 的具体 pin。
	govulnPinRe = regexp.MustCompile(`golang\.org/x/vuln/cmd/govulncheck@v[0-9]+\.[0-9]+\.[0-9]+`)
	// go-version 的浮动写法。
	floatingGoVersionRe = regexp.MustCompile(`(?m)^\s*go-version:\s*["']?(stable|latest|[0-9]+\.[0-9]+\.x|[0-9]+\.x)["']?\s*$`)
	// 全套件测试调用（不含带 tag 的集成腿）。
	fullSuiteRe = regexp.MustCompile(`go test \./\.\.\.`)
)

// ciRuns 收集 ci.yml 里所有 step 的 run 文本，附带 step 名。
func ciRuns(t *testing.T, root string) (map[string]string, string) {
	t.Helper()
	wf := loadCIWorkflow(t, root)
	runs := map[string]string{}
	var all strings.Builder
	for _, j := range wf.Jobs {
		for _, s := range j.Steps {
			if s.Run == "" {
				continue
			}
			runs[s.Name] = s.Run
			all.WriteString(s.Run)
			all.WriteString("\n")
		}
	}
	return runs, all.String()
}

func TestCIToolVersionsArePinned(t *testing.T) {
	root := repoRoot(t)
	runs, all := ciRuns(t, root)

	// 判据 1：run 里不得出现 @latest。
	if hits := unpinnedToolRe.FindAllString(all, -1); len(hits) > 0 {
		t.Errorf("ci.yml 的 run 里仍有浮动的工具版本 %v —— 改成具体版本并注释出处"+
			"（X-CI-BASELINE-PIN）", hits)
	}

	// 判据 2：govulncheck 必须钉住。
	if !govulnPinRe.MatchString(all) {
		t.Errorf("ci.yml 里找不到 `govulncheck@vX.Y.Z` 形式的 pin —— 依赖漏洞门必须可复现")
	}

	// 判据 3：Go 工具链版本不许浮动。
	raw := readFile(t, filepath.Join(root, ".github", "workflows", "ci.yml"))
	if m := floatingGoVersionRe.FindString(raw); m != "" {
		t.Errorf("ci.yml 的 Go 版本是浮动写法 %q —— 钉成具体 patch 版本（如 1.26.11）", strings.TrimSpace(m))
	}

	// 判据 4：全套件只跑一次，且带 -race。
	count := len(fullSuiteRe.FindAllString(all, -1))
	if count != 1 {
		t.Errorf("ci.yml 里 `go test ./...` 出现 %d 次（期望 1）—— core 的单次全量已带 -race，"+
			"再叠一遍全量只是把 CI 时间翻倍（X-CI-BASELINE-PIN ③③）", count)
	}
	for name, run := range runs {
		if !fullSuiteRe.MatchString(run) {
			continue
		}
		if !strings.Contains(run, "-race") {
			t.Errorf("step %q 跑了全套件却没有 -race —— core 的竞态门就嵌在这一步里", name)
		}
	}
}
