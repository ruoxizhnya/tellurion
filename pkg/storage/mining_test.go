package storage

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"tellurion/pkg/testenv"
)

// 因子挖掘留痕（D8 ③2 / migration 055 factor_mining_records）的库侧测试。
//
// 这份面此前**没有任何测试**：写入/读取路径都跑过（同包 D3/D4 测试同款形状），
// 但没有一条断言钉它。DBT-12 把挖掘编排接上这条持久化路径，先把库侧行为钉住
// 再接线，否则「写进去了」只能靠人眼看一轮输出。
//
// 与 pkg/storage/factor_registration_test.go 的差别是刻意的：那个文件按整表
// DELETE 清理，本文件**只按自己的锚点删**。共享开发库上多个会话并行
// （DATA-09 记过一次实测代价），一个测试不该把别人的行当成自己的垃圾清掉。
// 锚点 = 只在测试里出现的 version_hash 前缀（dbt12-…，补足 64 位），断言也一律
// 按锚点过滤，不看全表行数——全表行数属于别人。

// dbt12Hash 造一个可识别的 64 位版本哈希锚点。
//
// 留痕表的哈希只按长度校验（sha-256 hex 的 64 字符契约在 domain 里），
// 不需要真算——本文件测的是「存进去读出来还是不是同一条」，不是指纹算法。
func dbt12Hash(tag string) string {
	h := "dbt12-" + tag
	if len(h) > 64 {
		panic("dbt12Hash: tag too long for a 64-char anchor")
	}
	return h + strings.Repeat("0", 64-len(h))
}

func newMiningRecordTestStore(t *testing.T) *PostgresStore {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dsn := testenv.DatabaseDSN()
	if dsn == "" {
		testenv.SkipNoDB(t)
	}
	// 库侧测试必须走不迁移入口（DATA-09）：默认构建的测试跑 migrate() 会真的
	// 改共享开发库，护栏在 internal/repoguard/store_migration_isolation_test.go。
	store, err := NewPostgresStoreConnected(ctx, dsn)
	if err != nil {
		t.Fatalf("[testdb] TEST_DATABASE_DSN 已设置但连不上（fail-loud）: %v", err)
	}
	t.Cleanup(store.Close)

	anchor := func(c context.Context) {
		_, _ = store.pool.Exec(c, `DELETE FROM factor_mining_records WHERE version_hash LIKE 'dbt12-%'`)
	}
	if _, err := store.pool.Exec(ctx, `DELETE FROM factor_mining_records WHERE version_hash LIKE 'dbt12-%'`); err != nil {
		t.Skipf("factor_mining_records table unavailable (run migrations first): %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		anchor(cleanupCtx)
		var left int
		if err := store.pool.QueryRow(cleanupCtx,
			`SELECT count(*) FROM factor_mining_records WHERE version_hash LIKE 'dbt12-%'`).Scan(&left); err != nil {
			t.Errorf("cleanup 复核读不到行数: %v", err)
			return
		}
		if left != 0 {
			t.Errorf("cleanup 后仍残留 %d 行 dbt12-* 锚点行", left)
		}
	})
	return store
}

// miningRec 造一条挖掘留痕。admitted=false 时必须带 reject_reason（domain 与
// DB CHECK 双向都要求），且不带沙箱凭证。
func miningRec(hash string, name domain.FactorType, admitted bool, at time.Time) domain.FactorMiningRecord {
	rec := domain.FactorMiningRecord{
		FactorName:   name,
		VersionHash:  hash,
		SourceKind:   domain.FactorSourceAI,
		Generator:    "dbt12-storage-test",
		Generation:   2,
		Rationale:    "10 日动量在样例快照上稳定",
		NumTrials:    10,
		FailedTrials: 3,
		Admitted:     admitted,
		OverfitRawP:  0.0125,
		OverfitAdjP:  0.0375,
		OverfitFlag:  false,
		OOSInSample:  0.35,
		OOSOutSample: 0.28,
		OOSPass:      true,
		CreatedAt:    at,
	}
	if admitted {
		rec.SandboxHash = hash // 已入库必须带绑定该版本的凭证
		return rec
	}
	rec.RejectReason = "governance: out-of-sample IC retention below floor"
	return rec
}

