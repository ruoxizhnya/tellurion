---
status: active
last-verified: 2026-10-26
verified-by: 第四轮·目的对齐审计（12 仓 git / go.mod / DDL / 部署清单 / 台账实测；只读静态取证，未跑 Go、未起服务、未写库）
---

# 第四轮审计 · 目的对齐（2026-10-26）

> **这份审计只回答一个问题：已做出来的部分，有没有偏离最初要做的事。**
> 与前几轮的分工：wave3（2026-10-25）审的是**进度与账实**（到哪了 / 账对不对 / 下一步先做什么）；本篇审的是**目的**（做的方向对不对）。
> 不替代裁决：本篇只列证据与判断，**不代主任拍板**；待裁项集中在 §六。

---

## 一、结论（一句话）

**底座主线没跑歪；X 线服务拆分支线跑偏了。**

- **没跑歪的**：契约冻结（W0）、执行正确性（W1/W2）、agent 去 `replace`（S3）、contracts/kit 两个库仓的落地——都有代码、tag、护栏与台账证据，且方向与 D1「先端口、后拆进程」一致。
- **跑偏的**：X 线六仓不是「把边界收口成 port」，而是**先建了六个平行实现 + 六个可执行服务入口（5 仓；strategy 含 assetservice + compiler）+ 两张容器清单**；core 一侧**一件没减**（202k Go LOC 仍在原地），除 data 读链与 risk 客户端（代码已接线、开关默认关）外没有一条真正进入生产回路；而部署路径今天还是断的。
- **净效果**：这轮重构目前增加的是**表面积**（+51k LOC 的平行实现、6 个新 schema、6 组重复 DDL），不是**耦合下降**。这与 D1 的「切成独立服务是随时可做的能力，不是现在就付出的代价」相反。

> 判断口径：**「仓建好了 / 服务能启动」≠「解耦完成」**。解耦完成的判据只能是：旧归属停止写、新归属成为唯一真相、消费方按版本契约调用。今天在这三条上，只有 data 的两个作业与 agent 的实验日志走完了「停写」，其余都没有。

---

## 二、目的基线（审计所依据的原文，不转述）

| # | 原始目的 / 纪律 | 原文位置 | 判据 |
|---|---|---|---|
| G1 | **不拆进程，先做端口化模块化单体**；切成独立服务是「随时可做的能力」，不是现在就付的代价 | `tellurion:docs/design/qt-decoupling/README.md:15`（D1） | 是否先有 port、后才有进程 |
| G2 | **现在不要做**：拆 HTTP 服务 / MQ / K8s / 拆库分表 / 远程适配器；**不为「将来可能要拆」提前建空仓** | 同文件 §贯穿纪律 4 | 是否在触发条件满足前建了进程/容器/仓 |
| G3 | 六仓 **≠** 六进程；risk / analytics / orchestration / broker 今天只到「仓内实现 + core 侧 port」，**容器升格各需一条 ADR** | `tellurion:docs/design/qt-decoupling/repo-inventory.md:93-101`、`:155`（M5 对账） | 进程升格是否有 ADR |
| G4 | X 线服务仓从「仓内实现」升格为容器 = 阶梯 **S4**，**各需一条 ADR** | `tellurion-docs:docs/adr/adr-034-repo-topology-and-ladder.md`（S4 行） | 同上 |
| G5 | **DDL 只有一个 owner**；两个写者 = 两种真相 | `tellurion-docs:docs/SYSTEM-DESIGN.md:26`（P3） | 每张表是否只有一个 owner / 一个写者 |
| G6 | 形状只有一处（contracts），服务之间不 import、只经 contracts + HTTP | 同文件 P2 / §3 | 服务是否依赖版本化契约 |
| G7 | 产品目的：**AI 操作底座、人拍板**；不是「服务越多越像平台」 | `tellurion-docs:docs/PRODUCT.md:16`、`:63` | 工程投入是否服务于 AI 实验回路 |

---

## 三、发现（按严重度）

### P0-1 · X 线跳级：从「可选部署形态」变成「六个可执行服务入口 + 两张容器清单」，且无升格 ADR

**实测**

