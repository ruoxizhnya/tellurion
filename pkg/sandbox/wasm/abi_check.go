package wasm

// QT-01 · abi 契约校验：把 pkg/backtest/contracts 冻结下来的函数表
// 变成加载路径上真的会拒绝的三道门。
//
// 三道门的时序（这条时序本身就是契约的一部分）：
//
//	Compile ──► ValidateImports          （白名单 + 族互斥 + 签名比对，字节码级）
//	        ──► ValidateArtifactAbi      （import 自证族 × 资产记录的 abi_version）
//	Instantiate ──► ValidateLoadedArtifact（v2 才查宿主区导出符号；v1 走 legacy 分支）
//	        ──► Initialize               （params 长度 ≤ 声明的区间，不截断）
//
// 为什么族判定看 import section 而不是看 abi_version：abi_version 是编译服务
// 写在资产记录里的**元数据**，artifact 字节改不了自己的 import 表。以元数据
// 为准等于让「声明」说了算；以 import 为准才是「artifact 自身可验证」（§2.3）。

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tetratelabs/wazero/api"

	"tellurion/pkg/backtest/contracts"
)

// AbiModule 是 abi 校验需要的最小自省面：只要能列出 import 的函数定义。
//
// 为什么不直接收 wazero.CompiledModule：加载器手上拿到的是本包的 CompiledModule
// 包装（sandbox.Compile 的返回值），而 wazero 那个类型是它的私有字段。收接口
// 让两条路径（runtime 内部 + 会话层）用同一道门，且 wazero.CompiledModule 本身
// 就满足这个接口，所以老调用方一行都不用改。
type AbiModule interface {
	ImportedFunctions() []api.FunctionDefinition
}

// ValidateImports 扫描编译后模块的 import section，拒绝任何「非 env 模块」或
// 「白名单之外」的 import，并做版本族互斥与签名比对。
//
// ADR-029 §3.4 的 fail-closed 能力隔离：清单靠扫描 import section 得出。
//
// 分工说明（避免把两件事挤进一个函数）：「既不导 v1 也不导 v2」在这里**不判**
// —— Compile 是沙箱的通用入口，零 env import 的模块（工具/测试用的、或只做
// 内存搬运的）是一个「没有任何能力的合法模块」，它不是一个策略 artifact。
// 「策略 artifact 必须自证一族」发生在 ValidateArtifactAbi（加载路径）。
func ValidateImports(cm AbiModule) error {
	var forbidden []string
	var sigMismatch []string
	var sawV1, sawV2 bool

	for _, fn := range cm.ImportedFunctions() {
		modName, name, isImport := fn.Import()
		if !isImport {
			continue
		}
		if modName != HostModuleName {
			forbidden = append(forbidden, fmt.Sprintf("%s.%s", modName, name))
			continue
		}
		c, ok := contracts.HostABIContract(name)
		if !ok {
			forbidden = append(forbidden, fmt.Sprintf("%s.%s", modName, name))
			continue
		}
		if c.Since == contracts.HostAbiV2 {
			sawV2 = true
		} else {
			sawV1 = true
		}
		if err := checkImportedSignature(c, fn); err != nil {
			sigMismatch = append(sigMismatch, err.Error())
		}
	}

	if len(forbidden) > 0 {
		sort.Strings(forbidden)
		return fmt.Errorf("wasm: forbidden imports %v (only the frozen env whitelist is allowed): %w",
			forbidden, contracts.ErrImportNotWhitelisted)
	}
	// 族互斥（§2.3）：一个 artifact 只能属于一个协议族。混导意味着它自己
	// 都说不清要哪套语义，让 v2 的 get_bar2 去接 v1 的 per-symbol 上下文
	// 会造出一个「两个版本中间态」的静默错。
	if sawV1 && sawV2 {
		return fmt.Errorf("wasm: module imports both the v1 family (%s…) and the v2 family (%s…): %w",
			contracts.HostFnGetBar, contracts.HostFnGetBar2, contracts.ErrAbiFamilyMixed)
	}
	if len(sigMismatch) > 0 {
		sort.Strings(sigMismatch)
		return fmt.Errorf("wasm: %d host import(s) do not match the frozen signature; %w: %s",
			len(sigMismatch), contracts.ErrImportSignatureMismatch, sigMismatch[0])
	}
	return nil
}

