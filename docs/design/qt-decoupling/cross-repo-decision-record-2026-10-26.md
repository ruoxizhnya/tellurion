---
status: active
last-verified: 2026-11-08
verified-by: 第六轮·待裁决清单清理（主任口径「全部按建议」，10 条）；同日补 Go 1.25.14 / Docker 29.8.0 实测。2026-11-08 补裁 #11~#13（K1 接口面对齐 / CT-06 迁回拆分 / 退役单前置订正三条，均按建议代裁、待主任追认）
---

# 第六轮裁决记录（2026-10-26）

> **来源**：第四轮目的对齐审计 §六 A~F + wave3 派发方案 §5 + 2026-10-26 环境复测的新发现。
> **已由别处处置、不再列**：A（X 线既定事实）与 C（部署归属）由 `ADR-036` 裁决（六仓=服务、compose 纳入六服务）；
> F（kit `v0.1.4` 发版）已发生。本记录只写剩余 10 条。
> **主任口径**：全部按 §二 的建议执行；本文件是裁决正文，各仓台账只留指针。

---

## 一、10 条裁决一览

| # | 事项 | 裁决 | 落点 | 状态 |
|---|---|---|---|---|
| 1 | 产品 S1 与 X 线排序 | **冻结 X 线新增**，只收口已裁的 K1~K11；新单等 S1 出口 | 各仓 G1/G3 排期 | ✅ 即刻生效（管理口径） |
| 2 | 双 owner 表清单 | **只做五列只读清单**（run / strategy / risk / ai），market 按 ⑰ 继续；不排全量四步计划 | `core-governance-decision-materials-2026-10-26.md` §2 | ⬜ 待施工 |
| 3 | 旧仓 archive | **先打 tag，不删**；删除留到 S1 出口后 | 4 个 `quant-trading*` 仓 | ⬜ 待施工 |
| 4 | analytics `/healthz` 缺失 | **补无鉴权 `GET /healthz` + 单测**，保留 Dockerfile / compose 健康检查 | `tellurion-analytics:cmd/analyticsservice` | ✅ 已提交 `f010606`（2026-10-26） |
| 5 | core k8s risk / analytics 清单 | **删两份**；保留 `data-deployment.yaml`（端口一致） | `tellurion:deploy/k8s/` | ✅ 已落（2026-10-26 `ecc4f2d`；`deploy/k8s/` 实测 8 份，无 risk / analytics 两份） |
| 6 | contracts `v0.7.0` 与 pin 矩阵 | risk 包落 main 后打 `v0.7.0`；同批 core / agent kit 升 `v0.1.4`，risk / core 升 contracts | contracts tag + 三仓 `go.mod` | ✅ 已施工（2026-11-08；pin 目标按建议前移到最新 `v0.9.0`，**待主任追认**） |
| 7 | QT-51 合入 | **rebase 到本地 main**，跑全量门禁，ff merge；分支留 tag 作回滚点 | `tellurion:task/qt51-execution-port` | ⬜ 待施工 |
| 8 | `SVC-*` 立单 | 补 `SVC-DUAL-01` / `SVC-RETIRE-INDEX` 六字段；`SVC-DEPLOY-01` 记 🔶（不标 ✅） | `tellurion:docs/TASKS.md` | ✅ 两单六字段已落（`DEPLOY-01` 仍 🔶） |
| 9 | orchestration / broker 零 contracts | **本轮不做**；等各自 G1/G3 接线时一次接入 | 两仓 `go.mod` | ⬜ 延后 |
| 10 | `check_deploy_consistency.py` 假阳性 | **修 `BindEnv` 别名解析 + 破坏验证** | `tellurion:tools/check_deploy_consistency.py` | ✅ 已修（2026-10-26 `ecc4f2d`；退出码见 §二 #10 的环境条件） |

---

## 二、逐条六字段

### 1 · 产品 S1 出口与 X 线新动作的排序

