# qt-decoupling refactor 计划全维度审查（2026-10-20）

> 审查对象：`docs/design/qt-decoupling/` 设计集（24 篇）+ `docs/TASKS.md` 第 4 代（30 张 QT 单 + 6 张 DBT 存量）+ `quant-trading-docs:docs/adr/` 相关正式 ADR + 代码现状抽查。
> 审查维度：① 设计 ② 实现方式 ③ 计划 ④ 每个新仓的文档（边界 / 外部接口 / 内部功能）⑤ 其他（治理 / 安全 / 测试 / 文档卫生）。
> 结论分级：Critical / High / Medium / Low，每条带文件+行号与可执行修复建议。本报告只登记问题与建议，不代裁决。

---

## 0. 总评

**总体判断：计划质量在同类工程文档中属于上乘，可以进入实施，但有 1 项 Critical 与 5 项 High 必须在 W0 开工前收口。**

值得肯定的基线（不是客套，是本审查确认成立的事实）：

- 施工顺序有真实的证据支撑：import 实测（core 1153 处/526 文件、agent 268 处/37 文件、`pkg/domain` 306 文件引用）、Go 1.25 短 module path 的可行性实验（`repo-inventory.md` §0.1）、tag/replace 现状盘点（`repo-topology-and-naming.md` §6），不是拍脑袋排期。
- 关键工程陷阱已被提前识别并写进工单：`replace` 不传递（`repo-topology-and-naming.md` L185）、QT-18 护栏必须先于 QT-01 合入（`TASKS.md` L103）、删 replace 的唯一时点是 S3（`TASKS.md` L360）、S1 子 module 直接写最终路径避免二次全仓替换（`TASKS.md` L160）。
- 验收文化一致：逐位对拍、破坏验证（改回坏代码必须红）、向量测试、只读扫描先行（QT-04）——与项目既有硬约束（fail-closed、可复现留痕）对得上。
- 「未触发不建空仓」「内循环零网络」「两层风控（裁决永驻进程内）」等边界纪律明确写了负面约束，可被 AST/repoguard 机械执行。

以下问题按严重级别列出。

---

## Critical

### C1 · 状态语义与冻结的三值 `status` 直接冲突，且未走变更评审

- 证据：
  - `TASKS.md` L182（QT-10 ④）：`ResultSpec` 加 `Status ∈ {ok,degraded,failed}` + `Failures[]`；
  - `TASKS.md` L236（QT-15 ⑤）：terminated 后 run 标 `degraded` + `Failures += session_terminated`；
  - 但 `docs/SPEC.md` L3179-L3198 **冻结** `status` 三值语义 `completed / invalid / failed`，并明文「`status` 字段本身是调用方的唯一判据」「读路径同源（`Engine.ResponseFromState` 单一透传点），不允许第三处硬编码」，且该节带文档互锁护栏。
- 冲突性质：`ok/degraded/failed` 是第四套状态词汇。`degraded` 在三值空间里没有归宿——它既不是「跑完且有效」（completed），也不天然等于「结果无效」（invalid，其 `invalid_reasons` Code 是稳定枚举）也不是「没跑完」（failed）。compare 逻辑与 UI 均按三值消费，两套词汇并存 = 跨组件语义分裂，直接违反既有硬约束「status 字段跨 core API / compare / UI 三值一致」与「status/reasons 单一来源」。
- 修复建议（二选一，需用户裁决后落文档）：
  1. **映射进三值空间**（推荐，改动最小）：`degraded`（数据缺口跳天、session 终止重放）→ `invalid` + 稳定 `invalid_reasons` Code（如 `data_gap_skipped` / `session_terminated`）；`strategy_fault`/`contract_breach` → `failed` + `error`；`Failures[]` 降级为诊断明细而非状态来源。QT-10 ④、QT-15 ⑤ 的措辞随之改写。
  2. 若坚持四值：必须按硬约束走 ResultSpec 冻结 schema 的**正式变更评审**，同步改 `docs/SPEC.md` 三值节、`buildResponseFromState`/`ResponseFromState`、compare、UI 与合规测试——工作量远大于方案 1，且违背「status 是唯一判据」的既有裁决。
- 优先级：**P0，W0/QT-10 开工前必须定**，否则 QT-10/QT-15 实现完还要二次返工。

---

## High

### H1 · 冻结契约 schema 的变更未挂「正式变更评审 + 合规测试」步骤

