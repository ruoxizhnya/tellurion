package marketdata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-contracts/domain"
	apperrors "github.com/ruoxizhnya/tellurion-kit/errors"
	"github.com/ruoxizhnya/tellurion-kit/httpclient"
)

type httpProvider struct {
	client *httpclient.Client
	token  string
	logger zerolog.Logger
}

// NewHTTPProvider 构造不带凭证的 HTTP 行情 provider（回环 + 无 secret 的开发态）。
func NewHTTPProvider(baseURL string, logger zerolog.Logger) Provider {
	return NewHTTPProviderWithToken(baseURL, "", logger)
}

// NewHTTPProviderWithToken 构造带 `Authorization: Bearer` 的 HTTP 行情 provider。
//
// 为什么必须有：data 服务的 `/datasets/*` 与 `/api/v1/ohlcv/bulk` 都在
// 鉴权闸后面（裁-15）。core 侧对 data 的调用一旦对方配了 secret，不带凭证
// 就是 401 —— 回测取数会全线失败，而不是「行情服务没起来」。
// token 为空时不发头：与「无 secret + 回环 = 开放」的部署形态一致。
func NewHTTPProviderWithToken(baseURL, token string, logger zerolog.Logger) Provider {
	return &httpProvider{
		client: httpclient.New(baseURL, 30*time.Second, 3),
		token:  strings.TrimSpace(token),
		logger: logger.With().Str("component", "http_marketdata").Logger(),
	}
}

// headers 返回本次请求要带的头（目前只有服务间凭证）。
func (p *httpProvider) headers() map[string]string {
	if p.token == "" {
		return nil
	}
	return map[string]string{"Authorization": "Bearer " + p.token}
}

// get 是带凭证的 GET；所有取数方法都走它，避免某一条路径漏头。
func (p *httpProvider) get(ctx context.Context, path string) (*httpclient.Response, error) {
	return p.client.Do(ctx, httpclient.Request{Method: http.MethodGet, Path: path, Headers: p.headers()})
}

// post 是带凭证的 POST。
func (p *httpProvider) post(ctx context.Context, path string, body any) (*httpclient.Response, error) {
	return p.client.Do(ctx, httpclient.Request{Method: http.MethodPost, Path: path, Body: body, Headers: p.headers()})
}

func (p *httpProvider) Name() string {
	return "http"
}

func (p *httpProvider) CheckConnectivity(ctx context.Context) error {
	resp, err := p.get(ctx, "/health")
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("data service health check failed: status %d", resp.StatusCode)
	}
	return nil
}

func (p *httpProvider) GetOHLCV(ctx context.Context, symbol string, start, end time.Time) ([]domain.OHLCV, error) {
	path := fmt.Sprintf("/ohlcv/%s?start_date=%s&end_date=%s",
		symbol, start.Format("20060102"), end.Format("20060102"))

	resp, err := p.get(ctx, path)
	if err != nil {
		return nil, apperrors.Unavailable("data", err)
	}

	var result struct {
		OHLCV []domain.OHLCV `json:"ohlcv"`
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, apperrors.Wrap(err, apperrors.ErrCodeInternal, "failed to decode OHLCV response", "GetOHLCV")
	}

	return result.OHLCV, nil
}

func (p *httpProvider) GetFundamental(ctx context.Context, symbol string, date time.Time) (*domain.Fundamental, error) {
	path := fmt.Sprintf("/fundamental/%s?date=%s", symbol, date.Format("20060102"))

	resp, err := p.get(ctx, path)
	if err != nil {
		return nil, apperrors.Unavailable("data", err)
	}

	var result struct {
		Fundamental *domain.Fundamental `json:"fundamental"`
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, apperrors.Wrap(err, apperrors.ErrCodeInternal, "failed to decode fundamental response", "GetFundamental")
	}

	return result.Fundamental, nil
}

func (p *httpProvider) GetStocks(ctx context.Context, exchange string) ([]domain.Stock, error) {
	path := "/stocks"
	if exchange != "" {
		path += "?exchange=" + exchange
	}

	resp, err := p.get(ctx, path)
	if err != nil {
		return nil, apperrors.Unavailable("data", err)
	}

	var result struct {
		Stocks []domain.Stock `json:"stocks"`
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, apperrors.Wrap(err, apperrors.ErrCodeInternal, "failed to decode stocks response", "GetStocks")
	}

	return result.Stocks, nil
}

