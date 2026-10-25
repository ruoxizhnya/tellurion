package equitydeep

// Unit tests for the helper rules of this package (DBT-11): ticker
// normalization, the contract date layout, and source-side number parsing.
// The normalization rules themselves are tested in snapshot_test.go.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToTsCode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		want    string
		wantErr string
	}{
		{"600519", "600519.SH", ""},
		{"900901", "900901.SH", ""}, // leading 9 → Shanghai, per normalizeSymbol
		{"000001", "000001.SZ", ""},
		{"300750", "300750.SZ", ""},
		{"688981", "688981.SH", ""},
		// A bare code never yields .BJ: only 6/9 prefix → SH, everything else
		// → SZ. Beijing is reachable only by carrying the suffix explicitly.
		{"830799", "830799.SZ", ""},
		{"  600519  ", "600519.SH", ""},
		{"600519.SH", "600519.SH", ""}, // already suffixed: validated, returned as-is
		{"600519.sh", "600519.SH", ""},
		{"830799.BJ", "830799.BJ", ""},
		{"", "", "ticker is empty"},
		{"60051", "", "expected 6 digits"},
		{"6005190", "", "expected 6 digits"},
		{"60O519", "", "expected 6 digits"},
		{"600519.HK", "", `unknown exchange "HK"`},
		{".SH", "", "expected 6 digits"},
		{"abc.SH", "", "code must be 6 digits"},
		{"123456.XSH", "", `unknown exchange "XSH"`},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ToTsCode(tc.in)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestIsDigits(t *testing.T) {
	t.Parallel()
	assert.False(t, isDigits(""))
	assert.False(t, isDigits("12a"))
	assert.True(t, isDigits("000001"))
}

// parseContractDate is where the schema's "format: date" is enforced for the
// PIT anchors. The table pins all four shapes named by the DBT-14 acceptance:
// a calendar date is accepted; RFC3339 with Z, RFC3339 with an offset and a
// garbage string are rejected. fetched_at no longer goes through this parser
// (裁-16 gave it its own, wider one), which is exactly why these cases must
// keep proving it was not widened along with it.
func TestParseContractDate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		value   string
		want    time.Time
		wantOK  bool
		wantErr string
	}{
		{"calendar date", "2026-04-29", time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC), true, ""},
		{"RFC3339 Z", "2026-04-29T00:00:00Z", time.Time{}, false, "ann_date must be YYYY-MM-DD"},
		{"RFC3339 +08:00", "2026-04-29T00:00:00+08:00", time.Time{}, false, "ann_date must be YYYY-MM-DD"},
		{"garbage", "29/04/2026", time.Time{}, false, "ann_date must be YYYY-MM-DD"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, ok, err := parseContractDate("ann_date", tc.value)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantErr == "" {
				require.NoError(t, err)
				assert.Equal(t, tc.want, ts)
				return
			}
			require.Error(t, err)
			assert.True(t, ts.IsZero())
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}

	t.Run("report_period keeps the same strictness", func(t *testing.T) {
		for _, v := range []string{"2026-04-29T00:00:00Z", "20260429"} {
			_, ok, err := parseContractDate("report_period", v)
			require.Errorf(t, err, "report_period=%q must be rejected", v)
			assert.False(t, ok)
			assert.Contains(t, err.Error(), "report_period must be YYYY-MM-DD")
		}
	})

	t.Run("whitespace and empty", func(t *testing.T) {
		// Whitespace tolerated, empty is "absent" not "invalid" — the caller
		// decides whether absence is an error.
		ts, ok, err := parseContractDate("ann_date", "  2026-04-29 ")
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC), ts)

		for _, v := range []string{"", "   "} {
			_, ok, err := parseContractDate("ann_date", v)
			require.NoError(t, err)
			assert.False(t, ok, "an empty value must be reported as absent, not invalid")
		}
	})
}

// TestParseFetchedAt pins 裁-16's shape table for the one field that is
// declared "format: date-time" in contracts/snapshot.schema.json: date-only is
// a 00:00Z day marker, RFC3339 is an exact instant converted to UTC, and
// anything else is rejected. The last case matters: a date-time without an
// offset is not RFC3339, and reading it as UTC would be a guess.
func TestParseFetchedAt(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		value   string
		want    time.Time
		wantOK  bool
		wantErr string
	}{
		{"date-only day marker", "2026-05-06", time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC), true, ""},
		{"RFC3339 Z", "2026-05-06T03:00:00Z", time.Date(2026, 5, 6, 3, 0, 0, 0, time.UTC), true, ""},
		{"RFC3339 +08:00 converts to UTC", "2026-05-06T11:00:00+08:00", time.Date(2026, 5, 6, 3, 0, 0, 0, time.UTC), true, ""},
		{"garbage", "06-05-2026", time.Time{}, false, "fetched_at must be RFC3339 or YYYY-MM-DD"},
		{"date-time without offset", "2026-05-06T03:00:00", time.Time{}, false, "fetched_at must be RFC3339 or YYYY-MM-DD"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, ok, err := parseFetchedAt("fetched_at", tc.value)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantErr == "" {
				require.NoError(t, err)
				assert.Equal(t, tc.want, ts)
				return
			}
			require.Error(t, err)
			assert.True(t, ts.IsZero())
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}

	t.Run("whitespace and empty", func(t *testing.T) {
		ts, ok, err := parseFetchedAt("fetched_at", "  2026-05-06T03:00:00Z ")
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, time.Date(2026, 5, 6, 3, 0, 0, 0, time.UTC), ts)

		for _, v := range []string{"", "   "} {
			_, ok, err := parseFetchedAt("fetched_at", v)
			require.NoError(t, err)
			assert.False(t, ok, "an empty value must be reported as absent, not invalid")
		}
	})
}

func TestParseNumber(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want float64
		unit string
	}{
		{"1234", 1234, ""},
		{"  42  ", 42, ""},
		{"1,234", 1234, ""},   // thousands separators are a formatting detail
		{"2亿", 2, "CNY_100m"}, // longest suffix first: 亿元 before 亿
		{"1.5亿元", 1.5, "CNY_100m"},
		{"10万", 10, "CNY_10k"},
		{"3万元", 3, "CNY_10k"},
		{"-7", -7, ""},
		{"0", 0, ""},
		{".5", 0.5, ""},
		{"1e3", 1000, ""}, // strconv's grammar; the contract does not forbid it
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			v, unit, err := parseNumber(tc.in)
			require.NoError(t, err)
			assert.InDelta(t, tc.want, v, 1e-9)
			assert.Equal(t, tc.unit, unit)
		})
	}
}

func TestParseNumber_RejectsUnparsable(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"", "   ", "不是数字", "亿", "1.2.3", "12abc", "12 34", "1,2 3 4"} {
		_, _, err := parseNumber(in)
		require.Error(t, err, "parseNumber(%q) must fail", in)
	}

	// An empty value is reported as such: it is a missing field, not a number.
	_, _, err := parseNumber("")
	require.ErrorContains(t, err, "empty string value")

	// A bare magnitude suffix has no number to attach to.
	_, _, err = parseNumber("亿")
	require.ErrorContains(t, err, "not a plain decimal number")

	_, _, err = parseNumber("一千")
	require.ErrorContains(t, err, "not a plain decimal number")
}
