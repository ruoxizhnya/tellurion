package source

// TushareMoneyflowAdapter serves the capital-flow layer (DataTypeCapitalFlow,
// DBT-09) from the Tushare `moneyflow` API, persisted in capital_flow via the
// ETL chain (Registry.Fetch → ETLPipeline.Process → BulkInsert).
//
// 为什么是**又一个** Tushare adapter（而不是给现有 TushareAdapter 加一个
// SupportedTypes 成员）—— 与 tushare_daily_basic / tushare_macro 同一条理由：
//   - 门有自己的 kill switch（DATA_DISABLE_TUSHARE_MONEYFLOW），运维可以只停
//     资金流而不碰日线链；
//   - Registry.Register 按 Name() 覆盖同名 adapter，所以一个独立的门名才能让
//     几族 Tushare 数据并存。
// 它复用同一个 *data.TushareClient（同一个 TUSHARE_TOKEN，0 新密钥）；nil
// client（token 未配置）以 Enabled()=false 注册，启动不失败，Fetch 时跳过。
//
// 为什么这条腿是链上的**首选**（主任 2026-10-19 裁决）：DATA-10 接线的
// eastmoney `fflow/daykline/get` 本机恒不可达（AGENTS.md §14 [DATA-10]），
// 而链语义是「先注册者优先 = 主备优先级」——把不可得的一成员放前面，作业
// 恒失败。代价如实登记：capital_flow 表的数值口径因此变成 **Tushare 的
// 分档成交额形态**（四档净额），而不是东财的净流入时序；两族列的交集见
// Schema 与 pkg/data/tushare_moneyflow.go 的映射表。
//
// ⚠️ 本 adapter **不落库**。capital_flow 的写入面是 ETL 的 BulkInsert（作业层
// capitalFlowExecutor 那一跳）；在这里再写一次就是双写。原始响应由
// TushareClient.call 自动归档进 ingest.raw，也不需要自己写。

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"tellurion/pkg/data"
	apperrors "tellurion/pkg/errors"
)

// moneyflowMaxSpanDays 是单次 Fetch 允许的日历日跨度上限（同
// dailyBasicMaxSpanDays 的那道闸：无界大范围请求会把一次 ETL 拖成小时级
// 配额消耗）。这里更紧，因为本链**逐 symbol 一发请求**：25 票 × 400 日不是
// 400 次而是 25 次全区间请求，每次行数由源侧上界决定。
const moneyflowMaxSpanDays = 400

// TushareMoneyflowAdapter wraps the TushareClient's moneyflow fetch face
// (pkg/data/tushare_moneyflow.go) as a DataSourceAdapter.
type TushareMoneyflowAdapter struct {
	AdapterBase
	client *data.TushareClient
}

// NewTushareMoneyflowAdapter constructs a TushareMoneyflowAdapter. A nil
// client (TUSHARE_TOKEN not configured) yields a disabled adapter: the
// Registry skips it at Fetch time instead of failing at startup.
func NewTushareMoneyflowAdapter(client *data.TushareClient) *TushareMoneyflowAdapter {
	return &TushareMoneyflowAdapter{
		AdapterBase: NewAdapterBase("tushare_moneyflow", client != nil),
		client:      client,
	}
}

// Type implements DataSourceAdapter.Type.
func (a *TushareMoneyflowAdapter) Type() AdapterType { return AdapterTypeHTTP }

// SupportedTypes implements DataSourceAdapter.SupportedTypes.
func (a *TushareMoneyflowAdapter) SupportedTypes() []string {
	return []string{DataTypeCapitalFlow}
}

