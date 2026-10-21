# 策略加载外化设计（ADR-D12 草案）

> 状态：草案，未实现。定位：**定架构，不落地**。
> 关联：ADR-027 §2.6.1（策略服务「只管定义、不算信号」；WASM 三段分工：编译在 ai-service / 字节码作定义资产存策略服务 / wazero runtime 在回测服务——原引「§833」系行号误作节号，六处变化表见 §2.5）、ADR-028（表达力四层）、**ADR-024（Accepted：「不做 `plugin.Open`」——`.so` 口径以它为准，见 §4.3 修正）**、ADR-D6（策略资产服务 ✅ 独立、运行时 ❌ 不做服务）、REV-08（插件路径逃逸校验）。
> 证据全部来自 2026-10-19 源码实测（非测试代码）。

---

## 1. 现状：三种载体，只有一种真在生产链路上

| 载体 | 机制 | 触发方式 | 生产状态 |
|---|---|---|---|
| **编译进二进制**（`pkg/strategy/plugins` 14 个 `init()` 自注册） | `DefaultRegistry` 注册 | 启动即生效 | ✅ **唯一真实执行路径** |
| **`.so` 热加载** | `plugin.Open(path)` → `Lookup("Strategy")` → 断言 `Strategy` 接口 → `registry.Register` | HTTP：`POST /api/plugins/load`、`/:name/reload`、`/reload`、`/load-all`、`POST /watch-dir` | ⚠️ 端点齐、逻辑齐；**但编译步骤不在仓内**（见 §2），且 Windows 不可用 |
| **WASM** | `WASMSandbox.Compile(wasmBytes)` / `NewOnBarSession(ctx, wasmBytes)` | **无触发点** | ❌ **未接线**（repoguard 白名单：「P2-27 WASM 沙箱，待策略插件执行接线」） |

关键文件：`pkg/strategy/loader.go`（`PluginSymbol = "Strategy"`、`PluginInfo{Version, GitHash, Path, Size}`、`resolvePluginPath`）、`cmd/analysis/handlers_plugin.go`（路由与 REV-08 加固）、`pkg/sandbox/wasm/sandbox.go` 与 `on_bar_session.go`（全部接口只接受 `wasmBytes []byte`）。

**`.so` 的编译步骤查无实据**：`cmd/analysis/generated_strategies/` 是空目录；全仓唯一出现 `go build -buildmode=plugin` 的地方是 `pkg/strategy/plugins/example_plugin.go:6` 的**注释**（示例）。agent 仓 grep `buildmode` / `go build` 也零命中 ⇒ **ADR-027 §2.6.1「编译在 ai-service」目前没有落地**，编译是人工离线动作。

---

## 2. `.so` 的四条硬约束（决定性）

| # | 约束 | 证据 / 后果 |
|---|---|---|
| 1 | **Windows 完全不可用** | Go 源码 `src/plugin/plugin_dlopen.go` 的 build tag：`(linux && cgo) \|\| (darwin && cgo) \|\| (freebsd && cgo)`；其他平台落到 `plugin_stubs.go`，`plugin.Open` 直接返回 `errors.New("plugin: not implemented")`。**本机开发在 Windows ⇒ 这条路径本地根本跑不起来**（容器/Docker 内才可用） |
| 2 | **ABI 与宿主强绑定** | 必须与宿主**同一 Go 版本、同一构建标签、同一依赖版本**，否则 `plugin.Open` 报 "plugin was built with a different version of package ..."。core 任何依赖升级都可能让既有 `.so` 集体失效 |
| 3 | **无沙箱** | `loader.go:87` 注释原文：「`plugin.Open` 会执行 `.so` 里的 init 代码——路径由请求体直接决定时，任何能打这个端点的人都能在进程内跑任意原生代码」。REV-08 的路径校验只是缓解，**不是隔离**。AI 生成的策略绝不能走这条路 |
| 4 | **不可卸载** | Go `plugin` 没有 `Close`，`Unload` 只是从注册表摘除，符号与内存不释放 ⇒ 所谓"热加载"实为"热追加"，长期运行会累积 |

**结论**：`.so` **不适合作为外化通道**——第 1、2 条是硬否决。且按 **ADR-024（Accepted，2026-09-17）** 的裁决「**不做 `plugin.Open`**」，`.so` 的定位是**存量遗留**（loader 与 HTTP 端点已在生产，冻结不扩展、逐步退役），**不是**"可信一等公民"通道——本草案早期口径与 ADR-024 相抵触，2026-10-19 审查修正于此。

