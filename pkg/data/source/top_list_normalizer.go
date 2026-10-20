package source

// TopListNormalizer 是 DBT-10（2026-10-19）落第二条生产 Normalizer。
//
// 背景：`ETLPipeline.Process` 的 Normalizer 参数在 DATA-10 之前全仓零生产
// 实现，DATA-10 补上了 `CapitalFlowNormalizer`；`top_list` 那条链的适配器
// （`EastmoneyTopListAdapter.fetchTopList`，ODR-011 就在册）一直缺的正是
// 这最后一段 —— 没有 Normalizer，作业层就接不上去。
//
// 它挡的三种形状，每一种都会把**整个写事务**带崩或写进一行没有身份的数据：
//
//   - Symbol 空：`symbol` 是 NOT NULL 列，也是 upsert 冲突键
//     (trade_date, symbol) 的第二列。适配器自己也会跳过空 SECURITY_CODE，
//     但那是源侧的宽容；落库侧不能依赖上游一直守规矩（CR-21 把契约定在
//     storage 侧的同一理由）。
//   - TradeTime 零值：`trade_date` 同样是 NOT NULL + 冲突键第一列。
//     `buildInsertArgs` 把零值时间折成 NULL（bulk_insert.go normalizeDate），
//     于是整行 INSERT 失败、BulkInsert 的单事务整体回滚 —— 一条脏日期
//     就足以让那一天所有榜单一行都落不下去。
//     ⚠️ **这条在本链上目前抓不到东西，而且是明知故留**：上游
//     `parseEastmoneyDate`（eastmoney_sectors_adapter.go:661-679）对缺失/畸形
//     TRADE_DATE 返回的是 `time.Now().UTC()`，**不是零值**。所以日期坏掉的
//     行会带着「今天」的日期通过这道闸 —— 本判据挡住的是*别的*生产者给出
//     零值日期的情形，而不是东财这一条。真正的洞在 `parseEastmoneyDate`
//     自己身上：把坏日期静默折成今天，等于把一行历史榜单写进冲突键
//     (今天, symbol)，既错又覆盖当天真数据。改它属另一单（同一函数也服务
//     limit_up_pool，改动会让那条链的行被 ValidatePoints 丢掉，需要它自己的
//     工单与测试），DBT-10 不顺手改；此处如实登记，不假装这道闸管用。
//   - Data["name"] 缺失或空串：`name` 在 DDL 里是 NOT NULL（空串能过约束，
//     但一行「没有名字的龙虎榜席位」在下游不可解释），而适配器自己的 wire
//     schema 把它声明成 `Required: true`（eastmoney_sectors_adapter.go
//     Schema(DataTypeTopList)）。这一条是本函数独有的判据 ——
//     ValidatePoints 只看 Symbol / TradeTime / Data，看不见 map 里的键。
//
// 返回 Data==nil 即「丢弃这条」（ETLPipeline.Process 的约定），被丢的条数进
// ProcessResult.Skipped —— 不是静默吞掉，是流水线已有的可见计数。
//
// 不做的事（与 CapitalFlowNormalizer 同一条纪律）：
//   - 不换算数值。适配器已经把 BILLBOARD_*_AMT 按元放进 Data，`net_buy` 等
//     四列的 numericColumns 强转（bulk_insert.go）只做类型折算不做量纲折算。
//   - 不改写 / 不补造字段：适配器给了 `close_price` / `change_pct`，但
//     `top_list` 的 TableMapper 列集**不写**这两列（DDL 里有它们，mapper
//     没有 —— 见 pkg/storage/bulk_insert.go:115-118）。Normalizer 不去
//     「帮忙」把它们塞进别处：谁该落库由列集决定，不由谁手里有值决定。
//   - 不复制 Data map：不写就不需要副本。

import "strings"

func TopListNormalizer(item DataItem, sourceName, dataType string) UnifiedDataPoint {
	if item.Symbol == "" || item.TradeTime.IsZero() {
		return UnifiedDataPoint{}
	}
	if name, ok := item.Data["name"].(string); !ok || strings.TrimSpace(name) == "" {
		return UnifiedDataPoint{}
	}
	return UnifiedDataPoint{
		Symbol:    item.Symbol,
		TradeTime: item.TradeTime,
		Source:    sourceName,
		DataType:  dataType,
		Data:      item.Data,
	}
}
