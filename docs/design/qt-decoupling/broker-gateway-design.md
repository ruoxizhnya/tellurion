# 券商/交易通道网关设计（X6 · Broker Gateway）

> ADR-D23 草案 · 2026-10-19 · 关联 ADR-D18（排序「最后做，实盘延迟敏感」）、ADR-D11（live 依赖反转）
> 性质：设计固化，未实施

## 1. 服务定位

**交易通道的统一适配层：把券商柜台 API 归一成内部订单协议。延迟敏感，最后拆（X6）。**

| | 内容 |
|---|---|
| **管** | 券商适配（XTP 及后续）、下单/撤单/查询、成交回报、连接管理与重连、通道级限流 |
| **不管** | 风控裁决（X3 + 进程内 Policy）、订单意图生成（策略/执行算法）、组合会计、合规报送 |

**注意区分两条网关线**（ADR-D19 已覆盖数据侧）：

| 网关 | 内容 | 归属 |
|---|---|---|
| **数据源 connector 网关** | 12 个数据源适配 + 归一化 | X1（已固化，ADR-D19） |
| **交易通道网关**（本文档） | 券商柜台 API 适配 | X6 |

## 2. 现状零件（实测）

| 零件 | 位置 | 说明 |
|---|---|---|
| `XTPTrader` + `Config` + `OrderReq` | `pkg/live/broker/xtp/xtp.go:240/108/213` | 唯一现有券商适配（XTP） |
| `risk.ValidateOrderQuantity` 在 XTP 侧调用 | `xtp.go:387` | ⚠️ **边界注意**：通道里做了数量校验——摘出时应只留**通道级**校验（柜台规则），策略/风控级归 RiskPolicy |
| `LiveTrader` 接口（ExecuteSignal 转发） | `pkg/live/`（LiveBridge 承接，ADR-D11 E1） | 订单生命周期抽象雏形 |
| `MockTrader` | `pkg/live/mock_trader.go` | 回测/仿真共用的假通道——**第二实现**，port 成立判据已满足 |
| 对账 `ReconciliationDispatcher` | `pkg/alert/reconcile.go:35` | 归 X3 监控层，但依赖本网关的成交查询 |
| `paper_session` / `bar_replay_feed` | `pkg/live/` | 纸交易/回放驱动，留实盘运行域 |

**ADR-D11 的 E1（依赖反转）与本线的关系**：`backtest → live` 三处切断（LiveBridge 迁实盘侧）之后，实盘侧持有 `LiveTrader` → 本网关。摘出时网关只暴露**通道协议**，实盘运行域（订单管理、状态机）留在其上层。

## 3. 边界协议（port 草案，contracts）

```go
// contracts —— 券商通道统一协议（归一化后）
type BrokerGateway interface {
    PlaceOrder(ctx, req OrderRequest) (OrderAck, error)   // 幂等键 OrderID
    CancelOrder(ctx, orderID string) error
    QueryOrder(ctx, orderID string) (OrderStatus, error)
    QueryPositions(ctx, account string) ([]Position, error)
    QueryFills(ctx, since time.Time) ([]Fill, error)      // 对账输入
}
// OrderRequest / OrderAck / OrderStatus / Fill = 归一化 DTO（字段与 domain.Order/Fill 对齐）
```

**归一化纪律**（与数据侧 Normalizer 同款思想）：各券商字段映射 + 柜台规则差异（最小单位、涨跌停、T+1）→ **适配器内消化**；对外只出统一 DTO。禁止把 XTP 特有字段泄漏进契约。

**通道级校验 vs 风控校验的分界**（修正现状 `xtp.go:387` 的混用）：

| 校验 | 归属 |
|---|---|
| 最小单位、柜台委托类型、柜台限流 | **网关内**（适配器） |
| 数量上限、集中度、T+1、止损 | **RiskPolicy**（进程内裁决，X3 规则源） |

## 4. 部署形态（为什么不急着独立进程）

- 下单链路是**实盘延迟敏感路径**：进程内 port 调用 → 独立进程 = 每笔多一次网络跳；
- 独立进程的收益（多策略共享通道、通道独立重启）在单账户单实例阶段**兑现不了**；
- 触发拆分进程的条件：多账户/多策略并发共用通道、或需要通道级独立高可用。

**当前形态建议**：`BrokerGateway` 作为 **port + 进程内实现**（XTP 适配器），与实盘运行域同进程；接口按可远程设计（DTO 序列化友好），将来换 HTTP 实现不动调用方（与 agent↔core 的 contracts 思路一致）。

## 5. 数据归属

| 数据 | 归属 | 说明 |
|---|---|---|
| 订单/成交/持仓快照 | **现状 core `quant`**（`quant.orders` / `fills` / `positions` / `portfolio_snapshot`）；**最终归属待裁（U2）**——`run` 已排除（任务域不含订单），将来拆 `exec` schema 须先立 ADR，见 `repo-inventory.md` §3.1 | 网关不持有业务表，只透传柜台状态 |
| 通道配置（账户、连接参数） | 网关自有配置（加密存储，**不进库明文**——安全红线） | 密钥走环境/密钥管理 |
| 对账留痕 | X3 风控服务（`risk` schema） | 网关只供 `QueryFills` 数据 |

## 6. 实盘支持要点（与 X3 协作）

```
策略/execalgo → OrderIntent → [进程内 RiskPolicy.CheckOrder] → BrokerGateway.PlaceOrder
                                                      ↓
                                          X3 聚合监控（异步收 risk_events）
                                                      ↓
                                     熔断指令（circuit_orders）→ 实盘执行层
```

- 熔断生效点在**实盘执行层**（撤单/停机），网关只执行指令；
- 成交回报 → `QueryFills` → 对账（X3）→ 偏离告警（`DualRunBreach` 同款机制）。

## 7. 分期（排最后，X6）

| 阶段 | 内容 | 验收 |
|---|---|---|
| B0 | `BrokerGateway` port 外置 + XTP 实现收口 + 校验分界修正 | MockTrader/XTPTrader 均可作为实现注入 |
| B1 | 归一化 DTO 定稿进 contracts | 第二家券商适配只写映射不改契约 |
| B2 | 进程拆分评估（多账户/高可用触发时） | 换 HTTP 实现，调用方零改动 |

## 8. 风险

| 风险 | 兜底 |
|---|---|
| 券商 SDK（cgo/专有库）污染契约层 | 适配器内消化，contracts 只见 DTO |
| 网络/柜台故障放大成交易风险 | 重连 + 幂等键 + fail-closed（不确认即不重报） |
| 通道校验与风控校验重叠漂移 | §3 分界表 + 护栏（网关内禁 import risk 裁决） |
| 密钥泄漏 | 配置不进库、不进日志（安全红线） |
