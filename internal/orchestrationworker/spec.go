package orchestrationworker

// run_spec 的解析：把编排侧的 RunSpec 折成 core 可执行的 BacktestRequest。
//
// 两种形状（docs/api-schema.md §1 与 analytics 的 interim 锚点）：
//   - 完整 RunSpec：strategy + start_date + end_date 都在，直接执行；
//   - 锚点 RunSpec：只带 `run_id`（analytics sensitivity 的 base_run_spec），
//     由 LoadBase 从 core 的 `backtest_jobs` 恢复基础请求，再用 `strategy_params`
//     等字段覆盖。
//
// 无论哪种，RunID 一律改写成本次编排 run 的 id：结果、provenance 与
// `GET /api/backtest/{id}` 才能与编排侧的 run 对齐。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ruoxizhnya/tellurion-contracts/engine"
)

// SpecResolver 把一条编排 run 的 run_spec 解成可执行请求。
type SpecResolver interface {
	Resolve(ctx context.Context, runID string, spec json.RawMessage) (contracts.BacktestRequest, error)
}

// BaseRunLoader 按 run_id 恢复 core 侧一条既有 run 的基础请求。
type BaseRunLoader func(ctx context.Context, runID string) (*BaseRun, error)

// AnchorResolver 支持完整 RunSpec 与 `run_id` 锚点两种形状。
type AnchorResolver struct {
	LoadBase BaseRunLoader
}

// specEnvelope 是 RunSpec 的超集（BacktestRequest 的全部字段 + run_id 锚点）。
type specEnvelope struct {
	RunID          string                   `json:"run_id"`
	Strategy       string                   `json:"strategy"`
	StockPool      []string                 `json:"stock_pool"`
	IndexCode      string                   `json:"index_code"`
	StartDate      string                   `json:"start_date"`
	EndDate        string                   `json:"end_date"`
	InitialCapital float64                  `json:"initial_capital"`
	RiskFreeRate   float64                  `json:"risk_free_rate"`
	Kernel         string                   `json:"kernel"`
	FillTiming     string                   `json:"fill_timing"`
	EngineParams   map[string]any           `json:"engine_params"`
	StrategyParams map[string]any           `json:"strategy_params"`
	StrategyRef    string                   `json:"strategy_ref"`
	Signals        []contracts.SignalWeight `json:"signals"`
}

// Resolve 见 SpecResolver。
func (r *AnchorResolver) Resolve(ctx context.Context, runID string, spec json.RawMessage) (contracts.BacktestRequest, error) {
	var env specEnvelope
	if err := json.Unmarshal(spec, &env); err != nil {
		return contracts.BacktestRequest{}, fmt.Errorf("run spec is not valid JSON: %w", err)
	}

	if env.Strategy != "" && env.StartDate != "" && env.EndDate != "" {
		req := contracts.BacktestRequest{
			Strategy:       env.Strategy,
			StockPool:      env.StockPool,
			IndexCode:      env.IndexCode,
			StartDate:      env.StartDate,
			EndDate:        env.EndDate,
			InitialCapital: env.InitialCapital,
			RiskFreeRate:   env.RiskFreeRate,
			Kernel:         env.Kernel,
			FillTiming:     env.FillTiming,
			EngineParams:   env.EngineParams,
			StrategyParams: env.StrategyParams,
			StrategyRef:    env.StrategyRef,
			Signals:        env.Signals,
			RunID:          runID,
		}
		if err := req.ValidateRunSpec(); err != nil {
			return contracts.BacktestRequest{}, fmt.Errorf("run spec is invalid: %w", err)
		}
		return req, nil
	}

	if env.RunID == "" {
		return contracts.BacktestRequest{}, fmt.Errorf(
			"run spec must be a full BacktestRequest (strategy + start_date + end_date) or carry run_id")
	}
	if r.LoadBase == nil {
		return contracts.BacktestRequest{}, fmt.Errorf("run spec %q is an anchor but no base loader is configured", env.RunID)
	}
	base, err := r.LoadBase(ctx, env.RunID)
	if err != nil {
		return contracts.BacktestRequest{}, fmt.Errorf("load base run %s: %w", env.RunID, err)
	}
	if base == nil {
		return contracts.BacktestRequest{}, fmt.Errorf("base run %s not found", env.RunID)
	}

	req := base.Request()
	// 覆盖只认显式给出的字段：空串 / 零值表示「沿用 base」。
	if env.Kernel != "" {
		req.Kernel = env.Kernel
	}
	if env.FillTiming != "" {
		req.FillTiming = env.FillTiming
	}
	if env.InitialCapital > 0 {
		req.InitialCapital = env.InitialCapital
	}
	if env.RiskFreeRate > 0 {
		req.RiskFreeRate = env.RiskFreeRate
	}
	if env.EngineParams != nil {
		req.EngineParams = env.EngineParams
	}
	if env.StrategyParams != nil {
		req.StrategyParams = env.StrategyParams
	}
	if env.StrategyRef != "" {
		req.StrategyRef = env.StrategyRef
	}
	if len(env.Signals) > 0 {
		req.Signals = env.Signals
	}
	req.RunID = runID
	if err := req.ValidateRunSpec(); err != nil {
		return contracts.BacktestRequest{}, fmt.Errorf("resolved run spec is invalid: %w", err)
	}
	return req, nil
}

