package strategy

import (
	"testing"
	"time"

	"tellurion/pkg/domain"
)

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func valuationOn(tsCode string, date time.Time, pe float64) domain.DailyBasicValuation {
	return domain.DailyBasicValuation{
		TsCode:    tsCode,
		TradeDate: date,
		PE:        &pe,
		PESource:  domain.DailyBasicPESourceTTM,
	}
}

// TestNewValuationReaderPITBoundary 锁定 PIT 口径：读取器只返回
// `trade_date <= 决策日` 的最近一条，决策日之后的估值行永不参与。
func TestNewValuationReaderPITBoundary(t *testing.T) {
	snapshot := map[string][]domain.DailyBasicValuation{
		"A": {
			valuationOn("A", day(2024, 1, 2), 10),
			valuationOn("A", day(2024, 1, 5), 12),
			valuationOn("A", day(2024, 1, 10), 99), // 未来值：永远不该被读到
		},
		"B": {
			valuationOn("B", day(2024, 1, 8), 20), // 决策日之前没有任何行
		},
	}

	tests := []struct {
		name    string
		asOf    time.Time
		wantA   bool
		wantAPE float64
		wantB   bool
	}{
		{"before first row", day(2024, 1, 1), false, 0, false},
		{"exactly first row", day(2024, 1, 2), true, 10, false},
		{"between rows picks earlier", day(2024, 1, 4), true, 10, false},
		{"exactly second row", day(2024, 1, 5), true, 12, false},
		{"after last row does not use future", day(2024, 1, 9), true, 12, true},
		{"same day as future row", day(2024, 1, 10), true, 99, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reader := NewValuationReader(snapshot, tc.asOf)
			got := reader([]string{"A", "B"})

			a, ok := got["A"]
			if ok != tc.wantA {
				t.Fatalf("A presence: got %v, want %v", ok, tc.wantA)
			}
			if ok && a.PE != nil && *a.PE != tc.wantAPE {
				t.Errorf("A PE: got %v, want %v", *a.PE, tc.wantAPE)
			}

			b, ok := got["B"]
			if ok != tc.wantB {
				t.Fatalf("B presence: got %v, want %v", ok, tc.wantB)
			}
			if ok && b.TradeDate.After(tc.asOf) {
				t.Errorf("B must never expose a row after asOf: got %v > %v", b.TradeDate, tc.asOf)
			}
		})
	}
}

// TestNewValuationReaderNullRowStaysDistinguishable 证明「有行但 PE 为 NULL」
// 会出现在返回 map 里（可与「截至该日没有行」区分）。
func TestNewValuationReaderNullRowStaysDistinguishable(t *testing.T) {
	snapshot := map[string][]domain.DailyBasicValuation{
		"A": {{TsCode: "A", TradeDate: day(2024, 1, 2)}}, // PE/PB 全 NULL
	}
	reader := NewValuationReader(snapshot, day(2024, 1, 3))
	got := reader([]string{"A", "B"})

	if _, ok := got["B"]; ok {
		t.Errorf("B has no rows and must be absent from the snapshot map")
	}
	a, ok := got["A"]
	if !ok {
		t.Fatalf("A has a row on/before asOf and must be present even with NULL PE")
	}
	if a.HasComparablePE() || a.HasComparablePB() {
		t.Errorf("A's NULL valuation must not be reported as comparable")
	}
}
