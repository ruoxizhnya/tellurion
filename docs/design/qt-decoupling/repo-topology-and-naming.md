# 分仓拓扑与命名（repo = 模块/服务，边界由 contract 约束）

> 建立：2026-10-20 ｜ 状态：**已裁决（用户 2026-10-20 确认三处判断）**，实施按 `docs/TASKS.md` 的 S0~S3 阶梯
> 用户裁决：**仓库之间代表独立模块和服务，服务之间用 contract 限制**；拆仓的目的是**让单个 agent 能独立完成单个 repo 的任务**。
> ⚠️ **命名以 [repo-inventory.md](repo-inventory.md) 为准（ADR-D30，2026-10-20 用户裁决）**：项目定名 **tellurion**，owner 保持 `ruoxizhnya` 不变（不建 org、不用 vanity 域名），**应用/服务用单段短名、库用全名**。**本篇正文中的 `quant-trading-*` 是改名前的历史名字**；拓扑（谁依赖谁）、五条不变量 R1~R5、S0~S3 阶梯、跨仓规则 C1~C5 **全部不变**，只换名字。
> 关联：[target-module-topology.md](target-module-topology.md)（D9/D24，本篇修正其 module 归属）、[protocol-and-data-boundary.md](protocol-and-data-boundary.md)（D7 协议分层）、ADR-027（服务拆分判据）、ADR-031（四契约）

---

## 0. 裁决落档（2026-10-20，用户确认）

| # | 事项 | 裁决 | 落点 |
|---|---|---|---|
| M1 | `domain` 的归属 | ✅ **并入 contracts 仓**（子包 `contracts/domain`）；module 数 5 → 4（`kit → contracts → core`）。**替代 D24 第 1 条**，D24 第 2/3 条不变 | §5、QT-05 |
| M2 | 版本化起点与 `replace` | ✅ **S0 就打 `v0.1.0`，但 `replace` 保留**；删除前提是 QT-50（HTTP 粗粒度）完成，不写成「改一行」 | §7-S0/S3、QT-06、QT-07 |
| M3 | 拆仓时机与路径 | ✅ **S1 排在 W0 之后**；**ADR-D31 改形态：代码直接落两个新仓**（`require` 伪版本 + 本地 `go.work`，**无 replace**），S2 只把伪版本换成 `v0.1.0` | §7、QT-05、QT-07 |

---

## 1. 先把三条概念钉死（这是过去混乱的根源）

| 概念 | 是什么 | 边界由什么保证 | 数量 |
|---|---|---|---|
| **repo（仓）** | agent 的工作上下文单位 | git 边界 + 本仓 `AGENTS.md`/CI/`TASKS.md` | **现在 4 个** → S2 后 6 个 → 终态 12 个 |
| **module（Go module）** | 编译单元，决定"能不能 import" | `go.mod` + 依赖方向护栏 | 每仓 1 个（仓内不再多 module） |
| **service（进程）** | 部署单元 | contract + 网络边界 | 现在 1 个，X 线起进程时才增加 |

**规则**：仓 = 模块/服务；**终态一个仓一个 go module**；服务的 contract 装在 `tellurion-contracts` 仓。**「同仓多 module + `go.work`」既不必要也不算终态**——ADR-D31 之后 S1 就是"代码直接落新仓"，core 从第一天起就是 1:1，`go.work` 只存在于开发机（不入库）。
> 措辞澄清（**已随 ADR-D31 作废，原文保留追溯**）：原写「S1 期 core 仓内会暂时有 3 个 go module（`core` / `./contracts` / `./kit`）」。Plan B 之后**不存在这个中间态**：S1 起 core 只有 1 个 module，contracts / kit 住在自己的仓里。

---

## 2. 目标：什么样的仓才算"一个 agent 能独立干完"

五条硬不变量，每条都能机械检查（写进 `tools/check_repo_autonomy.py`，见 QT-08）：

