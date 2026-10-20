package main

// DBT-11 — end-to-end acceptance for the C-3 equitydeep ingest door
// (POST /api/ingest/equitydeep, TASKS.md EQD-P1-2).
//
// EQD-P1-2 shipped the gate, the normalizer and the five vertical factors that
// read the result, but nothing ever sent a snapshot through the gate over a real
// request: the whole path from ndjson body to a fundamentals_detail row that
// GetFundamentalsDetailAsOf can serve existed only in prose. These tests run it
// once, against the real store, over a real HTTP server, with the real frozen
// dictionary.
//
// Two disciplines hold because the database is a shared development library:
//
//   - every row this test writes is anchored on the content_hash it generated
//     for itself, and cleanup deletes exactly that anchor (never a table-wide
//     TRUNCATE);
//   - the cleanup is verified, not assumed: a second cleanup registered BEFORE
//     the deletion runs AFTER it and fails the test if any row survived. Remove
//     the deletion and the test goes red — a guard nobody can break is not a
//     guard.
//
// The numbers below are demonstrative. They must never be read as company
// financials: the point is that the chain works, not that it is populated.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/data/equitydeep"
	"tellurion/pkg/domain"
	"tellurion/pkg/storage"
	"tellurion/pkg/testenv"
)

const (
	// edDictPath is the frozen contract file the production door is assembled
	// with (cmd/data/setup.go buildEquityDeepDictionary uses the same search
	// order; from the cmd/data test working directory only this one applies).
	edDictPath = "../../contracts/field_dictionary.yaml"

	// edDemoTicker is deliberately not an issuable A-share code: 990001 carries
	// neither a Shanghai (6/9 followed by digits) nor a Shenzhen prefix pattern
	// that any live instrument uses, so a demonstrative row cannot be mistaken
	// for a real one even if cleanup ever failed to run.
	edDemoTicker = "990001"
	edDemoTsCode = "990001.SH"
)

// snapshotURIPattern is the evidence-coordinate form the door must stamp: the
// ingest.raw table, a colon, then the 64-char lowercase sha256 it archived.
var snapshotURIPattern = regexp.MustCompile(`^ingest\.raw:[0-9a-f]{64}$`)

// The three demo snapshots. Same ts_code, same report_period, three distinct
// announcement/fetch combinations, chosen so that one POST exercises every
// reading the table and its PIT query are built for:
//
//	edSnapFirst  2026-03-31 reported, announced 2026-04-29, fetched 2026-05-10
//	edSnapRestat same period, same ann_date, fetched 2026-05-20  → a restatement
//	                                                               of one
//	                                                               announcement
//	edSnapLater  same period, announced 2026-08-29              → a second
//	                                                               announcement
//
// Plus: a unit that is not the base unit (CNY_100m, so the conversion has to
// happen), a null reading (missing must stay distinguishable from zero), a
// raw_name outside the whitelist (dropped and reported, never guessed), and two
// snapshots that omit source/fetched_at so the ingest record's fallbacks run.
const (
	edSnapFirst = `{"ticker":"` + edDemoTicker + `","report_period":"2026-03-31","ann_date":"2026-04-29",` +
		`"dataset":"stock_financial_abstract","fetched_at":"2026-05-10","unit":"CNY_100m",` +
		`"data":{"营业总收入":1.5,"存货":null,"净资产收益率":8.23}}`

	edSnapLater = `{"ticker":"` + edDemoTicker + `","report_period":"2026-03-31","ann_date":"2026-08-29",` +
		`"source":"ths","data":{"营业总收入":200000000,"资产总计":1000000000}}`

	edSnapRestat = `{"ticker":"` + edDemoTicker + `","report_period":"2026-03-31","ann_date":"2026-04-29",` +
		`"fetched_at":"2026-05-20","data":{"营业总收入":160000000}}`
)

// edSnapshotNDJSON is the body the producer posts: one record per line.
func edSnapshotNDJSON() string {
	return edSnapFirst + "\n" + edSnapLater + "\n" + edSnapRestat + "\n"
}

// edDemoPayload is the raw response that gets archived first. The door requires
// a content_hash it can resolve, so nothing can enter fundamentals_detail
// without an archived record behind it — that is the whole provenance gate.
func edDemoPayload(probe string) string {
	return fmt.Sprintf(`{"probe":%q,"dataset":"stock_financial_abstract","snapshots":[%s,%s,%s]}`,
		probe, edSnapFirst, edSnapLater, edSnapRestat)
}

