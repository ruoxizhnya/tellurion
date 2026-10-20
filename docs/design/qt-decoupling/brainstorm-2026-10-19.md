# 解耦架构头脑风暴记录（2026-10-19）

> 配套：[project-engineering-file.md](project-engineering-file.md)（档案与 ADR-D1~D5）
> 本轮新增结论与一条**自我修正**（ADR-D6），以及四个问题的深度展开。

## 0. 与既有 ADR 的对齐（重要，先摆正）

读完 `tellurion-docs/docs/adr/adr-027-modular-decomposition-to-independent-services.md` 后确认：

- ADR-027 已裁决：**先做「进程内核模块化」（P0–P2，在进程内划清 11 模块边界并冻结），服务拆分（5→7）在其后**，且明确写了「服务拆分是**可选的后续部署形态**」。
- 因此本档案 ADR-D1（先端口化、不急着拆进程）**不是新方向，而是 ADR-027 的延续与细化**；以后引用请写「对齐 ADR-027 的 P0–P2 阶段」，不要另起口径。
- ADR-027 §2.6.1 已裁决**策略服务独立**（第四个服务），附加关键限定：**只管定义、不算信号**（原文：「每天把全市场 K 线 POST 给策略服务算信号」这个方向是错的；「传定义，不传信号」，HTTP 边是**开跑前 1 次**而非 per-day/per-bar）。
- ADR-027 §833 已裁决 WASM 三段分工：**编译（`BLD`）在 ai-service → `.wasm` 字节码作为定义资产存策略服务 → wazero runtime（`WRT`）在回测服务**（理由：host API 要逐 bar 喂数据，数据在引擎的 date-loop 里）。

## ADR-D6：修正 ADR-D3 —— 区分「策略资产服务」与「策略运行时」

- 场景：上一轮我把「strategy 独立成服务」整体判为 ❌。读完 ADR-027 后确认该判定**过粗**，需拆分。
- 决策（修正后）：
  - ✅ **策略资产/定义服务**：应独立（ADR-027 §2.6.1 已裁决）。策略 CRUD、因子定义、`.wasm` 字节码、表达式 YAML 都住这里；调用方**开跑前拉一次定义**，不是 per-day/per-bar。
  - ❌ **策略运行时/信号计算服务**：不做。wazero runtime 必须住在回测引擎进程内（逐 bar 喂数据）。
- 理由：这两件事的**调用频率差 6 个数量级**（一次拉定义 vs 每 bar × 每标的调信号），因此必须在不同的边界上。
- 代价：策略资产服务要处理「定义版本 + 校验 + 分发」，是一个真实的 CRUD 服务工作量。
- 状态：**提议**（替代 ADR-D3 的粗判；ADR-D3 的 WASM 插件边界部分仍然成立）

## 1. Q1：为什么不用 HTTP？contracts 放什么？agent 到底要 core 的什么？

### 1.1 为什么 HTTP 不是解耦

HTTP 只改变**传输方式**，不改变**依赖关系**。agent 依赖 core 的最大头是 `pkg/domain`（51 处引用）——那是**类型与语义依赖**，不是调用依赖。走 HTTP 后你仍要：要么继续 import `domain` 来反序列化（依赖没减），要么另造一套 DTO（**两套真相**，漂移风险更高）。

叠加本项目具体代价：
- 回测结果是 bar 级序列，序列化/传输成本与内存放大显著（单机本地栈纯亏）；
- **同参对等**失效：ai-service 现在用与 analysis 逐项对等的装配跑回测（进程内），拆开后双跑对齐（dualrun K15）失去确定性基准；
- 断点续跑、BacktestState 需要跨进程状态；
- 共享同一个 PG，两个进程各写一份 = 分布式单体 + 一致性问题。

### 1.2 contracts 里放什么（分三层）

