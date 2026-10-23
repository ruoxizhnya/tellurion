# qt-decoupling · 解耦与模块化重构设计集（索引）

> 建立：2026-10-19 ｜ 状态：**部分已裁决/已定稿**（D24/D25/D26/D28 用户拍板、D29 分仓拓扑、**D30 仓清单与命名**）；其余为草案。**全部未实施**（实现前须按台账立单走验收）
> 范围：让 tellurion 从「核心仓强耦合 + agent 编译期绑定」演进为**端口化模块化单体（Ports & Adapters）**；「切成独立服务」保留为随时可做的能力，而非现在就付出的代价。
> 对齐既有裁决：ADR-027（先进程内模块化，服务拆分是可选后续部署形态）、ADR-028/029（表达力分层 + WASM 轨道）、ADR-031（内核可插拔）。
> 升格路径：ADR-D 系列为本设计集内部草案编号；正式裁决后按 `tellurion-docs:docs/adr/` 既有流程升格（对照先例：`docs/design/kernel/kernel-pluggable-nt-ready.md` → ADR-031）。
> 配套图：[`../qt-component-diagram/`](../qt-component-diagram/)（全系统构件图，mermaid 源 + 可视化 `index.html`）。
> **2026-10-19 融入审查修正**（对齐正式裁决）：① D12/BE-18 的 `.so` 口径从「可信一等公民」改为**存量遗留**（对齐 **ADR-024 Accepted「不做 `plugin.Open`」**）；② D18 补与 **ADR-027 服务数 5→7** 的对账注（X 线是能力边界清单，非最终拓扑）；③ kernel-interface-map 补 **ADR-031 四契约**（RunSpec/SignalSpec/ResultSpec/EngineRunner）术语对齐。
> 时间估算（「1-2 周」「2-3 天」等）为**量级参考**，非排期承诺；正式排期以 TASKS 台账为准。

## ADR-D 编号 → 文件映射（编号散落各文件，此处为唯一索引）