| # | 不变量 | 机械判据 |
|---|---|---|
| R1 | **单仓可构建** | 只 checkout 本仓 + 公网依赖，`go build ./...` 绿；`go.mod` 内 `replace` 指向仓外路径 = 红 |
| R2 | **单仓可自证** | `go test ./...` 绿；本仓有 CI；不依赖"另一个仓先跑一遍" |
| R3 | **有行为入口** | 仓根有 `AGENTS.md`：构建/测试命令、模块边界、**依赖的契约版本**、禁止事项、`docs/TASKS.md` 指针 |
| R4 | **有任务台账** | 本仓 `docs/TASKS.md` 只放本仓可完成的单；跨仓事项只留指针 |
| R5 | **契约是版本依赖** | 依赖 `tellurion-contracts vX.Y.Z`；升级 = 改 require + 跑测试，**不需要读契约仓源码** |
| R6 | **功能边界有契约**（2026-10-20 新增） | 仓内存在 `docs/db-schema.md` / `docs/api-schema.md` / `docs/dataflow.md` 三份且非空；`db-schema.md` 必须明确"本仓拥有哪些表 / 为什么不拥有表"；三份互相引用且被 `docs/README.md` 索引 |

> R1 对 `tellurion`（core）本仓天然成立；对 `agent` **目前不成立**（实测见 §6），这是它至今"移出去了但没真在上面工作"的直接原因。

> **R6 的由来**（2026-10-20）：R1~R5 只保证"这个仓能自己跑起来"，不保证"这个仓知道自己该做什么"。一个 agent 拿到只有 AGENTS + TASKS 的仓，仍然无法判断"某个功能该不该放这"——那要靠 db / api / dataflow 三份契约把边界画死。R6 就是把这个判断变成可检查的文件存在性。

---

## 3. 目标仓拓扑

### 3.1 现在（6 仓）

```
第 1 层   ┌──────────────────────────────┐
（底座）   │  tellurion-kit           │  横切工具，零业务概念
          │  logging · db · sandbox …    │  不持有任何表
          └──────────────▲───────────────┘
                         │ 被 contracts 与 core 依赖
第 2 层   ┌──────────────┴───────────────┐
（契约）   │  tellurion-contracts     │  port · DTO/值对象 · 哨兵错误
          │  （含 domain/ 与 engine/）     │  abi 常量 · OpenAPI 源
          └──────▲───────────────▲───────┘
                 │               │
第 3 层   ┌──────┴───────┐  ┌────┴──────────────┐
（实现）   │ tellurion│  │ tellurion-agent│
          │ core         │  │ 实验员（AI 链路）   │
          │ 引擎/数据/风控 │  │ 编译期只依赖        │
          │ 策略运行时/存储│  │ kit + contracts    │
          └──────▲───────┘  └────────┬──────────┘
                 │                   │
                 └──── HTTP 粗粒度 ───┘   ← 运行期唯一通道，非 import

第 4 层   tellurion-ui（纯 HTTP，无 go.mod）   tellurion-docs（文档，无代码）
```

| 依赖边 | 类型 | 允许？ |
|---|---|---|
| `contracts` → `kit` | 编译期（仅 `decimal`/`errors` 等纯工具） | ✅ 不得取业务包 |
| `core` → `kit` / `contracts` | 编译期 | ✅ |
| `agent` → `kit` / `contracts` | 编译期 | ✅ |
| `agent` → `core` | **运行期 HTTP 粗粒度**（提交回测/批量/因子求值/行情查询/策略拉取/因子登记） | ✅ 仅此六类 |
| `agent` → `core` | 编译期 import | ❌ |
| `agent` → `core` | per-bar / per-symbol / per-order 远程调用 | ❌ 硬否决 |
| `ui` → 任何仓 | HTTP | ✅ |
| `contracts` → `core` / `agent` | 任何 | ❌ 反向即红 |
| `kit` → 任何业务 | 任何 | ❌ |

