package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeProfile 落一份合成 profile 到临时目录并返回路径。
func writeProfile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "coverage.out")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	return path
}

const syntheticProfile = `mode: set
github.com/example.com/m/pkg/a/a.go:10.2,12.10 3 1
github.com/example.com/m/pkg/a/a.go:14.2,15.8 2 0
github.com/example.com/m/pkg/b/b.go:5.2,6.8 1 1
github.com/example.com/m/pkg/c/c.go:1.1,2.2 4 0
`

func TestParseProfile_StatementWeighted(t *testing.T) {
	stats, err := parseProfile(writeProfile(t, syntheticProfile))
	if err != nil {
		t.Fatalf("parseProfile: %v", err)
	}

	if got := stats[""].percent(); got != 40 { // (3+1)/10
		t.Fatalf("total = %.1f%%; want 40.0%%", got)
	}
	if got := stats["github.com/example.com/m/pkg/a"].percent(); got != 60 { // 3/5
		t.Fatalf("pkg/a = %.1f%%; want 60.0%%", got)
	}
	if got := stats["github.com/example.com/m/pkg/b"].percent(); got != 100 {
		t.Fatalf("pkg/b = %.1f%%; want 100.0%%", got)
	}
	c := stats["github.com/example.com/m/pkg/c"]
	if !c.present || c.percent() != 0 || c.covered != 0 {
		t.Fatalf("pkg/c = %+v; want present, 0%%, 0 covered", c)
	}
}

func TestParseProfile_RejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"首行不是 mode":     "mode: set\nbadline\n",
		"字段数不对":         "mode: set\ngithub.com/x/y.go:1.1,2.2 1\n",
		"numStmts 不是整数": "mode: set\ngithub.com/x/y.go:1.1,2.2 x 1\n",
		"count 不是整数":    "mode: set\ngithub.com/x/y.go:1.1,2.2 1 x\n",
		"location 缺冒号":  "mode: set\ngithub.com/x/y.go 1 1\n",
		"空 profile":     "mode: set\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseProfile(writeProfile(t, body)); err == nil {
				t.Fatalf("parseProfile(%q) = nil error; want an error", body)
			}
		})
	}
}

func TestParseProfile_MissingFile(t *testing.T) {
	if _, err := parseProfile(filepath.Join(t.TempDir(), "nope.out")); err == nil {
		t.Fatal("parseProfile on a missing file = nil error; want an error")
	}
}

// TestImportPathOf 钉住「文件路径 → import path」的截取规则（丢文件名）。
func TestImportPathOf(t *testing.T) {
	if got := importPathOf("github.com/example.com/m/pkg/a/a.go"); got != "github.com/example.com/m/pkg/a" {
		t.Fatalf("importPathOf = %q", got)
	}
	if got := importPathOf("plain.go"); got != "plain.go" {
		t.Fatalf("importPathOf(no dir) = %q; want the input unchanged", got)
	}
}

// TestGateExitCodes 用合成 profile 端到端验门禁判据（进程退出码）。
// 直接调 main 的逻辑不好断言退出码，这里退而求其次：验证判定所依赖的
// 百分比计算与阈值比较语义（percent < min 即红）。
func TestThresholdSemantics(t *testing.T) {
	stats, err := parseProfile(writeProfile(t, syntheticProfile))
	if err != nil {
		t.Fatalf("parseProfile: %v", err)
	}
	total := stats[""].percent()
	cases := []struct {
		name string
		min  float64
		want bool // true = 应判红
	}{
		{"阈值恰等于实测：不红", 40, false},
		{"阈值高一点：红", 40.1, true},
		{"阈值低很多：不红", 30, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := total < tt.min
			if got != tt.want {
				t.Fatalf("total %.1f < min %.1f = %v; want %v", total, tt.min, got, tt.want)
			}
		})
	}
}

// TestMultiFlag 钉住可重复 -pkg-min 的收集语义。
func TestMultiFlag(t *testing.T) {
	var m multiFlag
	for _, v := range []string{"a=1", "b=2"} {
		if err := m.Set(v); err != nil {
			t.Fatalf("Set(%q): %v", v, err)
		}
	}
	if got := strings.Join(m, ","); got != "a=1,b=2" {
		t.Fatalf("multiFlag = %q; want a=1,b=2", got)
	}
}