// Schema implements DataSourceAdapter.Schema.
//
// 只声明本 connector **真正产出**的列（同 tushare_daily_basic 的口径）：
// 四档净额 + period。capital_flow 表里的 main_net / main_buy_amount /
// main_sell_amount / main_net_ratio / retail_net / retail_net_ratio /
// close_price / change_pct 不在这里 —— moneyflow 不给，声明它们就等于宣称
// 本门能供上那些数。它们经 buildInsertArgs 落 NULL（缺失可见，不折 0）。
//
// 单位声明为 "yuan"：源给万元，×10000 的换算在**同步边界**
// （pkg/data/tushare_moneyflow.go 的 cnyPerWanYuanUnit）已经做完，wire 上
// 到这里就是元 —— 与 ETL 侧「不做任何换算」（capital_flow_normalizer.go）
// 不矛盾，那是同一条纪律的另一半。
func (a *TushareMoneyflowAdapter) Schema(dataType string) (DataSchema, error) {
	if dataType != DataTypeCapitalFlow {
		return DataSchema{}, fmt.Errorf("%w: tushare_moneyflow does not serve %s", ErrUnsupported, dataType)
	}
	return DataSchema{
		DataType: DataTypeCapitalFlow,
		Fields: []SchemaField{
			{Name: "period", Type: "string", Required: true},
			{Name: "super_net", Type: "float", Required: false, Unit: "yuan"},
			{Name: "large_net", Type: "float", Required: false, Unit: "yuan"},
			{Name: "medium_net", Type: "float", Required: false, Unit: "yuan"},
			{Name: "small_net", Type: "float", Required: false, Unit: "yuan"},
		},
	}, nil
}

// Fetch implements DataSourceAdapter.Fetch.
//
// 请求形状与链上另一员 eastmoney 一致：Symbols 必填非空、按票逐发请求。
// 这一步是配额敏感的（一票一发 ≈ 5900 票的全池要 5900 次调用），所以
// 「symbols 不许为空」不是洁癖，是把一次误发请求挡在配额墙前。
//
// 失败分类（决定链会不会兜底，别随手改）：
//   - 传输/上游侧失败 → 包成 ErrUpstreamUnavailable，Registry 认定可重试，
//     跳过本门去试 eastmoney（这正是「Tushare 优先、eastmoney 兜底」那条例由
//     之成立的前提；不分类就永远是主用直报、备胎形同虚设）；
//   - 参数非法 / 数据质量（缺身份列、整批脏） → 原样上抛，不可重试：换源
//     重取同一段区间不会让契约变得符合。
func (a *TushareMoneyflowAdapter) Fetch(ctx context.Context, req FetchRequest) (*FetchResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if req.DataType != DataTypeCapitalFlow {
		return nil, fmt.Errorf("%w: tushare_moneyflow: %s", ErrUnsupported, req.DataType)
	}
	if a.client == nil {
		return nil, fmt.Errorf("tushare_moneyflow adapter: nil client")
	}
	if len(req.Symbols) == 0 {
		return nil, fmt.Errorf("tushare_moneyflow capital_flow: symbols required")
	}
	start, end, err := moneyflowDateRange(req)
	if err != nil {
		return nil, err
	}
	period := req.Period
	if period == "" {
		period = "daily"
	}

	items := make([]DataItem, 0, len(req.Symbols)*8)
	for _, sym := range req.Symbols {
		rows, err := a.client.FetchMoneyflowTiers(ctx, sym, start.Format("20060102"), end.Format("20060102"))
		if err != nil {
			wrapped := moneyflowWrapTransportAsUnavailable(err)
			return nil, fmt.Errorf("tushare_moneyflow capital_flow %s: %w", sym, wrapped)
		}
		for _, r := range rows {
			items = append(items, DataItem{
				Symbol:    r.Symbol,
				TradeTime: r.TradeDate,
				Data: map[string]interface{}{
					"period": period,
					// nil 原样进 map：buildInsertArgs 把 nil 折成 NULL。
					// 不写这个键与写 nil 效果相同，但显式写出「这一档可能没有」
					// 比让读者去猜 map 里到底有几把强。
					"super_net":  tierValue(r.SuperNet),
					"large_net":  tierValue(r.LargeNet),
					"medium_net": tierValue(r.MediumNet),
					"small_net":  tierValue(r.SmallNet),
				},
			})
		}
	}
	return &FetchResponse{
		Source:    a.Name(),
		Items:     items,
		FetchedAt: time.Now().UTC(),
	}, nil
}

