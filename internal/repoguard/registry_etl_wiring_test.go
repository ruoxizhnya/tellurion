package repoguard

// DATA-08 结构护栏：多源 Registry 的取数链今天**没有生产调用方**，这是
// 2026-10-16 主任裁决的「停用路线」—— 链保持配置（观测端点要展示它），
// 但「没有生产路径走 Fetch」必须是一个**可见且刻意**的状态，而不是
// 「反正没人管，谁想接就接」。
//
// 守两条，各管一条漂移路径：
//
//   A. 生产代码（cmd/ pkg/ internal/ 的非测试文件）里，package source
//      **之外**不得出现 `NewETLPipeline` 的任何引用 —— 调用、函数值
//      （`f := source.NewETLPipeline` 存起来再调）、点导入后的裸名，
//      一律 Ident 级判定：包一层函数值绕不开名字匹配。package source
//      **内部**不许调用 `NewETLPipeline(...)`（同包构造同样是接线意图），
//      但构造器本体的 `&ETLPipeline{...}` 字面量与 `NewETLPipeline` 这个
//      函数名本身是**实现**，放行 —— 第一版判据把包内字面量也算违规，
//      结果守卫从第一天起就对着 etl.go 常红（假护栏形状），故按「包内
//      字面量是实现、包外才算接线」收窄。包外同样不得出现
//      `ETLPipeline{...}` 复合字面量。
//      Registry.Fetch 的唯一生产调用方是 ETLPipeline.Process，而
//      ETLPipeline 的生产构造点必须逐条进 etlWiringAllowlist 并写理由 ——
//      哪天有人把它接进 HTTP/作业层而没走 DATA-10 的接线计划
//      （单位边界 → Normalizer → sync_jobs 关系），这条守卫当场红。
//      白名单今天有两条：DATA-10（2026-10-16 主任裁决入口 (b)）批准的
//      `cmd/data/capital_flow_executor.go`，与 DBT-10（2026-10-19）批准的
//      `cmd/data/top_list_executor.go` —— 两处都是 `NewETLPipeline` 引用。
//      放行按「文件 + 形态」精确匹配，不是文件级免检；条目不再匹配时报红
//      （死条目同查，理由同 store_migration_isolation_test.go）。
//      **已知边界（刻意不追）**：`type P = source.ETLPipeline` 后再
//      `P{}` —— 类型别名绕开名字匹配。防它需要类型信息（go/types），
//      而那是编译器的事；本护栏防的是漂移与疏忽，不是蓄意伪装（蓄意者
//      会直接改护栏，那时红的是他的 diff）。
//
//   B. cmd/data/registry_*.go 这一组观测面文件（production，非 _test）
//      不得出现 `.Fetch(` 选择器调用。Registry 的 HTTP 端点今天是也只
//      应该是只读观测（status/health/chains）；一个会触发真实取数的端点
//      必须经过与 A 同样的显式决策。按前缀 glob 而不是写死一个文件名：
//      今天射程是 registry_handlers.go 与 registry_init.go，将来观测面
//      长出新文件（registry_admin.go 之类）自动进射程，不会静默漏查；
//      glob 命中为空时测试自报失败 —— 文件被改名/删光的那一刻不许假绿。
//
// 为什么放在 repoguard：要扫的是全仓生产代码，walk 机器在这个包里已有
// 先例（package_wiring_test.go，AUD-45/46）。测试文件被刻意排除 ——
// etl_test.go / integration_test.go 对 ETLPipeline 的行为覆盖正是今天
// 这条链唯一的「使用者」，把它们算进违规会让守卫从第一天起就红。
//
// 两条规则的牙都由替身用例当场证明（TestETLWiringGuard_* /
// TestRegistryObservabilityGuard_*）：合成源码喂进去，该红的红、该放的
// 放 —— 不靠「将来真有人踩」。