- **事项**：X 线已服务化，但 S1「无人干预连续跑完 100 次实验」未达，资源只有一份。
- **裁决**：冻结 X 线**新增**动作；只允许收口已裁的 K1~K11 与本批 G0 尾巴（analytics health、部署校验）。新单等 S1 出口达成。
- **依据实测**：`tellurion-docs:docs/ROADMAP.md` 的 S1 仍为进行中；六服务仓已实装但只有 data 一条读链被 core 真消费（第四轮审计 §三）。
- **落地动作**：各仓 `SVC-*-G1/G3` 不再开新单；S1 出口未达前，X 线不进下一阶段排期。
- **代价**：run / strategy / risk / ai 四簇双 owner 会继续存在；本批已覆盖 market 簇。
- **落点 / 状态**：管理口径，即刻生效；各仓台账只需保留本指针。

### 2 · 双 owner 表清单与逐表计划

- **事项**：market / run / strategy / ai 四簇 DDL 双 owner，risk 是「裁决权双真相」。
- **裁决**：只做**五列只读清单**（owner / 当前写者 / 读方 / 对拍方式 / 停写条件），覆盖 run / strategy / risk / ai；market 按裁决 ⑰ 继续；不排全量四步计划。
- **依据实测**：`core-governance-decision-materials-2026-10-26.md` §2 已有 55 行归属（32 行双 owner / 23 行 core 自有），逐行带 `repo:path:line`。
- **落地动作**：`SVC-DUAL-01` 的正文采用该 §2 的清单；只补「五列」中缺的对拍 / 停写条件。
- **代价**：清单不产生解耦，但可机械判定「旧写入点归零」。
- **落点 / 状态**：`tellurion:docs/TASKS.md` `SVC-DUAL-01`；⬜ 待施工。

### 3 · 旧仓 archive

- **事项**：`quant-trading` / `quant-trading-agent` / `quant-trading-docs` / `quant-trading-ui` 仍在本地。
- **裁决**：**先打 tag**（如 `archive/2026-10-26`），**不删**；删除留到 S1 出口后，届时另裁。
- **依据实测**：`C:\Users\ruoxi\Workspace` 下这四个目录仍存在（另有 `quant-trading-worktrees`）。
- **落地动作**：逐仓 `git tag archive/2026-10-26`；不执行 `rm` / 远端归档。
- **代价**：清点「12 仓」时长期多四项噪声。
- **落点 / 状态**：四个旧仓；⬜ 待施工（不可逆动作，仅打 tag 可逆）。

### 4 · analytics `/healthz` 缺失

- **事项**：`SVC-ANALYTICS-G0` 验收写 `GET /healthz` 200，但生产 HTTP 面没有该路由。
- **裁决**：补一个**无鉴权、只接 GET** 的 `/healthz`，并加单测；保留 Dockerfile / compose 的 HTTP 健康检查（不改 TCP 探针）。
- **依据实测**：聚合栈实跑 6 服务，5 healthy；analytics `curl /healthz` = **404**（容器日志显示已监听 `0.0.0.0:8087`）；工作树已有 `newMux` + `main_test.go`，同一镜像重建后 `curl /healthz` = **200**、容器 **healthy**。
- **落地动作**：提交工作树改动（已提交 `f010606`）；重跑 `docker compose -f docker-compose.services.yml up -d --build analytics` 复核 healthy（**单容器实测已 healthy；整栈 6/6 复跑未做**，归 `SVC-DEPLOY-01` 剩余腿）。
- **代价**：约 30 行代码 + 一条测试；不影响 8 个分析端点契约。
- **落点 / 状态**：`tellurion-analytics:cmd/analyticsservice/main.go`、`main_test.go`、`docs/TASKS.md`；✅ 已提交（2026-10-26，`f010606`）。

### 5 · core `deploy/k8s/` 的 risk / analytics 清单

