package main

// CORE-TEST-HERMETIC（2026-11-13）：测试定位仓根不许靠「当前工作目录恰好是
// cmd/analysis」。
//
// 缺陷现场：rate_limit_budget_test.go 用 `filepath.Abs("../..")` 找 config/
// 与 docker-compose.yml。`go test ./cmd/analysis` 会把 cwd 设成包目录，所以
// CI 里一直绿；但 `go test -c` 出来的二进制、IDE 的自定义 cwd、或从别处直接
// 执行同一个二进制时，`../..` 指向完全不同的地方 —— 要么 ReadInConfig 报一个
// 看不出原因的错，要么更糟：读到别处同名文件后**假绿**。
//
// 这里用编译期写进二进制的本文件路径（runtime.Caller）定位仓根，并显式校验
// 仓根标记；标记缺失就 t.Fatal 点名，**不退回工作目录、不静默跳过**。

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// repoRootMarkers 是「这个目录确实是 core 仓根」的判据。
// 三个都取测试实际要用的资产：go.mod、shipped 配置、compose 文件。
var repoRootMarkers = []string{
	"go.mod",
	filepath.Join("config", "analysis-service.yaml"),
	"docker-compose.yml",
}

// validateRepoRoot 检查 dir 是否具备仓根标记，返回**所有**缺失项。
// 单独抽出来是为了能对「不合格目录」直接做反向测试（见下方用例）。
func validateRepoRoot(dir string) error {
	var missing []string
	for _, marker := range repoRootMarkers {
		if _, err := os.Stat(filepath.Join(dir, marker)); err != nil {
			missing = append(missing, marker)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%q 不是 core 仓根：缺 %v", dir, missing)
	}
	return nil
}

// repoRootForTest 用本测试文件的编译期路径上溯两级得到仓根。
func repoRootForTest(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 取不到本文件路径，无法定位仓根")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	if err := validateRepoRoot(root); err != nil {
		t.Fatalf("仓根定位失败：%v —— 不用工作目录兜底，也不跳过（CORE-TEST-HERMETIC）", err)
	}
	return root
}

// TestValidateRepoRootRejectsNonRepoDir 钉住 fail-closed 行为：
// 把「不是一个仓」的目录（t.TempDir）交给校验必须**明确报错**，
// 而不是返回一个「差不多能用」的路径让后续用例静默读空配置。
func TestValidateRepoRootRejectsNonRepoDir(t *testing.T) {
	if err := validateRepoRoot(t.TempDir()); err == nil {
		t.Fatal("空临时目录被判成 core 仓根 —— 校验器失效，测试会从任意 cwd 假绿")
	}
}

// TestRepoRootForTestFindsShippedAssets 钉住正向结果：定位到的仓根必须真的
// 能读到本用例服务的两个资产（避免「校验过、路径却不对」的中间态）。
func TestRepoRootForTestFindsShippedAssets(t *testing.T) {
	root := repoRootForTest(t)
	for _, rel := range []string{
		filepath.Join("config", "analysis-service.yaml"),
		"docker-compose.yml",
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("仓根 %q 下读不到 %q: %v", root, rel, err)
		}
	}
}
