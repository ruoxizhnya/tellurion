# 外部《基于 Wasm 的动态代码生成与沙箱执行系统》方案评审

> 评审对象：用户提供的第三方 AI 架构建议（服务 A 生成编译端 / 服务 B 执行引擎 + wazero + WASI + 安全三层防御 + 推荐 JS/QuickJS 路线）。
> 方法：逐条对照本仓源码与既有 ADR，**wazero v1.12.0 真实 API 已核实**（未凭记忆下判断）。
> 结论：**架构形状约 70% 可用，3 条直接不成立，且该方案漏掉了我们自己代码里的一个 P0 缺陷。**

---

## 1. 逐条判定

| # | 建议 | 判定 | 依据 |
|---|---|---|---|
| 1 | 服务 A（生成/编译）/ 服务 B（执行）分离 | ✅ **一致** | 就是 ADR-D13/14 的「编译服务 + 引擎」，方向相同 |
| 2 | 制品中心存储 + MQ/Webhook 分发 | ⚠️ **降级采纳** | 本地栈单机，HTTP 按 hash 拉取已足够；Webhook 仅用于 latest 引用失效（ADR-D13 §3.2 未决项 F） |
| 3 | 编译到 **wasip1** 规范 | 🔴 **不可用** | 见 §2 |
| 4 | `wasi_snapshot_preview1.MustInstantiate` | 🔴 **不可用** | 见 §2 |
| 5 | **每次调用 `wazero.NewRuntime`** | 🔴 **性能灾难** | 见 §3 |
| 6 | 单次调用 50ms 超时 | ⚠️ **数值错场景** | 见 §4 |
| 7 | **Gas 指令计数器** | 🔴 **wazero 不提供** | 见 §5 |
| 8 | 内存页上限（建议 256 页 / 16MB） | ✅ **一致** | 我们默认 64MB；且 `wazero_runtime.go:21` 已注明"wazero 的内存上限是 runtime 级" |
| 9 | 全程内存字节流、禁止落宿主机盘 | ✅ **一致** | 我们本来就是 `Compile(wasmBytes)` |
| 10 | 能力隔离、无网络、需外部访问走 host 代理 | ✅ **我们更严** | env-only 白名单，连网络 host 函数都没有（比"挂空 WASI"更彻底） |
| 11 | 路线二：AI 生成 JS → Wasm 内 QuickJS | ❌ **不推荐** | 见 §6 |
| 12 | 安全三层（超时 / Gas / 内存 / 能力） | ⚠️ 我们**只有两层半** | 见 §5（缺可中断执行） |

---

## 2. 🔴 WASI 路线在我们这里被架构性禁止

Go 编译的 `wasip1` 产物必然 import `wasi_snapshot_preview1`（`fd_write` / `proc_exit` / **`random_get`**），而我们的 `ValidateImports` **只允许 `env.*`**（`host_api.go` 白名单 8 函数）⇒ 直接拒绝。

更关键的是两条性质：

- **`random_get` 破坏确定性**：Go 的 map 迭代随机化依赖它——我们靠"喂 bar 按 symbol 字典序 + 契约防前视"换来的可复现性会被废掉；
- **`fd_write` 带回 IO**：与 ADR-029「刻意不提供文件/网络/时钟/随机源」的设计原则正面冲突。

⇒ ADR-D15 §4 已裁决：**坚持 env-only，不放行 WASI**。编译侧改用 TinyGo `-target=wasm-unknown` / Rust `wasm32-unknown-unknown` / AssemblyScript。

（补充：该方案的 `wasi_snapshot_preview1.MustInstantiate` + "文件系统参数留空" 是常见做法，但它本质是**先给 IO 能力再靠配置堵**；我们是**根本不给**——后者更硬，别倒退。）

---

## 3. 🔴 "每次调用新建 Runtime" 在回测场景下是灾难

方案代码第 2 步：`r := wazero.NewRuntime(execCtx)`，**每次调用创建一个新 Runtime**。

这是 Serverless「一次调用 = 一次请求」的心智模型。但我们的调用形状是：

```
5000 标的 × 727 交易日 = 约 363 万次 on_bar
```

每 bar 新建一个 Runtime ⇒ **每 bar 重新编译整个模块**（Runtime 级编译缓存全部失效），并使实例无法复用、状态无法延续（状态机直接报废）。

正确做法（wazero 的三层复用）：

| 对象 | 复用粒度 |
|---|---|
| `Runtime` | **进程级单例**（我们的 `NewWazeroRuntime` 已经是） |
| `CompiledModule` | **按 `artifact_hash` 缓存**（编译一次，多次实例化） |
| `Instance` | 按策略实例/会话持有（状态机需要它跨 bar 存活） |

---

## 4. ⚠️ 超时预算的形态错了（不是数值错）

方案给"每次调用 50ms"。我们需要的是**双层预算**：

