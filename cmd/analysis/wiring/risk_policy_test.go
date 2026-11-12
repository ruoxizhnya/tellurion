package wiring

// SVC-CORE-RISK-RETIRE（2026-11-09）：限额装配入口的行为契约。
//
// 三件事必须钉住：
//  1. `resolve_enabled=false` = 未接入 ⇒ 代码常量兜底（并留 WARN），不静默。
//  2. `resolve_enabled=true` ⇒ 真的从 `POST /api/risk/resolve` 取快照，
//     14 个限额按参数字典换算；取不到即 error（不拿默认值顶上）。
//  3. 响应自称的 hash 必须与快照内容自洽的 hash 一致（内容寻址被破坏即拒）。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	contractsrisk "github.com/ruoxizhnya/tellurion-contracts/risk"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/risk"
)

const policySizingParams = `{"target_volatility":0.18,"max_position_weight":0.08,` +
	`"min_position_weight":0.01,"lookback_days":25,"annualization_factor":16.0}`
const policyStopParams = `{"atr_period":10,"base_multiplier":2.0,"bull_multiplier":1.5,` +
	`"bear_multiplier":4.0,"sideways_multiplier":2.0}`
const policyTakeParams = `{"atr_multiplier":2.5}`
const policyRegimeParams = `{"fast_ma_period":5,"slow_ma_period":30,"vol_lookback":40}`

func policyRule(id string, typ contractsrisk.RuleType, params string) contractsrisk.SnapshotRule {
	return contractsrisk.SnapshotRule{
		RuleID:      id,
		Revision:    3,
		Scope:       contractsrisk.ScopeStrategy,
		Type:        typ,
		Params:      json.RawMessage(params),
		VersionHash: "vh-" + id,
	}
}

// policySnapshotBytes 生成自洽（hash 由内容派生）的 Snapshot JSON。
func policySnapshotBytes(t *testing.T) (string, []byte) {
	t.Helper()
	snap := contractsrisk.Snapshot{
		Rules: []contractsrisk.SnapshotRule{
			policyRule("p-sizing", contractsrisk.RuleTypePositionSizing, policySizingParams),
			policyRule("p-stop", contractsrisk.RuleTypeStopLoss, policyStopParams),
			policyRule("p-take", contractsrisk.RuleTypeTakeProfit, policyTakeParams),
			policyRule("p-regime", contractsrisk.RuleTypeRegime, policyRegimeParams),
		},
		CreatedAt: time.Date(2026, 11, 9, 8, 30, 0, 0, time.UTC),
	}
	hash, err := snap.Hash()
	require.NoError(t, err)
	snap.RiskRulesHash = hash
	raw, err := json.Marshal(snap)
	require.NoError(t, err)
	return hash, raw
}

func policyViper(enabled bool, url string) *viper.Viper {
	v := viper.New()
	v.Set("risk_service.resolve_enabled", enabled)
	v.Set("risk_service.resolve_url", url)
	v.Set("risk_service.resolve_timeout", "5s")
	return v
}

func TestLoadRiskPolicy_DisabledUsesBuiltin(t *testing.T) {
	var buf bytes.Buffer
	logger := zerolog.New(&buf)

	p, err := LoadRiskPolicy(policyViper(false, "http://127.0.0.1:1"), logger)
	require.NoError(t, err)
	assert.Equal(t, "builtin_default", p.Source)
	assert.Empty(t, p.RiskRulesHash)
	assert.Equal(t, risk.DefaultRiskManagerConfig(), p.Config)

	logs := buf.String()
	assert.Contains(t, logs, "risk_policy_source")
	assert.Contains(t, logs, "builtin_default", "兜底必须在启动日志里留痕（fail-visible）")
}

func TestLoadRiskPolicy_LoadsSnapshot(t *testing.T) {
	hash, snapshot := policySnapshotBytes(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, riskResolvePath, r.URL.Path)
		body, _ := json.Marshal(riskResolveResponse{RiskRulesHash: hash, Snapshot: snapshot})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	p, err := LoadRiskPolicy(policyViper(true, srv.URL), zerolog.Nop())
	require.NoError(t, err)
	assert.Equal(t, "risk_snapshot", p.Source)
	assert.Equal(t, hash, p.RiskRulesHash)
	assert.Equal(t, contractsrisk.ScopeStrategy, p.Scope)

	// 换算口径逐项（数字故意与出厂值不同，防止「读到却没用」）。
	assert.InDelta(t, 0.18, p.Config.TargetVolatility, 1e-12)
	assert.InDelta(t, 0.08, p.Config.MaxPositionWeight, 1e-12)
	assert.Equal(t, 25, p.Config.VolLookbackDays)
	assert.Equal(t, 10, p.Config.ATRPeriod)
	assert.InDelta(t, 4.0, p.Config.BearMultiplier, 1e-12)
	assert.InDelta(t, 2.5, p.Config.TakeProfitMult, 1e-12)
	assert.Equal(t, 5, p.Config.FastMAPeriod)
	assert.Equal(t, 30, p.Config.SlowMAPeriod)
	assert.Equal(t, 40, p.Config.RegimeVolLookback)
}

func TestLoadRiskPolicy_EnabledRequiresResolveURL(t *testing.T) {
	_, err := LoadRiskPolicy(policyViper(true, ""), zerolog.Nop())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolve_url")
}

func TestLoadRiskPolicy_ResolveFailureIsFatal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"no_approved_rules","error":"no approved rules"}`))
	}))
	defer srv.Close()

	_, err := LoadRiskPolicy(policyViper(true, srv.URL), zerolog.Nop())
	require.Error(t, err, "取不到快照必须失败，不许退回默认限额")
	assert.Contains(t, err.Error(), "no_approved_rules")
}

// TestLoadRiskPolicy_AuthUnavailableIsActionable 钉住「503 必须是可操作错误」：
// 文案要同时给出两条出路（配 secret + 注入 resolve_token / risk 显式声明
// loopback-published），否则运维只看到一次莫名其妙的启动失败。
func TestLoadRiskPolicy_AuthUnavailableIsActionable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"code":"auth_unavailable","error":"authentication is not configured"}`))
	}))
	defer srv.Close()

	_, err := LoadRiskPolicy(policyViper(true, srv.URL), zerolog.Nop())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth_unavailable")
	assert.Contains(t, err.Error(), "JWT_SECRET")
	assert.Contains(t, err.Error(), "resolve_token")
	assert.Contains(t, err.Error(), "AUTH_INSECURE_EXPOSURE=loopback-published")
}

func TestLoadRiskPolicy_RejectsHashMismatch(t *testing.T) {
	hash, snapshot := policySnapshotBytes(t)
	other := strings.Repeat("f", 64)
	require.NotEqual(t, hash, other)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := json.Marshal(riskResolveResponse{RiskRulesHash: other, Snapshot: snapshot})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	_, err := LoadRiskPolicy(policyViper(true, srv.URL), zerolog.Nop())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "!=", "响应自称的 hash 与快照自洽的 hash 不一致必须拒")
}

func TestLoadRiskPolicy_RejectsMissingSnapshot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 只有 hash、没有 snapshot 本体：QT-66 的 hash 端点形状，不足以装配限额。
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"risk_rules_hash":"` + qt66Hash + `"}`))
	}))
	defer srv.Close()

	_, err := LoadRiskPolicy(policyViper(true, srv.URL), zerolog.Nop())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no snapshot")
}
