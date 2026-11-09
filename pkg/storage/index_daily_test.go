package storage

// DATA-12 的库侧腿：market.index_daily 的写入面（SaveIndexDailyBatch）与区间读面
// （GetIndexDailyRange）。工单 ⑤ 点名要的证据是「幂等重跑无重复行」——
// 这里钉死它，并顺带钉「重导把取值刷新」与「缺失身份列 fail-loud」。

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"tellurion/pkg/domain"
)

// TestSaveIndexDailyBatch_IdempotentUpsert 钉「同 (code, trade_date) 重跑不增行、
// 且取值被刷新」：唯一键就是幂等键，ON CONFLICT DO UPDATE 是唯一真相。
func TestSaveIndexDailyBatch_IdempotentUpsert(t *testing.T) {
	store := testStore(t)
	t.Cleanup(store.Close)
	ctx := context.Background()

	// 本次运行独有的假指数代码（D38… 形状撞不上真指数，也撞不上别的探针）。
	code := fmt.Sprintf("D38%08d.SH", time.Now().UnixNano()%100000000)
	t.Cleanup(func() {
		if _, err := store.DB().Exec(context.Background(),
			`DELETE FROM market.index_daily WHERE code=$1`, code); err != nil {
			t.Errorf("cleanup probe %s failed: %v", code, err)
		}
	})

	day := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	vol := 15196578700.0 // 规范层口径：**股**（源侧 151965787 手 ×100）
	amt := 375461676142.8
	require.NoError(t, store.SaveIndexDailyBatch(ctx, []*domain.IndexDaily{{
		Code: code, TradeDate: day, Close: 4530.0, Volume: vol, Turnover: &amt,
		Source: "test", AsOf: time.Now().UTC(),
	}}))

	// 重跑：同键、改 close，必须原地刷新而不是插第二行。
	require.NoError(t, store.SaveIndexDailyBatch(ctx, []*domain.IndexDaily{{
		Code: code, TradeDate: day, Close: 4540.0, Volume: vol, Turnover: &amt,
		Source: "test", AsOf: time.Now().UTC(),
	}}))

	var n int
	require.NoError(t, store.DB().QueryRow(ctx,
		`SELECT count(*) FROM market.index_daily WHERE code=$1`, code).Scan(&n))
	assert.Equal(t, 1, n, "同键重导必须原地刷新，不得插重复行")

	got, err := store.GetIndexDailyRange(ctx, code, day, day)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, 4540.0, got[0].Close, "重导必须把取值刷成新值")
	assert.Equal(t, vol, got[0].Volume)
	require.NotNil(t, got[0].Turnover)
	assert.InDelta(t, amt, *got[0].Turnover, 1e-6)
}

// TestSaveIndexDailyBatch_RejectsMissingIdentity 钉「缺失身份列 fail-loud」：
// 空 code 在落库前就被拒，不写半行、不静默成功。
func TestSaveIndexDailyBatch_RejectsMissingIdentity(t *testing.T) {
	store := testStore(t)
	t.Cleanup(store.Close)

	err := store.SaveIndexDailyBatch(context.Background(), []*domain.IndexDaily{{
		Code: "", TradeDate: time.Now().UTC(), Close: 1, Volume: 1, Source: "test", AsOf: time.Now().UTC(),
	}})
	require.Error(t, err, "缺 code 必须拒绝（不落半行）")
}
