package repoguard

// SVC-CORE-AI-RETIRE 反向护栏：AI 实验域的七张遗留表退役是**不可逆**的。
//
// 本护栏钉三件事：
//
//  1. `pkg/storage/postgres.go` 的 migrate() 内联 DDL 里，experiments /
//     factor_hypothesis / factor_genes / strategy_genes / research.profile /
//     research.conclusion / research.question **零** `CREATE TABLE`。
//     这七张表的唯一 owner 是 `tellurion-agent` 的 `ai.*` schema；本仓一旦
//     重新建表，就又是双 owner（ADR-036 §6 / ADR-0003）。
//  2. `pkg/storage/{experiments,factor_hypothesis,research}.go` 三个文件不存在。
//     它们是这七张表的读写实现；随 DDL 一起退役，回归即重新撕开双写口。
//  3. 生产 Go 代码（`_test.go` 除外）对七张表零 INSERT / UPDATE / DELETE。
//     静态文本检查在 CI 无库环境同样有效；不依赖连库。
//
// 正向对照（防止护栏因「postgres.go 被删空」而假绿）：同一份 DDL 里必须仍能
// 看到 `factor_definitions` 的 CREATE TABLE —— 公式化因子引擎的定义库仍归本仓
// （Migration 051），不在本单退役范围内。若哪天 postgres.go 被整块删空，这条先红。
//
// 破坏验证（2026-11-10 施工时实做）：往 postgres.go 的 migrations 里加回一行
// `CREATE TABLE IF NOT EXISTS experiments (...)` ⇒ 本护栏红；逐字节还原后复绿。
// 同型验证文件断言：临时建空文件 `pkg/storage/experiments.go` ⇒ 红，删除后复绿。

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// retiredAITables 是 SVC-CORE-AI-RETIRE 退役的七张表（材料 §4.7）。
var retiredAITables = []string{
	"experiments",
	"factor_hypothesis",
	"factor_genes",
	"strategy_genes",
	"research.profile",
	"research.conclusion",
	"research.question",
}

// TestRetiredAITablesHaveNoInlineDDL 断言七张退役表在 migrate() 的内联 DDL
// 里零 CREATE TABLE，且保留表 factor_definitions 仍在。
func TestRetiredAITablesHaveNoInlineDDL(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	src := stripSQLLineComments(readFile(t, filepath.Join(root, "pkg", "storage", "postgres.go")))

	for _, table := range retiredAITables {
		pattern := regexp.MustCompile(
			`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?` + regexp.QuoteMeta(table) + `\b`)
		if loc := pattern.FindStringIndex(src); loc != nil {
			line := 1 + strings.Count(src[:loc[0]], "\n")
			t.Errorf("pkg/storage/postgres.go:%d 重新建了已退役的表 %s —— "+
				"AI 实验域唯一 owner 是 tellurion-agent 的 ai.*；core 不得再持有该表 DDL（SVC-CORE-AI-RETIRE）",
				line, table)
		}
	}

	// 正向对照：保留表必须还在，否则本护栏可能因「DDL 被整块删空」而假绿。
	kept := regexp.MustCompile(`(?i)CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+factor_definitions\b`)
	if !kept.MatchString(src) {
		t.Error("pkg/storage/postgres.go 里找不到 factor_definitions 的 CREATE TABLE —— " +
			"公式化因子引擎的定义库仍归本仓（Migration 051），必须保留；若确已改判，请同批订正本护栏")
	}
}

// TestAIStorageLegacyFilesAreGone 断言 pkg/storage 下三份遗留实现文件不存在。
func TestAIStorageLegacyFilesAreGone(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	for _, name := range []string{"experiments.go", "factor_hypothesis.go", "research.go"} {
		abs := filepath.Join(root, "pkg", "storage", name)
		if _, err := os.Stat(abs); err == nil {
			t.Errorf("pkg/storage/%s 仍存在 —— 该文件的表已随 SVC-CORE-AI-RETIRE 交给 ai.*；"+
				"读写实现回归即重新撕开双写口", name)
		} else if !os.IsNotExist(err) {
			t.Errorf("无法判定 pkg/storage/%s 是否存在：%v", name, err)
		}
	}
}

// TestNoProductionWritesToRetiredAITables 扫描全仓生产 Go 代码，断言七张退役表
// 没有任何写入语句。测试文件不在扫描范围（它们只做只读对拍或已被删除）。
func TestNoProductionWritesToRetiredAITables(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	tableAlt := `(?:experiments|factor_hypothesis|factor_genes|strategy_genes|research\.(?:profile|conclusion|question))`
	writeRe := regexp.MustCompile(`(?i)\b(?:INSERT\s+INTO|UPDATE|DELETE\s+FROM)\s+(?:public\.)?` + tableAlt + `\b`)

	scanned := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", "docs", "testdata", "migrations", "archive":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		scanned++
		src := stripGoLineComments(readFile(t, path))
		if loc := writeRe.FindStringIndex(src); loc != nil {
			line := 1 + strings.Count(src[:loc[0]], "\n")
			rel, _ := filepath.Rel(root, path)
			t.Errorf("%s:%d 仍对已退役的 AI 表发写语句（%s）—— 写者只能是 tellurion-agent 的 aistore",
				rel, line, writeRe.FindString(src))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历仓库失败: %v", err)
	}
	// 正向对照：扫描本身必须真的扫到了文件，否则零命中毫无意义。
	if scanned < 100 {
		t.Fatalf("只扫到 %d 个生产 Go 文件 —— 遍历路径失效，护栏假绿，检查 repoRoot 返回值", scanned)
	}
}
