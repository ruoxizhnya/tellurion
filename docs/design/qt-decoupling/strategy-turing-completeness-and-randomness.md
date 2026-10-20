# 载体表达力边界：图灵完备性与随机性（ADR-D15 草案）

> 状态：草案，未实现。本文件**修正** `strategy-service-landing-plan.md` §4 中"腿 1 = 表达式 DSL 覆盖 80%"的乐观判断。
> 触发：用户指出「表达式并不图灵完备，复杂/随机函数类策略不支持」。**该质疑成立**。
> 证据：2026-10-19 源码实测。

---

## 1. 事实：表达式 DSL 的确不图灵完备

`pkg/expression/registry.go` 内置算子全集（实测）：

| 类别 | 算子 |
|---|---|
| 数学 | `abs` `sqrt` `log` `exp` `sign` `neg` |
| 行情字段 | `open` `high` `low` `close` `volume` `turnover` `turnover_rate` `turnover_rate_f` `volume_ratio` |
| 基本面 | `pe` `pb` `ps` `roe` `roa` `revenue` `profit` `sector` |
| 时序（有状态递推） | `ts_mean` `ts_std` `ts_sum` `ts_max` `ts_min` `ts_delay` `ts_delta` `ts_pct_change` `ts_rank` `ts_ewma` `ts_rma` `ts_kalman` `ts_corr` |
| 横截面 | `cs_rank` `cs_zscore` `cs_percentile` `cs_neutralize` |

**没有 `if`、没有循环、没有递归、没有函数定义、没有随机算子。**

准确表述：它是**算子组合语言**（能表达"有状态递推"，因为有 `ts_ewma` / `ts_rma` / `ts_kalman`），但**不图灵完备**——无法表达任意控制流与过程式逻辑。

**修正此前判断**：若策略谱系包含"复杂过程式逻辑 + 随机函数"，DSL **不能当主腿**，只能退回它本来的位置：管理面与简单规则型策略。

---

## 2. 关键事实：种子化随机**已经存在**（不是禁区，但条件很硬）

`pkg/backtest/engine.go:171-304`（实测）：

```go
// rng is the per-engine *rand.Rand, initialized from config.Seed ...
rng *rand.Rand
...
if config.Seed != 0 {
    rng = rand.New(rand.NewSource(config.Seed))        // 确定性
} else {
    rng = rand.New(rand.NewSource(time.Now().UnixNano())) // 非确定性（生产）
}
```

注释还记录了修复过的 bug：早期 `rand.New(rand.NewSource(seed))` 的返回值被丢弃、实际用了全局状态——说明**"确定性随机"是该项目明确在意并专门治理过的性质**。

所以"随机"要分三类，落点完全不同：

| 需求 | 判定 | 落点 |
|---|---|---|
| **种子化伪随机**（随机化规则、随机权重、蒙特卡洛决策） | ✅ **允许，且基础设施已有** | 需把它暴露成策略可见的能力（见 §3） |
| **外层随机探索**（搜参、蒙特卡洛鲁棒性、bootstrap） | ✅ 允许，且是**正确位置** | 多次回测 × 不同 seed（对应 BE-06 缺口），**不该塞进策略内** |
| **未种子化的真随机**（`time`、WASI `random_get`、裸 `math/rand` 全局源） | ❌ **禁区** | 毁掉可复现与 K15 双跑对齐——这是"不放行 WASI"的核心理由之一 |

---

## 3. 图灵完备需求的三条路径（按落地成本排序）

| 路径 | 图灵完备 | 沙箱 | 在线逐 bar | 生态/库 | 现状 | 代价 |
|---|---|---|---|---|---|---|
| **L3b 外部信号** | ✅ | 进程外隔离 | ❌ 离线批量（按 `as_of` 消费） | ✅ 任意语言/库（Python + numpy / sklearn 等） | ✅ **已实现**（`signal_strategy.go` + `quant.external_signals`） | 不逐 bar 交互；状态与随机自己管（必须种子化） |
| **WASM（L3a）** | ✅ | ✅ 完整 | ✅ | ⚠️ 仅 host API 提供 | ⚠️ 运行时已实现、**未接线**、无工具链 | 需补 `get_rand`；per-symbol/横截面未定案；性能待实测 |
| **内置 Go / `.so`** | ✅ | ❌ 无 | ✅ | ✅ 整个 Go 生态 | ✅ 在用（14 个内置） | Windows 不可用；无沙箱（AI/第三方代码禁用） |

