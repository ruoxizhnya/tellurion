package mining

import (
	"testing"
)

// 护栏：snapshotProvider 的 GetSymbols / Fields 返回副本，改返回值不能
// 影响 provider 内部状态。
func TestSnapshotProvider_SymbolsAndFieldsReturnCopies(t *testing.T) {
	p := &snapshotProvider{
		symbols: []string{"600000.SH", "000001.SZ"},
		fields:  []string{"close", "volume"},
	}

	syms := p.GetSymbols()
	syms[0] = "TAMPERED"
	syms = append(syms, "INJECTED")
	if p.symbols[0] != "600000.SH" {
		t.Fatalf("改 GetSymbols 返回值不能改到 provider，got %q", p.symbols[0])
	}
	if len(p.symbols) != 2 {
		t.Fatalf("append 不能改到 provider，got len=%d", len(p.symbols))
	}

	fields := p.Fields()
	fields[0] = "TAMPERED"
	if p.fields[0] != "close" {
		t.Fatalf("改 Fields 返回值不能改到 provider，got %q", p.fields[0])
	}
}
