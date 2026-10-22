//go:build qt04scan

// QT-04 ③-① · 重复信号存量扫描器。
//
// 台账判据：「拿到『同 bar 同 symbol 多条信号』在存量策略里的真实重复率，据此把
// A12 从『二选一待定』变成『已成文规则』」。§8.3 的分支只有两条，而选错一条的代价
// 不对称 —— 存量有重复却选了 trap，内置策略的回测当场整场失败（判据 B）；存量零
// 重复却选合并，等于把一条永不触发的规则写进契约。所以这份文件的唯一职责是
// **把重复率量出来**，并且量到能定位源码的程度。
//
// 它是只读的：不改策略、不改引擎、不写库（连 qfq 基准都走内存）。
//
// 复现命令（DSN 是唯一必填项）：
//
//	QT04_PG_DSN='postgres://postgres:postgres@127.0.0.1:5432/quant_trading?sslmode=disable' \
//	  go test ./pkg/backtest -tags qt04scan -run TestQT04DuplicateSignalScan -count=1 -v
//
// 可选覆盖：QT04_UNIVERSE / QT04_WINDOWS / QT04_OUT / QT04_SAMPLES /
// QT04_SKIP_RERUN=1（只用于调试复跑腿；报告会在页首拒绝承认这种跑出来的数是证据）。
//
// 库侧入口用 NewPostgresStoreConnected（连库 + ping，**不迁移**）—— 本仓库侧测试的
// 硬规则（AGENTS §6，护栏在 internal/repoguard）。
//
// 与 QT-02 生成器的关系：同一套装配形状，但**不复用它的 helper** —— 那份文件整份在
// `//go:build qt02report` 后面，带 `qt04scan` 编译时它根本不存在。
package backtest

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"tellurion/pkg/backtest/contracts"
	"tellurion/pkg/domain"
	"tellurion/pkg/fees"
	"tellurion/pkg/logging"
	"tellurion/pkg/marketdata"
	"tellurion/pkg/risk"
	"tellurion/pkg/storage"
	"tellurion/pkg/strategy"
	_ "tellurion/pkg/strategy/plugins"
)

// qt04Strategies 是 ③-① 要求的「14 个内置策略」—— `pkg/strategy/plugins` 里通过
// init() + strategy.GlobalRegister 注册的那一批。
//
// 这份名单**不靠数文件，也不靠台账**：下面有一条断言把「注册表实际有什么」和
// 「这里写了什么」对上，注册表多名字/少名字都会红。台账的「14 个」是 2026-10-20 的快照。
var qt04Strategies = []string{
	"momentum",
	"mean_reversion",
	"multi_factor",
	"value",
	"quality",
	"value_screening",
	"risk_parity",
	"convertible_bond",
	"event_driven",
	"sentiment",
	"td_sequential",
	"bollinger_mr",
	"volume_price_trend",
	"volatility_breakout",
}

// qt04Window 是一个基准区间。
type qt04Window struct {
	Name  string
	Start string
	End   string
}

// qt04Windows 是「3 个基准区间」。选择规则先写在这里，而不是事后找理由：
//
//   - 三段连续、互不重叠，合计 2024-01-02..2025-09-30（库里 bar 从 2023-09-25 到
//     2026-09-24 —— 三段都落在覆盖区内）；
//   - 每段约 140 个交易日：够长到预热期（内置策略普遍要 20~200 根 bar）只吃掉前 1/6，
//     够短到 42 场回测跑得完；
//   - **不**按「涨段/跌段/震荡段」挑：那是拿结论挑样本。本库 `market.index_daily`
//     0 行，我没有独立证据给区间贴行情标签。
var qt04Windows = []qt04Window{
	{"W1 2024-01-02..2024-07-31", "2024-01-02", "2024-07-31"},
	{"W2 2024-08-01..2025-02-28", "2024-08-01", "2025-02-28"},
	{"W3 2025-03-03..2025-09-30", "2025-03-03", "2025-09-30"},
}

// qt04Universe 与 QT-02 用同一份 24 只（两份报告要能互相引用，universe 不同就废了）。
const qt04Universe = "000001.SZ,000002.SZ,000006.SZ,000009.SZ,000011.SZ,000012.SZ," +
	"000014.SZ,000017.SZ,000019.SZ,000020.SZ,000021.SZ,000025.SZ," +
	"600000.SH,600004.SH,600006.SH,600008.SH,600009.SH,600011.SH," +
	"600015.SH,600016.SH,600018.SH,600019.SH,600020.SH,600021.SH"

const (
	qt04Seed           = 42
	qt04Commission     = fees.DefaultCommissionRate
	qt04Slippage       = fees.FixedSlippageRate
	qt04Capital        = 1_000_000.0
	qt04OutDefault     = "docs/test-cases/QT04_DUPLICATE_SIGNAL_SCAN.md"
	qt04SamplesDefault = 6

	// qt04FillTimingPin 显式钉住成交口径。重复是信号层的性质，但策略会读持仓，
	// 而持仓由成交口径决定 ⇒ 不钉住，默认值一改这份扫描的前置条件就跟着漂移。
	qt04FillTimingPin = "next_open"
)

// qt04Sig 是一条被观察到的信号（撮合前、风控前 —— 与 K5 对账钩子同构的那一层）。
type qt04Sig struct {
	Seq        int // 在当日信号切片里的下标 = map 覆盖时的先后顺序
	Symbol     string
	Direction  string
	Strength   float64
	Score      float64
	OrderType  string
	LimitPrice float64
}

// actionKey 是「这条信号会让引擎做什么」的最小可区分描述。
//
// Strength/CompositeScore 进键是因为仓位确实由它们算出：
// `pkg/risk/manager.go` 的批量路径用 weight（来自策略的 Weight(signal, …)）与
// `NormalizeOrderQuantity(positionValue/currentPrice)`，score 还进 RiskScore。
// 两条 actionKey 相同的信号覆盖彼此 = 白刷一遍 map，不改变任何订单。
func (s qt04Sig) actionKey() string {
	return fmt.Sprintf("%s/st=%.4f/cs=%.4f/%s", s.Direction, s.Strength, s.Score, s.OrderType)
}

// qt04Group 是同一个 (date, symbol) 上的全部信号。
type qt04Group struct {
	Date     string
	Symbol   string
	Members  []qt04Sig
	Distinct int  // 不同 actionKey 的个数
	Opposite bool // 组内是否出现「建仓 vs 平仓/反向」这种方向冲突
}

func (g qt04Group) isDuplicate() bool { return len(g.Members) > 1 }

// hasHarmfulDuplicate = 多条且彼此可区分。全同的重复只是噪声；trap 若把噪声也算
// 失败，就是把「引擎行为」写成了「策略必须自证唯一」。
func (g *qt04Group) hasHarmfulDuplicate() bool { return g.isDuplicate() && g.Distinct > 1 }

// qt04Row 是一个（策略 × 区间）组合的测量结果。
type qt04Row struct {
	Strategy string
	Window   string

	Status  string
	Reasons []string

	Days    int
	Signals int
	Keys    int

	DupGroups        int
	DupDays          int
	HarmfulGroups    int
	HarmfulDays      int
	OppositeGroups   int
	ExtraOverwritten int // Signals - Keys：被 map 覆盖掉的条数
	DupRate          float64
	HarmfulRate      float64

	Samples []qt04Group

	// 复跑腿（同进程、同参、同库）：重复组里「最后一条生效」的那条有没有换人。
	RerunDone       bool
	RerunSignals    int
	RerunWinnerDiff int // 末条 action 变了（含整组消失）
	RerunOrderDiff  int // 成员集合相同、但组内顺序不同
	RerunMemberDiff int // 成员集合本身变了（键消失或条数不同）

	// 当日信号切片的**跨标的**顺序：A8 说引擎不排序，这里量它到底变不变。
	// 与组内顺序（RerunOrderDiff）是两件事：组内决定「哪条覆盖哪条」，
	// 跨标的决定「同一天里谁先拿到现金」。
	RerunDayOrderDiff int

	// 成交层对照（same_close：决策日 == 成交日，才能逐日对上）：
	// 一个有害重复组究竟下了几笔单 —— 「最后一条生效」是不是真的。
	JoinDone      bool
	JoinTwoTrades int
	JoinOneTrade  int
	JoinZeroTrade int
	JoinSamples   []string

	// 同日同标的既买又卖（往返）的组数与它的实际成本 —— 从成交流水里加，不估。
	JoinRoundTripGroups     int
	JoinRoundTripCommission float64
	JoinRoundTripNotional   float64

	// DupPatterns：有害重复组按「方向组合」点名（`long+close` / `long+long` …）。
	// §8.3 的合并规则分两支（close 优先 / 同向求和 clamp），哪一支会真的触发要有数。
	DupPatterns     map[string]int
	DiffStrengthDup int // 同向重复里 strength 不同的组数（求和 clamp 那一支的覆盖面）
}

