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
| **S-1** | QT-60 | **改名到 tellurion**（4 仓 + module path + 文档引用；旧仓 archive） | P0 | **排在一切之前**：tag 必须打在新仓；先改名再抽 module，同一批路径只改一次 |
| **S0** | QT-06 → **QT-18** → QT-08 | **分仓起步**：core 打 `v0.1.0` + **过渡期连体护栏** + 四仓自治三件套 | P0 / P1 | **QT-18 是 W0 的合入门禁**：未合入前 QT-01 不得进 main（第一个 breaking 就发生在 QT-01 的 abi v2）；QT-06/08 可与 W0 并行 |
| **W0** | QT-01 | abi v2 契约冻结（13 函数 + 内存区 + 枚举） | P0 | 一切 WASM 单的前置；artifact 出厂后改不动 |
| W0 | QT-02 | `FillTiming` + 两口径差异报告 | P0 | 决定全部历史基准怎么读；与 QT-01 无文件冲突，可并行 |
| W0 | QT-03 | 留痕 hash 体系（run_spec / dataset / artifact_hash） | P0 | 后面所有"可复现"验收都要引用它 |
| W0 | QT-04 | 重复信号存量扫描（定 trap / 合并） | P0 | 只读扫描，可与 01~03 并行；结论进 QT-21 |
| W0 | QT-05 | **S1 契约层 + 工具层抽取**（`contracts/` 含 domain + `kit/` 含 db，两个子 module） | P0 | 契约要有地方住；与 01 撞 `contracts/` → 01 先落常量，05 再搬；**不建新仓** |
| **S2/S3** | QT-17 / QT-07 / QT-19 / QT-09 | **拆仓**：agent 数据自持（`ai.*`）→ contracts/kit 成独立仓 → 契约版本策略 → 跨仓护栏 + 契约测试；agent 去 replace | P1 | **W0 + QT-05 全绿之后**；QT-17/QT-19 分别是 S3 删 replace 的数据前置与版本前置 |
| **W1** | QT-10 | 错误分级 / fail-closed 落地 | P0 | 与 QT-14 同批（EndRun 语义的前提） |
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
| **X 线** | QT-40~45 | 数据/策略/风控/分析/编排/网关六服务 | P1-P2 | 各自独立，X0（三 hash）已在 QT-03 |
| **横切** | QT-50~52 | 装配外移 / 执行 port / 市场摩擦核实 | P1 | 与 W1 可并行，撞 `pkg/backtest` 的按序号串行 |
| **延续** | DBT-13~18 | 第 3 代存量 6 项 | P1-P3 | 前提已复核；DBT-16 阻塞于外部产出方 |

**并行指引**：**QT-06/08 与 W0 并行**（06 只打 tag，08 只写各仓入口文件）；**但 QT-18 必须先于 W0 合入 main**——QT-01 就是 abi v2 换 13 个函数，那是第一个会打穿 agent 编译的改动，护栏不在就漏过去了。W0 的 01/02/04 文件域互不重叠（`contracts/` vs `pkg/backtest/engine*` vs 只读扫描）；05 会搬 `contracts/`，务必排在 01 之后。**07/09 必须等 05 与 W0 全绿**（拆的是已冻结的东西）；**17 可与 W1 并行**（只动 agent 仓与 `ai.*`，不碰 core 的引擎代码），它是 07 的 S3 段前置；**19 紧随 07**（两仓拆出来才有版本可管）；**18 必须最先做**（否则 W0/W1 的 breaking 改动会让 agent 仓静默烂掉）。W1 的 11/12/13/16 都动 `pkg/backtest`，按「10 → 11 → 12 → 13 → 14 → 16 → 15」串行、一单一 commit（**16 必须先于 15**）。W2 的 20/21/22 都动 `contracts` + 信号应用，串行。

---

## S-1 · 改名（排在一切之前，当前未启动）

> 依据 **ADR-D30** 与 [`repo-inventory.md`](design/qt-decoupling/repo-inventory.md)。**只改名，不改业务逻辑。**
> 用户口径：新建一套仓、把东西搬过去，不在旧仓大改；旧仓 archive 保留。

### QT-60 · 项目改名到 tellurion（4 仓 + module path + 文档引用）[P0] 🟡

- **① 目标**：把 4 个现有仓（`quant-trading` / `-agent` / `-ui` / `-docs`）迁到 `tellurion` 命名，module path 同步为「应用短名 / 库全名」；旧仓 archive，新仓可独立构建。
- **② 上下文**：`repo-inventory.md` §1/§2（D30）；实测待改 import：core **1153 处 / 526 文件**、agent **268 处 / 37 文件**；四个新仓名在本账号下 **404 空闲**；当前 4 仓 `git tag` 全为 0、工作区干净 —— 这是唯一无损改名窗口（一旦 S0 打了 tag、S2 拆了 contracts，成本翻倍）。
- **③ 要求**：
  ① 新建 4 个空仓：`ruoxizhnya/tellurion`、`tellurion-agent`、`tellurion-ui`、`tellurion-docs`（**由用户创建或授权**）；
  ② `git clone <旧仓> <新目录>`（**保历史**；不要用 `git archive` 或拷目录，会丢 blame），`git remote set-url origin` 指向新仓；
  ③ core：`go mod edit -module tellurion`；agent：`go mod edit -module tellurion-agent`；
  ④ 全仓 import 替换：`github.com/ruoxizhnya/quant-trading/` → `tellurion/`；`github.com/ruoxizhnya/quant-trading-agent` → `tellurion-agent`（含 `_test.go`）；
  ⑤ agent 的 `replace` 与 CI 的 `go mod edit -replace` 改为 `tellurion => ../tellurion`（**replace 保留**，S3 才删）；
  ⑥ 文档引用替换：跨仓引用前缀 `quant-trading:` → `tellurion:`；仓名 `quant-trading-*` → `tellurion-*`（**历史记录与现状描述不改**，只改目标态）；
  ⑦ 每仓 `go build ./...` + `go vet ./...` +（core）`go test ./...`；agent 需 `../tellurion` 存在才能构建；
  ⑧ push 新仓 → CI 首跑绿；旧仓 GitHub **archive（不删）**，本地保留 2 周只读；
  ⑨ 更新三处导航：core `AGENTS.md`、core `docs/README.md`、`tellurion-docs:docs/README.md`。
