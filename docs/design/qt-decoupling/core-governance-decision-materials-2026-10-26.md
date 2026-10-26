---
status: active
last-verified: 2026-10-26
verified-by: B 组·core 治理决策材料（core + 七服务仓只读取证：`git grep` 逐表计数 / DDL 底数 / compose config / deploy 一致性检查 / 本轮真实跑过 go build+vet+test；**未改任何代码、DDL、pin 与服务仓**）
---

# core 治理 · B 组决策材料（2026-10-26）

> **状态：决策材料（decision-material），不可执行。**
> 本文件**不含实现**：没有改 `pkg/storage/postgres.go`，没有改任何服务仓，没有改 `contracts` / `kit` pin，
> 没有新建/删除任何表或索引。它的用途只有一个 —— 把台账里只有一行总览的 B 组五单（部署归属、
> 双 owner 清单、索引退役、七条退役线、组内顺序）**补齐到「主任可以逐条拍板」的粒度**，
> 让后续派工能写得出六字段正文（AGENTS.md §8.5）。
>
> 来由：`docs/TASKS.md` `:20-24` 的总览表 + 审计 §五之二「3 条已有退役任务、4 条只有迁出没有删除、
> data 缺代码删除那一半」（`purpose-alignment-audit-2026-10-26.md`）+ wave3 派发 §5 待裁 B/C
> （`cross-repo-dispatch-plan-wave3-2026-10-26.md`）。
>
> 取证：2026-10-26，core 工作区 `main`（HEAD `4f44dd6`，2 ahead of `origin/main`），工作树干净。
> **所有计数都可用本文件给出的命令复跑**；数字与命令不一致时以命令为准。

---

## 0. 取证口径与门禁基线

### 0.1 与 wave3 §0 的订正（重要）

wave3 派发文档 §0 与审计 §八写「本机无 Go 工具链，未跑 build/vet/test」。**该结论已不成立**：
本机存在 Go 工具链，本轮已实跑三条门禁。

| 项 | 值 |
|---|---|
| Go | `C:\Users\ruoxi\AppData\Local\Programs\go1.25.14\bin\go.exe`，`go version go1.25.14 windows/amd64`（2026-10-26 已加入用户级 PATH） |
| Docker CLI / daemon | `docker compose` 可用；**Engine 29.8.0 可达**（2026-10-26 复测） |
| Python | 可用（`tools/*.py` 可跑） |

**结论（已随 2026-10-26 复测更新）**：「build 已验、run 已验一半」——六服务镜像 + core `Dockerfile.service` + agent `Dockerfile` 均构建成功；聚合栈实跑 5/6 healthy（`analytics /healthz` 404，见裁决记录 #4）。剩余未验：全量 `go vet/test`、真库集成腿、ui e2e。

### 0.2 三条门禁基线（本轮真实输出）

```powershell
$env:PATH = 'C:\Users\ruoxi\sdk\go1.25.0\bin;' + $env:PATH
go build ./...        # exit 0
go vet ./...          # exit 0
go test ./... -count=1  # exit 0；ok=70 / FAIL=0 / 无测试包=3
```

`go test` 明细（日志 `%TEMP%\core_test_b.log`，**73 行**；`ok=70 / FAIL=0 / 无测试包=3`）。
真实抽样（原样粘贴，未改写）：

```
ok  	tellurion/cmd/analysis	3.693s
ok  	tellurion/pkg/storage	0.926s
ok  	tellurion/pkg/strategy/plugins	0.185s
?   	tellurion/pkg/sync/types	[no test files]
ok  	tellurion/pkg/testenv	0.124s
ok  	tellurion/tools/coveragegate	0.097s
```

> 本角色（决策材料）**不产生代码**，因此「破坏验证」按派发原文记为 **不适用**（无实现可破坏）。
> 但本文件对**每一条退役判据**都要求了一个「改坏应红」的反向护栏（见 §4），那是开工单的验收内容，
> 不是本文件的验收内容。

### 0.3 core 的模块口径（后文反复引用）

| 事实 | 值 | 取证 |
|---|---|---|
| module path | `tellurion`（短名，应用不可被 import） | `go.mod:1` |
| contracts pin | `v0.6.0` | `go.mod:15` |
| kit pin | `v0.1.1` | `go.mod:16` |
| `replace` 指向仓外 | 0 条 | `Select-String go.mod '^replace'` 无命中 |
| `go.work` / `go.work.sum` | 均不存在 | `Test-Path` = False |

**本文件未动上面四项中的任何一项**（守住「A 组发版前不改 contracts / kit pin」的禁区）。

---

## 1. SVC-DEPLOY-01 · 部署归属

### 1.1 现状（实测，不是推断）

| 工件 | 路径 | 现状 |
|---|---|---|
| core 单仓多进程镜像 | `Dockerfile.service` | `:24` build `/app/data-service` ← `./cmd/data`；`:25` build `/app/analysis-service` ← `./cmd/analysis`；`:36-37` 只 COPY 这两个二进制 |
| core 主 compose | `docker-compose.yml` | `:62-118` `data-service`（`context: .` + `Dockerfile.service`，`:76` 发布 `127.0.0.1:8081`）；`:120-208` `analysis-service`（同 Dockerfile，8085）；`:218-259` `ai-service`（`context: ../tellurion-agent`，**无 `additional_contexts`**，entrypoint `/app/deploy/wait-for-deps.sh`，8086） |
| 六服务聚合 compose | `docker-compose.services.yml`（**已跟踪**，131 行） | `name: tellurion-services`；`postgres:16-alpine`（:15）+ data(:32) / risk(:49) / strategy(:65) / analytics(:82) / orchestration(:104) / broker(:119)；全部 `ports` 带 `127.0.0.1:` |

历史教训已写进 `Dockerfile.service:20-23` 的注释（ODR-021 删 `cmd/risk` 与 `cmd/execution`、REV-16 删 `cmd/strategy`）——
**这正是「Dockerfile 里的目标二进制清单会随 core 的删包而腐坏」的实例**，也是本单要把它钉死的原因。

### 1.2 四道验收命令与实测退出码

| # | 命令 | 目标 | 2026-10-26 实测 |
|---|---|---|---|
| V1 | `docker compose -f docker-compose.services.yml config -q` | 聚合文件自洽 | **exit 0** |
| V2 | `docker compose -f docker-compose.yml -f docker-compose.services.yml config -q` | 两份可合并 | **exit 0**（但见 1.3 冲突） |
| V3 | `docker compose -f docker-compose.yml config -q` | 主文件自洽 | exit 0 |
| V4 | `python tools/check_deploy_consistency.py` | compose ↔ k8s ↔ 代码三方一致 | **exit 0（2026-10-26 已修）**；修前 `exit 1（2 条假阳性）`，见下方原文与订正 |
| V5 | `docker compose ... build` | 镜像真构建 | **已验（2026-10-26）**：六服务 + core + ai 镜像构建成功 |
| V6 | `docker compose ... up -d` + 健康检查 | 真跑 | **已验一半（2026-10-26）**：聚合栈起，5/6 healthy（analytics `/healthz` 404，见裁决记录 #4） |

V4 的两条（原文）：

```
✗ 部署配置漂移 2 处：
  - deploy/k8s/analysis-deployment.yaml 注入的 JWT_SECRET 全仓没有任何读取点（AUD-39）
  - deploy/k8s/data-deployment.yaml 注入的 JWT_SECRET 全仓没有任何读取点（AUD-39）
```

判读（**已订正，2026-10-26**）：这两条**不是**本单引入的，且 `check_deploy_consistency.py:203-207` 的 `_GO_KEY_CALL`
正则只取 `BindEnv` 第一个参数 ⇒ 属于**误报（假阳性）**。裁决 #10 已补 `_GO_BINDENV_CALL` 收别名实参：复跑
**exit 0**；破坏验证（删别名）复现两条、exit 1，还原后 exit 0。V4 现可作为硬门禁。

### 1.3 合并后的真冲突（必须由拍板解决）

`docker compose -f docker-compose.yml -f docker-compose.services.yml config --services` 实测输出 10 项：

```
postgres, analytics, data, data-service, risk, analysis-service,
ai-service, broker, orchestration, strategy
```

即 **9 个应用服务 + PG**。其中：

- `data`（服务仓 `tellurion-data`，`docker-compose.services.yml:39`）与
  `data-service`（core 进程，`docker-compose.yml:76`）**同时发布 `127.0.0.1:8081`** ⇒ 真 `up` 必端口冲突；
