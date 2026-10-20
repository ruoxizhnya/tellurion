# P0 修复设计（实施前基线）

> 状态：**设计基线（待用户确认后进入实施）**，2026-10-20
> 关联：ADR-D17（落地计划）、ADR-D25（B/I/J 裁决）、`adr-029-amendment-host-api-v2.md`（host API v2）、BE-21/BE-22
> 性质：只定契约与语义，不含实现代码
> **第二轮审查（2026-10-20）**：本篇 20 项的闭环核对结论 + 新增 N1~N9（含本篇 A3/A4/A6/A9/B4 的半闭环补齐文本）见 [`p0-round2-review.md`](p0-round2-review.md)。**两份文档合起来才是完整的 P0 基线**；依赖顺序以 round2 §4 为准（新增 wave-0 契约冻结）。

## 0. 什么算 P0（判据，先定再列）

一条缺陷算 P0，必须命中以下任一：

| 判据 | 含义 | 为什么最贵 |
|---|---|---|
| **A. 静默错误** | 系统正常返回，但结果是错的 | 回测曲线照样漂亮，事后无法反查——比崩溃贵一个量级 |
| **B. 不可用/挂死** | 单次输入能让进程级服务失效 | 影响所有租户/所有 run |
| **C. 不可复现** | 同一输入两次跑结果不同 | 回测的结论失去意义，K15 对拍失效 |

**不算 P0**：能跑但慢、功能缺失但报错清晰、文档措辞。这些进 P1/P2。

按此判据，当前共 **20 项 P0**，分四组（A 12 / B 5 / C 2 / D 1）。

## 1. 总表

| 组 | # | 问题 | 判据 | 一句话修法 | 工单 |
|---|---|---|---|---|---|
| **A 执行正确性** | A1 | Provider/session 无生命周期，共享 Engine 上串状态 | A+C | `RunMeta` + `SessionLifecycle`（BeginRun/EndRun），有状态未实现即**装配失败** | G7 |
| | A2 | 策略实例跨 run 共享（`Registry.Get` 返回单例，`SignalStrategy.cursor` 存活） | A+C | Registry 改**注册构造函数**，执行路径一律 `NewInstance` | G11 / BE-21 |
| | A3 | 策略参数无运行时投递路径（`BacktestRequest` 无 params；`ReloadFromDB` 零调用点） | A+C | 两层参数：定义默认（进 hash）+ run 覆写（进 RunSpec）+ 生效值落运行记录 | G10 / BE-22 |
| | A4 | `version_hash` 未覆盖 `scope`（及默认参数） | A | canonical 集合补 `scope` + `params_defaults`，冻结算法并加向量测试 | — |
| | A5 | `Action`/`Strength` 语义未定义；`emit_signal` 无越界校验 | A | 枚举对齐 `domain.Direction`；Strength=强度∈[0,1]；越界 trap | G9 |
| | A6 | params 写 offset 0 无上限、state 固定写 offset 4096；`set_state` 无限长 | A+B | 冻结内存布局与上限，超限 fail-closed | G13 |
| | **A7** | **共享 Engine 在 run 内替换引擎级缓存**（`warmFactorCache` / `warmFundamentalsIfNeeded`） | **A** | run 级缓存不挂共享引擎：per-run 缓存对象，或 per-run Engine | G14 |
| | **A8** | **信号顺序未规范化**（engine 不排序；`value_momentum.go:366` `range map` 已证非确定） | **C** | 应用前按 `(symbol, direction, strength)` 稳定排序 | G15 |
| | **A9** | **检查点状态无 schema 版本**（`State map[string][]byte` 裸存） | **A** | state 带 `state_schema_version` + `artifact_hash`，恢复时校验 | G16 |
| | **A10** | **L1 OHLCV 缓存跨 run 单调合并、永不清理**（`Warm` 是 merge，`LoadOHLCVInMemory(nil)` 无生产调用点） | **B**（慢性 OOM） | 缓存下沉 per-run；或按字节预算 + LRU 驱逐 | G17 |
| | **A11** | **复权锚点 digest 覆盖"合并后全序列"而非 run 区间** ⇒ 重启后同参 run 校验失败 / 锚点窗口错 | **A+C** | `fullBars` 先按 `[StartDate, EndDate]` 切片再算 digest | G18 |
| | **A12** | **同 bar 同 symbol 多条信号的语义未定义**（现为 map 覆盖 = 最后一条生效，顺序决定） | **A** | 冻结语义：重复 → trap（推荐）或明确定义合并规则 | G19 |
| **B 协议可实现性** | B1 | `BarContext` 只有 close，`field_id` 从未接线；`T` 语义未定义 | A（写不出/写错） | `Axis + Bars + Symbols` 单一真相，冻结 T 语义与 field 枚举 | G8 |
| | B2 | ABI 面缺"当前日期" | A（状态机不可表达） | 新增 `get_date2() -> YYYYMMDD`，**现在定，成本近零** | G12 |
| | B3 | v1/v2 同名改签名会撞 import 类型，无法共存 | B | 增量函数名（`get_bar2`…）+ import section 自证版本 | G8 |
| | **B4** | **`Scope.Universe` 每日变化**（传的是 `eligibleUniverse(date)` 结果），与 symbol_idx 稳定性直接冲突 | **A** | 新增 run 级稳定 `RunUniverse`；当日可交易性用 mask/NaN 表达 | G20 |
| | **B5** | **`finalize` 产生的信号归属未定义**（`Finalize` 返回 `[]byte`，不是信号） | **A**（终局单静默丢） | 冻结：finalize 期间的 `emit_signal` 归最后一个 bar，在其清算前应用 | G8 |
| **C 可用性** | C1 | `WithCloseOnContextDone` 未启用，wasm 死循环不可终止 | B | 启用 + 双层预算 + **trap≠timeout** 的终止语义 | G1 |
| | **C2** | **`symbol` scope 的实例并发与内存预算未定义**（设计只写"N/run 分片"，5000 实例 × 独立内存） | **B** | 分片执行 + 并发信号量 + 总 wasm 内存预算 | G21 |
| **D 流程** | D1 | M0 计划按 per-symbol 写，与 D25「市场级优先」冲突 | A（验收基准错） | M0 改市场级、M0b 才做 symbol scope | — |

