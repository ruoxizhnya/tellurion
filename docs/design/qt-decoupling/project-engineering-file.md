# 项目工程档案：tellurion 解耦与模块化重构

## 1. 项目概况

- 目标：让 tellurion 从「核心仓强耦合 + agent 编译期绑定」演进为**端口化的模块化单体（Ports & Adapters）**，使应用层（UI / AI / analysis）与底层能力（回测、策略、风控、行情、因子、存储）之间边界清晰，并把「切成独立服务」变成**随时可做的能力**而非现在就付出的代价。
- 工程周期位置：**④ 评审与重构 → 回退到 ② 架构设计**（回退理由：现有模块边界问题需先在架构层定端口与契约，否则实现层改不动）。
- 驱动目标（用户原话）：更高拓展性（**廉价拓展**：加因子、加因子挖掘）、更高易维护性、**各模块可单独演进**（清楚边界）。
- 最后更新：2026-10-20

## 2. 技术栈与版本

| 项 | 值 | 核实状态 |
|---|---|---|
| 语言 | Go 1.25（go.mod 声明 go 1.25.0） | 已核实（仓库 go.mod） |
| HTTP | gin v1.10.0 | 已核实（agent go.mod） |
| 配置 | viper v1.18.2 | 已核实 |
| DB / 缓存 | PostgreSQL（52 表，6 schema）/ Redis | 已核实（storage migrate 注释、config） |
| 侧车 | Python nt-sidecar + NautilusTrader（HTTP /v1/backtest） | 已核实 |
| 前端 | Vue 3 + Vite + Naive UI（独立仓，纯 HTTP） | 已核实 |
| 跨仓绑定 | **现状**：agent `require core v0.0.0` + `replace => ../tellurion`（CI 用 `go mod edit -replace` 改写）；**目标**：`require contracts/kit` 的 tag，禁 replace（ADR-D29） | 已核实（2026-10-20 复核） |
| 部署形态 | 单机本地栈（web 8080 / data 8081 / analysis 8085 / ai 8086） | 已核实 |

## 3. 架构决策记录（ADR）

### ADR-D1：不做「模块拆成 HTTP 服务」，先做端口化模块化单体
- 场景：用户提出把 strategy / risk / backtest 各自独立成服务，应用层全 HTTP 化。
- 决策：**不拆进程**。改为在同一进程内定义 port 接口集，应用层只依赖 port；「可独立部署」以适配器的形式保留（本地实现 / 远程实现同签名）。
- 理由：① 回测内循环是 CPU 密集、每 bar × 每标的调用策略与风控，进程化把 ns 级调用变成 ms 级网络往返（数量级 10³~10⁶）；② 参考库微服务采用条件三条（团队≥10 + 边界稳定 + 运维到位）当前**一条不满足**（1 人 + AI agent、边界仍在 P2 演进、单机本地栈）；③ 共享数据库 + 网络调用 = 分布式单体，复杂度翻倍收益为零；④ 断点续跑、同参双跑对齐（dualrun K15）在进程内才有确定性。
- 代价：短期内仍是一份二进制，「按模块单独扩容」做不到；需要靠护栏维持边界纪律。
- **范围限定（2026-10-19 用户裁决，ADR-D24）**：本条的「不做 HTTP 服务」**只约束内核内部模块之间**（strategy / risk / backtest）；**agent ↔ core 这条应用边界已裁决走 HTTP（粗粒度）**——逐 bar 循环仍在 core 进程内，agent 只做粗粒度仪器调用。两者不矛盾。
- 状态：**提议（范围已由 D24 限定）**

### ADR-D2：契约外置为只依赖 domain 的 module `tellurion-contracts`
> ⚠️ **2026-10-20 订正（M1 / D29 §5 替代本条「只依赖 domain」口径）**：`domain` 已**并入 contracts 仓**（`contracts/domain`），module 由 5 回落 4；最终依赖方向 **`kit ← contracts(含 domain) ← core`，`agent → kit + contracts`（编译期）+ `HTTP → core`（运行期粗粒度）**，`contracts → kit`（仅纯工具包）允许（见 `repo-topology-and-naming.md` §3.1/§5）。本条正文保留追溯，护栏与 go.mod 实现一律按订正后方向（refactor-plan-review M2）。
- 场景：agent 与 core 现在是编译期 `replace` 单向绑定，core 改签名就打断 agent 构建。
- 决策：把 `RunSpec`/`ResultSpec`/`BacktestRunner`/`EngineRunner`/`MarketRouter` 及后续 port 抽成**只依赖 `quant-trading-domain` 的 Go module**（四契约引用 `domain.Signal`/`domain.BacktestResult` 等值对象；强行"零依赖"只能复制类型 → 两套真相），core 与 agent（及未来任何应用）都只依赖它。
- 理由：把「单向库依赖」升级为「双方依赖同一契约」，方向不变但耦合面从「整个 core 的导出面」收缩到「契约接口面」；契约可独立版本化；为将来真拆进程预留了天然的序列化边界。
- 代价：多一个仓库/版本要管；契约变更需走版本纪律（v1 冻结，新增 v2）。
- 状态：**提议**（依赖 ADR-D1）

### ADR-D3：策略的解耦方向是「插件/WASM 边界」而不是「HTTP 服务」（⚠️ 「策略独立」部分已由 ADR-D6 细化——资产服务 ✅ / 运行时 ❌ 两分，见 brainstorm-2026-10-19.md；WASM 插件边界部分仍成立）
- 场景：用户希望 strategy 独立，可对接 agent / UI / 内核。
- 决策：策略继续以**插件**形态存在（.so 热加载 + WASM L3a 沙箱），边界由 host API 白名单（ADR-029 前视隔离：`get_bar` / `get_cross_section` / `get_state` / `set_state` / `emit_signal`）强制；**禁止**策略运行时直连取数包（已有 `strategy_marketdata_isolation` 护栏）。
- 理由：策略被调用的频率与延迟要求决定了它必须同进程；WASM 边界已经提供了「独立版本、独立沙箱、可热插拔、可防前视」这些真正想要的独立性，且成本已付。研究态的批量扫描若需横向扩算力，走 **worker/队列**而非把策略变成在线服务。
- 代价：WASM 插件的表达力受 host API 限制，复杂策略仍需 .so 或内置插件通道。
- 状态：**提议**

### ADR-D4：双引擎可插拔升级为「能力声明 + 按能力路由」
- 场景：用户要「用哪个引擎都能工作」（Go 内核 / Nautilus 侧车）。
- 决策：现状已具备 `contracts.BacktestRunner` / `contracts.EngineRunner` + `EngineRegistry` + `MarketRouter`（fail-closed，未注册内核/市场显式拒绝）。升级点：让每个引擎声明**能力集**（支持频率 1d/1m/15m、市场 CN/US、日内特征、竞价撮合、集合竞价、期权等），路由按「RunSpec 需求 ∩ 引擎能力」匹配并 fail-closed，而不是按名字/市场硬编码。
- 理由：新引擎接入 = 实现接口 + 注册能力 + 加一条配置，不改路由代码；这是「廉价拓展」的直接载体。
- 代价：能力矩阵需要维护，能力声明与实际能力不一致会误导路由（需测试兜底）。
- 状态：**提议**

