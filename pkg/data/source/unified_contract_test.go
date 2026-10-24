package source

import (
	"reflect"
	"testing"

	"github.com/ruoxizhnya/tellurion-contracts/dataprotocol"
)

// QT-40 X1a：ETL 侧不再持有点位副本，类型必须就是契约那个类型。
func TestSourceUnifiedDataPointIsContractType(t *testing.T) {
	got := reflect.TypeOf(UnifiedDataPoint{})
	want := reflect.TypeOf(dataprotocol.UnifiedDataPoint{})
	if got != want {
		t.Fatalf("source.UnifiedDataPoint = %v, want the contract type %v", got, want)
	}
}
