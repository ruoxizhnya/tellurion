package backtest

// QT-25 验收：symbol scope 的分片与预算（C2）。
//
// 四条腿：
//  1. W = min(configured_workers, budget/per_instance_estimate)；超预算降 W
//     不失败；连一个实例都装不下 → fail-closed 并点名改 scope=market；
//  2. 5000 标的按 W 分批跑完，峰值实例数 / 线性内存不越预算；
//  3. 预算调到 64 MiB（W=1）仍能把 5000 个分片跑完（只是变慢）；
//  4. 跨批恢复（分段 + 检查点）与一次跑完的信号逐位一致；
//     每个分片的检查点行坐标 (strategy, run, shard_key) 唯一（QT-16）。

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
	contracts "github.com/ruoxizhnya/tellurion-contracts/engine"
	"tellurion/pkg/sandbox/wasm"
	"tellurion/pkg/strategy"
)

// ─── 预算纯函数 ───────────────────────────────────────────────────

func wasmShardTestBudget(workers int) wasm.ShardBudget {
	return wasm.ShardBudget{
		Workers:          workers,
		BudgetBytes:      wasm.DefaultRunWasmBudget,
		PerInstanceBytes: wasm.DefaultMaxMemory,
		// PerCall 留 0 = 冻结默认（symbol 5ms）。实测教训：曾为每个分片预分配
		// 一张空 State map，5000 分片的 GC 压力会把合法调用顶过 5ms 墙；去掉
		// 那次分配后默认预算稳定通过（见 QT-25 施工记录）。
	}
}

func TestShardBudget_Plan(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		plan, err := wasm.ShardBudget{}.Plan()
		require.NoError(t, err)
		assert.Equal(t, 1, plan.Workers)
		assert.Equal(t, int64(512<<20), plan.BudgetBytes)
		assert.Equal(t, int64(64<<20), plan.PerInstanceBytes)
	})

	t.Run("configured_is_the_cap", func(t *testing.T) {
		// 512 MiB / 64 MiB = 8 个装得下，配置 3 ⇒ W=3。
		plan, err := wasmShardTestBudget(3).Plan()
		require.NoError(t, err)
		assert.Equal(t, 3, plan.Workers)
		assert.Equal(t, 3, plan.ConfiguredWorkers)
	})

	t.Run("budget_halves_workers", func(t *testing.T) {
		// 预算 128 MiB / 64 MiB = 2，配置 8 ⇒ 降 W 到 2，不失败。
		plan, err := (wasm.ShardBudget{
			Workers:          8,
			BudgetBytes:      128 << 20,
			PerInstanceBytes: 64 << 20,
		}).Plan()
		require.NoError(t, err)
		assert.Equal(t, 2, plan.Workers)
		assert.Equal(t, 8, plan.ConfiguredWorkers)
	})

	t.Run("tiny_budget_still_fits_one", func(t *testing.T) {
		plan, err := (wasm.ShardBudget{
			Workers:          8,
			BudgetBytes:      64 << 20,
			PerInstanceBytes: 64 << 20,
		}).Plan()
		require.NoError(t, err)
		assert.Equal(t, 1, plan.Workers)
	})

	t.Run("below_one_instance_fails_closed_with_market_hint", func(t *testing.T) {
		_, err := (wasm.ShardBudget{
			Workers:          8,
			BudgetBytes:      32 << 20,
			PerInstanceBytes: 64 << 20,
		}).Plan()
		require.ErrorIs(t, err, wasm.ErrShardBudgetTooSmall)
		assert.Contains(t, err.Error(), "scope=market", "错误必须提示改 market，而不是让人反复调小 W")
	})
}