### ADR-D5：装配层从 `pkg/kernel` 外移，kernel 只留框架
- 场景：实测 `pkg/kernel/modules_analysis.go`（非测试）import 了 `pkg/backtest`，框架包反向依赖业务族；`pkg/kernel` 还依赖 live / risk / marketdata / storage。
- 决策：把 analysis 侧的装配代码移到 `cmd/analysis/wiring`（或独立 `pkg/wiring`），`pkg/kernel` 只保留：Module 接口、BootOrder 编排、生命周期、port 装配槽位。
- 理由：框架包必须零业务依赖，否则 kernel 不可复用也不可测试；装配外置后「换一套装配 = 换一个应用」成为可能（正是用户要的应用层独立）。
- 代价：一次性搬运 + 需要补装配守卫护栏（已有 `package_wiring_test.go` 可复用）。
- 状态：**提议**

## 4. 目标架构（端口化模块化单体）

```
┌───────────────────────── 接入层（HTTP / 进程外）──────────────────────────┐
│  UI(Vue3,nginx:8080)          external LLM          tushare / 多数据源      │
└───────────────┬──────────────────────┬───────────────────────┬───────────┘
                │ /api + JWT           │                       │
┌───────────────▼──────────────────────▼───────────────────────▼───────────┐
│ 应用层（可独立部署；只依赖 port，不依赖实现）                              │
│  analysis-service :8085   ai-service :8086   data-service :8081            │
│  （网关/编排/handlers）   （Copilot/实验）    （同步/摄取/因子计算）        │
│        └── 未来新增：factor-mining-service、screener-service… 同法组装 ──┘ │
└──────────────────────────────┬───────────────────────────────────────────┘
                               │ 依赖倒置：只 import 契约与 port
┌──────────────────────────────▼───────────────────────────────────────────┐
│ 契约层  tellurion-contracts（只依赖 domain）                            │
│  RunSpec · ResultSpec · BacktestRunner · EngineRunner · MarketRouter       │
│  + StrategyPort · RiskPolicy · DatasetPort · FactorPort · StoragePort      │
└──────────────────────────────┬───────────────────────────────────────────┘
                               │ 实现（进程内装配，可替换为远程适配器）
┌──────────────────────────────▼───────────────────────────────────────────┐
│ 能力层（可插拔，注册表驱动）                                               │
│  BacktestEngine: go-Engine │ nautilus Runner  （能力声明 + fail-closed 路由）│
│  StrategyRuntime: 内置插件 │ .so 热加载 │ WASM(L3a, 前视隔离)              │
│  RiskPolicy: 前置裁决（止损/止盈/regime/VaR/手数）— 可替换规则实现          │
│  MarketData(D1 规范层) · Factor(D3 定义/D4 登记) · Mining(D8 沙箱→治理→登记)│
│  Storage: PostgresStore(52 表, 唯一 DDL 真相) · RedisCache                 │
└──────────────────────────────┬───────────────────────────────────────────┘
                               ▼
              PostgreSQL :5432（6 schema）      Redis :6379
```

分层纪律（对照参考库 §7）：
- 按**业务能力**划分模块，不按技术分层；
- 依赖**单向**：应用层 → 契约层 → 能力层，**禁止反向**；
- **数据归属唯一**：所有写路径经 `storage.PostgresStore`（唯一 DDL 真相），能力层不各自建表；
- 跨模块调用**只走 port**，禁止摸别的模块内部结构体/表。
- **仓边界（ADR-D29）**：`kit` → `contracts`（含 `domain`）→ `core`；`agent → kit + contracts`（编译期）+ HTTP（运行期）。跨仓只认 tag，`replace`/`go.work` 不入库。

## 5. 接口约定（port 契约草案）

| Port | 方法（草案，签名以实现为准） | 幂等/语义 | 现状 |
|---|---|---|---|
| `BacktestRunner` | `RunBacktest(ctx, strategyName, stockPool, startDate, endDate) (*domain.BacktestResult, error)` | 同参数同结果（确定性回放） | **已有**（`contracts/runner.go:54`） |
| `EngineRunner` | `RunBacktest(ctx, BacktestRequest) (*BacktestResponse, error)` | walkforward/batch/job 依赖此接口以打破 import 环 | **已有**（`contracts/contracts.go:366`） |
| `MarketRouter` | 市场 → 内核名，未注册返回哨兵错误 | fail-closed，禁止回退 | **已有**（`contracts/marketroute.go`） |
| `StrategyRuntime`（新增） | 加载/驱动/检查点；经 WASM host API 交互 | 前视隔离由 host 白名单强制执行 | 部分（WASM host + PluginLoader 已有，未收口为 port） |
| `RiskPolicy`（新增） | 订单前置裁决 verdict | 纯函数式、可替换 | 部分（`risk.RiskManager` 已是裁决点，未接口化） |
| `FactorPort` / `MinerPort`（新增） | 因子定义注册、计算、挖掘算法注册 | 注册表驱动、版本哈希 fail-closed | 部分（D3/D4 注册表 + mining D8 已有） |

## 6. 已知风险

| # | 风险 | 等级 | 兜底 |
|---|---|---|---|
| 1 | 契约外置后版本漂移（core / agent / contracts 三方） | 高 | 契约 v1 冻结 + 新增 v2；CI 校验三方版本矩阵 |
| 2 | port 化过程出现「接口膨胀、实现只是转发」的空壳 | 中 | 每个 port 必须有 ≥2 个实现（真实实现 + mock 不算）才允许存在 |
| 3 | 装配层外移时破坏 kernel BootOrder 语义 | 高 | 复用 `package_wiring_test.go` + `module_lifecycle_callback_test.go`，搬运前后各跑一次 |
| 4 | 能力声明与实际引擎能力不一致，路由选错引擎 | 高 | 双跑对齐（dualrun K15）扩展为「能力一致性回归」，fail-closed 不回退 |
| 5 | 护栏不足导致边界二次腐化（已知洞：strategy → pkg/data 无护栏） | 中 | 补 `strategy_data_isolation` 护栏（与 marketdata 同款 AST 扫描） |
| 6 | 过度设计：为不存在的扩容需求提前建远程适配器 | 中 | 「现在不要做」清单（见 §7），远程适配器等真有部署诉求再写 |

## 7. 待办（分期演进路线 · 绞杀者模式，不推倒重来）

**P0 · 契约与端口（1-2 周）**
1. 抽出 `tellurion-contracts` module（只依赖 domain；RunSpec/ResultSpec/BacktestRunner/EngineRunner/MarketRouter）——**代码直接落 `tellurion-contracts` 仓**，core 以 `require` 伪版本取用（ADR-D31 / QT-05）。
2. core 与 agent 改为依赖该 module；agent 的 `replace` 保留但语义变为「本地开发便利」。
3. 定义 `StrategyRuntime` / `RiskPolicy` / `FactorPort` / `MinerPort` 四类 port（先写接口 + 现有实现适配，不改调用点行为）。
4. 补护栏：应用层禁止直接 import 能力层实现包（AST 扫描，与 `canonical_layer_test.go` 同款）。

**P1 · 装配与插件收口**
5. 装配层从 `pkg/kernel` 外移到 `cmd/analysis/wiring`，kernel 只留框架（ADR-D5）。
6. 策略访问路径全部收口到 WASM host API / 插件接口；补 `strategy_data_isolation` 护栏（堵 strategy→data 的洞）。

