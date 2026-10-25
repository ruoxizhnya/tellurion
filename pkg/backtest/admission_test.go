package backtest

// QT-12 ④ 验收 · 准入拒绝路径与降级顺序。
//
// 任务单要求的第 ③ 条就是这条：**拒绝**要有测试。拒绝路径最容易写成
// 「返回一个 error 就行」，而它其实是三件事：
//   - 拒得对（满了才拒，空着不能拒）；
//   - 拒得可区分（「稍后再试」与「这笔本身装不下」调用方处理方式相反）；
//   - 拒了不留痕（不能把内存账或槽位留在那儿，那等于每次拒绝都少一格）。
//
// 降级顺序也在这里钉：降 W → 降预加载窗口 → 拒绝。顺序反了会先砍窗口
// （慢一倍）却还留着 4 个 worker 的峰值内存，正是当初要修的那一侧。

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
	apperrors "github.com/ruoxizhnya/tellurion-kit/errors"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"tellurion/pkg/backtest/cache"
	"tellurion/pkg/marketdata"
	"tellurion/pkg/strategy"
)

// gateWith 装一台只为测准入而存在的引擎：闸按给的档位来。
func gateWith(t *testing.T, cfg admissionConfig, ob RunObserver) (*Engine, *runAdmission) {
	t.Helper()
	eng := newTestEngine(t)
	eng.admission = newRunAdmission(cfg, ob)
	return eng, eng.admission
}

func params1y() domain.BacktestParams {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	return domain.BacktestParams{StartDate: start, EndDate: start.AddDate(0, 0, 364)}
}

func symbolsN(n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, "SYM"+string(rune('A'+i%26))+string(rune('0'+i/26))+".SH")
	}
	return out
}

// budgetRun 是「只测内存账、不占槽位」的一条 run。
//
// 为什么绕开槽位：applyAdmission 只动字节账，与槽位无关；把它和 acquire
// 绑在一起测，一旦用例断言失败就会连带堵死后面的 acquire（1 格闸 + 4 个用例）。
// 槽位与凭证的行为由本文件 acquire/Release 那几条单独覆盖，
// 「acquire → applyAdmission → close」的完整顺序则由
// run_context_seed_test.go 的收尾归零用例覆盖。
type budgetRun struct {
	*runContext
	refund func()
}

func newBudgetRun(t *testing.T, eng *Engine, params domain.BacktestParams, workers int) *budgetRun {
	t.Helper()
	gate := eng.admission
	require.NotNil(t, gate, "用例前提：闸必须在场（这些用例测的就是闸）")

	eng.parallelWorkers = workers
	rc := newRunContext(eng, params)
	br := &budgetRun{runContext: rc}
	var done bool
	br.refund = func() {
		if done {
			return
		}
		done = true
		gate.unreserve(rc.wantBytes)
		gate.retireRun(rc)
	}
	t.Cleanup(br.refund)
	return br
}

// ─── 槽位与队列 ────────────────────────────────────────────────────────

// max_queued_runs=0 的正确读法是「不排队」，不是「不让跑」。
//
// 这条有历史形状：早期实现让所有 run 先去抢一个排队许可，容量 0 时
// send 永远失败 —— 槽位明明空着也进不去，等于把闸拧成了死闸。
func TestAdmission_ZeroQueueLimit_StillAdmitsWhenSlotFree(t *testing.T) {
	_, gate := gateWith(t, admissionConfig{
		maxConcurrentRuns: 2,
		maxQueuedRuns:     0,
		maxRunBytes:       1 << 30,
		maxProcessBytes:   1 << 32,
	}, nil)

	lease, err := gate.acquire(context.Background())
	require.NoError(t, err, "槽位空着又没配排队，也必须能开工")
	require.NotNil(t, lease)

	lease2, err := gate.acquire(context.Background())
	require.NoError(t, err)

	_, err = gate.acquire(context.Background())
	require.Error(t, err, "两个槽位都占了还放行 = 上限没生效")
	assert.True(t, errors.Is(err, ErrRunQueueFull),
		"槽位占满的拒绝要能按 ErrRunQueueFull 判定，got %v", err)

	lease.Release(0)
	lease2.Release(0)
	third, err := gate.acquire(context.Background())
	require.NoError(t, err, "还回槽位后必须能再开工（拒绝不能是单向门）")
	third.Release(0)
}