- **④ 约束与非目标**：**不改任何业务逻辑**；不趁改名做重构；不删旧仓、不在旧仓打 tag；不建 contracts/kit（那是 QT-07）；不改 owner。
- **⑤ 验收**：`grep -rn "github.com/ruoxizhnya/quant-trading" --include=*.go` = **0**；`*.md` 中仅剩历史记录；`go build ./...` + `go vet ./...` 全绿；`git log --oneline | wc -l` 与旧仓一致（历史未丢）；新仓 CI 绿；旧仓只读。
- **⑥ 边界**：只做改名与依赖路径；`contracts/` / `kit/` 的抽取与拆仓是 QT-05 / QT-07。
- **⑦ 执行进度（2026-10-20）**：
  - ✅ 本地四仓已建并改名：`tellurion`(970 文件) / `tellurion-agent`(181) / `tellurion-ui`(207) / `tellurion-docs`(140)，各有一笔初始 commit；
  - ✅ module path：core=`tellurion`、agent=`tellurion-agent`，agent 走 `replace tellurion => ../tellurion`（**保留到 S3**）；
  - ✅ **运行时标识全部改名**（用户 2026-10-20 追加要求）：库名 `quant_trading`→`tellurion`、测试库→`tellurion_test`、k8s namespace/configmap/ingress/host、镜像名、compose 变量、JWT Issuer、HTTP UA、默认 DSN、vscode workspace；**旧 Docker 卷名按事实保留在注释里**（改了会让运维删错卷）；
  - ✅ 验证：core/agent `go build ./...`+`go vet ./...` 绿；`tools/check_deploy_consistency.py` 全绿（含 compose↔k8s↔config 库名一致）；`check_doc_links.py` 40 md 零坏链；
  - ✅ 另外 8 仓的**本地占位仓**已建（README/AGENTS/go.mod/doc.go，`go build ./...` 全绿）：`contracts` / `kit` / `data` / `strategy` / `risk` / `analytics` / `orchestration` / `broker`；
  - ✅ 12 个仓的**远端创建与 push**（2026-10-20 14:10）：全部在 `ruoxizhnya` 下建好，`main` 已推。中途撞 GitHub 二级限流（连续建 10 仓后 `temporarily blocked from content creation`），最后 2 仓由退避脚本补上；
  - ✅ 首次 CI 发现的**两个改名回归已修**：gofmt（121 文件 import 排序）+ 归档 5 条坏链；复跑后 `Doc link check` 与 `gofmt` 均 success；
  - ⏳ 未做：旧仓 archive（**待用户确认**，建议观察一周）、agent/ui/docs 三仓的 AGENTS 三件套、QT-08 的自检脚本；
  - 📌 剩下的 CI 红是**预存在**问题，已立 **QT-61**（NT catalog 生成物未入库 / agent OBS-08 注册表与护栏漂移 / ui 疑似 flaky / core universe 需成分数据 seed）。

---

## 零、S0 · 分仓起步（可立即执行，与 W0 并行）

> 依据 ADR-D29 §7-S0。这两单**不改任何业务代码**，只让「独立仓」从目录独立变成**版本独立 + 有行为入口**。

### QT-06 · core 打 `v0.1.0` + agent 改 require tag（S0）[P0] ⬜

- **① 目标**：agent 对 core 的依赖从「源码路径」变成「版本号」——这是 S3 删 `replace` 的唯一前置（没有 tag，就没有版本可依赖）。
- **② 上下文**：`repo-topology-and-naming.md` §6 实测：core 仓 `git tag` 数量 = **0**；agent `go.mod:10` `require github.com/ruoxizhnya/quant-trading v0.0.0`、`:81` `replace ... => ../quant-trading`（**QT-60 改名后**：`require tellurion`、`replace tellurion => ../tellurion`，行号以改名后为准）；CI 用 `go mod edit -replace` 改写路径（`.github/workflows/ci.yml:32-34`）。agent 当前 import core 的 20 个包 / 37 个文件，**所以本单不删 replace**。
- **③ 要求**：⓪ **前置（硬）：QT-60 已完成**——tag 必须打在新仓 `tellurion`，打在即将 archive 的旧仓等于白打；① 在 core 当前 HEAD 打 annotated tag `v0.1.0`（`git tag -a v0.1.0 -m "..."`），**不改任何代码**；② agent `go.mod` 的 require 版本改为 `v0.1.0`；③ `replace` **保留**，行尾加注释「本地/CI 联调专用，S3（QT-07 收尾）删除」；④ agent CI 的 rewrite 步骤保留并同步注释；⑤ 在设计集 README 与 `docs/TASKS.md` 记录「`v0.1.0` = 分仓起点，不是发布版本」；⑥ **tag 要 push 到远端**才可能被其他仓 `go get`（本地 tag ≠ 可用版本）——S0 期靠本地 replace 不依赖它，但 S3 前必须 push（或改用私有 module proxy）。
- **④ 约束与非目标**：不打 `v1.0.0`、不发 release、不建 CHANGELOG 流程；不改 agent 代码；**不删 replace**（前置未满足：agent 仍 import core 实现包，见 QT-07 §S3）。
- **⑤ 验收**：`git rev-parse v0.1.0^{tag}` 存在且指向分仓起点 commit；两仓 CI 绿；agent `go mod tidy` 后 require 仍为 `v0.1.0`（未被 replace 抹回 `v0.0.0`）。
- **⑥ 边界**：只做版本化起点；单仓可构建属 QT-07 的 S3 段。

### QT-18 · 过渡期 core↔agent 连体护栏（S0）[P0] ⬜

