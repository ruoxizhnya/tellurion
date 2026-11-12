//go:build integration

package storage

// QT-42 X3c 的真库腿：`quant.alert_outbox` 的投递状态机。
//
// 静态护栏（alert_outbox_test.go）只能证明「DDL 写好了、挂进了 migrate() 数组」；
// 这一腿证明它在真 Postgres 上能建、能按状态机走，且 DDL 的 CHECK 真的拦得住。
// 跑法与其余 storage 集成腿一致（dockertest 一次性容器）：
//   go test -tags integration ./pkg/storage -run TestIntegration_AlertOutbox

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func qt42Row(alertID string) *AlertOutboxRecord {
	return &AlertOutboxRecord{
		AlertID:  alertID,
		RunID:    "core:alert-loop",
		OrderID:  alertID,
		EventSeq: 0,
		Symbol:   "600000.SH",
		Reason:   "drawdown",
		Payload:  []byte(`{"id":"` + alertID + `","rule":"drawdown","severity":"critical","message":"dd"}`),
		EventTs:  time.Date(2026, 10, 25, 3, 4, 5, 0, time.UTC),
	}
}

func qt42Cleanup(t *testing.T, store *PostgresStore, ids ...string) {
	t.Helper()
	_, _ = store.pool.Exec(t.Context(), `DELETE FROM quant.alert_outbox WHERE alert_id = ANY($1)`, ids)
}

func TestIntegration_AlertOutbox_SaveListMarkSent(t *testing.T) {
	store, ctx := setUp(t)
	const id = "qt42-outbox-roundtrip-1"
	t.Cleanup(func() { qt42Cleanup(t, store, id) })

	require.True(t, qt42Save(t, store, ctx, qt42Row(id)), "首次入队必须落一行")

	pending, err := store.ListPendingAlertOutbox(ctx, 100)
	require.NoError(t, err)
	var got *AlertOutboxRecord
	for i := range pending {
		if pending[i].AlertID == id {
			got = &pending[i]
		}
	}
	require.NotNil(t, got, "刚落的一行必须出现在 pending 里")
	assert.Equal(t, "core:alert-loop", got.RunID)
	assert.Equal(t, AlertOutboxStatusPending, got.Status)
	assert.Equal(t, 0, got.Attempts)
	assert.True(t, got.EventTs.Equal(qt42Row(id).EventTs), "event_ts 必须原样往返，不被墙上时钟顶掉")
	// payload 列是 JSONB：PG 会规范化空白并按 key 顺序重排后再吐回来，
	// 拿原始字符串比 `"rule":"drawdown"` 必然红。验的是「内容原样往返」，
	// 所以比解析后的结构，而不是比字符串。
	require.JSONEq(t, string(qt42Row(id).Payload), string(got.Payload),
		"payload 内容必须原样往返（JSONB 只允许空白/key 顺序变化）")

	n, err := store.MarkAlertOutboxSent(ctx, []string{id})
	require.NoError(t, err)
	assert.Equal(t, 1, n, "第一次标记应推进 1 行")

	// 再标一次不该重复推进，也不该报错（relay 轮次重叠是真实形态）。
	n, err = store.MarkAlertOutboxSent(ctx, []string{id})
	require.NoError(t, err)
	assert.Zero(t, n, "已 sent 的行不该被再算一次")

	pending, err = store.ListPendingAlertOutbox(ctx, 100)
	require.NoError(t, err)
	for i := range pending {
		require.NotEqual(t, id, pending[i].AlertID, "sent 的行不能再被 relay 取到")
	}
}

