// CT-06-B (2026-11-10): 这两个查询/序列化边界形状只有 core 一个使用方，
// 自 `tellurion-contracts:domain/factor.go` 迁回。`FactorType` 名册本体
// （core + agent 两个使用方）仍留在契约仓，本包直接 import 使用、不重导出。
// CORE-FACTOR-CACHE-RETIRE（2026-11-12）：第三个形状 FactorCacheEntry 随
// public.factor_cache 退役一并删除。
package domain

import (
	"time"

	cdom "github.com/ruoxizhnya/tellurion-contracts/domain"
)

type FactorReturn struct {
	ID               int64           `json:"id"`
	FactorName       cdom.FactorType `json:"factor_name"`
	TradeDate        time.Time       `json:"trade_date"`
	Quintile         int             `json:"quintile"`
	AvgReturn        float64         `json:"avg_return"`
	CumulativeReturn float64         `json:"cumulative_return"`
	TopMinusBot      float64         `json:"top_minus_bot"`
}

type ICEntry struct {
	ID         int64           `json:"id"`
	FactorName cdom.FactorType `json:"factor_name"`
	TradeDate  time.Time       `json:"trade_date"`
	IC         float64         `json:"ic"`
	PValue     float64         `json:"p_value"`
	TopIC      float64         `json:"top_ic"`
}