---

## 3. WASM 的机制与状态

- **接口已就绪且只吃字节**：`Compile(ctx, wasmBytes)`、`NewOnBarSession(ctx, wasmBytes)`、`Run(ctx, wasmBytes, input)`。全仓**没有任何读取 `.wasm` 文件/HTTP 的入口**（`ReadFile` 在 `pkg/sandbox`、`pkg/strategy` 非测试代码零命中）⇒ 外化时"从哪拿字节"完全自由，这是它的优势。
- **沙箱四件套**：线性内存 + import 白名单（`ValidateImports` 只放行 `env`）+ 前视隔离（`get_bar(t_offset≤0)`，越界 `trapFuture`）+ 超时/内存限制（`ErrTimeout` / `ErrMemoryLimitExceeded` / `ErrMemoryOutOfBounds` 已定义）。
- **跨内核优势（关键）**：NT 内核是 Python（nt-sidecar），跑不了 `.so`，但**能跑 wasm**（wasmtime/wasmer）。同一份 `.wasm` 在 Go 内核与 NT 内核都能执行 ⇒ 这是双内核口径一致（dualrun K15）目前唯一可行的**共享载体**。
- **状态**：未接线。接线位置是 **`SignalProvider`**（与 `LocalSignalProvider` 并列的一种实现，在回测引擎进程内吃 date-loop 数据）。

---

## 4. 目标工作流：可以外化，但**编译不能进引擎**

### 4.1 原则

1. **构建期 / 运行期分离**：编译是构建动作，发生在独立编译服务；引擎是纯执行语义，只加载**已验证的内容寻址 artifact**。引擎里放 `go toolchain` = 攻击面 + 部署体积 + 破坏内核纯粹性，三输。
2. **拉取是一次性的**：开跑前按 `strategy_ref` 拉 1 次定义/字节码，**不是逐 bar**。这与 ADR-027「只传定义、不传信号」完全一致。
3. **内容寻址 + 版本留痕**：artifact 以 `content_hash` 为主键；回测结果必须记录所用 artifact 的 hash（复用 `PluginInfo.Version` / `GitHash` 已有的留痕思路，P1-D VISION.md 已要求"哪个策略版本跑了哪个回测"）。没有这一步，同参回测不可复现，dualrun K15 会失去意义。

### 4.2 五步工作流

| 步 | 谁 | 做什么 | 产出 |
|---|---|---|---|
| 1 | 人 / AI | 写策略源码（表达式 DSL 优先，Go 源码次之） | 源码 + 声明（频率/市场/所需 host 能力） |
| 2 | **编译服务**（独立进程/容器，锁 toolchain 版本） | `tinygo build`（→ `.wasm`）；`-buildmode=plugin` 仅存量 `.so` 再编译需要（ADR-024 口径：不新增 `.so` 通道）；静态校验（import 白名单、体积、内存上限） | artifact + `content_hash` + `abi_version` + toolchain 元信息 |
| 3 | **策略资产服务**（ADR-027 §2.6.1） | 存源码、artifact、版本、`content_hash`；暴露 `GET /strategies/{id}/artifact?hash=` | 定义资产（**只管定义，不算信号**） |
| 4 | **引擎侧加载器** | 按 `strategy_ref` 拉一次 → **校验**（hash 比对、`abi_version` 兼容、wasm import 白名单、size/memory 上限）→ `Compile` | 已编译模块 |
| 5 | **缓存与挂载** | 按 `content_hash` 缓存编译产物（避免重复 Compile）→ 挂到 `SignalProvider` → 回测结果记录 hash | 可复现的一次回测 |

失败一律 **fail-closed**：hash 不匹配 / abi 不兼容 / import 越界 ⇒ 拒绝加载并点名原因，不降级、不回退（与 `contracts.MarketRouter` 的 `ErrKernelNotRegistered` 同款语义）。

### 4.3 三条通道的取舍

