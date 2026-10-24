package backtest

// QT-26：WASM SignalProvider —— 从 artifact 加载 → 逐 bar 执行 → 信号 → 成交。
//
// 五道门（strategy-asset-service-api §4）在这里的落点：
//
//	Resolve  策略名 → 装配期注入的 artifact（WithWASMArtifacts；手工 artifact 即可，
//	         不需要编译服务 —— 后者是 QT-41b，归属 tellurion-strategy:cmd/compiler）
//	Fetch    artifact 字节已在内存（内循环零网络）
//	Verify   import 白名单（Compile）+ 协议族自证 + 与声明 abi_version 交叉校验
//	         + scope 解析（v2 market；symbol scope 归 QT-25）
//	Compile  wasm.Sandbox.Compile（同一份白名单门）
//	Mount    initialize(params)：v2 写到模块声明的 __host_params_ptr
//
// 一次 run 一个 provider 实例（QT-14）：runContext.newSignalProvider 每 run 新建，
// BeginRun 开运行时会话、EndRun 关闭。内部状态（bars/state/axis）不跨 run。
//
// 数据模型（QT-24）：BarContext.Axis = RunMeta.Axis（唯一日历源），
// Symbols = RunMeta.RunUniverse（symbol_idx 唯一来源），Bars[sym] 直接引用
// 引擎当日给出的**累积**行情切片（零拷贝）。停牌导致当天 MarketData 缺该
// symbol 时保留上一份切片 —— get_bar2 按日期查不到就返回 NaN，不会重排索引。

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
	contracts "github.com/ruoxizhnya/tellurion-contracts/engine"
	"tellurion/pkg/sandbox/wasm"
	"tellurion/pkg/strategy"
)

// Signal.Metadata 的键与 sizing_source 取值（QT-26）。
//
// 放在 core 侧而不是 contracts：domain.Signal.Metadata 是开放扩展槽，
// 这两个键是**本实现**的约定，不是跨仓 ABI；写进 contracts 会把一次
// 内部接线升级成一次契约冻结。
const (
	signalMetaTargetWeight  = "target_weight"
	signalMetaSizingSource  = "sizing_source"
	sizingSourceEmitTarget2 = "emit_target2"
)

// WASMArtifact 是一份可直接执行的策略制品及其资产元数据。
//
// 今天由装配方手工注入（WithWASMArtifacts）；资产服务（QT-41）落地后由
// Resolve/Fetch 门从服务取回同形状的记录。
type WASMArtifact struct {
	Name  string
	Bytes []byte
	// ABI 是资产记录声明的 abi_version；必须与 import section 自证一致。
	ABI contracts.HostAbiVersion
	// Scope 是资产记录声明的实例模型；v2 必填（v1 只能是 symbol）。
	Scope contracts.StrategyScope
}

// wasmSignalProvider 是 strategy.SignalProvider 的 WASM 实现。
type wasmSignalProvider struct {
	artifact WASMArtifact
	logger   zerolog.Logger

	mu      sync.Mutex
	rt      *wasm.WazeroRuntime
	sess    *wasm.OnBarSession
	meta    strategy.RunMeta
	axis    []time.Time
	symbols []string
	bars    [][]domain.OHLCV
	bc      *wasm.BarContext
	final   bool
	closed  bool
}

var (
	_ strategy.SignalProvider   = (*wasmSignalProvider)(nil)
	_ strategy.Stateful         = (*wasmSignalProvider)(nil)
	_ strategy.SessionLifecycle = (*wasmSignalProvider)(nil)
)

func newWASMSignalProvider(artifact WASMArtifact, logger zerolog.Logger) *wasmSignalProvider {
	return &wasmSignalProvider{artifact: artifact, logger: logger}
}

// Stateful 声明为有状态：会话随 run 开关（QT-14 强制实现 SessionLifecycle）。
func (p *wasmSignalProvider) Stateful() bool { return true }

