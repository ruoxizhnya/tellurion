package orchestrationworker

// run_spec 的解析：把编排侧的 RunSpec 折成 core 可执行的 BacktestRequest。
//
// 两种形状（docs/api-schema.md §1 与 analytics 的 interim 锚点）：
//   - 完整 RunSpec：strategy + start_date + end_date 都在，直接执行；
//   - 锚点 RunSpec：只带 `run_id`（analytics sensitivity 的 base_run_spec），
//     由 LoadBase 从**编排任务面保存的 run_spec**（GET job）恢复基础请求，
//     再用 `strategy_params` 等字段覆盖。
//
// SVC-ORCH-G3 收尾腿（2026-11-09）：core 的 quant.backtest_jobs 退役后，base
// 的权威来源是任务面的 request_json —— 任务面本来就是 RunSpec 的保管人。
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

// EngineParamString 从 canonical RunSpec 的 engine_params 取一个字符串参数。
//
// SVC-ORCH-G3 收尾腿：基准覆盖（benchmark_index_code）以前从 backtest_jobs
// 的 params 里读，那条表退役后改从任务面的 run_spec 读；锚点 spec 的
// engine_params 就是覆盖块，所以这里直接读 envelope，不要求完整 RunSpec。
func EngineParamString(raw json.RawMessage, key string) string {
	if len(raw) == 0 {
		return ""
	}
	var env specEnvelope
	if err := json.Unmarshal(raw, &env); err != nil || env.EngineParams == nil {
		return ""
	}
	if s, ok := env.EngineParams[key].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// BaseRun 是从任务面 run_spec 可恢复的执行输入。
//
// 它不是「完整 RunSpec」：core 旧异步路径只保证 strategy / stock_pool /
// 区间 / initial_capital / kernel / engine_params 六项（fill_timing / seed /
// signals 曾是不落库的已知缺口 F2 / R3），所以锚点恢复只能拿到这些字段的
// 零值，由引擎按默认口径解释。这条边界写在这里，避免读方以为锚点与完整
// spec 等价。
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

// ParseBaseRunSpec 把任务面保存的 canonical RunSpec 折成 BaseRun。
//
// SVC-ORCH-G3 收尾腿（2026-11-09）：core 的 quant.backtest_jobs 退役后，锚点
// base 的唯一权威来源是编排任务面保存的 request_json（GET job 的 run_spec）。
// 只接受**完整** RunSpec（strategy + 区间）：只带 run_id 的 spec 不能当 base，
// 否则锚点会递归，且无法判定链尾。
func ParseBaseRunSpec(raw json.RawMessage) (*BaseRun, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("base run has no run_spec on the task face")
	}
	var env specEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("base run spec is not valid JSON: %w", err)
	}
	if env.Strategy == "" || env.StartDate == "" || env.EndDate == "" {
		return nil, fmt.Errorf("base run spec is not a full RunSpec (strategy + start_date + end_date required)")
	}
	return &BaseRun{
		Strategy:       env.Strategy,
		StockPool:      env.StockPool,
		StartDate:      env.StartDate,
		EndDate:        env.EndDate,
		InitialCapital: env.InitialCapital,
		Kernel:         env.Kernel,
		EngineParams:   env.EngineParams,
	}, nil
}
