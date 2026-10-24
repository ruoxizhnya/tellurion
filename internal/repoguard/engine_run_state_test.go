package repoguard

// QT-12 护栏：Engine 不许再长出「一次回测私有的可变态」，装配期种子只许装配路径写。
//
// 缺陷原貌（p0-remediation-design.md A7 + A10）：L1 OHLCV 缓存 / 因子缓存 /
// 财报 / 上市日历 / 日内快照都挂在 Engine 上，而 Engine 是共享对象 ——
// A run 的 warm 覆盖 B run 正在读的那份（A7），缓存还只增不减（A10，慢性 OOM）。
// 后果不是报错，是**结果随并发顺序变**；生产上绕开它的办法是
// 「每个 walk-forward 窗口新建一台 Engine」（cmd/analysis 的注释曾把这件事
// 写成约束），那是躲，不是修。
//
// QT-12 把可变运行态搬进 per-run 的 runContext，Engine 上的同名字段降级为
// **装配期种子**（Set*/Load* 写的是种子，run 开工时各拷一份）。本护栏钉两侧：
//
//	A. Engine 结构体的字段名集合与冻结白名单逐一对应 —— 谁再往 Engine 上
//	   加一个 run 级可变态，当场红。白名单不解释"为什么可以"，加字段的人
//	   必须在 PR 里说清它为什么不是 run 状态。
//	B. 五个种子字段在 pkg/backtest 生产代码里的**写点**只许落在 allowlist 的
//	   函数里（构造函数与注入器）。run 流程里出现写点 = 隔离又被打通了。
//
// 为什么是 AST 而不是 grep：本文件与 run_context.go 的注释里大量出现
// 「引擎级缓存」「e.cache」这类词，文本扫描会在正确状态下报红；而
// `seedCache := e.cache` 这类**读**必须放行（runContext 就是从种子拷的），
// 只有拿得出语法结构的判断才分得清读和写。同一条教训见
// registry_instance_test.go 与 module_lifecycle_callback_test.go。

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// engineGuardDir 是被扫描的目录（相对仓库根）。run 流程全在这儿。
const engineGuardDir = "pkg/backtest"

// engineGuardStructFile 是 Engine 结构体所在文件。
const engineGuardStructFile = "pkg/backtest/engine.go"

// engineSeedFields 是「装配期种子」的字段名 —— 它们的写点受 Leg B 约束。
//
// 每一个都在 QT-12 里被搬进了 runContext；留在 Engine 上的这份只在装配期写。
var engineSeedFields = map[string]bool{
	"cache":        true, // L1 OHLCV
	"factor":       true, // 因子 z-score
	"fundamentals": true, // 财报
	"listing":      true, // 上市日历
	"intraday":     true, // 日内分钟快照
}

// engineSeedWriteAllowlist 是允许写种子的函数名。
//
// 口径：构造函数 + 显式注入器（Set*/Load*）。**没有**任何一个 run 流程函数。
// 想往这里加名字，等于承认「有一个 run 路径会写引擎级状态」——那是本单修的
// 缺陷本身，要走评审而不是往表里塞一行。
var engineSeedWriteAllowlist = map[string]bool{
	"NewEngine":            true, // 构造（含 composite literal 里的初始化）
	"NewEngineWithOptions": true,
	"SetFundamentals":      true, // P2-12 注入门
	"SetListingWindows":    true, // P2-4 注入门
	"SetIntradaySnapshot":  true, // D6 注入门
	"LoadOHLCVInMemory":    true, // 测试/离线 bench 的 L1 注入门
	"LoadFactorCache":      true, // 因子注入门
}

// engineFieldWhitelist 是 Engine 结构体字段的冻结面（Leg A 的比对基准）。
//
// 分三类读（注释不影响判定，只回答「为什么这个字段该在 Engine 上」）：
//   - 装配：构造后不再变的服务依赖；
//   - 种子：QT-12 降级成装配期快照的那五个（见 engineSeedFields）；
//   - 进程级：本就该跨 run 共享的那两份（准入闸、wasm 账源）。
var engineFieldWhitelist = []string{
	// 装配
	"mu", "config", "provider", "riskServiceURL", "httpClient", "logger",
	"stateStore", "riskManager", "dataAdapter", "store", "adjStore",
	"provWriter", "provenanceOff", "liveBridge", "executionBridge", "rng",
	"signalObserver", "signalProvider", "router", "parallelWorkers",
	// QT-26：WASM 制品映射是装配期只读配置（不是 run 私有可变态）。
	"wasmArtifacts",
	// QT-25：分片预算/并发与指标观察面都是装配期只读配置；
	// runContext 只在建 provider 时读它们。
	"wasmShardBudget", "wasmShardObserver",
	// 种子（run 各拷一份）
	"cache", "factor", "fundamentals", "listing", "intraday",
	// 进程级
	"admission", "wasmBytesAccounting",
}

