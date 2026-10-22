# ADR-029 修订草案：host API v2（symbol 维度 + 混合实例模型）

> 状态：**修订草案**（未升格为正式 ADR；方向已由用户 2026-10-19 裁决，见 ADR-D25）
> 关联：ADR-029 §3（WASM host API）、ADR-D25（I/B/J 裁决落档）、D16（状态机契约）、D14（落地方案）
> 性质：协议修订草案，未实施

## 1. 为什么改

现状（`pkg/sandbox/wasm/host_api.go:17-19` 注释原文）：

> 数据模型（per-symbol 实例）：ADR-029 §3 的 `get_bar` 签名**没有 symbol 参数**，因此一个 WASM 实例绑定「当前 symbol 的序列」……

后果：

1. **市场级/横截面策略写不了**——每个实例只看自己那个 symbol，跨标的协同决策（regime 开关、轮动、组合约束）无法表达；
2. 全市场按 per-symbol 起实例 = **5000 个活实例**，内存与调度成本高；
3. 用户裁决（ADR-D25）：**市场级优先、混合为目标**，现有协议与目标形态不匹配。

## 2. 变更（abi v2）

### 2.1 host 函数签名

| 函数 | v1（现状） | **v2（本草案）** | 说明 |
|---|---|---|---|
| `get_bar2` | —（v2 新增） | **`(symbol_idx: i32, t_offset: i32, field_id: i32) -> f64`** | 增加 symbol 维度；`symbol_idx` 索引 `Symbols` 有序表 |
| `get_series_len2` | —（v2 新增） | **`(symbol_idx: i32) -> i32`** | 返回**可见长度**（日期 ≤ 当前 T 的 bar 数），不是数组总长 |
| `get_date2` | —（v2 新增） | **`() -> i32`**（`YYYYMMDD`，UTC） | 状态机做日历相关转移必需（月末调仓、持有 N 个交易日、事件窗口）；**现在定成本近零，以后再定会让 v2 内部出现两种形态** |
| `get_bar` | `(t_offset: i32, field_id: i32) -> f64` | 保留不动（仅 v1 artifact 可导入） | **不改签名**，理由见 §2.3 |
| `get_symbol_count` | `() -> i32` | 不变 | — |
| `get_cross_section` | `(t_offset, field_id, idx) -> f64` | 不变（**实现即三参数**） | 旧注释写两参数是笔误，以本表为准 |
| `get_state` / `set_state` | `(ptr,len)` | 不变 | 宿主持有，按日期边界 flush |
| `emit_signal` | `(symbol_idx, action, strength)` | 不变 | 语义不变（D26：Strength=强度∈[0,1]） |
| **`emit_target2`** | —（v2 新增） | **`(symbol_idx: i32, weight: f64) -> void`** | **目标权重 ∈ [-1,1]**；`-1`=不动、`0`=清仓；与 `emit_signal` 同 bar 同 symbol 混用 → trap；政策 cap 优先（见 `p0-round2-review.md` §8.2） |
| `log` | — | 不变 | — |

> **本表是 ABI 的唯一真相**：SDK 绑定、NT 侧实现、`ValidateImports` 都以此为准；`host_api.go` 头部注释的 `get_cross_section(t_offset, idx)` 是笔误（实现为三参数），2026-10-19 终审订正。

### 2.3 版本共存：用「增量函数名」而非「同名改签名」

**否决**「同名 `get_bar` 改签名 + runtime 按 abi_version 注册不同函数集」：
- Wasm 导入是**按名字 + 类型**匹配的，同一个 host 模块不能导出两个同名函数 ⇒ 要么起两个 Runtime（与 D14「Runtime 进程级单例」的编译缓存复用冲突），要么每个 artifact 一个 Runtime（编译缓存全废）；
- 「同名不同类型」是 ABI 地雷：错误只能在 instantiate 时才暴露，且报错是类型不匹配而非"协议版本不对"；
- 协议版本变成**元数据说了算**，而不是 artifact 自身可验证。

**采用**：v2 只**新增**两个函数名（`get_bar2` / `get_series_len2`），与 v1 的 `get_bar` / `get_series_len` 同住 `env` 模块。

