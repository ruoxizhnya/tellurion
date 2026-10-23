package wasm

// execution_errors.go —— QT-10 ③-②：wasm 宿主侧的三类执行错误归类。
//
// 与 `pkg/strategy` 的同名助手同一口径（判定只用 errors.Is、双 %w 保链、
// 已归类不重复包）。分两处是因为两个包之间没有依赖关系：宿主不该 import
// strategy，strategy 也不该 import 宿主实现。
//
// 归类边界（QT-10 ③-②）：
//   - trap / 预算耗尽 / run deadline 到期 / artifact 校验失败 ⇒ ErrStrategyFault
//     （策略自己跑坏了 —— 引擎必须终止 run，不能跳天）；
//   - abi 族不符 / params 超限 / host 调用缺 BarContext ⇒ ErrContractBreach
//     （跨边界约定被违反）；
//   - wasm 侧**不产生** ErrDataGap：数据缺口来自取数通道，与本包无关。

import (
	"errors"
	"fmt"

	"github.com/ruoxizhnya/tellurion-contracts/engine"
)

// classifyExecutionError 把 err 归入 class 指定的执行错误类。
func classifyExecutionError(class error, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, contracts.ErrDataGap) ||
		errors.Is(err, contracts.ErrStrategyFault) ||
		errors.Is(err, contracts.ErrContractBreach) {
		return err
	}
	return fmt.Errorf("%w: %w", class, err)
}
