package storage

// QT-72 · X0 三 hash 对外读面（core 侧）。
//
// 一次读 `quant.run_provenance ⋈ run.backtest_results`：输入身份（三 hash /
// 生效值）来自留痕表，**结论轴（result_status）只从结果面取**。留痕表没有
// status 列，也不许把某个 hash 列当结论用 —— 那会让「跑完但无效」与「根本
// 没跑完」在对外响应里混成一个值。
//
// SVC-ORCH-G3 收尾腿（2026-11-09）：core 的 quant.backtest_jobs 退役，驱动表
// 从 backtest_jobs 换成留痕表；结论轴改读 run.backtest_results（Migration 063
// 建的结果面，worker 与同步端点写入的 K9 三值）。
//
// 可空性：驱动表无行 → (nil, nil)（handler 出 404 job_not_found）；无留痕行
// 时 Provenance == nil；无结果行时 ResultStatus 为空串（handler 出 409
// provenance_not_ready）。

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// BacktestProvenance 是「输入身份 + 结论」的一次读取结果。
type BacktestProvenance struct {
	// ResultStatus 是结果面 run.backtest_results.result_status（K9 三值）；
	// 空串 = 尚无结果行（run 未收尾）。
	ResultStatus string
	Provenance   *RunProvenanceRecord
}

// provenanceResultStatusExpr 是结论轴的唯一表达式。
//
// 破坏验证的锚点：把它换成 provenance 的任意 hash 列（例如 r.run_spec_hash），
// TestQT72_ProvenanceResultStatusComesFromResultFace 必须红。
const provenanceResultStatusExpr = "COALESCE(res.result_status, '')"

const backtestProvenanceSelectSQL = `
	SELECT ` + provenanceResultStatusExpr + `,
	       r.run_id,
	       COALESCE(r.run_spec_hash, ''),
	       COALESCE(r.dataset_snapshot_hash, ''),
	       COALESCE(r.artifact_hash, ''),
	       COALESCE(r.version_hash, ''),
	       COALESCE(r.strategy_ref_input, ''),
	       COALESCE(r.params_json, '{}'::jsonb),
	       COALESCE(r.params_hash, ''),
	       COALESCE(r.seed, 0),
	       COALESCE(r.kernel, ''),
	       COALESCE(r.engine_version, ''),
	       COALESCE(r.fill_timing, ''),
	       COALESCE(r.risk_rules_hash, ''),
	       r.created_at
	FROM quant.run_provenance r
	LEFT JOIN run.backtest_results res ON res.run_id = r.run_id
	WHERE r.run_id = $1
`

// GetBacktestJobProvenance 读一条 run 的「输入身份 + 结论」。
// run 无留痕也无结果 → (nil, nil)；有留痕但无结果 → ResultStatus 为空串。
func (s *PostgresStore) GetBacktestJobProvenance(ctx context.Context, runID string) (*BacktestProvenance, error) {
	var (
		out       BacktestProvenance
		runIDp    *string
		rec       RunProvenanceRecord
		createdAt *time.Time
	)
	err := s.pool.QueryRow(ctx, backtestProvenanceSelectSQL, runID).Scan(
		&out.ResultStatus,
		&runIDp,
		&rec.RunSpecHash, &rec.DatasetSnapshotHash, &rec.ArtifactHash, &rec.VersionHash,
		&rec.StrategyRefInput, &rec.ParamsJSON, &rec.ParamsHash, &rec.Seed,
		&rec.Kernel, &rec.EngineVersion, &rec.FillTiming, &rec.RiskRulesHash,
		&createdAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get backtest provenance for %s: %w", runID, err)
	}
	if runIDp != nil {
		rec.RunID = *runIDp
		if createdAt != nil {
			rec.CreatedAt = *createdAt
		}
		out.Provenance = &rec
	}
	return &out, nil
}
