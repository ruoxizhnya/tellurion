package strategy

// QT-10 ③-②：LocalSignalProvider 的错误归类。
//
// 判据（contracts API.md §3）：只用 errors.Is；包装保链 —— 归类之后
// `apperrors` 码仍可 errors.As 追到（HTTP 映射不退化）。

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/ruoxizhnya/tellurion-contracts/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperrors "tellurion/pkg/errors"
)

var qt10Day = time.Date(2024, 1, 5, 0, 0, 0, 0, time.UTC)

func qt10ProviderFor(t *testing.T, gen func() ([]Signal, error)) (*LocalSignalProvider, Scope) {
	t.Helper()
	reg := NewRegistry()
	require.NoError(t, reg.RegisterFactory(func() Strategy {
		return &pvBatch{name: "qt10_fake", gen: func(map[string][]domain.OHLCV, *domain.Portfolio) ([]Signal, error) {
			return gen()
		}}
	}))
	p := NewLocalSignalProvider(reg)
	plBegin(t, p)
	return p, Scope{
		Strategy:   "qt10_fake",
		Universe:   []string{"600519.SH"},
		MarketData: map[string][]domain.OHLCV{"600519.SH": pvBars("600519.SH", qt10Day, 2)},
	}
}

// 策略跑坏（GenerateSignals 报错）⇒ StrategyFault。
func TestQT10_LocalProvider_StrategyErrorIsStrategyFault(t *testing.T) {
	p, scope := qt10ProviderFor(t, func() ([]Signal, error) {
		return nil, errors.New("strategy exploded")
	})

	_, err := p.Signals(context.Background(), scope, qt10Day)
	require.Error(t, err)
	assert.True(t, errors.Is(err, contracts.ErrStrategyFault), "策略执行失败必须归 StrategyFault，got: %v", err)
	assert.False(t, errors.Is(err, contracts.ErrDataGap), "策略故障不得冒充数据缺口")
	assert.False(t, errors.Is(err, contracts.ErrContractBreach))

	var appErr *apperrors.AppError
	require.True(t, errors.As(err, &appErr), "归类不得断掉 apperrors 链（HTTP 映射依赖它）")
	assert.Equal(t, apperrors.ErrCodeInternal, appErr.Code)
}

// 未来信号（契约级防前视）⇒ ContractBreach。
func TestQT10_LocalProvider_FutureSignalIsContractBreach(t *testing.T) {
	p, scope := qt10ProviderFor(t, func() ([]Signal, error) {
		return []Signal{{
			Symbol: "600519.SH",
			Action: "buy",
			Date:   qt10Day.AddDate(0, 0, 1), // 未来一天
		}}, nil
	})

	_, err := p.Signals(context.Background(), scope, qt10Day)
	require.Error(t, err)
	assert.True(t, errors.Is(err, contracts.ErrContractBreach), "前视信号是契约违规，got: %v", err)
	assert.False(t, errors.Is(err, contracts.ErrStrategyFault))
}

// 未注册策略名是装配错误 ⇒ ContractBreach（而不是跳过当天）。
func TestQT10_LocalProvider_UnknownStrategyIsContractBreach(t *testing.T) {
	reg := NewRegistry()
	p := NewLocalSignalProvider(reg)
	plBegin(t, p)

	_, err := p.Signals(context.Background(), Scope{Strategy: "not_registered"}, qt10Day)
	require.Error(t, err)
	assert.True(t, errors.Is(err, contracts.ErrContractBreach), "未注册策略是契约/装配错误，got: %v", err)
	assert.False(t, errors.Is(err, contracts.ErrDataGap))
}
