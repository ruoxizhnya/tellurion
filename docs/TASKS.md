---
status: active
last-verified: 2026-10-20
verified-by: 第 4 代换代（2026-10-20）；**2026-10-20 补分仓批次（ADR-D29 / QT-05~09）与改名批次（ADR-D30 / QT-60）**。来由：ADB-029 abi v2 裁决（D24/D25/D26/D28）+ P0 第二轮深度审查（`docs/design/qt-decoupling/p0-round2-review.md`，N1~N9）。本代 30 张新工单**全部可追溯到设计文档的具体节**（无一条来自猜测）；第 3 代 6 项（DBT-13~18）经前提复核后原样延续。第 3 代正文已归档到 `archive/TASKS-2026-10-20-archived.md`。
---

# TASKS — 未完成项（第 4 代）

> **只放未完成项**，完成即标 ✅（附日期）并移入 `archive/`。
> 历史代：[第 1 代 35 项](archive/TASKS-2026-10-14-archived.md)、[第 2 代 27 项](archive/TASKS-2026-10-16-gen2-archived.md)、[第 3 代 6 项](archive/TASKS-2026-10-20-archived.md)（DBT 系列前提核验与证伪台账在归档内，**写 DBT 单前先读那里**）。
> 状态：`⬜ 待做` / `🔶 进行中` / `⛔ 阻塞（有未解除前置）` / `✅ 完成（附日期）`
> 每张 task 六字段（目标/上下文/要求/约束与非目标/验收/边界），判据见 [AGENTS.md §8.5](../AGENTS.md)。

**本代的特殊性**：这不是一份「新功能清单」，是**先把契约冻死、再改实现**的施工顺序；同时按 ADR-D29 **把契约从本仓拆成独立仓**（S0 可立即做，S1 排在 W0 后，S2/S3 逐级验收）。wave-0 五行**不改任何行为**（只写契约/常量/差异报告），目的是让后面 25 张单不必返工。**跳序施工 = 第二次 ABI 破坏 + 基准全量重跑**。

> **命名基线（ADR-D30）**：项目定名 **tellurion**，owner 保持 `ruoxizhnya`；**应用/服务用单段短名、库用全名**。本台账「② 上下文」里的 `quant-trading*` 是**改名前的现状描述**（不改）；「③ 要求」里的名字是**目标态**（QT-60 完成后生效）。仓名与 module path 的唯一真相源是 [`repo-inventory.md`](design/qt-decoupling/repo-inventory.md)。

---

## 执行顺序总览

| 波次 | 单 | 主题 | 级别 | 依赖 / 排序理由 |
|---|---|---|---|---|
| **S-1** | QT-60 ✅ | **改名到 tellurion**（12 仓已建 + module path + 运行时标识；旧仓 archive 延后） | P0 | **已完成（2026-10-20）**——tag 已可打在新仓 |
| **S0-前置** | QT-61 | **CI 基线归零**（4 项预存在失败） | P0 | **排在 QT-06 之前**：QT-06/QT-07 的验收写着「CI 绿」，基线不归零则验收不可达 |
| **S0** | QT-06 → **QT-18** → QT-08 | **分仓起步**：core 打 `v0.1.0` + **过渡期连体护栏** + 四仓自治三件套 | P0 / P1 | **QT-18 是 W0 的合入门禁**：未合入前 QT-01 不得进 main（第一个 breaking 就发生在 QT-01 的 abi v2）；QT-06/08 可与 W0 并行 |
| **W0** | QT-01 | abi v2 契约冻结（13 函数 + 内存区 + 枚举） | P0 | 一切 WASM 单的前置；artifact 出厂后改不动 |
| W0 | QT-02 | `FillTiming` + 两口径差异报告 | P0 | 决定全部历史基准怎么读；与 QT-01 无文件冲突，可并行 |
| W0 | QT-03 | 留痕 hash 体系（run_spec / dataset / artifact_hash） | P0 | 后面所有"可复现"验收都要引用它 |
| W0 | QT-04 | 重复信号存量扫描（定 trap / 合并） | P0 | 只读扫描，可与 01~03 并行；结论进 QT-21 |
| **S1** | QT-05 | **契约层 + 工具层落地**（代码直接写进 `tellurion-contracts` / `tellurion-kit`；domain 并入 contracts、kit 含 db） | P0 | **W0 全绿之后**（W0 定形状、S1 搬形状）；core 用 `require` 伪版本 + 本地 `go.work`，**全程不用 replace**（ADR-D31） |
| **S2/S3** | QT-17 / QT-07 / QT-19 / QT-09 | **收口**：agent 数据自持（`ai.*`）→ 两仓打 `v0.1.0` 并把伪版本换成 tag → 契约版本策略 → 跨仓护栏 + 契约测试；agent 去 replace | P1 | **W0 + QT-05 全绿之后**；QT-17/QT-19 分别是 S3 删 replace 的数据前置与版本前置 |
| **W1** | QT-10 | 错误分级 / fail-closed 落地 | P0 | 与 QT-14 同批（EndRun 语义的前提）；**W1 不依赖 S2**（只有 QT-17 可与 W1 并行，见并行指引） |
| W1 | QT-11 | per-run 策略实例 | P0 | K15 对拍前置；无文件冲突 |
| W1 | QT-12 | run 级缓存下沉 + 资源准入 | P0 | 同一笔内存账，必须一起做 |
| W1 | QT-13 | 复权锚点按 run 区间切片 | P0 | 依赖 QT-12（读的是同一个缓存） |
| W1 | QT-14 | Provider 生命周期（BeginRun/EndRun） | P0 | 依赖 QT-11（实例语义） |
| W1 | QT-16 | 检查点身份与版本（`shard_key` + `run_identity_hash`） | P0 | **先于 QT-15**：重放要按分片恢复状态，身份不定就分不清「这份字节属于哪个实例」 |
| W1 | QT-15 | 不可终止执行 + 重放语义 | P0 | 依赖 QT-14（会话）+ **QT-16（身份）**；两者不再「同批」，16 先落 DDL |
| **W2** | QT-20 | 参数投递两层 | P0 | 依赖 QT-03（run_spec_hash） |
| W2 | QT-21 | 信号排序 + 重复语义 | P0 | 依赖 QT-04 结论 |
| W2 | QT-22 | `version_hash` canonical + 向量测试 | P0 | 与 QT-03 同一套 canonical 工具 |
| W2 | QT-23 | `RunUniverse`（run 级稳定） | P0 | 必须在 QT-24 之前（索引来源） |
| W2 | QT-24 | host 数据模型 v2 落地（Bars/Axis/T）+ finalize 归属 | P0 | 依赖 QT-01/QT-23 |
| W2 | QT-25 | symbol scope 分片与预算 | P0 | 依赖 QT-16（shard_key）、QT-24 |
| W2 | QT-26 | WASM SignalProvider 接线 + EmittedSignal 转换 | P0 | 依赖 QT-01/QT-24 |
| **W3** | QT-30 | M0 市场级状态机 PoC | P0 | 依赖 W1 全部 + QT-24/26 |
| W3 | QT-31 | M0b symbol scope PoC | P1 | 依赖 QT-25/QT-30 |
| **W4** | QT-32 | AI 策略生成闭环（promote 闸门 + 全链留痕，承接 G6/M2） | P1 | 依赖 QT-41b（编译管线）+ QT-41 + QT-03；AI 生成侧在 agent 仓 |
| **X 线** | QT-40~45、QT-41b | 数据/策略/风控/分析/编排/网关六服务 + 编译服务 | P1-P2 | 各自独立，X0（三 hash）已在 QT-03；QT-41b ⛔ 阻塞于待裁决 D |
| **横切** | QT-50~52 | 装配外移 / 执行 port / 市场摩擦核实 | P1 | 与 W1 可并行，撞 `pkg/backtest` 的按序号串行 |
| **延续** | DBT-13~18 | 第 3 代存量 6 项 | P1-P3 | 前提已复核；DBT-16 阻塞于外部产出方 |

**并行指引**：**QT-06/08 与 W0 并行**（06 只打 tag，08 只写各仓入口文件）；**但 QT-18 必须先于 W0 合入 main**——QT-01 就是 abi v2 换 13 个函数，那是第一个会打穿 agent 编译的改动，护栏不在就漏过去了。W0 的 01/02/04 文件域互不重叠（`pkg/backtest/contracts` vs `pkg/backtest/engine*` vs 只读扫描）；**05 不在 W0 里跑**——它排在 W0 全绿之后的 S1（ADR-D31），搬的是 `pkg/domain` 与 `pkg/backtest/contracts`，**不是** core 顶层那个装 yaml/schema 的 `contracts/`。**07/09 必须等 05 与 W0 全绿**（拆的是已冻结的东西）；**17 可与 W1 并行**（只动 agent 仓与 `ai.*`，不碰 core 的引擎代码），它是 07 的 S3 段前置；**19 紧随 07**（两仓拆出来才有版本可管）；**18 必须最先做**（否则 W0/W1 的 breaking 改动会让 agent 仓静默烂掉）。W1 的 11/12/13/16 都动 `pkg/backtest`，按「10 → 11 → 12 → 13 → 14 → 16 → 15」串行、一单一 commit（**16 必须先于 15**）。W2 的 20/21/22 都动 `contracts` + 信号应用，串行。

---

## S-1 · 改名（✅ 2026-10-20 完成；旧仓 archive 延后）

> 依据 **ADR-D30** 与 [`repo-inventory.md`](design/qt-decoupling/repo-inventory.md)。**只改名，不改业务逻辑。**
> 用户口径：新建一套仓、把东西搬过去，不在旧仓大改；旧仓 archive 保留。

### QT-60 · 项目改名到 tellurion（12 仓已建 + module path + 运行时标识）[P0] ✅（2026-10-20，archive 段延后）

- **① 目标**：把项目迁到 `tellurion` 命名 —— **12 仓全部建好并推送**（4 个迁移仓 + 2 契约/工具仓 + 6 服务占位仓），module path 同步为「应用短名 / 库全名」，运行时标识（库/Redis/卷/ingress/镜像/Issuer/UA/DSN）全部改名。**旧仓 archive 不在本单验收内**（已延后，见 ⑧）。
- **② 上下文（执行前的原貌）**：`repo-inventory.md` §1/§2（D30）；实测待改 import：core **1153 处 / 526 文件**、agent **268 处 / 37 文件**；4 个目标仓名在账号下 404 空闲、8 个服务/契约仓名亦空闲；4 仓 `git tag` 全为 0、工作区干净 —— 唯一无损改名窗口（一旦打 tag、拆 contracts，成本翻倍）。
- **③ 实际执行方式（与原计划的差异，已发生，照此记录）**：
  ① 建仓 **12 个**（不是 4 个）：`tellurion` / `-agent` / `-ui` / `-docs` / `-contracts` / `-kit` / `-data` / `-strategy` / `-risk` / `-analytics` / `-orchestration` / `-broker`，全部 public；
  ② 迁移用 **`git archive HEAD | tar -x`**（只导被跟踪文件，自动排除 `.pyc`/`node_modules`/`.workbuddy` 等约 950MB 垃圾）——**用户裁决「不用太在意 git history」**，因此**不保留 blame**，每仓只有 1~2 个 commit。原计划写的 `git clone 保历史` 已作废；
  ③ module path：core=`tellurion`、agent=`tellurion-agent`，agent 走 `replace tellurion => ../tellurion`（**保留到 S3**）；契约/工具仓=`github.com/ruoxizhnya/tellurion-{contracts,kit}`；6 服务仓=单段短名；
  ④ 全仓 import 替换：`github.com/ruoxizhnya/quant-trading/` → `tellurion/`；`...-agent` → `tellurion-agent`（含 `_test.go`）；
  ⑤ **运行时标识全量改名**（用户新增要求）：库名 `quant_trading`→`tellurion`、测试库→`tellurion_test`、k8s namespace/configmap/ingress/host、镜像名、compose 变量、JWT Issuer、HTTP UA、默认 DSN、vscode workspace 文件名；
  ⑥ 文档引用替换：`quant-trading:` → `tellurion:`、仓名 `quant-trading-*` → `tellurion-*`（历史记录与现状描述不改）；
  ⑦ 验证：core/agent `go build`+`go vet` 绿、`check_deploy_consistency.py` 全绿、`check_doc_links.py` 零坏链；
  ⑧ push 全部 12 仓（遇 GitHub 二级限流，最后 2 仓由退避脚本补上）。**旧仓 archive 延后**：用户决定不急，且建议观察一周再归档。
- **④ 约束与非目标**：**不改任何业务逻辑**；不趁改名做重构；不删旧仓、不在旧仓打 tag；**不在本单迁 contracts/kit 的内容**（那是 QT-05）；不改 owner。
- **⑤ 验收（按实际口径改写）**：`grep -rn "github.com/ruoxizhnya/quant-trading" --include=*.go` = **0**（已达成）；`*.md` 中仅剩历史记录（已达成）；core/agent `go build`+`go vet` 全绿（已达成）；**12 仓在远端存在且 `main` 已推**（已达成，`gh repo list` 计数 = 12）；运行时标识 grep `quant_trading` 在非文档文件 = 0（已达成，仅 docker-compose 的旧卷注释按事实保留）。
  > ⚠️ **原验收里的「`git log` 条数与旧仓一致」已删除** —— 与「不保 history」的裁决直接矛盾，留着他会判一个已正确完成的任务为失败。
- **⑥ 边界**：只做改名与依赖路径；`contracts/` / `kit/` 的抽取与拆仓是 QT-05 / QT-07。
- **⑦ 收尾状态（2026-10-20）**：
  - 12 仓文件数：`tellurion` 970 / `-agent` 181 / `-ui` 207 / `-docs` 140；8 个占位仓各 5 文件（README/AGENTS/go.mod/doc.go/.gitattributes）；
  - 改名后暴露的**两个真回归已修并推送**：gofmt（121 文件 import 排序）、归档 5 条坏链；复跑 `gofmt` 与 `Doc link check` 均 success；
  - **未做且不属本单**：旧仓 archive（延后，建议观察一周）、agent/ui/docs 三仓的 AGENTS（→ QT-08）、8 个新仓的 CI（→ QT-08）；
  - 剩余 CI 红是**预存在**问题 → **QT-61**。

---

## 零、S0 · 分仓起步（可立即执行，与 W0 并行）

> 依据 ADR-D29 §7-S0。这两单**不改任何业务代码**，只让「独立仓」从目录独立变成**版本独立 + 有行为入口**。

### QT-06 · core 打 `v0.1.0` + agent 改 require tag（S0）[P0] 🔶

- **① 目标**：agent 对 core 的依赖从「源码路径」变成「版本号」——这是 S3 删 `replace` 的唯一前置（没有 tag，就没有版本可依赖）。
- **② 上下文**：`repo-topology-and-naming.md` §6 实测：core 仓 `git tag` 数量 = **0**；agent `go.mod:10` `require github.com/ruoxizhnya/quant-trading v0.0.0`、`:81` `replace ... => ../quant-trading`（**QT-60 改名后**：`require tellurion`、`replace tellurion => ../tellurion`，行号以改名后为准）；CI 用 `go mod edit -replace` 改写路径（`.github/workflows/ci.yml:32-34`）。agent 当前 import core 的 20 个包 / 37 个文件，**所以本单不删 replace**。
- **③ 要求**：⓪ **前置（硬）：QT-60 已完成**——tag 必须打在新仓 `tellurion`，打在即将 archive 的旧仓等于白打；① 在 core 当前 HEAD 打 annotated tag `v0.1.0`（`git tag -a v0.1.0 -m "..."`），**不改任何代码**；② agent `go.mod` 的 require 版本改为 `v0.1.0`；③ `replace` **保留**，行尾加注释「本地/CI 联调专用，S3（QT-07 收尾）删除」；④ agent CI 的 rewrite 步骤保留并同步注释；⑤ 在设计集 README 与 `docs/TASKS.md` 记录「`v0.1.0` = 分仓起点，不是发布版本」；⑥ **tag 要 push 到远端**才可能被其他仓 `go get`（本地 tag ≠ 可用版本）——S0 期靠本地 replace 不依赖它，但 S3 前必须 push（或改用私有 module proxy）。
- **④ 约束与非目标**：不打 `v1.0.0`、不发 release、不建 CHANGELOG 流程；不改 agent 代码；**不删 replace**（前置未满足：agent 仍 import core 实现包，见 QT-07 §S3）。
- **⑤ 验收**：`git rev-parse v0.1.0^{tag}` 存在且指向分仓起点 commit；**本次改动相关的 job 绿**（core 的 `gofmt` / `Doc link check`、agent 的 `Build` / `Vet`），其余红项按 QT-61 登记为 known-fail、**不作为本单失败**（原验收写「两仓 CI 全绿」，在 QT-61 之前不可达）；tag 已 push 到远端（`git ls-remote --tags origin` 可见）；agent `go mod tidy` 后 require 仍为 `v0.1.0`（未被 replace 抹回 `v0.0.0`）。
- **⑥ 边界**：只做版本化起点；单仓可构建属 QT-07 的 S3 段。

> **🔶 本地已就绪（2026-10-21）；待主任授权后 push 才算落地**：
> - **core tag**：`git tag -a v0.1.0` 打在分仓起点 HEAD（annotated，`v0.1.0^{tag}` 存在、解引用到当前 main 提交），message 明写「= 分仓起点，不是发布版本」。只打 tag，**未改任何 core 代码**。
> - **agent require**：`go.mod` 的 `tellurion v0.0.0` → `v0.1.0`；`replace tellurion => ../tellurion` **保留**并补注释「本地/CI 联调专用，S3（QT-07）删除」。本地验证 `go mod tidy` 后 require 仍为 `v0.1.0`（未被抹回 v0.0.0，§⑤ 判据），`go build ./...` 经 replace 绿、`go.sum` 无变化。
> - **文档**：`repo-topology-and-naming.md` §6 现状表 core tag 0→1、agent require v0.0.0→v0.1.0（AGENTS/CI 列的陈旧归 QT-08，不在本单混改）。
> - **未完成（阻塞在授权）**：**push tag 到远端**（`git ls-remote --tags origin` 可见）+ push 相关 commit 后「本次改动相关 job 绿」的 CI 观察 —— 二者是远端动作，需主任确认后再做。S0 期靠 replace 本地/CI 编译，不依赖远端 tag；真正要用它跑 S3 之前 push 即可。远端 CI 全绿还依赖 QT-61 合入后首跑。

### QT-18 · 过渡期 core↔agent 连体护栏（S0）[P0] ✅