func TestAdmission_BoundedQueueRejectsWhenFull(t *testing.T) {
	_, gate := gateWith(t, admissionConfig{
		maxConcurrentRuns: 1,
		maxQueuedRuns:     1,
		maxRunBytes:       1 << 30,
		maxProcessBytes:   1 << 32,
	}, nil)

	running, err := gate.acquire(context.Background())
	require.NoError(t, err)

	// 排队许可只有一个：第二个 run 拿到它并开始等槽位（另起 goroutine，
	// 因为它会阻塞在槽位上）。
	waiting := make(chan error, 1)
	go func() {
		_, err := gate.acquire(context.Background())
		waiting <- err
	}()

	require.Eventually(t, func() bool { return len(gate.queue) == 1 }, time.Second, 5*time.Millisecond,
		"第二个 run 应当占住唯一的排队位")

	_, err = gate.acquire(context.Background())
	require.Error(t, err, "队列只有 1 格，第三笔必须被拒")
	assert.True(t, errors.Is(err, ErrRunQueueFull),
		"拒绝类型要是 ErrRunQueueFull（「稍后再试」那一类），got %v", err)
	// 拒绝还要带上可重试的语义码：HTTP 侧靠它映射 429，而不是 500。
	var appErr *apperrors.AppError
	require.True(t, errors.As(err, &appErr), "拒绝要包成 AppError 才带得到错误码")
	assert.Equal(t, apperrors.ErrCodeRateLimit, appErr.Code)

	// 放行等待中的那一笔，并确认队列被腾空（排队许可随 acquire 返回退位）。
	running.Release(0)
	require.NoError(t, <-waiting)
	assert.Equal(t, 0, len(gate.queue), "拿到槽位后排队许可必须退回，否则队列长度不可信")
}

func TestAdmission_CtxCancelWhileWaitingIsNotRejection(t *testing.T) {
	_, gate := gateWith(t, admissionConfig{
		maxConcurrentRuns: 1,
		maxQueuedRuns:     4,
		maxRunBytes:       1 << 30,
		maxProcessBytes:   1 << 32,
	}, nil)

	held, err := gate.acquire(context.Background())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err = gate.acquire(ctx)
	assert.True(t, errors.Is(err, context.Canceled),
		"「调用方取消」不能报成「队列满」：前者是别等了，后者是稍后再试，got %v", err)
	assert.Equal(t, 0, len(gate.queue), "取消掉的 run 不许留在队列里占位")

	held.Release(0)
}

func TestAdmission_LeaseReleaseIsIdempotent(t *testing.T) {
	_, gate := gateWith(t, admissionConfig{
		maxConcurrentRuns: 1,
		maxQueuedRuns:     1,
		maxRunBytes:       1 << 30,
		maxProcessBytes:   1 << 32,
	}, nil)

	require.True(t, gate.reserve(1000), "用例前提：1000 字节要记得进账")
	lease, err := gate.acquire(context.Background())
	require.NoError(t, err)

	// 第二次 Release 必须**立刻**返回且不重复扣账：RunBacktest 里 defer 与
	// 显式 release 都可能走到，而重复从 slots 收会永久阻塞（等于整个服务
	// 从此少一格且没有任何报错）。
	done := make(chan struct{})
	go func() {
		lease.Release(1000)
		lease.Release(1000)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("第二次 Release 卡住了 —— 凭证释放不幂等，重复调用会从空 channel 收")
	}

	assert.Equal(t, int64(0), gate.committed, "退还只能扣一次")
	assert.Equal(t, 0, len(gate.slots), "槽位要还回来")
}

// ─── 内存账：降级顺序与拒绝 ───────────────────────────────────────────

