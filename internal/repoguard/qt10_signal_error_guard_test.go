package repoguard

// QT-10 ⑤ 护栏：getSignals 的错误必须被**消费**，禁止只 log。
//
// 为什么需要机械判据：QT-10 之前 `runBacktestInternal` 对 getSignals 的
// 一切错误只 `Warn` + 跳过当天 —— 「策略每 50 天 trap 一次」与「那天真的
// 缺数据」在曲线上完全一样。分级落地后，正确形状是：
//
//	signals, err := e.getSignals(...)
//	if err != nil {
//	    errors.Is(err, contracts.ErrDataGap) ? 跳天 + data_gap_skipped
//	                                         : return nil, terminateError(...)
//	}
//
// 本护栏用 AST（不是 grep）定位「getSignals 赋值 + 紧跟的 if err != nil」，
// 要求该分支同时具备 (a) 分类判定（`errors.Is`）与 (b) `return`。把分支改回
// `logger.Warn(...)` 而不 return → 本测试红；把 errors.Is 删掉只 return →
// 也红（说明分类被绕过）。
//
// 破坏验证（QT-10 施工时实跑）：见 docs/TASKS.md QT-10 完成记录。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// qt10ErrBlockVerdict 判定一个 `if err != nil` 分支是否消费错误。
type qt10ErrBlockVerdict struct {
	HasReturn   bool // 存在终止路径
	HasClassify bool // 存在 errors.Is 分类判定
}

// qt10JudgeSource 在源码里定位 getSignals 赋值后紧跟的 err 分支并判定。
// 返回 (verdict, found)。
func qt10JudgeSource(t *testing.T, src []byte) (qt10ErrBlockVerdict, bool) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "snippet.go", src, 0)
	require.NoError(t, err)

	var block *ast.IfStmt
	ast.Inspect(f, func(n ast.Node) bool {
		if block != nil {
			return false
		}
		b, ok := n.(*ast.BlockStmt)
		if !ok {
			return true
		}
		for i := 0; i+1 < len(b.List); i++ {
			as, ok := b.List[i].(*ast.AssignStmt)
			if !ok || !qt10AssignsGetSignals(as) {
				continue
			}
			ifs, ok := b.List[i+1].(*ast.IfStmt)
			if !ok {
				continue
			}
			if !qt10IsErrNotNil(ifs.Cond) {
				continue
			}
			block = ifs
			return false
		}
		return true
	})
	if block == nil {
		return qt10ErrBlockVerdict{}, false
	}

	var v qt10ErrBlockVerdict
	ast.Inspect(block.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.ReturnStmt:
			v.HasReturn = true
		case *ast.CallExpr:
			call, ok := x.Fun.(*ast.SelectorExpr)
			if !ok || call.Sel.Name != "Is" {
				return true
			}
			if pkg, ok := call.X.(*ast.Ident); ok && pkg.Name == "errors" {
				v.HasClassify = true
			}
		}
		return true
	})
	return v, true
}

func qt10AssignsGetSignals(as *ast.AssignStmt) bool {
	for _, rhs := range as.Rhs {
		call, ok := rhs.(*ast.CallExpr)
		if !ok {
			continue
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if ok && sel.Sel.Name == "getSignals" {
			return true
		}
	}
	return false
}

func qt10IsErrNotNil(cond ast.Expr) bool {
	bin, ok := cond.(*ast.BinaryExpr)
	if !ok || bin.Op != token.NEQ {
		return false
	}
	id, ok := bin.X.(*ast.Ident)
	if !ok || id.Name != "err" {
		return false
	}
	rhs, ok := bin.Y.(*ast.Ident)
	return ok && rhs.Name == "nil"
}

// TestQT10_GetSignalsErrorIsConsumed 钉住生产代码的正确形状。
func TestQT10_GetSignalsErrorIsConsumed(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")
	src, err := os.ReadFile(filepath.Join(root, "pkg", "backtest", "engine.go"))
	require.NoError(t, err)

	v, found := qt10JudgeSource(t, src)
	require.True(t, found, "engine.go 里没找到 getSignals 赋值 + 紧跟的 if err != nil —— 结构变了，本护栏需要同步更新")
	assert.True(t, v.HasReturn, "getSignals 的错误分支缺少 return —— 退回了「只 log + 跳天」（QT-10 要堵的洞）")
	assert.True(t, v.HasClassify, "getSignals 的错误分支没有 errors.Is 分类 —— 数据缺口与策略故障无法区分")

	text := string(src)
	assert.Contains(t, text, "contracts.ErrDataGap", "跳天分支必须显式判 ErrDataGap")
	assert.Contains(t, text, "contracts.CodeDataGapSkipped", "跳天必须留 invalid_reasons=data_gap_skipped")

	// provider 侧的两个分类点不许被摘掉（QT-10 ③-②）。
	for _, rel := range []string{
		filepath.Join("pkg", "strategy", "signal_provider.go"),
		filepath.Join("pkg", "sandbox", "wasm", "on_bar_session.go"),
	} {
		raw, readErr := os.ReadFile(filepath.Join(root, rel))
		require.NoError(t, readErr)
		assert.Contains(t, string(raw), "classifyExecutionError(",
			"%s 必须经 classifyExecutionError 归类（provider 侧三类包装）", rel)
		assert.Contains(t, string(raw), "contracts.ErrStrategyFault",
			"%s 必须产生 ErrStrategyFault（trap / 执行故障）", rel)
	}
}

// TestQT10_GuardJudgeSnippets 证明判据是 AST 而非文案：三种形状分别判。
func TestQT10_GuardJudgeSnippets(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		src        string
		wantFound  bool
		wantReturn bool
		wantClass  bool
	}{
		{
			name: "正确：分类 + 终止",
			src: `package p
func f() error {
	signals, err := e.getSignals()
	if err != nil {
		if errors.Is(err, contracts.ErrDataGap) {
			log()
		} else {
			return err
		}
	}
	_ = signals
	return nil
}`,
			wantFound: true, wantReturn: true, wantClass: true,
		},
		{
			name: "错误：只 log（QT-10 前的形状）",
			src: `package p
func f() error {
	signals, err := e.getSignals()
	if err != nil {
		logger.Warn().Err(err).Msg("Failed to get signals, skipping day")
	}
	_ = signals
	return nil
}`,
			wantFound: true, wantReturn: false, wantClass: false,
		},
		{
			name: "错误：不分类直接 return（绕过分级）",
			src: `package p
func f() error {
	signals, err := e.getSignals()
	if err != nil {
		return err
	}
	_ = signals
	return nil
}`,
			wantFound: true, wantReturn: true, wantClass: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, found := qt10JudgeSource(t, []byte(tc.src))
			assert.Equal(t, tc.wantFound, found)
			if !tc.wantFound {
				return
			}
			assert.Equal(t, tc.wantReturn, v.HasReturn)
			assert.Equal(t, tc.wantClass, v.HasClassify)
		})
	}
}