- 证据：`TASKS.md` L132（QT-02 ③：`ResultSpec` 落 `fill_timing`/`known_bias`）、L182（QT-10 ④）、L141（QT-03 ④：`run_artifacts` 落新列）。而硬约束明文：ResultSpec canonical schema 已冻结于 `docs/SPEC.md`，任何 schema 变更必须打破合规测试并走正式变更评审。
- 问题：三张单的六字段里都只有「实现+验收」，没有「触发合规测试红 → 更新 SPEC.md → 变更评审记录」这一步。按现文本派工，独立 agent 会在不惊动评审的情况下改冻结面。
- 修复建议：在 `TASKS.md`「维护本文件」节（L557 附近）加一条硬规则：**凡触碰 `ResultSpec`/`RunSpec`/`SignalSpec`/`docs/SPEC.md` 冻结字段的单，六字段内必须列出「schema 变更评审项 + 需同步更新的 SPEC 节号」**；并在 QT-02/QT-03/QT-10 的「③ 要求」各补一行。

### H2 · agent↔core HTTP 新边界的 wire 契约与鉴权/授权完全缺位

- 证据：
  - `target-module-topology.md` L134（D24-2）列出六类合法动作；`TASKS.md` L458-L459（QT-50 ②）只要求「定义 agent 侧 HTTP 客户端（六类粗粒度动作）」；`TASKS.md` L344（QT-17 ③）又增补第 7 类（日历/上市窗口查询，待用户确认）。
  - 但 `protocol-and-data-boundary.md` L19-L29（D7 单一真相源规则）规定：跨进程字段的真相源是 `docs/openapi.yaml` + `oapi-codegen` 生成 `contracts/dto`，且现有「生产路由表与 yaml 双向对拍」机制管着这张表——QT-50/QT-17 均未要求把新端点写进 `docs/openapi.yaml`、未提对拍、未提 DTO 生成。
  - 全部相关文本（D24 / D22 / QT-50 / QT-17）**零处**提及新边界的鉴权：token/RBAC（`RequireRole`）/loopback-only/CORS 均无归属。既有硬约束要求 API 端点敏感操作必须 RBAC、鉴权 fail-closed。
- 问题：这是 refactor 之后第一个真正的跨进程攻击面，现在只有「六类动作」的中文清单，没有协议、没有错误语义、没有版本策略、没有权限模型。
- 修复建议：QT-50 拆出一张前置单（或在 QT-50 六字段内补齐）：① 六/七类动作的 endpoint + DTO 写入 `docs/openapi.yaml` 并纳入路由表对拍；② 鉴权模型（默认复用既有 JWT+RBAC，本地开发 loopback-only 且 fail-closed）；③ 错误码与限流；④ 生成 `contracts/dto` 的落点与禁手改规则。同时把 D7「现在不该为 agent→core 造 OpenAPI DTO」的推论加订正注（该边在 QT-50 后即为跨进程，判据已翻转）。

### H3 · 待裁决项 A~H 的处置自相矛盾，工单引用不存在的工单号

- 证据：
  - `README.md` L77-L89：A/C/D/E/F/G/H「文档不代裁，阻塞实施」；
  - 但 `TASKS.md` L409（QT-41 ②）擅自按「未决 A 当前按 BYTEA 落地」写入要求，L410（QT-41 ③）又按 F 的「TTL/事件失效」二选一写了实现要求——A/F 实为待用户拍板项；
  - `TASKS.md` L413（QT-41 ⑥）引用「QT-41b」，该单**不存在**；`TASKS.md` L305（QT-26 ④）说「不实现编译服务（QT-41）」，与 QT-41 说编译服务在 QT-41b 互相推诿；而编译服务归属（裁决 D）本身仍待定。
- 问题：派工 agent 会按工单正文施工，正文里藏着「代裁」的决定与悬空引用，违反「文档不代裁」与台账可执行性要求。
- 修复建议：① QT-41 ②③ 把 A/F 的假设显式标为「**假设，需裁决后生效**」并在待裁决清单注明阻塞的具体单号；② 删除 QT-41b 引用或正式立单；③ 编译服务归属（D）裁决前，QT-26 ⑤「五道门」中的 Compile 门标注「依赖裁决 D」。

### H4 · 产品主目标（AI 生成策略闭环）没有工单落点

- 证据：`implementation-plan-mvp.md` L30 定义「档 2 = 代码→编译→资产服务→引擎缓存加载执行」为**目标**，L66 G6「AI 生成闭环（Go 子集+静态校验+promote）2-3 周」，L125-L131 M2 里程碑（promote 人工闸门、全程留痕）。但 `TASKS.md` 的 30 张 QT 单在 W3（QT-30/31 PoC）后即结束，**没有任何单承接 G6/M2**；`TASKS.md` L4 的自述「30 张新工单全部可追溯到设计文档的具体节」在 G6/M2 上不成立。
- 问题：计划的终点（PoC 证明载体可用）与产品需求的终点（AI 生成策略可入回测）之间断链；若不补，refactor 完成时产品目标仍未交付。
- 修复建议：补一张 W4 单（如 QT-32「AI 策略生成闭环（promote 闸门 + 全链留痕）」，六字段齐全，引用 implementation-plan-mvp §6-M2 与 ADR-023 的边界），或在台账「执行顺序总览」显式声明「G6/M2 不在本代范围，另立代」——二选一，不能沉默。

