package data

// 资金流分档净额导入面（DBT-09）—— Tushare `moneyflow` → capital_flow 表，
// 写入经能力层 ETL（adapter → ETLPipeline.Process → BulkInsert），本文件
// **只取数 + 归一，不落库**。
//
// 为什么补这条腿：DATA-10（2026-10-16）把 capital_flow 接进作业层时链上只有
// eastmoney 一员，而那条 path `/api/qt/stock/fflow/daykline/get` 本机恒不可达
// （push2 502 / push2his 连接重置，AGENTS.md §14 [DATA-10]），表始终 0 行。
// Tushare `moneyflow` 实测可得（DBT-07 2026-10-17 复测 code=0；本次 2026-10-19
// 以 600519.SH 2026-09 区间再复测）。主任 2026-10-19 裁决链顺序
// **Tushare 优先、eastmoney 兜底**（理由：可得性 —— "先注册者优先"是主备优先级
// 语义，把不可得的一员放前面等于让作业恒失败）。
//
// 口径映射（「字段对不齐就落 NULL，不硬凑」逐字兑现）：
//
//	特大单净额 → super_net     大单净额 → large_net
//	中单净额   → medium_net    小单净额 → small_net
//
// 四档净额各取**同档**的 buy_*_amount − sell_*_amount：同一行、同一档、同一
// 单位相减，不含任何跨档假设。
//
// 以下 capital_flow 列本 connector **不产出**，经 storage.buildInsertArgs 落
// NULL（NULL 是「源没给」，0 是「源给了 0」，下游含义不同）：
//   - main_net / main_buy_amount / main_sell_amount —— moneyflow 没有「主力」
//     这一档；把 super+large 相加当主力是东财的分档惯例，不是本源给的字段。
//   - **net_mf_amount 不映射给任何列**。2026-10-19 实测 600519.SH 20260930：
//     四档 (buy−sell) 之和 = 41024.10 + 13010.63 − 53998.32 − 36.41 = 0.00
//     （每笔成交买卖必相等，求和抵消是结构性的），而同一行的
//     net_mf_amount = 60958.84 万元 —— 两者对不上，说明它的口径在本仓无从确证。
//     拿一个定义不明的源字段去填一个有明确东财含义的列，正是「硬凑」。
//   - main_net_ratio / retail_net / retail_net_ratio —— 比率的分母（东财按当日
//     成交额）本源不提供，「散户」档的定义亦不同。
//   - close_price / change_pct —— moneyflow 不携带价格。
//
// 单位：`*_amount` 是**万元**（2026-10-19 实测 600519 单日四档买入合计
// ≈48 亿元，与同日成交额同量纲 ⇒ 万元坐实），规范层是元（CNY），故在**同步
// 边界** ×cnyPerWanYuanUnit —— 与 tushare 日线「vol 手→股、amount 千元→CNY」
// （DATA-04/05，见 tushare.go 的 sharesPerLot / cnyPerAmountUnit）同一条纪律。
// ETL 侧「不做任何换算」（capital_flow_normalizer.go）因此仍然成立：换算只
// 发生一次，且发生在源侧。
//
// 纪律（与 tushare_daily_basic.go 同款）：
//   - **错误一律向上传播**：call 失败、缺身份列、落库前置校验不过，全部包装后
//     return err，绝不 log 后 continue；
//   - **有行但全被跳过 → fail-loud**（moneyflowAllSkippedError）：真空响应
//     （非交易日 items==0）才是合法的「0 行」；
//   - **缺失≠0**：任一档的买或卖缺一侧 ⇒ 该档净额为 nil（落 NULL）；
//   - **按列名定位索引**：Tushare 会剔除该期全空的列（DBT-07 那个缺陷的根因
//     —— 按固定索引解包会把整行右移，把 roe 写进 pe 列），所以列缺失时其余
//     列仍必须对位。

import (
	"context"
	"fmt"
	"strings"
	"time"

	apperrors "github.com/ruoxizhnya/tellurion-kit/errors"
)

// cnyPerWanYuanUnit 把 Tushare 资金流类接口的「万元」折成规范层的「元」（CNY）。
// 与 sharesPerLot / cnyPerAmountUnit 同一个家族：换算只放在同步边界一处。
const cnyPerWanYuanUnit = 10000.0

