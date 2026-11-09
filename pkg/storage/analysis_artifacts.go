package storage

// QT-78 · quant.run_analysis_artifacts 的读写。
//
// 这张表存**回测终态的分析产物载荷**（portfolio-snapshot / factor-exposures），
// 供 analytics 的 Brinson / 因子归因读取。它与 orchestration 的
// `run.run_artifacts` 不是同一张表、也不是同一类数据：
//   - run.run_artifacts 只存一次 run 的三 hash 身份（不复制内容，主键 run_id）；
//   - 本表按 (run_id, kind) 存两类载荷本体，主键允许一次 run 两行。
//
// 不变量：载荷一旦落库不可覆盖——artifact_hash 是内容身份，同 key 不同 hash
// 只能是「同一个 run 产出了不同内容」，静默覆盖等于把两次产出并成一次。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	// ErrAnalysisArtifactNotFound 表示该 run 的该类产物尚未产出（消费侧应据此 fail-closed）。
	ErrAnalysisArtifactNotFound = errors.New("storage: analysis artifact not found")
	// ErrAnalysisArtifactConflict 表示同一 (run_id, kind) 已记录过另一份内容身份。
	ErrAnalysisArtifactConflict = errors.New("storage: analysis artifact identity conflict")
)

// AnalysisArtifactRecord 是 quant.run_analysis_artifacts 的一行。
type AnalysisArtifactRecord struct {
	RunID        string          `json:"run_id"`
	Kind         string          `json:"kind"`
	DatasetHash  string          `json:"dataset_hash"`
	ArtifactHash string          `json:"artifact_hash"`
	Payload      json.RawMessage `json:"payload"`
	CreatedAt    time.Time       `json:"created_at,omitempty"`
}

// AnalysisArtifactsSchemaDDL 建表 DDL —— migrate() 的 Migration 062 执行的就是这组语句。
var AnalysisArtifactsSchemaDDL = []string{
	`CREATE SCHEMA IF NOT EXISTS quant`,
	`CREATE TABLE IF NOT EXISTS quant.run_analysis_artifacts (
		run_id        VARCHAR(64)  NOT NULL,
		kind          VARCHAR(32)  NOT NULL,
		dataset_hash  VARCHAR(64)  NOT NULL,
		artifact_hash VARCHAR(64)  NOT NULL,
		payload       JSONB        NOT NULL,
		created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
		PRIMARY KEY (run_id, kind)
	)`,
}

// SaveAnalysisArtifact 写入一行产物载荷。
//
// 幂等边界：同一 (run_id, kind) 且 artifact_hash 相同 → 返回原行（worker 重试
// 会重复写一次）；artifact_hash 不同 → ErrAnalysisArtifactConflict。
// 判断与写入在一条语句内完成，避免「先查后写」的竞态窗口。
func (s *PostgresStore) SaveAnalysisArtifact(ctx context.Context, rec *AnalysisArtifactRecord) (*AnalysisArtifactRecord, error) {
	if rec == nil {
		return nil, fmt.Errorf("SaveAnalysisArtifact: nil record")
	}
	if rec.RunID == "" || rec.Kind == "" {
		return nil, fmt.Errorf("SaveAnalysisArtifact: run_id and kind are required")
	}
	payload := rec.Payload
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}
	if !json.Valid(payload) {
		return nil, fmt.Errorf("SaveAnalysisArtifact: payload is not valid JSON")
	}

	const q = `
		INSERT INTO quant.run_analysis_artifacts (run_id, kind, dataset_hash, artifact_hash, payload)
		VALUES ($1,$2,$3,$4,$5::jsonb)
		ON CONFLICT (run_id, kind) DO UPDATE SET run_id = EXCLUDED.run_id
		WHERE quant.run_analysis_artifacts.artifact_hash = EXCLUDED.artifact_hash
		RETURNING run_id, kind, dataset_hash, artifact_hash, payload, created_at
	`
	var out AnalysisArtifactRecord
	err := s.pool.QueryRow(ctx, q,
		rec.RunID, rec.Kind, rec.DatasetHash, rec.ArtifactHash, string(payload),
	).Scan(&out.RunID, &out.Kind, &out.DatasetHash, &out.ArtifactHash, &out.Payload, &out.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: run_id %q kind %q already recorded with a different artifact_hash",
				ErrAnalysisArtifactConflict, rec.RunID, rec.Kind)
		}
		return nil, fmt.Errorf("save analysis artifact for %s/%s: %w", rec.RunID, rec.Kind, err)
	}
	return &out, nil
}

// GetAnalysisArtifact 读回一行；不存在时返回 ErrAnalysisArtifactNotFound。
func (s *PostgresStore) GetAnalysisArtifact(ctx context.Context, runID, kind string) (*AnalysisArtifactRecord, error) {
	const q = `
		SELECT run_id, kind, dataset_hash, artifact_hash, payload, created_at
		FROM quant.run_analysis_artifacts WHERE run_id = $1 AND kind = $2
	`
	var rec AnalysisArtifactRecord
	err := s.pool.QueryRow(ctx, q, runID, kind).Scan(
		&rec.RunID, &rec.Kind, &rec.DatasetHash, &rec.ArtifactHash, &rec.Payload, &rec.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: run_id %q kind %q", ErrAnalysisArtifactNotFound, runID, kind)
		}
		return nil, fmt.Errorf("get analysis artifact for %s/%s: %w", runID, kind, err)
	}
	return &rec, nil
}