收益：
1. 单 Runtime、单 host 模块、编译缓存全复用；
2. **协议版本由 import section 自证**：artifact 导入 `get_bar2` ⇒ 是 v2；导入 `get_bar` ⇒ 是 v1；两者都导或都不导 ⇒ fail-closed。`ValidateImports` 从"名字白名单"升级为"名字白名单 + 版本族一致性校验"，不再信任 `abi_version` 元数据；
3. `abi_version` 与 import 族不符时在**加载前**就拒绝（可在编译后、实例化前判定），错误可读。

**v3 及以后**：同样新增名字（`get_bar3`…），永不改已有名字的签名。

### 2.2 语义细节（必须冻结，否则口径漂移）

1. **缺 bar 返回 NaN，不是 0**：停牌/未上市标的在 `t` 无 bar 时 `get_bar` 返回 `NaN`，策略必须显式处理；禁止用 0 掩盖缺失。
2. **`t_offset > 0` 仍 trap**：前视隔离不变，`hostGetBar` 的 panic→trap 逻辑保留。
3. **`symbol_idx` 越界 → trap**（fail-closed，不返回 0）。**`emit_signal` 的 `symbolIdx` 同样越界即 trap**——现状（`host_api.go:257-267`）**完全无校验**，可发出幽灵标的的信号。
4. **状态序列化必须确定性**（固定顺序），否则 K15 对拍假失败。
5. **`Action` 枚举冻结**（与 `domain.Direction` 一一对应，零映射歧义）：`0=hold`、`1=long`、`2=close`、`3=short`；其余值 → trap。
6. **`Strength` 语义冻结为「信号强度/置信度」，finite ∈ [0,1]**；非 finite 或越界 → trap。**不定义为"目标权重"**——现有 Go 策略把 Strength 当强度再乘 baseWeight（`plugins/momentum.go:203`、`examples/value_momentum.go:462`），定义成权重会与既有语义冲突。
7. **资源边界（宿主侧，不受 64MB wasm 内存保护）**：
   - 每 bar `emit_signal` 次数 ≤ `2 × symbolCount`，超出 → trap；
   - 单次 `set_state` value ≤ 64 KiB，且单实例 `State` 总字节 ≤ 4 MiB，超出 → trap；
   - 理由：`hostSetState` 会 `make([]byte)` 复制进宿主 Go 内存（`host_api.go:251`），**wasm 内存上限管不住宿主侧增长**，AI 生成代码可 OOM 整个进程。
8. **`symbol_idx` 全 run 稳定**：来源 = `Scope.Universe` 排序后的下标，**不得**从每日 `Scope.MarketData` 的 keys 派生（该 map 每日重建且剔除停牌标的，见 `engine_daily.go:130-141`）；缺 bar 的标的保留索引，取数返回 `NaN`。
9. **`Axis` 唯一来源** = `Engine.getTradingDays`（`engine.go:1257-1268`）返回的 `tradingDays`，**不得**由数据日期合成；且 `Axis` 必须纳入 `dataset_snapshot_hash` —— **已冻结（QT-03，2026-10-21）**：唯一定义在 `pkg/backtest/contracts/provenance.go` 的 `DatasetSnapshotInput`（`calendar_hash` 分量，**保序**，因为轴的顺序就是 T 的推进顺序）。本处不再重述分量清单。
10. **state 必须带信封**：落库形态 `{state_schema_version, artifact_hash, abi_version, payload}`；恢复时三者与当前 artifact 不匹配即 fail-closed，不"尽力解释"、不静默重置。
11. **state scratch 失效语义**：`get_state` 的返回指针在下一次 host 调用后失效；推荐实现为 **per-bar bump allocator**（每 bar 重置、不覆盖同 bar 内已返回的块，上限 256 KiB/bar），避免"取 k1 再取 k2 后 k1 已失效"的隐蔽错。
12. **`symbol_idx` 的来源是 `Scope.RunUniverse`（run 级稳定）**，不是 `Scope.Universe`（当日可交易名单，逐日变化）。`Universe` 的语义降级为"今日可交易"，**禁止**用于索引派生。
13. **同 bar 同 symbol 多条信号 = trap**（推荐）或按冻结规则合并；现状是 map 覆盖（最后一条生效）且顺序未定，属静默歧义。
14. **`finalize` 期间的 `emit_signal` 归最后一个交易日**，且在期末强平**之前**应用；`Finalize()` 的返回值定义为**诊断字节**，不是信号通道；`finalize` 里发 `long/short` → trap（避免用最后已知价开新仓）。