**P2 · 能力化路由**
7. 引擎能力声明（频率/市场/特性）+ 路由改为「需求 ∩ 能力」匹配，fail-closed。
8. 风控 Policy 对象化（规则可配置、可替换实现），保持订单路径进程内。
9. 回测状态/结果持久化 port 化（为将来拆进程留口，不现在拆）。

**P3 · 条件触发才做（现在不要做）**
- ❌ 现在不要把 strategy / risk / backtest 拆成 HTTP 服务
- ❌ 现在不要引入 MQ / K8s / 服务网格
- ❌ 现在不要给 core 拆库分表
- ✅ 触发条件（满足其一再评估）：出现多机部署诉求 / 批量扫描算力常态化打满且需横向扩 / 团队扩到能按服务划分 / 某模块发布节奏明显被整体阻塞

## 8. 现状耦合实测基线（供后续对比）

数据来源：2026-10-19 对非测试 Go 文件的全仓 import 扫描。

- 顶层依赖数最大：`backtest(14)` > `live(10)` > `bootstrap(10)` > `kernel(8)` > `strategy(7)` / `data(7)`。
- fan-in 最高：`domain(13)`、`storage(7)`、`errors(7)`、`marketdata(6)` —— 底层稳定、无倒挂（健康）。
- 已确认的边界松动点 4 处：① `strategy → pkg/data`（仅 `plugins/sentiment.go`，无护栏）；② `kernel → backtest/live/risk`（装配代码在框架包内）；③ `storage → pkg/sync/types`（仅类型引用）；④ `domain → fees`（`execution.go` 成本内联）。
- 已做对的部分（要保留并强化）：contracts 冻结契约、EngineRegistry + MarketRouter fail-closed、`ai_boundary` 单向依赖护栏、WASM 前视隔离（ADR-029）、expression/indicator 分层、storage 唯一 DDL 真相。

---

### ADR-D17：产品需求可实现，走 M0/M1/M2 三里程碑（进入执行）

- 场景/上下文：用户问「你否决几乎所有的建议……这个产品需求实现不了？」。产品需求 = **运行时动态加入 AI 生成的复杂状态机策略**，不重启引擎；源头在策略服务、引擎只持缓存副本；安全沙箱；结果可复现。
- 决策：**判定可实现，进入执行**。按 M0（证明状态机能跑，2-3 天，不建服务）→ M1（真相源 + 缓存，1-2 周）→ M2（AI 闭环 + promote 闸门，2-3 周）三里程碑推进。
- 理由：约 70% 基础设施已存在（wazero v1.12.0 / OnBarSession 逐 bar 协议 / 前视 trap / env 8 函数白名单 / 64MB 内存上限 / checkpoint.go 日期边界断点续跑 / MarketRouter fail-closed / factor_definitions.version_hash 治理样板 / L3b external_signals 全链路），剩余是胶水代码 + 一条编译链路。安全不需要 Gas（wazero v1.12 无此 API，已核实模块源码），四层已够：env-only 能力隔离 + `WithCloseOnContextDone` 可中断 + 内存上限 + **人工 promote 闸门**。
- 关键杠杆（性能）：**数据模型决定调用次数**——单实例 + 横截面 = 727 次 `on_bar`；per-symbol = 约 363 万次。市场级状态机选前者则性能问题消失。明确禁止「批量喂全序列」（倒退回前视漏洞）。
- 代价/后果：需补 G1（`WithCloseOnContextDone(true)`，含周期检查的性能开销，须基准量化；超时后 `sys.ExitError` 且模块关闭 ⇒ 需重建会话语义，与状态机恢复路径打通）。性能数字待实测，不预设结论。
- 状态：**提议（进入执行）**，关联 ADR-D13 / D14 / D15 / D16，产物 `implementation-plan-mvp.md`

#### 待办增补（2026-10-19 晚 · 执行线）