import (
	"fmt"
	"go/ast"
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

// etlPackageName 是 ETLPipeline 的定义包名。包内的复合字面量是实现
// （构造器本体），包外才是接线。
const etlPackageName = "source"

// etlWiringOffendersInSource 返回一份源码里所有「把 ETLPipeline 接进
// 生产」的站点，元素形如 `file:line: 形态`。
//
// 判定用 AST 而不是文本匹配，文本陷阱因此不存在：定义不是代码里的一次
// 「引用命中」之外的形态、注释点名（etl.go 文件头自己就有）不进 AST
// —— 解析时刻意不带 comment。
//
// 注意解析模式：必须完整解析（mode 0）。package_wiring_test.go 用的
// parser.ImportsOnly 只解析 import 声明、整个跳过函数体 —— 第一版图省
// 事照抄了它，结果替身腿全部落空、仓库级测试「假绿」（扫了个空 AST）。
func etlWiringOffendersInSource(src, filename string) []string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		// 解析失败如实报出来：文件坏掉是错误，不是静默跳过。
		return []string{filename + ": parse error: " + err.Error()}
	}
	inETLPackage := f.Name.Name == etlPackageName
	var offenders []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			// 同包调用才算这一腿的违规；包外的调用由下面的 Ident 腿覆盖
			//（Ident 腿在包内放行，避免把构造器自己的函数名算成接线）。
			if inETLPackage {
				if name := calleeName(x.Fun); name == "NewETLPipeline" || name == etlPackageName+".NewETLPipeline" {
					offenders = append(offenders, fmt.Sprintf("%s:%d: NewETLPipeline 调用", filename, fset.Position(x.Pos()).Line))
				}
			}
		case *ast.Ident:
			// 包外任何 NewETLPipeline 引用即红：直接调用、函数值
			// （f := source.NewETLPipeline）、点导入后的裸名。选择器
			// 的 Sel 也是 Ident，source.NewETLPipeline 里的
			// NewETLPipeline 会以 Ident 形态被访到，故无需单列
			// SelectorExpr 腿。
			if !inETLPackage && x.Name == "NewETLPipeline" {
				offenders = append(offenders, fmt.Sprintf("%s:%d: NewETLPipeline 引用", filename, fset.Position(x.Pos()).Line))
			}
		case *ast.CompositeLit:
			// 包内的字面量是构造器实现，放行；包外手搓 pipeline 算接线。
			if !inETLPackage {
				if name := typeName(x.Type); name == "ETLPipeline" || name == etlPackageName+".ETLPipeline" {
					offenders = append(offenders, fmt.Sprintf("%s:%d: ETLPipeline 复合字面量", filename, fset.Position(x.Pos()).Line))
				}
			}
		}
		return true
	})
	return offenders
}

// calleeName 把调用表达式的被调方渲染成可比对的名字：包级函数是 Ident
// （NewETLPipeline），跨包调用是 SelectorExpr（source.NewETLPipeline）。
// 其他形态（函数字面量立即调用、方法值……）返回 ""，不误伤。
func calleeName(fun ast.Expr) string {
	switch e := fun.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		if pkg, ok := e.X.(*ast.Ident); ok {
			return pkg.Name + "." + e.Sel.Name
		}
	}
	return ""
}

// typeName 渲染复合字面量的类型名：跨包是 SelectorExpr（source.ETLPipeline），
// 同包是 Ident（ETLPipeline）。指针形态 `&ETLPipeline{}` 的 Type 仍是
// CompositeLit 里的那个类型表达式，一并覆盖。
func typeName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		if pkg, ok := e.X.(*ast.Ident); ok {
			return pkg.Name + "." + e.Sel.Name
		}
	}
	return ""
}

// registryObservabilityOffendersInSource 返回观测面源码里所有 `.Fetch(`
// 选择器调用站点。只认精确的 `Fetch`——`FetchStocks` /
// `FetchDailyOHLCV` 这类选择器不是它，不误伤。
func registryObservabilityOffendersInSource(src, filename string) []string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		return []string{filename + ": parse error: " + err.Error()}
	}
	var offenders []string
	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "Fetch" {
			offenders = append(offenders, fmt.Sprintf("%s:%d: .Fetch 选择器调用", filename, fset.Position(sel.Pos()).Line))
		}
		return true
	})
	return offenders
}