## 3. 实例模型：混合（scope 声明）

策略 artifact 元数据**必填 `scope`**，未知值一律拒绝（fail-closed）：

| `scope` | 实例数 | `on_bar(t)` 频率 | `get_bar` 的 `symbol_idx` | 内存 |
|---|---|---|---|---|
| **`market`**（默认推荐） | 1 / run | **每交易日 1 次**（727 次/2 年） | 遍历 `0..get_symbol_count()-1` | 单实例 64MB **共享**（所有标的状态） |
| **`symbol`**（纯时序快速路径） | N / run，**但并发受预算约束**（分片 + 信号量，见 p0-remediation C2） | 每标的每日 1 次（约 363 万次） | 必须等于实例绑定索引，否则 trap | 每实例 64MB **独立**；单 run 总预算默认 512 MiB，超预算降并发而非失败 |

**abi v1 → scope 映射**：`abi_version=1` 的旧 artifact 按 `scope=symbol` 执行（保持旧语义，兼容期）；新编译产物一律 `abi_version=2`，必须声明 `scope`。

## 4. 不变的红线（与 v1 完全一致）

- 数据只能经 host 函数按坐标索取，**整段序列永不进入 wasm 内存**；
- `t_offset > 0` 物理不可达（trap）；
- 喂 bar 按 symbol 字典序（确定性）；
- 检查点只落在**日期边界**（否则多 symbol 平铺流会静默丢 bar）；
- 内存 64MB / 超时 30s 上限不变（另见待补的 `WithCloseOnContextDone`，G1）。

## 5. 迁移影响

| 对象 | 影响 |
|---|---|
| **NT 内核（Python/wasmtime）** | 必须实现同一 v2 签名（`get_bar` 三参数）；是否实现取决于待裁决项 C——若做，直接按 v2 做，不要先做 v1 |
| 策略 SDK / 模板 | `get_bar` 调用点全部改三参数；示例策略更新 |
| 编译服务 | 产出 `abi_version=2` + `scope` 元数据；旧 artifact 保留 v1 |
| 引擎加载器 | host 模块**一次注册 v1+v2 全部函数**（不同名，无冲突）；加载时按 import section 判定协议族（`get_bar2` → v2；`get_bar` → v1），与声明的 `abi_version` 交叉校验，不符即 fail-closed；v1 → symbol scope 兼容 |
| PoC（P4） | 从「A vs B 定案」改为「v2 语义验证」：market scope 的 64MB 共享是否够、symbol scope 分片成本 |

## 6. 未覆盖 / 已知风险

| 风险 | 说明 | 兜底 |
|---|---|---|
| market scope 的 64MB 共享内存 | 5000 标的的状态若都塞进单实例，可能超限 | 策略只存必要状态；超限时运行时报错并提示改 `scope=symbol`（不静默截断） |
| `get_series_len` 在 market scope 的语义 | 各标的停牌导致长度不一 | 以全市场共同日期轴为准（`t+1`），缺 bar 由 NaN 表达 |
| 旧策略兼容 | v1 策略默认 symbol scope，行为不变 | 兼容期内保留 v1 分派；升 v2 时重新编译 |
| 双内核协议漂移 | Python 侧实现细节可能不同 | K15 对拍 + 同签名验证用例（待 C 项裁决后补） |


## 7. 实施前必须补齐（P0，2026-10-19 终审发现）

以下五项是"设计已定方向、但实现前提缺失"的缺口。不补就写代码，会直接返工或埋并发事故。

### 7.1 Host 数据模型：当前 `BarContext` 撑不起 v2

**事实**（`host_api.go:65-72`）：`Series []float64` 只有**当前 symbol 的 close**；`CrossSection map[int32]map[string]float64` 也**只有 close**；`hostGetBar` 里 `_ = fieldID`（`host_api.go:164` 注释"本切片 field_id 未接线"）——**field_id 从未接线**。而 `Scope.MarketData` 本身就带完整 OHLCV，数据在 provider 手上、只是没往 host 侧接。

**目标形态（冻结）**：单一数据真相 = `Axis` + `Bars`，`CrossSection` 删除（它只是 close 的第二份真相）。