- 六仓均已实装，`cmd/*service` 监听默认值：data `:8081`、risk `127.0.0.1:8086`、strategy `127.0.0.1:8085`（另有 `cmd/compiler`）、analytics `127.0.0.1:8087`、orchestration `127.0.0.1:8088`；broker 无 `cmd/`（仍是 port 实现，符合 M5 结论）。
- core `deploy/k8s/` 已有 **10 份清单**，其中 `risk-deployment.yaml`（`containerPort: 8086`）与 `analytics-deployment.yaml`（`containerPort: 8087`）是**真实容器部署**。
- 未找到任何「risk / analytics 进程升格」ADR：现行 ADR-032~035 讲的是拓扑/载体/仓清单，均未裁决升进程；ADR-027 仍是 **Proposed**，且其 §2.1 明确判定 risk 与 engine **同进程**。

**为什么这是偏离**

- G2 明写「现在不要做拆 HTTP 服务 / K8s」；G3/G4 要求四仓升进程**各需一条 ADR**。今天的形态同时越过了这两道闸。
- 端口默认值本身证明「没有做过同机集成」：risk `8086` 撞 ai-service，strategy `8085` 撞 analysis-service——两份默认值同时起会 bind 失败，只能靠 `-addr` 手工错开。

**代价**

- 六个可执行入口的运维面（鉴权、端口、健康检查、schema 迁移）已产生，但只有 1 条读链是真被 core 消费的；其余是「养着但不跑」。
- 违反 G2 的直接后果不是「多写了代码」，而是**新真相先于旧真相退场而存在**（见 P0-2）。

**建议（不代裁）**

- 二选一：① 补 ADR 承认升格并补全接线/部署/鉴权；② 撤回容器清单与监听默认值，回到「仓内实现 + core 侧 port」，等 S4 触发。**不允许**维持「有清单、无 ADR、无接线」的中间态。

---

### P0-2 · 六个能力在 core 里仍是全量实现：这是「复制」，不是「迁移」

**实测（Go LOC，含测试）**

| 能力 | core 侧仍在的实现 | 服务仓实现 | 状态 |
|---|---|---|---|
| data | `pkg/data` 25,620 + `cmd/data` 11,846 | `tellurion-data` 12,158 | core 仍是生产写入方；新仓是平行实现 |
| strategy 资产 | `pkg/strategy` 21,575（含运行时） | `tellurion-strategy` 9,916 | core 仍持 `strategies` 表与本地注册表；资产仓零 core 调用方 |
| 风控 | `pkg/risk` 4,796 + `pkg/compliance` 3,523 + `pkg/alert` 2,124 | `tellurion-risk` 6,151 | 规则快照已外置；`bundle → RiskPolicy` 仍未接 |
| 交易通道 | `pkg/live` 14,077 | `tellurion-broker` 3,238 | core 仍跑 MockTrader；broker 是 port 实现 |
| 编排 | `pkg/backtest/job` 2,093 + `batch` 1,754 | `tellurion-orchestration` 12,467 | core 仍写 `backtest_jobs`；编排写 `run.backtest_jobs` |
| 分析 | `pkg/backtest/metrics` 903 + `reporting` 1,296 | `tellurion-analytics` 7,346 | core 仍在 `cmd/analysis` 直接算；analytics 零 core 调用方 |

- 总量：core **202,198 LOC / 840 文件**；六个服务仓合计 **51,276 LOC**。重构没有让 core 变小，只是在旁边长出了 51k LOC。
- DDL：core 内联 **54 条 `CREATE TABLE`**（多数是裸名 `public.*`）；六服务仓另有 data 23 / strategy 5 / risk 7 / analytics 5 / orchestration 6 / broker 1，agent 8。

**重复 DDL 的具体簇（不是抽象风险）**

| 簇 | core 侧 | 新归属 | 决策状态 |
|---|---|---|---|
| 行情/同步 | 裸名 `stocks` / `ohlcv_daily_qfq` / `ohlcv_minute` / `realtime_quote` / `capital_flow` / `sectors` / `stock_sector_map` / `top_list` / `limit_up_pool` / `announcements` / `news` / `hot_search` / `global_ohlcv` / `trading_calendar` / `index_constituents` / `dividends` / `splits` / `market_activity` / `sync_jobs` / `sync_schedules` / `ingest.raw` | data 的 `market.*` / `ingest.*` | **只裁了 `market.index_daily`**（裁决 ⑰）；其余无逐表切读/停写计划 |
| 任务面 | 裸名 `backtest_jobs` / `walk_forward_reports` | `run.backtest_jobs` / `run.walk_forward_reports` | 无裁决；core 仍是状态权威，编排侧同形表并存 |
| 策略资产 | 裸名 `strategies` | `strategy.strategy_definitions` / `_versions` / `_artifacts` | 无切读/停写计划 |
| 风控（**不是同表双写，是裁决权双真相**） | `pkg/risk` 的进程内 `RiskPolicy` + `quant.alert_outbox`；`audit_logs`（core 人审计） | `risk.rule_snapshots` / `risk.risk_events` / `risk.audit_logs` | 规则快照已外置、`risk_rules_hash` 已生产；但 `bundle → RiskPolicy` **未接**，进程内限额仍来自 core 配置；事件回传默认关 |
| AI 实验域 | 裸名 `experiments` / `factor_hypothesis` / `factor_genes` / `strategy_genes` | `ai.*` | agent 已停写 `experiments`；core 侧 `experiments.go` 仍有 `INSERT`、`factor_hypothesis.go` 仍有写方法（生产零调用方） |