type engineSeedWrite struct {
	file  string
	fun   string
	field string
	shape string
}

// TestEngineFieldsMatchFrozenWhitelist 执行 Leg A：字段面冻结。
func TestEngineFieldsMatchFrozenWhitelist(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")
	got, err := engineStructFieldNames(root, engineGuardStructFile)
	require.NoError(t, err, "读 Engine 结构体失败——护栏判不了就是没判")

	assert.ElementsMatch(t, engineFieldWhitelist, got,
		"QT-12：Engine 的字段面与冻结白名单不一致。新增字段先问一句：它是「一次回测私有的可变态」吗？"+
			"是 → 放进 runContext（pkg/backtest/run_context.go），不是 Engine；不是 → 在这里登记并写明理由。\n"+
			"实际字段：%v", got)
}

// TestEngineSeedWritesStayInAssemblyPath 执行 Leg B：种子写点只许装配路径。
func TestEngineSeedWritesStayInAssemblyPath(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")
	writes, scanned := scanEngineSeedWrites(root)

	unexpected := make([]engineSeedWrite, 0, len(writes))
	for _, w := range writes {
		if !engineSeedWriteAllowlist[w.fun] {
			unexpected = append(unexpected, w)
		}
	}
	sort.Slice(unexpected, func(i, j int) bool {
		return unexpected[i].file+unexpected[i].fun < unexpected[j].file+unexpected[j].fun
	})

	assert.Empty(t, unexpected,
		"QT-12：run 流程写了引擎级种子（扫描 %d 个生产文件）—— 那是 A7 的串扰原形："+
			"一个 run 的 warm 覆盖另一个 run 正在读的缓存。写给本 run 的 runContext，别写给 Engine。\n%s",
		len(scanned), renderEngineSeedWrites(unexpected))
}

// TestEngineSeedWriteGuardSeesTheAssemblyWrites 反盲区：白名单里那些写点必须
// 真被扫到。永远绿的原因有两种，「今天没有违规」和「扫描器什么也没看见」，
// 只有后者能被这条区分出来。
func TestEngineSeedWriteGuardSeesTheAssemblyWrites(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")
	writes, scanned := scanEngineSeedWrites(root)

	require.NotEmpty(t, writes, "一个种子写点都没扫到：Engine 结构体改名了，还是扫描器瞎了？")

	byFunc := map[string]int{}
	for _, w := range writes {
		byFunc[w.fun]++
	}
	backtestFiles := 0
	for _, f := range scanned {
		if strings.HasPrefix(f, engineGuardDir+"/") {
			backtestFiles++
		}
	}
	assert.GreaterOrEqual(t, backtestFiles, 20,
		"pkg/backtest 生产文件只扫到 %d 个，明显低于实际规模——扫描面坏了", backtestFiles)

	// 五个种子各自的装配写点都要在（少一个是漏判的形状）。
	for _, want := range []string{"SetFundamentals", "SetListingWindows", "SetIntradaySnapshot", "LoadOHLCVInMemory", "LoadFactorCache"} {
		assert.Positive(t, byFunc[want], "白名单函数 %s 的写点没被扫到——扫描器漏了这种形状", want)
	}
}

