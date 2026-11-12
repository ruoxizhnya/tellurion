package backtest

// 持久化端口（SVC-CORE-RUN-RETIRE ③，2026-11-10）。
//
// 引擎不再持有具体存储指针：它只需要「基本面 PIT、复权基准、run 留痕」
// 三件事，全部在这里声明成窄接口。生产实现仍是 storage 的 PG store，由
// 装配层按接口注入；编译期自证在 `cmd/analysis/wiring`（本包不 import
// storage，也不 import 任何会反向依赖本包的存储实现）。
//
// 记录形状放在 `pkg/persistence` 叶子包里：端口签名与 PG 实现逐字段同型，
// 才不会为了「去依赖」复制两份结构、再靠转换层维持同步。

import (
	"context"
	"time"

	"tellurion/pkg/domain"
)

// FundamentalsReader 是基本面 PIT 批量取数端口。
//
// 口径与 storage 的同名方法逐字一致：只返回 asOf 当日**已可用**的期数，
// 按 symbol 分组、按 available_date 升序；缺失的 symbol 不出现在 map 里。
type FundamentalsReader interface {
	GetFundamentalsPITBulk(ctx context.Context, symbols []string, asOf time.Time) (map[string][]domain.Fundamental, error)
}

// Storage 是引擎对持久化层的全部需求。
//
// 三个能力各自独立，组合在这里只为让装配层一次 `SetStore` 完成接线；
// 任何实现只要方法集齐全就能注入（测试可用内存替身，不必起 PG）。
type Storage interface {
	FundamentalsReader
	RunProvenanceWriter
	AdjustmentAnchorStore
}
