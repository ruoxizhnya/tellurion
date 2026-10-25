package backtest

// 资源准入（QT-12 ④⑤ · A10 的另一半）。
//
// A10 把 L1 缓存从「一份账」改成「N 份账」之后，内存问题换了形态：并发 run
// 各持一份缓存，没有上限就是「跑几个就 OOM」。所以隔离与准入必须同一笔做
// —— 只隔离不限并发，等于把 OOM 从慢性改成急性。
//
// 三层闸，按任务单的降级顺序：
//
//	① 降 W        并行取数度收到 1（峰值少 (W-1) 份在途标的）
//	② 降预加载窗口  只预加载区间前段，其余日子退回 provider（**结果不变**，
//	                 只是慢；这条是整个降级设计成立的前提）
//	③ 拒绝        还是装不下 → 明确拒绝，不排队到死
//
// 排队是**有界**的：拿到槽位前先抢一个排队许可，抢不到当场拒 —— 无界队列
// 只是把 OOM 换成 goroutine 堆积，且调用方看不出区别。
//
// 禁止用全局锁串行化 RunBacktest（任务单 ④ 原文）：那会同时废掉并发跑批
// 与「同参数可复现」两件事，且把结构性缺陷藏成绩效问题。

import (
	"context"
	"errors"
	"fmt"
	"math"
	"runtime"
	"sync"
	"time"

	apperrors "github.com/ruoxizhnya/tellurion-kit/errors"
	"github.com/spf13/viper"
	"tellurion/pkg/backtest/cache"
)

// admissionFromViper 从 config 读准入闸的四个旋钮（QT-12 ④）。
//
// 没配的键保持默认档（min(GOMAXPROCS,4) + 单 run 1 GiB / 进程 4 GiB）——
// 「有闸但很宽」是默认，不是没有闸；收紧档位才是配置要做的事。
// max_queued_runs 用 IsSet 判定：0 是合法档位（不给排队，满了直接拒）。
func admissionFromViper(v *viper.Viper) admissionConfig {
	cfg := defaultAdmissionConfig()
	if n := v.GetInt("backtest.max_concurrent_runs"); n > 0 {
		cfg.maxConcurrentRuns = n
	}
	if v.IsSet("backtest.max_queued_runs") {
		cfg.maxQueuedRuns = v.GetInt("backtest.max_queued_runs")
	}
	if n := v.GetInt64("backtest.run_memory_budget_bytes"); n > 0 {
		cfg.maxRunBytes = n
	}
	if n := v.GetInt64("backtest.process_memory_budget_bytes"); n > 0 {
		cfg.maxProcessBytes = n
	}
	return cfg
}

// ErrRunQueueFull 是「排队位也满了」的拒绝。
//
// 它与 ErrRunBudgetExceeded 必须可区分：前者是「现在忙，稍后再试」，
// 后者是「这笔本身装不下，重试也没用」—— 调用方（HTTP / job 重试策略）
// 对这两者的处理相反。
var ErrRunQueueFull = errors.New("backtest: run queue is full")

// ErrRunBudgetExceeded 是「降级到地板仍装不下」的拒绝。
var ErrRunBudgetExceeded = errors.New("backtest: run exceeds memory budget after degradation")

// minWarmWindowDays 是降级的地板：窗口再收就没有预热意义了（一天都不留
// 等于全走 provider，那和关掉缓存是一回事，留给调用方显式决定）。
const minWarmWindowDays = 30

// admissionConfig 是准入闸的四个旋钮（全部可由 config 覆盖，见 options.go）。
type admissionConfig struct {
	// maxConcurrentRuns 是同时在跑的 run 上限（默认 min(GOMAXPROCS, 4)）。
	maxConcurrentRuns int
	// maxQueuedRuns 是等待槽位的排队上限（超过即 ErrRunQueueFull）。
	maxQueuedRuns int
	// maxRunBytes 是单 run 的缓存预算（估算口径见 estimateRunBytes）。
	maxRunBytes int64
	// maxProcessBytes 是进程内所有在跑 run 的缓存总账。
	maxProcessBytes int64
}