// ─── 有状态的 symbol-scope 手工 artifact ───────────────────────────
//
// on_bar(t)：
//
//	mem[128]++                       // 实例自有状态（线性内存）
//	get_bar2(0,0,close); drop        // 证明数据面可达
//	emit_target2(0, counter%2==0 ? 0.2 : 0.1)
//
// finalize 不发信号。状态只落在线性内存 —— 跨批恢复必须靠内存镜像，
// 这正是 QT-25 检查点的载体。
func qt25StatefulSymbolArtifact() []byte {
	const (
		opI32Const = 0x41
		opI32Load  = 0x28
		opI32Store = 0x36
		opI32Add   = 0x6a
		opI32RemS  = 0x6f
		opI32Eqz   = 0x45
		opCall     = 0x10
		opDrop     = 0x1a
		opIf       = 0x04
		opElse     = 0x05
		opEnd      = 0x0b
		opI64Const = 0x42
		blockF64   = 0x7c
	)
	uleb := func(v uint32) []byte { return qt26Uleb(nil, v) }
	memarg := []byte{0x02, 0x00} // align=2, offset=0

	onBar := make([]byte, 0, 64)
	// get_bar2(0,0,close); drop
	onBar = append(onBar, opI32Const, 0x00, opI32Const, 0x00, opI32Const, 0x00, opCall, 0x00, opDrop)
	// mem[128] = mem[128] + 1
	onBar = append(onBar, opI32Const)
	onBar = append(onBar, uleb(128)...)
	onBar = append(onBar, opI32Const)
	onBar = append(onBar, uleb(128)...)
	onBar = append(onBar, opI32Load)
	onBar = append(onBar, memarg...)
	onBar = append(onBar, opI32Const, 0x01, opI32Add)
	onBar = append(onBar, opI32Store)
	onBar = append(onBar, memarg...)
	// emit_target2(0, mem[128]%2==0 ? 0.2 : 0.1)
	onBar = append(onBar, opI32Const, 0x00, opI32Const)
	onBar = append(onBar, uleb(128)...)
	onBar = append(onBar, opI32Load)
	onBar = append(onBar, memarg...)
	onBar = append(onBar, opI32Const, 0x02, opI32RemS, opI32Eqz)
	onBar = append(onBar, opIf, blockF64)
	onBar = qt26F64(onBar, 0.2)
	onBar = append(onBar, opElse)
	onBar = qt26F64(onBar, 0.1)
	onBar = append(onBar, opEnd, opCall, 0x01, opI64Const, 0x00, opEnd)

	m := &qt26Module{
		types: []qt26FuncType{
			{params: []byte{0x7f, 0x7f, 0x7f}, results: []byte{0x7c}}, // get_bar2
			{params: []byte{0x7f, 0x7c}},                              // emit_target2
			{params: []byte{0x7f, 0x7f}, results: []byte{0x7f}},       // initialize
			{params: []byte{0x7f}, results: []byte{0x7e}},             // on_bar
			{results: []byte{0x7e}},                                   // finalize
		},
		imports: []qt26Import{
			{name: contracts.HostFnGetBar2, typeIdx: 0},
			{name: contracts.HostFnEmitTarget2, typeIdx: 1},
		},
		funcs:    []uint32{2, 3, 4},
		memPages: 2, // 128 KiB：宿主区 4096..8192 + scratch 8192..73728
		globals:  []int32{4096, 8192, 65536},
		exports: []qt26Export{
			{name: "initialize", kind: 0, idx: 2},
			{name: "on_bar", kind: 0, idx: 3},
			{name: "finalize", kind: 0, idx: 4},
			{name: contracts.ExportHostParamsPtr, kind: 3, idx: 0},
			{name: contracts.ExportHostScratchPtr, kind: 3, idx: 1},
			{name: contracts.ExportHostScratchSize, kind: 3, idx: 2},
		},
		codes: [][]byte{
			{opI32Const, 0x00, opEnd},
			onBar,
			{opI64Const, 0x00, opEnd},
		},
	}
	return m.encode()
}

// ─── 夹具 ─────────────────────────────────────────────────────────

func qt25Axis(start time.Time, days int) []time.Time {
	axis := make([]time.Time, 0, days)
	for d := 0; d < days; d++ {
		axis = append(axis, start.AddDate(0, 0, d))
	}
	return axis
}