### 3.2 终态全景（12 仓 = 现有 4 + 待建 2 + 服务 6）

> 澄清一个容易误读的点：**「X 线 6 个服务仓」是「+6」，不是「终态只有 6 个」**。ui / agent / docs 已经是独立仓，所以不在「待拆」清单里——它们不因为服务拆分而改仓，只改调用方式。

| # | 仓 | 状态 | 来源（拆自 core 的哪里） | 何时建 |
|---|---|---|---|---|
| 1 | `tellurion` | **现有** | —（保留：回测引擎 / 策略运行时 / 撮合 / 组合结算 / 仪器域存储 / 内核装配） | 已有 |
| 2 | `tellurion-contracts` | **已建（2026-10-20 占位）** | `pkg/domain`（17 文件）+ `pkg/backtest/contracts`（6 文件）；**core 顶层 5 个数据契约文件不迁**（运行时按相对路径加载，见 ADR-D31） | **S1**（QT-05）落代码，S2（QT-07）打 tag |
| 3 | `tellurion-kit` | **已建（2026-10-20 占位）** | 12 个横切包 + 新增 `kit/db` | **S1**（QT-05）落代码，S2（QT-07）打 tag |
| 4 | `tellurion-agent` | **现有** | —（应用层：实验员） | 已有；S3 去 replace（QT-07） |
| 5 | `tellurion-ui` | **现有** | —（应用层：前端） | 已有；服务拆分后只改 HTTP 端点 |
| 6 | `tellurion-docs` | **现有** | —（决策/规范） | 已有 |
| 7 | `tellurion-data` | 触发式 | `cmd/data`（**已有进程入口**，:8081）+ `pkg/marketdata` + `pkg/data/*` | 需独立伸缩/部署时 |
| 8 | `tellurion-strategy` | 触发式 | `pkg/strategy` 的**定义/资产一半**（版本、artifact、resolve）；**运行时留 core** | 同上 |
| 9 | `tellurion-risk` | 触发式 | `pkg/risk` 的**规则真相源**；**`RiskPolicy` 同步裁决留 core** | 同上 |
| 10 | `tellurion-analytics` | 触发式 | `cmd/analysis`（**已有进程入口**，:8085）+ 归因/基准纯函数（新增部分见 `analytics-service-design.md`，**代码位置待核实**） | 同上 |
| 11 | `tellurion-orchestration` | 触发式 | `pkg/backtest/batch` + `pkg/backtest/walkforward` + `cmd/dualrun`（**归属待裁决**，见下） | 同上 |
| 12 | `tellurion-broker` | 触发式 | `pkg/live` + `pkg/execution` 的实盘侧 | 同上 |

**触发条件**（ADR-027 / D20 口径）：该服务需要独立伸缩或独立部署，且**只走高/低频路径**——逐 bar、逐笔、风控裁决**永驻 core 进程内**。**未触发前不建仓**：空壳仓会让 agent 无事可做，反而制造「跨仓才能验证」。

**三个必须注意的点**：

1. **`data` 与 `analytics` 已经是有 `main` 的进程**（`cmd/data` / `cmd/analysis`，各有 Dockerfile），拆仓成本最低；另外四个（strategy / risk / orchestration / broker）目前只有库，拆之前要先有 `main`。
2. **`pkg/strategy` 与 `pkg/risk` 是「切一半」而不是「整包搬」**：定义/真相源出去，运行时/同步裁决留 core（D6 / D20）。把整包搬走会让内循环变成远程调用（硬否决）。
3. **`cmd/dualrun`（K15 双跑）归属未定**：它同时依赖两个内核的进程内调用，既像 orchestration 的能力，又必须留在能同时链接两个内核的地方。**下次评估 X5 时必须先回答它归谁**，否则双跑会在拆分中失去运行位置。

