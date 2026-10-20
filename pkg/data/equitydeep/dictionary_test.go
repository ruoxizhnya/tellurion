package equitydeep

// Unit tests for the field dictionary loader (DBT-11): the contract file
// contracts/field_dictionary.yaml is the whitelist and the conversion table, so
// both its structural validation and its frozen content are pinned here.

import (
	"testing"

	"os"
	"path/filepath"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// contractsDictionaryPath is the frozen contract file as seen from this
// package's test working directory (the go tool runs a package's tests with
// that package's directory as cwd).
const contractsDictionaryPath = "../../../contracts/field_dictionary.yaml"

// ─── ParseDictionary: structural validation ─────────────────────────────────

func TestParseDictionary_ValidInputBuildsLookups(t *testing.T) {
	t.Parallel()
	d := mustTestDictionary(t)

	assert.Equal(t, 1, d.Version)
	assert.Equal(t, "frozen", d.Status)
	assert.Equal(t, "CNY", d.BaseUnit)

	f, ok := d.LookupRaw("营业总收入")
	require.True(t, ok)
	assert.Equal(t, FieldTotalRevenue, f.FieldCode)

	// Aliases of the same field_code resolve to the same entry.
	alias, ok := d.LookupRaw("营业总收入(元)")
	require.True(t, ok)
	assert.Equal(t, FieldTotalRevenue, alias.FieldCode)

	// A miss is a legitimate answer, not an error: the caller must drop and warn.
	_, ok = d.LookupRaw("营业总收入（元）全角括号变体")
	assert.False(t, ok)
	_, ok = d.LookupRaw("")
	assert.False(t, ok)

	scale, ok := d.ScaleFor("CNY_100m")
	require.True(t, ok)
	assert.Equal(t, 1e8, scale)
	_, ok = d.ScaleFor("USD")
	assert.False(t, ok)

	assert.Equal(t, []string{"inventory", "total_assets", "total_revenue"}, d.FieldCodes())
}

func TestParseDictionary_RejectsStructurallyInvalidDictionaries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		yaml  string
		wants string
	}{
		{"not yaml", `: :`, "parse yaml"},
		{"empty unit_scale", `
base_unit: CNY
fields:
  - field_code: total_revenue
    raw_names: ["营业总收入"]
    unit: CNY
`, "unit_scale is empty"},
		{"base_unit outside unit_scale", `
base_unit: USD
unit_scale:
  CNY: 1
fields:
  - field_code: total_revenue
    raw_names: ["营业总收入"]
    unit: CNY
`, "base_unit \"USD\" is not a unit_scale key"},
		{"no fields", `
base_unit: CNY
unit_scale:
  CNY: 1
`, "fields is empty"},
		{"field without field_code", `
base_unit: CNY
unit_scale:
  CNY: 1
fields:
  - raw_names: ["营业总收入"]
    unit: CNY
`, "has no field_code"},
		{"field without raw_names", `
base_unit: CNY
unit_scale:
  CNY: 1
fields:
  - field_code: total_revenue
    unit: CNY
`, "has no raw_names"},
		{"field without unit", `
base_unit: CNY
unit_scale:
  CNY: 1
fields:
  - field_code: total_revenue
    raw_names: ["营业总收入"]
`, "has no unit"},
		{"field unit outside unit_scale", `
base_unit: CNY
unit_scale:
  CNY: 1
fields:
  - field_code: total_revenue
    raw_names: ["营业总收入"]
    unit: USD
`, "is not a unit_scale key"},
		{"raw name claimed by two fields", `
base_unit: CNY
unit_scale:
  CNY: 1
fields:
  - field_code: total_revenue
    raw_names: ["营业收入"]
    unit: CNY
  - field_code: operating_cost
    raw_names: ["营业收入"]
    unit: CNY
`, "claimed by more than one field_code"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := ParseDictionary([]byte(tc.yaml))
			require.Error(t, err)
			assert.Nil(t, d)
			assert.Contains(t, err.Error(), tc.wants)
		})
	}
}