---

## 2. 逐项设计

### A1 · Provider / session 生命周期（G7）

**裁决**：生命周期进入契约，且**"有状态 ⇒ 必须实现，否则装配失败"**，不做"可选接口 + 静默降级"。

```go
// pkg/strategy（或 contracts）——run 身份与输入的不可变快照
type RunMeta struct {
    RunID    string          // 复用 Engine 已生成的 backtestID（engine.go:518）
    Strategy string
    Params   map[string]any  // 解析后的生效参数（见 A3）
    Start, End time.Time
    Universe []string
}

type SessionLifecycle interface {
    BeginRun(ctx context.Context, meta RunMeta) error
    EndRun(ctx context.Context, runID string) error
}

// 显式声明"我有状态"——装配期据此强制校验
type Stateful interface{ Stateful() bool }
```

**规则**：
1. `Engine.RunBacktest` 在拿到 `backtestID` 后若 provider 实现 `SessionLifecycle` → `BeginRun`，`defer EndRun`（错误路径同样执行）；
2. `EndRun` 必须 `finalize()` + `Close()`；`finalize` 冲刷的信号归属该 runID，不允许跨 run 合并；
3. provider 内部 `map[runID]*session` 加锁；session 数上限（建议 ≤ 并发 run 上限 × 1.2）超限 fail-closed，不排队悄悄阻塞；
4. **装配期**（`bootstrap`）：`Stateful()==true` 但未实现 `SessionLifecycle` ⇒ 直接 Fatal；
5. **顺序 run 也必须走 BeginRun/EndRun**——不做"顺序复用会话"的优化，否则残留状态回归。

**验收**：① 同一策略并发两笔回测（不同 universe）结果与各自单独跑逐位一致；② 连续跑两次同一回测，第二次结果与第一次一致；③ 有状态但未实现 lifecycle 的 provider 在装配期失败。

---

### A2 · 策略 per-run 实例（G11 / BE-21）

**裁决**：注册表从"存实例"改为"存构造函数"，**执行路径禁止使用共享实例**。

```go
func (r *Registry) RegisterFactory(name string, f func() Strategy) error
func (r *Registry) NewInstance(name string) (Strategy, error)  // 执行路径唯一入口
func (r *Registry) Get(name string) (Strategy, error)          // 保留：仅元数据查询（Parameters/Info）
```

**迁移与护栏**：
1. 14 个内置插件的 `init()` 从 `Register(&x{})` 改为 `RegisterFactory("x", func() Strategy { return &x{} })`；
2. `Register(Strategy)` 保留但标记 `shared=true`；**`NewInstance` 遇到 shared 实例直接报错**（fail-closed），迫使迁移完成；
3. `LocalSignalProvider.Signals` 与 WASM provider 都必须走 `NewInstance`；
4. 加一条 repoguard：`pkg/backtest` 与 provider 实现内出现 `.Get(` 即红。

**为什么不用 `Reset()`**：Reset 是"自觉遵守"，漏实现就漏重置，缺陷静默；构造函数是结构性保证。

**验收**：同一策略顺序跑两次结果一致；并发不串；`Get` 在执行路径 grep 零命中。

---

### A3 · 策略参数投递（G10 / BE-22）

**现状事实**：`BacktestRequest`（`contracts.go:171-199`）无 params；`strategies.params` 只被 `GetStrategyParams` 读，而 `ReloadFromDB`/`ConfigureStrategy` 在 cmd/pkg **零生产调用点** ⇒ 内置策略恒用编译期默认值。

**裁决：两层参数**

| 层 | 归属 | 是否进 `version_hash` | 说明 |
|---|---|---|---|
| 定义默认值 `params_defaults` | 策略定义（资产服务） | ✅ 进 | 改默认值 = 改策略版本（与因子 `version_hash` 同口径） |
| run 覆写 `strategy_params` | `BacktestRequest` | ❌ 不进策略 hash，进 `run_spec_hash` | 参数扫描（sweep）不改策略版本 |

```go
// contracts.BacktestRequest 追加
StrategyRef    string         `json:"strategy_ref,omitempty"`   // "id@hash" | "id"
StrategyParams map[string]any `json:"strategy_params,omitempty"`
```

**解析与校验**：
1. 生效参数 = `params_defaults ⊕ strategy_params`（浅合并；嵌套键不支持，避免歧义）；
2. **未知名拒绝**（沿用 `EngineParams` 口径，`ValidateRunSpec`）：未知键 400，不静默忽略；
3. 类型/范围校验由策略清单声明（WASM 无 Go 类型系统，必须自带 schema）：`manifest.params[] = {name, type, min, max, required}`；
4. 生效参数经 `RunMeta.Params` → `initialize(paramsJSON)`；**执行期禁止读 `strategies.params`**；
5. 运行记录必须存 `params_json` + `params_hash`（否则"同参复现"仍是空话）。

