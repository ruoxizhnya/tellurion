# P0 第二轮深度审查：设计闭环核对 + 新增 P0（N1~N9）

> 状态：**审查结论 + 设计补充基线**（2026-10-20）；N2/N9/A12 三项已于 2026-10-20 裁决（见 §8）。未实施。
> 范围：① 对**历次发现的全部 P0** 逐条核对"设计是否真的接住了"；② 在此之上做**深度审查**，找被漏掉的 P0。
> 方法：只认源码事实与已落档文本；每条新发现都给"判据 / 证据 / 为什么现在处理 / 冻结语义 / 验收"。
> 关联：`p0-remediation-design.md`（20 项基线）、`backtest-engine-review.md`（BE-01~30）、`adr-029-amendment-host-api-v2.md`（ABI v2）

---

## 0. 结论先行

**一、覆盖度：20 项旧 P0 全部有设计，其中 6 项"名字覆盖、语义未闭环"。**

不闭环的 6 项不是"漏了"，是"写了一句话但没法定成契约/测不出来"：A3、A4、A6、A9、A12、B4。它们的缺口已在 §2 逐条列出并给出补齐文本。

**二、新增 9 项 P0（N1~N9），按"什么时候会炸"分三档：**

| 档 | 编号 | 问题 | 判据 |
|---|---|---|---|
| **今天就在**（不接线也错） | N1 | **策略执行错误被降级为"跳过当天"**，trap 不终止 run，结果照出 | A |
| | N2 | **执行时点/成交价语义从未冻结**（同 bar 收盘决策 + 同 bar 收盘成交） | A + C |
| | N6 | **可复现留痕不闭合**：`run_spec_hash` 只被引用从未定义；三 hash 覆盖面不足；`dataset_snapshot_hash` 两处定义互相矛盾 | C |
| | N7 | **`version_hash` ↔ `artifact_hash` 双哈希解析漏洞**：同源码重编 = 同 ref 不同字节 | C |
| **契约冻结前必须定**（一旦 artifact 出厂就改不动） | N5 | **host 固定内存偏移没有"模块预留"契约**，`WriteMemory(0, params)` 写的是谁的地址没人保证 | A |
| | N9 | **ABI 表达不了目标仓位/数量**，而 Strength 已被 D26 明确排除权重语义 ⇒ M0b"分层加仓"验收不可实现 | A（功能不可达） |
| **M0b / 并发上线前** | N3 | **检查点状态身份维度不足**：PK 是 `(strategy_id, run_id)`，symbol scope 5000 分片互相覆盖；且不校验 universe | A |
| | N4 | **终止重建的"重放"会重复应用信号** ⇒ 重复下单 | A |
| | N8 | **run 级资源准入缺失**：并发 run × per-run 缓存无总预算/队列（A10 修完反而更危险） | B |

> 一句话：**旧 P0 处理了"共享单例上的 run 级状态"，但没有处理"错误分级""时间语义""状态身份""内存所有权"这四个同样被判据 A/C 命中的面。**

---

## 1. 旧 P0 应对核对表

### 1.1 p0-remediation-design 的 20 项

| # | 设计落点 | 状态 | 缺口（→ 见 §2 / §3） |
|---|---|---|---|
| A1 Provider/session 生命周期 | §2 A1：RunMeta + SessionLifecycle + 装配期 fail-closed | ✅ 闭环 | 但"EndRun 必须 finalize"在终止路径不成立 → N4；会话上限没约束 run 准入 → N8 |
| A2 策略 per-run 实例 | §2 A2：注册工厂 + `NewInstance` + repoguard | ✅ 闭环 | — |
| A3 参数投递 | §2 A3：两层参数 + 未知名拒绝 + `params_json` | ⚠️ 半闭环 | `run_spec_hash` 未定义，第 5 条落不了地 → N6 |
| A4 `version_hash` 覆盖 | §2 A4：canonical 七项 + 向量测试 | ⚠️ 半闭环 | 不含 toolchain/build 参数，与 `artifact_hash` 的可解析性冲突 → N7 |
| A5 Action/Strength 冻结 | §2 A5：0..3 枚举 + [0,1] + 越界 trap + 每 bar 上限 | ✅ 闭环（语义） | trap 的上游被吞（N1）；且无数量表达 → N9 |
| A6 params/state 内存布局 | §2 A6：区间/上限/scratch 失效语义 | ⚠️ 半闭环 | **没定义"这段内存归谁"**：模块是否预留 [0,4096) 无契约 → N5 |
| A7 共享 Engine 缓存污染 | §2 A7：缓存下沉 runContext（推荐 A） | ✅ 闭环 | 代价（跨 run 复用丢失、per-run 预热成本）未记账；并发下内存更紧 → N8 |
| A8 信号顺序 | §2 A8：稳定排序四元键 + 写进 SignalSpec | ✅ 闭环 | — |
| A9 检查点状态版本 | §2 A9：信封三字段 + 恢复校验 | ⚠️ 半闭环 | 身份维度只有"版本"，没有 **shard** 与 **universe** → N3 |
| A10 L1 缓存永不清理 | §2 A10：下沉 per-run 或字节预算+LRU | ✅ 闭环（方向） | 只有单 run 预算，无全局预算 → N8 |
| A11 复权锚点 digest | §2 A11：先切 `[Start,End]` 再算 | ✅ 闭环 | digest 未进任何"结果留痕"集合 → N6 |
| A12 同 bar 多信号 | §2 A12：trap（推荐）或合并规则 | ⚠️ 待裁决 | 未做**存量 14 策略重复信号扫描**；trap 可能直接让现有策略全红（判据 B） |
| B1 Host 数据模型 | §2 B1：Axis+Bars+Symbols，T 语义表，field 枚举 | ✅ 闭环 | `t<0` 越界语义未写；`Bars` 引用的是"截至今日的整段历史"，与"零拷贝"一致但每日重建成本未评估 |
| B2 `get_date2` | §2 B2 | ✅ 闭环 | — |
| B3 v1/v2 共存 | §2 B3：增量函数名 + import section 自证 | ✅ 闭环 | — |
| B4 `Scope.Universe` 逐日变 | §2 B4：新增 `RunUniverse` | ⚠️ 半闭环 | "StockPool ∪ 曾持有"是**循环定义**（持仓来自 run 自身）→ §2.6 |
| B5 `finalize` 信号归属 | §2 B5：归最后一日、清算前应用、禁止开新仓 | ✅ 闭环 | 终止路径下 finalize 不可能执行（模块已 Close）→ N4 附注 |
| C1 不可终止执行 | §2 C1：三层（关闭/双层预算/重建上限） | ✅ 闭环（能力） | 重建的"重放"语义未定义 → N4 |
| C2 symbol scope 预算 | §2 C2：分片 + 信号量 + 512MiB/run | ✅ 闭环 | 分片状态落库会互相覆盖 → N3；只约束 wasm，不约束数据缓存 → N8 |
| D1 M0 市场级 | §2 D1：M0 market / M0b symbol + 两条前置 | ✅ 闭环 | — |

