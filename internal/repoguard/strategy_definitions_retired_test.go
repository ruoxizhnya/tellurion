package repoguard

// SVC-CORE-STRAT-RETIRE 反向护栏：core 的 `strategies` 表与 DB 腿退役是
// **不可逆**的（定义真相源 = tellurion-strategy 的 strategy.strategy_definitions）。
//
// 本护栏钉三件事：
//
//  1. `pkg/strategy` 与 `pkg/storage` 的非测试 Go 文件里，除整行注释外**零**
//     表名 `strategies` 引用 —— 建表 DDL、FROM/INTO/UPDATE/DELETE 都不许回来。
//  2. `pkg/strategy` 的非测试 Go 文件**不 import** `tellurion/pkg/storage`，
//     只放行 `signal_store.go` / `state_store.go`（它们读写的是裁决 U3 明确
//     留在 core 的 `quant.strategy_state` / `quant.external_signals`，不是
//     `strategies` 表）。
//  3. 正向对照：运行时 `pkg/strategy/registry.go` 仍在且含 `DefaultRegistry`
//     （ADR-036 §6「运行时 pkg/strategy 保留」）；`pkg/storage/postgres.go`
//     仍建 `backtest_jobs` / `factor_cache` —— 防「删过头」造成的假绿。
//
// 为什么是静态断言：表已删，无库时「查不到这张表」什么也不证明；SQL 文本
// 是纯事实，静态检查在无库 CI 环境同样有效。
//
// 破坏验证（2026-11-09 施工时实做）：往 `pkg/strategy` 加一行读该表名的
// SQL ⇒ 红；加回 `"tellurion/pkg/storage"` import ⇒ 红；逐字节还原后复绿。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestStrategyDefinitionsPersistenceIsRetired(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	tableRe := regexp.MustCompile(`(?i)(?:FROM|INTO|UPDATE|JOIN|DELETE\s+FROM|CREATE\s+TABLE(?:\s+IF\s+NOT\s+EXISTS)?)\s+strategies\b`)
	literalRe := regexp.MustCompile(`"strategies"`)

	// ① + ② pkg/strategy：零表名；storage import 仅放行 U3 保留的两条状态腿。
	strategyDir := filepath.Join(root, "pkg", "strategy")
	storageImportRe := regexp.MustCompile(`"tellurion/pkg/storage"`)
	storageImportAllowed := map[string]bool{
		"signal_store.go": true, // quant.external_signals（U3：留 core）
		"state_store.go":  true, // quant.strategy_state（U3：留 core）
	}
	for _, name := range nonTestGoFiles(t, strategyDir) {
		src := stripGoLineComments(readFile(t, filepath.Join(strategyDir, name)))
		for _, re := range []*regexp.Regexp{tableRe, literalRe} {
			if loc := re.FindStringIndex(src); loc != nil {
				line := 1 + strings.Count(src[:loc[0]], "\n")
				t.Errorf("pkg/strategy/%s:%d 仍引用 strategies 表名 —— "+
					"策略定义真相源在 tellurion-strategy；core 运行时注册表不得读写该表（SVC-CORE-STRAT-RETIRE）", name, line)
			}
		}
		if storageImportAllowed[name] {
			continue
		}
		if loc := storageImportRe.FindStringIndex(src); loc != nil {
			line := 1 + strings.Count(src[:loc[0]], "\n")
			t.Errorf("pkg/strategy/%s:%d 重新 import 了 tellurion/pkg/storage —— "+
				"DB 腿已退役，运行时注册表不得持有存储依赖（SVC-CORE-STRAT-RETIRE）", name, line)
		}
	}

	// ① pkg/storage：零表名（strategies 表的 DDL 与读写方法均已删除）。
	storageDir := filepath.Join(root, "pkg", "storage")
	for _, name := range nonTestGoFiles(t, storageDir) {
		src := stripGoLineComments(readFile(t, filepath.Join(storageDir, name)))
		for _, re := range []*regexp.Regexp{tableRe, literalRe} {
			if loc := re.FindStringIndex(src); loc != nil {
				line := 1 + strings.Count(src[:loc[0]], "\n")
				t.Errorf("pkg/storage/%s:%d 仍引用 strategies 表名 —— "+
					"该表 owner 是 tellurion-strategy；core 不得再建表或读写（SVC-CORE-STRAT-RETIRE）", name, line)
			}
		}
	}

	// ③ 正向对照：运行时注册表与其它保留表仍在。
	registry := readFile(t, filepath.Join(strategyDir, "registry.go"))
	if !strings.Contains(registry, "var DefaultRegistry = NewRegistry()") {
		t.Errorf("pkg/strategy/registry.go 里找不到 DefaultRegistry —— " +
			"ADR-036 §6 明写运行时 pkg/strategy 保留；若确已整体删除，请同批订正本护栏")
	}
	postgres := stripGoLineComments(readFile(t, filepath.Join(storageDir, "postgres.go")))
	for _, kept := range []string{"backtest_jobs", "factor_cache"} {
		keptRe := regexp.MustCompile(`(?i)CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+` + kept + `\b`)
		if !keptRe.MatchString(postgres) {
			t.Errorf("pkg/storage/postgres.go 里找不到 %s 的 CREATE TABLE —— "+
				"本次只退役 strategies；若确已改判，请同批订正本护栏", kept)
		}
	}

	// ② 负向存在性：DB 腿文件本身不应存在。
	if _, err := os.Stat(filepath.Join(strategyDir, "db.go")); err == nil {
		t.Errorf("pkg/strategy/db.go 仍然存在 —— DB 腿已随 strategies 表退役（SVC-CORE-STRAT-RETIRE）")
	}
}