### 3.3 每仓的职责与禁止

| 仓 | 放什么 | 禁止 |
|---|---|---|
| `tellurion-contracts` | `EngineRunner`/`SignalProvider`/`ExecutionPort`/`BrokerPort`/`DataPort`；`RunSpec`/`SignalSpec`/`ResultSpec`；值对象（`BacktestResult`/`OHLCV`/`Trade`/`Factor*`/`Direction`…）；哨兵错误；abi v2 常量与上限；OpenAPI 源 | 实现、业务规则、DB 访问、任何 `Impl`/`Config`/`Options` 类型 |
| `tellurion-kit` | logging · errors · id · decimal · statistics · httpclient · httpserver · auth/authz · observability · clock · sandbox(staticcheck/runner) · **db**（仅 connection/migrate/DSN） | 任何业务概念（backtest/strategy/order/factor/market…）；任何表结构 |
| `tellurion`（core） | 引擎、行情、风控、策略运行时、存储、因子、`sandbox/wasm`、`domain` 中**仅 core 内部使用**的类型 | import agent/ui；装配逻辑留 `pkg/kernel`（QT-50 外移） |
| `tellurion-agent` | 实验循环、基因池、验证、工具编排、Copilot、`ai.*` schema | import core 实现包；逐 bar/逐笔远程调用；直连 core 表 |
| `tellurion-ui` | 前端 | 直连 DB |
| `tellurion-docs` | ADR、SPEC、指南、跨仓导航 | 代码 |

---

## 4. 命名规则（已被 ADR-D30 更新）

> ⚠️ 本节原为 D29 的规则（`quant-trading-<能力>`、`github.com/ruoxizhnya/<仓名>`）。2026-10-20 用户定名 **tellurion** 后，**仓名与 module path 的唯一真相源是 [`repo-inventory.md`](repo-inventory.md)**；下表是更新后的版本，D29 原文仅作历史。

| 对象 | 规则 | 例 |
|---|---|---|
| git 仓 | `tellurion-<角色>`；kebab-case；**单数**；**不带 `-service` 后缀** | `tellurion-contracts`、`tellurion-agent` |
| core 仓 | **就叫 `tellurion`**，不加 `-core` | 避免「到底谁是 core」 |
| **应用 / 服务** module path | **单段短名**（第一段不含点）→ 编译器拒绝被 `require` | `tellurion`、`tellurion-agent`、`tellurion-risk` |
| **库** module path | `github.com/ruoxizhnya/<仓名>` | `github.com/ruoxizhnya/tellurion-contracts` |
| 仓内目录 | 按**能力**分包，不按层分（`logging/` 而非 `pkg/logging/`）；**contracts/kit 仓根即 package** | `contracts/engine.go`、`kit/logging/` |
| 服务名（进程/schema/指标前缀） | 与仓名同词干，去掉 `tellurion-` 前缀 | 仓 `tellurion-data` → 服务名 `data`；schema 已有 `market/ingest` 不动 |
| 版本 tag | 语义化 `vMAJOR.MINOR.PATCH`；contracts/kit 独立打 tag，**core 不代打** | `v0.1.0` |
| 分支 | `main` + 短命特性分支；跨仓变更用**同名分支**（便于对拍，非强制） | `feat/fill-timing` |

#### 4.1 为什么应用用短名、库用全名（实测依据）

| 实验（Go 1.25.0） | 结果 |
|---|---|
| `module tellurion` 单仓内 `import "tellurion/pkg/b"` | ✅ build / tidy / vet 全绿 |
| 外部 `require tellurion v0.0.0`（无 replace） | ❌ `malformed module path "tellurion": missing dot in first path element` |
| core `module tellurion` + `require github.com/ruoxizhnya/tellurion-contracts` + replace | ✅ 共存、build 绿 |
| `import "contracts/kit"`（省略 module path） | ❌ `package contracts/kit is not in std` |

