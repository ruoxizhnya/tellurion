# W1 跨仓落点与仓归属（施工前定盘）

> 状态：**定盘（2026-10-23）**｜对象：`tellurion:docs/TASKS.md` W1 七单（QT-10 → 11 → 12 → 13 → 14 → 16 → 15）+ QT-22 的落点核对。
> 用途：回答三个问题——① QT-10 / QT-11 / QT-22 的代码落在**哪个仓、哪个包**；② W1 每一单**动哪些文件**、要不要动 `contracts` / `kit` / DDL；③ 哪些单**改同一个文件**（撞车点与串行边界）。
> 真相源优先级：`repo-inventory.md`（仓名 / module path）> `target-module-topology.md`（module 职责与依赖方向）> **本文件（仅 W1 落点）**。本文件不改判任何已裁决边界；发现冲突时以真相源为准，并回来订正本表。

---

## 0. 判据（本表按这四条裁，不按个人偏好）

| # | 判据 | 出处 | 对 W1 的直接后果 |
|---|---|---|---|
| J1 | 依赖方向 `kit ← contracts(含 domain) ← core`；agent → kit + contracts（编译期）+ HTTP → core（运行期）；服务仓只经 contracts 交互 | [target-module-topology.md](target-module-topology.md) 依赖不变量 | W1 **不得新增任何反向边**。注意 `tellurion-data` 的运行期箭头是 **core → data**（`marketdata.NewHTTPProvider(dataServiceURL)` 按 URL 取数），但那是 HTTP 运行期调用、不是编译期 `require`；W1 不得把这条变成编译期依赖 |
| J2 | `contracts` = port 接口 + 边界 DTO + 哨兵错误；禁止实现、禁止业务规则 | 同上 §module 职责表 | 哨兵错误 / 稳定 Code / canonical hash **进 contracts**；注册表、包装、状态映射 **留 core** |
| J3 | **住在 core 的 port 不因本表搬仓**——`SignalProvider` 已登记「留 core 至策略资产拆分」 | `tellurion-contracts:docs/API.md` §5.2（`SignalProvider` 行） | QT-14 的生命周期类型**留 core `pkg/strategy`**，与 `SignalProvider` 同域，不拆两处 |
| J4 | 谁算 hash 谁唯一：canonical 编码器只有 `contracts/engine/canonical.go` 一处；新 hash = 新域标签（**只增不改**） | `tellurion-contracts:engine/canonical.go` 文件头纪律 | 任何仓都不得自写 `json.Marshal + sha256`；QT-16 / QT-22 的新 hash 必须走 `contracts.Canonical` |
| J5 | 逐 bar / 逐笔路径永驻 core 进程内 | ADR-027 §2.1；项目硬约束 | W1 不产生任何 HTTP / 消息队列调用 |

---

## 1. W1 仓归属表（七单）

| 单 | 落点仓 | 要改的包 / 文件（只列关键项） | 动 `contracts`？ | 动 `kit`？ | 动 DDL？ | 前置 |
|---|---|---|---|---|---|---|
| **QT-10** 错误分级 | **contracts + core** | contracts：`engine/execution_errors.go`（新，三类哨兵）+ `engine/validity.go`（+1 稳定 Code `data_gap_skipped`）；core：`pkg/backtest/engine_daily.go`（**今天在此 `Warn + continue` 吞掉取数错误**）、`pkg/backtest/engine.go`（`getSignals` 只 log 的分支 + 已有 failed 分支）、`pkg/strategy/signal_provider.go`、`pkg/sandbox/wasm/on_bar_session.go` | ✅ 哨兵 + 1 个 Code | ❌ | ❌ | 与 QT-14 同批；QT-10 落「分类 + 状态映射 + 终止判定」，EndRun 本体由 QT-14 补（见 §2.1） |
| **QT-11** per-run 实例 | **core** | `pkg/strategy/registry.go`、`pkg/strategy/signal_provider.go`、`pkg/strategy/plugins/*.go`（14 个注册点）、`internal/repoguard/`（新护栏测试） | ❌ | ❌ | ❌ | 无。WASM 腿无落点（今天仓内无 WASM provider，见 §2.2） |
| **QT-12** 缓存下沉 + 准入 | **core** | `pkg/backtest/engine.go`、`pkg/backtest/cache/cache.go`、`pkg/backtest/options.go`、`internal/repoguard/`（Engine 字段 AST 白名单） | ❌ | ❌ | ❌ | 无 |
| **QT-13** 复权锚点切片 | **core** | `pkg/backtest/adjustment_snapshot.go`（+ 回归测试） | ❌ | ❌ | ❌ | QT-12（读同一个合并缓存） |
| **QT-14** Provider 生命周期 | **core** | `pkg/strategy/signal_provider.go`（`RunMeta` / `SessionLifecycle` / `Stateful`）、`pkg/backtest/engine.go`（BeginRun / `defer EndRun`）、`pkg/backtest/options.go`、`pkg/bootstrap/engine.go`（装配期 fail-closed） | ❌（裁决见 §2.3） | ❌ | ❌ | QT-11（实例语义）；与 QT-10 同批 |
| **QT-16** 检查点身份 | **contracts + core** | contracts：`engine/canonical.go`（+1 域标签 `DomainRunIdentityHash`）；core：`pkg/storage/postgres.go`（内联 DDL 迁移）、`pkg/strategy/state_store.go`（PK + 信封）、`pkg/strategy/checkpoint.go`、`pkg/strategy/state_identity.go`（新）、**`contracts/kernel_modules.schema.sql`（core 内 K0 冻结资产，必改）** | ✅ +1 域标签（裁决见 §2.5） | ❌ | ✅ **W1 唯一一单** | 无；必须先于 QT-15 |
| **QT-15** 终止 + 重放 | **core** | `pkg/sandbox/wasm/wazero_runtime.go`、`pkg/sandbox/wasm/on_bar_session.go`、`pkg/backtest/engine.go` | ❌ | ❌ | ❌（用 QT-16 的 `shard_key`，不自己加） | QT-14 + **QT-16** |