// walkProductionGoFiles 枚举仓库里所有**生产** Go 文件（非 _test.go），
// 范围 cmd/ pkg/ internal/ —— 接线只可能发生在这三棵树里。跳过目录与
// package_wiring_test.go 保持一致（.workbuddy-ai 里躺着别的工单的
// worktree，扫进去会把别人的在途代码算进本仓状态）。
func walkProductionGoFiles(t *testing.T, root string, fn func(rel, src string)) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".workbuddy-ai", "node_modules", "web", "testdata", "vendor":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if !strings.HasPrefix(rel, "cmd/") && !strings.HasPrefix(rel, "pkg/") && !strings.HasPrefix(rel, "internal/") {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		fn(rel, string(raw))
		return nil
	})
	require.NoError(t, err)
}

// etlWiringPermit 是一条被显式批准的接线站点：精确到「哪个文件的哪种形态」。
//
// 粒度是刻意的：放行整个文件等于「以后往里塞什么都不拦」，
// 而「谁接了线」这件事的价值恰恰在于它可被逐条质问。
type etlWiringPermit struct {
	file   string
	kind   string
	reason string
}

// etlWiringAllowlist —— DATA-08「停用」到 DATA-10「解禁」的那次决策留痕。
var etlWiringAllowlist = []etlWiringPermit{
	{
		file: "cmd/data/capital_flow_executor.go",
		kind: "NewETLPipeline 引用",
		reason: "DATA-10（主任 2026-10-16 裁决：链路选 capital_flow:[eastmoney]、" +
			"入口选 (b) 作业层）落的第一条链。执行器经 Registry.Fetch → ETLPipeline.Process → " +
			"BulkInsert 写 capital_flow 表；ctx 取消与进度用既有 worker 语义。" +
			"另一条链（global_ohlcv 等）要接入时另立工单、另批一条。" +
			"〔DBT-09 2026-10-19 追注：批准的是**这个接线站点**，不是链内容 —— " +
			"链首选已按可得性改成 tushare_moneyflow，eastmoney 降为兜底；" +
			"入口、执行器、站点均未变，所以这条批准继续有效。〕",
	},
	{
		file: "cmd/data/top_list_executor.go",
		kind: "NewETLPipeline 引用",
		reason: "DBT-10（主任 2026-10-19 派工，源头是 DBT-07 逐表处置里登记的「top_list 接线另立」）" +
			"接的第二条链：top_list:[eastmoney_toplist]。接线站点形态沿用 DATA-10 立的那条先例" +
			"（作业层 job_type → executor → Registry.Fetch → ETLPipeline.Process → BulkInsert），" +
			"入口与站点形状一致，内容与链不同。" +
			"适配器自 ODR-011 就在册（EastmoneyTopListAdapter.fetchTopList），缺的一直是作业层这一段 —— " +
			"job_type 枚举 + 本执行器 + TopListNormalizer + 类型化门的 case。" +
			"执行器按页翻页（整市场榜单按日分页，不按 symbol 分片），任一页失败即中止整窗，" +
			"翻页预算用尽仍未取尽 ⇒ 如实报错不静默截断（CR-41 家族）。" +
			"落库经 BulkInsert 写 top_list 表；ctx 取消与进度沿用既有 worker 语义。",
	},
}

// partitionETLWiringOffenders 把守卫抓到的站点分成「未批准的违规」与
// 「不再匹配的死条目」。
//
// 两个方向都得查，只查一个就是半个护栏：
//   - 只查违规 ⇒ 白名单里的条目腐烂后，那个文件变成永久免检区；
//   - 只查死条目 ⇒ 没人会去删它，因为删了就等于承认接线发生过。
//
// 站点文本解析不出「文件:行号: 形态」的形状时一律算违规（fail-loud）——
// 解析器瞎了不许表现为「零违规」。
func partitionETLWiringOffenders(offenders []string, permits []etlWiringPermit) (violations, deadPermits []string) {
	matched := make([]bool, len(permits))
	for _, off := range offenders {
		file, kind, ok := parseETLWiringSite(off)
		if !ok {
			violations = append(violations, off+" （站点文本不是「文件:行号: 形态」的形状，按 fail-loud 算违规）")
			continue
		}
		permitted := false
		for i, p := range permits {
			if p.file == file && p.kind == kind {
				permitted = true
				matched[i] = true
			}
		}
		if !permitted {
			violations = append(violations, off)
		}
	}
	for i, p := range permits {
		if !matched[i] {
			deadPermits = append(deadPermits, fmt.Sprintf("%s: %s（批准理由：%s）", p.file, p.kind, p.reason))
		}
	}
	return violations, deadPermits
}

