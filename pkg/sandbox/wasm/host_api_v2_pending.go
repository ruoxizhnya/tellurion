package wasm

// QT-01 · abi v2 新增函数的**注册骨架**。
//
// 本单冻结的是契约（名字 + 签名 + 语义 + 上限），不是实现：函数体属 QT-24
// （host 数据模型 Bars/Axis/T）与 QT-26（WASM SignalProvider 接线）。
//
// 那为什么现在就注册？因为 §5「引擎加载器」要求 env 模块一次注册 v1+v2 全部
// 12 个函数，而**宿主区校验发生在 instantiate 之后**——不注册 v2 名字，任何
// v2 artifact 都实例化不了，QT-01 的「缺符号 / 落在数据段 / 混导 / 版本不符
// 四类坏 artifact 全部被拒」就只能靠造假输入来测。注册一个「调用即 trap」的
// 骨架，让坏 artifact 在真实字节码上被拒，同时保证没有任何人能把 v2 路径
// 当成可用能力来依赖。

import (
	"context"

	"github.com/ruoxizhnya/tellurion-contracts/engine"
)

// pendingHostFnBody 生成统一的 trap 文案：点名函数、点名它归属的工单。
func pendingHostFnBody(name string) string {
	return "wasm: " + name + " is frozen by the abi v2 contract (QT-01) but its host body " +
		"is not wired yet (QT-24 data model / QT-26 provider wiring) — calling it traps by design"
}

// hostGetBar2 实现 get_bar2(symbol_idx i32, t_offset i32, field_id i32) → f64。
func hostGetBar2(_ context.Context, _, _, _ int32) float64 {
	panic(pendingHostFnBody(contracts.HostFnGetBar2))
}

// hostGetSeriesLen2 实现 get_series_len2(symbol_idx i32) → i32。
func hostGetSeriesLen2(_ context.Context, _ int32) int32 {
	panic(pendingHostFnBody(contracts.HostFnGetSeriesLen2))
}

// hostGetDate2 实现 get_date2() → i32（YYYYMMDD，UTC）。
func hostGetDate2(_ context.Context) int32 {
	panic(pendingHostFnBody(contracts.HostFnGetDate2))
}

// hostEmitTarget2 实现 emit_target2(symbol_idx i32, weight f64) → void。
func hostEmitTarget2(_ context.Context, _ int32, _ float64) {
	panic(pendingHostFnBody(contracts.HostFnEmitTarget2))
}
