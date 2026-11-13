package main

// backtest_request_mode.go —— CORE-BACKTEST-REQUEST-UNION（2026-11-13 档 3-4）。
//
// `POST /api/backtest` 的请求体有两种形状：同步信封（`strategy` + `stock_pool`
// + 日期，直接跑完返回结果）与异步任务（`strategy_id` + `universe` + 日期，
// 交编排排队）。旧实现靠「strategy_id 和 universe 同时非空」猜：
//   - 两类字段一起带 → 静默走异步，同步字段被无视；
//   - 只有 strategy_id 没有 universe → 掉进 legacy 分支报 "strategy required"，
//     真实原因是缺 universe，错误信息与事实不符。
//
// 现在改为显式判别：
//   - 可选 `mode`：`sync` / `async`，给了就按它走，缺字段/冲突字段直接 400；
//   - 没给 `mode` 时按字段判别（一个兼容周期），但**混用直接 400**，
//     **半套异步体 400 点名缺的字段**；
//   - legacy 信封继续可用，同步响应附 `Deprecation` 头（body 逐字节不变）。

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	backtestModeSync  = "sync"
	backtestModeAsync = "async"
)

// classifyBacktestRequest 决定请求走哪条分支，返回 backtestModeSync / backtestModeAsync。
// 返回的 error 是可直接回给调用方的 400 文案（点名冲突/缺失字段）。
func classifyBacktestRequest(body []byte) (string, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", fmt.Errorf("request body must be a JSON object: %v", err)
	}

	mode := strings.TrimSpace(stringField(raw, "mode"))
	switch mode {
	case "", backtestModeSync, backtestModeAsync:
		// 合法（空 = 未显式指定，按字段判别）
	default:
		return "", fmt.Errorf("mode=%q is not supported (want %q or %q)", mode, backtestModeSync, backtestModeAsync)
	}

	hasStrategy := stringField(raw, "strategy") != ""
	hasStockPool := len(raw["stock_pool"]) > 0 && string(raw["stock_pool"]) != "null" && string(raw["stock_pool"]) != "[]"
	hasStrategyID := stringField(raw, "strategy_id") != ""
	hasUniverse := stringField(raw, "universe") != ""

	legacyFields := make([]string, 0, 2)
	if hasStrategy {
		legacyFields = append(legacyFields, "strategy")
	}
	if hasStockPool {
		legacyFields = append(legacyFields, "stock_pool")
	}
	asyncFields := make([]string, 0, 2)
	if hasStrategyID {
		asyncFields = append(asyncFields, "strategy_id")
	}
	if hasUniverse {
		asyncFields = append(asyncFields, "universe")
	}

	// 规则 1：混用 → 400，点名冲突字段。这条比任何「优先走哪边」都安全：
	// 静默走异步会把调用方以为生效的同步参数丢掉。
	if len(legacyFields) > 0 && len(asyncFields) > 0 {
		return "", fmt.Errorf(
			"ambiguous request body: sync fields [%s] and async fields [%s] cannot be combined; "+
				"drop one set or pass mode=sync|async explicitly",
			strings.Join(legacyFields, ", "), strings.Join(asyncFields, ", "))
	}

	// 规则 2：显式 mode 与字段形状必须自洽。
	if mode == backtestModeSync && len(asyncFields) > 0 {
		return "", fmt.Errorf("mode=sync conflicts with async fields [%s]", strings.Join(asyncFields, ", "))
	}
	if mode == backtestModeAsync && len(legacyFields) > 0 {
		return "", fmt.Errorf("mode=async conflicts with sync fields [%s]", strings.Join(legacyFields, ", "))
	}

	// 规则 3：异步形状必须两件套齐全 —— 缺哪个点名哪个。
	if mode == backtestModeAsync || len(asyncFields) > 0 {
		if !hasStrategyID {
			return "", fmt.Errorf("async backtest requires strategy_id (missing)")
		}
		if !hasUniverse {
			return "", fmt.Errorf("async backtest requires universe (missing)")
		}
		return backtestModeAsync, nil
	}

	// 其余情况走同步信封。strategy 缺失的 400 仍由同步分支按原文案给出
	// （存量行为逐字节不变）。
	return backtestModeSync, nil
}

// stringField 取字符串字段；缺失 / null / 非字符串一律当空。
func stringField(raw map[string]json.RawMessage, key string) string {
	v, ok := raw[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return ""
	}
	return s
}
