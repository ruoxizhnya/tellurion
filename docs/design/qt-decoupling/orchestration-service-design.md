# 回测编排/Worker 服务设计（X5 · Orchestration Service）

> ADR-D22 草案 · 2026-10-19 · 关联 ADR-D18（算力出口）、BE-03（编排层混进能力层）、ADR-D11（E2 清内核）
> 性质：设计固化，未实施

## 1. 服务定位

**回测任务的编排、调度与算力出口。批量扫描打满时加 worker，而不是拆内核。**

| | 内容 |
|---|---|
| **管** | 任务队列（job）、批量扫描（batch）、前向验证（walkforward）、worker 池调度、断点续跑协调、三 hash 落结果（X0） |
| **不管** | 回测执行语义（撮合/成交/风控——归内核）、数据分析呈现（归 X4）、数据供给（归 X1） |

这就是 ADR-D18 里「批量扫描算力打满」的**正确出口**：内核保持进程内确定性执行，编排层横向加 worker 扩容。BE-03 已登记：job/batch/walkforward 是**应用编排**，不该住在 `pkg/backtest` 能力层里——摘出即修掉。

## 2. 现状零件（实测）

| 零件 | 位置 | 说明 |
|---|---|---|
| `JobService` + **`JobStore` 接口** | `pkg/backtest/job/job.go:22/56` | Create/Update(Started/Completed/Failed)/Get/List/Delete —— **port 雏形已现** |
| `JobRecord` / `CreateJobRequest` / `Job` | 同上 `:68/181/209` | 任务模型（Params/Result 均 JSONB） |
| `BatchEngine` + `BatchTask/Result/Score/Report/Summary/Config` | `pkg/backtest/batch/batch.go` | 批量扫描 |
| `Scorer`（batch 评分） | `pkg/backtest/batch/batch_scorer.go` | 批量结果排序 |
| `WalkForwardEngine` + `WalkForwardRequest` / `wfWindow` | `pkg/backtest/walkforward/walkforward.go` | 前向验证（多窗口） |
| `NewBatchEngine(runner contracts.EngineRunner, ...)` | `batch.go:114` | **已按 port 注入引擎**（不 import 具体 Engine）——摘出成本低 |
| `backtest_jobs` 表（Migration 009） | `pkg/storage/postgres.go:295` | 任务队列持久化 |
| `walk_forward_reports` 表（Migration 010） | 同上 `:268` | 前向验证报告 |
| workers（并行基建） | `pkg/backtest/workers/` | worker 池雏形 |

**关键事实**：`BatchEngine` 构造函数已接受 `contracts.EngineRunner` 接口而非具体引擎（注释明写"打破被迫 import 父包的环"）——这正是摘出的现成接缝，BE-03 的技术债在编排层已经解了一半。

## 3. 数据归属（schema = `run`，DDL 唯一 owner）

| 表 | 内容 | 现状 |
|---|---|---|
| `run.backtest_jobs` | 任务队列（状态机：queued→running→done/failed） | 从 `backtest_jobs`（无归属 schema）迁入 |
| `run.walk_forward_reports` | 前向验证报告 | 从 `walk_forward_reports` 迁入 |
| `run.batch_runs` | 批量扫描批次 + 评分汇总 | 待建 |
| `run.run_artifacts` | 每次 run 的三 hash 留痕（`strategy_ref` / `dataset_snapshot_hash` / `risk_rules_hash`）+ 明细 artefact 指针 | **待建（X0 载体）** |
| `run.worker_registry` | worker 注册与心跳 | 待建（多 worker 时） |

**归属修正**（ADR-D19 已登记）：~~`backtest_jobs` / `strategy_state`~~ `backtest_jobs` 目前在内核侧持久化，**迁到 `run`**，内核走 port——否则"内核无持久化状态"不成立。**（⚠️ 2026-10-21 订正（H5 裁决，`tellurion:docs/design/system-spec-review-2026-10-21.md`）：`strategy_state` 不迁 `run`——归 strategy-runtime 模块（K0 冻结契约 `contracts/kernel_modules.schema.sql`），core 继续写 `quant.strategy_state`。）**

