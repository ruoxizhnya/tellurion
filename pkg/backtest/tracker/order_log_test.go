package tracker

import (
	"testing"
	"time"

	"github.com/rs/zerolog"
	cdom "github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/ruoxizhnya/tellurion-contracts/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

import "tellurion/pkg/domain"

func TestOrderLog_RecordAndGetOrders(t *testing.T) {
	log := &OrderLog{}

	orders := log.GetOrders()
	assert.Empty(t, orders)

	o1 := domain.Order{
		Symbol:    "600000.SH",
		Direction: cdom.DirectionLong,
		Quantity:  100,
		FillPrice: 10.0,
		Timestamp: time.Now(),
	}
	log.Record(o1)

	o2 := domain.Order{
		Symbol:    "600001.SH",
		Direction: cdom.DirectionClose,
		Quantity:  200,
		FillPrice: 20.0,
		Timestamp: time.Now(),
	}
	log.Record(o2)

	all := log.GetOrders()
	assert.Len(t, all, 2)
	assert.Equal(t, "600000.SH", all[0].Symbol)
	assert.Equal(t, "600001.SH", all[1].Symbol)
}

func TestOrderLog_Empty(t *testing.T) {
	log := &OrderLog{}
	orders := log.GetOrders()
	assert.Nil(t, orders)
}

// 护栏：GetOrders 必须返回深拷贝。改返回的 slice / 元素 / ExecProfile 都
// 不能影响 OrderLog 内部记录。
func TestOrderLog_GetOrdersReturnsDeepCopy(t *testing.T) {
	log := &OrderLog{}
	log.Record(domain.Order{
		Symbol:      "600000.SH",
		Quantity:    100,
		FillPrice:   10,
		ExecProfile: []float64{10, 10.1},
		Timestamp:   time.Now(),
	})

	got := log.GetOrders()
	require.Len(t, got, 1)
	got[0].Symbol = "TAMPERED"
	got[0].ExecProfile[0] = 999
	got[0].ExecProfile = append(got[0].ExecProfile, 123)

	again := log.GetOrders()
	require.Len(t, again, 1)
	assert.Equal(t, "600000.SH", again[0].Symbol, "改返回元素不能改到内部记录")
	assert.Equal(t, []float64{10, 10.1}, again[0].ExecProfile, "ExecProfile 必须是独立切片")
}

// 护栏：Tracker.GetPosition / GetAllPositions 返回的 Metadata 是独立 map。
func TestTracker_GettersCopyMetadata(t *testing.T) {
	trk := NewTracker(1_000_000, 0.0003, 0, contracts.TradingConfig{}, zerolog.Nop())
	trk.positions["600000.SH"] = &domain.Position{
		Symbol:   "600000.SH",
		Quantity: 100,
		Metadata: map[string]any{"source": "seed"},
	}

	pos, ok := trk.GetPosition("600000.SH")
	require.True(t, ok)
	pos.Metadata["source"] = "tampered"
	pos.Metadata["new"] = true

	again, ok := trk.GetPosition("600000.SH")
	require.True(t, ok)
	assert.Equal(t, map[string]any{"source": "seed"}, again.Metadata, "改返回的 Metadata 不能写回 tracker")

	all := trk.GetAllPositions()
	all["600000.SH"].Metadata["source"] = "tampered-2"
	againAll := trk.GetAllPositions()
	assert.Equal(t, "seed", againAll["600000.SH"].Metadata["source"])
}
