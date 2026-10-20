package source

// DATA-10（2026-10-16 主任裁决：入口 (b) 作业层）落的第一件生产 Normalizer。
//
// 背景：`ETLPipeline.Process` 的 Normalizer 参数此前全仓零生产实现
// （只有 etl_test.go / integration_test.go 里的内联闭包），这也是
// DATA-08「停用路线」列出的接线前置条件之一。本文件把它补上。
//
// 单位口径（④「单位口径不重开，接线只做换算到规范层」的逐字兑现）：
// **不做任何换算**。依据是实测而不是文档：
//   - push2 同族分钟端点 `/api/qt/stock/fflow/kline/get` 2026-10-16 真取到
//     600519 14:51 累计主力 `357,794,007`，同日 `/api/qt/stock/kline/get`
//     给当日成交额 `5,075,528,306`（元）⇒ fflow 系列数值与成交额同量纲，
//     即**元 = 规范层 CNY**，与 wire schema 的 `Unit: "yuan"` 声明一致。
//   - 日线端点 `/api/qt/stock/fflow/daykline/get`（本 connector 的取数路径）
//     当天本机不可达（push2 恒 502、push2his 连接重置；同主机兄弟路径 200），
//     所以日线值本身没有直接读数，单位是从同族端点推的。该缺口如实登记在
//     docs/TASKS.md DATA-10 完成记录里，不写成「已实测日线」。
//
// 与 tushare 日线那族的差别要记住：那边是源给「手 / 千元」、同步边界
// ×100 / ×1000（DATA-04/05）；这边源已经给元，再乘一次就是 DATA-04 的坑
// 换个方向重演。所以「不换算」是一条有内容的决定，不是偷懒。

import (
	"strings"
)

// CapitalFlowNormalizer 把一个 eastmoney push2 资金流 DataItem 转成
// UnifiedDataPoint。
//
// 返回 Data==nil 即「丢弃这条」（ETLPipeline.Process 的约定），被丢的条数
// 进 ProcessResult.Skipped —— 不是静默吞掉，是流水线已有的可见计数。
// 三道判定各挡一种会把整批写事务带崩的形状：
//
//   - Symbol 空：`symbol` 是 NOT NULL 列，也是 upsert 冲突键的一部分。
//   - TradeTime 零值：`trade_date` 同样 NOT NULL；buildInsertArgs 会把零值
//     时间折成 NULL，于是整行 INSERT 失败、BulkInsert 的单事务整体回滚。
//   - Data["period"] 缺失或空串：它是冲突键 (symbol, trade_date, period)
//     的第三列且 NOT NULL。这一条是**本函数独有**的判据 ——
//     ValidatePoints 只看 Symbol / TradeTime / Data，看不见 map 里的键，
//     所以漏了它，一条脏 period 就足以让一整批数据一行都落不下去。
//
// 不做的事（都是刻意的，别顺手加）：
//   - 不换算数值（见文件头单位依据）。
//   - 不改写 / 不补造字段：adapter 的 wire schema 没给 main_buy_amount /
//     main_sell_amount，就让它们按 buildInsertArgs 的既定语义落 NULL。
//     NULL 是「源没给」，0 是「源给了 0」，两者的下游含义不同。
//   - 不复制 Data map：不写就不需要副本，复制只会让人以为这里会做转换。
func CapitalFlowNormalizer(item DataItem, sourceName, dataType string) UnifiedDataPoint {
	if item.Symbol == "" || item.TradeTime.IsZero() {
		return UnifiedDataPoint{}
	}
	if period, ok := item.Data["period"].(string); !ok || strings.TrimSpace(period) == "" {
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