// TestEngineSeedWriteGuardJudgesSnippets 自证腿：把「该判成写」与「该放行」
// 的形状逐条喂给判定函数。判定只吃 AST，所以给它一段代码就等于给它一个文件。
func TestEngineSeedWriteGuardJudgesSnippets(t *testing.T) {
	t.Parallel()

	cases := []struct {
		leg   string // "write" = 必须判成写点；"ok" = 必须不判
		src   string
		field string // 期望命中的种子字段（仅 write 腿看）
		fun   string // 期望归属的函数名（仅 write 腿看）
	}{
		{"write", "func (e *Engine) WarmHere() { e.fundamentals = nil }", "fundamentals", "WarmHere"},
		{"write", "func (e *Engine) WarmHere() { e.cache.Load(data) }", "cache", "WarmHere"},
		{"write", "func (e *Engine) WarmHere() { e.factor.Warm(ctx, s, t, st) }", "factor", "WarmHere"},
		{"write", "func (e *Engine) WarmHere() { e.intraday[\"A\"] = bars }", "intraday", "WarmHere"},
		{"write", "func (e *Engine) WarmHere() { delete(e.listing, \"A\") }", "listing", "WarmHere"},
		{"write", "func (e *Engine) WarmHere() { e.cache = newCache() }", "cache", "WarmHere"},
		// 经 runContext 持有的引擎指针写回去：与上面那条是同一个缺陷。
		{"write", "func (rc *runContext) Warm() { rc.e.fundamentals = records }", "fundamentals", "Warm"},
		{"write", "func (rc *runContext) Warm() { rc.e.cache.Load(data) }", "cache", "Warm"},

		// 读放行：runContext 从种子拷一份、访问器返回引用，都不是写。
		{"ok", "func (e *Engine) NewRun() { seedCache := e.cache; _ = seedCache }", "", ""},
		{"ok", "func (e *Engine) NewRun() { rc := &runContext{e: e}; rc.factor = seedFactor.Copy(); _ = rc }", "", ""},
		{"ok", "func (e *Engine) Read() { return e.factor.Get(f, d, s) }", "", ""},
		{"ok", "func (e *Engine) Read() { return e.listing }", "", ""},
		{"ok", "func (rc *runContext) Read() { return rc.e.intraday }", "", ""},
		// run 侧写自己的 runContext 合法（QT-12 的正解形态）。
		{"ok", "func (rc *runContext) Warm() { rc.fundamentals = records }", "", ""},
		{"ok", "func (rc *runContext) Warm() { rc.cache.Load(data) }", "", ""},
		// 别的接收者的同名字段不在射程内（Engine 的种子是私有的）。
		{"ok", "func (s *State) Touch() { s.cache = nil }", "", ""},
		// 注释里出现判定词不算命中（这就是选 AST 的理由）。
		{"ok", "func (e *Engine) Read() { /* e.cache.Load(...) 以前在这里发生 */ }", "", ""},
	}

	for _, tc := range cases {
		src := "package backtest\n\nimport \"context\"\n\nvar _ = context.Background\n\n" + tc.src + "\n"
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "snippet.go", src, 0)
		require.NoError(t, err, "夹具解析失败：%s", tc.src)

		hits := engineSeedWritesInFile(fset, file, "snippet.go")
		switch tc.leg {
		case "write":
			require.Len(t, hits, 1, "该判成引擎级写点却判成 %d 条：%s", len(hits), tc.src)
			assert.Equal(t, tc.field, hits[0].field, "写点字段名判错：%s", tc.src)
			assert.Equal(t, tc.fun, hits[0].fun, "写点归属函数判错：%s", tc.src)
		case "ok":
			assert.Empty(t, hits, "不该判成写点却报了：%s → %s", tc.src, renderEngineSeedWrites(hits))
		}
	}
}

// ─── 扫描实现 ──────────────────────────────────────────────────────────

// engineStructFieldNames 解析出 `type Engine struct` 的字段名列表。
func engineStructFieldNames(root, rel string) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(rel)), nil, 0)
	if err != nil {
		return nil, err
	}
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "Engine" {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			var names []string
			for _, fld := range st.Fields.List {
				if len(fld.Names) == 0 {
					names = append(names, "(embedded)")
					continue
				}
				for _, nm := range fld.Names {
					names = append(names, nm.Name)
				}
			}
			if len(names) == 0 {
				return nil, fmt.Errorf("%s 里 Engine 结构体没有字段——解析走到了意料外的形状", rel)
			}
			return names, nil
		}
	}
	return nil, fmt.Errorf("%s 里找不到 type Engine struct", rel)
}

// scanEngineSeedWrites 递归扫 pkg/backtest 的生产文件（跳过 _test.go），
// 收集「对 Engine 种子的写」。返回命中与扫过的文件（覆盖面自证用）。
func scanEngineSeedWrites(root string) (writes []engineSeedWrite, scanned []string) {
	dir := filepath.Join(root, filepath.FromSlash(engineGuardDir))
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)

		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			writes = append(writes, engineSeedWrite{file: rel, fun: "<parse-error: " + parseErr.Error() + ">"})
			return nil
		}
		writes = append(writes, engineSeedWritesInFile(fset, file, rel)...)
		scanned = append(scanned, rel)
		return nil
	})
	return writes, scanned
}

