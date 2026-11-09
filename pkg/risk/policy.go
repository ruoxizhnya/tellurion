package risk

// policy.go —— SVC-CORE-RISK-RETIRE（2026-11-09）的「限额真相源」唯一入口。
//
// 旧形态：`risk_manager.*`（14 个标量）来自本仓 config/analysis-service.yaml，
// 与 tellurion-risk 的规则快照是两套独立真相。新形态：
//
//	risk 服务 `POST /api/risk/resolve` → canonical Snapshot JSON
//	  → contracts/risk.Snapshot.Verify()（重新 derive hash，内容寻址）
//	  → PolicyFromSnapshot()（按规则类型严格取参数，未知键/未知类型拒收）
//	  → RiskManagerConfig
//
// 为什么不是 import risk 仓的 `rule.ParseBundle`：那是服务内部策略（带 ForScope
// 索引与默认值填充），按 CT-03 ④ 不迁契约；core 只消费**已冻结的形状与 hash**
// （`contracts/risk`）。参数键名与 `risk_manager.*` 是 1:1（risk 仓参数字典明文）。
//
// 失败口径：任何畸形（hash 不符、未知类型/scope、参数键不认识、缺某一类规则）
// 一律返回 error —— 装配期 fail-fast，不拿默认限额顶上（risk 仓
// docs/ARCHITECTURE.md §1 的同一句话）。

import (
	"bytes"
	"encoding/json"
	"fmt"

	contractsrisk "github.com/ruoxizhnya/tellurion-contracts/risk"
)

// Policy 是 core 进程内风控的实际输入 + 可观测来源。
type Policy struct {
	Config RiskManagerConfig
	// Source = "risk_snapshot"（来自 risk 服务）或 "builtin_default"（未接入）。
	Source string
	// RiskRulesHash 是快照的内容地址；builtin_default 时为空串。
	RiskRulesHash string
	// Scope 是被采用规则的适用范围（"global" 或 "strategy"）；builtin_default 时为空串。
	Scope contractsrisk.Scope
}

// DefaultRiskManagerConfig 是未接入 risk 服务时的出厂值（原 config 默认值）。
//
// SVC-CORE-RISK-RETIRE 之后 config/*.yaml **不再**承载这些限额；它们是代码常量，
// 只在 `risk_service.resolve_enabled=false` 的「未接入」部署里兜底，并在启动日志里
// 明示 source=builtin_default（fail-visible，不静默）。
func DefaultRiskManagerConfig() RiskManagerConfig {
	return RiskManagerConfig{
		TargetVolatility:    0.15,
		MaxPositionWeight:   0.10,
		MinPositionWeight:   0.01,
		ATRPeriod:           14,
		BaseMultiplier:      2.5,
		BullMultiplier:      2.0,
		BearMultiplier:      3.5,
		SidewaysMultiplier:  2.5,
		TakeProfitMult:      3.0,
		VolLookbackDays:     20,
		AnnualizationFactor: 16.0,
		FastMAPeriod:        10,
		SlowMAPeriod:        20,
		RegimeVolLookback:   60,
	}
}

// BuiltinPolicy 是「未接入 risk 服务」的显式形态。
func BuiltinPolicy() Policy {
	return Policy{Config: DefaultRiskManagerConfig(), Source: "builtin_default"}
}

// 参数字典（risk 仓 docs/params.md 的冻结键名，与 risk_manager.* 1:1）。
// 结构体字段集即白名单：DisallowUnknownFields ⇒ 词典多出键 = 消费方无从解释 ⇒ 拒。

type positionSizingParams struct {
	TargetVolatility    float64 `json:"target_volatility"`
	MaxPositionWeight   float64 `json:"max_position_weight"`
	MinPositionWeight   float64 `json:"min_position_weight"`
	LookbackDays        int     `json:"lookback_days"`
	AnnualizationFactor float64 `json:"annualization_factor"`
}

type stopLossParams struct {
	ATRPeriod          int     `json:"atr_period"`
	BaseMultiplier     float64 `json:"base_multiplier"`
	BullMultiplier     float64 `json:"bull_multiplier"`
	BearMultiplier     float64 `json:"bear_multiplier"`
	SidewaysMultiplier float64 `json:"sideways_multiplier"`
}

type takeProfitParams struct {
	ATRMultiplier float64 `json:"atr_multiplier"`
}

