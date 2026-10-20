package repoguard

// 结构护栏：价格笼子**保持未接线**（REV-25 冻结）。
//
// 为什么需要它：`pkg/live/price_cage.go` 实现了 A 股申报价格笼子（沪深主板
// ±2%），但 `SetPriceCageValidator` 在生产路径上**零调用方**——`paper_session`
// 接了 `SetCurrentBarProvider`，却没接笼子。于是「MockTrader 的限价单做过笼子
// 校验」是一个很容易被读出来的错误结论：代码在、测试绿、实现正确，只是没接。
//
// 本仓对这类状态的一贯处理是「显式冻结 + 机械执法」（先例：package_wiring_test
// 的 unwiredPackages、retiredPackages）。价格笼子挂在 pkg/live 内部、不在包级
// 白名单里，所以用本文件单独钉住：
//
//  1. 生产代码里 `SetPriceCageValidator` 只允许出现在两处**定义**（engine.go 的
//     转发、order_manager.go 的实现）——任何第三处生产调用都算「接线了」；
//  2. 一旦真的接线，本测试**变红**，强制接线者回来把 REV-25 的冻结记录
//     （pkg/live/price_cage.go 文件头 + docs/SPEC.md「价格笼子状态」节）改成
//     「已接线」。两处同步是 ODR-067 的纪律：授权/状态与文档相反时必须两处一起改。
//
// 这不是「不许接线」的禁令——是「接线必须是一次显式动作，且文档跟着改」。
//
// 破坏验证（本单已做）：在 `pkg/live/paper_session.go` 里加一行
// `cbs.SetPriceCageValidator(...)` → 本测试红；删除 → 绿。

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// priceCageAllowedFiles 是允许出现 `SetPriceCageValidator` 的**生产文件**
// （相对仓库根，正斜杠）。只列定义处：接线点不在其中，所以新增接线 = 红。
var priceCageAllowedFiles = map[string]string{
	"pkg/live/engine.go":        "定义 + 向 OrderManager 的转发（接线时这里是合法入口）",
	"pkg/live/order_manager.go": "OrderManager 侧的 setter 实现与撮合前校验",
}

// TestPriceCageStaysUnwired 断言生产代码里没有第三处 SetPriceCageValidator。
func TestPriceCageStaysUnwired(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")
	var offenders []string

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
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

		rel, relErr := filepath.Rel(root, path)
		require.NoError(t, relErr)
		rel = filepath.ToSlash(rel)

		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		// 只认**调用/定义形态**（标识符后紧跟括号）。冻结说明的散文里会出现
		// 反引号包起来的 `SetPriceCageValidator`（无括号）——那是文档，不是接线。
		if !strings.Contains(string(raw), "SetPriceCageValidator(") {
			return nil
		}
		if _, ok := priceCageAllowedFiles[rel]; ok {
			return nil
		}
		offenders = append(offenders, rel)
		return nil
	})
	require.NoError(t, err)

	assert.Empty(t, offenders,
		"以下生产文件出现了 SetPriceCageValidator( 调用 —— 说明价格笼子被接线了。\n"+
			"这不是禁止接线：REV-25 把「未接线」冻结成显式状态，接线时必须同步做三件事：\n"+
			"  1. 删掉本文件的断言（或把 offencers 白名单改成新的接线点）；\n"+
			"  2. 改 pkg/live/price_cage.go 文件头的「未接线」块为「已接线」；\n"+
			"  3. 改 docs/SPEC.md「价格笼子（Price Cage）状态」一节。\n"+
			"两处文档同步是 ODR-067 的纪律。\n%s",
		strings.Join(offenders, "\n"))
}

// TestPriceCageFreezeRecordStaysHonest 断言冻结记录本身还在。
//
// 为什么连文档一起钉：本护栏的语义是「未接线 + 写明未接线」。只钉代码不钉文档，
// 会出现「注释被人顺手删掉、测试照样绿」的状态——那时读者又回到最初的误解里。
func TestPriceCageFreezeRecordStaysHonest(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")
	code, err := os.ReadFile(filepath.Join(root, "pkg", "live", "price_cage.go"))
	require.NoError(t, err)
	assert.Contains(t, string(code), "未接线（REV-25 冻结",
		"price_cage.go 文件头的冻结说明被删了——读者会误以为笼子已生效")
	assert.Contains(t, string(code), "实盘接入前必须接",
		"冻结说明必须写明「实盘接入前必须接」这个前置条件")

	spec, err := os.ReadFile(filepath.Join(root, "docs", "SPEC.md"))
	require.NoError(t, err)
	assert.Contains(t, string(spec), "价格笼子（Price Cage）状态：**未接线**",
		"docs/SPEC.md 的价格笼子状态节被删了——与代码注释必须两处同步（ODR-067）")
}
