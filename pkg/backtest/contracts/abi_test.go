package contracts

// QT-01 ③-① 的逐项落地：枚举、六类上限、宿主区声明、预算判定。
//
// 这些数字全部写成**绝对字面量断言**，不从常量反推：
// 把 assert.Equal(MaxStateValueBytes, 64<<10) 写成这样，等于让常量的任何改动
// 都自动通过——那正是护栏最没用的形态。阈值类守卫的输入必须独立于阈值本身。

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/domain"
)

// TestFrozenLimits_AreTheAdjudicatedBytes 对表 A6 / N5 / §2.2-7 冻结的六个上限。
//
// 每一个数字都对应一次「超限会怎样」的裁决，所以它们是契约而不是配置：
// 策略作者按上限写代码，事后调低 = 已出厂 artifact 运行期 trap。
func TestFrozenLimits_AreTheAdjudicatedBytes(t *testing.T) {
	assert.Equal(t, 4096, MaxParamsBytes, "A6: params 区 [0,4096) —— initialize 超限拒绝")
	assert.Equal(t, 65536, MaxStateValueBytes, "A6: 单个 state value ≤ 64 KiB")
	assert.Equal(t, 4194304, MaxInstanceStateBytes, "A6: 单实例 State 总字节 ≤ 4 MiB（宿主 Go 内存）")
	assert.Equal(t, 4096, MaxStateKeys, "A6: 单实例 key 数 ≤ 4096")
	assert.Equal(t, 262144, MaxScratchPerBar, "§2.2-11: per-bar bump allocator ≤ 256 KiB")

	// 下限与对齐同样冻结：它们是「host 不踩模块自己的数据」的判据（N5）。
	assert.Equal(t, 8, HostRegionAlignment, "N5: __host_params_ptr / __host_scratch_ptr 必须 8 字节对齐")
	assert.Equal(t, 4096, MinHostParamsPtr, "N5: 宿主区下界 = MaxParamsBytes")
	assert.Equal(t, 65536, MinHostScratchSize, "N5: __host_scratch_size 至少 64 KiB")

	// 单位一致性自检：64KiB / 4MiB / 256KiB 是设计文本里的口径，别让 <<10/<<20 打错。
	assert.Equal(t, 64*1024, MaxStateValueBytes)
	assert.Equal(t, 4*1024*1024, MaxInstanceStateBytes)
	assert.Equal(t, 256*1024, MaxScratchPerBar)
	assert.Equal(t, 4*1024, MaxStateKeys)
}

func TestMaxSignalsPerBar(t *testing.T) {
	// 判据 = 2 × symbolCount：每个标的最多「平旧仓 + 开新仓」两条（A6-7）。
	assert.Equal(t, int32(6), MaxSignalsPerBar(3))
	assert.Equal(t, int32(2), MaxSignalsPerBar(1))
	assert.Equal(t, int32(10000), MaxSignalsPerBar(5000), "全市场 5000 标的 → 每 bar 上限 10000")
	// 退化输入不下探成负数：负 symbolCount 会让「≤ 上限」恒成立，等于关掉守卫。
	assert.Equal(t, int32(0), MaxSignalsPerBar(0))
	assert.Equal(t, int32(0), MaxSignalsPerBar(-1))
}

func TestCheckSignalBudget(t *testing.T) {
	// 边界两侧各钉一次：3 个标的 → 上限 6。
	require.NoError(t, CheckSignalBudget(6, 3))
	require.NoError(t, CheckSignalBudget(0, 3))
	err := CheckSignalBudget(7, 3)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSignalBudgetExceeded)
	assert.Contains(t, err.Error(), "7 emissions")
	assert.Contains(t, err.Error(), "MaxSignalsPerBar=6")
}

func TestCheckStateBudget(t *testing.T) {
	require.NoError(t, CheckStateBudget(4096, 4194304), "两个上限定值本身必须放行")
	require.NoError(t, CheckStateBudget(0, 0))

	err := CheckStateBudget(4097, 10)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStateBudgetExceeded)
	assert.Contains(t, err.Error(), "MaxStateKeys=4096")

	err = CheckStateBudget(1, 4194305)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStateBudgetExceeded)
	assert.Contains(t, err.Error(), "MaxInstanceStateBytes=4194304")
}