- **① 目标**：S0~S3 期间 agent 编译期依赖 core 源码，而 W0/W1 的改动**全是 breaking**（abi v2 换 13 个函数、错误分级改 `getSignals` 返回、per-run 实例改 `Registry.Get` 语义、检查点主键变更）。要让这类破坏**在 core 的 PR 上立刻红**，而不是等两周后有人打开 agent 仓才发现它已经烂了。
- **② 上下文**：`repo-topology-and-naming.md` §7/§10（D29）；实测 core `.github/workflows/ci.yml` **零 agent 相关步骤**（grep 0 命中）；agent CI 反向 checkout core 并 `go mod edit -replace`（`.github/workflows/ci.yml:18-34`）——只有 agent 侧知道 break，core 侧无感知。
- **③ 要求**：① core CI 新增 `agent-compat` job：`actions/checkout` 拉 agent 仓（`repository: ruoxizhnya/tellurion-agent`）→ `go mod edit -replace tellurion=${{ github.workspace }}` → `go build ./...` + `go vet ./...`；
  ①-b **前置（外部）**：GitHub Actions 默认 token 只能访问当前仓，跨仓 checkout 必须配 `secrets.AGENT_REPO_TOKEN`（fine-grained PAT，只读 agent 仓）。**没有 PAT 时走降级方案**：提供 `make agent-compat`（本地/开发者机器跑同样的 checkout+build），并在 PR 模板里要求贴出该命令的输出——**降级只是过渡，不能长期替代 CI**；② **只 build/vet，不跑 agent 测试**（agent 测试需要 core 二进制/数据库，成本不成比例）；③ job 红时：core PR 必须附带 agent 侧适配 PR 链接，或在 PR 描述登记「known-break + 单号 + 期限」，二者必须有其一；④ 若 break 是有意的（如 abi v2），在 job 里允许 `known-break` 白名单（文件化，不用注释掉的代码）。
- **④ 约束与非目标**：不在 core CI 里跑 agent 的单测；不引入 monorepo 工具（Bazel/Nx）；不改 agent 代码；不替代 QT-09 的契约测试（本单只管**编译**兼容，不管**行为**兼容）。
- **⑤ 验收**：故意改一个 core 导出签名 → core PR 的 `agent-compat` 红（留证后撤回）；未改签名时绿；该 job 单次 ≤ 5 分钟（build-only）。
- **⑥ 边界**：只做编译兼容护栏；S3 后 agent 不再依赖 core 源码，本 job 转为「跨仓 tag 兼容检查」并保留。
- **⑦ 排序约束（硬）**：**本单合入 main 之前，QT-01 不得合入**。理由：QT-01 把 host 函数从 8 个换成 13 个（abi v2），是分仓后第一个打穿 agent 编译的改动；护栏不在，这次破坏就已经漏过去了。

### QT-08 · 四仓自治三件套（AGENTS / CI / TASKS）+ 自检脚本 [P1] ⬜

- **① 目标**：任何一个仓被一个 agent 接手时，都能**从本仓内**知道「怎么构建、边界在哪、依赖哪版契约、该做什么、禁止什么」，不靠读别的仓。
- **② 上下文**：ADR-D29 §2 五条不变量（R1~R5）+ §6 实测：`agent` / `ui` / `docs` 三仓**均无 `AGENTS.md`**；`docs` 仓**无 CI、无 `docs/TASKS.md`**；core 与 agent 的 tag 均为 0（QT-06 解决）。
- **③ 要求**：① 写 `tools/check_repo_autonomy.py`：逐仓检查 R1~R5，**未通过的项必须在 `docs/TASKS.md`（或该仓 TASKS）有对应单号**，否则红；② `agent` / `ui` / `docs` 各补 `AGENTS.md`（构建/测试命令、模块边界、**依赖的契约版本**、禁止事项、`docs/TASKS.md` 指针）；③ `docs` 仓补 CI（markdown 链接检查 + frontmatter 齐全）与 `docs/TASKS.md`（只放本仓事项，跨仓只留指针）；④ core `AGENTS.md` 增「分仓规则」一节（D29 §9 的 C1~C5）；⑤ 脚本**按仓类型分派**：go 仓查 `go build/vet/test`；`ui` 查 node 构建；`docs` 查链接与 frontmatter——不能用一把 `go build` 量四个仓；**R1 判据是「replace 指向仓外路径 = 红」，仓内 `replace ./contracts` 在 S1 期合法**，不要误杀；⑥ `docs` 仓的跨仓引用改 `repo:path` 纯文本（如 `tellurion:docs/...`；改名前后一律以新名书写，QT-60 负责批量替换），**不做成可点击相对链接**——独立 checkout 时 `../../` 必断，会让 R2 永远红；⑦ 脚本住 core 的 `tools/`，各仓 CI 只自证本仓（R1~R4）；**R5 的跨仓版本比对由 core 汇总跑**。
- **④ 约束与非目标**：不把 core 的 AGENTS 全文复制到各仓（只写本仓必须知道的）；不引入新 CI 平台；不改任何代码。
- **⑤ 验收**：脚本对 4 仓输出绿（agent 的 R1 以 `known-gap → QT-07` 登记方式通过，不是静默忽略）；`docs` 仓 CI 首次绿；四仓 `AGENTS.md` 均含「依赖的契约版本」与「禁止事项」两节。
- **⑥ 边界**：只做入口与自检；拆仓本身是 QT-07。

---

## 一、W0 · 契约冻结（写契约/常量/报告；**默认行为不变**）

### QT-01 · abi v2 契约冻结（13 个 host 函数 + 内存区所有权 + 枚举）[P0] ⬜