- **① 目标**：S0~S3 期间 agent 编译期依赖 core 源码，而 W0/W1 的改动**全是 breaking**（abi v2 换 13 个函数、错误分级改 `getSignals` 返回、per-run 实例改 `Registry.Get` 语义、检查点主键变更）。要让这类破坏**在 core 的 PR 上立刻红**，而不是等两周后有人打开 agent 仓才发现它已经烂了。
- **② 上下文**：`repo-topology-and-naming.md` §7/§10（D29）；实测 core `.github/workflows/ci.yml` **零 agent 相关步骤**（grep 0 命中）；agent CI 反向 checkout core 并 `go mod edit -replace`（`.github/workflows/ci.yml:18-34`）——只有 agent 侧知道 break，core 侧无感知。
- **③ 要求**：① core CI 新增 `agent-compat` job：`actions/checkout` 拉 agent 仓（`repository: ruoxizhnya/tellurion-agent`）→ `go mod edit -replace tellurion=${{ github.workspace }}` → `go build ./...` + `go vet ./...`；
  ①-b **前置（外部）**：GitHub Actions 默认 token 只能访问当前仓，跨仓 checkout 必须配 `secrets.AGENT_REPO_TOKEN`（fine-grained PAT，只读 agent 仓）。**没有 PAT 时走降级方案**：提供 `make agent-compat`（本地/开发者机器跑同样的 checkout+build），并在 PR 模板里要求贴出该命令的输出——**降级只是过渡，不能长期替代 CI**；② **只 build/vet，不跑 agent 测试**（agent 测试需要 core 二进制/数据库，成本不成比例）；③ job 红时：core PR 必须附带 agent 侧适配 PR 链接，或在 PR 描述登记「known-break + 单号 + 期限」，二者必须有其一；④ 若 break 是有意的（如 abi v2），在 job 里允许 `known-break` 白名单（文件化，不用注释掉的代码）。
- **④ 约束与非目标**：不在 core CI 里跑 agent 的单测；不引入 monorepo 工具（Bazel/Nx）；不改 agent 代码；不替代 QT-09 的契约测试（本单只管**编译**兼容，不管**行为**兼容）。
- **⑤ 验收**：故意改一个 core 导出签名 → core PR 的 `agent-compat` 红（留证后撤回）；未改签名时绿；该 job 单次 ≤ 5 分钟（build-only）。
- **⑥ 边界**：只做编译兼容护栏；S3 后 agent 不再依赖 core 源码，本 job 转为「跨仓 tag 兼容检查」并保留。
- **⑦ 排序约束（硬）**：**本单合入 main 之前，QT-01 不得合入**。理由：QT-01 把 host 函数从 8 个换成 13 个（abi v2），是分仓后第一个打穿 agent 编译的改动；护栏不在，这次破坏就已经漏过去了。

> **✅ 完成记录（2026-10-21，本地验证；CI job 待 PAT 就位后接管）**：
> - **降级门禁 `make agent-compat`（已本地验证）**：把兄弟 `../tellurion-agent` 浅 clone 到临时目录（不脏开发者工作区）、`go mod edit -replace tellurion=$(CURDIR)`（指当前 core 工作区，含未提交改动 = PR 语义）、`go build ./... && go vet ./...`。正常态绿（build/vet exit 0）；**破坏验证**：把 `pkg/expression/registry.go` 的 `AvailableDataFields` 定义改名 ⇒ agent-compat `AGENT_BUILD_EXIT=1`，报错 `registry.go:722: undefined: AvailableDataFields` ⇒ 还原后 `git diff` 空、sha 一致。护栏有牙齿，非惰性。
> - **CI `agent-compat` job（.github/workflows/ci.yml）**：镜像 agent 侧反向 checkout 的做法（core→agent 方向），checkout `ruoxizhnya/tellurion-agent` → replace 指 `${{ github.workspace }}` → build/vet，**不跑 agent 单测**（§②）。§④ 的 known-break 走**文件化**白名单 `.github/agent-compat-known-break`：非空时把红降级成 warning 并打印内容（有意 breaking 如 QT-01 用），文件不存在即严格红 —— 不是 `continue-on-error`（文件进仓、reviewer 看得见，是人在环签核）。
> - **PAT 依赖（§①-b，需主任操作）**：跨仓 checkout 需只读 fine-grained PAT `secrets.AGENT_REPO_TOKEN`（默认 GITHUB_TOKEN 看不到别的仓）。**未配时 job 不硬失败**：`Detect cross-repo PAT` 步发 notice「降级模式」并让 `make agent-compat` + PR 贴输出兜住合入门禁；PAT 就位后 job 自动接管、降级下线。`.github/pull_request_template.md` 已加「agent 编译兼容」节要求三选一并贴 `make agent-compat` 输出。
> - **未做**：PAT 由主任在 GitHub 配置后，本 job 需跑一次**真破坏腿**留证（改一个 core 导出签名 → 观察 agent-compat 红 → 撤回）作为 CI 侧的最终验收；本地已用同一条命令序列证明机制成立。合入门禁：QT-01 之前本单须已在 main。
>
> YAML 校验通过（5 job）；doc-links 0 断链。

### QT-08 · 十二仓自治三件套（AGENTS / CI / TASKS）+ 自检脚本 [P1] 🔶

- **① 目标**：任何一个仓被一个 agent 接手时，都能**从本仓内**知道「怎么构建、边界在哪、依赖哪版契约、该做什么、禁止什么」，不靠读别的仓。

> **进度（2026-10-20，已推送）**：10 个仓（agent / ui / contracts / kit / data / strategy / risk / analytics / orchestration / broker）已按主仓 `docs/` 模式建好文档机制并提交：每仓 `AGENTS.md`（构建 / 边界 / 禁止 / 依赖版本 / 台账入口）+ `docs/README.md`（导航）+ `docs/TASKS.md`（本仓唯一台账，六字段）+ `docs/ARCHITECTURE.md` + `docs/adr/`（只增不删 + 模板）+ `docs/archive/`（归档规则）；8 个 Go 仓补 `.gitignore`（含 `go.work`）+ `.github/workflows/ci.yml`（gofmt/build/vet/test，**本机实跑四条全绿**）+ `LICENSE`（MIT）；`agent` / `ui` 的 `AGENTS.md` 与 `.gitattributes` 属**首次建立**（此前完全缺失）。
> 顺带修掉：`agent` 37 个文件的 gofmt（改名副作用，此前遗漏）+ `agent` / `ui` 共 15 条存量坏链（拆仓后文档失配）→ 现在 10 仓 markdown **相对链接 0 断链**。
> **第二批（2026-10-20 傍晚，已推送）**：10 仓补齐**功能边界契约** —— 每仓 `docs/db-schema.md`（表归属与 DDL 唯一 owner，无表也要写清为什么）+ `docs/api-schema.md`（接口面 / 错误码 / 幂等 / 兼容）+ `docs/dataflow.md`（流向 / 时序 / fail-closed 点 / 不变量）+ `docs/ARCHITECTURE.md` 改写为高层设计 + 每仓首条 ADR（场景/决策/理由/代价/来源）+ `docs/odr/`（运行治理决策机制，与 ADR 分工）+ `docs/README.md` 的「变更触发」表 + `AGENTS.md` 的「文档真相源」节 + `TASKS.md` 的「施工序」。
> 同时把这条机制登记为 **R6「功能边界有契约」**（`repo-topology-and-naming.md` §2）：R1~R5 只保证仓能跑，R6 保证仓知道自己该做什么。
> **第三批（2026-10-20 夜，已推送）**：**审查十仓文档并修掉三处「写得像事实、其实不成立」**——
> ① 十仓 `db-schema.md` 的 §4 曾把「`migrations/` + 序号递增 + 只前滚」写成现状，但 core **没有迁移执行器**（`grep 'func Migrate'` 零命中；建表靠 `pkg/storage` 内联 DDL，另有 `migrations/` 14 个与 `docs/migrations/` 25 个手工 SQL）→ 改为「现状（这是债）+ 本仓约定」；4 个无表仓整体改成「数据治理」。
> ② 五个未实现服务仓的 `api-schema.md` 读起来像现状 → 加「全部未实现」横幅。
> ③ `contracts` 的 port 表把已存在（`EngineRunner`/`SignalProvider`）与设计中（`DataPort`/`BrokerGateway`/`ExecutionPort`，源码 0 命中）混在一起 → 加状态列；并澄清 `RunSpec`/`SignalSpec`/`ResultSpec` 是**设计术语**，今天由 `BacktestRequest`/`BacktestResponse` 承载。
> ④ `data` 仓归属表曾按类型名臆造表名（`market.ohlcv`/`market.symbols`/`ingest.data_sync_state` 等**一个都不存在**）→ 用全量内联 DDL 实测重写。
> **`tellurion-docs` 同时改造为「总体设计知识库」**（此前两轮被排除）：新增 `SYSTEM-DESIGN` / `REPO-MAP` / `SERVICE-INDEX` 三份总体层索引、`AGENTS.md`、链接检查脚本与 CI；修 153 条拆仓后坏链（含 41 条 `file:///Users/.../quant-trading/*` 本机路径）；`ARCHITECTURE.md` 降级为历史 Reference。活跃层 198 条链接 0 断。
> **第四批（2026-10-20 深夜，已推送）**：**逐仓审查「职责与边界是否写清」**，修掉 8 类问题（证据逐条 grep 过）：
> ① **core 的 AGENTS 三节失真**（932 行，停在 2026-09-21）：§3 目录结构列着 `pkg/ai/**`（15 子包）/ `pkg/validation` / `pkg/tools` / `web/src/`——全部已迁 agent 或 ui；§4 工作范围把「前端」算成本仓的活；§1/§6/§9 有 58 行前端规范。→ 目录换成实测（38 个 pkg 全覆盖）+「已迁出」表；§6 前端整节换指针；§9 UI 条目打 `[ui]`；新增 **§0 分仓拓扑：我在哪**（12 仓表 + 三条硬规则）。
> ② **迁移机制我在上一版写错**：说「core 没有迁移执行器」，实际是 `migrationStatements()` + `migrate()` 启动按序执行（幂等、无版本表、无 down、无记录），`migrations/*.sql` 是**不执行的**历史镜像；我用的 `grep 'func Migrate'` 漏了方法接收者的小写签名。六仓 db-schema 已按真实机制重写。
> ③ **订单/成交/持仓的归属三方矛盾**：broker 写「归运行域（`run` 或 `exec`，待裁）」、orchestration 拥有 `run`——字面合起来 = 让「只派活不裁决」的服务拥有交易真相。→ 统一为 **core `quant`**；`run` 明确是**任务域**（含 `strategy_state` 检查点），不含订单。
> ④ **analytics 自相矛盾**：四处写「不写业务表/只读是硬边界」，但它拥有 `analysis.*` 五张结果表 → 统一为「只读**上游**，只写自己的结果表」。
> ⑤ **两个 `audit` 没说清**：`audit` schema（事件存储，core 的 eventstore/msgbus，用于重放）vs `risk.audit_logs`（人操作审计）→ 两边各加一段判据。
> ⑥ **6 个服务仓不说「今天的实现在 core 哪里」** → 每仓 AGENTS §1 加「今天的实现」一行（如 risk = `pkg/risk` + `pkg/compliance` + `pkg/alert`）。
> ⑦ **未裁事项散落**：`cmd/dualrun` 只写在 orchestration 单仓文档里 → core `repo-inventory.md` 新增 **§3.1 未裁边界 U1~U5**，单仓只留指针。其中 **U3（`strategy_state` 迁出后 core 缺 `RunStateStore` port）** 已补进 QT-44 的前置——不补这条，X5 一动 core 就编不过。
> ⑧ 顺带：ui「不做权限模型」太含糊 → 改成「不做权限**判定**，只做展示级隐藏（隐藏不等于保护）」；orchestration 的「持有回测结果真相」→「持有结果**内容**」（它本来就该存指针 + 三 hash）。
> 验证：十仓 292 条相对链接 0 断；core 的 `tools/check_doc_links.py` 通过；docs 仓活跃层 0 断。
> **第五批（2026-10-20，已推送）**：**U1 裁决落地——`cmd/dualrun` 留 core**（用户拍板）。已从「未裁」挪进 `repo-inventory.md` §3「已裁决的边界」，并同步 core AGENTS §3、`repo-topology-and-naming.md` §3.2/§3.3、QT-44 非目标、orchestration 三处（AGENTS/README/db-schema）、docs 仓 REPO-MAP。
> **顺带纠正我先前给的一条理由**：我写「它读订单/成交（运行域真相）」——**不准确**。`cmd/dualrun` 读的是**回测内**的成交明细（`pkg/backtest/tracker` 在进程内产出），不碰实盘 `quant.orders`/`fills`。真正理由是：要**同进程**链接 `pkg/backtest` + `pkg/backtest/nautilus` 两套内核（跨进程拿不到同一次运行的逐笔成交），且它是**引擎的验收门**（CI `align-gate`），不是生产服务。
> 未裁边界由 5 条降为 4 条（U1 留空号，不重编号——避免各仓文档里的 `U3` 指针失效）。
> **第六批（2026-10-21，本单 ① 自检脚本）**：`tools/check_repo_autonomy.py` 已交付。静态核 R1（仓外 `=>` / committed go.work 即红，唯一容忍 = agent `=> ../tellurion` 作**已登记缺口→QT-07**）、R2（按类型分派；8 个 S0 占位仓「有 CI 无测试」**显式列出、不静默通过**）、R3（AGENTS 依赖章节 + 禁止标记；core 缺字面标题作**已登记缺口→QT-08**）、R5（contracts/kit 跨仓版本声明一致性，core 汇总）、R6（三份边界契约非空；core / docs **显式豁免**并写理由）。已知缺口登记表是单一来源，脚本会去台账核实工单号在册——「登记才放行，没登记就红」。本地对**12 仓全量**跑 GREEN（2 缺口登记 + 2 豁免，均显式）；**破坏验证**：给 kit 塞未登记 `go.work` → RED + exit 1，删除 → 复绿；`--run-builds` 逐仓实跑 build/vet 的接线已单仓 smoke。已接入 core CI 的 docs job 作 R5 汇总入口（core-only checkout 时核 core 自身 + 点名兄弟仓缺席）。修掉一处**假护栏**：R1 正则原先只匹配单行 `^replace`，块形式会漏检 → 改为取任何非注释行的 `=>` 目标。
> **剩余**：③ 的 `docs` 仓 CI/TASKS（用户本轮明确排除 `docs`）；④ core `AGENTS.md` 的「分仓规则」节；⑤ 各仓 CI 首次绿的线上确认（8 个新仓的 workflow 是本次新增，首跑结果待看）。
- **② 上下文**：ADR-D29 §2 五条不变量（R1~R5）。**2026-10-20 复核现状（12 仓）**：core 有 AGENTS + CI；`agent`/`ui`/`docs` 三仓**均无 `AGENTS.md`**，其中 `docs` 还**无 CI、无 `docs/TASKS.md`**；**新增的 8 仓（contracts/kit/data/strategy/risk/analytics/orchestration/broker）有 AGENTS 但全部无 CI**；全部 12 仓 tag 均为 0（core 由 QT-06 解决；contracts/kit 由 **QT-07（S2）** 打 tag——S1 只落代码，见 ADR-D31）。
- **③ 要求**：① 写 `tools/check_repo_autonomy.py`：逐仓检查 R1~**R6**（R6 = 三份功能边界契约存在且非空），**未通过的项必须在 `docs/TASKS.md`（或该仓 TASKS）有对应单号**，否则红；② `agent` / `ui` / `docs` 各补 `AGENTS.md`（构建/测试命令、模块边界、**依赖的契约版本**、禁止事项、`docs/TASKS.md` 指针）；③ `docs` 仓补 CI（markdown 链接检查 + frontmatter 齐全）与 `docs/TASKS.md`（只放本仓事项，跨仓只留指针）；**8 个新仓各补最小 CI**（`go build ./...` + `go vet ./...` + `go test ./...`），否则 R2「单仓可自证」对它们恒为红；④ core `AGENTS.md` 增「分仓规则」一节（D29 §9 的 C1~C5）；⑤ 脚本**按仓类型分派**：11 个 go 仓查 `go build/vet/test`；`ui` 查 node 构建；`docs` 查链接与 frontmatter——不能用一把 `go build` 量十二个仓；**R1 判据是「`replace` 指向仓外路径 = 红」**；ADR-D31 之后 S1 不再有子 module，所以 core 与两新仓**任何 `=>` 都该红**，唯一白名单是 agent 的 `=> ../tellurion`（S3 删）；⑥ `docs` 仓的跨仓引用改 `repo:path` 纯文本（如 `tellurion:docs/...`；改名前后一律以新名书写，QT-60 负责批量替换），**不做成可点击相对链接**——独立 checkout 时 `../../` 必断，会让 R2 永远红；⑦ 脚本住 core 的 `tools/`，各仓 CI 只自证本仓（R1~R4）；**R5 的跨仓版本比对由 core 汇总跑**。
- **④ 约束与非目标**：不把 core 的 AGENTS 全文复制到各仓（只写本仓必须知道的）；不引入新 CI 平台；不改任何代码。
- **⑤ 验收**：脚本对 **12 仓**输出绿（agent 的 R1 以 `known-gap → QT-07` 登记方式通过，不是静默忽略；8 个占位仓的 R2 以「有 CI 但无测试」也要显式列出，不许静默通过）；`docs` 仓与 8 个新仓的 CI 首次绿；**12 仓** `AGENTS.md` 均含「依赖的契约版本」与「禁止事项」两节。
- **⑥ 边界**：只做入口与自检；拆仓本身是 QT-07。

---

## 一、W0 · 契约冻结（写契约/常量/报告；**默认行为不变**）

### QT-01 · abi v2 契约冻结（13 个 host 函数 + 内存区所有权 + 枚举）[P0] ⬜