**为什么这是偏离**

- G5 要求「DDL 只有一个 owner」；今天 **4 个簇是 DDL 双 owner**（market / run / strategy / ai），另有 **1 个簇（risk）是裁决权双真相**而非同表双写。
- 计划的迁移纪律是「先建第二写者 → 对拍 → 切读 → 停写」，但实际只有 data 的 `capital_flow` / `top_list` 与 agent 的 `experiments` 走完了停写；其余停在「第二写者/第二份 DDL」，没有对拍与切读。

**代价**

- 每一张双 owner 表都是一次静默分歧的机会；schema 迁移、备份、权限、审计都要做两遍。
- 「重构完成」无法验收：没有一个可机械判定的「旧写入点归零」清单。

**建议（不代裁）**

- 先产出一张**全量双 owner 表清单 + 逐表四步计划**（owner / 当前写者 / 读方 / 对拍方式 / 停写条件），再谈任何新服务。
- 对已确认零生产调用方的 core 遗留写方法（`SaveFactorHypothesis`）直接删除或标 `//go:build legacy`，不要留「可写但没人写」的第三种状态。

---

### P0-3 · 部署路径今天构建不出来：compose 用的 Dockerfile 仍在 build 已删除的 `cmd/strategy`（**2026-10-26 已修**）

> **2026-10-26 已修**：`Dockerfile.service` 的 `cmd/strategy` build 行与 `strategy-service` COPY 行已删除；只构建 analysis/data。`SVC-DEPLOY-01` 随 ADR-036 立单。以下为审计时的原文，保留作证据。

**实测**

- `tellurion:Dockerfile.service:25-27` 仍执行 `go build ./cmd/data`、`./cmd/strategy`、`./cmd/analysis`；而 core 的 `cmd/` 只有 `analysis` / `data` / `worker` / `dualrun`——`cmd/strategy` 已于 REV-16 删除。
- `tellurion:docker-compose.yml` 的 `data-service` 与 `analysis-service` 都是 `context: .` + `dockerfile: Dockerfile.service` ⇒ 两份镜像都会在 `go build ./cmd/strategy` 处失败。
- 即使构建修好，compose 里名为 `data-service` 的容器跑的也是 **core 的 `cmd/data`**，不是 `tellurion-data:cmd/dataservice`。**「data 服务已拆仓」在部署面上不成立。**

**为什么这是偏离**

- 重构的目标形态若不能一键构建，那么「六个仓/十二个仓」只是源码分类，不是可运行系统。
- 这条与 G2 的「先端口、后进程」互相印证：进程先建了，部署链路没验证。

**同批实测（部署护栏本身是红的，而且是假阳性）**

- `python tools/check_deploy_consistency.py` 退出 **1**，两条：`analysis-deployment.yaml` / `data-deployment.yaml` 注入的 `JWT_SECRET`「全仓没有任何读取点」。
- 取证：`tellurion:pkg/bootstrap/config.go:25` 就是 `v.BindEnv("auth.jwt_secret", "JWT_SECRET", "AUTH_JWT_SECRET")` —— 读点存在。
- 根因：检查器 `tools/check_deploy_consistency.py:203-207` 的 `_GO_KEY_CALL` 只取 `BindEnv` 的**第一个参数**，没有解析后面的 env 别名 ⇒ **假阳性**。
- 该步在 `.github/workflows/ci.yml:310-311`。core 今天已 0 ahead/0 behind，若 CI 真跑过，这次推送应是**红的**；这与「QT-61 CI 基线归零」的口径冲突。**假护栏比没护栏更糟**——它训练人忽略这条红。

**建议（不代裁）**