**统计：16 项闭环 / 1 项待裁决（A12）/ 4 项半闭环（A3、A4、A6、A9、B4 —— 计 5 项，其中 A3 与 N6 同源）。**

### 1.2 backtest-engine-review 的 🔴 项

| BE | 落点 | 状态 |
|---|---|---|
| BE-05 / BE-06（基准、归因、敏感性、蒙特卡洛缺） | D21 `analytics-service-design.md` | ✅ 有设计（纯函数 + seed 留痕） |
| BE-18（`.so` Windows 不可用） | D12 + ADR-024「不做 plugin.Open」 | ✅ 口径已统一为存量遗留 |
| BE-21 / BE-22 / BE-23 / BE-24 / BE-25 / BE-26 / BE-27 / BE-28 / BE-29 | A2 / A3 / A7 / A8 / A9 / A10 / A11 / A12 / B4 | ✅ 一一对应（缺口同上表） |
| BE-30（symbol scope 预算） | C2 | ✅ |

### 1.3 非 P0 但仍未闭环（记账，不阻塞）

| BE | 问题 | 现状 |
|---|---|---|
| BE-02 | 回测复用 `pkg/live` 撮合，改实盘会改回测口径 | 只有"待切 port"的意向，无设计。**建议升为 P1 首项**（它直接决定"回测结论能不能代表实盘"） |
| BE-07/08/09 | auction / margin / options 未接线 | 无设计，属功能缺口 |
| BE-10 | `Engine.httpClient` 用途未查清 | p0 §4 只加了"禁止内循环 HTTP"护栏，未处理该字段 |
| BE-11 | 停牌/成交量/复权/分红/涨跌停的真实完整度 | 仍为"待查"，是**事实缺口**，不是设计缺口；BE-11 没查清前，"逐位一致"的基准本身不可信 |

---

## 2. 六项"半闭环"的补齐文本（直接可冻）

### 2.1 A3 → 见 N6（`run_spec_hash` 必须定义）

A3 第 5 条写"运行记录必须存 `params_json` + `params_hash`"，D26 写"覆写进 `run_spec_hash`"——但**全设计集 grep `run_spec_hash` 只有这两处引用，没有任何一处定义它算哪些字段**。参数只是 run 输入的一小部分，只哈希参数等于没哈希。补齐文本见 **N6**。

### 2.2 A4 / N7：`version_hash` 的边界必须写清

A4 冻结的 canonical 集合是 `strategy_id + carrier + abi_version + scope + entry + source + params_defaults`。这组字段**不包含 toolchain 与 build flags**，而 `strategy_artifacts.toolchain` 是单独一列（`strategy-asset-service-api.md` §1.2）。后果见 **N7**。

### 2.3 A6 / N5：内存布局只定了"多大"，没定"归谁"

A6 把 `[0,4096)` 定为 params、`[4096, 4096+64KiB)` 定为 scratch，并给了上限与失效语义——**这些都对，但整段设计里没有一句话回答：这两段地址在 wasm 线性内存里由谁预留、host 凭什么能写。**

事实：`OnBarSession.Initialize` 直接 `WriteMemory(0, params)`（`on_bar_session.go:67`），`hostGetState` 直接 `Write(4096, val)`（`host_api.go:231`）。线性内存低地址是模块自己的数据段/栈区（TinyGo 默认布局把 data、heap、stack 都放在线性内存里），**host 往 0 写 = 往策略自己的变量上写**。现在能跑通，只说明"当前 TinyGo 版本的默认布局恰好没用到这一小段"——这是**未声明的巧合**，不是契约。详见 **N5**。

### 2.4 A9 / N3：信封有"版本"，没有"身份"

A9 的信封是 `{state_schema_version, artifact_hash, abi_version, payload}`。这三个字段回答的是"这份字节该用哪种结构解释"。它**没有回答"这份字节属于哪个实例、哪个票池"**：

- `quant.strategy_state` 的 PK 是 `(strategy_id, run_id)` 且 **UPSERT 只留最新一份**（`pkg/strategy/state_store.go:60-69`，注释明写"只保留最新一份"）。symbol scope 是 N 个分片实例，**N 份状态写同一行，后写覆盖先写**；
- 状态里的 `symbol_idx` 语义由 `RunUniverse` 决定。同一个 artifact hash、同一个 `state_schema_version`，换一个票池（指数成分更新、用户改 pool）恢复旧检查点 ⇒ **索引映射到别的标的**，策略按 idx 存的持仓状态会落到错的股票上。

详见 **N3**。

### 2.5 A12：trap 之前先做存量扫描（否则是新引入的"不可用"）

A12 推荐"重复即 trap"。判据没错，但**没有验证存量 14 个内置策略是否已经在同一 bar 对同一 symbol 发多条信号**。若存在（组合调仓、先平后开是常见写法），开关一上，这些策略的回测**直接全失败**——按 p0 自己的判据，这属于 B（不可用），而且是从"静默错"换成"全挂"，不是净改进。

**补齐要求（实施前必做，半天）**：
1. 跑一遍全量存量策略 × 3 个基准区间，统计 `(date, symbol)` 重复度；
2. 若重复率 > 0：先按 §2.5 的冻结规则**合并**（`close` 优先、同向求和 clamp），保留 warning 计数，把 trap 作为下一版的开关；
3. 若重复率 = 0：直接 trap，并在 SignalSpec 写明"这是契约，不是配置"。

### 2.6 B4：`RunUniverse` 的"∪ 曾持有"是循环定义

B4 写 `RunUniverse = StockPool ∪ 曾持有`。但"曾持有"是**这次 run 跑出来的结果**，run 开始时不知道；而 `symbol_idx` 必须在**第一根 bar 之前**定死。这是一个真实的定义漏洞（不是措辞问题）：

