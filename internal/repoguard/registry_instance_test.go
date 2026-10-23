package repoguard

// QT-11 护栏：**执行路径不许从注册表取共享实例**。
//
// 缺陷原貌（p0-remediation-design.md A2 / BE-21）：`Registry.Get` 返回的是
// 注册时那一个实例，回测执行路径拿它算信号 —— `SignalStrategy.cursor`、
// 流式 EMA 这类 per-instance 状态因此跨 run 存活。第二笔回测继承第一笔的
// 残留状态，曲线照样漂亮，没人看得出来。
//
// 修法是结构性的：注册表存构造函数，执行路径唯一入口是 `NewInstance`。
// 本护栏钉住这条纪律的两侧——
//   - 禁：`pkg/backtest/**`（生产代码）与 provider 实现
//     `pkg/strategy/signal_provider.go` 里出现「注册表 .Get(」或
//     `strategy.GlobalGet(`（它同样返回共享实例）；
//   - 证：provider 实现里必须真的出现 `.NewInstance(`——只删 `.Get(`
//     不接上 `NewInstance`，执行路径会瞎，这条正腿把它兜住。
//
// 为什么是 AST 而不是 grep：注释与文档字符串里大量出现 `Registry.Get`
// （包括本文件的说明），文本扫描会在正确状态下报红——参见
// module_lifecycle_callback_test.go 的同款教训。
//
// 判定口径（无类型信息，按名字近似）：接收者渲染文本含 "registry"
// （不区分大小写，覆盖 `strategy.DefaultRegistry`、`pl.registry`、
// `registry`），或正好是 `reg`（provider 里的局部变量名）。
// `stateStore.Get` / `cache.Get` / `Loader.Get` 这些同名方法与此无关，
// 不在射程内——所以本护栏不会误伤插件元数据查询
// （`strategy.GlobalPluginLoader.Get`，拿的是 PluginInfo，不是策略实例）。
//
// 元数据查询的合法形态：`Registry.Describe` / `ListWithInfo`。需要实例本身
// 的调用方（如 `ConfigureStrategy`）留在 pkg/strategy 内，且必须先过
// `IsShared` —— 工厂注册的条目改不到 run 用的实例。

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

// registryGuardBacktestDir 是被整体扫描的执行路径目录（相对仓库根）。
const registryGuardBacktestDir = "pkg/backtest"

// registryGuardProviderFile 是 provider 实现文件——它拥有取实例的唯一入口，
// 也是「必须走 NewInstance」那条正腿的落点。
const registryGuardProviderFile = "pkg/strategy/signal_provider.go"

type registryCallViolation struct {
	file string
	call string
}

// TestExecutionPathDoesNotUseSharedRegistryInstances 执行「禁」那一侧。
func TestExecutionPathDoesNotUseSharedRegistryInstances(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")
	violations, scanned := scanSharedRegistryGetCalls(root)

	sorted := append([]registryCallViolation(nil), violations...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].file+sorted[i].call < sorted[j].file+sorted[j].call
	})

	assert.Empty(t, sorted,
		"QT-11：执行路径从注册表取了共享实例（%s / %s 共扫描 %d 个生产文件）——"+
			"`Registry.Get` 返回注册期那一份，per-instance 状态会跨 run 存活。"+
			"执行路径改用 Registry.NewInstance（工厂注册）；只要元数据就用 Describe/ListWithInfo：%s",
		registryGuardBacktestDir, registryGuardProviderFile, len(scanned),
		strings.Join(renderRegistryViolations(sorted), "\n"))
}

// TestRegistryGetGuardSeesTheExecutionPath 反盲区：证明扫描器真的读到了
// 它声称覆盖的文件（目录改名 / glob 写错会让上一条永远绿）。
func TestRegistryGetGuardSeesTheExecutionPath(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")
	_, scanned := scanSharedRegistryGetCalls(root)

	require.Contains(t, scanned, registryGuardProviderFile,
		"provider 实现没被扫到——护栏的覆盖面被悄悄缩小了")
	backtestFiles := 0
	for _, f := range scanned {
		if strings.HasPrefix(f, registryGuardBacktestDir+"/") {
			backtestFiles++
		}
	}
	assert.GreaterOrEqual(t, backtestFiles, 20,
		"pkg/backtest 生产文件只扫到 %d 个，明显低于实际规模（引擎侧至少 20 个）——扫描面坏了", backtestFiles)
}

