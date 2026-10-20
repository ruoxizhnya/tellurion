package main

// DBT-04：mining 编排入口的测试。
//
// 覆盖：候选文件解析（合法 / 坏 JSON）、IC 评估器（同向构造 → IC=1、
// 乱数 → |IC|<1 且逐日可比）、runMiningPipeline 端到端（内存快照 +
// 合规候选 → admitted 留痕输出）。真实 store 的加载腿由
// loadCloseSnapshot 的空 symbols / 无数据分支测试覆盖（真库冒烟走
// 子命令留证，不进单测）。
//
// DBT-12：D4 登记表接上 PostgreSQL 之后的三条腿——
//   - 从库重建账本（历史登记跨运行存活：同版再挖一次撞 duplicate，
//     而不是悄悄二次入库）；
//   - 裁决后写穿（新增登记 + 每条有版本哈希的候选留痕，被拒的也留）；
//   - 写失败必须可见（PERSIST-FAILED 行 + 非零退出码），读侧不可用
//     必须如实说明降级走了哪条路径。
//
// 逻辑腿用 fakeMiningStore（生产实现只有 *storage.PostgresStore，替身
// 不进生产代码），真库腿另有一条：假替身证明不了「真表的键、CHECK 与
// CHAR 列能把这条留痕原样接住」。

import (
	"bytes"
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/domain"
	"tellurion/pkg/mining"
	"tellurion/pkg/storage"
	"tellurion/pkg/testenv"
)

func TestLoadCandidatesFile(t *testing.T) {
	t.Parallel()

	t.Run("valid file", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "cands.json")
		require.NoError(t, os.WriteFile(path, []byte(`[
			{"name":"momentum","formula":"close / ts_delay(close, 10) - 1",
			 "lookahead":"trade_date","lookback":20,"effective_from":"2026-10-17",
			 "provenance":{"source_kind":"ai_hypothesis","generator":"dbt04-test",
			 "generation":1,"num_trials":3,"failed_trials":1}}
		]`), 0o644))
		cands, err := loadCandidatesFile(path)
		require.NoError(t, err)
		require.Len(t, cands, 1)
		assert.Equal(t, domain.FactorMomentum, cands[0].Name)
	})

	t.Run("bad json fails loudly", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "bad.json")
		require.NoError(t, os.WriteFile(path, []byte(`{not json}`), 0o644))
		_, err := loadCandidatesFile(path)
		require.Error(t, err)
	})

	t.Run("empty array is legal", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "empty.json")
		require.NoError(t, os.WriteFile(path, []byte(`[]`), 0o644))
		cands, err := loadCandidatesFile(path)
		require.NoError(t, err)
		assert.Empty(t, cands)
	})
}

// dbt04Snapshot 构造已知形态的快照：三只股票，close 单调递增且
// slope/base 比例不同——momentum 型因子（close/ts_delay-1）的截面排序与
// forward return 的截面排序完全一致 ⇒ IC 应为 1（排序一致 ⇔ Pearson=1，
// 即便数值本身不同）。注意 slope/base 必须有差异：等比递增的三只股票
// momentum 值完全相同，截面方差为 0，Pearson 是 NaN。
func dbt04Snapshot() *mining.Snapshot {
	series := func(base, slope float64, n int) []float64 {
		out := make([]float64, n)
		for i := range out {
			out[i] = base + slope*float64(i)
		}
		return out
	}
	n := 120
	return mining.NewSnapshot(map[string]map[string][]float64{
		"close": {
			"AAA": series(10, 0.5, n), // slope/base = 0.05
			"BBB": series(20, 0.2, n), // 0.01
			"CCC": series(30, 0.1, n), // 0.0033
		},
	})
}

func dbt04MomentumCandidate() mining.Candidate {
	return mining.Candidate{
		Name:          domain.FactorMomentum,
		Formula:       "close / ts_delay(close, 10) - 1",
		Validity:      "close > 0 and ts_delay(close, 10) > 0",
		Caliber:       domain.CaliberZScore,
		Lookahead:     "trade_date",
		Lookback:      60,
		EffectiveFrom: "2026-10-17",
		Provenance: mining.Provenance{
			SourceKind: domain.FactorSourceAI, Generator: "dbt04-test",
			Generation: 1, NumTrials: 3, FailedTrials: 1,
		},
	}
}

