package repoguard

// QT-05 B1（2026-10-23）契约身份护栏。domain / domain/market / engine / fees
// 四包的本体已整包迁到 `tellurion-contracts`，本仓经版本依赖取用。迁走之后有
// 三类复燃编译器**看不见**，本护栏钉的就是这三条：
//
//	腿 A（包不许重建）：有人「先拷回来应急」，把 pkg/domain / pkg/fees /
//	  pkg/backtest/contracts 重新建出来 —— 新建包完全合法，go build 不红，仓里
//	  立刻有两份同名类型各自演化（交接单警告的「双份真相窗口」就是它）。同一
//	  条里顺带核 import：旧路径再被 import 时，报错要说清「去契约仓改」。
//	腿 B（门面不许变成第二份定义）：pkg/backtest/aliases.go 是 core 的门面，
//	  与契约同名的类型全靠 `type X = contracts.X` 这层零成本别名。若有人为了
//	  「消掉一个编译错误」把别名改成本地 struct，调用点仍然写
//	  backtest.BacktestResponse，而序列化出去的已是另一份形状 —— 编译器同样不红。
//	  判据：门面里每一个与契约同名的声明都必须是别名，且别名右侧的包必须真是
//	  本仓依赖的契约包、那个名字必须真在那边导出（名单现场解析，不手抄）。
//	腿 C（不许新增撞名的第二定义）：core 里另有 4 个与契约同名但**刻意分型**的
//	  本地类型（逐条理由见 shadowContractNameBaseline），是 B1 之前就存在的既成
//	  事实；这条腿守的是「第 5 个不许悄悄长出来」。非别名、又不在基线里 = 红；
//	  基线条目不再命中 = 反向棘轮逼删（白名单不许替已消失的违规打掩护）。
//
// 三条都 fail-closed：定位不到契约模块、解析走空，一律红，不把「没核成」伪装
// 成「核过了」。

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// contractsModulePath 是本护栏的核保对象，写死是故意的：契约仓改名属跨仓决策。
const contractsModulePath = "github.com/ruoxizhnya/tellurion-contracts"

// minContractTypeNames 是解析正证据的下限。2026-10-23 实测四包共 97 个导出类型
// 名：留到 90 容得下 bump，容不下走空（名单为空会让腿 B/C 恒绿）。
const minContractTypeNames = 90

// minFacadeAliases 是门面别名腿的正证据下限。2026-10-23 实测 7 条
// （TradingConfig / PriceLimitConfig / BacktestRequest / BacktestResponse /
// InvalidReason / FillTiming / EngineRunner）。
const minFacadeAliases = 5

// facadeFile 是 core 侧的门面（相对仓根），唯一被允许别名再导出契约类型的地方。
const facadeFile = "pkg/backtest/aliases.go"

var (
	// legacyContractDirs 是已迁走的包目录（相对仓根）。
	legacyContractDirs = []string{"pkg/domain", "pkg/domain/market", "pkg/fees", "pkg/backtest/contracts"}

	// legacyImportPaths 是与之对应的旧 import 路径。
	legacyImportPaths = []string{
		"tellurion/pkg/domain",
		"tellurion/pkg/domain/market",
		"tellurion/pkg/fees",
		"tellurion/pkg/backtest/contracts",
	}

	// contractPackages 是契约仓里本仓取用的四个目录。注意 engine/ 的 package 名
	// 仍叫 contracts（目录名≠包名，契约仓 ADR-0002 的裁决），所以别名检查按
	// **package 子句读出来的标识符**核，不拿目录名猜。
	contractPackages = []string{"domain", "domain/market", "engine", "fees"}
)