> `Axis` 取 `Engine.getTradingDays` 的返回值（唯一日历源），并纳入 `dataset_snapshot_hash`：日历变了而 OHLCV 不变时 T 的推进序列会变，不纳入 hash 就无法解释"同 hash 不同结果"。

```go
type BarContext struct {
    T       int32              // 当前下标：market = Axis 下标；symbol = 该标的自有序列下标
    Axis    []time.Time        // market scope 必填：共同交易日轴（升序、去重）；symbol scope 为 nil
    Symbols []string           // 稳定顺序（按 Scope.Universe 字典序），symbol_idx = 下标
    Bars    [][]domain.OHLCV   // 与 Symbols 等长；元素直接引用 Scope.MarketData[symbol]（零拷贝）
    State   map[string][]byte
    Signals []EmittedSignal
}
```

**T 的语义必须写死**（这是 v2 最容易含糊的一处）：

| scope | `T` 是什么 | `get_bar2(sym, off, field)` 解析规则 |
|---|---|---|
| `market` | `Axis` 的日期下标（每个交易日 +1） | `date := Axis[T+off]` → 取 `Bars[sym]` 中该日期的 bar；无（停牌/未上市）→ `NaN` |
| `symbol` | 该标的自身序列的下标 | 等价 `Bars[0][T+off]`；`sym` 必须为 0，否则 trap |

**field_id 枚举（冻结，SDK / NT 侧共用）**：`0=close, 1=open, 2=high, 3=low, 4=volume`；其余值 → trap（不返回 0）。

**性能**：`Bars[sym]` 内按日期查找应为**预建索引**（`[]map[int32]int32` 或 `[][]int32` 的日期→下标表），不要每次二分；market scope 的热路径是 `727 × 标的数 × 每次取数`。

**一条防未来泄漏的硬约束**：`get_series_len2` 返回的是**可见长度**（日期 ≤ `Axis[T]` 的 bar 数），**不是** `len(Bars[sym])`。原因：X1 计划引入 dataset snapshot（整段区间预加载）后，`len(Bars[sym])` 会包含未来 bar，**返回值本身就是前视**。这条现在写死，避免将来换数据供给方式时静默破防。

### 7.2 Host 模块版本化：v1 artifact 在 v2 host 上会实例化失败

**机制事实**：`WithFunc(hostGetBar)` 的 Go 签名决定 wasm 导入类型（`host_api.go:107`），v1 是 `(i32,i32)->f64`、v2 是 `(i32,i32,i32)->f64`；wasm 导入按**名字 + 类型**匹配，**同一 host 模块不能导出两个同名函数**。

**要求**（已采纳 §2.3 的增量函数名方案，取代原先的"按版本注册不同函数集"）：
1. host 模块 `env` 一次注册 **12 个函数**（8 个 v1 + `get_bar2` + `get_series_len2` + `get_date2` + `emit_target2`，其余共用）；
   > **2026-10-21 订正（QT-01 落代码时发现）**：本条原写「13 个函数」，但它自己给的枚举是 8+4=**12**。§2.1 表（ABI 的唯一真相）枚举出的冻结集合就是 12 个名字。已同步订正 `p0-round2-review.md` §8.4、`repo-topology-and-naming.md` 的合入门禁、`docs/TASKS.md` QT-01；代码侧的判据是 `contracts.HostFnCount()`（返回枚举结果，不返回文档里的字面数），并由 `abi_contract_table_test.go` 钉住。**若将来真补第 13 个函数（如 B4 里那条可选的 `is_tradable`），必须先改本表、走一次 abi 冻结，再让数字变成 13。**
2. `ValidateImports` 升级为两步：① 名字白名单（含新名）；② **版本族一致性**——导入 `get_bar2` 与 `get_bar` 互斥，一对都没有 ⇒ 拒绝；
3. 加载器用 import section 判定协议族，与声明 `abi_version` 交叉校验，不符 **fail-closed**（在 instantiate 之前，错误可读）；
4. `abi_version=1` → `scope` 强制按 `symbol`（旧语义）；声明 v1 却写 `scope=market` ⇒ 拒绝（v1 协议表达不了横截面）。

**为什么不用「按版本注册」**：`Runtime` 是进程级单例（D14）且编译缓存按 Runtime 走，两套注册要么两个 Runtime（缓存分裂）、要么每 artifact 一个 Runtime（缓存全废）；且同名不同型只能靠 instantiate 报类型错，错误不可读、版本不可自证。

