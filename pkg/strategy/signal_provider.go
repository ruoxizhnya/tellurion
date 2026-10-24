// SignalProvider 契约（K8 · SignalSpec）——引擎取信号的唯一入口。
//
// 设计文档 §2.2：内核不关心信号从哪来（本地注册表 / 外部模型信号表 / 将来
// 阶段 2 的 HTTP、Redis 远程供给），只认这份契约。接口只有一个方法：
//
//	Signals(ctx, scope, asOf) ([]Signal, error)
//
// ─── 契约级语义：未来信号物理不可见 ────────────────────────────────
// 返回的信号必须满足 as_of <= asOf（按 DayOf 日粒度比较）。这是防前视的
// 契约边界——任何实现返回未来信号即违约（验收破坏腿①）。LocalSignalProvider
// 在输出前经 RejectFutureSignals fail-loud 把关；将来的远程实现复用同一守卫。
//
// 落位：pkg/strategy——Signal 类型与注册表都在这里，接口与它们同域；
// pkg/backtest 只依赖接口（依赖方向 backtest → strategy，不反向）。
package strategy

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/ruoxizhnya/tellurion-contracts/engine"
	apperrors "tellurion/pkg/errors"
)

// Scope 是一次取信号调用的输入快照（契约参数包）。
type Scope struct {
	// Strategy 是注册表键（策略名）。
	Strategy string
	// Universe 是本次运行的标的池。
	Universe []string
	// MarketData 是「截至 asOf」的累积行情（symbol → 按日期升序的 OHLCV）。
	MarketData map[string][]domain.OHLCV
	// Portfolio 是调用方在 asOf 时刻的组合快照（批式 GenerateSignals 的
	// 第二参数）。流式路径不用；调用方没有 tracker 时可传 nil。
	Portfolio *domain.Portfolio
}

// SignalProvider 是可插拔信号供给契约（K8 · SignalSpec，设计文档 §2.2）。
type SignalProvider interface {
	// Signals 返回 as_of <= asOf 的信号（DayOf 日粒度）。未来信号物理
	// 不可见是契约级语义——返回未来信号即违约，调用方有权 fail-loud。
	Signals(ctx context.Context, scope Scope, asOf time.Time) ([]Signal, error)
}

// RunMeta 是一次 run 的身份与输入快照（QT-14）。BeginRun 收到它之后实现方
// 应把它当成**不可变**的：同一 runID 的会话期内字段不许被改写。
type RunMeta struct {
	// RunID 复用引擎生成的 backtestID（Engine.RunBacktest 里 uuid 或请求
	// 指定的 RunID）。空 RunID 的 BeginRun 违约。
	RunID string
	// Strategy 是注册表键（策略名）。
	Strategy string
	// Params 是解析后的生效参数（QT-20）：策略清单默认值 ⊕ run 覆写。
	// provider 在 BeginRun 时收下它，并在为本 run 构造策略实例时通过
	// Configure 注入 —— 这是 Go 内置策略的 `initialize(paramsJSON)` 等价物。
	Params map[string]any
	// Start / End 是本 run 的回放区间（UTC 零点）。
	Start, End time.Time
	// Universe 是本 run 的标的池。
	Universe []string
}

// SessionLifecycle 是有状态 provider 必须实现的生命周期契约（QT-14）。
//
// 语义（p0-remediation-design.md A1，非可选）：
//   - BeginRun 在 run 的任何 Signals 调用**之前**被调用；实现方在内部
//     `map[runID]*session`（加锁）里开一份会话；
//   - EndRun = finalize() + Close()：冲刷/清点的信号归属该 runID，**不许
//     跨 run 合并**；实现方销毁该 runID 的会话与其内部状态；
//   - 会话数有上限（建议 ≤ 并发 run 上限 × 1.2），超限 **fail-closed 不排队**
//     —— 悄悄阻塞等于把串状态问题换成死锁问题；
//   - 顺序 run 也必须走 BeginRun/EndRun，不做「顺序复用会话」的优化，
//     否则残留状态回归（正是本契约要消灭的东西）。
type SessionLifecycle interface {
	BeginRun(ctx context.Context, meta RunMeta) error
	EndRun(ctx context.Context, runID string) error
}

// Stateful 是 provider 的显式状态声明（QT-14）：Stateful()==true 的实现
// **必须**同时实现 SessionLifecycle，否则装配期当场失败
// （ValidateSessionLifecycle / bootstrap Fatal）——不做「可选接口 + 静默降级」，
// 因为串状态的后果是结果随并发顺序变，跑起来才发现就晚了。
type Stateful interface{ Stateful() bool }