// shadowContractNameBaseline 登记 core 里与契约同名、但**不是同一份形状**的本地
// 类型（非别名声明）。key 形如 "<相对路径>:<类型名>"，每条写清撞的是什么、以及
// 收敛归哪条单。
//
// 这 4 条都是 B1 之前就存在的既成事实（迁移只是把「core 的 pkg/domain vs core
// 的本地包」换成「契约包 vs core 的本地包」），本基线守的是不要再长第 5 条。
// 2026-10-23 逐条实测过形状（见 docs/TASKS.md QT-05 core 腿结案段的缺口登记）。
var shadowContractNameBaseline = map[string]string{
	// 实测：core 这 5 个方法名与契约 market.Provider 逐字相同，另多 5 个
	// （Name / CheckConnectivity / GetStock / GetTradingDays / BulkLoadOHLCV）
	// ⇒ 超集关系，且两者之间没有编译期断言钉住。收敛属数据面（X1 / QT-40）。
	"pkg/marketdata/provider.go:Provider":   "取数 Provider 接口，是契约 market.Provider 的**超集**（同名 5 方法逐字相同 + 另 5 个）；今天靠实现体同时满足两边，没有 var _ market.Provider = ... 这类断言 —— 已登记缺口，不在本护栏裁",
	"pkg/portfolio/interfaces.go:Portfolio": "组合域自己的接口（ApplyFill / Value / Positions / Snapshot）；契约 domain.Portfolio 是可变的内存持仓载体（struct），两者关系已在该文件注释里写清",
	"pkg/risk/manager.go:RiskManager":       "契约 domain.RiskManager 那个 port 的**实现体**（struct）；实现者与被实现的接口同名是刻意的（同步裁决留 core 进程内）",
	// 实测 45 个文件在用；engine.go:1504 convertStrategySignals 是唯一的映射腿。
	"pkg/strategy/strategy.go:Signal": "策略插件的产出形状（Action string / Price / Date interface{}），在 pkg/backtest/engine.go 的 convertStrategySignals 显式映射成契约 domain.Signal（Direction / CompositeScore / Factors）；要不要收敛属策略面的单，不由本护栏裁",
}

// TestLegacyContractPackagesNotRebuilt 腿 A：旧契约包不得在本仓重建，也不得再
// 被 import。
func TestLegacyContractPackagesNotRebuilt(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..")

	var rebuilt []string
	for _, dir := range legacyContractDirs {
		abs := filepath.Join(root, filepath.FromSlash(dir))
		if st, err := os.Stat(abs); err == nil && st.IsDir() {
			rebuilt = append(rebuilt, dir+"/")
		}
	}
	assert.Empty(t, rebuilt,
		"这些已整包迁到 %s 的契约包在本仓被重建了。形状的唯一真相在契约仓：要改就"+
			"去那边改、走它的兼容矩阵与发版流程；本仓只经版本依赖取用，不留第二份：%v",
		contractsModulePath, rebuilt)

	bad := filesImporting(t, root, legacyImportPaths)
	assert.Empty(t, bad,
		"这些文件还在 import 已迁走的旧契约路径。换成 %s 下对应包（domain /"+
			" domain/market / engine / fees；engine 的 package 名仍是 contracts，"+
			"所以调用点 contracts.X 前缀不用改）：%v", contractsModulePath, bad)
}

