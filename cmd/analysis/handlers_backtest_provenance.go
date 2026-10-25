package main

// QT-72 · X0 三 hash 对外读面（裁-19 定稿）。
//
// `GET /api/backtest/{id}/provenance` 读 `quant.run_provenance ⋈ backtest_jobs`：
// hash / 指针 / 生效值来自留痕表，**status / result_status 只从 backtest_jobs 取**
// （QT-63 ④ 单一同源 —— 留痕表没有 status 列）。
//
// 三态沿用 orchestration `provenanceView` 的三个 wire code（裁-⑬）：
//   - 非终态                → 409 `provenance_not_ready`
//   - 终态 failed/cancelled → 404 `provenance_not_found`
//   - 终态 completed/invalid→ 500 `provenance_missing`（数据完整性破坏）
// `invalid` 是**结论**不是失败，缺留痕同样是 500，不能读成 404。
// 本端点**不引入** `producer_pending`：那是 orchestration 侧 worker 投影的
// 提示位，core 读自己的表，不依赖那个进程。
//
// 可空列显式出 `null`，不省略键（读侧要能区分「没有」与「未提供」）：
//   - `artifact_uri`：core 的留痕表没有这一列（指针归 worker 投影），恒 null；
//   - `artifact_hash` / `version_hash`：库内空串 = 无独立字节码，折成 null；
//   - `result_status`：非终态为 null；
//   - `seed` / `created_at`：有留痕行就一定有值（列 NOT NULL）。

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"tellurion/pkg/httpserver"

	"tellurion/pkg/backtest"
	"tellurion/pkg/storage"
)

// backtestProvenanceReader 是 X0 读面的最小端口。
// 生产实现是 *storage.PostgresStore；测试注入替身。
type backtestProvenanceReader interface {
	GetBacktestJobProvenance(ctx context.Context, runID string) (*storage.BacktestJobProvenance, error)
}

// backtestProvenanceView 是 `ProvenanceResponse` 的 core 侧 DTO。
//
// **独立 DTO，不扩冻结的 BacktestResponse**。字段名 / 可空性 / 顺序与
// tellurion-orchestration `internal/httpapi/provenance.go` 的 `provenanceView`
// 逐字对齐（15 个键）——两侧读的是同一个概念，wire 形状不能各说各话。
// 跟它对齐的测试见 handlers_backtest_provenance_test.go。
type backtestProvenanceView struct {
	RunID               string     `json:"run_id"`
	RunSpecHash         string     `json:"run_spec_hash"`
	DatasetSnapshotHash string     `json:"dataset_snapshot_hash"`
	RiskRulesHash       string     `json:"risk_rules_hash"`
	StrategyRef         string     `json:"strategy_ref"`
	ArtifactURI         *string    `json:"artifact_uri"`
	ArtifactHash        *string    `json:"artifact_hash"`
	VersionHash         *string    `json:"version_hash"`
	ParamsHash          string     `json:"params_hash"`
	Seed                *int64     `json:"seed"`
	Kernel              string     `json:"kernel"`
	EngineVersion       string     `json:"engine_version"`
	FillTiming          string     `json:"fill_timing"`
	ResultStatus        *string    `json:"result_status"`
	CreatedAt           *time.Time `json:"created_at"`
}

// registerBacktestProvenanceRoute 挂 `GET /api/backtest/:id/provenance`。
func registerBacktestProvenanceRoute(api *gin.RouterGroup, reader backtestProvenanceReader, logger zerolog.Logger) {
	api.GET("/:id/provenance", func(c *gin.Context) {
		if reader == nil {
			// 装配缺失（不是数据问题）：读面端口没接线，无法回答。
			httpserver.Fail(c, http.StatusServiceUnavailable, "provenance reader is not wired")
			return
		}
		runID := c.Param("id")
		row, err := reader.GetBacktestJobProvenance(c.Request.Context(), runID)
		if err != nil {
			logger.Error().Err(err).Str("backtest_id", runID).Msg("Failed to read run provenance")
			httpserver.FailCause(c, http.StatusInternalServerError, "failed to read run provenance", err)
			return
		}
		if row == nil {
			writeProvenanceWireError(c, http.StatusNotFound, "job_not_found", "backtest job not found")
			return
		}
		if row.Provenance == nil {
			writeProvenanceAbsent(c, row, runID, logger)
			return
		}
		c.JSON(http.StatusOK, provenanceViewOf(row))
	})
}

// provenanceViewOf 把「留痕 ⋈ job」折成对外 DTO。
func provenanceViewOf(row *storage.BacktestJobProvenance) backtestProvenanceView {
	p := row.Provenance
	seed := p.Seed
	createdAt := p.CreatedAt
	return backtestProvenanceView{
		RunID:               p.RunID,
		RunSpecHash:         p.RunSpecHash,
		DatasetSnapshotHash: p.DatasetSnapshotHash,
		RiskRulesHash:       p.RiskRulesHash,
		StrategyRef:         p.StrategyRefInput,
		// core 的留痕表没有 artifact URI 列：显式 null，不是省略键。
		ArtifactURI:   nil,
		ArtifactHash:  optProvenanceString(p.ArtifactHash),
		VersionHash:   optProvenanceString(p.VersionHash),
		ParamsHash:    p.ParamsHash,
		Seed:          &seed,
		Kernel:        p.Kernel,
		EngineVersion: p.EngineVersion,
		FillTiming:    p.FillTiming,
		ResultStatus:  optProvenanceString(row.ResultStatus),
		CreatedAt:     &createdAt,
	}
}

// writeProvenanceAbsent 按 job 的两轴决定「没有留痕」是哪种语义。
//
// 判据落在 result_status（K9 结论）而不是只落在 status：status=completed 时
// result_status 可能是 invalid —— 两者都要求有留痕。invalid 是正常结论，
// 不是失败，所以它缺留痕同样是完整性破坏（500），不能读成 failed 的 404。
func writeProvenanceAbsent(c *gin.Context, row *storage.BacktestJobProvenance, runID string, logger zerolog.Logger) {
	if !isTerminalBacktestStatus(row.JobStatus) {
		writeProvenanceWireError(c, http.StatusConflict,
			"provenance_not_ready", "run is not terminal; provenance is written when it finishes")
		return
	}
	if row.ResultStatus == backtest.StatusCompleted ||
		row.ResultStatus == backtest.StatusInvalid ||
		row.JobStatus == "completed" {
		logger.Error().
			Str("backtest_id", runID).
			Str("status", row.JobStatus).
			Str("result_status", row.ResultStatus).
			Msg("terminal run has no provenance row")
		writeProvenanceWireError(c, http.StatusInternalServerError,
			"provenance_missing", "terminal run has no provenance row")
		return
	}
	writeProvenanceWireError(c, http.StatusNotFound,
		"provenance_not_found", "run never produced provenance")
}

// isTerminalBacktestStatus 与 orchestration 的 `Terminal()` 同口径。
// core 的 job 生命周期目前只写 pending / running / completed / failed；
// cancelled 留在这里是为了与两侧的终态集合逐字一致。
func isTerminalBacktestStatus(status string) bool {
	switch status {
	case "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

// writeProvenanceWireError 直接写三态 wire code。
//
// 不走 httpserver.Fail：它按状态码派生 code（409 → "conflict"），而这条端点
// 的对外契约是**具名**的三态码（与 orchestration 逐字一致）。
func writeProvenanceWireError(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": message, "code": code})
}

// optProvenanceString 把「可选文本列」折成 JSON null / 字符串。
func optProvenanceString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