结论：**「短」只能靠 module path 本身短，不能靠省略前缀**；而短名恰好让编译器替我们守住「应用不可被 import」这条边界（D24）。

#### 4.2 owner 段：保持 `ruoxizhnya`（用户 2026-10-20 裁决）

- **不改 owner**：不建 GitHub Org、不用 vanity 域名。理由：改名本身已有一次性成本，owner 再变会牵动所有 `require` / CI / 文档，而收益（更短的 path）不值得。
- **已登记的代价**：若将来 GitHub 账号改名，写进 `go.mod` 的 path 不会自动跟着变（git 的 302 只覆盖 git 协议，module proxy 按 path 拼 URL），需要一次全仓替换。
- 因此 D29 原文里「建议顺手换 org」的那段**已被本条否决**。

**两条负面约束**：
1. 不叫 `tellurion-domain`（domain 并入 contracts，见 §5）；
2. 不为"将来可能要拆"提前建空仓。

---

## 5. 裁定：`domain` 并入 contracts 仓（**修正 ADR-D24 第 1 条**）

- 场景：D24 裁决 `domain` 独立为第 5 个 module。按"仓 = 模块/服务、跨边界只走 contract"重审后，这个归属站不住。
- 实测证据：
  - `pkg/domain` 共 17 文件 / 3983 行，被 **core 内 306 个文件**、**agent 内 48 个文件**使用；
  - agent 用到的全是**跨边界 DTO**：`BacktestResult`(101) · `OHLCV`(26) · `MarketRegime`(24) · `PortfolioValue`(23) · `WalkForwardReport`(14) · `Trade`(13) · `Factor*`(10+) · `Direction`(7+)；
  - `pkg/domain` 自身只依赖 `domain/market`（子包）与 `fees`（2 处）。
- 决策：**`domain` 整体进 `tellurion-contracts` 仓，作为仓内子包 `domain/`（同一 go module）**。理由：
  1. 它被两个仓同时使用 ⇒ 按定义就是契约，不是"某个仓的内部实现"；
  2. 它与 contracts 的变更节奏完全同步（改值对象 = 改契约），分成两仓只会让一次改动变成两次发版；
  3. 它依赖 `fees`（费率模型）是"业务规则漏进契约"的征兆，必须借此机会切开：**费率的数据形状进 contracts，费率计算留 core**。
- 代价：306 处 import 路径机械替换（`tellurion/pkg/domain` → `tellurion-contracts/domain`），一次 sed + `go build ./...` 验证；`fees` 需拆成"类型/计算"两半。
- 状态：**已裁决**（用户 2026-10-20；替代 D24-1；D24 的第 2、3 条 —— agent↔core HTTP 粗粒度、策略线先资产后接线 —— 不变）

#### 5.1 两个必须一次定死的实施细节

| 项 | 定法 | 为什么 |
|---|---|---|
| **包名** | `contracts/engine/` 目录内 **`package contracts`**（现 `pkg/backtest/contracts` 的包名就是 `contracts`，已核实）；`contracts/domain/` 内 `package domain` | 若改成 `package engine`，本仓 50 处 + agent 5 处调用点全要改**前缀**；保留包名则只在 import 处加 alias，调用点一行不动 |
| **agent 侧依赖** | agent 必须**自己** `require` 两仓（S1 伪版本 / S2 之后 tag） | **`require` 与 `replace` 都不传递**：core 的依赖不会传给 agent，漏了这条 S1 一完成 agent 立刻编不过。ADR-D31 之后不用 replace，agent 用自己仓根的 `go.work`（含 `../tellurion`）做本地联调 |

#### 5.2 与 C4「只收被 ≥2 仓使用」的张力（诚实登记）

实测 agent **完全不用** domain 的 `screen` / `sector` / `supply_chain` / `macro` / `mining`（各 0 命中）——它们是 core 专用类型。整包迁入会让 contracts 变大、偏离开篇的 C4。