// ─── 枚举 ─────────────────────────────────────────────────────

// TestFieldID_Enum 钉住 §7.1 冻结的 field_id 编号。
//
// 编号写死成字面量是因为 SDK 绑定与 NT 侧各自硬编码它们：0/1 顺序错一位，
// 就是「把 open 当 close 用」这种不报错、结果还看着合理的静默错。
func TestFieldID_Enum(t *testing.T) {
	assert.Equal(t, FieldID(0), FieldClose)
	assert.Equal(t, FieldID(1), FieldOpen)
	assert.Equal(t, FieldID(2), FieldHigh)
	assert.Equal(t, FieldID(3), FieldLow)
	assert.Equal(t, FieldID(4), FieldVolume)

	assert.Equal(t, []FieldID{FieldClose, FieldOpen, FieldHigh, FieldLow, FieldVolume}, FieldIDs())

	for _, f := range FieldIDs() {
		assert.Truef(t, f.Valid(), "field %d must be valid", int32(f))
	}
	// 枚举之外 → trap（不返回 0，§7.1）。
	assert.False(t, FieldID(5).Valid())
	assert.False(t, FieldID(-1).Valid())
	assert.False(t, FieldID(999).Valid())
}

// TestAction_Enum 钉住 A5 的 Action ↔ domain.Direction 一一对应。
func TestAction_Enum(t *testing.T) {
	assert.Equal(t, Action(0), ActionHold)
	assert.Equal(t, Action(1), ActionLong)
	assert.Equal(t, Action(2), ActionClose)
	assert.Equal(t, Action(3), ActionShort)
	assert.Equal(t, []Action{ActionHold, ActionLong, ActionClose, ActionShort}, Actions())

	want := map[Action]domain.Direction{
		ActionHold:  domain.DirectionHold,
		ActionLong:  domain.DirectionLong,
		ActionClose: domain.DirectionClose,
		ActionShort: domain.DirectionShort,
	}
	for a, d := range want {
		got, err := a.Direction()
		require.NoErrorf(t, err, "action %d must map", int32(a))
		assert.Equalf(t, d, got, "action %d must map to %s", int32(a), d)
	}

	// 枚举之外必须报错，且**不**回落 hold —— 猜错方向 = 买卖反向。
	for _, bad := range []Action{Action(4), Action(-1), Action(255)} {
		assert.Falsef(t, bad.Valid(), "action %d must be invalid", int32(bad))
		_, err := bad.Direction()
		require.Errorf(t, err, "action %d must not map", int32(bad))
		assert.ErrorIsf(t, err, ErrActionIllegal, "action %d must fail with ErrActionIllegal", int32(bad))
	}
}

func TestValidateStrength(t *testing.T) {
	// D26：Strength = 信号强度/置信度，finite ∈ [0,1]。定值两侧都钉。
	require.NoError(t, ValidateStrength(0))
	require.NoError(t, ValidateStrength(1))
	require.NoError(t, ValidateStrength(0.5))
	for _, bad := range []float64{-0.001, 1.001, -1, 2} {
		err := ValidateStrength(bad)
		require.Errorf(t, err, "strength %v must be rejected", bad)
		assert.ErrorIsf(t, err, ErrStrengthOutOfRange, "strength %v", bad)
	}
	for _, bad := range nonFinite() {
		err := ValidateStrength(bad)
		require.Error(t, err, "non-finite strength must be rejected")
		assert.ErrorIs(t, err, ErrStrengthOutOfRange)
	}
}

func TestValidateTargetWeight(t *testing.T) {
	// §8.2：emit_target2 的 weight ∈ [-1,1]；-1 = 不动，0 = 清仓。
	require.NoError(t, ValidateTargetWeight(-1))
	require.NoError(t, ValidateTargetWeight(0))
	require.NoError(t, ValidateTargetWeight(1))
	require.NoError(t, ValidateTargetWeight(-0.34))
	for _, bad := range []float64{-1.001, 1.001, -2, 2} {
		err := ValidateTargetWeight(bad)
		require.Errorf(t, err, "weight %v must be rejected", bad)
		assert.ErrorIsf(t, err, ErrTargetWeightOutOfRange, "weight %v", bad)
	}
	for _, bad := range nonFinite() {
		err := ValidateTargetWeight(bad)
		require.Error(t, err, "non-finite weight must be rejected")
		assert.ErrorIs(t, err, ErrTargetWeightOutOfRange)
	}
}