**冻结文本**：
```
RunUniverse := sort( dedup(
     P0  ∪  P1  ∪  P2  )
)
P0 = 期初持仓（resume 时来自检查点；新建 run 为空）
P1 = StockPool 直接给定时：该列表（不做时点过滤）
P2 = IndexCode 给定时：区间 [Start,End] 内**成分股历史并集**（含已调出）
```
即：**票池在 run 前一次性解析成"区间并集"，run 期间只增不减**；当日可交易性只由 `Universe`（当日名单）+ NaN/mask 表达。`曾持有` 只能来自 P0/P2，**不得**由运行中的成交反向扩张（否则索引集合中途变化 = B4 想防的事又回来了）。

推论：`RunUniverse` 必须在 `dataset_snapshot_hash` 之前算完，并且**进 hash**（否则同 hash 不同票池）。

---

## 3. 新增 P0（N1~N9）

### N1 · 策略执行错误被降级为"跳过当天"（判据 A，今天就存在）

**事实（已核实）**

```
engine.go:918-926
signals, err := e.getSignals(...)
if err != nil {
    // 不进 invalid_reasons：单日策略故障不否定整段回测的有效性
    execAlgoDiag.recordSignalFailure(date, err)
    logger.Warn()...Msg("Failed to get signals, skipping day")
}
```

`getSignals` 的错误面包含**三类完全不同性质**的失败：

| 错误来源 | 性质 | 现在的处置 | 应该的处置 |
|---|---|---|---|
| provider 取数失败 / 某标的缺 bar | 数据缺口 | 跳过当天 | 跳过 + 进 `invalid_reasons`（可接受） |
| `RejectFutureSignals` 拒绝（前视信号） | **契约违规** | **跳过当天** | 终止 run |
| wasm trap（死循环超时 / 非法 action / strength 越界 / symbol_idx 越界 / 重复信号 / state 超预算） | **策略故障** | **跳过当天** | 终止 run |

**后果**：一个"每隔几天 trap 一次"的 AI 策略会产出一条**漂亮且看似有效**的净值曲线；`invalid_reasons` 里没有任何痕迹（代码注释明说"不进 invalid_reasons"）。这是本设计集全部 fail-closed 承诺（A5/A12/B1/C1）的**总漏斗**——所有 trap 都在这里被吃掉。

**设计裁决（冻结）**

1. 错误必须分级，且分级在**类型层**而非字符串层：

```go
// contracts，哨兵错误
var (
    ErrDataGap        = ...  // 可跳过：数据缺失/停牌/供应商抖动
    ErrStrategyFault  = ...  // 必须终止：trap、非法信号、预算超限、artifact 校验失败
    ErrContractBreach = ...  // 必须终止：前视信号、unknown param、abi 不符
)
```

2. `getSignals` 的错误按上表映射后**向上抛**，由 `RunBacktest` 决定：
   - `ErrDataGap` → 当日不成交 + `invalid_reasons += "data_gap:date:symbol"`（现状行为，但必须留痕）；
   - `ErrStrategyFault` / `ErrContractBreach` → **立刻 `EndRun`，run status = `failed`，不产出可用于决策的 `BacktestResult`**。
3. `ResultSpec` 增加 `Status ∈ {ok, degraded, failed}` 与 `Failures []Failure`。下游（分析服务、编排、promote 闸门）**只接受 `ok`**；`degraded` 必须显式带原因列表。
   > ⚠️ **2026-10-20 订正（refactor-plan-review C1）**：`{ok, degraded, failed}` 与冻结三值 `status`（`docs/SPEC.md`：`completed / invalid / failed`）冲突，**不得按本条引入第四值**。实施口径：`degraded`（数据缺口跳天）→ **`invalid`** + 稳定 `invalid_reasons` code `data_gap_skipped`；`ErrStrategyFault`/`ErrContractBreach` → **`failed`** + `error`；`Failures[]` 只作**诊断明细**，不作状态来源；status 仍由 `buildResponseFromState` 单一透传点产出。下游判据同步改为「**只接受 `completed`**」。详见 `tellurion:docs/TASKS.md` QT-10 ③-④ 映射规则。
4. 护栏：`pkg/backtest` 内对 `getSignals` 的返回值**禁止"仅 log 继续"**（写一条单测/repoguard：`getSignals` 的 err 必须在同一函数内被 `return` 或映射为 fatal）。

**验收**：构造一个"每 50 天 trap 一次"的 wasm → run 必须 `failed`，且 `Failures` 点名日期与原因；构造一个"某天数据缺失"的 provider → run ~~`degraded`~~ **`invalid`**（+ 稳定 `invalid_reasons` code `data_gap_skipped`，见上 ⚠️ 订正），`invalid_reasons` 有记录，成交笔数符合预期。

**工单**：G22（P0，与 A1 同批——`EndRun` 的语义本来就依赖"错误路径也要走到 EndRun"）。

---

### N2 · 执行时点 / 成交价语义从未冻结（判据 A + C）｜**已裁决：`next_open` 为终态，先出差异报告**

**事实（已核实）**

| 事实 | 证据 |
|---|---|
| 策略拿到的当日切片**包含当日收盘价** | `processStockJob` → `getOHLCV(sym, StartDate, date)`，末根 = 当日 bar（`engine_daily.go:181-205`） |
| 成交价 = **当日最后一根 bar 的 close** | `res.price = ohlcvData[len-1].Close`（`:263-270`）→ `pricesCache` → `price := pricesCache[signal.Symbol]`（`:571`）；`DayBar` 也是当日 bar（`:336-350`） |
| 全设计集**没有一处**定义"信号在 T 日产生，成交在哪个时点" | `SignalSpec` 只定义了信号形状；B1 只冻结了"数据可见性"（t_offset ≤ 0），没冻结"成交时点" |

**后果（两条）**

1. **隐性前视**：用 T 日**收盘后**才知道的 close 做决策，又按**同一个 close** 成交。A 股收盘集合竞价（14:57-15:00）确实存在，所以"尾盘决策 + 收盘成交"可以作为**有意假设**成立——但它必须被声明，而不是从代码里反推。当前状态下，任何人都无法回答"这条曲线含不含 1 bar 前视"。
2. **双内核无法对齐**：ADR-031 的 K15 要求 Go 与 NT 逐位一致。NT 的默认撮合是"下一根 bar 开盘"，Go 是"同一根收盘"。两边都"对"，但差一个 bar 的收益；谁也不知道该改谁。**这使 K15 的失败无法归因**（分不清是策略差异还是撮合口径差异）。

**设计裁决（冻结）**

1. `SignalSpec` 增加执行时点字段，**必填、无默认**：

```
FillTiming = next_open | same_close
```
   - `next_open`（推荐终态）：T 日收盘决策，T+1 开盘成交（含滑点）；T+1 停牌/涨跌停 → 顺延或拒单（与 REV-09 一致）；
   - `same_close`（现状）：T 日收盘决策 + T 日收盘成交，语义必须是"收盘集合竞价"，并在 ResultSpec 里标注 `known_bias: same_close_fill`。
