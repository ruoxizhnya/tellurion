# 策略资产服务 API 面与 artifact schema（ADR-D12 配套设计）

> 状态：设计草案，未实现。上一份见 `strategy-loading-externalization.md`。
> 边界（ADR-027 §2.6.1）：**策略服务只管定义，不算信号**。引擎对它的全部依赖 = **2 个读端点**（resolve / artifact）。
> 既有可复用样板（2026-10-19 实测）：`factor_definitions.version_hash CHAR(64)` = canonical JSON 的 sha256（`pkg/domain/factor_def.go:151`），治理层报错 `ErrRegistrationDuplicate` 时明写「a content change must change the version hash」；`quant.external_signals.content_hash`（迁移 048）为可执行字段 sha256，UNIQUE 做幂等与可追溯坐标。

---

## 1. 数据模型（三张表，两新一改）

### 1.1 `strategies` → 升级为 `strategy_definitions`（改）

现状（`pkg/storage/postgres.go:284`）：`strategy_id / name / description / strategy_type / params JSONB / is_active / created_at / updated_at`——**只有管理面元数据，没有载体、没有版本、没有能力声明**，执行路径也完全不查它（策略是编译进二进制或 `.so`）。

新增列：

| 列 | 类型 | 语义 |
|---|---|---|
| `carrier` | TEXT NOT NULL | `builtin \| dsl \| wasm \| so \| external`（表达力分层 L1/L2/L3a/L3b 的载体位） |
| `abi_version` | TEXT NOT NULL | 载体协议版本：`on_bar/1`（v1 per-symbol）、**`on_bar/2`（v2 混合 scope）**、`go-plugin/1`、`dsl/2`。**引擎用它选 host 模块版本与兼容判定** |
| `scope` | TEXT NOT NULL | **`market` \| `symbol`**（D25/abi v2）。决定实例模型与 `on_bar` 调用频率；**必须参与 `version_hash`**（同源码不同 scope 行为不同） |
| `capability` | JSONB | 能力声明：`{"frequencies":["1d"],"markets":["A"],"features":["factor","fundamental"]}` —— 供 `MarketRouter` 按"需求 ∩ 能力"路由（BE-14） |
| `entry` | TEXT | 入口：表达式 DSL 文本 / wasm 导出函数名 / `.so` 符号（默认 `Strategy`） |
| `active_version_hash` | CHAR(64) | 当前生效版本（可空 = 无可用 artifact，fail-closed） |

### 1.2 `strategy_artifacts`（新，artifact 元数据）

| 列 | 类型 | 语义 |
|---|---|---|
| `strategy_id` | TEXT | 组合主键之一 |
| `version_hash` | CHAR(64) | **源码 canonical 化后的 sha256**（同因子侧口径）。组合主键之二 |
| `artifact_hash` | CHAR(64) | **字节码本身的 sha256**——引擎校验的那个 |
| `carrier` / `abi_version` | TEXT | 冗余记录，便于按 abi 检索 |
| `size_bytes` / `memory_limit_bytes` | BIGINT | 体积与内存上限（wasm 校验项） |
| `imports` | TEXT[] | 实际 import 列表（wasm 侧与白名单比对） |
| `toolchain` | TEXT | 编译器与版本，如 `tinygo 0.34`、`go1.25.14` |
| `status` | TEXT | `built → validated → promoted / rejected / deprecated` |
| `storage` | TEXT | `inline`（BYTEA）或 `url`（对象存储/本地目录引用） |
| `compile_log` | TEXT | 编译与静态校验输出 |
| `created_at` / `built_by` | — | 审计 |

约束：`UNIQUE(strategy_id, version_hash)`；`version_hash` 由**源码内容派生**，内容改必须改 hash（沿用因子治理判据）。

### 1.3 `strategy_artifact_blobs`（新，字节码本体，可选分表）

`strategy_id + version_hash` → `bytes BYTEA`。**已裁决（2026-10-21，A 行）：本地栈直接 BYTEA + 体积上限 8 MiB（超限 fail-loud）**；`storage='url'` 时此表可空，走 `artifact_url` 字段指向对象存储/本地目录（列分支保留，暂不实现）。分表的理由：避免字节码拖慢定义表的查询与备份。

---

## 2. `version_hash` 计算规则（沿用因子侧口径）

```go
// canonical：字段排序固定、剔除审计/时间字段，只哈希「影响执行的字段」
b, _ := json.Marshal(def.canonical())   // strategy_id + carrier + abi_version + scope + entry + source
version_hash = hex(sha256.Sum256(b))
```

三条纪律（照抄 `factor_governance.go` 的现成判据）：
1. **内容变 ⇒ hash 必变**（否则 `ErrRegistrationDuplicate` 拒绝登记）；
2. `artifact_hash`（字节码哈希）与 `version_hash`（源码哈希）**两个都要留**——前者给引擎校验传输完整性，后者给"这次回测跑的是哪份源码"留痕；
3. **不可变**：artifact 一旦 `promoted` 即为只读记录，改动 = 新版本。

---

## 3. API 面

### 3.1 写侧（作者 / AI / 编译服务）

| 方法 | 路径 | 用途 | 调用方 |
|---|---|---|---|
| POST | `/api/strategies` | 创建定义（carrier / abi_version / **scope** / capability / entry） | 人、AI pipeline |
| PUT | `/api/strategies/{id}/source` | 上传/更新源码 → 计算 `version_hash` 并登记版本 | 人、AI pipeline |
| POST | `/api/strategies/{id}/build` | 触发编译（异步 job）→ 产出 artifact + `artifact_hash` | AI pipeline、CI |
| POST | `/api/strategies/{id}/versions/{hash}/validate` | 静态校验（imports 白名单 / 体积 / 内存上限） | 编译服务、CI |
| POST | `/api/strategies/{id}/versions/{hash}/promote` | 标为可用（写 `active_version_hash`） | 人（审批） |
| POST | `/api/strategies/{id}/versions/{hash}/deprecate` | 下线（不删除，历史回测仍可解析） | 人 |
| GET | `/api/strategies/{id}/versions` | 版本列表（含 status / hash / toolchain） | UI、AI |

