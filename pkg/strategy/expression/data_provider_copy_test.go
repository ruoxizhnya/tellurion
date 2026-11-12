package expression

import (
	"testing"

	"tellurion/pkg/domain"
)

// 护栏：OHLCVDataProvider.GetSymbols 返回副本，改返回值不能改到 provider
// 的排序/内部状态。
func TestOHLCVDataProvider_GetSymbolsReturnsCopy(t *testing.T) {
	p := NewOHLCVDataProvider(map[string][]domain.OHLCV{
		"600000.SH": nil,
		"000001.SZ": nil,
	})

	syms := p.GetSymbols()
	if len(syms) != 2 {
		t.Fatalf("want 2 symbols, got %d", len(syms))
	}
	syms[0] = "TAMPERED"
	syms = append(syms, "INJECTED")

	again := p.GetSymbols()
	if again[0] != "000001.SZ" {
		t.Fatalf("改 GetSymbols 返回值不能改到 provider，got %q", again[0])
	}
	if len(again) != 2 {
		t.Fatalf("append 不能改到 provider，got len=%d", len(again))
	}
}