| 层 | 建议值 | 作用 |
|---|---|---|
| **每 bar 预算** | 1-5 ms（可配） | 抓住"某个 bar 上卡住"的坏策略 |
| **整体预算** | 按回测规模（分钟级） | 防止整体失控 |

判据：**单次调用预算不能按"一次请求"定，要按"内循环单次迭代"定**。

---

## 5. 🔴 Gas 计数器 wazero 没有；更要命的是我们没开可中断执行

**Gas**：wazero v1.12.0 **不提供** gas/instruction metering API（已在模块源码核实，无相关 API）。该建议在这套技术栈上做不到。

**但真正的问题在这里（方案没提，我们自己有洞）**：

`pkg/sandbox/wasm/wazero_runtime.go:41` 目前只有：

```go
rtCfg := wazero.NewRuntimeConfig().WithMemoryLimitPages(uint32(pages))
```

**没有 `WithCloseOnContextDone(true)`**。而 wazero 文档原文（config.go:149-171）：

> WithCloseOnContextDone ensures the executions of functions to be terminated when ctx is canceled / reaches timeout... This is especially useful when one wants to run **untrusted** Wasm binaries since otherwise, **any invocation can potentially block the corresponding Goroutine forever. Moreover, it might block the entire underlying OS thread**... this is **disabled by default**.

⇒ 后果：**我们现有的 `CallWithTimeout`（30s）对 wasm 死循环完全无效**。AI 生成的策略写一个 `while(true)`，就能把回测进程（及其 OS 线程）永久挂死。

**这是接线前的 P0 必修项**，且正好是该方案声称要防的场景（`while(true)`），但它给的解法（Gas）在我们栈上不存在，真正的解法是 `WithCloseOnContextDone(true)`。代价是"插入周期检查"带来的额外开销——**必须在 PoC 里量化**（P4 基准）。

---

## 6. ❌ 路线二（JS + Wasm 内 QuickJS）不推荐给我们

它的优势判断本身是对的：**LLM 对 JavaScript 的训练集最充分、零编译延迟、冷启动快**。但这三点在我们的场景里**已经被更便宜的方式覆盖**，而它的代价恰好踩中我们最紧的约束：

| 维度 | 评估 |
|---|---|
| **性能** | ❌ 双重解释损耗（QuickJS 在 Wasm 内）。我们是"逐 bar × 5000 标的"，是性能预算最紧的场景，而它是最贵的执行方式 |
| **确定性** | ❌ JS 内建 `Math.random()` / `Date.now()` 是非确定源，QuickJS 会提供 ⇒ 破坏可复现与 K15 双跑对齐；要堵得改造 QuickJS 或替换内建（额外工作量） |
| **分层纪律** | ❌ 我们已有 L1 DSL / L2 算子 / L3a WASM / L3b 外部信号四层（ADR-028），再加 QuickJS 变五层，维护成本上升 |
| **LLM 准确率优势** | ✅ 洞察成立——**但正确落法是让 AI 生成我们的表达式 DSL**（受限语言、定义即数据、零编译、天然确定），或生成 Go/Rust 再编译为 env-only wasm |

⇒ 结论：**采纳它的洞察，拒绝它的载体**。

---

## 7. 采纳清单（落到我们的路线）

**采纳**
- A/B 分离（编译服务 / 引擎）→ 并入 S2；
- 全程内存字节流 → 已是现状；
- 内存页上限 → 已是现状（64MB，runtime 级）；
- 能力隔离、无网络 → 我们更严（env-only）；
- Webhook 制品通知 → 仅用于 latest 失效（未决项 F）。

**否决**
- WASI / wasip1（架构冲突 + 确定性风险）；
- 每次调用新建 Runtime（性能灾难）；
- 单次 50ms 超时（形态错，改双层预算）；
- Gas 计数器（wazero 无此 API）；
- QuickJS 路线（性能 + 确定性 + 分层冲突）。

**必补（方案没提，但我们必须做）**
1. **`WithCloseOnContextDone(true)`**（P0，防死循环挂死 OS 线程）；
2. 双层超时预算（每 bar 1-5ms + 整体预算）；
3. PoC 中量化"周期检查"的性能代价；
4. 超时终止后模块被关闭 ⇒ 会话需要重建语义（与状态机恢复路径打通）。

---

## 8. 修正后的 PoC 增项

| # | 增项 | 判据 |
|---|---|---|
| P6 | 构造一个 `while(true)` 的恶意 wasm，验证启用 `WithCloseOnContextDone` 后能被超时终止 | 进程不被挂死，返回可识别错误 |
| P7 | 基准：开启/关闭周期检查的性能差 | 给出百分比，决定是否按策略分级启用 |
| P8 | 验证超时终止后 `sys.ExitError` 的处理与会话重建 | 不影响后续回测与状态恢复 |
