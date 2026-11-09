// CT-06-B (2026-11-10): 随形状从 `tellurion-contracts:domain/types_test.go`
// 迁回的测试；断言内容逐字保留，仅包与文件位置变化。
package domain

import (
	"encoding/json"
	"testing"
	"time"

	cdom "github.com/ruoxizhnya/tellurion-contracts/domain"
)

func TestOrderType_Constants(t *testing.T) {
	tests := []struct {
		name     string
		value    OrderType
		expected string
	}{
		{"market", OrderTypeMarket, "market"},
		{"limit", OrderTypeLimit, "limit"},
		{"stop", OrderTypeStop, "stop"},
		{"trailing", OrderTypeTrailing, "trailing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.value) != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, string(tt.value))
			}
		})
	}
}

func TestSignal_ZeroValues(t *testing.T) {
	var s Signal
	if s.Symbol != "" {
		t.Errorf("expected empty Symbol, got %q", s.Symbol)
	}
	if !s.Date.IsZero() {
		t.Error("expected zero Date")
	}
	if s.Direction != "" {
		t.Errorf("expected empty Direction, got %q", s.Direction)
	}
	if s.Strength != 0 || s.CompositeScore != 0 {
		t.Error("expected zero strength/score")
	}
	if s.Factors != nil {
		t.Error("expected nil Factors")
	}
	if s.Metadata != nil {
		t.Error("expected nil Metadata")
	}
	if s.LimitPrice != 0 {
		t.Error("expected zero LimitPrice")
	}
	if s.OrderType != "" {
		t.Errorf("expected empty OrderType, got %q", s.OrderType)
	}
}

func TestSignal_JSONRoundTrip(t *testing.T) {
	original := Signal{
		Symbol:         "000001.SZ",
		Date:           time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC),
		Direction:      cdom.DirectionLong,
		Strength:       0.85,
		CompositeScore: 0.72,
		Factors:        map[string]float64{"momentum": 0.8, "value": 0.6},
		Metadata:       map[string]any{"source": "test"},
		LimitPrice:     10.5,
		OrderType:      OrderTypeLimit,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded Signal
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.Symbol != original.Symbol {
		t.Errorf("Symbol mismatch")
	}
	if decoded.Direction != original.Direction {
		t.Errorf("Direction mismatch")
	}
	if decoded.Strength != original.Strength {
		t.Errorf("Strength mismatch")
	}
	if decoded.Factors["momentum"] != original.Factors["momentum"] {
		t.Errorf("Factors mismatch")
	}
	if decoded.OrderType != original.OrderType {
		t.Errorf("OrderType mismatch")
	}
}

func TestPosition_ZeroValues(t *testing.T) {
	var p Position
	if p.Symbol != "" {
		t.Errorf("expected empty Symbol")
	}
	if p.Quantity != 0 || p.AvgCost != 0 || p.CurrentPrice != 0 {
		t.Error("expected zero numeric fields")
	}
	if p.MarketValue != 0 || p.UnrealizedPnL != 0 || p.RealizedPnL != 0 {
		t.Error("expected zero PnL fields")
	}
	if p.Weight != 0 {
		t.Error("expected zero Weight")
	}
	if !p.EntryDate.IsZero() {
		t.Error("expected zero EntryDate")
	}
	if !p.BuyDate.IsZero() {
		t.Error("expected zero BuyDate")
	}
	if p.QuantityToday != 0 || p.QuantityYesterday != 0 {
		t.Error("expected zero T+1 fields")
	}
}