**验收**：① 同一策略不同参数并发两笔各自生效；② 未知键返回 400 并点名；③ 从历史记录可完整还原生效参数。

---

### A4 · `version_hash` 覆盖行为决定项

**裁决**：`version_hash` 必须覆盖"**哪些输入会改变执行结果**"，canonical 集合冻结为：

```
strategy_id + carrier + abi_version + scope + entry + source + params_defaults
```

**规则**：
- 算法冻结：canonical JSON（键排序、无空白、UTF-8、数字不经 float 往返）→ SHA-256 小写 hex；
- 加**向量测试**（给定输入 → 固定 hash 常量），防止有人改序列化顺序导致全量缓存失效；
- `scope` 进 hash 的理由：同源码 `market` vs `symbol` 的执行模型完全不同（727 次 vs 363 万次调用、状态隔离粒度不同）；
- `params_defaults` 进 hash 的理由：默认值改变执行结果，不进 hash 会让缓存命中错版本。

**验收**：改 scope / 改默认参数 / 改源码，三者任一都产生新 hash；只改 run 覆写参数不产生新 hash。

---

### A5 · Action / Strength 冻结（G9）

**裁决**：与既有 `domain.Direction` 一一对应，零映射歧义：

| Action | 语义 | → `domain.Direction` |
|---|---|---|
| 0 | hold / no-op | `DirectionHold` |
| 1 | long / 开多 | `DirectionLong` |
| 2 | close / 平仓 | `DirectionClose` |
| 3 | short / 开空 | `DirectionShort` |
| 其他 | **trap** | — |

**Strength**：定义为**信号强度/置信度，finite ∈ [0,1]**，越界或 NaN/Inf → trap。

> 为什么不定义为"目标权重"：现有 Go 策略把 Strength 当强度再乘 baseWeight（`plugins/momentum.go:203`、`examples/value_momentum.go:462`、`plugins/mean_reversion.go:312`）。定义成权重会与既有语义冲突，K15 对拍立刻失真。
> **若你要"目标权重"语义**（跨策略组合更自然），那是另一套设计：需同时改 Go 侧消费点与 `SignalSpec`，建议单独立项，不要混在 v2 里。

**`emit_signal` 校验（现状全缺）**：
- `symbolIdx ∉ [0, symbolCount)` → trap；
- `action ∉ {0,1,2,3}` → trap；
- `strength` 非 finite 或 ∉ [0,1] → trap；
- **每 bar 发射数 ≤ 2 × symbolCount** → 超出 trap（防宿主内存被打爆，见 A6）。

**验收**：越界/非法值均 trap 而非静默；Go 与 WASM 同逻辑策略的 Strength 分布都在 [0,1]（否则先统一口径再对拍）。

---

### A6 · params / state 内存布局（G13）

**事实**：`initialize` 写 `WriteMemory(0, params)` **无长度上限**；`get_state` 固定写 `Write(4096, val)`；`set_state` 把 value `make([]byte)` 复制进宿主 Go 内存，**无长度/总量上限**。三者叠加 ⇒ params > 4096 时被 state 覆盖（参数静默错），以及宿主侧可被 OOM。

**裁决：冻结内存布局**

| 区域 | 区间 | 上限 | 超限行为 |
|---|---|---|---|
| params（只读） | `[0, 4096)` | `MaxParamsBytes = 4096` | `Initialize` 直接拒绝（不截断） |
| state 返回 scratch | `[4096, 4096+64KiB)` | `MaxStateValueBytes = 64 KiB` | `get_state` 返回 0,0（未命中）或 trap（按实现二选一并冻结） |
| 其余 | wasm 线性内存 | 由 `WithMemoryLimitPages` 管 | 实例化/增长失败 |

**另有宿主侧总量闸**：
- 单实例 `State` 总字节 ≤ **4 MiB**（key 数 ≤ 4096）；
- 超限 → `set_state` trap，错误信息点名 `state_budget_exceeded`。

**scratch 的失效语义（易漏，必须写死）**：`get_state` 返回的是**共享 scratch 的指针**（固定 4096），**任何后续 host 调用都会覆盖它**。两种处理：
- 便宜方案：文档声明"返回指针在下一次 host 调用后失效"，SDK 模板强制立即 `memcpy`；
- 更安全方案（推荐）：**per-bar bump allocator**——每次 `on_bar` 开始重置游标，`get_state` 依次分配、不覆盖前一次结果，直到该 bar 结束；配 `MaxScratchPerBar = 256 KiB`，超限 trap。
理由：AI 生成代码「先取 k1、再取 k2、再拼装」是最自然的写法，共享 scratch 会让它拿到被覆盖的 k1，**表现是状态随机错乱、极难定位**。

**为什么必须现在定**：这是 ABI 的一部分（策略作者会依赖偏移与上限），事后调整等于破坏所有已编译 artifact。

**验收**：params 4097 字节 → 拒绝且错误可读；写 4 MiB+1 state → trap；params 与 state 互不覆盖（回归测试）。

---

### A7 · 共享 Engine 的 run 级缓存污染（G14）— 本轮新发现，**今天就存在**

**事实**：