func TestEmitSource_String(t *testing.T) {
	// 报错文案要点名是哪个 emit 函数，否则「混用」无法定位。
	assert.Equal(t, "emit_signal", EmitSourceSignal.String())
	assert.Equal(t, "emit_target2", EmitSourceTarget.String())
	assert.Equal(t, "unknown(0)", EmitSource(0).String())
}

// ─── scope ────────────────────────────────────────────────────

func TestResolveScope(t *testing.T) {
	// v1：只能是 symbol。空 = 旧 artifact 没这字段，兼容期按 symbol 补（§3 末）。
	got, err := ResolveScope(HostAbiV1, "")
	require.NoError(t, err)
	assert.Equal(t, ScopeSymbol, got)

	got, err = ResolveScope(HostAbiV1, ScopeSymbol)
	require.NoError(t, err)
	assert.Equal(t, ScopeSymbol, got)

	// v1 + market 必须拒：v1 协议没有 symbol 维度，表达不了横截面（B3-4）。
	_, err = ResolveScope(HostAbiV1, ScopeMarket)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrScopeMarketV1)

	// v2：必须显式声明，未知值 fail-closed。
	got, err = ResolveScope(HostAbiV2, ScopeMarket)
	require.NoError(t, err)
	assert.Equal(t, ScopeMarket, got)

	for _, declared := range []StrategyScope{"", "MARKET", "cross", "per_symbol"} {
		_, err = ResolveScope(HostAbiV2, declared)
		require.Errorf(t, err, "scope %q must be rejected for v2", declared)
		assert.ErrorIsf(t, err, ErrScopeUnknown, "scope %q", declared)
	}

	// 未知 abi 版本本身也要拒（不"尽力解释"）。
	_, err = ResolveScope(HostAbiVersion(3), ScopeMarket)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAbiVersionUnknown)
}

func TestStrategyScope_Valid(t *testing.T) {
	assert.True(t, ScopeMarket.Valid())
	assert.True(t, ScopeSymbol.Valid())
	assert.False(t, StrategyScope("market ").Valid(), "尾空格不是合法 scope")
	assert.False(t, StrategyScope("").Valid())
}

// ─── 宿主区声明 ───────────────────────────────────────────────

// 一份合法声明：params [4096,8192)、scratch [32768,98304)，内存 2 页。
var validRegion = HostRegionDeclaration{ParamsPtr: 4096, ScratchPtr: 32768, ScratchSize: MaxStateValueBytes}

func TestHostRegionDeclaration_ValidAcceptsAFullyReservedLayout(t *testing.T) {
	require.NoError(t, validRegion.Validate(131072))
}

