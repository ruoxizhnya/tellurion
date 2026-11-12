package domain

import "time"

// ─── 个股日频估值读模型（CORE-STRATEGY-SCREEN-LOCAL-VALUE）───────────────
//
// 数据来源是 `market.daily_basic`（写者归 tellurion-data）。读模型放在
// domain 而不是 storage，是因为消费方在 `pkg/strategy`（价值筛选）
// 与 `pkg/backtest`（run 快照）两侧；若留在 storage，上层要么依赖存储层，
// 要么复制一份等价结构，口径必然漂移。

// 估值字段的来源标记（provenance）。
//
// 回退必须可见：只看 PE 数值无法判断某天用的是 TTM 口径还是报告期口径，
// 而这两者可能差很多。PESource 让调用方/日志能直接分辨。
const (
	DailyBasicPESourceTTM      = "pe_ttm"
	DailyBasicPESourceReported = "pe"
	DailyBasicPBSourceReported = "pb"
)

// DailyBasicValuation 是单只票在某一交易日（PIT 截取后）的估值快照。
//
// PE/PB 为 nil = 上游该列为 NULL（亏损或未发布），语义是「该票当日不参与
// 筛选」，不是 0、不是极便宜。TotalMV/CircMV 已由存储层换算为元。
type DailyBasicValuation struct {
	TsCode    string
	TradeDate time.Time
	PE        *float64
	PESource  string
	PB        *float64
	PBSource  string
	TotalMV   *float64 // 元
	CircMV    *float64 // 元
}

// HasComparablePE 报告该票当日是否有可比的 PE。
//
// false 的语义是「该票当日不参与筛选」：没有可比的估值，就不进入当天的
// 价值横截面。它不是「被永久排除」，也不能被当成 0 或「最便宜」—— 否则
// 亏损股会稳定排到价值榜第一。
func (v DailyBasicValuation) HasComparablePE() bool { return v.PE != nil }

// HasComparablePB 同 HasComparablePE，针对 pb。
func (v DailyBasicValuation) HasComparablePB() bool { return v.PB != nil }
