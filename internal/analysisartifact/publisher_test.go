package analysisartifact

// QT-78：发布器的「产出 → 落库 → 回传身份」三条边界。

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/rs/zerolog"
	cdom "github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/ruoxizhnya/tellurion-contracts/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/storage"
)

import "tellurion/pkg/domain"

type fakeProvenance struct {
	rec *storage.RunProvenanceRecord
	err error
}

func (f fakeProvenance) GetRunProvenance(ctx context.Context, runID string) (*storage.RunProvenanceRecord, error) {
	return f.rec, f.err
}

type fakeEngine struct {
	positions []domain.Position
	zscores   fakeZScores
}

func (f fakeEngine) GetBacktestPositions(runID string) ([]domain.Position, error) {
	return f.positions, nil
}

func (f fakeEngine) GetFactorZScore(factor cdom.FactorType, date time.Time, symbol string) (float64, bool) {
	if f.zscores == nil {
		return 0, false
	}
	return f.zscores.GetFactorZScore(factor, date, symbol)
}

type fakeCatalog struct {
	stocks []domain.Stock
}

func (f fakeCatalog) Stocks(ctx context.Context) ([]domain.Stock, error) {
	return f.stocks, nil
}

type fakeArtifactStore struct {
	saved []*storage.AnalysisArtifactRecord
}

func (f *fakeArtifactStore) SaveAnalysisArtifact(ctx context.Context, rec *storage.AnalysisArtifactRecord) (*storage.AnalysisArtifactRecord, error) {
	f.saved = append(f.saved, rec)
	return rec, nil
}

type fakeDeliverer struct {
	calls int
	runID string
	body  []byte
	err   error
}

func (f *fakeDeliverer) SaveArtifact(ctx context.Context, runID string, body []byte) error {
	f.calls++
	f.runID = runID
	f.body = body
	return f.err
}

func provenanceFixture() *storage.RunProvenanceRecord {
	return &storage.RunProvenanceRecord{
		RunID:               "run-1",
		RunSpecHash:         "spec-hash",
		DatasetSnapshotHash: "ds-1",
		ArtifactHash:        "policy-hash",
		VersionHash:         "",
		StrategyRefInput:    "momentum@v1",
		ParamsJSON:          []byte(`{"lookback":20}`),
		ParamsHash:          "params-hash",
		Seed:                7,
		Kernel:              "go",
		EngineVersion:       "core-test",
		FillTiming:          "next_open",
	}
}

func publisherFixture() (*Publisher, *fakeEngine, *fakeArtifactStore, *fakeDeliverer) {
	in, market := portfolioFixture()
	eng := &fakeEngine{
		positions: in.Positions,
		zscores: fakeZScores{
			"A": {cdom.FactorMomentum: 1.0},
			"B": {cdom.FactorMomentum: -1.0},
		},
	}
	store := &fakeArtifactStore{}
	deliverer := &fakeDeliverer{}
	catalog := fakeCatalog{stocks: []domain.Stock{in.Stocks["A"], in.Stocks["B"], in.Stocks["C"]}}

	p := &Publisher{
		Engine:        eng,
		Market:        market,
		Catalog:       catalog,
		FactorReturns: fakeFactorReturns{cdom.FactorMomentum: {{TradeDate: in.EndDate, TopMinusBot: 0.05}}},
		Provenance:    fakeProvenance{rec: provenanceFixture()},
		Artifacts:     store,
		Deliverer:     deliverer,
		Benchmark:     "000300.SH",
		Factors:       []cdom.FactorType{cdom.FactorMomentum},
		Log:           zerolog.Nop(),
	}
	return p, eng, store, deliverer
}

func responseFixture() *contracts.BacktestResponse {
	return &contracts.BacktestResponse{
		ID:        "run-1",
		Status:    "completed",
		StartDate: "2024-01-02",
		EndDate:   "2024-01-31",
	}
}

func TestPublish_ProducesBothArtifactsAndDeliversIdentity(t *testing.T) {
	t.Parallel()

	p, _, store, deliverer := publisherFixture()
	require.NoError(t, p.Publish(context.Background(), responseFixture()))

	// 两类载荷都落库，artifact_hash 是内容摘要（非空）。
	require.Len(t, store.saved, 2)
	kinds := map[string]bool{}
	for _, rec := range store.saved {
		kinds[rec.Kind] = true
		assert.Equal(t, "ds-1", rec.DatasetHash)
		assert.NotEmpty(t, rec.ArtifactHash)
		assert.True(t, json.Valid(rec.Payload))
	}
	assert.True(t, kinds[KindPortfolioSnapshot])
	assert.True(t, kinds[KindFactorExposures])

	// 回传的是身份，不是载荷：artifact_uri 指回 core，artifact_hash 是策略产物 hash。
	require.Equal(t, 1, deliverer.calls)
	assert.Equal(t, "run-1", deliverer.runID)

	var body map[string]any
	require.NoError(t, json.Unmarshal(deliverer.body, &body))
	assert.Equal(t, "analysis:run-1", body["artifact_uri"])
	assert.Equal(t, "policy-hash", body["artifact_hash"],
		"artifact_hash 必须沿用 run_provenance 的策略产物 hash，不能写分析 bundle hash")
	assert.Equal(t, "spec-hash", body["run_spec_hash"])
	assert.Equal(t, "params-hash", body["params_hash"])
	assert.InDelta(t, 7, body["seed"], 0)
}

func TestPublish_NoPayloadStillDeliversIdentity(t *testing.T) {
	t.Parallel()

	p, eng, store, deliverer := publisherFixture()
	eng.positions = nil

	require.NoError(t, p.Publish(context.Background(), responseFixture()))

	assert.Empty(t, store.saved, "没有持仓就不能产出载荷（fail-closed）")
	require.Equal(t, 1, deliverer.calls)

	var body map[string]any
	require.NoError(t, json.Unmarshal(deliverer.body, &body))
	_, hasURI := body["artifact_uri"]
	assert.False(t, hasURI, "没有载荷时不写 artifact_uri")
}

func TestPublish_IncompleteIdentityIsRejectedBeforeDelivery(t *testing.T) {
	t.Parallel()

	p, _, _, deliverer := publisherFixture()
	p.Provenance = fakeProvenance{rec: &storage.RunProvenanceRecord{RunID: "run-1"}}

	err := p.Publish(context.Background(), responseFixture())
	require.Error(t, err)
	assert.Equal(t, 0, deliverer.calls, "身份不全不制造无效请求")
}

func TestPublish_DeliverErrorIsReturned(t *testing.T) {
	t.Parallel()

	p, _, _, deliverer := publisherFixture()
	deliverer.err = errors.New("orchestration down")

	err := p.Publish(context.Background(), responseFixture())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "orchestration down")
}

func TestPublish_RequiresFullWiring(t *testing.T) {
	t.Parallel()

	p := &Publisher{Log: zerolog.Nop()}
	err := p.Publish(context.Background(), responseFixture())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not fully wired")
}

func TestPublish_NilResponseIsNoop(t *testing.T) {
	t.Parallel()

	p, _, _, deliverer := publisherFixture()
	require.NoError(t, p.Publish(context.Background(), nil))
	assert.Equal(t, 0, deliverer.calls)
}