- 合并后项目名变成 `tellurion-services`（后文件的 `name` 覆盖前文件），`ai-service` 仍在集合里 ⇒ **两份 compose + 一套 k8s 清单
  同时自称部署真相**（k8s 清单另见 §1.2 V4 的两条）。

**这是 config 校验抓不到的**（config 不检查端口冲突，也不检查 build context 是否存在 —— 实测在无兄弟仓的隔离目录中 `config -q` 仍 exit 0）。

### 1.4 决策题 D-DEPLOY-1：部署面 owner

| 选项 | 内容 | 利 | 弊 | 现成证据 |
|---|---|---|---|---|
| **A（建议）** | **core 自有「本地开发栈」这一层**：`docker-compose.yml` 只留 core 进程（data-service / analysis-service / ai-service + PG），`docker-compose.services.yml` 降级为**纯服务仓联调**用，且**不与主文件合并**（各自独立 `-f`） | 单一文件单一 owner；不引入跨仓 build context；core CI 仍不需要兄弟仓 | 两套栈并存，需文档明确「什么时候用哪套」 | 两份文件今天各自 `config -q` 都 exit 0；合并才会撞 8081 |
| B | 部署面**全部下放各服务仓**（每仓自带 `deploy/compose.yaml`，core 只留 Dockerfile） | 仓库自治最彻底 | core 的 data/analysis/ai 三进程没有「服务仓」可去；聚合栈需要有人维护跨仓编排 | 六仓已有 `Dockerfile` + `deploy/`，core 无法自举 |
| C | 保留合并形态，靠改端口消冲突 | 一份文件起全栈 | 端口表要跟着服务增减漂移；`name` 覆盖仍是隐式行为 | 合并后 8081 双发布 |

**建议 A 的具体收口动作（写进未来工单，不是本文件执行）**：
1. `docker-compose.services.yml` 头部加一句「**不与 `docker-compose.yml` 合并使用**」；
2. `SVC-BROKER-G3` 之前，把「谁发布 8081」写成唯一真相（data 服务仓接管读面时，core 的 `data-service` 退到 8085 之后的内部端口或退出栈）；
3. V4 的两条假阳性单独开一单修 `_GO_KEY_CALL`，不与本单混提。

### 1.5 `Dockerfile.service` 的目标二进制清单（**这是本单要钉死的核心产物**）

| 二进制 | 源包 | 端口 | 对应 compose 服务 | 归属判定 |
|---|---|---|---|---|
| `/app/data-service` | `./cmd/data` | 8081 | `docker-compose.yml:62` `data-service` | core（同步/调度/取数仍在 core，见 `tellurion-data:AGENTS.md` §1） |
| `/app/analysis-service` | `./cmd/analysis` | 8085 | `docker-compose.yml:120` `analysis-service` | core（回测/因子/分析面） |
| `/app/ai-service` | `../tellurion-agent` 的镜像 | 8086 | `docker-compose.yml:218` `ai-service` | **不在本 Dockerfile**（跨仓 context），本单不涉 |
| —— | `./cmd/worker` | 无 HTTP | 无 | **未进镜像**（今天只能裸跑） |
| —— | `./cmd/dualrun` | 无 HTTP | 无 | **未进镜像**（已裁留 core，见 orchestration 仓 `docs/TASKS.md`） |

**机械判据（验收 V7）**：

```powershell
# Dockerfile.service 里出现的每个 -o /app/<name>，必须对应一个真实存在的 ./cmd/<dir>，且反向不多不少
git grep -oE '\-o /app/[a-z-]+' -- Dockerfile.service
git grep -oE 'COPY --from=builder /app/[a-z-]+' -- Dockerfile.service
Test-Path cmd/data, cmd/analysis, cmd/worker, cmd/dualrun
```

今天实测：`-o /app/data-service`、`-o /app/analysis-service` 各一条，与 `:36-37` 的 COPY **一一对应**，
两个源包都存在 ⇒ **本项已自洽**。若 `cmd/worker` / `cmd/dualrun` 要进镜像，必须**同批**改 `:24-25` 与 `:36-37`，
否则又回到「build 了但没 COPY」的旧 bug 形态。

### 1.6 本单未决

1. ~~Docker daemon 不可达 ⇒ V5/V6 未验~~ → **已解除（2026-10-26）**：Engine 29.8.0 可达；V5/V6 已实跑——六服务 + core + ai 镜像构建成功，聚合栈 5/6 healthy；`analytics /healthz` 404 见裁决记录 #4。
2. V4 的 2 条假阳性是否本单修（建议：否，另开单）→ **已裁 2026-10-26**：另开单修 `_GO_KEY_CALL` 别名解析 + 破坏验证（裁决记录 #10）。
3. 「core 的 `data-service` 在 data 服务仓接管读面后是否退出栈」取决于 `SVC-DATA-G3`，本单不代裁。

---

## 2. SVC-DUAL-01 · 双 owner 全量清单

### 2.1 判定口径

**「双 owner」= 同一份业务事实，在两个仓各有一个可写的落点**（表 / 文件 / 配置）。判定分三类：

| 类 | 定义 | 例 |
|---|---|---|
| **T 类（表级）** | 两仓各建一张语义相同的表，或一仓裸表 `public.x` 对另一仓 `s.x` | `public.strategies` ↔ `strategy.strategy_definitions` |
| **B 类（行为级）** | 没有同名表，但同一裁决权两处各有一份实现 | risk 限额：core `config/analysis-service.yaml` `alert.*` / `risk_manager.*` vs `risk.limits` |
| **C 类（编排级）** | 同一张表的**写者**在 A 仓、**读者**在 B 仓，或反之 | core 写 `public.ohlcv_daily_qfq`，`tellurion-data` 读 `market.ohlcv_daily_qfq` |

### 2.2 core 侧底数（55 张，逐张可核）

core 的建表真相只有一条路径：`postgres.go` 的 `migrationStatements()`（`:171` 起），由 `migrate()`（`:1241`）
逐条 `Exec`、**无事务包裹**（`:1242-1246`）；`migrations/`、`docs/migrations/` 的 `.sql` **不被执行**
（`migration_manager.go` 已于 2026-09-18 删除，零调用方，理由写在 `:173-200` 的注释里）。

计数：

```powershell
git grep -c 'CREATE TABLE IF NOT EXISTS' -- pkg/storage/postgres.go   # 54
git grep -c 'CREATE TABLE' -- pkg/storage/postgres.go                 # 55（含一行注释 :1378）
```

`54 + 2`（模块内自建 `quant.alert_outbox` / `quant.run_provenance`）= **56**；扣掉
`postgres.go:580` 的一次性备份 CTAS（`ohlcv_daily_qfq_bak_054 AS`，不是真实业务表）= **55 张**。
台账写的「1569 行 / 55 条 CREATE TABLE」与此**一致**。

### 2.3 全量清单（55 行，按归属分组）

> 列说明：**W** = core 生产侧字面写点命中数；**R** = core 生产侧 `FROM <table>` 命中数；
> 两者都取自 `git grep ... -- '*.go' ':!*_test.go'`（**不含动态拼表名的 BulkInsert，见 §2.4**）。
> 「对拍方式」写 `无` 的，是**实测没有**，不是「应该有」。

#### 2.3.1 market 簇（22 行，T/C 类，新 owner = `tellurion-data`）

