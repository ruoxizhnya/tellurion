package wasm

// QT-01 ⑤ 的第二条验收：**四类坏 artifact 全部被拒且错误可检索**，
// 外加「v1 artifact 仍能实例化」这条不能被新门打死的对照腿。
//
// 全部用真字节码（wasm_builder_test.go 编出来的合法模块）走真实加载路径，
// 不造假输入：契约校验最怕的形态是「单测喂一个结构体，生产喂的是 wasm 字节，
// 两边根本不是同一个东西」——那样护栏测的是自己的测试脚手架。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tetratelabs/wazero"

	"github.com/ruoxizhnya/tellurion-contracts/engine"
)

// ─── 测试模块构造 ─────────────────────────────────────────────

// namedGlobal 是一个带导出名的全局。
type namedGlobal struct {
	name    string
	i32     int32
	valType byte // 零值 → i32
}

// hostTypeByte 把契约值类型映射到编码器字节。
func hostTypeByte(t contracts.HostValueType) byte {
	switch t {
	case contracts.HostI32:
		return valI32
	case contracts.HostI64:
		return valI64
	case contracts.HostF64:
		return valF64
	default:
		panic("unknown host value type in test builder")
	}
}

// contractParamTypes 从契约表取一个 host 函数的参数类型字节。
func contractParamTypes(t *testing.T, name string) (params, results []byte) {
	t.Helper()
	c, ok := contracts.HostABIContract(name)
	require.Truef(t, ok, "%s is not in the contract table", name)
	for _, p := range c.Params {
		params = append(params, hostTypeByte(p))
	}
	for _, r := range c.Results {
		results = append(results, hostTypeByte(r))
	}
	return params, results
}

// buildAbiModule 造一个「on_bar 协议骨架 + 指定 import 族 + 指定宿主区声明 +
// 指定数据段」的模块。import 的签名一律从**契约表**取，所以除非刻意测「同名
// 不同型」，构造出来的模块天然满足签名校验。
func buildAbiModule(t *testing.T, importNames []string, globals []namedGlobal, data []wasmDataSegment, pages uint32) []byte {
	t.Helper()
	if pages == 0 {
		pages = 1
	}

	m := &wasmModule{}
	// 1) 每个 import 一个函数类型（按契约签名）
	for _, name := range importNames {
		p, r := contractParamTypes(t, name)
		m.types = append(m.types, wasmFuncType{params: p, results: r})
		m.imports = append(m.imports, wasmImport{module: HostModuleName, name: name, typeIdx: uint32(len(m.types) - 1)})
	}
	// 2) on_bar 协议的三个导出函数类型
	initIdx := uint32(len(m.types))
	m.types = append(m.types, wasmFuncType{params: []byte{valI32, valI32}, results: []byte{valI32}})
	onBarIdx := uint32(len(m.types))
	m.types = append(m.types, wasmFuncType{params: []byte{valI32}, results: []byte{valI64}})
	finIdx := uint32(len(m.types))
	m.types = append(m.types, wasmFuncType{results: []byte{valI64}})

	m.funcs = []uint32{initIdx, onBarIdx, finIdx}
	m.codes = [][]byte{
		{opI32Const, 0x00, opEnd},
		{opI64Const, 0x00, opEnd},
		{opI64Const, 0x00, opEnd},
	}
	m.memory = &wasmMemory{min: pages}

	for _, g := range globals {
		vt := g.valType
		if vt == 0 {
			vt = valI32
		}
		m.globals = append(m.globals, wasmGlobal{valType: vt, i32: g.i32})
	}
	base := m.funcIndexBase()
	m.export = []wasmExport{
		{name: "initialize", kind: expKindFunc, idx: base + 0},
		{name: "on_bar", kind: expKindFunc, idx: base + 1},
		{name: "finalize", kind: expKindFunc, idx: base + 2},
	}
	for i, g := range globals {
		m.export = append(m.export, wasmExport{name: g.name, kind: expKindGlobal, idx: uint32(i)})
	}
	m.data = data
	return m.encode()
}

