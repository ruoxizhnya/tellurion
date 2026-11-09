package domain

import "github.com/ruoxizhnya/tellurion-contracts/domain/market"

// 契约仓 market 形状的同形别名（CT-06-B 裁决：别名类只在 core 建同形别名，
// 身份不变）。实类型住在 `tellurion-contracts:domain/market`，core 侧
// `domain.OHLCV` 与 `market.OHLCV` 是同一个类型，切片/map 互通、无需转换。
//
// 只有 core 一个使用方的 `IndexConstituent` / `FundamentalData` 不建别名：
// 契约仓根包已删这两个别名，需要时直接 import market 包。
type (
	OHLCV           = market.OHLCV
	Stock           = market.Stock
	IndexDaily      = market.IndexDaily
	Split           = market.Split
	Dividend        = market.Dividend
	Fundamental     = market.Fundamental
	FundamentalData = market.FundamentalData
)