| 表 | core W | core 写点（首例） | core R | core 读点（首例） | data 侧对应 | 对拍方式 | 停写条件（四步：建第二写者→对拍→切读→停写） |
|---|---|---|---|---|---|---|---|
| `stocks` | 2 | `pkg/storage/stocks.go:29` | 8 | `pkg/storage/sector.go:20` | `market.stocks`（DDL 有，**无生产写者**） | 无 | ①data 侧出现写者 ②行数/`symbol` 集合对拍 ③core 读方切 data HTTP ④W=0 |
| `ohlcv_daily_qfq` | 4 | `pkg/storage/ohlcv.go:15` | 11 | `pkg/storage/ohlcv.go:87` | `market.ohlcv_daily_qfq`（A 集，`internal/store/write.go:268`） | 无 | 同上；**注意**：core 已有 HTTP 读面（`pkg/marketdata/http_provider.go:34` → `/ohlcv/{symbol}`、`:226` → `/api/v1/ohlcv/bulk`），但 `data_service.url` 默认 `http://localhost:8081` 指向 **core 自己的 `data-service` 进程**，不是 `tellurion-data` 服务（见 §2.6 ①） |
| `trading_calendar` | 2 | `pkg/storage/calendar.go:21` | 3 | `pkg/storage/calendar.go:68` | `market.trading_calendar`（C 集，无写者） | 无 | 同 stocks |
| `dividends` | 1 | `pkg/storage/cache.go:280` | 2 | `pkg/storage/cache.go:316` | `market.dividends`（C 集，无写者） | 无 | 同 stocks |
| `index_constituents` | 1 | `pkg/storage/cache.go:383` | 2 | `pkg/storage/cache.go:418` | `market.index_constituents`（C 集，无写者） | 无 | 同 stocks |
| `splits` | 1 | `pkg/storage/cache.go:499` | 2 | `pkg/storage/cache.go:534` | `market.splits`（C 集，无写者） | 无 | 同 stocks |
| `realtime_quote` | 0 | ——（仅 BulkInsert 动态） | 0 | —— | `market.realtime_quote`（A 集） | 无 | 同 stocks（core 写链已不可达，见 2.4） |
| `ohlcv_minute` | 0 | 同上 | 0 | —— | `market.ohlcv_minute`（A 集） | 无 | 同上 |
| `capital_flow` | 0 | 同上 | 0 | —— | `market.capital_flow`（A 集） | 无 | **已切**：core 改投 `POST /ingest/points`（`cmd/data/capital_flow_executor.go:1-70`） |
| `sectors` | 1 | `pkg/storage/sector.go:56` | 0 | —— | `market.sectors`（A 集） | 无 | 同 stocks |
| `stock_sector_map` | 1 | `pkg/storage/sector.go:96` | 0 | —— | `market.stock_sector_map`（A 集） | 无 | 同 stocks |
| `top_list` | 0 | 仅 BulkInsert 动态 | 0 | —— | `market.top_list`（A 集） | 无 | **已切**（`cmd/data/top_list_executor.go`） |
| `limit_up_pool` | 0 | 同上 | 0 | —— | `market.limit_up_pool`（A 集） | 无 | 同 stocks |
| `announcements` | 0 | 同上 | 0 | —— | `market.announcements`（A 集） | 无 | 同 stocks |
| `news` | 0 | 同上 | 0 | —— | `market.news`（A 集） | 无 | 同 stocks |
| `hot_search` | 0 | 同上 | 0 | —— | `market.hot_search`（A 集） | 无 | 同 stocks |
| `global_ohlcv` | 0 | 同上 | 0 | —— | `market.global_ohlcv`（A 集） | 无 | 同 stocks |
| `market_activity` | 1 | `pkg/storage/market_activity.go:35` | 1 | `pkg/storage/market_activity.go:111` | `market.market_activity`（**data 显式拒写** `write.go:239` `daily_basic`） | 无 | 写者**只能**是 core ⇒ 停写条件是「core 的 daily_basic 同步腿迁到 data 且 data 解除拒写」 |
| `market.index_daily` | 1 | `pkg/storage/index_daily.go:31` | 2 | `pkg/storage/index_daily.go:117`、`pkg/storage/ohlcv.go:286` | `market.index_daily`（C 集豁免，**无写者**；**DDL 形状冲突**见 2.5） | 无 | ①data 出现写者 ②逐日 `(code, trade_date, close)` 对拍 ③切读 ④W=0 |
| `ingest.raw` | 1 | `pkg/storage/ingest_raw.go:62` | 1 | `pkg/storage/ingest_raw.go:80` | `ingest.raw`（C 集，无写者） | 无 | 按 `content_hash` 双查对拍；core W=0 |
| `sync_jobs` | 4 | `pkg/storage/sync_jobs.go:15` | 5 | `pkg/storage/sync_jobs.go:36` | `ingest.sync_jobs`（无写者） | 无 | 同步调度整体迁 data 后停写 |
| `sync_schedules` | 3 | `pkg/storage/sync_jobs.go:230` | 5 | `pkg/storage/sync_jobs.go:250` | `ingest.sync_schedules`（无写者） | 无 | 同上 |

> **`stock_fundamentals` 不在此簇**：data 侧没有对应表（它被列进 data 的 **C 集豁免名单**，理由是该表已有生成列
> `available_date = COALESCE(ann_date, trade_date)`，见 `tellurion-data:internal/schema/visible_at.go:53`）⇒ 归 core 自有，见 §2.3.6。

#### 2.3.2 run 簇（2 行 T 类 + 1 行 core-only，新 owner = `tellurion-orchestration`）

| 表 | core W | core 写点 | core R | core 读点 | orchestration 侧 | 对拍方式 | 停写条件 |
|---|---|---|---|---|---|---|---|
| `backtest_jobs` | 6 | `pkg/storage/backtest_jobs.go:14`（+`:29` UPDATE） | 4 | `pkg/storage/backtest_jobs.go:67` | `run.backtest_jobs`（`internal/queue/ddl.go`） | 无 | ①orchestration 收编 job 面 ②`id/status/updated_at` 逐行对拍 ③core `JobService` 切 HTTP ④W=0 |
| `walk_forward_reports` | 1 | `pkg/storage/cache.go:593` | 3 | `pkg/storage/cache.go:625` | `run.walk_forward_reports`（**无生产读写**） | 无 | 同上；且 orchestration 侧先要有写者 |
| `backtest_adjustment_anchors` | 1 | `pkg/storage/adjustment_anchors.go:84` | 1 | `pkg/storage/adjustment_anchors.go:35` | **无对应表** | 无 | **core-only**，不进本簇退役范围 |

#### 2.3.3 strategy 簇（1 行 T 类，新 owner = `tellurion-strategy`）

| 表 | core W | core 写点 | core R | core 读点 | strategy 侧 | 对拍方式 | 停写条件 |
|---|---|---|---|---|---|---|---|
| `strategies` | 2 | `pkg/storage/strategies.go:14`、`:107`（软删 `is_active=FALSE`） | 5 | `pkg/storage/strategies.go:37` | `strategy.strategy_definitions` | 无 | ①strategy 侧建「定义 + 版本」写者 ②`strategy_id` ↔ `name/type/params` 对拍 ③core `pkg/strategy/db.go` 切 HTTP ④W=0；**软删语义要先对齐**（core 是 `is_active`，strategy 是版本链） |

补充：`quant.strategy_state` / `quant.external_signals` 虽在 `quant.*`，但**归属 core**
（`tellurion-strategy:docs/db-schema.md:22` 明写 `quant.external_signals` 不属本仓）⇒ 不进本簇。

#### 2.3.4 ai 簇（7 行 T 类，新 owner = `tellurion-agent`）

| 表 | core W | core 写点 | core R | core 读点 | agent 侧 | 对拍方式 | 停写条件 |
|---|---|---|---|---|---|---|---|
| `experiments` | 4 | `pkg/storage/experiments.go:108`（+`:171`/`:200`/`:304`） | 2 | `pkg/storage/experiments.go:131` | `ai.experiments` | **有**：`cmd/ai/experiment_stopwrite_integration_test.go:28`（`public.experiments` count + max(id) 冻结断言） | 该测试已就位 ⇒ 停写条件 = 「core 侧 6 个方法生产零调用」+ 该断言长期绿 |
| `factor_hypothesis` | 2 | `pkg/storage/factor_hypothesis.go:35`、`:108` | 2 | `pkg/storage/factor_hypothesis.go:59` | `ai.factor_hypothesis` | 无 | `SeedBuiltinFactorHypotheses` 调用点归零（今天唯一生产调用 `postgres.go:47`，启动 seed） |
| `factor_genes` | 0 | —— | 0 | —— | `ai.factor_genes` | **有**：`pkg/ai/gene_pool/pool_ai_schema_integration_test.go:24`（write-scope：`public.*` 0 行） | core 侧已无读写方 ⇒ **只等删 DDL** |
| `strategy_genes` | 0 | —— | 0 | —— | `ai.strategy_genes` | 同上 | 同上 |
| `research.profile` | 0 | —— | 1 | `pkg/storage/research.go:101` | `ai.research_profile` | 无 | core 只读不写 ⇒ 切读即可停 |
| `research.conclusion` | 0 | —— | 1 | `pkg/storage/research.go:135` | `ai.research_conclusion` | 无 | 同上 |
| `research.question` | 0 | —— | 1 | `pkg/storage/research.go:168` | `ai.research_question` | 无 | 同上 |

补充（对拍工具的**唯一富集仓**，可复用范式）：
`tellurion-agent:pkg/aistore/schema_core_drift_test.go:117`（读 core `postgres.go` 比列集，缺 core 时 skip）、
`pkg/ai/gene_pool/sql_scope_test.go:19`（静态 schema 限定）、
`internal/repoguard/storage_import_test.go:128`（禁 import core storage，白名单空）。