- **① 目标**：一份**唯一真相**的 abi v2 契约落到代码里（常量 + 校验 + schema），v1 artifact 仍可跑；后续所有 WASM 单都引用它，不再各自解释文档。
- **② 上下文**：`docs/design/qt-decoupling/adr-029-amendment-host-api-v2.md` §2.1（13 函数表，2026-10-20 补 `emit_target2`）、§7.1~7.8、§8；`p0-remediation-design.md` A5/A6/B1/B2/B3；`p0-round2-review.md` §8.2（emit_target2）、N5（host 区所有权）。现状：`host_api.go:40-47` 只有 8 函数；`BarContext.Series []float64` 只有 close；`hostGetBar` 的 `_ = fieldID`（`:164`）；`OnBarSession.Initialize` 直接 `WriteMemory(0, params)`（`on_bar_session.go:67`）；`hostGetState` 固定 `Write(4096, val)`（`host_api.go:231`）。
- **③ 要求**：① 把 §2.1 表的签名/语义/错误码写成 `contracts` 下的常量与类型（`FieldID`、`Action`、`MaxParamsBytes=4096`、`MaxStateValueBytes=64KiB`、`MaxInstanceStateBytes=4MiB`、`MaxKeys=4096`、`MaxScratchPerBar=256KiB`、`MaxSignalsPerBar=2×symbolCount`）；② 增量命名 `get_bar2`/`get_series_len2`/`get_date2`/`emit_target2`，**不改已有签名**；③ `ValidateImports` 升级为「名字白名单 + 版本族一致性」+ 与 `abi_version` 交叉校验，错误必须在 instantiate 之前可读；④ host 区导出符号 `__host_params_ptr` / `__host_scratch_ptr` / `__host_scratch_size` 的校验骨架（缺失/重叠/越界/未对齐 → 专用哨兵错误），**但只对 `abi_version=2` 强制**：`abi_version=1` 走白名单跳过符号校验——现有 14 个内置 artifact 都没有这些符号，一律 fail-closed 会把 v1 全打死，与本单 ⑤「v1 仍能跑」直接矛盾；⑤ `emit_signal` 与 `emit_target2` 同 bar 同 symbol 混用 → trap 的判定入口；⑥ 契约文件与文档逐项对表（一个测试读常量核 13 项）。
- **④ 约束与非目标**：**不实现** host 函数体、不改 `BarContext` 结构（那是 QT-24）、不动引擎与检查点、不改 v1 行为、不为"兼容"给 v1 补新符号。
- **⑤ 验收**：契约测试逐项对表 13 个函数与 6 类上限；4 个 bad artifact（缺 `__host_params_ptr` / ptr 落在模块数据段 / v1+v2 混导 / `abi_version` 与 import 族不符）全部被拒且错误可检索；v1 artifact 仍能实例化并跑通现有测试。
- **⑥ 边界**：只做"契约 + 校验骨架"；数据填充、host 函数实现、SDK 模板分属 QT-24/QT-26。**本契约冻结仅覆盖 Go 内核 + WASM 载体；NT（NautilusTrader）侧 on_bar host API 对齐待裁决 C（设计集 README 待裁决表）裁决后另立单**——C 判「做」则按 abi v2 签名立 NT 对齐单，判「不做」则须成文声明「一份 wasm 跑两核」的对齐边界与 D25 abi v2 的适用范围；两种结局均不回改本单已冻结的 Go 侧契约。
- **⑦ 前置（硬）**：**QT-18 已合入 main**。本单是 abi v2 的第一次 breaking，core 与 agent 在 S0~S3 期间编译连体，没有 `agent-compat` job 就会"core 绿、agent 静默烂"。

### QT-02 · `FillTiming` 冻结 + 两口径差异报告 [P0] ⬜

- **① 目标**：`SignalSpec.FillTiming` 成为必填契约；默认从 `same_close` 切到 `next_open` 有**可解释的差异报告**作依据。
- **② 上下文**：`p0-round2-review.md` §8.1（冻结文本）；现状 `engine_daily.go:181-205`（当日切片含当日 close）、`:263-270`（`res.price=末根 close`）、`:571`（`price := pricesCache[symbol]`）⇒ T 日收盘决策 + T 日收盘成交。全设计集此前无一处定义成交时点。
- **③ 要求**：① `contracts` 加 `FillTiming` 枚举 + `ErrFillTimingMissing`；② 引擎按 `FillTiming` 取成交价：`next_open` 用 T+1 开盘（T+1 停牌/涨跌停按 REV-09 顺延或拒单），`same_close` 保持现状；③ `ResultSpec` 落 `fill_timing` 与 `known_bias: same_close_fill`；④ 选 3~5 个基准策略跑两口径，产出差异报告（笔数/总收益/最大回撤/换手，并解释差异 ≈ 日收益平移一个 bar）；⑤ K15 前置断言：两内核 `FillTiming` 不一致 → `ErrFillTimingMismatch`（口径不符，不是结果不一致）；⑥ **schema 变更评审项**（按「维护本文件」节硬规则）：`ResultSpec` 新增 `fill_timing`/`known_bias` 字段触碰冻结面——先触发 `docs/SPEC.md` 合规测试红，同步更新 SPEC 的 ResultSpec 字段节（节号以现行 SPEC 为准，落单时补具体节号），走正式变更评审记录后再落代码。
- **④ 约束与非目标**：**先出报告再切默认**——本单要**实现 `next_open` 的取价分支**（否则出不了报告），但 `FillTiming` 的默认值在报告归档前不动，即「现有 run 的行为零变化」；不改滑点/费率/撮合内部（部分成交、涨跌停判定）；不允许为"保持历史基准不变"而选口径。
- **⑤ 验收**：缺字段 → run 拒绝且错误可读；报告归档在 `docs/test-cases/` 或设计集内并被引用；切换后基准结果与报告数值一致（同环境重跑 ±0）。
- **⑥ 边界**：只做"时点选择"；撮合真实性属 QT-52。

### QT-03 · 可复现留痕体系（`run_spec_hash` / `dataset_snapshot_hash` / `artifact_hash`）[P0] ⬜

- **① 目标**：任何一个回测结果都能回答"用什么输入跑出来的"，且**改任一影响结果的输入 ⇒ hash 必变**；三份 hash 的覆盖面与层级不再互相矛盾。
- **② 上下文**：`p0-round2-review.md` N6 + N7；事实：`run_spec_hash` 全设计集仅 2 处引用、零定义；三 hash 不含费率/滑点/成本模型/T+1/初始资金/`FillTiming`/锚点 digest/内核版本/toolchain/seed；`dataset_snapshot_hash` 两处定义冲突（`data-service-contract-and-ownership.md` §2.1 五项 vs `adr-029-amendment-host-api-v2.md` §7.1 要求含 `Axis`）；`version_hash` 不含 toolchain 而 `@hash` 却指它（`strategy-asset-service-api.md` §1.2/§3.3）。
- **③ 要求**：① 按 N6 的 canonical 集合定义 `run_spec_hash`（含 `fill_timing`、`universe_hash`、`adjustment_anchor_digests`、`kernel`/`engine_version`、`seed`）；② `dataset_snapshot_hash` **只保留一处定义**（含 `calendar_hash` 与 `adjustment_basis`），其余文档改为引用；③ `artifact_hash` 定为执行真相，`strategy_ref` 三形态语义写死（`id@artifact_hash` / `id@version_hash` / `id`），结果同时记 `{输入引用, version_hash, artifact_hash}`；④ `run_artifacts` 落 `run_spec_hash` + 分量 + `params_json` + `status`（DDL 加列进 `postgres.go` inline DDL，不建 migration 文件）；⑤ 向量测试：改任一分量 → hash 变；不改 → 不变；⑥ **schema 变更评审项**（按「维护本文件」节硬规则）：若 `ResultSpec`/`RunSpec` 需暴露三 hash 字段（provenance 返回面），触碰冻结面——先触发 `docs/SPEC.md` 合规测试红，同步更新 SPEC 对应节号（落单时补），走正式变更评审；纯 `run_artifacts` 表加列不属冻结面，但 `status` 列取值必须遵守三值口径。
- **④ 约束与非目标**：不改 `version_hash` 的 canonical 集合（那是 QT-22）；不建新表（`run_artifacts` 已有落点，X0 载体）；不实现数据服务 aggregator（QT-40）。
- **⑤ 验收**：向量测试覆盖 8 个"必变"分量与 3 个"不必变"场景；`GET /api/runs/{id}/provenance` 能返回三 hash + 引擎名 + seed（可先只落库，端点随 QT-44）；两处 `dataset_snapshot_hash` 定义在文档中只剩一处正文、另一处为引用。
- **⑥ 边界**：只做"定义 + 落库 + 校验"；数据快照的实际计算在 QT-40。

### QT-04 · 重复信号存量扫描（决定 trap / 合并）[P0] ⬜

- **① 目标**：拿到「同 bar 同 symbol 多条信号」在存量策略里的真实重复率，据此把 A12 从"二选一待定"变成"已成文规则"。
- **② 上下文**：`p0-round2-review.md` §8.3；现状两条建仓路径都是 `map[symbol]` 覆盖（`pkg/risk/manager.go:185-220`、`engine.go:1552-1560`），"最后一条生效"取决于未排序顺序（A8）。风险：直接上 trap 可能让 14 个内置策略整体失败（判据 B）。
- **③ 要求**：① 写一个只读扫描器（可直接用 `SignalObserver` 钩子 + 现有回测），对 14 个内置策略 × 3 个基准区间统计 `(date,symbol)` 重复度；② 输出报告：重复率、样例（策略/日期/两条 action）、是否含相反 action；③ 按 §8.3 给出结论（零重复 → trap；有重复 → 先 `close` 优先合并 + 同向 strength 求和 clamp + `duplicate_signals_merged` 计数）；④ 结论写进 `SignalSpec` 草案，供 QT-21 实现。
- **④ 约束与非目标**：**只读**，不改任何策略与引擎行为；不顺手调整策略信号逻辑；不把扫描器做成生产代码（可留在 `testdata/` 或一次性命令）。
- **⑤ 验收**：报告存在且含可复现命令；每个重复样例都能定位到源码行；结论落到 `p0-round2-review.md` §8.3 的表格里（勾选分支）。
- **⑥ 边界**：只决定"重复怎么办"；排序本身是 QT-21。

### QT-05 · S1 契约层与工具层落地（代码直接写进两个新仓）[P0] ⬜

> **形态已裁决（2026-10-20，用户选 Plan B → 落地形态见 ADR-D31）**：两仓已存在（各含最终 module path 的 `go.mod` + `AGENTS.md` + `doc.go` 占位），所以**代码直接写进新仓**，core/agent 用 `require <伪版本>` 取用，**全程不出现 `replace`**。原 Plan A（先在 core 内建 `contracts/` `kit/` 子 module，S2 再搬）作废——它会搬两次，且与 core 顶层 `contracts/` 数据目录撞名。

- **① 目标**：契约（含值对象）与横切工具**物理上住进两个独立仓**，依赖方向由编译器 + `go.sum` 强制；core 与 agent 通过**版本依赖**引用它们。
- **② 上下文**：`repo-topology-and-naming.md` §5/§7-S1（D29）+ ADR-D31；实测：`pkg/domain` 17 文件 3983 行，被 **core 306 文件、agent 48 文件**使用，自身仅依赖 `domain/market` 与 `fees`（2 处）；`pkg/backtest/contracts` 6 文件 2113 行，仅依赖 `domain` + `fees`，被 50 处（含 agent 5 处）使用；ADR-031 四契约（`RunSpec`/`SignalSpec`/`ResultSpec`/`EngineRunner`）就在这个包里。两仓现状：`github.com/ruoxizhnya/tellurion-{contracts,kit}`、`go 1.25`、**public**（`GET /repos/...` = 200）；`tellurion-contracts@main` 实测可 `go get` 到 `v0.0.0-20261020112435-a55208cc2412`（Go 1.25.0）。
- **③ 要求**：
  ① `tellurion-contracts`：`domain/`（17 文件，`package domain`）、`engine/`（6 文件，**保持 `package contracts`**）、`fees/`（**只放数据形状**：费率/滑点/成本模型的定义与枚举）；
  ② `tellurion-kit`：迁 12 个包 + 新增 **`db`**（仅 connection/migrate/DSN 解析，**零表结构**，K 裁决落地）；
  ③ core 侧：`go get github.com/ruoxizhnya/tellurion-contracts@<sha>`、`@...-kit@<sha>` 写入 `require` 与 `go.sum`；再一次性 sed 全替 `tellurion/pkg/domain` → `github.com/ruoxizhnya/tellurion-contracts/domain`（306 文件）与 `tellurion/pkg/backtest/contracts` → `.../contracts/engine`（50 处）；
  ④ **禁止 `replace`**：core / agent 的 `go.mod` 里不得出现 `=>`。本地要同时改三个仓时用 **`go.work`**（仓根，`use ( . ../tellurion-contracts ../tellurion-kit )`），**加入 `.gitignore`、不入库**——实测：有 `go.work` 时直接读本地目录、无 `go.sum` 也能构建；没有时按 `go.sum` 走真实版本（**这正是 CI 与 Docker 的路径**）；
  ⑤ **agent 侧同步**：agent 仓根 `go.work` 写 `use ( . ../tellurion ../tellurion-contracts ../tellurion-kit )`（它仍 replace core，到 S3 才删）；**`require` 同样不传递**，agent 必须自己 `require` 两个仓——漏一条它立刻编不过；
  ⑥ **core 顶层 `contracts/` 的 5 个数据文件留在原地**：`field_dictionary.yaml` + 4 个 `*.schema.{json,sql}` 是**运行时按 cwd 相对路径自动发现**的部署资产（`cmd/data/setup.go:223-225` 依次试 `./contracts/`、`../contracts/`、`../../contracts/`），不是 Go 包。跟 module 搬走 = 部署后字典**静默加载失败**（编译期查不出）。本单**只搬 Go 代码**；
  ⑦ 新增 `tools/check_contract_pin.py` + `make contracts-pin-check`：core / agent 的 contracts/kit pin **必须是该仓 `main` 的祖先**（禁止指向未合入的提交），并打印 `repo@sha → 提交日期` 供评审；
  ⑧ 包名策略：`contracts/engine/` 内保持 `package contracts`、`contracts/domain/` 内 `package domain`；调用点前缀一行不改，只在 import 处处理 alias。若改成 `package engine`，本仓 50 处 + agent 5 处都要改前缀；
  ⑨ 输出「**core-only 类型清单**」：`screen`/`sector`/`supply_chain`/`macro`/`mining` 在 agent 侧各 0 命中，属 core 专用；S1 仍整包迁，清单留给 S2 后回收（D29 §5.2）。
- **④ 约束与非目标**：**不改任何业务逻辑**；不搬 `sandbox/wasm`（必须与引擎同进程）；不搬 `fees` 的计算部分；**不动那 5 个数据契约文件**；不在本单打 tag（S2 = QT-07）；**不为省事给 core/agent 加 replace**；一次只搬一个包，每搬一次跑 `go build ./...` + `go vet ./...`。
- **⑤ 验收**：`go list -m all` 显示 3 个 module，且 contracts/kit 两行**带真实伪版本**（不是 `=> ./...`）；`go build ./...` + `go test ./...` 绿；旧路径 grep 零命中；`git grep -E '=> *\.\./tellurion-(contracts|kit)'` 在 core / agent 均零命中；在两仓内 import `tellurion/...` **编译失败**；`kit` 业务词零命中；agent 与 core 均能 build；`make contracts-pin-check` 绿；**删掉 `go.work` 后 `go build ./...` 仍绿**（证明走的是 `go.sum` 而非本地目录）；`equitydeep` 字典在 `cmd/data` 与部署镜像里都能加载（回归）。
- **⑥ 边界**：只做代码搬迁与依赖切换；tag、版本策略、跨仓护栏属 QT-07 / QT-09 / QT-19。

---

## 二、W1 · 执行正确性（修"今天就在错"的）

### QT-10 · 策略执行错误分级（fail-closed 落地）[P0] ⬜

- **① 目标**：策略故障与契约违规**终止 run**，只有数据缺口可以跳过当天且必须留痕；"漂亮曲线但策略一直在 trap"不再可能。
- **② 上下文**：`p0-round2-review.md` N1；`engine.go:918-926` 对 `getSignals` 的一切错误只 `Warn` + `skipping day`，注释明写"不进 invalid_reasons"；而该错误面同时含 `RejectFutureSignals` 拒绝与 wasm trap。
- **③ 要求**：① `contracts` 定义 `ErrDataGap` / `ErrStrategyFault` / `ErrContractBreach` 三类哨兵；② provider 侧按类包装（数据缺失→DataGap；trap/非法信号/预算超限/artifact 校验失败→StrategyFault；前视/未知参数/abi 不符→ContractBreach）；③ `RunBacktest` 在 `ErrDataGap` 时跳过当天并写 `invalid_reasons`，其余两类立刻 `EndRun` + `status=failed`，不返回可用于决策的结果；④ **`ResultSpec` 状态沿用冻结三值 `completed / invalid / failed`（`docs/SPEC.md` 状态语义节），不引入第四值**：错误分级经映射进三值空间——数据缺口跳天 → `invalid` + 稳定 `invalid_reasons` code `data_gap_skipped`；`ErrStrategyFault`/`ErrContractBreach` → `failed` + `error`；`Failures[]` 只作**诊断明细**（记录日期/错误类/原因），**不是状态来源**，status 仍由 `buildResponseFromState` 单一透传点产出；⑤ 护栏：`pkg/backtest` 内 `getSignals` 的 err 必须被 return 或映射为 fatal，禁止只 log。
- **④ 约束与非目标**：不改变数据缺口的既有语义（仍跳过当天）；不新增 run 状态存储（用现有 `StateStore`）；不动信号内容；**不改冻结三值 `status` 语义本身**（`docs/SPEC.md` 状态节属冻结面，本单只落映射规则与 `invalid_reasons` 新 code——按「维护本文件」节的冻结 schema 变更评审规则走）。
- **⑤ 验收**：构造"每 50 天 trap 一次"的 wasm → run `failed` 且 `Failures`（诊断明细）点名日期/原因；构造缺一天数据的 provider → `invalid` 且 `invalid_reasons` 含 `data_gap_skipped`；护栏测试：把错误改回"只 log"→ 红。
- **⑥ 边界**：只做错误分级；`EndRun` 的创建/销毁是 QT-14。

### QT-11 · 策略 per-run 实例（注册工厂 + `NewInstance`）[P0] ⬜

- **① 目标**：同一策略并发/连续跑互不串状态（`SignalStrategy.cursor` 不再跨 run 存活）。
- **② 上下文**：`p0-remediation-design.md` A2 / BE-21；`registry.go:51-61` 返回注册时同一实例；14 个内置插件 `init()` 里 `Register(&x{})`。
- **③ 要求**：① `RegisterFactory` / `NewInstance`；`Register(Strategy)` 保留但标 `shared=true`，`NewInstance` 遇 shared 直接报错；② 14 个插件改注册构造函数；③ `LocalSignalProvider` 与 WASM provider 都走 `NewInstance`；④ repoguard：`pkg/backtest` 与 provider 实现内 `.Get(` 即红。
- **④ 约束与非目标**：不引入 `Reset()` 作为替代（弱保证）；不改策略业务逻辑；不动 `Registry.Get` 的元数据查询用途（Parameters/Info）。
- **⑤ 验收**：同一策略顺序跑两次逐位一致；并发两笔不串；执行路径 grep `.Get(` 零命中；14 个插件全部 `NewInstance` 成功。
- **⑥ 边界**：只做实例生命周期；状态落库是 QT-16。

### QT-12 · run 级缓存下沉 + 资源准入（A7/A10/N8 一笔账）[P0] ⬜