// 一份合法宿主区声明：params [4096,8192)、scratch [32768,98304)，内存 2 页 = 131072。
func validRegionGlobals() []namedGlobal {
	return []namedGlobal{
		{name: contracts.ExportHostParamsPtr, i32: 4096},
		{name: contracts.ExportHostScratchPtr, i32: 32768},
		{name: contracts.ExportHostScratchSize, i32: contracts.MinHostScratchSize},
	}
}

func compileModule(t *testing.T, r *WazeroRuntime, bytes []byte) wazero.CompiledModule {
	t.Helper()
	cm, err := r.rt.CompileModule(context.Background(), bytes)
	require.NoError(t, err, "测试造的模块本身必须是合法 wasm（否则「被 abi 门拒绝」这条结论不成立）")
	t.Cleanup(func() { _ = cm.Close(context.Background()) })
	return cm
}

// ─── 契约表 × 运行时注册表 ────────────────────────────────────

// TestHostModuleRegistration_MatchesContractTable 钉住「契约里 12 个、运行时
// 也只注册 12 个，且类型逐一对应」。
//
// 这条是 configureHostModule 被抽成函数的**唯一理由**：不抽出来，「表与实现漂移」
// 就只能靠人眼比对文档。
func TestHostModuleRegistration_MatchesContractTable(t *testing.T) {
	ctx := context.Background()
	rt := wazero.NewRuntime(ctx)
	t.Cleanup(func() { _ = rt.Close(ctx) })

	hostMod, err := configureHostModule(rt.NewHostModuleBuilder(HostModuleName)).Instantiate(ctx)
	require.NoError(t, err)

	defs := hostMod.ExportedFunctionDefinitions()
	table := contracts.HostABIContracts()
	require.Len(t, defs, contracts.HostFnCount(), "host 模块注册的函数数与契约表不符")
	require.Len(t, table, contracts.HostFnCount())

	for _, c := range table {
		def, ok := defs[c.Name]
		require.Truef(t, ok, "contract table declares env.%s but the host module does not register it", c.Name)

		params, err := hostValueTypes(def.ParamTypes())
		require.NoErrorf(t, err, "env.%s uses a value type the contract cannot express", c.Name)
		results, err := hostValueTypes(def.ResultTypes())
		require.NoErrorf(t, err, "env.%s uses a value type the contract cannot express", c.Name)

		assert.Equalf(t, c.Params, params, "env.%s param types drifted from the contract", c.Name)
		assert.Equalf(t, c.Results, results, "env.%s result types drifted from the contract", c.Name)
	}

	// 反向：注册表里没有契约之外的多余函数（多注册一个名字 = 多给一个能力）。
	for name := range defs {
		_, ok := contracts.HostABIContract(name)
		assert.Truef(t, ok, "host module registers env.%s which is not in the frozen contract", name)
	}
}

// ─── 坏 artifact ①③：import 门 ────────────────────────────────

// TestValidateImports_MixedFamiliesRejected 是坏 artifact #3（v1+v2 混导）。
func TestValidateImports_MixedFamiliesRejected(t *testing.T) {
	r := newHostRuntime(t)
	cm := compileModule(t, r, buildAbiModule(t,
		[]string{contracts.HostFnGetBar, contracts.HostFnGetBar2}, validRegionGlobals(), nil, 2))

	err := ValidateImports(cm)
	require.Error(t, err, "同导 v1 与 v2 家族必须被拒（§2.3：一个 artifact 只属于一个协议族）")
	assert.ErrorIs(t, err, contracts.ErrAbiFamilyMixed)
	assert.Contains(t, err.Error(), contracts.HostFnGetBar)
	assert.Contains(t, err.Error(), contracts.HostFnGetBar2)

	// 同一条门在真实加载路径上也必须拦住（不只是单测里调函数）。
	sb := NewSandbox(r, Config{})
	_, err = sb.NewOnBarSession(context.Background(), buildAbiModule(t,
		[]string{contracts.HostFnGetBar, contracts.HostFnGetBar2}, validRegionGlobals(), nil, 2))
	require.Error(t, err)
	assert.ErrorIs(t, err, contracts.ErrAbiFamilyMixed)
}