func TestLoadDictionary_PathErrors(t *testing.T) {
	t.Parallel()
	d, err := LoadDictionary("does-not-exist.yaml")
	require.Error(t, err)
	assert.Nil(t, d)
	assert.Contains(t, err.Error(), "read field dictionary")
}

// TestLoadDictionary_UnparseableFileIsNamed pins the other half of the error
// path: a file that exists but fails validation is reported WITH its path.
// cmd/data's startup only has a logger to work from (buildEquityDeepDictionary
// returns nil → the door answers 503), so an unlocatable failure would leave the
// outage unexplainable.
func TestLoadDictionary_UnparseableFileIsNamed(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "broken_dictionary.yaml")
	require.NoError(t, os.WriteFile(path, []byte("base_unit: CNY\nunit_scale: {}\nfields: []\n"), 0o600))

	d, err := LoadDictionary(path)
	require.Error(t, err)
	assert.Nil(t, d)
	assert.Contains(t, err.Error(), "broken_dictionary.yaml")
	assert.Contains(t, err.Error(), "unit_scale is empty")
}

// ─── the frozen contract file itself ────────────────────────────────────────

// TestFieldDictionary_ContractsFileMatchesTheFrozenCodes pins the file the
// ingest door is built on: the codes the B1 vertical factors read must exist,
// with the raw_names the producer sends. Adding a code is a contract change, so
// this test is where such a change has to be deliberate.
func TestFieldDictionary_ContractsFileMatchesTheFrozenCodes(t *testing.T) {
	t.Parallel()
	d, err := LoadDictionary(contractsDictionaryPath)
	require.NoError(t, err, "the frozen dictionary must be loadable from the package test dir")

	assert.Equal(t, 1, d.Version)
	assert.Equal(t, "frozen", d.Status)
	assert.Equal(t, "2026-09-15", d.FrozenAt)
	assert.Equal(t, "CNY", d.BaseUnit)
	assert.Equal(t, map[string]float64{
		"CNY":          1,
		"CNY_thousand": 1000,
		"CNY_10k":      10000,
		"CNY_million":  1000000,
		"CNY_100m":     1e8,
	}, d.UnitScale)
	assert.Len(t, d.Fields, 20)

	// Every field code named in equitydeep.go — i.e. every code a B1 factor
	// reads — must be present, and reachable through at least one raw_name.
	for _, want := range []struct {
		code    string
		rawName string
	}{
		{FieldTotalRevenue, "营业总收入"},
		{FieldOperatingCost, "营业成本"},
		{FieldNetProfitAttr, "归属于母公司所有者的净利润"},
		{FieldTotalAssets, "资产总计"},
		{FieldEquityAttr, "归属于母公司所有者权益合计"},
		{FieldContractLiability, "合同负债"},
		{FieldInventory, "存货"},
		{FieldOCFNet, "经营活动产生的现金流量净额"},
	} {
		f, ok := d.LookupRaw(want.rawName)
		require.Truef(t, ok, "raw_name %q must be in the frozen whitelist", want.rawName)
		assert.Equalf(t, want.code, f.FieldCode, "raw_name %q", want.rawName)
		require.Contains(t, d.FieldCodes(), want.code)
	}
}

// TestFieldDictionary_TushareFinaIndicatorNamesAreNotWhitelisted pins the other
// half of the DBT-11 finding at the contract level: the six names
// tushare.fina_indicator actually returns on the free token resolve to nothing
// here, so the deep vertical fields cannot be produced from that response.
// (The payload-side evidence is the raw row in ingest.raw; this is the mapping
// side.)
func TestFieldDictionary_TushareFinaIndicatorNamesAreNotWhitelisted(t *testing.T) {
	t.Parallel()
	d, err := LoadDictionary(contractsDictionaryPath)
	require.NoError(t, err)

	for _, rawName := range []string{"ts_code", "ann_date", "end_date", "roe", "roa", "gross_margin"} {
		_, ok := d.LookupRaw(rawName)
		assert.Falsef(t, ok, "%q must stay outside the whitelist until the contract adds it", rawName)
	}
}
