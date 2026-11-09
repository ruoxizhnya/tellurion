package repoguard

// SVC-CORE-RUN-RETIRE 反向护栏（walk-forward 半边）：`walk_forward_reports`
// 的持久化退役同样是**不可逆**的。
//
// 本护栏钉三件事：
//
//  1. `pkg/storage` 的非测试 Go 文件里，除了行注释外**零** `walk_forward_reports`
//     引用 —— 建表 DDL、INSERT、SELECT 都不许再回来（owner = orchestration
//     的 `run.walk_forward_reports`）。
//  2. `pkg/backtest/walkforward` 的非测试 Go 文件**不 import**
//     `tellurion/pkg/storage` —— 计算引擎不得重新长出一条落库腿。
//  3. 正向对照：`pkg/storage/postgres.go` 仍须建 `backtest_jobs` 与
//     `factor_cache`。`backtest_jobs` 半边归 `SVC-ORCH-G3`，本次明确保留；
//     若 postgres.go 被整块删空，这条先红，防止前两条假绿。
//
// 为什么是静态断言而不是行为测试：表已被删掉，连不上库时「没有这张表」什么
// 也不证明；而 SQL 文本是纯事实，静态检查在 CI 无库环境同样有效。
//
// 破坏验证（2026-11-09 施工时实做）：往 `pkg/storage/cache.go` 加回一行含
// `walk_forward_reports` 的 SQL ⇒ 本护栏红；往 `walkforward.go` 加回
// `"tellurion/pkg/storage"` import ⇒ 红；逐字节还原后复绿。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestWalkForwardReportsPersistenceIsRetired 断言持久化腿零残留。
func TestWalkForwardReportsPersistenceIsRetired(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)

	// ① pkg/storage：非测试文件零 walk_forward_reports（行注释豁免 ——
	// 「已退役」的说明本身不是建表/读写）。
	storageDir := filepath.Join(root, "pkg", "storage")
	tableRe := regexp.MustCompile(`walk_forward_reports`)
	for _, name := range nonTestGoFiles(t, storageDir) {
		src := stripGoLineComments(readFile(t, filepath.Join(storageDir, name)))
		if loc := tableRe.FindStringIndex(src); loc != nil {
			line := 1 + strings.Count(src[:loc[0]], "\n")
			t.Errorf("pkg/storage/%s:%d 仍引用 walk_forward_reports —— "+
				"该表 owner 是 orchestration 的 run.walk_forward_reports；"+
				"core 只保留 WF 计算，不得再建表或读写（SVC-CORE-RUN-RETIRE）", name, line)
		}
	}

	// ② pkg/backtest/walkforward：非测试文件不 import tellurion/pkg/storage。
	wfDir := filepath.Join(root, "pkg", "backtest", "walkforward")
	importRe := regexp.MustCompile(`"tellurion/pkg/storage"`)
	for _, name := range nonTestGoFiles(t, wfDir) {
		src := stripGoLineComments(readFile(t, filepath.Join(wfDir, name)))
		if loc := importRe.FindStringIndex(src); loc != nil {
			line := 1 + strings.Count(src[:loc[0]], "\n")
			t.Errorf("pkg/backtest/walkforward/%s:%d 重新 import 了 tellurion/pkg/storage —— "+
				"WF 报告不再落库，计算引擎不得持有存储依赖（SVC-CORE-RUN-RETIRE）", name, line)
		}
	}

	// ③ 正向对照：本次**保留**的 backtest_jobs（归 SVC-ORCH-G3）与
	// factor_cache 必须仍在 DDL 里。
	postgres := stripGoLineComments(readFile(t, filepath.Join(storageDir, "postgres.go")))
	for _, kept := range []string{"backtest_jobs", "factor_cache"} {
		keptRe := regexp.MustCompile(`(?i)CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+` + kept + `\b`)
		if !keptRe.MatchString(postgres) {
			t.Errorf("pkg/storage/postgres.go 里找不到 %s 的 CREATE TABLE —— "+
				"本次只退役 walk_forward_reports；若确已改判，请同批订正本护栏", kept)
		}
	}
}

// nonTestGoFiles 返回 dir 下的非测试 .go 文件名（已排序，便于稳定报错）。
func nonTestGoFiles(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取目录 %s 失败：%v", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		names = append(names, e.Name())
	}
	if len(names) == 0 {
		t.Fatalf("%s 下没有任何非测试 .go 文件 —— 护栏本身失效", dir)
	}
	return names
}

// stripGoLineComments 去掉整行 `//` 注释。只剥「整行注释」而不动行内 `//`：
// 后者可能出现在字符串字面量（如 URL）里，误剥会把真实代码截断。护栏关心的
// 表名/import 都出现在代码或 SQL 字面量里，整行注释豁免已经足够。
func stripGoLineComments(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			b.WriteString("\n")
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}
