package repoguard

// QT-52 的文档护栏：**结论必须留在能被查到的位置**，且不许退回「待查」。
//
// 为什么值得一个测试：BE-11 在问题登记本里躺了很久，原因不是没人看，而是它
// 只有「文件命中统计」这种看起来像已核实的形状 —— 文件命中数不会错，但它也
// 不回答任何问题。本单的产出是**结论**，结论的唯一载体是 markdown；没有护栏时，
// 一次「整理文档」就可能把六类结论表删掉或把 BE-11 改回旧文案，而代码与 CI 全绿。
//
// 判据刻意做成结构性的（按表头定位结论表），不是「全文包含某个词」：
// 后者在正文任何一句提到「部分」时都会假通过。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	marketFrictionAuditPath = "docs/design/qt-decoupling/market-friction-audit.md"
	backtestReviewPath      = "docs/design/qt-decoupling/backtest-engine-review.md"
)

// frictionCategories 是 QT-52 ② 点名的六类，顺序与报告结论表一致。
var frictionCategories = []string{"停牌", "成交量约束", "复权", "分红送股", "涨跌停", "集合竞价"}

// verdictTokens 是三档结论的字面量。缺一档就说明判定口径被改宽了。
var verdictTokens = []string{"完备", "部分", "缺失"}

func TestMarketFrictionAudit_HasAVerdictForEveryCategory(t *testing.T) {
	body := readRepoFile(t, marketFrictionAuditPath)

	rows := conclusionTableRows(t, body)
	require.NotEmpty(t, rows,
		"%s 找不到「| # | 摩擦 | 结论 |」结论表 —— BE-11 的六类结论没有汇总处", marketFrictionAuditPath)

	for _, category := range frictionCategories {
		line, ok := rowForCategory(rows, category)
		if !assert.Truef(t, ok, "结论表里缺 %q 一行 —— 六类里少一类，BE-11 就只结了一半", category) {
			continue
		}
		assert.Truef(t, hasVerdict(line),
			"%q 一行没有「完备 / 部分 / 缺失」结论 —— 只写现象不写判定，等于没核实：%s", category, line)
	}
}

// TestBacktestReview_BE11NoLongerSaysPending 钉住「结论替换待查文案」这件事本身。
//
// 「仅统计到文件命中」是核实**之前**的措辞。它一旦回来，读者就会以为 BE-11 又
// 变成未查状态，而实际上结论仍在另一份文件里 —— 两份文档互相矛盾比只留一份更糟。
func TestBacktestReview_BE11NoLongerSaysPending(t *testing.T) {
	body := readRepoFile(t, backtestReviewPath)

	var row string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "| BE-11 |") {
			row = line
			break
		}
	}
	require.NotEmpty(t, row, "BE-11 行被删了 —— 结论必须留在问题登记本里，否则读 BE 清单的人会以为它没查")
	assert.NotContains(t, row, "仅统计到文件命中",
		"BE-11 退回了核实前的「待查」文案 —— QT-52 的结论被覆盖了")
	assert.Contains(t, row, "market-friction-audit.md",
		"BE-11 必须指向结论文件，否则结论不可追溯（grep 类名找不到第二次）")
}

// ─── 解析与读取 ─────────────────────────────────────────────────

// conclusionTableRows 返回结论表的数据行（表头之下、空行之前的 `|` 行）。
func conclusionTableRows(t *testing.T, body string) []string {
	t.Helper()
	lines := strings.Split(body, "\n")
	start := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "|") &&
			strings.Contains(trimmed, "摩擦") && strings.Contains(trimmed, "结论") {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	var rows []string
	for _, line := range lines[start+1:] {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "|") {
			break
		}
		// 分隔行（|---|）不是数据。
		if strings.Trim(strings.Trim(trimmed, "|"), "-: ") == "" {
			continue
		}
		rows = append(rows, trimmed)
	}
	return rows
}

// rowForCategory 在结论表里找「某个摩擦」那一行。匹配单元格而不是全文：
// 正文里的「停牌日」不该被当成结论表的一行。
func rowForCategory(rows []string, category string) (string, bool) {
	for _, row := range rows {
		for _, cell := range strings.Split(strings.Trim(row, "|"), "|") {
			if strings.TrimSpace(cell) == category {
				return row, true
			}
		}
	}
	return "", false
}

func hasVerdict(line string) bool {
	for _, token := range verdictTokens {
		if strings.Contains(line, token) {
			return true
		}
	}
	return false
}

// readRepoFile 从 repoguard 包目录读仓内文件（该包在 internal/repoguard）。
func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
	require.NoErrorf(t, err, "读取 %s 失败 —— 测试必须在仓库内运行", rel)
	return string(raw)
}
