# 产品需求落地：现实可行路径（ADR-D17）

> 状态：**执行计划**（不再是可行性讨论）。
> 工程周期定位：② 架构设计 → **③ 模块实现**。
> 起因：用户问「你否决几乎所有的建议，这个产品需求到底实现不了？」

---

## 0. 结论先行

**能实现。不存在实现不了。**

我前面把每条建议按"逐 bar 在线 + 不可信代码 + 5000 标的 × 727 天"这个**最紧约束**去评，得出一堆"否决"——那是**极端形态下的正确性检查**，不是"必须一步到位才允许开工"。实际上：

- **约 70% 的基础设施已经躺在你们仓库里**（见 §2）；
- 剩下的 30% 是**胶水代码 + 一条编译链路**，不是研究课题；
- 唯一真正的不确定性是**性能数字**，而性能有明确的降级杠杆（§4），且杠杆点就在数据模型选择上。

---

## 1. 产品需求（精确化）

**运行时动态加入「AI 生成的复杂状态机策略」，不重启引擎；源头在策略服务；引擎只持缓存副本；安全沙箱；结果可复现。**

把它拆成三档，看清你要哪一档：

| 档 | 内容 | 现状 |
|---|---|---|
| 1 | 人写策略、编译进二进制 | ✅ 现状（14 个内置策略） |
| **2** | **代码（人写或 AI 写）→ 编译 → 资产服务 → 引擎缓存加载执行** | **目标** |
| 3 | AI 全自动闭环（生成 → 编译 → 自动进回测，无人工闸门） | 远期 |

**档 2 是本计划要交付的**。档 3 只是在档 2 上加一个"自动 promote"，不需要新架构。

---

## 2. 已有地基（这部分不用写）

| 组件 | 状态 | 位置 |
|---|---|---|
| WASM 运行时 wazero v1.12.0（纯 Go 无 cgo） | ✅ | `go.mod:18` |
| 逐 bar 协议 `initialize → on_bar(t) → finalize` | ✅ | `pkg/sandbox/wasm/on_bar_session.go:51` |
| 前视隔离（`t_offset>0` → trap） | ✅ | `pkg/sandbox/wasm/host_api.go` `hostGetBar` |
| 能力白名单 `env` 8 函数 + `ValidateImports` | ✅ | `pkg/sandbox/wasm/host_api.go` |
| 内存上限 64MB（runtime 级） | ✅ | `pkg/sandbox/wasm/sandbox.go` `DefaultMaxMemory` |
| 超时调用框架（30s） | ✅ | `pkg/sandbox/wasm/sandbox.go:258` `CallWithTimeout` |
| 状态持久化 + 断点续跑（**日期边界裁决**） | ✅ | `pkg/strategy/checkpoint.go` + `pkg/strategy/state_store.go`（`StateStore`；另 `pkg/backtest/state/state_store.go`） |
| 双引擎路由 fail-closed | ✅ | `pkg/backtest/contracts` `MarketRouter` |
| 版本哈希治理样板 | ✅ | `factor_definitions.version_hash`（`pkg/domain/factor_def.go:151`） |
| L3b 外部信号全链路（按 `as_of` 消费） | ✅ | `pkg/strategy/signal_strategy.go` |
| 插件 HTTP 加载端点（路径逃逸防护） | ✅ | `cmd/analysis/handlers_plugin.go`（REV-08） |

> （refactor-plan-review L7：位置列已补仓内相对全路径——行号会随 W0 后改动漂移，认路径不认行号。）

**换句话说：最难的部分（沙箱、协议、防前视、状态恢复）已经做完了。**

---

## 3. 缺口清单（要写的就这些）

| # | 缺口 | 工作量 | 说明 |
|---|---|---|---|
| G1 | `WithCloseOnContextDone(true)` | **几小时** | P0，一行配置 + 恶意死循环测试（见 §5） |
| G2 | `SignalProvider` 的 WASM 实现 | 1-3 天 | 把 `OnBarSession` 接进引擎现有信号位 |
| G3 | `EmittedSignal → domain.Signal` 转换 | 半天 | 字段映射 + 校验 |
| G4 | 编译工具链（TinyGo） | 1 小时 | 装 Windows 二进制，或在 Docker 里编 |
| G5 | 策略服务（表 + 2 个端点 + 引擎缓存） | 1-2 周 | 复用 ADR-D13/D14 设计 |
| G6 | AI 生成闭环（Go 子集 + 静态校验 + promote） | 2-3 周 | 档 2 的完整形态 |

---

## 4. 性能的现实解法（关键杠杆在这里）

**不要先假设不行。** 先测单次 `on_bar` 的 wall time（wazero 在 amd64 有编译后端，纯计算策略大概率是 μs 级——**需实测，不给编造数字**）。

**真正的杠杆是数据模型选择，它决定调用次数：**

| 数据模型 | `on_bar` 调用次数（5000 标的 × 727 天） | 适用 |
|---|---|---|
| per-symbol 实例（现有 `get_bar` 无 symbol 参数） | **约 363 万次** | 单标的时序状态机 |
| **单实例 + 横截面**（给 `get_bar` 加 symbol 维度） | **727 次**（减少 5000 倍） | 市场级/横截面状态机 |