// BeginRun 在 run 的任何信号之前完成全部加载门并 Mount。
func (p *wasmSignalProvider) BeginRun(ctx context.Context, meta strategy.RunMeta) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return fmt.Errorf("wasm provider: BeginRun after EndRun")
	}
	if meta.RunID == "" {
		return fmt.Errorf("wasm provider: BeginRun requires RunID")
	}
	if len(meta.Axis) == 0 {
		return fmt.Errorf("wasm provider: RunMeta.Axis is empty — market scope needs the engine trading-day axis")
	}
	if len(meta.RunUniverse) == 0 {
		return fmt.Errorf("wasm provider: RunMeta.RunUniverse is empty — symbol_idx has no stable source")
	}
	if len(p.artifact.Bytes) == 0 {
		return fmt.Errorf("wasm provider: artifact %q has no bytes", p.artifact.Name)
	}
	if !p.artifact.ABI.Valid() {
		return fmt.Errorf("wasm provider: artifact %q declares unsupported abi_version %v", p.artifact.Name, p.artifact.ABI)
	}

	rt := wasm.NewWazeroRuntime(ctx, wasm.DefaultMaxMemory)
	if err := rt.InstantiateHostModule(ctx); err != nil {
		_ = rt.Close(ctx)
		return fmt.Errorf("wasm provider: instantiate host module: %w", err)
	}
	sb := wasm.NewSandbox(rt, wasm.Config{MaxMemoryBytes: wasm.DefaultMaxMemory})
	sb.SetLogger(p.logger)

	// Verify（先于实例化）：import 白名单 + 协议族自证 + 声明版本交叉校验。
	// 单独编译一次是为了把「声明与自证不符」在 instantiate 之前判定（§7.2-3）。
	cm, err := sb.Compile(ctx, p.artifact.Bytes)
	if err != nil {
		_ = rt.Close(ctx)
		return fmt.Errorf("wasm provider: verify artifact %q: %w", p.artifact.Name, err)
	}
	abiMod, ok := cm.(wasm.AbiModule)
	if !ok {
		_ = cm.Close(ctx)
		_ = rt.Close(ctx)
		return fmt.Errorf("wasm provider: compiled module %T does not expose the import section", cm)
	}
	family, err := wasm.ValidateArtifactAbi(abiMod, p.artifact.ABI)
	_ = cm.Close(ctx)
	if err != nil {
		_ = rt.Close(ctx)
		return fmt.Errorf("wasm provider: verify artifact %q: %w", p.artifact.Name, err)
	}
	scope, err := contracts.ResolveScope(family, p.artifact.Scope)
	if err != nil {
		_ = rt.Close(ctx)
		return fmt.Errorf("wasm provider: resolve scope: %w", err)
	}
	if scope != contracts.ScopeMarket {
		_ = rt.Close(ctx)
		return fmt.Errorf("wasm provider: scope=%s is not wired yet (symbol-scope sharding is QT-25); compile the artifact as scope=market", scope)
	}

	budget, err := wasm.BudgetForScope(scope)
	if err != nil {
		_ = rt.Close(ctx)
		return err
	}
	sess, err := sb.NewOnBarSessionWithBudget(ctx, p.artifact.Bytes, budget)
	if err != nil {
		_ = rt.Close(ctx)
		return fmt.Errorf("wasm provider: load artifact %q: %w", p.artifact.Name, err)
	}

	paramsJSON, err := json.Marshal(meta.Params)
	if err != nil {
		_ = sess.Close()
		_ = rt.Close(ctx)
		return fmt.Errorf("wasm provider: marshal params: %w", err)
	}
	if err := sess.Initialize(ctx, paramsJSON); err != nil {
		_ = sess.Close()
		_ = rt.Close(ctx)
		return fmt.Errorf("wasm provider: initialize: %w", err)
	}

	bc := &wasm.BarContext{
		Axis:    append([]time.Time(nil), meta.Axis...),
		Symbols: append([]string(nil), meta.RunUniverse...),
		Bars:    make([][]domain.OHLCV, len(meta.RunUniverse)),
		State:   make(map[string][]byte),
	}
	if decl, ok := sess.HostRegion(); ok {
		bc.ScratchPtr = decl.ScratchPtr
		bc.ScratchSize = decl.ScratchSize
	}

	p.rt = rt
	p.sess = sess
	p.meta = meta
	p.axis = append([]time.Time(nil), meta.Axis...)
	p.symbols = append([]string(nil), meta.RunUniverse...)
	p.bars = make([][]domain.OHLCV, len(meta.RunUniverse))
	p.bc = bc
	p.final = false
	p.closed = false
	return nil
}