| 事实 | 证据 |
|---|---|
| `RunBacktest` 在**每个 run 开始**替换引擎级缓存 | `warmFactorCache(ctx, start, end)`（`engine.go:783`）→ `e.factor.Warm(...)`；`warmFundamentalsIfNeeded(...)`（`:790`）→ **`e.fundamentals = records`**（`:1245`），`records` 来自"本次 run 的 symbols + asOf" |
| Engine 被 HTTP handler 共享 | `handlers_backtest.go:136` 经 `engineRegistry.Resolve()` 取同一 runner |
| 项目自己已承认这个坑 | `cmd/analysis/main.go` 的 `newEngine` 注释原文：「**共享单例会让并发窗口互相污染 —— 因子缓存是「整体替换」语义，窗口 B 的 Warm 会直接覆盖窗口 A 正在读取的缓存**」——但该注释只对 walk-forward 生效，HTTP 路径仍是共享单例 |

**后果**：两笔并发回测（不同 universe/区间），后启动者会覆盖前者正在使用的因子 z-score 与财报快照 ⇒ 前者拿到缺失/错标的数据。基本面策略会静默少算，因子策略会静默用错 z-score。**这是判据 A（静默错误）+ C（不可复现）双中**。

**设计要求（二选一，推荐 A）**：

- **A. 缓存下沉到 run 作用域**：把 `fundamentals` / factor cache / `listing` 从 `Engine` 字段移到 `runContext`（`runBacktestInternal` 内创建），通过参数传给 `getSignals`/策略注入。`Engine` 只保留**不可变**配置（provider/store/workers）。这同时让 A1/A2 的"per-run"语义统一。
- **B. Engine 变 per-run**：`EngineFactory.NewRun(req)` 每次返回新实例；HTTP handler 每请求取新引擎。改动小但实例化成本与连接复用要评估。

**禁止的"修法"**：加锁串行化整个 `RunBacktest`——会把并发能力砍掉，且掩盖问题（run 内替换仍然存在，只是不可见）。

**验收**：两笔并发（universe A / universe B，其中 A 有财报数据、B 没有）结果与各自串行跑逐位一致；抓一个「B 先 Warm、A 后读」的确定性交错用例。

---

### A8 · 信号顺序规范化（G15）— 本轮新发现

**事实**：
- `RunBacktest` 只对 `tradingDays` 排序（`engine.go:1263`），**对 signals 不排序**；
- 信号按 provider 返回顺序直接进入撮合（`engine.go:1553`、`engine_daily.go:297`）；
- 至少一个内置策略直接 `for symbol, d := range data`（`examples/value_momentum.go:366`）——**Go map 遍历顺序随机** ⇒ 同一输入两次跑，signals 顺序可能不同；
- 顺序敏感：在现金/仓位约束下，先处理谁决定谁成交（部分成交、手数取整、单票上限都是顺序依赖）。

**后果**：判据 C（不可复现）。且 Go 与 WASM 的 emit 顺序天然不同 ⇒ **K15 对拍会假失败**（或更糟：假通过，因为差异被其他因素掩盖）。

**设计要求**：
1. `Engine` 在应用信号前做**稳定排序**，键：`(Symbol asc, Direction 固定序, Strength desc, CompositeScore desc)`；同键保持原序（stable）；
2. 排序口径写进 `SignalSpec`，Go/WASM/NT 三方共用；
3. 若排序会改变现有基准结果（在现金约束场景下可能），先跑一次"旧顺序 vs 新顺序"的差异报告，确认差异可解释后再切换；**不允许**为了"保持结果不变"而放弃排序。

**验收**：同一输入连跑 20 次信号序列完全一致；`value_momentum` 在 20 次跑动中结果逐位一致。

---

### A9 · 检查点状态无 schema 版本（G16）

**事实**：`BarContext.State` 是 `map[string][]byte` 裸存（`host_api.go:70`）；`checkpoint.go` 只按日期边界裁决保存点，**没有任何 schema/artifact 版本标记**（全包 grep `schema_version|state_version` 零命中）。

**后果**：升级策略 artifact（状态结构变了）后**恢复旧检查点**，会按新结构解释旧字节 —— 判据 A（静默错误）。这类 bug 的典型表现是"换个版本跑，前两天结果奇怪然后恢复正常"。

**设计要求**：
- 状态落库时附带 `{state_schema_version, artifact_hash, abi_version}`（与策略版本一起存，`quant.strategy_state` 加列或包一层信封）；
- 恢复时**三者必须与当前 artifact 匹配**，不符 fail-closed 并点名（不静默重置、不"尽力解释"）;
- 策略清单里声明 `state_schema_version`，改结构必须自增；护栏：源码 hash 变了但 `state_schema_version` 没变 ⇒ **告警**（不阻断，因为改逻辑不改结构是合法的）。

**验收**：旧 artifact 写入的检查点，被新 artifact 恢复时被拒且错误可读；同版本恢复成功且三路一致（Batch ≡ Step 从零 ≡ Step 从恢复）。

---

### A10 · L1 OHLCV 缓存跨 run 单调合并、永不清理（G17）— 本轮新发现

**事实**：

| 事实 | 证据 |
|---|---|
| 每个 run 预热本 run 的票池与区间 | `RunBacktest` → `e.warmCache(warmCtx, params.StockPool, params.StartDate, params.EndDate)`（`engine.go:775`） |
| `Warm` 是**合并**不是替换 | `pkg/backtest/cache/cache.go:127-140`：`mergeBars(existing, bars)`，并扩展 `warmStart/warmEnd` 并集 |
| 没有任何清理调用点 | `LoadOHLCVInMemory(nil)` 全仓无生产调用（只有注释提到"跨回测需调用"） |
| 注释与现实相反 | `cache.go:43-46` 写"缓存是 **per-backtest-lifecycle** 的——warm 一次，跑一次回测"；但 `Engine` 在 handler 里是**共享单例** |

