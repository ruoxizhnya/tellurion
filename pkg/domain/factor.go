// CT-06-B (2026-11-10): 这三个查询/序列化边界形状只有 core 一个使用方，
// 自 `tellurion-contracts:domain/factor.go` 迁回。`FactorType` 名册本体
// （core + agent 两个使用方）仍留在契约仓，本包直接 import 使用、不重导出。
package domain

import (
	"encoding/json"
	"time"

	cdom "github.com/ruoxizhnya/tellurion-contracts/domain"
)

type FactorCacheEntry struct {
	ID         int64           `json:"id"`
	Symbol     string          `json:"symbol"`
	TradeDate  time.Time       `json:"trade_date"`
	FactorName cdom.FactorType `json:"factor_name"`
	RawValue   float64         `json:"raw_value"`
	ZScore     float64         `json:"z_score"`
	Percentile float64         `json:"percentile"`
	// Citation is the stored provenance of this row's source batches, of the
	// form [{"content_hash":"<64hex>"}]. It is deliberately json.RawMessage:
	// the stored (hash-only) shape and the output shape (ADR-022 §5 five-tuple,
	// expanded by the handler) differ, and the stored form must stay losslessly
	// upgradable. Empty citation is '[]' — "no A→B chain established yet",
	// never null (ODR-061).
	Citation json.RawMessage `json:"citation,omitempty"`
}

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