- **事项**：ADR-036 已定 compose 为本地唯一部署真相、K8s 非必需；两份 k8s 清单仍把风险 / 分析写成容器进程，且 risk 端口过期。
- **裁决**：**删除** `risk-deployment.yaml` 与 `analytics-deployment.yaml`；保留 `data-deployment.yaml`（`:8081` 与现值一致）。
- **依据实测**：`risk-deployment.yaml` 写 `containerPort: 8086`，实际默认 `8089`；`analytics-deployment.yaml` 写 8087，与实际一致，但同属无升格 ADR 的容器清单。
- **落地动作**：删两份文件；`docs/guides/deploy-config.md` 同步为「六仓 compose 为准，k8s 仅 data 保留」。
- **代价**：失去「risk / analytics 可 k8s 部署」的旧证据；与 ADR-036 口径一致。
- **落点 / 状态**：`tellurion:deploy/k8s/`；✅ 已删两份（2026-10-26，`ecc4f2d`）；`check_deploy_consistency.py` 复跑 exit 0（**取该退出码需 `PYTHONIOENCODING=utf-8`，理由见 #10**）；`deploy/k8s/` 现 **8 份**（data / analysis / web / configmap / ingress / namespace / postgres-statefulset / redis-deployment，实测 `ls` 计数）。

### 6 · contracts `v0.7.0` 与 pin 矩阵

- **事项**：contracts 缺 `risk` 包（裁决 ②）；core 仍 kit `v0.1.1`、agent 仍 kit `v0.1.0`。
- **裁决**：`risk` 包落 main 后打 `v0.7.0`；同批发版批次里 core / agent 升 kit `v0.1.4`，risk / core 升 contracts `v0.7.0`。
- **依据实测**：contracts 当前只有 A 组裁决材料（无 `risk` 包代码）；core `go.mod` kit `v0.1.1`；agent kit `v0.1.0`；data / risk / analytics 已 `v0.1.4`；strategy 仍 contracts `v0.5.0` 且无 kit。
- **落地动作**：先合 `risk` 包 → `git tag v0.7.0` → 逐仓 `go get` + `go mod tidy` → 跑 `make contracts-pin-check` 与各自门禁。
- **代价**：一次全量 build/test；建议与 QT-51 合入分开，避免混因。
- **落点 / 状态**：`tellurion-contracts`、`tellurion`、`tellurion-agent`、`tellurion-risk`；✅ **已施工（2026-11-08）**，取证如下。
  - **发版与推送**：contracts annotated tag `v0.7.0` / `v0.8.0` / `v0.9.0` 与 `main` 已推远端——`git ls-remote origin refs/heads/main` = `eda04ae010be…`；三条 tag 的 peeled 提交分别是 CT-03 / CT-04 / CT-05 的落码提交。
  - **pin 目标按建议前移**：裁决原文写 `v0.7.0`；实际切到 **`v0.9.0`**（= v0.7.0 的超集：`risk` + `run` + `broker` 三个包都是**新增**，无删改签名），避免为 `run` / `broker` 再发一次 pin。**该前移待主任追认**。
  - **各仓实测**：core `contracts v0.6.0 → v0.9.0`；agent `contracts v0.6.0 → v0.9.0` + `kit v0.1.0 → v0.1.4`；risk `go.mod` **无 contracts 依赖**（实测只有 kit `v0.1.4`，已是目标版本）⇒ 无 pin 可切，裁决里「risk 升 contracts」这半条**落空**（不是漏做）；core kit 已是 `v0.1.4`，无需动。
  - **门禁**：`check_contract_pin.py` **GREEN**（11 个 pin 全是目标仓 `origin/main` 祖先，3 仓无 pin / 跳过）；core `gofmt -l .` 空 + `go build ./...` / `go vet ./...` / `go test ./... -count=1` 全 `exit 0`；agent 同四条门禁全 `exit 0`。
  - **未做**：data / strategy 不在本裁决点名范围，本轮不动（仍 `v0.6.0` / `v0.5.0`）；orchestration / broker 按裁决 #9 继续零 contracts。

### 7 · QT-51 合入

