package job

// DBT-06 · job 异步路径的市场路由验收腿。
//
// 语义：market 在 **创建期** 经 contracts.ResolveRunTarget 解析成内核名
// 落定进 params["kernel"]（与 K10 的 kernel 保留键同一条落库路径），
// worker 执行期只消费已定路由——StartJob 零改动。与同步端点共用同一
// 映射实现（ResolveRunTarget）与同一 EngineRegistry——「job 路径指定
// market 后取数通道与同步路径一致」由同一内核名 + 同一 registry 保证。
//
// 五条腿：
//  1. 正证据：路由表 CN→fake + Market="CN" → params["kernel"]=="fake"，
//     fake runner 收到 Kernel="fake"（执行期消费已定路由）；
//  2. 反证腿：未登记市场 → CreateJob 报错、**job 不落库**、runner 零调用
//     （创建即拒绝，不是落一条注定失败的作业）；
//  3. 冲突腿：market 路由结果与显式 kernel 不一致 → 创建期报错；
//  4. 回归腿：不带 market 的请求 params 不多出 "market"/"kernel" 键
//     （存量序列化逐字节不变）；nil 路由表 + market 请求 → fail-closed。
//  5. 落库形状：market 原样留 params["market"] 做审计。

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-contracts/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newMarketRoutingJobService 构造带路由表（CN→fake）的 JobService。
func newMarketRoutingJobService(t *testing.T) (*JobService, *mockJobStore, *kernelMarkerRunner) {
	t.Helper()
	store := newMockJobStore()
	goRunner := &kernelMarkerRunner{marker: "go"}
	fakeRunner := &kernelMarkerRunner{marker: "fake"}
	registry := contracts.NewEngineRegistry()
	registry.Register(contracts.DefaultKernel, goRunner)
	registry.Register("fake", fakeRunner)
	router, err := contracts.NewMarketRouter(map[string]string{"CN": "fake"})
	require.NoError(t, err)
	svc := NewJobService(store, registry, zerolog.Nop(), WithMarketRouter(router))
	return svc, store, fakeRunner
}

// 腿 1（正证据）：Market="CN" → 创建期落定 params["kernel"]="fake"，
// 执行期 fake runner 收到已定内核——与同步路径同一 ResolveRunTarget 映射。
func TestJobService_MarketRoutingResolvesAtCreation(t *testing.T) {
	svc, store, fakeRunner := newMarketRoutingJobService(t)

	job, err := svc.CreateJob(context.Background(), CreateJobRequest{
		StrategyID: "momentum",
		Universe:   "600000.SH,600001.SH",
		StartDate:  "2024-01-02",
		EndDate:    "2024-02-02",
		Market:     "CN",
	})
	require.NoError(t, err)

	got := waitJobSettled(t, store, job.ID)
	assert.Equal(t, "completed", got["status"])

	// 落库形状：kernel=路由结果（执行期消费键），market=原样审计。
	var params map[string]any
	require.NoError(t, json.Unmarshal(got["params"].([]byte), &params))
	assert.Equal(t, "fake", params["kernel"], "market 必须在创建期解析成内核名落 params 保留键")
	assert.Equal(t, "CN", params["market"], "market 原样留 params 做审计")

	// 执行期消费已定路由：fake runner 收到 Kernel="fake"。
	require.Len(t, fakeRunner.reqs, 1)
	assert.Equal(t, "fake", fakeRunner.reqs[0].Kernel,
		"worker 必须消费创建期已定的路由（同一 registry，与同步路径同构）")
}

// 腿 2（反证腿）：未登记市场 → CreateJob 报错、job 不落库、runner 零调用。
func TestJobService_UnregisteredMarketFailsAtCreation(t *testing.T) {
	svc, store, fakeRunner := newMarketRoutingJobService(t)

	_, err := svc.CreateJob(context.Background(), CreateJobRequest{
		StrategyID: "momentum",
		Universe:   "600000.SH",
		StartDate:  "2024-01-02",
		EndDate:    "2024-02-02",
		Market:     "US", // 路由表只有 CN
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, contracts.ErrMarketNotRegistered),
		"必须以 ErrMarketNotRegistered 哨兵判别（handler 转 400）: %v", err)

	assert.Equal(t, 0, fakeRunner.callCount())
	assert.Empty(t, store.jobs, "创建即拒绝的 job 不得落库")
}

// 腿 3（冲突腿）：market 路由结果（fake）与显式 kernel（go）不一致 →
// 创建期报错——ResolveRunTarget 不替调用方挑一个。
func TestJobService_MarketKernelConflictRejected(t *testing.T) {
	svc, store, _ := newMarketRoutingJobService(t)

	_, err := svc.CreateJob(context.Background(), CreateJobRequest{
		StrategyID: "momentum",
		Universe:   "600000.SH",
		StartDate:  "2024-01-02",
		EndDate:    "2024-02-02",
		Market:     "CN", // → fake
		Kernel:     "go", // 与路由结果不一致
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to pick one")
	assert.Empty(t, store.jobs, "冲突请求不得落库")
}

// 腿 4（回归腿）：不带 market 的请求 params 不多出 market/kernel 键；
// 未注入路由表（nil）+ market 请求 → fail-closed（ResolveKernel 对 nil 表自带拒绝）。
func TestJobService_NoMarketRegressesAndNilRouterFailsClosed(t *testing.T) {
	store := newMockJobStore()
	goRunner := &kernelMarkerRunner{marker: "go"}
	registry := contracts.NewEngineRegistry()
	registry.Register(contracts.DefaultKernel, goRunner)
	svc := NewJobService(store, registry, zerolog.Nop()) // 无 WithMarketRouter

	// 回归腿：无 market 的存量请求——params 不多出两个保留键。
	job, err := svc.CreateJob(context.Background(), CreateJobRequest{
		StrategyID: "momentum",
		Universe:   "600000.SH",
		StartDate:  "2024-01-02",
		EndDate:    "2024-02-02",
	})
	require.NoError(t, err)
	got := waitJobSettled(t, store, job.ID)
	var params map[string]any
	require.NoError(t, json.Unmarshal(got["params"].([]byte), &params))
	assert.NotContains(t, params, "market")
	assert.NotContains(t, params, "kernel", "不带 market/kernel 的存量 params 序列化不变")

	// nil 路由表 + market → fail-closed（而不是静默走默认内核）。
	baseline := goRunner.callCount() // 回归腿的 job 已用默认 go 跑过一次
	_, err = svc.CreateJob(context.Background(), CreateJobRequest{
		StrategyID: "momentum",
		Universe:   "600000.SH",
		StartDate:  "2024-01-02",
		EndDate:    "2024-02-02",
		Market:     "CN",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, contracts.ErrMarketNotRegistered))
	assert.Equal(t, baseline, goRunner.callCount(),
		"被拒的 market 请求不得触发任何 runner")
}
