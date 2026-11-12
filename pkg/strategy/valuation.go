package strategy

import (
	"sort"
	"time"

	"tellurion/pkg/domain"
)

// ValuationReader 返回给定票在「当前决策日」的 PIT 估值快照。
//
// 实现必须只返回 `trade_date <= 决策日` 的最近一条；截至决策日没有任何
// 估值行的票不出现在返回 map 中（「没有行」与「有行但 PE/PB 为 NULL」
// 是两种不同的缺失，消费方要能区分）。
type ValuationReader func(symbols []string) map[string]domain.DailyBasicValuation

// ValuationAware 是策略的可选能力接口（同 FactorAware / FundamentalAware
// 的范式）：声明自己需要个股估值快照。引擎据此在 run 预热阶段加载
// market.daily_basic，并在每个回放日注入「当日绑定」的读取器。
//
// 没实现本接口的策略不付这份读取成本。
type ValuationAware interface {
	SetValuationReader(reader ValuationReader)
}

// NewValuationReader 由 run 内逐日估值历史构造按日绑定的 PIT 读取器。
//
// 参数 snapshot 的每个切片必须按 TradeDate 升序（storage.GetDailyBasicHistory
// 的返回契约）。asOf 是引擎的回放日；本函数把「最近一条」的判定固定在
// `DayOf(tradeDate) <= DayOf(asOf)`，与存储层 `trade_date <= 决策日` 同一口径，
// 不在这里另立一套。
func NewValuationReader(
	snapshot map[string][]domain.DailyBasicValuation, asOf time.Time,
) ValuationReader {
	day := DayOf(asOf)
	return func(symbols []string) map[string]domain.DailyBasicValuation {
		out := make(map[string]domain.DailyBasicValuation, len(symbols))
		for _, symbol := range symbols {
			series := snapshot[symbol]
			if len(series) == 0 {
				continue
			}
			// 二分找第一条「晚于决策日」的行，它的前一条就是 PIT 最近值。
			idx := sort.Search(len(series), func(i int) bool {
				return DayOf(series[i].TradeDate).After(day)
			})
			if idx == 0 {
				continue // 全部行都晚于决策日：截至该日无可用估值
			}
			out[symbol] = series[idx-1]
		}
		return out
	}
}