### H5 · NT 侧（NautilusTrader 内核）的策略层对齐没有计划轨道

- 证据：待裁决 C（`README.md` L84：NT 内核是否实现同一套 on_bar host API）未定；`TASKS.md` 全篇 NT 相关只有 QT-02 ⑤ 的 `ErrFillTimingMismatch` 断言（L132）与跨仓索引一句话（L551）；QT-26/QT-30 的对拍是「Go 内置 vs WASM」，不含 NT。
- 问题：双内核架构（按市场完整核心切换）是已裁决的终态。若 C 判「做」，NT 侧 host API/abi v2 是一块未估量的工程；若判「不做」，「一份 wasm 跑两核」不成立，D25 的 abi v2 设计只服务 Go 核，需要成文说明对齐边界在哪。两种结局都影响 QT-01 的契约冻结范围。
- 修复建议：在 W0 前给 C 一个裁决；若暂不能裁，在 QT-01 的六字段里显式声明「本契约冻结仅覆盖 Go 内核；NT 侧对齐待 C 裁决后另立单」，并把 C 从「待裁决」升级为「阻塞 QT-01 定稿范围」。

---

## Medium

### M1 · 命名残留会让独立 agent 照旧名建仓/写 replace

- 证据：`repo-topology-and-naming.md` L185（§5.1：agent replace 写 `../quant-trading/contracts`）、L223（§7：最终路径写 `github.com/ruoxizhnya/quant-trading-contracts`）、L236-L243（§8 建仓 checklist 脚本整段用 `quant-trading-contracts`）。虽 L130 声明「本篇名字读作 repo-inventory 的名字」，但 §7/§8 是**照抄执行的施工脚本**，不是历史叙述；`TASKS.md` L160/L167 已是 tellurion 正确口径，两处并存。
- 修复建议：把 §5.1/§7/§8 三处脚本与代码块机械替换为 tellurion 口径（正文历史叙述可不动），或在三处顶部加「⚠️ 执行时以 `repo-inventory.md` §0 为准，以下命令需先替换仓名」的显式警示。

### M2 · contracts/kit/domain 依赖方向在三份文档里三种口径

- 证据：`project-engineering-file.md`（D2：contracts「只依赖 domain」）；`target-module-topology.md` L13/L31/L45（contracts「零依赖」、不变量 `kit ← domain ← contracts`）；`repo-topology-and-naming.md` L78（`contracts → kit` ✅）与 `TASKS.md` L362（QT-07 ④：contracts 依赖 kit 的 decimal/errors）。M1 裁决（domain 并入 contracts）落地后，`target-module-topology.md` L49 的「依赖不变量（护栏）」文本仍是旧式——护栏若按该行实现会写错方向。
- 修复建议：以 `repo-topology-and-naming.md` L78 表为准统一为「`kit ← contracts(含 domain) ← core`，`agent → kit + contracts`」；给 `target-module-topology.md` L49 与 `project-engineering-file.md` D2 加订正 banner（与该文档其余 ⚠️ 注同款），注明被 M1/D29 §5 替代。

### M3 · OpenAPI / SPEC 的仓归属未定义

- 证据：`protocol-and-data-boundary.md` L23（openapi.yaml「现状，不动」，住 core `docs/`，生成 `contracts/dto`）vs `repo-topology-and-naming.md` L119（contracts 仓放「OpenAPI 源」）vs `repo-inventory.md` L51（contracts 仓含 `schemas/`）。且 `docs/SPEC.md`（ResultSpec 冻结定义，21 万字）在 qt-decoupling 全集中 **0 处**提及拆仓后的归属——S2 后 contracts 仓拥有 schema 类型，而 schema 的规范文本仍在 core 仓 docs。
- 修复建议：在 `repo-inventory.md` §3 已裁边界表补两行：「`docs/openapi.yaml` 归 core（服务端点的真相源），contracts 只收生成物 `dto/`（禁手改）」（或改判归 contracts，二选一）；「`docs/SPEC.md` 归 core，S2 后其四契约节与 contracts 仓 `engine/` 互锁，改一处必须同步另一仓」。

### M4 · 执行顺序存在互相矛盾的三种读法