// qt04ScanState 是一次 run 的观察累加器。
//
// 分组与计数分开：复跑腿要拿同一套折叠逻辑再跑一遍，两遍必须同形。
type qt04ScanState struct {
	row    qt04Row
	groups map[string]*qt04Group
	trades []domain.Trade

	// days: 交易日 → 当日信号切片里标的的出现顺序（原样，不排序）。
	// A8 的「引擎不排序」到底有没有后果，就看这个序在复跑之间变不变。
	days map[string][]string
}

func newQT04ScanState(strategyName string) *qt04ScanState {
	return &qt04ScanState{
		row:    qt04Row{Strategy: strategyName, DupPatterns: map[string]int{}},
		groups: map[string]*qt04Group{},
		days:   map[string][]string{},
	}
}

// observe 挂在 SetSignalObserver 上：每个交易日一条，内容是当日全部信号。
func (st *qt04ScanState) observe(date time.Time, signals []domain.Signal) {
	st.row.Days++
	day := date.Format("2006-01-02")
	order := make([]string, 0, len(signals))
	for i, s := range signals {
		order = append(order, s.Symbol)
		g := st.groups[day+"|"+s.Symbol]
		if g == nil {
			g = &qt04Group{Date: day, Symbol: s.Symbol}
			st.groups[day+"|"+s.Symbol] = g
		}
		g.Members = append(g.Members, qt04Sig{
			Seq:        i,
			Symbol:     s.Symbol,
			Direction:  string(s.Direction),
			Strength:   s.Strength,
			Score:      s.CompositeScore,
			OrderType:  string(s.OrderType),
			LimitPrice: s.LimitPrice,
		})
		st.row.Signals++
	}
	st.days[day] = order
}

// fold 把分组折进统计字段并按固定顺序取样例。
func (st *qt04ScanState) fold(maxSamples int) {
	row := &st.row
	row.Keys = len(st.groups)
	row.ExtraOverwritten = row.Signals - row.Keys

	dupDays := map[string]bool{}
	harmfulDays := map[string]bool{}
	harmfulExtra := 0
	for _, g := range st.groups {
		if !g.isDuplicate() {
			continue
		}
		distinct := map[string]bool{}
		dirs := map[string]bool{}
		for _, m := range g.Members {
			distinct[m.actionKey()] = true
			dirs[m.Direction] = true
		}
		g.Distinct = len(distinct)
		g.Opposite = q04DirectionsConflict(dirs)
		row.DupGroups++
		dupDays[g.Date] = true
		if g.hasHarmfulDuplicate() {
			row.HarmfulGroups++
			harmfulDays[g.Date] = true
			harmfulExtra += len(g.Members) - 1
			if row.DupPatterns == nil {
				row.DupPatterns = map[string]int{}
			}
			row.DupPatterns[qt04DirectionPattern(g.Members)]++
			if !g.Opposite && qt04StrengthsDiffer(g.Members) {
				row.DiffStrengthDup++
			}
		}
		if g.Opposite {
			row.OppositeGroups++
		}
	}
	row.DupDays = len(dupDays)
	row.HarmfulDays = len(harmfulDays)
	row.DupRate = qt04Share(row.ExtraOverwritten, row.Signals)
	row.HarmfulRate = qt04Share(harmfulExtra, row.Signals)

	// 样例顺序固定（有害优先 → 日期 → 标的），报告要能逐字节重生成。
	keys := make([]string, 0, len(st.groups))
	for k := range st.groups {
		keys = append(keys, k)
	}
	sort.SliceStable(keys, func(i, j int) bool {
		gi, gj := st.groups[keys[i]], st.groups[keys[j]]
		harmI, harmJ := 0, 0
		if gi.hasHarmfulDuplicate() {
			harmI = 1
		}
		if gj.hasHarmfulDuplicate() {
			harmJ = 1
		}
		if harmI != harmJ {
			return harmI > harmJ
		}
		if gi.Date != gj.Date {
			return gi.Date < gj.Date
		}
		return gi.Symbol < gj.Symbol
	})
	for _, k := range keys {
		g := st.groups[k]
		if !g.hasHarmfulDuplicate() {
			continue
		}
		if len(row.Samples) >= maxSamples {
			break
		}
		row.Samples = append(row.Samples, *g)
	}
}

// q04DirectionsConflict：同组里既有建仓又有平仓/反向，就是 A12 里最坏的那一类 ——
// 哪条生效决定仓位方向。
func q04DirectionsConflict(dirs map[string]bool) bool {
	hasLong := dirs[string(domain.DirectionLong)]
	hasShort := dirs[string(domain.DirectionShort)]
	hasClose := dirs[string(domain.DirectionClose)]
	if hasLong && hasShort {
		return true
	}
	return hasClose && (hasLong || hasShort)
}

// qt04DirectionPattern 给一个重复组的方向组合起名（`long+close` / `long` / …）。
func qt04DirectionPattern(members []qt04Sig) string {
	set := map[string]bool{}
	for _, m := range members {
		set[m.Direction] = true
	}
	ds := make([]string, 0, len(set))
	for d := range set {
		ds = append(ds, d)
	}
	sort.Strings(ds)
	return strings.Join(ds, "+")
}

// qt04StrengthsDiffer = 同向多条里 strength 不全等 —— 「求和后 clamp」那一支真正会动到的场景。
func qt04StrengthsDiffer(members []qt04Sig) bool {
	first := members[0].Strength
	for _, m := range members[1:] {
		if math.Abs(m.Strength-first) > 1e-12 {
			return true
		}
	}
	return false
}

// qt04RerunReading 把四个复跑数折成一句解读。
//
// 为什么要有这个函数而不是直接在报告里写结论句：结论句写死就等于「先有说法，再找数」。
// 换人=0 和换人>0 说的是两件完全不同的事（前者 = 对内部顺序稳定、不确定来自「这一对今天
// 是否存在」；后者 = 「哪条生效」本身在漂），文本必须由数字选。
func qt04RerunReading(winner, member, order, dayOrder int) string {
	var b strings.Builder
	switch {
	case winner == 0 && order == 0 && member > 0:
		b.WriteString("  读法：**重复对内部的先后是稳的**（换人 0、变序 0 —— 两条来自同一轮迭代里\n")
		b.WriteString("  相邻的两个 append），不稳定的是**这一对当天到底存不存在**（变集合 " + fmt.Sprint(member) + " 组）。\n")
		b.WriteString("  所以 A12 里「哪条生效不确定」在这批存量上不是主症状；主症状是「配对本身随持仓状态漂」，\n")
		b.WriteString("  而持仓由当天的执行顺序影响。\n")
	case winner > 0:
		b.WriteString("  读法：**「哪条生效」本身在复跑里变了**（换人 " + fmt.Sprint(winner) + " 组）——\n")
		b.WriteString("  这就是 A12 原文说的不确定，直接测到。\n")
	default:
		b.WriteString("  读法：四类变化全为 0 ⇒ 这批重复在复跑里逐位稳定（" +
			"「不确定」在本环境未被触发，这不等于它不存在）。\n")
	}
	if dayOrder > 0 {
		b.WriteString("  跨标的序变（条数相同的交易日里顺序不同）= " + fmt.Sprint(dayOrder) +
			"：同一天里「谁先下单」的序在漂（A8）。它是上一条的入口 ——\n")
		b.WriteString("  现金与持仓随执行先后改变，第二天策略要不要发平仓腿就跟着变。\n")
	} else if !strings.Contains(b.String(), "全为 0") {
		b.WriteString("  跨标的顺序在复跑里没变（本次样本内）。\n")
	}
	return b.String()
}