// TestValidateImports_SignatureMismatch 钉「同名不同型」在 compile 期就有可读报错。
//
// 这条值得单独测：wasm import 按名字+类型匹配，只查名字的话，一个把 get_bar
// 按三参数导入的模块要等到 instantiate 才炸，报的是类型不匹配而不是「协议不对」
// （§7.2 点名的不可读错误）。
func TestValidateImports_SignatureMismatch(t *testing.T) {
	ctx := context.Background()
	// 手工造一个「import env.get_bar 但签名是 (i32,i32,i32)->f64」的模块。
	m := &wasmModule{
		types: []wasmFuncType{
			{params: []byte{valI32, valI32, valI32}, results: []byte{valF64}},
			{results: []byte{valI32}},
		},
		imports: []wasmImport{{module: HostModuleName, name: contracts.HostFnGetBar, typeIdx: 0}},
		funcs:   []uint32{1},
		codes:   [][]byte{{opI32Const, 0x00, opEnd}},
	}
	m.export = []wasmExport{{name: "junk", kind: expKindFunc, idx: m.funcIndexBase()}}

	r := newHostRuntime(t)
	cm, err := r.rt.CompileModule(ctx, m.encode())
	require.NoError(t, err, "模块本身合法，错的只是它导入的签名")
	t.Cleanup(func() { _ = cm.Close(ctx) })

	err = ValidateImports(cm)
	require.Error(t, err)
	assert.ErrorIs(t, err, contracts.ErrImportSignatureMismatch)
	assert.Contains(t, err.Error(), contracts.HostFnGetBar)
	assert.Contains(t, err.Error(), "(i32,i32)->f64", "报错必须给出契约要求的签名，否则无从对照")
}

// TestValidateImports_WhitelistIntactAfterNamesMovedToContracts 是搬迁对照腿。
//
// 函数名从 host_api.go 搬进契约表时最容易出的事故是「漏搬一个」——漏掉的那个
// 会突然变成 forbidden import，把一个能跑的 v1 artifact 判死。逐个过一遍。
func TestValidateImports_WhitelistIntactAfterNamesMovedToContracts(t *testing.T) {
	r := newHostRuntime(t)
	v1Names := []string{
		contracts.HostFnGetBar, contracts.HostFnGetSeriesLen, contracts.HostFnGetSymbolCount,
		contracts.HostFnGetCrossSection, contracts.HostFnGetState, contracts.HostFnSetState,
		contracts.HostFnEmitSignal, contracts.HostFnLog,
	}
	require.Len(t, v1Names, 8)
	for _, name := range v1Names {
		cm := compileModule(t, r, buildAbiModule(t, []string{name}, nil, nil, 1))
		require.NoErrorf(t, ValidateImports(cm), "v1 host function %s must still be whitelisted", name)
	}
	for _, name := range []string{contracts.HostFnGetBar2, contracts.HostFnGetSeriesLen2, contracts.HostFnGetDate2, contracts.HostFnEmitTarget2} {
		cm := compileModule(t, r, buildAbiModule(t, []string{name}, validRegionGlobals(), nil, 2))
		require.NoErrorf(t, ValidateImports(cm), "v2 host function %s must be whitelisted", name)
	}
}

// ─── 协议族自证 + 元数据交叉校验 ──────────────────────────────

