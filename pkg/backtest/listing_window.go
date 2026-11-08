package backtest

import "time"

// ListingWindow 是一只股票的「在市区间」：上市之后、摘牌之前。
//
// SVC-CORE-DATA-RETIRE（2026-11-08）：本类型原在 pkg/storage/stocks.go。
// stocks 表随数据面退役后，core 不再从库里读取这段元数据；引擎仍需要
// 「某日是否在市」的判定能力，因此类型留在它真正的使用者这里，数据由
// 调用方注入（SetListingWindows），或在 run 上下文里由 Provider 侧提供。
//
// Delist == nil 表示还没退市（右端开放）。
// List 为零值表示上市日未知 —— 此时不做左端过滤（宁可放宽也不凭空剔除）。
type ListingWindow struct {
	List   time.Time
	Delist *time.Time
}

// IsListed 报告这只票在 date 当天是否还在市。
func (w ListingWindow) IsListed(date time.Time) bool {
	if !w.List.IsZero() && date.Before(w.List) {
		return false // 还没上市
	}
	if w.Delist != nil && date.After(*w.Delist) {
		return false // 已经摘牌
	}
	return true
}