2. `FillTiming` 进 `run_spec_hash`（见 N6）。**切换默认值必须走"差异报告"**：同一批策略在两种口径下各跑一遍，把差异写进报告后再定默认——不允许为了"结果好看/不变"而选口径。
3. NT 侧必须按同一 `FillTiming` 配置实现；K15 对拍前先对 `FillTiming` 做一致性断言（不匹配直接拒绝比较，而不是"跑出来数不一样"）。
4. `get_bar2` 的可见性与 `FillTiming` 必须自洽：`same_close` 下允许读当日 close；`next_open` 下同样允许（决策需要），但成交价必须取 T+1 的 open。**两者不冲突，但文档必须写在一起**，否则实现者会以为"读了 close 就必须用 close 成交"。

**验收**：同一策略 `next_open` vs `same_close` 两跑，差异报告可解释（差异应约等于"每日收益平移一个 bar"）；K15 在 `FillTiming` 不一致时必须报"口径不符"而不是"结果不一致"。

**工单**：G23（P0，契约冻结；实施可分期——先定字段与差异报告，再切默认）。

---

### N3 · 检查点状态的身份维度不足（判据 A）

**事实（已核实）**

```sql
-- pkg/strategy/state_store.go:60-69
CREATE TABLE quant.strategy_state (
  strategy_id TEXT, run_id TEXT, ts TIMESTAMPTZ, state BYTEA, ...,
  PRIMARY KEY (strategy_id, run_id)      -- UPSERT，只保留最新一份
)
```

| 场景 | 后果 |
|---|---|
| `scope=symbol`，5000 分片各自 flush | 5000 份状态写**同一行**，后写覆盖先写；恢复时只有一个分片拿到状态，其余从零 ⇒ 结果与不恢复不同（判据 A/C） |
| symbol scope 分批执行（C2 的 `W` 个一批） | 各批 `ts` 不同，单行 UPSERT 只留最后一批 ⇒ 前面批次的状态永久丢失 |
| 同一 artifact + 同一 schema，**换了票池** | `symbol_idx` 映射到别的标的，策略按 idx 存的状态落到错股票（判据 A，且极难定位） |
| resume 时 `RunUniverse` 与首次 run 不同（指数成分更新） | 同上 |

A9 的信封只校验"结构版本"，**校验不了"这份字节属于谁"**。

**设计裁决（冻结）**

1. 主键扩为 `(strategy_id, run_id, shard_key)`：
   - `scope=market` → `shard_key = ''`；
   - `scope=symbol` → `shard_key = symbol`（与 `RunUniverse` 同一符号口径）。
2. 信封字段扩为四元组：`{state_schema_version, artifact_hash, abi_version, run_identity_hash}`，其中

```
run_identity_hash = sha256(canonical{
    run_universe_hash,      // RunUniverse 排序后的 hash
    scope,                  // market|symbol
    shard_key,              // '' 或 symbol
    param_effective_hash    // 生效参数（含 run 覆写）
})
```
3. 恢复时**四元组全部匹配**才恢复；任一不符 → `ErrStateIdentityMismatch`，fail-closed，错误信息点名"哪一项变了"（票池变了 / 参数变了 / artifact 变了），并提示"换票池请从零跑，不要恢复"。
4. `ts` 的语义同时收紧：**同一 `(strategy, run, shard)` 的 `ts` 必须单调不减**，回退即拒绝（防时钟/乱序写入导致"跳过前缀"跳错）。

**验收**：① symbol scope 5000 分片跑完 → 表中 5000 行、每行 `shard_key` 唯一；② 中断后续跑结果与一次跑完逐位一致；③ 换票池后恢复必须被拒且错误可读；④ market scope 旧行（`shard_key=''`）可平滑迁移。

**工单**：G24（P0，M0b 前置；market scope 也要改，因为 PK 变更是一次性的）。

---

### N4 · 终止重建的"重放"会重复应用信号（判据 A）

**事实**

C1 的处置写："会话进入 terminated，禁止继续调用。恢复路径 = 从最近检查点重建实例并**重放**（复用 `checkpoint.go` 的日期边界裁决）"。
`strategy-state-machine-wasm.md` §3 写："载入检查点 → 重建 → initialize → load_state → **重放该日期块剩余的 bar** → 继续推进"。

**歧义**：检查点在**日期边界**（状态 = 第 D 日收盘）。若 trap 发生在第 D+3 日：
- 重放 D+1、D+2、D+3 的 bar 时，策略会**再次 emit** 这三天的信号；
- 而 D+1、D+2 的信号**已经在引擎侧成交**（引擎没有回滚）；
- ⇒ 重复下单 / 重复成交，净值凭空多出一截。**判据 A，且看起来完全正常。**

同类问题还有一处：A1 要求 `EndRun` 必须 `finalize()`。但 **trap 后模块已被 `WithCloseOnContextDone` 关闭，`finalize` 根本调不了** ⇒ B5 的"finalize 信号归最后一日"在终止路径上不可能成立，必须显式定义为"本 run 降级，finalize 不执行"。

**设计裁决（冻结）**

1. 重放分两种，**必须显式选择**，且 host 侧知道当前处于哪种：

```
replay_mode = state_only | apply
```
   - **`state_only`（推荐，唯一允许的默认）**：重放期间 `emit_signal` **不写入 Signals**，只推进策略内部状态；host 维护 `replay_cursor`，重放到故障 bar 后**重新执行该 bar**，此时 `replay_mode=apply`，其信号才是唯一被应用的信号；
   - `apply`（不推荐）：回滚引擎侧组合/成交/资金到检查点再重放。需要引擎支持事务性回滚，成本高，只在"策略状态与成交强耦合且无法 state_only 复现"时使用，必须显式配置。
2. `emit_signal` 在 `state_only` 下返回**成功但不记录**（返回值语义固定），并计数 `replay_suppressed_signals`；超过阈值（如 10×预期）告警——它意味着"策略在重放期行为异常"。
3. 终止路径的 `EndRun`：**不得调用 `finalize`**（模块已 Close），run 标记 `degraded`，`Failures += session_terminated`；B5 的 finalize 语义只在正常路径成立（文档必须写这一句）。
   > ⚠️ **2026-10-20 订正（refactor-plan-review C1）**：「标记 `degraded`」按冻结三值改为 **`invalid` + 稳定 `invalid_reasons` code `session_terminated`**（映射规则见 `tellurion:docs/TASKS.md` QT-10 ③-④ / QT-15 ⑤），`Failures += session_terminated` 仅作诊断明细，不引入 `degraded` 第四值。
