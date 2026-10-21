## 变更说明

<!-- what/why，不要写 how。若对应施工单，写单号（QT-* / *-*）。 -->

## agent 编译兼容（QT-18 门禁）

S0~S3 期间 agent 编译期依赖 core 源码，跨边界的改动**默认是 breaking**。请按下述其一勾选：

- [ ] 本次改动不触碰被 agent 消费的 core 导出（`pkg/**` 对外签名/类型/常量）
- [ ] 已跑 `sh tools/ci/agent_compat.sh` 并通过，输出贴在下方（与 CI 同一份脚本；CI 的 `agent-compat` job 也会真跑，本地自检是先于推送的快速反馈）
- [ ] 有意 breaking：附 agent 侧适配 PR 链接，**并**在 `.github/agent-compat-known-break` 登记带单号的 known-break（合入后尽快清除）

```
$ sh tools/ci/agent_compat.sh
<粘贴输出>
```

> `agent-compat` job 通过匿名 checkout 拉 public 的 agent 仓（2026-10-21 起无需 PAT），
> 在本 PR 上直接编 agent —— 红即代表破了编译；本地脚本自检用于推送前快速反馈。

## 文档同步（变更触发，见 docs/README.md §3）

- [ ] 改表 → `docs/db-schema.md` / 改接口 → `docs/api-schema.md` / 改链路 → `docs/dataflow.md`
- [ ] 改分层 → `docs/ARCHITECTURE.md` + 一条 ADR；删档/迁移/换工具 → 一条 ODR
- [ ] 以上都不适用

## 提交前门禁

- [ ] `go build ./...` 绿
- [ ] `go vet ./...` 无警告
- [ ] `gofmt -l .` 无输出
- [ ] 受影响包 `go test ./... -count=1 -race` 绿
- [ ] 一个 commit 只做一件事；新红已登记（不当既有噪声）
