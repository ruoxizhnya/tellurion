// CT-06-B (2026-11-10): BacktestParams 自 `tellurion-contracts:domain/backtest.go`
// 迁回（契约仓只有 core 一个使用方）。字段、JSON tag、纪律注释逐字保留。
package domain

import "time"

type BacktestParams struct {
	StrategyName   string    `json:"strategy_name"`
	StockPool      []string  `json:"stock_pool"`
	StartDate      time.Time `json:"start_date"`
	EndDate        time.Time `json:"end_date"`
	InitialCapital float64   `json:"initial_capital"`
	RiskFreeRate   float64   `json:"risk_free_rate"`
	LookbackDays   int       `json:"lookback_days"`
	// FillTiming 是本次运行**实际生效**的成交时点口径（QT-02 · contracts.
	// FillTimingSameClose / …NextOpen）。这里是裸 string 而不是 contracts 的
	// 命名类型：contracts import domain，反向依赖会成环。
	//
	// 取值由请求解析点写入（Engine.newBacktestState ← EffectiveFillTiming），
	// 日循环入口再严格校验一次（ResolveFillTiming）—— 引擎永远不猜：
	// 空值（含 QT-02 之前落盘的历史快照）不是「按收盘成交」而是「口径未知」，
	// 未知就不该产出结果。
	FillTiming string `json:"fill_timing,omitempty"`

	// Kernel 是本 run **实际执行**的内核名（QT-63 落 provenance 用）。
	// 与 FillTiming 同一条纪律：由请求解析点写入生效值（EffectiveKernel），
	// 引擎内部不猜。它不影响撮合，只进 run_spec_hash —— 换内核必须换 hash，
	// 否则两个不同实现的结果会顶着同一份输入身份。
	Kernel string `json:"kernel,omitempty"`

	// IndexCode 是请求里的指数代码（已归一化）。票池由它展开时，展开结果
	// 已经写进 StockPool；保留原值是为了让 run_spec_hash 记下「票池从哪来」，
	// 而不是只记一张看起来等价的名单。
	IndexCode string `json:"index_code,omitempty"`

	// Params 是本次运行**实际生效**的策略参数（QT-20）：manifest 的
	// params_defaults 与请求覆写浅合并后的结果，字段名对齐 core 的
	// RunMeta.Params。请求侧只写覆写（engine.BacktestRequest.StrategyParams），
	// 生效值由执行侧解析后写入这里 —— 与 FillTiming / Kernel 同一条纪律：
	// 引擎不猜，记录的是实际用过的值。
	//
	// 只承载浅层对象（与请求侧校验同口径）；嵌套会让 params_hash 的
	// 编码失去唯一定义。空/nil = 未提供参数（不是「空参数集」）。
	Params map[string]any `json:"params,omitempty"`
}
