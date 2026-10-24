package backtest

// QT-25：symbol scope 的分片执行器。
//
// 执行模型（C2 / abi v2 §3）：
//
//	每个标的一个独立 wasm 实例（分片），状态由实例自己的线性内存承载；
//	按 W = min(configured_workers, budget/per_instance_estimate) 起批，
//	一批跑完再起下一批；批间不保留实例，只留已 flush 的检查点。
//
// 为什么必须一次拿全区间行情（RunMeta.MarketData）：SignalProvider 的
// Signals 是逐日调用的。若按日推进，要让每个分片跨日存活就得同时起 N 个
// 实例（5000 个 → 直接违背预算）；若每天重建实例，v2 协议族又没有
// get_state/set_state（那是 v1 家族），状态无处可放。分片模型的前提就是
// 「一个标的从第一天跑到最后一天」——数据必须先到齐。
//
// 检查点 = 线性内存镜像（见 wasm.OnBarSession.SnapshotMemory 的理由）。
// 只在日期边界保存（复用 checkpoint.go 的裁决口径：日线序列的每根 bar
// 都是日期边界）；跨批恢复时按 ts 跳过已处理前缀并恢复内存镜像。

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
	contracts "github.com/ruoxizhnya/tellurion-contracts/engine"
	"tellurion/pkg/sandbox/wasm"
	"tellurion/pkg/strategy"
)

// shardCheckpoint 是单个分片在日期边界的检查点。
type shardCheckpoint struct {
	// Ts 是最后已处理 bar 的日期；恢复时跳过 Date <= Ts 的 bar。
	Ts time.Time
	// Memory 是当时的线性内存镜像（v2 的唯一状态载体）。
	Memory []byte
}

// WASMShardObserver 是 symbol scope 分片指标的观察面（QT-25 ③）。
//
// 只放接口不引依赖：pkg/backtest 不 import pkg/observability
// （口径同 RunObserver / nautilus.RunObserver —— *observability.Metrics
// 隐式实现本接口，编译期断言在 cmd/analysis/setup.go）。
type WASMShardObserver interface {
	SetWASMInstancesLive(v float64)
	SetWASMBytesLive(v float64)
	SetWASMBatchSize(v float64)
}

// wasmShardConfig 是分片调度的装配配置。
type wasmShardConfig struct {
	// Budget 是并发与内存预算。
	Budget wasm.ShardBudget
	// Observer 非 nil 时上报 QT-25 ③ 的三项指标（nil = 只写日志）。
	Observer WASMShardObserver
	// CheckpointEvery > 0 时每处理这么多根 bar（必是日期边界）保存一次
	// 检查点；<= 0 且 Store == nil 时不保存任何检查点（5000 个分片各留
	// 一份内存镜像没有收益，恢复入口本来就要求显式装配）。
	CheckpointEvery int
	// Resume 是非空时的恢复起点（symbol → 检查点）。只恢复有检查点的
	// 分片，其余从零跑。
	Resume map[string]shardCheckpoint
	// Store 非 nil 时把检查点写进 quant.strategy_state（QT-16 的
	// (strategy_id, run_id, shard_key) 唯一行）。nil = 只留在内存。
	Store strategy.StateStore
}

type shardResult struct {
	symbol   string
	signals  map[int64][]strategy.Signal
	bytes    int64
	instance int
	err      error
}

// NeedsFullMarketData 实现 strategy.FullMarketDataProvider（QT-25）：只有
// symbol scope 需要整段行情；market scope 仍走逐日 MarketData。
//
// v1 artifact 一律按 symbol 执行（ResolveScope 的兼容规则），所以即使
// 资产记录没写 scope，也要拿全区间行情——用声明字段判会漏掉它们。
func (p *wasmSignalProvider) NeedsFullMarketData() bool {
	if p.artifact.ABI == contracts.HostAbiV1 {
		return true
	}
	return p.artifact.Scope == contracts.ScopeSymbol
}

