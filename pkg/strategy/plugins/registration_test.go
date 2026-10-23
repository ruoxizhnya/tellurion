package plugins

// QT-11 · 内置策略注册形态的清点。
//
// 任务单验收 ⑤ 的「14 个插件全部 NewInstance 成功」落在这里：注册表存的是
// 构造函数，执行路径（LocalSignalProvider → Registry.NewInstance）每笔 run
// 各拿一份。漏迁移某个插件不会编译失败，只会在跑到它时报错——所以这条清点
// 必须逐个点名，而不是「List 非空」。
//
// 破坏验证：把任意一个插件的 init 改回 registerFactory 之外
// （strategy.GlobalRegister(s)）→ 该名字在 NewInstance 处红。

import (
	"testing"

	"github.com/stretchr/testify/require"
	"tellurion/pkg/strategy"
)

// builtinNames 是本包注册的全部内置策略（14 个，与 docs/TASKS.md QT-11 ③-②
// 的口径一一对应；新增策略必须同时加进这张表，否则下面的集合断言会红）。
var builtinNames = []string{
	"convertible_bond",
	"event_driven",
	"mean_reversion",
	"momentum",
	"multi_factor",
	"quality",
	"risk_parity",
	"sentiment",
	"td_sequential",
	"bollinger_mr",
	"volume_price_trend",
	"volatility_breakout",
	"value",
	"value_screening",
}

func TestBuiltInStrategiesAreFactoryRegistered(t *testing.T) {
	require.Len(t, builtinNames, 14, "QT-11 的清点口径就是 14 个注册点")

	for _, name := range builtinNames {
		name := name
		t.Run(name, func(t *testing.T) {
			first, err := strategy.GlobalNewInstance(name)
			require.NoError(t, err,
				"%s 拿不到 per-run 实例——它的注册方式还没迁到构造函数（NewInstance 对共享实例 fail-closed）", name)
			require.Equal(t, name, first.Name())

			second, err := strategy.GlobalNewInstance(name)
			require.NoError(t, err)
			require.NotSame(t, first, second,
				"%s 两次 NewInstance 拿到同一份 = 注册的是实例，不是工厂", name)

			// 元数据面（Parameters/Info）仍能查——QT-11 ④ 明令不动它。
			info, err := strategy.GlobalDescribe(name)
			require.NoError(t, err)
			require.Equal(t, name, info.Name)
			require.NotEmpty(t, info.Description, "%s 的元数据必须还在", name)
		})
	}

	require.ElementsMatch(t, builtinNames, strategy.GlobalList(),
		"注册表内容与清点表不一致：要么丢了某个注册点，要么多了一个没登记的")
}

// TestBuiltInInstancesAreNotTheRegistrationPrototype 钉住工厂注册的一个
// 隐蔽失败模式：工厂返回注册期那一份原型（`return proto`），于是
// NewInstance「成功」了但仍然是共享实例。
func TestBuiltInInstancesAreNotTheRegistrationPrototype(t *testing.T) {
	for _, name := range builtinNames {
		proto, err := strategy.GlobalGet(name)
		require.NoError(t, err, "%s 元数据读取失败", name)

		got, err := strategy.GlobalNewInstance(name)
		require.NoError(t, err)
		require.NotSame(t, proto, got,
			"%s 的工厂把注册期原型递了出来 —— 表面是工厂、实质还是共享实例", name)
	}
}
