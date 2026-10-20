# 仓库清单与命名（现行唯一名字源）

> 建立：2026-10-20 ｜ 状态：**已裁决（用户 2026-10-20）**｜ 关联：[ADR-D29](repo-topology-and-naming.md)（拓扑与阶梯）→ 本篇（名字与清单）
> **本文件是仓名与 module path 的唯一真相源。** 其他文档里出现的 `quant-trading*` 属于「现状描述」或历史记录（如 D29 正文、实测行号），**目标态一律以本表为准**。

---

## 0. 两条命名规则（先看这个）

| 类型 | module path | 判据 | 例 |
|---|---|---|---|
| **应用 / 服务**（不该被任何仓 import） | **单段短名**（第一段不含点） | Go 会拒绝 `require`，**编译器强制边界** | `tellurion` · `tellurion-agent` · `tellurion-risk` |
| **库**（必须能被跨仓依赖） | `github.com/ruoxizhnya/<仓名>` | 要被 core / agent / 服务 `require` | `github.com/ruoxizhnya/tellurion-contracts` |

owner 固定为 `ruoxizhnya`（用户明确：**不建 org、不改 owner**，避免引用名变更引发连锁错误）。

### 0.1 为什么应用用短名：实测依据（Go 1.25.0）

| 实验 | 结果 |
|---|---|
| `module tellurion`，单仓内 `import "tellurion/pkg/b"` | ✅ `go build` / `go mod tidy` / `go vet` 全绿；`go list` → `tellurion/pkg/a` |
| 另一个 module 直接 `require tellurion v0.0.0`（无 replace） | ❌ `malformed module path "tellurion": missing dot in first path element` |
| core `module tellurion` + `require github.com/ruoxizhnya/tellurion-contracts` + replace | ✅ build 绿、`go run` 正常 —— **本仓短名 + 跨仓全名可以共存** |
| 省略 module path：`import "contracts/kit"` | ❌ `package contracts/kit is not in std` —— **module path 不能省略，「短」只能靠 module path 本身短** |
| 相对 import：`import "./b"` | ❌ `relative import paths are not supported in module mode` |

**这意味着**：把 core 的 module path 定为 `tellurion`，等于让编译器替我们守住 D24 的「agent 编译期不依赖 core」——任何人想 import 一个 core 的包，都会在 `require` 阶段被拒。

### 0.2 一个必须写进 AGENTS.md 的坑

按**仓地址**去依赖 core 时：

```
require github.com/ruoxizhnya/tellurion v0.0.0
```

实测报错是 `package tellurion/pkg/b is not in std` —— **完全看不出是「module path 与仓地址不一致」**，会让人误以为是 GOPATH/标准库问题。

→ core 的 `AGENTS.md` 必须写死一句：**`tellurion` 是应用，不可被任何仓 import；要复用能力走 contracts。**

---

## 1. 十二个仓（现有 4 + 待建 2 + 触发式 6）

| # | 状态 | GitHub 仓 | module path | 职责 |
|---|---|---|---|---|
| 1 | **现有→改名** | `ruoxizhnya/tellurion` | `tellurion` | core：回测引擎 / 策略运行时 / 撮合 / 组合结算 / 仪器域存储 / 内核装配 |
| 2 | **现有→改名** | `ruoxizhnya/tellurion-agent` | `tellurion-agent` | 实验员（AI 链路）；运行期只走 HTTP 调 core |
| 3 | **现有→改名** | `ruoxizhnya/tellurion-ui` | —（前端，无 `go.mod`） | 前端 |
| 4 | **现有→改名** | `ruoxizhnya/tellurion-docs` | —（纯文档） | 决策 / 规范 / ADR |
| 5 | **待建（S2）** · 本地占位已建 | `ruoxizhnya/tellurion-contracts` | `github.com/ruoxizhnya/tellurion-contracts` | port · DTO/值对象（含 `domain/`）· 四契约（`engine/`）· 哨兵错误 · abi 常量 · `schemas/` |
| 6 | **待建（S2）** · 本地占位已建 | `ruoxizhnya/tellurion-kit` | `github.com/ruoxizhnya/tellurion-kit` | logging · db · errors · decimal · statistics · httpclient · httpserver · authz · observability · clock · sandbox |
| 7 | **触发式** · 本地占位已建 | `ruoxizhnya/tellurion-data` | `tellurion-data` | X1 数据服务（`cmd/data` + `pkg/marketdata` + `pkg/data/*`） |
| 8 | **触发式** · 本地占位已建 | `ruoxizhnya/tellurion-strategy` | `tellurion-strategy` | X2 策略资产（定义 / 版本 / artifact）；运行时留 core |
| 9 | **触发式** · 本地占位已建 | `ruoxizhnya/tellurion-risk` | `tellurion-risk` | X3 风控规则真相源；`RiskPolicy` 同步裁决留 core |
| 10 | **触发式** · 本地占位已建 | `ruoxizhnya/tellurion-analytics` | `tellurion-analytics` | X4 分析（`cmd/analysis` + 归因 / 基准纯函数） |
| 11 | **触发式** · 本地占位已建 | `ruoxizhnya/tellurion-orchestration` | `tellurion-orchestration` | X5 编排 / worker（`pkg/backtest/batch` + `walkforward`；`cmd/dualrun` 归属待裁） |
| 12 | **触发式** · 本地占位已建 | `ruoxizhnya/tellurion-broker` | `tellurion-broker` | X6 交易网关（`pkg/live` + 实盘侧） |

**触发条件不变**（ADR-027 / D20 口径）：需要独立伸缩或独立部署，且只走高 / 低频路径；逐 bar、逐笔、风控裁决**永驻 core 进程内**。**未触发不建仓**。

