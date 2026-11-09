package analysisartifact

// QT-78 · 股票静态信息目录：把 marketdata.Provider 的按交易所取数折成一次全量列表。
//
// 构建基准行业权重需要「成分股 → 行业 + 市值」。逐个成分调用 GetStock 会是
// 数百次往返，这里改用 GetStocks 的全量语义：exchange 传空串时实现方返回
// 全部；返回空再按 SH / SZ 各取一次兜底（数据服务的两种实现都见过）。

import (
	"context"

	"tellurion/pkg/domain"
)

// StockListProvider 是证券列表的最小读面（*marketdata.Provider 满足）。
type StockListProvider interface {
	GetStocks(ctx context.Context, exchange string) ([]domain.Stock, error)
}

// ProviderStockCatalog 用行情 provider 实现 StockCatalog。
type ProviderStockCatalog struct {
	Provider StockListProvider
}

// Stocks 返回去重后的全市场股票静态信息。
func (c *ProviderStockCatalog) Stocks(ctx context.Context) ([]domain.Stock, error) {
	all, err := c.Provider.GetStocks(ctx, "")
	if err != nil {
		return nil, err
	}
	if len(all) > 0 {
		return dedupeStocks(all), nil
	}

	var merged []domain.Stock
	for _, exchange := range []string{"SH", "SZ"} {
		stocks, err := c.Provider.GetStocks(ctx, exchange)
		if err != nil {
			continue
		}
		merged = append(merged, stocks...)
	}
	return dedupeStocks(merged), nil
}

func dedupeStocks(in []domain.Stock) []domain.Stock {
	seen := make(map[string]bool, len(in))
	out := make([]domain.Stock, 0, len(in))
	for _, s := range in {
		if s.Symbol == "" || seen[s.Symbol] {
			continue
		}
		seen[s.Symbol] = true
		out = append(out, s)
	}
	return out
}
