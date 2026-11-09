package repoguard

// SVC-CORE-RUN-RETIRE ③ 的反向护栏（2026-11-10）：回测引擎不得重新长回
// 「直接依赖存储包」的那条腿。
//
// 本护栏钉四件事：
//
//  1. `pkg/backtest/**` 的非测试 Go 文件**不 import** `tellurion/pkg/storage`
//     ——持久化能力只经 `backtest.Storage` 接口注入。
//  2. 同一范围里代码中**零** `*storage.PostgresStore` 出现（含注释：
//    验收口径按全文计，注释里留旧类型名会误导后来者）。
//  3. 正向对照：`Engine.SetStore` 的参数仍是接口 `Storage`，且
//     `pkg/persistence` 里两种共享行形状仍在——防止「把文件删空」让前两条假绿。
//  4. 生产实现的结构化满足仍被装配层自证（`cmd/analysis/wiring` 的
//     `var _ backtest.Storage = (*storage.PostgresStore)(nil)`）——接口与 PG
//     实现一旦漂移，先红在这里，而不是运行期 panic。
//
// 为什么断言放在 wiring 而不是 storage：`pkg/storage` import `pkg/backtest`
// 会成环（backtest → strategy → storage），所以编译期自证只能由装配层承担。
//
// 破坏验证（2026-11-10 施工时实做）：往 `pkg/backtest/engine.go` 加回
// `"tellurion/pkg/storage"` import ⇒ 本护栏红；逐字节还原后复绿。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestBacktestDoesNotImportStorage 断言回测引擎与存储实现已解耦。
func TestBacktestDoesNotImportStorage(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	backtestDir := filepath.Join(root, "pkg", "backtest")

	importRe := regexp.MustCompile(`"tellurion/pkg/storage"`)
	concreteRe := regexp.MustCompile(`\*storage\.PostgresStore`)

	scanned := 0
	err := filepath.WalkDir(backtestDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if path != backtestDir && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		scanned++
		src := stripGoLineComments(readFile(t, path))
		rel, _ := filepath.Rel(root, path)
		if loc := importRe.FindStringIndex(src); loc != nil {
			line := 1 + strings.Count(src[:loc[0]], "\n")
			t.Errorf("%s:%d 重新 import 了 tellurion/pkg/storage —— "+
				"回测引擎只经 backtest.Storage 接口取持久化能力（SVC-CORE-RUN-RETIRE ③）", rel, line)
		}
		if loc := concreteRe.FindStringIndex(src); loc != nil {
			line := 1 + strings.Count(src[:loc[0]], "\n")
			t.Errorf("%s:%d 重新出现 *storage.PostgresStore —— "+
				"端口字段/参数必须是接口，不能退回具体存储类型（SVC-CORE-RUN-RETIRE ③）", rel, line)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 pkg/backtest 失败：%v", err)
	}
	if scanned == 0 {
		t.Fatal("pkg/backtest 下没有扫描到任何非测试 .go 文件 —— 护栏本身失效")
	}
}

// TestBacktestStoragePortStaysInjectable 是正向对照：接口与共享行形状必须还在，
// 且 PG 实现的结构化满足仍被装配层自证。
func TestBacktestStoragePortStaysInjectable(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)

	ports := readFile(t, filepath.Join(root, "pkg", "backtest", "ports.go"))
	if !regexp.MustCompile(`(?m)^type Storage interface \{`).MatchString(ports) {
		t.Error("pkg/backtest/ports.go 里找不到 `type Storage interface {` —— 端口定义被删会让反向护栏假绿")
	}
	if !regexp.MustCompile(`FundamentalsReader`).MatchString(ports) {
		t.Error("pkg/backtest/ports.go 里找不到 FundamentalsReader —— 基本面 PIT 端口丢了")
	}

	engine := readFile(t, filepath.Join(root, "pkg", "backtest", "engine.go"))
	if !regexp.MustCompile(`func \(e \*Engine\) SetStore\(store Storage\)`).MatchString(engine) {
		t.Error("Engine.SetStore 的参数不再是接口 Storage —— 回退成具体类型会重新引入 storage 依赖")
	}

	persistence := readFile(t, filepath.Join(root, "pkg", "persistence", "types.go"))
	for _, name := range []string{"RunProvenanceRecord", "AdjustmentAnchor"} {
		if !regexp.MustCompile(`(?m)^type ` + name + ` struct \{`).MatchString(persistence) {
			t.Errorf("pkg/persistence/types.go 里找不到 %s —— 共享行形状必须留在叶子包", name)
		}
	}

	// PG 实现的结构化满足：断言在装配层（storage → backtest 会成环）。
	wiring := readFile(t, filepath.Join(root, "cmd", "analysis", "wiring", "modules_analysis.go"))
	if !regexp.MustCompile(`_ backtest\.Storage = \(\*storage\.PostgresStore\)\(nil\)`).MatchString(wiring) {
		t.Error("cmd/analysis/wiring 里找不到 `_ backtest.Storage = (*storage.PostgresStore)(nil)` —— " +
			"端口与 PG 实现漂移将失去编译期自证")
	}

	// storage 侧别名必须保留（外部调用方继续用旧类型名，类型身份不变）。
	for file, name := range map[string]string{
		"run_provenance.go":     "RunProvenanceRecord",
		"adjustment_anchors.go": "AdjustmentAnchor",
	} {
		src := readFile(t, filepath.Join(root, "pkg", "storage", file))
		if !regexp.MustCompile(`(?m)^type ` + name + ` = persistence\.` + name + `$`).MatchString(src) {
			t.Errorf("pkg/storage/%s 里找不到 `type %s = persistence.%s` 别名 —— "+
				"外部调用方会断，或有人把结构复制成了第二份", file, name, name)
		}
	}
}
