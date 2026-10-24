package walkforward

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/ruoxizhnya/tellurion-contracts/engine"
	"tellurion/pkg/backtest/workers"
	"tellurion/pkg/statistics"
	"tellurion/pkg/storage"
)

// WalkForwardEngine runs walk-forward validation for a strategy.
type WalkForwardEngine struct {
	// runnerFactory 为每个窗口提供一个执行引擎。
	//
	// 历史：这里曾存一个共享 runner，所有窗口复用同一个 Engine，于是引擎级
	// 缓存（OHLCV / 因子 / 基本面 / 上市日历）跨窗口污染 —— 因子缓存尤其危险，
	// 它是「整体替换」语义（f.cache = combined），窗口 B 的 Warm 会覆盖窗口 A
	// 正在读取的缓存，A 于是读到别的窗口日期区间的因子。
	//
	// QT-12 把那件事修在了引擎里：可变运行态住在一个 run 一个的 runContext，
	// Engine 只持装配期种子。所以本工厂**不再是防污染的必要条件**，
	// 复用同一实例也不会重现那个 bug（防回归的钉子见 run_context_seed_test.go
	// 「A 带财报 / B 不带」那条，它跑的就是共享 Engine）。
	// 工厂保留下来是为了另一件事：每窗口一份干净的 state store 与桥接组件。
	runnerFactory func() (contracts.EngineRunner, error)
	store         *storage.PostgresStore
	logger        zerolog.Logger
}

// NewWalkForwardEngine creates a new WalkForwardEngine.
//
// runnerFactory 为每个窗口返回一个 runner；返回错误时该窗口被跳过，不影响其他
// 窗口。工厂可以返回共享实例 —— QT-12 之后那不会造成跨窗口污染（历史上的
// 污染根因已在引擎内修掉，理由见 WalkForwardEngine.runnerFactory）。
func NewWalkForwardEngine(runnerFactory func() (contracts.EngineRunner, error), store *storage.PostgresStore, logger zerolog.Logger) *WalkForwardEngine {
	return &WalkForwardEngine{
		runnerFactory: runnerFactory,
		store:         store,
		logger:        logger.With().Str("component", "walkforward").Logger(),
	}
}

// GetLatestReport returns the latest walk-forward report for a strategy.
func (wf *WalkForwardEngine) GetLatestReport(ctx context.Context, strategyID string) (*domain.WalkForwardReport, error) {
	return wf.store.GetLatestWalkForwardReport(ctx, strategyID)
}

// ListReports returns all walk-forward reports, limited to n most recent.
func (wf *WalkForwardEngine) ListReports(ctx context.Context, limit int) ([]*domain.WalkForwardReport, error) {
	return wf.store.ListAllWalkForwardReports(ctx, limit)
}

// WalkForwardRequest holds parameters for a walk-forward validation run.
type WalkForwardRequest struct {
	Strategy       string   `json:"strategy" binding:"required"`
	StockPool      []string `json:"stock_pool" binding:"required"`
	StartDate      string   `json:"start_date" binding:"required"`
	EndDate        string   `json:"end_date" binding:"required"`
	InitialCapital float64  `json:"initial_capital"`
	RiskFreeRate   float64  `json:"risk_free_rate"`

	// StrategyParams 是本次 walk-forward 的 run 级参数覆写（QT-68；与
	// contracts.BacktestRequest.StrategyParams 同形）。每个窗口的 train/test
	// 两段回测都**原样带上**它 —— 生效值仍由引擎在 RunBacktest 入口按
	// 「清单默认 ⊕ run 覆写」解析（QT-20），本层不复制合并逻辑。
	StrategyParams map[string]any `json:"strategy_params,omitempty"`

	WalkForwardParams domain.WalkForwardParams `json:"walk_forward_params"`
}