func qt25MarketData(symbols []string, axis []time.Time) map[string][]domain.OHLCV {
	out := make(map[string][]domain.OHLCV, len(symbols))
	for i, sym := range symbols {
		bars := make([]domain.OHLCV, 0, len(axis))
		price := 10.0 + float64(i%7)
		for d, date := range axis {
			price += float64((d%5)-2) * 0.1
			if price < 1 {
				price = 1
			}
			bars = append(bars, domain.OHLCV{
				Symbol: sym, Date: date,
				Open: price, High: price * 1.01, Low: price * 0.99, Close: price,
				Volume: 1_000_000,
			})
		}
		out[sym] = bars
	}
	return out
}

func qt25Meta(runID string, symbols []string, axis []time.Time) strategy.RunMeta {
	return strategy.RunMeta{
		RunID:       runID,
		Strategy:    "qt25-shard",
		Start:       axis[0],
		End:         axis[len(axis)-1],
		Universe:    symbols,
		RunUniverse: symbols,
		Axis:        axis,
		MarketData:  qt25MarketData(symbols, axis),
	}
}

func qt25Provider(artifact []byte, cfg wasmShardConfig) *wasmSignalProvider {
	return newWASMSignalProvider(WASMArtifact{
		Name:  "qt25-shard",
		Bytes: artifact,
		ABI:   contracts.HostAbiV2,
		Scope: contracts.ScopeSymbol,
	}, cfg, zerolog.Nop())
}

func qt25Run(t *testing.T, p *wasmSignalProvider, meta strategy.RunMeta) {
	t.Helper()
	require.NoError(t, p.BeginRun(context.Background(), meta))
	t.Cleanup(func() { _ = p.EndRun(context.Background(), meta.RunID) })
}

func countSignals(buffered map[int64][]strategy.Signal) int {
	n := 0
	for _, sigs := range buffered {
		n += len(sigs)
	}
	return n
}

// ─── 验收 2：5000 标的按批跑完，峰值不越预算 ─────────────────────

func TestSymbolScope_5000SymbolsWithinBudget(t *testing.T) {
	const (
		symbols = 5000
		days    = 3
		workers = 4
	)
	universe := make([]string, 0, symbols)
	for i := 0; i < symbols; i++ {
		universe = append(universe, fmt.Sprintf("%06d.SZ", i))
	}
	axis := qt25Axis(provenanceTestStart, days)
	meta := qt25Meta("qt25-5000", universe, axis)

	p := qt25Provider(qt25StatefulSymbolArtifact(), wasmShardConfig{Budget: wasmShardTestBudget(workers)})
	qt25Run(t, p, meta)

	// 每个分片每天发一条目标权重信号 ⇒ 5000 × 3。少一条就是某个分片没跑。
	assert.Equal(t, symbols*days, countSignals(p.buffered), "5000 个分片 × 3 天都应产出信号")
	assert.Equal(t, workers, p.stats.BatchSize, "wasm_batch_size = 生效的 W")
	assert.LessOrEqual(t, p.stats.PeakInstances, workers, "同时存活的实例数不得超过预算允许的 W")
	assert.Less(t, p.stats.PeakBytes, int64(workers)*int64(wasm.DefaultMaxMemory), "线性内存峰值必须落在 W × 单实例上限之内")
}

// ─── 验收 3：预算调到最小（W=1）仍能跑完 ──────────────────────────

func TestSymbolScope_TinyBudgetStillCompletes(t *testing.T) {
	const (
		symbols = 5000
		days    = 2
	)
	universe := make([]string, 0, symbols)
	for i := 0; i < symbols; i++ {
		universe = append(universe, fmt.Sprintf("%06d.SZ", i))
	}
	axis := qt25Axis(provenanceTestStart, days)
	meta := qt25Meta("qt25-tiny", universe, axis)

	// 64 MiB = 恰好一个实例：W 被压到 1，但**不许失败**。
	p := qt25Provider(qt25StatefulSymbolArtifact(), wasmShardConfig{Budget: wasm.ShardBudget{
		Workers:          8,
		BudgetBytes:      64 << 20,
		PerInstanceBytes: 64 << 20,
	}})
	qt25Run(t, p, meta)

	assert.Equal(t, 1, p.stats.BatchSize, "预算只够一个实例 ⇒ W=1")
	assert.Equal(t, symbols*days, countSignals(p.buffered), "降 W 只变慢，不改结果")
}

