package backtest

// QT-66 的验收腿：`risk_rules_hash` 真的从 provider 进 provenance。
//
// 三条要证的事（裁-10 / 裁-11）：
//  1. 接入了 provider ⇒ 记录里的 `risk_rules_hash` 等于 provider 给的值，
//     且换一个 hash（换规则快照）⇒ 新 run 的 `run_spec_hash` 跟着变；
//  2. 未接入 ⇒ 空串，且状态是 `not_configured`（唯一允许空串的分支）；
//  3. 接了但取不到（报错 / 返回空串）⇒ run = failed +
//     `risk_rules_unavailable`，且**不写** provenance（不留半份身份）。

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/tellurion-contracts/fees"
)

// fakeRiskRulesProvider 是确定性替身：按给定 hash / error 回答，并记录调用。
type fakeRiskRulesProvider struct {
	hash   string
	err    error
	calls  int
	pinned []string
}

func (f *fakeRiskRulesProvider) ResolveRiskRulesHash(_ context.Context, pinnedHash string) (string, error) {
	f.calls++
	f.pinned = append(f.pinned, pinnedHash)
	if f.err != nil {
		return "", f.err
	}
	return f.hash, nil
}

// qt66Attempt 跑一次真实 RunBacktest，不假定成功（与 provenanceScenario.run
// 的差别只在失败腿也要能看现场）。
func qt66Attempt(t *testing.T, sc provenanceScenario, provider RiskRulesProvider) (*BacktestResponse, error, *provenanceCapture) {
	t.Helper()
	registerQT63Probe(t)

	days := sc.days
	if days == 0 {
		days = 20
	}
	seed := sc.seed
	if seed == 0 {
		seed = 42
	}
	commission := sc.commission
	if commission == 0 {
		commission = fees.DefaultCommissionRate
	}
	slippage := sc.slippage
	if slippage == 0 {
		slippage = fees.DefaultSlippageRate
	}
	stampTax := sc.stampTax
	if stampTax == 0 {
		stampTax = fees.DefaultStampTaxRate
	}

	capture := &provenanceCapture{}
	opts := provenanceBaseOptions(t)
	opts = append(opts, WithRunProvenanceWriter(capture))
	if provider != nil {
		opts = append(opts, WithRiskRulesProvider(provider))
	}

	eng, err := NewEngine(provenanceViper(seed, commission, slippage, stampTax), provenanceProvider(t, sc.pool, days), zerolog.Nop())
	require.NoError(t, err)
	applyOptions(eng, opts)

	resp, err := eng.RunBacktest(context.Background(), BacktestRequest{
		Strategy:       qt63ProbeName,
		StockPool:      sc.pool,
		StartDate:      provenanceTestStart.Format("2006-01-02"),
		EndDate:        provenanceTestStart.AddDate(0, 0, days-1).Format("2006-01-02"),
		InitialCapital: 1_000_000,
		RiskFreeRate:   0.03,
		RunID:          "qt66-attempt",
	})
	return resp, err, capture
}

func TestQT66_RiskRulesHashIsRecordedAndMovesRunSpec(t *testing.T) {
	hashA := strings.Repeat("a", 64)
	hashB := strings.Repeat("b", 64)

	sc := provenanceBaseline()
	pA := &fakeRiskRulesProvider{hash: hashA}
	sc.writer = &provenanceCapture{}
	sc.engineOpts = []EngineOption{WithRiskRulesProvider(pA)}
	recA, _ := sc.run(t, "qt66-risk-a")

	assert.Equal(t, hashA, recA.RiskRulesHash, "接入了 provider，记录必须是它给的 hash")
	assert.Equal(t, 1, pA.calls, "每次 run 取一次（裁-10：不缓存到重启）")
	assert.Equal(t, []string{""}, pA.pinned, "当前 RunSpec 没有 pin 字段 ⇒ 恒走 latest")

	// 换规则快照 ⇒ 新 run 的 risk_rules_hash 与 run_spec_hash 都必须变。
	pB := &fakeRiskRulesProvider{hash: hashB}
	sc.writer = &provenanceCapture{}
	sc.engineOpts = []EngineOption{WithRiskRulesProvider(pB)}
	recB, _ := sc.run(t, "qt66-risk-b")

	assert.Equal(t, hashB, recB.RiskRulesHash)
	assert.NotEqual(t, recA.RunSpecHash, recB.RunSpecHash,
		"规则快照是 run_spec_hash 的分量：换快照必须换身份")
}

func TestQT66_NotConfiguredKeepsHashEmpty(t *testing.T) {
	// 结构腿：provider 未注入时 resolution 明确是 not_configured。
	eng, err := NewEngine(
		provenanceViper(42, fees.DefaultCommissionRate, fees.DefaultSlippageRate, fees.DefaultStampTaxRate),
		provenanceProvider(t, []string{"600000.SH"}, 20),
		zerolog.Nop(),
	)
	require.NoError(t, err)

	res, err := eng.resolveRiskRulesForRun(context.Background())
	require.NoError(t, err)
	assert.Equal(t, riskRulesStatusNotConfigured, res.Status)
	assert.Empty(t, res.Hash, "空串只允许出现在 not_configured")

	// 行为腿：整轮 run 照常完成，provenance 的该列为空。
	sc := provenanceBaseline()
	sc.writer = &provenanceCapture{}
	rec, resp := sc.run(t, "qt66-not-configured")
	require.Equal(t, StatusCompleted, resp.Status)
	assert.Empty(t, rec.RiskRulesHash)
}

func TestQT66_ProviderFailureFailsRunWithoutProvenance(t *testing.T) {
	cases := []struct {
		name     string
		provider *fakeRiskRulesProvider
	}{
		{"provider error", &fakeRiskRulesProvider{err: errors.New("no_approved_rules")}},
		{"empty hash", &fakeRiskRulesProvider{hash: "   "}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc := provenanceBaseline()
			resp, err, capture := qt66Attempt(t, sc, tc.provider)

			require.Error(t, err, "取不到规则 hash 必须让 run 失败（裁-11）")
			assert.True(t, errors.Is(err, ErrRiskRulesUnavailable),
				"错误必须可按 risk_rules_unavailable 分诊，实际：%v", err)
			require.NotNil(t, resp)
			assert.Equal(t, StatusFailed, resp.Status)
			assert.Contains(t, resp.Error, "risk_rules_unavailable")
			assert.Equal(t, 1, tc.provider.calls)
			assert.Zero(t, capture.calls, "取 hash 失败不得写半份 provenance")
		})
	}
}