func TestDetectHostAbiFamily(t *testing.T) {
	r := newHostRuntime(t)

	cm := compileModule(t, r, buildAbiModule(t, []string{contracts.HostFnGetBar}, nil, nil, 1))
	family, err := DetectHostAbiFamily(cm)
	require.NoError(t, err)
	assert.Equal(t, contracts.HostAbiV1, family)

	cm = compileModule(t, r, buildAbiModule(t, []string{contracts.HostFnGetBar2}, validRegionGlobals(), nil, 2))
	family, err = DetectHostAbiFamily(cm)
	require.NoError(t, err)
	assert.Equal(t, contracts.HostAbiV2, family)

	// 零 env import：合法模块，但不是策略 artifact —— 由族判定拒绝，不由白名单拒绝。
	cm = compileModule(t, r, wasmReturnZero())
	_, err = DetectHostAbiFamily(cm)
	require.Error(t, err)
	assert.ErrorIs(t, err, contracts.ErrAbiFamilyMissing)

	cm = compileModule(t, r, buildAbiModule(t, []string{contracts.HostFnGetBar, contracts.HostFnGetBar2}, validRegionGlobals(), nil, 2))
	_, err = DetectHostAbiFamily(cm)
	require.Error(t, err)
	assert.ErrorIs(t, err, contracts.ErrAbiFamilyMixed)
}

// TestValidateArtifactAbi_CrossCheckRejectsContradictingMetadata 是坏 artifact #4
// （声明的 abi_version 与 import 族不符）。
//
// 判据刻意做成「自证族 × 元数据」两个方向各测一次：只测一个方向的交叉校验，
// 等于允许另一半伪造。
func TestValidateArtifactAbi_CrossCheckRejectsContradictingMetadata(t *testing.T) {
	r := newHostRuntime(t)

	// 字节是 v2，记录写 v1。
	cm := compileModule(t, r, buildAbiModule(t, []string{contracts.HostFnGetBar2}, validRegionGlobals(), nil, 2))
	_, err := ValidateArtifactAbi(cm, contracts.HostAbiV1)
	require.Error(t, err)
	assert.ErrorIs(t, err, contracts.ErrAbiVersionMismatch)
	assert.Contains(t, err.Error(), "self-proves on_bar/2", "报错必须点名「谁说的不算」")

	// 字节是 v1，记录写 v2。
	cm = compileModule(t, r, buildAbiModule(t, []string{contracts.HostFnGetBar}, nil, nil, 1))
	_, err = ValidateArtifactAbi(cm, contracts.HostAbiV2)
	require.Error(t, err)
	assert.ErrorIs(t, err, contracts.ErrAbiVersionMismatch)

	// 一致的两条腿必须放行。
	family, err := ValidateArtifactAbi(cm, contracts.HostAbiV1)
	require.NoError(t, err)
	assert.Equal(t, contracts.HostAbiV1, family)

	// 未知的版本号：拒绝，不"尽力解释"。
	_, err = ValidateArtifactAbi(cm, contracts.HostAbiVersion(3))
	require.Error(t, err)
	assert.ErrorIs(t, err, contracts.ErrAbiVersionUnknown)
}

// ─── 坏 artifact ①②：宿主区门 ─────────────────────────────────

func newSessionWith(t *testing.T, importNames []string, globals []namedGlobal, data []wasmDataSegment, pages uint32) (*OnBarSession, error) {
	t.Helper()
	r := newHostRuntime(t)
	sb := NewSandbox(r, Config{})
	return sb.NewOnBarSession(context.Background(), buildAbiModule(t, importNames, globals, data, pages))
}