- 证据：`TASKS.md` L560（「S-1 → S0 → W0 → S1 → S2 → W1 → W2 → W3」）vs L52（「17 可与 W1 并行」，17 属 S2 段）vs L25/L31（总览表把 S2/S3 排在 W0 后、W1 排在 S2/S3 后）。按 L560 读：W1 必须等 S2 全部完成；按 L52 读：W1 可与 S2 并行。多 agent 派工会得出两种排程。
- 修复建议：把 L560 改为与并行指引一致的偏序表述（如「W1 不依赖 S2，仅 QT-17 与 W1 可并行；QT-07/09/19 必须在 W0+QT-05 后」），并在总览表 W1 行的「依赖」列写明「无 S2 依赖」。

### M5 · 「终态 12 仓」与「X 线未与 ADR-027 对账」并存

- 证据：`repo-topology-and-naming.md` L24（「终态 12 个」）；`repo-inventory.md` L43（12 仓表）；但 `service-extraction-plan.md` L9 自述「六个候选是能力边界清单，**不是最终服务拓扑**，与 ADR-027 §2 的 5→7 未逐一对账，升格时对账」。
- 问题：对外承诺了 12 仓终态，对内又说拓扑未定；X 线真触发建仓时可能发现服务数与 027 口径对不上（例如 data A/B 二分、reduction-service、分析并入他服务）。
- 修复建议：`repo-inventory.md` §1 表头改为「12 仓上限/当前规划」，并把「X 线 6 仓建仓前必须先完成与 ADR-027 §2 的对账」写进 QT-40~45 的公共前置。

### M6 · 新仓只有「入口三件套」，没有随仓的接口规格载体

- 证据：`TASKS.md` L105-L112（QT-08 只要求 AGENTS.md/TASKS/CI）；`repo-topology-and-naming.md` L232-L255（8 步建仓 checklist 同样止于 AGENTS/TASKS/CI/护栏）。contracts 仓的导出面（四契约 + port + 值对象 + 哨兵错误的完整清单、稳定性标注、错误语义）目前散在 core 仓的 `kernel-interface-map.md`/`p0-*.md`/`docs/SPEC.md`；kit 各包 API 边界只有一句话（如 `kit/db`「仅 connection/migrate/DSN」）。
- 修复建议：QT-07 的 S2-c 补一项：contracts 仓建 `docs/API.md`（导出面清单 + 稳定性 + 错误语义 + 指回 `tellurion:docs/SPEC.md` 的节号）；kit 仓建 `docs/SCOPE.md`（12 包逐包准入/禁入清单，源出 `target-module-topology.md` L56-L68 的表）。二者都随 CI 的 AST 护栏更新。

### M7 · QT-60 验收判据有瑕疵

- 证据：`TASKS.md` L76（验收「`git log --oneline | wc -l` 与旧仓一致」）与 `repo-inventory.md` L83（执行步骤含 rename commit）矛盾——改名必然新增 ≥1 个 commit，按现判据永远「不一致」。另 L71/L76 的「历史记录与现状描述不改」「md 中仅剩历史记录」缺少可机械判据。
- 修复建议：验收改为「旧仓 commit 数 + N（本次改名提交数，预期 1~4）」；md 替换规则补机械判据（如「`quant-trading:` 前缀 grep 仅允许出现在 `docs/archive/` 与带日期的历史注记中」）。

### M8 · 服务设计文档的对外接口停在路径/端口名级

- 证据（子代理复核 + 抽查）：`analytics-service-design.md` L53-L83、`broker-gateway-design.md` L34-L63、`orchestration-service-design.md` L5-L14、`risk-service-design.md` L5-L21 均给出清晰的「管什么/不管什么」与端口/REST 路径，但 DTO 字段、错误语义、版本策略未成文；`orchestration`→内核、`analytics`→`ResultReader` 等跨面接口与 `kernel-interface-map.md` 的签名未逐一咬合（后者是「契约外置的输入」，但两处未对表）。`data-service-contract-and-ownership.md` L51-L78（`UnifiedDataPoint`）是唯一落到字段级的例外，质量好，应作为其余五篇的模板。
- 修复建议：X 线各单（QT-40~45）开工前，对应设计文档补「接口对表」小节：逐个 port/endpoint 列签名+错误语义，并显式引用 `kernel-interface-map.md`/`contracts` 的对应符号；未对表不开工（与 C3 契约变更是使用方驱动的纪律一致）。

### M9 · 已裁决 ADR-D 系列未升格，docs 仓无承接单