// RunWalkForward runs a rolling-origin segmented backtest for a strategy
// across consecutive time windows.
//
// REV-20（名实相符）：这里**没有**参数优化/校准步骤——train 与 test 两段用
// **同一策略、同一参数**各跑一段独立回测（见 runSingleWindow：trainReq 与
// testReq 只差日期区间）。因此本报告是「分时段稳健性概览」（rolling-origin
// evaluation），不是 walk-forward optimization，下面的 degradation /
// overfit score 也只是两段独立回测的比值启发式，**不是**过拟合的统计度量。
// 真正的「train 上优化参数 → test 上验证」流水线是另一个量级的工程
// （参数优化引擎），已另立设计单；在那之前，字段名与文档按事实表述。
//
// Algorithm:
//  1. Get all trading days in [fullStart, fullEnd]
//  2. Build rolling windows: segment A=[a_start, a_end], segment B=[b_start, b_end]
//  3. For each window: run the SAME strategy with the SAME params on segment A,
//     then independently on segment B
//  4. Compute oosVsTrain = segmentB_Sharpe / segmentA_Sharpe for each window
//  5. Aggregate: avg segment-B Sharpe, avg ratio, pass rate, heuristic scores
func (wf *WalkForwardEngine) RunWalkForward(ctx context.Context, req WalkForwardRequest) (*domain.WalkForwardReport, error) {
	// QT-68：strategy_params 的形状校验与同步回测同一道闸门（contracts 的
	// ValidateRunSpec：只接受浅层标量）。放在任何取数之前，直接调用方也能
	// 在启动窗口之前拿到点名错误；HTTP 侧另有 handler 的语义校验（未知键 /
	// 类型 / 范围），二者合起来就是 QT-20 的两层口径。
	if err := (contracts.BacktestRequest{
		Strategy:       req.Strategy,
		StrategyParams: req.StrategyParams,
	}).ValidateRunSpec(); err != nil {
		return nil, fmt.Errorf("walk-forward: %w", err)
	}

	params := req.WalkForwardParams

	if params.TrainDays <= 0 {
		return nil, fmt.Errorf("train_days must be positive")
	}
	if params.TestDays <= 0 {
		return nil, fmt.Errorf("test_days must be positive")
	}
	if params.StepDays <= 0 {
		return nil, fmt.Errorf("step_days must be positive")
	}
	if params.MinTrainDays <= 0 {
		params.MinTrainDays = params.TrainDays / 2
	}

	startDate, err := time.Parse("2006-01-02", req.StartDate)
	if err != nil {
		return nil, fmt.Errorf("invalid start_date format: %w", err)
	}
	endDate, err := time.Parse("2006-01-02", req.EndDate)
	if err != nil {
		return nil, fmt.Errorf("invalid end_date format: %w", err)
	}

	tradingDays, err := wf.store.GetTradingDates(ctx, startDate, endDate)
	if err != nil {
		return nil, fmt.Errorf("failed to get trading days: %w", err)
	}

	if len(tradingDays) < params.TrainDays+params.TestDays {
		return nil, fmt.Errorf("insufficient trading days: need at least %d, got %d",
			params.TrainDays+params.TestDays, len(tradingDays))
	}

	windows := wf.buildWindows(tradingDays, params)
	if len(windows) == 0 {
		return nil, fmt.Errorf("no walk-forward windows could be generated")
	}

	wf.logger.Info().
		Int("windows", len(windows)).
		Int("symbols", len(req.StockPool)).
		Str("range", req.StartDate+" ~ "+req.EndDate).
		Msg("Starting walk-forward validation")

	report := &domain.WalkForwardReport{
		StrategyID: req.Strategy,
		Universe:   fmt.Sprintf("%d symbols", len(req.StockPool)),
		Windows:    make([]*domain.WalkForwardResult, 0, len(windows)),
	}

	results := wf.runWindowsParallel(ctx, req, windows)

	for _, r := range results {
		if r != nil {
			report.Windows = append(report.Windows, r)
		}
	}

	if len(report.Windows) == 0 {
		return nil, fmt.Errorf("all walk-forward windows failed")
	}

	wf.computeAggregateMetrics(report)
	wf.detectOverfitting(report)

	if err := wf.store.SaveWalkForwardReport(ctx, report); err != nil {
		wf.logger.Warn().Err(err).Msg("Failed to save walk-forward report to DB")
	}

	return report, nil
}