- **① 目标**：引擎级可变缓存不再被并发 run 互相覆盖；并发 run 有内存总闸，不会"跑几个就 OOM"。
- **② 上下文**：`p0-remediation-design.md` A7/A10；`p0-round2-review.md` N8。证据：`engine.go:783` `warmFactorCache`、`:790→:1245` `e.fundamentals = records`、`:775` `warmCache`；`cache.go:127-140` `mergeBars` 合并且无清理调用点；A10 改 per-run 后内存从"一份账"变"N 份账"，而 `Engine` 无并发上限（`:155` 允许并发）。
- **③ 要求**：① `fundamentals` / factor cache / `listing` 移入 `runContext`（`runBacktestInternal` 内创建），`Engine` 只留不可变配置；② L1 OHLCV 缓存同刀下沉 per-run（或保留进程级但必须字节预算 + LRU）；③ `CacheManager.Bytes()` 可计量；④ run 准入闸：`maxConcurrentRuns`（默认 `min(GOMAXPROCS,4)`）+ 有界队列 + 单 run/进程内存预算 + 降级顺序（降 W → 降预加载窗口 → 拒绝）；⑤ 指标 `runs_active/runs_queued/cache_bytes_total/wasm_bytes_total`；⑥ repoguard：`Engine` 结构体禁止新增 run 级可变字段（AST 白名单）。
- **④ 约束与非目标**：**禁止**用全局锁串行化 `RunBacktest` 掩盖问题；不改变单 run 的结果（回归必须逐位一致）；不动 `stateStore` 的 LRU 语义。
- **⑤ 验收**：A（有财报）/B（无财报）并发两笔，结果与各自串行逐位一致（含"B 先 Warm、A 后读"的确定性交错用例）；连续 50 个不同票池 run 后 RSS 回落基线 ±10%；10 个全市场 run 并发不超预算（其余排队/拒绝），kill 一个后内存回落。
- **⑥ 边界**：只做"run 级状态隔离 + 内存账"；复权锚点在 QT-13。

### QT-13 · 复权锚点按 run 区间切片（digest 不许覆盖合并缓存）[P0] ⬜

- **① 目标**：同参数 run 在"先跑长区间、再跑短区间、重启后再跑短区间"三种情形下结果一致；锚点不会因为缓存里有别人的数据而永久校验失败。
- **② 上下文**：`p0-remediation-design.md` A11；`adjustment_snapshot.go:322`（`fullBars = cache.Peek`）、`:151-156`（`AnchorDate=bars[0].Date` + `seriesDigest(quantizeBars(bars))`）、`:134-137`（digest 不符即 error → run 失败）。A11 直接读 A10 的合并缓存，所以必须在 QT-12 之后。
- **③ 要求**：① `fullBars` **先按 `[params.StartDate, params.EndDate]` 切片**再交给 `prepareAdjustmentRun`；② `AnchorDate` = 切片 `[0]`；③ digest 输入集合与 `(symbol,start_date)` 主键语义对齐；④ 回归测试三条（长→短→重启→短、首跑时缓存有旧数据、跨进程重启）。
- **④ 约束与非目标**：不改复权数学口径（qfq 因子计算）；不动 `ON CONFLICT (symbol,start_date)` 主键；不清理历史锚点行。
- **⑤ 验收**：三条回归全绿且三次结果逐位一致；构造"缓存含更长序列"的交错用例不再产生 anchor 校验失败。
- **⑥ 边界**：只做锚点区间；复权因子本身的正确性属 QT-52 的核对范围。

### QT-14 · Provider 生命周期（`BeginRun` / `EndRun`）[P0] ⬜

- **① 目标**：有状态 provider 的会话随 run 创建与销毁；"有状态但没实现生命周期"在**装配期**就失败，而不是跑起来才发现串状态。
- **② 上下文**：`p0-remediation-design.md` A1；`signal_provider.go:41-44` `Signals(ctx,scope,asOf)` 无 runID；`handlers_backtest.go:136` 共享 runner。
- **③ 要求**：① `RunMeta`（RunID/Strategy/Params/Start/End/Universe）+ `SessionLifecycle`（BeginRun/EndRun）+ `Stateful() bool`；② `Engine.RunBacktest` 拿到 `backtestID` 后 BeginRun + `defer EndRun`（错误路径也走）；③ `EndRun` = `finalize()` + `Close()`，信号归本 runID，不许跨 run 合并；④ provider 内 `map[runID]*session` 加锁；session 上限超限 fail-closed 不排队；⑤ 装配期：`Stateful()==true` 但未实现 SessionLifecycle ⇒ Fatal；⑥ 顺序 run 也必须走 Begin/End（不做"复用会话"优化）。
- **④ 约束与非目标**：不把生命周期做成"可选接口 + 静默降级"；不改 `Signals` 的业务语义；`finalize` 的信号归属见 QT-24。
- **⑤ 验收**：同一策略并发两笔（不同 universe）与单独跑逐位一致；连续两次同参一致；有状态未实现 lifecycle 的 provider 装配失败。
- **⑥ 边界**：只做生命周期；终止后的重建在 QT-15。

### QT-16 · 检查点身份与版本（shard_key + run_identity_hash）[P0] ⬜

- **① 目标**：恢复检查点时能回答"这份字节属于哪个实例、哪个票池、哪版参数"；symbol scope 的 N 个分片状态不再互相覆盖。
- **② 上下文**：`p0-remediation-design.md` A9 + `p0-round2-review.md` N3；`pkg/strategy/state_store.go:60-69`：PK `(strategy_id, run_id)` + UPSERT 只留最新一份；`BarContext.State` 是裸 `map[string][]byte`；状态里的 `symbol_idx` 由 `RunUniverse` 决定。
- **③ 要求**：① PK 扩为 `(strategy_id, run_id, shard_key)`（market=`''`，symbol=标的）；② 信封四元组 `{state_schema_version, artifact_hash, abi_version, run_identity_hash}`，其中 `run_identity_hash` 覆盖 `run_universe_hash + scope + shard_key + param_effective_hash`；③ 恢复时四元组全匹配才恢复，任一不符 → `ErrStateIdentityMismatch` 并点名差异项（票池变了/参数变了/artifact 变了）；④ `ts` 单调不减校验；⑤ DDL 迁移（旧行 `shard_key=''`）+ 清单里的 `state_schema_version` 声明与源码 hash 变更告警。
- **④ 约束与非目标**：不删旧检查点；不改 `checkpoint.go` 的"日期边界"裁决（那是正确设计）；不做"尽力解释旧格式"。
- **⑤ 验收**：symbol scope 5000 分片 → 5000 行且 `shard_key` 唯一；中断续跑与一次跑完逐位一致；换票池/换参数/换 artifact 三场景恢复均被拒且错误可读；market 旧行平滑迁移。
- **⑥ 边界**：只做状态身份；分片调度是 QT-25。

### QT-15 · 不可终止执行 + 重放语义（`state_only`）[P0] ⬜

- **① 目标**：`while(true)` 能在预算内被终止且进程存活；终止后的重建**不会重复下单**。
- **② 上下文**：`p0-remediation-design.md` C1 + `p0-round2-review.md` N4；`wazero_runtime.go:41-43` 只有 `WithMemoryLimitPages`，未启 `WithCloseOnContextDone`（wazero 默认关闭，关闭时单次调用可永久阻塞 Goroutine/OS 线程）；C1 只写"重建并重放该日期块剩余 bar"，没说重放期 emit 怎么处理。
- **③ 要求**：① 启用 `WithCloseOnContextDone(true)`，超时返回 `sys.ExitError`；② 双层预算（单次调用 market 50ms / symbol 5ms 可配 + run deadline 默认 10min）；③ `ErrSessionTerminated` 与 `terminated` 状态，终止后禁止调用；④ **`replay_mode = state_only` 为默认**：重放期 `emit_signal`/`emit_target2` 成功但不落 Signals，计数 `replay_suppressed_signals`；重放到故障 bar 后以 `apply` 重执行该 bar；⑤ terminated 的 `EndRun` **不调 finalize**，run 标 **`invalid`** + 稳定 `invalid_reasons` code **`session_terminated`**（映射进冻结三值 `completed/invalid/failed`，不引入 `degraded` 第四值，见 QT-10 ③-④ 的映射规则）；`Failures += session_terminated` 仅作诊断明细；⑥ 重建次数按 run 计（上限 3），超限 `failed`；⑦ 开/关周期检查各跑一遍 market scope 727 次 on_bar，量化开销（>10% 则按可信度分级启用，结论成文）。
- **④ 约束与非目标**：不用 Gas（wazero v1.12 无此 API）；不做"批量喂全序列"降级；不把 `apply` 重放做成默认（需事务性回滚，成本高）。
- **⑤ 验收**：`while(true)` 在单次预算内终止且进程存活；在 bar k 注入一次超预算，重建后成交笔数/成交价/净值与无 trap 跑法**逐笔一致**；`replay_suppressed_signals` 计数符合预期；性能基准报告归档。
- **⑥ 边界**：只做终止与重放；检查点身份是 QT-16。
- **⑦ 前置（硬）**：**QT-16 已合入**。重放要按分片恢复状态，没有 `shard_key` 与 `run_identity_hash` 就分不清「这份字节属于哪个实例」；market scope（`shard_key=''`）可先验，但 symbol scope 必须等 16。

---

## 三、W2 · 协议落地

### QT-20 · 参数投递两层（`params_defaults` ⊕ `strategy_params`）[P0] ⬜

- **① 目标**：run 覆写参数真正生效且可追溯；未知键 400；执行期不再读 DB 的 `strategies.params`。
- **② 上下文**：`p0-remediation-design.md` A3 / D26；`contracts.go:171-199` 无 params；`ReloadFromDB`/`ConfigureStrategy` 零生产调用点。
- **③ 要求**：① `BacktestRequest` 加 `StrategyRef` + `StrategyParams`；② 生效参数 = 默认 ⊕ 覆写（浅合并），未知键按 `ValidateRunSpec` 口径拒绝；③ 类型/范围校验用清单 `manifest.params[]`；④ 经 `RunMeta.Params → initialize(paramsJSON)`；⑤ `run_artifacts` 存 `params_json + params_hash`（QT-03 的字段）。
- **④ 约束与非目标**：不支持嵌套键（避免歧义）；不改 `strategies.params` 的管理面语义（只是执行期不再读它）；不做参数扫描器（属编排 QT-44）。
- **⑤ 验收**：同策略不同参数并发两笔各自生效；未知键 400 且点名；从历史记录能完整还原生效参数。
- **⑥ 边界**：只做参数通路；版本哈希是 QT-22。

### QT-21 · 信号顺序规范化 + 重复语义落地 [P0] ⬜

- **① 目标**：同一输入 20 次跑信号序列完全一致；Go 与 WASM 顺序差异不再造成假失败。
- **② 上下文**：`p0-remediation-design.md` A8/A12 + `p0-round2-review.md` §8.3（QT-04 的结论）。`engine.go:1263` 只排序 `tradingDays`；`examples/value_momentum.go:366` `for symbol := range map` 已证非确定。
- **③ 要求**：① 应用信号前按 `(Symbol asc, Direction 固定序, Strength desc, CompositeScore desc)` **稳定排序**；② 排序口径写进 `SignalSpec`，Go/WASM/NT 共用；③ 按 QT-04 结论实现重复语义（trap 或 `close` 优先合并 + 计数）；④ 先跑"旧顺序 vs 新顺序"差异报告，差异可解释后再切换。
- **④ 约束与非目标**：**不允许**为保持结果不变而放弃排序；不合并不同 bar 的信号；不改 `domain.Signal` 字段。
- **⑤ 验收**：同输入 20 次信号序一致；`value_momentum` 20 次结果逐位一致；差异报告归档；重复语义有专门测试。
- **⑥ 边界**：只做顺序与重复；单量/仓位是 QT-24 的 `emit_target2`。

### QT-22 · `version_hash` canonical 冻结 + 向量测试 [P0] ⬜

- **① 目标**：改 scope / 改默认参数 / 改源码，三者任一都产生新 hash；只改 run 覆写不产生新 hash；序列化顺序被人改动会立刻红。
- **② 上下文**：`p0-remediation-design.md` A4；canonical 集合 = `strategy_id + carrier + abi_version + scope + entry + source + params_defaults`；`factor_definitions.version_hash` 是可复用样板（`factor_def.go:151`）。
- **③ 要求**：① 实现 canonical 序列化（键排序、无空白、UTF-8、数字不经 float 往返）；② SHA-256 小写 hex；③ 向量测试（给定输入 → 固定 hash 常量）；④ `params_defaults` 与 `scope` 必须在集合内（有测试钉住）。
- **④ 约束与非目标**：不含 toolchain（toolchain 差异由 `artifact_hash` 区分，见 QT-03）；不改 `factor_definitions` 的既有算法。
- **⑤ 验收**：三条"必变"+ 一条"不必变"测试；向量常量固定；改动序列化顺序 → 向量测试红。
- **⑥ 边界**：只做哈希算法；资产表结构是 QT-41。

### QT-23 · `RunUniverse`（run 级稳定票池）[P0] ⬜

- **① 目标**：`symbol_idx` 全 run 稳定，不再随当日可交易名单漂移。
- **② 上下文**：`p0-remediation-design.md` B4 + `p0-round2-review.md` §2.6；`engine.go:916` 把 `eligibleUniverse(date)` 当日结果直接当 `Scope.Universe`；`engine_daily.go:130-141` 停牌标的不进 map。
- **③ 要求**：① 按 §2.6 冻结公式算 `RunUniverse = sort(dedup(期初持仓 ∪ StockPool ∪ IndexCode 区间成分并集))`，**run 前一次算完、期间只增不减**；② `Scope` 拆 `RunUniverse`（索引来源）+ `Universe`（当日可交易，禁止用于索引）；③ 不可交易用 `NaN`（+ 可选 `is_tradable`）表达；④ `RunUniverse` 进 `dataset_snapshot_hash`（QT-03 的分量）且在算 hash 之前确定；⑤ 回归：连续 5 个交易日 Universe 增减，索引映射不变。
- **④ 约束与非目标**：不把"曾持有"做成运行中动态扩张（会重新引入漂移）；不改 `eligibleUniverse` 的过滤规则本身；不动停牌估值口径（`pricesCache` 保留最后成交价）。
- **⑤ 验收**：5 日 Universe 增减下 `symbol_idx` 恒定；策略跨日状态不串标的；`RunUniverse` 相同 → `universe_hash` 相同。
- **⑥ 边界**：只做票池稳定性；`BarContext` 布局是 QT-24。

### QT-24 · host 数据模型 v2 落地（Bars / Axis / T）+ `finalize` 归属 [P0] ⬜

- **① 目标**：market scope 能取到 5 个字段、停牌返回 NaN、`get_series_len2` 只返回可见长度；`finalize` 里的平仓信号不再静默丢。
- **② 上下文**：`p0-remediation-design.md` B1/B5 + `adr-029` §7.1；`host_api.go:65-72` `Series []float64` 只有 close，`CrossSection` 也是 close；`OnBarSession.Finalize` 返回 `[]byte`（`on_bar_session.go:95-113`）。数据源：`marketDataCache[sym]` 是"截至当日"的整段切片（`engine_daily.go:181-194`），可直接零拷贝引用。
- **③ 要求**：① `BarContext` 改为 `{T, Axis, Symbols, Bars, State, Signals}`，删 `CrossSection`；② T 语义按 §7.1 表（market=Axis 下标，symbol=自有序列下标）；③ `field_id` 枚举 0..4 接线，其余 trap；④ `Bars[sym]` 预建"日期→下标"索引（不要每次二分）；⑤ `get_series_len2` 返回**可见长度**（日期 ≤ Axis[T]）；⑥ `finalize` 期间 emit 归最后一日、在期末强平**之前**应用，`finalize` 里发 `long/short` → trap；`Finalize()` 返回值定义为诊断字节；⑦ `t_offset` 使 `T+off < 0` → trap。
- **④ 约束与非目标**：整段序列永不进 wasm 内存（仍按坐标取数）；不做"批量喂全序列"；不改 v1 的 `get_bar` 行为。
- **⑤ 验收**：market scope 取到 5 字段；停牌返回 NaN；"整段预加载"模拟下 `get_series_len2` 仍只返回可见长度；finalize 发 `close` 能平仓且成交价取最后一日，发 `long` 被 trap。
- **⑥ 边界**：只做数据面与 finalize；`emit_target2` 的 sizing 在 QT-26。

### QT-25 · `symbol` scope 分片与预算 [P0] ⬜

- **① 目标**：5000 标的 symbol scope 跑完不超预算；预算调小仍能跑完（只是变慢），不 OOM。
- **② 上下文**：`p0-remediation-design.md` C2；abi v2 §3 定 symbol scope 为"每标的分片实例、每实例 64MB"；QT-16 已给每分片独立状态行。
- **③ 要求**：① `W = min(configured_workers, budget/perInstanceEstimate)`，按批起停，批间只留已 flush 状态；② 单 run wasm 总预算默认 512MiB，超预算**降 W**（不失败），W=1 仍超 → fail-closed 并提示改 market；③ 指标 `wasm_instances_live/wasm_bytes_live/wasm_batch_size`；④ 分片边界必须落日期边界（复用 `checkpoint.go` 裁决）。
- **④ 约束与非目标**：不一次性起 5000 实例；不改 market scope 的实例模型；不与 QT-12 的进程总预算冲突（run 内 wasm 预算 ≤ 进程预算的份额）。
- **⑤ 验收**：5000 标的跑完不超预算；预算调到很小仍跑完；跨批恢复与单批结果逐位一致；每分片状态行唯一（QT-16）。
- **⑥ 边界**：只做分片调度与预算；单实例内存上限仍由 runtime 管。

### QT-26 · WASM `SignalProvider` 接线 + `EmittedSignal` 转换 [P0] ⬜

- **① 目标**：一条真实回测能从 artifact 加载 → 逐 bar 执行 → 产出信号 → 成交，且 `emit_target2` 的 sizing 与 policy cap 正确。
- **② 上下文**：`implementation-plan-mvp.md` G2/G3；`pkg/sandbox/wasm` 整包仍在 repoguard 未接线白名单。
- **③ 要求**：① `SignalProvider` 的 WASM 实现（`OnBarSession` → `Signals`，走 QT-14 的 per-run 实例）；② `EmittedSignal → domain.Signal` 映射（Action→Direction、Strength、SymbolIdx→Symbol 经 `RunUniverse`）；③ `emit_target2` → 目标权重→手数（`floor(|w|×equity/price/lot)×lot`），policy cap 优先并计 `sizing_clamped_total`；④ 结果标 `sizing_source`；⑤ 五道门（Resolve/Fetch/Verify/Compile/Mount）按 `strategy-asset-service-api.md` §4——其中 **Compile 门依赖待裁决 D（编译服务归属）**，未裁决前只对表语义、不实现编译链路（实现属 QT-41b）。
- **④ 约束与非目标**：内循环零网络（resolve/fetch 只在 run 前一次）；不实现编译服务（**QT-41b**，且其开工依赖裁决 D）；不绕过 `ValidateImports`。
- **⑤ 验收**：手工 artifact（无需服务）能跑通一次真实回测；分层加仓在 Go/WASM 下逐位一致；混用 `emit_signal`/`emit_target2` 被 trap。
- **⑥ 边界**：只做接线与转换；M0 的验收是 QT-30。

---

## 四、W3 · 里程碑

### QT-30 · M0：市场级状态机 PoC（Go vs WASM 逐位一致）[P0] ⬜