- 证据：`README.md` L6 只有一句「正式裁决后按既有流程升格」；D24/D25/D26/D28/D29/D30 均已标「已裁决」且直接驱动施工（QT-05/07/60），但 `quant-trading-docs:docs/adr/` 无对应正式 ADR，`TASKS.md` 无升格单；docs 仓自身 CI/TASKS 缺失靠 QT-08 补。
- 修复建议：QT-60/QT-05 开工前，在 docs 仓台账立「ADR-D24/D25/D29/D30 升格」单（或在 core `TASKS.md` 跨仓索引加指针）；升格范围至少覆盖驱动施工的四篇，其余可随迭代。

---

## Low

- **L1** `README.md` L43-L44：D29 表行断行，markdown 表格渲染错位（状态列内容溢出为独立一行）。修复：压缩 D29 行或移到表下注释。
- **L2** `README.md` L23（「module 数量已被 D24 修正为五」）未补 M1「回落为 4」的订正；L103 对 `repo-topology-and-naming.md` 的描述是「6 仓拓扑」，与 `repo-inventory.md` 的 12 仓、该文 §3.2 的 12 仓均不一致。
- **L3** `repo-topology-and-naming.md` L51 标题「3.1 现在（6 仓）」实为 S2 后形态（现为 4 仓，kit/contracts 尚不存在），与 §1 L24「现在 4 个」自相矛盾。
- **L4** kit 仓缺版本策略：`TASKS.md` L384-L388（QT-19）只管 contracts 的 semver/兼容矩阵，kit 仅 `repo-topology-and-naming.md` L140 一行 tag 规则。建议 QT-19 ③ 把兼容矩阵扩为「contracts + kit」两仓。
- **L5** 私有仓前提（`GOPRIVATE`、git 凭证）只在 `repo-inventory.md` L125 登记「QT-07/QT-19 需补」，但 QT-07/QT-19 正文的「③ 要求」均无此项。建议直接补进 QT-07 S2-b 或 QT-19 ③。
- **L6** QT-50 ③ 装配层落点写「`cmd/*/wiring`（或 `pkg/wiring`）」二选一未定；派工单应定死一处（符合「任务内容可独立执行」要求）。
- **L7** `implementation-plan-mvp.md` L41-L51 的「已有地基」以文件名+行号引用现状（如 `factor_def.go:151`），未带完整路径；W0 后行号漂移，建议引用处补仓内相对全路径。

---

## 分维度结论摘要

### ① 设计评估：优（A-）

架构本体（端口化单体 → 触发式服务、契约/工具/实现三层、数据归属 DDD 划分、两层风控、内容寻址三 hash 可复现、短 module path 由编译器守边界）方向正确、克制合理，负面约束明确。扣分点：状态语义四值/三值冲突（C1）、新 HTTP 边界无协议与安全设计（H2）、拓扑终态口径漂移（M5）、依赖方向三处不一（M2）。设计集「自我订正」文化好（D8/D24/D9 均有 banner 订正），但订正不彻底留下了 M1/M2 这类「半更新」文档。

### ② 实现方式评估：优（A-）

改名保历史（clone 而非 archive）、S1 子 module 写最终路径、replace 不传递双侧补、一次一包+编译门、agent-compat 护栏先行、对拍验收、破坏验证——都是对的且可机械验证。扣分点：QT-60 单笔 1153 处替换缺中间检查点（建议按包分批 commit）；H1 的 schema 变更流程缺失会让「实现正确但流程违规」；L6 落点未定。

### ③ 计划评估：良（B+）

六字段工单、硬合入门禁（QT-60→06、QT-18→01、QT-05→07）、依赖实测、「派工前先证伪前提」是同类台账少见的严谨。扣分点：顺序三种读法（M4）、代裁与悬空引用（H3）、产品主目标无落点（H4）、NT 轨道缺失（H5）——后两项是「计划覆盖度」问题，前两项是「可执行性」问题。

### ④ 新仓文档三问（边界 / 外部接口 / 内部功能）

