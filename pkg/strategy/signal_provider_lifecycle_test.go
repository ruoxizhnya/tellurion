package strategy

// QT-14 · Provider 生命周期（BeginRun/EndRun）的契约测试。
//
// 三条必须同时成立的性质，缺一即错：
//   - 有状态 provider 不开会话就不服务：Signals 拒绝，而不是悄悄用
//     可能带着上一笔残留的实例缓存（那是 QT-11 之前的串状态老路）；
//   - 会话簿记 fail-closed：空 RunID / 并发第二个 Begin / 错误的 runID
//     End / 无会话 End 都当场报错，不排队不猜；
//   - EndRun = finalize + Close：实例被 Cleanup、缓存被清空 —— 信号归属
//     本 runID，下一个 run 从零开始，绝不跨 run 合并。
//
// 装配期校验（ValidateSessionLifecycle）也在本文件：Stateful 没有生命
// 周期 = 装配失败，不做「可选接口 + 静默降级」。
//
// 破坏验证（改坏 → 变红 → 还原，实测见 TASKS.md QT-14 施工记录）：
//   - Signals 里的会话门删掉 → TestLocalSignalProviderRequiresSessionBeforeSignals 红；
//   - EndRun 不 Cleanup / 不清缓存 → TestLocalSignalProviderEndRunFinalizesAndCloses 红；
//   - BeginRun 的上限判断删掉 → 会话簿记测试的「并发第二个 Begin」腿红；
//   - ValidateSessionLifecycle 的 Stateful 分支删掉 → 对应用例红。

import (
	"context"
	"testing"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/stretchr/testify/require"
)

// ─── 测试替身 ───────────────────────────────────────────────────────

// lcStateless 只实现 SignalProvider：无状态，不需要生命周期。
type lcStateless struct{}

func (lcStateless) Signals(context.Context, Scope, time.Time) ([]Signal, error) { return nil, nil }

// lcStatefulNoLifecycle 声明了 Stateful 却不实现 SessionLifecycle ——
// 装配期必须拒绝的形状。
type lcStatefulNoLifecycle struct{ lcStateless }

func (lcStatefulNoLifecycle) Stateful() bool { return true }

// lcStatefulWithLifecycle 三件套齐全 —— 装配期必须放行的形状。
type lcStatefulWithLifecycle struct{ lcStateless }

func (lcStatefulWithLifecycle) Stateful() bool { return true }
func (lcStatefulWithLifecycle) BeginRun(context.Context, RunMeta) error {
	return nil
}
func (lcStatefulWithLifecycle) EndRun(context.Context, string) error { return nil }

// lcCounting 是带 Cleanup 计数的流式替身：EndRun 必须 Cleanup 到它。
type lcCounting struct {
	pvStream
	cleanups int
}

func (s *lcCounting) Cleanup() { s.cleanups++ }

// lcRegistry 注册一个计数工厂，返回注册表与构造清单（清单 [0] 是注册期原型）。
func lcRegistry(t *testing.T, name string) (*Registry, *[]*lcCounting) {
	t.Helper()
	built := make([]*lcCounting, 0, 4)
	reg := NewRegistry()
	require.NoError(t, reg.RegisterFactory(func() Strategy {
		s := &lcCounting{pvStream: pvStream{pvBatch: pvBatch{name: name}}}
		built = append(built, s)
		return s
	}))
	return reg, &built
}

// lcMeta 是测试用的 RunMeta（RunID 由调用方给，其余字段带值以防被空串蒙混）。
func lcMeta(runID string) RunMeta {
	day := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	return RunMeta{
		RunID:    runID,
		Strategy: "lc_stream",
		Params:   map[string]any{"k": "v"},
		Start:    day,
		End:      day.AddDate(0, 0, 9),
		Universe: []string{"AAA", "BBB"},
	}
}

// ─── ValidateSessionLifecycle（装配期校验）──────────────────────────

func TestValidateSessionLifecycle(t *testing.T) {
	require.NoError(t, ValidateSessionLifecycle(nil), "nil provider 恒通过（默认本地实现由引擎自建）")
	require.NoError(t, ValidateSessionLifecycle(lcStateless{}),
		"无状态 provider 不需要生命周期，恒通过")
	require.NoError(t, ValidateSessionLifecycle(lcStatefulWithLifecycle{}),
		"有状态且有生命周期的 provider 放行")

	err := ValidateSessionLifecycle(lcStatefulNoLifecycle{})
	require.Error(t, err, "有状态但没实现 SessionLifecycle 必须在装配期被拒（QT-14 ⑤）")
	require.Contains(t, err.Error(), "SessionLifecycle", "错误要点名缺的东西")
	require.Contains(t, err.Error(), "Stateful", "错误要点名出问题的声明")
}