// TestFacadeReexportsStayAliasesIntoContracts 腿 B：门面对契约类型的再导出必须
// 是指向契约包的别名。
func TestFacadeReexportsStayAliasesIntoContracts(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..")

	names := contractExportedTypeNames(t, root)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(facadeFile)), nil, 0)
	require.NoError(t, err,
		"门面文件 %s 读不到 / 解析失败 —— 它是 core 的门面，必须留在本仓（QT-05 交接单 A-4）", facadeFile)

	// import 标识符 -> 导入路径（无别名时标识符 = 契约包 package 名，下面按
	// package 子句核，不拿目录名猜）。
	pathOfIdent := map[string]string{}
	for _, spec := range file.Imports {
		path := strings.Trim(spec.Path.Value, `"`)
		ident := ""
		if spec.Name != nil { // 显式别名才有；裸 import 时 spec.Name 是 nil
			ident = spec.Name.Name
		}
		if ident == "" {
			ident = packageIdentOfImportPath(t, root, path)
		}
		pathOfIdent[ident] = path
	}

	var offenders []string
	aliased := 0
	for _, ts := range typeSpecs(file) {
		if !names[ts.Name.Name] {
			continue // 不是契约类型名，门面里的本地类型不归本条腿管
		}
		where := facadeFile + ":" + strconv.Itoa(fset.Position(ts.Pos()).Line)
		if ts.Assign.IsValid() {
			// 别名：右侧必须是 <契约包标识符>.<契约导出的名字>。
			if path, ok := aliasTargetContractPackage(ts, pathOfIdent); !ok {
				offenders = append(offenders, where+" type "+ts.Name.Name+
					" = 指向的不是契约模块（改成再导出 "+contractsModulePath+"/… 的那个类型）")
				continue
			} else if !contractTypesAt(t, root, path)[ts.Name.Name] {
				offenders = append(offenders, where+" type "+ts.Name.Name+
					" = "+path+" 里并没有导出这个名字")
				continue
			}
			aliased++
			continue
		}
		offenders = append(offenders, where+" type "+ts.Name.Name+
			" 是本地**定义**而不是别名 —— 门面变成第二份形状，调用点仍写 backtest.X 而序列化已换了一份")
	}
	assert.Empty(t, offenders,
		"门面 %s 对契约类型的再导出坏了：\n%s", facadeFile, strings.Join(offenders, "\n"))
	assert.GreaterOrEqual(t, aliased, minFacadeAliases,
		"门面对契约类型只剩 %d 条合法别名（下限 %d）—— 别名被删光了这条腿就会假绿，"+
			"门面是 core 的 API 面，别缩", aliased, minFacadeAliases)
}

// TestNoUnlistedShadowContractTypeDeclarations 腿 C：core 里不得新增与契约同名
// 的本地定义；既成的 4 条按基线登记。
func TestNoUnlistedShadowContractTypeDeclarations(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..")

	names := contractExportedTypeNames(t, root)

	hits := map[string]bool{}
	var unlisted []string
	forEachGoFile(t, root, func(rel string, file *ast.File, fset *token.FileSet) {
		for _, ts := range typeSpecs(file) {
			if !names[ts.Name.Name] {
				continue
			}
			if ts.Assign.IsValid() {
				continue // 别名不是第二份定义（腿 B 专门核门面的别名）
			}
			key := rel + ":" + ts.Name.Name
			hits[key] = true
			if _, ok := shadowContractNameBaseline[key]; !ok {
				unlisted = append(unlisted, key+"（行 "+
					strconv.Itoa(fset.Position(ts.Pos()).Line)+"）")
			}
		}
	})
	sort.Strings(unlisted)
	assert.Empty(t, unlisted,
		"这些本仓类型与 %s 导出的契约类型**同名且各自定义**，又没登记理由。同名两份类型"+
			"= 两份真相（JSON tag / DB 口径会各自演化）。要么直接用契约里那个，要么改名，"+
			"确有理由留在本仓的按 shadowContractNameBaseline 的格式登记并写清撞的是什么：%v",
		contractsModulePath, unlisted)

	var stale []string
	for key, reason := range shadowContractNameBaseline {
		if !hits[key] {
			stale = append(stale, key+"（登记理由："+reason+"）")
		}
	}
	sort.Strings(stale)
	assert.Empty(t, stale,
		"shadowContractNameBaseline 里这些声明已不在仓里 —— 删掉条目，白名单不许替已消失的"+
			"撞名打掩护：\n%s", strings.Join(stale, "\n"))
}

// contractTypesByPackageIdent 返回「import 标识符（= 契约包的 package 名）->
// 该包导出的类型名集合」。
func contractTypesByPackageIdent(t *testing.T, root string) map[string]map[string]bool {
	t.Helper()
	dir := contractModuleDir(t, goModContractVersion(t, root))
	out := map[string]map[string]bool{}
	for _, pkg := range contractPackages {
		pkgDir := filepath.Join(dir, filepath.FromSlash(pkg))
		ident, types := packageIdentAndExportedTypes(t, pkgDir, pkg)
		out[ident] = types
	}
	return out
}