// DetectHostAbiFamily 由 import section **自证**协议族。
//
// 返回 contracts.ErrAbiFamilyMissing（一对都不导：不是策略 artifact）或
// contracts.ErrAbiFamilyMixed（同时导）。判定只看 env 模块里被冻结的那 12 个名字。
func DetectHostAbiFamily(cm AbiModule) (contracts.HostAbiVersion, error) {
	var sawV1, sawV2 bool
	for _, fn := range cm.ImportedFunctions() {
		modName, name, isImport := fn.Import()
		if !isImport || modName != HostModuleName {
			continue
		}
		c, ok := contracts.HostABIContract(name)
		if !ok {
			continue
		}
		if c.Since == contracts.HostAbiV2 {
			sawV2 = true
		} else {
			sawV1 = true
		}
	}
	switch {
	case sawV1 && sawV2:
		return 0, fmt.Errorf("wasm: import section mixes v1 and v2 host families: %w", contracts.ErrAbiFamilyMixed)
	case sawV2:
		return contracts.HostAbiV2, nil
	case sawV1:
		return contracts.HostAbiV1, nil
	default:
		return 0, fmt.Errorf(
			"wasm: import section declares no host protocol family (neither %s nor %s); a strategy artifact must import at least one: %w",
			contracts.HostFnGetBar, contracts.HostFnGetBar2, contracts.ErrAbiFamilyMissing)
	}
}

// CrossCheckAbiVersion 把「自证族」与「资产记录声明的 abi_version」对撞。
//
// 不符即 fail-closed，且发生在 instantiate **之前**——错误必须是「协议版本不对」
// 而不是「import 类型不匹配」（§7.2-3）。
func CrossCheckAbiVersion(selfProved, declared contracts.HostAbiVersion) error {
	if !declared.Valid() {
		return fmt.Errorf("wasm: declared abi_version %v is not supported: %w", declared, contracts.ErrAbiVersionUnknown)
	}
	if selfProved != declared {
		return fmt.Errorf("wasm: artifact declares abi_version=%v but its import section self-proves %v: %w",
			declared, selfProved, contracts.ErrAbiVersionMismatch)
	}
	return nil
}

// ValidateArtifactAbi 是加载器用的组合门：自证 + 交叉校验。
//
// declaredVersion 来自策略资产记录的 abi_version（`on_bar/N` → HostAbiVersion）。
func ValidateArtifactAbi(cm AbiModule, declaredVersion contracts.HostAbiVersion) (contracts.HostAbiVersion, error) {
	family, err := DetectHostAbiFamily(cm)
	if err != nil {
		return 0, err
	}
	if err := CrossCheckAbiVersion(family, declaredVersion); err != nil {
		return 0, err
	}
	return family, nil
}

// checkImportedSignature 比对一行契约与模块实际导入的函数类型。
//
// 为什么值得单独判：wasm import 按「名字 + 类型」匹配，同名不同型只会在
// instantiate 时炸，报的是类型不匹配而不是「协议版本不对」（§7.2）。把类型
// 比对提到 compile 期，一个 v1 artifact 误用 v2 签名的 get_bar 就能被点名。
func checkImportedSignature(c contracts.HostFnContract, def api.FunctionDefinition) error {
	params, err := hostValueTypes(def.ParamTypes())
	if err != nil {
		return fmt.Errorf("env.%s: params: %w", c.Name, err)
	}
	results, err := hostValueTypes(def.ResultTypes())
	if err != nil {
		return fmt.Errorf("env.%s: results: %w", c.Name, err)
	}
	if !sameTypes(params, c.Params) || !sameTypes(results, c.Results) {
		return fmt.Errorf("env.%s is imported as (%s)->%s but the frozen contract is %s",
			c.Name, joinTypes(params), joinTypesOrVoid(results), c.Signature())
	}
	return nil
}

// hostValueTypes 把 wazero 的值类型映射到契约层的 HostValueType。
// 契约包是叶子包、不 import wazero，所以映射表放在这一侧。
func hostValueTypes(vts []api.ValueType) ([]contracts.HostValueType, error) {
	if len(vts) == 0 {
		// 返回 nil 而不是空切片：契约表里「无参数 / 无返回值」写的就是 nil，
		// 两边口径统一，等值比较才不会被 nil-vs-[] 这种无意义差异绊倒。
		return nil, nil
	}
	out := make([]contracts.HostValueType, len(vts))
	for i, vt := range vts {
		switch vt {
		case api.ValueTypeI32:
			out[i] = contracts.HostI32
		case api.ValueTypeI64:
			out[i] = contracts.HostI64
		case api.ValueTypeF64:
			out[i] = contracts.HostF64
		default:
			return nil, fmt.Errorf("value type 0x%x is not expressible in the frozen host ABI", uint8(vt))
		}
	}
	return out, nil
}