| 编号 | 裁决内容 | 定义文件 | 状态 |
|---|---|---|---|
| D1 | 不拆进程，先做端口化模块化单体 | [project-engineering-file.md](project-engineering-file.md) | 提议 |
| D2 | 契约外置为**只依赖 domain** 的 module `tellurion-contracts` | [project-engineering-file.md](project-engineering-file.md) | 提议 |
| D3 | 策略解耦走插件/WASM 边界而非 HTTP（⚠️「策略独立」部分已由 D6 细化） | [project-engineering-file.md](project-engineering-file.md) | 提议（部分被 D6 细化） |
| D4 | 双引擎升级为「能力声明 + 按能力路由」（fail-closed） | [project-engineering-file.md](project-engineering-file.md) | 提议 |
| D5 | 装配层从 `pkg/kernel` 外移，kernel 只留框架 | [project-engineering-file.md](project-engineering-file.md) | 提议 |
| D6 | 区分「策略资产服务 ✅ 独立」与「策略运行时 ❌ 不做服务」 | [brainstorm-2026-10-19.md](brainstorm-2026-10-19.md) | 提议（修正 D3） |
| D7 | 协议分层：contracts（进程内）与 OpenAPI（跨进程）各管一段 | [protocol-and-data-boundary.md](protocol-and-data-boundary.md) | 提议 |
| D8 | 表归属边界：复用存储能力 ≠ 共享表（⚠️ 两处已被 D10 修正） | [protocol-and-data-boundary.md](protocol-and-data-boundary.md) | 提议（部分被 D10 修正） |
| D9 | 四 module 拓扑 kit / contracts / core / agent，依赖单向无环（⚠️ module 数量已被 D24 修正为五，**再经 M1 并入回落为 4：`kit → contracts(含 domain) → core`，见 repo-topology §5**） | [target-module-topology.md](target-module-topology.md) | 提议（部分被 D24 / M1 修正） |
| D10 | 数据归属重划：实验域数据归 agent 自建 `ai.*` schema | [target-module-topology.md](target-module-topology.md) | 提议（修正 D8） |
| D11 | 回测引擎「摘出来」决策（依赖反转 E1~E4） | [backtest-extraction.md](backtest-extraction.md) | 提议 |
| D12 | 策略加载外化（编译/资产/执行三分 + 五步工作流） | [strategy-loading-externalization.md](strategy-loading-externalization.md) | 提议 |
| D13 | 策略真相源外置 + 引擎内容寻址缓存（三层所有权） | [strategy-source-of-truth-and-cache.md](strategy-source-of-truth-and-cache.md) | 草案 |
| D14 | 策略服务落地方案（两条腿：DSL 资产化 + WASM PoC，S0~S4） | [strategy-service-landing-plan.md](strategy-service-landing-plan.md) | 草案 |
| D15 | 载体表达力边界：图灵完备性与种子化随机（修正 D14 乐观判断） | [strategy-turing-completeness-and-randomness.md](strategy-turing-completeness-and-randomness.md) | 草案 |
| D16 | 状态机策略落地契约（三层状态模型 + 恢复语义 + 4 协议缺口） | [strategy-state-machine-wasm.md](strategy-state-machine-wasm.md) | 草案 |
| D17 | 产品需求可实现，M0/M1/M2 三里程碑进入执行（G1~G6 待办） | [project-engineering-file.md](project-engineering-file.md) + [implementation-plan-mvp.md](implementation-plan-mvp.md) | 提议（进入执行） |
| D18 | 服务拆分统一模式 + 六服务边界（X0~X6 分期） | [service-extraction-plan.md](service-extraction-plan.md) | 提议 |
| D19 | 数据服务契约与数据归属（DDD 分库 + 统一数据协议 + connector/aggregator） | [data-service-contract-and-ownership.md](data-service-contract-and-ownership.md) | 提议 |
| D20 | 风控服务（两层：外部规则真相源 + 进程内 RiskPolicy 同步裁决） | [risk-service-design.md](risk-service-design.md) | 提议（设计固化） |
| D21 | 分析服务（纯函数只读，补 BE-05/BE-06 缺口） | [analytics-service-design.md](analytics-service-design.md) | 提议（设计固化） |
| D22 | 编排/worker 服务（job/batch/walkforward = 算力出口，schema `run`） | [orchestration-service-design.md](orchestration-service-design.md) | 提议（设计固化） |
| D23 | 交易通道网关（XTPTrader + MockTrader = port 两实现） | [broker-gateway-design.md](broker-gateway-design.md) | 提议（设计固化） |
| D24 | **用户裁决落档**：五 module（domain 独立）+ agent↔core HTTP 粗粒度 + 策略线先资产后接线 | [target-module-topology.md](target-module-topology.md) | **已裁决（用户 2026-10-19）** |
| D25 | **用户裁决落档**：J 市场级优先 / I 混合实例模型（abi v2）/ B latest 语法糖+提交固化 hash | [adr-029-amendment-host-api-v2.md](adr-029-amendment-host-api-v2.md) | **已裁决（用户 2026-10-19）** |
| D26 | **用户裁决落档**：L 参数两层 / M Strength=强度[0,1] | [project-engineering-file.md](project-engineering-file.md) | **已裁决（用户 2026-10-20）** |
| D27 | **P0 第二轮深度审查**：旧 20 项逐条闭环核对 + 新增 9 项 P0（N1~N9） | [p0-round2-review.md](p0-round2-review.md) | 审查结论（设计补充基线） |
| D28 | **用户裁决落档**：N2 成交时点 `next_open` / N9 新增 `emit_target2` / A12 先扫描再定 trap | [p0-round2-review.md](p0-round2-review.md) | **已裁决（用户 2026-10-20）** |
| D29 | **分仓拓扑与命名**：repo=模块/服务、contract 约束边界、**domain 并入 contracts 仓**（修正 D24-1）、S0~S3 迁移阶梯 + 第二轮审查补遗（→ QT-17~19） | [repo-topology-and-naming.md](repo-topology-and-naming.md) | **已裁决（用户 2026-10-20）**；仓名与 module path 唯一真相源见 [repo-inventory.md](repo-inventory.md)（施工前必读）¹ |
| D30 | **仓清单与命名终稿**：项目定名 **tellurion**、owner 保持 `ruoxizhnya`、**应用/服务用单段短名、库用全名**、12 仓表 + 旧→新映射 + 已裁边界 | [repo-inventory.md](repo-inventory.md) | **已裁决（用户 2026-10-20）** |
| D31 | **S1 姿态改为 Plan B**：12 仓已建 -> **代码直接落两个新仓**，core/agent 用 require 伪版本 + 本地 go.work（**全程无 replace**）；S2 收窄为「伪版本 -> tag v0.1.0」；core 顶层 5 个数据契约文件留原地 | [project-engineering-file.md](project-engineering-file.md) | **已裁决（用户 2026-10-20）**；替代 D29 第 7 节的 S1 形态与 QT-05 原文 |