- **事项**：执行 port 已在未合入分支落地（`git log` tip `e015a83`），Go 环境已恢复。
- **裁决**：**rebase 到本地 main**（以合入当时的本地 main 为准，不是 origin/main；裁决当时是 `49cfdd5`，本批落账后已到 `0617f54` —— 别照抄哈希），跑 `gofmt -l .` / `go build ./...` / `go vet ./...` / `go test ./... -count=1`，全绿后 ff merge；分支留 `archive/qt51-execution-port` tag 作回滚点。
- **依据实测**：分支与 main 的差 = **branch-only 1 / main-only 10**（2026-10-26 本批落账后，`git rev-list --left-right --count main...task/qt51-execution-port` 现跑；本地 main `0617f54`、origin/main `5b9dc11`，`git rev-list --count origin/main..main` = 10）。同一天先后记过 3 / 7 / 10 —— **本批自己的三笔文档提交就把它从 7 推到 10**，所以这两个数只许现跑，不许引用。本机 `go build ./...` 已通过（QT-51 工作树，EXIT=0），`vet` / `test` 未跑。
- **落地动作**：`git rebase main` → 四道门禁 → `git merge --ff-only`；不重写第二种形态。⚠️ **两条实测补充（2026-10-26 再复测）**：① 分支 tip `e015a83` 未变、**真冲突只有 `docs/TASKS.md` 一处**（两侧改动交集 3 个文件全是文档，Go 侧零冲突；取证见 wave3 K10 ③）；② 本机 Go 1.25.14 可用但**不在默认 Git Bash 的 PATH**，跑门禁前先 `export PATH="/c/Users/ruoxi/AppData/Local/Programs/go1.25.14/bin:$PATH"`。
- **代价**：一次全仓测试；~~rebase 冲突集中在 `cmd/analysis` 与 `pkg/backtest`~~ → **该估计已被实测证伪**：Go 侧零冲突，文档侧只有 `docs/TASKS.md` 一处真冲突（另两处自动合并）。
- **落点 / 状态**：`tellurion:task/qt51-execution-port`；✅ 已施工（2026-11-07：rebase 到本地 `main@b3f100e` → 唯一冲突 `docs/TASKS.md`（保留 main 侧）→ ff merge `a2e388a`；四道门禁真退出码全 0（`ok`=70 / `FAIL`=0）；等价性摘要 `60a2713b…`（trades=70 / pv=120 / port_calls=0）与归档报告同值；回滚点 tag `archive/qt51-execution-port` 指 `e015a83`；破坏验证真做真还原，`sha256=CC6DF55F…`）。取证与未落项见 `docs/TASKS.md` QT-51 结案记录。

### 8 · `SVC-*` 立单状态

- **事项**：三张总单与七条退役 ID 原先只有表格行，按「未立单不得开工」不可派发。
- **裁决**：补 `SVC-DUAL-01` / `SVC-RETIRE-INDEX` 六字段（正文见 `docs/TASKS.md`）；`SVC-DEPLOY-01` 记 **🔶**，不标 ✅。
- **依据实测**：`Dockerfile.service` 修复已入库；聚合 compose `config -q` 通过；六服务镜像 + core service 镜像 + ai 镜像均构建成功；聚合栈实跑 5/6 healthy（analytics 404 见 #4）；ai 镜像本轮已补构建成功。
- **落地动作**：台账写六字段；`SVC-DEPLOY-01` 的剩余腿写成「analytics health 提交 + 复跑聚合栈 healthy」。
- **代价**：文档工作量；不做则七条退役线无法开工。
- **落点 / 状态**：`tellurion:docs/TASKS.md`；✅ 两单六字段已落（§六），`SVC-DEPLOY-01` 仍 🔶。

### 9 · orchestration / broker 零 contracts 依赖

- **事项**：两仓 `go.mod` 只有 pgx，wire 形状各写一份。
- **裁决**：**本轮不做**；等各自 G1/G3 真正接线时一次接入 contracts，避免为「将来」提前建形状。
- **依据实测**：两仓 `go.mod` 无 contracts / kit；core 侧 `ClaimedJob` 与编排侧 claim 结构各一份。
- **落地动作**：在两仓 G3 单的约束里写明「接线时同时引 contracts，不另建本地形状」。
- **代价**：第一次改字段会错位；风险已知且被冻结窗口覆盖。
- **落点 / 状态**：`tellurion-orchestration`、`tellurion-broker`；⬜ 延后。

### 10 · `check_deploy_consistency.py` 假阳性

