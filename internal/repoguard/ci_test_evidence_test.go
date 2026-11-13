package repoguard

// CORE-TEST-EVIDENCE-VERSIONED 的结构护栏（2026-11-13，主任裁决 = CI artifact +
// 仓内摘要）：
//  1. Test 步必须把完整输出落到 `gotest.log`，并仍然生成 `coverage.out`；
//  2. 同一 job 里必须有 `actions/upload-artifact` 的 `test-evidence` 步，
//     覆盖日志 + 覆盖率 + 集成腿日志，保留期写明；
//  3. 上传步在 Test 步之后，且 `if: always()` —— 失败时证据比通过时更重要。
//
// 为什么按 YAML 结构而不是 grep 文本：注释里写一个 `upload-artifact` 也能命中，
// 但真正的缺陷是「结构不存在 / 顺序不对 / 上传的是空路径」。

import (
	"strings"
	"testing"
)

func TestCITestEvidenceArtifact(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	wf := loadCIWorkflow(t, root)
	job, ok := wf.Jobs["go"]
	if !ok {
		t.Fatal("ci.yml 里找不到 `go` job —— 测试证据必须挂在跑测试的那个 job 上")
	}

	testIdx, uploadIdx := -1, -1
	for i, step := range job.Steps {
		if step.Name == "Test" {
			testIdx = i
			for _, want := range []string{"-coverprofile=coverage.out", "tee gotest.log"} {
				if !strings.Contains(step.Run, want) {
					t.Errorf("Test 步缺少 %q —— 日志/覆盖率必须落成可上传的文件（CORE-TEST-EVIDENCE-VERSIONED）", want)
				}
			}
		}
		if strings.Contains(step.Uses, "actions/upload-artifact@") && step.With["name"] == "test-evidence" {
			uploadIdx = i
			if step.If != "always()" {
				t.Errorf("test-evidence 上传步的 if = %q（期望 always()）—— 失败时恰恰最需要证据", step.If)
			}
			if got := step.With["retention-days"]; got != "30" {
				t.Errorf("test-evidence 的 retention-days = %q（期望 30）—— 保留期必须明写", got)
			}
			for _, want := range []string{"coverage.out", "gotest.log", "/tmp/integration.log"} {
				if !strings.Contains(step.With["path"], want) {
					t.Errorf("test-evidence 的 path 没覆盖 %q —— 测试证据不完整", want)
				}
			}
			if !strings.Contains(step.With["if-no-files-found"], "warn") {
				t.Errorf("test-evidence 的 if-no-files-found = %q（期望 warn）—— 编译期红时不能因缺 coverage.out 再报一次假红", step.With["if-no-files-found"])
			}
		}
	}
	if testIdx < 0 {
		t.Fatal("ci.yml 的 `go` job 里找不到 Test 步")
	}
	if uploadIdx < 0 {
		t.Fatal("ci.yml 的 `go` job 里找不到上传 test-evidence 的 upload-artifact 步")
	}
	if uploadIdx < testIdx {
		t.Error("test-evidence 上传步在 Test 步之前 —— 那时还没有日志可传")
	}
}
