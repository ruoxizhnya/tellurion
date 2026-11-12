package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cdom "github.com/ruoxizhnya/tellurion-contracts/domain"

	"tellurion/internal/factoranalytics"
	"tellurion/pkg/domain"
	"tellurion/pkg/storage"
)

type fakeFactorAnalyticsStore struct {
	summaries []storage.FactorAnalyticsSummary
	returns   []*domain.FactorReturn
	ics       []*domain.ICEntry
	err       error
}

func (f *fakeFactorAnalyticsStore) ListFactorAnalyticsSummaries(ctx context.Context) ([]storage.FactorAnalyticsSummary, error) {
	return f.summaries, f.err
}

func (f *fakeFactorAnalyticsStore) GetFactorReturns(ctx context.Context, factor cdom.FactorType, startDate, endDate time.Time) ([]*domain.FactorReturn, error) {
	return f.returns, f.err
}

func (f *fakeFactorAnalyticsStore) GetICEntries(ctx context.Context, factor cdom.FactorType, startDate, endDate time.Time) ([]*domain.ICEntry, error) {
	return f.ics, f.err
}

func newFactorAnalyticsRouter(store factorAnalyticsStore, computer *factoranalytics.Computer) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	NewFactorAnalyticsHandler(store, computer, zerolog.Nop()).RegisterRoutes(r)
	return r
}

func TestFactorAnalyticsListFactors(t *testing.T) {
	store := &fakeFactorAnalyticsStore{summaries: []storage.FactorAnalyticsSummary{{
		FactorName:   cdom.FactorMomentum,
		Observations: 12,
		StartDate:    time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC),
		EndDate:      time.Date(2026, 2, 5, 0, 0, 0, 0, time.UTC),
		MeanIC:       0.12,
		LatestIC:     0.2,
	}}}
	r := newFactorAnalyticsRouter(store, nil)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/factor-analytics/factors", nil))

	require.Equal(t, http.StatusOK, w.Code)
	var body struct {
		Factors []factorAnalyticsSummaryResponse `json:"factors"`
		Support []string                         `json:"supported"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Len(t, body.Factors, 1)
	assert.Equal(t, "momentum", string(body.Factors[0].FactorName))
	assert.Equal(t, "2026-01-05", body.Factors[0].StartDate)
	assert.Equal(t, "2026-02-05", body.Factors[0].EndDate)
	assert.ElementsMatch(t, supportedFactorNames(), body.Support)
}

func TestFactorAnalyticsReturnsAndIC(t *testing.T) {
	store := &fakeFactorAnalyticsStore{
		returns: []*domain.FactorReturn{{
			FactorName: cdom.FactorMomentum, TradeDate: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC),
			Quintile: 5, AvgReturn: 0.02, CumulativeReturn: 0.05, TopMinusBot: 0.03,
		}},
		ics: []*domain.ICEntry{{
			FactorName: cdom.FactorMomentum, TradeDate: time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC),
			IC: 0.3, PValue: 0.01, TopIC: 0.25,
		}},
	}
	r := newFactorAnalyticsRouter(store, nil)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/api/factor-analytics/factors/momentum/returns?start=2026-01-01&end=2026-02-01", nil))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"quintile":5`)

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/api/factor-analytics/factors/momentum/ic?start=2026-01-01&end=2026-02-01", nil))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"ic":0.3`)
}

func TestFactorAnalyticsRejectsUnknownFactorAndBadDate(t *testing.T) {
	r := newFactorAnalyticsRouter(&fakeFactorAnalyticsStore{}, nil)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/factor-analytics/factors/volatility/ic", nil))
	assert.Equal(t, http.StatusBadRequest, w.Code)

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/api/factor-analytics/factors/momentum/ic?start=2026-13-01", nil))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestFactorAnalyticsComputeUnavailableWithoutComputer(t *testing.T) {
	r := newFactorAnalyticsRouter(&fakeFactorAnalyticsStore{}, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/factor-analytics/compute",
		strings.NewReader(`{"start":"2026-01-01","end":"2026-02-01"}`)))
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

type fakeAnalyticsBars struct{}

func analyticsSymbol(i int) string {
	return "S" + string(rune('A'+i%26)) + string(rune('A'+i/26))
}

func (fakeAnalyticsBars) GetStocks(ctx context.Context, exchange string) ([]domain.Stock, error) {
	out := make([]domain.Stock, 0, 30)
	for i := 0; i < 30; i++ {
		out = append(out, domain.Stock{Symbol: analyticsSymbol(i), Status: "listed"})
	}
	return out, nil
}

func (fakeAnalyticsBars) BulkLoadOHLCV(ctx context.Context, symbols []string, start, end time.Time) (map[string][]domain.OHLCV, error) {
	out := make(map[string][]domain.OHLCV, 30)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 30; i++ {
		symbol := analyticsSymbol(i)
		price := 10.0
		daily := 0.001 + 0.0002*float64(i)
		series := make([]domain.OHLCV, 0, 80)
		for d := 0; d < 80; d++ {
			if d > 0 {
				price *= 1 + daily
			}
			series = append(series, domain.OHLCV{Symbol: symbol, Date: base.AddDate(0, 0, d), Close: price})
		}
		out[symbol] = series
	}
	return out, nil
}

func TestFactorAnalyticsComputeWritesAndReports(t *testing.T) {
	store := &fakeFactorAnalyticsStore{}
	computer := factoranalytics.NewComputer(fakeAnalyticsBars{}, nil, nil)
	r := newFactorAnalyticsRouter(store, computer)

	body := `{"start":"2026-01-26","end":"2026-03-05","factors":["momentum"],"min_cross_section":20}`
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/factor-analytics/compute", strings.NewReader(body)))

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp struct {
		Written        bool `json:"written"`
		Universe       int  `json:"universe"`
		FactorReturns  int  `json:"factor_returns"`
		ICEntries      int  `json:"ic_entries"`
		RebalanceDates int  `json:"rebalance_dates"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.False(t, resp.Written, "computer built without writer must not claim written")
	assert.Equal(t, 30, resp.Universe)
	assert.Greater(t, resp.RebalanceDates, 0)
	assert.Equal(t, resp.RebalanceDates*5, resp.FactorReturns)
	assert.Equal(t, resp.RebalanceDates, resp.ICEntries)
}