处置：**S1 仍整包迁**（一次 306 处替换，比拆两次便宜），同时输出「core-only 类型清单」；S2 之后按清单把 core 专用类型搬回 core（范围仅这 5 个文件相关，二次 import 改动量小）。**不因此把 S1 改成"按类型分包"**——那会让第一次替换就要分辨 306 处的去向。

**module 数因此从 5 回落到 4**：`kit`（底座）→ `contracts`（含 domain）→ `core`；`agent → kit + contracts`（编译期）+ HTTP（运行期）。这比"五 module"少一层，且依赖方向更严格（contracts 不依赖 core 的任何东西）。

---

## 6. 现状差距（2026-10-20 实测，不是推测）

| 仓 | `go.mod` | replace | tag | AGENTS | TASKS | CI | 差距 |
|---|---|---|---|---|---|---|---|
| `tellurion` | `tellurion` | 0 | **0** | ✅ | ✅ | ✅ | **没有任何版本可被依赖** |
| `tellurion-agent` | `...-agent` | **1（→ `../tellurion`，require `v0.0.0`）** | 0 | ❌ | ✅ | ✅（**checkout core 并改写 replace**） | R1/R3/R5 全不成立 |
| `tellurion-ui` | 无（前端） | — | 0 | ❌ | ✅ | ✅ | R3 |
| `tellurion-docs` | 无 | — | 0 | ❌ | ❌ | ❌ | R2/R3/R4 |
| `tellurion-contracts` / `-kit` | — | — | — | — | — | — | **尚不存在** |

补一条决定顺序的事实：**agent 目前 import core 的 20 个包 / 37 个文件**（`domain` 48 处、`storage` 14、`strategy` 11、`expression` 10、`data/source` 6、`backtest` 5…）。
⇒ **删掉 `replace` 不是"改一行"，而是"agent 先不再 import core 实现包"**。所以 agent 的单仓自治排在 S3（QT-50 HTTP 化之后），不是现在。

---

## 7. 迁移阶梯（S0 → S3，每级都可单独验收、可停）

| 级 | 做什么 | 前置 | 完成后得到什么 | 任务 |
|---|---|---|---|---|
| **S0** | core 打 `v0.1.0`；agent `require v0.1.0`（**replace 保留**，标注「仅本地/CI 联调」）；**core CI 加 `agent-compat` job**；四仓补 AGENTS/CI/TASKS 与自检脚本 | 无 | agent 有**版本**可依赖；**W0 的 breaking 一发生就会在 core 侧红**；四仓都有 agent 行为入口 | QT-06 → **QT-18** → QT-08 |
| **S1** | 代码**直接落两个新仓**：`pkg/domain` → `contracts/domain`、`pkg/backtest/contracts` → `contracts/engine`、`fees` 类型/计算分离、12 个横切包 + `kit/db`；core/agent 改 `require` 伪版本（**无 replace**，本地用 `go.work`） | W0 契约冻结（QT-01~04） | **契约与工具已在独立仓**，core 与 agent 通过版本依赖取用 | QT-05 |
| **S2** | 两仓打 `v0.1.0`；core/agent 把伪版本换成 tag + `go mod tidy`；两仓补 CI/examples/TASKS；core 加反向护栏 | S1 全绿 | contracts/kit **单仓可构建可自证**（R1~R5 全成立）且**以 tag 被依赖** | QT-07 / QT-09 |
| **S3** | agent 删 `../tellurion` replace；CI 删 checkout core 步骤；`ai.*` schema 自持 | QT-50（HTTP 粗粒度）+ QT-05 + **QT-17（数据自持）** | **agent 真正单仓可完成** | QT-07 的收尾段 |
| S4 | X 线服务各自成仓 | 真起进程（ADR-027 触发条件） | — | 按 §3.2 触发时立项 |

