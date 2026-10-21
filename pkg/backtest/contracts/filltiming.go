package contracts

// QT-02 · 成交时点口径（FillTiming）的**唯一**定义点。
//
// 冻结文本：p0-round2-review.md §8.1（N2）。为什么这是一份「口径」而不是一个
// 可调参数：同一批信号在 T 日收盘成交，与在 T+1 日开盘成交，决定的是「这两份
// 结果能不能互相比」——它进 run_spec_hash（N6-5，见 provenance.go），也必须
// 跟着结果一起返回（ResultSpec 的 fill_timing / known_bias）。一个不记成交
// 时点的回测结果，下游无法判断自己拿到的是哪种基准。
//
// 「必填、无默认值」是刻意的：让引擎在没声明时猜一个，等于亲手制造
// 「同一份输入、两种成交时点、同一个 hash」的留痕空洞 —— 那正是本体系
// 唯一要防的失败模式（provenance.go 的 RunSpecInput 注记同一条）。
// 因此默认语义只有**一处**（EffectiveFillTiming，与 K9 的 EffectiveKernel 同形），
// 而引擎侧永远校验自己拿到的是**已知值**（ResolveFillTiming）。

import (
	"errors"
	"fmt"
)

// FillTiming 是「决策 → 成交」的时点口径。底型是 string：它要被序列化进
// 请求、落进 hash、挂在响应上，字符串身份就是契约取值。
type FillTiming string

const (
	// FillTimingNextOpen：T 日收盘决策 → T+1 开盘成交（含滑点）。
	// §8.1 认定的**终态**口径（同收盘成交含前视偏差）。
	FillTimingNextOpen FillTiming = "next_open"
	// FillTimingSameClose：T 日收盘决策 → T 日收盘成交（= 收盘集合竞价假设）。
	// 这是本引擎在 QT-02 之前的唯一行为，保留是为了让历史基准仍然可读；
	// 用它跑出来的结果必须标 known_bias = same_close_fill（KnownBiases）。
	FillTimingSameClose FillTiming = "same_close"
)

// DefaultFillTiming 是请求**未声明**时回落的口径。
//
// 2026-10-21 按 QT-02 ④ 的纪律翻到 next_open：差异报告
// （docs/test-cases/QT02_FILL_TIMING_REPORT.md）先归档、默认值后切换，
// 顺序反过来就让全部历史基准在一次提交里同时失去解释。差随换仓频率放大
// （逐策略数值以报告为准，别在这里重抄 —— 有重复信号的策略每次重生成都会换一组数），
// 报告同时写明「§8.1 那句日收益平移一个 bar 只在成交层成立」。
//
// 改这一行 = 一次基准变更：须同时更新 docs/SPEC.md 的 ResultSpec 节、
// 该报告结论的引用处，以及所有依赖旧默认值的测试夹具。代价要如实读——
// 未声明口径的 nautilus 请求现在会撞上侧车的 next_open 拒收（侧车只实现
// 收盘成交），这是设定的 fail-closed，不是回归：要 NT 结果就显式写口径。
const DefaultFillTiming = FillTimingNextOpen

// KnownBiasSameCloseFill 标注「本结果按收盘成交，含 T 日信息参与成交的偏差」。
// §8.1 兼容行：same_close 保留可以，但结果必须自己说清带了哪个已知偏差。
const KnownBiasSameCloseFill = "same_close_fill"

// 三个哨兵错误分管三件不同的事，混成一个就会让调用方分不清该改哪边：
//   - Missing  = 没给（必填面缺失），
//   - Unknown  = 给了但不认识（拼错 / 上游新增了口径而本内核没实现），
//   - Mismatch = 两个内核各自声明的口径不一致（K15 对拍的前置门）。
//
// Mismatch 与「结果不一致」是两类失败：口径不符时两侧的数字**根本没有可比性**，
// 把它当成一次超差的 diff 去归因，会在错误的问题上耗掉整轮双跑。
var (
	ErrFillTimingMissing  = errors.New("fill_timing is required: known values are next_open | same_close")
	ErrFillTimingUnknown  = errors.New("fill_timing is not a known caliber: known values are next_open | same_close")
	ErrFillTimingMismatch = errors.New("kernels filled at different fill_timing calibers (caliber mismatch, not a result mismatch)")
)

// Valid 报告 f 是否是冻结集里的口径。空串**不**算有效：默认值由
// EffectiveFillTiming 填，走到校验点的空值意味着「有人绕过了默认解析」。
func (f FillTiming) Valid() bool {
	return f == FillTimingNextOpen || f == FillTimingSameClose
}

