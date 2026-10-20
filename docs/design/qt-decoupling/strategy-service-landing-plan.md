# 策略服务落地方案：技术选型、可行性与分期（ADR-D14 草案）

> 状态：草案，未实现。承接 ADR-D13（真相源外置 + 引擎缓存）。
> 前提（用户已确认）：**信号留在回测内**——策略服务只管定义，不算信号。
> 所有结论基于 2026-10-19 实测，不含推测。

---

## 1. 能做到吗？分两件事回答

| 事项 | 判定 | 理由 |
|---|---|---|
| **真相源外置 + 引擎缓存**（ADR-D13） | ✅ **能做，低风险** | 纯 Go 改动，不引入任何新技术：`strategy_ref` 解析 + 内容寻址缓存 + 只读副本语义 |
| **WASM 在引擎内执行** | ⚠️ **能做，但必须先做 PoC** | 运行时与协议已实现（wazero v1.12 + `OnBarSession` + host 白名单），但**未接线**、**本机无编译工具链**、且有 2 个未决的硬设计冲突（见 §3） |

---

## 2. 技术账本（现状 vs 缺口）

| 环节 | 技术 | 现状 | 证据 |
|---|---|---|---|
| 运行时 | **wazero v1.12.0**（纯 Go、无 cgo） | ✅ 已是依赖 | `go.mod:18` |
| 沙箱与协议 | `OnBarSession`（initialize / on_bar / finalize） | ✅ 已实现，**未接线** | `on_bar_session.go:51` |
| 前视隔离 | `get_bar(t_offset>0)` → panic → trap | ✅ 已实现 | `host_api.go` `hostGetBar` |
| 导入白名单 | `env` 模块 8 函数 | ✅ 已实现（**且只允许 env**） | `hostFnWhitelist` + `ValidateImports` |
| 资源限制 | 64 MB 内存 + 30s/次超时 | ✅ 已实现 | `WithMemoryLimitPages` / `CallWithTimeout` |
| **编译工具链** | TinyGo / Rust / AssemblyScript | ❌ **本机没有**（无 tinygo、无 wat2wasm、无 wasmtime） | `which` 全空 |
| **执行接线** | `SignalProvider` 的 WASM 实现 | ❌ 无（repoguard 未接线白名单） | 「待策略插件执行接线」 |
| **信号转换** | `EmittedSignal` → `domain.Signal` | ❌ 未写 | — |
| **分发与缓存** | HTTP + 内容寻址缓存 | ❌ 待建（ADR-D13） | — |

---

## 3. 两个必须先在 PoC 里解决的硬冲突

### 3.1 Go 原生 `wasip1` 目标 vs 现有 env-only 白名单（🔴 会直接编译失败/加载被拒）

- `go1.25.14` 支持 `wasip1/wasm` 目标（已确认：`go tool dist list` 有 `wasip1/wasm`）。
- 但 **Go 的 wasip1 产物必然 import `wasi_snapshot_preview1`**（`fd_write` / `proc_exit` / `random_get` 等），而 `ValidateImports` 只允许 `env.*` ⇒ **直接拒绝**。
- 更糟的是 **`random_get`**：Go 的 map 迭代随机化依赖它，一旦放行，**回测的确定性就没了**——你们靠"喂 bar 按 symbol 字典序 + 契约级防前视"换来的可复现性会被废掉。

**裁决建议：不放行 WASI，坚持 env-only。** 编译侧改用不带 WASI 的目标：

| 方案 | 命令/方式 | 评价 |
|---|---|---|
| **TinyGo `-target=wasm-unknown`** | `tinygo build -target=wasm-unknown`（或 `wasm` 变体，需确认 import 面） | 推荐：Go 语法、无 WASI 导入、产物小；代价是不支持完整 stdlib（net/reflect 受限，策略场景够用） |
| Rust `wasm32-unknown-unknown` | `cargo build --target wasm32-unknown-unknown` | 控制力最强、无隐式导入；代价是策略作者要换语言 |
| AssemblyScript | `asc` | 类 TS，对 AI 生成友好；生态较小 |
| Go 原生 `GOOS=wasip1` | — | ❌ 与 env-only 白名单冲突，且带来 `random_get` 确定性风险 |

**PoC 第一步就验证这个**：编译出的 `.wasm` 用 `ValidateImports` 跑一遍，确认 import section 里只有 `env.*`。

### 3.2 per-symbol 实例 vs 横截面策略（✅ 已于 2026-10-19 裁决为混合模型，见 ADR-D25）

> **裁决结果（ADR-D25 / abi v2）**：选「混合（C）」——`get_bar` 加 symbol 维度；策略声明 `scope: market`（默认，单实例横截面，727 次 on_bar）或 `scope: symbol`（分片，每实例绑一标的）。本节以下内容保留为决策背景，P4 改为验证 v2 语义与性能。

- ADR-029 §3 的 `get_bar` **没有 symbol 参数** ⇒ 一个实例绑定一个 symbol 的序列（代码注释明写 "per-symbol 实例"）。
- 但 `BarContext` 同时提供 `CrossSection`（全市场横截面）+ `get_cross_section`，而横截面策略需要"同一时刻看到所有标的"。

冲突后果：若按 per-symbol 起实例，全市场 5000 标的 ⇒ **5000 个活实例 × 内存上限**，内存直接爆炸；若按 t 外层循环推进，又与串行 per-symbol 模型冲突。

