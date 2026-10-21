# 风控服务设计（X3 · External Risk Control）

> ADR-D20 草案 · 2026-10-19 · 关联 ADR-D18（服务拆分统一模式）、ADR-D11（回测摘出）、ADR-D19（数据归属）
> 性质：设计固化，未实施

## 1. 服务定位

**规则真相源 + 跨策略/账户聚合监控 + 熔断指令下发。不进内循环。**

| | 内容 |
|---|---|
| **管** | 风控规则/限额的定义、版本、审批、审计；规则快照分发（run 前 1 次）；跨策略/账户/实盘账户的聚合监控；熔断与降级指令下发；合规检测与报送；告警编排 |
| **不管** | 逐 bar/逐笔同步裁决（归进程内 `RiskPolicy`）；订单执行；策略逻辑 |

**核心裁决（两层，不可合）**：

| 层 | 位置 | 频率 | 职责 |
|---|---|---|---|
| **规则真相源 + 监控**（本服务） | 独立服务 | 异步/秒级 | 规则 CRUD+版本、聚合看板、熔断下发、审计留痕 |
| **同步裁决** | 内核/实盘进程内 | 逐 bar/逐笔 | `RiskPolicy.CheckOrder(intent) → Verdict`，零网络 |

实盘与回测调用**同一个 `RiskPolicy` 实现**（回测可信与 K15 对齐的前提）。进程内 Policy 在 run 前从本服务拉**规则快照**（内容寻址），run 期间零网络。

## 2. 现状零件（实测，可直接搬）

| 零件 | 位置 | 迁移后归属 |
|---|---|---|
| `RiskEngine` 接口（CalculatePosition / DetectRegime / CheckStopLoss / **CheckOrder**） | `pkg/risk/interfaces.go:65` | contracts（port）+ 内核侧实现 |
| `OrderIntent` / `Verdict`（**fail-closed 冻结**：零值即拒绝） | `pkg/risk/interfaces.go:29/46` | contracts（DTO） |
| `RiskManager`、`RegimeDetector`、`StopLossChecker` | `pkg/risk/` | **进程内 RiskPolicy 实现**（留 core） |
| `TakeProfitRule`（Fixed / Trailing / Tiered）+ `RuleBinding` | `pkg/risk/take_profit.go` | 进程内 + 规则配置来自本服务 |
| 异常交易 `Detector` + `Thresholds` | `pkg/compliance/abnormal_trade.go:117/167` | 本服务聚合监控层 |
| 减持合规 `DivestmentChecker` + `DefaultDivestmentRules` | `pkg/compliance/divestment.go` | 本服务 |
| 大额交易报送 `LargeTraderReporter` | `pkg/compliance/reporter.go:113` | 本服务 |
| `AlertManager`（6 内置检测器）+ `Channel`（Log/Recorder/Webhook） | `pkg/alert/manager.go:232` | 本服务告警编排 |
| `ReconciliationDispatcher` / `SignalDispatcher` / `DualRunBreach` | `pkg/alert/reconcile.go` | 本服务（对账/双跑偏离告警） |
| `audit_logs` 表 | `pkg/storage/postgres.go` | 迁 `risk` schema |

> ⚠️ **2026-10-21 勘误（system-spec-review 残留B）**：上行「`audit_logs` → 迁 `risk` schema」误导——现 `audit_logs`（`pkg/storage/postgres.go:335`，public schema）是 ADR-017 鉴权访问审计（`pkg/auth` 写入，见 `pkg/auth/auth.go:492` `RecordAudit`），**留 core 不迁**；`risk.audit_logs` 应为**新建表**，与现表无迁移关系（见 §3 勘误）。

## 3. 数据归属（schema = `risk`，DDL 唯一 owner）

| 表 | 内容 | 现状 |
|---|---|---|
| `risk.rule_defs` | 规则定义（类型/参数/适用范围），带 `version_hash` | **待建**（沿用 `factor_definitions.version_hash` 样板） |
| `risk.rule_revisions` | 版本追加 + 审批状态（draft→approved→active→deprecated） | 待建 |
| `risk.rule_snapshots` | run 前下发的规则快照（内容寻址，回测结果记 `risk_rules_hash`） | 待建 |
| `risk.limits` | 账户/策略/标的级限额 | 待建 |
| `risk.risk_events` | 裁决事件留痕（`OrderIntent` 注释已指向 `quant.risk_events`，**表未建**） | 待建，迁 `risk` |
| `risk.circuit_orders` | 熔断指令下发记录（幂等） | 待建 |
| `risk.audit_logs` | 操作审计 | 从现 `audit_logs` 迁入 |

