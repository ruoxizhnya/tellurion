package repoguard

// CORE-DEAD-PROXY-SCREEN-VERIFY 的静态护栏：三条「已退役上游」不许以任何形式
// 回到**网关**生产代码里。
//
// 2026-11-12 的实测（判据见 cmd/analysis/dead_proxy_screen_test.go 的文件头）：
// 修复前 `POST /api/screen` 被反代到 data 的 `/screen` —— 该路由在 data 的兼容
// 面里**从来没有存在过**，于是选股恒失败；`GET /api/stocks/count` 同样打到
// 不存在的 `/stocks/count`；`POST /api/ingest/raw` 连路由都没注册。
//
// 行为测试能钉住「今天这三个请求不回上游」，钉不住「明天第 N 个 handler 又把
// 反代拼回去」——而复发恰恰最可能发生在新增一条同款路由时。所以这里用**静态**
// 判据：凡是以字符串拼接指向 `/screen`、`/stocks/count`、`/ingest/raw` 的上游
// URL，出现即红。
//
// 为什么按「+ "/path"」而不是按「出现的字符串」判：`registerScreenRoutes` 里
// 合法的本地路由是字面量 `"/api/screen"`，整个字符串匹配会把它一起判红，
// 护栏就会在它自己的正当用途上失效（AUD-29 同款教训）。
//
// 边界：只看源码文本的拼接形状，不做数据流分析。换成常量再拼接的复发不在此列
// ——那属于刻意绕过，由 code review 兜底。
//
// 破坏验证（施工时实做）：在 handlers_proxy.go 加回一行
// `proxyRequest(c, http.MethodPost, dataServiceURL+"/screen", nil)` ⇒ 本护栏红；
// 删除后逐字节还原，复绿。

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// retiredUpstreamAllowlist 是**缺陷台账**，不是许可：
//
//	存在条目 = 该文件仍会打已退役路由，且改法超出「网关死反代」的边界；
//	修好之后必须删掉条目（下方的 staleness 断言会强制，留着就红）。
//
// 2026-11-12 已清空：策略插件里 `/screen` 的 HTTP 兜底随
// CORE-STRATEGY-SCREEN-FALLBACK-DEAD-UPSTREAM 裁决（B1）整条删除 ——
// value_screen 多票改为返回可诊断的 contracts.ErrDataGap，multi_factor 只保留
// run 级因子缓存与本地价量两条来源。
var retiredUpstreamAllowlist = map[string]string{}

func TestRetiredUpstreamPathsAreNotProxied(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	// 三种退役路径作为**上游**出现：`xxx+"/screen"` / `xxx + "/stocks/count"` /
	// `xxx+"/ingest/raw"`。注意不匹配 `"/api/screen"` 这类本地路由字面量。
	retiredUpstream := regexp.MustCompile(`\+\s*"(?:/screen|/stocks/count|/ingest/raw)"`)

	scanned := 0
	seen := map[string]bool{}
	for _, dir := range []string{"cmd", "pkg"} {
		base := filepath.Join(root, dir)
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			scanned++
			body := stripGoLineComments(readFile(t, path))
			m := retiredUpstream.FindString(body)
			if m == "" {
				return nil
			}
			if _, allowed := retiredUpstreamAllowlist[filepath.ToSlash(rel)]; allowed {
				seen[filepath.ToSlash(rel)] = true
				return nil
			}
			t.Errorf("%s 仍把请求拼向已退役的上游 %q —— 这三条路由在 data 侧从未存在"+
				"（CORE-DEAD-PROXY-SCREEN-VERIFY）：screen 走本地供数，count 派生自 /stocks，"+
				"ingest/raw 返回 410 机器码", rel, m)
			return nil
		})
		if err != nil {
			t.Fatalf("扫描 %s 失败: %v", dir, err)
		}
	}
	if scanned == 0 {
		t.Fatal("一个生产 .go 都没扫到 —— 遍历瞎了，这条护栏的绿不可信")
	}

	// 台账不许腐烂：允许清单里的文件若已修好，条目必须删掉，否则这份「已知残留」
	// 台账会变成永久豁免。
	for rel, reason := range retiredUpstreamAllowlist {
		if !seen[rel] {
			t.Errorf("retiredUpstreamAllowlist 里的 %s 已经不再拼向退役上游（%s）—— "+
				"删掉这条豁免，别让缺陷台账变成永久许可", rel, reason)
		}
	}

	// 正向对照：三条替代实现必须真实存在。否则「把路由整块删掉」也会让上面的
	// 反向断言通过——那是假绿。
	proxy := stripGoLineComments(readFile(t, filepath.Join(root, "cmd", "analysis", "handlers_proxy.go")))
	screen := stripGoLineComments(readFile(t, filepath.Join(root, "cmd", "analysis", "handlers_screen.go")))

	for _, want := range []struct {
		file string
		name string
		body string
		need string
	}{
		{"handlers_screen.go", "screen 本地路由", screen, `router.POST("/api/screen"`},
		{"handlers_screen.go", "screen 本地供数", screen, "ScreenFundamentals("},
		{"handlers_proxy.go", "count 派生读面", proxy, `dataServiceURL+"/stocks"`},
		{"handlers_proxy.go", "ingest 真实写门", proxy, `dataServiceURL+"/ingest/points"`},
		{"handlers_proxy.go", "ingest/raw 退役壳", proxy, `"ingest_raw_retired"`},
	} {
		if !strings.Contains(want.body, want.need) {
			t.Errorf("缺失正向对照「%s」：cmd/analysis/%s 里找不到 %q —— 删除替代实现会让反向断言假绿",
				want.name, want.file, want.need)
		}
	}

	// 正向对照（策略侧）：删掉 `dataServiceURL + "/screen"` 两条腿不算修好，必须
	// 证明替代路径真的在。value_screen 多票的统一出口是 contracts.ErrDataGap
	// （引擎据此记 data_gap_skipped、终态 invalid），multi_factor 只剩 run 级
	// 因子缓存与本地价量两条来源。
	strategyFiles := map[string]string{
		"pkg/strategy/plugins/value_screen.go": stripGoLineComments(readFile(t,
			filepath.Join(root, "pkg", "strategy", "plugins", "value_screen.go"))),
		"pkg/strategy/plugins/multi_factor.go": stripGoLineComments(readFile(t,
			filepath.Join(root, "pkg", "strategy", "plugins", "multi_factor.go"))),
	}
	for _, want := range []struct {
		file string
		name string
		need string
	}{
		{"pkg/strategy/plugins/value_screen.go", "多票缺数据的可诊断出口",
			"contracts.ErrDataGap"},
		{"pkg/strategy/plugins/value_screen.go", "缺数据点名到表与字段",
			"market.stock_fundamentals"},
		{"pkg/strategy/plugins/multi_factor.go", "本地价量替代路径",
			"generateSignalsFromPriceData("},
		{"pkg/strategy/plugins/multi_factor.go", "run 级因子缓存路径",
			"generateSignalsFromFactorCache("},
	} {
		if !strings.Contains(strategyFiles[want.file], want.need) {
			t.Errorf("缺失正向对照「%s」：%s 里找不到 %q —— 删除替代路径会让反向断言假绿",
				want.name, want.file, want.need)
		}
	}
}