| 编号 | 事项 | 工作量 | 验收 |
|---|---|---|---|
| G1 | `wazero_runtime.go:41` 补 `WithCloseOnContextDone(true)` + 恶意 `while(true)` 测试 + 开/关性能基准 | 几小时（P0） | 死循环能在超时内被终止，进程不被挂死 |
| G2 | `SignalProvider` 的 WASM 实现（接 `OnBarSession`） | 1-3 天 | 能跑通一次真实回测 |
| G3 | `EmittedSignal → domain.Signal` 转换 | 半天 | 字段映射 + 校验 |
| G4 | 装 TinyGo（Windows 二进制 / 或 Docker） | 1 小时 | 编出 env-only `.wasm`，`ValidateImports` 通过 |
| G5 | 策略服务：`strategies` 加列 + `strategy_artifacts` + resolve/artifact 两端点 + 引擎缓存 | 1-2 周 | 服务停机后已缓存 hash 的回测照跑且结果一致 |
| G6 | AI 生成闭环（Go 子集 + 静态校验 + promote 闸门） | 2-3 周 | prompt → 源码 → hash → 回测结果全程留痕 |
| G7 | **Provider 生命周期与并发**（RunScopedProvider：BeginRun/EndRun + session map + finalize/Close） | 1-2 天（P0） | 并发两笔回测互不串状态；finalize 被调用 | 
| G8 | **Host 数据模型 v2**（BarContext per-symbol OHLCV + field 枚举 + 双 host 模块版本） | 2-3 天（P0） | v1 artifact 与 v2 artifact 均能实例化；OHLCV 可取到 | 
| G9 | **Action 枚举冻结**（0..3 + Strength 语义 + domain.Signal 映射） | 半天（G3 前置） | 映射表进 ABI 文档，SDK 与实现一致 |
| G10 | **策略参数投递路径**（`BacktestRequest.StrategyParams` + 定义默认⊕run 覆写 + 生效参数落 run_artifacts；`ReloadFromDB` 零调用点的事实要一并处理） | 1-2 天（P0，M0 前置） | 同一策略不同参数并发两笔回测各自生效；生效参数可从历史记录还原 |
| G11 | **策略 per-run 实例**（`Registry` 注册 factory / `NewInstance`；`SignalStrategy.cursor` 不再跨 run 存活，= BE-21） | 1-2 天（P0，K15 对拍前置） | 同一策略连续跑两次结果一致；并发两笔不串状态 |
| G12 | **ABI 面补齐**：`get_date2()`（YYYYMMDD）+ `emit_signal` 越界/枚举/强度校验 + 每 bar 发射上限 | 半天（P0，与 G8 同批） | 状态机可读日期；非法 signal 一律 trap |
| G13 | **内存布局冻结**：`MaxParamsBytes=4096` / state scratch 64 KiB / 单实例 State ≤ 4 MiB（宿主侧闸） | 半天（P0） | params 与 state 互不覆盖；超限 fail-closed |
| G14 | **引擎级 run 缓存隔离**（`warmFactorCache` / `warmFundamentalsIfNeeded` 在 RunBacktest 内替换共享 Engine 字段，= P0-12） | 2-3 天（P0） | 并发两笔（不同 universe/区间）因子与基本面互不覆盖 |
| G15 | **信号顺序规范化**（engine 应用前稳定排序；`value_momentum.go:366` range map 已证非确定） | 1 天（P0，K15 前置） | 同一输入两次跑信号序一致；Go/WASM 同逻辑结果一致 |
| G16 | **状态 schema 版本**（state 带 `state_schema_version` + artifact_hash，恢复时校验） | 半天（P0） | 换 artifact 后恢复旧检查点被拒绝而非误读 |
| G17 | **L1 OHLCV 缓存不再跨 run 累积**（下沉 per-run，或按字节预算 + LRU 驱逐；现状 merge 且无清理） | 1-2 天（P0） | 50 个不同票池 run 后内存回落基线；重复 run 结果不变 |
| G18 | **复权锚点按 run 区间切片**（`fullBars` 先切 `[Start,End]` 再算 anchor/digest） | 半天（P0） | 长区间→短区间→重启→短区间，三次一致 |
| G19 | **同 bar 同 symbol 多信号语义冻结**（推荐 trap） | 半天（P0） | 相反 action 两条 → 确定性行为 |
| G20 | **`Scope.RunUniverse`**（run 级稳定）+ `Universe` 降级为当日名单 | 1 天（P0） | Universe 增减 5 天，symbol_idx 不变 |
| G21 | **symbol scope 分片与预算**（并发信号量 + 单 run 512 MiB 软预算 + 指标） | 2 天（P0，M0b 前置） | 5000 标的跑完不 OOM；预算调小仍能跑完 |
| G22 | **错误分级 / fail-closed 落地**（`ErrDataGap` vs `ErrStrategyFault` vs `ErrContractBreach`；策略故障终止 run，不再「跳过当天」） | 1-2 天（P0，与 G7 同批） | 每 50 天 trap 一次的 wasm → run=failed；数据缺口 → ~~degraded~~ **invalid**（+ `data_gap_skipped`，refactor-plan-review C1 订正：映射进冻结三值，不引入第四值，见 `tellurion:docs/TASKS.md` QT-10 ③-④）且 invalid_reasons 有记录 |
| G23 | **成交时点冻结**（`FillTiming = next_open \| same_close`，进 run_spec_hash；先出差异报告再定默认） | 1 天 + 差异报告（P0，契约冻结） | 两口径差异可解释；K15 遇口径不一致报「口径不符」而非「结果不一致」 |
| G24 | **检查点身份**（PK 加 `shard_key`；信封加 `run_identity_hash`=universe+scope+shard+params） | 1-2 天（P0，与 G16 同批） | 5000 分片 5000 行；换票池恢复被拒且点名差异项 |
| G25 | **终止重建的重放语义**（`replay_mode=state_only` 默认；重放期 emit 不落 Signals；terminated 不调 finalize） | 1-2 天（P0，与 G1/G16 同批） | 注入 trap 后成交逐笔与无 trap 一致 |
| G26 | **host 区所有权**（模块导出 `__host_params_ptr/__host_scratch_ptr/size`；缺失 fail-closed） | 1-2 天（P0，abi v2 同批） | 缺符号/越界/重叠均被拒；哨兵全局变量不被覆盖 |
| G27 | **`run_spec_hash` 与 `dataset_snapshot_hash` 单一定义**（canonical 全字段 + 分量留痕 + 向量测试） | 1-2 天（P0，契约冻结） | 改费率/滑点/票池/日历/复权/内核任一 → hash 必变 |
| G28 | **`artifact_hash` 为执行真相**（`strategy_ref` 三形态；promote 不覆盖 artifact 行） | 1 天（P0，与 G5 同批） | 同源码两工具链 → 两 artifact 可分别追溯 |
| G29 | **run 级资源准入**（并发上限 + 有界队列 + 单 run/进程内存预算 + 指标） | 1-2 天（P0，与 G17/G21 同批） | 10 个全市场 run 并发不超预算；超限排队/拒绝不 OOM |
| G30 | **仓位表达**（`emit_target2(symbol_idx, weight)`，政策上限优先；与 emit_signal 混用 trap） | 1 天（P0，M0b 前置，abi v2 同批） | 分层加仓 Go/WASM 逐位一致 |

**M0 验收（最硬的一条）**：同一个状态机的 Go 内置实现 vs WASM 实现，回测结果**逐位一致**。

---

### ADR-D18：服务拆分统一模式 + 四个服务边界（数据 / 风控 / 策略 / 分析）

- 场景/上下文：用户提出 ① 拆数据服务、回测/实盘无状态（数据与策略从外部供给）② 摘风控为 external risk control（支持实盘）③ 策略服务 ④ 还有什么可摘。
- 决策：
  1. **统一模式**（三服务共用）：真相源外置 + 内容寻址副本 + 运行期本地执行 + **内循环零网络**。回测结果记录三 hash：`strategy_ref hash` / `dataset_snapshot_hash` / 风控规则 hash。
  2. **「无状态」精确含义**：内核有状态计算、**无持久化状态**（不拥有业务表）；数据/策略在 run 前一次性加载，run 期间零网络。**禁止**逐 bar 远程取数（内循环网络化）。
  3. **风控必须两层**：外部服务 = 规则真相源 + 跨策略/账户聚合监控 + 熔断指令下发（异步）；进程内 `RiskPolicy` 对象 = 同步裁决（逐 bar/逐笔，零网络）。实盘与回测调用**同一 RiskPolicy**，这是回测可信与 K15 对齐的前提。`compliance` + `alert` 并入风控服务的聚合监控层。
  4. **可摘候选排序**：数据 → 策略 → 风控 → **分析/报表（性价比最高，正好补 BE-05/06 缺口）** → 回测编排/worker（算力出口）→ 券商/数据源网关（最后，实盘延迟敏感）。
  5. **不摘**：回测内核（contracts/execution/tracker/metrics/auction/state，热路径）、domain/expression/indicator（是库）、storage 连接与 kit（共享基建）。
- 理由：判据 5 条 = 数据归属唯一 / 独立发布 / 扩容诉求不同 / 边界稳定 / **不在内循环热路径**（硬闸门）。参考库 §七/§八：先拆数据边界再拆服务；共享数据库的伪服务 = 分布式单体。
- 代价/后果：1-2 人团队**现在不要真拆进程**——先把边界收口成 port + 服务接口，进程拆分等触发条件（工程档案 §7 P3）。风险见方案文档 §7。
- 状态：**提议**，产物 `service-extraction-plan.md`，关联 ADR-D1/D13/D14/D17

#### 分期（X 线）
X0 三 hash 记入结果 → X1 数据服务（数据归属最重，其他都依赖它）→ X2 策略服务 → X3 风控服务（两层）→ X4 分析服务 → X5 编排/worker → X6 券商/数据源网关。

---

### ADR-D19：数据服务契约与数据归属（DDD 分库 + 统一数据协议 + connector/aggregator）