func TestPosition_JSONRoundTrip(t *testing.T) {
	original := Position{
		Symbol:            "000001.SZ",
		Quantity:          1000,
		AvgCost:           10.5,
		CurrentPrice:      11.0,
		MarketValue:       11000,
		UnrealizedPnL:     500,
		Weight:            0.15,
		EntryDate:         time.Date(2024, 1, 10, 0, 0, 0, 0, time.UTC),
		BuyDate:           time.Date(2024, 1, 10, 0, 0, 0, 0, time.UTC),
		QuantityToday:     500,
		QuantityYesterday: 500,
		Metadata:          map[string]any{"trailing_high": 11.5},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded Position
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.Symbol != original.Symbol {
		t.Errorf("Symbol mismatch")
	}
	if decoded.Quantity != original.Quantity {
		t.Errorf("Quantity mismatch")
	}
	if decoded.QuantityToday != original.QuantityToday {
		t.Errorf("QuantityToday mismatch")
	}
}

func TestPosition_MetadataOmitEmpty(t *testing.T) {
	p := Position{Symbol: "TEST", Quantity: 100}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	// Metadata has omitempty, so it should not appear in JSON when nil
	if contains(string(data), `"metadata"`) {
		t.Errorf("expected metadata to be omitted when nil, got: %s", string(data))
	}
}

func TestPortfolio_ZeroValues(t *testing.T) {
	var p Portfolio
	if p.Cash != 0 {
		t.Error("expected zero Cash")
	}
	if p.Positions != nil {
		t.Error("expected nil Positions")
	}
	if p.TotalValue != 0 {
		t.Error("expected zero TotalValue")
	}
	if p.DailyReturn != 0 {
		t.Error("expected zero DailyReturn")
	}
	if !p.UpdatedAt.IsZero() {
		t.Error("expected zero UpdatedAt")
	}
}

func TestPortfolio_JSONRoundTrip(t *testing.T) {
	original := Portfolio{
		Cash:        100000,
		Positions:   map[string]Position{"000001.SZ": {Symbol: "000001.SZ", Quantity: 100}},
		TotalValue:  101000,
		DailyReturn: 0.01,
		UpdatedAt:   time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC),
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded Portfolio
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.Cash != original.Cash {
		t.Errorf("Cash mismatch")
	}
	if decoded.TotalValue != original.TotalValue {
		t.Errorf("TotalValue mismatch")
	}
	if len(decoded.Positions) != 1 {
		t.Errorf("expected 1 position, got %d", len(decoded.Positions))
	}
}

func TestPositionSize_ZeroValues(t *testing.T) {
	var ps PositionSize
	if ps.Size != 0 || ps.Weight != 0 || ps.StopLoss != 0 || ps.TakeProfit != 0 || ps.RiskScore != 0 {
		t.Error("expected all zero values")
	}
}

func TestPositionSize_JSONRoundTrip(t *testing.T) {
	original := PositionSize{
		Size:       1000,
		Weight:     0.15,
		StopLoss:   9.5,
		TakeProfit: 12.0,
		RiskScore:  0.3,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded PositionSize
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.Size != original.Size {
		t.Errorf("Size mismatch")
	}
	if decoded.Weight != original.Weight {
		t.Errorf("Weight mismatch")
	}
}

func TestStopLossEvent_ZeroValues(t *testing.T) {
	var e StopLossEvent
	if e.Symbol != "" {
		t.Error("expected empty Symbol")
	}
	if e.Type != "" {
		t.Error("expected empty Type")
	}
	if e.Price != 0 || e.Quantity != 0 {
		t.Error("expected zero Price/Quantity")
	}
	if e.Reason != "" {
		t.Error("expected empty Reason")
	}
}

func TestStopLossEvent_JSONRoundTrip(t *testing.T) {
	original := StopLossEvent{
		Symbol:   "000001.SZ",
		Type:     "stop_loss",
		Price:    9.5,
		Quantity: 1000,
		Reason:   "ATR-based stop loss triggered",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded StopLossEvent
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.Symbol != original.Symbol {
		t.Errorf("Symbol mismatch")
	}
	if decoded.Type != original.Type {
		t.Errorf("Type mismatch")
	}
}

func TestSignal_EmptyFactors(t *testing.T) {
	s := Signal{
		Symbol:    "TEST",
		Direction: cdom.DirectionLong,
		Factors:   map[string]float64{},
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	var decoded Signal
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if decoded.Factors == nil {
		t.Error("expected non-nil empty Factors map")
	}
	if len(decoded.Factors) != 0 {
		t.Errorf("expected 0 factors, got %d", len(decoded.Factors))
	}
}

func TestPortfolio_EmptyPositions(t *testing.T) {
	p := Portfolio{
		Cash:       100000,
		Positions:  map[string]Position{},
		TotalValue: 100000,
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	var decoded Portfolio
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if decoded.Positions == nil {
		t.Error("expected non-nil empty Positions map")
	}
	if len(decoded.Positions) != 0 {
		t.Errorf("expected 0 positions, got %d", len(decoded.Positions))
	}
}

func TestRiskMetrics_ZeroValues(t *testing.T) {
	var rm RiskMetrics
	if rm.Volatility != 0 || rm.Beta != 0 || rm.SharpeRatio != 0 {
		t.Error("expected zero values")
	}
	if rm.MaxDrawdown != 0 || rm.VaR95 != 0 || rm.CVaR95 != 0 {
		t.Error("expected zero risk values")
	}
}

func TestRiskMetrics_JSONRoundTrip(t *testing.T) {
	original := RiskMetrics{
		Volatility:   0.15,
		Beta:         1.2,
		SharpeRatio:  1.5,
		SortinoRatio: 2.0,
		MaxDrawdown:  -0.2,
		VaR95:        -0.03,
		CVaR95:       -0.04,
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	var decoded RiskMetrics
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if decoded.SharpeRatio != original.SharpeRatio {
		t.Errorf("SharpeRatio mismatch")
	}
}

func TestOrder_JSONRoundTrip(t *testing.T) {
	original := Order{
		ID:         "order-1",
		Symbol:     "000001.SZ",
		Direction:  cdom.DirectionLong,
		OrderType:  OrderTypeLimit,
		Quantity:   1000,
		LimitPrice: 10.5,
		Timestamp:  time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
		Status:     "filled",
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	var decoded Order
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if decoded.Symbol != original.Symbol {
		t.Errorf("Symbol mismatch")
	}
	if decoded.OrderType != original.OrderType {
		t.Errorf("OrderType mismatch")
	}
}

func TestOrder_OptionalFieldsOmitEmpty(t *testing.T) {
	o := Order{ID: "test", Symbol: "TEST", Direction: cdom.DirectionLong, OrderType: OrderTypeMarket}
	data, err := json.Marshal(o)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	// StopPrice, TrailAmount, TrailPercent, HighWaterMark have omitempty
	jsonStr := string(data)
	for _, field := range []string{`"stop_price"`, `"trail_amount"`, `"trail_percent"`, `"high_water_mark"`} {
		if contains(jsonStr, field) {
			t.Errorf("expected %s to be omitted, got: %s", field, jsonStr)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
