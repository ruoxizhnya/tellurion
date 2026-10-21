package storage

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// SPEC-R-01：三处「模块内自建表」收编进 migrate() 后的守门测试。
//
// 收编前，audit.message_log / quant.strategy_state / quant.external_signals 的
// 建表文本散落在 pkg/eventstore 与 pkg/strategy 里，与「DDL 唯一真相 =
// migrationStatements()」的声明相矛盾；改一处漏一处不会有人发现。
// 收编后模块侧只剩 storage.*SchemaDDL 的别名，本测试把这条纪律钉死：
//
//  1. migrate() 对三张表的 CREATE TABLE 各恰好一条（不多不少）；
//  2. 仓库内**生产态** Go 文件（_test.go 除外、pkg/storage 自身除外）不再出现
//     任何 CREATE TABLE / CREATE SCHEMA 字面量——新增 DDL 的路径只有
//     pkg/storage/postgres.go。
//
// 刻意零 DB 依赖：静态扫描源码即可，CI 的 go job 不需要起 Postgres。
func TestModuleDDLConsolidatedIntoMigrate(t *testing.T) {
	t.Parallel()

	// ── ① migrate() 对三张表各建一次 ────────────────────────────────
	declared := make(map[string]int)
	tableRE := regexp.MustCompile(`(?i)CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+([a-zA-Z_][a-zA-Z0-9_.]*)`)
	for _, stmt := range migrationStatements() {
		for _, m := range tableRE.FindAllStringSubmatch(stmt, -1) {
			declared[m[1]]++
		}
	}

	wantTables := []string{
		"audit.message_log",
		"quant.strategy_state",
		"quant.external_signals",
	}
	for _, table := range wantTables {
		if got := declared[table]; got != 1 {
			t.Errorf("migrate() 中 %s 的 CREATE TABLE 出现 %d 次, want 1（SPEC-R-01：建表落点唯一）",
				table, got)
		}
	}

	// ── ② pkg/storage 之外的production 代码不得再持有 DDL 文本 ────────
	offenders, err := findForeignDDL("../..")
	if err != nil {
		t.Fatalf("扫描仓库 Go 源码失败: %v", err)
	}
	for _, o := range offenders {
		t.Errorf("%s 出现 CREATE TABLE/SCHEMA 字面量 —— 建表 DDL 的唯一落点是 "+
			"pkg/storage/postgres.go 的 migrationStatements()（SPEC-R-01），"+
			"模块侧只能别名引用 storage.*SchemaDDL", o)
	}
}

// findForeignDDL 遍历 root 下的生产态（非 _test.go）Go 文件，返回其中含
// CREATE TABLE / CREATE SCHEMA 字符串字面量的文件路径。
//
// 只看 AST 里的字符串字面量、不看注释：注释里提到 DDL 是正常文档，不构成
// 「第二份可执行真相」。
func findForeignDDL(root string) ([]string, error) {
	skipDirs := map[string]bool{
		".git":         true,
		"vendor":       true,
		"node_modules": true,
		"web":          true,
		"testdata":     true,
		"pkg/storage":  true, // 唯一合法落点，本包自身不参与扫描
	}

	var offenders []string
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root {
				rel, relErr := filepath.Rel(root, path)
				if relErr != nil {
					return relErr
				}
				if skipDirs[filepath.ToSlash(rel)] {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		f, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		hasDDL := false
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			up := strings.ToUpper(lit.Value)
			if strings.Contains(up, "CREATE TABLE") || strings.Contains(up, "CREATE SCHEMA") {
				hasDDL = true
				return false
			}
			return true
		})
		if hasDDL {
			// 用相对路径报告，便于定位。
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				rel = path
			}
			offenders = append(offenders, filepath.ToSlash(rel))
		}
		return nil
	})
	return offenders, err
}

// TestModuleSchemaDDLComplete 钉住三条 DDL 变量本身完整（schema + 表 + 索引），
// 作为「模块侧别名指向的数组确实完整」的基线。跨包的别名身份断言放在
// pkg/eventstore / pkg/strategy 各自包内（避免本包反向导入模块包）。
func TestModuleSchemaDDLComplete(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		ddl  []string
		want int
	}{
		{"AuditSchemaDDL", AuditSchemaDDL, 4},
		{"StrategyStateSchemaDDL", StrategyStateSchemaDDL, 3},
		{"ExternalSignalsSchemaDDL", ExternalSignalsSchemaDDL, 3},
	}
	for _, c := range cases {
		if len(c.ddl) != c.want {
			t.Errorf("%s 语句数 = %d, want %d（schema/表/索引缺一条就建不起来）",
				c.name, len(c.ddl), c.want)
		}
	}
}