⇒ **状态机如果是市场级（regime 状态机、全市场轮动状态机），选单实例模型，调用次数直接降到千级，性能问题消失。** 如果是单标的（个股持仓状态机），才需要面对 363 万次调用，此时先实测再定。

**明确禁止的降级**：批量把全序列喂进 wasm（即回到旧协议 `generate_signals(bars)`）——那会**倒退回前视漏洞**（沙箱内读 `bars[len-1]` 拿到未来）。这条路永远不能走。

---

## 5. 安全的现实解法（不需要 Gas）

| 层 | 手段 | 状态 |
|---|---|---|
| 能力隔离 | env-only 白名单（比"WASI 留空"更严） | ✅ |
| **可中断执行** | `WithCloseOnContextDone(true)` | 🔴 **G1，必补** |
| 资源上限 | `WithMemoryLimitPages` 64MB + 每次调用超时 | ✅ |
| **人工闸门** | AI 生成的代码必须 `promote` 过人审才进生产回测 | 需要（G6） |

**不需要 Gas**（wazero v1.12 无此 API，已核实）。剩下的"策略逻辑写错"不是安全问题，是**质量问题**，靠"与内置 Go 同逻辑策略对拍"来抓——这是最有效的一层，而且你们已经有 dualrun K15 的机制。

---

## 6. 三个里程碑（每步都有验收）

### M0：证明"状态机能跑"（2-3 天，不建任何服务）

1. 装 TinyGo（Windows 二进制 / 或 Docker）；
2. **手写一个市场级状态机**（regime 开关：牛/熊/震荡 + 冷却 + 总仓位闸门，`scope=market`，727 次 `on_bar`），Go 子集 → `tinygo build -target=wasm-unknown` → env-only `on_bar/2` `.wasm`；
3. 补 G1（`WithCloseOnContextDone`）+ G2/G3（接线）；
4. 用 `OnBarSession` 跑通一次真实回测。

**验收**：同一个状态机的 Go 内置实现 vs WASM 实现，回测结果**逐位一致**。
这一步不碰服务、不碰 AI，纯粹证明载体可用。

### M0b：符号级状态机（M0 之后，验证 `scope=symbol` 分片）

用同一载体写「持仓状态机 + 冷却期 + 分层加仓」（每标的一份状态），验证分片实例的实例化成本与状态隔离；验收同样是 Go 内置 vs WASM 逐位一致。**M0 先做市场级**是 D25 的裁决，不要颠倒顺序。

### M1：真相源 + 缓存（1-2 周）

1. `strategies` 加列 + `strategy_artifacts`（BYTEA，本地栈够用）；
2. 引擎侧缓存（内存 map + LRU，键 = `artifact_hash`）；
3. 两个 HTTP 端点：`GET /strategies/{id}/resolve` + `GET .../artifact`；
4. `strategy_ref{id@hash}` 进 `BacktestRequest`，结果记录 hash。

**验收**：策略服务停机后，已缓存 hash 的回测照样跑，且结果一致（ADR-D13 离线降级）。

### M2：AI 闭环（2-3 周）

1. AI 生成 Go 子集 → 编译服务（TinyGo + 静态校验：import 白名单 / 体积 / 内存上限）→ artifact 登记；
2. `promote` 人工闸门；
3. 状态机三路一致性（Batch / Step 从零 / Step 从断点）+ dualrun K15。

**验收**：一条 AI 生成的策略，从 prompt 到回测结果**全程留痕**（源码 → hash → 结果）。

---

## 7. 今天就能做的第一件事

**G1：补 `WithCloseOnContextDone(true)`。**

前提：Go 1.25、wazero v1.12.0、`pkg/sandbox/wasm` 现有代码。

```go
// pkg/sandbox/wasm/wazero_runtime.go:41 附近
rtCfg := wazero.NewRuntimeConfig().
    WithMemoryLimitPages(uint32(pages)).
    WithCloseOnContextDone(true)   // 新增：让 ctx 超时能真正中断 wasm 执行
```

⚠️ **代价**：wazero 会插入周期检查，有一定性能开销（config.go:162-163 原文）。**必须同时做基准测试**，量化开/关的差异，决定是否按策略分级启用。

⚠️ **连带影响**：超时后 `sys.ExitError` 会被抛出且模块被关闭 ⇒ 会话需要重建语义（与状态机恢复路径打通，见 ADR-D16 §3）。

配套测试（必须写）：构造一个 `while(true)` 的 wasm，验证启用后能在超时内被终止、进程不被挂死。

---

## 8. 风险与兜底

| 风险 | 兜底 |
|---|---|
| 性能不达标（per-symbol 363 万次调用） | 优先用单实例+横截面模型；或先在小票池/单标的上跑 |
| 三路一致性难保证 | 直接复用 `checkpoint.go` 已有语义与测试 |
| TinyGo 不支持某些 Go 特性 | 策略代码用受限子集（禁 goroutine / net / reflect），写进编译服务静态校验 |
| AI 生成代码质量差 | `promote` 人工闸门 + 与内置实现对拍 |

---

## 9. 明确的边界（哪些真的不做）

- 不上微服务、不上 MQ、不上 K8s（团队 1-2 人 + 单机栈，参考库微服务条件一条不满足）；
- 不放行 WASI（确定性风险）；
- 不做 QuickJS/JS 载体（双重解释损耗 + 非确定源）；
- 不做"批量喂全序列"（前视漏洞）。
