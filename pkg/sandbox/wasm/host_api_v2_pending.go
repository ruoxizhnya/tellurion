package wasm

// QT-01 · abi v2 契约里**尚未实现**的函数体。
//
// QT-24 已把数据面三个（get_bar2 / get_series_len2 / get_date2）接线到
// host_api.go；emit_target2 的收集与 sizing 归 QT-26，本文件保留它的
// 「调用即 trap」骨架，保证在 QT-26 落地前没有任何人能把目标权重路径
// 当成可用能力来依赖。
//
// 注册早于实现的理由（QT-01）：env 模块一次注册 v1+v2 全部 12 个函数，
// 宿主区校验发生在 instantiate 之后 —— 不注册 v2 名字，任何 v2 artifact
// 都实例化不了，「缺符号 / 落在数据段 / 混导 / 版本不符」四类坏 artifact
// 就只能靠造假输入来测。

import (
	"context"

	"github.com/ruoxizhnya/tellurion-contracts/engine"
)

// pendingHostFnBody 生成统一的 trap 文案：点名函数、点名它归属的工单。
func pendingHostFnBody(name string, ticket string) string {
	return "wasm: " + name + " is frozen by the abi v2 contract (QT-01) but its host body " +
		"is not wired yet (" + ticket + ") — calling it traps by design"
}

// hostEmitTarget2 实现 emit_target2(symbol_idx i32, weight f64) → void。
// 接线归 QT-26（目标权重 → 手数 + policy cap + sizing_source）。
func hostEmitTarget2(_ context.Context, _ int32, _ float64) {
	panic(pendingHostFnBody(contracts.HostFnEmitTarget2, "QT-26"))
}
