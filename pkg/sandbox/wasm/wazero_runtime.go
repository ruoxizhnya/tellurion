package wasm

import (
	"context"
	"errors"
	"fmt"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// ─── WazeroRuntime（生产实现）────────────────────────────────────
//
// K6 切片 1：用 wazero（纯 Go 的 WebAssembly 运行时）替换 InProcessRuntime。
// 与 InProcessRuntime 的「注入 Go handler 模拟执行」不同，本实现提供**真实
// 隔离**：wasm 字节码在受限的线性内存里执行，只能调用宿主显式导入的函数
// （host API 白名单，见 K6 切片 2）。
//
// 内存限制语义（与 InProcessRuntime 的关键差异，已裁决）：
//   - InProcessRuntime 的 memoryLimitBytes 是 **per-instance** 的软上限；
//   - wazero 的内存上限是 **runtime 级**（NewRuntimeConfig().WithMemoryLimitPages），
//     实例化后 Grow 超过该上限会失败/裁剪。
//   - 现有代码里 Instantiate 的 memoryLimitBytes 参数**总是传同一个
//     cfg.MaxMemoryBytes**（见 WASMSandbox.Run / InstantiateModule），所以
//     per-instance 参数本就是冗余的。故本实现把上限收敛到 runtime 级：构造时
//     按 maxMemoryBytes 换算页数，实例化时校验请求的 limit ≤ 该上限。
type WazeroRuntime struct {
	rt  wazero.Runtime
	max int // 运行时内存硬上限（字节）
}

// NewWazeroRuntime 创建 wazero 支持的运行时。maxMemoryBytes 是运行时级内存
// 硬上限（向上取整到 64KB 页）；≤0 时用 DefaultMaxMemory。
//
// WithCloseOnContextDone(true) 是 C1（QT-15）的终止能力：不启用时任何一次
// 调用都可能把 Goroutine 甚至 OS 线程永久阻塞（wasm 里的 while(true) 对
// CallWithTimeout 的 30s 上限免疫——超时只废掉 ctx，杀不死执行）。启用后
// ctx 到期会把模块连同执行一起关闭，调用方收到 sys.ExitError。代价是执行
// 路径插入周期性检查——实测开销见 bench（QT-15 ⑦，wazero_runtime_bench_test.go）。
func NewWazeroRuntime(ctx context.Context, maxMemoryBytes int) *WazeroRuntime {
	return newWazeroRuntime(ctx, maxMemoryBytes, true)
}

// newWazeroRuntime 是构造内部入口。closeOnDone 仅供基准测试造「关掉终止
// 检查」的对照腿（QT-15 ⑦ 的开/关对拍需要同一份代码只差这一个开关）；
// 生产路径一律走 NewWazeroRuntime（恒 true）。
func newWazeroRuntime(ctx context.Context, maxMemoryBytes int, closeOnDone bool) *WazeroRuntime {
	if maxMemoryBytes <= 0 {
		maxMemoryBytes = DefaultMaxMemory
	}
	// 向上取整到页（wazero 页 = 64KB）。max 存页对齐后的字节数，与 wazero 的
	// 实际上限一致（避免「max 存原始值、wazero 按页取整」两套口径）。
	pages := (maxMemoryBytes + 65535) / 65536
	rtCfg := wazero.NewRuntimeConfig().
		WithMemoryLimitPages(uint32(pages)).
		WithCloseOnContextDone(closeOnDone)
	return &WazeroRuntime{
		rt:  wazero.NewRuntimeWithConfig(ctx, rtCfg),
		max: pages * 65536,
	}
}

// Compile implements Runtime. 对无效 wasm 字节，wazero 返回解析/校验错误，
// 统一包装为 ErrModuleNotFound（沿用「Compile 收到无法解释为模块的字节」语义）。
// 编译成功后执行 import section 扫描：任何非 env 白名单的 import 直接拒绝
// （ADR-029 §3.4 的 fail-closed 能力隔离）。
func (r *WazeroRuntime) Compile(ctx context.Context, wasmBytes []byte) (CompiledModule, error) {
	cm, err := r.rt.CompileModule(ctx, wasmBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrModuleNotFound, err)
	}
	if err := ValidateImports(cm); err != nil {
		_ = cm.Close(ctx)
		return nil, err
	}
	return &wazeroCompiledModule{cm: cm, rt: r.rt, max: r.max}, nil
}

// Close implements Runtime.
func (r *WazeroRuntime) Close(ctx context.Context) error {
	return r.rt.Close(ctx)
}

// wazeroCompiledModule implements CompiledModule over wazero.CompiledModule.
type wazeroCompiledModule struct {
	cm  wazero.CompiledModule
	rt  wazero.Runtime
	max int
}