// newEquityDeepTestDoor assembles the real routes over the real store: the L0
// write door plus the C-3 equitydeep door, both with the frozen dictionary.
func newEquityDeepTestDoor(t *testing.T, store *storage.PostgresStore) *httptest.Server {
	t.Helper()
	dict, err := equitydeep.LoadDictionary(edDictPath)
	require.NoError(t, err, "the ingest door cannot be tested without the frozen dictionary it runs on")

	router := gin.New()
	router.POST("/api/ingest/raw", ingestRawHandler(store))
	router.POST("/api/ingest/equitydeep", equityDeepIngestHandler(store, dict))

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return srv
}

// edPostRaw archives one raw response through the real door and returns the
// content_hash it was given.
func edPostRaw(t *testing.T, srv *httptest.Server, payload string) string {
	t.Helper()
	body := fmt.Sprintf(`{"source":"akshare","dataset":"stock_financial_abstract","key":"%s/demo","as_of":"2026-03-31","payload":%s}`,
		edDemoTsCode, payload)

	resp := edDo(t, srv, http.MethodPost, "/api/ingest/raw", body)
	require.Equal(t, http.StatusCreated, resp.status, "body: %s", resp.body)

	var created storage.RawIngest
	require.NoError(t, json.Unmarshal(resp.body, &created))
	assert.NotEmpty(t, created.ContentHash)
	return created.ContentHash
}

type edResponse struct {
	status int
	body   []byte
}

func edDo(t *testing.T, srv *httptest.Server, method, path, body string) edResponse {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-ndjson")

	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return edResponse{status: resp.StatusCode, body: raw}
}

// edCount runs one scalar count query. It is a test-side read of the physical
// table on purpose: the assertion is about what actually landed in the library,
// not about what a Go method reported.
func edCount(t *testing.T, store *storage.PostgresStore, query string, args ...any) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var n int64
	require.NoError(t, store.DB().QueryRow(ctx, query, args...).Scan(&n))
	return n
}