// defaultAdmissionConfig 给出「单人实验室开箱就能跑、不需要调参」的档位。
//
// 预算按 L1 缓存的量级取：单 run 1 GiB、进程 4 GiB —— 5000 标的 × 250 天
// 的日线全量约 250 MiB（bar 宽度由 reflect 现算），正常研究任务碰不到闸。
// 收紧档位是配置的事，不是默认值的事；默认值放松不等于没有闸。
func defaultAdmissionConfig() admissionConfig {
	maxRuns := runtime.GOMAXPROCS(0)
	if maxRuns > 4 {
		maxRuns = 4
	}
	if maxRuns < 1 {
		maxRuns = 1
	}
	return admissionConfig{
		maxConcurrentRuns: maxRuns,
		maxQueuedRuns:     maxRuns * 4,
		maxRunBytes:       1 << 30,
		maxProcessBytes:   4 << 30,
	}
}

// RunObserver 是准入与缓存账的观察面（QT-12 ⑤）。
//
// 只放接口不引依赖：pkg/backtest 不许 import pkg/observability
// （口径同 nautilus.RunObserver —— *observability.Metrics 隐式实现本接口，
// 编译期断言在 cmd/analysis/setup.go）。
type RunObserver interface {
	// SetRunsActive / SetRunsQueued 对应 backtest_runs_active /
	// backtest_runs_queued（瞬时量，Gauge）。
	SetRunsActive(v float64)
	SetRunsQueued(v float64)
	// SetCacheBytes 按 kind 上报 backtest_cache_bytes{kind}
	// （kind ∈ ohlcv / factor / intraday / fundamentals；值是所有在跑
	// run 该 kind 之和，不是最后一笔 run 的）。
	SetCacheBytes(kind string, v float64)
	// SetWasmBytes 上报 backtest_wasm_bytes。今天仓内没有 wasm 信号供给
	// （接线是 QT-26），值恒 0 —— 见 Engine.wasmBytesAccounting 的理由。
	SetWasmBytes(v float64)
}

// runAdmission 是准入闸本体。
//
// 并发模型：槽位与排队许可都用**带缓冲 channel**，而不是 mutex+cond ——
// cond 不认 ctx.Done()，取消一个正在排队的 run 会把它挂死。
// active / queued 直接读 `len(chan)`：这就是 channel 的语义本身，
// 另设计数器只会多一个可以和真实队列漂移的账。
type runAdmission struct {
	cfg      admissionConfig
	slots    chan struct{}
	queue    chan struct{}
	observer RunObserver

	mu        sync.Mutex
	committed int64 // 已承诺的估算字节（所有在跑 run 之和）

	// ledger 是「每个在跑 run 当前占了多少缓存字节」的台账，按 kind 求和
	// 后报给 observer（QT-12 ⑤）。run 收尾必须撤账（retireRun），否则
	// 指标会在内存已释放后继续报着那份大小。
	ledger cacheLedger
}

func newRunAdmission(cfg admissionConfig, observer RunObserver) *runAdmission {
	def := defaultAdmissionConfig()
	if cfg.maxConcurrentRuns <= 0 {
		cfg.maxConcurrentRuns = def.maxConcurrentRuns
	}
	if cfg.maxQueuedRuns < 0 {
		cfg.maxQueuedRuns = 0
	}
	if cfg.maxRunBytes <= 0 {
		cfg.maxRunBytes = def.maxRunBytes
	}
	if cfg.maxProcessBytes <= 0 {
		cfg.maxProcessBytes = def.maxProcessBytes
	}
	return &runAdmission{
		cfg:      cfg,
		slots:    make(chan struct{}, cfg.maxConcurrentRuns),
		queue:    make(chan struct{}, cfg.maxQueuedRuns),
		observer: observer,
	}
}

// estimateRunBytes 估算「symbols × days 的日线全量 + 在途取数」需要多少字节。
//
// 估算器在 cache 包（三种数据形态共用一组常数，见 cache.EstimateOHLCVBytes）。
// 这里加的是在途余量：W 个 worker 同时各持一只票的全区间结果，峰值多
// (W-1) 份 —— 这正是「降 W」能省下的那部分。
func estimateRunBytes(symbols, days, workers int) int64 {
	if symbols <= 0 || days <= 0 {
		return 0
	}
	base := cache.EstimateOHLCVBytes(int64(symbols), int64(days))
	w := workers
	if w < 1 {
		w = 1
	}
	extra := int64(w-1) * cache.EstimateOHLCVBytes(1, int64(days))
	// 溢出保护：int64 相加在极端参数下可能翻负，翻负等于「预算永远够」。
	if extra > math.MaxInt64-base {
		return math.MaxInt64
	}
	return base + extra
}