// ─── LocalSignalProvider 会话门 ─────────────────────────────────────

func TestLocalSignalProviderRequiresSessionBeforeSignals(t *testing.T) {
	asOf := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	reg, built := lcRegistry(t, "lc_stream")

	p := NewLocalSignalProvider(reg)
	_, err := p.Signals(context.Background(),
		Scope{Strategy: "lc_stream", MarketData: map[string][]domain.OHLCV{"AAA": pvBars("AAA", asOf, 3)}}, asOf)
	require.Error(t, err, "会话未开就取信号必须被拒绝 —— 放行等于允许跨 run 共享残留状态")
	require.Contains(t, err.Error(), "BeginRun", "错误要指路 BeginRun")
	require.Len(t, *built, 1, "拒绝的同时不得构造实例（清单里只有注册期原型）")
}

func TestLocalSignalProviderSessionBookkeepingFailsClosed(t *testing.T) {
	asOf := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	reg, _ := lcRegistry(t, "lc_stream")
	p := NewLocalSignalProvider(reg)
	ctx := context.Background()

	require.Error(t, p.BeginRun(ctx, RunMeta{Strategy: "lc_stream"}),
		"空 RunID 的 BeginRun 违约：会话必须有 run 身份")

	require.NoError(t, p.BeginRun(ctx, lcMeta("run-a")))
	require.Error(t, p.BeginRun(ctx, lcMeta("run-b")),
		"并发第二个 Begin 必须失败（会话上限 1，fail-closed 不排队）")
	require.Contains(t, p.activeRun, "run-a", "被拒的 Begin 不得顶掉活着的会话")

	// 会话开着：Signals 正常服务（喂一根 bar，证明不是死锁在门上）。
	_, err := p.Signals(ctx, Scope{Strategy: "lc_stream",
		MarketData: map[string][]domain.OHLCV{"AAA": pvBars("AAA", asOf, 3)}}, asOf)
	require.NoError(t, err)

	require.Error(t, p.EndRun(ctx, "run-other"),
		"EndRun 的 runID 与活会话不符必须失败")
	require.Equal(t, "run-a", p.activeRun, "失败的 End 不得关掉别人的会话")

	require.NoError(t, p.EndRun(ctx, "run-a"))
	require.Error(t, p.EndRun(ctx, "run-a"), "重复 EndRun（无会话）必须失败")
}

func TestLocalSignalProviderEndRunFinalizesAndCloses(t *testing.T) {
	asOf := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	reg, built := lcRegistry(t, "lc_stream")
	bars := map[string][]domain.OHLCV{"AAA": pvBars("AAA", asOf, 3)}
	p := NewLocalSignalProvider(reg)
	ctx := context.Background()

	// run-a：构造实例、喂 bar；EndRun 之后实例被 Cleanup、缓存被清空。
	require.NoError(t, p.BeginRun(ctx, lcMeta("run-a")))
	_, err := p.Signals(ctx, Scope{Strategy: "lc_stream", MarketData: bars}, asOf)
	require.NoError(t, err)
	instA := (*built)[1]
	require.Len(t, instA.fed, 1, "前置：run-a 的实例真喂过 bar")

	// run-b：同一 provider 顺序第二个 run（A1 规则 5：顺序 run 也走
	// Begin/End），必须拿到**全新**实例 —— 上一笔的状态一滴不剩。
	require.NoError(t, p.EndRun(ctx, "run-a"))
	require.Equal(t, 1, instA.cleanups, "EndRun 必须 finalize：对本实现 = 逐实例 Cleanup")

	require.NoError(t, p.BeginRun(ctx, lcMeta("run-b")))
	_, err = p.Signals(ctx, Scope{Strategy: "lc_stream", MarketData: bars}, asOf)
	require.NoError(t, err)
	instB := (*built)[2]
	require.NotSame(t, instA, instB, "EndRun 清空缓存后，下一个 run 必须拿到新实例")
	require.Len(t, instB.fed, 1, "新实例从 0 开始数 bar（不跨 run 合并）")
	require.Equal(t, 0, instB.cleanups, "新实例还没被 Cleanup")
}