// TestEquityDeepIngestDoor_EndToEnd is the deliverable: an archived raw response
// → ndjson through the HTTP door → rows in fundamentals_detail → read back
// through GetFundamentalsDetailAsOf with PIT semantics → cleaned up again.
func TestEquityDeepIngestDoor_EndToEnd(t *testing.T) {
	dsn := testenv.DatabaseDSN()
	if dsn == "" {
		testenv.SkipNoDB(t)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := storage.NewPostgresStoreConnected(ctx, dsn)
	require.NoError(t, err, "[testdb] TEST_DATABASE_DSN 已设置但连不上（fail-loud）")

	// Cleanup order is LIFO, hence this registration sequence: close last,
	// verify after deleting, delete first.
	t.Cleanup(store.Close)
	srv := newEquityDeepTestDoor(t, store) // registers srv.Close before the two below

	// Reading before writing, so the recheck at the end can prove the table is
	// back to exactly what this test found.
	baseline := edCount(t, store, `SELECT count(*) FROM fundamentals_detail`)
	t.Logf("fundamentals_detail rows before this test: %d", baseline)

	// One probe per run: the content_hash, and therefore snapshot_uri, is unique
	// to this execution, which is what makes the delete anchor safe to use on a
	// shared library.
	probe := fmt.Sprintf("dbt11-equitydeep-door-%d", time.Now().UnixNano())
	hash := edPostRaw(t, srv, edDemoPayload(probe))
	snapshotURI := "ingest.raw:" + hash

	// Cleanup order is LIFO, so the verification has to be registered BEFORE the
	// deletion to run AFTER it. Reversing these two registrations makes the test
	// fail with "cleanup left 5 demo rows behind" — the deletion is a guard, not
	// a promise.
	t.Cleanup(func() {
		// This is the leg that makes the deletion below a guard instead of a
		// promise: drop the delete cleanup and the test fails here.
		if left := edCount(t, store, `SELECT count(*) FROM fundamentals_detail WHERE snapshot_uri = $1`, snapshotURI); left != 0 {
			t.Errorf("cleanup left %d demo fundamentals_detail rows behind (anchor %s)", left, snapshotURI)
		}
		if left := edCount(t, store, `SELECT count(*) FROM ingest.raw WHERE content_hash = $1`, hash); left != 0 {
			t.Errorf("cleanup left %d demo ingest.raw rows behind", left)
		}
		if after := edCount(t, store, `SELECT count(*) FROM fundamentals_detail`); after != baseline {
			t.Errorf("fundamentals_detail did not return to its pre-test row count: before=%d after=%d", baseline, after)
		}
	})
	t.Cleanup(func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanCancel()
		// Delete only what this run wrote, by the coordinate it generated. The
		// raw record goes too: leaving a demo payload in the evidence table
		// would be noise the next session has to reason about.
		_, err := store.DB().Exec(cleanCtx, `DELETE FROM fundamentals_detail WHERE snapshot_uri = $1`, snapshotURI)
		if err != nil {
			t.Errorf("cleanup: delete fundamentals_detail: %v", err)
			return
		}
		if _, err := store.DB().Exec(cleanCtx, `DELETE FROM ingest.raw WHERE content_hash = $1`, hash); err != nil {
			t.Errorf("cleanup: delete ingest.raw demo record: %v", err)
		}
	})

	// ─── the door ───
	resp := edDo(t, srv, http.MethodPost, "/api/ingest/equitydeep?content_hash="+url.QueryEscape(hash), edSnapshotNDJSON())
	require.Equal(t, http.StatusCreated, resp.status, "body: %s", resp.body)

	var out equityDeepIngestResponse
	require.NoError(t, json.Unmarshal(resp.body, &out))
	assert.Equal(t, hash, out.ContentHash)
	assert.Equal(t, 3, out.Snapshots)
	// 5 = 营业总收入 + 存货 (first) + 营业总收入 + 资产总计 (later) + 营业总收入
	// (restatement). The whitelisted-but-unknown 净资产收益率 is dropped, not stored.
	assert.Equal(t, 5, out.Rows, "body: %s", resp.body)
	require.Len(t, out.Dropped, 1)
	assert.Equal(t, 1, out.Dropped[0].Line, "the dropped field must be locatable in the uploaded file")
	assert.Equal(t, "净资产收益率", out.Dropped[0].RawName)
	assert.Contains(t, out.Dropped[0].Reason, "raw_names whitelist")

	// ─── it really landed in the shared library ───
	assert.Equal(t, int64(5), edCount(t, store, `SELECT count(*) FROM fundamentals_detail WHERE snapshot_uri = $1`, snapshotURI))

	// Values, units and coordinates as they are actually stored — this is what
	// the five vertical factors will read, so the unit conversion and the
	// evidence coordinate have to be visible in the table, not only in the
	// normalizer's return value.
	//
	// Rows are addressed by (field_code, ann_date, fetched_at) instead of being
	// read positionally: that triple is the table's identity, and it is exactly
	// what distinguishes a restatement from a re-announcement.
	archived, err := store.GetRawIngest(ctx, hash)
	require.NoError(t, err)
	require.NotNil(t, archived, "the door must be able to resolve the hash it just handed back")
	ingestDay := day(archived.FetchedAt)

	{
		rowsCtx, rowsCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer rowsCancel()
		query := `SELECT field_code, ann_date, fetched_at, value, unit, source, raw_field_name, snapshot_uri
		          FROM fundamentals_detail WHERE snapshot_uri = $1`
		rset, err := store.DB().Query(rowsCtx, query, snapshotURI)
		require.NoError(t, err)

		got := map[string]edStoredRow{}
		for rset.Next() {
			var r edStoredRow
			require.NoError(t, rset.Scan(&r.FieldCode, &r.AnnDate, &r.FetchedAt, &r.Value,
				&r.Unit, &r.Source, &r.RawName, &r.SnapshotURI))
			got[r.key()] = r
		}
		require.NoError(t, rset.Err())
		rset.Close()

		require.Len(t, got, 5, "one row per (field_code, ann_date, fetched_at)")
		for k, r := range got {
			assert.Equalf(t, "CNY", r.Unit, "row %s: rows are stored in the dictionary base unit", k)
			assert.Equalf(t, snapshotURI, r.SnapshotURI, "row %s", k)
			assert.Regexpf(t, snapshotURIPattern, r.SnapshotURI,
				"row %s: every stored number must carry a resolvable ingest.raw coordinate", k)
		}

		// Announcement 2026-04-29, fetched 2026-05-10: the snapshot declared
		// CNY_100m, so 1.5 has to have become 150,000,000 元 by the time it is a row.
		first := mustEdRow(t, got, equitydeep.FieldTotalRevenue, "2026-04-29", "2026-05-10")
		require.NotNil(t, first.Value)
		assert.InDelta(t, 150_000_000, *first.Value, 1e-6)
		assert.Equal(t, "equitydeep:akshare", first.Source, "a snapshot without source inherits the ingest source")
		assert.Equal(t, "营业总收入", first.RawName, "the source key is kept verbatim for traceability")

		// The null in the same snapshot: 存货 stayed NULL rather than 0.
		inventory := mustEdRow(t, got, equitydeep.FieldInventory, "2026-04-29", "2026-05-10")
		assert.Nil(t, inventory.Value, "a null reading must be stored as NULL, not 0")

		// Same announcement, later fetch: the restatement is a NEW row and the
		// original is still there — the primary key includes fetched_at, so
		// history is never overwritten.
		restated := mustEdRow(t, got, equitydeep.FieldTotalRevenue, "2026-04-29", "2026-05-20")
		require.NotNil(t, restated.Value)
		assert.InDelta(t, 160_000_000, *restated.Value, 1e-6)

		// Second announcement (2026-08-29), already in base units, and with no
		// fetched_at of its own: it inherits the ingest record's fetch time.
		later := mustEdRow(t, got, equitydeep.FieldTotalRevenue, "2026-08-29", ingestDay)
		require.NotNil(t, later.Value)
		assert.InDelta(t, 200_000_000, *later.Value, 1e-6)
		assert.Equal(t, "equitydeep:ths", later.Source, "a snapshot may name its own producer")

		assets := mustEdRow(t, got, equitydeep.FieldTotalAssets, "2026-08-29", ingestDay)
		require.NotNil(t, assets.Value)
		assert.InDelta(t, 1_000_000_000, *assets.Value, 1e-6)
		assert.Equal(t, "资产总计", assets.RawName)

		// The field the whitelist refused never reached the table at all: not as
		// a raw_field_name, and not under a guessed field_code.
		for k, r := range got {
			assert.NotEqualf(t, "净资产收益率", r.RawName, "row %s: a dropped raw_name must not be stored", k)
			assert.NotEqualf(t, "roe", r.FieldCode, "row %s: a dropped raw_name must not be mapped by guess", k)
		}
	}

	// ─── read it back the way the factors do ───
	asOf := func(y int, m time.Month, d int) time.Time {
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	}
	// Before any announcement of this period: nothing may be visible. This is
	// the look-ahead bias the table exists to prevent.
	before, err := store.GetFundamentalsDetailAsOf(ctx, edAllFieldCodes, asOf(2026, 4, 1))
	require.NoError(t, err)
	assert.Empty(t, filterByTsCode(before, edDemoTsCode), "no reading may exist before its ann_date")

	// After the first announcement and its restatement, but BEFORE the second:
	// the second announcement's number must not be readable yet, even though
	// both describe the same report_period — which is exactly why this table
	// filters on ann_date and not end_date.
	mid, err := store.GetFundamentalsDetailAsOf(ctx, edAllFieldCodes, asOf(2026, 6, 30))
	require.NoError(t, err)
	midRev := filterByTsCodeAndField(mid, edDemoTsCode, equitydeep.FieldTotalRevenue)
	require.Len(t, midRev, 1, "one announcement known by 2026-06-30, restatements folded into it")
	assert.Equal(t, asOf(2026, 4, 29), midRev[0].AnnDate)
	require.NotNil(t, midRev[0].Value)
	assert.InDelta(t, 160_000_000, *midRev[0].Value, 1e-6,
		"the latest fetched_at for one (ts_code, end_date, ann_date, field_code) wins")
	assert.Equal(t, snapshotURI, midRev[0].SnapshotURI)
	assert.Regexp(t, snapshotURIPattern, midRev[0].SnapshotURI,
		"a stored number must carry a resolvable evidence coordinate, not a bare hash")
	assert.Equal(t, edDemoTsCode, midRev[0].TsCode)
	assert.Equal(t, asOf(2026, 3, 31), midRev[0].EndDate)

	// Both announcements are visible once both have been published.
	late, err := store.GetFundamentalsDetailAsOf(ctx, edAllFieldCodes, asOf(2026, 9, 30))
	require.NoError(t, err)
	lateRev := filterByTsCodeAndField(late, edDemoTsCode, equitydeep.FieldTotalRevenue)
	require.Len(t, lateRev, 2, "two announcements of one report period are two readings, not one")
	assert.Equal(t, asOf(2026, 4, 29), lateRev[0].AnnDate)
	assert.Equal(t, asOf(2026, 8, 29), lateRev[1].AnnDate)
	require.NotNil(t, lateRev[1].Value)
	assert.InDelta(t, 200_000_000, *lateRev[1].Value, 1e-6)

	// The inventory row is reachable too — and its value stays NULL after a
	// round trip through the table, which is what keeps "not disclosed" apart
	// from "zero".
	inventory := filterByTsCodeAndField(late, edDemoTsCode, equitydeep.FieldInventory)
	require.Len(t, inventory, 1)
	assert.Nil(t, inventory[0].Value)

	// Asking for no field codes reads nothing: the factors' guard against an
	// unbounded query.
	none, err := store.GetFundamentalsDetailAsOf(ctx, nil, asOf(2026, 12, 31))
	require.NoError(t, err)
	assert.Empty(t, none)
}

