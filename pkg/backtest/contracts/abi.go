package contracts

// QT-01 · abi v2 契约冻结：host 函数表 · 内存区所有权 · 枚举与上限。
//
// 真相源层级（重要，别搞反）：
//   - adr-029-amendment-host-api-v2.md §2.1 的函数表**自称 ABI 的唯一真相**；
//     本文件是它落到代码的那一份，二者由 abi_contract_table_test.go 逐项对表。
//   - 本包是叶子包（只依赖 domain / fees / stdlib）：**不** import wazero。
//     类型检查发生在 pkg/sandbox/wasm 侧，它把 api.ValueType 映射到这里的
//     HostValueType 再比对。契约因此能在「没有运行时」的地方被读、被测、被
//     引用（SDK 模板、NT 侧、编译服务都吃这张表）。
//
// 「冻结」意味着什么：artifact 出厂后这些名字、签名、偏移、上限都改不动 ——
// 改一次 = 所有已编译 artifact 作废。所以本文件宁可写得啰嗦，也不留解释空间。
// v3 及以后按同一规则**新增函数名**（get_bar3…），永不改已有名字的签名（§2.3）。

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"tellurion/pkg/domain"
)

// ─── 协议版本 ────────────────────────────────────────────────────

// HostAbiVersion 是 host API 的协议版本号（artifact 元数据 abi_version 字段）。
//
// 版本号**不能单独采信**：它由编译服务写在资产记录里，而 artifact 自己带的
// import section 才是不可伪造的声明。本单的角色分工是——import 族自证版本，
// abi_version 只用来交叉校验（见 wasm.DetectHostAbiFamily / CrossCheckAbiVersion）。
type HostAbiVersion int32

const (
	// HostAbiV1 是 per-symbol 实例的旧协议（get_bar 无 symbol 维度）。
	HostAbiV1 HostAbiVersion = 1
	// HostAbiV2 是混合 scope 协议：新增 get_bar2 / get_series_len2 / get_date2 /
	// emit_target2 四个函数名，v1 的 8 个名字原样保留。
	HostAbiV2 HostAbiVersion = 2
)

func (v HostAbiVersion) Valid() bool { return v == HostAbiV1 || v == HostAbiV2 }

func (v HostAbiVersion) String() string {
	if !v.Valid() {
		return fmt.Sprintf("unknown(%d)", int32(v))
	}
	return fmt.Sprintf("on_bar/%d", int32(v))
}

// HostAbiVersionFromName 把 strategy-asset-service-api.md §1 的 abi_version 文本
// （`on_bar/1` / `on_bar/2`）解析成 HostAbiVersion。
func HostAbiVersionFromName(name string) (HostAbiVersion, error) {
	var n int32
	if _, err := fmt.Sscanf(name, "on_bar/%d", &n); err != nil {
		return 0, fmt.Errorf("%w: %q is not an on_bar/N abi_version", ErrAbiVersionUnknown, name)
	}
	v := HostAbiVersion(n)
	if !v.Valid() {
		return 0, fmt.Errorf("%w: on_bar/%d is not a supported host ABI", ErrAbiVersionUnknown, n)
	}
	return v, nil
}

// ─── host 函数名 ─────────────────────────────────────────────────

// v1 家族（8 个，签名与 pkg/sandbox/wasm/host_api.go 现有实现逐一对应）。
const (
	HostFnGetBar          = "get_bar"
	HostFnGetSeriesLen    = "get_series_len"
	HostFnGetSymbolCount  = "get_symbol_count"
	HostFnGetCrossSection = "get_cross_section"
	HostFnGetState        = "get_state"
	HostFnSetState        = "set_state"
	HostFnEmitSignal      = "emit_signal"
	HostFnLog             = "log"
)

// v2 家族（4 个新增名字）。增量命名是刻意的：wasm import 按「名字 + 类型」匹配，
// 同一个 host 模块不能导出两个同名函数，所以 v2 不可能「同名改签名」（§2.3）。
const (
	HostFnGetBar2       = "get_bar2"
	HostFnGetSeriesLen2 = "get_series_len2"
	HostFnGetDate2      = "get_date2"
	HostFnEmitTarget2   = "emit_target2"
)

// ─── 签名 ───────────────────────────────────────────────────────

// HostValueType 是 wasm 值类型的最小枚举。这里不用 wazero 的 api.ValueType，
// 是为了让契约包保持叶子包（见文件头）。映射表在 pkg/sandbox/wasm/abi_check.go。
type HostValueType uint8

const (
	HostI32 HostValueType = iota + 1
	HostI64
	HostF64
)

func (v HostValueType) String() string {
	switch v {
	case HostI32:
		return "i32"
	case HostI64:
		return "i64"
	case HostF64:
		return "f64"
	default:
		return fmt.Sprintf("unknown(%d)", uint8(v))
	}
}