- **① 目标**：证明"运行时动态加入状态机策略"这条路在 market scope 上端到端成立。
- **② 上下文**：`implementation-plan-mvp.md` M0；前置：QT-10/11/12/13/14/15/16/20/21/24/26 完成（D1 明写 A2/A3 是对拍有意义的前提）；工具链：TinyGo（Windows 二进制或 Docker），产物 `-target=wasm-unknown`、env-only。
- **③ 要求**：① 装 TinyGo 并编出 env-only 状态机（regime 开关 + 冷却 + 总仓位闸门，`scope=market`，727 次 on_bar）；② 用 `OnBarSession` 跑通一次真实回测；③ 同一状态机写 Go 内置版；④ 两版逐位对拍（净值/成交/信号序列）。
- **④ 约束与非目标**：M0 **必须** market scope（D1 裁决）；不建任何服务、不接 AI；不碰 DB schema 迁移。
- **⑤ 验收**：Go 内置 vs WASM 回测结果**逐位一致**（含成交笔数与每笔价量）；`while(true)` 版本被预算终止且 run `failed`（QT-15 的端到端验证）。
- **⑥ 边界**：只证明载体可用；symbol scope 是 QT-31。

### QT-31 · M0b：symbol scope 状态机 PoC（分片 + 分层加仓）[P1] ⬜

- **① 目标**：验证 `scope=symbol` 的分片实例、状态隔离、`emit_target2` 分层加仓在真实规模下可用。
- **② 上下文**：`implementation-plan-mvp.md` M0b；前置 QT-25（分片预算）、QT-16（shard_key）、QT-24（emit_target2 语义）、QT-30（载体已证明）。
- **③ 要求**：① 写"持仓状态机 + 冷却期 + 分层加仓"（每标的一份状态）；② 用 `emit_target2` 表达三层加仓；③ 5000 标的（或可代表的小票池外推）跑完并记录实例化成本/内存峰值；④ Go 内置 vs WASM 逐位对拍；⑤ 跨批恢复与单批一致。
- **④ 约束与非目标**：不改 market scope 默认；不为跑完而放宽预算；不做实盘接线。
- **⑤ 验收**：逐位一致；内存不超预算；跨批恢复一致；`sizing_clamped_total` 与 policy cap 行为符合预期。
- **⑥ 边界**：只做 symbol scope 验证；生产接线属后续。

---

## 四之二、W4 · AI 策略生成闭环

### QT-32 · W4：AI 策略生成闭环（promote 人工闸门 + 全链留痕）[P1] ⬜

- **① 目标**：产品主目标落地——一条 AI 生成的策略，从 prompt 到回测结果**全程留痕**（源码 → hash → 结果），且 AI 产物必须过 `promote` 人工闸门才进生产回测（`implementation-plan-mvp.md` §6-M2 验收原文）。
- **② 上下文**：`implementation-plan-mvp.md` 档 2（「代码→编译→资产服务→引擎缓存加载执行」是**要交付的目标**）、G6（AI 生成闭环：Go 子集 + 静态校验 + promote）、§6-M2 里程碑；AI 生成/实验编排归 `tellurion-agent` 仓（ADR-023 三层模型，core 不实现第二个编排者）；编译管线属 QT-41b（本单是其消费方）。**本单承接 G6/M2**——refactor-plan-review H4 指出原台账在 W3 后断链，无单承接产品主目标，本单即补缺（若产品侧改判「G6/M2 不在本代范围」，则删除本单并在执行顺序总览显式声明划出，不得沉默）。
- **③ 要求**：① AI 生成 Go 子集 → QT-41b 编译管线（TinyGo + 静态校验：import 白名单 / 体积 / 内存上限）→ `artifact_hash` 登记（QT-03）；② **`promote` 人工闸门**：AI 产物默认不进生产回测，须人工审批后才可被 `strategy_ref` 解析为可执行资产（闸门记录进审计，审批人/时间/结论留痕）；③ 全链留痕：prompt / 生成源码 / 编译产物 hash / 回测 `run_spec_hash` / 结果串成可追溯链（复用 QT-03 三 hash + `run_artifacts`）；④ 状态机三路一致性（Batch / Step 从零 / Step 从断点）+ dualrun K15 对拍作质量门（AI 质量问题靠与内置 Go 同逻辑对拍抓，`implementation-plan-mvp.md` §5）；⑤ AI 侧护栏沿用 agent 仓既有 AST/白名单（不新造第二套）。
- **④ 约束与非目标**：不把 AI 编排逻辑搬进 core（ADR-023）；不省略人工闸门做「自动 promote」（档 3 是远期，不在本单）；不在本单做编译服务实现（QT-41b）；不为 AI 产物放宽 `ValidateImports` / 预算 / fail-closed 任何一条。
- **⑤ 验收**：一条 AI 生成策略从 prompt 到回测结果全程可追溯（链上每环有 hash 或留痕记录）；未过 promote 的 AI 产物被回测路径拒绝（fail-closed 用例）；三路一致性 + K15 对拍通过；破坏验证：绕过闸门直接登记 → 有测试红。
- **⑥ 边界**：只做闭环接线与闸门/留痕；生成质量与实验编排属 agent 仓（其台账另立单）；编译管线是 QT-41b；资产存储是 QT-41。

---

## S2~S3 · 拆仓（W0 与 QT-05 全绿之后）

> 依据 ADR-D29 §7-S2/S3、§8 建仓 checklist。**拆的是已经冻结的东西**——W0 未绿就拆 = 契约每天发版。

### QT-17 · agent 数据自持（D10：`ai.*` schema 建表与迁移）[P1] ⬜

- **① 目标**：agent 不再通过 core 的 `*PostgresStore` 读写实验域数据；`ai.*` 的 DDL 与 migrate 由 agent 仓持有——**这是 S3 删 replace 的数据前置**（storage 依赖没有别的出路）。
- **② 上下文**：ADR-D10（实验域数据归 agent）；实测 agent 用到 storage 的 16 个符号 / 14 个文件：连接层 `PostgresStore`(12) `NewPostgresStore`(3) `BuildDSN` `DatabaseConfig`；实验域 `Experiment`(5) `ExperimentMetrics`(6) `ExperimentUpdate`(4) `ExperimentStatus*`；研究域 `ResearchProfile`(10) `ResearchQuestion` `ResearchConclusion`；**仪器域** `TradingCalendar` `ListingWindow`(4)。`kit/db` 由 QT-05 提供。
- **③ 要求**：
  ① agent 仓建 `ai` schema 的 DDL（表名以 core `pkg/storage/experiments.go` / `research.go` / `postgres.go` 的现有定义为准，逐表核对后迁移，**不凭类型名臆造表名**）与自己的 migrate 入口；
  ② agent 用 `kit/db` 建自己的 store，替换 12 处 `PostgresStore` 用法；
  ③ **仪器域不迁**：`TradingCalendar` / `ListingWindow` 属 core，agent 改走 core 的**只读 port**（D10 §87「只读视图」通道）——**本单需同时补进 D24 的合法动作清单**（现六类里没有「交易日历/上市窗口查询」）——这是对**已裁决文档的增补**，开工前需用户确认第 7 类动作；**获确认后该动作的 endpoint + DTO + 鉴权随 QT-50 ③-③/④ 统一落 `docs/openapi.yaml` 并纳入路由表对拍**（勿另起手写 DTO，refactor-plan-review H2）；
  ④ 双写期：旧表与 `ai.*` 同时写 → 行数/校验和对拍 → 切读 → 停写；core 侧 `experiments.go` / `research.go` 定义标 deprecated 并登记删除期限；
  ⑤ 护栏：agent 禁止 import `pkg/storage`（AST）。
- **④ 约束与非目标**：**不删 core 旧表**（保留只读一个周期）；不迁行情/回测结果/因子定义（仪器域）；不改 core 的实验 handler；不在本单做 HTTP 化（QT-50）。
- **⑤ 验收**：agent `grep -r "pkg/storage"` = 0；`ai.*` 行数与旧表对拍一致（报告归档）；停写后 core 旧表不再增长；core 侧 deprecated 标记存在；日历/上市窗口经 port 取到且值一致。
- **⑥ 边界**：只做实验/研究域的数据归属；`ai.*` 之外的 schema 不动；X 线服务的数据归属按 QT-40。

### QT-07 · 两仓打 tag 并把伪版本切成版本依赖（S2 → S3）[P1] ⬜

- **① 目标**：`tellurion-contracts` / `tellurion-kit` 各自**单仓可构建、可自证**（R1~R5 全成立）；core 与 agent 从「伪版本」切到「`v0.1.0`」；agent 最终删掉 `../tellurion` replace。
- **② 上下文**：`repo-topology-and-naming.md` §7/§8（D29）+ **ADR-D31**；前置 = W0 + QT-05 全绿。**两仓在 S1 已有实现与真实伪版本依赖**——本单**不再 `git init`、不再迁目录**（原 ①/② 已随 Plan A 作废），只做「伪版本 → tag」与自治补齐。**分两次合入**：S2（①~⑤）先合；S3（⑥）等 QT-50 + QT-17 完成后再合——不要因为 ⑥ 未完成而卡住 ①~⑤。
- **③ 要求**：
  ① S2-a：两仓 `git tag -a v0.1.0 -m "v0.1.0: <类型> ..."` 并 push，**tag 必须在 `main` 上**（tag 不 push 不可 `go get`）；
  ② S2-b：core / agent 把 `require` 的伪版本换成 `v0.1.0` + `go mod tidy`（`go.sum` 同步更新）；**`go.work` 仍不入库**（本地继续用它联调三仓）；
  ③ S2-c：两仓复制主仓 CI 门禁（gofmt / vet / test / repoguard），**不得 checkout 其他仓**；contracts 仓补 `examples/`（每个 port 一个最小用法，QT-09 ③ 复用）与 `docs/TASKS.md`（AGENTS 建仓时已有）；**补随仓接口规格载体**（refactor-plan-review M6/L5）：contracts 仓建 **`docs/API.md`**（导出面完整清单——四契约 + port + 值对象 + 哨兵错误，逐项标稳定性标注与错误语义，指回 `tellurion:docs/SPEC.md` 对应节号）、kit 仓建 **`docs/SCOPE.md`**（12 包逐包准入/禁入清单，源出 `target-module-topology.md` 的包边界表）；二者随 CI 的 AST 护栏同步更新；**私有仓拉取前提**：两仓若设私有，全部 CI/构建环境须配 `GOPRIVATE=github.com/ruoxizhnya/*` 与 git 凭证（否则 `go get` 拉不到），在两仓 AGENTS.md 各写明一行；
  ④ S2-d：主仓加反向护栏——禁止 import 已迁出路径、**禁止对两仓写 `replace`**；
  ⑤ S2-e：把 `tools/check_contract_pin.py` 接进 CI（QT-09 ①），并在 core `AGENTS.md` 写 3 行「契约 pin 怎么 bump」；
  ⑥ S3（**依赖 QT-50 + QT-17 完成**）：agent 删 `../tellurion` replace 与 CI 的 rewrite 步骤；agent 只依赖两个新仓的 tag + HTTP。**注意 S3 是唯一允许删该 replace 的时点**，提前删 = agent 立刻编不过。
- **④ 约束与非目标**：**不改任何业务逻辑**（只换版本号）；不在拆仓同时做 HTTP 化（那是 QT-50）；不为"减少仓数"合并两仓（依赖方向不同：contracts 依赖 kit 的 decimal/errors，kit 零业务）；不发布 release note（CHANGELOG 见 QT-19）。
- **⑤ 验收**：两仓单独 checkout 后 `go build ./...` + `go test ./...` 绿（不 checkout core/agent）；core / agent `go list -m` 显示 `v0.1.0` 且**无 `=>`**；`make contracts-pin-check` 绿；`tools/check_repo_autonomy.py` 对两仓全绿；切 tag 前后同参回测**逐位一致**（报告归档）；agent 的 R1 在 S3 后转绿。
- **⑥ 边界**：只做版本切换与自治补齐；X 线六个服务仓**不在本单**（真起进程时另立）。

### QT-09 · 跨仓依赖护栏 + 契约测试（S2）[P1] ⬜

- **① 目标**：ADR-D29 §9 的 C1~C5 五条规则**机械可查**；「两边各写一份理解」能在契约层被发现，而不是靠 K15 双跑。
- **② 上下文**：`repo-topology-and-naming.md` §9；现状无任何跨仓护栏（agent CI 反而在「帮」跨仓构建：checkout core + `go mod edit -replace`）；契约仓**自 S1 起就存在且有实现**（ADR-D31），但「以版本被依赖」要等 S2 的 QT-07。
- **③ 要求**：
  ① CI 步骤：`replace` 指向**仓外**路径 → 失败（**ADR-D31 之后 core 与两新仓本就不该有 `=>`**，唯一白名单是 agent 的 `=> ../tellurion`，S3 删）；`go.work*` 被 git 跟踪 → 失败（C1/C2）；**contracts/kit 的 pin SHA 必须是该仓 `main` 的祖先**，否则失败（Plan B 的代价，见 ADR-D31 与 QT-05 ⑦）；
  ② 契约仓 `examples/`：每个 port 一个最小用法，让下游 agent 不读实现即知调用形状；
  ③ **契约测试**：同一组用例分别喂 core 与 agent 的实现，语义漂移即红（C3/C5）；
  ④ 契约仓 PR 模板必填「哪个使用方需要这条变更」；
  ⑤ 契约仓 AST 扫描：禁 `Config`/`Options`/`Impl` 命名、禁 DB import（C4）。
- **④ 约束与非目标**：不比对「两边代码是否相同」（只比契约行为）；不引入新测试框架；不阻断 additive 变更（C5 只约束 breaking）。
- **⑤ 验收**：故意提交一个仓外 replace → CI 红（留证后撤回）；契约测试能捕获「一侧改了默认值/语义」；PR 模板缺填时 CI 提示；`go.work` 被 add 时 CI 红。
- **⑥ 边界**：只做护栏与契约测试；契约内容本身由 W0 冻结。

### QT-19 · 契约版本策略与兼容矩阵（C5 落地）[P1] ⬜

- **① 目标**：把 D29 §9 C5「破坏性变更保留旧接口一个版本周期」变成可执行纪律——否则每次 breaking 都靠人记，`deprecated` 会永远不被删也不可能被删。
- **② 上下文**：D29 §9 C1~C5；现状三仓 tag 全为 0（QT-06 只解决 core），无 semver 惯例、无兼容矩阵、无 CHANGELOG。contracts 拆仓后独立发版，必须有规则。
- **③ 要求**：
  ① contracts 仓 README 写**版本规则表**：`MAJOR` = 删/改签名或语义；`MINOR` = 新增能力（向后兼容）；`PATCH` = 文档/注释/无行为修正；**kit 仓同表**（kit 同为被依赖的库，`MINOR` 含新增导出；refactor-plan-review L4）；
  ② **0.x 阶段（现在到 1.0 前）允许 breaking，但必须 bump MINOR + 写 CHANGELOG 条目**；**1.0 之后**才启用「deprecated 至少保留 1 个 MINOR 周期」；
  ③ **兼容矩阵**：contracts `vX.Y` ← 支持 core / agent 的哪些版本 + **kit `vX.Y` ← contracts / core 的兼容范围**，分别写在两仓 README，每次发版更新；
  ④ 内容寻址标识（`strategy_ref` / `artifact_hash` / `run_spec_hash`）**不参与 semver**——它们自带版本语义；
  ⑤ tag 规范：annotated tag，message 首行 `vX.Y.Z: <类型>`（feat/fix/breaking）。
- **④ 约束与非目标**：不引入 release 工具链（goreleaser 等）；不做自动 CHANGELOG 生成；不强制 core 也走同一套（core 是应用不是库，tag 只作依赖锚点）。
- **⑤ 验收**：contracts 与 kit 两仓 README 各存在版本规则表与兼容矩阵；CHANGELOG 至少有一条记录；三个仓的 require 版本都落在矩阵声明范围内。
- **⑥ 边界**：只定策略与文档；「禁删 deprecated」的机械护栏由 QT-09 ⑤ 实现；`GOPRIVATE`/git 凭证前提见 QT-07 ③-S2-c（refactor-plan-review L5，本单不重复登记）。

---

## 五、X 线 · 服务拆分（可选部署形态，进程内 port 先行）

> **X 线公共前置（M5/M8，QT-40~45 与 QT-41b 开工前必读，两项都不满足不开工）**：
> ① **建仓/升格前先与 ADR-027 §2 对账**（M5）：六候选是**能力边界清单，不是最终服务拓扑**（`service-extraction-plan.md` 自述「与 ADR-027 §2 的 5→7 未逐一对账」）；升格建仓前必须与 027 §2 服务口径逐一对账（可能触发 data A/B 二分、reduction-service、分析并入他服务等改判），对账结论落 `repo-inventory.md`；「12 仓」是**上限/当前规划**，不是承诺终态。
> ② **「接口对表」小节**（M8）：对应服务设计文档开工前补「接口对表」——逐个 port/endpoint 列**签名 + 错误语义 + 版本策略**，显式引用 `kernel-interface-map.md` / contracts 仓对应符号（模板：`data-service-contract-and-ownership.md` 的 `UnifiedDataPoint` 节）；**未对表不开工**。跨进程 wire 面（HTTP endpoint/DTO/鉴权）按 QT-50 ③-③/④ 统一落 `docs/openapi.yaml`，不另起炉灶。

### QT-40 · X1 数据服务（统一协议 + aggregator + `dataset_snapshot_hash`）[P1] ⬜

- **① 目标**：数据协议冻结进 contracts；aggregator 能给出可复现的 `dataset_snapshot_hash`；connector 全部走声明式契约。
- **② 上下文**：`data-service-contract-and-ownership.md`（D19）X1a~X1d；现状已实现约 80%（12 connector + `ConnectorDeclaration` + `ETLPipeline`/`Normalizer`/`ValidatePoints`/`Deduplicate`；PG 已有 ingest/market/quant/research）。
- **③ 要求**：① 协议类型（`UnifiedDataPoint`/`OHLCV`/`FrequencySpec`/`TimeSemantics`）进 QT-05 的 contracts；② `ErrVisibleAtMissing` 等错误码；③ aggregator 面（PIT 过滤 + 频率聚合 + 票池解析 + 快照 hash + 缓存）；④ `dataset_snapshot_hash` 按 QT-03 的唯一定义实现（含 `calendar_hash`/`adjustment_basis`）；⑤ 12 connector 全走 Declaration + 共享 Normalizer。
- **④ 约束与非目标**：不重写现有 connector；不在内核内保留第二写者；不做跨库拆分。
- **⑤ 验收**：同一 hash 两次拉取返回一致；漏声明 `visible_at` 的源被拒收；改日历/复权口径 → hash 变。
- **⑥ 边界**：只做数据面；策略/风控/分析各自的 schema 见后续单。

### QT-41 · X2 策略资产服务（定义/版本/artifact/resolve）[P1] ⬜