func mustSaveMining(t *testing.T, store *PostgresStore, rec domain.FactorMiningRecord) {
	t.Helper()
	if err := store.SaveFactorMiningRecord(context.Background(), rec); err != nil {
		t.Fatalf("save %s@%s: %v", rec.FactorName, rec.VersionHash, err)
	}
}

// findMining 按锚点从全表读取结果里挑出本测试关心的那条（不看全表行数）。
func findMining(t *testing.T, store *PostgresStore, hash string) (domain.FactorMiningRecord, bool) {
	t.Helper()
	recs, err := store.LoadFactorMiningRecords(context.Background())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, r := range recs {
		if r.VersionHash == hash {
			return r, true
		}
	}
	return domain.FactorMiningRecord{}, false
}

// ─── 写→读往返：已入库那条的 18 个字段逐一原样回来 ───

func TestFactorMiningRecords_RoundTripPreservesEveryField(t *testing.T) {
	store := newMiningRecordTestStore(t)
	hash := dbt12Hash("admitted")
	want := miningRec(hash, domain.FactorMomentum, true, time.Date(2026, 10, 20, 9, 30, 0, 0, time.UTC))
	mustSaveMining(t, store, want)

	got, ok := findMining(t, store, hash)
	if !ok {
		t.Fatalf("round trip: %s 读不回来（写路径与读路径的键不一致或行没落）", hash)
	}
	// 逐字段比，不用 DeepEqual：漏一个字段是治理留痕的失，而 DeepEqual 只会说
	// 「不相等」，说不出丢了哪一件。
	if got.FactorName != want.FactorName {
		t.Errorf("FactorName = %q, want %q", got.FactorName, want.FactorName)
	}
	if got.VersionHash != want.VersionHash {
		t.Errorf("VersionHash = %q, want %q", got.VersionHash, want.VersionHash)
	}
	if got.SourceKind != want.SourceKind {
		t.Errorf("SourceKind = %q, want %q", got.SourceKind, want.SourceKind)
	}
	if got.Generator != want.Generator {
		t.Errorf("Generator = %q, want %q", got.Generator, want.Generator)
	}
	if got.Generation != want.Generation {
		t.Errorf("Generation = %d, want %d", got.Generation, want.Generation)
	}
	if got.Rationale != want.Rationale {
		t.Errorf("Rationale = %q, want %q", got.Rationale, want.Rationale)
	}
	if got.NumTrials != want.NumTrials {
		t.Errorf("NumTrials = %d, want %d", got.NumTrials, want.NumTrials)
	}
	if got.FailedTrials != want.FailedTrials {
		t.Errorf("FailedTrials = %d, want %d", got.FailedTrials, want.FailedTrials)
	}
	if got.SandboxHash != want.SandboxHash {
		t.Errorf("SandboxHash = %q, want %q", got.SandboxHash, want.SandboxHash)
	}
	if got.Admitted != want.Admitted {
		t.Errorf("Admitted = %v, want %v", got.Admitted, want.Admitted)
	}
	if got.RejectReason != want.RejectReason {
		t.Errorf("RejectReason = %q, want %q", got.RejectReason, want.RejectReason)
	}
	if got.OverfitRawP != want.OverfitRawP {
		t.Errorf("OverfitRawP = %v, want %v", got.OverfitRawP, want.OverfitRawP)
	}
	if got.OverfitAdjP != want.OverfitAdjP {
		t.Errorf("OverfitAdjP = %v, want %v", got.OverfitAdjP, want.OverfitAdjP)
	}
	if got.OverfitFlag != want.OverfitFlag {
		t.Errorf("OverfitFlag = %v, want %v", got.OverfitFlag, want.OverfitFlag)
	}
	if got.OOSInSample != want.OOSInSample {
		t.Errorf("OOSInSample = %v, want %v", got.OOSInSample, want.OOSInSample)
	}
	if got.OOSOutSample != want.OOSOutSample {
		t.Errorf("OOSOutSample = %v, want %v", got.OOSOutSample, want.OOSOutSample)
	}
	if got.OOSPass != want.OOSPass {
		t.Errorf("OOSPass = %v, want %v", got.OOSPass, want.OOSPass)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, want.CreatedAt)
	}
}