func qt04Share(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return 100 * float64(n) / float64(d)
}

// harmfulCount 汇总某策略跨区间的有害重复组数（只用于报错文案）。
func harmfulCount(rows []qt04Row, strategyName string) int {
	n := 0
	for _, r := range rows {
		if r.Strategy == strategyName {
			n += r.HarmfulGroups
		}
	}
	return n
}

func qt04EnvOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// qt04OutPath 把相对路径落到模块根下（`go test` 的 cwd 是包目录，
// 直接写相对路径会把报告丢进 pkg/backtest/docs/…）。
func qt04OutPath(t *testing.T, rel string) string {
	t.Helper()
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(qt04ModuleRoot(t), filepath.FromSlash(rel))
}

// qt04EmitSite 是策略源码里一条 `Action: "…"` 的发射点。
type qt04EmitSite struct {
	File string
	Line int
	Text string
}

// qt04StrategyType 是注册名 → (文件, 接收者类型)。
type qt04StrategyType struct {
	File     string
	Receiver string
}

// qt04FindStrategyType 按注册名在插件目录里找到它的实现类型。
func qt04FindStrategyType(t *testing.T, strategyName string) (qt04StrategyType, bool) {
	t.Helper()
	re := regexp.MustCompile(`&(\w+)\{\s*BaseStrategy: strategy\.NewBaseStrategy\(\s*"` +
		regexp.QuoteMeta(strategyName) + `"`)
	for _, e := range qt04PluginFiles(t) {
		m := re.FindSubmatch(e.data)
		if m == nil {
			continue
		}
		return qt04StrategyType{File: e.rel, Receiver: string(m[1])}, true
	}
	return qt04StrategyType{}, false
}

// qt04ReceiverOfLine 从某一行往上走到最近的 `func ` 行，取出它的接收者类型。
//
// 这是**独立于定位器**的第二套实现：定位器按「顶层函数块切分」筛发射点，这里按
// 「从发射点往上找函数签名」筛。两套实现同意，才说明筛出来的是这个策略自己的发射点；
// 它们不同意，就是切分逻辑漏了边界。少这一层交叉核对，「一个文件住了四个策略」这种
// 形状会安静地把邻居的行数给自己的。
func qt04ReceiverOfLine(t *testing.T, rel string, line int) string {
	t.Helper()
	for _, e := range qt04PluginFiles(t) {
		if e.rel != rel {
			continue
		}
		lines := strings.Split(string(e.data), "\n")
		for i := line - 1; i >= 0; i-- {
			if !strings.HasPrefix(lines[i], "func ") {
				continue
			}
			m := regexp.MustCompile(`func \(\w+ \*(\w+)\)`).FindStringSubmatch(lines[i])
			if m == nil {
				return "" // 普通函数，没有接收者
			}
			return m[1]
		}
		return ""
	}
	t.Fatalf("qt04ReceiverOfLine: 找不到文件 %s", rel)
	return ""
}

type qt04PluginFile struct {
	rel  string
	data []byte
}

func qt04PluginFiles(t *testing.T) []qt04PluginFile {
	t.Helper()
	dir := filepath.Join(qt04ModuleRoot(t), "pkg", "strategy", "plugins")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读 %s 失败: %v", dir, err)
	}
	var out []qt04PluginFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("读 %s: %v", e.Name(), err)
		}
		out = append(out, qt04PluginFile{rel: "pkg/strategy/plugins/" + e.Name(), data: data})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out
}

// qt04LocateEmitSites 在 `pkg/strategy/plugins/` 里按注册名找到策略的实现文件，
// 再列出**它自己方法里**每一条 `Action:` 发射语句的 file:行号。
//
// 为什么定位器要按接收者类型筛文件的一部分：`new_strategies.go` 一个文件里住了
// 四个策略（td_sequential / bollinger_mr / volume_price_trend / volatility_breakout）。
// 只按「文件里有没有这个注册名」筛，就会把邻居策略的发射行也列到 td_sequential 名下 ——
// 指错地方的 file:行号比不给行号更糟（⑤ 要的就是「样例能定位到源码」）。
//
// 为什么仍要现场扫而不是手写清单：手写清单在策略改名/挪文件之后一定腐掉。
func qt04LocateEmitSites(t *testing.T, strategyName string) []qt04EmitSite {
	t.Helper()
	info, ok := qt04FindStrategyType(t, strategyName)
	if !ok {
		return nil
	}
	receiver := "*" + info.Receiver + ")"

	var sites []qt04EmitSite
	for _, e := range qt04PluginFiles(t) {
		if e.rel != info.File {
			continue
		}
		lines := strings.Split(string(e.data), "\n")
		start, sig := -1, ""
		collect := func(from, to int) {
			for i := from; i < to; i++ {
				trimmed := strings.TrimSpace(lines[i])
				if strings.HasPrefix(trimmed, "Action:") {
					sites = append(sites, qt04EmitSite{File: e.rel, Line: i + 1, Text: trimmed})
				}
			}
		}
		for i, ln := range lines {
			if !strings.HasPrefix(ln, "func ") {
				continue
			}
			if start >= 0 && strings.Contains(sig, receiver) {
				collect(start, i)
			}
			start, sig = i, ln
		}
		if start >= 0 && strings.Contains(sig, receiver) {
			collect(start, len(lines))
		}
	}
	return sites
}

// qt04ModuleRoot 从包目录向上找到放 go.mod 的目录。
func qt04ModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("找不到模块根（go.mod）：从 %s 一路向上", dir)
		}
		dir = parent
	}
}