- **① 目标**：策略真相源在服务侧，引擎只持可驱逐只读副本；`id@artifact_hash` 可离线复现。
- **② 上下文**：`strategy-asset-service-api.md` + `strategy-source-of-truth-and-cache.md`（D13/D14）+ QT-03 的 hash 体系；**未决 A（BYTEA vs 对象存储）——下文 ③-① 的 BYTEA 落地是「假设，需裁决后生效」**（阻塞单：本单，裁决见设计集 README 待裁决表 A 行；未裁决前不开工 ③-①）。
- **③ 要求**：① `strategies` 加列（carrier/abi_version/scope/capability/entry/`active_version_hash`/`active_artifact_hash`）+ `strategy_artifacts` + blobs（**假设，需裁决 A 生效后落地**：blob 载体按 BYTEA 假设写，A 若改判对象存储则本项重写）；② 写侧 7 端点、读侧 2 端点（resolve/artifact）；③ 缓存按 `artifact_hash`（永不失效）+ latest 的失效策略——**TTL vs 事件失效二选一属未决 F，未裁决前本项按「假设，需裁决后生效」处理，不得静默二选一**（阻塞单：本单，裁决见 README 待裁决表 F 行）；④ promote 不覆盖 artifact 行；⑤ 14 个内置策略登记为 `carrier=builtin`。
- **④ 约束与非目标**：策略服务**不算信号**；不改执行路径的"先查 artifact 再回退内置"在 QT-26 已定的顺序；不做对象存储（除非 A 改判）；**A/F 未裁决的假设项不许按假设值直接上 main**（裁决落地前相关子项保持 ⛔）。
- **⑤ 验收**：服务停机后已缓存 hash 的回测照跑且结果一致；同源码两工具链 → 两 artifact 可分别追溯；`id@version_hash` 解析结果回写实际 `artifact_hash`。
- **⑥ 边界**：只做资产与解析；编译服务是独立单 **QT-41b**（本单 ①~⑤ 不含编译链路）。

### QT-41b · X2 策略编译服务（Go 子集 → artifact 管线）[P1] ⛔（阻塞于待裁决 D：编译服务归属）

- **① 目标**：策略源码到 `artifact_hash` 产物的编译链路成文并落地（编译 → 校验 → 登记 artifact），AI 生成闭环（QT-32）与人工 promote 共用同一条管线。
- **② 上下文**：`strategy-asset-service-api.md` §4 五道门中的 Compile 门；`implementation-plan-mvp.md` §6-M2（AI 生成闭环含编译环节）；原 QT-41 ⑥ 曾以「QT-41b」悬空引用（refactor-plan-review H3），本单即其正式落点。**归属未定**：编译服务归策略资产服务（X2）还是独立能力，属待裁决 D（见设计集 README 待裁决表）；未裁决前本单 ⛔ 不开工。
- **③ 要求**：① Compile 门语义成文（toolchain 版本固定、env-only 白名单、产物 `-target=wasm-unknown`）；② 编译产物经 QT-03 的 `artifact_hash` 登记进 QT-41 的 `strategy_artifacts`；③ 同源码两工具链 → 两 artifact 可分别追溯（与 QT-41 ⑤ 对齐）；④ 编译失败的错误语义进 contracts 哨兵错误族；⑤ 与 QT-26 ③-⑤ 五道门的 Resolve/Fetch/Verify/Mount 分界逐项对表。
- **④ 约束与非目标**：不实现第二个编排者（ADR-023）；不放宽 `ValidateImports`；不在本单做 promote 闸门（那是 QT-32）。
- **⑤ 验收**：一份 Go 子集策略源码经本管线产出 artifact 且 `artifact_hash` 可离线复现；编译失败可检索；五道门对表文档归档。
- **⑥ 边界**：只做编译链路；资产存储/解析是 QT-41；AI 生成闭环是 QT-32。

### QT-42 · X3 风控服务（规则真相源 + 进程内 RiskPolicy）[P2] ⬜

- **① 目标**：风控规则可版本化、可审计；回测与实盘调用**同一** RiskPolicy；结果能记 `risk_rules_hash`。
- **② 上下文**：`risk-service-design.md`（D20）；`risk.rule_defs`/`rule_revisions`/`rule_snapshots` 待建；`risk_events` 表未落地（`OrderIntent` 注释已指向）。
- **③ 要求**：① 规则表 + `version_hash`（复用 factor 样板）+ 快照下发 `/api/risk/snapshots/{hash}`；② 进程内 `RiskPolicy` 接口化（现状 `RiskManager` 已无状态，可直接适配）；③ `CheckOrder` **永不走 HTTP**；④ 熔断指令走异步；⑤ 结果记 `risk_rules_hash`（QT-03 分量）。
- **④ 约束与非目标**：不把逐笔裁决搬出进程；不与 compliance/alert 的既有实现重复造；不改 `RiskManagerConfig` 的默认值。
- **⑤ 验收**：锁定 hash 的规则快照可复现；换规则 → `run_spec_hash` 变；双跑对拍一致。
- **⑥ 边界**：只做规则与裁决面；聚合监控/报送属同单的后续切片。

### QT-43 · X4 分析服务（基准 / 归因 / 敏感性 / 蒙特卡洛）[P1] ⬜

- **① 目标**：补齐 BE-05/BE-06 的真实缺口，且分析只读 run 结果、零 import 引擎。
- **② 上下文**：`analytics-service-design.md`（D21）；回测族 grep benchmark/attribution 仅一行注释；MC/bootstrap 全无；`mc_runs.seed` 必填。
- **③ 要求**：① 搬现有 metrics/compare/correlation/export；② 新增基准对比 + Brinson/因子归因；③ 参数敏感性（网格）+ 蒙特卡洛/bootstrap（seed 留痕）；④ 只按 `run_id` 拉结果；⑤ `analysis.*` schema 唯一 owner。
- **④ 约束与非目标**：纯函数 + 只读；不 import `pkg/backtest`；不在分析层做交易决策。
- **⑤ 验收**：同一 run + 同一 seed 两次分析结果一致；归因分项和 ≈ 总收益（容差写明）；缺 seed 直接拒绝。
- **⑥ 边界**：只做分析与指标；报表呈现归 UI 仓。

### QT-44 · X5 编排 / worker 服务（job/batch/walkforward 上移）[P2] ⬜

- **① 目标**：批量扫描的算力出口独立成服务；`backtest_jobs` / `strategy_state` 归属迁出内核；三 hash 落 `run_artifacts`。
- **② 上下文**：`orchestration-service-design.md`（D22）+ BE-03；`BatchEngine` 已按 `contracts.EngineRunner` 注入，摘出成本低。
- **③ 要求**：① `job`/`batch`/`walkforward` 从 `pkg/backtest` 上移；② worker 池调度 + 断点续跑协调；③ `run` schema（`backtest_jobs`/`walk_forward_reports`/`run_artifacts`/`strategy_state`）；④ `/api/runs/{id}/provenance` 端点；⑤ 内核通过 port 读写这些表。
- **② 前置（U3，2026-10-20 审查补登记）**：`strategy_state` 迁 `run` 之前，core 必须先有 **`RunStateStore` port**（`Save(ctx, runID, state)` / `Load(ctx, runID)`），否则**内核失去状态持久化、X5 一动就编不过**。这个 port 以前谁都没登记，只在 `orchestration-service-design.md` 里被当成"内核走 port"一句带过。
- **④ 约束与非目标**：不在内核保留编排逻辑；不改 `EngineRunner` 契约；不做 K8s；**不搬 `cmd/dualrun`**——它 2026-10-20 已裁决留 core（要同进程链接两个内核，且是引擎的验收门，见 `repo-inventory.md` §3）。
- **⑤ 验收**：`pkg/backtest` 里 `job|batch|walkforward` 零命中；同一 batch 在编排服务与旧路径结果一致；provenance 端点返回三 hash。
- **⑥ 边界**：只做编排与结果归属。

### QT-45 · X6 交易通道网关（XTPTrader / MockTrader 两实现）[P2] ⬜

- **① 目标**：交易通道收敛为 port 两实现，归一化 DTO 进 contracts；通道级校验与风控校验分界清晰。
- **② 上下文**：`broker-gateway-design.md`（D23）；`xtp.go:387` 通道校验与风控校验混用需拆。
- **③ 要求**：① `BrokerPort` + 两实现；② 归一化 DTO 进 contracts；③ 拆开通道规则校验与 RiskPolicy 裁决；④ paper/实盘共用同一调用面。
- **④ 约束与非目标**：延迟敏感项保持进程内；不接新柜台；不改实盘下单语义。
- **⑤ 验收**：MockTrader 全链路测试；通道拒绝与风控拒绝的错误码可区分；DTO 有契约测试。
- **⑥ 边界**：只做交易通道；数据源 connector 在 QT-40。

---

## 六、横切

### QT-50 · 装配外移（`pkg/kernel` 只留框架）+ agent ↔ core HTTP 粗粒度 [P1] ⬜

- **① 目标**：框架包零业务依赖；agent 与 core 的边界只剩"粗粒度 HTTP + contracts"。
- **② 上下文**：ADR-D5/D24；`pkg/kernel/modules_analysis.go:43` import `pkg/backtest`（实测仍在）；D24 允许的粗粒度动作清单（提交回测/批量/因子求值/行情查询/策略定义拉取/因子登记），禁止 per-bar/per-symbol/per-order 远程调用。
- **③ 要求**：① 装配代码移到 **`cmd/*/wiring`**（**落点定死，不再二选一**——装配属应用层不属库，不放 `pkg/wiring`；refactor-plan-review L6），kernel 只留 Module/BootOrder/生命周期/槽位；② 定义 agent 侧 HTTP 客户端（六类粗粒度动作；第 7 类「日历/上市窗口查询」待 QT-17 ③-③ 用户确认后并入）；③ **wire 契约落 `docs/openapi.yaml`**：每类动作的 endpoint + DTO 写入 `docs/openapi.yaml`，纳入既有「生产路由表与 yaml 双向对拍」机制（D7 单一真相源规则，`protocol-and-data-boundary.md`）；`contracts/dto` 由 `oapi-codegen` 生成、**禁手改**（CI 重新生成后 diff 必须为空）；④ **鉴权模型**：复用既有 JWT + RBAC（敏感操作走 `RequireRole`），CORS 沿白名单；本地开发 insecure 模式 **loopback-only 且 fail-closed**（非环回一律拒）；⑤ 错误码与限流语义随 endpoint 成文（进 openapi.yaml 描述面）；⑥ 护栏：kernel 禁止 import 业务族；agent 禁止 import core 实现包、禁止逐 bar 远程调用（AST）。
- **④ 约束与非目标**：不拆进程（仍在同一可执行体内可选装配）；不改 BootOrder 语义；**不绕过 `docs/openapi.yaml` 手写 dto**（生成物禁手改）；不为图省事把七类动作拆成 per-bar/per-symbol 细粒度端点。
- **⑤ 验收**：kernel 的非测试 import 里零业务族；搬运前后 `package_wiring_test.go`/`module_lifecycle_callback_test.go` 各跑一次全绿；agent 侧 grep 无 per-bar 远程调用；`docs/openapi.yaml` 含全部新端点且**路由表对拍绿**；`contracts/dto` 与生成物一致（重生成 diff 为空）；**fail-closed 用例**：无 token / 越权角色 / 非白名单来源 / 非环回 insecure 请求全部被拒且报错可检索。
- **⑥ 边界**：只做装配与边界；module 拆分是 QT-05。

### QT-51 · 回测/实盘执行语义分离（BE-02）[P1] ⬜

- **① 目标**：改实盘撮合不会悄悄改变回测口径；两者共享的是**契约**，不是实现。
- **② 上下文**：BE-02；`engine.go`、`execution/live_bridge.go`、`options.go` 三处 import `pkg/live`；BE-02 一直只有"待切 port"的意向，无设计。
- **③ 要求**：① 定义 `ExecutionPort`（下单/撮合/成交回报）；② 回测实现与实盘实现各自独立；③ 若保留"同构桥"为有意设计，必须写明并在测试里锁住"改 live 不改回测结果"。
- **④ 约束与非目标**：不动 Order/Trade 值对象；不改 T+1 簿记；不引入 MQ。
- **⑤ 验收**：`pkg/backtest` 对 `pkg/live` 的 import 为 0（或仅剩接口适配层且有护栏）；同一策略回测结果在改动 live 前后逐位一致。
- **⑥ 边界**：只做执行语义边界；通道网关是 QT-45。

### QT-52 · 市场摩擦事实核实（BE-11：停牌/成交量/复权/分红/涨跌停/集合竞价）[P1] ⬜

- **① 目标**：把"逐位一致"的基准本身先核实清楚——六类摩擦到底是"已完备"还是"只触及"。
- **② 上下文**：BE-11 一直是"待查"（只做了文件命中统计：停牌 6 / 成交量 6 / 复权 5 / 分红送股 5 / 涨跌停 2）；BE-07 的 `auction` 仍在未接线白名单。
- **③ 要求**：① 逐类读实现并给出"完备/部分/缺失"结论 + 证据行号；② 对"部分/缺失"项评估对回测结论的方向性影响（高估/低估）；③ 涨跌停只有 2 处是否覆盖完整要专门回答；④ `auction` 接线方案（与 `ExecutionService` 的关系）单列建议。
- **④ 约束与非目标**：本单**只核实与结论**，不改代码；不顺手补实现（按结论另立单）。
- **⑤ 验收**：六类各一条结论 + 证据；`docs/design/qt-decoupling/backtest-engine-review.md` 的 BE-11 行被结论替换；对"缺失"项给出优先级建议。
- **⑥ 边界**：只做事实核实；实现另立。

---

### QT-61 · CI 基线归零（改名后暴露的 4 项预存在失败）[P1] ✅

- **① 目标**：让四仓 CI 从"长期红"变成"红即代表新问题"。现状是红太久，真回归会被当成噪声忽略——这次改名的两个真回归（gofmt 121 文件、归档 5 条坏链）就是混在这堆红里才没被及时发现。
- **② 上下文**：`tellurion` 首次 CI（run 39418563489）四项失败，逐项取证如下；**均非改名引入**：
  | 失败项 | 证据 |
  |---|---|
  | NT sidecar tests / Dual-run gate | `catalog path .../nt-sidecar/catalog does not exist`；旧仓 2026-10-16 同一行报错（catalog 是生成物，未入库） |
  | agent `TestStrategyPromptIncludesDSLSyntax` / `TestFactorDSLSyntax_DerivesFromRegistry` | 旧 agent 仓同测试同报错；`turnover_rate` 在 `pkg/expression/registry.go:222` **确实注册**（DependsOn TableMarketActivity），而测试把它列为"幻影字段" → 注册表与护栏测试漂移（OBS-08 未闭环） |
  | ui `SchedulePanel.test.ts` 1 failed（4910ms） | 旧 ui 仓 CI 亦 failure；该用例 4.9s 接近超时，疑似 flaky |
  | core `TestResolveStockPool_UniverseCsi300Expands` / `_FailsLoudWhenConstituentsEmpty` | 需要真库里有 csi300 成分（2024-10-31 ~ 2026-09-30 快照）；CI 只灌 `seed_alignment_fixture.sql`。旧仓该步骤被 gofmt 先拦住，**从未跑到** |
- **③ 要求**：① NT：把 `nt-sidecar/catalog` 的生成纳入 CI（`export_bars.py` + 固定 symbols），或把依赖 catalog 的用例改成显式 skip 并在报告里点名"catalog 未生成"（**不许静默 skip**）；② OBS-08：先判定 `turnover_rate` 是否真能被 DSL 求值（注册表说能、护栏测试说不能，二者必须有一方错），再决定修注册表还是修测试；③ ui：给 `SchedulePanel` 用例加稳定等待或提高 timeout，连跑 20 次不许失败；④ core universe：CI 增加成分数据 seed（或该用例改 `TEST_UNIVERSE=1` 显式开关，默认 skip 且在 CI 汇总里显示"未覆盖"）。
- **④ 约束与非目标**：**不许用 `continue-on-error` 掩盖**；不许删测试求绿；不改业务语义；不引第三方 CI 服务。
- **⑤ 验收**：四仓 `main` 的 CI 全绿，或剩余失败**全部**是 `known-fail` 白名单里带单号+期限的项；CI 汇总里能看到"跳过了哪些用例、为什么"。
- **⑥ 边界**：只做测试与 CI 配置；`turnover_rate` 若确认是业务缺陷，另立单进 W2。

> **✅ 完成记录（2026-10-21，本地验证；远端 CI 全绿待 push 后跑一次确认）**：四项逐一取证后修复，均不违反 §④（无 `continue-on-error`、未删测试、未改业务语义）：
> 1. **NT catalog（`python` 任务）** — 根因：`nt-sidecar/tests/test_k15r1_fees.py` 的 `FillReportFeeRegressionTest` 端到端腿读 `nt-sidecar/catalog`（`export_bars.py` 生成物、未入库），而 `python` 任务刻意无 PG、生成不了。改成缺目录时 `self.skipTest` 显式点名「catalog 未生成」；破坏验证证守卫是**条件性**的（临时建 `catalog/` 即越过守卫走到 `run_backtest`，本机转而报缺 pandas）。CI `python` 任务加 notice 步点名跳过项。
>    - **订正我先前的推断（首跑 CI 打脸）**：我原写「`align-gate` 本就自己 `export catalog`，那腿照常执行不失牙」——**不成立**。首跑 CI（run 39612286946）`align-gate` 仍红，卡在 `Export fixture catalog` 步：`tools/ci/seed_alignment_fixture.sql` 把 `volume` 灌成 `2000000 + 400000*sin(...)` 的**小数值**，而 NT 翻译层 `mapping.as_quantity` 对非整数 volume 按设计 fail-loud。这正是当初 `catalog does not exist` 的**上游真根因**（export 崩→catalog 从没建出来）。修夹具：`volume`/`turnover` 取整（真实日线 volume 本就是整数股，migration 052 对齐）——让合成数据与真实口径一致，不动那条 fail-loud 判定。此红之前被更早的 gofmt/universe 红掩盖、从未跑到，恰是 QT-61 立项要挖的「混在噪声里的真回归」。
> 2. **OBS-08（agent，闭环）** — 判定 `turnover_rate` **确可被 DSL 求值**：注册于 `pkg/expression/registry.go:222`（Source=activity、DependsOn=market_activity），有物理表 `market_activity`（Migration 056）、`domain.MarketActivity`、SPEC §960 目标公式 `turnover_rate / ts_mean(turnover_rate, 20) - 1`。§1029 的「现 0 行」是**可用性**维度、不影响 `factorDSLSyntax(nil)` 的**能力层全集**语义。⇒ **修测试不改注册表**：把 `turnover_rate` 从两项护栏的幻影清单移除。根因是清单在两个测试文件各抄一份、D5 加注册项后没同步删 → 收敛为**单一来源** `dslPhantomFields` + `phantomViolations` 的「过时自检」（喂已注册字段当幻影会点名「清单过时」而非恒红），加 `TestPhantomGuard_DetectsStaleRegistry` 做三腿破坏验证。本地复现红→绿。
> 3. **ui SchedulePanel flaky** — 本机无法复现（20× 连跑 400 条 0 失败、最慢单条 364ms，整文件 ~4s）；那次 4910ms 是 CI runner 争用把某条 naive-UI 挂载用例拖近 5000ms 默认墙。给本文件 4 个 `describe` 传 `{ timeout: 15_000 }` 加时间余量（**不放宽任何断言**）；破坏验证：预算调到 1ms→`Test timed out in 1ms`，证明该 timeout 真生效非惰性。
> 4. **core universe** — `TestResolveStockPool_UniverseCsi300Expands` / `_FailsLoudWhenConstituentsEmpty` 用硬编码 DSN 默认值连 CI 空 postgres service（表不存在→`require.NoError` 直接红，其余 DB 测试走 `TEST_DATABASE_DSN` 早 skip、独它连不上）。改成 `TEST_UNIVERSE=1` 显式 opt-in，默认在 `universeDSN` 顶部 `t.Skip`（非静默：CI `go` 任务加 notice 步点名「未覆盖面」）。本地验证默认 SKIP、`TEST_UNIVERSE=1` 时越过守卫走到 DB 连接。
>
> 合入本项后，QT-61 的「gofmt + doc-links」与本四项一起满足 QT-06 的「相关 job 绿 + 其余显式 skip 且在汇总点名」验收；⓪' 门禁解除。