- 场景/上下文：用户补充 ① 数据库归属各自服务（可同一实例，每服务自己的 db，遵循 DDD）② 数据服务供给回测的数据遵循**统一内部数据协议**③ 数据服务可多服务聚合，但要有 **aggregator** 对外提供数据 ④ 数据服务群有多个数据源 connector，每个 connector 归一化后入库。
- 决策：
  1. **数据归属**：同一 PG 实例、每服务一个 schema，DDL 唯一 owner。数据服务=`market`+`ingest`；策略=`strategy`；风控=`risk`；分析=`analysis`；实验=`ai`；编排=`run`；**内核无业务表**。跨服务只读走接口/视图，禁止第二写者。`strategy_state`/`backtest_jobs` 应从内核迁到编排服务（X5），内核走 port。
  2. **统一内部数据协议** = `market.UnifiedDataPoint` + `domain/market` 语义层，**冻结进 `tellurion-contracts`**。七要素：标的标识 / 时间语义（含 **visible_at 必填**）/ 频率语义 / 行情字段（Volume 单位=股）/ 公司行为 / 质量标记 / **dataset_snapshot_hash（待补 X0）**。
  3. **visible_at 是防前视的第二道防线**（第一道是沙箱 `get_bar(t_offset>0)` trap）：防的是"数据本身泄漏"（财报用报告期截止日当锚点、后复权价格等）。`contract.go` 注释原文红线："漏声明的时间语义 = 数据泄漏，且回测曲线照样漂亮，事后极难发现"。
  4. **connector 责任精确化（不推翻用户意图，防口径漂移）**：字段映射 + 基础校验 → connector 内（Adapter）；**语义归一（时间语义推导/频率聚合/复权口径/单位换算/去重）→ 共享 Normalizer 函数库**。现有代码正是此形态（`type Normalizer func(...) UnifiedDataPoint` 可注入）。
  5. **aggregator 职责**（不只是转发）：PIT 过滤 · 频率聚合 · 票池解析 · `dataset_snapshot_hash` · 缓存。`marketdata.Router`（D1 唯一入口）就是 aggregator 雏形。
- 理由：**这套设计代码已实现 80%**（实测 `pkg/data/source/`：12 个 connector + `ConnectorDeclaration` 必填 TimeSemantics/FrequencySpec + fail-closed 拒收 + `ETLPipeline`/`Normalizer`/`ValidatePoints`/`Deduplicate`；PG 已分 ingest/market/quant/research）。本轮是**冻结成跨服务契约**（进 contracts），不是重新设计。
- 代价/后果：需补 `dataset_snapshot_hash`（X0）与 `ErrVisibleAtMissing` 等错误码；`strategy_state` 归属迁移。
- 状态：**提议**，产物 `data-service-contract-and-ownership.md`，关联 ADR-D2/D7/D18

#### X1 细化分期
X1a 协议冻结进 contracts → X1b aggregator 面（PIT/频率/票池/快照 hash）→ X1c connector 收口（12 个全走 Declaration + 共享 Normalizer）→ X1d schema 归属迁移 → X0 快照 hash 记入结果。

---

### ADR-D20~D23：其余四个摘出服务设计固化（风控 / 分析 / 编排 / 交易网关）

- 场景/上下文：用户要求「把其他的需要摘出来的服务也固化成设计文档」。ADR-D18 X 线中数据服务（D19）与策略服务（D13/D14 + asset-api）已固化，本轮补齐剩余四个。
- 决策（四份设计文档，各自独立成篇）：
  1. **ADR-D20 风控服务（X3）** `risk-service-design.md`：两层不可合——外部服务=规则真相源（`risk.rule_defs/revisions/snapshots`，version_hash 沿用 factor 样板）+ 聚合监控（compliance 异常交易/减持/大额报送 + alert 六检测器直接并入）+ 熔断下发；进程内 `RiskPolicy` = 同步裁决（零网络，实盘回测同一实现）。`risk_events` 表待建（OrderIntent 注释已指向但未落地）。CheckOrder 永不走 HTTP。
  2. **ADR-D21 分析服务（X4）** `analytics-service-design.md`：纯函数+只读，性价比最高。现有 metrics 8 函数 + compare/correlation/html export 可直接搬；新建补 BE-05 基准对比、BE-06 归因/敏感性/蒙特卡洛（seed 必填留痕）。schema=`analysis`，只按 run_id 拉结果，零 import 引擎。
  3. **ADR-D22 编排/worker 服务（X5）** `orchestration-service-design.md`：job/batch/walkforward + worker 池 = 批量扫描算力出口。关键事实：`BatchEngine` 已按 `contracts.EngineRunner` port 注入（摘出成本低）。schema=`run`（backtest_jobs/walk_forward_reports 迁入 + run_artifacts 记三 hash=X0 载体）。修 BE-03（编排移出能力层）。
  4. **ADR-D23 交易通道网关（X6）** `broker-gateway-design.md`：XTPTrader+MockTrader=port 两实现（判据已满足）。归一化 DTO 进 contracts，通道级校验（柜台规则）vs 风控校验分界修正（xtp.go:387 混用需拆）。延迟敏感，当前进程内 port，独立进程等多账户/高可用触发。数据源 connector 网关已在 D19（X1）覆盖，本文只管交易通道。
- 统一纪律（贯穿四份）：每服务 schema DDL 唯一 owner（risk/analysis/run，同 PG 实例）；跨服务只读走 port/视图；热路径（逐 bar/逐笔）零网络；结果留痕 hash（三 hash + seed）。
- 状态：**提议（设计固化）**，关联 ADR-D18/D19/D11/D13~D17

#### X 线文档覆盖现状
| 阶段 | 服务 | 文档 |
|---|---|---|
| X1 | 数据服务 | data-service-contract-and-ownership.md（D19） |
| X2 | 策略服务 | strategy-asset-service-api.md + 4 篇策略线 |
| X3 | 风控服务 | risk-service-design.md（D20） |
| X4 | 分析服务 | analytics-service-design.md（D21） |
| X5 | 编排/worker | orchestration-service-design.md（D22） |
| X6 | 交易网关 | broker-gateway-design.md（D23） |

---

### 2026-10-19 晚 · 设计集融入审查记录（非 ADR，审查留痕）

对 qt-decoupling 设计集（22 文件，约 2800 行）做了与正式裁决的一致性审查，结论：**架构主体站得住，1 处真冲突 + 3 处小毛病，已直接修正**。

| # | 发现 | 处置 |
|---|---|---|
| 1 | 🔴 D12/BE-18 把 `.so` 写成「可信一等公民通道」，与 **ADR-024（Accepted）「不做 `plugin.Open`」** 直接冲突 | ✅ 已修正：`.so` = 存量遗留（冻结不扩展、逐步退役），strategy-loading-externalization.md 5 处 |
| 2 | 🟡 D18 service-extraction-plan 未与 **ADR-027 服务数 5→7** 对账 | ✅ 已补对账注：X 线 = 能力边界清单，非最终拓扑，升格时以 ADR-027 为基准 |
| 3 | 🟡 kernel-interface-map 未对齐 **ADR-031 四契约**命名（SignalSpec 缺席） | ✅ 已补术语对照：RunSpec/SignalSpec/ResultSpec/EngineRunner ↔ 本清单 |
| 4 | 🟡 `tellurion-docs:docs/README.md` 的 `design/kernel/` 导航是**断链**（真身在本仓 docs/design/kernel/） | ✅ 已修复链接并登记 qt-decoupling 导航行 |

**审查确认无问题的对齐点**：端口化模块化单体 ↔ ADR-027「先进程内模块化、服务拆分可选在后」；双内核/四契约 ↔ ADR-031；WASM 轨道 ↔ ADR-028/029；fail-closed 语义全集一致；「内循环零网络」贯穿一致。

**长效文档融入状态**：`tellurion-docs:docs/README.md`（唯一入口）已登记 ✅ · `AGENTS.md` 路由行已有 ✅ · qt-decoupling/README.md 索引已有 ✅ · 升格路径 = 裁决后按 tellurion-docs:docs/adr/ 流程成 ADR-NNN（先例 kernel-pluggable → ADR-031）。


