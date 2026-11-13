package bootstrap

// dataservice_test.go —— CORE-DATA-URL-SINGLE-DEFAULT 的回归面。
//
// 两件事必须同时成立：
//  1. 缺配置 ⇒ 明确错误（fail-closed），绝不回落某个猜的地址；
//  2. 仓内只有这一个解析入口 —— 任何非测试 .go 里再出现
//     `localhost:8081` / `data-service:8081` 字面量，都说明「第二套默认值」
//     又长回来了（破坏验证正是把旧的回落写回去应当变红）。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDataServiceURL(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{name: "explicit", value: "http://data-service:8081", want: "http://data-service:8081"},
		{name: "explicit with trailing slash", value: "http://127.0.0.1:8081/", want: "http://127.0.0.1:8081"},
		{name: "explicit https", value: "https://data.example.com", want: "https://data.example.com"},
		{name: "missing is an error", value: "", wantErr: true},
		{name: "whitespace is missing", value: "   ", wantErr: true},
		{name: "relative is an error", value: "data-service:8081", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := viper.New()
			if tc.value != "" {
				v.Set("data_service.url", tc.value)
			}
			got, err := DataServiceURL(v)
			if tc.wantErr {
				require.Error(t, err)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestBuildBacktestEngineFailsClosedOnMissingDataServiceURL 守住装配腿：
// 缺 data_service.url 时返回错误而不是构造一个指向 localhost 的引擎。
func TestBuildBacktestEngineFailsClosedOnMissingDataServiceURL(t *testing.T) {
	t.Parallel()

	engine, provider, err := BuildBacktestEngine(viper.New(), nil, zerolog.Nop())
	require.Error(t, err)
	assert.Nil(t, engine)
	assert.Nil(t, provider)
	assert.Contains(t, err.Error(), "data_service.url")
}

// TestNoDataServiceDefaultLiterals 是结构性护栏：扫描仓内非测试 Go 源码的
// 字符串字面量，禁止再出现数据服务的硬编码基址。地址只允许来自配置。
func TestNoDataServiceDefaultLiterals(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")
	forbidden := []string{"localhost:8081", "data-service:8081"}

	var violations []string
	fset := token.NewFileSet()
	for _, dir := range []string{"pkg", "cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				return perr
			}
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				val, uerr := strconv.Unquote(lit.Value)
				if uerr != nil {
					return true
				}
				for _, banned := range forbidden {
					if strings.Contains(val, banned) {
						rel, _ := filepath.Rel(root, path)
						violations = append(violations, rel+":"+fset.Position(lit.Pos()).String())
					}
				}
				return true
			})
			return nil
		})
		require.NoError(t, err)
	}

	assert.Empty(t, violations,
		"数据服务基址只能从配置读取（bootstrap.DataServiceURL）；这些位置又写回了硬编码默认值：%v", violations)
}