### 3.1 被低估的一条：L3b 其实最匹配"复杂 + 随机"

`pkg/strategy/signal_strategy.go` 的链路**已经完整实现**：外部模型写 `quant.external_signals`（`content_hash` 幂等坐标）→ 内核 `SignalStrategy` 在 `OnBar` 里按 `as_of` 拉取 → cursor 去重 → `rejectFutureSignals` 防前视 → `SaveState/LoadState` 断点续跑。注释原文："**自由度挡在内核外，内核内仍确定、可审阅**"。

对你说的"复杂随机函数类策略"（遗传规划生成的公式、随机森林、蒙特卡洛决策、带随机扰动的规则），L3b 的匹配度其实**高于 WASM**：

- 可以用 Python 全生态（numpy / sklearn / scipy）；
- 随机与状态都在你自己的进程里，**只要种子化，回测照样可复现**；
- 今天就能用，不需要装工具链、不需要 PoC。

代价要说清：**它是"信号"级外置，不是"策略"级在线**——外部模型不直接下单，产出信号注入内核，由内核策略消费。这对"策略本身要逐 bar 做复杂状态机"的场景不适用，那种只能走 WASM。

---

## 4. WASM 侧的具体补丁：给 host API 加确定性随机

如果走 WASM 路线，"随机"必须按下面四条设计，否则会毁掉可复现性：

1. **新增 host 函数**（扩到 9 个）：`get_rand()` / `get_rand_f64()`，由宿主用**策略实例独立的 seed 化 PRNG** 提供（不要直接复用 `engine.rng`，避免策略之间互相影响序列）。
2. **随机状态必须持久化**：PRNG 的内部状态要进 `get_state` / `set_state`，否则**断点续跑结果不一致**（ADR-028 §7 三路一致性会破）。
3. **禁止 `random_get`（WASI）**：这是"坚持 env-only 白名单"的第二个理由（第一个是 `fd_write`）。
4. **seed 必须留痕**：seed 进 `strategy_ref` / RunSpec，并写进回测结果元数据——否则"同参回测"无法复现，K15 双跑对齐失去前提（与 BE-20 同源）。

---

## 5. 修正后的落地路线（替换原 §4 与 §6）

| 阶段 | 内容 | 说明 |
|---|---|---|
| **S0′** | **L3b 外部信号作为"复杂/随机策略"的主通道**（已实现，直接用） | 复杂与随机需求不再等 WASM |
| **S0″** | DSL 资产化 + 真相源/缓存/`strategy_ref` | 退回管理面与简单规则；**机制验证仍走它**（成本最低） |
| **S1** | WASM PoC（补 `get_rand` 设计 + 数据模型定案 + 性能实测） | 需要"在线逐 bar + 沙箱"时才推进 |
| **S2** | 编译服务 + artifact + 接线 | 依赖 S1 通过 |
| **S3** | 内置策略登记、统一入口 | — |

**判据一句话**：**能用 L3b 离线的，不要上 WASM 在线；能种子化的随机，才允许存在。**

---

## 6. 需要你确认的一点

你说的"复杂随机函数类"，是**哪一种**（决定主通道）：

| 场景 | 主通道 |
|---|---|
| 遗传规划/公式挖掘产出的复杂公式，但要**逐 bar 在线**算 | WASM（DSL 装不下） |
| 随机森林 / ML 模型 / 蒙特卡洛，**可离线批量**算完喂信号 | **L3b**（今天就能用） |
| 策略内部要**带随机扰动的规则**（如随机化入场时点） | WASM + `get_rand`，或 DSL 加种子化随机算子 |