### 7.3 Provider 生命周期与并发：当前注入方式会串状态

**事实（本轮复核，结论成立，但归因要修正）**：

| 事实 | 证据 |
|---|---|
| 引擎实例被 HTTP handler 共享，且允许并发 | `handlers_backtest.go:136` 经 `engineRegistry.Resolve()` 拿到同一 `EngineRunner`；`engine.go:155` 注释原文"**Engine 实例是共享的，多个回测可以并发跑**"；`RunBacktest` 全程不持全局锁（`e.mu` 只保护缓存字段） |
| `SignalProvider` 契约没有 run 身份、没有生命周期 | `signal_provider.go:41-44` 只有 `Signals(ctx, scope, asOf)`；`Scope`（`:27-38`）无 runID |
| **归因修正**：这不是 WASM 引入的，Go 流式路径**已经有同样的病** | `Registry.Get` 返回**同一实例**（`registry.go:51-61` 只做 map 查找）；`SignalStrategy` 持有 `cursor map[string]time.Time`（`signal_strategy.go:52`）——同一策略被两个 run 使用即共享游标；且队列里**没有任何 `Configure` 调用点**（`pkg/backtest` 全包 grep `Configure(` 零命中） |

所以：WASM provider 会把一个**已有的潜在缺陷**放大成必现缺陷。修的时候必须一起修，否则 Go 流式与 WASM 的"对拍"没有共同基准。

**要求**（接口级，一次改对）：

```go
// RunMeta 是一次 run 的身份与输入，供有状态 provider 建立/销毁会话。
type RunMeta struct {
    RunID     string          // = Engine 已生成的 backtestID（engine.go:518）
    Strategy  string
    Params    map[string]any  // 见 §7.6
    StartDate, EndDate time.Time
    Universe  []string
}

// SessionLifecycle 由有状态 provider 实现；无状态实现可不实现。
type SessionLifecycle interface {
    BeginRun(ctx context.Context, meta RunMeta) error
    EndRun(ctx context.Context, runID string) error
}
```

- `Engine.RunBacktest`：拿到 `backtestID` 后，若 provider 实现 `SessionLifecycle` → `BeginRun`，`defer EndRun`（含错误路径）；
- provider 内部 `map[runID]*session` **加锁**；`EndRun` 必须 `finalize()` + `Close()`；
- **装配期 fail-closed**：若注入的是"有状态实现"（明确标记，如 `Stateful() bool`）但未实现 `SessionLifecycle`，装配直接失败——不允许"跑起来再发现串状态"。

**为什么不只是"可选接口"**：纯可选意味着忘实现也照跑，缺陷静默。必须是"有状态 ⇒ 强制实现，否则装配失败"。

### 7.4 状态 scratch 区冲突：params 与 state 共用固定偏移（**升级为 P0**：params >4096 时被 state 覆盖 ⇒ 参数静默错）

**事实**（`host_api.go:231`）：`get_state` 固定写 `Memory().Write(4096, val)`；`initialize` 的 params 写在 offset 0 且**无长度上限**。params JSON 超过 4096 字节时，state 写入会**覆盖策略参数**；state 值本身也无长度上限。

**要求**：冻结 `MaxParamsBytes = 4096`（`initialize` 超限拒绝）；state scratch 从 4096 起、上限 = `memory_limit - 4096`；超限 **fail-closed 报错**，不静默截断。

### 7.5 `EmittedSignal.Action` 语义未定义（**升级为 P0**：猜错方向 = 买卖反向的静默错误）

**事实**：`EmittedSignal{SymbolIdx, Action int32, Strength float64}` 的 `Action` **在任何设计文档中都没有枚举定义**；G3 只写"字段映射 + 校验"。

**要求**（G3 前置）：冻结 `Action` 枚举与 `Strength` 语义，并给出到 `domain.Signal` 的映射表。建议：`0=hold/no-op, 1=long/enter, 2=exit/close, 3=short`；`Strength ∈ [0,1]` 表示目标权重或置信度（**必须二选一写死**，否则回测口径漂移）。

### 7.6 策略参数没有运行时投递路径（本轮新发现，P0）

**事实（三条互相印证，均为非测试代码）**：