// TestEquityDeepIngestDoor_RejectsBeforeWriting pins the provenance gate itself:
// a hash that was never archived, a body that is not ndjson, and a body with no
// records must all fail without touching fundamentals_detail.
func TestEquityDeepIngestDoor_RejectsBeforeWriting(t *testing.T) {
	dsn := testenv.DatabaseDSN()
	if dsn == "" {
		testenv.SkipNoDB(t)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := storage.NewPostgresStoreConnected(ctx, dsn)
	require.NoError(t, err, "[testdb] TEST_DATABASE_DSN 已设置但连不上（fail-loud）")
	t.Cleanup(store.Close)

	before := edCount(t, store, `SELECT count(*) FROM fundamentals_detail`)
	srv := newEquityDeepTestDoor(t, store)

	// A hash of the right shape that ingest.raw has never seen — the caller
	// skipped POST /api/ingest/raw, so it has no evidence to quote.
	unarchived := strings.Repeat("a", 64)
	resp := edDo(t, srv, http.MethodPost, "/api/ingest/equitydeep?content_hash="+unarchived, edSnapFirst+"\n")
	assert.Equal(t, http.StatusBadRequest, resp.status)
	assert.Contains(t, string(resp.body), "never ingested")

	// A real archived hash, but a body whose second line is not a snapshot.
	hash := edPostRaw(t, srv, edDemoPayload(fmt.Sprintf("dbt11-reject-%d", time.Now().UnixNano())))
	resp = edDo(t, srv, http.MethodPost, "/api/ingest/equitydeep?content_hash="+hash,
		edSnapFirst+"\n"+`{"ticker":"broken`+"\n")
	assert.Equal(t, http.StatusBadRequest, resp.status)
	assert.Contains(t, string(resp.body), "line 2")

	// No records at all.
	resp = edDo(t, srv, http.MethodPost, "/api/ingest/equitydeep?content_hash="+hash, "  \n\n")
	assert.Equal(t, http.StatusBadRequest, resp.status)
	assert.Contains(t, string(resp.body), "no snapshot records")

	// Every rejection happened before any write.
	assert.Equal(t, before, edCount(t, store, `SELECT count(*) FROM fundamentals_detail`))

	// And the demo raw record this test archived is taken back out, so the run
	// leaves no more noise than it found.
	_, err = store.DB().Exec(ctx, `DELETE FROM ingest.raw WHERE content_hash = $1`, hash)
	require.NoError(t, err)
	assert.Equal(t, int64(0), edCount(t, store, `SELECT count(*) FROM ingest.raw WHERE content_hash = $1`, hash))
}

// TestEquityDeepIngestHandler_ValidationErrorsBeforeTheStore needs no database:
// each case has to fail before the store is touched, which a nil store proves —
// reaching it would panic instead of answering. Same technique as
// validationRouter() in handlers_ingest_test.go.
func TestEquityDeepIngestHandler_ValidationErrorsBeforeTheStore(t *testing.T) {
	t.Parallel()
	// gin mode is set once by TestMain (AUD-28) — do not call gin.SetMode here.

	// No dictionary loaded → 503, and it must be answered before the provenance
	// check: without the whitelist there is no mapping to apply at all.
	noDict := gin.New()
	noDict.POST("/api/ingest/equitydeep", equityDeepIngestHandler(nil, nil))
	w := httptest.NewRecorder()
	noDict.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/ingest/equitydeep?content_hash="+strings.Repeat("b", 64), strings.NewReader(edSnapFirst)))
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), "field dictionary is not loaded")

	// content_hash is validated before the store, so a nil store is safe here.
	cases := []struct {
		name string
		path string
	}{
		{"missing", "/api/ingest/equitydeep"},
		{"empty", "/api/ingest/equitydeep?content_hash="},
		{"too short", "/api/ingest/equitydeep?content_hash=deadbeef"},
		{"uppercase hex", "/api/ingest/equitydeep?content_hash=" + strings.Repeat("A", 64)},
		{"non hex", "/api/ingest/equitydeep?content_hash=" + strings.Repeat("g", 64)},
		{"too long", "/api/ingest/equitydeep?content_hash=" + strings.Repeat("0", 65)},
	}
	dict, err := equitydeep.LoadDictionary(edDictPath)
	require.NoError(t, err)
	router := gin.New()
	router.POST("/api/ingest/equitydeep", equityDeepIngestHandler(nil, dict))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(edSnapFirst)))
			assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
			assert.Contains(t, w.Body.String(), "64-character lowercase hex")
		})
	}
}

