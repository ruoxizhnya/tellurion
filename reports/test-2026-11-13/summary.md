---
status: archived
date: 2026-11-13
scope: tellurion 12 仓综合测试（隔离栈 tellurion-next，8 波次 W0~W7）
owner: 综合测试执行者
source: docs/COMPREHENSIVE-TEST-PLAN-2026-11-13.md §7
---

# 综合测试执行摘要（2026-11-13）

> **性质**：对 `docs/COMPREHENSIVE-TEST-PLAN-2026-11-13.md` 执行结果的**蒸馏**。判据原文、
> 逐条结论与计划口径订正以计划文档 §7 为准；本文件只保留波次计数、阻塞原因、修复复跑
> 结论与原始日志对号表，**不复制日志正文**。
>
> **环境**：隔离栈 `tellurion-next`（pg + 8 服务 + `orchestration-worker`），隔离 PG
> `127.0.0.1:15432`；宿主部署库 `tellurion` 全程**未连接**（宿主 5432 只做只读观测）。

## 1. 总览

| 波次 | PASS | FAIL | BLOCKED | KNOWN-DEBT | 一句话结论 |
|---|---|---|---|---|---|
| W0 安全门 / 重建镜像 / 起栈 / 账本 | 6 | 0 | 0 | 0 | 9 容器 healthy；账本 85 行，两次重启 `max(applied_at)` 不变 |
| W1 L0 静态 + L1 单元 | 4 | 0 | 1 | 0 | F1 已修（`edb1c6b`）后 `go test ./cmd/analysis` 与 `tools/check.sh --clean` 均 exit 0；`-race` 腿 BLOCKED |
| W2 L2 契约与一致性 | 2 | 0 | 0 | 0 | 双侧 conformance + 5 条 contracts 护栏全绿 |
| W3 L4 单服务 E2E / 端点矩阵 | 3 | 0 | 0 | 0 | 4 条必需 e2e 真跑真过；退役端点 404/410 符合 |
| W4 L5 跨服务业务场景 | 5 | 0 | 0 | 0 | F2 已修（`b656ebc`，根因是隔离覆盖层 worker 库指向）；读面四端点 200；4.5 由 `abf26ea` 修复 |
| W5 UI Playwright + 视觉基线 | 2 | 0 | 0 | 0 | 最终 **218 passed / 0 failed / 2 skipped**；视觉基线 **12/12**；原 7 条债务由 UI 侧清零 |
| W6 L6 非功能 | 7 | 0 | 1 | 0 | F4 已修（`a35389b`）：201 → 503（无上游原文）→ 201；租约回收时序腿 BLOCKED |
| W7 对齐门 / 破坏验证 / 收口 | 2 | 0 | 0 | 0 | `dualrun -gate` exit 0；两处「改坏必红 → 还原复绿」成立 |
| **合计** | **31** | **0** | **2** | **0** | 无 FAIL、无 KNOWN-DEBT；两个 BLOCKED 均为**环境/时序不可构造**，不是产品缺陷 |

## 2. 两个 BLOCKED（准确原因与解除条件）

**B1 · `-race` 腿（W1）**
- 原因：本机 Windows 宿主无 gcc/cc（`Get-Command gcc`、`cc` 均 not found），`CGO_ENABLED=1`
  的 `go test -race` 编译不出来 —— 是**未执行**，不是 race 失败。
- 覆盖：CI `go` job（`ubuntu-latest` 自带 gcc）跑
  `go test ./... -count=1 -race -coverprofile=coverage.out` + `coveragegate`，即本机缺失腿的权威覆盖。
- 解除：本机装 gcc（MSYS2/mingw-w64）后 `CGO_ENABLED=1 CC=gcc go test -race ./...`，
  或直接采用 CI 同 job 的 run 结论；解除后把本节与计划 §7.1 的 W1 BLOCKED 改 0。

**B2 · 租约回收时序腿（W6，计划内可选条件项）**
- 原因：隔离栈 orchestration 按默认 `-worker-lease 60s` / `-reap-interval 30s` 启动，
  「过期 → 重领 → succeeded」链路最早约 90s 且需第二 worker（或恢复后的 worker）接力；
  本轮观察窗内未注入 `docker pause` / 双 worker 故障，凑不出完整链路，故按计划约定记
  BLOCKED(时序)，**不伪造通过**。
- 解除：用短参数重启 orchestration（`-worker-lease 5s -reap-interval 2s`），`docker pause`
  worker 停止续租，确认 `lease_until` 过期且 reaper 重排后 unpause / 起第二 worker 重领；
  判据是作业最终 `succeeded` 且**只产出一份结果**。或由产品提供可暂停时钟 / 故障注入钩子。

## 3. 缺陷修复后的定点复跑结论