// beginSymbolRun 在 BeginRun 内一次性跑完所有分片，把信号按交易日缓冲起来。
func (p *wasmSignalProvider) beginSymbolRun(
	ctx context.Context,
	meta strategy.RunMeta,
	factory *wasm.OnBarSessionFactory,
	paramsJSON []byte,
) error {
	if len(meta.MarketData) == 0 {
		return fmt.Errorf("wasm provider: scope=symbol requires RunMeta.MarketData (the engine preloads RunUniverse OHLCV for QT-25 sharding) — got none")
	}
	plan, err := p.shardCfg.Budget.Plan()
	if err != nil {
		return fmt.Errorf("wasm provider: symbol scope shard budget: %w", err)
	}
	var missing []string
	for _, sym := range meta.RunUniverse {
		if len(meta.MarketData[sym]) == 0 {
			missing = append(missing, sym)
		}
	}
	if len(missing) > 0 {
		const maxListed = 8
		listed := missing
		if len(listed) > maxListed {
			listed = listed[:maxListed]
		}
		return fmt.Errorf(
			"wasm provider: scope=symbol requires OHLCV for the whole RunUniverse; %d symbol(s) missing (e.g. %v) — 预热失败或票池与缓存不一致，拒绝用缺失数据起分片",
			len(missing), listed)
	}

	if n := len(meta.Axis); n > 0 {
		p.lastAxisDate = meta.Axis[n-1]
		p.lastAxisDay = dayKeyUTC(p.lastAxisDate)
	}

	// 会话级预算：symbol scope 的单次调用预算（C1 冻结 5ms）；run deadline
	// 由本 provider 统一持有，避免每个分片各起一个 10min 计时器。
	budget, err := wasm.BudgetForScope(contracts.ScopeSymbol)
	if err != nil {
		return err
	}
	if p.shardCfg.Budget.PerCall > 0 {
		budget.PerCall = p.shardCfg.Budget.PerCall
	}
	deadline := budget.RunDeadline
	if deadline <= 0 {
		deadline = wasm.DefaultRunDeadline
	}
	runCtx, runCancel := context.WithTimeout(ctx, deadline)
	p.runCancel = runCancel
	p.symbolBudget = budget

	p.symbols = append([]string(nil), meta.RunUniverse...)
	p.buffered = make(map[int64][]strategy.Signal)
	p.checks = make(map[string]shardCheckpoint)
	p.stats = wasm.ShardStats{BatchSize: plan.Workers}

	p.logger.Info().
		Int("workers_configured", plan.ConfiguredWorkers).
		Int("workers", plan.Workers).
		Int64("run_budget_bytes", plan.BudgetBytes).
		Int64("per_instance_bytes", plan.PerInstanceBytes).
		Int("symbols", len(meta.RunUniverse)).
		Msg("wasm symbol scope shard plan")

	// 按 W 分批：批间只保留已 flush 的检查点，实例全部关闭。
	for start := 0; start < len(meta.RunUniverse); start += plan.Workers {
		end := start + plan.Workers
		if end > len(meta.RunUniverse) {
			end = len(meta.RunUniverse)
		}
		if err := p.runShardBatch(runCtx, factory, paramsJSON, meta, plan.Workers, start, end); err != nil {
			return err
		}
	}
	return nil
}