**后果**：不同 universe 的 run 会把行情**累积**进同一个共享缓存，永不释放。全市场 5000 标的 × 数年日线 ≈ 数百 MB；跑几天不同票池即可到 GB 级。判据 **B（慢性 OOM）**，不是立刻炸，所以更容易被忽略。

**设计要求**：
- 首选与 A7 同一刀：**缓存移入 per-run `runContext`**，run 结束随 GC 释放；
- 若保留进程级缓存：必须按**字节预算**（如 2 GiB）+ LRU 驱逐，并打 `cache_bytes` 指标；容量按条数是错的（bar 数差异巨大）；
- 无论哪种：**禁止**"合并后永不清理"这一现状（它让内存与"跑过多少次"线性相关）。

**验收**：连续跑 50 个不同票池的 run 后，常驻内存回落到基线 ±10%；同一 run 重复跑结果不变。

---

### A11 · 复权锚点 digest 覆盖"合并后全序列"而非 run 区间（G18）— 本轮新发现

**事实链（每一环都已核实）**：

1. `newAdjustmentRunForRun` → `prepareAdjustmentRun(..., fullBars: func(sym) []OHLCV { return e.cache.Peek(sym) })`（`adjustment_snapshot.go:322`）；
2. `Peek` 返回**合并缓存里的整条序列**（`cache.go:268`），而合并缓存跨 run 累积（A10）；
3. 新锚点用 `bars[0].Date` 当 `AnchorDate`、`seriesDigest(quantizeBars(bars))` 当 digest（`adjustment_snapshot.go:151-156`）——**覆盖的是整条缓存切片，不是 `[StartDate, EndDate]`**；
4. 复用已冻结锚点时走 `verifyAgainstFrozen(sym, bars, anchor)`，digest 不匹配即 **返回 error → 整个 run 失败**（`:134-137`）。

**后果（两条，都是判据 A/C）**：

| 场景 | 表现 |
|---|---|
| 进程内先跑过更长区间的 run，再跑短区间 run | 短区间 run 的 digest 覆盖了更长序列；进程重启后缓存清空、digest 变成短序列 → **锚点校验失败，同参数 run 直接报错**（看起来像"数据损坏"） |
| 首次跑（无锚点）时缓存里有别的 run 的旧数据 | 写入的锚点 `AnchorDate` 早于本 run 的 `StartDate`、digest 覆盖超范围 → 该 `(symbol, start_date)` 的锚点从此**永远校验不过** |

**设计要求**：
- `fullBars` 必须**先按 `[params.StartDate, params.EndDate]` 切片**再交给 `prepareAdjustmentRun`；`AnchorDate` 必须等于该切片的 `[0]`；
- digest 的输入集合写入当前 `(symbol, start_date)` 行；`start_date` 已是主键的一部分（`ON CONFLICT (symbol, start_date)`），语义要对齐；
- 加一条回归测试：**"先跑长区间、再跑短区间、再重启（清缓存）跑短区间"，三次结果一致**。

**为什么这条值钱**：REV-18 的目标就是"同区间重跑逐位一致"，而它现在依赖一个**跨 run 累积的共享缓存**——保护机制自己被 A10 污染了。这也说明 A10 与 A11 必须一起修。

---

### A12 · 同 bar 同 symbol 多条信号的语义未定义（G19）

**事实**：两条建仓路径都是 `map[symbol]PositionSize` 赋值覆盖：
- 风控路径：`CalculatePositionsBatch` 里 `results[signal.Symbol] = ...`（`pkg/risk/manager.go:185-220`）；
- 兜底路径：`calculatePositionsBatch` 里 `results[sig.Symbol] = ps`（`engine.go:1552-1560`）。

即**最后一条生效**，而"最后"取决于未经排序的信号顺序（A8）⇒ 同 bar 同时发 `long` 与 `close` 时，**哪条生效是不确定的**。

**后果**：判据 A（静默错误）+ 判据 C。典型触发：状态机在"平旧仓 + 开新仓"或"分批加仓"时对同一标的发多条。

**设计要求（推荐第一条）**：
1. **重复即 trap**：同一 bar 同一 symbol 出现 >1 条信号 → 该 run 失败并点名 symbol/两条 action。理由：语义歧义不该由引擎猜；
2. 或者**显式定义合并**：`close` 优先于 `long/short`；同向多条按 strength 求和并 clamp——但这要求排序（A8）先落地，否则合并不确定。

无论选哪种，**必须写进 `SignalSpec`**，Go 侧同逻辑策略一起遵守（否则 K15 对拍再次失真）。

**验收**：同 bar 对同标的两条相反 action，行为确定（trap 或按冻结规则合并），且重复 20 次结果一致。

---

### B1 · Host 数据模型（G8）

**裁决**：单一数据真相 = `Axis` + `Bars` + `Symbols`；`CrossSection` 删除。

```go
type BarContext struct {
    T       int32              // market: Axis 下标；symbol: 该标的自有序列下标
    Axis    []time.Time        // market 必填：共同交易日轴（升序、去重）；symbol 为 nil
    Symbols []string           // 稳定顺序（Universe 字典序），symbol_idx = 下标
    Bars    [][]domain.OHLCV   // 与 Symbols 等长；直接引用 Scope.MarketData（零拷贝）
    State   map[string][]byte
    Signals []EmittedSignal
}
```

**T 的语义（必须写死，这是最容易含糊处）**：