| 仓 | 边界 | 外部接口 | 内部功能 | 计划/实现文档 | 主要缺口 |
|---|---|---|---|---|---|
| `tellurion-contracts` | ✅ 清（§3.3 禁止清单 + C3/C4/C5 + AST 禁 `Config/Options/Impl`） | 🔶 有四契约/port/错误策略，但无随仓 API 清单 | ✅ 布局清（`engine/` 包名沿用、`domain/`、`schemas/`） | ✅ QT-05/07 + 8 步 checklist | M6、M3（SPEC/OpenAPI 归属）、M1（checklist 旧名） |
| `tellurion-kit` | ✅ 清（零业务禁词 + 双判据准入） | 🔶 `kit/db` 有三能力定界，其余 12 包无 API 边界文本 | ✅ 合理 | ✅ QT-05 ④ | M6、L4（版本策略）、L5（GOPRIVATE） |
| `tellurion-data`（X1） | ✅ 清 | ✅ 字段级（`UnifiedDataPoint`/`visible_at`/声明式 connector，D19 质量最高） | ✅ 合理（aggregator/PIT/快照 hash） | 🔶 QT-40 六字段偏薄 | M5（建仓前对账）、M8 |
| `tellurion-strategy`（X2） | ✅ 资产/运行时两分清晰 | 🔶 9 端点有清单，五道门语义散在两文档 | 🔶 缓存三层所有权清楚，编译服务归属未定 | 🔶 QT-41 + D13/D14 | H3（A/D/F/G/H 未决被代裁）、H4（AI 闭环无单） |
| `tellurion-risk`（X3） | ✅ 两层分界清楚 | 🔶 快照下发端点有，规则 schema 未成文 | ✅ 合理（compliance/alert 并入） | 🔶 QT-42 | M8 |
| `tellurion-analytics`（X4） | ✅ 纯函数只读边界清 | 🔶 REST 路径级 | 🔶 合理 | 🔶 QT-43 | M8、代码位置「待核实」（`repo-topology-and-naming.md` L103 自述） |
| `tellurion-orchestration`（X5） | ~~🔶 `cmd/dualrun` 归属未定（自述 L113）~~ ✅ 已裁（2026-10-20 注记：U1 裁决 `cmd/dualrun` 留 core，见 `tellurion:docs/design/qt-decoupling/repo-inventory.md` §3，U1 空号保留） | 🔶 provenance 端点有，job schema 细节欠 | ✅ 合理 | 🔶 QT-44 | ~~dualrun 归属~~、M8 |
| `tellurion-broker`（X6） | ✅ 与风控分界清 | 🔶 port 五方法有签名级描述 | 🔶 通道/风控校验拆分未细化 | 🔶 QT-45 | M8 |

结论：**边界维度普遍达标（得益于「管什么/不管什么」+ 数据归属表的统一写法）；外部接口维度除 data 服务外普遍欠字段级/错误语义/版本策略；「实现文档」对触发式仓是合理缺失，但 contracts/kit 两仓的随仓接口规格（M6）应在 S2 补齐。**

### ⑤ 其他

- **安全**：H2（新 HTTP 面无鉴权）为最大缺口；AI 产物 promote 人工闸门（implementation-plan-mvp L94）随 H4 一并缺位；其余（WASI 不放行、env-only 白名单、路径逃逸防护、`kit` 含 sandbox）良好。
- **测试**：对拍/破坏验证/向量测试/契约测试（QT-09）体系完整；缺性能预算门禁的统一登记（仅 QT-15 ③ 有一处基准要求）。
- **治理/流程**：M9（ADR-D 升格无单）；TASKS 台账自身纪律（归档、六字段、证伪前提）与项目记忆中的台账规则一致，好。
- **文档卫生**：L1/L2/L3；子代理复核显示各设计文档的「正面覆盖率」好，问题集中在跨文档对表。

---

## 修复清单（按开工优先级）

| 序 | 级别 | 事项 | 落点 | 阻塞什么 |
|---|---|---|---|---|
| 1 | Critical | C1 状态语义归一（映射进 completed/invalid/failed 或走正式变更评审） | `TASKS.md` QT-10 ④ / QT-15 ⑤ + `docs/SPEC.md` 三值节 | QT-10/QT-15 开工 |
| 2 | High | H1 冻结 schema 变更挂变更评审 + SPEC 同步规则 | `TASKS.md` 维护节 + QT-02/03/10 | W0 开工 |
| 3 | High | H2 agent↔core HTTP 的 openapi/鉴权/错误语义补单 | QT-50 拆前置单或扩六字段 + D7 订正注 | QT-50、QT-17 ③ |
| 4 | High | H3 待裁决 A/F/D 的假设显式化、删悬空 QT-41b | `TASKS.md` QT-41/QT-26 + `README.md` 待裁决表 | QT-41、QT-26 |
| 5 | High | H4 G6/M2（AI 闭环）立单或显式划出范围 | `TASKS.md` W3 后 | 计划完整性 |
| 6 | High | H5 裁决 C（NT host API）或在 QT-01 声明冻结范围 | `README.md` + QT-01 | QT-01 定稿 |
| 7 | Medium | M1 命名残留替换（§5.1/§7/§8 脚本） | `repo-topology-and-naming.md` | QT-05/QT-07 派工 |
| 8 | Medium | M2 依赖方向统一 + 订正 banner | `target-module-topology.md` L49、`project-engineering-file.md` D2 | QT-05 护栏实现 |
| 9 | Medium | M4 执行顺序改偏序表述 | `TASKS.md` L560 + 总览表 | 多 agent 派工 |
| 10 | Medium | M3/M5/M6/M7/M8/M9 | 各自文档，见条目 | S2 / X 线触发前 |
| 11 | Low | L1~L7 文档卫生 | 各自文档 | 不阻塞 |