- **① 目标**：一份**唯一真相**的 abi v2 契约落到代码里（常量 + 校验 + schema），v1 artifact 仍可跑；后续所有 WASM 单都引用它，不再各自解释文档。
- **② 上下文**：`docs/design/qt-decoupling/adr-029-amendment-host-api-v2.md` §2.1（13 函数表，2026-10-20 补 `emit_target2`）、§7.1~7.8、§8；`p0-remediation-design.md` A5/A6/B1/B2/B3；`p0-round2-review.md` §8.2（emit_target2）、N5（host 区所有权）。现状：`host_api.go:40-47` 只有 8 函数；`BarContext.Series []float64` 只有 close；`hostGetBar` 的 `_ = fieldID`（`:164`）；`OnBarSession.Initialize` 直接 `WriteMemory(0, params)`（`on_bar_session.go:67`）；`hostGetState` 固定 `Write(4096, val)`（`host_api.go:231`）。
- **③ 要求**：① 把 §2.1 表的签名/语义/错误码写成 `contracts` 下的常量与类型（`FieldID`、`Action`、`MaxParamsBytes=4096`、`MaxStateValueBytes=64KiB`、`MaxInstanceStateBytes=4MiB`、`MaxKeys=4096`、`MaxScratchPerBar=256KiB`、`MaxSignalsPerBar=2×symbolCount`）；② 增量命名 `get_bar2`/`get_series_len2`/`get_date2`/`emit_target2`，**不改已有签名**；③ `ValidateImports` 升级为「名字白名单 + 版本族一致性」+ 与 `abi_version` 交叉校验，错误必须在 instantiate 之前可读；④ host 区导出符号 `__host_params_ptr` / `__host_scratch_ptr` / `__host_scratch_size` 的校验骨架（缺失/重叠/越界/未对齐 → 专用哨兵错误），**但只对 `abi_version=2` 强制**：`abi_version=1` 走白名单跳过符号校验——现有 14 个内置 artifact 都没有这些符号，一律 fail-closed 会把 v1 全打死，与本单 ⑤「v1 仍能跑」直接矛盾；⑤ `emit_signal` 与 `emit_target2` 同 bar 同 symbol 混用 → trap 的判定入口；⑥ 契约文件与文档逐项对表（一个测试读常量核 13 项）。
- **④ 约束与非目标**：**不实现** host 函数体、不改 `BarContext` 结构（那是 QT-24）、不动引擎与检查点、不改 v1 行为、不为"兼容"给 v1 补新符号。
- **⑤ 验收**：契约测试逐项对表 13 个函数与 6 类上限；4 个 bad artifact（缺 `__host_params_ptr` / ptr 落在模块数据段 / v1+v2 混导 / `abi_version` 与 import 族不符）全部被拒且错误可检索；v1 artifact 仍能实例化并跑通现有测试。
- **⑥ 边界**：只做"契约 + 校验骨架"；数据填充、host 函数实现、SDK 模板分属 QT-24/QT-26。
- **⑦ 前置（硬）**：**QT-18 已合入 main**。本单是 abi v2 的第一次 breaking，core 与 agent 在 S0~S3 期间编译连体，没有 `agent-compat` job 就会"core 绿、agent 静默烂"。

### QT-02 · `FillTiming` 冻结 + 两口径差异报告 [P0] ⬜

- **① 目标**：`SignalSpec.FillTiming` 成为必填契约；默认从 `same_close` 切到 `next_open` 有**可解释的差异报告**作依据。
- **② 上下文**：`p0-round2-review.md` §8.1（冻结文本）；现状 `engine_daily.go:181-205`（当日切片含当日 close）、`:263-270`（`res.price=末根 close`）、`:571`（`price := pricesCache[symbol]`）⇒ T 日收盘决策 + T 日收盘成交。全设计集此前无一处定义成交时点。
- **③ 要求**：① `contracts` 加 `FillTiming` 枚举 + `ErrFillTimingMissing`；② 引擎按 `FillTiming` 取成交价：`next_open` 用 T+1 开盘（T+1 停牌/涨跌停按 REV-09 顺延或拒单），`same_close` 保持现状；③ `ResultSpec` 落 `fill_timing` 与 `known_bias: same_close_fill`；④ 选 3~5 个基准策略跑两口径，产出差异报告（笔数/总收益/最大回撤/换手，并解释差异 ≈ 日收益平移一个 bar）；⑤ K15 前置断言：两内核 `FillTiming` 不一致 → `ErrFillTimingMismatch`（口径不符，不是结果不一致）。
- **④ 约束与非目标**：**先出报告再切默认**——本单要**实现 `next_open` 的取价分支**（否则出不了报告），但 `FillTiming` 的默认值在报告归档前不动，即「现有 run 的行为零变化」；不改滑点/费率/撮合内部（部分成交、涨跌停判定）；不允许为"保持历史基准不变"而选口径。
- **⑤ 验收**：缺字段 → run 拒绝且错误可读；报告归档在 `docs/test-cases/` 或设计集内并被引用；切换后基准结果与报告数值一致（同环境重跑 ±0）。
- **⑥ 边界**：只做"时点选择"；撮合真实性属 QT-52。

### QT-03 · 可复现留痕体系（`run_spec_hash` / `dataset_snapshot_hash` / `artifact_hash`）[P0] ⬜

- **① 目标**：任何一个回测结果都能回答"用什么输入跑出来的"，且**改任一影响结果的输入 ⇒ hash 必变**；三份 hash 的覆盖面与层级不再互相矛盾。
- **② 上下文**：`p0-round2-review.md` N6 + N7；事实：`run_spec_hash` 全设计集仅 2 处引用、零定义；三 hash 不含费率/滑点/成本模型/T+1/初始资金/`FillTiming`/锚点 digest/内核版本/toolchain/seed；`dataset_snapshot_hash` 两处定义冲突（`data-service-contract-and-ownership.md` §2.1 五项 vs `adr-029-amendment-host-api-v2.md` §7.1 要求含 `Axis`）；`version_hash` 不含 toolchain 而 `@hash` 却指它（`strategy-asset-service-api.md` §1.2/§3.3）。
- **③ 要求**：① 按 N6 的 canonical 集合定义 `run_spec_hash`（含 `fill_timing`、`universe_hash`、`adjustment_anchor_digests`、`kernel`/`engine_version`、`seed`）；② `dataset_snapshot_hash` **只保留一处定义**（含 `calendar_hash` 与 `adjustment_basis`），其余文档改为引用；③ `artifact_hash` 定为执行真相，`strategy_ref` 三形态语义写死（`id@artifact_hash` / `id@version_hash` / `id`），结果同时记 `{输入引用, version_hash, artifact_hash}`；④ `run_artifacts` 落 `run_spec_hash` + 分量 + `params_json` + `status`；⑤ 向量测试：改任一分量 → hash 变；不改 → 不变。
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

### QT-05 · S1 契约层与工具层抽取（`contracts/` 含 domain + `kit/` 含 db）[P0] ⬜