// moneyflowFields 只取身份列 + 四档买卖额。
//
// 刻意**不请求** net_mf_amount / *_vol：前者口径无从确证（见文件头），后者是
// 「手」单位的量额，capital_flow 表里没有对应列，取来只会诱使后人硬凑。
const moneyflowFields = "ts_code,trade_date," +
	"buy_elg_amount,sell_elg_amount," +
	"buy_lg_amount,sell_lg_amount," +
	"buy_md_amount,sell_md_amount," +
	"buy_sm_amount,sell_sm_amount"

// moneyflowTier 是一个资金流档位在 capital_flow 表里的落库列名 + 源侧买卖列名。
type moneyflowTier struct {
	// netColumn 是 capital_flow 的净额列（同时是 ETL Data map 的键）。
	netColumn string
	// buyField / sellField 是源侧同一档的买入额与卖出额列名。
	buyField  string
	sellField string
}

// moneyflowTiers 是四档映射，顺序即 super/large/medium/small。
// 顺序只影响报错文案的稳定性，不影响解包（解包按列名）。
var moneyflowTiers = []moneyflowTier{
	{netColumn: "super_net", buyField: "buy_elg_amount", sellField: "sell_elg_amount"},
	{netColumn: "large_net", buyField: "buy_lg_amount", sellField: "sell_lg_amount"},
	{netColumn: "medium_net", buyField: "buy_md_amount", sellField: "sell_md_amount"},
	{netColumn: "small_net", buyField: "buy_sm_amount", sellField: "sell_sm_amount"},
}

// MoneyflowTierNet 是一票一日的分档净额观测，单位为**元（CNY）**。
//
// 四个净额都是可空的：nil = 该档本行不可得（源剔除了整列、或该档买/卖缺一侧、
// 或该侧未发布）。nil 落到 capital_flow 是 NULL，不是 0 —— 这是本仓的「缺失≠0」
// 全局口径。
type MoneyflowTierNet struct {
	Symbol    string
	TradeDate time.Time
	SuperNet  *float64
	LargeNet  *float64
	MediumNet *float64
	SmallNet  *float64
}

// NetByColumn 按 capital_flow 列名取该档净额，供 ETL adapter 组装 Data map。
// 未知列名返回 nil（调用方只会拿到 nil，不会把一个不存在的列写成 0）。
func (m MoneyflowTierNet) NetByColumn(column string) *float64 {
	switch column {
	case "super_net":
		return m.SuperNet
	case "large_net":
		return m.LargeNet
	case "medium_net":
		return m.MediumNet
	case "small_net":
		return m.SmallNet
	default:
		return nil
	}
}

// FetchMoneyflowTiers 拉取**单只票**在 [startDate, endDate] 区间的分档资金流
// 净额（单位元），归一后返回，**不落库**。
//
// 日期为 "YYYYMMDD"，两者都必须给出：全历史区间在 moneyflow 上没有可控行数
// 上界，而「不给就拉全历史」是一个隐藏默认值（同 D5 的 StartDate/EndDate
// 必填口径）。非交易日上游返回空 items —— 合法，返回 (nil, nil)。
//
// 单行被跳过的三种形状（各自计数，全部跳过 → fail-loud）：身份不全
// （ts_code 空）、PIT 身份不可解析（trade_date 坏）、数值非 nil 但不可解析/
// 非有限（脏值不进库）。响应**整列**缺失时不做同样的宽容：身份列缺一即报错
// （契约不符），四个档位列缺谁就把谁置 nil —— 后者是 Tushare 剔除全空列的
// 既定行为（DBT-07 的根因），前者不是。
func (c *TushareClient) FetchMoneyflowTiers(ctx context.Context, tsCode, startDate, endDate string) ([]MoneyflowTierNet, error) {
	if strings.TrimSpace(tsCode) == "" {
		return nil, apperrors.New(apperrors.ErrCodeInvalidInput, "moneyflow ts_code 必填（本接口按票取数，不给票就是无界请求）")
	}
	for _, p := range []struct {
		name  string
		value string
	}{
		{"start_date", startDate},
		{"end_date", endDate},
	} {
		if _, err := time.Parse("20060102", p.value); err != nil {
			return nil, apperrors.New(apperrors.ErrCodeInvalidInput, fmt.Sprintf(
				"moneyflow %s 必须是 YYYYMMDD（got %q）", p.name, p.value))
		}
	}

	resp, err := c.call(ctx, "moneyflow", map[string]interface{}{
		"ts_code":    tsCode,
		"start_date": startDate,
		"end_date":   endDate,
	}, moneyflowFields)
	if err != nil {
		return nil, err
	}

	rows, skipped, err := normalizeMoneyflow(resp)
	if err != nil {
		return nil, err
	}
	if err := moneyflowAllSkippedError(tsCode, len(resp.Data.Items), skipped); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		// 真空响应（非交易日 / 该票区间无数据）——合法。
		return nil, nil
	}

	c.logger.Info().Str("ts_code", tsCode).Str("start_date", startDate).
		Str("end_date", endDate).Int("count", len(rows)).
		Int("skipped", skipped.Skipped()).Msg("moneyflow fetched (ETL 侧落库，本方法不写)")
	return rows, nil
}

