// Command worker 用 core 的引擎当 tellurion-orchestration 的 worker：
// 领一条 run_spec → 跑 core 引擎 → 把结果落回 core → 上报 K9 结论。
//
// 它不 import orchestration 的代码（对方也不 import core）：两边只经
// `tellurion-orchestration:docs/api-schema.md` §1「算力」的 HTTP 面交互。
//
// 配置沿用 analysis-service 的引擎配置（CONFIG_PATH / -config）：同一个部署里
// worker 与 analysis 必须看到同一份 backtest / risk_manager / data_service 配置，
// 否则同一条 run 在两个进程里算出的数会不同。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/engine"
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
	engine, _ := bootstrap.BuildBacktestEngine(v, logger)
	engine.SetStore(store)
	engine.SetRiskManager(bootstrap.BuildRiskManager(v, logger))

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

	resolver := &orchestrationworker.AnchorResolver{
		LoadBase: func(ctx context.Context, runID string) (*orchestrationworker.BaseRun, error) {
			row, err := store.GetBacktestJob(ctx, runID)
			if err != nil {
				return nil, err
			}
			if row == nil {
				return nil, fmt.Errorf("base run %s not found in backtest_jobs", runID)
			}
			return orchestrationworker.ParseBaseRun(row)
		},
	}
	executor := &orchestrationworker.Executor{
		RunnerFor: func(kernel string) (contracts.EngineRunner, error) {
			return registry.Resolve(kernel)
		},
		Sink:    &orchestrationworker.JobStoreSink{Store: store},
		Timeout: *runTimeout,
	}

	client := orchestrationworker.NewClient(url, nil)
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
	_ orchestrationworker.JobStore = (*storage.PostgresStore)(nil)
	_ contracts.EngineRunner       = (*backtest.Engine)(nil)
)