- 二选一：① 删掉 `cmd/strategy` 这一行并让 compose 真跑通（最小修复，立刻可验证）；② 明确 compose 只保留 core 两进程，六服务不进 compose，删掉误导性清单。
- 无论哪条，都要在 CI 加一条 `docker build -f Dockerfile.service .`（当前 CI 不构建镜像，所以这个问题能活这么久）。
- 另立小单修 `check_deploy_consistency.py` 的 `BindEnv` 别名解析（含一条破坏验证：把别名删掉 ⇒ 必须红），否则这条门禁不能当结论用。

---

### P1-1 · 契约边界不齐：两个服务仓零契约依赖，版本矩阵分叉

**实测**

- `tellurion-orchestration/go.mod` 与 `tellurion-broker/go.mod` **只有 pgx**，没有 `tellurion-contracts` / `tellurion-kit`。
- 同一组 wire 形状在两边各写一份：core `internal/orchestrationworker/client.go:93` 的 `ClaimedJob` vs orchestration 侧自己的 claim 结构；没有 contracts 包做唯一形状。
- 版本矩阵（2026-10-26 实测）：core `contracts v0.6.0` / `kit v0.1.1`；data `v0.6.0` / `v0.1.4`；agent `v0.6.0` / **kit v0.1.0**；risk `kit v0.1.4`；analytics `kit v0.1.4`；strategy `contracts v0.5.0`（无 kit）；kit 最新 **`v0.1.4`（已发版）**。

**代价**

- G6 的「形状只有一处」在这两个仓不成立；第一次改字段就会错位。
- service token 的 401 已随 kit `v0.1.4` 发版 + data/risk/analytics 升 pin **关闭**（2026-10-26 实测：三仓 `go.mod` 均 `v0.1.4`）。剩余的矩阵分叉是：core 仍 `v0.1.1`、agent 仍 `v0.1.0`、strategy 未引 contracts/kit。

---

### P1-2 · 执行 port 没闭合：回测仍直接 import `pkg/live`

**实测**

- `tellurion:pkg/backtest/engine.go:25`、`options.go:35`、`execution/live_bridge.go:37` 仍 import `tellurion/pkg/live`（非测试 5 处）。
- QT-51 仍在分支 `task/qt51-execution-port`（1 ahead / 12 behind），未合入；台账标 🔶。

**为什么算偏离**

- D1 的「先端口」在回测/执行这条边界上还没发生；这不是 X 线问题，是 core 内部解耦的基本盘。

---

### P1-3 · AI 实验域「停了写、没删表」：core 仍保留可写路径

**实测**

- agent 侧 QT-17 ④ 已停写 `ai.experiments`（`tellurion-agent:docs/TASKS.md`），这是对的。
- core 侧 `pkg/storage/experiments.go:108` 仍有 `INSERT INTO experiments`、`factor_hypothesis.go:35` 仍有 `INSERT INTO factor_hypothesis`；`factor_hypothesis` 的写方法**生产零调用方**（全仓 grep 只命中定义）。
- core `postgres.go` 仍创建这两张裸表（`experiments` / `factor_hypothesis`）。

**代价**

- 「已迁走」与「还能写」同时为真；下一个 agent 很可能把数据写回旧表，双写会复活。

---

### P1-4 · 产品主线仍未到 S1 出口，工程支线已跑到 S4

**实测**

- `tellurion-docs:docs/ROADMAP.md:37`：S1「通回路」仍是 ⏳ 进行中；出口判据是「无人干预连续跑完 100 次实验」。
- core `docs/TASKS.md` QT-32（AI 策略生成闭环）仍 ⬜；agent QT-32-B 仍「⬜ 待裁」。
- 同期 X 线六仓、k8s 清单、service token、provenance 读面都已推进。

**判断（这是审计意见，不是事实指控）**

- 这些工程件本身有价值（可复现留痕、服务凭证、读面语义都服务产品）；但当**产品出口未达**而**部署形态先行**时，投入结构就从「打破研究死锁」偏向「把平台搭得更像平台」。建议在下一轮排序里显式回答：X 线剩余动作是否暂停，直到 S1 出口达成。

---

### P2-1 · 长效文档漂移（已在本轮维护，见 §七）