// TestNewOnBarSession_V2MissingHostRegionExports 是坏 artifact #1。
func TestNewOnBarSession_V2MissingHostRegionExports(t *testing.T) {
	// 只声明两个：缺 __host_params_ptr。
	globals := []namedGlobal{
		{name: contracts.ExportHostScratchPtr, i32: 32768},
		{name: contracts.ExportHostScratchSize, i32: contracts.MinHostScratchSize},
	}
	_, err := newSessionWith(t, []string{contracts.HostFnGetBar2}, globals, nil, 2)
	require.Error(t, err, "abi v2 缺宿主区声明必须 fail-closed（§8：只能靠 TinyGo 布局巧合留空不算契约）")
	assert.ErrorIs(t, err, contracts.ErrHostRegionMissing)
	assert.Contains(t, err.Error(), contracts.ExportHostParamsPtr, "报错必须点名缺哪个符号")
	assert.Contains(t, err.Error(), "v2 SDK template", "报错必须给出可执行的下一步，而不是一句加载失败")

	// 三个全缺：报错要把三个名字都列出来（否则要试三轮）。
	_, err = newSessionWith(t, []string{contracts.HostFnGetBar2}, nil, nil, 2)
	require.Error(t, err)
	assert.ErrorIs(t, err, contracts.ErrHostRegionMissing)
	for _, name := range contracts.HostRegionExports() {
		assert.Contains(t, err.Error(), name)
	}
}

// TestNewOnBarSession_V2ParamsPtrInsideModuleDataSegment 是坏 artifact #2。
//
// 模块在 1024 处有**真实数据段**（TinyGo 的 data/stack 就在这个低区），并把
// __host_params_ptr 声明成同一段地址。判据是 MinHostParamsPtr 下界：落在低区
// 的声明必然与模块自身数据重叠，host 一写 params 就改掉了策略的变量。
func TestNewOnBarSession_V2ParamsPtrInsideModuleDataSegment(t *testing.T) {
	data := []wasmDataSegment{{offset: 1024, bytes: make([]byte, 256)}}
	globals := []namedGlobal{
		{name: contracts.ExportHostParamsPtr, i32: 1024},
		{name: contracts.ExportHostScratchPtr, i32: 32768},
		{name: contracts.ExportHostScratchSize, i32: contracts.MinHostScratchSize},
	}
	_, err := newSessionWith(t, []string{contracts.HostFnGetBar2}, globals, data, 2)
	require.Error(t, err, "params 指针落在模块自己的数据段必须被拒")
	assert.ErrorIs(t, err, contracts.ErrHostRegionInvalid)
	assert.Contains(t, err.Error(), "below the reserved floor")

	// 未对齐的同段地址同样被拒（1032 是 8 对齐但仍在低区）。
	globals[0].i32 = 1032
	_, err = newSessionWith(t, []string{contracts.HostFnGetBar2}, globals, data, 2)
	require.Error(t, err)
	assert.ErrorIs(t, err, contracts.ErrHostRegionInvalid)

	// 对照腿：把同一个声明挪到下界之上、数据段之外 ⇒ 必须放行。
	// 没有这条对照，上面的拒绝可能只是「v2 一律加载失败」。
	globals[0].i32 = 4096
	_, err = newSessionWith(t, []string{contracts.HostFnGetBar2}, globals, data, 2)
	require.NoError(t, err)
}

