package storage

import (
	"context"
	"testing"
	"time"

	cdom "github.com/ruoxizhnya/tellurion-contracts/domain"

	"tellurion/pkg/domain"
	"tellurion/pkg/testenv"
)

// B-05：factor_returns / ic_analysis 的写者（ON CONFLICT 幂等）与读面
// （GetFactorReturns / GetICEntries / ListFactorAnalyticsSummaries）在真库
// 上的闭环。DSN 契约与仓内其他存储测试一致（REV-21）：
// 未设置 → 登记跳过；已设置但连不上 → fail-loud。
const factorAnalyticsTestFactor = cdom.FactorType("b05_test_factor")

func newFactorAnalyticsTestStore(t *testing.T) *PostgresStore {
	t.Helper()
	dsn := testenv.DatabaseDSN()
	if dsn == "" {
		testenv.SkipNoDB(t)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := NewPostgresStoreConnected(ctx, dsn)
	if err != nil {
		t.Fatalf("[testdb] TEST_DATABASE_DSN 已设置但连不上（fail-loud）: %v", err)
	}
	cleanup := func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = store.pool.Exec(cleanupCtx,
			`DELETE FROM factor_returns WHERE factor_name = $1`, factorAnalyticsTestFactor)
		_, _ = store.pool.Exec(cleanupCtx,
			`DELETE FROM ic_analysis WHERE factor_name = $1`, factorAnalyticsTestFactor)
	}
	// 前置清场：上次运行可能留尾；表不存在则跳过（需先跑迁移）。
	if _, err := store.pool.Exec(ctx, `DELETE FROM factor_returns WHERE factor_name = $1`, factorAnalyticsTestFactor); err != nil {
		t.Skipf("factor_returns table unavailable (run migrations first): %v", err)
	}
	if _, err := store.pool.Exec(ctx, `DELETE FROM ic_analysis WHERE factor_name = $1`, factorAnalyticsTestFactor); err != nil {
		t.Skipf("ic_analysis table unavailable (run migrations first): %v", err)
	}
	t.Cleanup(cleanup)
	return store
}

func TestFactorAnalytics_WriteReadRoundTripAndSummary(t *testing.T) {
	store := newFactorAnalyticsTestStore(t)
	ctx := context.Background()

	day1 := time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 2, 9, 0, 0, 0, 0, time.UTC)

	var returns []*domain.FactorReturn
	for _, d := range []time.Time{day1, day2} {
		for q := 1; q <= 5; q++ {
			returns = append(returns, &domain.FactorReturn{
				FactorName:       factorAnalyticsTestFactor,
				TradeDate:        d,
				Quintile:         q,
				AvgReturn:        0.001 * float64(q),
				CumulativeReturn: 0.002 * float64(q),
				TopMinusBot:      0.004,
			})
		}
	}
	ics := []*domain.ICEntry{
		{FactorName: factorAnalyticsTestFactor, TradeDate: day1, IC: 0.2, PValue: 0.05, TopIC: 0.25},
		{FactorName: factorAnalyticsTestFactor, TradeDate: day2, IC: 0.4, PValue: 0.01, TopIC: 0.35},
	}
	if err := store.SaveFactorReturnBatch(ctx, returns); err != nil {
		t.Fatalf("SaveFactorReturnBatch: %v", err)
	}
	if err := store.SaveICEntryBatch(ctx, ics); err != nil {
		t.Fatalf("SaveICEntryBatch: %v", err)
	}

	// 幂等：同一主键重写必须走 ON CONFLICT DO UPDATE，而不是新增行。
	returns[0].AvgReturn = 0.999
	returns[0].TopMinusBot = 0.777
	if err := store.SaveFactorReturnBatch(ctx, returns[:1]); err != nil {
		t.Fatalf("SaveFactorReturnBatch (conflict update): %v", err)
	}

	got, err := store.GetFactorReturns(ctx, factorAnalyticsTestFactor, day1, day2)
	if err != nil {
		t.Fatalf("GetFactorReturns: %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("factor returns = %d rows, want 10（重写必须命中同一行）", len(got))
	}
	if got[0].Quintile != 1 || got[0].AvgReturn != 0.999 || got[0].TopMinusBot != 0.777 {
		t.Fatalf("conflict update not applied: %+v", got[0])
	}
	if !got[0].TradeDate.Equal(day1) || !got[9].TradeDate.Equal(day2) {
		t.Fatalf("ordering drift: first=%s last=%s", got[0].TradeDate, got[9].TradeDate)
	}

	gotIC, err := store.GetICEntries(ctx, factorAnalyticsTestFactor, day1, day2)
	if err != nil {
		t.Fatalf("GetICEntries: %v", err)
	}
	if len(gotIC) != 2 {
		t.Fatalf("ic entries = %d, want 2", len(gotIC))
	}
	if gotIC[0].IC != 0.2 || gotIC[1].IC != 0.4 {
		t.Fatalf("ic values drift: %v", []float64{gotIC[0].IC, gotIC[1].IC})
	}

	summaries, err := store.ListFactorAnalyticsSummaries(ctx)
	if err != nil {
		t.Fatalf("ListFactorAnalyticsSummaries: %v", err)
	}
	var found *FactorAnalyticsSummary
	for i := range summaries {
		if summaries[i].FactorName == factorAnalyticsTestFactor {
			found = &summaries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("summary for %q missing: %+v", factorAnalyticsTestFactor, summaries)
	}
	if found.Observations != 2 {
		t.Fatalf("observations = %d, want 2（按 trade_date 去重，不是 10 行）", found.Observations)
	}
	if !found.StartDate.Equal(day1) || !found.EndDate.Equal(day2) {
		t.Fatalf("coverage = [%s, %s], want [%s, %s]", found.StartDate, found.EndDate, day1, day2)
	}
	if diff := found.MeanIC - 0.3; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("mean ic = %.9f, want 0.3", found.MeanIC)
	}
	if found.LatestIC != 0.4 {
		t.Fatalf("latest ic = %.9f, want 0.4", found.LatestIC)
	}
}