---

### ADR-D25：三项结构性裁决落档（J 状态机形态 / I 实例模型 / B 版本治理）

- 场景：README 待裁决清单的 I / B / J 三项阻塞正式升格，用户 2026-10-19 按建议组合拍板。
- 决策：
  1. **J（状态机形态）**：市场级/横截面**优先**，**混合为目标**。第一批 PoC = 市场级单实例状态机（727 次 `on_bar`，性能无风险）；后续补单标的持仓状态机；最终形态 = 市场级门控 + 每标的执行状态机。
  2. **I（实例模型）**：**混合（C）**。host API **abi v2**：`get_bar(symbol_idx, t_offset, field_id)` 增加 symbol 维度；策略 artifact 必填 `scope: market|symbol`（未知值 fail-closed）；`market` = 单实例+横截面（默认），`symbol` = 按标的分片实例。破坏性变更**只做一次**；`abi_version=1` 旧策略按 symbol scope 兼容。
  3. **B（版本治理）**：`strategy_ref` **允许 latest 作语法糖**，但**解析必须发生在提交边界并立即固化为 hash**；执行层与结果层永远只认 hash；CI / dualrun K15 / 正式台账直接传 hash；结果必须记 `strategy_ref{id, hash}`（BE-20）。
- 理由：market scope 调用次数少 5000 倍且横截面决策天然；协议一次做对避免 ABI 二次破坏；latest 语法糖保交互体验、提交固化保可复现。
- 代价/后果：ADR-029 §3 需修订（草案已写：`adr-029-amendment-host-api-v2.md`）；NT 内核若实现必须直接按 v2；market scope 的 64MB 共享内存需 PoC 实测；`domain → fees` 解耦仍是 D24 前置。
- 状态：**已裁决（用户 2026-10-19）**，关联 D14/D16/D24，产物 `adr-029-amendment-host-api-v2.md`


---

### ADR-D26：两项裁决落档（L 参数两层 / M Strength 语义）

- 场景：README 待裁决 L / M 阻塞参数与信号语义，用户 2026-10-20 同意建议。
- 决策：
  1. **L（参数归属）= 两层**：`params_defaults` 属策略定义、进 `version_hash`；`strategy_params` 属 `BacktestRequest` 覆写、进 `run_spec_hash`；生效参数 = 默认 ⊕ 覆写，落 `run_artifacts.params_json + params_hash`；执行期禁止读 `strategies.params`；未知键 400。
  2. **M（Strength 语义）= 信号强度/置信度**，finite ∈ [0,1]，越界 trap；**不定义为目标权重**（现有 Go 策略按"强度 × baseWeight"消费，改为权重会同时破坏 Go 侧与 K15 口径）。
- 理由：参数分层保住"改默认=改版本"的可复现性，同时让参数扫描（sweep）不产生版本爆炸；Strength 维持既有语义避免跨引擎口径漂移。
- 代价/后果：WASM 策略清单必须带 `manifest.params[]`（name/type/min/max/required）做类型与范围校验（无 Go 类型系统可用）。
- 状态：**已裁决（用户 2026-10-20）**，关联 D25、abi v2 §2.2/§7.6、G9/G10

---

### ADR-D27：P0 第二轮深度审查（旧项闭环核对 + 新增 9 项 P0）

- 场景/上下文：用户要求「确保发现的所有 P0 都在设计上做了合理应对，不光是这一次，之前所有；然后深度审查还有没有遗漏的 P0」。
- 结论：
  1. **覆盖度**：`p0-remediation` 的 20 项与 BE 系列 🔴 项**全部有设计落点**；其中 **5 项半闭环**（A3/A4/A6/A9/B4）、**1 项待裁决**（A12）。半闭环的共同特征：写了方向，但**没定成可测的契约**。
  2. **新增 9 项 P0**（详见 `p0-round2-review.md`）：
     - 今天就存在：**N1 策略错误被降级为跳过当天**（fail-closed 总漏斗）、**N2 成交时点/价格语义从未冻结**（同 bar 收盘决策+同 bar 收盘成交）、**N6 可复现留痕不闭合**（`run_spec_hash` 只引用未定义、三 hash 覆盖不足、`dataset_snapshot_hash` 两处定义互相矛盾）、**N7 双哈希解析漏洞**（同源码重编 = 同 ref 不同字节）；
     - 契约冻结前必须定：**N5 host 固定偏移无「模块预留」契约**、**N9 ABI 表达不了目标仓位**（与 M0b「分层加仓」验收冲突）；
     - M0b/并发前：**N3 检查点身份维度不足**（PK 缺 shard、信封缺 universe）、**N4 终止重建的重放会重复应用信号**、**N8 run 级资源准入缺失**（A10 的 per-run 缓存放大该风险）。
- 处置：全部落为工单 **G22~G30**；依赖顺序新增 **wave-0 契约冻结**（N2/N5/N6/N7/N9 + A12 存量扫描，只写文本与差异报告、不改行为），排在 WAVE-1 执行正确性之前。
- 代价/后果：wave-0 会让"开工时间"看起来推迟 3-5 天，但换掉的是"artifact 出厂后再改 ABI / 基准全量重跑 / 同参复现不成立"三类返工。
- 状态：**审查结论（设计补充基线）**，关联 D17/D25/D26、`p0-remediation-design.md`、`p0-round2-review.md`

---

### ADR-D28：三项裁决落档（N2 成交时点 / N9 仓位表达 / A12 重复信号）

- 场景：`p0-round2-review.md` 的 N2/N9/A12 是 wave-0 契约冻结的阻塞项，用户 2026-10-20 按建议全采纳。
- 决策：
  1. **N2 成交时点**：`SignalSpec.FillTiming` 必填（`next_open` | `same_close`，无默认）；终态 `next_open`（T 日收盘决策 → T+1 开盘成交）；`same_close` 保留为兼容并标 `known_bias: same_close_fill`；**先出两口径差异报告再切默认**，禁止为保基准而选口径；`fill_timing` 进 `run_spec_hash`；NT 侧必须同口径，K15 前断言不一致即 `ErrFillTimingMismatch`。
  2. **N9 仓位表达**：新增 `emit_target2(symbol_idx, weight∈[-1,1])`＝目标权重（-1 不动 / 0 清仓），`qty = floor(|w|×equity/price/lot)×lot`；`RiskPolicy` cap 优先并计 `sizing_clamped_total`；与 `emit_signal` 同 bar 同 symbol 混用 → trap；结果标 `sizing_source`。**不推翻 D26 的 Strength 语义**（新增函数名，符合 B3 增量命名）。
  3. **A12 重复信号**：先做存量 14 策略 × 3 区间扫描；零重复 → 直接 trap；有重复 → 先 `close` 优先合并 + 同向 strength 求和 clamp + `duplicate_signals_merged` 计数，trap 留作下一版开关。