**建议的收口顺序**：1→2→3 属 W0 前置（契约/边界类），4→5→6 属计划完备性（可在 W0 并行补文档），7→9 属派工前文档订正（一次性，半天量级），10→11 随迭代消化。

---

## 附：本审查做过的事实抽查

| 抽查项 | 结果 |
|---|---|
| `docs/SPEC.md` L3179-L3198 status 三值语义与读路径同源互锁 | 与 C1 判定一致，冻结文本属实 |
| `quant-trading-agent` 仓现状（go.mod require `quant-trading v0.0.0`、无 AGENTS.md） | 与 `repo-topology-and-naming.md` §6 实测一致 |
| `protocol-and-data-boundary.md` L23 openapi.yaml 归属表述 | 与 M3 判定一致 |
| 子代理三路复核（服务设计篇 / p0 两篇 / 工程档案+内核契约篇） | 服务边界与 P0 契约化程度确认为正面；发现的问题集中在跨文档对表，已并入上文 |

---

## 修复记录（2026-10-20，全部 22 条已修）

> **移植说明**：本报告原写于旧仓 `quant-trading`（将归档），修复按 **per-repo 原则**落到 tellurion* 各仓的当前演化版本。修复文本统一标注「refactor-plan-review X」溯源编号，规则口径冻结在 core 台账、各仓单只引用不镜像。下表为逐条落点，供后续 agent 改动时对账。

| 发现 | 修复落点（per-repo） | 要点 |
|---|---|---|
| **C1** | core `tellurion:docs/TASKS.md` QT-10 ③-④ / QT-15 ⑤；`tellurion:docs/design/qt-decoupling/p0-round2-review.md` 两处 ⚠️ 订正 banner；消费侧 `tellurion-agent:docs/TASKS.md` REV-01-A、`tellurion-ui:docs/TASKS.md` UI-02 | 采纳方案 1：映射进冻结三值（`data_gap_skipped`/`session_terminated`→`invalid`；`ErrStrategyFault`/`ErrContractBreach`→`failed`；`Failures[]` 降为诊断明细；下游只接受 `completed`），不引入第四值 |
| **H1** | core `tellurion:docs/TASKS.md`「维护本文件」冻结 schema 变更评审硬规则 + QT-02/QT-03 补「schema 变更评审项 + SPEC 节号」 | 合规测试红 → 更新 SPEC → 正式变更评审 → 才许合入 |
| **H2** | core `tellurion:docs/TASKS.md` QT-50 ③④⑤ 重写 + QT-17 ③-③ 补注；`protocol-and-data-boundary.md` D7 ⚠️ 订正注；消费侧 `tellurion-agent:docs/TASKS.md` REV-01-A | 六/七类粗粒度动作落 `docs/openapi.yaml` + 路由表对拍 + `oapi-codegen` 生成 `contracts/dto` 禁手改；JWT+RBAC+CORS 白名单+loopback-only fail-closed |
| **H3** | core `tellurion:docs/TASKS.md` QT-41 ③ A/F 标「假设，需裁决」+ QT-41b 新单（⛔ 阻塞于待裁决 D）+ QT-26 标依赖；`README.md` 待裁决表补「阻塞单」列 | 代裁与悬空引用显式化 |
| **H4** | core `tellurion:docs/TASKS.md` QT-32 新单（AI 策略生成闭环 + promote 人工闸门 + 全链留痕） | 承接 G6/M2 产品主目标 |
| **H5** | core `tellurion:docs/TASKS.md` QT-01 ⑥ + `README.md` C 行 | 声明「契约冻结仅覆盖 Go 内核 + WASM 载体，NT 侧对齐待裁决 C」 |
| **M1** | 告知：tellurion 侧 §5.1/§7/§8 已由 QT-60 改名迁移机械解决，仅剩历史注记（不改写） | 无需重复替换 |
| **M2** | `target-module-topology.md` L49/L104 ⚠️ 订正 banner；`project-engineering-file.md` ADR-D2 订正 banner | 最终不变量 `kit ← contracts(含 domain) ← core`，`agent → kit + contracts`（编译期）+ `HTTP → core`（运行期） |
| **M3** | `repo-inventory.md` §3 已裁边界表补两行 | `docs/openapi.yaml` 归 core（真相源）、contracts 只收 `dto/` 禁手改；`docs/SPEC.md` 归 core、S2 后与 contracts `engine/` 互锁 |
| **M4** | core `tellurion:docs/TASKS.md`「维护本文件」执行顺序改偏序 + 总览表 | 硬链 S-1→QT-61→S0→W0→S1；W1 不依赖 S2；仅 QT-17 可与 W1 并行 |
| **M5** | `repo-inventory.md` §1 口径澄清 blockquote；X 线 6 仓 `docs/TASKS.md` 各加开工前置指针注 | 12 仓是上限/当前规划非承诺终态；建仓/升格前与 ADR-027 §2 对账 |
| **M6** | core QT-07 ③-S2-c 补项；`tellurion-contracts:docs/TASKS.md` CT-01、`tellurion-kit:docs/TASKS.md` KT-01 | contracts 建 `docs/API.md`（导出面+稳定性+错误语义），kit 建 `docs/SCOPE.md`（12 包准入/禁入），随 AST 护栏更新 |
| **M7** | core `tellurion:docs/TASKS.md` QT-60 验收 | 改「旧仓 commit 数 + N（预期 1~4）」+ md 替换机械判据（`quant-trading:` 前缀仅允许在 `docs/archive/` 与历史注记） |
| **M8** | core `tellurion:docs/TASKS.md` X 线公共前置 ② + X 线 6 仓指针注 | 设计文档补「接口对表」小节（签名+错误语义+版本策略，模板 UnifiedDataPoint 节），未对表不开工 |
| **M9** | `tellurion-docs:docs/TASKS.md` DOC-09 | ADR-D24/D25/D29/D30（D31 可并入）升格为正式 ADR，升格时核对现行裁决 |
| **L1** | `README.md` D29 行压缩 + 表下脚注¹ | 修表格断行渲染错位 |
| **L2** | `README.md` L23/L103 描述口径 | 「6 仓拓扑」→「现 4 库；12 仓为上限/当前规划」 |
| **L3** | `repo-topology-and-naming.md` §1/§3.1/§6 | 数量口径与 §6 表 contracts/kit 行（「已建 2026-10-20 占位，代码待 S1 落」）对齐现实 |
| **L4/L5** | core QT-19 ③⑤⑥ + QT-07 ③-S2-c；`tellurion-contracts:docs/TASKS.md` CT-02、`tellurion-kit:docs/TASKS.md` KT-02 | 兼容矩阵扩为 contracts + kit 两仓同表；`GOPRIVATE=github.com/ruoxizhnya/*` + git 凭证前提落 QT-07 |
| **L6** | core `tellurion:docs/TASKS.md` QT-50 ③ | 装配落点定死 `cmd/*/wiring`（不放 `pkg/wiring`） |
| **L7** | `implementation-plan-mvp.md` §2「已有地基」表 | 位置列补仓内相对全路径（认路径不认行号） |

