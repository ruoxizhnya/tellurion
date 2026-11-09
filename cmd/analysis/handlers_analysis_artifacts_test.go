package main

// QT-78：R2 读面的 200 / 404 / 500 / 503 四条边界。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/internal/analysisartifact"
	"tellurion/pkg/storage"
)

type fakeArtifactReader struct {
	rec *storage.AnalysisArtifactRecord
	err error
}

func (f fakeArtifactReader) GetAnalysisArtifact(ctx context.Context, runID, kind string) (*storage.AnalysisArtifactRecord, error) {
	return f.rec, f.err
}

func artifactRouter(reader analysisArtifactReader) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerAnalysisArtifactRoutes(r, reader, zerolog.Nop())
	return r
}

func doArtifactGET(r *gin.Engine, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestAnalysisArtifactRoute_ReturnsPayloadVerbatim(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"holdings":[{"symbol":"A","sector":"tech","weight":1,"period_return":0.1}],"benchmark_sector_weights":{},"benchmark_sector_returns":{},"benchmark_return":0,"dataset_hash":"ds-1"}`)
	r := artifactRouter(fakeArtifactReader{rec: &storage.AnalysisArtifactRecord{
		RunID: "run-1", Kind: analysisartifact.KindPortfolioSnapshot, Payload: payload,
	}})

	w := doArtifactGET(r, "/api/runs/run-1/portfolio-snapshot")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, payload, w.Body.Bytes())
	assert.Contains(t, w.Header().Get("Content-Type"), "application/json")
}

func TestAnalysisArtifactRoute_BothKindsRegistered(t *testing.T) {
	t.Parallel()

	r := artifactRouter(fakeArtifactReader{rec: &storage.AnalysisArtifactRecord{Payload: []byte(`{}`)}})
	for _, path := range []string{
		"/api/runs/run-1/portfolio-snapshot",
		"/api/runs/run-1/factor-exposures",
	} {
		w := doArtifactGET(r, path)
		assert.Equal(t, http.StatusOK, w.Code, "%s: %s", path, w.Body.String())
	}
}

func TestAnalysisArtifactRoute_NotFound(t *testing.T) {
	t.Parallel()

	r := artifactRouter(fakeArtifactReader{
		err: errors.Join(storage.ErrAnalysisArtifactNotFound, errors.New("run-1")),
	})
	w := doArtifactGET(r, "/api/runs/run-1/factor-exposures")
	assert.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
}

func TestAnalysisArtifactRoute_StoreErrorIs500(t *testing.T) {
	t.Parallel()

	r := artifactRouter(fakeArtifactReader{err: errors.New("pool closed")})
	w := doArtifactGET(r, "/api/runs/run-1/portfolio-snapshot")
	assert.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), "pool closed", "5xx 不把内部原因吐给调用方")
}

func TestAnalysisArtifactRoute_UnwiredIs503(t *testing.T) {
	t.Parallel()

	r := artifactRouter(nil)
	w := doArtifactGET(r, "/api/runs/run-1/portfolio-snapshot")
	assert.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
}
