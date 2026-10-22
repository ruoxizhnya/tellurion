package guard

// K9 契约护栏：ResultSpec canonical schema 冻结。
//
// 这是「变更须破坏本测试、走变更评审」的执行点（docs/SPEC.md「ResultSpec 冻结」）。
// 三条腿：
//  1. 字段集护栏 —— BacktestResponse 的 JSON 字段名集合（含顺序）与冻结清单
//     逐项相等。增删字段、改 json tag ⇒ 红。破坏腿：随便删一个字段或改 tag。
//  2. status 三值护栏 —— 语义值集合恰好是 {completed, invalid, failed}，
//     常量值与语义一一对应。新增第四种取值 ⇒ 必须显式改这里（= 走评审）。
//  3. 文档互锁 —— docs/SPEC.md 必须覆盖冻结字段集与三值语义。改字段不改
//     文档 ⇒ 红。这条腿的来由：本节前身「Backtest Report Metrics」列着根本
//     不存在的 Profit factor / Turnover —— 文档与实现静默漂移的标本。
//
// 为什么住在 core 的 e2e/guard 而不是契约旁边（QT-05 B1，2026-10-23）：
// 契约本体已迁到 `tellurion-contracts:engine`（包名仍是 `contracts`），但第
// 三条腿读的 docs/SPEC.md 是 **core 的文档**。留在契约仓里那条腿只能核到
// 「文档恰好也抄了一份」，而本仓的口径是 SPEC.md 只有一处真相。代价如实
// 登记：在 contracts 仓改 BacktestResponse 字段，红的地方是本测试。

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	contracts "github.com/ruoxizhnya/tellurion-contracts/engine"
)

// resultSpecPath 相对本测试文件（e2e/guard/）的 SPEC 位置。
const resultSpecPath = "../../docs/SPEC.md"

// frozenResultSpecFields 是 BacktestResponse 的冻结 JSON 字段集，按声明序。
// 这份清单是契约本身：它与 docs/SPEC.md 的字段表一一对应。
var frozenResultSpecFields = []string{
	"id",
	"status",
	"strategy",
	"strategy_git_hash",
	"start_date",
	"end_date",
	"total_return",
	"annual_return",
	"sharpe_ratio",
	"sortino_ratio",
	"max_drawdown",
	"max_drawdown_date",
	"win_rate",
	"total_trades",
	"win_trades",
	"lose_trades",
	"avg_holding_days",
	"calmar_ratio",
	"started_at",
	"completed_at",
	"error",
	"portfolio_values",
	"trades",
	"stock_pool",
	"initial_capital",
	// QT-02（2026-10-21）：成交时点口径随结果返回 —— 缺席 = 未知，
	// 不等于 same_close。变更评审记录见 docs/TASKS.md 的 QT-02 结案段。
	"fill_timing",
	"invalid_reasons",
	"universe_max_size",
	// QT-02：结果自带的已知偏差 Code（same_close ⇒ same_close_fill）。
	"known_bias",
}

// frozenStatusValues 是 status 的冻结语义值集。
var frozenStatusValues = []string{contracts.StatusCompleted, contracts.StatusInvalid, contracts.StatusFailed}

func TestResultSpec_FieldSetFrozen(t *testing.T) {
	typ := reflect.TypeOf(contracts.BacktestResponse{})
	got := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			t.Fatalf("字段 %s 缺少有效 json tag —— ResultSpec 的每个字段都必须可寻址", f.Name)
		}
		got = append(got, name)
	}

	if len(got) != len(frozenResultSpecFields) {
		t.Fatalf("ResultSpec 字段集变了（契约，需走变更评审）:\n got(%d): %v\nwant(%d): %v",
			len(got), got, len(frozenResultSpecFields), frozenResultSpecFields)
	}
	for i := range got {
		if got[i] != frozenResultSpecFields[i] {
			t.Fatalf("ResultSpec 字段集第 %d 项变了（契约，需走变更评审）:\n got: %v\nwant: %v",
				i, got, frozenResultSpecFields)
		}
	}
}

