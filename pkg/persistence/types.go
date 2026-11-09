// Package persistence 持有存储层与回测引擎之间共享的**行形状**。
//
// 为什么单独成包（SVC-CORE-RUN-RETIRE ③，2026-11-10）：`pkg/backtest` 不再
// import `pkg/storage`（改为端口注入），而端口方法的签名必须与 PG 实现逐字段
// 一致。把两侧共用的记录类型放在这个叶子包里，`pkg/storage` 用类型别名导出
// （外部 API 与旧调用方不变），`pkg/backtest` 直接引用——于是
// `*storage.PostgresStore` 能**结构化满足** `backtest.Storage`，不需要转换层，
// 也不会为了一个签名把整张表类型复制两份。
package persistence

import "time"

// RunProvenanceRecord 是 quant.run_provenance 的一行。
//
// 列清单由 QT-63 ③-⑦ 固定；新增列要先过那张单。RiskRulesHash 可空
// （QT-42 预留：风控规则版本化之后才有人填）。
type RunProvenanceRecord struct {
	RunID               string    `json:"run_id"`
	RunSpecHash         string    `json:"run_spec_hash"`
	DatasetSnapshotHash string    `json:"dataset_snapshot_hash"`
	ArtifactHash        string    `json:"artifact_hash"`
	VersionHash         string    `json:"version_hash"`
	StrategyRefInput    string    `json:"strategy_ref_input"`
	ParamsJSON          []byte    `json:"params_json"`
	ParamsHash          string    `json:"params_hash"`
	Seed                int64     `json:"seed"`
	Kernel              string    `json:"kernel"`
	EngineVersion       string    `json:"engine_version"`
	FillTiming          string    `json:"fill_timing"`
	RiskRulesHash       string    `json:"risk_rules_hash,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
}

// AdjustmentAnchor 是一个 symbol 在某段回测区间起点的复权基准。
//
// 身份是 (symbol, start_date)：同一区间重跑必须得到同一组锚点，否则价格刻度
// 会随运行漂移。写入口径是首写者胜（ON CONFLICT DO NOTHING）。
type AdjustmentAnchor struct {
	Symbol       string    `json:"symbol"`
	AnchorDate   time.Time `json:"anchor_date"`
	AnchorClose  float64   `json:"anchor_close"`
	SeriesDigest string    `json:"series_digest"`
}