| scope | `T` | `get_bar2(sym, off, field)` 解析 |
|---|---|---|
| `market` | `Axis` 下标，每交易日 +1 | `date := Axis[T+off]` → 该 symbol 在 `date` 的 bar；缺 → `NaN` |
| `symbol` | 该标的自身序列下标 | 等价 `Bars[0][T+off]`；`sym != 0` → trap |

**field_id 枚举（冻结）**：`0=close, 1=open, 2=high, 3=low, 4=volume`；其余 trap。

**性能**：`Bars[sym]` 需预建"日期 → 下标"索引（`[]map[int32]int32` 或 `[][]int32`），不要每次二分；market scope 热路径是 `727 × 标的数 × 取数次数`。

**一条防未来泄漏的硬约束**：`get_series_len2` 返回**可见长度**（日期 ≤ `Axis[T]` 的 bar 数），**不是** `len(Bars[sym])`。原因：X1 的 dataset snapshot 会整段预加载，那时 `len` 包含未来 bar，**返回值本身就是前视**。

**两条必须写死的索引约束（否则状态错位、结果不可复现）**：

1. **`symbol_idx` 必须全 run 稳定**，来源 = `Scope.Universe` 排序后的下标，**不得**从 `Scope.MarketData` 的 keys 派生。
   证据：`fetchMarketDataForDay` 每日重建 map，且**停牌标的不进 map**（`engine_daily.go:130-141`，REV-09 裁决）。若从 keys 派生，索引会逐日漂移——策略按 idx 存的状态会串到别的标的上，**静默错**。
   缺 bar 的标的：`Bars[sym]` 保留（可 nil/空），取数返回 `NaN`，不删索引。
2. **`Axis` 必须来自唯一的交易日历源**：`Engine.getTradingDays` → `Provider.GetTradingDays`（`engine.go:1257-1268`），即 `tradingDays` 切片；**不得**由"数据里出现过的日期"合成。
   并且 **`Axis` 必须纳入 `dataset_snapshot_hash`**（X0）：日历变了（供应商/兜底不同、补充了节假日）而 OHLCV 不变时，T 的推进序列会变，结果会变——不纳入 hash 就无法解释"同 hash 不同结果"。

**顺带修掉的现状缺陷**：`hostGetBar` 的 `_ = fieldID`（`host_api.go:164`）与 `CrossSection` 只存 close——v2 之后两者都不再成立。

**验收**：market scope 能取到 5 个字段；停牌返回 NaN；`get_series_len2` 在"整段预加载"模拟下仍只返回可见长度。

---

### B2 · ABI 面补 `get_date2`（G12）

**裁决**：v2 一次补齐 `get_date2() -> i32`（`YYYYMMDD`，UTC）。

**理由**：状态机需要日历锚点（月末调仓、持有 N 个交易日、事件窗口、冷却期跨假期）。现有 8 个 host 函数**没有任何时间来源**（`host_api.go:40-47`），策略只能数 `on_bar` 次数，无法知道真实日期。

**为什么现在定**：v2 采用增量函数名策略（B3），以后再补会让"v2"内部出现两种形态（有/无 `get_date2`），artifact 能力矩阵出现第三个维度。现在加，成本≈0。

**验收**：`get_date2` 返回与引擎 `asOf` 同日；跨周末/假期序列连续且无时区漂移。

---

### B3 · v1/v2 版本共存（G8 内）

**问题机制**：`WithFunc` 的 Go 签名决定 wasm 导入类型；v1 `(i32,i32)->f64`、v2 `(i32,i32,i32)->f64`；wasm 导入按**名字+类型**匹配，**同一 host 模块不能导出两个同名函数**。

**裁决：增量函数名，不改已有签名**

| v1（保留） | v2（新增） |
|---|---|
| `get_bar(t_offset, field_id)` | `get_bar2(symbol_idx, t_offset, field_id)` |
| `get_series_len()` | `get_series_len2(symbol_idx)` |
| — | `get_date2()` |

**机制**：
1. `env` 模块一次注册 v1+v2 全部函数（不同名，无冲突）；单 Runtime、编译缓存全复用；
2. `ValidateImports` 升级两步：① 名字白名单；② **版本族一致性**——导入 `get_bar2` 与 `get_bar` 互斥，一对都没有 ⇒ 拒绝；
3. 加载器用 import section 判定协议族，与 `abi_version` **交叉校验**，不符在 instantiate 之前 fail-closed；
4. `abi_version=1` ⇒ `scope` 强制为 `symbol`；声明 v1 却写 `scope=market` ⇒ 拒绝。

**否决的替代方案**：「按 abi_version 注册不同函数集」——会逼出双 Runtime（与 D14「Runtime 进程级单例 + 编译缓存」冲突），且同名不同型只能靠 instantiate 报类型错、版本不可自证。

**验收**：同一 Runtime 上 v1 artifact 与 v2 artifact 都能实例化；`abi_version` 与 import 族不符时报错可读且发生在实例化前。

---

### B4 · `Scope.Universe` 每日变化，与 symbol_idx 稳定性冲突（G20）— 本轮新发现

**事实**：`engine.go:916` 把**当日**的可交易池直接作为 `Scope.Universe` 传下去：

```go
universe := e.eligibleUniverse(params.StockPool, date, held)   // 按当天在市/持仓过滤
signals, err := e.getSignals(ctx, params.StrategyName, universe, marketDataCache, date, ...)
```

即 `Scope.Universe` **逐日不同**（今天停牌/退市的被剔掉、昨天新买的加进来）。

**后果**：B1 已经要求"`symbol_idx` 由 `Universe` 排序派生、全 run 稳定"——但 `Universe` 自己每天在变。若实现时直接用它，索引逐日漂移，策略按 idx 存的状态会串到别的标的，**静默错**（判据 A）。