func TestHostRegionDeclaration_RejectsEveryFrozenViolation(t *testing.T) {
	tests := []struct {
		name       string
		decl       HostRegionDeclaration
		memory     uint32
		wantErrIs  error
		wantPhrase string
	}{
		{
			// 这就是「ptr 落在模块自己的数据段」那一条：TinyGo 的 data/stack 在
			// 线性内存低地址，声明在 4096 以下必然与模块数据重叠。
			name:       "params_ptr inside the module's own low data region",
			decl:       HostRegionDeclaration{ParamsPtr: 1024, ScratchPtr: 32768, ScratchSize: MaxStateValueBytes},
			memory:     131072,
			wantErrIs:  ErrHostRegionInvalid,
			wantPhrase: "below the reserved floor",
		},
		{
			name:       "params_ptr unaligned",
			decl:       HostRegionDeclaration{ParamsPtr: 4100, ScratchPtr: 32768, ScratchSize: MaxStateValueBytes},
			memory:     131072,
			wantErrIs:  ErrHostRegionInvalid,
			wantPhrase: "not 8-byte aligned",
		},
		{
			name:       "scratch_ptr unaligned",
			decl:       HostRegionDeclaration{ParamsPtr: 4096, ScratchPtr: 32770, ScratchSize: MaxStateValueBytes},
			memory:     131072,
			wantErrIs:  ErrHostRegionInvalid,
			wantPhrase: "not 8-byte aligned",
		},
		{
			// 重叠：params [4096,8192) 与 scratch [6144, ...) 交在 6144..8192。
			name:       "params overlaps scratch",
			decl:       HostRegionDeclaration{ParamsPtr: 4096, ScratchPtr: 6144, ScratchSize: MaxStateValueBytes},
			memory:     131072,
			wantErrIs:  ErrHostRegionInvalid,
			wantPhrase: "overlaps scratch",
		},
		{
			// 反向重叠：scratch 在前、params 落进 scratch 里。
			name:       "scratch overlaps params",
			decl:       HostRegionDeclaration{ParamsPtr: 32768, ScratchPtr: 4096, ScratchSize: MaxStateValueBytes},
			memory:     131072,
			wantErrIs:  ErrHostRegionInvalid,
			wantPhrase: "overlaps scratch",
		},
		{
			name:       "scratch beyond memory",
			decl:       HostRegionDeclaration{ParamsPtr: 4096, ScratchPtr: 98304, ScratchSize: MaxStateValueBytes},
			memory:     131072,
			wantErrIs:  ErrHostRegionInvalid,
			wantPhrase: "exceeds memory size",
		},
		{
			name:       "scratch smaller than the frozen minimum",
			decl:       HostRegionDeclaration{ParamsPtr: 4096, ScratchPtr: 32768, ScratchSize: 65535},
			memory:     131072,
			wantErrIs:  ErrHostRegionInvalid,
			wantPhrase: "required minimum scratch",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.decl.Validate(tc.memory)
			require.Error(t, err)
			assert.ErrorIs(t, err, tc.wantErrIs)
			assert.Contains(t, err.Error(), tc.wantPhrase)
		})
	}
}

// TestHostRegionDeclaration_NoOverflowNearUint32Max 是算术腿：
// ptr + size 用 uint64 比，否则 0xFFFFFFF8 + 64KiB 绕回小地址就成了「不越界」。
func TestHostRegionDeclaration_NoOverflowNearUint32Max(t *testing.T) {
	decl := HostRegionDeclaration{
		ParamsPtr:   0xFFFFFFF8,
		ScratchPtr:  0xFFFFFFF0,
		ScratchSize: MaxStateValueBytes,
	}
	err := decl.Validate(131072)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrHostRegionInvalid)
}

func TestValidateParamsWrite(t *testing.T) {
	// 满区间：room = 8192-4096 = 4096 = MaxParamsBytes，定值放行、+1 拒绝。
	decl := HostRegionDeclaration{ParamsPtr: 4096, ScratchPtr: 8192, ScratchSize: MaxStateValueBytes}
	require.NoError(t, ValidateParamsWrite(MaxParamsBytes, decl))
	err := ValidateParamsWrite(MaxParamsBytes+1, decl)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrParamsTooLarge)
	assert.Contains(t, err.Error(), "truncation is not allowed")

	// 窄区间：params 区被 scratch 挤到只剩 1024 字节 —— 预算取 min，不是 4096。
	narrow := HostRegionDeclaration{ParamsPtr: 4096, ScratchPtr: 5120, ScratchSize: MaxStateValueBytes}
	require.NoError(t, ValidateParamsWrite(1024, narrow))
	err = ValidateParamsWrite(1025, narrow)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrParamsTooLarge)
	assert.Contains(t, err.Error(), "= 1024")

	// 零长 params 永远合法（无参策略）。
	require.NoError(t, ValidateParamsWrite(0, decl))
	require.NoError(t, ValidateParamsWrite(0, HostRegionDeclaration{}))

	// scratch 在 params 之前（或同址）→ room=0，任何 params 都放不下。
	err = ValidateParamsWrite(1, HostRegionDeclaration{ParamsPtr: 4096, ScratchPtr: 4096, ScratchSize: MaxStateValueBytes})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrParamsTooLarge)
}