#### 2.3.5 risk 簇（0 行表级 · 1 条 B 类行为级）

| 项 | core 侧 | `tellurion-risk` 侧 | 判读 |
|---|---|---|---|
| 限额/阈值真相 | `config/analysis-service.yaml:187-198` `alert.*`（`max_position_weight 0.20` / `max_sector_weight 0.40` / `max_drawdown 0.15` / `daily_loss_limit -50000` / `failure_rate_limit 0.10`）+ `:155-160` `risk_manager.*` | `risk.limits`（`internal/rule/ddl.go:65`）**无生产读写方**（`:63-64` 注释「本切片只建表」） | 真相**今天只在 core**，risk 侧表是空壳 |
| 裁决实现 | `pkg/risk/manager.go:13-28` `RiskManagerConfig`（14 个标量字段）+ `pkg/alert/detectors.go` 6 个检测器 | `contracts/risk` 的 `Bundle`（第三轮裁决 ②）**已建包，未接 `bundle → RiskPolicy`** | U7 已裁：检测器留 core、通道编排归 risk |
| 告警投递 | `quant.alert_outbox`（`pkg/storage/alert_outbox.go:53`）+ `POST /api/risk/events`（`cmd/analysis/setup.go` `buildAlertSystem`） | `risk.risk_events`（`internal/rule/ddl.go`） | 这是**已接的一条腿**（QT-42 X3c），不是双 owner |
| `risk.audit_logs` vs `public.audit_logs` | `public.audit_logs`（`pkg/auth/auth.go:505`） | `risk.audit_logs`（`internal/rule/ddl.go`） | **不是同一张表**（`tellurion-risk:docs/db-schema.md:23`）⇒ 不计入双 owner |

**B 类判据（机械）**：`git grep -n 'resolve_enabled' -- config/` 今天是 `false`（`:122`，`resolve_url` 指向 8086）；
`cmd/analysis/wiring/risk_rules.go` 的文件头写明「代码已接线、开关默认关」。**停双真相 = 把 `RiskManagerConfig`
的构造源切到 risk 服务返回的 policy**，判据是「`git grep -n 'v.GetFloat64("alert.max' -- '*.go'` 归零」。

#### 2.3.6 core-only（23 行，无服务仓对手方）

| 表 | core W | core 写点 | core R | core 读点 | 归属判读 |
|---|---|---|---|---|---|
| `stock_fundamentals` | 6 | `pkg/storage/fundamentals.go:212` | 10 | `pkg/storage/fundamentals.go:68` | **core 自有**（data 列为 C 集豁免）；未来若迁，先有 data 侧 DDL |
| `fundamentals_detail` | 1 | `pkg/storage/fundamentals_detail.go:46` | 1 | `:106` | core 自有 |
| `factor_cache` | 1 | `pkg/storage/cache.go:37` | 2 | `:75` | core 自有（因子中间态） |
| `factor_returns` | 1 | `pkg/storage/cache.go:134` | 1 | `:168` | core 自有（分析产物） |
| `ic_analysis` | 1 | `pkg/storage/cache.go:207` | 1 | `:241` | core 自有（分析产物） |
| `backtest_adjustment_anchors` | 1 | `pkg/storage/adjustment_anchors.go:84` | 1 | `:35` | core 自有 |
| `users` | 3 | `pkg/auth/auth.go:215`、`:331` | 5 | `:180`、`:248` | core 自有（鉴权域，不进任何服务仓） |
| `audit_logs` | 1 | `pkg/auth/auth.go:505` | 1 | `:521` | core 自有（≠ `risk.audit_logs`） |
| `data_migrations` | 2 | `pkg/storage/postgres.go:546`、`:584` | 2 | `:542`、`:576` | core 自有（迁移留痕，**退役时最后处理**） |
| `factor_definitions` | 1 | `pkg/storage/factor_definition.go:29` | 1 | `:74` | core 自有（表达式字段目录） |
| `factor_registrations` | 2 | `pkg/storage/factor_registration.go:43`、`:79` | 1 | `:122` | core 自有 |
| `factor_registration_audit` | 1 | `pkg/storage/factor_registration.go:103` | 1 | `:163` | core 自有 |
| `factor_mining_records` | 1 | `pkg/storage/mining.go:33` | 1 | `:69` | core 自有 |
| `audit.message_log` | 1 | `pkg/eventstore/interfaces.go:109` | 3 | `:120`、`:130` | core 自有（事件溯源留痕） |
| `quant.strategy_state` | 1 | `pkg/strategy/state_store.go:142` | 2 | `:158`、`:164` | core 自有（**strategy 仓明说不属它**） |
| `quant.external_signals` | 1 | `pkg/strategy/signal_store.go:81` | 1 | `:89` | core 自有（同上） |
| `quant.sw_industry_tree` | 1 | `pkg/storage/supply_chain.go:68` | 1 | `:267` | core 自有 |
| `quant.supply_chain_edge` | 1 | `pkg/storage/supply_chain.go:81` | 1 | `:309` | core 自有 |
| `quant.industry_vitality` | 1 | `pkg/storage/supply_chain.go:92` | 1 | `:359` | core 自有 |
| `quant.etf_constituent` | 1 | `pkg/storage/supply_chain.go:103` | 3 | `:403`、`:405` | core 自有 |
| `quant.macro_series` | 1 | `pkg/storage/macro.go:56` | 1 | `:107` | core 自有（data 侧 `quant.macro_series` 是 **B 集锚点**，`visible_at.go:43`，写者仍是 core） |
| `quant.alert_outbox` | 3 | `pkg/storage/alert_outbox.go:101`、`:164` | 2 | `:126`、`:236` | core 自有（risk 域的发件箱，**不是**双 owner） |
| `quant.run_provenance` | 1 | `pkg/storage/run_provenance.go:99` | 1 | `:131` | core 自有（run 留痕；orchestration 侧无同名表） |

**算术校验**：T 类 22 + 2 + 1 + 7 = **32**；core-only = **23**；合计 **55** ✔

### 2.4 静态 grep 抓不到的那一半：动态拼表名

`realtime_quote` / `ohlcv_minute` / `capital_flow` / `top_list` / `limit_up_pool` / `announcements` / `news` /
`hot_search` / `global_ohlcv` 这 9 张表在 core **没有任何字面 SQL**，写点来自
`pkg/storage/bulk_insert.go:42-57` 的 `defaultTableMapper.tables`（13 个 `data_type` → 表名），
SQL 在 `:265` `buildUpsertSQL(table, cols, pk)` 现场拼。

**⇒ 停写判据不能只 grep 表名**，必须同时证明这条链不可达：

| 环节 | 证据（实测） |
|---|---|
| `BulkInsert` 生产调用方 | 仅 `pkg/data/source/etl.go:115`（`git grep -nE '\.BulkInsert\(' -- '*.go' ':!*_test.go'` = 1） |
| `ETLPipeline.Process` 生产调用方 | **0**（`capital_flow`/`top_list` 已改投 `POST /ingest/points`，见 `cmd/data/capital_flow_executor.go:1-70`） |
| `NewETLPipeline` 托管引用 | 非 test 命中 7 行 = **6 条注释 + 1 个定义**，**零调用点**（`pkg/data/source/etl.go:43` 是定义） |
| 反向护栏已存在 | `internal/repoguard/registry_etl_wiring_test.go`（白名单已清空，规则 A 断言引用归零） |

**判读**：这 9 张表的 core 写链**今天已不可达**（代码还在，但没有人调）。
所以 DATA 退役的「停写」半步**事实上已完成**，缺的是**删代码 + 删 DDL** —— 这正是审计 §五之二
说 data「缺代码删除那一半」的具体所指。

### 2.5 形状冲突：`market.index_daily`（必须先裁，否则第二步就会踩）

| 侧 | 定义 | 关键差异 |
|---|---|---|
| core `postgres.go:1180-1201` | `id BIGSERIAL PRIMARY KEY` + `UNIQUE (code, trade_date)` + `CHECK` + `source VARCHAR(64)` | 有代理主键 |
| data `internal/schema/market.go:301-317` | `PRIMARY KEY (code, trade_date)` + **无 id 列** + `source VARCHAR(32)` + `volume DEFAULT 0` | 无代理主键 |

