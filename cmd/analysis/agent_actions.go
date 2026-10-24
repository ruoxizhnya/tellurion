package main

// agent_actions.go —— agent ↔ core 的**粗粒度动作**清单（QT-50 ②③）。
//
// 为什么要有这张表：D24 允许 agent 经 HTTP 调用 core，但只允许六类粗粒度
// 动作（提交回测 / 批量回测 / 因子求值查询 / 行情查询 / 策略定义拉取 /
// 因子登记），**禁止** per-bar / per-symbol / per-order 粒度。允许清单若只
// 写在设计文档里，下一个人新增一条 `/api/ohlcv/{symbol}/bar` 时没有任何
// 机械判据会红。
//
// 本表是**唯一声明处**：openapi.yaml 里对应 operation 上的
// `x-agent-coarse-action: <name>` 由 agent_coarse_actions_test.go 与本表双向
// 对拍 —— 少一条（代码声明了、yaml 没登记）红，多一条（yaml 标了、这里没
// 声明）也红。粒度判据（禁通配、禁 per-bar/per-order 路径参数）同在那个测试里。
//
// 第 7 类「交易日历 / 上市窗口查询」**已由用户裁-04 于 2026-10-24 确认**，
// 落两条只读端点（QT-67）：日历按区间查、上市窗口按标的集合查。一条动作
// 对应一个 operation，所以第 7 类让本表从 6 条变成 8 条 —— 别把「类数」
// 当成「条目数」。

import "net/http"

// agentCoarseAction 是一条被允许的 agent → core 粗粒度动作。
type agentCoarseAction struct {
	// Name 是动作标识（稳定），同时是 openapi operation 上
	// `x-agent-coarse-action` 的取值。
	Name string
	// Method / Route 是 gin 路由形状（参数用 `:x`，测试负责换成 openapi 的 `{x}`）。
	Method string
	Route  string
	// MinRole 是**文档口径**的最低角色；真正的执法在
	// cmd/analysis/route_policy.go（authz.Middleware 按 FULLPATH 查表）。
	MinRole string
	// Rationale 写清「为什么这是粗粒度」——粒度判据不是名字看起来像，
	// 而是一次请求承载一整批工作。
	Rationale string
}

// agentCoarseActions 是 D24 的六类动作。顺序与 D24 原文一致，便于对读。
var agentCoarseActions = []agentCoarseAction{
	{
		Name:      "submit_backtest",
		Method:    http.MethodPost,
		Route:     "/api/backtest",
		MinRole:   "trader",
		Rationale: "一次请求提交一个 RunSpec（universe 在 core 内部展开），进出都是作业/摘要，不是逐 bar",
	},
	{
		Name:      "submit_batch_backtest",
		Method:    http.MethodPost,
		Route:     "/api/batch",
		MinRole:   "trader",
		Rationale: "一次请求提交一批参数组合，返回批量作业句柄；参数展开在 core 进程内完成",
	},
	{
		Name:      "query_factor_evaluation",
		Method:    http.MethodGet,
		Route:     "/api/factor/returns/:factor",
		MinRole:   "any-authenticated（GET 未认证拒绝）",
		Rationale: "按日期区间拉因子收益时间序列；单次调用覆盖整个区间，不对每个交易日发一次请求",
	},
	{
		Name:      "query_market_data",
		Method:    http.MethodPost,
		Route:     "/api/v1/ohlcv/bulk",
		MinRole:   "readonly-post（任意已认证）",
		Rationale: "多 symbol × 日期区间在**一次**请求内取回（POST 只因为参数是集合）；刻意不用 per-symbol 的 GET /api/ohlcv/{symbol}",
	},
	{
		Name:      "fetch_strategy_definitions",
		Method:    http.MethodGet,
		Route:     "/api/strategies",
		MinRole:   "any-authenticated（GET 未认证拒绝）",
		Rationale: "拉取策略定义清单/详情；执行与逐 bar 信号永远留在 core 进程内",
	},
	{
		Name:      "register_factor",
		Method:    http.MethodPost,
		Route:     "/api/factor/registrations",
		MinRole:   "admin",
		Rationale: "一次请求登记一版因子定义（四样校验 + 版本哈希在 core 侧派生），写的是治理账本不是行情",
	},
	// ── 第 7 类（2026-10-24 裁-04）：交易日历 / 上市窗口查询，两条只读端点 ──
	{
		Name:      "query_trading_calendar",
		Method:    http.MethodGet,
		Route:     "/api/v1/trading-calendar",
		MinRole:   "any-authenticated（GET 未认证拒绝）",
		Rationale: "一次请求返回整个 [start, end] 区间，不按交易日逐次调用；只读、不写表",
	},
	{
		Name:      "query_listing_windows",
		Method:    http.MethodPost,
		Route:     "/api/v1/listing-windows",
		MinRole:   "readonly-post（任意已认证）",
		Rationale: "一次请求取回整批 symbol 的上市/摘牌窗口（symbols 为空即全部）；POST 只因参数是集合，不是 per-symbol 端点",
	},
}