// TestProviderImplementationResolvesInstancesPerRun 执行「证」那一侧：
// provider 必须经 NewInstance 拿实例。
func TestProviderImplementationResolvesInstancesPerRun(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")
	rel := filepath.FromSlash(registryGuardProviderFile)
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(root, rel), nil, 0)
	require.NoError(t, err, "provider 实现解析失败")

	hits := newInstanceCalls(fset, file)
	assert.NotEmpty(t, hits,
		"QT-11：%s 里没有任何 .NewInstance( 调用——执行路径没有 per-run 实例入口，"+
			"策略状态又会跨 run 串", registryGuardProviderFile)
}

// TestRegistryGetGuardJudgesSnippets 自证腿：把扫描器声称能抓的形状逐条喂给它。
// 没有这一条，「今天零命中」可能只是「这个扫描器是瞎的」。
func TestRegistryGetGuardJudgesSnippets(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		src  string
		// leg = "ban" 断言禁腿（.Get 必须被报出 / 放行）；
		// leg = "instance" 断言正腿（.NewInstance 必须被认出，且禁腿不误报）。
		leg  string
		want bool // ban 腿：是否应报违规；instance 腿：是否应命中 NewInstance
	}{
		{
			name: "默认注册表取实例（引擎侧真实缺陷形状）",
			src: `package p
func f() {
	_, _ = strategy.DefaultRegistry.Get("momentum")
}`,
			leg:  "ban",
			want: true,
		},
		{
			name: "包级 GlobalGet 同样返回共享实例",
			src: `package p
func f() {
	_ = strategy.GlobalGet("momentum")
}`,
			leg:  "ban",
			want: true,
		},
		{
			name: "局部变量指向注册表再取实例",
			src: `package p
func f(reg *Registry) {
	s, _ := reg.Get("x")
	_ = s
}`,
			leg:  "ban",
			want: true,
		},
		{
			name: "结构体字段上的注册表",
			src: `package p
func f(p *provider) {
	s, _ := p.registry.Get(p.name)
	_ = s
}`,
			leg:  "ban",
			want: true,
		},
		{
			name: "执行路径合法入口：NewInstance",
			src: `package p
func f(reg *Registry) {
	s, _ := reg.NewInstance("x")
	_ = s
}`,
			leg:  "instance",
			want: true,
		},
		{
			name: "对照腿：Describe 只要元数据，禁腿放行",
			src: `package p
func f(reg *Registry) {
	i, _ := reg.Describe("x")
		_ = i
}`,
			leg:  "ban",
			want: false,
		},
		{
			name: "对照腿：同名但不同对象的 Get 不在射程（stateStore / cache / Loader）",
			src: `package p
func f(e *Engine) {
	_ = e.stateStore.Get("id")
	_ = e.cache.Get(nil, "600000.SH", "", "")
	_ = strategy.GlobalPluginLoader.Get("x")
}`,
			leg:  "ban",
			want: false,
		},
		{
			name: "对照腿：注释里写 Registry.Get 不算（证明是 AST 而非扫原文）",
			src: `package p
// 旧实现经 Registry.Get 拿共享实例 —— 那条路已封死。
func f() {}`,
			leg:  "ban",
			want: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "snippet.go", tc.src, 0)
			require.NoError(t, err)

			violations := registryGetViolationsIn(fset, "snippet.go", f)

			switch tc.leg {
			case "instance":
				hits := newInstanceCalls(fset, f)
				if tc.want {
					assert.NotEmpty(t, hits, "该认出 NewInstance 调用却没认——正腿是瞎的")
				}
				assert.Empty(t, violations, "NewInstance 被误判成共享实例取用：%v", renderRegistryViolations(violations))
			default:
				if tc.want {
					assert.NotEmpty(t, violations, "该报违规却没报 —— 扫描器对这个形状是瞎的")
				} else {
					assert.Empty(t, violations, "不该报却报了：%v", renderRegistryViolations(violations))
				}
			}
		})
	}
}