**总账**：W1 七单中 **5 单纯 core**（11 / 12 / 13 / 14 / 15），**2 单带 contracts 腿**（10 / 16）；**DDL 仅 QT-16 一单**；**`kit` 全程零改动**。跨仓提交顺序固定为 **contracts 先、core 后**（core 用 `require` 伪版本，`go get` 升版；S1 期不加 `replace`）。

---

## 2. 落点专项裁决（题面 ① 三单 + 两处补裁）

### 2.1 QT-10：三类哨兵 → `contracts/engine`（已核，任务单原文即指定）

**落点**

| 内容 | 文件 | 形态 |
|---|---|---|
| `ErrDataGap` / `ErrStrategyFault` / `ErrContractBreach` | `tellurion-contracts:engine/execution_errors.go`（新建） | 三个 `var Err... = errors.New(...)`，只做稳定标识 |
| `data_gap_skipped` | `tellurion-contracts:engine/validity.go`（追加常量） | 与 OBS-01 / K17-R1 的稳定 Code 同处，命名 `InvalidDataGapSkipped` |

**为什么在 contracts**：① QT-10 ③-① 原文直接写「`contracts` 定义三类哨兵」；② J2 把哨兵错误划给 contracts；③ 未来外部 provider（策略资产服务 / 远程供给）必须能表达同一分类——它不能 `import tellurion/pkg/...`，否则形成反向边。

**义务边界（防 contracts 膨胀）**：contracts 只放哨兵与 Code。**分类映射是业务判断，必须留在 core 包装侧**——下表是**今天错误实际产生的位置**（照实登记，不是设想）：

| 类 | 今天的产生点 / 吞点 | 现状（实测） |
|---|---|---|
| `ErrDataGap` | `pkg/backtest/engine_daily.go:124-127` | **没有 error 上升路径**：`fetchMarketDataForDay` 返回多值是纯数据、无 `error`；每个标的的取数失败在 `res.err != nil` 处 `logger.Warn(... "Failed to get stock data")` + `continue`，该标的当天**静默从 `marketDataCache` 消失**，run 照常跑完。QT-10 要做的正是把这个吞点改成分类 + 留痕 |
| `ErrStrategyFault` | `pkg/backtest/engine.go:976-987`（`getSignals` 的 err 只 `Warn` + `recordSignalFailure`）、`pkg/sandbox/wasm/on_bar_session.go` | 单日策略故障只进 run 级诊断、不进 `invalid_reasons`（注释明写）；wasm trap 经 `LocalSignalProvider` 被包成 `ErrCodeInternal`，**类别信息在包装后丢失** |
| `ErrContractBreach` | `pkg/strategy/signal_provider.go:191-193`（`RejectFutureSignals`）、`pkg/sandbox/wasm/host_api*.go` | `RejectFutureSignals` 返回裸 `fmt.Errorf`，未打类；调用方只能按字符串认 |