`CREATE TABLE IF NOT EXISTS` **谁先建谁定形**。今天 core 的 `migrate()` 会先建，
data 的 DDL 撞上已存在的表会静默跳过 ⇒ **data 侧以为在建，其实建的是 core 的形状**。
这条必须在 DUAL-01 拍板时二选一（建议：data 侧形状为准，core 侧在 DATA 退役时删表由 data 重建；
或 core 侧保持 `source VARCHAR(64)` 并把 data 侧对齐 —— 二者都要改一仓，不能两仓都「自认唯一」）。

### 2.6 「切读」今天卡在哪（比「有没有对拍工具」更要紧的事实）

**① core 已有的 HTTP 读面，读的是 core 自己。**

| 证据 | 内容 |
|---|---|
| `pkg/marketdata/http_provider.go:34` `NewHTTPProviderWithToken(baseURL, token, logger)` | core 唯一的行情 HTTP 读面 |
| `:76` | `GET /ohlcv/{symbol}?start_date=&end_date=` |
| `:226` | `POST /api/v1/ohlcv/bulk` |
| `pkg/bootstrap/engine.go:19-25`、`cmd/analysis/kernel_wiring.go:197` | `data_service.url` 缺省 `http://localhost:8081` |
| `cmd/analysis/handlers_proxy.go:110/141/151/155/171/195` | 另有一组 `/ohlcv`、`/screen`、`/stocks/count`、`/market/index`、`/factors/{name}` 代理到同一地址 |
| `docker-compose.yml:76` | 8081 上是 **core 的 `data-service`** |

⇒ 这些路由与 `tellurion-data` 的对外面（`/health`、`/datasets/resolve`、`/datasets/{hash}`、
`POST /ingest/points`，见 `tellurion-data:AGENTS.md` §1）**不是同一套**。所以「切读」不是改一行 URL，
而是**先做路由/协议对齐**（谁把 `/ohlcv/{symbol}` 这类面向旧接口做成适配层）。

**② 表级 writer 对拍工具的可用性（现状，不粉饰）**

| 仓 | 表级 writer 对拍工具 | 证据 |
|---|---|---|
| `tellurion-data` | **无** | 无 `*diff*/*verify*/*compare*/*recon*/*backfill*` cmd；无 Makefile；CI 只 build/vet/test |
| `tellurion-orchestration` | **无** | 同上；`run.run_artifacts` 唯一写方法只有测试调用 |
| `tellurion-strategy` | **无** | 同上；写点全在 `internal/asset/pgstore.go`，无 DELETE |
| `tellurion-risk` | **无** | 同上 |
| `tellurion-analytics` | **无** | 同上；`analysis.benchmarks` 写方法生产零调用 |
| `tellurion-broker` | 部分 | `internal/reconcile/reconcile.go:102` 是**账户快照**对账纯函数，不是表级 writer 对拍 |
| `tellurion-agent` | **有 5 件** | `schema_core_drift_test.go:117`、`experiment_stopwrite_integration_test.go:28`、`pool_ai_schema_integration_test.go:24`、`sql_scope_test.go:19`、`storage_import_test.go:128` |

**⇒ 结论**：DUAL-01 的「对拍方式」列，除 ai 簇外**全是「待建」**。这不是文档偷懒，是实测结果；
任何写成「有对拍」的都应当是假账。

### 2.7 本单未决

1. **切读的接口面谁做**：core 的 `/ohlcv/{symbol}`、`/api/v1/ohlcv/bulk`、`/screen`、`/market/index`、`/factors/{name}`
   与 `tellurion-data` 的 4 条路由不是同一套（§2.6 ①）⇒ 需要裁决「data 服务补旧面」还是「core 改调新面」。
2. 对拍工具谁建（core 建「双写比对 job」还是各服务仓自建自测）—— 建议**各仓自建**，core 只定义不变式。
3. `market.index_daily` 形状二选一（§2.5）。
4. `strategies` 软删语义 ↔ strategy 版本链的对齐（§2.3.3）。
5. 「表在新归属是否已唯一」的跨仓核账：data 23 / strategy 5 / risk 7 / analytics 5 / orchestration 6 / broker 1 / agent 8 ——
   本角色只核了 **data 23 全部、orchestration 6 全部、strategy 5 全部、risk 7 全部、analytics 5 全部、broker 1、agent 8** 的**存在性**
   （见 §2.8），但「是否已有唯一写者」需各仓 owner 复核。

### 2.8 跨仓表数核对（只读取证）

```powershell
foreach ($r in 'data','orchestration','strategy','risk','analytics','broker','agent') {
  git -C "..\tellurion-$r" grep -nE 'CREATE TABLE IF NOT EXISTS' -- '*.go'
}
```

| 仓 | 表数 | 清单 |
|---|---|---|
| data | 23 | `market.*` 19（announcements, capital_flow, dividends, global_ohlcv, hot_search, index_constituents, index_daily, limit_up_pool, market_activity, news, ohlcv_daily_qfq, ohlcv_minute, realtime_quote, sectors, splits, stock_sector_map, stocks, top_list, trading_calendar）+ `ingest.*` 4（data_migrations, raw, sync_jobs, sync_schedules） |
| orchestration | 6 | `run.backtest_jobs`, `run.batch_runs`, `run.parent_jobs`, `run.run_artifacts`, `run.walk_forward_reports`, `run.worker_registry` |
| strategy | 5 | `strategy.strategy_artifact_blobs`, `strategy.strategy_artifacts`, `strategy.strategy_build_jobs`, `strategy.strategy_definitions`, `strategy.strategy_versions` |
| risk | 7 | `risk.audit_logs`, `risk.circuit_orders`, `risk.limits`, `risk.risk_events`, `risk.rule_defs`, `risk.rule_revisions`, `risk.rule_snapshots` |
| analytics | 5 | `analysis.attribution`, `analysis.benchmarks`, `analysis.mc_runs`, `analysis.reports`, `analysis.sensitivity_runs` |
| broker | 1 | `broker.order_acks` |
| agent | 8 | `ai.experiments`, `ai.factor_genes`, `ai.factor_hypothesis`, `ai.generation_chain`, `ai.research_conclusion`, `ai.research_profile`, `ai.research_question`, `ai.strategy_genes`（`ai.tampered` 只出现在 `pkg/aistore/schema_test.go:135` 的**夹具**里，不是真实表） |

与派发原文的 23 / 5 / 7 / 5 / 6 / 1 / 8 **逐项一致**。

---

## 3. SVC-RETIRE-INDEX · 索引退役判据

### 3.1 底数（实测）

```powershell
git grep -nE 'CREATE (UNIQUE )?INDEX' -- pkg/storage/postgres.go   # 102 行
```

| 项 | 值 |
|---|---|
| 索引语句（`postgres.go`） | **102** 条 |
| 唯一索引名 | **95** 个 |
| 重名（写得两遍） | **7** 个 |
| 其中 `CREATE UNIQUE INDEX` | **6** 条 |
| 模块内自建索引 | `quant.alert_outbox`（`alert_outbox.go:75` `idx_alert_outbox_pending`）、`quant.run_provenance`（`run_provenance.go:73` `idx_run_provenance_run_spec_hash`） |
| 修复前 `DROP INDEX` | **0 条**（`git grep -in 'DROP INDEX' -- '*.go' '*.sql'` 无命中） |

**7 个重名**（同名在 `migrationStatements()` 与 `migrate()` 末尾的 `indexes` 数组各写一次；
`CREATE INDEX IF NOT EXISTS` 让第二遍是 no-op）：

```
idx_ingest_raw_source_dataset      （:379, :1294）
idx_ingest_raw_as_of               （:380, :1295）
idx_research_conclusion_ticker     （:415, :1297）
idx_research_conclusion_status     （:416, :1298）
idx_research_question_ticker       （:417, :1299）
idx_research_question_status       （:418, :1300）
idx_research_profile_needs_review  （:419, :1301）
```

⇒ 纯去重可消掉 **7 条语句 / 0 个索引对象**（幂等，行为零变化）。这是**唯一可以先做**、
但**仍属施工**的动作，本文件不执行。

### 3.2 退役判据（本体）

**判据不是一个数字，是「对象级前后快照 + 归零断言」三件套。**

#### P1 — 前后快照（对象级，不是语句级）

```sql
-- 退役前、每一批 DDL 删除前各跑一次，落两份文件
SELECT schemaname || '.' || tablename AS tbl,
       indexname,
       indexdef
FROM pg_indexes
WHERE schemaname IN ('public','quant','market','ingest','run','strategy','risk','analysis','ai','broker')
ORDER BY 1, 2;
```