func (p *httpProvider) GetLatestPrice(ctx context.Context, symbol string) (float64, error) {
	end := time.Now()
	start := end.AddDate(0, 0, -5)

	bars, err := p.GetOHLCV(ctx, symbol, start, end)
	if err != nil {
		return 0, err
	}
	if len(bars) == 0 {
		return 0, apperrors.NotFound("OHLCV", symbol)
	}
	return bars[len(bars)-1].Close, nil
}

func (p *httpProvider) GetIndexConstituents(ctx context.Context, indexCode string) ([]string, error) {
	path := fmt.Sprintf("/index/%s/constituents", indexCode)

	resp, err := p.get(ctx, path)
	if err != nil {
		return nil, apperrors.Unavailable("data", err)
	}

	var result struct {
		Constituents []struct {
			Symbol string `json:"symbol"`
		} `json:"constituents"`
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, apperrors.Wrap(err, apperrors.ErrCodeInternal, "failed to decode index constituents response", "GetIndexConstituents")
	}

	symbols := make([]string, len(result.Constituents))
	for i, c := range result.Constituents {
		symbols[i] = c.Symbol
	}
	return symbols, nil
}

func (p *httpProvider) GetTradingDays(ctx context.Context, start, end time.Time) ([]time.Time, error) {
	path := fmt.Sprintf("/api/v1/trading/calendar?start=%s&end=%s",
		start.Format("2006-01-02"), end.Format("2006-01-02"))

	resp, err := p.get(ctx, path)
	if err != nil {
		return nil, apperrors.Unavailable("data", err)
	}

	var result struct {
		TradingDays []string `json:"trading_days"`
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, apperrors.Wrap(err, apperrors.ErrCodeInternal, "failed to decode trading days", "GetTradingDays")
	}

	days := make([]time.Time, 0, len(result.TradingDays))
	for _, d := range result.TradingDays {
		t, err := time.Parse("2006-01-02", d)
		if err != nil {
			continue
		}
		days = append(days, t)
	}
	return days, nil
}

func (p *httpProvider) GetStock(ctx context.Context, symbol string) (domain.Stock, error) {
	path := fmt.Sprintf("/api/v1/stocks/%s", symbol)

	resp, err := p.get(ctx, path)
	if err != nil {
		return domain.Stock{}, err
	}

	if resp.StatusCode == 404 {
		return domain.Stock{Symbol: symbol}, nil
	}

	var result struct {
		Stock domain.Stock `json:"stock"`
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return domain.Stock{}, err
	}
	return result.Stock, nil
}

func (p *httpProvider) BulkLoadOHLCV(ctx context.Context, symbols []string, start, end time.Time) (map[string][]domain.OHLCV, error) {
	body := map[string]any{
		"symbols":    symbols,
		"start_date": start.Format("20060102"),
		"end_date":   end.Format("20060102"),
	}

	resp, err := p.post(ctx, "/api/v1/ohlcv/bulk", body)
	if err != nil {
		return nil, apperrors.Unavailable("data", err)
	}

	var result struct {
		Results []struct {
			Symbol string         `json:"symbol"`
			Error  string         `json:"error,omitempty"`
			OHLCV  []domain.OHLCV `json:"ohlcv"`
		} `json:"results"`
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, apperrors.Wrap(err, apperrors.ErrCodeInternal, "failed to decode bulk response", "BulkLoadOHLCV")
	}

	data := make(map[string][]domain.OHLCV, len(result.Results))
	for _, r := range result.Results {
		if r.Error != "" {
			continue
		}
		data[r.Symbol] = r.OHLCV
	}
	return data, nil
}

func (p *httpProvider) CheckCalendarExists(ctx context.Context, start, end time.Time) (bool, error) {
	path := fmt.Sprintf("/api/v1/trading/calendar?start=%s&end=%s",
		start.Format("2006-01-02"), end.Format("2006-01-02"))

	resp, err := p.get(ctx, path)
	if err != nil {
		return false, err
	}

	if resp.StatusCode == 404 || resp.StatusCode == 400 {
		return false, nil
	}

	var result struct {
		TradingDays []string `json:"trading_days"`
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return false, err
	}
	return len(result.TradingDays) > 0, nil
}
