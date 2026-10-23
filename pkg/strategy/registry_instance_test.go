package strategy

// QT-11 · 注册工厂与 per-run 实例。
//
// 缺陷原貌（p0-remediation-design.md A2 / BE-21）：注册表存的是注册期那一个
// 实例，执行路径拿它算信号 —— `SignalStrategy.cursor` 这类 per-instance 状态
// 因此跨 run 存活，第二笔回测继承第一笔的残留。
//
// 本文件钉住修法的两半：
//   - RegisterFactory + NewInstance 是执行路径唯一能拿到实例的通道，
//     且每次拿到的都是**新的一份**；
//   - Register（共享实例）在 NewInstance 上 fail-closed —— 不是降级成
//     「那就用共享的」，因为串状态的回测曲线看起来完全正常。
//
// 破坏验证：
//   - 把 NewInstance 里 `entry.factory == nil` 那支改成回落共享实例 →
//     TestNewInstanceRejectsSharedRegistration 变红；
//   - 把 NewInstance 改成返回 entry.meta（同一个实例） →
//     TestNewInstanceReturnsADistinctInstancePerCall 变红；
//   - 删掉 ConfigureStrategy 的工厂拒绝分支 →
//     TestConfigureStrategyRefusesToMutateAFactoryEntry 变红（静默无效）。

import (
	"context"
	"errors"
	"testing"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/stretchr/testify/require"
)

// cfgProbe 是一个「状态写在实例上」的探针：Configure 改 params，
// GenerateSignals 把 params 原样吐回信号，于是「拿到的是哪一份实例」
// 在返回值上可直接观测。
type cfgProbe struct {
	name   string
	params map[string]any
}

func (p *cfgProbe) Name() string            { return p.name }
func (p *cfgProbe) Description() string     { return "probe" }
func (p *cfgProbe) Parameters() []Parameter { return nil }
func (p *cfgProbe) Configure(params map[string]interface{}) error {
	p.params = params
	return nil
}
func (p *cfgProbe) GenerateSignals(context.Context, map[string][]domain.OHLCV, *domain.Portfolio) ([]Signal, error) {
	return []Signal{{Symbol: "seen", Strength: float64(len(p.params))}}, nil
}
func (p *cfgProbe) Weight(Signal, float64) float64 { return 1 }
func (p *cfgProbe) Cleanup()                       {}

func TestNewInstanceReturnsADistinctInstancePerCall(t *testing.T) {
	var built int
	r := NewRegistry()
	require.NoError(t, r.RegisterFactory(func() Strategy {
		built++
		return &mockStrategy{name: "per_run"}
	}))
	require.Equal(t, 1, built, "注册期构造一次（取名字与元数据的原型）")

	first, err := r.NewInstance("per_run")
	require.NoError(t, err)
	second, err := r.NewInstance("per_run")
	require.NoError(t, err)

	require.NotSame(t, first, second,
		"两次 NewInstance 拿到同一个实例 = per-run 隔离不存在（QT-11 要修的正是这个）")
	require.Equal(t, 3, built, "每次 NewInstance 必须真调用工厂，不能把注册期那份递出去")
}

func TestNewInstanceRejectsSharedRegistration(t *testing.T) {
	r := NewRegistry()
	shared := &mockStrategy{name: "legacy_shared"}
	require.NoError(t, r.Register(shared))

	got, err := r.NewInstance("legacy_shared")
	require.ErrorIs(t, err, ErrSharedStrategy,
		"共享实例必须 fail-closed；悄悄把它递出去就是 A2 原缺陷本身")
	require.Nil(t, got, "报错时不得返回可用实例（否则调用方会忽略 error 继续跑）")
	require.Contains(t, err.Error(), "RegisterFactory",
		"错误必须点名补救办法，否则现场只会看到一句「不行」")

	// 元数据面不受影响：Get 仍返回注册期那一份（Parameters/Info 查询是它的用途）。
	meta, err := r.Get("legacy_shared")
	require.NoError(t, err)
	require.Same(t, shared, meta, "Get 的元数据用途不得被本单改掉")
}

func TestNewInstanceUnknownStrategyKeepsNotFoundMessage(t *testing.T) {
	r := NewRegistry()
	_, err := r.NewInstance("ghost")
	require.Error(t, err)
	// LocalSignalProvider 的 OBS-11 错误消息逐字节依赖这句（signal_provider_test.go）。
	require.EqualError(t, err, "strategy not found: ghost")
}

