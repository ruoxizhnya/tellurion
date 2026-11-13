package repoguard

// X-CI-WINDOWS 结构护栏（2026-11-13，主任裁决 = 不引入 Windows runner，
// 改交叉编译门）：
//  1. `go` job 必须有 `Cross-compile gate (windows/amd64)`，同时跑
//     `GOOS=windows GOARCH=amd64 go build ./...` 与 `... go vet ./...`；
//  2. 任何 job 都不得使用 windows runner —— 裁决买的是编译期覆盖，不是
//     「再养一套 Windows 机器」；若哪天有人加了 runner，本条必须红，让成本
//     决策回到台账而不是漂在 yml 里。
//
// 为什么 build 和 vet 都要：build 覆盖非测试源码，vet 会连 _test.go 一起
// 做类型检查；AUD-26/AUD-38 的教训正是「测试文件里的 Windows 编译错误
// 只有 vet 能拦住」。

import (
	"strings"
	"testing"
)

func TestCIWindowsCrossCompileGate(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	wf := loadCIWorkflow(t, root)

	for name, job := range wf.Jobs {
		if strings.Contains(strings.ToLower(job.RunsOn), "windows") {
			t.Errorf("job %q 使用了 windows runner（%q）—— X-CI-WINDOWS 裁决是不引入 runner，只做交叉编译门", name, job.RunsOn)
		}
	}

	job, ok := wf.Jobs["go"]
	if !ok {
		t.Fatal("ci.yml 里找不到 `go` job")
	}
	var step *ciStep
	for i := range job.Steps {
		if job.Steps[i].Name == "Cross-compile gate (windows/amd64)" {
			step = &job.Steps[i]
			break
		}
	}
	if step == nil {
		t.Fatal("ci.yml 的 `go` job 里找不到 `Cross-compile gate (windows/amd64)` 步")
	}
	for _, want := range []string{
		"GOOS=windows GOARCH=amd64 go build ./...",
		"GOOS=windows GOARCH=amd64 go vet ./...",
	} {
		if !strings.Contains(step.Run, want) {
			t.Errorf("交叉编译门缺少 %q —— Windows 编译覆盖不完整", want)
		}
	}
}