**取数的现成抓手**：`processStockJob`（`engine_daily.go:177-193`）已把取数失败包成 `apperrors.ErrCodeDataQuality`，op 分别是 `fetch_ohlcv` / `adjustment_snapshot`。QT-10 的分类可以从这两个 op 起手，而不是重新发明一套错误码。

**必须先解决的一个歧义（否则 `data_gap_skipped` 会被灌满）**：`engine_daily.go:196-205` 用一个 `suspended` 布尔同时表达**停牌**与**数据缺口**（注释自述「不是则当日停牌（**或数据缺口**）」）。若不先把两者分开，每个停牌日都会被判成数据缺口、写进 `invalid_reasons`，正常回测也会被判 `invalid`。QT-10 的前置动作是：让 `processStockJob` 区分「当日无 bar 但序列完整（停牌）」与「取数失败 / 序列断裂（缺口）」。

**状态映射（真实符号，非任务单原文的名字）**：任务单 ③-④ 写的 `buildResponseFromState` **在源码里不存在**。实际有两处：

- 写路径：`RunBacktest`（`engine.go:528-546`）——`runBacktestInternal` 返回 err 时**已经** `state.SetStatus(StatusFailed)` + `SetError` + 返回 `Status: StatusFailed` 的响应。QT-10 的 fatal 只需要**让错误真的从 `runBacktestInternal` 返回**（今天在 976-987 被吞），不必新建 failed 分支；
- 读路径：`ResponseFromState`（`engine.go:732-736`）——只放行 `StatusCompleted` / `StatusInvalid`，**`failed` 返回 `(zero, false)`**，调用方退回 DB job 路径。⇒ QT-10 落 failed 后，必须同步决定「failed 的 run 结果是否要能被读路径读出」，否则 UI/报告看不到失败原因。

**契约硬约束**：`StatusFailed` 在 `contracts/engine/contracts.go:289` 的定义是「`Error` 必填，`InvalidReasons` 必空」。⇒ 走 failed 的分支**不得**再往 `InvalidReasons` 里塞东西；诊断信息只能进 `Failures[]` / `Error`。

**与 QT-14 的接口（本表定盘，防撞车）**：QT-10 要「立刻结束」但 `EndRun` 的创建/销毁属 QT-14（QT-10 ⑥ 边界原文）。两单同批、按序 10 先：QT-10 的终止分支只做「返回 fatal error + `status=failed`」，**不得在 10 里自造 provider 会话销毁逻辑**；QT-14 落 `BeginRun` / `defer EndRun` 后，10 的终止路径自然经 defer 走到 EndRun。

### 2.2 QT-11：注册工厂 → core `pkg/strategy`（已核，**不进 contracts**）

**落点**：`pkg/strategy/registry.go`（`RegisterFactory` / `NewInstance` / `Register` 标 `shared=true`）、`pkg/strategy/plugins/*.go`（14 个策略注册点改构造函数）、`pkg/strategy/signal_provider.go`（`LocalSignalProvider` 走 `NewInstance`）、`internal/repoguard/`（执行路径 `.Get(` 即红的护栏）。

**为什么不进 contracts**：

1. `Strategy` 接口、`Registry`、14 个插件都是 **core 运行时对象**；J2 禁止 contracts 放实现。
2. `contracts/engine/registry.go` 是**内核路由注册表**（`EngineRegistry`：engine runner name → `EngineRunner`），与「策略注册表」同名不同物——**不得**当作 QT-11 的落点证据。
3. 把 `RegisterFactory` 放 contracts 会迫使 contracts 依赖 `pkg/strategy.Strategy`（它引用 `Signal` / `Parameter` / `domain.Portfolio` 与 `apperrors`），形成 `contracts → core` 反向边，直接违反 J1。

**WASM 腿的边界**：QT-11 ③ 写「`LocalSignalProvider` 与 WASM provider 都走 `NewInstance`」，但**今天仓内没有 WASM `SignalProvider` 实现**（接线是 W2 的 QT-26，`repo-topology-and-naming.md` 也记它「未接线」）。因此 QT-11 的 WASM 腿在 11 里**无文件可改**；该要求转记为 QT-26 的验收腿（QT-26 落 WASM provider 时必须走 `NewInstance`）。不转记就会变成「验收要求改一个不存在的文件」。

> **已落（2026-10-23）与一处口径订正**：QT-11 ①~④ 全部落地（验收与破坏验证见 `tellurion:docs/TASKS.md` QT-11 进度）。上表与本节写的「14 个注册点」**实测是 15 处**：第 15 处是 `pkg/strategy/expression/strategy.go` 的 `expression_template`（它同样是内置策略，漏迁就永远拿不到 per-run 实例），已一并迁移。本表其余各行（12 / 13 / 14 / 16 / 15）不动。