func TestICEvaluator_PrefectlyAlignedFactors(t *testing.T) {
	t.Parallel()
	ev := &icEvaluator{horizon: 5, inFraction: 0.7}
	m, err := ev.Evaluate(context.Background(), dbt04MomentumCandidate(), dbt04Snapshot())
	require.NoError(t, err)

	// 单调系列下 momentum 值与 forward return 完全同向 → 每日截面 IC=1。
	assert.Greater(t, m.InSampleIC, 0.95, "同向构造的 in-sample IC 应接近 1（Pearson 非秩相关，非线性关系下 <1）")
	assert.Greater(t, m.OutSampleIC, 0.95, "同向构造的 out-sample IC 应接近 1")
	assert.Positive(t, m.NumPeriods)
}

func TestICEvaluator_DegenerateCrossSection(t *testing.T) {
	t.Parallel()
	ev := &icEvaluator{horizon: 5, inFraction: 0.7}
	// 只有两只 → 截面 <3 全被过滤 → 评估报「no evaluable cross-section」。
	snap := mining.NewSnapshot(map[string]map[string][]float64{
		"close": {
			"AAA": {10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25},
			"BBB": {20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35},
		},
	})
	_, err := ev.Evaluate(context.Background(), dbt04MomentumCandidate(), snap)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no evaluable cross-section")
}

// TestRunMiningPipeline_EndToEnd 是 DBT-04 ④ 的冒烟腿：合规候选 →
// 沙箱 → 治理 → 登记，输出留痕含 run id 与 ADMITTED 行。
func TestRunMiningPipeline_EndToEnd(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	rc := runMiningPipeline(
		[]mining.Candidate{dbt04MomentumCandidate()},
		dbt04Snapshot(),
		time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC),
		"dbt04-test-registrar", 5, &fakeMiningStore{}, &out,
	)
	require.Equal(t, 0, rc, "output: %s", out.String())

	s := out.String()
	assert.Contains(t, s, "mining run ", "输出必须带 run id 留痕")
	assert.Contains(t, s, "ADMITTED momentum", "合规候选应被登记")
	assert.Contains(t, s, "version=", "ADMITTED 行必须带版本哈希（D4 锁版本键）")
	assert.Contains(t, s, "1/1 admitted")
	// DBT-12：接线已经落上，输出不许再自陈「持久化是后续工单」，也不许
	// 出现内存形态（账本已从库重建）。
	assert.NotContains(t, s, "persist wiring is a follow-up",
		"登记表已接库，输出不能再自陈未接线：%s", s)
	assert.NotContains(t, s, "in-memory",
		"账本走的是库路径，输出里的来源必须如实：%s", s)
}

// TestRunMiningPipeline_BadFormulaRejected 钉住拒收腿：公式不解析的候选
// 只拒收该条，不中断整轮（「挖一批、坏的挡在外面」）。
func TestRunMiningPipeline_BadFormulaRejected(t *testing.T) {
	t.Parallel()
	bad := dbt04MomentumCandidate()
	bad.Formula = "this is not a formula (("

	var out bytes.Buffer
	rc := runMiningPipeline(
		[]mining.Candidate{bad, dbt04MomentumCandidate()},
		dbt04Snapshot(),
		time.Now().UTC(), "dbt04-test-registrar", 5, &fakeMiningStore{}, &out,
	)
	require.Equal(t, 0, rc)
	s := out.String()
	assert.Contains(t, s, "REJECTED momentum", "坏候选应被拒收并留 reject reason")
	assert.Contains(t, s, "1/2 admitted", "好候选不受坏候选影响")
}

// TestLoadCloseSnapshot_Guards：无 symbols / 快照空的失败分支（不触真库）。
func TestLoadCloseSnapshot_Guards(t *testing.T) {
	t.Parallel()

	t.Run("no symbols", func(t *testing.T) {
		t.Parallel()
		_, _, err := loadCloseSnapshot(context.Background(), nil, nil, 120)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no symbols selected")
	})
}

// 编译守卫：icEvaluator 的 Pearson 实现对常数序列必须返回 NaN（方差 0）。
func TestPearson_ConstantSeries(t *testing.T) {
	t.Parallel()
	assert.True(t, math.IsNaN(pearson([]float64{1, 1, 1}, []float64{1, 2, 3})),
		"常数序列方差为 0，相关系数必须 NaN 而非除零 panic")
}

