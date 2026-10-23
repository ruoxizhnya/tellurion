package backtest

// run_failures.go —— QT-10 ③-④/⑤：run 级失败诊断（Failures）。
//
// 定位：`Failures` 是**诊断明细**，不是状态来源。冻结三值
// `completed / invalid / failed` 的唯一产出点仍是 `RunBacktest` +
// `buildBacktestResponse`（见 contracts API.md §5.3 的「禁止第四值」）。
// 本文件只回答「哪一天、哪一类、什么原因」，并在终止路径上把这份信息
// 拼进返回的 error —— status=failed 时 error 必填，读方无需新增字段。
//
// 分类边界（contracts API.md §3.4）：
//   - `errors.Is(err, ErrStrategyFault)` ⇒ strategy_fault；
//   - 其余（含 `ErrContractBreach` 与**无法归类**）⇒ contract_breach。
//     无法归类按 fail-closed 处理：宁可停，不要跑出漂亮但错的曲线。

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/engine"
)

// FailureClass 是终止类失败的稳定分类名（诊断用，不是状态）。
type FailureClass string

const (
	// FailureStrategyFault 对应 contracts.ErrStrategyFault（trap / 非法信号 /
	// 预算超限 / artifact 校验失败）。
	FailureStrategyFault FailureClass = "strategy_fault"
	// FailureContractBreach 对应 contracts.ErrContractBreach，同时是
	// 「无法归类」的兜底类。
	FailureContractBreach FailureClass = "contract_breach"
)

// RunFailure 是一条被点名的失败现场。
type RunFailure struct {
	Date   time.Time
	Class  FailureClass
	Reason string
}

// classifyFatal 把终止类错误归到两个诊断类之一（fail-closed：未知按违约为准）。
func classifyFatal(err error) FailureClass {
	if errors.Is(err, contracts.ErrStrategyFault) {
		return FailureStrategyFault
	}
	return FailureContractBreach
}

// runFailures 收集本 run 的终止类失败（诊断明细）。
type runFailures struct {
	items []RunFailure
}

// record 记一条失败现场，返回落库形状的记录供日志/错误串复用。
func (f *runFailures) record(date time.Time, class FailureClass, err error) RunFailure {
	rec := RunFailure{Date: date, Class: class, Reason: err.Error()}
	if f != nil {
		f.items = append(f.items, rec)
	}
	return rec
}

// items 返回只读快照（诊断消费者/测试用）。
func (f *runFailures) snapshot() []RunFailure {
	if f == nil || len(f.items) == 0 {
		return nil
	}
	out := make([]RunFailure, len(f.items))
	copy(out, f.items)
	return out
}

// terminateError 把现场记录变成返回给 RunBacktest 的终止错误：日期、类、
// 原因三样都在 error 里（status=failed 的 error 必填），并用 %w 保住原链。
func terminateError(rec RunFailure, err error) error {
	return fmt.Errorf("backtest terminated at %s [%s]: %w",
		rec.Date.Format("2006-01-02"), rec.Class, err)
}

// logLine 是日志里的人读版本（诊断明细按发生顺序，最多几条由调用方决定）。
func (f *runFailures) logLine() string {
	items := f.snapshot()
	if len(items) == 0 {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, fmt.Sprintf("%s/%s: %s",
			it.Date.Format("2006-01-02"), it.Class, it.Reason))
	}
	return strings.Join(parts, " | ")
}