// ─── 验收 4a：跨批恢复 == 一次跑完 ───────────────────────────────

func TestSymbolScope_ResumeAcrossBatchesMatchesSingleRun(t *testing.T) {
	symbols := []string{"600000.SH"}
	axis := qt25Axis(provenanceTestStart, 12)
	full := qt25Meta("qt25-full", symbols, axis)

	// 一次跑完（参照）。
	ref := qt25Provider(qt25StatefulSymbolArtifact(), wasmShardConfig{
		Budget:          wasmShardTestBudget(2),
		CheckpointEvery: 3,
	})
	qt25Run(t, ref, full)
	refSignals := map[int64][]strategy.Signal{}
	for day, sigs := range ref.buffered {
		refSignals[day] = append([]strategy.Signal(nil), sigs...)
	}

	// 第一段：跑到第 5 天结束，留检查点。切点刻意取奇数天（5）——
	// 状态机的目标权重按计数器奇偶翻转，偶数切点会让「重启计数器」与
	// 「续跑」恰好落在同一奇偶上，测不出状态是否真的恢复。
	cut := 4 // 下标 0..4 共 5 天
	part1Meta := qt25Meta("qt25-part1", symbols, axis[:cut+1])
	part1 := qt25Provider(qt25StatefulSymbolArtifact(), wasmShardConfig{
		Budget:          wasmShardTestBudget(2),
		CheckpointEvery: 1,
	})
	qt25Run(t, part1, part1Meta)
	require.Contains(t, part1.checks, symbols[0], "第一段结束时必须留下终局检查点")
	cp := part1.checks[symbols[0]]
	assert.Equal(t, axis[cut], cp.Ts, "检查点必须落在日期边界（最后已处理 bar）")

	// 第二段：从检查点恢复，跑完剩余区间。
	part2 := qt25Provider(qt25StatefulSymbolArtifact(), wasmShardConfig{
		Budget:          wasmShardTestBudget(2),
		CheckpointEvery: 1,
		Resume:          map[string]shardCheckpoint{symbols[0]: cp},
	})
	qt25Run(t, part2, full)

	// 拼接：第一段覆盖的日期用第一段结果，其余用第二段结果。
	got := map[int64][]strategy.Signal{}
	for day, sigs := range part1.buffered {
		got[day] = append([]strategy.Signal(nil), sigs...)
	}
	for day, sigs := range part2.buffered {
		got[day] = append(got[day], sigs...)
	}

	require.Len(t, got, len(refSignals), "拼接后的交易日数应与一次跑完相同")
	for day, want := range refSignals {
		require.Len(t, got[day], len(want), "day=%d 信号条数", day)
		for i := range want {
			assert.Equal(t, want[i].Symbol, got[day][i].Symbol, "day=%d signal[%d].symbol", day, i)
			assert.Equal(t, want[i].Direction, got[day][i].Direction, "day=%d signal[%d].direction", day, i)
			assert.Equal(t, want[i].Strength, got[day][i].Strength, "day=%d signal[%d].strength", day, i)
		}
	}
}

// ─── 验收 4b：每分片状态行唯一（QT-16）────────────────────────────

type qt25RecordingStore struct {
	mu   sync.Mutex
	keys []strategy.StateKey
	envs []strategy.StateEnvelope
}

func (s *qt25RecordingStore) Save(_ context.Context, key strategy.StateKey, _ time.Time, state []byte, env strategy.StateEnvelope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = append(s.keys, key)
	s.envs = append(s.envs, env)
	return nil
}

func (s *qt25RecordingStore) Load(context.Context, strategy.StateKey) ([]byte, time.Time, strategy.StateEnvelope, error) {
	return nil, time.Time{}, strategy.StateEnvelope{}, strategy.ErrStateNotFound
}