func TestResultSpec_StatusTriStateFrozen(t *testing.T) {
	// 常量与语义值一一对应：改常量值 = 改线上判据，必须走评审。
	want := map[string]string{
		"completed": contracts.StatusCompleted,
		"invalid":   contracts.StatusInvalid,
		"failed":    contracts.StatusFailed,
	}
	if len(frozenStatusValues) != len(want) {
		t.Fatalf("status 语义值集大小变了: %d, want %d（新增取值 = 契约变更）", len(frozenStatusValues), len(want))
	}
	for _, v := range frozenStatusValues {
		if _, ok := want[v]; !ok {
			t.Errorf("status 出现冻结集之外的取值 %q —— 契约变更需走评审", v)
		}
	}
	for semantic, constant := range want {
		if constant != semantic {
			t.Errorf("常量值 %q 与语义 %q 不一致", constant, semantic)
		}
	}

	// 不变量涉及的三个键必须都在字段集里，否则三值语义无处安放。
	for _, key := range []string{"status", "error", "invalid_reasons"} {
		found := false
		for _, f := range frozenResultSpecFields {
			if f == key {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("三值不变量依赖字段 %q，但它不在冻结字段集里", key)
		}
	}
}

func TestResultSpec_SpecDocCoversFrozenFieldSet(t *testing.T) {
	raw, err := os.ReadFile(resultSpecPath)
	if err != nil {
		t.Fatalf("读 docs/SPEC.md 失败（护栏需要文档互锁）: %v", err)
	}
	doc := string(raw)

	if !strings.Contains(doc, "ResultSpec 冻结") {
		t.Fatal("docs/SPEC.md 缺「ResultSpec 冻结」节 —— 契约文本必须在文档里")
	}

	// 字段集只认**该节字段表格首格**里的反引号名，不认全文。
	//
	// 为什么不能全文 Contains（2026-10-23 实测出的空转）：`fill_timing` 在
	// ResultSpec 节里出现 3 次——表格 1 次 + 「成交时点与已知偏差」小节散文
	// 2 次。把表里那一行删掉，全文 Contains 仍然绿，于是这条腿对它声称守住
	// 的东西（「字段表缺 %q」）其实一点牙没有。表格首格允许 `a` / `b` 合并写
	// （start_date/end_date、win_trades/lose_trades、started_at/completed_at
	// 三对就是这么写的），所以按首格内所有反引号名收，不按整格相等。
	fields := specResultSpecFieldNames(doc)
	var missing []string
	for _, key := range frozenResultSpecFields {
		if !fields[key] {
			missing = append(missing, key)
		}
	}
	assert.Empty(t, missing,
		"docs/SPEC.md「ResultSpec 冻结」节的**字段表**缺这些冻结字段（只认同节表格首格，"+
			"散文里提到不算）—— 改字段集必须同步这张表并走变更评审：%v（表里现有 %d 项）",
		missing, len(fields))

	// status 三值仍按整节文本核：它们是语义取值，不是字段行。
	section := resultSpecSection(doc)
	for _, v := range frozenStatusValues {
		if !strings.Contains(section, "`"+v+"`") {
			t.Errorf("docs/SPEC.md 的 ResultSpec 节缺 status 取值 %q", v)
		}
	}
}

// resultSpecSection 取「### ResultSpec 冻结」到下一个二级标题之间的文本。
// 找不到节则返回整个文档 —— 上面已先断言节标题存在，这里不再重复失败一次。
func resultSpecSection(doc string) string {
	start := strings.Index(doc, "ResultSpec 冻结")
	if start < 0 {
		return doc
	}
	rest := doc[start:]
	if next := strings.Index(rest, "\n## "); next > 0 {
		return rest[:next]
	}
	return rest
}

// specResultSpecFieldNames 收集 ResultSpec 节里表格首格出现的所有反引号名。
func specResultSpecFieldNames(doc string) map[string]bool {
	names := map[string]bool{}
	for _, line := range strings.Split(resultSpecSection(doc), "\n") {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) == 0 {
			continue
		}
		for _, m := range jsonTagPattern.FindAllStringSubmatch(cells[0], -1) {
			names[m[1]] = true
		}
	}
	return names
}

// jsonTagPattern 抓反引号里的 snake_case 字段名（`fill_timing` → fill_timing）。
var jsonTagPattern = regexp.MustCompile("`([a-z][a-z0-9_]*)`")
