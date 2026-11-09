package execution

// P1-17 (Sprint 6, ODR-013 CQ-001, ADR-020) / QT-51 (BE-02):
// LiveBridge — 引擎与**出站执行通道**之间的桥接组件。
//
// 该文件从 pkg/backtest/engine.go (1408 行 God Object) 中抽离出
// 以下职责：
//
//   - 持有一个可选的 ExecutionPort（实盘 / paper 通道由装配层适配注入）
//   - 通过 ExecuteSignal / ExecuteSignals 把策略信号翻译成订单请求
//   - 暴露 HealthCheck 供运维 / `/health` 端点调用
//
// QT-51 起本文件**不再 import `pkg/live`**：引擎只认识本包定义的
// `ExecutionPort`。`live.LiveTrader` → `ExecutionPort` 的适配住在
// 装配层（`cmd/analysis/wiring/live_trader_port.go`），是本仓唯一
// 同时认识两侧类型的位置（结构护栏见
// `internal/repoguard/backtest_live_isolation_test.go`）。
//
// 引擎层仍保留 Engine.SetLiveTrader / Engine.ExecuteSignalViaLiveTrader
// 这些方法名（per ADR-020 §2 的 shim 阶段）；QT-51 把参数/返回类型从
// live.LiveTrader 换成 execution.ExecutionPort，shim 仍只做
// "取 bridge → 调 method"的薄封装。
//
// 线程安全：
//   - LiveBridge 拥有自己的 sync.RWMutex，不受 Engine 主锁影响
//   - Execute* 不在 bridge 内额外加锁；port 实现需自行保证并发安全

import (
	"context"
	"fmt"
	"sync"

	"github.com/rs/zerolog"
	"tellurion/pkg/domain"
)

// LiveBridge 桥接 Engine 与 ExecutionPort。
//
// 它本身**不**持策略或信号生成逻辑——只负责"信号 → 订单请求"这一步。
// 这种单一职责使 LiveBridge 易于 mock、易于在新场景中替换（例如
// paper 模式 → 实盘模式），也方便未来加入高级 broker 行为（拆单、
// TWAP、VWAP、Iceberg 等）。
//
// 线程安全：所有访问通过 mu (sync.RWMutex) 保护。Get / Set 是
// O(1) RLock；Execute* 不在 bridge 内额外加锁（ExecutionPort
// 实现需自行保证并发安全）。
type LiveBridge struct {
	mu     sync.RWMutex
	port   ExecutionPort
	logger zerolog.Logger
}

// NewLiveBridge 构造一个未附加 ExecutionPort 的桥接器。
// 调用方应随后通过 Set() 注入 port，或保持 nil 以禁用出站路径。
func NewLiveBridge(logger zerolog.Logger) *LiveBridge {
	return &LiveBridge{
		logger: logger.With().Str("component", "live_bridge").Logger(),
	}
}

// Set 注入或替换 ExecutionPort。传 nil 禁用出站路径（回到纯模拟）。
// 注意：注入非 nil 时记录一条 Info 日志，便于运维确认"已挂载执行通道"。
func (b *LiveBridge) Set(port ExecutionPort) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.port = port
	if port != nil {
		b.logger.Info().Str("port", port.Name()).Msg("ExecutionPort attached to bridge")
	} else {
		b.logger.Info().Msg("ExecutionPort detached from bridge (execution path disabled)")
	}
}

// Get 返回当前挂载的 ExecutionPort；若未设置则返回 nil。
// 调用方负责 nil 检查（通常意味着"走纯模拟"分支）。
func (b *LiveBridge) Get() ExecutionPort {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.port
}

