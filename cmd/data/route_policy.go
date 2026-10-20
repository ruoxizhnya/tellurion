package main

// REV-17：data-service 的「端点 × 最低角色」明文表。
//
// 背景：这个服务此前**完全没有认证**（`buildRouter` 只有 Recovery/CORS/限流/
// 日志），而 `POST /api/ingest/raw` 是证据链（ingest.raw）的**唯一写门**、
// `/sync/ohlcv/all` 是一次触发 ≈5900 次 Tushare 调用的入口——任何能连到 :8081
// 的人都能打。ADR-025 只发布回环/内网缓解了严重度，但「靠网络边界当唯一屏障」
// 不是鉴权。
//
// 规则（2026-10-16 主任裁决）：所有 POST/PUT/PATCH/DELETE 至少 trader；
// 管理面 admin。排程管理（增删改启停）是**管理面**——它决定「以后每天自动跑
// 什么」，影响面远超一次手动触发；触发一次同步（含"现在就跑这条排程"）是
// trader 动作。

import "tellurion/pkg/authz"

var dataRoutePolicy = authz.Policy{
	// ── Public：data-service 没有登录入口（token 由 analysis 签），故为空 ──
	Public: nil,

	// ── Admin：排程管理 ──
	Admin: []string{
		"POST /api/sync/schedules",            // 新增排程：决定"以后自动跑什么"
		"PUT /api/sync/schedules/:id",         // 改排程（含 cron / 参数）
		"DELETE /api/sync/schedules/:id",      // 删排程
		"POST /api/sync/schedules/:id/toggle", // 启停排程
	},

	// ── ReadOnlyPOST：POST 承载查询 DSL，不写库 ──
	ReadOnlyPOST: []string{
		"POST /screen",            // 选股：查询 DSL 走 body，纯读
		"POST /api/v1/ohlcv/bulk", // 批量读行情，纯读
	},

	// ── Trader：其余全部变更端点 ──
	Trader: []string{
		// 证据链写门（ADR-022 §2）：类 A 原始响应归档 / EquityDeep 归一化
		"POST /api/ingest/raw",
		"POST /api/ingest/equitydeep",

		// 同步触发（手动）
		"POST /sync/stocks",
		"POST /sync/ohlcv",
		"POST /sync/ohlcv/all", // 一次触发 ≈5900 只逐只调 Tushare 配额
		"POST /sync/fundamental",
		"POST /sync/fundamentals",
		"POST /sync/dividends",
		"POST /sync/splits",
		"POST /sync/calendar",
		"POST /sync/sectors",
		"POST /sync/index-constituents/:index_code",
		"POST /sync/index-daily/:index_code",

		// 因子计算（写 factor_cache / factor_returns / ic_analysis）
		"POST /sync/factors/:factor_name",
		"POST /sync/factors/all",
		"POST /api/sync/factor-attribution/:factor_name",
		"POST /api/sync/factor-ic/:factor_name",

		// 异步作业（typed door）：创建 / 取消 / 重试 / 立刻跑一次排程
		"POST /api/sync/jobs",
		"POST /api/sync/jobs/:id/cancel",
		"POST /api/sync/jobs/:id/retry",
		"POST /api/sync/schedules/:id/run",

		// 缓存预热（写缓存，资源开销与上游调用）
		"POST /api/v1/cache/warm",
	},
}