- **① 目标**：契约（含值对象）与横切工具在**物理上**成为两个独立 go module，依赖方向由编译器强制；这是拆仓前的最后一步，**仍住在本仓，不建新 git 仓**。
- **② 上下文**：`repo-topology-and-naming.md` §5/§7-S1（D29）；实测：`pkg/domain` 17 文件 3983 行，被 **core 306 个文件、agent 48 个文件**使用，自身仅依赖 `domain/market` 与 `fees`（2 处）；`pkg/backtest/contracts` 6 文件 2113 行，仅依赖 `domain` + `fees`，被 50 处（含 agent 5 处）使用；现状单 module、无 `go.work`。ADR-031 四契约（`RunSpec`/`SignalSpec`/`ResultSpec`/`EngineRunner`）就在这个包里。
- **③ 要求**：
  ① 建 `contracts/` 子 module，**go.mod 直接写最终路径** `github.com/ruoxizhnya/tellurion-contracts`——S2 拆仓时 import 路径不用改，只把 replace 换成 tag；
  ② `pkg/domain` 整包迁入 `contracts/domain`（17 文件）；`fees` 拆两半：**数据形状**（费率/滑点/成本模型的定义）进 contracts，**计算**留 core；
  ③ `pkg/backtest/contracts` 迁入 `contracts/engine`（四契约 + `MarketRouter`）；
  ④ 建 `kit/` 子 module（路径 `github.com/ruoxizhnya/tellurion-kit`），迁 12 个包 + 新增 **`kit/db`**（仅 connection/migrate/DSN 解析，**零表结构**，K 裁决落地）；
  ⑤ core `go.mod` `require` 两个 module + `replace` 指向 `./contracts` `./kit`（**仓内 replace 允许**；S2 删）；
  ⑥ `go.work` 仅本地生成，加入 `.gitignore`，**不入库**；
  ⑦ 护栏：反向 import 编译失败；`kit` 内禁业务词（backtest/strategy/order/factor/market…）；全仓禁旧路径 `tellurion/pkg/domain`、`tellurion/pkg/backtest/contracts`；
  ⑧ **agent 侧同步加 replace**（`replace` 不传递）：agent `go.mod` 加 `...-contracts => ../tellurion/contracts`、`...-kit => ../tellurion/kit`——漏了这条，S1 一完成 agent 立即编译失败；
  ⑨ **包名策略**：`contracts/engine/` 目录内保持 `package contracts`（现包名就是它），`contracts/domain/` 内 `package domain`；调用点前缀一行不改，只在 import 处加 alias。若改成 `package engine`，本仓 50 处 + agent 5 处都要改前缀；
  ⑩ 输出「**core-only 类型清单**」：`domain` 的 `screen`/`sector`/`supply_chain`/`macro`/`mining` 在 agent 侧各 0 命中，属 core 专用；S1 仍整包迁，清单留给 S2 后回收（见 D29 §5.2）。
- **④ 约束与非目标**：**不改任何业务逻辑**；不建新 git 仓（QT-07）；不搬 `sandbox/wasm`（必须与引擎同进程）；不搬 `fees` 的计算部分；**一次只搬一个包**，每搬一次跑 `go build ./...` + `go vet ./...`。
- **⑤ 验收**：`go list -m all` 显示 3 个 module；`go build ./...` + `go test ./...` 绿；旧路径 grep 零命中；在 `contracts/` 内 import `tellurion/pkg/*` **编译失败**；`kit` 业务词零命中；agent 构建仍走 replace（S3 才删）。
- **⑥ 边界**：只做 module 抽取与路径替换；拆仓、tag、跨仓护栏属 QT-07 / QT-09。

---

## 二、W1 · 执行正确性（修"今天就在错"的）

### QT-10 · 策略执行错误分级（fail-closed 落地）[P0] ⬜

- **① 目标**：策略故障与契约违规**终止 run**，只有数据缺口可以跳过当天且必须留痕；"漂亮曲线但策略一直在 trap"不再可能。
- **② 上下文**：`p0-round2-review.md` N1；`engine.go:918-926` 对 `getSignals` 的一切错误只 `Warn` + `skipping day`，注释明写"不进 invalid_reasons"；而该错误面同时含 `RejectFutureSignals` 拒绝与 wasm trap。
- **③ 要求**：① `contracts` 定义 `ErrDataGap` / `ErrStrategyFault` / `ErrContractBreach` 三类哨兵；② provider 侧按类包装（数据缺失→DataGap；trap/非法信号/预算超限/artifact 校验失败→StrategyFault；前视/未知参数/abi 不符→ContractBreach）；③ `RunBacktest` 在 `ErrDataGap` 时跳过当天并写 `invalid_reasons`，其余两类立刻 `EndRun` + `status=failed`，不返回可用于决策的结果；④ `ResultSpec` 加 `Status ∈ {ok,degraded,failed}` + `Failures[]`；⑤ 护栏：`pkg/backtest` 内 `getSignals` 的 err 必须被 return 或映射为 fatal，禁止只 log。
- **④ 约束与非目标**：不改变数据缺口的既有语义（仍跳过当天）；不新增 run 状态存储（用现有 `StateStore`）；不动信号内容。
- **⑤ 验收**：构造"每 50 天 trap 一次"的 wasm → run `failed` 且 `Failures` 点名日期/原因；构造缺一天数据的 provider → `degraded` 且 `invalid_reasons` 有记录；护栏测试：把错误改回"只 log"→ 红。
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
- **③ 要求**：① 启用 `WithCloseOnContextDone(true)`，超时返回 `sys.ExitError`；② 双层预算（单次调用 market 50ms / symbol 5ms 可配 + run deadline 默认 10min）；③ `ErrSessionTerminated` 与 `terminated` 状态，终止后禁止调用；④ **`replay_mode = state_only` 为默认**：重放期 `emit_signal`/`emit_target2` 成功但不落 Signals，计数 `replay_suppressed_signals`；重放到故障 bar 后以 `apply` 重执行该 bar；⑤ terminated 的 `EndRun` **不调 finalize**，run 标 `degraded` + `Failures += session_terminated`；⑥ 重建次数按 run 计（上限 3），超限 `failed`；⑦ 开/关周期检查各跑一遍 market scope 727 次 on_bar，量化开销（>10% 则按可信度分级启用，结论成文）。
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
- **③ 要求**：① `SignalProvider` 的 WASM 实现（`OnBarSession` → `Signals`，走 QT-14 的 per-run 实例）；② `EmittedSignal → domain.Signal` 映射（Action→Direction、Strength、SymbolIdx→Symbol 经 `RunUniverse`）；③ `emit_target2` → 目标权重→手数（`floor(|w|×equity/price/lot)×lot`），policy cap 优先并计 `sizing_clamped_total`；④ 结果标 `sizing_source`；⑤ 五道门（Resolve/Fetch/Verify/Compile/Mount）按 `strategy-asset-service-api.md` §4。
- **④ 约束与非目标**：内循环零网络（resolve/fetch 只在 run 前一次）；不实现编译服务（QT-41）；不绕过 `ValidateImports`。
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