func TestHostRegionExports_NamesAreFrozen(t *testing.T) {
	// 符号名本身是 ABI 的一部分：SDK 模板导出这三个名字，改一个字符就找不到。
	assert.Equal(t, "__host_params_ptr", ExportHostParamsPtr)
	assert.Equal(t, "__host_scratch_ptr", ExportHostScratchPtr)
	assert.Equal(t, "__host_scratch_size", ExportHostScratchSize)
	assert.Equal(t, []string{ExportHostParamsPtr, ExportHostScratchPtr, ExportHostScratchSize}, HostRegionExports())
}

// ─── 模块静态数据 × 宿主区（QT-62）───────────────────────────────

// TestHostRegionDeclaration_CheckAgainstStaticData 钉「高地址重叠」这条新判据。
//
// QT-01 的下界只能拦住 < 4096；数据段一旦越过 4096，声明可以完全合法却仍然
// 踩在模块自己的变量上。下面每一条都用**绝对地址**写，不从常量反推。
func TestHostRegionDeclaration_CheckAgainstStaticData(t *testing.T) {
	decl := HostRegionDeclaration{ParamsPtr: 4096, ScratchPtr: 32768, ScratchSize: MaxStateValueBytes}

	tests := []struct {
		name       string
		image      StaticDataImage
		wantErrIs  error
		wantPhrase string
	}{
		{
			name:  "no static data at all",
			image: StaticDataImage{},
		},
		{
			name:  "segment before params (below the floor) does not overlap",
			image: StaticDataImage{Segments: []StaticDataSegment{{Offset: 1024, Length: 256}}},
		},
		{
			// 半开区间：段尾正好等于 params 起点不算重叠。
			name:  "segment ending exactly at the params start",
			image: StaticDataImage{Segments: []StaticDataSegment{{Offset: 3072, Length: 1024}}},
		},
		{
			name:  "zero-length segment inside params is not an overlap",
			image: StaticDataImage{Segments: []StaticDataSegment{{Offset: 5000, Length: 0}}},
		},
		{
			// 立项证据：32768 ≥ 下界，声明本身完全合法，但数据段就在那儿。
			name:       "segment at the scratch pointer",
			image:      StaticDataImage{Segments: []StaticDataSegment{{Offset: 32768, Length: 128}}},
			wantErrIs:  ErrHostRegionOverlapsData,
			wantPhrase: ExportHostScratchPtr,
		},
		{
			// 段尾落进 params 区间：起点在 0 也不算安全。
			name:       "segment tail crossing into params",
			image:      StaticDataImage{Segments: []StaticDataSegment{{Offset: 4000, Length: 200}}},
			wantErrIs:  ErrHostRegionOverlapsData,
			wantPhrase: ExportHostParamsPtr,
		},
		{
			// 段覆盖整个 scratch 区间（起点更低、尾巴更长）。
			name:       "segment spanning the whole scratch region",
			image:      StaticDataImage{Segments: []StaticDataSegment{{Offset: 32760, Length: 70000}}},
			wantErrIs:  ErrHostRegionOverlapsData,
			wantPhrase: ExportHostScratchPtr,
		},
		{
			name:       "unprovable image is fail-closed",
			image:      StaticDataImage{Unprovable: "data segment #0 is passive (bulk memory)"},
			wantErrIs:  ErrHostRegionDataUnprovable,
			wantPhrase: "passive",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := decl.CheckAgainstStaticData(tc.image)
			if tc.wantErrIs == nil {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.ErrorIs(t, err, tc.wantErrIs)
			assert.Contains(t, err.Error(), tc.wantPhrase)
		})
	}
}

// TestHostRegionDeclaration_CheckAgainstStaticDataNoWraparound 是算术腿：
// 段的 offset+length 在 uint64 里算，0xFFFFFFF8 + 64 仍是「远离宿主区的大地址」，
// 不得绕回低区从而**误报**重叠 —— 误报会让合法 artifact 被拒，和漏报一样坏。
func TestHostRegionDeclaration_CheckAgainstStaticDataNoWraparound(t *testing.T) {
	decl := HostRegionDeclaration{ParamsPtr: 4096, ScratchPtr: 32768, ScratchSize: MaxStateValueBytes}
	require.NoError(t, decl.CheckAgainstStaticData(StaticDataImage{
		Segments: []StaticDataSegment{{Offset: 0xFFFFFFF8, Length: 64}},
	}))
}