// runWindowsParallel executes all walk-forward windows concurrently.
// Each window runs its train + test backtests sequentially (they share the same engine),
// but different windows run in parallel goroutines.
func (wf *WalkForwardEngine) runWindowsParallel(
	ctx context.Context,
	req WalkForwardRequest,
	windows []wfWindow,
) []*domain.WalkForwardResult {
	var mu sync.Mutex
	results := make([]*domain.WalkForwardResult, len(windows))

	// P1-6：固定 worker pool。此前是「每窗口一个 goroutine + 信号量限并发」，
	// goroutine 数 = 窗口数（长回测可以轻松上千）。
	//
	// 窗口之间靠 runContext 隔离（QT-12）：每次 RunBacktest 建一份自己的
	// 缓存，runner 是共享实例也不会互相覆盖 —— 历史上这里靠「每窗口一个
	// 引擎实例」防污染，那个根因已经在引擎里修掉了。
	// train + test 的先后在 runSingleWindow 内部保证，二者共用同一窗口的
	// runner 是刻意的 —— 它们属于同一次拟合/验证，而 L1 OHLCV 缓存按日期
	// 区间合并（不替换），train 段已预热的 bar 对 test 段是净收益。
	const wfConcurrency = 4
	workers.RunWorkers(ctx, wfConcurrency, windows,
		func(ctx context.Context, idx int, w wfWindow) {
			runner, err := wf.runnerFactory()
			if err != nil {
				wf.logger.Warn().Err(err).Int("window", idx+1).
					Msg("Failed to construct runner for window — skipping")
				return
			}
			r := wf.runSingleWindow(ctx, req, idx, len(windows), w, runner)
			mu.Lock()
			results[idx] = r
			mu.Unlock()
		})

	valid := make([]*domain.WalkForwardResult, 0, len(results))
	for _, r := range results {
		if r != nil {
			valid = append(valid, r)
		}
	}
	return valid
}

// runSingleWindow executes train + test backtests for one walk-forward window.
func (wf *WalkForwardEngine) runSingleWindow(
	ctx context.Context,
	req WalkForwardRequest,
	windowIdx int,
	totalWindows int,
	win wfWindow,
	runner contracts.EngineRunner,
) *domain.WalkForwardResult {
	wf.logger.Info().
		Int("window", windowIdx+1).
		Int("total", totalWindows). // REV-20：此前误打 windowIdx，total 恒为 0..n-1 里的下标
		Str("train", win.trainStart.Format("2006-01-02")+"~"+win.trainEnd.Format("2006-01-02")).
		Str("test", win.testStart.Format("2006-01-02")+"~"+win.testEnd.Format("2006-01-02")).
		Msg("Running rolling-origin window (same strategy/params on both segments)")

	capital := req.InitialCapital
	if capital <= 0 {
		capital = 1000000.0
	}
	riskFree := req.RiskFreeRate

	trainReq := contracts.BacktestRequest{
		Strategy:       req.Strategy,
		StockPool:      req.StockPool,
		StartDate:      win.trainStart.Format("2006-01-02"),
		EndDate:        win.trainEnd.Format("2006-01-02"),
		InitialCapital: capital,
		RiskFreeRate:   riskFree,
		StrategyParams: req.StrategyParams,
	}

	testReq := contracts.BacktestRequest{
		Strategy:       req.Strategy,
		StockPool:      req.StockPool,
		StartDate:      win.testStart.Format("2006-01-02"),
		EndDate:        win.testEnd.Format("2006-01-02"),
		InitialCapital: capital,
		RiskFreeRate:   riskFree,
		StrategyParams: req.StrategyParams,
	}

	if runner == nil {
		wf.logger.Warn().Int("window", windowIdx+1).
			Msg("Runner factory returned a nil runner — skipping window")
		return nil
	}

	trainResp, err := runner.RunBacktest(ctx, trainReq)
	if err != nil {
		wf.logger.Warn().Err(err).Int("window", windowIdx+1).Msg("Train backtest failed")
		return nil
	}

	testResp, err := runner.RunBacktest(ctx, testReq)
	if err != nil {
		wf.logger.Warn().Err(err).Int("window", windowIdx+1).Msg("Test backtest failed")
		return nil
	}

	trainBR := wf.toBacktestResult(trainResp)
	testBR := wf.toBacktestResult(testResp)

	trainSharpe := trainBR.SharpeRatio
	testSharpe := testBR.SharpeRatio
	// REV-20 语义澄清：两段是**同参数独立回测**，这个比值只回答「B 段行情里
	// 策略表现是 A 段的几倍」，不回答「有没有过拟合」——没有优化步骤就
	// 谈不上 in-sample 拟合。字段名 OOSvsTrain 是历史遗留（json tag 不动，
	// 避免破坏 API 兼容），含义以此注释为准。
	oosVsTrain := math.NaN()
	if trainSharpe > 1e-9 {
		oosVsTrain = testSharpe / trainSharpe
	}

	return &domain.WalkForwardResult{
		WindowIndex:     windowIdx + 1,
		TrainStart:      win.trainStart.Format("2006-01-02"),
		TrainEnd:        win.trainEnd.Format("2006-01-02"),
		TestStart:       win.testStart.Format("2006-01-02"),
		TestEnd:         win.testEnd.Format("2006-01-02"),
		TrainResult:     trainBR,
		TestResult:      testBR,
		TrainSharpe:     trainSharpe,
		TestSharpe:      testSharpe,
		TestReturn:      testBR.AnnualReturn,
		TestMaxDrawdown: testBR.MaxDrawdown,
		OOSvsTrain:      oosVsTrain,
	}
}