## S2~S3 · 拆仓（W0 与 QT-05 全绿之后）

> 依据 ADR-D29 §7-S2/S3、§8 建仓 checklist。**拆的是已经冻结的东西**——W0 未绿就拆 = 契约每天发版。

### QT-17 · agent 数据自持（D10：`ai.*` schema 建表与迁移）[P1] ⬜

- **① 目标**：agent 不再通过 core 的 `*PostgresStore` 读写实验域数据；`ai.*` 的 DDL 与 migrate 由 agent 仓持有——**这是 S3 删 replace 的数据前置**（storage 依赖没有别的出路）。
- **② 上下文**：ADR-D10（实验域数据归 agent）；实测 agent 用到 storage 的 16 个符号 / 14 个文件：连接层 `PostgresStore`(12) `NewPostgresStore`(3) `BuildDSN` `DatabaseConfig`；实验域 `Experiment`(5) `ExperimentMetrics`(6) `ExperimentUpdate`(4) `ExperimentStatus*`；研究域 `ResearchProfile`(10) `ResearchQuestion` `ResearchConclusion`；**仪器域** `TradingCalendar` `ListingWindow`(4)。`kit/db` 由 QT-05 提供。
- **③ 要求**：
  ① agent 仓建 `ai` schema 的 DDL（表名以 core `pkg/storage/experiments.go` / `research.go` / `postgres.go` 的现有定义为准，逐表核对后迁移，**不凭类型名臆造表名**）与自己的 migrate 入口；
  ② agent 用 `kit/db` 建自己的 store，替换 12 处 `PostgresStore` 用法；
  ③ **仪器域不迁**：`TradingCalendar` / `ListingWindow` 属 core，agent 改走 core 的**只读 port**（D10 §87「只读视图」通道）——**本单需同时补进 D24 的合法动作清单**（现六类里没有「交易日历/上市窗口查询」）——这是对**已裁决文档的增补**，开工前需用户确认第 7 类动作；
  ④ 双写期：旧表与 `ai.*` 同时写 → 行数/校验和对拍 → 切读 → 停写；core 侧 `experiments.go` / `research.go` 定义标 deprecated 并登记删除期限；
  ⑤ 护栏：agent 禁止 import `pkg/storage`（AST）。
- **④ 约束与非目标**：**不删 core 旧表**（保留只读一个周期）；不迁行情/回测结果/因子定义（仪器域）；不改 core 的实验 handler；不在本单做 HTTP 化（QT-50）。
- **⑤ 验收**：agent `grep -r "pkg/storage"` = 0；`ai.*` 行数与旧表对拍一致（报告归档）；停写后 core 旧表不再增长；core 侧 deprecated 标记存在；日历/上市窗口经 port 取到且值一致。
- **⑥ 边界**：只做实验/研究域的数据归属；`ai.*` 之外的 schema 不动；X 线服务的数据归属按 QT-40。

### QT-07 · contracts / kit 拆成独立 git 仓（S2 → S3）[P1] ⬜

- **① 目标**：`tellurion-contracts` 与 `tellurion-kit` 各自**单仓可构建、可自证**（R1~R5 全成立）；core 从「仓内 replace」切到「跨仓 tag」；agent 最终删掉 `../tellurion` replace。
- **② 上下文**：`repo-topology-and-naming.md` §7/§8（D29）；前置 = W0 五单 + QT-05 全绿；现状两目录已在本仓内且 go.mod 用最终路径（QT-05 的设计），拆仓时 **import 路径不用改**。**本单分两次合入**：S2（①~④⑥，不依赖 QT-50）先合；S3（⑤）等 QT-50 + QT-17 完成后再合——不要因为 ⑤ 未完成而卡住 ①~④。
- **③ 要求**：
  ① S2-a：`git init -b main` 两仓 → 迁出目录 → `go mod init` 已是最终路径（无需改）→ `git tag -a v0.1.0`；
  ② S2-b：core 改 `require github.com/ruoxizhnya/tellurion-contracts v0.1.0` + `-kit v0.1.0`，**删 `replace` 与 `go.work`**；
  ③ S2-c：两新仓复制主仓 CI 门禁（gofmt/vet/test/repoguard），**不得 checkout 其他仓**；补 `AGENTS.md` + `docs/TASKS.md`（QT-08 的模板）；
  ④ S2-d：主仓加反向护栏（禁止重新 import 已迁出的本地路径）；
  ⑤ S3（**依赖 QT-50 + QT-17 完成**）：agent 删 `../tellurion` replace 与 CI 的 rewrite 步骤；agent 只依赖两个新仓的 tag + HTTP；**注意 S3 是唯一允许删 replace 的时点**，提前删 = agent 立刻编不过；
  ⑥ 行为对拍：拆仓前后同参回测结果**逐位一致**（报告归档）。
- **④ 约束与非目标**：**不改任何业务逻辑**（纯搬 + 改 require）；不在拆仓同时做 HTTP 化（那是 QT-50）；不为"减少仓数"合并 contracts 与 kit（二者依赖方向不同：contracts 依赖 kit 的 decimal/errors，kit 零业务）；不发布 release note。
- **⑤ 验收**：两新仓单独 checkout 后 `go build ./...` + `go test ./...` 绿（不 checkout core/agent）；core 删 replace 后 `go build ./...` 绿；`tools/check_repo_autonomy.py` 对两新仓全绿；拆仓前后对拍逐位一致；agent 仓的 R1 在 S3 后转绿。
- **⑥ 边界**：只做仓的搬出与依赖切换；X 线六个服务仓**不在本单**（真起进程时另立）。

### QT-09 · 跨仓依赖护栏 + 契约测试（S2）[P1] ⬜