- 理由：三项都属"artifact 出厂后改不动 / 历史基准无法回溯"的契约面，必须在 wave-0 一次定死；N9 选新增函数而非改 Strength，是为了不动已裁决语义、避免 K15 口径整体漂移。
- 代价/后果：`next_open` 切换会让历史基准整体位移一个 bar，必须留差异报告；`emit_target2` 增加一套 sizing 路径（须与 policy cap、手数取整、K15 对拍同时验）；A12 的合并分支会保留一段"临时语义"，等存量迁移完才能收成 trap。
- 状态：**已裁决（用户 2026-10-20）**，关联 D26、`p0-round2-review.md` §8、`adr-029-amendment-host-api-v2.md` §8


---

### ADR-D29：分仓拓扑与命名（repo = 模块/服务，边界由 contract 约束）

- 场景：用户要求「按分仓来」推进——目标是**让单个 agent 能独立完成单个 repo 的任务**，而不是「部署节奏不同才拆仓」。此前 D24 裁了五 module（`domain` 独立），但从未回答「module 住哪个 git 仓、仓怎么命名、什么时候才配拥有一个仓」。
- 实测差距（2026-10-20）：
  - core 仓 **`git tag` 数量 = 0** ⇒ agent 只能写 `v0.0.0`；
  - agent `go.mod:81` `replace => ../tellurion`，CI 里 checkout core 并改写 replace ⇒ **单仓 checkout 构建不出来**；
  - agent import core 的 **20 个包 / 37 个文件**（`domain` 48 处、`storage` 14、`strategy` 11、`expression` 10…）⇒ 删 replace 的前置是「agent 不再 import core 实现包」；
  - `agent` / `ui` / `docs` 三仓**均无 `AGENTS.md`**，`docs` 仓还无 CI、无 TASKS。
- 决策：
  1. **仓 = 模块/服务，一个仓一个 go module**；跨仓只通过 `tellurion-contracts` 的 tag 交互。终态 6 仓（`tellurion` / `-contracts` / `-kit` / `-agent` / `-ui` / `-docs`），X 线服务真起进程时再 +6。
  2. **`domain` 并入 contracts 仓**（子包 `contracts/domain`）——**修正 D24 第 1 条**。依据：`pkg/domain` 被 core 306 文件、agent 48 文件使用，agent 用到的全是跨边界 DTO（`BacktestResult`/`OHLCV`/`Trade`/`Factor*`/`Direction`）；它与 contracts 变更节奏同步，独立成仓只会让一次改动变两次发版。module 数由 5 回落为 4：`kit → contracts → core`。
  3. **`pkg/backtest/contracts`（ADR-031 四契约，2113 行，仅依赖 domain+fees）迁入 contracts 仓 `engine/` 子包**；`fees` 拆为「数据形状进 contracts / 计算留 core」。
  4. **`kit/db`**（仅 connection/migrate/DSN）——K 裁决落地，使 agent 能自建 `ai.*` 而不借 core 的 store。
  5. **五条 repo autonomy 不变量**（R1 单仓可构建 / R2 单仓可自证 / R3 有 AGENTS / R4 有 TASKS / R5 契约是版本依赖），由 `tools/check_repo_autonomy.py` 机械检查。
  6. **迁移阶梯 S0~S3**：S0 打 tag + 补自治三件套 → S1 本仓内建 `contracts/` `kit/` 子 module（go.work 不入库）→ S2 拆仓 + core 改 tag + 删 replace → S3 agent 去 replace（依赖 QT-50 HTTP 化）。

> ⚠️ **第 6 条的 S1 形态已被 ADR-D31 替换（2026-10-20，用户选 Plan B）**：S1 不再在 core 内建 `contracts/` `kit/` 子 module，改为**代码直接落地两个新仓 + `require` 伪版本**。S0 / S2 / S3 与「S1 排在 W0 之后」不变。原文保留以追溯决策链。
- 理由：仓是 **agent 的工作上下文单位**；只有「单仓可构建 + 契约以 tag 存在」，一个 agent 才可能不读别的仓源码就完成本仓任务。**S1 排在 W0 之后**，因为 S1 的动作本身就是「把契约形状定下来」，属 W0 产出，做一次而不是两次。
- 代价/后果：① `pkg/domain` 306 处 import 路径机械替换；② `fees` 需拆类型/计算两半；③ 拆仓后「顺手改另一边」的路没了，边界错误会以「契约里塞实现」的形式沉淀 → 由跨仓依赖五规则（C1~C5）与契约测试兜底；④ agent 的单仓自治要等 QT-50，**不是现在的起点**（诚实登记，不写成已达成）。
- 状态：**已裁决（用户 2026-10-20 确认三处判断：domain 并入 contracts / S0 打 tag 但保留 replace / S1 排在 W0 后）**；替代 D24 第 1 条，D24 第 2/3 条不变。详见 `repo-topology-and-naming.md` §0，工单 QT-05~QT-09。

#### D29 补遗（2026-10-20 第二轮审查：设计与任务的缺口核对）

审查方法：把 D29 的每条承诺（§2 五不变量 / §5 归属 / §7 阶梯 / §8 checklist / §9 五规则 / §11 关系）逐条对照 `docs/TASKS.md`，找「写了但没单」与「单里缺要求」两类缺口。结论：

| 类别 | 缺口 | 处置 |
|---|---|---|
| **无单**（3） | ① **agent 数据自持（D10）**：agent 用 storage 16 符号 / 14 文件，此前零任务——而这是 S3 删 replace 的必要条件；② **过渡期 core↔agent 连体**：core CI 无任何 agent 检查（实测 0 命中），W0/W1 的 breaking 会静默烂掉 agent 仓；③ **契约版本策略**：C5「保留旧接口一个版本周期」无定义、无兼容矩阵 | 新增 **QT-17 / QT-18 / QT-19** |
| **单里缺要求**（7） | QT-05：agent 侧 replace（**Go replace 不传递**，漏则 S1 即编不过）、`contracts/engine` 包名策略、core-only 类型清单；QT-06：tag 需 push 才可 `go get`；QT-08：按仓类型分派、docs 跨仓链接必断、脚本归属；QT-07：S3 依赖补 QT-17 | 逐条写入对应单 |
| **设计内部不一致**（2） | §1「一仓一 module」与 S1 期 core 仓内 3 module 冲突；§5「domain 整包迁」与 C4「只收 ≥2 仓使用」冲突（实测 `screen/sector/supply_chain/macro/mining` 在 agent 侧 0 命中） | §1 加过渡期澄清；新增 §5.1（包名/replace）与 §5.2（core-only 清单 + S2 回收） |
| **D24 清单缺一类动作** | agent 需要 `TradingCalendar` / `ListingWindow`，但 D24 允许的六类粗粒度动作里没有「交易日历/上市窗口查询」 | 记入 QT-17 要求③，S3 前补进 D24 清单 |

- 审查后工单总数：30 → **33**（+QT-17/18/19）；执行顺序细化为「S0（06→18→08）→ W0 → S1(05) → S2（17→07→19→09）→ W1 → W2 → W3」，并落为**合入门禁**：QT-18 未合入前 QT-01 不得合入（第一个 breaking 在 abi v2）；QT-05 未合入前 QT-07 不得开工。
- 状态：**已裁决 + 已补遗**；仍未实施。

---

### ADR-D30：项目定名 tellurion —— 仓清单与「应用短名 / 库全名」