4. 重建次数上限（C1 的 3 次）按 **run** 计，不按 bar 计；超过即 `failed`（不要"每个 bar 都可以重建 3 次"）。

**验收**：在 bar k 注入一次 trap（人为 sleep 超预算）→ 重建后成交笔数、成交价、净值与"无 trap 跑法"**逐笔一致**（不是"接近"）；`replay_suppressed_signals` 计数符合预期；finalize 里发 `long` 在 normal 路径 trap、在 terminated 路径不执行且 run 标 ~~`degraded`~~ **`invalid`**（+ `session_terminated`，见上 ⚠️ 订正）。

**工单**：G25（P0，与 C1/A9 同批——三者共用"会话/检查点"语义）。

---

### N5 · host 固定内存偏移没有"模块预留"契约（判据 A，契约冻结前必须定）

**事实**

| 事实 | 证据 |
|---|---|
| params 写到线性内存**绝对偏移 0** | `sp.inst.WriteMemory(0, params)`（`on_bar_session.go:67`） |
| state 写到**绝对偏移 4096** | `m.Memory().Write(4096, val)`（`host_api.go:231`） |
| A6 只冻结了"区间与上限"，**没有一句话说这两段由模块预留** | `p0-remediation-design.md` §2 A6 |

线性内存是**模块自己的地址空间**：TinyGo/Rust 编译产物把 data、heap、stack 都放在里面。host 从 0 开始写 = 直接覆盖模块的运行期数据。当前能跑，是因为"当前 TinyGo 版本的默认布局恰好把低地址留空了一点"——**这是巧合，不是契约**：换编译器版本、加一个全局变量、TinyGo 调整链接脚本，就会变成"策略状态随机错乱/崩溃"，而且是**静默的**（A6 描述的正是这种最难定位的 bug）。

同时 A6 的 scratch 失效语义（per-bar bump allocator，上限 256 KiB）也依赖"host 能安全写 [4096, 4096+256KiB)"——同样没有预留保证。

**设计裁决（冻结）**

1. **宿主区由模块声明，host 只写声明过的区域**：

```
// 策略模块必须导出（global 或函数，二者取一，建议 global i32 常量）
__host_params_ptr   : i32   // 必须 ≥ 4096 且 8 字节对齐
__host_scratch_ptr  : i32
__host_scratch_size : i32   // 必须 ≥ 64 KiB（与 A6 的 MaxScratchPerBar 取 min）
```
2. 加载时校验（fail-closed，在 `instantiate` 之后、`initialize` 之前）：
   - 符号缺失 → `ErrHostRegionMissing`，错误信息含"请用 v2 SDK 模板重新编译"；
   - `ptr` 与区间重叠、未对齐、`ptr+size > memory.Size()` → `ErrHostRegionInvalid`。
3. `initialize` 写 params 前再校验一次 `len(params) ≤ min(MaxParamsBytes, scratch_ptr - params_ptr)`；**不允许**默认 0、不允许截断。
4. **v1 兼容**：v1 artifact 没有这些导出符号 ⇒ 只能走"白名单内的内置模板 + 已知布局"分支，并打 `legacy_host_layout` warning；新编译产物一律必须带（与 B3 的 import 版本族校验同一道门）。
5. `get_state` 的 scratch 从 `__host_scratch_ptr` 起分配（per-bar bump allocator），不再硬编码 4096。

**验收**：① 缺符号的 wasm 被拒且错误可读；② `ptr` 指向模块数据段的 wasm 被拒；③ params 4097 字节被拒；④ 同一 artifact 在"有 params 但从不读"与"读 params"两种策略下都不覆盖模块自身数据（用一个把全局变量填成哨兵值的测试策略验证哨兵不被改写）。

**工单**：G26（P0，**属于 abi v2 的一部分，必须与 B1/B3 同批冻结**——artifact 出厂后再加导出符号 = 又是一次 ABI 破坏）。

---

### N6 · 可复现留痕不闭合（判据 C）

**事实（三处互相印证）**

1. `run_spec_hash` **只被引用、从未定义**：全设计集仅 2 处提及（A3 表、D26），无字段清单、无 canonical 规则、无落库位置。
2. "三 hash"覆盖面不足：设计承诺记 `strategy_ref hash` / `dataset_snapshot_hash` / `risk_rules_hash`（+ seed）。但**影响结果的输入远不止这三个**：初始资金、区间、费率、滑点、手数取整、T+1 规则、涨跌停处理、成本模型参数、`FillTiming`（N2）、复权锚点 digest（A11 自己证明了它会让 run 失败）、执行算法、内核名与引擎版本、toolchain/abi。
3. **`dataset_snapshot_hash` 两处定义不一致**：
   - `data-service-contract-and-ownership.md` §2.1：`票池 + 窗口 + 频率 + 数据版本`（5 项）；
   - `adr-029-amendment-host-api-v2.md` §7.1：`Axis 必须纳入 dataset_snapshot_hash`。
   两份文档都是"提议"状态，实现者会随机挑一个。缺 `Axis` 的那份会导致"日历补了一个节假日 → 同 hash 不同结果"，正是 A4/B1 反复强调要防的事。

**设计裁决（冻结）**

1. **`run_spec_hash` 成为结果的一等身份**，canonical 集合（顺序固定、UTF-8、数字不经 float 往返）：

```
run_spec_hash = sha256(canonical{
  start, end, initial_capital, risk_free_rate,
  strategy_artifact_hash,          // 执行真相（见 N7）
  strategy_params_effective,       // 默认 ⊕ 覆写（A3）
  stock_pool, index_code, universe_hash,   // RunUniverse（B4/§2.6）
  frequency, fill_timing,          // N2
  kernel, engine_version,
  fees, slippage, cost_model, lot_size, t_plus, price_limit_rule,
  risk_rules_hash,
  dataset_snapshot_hash,
  adjustment_anchor_digests,       // A11：按 (symbol,start_date) 排序后的 digest 集合
  seed
})
```
2. `dataset_snapshot_hash` **只保留一处定义**（写进 contracts 文档，其余文档改为引用）：

