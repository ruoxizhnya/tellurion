package storage

import (
	"reflect"
	"testing"

	"github.com/ruoxizhnya/tellurion-contracts/dataprotocol"
)

// QT-40 X1a：存储侧不再另抄一份点位结构（旧写法是为避开 source → storage
// 的 import 环）。类型必须就是契约那个类型——谁把它改回本地 struct，这条当场红。
func TestStorageUnifiedDataPointIsContractType(t *testing.T) {
	got := reflect.TypeOf(UnifiedDataPoint{})
	want := reflect.TypeOf(dataprotocol.UnifiedDataPoint{})
	if got != want {
		t.Fatalf("storage.UnifiedDataPoint = %v, want the contract type %v", got, want)
	}
}
