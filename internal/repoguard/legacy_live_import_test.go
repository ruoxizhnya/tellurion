package repoguard

// SVC-CORE-BROKER-RETIRE ③ 结构护栏：**全仓不得再出现 `tellurion/pkg/live`**。
//
// 背景：`pkg/live` 在步骤 A 整包改名为 `pkg/trading`（施工记录见
// docs/TASKS.md 的 SVC-CORE-BROKER-RETIRE）。改名后若还有 import 指回旧路径，
// 通常意味着有人从旧分支 / 旧文档 / 旧缓存把路径抄了回来；更糟的是有人为了
// 让它编译过，把旧目录重新建出来，于是两套路径并存、包注释与真实 owner 再次
// 漂移。
//
// 与 backtest_live_isolation_test.go 的分工：
//   - 那边管「`pkg/backtest` 不得 import `pkg/trading`」（依赖方向）；
//   - 这边管「任何位置都不得 import `pkg/live`」（旧路径已注销）。
//
// 两者都用 AST 读 import，而不是文本 grep —— 注释与文档里可以继续写旧路径，
// 那是在记录历史；只有真的 import 才算复活。
//
// 判据：import 路径 == `pkg/live`、以 `/pkg/live` 结尾，或含 `/pkg/live/` 子包。
// 同时断言旧目录本身不存在：零 import 也可能只是「目录还在但没人用」。

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const legacyLiveDir = "pkg/live"

// TestLegacyLivePackageIsGone 守住「pkg/live 已注销」。
func TestLegacyLivePackageIsGone(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(legacyLiveDir))); err == nil {
		t.Errorf("%s 目录又出现了 —— 它已在 SVC-CORE-BROKER-RETIRE 步骤 A 改名为 "+
			"pkg/trading；要么删掉它，要么在 TASKS 里说明当初的改名判断错在哪", legacyLiveDir)
	}

	var violations []string // "file -> import path"

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".workbuddy-ai", "node_modules", "web", "testdata", "vendor":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		rel := fileRel(root, path)
		if rel == "" {
			return nil // 模块外
		}
		file, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if perr != nil {
			return nil // 由 go build / go vet 报语法错误，护栏不重复
		}
		for _, spec := range file.Imports {
			p := strings.Trim(spec.Path.Value, `"`)
			if isLegacyLiveImport(p) {
				violations = append(violations, rel+" -> "+p)
			}
		}
		return nil
	})
	require.NoError(t, err)

	sort.Strings(violations)
	assert.Empty(t, violations,
		"SVC-CORE-BROKER-RETIRE：pkg/live 已改名为 pkg/trading，全仓不得再 import 旧路径。"+
			"若确有新的合法用途，先改设计并更新本护栏，而不是把旧目录建回来：\n%s",
		strings.Join(violations, "\n"))
}

// isLegacyLiveImport 判定 import 路径是否属于旧的 pkg/live 域（含子包）。
// 用边界匹配而不是裸 Contains，避免把 `pkg/livewire` 误判成旧路径。
func isLegacyLiveImport(path string) bool {
	return path == "pkg/live" ||
		strings.HasSuffix(path, "/pkg/live") ||
		strings.Contains(path, "/pkg/live/")
}

// TestLegacyLiveImportMatcher 锁住判据本身：精确包 / 子包命中，近似名不命中。
func TestLegacyLiveImportMatcher(t *testing.T) {
	t.Parallel()

	cases := map[string]bool{
		"pkg/live":                             true,
		"tellurion/pkg/live":                   true,
		"tellurion/pkg/live/reconciliation":    true,
		"tellurion/pkg/trading":                false,
		"tellurion/pkg/trading/reconciliation": false,
		"tellurion/pkg/livewire":               false,
		"tellurion/pkg/livewire/x":             false,
	}
	for path, want := range cases {
		assert.Equal(t, want, isLegacyLiveImport(path), "isLegacyLiveImport(%q)", path)
	}
}