| 缺陷 | 修复 commit | 复跑动作 | 结论 | 原始证据 |
|---|---|---|---|---|
| F1 core 护栏与隔离栈配置互斥 | `edb1c6b` | `go test ./cmd/analysis`；`GOFLAGS=-p=1 TEST_DATABASE_DSN=… sh tools/check.sh --clean` | 均 exit 0（`ok` / `✅ check 通过`） | `w1-f1-check-sh-rerun-final.log` |
| F2 编排完成态 run 读面断裂 | `b656ebc` | 新建 async job（`600000.SH`，`2024-01-01..03-31`）→ 轮询编排 → core 四读面 | 编排 `completed`；report / trades / equity / provenance 全 **200**；analytics metrics 200、attribution 409（缺 `factor_exposures`，域前置） | `f2-async-readface.log`、`f2-job-id.txt` |
| F3 UI 用例日期超夹具日历 | `83226ec` | 重装夹具（日历 297 行，至 `2024-07-31`）→ W5 全量 | `api-backtest` 全绿；409 连锁红清零 | `w5-fixture-load.log`、`w5-playwright-final.log` |
| F4 broker 故障被归成 400 | `a35389b` | 在线下单 → `docker stop broker` → 下单 → `docker start` → 下单 | **201 → 503**（`{"code":"unavailable"}`，无 `no such host`/`connection refused`/`dial tcp` 原文）**→ 201** | `f4-broker-failure.log`、`f4-order.json` |
| F5 视觉基线红面大于登记 | `d91c489` | W5 全量视觉基线 | **12/12 passed**，连跑两轮一致 | `w5-playwright-final.log` |
| F6 内置策略无 active version | `abf26ea` | 重建 strategy 镜像 → 删 `strategy` schema 模拟零状态 → 起容器 → 重启 | 零状态 14/14/14、active=14；`resolve` 200 且 hash 稳定；重启 `seeded count=0`、计数不变 | `f6-zero-state-seed.log`、`f6-restart-idempotent.log` |
| 断言口径（回测结果页） | `0857e74` | W5 回测结果页 | `max_drawdown`/`calmar` 按 `omitempty` 契约通过 | `w5-playwright-final.log` |

> 关键前提：F6 必须**重建镜像**后验证（此前 8090 跑的是不含种子代码的旧镜像，靠数据修复
> 短暂变绿属假绿）；F4 复跑同样基于含 `a35389b` 的新镜像。

## 4. `baseline.dump` —— 存在、来源与处置

- **存在**：`reports/test-2026-11-13/baseline.dump`（约 200 KB，`pg_dump -Fc` 自定义格式）。
- **来源（按计划 §W0.6 的命令原文）**：
  `docker exec tellurion-next-pg-1 pg_dump -U postgres -d tellurion -Fc` → `docker cp` 回
  证据目录。即**隔离栈 PG 容器**（`tellurion-next-pg-1`，宿主端口 `127.0.0.1:15432`）里
  名为 `tellurion` 的库的快照；宿主 5432 的部署库本轮**未被连接、未导出**。
- **口径说明**：转达意见称其为「宿主部署库快照」，与上述命令/容器映射不一致；此处按计划
  可核对的命令如实记录，定性（含是否删除、是否可用于还原）**留待主任裁决**。
- **处置**：**不入库、不删除**。文件保留在原路径，`git status` 中保持未跟踪；本摘要即其
  存在与来源的书面记录。

## 5. 原始证据对号表（同目录，均不入库）

| 波次 | 文件 |
|---|---|
| W0 | `w0-build.log`、`w0-up.log`、`w0-rowcounts.txt` |
| W1 | `w1-core-unit.log`、`w1-core-db-tests.log`、`w1-core-cover.log`、`w1-core-structural.log`、`w1-check-sh.log`、`w1-siblings.log`、`w1-contracts-guards.log`、`w1-agent-compat.log`、`w1-ui.log`、`w1-f1-check-sh-rerun.log`、`w1-f1-check-sh-rerun-db.log`、`w1-f1-check-sh-rerun-db-p1.log`、`w1-f1-check-sh-rerun-db-p1eq.log`、`w1-f1-check-sh-rerun-final.log`、`w1-f1-rerun.log`、`w1-f1-rerun-db.log` |
| W2 | `w2-contracts.log` |
| W3 | `w3-core-e2e.log` |
| W4 | `w4-worker-up.log`、`f2-async-readface.log`、`f2-job-id.txt`、`f2-metrics-req.json`、`f2-attr-req.json` |
| W5 | `w5-vite.log`、`w5-fixture-load.log`、`w5-playwright.log`（首轮）、`w5-playwright-final.log`（终轮）、`copilot-req.json`（复现 `api-strategy.spec.ts` 的 `prompt` 载荷问题） |
| W6 | `w6-drill.log`、`f4-broker-failure.log`、`f4-order.json`、`f6-zero-state-seed.log`、`f6-restart-idempotent.log` |
| W7 | `w7-dualrun.log`、`w7-sidecar.log`、`w7-sidecar2.log`、`dualrun.md`（K15 指标对齐报告） |
| 快照 | `baseline.dump`（见 §4） |

## 6. 遗留与口径提醒

- W5 的 `2 skipped` 是 `auth-login-flow` 的 state1 JWT 两条：本栈 state3 开放鉴权，按设计不跑。
- 异步状态查询的真实入口是编排 `GET /api/runs/jobs/{id}`；core **没有** `/api/backtest/status/{id}`
  （`e2e/tests` 里旧轮询助手已按 `CORE-E2E-FAIL-LOUD` 删除）。
- `-race` 与租约回收两条解除后，需同步把计划 §7.1 对应波次的 BLOCKED 改 0 并补证据文件。