// Signals 推进到 asOf 对应的 Axis 下标，执行 on_bar，并（在最后一个交易日）
// 执行 finalize。finalize 发出的信号归最后一日、在期末强平之前交给引擎。
func (p *wasmSignalProvider) Signals(ctx context.Context, scope strategy.Scope, asOf time.Time) ([]strategy.Signal, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.sess == nil || p.bc == nil {
		return nil, fmt.Errorf("wasm provider: Signals before BeginRun")
	}
	t := axisIndex(p.axis, asOf)
	if t < 0 {
		return nil, fmt.Errorf("wasm provider: as_of %s is not on the run axis", asOf.Format("2006-01-02"))
	}

	// 累积行情：引擎给的是「截至当日」的切片；停牌日缺 symbol 时保留上一份，
	// 让 get_bar2 按日期查不到并返回 NaN（不重排索引）。
	for i, sym := range p.symbols {
		if bars, ok := scope.MarketData[sym]; ok && len(bars) > 0 {
			p.bars[i] = bars
		}
	}
	p.bc.T = int32(t)
	p.bc.Bars = p.bars
	p.bc.Signals = nil
	p.bc.Targets = nil

	if err := p.sess.OnBar(ctx, p.bc); err != nil {
		return nil, err
	}
	out := convertWASEmission(p.symbols, p.bc.Signals, p.bc.Targets, asOf)

	// finalize 归属最后一日（QT-24）：返回值是诊断字节，信号在 bc 里。
	if t == len(p.axis)-1 && !p.final {
		// 关键：先清掉 on_bar 已经转换过的 emission，再接 finalize 的 ——
		// 否则最后一天的正常信号会被转换两次（Qt-21 合并后 Strength 翻倍，
		// risk sizing 的 cap 跟着翻倍，Go/WASM 对拍立刻失真）。
		p.bc.Signals = nil
		p.bc.Targets = nil
		diag, err := p.sess.Finalize(ctx, p.bc)
		if err != nil {
			return nil, err
		}
		if len(diag) > 0 {
			p.logger.Debug().Str("run_id", p.meta.RunID).Bytes("finalize_diagnostics", diag).
				Msg("wasm finalize diagnostics (not a signal channel)")
		}
		out = append(out, convertWASEmission(p.symbols, p.bc.Signals, p.bc.Targets, asOf)...)
		p.final = true
	}
	p.bc.Signals = nil
	p.bc.Targets = nil
	return out, nil
}

// EndRun 关闭会话与运行时。幂等。
func (p *wasmSignalProvider) EndRun(ctx context.Context, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	var firstErr error
	if p.sess != nil {
		if err := p.sess.Close(); err != nil {
			firstErr = err
		}
		p.sess = nil
	}
	if p.rt != nil {
		if err := p.rt.Close(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
		p.rt = nil
	}
	return firstErr
}

// axisIndex 返回 asOf 在 Axis 上的下标（UTC 日粒度）；不在轴上返回 -1。
func axisIndex(axis []time.Time, asOf time.Time) int {
	target := dayKeyUTC(asOf)
	i := sort.Search(len(axis), func(i int) bool { return dayKeyUTC(axis[i]) >= target })
	if i < len(axis) && dayKeyUTC(axis[i]) == target {
		return i
	}
	return -1
}

func dayKeyUTC(t time.Time) int64 {
	u := t.UTC()
	return int64(u.Year()*10000 + int(u.Month())*100 + u.Day())
}

// convertWASEmission 把 host 收集的两套 emission 折成 SignalSpec 的
// strategy.Signal。
//
//   - emit_signal：Action→Direction（0=hold 跳过），Strength 原样；
//   - emit_target2：weight -1=不动（跳过）、0=清仓、>0=多头目标权重；
//     Metadata 带 target_weight + sizing_source=emit_target2，供引擎按
//     目标权重计算手数并保留 policy cap。
func convertWASEmission(symbols []string, sigs []wasm.EmittedSignal, targets []wasm.EmittedTarget, asOf time.Time) []strategy.Signal {
	out := make([]strategy.Signal, 0, len(sigs)+len(targets))
	for _, es := range sigs {
		if es.SymbolIdx < 0 || int(es.SymbolIdx) >= len(symbols) {
			continue // host 已 trap；这里是纵深防御
		}
		dir, err := contracts.Action(es.Action).Direction()
		if err != nil || dir == domain.DirectionHold {
			continue
		}
		out = append(out, strategy.Signal{
			Symbol:    symbols[es.SymbolIdx],
			Direction: dir,
			Strength:  es.Strength,
			Date:      asOf,
		})
	}
	for _, et := range targets {
		if et.SymbolIdx < 0 || int(et.SymbolIdx) >= len(symbols) {
			continue
		}
		w := et.Weight
		if w == -1 { // 不动
			continue
		}
		dir := domain.DirectionLong
		switch {
		case w == 0:
			dir = domain.DirectionClose
		case w < 0:
			dir = domain.DirectionShort
		}
		out = append(out, strategy.Signal{
			Symbol:    symbols[et.SymbolIdx],
			Direction: dir,
			Strength:  math.Abs(w),
			Date:      asOf,
			Metadata: map[string]interface{}{
				signalMetaTargetWeight: w,
				signalMetaSizingSource: sizingSourceEmitTarget2,
			},
		})
	}
	return out
}