> ¹ repo-inventory 唯一真相源含：两条 module path 规则（应用短名 / 库全名，含 Go 1.25 实测）+ 12 仓表 + 旧→新映射 + 已裁边界 + 已知坑（core 不可被 import、GOPRIVATE、文档引用）。（行内注释移此，防表行断行——refactor-plan-review L1）

### ADR-D ↔ 正式 ADR 关联（引用前先看这张表）

| ADR-D | 对应正式裁决 | 关系 |
|---|---|---|
| D1 / D2 / D5 / D9 | **ADR-027**（模块化拆解，Proposed） | 进程内模块化与契约外置 = ADR-027 的前置细化；服务拆分仍以 027 的 5→7 为终态口径 |
| D3 / D6 / D12~D17 | **ADR-027 §2.6.1** + **ADR-028/029**（表达力分层 + WASM 轨道）+ **ADR-024**（`.so` 不做的裁决） | 策略资产服务/运行时两分是 027 的裁决延伸；`.so` 口径以 024 为准 |
| **D25** | **ADR-029 §3（修订草案）** | host API **abi v2**：`get_bar` 加 symbol 维度、混合 scope、latest 固化 hash；草案见 `adr-029-amendment-host-api-v2.md` |
| D4 / D11 | **ADR-031**（内核可插拔，Accepted） | 双内核 + RunSpec/SignalSpec/ResultSpec/EngineRunner 四契约 |
| D7 | **ADR-008**（服务间通信，Accepted） | 同步/异步边界；contracts 与 OpenAPI 的分层是其进程内细化 |
| D8 / D10 / D19 | **ADR-023**（AI 实验员三层模型，Accepted）+ **ADR-016**（多源数据，Proposed） | 数据归属（实验域归 agent、仪器域归 core）+ connector/聚合口径 |
| D18 / D20~D23 | **ADR-027** | X 线是能力边界清单；升格为服务拓扑时与 027 §2 逐一对账（已在对账注声明） |

### 裁决记录（2026-10-19 ~ 2026-10-20）