// 预算刚好够 W=1 的那份估算：只能降并行度，不许动窗口。
func TestAdmission_DegradesWorkersBeforeWindow(t *testing.T) {
	params := params1y()
	symbols := symbolsN(20)
	days := spanDays(params.StartDate, params.EndDate)

	eng, _ := gateWith(t, admissionConfig{
		maxConcurrentRuns: 4,
		maxQueuedRuns:     4,
		maxRunBytes:       estimateRunBytes(len(symbols), days, 1),
		maxProcessBytes:   1 << 32,
	}, nil)
	require.Greater(t, estimateRunBytes(len(symbols), days, 4), estimateRunBytes(len(symbols), days, 1),
		"用例前提：W=4 的估算必须大于 W=1，否则这条测的是别的东西")

	rc := newBudgetRun(t, eng, params, 4)
	require.NoError(t, rc.applyAdmission(symbols))
	assert.Equal(t, 1, rc.workers, "降级第一档是并行取数度")
	assert.True(t, rc.warmEnd.Equal(params.EndDate),
		"降到 W=1 就够了，不该顺手砍预加载窗口（砍窗口会让后面每一天都退回 provider）")
}

// W 已经在地板上，才轮到砍窗口；窗口砍到 minWarmWindowDays 为止。
func TestAdmission_ShrinksWarmWindowWhenWorkersAlreadyFloor(t *testing.T) {
	params := params1y()
	symbols := symbolsN(20)
	days := spanDays(params.StartDate, params.EndDate)

	budget := estimateRunBytes(len(symbols), minWarmWindowDays, 1)
	eng, _ := gateWith(t, admissionConfig{
		maxConcurrentRuns: 4,
		maxQueuedRuns:     4,
		maxRunBytes:       budget,
		maxProcessBytes:   1 << 32,
	}, nil)
	require.Less(t, budget, estimateRunBytes(len(symbols), days, 1),
		"用例前提：预算必须真的装不下全区间，否则这条测的是别的东西")

	rc := newBudgetRun(t, eng, params, 1) // W 已经在 1
	require.NoError(t, rc.applyAdmission(symbols))
	assert.Equal(t, 1, rc.workers)
	assert.False(t, rc.warmEnd.Equal(params.EndDate), "窗口该收缩")
	assert.Equal(t, minWarmWindowDays, rc.windowDays(),
		"收缩要停在地板上（多砍一天不会更快，只会更慢）")
}

// 降到地板还装不下 → 明确拒绝，且不留痕、不粘滞。
func TestAdmission_RejectsAfterDegradationFloor(t *testing.T) {
	params := params1y()
	symbols := symbolsN(20)

	eng, gate := gateWith(t, admissionConfig{
		maxConcurrentRuns: 4,
		maxQueuedRuns:     4,
		maxRunBytes:       1, // 连一天的预加载都装不下
		maxProcessBytes:   1 << 32,
	}, nil)

	rc := newBudgetRun(t, eng, params, 8)
	err := rc.applyAdmission(symbols)
	require.Error(t, err, "预算小于一份 run 的地板还放行 = 闸形同不存在")
	assert.True(t, errors.Is(err, ErrRunBudgetExceeded),
		"装不下要报成 ErrRunBudgetExceeded（重试也没用），不是 ErrRunQueueFull，got %v", err)
	var appErr *apperrors.AppError
	require.True(t, errors.As(err, &appErr))
	assert.Equal(t, apperrors.ErrCodeUnavailable, appErr.Code,
		"这条映射成 503：客户端要能区分「稍后重试」与「这台机器装不下」")
	assert.Equal(t, 1, rc.workers, "拒绝前必须把两档降级都试过")
	assert.Equal(t, minWarmWindowDays, rc.windowDays(), "拒绝前窗口也该降到地板")

	// 拒绝之后账必须是干净的：记了账又没还 = 每拒一笔就永久少一块。
	assert.Equal(t, int64(0), gate.committed, "被拒的 run 不许在进程账上留下痕迹")

	// 把预算放宽后同一台闸仍能接单：拒绝不可累积成死锁。
	gate.setRunBudget(estimateRunBytes(len(symbols), spanDays(params.StartDate, params.EndDate), 1))
	rc2 := newBudgetRun(t, eng, params, 1)
	require.NoError(t, rc2.applyAdmission(symbols), "放宽预算后新的 run 该能开工（拒绝是判据不是状态）")
}