// ProbeMoneyflow 对单票做一日期区间**只取不落**的连通性探测（HealthCheck 用）。
// 不 normalize、不落库 —— 探测不应产生数据副作用。
func (c *TushareClient) ProbeMoneyflow(ctx context.Context, tsCode, startDate, endDate string) error {
	_, err := c.call(ctx, "moneyflow", map[string]interface{}{
		"ts_code":    tsCode,
		"start_date": startDate,
		"end_date":   endDate,
	}, moneyflowFields)
	return err
}

// moneyflowSkipped 是 normalizeMoneyflow 的跳过计数。
type moneyflowSkipped struct {
	NilTier  int // 四档全为 nil（身份完整、数值未发布——**照常入库**，不跳过）
	BadValue int // 某档买/卖非 nil 但不可解析/非有限，或行比表头短（脏值，跳过该行）
	BadCode  int // ts_code 为空（身份不完整）
	BadDate  int // trade_date 不可解析（PIT 身份不完整）
}

// Skipped 是总跳过行数（BadCode + BadDate + BadValue；NilTier 不跳过）。
func (n moneyflowSkipped) Skipped() int {
	return n.BadCode + n.BadDate + n.BadValue
}

// moneyflowColumnIndex 在响应的 Fields 头里按**名字（大小写不敏感）**找列下标，
// 找不到返回 -1（同 dailyBasicColumnIndex / macroColumnIndex 的口径）。
//
// 这一层就是 DBT-07 那个缺陷的解药：Tushare 会剔除该期全空的列，响应里的
// 列序因此**不固定**。按固定索引解包 = 把整行右移，把某一档的钱写进另一档的列。
func moneyflowColumnIndex(resp *TushareResponse, name string) int {
	lower := make(map[string]int, len(resp.Data.Fields))
	for i, f := range resp.Data.Fields {
		lower[strings.ToLower(f)] = i
	}
	if idx, ok := lower[strings.ToLower(name)]; ok {
		return idx
	}
	return -1
}