// ─── helpers ────────────────────────────────────────────────────────────────

// edAllFieldCodes is the set of deep-financial codes the five B1 vertical
// factors read (pkg/data/factor_equitydeep.go). It is taken from the package
// constants and then cross-checked against the frozen dictionary, so this test
// cannot pass on a code the dictionary does not actually know.
var edAllFieldCodes = []string{
	equitydeep.FieldTotalRevenue,
	equitydeep.FieldOperatingCost,
	equitydeep.FieldNetProfitAttr,
	equitydeep.FieldTotalAssets,
	equitydeep.FieldEquityAttr,
	equitydeep.FieldContractLiability,
	equitydeep.FieldInventory,
	equitydeep.FieldOCFNet,
}

func TestEdAllFieldCodesAreInTheFrozenDictionary(t *testing.T) {
	t.Parallel()
	dict, err := equitydeep.LoadDictionary(edDictPath)
	require.NoError(t, err)
	for _, code := range edAllFieldCodes {
		require.Contains(t, dict.FieldCodes(), code,
			"a field code the factors read must exist in the frozen dictionary")
	}
}

// edStoredRow is one fundamentals_detail row as the library holds it.
type edStoredRow struct {
	FieldCode   string
	AnnDate     time.Time
	FetchedAt   time.Time
	Value       *float64
	Unit        string
	Source      string
	RawName     string
	SnapshotURI string
}