// 两个 run 共享进程账：第二笔装不下时必须拒，且第一笔的账不许被动过。
//
// 区间取 30 天：那是降级地板（minWarmWindowDays），第二笔**没有**再让步的
// 余地，才会走到「拒」。用一年区间测这条会得到「第二笔收缩窗口后照常
// 开工」—— 那是降级在用例里成功工作，不是拒绝，断言会假绿。
func TestAdmission_ProcessBudgetSpansRunsNotJustOne(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	params := domain.BacktestParams{StartDate: start, EndDate: start.AddDate(0, 0, minWarmWindowDays-1)}
	symbols := symbolsN(20)
	days := spanDays(params.StartDate, params.EndDate)
	require.Equal(t, minWarmWindowDays, days, "用例前提：窗口已在降级地板上")

	one := estimateRunBytes(len(symbols), days, 1)
	eng, gate := gateWith(t, admissionConfig{
		maxConcurrentRuns: 4,
		maxQueuedRuns:     4,
		maxRunBytes:       one, // 单 run 装得下
		maxProcessBytes:   one + one/2,
	}, nil)

	rcA := newBudgetRun(t, eng, params, 1)
	require.NoError(t, rcA.applyAdmission(symbols), "第一笔按单 run 预算装得下")
	require.Equal(t, one, gate.committed)

	rcB := newBudgetRun(t, eng, params, 1)
	err := rcB.applyAdmission(symbols)
	require.Error(t, err, "进程总账只够一笔半，第二笔必须被拒 —— 单 run 预算管不住并发")
	assert.True(t, errors.Is(err, ErrRunBudgetExceeded))
	assert.Equal(t, one, gate.committed, "被拒的第二笔不许改动第一笔已承诺的账")

	// 第一笔还账后，第二笔的位置才腾得出来。
	rcA.refund()
	assert.Equal(t, int64(0), gate.committed)
	rcC := newBudgetRun(t, eng, params, 1)
	require.NoError(t, rcC.applyAdmission(symbols), "前一笔还账后，新的 run 该能开工")
}

// 「装不下」与「降不到地板」之间还有一档真降级：进程账只够一笔半时，
// 第二笔靠**收缩预加载窗口**照样开工（结果不变，只是后面的日子退回 provider）。
func TestAdmission_SecondRunFitsByShrinkingWindow(t *testing.T) {
	params := params1y()
	symbols := symbolsN(20)
	days := spanDays(params.StartDate, params.EndDate)

	one := estimateRunBytes(len(symbols), days, 1)
	eng, gate := gateWith(t, admissionConfig{
		maxConcurrentRuns: 4,
		maxQueuedRuns:     4,
		maxRunBytes:       one,
		maxProcessBytes:   one + one/2,
	}, nil)

	rcA := newBudgetRun(t, eng, params, 1)
	require.NoError(t, rcA.applyAdmission(symbols))

	rcB := newBudgetRun(t, eng, params, 1)
	require.NoError(t, rcB.applyAdmission(symbols), "第二笔该靠降级窗口开工，不是被拒")
	assert.Equal(t, minWarmWindowDays, rcB.windowDays(), "让到的就是地板，别再砍")
	assert.True(t, rcB.warmEnd.Before(params.EndDate), "窗口确实收缩了")

	// 关键口径：**第一笔的档位没被第二笔动过**（降级只让步自己，不能去改
	// 已经在跑的邻居 —— 那等于把串扰从缓存搬进了准入）。
	assert.True(t, rcA.warmEnd.Equal(params.EndDate), "邻居降级不该砍掉本 run 的预加载窗口")
	assert.Equal(t, one, rcA.wantBytes, "本 run 记进的账不该被邻居改写")

	// 进程账 = 两笔各自承诺的数（第二笔按收缩后的窗口记，不是按全区间记）：
	// 记多了会把预算吃空，记少了就是闸形同虚设。
	assert.Equal(t, one+rcB.wantBytes, gate.committed, "账要按各自实际档位成对记账")
	assert.Equal(t, estimateRunBytes(len(symbols), minWarmWindowDays, 1), rcB.wantBytes)
}