func qt04Connect(t *testing.T) *storage.PostgresStore {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("QT04_PG_DSN"))
	if dsn == "" {
		t.Fatal("QT04_PG_DSN 未设置 —— 存量扫描必须有真数据。" +
			"例：postgres://postgres:postgres@127.0.0.1:5432/quant_trading?sslmode=disable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := storage.NewPostgresStoreConnected(ctx, dsn) // 连库 + ping，不迁移
	if err != nil {
		t.Fatalf("连库失败: %v", err)
	}
	t.Cleanup(store.Close)
	return store
}

// qt04EngineFor 与 QT-02 生成器同形：PG provider + store + 进程内风控 + 内存 qfq 基准。
//
// 内存基准（不碰共享库的 backtest_adjustment_anchors）的理由与 QT-02 相同：
// 库里 2026-10-17 冻结的基准与当前序列已漂移，REV-18 的漂移守卫会当场拦下整场回测；
// 那是「数据被改过」的如实报错，不是本单要修的东西，而写新基准等于替共享库做一次
// 没人要求的冻结。
//
// 每 run 一台新引擎：引擎级缓存与持仓状态是 per-instance 的，共用会把上一场带进下一场。
func qt04EngineFor(t *testing.T, store *storage.PostgresStore) *Engine {
	t.Helper()
	logger := logging.WithContext(map[string]any{})
	prov := marketdata.NewPostgresProvider(store, logger)

	rm, err := risk.NewRiskManager(risk.RiskManagerConfig{
		TargetVolatility:    0.15,
		MaxPositionWeight:   0.10,
		MinPositionWeight:   0.01,
		ATRPeriod:           14,
		BaseMultiplier:      2.0,
		BullMultiplier:      1.5,
		BearMultiplier:      3.0,
		SidewaysMultiplier:  2.0,
		TakeProfitMult:      3.0,
		VolLookbackDays:     60,
		AnnualizationFactor: math.Sqrt(252),
		FastMAPeriod:        50,
		SlowMAPeriod:        200,
		RegimeVolLookback:   120,
	}, logger)
	if err != nil {
		t.Fatalf("risk manager: %v", err)
	}

	eng, err := NewEngineWithOptions(Config{
		InitialCapital: qt04Capital,
		CommissionRate: qt04Commission,
		SlippageRate:   qt04Slippage,
		Seed:           qt04Seed,
	}, prov,
		WithLogger(logger),
		WithStore(store),
		WithRiskManager(rm),
		WithAdjustmentAnchorStore(newMemAnchorStore()),
	)
	if err != nil {
		t.Fatalf("NewEngineWithOptions: %v", err)
	}
	return eng
}

// qt04RunOne 跑一场回测并把 (date,symbol) 分组扫出来。
//
// 观察点在 `getSignals` 之后、`processSignalsAndExecuteTrades` 之前 —— 也就是
// `results[symbol]` 覆盖**发生之前**。等覆盖之后被抹掉的那条已经不在数据里了，
// 在这一层才数得到重复。
func qt04RunOne(t *testing.T, store *storage.PostgresStore, strategyName string, w qt04Window, universe []string, maxSamples int, fillTiming string) *qt04ScanState {
	t.Helper()
	eng := qt04EngineFor(t, store)
	st := newQT04ScanState(strategyName)
	eng.SetSignalObserver(st.observe)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	resp, err := eng.RunBacktest(ctx, contracts.BacktestRequest{
		Strategy:       strategyName,
		StockPool:      universe,
		StartDate:      w.Start,
		EndDate:        w.End,
		InitialCapital: qt04Capital,
		RiskFreeRate:   DefaultRiskFreeRate,
		FillTiming:     fillTiming,
	})
	if err != nil {
		st.row.Status = "error"
		st.row.Reasons = []string{err.Error()}
	} else {
		st.row.Status = resp.Status
		st.row.Reasons = resp.InvalidReasons
		st.trades = resp.Trades
	}
	st.row.Window = w.Name
	st.fold(maxSamples)
	return st
}

// qt04JoinTrades 把「有害重复组」与成交流水对上，量一个组究竟下了几笔单。
//
// 只在 same_close 口径下做：那时决策日 == 成交日，(date,symbol) 能逐日精确 join。
// next_open 下决策 D 的单落在 D+1，join 要额外拿交易日历，而这里要回答的问题
// （「两条信号是一条单还是两条单」）不需要换口径才能答。
//
// 它存在的原因：A12 的原文写「map 覆盖 = 最后一条生效」。但
// `engine_daily.go:393` 的循环走的是**信号切片**（`for _, signal := range validSignals`），
// 只有仓位尺寸取自 `positionSizes[signal.Symbol]`（map，最后一条写进去的那个）。
// 也就是说覆盖的是仓位、不是订单。这条把两种说法用成交流水区分开，不靠读代码下结论。
func qt04JoinTrades(st *qt04ScanState) {
	perKey := map[string][]domain.Trade{}
	for _, tr := range st.trades {
		key := tr.Timestamp.Format("2006-01-02") + "|" + tr.Symbol
		perKey[key] = append(perKey[key], tr)
	}
	for _, g := range st.groups {
		if !g.hasHarmfulDuplicate() {
			continue
		}
		n := len(perKey[g.Date+"|"+g.Symbol])
		switch {
		case n == 0:
			st.row.JoinZeroTrade++
		case n == 1:
			st.row.JoinOneTrade++
		default:
			st.row.JoinTwoTrades++
		}
		// 同日同标的既建仓又平仓 = 一趟往返。成本从流水里加，不估。
		if q04RoundTrip(perKey[g.Date+"|"+g.Symbol]) {
			st.row.JoinRoundTripGroups++
			for _, tr := range perKey[g.Date+"|"+g.Symbol] {
				st.row.JoinRoundTripCommission += tr.Commission + tr.StampTax + tr.TransferFee
				st.row.JoinRoundTripNotional += tr.Quantity * tr.Price
			}
		}
		if len(st.row.JoinSamples) < qt04SamplesDefault {
			dirs := make([]string, 0, n)
			for _, tr := range perKey[g.Date+"|"+g.Symbol] {
				dirs = append(dirs, fmt.Sprintf("%s×%.0f@%.2f", tr.Direction, tr.Quantity, tr.Price))
			}
			st.row.JoinSamples = append(st.row.JoinSamples, fmt.Sprintf(
				"%s %s：信号 %d 条 [%s] → 成交 %d 笔 [%s]",
				g.Date, g.Symbol, len(g.Members), strings.Join(actionKeys(g.Members), " | "),
				n, strings.Join(dirs, " | ")))
		}
	}
	st.row.JoinDone = true
}

func actionKeys(members []qt04Sig) []string {
	out := make([]string, 0, len(members))
	for _, m := range members {
		out = append(out, m.actionKey())
	}
	return out
}

// q04RoundTrip = 同一个 (date,symbol) 的成交流水里既有建仓又有平仓。
func q04RoundTrip(trades []domain.Trade) bool {
	var hasOpen, hasClose bool
	for _, tr := range trades {
		switch tr.Direction {
		case domain.DirectionLong, domain.DirectionShort:
			hasOpen = true
		case domain.DirectionClose:
			hasClose = true
		}
	}
	return hasOpen && hasClose
}

// branch 是 §8.3 的分支。decideBranch 是纯函数，报告文本与它比一次（有断言盯着），
// 免得「数据说有、结论写没有」。
type branch string

const (
	branchTrap        branch = "trap"
	branchMerge       branch = "merge"
	branchNotMeasured branch = "not-measured"
)

func qt04DecideBranch(rows []qt04Row) branch {
	harmful, measured := 0, 0
	for _, r := range rows {
		if r.Status != "completed" {
			continue
		}
		measured++
		harmful += r.HarmfulGroups
	}
	if measured == 0 {
		return branchNotMeasured
	}
	if harmful > 0 {
		return branchMerge
	}
	return branchTrap
}

func TestQT04DuplicateSignalScan(t *testing.T) {
	store := qt04Connect(t)

	// 名单核对（先于扫描）：注册表是活的，台账里的「14 个」是快照。
	registered := map[string]bool{}
	for _, name := range strategy.GlobalList() {
		registered[name] = true
	}
	var missing []string
	for _, name := range qt04Strategies {
		if !registered[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) == len(qt04Strategies) {
		t.Fatalf("注册表里一个内置策略都没找到（GlobalList=%v）—— 名单与装配脱节，"+
			"这份扫描不构成证据", strategy.GlobalList())
	}
	if len(missing) > 0 {
		t.Fatalf("名单里的策略不在注册表里: %v（注册表全量=%v）—— 改名/删除要同步这里，"+
			"否则报告悄悄少扫几个策略", missing, strategy.GlobalList())
	}

	universe := strings.Split(qt04EnvOr("QT04_UNIVERSE", qt04Universe), ",")
	for i := range universe {
		universe[i] = strings.TrimSpace(universe[i])
	}
	windows := qt04Windows
	if raw := strings.TrimSpace(os.Getenv("QT04_WINDOWS")); raw != "" {
		windows = nil
		for _, spec := range strings.Split(raw, ";") {
			parts := strings.Split(spec, ",")
			if len(parts) != 3 {
				t.Fatalf("QT04_WINDOWS 形状应为 Name,Start,End;… got %q", spec)
			}
			windows = append(windows, qt04Window{parts[0], parts[1], parts[2]})
		}
	}
	maxSamples := qt04SamplesDefault
	if v := strings.TrimSpace(os.Getenv("QT04_SAMPLES")); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &maxSamples); err != nil || maxSamples <= 0 {
			t.Fatalf("QT04_SAMPLES 要个正整数, got %q", v)
		}
	}
	skipRerun := strings.TrimSpace(os.Getenv("QT04_SKIP_RERUN")) == "1"

	var rows []qt04Row
	firstPass := map[string]*qt04ScanState{} // key = 策略|区间，只留有重复的

	for _, w := range windows {
		for _, name := range qt04Strategies {
			st := qt04RunOne(t, store, name, w, universe, maxSamples, qt04FillTimingPin)
			rows = append(rows, st.row)
			if st.row.DupGroups > 0 {
				firstPass[name+"|"+w.Name] = st
			}
			t.Logf("%s / %s: signals=%d keys=%d dup=%d harmful=%d opposite=%d status=%s",
				name, w.Name, st.row.Signals, st.row.Keys, st.row.DupGroups,
				st.row.HarmfulGroups, st.row.OppositeGroups, st.row.Status)
		}
	}

	// 复跑腿：对**每一个出现过重复的组合**同参再跑一次，量重复组里「最后一条」有没有换人。
	//
	// A12 说「哪条生效取决于未排序的信号顺序」。QT-02 逮到过结果层的漂移（同参复跑
	// 笔数不同），那是间接证据；这里在信号层直接看谁赢，并且把三种变化分开数：
	//
	//   - WinnerDiff：组还在、成员数一样，但末条 action 变了 —— 这才是「哪条生效」变了；
	//   - MemberDiff：组的成员集合本身变了（键消失或条数不同）；
	//   - OrderDiff：成员集合一样、组内顺序不同。
	//
	// 分开的理由：策略会读持仓（td_sequential 的平仓腿要求 `portfolio.Positions[symbol]`
	// 有量），而持仓被上一天的覆盖结果影响 ⇒ 复跑里「换人」与「整组消失」是两件事，
	// 混在一个数里就没法说清不确定到底从哪来。
	byIndex := map[string]int{}
	for i := range rows {
		byIndex[rows[i].Strategy+"|"+rows[i].Window] = i
	}
	if !skipRerun {
		for key, st := range firstPass {
			i, ok := byIndex[key]
			if !ok {
				t.Fatalf("复跑对照找不到首跑行 %s —— 索引构造错了", key)
			}
			name, wname := rows[i].Strategy, rows[i].Window
			var w qt04Window
			for _, cand := range windows {
				if cand.Name == wname {
					w = cand
				}
			}
			if w.Name == "" {
				t.Fatalf("复跑对照找不到区间 %s", wname)
			}
			again := qt04RunOne(t, store, name, w, universe, maxSamples, qt04FillTimingPin)
			rows[i].RerunDone = true
			rows[i].RerunSignals = again.row.Signals

			for k, g := range st.groups {
				if !g.isDuplicate() {
					continue // 只判重复组：单条信号的组没有「哪条生效」这个问题
				}
				g2, has := again.groups[k]
				if !has || len(g.Members) != len(g2.Members) {
					rows[i].RerunMemberDiff++
					continue
				}
				if g.Members[len(g.Members)-1].actionKey() != g2.Members[len(g2.Members)-1].actionKey() {
					rows[i].RerunWinnerDiff++
				}
				ordered := true
				for m := range g.Members {
					if g.Members[m].actionKey() != g2.Members[m].actionKey() {
						ordered = false
						break
					}
				}
				if !ordered {
					rows[i].RerunOrderDiff++
				}
			}

			// A8 那一半：同一天里**跨标的**的信号顺序变不变。
			// 引擎按信号切片原序下单（`for _, signal := range validSignals`），
			// 所以这个序就是「谁先拿到现金」的序。策略侧 `for symbol := range bars`
			// 是 map 遍历 ⇒ 预期会变；量出来才算。
			for day, want := range st.days {
				got, has := again.days[day]
				if !has || len(want) != len(got) {
					continue
				}
				for m := range want {
					if want[m] != got[m] {
						rows[i].RerunDayOrderDiff++
						break
					}
				}
			}
		}
	}

	// 成交层对照：有害重复组究竟下了几笔单（same_close，决策日 == 成交日才可精确 join）。
	//
	// 这一腿是为了**验工单前提**：A12 的原文是「map 覆盖 ⇒ 最后一条生效」。代码读起来
	// 覆盖的只是仓位尺寸（订单循环走的是信号切片），但那是我对代码的解读，不是证据。
	// 成交流水才是证据：一组两条信号若下出两笔反向单，语义就不是「哪条生效」而是
	// 「两条都下单、共用最后一条的仓位」—— 合并规则要按后者写。
	var joinRows []qt04Row
	joinTargets := map[string]bool{}
	for _, r := range rows {
		if r.HarmfulGroups > 0 && r.Status == "completed" {
			joinTargets[r.Strategy] = true
		}
	}
	for name := range joinTargets {
		st := qt04RunOne(t, store, name, windows[0], universe, maxSamples, "same_close")
		qt04JoinTrades(st)
		joinRows = append(joinRows, st.row)
		t.Logf("成交层对照 %s / %s (same_close)：两条以上=%d 一笔=%d 零笔=%d（有害重复组=%d）",
			name, windows[0].Name, st.row.JoinTwoTrades, st.row.JoinOneTrade,
			st.row.JoinZeroTrade, st.row.HarmfulGroups)
	}
	sort.Slice(joinRows, func(i, j int) bool { return joinRows[i].Strategy < joinRows[j].Strategy })

	// 源码定位：出现有害重复的策略，把它源文件里的 `Action:` 发射点现场列出来。
	//
	// 「一个 (date,symbol) 同一天被发两条」在单次遍历的写法里至少要两个发射语句
	// （循环每只票只走一遍）。凑不满两个就是定位失败 —— 宁可当场红，也不要报告里
	// 写一行指错地方的 file:行号。
	sitesByStrategy := map[string][]qt04EmitSite{}
	{
		harmful := map[string]bool{}
		for _, r := range rows {
			if r.HarmfulGroups > 0 {
				harmful[r.Strategy] = true
			}
		}
		for name := range harmful {
			sites := qt04LocateEmitSites(t, name)
			sitesByStrategy[name] = sites
			if len(sites) < 2 {
				t.Errorf("%s 有 %d 个有害重复组，却只定位到 %d 个属于它自己方法内的 `Action:` 发射点 "+
					"—— 样例定位不到源码行，⑤ 就没过；要么定位器找错了文件/接收者，要么重复另有来路",
					name, harmfulCount(rows, name), len(sites))
			}
			// 交叉核对（两套独立实现）：每个报出去的 file:行号，往上走最近的 `func ` 行
			// 取出的接收者，必须就是这个策略自己的类型。邻居的行被算进来 = 行号指错地方。
			info, ok := qt04FindStrategyType(t, name)
			if !ok {
				t.Errorf("%s 在 pkg/strategy/plugins 里找不到注册点 —— 名单与装配漂移，定位无从谈起", name)
				continue
			}
			for _, s := range sites {
				if got := qt04ReceiverOfLine(t, s.File, s.Line); got != info.Receiver {
					t.Errorf("%s 的发射点 %s:%d 往上走属于类型 %q，不是 %q —— 定位器把同文件里"+
						"别的策略的行算给了它（一个文件住了多个策略），报告里的行号是指错地方的行号",
						name, s.File, s.Line, got, info.Receiver)
				}
			}
		}
	}

	// 扫描器自检：折叠出来的数必须自洽。这条不测「策略对不对」，测「这份证据没算错」。
	for _, r := range rows {
		if r.Keys+r.ExtraOverwritten != r.Signals {
			t.Errorf("%s/%s 分组不自洽: keys=%d + extra=%d != signals=%d",
				r.Strategy, r.Window, r.Keys, r.ExtraOverwritten, r.Signals)
		}
		if r.HarmfulGroups > r.DupGroups {
			t.Errorf("%s/%s 有害重复(%d) 多于重复(%d) —— 分类逻辑错了",
				r.Strategy, r.Window, r.HarmfulGroups, r.DupGroups)
		}
		if r.OppositeGroups > r.DupGroups {
			t.Errorf("%s/%s 相反 action(%d) 多于重复(%d)",
				r.Strategy, r.Window, r.OppositeGroups, r.DupGroups)
		}
		if r.ExtraOverwritten > 0 && r.DupGroups == 0 {
			t.Errorf("%s/%s 有被覆盖的条数(%d) 却没有重复组 —— 分组漏了",
				r.Strategy, r.Window, r.ExtraOverwritten)
		}
	}

	// 复跑腿覆盖面：出现过重复的组合必须都被复跑量过（调试跑除外）。
	if !skipRerun {
		for _, r := range rows {
			if r.DupGroups > 0 && !r.RerunDone {
				t.Errorf("%s/%s 有 %d 个重复组却没跑复跑腿 —— 「哪条生效」没被量过，报告不能出结论",
					r.Strategy, r.Window, r.DupGroups)
			}
		}
	}

	// 成交层对照覆盖面：判据要用到「几笔单」的策略，必须每一个都被量过。
	joinByStrategy := map[string]qt04Row{}
	for _, j := range joinRows {
		joinByStrategy[j.Strategy] = j
	}
	for _, r := range rows {
		if r.HarmfulGroups == 0 || r.Status != "completed" {
			continue
		}
		j, ok := joinByStrategy[r.Strategy]
		if !ok || !j.JoinDone {
			t.Errorf("%s 有 %d 个有害重复组却没做成交层对照 —— "+
				"「一个组下几笔单」没被量过，报告就只能在读代码的层面下结论",
				r.Strategy, r.HarmfulGroups)
		}
		if ok && j.JoinTwoTrades+j.JoinOneTrade+j.JoinZeroTrade != j.HarmfulGroups {
			t.Errorf("%s 成交层对照不自洽：两笔=%d + 一笔=%d + 零笔=%d != 有害重复组=%d",
				j.Strategy, j.JoinTwoTrades, j.JoinOneTrade, j.JoinZeroTrade, j.HarmfulGroups)
		}
	}

	measuredBranch := qt04DecideBranch(rows)
	nonCompleted := []string{}
	for _, r := range rows {
		if r.Status != "completed" {
			nonCompleted = append(nonCompleted, fmt.Sprintf("%s/%s = %s（invalid_reasons=%v）",
				r.Strategy, r.Window, r.Status, r.Reasons))
		}
	}

	md := qt04Markdown(rows, joinRows, sitesByStrategy, universe, windows, measuredBranch, nonCompleted, skipRerun)
	// 报告文本里的分支必须由同一份测量决定：文本与数据分叉就是证据造假的前奏。
	if !strings.Contains(md, "分支 = `"+string(measuredBranch)+"`") {
		t.Errorf("报告文本里的分支与 decideBranch 的返回值不一致（want %q）—— 两者必须同源", measuredBranch)
	}
	path := qt04OutPath(t, qt04EnvOr("QT04_OUT", qt04OutDefault))
	if err := os.WriteFile(path, []byte(md), 0o644); err != nil {
		t.Fatalf("写报告失败 %s: %v", path, err)
	}
	t.Logf("报告已写出: %s（分支=%s，组合=%d，非 completed=%d）", path, measuredBranch, len(rows), len(nonCompleted))
}

