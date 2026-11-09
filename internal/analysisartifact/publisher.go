package analysisartifact

// QT-78 · 分析产物的编排：读 run 终态 → 构建 → 落 core 载荷表 → 回传三 hash 身份。
//
// 投递只走 orchestration 的 `POST /api/runs/jobs/{id}/artifacts`（裁決 ⑱：
// worker 不直写 run.*）。该端点只收身份（三 hash + 指针），不收载荷；载荷落
// core 自己的 `quant.run_analysis_artifacts`，由 `cmd/analysis` 的两个只读
// 端点按 analytics 的 R2 形状原样返回。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/ruoxizhnya/tellurion-contracts/engine"

	"tellurion/pkg/storage"
)

// EnginePort 是发布器需要的引擎读面。
type EnginePort interface {
	GetBacktestPositions(runID string) ([]domain.Position, error)
	GetFactorZScore(factor domain.FactorType, date time.Time, symbol string) (float64, bool)
}

// ProvenanceReader 读一次 run 的输入身份（quant.run_provenance）。
type ProvenanceReader interface {
	GetRunProvenance(ctx context.Context, runID string) (*storage.RunProvenanceRecord, error)
}

// ArtifactStore 落分析产物载荷。
type ArtifactStore interface {
	SaveAnalysisArtifact(ctx context.Context, rec *storage.AnalysisArtifactRecord) (*storage.AnalysisArtifactRecord, error)
}

// Deliverer 把身份回传给 orchestration。
type Deliverer interface {
	SaveArtifact(ctx context.Context, runID string, body []byte) error
}

// Publisher 串起「产出 + 落库 + 回传身份」。
type Publisher struct {
	Engine        EnginePort
	Market        MarketProvider
	Catalog       StockCatalog
	FactorReturns FactorReturnReader
	Provenance    ProvenanceReader
	Artifacts     ArtifactStore
	Deliverer     Deliverer
	Benchmark     string
	// BenchmarkFor 可选：按 run 覆盖基准指数（空串 = 用 Benchmark 默认值）。
	BenchmarkFor func(ctx context.Context, runID string) string
	Factors      []domain.FactorType
	Log          zerolog.Logger
}

type savedArtifact struct {
	kind string
	hash string
}

// Publish 在 run 完成后尽力产出两类产物并回传身份。
//
// 失败语义：产物缺输入 → 记日志、不落库（消费侧 fail-closed）；身份回传失败
// → 返回错误交给调用方记录，但不改变 run 的完成结论。
func (p *Publisher) Publish(ctx context.Context, resp *contracts.BacktestResponse) error {
	if p == nil || resp == nil || resp.ID == "" {
		return nil
	}
	if p.Provenance == nil || p.Artifacts == nil || p.Deliverer == nil || p.Engine == nil {
		return fmt.Errorf("analysisartifact: publisher is not fully wired")
	}

	prov, err := p.Provenance.GetRunProvenance(ctx, resp.ID)
	if err != nil {
		return fmt.Errorf("analysisartifact: read provenance for %s: %w", resp.ID, err)
	}
	if prov == nil {
		return fmt.Errorf("analysisartifact: run_provenance not found for %s", resp.ID)
	}

	saved := p.produceArtifacts(ctx, resp, prov)

	body, err := p.identityBody(prov, len(saved) > 0)
	if err != nil {
		return err
	}
	if err := p.Deliverer.SaveArtifact(ctx, resp.ID, body); err != nil {
		return fmt.Errorf("analysisartifact: deliver identity for %s: %w", resp.ID, err)
	}
	if len(saved) > 0 {
		p.Log.Info().
			Str("run_id", resp.ID).
			Str("bundle_hash", bundleHash(saved)).
			Int("artifacts", len(saved)).
			Msg("analysis artifacts published")
	}
	return nil
}

// produceArtifacts 逐类构建并落库；每一类独立 fail-closed。
func (p *Publisher) produceArtifacts(ctx context.Context, resp *contracts.BacktestResponse, prov *storage.RunProvenanceRecord) []savedArtifact {
	start, okStart := parseRunDate(resp.StartDate)
	end, okEnd := parseRunDate(resp.EndDate)
	if !okStart || !okEnd {
		p.Log.Warn().
			Str("run_id", resp.ID).
			Str("start_date", resp.StartDate).
			Str("end_date", resp.EndDate).
			Msg("analysis artifacts skipped: run window is not parseable")
		return nil
	}
	if p.Market == nil || p.Catalog == nil {
		p.Log.Warn().Str("run_id", resp.ID).Msg("analysis artifacts skipped: benchmark market data is not wired")
		return nil
	}
	benchmark := p.Benchmark
	if p.BenchmarkFor != nil {
		if override := p.BenchmarkFor(ctx, resp.ID); override != "" {
			benchmark = override
		}
	}
	if benchmark == "" {
		p.Log.Warn().Str("run_id", resp.ID).Msg("analysis artifacts skipped: no benchmark index configured")
		return nil
	}

	positions, err := p.Engine.GetBacktestPositions(resp.ID)
	if err != nil {
		p.Log.Warn().Str("run_id", resp.ID).Err(err).Msg("analysis artifacts skipped: positions unavailable")
		return nil
	}
	stocks, err := p.Catalog.Stocks(ctx)
	if err != nil {
		p.Log.Warn().Str("run_id", resp.ID).Err(err).Msg("analysis artifacts skipped: stock catalog unavailable")
		return nil
	}
	bySymbol := make(map[string]domain.Stock, len(stocks))
	for _, s := range stocks {
		bySymbol[s.Symbol] = s
	}

	builder := &Builder{
		Market:        p.Market,
		ZScores:       p.Engine,
		FactorReturns: p.FactorReturns,
		Factors:       p.Factors,
	}
	in := Input{
		RunID:          resp.ID,
		DatasetHash:    prov.DatasetSnapshotHash,
		StartDate:      start,
		EndDate:        end,
		BenchmarkIndex: benchmark,
		Positions:      positions,
		Stocks:         bySymbol,
	}

	buildPortfolio := func(ctx context.Context, in Input) (any, error) { return builder.BuildPortfolio(ctx, in) }
	buildFactors := func(ctx context.Context, in Input) (any, error) { return builder.BuildFactors(ctx, in) }

	var saved []savedArtifact
	if rec, err := p.savePayload(ctx, resp.ID, KindPortfolioSnapshot, in.DatasetHash, buildPortfolio, in); err != nil {
		p.Log.Warn().Str("run_id", resp.ID).Str("kind", KindPortfolioSnapshot).Err(err).Msg("analysis artifact not produced")
	} else {
		saved = append(saved, savedArtifact{kind: KindPortfolioSnapshot, hash: rec.ArtifactHash})
	}

	if p.FactorReturns != nil {
		if rec, err := p.savePayload(ctx, resp.ID, KindFactorExposures, in.DatasetHash, buildFactors, in); err != nil {
			p.Log.Warn().Str("run_id", resp.ID).Str("kind", KindFactorExposures).Err(err).Msg("analysis artifact not produced")
		} else {
			saved = append(saved, savedArtifact{kind: KindFactorExposures, hash: rec.ArtifactHash})
		}
	}
	return saved
}