- **事项**：检查器只取 `BindEnv` 第一个参数，误报 `JWT_SECRET` 无读点，CI 部署门禁变红。
- **裁决**：修别名解析（解析 `BindEnv` 的 env 别名参数）+ 一条破坏验证（删别名 ⇒ 必须红）。
- **依据实测**：`tellurion:pkg/bootstrap/config.go:25` 有 `BindEnv("auth.jwt_secret", "JWT_SECRET", "AUTH_JWT_SECRET")`；检查器 `tools/check_deploy_consistency.py:203-207` 只取第一个参数。
- **落地动作**：改 `_GO_KEY_CALL` 的别名解析（新增 `_GO_BINDENV_CALL` 收 `BindEnv` 全部大写别名实参）；跑一次全量门禁。
- **代价**：小；不修则 CI 门禁不可信。
- **落点 / 状态**：`tellurion:tools/check_deploy_consistency.py`；✅ 已修（2026-10-26，`ecc4f2d`）：**破坏验证**（把 `config.go:25` 的 `JWT_SECRET` / `AUTH_JWT_SECRET` 别名删到只剩配置键）→ 两条假阳性复现、exit 1；逐字节还原后 exit 0。
  ⚠️ **「exit 0」有环境条件（2026-10-26 复测）**：本机的 GBK 控制台（cmd 默认 code page）会让它在打印 `✓` 那一行抛 `UnicodeEncodeError` ⇒ **exit 1**，内容其实全绿。取结论前必须 `export PYTHONIOENCODING=utf-8`（那样实测 exit 0，9 条 `✓` + 1 条已登记的 `ai-service k8s 未部署` 提示）；CI 在 ubuntu/UTF-8 上不受影响。**别把这条编码假红读成门禁红，也别在无 UTF-8 的环境下引用「exit 0」。**

---

## 三、环境前置（2026-10-26 实测；**各仓引用环境口径以本节为准，别再各写一份**）

> **为什么要这一节**：各仓台账里今天并存着两套相反的环境结论——`tellurion-data:docs/TASKS.md` 与
> `tellurion-analytics:docs/TASKS.md` 都用「本机无 Go 工具链 ⇒ 验收证据 = CI 退出码」当作不复跑的理由，
> 而 `tellurion-orchestration:docs/TASKS.md` 记的是「5432 未监听 ⇒ PG 腿 skip」。两段都写于 2026-10-25/26，
> 结论却互相打脸。**统一实测如下**，凡与之冲突的行都要订正而不是照抄。

| 项 | 实测（2026-10-26 本批复测） | 结论 |
|---|---|---|
| Go | `C:\Users\ruoxi\AppData\Local\Programs\go1.25.14\bin\go.exe` = **go1.25.14**，可执行。**但默认 Git Bash 的 PATH 里没有它**：`command -v go` 空，必须先 `export PATH="/c/Users/ruoxi/AppData/Local/Programs/go1.25.14/bin:$PATH"`（本节原文曾写「已加入用户级 PATH」，那条对本 agent 的 shell 不成立） | ✅ 门禁**可在本机复跑**；「本机无 Go」从此不许当前置 |
| Go 门禁实跑（data 一仓） | `gofmt -l .` 空；`go build ./...` / `go vet ./...` / `go test ./... -count=1` 全 **EXIT=0**，`-v` 下 **4 条 SKIP**（全是 `TestPG_BulkInsert_*`） | ✅ 只证 data；其余 9 仓的「build EXIT=0」是上一批的结论，本批未复跑，按仓各自现跑 |
| `-race` | `go env CGO_ENABLED` = 0；`go build -race ./internal/schema` **EXIT=2**（`-race requires cgo`），本机无 C 编译器 | ⛔ race 腿归 CI，不许写「本机 -race 全绿」 |
| PostgreSQL | `netstat -ano` 里 **5432 零条监听**；容器 `odh-test-pg` `Exited (255) 4 weeks ago` | ⛔ 本机无库 ⇒ 所有 `*_TEST_DSN` 腿**只能 skip**；skip ≠ 通过 |
| Docker | Docker Desktop 4.92.0 / **Engine 29.8.0 可达**；`docker ps -q` = **0 个在跑**，`docker ps -aq` = 5 个（`quant-trading-{analysis,data,ai,strategy}-service-1` + `odh-test-pg`，全部 `Exited (255)`） | ✅ daemon 可用；⚠️ 「无容器」要说清是「没有在跑的容器」，不是「没有容器」 |
| 六服务镜像 | data / risk / strategy / analytics / orchestration / broker 全部 build 成功 | ✅（上批结论，本批未复跑） |
| core / ai 镜像 | `Dockerfile.service`（main）与 agent `Dockerfile` 均 build 成功 | ✅（上批结论，本批未复跑） |
| 聚合栈 | `up --wait` → 5/6 healthy；analytics `/healthz` 404（见 #4） | 🔶 修复后单容器已 healthy（`f010606`）；**整栈 6/6 未复跑** |
| 端口 | 5173 / 8085 / 8086 空闲 | ✅ AUD-61 ① 前置解除 |
| Python 门禁 | 取 `check_deploy_consistency.py` 这类带 `✓` 输出的检查器退出码，**必须先 `export PYTHONIOENCODING=utf-8`**；GBK 控制台下它打印时抛 `UnicodeEncodeError` ⇒ **EXIT=1 假红**（见 #10） | ⚠️ 退出码要先确认不是编码假红 |

