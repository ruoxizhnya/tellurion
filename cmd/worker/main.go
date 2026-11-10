// Command worker 用 core 的引擎当 tellurion-orchestration 的 worker：
// 领一条 run_spec → 跑 core 引擎 → 把结果落回 core → 上报 K9 结论。
//
// 它不 import orchestration 的代码（对方也不 import core）：两边只经
// `tellurion-orchestration:docs/api-schema.md` §1「算力」的 HTTP 面交互。
//
// 配置沿用 analysis-service 的引擎配置（CONFIG_PATH / -config）：同一个部署里
// worker 与 analysis 必须看到同一份 backtest / data_service 配置，否则同一条 run
// 在两个进程里算出的数会不同。风控限额不在这份 YAML 里 —— 两边都经
// `wiring.LoadRiskPolicy` 从 tellurion-risk 的规则快照取（SVC-CORE-RISK-RETIRE）。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/engine"
	"tellurion/cmd/analysis/wiring"
	"tellurion/internal/analysisartifact"
	"tellurion/internal/orchestrationworker"
	"tellurion/pkg/backtest"
	"tellurion/pkg/bootstrap"
	"tellurion/pkg/storage"

	// 策略注册表：worker 必须与 analysis 注册同一批内置策略，否则
	// `strategy` 名字在 worker 进程里不存在（run 会被报成 failed）。
	_ "tellurion/pkg/strategy/expression"
	_ "tellurion/pkg/strategy/plugins"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "worker:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		orchestrationURL = flag.String("orchestration-url", "", "编排服务根 URL（空则读 ORCHESTRATION_URL；两者都空即拒绝启动）")
		workerID         = flag.String("worker-id", "", "worker id（默认 hostname-pid-启动秒；drain 过的 id 不会复活，别复用）")
		kernelsFlag      = flag.String("kernels", contracts.DefaultKernel, "声明的内核能力（逗号分隔）；空串 = 不声明能力 = 不按内核过滤")
		heartbeat        = flag.Duration("heartbeat", 20*time.Second, "心跳 / 续租周期（必须短于编排侧 -worker-lease）")
		poll             = flag.Duration("poll", 2*time.Second, "无活时的轮询间隔")
		runTimeout       = flag.Duration("run-timeout", 30*time.Minute, "单条回测的超时（超时按 failed 上报）")
		configPath       = flag.String("config", "", "引擎配置文件（默认 config/analysis-service.yaml）")
		serviceToken     = flag.String("orchestration-token", strings.TrimSpace(os.Getenv("ORCHESTRATION_SERVICE_TOKEN")), "调用编排服务的 service token（全量 R5；空 = 不带 Authorization）")
	)
	flag.Parse()

	url, err := resolveURL(*orchestrationURL)
	if err != nil {
		return err
	}
	if *configPath != "" {
		if err := os.Setenv("CONFIG_PATH", *configPath); err != nil {
			return fmt.Errorf("set CONFIG_PATH: %w", err)
		}
	}

	logger := bootstrap.InitLogger()
	v := bootstrap.LoadConfig(logger, "config/analysis-service.yaml")
	store := bootstrap.InitStore(v, logger)
	defer store.Close()

	// 与 analysis 的 strategy-runtime 模块同一套注入：引擎、store、风控。
	// DataAdapter / 市场路由 / risk rules 取数口属 analysis 的装配细节，
	// worker 今天不搬（见 docs/TASKS.md 的施工记录「未落」）。
	engine, market := bootstrap.BuildBacktestEngine(v, logger)
	engine.SetStore(store)
	// SVC-CORE-RISK-RETIRE（2026-11-09）：限额真相源在 risk 服务快照。
	// worker 与 analysis 必须用同一份限额（否则同一条 run 在两个进程里算出
	// 不同的数），所以走**同一个** LoadRiskPolicy；取不到即拒绝启动 ——
	// 不再有「worker 拿 config 默认值顶上」的第二真相。
	policy, err := wiring.LoadRiskPolicy(v, logger)
	if err != nil {
		return err
	}
	engine.SetRiskManager(bootstrap.BuildRiskManager(policy.Config, logger))

	registry := contracts.NewEngineRegistry()
	registry.Register(contracts.DefaultKernel, engine)

	kernels := parseKernels(*kernelsFlag)
	for _, k := range kernels {
		if _, err := registry.Resolve(k); err != nil {
			return fmt.Errorf("declared kernel %q is not registered in this worker: %w", k, err)
		}
	}

	if *workerID == "" {
		*workerID = defaultWorkerID()
	}

	// SVC-ORCH-G3 收尾腿（2026-11-09）：core 的 quant.backtest_jobs 退役后，
	// 历史 run 的 run_spec 只存在于编排任务面（Task face 是 RunSpec 的保管人）。
	// anchor base 与基准覆盖都改经 `GET /api/runs/jobs/{id}` 的 run_spec 取。
	client := orchestrationworker.NewClient(url, nil)
	client.Token = strings.TrimSpace(*serviceToken)

	resolver := &orchestrationworker.AnchorResolver{
		LoadBase: func(ctx context.Context, runID string) (*orchestrationworker.BaseRun, error) {
			state, err := client.Job(ctx, runID)
			if err != nil {
				return nil, fmt.Errorf("base run %s: %w", runID, err)
			}
			return orchestrationworker.ParseBaseRunSpec(state.RunSpec)
		},
	}
	benchmarkIndex := strings.TrimSpace(v.GetString("benchmark.index_code"))
	if benchmarkIndex == "" {
		benchmarkIndex = "000300.SH"
	}
	// QT-78：run 终态产出 portfolio-snapshot / factor-exposures。
	// 基准可由 run 的 engine_params.benchmark_index_code 覆盖（open map，
	// 不动冻结的 BacktestResponse 27 字段）。
	publisher := &analysisartifact.Publisher{
		Engine:        engine,
		Market:        market,
		Catalog:       &analysisartifact.ProviderStockCatalog{Provider: market},
		FactorReturns: store,
		FactorCache:   store,
		Provenance:    store,
		Artifacts:     store,
		Deliverer:     client,
		Benchmark:     benchmarkIndex,
		BenchmarkFor: func(ctx context.Context, runID string) string {
			state, err := client.Job(ctx, runID)
			if err != nil {
				return ""
			}
			return orchestrationworker.EngineParamString(state.RunSpec, "benchmark_index_code")
		},
		Log: logger,
	}

	// QT-32 K11：worker 也要过同一道资产闸 —— 直接把 run spec POST 给编排
	// 服务的路径绕过 analysis 入口，闸门不能只装在入口。未启用 = nil 闸门，
	// 非空 strategy_ref 在这里被折成 failed（fail-closed）。
	strategyAssetGate, err := wiring.LoadStrategyAssetGate(v, logger)
	if err != nil {
		return err
	}

	executor := &orchestrationworker.Executor{
		RunnerFor: func(kernel string) (contracts.EngineRunner, error) {
			return registry.Resolve(kernel)
		},
		// 结果面（归属 core，`SVC-ORCH-G3`）：run.backtest_results 是结果
		// 真相，单一写者是 worker；core 的任务面遗留表 backtest_jobs 已退役
		// （Migration 065 DROP TABLE）。
		Sink: &orchestrationworker.ResultStoreSink{Store: resultFaceStore{store: store}},
		// 失败留痕：failed 行没有载荷，走结果面的直接写口（见 recordFailure）。
		Face:    resultFaceStore{store: store},
		Timeout: *runTimeout,
		StrategyGate: func(ctx context.Context, rawRef string) (*backtest.StrategyAssetIdentity, error) {
			return wiring.CheckStrategyRef(ctx, strategyAssetGate, rawRef)
		},
		Publisher: publisher,
		Log:       logger,
	}

	loop := orchestrationworker.NewLoop(client, resolver, executor, *workerID, kernels, orchestrationworker.Options{
		HeartbeatEvery: *heartbeat,
		PollEvery:      *poll,
	}, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info().
		Str("worker_id", *workerID).
		Str("orchestration_url", url).
		Strs("kernels", kernels).
		Msg("orchestration worker starting")
	if err := loop.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func resolveURL(fromFlag string) (string, error) {
	if u := strings.TrimSpace(fromFlag); u != "" {
		return u, nil
	}
	if u := strings.TrimSpace(os.Getenv("ORCHESTRATION_URL")); u != "" {
		return u, nil
	}
	return "", errors.New("no orchestration URL: pass -orchestration-url or set ORCHESTRATION_URL")
}

// parseKernels 解析逗号分隔的能力声明。空串 = 不声明（nil），与编排侧的
// 「空数组归一成 NULL」同义。
func parseKernels(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	seen := make(map[string]bool)
	out := make([]string, 0, 2)
	for _, part := range strings.Split(raw, ",") {
		k := strings.TrimSpace(part)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out
}

func defaultWorkerID() string {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "worker"
	}
	return fmt.Sprintf("%s-%d-%d", host, os.Getpid(), time.Now().Unix())
}

// 编译期断言：worker 的存储面与执行器契约都由真实实现满足。
var (
	_ contracts.EngineRunner              = (*backtest.Engine)(nil)
	_ orchestrationworker.ResultFaceStore = resultFaceStore{}
)

// resultFaceStore 把 *storage.PostgresStore 适配成 orchestrationworker 的结果面
// 写口（接口用原始类型，执行器契约不依赖 storage 的数据结构）。
type resultFaceStore struct{ store *storage.PostgresStore }

// SaveBacktestResult 见 orchestrationworker.ResultFaceStore。
func (s resultFaceStore) SaveBacktestResult(ctx context.Context, runID, resultStatus string, result []byte) error {
	_, err := s.store.SaveBacktestResult(ctx, &storage.BacktestResultRecord{
		RunID:        runID,
		ResultStatus: resultStatus,
		Result:       json.RawMessage(result),
	})
	return err
}
