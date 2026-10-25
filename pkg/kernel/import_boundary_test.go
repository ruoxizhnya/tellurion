package kernel_test

// QT-50 ①/⑥ 护栏：kernel 是**纯装配机制**，非测试 import 里不许出现业务族。
//
// 为什么需要它：QT-50 施工前 `pkg/kernel` 直接 import `pkg/backtest` / `pkg/live` /
// `pkg/risk` / `pkg/marketdata` / `pkg/storage`（modules_analysis.go 与
// marketdata_wiring.go）——于是「内核」等于把整个业务面拖进了框架包，别的应用
// 复用 kernel 就得同时拖上回测引擎。装配属应用层（`cmd/*/wiring`），框架包只留
// Module / BootOrder / 生命周期 / 槽位。
//
// 判据是**非测试 import**（`_test.go` 可以引用任何东西来验证契约）。框架自身
// 依赖的 clock / msgbus / eventstore 不在此列：它们是 Kernel 接口的返回类型
// （`Kernel.Clock/Bus/Store`），不是业务族。
//
// 破坏验证：把 `tellurion/pkg/backtest` 加回任一非测试文件的 import → 本测试红。

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// businessFamilyPrefixes 是业务族的 import 前缀（前缀匹配覆盖子包，例如
// `tellurion/pkg/backtest/tracker`）。清单口径：本仓 `pkg/` 下承担业务规则的包，
// 与 repo-topology「kernel 只留框架」一致。新增业务包时把它加进来——漏加的
// 后果是护栏失明，不是误报。
var businessFamilyPrefixes = []string{
	"tellurion/pkg/alert",
	"tellurion/pkg/backtest",
	"tellurion/pkg/compliance",
	"tellurion/pkg/data",
	"tellurion/pkg/execalgo",
	"tellurion/pkg/execution",
	"tellurion/pkg/expression",
	"tellurion/pkg/indicator",
	"tellurion/pkg/live",
	"tellurion/pkg/marketdata",
	"tellurion/pkg/marketimpact",
	"tellurion/pkg/mining",
	"tellurion/pkg/portfolio",
	"tellurion/pkg/risk",
	"tellurion/pkg/settlement",
	"tellurion/pkg/storage",
	"tellurion/pkg/strategy",
}

// frameworkAllowedExact 是上面清单里**必须放行**的精确 import：它们出现在
// Kernel 接口签名里（`Clock() clock.Clock` / `Bus() msgbus.MsgBus` /
// `Store() eventstore.EventStore`），是框架契约的一部分，不是业务依赖。
var frameworkAllowedExact = map[string]string{
	"github.com/ruoxizhnya/tellurion-kit/clock": "Kernel.Clock 的返回类型（替换点①）",
	"tellurion/pkg/msgbus":                      "Kernel.Bus 的返回类型 + kernel.boot/shutdown 发布通道",
	"tellurion/pkg/eventstore":                  "Kernel.Store 的返回类型 + 生命周期事件落库",
}

func TestKernelImportsNoBusinessFamily(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读取 pkg/kernel 目录失败: %v", err)
	}

	fset := token.NewFileSet()
	var scanned []string
	var offenders []string
	var sawFrameworkImport bool

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(".", name)
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("解析 %s 失败（护栏不能跳过读不懂的文件）: %v", name, err)
		}
		scanned = append(scanned, name)
		for _, spec := range file.Imports {
			imp, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("%s: import 路径 %s 不是合法字符串: %v", name, spec.Path.Value, err)
			}
			if _, ok := frameworkAllowedExact[imp]; ok {
				sawFrameworkImport = true
				continue
			}
			for _, prefix := range businessFamilyPrefixes {
				if imp == prefix || strings.HasPrefix(imp, prefix+"/") {
					offenders = append(offenders, name+" → "+imp)
					break
				}
			}
		}
	}

	// 非盲：必须真的扫到了本包的非测试文件与一条框架依赖，否则「没有违规」
	// 可能只是「什么都没读到」。
	sort.Strings(scanned)
	if len(scanned) == 0 {
		t.Fatal("pkg/kernel 下没有扫到任何非测试 .go 文件 —— 护栏失明")
	}
	for _, want := range []string{"adapters.go", "interfaces.go", "kernel.go"} {
		if !containsString(scanned, want) {
			t.Fatalf("扫描覆盖缺 %s（scanned=%v）—— 覆盖面声明是假的", want, scanned)
		}
	}
	if !sawFrameworkImport {
		t.Fatal("没有识别到 clock/msgbus/eventstore 任一框架 import —— 解析逻辑瞎了")
	}

	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Fatalf("pkg/kernel 的非测试 import 里出现业务族（装配属应用层，应放 cmd/*/wiring；QT-50 ①）:\n%s",
			strings.Join(offenders, "\n"))
	}
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