// ─── emit 源混用 ──────────────────────────────────────────────

func TestCheckEmitSources(t *testing.T) {
	tests := []struct {
		name       string
		emissions  []BarEmission
		wantErr    bool
		wantIs     error
		wantPhrase string
	}{
		{
			name:      "signal only",
			emissions: []BarEmission{{SymbolIdx: 0, Source: EmitSourceSignal}, {SymbolIdx: 1, Source: EmitSourceSignal}},
		},
		{
			name:      "target only",
			emissions: []BarEmission{{SymbolIdx: 0, Source: EmitSourceTarget}, {SymbolIdx: 1, Source: EmitSourceTarget}},
		},
		{
			// 关键对照：同一 bar 里 **不同** symbol 各用一种 emit 是合法的。
			// 混用的判据是「同 bar 同 symbol」，不是「同 bar 两种函数」。
			name:      "different symbols, different sources",
			emissions: []BarEmission{{SymbolIdx: 0, Source: EmitSourceSignal}, {SymbolIdx: 1, Source: EmitSourceTarget}},
		},
		{
			// 重复同一种源不构成混用（同一 symbol 连发两条 signal 是 A12 的事）。
			name:      "same source twice",
			emissions: []BarEmission{{SymbolIdx: 2, Source: EmitSourceTarget}, {SymbolIdx: 2, Source: EmitSourceTarget}},
		},
		{
			name:       "same symbol both sources: signal first",
			emissions:  []BarEmission{{SymbolIdx: 3, Source: EmitSourceSignal}, {SymbolIdx: 3, Source: EmitSourceTarget}},
			wantErr:    true,
			wantIs:     ErrEmitSourcesMixed,
			wantPhrase: "symbols [3]",
		},
		{
			name:       "same symbol both sources: target first",
			emissions:  []BarEmission{{SymbolIdx: 3, Source: EmitSourceTarget}, {SymbolIdx: 3, Source: EmitSourceSignal}},
			wantErr:    true,
			wantIs:     ErrEmitSourcesMixed,
			wantPhrase: "symbols [3]",
		},
		{
			name:      "multiple mixed symbols are listed sorted",
			emissions: []BarEmission{{SymbolIdx: 9, Source: EmitSourceSignal}, {SymbolIdx: 9, Source: EmitSourceTarget}, {SymbolIdx: 1, Source: EmitSourceTarget}, {SymbolIdx: 1, Source: EmitSourceSignal}},
			wantErr:   true,
			wantIs:    ErrEmitSourcesMixed,
		},
		{
			name:      "empty bar",
			emissions: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckEmitSources(tc.emissions)
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.ErrorIs(t, err, tc.wantIs)
			if tc.wantPhrase != "" {
				assert.Contains(t, err.Error(), tc.wantPhrase)
			}
			// 报错必须点名两个函数名，否则读者不知道是哪两套 sizing 打架。
			assert.Contains(t, err.Error(), HostFnEmitSignal)
			assert.Contains(t, err.Error(), HostFnEmitTarget2)
		})
	}

	// 多个混用 symbol 必须按升序列出（错误文案确定性，否则同一故障两次报不同顺序）。
	err := CheckEmitSources([]BarEmission{
		{SymbolIdx: 9, Source: EmitSourceSignal}, {SymbolIdx: 9, Source: EmitSourceTarget},
		{SymbolIdx: 1, Source: EmitSourceTarget}, {SymbolIdx: 1, Source: EmitSourceSignal},
		{SymbolIdx: 5, Source: EmitSourceSignal}, {SymbolIdx: 5, Source: EmitSourceTarget},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symbols [1 5 9]")

	err = CheckEmitSources([]BarEmission{{SymbolIdx: 0, Source: EmitSource(7)}})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEmitSourceUnknown)
}

// nonFinite 返回三个「非有限」数值。写成函数而不是字面量，是为了让上面那批
// 断言读起来是在测「有限性」这一条规则，而不是在测 math 包。
func nonFinite() []float64 { return []float64{math.NaN(), math.Inf(1), math.Inf(-1)} }