| 层 | 内容 | 现状 |
|---|---|---|
| L1 边界 DTO / 值对象 | 跨边界才需要的最小类型集（请求/响应/错误） | 部分已有：`BacktestRequest` / `BacktestResponse` |
| L2 接口 / port | `BacktestRunner` · `EngineRunner` · `MarketRouter` + 待补 `StrategyRuntime` / `RiskPolicy` / `FactorPort` / `MinerPort` / `StoragePort` | **已有 3 个**（`contracts/runner.go`、`contracts/contracts.go`、`contracts/marketroute.go`） |
| L3 错误语义 | 哨兵错误（`ErrMarketNotRegistered` / `ErrKernelNotRegistered`）+ fail-closed 约定 | **已有**（marketroute.go） |

**关键取舍：domain 类型要不要下沉？**

| 选项 | 做法 | 收益 | 代价 | 建议 |
|---|---|---|---|---|
| A | 只下沉接口与边界 DTO，`domain` 仍留 core | 依赖面从「core 全导出面」→「接口面 + domain」；改动小 | agent 仍 import core 的 domain（不彻底） | **先做 A** |
| B | `domain` 值对象整体下沉为 `quant-trading-domain` module | 彻底解耦 | core 内部 13 个包依赖 domain；且 `domain→fees`（execution.go）耦合要先解，迁移面大 | P2 候选 |

B 的触发条件：出现**第二个需要依赖 domain 的独立部署单元**（如独立的矿业/扫描服务）时再下沉，否则 A 足够。

### 1.3 agent 需要 core 的四类东西（实测）与处置

| 类别 | 实测引用 | 性质 | 处置 |
|---|---|---|---|
| ① 领域类型 | `domain`×51 | 语义依赖（非调用） | **保留**库依赖（或按 1.2 选项 B 择机下沉） |
| ② 仪器能力 | `backtest/contracts`×5 · `backtest`×5 · `strategy`×12 · `expression`×10 · `risk`×3 · `marketdata`×3 · `strategy/expression`×3 | 真正要解耦的部分 | **port 化**（这是 P0 的主战场） |
| ③ 存储 | `storage`×14 | 实验日志 / gene_pool 落库 | **`StoragePort` 化** |
| ④ 装配与基建 | `bootstrap`×3 · `httpserver`×3 · `auth`×2 · `observability`×1 · `sandbox/{staticcheck,runner}`×3 | 复用 core 的服务骨架与沙箱 | **保留**库依赖（合理，不是业务耦合） |

结论：**真正需要 port 化的只有 ②④ 两类**，工作量远小于「全部 HTTP 化」。

## 2. Q2：WASM 插件怎么工作？为什么当初选它？

### 2.1 工作机制（对照 `pkg/sandbox/wasm/host_api.go` 实测）

- 会话模型：`on_bar(t)` 逐 bar 推进，host 以模块名 `env` 导出白名单函数。
- 白名单：`get_bar(t_offset, field_id)` · `get_series_len` · `get_symbol_count` · `get_cross_section(t_offset, field_id, idx)` · `get_state` / `set_state` · `emit_signal` · `host_log`。
- 前视防护：`trapFuture`（t_offset > 0 直接 trap）、`trapNoContext`（无回放上下文直接 trap）；`ValidateImports` 编译期**拒绝任何非 `env` 模块的 import**。
- 调用链：策略（wasm）→ `emit_signal` → 引擎侧收集信号 → 风控裁决 → 撮合。

### 2.2 为什么选它（ADR-007 + ADR-029 原文口径）

1. **前视隔离是唯一可行机制**：早期实现把全部 bars 交给沙箱，沙箱内读 `bars[len-1]` 就能拿到未来数据；必须改成逐 bar + `get_bar(t_offset ≤ 0)` + 越界 trap。
2. **AI 生成的代码不可信**：必须有强隔离（不能只靠 staticcheck 正则）。
3. **wazero 是 Go-native**（无 cgo / 无外部运行时），部署与 core 同构。
4. **表达力分层（ADR-028 / 内核 §6）**：WASM = **L3a，是最后一层**——能用 L2 确定性有状态算子（EWMA / RMA / IIR / Kalman）表达的不允许上 L3（D2 约束）。

### 2.3 与「策略独立」的关系（见 ADR-D6）

编译（ai-service）→ 字节码存策略服务（定义资产）→ 执行（wazero runtime 在回测引擎进程内）。

## 3. Q3：「解耦 = 定义边界 = contracts」到底是什么意思