func TestSymbolScope_ShardStateKeysAreUnique(t *testing.T) {
	symbols := []string{"600000.SH", "600001.SH", "600002.SH", "000001.SZ", "000002.SZ"}
	axis := qt25Axis(provenanceTestStart, 4)
	meta := qt25Meta("qt25-keys", symbols, axis)

	store := &qt25RecordingStore{}
	p := qt25Provider(qt25StatefulSymbolArtifact(), wasmShardConfig{
		Budget:          wasmShardTestBudget(2),
		CheckpointEvery: 2,
		Store:           store,
	})
	qt25Run(t, p, meta)

	require.NotEmpty(t, store.keys)
	seen := map[strategy.StateKey]bool{}
	shardKeys := map[string]bool{}
	for i, key := range store.keys {
		assert.Equal(t, "qt25-shard", key.StrategyID)
		assert.Equal(t, "qt25-keys", key.RunID)
		assert.NotEmpty(t, key.ShardKey, "symbol scope 的 shard_key 必须非空")
		seen[key] = true
		shardKeys[key.ShardKey] = true
		assert.NotEmpty(t, store.envs[i].ArtifactHash, "信封必须带 artifact_hash")
		assert.NotEmpty(t, store.envs[i].AbiVersion, "信封必须带 abi_version")
		assert.NotEmpty(t, store.envs[i].RunIdentityHash, "信封必须带 run_identity_hash（含 shard_key）")
	}
	assert.Len(t, shardKeys, len(symbols), "每个分片至少一行，且 shard_key 互不覆盖（QT-16）")
	assert.Len(t, seen, len(symbols), "唯一行坐标数 = 分片数")
	assert.Greater(t, len(store.keys), len(seen), "同一 (strategy, run, shard) 的重复保存复用同一行坐标（upsert 语义）")
}

// ─── 指标：三项观察面真的被喂到（QT-25 ③）──────────────────────────

type qt25Observer struct {
	mu        sync.Mutex
	instances []float64
	bytes     []float64
	batch     []float64
}

func (o *qt25Observer) SetWASMInstancesLive(v float64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.instances = append(o.instances, v)
}
func (o *qt25Observer) SetWASMBytesLive(v float64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.bytes = append(o.bytes, v)
}
func (o *qt25Observer) SetWASMBatchSize(v float64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.batch = append(o.batch, v)
}

func TestSymbolScope_ReportsThreeShardMetrics(t *testing.T) {
	symbols := []string{"600000.SH", "600001.SH", "600002.SH", "000001.SZ", "000002.SZ"}
	axis := qt25Axis(provenanceTestStart, 2)
	meta := qt25Meta("qt25-metrics", symbols, axis)

	ob := &qt25Observer{}
	p := qt25Provider(qt25StatefulSymbolArtifact(), wasmShardConfig{
		Budget:   wasmShardTestBudget(2),
		Observer: ob,
	})
	qt25Run(t, p, meta)

	require.NotEmpty(t, ob.instances)
	require.Equal(t, len(ob.instances), len(ob.bytes))
	require.Equal(t, len(ob.instances), len(ob.batch))
	for i := range ob.instances {
		assert.Equal(t, 2.0, ob.batch[i], "wasm_batch_size = 生效的 W")
		assert.LessOrEqual(t, ob.instances[i], 2.0, "wasm_instances_live 不得超过 W")
		assert.Greater(t, ob.bytes[i], 0.0, "wasm_bytes_live 必须是实际线性内存")
	}
}

// ─── 预算越界时的 fail-closed 接线（不是纯函数）─────────────────────

func TestSymbolScope_BudgetBelowOneInstanceFailsClosed(t *testing.T) {
	axis := qt25Axis(provenanceTestStart, 2)
	meta := qt25Meta("qt25-oom", []string{"600000.SH"}, axis)
	p := qt25Provider(qt25StatefulSymbolArtifact(), wasmShardConfig{Budget: wasm.ShardBudget{
		Workers:          4,
		BudgetBytes:      32 << 20,
		PerInstanceBytes: 64 << 20,
	}})
	err := p.BeginRun(context.Background(), meta)
	require.Error(t, err)
	assert.True(t, errors.Is(err, wasm.ErrShardBudgetTooSmall), "应命中预算哨兵，而不是普通错误")
	assert.Contains(t, err.Error(), "scope=market")
}
