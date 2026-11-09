package risk

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	contractsrisk "github.com/ruoxizhnya/tellurion-contracts/risk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SVC-CORE-RISK-RETIRE（2026-11-09）：限额真相源 = risk 服务下发的快照。
// 这组用例钉住「快照 → RiskManagerConfig」的换算口径与全部失败分支。

const (
	sizingParamsJSON = `{"target_volatility":0.15,"max_position_weight":0.10,` +
		`"min_position_weight":0.01,"lookback_days":20,"annualization_factor":16.0}`
	stopLossParamsJSON = `{"atr_period":14,"base_multiplier":2.5,"bull_multiplier":2.0,` +
		`"bear_multiplier":3.5,"sideways_multiplier":2.5}`
	takeProfitParamsJSON = `{"atr_multiplier":3.0}`
	regimeParamsJSON     = `{"fast_ma_period":10,"slow_ma_period":20,"vol_lookback":60}`
)

type ruleOpt func(*contractsrisk.SnapshotRule)

func withParams(raw string) ruleOpt {
	return func(r *contractsrisk.SnapshotRule) { r.Params = json.RawMessage(raw) }
}

func withScope(s contractsrisk.Scope) ruleOpt {
	return func(r *contractsrisk.SnapshotRule) { r.Scope = s }
}

func rule(id string, t contractsrisk.RuleType, opts ...ruleOpt) contractsrisk.SnapshotRule {
	r := contractsrisk.SnapshotRule{
		RuleID:      id,
		Revision:    1,
		Scope:       contractsrisk.ScopeStrategy,
		Type:        t,
		VersionHash: "vh-" + id,
	}
	for _, o := range opts {
		o(&r)
	}
	return r
}

// fourRules 是四类规则各一条的完整集合（最小可用的进程级风控来源）。
func fourRules(scope contractsrisk.Scope) []contractsrisk.SnapshotRule {
	return []contractsrisk.SnapshotRule{
		rule("rs-sizing", contractsrisk.RuleTypePositionSizing, withScope(scope), withParams(sizingParamsJSON)),
		rule("rs-stop", contractsrisk.RuleTypeStopLoss, withScope(scope), withParams(stopLossParamsJSON)),
		rule("rs-take", contractsrisk.RuleTypeTakeProfit, withScope(scope), withParams(takeProfitParamsJSON)),
		rule("rs-regime", contractsrisk.RuleTypeRegime, withScope(scope), withParams(regimeParamsJSON)),
	}
}

// snapshotJSON 用契约自己的 Hash() 生成**自洽**的快照字节（内容寻址）。
func snapshotJSON(t *testing.T, rules []contractsrisk.SnapshotRule) []byte {
	t.Helper()
	snap := contractsrisk.Snapshot{
		Rules:     rules,
		CreatedAt: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC),
	}
	hash, err := snap.Hash()
	require.NoError(t, err, "fixture must be hashable")
	snap.RiskRulesHash = hash
	raw, err := json.Marshal(snap)
	require.NoError(t, err)
	return raw
}

func TestPolicyFromSnapshot_StrategyScope(t *testing.T) {
	p, err := PolicyFromSnapshot(snapshotJSON(t, fourRules(contractsrisk.ScopeStrategy)))
	require.NoError(t, err)

	assert.Equal(t, "risk_snapshot", p.Source)
	assert.Equal(t, contractsrisk.ScopeStrategy, p.Scope)
	assert.Len(t, p.RiskRulesHash, 64, "hash 必须原样带出（run provenance 的锚点）")

	want := DefaultRiskManagerConfig()
	assert.Equal(t, want, p.Config, "fixture 用的就是出厂值，换算口径必须逐字段一致")

	// 逐项点一遍映射（防止只改 DefaultRiskManagerConfig 就让上面的相等断言变绿）。
	assert.InDelta(t, 0.15, p.Config.TargetVolatility, 1e-12)
	assert.InDelta(t, 0.10, p.Config.MaxPositionWeight, 1e-12)
	assert.InDelta(t, 0.01, p.Config.MinPositionWeight, 1e-12)
	assert.Equal(t, 14, p.Config.ATRPeriod)
	assert.InDelta(t, 2.5, p.Config.BaseMultiplier, 1e-12)
	assert.InDelta(t, 2.0, p.Config.BullMultiplier, 1e-12)
	assert.InDelta(t, 3.5, p.Config.BearMultiplier, 1e-12)
	assert.InDelta(t, 2.5, p.Config.SidewaysMultiplier, 1e-12)
	assert.InDelta(t, 3.0, p.Config.TakeProfitMult, 1e-12)
	assert.Equal(t, 20, p.Config.VolLookbackDays)
	assert.InDelta(t, 16.0, p.Config.AnnualizationFactor, 1e-12)
	assert.Equal(t, 10, p.Config.FastMAPeriod)
	assert.Equal(t, 20, p.Config.SlowMAPeriod)
	assert.Equal(t, 60, p.Config.RegimeVolLookback)

	assert.Contains(t, p.String(), "risk_snapshot:strategy:")
}

func TestPolicyFromSnapshot_PrefersGlobal(t *testing.T) {
	rules := fourRules(contractsrisk.ScopeGlobal)
	// 同类型的 strategy 副本必须被忽略（global 优先），值故意写成别的一组。
	rules = append(rules,
		rule("str-sizing", contractsrisk.RuleTypePositionSizing,
			withScope(contractsrisk.ScopeStrategy),
			withParams(`{"target_volatility":0.30,"max_position_weight":0.50,`+
				`"min_position_weight":0.02,"lookback_days":60,"annualization_factor":16.0}`)),
	)
	p, err := PolicyFromSnapshot(snapshotJSON(t, rules))
	require.NoError(t, err)
	assert.Equal(t, contractsrisk.ScopeGlobal, p.Scope)
	assert.InDelta(t, 0.10, p.Config.MaxPositionWeight, 1e-12, "global 的 sizing 获胜")
}

