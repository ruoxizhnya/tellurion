---
status: active
last-verified: 2026-11-07
verified-by: QT-81 首次落地（2026-11-07：新增 `cmd/recon` + `internal/recon`，单测与只读自证全绿；**本机无 PG，真库夹具腿与文档里所有样例命令均未执行**，如实登记）
---

# 表级 writer 对拍（`cmd/recon`）

> 任务单：`tellurion:docs/TASKS.md` **QT-81**。首次落地 2026-11-07。
> 服务的判据：`SVC-*-G3`（切读/停写）与六条 `SVC-CORE-*-RETIRE` 的「对拍报告归档」。
> **它不裁决「谁该停写」**——那是 `SVC-DUAL-01` 的事；它只回答「两侧今天到底同不同」。

## 1. 为什么有它

2026-10-26 的材料实测（`core-governance-decision-materials-2026-10-26.md` §2.6 ②）：
data / orchestration / strategy / risk / analytics 五簇**没有任何** `*diff*/*verify*/*compare*/*recon*/*backfill*` 命令，
broker 只有账户快照对账（不是表级），**只有 agent 有 5 件**。⇒ 停写判据里那句「对拍报告」在此前**无可执行载体**。

## 2. 落点为什么是 Go 的 `cmd/recon`（而不是 `tools/*.py`）

二选一在本单内定死：

- **Go**：可直接复用本仓已 `require` 的 `jackc/pgx/v5`（`go.mod:9`），流式读大表、`context` 超时、
  PG 错误码（`42P01` 表不存在 / `42501` 权限不足）都是一等公民；
- **Python 零依赖拿不到 PG 驱动**：`tools/` 同族的 `check_*.py` 全是静态文本检查，没有 DB 连接；
  走 `psql` 子进程需要额外安装且解析输出脆弱（Windows 上尤其）。
- 代价：它是本仓第一个「连库的一次性命令」；只读约束必须由代码自证（§4），不能靠人记。

## 3. 用法

```bash
export RECON_LEFT_DSN='postgres://core:***@127.0.0.1:5432/tellurion'
export RECON_RIGHT_DSN='postgres://data:***@127.0.0.1:5433/tellurion'

go run ./cmd/recon -pair public.stocks=data.stocks
go run ./cmd/recon -pair public.stocks=data.stocks -pair public.ohlcv_daily_qfq=data.ohlcv_daily_qfq -json
```

- `-pair schema.a=schema.b`：两侧不同名；`-pair schema.t`：两侧同名。可重复。
- `-timeout`（默认 `60s`）：**单表**读取超时；超时记 `timeout`，不折成相等。
- `-json`：整份 `Report` 落 stdout，便于归档到 `docs/test-cases/`。
- **退出码**：`0` = 全部相等；`1` = 有差异（含 `table_missing` / `permission_denied` / `timeout`）；`2` = 用法或连接错误。

输出列：`LEFT / RIGHT / STATUS / ROWS(L/R) / MISSING / EXTRA / MISMATCH / COLUMN_DIFFS`，
下方按表追加 `Error`（带 `left:` / `right:` 侧别）与 `Notes`（列集不同 / 主键不同）。

## 4. 只读与「不做假相等」

| 保证 | 实现 | 自证用例 |
|---|---|---|
| 只发 SELECT | `assertReadOnlySQL` 在**每次**构建语句时校验；含 `INSERT/UPDATE/DELETE/ALTER/DROP/TRUNCATE/CREATE/GRANT/REVOKE/COPY/VACUUM/REINDEX/MERGE` 或非 `SELECT` 即拒绝执行 | `internal/recon/recon_test.go` 的 `TestAssertReadOnlySQL_RejectsWrites` + `TestReadOnly_GeneratedStatements`（表名 `update_log`、列名 `deleted_at` 不得误伤） |
| 表不存在 ≠ 0 行相等 | `Snapshot.Exists=false` 与 SQLSTATE `42P01` 都走 `table_missing`，独立状态 | `TestCompare_TableMissingIsNotZeroRowsEqual`（**本工具唯一重要的反证**） |
| 权限不足 ≠ 相等 | SQLSTATE `42501` → `permission_denied` | `TestCompare_PermissionDenied` |
| 超时 ≠ 相等 | `context.DeadlineExceeded` → `timeout` | `TestCompare_Timeout` |
| 无主键不静默降级 | 主键为空 → `ErrNoPrimaryKey`（**不**退回「只比行数」） | `PGReader.Read` 的显式分支 |

内容口径：逐行 `md5(row_to_json(t)::text)` 按主键归集，三分类 = 缺行（左有右无）/ 多行（右有左无）/ 同键异值；
列校验和 = `md5(string_agg(md5(coalesce(col::text,'__NULL__')), '' ORDER BY <pk>))`（排序保证跨库稳定）。