**S1 的两个硬要求（避免二次重写）**：

1. 两仓的 `go.mod` **从建仓起就是最终路径** `github.com/ruoxizhnya/tellurion-contracts` / `-kit`（已就位）。这样 S1 的 306 处 import 替换只做一次，S2 只换版本号。若用 `tellurion/contracts` 之类临时路径，就要做第二次全仓重写。
2. **core 与 agent 都要各自 `require`**（依赖不传递）：漏一条立刻编不过；本地联调靠**各自仓根的 `go.work`**（含 `../tellurion-contracts`、`../tellurion-kit`，agent 另含 `../tellurion`），**一律不入库**。

> **ADR-D31 之后这两条仍然成立，只是第 2 条的手段从 `replace` 换成 `require` + `go.work`**：`replace` 在任何仓都不是手段（QT-09 ① 会判红），跨仓依赖一律走版本号。

**合入门禁**：**QT-18 未合入 main 前，W0 的 QT-01 不得合入**。QT-01 就是 abi v2（8 → 13 个 host 函数），是分仓后第一个打穿 agent 编译的改动；`agent-compat` job 不在，这次破坏就已经漏过去了，等发现时 agent 仓已经烂了若干个提交。

**冻结纪律**：S1 必须在 W0（契约冻结）**之后**。理由不是"频率会自然下降"，而是 **S1 的动作本身就是"把契约形状定下来"——那是 W0 的产出；先做一次，别做两次**。

---

## 8. 建仓 checklist（S2/S3/S4 通用，8 步不可换序）

```bash
# 1) 建仓 + 初始化（owner 沿用现有 module path）
mkdir tellurion-contracts && cd tellurion-contracts
git init -b main
go mod init github.com/ruoxizhnya/tellurion-contracts
git add -A && git commit -m "chore: 初始化契约仓（从 tellurion 迁出）"
git tag -a v0.1.0 -m "contracts v0.1.0: 从 core 迁出的 port/DTO/错误/常量"

# 2) 本地联调：go.work 只在开发机建，.gitignore 必须忽略
go work init . ../tellurion            # 下游仓：把上游仓加进 workspace（仅本地）
```

| # | 步骤 | 验收 |
|---|---|---|
| 1 | `git init` + `go mod init` + 首 tag `v0.1.0` | 仓可被 `go get` |
| 2 | 只搬**契约层**（port/DTO/错误/常量），实现留 core | 新仓无 core import |
| 3 | 下游 `require <版本>`（S1 伪版本 → S2 换 `v0.1.0`），**不写 replace** | 无 `=>` 且 `go build ./...` 绿 |
| 4 | 新仓 CI 复制主仓门禁（`gofmt` / `go vet` / `go test` / repoguard） | CI 绿且不 checkout 其他仓 |
| 5 | 补 `AGENTS.md` + `docs/TASKS.md`（R3/R4） | `tools/check_repo_autonomy.py` 通过 |
| 6 | 主仓加反向护栏（禁止 import 已迁出路径） | 反向 import 编译或护栏失败 |
| 7 | 数据/行为对拍（同参运行结果逐位一致） | 对拍报告归档 |
| 8 | 三处登记：`tellurion-docs:docs/README.md` 导航、主仓 `AGENTS.md` 路由、`docs/TASKS.md` 跨仓索引 | 三处均有该仓 |

---

## 9. 跨仓依赖规则（这五条是"contract 限制"的落地）

| # | 规则 | 违反后果 | 检查 |
|---|---|---|---|
| C1 | 跨仓只认 **tag**，不认路径 | 单仓构建失败（R1） | `go.mod` 里 `replace` 指向仓外 = 红 |
| C2 | `go.work` **不入任何仓** | CI 与别人机器上构建行为不一致 | `.gitignore` + CI grep |
| C3 | 契约变更的**触发者必须是使用方的新需求**，不能是实现方的重构 | 实现细节漏进契约 → contracts 变 common 包 | 契约 PR 模板必填"哪个使用方需要" |
| C4 | 契约准入：只收**被 ≥2 个仓使用**且**不含实现**的东西 | 同上 | review 判据 |
| C5 | 破坏性变更（breaking）必须**保留旧接口一个版本周期** | 两仓必须同时改才能编译 = agent 无法独立完成 | 契约仓保留 deprecated 符号至下一 minor |