// ─── DBT-12：D4 登记表的 PostgreSQL 接线 ─────────────────────────────────

// fakeMiningStore 是 miningStore 的测试替身：记下每次写调用，可注入读/写失败。
//
// 生产实现只有一个（*storage.PostgresStore），本替身不进生产代码，也不给
// 生产路径加分支：runMiningPipeline 拿到的永远是接口，degradation 由库真不
// 可达决定，不由「是不是测试」决定。
type fakeMiningStore struct {
	loaded    []domain.FactorRegistration
	loadErr   error
	saveErr   error
	savedRegs []domain.FactorRegistration
	savedRecs []domain.FactorMiningRecord
}

func (f *fakeMiningStore) LoadFactorRegistrations(context.Context) ([]domain.FactorRegistration, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	return f.loaded, nil
}

func (f *fakeMiningStore) SaveFactorRegistration(_ context.Context, reg domain.FactorRegistration) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.savedRegs = append(f.savedRegs, reg)
	return nil
}

func (f *fakeMiningStore) SaveFactorMiningRecord(_ context.Context, rec domain.FactorMiningRecord) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.savedRecs = append(f.savedRecs, rec)
	return nil
}

// registrationOf 把候选翻成一条合规登记（等价于「上一轮挖出来并登记过」）。
func registrationOf(t *testing.T, cand mining.Candidate, registrar string, at time.Time) domain.FactorRegistration {
	t.Helper()
	def, err := cand.Definition()
	require.NoError(t, err)
	return domain.FactorRegistration{
		Definition:   def,
		VersionHash:  def.VersionHash(),
		Registrar:    registrar,
		Status:       domain.RegistrationActive,
		RegisteredAt: at,
	}
}

// dbt12NewVersionCandidate 是同一因子名的**另一个版本**（lookback 进 canonical
// 投影 ⇒ 新哈希），用来区分「历史已在册」与「本轮新增」。
func dbt12NewVersionCandidate() mining.Candidate {
	c := dbt04MomentumCandidate()
	c.Lookback = 45
	return c
}

func TestRunMiningPipeline_WritesThroughNewRegistrationsAndTrail(t *testing.T) {
	t.Parallel()
	store := &fakeMiningStore{}
	good := dbt04MomentumCandidate()
	bad := good
	bad.Formula = "this is not a formula (("

	var out bytes.Buffer
	rc := runMiningPipeline([]mining.Candidate{bad, good}, dbt04Snapshot(),
		time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC), "dbt12-registrar", 5, store, &out)
	require.Equal(t, 0, rc, "output: %s", out.String())

	goodDef, err := good.Definition()
	require.NoError(t, err)

	// 写穿①：本轮新增的登记落到 factor_registrations，登记人 = 本轮 -registrar。
	require.Len(t, store.savedRegs, 1, "只该写本轮新增的那一条")
	assert.Equal(t, goodDef.VersionHash(), store.savedRegs[0].VersionHash)
	assert.Equal(t, "dbt12-registrar", store.savedRegs[0].Registrar)
	assert.Equal(t, domain.RegistrationActive, store.savedRegs[0].Status)

	// 写穿②：留痕只给「有定义版本」的候选——公式没解析过的候选没有版本，
	// 不进以版本为键的留痕表（mining.Record.Evidence 的既有语义）。
	require.Len(t, store.savedRecs, 1)
	rec := store.savedRecs[0]
	assert.True(t, rec.Admitted, "合规候选的留痕必须是 admitted")
	assert.Equal(t, goodDef.VersionHash(), rec.VersionHash)
	assert.Equal(t, goodDef.VersionHash(), rec.SandboxHash,
		"已入库的留痕必须带绑定该版本的沙箱凭证")
	assert.Positive(t, rec.NumTrials)

	assert.NotContains(t, out.String(), "PERSIST-FAILED")
	assert.Contains(t, out.String(), "registrations written: 1, mining records written: 1")
	assert.Contains(t, out.String(), "registry = postgres (rebuilt from 0 existing registration(s))")
}