// ExecuteSignal 通过已挂载的 ExecutionPort 执行单个信号。
//
// 行为契约（QT-51 前后逐位不变）：
//   - port 为 nil 时返回 (nil, nil)，与旧 Engine.ExecuteSignalViaLiveTrader
//     保持完全一致——这是 engine → bridge 迁移的兼容点
//   - currentPrice <= 0 时返回 error，避免向执行端提交无意义订单
//   - 根据 signal.OrderType 自动选择市价/限价逻辑：
//   - market:  price = 0
//   - limit:   price = signal.LimitPrice (fallback: currentPrice)
//   - quantity 由 signal.Strength 推导，旧实现使用 `100.0 * max(1, strength*10)`
//     公式（封顶 10000）——保留以保持行为完全一致
//
// 返回 (result, nil) 表示下单成功（执行端接受订单）；
// 返回 (nil, err) 表示下单失败（拒绝、网络错误等）。
func (b *LiveBridge) ExecuteSignal(ctx context.Context, signal domain.Signal, currentPrice float64) (*OrderResult, error) {
	port := b.Get()
	if port == nil {
		return nil, nil
	}

	if currentPrice <= 0 {
		return nil, fmt.Errorf("cannot execute signal: invalid price %.4f for %s", currentPrice, signal.Symbol)
	}

	// Determine order type from signal
	orderType := signal.OrderType
	if orderType == "" {
		orderType = domain.OrderTypeMarket
	}

	// Determine price: limit price for limit orders, 0 for market orders
	price := 0.0
	if orderType == domain.OrderTypeLimit {
		price = signal.LimitPrice
		if price <= 0 {
			price = currentPrice
		}
	}

	// Calculate quantity from position sizing or default to a fixed lot.
	// 旧实现 (engine.go:1369) 沿用 max builtin (Go 1.21+)；此处直接写出。
	quantity := 100.0 // default lot size; in production this comes from risk/position sizing
	if signal.Strength > 0 {
		scaled := 1.0
		if signal.Strength*10 > 1.0 {
			scaled = signal.Strength * 10
		}
		quantity = 100.0 * scaled
		if quantity > 10000 {
			quantity = 10000
		}
	}

	result, err := port.SubmitOrder(ctx, OrderRequest{
		Symbol:    signal.Symbol,
		Direction: signal.Direction,
		OrderType: orderType,
		Quantity:  quantity,
		Price:     price,
	})
	if err != nil {
		b.logger.Warn().
			Str("symbol", signal.Symbol).
			Str("direction", string(signal.Direction)).
			Float64("price", currentPrice).
			Err(err).
			Msg("ExecutionPort order submission failed")
		return nil, err
	}
	if result == nil {
		return nil, nil
	}

	b.logger.Info().
		Str("order_id", result.OrderID).
		Str("symbol", signal.Symbol).
		Str("direction", string(signal.Direction)).
		Str("status", result.Status).
		Float64("qty", result.FilledQty).
		Float64("price", result.Price).
		Msg("Signal executed via ExecutionPort")

	return result, nil
}

// ExecuteSignals 批量执行多个信号（典型用途：每日 rebalance）。
//
// 行为契约：
//   - port 为 nil 时返回 nil（与 ExecuteSignal 一致）
//   - 单个信号失败不中断后续信号；失败的信号不出现在结果 map 中
//   - 价格缺失或 <= 0 的信号会被跳过并打 Warn 日志
//
// 返回 symbol → OrderResult 映射，仅包含成功执行的信号。
func (b *LiveBridge) ExecuteSignals(ctx context.Context, signals []domain.Signal, prices map[string]float64) map[string]*OrderResult {
	port := b.Get()
	if port == nil {
		return nil
	}

	results := make(map[string]*OrderResult, len(signals))
	for _, signal := range signals {
		price, ok := prices[signal.Symbol]
		if !ok || price <= 0 {
			b.logger.Warn().Str("symbol", signal.Symbol).Msg("No price available for execution, skipping")
			continue
		}

		result, err := b.ExecuteSignal(ctx, signal, price)
		if err != nil {
			continue
		}
		if result != nil {
			results[signal.Symbol] = result
		}
	}

	b.logger.Info().
		Int("signals", len(signals)).
		Int("executed", len(results)).
		Msg("Batch signal execution via ExecutionPort completed")

	return results
}

// HealthCheck 检查挂载的 ExecutionPort 健康状态。
// 返回 nil 表示"无 port 挂载"或"通道健康"，与旧 Engine 实现一致。
func (b *LiveBridge) HealthCheck(ctx context.Context) error {
	port := b.Get()
	if port == nil {
		return nil
	}
	return port.HealthCheck(ctx)
}