```
dataset_snapshot_hash = sha256(canonical{
  universe_hash, window, frequency, data_version,
  calendar_hash,        // Axis（交易日序列）
  adjustment_basis      // 复权口径 + 基准锚点摘要
})
```
3. `run_artifacts` 落 `run_spec_hash` + 各分量 hash + `params_json`（A3）+ `status`（N1）。**重跑校验**：给定 `run_spec_hash`，能重建全部输入；任一分量不一致 → 拒绝"同参复现"的宣称。
4. `seed` 进 `RunSpec`，默认 0 且**必须显式**（D15 的随机性补丁一旦启用，seed 缺失就是不可复现）。
5. 结果里必须记**实际执行核**（D4 能力路由后 `Kernel` 可能被路由改写；现状 `Kernel` 空 = 默认 go，且不回填 ⇒ 结果不知道自己是谁跑的）。

**验收**：改任一项（费率/滑点/票池/日历/复权基准/内核/`FillTiming`）⇒ `run_spec_hash` 必变；不改 ⇒ 必不变（向量测试）；两条不同 hash 的结果**禁止**被标为"同参复现"。

**工单**：G27（P0，契约冻结；`run_spec_hash` 的字段清单必须在 M0 前定，否则 M0 的"逐位一致"验收没有留痕可依）。

---

### N7 · `version_hash` ↔ `artifact_hash` 双哈希的解析漏洞（判据 C）

**事实**

| 事实 | 出处 |
|---|---|
| `version_hash` = 源码 canonical（strategy_id/carrier/abi/scope/entry/source） | asset-api §2、A4 |
| `artifact_hash` = **字节码** sha256，引擎缓存与校验用它 | asset-api §1.2、§4 门 3/5 |
| `toolchain` 只作为 artifact 表的一列，**不进 `version_hash`** | asset-api §1.2 |
| `strategy_ref` 的 `@hash` 语义指向 `version_hash`（`active_version_hash`） | asset-api §1.1/§3.3、D25 |

**后果**：同一份源码用 TinyGo 0.34 与 0.35 各编一次 → **同一个 `version_hash`，两个 `artifact_hash`**。于是：
- `strategy_ref = id@version_hash` 这个"锁定版本"的引用，**不锁定字节码**；
- 结果记录的是 `version_hash`，但真正决定结果的是那份字节码；
- 策略服务重建一次（换工具链/换构建机）就可能让历史 `id@hash` 指向新的字节码 ⇒ **"同 ref 同结果"不成立**，且没有任何一处会报错。

这与 N6 是同一类病（留痕的不是真正决定结果的东西），但修法不同，所以单列。

**设计裁决（冻结）**

1. **执行与结果一律以 `artifact_hash` 为真相**；`version_hash` 降级为"源码版本的人读标签"，两者的关系是 1:N。
2. `resolve` 必须同时返回两者，并在返回体里明确 `artifact_hash` 是执行身份。
3. `strategy_ref` 接受两种形式，语义写死：

| 形式 | 语义 | 可复现性 |
|---|---|---|
| `id@artifact_hash` | 直接锁定字节码（**CI/K15/台账推荐**） | 强 |
| `id@version_hash` | 解析到"该源码版本下当前 promote 的 artifact"，并把**实际 artifact_hash 回写进结果** | 中（依赖 promote 不变） |
| `id`（latest） | 提交边界解析 → 固化为 `artifact_hash`（D25） | 强（提交后） |

4. `promote` 一个 `version_hash` 下已有 artifact 时：**必须新增 artifact 行，不得覆盖**；`active_artifact_hash` 与 `active_version_hash` 分开存。
5. `run_artifacts` 存 `{strategy_ref_input, version_hash, artifact_hash}` 三个值，便于回答"当初用的是哪个引用、解析成了什么"。

**验收**：同源码两工具链编译 ⇒ 两行 artifact、两个 `artifact_hash`、两套结果可分别追溯；用 `id@version_hash` 跑出的结果里能读到实际 `artifact_hash`；`id@artifact_hash` 在 artifact 被 deprecate 后仍可解析（历史回放不因下线而失败）。

**工单**：G28（P0，与 N6 同批；asset-api §1/§3 需同步修订）。

---

### N8 · run 级资源准入缺失（判据 B）

**事实**

| 事实 | 出处 |
|---|---|
| `Engine` 共享、允许并发、**无并发上限** | `engine.go:155` 注释；`handlers_backtest.go:136` |
| 每个 run 预热本 run 票池的**全区间** OHLCV | `engine.go:775` `warmCache(pool, start, end)` |
| A10 的修法是"缓存下沉 per-run" | p0 A10 首选方案 |
| A1 的会话上限写的是"≤ **并发 run 上限** × 1.2"，但**并发 run 上限本身没有定义** | p0 A1 规则 3 |
| C2 的 512 MiB 只约束 **wasm**，不含数据缓存 | p0 C2 |

**后果**：A10 修完后，内存从"一份共享缓存"变成"**每 run 一份缓存**"。全市场 5000 标的 × 数年日线 ≈ 数百 MB/run；10 个并发 run ⇒ GB 级；再加上每 run 的 wasm 实例与结果驻留。**这不是"会不会 OOM"，是"跑几个之后 OOM"**。判据 B，而且它被 A10 的修复**放大**了（修之前是"慢慢涨"，修之后是"并发倍数"）。

**设计裁决（冻结）**

1. `Engine` 增加 **run 准入闸**（不是限流中间件，是内核自己的资源契约）：

```
maxConcurrentRuns  默认 min(GOMAXPROCS, 4)，可配
admissionQueue     有界（默认 2×maxConcurrentRuns），满则快速拒绝（429/ErrRunQueueFull）
runMemoryBudget    单 run 预算：dataCacheBytes + wasmBytes + resultBytes
processMemoryBudget 进程总预算（默认 70% of RSS 上限），用于驱逐/拒绝判定
```
2. 准入时机：`RunBacktest` **在分配任何缓存之前**申请配额；申请失败 → 排队（有超时）或拒绝，**不允许"先跑起来再说"**。
3. 预算不足时的降级顺序（写死，不许各模块自己决定）：降 `W`（C2 的分片并发）→ 降预加载窗口（只 warm 需要的区间）→ 拒绝。
4. 指标：`runs_active` / `runs_queued` / `cache_bytes_total` / `wasm_bytes_total` / `run_evicted_total`；超过 80% 预算告警。
5. 与 A10 的接口：per-run 缓存的**字节数必须可计量**（`CacheManager` 暴露 `Bytes()`），否则预算无从判定——这一条要写进 A10 的验收。

**验收**：10 个全市场 run 同时提交 → 进程 RSS 不超过 `processMemoryBudget`，其余排队并在超时后明确拒绝；kill 一个 run 后内存回落；预算调到很小 → 全部排队/拒绝，**不 OOM**。