---

## 四、2026-11-08 补裁（第 6 轮续，按建议代裁，**待主任追认**）

> 授权来源：主任「跨仓裁决类按建议代裁并落档」。本节三条都**不新增架构方向**，只把既有材料里的建议
> （材料 §5.2 K1、`tellurion-contracts:docs/TASKS.md` CT-06 ③②、CT-03 ④/CT-04/CT-05 施工记录）落成可执行的裁决，
> 并订正被 2026-11-08 实测证伪的前提。状态一律 `✅ 口径生效 / ⬜ 施工待排`，不把裁决当完工。

| # | 事项 | 裁决 | 落点 | 状态 |
|---|---|---|---|---|
| 11 | K1：切读接口面（core 5 条旧读面 vs data 4 条路由） | **以 data 仓 4 条路由为唯一数据服务面**；core 的 5 条旧读面**不由 data 补齐**，在 `SVC-DATA-G3` 逐条处置（可映射的改反代，不可映射的随退役删） | data `docs/TASKS.md` SVC-DATA-G1 ④⑥ / SVC-DATA-G3；core `docs/TASKS.md` SVC-CORE-DATA-RETIRE ⑥ | ✅ 口径生效（代裁，**待主任追认**）；施工归 G3 |
| 12 | CT-06：61 个「仅 1 使用方」根名 | **按原建议迁回**（core 60 / agent 1 `BuiltinHypothesisFor`），拆两笔跨仓单 `CT-06-A`（agent，先做）/ `CT-06-B`（core，按「别名 / 实类型」分类）；本轮**只裁决与拆分，不施工** | contracts `docs/TASKS.md` CT-06 | ✅ 口径生效（代裁，**待主任追认**）；⬜ 施工待排 |
| 13 | 退役单三条前置订正（实测证伪） | 见 §四.13：`Bundle/ParseBundle` 不迁契约（CT-03 ④ 已裁）、run pin 前置已解除、CT-05 tag 已推 | core `docs/TASKS.md` 三条 RETIRE ④；contracts CT-05 施工记录 | ✅ 已订正（**待主任追认**） |

### 11 · K1 切读接口面对齐

- **事项**：core 的 5 条旧读面 `/ohlcv/{symbol}` / `/api/v1/ohlcv/bulk` / `/screen` / `/market/index` / `/factors/{name}` 与 data 仓 4 条路由不对齐（材料 §5.2 K1、§2.6 ①）。
- **裁决**：**以 data 仓已登记的路由为唯一数据服务面**（业务面 `/datasets/resolve`、`/datasets/{hash}`、`POST /ingest/points`，加 `/health`、`/readyz` 两条探针）。data **不补齐** core 的 5 条旧面；core 在 `SVC-DATA-G3` 切读时逐条处置：语义可映射的改成对 data 的反代，映射不了的随 `SVC-CORE-DATA-RETIRE` 删除。
- **依据实测**：① data 的接口面已按 G1 五件（版本 / 错误码闭合 / 鉴权 / 幂等 / 分页限流）定型，并有 `/readyz` 三态与真库腿（`tellurion-data:docs/TASKS.md` SVC-DATA-G1 施工记录）；② `SVC-DATA-G1` 的 ④ 本来就按「不实现 5 条旧面」施工，今天的数据面**没有**这些形状；③ 补齐旧面 = 把 core 的旧形状固化进新服务，与 ADR-036「新仓只承接已登记形状」冲突。
- **代价**：`SVC-DATA-G3` 里 `/factors/{name}` 与 `/screen` 两条若找不到 data 侧等价面，core 侧只能删或留在 core（届时在 G3 单内逐条定，不许默认补齐）。
- **落点 / 状态**：data `docs/TASKS.md` SVC-DATA-G1 ④⑥ + SVC-DATA-G3；core `docs/TASKS.md` SVC-CORE-DATA-RETIRE ⑥。**代裁，待主任追认**。