**设计要求**：

- `Scope` 拆成两个字段，语义必须分清：
  - **`RunUniverse []string`（新增，run 级稳定）**：= 本次 run 解析出的票池超集（`StockPool ∪ 曾持有`），**排序后**固定不变，`symbol_idx` 只能由它派生；
  - `Universe []string`（保留，当日）**降级为"今日可交易名单"**，仅供批式策略做当日筛选，**禁止**用于索引；
- 当日不可交易的表达：`get_bar2` 返回 `NaN` + 可选 `is_tradable(sym) -> i32`（新增，或由 NaN 推断）；
- 该改动会影响 Go 批式策略的读法（多数按 `MarketData` 迭代，不受影响；少数按 `Scope.Universe` 迭代的需回归）。

**验收**：连续 5 个交易日里 Universe 有增减，`symbol_idx` 映射始终不变；策略跨日状态不串标的。

---

### B5 · `finalize` 产生的信号归属未定义（G8 内）

**事实**：`OnBarSession.Finalize` 返回 `[]byte`（`on_bar_session.go:95-113`，从 `finalize` 的 `i64 ptr/len` 读回），**不是信号**；`emit_signal` 写入的是 `BarContext.Signals`。所以在 `finalize` 里发的信号会落进最后一个 `bc.Signals`——而那时日循环已经结束、引擎可能已进入清算阶段。

**后果**：状态机"收尾平仓/终局调仓"这类最自然的写法会**静默丢单**（判据 A）。

**设计要求（冻结）**：
- `finalize` 期间 `emit_signal` 的信号**归最后一个交易日**，且必须在**期末强平之前**被应用；
- `Finalize` 的返回值定义为**诊断字节**（JSON，落 run 诊断），**不是信号通道**——避免两套信号来源；
- 若引擎已在最后一日后清算，则 finalize 的信号只允许 `close`/`hold`，出现 `long/short` → trap（否则就是"用最后已知价开新仓"的隐性前视/虚假成交）。

**验收**：finalize 里发 `close` 能把持仓平掉且成交价取最后一日；发 `long` 被 trap 并给出可读原因。

---

### C1 · 不可终止执行（G1）

**事实**：`NewWazeroRuntime` 只设了 `WithMemoryLimitPages`（`wazero_runtime.go:41-43`），**未启用 `WithCloseOnContextDone`**。wazero 文档明确该选项默认关闭，关闭时"任何调用都可能把 Goroutine 甚至整个 OS 线程永久阻塞"。现有 `CallWithTimeout`（默认 30s）**对 wasm 死循环无效**。

**裁决：三层**

| 层 | 设计 |
|---|---|
| 终止能力 | `WithCloseOnContextDone(true)`；超时后返回 `sys.ExitError`，**模块被关闭 ⇒ 实例不可再用** |
| 预算 | **双层**：单次调用预算（`market` 50ms / `symbol` 5ms，可配）+ run 级 deadline（来自请求 ctx，默认 10min）。单次预算 ≠ run 预算，避免"每 bar 合法、整体无限" |
| 终止后语义 | 定义 `ErrSessionTerminated`；会话进入 `terminated`，**禁止继续调用**。恢复路径 = 从最近检查点重建实例并重放（复用 `checkpoint.go` 的日期边界裁决），重建次数上限（如 3 次/run）后 fail-closed |

**必须同时做的基准**：`WithCloseOnContextDone` 会插入周期性检查（wazero 文档注明有性能代价）。**开/关各跑一遍 market scope 727 次 on_bar**，把差值写进 PoC 报告；若代价超过 10%，改为"仅在不可信来源策略上启用"（可信内置策略关闭）——这是一个**必须在 PoC 量化后决定**的分支，不允许拍脑袋。

**验收**：`while(true)` 能在单次预算内被终止且进程存活；终止后重建会话能继续跑完并给出结果或明确失败。

---

### C2 · `symbol` scope 的实例并发与内存预算缺失（G21）— 本轮新发现

**事实**：D25 裁决为混合模型，`scope=symbol` 是"每标的分片实例、每实例 64MB 独立内存"（abi v2 §3）。但设计**没有定义**：
- 同时存活的实例数上限；
- 整个 run 的 wasm 内存总预算；
- 分片调度方式（一次全起 vs 分批）。

**后果**：全市场 5000 标的，若一次性起实例，即便每实例只用初始页（64 KiB），也是 ~300MB 的 wasm 线性内存 + 每实例的 Go 侧结构开销；策略一旦各自涨到几 MB，立刻 GB 级。判据 **B（OOM）**，且是 M0b 的必经路径。

**设计要求**：

1. **分片执行**：实例并发数 `W = min(configured_workers, budget / perInstanceEstimate)`，按 `W` 个一批跑完一批再起下一批；批间只保留**已 flush 的状态**（检查点），不保留实例；
2. **总预算**：单 run wasm 内存上限（建议默认 512 MiB，可配）；超预算 → 降低 `W`，而不是失败；`W` 降到 1 仍超 → fail-closed 并提示改 `scope=market`；
3. **指标**：`wasm_instances_live` / `wasm_bytes_live` / `wasm_batch_size`，便于调参；
4. **与检查点对齐**：分片边界必须落在**日期边界**（复用 `checkpoint.go` 的裁决），否则跨批恢复会丢 bar。

**验收**：5000 标的 symbol scope 跑完不超预算；把预算调到很小仍能跑完（只是变慢），不 OOM；跨批恢复结果与单批一致。

