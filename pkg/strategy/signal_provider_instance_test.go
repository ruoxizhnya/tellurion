package strategy

// QT-11 · provider 侧的实例语义。
//
// 两条必须同时成立的性质，缺一即错：
//   - **一个 run 内复用同一份实例**：流式策略（BarHandler）的递推状态要跨
//     交易日活着，每天换一份实例等于把 EMA / 游标清零；
//   - **换个 run 就是全新的一份**：provider 是 run 级的，它的实例缓存因此
//     也是 run 级的 —— 这是 A2 缺陷（cursor 跨 run 存活）的真正出口。
//
// 破坏验证：
//   - strategyFor 里去掉 instances 缓存（每次新建）→
//     TestLocalSignalProviderReusesOneInstanceWithinARun 变红（流式状态被清零）；
//   - 把 LocalSignalProvider 的实例缓存挪成包级变量（跨 provider 共享）→
//     TestLocalSignalProviderGivesEachRunAFreshInstance 变红（残留回来了）；
//   - strategyFor 遇 ErrSharedStrategy 时回落共享实例 →
//     TestLocalSignalProviderFailsClosedOnSharedRegistration 变红。

import (
	"context"
	"testing"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/stretchr/testify/require"
)

// piStream 是「状态写在实例上」的流式替身：fed 记录本实例见过的 bar。
type piStream struct {
	pvBatch
	fed []domain.OHLCV
}

func (s *piStream) OnBar(_ context.Context, bar domain.OHLCV) error {
	s.fed = append(s.fed, bar)
	return nil
}
func (s *piStream) Warmup() int                { return 0 }
func (s *piStream) SaveState() ([]byte, error) { return nil, nil }
func (s *piStream) LoadState([]byte) error     { return nil }
func (s *piStream) Signals() []Signal          { return nil }

// piRegistry 注册一个计数工厂，返回注册表与构造清单。
//
// 清单第 0 项是**注册期原型**（RegisterFactory 要构造一次才能拿到名字），
// 从第 1 项起才是各 run 经 NewInstance 拿到的实例。
func piRegistry(t *testing.T, name string) (*Registry, *[]*piStream) {
	t.Helper()
	built := make([]*piStream, 0, 4)
	reg := NewRegistry()
	require.NoError(t, reg.RegisterFactory(func() Strategy {
		s := &piStream{pvBatch: pvBatch{name: name}}
		built = append(built, s)
		return s
	}))
	require.Len(t, built, 1, "注册期构造一次原型")
	return reg, &built
}

func TestLocalSignalProviderReusesOneInstanceWithinARun(t *testing.T) {
	asOf := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	reg, built := piRegistry(t, "pi_stream")
	// 一个 provider = 一个 run（QT-11 的装配纪律，见 LocalSignalProvider 注释）。
	provider := NewLocalSignalProvider(reg)

	bars := map[string][]domain.OHLCV{"AAA": pvBars("AAA", asOf, 3)}
	for i := 1; i <= 3; i++ {
		_, err := provider.Signals(context.Background(), Scope{Strategy: "pi_stream", MarketData: bars}, asOf)
		require.NoError(t, err)
	}

	require.Len(t, *built, 2, "同一个 run 里反复取信号不得反复构造实例（每天换实例=流式状态清零）")
	require.Len(t, (*built)[1].fed, 3, "同一份实例必须攒下这个 run 的全部 bar")
}

func TestLocalSignalProviderGivesEachRunAFreshInstance(t *testing.T) {
	asOf := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	reg, built := piRegistry(t, "pi_stream")
	bars := map[string][]domain.OHLCV{"AAA": pvBars("AAA", asOf, 3)}

	// 第一笔 run：喂了 2 根。
	first := NewLocalSignalProvider(reg)
	for i := 0; i < 2; i++ {
		_, err := first.Signals(context.Background(), Scope{Strategy: "pi_stream", MarketData: bars}, asOf)
		require.NoError(t, err)
	}

	// 第二笔 run（新的 provider）：只能看见自己这一根。
	second := NewLocalSignalProvider(reg)
	_, err := second.Signals(context.Background(), Scope{Strategy: "pi_stream", MarketData: bars}, asOf)
	require.NoError(t, err)

	require.Len(t, *built, 3, "原型 + 每笔 run 各一份实例")
	run1, run2 := (*built)[1], (*built)[2]
	require.Len(t, run1.fed, 2, "第一笔的实例不该被第二笔继续喂")
	require.Len(t, run2.fed, 1,
		"A2 原缺陷的形状：第二笔继承了第一笔的递推状态（这里必须从 0 开始数）")
}

func TestLocalSignalProviderFailsClosedOnSharedRegistration(t *testing.T) {
	asOf := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	reg := NewRegistry()
	shared := &piStream{pvBatch: pvBatch{name: "pi_shared"}}
	require.NoError(t, reg.Register(shared))

	_, err := NewLocalSignalProvider(reg).Signals(context.Background(),
		Scope{Strategy: "pi_shared", MarketData: map[string][]domain.OHLCV{"AAA": pvBars("AAA", asOf, 3)}}, asOf)

	require.Error(t, err, "共享实例注册在 run 取实例那一步就该失败")
	require.Contains(t, err.Error(), "RegisterFactory", "错误要给出可执行的补救办法")
	require.Empty(t, shared.fed,
		"共享实例被拿去算了信号 = fail-closed 没做到，只是报了个警告")
}
