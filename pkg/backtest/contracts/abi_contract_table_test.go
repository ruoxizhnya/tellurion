package contracts

// QT-01 ⑤ 的第一条验收：**契约表 × 设计文档 §2.1 逐项对表**。
//
// 下面这张 want 表是 adr-029-amendment-host-api-v2.md §2.1（含 §8 追加的
// emit_target2）的**人工抄写**。它刻意不把 hostFnContracts 当期望值——否则
// 这份测试只是在自证「代码等于代码」，改错了也发现不了。文档与代码谁漂了，
// 这里就红。

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHostABIContractTable_MatchesFrozenDocText(t *testing.T) {
	// want 表的每一个名字都是**字面量**，不是 Go 常量。
	//
	// 这条纪律是破坏验证逼出来的（QT-01）：原先写成 `{HostFnGetBar, …}`，于是
	// 把 `HostFnGetBar = "get_bar"` 改成 `"get_bar_x"` 时测试全绿——契约、注册表、
	// 校验器一起平移，没有任何一处发现「artifacts 绑的那个名字变了」。用常量
	// 引用被测对象，测的是「代码等于代码」；用字面量才测得到「代码等于文档」。
	want := []struct {
		name      string
		since     HostAbiVersion
		signature string
	}{
		// v1 家族（8 个，签名保留不动 —— §2.3「不改已有签名」）
		{"get_bar", HostAbiV1, "(i32,i32)->f64"},
		{"get_series_len", HostAbiV1, "()->i32"},
		{"get_symbol_count", HostAbiV1, "()->i32"},
		{"get_cross_section", HostAbiV1, "(i32,i32,i32)->f64"},
		{"get_state", HostAbiV1, "(i32,i32)->i64"},
		{"set_state", HostAbiV1, "(i32,i32,i32,i32)->void"},
		{"emit_signal", HostAbiV1, "(i32,i32,f64)->void"},
		{"log", HostAbiV1, "(i32,i32,i32)->void"},
		// v2 家族（4 个新增名字 —— §2.1 + §8）
		{"get_bar2", HostAbiV2, "(i32,i32,i32)->f64"},
		{"get_series_len2", HostAbiV2, "(i32)->i32"},
		{"get_date2", HostAbiV2, "()->i32"},
		{"emit_target2", HostAbiV2, "(i32,f64)->void"},
	}

	got := HostABIContracts()
	require.Len(t, got, len(want), "函数表行数与设计文档不符")

	// HostABIContracts 按名字排序；want 不排序，所以按名字查。
	for _, w := range want {
		c, ok := HostABIContract(w.name)
		require.Truef(t, ok, "contract table is missing %q (the wire name the doc freezes)", w.name)
		assert.Equalf(t, w.since, c.Since, "%s belongs to a different abi family than the doc says", w.name)
		assert.Equalf(t, w.signature, c.Signature(), "%s signature drifted from the doc", w.name)
		assert.NotEmptyf(t, c.Semantics, "%s has no frozen semantics text", w.name)
		assert.NotEmptyf(t, c.Traps, "%s has no frozen trap conditions", w.name)
	}

	// 表里既不能少（上面查不到就 Fatal）也不能多（Len 对不上就红）。
}

// TestFrozenWireNames 把**所有会出现在 artifact / 资产记录里的字符串**钉成字面量。
//
// 判据是「改一个字符 = 一次 ABI 破坏」的那一类：函数名、宿主区导出符号名、
// scope 取值、abi_version 文本。它们一旦漂移，已出厂的 artifact 会突然不被认出，
// 而这在编译期完全看不出来。
func TestFrozenWireNames(t *testing.T) {
	assert.Equal(t, "get_bar", HostFnGetBar)
	assert.Equal(t, "get_series_len", HostFnGetSeriesLen)
	assert.Equal(t, "get_symbol_count", HostFnGetSymbolCount)
	assert.Equal(t, "get_cross_section", HostFnGetCrossSection)
	assert.Equal(t, "get_state", HostFnGetState)
	assert.Equal(t, "set_state", HostFnSetState)
	assert.Equal(t, "emit_signal", HostFnEmitSignal)
	assert.Equal(t, "log", HostFnLog)
	assert.Equal(t, "get_bar2", HostFnGetBar2)
	assert.Equal(t, "get_series_len2", HostFnGetSeriesLen2)
	assert.Equal(t, "get_date2", HostFnGetDate2)
	assert.Equal(t, "emit_target2", HostFnEmitTarget2)

	assert.Equal(t, "__host_params_ptr", ExportHostParamsPtr)
	assert.Equal(t, "__host_scratch_ptr", ExportHostScratchPtr)
	assert.Equal(t, "__host_scratch_size", ExportHostScratchSize)

	assert.Equal(t, "market", string(ScopeMarket))
	assert.Equal(t, "symbol", string(ScopeSymbol))

	assert.Equal(t, "on_bar/1", HostAbiV1.String())
	assert.Equal(t, "on_bar/2", HostAbiV2.String())
	// 未冻结的版本号不能被伪装成 on_bar/3 —— 它是「不认识」，不是「支持」。
	assert.Equal(t, "unknown(3)", HostAbiVersion(3).String())
}