// contractExportedTypeNames 返回四包导出类型名的并集。
func contractExportedTypeNames(t *testing.T, root string) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	for _, set := range contractTypesByPackageIdent(t, root) {
		for n := range set {
			names[n] = true
		}
	}
	require.GreaterOrEqual(t, len(names), minContractTypeNames,
		"从契约模块只解析到 %d 个导出类型名（下限 %d）—— 解析走空会让本条腿恒绿",
		len(names), minContractTypeNames)
	return names
}

// contractTypesAt 取某个契约导入路径导出的类型名集合。
func contractTypesAt(t *testing.T, root, importPath string) map[string]bool {
	t.Helper()
	if !strings.HasPrefix(importPath, contractsModulePath+"/") {
		return nil
	}
	dir := contractModuleDir(t, goModContractVersion(t, root))
	pkg := strings.TrimPrefix(importPath, contractsModulePath+"/")
	for _, known := range contractPackages {
		if known == pkg {
			_, types := packageIdentAndExportedTypes(t, filepath.Join(dir, filepath.FromSlash(pkg)), pkg)
			return types
		}
	}
	return nil
}

// aliasTargetContractPackage 判断别名右侧是否形如 <ident>.<Name>，且该标识符
// 对应一个契约模块里的包。
func aliasTargetContractPackage(ts *ast.TypeSpec, pathOfIdent map[string]string) (string, bool) {
	ae, ok := ts.Type.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	ident, ok := ae.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	path, ok := pathOfIdent[ident.Name]
	if !ok || !strings.HasPrefix(path, contractsModulePath+"/") {
		return "", false
	}
	return path, true
}

// packageIdentAndExportedTypes 解析一个契约包目录：package 名（标识符）+ 导出
// 类型名集合（含类型别名 —— 别名同样占住这个名字）。
func packageIdentAndExportedTypes(t *testing.T, pkgDir, pkg string) (string, map[string]bool) {
	t.Helper()
	entries, err := os.ReadDir(pkgDir)
	require.NoError(t, err,
		"契约模块 %s 下读不到 %s/ —— 这是定位失败，不等于「那边没有类型」", pkgDir, pkg)

	ident := ""
	types := map[string]bool{}
	parsed := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(token.NewFileSet(), filepath.Join(pkgDir, e.Name()), nil, 0)
		require.NoError(t, perr, "解析契约源文件 %s/%s 失败", pkg, e.Name())
		parsed++
		if ident == "" {
			ident = file.Name.Name
		} else if file.Name.Name != ident {
			t.Fatalf("契约包 %s/ 里出现两个 package 名（%s vs %s）", pkg, ident, file.Name.Name)
		}
		for _, ts := range typeSpecs(file) {
			if ts.Name.IsExported() {
				types[ts.Name.Name] = true
			}
		}
	}
	require.NotEmpty(t, ident, "契约包 %s/ 解析不到 package 名", pkg)
	require.Positive(t, parsed, "契约包 %s/ 里一个非测试 .go 都没有，解析走空", pkg)
	require.NotEmpty(t, types, "契约包 %s/ 一个导出类型都没有，解析走空", pkg)
	return ident, types
}

// packageIdentOfImportPath 取本仓依赖的某个包在没有导入别名时的标识符：契约
// 模块从缓存目录的 package 子句读，其余（本仓自己的包）取路径末段。
func packageIdentOfImportPath(t *testing.T, root, importPath string) string {
	t.Helper()
	if strings.HasPrefix(importPath, contractsModulePath+"/") {
		dir := contractModuleDir(t, goModContractVersion(t, root))
		pkg := strings.TrimPrefix(importPath, contractsModulePath+"/")
		ident, _ := packageIdentAndExportedTypes(t, filepath.Join(dir, filepath.FromSlash(pkg)), pkg)
		return ident
	}
	return filepath.Base(importPath)
}