> ⚠️ **2026-10-21 勘误（system-spec-review 残留B）**：上行「从现 `audit_logs` 迁入」不成立——现 `audit_logs` 是 ADR-017 鉴权访问审计（`pkg/auth` 写入），语义与风控操作审计不同、留 core；`risk.audit_logs` 应为**新建表**（风控规则审批/操作留痕），与现表无迁移关系。

**红线**：`risk` schema 只有风控服务能写；内核与实盘只经 port 读快照、写事件经端口回传（或由服务侧异步收集）。

## 4. API 面

### 规则管理（写侧，人/审批）
- `POST /api/risk/rules` — 建规则（draft）
- `PUT /api/risk/rules/{id}` — 改规则 → 生成新 revision
- `POST /api/risk/rules/{id}/approve` — 审批激活（产生新 `version_hash`）
- `GET /api/risk/rules?scope=&active=true` — 查询
- `GET /api/risk/snapshots/{hash}` — 取规则快照（内容寻址）

### 运行接入（读侧，内核/实盘）
- `POST /api/risk/resolve` — 入参：规则集引用（锁定 hash 或 latest）→ 返回快照元数据
- `GET /api/risk/snapshots/{hash}/bundle` — 拉规则包（run 前 1 次，缓存按 hash 永不失效）
- `POST /api/risk/events` — 回传裁决事件（异步批量，非热路径）
- `POST /api/risk/breakers` — 下发熔断指令（幂等键）

### 监控（聚合层）
- `GET /api/risk/monitor/exposure` — 跨策略/账户敞口
- `GET /api/risk/monitor/alerts` — 告警列表
- `POST /api/risk/monitor/evaluate` — 手动触发检测（compliance 六检测器 + 减持 + 大额）

**HTTP 粒度纪律**（ADR-D7）：`resolve/snapshot` 是 run 前粗粒度；`CheckOrder` **永不走 HTTP**（进程内 port）。

## 5. 与内核的边界（port 草案）

```go
// contracts 包（tellurion-contracts）
type RiskPolicy interface {
    CheckOrder(ctx context.Context, intent OrderIntent) (Verdict, error)  // fail-closed
    CalculatePosition(...)  // 同 RiskEngine，冻结签名
    DetectRegime(...)
    CheckStopLoss(...)
}
type RiskRuleSource interface {
    ResolveSnapshot(ctx context.Context, ref string) (*RuleSnapshot, error) // ref = "ruleset@hash" | "ruleset"
}
```

- 内核/实盘持有 `RiskPolicy` 实现（core），规则参数来自 `RuleSnapshot`；
- `Verdict` fail-closed 语义已在 `interfaces.go:53` 冻结（`TestVerdictFailClosed` 强制），contracts 外置后测试一并迁移；
- 回测结果记 `risk_rules_hash`（三 hash 之一，X0）。

## 6. 实盘支持（必须保留的差异点）

| 维度 | 回测 | 实盘 |
|---|---|---|
| 时钟 | 数据时间 | 墙钟（`OrderIntent.Ts` 注释已区分） |
| 裁决 | 进程内 Policy | **同一 Policy** |
| 聚合监控 | run 结束后批量评估 | **实时**（秒级 Evaluate） |
| 熔断 | 不适用 | 下发 `circuit_orders` → 实盘侧执行（撤单/停机） |
| 对账 | — | `ReconciliationDispatcher` 已有 |

## 7. 分期

| 阶段 | 内容 | 验收 |
|---|---|---|
| R0 | 规则表 + `version_hash` + 快照下发（复用 factor 样板） | 回测结果能记 `risk_rules_hash` 并复现 |
| R1 | `risk_events` 落库 + compliance/alert 聚合监控迁入 | 六检测器在服务侧跑通 |
| R2 | 熔断指令通道 + 实盘挂钩 | 熔断能在实盘侧生效且留痕 |
| R3 | 规则审批流 + 审计 | 未审批规则不能进生产快照 |

## 8. 风险

| 风险 | 兜底 |
|---|---|
| 规则快照与进程内 Policy 版本漂移 | 快照带 `version_hash`，结果记 hash，dualrun 对拍 |
| 误把 CheckOrder 放到 HTTP | 护栏：AST 禁内循环 HTTP（同 G 线约束） |
| 熔断指令重复下发 | 幂等键 `circuit_orders` UNIQUE |
| 合规规则集变化频繁 | revision 追加制，旧快照永远可复现 |
