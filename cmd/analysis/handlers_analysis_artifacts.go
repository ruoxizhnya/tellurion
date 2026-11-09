package main

// QT-78 · R2 分析产物读面（core 自持）。
//
// analytics 的 ArtifactReader 按 `{ANALYTICS_ARTIFACT_URL}/runs/{id}/<kind>`
// 取期末持仓与因子暴露。这两个载荷由 core 的 worker 在 run 终态时写入
// `quant.run_analysis_artifacts`（pkg/storage/analysis_artifacts.go），所以
// 读面也留在 core：
//   - orchestration 的 `run.run_artifacts` 只存三 hash 身份、不做对象存储
//     （其非目标），没有 R2 读面；
//   - 载荷形状由 internal/analysisartifact 定义，与 analytics §6.2 逐字一致。
//
// 语义（消费侧 analytics 的 ArtifactReader）：
//   - 200：原样返回落库的 payload 字节；
//   - 404：该 run 的该类产物尚未产出 → 消费侧 fail-closed 成
//     `run_result_incomplete`（IncompleteError）；
//   - 503：读面端口未接线（未接库），不是数据问题；
//   - 500：读库失败。

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-kit/httpserver"

	"tellurion/internal/analysisartifact"
	"tellurion/pkg/storage"
)

// analysisArtifactReader 是 R2 读面的最小端口。
// 生产实现是 *storage.PostgresStore；测试注入替身。
type analysisArtifactReader interface {
	GetAnalysisArtifact(ctx context.Context, runID, kind string) (*storage.AnalysisArtifactRecord, error)
}

// registerAnalysisArtifactRoutes 挂两条 R2 读端点。
func registerAnalysisArtifactRoutes(router *gin.Engine, reader analysisArtifactReader, logger zerolog.Logger) {
	api := router.Group("/api/runs")
	api.GET("/:id/"+analysisartifact.KindPortfolioSnapshot,
		analysisArtifactHandler(reader, analysisartifact.KindPortfolioSnapshot, logger))
	api.GET("/:id/"+analysisartifact.KindFactorExposures,
		analysisArtifactHandler(reader, analysisartifact.KindFactorExposures, logger))
}

// analysisArtifactHandler 读一类产物并按「原样 payload」回写。
func analysisArtifactHandler(reader analysisArtifactReader, kind string, logger zerolog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		if reader == nil {
			// 装配缺失（不是数据问题）：未接库时路由照常注册、请求时 503。
			httpserver.Fail(c, http.StatusServiceUnavailable, "analysis artifact reader is not wired")
			return
		}
		runID := c.Param("id")
		rec, err := reader.GetAnalysisArtifact(c.Request.Context(), runID, kind)
		switch {
		case errors.Is(err, storage.ErrAnalysisArtifactNotFound):
			httpserver.Failf(c, http.StatusNotFound,
				"analysis artifact %q for run %q has not been produced", kind, runID)
			return
		case err != nil:
			logger.Error().Err(err).Str("run_id", runID).Str("kind", kind).Msg("Failed to read analysis artifact")
			httpserver.FailCause(c, http.StatusInternalServerError, "failed to read analysis artifact", err)
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", rec.Payload)
	}
}