比对口径：
- **消失的索引** ⊆ 本次声明的「待退役索引清单」（多出来的消失 = 事故）；
- **新增的索引** 必须来自对应服务仓的 DDL（core 不得新增）；
- **`indexdef` 变化**（同表同列但顺序不同）也算变化，需逐条解释。

#### P2 — 归零断言（按仓）

| 断言 | 命令 | 目标 |
|---|---|---|
| core 索引语句数 | `git grep -cE 'CREATE (UNIQUE )?INDEX' -- pkg/storage/postgres.go` | 从 102 降到退役后的声明值（逐批核算） |
| core 重名归零 | §3.1 的 7 个名字各出现 **1** 次 | 7 → 0 |
| core `DROP INDEX` 引入 | `git grep -n 'DROP INDEX' -- '*.go'` | **只有**白名单内的语句（见 P3） |

#### P3 — **未裁问题：是否允许 `DROP INDEX`**

这是本单**必须由主任拍板**的一条，因为它与仓库现有的「DDL 只增不改」纪律正面冲突：

| 选项 | 内容 | 利 | 弊 | 现成证据 |
|---|---|---|---|---|
| **X（建议，分阶段）** | **禁止 core 写 `DROP INDEX`**；退役用「**先停用、后由 DBA 一次性清理**」：core 删 `CREATE INDEX` 语句（新库不再建），旧库的索引对象由一条 **ODR** 记录 + 一个人工 `DROP INDEX` 脚本处理 | 不破坏「DDL 幂等只增」的现模型（`migrate()` 无版本表、无 down）；可回滚（索引还在只是没用） | 旧库会残留无用索引，占写入成本；需要运维动作 | `postgres.go:173-200` 明写「没有版本表、没有 down、没有执行记录 —— 这是有意的取舍」 |
| Y | **允许 `DROP INDEX IF EXISTS` 进 `migrationStatements()`** | core 自己收干净；一条命令 | 打开「DDL 可删」的口子，后续任何删除都可援引本单；`migrate()` 无事务，中途失败会留下**半删状态** | `:1242-1246` 逐条 `Exec` 无事务包裹 |
| Z | 永不删索引，只删语句 | 最安全 | 旧库索引永久残留，「退役」名不副实 | —— |

**建议 X**，并把「旧库索引清理」写成一条独立 ODR（与 `data_migrations` 的一次性哨兵同型）。

#### P4 — 约束背书索引**不可单独 DROP**

以下索引不是 `CREATE INDEX` 建的，是 PK / UNIQUE **自动生成**的，删语句删不掉、`DROP INDEX` 会连带破坏约束：

- 全部 `PRIMARY KEY`（如 `strategies.strategy_id`、`backtest_jobs.id`、`quant.alert_outbox.alert_id`）；
- 全部 `UNIQUE (...)`（如 `stock_fundamentals(ts_code, trade_date)`、`market.index_daily(code, trade_date)`）。

**⇒ 判据要求**：退役清单必须**分两列** ——「standalone 索引（可删语句）」/「约束背书索引（只能随表删）」。

### 3.3 各仓 standalone 索引清单（退役时不得误伤）

```powershell
foreach ($r in 'data','orchestration','strategy','risk','analytics','broker','agent') {
  git -C "..\tellurion-$r" grep -cE 'CREATE (UNIQUE )?INDEX' -- '*.go' ':!*_test.go'
}
```

| 仓 | standalone 索引语句 | 备注 |
|---|---|---|
| data | **21** | `internal/schema/market.go` 14 + `internal/schema/ingest.go` 7 |
| orchestration | **11** | `internal/queue/ddl.go` |
| strategy | **0** | 全在表内约束 |
| risk | **3** | `rule_revisions_status_idx` / `audit_logs_rule_idx` / `risk_events_ts_idx` |
| analytics | **1** | `reports_run_type_idx` |
| broker | **0** | —— |
| agent | **14** | 含 `idx_experiments_run_seq`（与 `UNIQUE (run_id, seq)` 冗余，`:77/:79`）、`idx_research_profile_needs_review`（写在**无写者**的表上，`:118`） |

**判读**：core 的 102 条是索引退役的**主战场**；服务仓合计仅 **50 条**（21+11+0+3+1+0+14），其中 agent 的 2 条本身已是冗余候选。

### 3.4 验收命令

```powershell
# V1 底数可复跑
git grep -cE 'CREATE (UNIQUE )?INDEX' -- pkg/storage/postgres.go      # 今天 102
# V2 重名归零
(git grep -hE 'CREATE (UNIQUE )?INDEX IF NOT EXISTS' -- pkg/storage/postgres.go) |
  ForEach-Object { if ($_ -match 'INDEX IF NOT EXISTS\s+([a-z0-9_]+)') { $matches[1] } } |
  Group-Object | Where-Object Count -gt 1                            # 今天 7 组，目标 0 组
# V3 未引入 DROP INDEX（若拍板 X）
git grep -n 'DROP INDEX' -- '*.go'                                    # 今天 0，目标 0
```

**破坏验证（写进未来工单）**：往 `migrationStatements()` 里插一条重复索引名 ⇒ V2 必须变红；
逐字节还原 ⇒ 必须变绿。

---

## 4. 七条 `SVC-CORE-*-RETIRE` 的机械判据

> 每条 = **删除对象** + **表名** + **包路径** + **grep 模式** + **今天的基线** + **归零目标** + **反向护栏**。
> 所有计数均为 `-- '*.go' ':!*_test.go'` 的生产侧，2026-10-26 实测。
> 组内顺序固定（见 §5），因为七条**全部触** `pkg/storage/postgres.go`。
>
> **计数口径提醒**：`git grep` 不区分注释，基线里含少量「注释里提到该 SQL」的行（已逐条标注）。
> 未来工单的归零断言应加一道注释过滤，例如
> `git grep -nE '<pat>' -- '*.go' ':!*_test.go' | Where-Object { $_ -notmatch ':\s*//' }` ——
> 否则删掉注释也能「变绿」，那是假绿。

### 4.1 `SVC-CORE-DATA-RETIRE`

| 维度 | 内容 |
|---|---|
| 删除对象 | ① core 版 22 张 market/ingest 表的 DDL 与索引；② `pkg/data` 的 connector/ETL/因子实现；③ `cmd/data` 的同步/调度腿；④ `pkg/storage/{bulk_insert,ohlcv,stocks,calendar,sector,cache,ingest_raw,sync_jobs,index_daily,market_activity}.go` |
| 表名 | `stocks, ohlcv_daily_qfq, trading_calendar, dividends, index_constituents, splits, realtime_quote, ohlcv_minute, capital_flow, sectors, stock_sector_map, top_list, limit_up_pool, announcements, news, hot_search, global_ohlcv, market_activity, market.index_daily, ingest.raw, sync_jobs, sync_schedules`（22） |
| 包路径 | `tellurion/pkg/data`, `tellurion/pkg/data/source`, `tellurion/pkg/marketdata`, `tellurion/cmd/data` |
| grep 模式（4 条，全部须 0） | ① `(INSERT INTO\|UPDATE\|DELETE FROM)[[:space:]]+(<上表 22 名，`\.` 转义>)\b` ② `NewETLPipeline` ③ `\.BulkInsert\(` ④ `"tellurion/pkg/data` |
| 今天的基线 | ① **23 行**（= 22 条活写点 + 1 条注释 `pkg/storage/sync_jobs.go:81`；其中 `postgres.go:544`/`:582` 是 Migration 052/054 的**一次性**换算修正，随表退役一并删） ② **7**（=6 注释 + 1 定义，零调用） ③ **1**（`pkg/data/source/etl.go:115`） ④ **29 行 / 24 文件** |
| 归零目标 | ①0 ②0（连定义一起删） ③0 ④0 |
| 反向护栏 | 新建 `internal/repoguard/storage_market_tables_retired_test.go`：静态读 `postgres.go`，断言上表 **零** `CREATE TABLE`；并断言 `pkg/data` 目录不存在 |
| 附加 | `bulk_insert_ddl_test.go` 里的 `codeReferenced`（`sync_jobs/sync_schedules/factor_genes/strategy_genes/market.index_daily`）要**同批**改，否则 DDL 删了测试先红 |
| 附加 | `Dockerfile.service:24` 的 `./cmd/data` 目标在 `cmd/data` 删空后必须同步删 —— **这就是本单另触 `Dockerfile.service` 的原因** |

### 4.2 `SVC-CORE-RUN-RETIRE`