### 12 · CT-06 单使用方根名的迁回拆分

- **事项**：CT-06 已删 22 个 0 使用方根名；余 61 个「仅 1 使用方」（core 60 + agent 1）卡在「迁回须改 core/agent，超出只改本仓边界」。
- **裁决**：**按 CT-06 ③② 的原建议执行迁回**，不保留在 contracts（C4 准入规则不放宽）。拆两笔：`CT-06-A`（agent 的 `BuiltinHypothesisFor`，单仓单点，先做）；`CT-06-B`（core 60 个，按「兼容别名 / 实类型」分类——别名类只在 core 建同形别名，身份不变；实类型类才物理搬家并同批改签）。每笔 = contracts 删符号 + 使用方本地定义 + `CHANGELOG` 记 breaking + `api-surface.txt -update`。
- **依据实测**：`tellurion-contracts:docs/API.md` §6.1 的逐名清单（带 `repo:path:line`）；`CheckApiSurface` 实测删除后 `domain/` 根包 107 符号 / 97 根名。
- **代价**：`CT-06-B` 触及 core 大量调用点（`OHLCV` 795 命中、`Order` 147 命中），必须按别名优先做，否则会把同形类型拆成两份真相。
- **落点 / 状态**：`tellurion-contracts:docs/TASKS.md` CT-06 ⑥ + 施工记录。**代裁，待主任追认**；本轮不施工。

### 13 · 退役单三条前置订正（2026-11-08 实测）

1. **`SVC-CORE-RISK-RETIRE` ③③**：原文要求「契约侧 `contracts/risk` 的 `Bundle → ParseBundle` 必须有测试（跨仓单 `CT-03`）」，与 CT-03 ④ 已裁的「`Bundle` / `ParseBundle` / `ForScope` **不迁**契约」直接冲突。**订正**：`ParseBundle` 留在 risk 服务，契约侧只固定 `Snapshot` 的形状与 hash 向量（`risk/snapshot_test.go` 已落）；本退役单的剩余前置 = risk 服务侧 `bundle → Snapshot` 的对外面（`SVC-RISK-G3`）+ core 的 F3 接线。
2. **`SVC-CORE-RUN-RETIRE` 的 CT-04 跨仓指针**：原文写「前置 = contracts tag 推远端后升 pin（本机纪律只 commit 不 push ⇒ 今天**未执行**）」。**订正**：`v0.8.0` / `v0.9.0` 已于 2026-11-08 推远端，core 已 pin `v0.9.0` ⇒ 该前置**已解除**；剩余 = `internal/orchestrationworker` 的 claim / 父作业 DTO 改用 `contracts/run`（本单客户端腿）。
3. **`CT-05` 施工记录**：原文写「`v0.9.0` tag 未 push，`go get …@v0.9.0` 解析不到 ⇒ broker 切 `go.mod` 会不可构建」。**订正**：tag 已于 2026-11-08 推远端；`SVC-BROKER-G1` 已 ✅ ⇒ broker 删本地形状改引 `contracts/broker` 的**前置已全部解除**，可开工（判据仍是「同一条报文快照改前 / 改后逐字节一致」）。

---

## 维护本文件

- 新增裁决：写全六字段并在各仓台账留一行指针。
- 裁决被推翻：改本文件，保留「原裁决 → 推翻理由」的对照，不改历史记录。
- 引用格式：跨仓写 `repo:path`，不写可点击相对链接。