- **① 目标**：ADR-D29 §9 的 C1~C5 五条规则**机械可查**；「两边各写一份理解」能在契约层被发现，而不是靠 K15 双跑。
- **② 上下文**：`repo-topology-and-naming.md` §9；现状无任何跨仓护栏（agent CI 反而在「帮」跨仓构建：checkout core + `go mod edit -replace`）；契约仓 S2 才存在。
- **③ 要求**：
  ① CI 步骤：`replace` 指向**仓外**路径 → 失败；`go.work*` 被 git 跟踪 → 失败（C1/C2）；
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
  ① contracts 仓 README 写**版本规则表**：`MAJOR` = 删/改签名或语义；`MINOR` = 新增能力（向后兼容）；`PATCH` = 文档/注释/无行为修正；
  ② **0.x 阶段（现在到 1.0 前）允许 breaking，但必须 bump MINOR + 写 CHANGELOG 条目**；**1.0 之后**才启用「deprecated 至少保留 1 个 MINOR 周期」；
  ③ **兼容矩阵**：contracts `vX.Y` ← 支持 core / agent 的哪些版本，写在 contracts 仓 README，每次发版更新；
  ④ 内容寻址标识（`strategy_ref` / `artifact_hash` / `run_spec_hash`）**不参与 semver**——它们自带版本语义；
  ⑤ tag 规范：annotated tag，message 首行 `vX.Y.Z: <类型>`（feat/fix/breaking）。
- **④ 约束与非目标**：不引入 release 工具链（goreleaser 等）；不做自动 CHANGELOG 生成；不强制 core 也走同一套（core 是应用不是库，tag 只作依赖锚点）。
- **⑤ 验收**：contracts 仓 README 存在版本规则表与兼容矩阵；CHANGELOG 至少有一条记录；三个仓的 require 版本都落在矩阵声明范围内。
- **⑥ 边界**：只定策略与文档；「禁删 deprecated」的机械护栏由 QT-09 ⑤ 实现。

---

## 五、X 线 · 服务拆分（可选部署形态，进程内 port 先行）

### QT-40 · X1 数据服务（统一协议 + aggregator + `dataset_snapshot_hash`）[P1] ⬜

- **① 目标**：数据协议冻结进 contracts；aggregator 能给出可复现的 `dataset_snapshot_hash`；connector 全部走声明式契约。
- **② 上下文**：`data-service-contract-and-ownership.md`（D19）X1a~X1d；现状已实现约 80%（12 connector + `ConnectorDeclaration` + `ETLPipeline`/`Normalizer`/`ValidatePoints`/`Deduplicate`；PG 已有 ingest/market/quant/research）。
- **③ 要求**：① 协议类型（`UnifiedDataPoint`/`OHLCV`/`FrequencySpec`/`TimeSemantics`）进 QT-05 的 contracts；② `ErrVisibleAtMissing` 等错误码；③ aggregator 面（PIT 过滤 + 频率聚合 + 票池解析 + 快照 hash + 缓存）；④ `dataset_snapshot_hash` 按 QT-03 的唯一定义实现（含 `calendar_hash`/`adjustment_basis`）；⑤ 12 connector 全走 Declaration + 共享 Normalizer。
- **④ 约束与非目标**：不重写现有 connector；不在内核内保留第二写者；不做跨库拆分。
- **⑤ 验收**：同一 hash 两次拉取返回一致；漏声明 `visible_at` 的源被拒收；改日历/复权口径 → hash 变。
- **⑥ 边界**：只做数据面；策略/风控/分析各自的 schema 见后续单。

### QT-41 · X2 策略资产服务（定义/版本/artifact/resolve）[P1] ⬜

- **① 目标**：策略真相源在服务侧，引擎只持可驱逐只读副本；`id@artifact_hash` 可离线复现。
- **② 上下文**：`strategy-asset-service-api.md` + `strategy-source-of-truth-and-cache.md`（D13/D14）+ QT-03 的 hash 体系；未决 A（BYTEA vs 对象存储）当前按 BYTEA 落地（本地栈）。
- **③ 要求**：① `strategies` 加列（carrier/abi_version/scope/capability/entry/`active_version_hash`/`active_artifact_hash`）+ `strategy_artifacts` + blobs；② 写侧 7 端点、读侧 2 端点（resolve/artifact）；③ 缓存按 `artifact_hash`（永不失效）+ latest 的 TTL/事件失效；④ promote 不覆盖 artifact 行；⑤ 14 个内置策略登记为 `carrier=builtin`。
- **④ 约束与非目标**：策略服务**不算信号**；不改执行路径的"先查 artifact 再回退内置"在 QT-26 已定的顺序；不做对象存储（除非 A 改判）。
- **⑤ 验收**：服务停机后已缓存 hash 的回测照跑且结果一致；同源码两工具链 → 两 artifact 可分别追溯；`id@version_hash` 解析结果回写实际 `artifact_hash`。
- **⑥ 边界**：只做资产与解析；编译服务见 QT-41b（并入本单要求 ① 之外的 CI 侧）。

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
- **③ 要求**：① `job`/`batch`/`walkforward` 从 `pkg/backtest` 上移；② worker 池调度 + 断点续跑协调；③ `run` schema（`backtest_jobs`/`walk_forward_reports`/`run_artifacts`）；④ `/api/runs/{id}/provenance` 端点；⑤ 内核通过 port 读写这些表。
- **④ 约束与非目标**：不在内核保留编排逻辑；不改 `EngineRunner` 契约；不做 K8s。
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
- **③ 要求**：① 装配代码移到 `cmd/*/wiring`（或 `pkg/wiring`），kernel 只留 Module/BootOrder/生命周期/槽位；② 定义 agent 侧 HTTP 客户端（六类粗粒度动作）；③ 护栏：kernel 禁止 import 业务族；agent 禁止 import core 实现包、禁止逐 bar 远程调用（AST）。
- **④ 约束与非目标**：不拆进程（仍在同一可执行体内可选装配）；不改 BootOrder 语义。
- **⑤ 验收**：kernel 的非测试 import 里零业务族；搬运前后 `package_wiring_test.go`/`module_lifecycle_callback_test.go` 各跑一次全绿；agent 侧 grep 无 per-bar 远程调用。
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

## 七、存量任务（第 3 代延续，ID 不变）

> 前提已于 2026-10-20 复核：DBT-13（回落仍在 `eastmoney_sectors_adapter.go:725`、juchao 两处调用仍在 `:238/:241`）、DBT-14（schema 仍 `date-time`、实现仍只接 `2006-01-02`）、DBT-17（`capital_flow` 仍无读方）**均成立**。DBT-15 需真库读数、DBT-16 需外部产出方、DBT-18 跨仓——六字段**原样迁入**如下（正文不改写）。

### DBT-13 · `parseEastmoneyDate` 解析失败静默回落「今天」[P1] ⬜