### 3.2 读侧（**引擎只用这两个**）

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | `/api/strategies/{id}/resolve?ref={hash\|latest}` | 解析 `strategy_ref` → `{strategy_id, version_hash, artifact_hash, carrier, abi_version, **scope**, capability, size_bytes, imports, artifact_url}` |
| GET | `/api/strategies/{id}/versions/{hash}/artifact` | 下载字节码（响应头带 `X-Artifact-Hash`；支持 `ETag` 做条件请求） |

### 3.3 与回测的接缝

`contracts.BacktestRequest` 现有 `Strategy string` 字段扩展为 **`strategy_ref`**：

- `"my-momentum"` → 解析为 `active_version_hash`（**结果里回写实际解析出的 hash**）；
- `"my-momentum@3f2a…"` → 锁定版本（**可复现回测与 K15 双跑对齐必须走这个形式**）；
- 解析失败 / 无 active 版本 / carrier 与内核能力不匹配 ⇒ **fail-closed 拒绝**，不回退到"用本地内置同名策略"。

---

## 4. 引擎侧加载器契约（五道门，逐道 fail-closed）

`StrategyRef{ID string, VersionHash string}` → 加载器：

| 门 | 动作 | 失败哨兵错误 |
|---|---|---|
| 1 | **Resolve**：`GET resolve` 拿元数据（开跑前 1 次） | `ErrStrategyNotFound` / `ErrNoActiveVersion` |
| 2 | **Fetch**：`GET artifact` 取字节（1 次，带 `ETag` 缓存；内循环零网络） | `ErrArtifactFetchFailed` |
| 3 | **Verify**：`sha256(bytes) == artifact_hash`；`abi_version ∈ 本内核支持集`；`size/memory` 不超上限；wasm `imports ⊆ 白名单` | `ErrArtifactHashMismatch` / `ErrABIIncompatible` / `ErrArtifactTooLarge` / `ErrImportNotAllowed` |
| 4 | **Compile**：wazero `Compile`；失败即拒（NT 侧为 wasmtime） | `ErrCompileFailed` |
| 5 | **Mount**：按 `artifact_hash` 缓存编译产物 → 挂到 `SignalProvider` | `ErrMountFailed` |

两条硬约束：
- **缓存键必须是 `artifact_hash`**，不是策略名——同名策略换版本必须重新编译，绝不能命中旧模块。
- **内循环零网络**：回测的 date-loop 里不得有任何远程调用；护栏可写死（AST 禁止 `pkg/backtest` 内循环中出现 HTTP 客户端调用，顺带约束 BE-10 的 `Engine.httpClient`）。

---

## 5. 与双内核的关系（回应 BE-17 / BE-14）

`capability` + `carrier` 直接进入 `MarketRouter` 的路由判据：

| carrier | Go 内核 | NT 内核（Python） | 说明 |
|---|---|---|---|
| `dsl` | ✅ | ✅（需 NT 侧实现同一表达式求值） | 理想态，但要保证两侧求值一致 |
| `wasm` | ✅ wazero | ✅ wasmtime | **双内核唯一天然共享的可执行载体** |
| `so` | ✅（仅 Linux/CGO） | ❌ | 能力声明里排除 ⇒ 路由到 NT 时直接拒绝而非静默降级 |
| `external` | ✅（信号表注入） | 视实现 | L3b，走 `quant.external_signals` 按 `as_of` 消费 |

---

## 6. 迁移路径（从现状出发）

1. `strategies` 加列（幂等 DDL，迁移 049+），`carrier` 对存量回填 `builtin`，`abi_version` 回填 `builtin/1`；
2. 建 `strategy_artifacts` / `strategy_artifact_blobs`（此时为空，不影响生产链路）；
3. 先只让 **DSL 载体**入库（零编译风险，立刻可用）；
4. 引入编译服务后，wasm 版本入库；`.so` 登记但标注 `markets` 能力时排除 NT；
5. **最后**才切换执行路径：引擎从"内置注册表"改为"先查 artifact，未命中再回退内置"——回退期必须打 warning 日志，且 dualrun K15 前后各跑一次对齐。

---

## 7. 未决问题

| # | 问题 | 影响 |
|---|---|---|
| A | ~~artifact 放 PG BYTEA 还是对象存储？~~ **✅ 已裁决（2026-10-21）**：本地栈 PG BYTEA（单 blob ≤ 8 MiB，超限 fail-loud），`storage='url'` 仅保留列分支作逃逸阀、暂不实现 | `storage` 列落地形态已定 |
| B | ~~`strategy_ref` 锁定版本是否为**强制**？~~ **✅ 已裁决（2026-10-19，D25）**：latest 仅作语法糖，提交边界解析并固化 hash；执行/结果只认 hash；CI/双跑/台账直接传 hash | 可复现性与交互便利兼得 |
| C | NT 内核是否实现同一套 on_bar host API？ | 决定 wasm 能否真的"一份跑两核" |
| D | ~~编译服务放 agent 仓（ADR-027 §2.6.1）还是独立？~~ **✅ 已裁决（2026-10-21）**：归 `tellurion-strategy` 仓的独立进程/容器（`cmd/compiler`，独立镜像、锁 toolchain 版本）；不新建第 13 仓、不放 agent 仓；ADR-027 §2.6.1 登记为被修订 | module 拓扑与 toolchain 归属已定 |
