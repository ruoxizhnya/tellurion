//go:build !race
// +build !race

package backtest

import "time"

// qt31PerCallBudget 在普通构建下保持 C1 冻结的 symbol scope 单次预算：
// 0 表示沿用 BudgetForScope 的默认 5ms，性能口径一分不放宽。
// race 构建的分档与理由见 qt31_percall_race_test.go。
const qt31PerCallBudget time.Duration = 0