// computeAggregateMetrics calculates summary statistics from all windows.
func (wf *WalkForwardEngine) computeAggregateMetrics(report *domain.WalkForwardReport) {
	n := len(report.Windows)
	if n == 0 {
		return
	}

	var sumTestSharpe, sumTestReturn, sumTestMaxDD, sumDegradation float64
	var sharpes []float64
	degradations := make([]float64, 0, n)
	positiveCount := 0

	for _, w := range report.Windows {
		sumTestSharpe += w.TestSharpe
		sumTestReturn += w.TestReturn
		sumTestMaxDD += w.TestMaxDrawdown
		sumDegradation += w.OOSvsTrain
		sharpes = append(sharpes, w.TestSharpe)
		if !math.IsNaN(w.OOSvsTrain) {
			degradations = append(degradations, w.OOSvsTrain)
		}
		if w.TestSharpe > 0 {
			positiveCount++
		}
	}

	fn := float64(n)
	report.AvgTestSharpe = sumTestSharpe / fn
	report.AvgTestReturn = sumTestReturn / fn
	report.AvgTestMaxDD = sumTestMaxDD / fn
	report.AvgDegradation = sumDegradation / fn
	report.PassRate = float64(positiveCount) / fn

	report.StdTestSharpe = statistics.SampleStdDev(sharpes)
	if len(degradations) > 0 {
		report.StdDegradation = statistics.SampleStdDev(degradations)
	}

	report.OverallPass = report.AvgTestSharpe > 0.5 && report.AvgDegradation < 0.7
}

// detectOverfitting 把分段比值压成 0~1 的启发式分数。
//
// REV-20 语义澄清：本函数**不是**过拟合的统计检验——输入的两段 Sharpe 来自
// 同参数独立回测，不存在 in-sample 拟合，「过拟合」无从谈起。分数度量的是
// 「B 段相对 A 段的衰减程度」这一稳健性启发式；等真正的参数优化引擎落地
// （train 上拟合、test 上验证），这里才有资格叫 overfit detection。名称
// detectOverfitting 与 OverfitScore/ProbNoOverfit 字段是历史遗留（json tag
// 不动），改名的 API 破坏面由 (b) 设计单一并评估。
func (wf *WalkForwardEngine) detectOverfitting(report *domain.WalkForwardReport) {
	n := len(report.Windows)
	if n < 2 {
		report.OverfitScore = 0.5
		report.ProbNoOverfit = 0.5
		report.StabilityScore = 0.5
		return
	}

	avgDegrade := report.AvgDegradation
	stdDegrade := report.StdDegradation
	avgOOS := report.AvgTestSharpe
	stdOOS := report.StdTestSharpe

	overfitScore := 0.0

	if avgDegrade >= 1.0 {
		overfitScore = 0.0
	} else if avgDegrade >= 0.7 {
		overfitScore = 0.2
	} else if avgDegrade >= 0.5 {
		overfitScore = 0.5
	} else if avgDegrade >= 0.3 {
		overfitScore = 0.75
	} else {
		overfitScore = 1.0
	}

	if stdDegrade > 0.3 && n > 2 {
		punishment := math.Min(stdDegrade*1.5, 0.25)
		overfitScore += punishment
	}
	overfitScore = math.Min(overfitScore, 1.0)

	negativeWindows := 0
	for _, w := range report.Windows {
		if w.TestSharpe < 0 {
			negativeWindows++
		}
	}
	negativeRatio := float64(negativeWindows) / float64(n)
	if negativeRatio > 0.5 {
		overfitScore = math.Max(overfitScore, 0.8)
	}

	report.OverfitScore = overfitScore

	probNoOverfit := 1.0 - overfitScore
	if avgOOS > 1.0 && stdOOS < avgOOS*0.5 {
		probNoOverfit = math.Min(probNoOverfit+0.15, 1.0)
	}
	if report.PassRate > 0.8 {
		probNoOverfit = math.Min(probNoOverfit+0.10, 1.0)
	}
	report.ProbNoOverfit = probNoOverfit

	cv := 0.0
	if avgOOS > 1e-9 {
		cv = stdOOS / avgOOS
	}
	stability := 1.0 / (1.0 + cv*3.0)
	if n >= 5 {
		halfN := float64(n / 2)
		firstHalf := report.Windows[:n/2]
		secondHalf := report.Windows[n/2:]

		var firstSum, secondSum float64
		for _, w := range firstHalf {
			firstSum += w.TestSharpe
		}
		for _, w := range secondHalf {
			secondSum += w.TestSharpe
		}
		firstAvg := firstSum / halfN
		secondAvg := secondSum / halfN

		drift := math.Abs(firstAvg - secondAvg)
		if avgOOS > 1e-9 {
			driftNorm := drift / avgOOS
			stability *= (1.0 - math.Min(driftNorm*0.5, 0.3))
		}
	}
	report.StabilityScore = math.Max(0.0, math.Min(stability, 1.0))
}

