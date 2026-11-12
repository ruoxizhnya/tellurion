package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/internal/listingseed"
)

const listingStatusFixture = `{
  "as_of": "2026-11-12",
  "trade_date": null,
  "count": 3,
  "windows": {
    "600519.SH": {"list_date": "2001-08-27", "delist_date": null, "delisted": false, "status": "active"},
    "301139.SZ": {"list_date": "2022-07-08", "delist_date": "2026-10-28", "delisted": true, "status": "delisted"},
    "000001.SZ": {"list_date": null, "delist_date": null, "delisted": false, "status": "active"}
  }
}`

func listingStatusRouter(t *testing.T, loader *listingseed.Loader) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	NewListingStatusHandler(loader).RegisterRoutes(router)
	return router
}

func decodeListingStatus(t *testing.T, w *httptest.ResponseRecorder) listingseed.Status {
	t.Helper()
	require.Equal(t, http.StatusOK, w.Code)
	var st listingseed.Status
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &st))
	return st
}

func TestListingStatusNotConfigured(t *testing.T) {
	router := listingStatusRouter(t, nil)

	st := decodeListingStatus(t, performRequest(router, http.MethodGet, "/api/listing-windows/status"))
	assert.Equal(t, listingseed.PoolSourceCurrent, st.PoolSource)
	assert.True(t, st.Degraded)
	assert.Equal(t, 0, st.Windows)
	assert.Equal(t, listingStatusNotConfigured, st.Reason)
}

func TestListingStatusPointInTime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(listingStatusFixture))
	}))
	t.Cleanup(srv.Close)

	loader := listingseed.NewLoader(srv.URL, "", zerolog.Nop())
	require.NotNil(t, loader)
	_, err := loader.Refresh(context.Background())
	require.NoError(t, err)

	st := decodeListingStatus(t, performRequest(listingStatusRouter(t, loader), http.MethodGet, "/api/listing-windows/status"))
	assert.Equal(t, listingseed.PoolSourcePointInTime, st.PoolSource)
	assert.False(t, st.Degraded)
	assert.Equal(t, 3, st.Windows)
	assert.Equal(t, 1, st.Delisted)
	assert.NotNil(t, st.AsOf)
	assert.NotNil(t, st.FetchedAt)
	assert.Empty(t, st.Reason)
}

// 取数失败后读面必须如实报 current，而不是继续声称 point_in_time。
func TestListingStatusDegradedAfterFailedRefresh(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"upstream down"}`, http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	loader := listingseed.NewLoader(srv.URL, "", zerolog.Nop())
	require.NotNil(t, loader)
	_, err := loader.Refresh(context.Background())
	require.Error(t, err)

	st := decodeListingStatus(t, performRequest(listingStatusRouter(t, loader), http.MethodGet, "/api/listing-windows/status"))
	assert.Equal(t, listingseed.PoolSourceCurrent, st.PoolSource)
	assert.True(t, st.Degraded)
	assert.Equal(t, 0, st.Windows)
	assert.NotEmpty(t, st.Reason)
}
