package equitydeep

// Unit tests for the two contract surfaces of this package (DBT-11):
// DecodeSnapshot (contracts/snapshot.schema.json) and Normalizer.Normalize
// (contracts/field_dictionary.yaml).
//
// ODR-055 registered this package as the highest-priority coverage gap: the
// rules below had never been executed by a test, so each was only asserted in
// prose. These tests pin the behaviour that exists today — they do not redefine
// it. Where the implementation and the upstream contract disagree, the
// disagreement is recorded rather than papered over (see
// TestNormalize_ContractDateLayoutIsCalendarDateOnly).

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testDictionaryYAML is a minimal on-contract dictionary: the same shape and a
// subset of the codes the B1 vertical factors read, so a unit test can state
// its expectations without depending on the frozen file's full 20-code set.
// TestFieldDictionary_ContractsFileMatchesTheFrozenCodes pins the real file.
const testDictionaryYAML = `
version: 1
status: frozen
frozen_at: "2026-09-15"
base_unit: CNY
unit_scale:
  CNY: 1
  CNY_thousand: 1000
  CNY_10k: 10000
  CNY_million: 1000000
  CNY_100m: 100000000
fields:
  - field_code: total_revenue
    raw_names: ["营业总收入", "营业总收入(元)"]
    unit: CNY
    statement: income
  - field_code: inventory
    raw_names: ["存货"]
    unit: CNY
    statement: balance
  - field_code: total_assets
    raw_names: ["资产总计", "资产合计"]
    unit: CNY
    statement: balance
`

func mustTestDictionary(t *testing.T) *Dictionary {
	t.Helper()
	d, err := ParseDictionary([]byte(testDictionaryYAML))
	require.NoError(t, err)
	return d
}

// validSnapshot is an on-contract snapshot; tests mutate one field to drive a
// specific branch.
func validSnapshot() *Snapshot {
	return &Snapshot{
		Ticker:       "600519",
		ReportPeriod: "2026-03-31",
		AnnDate:      "2026-04-29",
		Unit:         "CNY",
		Data:         map[string]any{"营业总收入": json.Number("1")},
	}
}

var ingestTime = time.Date(2026, 9, 15, 3, 0, 0, 0, time.UTC)

func normalize(t *testing.T, n *Normalizer, s *Snapshot, uri string, at time.Time) (Result, error) {
	t.Helper()
	if n == nil {
		n = NewNormalizer(mustTestDictionary(t), "akshare")
	}
	return n.Normalize(s, uri, at)
}

// ─── DecodeSnapshot ─────────────────────────────────────────────────────────

func TestDecodeSnapshot_KeepsRawNamesAndNumberPrecision(t *testing.T) {
	t.Parallel()
	line := []byte(`{"ticker":"600519","report_period":"2026-03-31","ann_date":"2026-04-29","source":"akshare","dataset":"stock_financial_abstract","fetched_at":"2026-05-06","unit":"CNY_100m","data":{"营业总收入":1234.567890123,"存货":null}}`)

	s, err := DecodeSnapshot(line)
	require.NoError(t, err)
	assert.Equal(t, "600519", s.Ticker)
	assert.Equal(t, "2026-03-31", s.ReportPeriod)
	assert.Equal(t, "2026-04-29", s.AnnDate)
	assert.Equal(t, "akshare", s.Source)
	assert.Equal(t, "stock_financial_abstract", s.Dataset)
	assert.Equal(t, "2026-05-06", s.FetchedAt)
	assert.Equal(t, "CNY_100m", s.Unit)

	// Keys are kept verbatim: no translation, no rewriting (schema rule 1).
	require.Contains(t, s.Data, "营业总收入")

	// dec.UseNumber(): the literal is not squeezed through float64 before the
	// unit conversion, so a long decimal keeps its digits.
	v, ok := s.Data["营业总收入"].(json.Number)
	require.Truef(t, ok, "data value must decode as json.Number, got %T", s.Data["营业总收入"])
	assert.Equal(t, "1234.567890123", v.String())

	// null is preserved as nil, not as 0.
	assert.Nil(t, s.Data["存货"])
}

