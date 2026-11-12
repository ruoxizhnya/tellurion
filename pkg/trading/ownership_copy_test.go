package trading

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/domain"
)

// 护栏：OrderManager 的 getter 返回深拷贝。改返回的 ExecProfile 不能写回订单簿。
func TestOrderManager_GettersCopyExecProfile(t *testing.T) {
	om := NewOrderManager(nil, domain.ExecutionConfig{})
	om.orders["o1"] = domain.Order{ID: "o1", ExecProfile: []float64{10, 10.1}}
	om.pending = append(om.pending, "o1")

	order, ok := om.GetOrder("o1")
	require.True(t, ok)
	order.ExecProfile[0] = 999
	order.ExecProfile = append(order.ExecProfile, 123)

	again, ok := om.GetOrder("o1")
	require.True(t, ok)
	assert.Equal(t, []float64{10, 10.1}, again.ExecProfile, "改返回的 ExecProfile 不能写回订单簿")

	orders := om.GetOrders()
	require.Len(t, orders, 1)
	orders[0].ExecProfile[0] = 999
	assert.Equal(t, float64(10), om.orders["o1"].ExecProfile[0])

	pending := om.GetPendingOrders()
	require.Len(t, pending, 1)
	pending[0].ExecProfile[0] = 999
	assert.Equal(t, float64(10), om.orders["o1"].ExecProfile[0])
}

// 护栏：PositionManager 的 getter 返回深拷贝。改返回的 Metadata 不能写回持仓表。
func TestPositionManager_GettersCopyMetadata(t *testing.T) {
	pm := NewPositionManager()
	pm.positions["600000.SH"] = domain.Position{
		Symbol:   "600000.SH",
		Metadata: map[string]any{"source": "seed"},
	}

	pos, ok := pm.GetPosition("600000.SH")
	require.True(t, ok)
	pos.Metadata["source"] = "tampered"
	pos.Metadata["new"] = true

	again, ok := pm.GetPosition("600000.SH")
	require.True(t, ok)
	assert.Equal(t, map[string]any{"source": "seed"}, again.Metadata, "改返回的 Metadata 不能写回持仓表")

	all := pm.GetPositions()
	require.Len(t, all, 1)
	all[0].Metadata["source"] = "tampered-2"
	assert.Equal(t, "seed", pm.positions["600000.SH"].Metadata["source"])
}