---

### D1 · M0 与 D25 对齐

**裁决**：M0 用**市场级**状态机（`scope=market`，727 次 `on_bar`），M0b 才做 `scope=symbol`。

**加两条验收前置**（否则 M0 的"逐位一致"没有意义）：
1. **先完成 A2（per-run 实例）**——否则两侧都带残留状态，对拍结论不可信；
2. **先完成 A3（参数投递）**——否则 M0 的 regime 阈值无法传入，只能用编译期常量，验收覆盖不到参数路径。

---

## 3. 依赖顺序（不可跳）

> ⚠️ 下方为第一轮顺序；**加入 N1~N9 后的修订顺序见 [`p0-round2-review.md`](p0-round2-review.md) §4**（新增 wave-0 契约冻结排在 C1 之前）。

```
C1(G1)
  → A2(G11) → A10(G17) → A7(G14) → A11(G18) → A1(G7)
  → A3(G10) → A8(G15) → A12(G19)
  → A4/A5/A6/A9（契约冻结）
  → B4(G20) → B1+B3+B5(G8) → B2(G12) → C2(G21)
  → G2/G3 接线 → M0 → M0b
```

排序理由：

- **A2 → A10 → A7 → A11 是一条链**：四者都在处理"共享单例上的 run 级状态"。A11（复权锚点）直接读 A10 的合并缓存，所以 A10 修完 A11 才可能对；顺序颠倒会写两遍。
- **A2 在 A1 之前**：`BeginRun` 要"新建 session + 新建策略实例"，实例语义没定就写生命周期必返工。
- **A8 在 A12 之前**：A12 若选"按规则合并重复信号"，合并结果依赖排序；排序没定，合并就不确定。
- **B4 在 B1 之前**：`symbol_idx` 的来源（`RunUniverse`）必须先于 `BarContext` 的数据布局确定，否则 `Symbols` 字段填进去就是错的。
- **C2 在 M0b 之前**：symbol scope 是 M0b 的形态，预算没定就跑 M0b = 直接 OOM。

### 3.1 一条系统性根因（比单点更重要）

A1 / A2 / A7 / A9 四项其实是**同一个模式**：**run 级可变状态挂在进程级单例上**。

| 位置 | 单例 | run 级可变状态 |
|---|---|---|
| provider | 注入的那个 provider | wasm session / 状态 |
| registry | `DefaultRegistry` 里的策略 | `cursor`、内部递推状态 |
| engine | `Engine`（handler 共享） | `fundamentals`、factor cache、`listing` |
| 检查点 | 表结构 | `State` 字节（无版本） |

处置原则（建议写进护栏）：**"进程级对象只持有不可变配置；一切随 run 变化的都得随 run 创建与销毁。"**
反例（现状）：`e.fundamentals = records`、`e.listing = windows`、`Registry.Get` 返回实例。

## 4. 影响面清单

| 位置 | 改什么 |
|---|---|
| `pkg/strategy/signal_provider.go` | `Scope`/`RunMeta`/`SessionLifecycle`/`NewInstance` 调用 |
| `pkg/strategy/registry.go` | `RegisterFactory` / `NewInstance`；`Get` 降级为元数据 |
| `pkg/strategy/plugins/*.go`（14 个） | `init()` 改注册构造函数 |
| `pkg/backtest/engine.go` | BeginRun/EndRun 钩子、`StrategyParams` 透传 |
| `pkg/backtest/contracts/contracts.go` | `StrategyRef` / `StrategyParams` + `ValidateRunSpec` |
| `pkg/sandbox/wasm/host_api.go` | `BarContext` 重构、`get_bar2`/`get_series_len2`/`get_date2`、校验与上限 |
| `pkg/sandbox/wasm/wazero_runtime.go` | `WithCloseOnContextDone(true)` |
| `pkg/sandbox/wasm/on_bar_session.go` | params 长度校验、终止后状态机、重建语义 |
| `pkg/backtest/engine.go` | **run 级缓存下沉**（`fundamentals`/factor/`listing` 移出 Engine 字段）；信号应用前稳定排序 |
| `pkg/backtest/tracker` + 信号应用 | 排序键与稳定性（A8） |
| `pkg/storage`（strategy_state） | 加 `state_schema_version` / `artifact_hash`（A9） |
| `pkg/backtest/cache/cache.go` | 合并语义改 per-run 或加字节预算 + LRU（A10） |
| `pkg/backtest/adjustment_snapshot.go` | `fullBars` 按 run 区间切片后再算 anchor/digest（A11） |
| `pkg/strategy/signal_provider.go` | `Scope.RunUniverse`（稳定）+ `Universe` 降级为当日名单（B4） |
| `pkg/backtest/engine.go`（信号应用） | 重复信号语义（A12） |
| `pkg/sandbox/wasm/on_bar_session.go` | `finalize` 信号归属 + 返回值定义为诊断（B5） |
| `internal/repoguard/` | **3 条新护栏**：执行路径禁用 `Registry.Get`；`pkg/backtest` 内禁止 HTTP 调用（顺带 BE-10）；**Engine 结构体禁止新增 run 级可变字段**（AST 白名单） |

## 5. 与既有文档的关系

- 本篇是 `adr-029-amendment-host-api-v2.md` 的实施前基线索引：ABI 细节以那篇为准（§2.1/§2.2/§2.3/§7）；
- 工单映射见 `implementation-plan-mvp.md` 的 G 表（G1、G7~G13）；
- 问题登记见 `backtest-engine-review.md`（BE-21/BE-22）。