| 通道 | 外化 | 沙箱 | 跨内核 | 性能 | 表达力 | 建议 |
|---|---|---|---|---|---|---|
| **表达式 DSL（L1/L2）** | ✅ 定义即数据，无需编译 | 天然安全（无可执行代码） | ✅ | 高 | 有限（确定性算子组合） | **默认层**，能用它表达的不许上高层（ADR-028） |
| **WASM（L3a）** | ✅ **推荐主通道** | ✅ 完整 | ✅ Go 内核 + NT 内核 | 中（bar 级频率可接受） | 高（受 host API 约束） | AI 生成 / 外部来源策略的唯一通道 |
| **`.so`（宿主插件）** | ⚠️ 仅限同源可信 | ❌ | ❌ | 最高 | 最高（整个 Go 生态） | **存量遗留**（ADR-024：不做 `plugin.Open`）——冻结不扩展、逐步退役；**绝不经 HTTP 动态拉取编译产物** |

### 4.4 与"廉价拓展"的关系

外化的**载体是 artifact 与注册表，不是进程**：新增一个策略 = 编译服务产出 artifact + 资产服务登记一行，引擎**零改动**。这与"新因子 = 注册表一条定义"是同一套机制。

---

## 5. 现有地基 / 要补的东西

**可直接复用**
- `PluginInfo`（Version / GitHash / Path / Size / Status）→ 版本留痕机制可平移到 wasm artifact。
- `resolvePluginPath` 的 **fail-closed 校验思路**（归一 → `filepath.Rel` 判定 → 未配置即禁用）→ 平移为 artifact 校验（hash + abi + import 白名单）。
- `ValidateImports` / `trapFuture` / 超时与内存限制 → WASM 侧已经写好了。
- `strategies` 表（管理面元数据，名字+类型+参数 JSON）→ 升级为资产服务的主表，补 `artifact_hash` / `abi_version` / `content_hash` 字段。

**要新建**
- 编译服务（锁 toolchain，产出内容寻址 artifact）——**这是目前完全缺的一环**。
- 引擎侧 artifact 加载器 + 按 hash 的编译缓存。
- WASM 接线到 `SignalProvider`（解除未接线状态）。
- 护栏：禁止引擎在回测内循环里拉取远程资源（开跑前一次性拉取，内循环零网络）。

---

## 6. 问题登记（沿用 BE 编号，同步进 review 记事本）

| ID | 等级 | 问题 | 方向 |
|---|---|---|---|
| BE-18 | 🔴 | `.so` 通道在 Windows 上不可用（`plugin_stubs.go` → `plugin: not implemented`），本地栈无法验证 | 外化主通道改走 WASM；`.so` 按 ADR-024 标注为存量遗留（冻结不扩展、逐步退役） |
| BE-19 | 🟠 | **无编译链路**：`cmd/analysis/generated_strategies/` 为空，仓内无 `go build -buildmode=plugin`/tinygo 调用，ADR-027 §2.6.1「编译在 ai-service」未落地 | 建独立编译服务，产出内容寻址 artifact |
| BE-20 | 🟠 | 回测结果未记录策略 artifact 的 `content_hash`（只有 `PluginInfo.GitHash`，且仅覆盖 `.so`） | 结果落到 `strategy_ref{id, hash}`，否则 K15 双跑对齐失去可复现前提 |

---

## 7. 附录：WASM 到底怎么跑起来（原理 + 工作流）

> 全部依据 `pkg/sandbox/wasm/` 现有代码（2026-10-19 读），不是通用科普。

### 7.1 原理：五条机制

**A1. WASM 是字节码，不是机器码。** 它描述的是一台**栈式虚拟机**的指令，由运行时解释或 JIT 编译成宿主机器码执行。性质：与语言无关（Go/Rust/Zig/AssemblyScript 都能编译到它）、与平台无关（同一份字节码 Linux/Windows 都能跑）、**默认零能力**。

**A2. 运行时是 wazero（纯 Go，无 cgo）。** `wazero.NewRuntimeWithConfig`（`wazero_runtime.go:34-43`），编译进 Go 二进制，不需要安装任何外部 runtime。三层对象：`Runtime` → `CompiledModule`（编译产物，**可复用**）→ `Instance`（**各自独立的线性内存**）。

**A3. 安全来自"缺失"，不来自"审查"。** `host_api.go` 注释原文：刻意**不提供**文件 / 网络 / 时钟 / 随机源 / goroutine / 进程 / 环境变量，"缺这些正是安全性的来源"。实例能做的唯一外部动作，是调用宿主**显式导入**的函数。