**服务仓也用短名的理由**：服务之间只通过 contracts 交互（D29 §9），不该互相 import。短名让这条纪律由编译器执行；若某个服务将来确实要对外提供 client 库，再单独拆一个 `tellurion-<x>-client`（库，全名）出来，而不是把服务本身变成库。

---

### 1.1 建仓执行进度（2026-10-20）

| 项 | 状态 |
|---|---|
| 12 个仓的**本地目录** | ✅ 全部就绪，各有一笔初始 commit（4 个迁移仓含全部代码；8 个占位仓含 README/AGENTS/go.mod/doc.go） |
| 12 个仓的**远端创建 + push** | ✅ **已完成**（2026-10-20 14:10）：12/12 仓在 `ruoxizhnya` 下创建，`main` 分支已推送。过程中遇到 GitHub 二级限流（连续建 10 仓后被 `temporarily blocked from content creation`），最后 2 仓由 `finish-two-repos.sh` 退避重试补上 |
| 复用脚本 | `Workspace/push-tellurion-repos.sh`（幂等：已存在则跳过创建，remote 已存在则改写）——以后加新仓可直接复用；`finish-two-repos.sh` 是限流退避版，可弃 |
| 旧仓 archive | ⏸ **待确认**：新仓已可独立工作，但 archive 会让旧仓变成只读；建议再观察一周（等 S0 的 tag 与 CI 稳定）再归档 |

> 占位仓的含义：**有仓、有边界、无实现**。README 写明触发条件，AGENTS 写明「台账出现本仓单号前不要写实现」，避免 agent 进去后凭空造代码。

## 2. 旧 → 新（改名批次的输入）

| 旧仓 | 新仓 | module path 变化 | 机械改动量（实测） |
|---|---|---|---|
| `quant-trading` | `tellurion` | `github.com/ruoxizhnya/quant-trading` → **`tellurion`** | 1153 处 import / 526 文件 |
| `quant-trading-agent` | `tellurion-agent` | `github.com/ruoxizhnya/quant-trading-agent` → **`tellurion-agent`** | 268 处 / 37 文件（引用 core） |
| `quant-trading-ui` | `tellurion-ui` | — | 文档链接 |
| `quant-trading-docs` | `tellurion-docs` | — | 文档链接 + 跨仓引用前缀 |

**执行方式（用户口径：新建仓 + 搬，不在旧仓大改）**：

```bash
git clone <old> <new>                     # 历史 100% 保留（archive / 拷目录都会丢 blame）
cd <new> && git remote set-url origin <new-url>
go mod edit -module <new-module-path>
# 全仓 import 替换 + 文档跨仓引用替换
go build ./... && go vet ./... && go test ./...
git commit -m "chore: 改名到 tellurion" && git push -u origin main
```

旧仓在 GitHub **archive（不删）**，本地保留 2 周只读，用于对照。旧仓**不打 tag**（tag 在新仓打，见 QT-06）。

**四个 404 已验证**：`ruoxizhnya/tellurion`、`-agent`、`-ui`、`-docs` 均未被占用，可直接创建。

---

## 3. 已裁决的边界（不再讨论）

| 项 | 裁决 |
|---|---|
| `quant-data-engine` | **不属于本项目**，不动、不改名、不纳入清单 |
| `nt-sidecar`（core 内 Python 子目录） | **现在不拆**；将来若独立，名字定为 **`tellurion-nt`**（Python，符合「语言不同才拆仓」判据） |
| `quant-trading-worktrees/` | 目录容器，不是仓 |
| `contracts/` 顶层数据契约文件 | core 内目录，不是仓；S1 迁入 contracts 仓的 `schemas/`（见 QT-05） |
| `nautilus_trader` / `vnpy` / `claudeer` / `gdpr-preferenc` | 上游或其他项目，不属本清单 |

---

## 4. 与 ADR-D29 的关系

| D29 的内容 | 本篇的处理 |
|---|---|
| §1 三条概念（repo / module / service） | **不变** |
| §2 五条不变量 R1~R5 | **不变** |
| §3 拓扑与依赖边（谁依赖谁、agent→core 只走 HTTP） | **不变** |
| §4 命名规则（`quant-trading-<能力>`、`github.com/ruoxizhnya/<仓名>`） | **被本篇 §0 替代**：项目段改 `tellurion`；新增「应用短名 / 库全名」二分 |
| §7 S0~S3 阶梯、§8 建仓 checklist、§9 跨仓 C1~C5 | **不变**（只是把其中的仓名读作本篇的名字） |

D29 正文保留旧名，作为 2026-10-20 之前的历史记录，不再逐处改写。

---

## 5. 落地与前置

| 项 | 说明 |
|---|---|
| 工单 | **QT-60（S-1 改名批次）**，排在 S0 之前，当前**未启动**（用户要求先落文档） |
| 为什么排在 S0 之前 | QT-06 要打 `v0.1.0`，tag 必须打在**新仓**；先打后改 = tag 指向一个即将 archive 的仓 |
| 为什么排在 QT-05 之前 | QT-05 要在 `pkg/domain` 上做 306 处路径替换；先改名再抽 module，同一批路径只改一次 |
| 私有仓前提 | contracts / kit 拆出去后是私有仓，消费方（core / agent / CI）必须配 `GOPRIVATE=github.com/ruoxizhnya/tellurion-*` + git 凭证，否则 `go get` 会走 proxy 直接失败（**此前未登记，QT-07/QT-19 需补**） |
| 文档引用 | 所有 `quant-trading:docs/...` 形式的跨仓引用要改 `tellurion:docs/...`（`check_doc_links` 查不出这类纯文本，属易漏项） |