func TestRegisterFactoryTakesNameFromPrototype(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.RegisterFactory(func() Strategy {
		return &mockStrategy{name: "named_by_factory", description: "D", params: []Parameter{{Name: "lookback_days"}}}
	}))

	require.Equal(t, []string{"named_by_factory"}, r.List())
	info, err := r.Describe("named_by_factory")
	require.NoError(t, err)
	require.Equal(t, StrategyInfo{Name: "named_by_factory", Description: "D",
		Parameters: []Parameter{{Name: "lookback_days"}}}, info)

	// 重名（无论以哪种方式注册第二次）必须红 —— 静默覆盖会让「跑的是哪份定义」不可知。
	require.ErrorContains(t, r.RegisterFactory(func() Strategy { return &mockStrategy{name: "named_by_factory"} }),
		"already registered")
	require.ErrorContains(t, r.Register(&mockStrategy{name: "named_by_factory"}), "already registered")
}

func TestRegisterFactoryRejectsUnusableFactories(t *testing.T) {
	r := NewRegistry()

	require.Error(t, r.RegisterFactory(nil), "nil 工厂必须在注册期就拒")
	require.Error(t, r.RegisterFactory(func() Strategy { return nil }),
		"工厂造不出实例 = 注册了一个永远取不到的名字，当场拒比运行时报错准")
	require.Error(t, r.RegisterFactory(func() Strategy { return &mockStrategy{} }),
		"空名字注册必须拒（与 Register 同口径）")
	require.Error(t, r.Register(nil), "nil 实例同样当场拒")

	require.Empty(t, r.List(), "被拒的注册不得在注册表里留下条目")
}

func TestIsSharedTellsTheTwoRegistrationModesApart(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(&mockStrategy{name: "inst"}))
	require.NoError(t, r.RegisterFactory(func() Strategy { return &mockStrategy{name: "fact"} }))

	shared, err := r.IsShared("inst")
	require.NoError(t, err)
	require.True(t, shared)

	shared, err = r.IsShared("fact")
	require.NoError(t, err)
	require.False(t, shared)

	_, err = r.IsShared("ghost")
	require.Error(t, err)
}

// TestConfigureStrategyRefusesToMutateAFactoryEntry 钉住 QT-11 带来的一个
// 语义空洞：改了注册表里那份实例，run 用的实例根本不是你改的那一份。
// 静默无效（旧形状）比报错危险得多。
func TestConfigureStrategyRefusesToMutateAFactoryEntry(t *testing.T) {
	orig := DefaultRegistry
	DefaultRegistry = NewRegistry()
	t.Cleanup(func() { DefaultRegistry = orig })

	built := make([]*cfgProbe, 0, 2)
	require.NoError(t, DefaultRegistry.RegisterFactory(func() Strategy {
		p := &cfgProbe{name: "cfg_factory"}
		built = append(built, p)
		return p
	}))

	err := ConfigureStrategy("cfg_factory", map[string]any{"lookback_days": 30})
	require.Error(t, err, "工厂注册的条目不得被 ConfigureStrategy 静默改掉")
	require.Contains(t, err.Error(), "QT-20", "错误要点名真正的参数投递单号，而不是只说不行")

	// 正证据：报错之外还得证明「真的没改到任何一份实例」——注册期那份原型
	// 也还是空的（它不是 run 用的那一份，改它等于没改）。
	require.Len(t, built, 1, "ConfigureStrategy 不得为工厂条目再构造实例")
	require.Nil(t, built[0].params, "注册期原型被改写：参数投递走进了没人读的那一份")

	// 对照腿：共享实例注册仍可按旧语义配置（那是唯一能被改到的那一份）。
	shared := &cfgProbe{name: "cfg_shared"}
	require.NoError(t, DefaultRegistry.Register(shared))
	require.NoError(t, ConfigureStrategy("cfg_shared", map[string]any{"lookback_days": 30}))
	require.Equal(t, map[string]any{"lookback_days": 30}, shared.params,
		"共享实例的 Configure 语义不得被本单一并改掉")
}

func TestNewInstanceErrorsAreDistinguishable(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(&mockStrategy{name: "shared_one"}))

	// 「未注册」与「注册方式不对」必须是两种可判定的错误：
	// provider 侧按它们分别包成 NOT_FOUND / INTERNAL。
	_, err := r.NewInstance("nope")
	require.False(t, errors.Is(err, ErrSharedStrategy))

	_, err = r.NewInstance("shared_one")
	require.True(t, errors.Is(err, ErrSharedStrategy))
}