// savePayload 构建一类产物、规范化序列化、落库。
func (p *Publisher) savePayload(
	ctx context.Context,
	runID, kind, datasetHash string,
	build func(context.Context, Input) (any, error),
	in Input,
) (*storage.AnalysisArtifactRecord, error) {
	payload, err := build(ctx, in)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", kind, err)
	}
	sum := sha256.Sum256(raw)
	return p.Artifacts.SaveAnalysisArtifact(ctx, &storage.AnalysisArtifactRecord{
		RunID:        runID,
		Kind:         kind,
		DatasetHash:  datasetHash,
		ArtifactHash: hex.EncodeToString(sum[:]),
		Payload:      raw,
	})
}

// identityBody 组装 orchestration `queue.ArtifactRecord` 的线形状（JSON tag 逐字对齐）。
//
// 关键裁决：`artifact_hash` 列是**策略产物** hash，不把分析 bundle hash 写进去，
// 否则 orchestration 的 provenance 读面会把分析产物错报成策略产物。
func (p *Publisher) identityBody(prov *storage.RunProvenanceRecord, hasPayload bool) ([]byte, error) {
	if prov.StrategyRefInput == "" || prov.RunSpecHash == "" || prov.ParamsHash == "" {
		// 身份不完整：投递也只会被写端点 400 拒掉，不制造无效请求。
		return nil, fmt.Errorf("analysisartifact: provenance identity incomplete for %s", prov.RunID)
	}

	params := prov.ParamsJSON
	if len(params) == 0 {
		params = []byte("{}")
	}
	seed := prov.Seed
	artifactURI := ""
	if hasPayload {
		artifactURI = "analysis:" + prov.RunID
	}

	body := struct {
		RunID               string          `json:"run_id"`
		StrategyRef         string          `json:"strategy_ref"`
		DatasetSnapshotHash string          `json:"dataset_snapshot_hash"`
		RiskRulesHash       string          `json:"risk_rules_hash,omitempty"`
		ArtifactURI         string          `json:"artifact_uri,omitempty"`
		ArtifactHash        string          `json:"artifact_hash,omitempty"`
		VersionHash         string          `json:"version_hash,omitempty"`
		RunSpecHash         string          `json:"run_spec_hash"`
		ParamsHash          string          `json:"params_hash"`
		ParamsJSON          json.RawMessage `json:"params_json,omitempty"`
		Seed                *int64          `json:"seed,omitempty"`
		Kernel              string          `json:"kernel"`
		EngineVersion       string          `json:"engine_version"`
		FillTiming          string          `json:"fill_timing"`
	}{
		RunID:               prov.RunID,
		StrategyRef:         prov.StrategyRefInput,
		DatasetSnapshotHash: prov.DatasetSnapshotHash,
		RiskRulesHash:       prov.RiskRulesHash,
		ArtifactURI:         artifactURI,
		ArtifactHash:        prov.ArtifactHash,
		VersionHash:         prov.VersionHash,
		RunSpecHash:         prov.RunSpecHash,
		ParamsHash:          prov.ParamsHash,
		ParamsJSON:          params,
		Seed:                &seed,
		Kernel:              prov.Kernel,
		EngineVersion:       prov.EngineVersion,
		FillTiming:          prov.FillTiming,
	}
	return json.Marshal(body)
}

// bundleHash 对「已落库的 kind + 内容 hash」做稳定摘要（仅用于日志留痕）。
func bundleHash(saved []savedArtifact) string {
	items := make([]savedArtifact, len(saved))
	copy(items, saved)
	sort.Slice(items, func(i, j int) bool { return items[i].kind < items[j].kind })

	h := sha256.New()
	for _, it := range items {
		h.Write([]byte(it.kind))
		h.Write([]byte{0})
		h.Write([]byte(it.hash))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// parseRunDate 解析回测窗口端点（引擎写 RFC3339；历史落盘可能只有日期）。
func parseRunDate(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