| 文档 | 过期陈述 | 实测 |
|---|---|---|
| `tellurion-docs:docs/SYSTEM-DESIGN.md` §3 / §7 | 「agent 仍编译期依赖 core」「六服务只有占位 `doc.go`」「X1~X6 未触发」「S3 ⬜」 | agent 零 import core、无 `replace`；六仓各有 `cmd/*service` 与自有 schema |
| `tellurion-docs:docs/REPO-MAP.md` | contracts/kit「占位（S1 落代码）」；agent「过渡态：仍 replace」；六服务「占位（X1~X6）」；core「内嵌前端静态资源」 | contracts `v0.6.0`、kit `v0.1.4`（审计时读到 `v0.1.3`，已订正）已发版；agent S3 已落；六仓已实装；core 无前端 |
| `tellurion-docs:docs/ROADMAP.md` | 「当前阶段：S0 止血」与同文表格「S0 ✅ / S1 进行中」自相矛盾；last-verified 2026-09-16 | 产品当前阶段是 S1 |
| `tellurion-docs:docs/README.md` | :40「QT-60，未启动」；:45「ADR 现行（18 条）」；:55 首读顺序仍写 ARCHITECTURE | S-1 ✅；现行 ADR 30 条；ARCHITECTURE 已降级 |
| `tellurion-docs:docs/guides/local-dev.md:42` | 六服务「只有 go.mod + doc.go（占位），没有实现、不起进程」 | 六仓均已实装 |
| `tellurion-docs:docs/guides/data-dependencies.md:5,27` | `tellurion-data` 仍只有 `go.mod` + `doc.go` | 已实装 `cmd/dataservice` + 23 张 schema DDL |
| `tellurion-docs:docs/guides/deploy-config.md:46` | 六服务仓「今天都没有部署资产（占位仓，不起进程）」 | core `deploy/k8s/` 已有 risk/analytics 清单；六仓本身仍无 deploy/ |
| `tellurion:AGENTS.md:111` | 「其余能力服务尚未立项」 | 六仓已实装 |
| `tellurion:AGENTS.md:198,609-610` | 「共 24 条 ADR」「odr-001 ~ odr-065」 | 现行 ADR 30 条、ODR 67 条 |
| `tellurion:docs/design/qt-decoupling/README.md:3` | 「全部未实施」 | D5 / D31 等已实施 |

---

### P2-2 · 记录卫生

- core 工作区有 3 份**未提交**的审计文档改动（wave3 / 裁决记录 / 终版建议的状态订正）——它们是有效结论，但未入账；下一次 `git status` 会把它们和别人的 WIP 混在一起。
- 旧仓 `quant-trading` / `quant-trading-agent` 仍在本地工作区；旧 agent 的 `go.mod` 仍 `replace github.com/ruoxizhnya/quant-trading => ../quant-trading`。`repo-inventory.md:78` 写「观察一周再 archive」，今天已超过一周。

---

## 四、明确没跑歪的部分（避免用支线问题否定主线）

| 项 | 证据 |
|---|---|
| 契约冻结与执行正确性 | W0（QT-01~04）与 W1/W2（QT-10~16、QT-20~26）全部 ✅；QT-30/QT-31 有 Go↔WASM 逐位对拍 |
| contracts / kit 两库真实可用 | contracts `v0.6.0`（远端 tag）、kit `v0.1.4`（远端 tag，2026-10-26 订正；审计时为 `v0.1.3`）；core/data/agent 已 pin；`dataprotocol` 导出面 447→465 |
| agent 去 `replace`（S3） | agent `go.mod` 无 `replace`、无 core `require`；agent 内 `"tellurion/` import = 0 |
| 跨仓硬纪律 | 12 仓 `go.work` 均未跟踪；`replace` 仅存在于旧 `quant-trading-agent`（不在 12 仓） |
| 文档结构齐套 | `check_repo_docs.py`：12/12 齐套、缺件 0；`check_doc_links.py`：活跃层 223 条 / 0 断（2026-10-26 复跑；审计时 222） |
| 产品叙事 | PRODUCT 已按「三角色 + 一座底座 + 扩展舱」改写，并如实声明「六条扩展舱今天只有 data 真被消费」 |

---

## 五、账实不符补充（wave3 之后的新读数）

| # | 账上写的 | 实测 | 处置 |
|---|---|---|---|
| 1 | 「X 线是可选部署形态，进程不升」 | 6 个可执行服务入口（data / risk / strategy assetservice / strategy compiler / analytics / orchestration）+ risk/analytics k8s Deployment | 立 ADR 或撤清单（P0-1） |
| 2 | 「data 服务已拆到 tellurion-data」 | compose 的 `data-service` 仍构建 core `cmd/data`；`tellurion-data` 未进 compose | 明确「源码归属」与「部署归属」是两件事（P0-3） |
| 3 | 「六仓不等六进程」的 M5 结论 | 六仓均有服务入口；broker 除外 | 保留结论，但需要一条裁决处理既定事实 |
| 4 | 「DDL 唯一 owner」 | core 54 张 + 服务仓 47 张，至少 5 簇双 owner | 全量双 owner 清单（P0-2） |
| 5 | 「agent 仍编译期依赖 core」 | 已不依赖 | 本轮已改文档（§七） |
| 6 | 「六服务占位」 | 已实装 | 本轮已改文档（§七） |
| 7 | `docs/README.md` ADR「18 条」 | 现行 30 条 | 本轮已改（§七） |

