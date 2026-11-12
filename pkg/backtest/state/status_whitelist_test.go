package state

import (
	"testing"

	"github.com/ruoxizhnya/tellurion-contracts/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSetStatus_AcceptsWhitelist 覆盖全部合法取值：运行态 + 契约终态三值。
func TestSetStatus_AcceptsWhitelist(t *testing.T) {
	legal := []string{
		StatusRunning,
		contracts.StatusCompleted,
		contracts.StatusFailed,
		contracts.StatusInvalid,
	}
	for _, status := range legal {
		t.Run(status, func(t *testing.T) {
			s := &BacktestState{ID: "bt-legal"}
			prev, ok, err := s.SetStatus(status)
			require.NoError(t, err)
			assert.True(t, ok)
			assert.Equal(t, "", prev)
			assert.Equal(t, status, s.GetStatus())
		})
	}
}

// TestSetStatus_RejectsUnknown 验证 fail-closed：未知值不写入、返回
// ErrInvalidStatus，且状态保持原值（不是 normalize 成某个合法值）。
func TestSetStatus_RejectsUnknown(t *testing.T) {
	unknown := []string{"", "RUNNING", "completed ", "pending", "ok"}
	for _, status := range unknown {
		t.Run(status, func(t *testing.T) {
			s := &BacktestState{ID: "bt-unknown"}
			require.NoError(t, mustSet(t, s, StatusRunning))

			prev, ok, err := s.SetStatus(status)
			require.ErrorIs(t, err, ErrInvalidStatus)
			assert.False(t, ok)
			assert.Equal(t, "", prev)
			assert.Equal(t, StatusRunning, s.GetStatus(), "非法值不得改变状态")
		})
	}
}

// TestSetStatus_InvalidIsTerminal 验证 invalid 是合法终态：可正常写入，
// Freeze 后与 completed/failed 一样不可再变。
func TestSetStatus_InvalidIsTerminal(t *testing.T) {
	s := &BacktestState{ID: "bt-invalid"}
	_, ok, err := s.SetStatus(StatusRunning)
	require.NoError(t, err)
	require.True(t, ok)

	prev, ok, err := s.SetStatus(contracts.StatusInvalid)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, StatusRunning, prev)
	assert.Equal(t, contracts.StatusInvalid, s.GetStatus())

	s.Freeze()
	assert.True(t, s.IsCompleted())

	prev, ok, err = s.SetStatus(contracts.StatusCompleted)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, contracts.StatusInvalid, prev, "frozen 后状态不得改变")
	assert.Equal(t, contracts.StatusInvalid, s.GetStatus())
}

func mustSet(t *testing.T, s *BacktestState, status string) error {
	t.Helper()
	_, _, err := s.SetStatus(status)
	return err
}