func TestRunMiningPipeline_PriorRegistrationSeenAndNotRewritten(t *testing.T) {
	t.Parallel()
	prior := registrationOf(t, dbt04MomentumCandidate(), "dbt12-history", time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC))
	store := &fakeMiningStore{loaded: []domain.FactorRegistration{prior}}

	var out bytes.Buffer
	rc := runMiningPipeline([]mining.Candidate{dbt04MomentumCandidate()}, dbt04Snapshot(),
		time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC), "dbt12-registrar", 5, store, &out)
	require.Equal(t, 0, rc, "output: %s", out.String())

	s := out.String()
	assert.Contains(t, s, "registry = postgres (rebuilt from 1 existing registration(s))",
		"历史登记必须在起跑时被看见：%s", s)
	assert.Contains(t, s, "REJECTED momentum", "同版再挖一次应被账本拦下，而不是二次入库")
	assert.Contains(t, s, "already registered", "拒收理由必须点名重复登记：%s", s)
	assert.Empty(t, store.savedRegs, "历史已在册的版本不得重复写穿")
	// 被拒的候选照样留痕：「试过但没要」同样是审计信息。
	require.Len(t, store.savedRecs, 1)
	assert.False(t, store.savedRecs[0].Admitted)
	assert.Contains(t, store.savedRecs[0].RejectReason, "already registered")
}

func TestRunMiningPipeline_PersistFailureIsVisibleAndNonZero(t *testing.T) {
	t.Parallel()
	store := &fakeMiningStore{saveErr: errors.New("dbt12 injected write failure")}

	var out bytes.Buffer
	rc := runMiningPipeline([]mining.Candidate{dbt04MomentumCandidate()}, dbt04Snapshot(),
		time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC), "dbt12-registrar", 5, store, &out)
	require.Equal(t, 1, rc, "写穿失败不能算这一轮成功：%s", out.String())

	s := out.String()
	assert.Contains(t, s, "PERSIST-FAILED registration momentum", s)
	assert.Contains(t, s, "PERSIST-FAILED mining record momentum", s)
	assert.Contains(t, s, "dbt12 injected write failure", "失败原因必须原样可见，不许吞")
	assert.Contains(t, s, "persist failures: 2")
}

func TestRunMiningPipeline_LoadFailureDegradesLoudly(t *testing.T) {
	t.Parallel()
	store := &fakeMiningStore{loadErr: errors.New("dbt12 relation factor_registrations does not exist")}

	var out bytes.Buffer
	rc := runMiningPipeline([]mining.Candidate{dbt04MomentumCandidate()}, dbt04Snapshot(),
		time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC), "dbt12-registrar", 5, store, &out)
	require.Equal(t, 0, rc, "读侧降级不阻断编排（写侧仍然尝试），但必须在输出里说明：%s", out.String())

	s := out.String()
	assert.Contains(t, s, "registry = in-memory fallback (postgres load failed: dbt12 relation factor_registrations does not exist)",
		"降级路径必须写清楚是从哪一条失败掉下来的：%s", s)
	assert.Len(t, store.savedRegs, 1, "降级到账本为空不等于放弃写穿")
	assert.Len(t, store.savedRecs, 1)
}

// ─── 真库腿：接线落到 PostgreSQL 上到底留没留下东西 ─────────────────────

// newMiningCmdTestStore 连库（**不迁移**，DATA-09：默认构建的测试不许调
// NewPostgresStore）并只清理本测试自己写下的行 —— 锚点是候选内容派生出来的
// version_hash，别的会话的行不动。
//
// 已知暴露：pkg/storage/factor_registration_test.go 会整表清 factor_registrations
// 与 factor_registration_audit，两个包的测试二进制并行时我的锚点行可能在
// 本测试的读窗口里被清掉。这里把 seed→run→读 压成一个尽量短的窗口，
// 并在断言消息里点名这个来源；红的时候先看输出里有没有 REJECTED/ADMITTED
// 行，再判是接线坏了还是被邻居擦了。
func newMiningCmdTestStore(t *testing.T, anchors ...string) *storage.PostgresStore {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dsn := testenv.DatabaseDSN()
	if dsn == "" {
		testenv.SkipNoDB(t)
	}
	store, err := storage.NewPostgresStoreConnected(ctx, dsn)
	if err != nil {
		t.Fatalf("[testdb] TEST_DATABASE_DSN 已设置但连不上（fail-loud）: %v", err)
	}
	t.Cleanup(store.Close)
	if _, err := store.DB().Exec(ctx, `SELECT count(*) FROM factor_registrations`); err != nil {
		t.Skipf("factor_registrations 不可用（先起一次服务或跑集成套件建库）: %v", err)
	}

	del := func(c context.Context) {
		for _, h := range anchors {
			_, _ = store.DB().Exec(c, `DELETE FROM factor_registration_audit WHERE version_hash = $1`, h)
			_, _ = store.DB().Exec(c, `DELETE FROM factor_mining_records WHERE version_hash = $1`, h)
			_, _ = store.DB().Exec(c, `DELETE FROM factor_registrations WHERE version_hash = $1`, h)
		}
	}
	del(ctx)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		del(cleanupCtx)
		for _, h := range anchors {
			var regs, audits, recs int
			_ = store.DB().QueryRow(cleanupCtx, `SELECT count(*) FROM factor_registrations WHERE version_hash = $1`, h).Scan(&regs)
			_ = store.DB().QueryRow(cleanupCtx, `SELECT count(*) FROM factor_registration_audit WHERE version_hash = $1`, h).Scan(&audits)
			_ = store.DB().QueryRow(cleanupCtx, `SELECT count(*) FROM factor_mining_records WHERE version_hash = $1`, h).Scan(&recs)
			if regs+audits+recs != 0 {
				t.Errorf("cleanup 后锚点 %s 仍残留：registrations=%d audit=%d mining=%d", h, regs, audits, recs)
			}
		}
	})
	return store
}