---

## 五之二、退役任务核对（2026-10-26，回答「有没有计划删除迁移过的代码」）

| 迁移对象 | 既有任务 | 覆盖到「删除旧代码」吗 | 缺口 / 新增单 |
|---|---|---|---|
| `market` / `ingest` schema | data `X1d`（QT-40） | **部分**：写的是「消灭第二写者」，验收停在 schema/停写；没写 core `pkg/data` / `cmd/data` 代码删除 | 新增 `SVC-CORE-DATA-RETIRE` |
| `run` 任务域 | orchestration `X5b`（QT-44） | **是**（验收含 `pkg/backtest` 零命中），但未达成 | 保留 `SVC-CORE-RUN-RETIRE` 作总指针 |
| 策略资产 | strategy 仓无 core 退役单 | **否**：只有「core 零调用方」的现状描述 | 新增 `SVC-CORE-STRAT-RETIRE`（运行时 `pkg/strategy` 保留） |
| 风控规则源 | risk `SVC-RISK-G3`（新）+ QT-74 | **否**：之前只有「规则源外置」，没有「core 本地规则配置删除」 | 新增 `SVC-CORE-RISK-RETIRE`（裁决代码保留） |
| 指标 / 报告 | analytics 仓无 core 退役单 | **否** | 新增 `SVC-CORE-AN-RETIRE` |
| 交易通道 | broker `R4`（QT-45 子项） | **是**（登记了 core `LiveTrader` / `Broker` 退役），但未达成 | 保留 `SVC-CORE-BROKER-RETIRE` 作总指针 |
| AI 实验域 | agent `QT-17` ④ 停写 | **部分**：停写已做；core 写方法与裸表删除未立单 | 新增 `SVC-CORE-AI-RETIRE` |

**结论**：7 条迁移线里，**3 条已有明确退役任务**（market / run / broker），**4 条只有「迁出」没有「删除」**（strategy / risk / analytics / ai）；data 还缺「代码删除」这一半。ADR-036 已把「新服务上线必须带旧实现删除验收」写成硬约束；`SVC-RETIRE-INDEX` 是这 7 条的总单。

## 六、待裁 / 待立单（只列，不代裁）

> 按本仓规矩：**未立单不得开工**。下表是候选，不是任务。

| # | 事项 | 为什么必须主任拍 | 建议的判定点 |
|---|---|---|---|
| A | X 线既定事实怎么处理：补 ADR 承认 / 撤回进程与清单 / 维持但冻结 | ✅ **已由 ADR-036 处置**（2026-10-26）：六仓=服务，保留 `cmd/*service` 与各仓 `deploy/`；k8s 两份按记录 #5 删除 | 见 `cross-repo-decision-record-2026-10-26.md` §二 #5 |
| B | 双 owner 表清单与逐表四步计划 | ✅ **已裁 2026-10-26**：只做五列只读清单（run / strategy / risk / ai），market 按 ⑰ 继续 | 记录 §二 #2 |
| C | 部署路径归谁：compose 只跑 core 两进程，还是把六服务纳入 | ✅ **已由 ADR-036 处置**：`docker-compose.services.yml` 纳入六服务，作为本地唯一部署真相 | 记录 §三（已实测） |
| D | 产品 S1 出口与工程 X 线的排序 | ✅ **已裁 2026-10-26**：冻结 X 线新增，只收口已裁的 K1~K11 | 记录 §二 #1 |
| E | 旧仓 archive 与本地残留 | ✅ **已裁 2026-10-26**：先打 tag，不删；删除留到 S1 出口后 | 记录 §二 #3 |
| F | ~~kit `v0.1.4` 发版与三仓升 pin~~ | ✅ **已发生**（2026-10-25/26）：`v0.1.4` tag 已推；data/risk/analytics 已 pin `v0.1.4` | 剩余：core 仍 `v0.1.1`、agent 仍 `v0.1.0` ⇒ 并入记录 §二 #6 |

> **本节已清空**：A~F 全部有归处；本轮新发现（analytics `/healthz` 404、core k8s 两份过期清单、`check_deploy_consistency.py` 假阳性）已并入 `cross-repo-decision-record-2026-10-26.md` §二 #4 / #5 / #10。