- 场景：用户 2026-10-20 定名 **tellurion**，同时明确 **owner 保持 `ruoxizhnya` 不变**（「引用名一改会出问题」），并提出「内部 import 不用 github 前缀、跨仓仍用」。此前 D29 只定了拓扑与阶梯，从未定「项目叫什么、module path 长什么样」。
- 实测（Go 1.25.0，四组沙箱实验）：
  1. `module tellurion`（单段无点）+ 仓内 `import "tellurion/pkg/b"` → build / tidy / vet **全绿**；
  2. 外部 `require tellurion v0.0.0`（无 replace）→ `malformed module path "tellurion": missing dot in first path element`；
  3. core `module tellurion` + `require github.com/ruoxizhnya/tellurion-contracts` + replace → **共存、build / run 绿**（即「本仓短名 + 跨仓全名」成立）；
  4. `import "contracts/kit"`（省略 module path）→ `package contracts/kit is not in std`；相对 import → `relative import paths are not supported in module mode`。
- 决策：
  1. **项目名 `tellurion` → `tellurion`**；owner 保持 `ruoxizhnya`，**不建 org、不用 vanity 域名**（否决 D29 §4.1 的建议）。
  2. **应用 / 服务用单段短名**（`tellurion` / `tellurion-agent` / `tellurion-risk` …）；**库用全名**（`github.com/ruoxizhnya/tellurion-contracts` / `-kit`）。
  3. **core 的 module path = `tellurion`** —— 让「core 不可被 import」由编译器强制，把 D24 从纪律变成机制。
  4. 12 仓清单（现有 4 + 待建 2 + 触发式 6）与旧→新映射见 `repo-inventory.md`；执行方式为**新建仓 + `git clone` 迁移（保历史）+ 旧仓 archive**。
  5. 边界：`quant-data-engine` **不属本项目**、不动；`nt-sidecar` 现在不拆，将来独立则叫 **`tellurion-nt`**（Python）。
- 理由：**短名把「应用 vs 库」的边界交给编译器**（被 require 即失败），比写进文档靠人守更可靠；owner 不动是因为它是 path 里最贵的一段，改动收益不抵连锁风险。
- 代价 / 后果：① 改名一次性成本 **1153 处**（core）/ **268 处**（agent）；② core 永不可被 import，按仓地址 `require` 会报不直观的 `package tellurion/pkg/b is not in std`（须写进 core `AGENTS.md`）；③ 私有 contracts/kit 需 `GOPRIVATE` + git 凭证；④ 文档跨仓引用（`tellurion:` → `tellurion:`）`check_doc_links` 查不出，属易漏项。
- 状态：**已裁决（用户 2026-10-20）**；替代 D29 §4 的命名规则，D29 的拓扑 / R1~R5 / S0~S3 / C1~C5 不变。工单 **QT-60（S-1，未启动）**。

---

### ADR-D31：S1 姿态改为 Plan B —— 代码直接落两个新仓，用伪版本而不是 replace

- 场景：QT-60 把 12 个仓**先建了**，其中 `tellurion-contracts` / `tellurion-kit` 各含**最终 module path 的 `go.mod`** + `AGENTS.md` + 占位 `doc.go`。而 D29 §7 的 S1 原文写「本仓内建 `contracts/` `kit/` 子 module、不建新仓、仓内 replace 快速迭代」——**前提被推翻**。S1 因此有两种走法：**A** 先在 core 内建子 module（仓内 replace），W0 冻结后由 QT-07 搬进新仓；**B** 代码直接写进两个新仓，core 通过版本依赖取用。**用户 2026-10-20 选 B。**
- 决策（B 的落地形态比"Plan B 原描述"更严格）：
  1. **全程不使用 `replace`**。core / agent 用 `require github.com/ruoxizhnya/tellurion-{contracts,kit} <伪版本>` + `go.sum`；本地同时改三个仓时用 **`go.work`**（不入库、进 `.gitignore`）。
  2. **S1 仍排在 W0 全绿之后**（D29 原判据不变）：W0 定契约形状，S1 搬形状，只做一次。
  3. **S2 收窄为「伪版本 → `v0.1.0`」**：不再 `git init`、不再迁目录（QT-07 原 ①/② 作废，③~⑥ 保留）。
  4. **core 顶层 `contracts/` 的 5 个数据文件不迁**（`field_dictionary.yaml` + 4 个 `*.schema.{json,sql}`）：它们是运行时按 cwd 相对路径自动发现的**部署资产**（`cmd/data/setup.go:223-225` 依次试 `./contracts/`、`../contracts/`、`../../contracts/`），不是 Go 包。
- 选 B 的理由：
  1. **只搬一次**。A 要把 `pkg/domain`（306 文件 import）先搬进 core/contracts、再搬进新仓，第二次搬还要再动一遍依赖与 agent。
  2. **避开目录撞名（A 的隐藏地雷）**。core 顶层已有 `contracts/`（数据文件）。A 会让该目录**同时**是「子 module 根」与「运行时相对路径目标」；S2 搬走子 module 时那 5 个文件跟着走，**部署后字典静默加载失败**——编译期查不出来。
  3. **R1 从第一天成立**。两仓从 S1 起就是独立可构建、可自证的（无 replace），不必等 S2 才"合法"。
  4. **CI 与 Docker 零改动**。`Dockerfile.service` 是 `COPY . .` 单仓上下文；A 的仓内 replace 反而要动构建上下文。B 走 `go.sum` 的真实版本，构建路径不变。
- 实测证据（Go 1.25.0 / Windows，2026-10-20）：
  - `go get github.com/ruoxizhnya/tellurion-contracts@main` → `v0.0.0-20261020112435-a55208cc2412`，`go.sum` 自动生成；两仓 `GET /repos/...` = **200（public）**。
  - 沙箱实验：`require example.com/lib v0.0.0-00010101000000-000000000000` 且**无 go.sum** → `missing go.sum entry`；加 `go.work use ../lib` 后 `go run` 直接读本地源码 —— **证明 `go.work` 只覆盖本地、不改 `go.mod`、不需要网络**。
- 代价 / 后果：
  1. **契约改动要"先合入 contracts 仓，再 bump core 的 pin"**：W0 期间内循环靠 `go.work` 不受影响，但**每个 core PR 之前必须把 pin 推到 contracts `main` 的提交**；否则可能 pin 到未评审的分支提交 → `tools/check_contract_pin.py`（pin SHA 必须是 main 的祖先）+ QT-09 ① 的 CI 步骤机械兜底。
  2. **伪版本不可读**（`v0.0.0-20261020112435-a55208cc2412`），PR diff 不直观 → `go.mod` 该行上方留一行注释写日期与用途；S2 换 tag 后消失。
  3. **两仓必须公开可读**（或 `GOPRIVATE` + 凭证）：core 的 CI / 镜像构建不再有本地目录兜底。**当前两仓 public；若将来转私有，core 的 CI 与镜像构建会同时红**——登记为已知风险。
  4. **`go.work` 是本地产物**：别人机器上没有，新加入者第一次 `go build` 会拉网络版本，看到的契约可能比本地 `../tellurion-contracts` 旧 → 写进 core `AGENTS.md` 的「首次配置」。
- 状态：**已裁决（用户 2026-10-20 选 Plan B）**；替代 D29 §7 的 S1 形态与 QT-05 原文；D29 的拓扑 / R1~R5 / S0·S2·S3 / C1~C5 不变。工单：QT-05（S1 重写）、QT-07（S2 收窄）、QT-09 ①（新增 pin 祖先检查）。