**PoC 必须定案（三选一）**：

| 模型 | 做法 | 代价 |
|---|---|---|
| **A. 单实例 + 横截面** | 一个实例处理全市场；`on_bar(t)` 时遍历 `get_cross_section` | 需要改 host API 给 `get_bar` 加 symbol 维度（改 ADR-029 协议） |
| **B. per-symbol 串行复用** | 逐 symbol：起实例 → 跑完它的 bars → Close → 下一个 | 5000 次 instantiate（编译产物复用，成本可接受）；**但跨标的横截面策略做不了** |
| **C. 池化** | 实例池 + LRU，按活跃 symbol 复用 | 复杂度最高 |

倾向：**A 为主（横截面是量化主战场），B 作为纯时序策略的快速路径**。这是 PoC 里必须实测定案的，不能拍脑袋。

---

## 4. 建议两条腿走路（而不是硬等 WASM）

### 腿 1：表达式 DSL 资产化（**零新工具链，立刻可做**）

`pkg/expression`（L1）+ `pkg/indicator`（L2 有状态算子）已经存在，**定义即数据，无需编译、无需沙箱**。用它先把 ADR-D13 的"真相源 + 缓存 + `strategy_ref`"整套机制跑通：

- 策略定义 = DSL 文本，直接进 `strategy_definitions`（`carrier=dsl`）；
- `version_hash` = 定义文本的 sha256（沿用因子侧口径）；
- 引擎缓存 = 已编译/解析的表达式对象，键 = `version_hash`；
- **零 WASM、零工具链、零沙箱风险**，且天然跨内核（NT 侧只要实现同一求值器）。

覆盖度判断：规则型策略（多因子、动量、均值回归、条件组合）基本都能表达；真正需要 WASM 的是"要写过程式逻辑/复杂状态机"的策略。**先把 80% 场景跑通，是"廉价拓展"目标的最快路径。**

### 腿 2：WASM（需要工具链 + PoC，服务于 AI 生成代码与第三方策略）

只有当"策略作者是 AI 或第三方、不能信任"时才**必需** WASM——因为那时你需要沙箱，而 DSL 的表达力可能不够。

---

## 5. WASM PoC 清单（建议 3-5 天，产出 go/no-go 决策）

| # | 任务 | 通过判据 |
|---|---|---|
| P1 | 装 TinyGo（Windows 二进制 / 或 Docker-Linux 内编译），把一个均线策略编成 `.wasm` | `ValidateImports` 通过，import section **只有 `env.*`** |
| P2 | 用现有 `NewOnBarSession` 跑通 `initialize → on_bar × N → finalize` | 拿到 `EmittedSignal`，与 Go 内置同参策略结果对拍一致 |
| P3 | **性能基准**：单实例 × 1000 bars 的 `on_bar` 吞吐；外推"5000 标的 × 727 交易日"总耗时 | 给出 ms/次与总时长，判断是否需要池化/编译缓存 |
| P4 | **v2 语义验证**（已定案混合，ADR-D25）：`scope=market` 的 64MB 共享内存峰值 与 `scope=symbol` 分片成本 | 内存峰值 + 总耗时 + 两种 scope 可用性 |
| P5 | 接线到 `SignalProvider`，跑一次真实回测并与内置策略 dualrun 对拍 | K15 级别结果一致 |

**注意 P3 不能靠估算**：wazero 在 amd64/arm64 有编译后端、其他平台回退解释器，Windows/amd64 属前者，但实际吞吐必须实测。

---

## 6. 分期落地路线

| 阶段 | 内容 | 依赖 | 风险 |
|---|---|---|---|
| **S0** | `carrier=dsl` 资产化 + `strategy_ref` + 内容寻址缓存 + 只读副本语义（ADR-D13） | 无新技术 | 低 |
| **S1** | WASM PoC（P1-P5） | 装 TinyGo | 中（可能否掉 WASM 或触发协议改版） |
| **S2** | 编译服务 + artifact 登记 + `carrier=wasm` 接线 | S1 通过 | 中 |
| **S3** | 内置 14 个策略登记进服务，执行路径收敛为唯一入口 | S0 | 低-中（护栏改造） |
| **S4**（可选） | `carrier=external`（L3b）纳入同一 `strategy_ref` 命名空间 | S0 | 低 |

**S0 与 S1 可并行**：S0 不依赖任何 WASM 结论，可以直接开工。

---

## 7. 风险登记

| ID | 风险 | 兜底 |
|---|---|---|
| R1 | WASM 性能不达标（逐 bar 调用开销） | 回退到 DSL；或只在高价值策略用 WASM，批量扫描用 DSL |
| R2 | per-symbol 模型与横截面需求冲突，需改 ADR-029 协议 | PoC 阶段定案；协议改版走评审 |
| R3 | 本机无 wasm 工具链，编译链路依赖外部安装 | 装 TinyGo 二进制，或在 Docker/Linux 内编译（容器已有 `Dockerfile.service`） |
| R4 | 两套载体（DSL + WASM）长期并存导致维护双倍 | 用 ADR-028 分层纪律约束：**能用 DSL 表达的不许上 WASM** |
| R5 | 引擎侧出现两份策略来源（内置 `init()` 与服务） | S3 收敛；护栏禁止新增 `init()` 注册 |