---

## 七、本轮已做的文档维护（长效层）

| 文档 | 动作 |
|---|---|
| `tellurion-docs:docs/SYSTEM-DESIGN.md` | 按 DOC-10 订正 §3 依赖边、§5「今天实际」、§7 阶梯与过渡态；不改 §2 原则 / §6 永不拆 / §8 不变量 |
| `tellurion-docs:docs/REPO-MAP.md` | 订正状态列（contracts/kit/agent/六服务/core 对外面）与依赖边说明 |
| `tellurion-docs:docs/ROADMAP.md` | 修正「当前阶段」自相矛盾；补产品 S1 与工程阶梯两条线的现状与指针 |
| `tellurion-docs:docs/README.md` | 订正 QT-60 状态、ADR 条数、首读顺序（ARCHITECTURE → SYSTEM-DESIGN） |
| `tellurion-docs:docs/guides/{local-dev,data-dependencies,deploy-config}.md` | 订正「六仓占位 / data 仅 go.mod / 六仓无部署资产」三处过期陈述 |
| `tellurion-docs:docs/TASKS.md` | DOC-10 结案 + 新增 DOC-11（本轮现状对齐）结案记录 |
| `tellurion:AGENTS.md` | 订正「其余能力服务尚未立项」、ADR/ODR 条数；加本轮审计指针 |
| `tellurion:docs/design/qt-decoupling/README.md` | 「全部未实施」改为「部分已实施（以台账为准）」，并登记本篇 |
| `tellurion:docs/TASKS.md` | 增加「第四轮审计」块与候选单指针；不改既有单状态 |
| `tellurion-docs:docs/adr/adr-036-service-topology-and-readiness-gate.md` | **新增**：12 仓定位 + G0~G4 就绪门 + G0/G1/G2 并行、G3 切读串行 + 部署资产回服务仓 + risk 控制面/进程内裁决 + broker 服务化；修订 ADR-027 §2.1 / ADR-034 §S4 / ADR-035 §M5 / repo-inventory §1.2 / broker X6d |
| `tellurion:docs/TASKS.md`（第五轮） | 增加 `SVC-DEPLOY-01` / `SVC-DUAL-01` / `SVC-RETIRE-INDEX` 与六仓 G0/G3 指针；补退役任务核对（3 条已有、4 条缺删除） |
| 六个服务仓 `docs/TASKS.md` | 各加 `SVC-*-G0`（自有部署）、`SVC-*-G3`（切读/停写 + 旧实现退役）；orchestration/broker 另加 G1 |
| 六个服务仓 + core | 新增 `Dockerfile` / `deploy/compose.yaml` / `docker-compose.services.yml`；risk `:8089`、strategy `:8090`、broker `:8091` 去端口冲突；broker 新增 `cmd/brokergateway`（`/healthz`、`/version`）；core `Dockerfile.service` 删除已不存在的 `cmd/strategy` build/COPY 行 |

> 文档维护的判据：`python tools/check_doc_links.py` 活跃层 0 断；`python tools/check_repo_docs.py` 12/12 齐套。

---

## 八、方法与取证边界

- **方法：五层证据链**（本篇即按此逐条落证）：
  1. **目的层**：PRODUCT / VISION / ADR-023 / ADR-024；
  2. **决策层**：ADR-D1~D31 + 正式 ADR-027/032~035 + 三轮裁决记录；
  3. **计划层**：repo-inventory 的 S0~S3 / X 线前置、M5 对账、wave3 分组；
  4. **施工层**：12 仓 git log / 工作区 / 各仓 `docs/TASKS.md`；
  5. **运行层**：`docker-compose.yml`、`deploy/k8s/`、各仓 `cmd/*service/main.go` 的默认监听、core 的 HTTP 客户端与配置开关。
- **判据**：① 是否守住「不做」清单；② 是否出现两处真相；③ 是否有可运行证据；④ 文档与实测是否一致；⑤ 是否服务产品目的。
- **边界（原始轮，如实）**：本篇静态取证时本机 **Go 不在 PATH**、**Docker daemon 不可达**，未执行 `go build/vet/test`、未起服务、未连库；所有代码结论来自静态阅读与 grep。标「已实装」指**有源码与入口**，不等于「已编译通过」或「已部署」。凡台账写「全绿」但本轮未复跑的，本篇不引用为证据。
- **2026-10-26 同日复测（取代上一条的环境事实）**：Go **1.25.14** 已恢复（`%LOCALAPPDATA%\Programs\go1.25.14\bin`，已加入用户级 PATH）；Docker **Engine 29.8.0** 可达。已实测：**10 仓 `go build ./...` EXIT=0**；六服务镜像 + core `Dockerfile.service` + agent `Dockerfile` 全部构建成功；聚合栈 **5/6 healthy**（analytics `/healthz` 404，修复见记录 #4）。**未跑**：全量 `go vet` / `go test`、`-tags integration` 真库腿、ui e2e。