// TestNewOnBarSession_V2RegionBeyondMemoryOrOverlapping 钉越界与重叠两分支。
// 这两个分支只能靠内存大小与区间算术判，与低区下界是三条独立的腿。
func TestNewOnBarSession_V2RegionBeyondMemoryOrOverlapping(t *testing.T) {
	// scratch 尾部越过 1 页（65536）内存。
	_, err := newSessionWith(t, []string{contracts.HostFnGetBar2}, []namedGlobal{
		{name: contracts.ExportHostParamsPtr, i32: 4096},
		{name: contracts.ExportHostScratchPtr, i32: 8192},
		{name: contracts.ExportHostScratchSize, i32: contracts.MinHostScratchSize},
	}, nil, 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, contracts.ErrHostRegionInvalid)
	assert.Contains(t, err.Error(), "exceeds memory size")

	// params 与 scratch 重叠。
	_, err = newSessionWith(t, []string{contracts.HostFnGetBar2}, []namedGlobal{
		{name: contracts.ExportHostParamsPtr, i32: 4096},
		{name: contracts.ExportHostScratchPtr, i32: 6144},
		{name: contracts.ExportHostScratchSize, i32: contracts.MinHostScratchSize},
	}, nil, 2)
	require.Error(t, err)
	assert.ErrorIs(t, err, contracts.ErrHostRegionInvalid)
	assert.Contains(t, err.Error(), "overlaps scratch")

	// scratch 未对齐。
	_, err = newSessionWith(t, []string{contracts.HostFnGetBar2}, []namedGlobal{
		{name: contracts.ExportHostParamsPtr, i32: 4096},
		{name: contracts.ExportHostScratchPtr, i32: 32770},
		{name: contracts.ExportHostScratchSize, i32: contracts.MinHostScratchSize},
	}, nil, 2)
	require.Error(t, err)
	assert.ErrorIs(t, err, contracts.ErrHostRegionInvalid)
	assert.Contains(t, err.Error(), "not 8-byte aligned")

	// 声明的 scratch 小于冻结下限。
	_, err = newSessionWith(t, []string{contracts.HostFnGetBar2}, []namedGlobal{
		{name: contracts.ExportHostParamsPtr, i32: 4096},
		{name: contracts.ExportHostScratchPtr, i32: 32768},
		{name: contracts.ExportHostScratchSize, i32: 65535},
	}, nil, 2)
	require.Error(t, err)
	assert.ErrorIs(t, err, contracts.ErrHostRegionInvalid)
	assert.Contains(t, err.Error(), "required minimum scratch")
}

// TestReadHostRegion_RejectsWrongGlobalType 钉「符号存在但类型不对」。
func TestReadHostRegion_RejectsWrongGlobalType(t *testing.T) {
	r := newHostRuntime(t)
	sb := NewSandbox(r, Config{})
	// params 声明成 i64：值能读出来，但它不是契约要的 i32。
	inst, err := sb.NewOnBarSession(context.Background(), buildAbiModule(t,
		[]string{contracts.HostFnGetBar2},
		[]namedGlobal{
			{name: contracts.ExportHostParamsPtr, i32: 4096, valType: valI64},
			{name: contracts.ExportHostScratchPtr, i32: 32768},
			{name: contracts.ExportHostScratchSize, i32: contracts.MinHostScratchSize},
		}, nil, 2))
	if err == nil {
		defer inst.Close()
		t.Fatal("expected the wrong-typed declaration symbol to be rejected at load")
	}
	assert.ErrorIs(t, err, contracts.ErrHostRegionInvalid)
	assert.Contains(t, err.Error(), contracts.ExportHostParamsPtr)
}

// ─── v1 对照腿：新门不能打死现有 artifact ─────────────────────

// TestNewOnBarSession_V1ArtifactStillLoadsWithLegacyDiagnostic 是「v1 仍能跑」
// 与「v1 走白名单跳过符号校验」两条腿的合体。
//
// wasmAccumulator 是既有测试里那个 v1 模块：导入 get_bar、不带任何宿主区符号
// ——正是「现有 14 个内置 artifact 都没这些符号」的形状。
func TestNewOnBarSession_V1ArtifactStillLoadsWithLegacyDiagnostic(t *testing.T) {
	r := newHostRuntime(t)
	sb := NewSandbox(r, Config{})
	sess, err := sb.NewOnBarSession(context.Background(), wasmAccumulator())
	require.NoError(t, err, "v1 artifact 必须仍然能实例化：一律 fail-closed 会把兼容期全打死")
	defer sess.Close()

	require.NoError(t, sess.Initialize(context.Background(), []byte(`{"lookback":5}`)))
	bc := bcWithCloses([]float64{1, 2, 3}, 0)
	require.NoError(t, sess.OnBar(context.Background(), bc))
	out, err := sess.Finalize(context.Background(), bc)
	require.NoError(t, err)
	assert.Empty(t, out)

	// v1 的 legacy 分支要**显式**给诊断，不能静默放行。
	diags, err := ValidateLoadedArtifact(sess.inst, contracts.HostAbiV1, contracts.StaticDataImage{})
	require.NoError(t, err)
	assert.Equal(t, []string{DiagLegacyHostLayout}, diags)
}