| # | 事项 | 裁决 |
|---|---|---|
| B | `strategy_ref` 版本治理 | ✅ **允许 latest 作语法糖，提交边界解析并固化 hash**；执行/结果只认 hash；CI/双跑/正式台账直接传 hash |
| I | per-symbol vs 单实例+横截面 | ✅ **混合（C）**：abi v2 加 symbol 维度，`scope: market`（默认单实例横截面）/ `scope: symbol`（分片） |
| J | 状态机类型 | ✅ **市场级优先、混合为目标**；M0 PoC = 市场级单实例（727 次 on_bar） |
| L | 参数归属 | ✅ **两层**：定义默认（进 `version_hash`）+ run 覆写（进 RunSpec，未知名拒绝）；生效值落运行记录 |
| M | `Strength` 语义 | ✅ **维持"信号强度/置信度"，finite ∈ [0,1]**；不改为目标权重（与既有 Go 消费点一致） |
| N2 | 成交时点 | ✅ **`SignalSpec.FillTiming` 必填；终态 `next_open`**，`same_close` 保留为兼容并标 `known_bias`；先出差异报告再切默认 |
| N9 | 仓位表达 | ✅ **新增 `emit_target2(symbol_idx, weight∈[-1,1])`**（目标权重，政策 cap 优先；与 `emit_signal` 混用 trap）；**不动 Strength 语义** |
| A12 | 重复信号 | ✅ **先做存量策略扫描**：零重复 → trap；有重复 → 先 `close` 优先合并 + 计数，trap 留作下一版开关 |
| M1 | `domain` 归属 | ✅ **并入 contracts 仓**（`contracts/domain`），module 由 5 回落到 4；**替代 D24 第 1 条** |
| M2 | 版本化起点 | ✅ **S0 打 `v0.1.0` 但保留 `replace`**；删 replace 的前提是 QT-50 完成 |
| M3 | 拆仓时机 | ✅ **S1 排在 W0 之后**；**ADR-D31 改形态：代码直接落两个新仓**（无 replace，S2 只把伪版本换成 tag） |
| M4 | 项目改名 | ✅ **项目段 `quant-trading` → `tellurion`**（2026-10-20 订正：本行曾被改名迁移误替换为自指）；owner 保持 `ruoxizhnya`（不建 org、不改 owner —— 引用名一改会连锁报错） |
| M5 | module path 形态 | ✅ **应用/服务用单段短名**（编译器拒绝 `require`，强制“不可被 import”）、**库用 `github.com/ruoxizhnya/<仓>`** |
| A | artifact 存储载体 | ✅ **本地栈 PG BYTEA**（单 blob ≤ 8 MiB，超限 fail-loud）；保留 `storage='url'` 列分支作将来逃逸阀，暂不实现对象存储 |
| D | 编译服务归属 | ✅ **归 `tellurion-strategy` 仓的独立进程/容器**（`cmd/compiler`，独立镜像、锁 toolchain 版本）；不新建第 13 仓、不放 `tellurion-agent`；ADR-027 §2.6.1「编译在 ai-service」登记为**被本裁决修订** |
| E | 引擎缓存载体 | ✅ **内存 LRU 一级 + 本地磁盘内容寻址二级**；按容量与 TTL 驱逐（artifact 不可变，磁盘缓存安全） |
| F | latest 失效策略 | ✅ **TTL 300s**；promote 响应携带新 hash 供本进程主动失效；**不引入 webhook/msgbus** |
| G | 内置 14 策略迁移 | ✅ **保留 init 启动 + 同步登记**（`carrier=builtin`），不做全量迁移 |
| H | external 是否纳入同一 `strategy_ref` | ✅ **纳入同一命名空间**，解析按 `carrier` 路由；外部载体 hash 语义单独成文，不与 `artifact_hash` 混用 |

### 待用户裁决清单（文档不代裁，阻塞实施）

> 2026-10-20：N2 / N9 / A12 已裁决，见上表；**K 已定（新增 `kit/db`：仅 connection/migrate/DSN）**，见 D29 与 QT-05。2026-10-21：**A/D/E/F/G/H 六项已裁决（见上表 裁决记录）**，QT-41 与 QT-41b 阻塞解除；**本表仅剩 C**（C 不阻塞实施：QT-01 已按「Go-only 冻结 + 书面声明 NT 对齐边界」兜底）。**「阻塞单」列**指未裁决时被卡住的具体工单（`tellurion:docs/TASKS.md`，refactor-plan-review H3 修复补列）。

| # | 事项 | 出处 | 为什么必须你拍 | 阻塞单 |
|---|---|---|---|---|
| C | NT 内核是否实现同一套 on_bar host API（**若做，直接按 v2 签名**）**｜升级：阻塞 QT-01 契约冻结的定稿范围** | D14 §7 | 决定「一份 wasm 跑两核」能否成立；两种结局都改变 QT-01 冻结覆盖 Go-only 还是双核（refactor-plan-review H5） | QT-01 ③（已写冻结范围声明：未裁则按 Go-only 冻结 + 书面声明 NT 对齐边界） |

> **A / D / E / F / G / H 已于 2026-10-21 裁决**（逐条结果见上一节「裁决记录」），对应阻塞单 QT-41 / QT-41b 已解除；本表只保留 C。