type regimeParams struct {
	FastMAPeriod int `json:"fast_ma_period"`
	SlowMAPeriod int `json:"slow_ma_period"`
	VolLookback  int `json:"vol_lookback"`
}

// PolicyFromSnapshot 把 canonical Snapshot JSON 转成进程内风控配置。
//
// 步骤与失败口径：
//  1. 严格解码（未知顶层字段拒收）→ `contractsrisk.Snapshot`；
//  2. `Verify()` 重新 derive hash 与声称值比对（内容寻址被破坏即拒）；
//  3. 选 scope：优先 global；没有 global 规则时退回 strategy；两者皆无 ⇒ 拒；
//  4. 在选中 scope 内，四类规则各**恰好一条**；缺一类或同类多条 ⇒ 拒（歧义不猜）；
//  5. 每类 params 按冻结键名严格解码；未知键 ⇒ 拒。
func PolicyFromSnapshot(data []byte) (Policy, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var snap contractsrisk.Snapshot
	if err := dec.Decode(&snap); err != nil {
		return Policy{}, fmt.Errorf("risk policy: snapshot is not valid JSON: %w", err)
	}
	if dec.More() {
		return Policy{}, fmt.Errorf("risk policy: snapshot has trailing content")
	}
	if err := snap.Verify(); err != nil {
		return Policy{}, fmt.Errorf("risk policy: %w", err)
	}

	scope := pickScope(snap.Rules)
	if scope == "" {
		return Policy{}, fmt.Errorf("risk policy: snapshot %s has no global/strategy rules (account/symbol scopes are not a process-wide policy source)",
			snap.RiskRulesHash)
	}

	var (
		sizing     positionSizingParams
		stopLoss   stopLossParams
		takeProfit takeProfitParams
		regime     regimeParams
		seen       = map[contractsrisk.RuleType]bool{}
	)
	for _, r := range snap.Rules {
		if r.Scope != scope {
			continue
		}
		if seen[r.Type] {
			return Policy{}, fmt.Errorf("risk policy: snapshot %s has more than one %s rule in scope %q — ambiguous process-wide policy",
				snap.RiskRulesHash, r.Type, scope)
		}
		seen[r.Type] = true
		var err error
		switch r.Type {
		case contractsrisk.RuleTypePositionSizing:
			err = decodeParams(r, &sizing)
		case contractsrisk.RuleTypeStopLoss:
			err = decodeParams(r, &stopLoss)
		case contractsrisk.RuleTypeTakeProfit:
			err = decodeParams(r, &takeProfit)
		case contractsrisk.RuleTypeRegime:
			err = decodeParams(r, &regime)
		default:
			// Snapshot.Verify 已拒未知类型；这里只是防御式穷尽。
			err = fmt.Errorf("unknown rule type %q", r.Type)
		}
		if err != nil {
			return Policy{}, err
		}
	}

	for _, t := range []contractsrisk.RuleType{
		contractsrisk.RuleTypePositionSizing,
		contractsrisk.RuleTypeStopLoss,
		contractsrisk.RuleTypeTakeProfit,
		contractsrisk.RuleTypeRegime,
	} {
		if !seen[t] {
			return Policy{}, fmt.Errorf("risk policy: snapshot %s has no %s rule in scope %q — 缺规则就是没有规则，不回落默认限额",
				snap.RiskRulesHash, t, scope)
		}
	}

	p := Policy{
		Config: RiskManagerConfig{
			TargetVolatility:    sizing.TargetVolatility,
			MaxPositionWeight:   sizing.MaxPositionWeight,
			MinPositionWeight:   sizing.MinPositionWeight,
			VolLookbackDays:     sizing.LookbackDays,
			AnnualizationFactor: sizing.AnnualizationFactor,
			ATRPeriod:           stopLoss.ATRPeriod,
			BaseMultiplier:      stopLoss.BaseMultiplier,
			BullMultiplier:      stopLoss.BullMultiplier,
			BearMultiplier:      stopLoss.BearMultiplier,
			SidewaysMultiplier:  stopLoss.SidewaysMultiplier,
			TakeProfitMult:      takeProfit.ATRMultiplier,
			FastMAPeriod:        regime.FastMAPeriod,
			SlowMAPeriod:        regime.SlowMAPeriod,
			RegimeVolLookback:   regime.VolLookback,
		},
		Source:        "risk_snapshot",
		RiskRulesHash: snap.RiskRulesHash,
		Scope:         scope,
	}
	if err := p.Validate(); err != nil {
		return Policy{}, err
	}
	return p, nil
}