// reserve 把 want 字节记进进程账；装不下返回 false 且**不记账**。
func (a *runAdmission) reserve(want int64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if want > a.cfg.maxRunBytes {
		return false
	}
	// 先减后比：写成 committed+want > max 在极端值下会溢出。
	if a.committed > a.cfg.maxProcessBytes-want {
		return false
	}
	a.committed += want
	return true
}

func (a *runAdmission) unreserve(want int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.committed -= want
	if a.committed < 0 {
		a.committed = 0
	}
}

// acquire 排队拿槽位。调用方必须 release 返回的凭证。
//
// 顺序是「先抢排队许可，再等槽位」：反过来会让被拒绝的 run 已经占住一个
// 槽位又得吐回去，队列长度也就不可信了。
//
// maxQueuedRuns=0 是单独一条路径，不走「先抢排队许可」：排队 channel 容量 0
// 时 send 永远失败，照原路径走等于把**所有** run 锁在门外 —— 哪怕槽位空着。
// 那一档的语义是「不排队」，不是「不让跑」。
func (a *runAdmission) acquire(ctx context.Context) (*admissionLease, error) {
	a.mu.Lock()
	queueLimit := a.cfg.maxQueuedRuns
	slotLimit := a.cfg.maxConcurrentRuns
	a.mu.Unlock()

	if queueLimit <= 0 {
		select {
		case a.slots <- struct{}{}:
			a.publishCounts()
			return &admissionLease{gate: a}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			return nil, apperrors.Wrap(ErrRunQueueFull, apperrors.ErrCodeRateLimit,
				fmt.Sprintf("backtest: all %d run slots busy and queuing is disabled (max_queued_runs=0) — retry later",
					slotLimit), "RunBacktest")
		}
	}

	select {
	case a.queue <- struct{}{}:
		a.publishCounts()
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		return nil, apperrors.Wrap(ErrRunQueueFull, apperrors.ErrCodeRateLimit,
			fmt.Sprintf("backtest: run queue full (%d waiting, limit %d) — retry later",
				len(a.queue), queueLimit), "RunBacktest")
	}

	// 排队许可只在这次 acquire 期间有效：拿到槽位或失败都要退回。
	defer a.returnQueueToken()

	select {
	case a.slots <- struct{}{}:
		a.publishCounts()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	return &admissionLease{gate: a}, nil
}

func (a *runAdmission) returnQueueToken() {
	select {
	case <-a.queue:
	default:
	}
	a.publishCounts()
}

// admissionLease 是一次准入的凭证。释放幂等（defer 与显式 release 都可能走到）。
type admissionLease struct {
	gate    *runAdmission
	release sync.Once
}

// Release 归还槽位并扣回内存账。
//
// 传的是准入时记下的那个数（rc.wantBytes）：reserve/unreserve 必须成对，
// 中途换成「实际字节」会让账本对不上（跑一半就取消的 run 实际才填了一半，
// 按实际退账等于把峰值挤到别人头上）。估算与实际的差正是预算该收紧的证据，
// 留在日志里而不是吞掉。
func (l *admissionLease) Release(bookedBytes int64) {
	if l == nil {
		return
	}
	l.release.Do(func() {
		l.gate.unreserve(bookedBytes)
		<-l.gate.slots
		l.gate.publishCounts()
	})
}

// ─── 装配期档位改写（options.go 的 With* 落点）────────────────────────
//
// 一律持 mu：档位写在 Engine.mu 里挡不住已经开跑的 run 读它（虽然
// 「只在装配期调」是约定，但约定不是互斥）。四个方法都在换 channel 之前
// 把 cfg 与 channel 一起改，避免出现「上限写着 2、槽位 channel 还有 4 格」。

func (a *runAdmission) setMaxConcurrentRuns(n int) {
	if n < 1 {
		n = 1
	}
	a.mu.Lock()
	a.cfg.maxConcurrentRuns = n
	a.slots = make(chan struct{}, n)
	a.mu.Unlock()
}

func (a *runAdmission) setMaxQueuedRuns(n int) {
	if n < 0 {
		n = 0
	}
	a.mu.Lock()
	a.cfg.maxQueuedRuns = n
	a.queue = make(chan struct{}, n)
	a.mu.Unlock()
}

func (a *runAdmission) setRunBudget(bytes int64) {
	a.mu.Lock()
	a.cfg.maxRunBytes = bytes
	a.mu.Unlock()
}

func (a *runAdmission) setProcessBudget(bytes int64) {
	a.mu.Lock()
	a.cfg.maxProcessBytes = bytes
	a.mu.Unlock()
}

// attachObserver 挂上观察面（WithRunObserver 的落点，装配期调用）。
//
// 挂完立刻报一次：不报的话 backtest_runs_active / backtest_runs_queued 这两条序列在第一个
// run 收尾之前根本不存在，看板上是一片空白而不是 0 —— 而「空白」和
// 「没接观察者」在 Grafana 上长得一模一样。
func (a *runAdmission) attachObserver(ob RunObserver) {
	a.mu.Lock()
	a.observer = ob
	a.mu.Unlock()
	a.publishCounts()
}

// publishCounts 把两个瞬时量报给观察面。无观察者时静默（单测与零配置路径）。
//
// 读 observer / len(chan) 都在 mu 内：观察者是装配期经 WithRunObserver
// 挂上来的，读它发生在 run 收尾 —— 不加锁就是一次真实的并发读写，
// Prometheus 的 Set 则在锁外做（持锁调外部代码容易养出死锁）。
func (a *runAdmission) publishCounts() {
	a.mu.Lock()
	ob := a.observer
	active := len(a.slots)
	queued := len(a.queue)
	a.mu.Unlock()

	if ob == nil {
		return
	}
	ob.SetRunsActive(float64(active))
	ob.SetRunsQueued(float64(queued))
}

// publishCacheBytes 把本 run 的缓存账记进进程台账，再按 kind 报总数
// （QT-12 ⑤ 的 backtest_cache_bytes）。
//
// 报的是**所有在跑 run 之和**，不是「刚才那个 run 用了多少」：单个 run 收尾
// 时把自己的那份报出去，看板上留下的是最后一笔的数字 —— 那既不是当前占用、
// 也不是历史峰值，是个没法解释的数。台账（ledger）才是能读的量。
func (a *runAdmission) publishCacheBytes(rc *runContext) {
	if a == nil {
		return
	}
	rc.mu.RLock()
	intraday := rc.intraday
	fundamentals := rc.fundamentals
	rc.mu.RUnlock()

	entry := cacheKinds{
		"ohlcv":        rc.cache.Bytes(),
		"factor":       rc.factor.Bytes(),
		"intraday":     cache.OHLCVMapBytes(intraday),
		"fundamentals": cache.FundamentalsBytes(fundamentals),
	}

	a.mu.Lock()
	if a.ledger == nil {
		a.ledger = make(cacheLedger)
	}
	a.ledger[rc] = entry
	sums := a.ledger.sumsLocked()
	ob := a.observer
	a.mu.Unlock()

	if ob == nil {
		return
	}
	// wasm 不进台账：注入的取数闭包报的是**进程内** wasm 会话的总量，
	// 按 run 相加会把同一个数加 N 遍。
	wasm := rc.wasmBytes()
	for kind, v := range sums {
		ob.SetCacheBytes(kind, float64(v))
	}
	ob.SetWasmBytes(float64(wasm))
}

// retireRun 把结束的 run 从台账撤下并重报余量。
//
// 少了这一步，最后一个 run 收尾后 backtest_cache_bytes 会永远停在它那份
// 大小上 —— 内存早就还了，看板还说进程占着 900 MiB。这种「只在下降时出错」
// 的指标比没有指标更坏。
func (a *runAdmission) retireRun(rc *runContext) {
	if a == nil {
		return
	}
	a.mu.Lock()
	delete(a.ledger, rc)
	sums := a.ledger.sumsLocked()
	ob := a.observer
	a.mu.Unlock()

	if ob == nil {
		return
	}
	for kind, v := range sums {
		ob.SetCacheBytes(kind, float64(v))
	}
}

// cacheKinds 是一个 run 名下按 kind 的缓存字节。
type cacheKinds map[string]int64

// cacheLedger 是进程台账：每个在跑 run 一份 cacheKinds。
// 键用 *runContext 而不是 run id —— 这份账只活到 run 收尾，而收尾
// （rc.close）手上拿的就是当初记账的那个对象，不需要再造一个标识。
type cacheLedger map[*runContext]cacheKinds

// sumsLocked 把台账按 kind 求和。调用方须持有 runAdmission.mu。
//
// 空台账返回全 0 —— 不是返回空 map：「回到零点」要把各条序列都写回 0，
// 返回空等于把上一条读数原样留在看板上。
func (l cacheLedger) sumsLocked() cacheKinds {
	out := cacheKinds{
		"ohlcv":        0,
		"factor":       0,
		"intraday":     0,
		"fundamentals": 0,
	}
	for _, entry := range l {
		for kind, v := range entry {
			out[kind] += v
		}
	}
	return out
}

// spanDays 把回放区间折成日历天上界（准入估算用，不是交易日数）。
//
// 为什么可以用上界：交易日只会更少，估大只会更早降级，方向是安全的 ——
// 反过来（估小了）才会让真峰值撞破预算。
func spanDays(start, end time.Time) int {
	if end.Before(start) {
		return 0
	}
	return int(end.Sub(start).Hours()/24) + 1
}

// applyAdmission 在**预热之前**决定这笔 run 的档位：记账成功即返回；
// 装不下就按「降 W → 降窗口 → 拒绝」依次让步。每次让步都打日志
// —— 否则「为什么我的回测只预加载了前半段」是个无法回答的问题。
func (rc *runContext) applyAdmission(symbols []string) error {
	a := rc.e.admission
	if a == nil {
		return nil // 准入闸关闭（WithRunAdmissionDisabled）
	}

	days := spanDays(rc.start, rc.end)
	if days <= 0 || len(symbols) == 0 {
		return nil // 空 run 不占内存账，也不该被闸挡住（与 warmCache 的口径一致）
	}

	a.mu.Lock()
	runBudget := a.cfg.maxRunBytes
	processBudget := a.cfg.maxProcessBytes
	a.mu.Unlock()

	// 两档降级 + 最后一次判定 = 三次循环封顶；default 分支自己返回，
	// 循环上界只是「绝不无限让步」的兜底。
	for attempt := 0; attempt < 3; attempt++ {
		want := rc.estimateFor(symbols, days)
		if a.reserve(want) {
			rc.wantBytes = want
			return nil
		}
		switch {
		case rc.workers > 1:
			from := rc.workers
			rc.workers = 1
			rc.e.logger.Warn().
				Int("from_workers", from).
				Int64("estimated_bytes", want).
				Int64("run_budget_bytes", runBudget).
				Msg("QT-12 降级 ①：并行取数度收到 1（峰值内存装不下）")
		case rc.windowDays() > minWarmWindowDays:
			from := rc.warmEnd
			rc.warmEnd = rc.start.AddDate(0, 0, minWarmWindowDays-1)
			rc.e.logger.Warn().
				Time("from_warm_end", from).
				Time("to_warm_end", rc.warmEnd).
				Int64("estimated_bytes", want).
				Msg("QT-12 降级 ②：预加载窗口收缩（未预加载的日子退回 provider，结果不变、只是慢）")
		default:
			return apperrors.Wrap(ErrRunBudgetExceeded, apperrors.ErrCodeUnavailable,
				fmt.Sprintf("backtest: %d 标的 × %d 天约需 %d 字节，超过单 run 预算 %d / 进程预算 %d（已降到 W=1、窗口 %d 天仍装不下）",
					len(symbols), days, want, runBudget, processBudget, rc.windowDays()),
				"RunBacktest")
		}
	}
	return apperrors.Wrap(ErrRunBudgetExceeded, apperrors.ErrCodeUnavailable,
		"backtest: admission degradation loop exhausted without a decision", "RunBacktest")
}

// estimateFor 按当前降级档位估算本 run 的字节数（窗口与 W 都取现值）。
func (rc *runContext) estimateFor(symbols []string, days int) int64 {
	window := rc.windowDays()
	if window > days || window <= 0 {
		window = days
	}
	return estimateRunBytes(len(symbols), window, rc.workers)
}

// windowDays 返回当前预加载窗口的天数。
func (rc *runContext) windowDays() int { return spanDays(rc.start, rc.warmEnd) }

// wasmBytes 是本 run 名下 wasm 会话的线性内存字节。
//
// 今天仓内没有 wasm 信号供给（QT-26 接线），所以只有装配方经
// WithWasmMemoryAccounting 注入过账源时才有值；没注入就是 0，
// 而不是「拿不到就不报」—— 指标存在但恒 0，比指标不存在更容易被发现是空的。
func (rc *runContext) wasmBytes() int64 {
	fn := rc.e.wasmBytesAccounting
	if fn == nil {
		return 0
	}
	return fn()
}