// TestHostFnCount_IsTwelve_NotTheThirteenTheDocsClaim 钉住一个**文档事实错误**。
//
// 设计集里「13 个 host 函数」出现在四处（adr-029 §7.2-1、p0-round2-review §8.4、
// repo-topology §合入门禁、docs/TASKS QT-01 标题与 ⑤），但 §7.2-1 自己给的枚举是
// 「8 个 v1 + get_bar2 + get_series_len2 + get_date2 + emit_target2」= **12**。
// 8+4 从来不等于 13。
//
// 这条测试的存在意义：把「13」这个数**留在测试里当反例**，而不是留在代码里当常量。
// 若将来真的补了第 13 个函数（例如 B4 里那条可选的 is_tradable），必须是先改文档、
// 走一次 abi 冻结，再让这个数变成 13 —— 不能先改代码。
func TestHostFnCount_IsTwelve_NotTheThirteenTheDocsClaim(t *testing.T) {
	assert.Equal(t, 12, HostFnCount(),
		"the frozen set is 8 v1 + 4 v2 = 12 host functions; the docs' \"13\" is an arithmetic error (see QT-01 completion record)")

	var v1, v2 int
	for _, c := range HostABIContracts() {
		switch c.Since {
		case HostAbiV1:
			v1++
		case HostAbiV2:
			v2++
		default:
			t.Fatalf("function %s has an unsupported abi family %v", c.Name, c.Since)
		}
	}
	assert.Equal(t, 8, v1, "v1 family must stay exactly the 8 already-implemented functions (§2.3: 不改已有签名)")
	assert.Equal(t, 4, v2, "v2 adds exactly get_bar2 / get_series_len2 / get_date2 / emit_target2")
}

// TestHostABI_V1SignaturesAreUnchanged 是「不改已有签名」的防漂移腿。
//
// 这条比上一条更贴代码：v1 的 8 个函数今天已由 pkg/sandbox/wasm 实现，签名一旦
// 在契约里被改写，v1 artifact 会立刻与注册表不符。判据写死成字面量。
func TestHostABI_V1SignaturesAreUnchanged(t *testing.T) {
	v1Want := map[string][]HostValueType{
		HostFnGetBar:          {HostI32, HostI32},
		HostFnGetSeriesLen:    nil,
		HostFnGetSymbolCount:  nil,
		HostFnGetCrossSection: {HostI32, HostI32, HostI32},
		HostFnGetState:        {HostI32, HostI32},
		HostFnSetState:        {HostI32, HostI32, HostI32, HostI32},
		HostFnEmitSignal:      {HostI32, HostI32, HostF64},
		HostFnLog:             {HostI32, HostI32, HostI32},
	}
	v1Results := map[string][]HostValueType{
		HostFnGetBar:          {HostF64},
		HostFnGetSeriesLen:    {HostI32},
		HostFnGetSymbolCount:  {HostI32},
		HostFnGetCrossSection: {HostF64},
		HostFnGetState:        {HostI64},
		HostFnSetState:        nil,
		HostFnEmitSignal:      nil,
		HostFnLog:             nil,
	}
	for name, params := range v1Want {
		c, ok := HostABIContract(name)
		require.Truef(t, ok, "v1 function %s disappeared from the contract table", name)
		assert.Equalf(t, HostAbiV1, c.Since, "%s is not v1", name)
		assert.Equalf(t, params, c.Params, "%s params drifted", name)
		assert.Equalf(t, v1Results[name], c.Results, "%s results drifted", name)
	}
}

func TestHostAbiVersionFromName(t *testing.T) {
	for _, tc := range []struct {
		text  string
		want  HostAbiVersion
		isErr bool
	}{
		{"on_bar/1", HostAbiV1, false},
		{"on_bar/2", HostAbiV2, false},
		{"on_bar/3", 0, true}, // 未冻结的版本号：不认识就拒绝，不"尽力解释"
		{"on_bar/0", 0, true}, //
		{"wasm/2", 0, true},   // 别的载体的 abi 文本不属于 host API
		{"", 0, true},
	} {
		got, err := HostAbiVersionFromName(tc.text)
		if tc.isErr {
			require.Errorf(t, err, "HostAbiVersionFromName(%q) must fail", tc.text)
			assert.ErrorIsf(t, err, ErrAbiVersionUnknown, "abi %q must fail with ErrAbiVersionUnknown", tc.text)
			continue
		}
		require.NoErrorf(t, err, "HostAbiVersionFromName(%q)", tc.text)
		assert.Equal(t, tc.want, got)
	}
}