func TestIntegration_AlertOutbox_SaveIsIdempotentAndNeverRevertsSent(t *testing.T) {
	store, ctx := setUp(t)
	const id = "qt42-outbox-idempotent-1"
	t.Cleanup(func() { qt42Cleanup(t, store, id) })

	require.True(t, qt42Save(t, store, ctx, qt42Row(id)), "首次入队必须落一行")
	require.False(t, qt42Save(t, store, ctx, qt42Row(id)), "重复入队必须 no-op（返回 false，不报错）")
	_, err := store.MarkAlertOutboxSent(ctx, []string{id})
	require.NoError(t, err)

	// 同一条告警在「已发出后」又被产出（重放同一行）：DO NOTHING，
	// 不许把 sent 倒回 pending —— 否则同一批会被永久重发。
	require.False(t, qt42Save(t, store, ctx, qt42Row(id)), "重放同一行不许报错，也不许多出第二行")
	pending, err := store.ListPendingAlertOutbox(ctx, 100)
	require.NoError(t, err)
	for i := range pending {
		require.NotEqual(t, id, pending[i].AlertID, "sent 的行被重放入队后仍不该回 pending")
	}
}

func TestIntegration_AlertOutbox_AttemptsBumpThenTerminalFailed(t *testing.T) {
	store, ctx := setUp(t)
	const id = "qt42-outbox-attempts-1"
	t.Cleanup(func() { qt42Cleanup(t, store, id) })

	require.True(t, qt42Save(t, store, ctx, qt42Row(id)), "首次入队必须落一行")
	ids := []string{id}

	require.NoError(t, store.BumpAlertOutboxAttempts(ctx, ids, "status 500: boom", 3))
	row := qt42MustPending(t, store, id)
	assert.Equal(t, 1, row.Attempts)
	assert.Equal(t, AlertOutboxStatusPending, row.Status)
	assert.Contains(t, row.LastError, "boom")

	require.NoError(t, store.BumpAlertOutboxAttempts(ctx, ids, "status 500: boom", 3))
	require.NoError(t, store.BumpAlertOutboxAttempts(ctx, ids, "status 500: boom", 3))
	// 第三次触顶 ⇒ 终态 failed，且不再是 pending（relay 不再取它）。
	pending, err := store.ListPendingAlertOutbox(ctx, 100)
	require.NoError(t, err)
	for i := range pending {
		require.NotEqual(t, id, pending[i].AlertID, "触顶的行必须离开 relay 的视野")
	}
	_, _, failed, err := store.CountAlertOutboxByStatus(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, failed, 1, "failed 是终态，留在表里等人查，不删")
}

func TestIntegration_AlertOutbox_RejectsUnusableRowsAndStatuses(t *testing.T) {
	store, ctx := setUp(t)
	const id = "qt42-outbox-invalid-1"
	t.Cleanup(func() { qt42Cleanup(t, store, id) })

	bad := qt42Row(id)
	bad.Payload = []byte(`{"rule":`)
	qt42Reject(t, store, ctx, bad, "非法 JSON 必须当场拒，不许落库")

	noTS := qt42Row(id)
	noTS.EventTs = time.Time{}
	qt42Reject(t, store, ctx, noTS, "event_ts 为零时刻即拒")

	noID := qt42Row("")
	qt42Reject(t, store, ctx, noID, "空 alert_id 即拒")

	// DDL 的 CHECK 真的拦得住：绕过 Go 常量直接写一个词表外的状态。
	_, err := store.pool.Exec(ctx, `
		INSERT INTO quant.alert_outbox (alert_id, run_id, order_id, payload, event_ts, status)
		VALUES ($1,'core:alert-loop',$1,'{}'::jsonb, now(), 'dropped')`, "qt42-outbox-check-1")
	require.Error(t, err, "status CHECK 必须拒绝词表外的值（'dropped' 不在状态机里）")
	assert.True(t, strings.Contains(err.Error(), "check constraint"), err.Error())
	_, _ = store.pool.Exec(ctx, `DELETE FROM quant.alert_outbox WHERE alert_id = 'qt42-outbox-check-1'`)
}

// qt42Save 入队一行并返回「是不是新行」；断言留在调用点，免得 helper 替用例说话。
func qt42Save(t *testing.T, store *PostgresStore, ctx context.Context, rec *AlertOutboxRecord) bool {
	t.Helper()
	inserted, err := store.SaveAlertOutbox(ctx, rec)
	require.NoError(t, err)
	return inserted
}