func (r edStoredRow) key() string {
	return r.FieldCode + "@" + day(r.AnnDate) + "@" + day(r.FetchedAt)
}

// mustEdRow fails the test when the row is absent. A missing row must never
// reach an assertion as a zero value: with *float64 nil, "the row is not there"
// and "the source had no reading" would look identical to the assertions below.
func mustEdRow(t *testing.T, got map[string]edStoredRow, fieldCode, annDay, fetchDay string) edStoredRow {
	t.Helper()
	key := fieldCode + "@" + annDay + "@" + fetchDay
	r, ok := got[key]
	require.Truef(t, ok, "row %s missing; present rows: %v", key, edRowKeys(got))
	return r
}

func edRowKeys(m map[string]edStoredRow) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// day renders the calendar day a stored value carries, in UTC. Both sides of
// every comparison below use it, so the render rule cannot skew a key: pgx
// scans DATE as UTC midnight, and TIMESTAMPTZ as an instant whose UTC day is
// the one the writer meant. For the row whose fetched_at came from the ingest
// record, the expectation is that same record read back through GetRawIngest,
// so the two sides cannot disagree about which day it was.
func day(ts time.Time) string { return ts.UTC().Format("2006-01-02") }

func filterByTsCode(rows []domain.FundamentalsDetailRow, tsCode string) []domain.FundamentalsDetailRow {
	var out []domain.FundamentalsDetailRow
	for _, r := range rows {
		if r.TsCode == tsCode {
			out = append(out, r)
		}
	}
	return out
}

func filterByTsCodeAndField(rows []domain.FundamentalsDetailRow, tsCode, fieldCode string) []domain.FundamentalsDetailRow {
	var out []domain.FundamentalsDetailRow
	for _, r := range rows {
		if r.TsCode == tsCode && r.FieldCode == fieldCode {
			out = append(out, r)
		}
	}
	return out
}