**工单**：G29（P0，与 A10/C2 同批——三者是同一笔账）。

---

### N9 · ABI 表达不了"目标仓位/数量"，与 M0b 验收冲突（判据 A：功能不可达）｜**已裁决：新增 `emit_target2`**

**事实**

- `EmittedSignal{SymbolIdx, Action int32, Strength float64}`（`host_api.go`，ABI v2 §2.1 未新增字段）；
- Action 只有 `hold/long/close/short`（A5 冻结）；
- **Strength 已被 D26 明确裁决为"信号强度/置信度 ∈ [0,1]"，且明确写了"不定义为目标权重"**；
- 仓位数量由 `RiskManager.CalculatePosition` / `calculatePositionsBatch` 决定（`risk/manager.go`、`engine_daily.go:318`）；
- 而 M0b 的验收原文是：「持仓状态机 + 冷却期 + **分层加仓**」。

**后果**：策略只能表达"方向 + 置信度"，**无法表达"再加 2% 仓位"或"减到 5%"**。`Strength` 被乘进 `baseWeight` 后，最终手数还受 `MaxPositionWeight`、资金、手数取整影响——策略作者写不出确定的分层加仓。M0b 要么做不出来，要么"看起来做了"（靠连发 long + 依赖风控恰好加仓），结果是**Go/WASM 对拍时两边都"差不多"但都不确定**——判据 A（语义不可控），且直接卡住 M0b 验收。

**设计裁决（二选一，必须现在定）**

- **方案 A（推荐，保 D26 不变）**：ABI v2 新增 `emit_target2(symbol_idx: i32, weight: f64) -> void`，`weight ∈ [0,1]` = **目标权重（占当前总权益）**，`-1` 表示"不动"。
  - 引擎翻译：`qty = floor(weight × equity / price / lot) × lot`，并受 `RiskPolicy` 上限约束（**政策上限优先，策略只能 ≤ cap**）；
  - 结果标注 `sizing_source ∈ {policy, strategy_target}`，K15 对拍时要求两侧一致；
  - `emit_signal` 与 `emit_target2` 同一 bar 混用 → trap（避免两套 sizing 打架）。
- **方案 B（不改 ABI）**：把 M0b 的"分层加仓"验收改为"跨日多次 `long` + 风控决定加仓量"，并**明确写"策略不承诺仓位"**。代价：AI 生成的状态机无法表达仓位意图，产品力打折。

**建议**：选 A。理由是它不动已被裁决的 Strength 语义（D26 不推翻），只是补一个正交的能力；且 `emit_target2` 属于**新增函数名**，与 B3 的增量命名策略一致，不构成第二次 ABI 破坏——**但必须与 `get_bar2` 同批冻结**（回到"artifact 出厂就改不动"的问题）。

**验收**：同一状态机在 Go 与 WASM 下用 `emit_target2` 表达"分三层加仓"，成交笔数/手数/成本逐位一致；混用两种 emit 被 trap。

**工单**：G30（P0，M0b 前置；与 B1/B3/N5 同一批 ABI 冻结）。

---

## 4. 修订后的依赖顺序

```
【wave-0 · 契约冻结（只写文本 + 向量测试 + 差异报告，不改行为）】
  N2 FillTiming（先出 same_close vs next_open 差异报告，再定默认）
  N5 host 区所有权（abi v2 的一部分）
  N6 run_spec_hash / dataset_snapshot_hash 单一定义
  N7 artifact_hash 为执行真相
  N9 emit_target2 决策（ABI 同批）
  A12 存量策略重复信号扫描 → 再决定 trap / 合并

【wave-1 · 执行正确性（修"今天就在错"的）】
  C1 → A2 → A10 → A7 → A11 → A1
       └─ N1 错误分级（与 A1 同批：EndRun 语义的前提）
       └─ N4 重放语义（与 C1/A9 同批）
       └─ N8 资源准入（与 A10 同批：per-run 缓存必须有账）

【wave-2 · 契约落地】
  A3 → A8 → A12 → A4/A5/A6/A9（含 N3 主键与信封）
  B4 → B1+B3+B5（含 N5 的导出符号）→ B2 → C2

【wave-3 · 里程碑】
  G2/G3 接线 → M0（market scope）→ M0b（symbol scope，需 N3/N9 已落地）
```

**排序理由（新增部分）**

- **N1 必须与 A1 同批**：A1 规定"错误路径也要 `EndRun`"，但现在的错误路径根本不会终止 run——先修分级，`EndRun` 才有意义。
- **N3 必须与 A9 同批**：信封加字段与 PK 重建是同一次 DDL 变更，分两次做等于迁移两遍。
- **N5/N9 必须与 B1/B3 同批**：三者都是"artifact 出厂后改不动"的 ABI 面，分批冻结 = 多次破坏。
- **N8 必须与 A10 同批**：A10 把缓存变 per-run，等于把"一份账"变成"N 份账"，没有总预算就是主动制造 OOM。
- **wave-0 全部排在代码之前**：这 6 项都是"决定语义"而非"改实现"，成本是写文档 + 跑一次差异报告；一旦进入实现再改，代价是重跑全部基准。

---

## 5. 三条 P1（记账，不列 P0）

| # | 问题 | 为什么不升 P0 |
|---|---|---|
| P1-1 | **BE-02：回测复用 `pkg/live` 撮合**，改实盘会改回测口径 | 现在没有"改 live 导致历史结论变化"的实际事件；但它是"回测结论能否代表实盘"的根本问题，建议作为 P1 首项 |
| P1-2 | **A7 方案 A 的代价未记账**：缓存下沉 per-run 后，重复跑同一区间**失去跨 run 复用**，每次都要全量 warm | 是性能退化不是错误；但与 N8 的预算强耦合，实施时一起评估 |
| P1-3 | **BE-11 市场摩擦完整度未查清**（停牌/成交量/复权/分红/涨跌停） | 属"事实未核实"；但它决定"逐位一致"的基准是否可信，建议在 M0 前补一次 |

---

## 6. 影响面增量（相对 p0-remediation §4）