// ──────────────────────────────────────────────────────────────────────
// scanner
// ──────────────────────────────────────────────────────────────────────

// scanSharedRegistryGetCalls 扫描执行路径（pkg/backtest 生产代码 +
// provider 实现），返回违规清单与「实际扫过的文件」清单。
func scanSharedRegistryGetCalls(root string) ([]registryCallViolation, []string) {
	var (
		violations []registryCallViolation
		scanned    []string
	)

	add := func(path, rel string) {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return // 解析失败由 go build / go vet 负责，护栏不重复报
		}
		scanned = append(scanned, rel)
		violations = append(violations, registryGetViolationsIn(fset, rel, file)...)
	}

	add(filepath.Join(root, filepath.FromSlash(registryGuardProviderFile)), registryGuardProviderFile)

	backtestRoot := filepath.Join(root, filepath.FromSlash(registryGuardBacktestDir))
	err := filepath.WalkDir(backtestRoot, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel := registryGuardRel(root, path)
		if rel == "" {
			return nil
		}
		add(path, rel)
		return nil
	})
	if err != nil {
		// 目录不存在 = 覆盖面整体消失，必须红（不是静默返回空清单）。
		panic(fmt.Sprintf("repoguard: 扫描 %s 失败：%v（目录被改名或护栏被搬家，覆盖面已失效）", registryGuardBacktestDir, err))
	}

	return violations, scanned
}

// registryGetViolationsIn 在一个已解析文件里找「注册表 .Get(」与
// `strategy.GlobalGet(`。
func registryGetViolationsIn(fset *token.FileSet, rel string, file *ast.File) []registryCallViolation {
	var out []registryCallViolation

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}

		switch sel.Sel.Name {
		case "Get":
			if isRegistryReceiver(renderRegistryExpr(fset, sel.X)) {
				out = append(out, registryCallViolation{file: rel, call: renderRegistryExpr(fset, call)})
			}
		case "GlobalGet":
			// 包级包装返回的仍是共享实例，与 Registry.Get 同一条禁令。
			if renderRegistryExpr(fset, sel.X) == "strategy" {
				out = append(out, registryCallViolation{file: rel, call: renderRegistryExpr(fset, call)})
			}
		}
		return true
	})

	return out
}

// newInstanceCalls 返回文件里所有 `.NewInstance(` 调用的渲染文本
// （provider 实现的正腿：执行路径必须经它取实例）。
func newInstanceCalls(fset *token.FileSet, file *ast.File) []string {
	var hits []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "NewInstance" {
			hits = append(hits, renderRegistryExpr(fset, call))
		}
		return true
	})
	return hits
}

// isRegistryReceiver 按名字近似判定接收者是不是策略注册表。
// 无类型信息，故取「含 registry」与「叫 reg」两个形状——判定依据写在文件头。
func isRegistryReceiver(receiver string) bool {
	low := strings.ToLower(receiver)
	if low == "reg" || strings.HasSuffix(low, ".reg") {
		return true
	}
	return strings.Contains(low, "registry")
}

func renderRegistryExpr(fset *token.FileSet, n ast.Node) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, n); err != nil {
		return ""
	}
	return strings.TrimSpace(buf.String())
}

func renderRegistryViolations(v []registryCallViolation) []string {
	out := make([]string, 0, len(v))
	for _, x := range v {
		out = append(out, "  "+x.file+": "+x.call)
	}
	return out
}

// registryGuardRel 把绝对路径换成仓库根相对（slash 分隔）；模块外返回 ""。
// 与 canonical_layer_test.go 的 fileRel 同逻辑，换个名字避免同包重名。
func registryGuardRel(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return ""
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return ""
	}
	return rel
}