// engineSeedWritesInFile 在一个文件里找 Engine 接收者方法对种子的写。
//
// 判定不需要类型信息：接收者是 *Engine（名字近似即可，这里取结构上的
// `func (e *Engine)` 形状）且选择器落在 engineSeedFields 上。
func engineSeedWritesInFile(fset *token.FileSet, file *ast.File, rel string) []engineSeedWrite {
	var out []engineSeedWrite

	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		// 形状①要求 Engine 接收者；形状②（`rc.e.<seed>`）在**任何**函数里都
		// 是写回引擎，所以这里不按接收者过滤 —— 按函数名放行已经够了。
		recvName := ""
		if fd.Recv != nil && len(fd.Recv.List) > 0 {
			if n, t := receiverShape(fd.Recv.List[0]); t == "Engine" {
				recvName = n
			}
		}

		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch stmt := n.(type) {
			case *ast.AssignStmt:
				for _, lhs := range stmt.Lhs {
					if field, ok := seedSelectorTarget(lhs, recvName); ok {
						out = append(out, engineSeedWrite{
							file: rel, fun: fd.Name.Name, field: field,
							shape: renderNode(fset, lhs),
						})
					}
				}
			case *ast.CallExpr:
				// e.<seed>.Load(...) / e.<seed>.Warm(...) —— 经引用改内容，
				// 与直接赋值等价（这正是 CacheManager 的「整体替换」语义）。
				if sel, ok := stmt.Fun.(*ast.SelectorExpr); ok && isMutatorName(sel.Sel.Name) {
					if field, ok := seedSelectorTarget(sel.X, recvName); ok {
						out = append(out, engineSeedWrite{
							file: rel, fun: fd.Name.Name, field: field,
							shape: renderNode(fset, stmt),
						})
						return true
					}
				}
				// delete(e.<seed>, k)
				if id, ok := stmt.Fun.(*ast.Ident); ok && id.Name == "delete" && len(stmt.Args) > 0 {
					if field, ok := seedSelectorTarget(stmt.Args[0], recvName); ok {
						out = append(out, engineSeedWrite{
							file: rel, fun: fd.Name.Name, field: field,
							shape: renderNode(fset, stmt),
						})
					}
				}
			}
			return true
		})
	}
	return out
}

// isMutatorName 是「会改这份缓存内容」的方法名集合。
//
// 只认 Load / Warm 这两个真实存在的写方法，不写成"任何方法调用都算"：
// Get / Peek / Bytes / Snapshot 是读，把它们算进射程会让白名单变成
// 一长串「其实只是读」的例外，那样的护栏没人维护得住。
func isMutatorName(name string) bool {
	return name == "Load" || name == "Warm"
}

// seedSelectorTarget 判断 expr 是不是指向 Engine 种子的写目标。
//
// 认两种形状，因为它们都能真的改掉引擎级那份数据：
//
//	① `e.<seed>`        —— Engine 自己的方法（注入器走函数名白名单放行）；
//	② `< anything >.e.<seed>` —— 经由结构体字段 `e`（runContext 持有的那台
//	   Engine）写回去。这条是刻意补的：run 流程拿不到 Engine 接收者，
//	   但它手上就有 rc.e，`rc.e.fundamentals = records` 与在 Engine 方法里
//	   赋值是同一个缺陷的两种写法，只认①的护栏会漏掉真正会发生的那种。
//
// `rc.fundamentals = …`（runContext 自己的字段）不在射程内 —— 那正是 QT-12
// 要的正解形态。
func seedSelectorTarget(expr ast.Expr, recvName string) (string, bool) {
	switch x := expr.(type) {
	case *ast.IndexExpr:
		return seedSelectorTarget(x.X, recvName)
	case *ast.StarExpr:
		return seedSelectorTarget(x.X, recvName)
	case *ast.SelectorExpr:
		field := x.Sel.Name
		if !engineSeedFields[field] {
			return "", false
		}
		switch inner := x.X.(type) {
		case *ast.Ident:
			// ①：接收者标识符必须就是本方法的 Engine 接收者。
			if inner.Name == recvName {
				return field, true
			}
			return "", false
		case *ast.SelectorExpr:
			// ②：`y.e.<seed>`，且 y 是个变量/字段链（不再往下要求类型信息）。
			if inner.Sel != nil && inner.Sel.Name == "e" {
				return field, true
			}
			return "", false
		}
		return "", false
	}
	return "", false
}

// receiverShape 渲染 `func (e *Engine)` 的接收者名与类型名。
//
// 无名接收者（`func (*Engine)`）返回空名字：那种写法拿不到标识符，判定
// 不了 `e.cache`，宁可让它落在扫描面外并由反盲区那条（白名单写点必须
// 扫到）暴露，也不要静默少扫一片。
func receiverShape(f *ast.Field) (name, typeName string) {
	star, ok := f.Type.(*ast.StarExpr)
	if !ok {
		return "", "" // 值接收者：不改字段，也不在本护栏射程内
	}
	id, ok := star.X.(*ast.Ident)
	if !ok {
		return "", ""
	}
	if len(f.Names) == 0 {
		return "", id.Name
	}
	return f.Names[0].Name, id.Name
}

func renderNode(fset *token.FileSet, n ast.Node) string {
	var buf bytes.Buffer
	_ = printer.Fprint(&buf, fset, n)
	return buf.String()
}

func renderEngineSeedWrites(ws []engineSeedWrite) string {
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		out = append(out, fmt.Sprintf("  - %s: %s 写 %s（%s）", w.file, w.fun, w.field, w.shape))
	}
	return strings.Join(out, "\n")
}