func sameTypes(a, b []contracts.HostValueType) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func joinTypes(ts []contracts.HostValueType) string {
	parts := make([]string, len(ts))
	for i, t := range ts {
		parts[i] = t.String()
	}
	return strings.Join(parts, ",")
}

func joinTypesOrVoid(ts []contracts.HostValueType) string {
	if len(ts) == 0 {
		return "void"
	}
	return joinTypes(ts)
}

// ─── 宿主区（host region）校验骨架 ──────────────────────────────

// DiagLegacyHostLayout 是 v1 artifact 的诊断标记：它没有（也不该有）宿主区
// 导出符号，host 只能按「已知内置模板布局」把 params 写在 0、scratch 写在 4096。
//
// 这不是「已验证的安全」，是**已登记的巧合**（N5：换编译器版本、加一个全局变量
// 就会让 host 的写踩到模块自己的数据）。兼容期允许，新产物一律要求声明。
const DiagLegacyHostLayout = "legacy_host_layout"

// hostRegionModule 是宿主区自省需要的最小面（*wazeroInstance 满足）。
// 刻意不扩 Instance 接口：给一个已经导出的接口加方法，对 S0~S3 期间编译连体的
// agent 仓是一次无谓的 breaking。
type hostRegionModule interface {
	ExportedGlobal(name string) api.Global
	MemorySize() uint32
}

// ReadHostRegion 读模块声明的三段宿主区地址。
//
// 缺失 → contracts.ErrHostRegionMissing（点名缺哪几个 + 让人用 v2 SDK 模板
// 重编，而不是一句「加载失败」）；类型不是 i32 → contracts.ErrHostRegionInvalid。
func ReadHostRegion(mod hostRegionModule) (contracts.HostRegionDeclaration, error) {
	var decl contracts.HostRegionDeclaration
	var missing []string

	targets := []struct {
		name string
		dst  *uint32
	}{
		{contracts.ExportHostParamsPtr, &decl.ParamsPtr},
		{contracts.ExportHostScratchPtr, &decl.ScratchPtr},
		{contracts.ExportHostScratchSize, &decl.ScratchSize},
	}
	for _, t := range targets {
		g := mod.ExportedGlobal(t.name)
		if g == nil {
			missing = append(missing, t.name)
			continue
		}
		if g.Type() != api.ValueTypeI32 {
			return contracts.HostRegionDeclaration{}, fmt.Errorf(
				"wasm: %w: %s must be an i32 global, got %v",
				contracts.ErrHostRegionInvalid, t.name, g.Type())
		}
		*t.dst = uint32(g.Get())
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return contracts.HostRegionDeclaration{}, fmt.Errorf(
			"wasm: %w: %v are not exported — recompile with the v2 SDK template, abi v2 requires the module to declare its host region",
			contracts.ErrHostRegionMissing, missing)
	}
	return decl, nil
}

// ValidateLoadedArtifact 是「instantiate 之后、initialize 之前」的加载门。
//
// 只有 abi_version=2 才强制宿主区符号：现有 14 个内置 v1 artifact 都没有这些
// 导出，一律 fail-closed 会把 v1 全打死，与「v1 仍能跑」直接矛盾（QT-01 ③-④）。
// v1 走白名单跳过符号校验，但返回 legacy_host_layout 诊断，让这条兼容分支
// 在日志里可见、可数，而不是静默。
func ValidateLoadedArtifact(inst Instance, family contracts.HostAbiVersion) ([]string, error) {
	mod, ok := inst.(*wazeroInstance)
	if !ok {
		return nil, fmt.Errorf("wasm: host region introspection requires the wazero runtime, got %T", inst)
	}
	if family == contracts.HostAbiV1 {
		return []string{DiagLegacyHostLayout}, nil
	}
	decl, err := ReadHostRegion(mod)
	if err != nil {
		return nil, err
	}
	// 区间重叠 / 未对齐 / 越界 全部由契约层的纯函数判（可独立测，见 contracts 侧测试）。
	if err := decl.Validate(mod.MemorySize()); err != nil {
		return nil, err
	}
	return nil, nil
}
