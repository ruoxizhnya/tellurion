---
status: active
last-verified: 2026-10-20
verified-by: 文档拆仓（2026-10-09）+ 2026-10-20 补 qt-decoupling / qt-component-diagram 指针；同日补 D30 仓清单指针
---

# Quant Trading — 文档入口（core 仓）

本仓是 Quant Lab 的**内核（仪器）**仓库。文档拆仓后（2026-10-09），本仓只保留
**内核自身 + 活跃治理 + 契约**三类文档，其余按 DDD 领域迁出：

| 领域 | 仓库 |
|---|---|
| 内核 / 回测 / 策略 / 表达式 / 执行 | 本仓（`tellurion`） |
| AI 实验员 | [`tellurion-agent`](https://github.com/ruoxizhnya/tellurion-agent) |
| 前端 | [`tellurion-ui`](https://github.com/ruoxizhnya/tellurion-ui) |
| 跨领域 / 高层 / 历史档案 | [`tellurion-docs`](https://github.com/ruoxizhnya/tellurion-docs) |

## 本仓文档

- [SPEC.md](SPEC.md) — 技术规格（API / 契约）
- [ARCHITECTURE.md](ARCHITECTURE.md) — 架构总览（服务拓扑 / 数据归属 / DB schema 唯一真相口径 / 缓存）
- [TASKS.md](TASKS.md) — 活跃任务台账（随代码演进）
- [live-trading.md](live-trading.md) — 实盘 / paper 定位

其它不在入口的目录：

- `design/kernel/` — 模块化内核目标架构蓝图（内核领域）
- `design/qt-decoupling/` — 解耦与模块化重构设计集（ADR-D1~D30；**P0 基线见其 `p0-round2-review.md`**；**分仓见其 `repo-topology-and-naming.md`**；**仓名与 module path 的唯一真相源是 `repo-inventory.md`**）
- `design/qt-component-diagram/` — 全系统构件图（mermaid 源 + 可视化）
- `migrations/` — 数据库迁移（内核的数据层）
- `test-cases/` — 回测用例
- `openapi.yaml` — 后端 API 契约（被 Go `//go:embed` 嵌进二进制，**不可迁出**）
- `archive/` — 内核领域的历史档案（其余 archive 已按领域迁到对应仓）

## ⚠️ 跨仓引用

本仓文档仍引用已迁出的文档（如 `adr/`、`ARCHITECTURE.md`、`hermes/`）。这些链接的
目标真实存在于其它仓，`tools/check_doc_links.py` 已内置迁移白名单，不会误报。
再次迁出文档时，记得把迁出的路径追加进该脚本的 `MIGRATED_PREFIXES` / `MIGRATED_FILES`。
