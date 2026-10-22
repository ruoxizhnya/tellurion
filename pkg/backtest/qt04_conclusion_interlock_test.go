package backtest

// QT-04 结论的文档互锁。
//
// 为什么要有它：QT-04 的交付物是**一个分支选择**（trap / 合并），它写在两处 ——
// 扫描报告 `docs/test-cases/QT04_DUPLICATE_SIGNAL_SCAN.md`（由 `qt04scan` 脚手架从
// 真数据里量出来）和设计集 `docs/design/qt-decoupling/p0-round2-review.md` §8.3（人和
// QT-21 读的那一句裁决）。两处各写各的，迟早漂移；而漂了的那一份会直接决定 QT-21
// 实现哪条规则 —— 选错的代价是「14 个内置策略当场全挂」或「契约里躺一条永不触发的规则」。
//
// 这条测试不参与扫描，只读三份已提交的文件，把「证据说的」和「裁决记的」钉成同一个字面量。
// 改裁决必须同时改证据（重跑扫描器），改证据必须同时改裁决 —— 单边改动当场红。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	qt04ScanReportRel   = "docs/test-cases/QT04_DUPLICATE_SIGNAL_SCAN.md"
	qt04Round2ReviewRel = "docs/design/qt-decoupling/p0-round2-review.md"
	qt04LedgerRel       = "docs/TASKS.md"

	// qt04ExpectedBranch 是扫描器本次量出来的分支。它同时出现在报告与 §8.3 里，
	// 两边必须逐字相同 —— 写成常量就是为了让人**只能**改一处，而那一处必须与证据一致。
	qt04ExpectedBranch = "merge"
)

func qt04ReadRepoFile(t *testing.T, rel string) string {
	t.Helper()
	p := filepath.Join(append([]string{"..", ".."}, strings.Split(rel, "/")...)...)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读 %s 失败（QT-04 的结论互锁需要这三份文件都在）: %v", rel, err)
	}
	return string(raw)
}

func TestQT04_ConclusionInterlock_ScanReportDesignDocLedger(t *testing.T) {
	report := qt04ReadRepoFile(t, qt04ScanReportRel)
	review := qt04ReadRepoFile(t, qt04Round2ReviewRel)
	ledger := qt04ReadRepoFile(t, qt04LedgerRel)

	// 腿 1：报告里必须有「由测量决定的分支」，且与期望值一致。
	branchLine := ""
	for _, ln := range strings.Split(report, "\n") {
		if strings.Contains(ln, "分支 = `") {
			branchLine = strings.TrimSpace(ln)
			break
		}
	}
	if branchLine == "" {
		t.Fatal(qt04ScanReportRel + " 里没有「分支 = `…`」那一条 —— 报告必须由扫描器生成，" +
			"手写一份没有分支的 markdown 不算存量扫描")
	}
	if !strings.Contains(branchLine, "分支 = `"+qt04ExpectedBranch+"`") {
		t.Errorf("扫描报告的分支不是 %q（该行：%s）—— 改分支要重跑 `go test -tags qt04scan`，"+
			"不是改文本", qt04ExpectedBranch, branchLine)
	}

	// 报告必须自带可复现命令，否则「证据」不可复查。
	if !strings.Contains(report, "-tags qt04scan") {
		t.Errorf("%s 缺可复现命令（要含 `-tags qt04scan`）—— ⑤ 的判据是「报告存在且含可复现命令」",
			qt04ScanReportRel)
	}

	// 腿 2：设计集 §8.3 必须把勾选结果写下来，且与报告的分支同一字面量。
	if !strings.Contains(review, "QT-04 已扫") {
		t.Errorf("%s 的 §8.3 没写「QT-04 已扫」那一步 —— 裁决表要记录扫描做过与结论落点，"+
			"不然 §8.3 读起来还像「二选一待定」", qt04Round2ReviewRel)
	}
	if !strings.Contains(review, "branch=`"+qt04ExpectedBranch+"`") {
		t.Errorf("%s 的 §8.3 没勾到 branch=`%s`，而扫描报告量出来的是 %s —— 两处必须同源；"+
			"要改裁决就重跑扫描器（§8.3 步 2=trap / 步 3=merge）",
			qt04Round2ReviewRel, qt04ExpectedBranch, qt04ExpectedBranch)
	}

	// 腿 3：QT-21 是这条结论的执行者 —— 它的派工文本必须已经带上落点，
	// 否则下一个 agent 拿到的还是「二选一待定」，结论等于没落档。
	qt21Start := strings.Index(ledger, "### QT-21")
	if qt21Start < 0 {
		t.Fatalf("%s 里找不到 QT-21 条目 —— 互锁的落点没了，先恢复台账再谈结论落档", qt04LedgerRel)
	}
	qt21 := ledger[qt21Start:]
	if end := strings.Index(qt21, "### QT-22"); end > 0 {
		qt21 = qt21[:end]
	}
	if !strings.Contains(qt21, "QT04_DUPLICATE_SIGNAL_SCAN.md") {
		t.Errorf("QT-21 的 ②上下文没引扫描报告 —— QT-04 的结论要交给它执行，"+
			"指针不落在派工文本里就等于没交出去（%s）", qt04LedgerRel)
	}
	if strings.Contains(qt21, "trap 或 `close` 优先合并") {
		t.Errorf("QT-21 的 ③-③ 还写着「trap 或 close 优先合并」这个二选一 —— " +
			"QT-04 已经把它量成一条规则了，派工文本不能还停在待定")
	}

	// 腿 4：QT-04 自己的台账条目要引到证据，并点名合并规则的可见性。
	if !strings.Contains(ledger, "QT04_DUPLICATE_SIGNAL_SCAN.md") {
		t.Errorf("%s 的 QT-04 条目没引用扫描报告 —— 结论落档必须带着证据指针", qt04LedgerRel)
	}
	if !strings.Contains(ledger, "duplicate_signals_merged") {
		t.Errorf("%s 的 QT-04 结论没写诊断计数名 `duplicate_signals_merged` —— "+
			"那是 §8.3 步 3 要求的可见性，QT-21 照台账实现", qt04LedgerRel)
	}
}
