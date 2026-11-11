package marketdata

// 归属裁决（2026-11-12）：`stock_fundamentals` 是 core 自己的表。
//
// 背景：P1 之后 core 的行情通道只剩 HTTP（数据服务），`GetFundamental` 仍
// 打 `/fundamental/{symbol}`；而数据服务的读实现指向 `market.stock_fundamentals`
// —— 那张表从未建立（DDL 在 core 的 `pkg/storage/postgres.go`，写者/读者也
// 都在 core）。结果是一条**设计上不该存在**的读链把空态冒成 500。
//
// 本装饰器把「基本面」这一项切回本地（`public.stock_fundamentals`），其余
// 行情能力继续走 HTTP。不新增第二份存储，也不在数据服务里补一张空表。

import (
	"context"
	"time"

	"tellurion/pkg/domain"
)

// FundamentalQuerier 是 core 自有的基本面读口（生产实现是
// *storage.PostgresStore，读 `public.stock_fundamentals`）。
type FundamentalQuerier interface {
	GetFundamental(ctx context.Context, symbol string, date time.Time) (*domain.Fundamental, error)
}

// localFundamentalProvider 只覆盖 GetFundamental，其余方法原样透传给 inner。
type localFundamentalProvider struct {
	inner Provider
	local FundamentalQuerier
}

// NewLocalFundamentalProvider 让人工装配的 HTTP provider 在基本面一项上读本地表。
//
// local 为 nil 时原样返回 inner（未接库的场景），调用方应自行保证该场景可用。
func NewLocalFundamentalProvider(inner Provider, local FundamentalQuerier) Provider {
	if local == nil || inner == nil {
		return inner
	}
	return &localFundamentalProvider{inner: inner, local: local}
}

func (p *localFundamentalProvider) Name() string { return p.inner.Name() }

func (p *localFundamentalProvider) CheckConnectivity(ctx context.Context) error {
	return p.inner.CheckConnectivity(ctx)
}

func (p *localFundamentalProvider) GetOHLCV(ctx context.Context, symbol string, start, end time.Time) ([]domain.OHLCV, error) {
	return p.inner.GetOHLCV(ctx, symbol, start, end)
}

func (p *localFundamentalProvider) GetFundamental(ctx context.Context, symbol string, date time.Time) (*domain.Fundamental, error) {
	return p.local.GetFundamental(ctx, symbol, date)
}

func (p *localFundamentalProvider) GetStocks(ctx context.Context, exchange string) ([]domain.Stock, error) {
	return p.inner.GetStocks(ctx, exchange)
}

func (p *localFundamentalProvider) GetLatestPrice(ctx context.Context, symbol string) (float64, error) {
	return p.inner.GetLatestPrice(ctx, symbol)
}

func (p *localFundamentalProvider) GetIndexConstituents(ctx context.Context, indexCode string) ([]string, error) {
	return p.inner.GetIndexConstituents(ctx, indexCode)
}

func (p *localFundamentalProvider) GetTradingDays(ctx context.Context, start, end time.Time) ([]time.Time, error) {
	return p.inner.GetTradingDays(ctx, start, end)
}

func (p *localFundamentalProvider) GetStock(ctx context.Context, symbol string) (domain.Stock, error) {
	return p.inner.GetStock(ctx, symbol)
}

func (p *localFundamentalProvider) BulkLoadOHLCV(ctx context.Context, symbols []string, start, end time.Time) (map[string][]domain.OHLCV, error) {
	return p.inner.BulkLoadOHLCV(ctx, symbols, start, end)
}

func (p *localFundamentalProvider) CheckCalendarExists(ctx context.Context, start, end time.Time) (bool, error) {
	return p.inner.CheckCalendarExists(ctx, start, end)
}