// Instantiate implements CompiledModule. memoryLimitBytes 必须 ≤ 运行时上限。
func (m *wazeroCompiledModule) Instantiate(ctx context.Context, memoryLimitBytes int) (Instance, error) {
	if memoryLimitBytes > m.max {
		return nil, fmt.Errorf("%w: requested %d bytes > runtime limit %d",
			ErrMemoryLimitExceeded, memoryLimitBytes, m.max)
	}
	cfg := wazero.NewModuleConfig().WithName(m.cm.Name())
	mod, err := m.rt.InstantiateModule(ctx, m.cm, cfg)
	if err != nil {
		return nil, fmt.Errorf("wasm: instantiate: %w", err)
	}
	return &wazeroInstance{mod: mod}, nil
}

// Exports implements CompiledModule.
func (m *wazeroCompiledModule) Exports() []string {
	names := m.cm.ExportedFunctions()
	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}
	return out
}

// ImportedFunctions implements AbiModule（QT-01 的协议族自证需要看到 import 表，
// 而加载器只拿得到本包装类型）。非导出接口方法，不进 CompiledModule 接口。
func (m *wazeroCompiledModule) ImportedFunctions() []api.FunctionDefinition {
	return m.cm.ImportedFunctions()
}

// Close implements CompiledModule.
func (m *wazeroCompiledModule) Close(ctx context.Context) error {
	return m.cm.Close(ctx)
}

// wazeroInstance implements Instance over api.Module.
type wazeroInstance struct {
	mod api.Module
}

// WriteMemory implements Instance. 接口契约「Grows memory if needed」：wazero 的
// Write 不会自动 grow（越界返回 false），故先尝试直接写，失败则 grow 到需要
// 的页数再写；grow 超过 runtime 上限（WithMemoryLimitPages）→ ErrMemoryLimitExceeded。
func (i *wazeroInstance) WriteMemory(offset uint32, data []byte) error {
	mem := i.mod.Memory()
	if mem == nil {
		return errors.New("wasm: module has no memory")
	}
	if mem.Write(offset, data) {
		return nil
	}

	// 写失败 = 当前内存不够。按接口契约 grow 到需要的大小。
	needed := offset + uint32(len(data))
	curPages := mem.Size() / 65536
	needPages := (needed + 65535) / 65536
	if needPages > curPages {
		if _, ok := mem.Grow(needPages - curPages); !ok {
			return fmt.Errorf("%w: need %d pages (limit %d)", ErrMemoryLimitExceeded, needPages, curPages)
		}
	}
	if !mem.Write(offset, data) {
		return fmt.Errorf("%w: write [%d:%d]", ErrMemoryOutOfBounds, offset, offset+uint32(len(data)))
	}
	return nil
}

// ReadMemory implements Instance. wazero 的 Read 返回 write-through **view**，
// 必须显式 copy —— 否则调用方修改返回值会污染 wasm 内存，与退役前
// InProcessRuntime 的 make+copy 隔离语义回归。
func (i *wazeroInstance) ReadMemory(offset uint32, length uint32) ([]byte, error) {
	mem := i.mod.Memory()
	if mem == nil {
		return nil, errors.New("wasm: module has no memory")
	}
	buf, ok := mem.Read(offset, length)
	if !ok {
		return nil, fmt.Errorf("%w: read [%d:%d]", ErrMemoryOutOfBounds, offset, offset+length)
	}
	out := make([]byte, length)
	copy(out, buf)
	return out, nil
}

// MemorySize implements Instance.
func (i *wazeroInstance) MemorySize() uint32 {
	mem := i.mod.Memory()
	if mem == nil {
		return 0
	}
	return mem.Size()
}

// ExportedGlobal 满足 hostRegionModule（QT-01 的宿主区自省）。
//
// 刻意不加进 Instance 接口：Instance 是导出接口，S0~S3 期间 agent 与本仓编译
// 连体，给一个用不到的接口加方法等于凭空制造一次 breaking。
func (i *wazeroInstance) ExportedGlobal(name string) api.Global {
	return i.mod.ExportedGlobal(name)
}

// Call implements Instance. 未导出的函数 → ErrFunctionNotExported。
func (i *wazeroInstance) Call(ctx context.Context, name string, args ...uint64) ([]uint64, error) {
	fn := i.mod.ExportedFunction(name)
	if fn == nil {
		return nil, fmt.Errorf("%w: %s", ErrFunctionNotExported, name)
	}
	return fn.Call(ctx, args...)
}

// Close implements Instance.
func (i *wazeroInstance) Close() error {
	return i.mod.Close(context.Background())
}

// 编译期契约检查：WazeroRuntime 满足 Runtime。
var _ Runtime = (*WazeroRuntime)(nil)
var _ CompiledModule = (*wazeroCompiledModule)(nil)
var _ Instance = (*wazeroInstance)(nil)