func TestDecodeSnapshot_RejectsBadLines(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		line  string
		wants string
	}{
		{"empty line", ``, "decode snapshot"},
		{"truncated json", `{"ticker":`, "decode snapshot"},
		{"array instead of object", `[{"ticker":"600519"}]`, "decode snapshot"},
		{"unknown top-level key", `{"ticker":"600519","report_period":"2026-03-31","ann_date":"2026-04-29","data":{},"note":"extra"}`, `"note"`},
		{"renamed required key", `{"ts_code":"600519","report_period":"2026-03-31","ann_date":"2026-04-29","data":{"存货":1}}`, `"ts_code"`},
		{"two objects on one line", `{"ticker":"600519","data":{}} {"ticker":"000001","data":{}}`, "trailing content"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := DecodeSnapshot([]byte(tc.line))
			require.Error(t, err)
			assert.Nil(t, s)
			assert.Contains(t, err.Error(), tc.wants)
		})
	}
}

// TestDecodeSnapshot_DoesNotEnforceRequiredKeys pins a real limit of the
// current gate: DecodeSnapshot enforces additionalProperties:false but not the
// "required" list — an absent ticker or ann_date reaches Normalize, which is
// where those become errors. The chain is still safe (nothing can land in the
// table without them), so this is a description of the division of labour, not
// a hole.
func TestDecodeSnapshot_DoesNotEnforceRequiredKeys(t *testing.T) {
	t.Parallel()
	s, err := DecodeSnapshot([]byte(`{"data":{"存货":1}}`))
	require.NoError(t, err, "required-key enforcement belongs to Normalize")
	assert.Empty(t, s.Ticker)

	_, err = NewNormalizer(mustTestDictionary(t), "akshare").Normalize(s, "ingest.raw:0", ingestTime)
	require.ErrorContains(t, err, "ticker is empty")
}

// data's keys are the raw field names and are deliberately NOT validated at
// decode time; the whitelist check belongs to Normalize. This pins that
// division: an unknown data key decodes fine and is dropped later.
func TestDecodeSnapshot_UnknownDataKeyDecodesAndIsDroppedByNormalize(t *testing.T) {
	t.Parallel()
	s, err := DecodeSnapshot([]byte(`{"ticker":"600519","report_period":"2026-03-31","ann_date":"2026-04-29","data":{"完全未知的字段":1}}`))
	require.NoError(t, err)
	assert.Len(t, s.Data, 1)

	res, err := normalize(t, nil, s, "ingest.raw:0", ingestTime)
	require.NoError(t, err)
	assert.Empty(t, res.Rows)
	require.Len(t, res.Dropped, 1)
	assert.Equal(t, "完全未知的字段", res.Dropped[0].RawName)
}

// ─── Normalize: the row shape it owes the table ─────────────────────────────

func TestNormalize_ValidSnapshotProducesBaseUnitRows(t *testing.T) {
	t.Parallel()
	s := validSnapshot()
	s.Unit = "CNY_100m"
	s.Dataset = "stock_financial_abstract"
	s.Data = map[string]any{
		"营业总收入": json.Number("1234.567890123"),
		"存货":    json.Number("7"),
	}

	res, err := normalize(t, nil, s, "ingest.raw:abc", ingestTime)
	require.NoError(t, err)
	require.Len(t, res.Rows, 2)
	assert.Empty(t, res.Dropped)

	// Deterministic order: raw names sorted, so one snapshot always yields the
	// same row sequence (Go map iteration is randomized, and the ingest must be
	// reproducible).
	assert.Equal(t, "存货", res.Rows[0].RawFieldName)
	assert.Equal(t, "营业总收入", res.Rows[1].RawFieldName)

	r := res.Rows[1]
	assert.Equal(t, "600519.SH", r.TsCode, "ticker must be normalized to ts_code")
	assert.Equal(t, FieldTotalRevenue, r.FieldCode)
	assert.Equal(t, "CNY", r.Unit, "rows are stored in the dictionary base unit")
	require.NotNil(t, r.Value)
	assert.InDelta(t, 1234.567890123*1e8, *r.Value, 1e-3)
	assert.Equal(t, ingestTime, r.FetchedAt, "absent fetched_at falls back to the ingest record")
	assert.Equal(t, "equitydeep:akshare", r.Source, "absent source falls back to the ingest source, prefixed")
	assert.Equal(t, "ingest.raw:abc", r.SnapshotURI, "every row carries the evidence coordinate")
	assert.Equal(t, time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC), r.EndDate)
	assert.Equal(t, time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC), r.AnnDate)
}