| 维度 | 内容 |
|---|---|
| 删除对象 | `backtest_jobs` / `walk_forward_reports` 两张表的 core DDL + `pkg/storage/{backtest_jobs,cache}.go` 的 job/WF 段 + `pkg/backtest/job`、`pkg/backtest/walkforward` 的落库腿 |
| 表名 | `backtest_jobs, walk_forward_reports`（**不含** `backtest_adjustment_anchors`，它 core-only） |
| 包路径 | `tellurion/pkg/backtest/job`, `tellurion/pkg/backtest/walkforward`, `tellurion/pkg/storage` |
| grep 模式 | ① `(INSERT INTO\|UPDATE\|DELETE FROM)[[:space:]]+(backtest_jobs\|walk_forward_reports)\b` ② `FROM[[:space:]]+(backtest_jobs\|walk_forward_reports)\b` ③ `storage\.PostgresStore` 出现在 `pkg/backtest/**` |
| 今天的基线 | ① **7**（`backtest_jobs` 6 + `walk_forward_reports` 1） ②（读点）**7**（4 + 3） ③ `pkg/backtest/engine.go:135`、`:511` `SetStore` |
| 归零目标 | ①②0 ③`pkg/backtest` 不出现 `*storage.PostgresStore`（改接口注入） |
| 反向护栏 | repoguard：`pkg/backtest/**` 禁 import `tellurion/pkg/storage`；改坏（加一行 import）应红 |
| 阻塞 | orchestration 的 `run.walk_forward_reports` **今天无生产读写**，先要有写者才谈得上停写 |

### 4.3 `SVC-CORE-STRAT-RETIRE`

| 维度 | 内容 |
|---|---|
| 删除对象 | `strategies` 表 DDL + `pkg/storage/strategies.go` + `pkg/strategy/db.go` 的 DB 腿（本地 registry 保留或改 HTTP） |
| 表名 | `strategies`（**不含** `quant.strategy_state` / `quant.external_signals`） |
| 包路径 | `tellurion/pkg/strategy`（`db.go` 为唯一 DB 入口）, `tellurion/pkg/storage` |
| grep 模式 | ① `(INSERT INTO\|UPDATE\|DELETE FROM)[[:space:]]+strategies\b` ② `FROM[[:space:]]+strategies\b` ③ `SaveStrategyConfig\|ListStrategyConfigs\|DeleteStrategyConfig\|SeedStrategies` |
| 今天的基线 | ① **2** ② **5** ③ **19 行**（`pkg/storage/strategies.go` 的 5 个方法定义 + 5 条 doc 注释 + 9 处调用：`pkg/strategy/db.go:47/:52/:57/:62/:72/:145`、`pkg/storage/strategies.go:155`、`cmd/analysis/setup.go:336` `SeedStrategies`、`pkg/strategy/db.go:98` 注释） |
| 归零目标 | ①②0 ③0 |
| 反向护栏 | repoguard：`pkg/strategy/**` 禁出现 `strategies` 表名 |
| 前置 | strategy 侧 `strategy.strategy_definitions` + 版本链要有**唯一写者**；软删语义先对齐（§2.3.3） |

### 4.4 `SVC-CORE-RISK-RETIRE`

> **本单不是表退役**（表级 0 行，见 §2.3.5），是**裁决权收口**。

| 维度 | 内容 |
|---|---|
| 删除对象 | core `RiskManagerConfig` 的**配置真相**（`config/analysis-service.yaml` 的 `alert.*` / `risk_manager.*` 作为限额来源的那一半）；检测器**保留**（U7 已裁） |
| 表名 | 无（`public.audit_logs` ≠ `risk.audit_logs`，不动） |
| 包路径 | `tellurion/pkg/risk`, `tellurion/cmd/analysis/wiring`（`risk_rules.go`） |
| grep 模式 | ① `v\.Get(Float64\|String\|Int)\("alert\.` ② `v\.Get(Float64\|Int)\("risk_manager\.` ③ `resolve_enabled` |
| 今天的基线 | ① **11 行**（5 个阈值 + webhook/recorder/loop 6 项，`cmd/analysis/setup.go:112-144`） ② **14 行**（`pkg/bootstrap/engine.go:38-51`） ③ **1**（`config/analysis-service.yaml:122` = `false`） |
| 归零目标 | ①②的**真相源**改为「risk 服务返回的 policy」⇒ ①②应降到「仅默认值兜底」或 0；③ 变 `true` 并长期有观测 |
| 反向护栏 | 契约侧：`contracts/risk` 的 `Bundle → ParseBundle` 必须有测试；core 侧：限额常量不得再出现在 `config/*.yaml` |
| 阻塞 | `bundle → RiskPolicy` 未接（第三轮裁决 ② 只建了契约包）⇒ **本单排在 AN 之前不可达** |

### 4.5 `SVC-CORE-AN-RETIRE`

| 维度 | 内容 |
|---|---|
| 删除对象 | core 侧「分析产品化」的实现：`pkg/backtest/metrics`、`pkg/backtest/reporting`（迁移/删除取决于 analytics 服务仓是否收编） |
| 表名 | 无直接表；产出落 `analysis.*`（analytics 仓） |
| 包路径 | `tellurion/pkg/backtest/metrics`, `tellurion/pkg/backtest/reporting`, `tellurion/cmd/analysis/handlers_backtest.go` |
| grep 模式 | `tellurion/pkg/backtest/(metrics\|reporting)` |
| 今天的基线 | **3**：`cmd/analysis/handlers_backtest.go:19`、`cmd/dualrun/main.go:38`、`pkg/backtest/engine.go:23` |
| 归零目标 | **0**（或按裁决「保留在 core、只是不再对外」则改为「引用集合冻结」的快照断言） |
| 反向护栏 | 冻结快照：把 3 条 import 写进 `internal/repoguard` 的允许清单；新增一条即红 |
| 阻塞 | analytics 的 `analysis.benchmarks` 写方法生产零调用 ⇒ 先要有真写者 |

### 4.6 `SVC-CORE-BROKER-RETIRE`

| 维度 | 内容 |
|---|---|
| 删除对象 | core 侧的券商/模拟撮合实现：`pkg/live` 的 MockTrader、`postgres_order_store.go`、`simulated_broker.go`、`broker/xtp`、`margin/`、`reconciliation/`、`stockstate/` |
| 表名 | **无**（`orders` 表的 DDL 只在**不被执行**的 `migrations/003_create_orders_table.sql`、`migrations/00000003_3_supporting_tables/up.sql:68`、`contracts/kernel_modules.schema.sql:138` ⇒ `migrate()` **不建 `orders`**；`git grep 'CREATE TABLE IF NOT EXISTS orders' -- pkg/storage/postgres.go` = 0） |
| 包路径 | `tellurion/pkg/live`（24 个生产文件） |
| grep 模式 | ① `"tellurion/pkg/live"`（直包）② `tellurion/pkg/live/`（子包，含 `reconciliation`/`margin`/`broker/xtp`） |
| 今天的基线 | ① **18 个文件 / 20 行** import `"tellurion/pkg/live"` ② 子包引用 **5**（`reconciliation` / `margin` / `broker/xtp` 等）；`NewPostgresOrderStore` 生产调用 **0**（只在定义处） |
| 归零目标 | ①②0 |
| 反向护栏 | repoguard：全仓禁 import `tellurion/pkg/live`（broker 服务经 HTTP/契约对接），改坏应红 |
| 阻塞 | `SVC-BROKER-G1`（订单 HTTP 面）+ `broker.order_acks` 的 ack 往返先要通；`tellurion-broker` 今天只有 `internal/reconcile` 的**快照**对账，不是 writer 对拍 |

### 4.7 `SVC-CORE-AI-RETIRE`