// goModContractVersion 取 go.mod 里对契约模块的 require 版本。缺这一行本身就是
// 回归：那意味着契约身份又回到了本仓。
func goModContractVersion(t *testing.T, root string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "go.mod"))
	require.NoError(t, err, "读不到 go.mod —— 本护栏预设测试在仓库内运行")

	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == contractsModulePath {
			return fields[1]
		}
	}
	require.FailNowf(t, "go.mod 没有契约模块的 require",
		"本仓应经版本依赖取用 %s；这一行没了，等于契约又搬回本仓各自一份", contractsModulePath)
	return ""
}

// contractModuleDir 定位模块缓存里的契约源码：优先 $GOMODCACHE，退回
// $GOPATH/pkg/mod（与 go/build 的默认口径一致）。
//
// 契约 module path 全小写，缓存目录名因此不需要 `!大写字母` 转义；契约仓将来
// 若改名带上大写，这里要补 go 的 module 转义规则。
func contractModuleDir(t *testing.T, version string) string {
	t.Helper()

	var cacheRoots []string
	if env := os.Getenv("GOMODCACHE"); env != "" {
		cacheRoots = append(cacheRoots, env)
	}
	for _, gp := range filepath.SplitList(build.Default.GOPATH) {
		if gp != "" {
			cacheRoots = append(cacheRoots, filepath.Join(gp, "pkg", "mod"))
		}
	}
	for _, r := range cacheRoots {
		candidate := filepath.Join(r, filepath.FromSlash(contractsModulePath)+"@"+version)
		if st, err := os.Stat(candidate); err == nil && st.IsDir() {
			return candidate
		}
	}
	require.FailNowf(t, "定位不到契约模块缓存",
		"GOMODCACHE=%q 与 GOPATH=%q 下都没有 %s@%s —— 宁可红，不假装核过",
		os.Getenv("GOMODCACHE"), build.Default.GOPATH, contractsModulePath, version)
	return ""
}

// typeSpecs 取一个文件里所有顶层 `type` 声明（含 `type (...)` 块与别名）。
func typeSpecs(file *ast.File) []*ast.TypeSpec {
	var out []*ast.TypeSpec
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			if ts, ok := spec.(*ast.TypeSpec); ok {
				out = append(out, ts)
			}
		}
	}
	return out
}

// filesImporting 扫本仓全部 .go（含测试：旧路径出现在测试里同样是复燃），返回
// "<相对路径>:<行号> -> <import path>" 清单。
func filesImporting(t *testing.T, root string, banned []string) []string {
	t.Helper()
	set := make(map[string]bool, len(banned))
	for _, p := range banned {
		set[p] = true
	}

	var hits []string
	forEachGoFile(t, root, func(rel string, file *ast.File, fset *token.FileSet) {
		for _, spec := range file.Imports {
			path := strings.Trim(spec.Path.Value, `"`)
			if set[path] {
				hits = append(hits, rel+":"+strconv.Itoa(fset.Position(spec.Pos()).Line)+" -> "+path)
			}
		}
	})
	sort.Strings(hits)
	return hits
}

// forEachGoFile 遍历仓库内的 .go 文件，回调（相对路径, AST, 共享 FileSet）。
// 目录跳过口径与同目录其它护栏一致（.git / 前端残留 / 测试数据 / vendor）。
// 解析失败当场红：扫不动就等于没扫，静默略过会把护栏变成假绿。
func forEachGoFile(t *testing.T, root string, visit func(rel string, file *ast.File, fset *token.FileSet)) {
	t.Helper()
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".workbuddy-ai", ".workbuddy", "node_modules", "web", "vendor", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			t.Fatalf("解析 %s 失败（护栏扫不动就等于没扫）: %v", path, perr)
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			rel = path
		}
		visit(filepath.ToSlash(rel), file, fset)
		return nil
	})
	require.NoError(t, err, "遍历 %s 失败", root)
}