func TestNormalize_SnapshotOwnSourceAndFetchedAtWinOverTheIngestRecord(t *testing.T) {
	t.Parallel()
	s := validSnapshot()
	s.Ticker = "000001"
	s.Source = "ths"
	s.FetchedAt = "2026-05-06"
	s.Data = map[string]any{"资产总计": json.Number("1000")}

	res, err := normalize(t, nil, s, "ingest.raw:abc", ingestTime)
	require.NoError(t, err)
	require.Len(t, res.Rows, 1)
	assert.Equal(t, "equitydeep:ths", res.Rows[0].Source)
	assert.Equal(t, "000001.SZ", res.Rows[0].TsCode)
	assert.Equal(t, time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC), res.Rows[0].FetchedAt)
}

// TestNormalize_MissingAnnDateIsRejectedNotBackfilled is the single most
// important assertion in this file: ann_date is the PIT key, and substituting
// report_period for it would hand a factor numbers from before they were
// published.
func TestNormalize_MissingAnnDateIsRejectedNotBackfilled(t *testing.T) {
	t.Parallel()
	s := validSnapshot()
	s.AnnDate = ""

	_, err := normalize(t, nil, s, "ingest.raw:abc", ingestTime)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ann_date is required")
	assert.Contains(t, err.Error(), "look-ahead bias")
}

func TestNormalize_ContractViolationsAreErrors(t *testing.T) {
	t.Parallel()
	n := NewNormalizer(mustTestDictionary(t), "akshare")

	cases := []struct {
		name   string
		mutate func(*Snapshot)
		wants  string
	}{
		{"empty ticker", func(s *Snapshot) { s.Ticker = "" }, "ticker is empty"},
		{"malformed ticker", func(s *Snapshot) { s.Ticker = "60051" }, "expected 6 digits"},
		{"unknown exchange suffix", func(s *Snapshot) { s.Ticker = "600519.HK" }, "unknown exchange"},
		{"malformed ann_date", func(s *Snapshot) { s.AnnDate = "20260429" }, "ann_date must be YYYY-MM-DD"},
		{"missing ann_date", func(s *Snapshot) { s.AnnDate = "" }, "ann_date is required"},
		{"malformed report_period", func(s *Snapshot) { s.ReportPeriod = "20260331" }, "report_period must be YYYY-MM-DD"},
		{"missing report_period", func(s *Snapshot) { s.ReportPeriod = "" }, "report_period is required"},
		{"unit outside unit_scale", func(s *Snapshot) { s.Unit = "USD" }, "not in the unit_scale table"},
		{"malformed fetched_at", func(s *Snapshot) { s.FetchedAt = "06-05-2026" }, "fetched_at must be YYYY-MM-DD"},
		{"empty data", func(s *Snapshot) { s.Data = map[string]any{} }, "data is empty"},
		{"nil data", func(s *Snapshot) { s.Data = nil }, "data is empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := validSnapshot()
			tc.mutate(s)
			res, err := n.Normalize(s, "ingest.raw:abc", ingestTime)
			require.Error(t, err, "a contract violation must fail the snapshot, not drop fields")
			assert.Contains(t, err.Error(), tc.wants)
			assert.Empty(t, res.Rows)
			assert.Empty(t, res.Dropped)
		})
	}
}