| 维度 | 内容 |
|---|---|
| 删除对象 | 7 张表的 core DDL/索引 + `pkg/storage/{experiments,factor_hypothesis}.go` + `SeedBuiltinFactorHypotheses` 的启动 seed + `pkg/mining` 的 AI 落库腿 |
| 表名 | `experiments, factor_hypothesis, factor_genes, strategy_genes, research.profile, research.conclusion, research.question`（7） |
| 包路径 | `tellurion/pkg/storage`, `tellurion/pkg/mining`, `tellurion/cmd/data/mining_cmd.go` |
| grep 模式 | ① `(INSERT INTO\|UPDATE\|DELETE FROM)[[:space:]]+(experiments\|factor_hypothesis\|factor_genes\|strategy_genes\|research\.(profile\|conclusion\|question))\b` ② `SeedBuiltinFactorHypotheses` ③ `CREATE TABLE IF NOT EXISTS (experiments\|factor_hypothesis\|factor_genes\|strategy_genes\|research\.(profile\|conclusion\|question))` |
| 今天的基线 | ① **6**（`experiments.go:108/:171/:200/:304`、`factor_hypothesis.go:35/:108`） ② **3**（定义 + `postgres.go:47` 调用 + 注释） ③ **7** |
| 归零目标 | ①0 ②0 ③0（只剩 agent 仓的 `ai.*`） |
| 反向护栏 | 已有可复用：`tellurion-agent:pkg/aistore/schema_core_drift_test.go:117`（核心侧列集变了会红）、`cmd/ai/experiment_stopwrite_integration_test.go:28`（`public.experiments` 冻结断言） |
| 前置 | `postgres.go:47` 的 seed 删除会让「假设来源查不到 → 回退代码内置表」路径重新变成唯一路径，`factor_hypothesis.go` 的注释要同批改 |

---

## 5. 组内顺序与理由

### 5.1 固定顺序

```
DEPLOY-01 → DUAL-01 → RETIRE-INDEX → DATA → RUN → STRAT → RISK → AN → BROKER → AI
```

| 序 | 单 | 为什么必须在这个位置 |
|---|---|---|
| 1 | `SVC-DEPLOY-01` | 它决定「谁发布 8081 / 哪些二进制进镜像」。**先做它**，后面每条退役删 `cmd/*` 时才知道要不要同步改 `Dockerfile.service` |
| 2 | `SVC-DUAL-01` | 退役的**前置**：不知道「谁是新 owner」，删了也没人接。且它要先裁 `market.index_daily` 形状冲突（§2.5） |
| 3 | `SVC-RETIRE-INDEX` | 它给出「删索引的许可与判据」。**没有 P3 的拍板，DATA/AI 的 DDL 删除无法验收** |
| 4 | `DATA` | 七条里唯一**写链已不可达**的一条（§2.4）⇒ 最快见效；且它另触 `Dockerfile.service`，必须紧跟 DEPLOY-01 |
| 5 | `RUN` | 与 DATA 无表交集，但要等 orchestration 的 WF 写者 |
| 6 | `STRAT` | 软删语义要对齐（§2.3.3），比 RUN 多一道 |
| 7 | `RISK` | 不是表退役；等 `contracts/risk` 的 `bundle → RiskPolicy` 接上 |
| 8 | `AN` | 依赖 analytics 侧 `analysis.*` 出现真写者 |
| 9 | `BROKER` | 依赖 `SVC-BROKER-G1` 的 HTTP 面 + ack 往返 |
| 10 | `AI` | 最晚：agent 侧虽有 5 件对拍工具（最成熟），但 `ai.*` 的 8 张表与 core 的 7 张是**语义搬迁**不是同表双写，收口成本最高 |

**共同理由（派发原文）**：七条**全部触** `pkg/storage/postgres.go`（1569 行 / 55 条 `CREATE TABLE`）⇒
**必须串行**，一单一次编辑该文件，禁止两个 agent 并行改。`DATA-RETIRE` 另触 `Dockerfile.service`。

### 5.2 跨仓腿（协调者先核，本角色只给清单）

| # | 待核问题 | 落在哪个仓 |
|---|---|---|
| K1 | **切读的接口面**：`/ohlcv/{symbol}` / `/api/v1/ohlcv/bulk` / `/screen` / `/market/index` / `/factors/{name}` 与 data 的 4 条路由不对齐（§2.6 ①） | data + core |
| K2 | `market.index_daily` 形状二选一（§2.5） | data + core |
| K3 | data 的 **10 张**「有表无写者」表是否需要写者：market 侧 7（stocks / dividends / splits / trading_calendar / index_constituents / index_daily / market_activity）+ ingest 侧 3（raw / sync_jobs / sync_schedules）；其中 12 张 A 集表由 `Writer.BulkInsert` 写、`ingest.data_migrations` 由 `visible_at.go:210` 写 ⇒ 23 − 13 = 10 | data |
| K4 | `run.walk_forward_reports` / `run.batch_runs` 的写者归谁 | orchestration |
| K5 | `strategy.strategy_definitions` 与 core `strategies` 的软删/版本语义对齐 | strategy + core |
| K6 | `risk.limits` 从空壳到真真相源的迁移（`bundle → RiskPolicy`） | risk + contracts |
| K7 | `analysis.benchmarks` 写方法生产零调用 ⇒ 谁先接 | analytics |
| K8 | `broker.order_acks` 的 ack 往返 + core `pkg/live` 的 HTTP 替代面 | broker + core |
| K9 | `ai.*` 8 张表与 core 7 张表的**一行一列**对照表（agent 已有 drift test，可直接作基线） | agent + core |
| K10 | 「表在新归属是否已唯一」的最终签字（各仓 owner） | 六服务仓 |

### 5.3 与「四步迁移纪律」的对应

派发原文的纪律：**先建第二写者 → 对拍 → 切读 → 停写**。本文件把第 4 步「停写」翻译成可验收的形态：

| 步 | 机械判据 |
|---|---|
| ① 建第二写者 | 新仓存在 `INSERT/UPDATE` 命中（>0），且**新表 owner 的 DDL 里该表存在** |
| ② 对拍 | §2.6 的「对拍方式」列**不许写「无」**；要么有工具，要么有一次性 SQL 不变式（data `docs/db-schema.md:189` 的三项不变式是现成范式） |
| ③ 切读 | core 侧 `FROM <table>` 归零 **且** 出现对目标仓的 HTTP/契约调用 |
| ④ 停写 | §4 各条的 W=0 + 反向护栏绿 + `pg_indexes` 快照无意外消失 |

---

## 6. 未落项（如实）

本文件是**决策材料**，下列各项**明确未做**，不得按「已完成」理解：

1. **0 行业务代码**：未改 `pkg/storage/postgres.go`（1569 行原样）、未删任何表/索引、未改 `Dockerfile.service`、未改 compose。
2. **未改任何服务仓**：七仓只读取证（`git -C ... grep`），无 checkout、无 `git -C ... add/commit`。
3. **未改 `contracts` / `kit` pin**：core 仍 `contracts v0.6.0` / `kit v0.1.1`（守住禁区）。
4. **未建对拍工具**：§2.6 的「无」是实测现状；建工具属施工。
5. ~~**未跑 Docker 镜像**：daemon 不可达，V5/V6 未验（§1.2）。~~ → **已解除（2026-10-26 复测）**：Engine 29.8.0 可达；V5 镜像构建成功、V6 聚合栈 5/6 healthy；analytics 那条不健康已裁为单独立单修复（裁决记录 #4）。
6. **`check_deploy_consistency.py` 仍 exit 1**（2 条 JWT_SECRET 假阳性），本文件未修（建议另开单）。
7. **一处已知代码/注释漂移，本文件只登记不修**：
   - `pkg/storage/bulk_insert_ddl_test.go:54-55` 的注释指向 `pkg/ai/gene_pool/{factor,strategy}_pool.go`，而 `pkg/ai` 在 core **已不存在**（`Test-Path pkg/ai` = False）；
   - ~~`tellurion-agent:Dockerfile:3-6,41,47` 的注释仍写「agent 的 go.mod 用 `replace ... => ../tellurion`」~~ → **已订正（2026-10-26，agent `cc51e84`）**：Dockerfile 头注释已改写成「S3 已删 `replace`，只需本仓源码 + 版本化依赖」。
8. **六字段正文的现状（2026-10-26 更新）**：`SVC-DUAL-01` / `SVC-RETIRE-INDEX` 已补六字段正文（core `docs/TASKS.md` §六）；`SVC-DEPLOY-01` 记 **🔶**（不标 ✅，剩余腿 = analytics `/healthz` 修复后复跑 healthy）；本文件其余材料仍只是材料。按「未立单不得开工」，未立正文的项仍不可派发。

## 7. 下一步（给协调者）

1. 对 §1.4 D-DEPLOY-1、§3.2 P3（`DROP INDEX`）、§2.5 形状冲突**逐条拍板**；
2. 拍板后按 §5.1 顺序把五单写成六字段正文，第一期只放 `DEPLOY-01` + `DUAL-01`（两者是本文件唯一已取证到位的）；
3. `RETIRE-INDEX` 的正文必须先有 P3 结论，否则 ⑤ 验收写不出来；
4. 七条 RETIRE 的 ⑤ 验收**直接抄 §4 的「grep 模式 + 基线 + 归零目标 + 反向护栏」四列**，不要重新发明判据。