// qt42Reject 钉「构造期就拒」：坏行既不落库，也不许返回一个看起来成功的 bool。
func qt42Reject(t *testing.T, store *PostgresStore, ctx context.Context, rec *AlertOutboxRecord, msg string) {
	t.Helper()
	inserted, err := store.SaveAlertOutbox(ctx, rec)
	require.Errorf(t, err, "%s", msg)
	require.False(t, inserted, "%s：拒了就不该有新行", msg)
}

func qt42MustPending(t *testing.T, store *PostgresStore, id string) AlertOutboxRecord {
	t.Helper()
	pending, err := store.ListPendingAlertOutbox(t.Context(), 200)
	require.NoError(t, err)
	for i := range pending {
		if pending[i].AlertID == id {
			return pending[i]
		}
	}
	t.Fatalf("行 %s 不在 pending 里（实测 %d 行）", id, len(pending))
	return AlertOutboxRecord{}
}

// QT-76 缺口①：行级领取。两个 claim 并发跑同一批 pending ⇒ 并集是全部、
// 交集为空；租约未到期时再 claim 拿不到；租约到期后重新可领。
func TestIntegration_AlertOutbox_ClaimIsRowLevelAndLeaseExpires(t *testing.T) {
	store, ctx := setUp(t)
	ids := []string{"qt76-claim-1", "qt76-claim-2", "qt76-claim-3", "qt76-claim-4", "qt76-claim-5"}
	t.Cleanup(func() { qt42Cleanup(t, store, ids...) })
	for _, id := range ids {
		require.True(t, qt42Save(t, store, ctx, qt42Row(id)))
	}

	var (
		wg   sync.WaitGroup
		a, b []AlertOutboxRecord
		errs [2]error
	)
	start := make(chan struct{})
	for i, dst := range []*[]AlertOutboxRecord{&a, &b} {
		wg.Add(1)
		go func(i int, dst *[]AlertOutboxRecord) {
			defer wg.Done()
			<-start
			*dst, errs[i] = store.ClaimPendingAlertOutbox(ctx, 100, time.Minute)
		}(i, dst)
	}
	close(start)
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}

	seen := map[string]int{}
	for _, rec := range append(append([]AlertOutboxRecord(nil), a...), b...) {
		seen[rec.AlertID]++
	}
	assert.Len(t, seen, len(ids), "两次 claim 的并集必须是全部 pending")
	for id, n := range seen {
		assert.Equal(t, 1, n, "%s 被两个 claim 同时领到 —— SKIP LOCKED / 租约没生效", id)
	}

	// 租约还没到期：谁都领不到（否则「行级」只是把竞态推到下一步）。
	again, err := store.ClaimPendingAlertOutbox(ctx, 100, time.Minute)
	require.NoError(t, err)
	assert.Empty(t, again, "租约有效期内不该再被领走")

	// 租约有效期内换一个更短的租约再领，同样领不到：`lease_until` 是
	// 「这行已被谁领到什么时候」的上界，不是「这次想领多久」——短租约
	// 不能把别人手上的行抢过来。
	stolen, err := store.ClaimPendingAlertOutbox(ctx, 100, time.Millisecond)
	require.NoError(t, err)
	assert.Empty(t, stolen, "短租约不能缩短或抢占已生效的租约")

	// 进程崩掉后的唯一出路：租约到期即可再领。把租约直接推到过去来模拟
	// 到期（而不是真 sleep 一分钟），这条腿因此与机器快慢无关。
	_, err = store.pool.Exec(ctx,
		`UPDATE quant.alert_outbox SET lease_until = now() - interval '1 second' WHERE alert_id = ANY($1)`, ids)
	require.NoError(t, err)

	reclaimed, err := store.ClaimPendingAlertOutbox(ctx, 100, time.Minute)
	require.NoError(t, err)
	assert.Len(t, reclaimed, len(ids), "租约到期后必须能再次领取")
}