// String 让 FillTiming 在日志与 hash 分量里读起来就是它自己。
func (f FillTiming) String() string { return string(f) }

// KnownBiases 返回该口径**必须**随结果标注的已知偏差 Code。
//
// next_open 不返任何偏差：它是 §8.1 认定的终态口径，标一个空列表比编造一个
// 「无偏差」的标识更诚实（known_bias 的语义是「已知偏差」，不是「口径名」——
// 口径名已经在 fill_timing 字段里了，两处各写一份就是双真相）。
func (f FillTiming) KnownBiases() []string {
	if f == FillTimingSameClose {
		return []string{KnownBiasSameCloseFill}
	}
	return nil
}

// ResolveFillTiming 严格解析一个声明值：空 → ErrFillTimingMissing，
// 不认识 → ErrFillTimingUnknown，两者都 fail-closed，绝不回落默认值。
//
// 为什么默认值不在这里：这条函数是「已经有人拿到了一个字符串、必须当场判定
// 它够不够格参与成交」的关口（引擎日循环、跨内核对拍）。在这里悄悄补默认，
// 就等于让「没声明」和「声明了默认值」在留痕里长得一模一样。
func ResolveFillTiming(raw string) (FillTiming, error) {
	f := FillTiming(raw)
	switch {
	case raw == "":
		return "", fmt.Errorf("%w (got an empty declaration)", ErrFillTimingMissing)
	case !f.Valid():
		return "", fmt.Errorf("%w (got %q)", ErrFillTimingUnknown, raw)
	default:
		return f, nil
	}
}

// KernelCaliber 是一个内核（或一条执行腿）「按什么口径成交」的自述。
//
// 带 Kernel 名不是为了好看：跨内核比对失败的报文必须一眼指出**哪一侧**要改。
// 两个匿名字符串比出 next_open ≠ same_close 时，读者无从知道该去找 Go 还是侧车。
type KernelCaliber struct {
	Kernel     string // 内核 / 执行腿名（"go"、"nautilus"、"go/wrReplay"…）
	FillTiming string // **实际成交**口径，不是「请求里想要的那个」
}

// CompareKernelCalibers 是两个内核成交前的**前置断言**（§8.1 跨内核行，K15）。
//
// 两侧都必须取自「实际成交」那一侧：Go 腿读自己代码里选定的价源，NT 腿读侧车
// **回报里**声明的口径。读回报而不是信请求，是因为请求说了不算 —— Go 侧自己
// 就刚修掉一处「执行服务把市单基准价覆盖成当日收盘」的缺陷（QT-02），同一类
// 缺陷在别的实现里同样存在。
//
// 任一侧为空 = 未知 = 同样拒绝：不知道对方按什么成交就没有可比性可言，
// 拿这种数字去归因是在错误的问题上烧掉一整轮双跑。
func CompareKernelCalibers(a, b KernelCaliber) error {
	left, err := ResolveFillTiming(a.FillTiming)
	if err != nil {
		return fmt.Errorf("%s kernel: %w", a.Kernel, err)
	}
	right, err := ResolveFillTiming(b.FillTiming)
	if err != nil {
		return fmt.Errorf("%s kernel: %w", b.Kernel, err)
	}
	if left == right {
		return nil
	}
	return fmt.Errorf("%w: %s filled at %s but %s filled at %s — calibers differ, so no metric between them is comparable (align them before reading any diff)",
		ErrFillTimingMismatch, a.Kernel, left, b.Kernel, right)
}

// EffectiveFillTiming 给出本次请求**实际生效**的口径：未声明回落
// DefaultFillTiming，声明了原样透传（哪怕是不认识的值 —— 拒绝它不是这里的
// 职责，Valid/ResolveFillTiming 那一侧才管，否则「拼错的值」会被静默换成
// 默认值，调用方以为自己要的那个口径生效了）。
//
// 与 K9 的 EffectiveKernel() 同形：默认值**不**写回 r.FillTiming。
// BacktestRequest 会被序列化（job 落库等），回填 `"fill_timing":"same_close"`
// 会让存量请求的 JSON 多出字段、破坏「存量请求序列化逐字节不变」。
func (r BacktestRequest) EffectiveFillTiming() FillTiming {
	if r.FillTiming == "" {
		return DefaultFillTiming
	}
	return FillTiming(r.FillTiming)
}