1. `BacktestRequest`（`contracts.go:171-199`）**没有 params 字段**——只有 Strategy / StockPool / IndexCode / 日期 / 初始资金 / Kernel / EngineParams / Signals；
2. `strategies.params` 列存在（`postgres.go:290`），但读它的 `StrategyDB.GetStrategyParams`（`db.go:143`）只被 `ReloadFromDB`（`registry.go:274`）调用，而 **`ReloadFromDB` / `ConfigureStrategy` 在 cmd/pkg 中零生产调用点**（grep 已确认）；
3. 因此：**内置策略一直跑编译期默认参数**，DB 里的 `params` 从未在回测执行路径生效。

这对 WASM 是硬阻塞：`Initialize(paramsJSON)` 的 params **没有来源**；M0 的 regime 阈值也无处传入。

**设计要求（建议，待裁决 L）**：

- **两层参数**：定义层默认值（随策略版本走，进 `version_hash`）+ **run 层覆写**（进 RunSpec）；
- `BacktestRequest` 增加 `StrategyParams map[string]any`，校验沿用 `EngineParams` 的**未知名拒绝**口径（`ValidateRunSpec`）；
- 生效参数 = 定义默认 ⊕ run 覆写，**由加载器解析后传给 provider**（经 `RunMeta.Params` → `initialize`）；
- 运行记录必须存**解析后的生效参数**（`run_artifacts.params_json` + `params_hash`），否则"同参复现"仍是空话；
- 禁止在执行期读 `strategies.params`（否则改一行 DB 就改变历史结果，且 hash 不变）。

### 7.7 Go 流式策略需要 per-run 实例（`BE-21`，与 §7.3 同源）

`Registry.Get` 返回注册时的**同一实例**，`SignalStrategy.cursor` 这类字段因此跨 run 存活。修法二选一：

- `Registry` 增加 `NewInstance(name) (Strategy, error)`（注册时存**构造函数**而非实例；`init()` 自注册改为注册 factory）；
- 或给 `BarHandler` 路径定义 `Reset()` 契约，`BeginRun` 时强制调用（较弱：漏实现就漏重置）。

推荐前者（构造型注册）。**这条必须在 K15「Go 内置 vs WASM 逐位一致」之前完成**，否则对拍的是两个都带残留状态的实现，结论无意义。

### 7.8 本轮复核的结论修正（诚实记录）

| 原判定 | 复核结论 |
|---|---|
| P0-3 的修法「按 abi_version 注册不同函数集」 | **修法不成立/次优** → 改为增量函数名（§2.3），单 Runtime 且版本可自证 |
| P0-1 表述为"WASM provider 会串状态" | **问题成立，归因需修正**：这是既有契约缺陷（Go 流式同样中招），WASM 只是让它必现 |
| 原 5 项之外 | **新增 P0-6（参数无投递路径）与 P0-7/BE-21（流式实例共享）**，这两条比 P0-3 更早阻塞 M0 |

---

## 8. 追加项（2026-10-20 裁决，见 `p0-round2-review.md` §8）

| 追加 | 内容 | 为什么必须与 v2 同批 |
|---|---|---|
| `emit_target2` | 目标权重表达（§2.1 表已补） | 与 `get_bar2` 同属"artifact 出厂后改不动"的 ABI 面；M0b 的「分层加仓」验收依赖它 |
| `FillTiming` | **不是 host 函数**，属 `SignalSpec`：`next_open`（终态）/ `same_close`（兼容） | 它决定"读了当日 close 之后按什么价成交"；NT 侧必须同口径，否则 K15 的差异无法归因 |
| host 区所有权 | 模块必须导出 `__host_params_ptr` / `__host_scratch_ptr` / `__host_scratch_size`；缺失 fail-closed | A6 只定了偏移与上限，没定"这段内存归谁"；没有导出符号就只能靠"TinyGo 当前布局恰好留空"的巧合。**地址空间权威已定（QT-62）**：模块自己的静态数据 = Data section 活动段并集，宿主区 = 三个导出 global 的声明；两者相交即拒绝 artifact（不改地址、不缩区间、不仲裁先后）。实现与证据见 [`host-region-overlap.md`](host-region-overlap.md) |

**冻结纪律**：以上三项 + `get_bar2/get_series_len2/get_date2` 必须在**同一次** abi v2 冻结中一起定稿；已出厂 artifact 不得再补签名。