// parseETLWiringSite 还原 etlWiringOffendersInSource 的输出形状
// `file:line: kind`。kind 里可能带冒号（parse error 那条），所以按
// **第一个** ": " 切左段，再在左段里按最后一个冒号取行号。
func parseETLWiringSite(off string) (file, kind string, ok bool) {
	sep := strings.Index(off, ": ")
	if sep < 0 {
		return "", "", false
	}
	left, kind := off[:sep], off[sep+2:]
	c := strings.LastIndex(left, ":")
	if c <= 0 {
		return "", "", false
	}
	line := left[c+1:]
	if line == "" {
		return "", "", false
	}
	for _, r := range line {
		if r < '0' || r > '9' {
			return "", "", false
		}
	}
	return left[:c], kind, true
}

// TestProductionCodeDoesNotWireETLPipeline —— 规则 A 的仓库级断言：
// 生产文件里的 ETLPipeline 构造站点必须逐条进白名单，白名单外的站点
// 一律红。
//
// 今天白名单里有两条（DATA-10 2026-10-16 主任裁决入口 (b) 的 capital_flow
// 执行器，与 DBT-10 2026-10-19 的 top_list 执行器）。红的那一刻只有两种正当
// 解释：又接了一条新链（来加白名单并写理由），或者有人偷偷接了线（回退）。
//
// 白名单不是「登记一下就完事」的清单，它自己也被钉住：死条目报红
// （登记会腐烂，只查「多」不查「少」的 allowlist 是半个护栏，
// 与 store_migration_isolation_test.go 的同一条纪律一致）。
func TestProductionCodeDoesNotWireETLPipeline(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")
	var offenders []string
	walkProductionGoFiles(t, root, func(rel, src string) {
		offenders = append(offenders, etlWiringOffendersInSource(src, rel)...)
	})
	sort.Strings(offenders)

	violations, deadPermits := partitionETLWiringOffenders(offenders, etlWiringAllowlist)
	sort.Strings(violations)
	sort.Strings(deadPermits)

	assert.Empty(t, deadPermits,
		"白名单里有不再匹配的条目 —— 该接线点被删了或改了形状，条目要一起清掉，"+
			"否则白名单会变成「以后随便往这个文件塞什么都算批过」的空壳：%v", deadPermits)

	assert.Empty(t, violations,
		"接线站点没有被白名单批准 —— Registry.Fetch 的取数链按 DATA-08（2026-10-16 主任裁决）默认停用，"+
			"接线必须先落 DATA-10 计划（源单位实测 → 同步边界换算 → Normalizer → 与 sync_jobs 的关系），"+
			"再来把站点加进 etlWiringAllowlist 并写理由。白名单按「文件 + 形态」批准："+
			"同文件换个形态也算新决策，不在批准范围内：%v", violations)
}

// TestRegistryObservabilitySurfaceDoesNotFetch —— 规则 B 的仓库级断言：
// 观测面文件（cmd/data/registry_*.go，production）不得触发真实取数。
// 按前缀 glob 而非写死文件名：观测面长出新文件自动进射程；glob 命中为
// 空时自报失败，文件被改名/删光的那一刻不许假绿。
func TestRegistryObservabilitySurfaceDoesNotFetch(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")
	matches, err := filepath.Glob(filepath.Join(root, "cmd", "data", "registry_*.go"))
	require.NoError(t, err)
	production := make([]string, 0, len(matches))
	for _, m := range matches {
		if strings.HasSuffix(m, "_test.go") {
			continue
		}
		production = append(production, m)
	}
	require.NotEmpty(t, production,
		"cmd/data/registry_*.go 一个生产文件都没匹配到 —— 观测面被改名或删光时，"+
			"本守卫不许以「零个文件可查」的形式假绿")

	var offenders []string
	for _, path := range production {
		raw, rerr := os.ReadFile(path)
		require.NoError(t, rerr)
		rel, rerr := filepath.Rel(root, path)
		require.NoError(t, rerr)
		offenders = append(offenders, registryObservabilityOffendersInSource(string(raw), filepath.ToSlash(rel))...)
	}
	sort.Strings(offenders)

	assert.Empty(t, offenders,
		"观测面文件出现了 .Fetch 调用 —— DATA-08 裁决 Registry 的 HTTP 面只做只读观测"+
			"（status/health/chains）。会触发真实取数的端点属于新的生产写路径，按 DATA-10 计划显式立项：%v", offenders)
}