---

## 七、存量任务（第 3 代延续，ID 不变）

> 前提已于 2026-10-20 复核：DBT-13（回落仍在 `eastmoney_sectors_adapter.go:725`、juchao 两处调用仍在 `:238/:241`）、DBT-14（schema 仍 `date-time`、实现仍只接 `2006-01-02`）、DBT-17（`capital_flow` 仍无读方）**均成立**。DBT-15 需真库读数、DBT-16 需外部产出方、DBT-18 跨仓——六字段**原样迁入**如下（正文不改写）。

### DBT-13 · `parseEastmoneyDate` 解析失败静默回落「今天」[P1] ✅（2026-10-21）

- **① 目标**：eastmoney 族任何一行数据，落库日期要么是真值、要么当场失败——**不存在"读不懂就当今天"** 这条路径。
- **② 上下文**（DBT-10 真源腿实测，2026-10-19）：`pkg/data/source/eastmoney_sectors_adapter.go` 的 `parseEastmoneyDate` 末尾 `return time.Now().UTC()`。它原先只认三种纯日期形状（其中 `"2006-01-02"` 还重复列了一次），而 datacenter 的 `RPT_DAILYBILLBOARD_DETAILS` 实际返回 **带时刻**的 `"2026-09-24 00:00:00"` ⇒ 龙虎榜**每一行**都掉进那个回落、落库日期全变成"今天"。DBT-10 已补齐带时刻两种形状并在 top_list 路径上加了「TRADE_DATE 缺失/空串当场失败」（`:506` 附近），但**回落本身仍在**，因为还有两个调用方共用它：`pkg/data/source/juchao_adapter.go:238`（传的是 `adjunctUrl`，注释自陈 placeholder）与 `:241`。把回落改成零值会连带改变巨潮那条链的行为——**不在 DBT-10 的文件边界里，故另立本单**。
- **③ 要求**：① 给"读不懂"一个显式出口（推荐 `parseEastmoneyDateStrict(v) (time.Time, bool)`，非严格版保持现行为不动，逐调用方迁移）；② 迁移 `juchao_adapter.go:238/241` 两处，逐处写明"这里读不懂时应该发生什么"（拒收该行 / 用别的字段兜），不许再用"今天"兜；③ 全仓 grep 其余 `time.Now()` 作为**解析兜底**的同类形状，登记或一并收口；④ 测试：不可解析输入 → 严格版返回 `ok=false`，且真库落库日期等于源给的日期而非当天（用固定历史日期的夹具，别用"今天"）。
- **④ 约束与非目标**：不改各 adapter 的业务语义与字段映射；不动 `ohlcv_daily_qfq` 存量数据；不顺手把 eastmoney 族的 filter/pagination 重构（DBT-10 已收口的部分不许回退）；不改 `internal/repoguard` 白名单形状。
- **⑤ 验收**：`go test ./pkg/data/source/ -count=1` 绿；有一条断言能区分「窗口外日期」与「日期读不懂」两种失败（报文不同、测试各自钉住）；**破坏验证**：把回落改回 `time.Now()` ⇒ 至少一条测试红 ⇒ 还原（判据：文件 sha256 与改前相同）。
- **⑥ 边界**：只做"日期解析失败的处理"这一件事；字段口径、单位换算另单。

> **✅ 完成记录（2026-10-21，本地验证；`-race` 与真库两腿缺，见 ⑧）**
>
> **① 前提复核**：成立（回落本体 `eastmoney_sectors_adapter.go:725`、juchao 两处调用在 `:238/:241`）。
> 但顺带订正 **DBT-10 的登记里两处不实**（`top_list_normalizer.go` 文件头，已就地改写）：
> ① 「同一函数也服务 `limit_up_pool`」——grep 实测 `parseEastmoneyDate` 的调用方只有 `fetchTopList` 与 juchao 两处，
> `fetchLimitUpPool` 是直接 `tradeDate := time.Now().UTC()`（`:596`），根本不经过这个函数；
> ② 引用的 `:661-679` 早已漂到 `:707-726`。改为**按符号名引用**，不再写行号（行号就是这类漂移的来源）。
>
> **② 实测比登记更严重的一环（决定修法，不是扩范围）**：juchao 那条链不是「改成零值会连带改变那条链的行为」这么轻。
> 2026-10-21 向真源发一条 `POST /new/hisAnnouncement/query`（600519，0 次凭据）取证：
> `"announcementTime": 1786723200000` —— 是 epoch **毫秒**（= 北京时间的 2026-08-15 午夜），而 `adjunctUrl`
> `finalpage/2026-08-15/1225475868.PDF` 的日期段与之一致。13 位数字进不了「8 位才算 `YYYYMMDD`」那条腿 ⇒
> **每一条公告的日期都等于跑作业那天**。改前把新测试跑在旧代码上实测：三行夹具全部落 `2026-10-21 14:09:28`。
>
> **③ 落地**：
> - `parseEastmoneyDate` → **`parseEastmoneyDateStrict(v) (time.Time, bool)`**，认不出 ⇒ 零值 + `ok=false`；
>   布局表里 `"2006-01-02"` / `"2006-01-02 15:04:05"` 各被 DBT-10 重复列过一次，去重（可接受形状集不变）。
>   工单原写「非严格版保持现行为不动」——三个调用点全部迁完后 lenient 版零调用方，**不留兼容壳**（直接删）。
> - `fetchTopList`：缺失（`has no TRADE_DATE`）与读不懂（`is not a date this parser can read`）分别当场失败，
>   与 DBT-10 的窗口闸（`outside the requested window`）三种报文互不相同。
> - `juchao_adapter.go`：删掉自陈 placeholder 的那行，改 `parseAnnouncementTime` 两条腿 ——
>   ① `announcementTime`（epoch 毫秒，折成**交易所日历日**落 UTC 零点：直接 `.UTC()` 会退一天，
>   且落库列是 DATE，折成 UTC 零点后结果与跑作业的机器时区无关，与同族 `2026-09-24 00:00:00` 的口径一致）
>   ② `adjunctUrl` 日期段兜底（工单 ③-② 明写的「用别的字段兜」）；两条都读不懂 ⇒ **拒收该行**。
>   另加一道**量级自检**：折出的年份不在 2000~2100 内当读不懂 —— 源改口径给成秒/微秒时下场是「一行被拒收」，
>   不是「一行 1970 年的公告」。
>
> **④ 测试**：`dbt13_adapter_date_fidelity_test.go`（只走现有入口 `Fetch` + httptest 替身，所以同一份断言能跑在改前改后）
> 与 `dbt13_strict_parser_test.go`（解析层出口，含「永不返回今天」的总闸）。
> 改前 **3 条真红**（`RefusesTradeDateItCannotRead` / `AnnouncementDateComesFromSourceNotToday` /
> `RejectsWrongEpochScaleInsteadOfInventing1970`）；改后 `go test ./pkg/data/source/ -count=1` **EXIT=0**、
> `go vet ./pkg/data/source/` **EXIT=0**、`gofmt -l` 空；消费方 `go test ./pkg/data/... ./cmd/data/ -count=1` **EXIT=0**。
>
> **⑤ 破坏验证四腿**（基线 `sha256sum` → 改坏 → 抓真退出码 → 还原 → `sha256sum -c` 两文件 OK）：
> 1. 回落改回 `return time.Now().UTC(), true` ⇒ **5 条红，EXIT=1** ✅（工单 ⑤ 点名的那条）
> 2. `FixedZone("CST",8h)` 改成 `.UTC()` ⇒ 3 条红 ✅（交易所日历日这条钉子有牙）
> 3. 摘掉 `fetchTopList` 的 `!readable` 拦差 ⇒ **第一遍绿（EXIT=0）** ⚠️ 抓到我自己测试的牙口不足：
>    下游 `checkTopListWindow` 的零值兜底代劳报了文面相近的错。补强为「报文必须点名
>    `is not a date this parser can read`」+ 三种失败报文两两不同，重跑该腿 **2 条红** ✅。
>    —— §8.5.3-2「假护栏比没护栏更糟」在这里兑现了一次，不补这条，`⑤ 验收`里「报文不同、各自钉住」是空的。
> 4. 摘掉 juchao 的 `adjunctUrl` 兜底腿 ⇒ 2 条红 ✅（兜底腿不是主腿，但也确有断言指着它）
>
> **⑥ ③-③ 的登记（全仓同类形状）**：多行 grep `time.Parse…return time.Now()` 全仓**只有 `:725` 一处**，本单已收口。
> `pkg/clock/interfaces.go:124`、`pkg/live/margin/margin_account.go:1016`、`cmd/analysis/handlers_compliance.go:85`
> 是墙钟提供者，不是解析兜底。**留在原地的两处快照语义（如实登记，非缺陷、不在本单收口）**：
> `fetchSectorList:134`（给了 `req.StartDate` 就用它）与 `fetchLimitUpPool:596`（直接墙上时钟）——
> 这两个端点是**当日快照**、源不给逐行日期，所以「日期 = 跑作业那天」是语义设定；读这两张表要先知道这一点。
>
> **⑦ 文档同步**（变更触发按 `docs/README.md` §3）：`docs/SPEC.md` 的 `top_list` 行「回落本体仍在」已改为收口口径；
> `AGENTS.md` §14 该行改判（划掉标题 + 写「已修」+ 保留两处快照语义作遗留口径）；
> `top_list_normalizer.go` 文件头与 `TestTopListNormalizer_CannotCatchUpstreamBadDate` 的「洞在上游」叙述按实订正
> —— 那道闸从此钉的是**责任归属**（日期真伪归源侧解析器），不是「洞」。
>
> **⑧ 缺的腿（不假装做过）**：① `-race` 本机不可跑（无 gcc/cgo；CI ubuntu-latest 有，见 `.github/workflows/ci.yml:94`）
> —— 新增测试各自建 `httptest.Server` 与独立 adapter 实例，无跨用例共享可变状态，但这条**没有本地执行证据**；
> ② ③-④ 的「真库落库日期」用**适配器边界的夹具断言代替**（源给的日期原样交到 `DataItem.TradeTime`），
> 落库那一段是 `bulk_insert.normalizeDate` 的既有职责，本单未起 DB；下次全仓套件时顺手确认即可。

### DBT-14 · EquityDeep snapshot 契约漂移：`fetched_at` 声明 date-time、实现只收日历日 [P1] ⬜

- **① 目标**：契约（`snapshot.schema.json`）与实现（`pkg/data/equitydeep`）对 `fetched_at` 的形状**只有说法一种**，且该说法被测试钉住。
- **② 上下文**（DBT-11 实测逮到，2026-10-19，已用测试钉住现状未改任何一边）：`contracts/snapshot.schema.json` 把 `fetched_at` 声明为 `format: date-time`，而实现只收 `YYYY-MM-DD`（`pkg/data/equitydeep/equitydeep.go:102`）——**严格按契约发 date-time，整条请求 400**。钉子是 `pkg/data/equitydeep` 里新增的 `TestNormalize_ContractDateLayoutIsCalendarDateOnly`。同处另一条较弱的事实：`DecodeSnapshot` 不校验 schema 的 `required` 列表，靠 `Normalize` 兜住（也已钉）。
- **③ 要求**：① **先由主任裁决改哪一边**（改实现以吃 date-time / 改契约声明成 `format: date` / 两者都吃但落库归一）；② 按裁决落代码 + 更新契约文件或实现；③ 同步 SPEC 的 citation/snapshot 形状节（`docs/SPEC.md` 的 EquityDeep 摄入契约处）；④ 若选"两者都吃"，必须明确 PIT 语义不受影响：`ann_date` 才是 PIT 锚点，`fetched_at` 只是同键多版本的排序位。
- **④ 约束与非目标**：不改 `ann_date` 的 PIT 语义（`GetFundamentalsDetailAsOf` 强制 `ann_date<=$1` 是本表存在的理由）；不动 `field_dictionary.yaml` 的白名单内容（raw_names 变更属兼容性决策，文件头已注明需与 `evals/data_quality/spot_check.py` 同步）；不改 `pkg/domain` 字段（`pkg/domain/spec_drift_test.go:190` 会双向钉 SPEC 锚点）。
- **⑤ 验收**：契约与实现各有一条断言且互不矛盾（同一形状在两侧结论一致）；`go test ./pkg/data/equitydeep/ ./cmd/data/ -count=1` 绿；**破坏验证**：按裁决后不该接受的形状发一条请求 ⇒ 必须被拒且原因可检索。
- **⑥ 边界**：只做 `fetched_at` 这一处形状漂移；其余 schema/实现不一致的发现列进本单正文附列，不顺手改。

### DBT-15 · `ingest.raw` 里 155 行历史探针假归档的处置 [P2] ⬜

- **① 目标**：证据表 `ingest.raw` 里没有**明知是假**的归档行；或它们的存在被成文承认并解释为何无害。
- **② 上下文**：`cmd/data/handlers_ingest_test.go` 的归档测试每跑一次往真库落一条探针假数据（payload 含 `probe` 键、`source=akshare`、`dataset=akshare.stock_financial_abstract`，与真表用过的名字相同），历史上从不删——库里已积 **155 行**（`select source, count(*) … where payload ? 'probe'` 实测：akshare 155/155）。另有 2 行 `source=unit-test*`。该测试的**自清理已于 2026-10-19 修好**（`e23ddec`，按本 run 的 content_hash 精确删 + 读回复核），但**既有 residue 不是我今天写的，删别人写进共享库的行需要单独授权**，故只登记不擅动。DBT-11 那次「`fina_indicator` 到底返回哪些字段」是**纯读这张表**做的取证——脏读数风险的形状就是这样一个具体例子。
- **③ 要求**：① 由主任决定：删除 / 保留并成文标注为演示 residue；② 若删，语句已备好，按可识别锚点精确删，不用全表 DELETE：`DELETE FROM ingest.raw WHERE payload ? 'probe' AND source='akshare';` 与 `DELETE FROM ingest.raw WHERE source LIKE 'unit-test%';`；③ 删前后各读一次 `count(*)`（预期 12,108 → 11,951）并留证；④ 把处置结论写进 SPEC 的 `ingest.raw` 节（A 类原始源响应，唯一归属）。
- **④ 约束与非目标**：**绝不做** `TRUNCATE` 或无条件 DELETE；不碰 `tushare.*` 那 11,932 行真归档（它们是证据 API 的返回源，且被 DBT-11 的取证读过）；不改测试去"绕过"这个问题（测试侧已修）。
- **⑤ 验收**：处置结论成文（SPEC 一句话）+ 若删则前后读数与 `count(*) filter (where payload ? 'probe')=0` 的复核。
- **⑥ 边界**：只处理 `ingest.raw` 的 residue；其他表的测试残留若顺带发现，登记不处理。

### DBT-16 · EquityDeep snapshot 的**生产者**（`fundamentals_detail` 唯一可行灌数路径）[P2] ⬜

- **① 目标**：`fundamentals_detail` 有真实数据来源，或明确成文「在本仓可及的源之内不可得，需要谁产出什么」。
- **② 上下文**（DBT-11 实测，2026-10-19）：门与读方都验证过了（真 HTTP 门 → 真库 → PIT 读回，`0 → 5 → 0` 行无残留，`1764598`）。缺的是**没人往门里投 ndjson**：`DecodeSnapshot` 的生产唯一调用点在 `cmd/data/handlers_equitydeep_ingest.go:116`，等的是外部 EquityDeep 产出。而 **Tushare 免费权限给不出所需字段**——纯读库取证（0 次 API 调用）：`tushare.fina_indicator` 全仓 25 条 raw 的 `fields` 只有一种形状 `[ts_code, ann_date, end_date, roe, roa, gross_margin]`，与因子需要的 `total_revenue / operating_cost / contract_liability / ocf_net / net_profit_attr / total_assets / equity_attr / inventory`（`pkg/data/equitydeep/equitydeep.go:41-49`）一个都不对得上，这 6 个名字经字典查找全部 miss。⚠️ **本单不许用编造数据灌数**：那五个因子会直接读到假财务数值。
- **③ 要求**：① 先定产出方——EquityDeep 侧（vault 研究产出的投影）还是别的可及源；② 若要 core 提供**投递工具**（把一份已合法的 snapshot 文件推到门上，含 `POST /api/ingest/raw` 先归档再引用 content_hash 的两步顺序），实现放 `cmd/data` 子命令，形状照 `mining` 子命令（`cmd/data/mining_cmd.go`，DBT-04 的先例）；③ 数据到位后跑一次真灌数并留前后读数；④ 拿不到就成文「不可得 + 需要谁产出什么格式」，并把那五个因子的降级语义（`ErrNoFundamentalsDetail`，`pkg/data/equitydeep/equitydeep.go:67`）在 SPEC 与 AGENTS §14 说清。
- **④ 约束与非目标**：不改字典白名单内容去"迁就"某个源；不改因子数学定义（D3 冻结）；不在 core 侧实现 EquityDeep 研究逻辑（ADR-023：EquityDeep 是**数据底座**，不是第二个 agent）；不新增外部依赖不报批。
- **⑤ 验收**：要么 `fundamentals_detail` 有真数据且有读方能读到（前后行数 + 一条 PIT 读回留证），要么「不可得」成文且 SPEC/AGENTS 两处口径一致。
- **⑥ 边界**：只做"数据从哪来"；`capital_flow`/`top_list` 的读方是 DBT-17。

### DBT-17 · `capital_flow` / `top_list` 零读方——接上还是明确为数据储备 [P2] ⬜