### 2.3 QT-14：生命周期类型 → core `pkg/strategy`（**裁决：不进 contracts**）

`p0-remediation-design.md` A1 的原文是「生命周期进入**契约**」，代码注释写 `pkg/strategy（或 contracts）`——仓层面留白，本节定盘。

**裁决**：`RunMeta` / `SessionLifecycle` / `Stateful` 落 `tellurion:pkg/strategy/signal_provider.go`，与 `SignalProvider` / `Scope` 同文件。

**理由**：① J3——`SignalProvider` 已由 `tellurion-contracts:docs/API.md` §5.2 登记「住 core，至策略资产拆分」，把它的生命周期类型单独搬去 contracts 会把一份 port 拆成两处，删改时必然漂移；② contracts 若定义 `SessionLifecycle`，就得同时接受它引用的 `RunMeta` 与未来扩展，而消费方（WASM / 本地 provider）全在 core——没有第二个仓需要它，进 contracts 只会制造无消费方的接口；③ W1 硬边界是「不依赖 S2」，QT-14 引入跨仓改动会把 W1 变成跨仓批次。

**未来搬仓**：当策略资产拆分把 `SignalProvider` 搬进 contracts 时，这三个类型**必须同一次提交一起搬**（单点搬 = 漂移）。本表把这条写成搬仓前置，登记在 `repo-topology-and-naming.md` 的既有「未接线」口径旁即可，不新开待裁号。

### 2.4 QT-22：`version_hash` → `contracts/engine`（已核）

**落点**：`tellurion-contracts:engine/provenance.go`（追加 `StrategyVersionInput` + `Hash()` / `MustHash()`）+ `canonical.go`（+1 域标签 `DomainStrategyVersionHash = "tellurion.strategy_version_hash.v1"`）。

**为什么在 contracts 而不是 core / strategy 仓**：① canonical 编码器唯一在 contracts（J4）；② **消费方不止 core**——`tellurion-strategy` 资产服务（QT-41）必须算出同一个 hash，contracts 是唯一共享面；③ `provenance.go` 已是 hash 体系唯一正文（`artifact_hash` / `universe_hash` / `calendar_hash` / `dataset_snapshot_hash` / `run_spec_hash` / `params_hash`），`version_hash` 归入同一文件而不是另起一处。

**集合（冻结 7 项，顺序在 `Hash()` 里逐行写死）**：`strategy_id + carrier + abi_version + scope + entry + source + params_defaults`；**不含 toolchain**（由 `artifact_hash` 区分，QT-03）；不动 `factor_definitions` 既有算法；资产表结构是 QT-41，不在本单。

**边界**：QT-22 只做算法 + 向量测试，**core 不接线**。因此 contracts 的这一定义必须**先于任何使用方**落地，避免各仓自算。

### 2.5 QT-16 的 contracts 腿（本表新裁，需在 16 开工前确认）

`run_identity_hash` 覆盖 `run_universe_hash + scope + shard_key + param_effective_hash`（QT-16 ③-②）。其中 `universe_hash` / `params_hash` 已在 contracts，`scope` / `shard_key` 是字符串——把四者拼成**无歧义**字节流的唯一正确做法是 `contracts.Canonical` + 新域标签，而不是 core 自拼 sha256（J4）。

**裁决**：contracts 增 `DomainRunIdentityHash = "tellurion.run_identity_hash.v1"`（只增不改）；计算函数落 core `pkg/strategy/state_identity.go`（消费方只有 core 的 state store，函数本身不必进 contracts）。**禁止**复用 `DomainParamsHash` 代替——两者语义不同，混用会让「只比参数」的查询误命中。

---

## 3. 撞车点与串行边界（本表最实用的一节）

W1 的施工序是 `10 → 11 → 12 → 13 → 14 → 16 → 15`，一单一 commit。下表列出**同一文件被多单触碰**的位置——这些文件**禁止并行 worktree 分头改**：