// runShardBatch 并发跑一批分片，按 RunUniverse 顺序合并结果。
//
// 顺序确定性：每个分片的结果写回固定下标，合并时按下标遍历；同一交易日内
// 的信号因此恒按 RunUniverse 顺序排列（引擎随后还会做 QT-21 规范化）。
func (p *wasmSignalProvider) runShardBatch(
	runCtx context.Context,
	factory *wasm.OnBarSessionFactory,
	paramsJSON []byte,
	meta strategy.RunMeta,
	batchSize, start, end int,
) error {
	n := end - start
	results := make([]shardResult, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sym := meta.RunUniverse[start+i]
			sigs, bytes, err := p.runShard(runCtx, factory, paramsJSON, meta, sym)
			results[i] = shardResult{symbol: sym, signals: sigs, bytes: bytes, instance: 1, err: err}
		}(i)
	}
	wg.Wait()

	var firstErr error
	var bytesLive int64
	instances := 0
	for i := range results {
		r := &results[i]
		if r.err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("wasm provider: shard %s: %w", r.symbol, r.err)
			}
			continue
		}
		instances += r.instance
		bytesLive += r.bytes
		for day, sigs := range r.signals {
			p.buffered[day] = append(p.buffered[day], sigs...)
		}
	}

	// 指标（C2 ③）：本批的并发实例、线性内存字节、批大小。
	p.stats = p.stats.ObserveBatch(instances, bytesLive)
	if ob := p.shardCfg.Observer; ob != nil {
		ob.SetWASMInstancesLive(float64(instances))
		ob.SetWASMBytesLive(float64(bytesLive))
		ob.SetWASMBatchSize(float64(batchSize))
	}
	p.logger.Debug().
		Int("wasm_instances_live", instances).
		Int64("wasm_bytes_live", bytesLive).
		Int("wasm_batch_size", batchSize).
		Int("batch_start", start).
		Int("batch_end", end).
		Msg("wasm symbol shard batch")
	// 批结束：实例已全部关闭，只留检查点。
	p.stats = p.stats.EndBatch()
	return firstErr
}

// runShard 把一个标的从区间起点跑到终点。
//
// 瞬态终止重试一次（QT-25 实测结论）：5ms 是 C1 冻结的单次调用预算，但它
// 是**墙钟**——5000 个分片连续实例化时，GC / 调度抖动可能让一次合法调用
// 摸到 5ms 墙，wazero 随即关闭模块（实测：每分片预分配一张空 State map
// 就会稳定复现）。这类终止不是策略跑坏，重跑同一分片即可。
//
// 为什么重试不会把「真卡死」放过去：`while(true)` 在两次重试里都会超时，
// 第二次仍返回 ErrSessionTerminated，run 照常 failed。为什么只重试一次：
// 两次都摸墙说明问题不是瞬态（要么策略真慢，要么预算确实不可行）。
func (p *wasmSignalProvider) runShard(
	runCtx context.Context,
	factory *wasm.OnBarSessionFactory,
	paramsJSON []byte,
	meta strategy.RunMeta,
	sym string,
) (map[int64][]strategy.Signal, int64, error) {
	out, bytes, err := p.runShardOnce(runCtx, factory, paramsJSON, meta, sym)
	if err == nil || !errors.Is(err, wasm.ErrSessionTerminated) {
		return out, bytes, err
	}
	p.logger.Warn().Err(err).Str("run_id", meta.RunID).Str("symbol", sym).
		Msg("wasm shard session terminated — restarting the shard once from its resume checkpoint (QT-25 transient-termination guard)")
	return p.runShardOnce(runCtx, factory, paramsJSON, meta, sym)
}