// ValidateSessionLifecycle 装配期校验（QT-14 ⑤）：Stateful()==true 但未实现
// SessionLifecycle ⇒ 返回错误（装配点应据此拒绝构造，bootstrap 直接 Fatal）。
// 非 nil 且无状态（或未声明 Stateful）的 provider 恒通过。
func ValidateSessionLifecycle(p SignalProvider) error {
	if p == nil {
		return nil
	}
	sf, ok := p.(Stateful)
	if !ok || !sf.Stateful() {
		return nil
	}
	if _, ok := p.(SessionLifecycle); !ok {
		return fmt.Errorf(
			"strategy: signal provider %T declares Stateful() but does not implement SessionLifecycle (BeginRun/EndRun) — 有状态 provider 必须随 run 开关会话，否则实例状态跨 run 泄漏；要么实现 SessionLifecycle，要么如实声明 Stateful()==false（QT-14 ⑤ 装配期 fail-closed）",
			p)
	}
	return nil
}

// ─── 编译期合规检查（破坏验证腿②）──────────────────────────────────
// 删掉接口任一方法 → 方法表达式守卫编译失败（样板同 streaming.go）。
var (
	_ SignalProvider   = (*LocalSignalProvider)(nil)
	_ Stateful         = (*LocalSignalProvider)(nil)
	_ SessionLifecycle = (*LocalSignalProvider)(nil)

	_ func(SignalProvider, context.Context, Scope, time.Time) ([]Signal, error) = SignalProvider.Signals
	_ func(SessionLifecycle, context.Context, RunMeta) error                    = SessionLifecycle.BeginRun
	_ func(SessionLifecycle, context.Context, string) error                     = SessionLifecycle.EndRun
	_ func(Stateful) bool                                                       = Stateful.Stateful
)

// RejectFutureSignals fail-loud 拒绝任何 as_of > asOf 的信号（DayOf 日粒度）。
//
// 与 signal_strategy.go 的 rejectFutureSignals 同一裁决口径：未来信号不是
// 静默跳过而是报错——静默跳过会让「偷看未来」变成「悄悄少下单」，结果错了
// 还查不出来。Date 非 time.Time 或零值的信号跳过（无锚点的信号由调用方归
// asOf 当日，物理不可能是未来）。
func RejectFutureSignals(sigs []Signal, asOf time.Time) error {
	limit := DayOf(asOf)
	for _, s := range sigs {
		d, ok := s.Date.(time.Time)
		if !ok || d.IsZero() {
			continue
		}
		if DayOf(d).After(limit) {
			return fmt.Errorf(
				"strategy: 信号未来日期被拒 (date=%s > 当前回放 %s, symbol=%s)——防前视",
				d.UTC().Format(time.RFC3339), asOf.UTC().Format(time.RFC3339), s.Symbol)
		}
	}
	return nil
}

// errRunParamsRejected 标记「策略实例拒绝了本 run 的参数」（QT-20）。
//
// 它与「策略未注册」是两类错误：前者是装配/参数问题，不该被包装成
// NotFound 把调用方引去查拼写；用 errors.Is 在 Signals 里分流。
var errRunParamsRejected = errors.New("strategy: run params rejected")