// wfWindow represents a single walk-forward window.
type wfWindow struct {
	trainStart, trainEnd time.Time
	testStart, testEnd   time.Time
}

// buildWindows generates walk-forward windows from trading days.
//
// Rolling window scheme:
//
//	Window k:  Train = [k*Step, k*Step+TrainDays-1]
//	          Test  = [k*Step+TrainDays, k*Step+TrainDays+TestDays-1]
//
// Expanding window variant (anchored): if StepDays == 0, train start is fixed at day 0
// and only the end expands. This is useful for strategies that need more data over time.
func (wf *WalkForwardEngine) buildWindows(days []time.Time, params domain.WalkForwardParams) []wfWindow {
	var windows []wfWindow
	totalNeed := params.TrainDays + params.TestDays

	isExpanding := params.StepDays == 0
	step := params.StepDays
	if step <= 0 {
		step = params.TestDays
	}

	for startIdx := 0; startIdx+totalNeed <= len(days); startIdx += step {
		trainEndIdx := startIdx + params.TrainDays - 1
		testEndIdx := trainEndIdx + params.TestDays

		if isExpanding {
			trainEndIdx = startIdx + params.TrainDays - 1 + (startIdx/params.TestDays)*params.TestDays
			testEndIdx = trainEndIdx + params.TestDays
		}

		if testEndIdx >= len(days) {
			testEndIdx = len(days) - 1
			if testEndIdx-trainEndIdx < params.TestDays/2 {
				continue
			}
		}

		window := wfWindow{
			trainStart: days[startIdx],
			trainEnd:   days[trainEndIdx],
			testStart:  days[trainEndIdx+1],
			testEnd:    days[testEndIdx],
		}

		trainDays := trainEndIdx - startIdx + 1
		if trainDays < params.MinTrainDays {
			continue
		}

		windows = append(windows, window)
	}

	return windows
}

// toBacktestResult converts a BacktestResponse to a BacktestResult.
func (wf *WalkForwardEngine) toBacktestResult(r *contracts.BacktestResponse) *domain.BacktestResult {
	if r == nil {
		return nil
	}
	var maxDDDate time.Time
	if r.MaxDrawdownDate != "" {
		maxDDDate, _ = time.Parse("2006-01-02", r.MaxDrawdownDate)
	}
	var completedAt time.Time
	if r.CompletedAt != "" {
		completedAt, _ = time.Parse(time.RFC3339, r.CompletedAt)
	}
	var startedAt time.Time
	if r.StartedAt != "" {
		startedAt, _ = time.Parse(time.RFC3339, r.StartedAt)
	}

	return &domain.BacktestResult{
		StartDate:       startedAt,
		EndDate:         completedAt,
		TotalReturn:     r.TotalReturn,
		AnnualReturn:    r.AnnualReturn,
		SharpeRatio:     r.SharpeRatio,
		SortinoRatio:    r.SortinoRatio,
		MaxDrawdown:     r.MaxDrawdown,
		MaxDrawdownDate: maxDDDate,
		WinRate:         r.WinRate,
		TotalTrades:     r.TotalTrades,
		WinTrades:       r.WinTrades,
		LoseTrades:      r.LoseTrades,
		AvgHoldingDays:  r.AvgHoldingDays,
		CalmarRatio:     r.CalmarRatio,
		PortfolioValues: r.PortfolioValues,
		Trades:          r.Trades,
	}
}

// stdDev was migrated to statistics.SampleStdDev in ODR-013 P1-21.
// Callers now use the shared helper directly.