## 其他文件

| 文件 | 内容 |
|---|---|
| [project-engineering-file.md](project-engineering-file.md) | 项目工程档案：概况、技术栈、目标架构图、port 契约草案、风险、P0~P3 路线、耦合实测基线 |
| [p0-round2-review.md](p0-round2-review.md) | **P0 第二轮审查（实施前必读）**：旧 20 项闭环核对（16 闭环 / 5 半闭环 / 1 待裁决）+ **新增 N1~N9**（错误分级、执行时点、状态身份、重放边界、内存所有权、留痕闭合、双哈希、资源准入、仓位表达）+ 修订依赖顺序 |
| [p0-remediation-design.md](p0-remediation-design.md) | **P0 修复设计基线（实施前必读）**：20 项 P0（A 执行正确性 / B 协议可实现性 / C 可用性 / D 流程）+ 逐项契约 + 依赖顺序 + 影响面 |
| [w1-repo-ownership.md](w1-repo-ownership.md) | **W1 跨仓落点与仓归属（施工前定盘，2026-10-23）**：QT-10~16 逐单落点仓/文件 + QT-10/11/22 专项裁决 + 撞车文件与串行边界 |
| [backtest-engine-review.md](backtest-engine-review.md) | **BE-01~BE-30 问题登记本**（现象 + 证据 + 初步方向，不是方案） |
| [market-friction-audit.md](market-friction-audit.md) | **市场摩擦六类事实核实（QT-52 / BE-11 结案）**：停牌 / 成交量 / 复权 / 分红送股 / 涨跌停 / 集合竞价逐类「完备·部分·缺失」结论 + 证据行号 + 方向性影响 + `auction` 接线方案 |
| [host-region-overlap.md](host-region-overlap.md) | **宿主区 × 模块静态数据（QT-62）**：地址空间权威定则、静态腿实现（解 Data section）、fail-closed 语义与验收/破坏验证证据 |
| [kernel-interface-map.md](kernel-interface-map.md) | Go 内核接口面清单（输入 5 类入口 / 输出面），契约外置的输入 |
| [external-wasm-proposal-review.md](external-wasm-proposal-review.md) | 第三方 Wasm 方案评审（3 条不成立；发现 P0 缺陷 `WithCloseOnContextDone` 缺失） |
| [brainstorm-2026-10-19.md](brainstorm-2026-10-19.md) | 头脑风暴记录（与 ADR-027 对齐、WASM 机制、contracts 三层、能力化模型） |
| [repo-topology-and-naming.md](repo-topology-and-naming.md) | **分仓拓扑与命名（D29，施工前必读）**：五条 repo autonomy 不变量 + 分仓拓扑与命名规则（现 4 库；12 仓为上限/当前规划，以 repo-inventory 为准）+ domain 归属裁定 + 现状差距实测 + S0~S3 阶梯 + 8 步建仓 checklist + 跨仓依赖五规则 |

## 贯穿纪律（摘要）

1. **内循环零网络**：逐 bar/逐笔同步远程调用是硬否决（ns → ms，10³~10⁶ 倍）；「无状态」= 无持久化状态，不是逐 bar 取数。
2. **fail-closed**：能力路由、artifact 校验、`strategy_ref` 解析、latest 失效，失败一律显式拒绝，不回退。
3. **可复现留痕**：结果记录 `strategy_ref hash` / `dataset_snapshot_hash` / 风控规则 hash（+ seed）。
4. **现在不要做**（P3 触发条件满足才评估）：拆 HTTP 服务 / MQ / K8s / 拆库分表 / 远程适配器；**不为「将来可能要拆」提前建空仓**（仓 = agent 的工作上下文，空壳仓只会制造跨仓验证）。
5. **跨仓只认 tag**：`replace` 指向仓外 = 单仓不可构建；`go.work` 不入库（D29 §9 C1/C2）；**应用用短名、库用全名**（D30），仓名以 `repo-inventory.md` 为准。
6. 服务拆分（X 线）是**可选的后续部署形态**，先把边界收口成 port，进程拆分等触发条件（ADR-027 口径）。
