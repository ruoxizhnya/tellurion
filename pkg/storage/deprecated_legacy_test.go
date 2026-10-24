package storage

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// QT-05 ④ / QT-17 ④：legacy 实验域与 research 域的所有权已迁至
// tellurion-agent:pkg/aistore。本测试钉住「标记不会悄悄消失」——两个文件的
// 每个导出声明都必须带 `Deprecated:` 段，否则读代码的人会以为它们还是
// 现行契约。删除期限 2027-01-31（S3 删掉 agent 的 replace 之后）；到期删除
// 这些符号时，本文件随符号一起删。
func TestLegacyExperimentAndResearchSymbolsAreDeprecated(t *testing.T) {
	for _, file := range []string{"experiments.go", "research.go"} {
		t.Run(file, func(t *testing.T) {
			f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ParseComments)
			if err != nil {
				t.Fatalf("parse %s: %v", file, err)
			}
			checked := 0
			for _, decl := range f.Decls {
				switch d := decl.(type) {
				case *ast.GenDecl:
					for _, spec := range d.Specs {
						for _, name := range exportedSpecNames(spec) {
							checked++
							assertDeprecated(t, file, name, docFor(d, spec))
						}
					}
				case *ast.FuncDecl:
					if d.Recv == nil || d.Name == nil || !d.Name.IsExported() {
						continue
					}
					checked++
					assertDeprecated(t, file, d.Name.Name, d.Doc)
				}
			}
			if checked == 0 {
				t.Fatalf("%s 一个导出声明都没扫到 —— 扫描器失效，不能空绿", file)
			}
		})
	}
}

func exportedSpecNames(spec ast.Spec) []string {
	switch s := spec.(type) {
	case *ast.TypeSpec:
		if s.Name.IsExported() {
			return []string{s.Name.Name}
		}
	case *ast.ValueSpec:
		var out []string
		for _, n := range s.Names {
			if n.IsExported() {
				out = append(out, n.Name)
			}
		}
		return out
	}
	return nil
}

// docFor 取符号自己的注释；const/var/type 组级注释挂在 GenDecl 上。
func docFor(d *ast.GenDecl, spec ast.Spec) *ast.CommentGroup {
	switch s := spec.(type) {
	case *ast.TypeSpec:
		if s.Doc != nil {
			return s.Doc
		}
	case *ast.ValueSpec:
		if s.Doc != nil {
			return s.Doc
		}
	}
	return d.Doc
}

func assertDeprecated(t *testing.T, file, name string, doc *ast.CommentGroup) {
	t.Helper()
	if doc == nil || !strings.Contains(doc.Text(), "Deprecated:") {
		t.Errorf("%s: 导出符号 %s 缺少 `Deprecated:` 段（QT-05 ④：owner 已迁 tellurion-agent/pkg/aistore，2027-01-31 前删除）",
			file, name)
	}
}
