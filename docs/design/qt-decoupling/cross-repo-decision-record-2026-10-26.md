---
status: active
last-verified: 2026-10-26
verified-by: 第六轮·待裁决清单清理（主任口径「全部按建议」，10 条）；同日补 Go 1.25.14 / Docker 29.8.0 实测
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
| 5 | core k8s risk / analytics 清单 | **删两份**；保留 `data-deployment.yaml`（端口一致） | `tellurion:deploy/k8s/` | ⬜ 待施工 |
| 6 | contracts `v0.7.0` 与 pin 矩阵 | risk 包落 main 后打 `v0.7.0`；同批 core / agent kit 升 `v0.1.4`，risk / core 升 contracts | contracts tag + 三仓 `go.mod` | ⬜ 待施工 |
| 7 | QT-51 合入 | **rebase 到本地 main**，跑全量门禁，ff merge；分支留 tag 作回滚点 | `tellurion:task/qt51-execution-port` | ⬜ 待施工 |
| 8 | `SVC-*` 立单 | 补 `SVC-DUAL-01` / `SVC-RETIRE-INDEX` 六字段；`SVC-DEPLOY-01` 记 🔶（不标 ✅） | `tellurion:docs/TASKS.md` | ✅ 两单六字段已落（`DEPLOY-01` 仍 🔶） |
| 9 | orchestration / broker 零 contracts | **本轮不做**；等各自 G1/G3 接线时一次接入 | 两仓 `go.mod` | ⬜ 延后 |
| 10 | `check_deploy_consistency.py` 假阳性 | **修 `BindEnv` 别名解析 + 破坏验证** | `tellurion:tools/check_deploy_consistency.py` | ⬜ 待施工 |

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
- **落点 / 状态**：`tellurion:deploy/k8s/`；✅ 已删两份（2026-10-26，`git status` 显示两条 `D`）；`check_deploy_consistency.py` 复跑 exit 0；`deploy/k8s/` 现 **8 份**（data / analysis / web / configmap / ingress / namespace / postgres-statefulset / redis-deployment）。

### 6 · contracts `v0.7.0` 与 pin 矩阵

- **事项**：contracts 缺 `risk` 包（裁决 ②）；core 仍 kit `v0.1.1`、agent 仍 kit `v0.1.0`。
- **裁决**：`risk` 包落 main 后打 `v0.7.0`；同批发版批次里 core / agent 升 kit `v0.1.4`，risk / core 升 contracts `v0.7.0`。
- **依据实测**：contracts 当前只有 A 组裁决材料（无 `risk` 包代码）；core `go.mod` kit `v0.1.1`；agent kit `v0.1.0`；data / risk / analytics 已 `v0.1.4`；strategy 仍 contracts `v0.5.0` 且无 kit。
- **落地动作**：先合 `risk` 包 → `git tag v0.7.0` → 逐仓 `go get` + `go mod tidy` → 跑 `make contracts-pin-check` 与各自门禁。
- **代价**：一次全量 build/test；建议与 QT-51 合入分开，避免混因。
- **落点 / 状态**：`tellurion-contracts`、`tellurion`、`tellurion-agent`、`tellurion-risk`；⬜ 待施工。

### 7 · QT-51 合入

- **事项**：执行 port 已在未合入分支落地（`git log` tip `e015a83`），Go 环境已恢复。
- **裁决**：**rebase 到本地 main**（当前 `49cfdd5`，不是 origin/main），跑 `gofmt -l .` / `go build ./...` / `go vet ./...` / `go test ./... -count=1`，全绿后 ff merge；分支留 `archive/qt51-execution-port` tag 作回滚点。
- **依据实测**：分支 `1 ahead`（origin/main `5b9dc11`）、本地 main 领先 origin 3 笔；本机已 `go build ./...` 通过（QT-51 工作树，EXIT=0），`vet` / `test` 本轮未跑。
- **落地动作**：`git rebase main` → 四道门禁 → `git merge --ff-only`；不重写第二种形态。
- **代价**：一次全仓测试；rebase 冲突集中在 `cmd/analysis` 与 `pkg/backtest`。
- **落点 / 状态**：`tellurion:task/qt51-execution-port`；⬜ 待施工。

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
- **落点 / 状态**：`tellurion:tools/check_deploy_consistency.py`；✅ 已修（2026-10-26）：exit 0；**破坏验证**（把 `config.go:25` 的 `JWT_SECRET` / `AUTH_JWT_SECRET` 别名删到只剩配置键）→ 两条假阳性复现、exit 1；逐字节还原后 exit 0。

---

## 三、环境前置（2026-10-26 实测，已解除）

| 项 | 实测 | 结论 |
|---|---|---|
| Go | `C:\Users\ruoxi\AppData\Local\Programs\go1.25.14\bin\go.exe` = go1.25.14；此前不在 PATH，已加入用户级 PATH | ✅ 10 仓 `go build ./...` EXIT=0 |
| Docker | Docker Desktop 4.92.0 / Engine 29.8.0 | ✅ daemon 可达 |
| 六服务镜像 | data / risk / strategy / analytics / orchestration / broker 全部 build 成功 | ✅ |
| core / ai 镜像 | `Dockerfile.service`（main）与 agent `Dockerfile` 均 build 成功 | ✅ |
| 聚合栈 | `up --wait` → 5/6 healthy；analytics `/healthz` 404（见 #4） | 🔶 修复后单容器已 healthy（`f010606`）；**整栈 6/6 未复跑** |
| 端口 | 5173 / 8085 / 8086 空闲 | ✅ AUD-61 ① 前置解除 |

---

## 维护本文件

- 新增裁决：写全六字段并在各仓台账留一行指针。
- 裁决被推翻：改本文件，保留「原裁决 → 推翻理由」的对照，不改历史记录。
- 引用格式：跨仓写 `repo:path`，不写可点击相对链接。