---

## 维护

- 新增发现：按 P0/P1/P2 追加，必须带 `repo:path:line` 与「实测 vs 账上」。
- 裁决落定：在本篇 §六 标「已裁 + 日期 + 裁决指针」，正文不改写历史。
- 本篇不是报告快照：它服务于台账与文档维护；过期后按新读数重写结论，不追加「当时如何」的叙述。

---

## 九、入库前二次复测订正（2026-10-26）

> 本轮把「工作树已改、台账未记」的读数逐项复测，只订正事实，不重写 §三 的历史判断。

| 审计原文 | 复测结果 | 证据 |
|---|---|---|
| P0-1：risk 默认 `:8086`、strategy `:8085`，与 ai-service / analysis-service 撞号 | **已消除**：`tellurion-risk:cmd/riskservice/main.go:30` = `127.0.0.1:8089`；`tellurion-strategy:cmd/assetservice/main.go:42` = `127.0.0.1:8090`（本轮入库） | 两文件实测 |
| P0-1：「broker 无 `cmd/`」 | **已不成立**：工作树有 `cmd/brokergateway/main.go`（`-addr 127.0.0.1:8091` :32、`/healthz` :45、`/version` :55），本轮入库 | `git status --porcelain` + 文件实测 |
| P0-2：core 内联 `CREATE TABLE` **54** 条 | 实测 **55** 条（文件 1569 行） | `pkg/storage/postgres.go` |
| P0-3：`Dockerfile.service` 仍 build `cmd/strategy` | **已在工作树修复并入库**：只 build `./cmd/data` 与 `./cmd/analysis` | `Dockerfile.service:24-25` |
| P1-1：orchestration / broker 无 contracts / kit | **仍成立**：两仓 `go.mod` 只有 pgx | `tellurion-orchestration/go.mod`、`tellurion-broker/go.mod` |
| P2-1：agent 已有 config / deploy | **不成立**：agent 无 `config/`、无 `deploy/`，`Dockerfile` 仍三处 `COPY --from=core` | `tellurion-agent/Dockerfile` |
| （未记）QT-51 分支落后数 | 实测 **1 ahead / 13 behind** `origin/main`，origin 无此分支 | `git rev-list --count` |
| （未记）12 仓工作树 | **9 脏 / 3 干净**（contracts、kit、ui 干净）；脏项均已于本轮分仓入库 | `git status --porcelain` |
| （未记）ADR 计数 | 文件实测：`docs/adr/` 31 份 + `docs/archive/superseded-adr/` 5 份 = **36**（001~036 全在）；不再现行 6 条（009/010/012 归档、014 由 020 §6 取代、021/022 由 023 取代）⇒ 现行 30 | `Get-ChildItem docs/adr`、`ADR.md` 状态列 |

**2026-10-26 复测结果（取代上句）**：Go **1.25.14** 已恢复（用户级 PATH）；Docker **Engine 29.8.0** 可达。已实测：

- **10 仓 `go build ./...` EXIT=0**（core 当前分支 + contracts / kit / data / strategy / risk / analytics / orchestration / broker / agent）；
- **六服务镜像全部构建成功**；core `Dockerfile.service` 与 agent `Dockerfile` 亦构建成功；
- **聚合栈 5/6 healthy**；`data /health`、`orchestration /healthz`、`broker /healthz` 均 200；
- **新发现**：`analytics /healthz` = **404**（任务验收写的是 200）；工作树修复后实测 200 且容器 healthy，已提交 `f010606`（记录 #4；整栈 6/6 未复跑）。
- **仍未跑**：全量 `go vet` / `go test`、`-tags integration` 真库腿、ui e2e。

**追加订正（同表行失效）**：「P2-1：agent 已有 config / deploy → 不成立」已被并发施工方 `cc51e84` 落实——agent 现有 `config/ai-service.yaml`、`deploy/compose.yaml`、`deploy/wait-for-deps.sh`，`Dockerfile` 不再有 `COPY --from=core`，镜像实构建成功。QT-51 分支已 rebase 到 origin/main（tip `e015a83`），当前 **1 ahead / 3 behind 本地 main**，见记录 #7。