func TestPolicyFromSnapshot_RejectsBrokenContentAddress(t *testing.T) {
	raw := snapshotJSON(t, fourRules(contractsrisk.ScopeStrategy))

	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	m["risk_rules_hash"] = strings.Repeat("0", 64)
	tampered, err := json.Marshal(m)
	require.NoError(t, err)

	_, err = PolicyFromSnapshot(tampered)
	require.Error(t, err, "声称的 hash 与内容不符必须拒收")
	assert.Contains(t, err.Error(), "risk policy:")
}

func TestPolicyFromSnapshot_RejectsUnknownRuleType(t *testing.T) {
	// Verify 会先拒未知类型（contracts/risk 的冻结词表）。
	bogus := `{"risk_rules_hash":"` + strings.Repeat("a", 64) + `","created_at":"2026-11-09T12:00:00Z",` +
		`"rules":[{"rule_id":"x","revision":1,"scope":"strategy","type":"leverage",` +
		`"params":{"max_leverage":3},"version_hash":"vh-x"}]}`

	_, err := PolicyFromSnapshot([]byte(bogus))
	require.Error(t, err)
}

func TestPolicyFromSnapshot_RejectsMissingRuleType(t *testing.T) {
	rules := fourRules(contractsrisk.ScopeStrategy)
	// 去掉 regime：缺规则 = 没有规则，不许回落默认限额。
	rules = rules[:3]
	_, err := PolicyFromSnapshot(snapshotJSON(t, rules))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "regime")
}

func TestPolicyFromSnapshot_RejectsDuplicateRuleType(t *testing.T) {
	rules := append(fourRules(contractsrisk.ScopeStrategy),
		rule("rs-sizing-2", contractsrisk.RuleTypePositionSizing,
			withScope(contractsrisk.ScopeStrategy), withParams(sizingParamsJSON)))
	_, err := PolicyFromSnapshot(snapshotJSON(t, rules))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "more than one")
}

func TestPolicyFromSnapshot_RejectsUnknownParamKey(t *testing.T) {
	rules := []contractsrisk.SnapshotRule{
		rule("rs-sizing", contractsrisk.RuleTypePositionSizing, withParams(
			`{"target_volatility":0.15,"max_position_weight":0.10,"min_position_weight":0.01,`+
				`"lookback_days":20,"annualization_factor":16.0,"max_pct":10}`)),
		rule("rs-stop", contractsrisk.RuleTypeStopLoss, withParams(stopLossParamsJSON)),
		rule("rs-take", contractsrisk.RuleTypeTakeProfit, withParams(takeProfitParamsJSON)),
		rule("rs-regime", contractsrisk.RuleTypeRegime, withParams(regimeParamsJSON)),
	}
	_, err := PolicyFromSnapshot(snapshotJSON(t, rules))
	require.Error(t, err, "词典外的键 = 消费方无从解释 ⇒ 拒收")
	assert.Contains(t, err.Error(), "max_pct")
}

func TestPolicyFromSnapshot_RejectsScopeWithoutGlobalOrStrategy(t *testing.T) {
	rules := fourRules(contractsrisk.ScopeAccount)
	_, err := PolicyFromSnapshot(snapshotJSON(t, rules))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no global/strategy")
}

func TestPolicyFromSnapshot_RejectsInvalidValues(t *testing.T) {
	rules := []contractsrisk.SnapshotRule{
		rule("rs-sizing", contractsrisk.RuleTypePositionSizing, withParams(sizingParamsJSON)),
		rule("rs-stop", contractsrisk.RuleTypeStopLoss, withParams(stopLossParamsJSON)),
		rule("rs-take", contractsrisk.RuleTypeTakeProfit, withParams(takeProfitParamsJSON)),
		// fast == slow：逆序/相等均线必然算出坏状态。
		rule("rs-regime", contractsrisk.RuleTypeRegime, withParams(
			`{"fast_ma_period":20,"slow_ma_period":20,"vol_lookback":60}`)),
	}
	_, err := PolicyFromSnapshot(snapshotJSON(t, rules))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "slow_ma_period")
}

func TestBuiltinPolicy_MatchesFactoryDefaults(t *testing.T) {
	p := BuiltinPolicy()
	assert.Equal(t, "builtin_default", p.Source)
	assert.Empty(t, p.RiskRulesHash)
	assert.Empty(t, p.Scope)
	assert.NoError(t, p.Validate())
	assert.Equal(t, DefaultRiskManagerConfig(), p.Config)
	assert.Equal(t, "builtin_default", p.String())
}

func TestParseSnapshotHash(t *testing.T) {
	require.NoError(t, ParseSnapshotHash(strings.Repeat("a", 64)))
	require.NoError(t, ParseSnapshotHash("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"))

	require.Error(t, ParseSnapshotHash(""), "空串不是合法 hash")
	require.Error(t, ParseSnapshotHash(strings.Repeat("a", 63)), "长度不足")
	require.Error(t, ParseSnapshotHash(strings.Repeat("A", 64)), "大写不合法（与 risk DDL CHECK 同口径）")
	require.Error(t, ParseSnapshotHash(strings.Repeat("g", 64)), "非 hex")
}