func TestValidateLoadedArtifact_RequiresWazeroInstance(t *testing.T) {
	// Instance 接口本身不暴露 global 自省；非 wazero 实现必须得到点名运行时的错误，
	// 而不是 nil panic 或「缺符号」。
	_, err := ValidateLoadedArtifact(stubInstance{}, contracts.HostAbiV2, contracts.StaticDataImage{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wazero")
}

// stubInstance 只为测上面那条断言分支存在，不参与任何 wasm 执行。
type stubInstance struct{}

func (stubInstance) WriteMemory(uint32, []byte) error          { return nil }
func (stubInstance) ReadMemory(uint32, uint32) ([]byte, error) { return nil, nil }
func (stubInstance) MemorySize() uint32                        { return 0 }
func (stubInstance) Call(context.Context, string, ...uint64) ([]uint64, error) {
	return nil, nil
}
func (stubInstance) Close() error { return nil }

// ─── pending 函数体：注册了但没接线，调用必须 trap ────────────

// TestPendingV2HostFunctions_Trap 钉住「注册 ≠ 可用」。
//
// QT-24 之后 v2 数据面三个已接线，只剩 emit_target2 仍为骨架（QT-26）。
// 本测试用它证明：契约里有名字、运行时能实例化，但函数体仍 trap ——
// 任何 v2 artifact 在 QT-26 落地前都不能把目标权重路径当成可用能力。
func TestPendingV2HostFunctions_Trap(t *testing.T) {
	ctx := context.Background()
	m := &wasmModule{
		types: []wasmFuncType{
			{params: []byte{valI32, valF64}}, // emit_target2(symbol_idx, weight)
			{},                               // run()
		},
		imports: []wasmImport{{module: HostModuleName, name: contracts.HostFnEmitTarget2, typeIdx: 0}},
		funcs:   []uint32{1},
		codes: [][]byte{
			appendF64Const([]byte{opI32Const, 0x00}, 0.5),
		},
	}
	// 上面的 appendF64Const 把 f64.const 接在 i32.const 之后，再接 call。
	m.codes[0] = append(m.codes[0], opCall, 0x00, opEnd)
	m.export = []wasmExport{{name: "run", kind: expKindFunc, idx: m.funcIndexBase()}}

	r := newHostRuntime(t)
	mod, err := r.Compile(ctx, m.encode())
	require.NoError(t, err)
	inst, err := mod.Instantiate(ctx, DefaultMaxMemory)
	require.NoError(t, err)
	defer inst.Close()

	bc := bcWithCloses([]float64{1}, 0)
	bc.Symbols = []string{"A"}
	_, err = inst.Call(withBarContext(ctx, bc), "run")
	require.Error(t, err, "emit_target2 函数体没接线，调用必须 trap 而不是静默")
	assert.Contains(t, err.Error(), contracts.HostFnEmitTarget2)
	assert.Contains(t, err.Error(), "QT-26")
}

// ─── params 上限 ────────────────────────────────────────────

// TestInitialize_RejectsParamsOverFrozenLimit 是 A6 的「params 4097 字节 → 拒绝
// 且错误可读」，定值两侧各钉一次。
func TestInitialize_RejectsParamsOverFrozenLimit(t *testing.T) {
	r := newHostRuntime(t)
	sb := NewSandbox(r, Config{})
	sess, err := sb.NewOnBarSession(context.Background(), wasmAccumulator())
	require.NoError(t, err)
	defer sess.Close()

	require.NoError(t, sess.Initialize(context.Background(), make([]byte, contracts.MaxParamsBytes)),
		"恰好等于上限必须放行")
	err = sess.Initialize(context.Background(), make([]byte, contracts.MaxParamsBytes+1))
	require.Error(t, err)
	assert.ErrorIs(t, err, contracts.ErrParamsTooLarge)
	assert.Contains(t, err.Error(), "4097")
	assert.Contains(t, err.Error(), "MaxParamsBytes=4096")
}