## 5. 七条退役线的样例命令

> **执行状态（如实）**：本机 `127.0.0.1:5432` 无监听、Docker 引擎不可用 ⇒ **下面每条都没有在本机跑过**。
> 表名取自 `core-governance-decision-materials-2026-10-26.md` §4.x，未做任何输出伪装。

### 5.1 `SVC-CORE-DATA-RETIRE`（22 张 market/ingest 表，左右两侧都存在）

```bash
go run ./cmd/recon \
  -pair public.stocks=data.stocks -pair public.ohlcv_daily_qfq=data.ohlcv_daily_qfq \
  -pair public.trading_calendar=data.trading_calendar -pair public.dividends=data.dividends \
  -pair public.index_constituents=data.index_constituents -pair public.splits=data.splits \
  -pair public.realtime_quote=data.realtime_quote -pair public.ohlcv_minute=data.ohlcv_minute \
  -pair public.capital_flow=data.capital_flow -pair public.sectors=data.sectors \
  -pair public.stock_sector_map=data.stock_sector_map -pair public.top_list=data.top_list \
  -pair public.limit_up_pool=data.limit_up_pool -pair public.announcements=data.announcements \
  -pair public.news=data.news -pair public.hot_search=data.hot_search \
  -pair public.global_ohlcv=data.global_ohlcv -pair public.market_activity=data.market_activity \
  -pair market.index_daily=data.index_daily
```

⚠️ 材料 §4.1 的 22 张里，`market.index_daily` / `ingest.raw` / `sync_jobs` / `sync_schedules` 的
**schema 归属**要按 `SVC-DUAL-01` 的五列清单核一遍再抄：本例只写了确定的一处（`market.index_daily`），
其余三张若在 `ingest` schema，改成 `-pair ingest.raw=data.ingest_raw` 这种两段式（工具只接受 `schema.table`）。

### 5.2 `SVC-CORE-RUN-RETIRE`（`backtest_jobs` / `walk_forward_reports`）

**今天无两侧可比的表**：orchestration 的 `run.walk_forward_reports` **无生产写者**
（`tellurion-orchestration:docs/TASKS.md:57` 自证），`run` 侧没有可对拍的行 ⇒ 命令留白，
等 `SVC-ORCH-G2` 落写者后再补。

### 5.3 `SVC-CORE-STRAT-RETIRE`（`strategies`）

**今天无两侧可比的表**：两侧不是同一张表的两个副本——core 的 `strategies`（配置行）与
strategy 服务的 `strategy.strategy_definitions` + 版本链是**不同形状**，行级对拍没有意义；
该线的停写判据是「唯一写者」而不是「行相等」（见材料 §4.3 前置）。

### 5.4 `SVC-CORE-RISK-RETIRE`

**表级 0 行**（材料 §4.4 原文：本单不是表退役，是裁决权收口）⇒ 无对拍对象。

### 5.5 `SVC-CORE-AN-RETIRE`

**无直接表**：产出落 analytics 的 `analysis.*`，且 `analysis.benchmarks` 写方法生产零调用
⇒ 今天没有两侧都有的表可对拍。

### 5.6 `SVC-CORE-BROKER-RETIRE`

**无表可比**：`orders` 的 DDL 只在**不被执行**的 `migrations/*` 与 `contracts/*.schema.sql` 里
（`migrate()` 不建 `orders`）⇒ core 侧没有 `orders` 行。

### 5.7 `SVC-CORE-AI-RETIRE`（7 张表，core `public.*` vs agent `ai.*`）

```bash
go run ./cmd/recon \
  -pair public.experiments=ai.experiments \
  -pair public.factor_hypothesis=ai.factor_hypothesis \
  -pair public.factor_genes=ai.factor_genes \
  -pair public.strategy_genes=ai.strategy_genes \
  -pair public.research.profile=ai.profile \
  -pair public.research.conclusion=ai.conclusion \
  -pair public.research.question=ai.question
```

⚠️ **右侧 schema 名与表名未在本单核实**（`research.profile` 这类是 schema=research、表=profile；
agent 侧对应名以 `tellurion-agent:pkg/aistore` 的 DDL 为准）。跑之前先按实际名替换，别照抄。

## 6. 限制（如实继承）

- **不覆盖动态拼表名**（材料 §2.4 的同一限制）。
- 大表成本：逐行摘要要全表扫一遍；列校验和要按主键排序（`string_agg ... ORDER BY`）。
  对千万级表建议先 `-timeout` 放宽，或在低峰跑——**工具不会自动分片**。
- 不做跨版本语义换算（如 `ohlcv_daily_qfq` 的复权口径差异）——它只回答「字节是否同」，不解释「为什么不同」。
- 不改任何 DDL、不建表、不写审计：它**不是**常驻服务，也不是迁移器。
