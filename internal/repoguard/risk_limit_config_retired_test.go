package repoguard

// SVC-CORE-RISK-RETIRE 反向护栏：core 的限额真相源已迁到 tellurion-risk 的
// 规则快照（`risk.PolicyFromSnapshot`），`config/*.yaml` 不再持有任何限额。
//
// 本护栏钉三件事：
//
//  1. 全仓非测试 Go 文件里**零** `v.Get*("risk_manager.` —— 14 个限额标量的
//     配置读取器（`pkg/bootstrap.RiskManagerConfigFromConfig`）已删除。
//  2. `config/*.yaml` 里**零**限额键 —— 不只是 `risk_manager:` 段，任何
//     `max_position_weight:` / `atr_period:` 之类「看起来像限额」的 YAML 键
//     都不许回来（第二真相 = 同一条 run 两个数）。
//  3. 正向对照：`pkg/risk/policy.go` 仍在且含 `DefaultRiskManagerConfig`；
//     `config/analysis-service.yaml` 的 `resolve_enabled` 是 `true` ——
//     防「把兜底也删了」造成的假绿。
//
// 破坏验证（2026-11-09 施工时实做）：往 `pkg/bootstrap` 加回一行
// `v.GetFloat64("risk_manager.target_volatility")` ⇒ 红；往
// `config/analysis-service.yaml` 加回 `max_position_weight: 0.10` ⇒ 红；
// 逐字节还原后复绿。

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestRiskLimitConfigIsRetired(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)

	// ① 配置读取器：全仓零 `risk_manager.` 读点。
	readRe := regexp.MustCompile(`v\.Get\w*\("risk_manager\.`)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == "web" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src := stripGoLineComments(readFile(t, path))
		if loc := readRe.FindStringIndex(src); loc != nil {
			rel, _ := filepath.Rel(root, path)
			line := 1 + strings.Count(src[:loc[0]], "\n")
			t.Errorf("%s:%d 仍读 risk_manager.* 配置 —— "+
				"限额真相源是 risk 服务快照（pkg/risk.PolicyFromSnapshot）；"+
				"未接入时用 risk.BuiltinPolicy() 代码常量（SVC-CORE-RISK-RETIRE）", rel, line)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历仓库失败：%v", err)
	}

	// ② config/*.yaml 零限额键（键名按 risk 仓参数字典 + 旧 YAML 的并集）。
	limitKey := regexp.MustCompile(`(?m)^\s*(risk_manager|target_volatility|max_position_weight|` +
		`min_position_weight|stoploss|take_profit|volatility|regime|atr_period|base_multiplier|` +
		`bull_multiplier|bear_multiplier|sideways_multiplier|atr_multiplier|lookback_days|` +
		`annualization_factor|fast_ma_period|slow_ma_period|vol_lookback|max_sector_weight|` +
		`max_drawdown|daily_loss_limit|failure_rate_limit)\s*:`)
	yamlDir := filepath.Join(root, "config")
	cfgs, err := filepath.Glob(filepath.Join(yamlDir, "*.yaml"))
	if err != nil {
		t.Fatalf("列举 config/*.yaml 失败：%v", err)
	}
	if len(cfgs) == 0 {
		t.Fatalf("config/*.yaml 一个都没找到 —— 护栏本身失效")
	}
	for _, path := range cfgs {
		src := readFile(t, path)
		if loc := limitKey.FindStringIndex(src); loc != nil {
			line := 1 + strings.Count(src[:loc[0]], "\n")
			rel, _ := filepath.Rel(root, path)
			t.Errorf("%s:%d 出现限额键 —— 限额真相源在 risk 快照，config 不得再持有"+
				"（SVC-CORE-RISK-RETIRE）", rel, line)
		}
	}

	// ③ 正向对照：换算入口与「出厂接入」状态仍在。
	policy := readFile(t, filepath.Join(root, "pkg", "risk", "policy.go"))
	if !strings.Contains(policy, "func DefaultRiskManagerConfig()") {
		t.Errorf("pkg/risk/policy.go 里找不到 DefaultRiskManagerConfig —— " +
			"兜底常量被删意味着未接入部署无法启动；若确已改判，请同批订正本护栏")
	}
	if !strings.Contains(policy, "func PolicyFromSnapshot(") {
		t.Errorf("pkg/risk/policy.go 里找不到 PolicyFromSnapshot —— 真相源入口不见了")
	}
	analysisCfg := readFile(t, filepath.Join(yamlDir, "analysis-service.yaml"))
	if !regexp.MustCompile(`(?m)^\s*resolve_enabled:\s*true\s*$`).MatchString(analysisCfg) {
		t.Errorf("config/analysis-service.yaml 的 resolve_enabled 不是 true —— " +
			"出厂必须接入快照（SVC-CORE-RISK-RETIRE ③）；若确要退回 false，请同批订正本护栏与 TASKS")
	}
}
