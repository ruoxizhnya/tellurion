package tracker

import (
	"tellurion/pkg/domain"
)

// OrderLog records all orders during a backtest run.
type OrderLog struct {
	orders []domain.Order
}

// Record adds an order to the log.
func (log *OrderLog) Record(o domain.Order) {
	log.orders = append(log.orders, o)
}

// GetOrders returns all recorded orders.
//
// 返回深拷贝：slice 与每条 Order 的 ExecProfile 都是独立的，调用方可以
// 自由修改；OrderLog 内部记录不受影响（并发读也不与后续 Record 竞争）。
func (log *OrderLog) GetOrders() []domain.Order {
	if len(log.orders) == 0 {
		return nil
	}
	out := make([]domain.Order, len(log.orders))
	for i, o := range log.orders {
		out[i] = o.Clone()
	}
	return out
}