// HostFnContract 是函数表里的一行：一个 host 函数的冻结形状。
type HostFnContract struct {
	// Name 是 env 模块里的导出名（artifact 的 import 名）。
	Name string
	// Since 是该函数自哪个 abi 版本起可用。Since=1 的行在 v2 里签名不变。
	Since HostAbiVersion
	// Params / Results 是冻结签名（v1 的 get_bar 与 v2 的 get_bar2 在这里就分得出）。
	Params  []HostValueType
	Results []HostValueType
	// Semantics / Traps 是从 §2.1、§2.2、§8.2 抄下来的语义与 trap 条件；
	// 它们不是注释而是**契约文本**：SDK、NT 侧、校验器都以此为准。
	Semantics string
	Traps     []string
}

// Signature 返回人类可读的冻结签名，形如 `(i32,i32,i32)->f64`。
func (c HostFnContract) Signature() string {
	return fmt.Sprintf("(%s)->%s", joinValueTypes(c.Params), joinValueTypesOrVoid(c.Results))
}

func joinValueTypes(ts []HostValueType) string {
	parts := make([]string, len(ts))
	for i, t := range ts {
		parts[i] = t.String()
	}
	return strings.Join(parts, ",")
}

func joinValueTypesOrVoid(ts []HostValueType) string {
	if len(ts) == 0 {
		return "void"
	}
	return joinValueTypes(ts)
}

// hostFnContracts 是 §2.1 函数表在代码里的形状，**逐项对表的对象**。
// 新增/删除/改签名都必须先改设计文档并走 abi vN 冻结，不能在这里顺手加。
var hostFnContracts = []HostFnContract{
	// ---- v1 家族（保留，签名不变）----
	{
		Name: HostFnGetBar, Since: HostAbiV1,
		Params:  []HostValueType{HostI32, HostI32},
		Results: []HostValueType{HostF64},
		Semantics: "取当前 symbol 在 t+t_offset 的字段值；仅 v1 artifact 可导入。" +
			"缺 bar 返回 NaN，不是 0。",
		Traps: []string{"t_offset > 0（前视隔离）", "无回放上下文"},
	},
	{
		Name: HostFnGetSeriesLen, Since: HostAbiV1,
		Params:    nil,
		Results:   []HostValueType{HostI32},
		Semantics: "当前 symbol 序列长度（v1 语义：等于宿主侧已加载长度；v2 见 get_series_len2）。",
		Traps:     []string{"无回放上下文"},
	},
	{
		Name: HostFnGetSymbolCount, Since: HostAbiV1,
		Params:    nil,
		Results:   []HostValueType{HostI32},
		Semantics: "Symbols 有序表长度；symbol_idx 的取值上界。",
		Traps:     []string{"无回放上下文"},
	},
	{
		Name: HostFnGetCrossSection, Since: HostAbiV1,
		Params:  []HostValueType{HostI32, HostI32, HostI32},
		Results: []HostValueType{HostF64},
		Semantics: "横截面取数 (t_offset, field_id, idx)。三参数是正解——" +
			"host_api.go 旧注释写的两参数是笔误（§2.1 表 2026-10-19 终审订正）。",
		Traps: []string{"t_offset > 0", "无回放上下文"},
	},
	{
		Name: HostFnGetState, Since: HostAbiV1,
		Params:  []HostValueType{HostI32, HostI32},
		Results: []HostValueType{HostI64},
		Semantics: "按 key 读宿主管理的持久化状态，返回打包 (ptr,len) 的 i64；" +
			"未命中返回 pack(0,0)。宿主持有，按日期边界 flush。" +
			"返回值指向 scratch，**下一次 host 调用后即失效**（§2.2-11）。",
		Traps: []string{"key 越界", "单值超 MaxStateValueBytes", "scratch 超 MaxScratchPerBar"},
	},
	{
		Name: HostFnSetState, Since: HostAbiV1,
		Params:  []HostValueType{HostI32, HostI32, HostI32, HostI32},
		Results: nil,
		Semantics: "写宿主状态 (key_ptr,key_len,val_ptr,val_len)。状态序列化必须确定性" +
			"（固定顺序），否则 K15 对拍假失败（§2.2-4）。",
		Traps: []string{"key/value 越界", "单值超 MaxStateValueBytes", "实例总字节超 MaxInstanceStateBytes", "key 数超 MaxStateKeys"},
	},
	{
		Name: HostFnEmitSignal, Since: HostAbiV1,
		Params:  []HostValueType{HostI32, HostI32, HostF64},
		Results: nil,
		Semantics: "发增量信号 (symbol_idx, action, strength)。Strength=信号强度/" +
			"置信度 ∈[0,1]（D26 裁决，**不是目标权重**——现有 Go 策略把它当强度再乘 baseWeight）。",
		Traps: []string{"symbol_idx 越界", "action ∉ {0,1,2,3}", "strength 非 finite 或越界", "本 bar 发射数超 MaxSignalsPerBar", "与 emit_target2 同 bar 同 symbol 混用"},
	},
	{
		Name: HostFnLog, Since: HostAbiV1,
		Params:    []HostValueType{HostI32, HostI32, HostI32},
		Results:   nil,
		Semantics: "诊断输出 (level,msg_ptr,msg_len)。刻意不落盘、不外发（ADR-029 §3.2 无文件/网络）。",
		Traps:     []string{"msg 越界"},
	},

	// ---- v2 家族（本单新增的名字；函数体属 QT-24/QT-26）----
	{
		Name: HostFnGetBar2, Since: HostAbiV2,
		Params:  []HostValueType{HostI32, HostI32, HostI32},
		Results: []HostValueType{HostF64},
		Semantics: "带 symbol 维度的取数 (symbol_idx, t_offset, field_id)。" +
			"market scope：date := Axis[T+t_offset] → Bars[symbol_idx] 该日 bar；" +
			"symbol scope：等价 Bars[0][T+t_offset] 且 symbol_idx 必须为 0。" +
			"缺 bar（停牌/未上市）返回 NaN，禁止用 0 掩盖（§2.2-1）。",
		Traps: []string{"t_offset > 0", "symbol_idx 越界", "field_id ∉ FieldID 枚举", "symbol scope 下 symbol_idx != 0"},
	},
	{
		Name: HostFnGetSeriesLen2, Since: HostAbiV2,
		Params:  []HostValueType{HostI32},
		Results: []HostValueType{HostI32},
		Semantics: "返回**可见长度**（日期 ≤ Axis[T] 的 bar 数），不是 len(Bars[symbol_idx])。" +
			"这条现在写死是防未来泄漏：X1 的 dataset snapshot 会整段预加载，" +
			"那时 len 含未来 bar，返回值本身就是前视（§7.1）。",
		Traps: []string{"symbol_idx 越界"},
	},
	{
		Name: HostFnGetDate2, Since: HostAbiV2,
		Params:  nil,
		Results: []HostValueType{HostI32},
		Semantics: "当前 bar 的日期，YYYYMMDD（UTC）。状态机做日历相关转移必需" +
			"（月末调仓、持有 N 个交易日、事件窗口）。现在定成本近零，以后再定会让" +
			"v2 内部出现两种形态（B2）。",
		Traps: []string{"无回放上下文"},
	},
	{
		Name: HostFnEmitTarget2, Since: HostAbiV2,
		Params:  []HostValueType{HostI32, HostF64},
		Results: nil,
		Semantics: "发**目标权重** (symbol_idx, weight ∈ [-1,1]，占总权益)：-1=不动、0=清仓，" +
			"符号定方向。引擎按 qty = floor(|w|×equity/price/lot)×lot 落单；" +
			"RiskPolicy 的 cap 优先，截断计 sizing_clamped_total（§8.2）。",
		Traps: []string{"symbol_idx 越界", "weight 非 finite 或越界", "与 emit_signal 同 bar 同 symbol 混用"},
	},
}