// TestETLWiringGuard_FlagsSyntheticCallSite —— 规则 A 的替身六腿：
// 跨包引用、同包调用、包外字面量、函数值旁路各红一次；解析失败必须
// fail-loud；构造器本体的包内字面量与注释不误伤。少了这一组，仓库级
// 测试今天的绿可能只是因为「扫的范围里恰好没有」。
func TestETLWiringGuard_FlagsSyntheticCallSite(t *testing.T) {
	t.Parallel()

	cross := `
package main

import "tellurion/pkg/data/source"

func wire() {
	_ = source.NewETLPipeline(nil, nil)
}
`
	got := etlWiringOffendersInSource(cross, "synthetic_cross.go")
	require.Len(t, got, 1, "source.NewETLPipeline(...) 必须被抓到：%v", got)
	assert.Contains(t, got[0], "NewETLPipeline 引用")

	local := `
package source

func wire() {
	_ = NewETLPipeline(nil, nil)
}
`
	got = etlWiringOffendersInSource(local, "synthetic_local.go")
	require.Len(t, got, 1, "同包 NewETLPipeline(...) 同样必须被抓到：%v", got)
	assert.Contains(t, got[0], "NewETLPipeline 调用")

	literal := `
package main

import "tellurion/pkg/data/source"

func wire() {
	_ = &source.ETLPipeline{}
}
`
	got = etlWiringOffendersInSource(literal, "synthetic_literal.go")
	require.Len(t, got, 1, "包外手搓 ETLPipeline 字面量必须被抓到：%v", got)

	// 函数值旁路腿（自审修订二补）：只认调用形态时，把构造器存进变量
	// 就绕开了匹配。Ident 级判定堵住这条路。
	value := `
package main

import "tellurion/pkg/data/source"

func wire() {
	f := source.NewETLPipeline
	_ = f
}
`
	got = etlWiringOffendersInSource(value, "synthetic_value.go")
	require.Len(t, got, 1, "把构造器存进函数值同样必须被抓到 —— 否则包一层变量就绕开了字面匹配：%v", got)

	// 反证腿一：构造器本体的包内字面量是实现不是接线 —— 第一版判据
	// 把它算违规，守卫从第一天起就对着 etl.go 常红（假护栏形状）。
	definition := `
package source

// NewETLPipeline 的注释里点名 NewETLPipeline 不算代码。
func NewETLPipeline(reg *Registry, store any) *ETLPipeline {
	return &ETLPipeline{Registry: reg}
}
`
	require.Empty(t, etlWiringOffendersInSource(definition, "synthetic_def.go"),
		"package source 内的字面量与注释不是接线站点，不该被算进违规")

	// 反证腿二：解析失败必须 fail-loud 报出来，而不是静默跳过 ——
	// 「扫了个空 AST」正是第一版假绿的形状。
	broken := "package main\n\nfunc {"
	got = etlWiringOffendersInSource(broken, "synthetic_broken.go")
	require.Len(t, got, 1, "解析失败必须报出一条 offender：%v", got)
	assert.Contains(t, got[0], "parse error")
}