// ─── 被拒的候选也留痕：空沙箱凭证必须原样读回（不能变成 64 个空格）───

func TestFactorMiningRecords_RoundTripRejectedKeepsEmptyAttestation(t *testing.T) {
	store := newMiningRecordTestStore(t)
	hash := dbt12Hash("rejected")
	want := miningRec(hash, domain.FactorSize, false, time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC))
	mustSaveMining(t, store, want)

	got, ok := findMining(t, store, hash)
	if !ok {
		t.Fatalf("被拒的候选也必须留痕（「试过但没要」是审计信息），读回来却没有 %s", hash)
	}
	if got.Admitted {
		t.Errorf("Admitted = true, want false")
	}
	if got.RejectReason != want.RejectReason {
		t.Errorf("RejectReason = %q, want %q", got.RejectReason, want.RejectReason)
	}
	// sandbox_hash 是 CHAR(64)：空串写进去会被补成 64 个空格，读路径不 trim
	// 就会把「没有凭证」读成「凭证是空格串」——那是一条会骗审计的字段。
	if got.SandboxHash != "" {
		t.Errorf("SandboxHash = %q (%d 字节), want 空串（被拒的候选没有沙箱凭证）", got.SandboxHash, len(got.SandboxHash))
	}
	if got.Rationale != want.Rationale || got.Generator != want.Generator {
		t.Errorf("来源留痕漂移: %+v", got)
	}
}

// ─── 幂等：同一 (名, 哈希) 重放 = no-op，且第一条留痕的时刻不被覆盖 ───

func TestFactorMiningRecords_SaveIdempotentKeepsFirstTrail(t *testing.T) {
	store := newMiningRecordTestStore(t)
	hash := dbt12Hash("idempotent")
	first := miningRec(hash, domain.FactorMomentum, true, time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC))
	mustSaveMining(t, store, first)

	// 重放「同一天又挖出同一版」：换了 created_at 也不该刷新——一个定义版本
	// 只该有一条「挖出它」的记录（DO NOTHING 的语义正是要钉住这个）。
	replay := first
	replay.CreatedAt = time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	replay.Rationale = "不该被写进去的第二次叙述"
	mustSaveMining(t, store, replay)

	loaded, err := store.LoadFactorMiningRecords(context.Background())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var rows []domain.FactorMiningRecord
	for _, r := range loaded {
		if r.VersionHash == hash {
			rows = append(rows, r)
		}
	}
	if len(rows) != 1 {
		t.Fatalf("重放后 %d 行, want 1（ON CONFLICT DO NOTHING）", len(rows))
	}
	if !rows[0].CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("重放覆盖了首次留痕时刻: got %v, want %v", rows[0].CreatedAt, first.CreatedAt)
	}
	if rows[0].Rationale != first.Rationale {
		t.Errorf("重放改写了首次叙述: %q", rows[0].Rationale)
	}
}

// ─── 写入路径自身 fail-closed：缺样留痕不进库 ───

func TestFactorMiningRecords_SaveRejectsInvalidAndWritesNothing(t *testing.T) {
	store := newMiningRecordTestStore(t)
	ctx := context.Background()

	cases := []struct {
		name string
		mut  func(r *domain.FactorMiningRecord)
	}{
		{"已入库但凭证没绑定该版本", func(r *domain.FactorMiningRecord) { r.SandboxHash = dbt12Hash("other") }},
		{"生成器缺失", func(r *domain.FactorMiningRecord) { r.Generator = "" }},
		{"来源未登记", func(r *domain.FactorMiningRecord) { r.SourceKind = "made_up" }},
		{"尝试次数非正", func(r *domain.FactorMiningRecord) { r.NumTrials = 0 }},
		{"名册外的因子名", func(r *domain.FactorMiningRecord) { r.FactorName = domain.FactorType("dbt12_not_a_factor") }},
		{"被拒不写原因", func(r *domain.FactorMiningRecord) { r.Admitted = false; r.RejectReason = "" }},
	}
	for i, tc := range cases {
		hash := dbt12Hash("invalid" + string(rune('a'+i)))
		rec := miningRec(hash, domain.FactorMomentum, true, time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC))
		tc.mut(&rec)
		if err := store.SaveFactorMiningRecord(ctx, rec); err == nil {
			t.Fatalf("%s: 缺样留痕竟然写成功了", tc.name)
		}
		if _, ok := findMining(t, store, hash); ok {
			t.Errorf("%s: 校验失败却留下了行 %s", tc.name, hash)
		}
	}
}