- **① 目标**：eastmoney 族任何一行数据，落库日期要么是真值、要么当场失败——**不存在"读不懂就当今天"** 这条路径。
- **② 上下文**（DBT-10 真源腿实测，2026-10-19）：`pkg/data/source/eastmoney_sectors_adapter.go` 的 `parseEastmoneyDate` 末尾 `return time.Now().UTC()`。它原先只认三种纯日期形状（其中 `"2006-01-02"` 还重复列了一次），而 datacenter 的 `RPT_DAILYBILLBOARD_DETAILS` 实际返回 **带时刻**的 `"2026-09-24 00:00:00"` ⇒ 龙虎榜**每一行**都掉进那个回落、落库日期全变成"今天"。DBT-10 已补齐带时刻两种形状并在 top_list 路径上加了「TRADE_DATE 缺失/空串当场失败」（`:506` 附近），但**回落本身仍在**，因为还有两个调用方共用它：`pkg/data/source/juchao_adapter.go:238`（传的是 `adjunctUrl`，注释自陈 placeholder）与 `:241`。把回落改成零值会连带改变巨潮那条链的行为——**不在 DBT-10 的文件边界里，故另立本单**。
- **③ 要求**：① 给"读不懂"一个显式出口（推荐 `parseEastmoneyDateStrict(v) (time.Time, bool)`，非严格版保持现行为不动，逐调用方迁移）；② 迁移 `juchao_adapter.go:238/241` 两处，逐处写明"这里读不懂时应该发生什么"（拒收该行 / 用别的字段兜），不许再用"今天"兜；③ 全仓 grep 其余 `time.Now()` 作为**解析兜底**的同类形状，登记或一并收口；④ 测试：不可解析输入 → 严格版返回 `ok=false`，且真库落库日期等于源给的日期而非当天（用固定历史日期的夹具，别用"今天"）。
- **④ 约束与非目标**：不改各 adapter 的业务语义与字段映射；不动 `ohlcv_daily_qfq` 存量数据；不顺手把 eastmoney 族的 filter/pagination 重构（DBT-10 已收口的部分不许回退）；不改 `internal/repoguard` 白名单形状。
- **⑤ 验收**：`go test ./pkg/data/source/ -count=1` 绿；有一条断言能区分「窗口外日期」与「日期读不懂」两种失败（报文不同、测试各自钉住）；**破坏验证**：把回落改回 `time.Now()` ⇒ 至少一条测试红 ⇒ 还原（判据：文件 sha256 与改前相同）。
- **⑥ 边界**：只做"日期解析失败的处理"这一件事；字段口径、单位换算另单。

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

---

### QT-61 · CI 基线归零（改名后暴露的 4 项预存在失败）[P1] ⬜

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

---

## 八、跨仓任务索引（不在本仓，勿双写）

> 仓名以 `repo-inventory.md` §2 的「旧→新」为准；**QT-60 完成前，实际仓名仍是旧名**。
> - **`tellurion-agent` 仓**（旧 `quant-trading-agent`）：`OBS-01-R2-A` ⬜（2026-10-16 裁决 (b)，core 零改动；2026-10-19 复核仍 ⬜）；DBT-18 属这一族（`mining.Proposer` 契约指针在 core）。
> - **`tellurion-ui` 仓**（旧 `quant-trading-ui`）：`AUD-61` ⬜（2026-10-19 复核仍在册，该仓另有 4 处 ⬜）；`docs/design/stitch_quantlab_ux_redesign/TASKS.md` 的 `A-05` ✅、`A-08` ✅。
> - **`tellurion-contracts` 仓 / `tellurion-kit` 仓**（旧 `quant-trading-*`）：**S2 后启用**（QT-07）。在此之前两目录住在本仓（QT-05），跨仓索引不登记未存在的仓。
> - **`tellurion-agent` 仓**：**S3 前仍依赖本仓源码**（replace），不可单仓构建 —— 这是 `known-gap`，对应 QT-07 收尾段，不是遗漏。
> - **`tellurion-docs` 仓**（旧 `quant-trading-docs`）：**NT v2.0.0 正式版升级**（`docs/guides/nt-sidecar-operations.md`）——唯一外部等待项。⚠️ QT-02 的 `FillTiming` 一旦切换，NT 侧必须同步实现 `next_open`，否则 K15 会报 `ErrFillTimingMismatch`；升版时把这条加进验收门。

---

## 维护本文件

- 新增 task 必须按 [AGENTS.md §8.5](../AGENTS.md) 写全六字段；**六字段里必须显式引用冻结契约的路径与节号**（本代 wave-0 的冻结文本在 `docs/design/qt-decoupling/p0-round2-review.md` §8、`p0-remediation-design.md`、`adr-029-amendment-host-api-v2.md`）。
- 完成即标 ✅（附日期）并移入 `archive/`；本文件只留未完成项。换代时整篇加时间戳归档（`archive/TASKS-YYYY-MM-DD-archived.md`），旧代正文不改写。
- **派工前必须先证伪工单前提**（读码核对 → 不符就停下回报 + 给拆法，不硬做、不扩范围）。
- **执行顺序**：严格按「**S-1(QT-60)** → S0（QT-06 → **QT-18** → QT-08）→ W0 → S1(QT-05) → S2（QT-17 → QT-07 → QT-19 → QT-09）→ W1 → W2 → W3」；同波次内撞文件的按 QT 序号串行，一单一 commit。**W0 完成前不开 W1 的代码单**——否则 artifact 与基准都要重做。
- **合入门禁（硬约束，不是建议）**：⓪ **QT-60 未完成前，QT-06 不得开工**——tag 必须打在新仓 `tellurion`，打在即将 archive 的旧仓等于白打；① **QT-18 未合入 main 前，QT-01 不得合入**——第一个 breaking 就在 QT-01 的 abi v2，护栏不在就会漏；② QT-05 未合入前，QT-07 不得开工；③ QT-07/QT-09 未绿前，不得在任何仓新增跨仓 `replace`（D29 §9 C1）。
- **新需求优先于存量未完成任务**；与旧任务重叠的可合并或直接删除，跨仓迁移的任务条目保留可追溯指针。
- 归档文件的状态标记若与自身完成记录矛盾，**只在文件头加订正说明**，条目正文与标题的历史措辞不改写。