// TestETLWiringAllowlist_PermitsOnlyTheApprovedShape —— 白名单这一层的牙。
//
// 加白名单这件事本身最容易写成假护栏：「文件进了白名单」如果被实现成
// 「文件进了白名单就不再检查」，那等于把 DATA-08 的守卫对着一个文件关掉。
// 五腿各钉一种绕法，缺一条就有一类静默接线过得去。
func TestETLWiringAllowlist_PermitsOnlyTheApprovedShape(t *testing.T) {
	t.Parallel()

	permits := []etlWiringPermit{{
		file:   "cmd/data/capital_flow_executor.go",
		kind:   "NewETLPipeline 引用",
		reason: "测试替身",
	}}

	// 腿一：批准的形状放行，且条目不算死。
	v, dead := partitionETLWiringOffenders([]string{"cmd/data/capital_flow_executor.go:94: NewETLPipeline 引用"}, permits)
	assert.Empty(t, v, "被批准的站点不该算违规")
	assert.Empty(t, dead, "匹配上的条目不该算死条目")

	// 腿二：同文件换形态仍然红 —— 白名单不是文件级免检。
	v, _ = partitionETLWiringOffenders([]string{"cmd/data/capital_flow_executor.go:99: ETLPipeline 复合字面量"}, permits)
	require.Len(t, v, 1, "同文件手搓 ETLPipeline 字面量不在批准范围内，必须仍然红")
	assert.Contains(t, v[0], "复合字面量")

	// 腿三：换文件同形态仍然红 —— 别的文件想接同一条链要另批。
	v, _ = partitionETLWiringOffenders([]string{"cmd/data/another_wiring.go:7: NewETLPipeline 引用"}, permits)
	require.Len(t, v, 1, "未批准的文件的同样形态必须红")
	assert.Contains(t, v[0], "another_wiring.go")

	// 腿四：死条目报红（登记会腐烂）。
	v, dead = partitionETLWiringOffenders(nil, permits)
	assert.Empty(t, v)
	require.Len(t, dead, 1, "白名单里不再匹配的条目必须报红")
	assert.Contains(t, dead[0], "capital_flow_executor.go")

	// 腿五：解析不出形状的站点按 fail-loud 算违规 —— 解析器瞎了不许
	// 表现为「零违规」（etlWiringOffendersInSource 的 parse error 就长这样）。
	v, _ = partitionETLWiringOffenders([]string{"broken.go: parse error: expected '}'"}, permits)
	require.Len(t, v, 1)
	assert.Contains(t, v[0], "fail-loud")
}

// TestETLWiringAllowlist_EntryCarriesReason 钉住白名单条目必须带理由。
//
// 「加白名单 + 写理由」是工单 ⑤ 的原话；只有文件名没有理由的条目，
// 下一个人无从判断它是不是还成立，于是它必然腐烂成没人敢删的装饰。
func TestETLWiringAllowlist_EntryCarriesReason(t *testing.T) {
	t.Parallel()

	require.NotEmpty(t, etlWiringAllowlist, "白名单空着时这条守卫不该假绿")
	for _, p := range etlWiringAllowlist {
		assert.NotEmpty(t, p.file, "白名单条目必须点名文件")
		assert.NotEmpty(t, p.kind, "白名单条目必须点名放行的形态")
		assert.GreaterOrEqual(t, len([]rune(p.reason)), 20,
			"%s: %s 的批准理由太短，无法被质问（要写清是哪张工单、谁的裁决）", p.file, p.kind)
		assert.Contains(t, p.reason, "DATA-",
			"%s: %s 的理由必须指回批准它的工单，否则不知道是谁批的", p.file, p.kind)
	}
}

// TestRegistryObservabilityGuard_FlagsSyntheticFetch —— 规则 B 的替身：
// 合成一个带 .Fetch 的观测面源码必须红；只读形态必须放行。
func TestRegistryObservabilityGuard_FlagsSyntheticFetch(t *testing.T) {
	t.Parallel()

	fetching := `
package main

func (h *registryHandler) preview(c *gin.Context) {
	resp, err := h.reg.Fetch(c.Request.Context(), req)
	_ = resp
	_ = err
}
`
	got := registryObservabilityOffendersInSource(fetching, "synthetic_fetch.go")
	require.Len(t, got, 1, "观测面里的 .Fetch 必须被抓到：%v", got)

	readonly := `
package main

func (h *registryHandler) status(c *gin.Context) {
	_ = h.reg.ListAdapters()
	_ = h.reg.ListChains()
	_ = h.reg.HealthCheck(c.Request.Context())
}
`
	require.Empty(t, registryObservabilityOffendersInSource(readonly, "synthetic_readonly.go"),
		"只读观测形态（List/HealthCheck）必须放行 —— 否则判据不是「不得取数」而是别的什么")
}