func countByHash(t *testing.T, store *storage.PostgresStore, table, hash string) int {
	t.Helper()
	var n int
	// 表名是本包测试自己的三张治理表，不是用户输入；显式 switch 防注入面。
	switch table {
	case "factor_registrations", "factor_registration_audit", "factor_mining_records":
	default:
		t.Fatalf("countByHash: 未登记的表 %q", table)
	}
	require.NoError(t, store.DB().QueryRow(context.Background(),
		"SELECT count(*) FROM "+table+" WHERE version_hash = $1", hash).Scan(&n))
	return n
}

// TestRunMiningPipeline_PostgresRebuildsBookAndPersistsTrail 是 DBT-12 的
// 端到端库侧腿，跑**两轮**：
//
//   - 第一轮起跑前只往库里塞一条历史登记 → 输出必须看见它（同版再挖一次撞
//     duplicate），本轮新增的那版登记与两条留痕必须真的落到 PostgreSQL 的
//     三张表里；
//   - 第二轮拿同一批候选再跑 → 上一轮写进去的登记必须被这一轮读回来并拦住
//     重复登记（0 admitted / 0 registrations written）。这才是「跨运行存活」
//     的正证据，顺带证明读路径（加载时重派生哈希的 fail-closed 复核）接得住
//     自己写出去的形态。
//
// 断言一律按 (factor_name, version_hash) 锚点定向读，不看全表行数：共享开发库
// 上别人也在写，全表计数不属于本测试。
func TestRunMiningPipeline_PostgresRebuildsBookAndPersistsTrail(t *testing.T) {
	dupCand := dbt04MomentumCandidate()
	newCand := dbt12NewVersionCandidate()
	dupReg := registrationOf(t, dupCand, "dbt12-history-registrar", time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC))
	newDef, err := newCand.Definition()
	require.NoError(t, err)
	require.NotEqual(t, dupReg.VersionHash, newDef.VersionHash(),
		"前提不成立：两个候选必须不同版本，否则「不重复入库」这条腿没有对象")
	dupHash, newHash := dupReg.VersionHash, newDef.VersionHash()

	store := newMiningCmdTestStore(t, dupHash, newHash)
	ctx := context.Background()

	// 历史：上一轮把这版定义接进了库（本轮起跑前塞进去 = 跨运行的历史）。
	require.NoError(t, store.SaveFactorRegistration(ctx, dupReg))

	cands := []mining.Candidate{dupCand, newCand}
	var out1 bytes.Buffer
	rc := runMiningPipeline(cands, dbt04Snapshot(),
		time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC), "dbt12-postgres-registrar", 5, store, &out1)
	require.Equal(t, 0, rc, "output: %s", out1.String())
	s1 := out1.String()

	// ① 从库重建：历史登记被看见（库路径 + 至少读到刚塞进去的那一条）。
	assert.Regexp(t, `registry = postgres \(rebuilt from [1-9]\d* existing registration`, s1,
		"账本必须从 factor_registrations 重建；落到 in-memory fallback 就是读侧坏了：%s", s1)
	assert.Contains(t, s1, "REJECTED momentum", s1)
	assert.Contains(t, s1, "already registered", "同版二次登记必须被账本拦下：%s", s1)
	assert.Contains(t, s1, "ADMITTED momentum", s1)
	assert.Contains(t, s1, "version=", "ADMITTED 行必须带版本哈希：%s", s1)
	assert.Contains(t, s1, "1/2 admitted", s1)

	// ② 写穿：本轮新增的登记进了 factor_registrations，定义快照自包含。
	var registrar, status, formula string
	var lookback int
	require.NoError(t, store.DB().QueryRow(ctx,
		`SELECT registrar, status, definition->>'formula', (definition->>'lookback')::int
		   FROM factor_registrations WHERE version_hash = $1`, newHash).
		Scan(&registrar, &status, &formula, &lookback),
		"本轮新增的登记没在库里（写穿没落上）")
	assert.Equal(t, "dbt12-postgres-registrar", registrar)
	assert.Equal(t, "active", status)
	assert.Equal(t, newDef.Formula, formula)
	assert.Equal(t, newDef.Lookback, lookback)

	// ③ 审计留痕由 SaveFactorRegistration 自带，且历史那一版没被再记一次
	//    （登记行的重复由主键挡，这一条挡的是「本轮为它又写了一遍」）。
	assert.Equal(t, 1, countByHash(t, store, "factor_registrations", newHash))
	assert.Equal(t, 1, countByHash(t, store, "factor_registration_audit", newHash))
	assert.Equal(t, 1, countByHash(t, store, "factor_registration_audit", dupHash),
		"历史版本本轮被拒收，不该多出一条 register 审计")

	// ④ 留痕：入库的那版带绑定该版本的凭证，被拒的那版写明原因。
	var newAdmitted bool
	var newSandbox, newGenerator, newRationale string
	var newOOS float64
	require.NoError(t, store.DB().QueryRow(ctx,
		`SELECT admitted, BTRIM(sandbox_hash), generator, rationale, oos_in_sample
		   FROM factor_mining_records WHERE version_hash = $1`, newHash).
		Scan(&newAdmitted, &newSandbox, &newGenerator, &newRationale, &newOOS),
		"入库那版的留痕没在库里")
	assert.True(t, newAdmitted)
	assert.Equal(t, newHash, newSandbox, "已入库的留痕必须带绑定该版本的沙箱凭证")
	assert.Equal(t, "dbt04-test", newGenerator)
	assert.Positive(t, newOOS)

	var dupAdmitted bool
	var dupReason, dupSandbox string
	require.NoError(t, store.DB().QueryRow(ctx,
		`SELECT admitted, reject_reason, BTRIM(sandbox_hash) FROM factor_mining_records WHERE version_hash = $1`, dupHash).
		Scan(&dupAdmitted, &dupReason, &dupSandbox), "被拒的候选也必须留痕（「试过但没要」是审计信息）")
	assert.False(t, dupAdmitted)
	assert.Contains(t, dupReason, "already registered")
	// 被拒的这版是「过了沙箱但没入库」（撞了历史登记），所以凭证仍在：
	// 留痕区分的是 admitted 与否，不是有没有验过。
	assert.Equal(t, dupHash, dupSandbox)

	assert.Contains(t, s1, "registrations written: 1, mining records written: 2", s1)
	assert.NotContains(t, s1, "PERSIST-FAILED", s1)

	// ⑤ 第二轮：上一轮写进去的登记必须被这一轮读回来并拦住重复登记。
	//    「重启即失」的反面就是这里 —— 0 admitted、0 条新增登记。
	var out2 bytes.Buffer
	rc2 := runMiningPipeline(cands, dbt04Snapshot(),
		time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC), "dbt12-postgres-registrar", 5, store, &out2)
	require.Equal(t, 0, rc2, "output: %s", out2.String())
	s2 := out2.String()
	assert.Regexp(t, `registry = postgres \(rebuilt from [2-9]\d* existing registration`, s2,
		"第二轮必须把第一轮写进去的那版也读回来：%s", s2)
	assert.Contains(t, s2, "0/2 admitted", s2)
	assert.Contains(t, s2, "registrations written: 0", s2)
	assert.Equal(t, 1, countByHash(t, store, "factor_registrations", newHash),
		"跨运行重跑不得把同一版登记写第二遍")
	assert.Equal(t, 1, countByHash(t, store, "factor_registration_audit", newHash),
		"跨运行重跑不得留下第二条 register 审计")
}
