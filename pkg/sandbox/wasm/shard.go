package wasm

// QT-25：symbol scope 的分片并发预算（C2 / ABIs v2 §3）。
//
// 冻结设计：
//
//	W = min(configured_workers, run_budget / per_instance_estimate)
//
// 超预算的处理是**降 W，不失败**；只有「预算连一个实例都装不下」才
// fail-closed，并且错误必须点名改 scope=market —— 因为那已经不是调参
// 问题，而是这条执行模型在当前预算下不成立。
//
// 为什么 per-instance estimate 默认取 64MiB 而不是「初始页 64KiB」：
// abi v2 §3 给每实例的内存上限就是 64MiB，策略可能在 run 中途 grow 到
// 上限。按上限做并发预算 = 最坏情况下也不超总预算；按初始页估算等于
// 拿「现在还没涨」赌「以后也不涨」，OOM 恰恰是这么来的。

import (
	"errors"
	"fmt"
	"time"
)

// DefaultRunWasmBudget 是单 run wasm 线性内存总预算（C2 建议值，512 MiB）。
const DefaultRunWasmBudget int64 = 512 << 20

// ErrShardBudgetTooSmall 表示 run 预算连一个 symbol-scope 实例都装不下。
// 调用方应据此提示改 scope=market（或调大预算），而不是重试。
var ErrShardBudgetTooSmall = errors.New("wasm: run wasm budget cannot hold one symbol-scope instance")

// ShardBudget 是分片并发预算的输入。零值字段走默认：
//
//	Workers          ≤ 0 → 1（不并发）
//	BudgetBytes      ≤ 0 → DefaultRunWasmBudget
//	PerInstanceBytes ≤ 0 → DefaultMaxMemory（64 MiB，abi v2 的单实例上限）
type ShardBudget struct {
	Workers          int
	BudgetBytes      int64
	PerInstanceBytes int64
	// PerCall 覆盖 symbol scope 的单次调用预算；0 沿用 BudgetForScope 的
	// 冻结默认（5ms）。C1 原文就是「5ms，可配」——高并发下 GC/调度抖动
	// 可能让一次合法调用摸到 5ms 墙，配置项是给这种场景留的出口，不是
	// 放宽策略执行本身的预算口径。
	PerCall time.Duration
}

// ShardPlan 是生效后的预算与并发数。
type ShardPlan struct {
	// Workers 是实际并发实例数（≥ 1）。
	Workers int
	// ConfiguredWorkers 是调用方给的并发上限（便于诊断「是配置卡住还是预算卡住」）。
	ConfiguredWorkers int
	// BudgetBytes / PerInstanceBytes 是生效后的预算口径。
	BudgetBytes      int64
	PerInstanceBytes int64
}

// Plan 计算并发数。预算充足时 W = configured；预算不足时降 W；
// 连一个实例都装不下 → ErrShardBudgetTooSmall（错误里点名改 market）。
func (b ShardBudget) Plan() (ShardPlan, error) {
	workers := b.Workers
	if workers <= 0 {
		workers = 1
	}
	budget := b.BudgetBytes
	if budget <= 0 {
		budget = DefaultRunWasmBudget
	}
	per := b.PerInstanceBytes
	if per <= 0 {
		per = DefaultMaxMemory
	}

	fit := budget / per
	if fit < 1 {
		return ShardPlan{}, fmt.Errorf(
			"%w: run wasm budget=%d bytes < per-instance estimate=%d bytes — 调大预算，或把 artifact 编译为 scope=market（单实例横截面模型）",
			ErrShardBudgetTooSmall, budget, per)
	}
	w := workers
	if int64(w) > fit {
		// 超预算降 W（不失败）：这是 C2 的硬要求，不是「尽力而为」。
		w = int(fit)
	}
	return ShardPlan{
		Workers:           w,
		ConfiguredWorkers: workers,
		BudgetBytes:       budget,
		PerInstanceBytes:  per,
	}, nil
}

// ShardStats 是分片调度的可观测面（C2 ③ 的三项指标）。
//
// 口径：
//   - InstancesLive / BytesLive 是**当前**同时在跑的实例数与线性内存字节；
//     一批结束时归零（批间不保留实例）。
//   - BatchSize 是生效的 W（即 ③ 的 wasm_batch_size）。
//   - Peak* 是本 run 观察到的峰值，便于事后确认预算是否被守住。
type ShardStats struct {
	InstancesLive int
	BytesLive     int64
	BatchSize     int
	PeakInstances int
	PeakBytes     int64
}

// ObserveBatch 记录一批分片的峰值（批内并发实例数是确定值；字节数取各分片
// 实际线性内存之和）。返回更新后的统计。
func (s ShardStats) ObserveBatch(instances int, bytesLive int64) ShardStats {
	s.InstancesLive = instances
	s.BytesLive = bytesLive
	if instances > s.PeakInstances {
		s.PeakInstances = instances
	}
	if bytesLive > s.PeakBytes {
		s.PeakBytes = bytesLive
	}
	return s
}

// EndBatch 把「当前在跑」归零（批间只留已 flush 的状态，不留实例）。
func (s ShardStats) EndBatch() ShardStats {
	s.InstancesLive = 0
	s.BytesLive = 0
	return s
}