| 文件 | 涉及单 | 各单只许动的范围 |
|---|---|---|
| `pkg/backtest/engine.go` | 10 / 12 / 14 / 15（16 传入参数） | 10：`getSignals` 错误分支 + 终止/状态映射；12：`runContext` 与 Engine 字段迁移；14：`BeginRun` / `defer EndRun`；15：重建与重放；16：`run_identity_hash` 传入 |
| `pkg/strategy/signal_provider.go` | 10 / 11 / 14 | 10：错误分类包装；11：`NewInstance`；14：`RunMeta` / `SessionLifecycle` / `Stateful` |
| `pkg/sandbox/wasm/on_bar_session.go` | 10 / 15 | 10：trap / 预算超限 → `ErrStrategyFault`；15：终止状态机 + `replay_mode` |
| `pkg/strategy/state_store.go` | 16 | 独占（PK + 信封） |
| `pkg/storage/postgres.go` | 16 | 独占（内联 DDL 迁移） |
| `contracts/kernel_modules.schema.sql` | 16 | 独占（K0 冻结资产，须走冻结 schema 变更评审） |
| `pkg/backtest/engine_daily.go` | 10 | 独占（`ErrDataGap` 来源） |
| `internal/repoguard/` | 11 / 12 | 11 加「执行路径禁 `.Get(`」；12 加「Engine 禁新增 run 级可变字段」AST 白名单；文件名不同，可同一 agent 串行落 |
| `contracts/engine/*` | 10 / 16 / 22 | 跨仓：契约先落、core 后 bump 伪版本；**不同 agent 不得同时改 contracts**（同仓并行冲突） |

---

## 4. 与既有文档对拍

| 既有文档 | 本文结论 | 是否一致 |
|---|---|---|
| [target-module-topology.md](target-module-topology.md)「contracts = 接口 + 边界 DTO + 哨兵错误」 | QT-10 哨兵进 contracts；QT-11 注册工厂不进 | ✅ |
| `tellurion-contracts:docs/API.md` §5.2「`SignalProvider` 留 core 至策略资产拆分」 | QT-14 生命周期类型留 core；QT-22 hash 进 contracts（hash 是共享算法，不是 port） | ✅ |
| [p0-remediation-design.md](p0-remediation-design.md) A1「生命周期进入契约」+ 代码注释 `pkg/strategy（或 contracts）` | 定盘为 **core `pkg/strategy`**；「契约」读作 provider 接口，不是 contracts 仓 | ✅（把原文留白定盘，不改原文） |
| [p0-remediation-design.md](p0-remediation-design.md) §4 影响面清单 | 与 W1 各单落点逐行一致（`signal_provider.go` / `registry.go` / `plugins/*` / `engine.go` / `cache.go` / `adjustment_snapshot.go` / `state_store` / wasm 两文件） | ✅ |
| `tellurion:docs/TASKS.md` W1 施工序（L32-54） | 本表不改序号与依赖，只按序补落点 | ✅ |
| [repo-inventory.md](repo-inventory.md) §3 已裁边界 | 不涉及（`docs/SPEC.md` 归 core、`contracts/` 顶层数据契约文件不迁等均不变） | ✅ |

---

## 5. 不在本表范围（别顺手扩）

- **QT-17**（agent `ai.*` 数据自持）：与 W1 并行，但不在 W1 归属表。
- **QT-26**（W2，WASM `SignalProvider` 接线）：QT-11 的 WASM 腿转记在此（见 §2.2）。
- **QT-41 / QT-41b**（strategy 仓资产表与编译服务）：QT-22 只做 hash 算法，不碰表结构。
- **QT-15 的预算默认值调优**：性能基准另出报告，本表只登记落点。
- **QT-16 的 `contracts/kernel_modules.schema.sql`**：它是 core 内 K0 冻结数据契约资产。本表只登记「它是必改文件」，**不代表变更已获批准**——PK 改动 + 旧行 `shard_key=''` 迁移必须按 `tellurion:docs/TASKS.md`「维护本文件」节的冻结 schema 变更评审走。
- **paper 交易路径**（`pkg/live/paper_session.go`）：它**不吃 `SignalProvider`**——`PaperSession.Run(..., strat strategy.Strategy)` 收的是策略实例，信号产生由 `PaperSession.generateSignals`（:316-334）**平行重写**了一遍「优先流式 `BarHandler`、否则批式 `GenerateSignals`」。QT-14 ⑥ 边界只写回测 run 的生命周期，paper 侧不在 14 内；但这条平行实现是 QT-11/QT-14 落地的漂移风险：回测侧改成 per-run 实例后，paper 侧的实例来源若仍走 `Registry.Get` 就会重现「共享实例串状态」。paper 侧单独立单，不在 14 里顺手改。
- **`pkg/backtest/registry.go` 不存在**：策略注册表在 `pkg/strategy/registry.go`（`pkg/backtest` 只有 `engine.go` / `engine_daily.go` 等）。按任务单原文的 `registry.go:51-61` 去 `pkg/backtest` 找会扑空。
