package listingseed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/backtest"
)

const fixtureListingWindows = `{
  "as_of": "2026-11-12",
  "trade_date": null,
  "count": 5,
  "windows": {
    "600519.SH": {"list_date": "2001-08-27", "delist_date": null, "delisted": false, "status": "active"},
    "301139.SZ": {"list_date": "2022-07-08", "delist_date": "2026-10-28", "delisted": true, "status": "delisted"},
    "000001.SZ": {"list_date": null, "delist_date": null, "delisted": false, "status": "active"},
    "600000.SH": {"list_date": "not-a-date", "delist_date": null, "delisted": false, "status": "active"},
    "600001.SH": {"list_date": "2000-01-04", "delist_date": "2026-13-45", "delisted": true, "status": "delisted"}
  }
}`

func newFixtureLoader(t *testing.T, body string, status int) (*Loader, *httptest.Server, *string) {
	t.Helper()
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != ListingWindowsPath {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return NewLoader(srv.URL, "secret-token", zerolog.Nop()), srv, &gotAuth
}

func TestNewLoaderEmptyBaseURLIsNil(t *testing.T) {
	assert.Nil(t, NewLoader("", "", zerolog.Nop()))
	assert.Nil(t, NewLoader("   ", "", zerolog.Nop()))
}

// TestRefreshMapsDataContract 钉住 data 契约到 ListingWindow 的映射：
// list_date null ⇒ 零值（左端不过滤）；delist_date null ⇒ nil（未退市）；
// 坏日期只把该边界当未知，不阻断整批。
func TestRefreshMapsDataContract(t *testing.T) {
	loader, _, gotAuth := newFixtureLoader(t, fixtureListingWindows, http.StatusOK)
	assert.NotNil(t, loader)

	snap, err := loader.Refresh(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Bearer secret-token", *gotAuth,
		"loader must forward data_service.token as Bearer")
	assert.False(t, snap.Degraded)
	assert.Equal(t, 5, snap.WindowCount())
	assert.Equal(t, time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC), snap.AsOf)
	assert.False(t, snap.FetchedAt.IsZero())

	listed := snap.Windows["600519.SH"]
	assert.Equal(t, time.Date(2001, 8, 27, 0, 0, 0, 0, time.UTC), listed.List)
	assert.Nil(t, listed.Delist)

	delisted := snap.Windows["301139.SZ"]
	require.NotNil(t, delisted.Delist)
	assert.Equal(t, time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC), *delisted.Delist)
	// 与 ListingWindow.IsListed 同口径：退市日当天仍在市。
	assert.True(t, delisted.IsListed(time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)))
	assert.False(t, delisted.IsListed(time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)))

	// list_date:null ⇒ 零值 ⇒ 左端不过滤（上市未知，不猜）。
	unknown := snap.Windows["000001.SZ"]
	assert.True(t, unknown.List.IsZero())
	assert.True(t, unknown.IsListed(time.Date(1999, 1, 4, 0, 0, 0, 0, time.UTC)))

	// 坏日期不阻断整批：票仍在表里，对应边界当未知。
	assert.True(t, snap.Windows["600000.SH"].List.IsZero())
	assert.Nil(t, snap.Windows["600001.SH"].Delist)
}

func TestRefreshSuccessReportsPointInTime(t *testing.T) {
	loader, _, _ := newFixtureLoader(t, fixtureListingWindows, http.StatusOK)
	_, err := loader.Refresh(context.Background())
	require.NoError(t, err)

	st := loader.Status()
	assert.Equal(t, PoolSourcePointInTime, st.PoolSource)
	assert.Equal(t, 5, st.Windows)
	assert.Equal(t, 1, st.Delisted)
	assert.False(t, st.Degraded)
	assert.Empty(t, st.Reason)
	require.NotNil(t, st.AsOf)
	assert.Equal(t, time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC), *st.AsOf)
	require.NotNil(t, st.FetchedAt)
}

// TestRefreshFailureKeepsUniverseUnfiltered 是本次接线的核心失败语义：
// 接口不可用时**不合成**窗口，状态面必须报 current + degraded。
func TestRefreshFailureKeepsUniverseUnfiltered(t *testing.T) {
	loader, _, _ := newFixtureLoader(t, `{"error":"boom"}`, http.StatusBadGateway)

	snap, err := loader.Refresh(context.Background())
	require.Error(t, err)
	assert.True(t, snap.Degraded)
	assert.Nil(t, snap.Windows, "failed fetch must not synthesize listing windows")
	assert.Nil(t, loader.Windows())

	st := loader.Status()
	assert.Equal(t, PoolSourceCurrent, st.PoolSource)
	assert.True(t, st.Degraded)
	assert.Equal(t, 0, st.Windows)
	assert.NotEmpty(t, st.Reason)
}

func TestRefreshEmptyWindowsIsFailure(t *testing.T) {
	loader, _, _ := newFixtureLoader(t, `{"as_of":"2026-11-12","count":0,"windows":{}}`, http.StatusOK)

	snap, err := loader.Refresh(context.Background())
	require.Error(t, err)
	assert.True(t, snap.Degraded)
	assert.Empty(t, snap.Windows)
	assert.Equal(t, PoolSourceCurrent, loader.Status().PoolSource)
}

func TestSnapshotIsDeepCopy(t *testing.T) {
	loader, _, _ := newFixtureLoader(t, fixtureListingWindows, http.StatusOK)
	_, err := loader.Refresh(context.Background())
	require.NoError(t, err)

	snap := loader.Snapshot()
	delete(snap.Windows, "600519.SH")
	delist := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	snap.Windows["301139.SZ"] = backtest.ListingWindow{Delist: &delist}

	assert.Equal(t, 5, loader.Snapshot().WindowCount(), "mutating the returned snapshot must not leak into the loader")
	assert.Equal(t, 1, loader.Status().Delisted)
}

func TestUnavailableStatus(t *testing.T) {
	st := UnavailableStatus("not configured")
	assert.Equal(t, PoolSourceCurrent, st.PoolSource)
	assert.True(t, st.Degraded)
	assert.Equal(t, "not configured", st.Reason)
	assert.Nil(t, st.AsOf)
	assert.Nil(t, st.FetchedAt)
}