func TestNormalize_NilSnapshotAndNilDictionaryAreErrors(t *testing.T) {
	t.Parallel()
	_, err := NewNormalizer(mustTestDictionary(t), "akshare").Normalize(nil, "ingest.raw:abc", ingestTime)
	require.ErrorContains(t, err, "snapshot is nil")

	_, err = NewNormalizer(nil, "akshare").Normalize(validSnapshot(), "ingest.raw:abc", ingestTime)
	require.ErrorContains(t, err, "no field dictionary")
}

// TestNormalize_MissingFetchedAtOnBothSidesIsAnError pins the fallback chain
// snapshot.fetched_at → ingest.raw.fetched_at → error. A row with no fetch time
// would have no place in the primary key, and the restatement ordering of
// GetFundamentalsDetailAsOf depends on fetched_at existing.
func TestNormalize_MissingFetchedAtOnBothSidesIsAnError(t *testing.T) {
	t.Parallel()
	s := validSnapshot()
	s.FetchedAt = ""

	_, err := normalize(t, nil, s, "ingest.raw:abc", time.Time{})
	require.ErrorContains(t, err, "fetched_at is required")
}

// TestNormalize_SourceCheckOrder: fetched_at is validated before source, so a
// snapshot missing both reports the fetch time first. Pinned because the
// handler surfaces only the first message per line.
func TestNormalize_SourceAndFetchedAtErrorOrder(t *testing.T) {
	t.Parallel()
	n := NewNormalizer(mustTestDictionary(t), "")
	s := validSnapshot()
	s.FetchedAt = ""

	_, err := n.Normalize(s, "ingest.raw:abc", time.Time{})
	require.ErrorContains(t, err, "fetched_at is required")

	_, err = n.Normalize(validSnapshot(), "ingest.raw:abc", ingestTime)
	require.ErrorContains(t, err, "source is required")
}

// ─── Normalize: whitelist misses and bad values are drops, not errors ───────

// TestNormalize_WhitelistMissIsDroppedAndReported pins contract rule 1 / 3: a
// raw_name outside raw_names is discarded WITH a reason while the rest of the
// snapshot still lands. Failing the snapshot would let one unknown key block
// every known number; mapping it by guess would corrupt them.
func TestNormalize_WhitelistMissIsDroppedAndReported(t *testing.T) {
	t.Parallel()
	s := validSnapshot()
	s.Data = map[string]any{
		"营业总收入":  json.Number("1"),
		"净资产收益率": json.Number("8.2"), // ROE is not in the frozen dictionary
		"营业收入":   json.Number("1"),   // near-miss for 营业总收入: similarity is not a mapping
	}

	res, err := normalize(t, nil, s, "ingest.raw:abc", ingestTime)
	require.NoError(t, err)
	require.Len(t, res.Rows, 1)
	assert.Equal(t, FieldTotalRevenue, res.Rows[0].FieldCode)

	require.Len(t, res.Dropped, 2)
	byName := map[string]string{}
	for _, d := range res.Dropped {
		byName[d.RawName] = d.Reason
	}
	require.Contains(t, byName, "净资产收益率")
	assert.Contains(t, byName["净资产收益率"], "raw_names whitelist")
	require.Contains(t, byName, "营业收入")
	assert.Contains(t, byName["营业收入"], "raw_names whitelist")
}

// TestNormalize_AllRawNamesOfOneFieldCodeAreEquivalent pins that the aliases a
// dictionary entry declares are interchangeable: 营业总收入(元) and 营业总收入
// map to the same field_code.
func TestNormalize_AllRawNamesOfOneFieldCodeAreEquivalent(t *testing.T) {
	t.Parallel()
	for _, rawName := range []string{"营业总收入", "营业总收入(元)"} {
		s := validSnapshot()
		s.Data = map[string]any{rawName: json.Number("3")}
		res, err := normalize(t, nil, s, "ingest.raw:abc", ingestTime)
		require.NoErrorf(t, err, "raw_name %q", rawName)
		require.Len(t, res.Rows, 1, "raw_name %q", rawName)
		assert.Equal(t, FieldTotalRevenue, res.Rows[0].FieldCode)
		assert.Equal(t, rawName, res.Rows[0].RawFieldName, "the source key is stored verbatim")
	}

	// 资产合计 is the second alias of total_assets.
	s := validSnapshot()
	s.Data = map[string]any{"资产合计": json.Number("3")}
	res, err := normalize(t, nil, s, "ingest.raw:abc", ingestTime)
	require.NoError(t, err)
	require.Len(t, res.Rows, 1)
	assert.Equal(t, FieldTotalAssets, res.Rows[0].FieldCode)
}

