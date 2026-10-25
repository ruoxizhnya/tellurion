package repoguard

// QT-51（BE-02）结构护栏：**`pkg/backtest` 不得 import `pkg/live`**。
//
// 背景：回测引擎曾直接持有 `live.LiveTrader`（`engine.go` / `options.go` /
// `execution/live_bridge.go` 三处），于是"改实盘撮合会悄悄改回测口径"。
// QT-51 把依赖反转成 `execution.ExecutionPort`，实盘适配搬到装配层
// （`cmd/analysis/wiring/live_trader_port.go`）。
//
// 执法口径：
//   - 扫描范围：`pkg/backtest` **全树**（含子包、含 `_test.go`）。
//     测试也不豁免：K5 的 paper↔backtest 对账已迁到 `e2e/backtest_live/`，
//     引擎包里不该再出现任何 `pkg/live` 引用。
//   - 判据：import 路径 == `pkg/live`、以 `/pkg/live` 结尾，或含 `/pkg/live/`
//     子包（`margin` / `reconciliation` / `stockstate` / `broker` 同属该域）。
//   - 命中即红，报错点名文件与 import 行。
//
// 正向腿：适配层文件必须存在且同时 import 两侧，确保"切断"不是靠
// 把能力删掉，而是靠把跨域翻译收敛到一个装配点。

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	qt51BacktestDir = "pkg/backtest"
	qt51AdapterPath = "cmd/analysis/wiring/live_trader_port.go"
)

// TestBacktestPackage_CannotImportLive 守住 QT-51 的依赖方向。
func TestBacktestPackage_CannotImportLive(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")
	backtestRoot := filepath.Join(root, filepath.FromSlash(qt51BacktestDir))

	violations := []string{} // "file -> import path"

	err := filepath.WalkDir(backtestRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		rel := fileRel(root, path)
		if rel == "" {
			return nil // 模块外
		}
		for _, imp := range liveImports(t, path) {
			violations = append(violations, rel+" -> "+imp)
		}
		return nil
	})
	require.NoError(t, err)

	sort.Strings(violations)
	assert.Empty(t, violations,
		"QT-51（BE-02）：pkg/backtest 全树不许 import pkg/live —— "+
			"引擎只认 execution.ExecutionPort；live.LiveTrader 的适配只允许住在 "+
			"装配层（%s）。若确有新的合法通道，先改设计并更新本护栏，而不是就地绕过：\n%s",
		qt51AdapterPath, strings.Join(violations, "\n"))
}

// TestLiveTraderAdapterExistsAtCompositionLayer 是正向腿：
// 实盘适配层必须存在，且同时认识两侧（import live + import 回测执行端）。
func TestLiveTraderAdapterExistsAtCompositionLayer(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")
	adapter := filepath.Join(root, filepath.FromSlash(qt51AdapterPath))

	imports := allImports(t, adapter)
	require.NotEmpty(t, imports, "读数失败：%s 不存在或不可解析（QT-51 的适配层丢了？）", qt51AdapterPath)

	assert.Contains(t, imports, "tellurion/pkg/live",
		"适配层必须 import pkg/live（它是实盘侧入口）")
	assert.Contains(t, imports, "tellurion/pkg/backtest/execution",
		"适配层必须 import 回测执行端（ExecutionPort 住在 pkg/backtest/execution）")
	assert.Contains(t, imports, "tellurion/pkg/backtest",
		"适配层必须能拿到 Engine（AttachLiveTrader 注入点）")
}

// liveImports 返回文件中 import 了 pkg/live 的路径列表。
// 解析失败返回 nil（由 go build / go vet 负责报错，护栏不重复）。
func liveImports(t *testing.T, path string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		return nil
	}
	var hits []string
	for _, spec := range file.Imports {
		val := strings.Trim(spec.Path.Value, `"`)
		if isLiveImport(val) {
			hits = append(hits, val)
		}
	}
	return hits
}

// isLiveImport 判定 import 路径是否属于 pkg/live 域（含子包）。
// 用边界匹配而不是裸 Contains，避免把 `pkg/liveevil` 误判成实盘域。
func isLiveImport(path string) bool {
	return path == "pkg/live" ||
		strings.HasSuffix(path, "/pkg/live") ||
		strings.Contains(path, "/pkg/live/")
}

// TestQT51IsLiveImportMatcher 锁住判据本身：精确包 / 子包命中，近似名不命中。
func TestQT51IsLiveImportMatcher(t *testing.T) {
	t.Parallel()

	cases := map[string]bool{
		"pkg/live":                          true,
		"tellurion/pkg/live":                true,
		"tellurion/pkg/live/margin":         true,
		"tellurion/pkg/live/reconciliation": true,
		"tellurion/pkg/backtest/execution":  false,
		"tellurion/pkg/liveevil":            false,
		"tellurion/pkg/livex/margin":        false,
	}
	for path, want := range cases {
		assert.Equal(t, want, isLiveImport(path), "isLiveImport(%q)", path)
	}
}

// allImports 返回文件的全部 import 路径（解析失败返回 nil）。
func allImports(t *testing.T, path string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(file.Imports))
	for _, spec := range file.Imports {
		out = append(out, strings.Trim(spec.Path.Value, `"`))
	}
	return out
}