// ─── 加载即复核 fail-closed：绕过写入路径写进库的畸形行不会被将就着用 ───

func TestFactorMiningRecords_LoadFailsClosedOnMalformedRow(t *testing.T) {
	store := newMiningRecordTestStore(t)
	ctx := context.Background()
	hash := dbt12Hash("tampered")

	// 库侧 CHECK 认得「生成器/来源/尝试次数/凭证绑定/拒绝原因」，认不得
	// 「因子名在不在冻结名册里」——那条纪律只在 domain.Validate。
	// 绕过写入路径直插一行名册外的记录：DB 接受（护栏没覆盖到），读路径必须拒。
	_, err := store.pool.Exec(ctx, `
		INSERT INTO factor_mining_records
			(factor_name, version_hash, source_kind, generator, generation, rationale,
			 num_trials, failed_trials, sandbox_hash, admitted, reject_reason, created_at)
		VALUES ('dbt12_not_a_factor', $1, 'ai_hypothesis', 'dbt12-storage-test', 1, '', 1, 0, '', FALSE, 'raw insert', NOW())`,
		hash,
	)
	if err != nil {
		t.Fatalf("前提不成立：畸形行没能绕过写入路径插进库: %v", err)
	}
	if _, err := store.LoadFactorMiningRecords(ctx); err == nil {
		t.Fatal("名册外的因子名必须让加载 fail-closed，而不是将就着返回")
	} else if !strings.Contains(err.Error(), "dbt12_not_a_factor") {
		t.Fatalf("错误必须点名是哪条留痕坏了，got: %v", err)
	}
}

// ─── 读序稳定：按 (因子名, 创建时刻) 升序，重放不抖动 ───

func TestFactorMiningRecords_LoadOrderedByNameThenTime(t *testing.T) {
	store := newMiningRecordTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)

	// 三个键：momentum 一条 + size 早/晚两条。插入顺序故意打乱，
	// 断言读回来稳定按 (因子名, 创建时刻) 升序——与查询的 ORDER BY 一致。
	plan := []struct {
		hash string
		name domain.FactorType
		at   time.Time
	}{
		{dbt12Hash("order-size-late"), domain.FactorSize, base.Add(2 * time.Hour)},
		{dbt12Hash("order-momentum"), domain.FactorMomentum, base.Add(1 * time.Hour)},
		{dbt12Hash("order-size-early"), domain.FactorSize, base},
	}
	for _, p := range plan {
		rec := miningRec(p.hash, p.name, true, p.at)
		if err := store.SaveFactorMiningRecord(ctx, rec); err != nil {
			t.Fatalf("save %s: %v", p.hash, err)
		}
	}

	loaded, err := store.LoadFactorMiningRecords(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var got []domain.FactorType
	var gotHashes []string
	for _, r := range loaded {
		if strings.HasPrefix(r.VersionHash, "dbt12-order-") {
			got = append(got, r.FactorName)
			gotHashes = append(gotHashes, r.VersionHash)
		}
	}
	want := []struct {
		name domain.FactorType
		hash string
	}{
		// 读序 = 因子名字典序 + 同按创建时刻升序：momentum < size。
		{domain.FactorMomentum, dbt12Hash("order-momentum")},
		{domain.FactorSize, dbt12Hash("order-size-early")},
		{domain.FactorSize, dbt12Hash("order-size-late")},
	}
	if len(got) != len(want) {
		t.Fatalf("锚点行 %d 条, want %d（%v）", len(got), len(want), gotHashes)
	}
	for i, w := range want {
		if got[i] != w.name || gotHashes[i] != w.hash {
			t.Errorf("第 %d 条 = %s@%s, want %s@%s（读序必须稳定，否则留痕表重放一次换一个样）",
				i, got[i], gotHashes[i], w.name, w.hash)
		}
	}
}
