package wasm

// QT-24 测试辅助：v1/v2 的 BarContext 构造。
//
// v1 语义只关心 close 序列；symbol scope 下 Bars[0] 就是该标的的自有序列。
// 这里统一造出「日期逐日递增、OHLC 同价」的最小 bar，避免每个测试各写一份。

import (
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
)

var qt24Start = time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)

// barsForCloses 把 close 序列折成 symbol-scope 的 Bars（单 symbol）。
func barsForCloses(closes []float64) [][]domain.OHLCV {
	bars := make([]domain.OHLCV, len(closes))
	for i, c := range closes {
		bars[i] = domain.OHLCV{
			Date:   qt24Start.AddDate(0, 0, i),
			Open:   c,
			High:   c,
			Low:    c,
			Close:  c,
			Volume: 1000,
		}
	}
	return [][]domain.OHLCV{bars}
}

// bcWithCloses 是 v1 / symbol-scope 的 BarContext 快捷构造。
func bcWithCloses(closes []float64, t int32) *BarContext {
	return &BarContext{T: t, Bars: barsForCloses(closes), State: map[string][]byte{}}
}