// BaseRun 是 core `backtest_jobs` 行里可恢复的执行输入。
//
// 它不是「完整 RunSpec」：旧异步路径从未落过 fill_timing / seed / signals
// （已知缺口 F2 / R3），所以锚点恢复只能拿到这些字段的零值，由引擎按默认
// 口径解释。这条边界写在这里，避免读方以为锚点与完整 spec 等价。
type BaseRun struct {
	Strategy       string
	StockPool      []string
	StartDate      string
	EndDate        string
	InitialCapital float64
	Kernel         string
	EngineParams   map[string]any
}

// Request 把 BaseRun 折成 BacktestRequest（RunID 由调用方随后覆盖）。
func (b BaseRun) Request() contracts.BacktestRequest {
	return contracts.BacktestRequest{
		Strategy:       b.Strategy,
		StockPool:      append([]string(nil), b.StockPool...),
		StartDate:      b.StartDate,
		EndDate:        b.EndDate,
		InitialCapital: b.InitialCapital,
		Kernel:         b.Kernel,
		EngineParams:   b.EngineParams,
	}
}

// ParseBaseRun 把 storage.GetBacktestJob 的行折成 BaseRun。
//
// 保留键（initial_capital / kernel / engine_params）类型不对时当场拒绝：
// 与 core 旧异步路径的 fail-closed 同口径（「以为参数生效了」比报错更危险）。
func ParseBaseRun(row map[string]any) (*BaseRun, error) {
	str := func(key string) (string, error) {
		v, ok := row[key]
		if !ok || v == nil {
			return "", fmt.Errorf("backtest_jobs.%s is missing", key)
		}
		s, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("backtest_jobs.%s has unexpected type %T", key, v)
		}
		return s, nil
	}

	strategy, err := str("strategy_id")
	if err != nil {
		return nil, err
	}
	start, err := str("start_date")
	if err != nil {
		return nil, err
	}
	end, err := str("end_date")
	if err != nil {
		return nil, err
	}
	universe, err := str("universe")
	if err != nil {
		return nil, err
	}

	base := &BaseRun{Strategy: strategy, StartDate: start, EndDate: end, StockPool: parseUniverse(universe)}

	params, err := paramsOf(row["params"])
	if err != nil {
		return nil, err
	}
	if v, ok := params["initial_capital"]; ok {
		f, ok := v.(float64)
		if !ok {
			return nil, fmt.Errorf("params.initial_capital has unexpected type %T", v)
		}
		base.InitialCapital = f
	}
	if v, ok := params["kernel"]; ok {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("params.kernel has unexpected type %T", v)
		}
		base.Kernel = s
	}
	if v, ok := params["engine_params"]; ok {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("params.engine_params has unexpected type %T", v)
		}
		base.EngineParams = m
	}
	if base.Strategy == "" || base.StartDate == "" || base.EndDate == "" {
		return nil, fmt.Errorf("backtest job has an incomplete base spec (strategy/start_date/end_date)")
	}
	return base, nil
}

func paramsOf(v any) (map[string]any, error) {
	if v == nil {
		return map[string]any{}, nil
	}
	switch raw := v.(type) {
	case []byte:
		return decodeParams(raw)
	case string:
		return decodeParams([]byte(raw))
	case json.RawMessage:
		return decodeParams([]byte(raw))
	default:
		return nil, fmt.Errorf("backtest_jobs.params has unexpected type %T", v)
	}
}

func decodeParams(raw []byte) (map[string]any, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("backtest_jobs.params is not a JSON object: %w", err)
	}
	if out == nil {
		return map[string]any{}, nil
	}
	return out, nil
}

// parseUniverse 与 core 旧异步路径同口径：空串 / "all" = 全市场（nil）。
func parseUniverse(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "all" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if s := strings.TrimSpace(part); s != "" {
			out = append(out, s)
		}
	}
	return out
}
