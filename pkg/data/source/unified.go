package source

import (
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/dataprotocol"
)

// UnifiedDataPoint is the shared wire shape defined in
// `tellurion-contracts:dataprotocol` (QT-40 X1a, contract v0.6.0).
//
// The field-for-field local copy is gone: the seven fields (including
// VisibleAt), ToJSON and DeduplicateKey all come from the contract.
// Only the clock-reading constructor stays here — the contract
// deliberately has none (value objects do not read the clock, ADR-0004),
// so callers must keep injecting IngestTime.
type UnifiedDataPoint = dataprotocol.UnifiedDataPoint

// NewUnifiedDataPoint constructs a point with IngestTime set to now.
//
// This constructor is intentionally core-side: the contract must stay
// deterministic (same input → same bytes), so reading the wall clock is
// the caller's job.
func NewUnifiedDataPoint(symbol, source, dataType string, tradeTime time.Time, data map[string]interface{}) UnifiedDataPoint {
	return UnifiedDataPoint{
		Symbol:     symbol,
		TradeTime:  tradeTime,
		Source:     source,
		DataType:   dataType,
		Data:       data,
		IngestTime: time.Now().UTC(),
	}
}

// Deduplicate drops duplicate points in a slice, keeping the first
// occurrence (which callers can control by ordering the input).
//
// This is a simple O(n) implementation; for very large batches a
// sorted-input variant would be faster.
func Deduplicate(points []UnifiedDataPoint) []UnifiedDataPoint {
	out, _ := DeduplicateWithCount(points)
	return out
}

// DeduplicateWithCount is like Deduplicate but also returns the number
// of points dropped. The etl pipeline uses this to attribute skips
// to the deduplication stage.
//
// The key itself lives in the contract (UnifiedDataPoint.DeduplicateKey):
// Symbol | DataType | TradeTime, UTC/RFC3339Nano, Source excluded so a
// multi-source fallback chain keeps one logical point.
func DeduplicateWithCount(points []UnifiedDataPoint) ([]UnifiedDataPoint, int) {
	seen := make(map[string]struct{}, len(points))
	out := make([]UnifiedDataPoint, 0, len(points))
	skipped := 0
	for _, p := range points {
		k := p.DeduplicateKey()
		if _, ok := seen[k]; ok {
			skipped++
			continue
		}
		seen[k] = struct{}{}
		out = append(out, p)
	}
	return out, skipped
}