// qt04Aggregate 是一个策略跨全部区间的汇总。
type qt04Aggregate struct {
	Strategy         string
	Days             int
	Signals          int
	Keys             int
	DupGroups        int
	HarmfulGroups    int
	OppositeGroups   int
	ExtraOverwritten int
	ZeroSignal       bool
	AnyRerun         bool
	WinnerDiffTotal  int
}

func qt04Aggregates(rows []qt04Row) []qt04Aggregate {
	byName := map[string][]qt04Row{}
	for _, r := range rows {
		byName[r.Strategy] = append(byName[r.Strategy], r)
	}
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)

	out := make([]qt04Aggregate, 0, len(names))
	for _, n := range names {
		a := qt04Aggregate{Strategy: n}
		zero := true
		for _, r := range byName[n] {
			a.Days += r.Days
			a.Signals += r.Signals
			a.Keys += r.Keys
			a.DupGroups += r.DupGroups
			a.HarmfulGroups += r.HarmfulGroups
			a.OppositeGroups += r.OppositeGroups
			a.ExtraOverwritten += r.ExtraOverwritten
			a.WinnerDiffTotal += r.RerunWinnerDiff
			if r.RerunDone {
				a.AnyRerun = true
			}
			if r.Signals > 0 {
				zero = false
			}
		}
		a.ZeroSignal = zero
		out = append(out, a)
	}
	return out
}