func TestAdmission_EmptyRunNeverTouchesBudget(t *testing.T) {
	eng, gate := gateWith(t, admissionConfig{
		maxConcurrentRuns: 1,
		maxQueuedRuns:     1,
		maxRunBytes:       1,
		maxProcessBytes:   1,
	}, nil)

	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// 空池子在 warmCache 那里本来就是 no-op，在闸这边也一样：没什么要
	// 预加载就不该被当成「装不下」。
	rc := newBudgetRun(t, eng, params1y(), 1)
	require.NoError(t, rc.applyAdmission(nil))

	// 区间倒挂（end < start）→ spanDays 归 0 → 同样不动账。
	// 注意**同一天**不是这一档：start == end 是 1 个日历天，那笔 run 真的
	// 要预加载一天的 bar，该被闸管。
	rc2 := newBudgetRun(t, eng, domain.BacktestParams{
		StartDate: start,
		EndDate:   start.AddDate(0, 0, -1),
	}, 1)
	require.NoError(t, rc2.applyAdmission(symbolsN(5)))

	assert.Equal(t, int64(0), gate.committed)
}

func TestAdmission_DisabledGateIsANoOp(t *testing.T) {
	eng := newTestEngine(t)
	applyOptions(eng, []EngineOption{WithRunAdmissionDisabled()})

	rc := newRunContext(eng, params1y())
	require.Nil(t, eng.admission, "选项没把闸关掉")

	require.NoError(t, rc.applyAdmission(symbolsN(200)), "关掉闸之后不该有任何准入判定")
	rc.publishCacheMetrics() // 没闸也没观察者：静默，不能 panic
	rc.close()               // lease 为 nil：收尾必须是 no-op，不是 panic
}

// 10 笔并发压一台小闸：2 格在跑 + 3 格排队 + 5 笔当场被拒，一个都不能多。
//
// 这是验收 ⑤-3 的机械版本（「10 个 run 并发不超预算，其余排队/拒绝」）。
// 排队的那 3 笔必须**真的在等**而不是被拒：把有界队列做成「一律拒」，
// 客户端看到的就是一次扩容失败，而这正是队列要吸收的抖动。
func TestAdmission_TenConcurrentRuns_SplitAcrossAdmitQueueReject(t *testing.T) {
	const (
		maxRuns  = 2
		maxQueue = 3
		attempts = 10
	)
	_, gate := gateWith(t, admissionConfig{
		maxConcurrentRuns: maxRuns,
		maxQueuedRuns:     maxQueue,
		maxRunBytes:       1 << 30,
		maxProcessBytes:   1 << 34,
	}, nil)

	// 先把两个槽位占住（同步做，不留竞态）。
	leases := make([]*admissionLease, 0, maxRuns)
	for i := 0; i < maxRuns; i++ {
		l, err := gate.acquire(context.Background())
		require.NoError(t, err)
		leases = append(leases, l)
	}
	require.Equal(t, maxRuns, len(gate.slots))

	// 再放 3 笔进队列：它们阻塞在槽位上，不是被拒。
	admitted := make(chan error, attempts)
	for i := 0; i < maxQueue; i++ {
		go func() {
			l, err := gate.acquire(context.Background())
			if err == nil {
				l.Release(0)
			}
			admitted <- err
		}()
	}
	require.Eventually(t, func() bool { return len(gate.queue) == maxQueue }, 2*time.Second, 5*time.Millisecond,
		"3 笔应当都在排队，实测队列长度 %d", len(gate.queue))

	// 队列满了：剩下 10-2-3=5 笔必须**立刻**被拒（不阻塞、不排队）。
	rejected := 0
	for i := 0; i < attempts-maxRuns-maxQueue; i++ {
		start := time.Now()
		_, err := gate.acquire(context.Background())
		assert.Less(t, time.Since(start), 500*time.Millisecond, "队列已满还让调用方阻塞 = 有界队列没生效")
		if errors.Is(err, ErrRunQueueFull) {
			rejected++
		} else {
			t.Errorf("第 %d 笔应报 ErrRunQueueFull，got %v", i+1, err)
		}
	}
	assert.Equal(t, attempts-maxRuns-maxQueue, rejected, "被拒的笔数要对得上「既不在跑也没排队」")

	// 放掉一格：排队中的一笔立刻进去，队列随之短一格（容量是还回来的，不是重置出来的）。
	leases[0].Release(0)
	select {
	case err := <-admitted:
		require.NoError(t, err, "腾出一格后排队的那笔应当开工")
	case <-time.After(2 * time.Second):
		t.Fatal("槽位已腾出，排队的 run 却没进去 —— 队列与槽位没接上")
	}
	assert.Equal(t, maxRuns, len(gate.slots), "在跑数不得超过上限")

	for i := 1; i < len(leases); i++ {
		leases[i].Release(0)
	}
	for i := 0; i < maxQueue-1; i++ {
		select {
		case err := <-admitted:
			require.NoError(t, err)
		case <-time.After(2 * time.Second):
			t.Fatal("剩余排队者没能进场")
		}
	}
	require.Eventually(t, func() bool { return len(gate.slots) == 0 && len(gate.queue) == 0 },
		2*time.Second, 5*time.Millisecond, "全部收尾后槽位与队列都该空")
}