// tierValue 把可空净额折成 wire 值：nil → nil（缺失可见），非 nil → float64。
// 与 derefOrNil 同形（那边是 *float64 → interface{}，这里同类型同语义）。
func tierValue(v *float64) interface{} {
	if v == nil {
		return nil
	}
	return *v
}

// moneyflowWrapTransportAsUnavailable 把传输级失败归入 ErrUpstreamUnavailable
// （让 Registry 走兜底），而把「换源也治不好」的两类留在原样：参数非法与数据
// 质量。判据是 apperrors 的 code 而不是错误文本 —— 文本会变，code 是契约。
func moneyflowWrapTransportAsUnavailable(err error) error {
	var ae *apperrors.AppError
	if errors.As(err, &ae) {
		switch ae.Code {
		case apperrors.ErrCodeInvalidInput, apperrors.ErrCodeDataQuality:
			return err
		}
	}
	return fmt.Errorf("%w: %v", ErrUpstreamUnavailable, err)
}

// moneyflowDateRange 解析并校验 Fetch 的日期范围：起止都必须给出（缺省补
// 「最新一日」是隐藏默认值，同 dailyBasicDateRange 的裁决），跨度
// ≤ moneyflowMaxSpanDays。
func moneyflowDateRange(req FetchRequest) (time.Time, time.Time, error) {
	start, end := req.StartDate, req.EndDate
	if start.IsZero() || end.IsZero() {
		return time.Time{}, time.Time{}, fmt.Errorf(
			"tushare_moneyflow: StartDate/EndDate required (per-symbol range; no implicit full history)")
	}
	if end.Before(start) {
		return time.Time{}, time.Time{}, fmt.Errorf(
			"tushare_moneyflow: EndDate %s before StartDate %s",
			end.Format("2006-01-02"), start.Format("2006-01-02"))
	}
	if int(end.Sub(start).Hours()/24)+1 > moneyflowMaxSpanDays {
		return time.Time{}, time.Time{}, fmt.Errorf(
			"tushare_moneyflow: range %s..%s exceeds %d calendar days (shard the request)",
			start.Format("2006-01-02"), end.Format("2006-01-02"), moneyflowMaxSpanDays)
	}
	return time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC),
		time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.UTC), nil
}

// HealthCheck implements DataSourceAdapter.HealthCheck.
//
// 探针是**只取不落**（ProbeMoneyflow 不发 normalize、不写库）。取一日而非
// 区间：探测要快且便宜，一发请求就够证明「token 有效 + 接口在」，行数为 0
// （非交易日）同样算连通。
//
// 探针票固定为 600519.SH：它是本仓所有资金流/单位实测用的对照票
// （见 cmd/data/capital_flow_live_test.go 与 capital_flow_normalizer.go 的
// 单位实测记录），换一个票等于换一个未知对照。
func (a *TushareMoneyflowAdapter) HealthCheck(ctx context.Context) error {
	if a.client == nil {
		return fmt.Errorf("tushare_moneyflow adapter: nil client")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	day := time.Now().UTC().Format("20060102")
	if err := a.client.ProbeMoneyflow(probeCtx, "600519.SH", day, day); err != nil {
		return fmt.Errorf("%w: tushare_moneyflow: %v", ErrUpstreamUnavailable, err)
	}
	return nil
}

// RateLimit implements DataSourceAdapter.RateLimit.
func (a *TushareMoneyflowAdapter) RateLimit() RateLimitConfig {
	return RateLimitConfig{
		RequestsPerMinute: 200, // 与 TushareClient 自身的闸值一致（tushareRateLimit）
		Burst:             10,
	}
}

// String renders a debug-friendly summary.
func (a *TushareMoneyflowAdapter) String() string {
	return "TushareMoneyflowAdapter{name=" + a.Name() + ", enabled=" + strconv.FormatBool(a.Enabled()) + "}"
}
