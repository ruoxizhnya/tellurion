package repoguard

// CORE-REPOGUARD-HANDLER-WIRED（2026-11-13 五角审查档 3-1）：
// 「handler 已定义、路由未注册」的静态护栏。
//
// 为什么需要它。`package_wiring_test.go` 只守「包没有导入者」，看不见另一种
// 死面：handler 被编译进 `cmd/analysis`，构造函数也写好了，但 `main.go` /
// `registerRoutes` 里从来没有调用点 —— 运行时那族端点完全不可达，而所有
// 测试（单测直接构造 handler）照样绿。stockstate 就是样本：
// `handlers_stock_state.go` 定义了 6 条路由，全仓无 `NewStockStateHandler`
// 调用点（CORE-STOCKSTATE-DISPOSITION ⏸ 等裁决）。
//
// 判据（刻意保守）：
//   - 只扫 `cmd/analysis` 的非测试文件里的 `New*Handler` 构造函数；
//   - 只认「注册函数」里的调用点：函数名为 `main` 或 `register*`；
//   - 调用必须真的接到路由上：同一表达式链里跟着 `.RegisterRoutes(…)`，
//     或者把返回值赋给局部变量后在同一函数里对该变量调 `.RegisterRoutes`；
//   - 例外必须进 `unwiredHandlers`，每条写清理由与重开条件；
//   - 白名单自净：构造函数消失、或已经接线成功，条目必须删掉（否则护栏
//     在替死代码打掩护）。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unwiredHandlerDoc 是白名单条目的两段必填说明。空字段会被判红 ——
// 「写了个名字但没解释」等于没有决策。
type unwiredHandlerDoc struct {
	Reason string // 为什么当前不接线
	Reopen string // 什么条件下必须重新处理这条
}

// unwiredHandlers 是「已定义但未注册」的显式例外。
//
// 2026-11-13 初始清单：1 条。
var unwiredHandlers = map[string]unwiredHandlerDoc{
	"NewStockStateHandler": {
		Reason: "pkg/trading/stockstate 的去留未裁决（CORE-STOCKSTATE-DISPOSITION ⏸ 待主任裁决 A 接线 / B 退役）；" +
			"本条只把「handler 定义了但运行时不可达」记录成机器判据，不替裁决拍板",
		Reopen: "CORE-STOCKSTATE-DISPOSITION 裁决为 A ⇒ 接线进 registerRoutes 并删掉本条目；" +
			"裁决为 B ⇒ 删 handler 与包，本条目同样删除",
	},
}

func TestEveryHandlerConstructorIsRegistered(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")
	dir := filepath.Join(root, "cmd", "analysis")

	constructors, wired, err := scanHandlerWiring(dir)
	require.NoError(t, err)
	require.NotEmpty(t, constructors, "cmd/analysis 下没有扫到任何 New*Handler 构造函数 —— 扫描口径失效，护栏在空转")

	var missing, stale []string
	for name := range constructors {
		if !wired[name] {
			if _, ok := unwiredHandlers[name]; !ok {
				missing = append(missing, name)
			}
		}
	}
	for name, doc := range unwiredHandlers {
		if _, exists := constructors[name]; !exists {
			stale = append(stale, name+"（构造函数已不存在）")
			continue
		}
		if wired[name] {
			stale = append(stale, name+"（已接线）")
		}
		if strings.TrimSpace(doc.Reason) == "" || strings.TrimSpace(doc.Reopen) == "" {
			stale = append(stale, name+"（Reason/Reopen 未写全）")
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)

	assert.Empty(t, missing,
		"这些 handler 构造函数存在，但 main.go/注册函数里没有接到路由上 —— "+
			"要么接线（`.RegisterRoutes(router)`），要么加进 unwiredHandlers 并写清理由与重开条件：%v", missing)
	assert.Empty(t, stale,
		"unwiredHandlers 里这些条目已失效 —— 删掉它们（白名单只该列「当前确实未接线」的 handler）：%v", stale)
}

// scanHandlerWiring 返回 cmd/analysis 下的构造函数集合，以及已接线集合。
func scanHandlerWiring(dir string) (map[string]bool, map[string]bool, error) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}

	constructors := map[string]bool{}
	wired := map[string]bool{}

	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if perr != nil {
			return nil, nil, perr
		}
		files = append(files, f)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			if strings.HasPrefix(fn.Name.Name, "New") && strings.HasSuffix(fn.Name.Name, "Handler") {
				constructors[fn.Name.Name] = true
			}
		}
	}

	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Recv != nil {
				continue
			}
			if fn.Name.Name != "main" && !strings.HasPrefix(fn.Name.Name, "register") {
				continue
			}
			collectWiredHandlers(fn, wired)
		}
	}
	return constructors, wired, nil
}

// collectWiredHandlers 在一个注册函数体内判定哪些构造函数的调用真的接到了
// 路由上。两种被认可的形态：
//
//	NewXHandler(…).RegisterRoutes(router)
//	h := NewXHandler(…); …; h.RegisterRoutes(router)
func collectWiredHandlers(fn *ast.FuncDecl, wired map[string]bool) {
	// 第一遍：收集「赋值给局部变量的构造函数」。
	localCtor := map[string]string{} // 变量名 → 构造函数名
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != len(as.Rhs) {
			return true
		}
		for i, lhs := range as.Lhs {
			id, ok := lhs.(*ast.Ident)
			if !ok {
				continue
			}
			if ctor, ok := ctorCallName(as.Rhs[i]); ok {
				localCtor[id.Name] = ctor
			}
		}
		return true
	})

	// 第二遍：找 `.RegisterRoutes(…)` 的接收者。
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "RegisterRoutes" {
			return true
		}
		if ctor, ok := ctorCallName(sel.X); ok {
			wired[ctor] = true
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok {
			if ctor, ok := localCtor[id.Name]; ok {
				wired[ctor] = true
			}
		}
		return true
	})
}

// ctorCallName 判断表达式是否是对某个 New*Handler 的直接调用，返回函数名。
func ctorCallName(expr ast.Expr) (string, bool) {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return "", false
	}
	id, ok := call.Fun.(*ast.Ident)
	if !ok {
		return "", false
	}
	if strings.HasPrefix(id.Name, "New") && strings.HasSuffix(id.Name, "Handler") {
		return id.Name, true
	}
	return "", false
}
