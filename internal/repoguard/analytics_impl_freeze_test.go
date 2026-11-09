package repoguard

// SVC-CORE-AN-RETIRE 冻结快照（归属结论：保留在 core，引用面冻结）。
//
// 结论与依据见 docs/TASKS.md 本单 施工记录：
//   - 迁移结局被前置卡住：tellurion-analytics 的 `analysis.benchmarks`
//     写方法（`RegisterBenchmark`）**生产零调用**，只有测试在调（跨仓待核 K7）。
//   - core 的两包是**运行时承重实现**：`pkg/backtest/engine.go` 每次 run 用
//     `metrics.GenerateBacktestResult` 产出指标，`cmd/analysis/handlers_backtest.go`
//     用 `reporting.RenderHTML` / `CompareReports` 产出导出与对比，`cmd/dualrun`
//     用它做双内核等价性判定。删掉不是「产品面收口」，是砍运行时。
//   - analytics 侧明文「上游计算口径的参考实现仍在 core，本仓只对照公式、
//     **不 import**」（`tellurion-analytics:AGENTS.md` §1），且其指标面是
//     core 的子集 —— 迁移需要新端点 + DTO，超出 core 单仓范围。
//
// 本护栏钉住「恰好这 3 处生产 import」，新增一条即红；少一条也红（快照过期
// 必须在同一笔 commit 里显式订正，不许悄悄漂移）。
//
// 破坏验证（2026-11-09 施工时实做）：往 `cmd/analysis/setup.go` 加一行
// `_ "tellurion/pkg/backtest/metrics"` ⇒ 红；删除探针后复绿。

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// frozenAnalyticsImports 是 2026-11-09 冻结时的**完整**生产侧引用集合。
// 键 = 相对仓库根的路径（正斜杠），值 = 该文件 import 的包（短名）。
var frozenAnalyticsImports = map[string][]string{
	"cmd/analysis/handlers_backtest.go": {"reporting"},
	"cmd/dualrun/main.go":               {"metrics"},
	"pkg/backtest/engine.go":            {"metrics"},
}

func TestAnalyticsImplementationStaysFrozenInCore(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	importRe := map[string]*regexp.Regexp{
		"metrics":   regexp.MustCompile(`"tellurion/pkg/backtest/metrics"`),
		"reporting": regexp.MustCompile(`"tellurion/pkg/backtest/reporting"`),
	}

	found := map[string][]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "web", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src := stripGoLineComments(readFile(t, path))
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		for name, re := range importRe {
			if re.MatchString(src) {
				found[rel] = append(found[rel], name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历仓库失败：%v", err)
	}
	for k := range found {
		sort.Strings(found[k])
	}

	// 新增即红。
	for rel, pkgs := range found {
		if _, ok := frozenAnalyticsImports[rel]; !ok {
			t.Errorf("%s import 了 %v —— SVC-CORE-AN-RETIRE 的归属结论是「保留在 core、"+
				"引用面冻结」：新增引用必须先在 docs/TASKS.md 重开归属裁决，并同批更新本快照",
				rel, pkgs)
		}
	}
	// 少一条也红（快照过期）。
	for rel, want := range frozenAnalyticsImports {
		got, ok := found[rel]
		if !ok {
			t.Errorf("冻结快照里的 %s 不再 import %v —— 若是有意收窄，请同批订正本护栏与 TASKS 施工记录", rel, want)
			continue
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s 的 import 变成 %v，冻结快照是 %v —— 请同批订正", rel, got, want)
		}
	}

	// 正向对照：两包本体仍在，且冻结时被消费的入口没有消失（防「删过头」假绿）。
	metricsSrc := readFile(t, filepath.Join(root, "pkg", "backtest", "metrics", "performance.go"))
	if !strings.Contains(metricsSrc, "func GenerateBacktestResult(") {
		t.Errorf("pkg/backtest/metrics 里找不到 GenerateBacktestResult —— " +
			"engine.go / dualrun 的调用点会立刻编译失败；若确已迁移，请同批重开归属裁决")
	}
	reportingDir := filepath.Join(root, "pkg", "backtest", "reporting")
	var reportAPI strings.Builder
	for _, name := range nonTestGoFiles(t, reportingDir) {
		reportAPI.WriteString(readFile(t, filepath.Join(reportingDir, name)))
		reportAPI.WriteString("\n")
	}
	for _, fn := range []string{"func RenderHTML(", "func CompareReports("} {
		if !strings.Contains(reportAPI.String(), fn) {
			t.Errorf("pkg/backtest/reporting 里找不到 %s —— handlers_backtest.go 的导出/对比面会编译失败", fn)
		}
	}
}
