package repoguard

// CORE-BACKTEST-OPS-TEXT 反向护栏：回测的排障文案必须指向**真实存在**的入口。
//
// 本护栏钉两件事：
//
//  1. `pkg/backtest/engine.go` 的「交易日历未同步」提示不得再让人去
//     `POST /sync/calendar` —— 该路由已随数据面退役；真实入口是数据服务的
//     `POST /api/sync/jobs` + `job_type=trading_calendar`（同文件尾部
//     覆盖度错误已经是这个口径，两处必须一致）。
//  2. `pkg/backtest/cache/factor_cache.go` 不得再让人去 `sync/factors/all`
//     —— 因子同步面已退役（ADR-037 D2：因子值由 run 按需求值，不写库）。
//
// 排障文案指错路的代价不是编译错误，而是把人引到 404：运维照提示做，
// 拿不到日历/因子，再回头怀疑回测引擎。静态文本检查在任何环境都有效，
// 不依赖连库或起服务。
//
// 破坏验证（施工时实做）：把任一提示逐字节改回退役路由 ⇒ 本护栏红；
// 逐字节还原后复绿。

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestBacktestOpsHintsPointToLiveEntry(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	engine := readFile(t, filepath.Join(root, "pkg", "backtest", "engine.go"))
	if strings.Contains(engine, "POST /sync/calendar") {
		t.Error("pkg/backtest/engine.go 仍提示 POST /sync/calendar —— 该路由已退役，" +
			"真实入口是 POST /api/sync/jobs + job_type=trading_calendar（CORE-BACKTEST-OPS-TEXT）")
	}
	// 正向对照：同一文件必须仍给出真实入口，防止有人删掉提示了事。
	if !strings.Contains(engine, "POST /api/sync/jobs") || !strings.Contains(engine, "job_type=trading_calendar") {
		t.Error("pkg/backtest/engine.go 的日历排障文案未指向真实入口 " +
			"POST /api/sync/jobs + job_type=trading_calendar（CORE-BACKTEST-OPS-TEXT）")
	}

	factorCache := readFile(t, filepath.Join(root, "pkg", "backtest", "cache", "factor_cache.go"))
	if strings.Contains(factorCache, "sync/factors/all") {
		t.Error("pkg/backtest/cache/factor_cache.go 仍提示 sync/factors/all —— 因子同步面已退役，" +
			"因子值由 run 按需求值（ADR-037 D2），不存在该入口（CORE-BACKTEST-OPS-TEXT）")
	}
	if !strings.Contains(factorCache, "ADR-037") {
		t.Error("pkg/backtest/cache/factor_cache.go 的空表提示未说明因子值已改为按需求值（ADR-037 D2）；" +
			"删掉提示或只写「表为空」会把读者引回「找同步入口」的死路（CORE-BACKTEST-OPS-TEXT）")
	}
}