func qt04Markdown(rows []qt04Row, joinRows []qt04Row, sitesByStrategy map[string][]qt04EmitSite, universe []string, windows []qt04Window, br branch, nonCompleted []string, skipRerun bool) string {
	aggs := qt04Aggregates(rows)
	var totals qt04Row
	zeroSignal, measuredStrategies := 0, 0
	for _, r := range rows {
		totals.Signals += r.Signals
		totals.Keys += r.Keys
		totals.DupGroups += r.DupGroups
		totals.HarmfulGroups += r.HarmfulGroups
		totals.OppositeGroups += r.OppositeGroups
		totals.ExtraOverwritten += r.ExtraOverwritten
	}
	for _, a := range aggs {
		if a.ZeroSignal {
			zeroSignal++
		} else {
			measuredStrategies++
		}
	}

	var b strings.Builder
	b.WriteString("# QT-04 · 重复信号存量扫描\n\n")
	b.WriteString("> 由 `pkg/backtest/qt04_duplicate_signal_scan_test.go`（build tag `qt04scan`）生成的**只读**扫描报告。\n")
	b.WriteString("> 台账：`docs/TASKS.md` QT-04；裁决表：`docs/design/qt-decoupling/p0-round2-review.md` §8.3。\n")
	b.WriteString(fmt.Sprintf("> 生成时刻：%s UTC。**这份文件的数只在与上文命令同一环境下可复现**"+
		"（universe / 区间 / 引擎装配都固定；`fill_timing` 显式钉 `%s`）。\n\n",
		time.Now().UTC().Format("2006-01-02 15:04"), qt04FillTimingPin))
	if skipRerun {
		b.WriteString("> ⚠️ 本次带 `QT04_SKIP_RERUN=1`：复跑腿没跑，「复跑换人」两列无效，**这份文件不能作为 A12 的证据**。\n\n")
	}

	b.WriteString("## 结论（§8.3 的分支）\n\n")
	b.WriteString(fmt.Sprintf("- **分支 = `%s`**。判据是纯函数 `qt04DecideBranch`：只看 status=completed 的组合里\n"+
		"  「有害重复组」（同 bar 同 symbol ≥2 条且 action 可区分）的总数 —— 实测 **%d** 组，跨 %d 个组合。\n",
		br, totals.HarmfulGroups, len(rows)))
	switch br {
	case branchMerge:
		b.WriteString("- 存量**有**可区分的同 bar 重复 ⇒ §8.3 步 3：**先合并**，trap 留作下一版开关 ——\n" +
			"  `close` 优先于 `long/short`；同向多条按 strength 求和并 clamp 到 [0,1]；\n" +
			"  合并计数 `duplicate_signals_merged` 进 run 级诊断。\n")
		b.WriteString("- 直接上 trap 会让下面「有害重复 > 0」那批策略**整场回测当场失败**（判据 B）。\n")
	case branchTrap:
		b.WriteString("- 存量零有害重复 ⇒ §8.3 步 2：**直接 trap**，写进 `SignalSpec` 当契约而不是开关。\n")
		b.WriteString("- ⚠️ 这条结论的强度由「不可测」一行限定：整场 0 信号的策略没有证明任何东西，\n" +
			"  「没扫到」不等于「不存在」。\n")
	case branchNotMeasured:
		b.WriteString("- **没有任何一场跑成功**（completed 组合数 = 0）：本扫描量不出重复率，\n" +
			"  不构成 §8.3 任何一条分支的证据。\n")
	}
	b.WriteString(fmt.Sprintf("- **能测的策略 %d 个 / 名单 %d 个**：%d 个整场 0 信号。这些是输入缺失\n"+
		"  （见「输入覆盖」），不是策略行为 —— 它们对「零重复」的贡献是 **0 证据**，不是证据。\n",
		measuredStrategies, len(qt04Strategies), zeroSignal))
	b.WriteString(fmt.Sprintf("- **被 map 覆盖掉的条数 = %d / 总信号 %d（%.2f%%）**；其中「可区分」的重复组 %d 个、\n"+
		"  含方向冲突（建仓 vs 平仓，或 long vs short）的 %d 个。\n",
		totals.ExtraOverwritten, totals.Signals, qt04Share(totals.ExtraOverwritten, totals.Signals),
		totals.HarmfulGroups, totals.OppositeGroups))
	nSw, nOrder, nMember, nDayOrder := 0, 0, 0, 0
	for _, r := range rows {
		nSw += r.RerunWinnerDiff
		nOrder += r.RerunOrderDiff
		nMember += r.RerunMemberDiff
		nDayOrder += r.RerunDayOrderDiff
	}
	rerunNote := "复跑腿已执行"
	if skipRerun {
		rerunNote = "本次被 `QT04_SKIP_RERUN=1` 跳过，下面四个数无意义"
	}
	b.WriteString(fmt.Sprintf("- **同参复跑（%s）：重复组里「最后一条」换人 = %d 组，成员集合本身变了 = %d 组，\n"+
		"  成员相同但组内顺序变了 = %d 组，跨标的序变（条数相同的交易日）= %d 个。**\n",
		rerunNote, nSw, nMember, nOrder, nDayOrder))
	b.WriteString(qt04RerunReading(nSw, nMember, nOrder, nDayOrder))
	var joinTwo, joinOne, joinZero, joinGroups int
	for _, j := range joinRows {
		joinTwo += j.JoinTwoTrades
		joinOne += j.JoinOneTrade
		joinZero += j.JoinZeroTrade
		joinGroups += j.HarmfulGroups
	}
	if len(joinRows) > 0 {
		b.WriteString(fmt.Sprintf("- **一个有害重复组实际下出几笔单（same_close 对照，%d 个组）：两笔以上 %d、一笔 %d、零笔 %d。**\n"+
			"  这条是拿来核对 A12 原文「map 覆盖 ⇒ 最后一条生效」的：覆盖发生在\n"+
			"  `positionSizes[signal.Symbol]`（仓位尺寸），而订单循环 `for _, signal := range validSignals`\n"+
			"  走的是信号切片本身。上一行的分布就是两种说法的判据 —— 见「成交层对照」。\n",
			joinGroups, joinTwo, joinOne, joinZero))
	}

	maxDelta, maxWhere := 0, ""
	for _, r := range rows {
		if !r.RerunDone {
			continue
		}
		d := r.RerunSignals - r.Signals
		if d < 0 {
			d = -d
		}
		if d > maxDelta {
			maxDelta = d
			maxWhere = fmt.Sprintf("%s / %s（首跑 %d → 复跑 %d）", r.Strategy, r.Window, r.Signals, r.RerunSignals)
		}
	}
	switch {
	case maxDelta > 0:
		b.WriteString(fmt.Sprintf("- **这份报告对「有重复的策略」是一次抽样，不是逐位可复现的基准**：同参复跑的信号数\n"+
			"  最大偏离 **%d 条**（%s）。表里的绝对数字要和这条一起读。没出现重复的组合不在这里复跑\n"+
			"  （它们的 ±0 由 QT-02 报告的复跑断言覆盖）。\n", maxDelta, maxWhere))
	case !skipRerun:
		b.WriteString("- 出现重复的组合复跑后信号数逐位相同（本次抽样内）。\n")
	}

	b.WriteString("\n## 按策略汇总（全区间合计）\n\n")
	b.WriteString("判据分两列是刻意的：`重复组` 数的是「同 bar 同 symbol 多条」，`有害重复组` 只数其中\n")
	b.WriteString("action 可区分的（两条完全相同的信号互相覆盖 = 白刷 map，不改变任何订单）。\n\n")
	b.WriteString("| 策略 | 交易日 | 信号 | 唯一键 | 重复组 | 有害重复组 | 含方向冲突 | 覆盖条数 | 重复率 | 0 信号 | 复跑换人 |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, a := range aggs {
		b.WriteString(fmt.Sprintf("| %s | %d | %d | %d | %d | %d | %d | %d | %.2f%% | %s | %s |\n",
			a.Strategy, a.Days, a.Signals, a.Keys, a.DupGroups, a.HarmfulGroups,
			a.OppositeGroups, a.ExtraOverwritten, qt04Share(a.ExtraOverwritten, a.Signals),
			qt04YN(a.ZeroSignal), qt04DashBool(a.AnyRerun, a.WinnerDiffTotal)))
	}

	b.WriteString("\n## 逐区间\n\n")
	b.WriteString("| 策略 | 区间 | 交易日 | 信号 | 重复组 | 有害 | 方向冲突 | 覆盖条数 | 重复率 | 复跑换人 | 复跑变集合 | 复跑变序 | 复跑跨标的序变 | status |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, r := range rows {
		winner, order, member, dayOrder := "—", "—", "—", "—"
		if r.RerunDone {
			winner = fmt.Sprintf("%d", r.RerunWinnerDiff)
			order = fmt.Sprintf("%d", r.RerunOrderDiff)
			member = fmt.Sprintf("%d", r.RerunMemberDiff)
			dayOrder = fmt.Sprintf("%d", r.RerunDayOrderDiff)
		}
		b.WriteString(fmt.Sprintf("| %s | %s | %d | %d | %d | %d | %d | %d | %.2f%% | %s | %s | %s | %s | %s |\n",
			r.Strategy, r.Window, r.Days, r.Signals, r.DupGroups, r.HarmfulGroups,
			r.OppositeGroups, r.ExtraOverwritten, r.DupRate, winner, member, order, dayOrder, r.Status))
	}
	b.WriteString("\n复跑四列都在同一件事的四个切面上：「换人」= 重复组里**最后一条**（map 覆盖后生效的那条）\n" +
		"的 action 变了；「变集合」= 该 (date,symbol) 的键消失或条数不同；「变序」= 成员完全相同但组内\n" +
		"顺序不同；「跨标的序变」= 信号条数相同、但当天切片里标的出现顺序变了的日子数\n" +
		"（= A8 说的那个未排序的序；条数变了的日子不在这一列，它们已经落在「变集合」里）。\n" +
		"「—」= 该组合没有重复，复跑腿无需触发。\n")

	b.WriteString("\n## 重复样例（每条都要能定位到源码）\n\n")
	b.WriteString("`#seq` 是当日信号切片里的下标 = `positionSizes[symbol]` 被覆盖的先后，**仓位取最后一条**。\n" +
		"注意这不等于「只有最后一条下单」—— 订单循环走的是切片本身，几笔单见「成交层对照」。\n\n")
	any := false
	for _, r := range rows {
		for _, g := range r.Samples {
			any = true
			b.WriteString(fmt.Sprintf("- **%s / %s / %s %s** —— 同日同标的 %d 条，%d 种可区分 action：\n",
				r.Strategy, r.Window, g.Date, g.Symbol, len(g.Members), g.Distinct))
			for _, m := range g.Members {
				b.WriteString(fmt.Sprintf("  - `#%d %s`（strength=%.4f composite=%.4f order=%s limit=%.4f）\n",
					m.Seq, m.Direction, m.Strength, m.Score, m.OrderType, m.LimitPrice))
			}
		}
	}
	if !any {
		b.WriteString("- 本次扫描没有出现「可区分」的同 bar 重复 ⇒ 无样例可定位。\n")
	}

	if len(sitesByStrategy) > 0 {
		b.WriteString("\n### 发射点（现场从源码定位，不是手写清单）\n\n")
		b.WriteString("扫描器按注册名在 `pkg/strategy/plugins/` 里找到策略源文件，列出其中每一条 `Action:` 发射语句。\n")
		b.WriteString("同一天同一只票被发两条，在「循环每只票走一遍」的写法里至少要两个发射语句 —— 下面的\n")
		b.WriteString("行号就是「该改哪儿」的位置。**行号会随代码移动**，所以它由扫描器现读，不由人记。\n\n")
		var names []string
		for n := range sitesByStrategy {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			b.WriteString(fmt.Sprintf("- **%s**（有害重复 %d 组）：\n", n, harmfulCount(rows, n)))
			for _, s := range sitesByStrategy[n] {
				b.WriteString(fmt.Sprintf("  - `%s:%d` — `%s`\n", s.File, s.Line, s.Text))
			}
		}
	}

	if len(joinRows) > 0 {
		b.WriteString("\n## 成交层对照：一个有害重复组实际下了几笔单\n\n")
		b.WriteString("口径换成 `same_close`（决策日 == 成交日，(date,symbol) 才能与成交流水精确 join）。\n" +
			"这一腿不为量重复率，只为核对 A12 的原文。覆盖发生在 `positionSizes[signal.Symbol]`（**仓位尺寸**），\n" +
			"而 `engine_daily.go` 的订单循环走的是信号切片本身 ⇒ 「最后一条生效」与「两条都下单、\n" +
			"共用最后一条的仓位」是两种不同的语义，而合并规则要按哪一种写，只能看流水。\n\n")
		b.WriteString("| 策略 | 区间 | 有害重复组 | ≥2 笔 | 恰好 1 笔 | 0 笔 |\n")
		b.WriteString("|---|---|---|---|---|---|\n")
		for _, j := range joinRows {
			b.WriteString(fmt.Sprintf("| %s | %s | %d | %d | %d | %d |\n",
				j.Strategy, j.Window, j.HarmfulGroups, j.JoinTwoTrades, j.JoinOneTrade, j.JoinZeroTrade))
		}
		b.WriteString("\n注意这一行的「有害重复组」与上面主表**不可直接比较**：这里是 `same_close` 口径下的\n" +
			"同一区间（决策日 == 收盘成交日才能逐日 join），而主表钉的是 `next_open`。口径换了 → 持仓路径\n" +
			"换了 → 策略要不要发平仓腿也跟着换。这个差本身就是「重复对随状态漂」的又一次观测。\n")
		b.WriteString("\n样例（信号 → 成交，同 (date,symbol)）：\n\n")
		for _, j := range joinRows {
			for _, s := range j.JoinSamples {
				b.WriteString(fmt.Sprintf("- **%s**：%s\n", j.Strategy, s))
			}
		}
		var rtGroups int
		var rtComm, rtNotional float64
		for _, j := range joinRows {
			rtGroups += j.JoinRoundTripGroups
			rtComm += j.JoinRoundTripCommission
			rtNotional += j.JoinRoundTripNotional
		}
		b.WriteString(fmt.Sprintf("\n**同日既建仓又平仓（往返）的组数 = %d**，涉及成交额 %.0f、"+
			"该组内全部成交流水的费用合计 %.2f（费用口径 = 佣金 + 印花税 + 过户费，直接从流水加，不估）。"+
			"这就是「两条都下单」的现状代价：它不是「哪条生效」的哲学问题，是同一笔头寸一天内进出一趟。\n",
			rtGroups, rtNotional, rtComm))
	}

	b.WriteString("\n## 重复的方向组合（§8.3 两支规则各自的覆盖面）\n\n")
	b.WriteString("§8.3 的合并规则有两支：`close` 优先（处理方向冲突）、同向按 strength 求和并 clamp\n")
	b.WriteString("（处理同向多条）。哪一支真会触发，按下面的组合数决定 —— 写一条永不触发的规则\n")
	b.WriteString("进契约就是装饰。\n\n")
	b.WriteString("| 策略 | 区间 | 方向组合 | 组数 | 同向多条里 strength 不同 |\n")
	b.WriteString("|---|---|---|---|---|\n")
	anyPattern := false
	patternTotals := map[string]int{}
	totalSameDirStrengthDiffers := 0
	for _, r := range rows {
		pats := make([]string, 0, len(r.DupPatterns))
		for p := range r.DupPatterns {
			pats = append(pats, p)
		}
		sort.Strings(pats)
		totalSameDirStrengthDiffers += r.DiffStrengthDup
		for _, p := range pats {
			anyPattern = true
			patternTotals[p] += r.DupPatterns[p]
			b.WriteString(fmt.Sprintf("| %s | %s | `%s` | %d | %d |\n",
				r.Strategy, r.Window, p, r.DupPatterns[p], r.DiffStrengthDup))
		}
	}
	if !anyPattern {
		b.WriteString("| — | — | —（无有害重复） | 0 | 0 |\n")
	}
	var pats []string
	for p := range patternTotals {
		pats = append(pats, p)
	}
	sort.Strings(pats)
	b.WriteString("\n全量组合（跨策略跨区间）：")
	for i, p := range pats {
		if i > 0 {
			b.WriteString("；")
		}
		b.WriteString(fmt.Sprintf("`%s` = %d 组", p, patternTotals[p]))
	}
	b.WriteString(fmt.Sprintf("。同向多条且 strength 不同的组数 = %d。\n\n", totalSameDirStrengthDiffers))
	if totalSameDirStrengthDiffers == 0 {
		b.WriteString("⇒ 这批存量里 **只有 `close` 优先那一支会被触发**：全部有害重复都是「建仓 + 平仓」同现，\n" +
			"没有「同向两条但强度不同」。求和 clamp 那一支在现网是**未 exercising** 的 —— 仍然要写进契约\n" +
			"（否则新策略一撞就又是歧义），但它的验收得靠合成用例，不能靠这批存量。\n")
	} else {
		b.WriteString("⇒ 两支规则都有真实覆盖面：`close` 优先处理方向冲突，求和 clamp 处理同向多条。\n")
	}
	b.WriteString("\n> 观测但本单不裁决：样例里 market 单的 `limit_price` 并非 0（域里 `LimitPrice` 的注释写\n" +
		"  `0 for market orders`）。合并规则若哪天要把 LimitPrice 计入「可区分」判据，得先决定这个字段\n" +
		"  在 market 单上到底算什么 —— 那是 QT-21/QT-52 的事，不在本单。\n")

	if len(nonCompleted) > 0 {
		b.WriteString("\n## ⚠️ 非 completed 的组合（这些不是重复率证据）\n\n")
		b.WriteString("invalid_reasons 原样抄自回执，不做解释性改写：\n\n")
		for _, s := range nonCompleted {
			b.WriteString(fmt.Sprintf("- %s\n", s))
		}
	}

	b.WriteString("\n## 输入覆盖（为什么有的策略 0 信号）\n\n")
	b.WriteString("本库实测行数（`psql` 直读；扫描器只读，不写任何东西）：\n\n")
	b.WriteString("| 表 | 行数 | 说明 |\n|---|---|---|\n")
	b.WriteString("| `ohlcv_daily_qfq` | 3,926,510 | 5,671 只，2023-09-25..2026-09-24 |\n")
	b.WriteString("| `stock_fundamentals` | **112** | 只有 **2 只票**（000001.SZ / 600000.SH） |\n")
	b.WriteString("| `fundamentals_detail` | 0 | 深财务纵向无生产者 |\n")
	b.WriteString("| `news` / `announcements` / `hot_search` / `limit_up_pool` | 0 | 舆情、事件类输入缺失 |\n")
	b.WriteString("| `ohlcv_minute` | 0 | 日内特征输入缺失 |\n")
	b.WriteString("| `capital_flow` / `top_list` / `sectors` / `stock_sector_map` | 18 / 256 / 110 / 5,575 | 稀疏 |\n\n")
	b.WriteString("⇒ **本次整场 0 信号的策略**（见「按策略汇总」的「0 信号」列）与**跑成 invalid 的组合**\n")
	b.WriteString("（见上一节的 invalid_reasons）不是「策略没想法」，而是**没有输入**：本库没有可转债表、\n")
	b.WriteString("`news`/`hot_search`/`limit_up_pool` 0 行、`ohlcv_minute` 0 行、`stock_fundamentals` 只覆盖\n")
	b.WriteString("2 只票。\n\n")
	b.WriteString("反过来也要同一把尺：`value` 在基本面残缺时每天仍发上百条信号 —— 它给 PE 找的是\n" +
		"**价格波动率与趋势的代理**（`pkg/strategy/plugins/value.go` 的 `valueScore := 1.0/(1.0+cv) + …`），\n" +
		"本来就不读基本面表。「信号很多」不等于「输入很足」。\n\n")
	b.WriteString("「零重复」与「不可测」必须分开读：前者是证据，后者是空白。§8.3 的分支由上面那条\n")
	b.WriteString("纯函数按 completed 组合里的有害重复组数决定，不由这里的话题决定。\n")

	b.WriteString("\n## 复现\n\n")
	b.WriteString("```bash\nQT04_PG_DSN='postgres://postgres:postgres@127.0.0.1:5432/quant_trading?sslmode=disable' \\\n")
	b.WriteString("  go test ./pkg/backtest -tags qt04scan -run TestQT04DuplicateSignalScan -count=1 -v\n```\n\n")
	b.WriteString(fmt.Sprintf("- universe（%d 只，与 QT-02 同一份，两份报告要能互引）：%s\n",
		len(universe), strings.Join(universe, ", ")))
	b.WriteString("- 区间：")
	for i, w := range windows {
		if i > 0 {
			b.WriteString("；")
		}
		b.WriteString(w.Name)
	}
	b.WriteString(fmt.Sprintf("\n- 引擎装配与 QT-02 生成器同形（PG provider + store + 进程内风控 + 内存 qfq 基准），"+
		"`fill_timing` 显式钉 `%s`，Seed=%d，成本用 `fees.DefaultCommissionRate` + `fees.FixedSlippageRate`。\n",
		qt04FillTimingPin, qt04Seed))
	b.WriteString("- 库侧入口 `storage.NewPostgresStoreConnected` —— 连库**不迁移**（本仓硬规则，护栏在 internal/repoguard）。\n")
	return b.String()
}

func qt04DashBool(has bool, n int) string {
	if !has {
		return "—"
	}
	return fmt.Sprintf("%d", n)
}

func qt04YN(b bool) string {
	if b {
		return "是"
	}
	return "否"
}
