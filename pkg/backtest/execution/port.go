package execution

// QT-51（BE-02）执行语义边界：
//
// ExecutionPort 是**回测引擎的出站执行契约**。引擎只认识这个接口，
// 不 import 任何实盘实现（`pkg/trading`）。实盘/paper 的 `trading.LiveTrader`
// 由装配层（`cmd/analysis/wiring/live_trader_port.go`）适配后注入；
// 回测主循环自己的撮合仍走 `ExecutionService` + `tracker`，不经过本 port
// （port 为 nil = 纯模拟，行为与切 port 之前逐位一致）。
//
// 设计取舍与实测耦合面见 `docs/design/qt-decoupling/qt51-execution-port.md`。

import (
	"context"
	"time"

	cdom "github.com/ruoxizhnya/tellurion-contracts/domain"
)

import "tellurion/pkg/domain"

// ExecutionPort 是引擎把「信号已经翻译成订单」之后唯一需要的出站能力。
//
// 方法集刻意最小：下单 / 撤单 / 成交回报 / 身份 / 健康。
// 仓位、账户、紧急平仓不进本接口 —— 它们是运维面（HTTP handler 直接
// 消费 trading.LiveTrader），引擎运行期一个都不调用。
type ExecutionPort interface {
	// SubmitOrder 提交一笔订单。
	// 市价单 Price 必须为 0；限价单 Price 为限价。
	// 返回的 OrderResult 即该笔订单的即时成交回报（可能为部分成交）。
	SubmitOrder(ctx context.Context, req OrderRequest) (*OrderResult, error)

	// CancelOrder 撤销未成交/部分成交订单。
	// 已成交、已撤、不存在由实现返回错误。
	CancelOrder(ctx context.Context, orderID string) error

	// GetOrder 按 OrderID 查询订单当前状态与成交明细（成交回报）。
	GetOrder(ctx context.Context, orderID string) (*OrderResult, error)

	// Name 返回实现名（如 "mock_trader" / "simulated"）。
	Name() string

	// HealthCheck 检查通道可用性；nil 表示健康。
	HealthCheck(ctx context.Context) error
}

// OrderRequest 是引擎发往执行端的一笔订单意图。
//
// 字段与 `domain.Order` 的提交子集对齐，但**不复用** `domain.Order`：
// port 是跨进程/跨实现的边界，携带 ID/时间戳等引擎侧簿记字段只会
// 让边界语义变糊。
type OrderRequest struct {
	Symbol    string
	Direction cdom.Direction
	OrderType domain.OrderType
	Quantity  float64
	// Price：限价单为限价；市价单必须为 0（与 trading.LiveTrader 约定一致）。
	Price float64
}

// OrderResult 是执行端返回的订单状态与成交回报。
//
// 覆盖引擎日志/调用方实际消费的全部字段（含 K5 成交明细 FillPrice / Fee），
// 由 port 自己定义，避免回测域依赖实盘实现的值对象。
type OrderResult struct {
	OrderID     string
	Symbol      string
	Direction   cdom.Direction
	OrderType   domain.OrderType
	Quantity    float64
	FilledQty   float64
	Price       float64
	Status      string // pending / filled / partial / cancelled / rejected / expired
	SubmittedAt time.Time
	Message     string
	FillPrice   float64 // 施加滑点/冲击后的实际成交价（可选）
	Fee         float64 // 该笔总费用（可选）
}
