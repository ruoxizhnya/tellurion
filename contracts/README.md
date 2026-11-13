# `contracts/` —— 运行时数据契约资产（**不是** Go 契约仓）

本目录属于 **core（tellurion）仓**，只装几份**被代码/测试直接读取的数据契约资产**：

| 文件 | 内容 |
|---|---|
| `field_dictionary.yaml` | 字段字典（列名、单位、口径说明） |
| `fundamentals_detail.schema.sql` | 基本面明细表的 schema 镜像 |
| `kernel_modules.schema.sql` | 内核模块表的 schema 镜像 |
| `profile.schema.json` | 研究 profile 的 JSON Schema |
| `snapshot.schema.json` | 快照的 JSON Schema |

它**不是**：

- ❌ `tellurion-contracts` Go 契约仓（四契约本体：`BacktestRequest` / `RunSpec` /
  `BacktestResult` / `ContractsVersion`，以及 `engine` / `broker` 等工具层）；
- ❌ git submodule、也不是 Go package —— 本目录里没有 `.go` 文件，`go build ./...`
  不会编译它；
- ❌ 可以直接改形状的地方：这里的 yaml/sql 与 `tellurion-contracts` 的 Go 类型是
  **两套东西**，同名不同物，改之前先确认你在改哪一种。

**去哪里找什么**：

- Go 契约（类型、版本、路由/内核解析）→ `tellurion-contracts` 仓；
- 本仓的数据契约资产 → 就在本目录；
- 数据归属与写者边界 → [`../docs/ARCHITECTURE.md`](../docs/ARCHITECTURE.md) §2「数据归属」。