**内核侧新增 port**（~~供断点状态等~~ 仅 job 队列；断点状态不经本仓，见下注）：

```go
// contracts
type RunStateStore interface {          // strategy_state / BacktestState 的抽象
    Save(ctx, runID string, state []byte) error
    Load(ctx, runID string) ([]byte, error)
}
type JobQueue interface {              // 现 JobStore 接口外置
    Create(ctx, job JobRecord) error
    UpdateStarted/Completed/Failed(...)
    Get / List / Delete
}
```

> ⚠️ **2026-10-21 订正（H5 裁决）**：`RunStateStore` 以「`strategy_state` 迁 `run`」为前提，随裁决**作废**——`strategy_state` 归 strategy-runtime 模块（K0 冻结契约），core 继续写 `quant.strategy_state`，不经本仓 port；`JobQueue` 不受影响。

## 4. API 面

```
# 任务
POST /api/runs/jobs                提交单次回测（幂等键）     → job_id
GET  /api/runs/jobs/{id}           查状态/摘要结果
DELETE /api/runs/jobs/{id}         取消
GET  /api/runs/jobs?status=&limit= 列表

# 批量 / 前向
POST /api/runs/batch               批量扫描（参数网格）        → batch_id
GET  /api/runs/batch/{id}          汇总 + Scorer 排序
POST /api/runs/walkforward         前向验证（窗口配置）        → wf_id
GET  /api/runs/walkforward/{id}    各窗口报告

# 算力
GET  /api/runs/workers             worker 池状态
POST /api/runs/workers/{id}/drain  摘除 worker（滚动升级）

# 结果坐标（X0）
GET  /api/runs/{id}/provenance     三 hash + 引擎名 + seed
```

- 提交是**异步**语义（job 队列），符合「应用层编排」而非同步内循环；
- `provenance` 是 X0 的对外读口：任何结果可回答"跑的是哪份策略/数据/规则"。

## 5. 与内核的边界

```
编排服务 ──(EngineRunner port / HTTP 粗粒度)──▶ 内核（进程内执行）
   │                                              │
   ├── JobQueue / RunStateStore（run schema）       └── 零持久化表
   └── worker 池（水平扩容点）
```

- 编排**调**内核（`EngineRunner.RunBacktest`），内核不感知编排存在；
- ~~内核的断点续跑状态经 `RunStateStore` port 读写（实现可先同库，将来可换）；~~ **（⚠️ 2026-10-21 H5 裁决作废：断点续跑状态 `strategy_state` 归 strategy-runtime 模块、core 写 `quant.strategy_state`，不经本仓 port；上图 `RunStateStore` 同此。）**
- worker 扩容判据：队列积压 + CPU 常态化打满 → 加 worker 进程（每个 worker 内嵌内核装配，同参），**不是**把内核拆服务。

## 6. 双引擎在这里天然生效

编排层通过 `contracts.EngineRegistry` + 能力路由（BE-14 待补的能力声明）选择 go / nautilus 内核——批量扫描可以混跑两引擎，dualrun K15 对拍也由编排层发起。

## 7. 分期

| 阶段 | 内容 | 验收 |
|---|---|---|
| O0 | `run` schema 建立 + `backtest_jobs`/`walk_forward_reports` 迁入 + `JobQueue` port 外置 | 内核不再直接写任务表 |
| O1 | `run_artifacts` 三 hash 记入（**X0 落点**） | 任一结果可查 provenance 并复现 |
| O2 | worker 池化 + 水平扩容验证 | 压测下加 worker 线性提速 |
| O3 | 编排层整体移出 `pkg/backtest`（修 BE-03） | `pkg/backtest` 只剩内核（contracts/execution/tracker/metrics/auction/state） |

## 8. 风险

| 风险 | 兜底 |
|---|---|
| 任务表迁移丢数据 | 双写 → 对拍 → 切读 → 停写（与 ai schema 迁移同法） |
| worker 间结果不一致 | 同参装配 + 三 hash 对拍 + dualrun |
| 编排与内核版本漂移 | worker 与内核同仓同版发布；`provenance` 记内核版本 |
| 队列表成为第二真相源 | `run` schema DDL 只归编排服务，红线同 ADR-D19 |
