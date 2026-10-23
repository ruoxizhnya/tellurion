package repoguard

// 结构护栏：引擎装配必须带 `marketdata.Router`（QT-64）。
//
// 为什么需要它：`Engine.SetRouter` 在 QT-64 之前零生产调用点，D6 日内
// 预热在所有回测路径上静默不生效 —— 代码在、测试绿、策略却拿不到分钟
// 快照，且没有任何一层报错。接线本身很容易在后续重构里被「顺手删掉」
// （它不参与编译期契约，删了照样 build），所以用一条机械判据钉住：
//
//  1. 进程内唯一的 Router 由 `pkg/kernel.NewMarketDataRouter` 构造
//     （复用 store 池，不新开池）；
//  2. 主引擎（`pkg/kernel/modules_analysis.go`）与 walk-forward 窗口
//     工厂（`cmd/analysis/main.go`）都经 `AttachMarketDataRouter` 这
//     **同一个装配函数**接线；
//  3. 生产代码里 `SetRouter(` 只允许出现在定义处（`pkg/backtest/intraday.go`、
//     `pkg/backtest/options.go`）与这唯一的装配函数里 —— 第三处直连
//     SetRouter 就是第二真相，必须回来改本护栏而不是绕过它。
//
// 破坏验证（QT-64 施工时实跑）：删掉 modules_analysis.go 的
// AttachMarketDataRouter 调用 → 本测试红；在 cmd/analysis 里另写一处
// `eng.SetRouter(...)` → 本测试红；逐字节还原后绿。

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// routerAttachAllowedFiles 是允许出现 `SetRouter(` 调用/定义形态的生产文件。
// 只列定义处与唯一装配函数：新增接线点必须改这里，改这里就得先想清楚
// 为什么不是经 AttachMarketDataRouter。
var routerAttachAllowedFiles = map[string]string{
	"pkg/backtest/intraday.go":        "SetRouter 定义（引擎侧 setter）",
	"pkg/backtest/options.go":         "WithRouter 选项转发到 SetRouter",
	"pkg/kernel/marketdata_wiring.go": "唯一装配函数 AttachMarketDataRouter（主引擎与窗口引擎共用）",
}

// TestEngineAssemblyCarriesMarketDataRouter 钉住三个装配点与唯一构造点。
func TestEngineAssemblyCarriesMarketDataRouter(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")

	read := func(rel string) string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		require.NoError(t, err, "读取 %s 失败", rel)
		return string(raw)
	}

	// ① 唯一构造点：复用 store 的连接池，不新开池。
	wiring := read("pkg/kernel/marketdata_wiring.go")
	assert.Contains(t, wiring, "func NewMarketDataRouter(",
		"进程内唯一的 Router 构造点必须留在 pkg/kernel/marketdata_wiring.go（QT-64 ③-①）")
	assert.Contains(t, wiring, "marketdata.NewRouter(store.DB())",
		"Router 必须复用 store 的连接池（一个进程一个池，不新开池）")

	// ② 主引擎装配带 Router：strategy-runtime 的 Init 经同一装配函数接线。
	modules := read("pkg/kernel/modules_analysis.go")
	assert.Contains(t, modules, "AttachMarketDataRouter(engine, d.Router, m.logger)",
		"主引擎装配丢了 Router 接线 —— D6 日内预热会静默不生效（QT-64）")

	// ③ walk-forward 窗口工厂带 Router：同一个实例、同一个装配函数。
	mainSrc := read("cmd/analysis/main.go")
	assert.Contains(t, mainSrc, "kernel.AttachMarketDataRouter(eng, ak.Wiring.Router, logger)",
		"walk-forward 窗口引擎丢了 Router 接线 —— 窗口内的 IntradayAware 策略会静默无数据（QT-64）")

	// ④ 构造点确实喂给了内核模块（Router 实例只有一份）。
	kernelWiring := read("cmd/analysis/kernel_wiring.go")
	assert.Contains(t, kernelWiring, "kernel.NewMarketDataRouter(store)",
		"cmd/analysis 必须经 NewMarketDataRouter 构造 Router（复用 store 池）")
	assert.Contains(t, kernelWiring, "Router:     mdRouter",
		"构造出的 Router 必须传给 StrategyRuntimeDeps.Router（主引擎拿它）")

	// ⑤ 生产代码里 SetRouter( 只允许出现在白名单文件。
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
		if !strings.Contains(string(raw), "SetRouter(") {
			return nil
		}
		if _, ok := routerAttachAllowedFiles[rel]; ok {
			return nil
		}
		offenders = append(offenders, rel)
		return nil
	})
	require.NoError(t, err)

	assert.Empty(t, offenders,
		"以下生产文件出现了 SetRouter( 调用 —— 接线必须经 pkg/kernel.AttachMarketDataRouter，"+
			"否则会有第二条装配真相（QT-64 ③-②）：\n%s",
		strings.Join(offenders, "\n"))
}