// Validate 是装配期的最小自洽检查：只挡「明显不可能正确」的取值。
//
// 刻意**不**重校 risk 仓的取值域：历史快照按当年词典写入，用今天的域卡它会
// 让历史 hash 不可读（risk 仓 ParseBundle 的同一裁决）。这里只保证除零/负仓位
// /逆序均线这类必然算出坏结果的配置不进入热路径。
func (p Policy) Validate() error {
	c := p.Config
	checks := []struct {
		name string
		ok   bool
		msg  string
	}{
		{"target_volatility > 0", c.TargetVolatility > 0, "target_volatility 必须为正"},
		{"max_position_weight > 0", c.MaxPositionWeight > 0, "max_position_weight 必须为正"},
		{"min_position_weight >= 0", c.MinPositionWeight >= 0, "min_position_weight 不能为负"},
		{"min <= max", c.MinPositionWeight <= c.MaxPositionWeight, "min_position_weight 不能大于 max_position_weight"},
		{"atr_period > 0", c.ATRPeriod > 0, "atr_period 必须为正"},
		{"base_multiplier > 0", c.BaseMultiplier > 0, "base_multiplier 必须为正"},
		{"bull_multiplier > 0", c.BullMultiplier > 0, "bull_multiplier 必须为正"},
		{"bear_multiplier > 0", c.BearMultiplier > 0, "bear_multiplier 必须为正"},
		{"sideways_multiplier > 0", c.SidewaysMultiplier > 0, "sideways_multiplier 必须为正"},
		{"atr_multiplier > 0", c.TakeProfitMult > 0, "take_profit.atr_multiplier 必须为正"},
		{"lookback_days > 0", c.VolLookbackDays > 0, "volatility.lookback_days 必须为正"},
		{"annualization_factor > 0", c.AnnualizationFactor > 0, "annualization_factor 必须为正"},
		{"fast_ma_period > 0", c.FastMAPeriod > 0, "regime.fast_ma_period 必须为正"},
		{"slow_ma_period > fast", c.SlowMAPeriod > c.FastMAPeriod, "regime.slow_ma_period 必须大于 fast_ma_period"},
		{"vol_lookback > 0", c.RegimeVolLookback > 0, "regime.vol_lookback 必须为正"},
	}
	for _, chk := range checks {
		if !chk.ok {
			return fmt.Errorf("risk policy (%s, hash=%s): %s", p.Source, p.RiskRulesHash, chk.msg)
		}
	}
	return nil
}

// pickScope 返回进程级风控采用的规则 scope。
//
// 进程内 RiskManager 只有一个配置，因此只接受「全局」或「策略级」两档：
// global 存在就用 global；否则 strategy（典型部署只发布策略级规则）；
// 只有 account/symbol 规则时不猜——返回空串由调用方拒。
func pickScope(rules []contractsrisk.SnapshotRule) contractsrisk.Scope {
	hasGlobal, hasStrategy := false, false
	for _, r := range rules {
		switch r.Scope {
		case contractsrisk.ScopeGlobal:
			hasGlobal = true
		case contractsrisk.ScopeStrategy:
			hasStrategy = true
		}
	}
	switch {
	case hasGlobal:
		return contractsrisk.ScopeGlobal
	case hasStrategy:
		return contractsrisk.ScopeStrategy
	default:
		return ""
	}
}

// decodeParams 严格解码一类规则的 params：未知键即拒（与 risk 仓参数字典同口径）。
func decodeParams(rule contractsrisk.SnapshotRule, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(rule.Params))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("risk policy: rule %s (%s) params: %w", rule.RuleID, rule.Type, err)
	}
	if dec.More() {
		return fmt.Errorf("risk policy: rule %s (%s) params has trailing content", rule.RuleID, rule.Type)
	}
	return nil
}

// ParseSnapshotHash 校验 64 位小写 hex（与 risk 仓 DDL CHECK 同口径）。
func ParseSnapshotHash(s string) error {
	if len(s) != 64 {
		return fmt.Errorf("risk policy: risk_rules_hash %q is not 64 lowercase hex", s)
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("risk policy: risk_rules_hash %q is not 64 lowercase hex", s)
		}
	}
	return nil
}

// String 把来源折成一行日志用文本（不含参数值）。
func (p Policy) String() string {
	if p.RiskRulesHash == "" {
		return p.Source
	}
	return p.Source + ":" + string(p.Scope) + ":" + p.RiskRulesHash
}