// runShardOnce 是单个分片的一次完整执行（不重试）。
func (p *wasmSignalProvider) runShardOnce(
	runCtx context.Context,
	factory *wasm.OnBarSessionFactory,
	paramsJSON []byte,
	meta strategy.RunMeta,
	sym string,
) (map[int64][]strategy.Signal, int64, error) {
	bars := clipSeries(meta.MarketData[sym], meta.Start, meta.End)
	if len(bars) == 0 {
		return nil, 0, nil
	}

	sess, err := factory.NewSession(runCtx, p.symbolBudget)
	if err != nil {
		return nil, 0, err
	}
	defer sess.Close()

	if err := sess.Initialize(runCtx, paramsJSON); err != nil {
		return nil, 0, err
	}

	// 恢复：先恢复内存镜像，再按 ts 跳过已处理前缀（与 checkpoint.go 的
	// skipPrefix 同一判据：Date <= ts 已处理）。
	start := 0
	if cp, ok := p.shardCfg.Resume[sym]; ok && len(cp.Memory) > 0 {
		if err := sess.RestoreMemory(cp.Memory); err != nil {
			return nil, 0, fmt.Errorf("restore checkpoint at %s: %w", cp.Ts.Format("2006-01-02"), err)
		}
		start = skipProcessedBars(bars, cp.Ts)
	}

	// v2 协议族没有 get_state/set_state（族互斥），State 不会被读取；
	// 不预分配 map —— 5000 个分片各建一张空 map 只会给 GC 添压力。
	bc := &wasm.BarContext{
		Symbols: []string{sym},
		Bars:    [][]domain.OHLCV{bars},
	}
	if decl, ok := sess.HostRegion(); ok {
		bc.ScratchPtr = decl.ScratchPtr
		bc.ScratchSize = decl.ScratchSize
	}

	out := make(map[int64][]strategy.Signal)
	for i := start; i < len(bars); i++ {
		bc.T = int32(i)
		bc.Signals = nil
		bc.Targets = nil
		if err := sess.OnBar(runCtx, bc); err != nil {
			return nil, 0, fmt.Errorf("bar %s (t=%d): %w", bars[i].Date.Format("2006-01-02"), i, err)
		}
		day := dayKeyUTC(bars[i].Date)
		if sigs := convertWASEmission([]string{sym}, bc.Signals, bc.Targets, bars[i].Date); len(sigs) > 0 {
			out[day] = append(out[day], sigs...)
		}

		if i == len(bars)-1 {
			// 终局检查点必须在 finalize **之前**：finalize 是 run 结束时的
			// 收尾动作，它的副作用不属于「跑到第 i 根 bar 的状态」。先存后
			// finalize，续跑才与「分段边界之前的状态」对齐。
			if p.shardCfg.CheckpointEvery > 0 || p.shardCfg.Store != nil {
				if err := p.saveShardCheckpoint(runCtx, sess, meta, sym, bars[i].Date); err != nil {
					return nil, 0, err
				}
			}
			// finalize 的信号归 run 的最后一个交易日（QT-24），不是该标的
			// 最后一根 bar 的日期——停牌到期末的标的也要落在最后一日。
			bc.Signals = nil
			bc.Targets = nil
			diag, ferr := sess.Finalize(runCtx, bc)
			if ferr != nil {
				return nil, 0, fmt.Errorf("finalize: %w", ferr)
			}
			if len(diag) > 0 {
				p.logger.Debug().Str("run_id", meta.RunID).Str("symbol", sym).
					Bytes("finalize_diagnostics", diag).
					Msg("wasm finalize diagnostics (not a signal channel)")
			}
			lastDay := p.lastAxisDay
			if lastDay == 0 {
				lastDay = day
			}
			if sigs := convertWASEmission([]string{sym}, bc.Signals, bc.Targets, p.lastAxisDate); len(sigs) > 0 {
				out[lastDay] = append(out[lastDay], sigs...)
			}
		}

		// 检查点只在日期边界保存。日线序列的每根 bar 之后，下一根必然进入
		// 新日期（同一天不会有两根日线）；这里仍显式复用同一判据，避免
		// 将来换成分时序列时静默破坏「跳过的前缀恰好是完整日期块」。
		if p.shardCfg.CheckpointEvery > 0 && i+1 < len(bars) && (i+1)%p.shardCfg.CheckpointEvery == 0 && isShardDateBoundary(bars, i) {
			if err := p.saveShardCheckpoint(runCtx, sess, meta, sym, bars[i].Date); err != nil {
				return nil, 0, err
			}
		}
	}

	return out, int64(sess.MemorySize()), nil
}