// normalizeMoneyflow 把 moneyflow 响应转为分档净额观测（纯函数，不落库）。
//
// 三条判定：
//   - ts_code / trade_date 两列**整列缺失** → 报错（契约形状不符，身份列不可选）；
//   - 档位列整列缺失 → 该档 net = nil（Tushare 剔除全空列是既定行为，不是事故）；
//   - 单行某档买卖缺一侧 → 该档 net = nil（不拿 0 顶包：0 会被下游读成
//     「该档买卖恰好相等」，而事实是「源没给」）。
func normalizeMoneyflow(resp *TushareResponse) ([]MoneyflowTierNet, moneyflowSkipped, error) {
	iCode := moneyflowColumnIndex(resp, "ts_code")
	iDate := moneyflowColumnIndex(resp, "trade_date")
	var missing []string
	if iCode < 0 {
		missing = append(missing, "ts_code")
	}
	if iDate < 0 {
		missing = append(missing, "trade_date")
	}
	if len(missing) > 0 {
		return nil, moneyflowSkipped{}, apperrors.New(apperrors.ErrCodeDataQuality, fmt.Sprintf(
			"moneyflow 响应缺身份列 %v（实际 fields=%v）—— 形状不符契约，身份列不接受缺省",
			missing, resp.Data.Fields))
	}

	// 每档买卖列各自按名字定位一次（不在行内重复构造映射表）。
	// 整列被剔除（下标 -1）时该档留 nil —— 这是 Tushare 剔除该期全空列的既定
	// 行为，不是事故，所以这里**不报错**；报错的只有身份列。
	type tierIndex struct {
		net       string
		buy, sell int
	}
	tiers := make([]tierIndex, 0, len(moneyflowTiers))
	for _, t := range moneyflowTiers {
		tiers = append(tiers, tierIndex{
			net:  t.netColumn,
			buy:  moneyflowColumnIndex(resp, t.buyField),
			sell: moneyflowColumnIndex(resp, t.sellField),
		})
	}

	var (
		out     []MoneyflowTierNet
		skipped moneyflowSkipped
	)
	for _, item := range resp.Data.Items {
		symbol := strings.TrimSpace(tushareCell(item, iCode))
		if symbol == "" {
			skipped.BadCode++
			continue
		}
		d, ok := parseTradeDateCell(tushareCell(item, iDate))
		if !ok {
			skipped.BadDate++
			continue
		}

		row := MoneyflowTierNet{Symbol: symbol, TradeDate: d}
		dirty := false
		for ti := range tiers {
			t := tiers[ti]
			if t.buy < 0 || t.sell < 0 {
				continue // 整列被剔除 → 该档 nil（缺失可见）
			}
			if t.buy >= len(item) || t.sell >= len(item) {
				dirty = true // 行比表头短：形状变故，整行不进库
				break
			}
			buyV, buyBad := moneyflowAmount(item, t.buy)
			sellV, sellBad := moneyflowAmount(item, t.sell)
			if buyBad || sellBad {
				dirty = true
				break
			}
			if buyV == nil || sellV == nil {
				continue // 缺一侧 ⇒ 净额不可得 ⇒ nil（不折 0）
			}
			net := (*buyV - *sellV) * cnyPerWanYuanUnit
			switch t.net {
			case "super_net":
				row.SuperNet = &net
			case "large_net":
				row.LargeNet = &net
			case "medium_net":
				row.MediumNet = &net
			case "small_net":
				row.SmallNet = &net
			}
		}
		if dirty {
			skipped.BadValue++
			continue
		}
		if row.SuperNet == nil && row.LargeNet == nil && row.MediumNet == nil && row.SmallNet == nil {
			skipped.NilTier++ // 计数供审计，但**不跳过**（身份完整的「数值未发布」观测）
		}
		out = append(out, row)
	}
	return out, skipped, nil
}

// moneyflowAmount 取一个可空数值列：nil（或缺位）→ (nil, false)（缺失）；
// 非 nil 可解析且有限 → (值, false)；非 nil 但不可解析/非有限 → (nil, true)
// （脏值，调用方跳过该行）。与 activityValue 同形。
func moneyflowAmount(item []any, idx int) (*float64, bool) {
	if idx < 0 || idx >= len(item) || item[idx] == nil {
		return nil, false
	}
	v, ok := tushareFloatOK(item[idx])
	if !ok {
		return nil, true
	}
	return &v, false
}

// moneyflowAllSkippedError 是 fail-loud 闸门：响应**有行**却一行都没能归一
// 出来时拒绝静默成功（上游改格式 / 列整体变空的典型形态）。
// 真空响应（items==0，非交易日）不在此列 —— 那是合法的「导入 0 行」。
func moneyflowAllSkippedError(tsCode string, items int, skipped moneyflowSkipped) error {
	if items == 0 || skipped.Skipped() < items {
		return nil
	}
	return apperrors.New(apperrors.ErrCodeDataQuality, fmt.Sprintf(
		"资金流 %s：moneyflow 响应返回 %d 行但全部被跳过（坏代码 %d / 坏日期 %d / 脏值 %d）——上游形状/格式变故，拒绝以「导入 0 行」伪装成功",
		tsCode, items, skipped.BadCode, skipped.BadDate, skipped.BadValue))
}