- **① 目标**：两张今天已有真数据的表，有一个**说得出口的用途**：要么被读（因子/API/前端任一处），要么成文声明为"仅储备、暂无读方"并解释为何值得占配额。
- **② 上下文**：DBT-09/10 之后 `capital_flow` 18 行、`top_list` 256 行（真库实测，`1a1bb38`）。接线期间的 grep 结论是 **`capital_flow` 无任何读方**（无 handler / 无因子 / 无前端读它），这也是 DBT-09 决定**不**把它放进 cron 名单的理由——"每天烧一整桶免费配额喂一张没人读的表"。`top_list` 则**进了** cron 名单（整市场按日翻页，1–2 发请求，日更成本可忽略）。另需注意 DBT-09 的口径代价已登记：表内数值现在是 **Tushare 分档成交额**形态，`main_net`/`main_buy_amount`/`main_sell_amount`/两个 ratio/`retail_*`/`close_price`/`change_pct` 本源不给 ⇒ 落 NULL，**两族口径混在同一张表里靠 `source` 列区分**，任何读方按东财含义解释这些列之前必须先读 `source`。
- **③ 要求**：① 决定用途（候选：作为横截面因子进 `pkg/data/factor*.go` / 只进 API 读端点给人看 / 明确"储备"并停止日更讨论）；② 若接因子，走 D3 冻结的定义流程 + `factor_cache` 归属（Redis，C 类派生计算）；③ 无论哪种，SPEC 的数据缺口表要把两表的「行数/源/口径/读方现状」写全，NULL 列的含义写清（缺失≠0）。
- **④ 约束与非目标**：不为"有数据"而造读方（读方必须服务某个研究问题）；不改四契约签名（ADR-031）；不接新源（DBT-16/§14 的 D5 样板另议）。
- **⑤ 验收**：结论成文（SPEC 表格 + 一条 ODR 或台账段说明裁决理由）；若接了读方则有该读方的测试与一次真实读数；若判"储备"则 `scheduler.RegisterJobType` 与 cron 名单的状态与结论一致。
- **⑥ 边界**：只做"谁来读/要不要日更"；数据获取本身（DBT-09/10）已收口。

### DBT-18 · mining 的真实 Proposer（LLM/MCTS 候选生成）[P3] ⬜（跨仓，本仓只留指针）

- **① 目标**：`cmd/data mining` 的候选来源从 JSON 文件升级为真实候选生成器，或明确该能力归属 `tellurion-agent` 仓（旧 `quant-trading-agent`）。
- **② 上下文**：DBT-04 落地时用的是 `fileProposer`（候选 JSON 文件 → `mining.Candidate`），完成记录里写明「真实 LLM/MCTS Proposer 仍属另立工单」，且其非目标已排除「不接 AI 挖掘消费方（D8 治理层之外）」。2026-10-09 AI 层整体迁往 `quant-trading-agent` 仓（今 `tellurion-agent`）（ADR-027 §5 第 8 步前置切片），`pkg/ai` + 四族 AI handler 现在住那边，依赖方向单向 agent→core（repoguard 钉住）。⇒ 本单**主体不在 core**。
- **③ 要求**：① 由主任裁定归属；② 若归 agent 仓，本仓只保留 `mining.Proposer` 接口作为冻结契约 + 在本文件「跨仓任务索引」加一条指针（不镜像正文）；③ 若要求 core 提供候选投递 API，则写清端点与鉴权（走 `route_policy` 现有策略）。
- **④ 约束与非目标**：core 不引入 LLM SDK；不在本仓实现第二个编排者（ADR-023 禁）；不改 `Pipeline` 签名。
- **⑤ 验收**：归属结论成文 + 指针登记；跨仓那半在其归属仓台账里写全六字段。
- **⑥ 边界**：只做归属与契约指针；实现细节属 agent 仓。

> **2026-10-21 自 `tellurion-ui:docs/TASKS.md` 迁入**（该单原标注「待迁 core」，归属 core 的 `cmd/data` + `pkg/storage`；六字段原样迁入、正文不改写，ui 仓只留指针）。

### DATA-04 · 空列表编码成 `null` 而不是 `[]`（🟠 契约说谎，任何客户端都会踩）⬜ 待做

> ⚠️ **编号提醒（2026-10-21）**：`docs/archive/TASKS-2026-10-16-archived.md:553` 另有旧 DATA-04（成交量单位统一，✅ 2026-10-14），同号不同单；消歧换号见 SPEC-R-03。

- **① 目标**：集合型端点在**结果为空**时返回 `[]`（或 SPEC 明写可为 `null` 并让所有消费方按可空处理），二选一，不能一边声明数组一边给 `null`。
- **② 上下文**（2026-10-14 curl 当场实测，不是读码推测）：
  - `GET :8085/api/sync/jobs?status=pending` → `{"count":0,"jobs":null}`。
  - 根因形状：Go 侧 `Jobs []*SyncJob` 为 nil 时 `encoding/json` 编出 `null`，`[]` 需要显式初始化；`docs/openapi.yaml` 与前端类型 `web/src/types/sync.ts:60`（`jobs: SyncJob[]`）都按**必有数组**写。
  - 前端已止血（本轮 A-08：`stores/sync.ts` 的 `resp.jobs ?? []`，并有单测钉住真形状），但**止血不等于契约对了**：下一个客户端（agent 的 MCP 工具、curl 脚本、别的 SPA 页面）照类型写就会踩。本轮实测里同一个形状在 `schedules` 那条上也有对应处理（`resp.schedules ?? []`），说明这不是孤例而是**一族**。
  - **第三个实测实例（2026-10-16，core DATA-11 自审时顺带查出，已登记为 core `DATA-13`，✅ 2026-10-16，`docs/archive/TASKS-2026-10-16-archived.md:788`）**：`GET :8085/api/ohlcv/600000.SH?start_date=2024-01-01&end_date=2024-12-31` ⇒ **200 `{"ohlcv":null}`**，同参换 `20240101/20241231` ⇒ 200 带真行。成因不是「集合端点忘了初始化」而是**日期格式被吞**：`pkg/data/cache.go:60` 的 `parseDate` 写着 `t, _ := time.Parse("20060102", s)`，注释自承「Returns zero time on error」⇒ ISO 写法塌成零值日期、查询范围 `[0001-01-01, 0001-01-01]`、结果 nil slice 编成 `null`。分工提醒，别各自收一半：**本单管形状**（空集合该是 `[]` 还是明写可空），**core DATA-13 管成因**（坏格式该 400 而不是装成没数据）；同一台服务上 `cmd/data/handlers_factor.go:223-229` 对同样的 `YYYYMMDD` 声明是**回 400** 的，可见两种做法已经并存。
- **③ 要求**：把该族端点（至少 `GET /api/sync/jobs`、`GET /api/sync/schedules`）在空结果时序列化成 `[]`；或反过来在 SPEC/openapi 里把字段标成可空并同步所有已知消费方。**形状与取舍由 core 定**，前端不指挥。
- **④ 约束与非目标**：不改前端已就位的 fail-closed（那是防御，不是本单的解）；不动 `ResultSpec`；不在本单里顺手改别的端点 —— 先盘点还有哪些集合端点会吐 `null`，把清单写进结案记录。
- **⑤ 验收**：
  - `curl` 拿一条**保证为空**的查询（例如 `?status=retrying`，实测库里 0 条）→ body 里该字段是 `[]`（或 SPEC 明写可空且消费方同步完）。
  - 加一条 Go 侧断言：空结果路径序列化后含 `":[]"` 而不是 `":null"`（照 `pkg/storage/bulk_insert_ddl_test.go` 那种静态读源码/编解码的零 DB 依赖做法）。
  - **破坏验证**：把 `?? []` 那类前端防御临时摘掉 ⇒ 队列面板那条 e2e（`a08-job-console.spec.ts` 的「空队列的真实回包走空态」）必须红，还原转绿 —— 证明那条 e2e 真在钉形状，不是常驻绿。
- **⑥ 边界**：只处理"空集合的编码形状"这一件事；`latest_date` 静默缺席与 PIT 通道是 DATA-03、票池拆分是 DATA-02（两单均已归档 `docs/archive/TASKS-2026-10-16-archived.md`）。

---

## 八、跨仓任务索引（不在本仓，勿双写）

> 仓名以 `repo-inventory.md` §2 的「旧→新」为准；**QT-60 已于 2026-10-20 完成，实际仓名就是新名**（旧仓仍在，未归档）。
> - **`tellurion-agent` 仓**（旧 `quant-trading-agent`）：`OBS-01-R2-A` ⬜（2026-10-16 裁决 (b)，core 零改动；2026-10-19 复核仍 ⬜）；DBT-18 属这一族（`mining.Proposer` 契约指针在 core）；**refactor 修复传播单** ⬜（agent↔core HTTP wire 契约消费对齐 + C1 三值 `status`/`invalid_reasons` 消费口径——正文登记在该仓 `docs/TASKS.md`，本仓不镜像）。
> - **`tellurion-ui` 仓**（旧 `quant-trading-ui`）：`AUD-61` ⬜（2026-10-19 复核仍在册，该仓另有 4 处 ⬜）；`docs/design/stitch_quantlab_ux_redesign/TASKS.md` 的 `A-05` ✅、`A-08` ✅；**refactor 修复传播单** ⬜（三值 `status` 消费 + `invalid_reasons` 的 `data_gap_skipped`/`session_terminated` 展示口径，C1——正文在该仓 `docs/TASKS.md`）。
> - **`tellurion-contracts` 仓 / `tellurion-kit` 仓**：**仓已存在（2026-10-20 建，占位）**；**代码在 S1 直接落进这两仓**（ADR-D31，不用 replace），打 tag 见 QT-07；**随仓接口规格载体**（contracts `docs/API.md` / kit `docs/SCOPE.md`，M6）与**版本策略/兼容矩阵 + GOPRIVATE 前提**（L4/L5）见 QT-07 ③-S2-c / QT-19，正文立单在两仓自己的 `docs/TASKS.md`。
> - **`tellurion-agent` 仓**：**S3 前仍依赖本仓源码**（replace），不可单仓构建 —— 这是 `known-gap`，对应 QT-07 收尾段，不是遗漏。
> - **`tellurion-docs` 仓**（旧 `quant-trading-docs`）：**NT v2.0.0 正式版升级**（`docs/guides/nt-sidecar-operations.md`）——唯一外部等待项。⚠️ QT-02 的 `FillTiming` 一旦切换，NT 侧必须同步实现 `next_open`，否则 K15 会报 `ErrFillTimingMismatch`；升版时把这条加进验收门。**ADR-D 升格单** ⬜（M9：D24/D25/D29/D30 已裁决且直接驱动施工（QT-05/07/60），须按 `tellurion-docs:docs/adr/` 流程升格为正式 ADR；升格完成前设计集 README 的升格路径注记保留——正文立单在该仓 `docs/TASKS.md`）。

---

## 九、spec 审查遗留（2026-10-21）

> 来源：系统规格专项审查（`docs/design/system-spec-review-2026-10-21.md`）修复阶段立单——审查发现本身已全部修复或裁决落地，以下 3 条是修复中判定「需另行处置」的遗留。M10（10 份 api-schema 补「空集合编码语义」节）不单独立单，挂在现役 **DATA-04** 定形后统一补节。

### SPEC-R-01 · 3 处模块内自建表是否收编进 `migrate()`（DDL 真相口径收口）[P2] ⬜

- **① 目标**：裁定并落地 DDL 落点口径——把 `pkg/eventstore/schema.go`、`pkg/strategy/state_store.go`、`pkg/strategy/signal_store.go` 三处模块内自建表收编进 `pkg/storage/postgres.go` 的 `migrate()`，或成文认可「主路径 + 登记例外」为正式口径。
- **② 上下文**（spec 审查 M3）：`pkg/eventstore/schema.go:40`（audit schema + `audit.message_log`）、`pkg/strategy/state_store.go:54`（`quant.strategy_state`）、`pkg/strategy/signal_store.go:35`（重复建 `quant.external_signals`）均在 postgres.go 之外建表，「唯一 DDL 真相」声明不实；AGENTS.md 已按「DDL 主路径 + 例外登记」措辞修订（2026-10-21），但收编与否未裁。`signal_store.go` 的重复建表另有幂等隐患。
- **③ 要求**：① 裁定「收编」或「例外登记」；② 若收编，把三条建表语句并入 `migrate()`（`external_signals` 去重为一处）；③ 若例外登记，在 `docs/SPEC.md` 与 AGENTS.md 维持例外清单并保证三处建表幂等。**schema 变更评审项**：内联 DDL 落点合并（表结构字段零变更）；**需同步更新的 SPEC 节号**：`docs/SPEC.md` 存储/DDL 真相声明段（执行时按实定位回填）。执行顺序：合规测试红 → 更新 SPEC 文本 → 正式变更评审记录 → 合入。
- **④ 约束与非目标**：不改任何表结构字段；不改 BootOrder 与模块契约签名；去重 `external_signals` 不得改变其行为；`strategy_state` 归属已裁（strategy-runtime，H5 裁决）不重开。
- **⑤ 验收**：裁决成文（ODR 或本单结案记录）；若收编，`migrate()` 幂等测试覆盖三表，且 grep 证实 postgres.go 之外无生产态 `CREATE TABLE` 残留（测试文件除外）。
- **⑥ 边界**：只做 DDL 落点归一；表归属认领是 SPEC-R-02。

### SPEC-R-02 · 五张无主表的 owner 认领（L7 收尾）[P2] ⬜

- **① 目标**：`factor_hypothesis`、`factor_returns`、`ic_analysis`、`quant.external_signals`、`users` 五张表在各仓归属表中无认领行——逐张定唯一 owner 并补进归属表。
- **② 上下文**（spec 审查 L7，2026-10-21 实测）：全库 6 schema/53 常驻表中上述五表无主（`factor_mining_records` 已由 `tellurion-data:docs/db-schema.md` 补认领，L5 已修）；同源的「52 表/6 schema」口径不实已订正为 53 常驻表。
- **③ 要求**：按实际写者与 ADR-D19 归属原则逐表定 owner（候选：`factor_*`/`ic_analysis` 归数据因子域、`users` 属 ADR-017 鉴权域、`quant.external_signals` 随 signal_store 归属一并定，均须核实写者后拍板），补录 `tellurion:docs/design/qt-decoupling/project-engineering-file.md` 归属段与对应仓 `docs/db-schema.md`，并在对应仓 `docs/TASKS.md` 留双向指针指回本单。
- **④ 约束与非目标**：只做认领登记，不迁表、不改 schema；不重开 H5/M18/M21 已裁归属；DDL 收编归 SPEC-R-01。
- **⑤ 验收**：五表各有唯一 owner 行且与实际写者一致（DDL 以 `pkg/storage/postgres.go` 为准）；各仓归属表 grep 无「无主」残留；双向指针齐。
- **⑥ 边界**：只做归属认领；因子名注册表等协议面不涉及。

### SPEC-R-03 · 现役 DATA-04 与归档旧 DATA-04 同号歧义——换号处置 [P3] ⬜

- **① 目标**：消除「DATA-04」一号两指：现役 DATA-04（空集合 `null` vs `[]`，2026-10-21 自 `tellurion-ui` 迁入）与 `docs/archive/TASKS-2026-10-16-archived.md:553` 旧 DATA-04（成交量单位统一，✅ 2026-10-14）撞号。
- **② 上下文**（spec 审查 M1 修复时发现，已核实 archive:553）：引用只写「DATA-04」时无法区分两单；M10 联动注记与 `tellurion-ui:docs/TASKS.md` 迁移指针均指向现役单，需要唯一可引用的号。
- **③ 要求**：现役单换新号（顺延 **DATA-14**，归档最大号为 DATA-13），同步更新全部引用（本文件、`tellurion-ui:docs/TASKS.md` 迁移指针、spec 审查报告 M10/§8 行文）；归档旧单一字不改。
- **④ 约束与非目标**：`docs/archive/` 不动（历史不改写铁律）；只改号与引用，不改单的正文内容；空集合契约语义本身归现役单正文。
- **⑤ 验收**：现役活文档 grep「DATA-04」仅指向归档旧单（带 archive 路径），现役单以新号唯一可引用；迁移指针双向可追溯。
- **⑥ 边界**：只做编号消歧；不动其他单号。

---

## 维护本文件

- 新增 task 必须按 [AGENTS.md §8.5](../AGENTS.md) 写全六字段；**六字段里必须显式引用冻结契约的路径与节号**（本代 wave-0 的冻结文本在 `docs/design/qt-decoupling/p0-round2-review.md` §8、`p0-remediation-design.md`、`adr-029-amendment-host-api-v2.md`）。
- 完成即标 ✅（附日期）并移入 `archive/`；本文件只留未完成项。换代时整篇加时间戳归档（`archive/TASKS-YYYY-MM-DD-archived.md`），旧代正文不改写。
- **派工前必须先证伪工单前提**（读码核对 → 不符就停下回报 + 给拆法，不硬做、不扩范围）。
- **执行顺序（偏序，非全序；refactor-plan-review M4）**：硬链 **S-1(QT-60 ✅) → QT-61 → S0（QT-06 → QT-18 → QT-08）→ W0 → S1(QT-05)**；S2 段（QT-17 → QT-07 → QT-19 → QT-09）须在 W0 + QT-05 全绿后；**W1 不依赖 S2**——W0 后即可开工，且 **仅 QT-17 可与 W1 并行**（QT-07/09/19 不与 W1 抢档）；W2 在 W1 后、W3 在 W2 后。同波次内撞文件的按 QT 序号串行，一单一 commit。**W0 完成前不开 W1 的代码单**——否则 artifact 与基准都要重做。
- **冻结 schema 变更评审（硬规则；refactor-plan-review H1）**：凡触碰 `ResultSpec` / `RunSpec` / `SignalSpec` / `docs/SPEC.md` 冻结字段的单，六字段内必须列「**schema 变更评审项 + 需同步更新的 SPEC 节号**」，并按固定顺序执行：**先触发合规测试红 → 更新 SPEC 文本 → 走正式变更评审记录 → 才许合入**；未列评审项的单一律不得改冻结面（含「顺手加一列/加一字段」）。
- **合入门禁（硬约束，不是建议）**：⓪ ~~QT-60 未完成前 QT-06 不得开工~~ → **已满足（QT-60 于 2026-10-20 完成）**，tag 现在可以打在新仓；新增 ⓪' **QT-61 的「gofmt + doc-links」两项绿之前，QT-06 的「两仓 CI 绿」验收不可达**——要么先做 QT-61，要么把 QT-06 验收改成「本次改动相关的 job 绿 + 其余 known-fail 登记」；① **QT-18 未合入 main 前，QT-01 不得合入**——第一个 breaking 就在 QT-01 的 abi v2，护栏不在就会漏；② QT-05 未合入前，QT-07 不得开工；③ QT-07/QT-09 未绿前，不得在任何仓新增跨仓 `replace`（D29 §9 C1）。
- **新需求优先于存量未完成任务**；与旧任务重叠的可合并或直接删除，跨仓迁移的任务条目保留可追溯指针。
- 归档文件的状态标记若与自身完成记录矛盾，**只在文件头加订正说明**，条目正文与标题的历史措辞不改写。