func TestNormalize_UnparseableValueIsDroppedAndReportedWithReason(t *testing.T) {
	t.Parallel()
	s := validSnapshot()
	s.Data = map[string]any{
		"营业总收入": "不是数字",
		"存货":    json.Number("5"),
		"资产总计":  true,
	}

	res, err := normalize(t, nil, s, "ingest.raw:abc", ingestTime)
	require.NoError(t, err)
	require.Len(t, res.Rows, 1)
	assert.Equal(t, FieldInventory, res.Rows[0].FieldCode)

	byName := map[string]string{}
	for _, d := range res.Dropped {
		byName[d.RawName] = d.Reason
	}
	require.Len(t, byName, 2)
	require.Contains(t, byName, "营业总收入")
	assert.Contains(t, byName["营业总收入"], "not a plain decimal number")
	require.Contains(t, byName, "资产总计")
	assert.Contains(t, byName["资产总计"], "unsupported value type")
}

// TestNormalize_NullStaysNullAndZeroStaysZero: value is the only nullable column
// precisely so that "the source had no reading" survives into the table.
func TestNormalize_NullStaysNullAndZeroStaysZero(t *testing.T) {
	t.Parallel()
	s := validSnapshot()
	s.Data = map[string]any{"营业总收入": nil, "存货": json.Number("0")}

	res, err := normalize(t, nil, s, "ingest.raw:abc", ingestTime)
	require.NoError(t, err)
	require.Len(t, res.Rows, 2)
	assert.Empty(t, res.Dropped)

	for _, r := range res.Rows {
		switch r.FieldCode {
		case FieldTotalRevenue:
			assert.Nil(t, r.Value, "null must become SQL NULL, not 0")
		case FieldInventory:
			require.NotNil(t, r.Value)
			assert.Zero(t, *r.Value, "a real zero stays a zero")
		default:
			t.Fatalf("unexpected field_code %q", r.FieldCode)
		}
	}
}

// TestNormalize_NumberOutsideFloat64RangeIsDropped covers the one convert
// branch a syntactically valid json.Number can still hit: a literal beyond
// float64's range is dropped with its reason instead of landing as +Inf.
func TestNormalize_NumberOutsideFloat64RangeIsDropped(t *testing.T) {
	t.Parallel()
	s := validSnapshot()
	s.Data = map[string]any{"营业总收入": json.Number("1e999")}

	res, err := normalize(t, nil, s, "ingest.raw:abc", ingestTime)
	require.NoError(t, err, "one unrepresentable field must not fail the whole snapshot")
	assert.Empty(t, res.Rows)
	require.Len(t, res.Dropped, 1)
	assert.Contains(t, res.Dropped[0].Reason, "not representable")
}

// ─── value forms and unit_scale ─────────────────────────────────────────────