// HostABIContracts 返回冻结函数表的副本，按名字排序（确定性）。
//
// 深拷贝 Params/Results/Traps：这三个切片是调用方能碰到的**契约本体**，
// 给出去一份共享底层的副本，就等于让任何一个消费方都能改写整张表。
func HostABIContracts() []HostFnContract {
	out := make([]HostFnContract, len(hostFnContracts))
	for i, c := range hostFnContracts {
		out[i] = c.clone()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// HostABIContract 按名字查一行；第二个返回值是 false 表示该名字不在契约里。
func HostABIContract(name string) (HostFnContract, bool) {
	for _, c := range hostFnContracts {
		if c.Name == name {
			return c.clone(), true
		}
	}
	return HostFnContract{}, false
}

func (c HostFnContract) clone() HostFnContract {
	c.Params = append([]HostValueType(nil), c.Params...)
	c.Results = append([]HostValueType(nil), c.Results...)
	c.Traps = append([]string(nil), c.Traps...)
	return c
}

// HostFnCount 是表里的函数总数——文档对表用的那个数。
//
// ⚠️ 设计文档多处写「13 个 host 函数」（adr-029 §7.2-1 甚至自己写成
// 「8 个 v1 + get_bar2 + get_series_len2 + get_date2 + emit_target2」，8+4=12）。
// **13 是算错了**：枚举出来的集合是 12 个 —— v1 的 8 个（host_api.go 白名单实测）
// + v2 新增 4 个。这里返回枚举结果而不是文档里的字面数，是为了让对表测试能
// 抓出这个偏差，而不是把一个错数固化成常量。文档侧已同步订正。
func HostFnCount() int { return len(hostFnContracts) }

// ─── 枚举：field_id / Action / Strength / target weight ──────────

// FieldID 是 get_bar2 / get_cross_section 的字段枚举（§7.1 冻结）。
//
// 为什么必须冻结到具体数字：SDK 绑定和 NT 侧各自硬编码这些常量，错一位就
// 是「把 volume 当 close 用」这种静默错——不报错、结果还看着合理。
type FieldID int32

const (
	FieldClose  FieldID = 0
	FieldOpen   FieldID = 1
	FieldHigh   FieldID = 2
	FieldLow    FieldID = 3
	FieldVolume FieldID = 4
)

func (f FieldID) Valid() bool { return f >= FieldClose && f <= FieldVolume }

// FieldIDs 返回全部合法值（供 SDK/NT 侧与测试枚举）。
func FieldIDs() []FieldID {
	return []FieldID{FieldClose, FieldOpen, FieldHigh, FieldLow, FieldVolume}
}

// Action 是 emit_signal 的方向枚举，与 domain.Direction 一一对应（A5 裁决）。
//
// 「零映射歧义」是这条的目的：0/1/2/3 直接对应 hold/long/close/short，
// 猜错方向的后果是买卖反向，属于判据 A（静默错）。其余值 → trap。
type Action int32

const (
	ActionHold  Action = 0
	ActionLong  Action = 1
	ActionClose Action = 2
	ActionShort Action = 3
)

func (a Action) Valid() bool { return a >= ActionHold && a <= ActionShort }

// Actions 返回全部合法值。
func Actions() []Action { return []Action{ActionHold, ActionLong, ActionClose, ActionShort} }

// Direction 映射到 domain.Direction；非法值返回 error（调用方 trap，不回落 hold）。
func (a Action) Direction() (domain.Direction, error) {
	switch a {
	case ActionHold:
		return domain.DirectionHold, nil
	case ActionLong:
		return domain.DirectionLong, nil
	case ActionClose:
		return domain.DirectionClose, nil
	case ActionShort:
		return domain.DirectionShort, nil
	default:
		return "", fmt.Errorf("%w: action=%d is outside the frozen enum {0,1,2,3}", ErrActionIllegal, int32(a))
	}
}

// ValidateStrength 校验 emit_signal 的 strength（D26：信号强度/置信度，finite ∈ [0,1]）。
//
// 刻意**不**定义为权重：现有 Go 策略把它当强度再乘 baseWeight
// （plugins/momentum.go、examples/value_momentum.go），改定义会让 K15 立刻失真。
// 要目标权重请用 EmitTarget2 那一套（ValidateTargetWeight）。
func ValidateStrength(strength float64) error {
	if math.IsNaN(strength) || math.IsInf(strength, 0) {
		return fmt.Errorf("%w: strength=%v must be finite", ErrStrengthOutOfRange, strength)
	}
	if strength < 0 || strength > 1 {
		return fmt.Errorf("%w: strength=%v ∉ [0,1]", ErrStrengthOutOfRange, strength)
	}
	return nil
}

// ValidateTargetWeight 校验 emit_target2 的 weight（§8.2：目标权重 ∈ [-1,1]）。
//
// 边界语义要记牢：-1 = **不动**（保持现仓），0 = **清仓**。这和「权重 0 = 不持有」
// 直觉一致，但和「-1 = 满仓做空」的直觉不一致——所以它是个受冻的约定，不是推导结果。
func ValidateTargetWeight(weight float64) error {
	if math.IsNaN(weight) || math.IsInf(weight, 0) {
		return fmt.Errorf("%w: weight=%v must be finite", ErrTargetWeightOutOfRange, weight)
	}
	if weight < -1 || weight > 1 {
		return fmt.Errorf("%w: weight=%v ∉ [-1,1]", ErrTargetWeightOutOfRange, weight)
	}
	return nil
}

// ─── 枚举：scope ────────────────────────────────────────────────

// StrategyScope 是实例模型（§3）。必填、未知值一律拒绝（fail-closed）。
type StrategyScope string

const (
	ScopeMarket StrategyScope = "market" // 1 实例/run，横截面，Axis 为日期轴
	ScopeSymbol StrategyScope = "symbol" // N 实例/run，每实例绑定一个 symbol
)

func (s StrategyScope) Valid() bool { return s == ScopeMarket || s == ScopeSymbol }

// ResolveScope 把 (声明的 abi_version, 声明的 scope) 折成生效 scope。
//
// 规则（B3-4 / §3 末）：v1 只能是 symbol（v1 协议表达不了横截面）；
// v2 必须显式声明且 ∈ {market, symbol}。
//
// v1 + scope 空 = 旧 artifact 没这个字段，按 symbol 补——这是兼容期唯一允许的
// 「空值有意义」，且只在 v1 路上发生。
func ResolveScope(version HostAbiVersion, declared StrategyScope) (StrategyScope, error) {
	switch version {
	case HostAbiV1:
		switch declared {
		case "":
			return ScopeSymbol, nil
		case ScopeSymbol:
			return ScopeSymbol, nil
		default:
			return "", fmt.Errorf("%w: abi v1 declares scope %q; v1 has no symbol dimension", ErrScopeMarketV1, declared)
		}
	case HostAbiV2:
		if !declared.Valid() {
			return "", fmt.Errorf("%w: abi v2 requires an explicit scope, got %q", ErrScopeUnknown, declared)
		}
		return declared, nil
	default:
		return "", fmt.Errorf("%w: cannot resolve scope for abi %v", ErrAbiVersionUnknown, version)
	}
}

// ─── 资源边界（宿主侧，64MB wasm 内存管不住的那部分）────────────
//
// 为什么这些数字是契约而不是配置：策略作者会按上限写代码（「state 存 60KiB 够了」），
// 事后调低 = 已出厂 artifact 运行期 trap。所以数字一改就是 ABI 破坏。

const (
	// MaxParamsBytes 是 initialize 一次性投递的 params JSON 上限（A6）。
	MaxParamsBytes = 4096
	// MaxStateValueBytes 是单个 state value 上限（A6）。
	MaxStateValueBytes = 64 << 10
	// MaxInstanceStateBytes 是单实例 State 总字节上限（宿主 Go 内存，A6）。
	// hostSetState 会 make([]byte) 复制进宿主内存，wasm 的 64MB 上限管不到它。
	MaxInstanceStateBytes = 4 << 20
	// MaxStateKeys 是单实例 state key 数上限（A6）。
	MaxStateKeys = 4096
	// MaxScratchPerBar 是 get_state 的 per-bar bump allocator 上限（§2.2-11）。
	MaxScratchPerBar = 256 << 10

	// HostRegionAlignment 是 host 区指针的对齐要求（N5：8 字节）。
	HostRegionAlignment = 8
	// MinHostParamsPtr 是 __host_params_ptr 的下界。
	//
	// 这是「host 不踩模块自己的数据」的**唯一静态判据**：TinyGo 的 data/stack
	// 都在线性内存低地址，声明在这里以下的 ptr 必然与模块自身数据重叠。
	// A6 的旧布局把 params 放在 [0,4096)，是因为当时没有声明机制；N5 之后
	// 由模块自己声明，声明值必须 ≥ 4096。**v1 兼容路径仍写 0**（legacy_host_layout）。
	MinHostParamsPtr = MaxParamsBytes
	// MinHostScratchSize 是 __host_scratch_size 的下界（N5：≥ 64 KiB）。
	// 生效 scratch 预算 = min(MaxScratchPerBar, 声明值)。
	MinHostScratchSize = MaxStateValueBytes
)

// MaxSignalsPerBar 是单 bar 内 emit_signal + emit_target2 的合计上限（A6-7）。
//
// 判据是「2 × symbolCount」= 每个标的最多「平掉旧仓 + 开新仓」两条。超过它
// 说明策略在按 bar 灌水，会把宿主内存打爆（Signals 切片住在 Go 侧）。
func MaxSignalsPerBar(symbolCount int32) int32 {
	if symbolCount <= 0 {
		return 0
	}
	return 2 * symbolCount
}

// host region 导出符号名（N5 裁决：宿主区由模块声明，host 只写声明过的区域）。
const (
	ExportHostParamsPtr   = "__host_params_ptr"
	ExportHostScratchPtr  = "__host_scratch_ptr"
	ExportHostScratchSize = "__host_scratch_size"
)

// HostRegionExports 是三个导出符号的名字（校验/报错枚举用）。
func HostRegionExports() []string {
	return []string{ExportHostParamsPtr, ExportHostScratchPtr, ExportHostScratchSize}
}

// HostRegionDeclaration 是模块声明的宿主区布局。
type HostRegionDeclaration struct {
	ParamsPtr   uint32
	ScratchPtr  uint32
	ScratchSize uint32
}

// Validate 检查「对齐 / 下界 / 区间重叠 / 越界」四类违规；memorySizeBytes 是
// 实例当前线性内存大小（字节）。
//
// 为什么在 instantiate 之后、initialize 之前：global 的**值**只有实例化后读得到
// （编译期只有类型），而「不许写没声明过的地址」这件事必须在第一次写之前成立。
func (d HostRegionDeclaration) Validate(memorySizeBytes uint32) error {
	if d.ParamsPtr%HostRegionAlignment != 0 {
		return fmt.Errorf("%w: %s=%d is not %d-byte aligned",
			ErrHostRegionInvalid, ExportHostParamsPtr, d.ParamsPtr, HostRegionAlignment)
	}
	if d.ScratchPtr%HostRegionAlignment != 0 {
		return fmt.Errorf("%w: %s=%d is not %d-byte aligned",
			ErrHostRegionInvalid, ExportHostScratchPtr, d.ScratchPtr, HostRegionAlignment)
	}
	if d.ParamsPtr < MinHostParamsPtr {
		return fmt.Errorf("%w: %s=%d is below the reserved floor %d (the module's own data/stack lives below it)",
			ErrHostRegionInvalid, ExportHostParamsPtr, d.ParamsPtr, MinHostParamsPtr)
	}
	if d.ScratchSize < MinHostScratchSize {
		return fmt.Errorf("%w: %s=%d < the required minimum scratch %d",
			ErrHostRegionInvalid, ExportHostScratchSize, d.ScratchSize, MinHostScratchSize)
	}
	// params 区间的静态足迹按上限算：真实 len(params) 只可能更小，用上限是保守侧。
	paramsEnd := uint64(d.ParamsPtr) + MaxParamsBytes
	scratchEnd := uint64(d.ScratchPtr) + uint64(d.ScratchSize)
	if rangesOverlap(uint64(d.ParamsPtr), paramsEnd, uint64(d.ScratchPtr), scratchEnd) {
		return fmt.Errorf("%w: params [%d,%d) overlaps scratch [%d,%d)",
			ErrHostRegionInvalid, d.ParamsPtr, paramsEnd, d.ScratchPtr, scratchEnd)
	}
	mem := uint64(memorySizeBytes)
	if paramsEnd > mem {
		return fmt.Errorf("%w: params [%d,%d) exceeds memory size %d",
			ErrHostRegionInvalid, d.ParamsPtr, paramsEnd, memorySizeBytes)
	}
	if scratchEnd > mem {
		return fmt.Errorf("%w: scratch [%d,%d) exceeds memory size %d",
			ErrHostRegionInvalid, d.ScratchPtr, scratchEnd, memorySizeBytes)
	}
	return nil
}

func rangesOverlap(aStart, aEnd, bStart, bEnd uint64) bool {
	return aStart < bEnd && bStart < aEnd
}

// ValidateParamsWrite 是 initialize 之前的第二道门（N5-3）：
// params 既不能超契约上限，也不能塞进 scratch。
func ValidateParamsWrite(paramsLen int, d HostRegionDeclaration) error {
	if paramsLen < 0 {
		return fmt.Errorf("%w: negative params length", ErrParamsTooLarge)
	}
	room := uint64(0)
	if uint64(d.ScratchPtr) > uint64(d.ParamsPtr) {
		room = uint64(d.ScratchPtr) - uint64(d.ParamsPtr)
	}
	budget := uint64(MaxParamsBytes)
	if room < budget {
		budget = room
	}
	if uint64(paramsLen) > budget {
		return fmt.Errorf("%w: params %d bytes > min(MaxParamsBytes=%d, scratch_ptr-params_ptr=%d) = %d (truncation is not allowed)",
			ErrParamsTooLarge, paramsLen, MaxParamsBytes, room, budget)
	}
	return nil
}

// ─── 模块静态数据 × 宿主区（QT-62 的地址空间权威）───────────────
//
// **权威定则**（QT-62 ③ 的前置，先定再判）：
//   - 模块自己的静态数据 = artifact 的 Data section（id=11）里**活动段**的并集；
//   - 宿主区 = 模块通过三个导出 global 声明的三段区间。
//
// 两者都由 artifact 自己声明，没有第三方可以仲裁"谁先谁后"。所以重叠时
// **不重排地址、不缩区间**——直接拒绝该 artifact。这是 N5「host 只写声明过的
// 地址」的另一半：只声明还不够，声明的地址不能已经是模块自己的变量。
//
// 为什么必须从字节码解而不是问运行时：wazero v1.12 的 CompiledModule 不暴露
// Data section（实测接口只有 Name / ImportedFunctions / ExportedFunctions /
// ImportedMemories / ExportedMemories / CustomSections / Close），instantiate
// 之后能读到的只有 global 值与 Memory().Size()。低区下界（MinHostParamsPtr）
// 只是「TinyGo 当前布局在 4096 以下不放东西」的巧合，数据段一旦越过 4096
// 就看不出来了 —— 那正是本单的立项证据。

// StaticDataSegment 是模块 Data section 里一个**活动段**的常量区间。
type StaticDataSegment struct {
	Offset uint32
	Length uint32
}

// StaticDataImage 是从 artifact 字节解出的静态数据布局。
//
// Unprovable 非空表示存在**无法证明不重叠**的段（被动段 / 非常量 offset /
// 非零 memory index）。这不是"没有数据"，而是"这张图不完整"——调用方必须
// 按 fail-closed 处理，不许当成一张空图放行（那等于把"读不懂"当"没风险"）。
type StaticDataImage struct {
	Segments   []StaticDataSegment
	Unprovable string
}

// CheckAgainstStaticData 判定宿主区是否踩到模块自己的静态数据。
//
// 判定顺序上它排在 Validate 之后：先报"声明本身不合法"（对齐/下界/越界/
// 自相重叠），再报"声明合法但落在模块数据上"，读者才分得清该改声明还是
// 该改数据布局。零长段不构成重叠（rangesOverlap 用严格不等号）。
func (d HostRegionDeclaration) CheckAgainstStaticData(img StaticDataImage) error {
	if img.Unprovable != "" {
		return fmt.Errorf("%w: %s", ErrHostRegionDataUnprovable, img.Unprovable)
	}
	regions := []struct {
		export     string
		start, end uint64
	}{
		{ExportHostParamsPtr, uint64(d.ParamsPtr), uint64(d.ParamsPtr) + MaxParamsBytes},
		{ExportHostScratchPtr, uint64(d.ScratchPtr), uint64(d.ScratchPtr) + uint64(d.ScratchSize)},
	}
	for _, seg := range img.Segments {
		if seg.Length == 0 {
			// 空段不占任何字节：它不可能踩到任何人。rangesOverlap 对 [x,x)
			// 会返回 true（x<bEnd && bStart<x），所以这里必须先摘掉它。
			continue
		}
		segStart := uint64(seg.Offset)
		segEnd := segStart + uint64(seg.Length)
		for _, r := range regions {
			if !rangesOverlap(segStart, segEnd, r.start, r.end) {
				continue
			}
			return fmt.Errorf(
				"%w: %s region [%d,%d) overlaps the module's own data segment [%d,%d)",
				ErrHostRegionOverlapsData, r.export, r.start, r.end, segStart, segEnd)
		}
	}
	return nil
}

// ─── emit 源混用判定（§8.2）─────────────────────────────────────

// EmitSource 是一条 emission 由哪个 host 函数发出。
type EmitSource uint8

const (
	EmitSourceSignal EmitSource = iota + 1 // emit_signal（增量强度）
	EmitSourceTarget                       // emit_target2（目标权重）
)

func (s EmitSource) String() string {
	switch s {
	case EmitSourceSignal:
		return HostFnEmitSignal
	case EmitSourceTarget:
		return HostFnEmitTarget2
	default:
		return fmt.Sprintf("unknown(%d)", uint8(s))
	}
}

// BarEmission 是「某 bar 内、某 symbol 上的一次发射」的最小记录。
//
// 宿主按 bar 收集、每 bar 结束时调 CheckEmitSources 并重置。调用方（QT-26 的
// WASM SignalProvider）负责按 (bar, symbol) 攒出这张切片。
type BarEmission struct {
	SymbolIdx int32
	Source    EmitSource
}

// CheckEmitSources 判定「同一 bar 同一 symbol 混用 emit_signal 与 emit_target2」
// ——两套 sizing 不许打架，命中即 trap（§8.2 表「与 emit_signal」行）。
//
// 这是判定**入口**，不是实现：函数体接线在 QT-26；本单把它定成纯函数，
// 是为了让「混用一定被抓」这件事在契约层就可证伪。
func CheckEmitSources(emissions []BarEmission) error {
	// 每个 symbol 一个两位掩码：bit0=见过 emit_signal，bit1=见过 emit_target2。
	// 两位都置起来 = 同 bar 同 symbol 两套 sizing 都发过 = 违规。
	const (
		maskSignal uint8 = 1 << 0
		maskTarget uint8 = 1 << 1
	)
	seen := make(map[int32]uint8, len(emissions))
	for _, e := range emissions {
		var bit uint8
		switch e.Source {
		case EmitSourceSignal:
			bit = maskSignal
		case EmitSourceTarget:
			bit = maskTarget
		default:
			return fmt.Errorf("%w: symbol_idx=%d has an unknown emit source", ErrEmitSourceUnknown, e.SymbolIdx)
		}
		seen[e.SymbolIdx] |= bit
	}
	var mixed []int32
	for idx, mask := range seen {
		if mask == maskSignal|maskTarget {
			mixed = append(mixed, idx)
		}
	}
	if len(mixed) > 0 {
		sort.Slice(mixed, func(i, j int) bool { return mixed[i] < mixed[j] })
		return fmt.Errorf("%w: symbols %v received both %s and %s in the same bar",
			ErrEmitSourcesMixed, mixed, HostFnEmitSignal, HostFnEmitTarget2)
	}
	return nil
}

// CheckSignalBudget 判定单 bar 发射总数是否超过 MaxSignalsPerBar。
func CheckSignalBudget(emissionCount int, symbolCount int32) error {
	max := MaxSignalsPerBar(symbolCount)
	if int32(emissionCount) > max {
		return fmt.Errorf("%w: %d emissions in one bar > MaxSignalsPerBar=%d (symbolCount=%d)",
			ErrSignalBudgetExceeded, emissionCount, max, symbolCount)
	}
	return nil
}

// CheckStateBudget 判定写入后的宿主侧 state 总量（A6 的宿主侧总量闸）。
//
// keyCount / totalBytes 是「本次写入之后」的值；调用方负责先算再写。
func CheckStateBudget(keyCount int, totalBytes int) error {
	if keyCount > MaxStateKeys {
		return fmt.Errorf("%w: %d keys > MaxStateKeys=%d", ErrStateBudgetExceeded, keyCount, MaxStateKeys)
	}
	if totalBytes > MaxInstanceStateBytes {
		return fmt.Errorf("%w: %d bytes > MaxInstanceStateBytes=%d",
			ErrStateBudgetExceeded, totalBytes, MaxInstanceStateBytes)
	}
	return nil
}

// ─── 哨兵错误 ───────────────────────────────────────────────────
//
// 全部走 errors.Is 判定，不靠字符串匹配：trap 的原因要能被上层分级
// （N1 的 ErrStrategyFault / ErrContractBreach 映射）而不被「跳过当天」吃掉。

var (
	// ErrAbiVersionUnknown 是「abi_version 不是 on_bar/1 或 on_bar/2」。
	ErrAbiVersionUnknown = errors.New("abi: unsupported abi_version")
	// ErrAbiVersionMismatch 是「声明的 abi_version 与 import section 自证的族不符」。
	ErrAbiVersionMismatch = errors.New("abi: declared abi_version contradicts the import section")
	// ErrAbiFamilyMissing 是「既不导 v1 也不导 v2」——协议版本无法自证。
	ErrAbiFamilyMissing = errors.New("abi: module imports neither the v1 nor the v2 host family")
	// ErrAbiFamilyMixed 是「v1 与 v2 家族同时导入」。
	ErrAbiFamilyMixed = errors.New("abi: module mixes the v1 and v2 host families")
	// ErrImportNotWhitelisted 是「env 之外或白名单之外的 import」。
	ErrImportNotWhitelisted = errors.New("abi: import outside the env whitelist")
	// ErrImportSignatureMismatch 是「名字在白名单里但签名与冻结契约不符」。
	// 这一条的存在是为了让「同名不同型」在 instantiate **之前**就有可读错误
	// （否则只能等到运行时撞一个类型错，且报告的是类型不匹配而非「协议版本不对」）。
	ErrImportSignatureMismatch = errors.New("abi: host import signature does not match the frozen contract")
	// ErrHostRegionMissing 是「v2 artifact 没声明宿主区导出符号」。
	ErrHostRegionMissing = errors.New("abi: host region exports are missing")
	// ErrHostRegionInvalid 是「宿主区重叠 / 未对齐 / 越界 / 低于下界」。
	ErrHostRegionInvalid = errors.New("abi: host region declaration is invalid")
	// ErrHostRegionOverlapsData 是「宿主区落在模块自己的 Data section 上」（QT-62）。
	ErrHostRegionOverlapsData = errors.New("abi: host region overlaps the module's own static data")
	// ErrHostRegionDataUnprovable 是「无法从 artifact 字节证明宿主区不踩模块数据」
	// （被动段 / 非常量 offset / 非零 memory index）。读不懂即拒绝，不当作空图。
	ErrHostRegionDataUnprovable = errors.New("abi: host region overlap cannot be disproved from the artifact bytes")
	// ErrParamsTooLarge 是「params 超过 MaxParamsBytes 或塞不进声明的区间」。
	ErrParamsTooLarge = errors.New("abi: strategy params exceed the frozen budget")
	// ErrScopeUnknown 是「scope 缺失或取了未知值」。
	ErrScopeUnknown = errors.New("abi: unknown or missing strategy scope")
	// ErrScopeMarketV1 是「声明 v1 却要 market scope」。
	ErrScopeMarketV1 = errors.New("abi: scope=market requires abi v2")
	// ErrActionIllegal 是「action 不在 {0,1,2,3}」。
	ErrActionIllegal = errors.New("abi: illegal signal action")
	// ErrStrengthOutOfRange 是「strength 非 finite 或 ∉ [0,1]」。
	ErrStrengthOutOfRange = errors.New("abi: signal strength out of range")
	// ErrTargetWeightOutOfRange 是「weight 非 finite 或 ∉ [-1,1]」。
	ErrTargetWeightOutOfRange = errors.New("abi: target weight out of range")
	// ErrEmitSourcesMixed 是「同 bar 同 symbol 混用 emit_signal 与 emit_target2」。
	ErrEmitSourcesMixed = errors.New("abi: emit_signal and emit_target2 used for the same bar and symbol")
	// ErrEmitSourceUnknown 是「emission 记录带了这个枚举之外的源」。
	ErrEmitSourceUnknown = errors.New("abi: unknown emit source")
	// ErrSignalBudgetExceeded 是「单 bar 发射数超过 MaxSignalsPerBar」。
	ErrSignalBudgetExceeded = errors.New("abi: per-bar signal budget exceeded")
	// ErrStateBudgetExceeded 是「宿主侧 state 总量/键数超限」。
	ErrStateBudgetExceeded = errors.New("abi: host-side state budget exceeded")
)