// ─── 估算本身 ─────────────────────────────────────────────────────────

func TestEstimateRunBytes_AddsInFlightHeadroomForWorkers(t *testing.T) {
	const days = 250
	base := estimateRunBytes(10, days, 1)
	two := estimateRunBytes(10, days, 2)

	assert.Greater(t, two, base,
		"W 个 worker 峰值各持一只票的全区间，估算必须把 (W-1) 份算进去 —— 否则「降 W」省不下任何预算")
	assert.Equal(t, base+cache.EstimateOHLCVBytes(1, days), two, "多出来的正好是一只票的全区间")

	assert.Equal(t, int64(0), estimateRunBytes(0, days, 4), "空标的的估算必须是 0")
	assert.Equal(t, int64(0), estimateRunBytes(10, 0, 4))
	assert.Equal(t, base, estimateRunBytes(10, days, 0), "W<=0 归一成 1，不能算成负数")
}

// 极端参数下的溢出保护：翻负等于「谁都装得下」，闸会静默失效。
func TestEstimateRunBytes_SaturatesInsteadOfOverflowing(t *testing.T) {
	got := estimateRunBytes(1<<30, 1<<30, 64)
	assert.Positive(t, got, "估算翻负 = 预算判断恒真 = 闸形同虚设")
	assert.Equal(t, int64(math.MaxInt64), got)
}

// ─── 配置读数 ─────────────────────────────────────────────────────────

func TestAdmissionFromViper_DefaultsAndOverrides(t *testing.T) {
	v := viper.New()
	cfg := admissionFromViper(v)
	def := defaultAdmissionConfig()
	assert.Equal(t, def.maxConcurrentRuns, cfg.maxConcurrentRuns, "没配就得落在默认档上")
	assert.Equal(t, def.maxQueuedRuns, cfg.maxQueuedRuns)

	v.Set("backtest.max_concurrent_runs", 2)
	v.Set("backtest.max_queued_runs", 0) // 0 是合法档位，不能被「没配」吃掉
	v.Set("backtest.run_memory_budget_bytes", int64(1024))
	v.Set("backtest.process_memory_budget_bytes", int64(4096))
	got := admissionFromViper(v)
	assert.Equal(t, 2, got.maxConcurrentRuns)
	assert.Equal(t, 0, got.maxQueuedRuns, "max_queued_runs=0 必须读成 0（不给排队），不是回落默认")
	assert.Equal(t, int64(1024), got.maxRunBytes)
	assert.Equal(t, int64(4096), got.maxProcessBytes)

	// 负数与 0 一样落回默认：那更可能是写错了，而不是「我要关掉」。
	v2 := viper.New()
	v2.Set("backtest.run_memory_budget_bytes", int64(-5))
	assert.Equal(t, def.maxRunBytes, admissionFromViper(v2).maxRunBytes)
}

