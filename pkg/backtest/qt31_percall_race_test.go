//go:build race
// +build race

package backtest

import "time"

// qt31PerCallBudget 是 QT-31 在 -race 构建下的单次调用预算。
//
// 为什么按构建模式分档：C1 冻结的 symbol scope 单次预算是 5ms，那是普通
// 构建下的口径。-race 会给每条内存访问插桩，wazero 解释执行的热点被放大
// 约一个数量级——实测 20 symbols × 200 days 在 race 构建下会摸到 5ms 墙，
// 报 "module closed with context deadline exceeded"（CI 43215419306）。
// 这不是被测行为变化，而是测试前提（5ms 够跑完一次合法调用）在 race 构建
// 下不成立。故 race 构建放宽到 50ms（10×）；race 构建只承担竞态检测，
// 不承担性能断言。普通构建（qt31_percall_norace_test.go）保持冻结的 5ms。
const qt31PerCallBudget = 50 * time.Millisecond
