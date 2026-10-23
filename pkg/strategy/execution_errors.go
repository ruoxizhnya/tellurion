package strategy

// execution_errors.go —— QT-10 ③-②：provider 侧的三类执行错误归类。
//
// 三类哨兵住 `tellurion-contracts:engine`（`ErrDataGap` / `ErrStrategyFault` /
// `ErrContractBreach`），本文件只负责把**本包产生的**底层错误归入其中一类。
// 判定与包装口径（contracts API.md §3）：只用 errors.Is，包装用双 %w 保链，
// 已有分类信息的内层错误不重复包 —— 否则「哪一类」会取决于包装顺序。
//
// 为什么单独成文件而不是内联：归类点有六处（会话门、策略解析 ×2、流式
// OnBar、批式 GenerateSignals、防前视），复制六遍 `fmt.Errorf("%w: %w", …)`
// 就一定会有一处写错类，而写错类的后果是「trap 被当成数据缺口跳过」。

import (
	"errors"
	"fmt"

	"github.com/ruoxizhnya/tellurion-contracts/engine"
)

// classifyExecutionError 把 err 归入 class 指定的执行错误类。
//
// 已带三类之一的错误原样返回（内层分类优先）；err 为 nil 时返回 nil。
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
