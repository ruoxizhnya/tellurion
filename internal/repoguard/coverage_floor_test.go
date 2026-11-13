package repoguard

// CORE-COVERAGE-FLOOR（2026-11-13）的结构护栏：覆盖率地板必须真的执法，
// 且三处（CI、Makefile、AGENTS.md）不能分叉。
//
// 缺陷现场：REV-21 设的阈值是 52 / 65，而 2026-11-12 起基线口径（DSN 打开、
// DB 腿真跑）实测就有 72.5 / 82.5 —— 门禁比实测低 20 点，等于只是个摆设：
// 成片删测试后总量掉到 59.4% 仍然 PASS（已实测）。
//
// 判据：
//  1. ci.yml 的 Test 步与 Makefile 的 test 目标都带 `-total-min` / `-pkg-min`，
//     且**数值逐字相同**（历史上就是这么分叉的：Makefile 无门禁）；
//  2. 现值不得低于冻结地板（67 / 77）—— 只许抬、不许偷偷降；
//  3. AGENTS.md §8 必须写出同一组数字（避免「只活在命令行里」）；
//  4. 阈值必须来自「实测 - 5」的零头而不是整十：这里用 67 而非 60/65 的
//     具体性做不了机械判定，改由注释里的实测基线 + 本文件的冻结值共同钉住。
//
// 为什么用冻结值而不是「重算实测」：本护栏跑在普通单测里，没有 DSN 也没有
// 隔离库，现场算出来的覆盖率（DB 腿全 skip）不能用来判门禁。冻结值 + 注释
// 里的实测出处是这里能给出的最强约束；基线更新时同批改这三个文件。

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// 2026-11-13 隔离 PG 16（迁移后、-p 1 串行）实测基线 - 5 点取整。
// 只许抬：降值必须改这里并写清为什么（不让它悄悄滑回去）。
const (
	minTotalCov      = 67.0
	minBacktestCov   = 77.0
	backtestPkgName  = "tellurion/pkg/backtest"
	coverageGateTool = "tools/coveragegate"
)

var (
	totalMinRe    = regexp.MustCompile(`-total-min=([0-9]+(?:\.[0-9]+)?)`)
	backtestMinRe = regexp.MustCompile(`-pkg-min=` + regexp.QuoteMeta(backtestPkgName) + `=([0-9]+(?:\.[0-9]+)?)`)
)

func mustFloat(t *testing.T, raw, where string) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		t.Fatalf("%s 里的阈值 %q 不是数字: %v", where, raw, err)
	}
	return v
}

// extractThresholds 从一段文本里取 total / backtest 两个阈值。
func extractThresholds(t *testing.T, text, where string) (total, backtest float64) {
	t.Helper()
	tm := totalMinRe.FindStringSubmatch(text)
	if tm == nil {
		t.Fatalf("%s 缺 `-total-min=` —— 覆盖率门禁没有总量下限（CORE-COVERAGE-FLOOR）", where)
	}
	bm := backtestMinRe.FindStringSubmatch(text)
	if bm == nil {
		t.Fatalf("%s 缺 `-pkg-min=%s=` —— 回测包的下限没了", where, backtestPkgName)
	}
	return mustFloat(t, tm[1], where), mustFloat(t, bm[1], where)
}

// coverageGateStep 返回 ci.yml 里调用 coveragegate 的 Test 步（含 env）。
func coverageGateStep(t *testing.T, root string) ciStep {
	t.Helper()
	wf := loadCIWorkflow(t, root)
	for _, j := range wf.Jobs {
		for _, s := range j.Steps {
			if strings.Contains(s.Run, coverageGateTool) {
				return s
			}
		}
	}
	t.Fatalf("ci.yml 里没有调用 %s 的步骤 —— 覆盖率门禁整条消失了", coverageGateTool)
	return ciStep{}
}

func TestCoverageFloorIsEnforced(t *testing.T) {
	root := repoRoot(t)

	ciStep := coverageGateStep(t, root)
	ciTotal, ciBacktest := extractThresholds(t, ciStep.Run, "ci.yml 的 coveragegate 步")

	mk, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatalf("读 Makefile 失败: %v", err)
	}
	mkTotal, mkBacktest := extractThresholds(t, string(mk), "Makefile 的 test 目标")

	// 判据 1：同一份阈值（改就两边一起改）。
	if ciTotal != mkTotal {
		t.Errorf("总量阈值分叉：ci.yml=%v、Makefile=%v —— 「改就两边一起改」是 REV-21 的明文约定", ciTotal, mkTotal)
	}
	if ciBacktest != mkBacktest {
		t.Errorf("%s 阈值分叉：ci.yml=%v、Makefile=%v", backtestPkgName, ciBacktest, mkBacktest)
	}

	// 判据 2：只许抬、不许降。
	if ciTotal < minTotalCov {
		t.Errorf("总量阈值 %v 低于冻结地板 %v —— CORE-COVERAGE-FLOOR 抬起来的那一档又被降回去了", ciTotal, minTotalCov)
	}
	if ciBacktest < minBacktestCov {
		t.Errorf("%s 阈值 %v 低于冻结地板 %v —— 回测包的下限被降回去了", backtestPkgName, ciBacktest, minBacktestCov)
	}

	// 判据 3：AGENTS.md 里必须有同一组数字（避免门禁只活在命令行里）。
	agents, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil {
		t.Fatalf("读 AGENTS.md 失败: %v", err)
	}
	doc := string(agents)
	for _, want := range []string{
		"-total-min=" + strconv.FormatFloat(ciTotal, 'f', -1, 64),
		"-pkg-min=" + backtestPkgName + "=" + strconv.FormatFloat(ciBacktest, 'f', -1, 64),
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("AGENTS.md §8 没有写出 %q —— 阈值必须与 CI/Makefile 同文（改三处一起改）", want)
		}
	}
}