| 位置 | 新增改动 |
|---|---|
| `pkg/backtest/engine.go` | 错误分级（N1）、run 准入（N8）、`FillTiming` 分支（N2） |
| `pkg/backtest/engine_daily.go` | 成交价按时点取值（N2）；`invalid_reasons` 记录数据缺口（N1） |
| `pkg/backtest/contracts/contracts.go` | `FillTiming`、`RunSpecHash` 字段、`ResultSpec.Status/Failures` |
| `pkg/strategy/state_store.go` | PK 加 `shard_key`、信封四元组（N3）；`ts` 单调校验 |
| `pkg/strategy/checkpoint.go` | 恢复校验 `run_identity_hash`；`replay_mode` 传递（N4） |
| `pkg/sandbox/wasm/on_bar_session.go` | `__host_params_ptr` 校验（N5）；`state_only` 下 `emit_signal` 抑制（N4） |
| `pkg/sandbox/wasm/host_api.go` | 导出符号校验、scratch 基址动态化（N5）；`emit_target2`（N9） |
| `pkg/sandbox/wasm/loader` | `ErrHostRegionMissing/Invalid` 门（N5，与 B3 同批） |
| 策略 SDK 模板 | 预留宿主区 + 导出符号（N5）；`emit_target2`（N9） |
| `docs/openapi.yaml` + `run_artifacts` | `run_spec_hash` 与分量留痕（N6）；`strategy_ref` 三形态（N7） |
| `internal/repoguard` | 新增：`getSignals` 返回值禁止只 log（N1）；`run_spec_hash` 分量改动必须同步向量测试（N6） |

---

## 7. 一句话总结

**旧 P0 修的是"状态挂错地方"，新 P0 修的是"错误没有出口（N1）、时间没有定义（N2）、状态没有身份（N3）、重放没有边界（N4）、内存没有主人（N5）、留痕没有覆盖（N6/N7）、资源没有闸门（N8）、仓位没有表达（N9）"。**

这九条里有四条（N1/N2/N6/N7）**今天就影响每一次回测的正确性**，不需要等 WASM 接线——建议先把 wave-0 的文本冻结掉，再动代码。

---

## 8. 三项裁决落档（2026-10-20，用户按建议拍板）

> 本节是 wave-0 的**冻结依据**：QT-01/QT-02/QT-04 三张工单直接引用这里的文本，不再另写一份。

### 8.1 N2 · 成交时点：`FillTiming` 进 SignalSpec，**终态 `next_open`**

| 项 | 冻结内容 |
|---|---|
| 字段 | `SignalSpec.FillTiming ∈ {next_open, same_close}`，**必填、无默认值**（缺失 → `ErrFillTimingMissing`，fail-closed） |
| 终态 | `next_open`：T 日收盘决策 → T+1 开盘成交（含滑点）；T+1 停牌/涨跌停 → 按 REV-09 顺延或拒单 |
| 兼容 | `same_close` 保留（= 收盘集合竞价假设），结果必须标 `known_bias: same_close_fill` |
| 切换纪律 | **先出差异报告**：同一批基准策略在两种口径下各跑一遍，差异需可解释（约等于"日收益平移一个 bar"），报告归档后才把默认切到 `next_open`；**禁止**为"保持历史基准不变"而选口径 |
| 入 hash | `fill_timing` 进 `run_spec_hash`（§N6） |
| 跨内核 | NT 侧必须按同一 `FillTiming` 实现；K15 对拍前先断言口径一致，不一致报 `ErrFillTimingMismatch`（"口径不符"，不是"结果不一致"） |
| 与可见性 | `get_bar2` 允许读当日 close（决策需要），成交价按 `FillTiming` 取——两者不冲突，文档必须写在一起 |

**验收**：两口径差异报告存在且差异可解释；`FillTiming` 缺失时 run 拒绝；K15 口径不一致时错误可读。

### 8.2 N9 · 仓位表达：新增 `emit_target2`，**不动 D26 的 Strength 语义**

```
emit_target2(symbol_idx: i32, weight: f64) -> void
  weight ∈ [-1.0, 1.0]   // 目标权重（占总权益）；-1 = 不动；0 = 清仓
  非 finite 或越界 → trap
```
| 规则 | 冻结内容 |
|---|---|
| 语义 | **目标权重**（不是增量）：引擎算 `qty = floor(|weight| × equity / price / lot) × lot`，方向由符号决定（>0 多，<0 空） |
| 与政策 | `RiskPolicy` 的 cap **优先**：`final_weight = min(|weight|, policy_cap)`；被截断必须计数 `sizing_clamped_total` 并落 run 诊断 |
| 与 emit_signal | **同一 bar 同一 symbol 混用两种 emit → trap**（两套 sizing 不许打架） |
| 记录 | 结果标 `sizing_source ∈ {policy, emit_signal, emit_target2}` |
| 旧策略 | `emit_signal` 语义完全不变（D26 不动）；`emit_target2` 是**新增函数名**，符合 B3 的增量命名策略 |
| 冻结时机 | 与 `get_bar2` **同批**（同一次 abi v2 冻结）；artifact 出厂后再加 = 第二次 ABI 破坏 |

**验收**：分层加仓（3 层）在 Go/WASM 下成交笔数、手数、成本逐位一致；混用被 trap；policy cap 截断有计数。

### 8.3 A12 · 重复信号：**先扫描存量，再决定 trap / 合并**

| 步 | 动作 | 判据 |
|---|---|---|
| 1 | 存量扫描：14 个内置策略 × 3 个基准区间，统计 `(date, symbol)` 重复度 | 输出重复率与样例（哪个策略、哪一天、哪两条 action） |
| 2 | 若重复率 = 0 | **直接 trap**（写进 SignalSpec 当契约，不做开关） |
| 3 | 若重复率 > 0 | **先合并**：`close` 优先于 `long/short`；同向多条按 strength 求和并 clamp 到 [0,1]；合并计数 `duplicate_signals_merged` 进诊断；**trap 留作下一版开关**（等存量策略迁移完） |
| 4 | 无论哪种 | 规则写进 SignalSpec；Go 侧同逻辑策略一起遵守，否则 K15 失真 |

**验收**：扫描报告存在；按裁决实现后，同一 bar 相反 action 的行为确定且 20 次一致；存量策略无一因该规则意外失败（或失败原因被显式登记）。

### 8.4 三项裁决对已有文档的改动清单

| 文档 | 改动 |
|---|---|
| `adr-029-amendment-host-api-v2.md` | §2.1 函数表加 `emit_target2`（**8 个 v1 + 4 个 v2 = 12 个函数**；2026-10-21 QT-01 订正：本行原写「12 → 13」，与 §7.2-1 自己给的枚举 8+4 不符）；新增 §8 引用本节 |
| `README.md`（本设计集） | 裁决记录加 3 行；待裁决清单移除 N2/N9/A12 |
| `project-engineering-file.md` | 新增 ADR-D28 |
| `docs/TASKS.md` | 归档第 3 代，第 4 代工单 QT-01/02/04 直接引用本节 |