// saveShardCheckpoint 保存一个分片在 ts 时刻的检查点。
//
// 内存里总保留（供测试与跨批恢复）；配了 Store 时再写 quant.strategy_state，
// 行坐标 = (strategy, run, shard_key) —— 5000 个分片是 5000 行唯一键（QT-16）。
func (p *wasmSignalProvider) saveShardCheckpoint(
	ctx context.Context,
	sess *wasm.OnBarSession,
	meta strategy.RunMeta,
	sym string,
	ts time.Time,
) error {
	mem, err := sess.SnapshotMemory()
	if err != nil {
		return fmt.Errorf("checkpoint snapshot at %s: %w", ts.Format("2006-01-02"), err)
	}
	p.checksMu.Lock()
	p.checks[sym] = shardCheckpoint{Ts: ts, Memory: mem}
	p.checksMu.Unlock()

	if p.shardCfg.Store == nil {
		return nil
	}
	env, err := p.shardEnvelope(meta, sym)
	if err != nil {
		return err
	}
	key := strategy.StateKey{StrategyID: meta.Strategy, RunID: meta.RunID, ShardKey: sym}
	if err := p.shardCfg.Store.Save(ctx, key, ts, mem, env); err != nil {
		return fmt.Errorf("checkpoint save (shard=%s ts=%s): %w", sym, ts.Format("2006-01-02"), err)
	}
	return nil
}

// shardEnvelope 是 QT-16 的身份信封：artifact / abi / run 身份（含 shard_key）。
func (p *wasmSignalProvider) shardEnvelope(meta strategy.RunMeta, sym string) (strategy.StateEnvelope, error) {
	paramsHash, err := contracts.ParamsHash(meta.Params)
	if err != nil {
		return strategy.StateEnvelope{}, fmt.Errorf("checkpoint params hash: %w", err)
	}
	runHash, err := strategy.StateIdentity{
		Scope:      strategy.ScopeSymbol,
		ShardKey:   sym,
		Universe:   meta.RunUniverse,
		ParamsHash: paramsHash,
	}.RunIdentityHash()
	if err != nil {
		return strategy.StateEnvelope{}, fmt.Errorf("checkpoint run identity: %w", err)
	}
	return strategy.StateEnvelope{
		// 状态载体是「线性内存整段镜像」，版本随该口径走。
		StateSchemaVersion: "wasm-memory-v1",
		ArtifactHash:       contracts.ArtifactHash(p.artifact.Bytes),
		AbiVersion:         p.artifact.ABI.String(),
		RunIdentityHash:    runHash,
	}, nil
}

// clipSeries 取 [start, end] 内的 bar（含端点，按 UTC 日粒度），保持原顺序。
// 引擎预热窗口本就与 run 区间对齐；这里再裁一次是为了不把窗口外数据带进
// 分片（它会让 T 的推进序列与 run 不一致）。
func clipSeries(bars []domain.OHLCV, start, end time.Time) []domain.OHLCV {
	if len(bars) == 0 {
		return nil
	}
	lo, hi := dayKeyUTC(start), dayKeyUTC(end)
	out := make([]domain.OHLCV, 0, len(bars))
	for _, b := range bars {
		d := dayKeyUTC(b.Date)
		if d < lo || d > hi {
			continue
		}
		out = append(out, b)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out
}

// skipProcessedBars 返回第一根 Date > ts 的下标（= 已处理前缀长度）。
// 与 strategy 的 skipPrefix 同判据：等于 ts 的那根已经处理过。
func skipProcessedBars(bars []domain.OHLCV, ts time.Time) int {
	n := 0
	for n < len(bars) && !bars[n].Date.After(ts) {
		n++
	}
	return n
}

// isShardDateBoundary 与 strategy.isDateBoundary 同一裁决：最后一根，或
// 下一根进入新日期。checkpoint.go 未导出该函数（它属于流式路径的内部
// 细节），分片路径复写同一判据并在这里注明来源，避免两条路径各写一套。
func isShardDateBoundary(bars []domain.OHLCV, i int) bool {
	return i+1 == len(bars) || bars[i+1].Date.After(bars[i].Date)
}