func TestOptions_ReshapeGateAndRespectDisabled(t *testing.T) {
	eng := newTestEngine(t)
	require.NotNil(t, eng.admission)

	applyOptions(eng, []EngineOption{WithMaxConcurrentRuns(3), WithRunQueueLimit(7)})
	assert.Equal(t, 3, eng.admission.cfg.maxConcurrentRuns)
	assert.Equal(t, 3, cap(eng.admission.slots))
	assert.Equal(t, 7, eng.admission.cfg.maxQueuedRuns)
	assert.Equal(t, 7, cap(eng.admission.queue))

	// <=0 归一成 1（串行），不是「关掉」。
	applyOptions(eng, []EngineOption{WithMaxConcurrentRuns(0)})
	assert.Equal(t, 1, eng.admission.cfg.maxConcurrentRuns)

	// 关掉之后任何档位选项都不该把闸复活 —— 那会让 WithRunAdmissionDisabled
	// 的效果取决于选项顺序之外的东西。
	applyOptions(eng, []EngineOption{
		WithRunAdmissionDisabled(),
		WithMaxConcurrentRuns(2),
		WithRunObserver(newRecordingObserver()),
	})
	assert.Nil(t, eng.admission, "档位选项把已关闭的闸复活了")
}

func TestOptions_MemoryBudgetsAndWasmAccounting(t *testing.T) {
	eng := newTestEngine(t)
	applyOptions(eng, []EngineOption{
		WithRunMemoryBudget(1234),
		WithProcessMemoryBudget(5678),
		WithWasmMemoryAccounting(func() int64 { return 42 }),
	})
	assert.Equal(t, int64(1234), eng.admission.cfg.maxRunBytes)
	assert.Equal(t, int64(5678), eng.admission.cfg.maxProcessBytes)

	rec := newRecordingObserver()
	eng.SetRunObserver(rec)
	rc := newRunContext(eng, params1y())
	rc.publishCacheMetrics()
	require.NotEmpty(t, rec.wasmBytes, "注入了取数闭包就要报，不能静默")
	assert.Equal(t, int64(42), rec.wasmBytes[len(rec.wasmBytes)-1])
	rc.close()
}

// 观察面挂上之后，缓存四项 kind 必须**各自**都有读数：
// 少报一项不会报错，只会在看板上永久空白 —— 那种缺失没人会发现。
func TestOptions_RunObserverReportsAllCacheKinds(t *testing.T) {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	prov := marketdata.NewInMemoryProvider()
	prov.LoadOHLCV("600000.SH", barsFor("600000.SH", 10, start))

	eng := newTestEngineWithProvider(t, prov)
	rec := newRecordingObserver()
	eng.SetRunObserver(rec)

	params := domain.BacktestParams{StartDate: start, EndDate: start.AddDate(0, 0, 9)}
	rc := newRunContext(eng, params)
	lease, err := eng.admission.acquire(context.Background())
	require.NoError(t, err)
	rc.lease = lease

	require.NoError(t, rc.warmCache(context.Background(), []string{"600000.SH"}))
	pe := 12.5
	rc.mu.Lock()
	rc.fundamentals = map[string]strategy.FundamentalSeries{
		"600519.SH": {{Symbol: "600519.SH", Date: start, PE: &pe}},
	}
	rc.intraday = map[string][]domain.OHLCV{"600000.SH": barsFor("600000.SH", 3, start)}
	rc.mu.Unlock()
	rc.publishCacheMetrics()

	for _, kind := range []string{"ohlcv", "factor", "intraday", "fundamentals"} {
		assert.True(t, rec.seenCacheBytes(kind), "kind=%s 一条读数都没报过 —— 指标面缺了一项", kind)
	}
	assert.Greater(t, rec.lastCacheBytes("ohlcv"), int64(0), "预热过的 OHLCV 账不该是 0")
	assert.Greater(t, rec.lastCacheBytes("intraday"), int64(0))
	assert.Greater(t, rec.lastCacheBytes("fundamentals"), int64(0))
	assert.Zero(t, rec.lastCacheBytes("factor"), "没预热的因子账就该如实报 0，不是不报")

	// 台账报的是**和**，不是最后一笔。两条 run 同时在跑时，只看到其中一条
	// 的读数就是「cache_bytes 到底是谁的」这个无法回答的问题；写成覆盖
	// （out[kind] = v）在今天这条单 run 用例里照样绿，所以必须有第二条 run。
	rc2 := newRunContext(eng, params)
	lease2, err := eng.admission.acquire(context.Background())
	require.NoError(t, err)
	rc2.lease = lease2
	require.NoError(t, rc2.warmCache(context.Background(), []string{"600000.SH"}))
	rc2.publishCacheMetrics()

	sum := rc.cache.Bytes() + rc2.cache.Bytes()
	assert.Equal(t, sum, rec.lastCacheBytes("ohlcv"),
		"在跑 run 的缓存账要按 kind 求和（两条 run 各自 %d + %d）", rc.cache.Bytes(), rc2.cache.Bytes())

	rc.close()
	// 一条收尾后，余量应当只剩另一条 —— 归零要分两步走才对得上台账。
	assert.Equal(t, rc2.cache.Bytes(), rec.lastCacheBytes("ohlcv"), "撤账撤多了/少了都不行")

	rc2.close()
	for _, kind := range []string{"ohlcv", "factor", "intraday", "fundamentals"} {
		assert.Zero(t, rec.lastCacheBytes(kind), "两条 run 都收尾后每一项都要回到 0，kind=%s", kind)
	}
	assert.Equal(t, int64(0), eng.admission.committed)
	assert.Len(t, eng.admission.ledger, 0)
}