**A4. 导入白名单在编译期强制。** `InstantiateHostModule` 用 `wazero` 的 `HostModuleBuilder` 把 8 个函数注册到模块名 `env`：`get_bar` / `get_series_len` / `get_symbol_count` / `get_cross_section` / `get_state` / `set_state` / `emit_signal` / `log`。`Compile` 后立即 `ValidateImports` 扫描 import section，**凡不在 `env` 白名单内一律拒绝**（`wasm: forbidden imports ...`）。

**A5. 前视是物理不可能，不是约定。** 旧协议 `generate_signals(bars_ptr, bars_len)` 一次性把全部 bars 写进内存，沙箱内读 `bars[len-1]` 就拿到未来 ⇒ 已改为逐 bar 协议。现在 `hostGetBar(t_offset, field_id)` 在 `t_offset > 0` 时**直接 panic**，wazero 把 panic 转成 wasm trap；`get_cross_section` 同理。`trapNoContext` 保证 host 函数在未注入上下文时 fail-loud，不静默降级。

### 7.2 数据怎么进出（内存协议）

- **入参**：host 用 `WriteMemory(0, params)` 把 JSON 写到线性内存，**调用只传 (ptr, len) 两个整数**（`initialize(params_ptr, params_len)`）。
- **出参**：函数返回一个 `i64`，**高 32 位是 ptr、低 32 位是 len**（`UnpackPtrLen`），host 再按这对坐标 `ReadMemory` 读回 JSON。
- **上下文注入**：`*BarContext`（`T` / `Series` / `CrossSection` / `Symbols` / `State` / `Signals`）经 `context.WithValue` 注入，host 函数从调用链 ctx 取出。这是 Go 侧特有的做法——**序列数据根本不进 wasm 内存，wasm 只能按坐标索取**。
- **资源上限**：`WithMemoryLimitPages`（默认 64 MB）+ 每次调用 `context.WithTimeout`（默认 30s，见 `CallWithTimeout`）。
- **状态**：`get_state` / `set_state` 由 host 持有 `BarContext.State` ⇒ 断点续跑可用，且一致性要求 Batch ≡ Step 从零 ≡ Step 从恢复（ADR-028 §7 三路）。

### 7.3 工作流（一次回测的完整生命周期）

**构建期**
1. 用 Go 子集 / Rust / Zig 等写策略，`tinygo build` 或 `GOOS=wasip1 go build` → `.wasm`；
2. 静态校验（import 白名单 / 体积 / 内存上限）→ 登记 artifact（`version_hash` + `artifact_hash`）。
   ⚠️ **本机目前没有 wasm 工具链**（无 tinygo / wat2wasm / wasmtime），仓内测试是靠 `wasm_builder_test.go` **手工构造字节码**验证的——这正是 BE-19「编译服务缺失」的实证。

**运行期**（全程进程内、零网络）
3. `NewWazeroRuntime(ctx, maxMemory)` → **`InstantiateHostModule(ctx)` 必须先于策略模块实例化**；
4. `Compile(wasmBytes)`（内含 `ValidateImports`）→ `Instantiate` 得到独立实例；
5. `sess.Initialize(ctx, paramsJSON)`；
6. **date-loop**：每个 bar 更新 `bc.T` → `sess.OnBar(ctx, bc)`；策略内部只能 `get_bar(t_offset ≤ 0)` / `get_cross_section` / `get_state` / `emit_signal`；
7. `sess.Finalize(ctx, bc)` 冲刷最终信号（`pack(0,0)` 表示无信号）；
8. `sess.Close()` 释放实例。

第 6 步是逐 bar 的 ⇒ **WASM 运行时必须在引擎进程内**，这就是 `sandbox/wasm` 留在 core 而不是 kit 的原因。

### 7.4 两个尚未解决的点（别当已完备）

| 点 | 说明 |
|---|---|
| **per-symbol 实例模型** | ADR-029 §3 的 `get_bar` 签名**没有 symbol 参数**，一个实例绑定一个 symbol 的序列。全市场 N 个标的 ⇒ N 个实例（编译产物可复用，但实例与内存是 N 份）。成本与并发模型需在接线时定清楚 |
| **未接线** | 上面第 3-8 步的驱动者还不存在（repoguard 白名单「待策略插件执行接线」）；接线位置是 `SignalProvider`。`EmittedSignal` → `domain.Signal` 的转换也还没写 |