**复查口径**（全量 grep 后无残留）：`degraded` 仅剩修复文本自身的否定措辞；`quant-trading-contracts` 旧仓名 0 命中；`tellurion → tellurion` 自指错误已修；QT-41b 悬空引用已消除。

**遗留项处置（2026-10-20 三条收尾，随本注记清零）**：

1. **旧仓 `quant-trading` 工作树**：5 个修复草稿（`docs/TASKS.md` + `qt-decoupling/` 四篇）已全部移植到 tellurion* 各仓后 `git restore` 丢弃，恢复至最后提交态；未跟踪的本报告原件保留于 `quant-trading:docs/design/qt-decoupling/refactor-plan-review-2026-10-20.md` 作为历史出处，权威版本以 `tellurion:docs/design/qt-decoupling/refactor-plan-review-2026-10-20.md`（本文件）为准。
2. **并行 agent 协调核实**：tellurion 工作树改动集与并行 agent 已提交工作（aa3fe85 U1 裁决、f574227/8e2d19f/3945b4d 等）无未提交交织。其中 U1 裁决（`cmd/dualrun` 留 core）晚于本报告成文，④表 X5 行已随本注记订正；顺带说明：该裁决还更正了 U1 原理由（dualrun 读的是回测内成交明细，不碰实盘 `quant.orders`/`fills`），不影响本报告任何结论。
3. **旧仓名残留清零**：`tellurion-agent:docs/TASKS.md` OBS-01-R2-A ② 内「core 仓 `quant-trading`」已订正为 `tellurion`（同族残留：同文件头部「六字段规范」行一并订正）；`tellurion:docs/design/qt-component-diagram/` 三张架构图（`tellurion-overview.mmd` / `tellurion-components.mmd` / `index.html`，两个 .mmd 已随 QT-60 同步改名）仓库标签已迁为 tellurion 系，旧名仅存于带日期的「2026-10-20 注记」映射段——符合 M7 机械判据（`quant-trading` 前缀仅允许出现在 `docs/archive/` 与带日期历史注记）。
