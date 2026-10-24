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

// QT-40 X1a（常量腿）：15 个 DataType* 必须是契约常量的别名。
//
// 编译期自证的是「别名指向契约符号」；这条测试守住的是回归——任何人把
// 某个常量改回本地字面量（并且契约随后改值），这里当场红。
func TestSourceDataTypeConstantsMatchContract(t *testing.T) {
	cases := []struct{ name, got, want string }{
		{"DataTypeOHLCDaily", DataTypeOHLCDaily, dataprotocol.DataTypeOHLCDaily},
		{"DataTypeOHLCMinute", DataTypeOHLCMinute, dataprotocol.DataTypeOHLCMinute},
		{"DataTypeRealtime", DataTypeRealtime, dataprotocol.DataTypeRealtime},
		{"DataTypeCapitalFlow", DataTypeCapitalFlow, dataprotocol.DataTypeCapitalFlow},
		{"DataTypeFundamental", DataTypeFundamental, dataprotocol.DataTypeFundamental},
		{"DataTypeSectors", DataTypeSectors, dataprotocol.DataTypeSectors},
		{"DataTypeStockSector", DataTypeStockSector, dataprotocol.DataTypeStockSector},
		{"DataTypeTopList", DataTypeTopList, dataprotocol.DataTypeTopList},
		{"DataTypeLimitUpPool", DataTypeLimitUpPool, dataprotocol.DataTypeLimitUpPool},
		{"DataTypeAnnounce", DataTypeAnnounce, dataprotocol.DataTypeAnnounce},
		{"DataTypeNews", DataTypeNews, dataprotocol.DataTypeNews},
		{"DataTypeHotSearch", DataTypeHotSearch, dataprotocol.DataTypeHotSearch},
		{"DataTypeGlobalOHLCV", DataTypeGlobalOHLCV, dataprotocol.DataTypeGlobalOHLCV},
		{"DataTypeMacro", DataTypeMacro, dataprotocol.DataTypeMacro},
		{"DataTypeDailyBasic", DataTypeDailyBasic, dataprotocol.DataTypeDailyBasic},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want contract value %q", tc.name, tc.got, tc.want)
		}
	}
}