// LocalSignalProvider 是 SignalProvider 的本地实现（K8 ③.2）——包装现有
// 注册表 + 批式/流式双模式调用，行为与旧 pkg/backtest 的
// getSignalsFromLocalStrategy 逐字节一致（错误消息、错误码、op 均原样保留）。
//
// ⚠️ **一个实例只服务一个 run**（QT-11）。策略实例在本对象里按名字缓存，
// 所以同一个 provider 跨日复用是刻意的：流式（BarHandler）策略的状态必须
// 在一个 run 内活着。反过来，**每个 run 必须新建一个**——共享一个
// LocalSignalProvider 跑两笔，缓存就把第一笔的策略实例递给了第二笔，
// 正是 QT-11 要消灭的串状态。引擎侧的对应纪律见
// `Engine.newRunSignalProvider`。
//
// QT-14 把上面那句「必须新建一个」从注释升级成了代码：本类型如实声明
// Stateful 并实现 SessionLifecycle（会话上限 1），`Signals` 在会话未开时
// 直接拒绝 —— 忘了 Begin/End 的共享 provider 当场报错，而不是悄悄串状态。
type LocalSignalProvider struct {
	registry     *Registry
	factorReader FactorZScoreReader
	fundamentals func() map[string]FundamentalSeries
	// intraday 是日内分钟特征供给（D6）。nil = 引擎没接日内快照
	// （没配 Router / 没预热），IntradayAware 策略拿不到供给，
	// 用到特征时明确失败（契约注释口径）。
	intraday IntradayFeatureProvider

	// mu 护 instances 与 activeRun：run 内是单线程日循环，但 provider 是
	// 导出类型，调用方并发用（= 误用）时不能 data race。
	mu        sync.Mutex
	instances map[string]Strategy
	// params 是本 run 的生效参数（QT-20），BeginRun 收下、EndRun 清空。
	// 构造本 run 的策略实例时一次注入 Configure —— 之后每次 Signals 不再
	// 重复 Configure（策略的参数在一个 run 内不可变，重复注入只会白付重建）。
	params map[string]any
	// activeRun 是当前打开的会话（QT-14）。空串 = 会话未开；非空 = 已
	// BeginRun 且未 EndRun。**上限 1**：一个 LocalSignalProvider 的实例
	// 缓存就是一份 run 的状态，同时服务两个 run 等于把两笔 run 的流式
	// 状态记进同一批实例 —— 所以第二个 BeginRun 直接报错（fail-closed
	// 不排队）。引擎按 run 构造 provider（QT-11），生产路径永远打不到
	// 这堵墙；打到了就是有人在跨 run 共享 provider。
	activeRun string
}

// NewLocalSignalProvider 构造本地实现。registry 传 nil 表示调用时取
// DefaultRegistry（延迟取：兼容 Init() 重建 DefaultRegistry 的时序）。
func NewLocalSignalProvider(registry *Registry) *LocalSignalProvider {
	return &LocalSignalProvider{
		registry:  registry,
		instances: make(map[string]Strategy),
	}
}

// strategyFor 取本 run 用的策略实例：首调用经注册表构造函数拿一份新实例，
// 之后的调用复用同一份（流式状态跨日存活）。
//
// 唯一取实例的入口走 `Registry.NewInstance`；以共享实例注册的策略
// （插件 / 测试替身漏迁移）在这里 fail-closed 报错，错误里点名 remedy——
// 悄悄退回共享实例等于把 QT-11 白做。
func (p *LocalSignalProvider) strategyFor(name string) (Strategy, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if s, ok := p.instances[name]; ok {
		return s, nil
	}

	reg := p.registry
	if reg == nil {
		reg = DefaultRegistry
	}
	s, err := reg.NewInstance(name)
	if err != nil {
		return nil, err
	}
	// QT-20：把本 run 的生效参数注入策略实例。没声明参数（params 为空）
	// 时保持既有行为 —— 不调 Configure，内置策略继续用编译期默认值。
	// 不可配置的策略收到非空参数时不静默丢弃：参数是调用方明确表达的意图，
	// 丢掉它等于「以为生效其实没生效」，直接拒绝本 run。
	if len(p.params) > 0 {
		c := AsConfigurable(s)
		if c == nil {
			return nil, fmt.Errorf("%w: strategy %q does not accept parameters (no Configure/Parameters), but the run supplied %d override(s)",
				errRunParamsRejected, name, len(p.params))
		}
		if err := c.Configure(p.params); err != nil {
			return nil, fmt.Errorf("%w: strategy %q rejected the run params: %v",
				errRunParamsRejected, name, err)
		}
	}
	p.instances[name] = s
	return s, nil
}

// SetFactorReader 绑定因子 z-score 读取器（引擎装配期绑 method value，
// 对应 FactorAware 注入，同旧实现每调用注入一次）。
func (p *LocalSignalProvider) SetFactorReader(r FactorZScoreReader) {
	p.factorReader = r
}

// SetFundamentalsProvider 绑定基本面快照供给（对应 FundamentalAware 注入）。
func (p *LocalSignalProvider) SetFundamentalsProvider(f func() map[string]FundamentalSeries) {
	p.fundamentals = f
}

// SetIntradayProvider 绑定日内分钟特征供给（D6，对应 IntradayAware 注入）。
// 引擎每个交易日绑一个「当日绑定」的 provider（快照 + 回放日），
// 与 factor/fundamentals 的每调用注入同节奏。
func (p *LocalSignalProvider) SetIntradayProvider(ip IntradayFeatureProvider) {
	p.intraday = ip
}

// Stateful 如实声明：本 provider 的实例缓存（流式策略的递推状态）就是
// 状态。正因为有状态，它实现了 SessionLifecycle —— 会话不开就不服务。
func (p *LocalSignalProvider) Stateful() bool { return true }

