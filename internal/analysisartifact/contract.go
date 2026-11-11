package analysisartifact

// QT-78 · 分析产物的 wire 形状。
//
// 这是 analytics 侧 `docs/api-schema.md §6.2`（R2 artifact 面）的**第二份拷贝**：
// analytics 的 R2 裁决是「DTO 留在 analytics `internal/analysis/model.go`，
// core 不得 import analytics 的 Go 类型；HTTP 交互不算触发迁移」。所以这里
// 只镜像 JSON 线形状，字段名/可空性与消费端逐字一致，不做第三份。

// ContractVersion 钉住本包镜像的消费端契约版本。
const ContractVersion = "v1"

// 两类产物的 kind（与 `quant.run_analysis_artifacts.kind`、读面路由一一对应）。
const (
	KindPortfolioSnapshot = "portfolio-snapshot"
	KindFactorExposures   = "factor-exposures"
)

// Holding 是一笔期末持仓的归因输入。
type Holding struct {
	Symbol       string  `json:"symbol"`
	Sector       string  `json:"sector"`
	Weight       float64 `json:"weight"`
	PeriodReturn float64 `json:"period_return"`
}

// PortfolioSnapshot 是 Brinson 归因的输入（analytics §6.2）。
type PortfolioSnapshot struct {
	Holdings               []Holding          `json:"holdings"`
	BenchmarkSectorWeights map[string]float64 `json:"benchmark_sector_weights"`
	BenchmarkSectorReturns map[string]float64 `json:"benchmark_sector_returns"`
	BenchmarkReturn        float64            `json:"benchmark_return"`
	DatasetHash            string             `json:"dataset_hash"`
}

// FactorExposure 是单个因子的组合暴露与区间因子收益。
type FactorExposure struct {
	Name         string  `json:"name"`
	Exposure     float64 `json:"exposure"`
	FactorReturn float64 `json:"factor_return"`
}

// FactorExposures 是因子归因的输入（analytics §6.2）。
//
// MissingFactors 是部分缺失时的点名（ADR-038 D3）：有值因子照常计入，但缺哪些
// 必须可见，消费方据此判断归因覆盖度。全缺失不产出本结构，而是返回
// *FactorReturnsUnavailableError。新增字段是加法，旧 reader 解码时忽略即可。
type FactorExposures struct {
	Factors        []FactorExposure `json:"factors"`
	MissingFactors []string         `json:"missing_factors,omitempty"`
	DatasetHash    string           `json:"dataset_hash"`
}