**C3 的机械推论**：若某次契约变更是因为"core 内部想换数据结构"，说明**实现细节漏进了契约**——修契约，不要修流程。

---

## 10. 风险与代价

| 风险 | 说明 | 兜底 |
|---|---|---|
| 拆仓后"顺手改另一边"的路没了 | 边界错误会以"契约里塞实现"的形式沉淀 | C3/C4 + 契约仓禁止 `Config/Options/Impl`（AST 扫描） |
| 实现假设漂移 | 两边各写一份理解，K15 双跑才发现 | 契约仓带**契约测试**：同一组用例喂两个实现（QT-09） |
| 下游看不到调用方 | agent 在 core 仓写代码，看不见 agent 怎么用 | 契约仓放 `examples/`，每个 port 一个最小用法 |
| S1 期间两套 import 路径 | 迁移中 `pkg/domain` 与 `contracts/domain` 并存（跨仓） | S1 一次 sed 全替 + 编译卡口，禁止"新老混用"进 main |
| 306 处 import 替换出错 | 机械替换可能漏测试文件 | 按包搬、每搬一次 `go build ./...` + `go vet ./...`；repoguard 加「禁止旧路径」 |
| **S1 期依赖不传递** | 只给 core 加 `require`，agent 立刻编不过 | QT-05 要求⑤：agent 自己 `require` 两仓 + 仓根 `go.work`，验收以「agent 能 build」为准 |
| **过渡期 core↔agent 连体** | S0~S3 期间 agent 编译依赖 core 源码，而 W0/W1 全是 breaking 改动 | QT-18：core CI 加 `agent-compat` job（build-only），break 必须当次修 |
| contracts 因整包迁入而膨胀 | 与 C4「只收 ≥2 仓使用」冲突 | §5.2：S1 整包迁 + core-only 清单，S2 后回收 |
| **契约 pin 指向未合入提交**（ADR-D31 的新代价） | S1 期 core 依赖伪版本，伪版本可以指向任意 commit（含未评审分支） | `tools/check_contract_pin.py`：pin SHA 必须是该仓 `main` 的祖先；QT-09 ① 接进 CI |
| **两仓转私有会同时打断 core 的 CI 与镜像构建**（ADR-D31 的硬依赖） | S1 起没有本地目录兜底，core 靠 `go.sum` 去拉公开仓 | 转私有必须同步配 `GOPRIVATE` + CI/Docker 拉取凭证；写进两仓 `AGENTS.md` 的「不要随手改可见性」 |

---

## 11. 与既有裁决的关系

| 裁决 | 关系 |
|---|---|
| ADR-027（模块化拆解 → 服务） | 本篇是其"何时才配拥有一个 repo"的判定表；服务拆分判据不变 |
| D7（contracts 进程内 / OpenAPI 跨进程） | 不变；本篇确定 contracts 的**仓位置** |
| D9（四 module 拓扑） | 依赖方向沿用；`domain` 位置由本篇修正 |
| **D24 第 1 条（五 module、domain 独立）** | **被本篇替代**（§5）；D24 第 2/3 条不变 |
| D10（实验域数据归 agent，自建 `ai.*`） | 不变；agent 仓需 `kit/db` 才能脱离 core 的 store（K 裁决 → 进 `kit/db`） |
| ADR-031（四契约） | `RunSpec`/`SignalSpec`/`ResultSpec`/`EngineRunner` 落在 contracts 仓 `engine/` 子包 |
