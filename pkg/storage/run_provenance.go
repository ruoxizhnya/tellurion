package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// QT-63 · quant.run_provenance 的读写。
//
// 这张表只存**输入身份**（三份 hash + 生效参数 + 生效口径），**不存 run 状态**：
// status 的唯一权威是 backtest_jobs（O1 后是 run.backtest_jobs），两边写状态迟早
// 各说各话。查状态用 join / 视图（QT-44）。
//
// 落点裁决与迁移债（O1 时由 run.run_artifacts 双写/对拍/切读/停写后取代）见
// docs/TASKS.md QT-63 ③-①。

// ErrRunProvenanceConflict 表示同一个 run_id 被写入了不同的身份。
//
// 这不是「重复插入」而是「这个 id 已经属于另一份输入」—— 静默覆盖会让一份结果
// 的 provenance 指向别人的输入，比没有 provenance 更危险。故 fail-loud，
// 不做 ON CONFLICT DO NOTHING。
var ErrRunProvenanceConflict = errors.New("storage: run_provenance identity conflict")

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

// RunProvenanceSchemaDDL 建表 DDL —— migrate() 的 Migration 056 执行的就是这组语句。
//
// 列长度用 VARCHAR 而不是 CHAR：CHAR(64) 会补空格，空 artifact_hash 读回来是
// 64 个空格，比较时又是另一个值 —— 空白填充在「身份」这种列上没有好处。
var RunProvenanceSchemaDDL = []string{
	`CREATE SCHEMA IF NOT EXISTS quant`,
	`CREATE TABLE IF NOT EXISTS quant.run_provenance (
		run_id                VARCHAR(64)  PRIMARY KEY,
		run_spec_hash         VARCHAR(64)  NOT NULL,
		dataset_snapshot_hash VARCHAR(64)  NOT NULL,
		artifact_hash         VARCHAR(64)  NOT NULL DEFAULT '',
		version_hash          VARCHAR(128) NOT NULL DEFAULT '',
		strategy_ref_input    VARCHAR(256) NOT NULL DEFAULT '',
		params_json           JSONB        NOT NULL DEFAULT '{}'::jsonb,
		params_hash           VARCHAR(64)  NOT NULL,
		seed                  BIGINT       NOT NULL DEFAULT 0,
		kernel                VARCHAR(32)  NOT NULL,
		engine_version        VARCHAR(128) NOT NULL,
		fill_timing           VARCHAR(32)  NOT NULL,
		risk_rules_hash       VARCHAR(64),
		created_at            TIMESTAMPTZ  NOT NULL DEFAULT now()
	)`,
	// 「同参复现」是拿 hash 查的（QT-63 ③-⑥）：没有索引，这条查询就是全表扫。
	`CREATE INDEX IF NOT EXISTS idx_run_provenance_run_spec_hash ON quant.run_provenance (run_spec_hash)`,
}

// SaveRunProvenance 写入一行 provenance。
//
// 幂等边界：同一个 run_id **且四份 hash 全同**重复写入是允许的（进程重试、
// 断点续写都会走到）；任何一份 hash 不同 → ErrRunProvenanceConflict。
// 实现是一条语句内完成判断，避免「先查后写」的竞态窗口：
//
//	ON CONFLICT DO UPDATE ... WHERE 四 hash 全等
//
// WHERE 不成立时 Postgres 不报错、只返回 0 行受影响 —— 那正是身份漂移，必须
// 由调用方转成错误，不能当成功吞掉。
func (s *PostgresStore) SaveRunProvenance(ctx context.Context, rec *RunProvenanceRecord) error {
	if rec == nil {
		return fmt.Errorf("SaveRunProvenance: nil record")
	}
	paramsJSON := rec.ParamsJSON
	if len(paramsJSON) == 0 {
		paramsJSON = []byte("{}")
	}
	if !json.Valid(paramsJSON) {
		return fmt.Errorf("SaveRunProvenance: params_json is not valid JSON")
	}

	const q = `
		INSERT INTO quant.run_provenance (
			run_id, run_spec_hash, dataset_snapshot_hash, artifact_hash, version_hash,
			strategy_ref_input, params_json, params_hash, seed, kernel, engine_version,
			fill_timing, risk_rules_hash
		) VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8,$9,$10,$11,$12,NULLIF($13,''))
		ON CONFLICT (run_id) DO UPDATE SET run_id = EXCLUDED.run_id
		WHERE quant.run_provenance.run_spec_hash         = EXCLUDED.run_spec_hash
		  AND quant.run_provenance.dataset_snapshot_hash = EXCLUDED.dataset_snapshot_hash
		  AND quant.run_provenance.artifact_hash         = EXCLUDED.artifact_hash
		  AND quant.run_provenance.version_hash          = EXCLUDED.version_hash
	`
	tag, err := s.pool.Exec(ctx, q,
		rec.RunID, rec.RunSpecHash, rec.DatasetSnapshotHash, rec.ArtifactHash, rec.VersionHash,
		rec.StrategyRefInput, string(paramsJSON), rec.ParamsHash, rec.Seed, rec.Kernel, rec.EngineVersion,
		rec.FillTiming, rec.RiskRulesHash,
	)
	if err != nil {
		return fmt.Errorf("save run provenance for %s: %w", rec.RunID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: run_id %q already recorded with a different input identity "+
			"(run_spec_hash=%s)", ErrRunProvenanceConflict, rec.RunID, rec.RunSpecHash)
	}
	return nil
}

// GetRunProvenance 读回一行；不存在时返回 (nil, nil)。
func (s *PostgresStore) GetRunProvenance(ctx context.Context, runID string) (*RunProvenanceRecord, error) {
	const q = `
		SELECT run_id, run_spec_hash, dataset_snapshot_hash, artifact_hash, version_hash,
		       strategy_ref_input, params_json, params_hash, seed, kernel, engine_version,
		       fill_timing, COALESCE(risk_rules_hash, ''), created_at
		FROM quant.run_provenance WHERE run_id = $1
	`
	var rec RunProvenanceRecord
	err := s.pool.QueryRow(ctx, q, runID).Scan(
		&rec.RunID, &rec.RunSpecHash, &rec.DatasetSnapshotHash, &rec.ArtifactHash, &rec.VersionHash,
		&rec.StrategyRefInput, &rec.ParamsJSON, &rec.ParamsHash, &rec.Seed, &rec.Kernel,
		&rec.EngineVersion, &rec.FillTiming, &rec.RiskRulesHash, &rec.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get run provenance for %s: %w", runID, err)
	}
	return &rec, nil
}