一句话：**解耦不是「不调用」，而是「只按契约调用，且契约由第三方（contracts module）持有，任何一方都不能单方面改」**。

### 3.1 现成样板（你的仓库里已经有了）

`contracts.EngineRunner` 的存在理由写在 `contracts.go` 注释里：walkforward / batch / job **接受这个接口而不是 `*Engine`，以打破它们被迫 import 父包 `pkg/backtest` 的 import 环**。这就是「定义边界」的完整形态：调用方只认接口，实现方可替换。

### 3.2 风控的解耦长什么样（草案，签名需对照 `pkg/risk/manager.go` 收敛）

现状：`backtest` 直接 import `pkg/risk`（实测 9 处）→ 换风控实现要动回测。
目标：`contracts` 持有 `RiskPolicy` port，`backtest` 只依赖它：

```
// 草案：签名需与 pkg/risk/manager.go 的现有方法对齐后定稿
type RiskPolicy interface {
    Evaluate(ctx context.Context, intent OrderIntent, state PortfolioState) (Verdict, error)
}
```

收益：换风控实现（外部风控服务 / 规则引擎 / 更严的 regime 判断）**不改回测一行**；风控可独立版本化、独立测试。

### 3.3 边界成熟度三档（用来验收，而不是凭感觉）

| 档 | 形态 | 验收标准 | 当前分布 |
|---|---|---|---|
| ① | 有接口，但调用方仍直接 import 实现包 | — | 大部分（如 risk） |
| ② | 调用方只依赖接口，实现在装配时注入 | 把实现换成 mock，调用方**编译通过且行为不变** | `EngineRunner`（walkforward/batch/job）已达成 |
| ③ | 接口外置到 contracts module，且可挂远程实现 | 同一接口可有本地/远程两种实现 | 目标态 |

**判据很简单：能不能把实现换成 mock 而调用方零改动。做不到，边界就没成立。**

## 4. Q4：能力化 + 注册表驱动（模型细化）

底层 = **能力集合**：每个能力 = 接口 + ≥1 个实现 + 注册表 + **能力声明（capabilities）**。
上层应用 = **装配（wiring） + 编排（orchestration） + 协议（HTTP / CLI / MCP）**。

装配示例（示意，非现有配置格式）：

```
app: analysis-service
needs:
  - BacktestEngine[go]        capabilities: {freq: [1d,1m], market: [CN], auction: true}
  - BacktestEngine[nautilus]  capabilities: {freq: [1d,15m], market: [US], intraday: true}
  - RiskPolicy[default]
  - MarketData[D1]
  - Storage[postgres]
provides:
  - HTTP :8085  (handlers 25+ 族)
```

纪律（否则能力集会重新耦合）：
- **能力之间不横向互调**（当前 `backtest → live` / `backtest → risk` 就是横向互调）；
- 需要协作时，要么经内核 msgbus（已存在 `exec.order_intent` / `exec.fill`），要么在装配清单里**显式声明依赖的能力**，由装配器注入；
- 每个 port 必须有 **≥2 个真实实现**才允许存在（防止接口空转，档案 §6 风险 2）。

对应用户三个目标的论证：
- **廉价拓展** = 注册一个新实现（新因子 / 新挖掘算法 / 新引擎 / 新数据源），不改内核；
- **易维护** = 换实现不动调用方（边界档位 ② 的验收标准）；
- **单独演进** = 能力可版本化，应用按能力声明选择。

## 5. 待办更新（在档案 §7 基础上）

- P0 增加：按 1.2 选项 A 抽 `tellurion-contracts`（先只放接口 + 边界 DTO + 哨兵错误），并**明确记录「domain 暂不下沉」的决策与触发条件**。
- P0 增加：按 1.3 只 port 化 ②④ 两类依赖，① ④ 明确「保留库依赖」并写进护栏白名单（避免后人误判为违规）。
- ADR-D6 落地时才动策略资产服务（依赖 ADR-027 §5 第 6 步：先去 `GlobalPluginLoader` / `DefaultRegistry` 全局单例）。
- 档案 §3 的 ADR-D3 标注为「**已由 ADR-D6 细化**」（ADR 只增不删）。