// 真跑一场回测：跑的时候要有读数，跑完必须归零。
//
// 为什么单靠上面那些直接调用 publishCacheMetrics 的用例不够：引擎里
// 「预热收尾报一次」那一行（runBacktestInternal）本身没有任何用例经过它。
// 删掉那一行，run 期间看板一直是 0，只有 close 撤账时才有读数 —— 而
// 「活着的 run 占了多少内存」正是这个指标要回答的唯一问题。
func TestAdmission_LiveRunPublishesCacheLedger(t *testing.T) {
	const sym = "600519.SH"
	daily := map[string][]domain.OHLCV{
		sym: {
			d6DailyBar(sym, d6Day, 10.00, 10.60, 9.98, 10.50),
			d6DailyBar(sym, d6Day.AddDate(0, 0, 1), 10.50, 10.55, 10.20, 10.22),
			d6DailyBar(sym, d6Day.AddDate(0, 0, 2), 10.22, 10.30, 10.10, 10.15),
		},
	}
	eng := d6Engine(t, daily)
	rec := newRecordingObserver()
	eng.SetRunObserver(rec)

	_, err := eng.RunBacktest(context.Background(), BacktestRequest{
		Strategy:       "momentum",
		StockPool:      []string{sym},
		StartDate:      "2024-01-02",
		EndDate:        "2024-01-04",
		InitialCapital: 1_000_000,
		RiskFreeRate:   0.03,
	})
	require.NoError(t, err)

	require.NotEmpty(t, rec.runsActive)
	assert.Contains(t, rec.runsActive, 1, "回测在跑的那一刻必须报得出 runs_active=1")
	assert.Equal(t, 0, rec.runsActive[len(rec.runsActive)-1], "runs_active 收尾归零")

	var peak int64
	for _, v := range rec.cacheBytes["ohlcv"] {
		if v > peak {
			peak = v
		}
	}
	assert.Greater(t, peak, int64(0),
		"整场回测里 backtest_cache_bytes{{kind=ohlcv}} 一直是 0 —— 引擎预热后没报账，看板上看不见活着的 run")
	assert.Zero(t, rec.lastCacheBytes("ohlcv"), "run 结束后要回到 0")

	assert.Equal(t, int64(0), eng.admission.committed, "进程内存账要成对归还")
	assert.Len(t, eng.admission.ledger, 0, "缓存台账不能有残留")
}