// BeginRun 开启本 run 的会话（QT-14）。上限 1：并发第二个 BeginRun 直接
// 失败（fail-closed 不排队，见 activeRun 字段注释）。
func (p *LocalSignalProvider) BeginRun(_ context.Context, meta RunMeta) error {
	if meta.RunID == "" {
		return classifyExecutionError(contracts.ErrContractBreach,
			fmt.Errorf("strategy: BeginRun: RunMeta.RunID is empty — 会话必须有 run 身份（QT-14）"))
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.activeRun != "" {
		return classifyExecutionError(contracts.ErrContractBreach, fmt.Errorf(
			"strategy: BeginRun(%s) rejected: session %q is still active — 一个 LocalSignalProvider 同时只服务一个 run（会话上限 1，超限 fail-closed 不排队）；并发 run 请各持一份 provider（引擎按 run 构造，QT-11/QT-14）",
			meta.RunID, p.activeRun))
	}
	p.activeRun = meta.RunID
	p.params = copyRunParams(meta.Params)
	return nil
}

// EndRun 结束会话（QT-14 ③：finalize + Close）。对本实现而言 finalize 的
// 形态是：逐实例 Cleanup + 清空实例缓存 —— 流式缓冲随实例销毁，信号归属
// 本 runID，绝不跨 run 合并；下一个 run 从空缓存重新构造实例。
func (p *LocalSignalProvider) EndRun(_ context.Context, runID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.activeRun == "" {
		return classifyExecutionError(contracts.ErrContractBreach,
			fmt.Errorf("strategy: EndRun(%s) without an active session（QT-14）", runID))
	}
	if p.activeRun != runID {
		return classifyExecutionError(contracts.ErrContractBreach,
			fmt.Errorf("strategy: EndRun(%s) but the active session is %q — 会话身份不匹配（QT-14）", runID, p.activeRun))
	}
	for name, s := range p.instances {
		s.Cleanup()
		delete(p.instances, name)
	}
	p.activeRun = ""
	p.params = nil
	return nil
}

// copyRunParams 复制一份 run 参数快照：meta 的 map 属于调用方，provider
// 不该持有可变引用（调用方事后改 map 会把「本 run 的参数」改掉）。
func copyRunParams(params map[string]any) map[string]any {
	if len(params) == 0 {
		return nil
	}
	out := make(map[string]any, len(params))
	for k, v := range params {
		out[k] = v
	}
	return out
}

// Signals 实现 SignalProvider 契约。
//
// 双模式检测顺序「优先流式 BarHandler，否则批式 SignalGenerator」的结构保证
// （K2 切片 2 原注释）：注册表存的是 Strategy 复合接口，它不嵌入 BarHandler，
// 现有批式策略的类型断言恒假、必然落到批式分支——这是类型系统的结构保证，
// 不靠运行时标志。只有同时实现 Strategy 与 BarHandler 的双模式策略才走流式。
func (p *LocalSignalProvider) Signals(ctx context.Context, scope Scope, asOf time.Time) ([]Signal, error) {
	// QT-14 会话门：无会话即拒绝，不静默服务。放开这道门，跨 run 共享
	// 一个 provider 的调用方就回到「实例缓存把上一笔的状态递给下一笔」
	// 的老路 —— 错误结果不报错，比报错更糟。
	p.mu.Lock()
	active := p.activeRun
	p.mu.Unlock()
	if active == "" {
		return nil, classifyExecutionError(contracts.ErrContractBreach, fmt.Errorf(
			"strategy: Signals called before BeginRun — provider 会话未开启（QT-14）：有状态 provider 必须经 BeginRun/EndRun 走完一个 run 的生命周期，否则实例缓存的流式状态会跨 run 泄漏"))
	}

	strat, err := p.strategyFor(scope.Strategy)
	if err != nil {
		if errors.Is(err, errRunParamsRejected) {
			// 参数被策略拒绝：这是本 run 的输入问题，不是「策略不存在」。
			return nil, classifyExecutionError(contracts.ErrStrategyFault,
				apperrors.Wrap(err, apperrors.ErrCodeInvalidInput,
					fmt.Sprintf("local strategy %q rejected the run params (QT-20)", scope.Strategy), "getSignals"))
		}
		if errors.Is(err, ErrSharedStrategy) {
			// 装配错误，不是「策略不存在」：报 NOT_FOUND 会把人引去查名字拼写，
			// 而真正要改的是注册方式。
			return nil, classifyExecutionError(contracts.ErrContractBreach,
				apperrors.Wrap(err, apperrors.ErrCodeInternal,
					fmt.Sprintf("strategy %q is registered as a shared instance; per-run execution needs strategy.RegisterFactory (QT-11)",
						scope.Strategy), "getSignals"))
		}
		// fail-loud（OBS-11）：未注册策略名是装配错误。错误消息逐字节
		// 保留旧 getSignals 口径（含 op "getSignals"）。
		return nil, classifyExecutionError(contracts.ErrContractBreach,
			apperrors.Wrap(err, apperrors.ErrCodeNotFound,
				fmt.Sprintf("strategy %q not registered (strategy-service signal fallback removed per OBS-11/ADR-012); available: %v",
					scope.Strategy, ListStrategies()), "getSignals"))
	}

	if fa, ok := strat.(FactorAware); ok && p.factorReader != nil {
		fa.SetFactorCache(p.factorReader)
	}
	if fu, ok := strat.(FundamentalAware); ok && p.fundamentals != nil {
		fu.SetFundamentals(p.fundamentals())
	}
	// D6：日内特征注入。nil 供给时不注入 —— 策略保留旧状态（没拿到
	// 就没有），用到特征时明确失败，不塞空 provider 假装有数据。
	if ia, ok := strat.(IntradayAware); ok && p.intraday != nil {
		ia.SetIntradayFeatures(p.intraday)
	}

	var signals []Signal

	if bh, ok := strat.(BarHandler); ok {
		// ── 流式路径 ─────────────────────────────────────────────
		// 喂序确定性（裁决：按 symbol 字典序）：marketData 是 map，Go 的
		// range 遍历顺序是随机的——同一份输入两次跑会喂出不同的 bar 序列，
		// 任何 per-symbol 递推状态的方向因此不确定，回测不可复现。
		for _, symbol := range sortedSymbols(scope.MarketData) {
			bars := scope.MarketData[symbol]
			if len(bars) == 0 {
				continue
			}
			bar := bars[len(bars)-1]
			// 只喂「当日」bar。marketData 是「截至当日」的累积历史，本方法
			// 每个交易日被调用一次。若当日该 symbol 无新 bar（停牌），其最后
			// 一根是更早的日期——重复喂会让递推状态吞下一根陈旧 bar。故按
			// DayOf 比对，非当日则跳过（不喂 = 这天没有新信息）。
			if !DayOf(bar.Date).Equal(DayOf(asOf)) {
				continue
			}
			if err := bh.OnBar(ctx, bar); err != nil {
				return nil, classifyExecutionError(contracts.ErrStrategyFault,
					apperrors.Wrap(err, apperrors.ErrCodeInternal,
						fmt.Sprintf("local streaming strategy %s OnBar failed at %s", scope.Strategy, bar.Date.Format("2006-01-02")),
						"getSignals"))
			}
		}

		// 喂完当日全部 bar 后取走信号（取走即清空，防重复消费）。
		// Warmup 处理（裁决：引擎不设 warmup 门，依赖策略自守）：
		// Warmup() 是「静态声明需求」而非引擎维护的计数器，实现方负责
		// 据此自守，供给侧只负责喂 bar + 取走。
		signals = bh.Signals()
	} else {
		// ── 批式路径：一行未改 ───────────────────────────────────
		// 组合快照由调用方在 Scope 里给（旧实现的 extractLatestPrices +
		// tracker.GetPortfolio 提前到了调用方，GetPortfolio 是纯读）。
		signals, err = strat.GenerateSignals(ctx, scope.MarketData, scope.Portfolio)
		if err != nil {
			return nil, classifyExecutionError(contracts.ErrStrategyFault,
				apperrors.Wrap(err, apperrors.ErrCodeInternal,
					fmt.Sprintf("local strategy %s failed", scope.Strategy), "getSignals"))
		}
	}

	// 契约级防前视把关（破坏验证腿①的对象）：未来信号在这里被拒，
	// 不静默放行——删掉本调用，契约拒绝测试变红。
	if err := RejectFutureSignals(signals, asOf); err != nil {
		return nil, classifyExecutionError(contracts.ErrContractBreach, err)
	}
	return signals, nil
}

// sortedSymbols 返回 map 的键，按字典序排好（喂序确定性）。generic 小助手
// 各包一份是既有惯例（pkg/backtest、pkg/backtest/tracker、pkg/live 同名同形）。
func sortedSymbols[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
