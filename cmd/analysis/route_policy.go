package main

// REV-17：analysis-service 的「端点 × 最低角色」明文表。
//
// 规则（2026-10-16 主任裁决）：**所有 POST/PUT/PATCH/DELETE 至少 trader；
// 管理面 admin**；登录类三条是 Public。2026-10-25 裁-17 追加第五档
// Service：服务间凭证（`service` 角色）只能落在这一档，且与 trader/admin
// 并列放行——它不是更高角色，也永远不进交易/管理面。
//
// 为什么逐条列而不是按前缀批量生成：批量规则（例如「/api/plugins/* 都 admin」）
// 在新端点落进来时会**自动放行**，等于没有表态。逐条列 + 双向守卫测试
// （route_policy_test.go）才能让「新加了一个变更端点但没决定谁能用」变成红灯。
//
// 反代族（/api/copilot、/api/explore、/api/pipeline、/api/tools、/api/sync 的
// `*path` 与裸路径）也在表里：它们是**写穿到上游**的入口，上游没有独立的鉴权面
// （ai-service 由 analysis 网关保护、data-service 的写门在 REV-17 里补齐同款
// 策略），所以网关这一层必须按变更端点对待。

import "tellurion/pkg/authz"

var analysisRoutePolicy = authz.Policy{
	// ── Public：无 token 可达的入口（同时在 pkg/auth 的 publicPaths 白名单里）──
	Public: []string{
		"POST /api/auth/login",     // 登录：拿 token 的前提
		"POST /api/auth/refresh",   // 刷新：access token 过期后的唯一出路
		"POST /api/auth/bootstrap", // 首个管理员自助引导（窗口由 users 表是否为空决定）
	},

	// ── Admin：管理面 ──
	Admin: []string{
		"POST /api/auth/admin/users", // 建用户 = 发放访问权本身

		// 插件族（REV-08 已挂 admin，这里只是把同一条规则写进矩阵，
		// 让「矩阵」成为唯一可引用的规则来源）：
		// .so 的 init 代码在进程内执行，且 load 的路径还受 plugins.directory 约束。
		"POST /api/plugins/load",
		"POST /api/plugins/reload",
		"POST /api/plugins/load-all",
		"POST /api/plugins/watch-dir", // 改写路径校验的锚点
		"POST /api/plugins/:name/unload",
		"POST /api/plugins/:name/reload",

		// QT-50 ③ 第 6 类粗粒度动作：因子登记。改的是「后续所有回测能引用
		// 哪些因子定义」的治理账本，属管理面（登记人取自 JWT，不接受自报）。
		"POST /api/factor/registrations",

		// B-05：因子分析重算。一次请求会对全票池跑横截面计算并**覆盖写**
		// factor_returns / ic_analysis，是系统级批处理（耗时以分钟计），
		// 属管理面；读面三条 GET 不在本表（只读面只受认证约束）。
		"POST /api/factor-analytics/compute",
	},

	// ── ReadOnlyPOST：POST 承载查询/计算，不改变任何状态 ──
	//
	// 规则原文是「所有 POST/PUT/PATCH/DELETE 至少 trader」，但这几条是
	// **用 POST 承载的纯计算**（入参给价格/持仓，出参是数字，不写库）。
	// 把它们也压成 trader，等于让 viewer 这个 read-only 角色连「算一下该买多少」
	// 都做不了——那是功能回退，不是安全收紧。故单列一档：已认证即可。
	ReadOnlyPOST: []string{
		"POST /api/risk/calculate_position", // 仓位计算：纯函数，不落库
		"POST /api/risk/check_stoploss",     // 止损判定：纯函数
		"POST /api/risk/detect_regime",      // regime 识别：纯函数
		// legacy 别名与上面三条同一 handler，**不能**因为「旧路径」就另立口径。
		"POST /calculate_position",
		"POST /check_stoploss",
		"POST /detect_regime",

		// QT-50 ③ 第 4 类粗粒度动作：批量行情查询（多 symbol × 日期区间，
		// 纯读、不写库）。POST 只因参数是集合；agent 的「行情查询」走这一条，
		// 不走 per-symbol 的 GET /api/ohlcv/{symbol}（D24 禁 per-symbol）。
		"POST /api/v1/ohlcv/bulk",

		// QT-69：表达式静态校验（Parse + 闸门）。纯计算、不写库、不触行情，
		// viewer 也应能做——与风险三条同一档。
		"POST /api/expression/validate",
	},

	// ── ReadOnlyGET：显式登记的读端点（QT-72）──
	//
	// 读端点由 auth.Middleware 的全局认证覆盖（已认证即可，与 GET
	// /api/backtest/{id}/report 同档），本档不参与变更面执法。登记它是为了
	// 让 X0 读面在策略表里有明文表态，并由守卫测试钉住「读端点不得混进
	// 任何变更档位」。
	ReadOnlyGET: []string{
		// QT-72 · X0：三 hash + result_status 的对外读面（只读
		// quant.run_provenance ⋈ run.backtest_results，不写任何表）。
		"GET /api/backtest/:id/provenance",
		// QT-78 · R2：分析产物读面（只读 quant.run_analysis_artifacts）。
		// 消费方是 analytics 的服务间读取，与上面的 X0 读面同档：已认证即可。
		"GET /api/runs/:id/portfolio-snapshot",
		"GET /api/runs/:id/factor-exposures",
		// 归属裁决（2026-11-12）：基本面读面读回 core 自己的
		// public.stock_fundamentals（只读、不写库），与选股读面同档。
		"GET /api/fundamental/:symbol",
	},

	// ── Service：服务间调用（裁-17，第五档）──
	//
	// 这里**只**放 core 对外承诺给机器调用方的端点。档位语义是 any-of 并列：
	// service 与原本能调它的 trader / admin 同时放行（tellurion-kit 的角色
	// 判定不做排序，service 不是「更高角色」）。服务 token 的配置泄露不得
	// 等值于下单权，所以它**只能**出现在这一档，禁止混进 Trader / Admin。
	Service: []string{
		// 起一个回测（CPU/内存开销；同步路径写结果面 run.backtest_results，
		// 异步路径提交到编排的 run.backtest_jobs）。调用方是 worker /
		// analytics 这类服务；真人 trader 与 admin 照旧可调。
		"POST /api/backtest",
	},

	// ── Trader：其余全部变更端点 ──
	Trader: []string{
		// 回测 / 批量 / 走查
		"POST /api/batch", // 批量回测：任务数 × 并发，资源放大
		"POST /api/walkforward",

		// 执行面（AUD-02 已挂 trader，此处登记同一条规则）
		"POST /api/execution/orders",
		"POST /api/execution/orders/:id/cancel",
		"POST /api/execution/emergency-flatten", // kill-switch：能一键清仓，属交易动作

		// 选股 / 信号
		"POST /api/screen",
		"POST /api/signals",

		// 数据写门（2026-11-12 裁）：平台唯一写入口，落到数据服务的
		// `POST /ingest/points`。写库动作，viewer 不得触碰。
		"POST /api/ingest/points",
		// 退役壳（410 `ingest_raw_retired`）：方法仍是 POST，静态腿要求
		// 逐条表态；档位与真实写门同档——退役不等于降权。
		"POST /api/ingest/raw",

		// 合规（写合规报告 / 触发异常交易扫描）
		"POST /api/compliance/check",
		"POST /api/compliance/abnormal/run",
		"POST /api/compliance/report/daily",
		"POST /api/compliance/divestment/check",

		// 运维面（有副作用：强制对账、强制告警检查）
		"POST /api/reconciliation/run",
		"POST /api/alerts/force-check",

		// AI 反代族：写穿到 ai-service（生成策略/跑流水线/调工具）
		"POST /api/copilot",
		"POST /api/copilot/*path",
		"PUT /api/copilot",
		"PUT /api/copilot/*path",
		"PATCH /api/copilot",
		"PATCH /api/copilot/*path",
		"DELETE /api/copilot",
		"DELETE /api/copilot/*path",
		"POST /api/pipeline",
		"POST /api/pipeline/*path",
		"PUT /api/pipeline",
		"PUT /api/pipeline/*path",
		"PATCH /api/pipeline",
		"PATCH /api/pipeline/*path",
		"DELETE /api/pipeline",
		"DELETE /api/pipeline/*path",
		"POST /api/explore",
		"POST /api/explore/*path",
		"PUT /api/explore",
		"PUT /api/explore/*path",
		"PATCH /api/explore",
		"PATCH /api/explore/*path",
		"DELETE /api/explore",
		"DELETE /api/explore/*path",
		"POST /api/tools",
		"POST /api/tools/*path",
		"PUT /api/tools",
		"PUT /api/tools/*path",
		"PATCH /api/tools",
		"PATCH /api/tools/*path",
		"DELETE /api/tools",
		"DELETE /api/tools/*path",

		// data-service 网关（同步触发 / 改排程 / ingest 都走这里）
		"POST /api/sync/*path",
		"PUT /api/sync/*path",
		"PATCH /api/sync/*path",
		"DELETE /api/sync/*path",

		// legacy 根路径别名（AUD-02 / AUD-33 保留的兼容层）：
		// 与 /api/* 版本同一处理，**不能**因为「是旧路径」就放宽。
		"POST /backtest",
		"POST /orders",
		"POST /orders/:id/cancel",
	},
}