func TestNormalize_ValueFormsAndUnitScales(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		unit     string
		value    any
		expected float64
	}{
		{"json number, base unit", "CNY", json.Number("12.5"), 12.5},
		{"json number, 100m unit", "CNY_100m", json.Number("2"), 2e8},
		{"json number, thousand unit", "CNY_thousand", json.Number("3"), 3e3},
		{"float64 (hand-built snapshot)", "CNY_10k", float64(4), 4e4},
		{"int (hand-built snapshot)", "CNY_million", 5, 5e6},
		{"string with thousands separator", "CNY", "1,234", 1234},
		{"string whose 亿 suffix overrides the declared unit", "CNY", "2亿", 2e8},
		{"string with 亿元 suffix", "CNY_100m", "1.5亿元", 1.5e8},
		{"string with 万元 suffix", "CNY", "10万", 1e5},
		{"string with 万 suffix", "CNY", "3万", 3e4},
		{"absent unit defaults to base_unit", "", "7", 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := validSnapshot()
			s.Unit = tc.unit
			s.Data = map[string]any{"营业总收入": tc.value}

			res, err := normalize(t, nil, s, "ingest.raw:abc", ingestTime)
			require.NoError(t, err)
			require.Len(t, res.Rows, 1, "unexpected drop: %v", res.Dropped)
			require.NotNil(t, res.Rows[0].Value)
			assert.InDelta(t, tc.expected, *res.Rows[0].Value, 1e-9)
			assert.Equal(t, "CNY", res.Rows[0].Unit)
		})
	}
}

// TestNormalize_SuffixUnitOutsideTableIsDropped pins that a magnitude stated by
// the producer is never resolved by inference: if the suffix maps to a unit the
// dictionary does not scale, the field is dropped.
func TestNormalize_SuffixUnitOutsideTableIsDropped(t *testing.T) {
	t.Parallel()
	d, err := ParseDictionary([]byte(`
version: 1
base_unit: CNY
unit_scale:
  CNY: 1
fields:
  - field_code: total_revenue
    raw_names: ["营业总收入"]
    unit: CNY
`))
	require.NoError(t, err)

	s := validSnapshot()
	s.Data = map[string]any{"营业总收入": "1亿"}

	res, err := NewNormalizer(d, "akshare").Normalize(s, "ingest.raw:abc", ingestTime)
	require.NoError(t, err)
	assert.Empty(t, res.Rows)
	require.Len(t, res.Dropped, 1)
	assert.Contains(t, res.Dropped[0].Reason, "suffix unit")
}

// TestNormalize_OneFieldCannotBlockAnother is the reason dropping exists: the
// snapshot below mixes a good field, an unknown key, an unparseable value and a
// null, and the good number still lands.
func TestNormalize_OneFieldCannotBlockAnother(t *testing.T) {
	t.Parallel()
	s := validSnapshot()
	s.Data = map[string]any{
		"营业总收入": json.Number("1"),
		"未知字段A": json.Number("2"),
		"存货":    "not-a-number",
		"资产总计":  nil,
	}

	res, err := normalize(t, nil, s, "ingest.raw:abc", ingestTime)
	require.NoError(t, err)
	require.Len(t, res.Rows, 2)
	assert.Equal(t, FieldTotalRevenue, res.Rows[0].FieldCode)
	assert.Equal(t, FieldTotalAssets, res.Rows[1].FieldCode)
	assert.Nil(t, res.Rows[1].Value, "a null in a surviving field still lands as NULL")
	assert.Len(t, res.Dropped, 2)
}

// ─── documented drift between the code and snapshot.schema.json ─────────────

// TestNormalize_ContractDateLayoutIsCalendarDateOnly records, WITHOUT changing
// it, that snapshot.fetched_at is declared "format: date-time" in
// contracts/snapshot.schema.json but is parsed with the calendar-date layout
// only, so an on-contract producer is rejected for the whole snapshot. Fixing
// it is a contract decision, not a test decision.
func TestNormalize_ContractDateLayoutIsCalendarDateOnly(t *testing.T) {
	t.Parallel()
	n := NewNormalizer(mustTestDictionary(t), "akshare")

	s := validSnapshot()
	s.FetchedAt = "2026-05-06"
	_, err := n.Normalize(s, "ingest.raw:abc", ingestTime)
	require.NoError(t, err, "YYYY-MM-DD is what the implementation accepts today")

	s = validSnapshot()
	s.FetchedAt = "2026-05-06T03:00:00Z"
	_, err = n.Normalize(s, "ingest.raw:abc", ingestTime)
	require.Error(t, err, "the schema's own date-time form is rejected today (documented drift)")
	assert.Contains(t, err.Error(), "fetched_at must be YYYY-MM-DD")
}
