# QT-21 · 旧序 vs 新序差异报告（归档）

> 日期：2026-10-24（G1/core）
> 对应工单：`docs/TASKS.md` QT-21 ③-④「先跑旧顺序 vs 新顺序差异报告，差异可解释后再切换」
> 结论：**切换已生效**。差异全部可解释为「排序 + QT-04 合并规则」的直接结果，没有出现无法归因的变化。

## 1. 复现命令

```bash
go test ./pkg/backtest -run TestQT21_OldVsNewOrderDiffReport -count=1 -v
```

## 2. 实测输出（2026-10-24，本机 Go 1.25.0 windows/amd64）

```text
=== RUN   TestQT21_OldVsNewOrderDiffReport
    qt21_signal_order_test.go:291: QT-21 old vs new order:
        input=7 output=5 duplicates_merged=2 positions_changed=4
        old=[600003.SH:long:0.3000:0.3000 600001.SH:long:0.2000:0.2000 600002.SH:short:0.9000:0.9500 600001.SH:long:0.7000:0.7500 600002.SH:long:0.9000:0.9500 600004.SH:close:0.4000:0.4000 600004.SH:long:0.8000:0.8000]
        new=[600001.SH:long:0.9000:0.7500 600002.SH:long:0.9000:0.9500 600002.SH:short:0.9000:0.9500 600003.SH:long:0.3000:0.3000 600004.SH:close:0.4000:0.4000]
--- PASS: TestQT21_OldVsNewOrderDiffReport (0.00s)
PASS
```

字段格式：`symbol:direction:strength:composite_score`。

## 3. 差异逐条解释

| # | 旧序（来源顺序） | 新序（规范化后） | 差异原因 |
|---|---|---|---|
| 1 | `600003` 排在第一位 | 移到 `600002` 之后 | 排序键 `Symbol asc` |
| 2 | `600001` 两条 long（0.2 / 0.7）分开出现 | 合并成一条 long，strength `0.9`，composite 取 `0.75` | 同 bar 同向 strength 求和（未超 1，不触发 clamp） |
| 3 | `600002` short 与 long 各一条 | 保留两条，long 在 short 前 | 方向固定序（close=0 < long=1 < short=2）；相反意图不合并 |
| 4 | `600004` close + long | 只剩 close（strength 0.4） | **close 优先**：long 被丢弃 |
| 5 | 输入 7 条 | 输出 5 条，`duplicate_signals_merged=2` | 上面第 2、4 条各合并掉 1 条 |

`positions_changed=4` 是「同一位置上的信号身份变了」的计数，不是错误：旧序本来就没有定义，任何依赖它的读法都必须按新序重读。

## 4. 与存量策略的关系（如实登记）

- 真正被点名的非确定源是 `pkg/strategy/examples/value_momentum.go` 的 `for symbol := range map`。该策略的 `GenerateSignals` 当前是 **stub**（返回空信号集，PE/PB/ROE 需要接口尚未承载的基本面），所以本仓今天无法用它跑出真实的旧序差异。
- 上表用一份固定夹具模拟「map 遍历 + 重复」的形态；`TestEngineSignalSequence_TwentyRunsIdentical` 用同样的形态在真实回测路径上跑 20 次，观测到的规范化序列逐位一致（引擎级证据）。
- `TestValueMomentum_TwentyRunsBitwiseIdentical` 按 QT-21 ⑤ 原文跑了 20 次 `value_momentum` 回测并逐位比较净值/成交；由于 stub 无信号，这条腿只证明「整轮产出确定」，不冒充信号顺序证据。

## 5. 未做 / 下一版开关

- `trap`（重复即失败）**未启用**，按 QT-04 结论留作存量迁移完成后的下一版开关；本单只落地 `merge`。
- 单量 / 仓位合并仍归 QT-24 的 `emit_target2`，本单不动。