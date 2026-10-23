//go:build !integration

// QT-16 Migration 058：quant.strategy_state 旧形状 → 新形状的迁移测试。
//
// 真库上不能把 quant.strategy_state 改回旧形状再迁（写腿红线：改真库结构
// 不可逆且会动到别人的行）。所以把 StrategyStateSchemaDDL 的每条语句做
// schema 名替换（quant → 临时 schema），先造**旧形状**表、再跑全套迁移
// 语句、断言新形状 + 幂等。这样测到的是**真实的迁移语句文本**，不是复制品。
package storage

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/testenv"
)

// legacyStrategyStateDDL 是 QT-16 之前的旧形状（Migration 055 原文，PK 二元组、
// 无信封列）——迁移测试的起点。
const legacyStrategyStateDDL = `CREATE TABLE IF NOT EXISTS %s.strategy_state (
		strategy_id TEXT NOT NULL,
		run_id      TEXT NOT NULL,
		ts          TIMESTAMPTZ NOT NULL,
		state       BYTEA NOT NULL,
		created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY (strategy_id, run_id)
	)`

// pkColumns 返回指定表的 PK 列集（按位置序）。
func pkColumns(t *testing.T, pool *pgxpool.Pool, qualifiedTable string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT a.attname
		FROM pg_index i
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
		WHERE i.indrelid = $1::regclass AND i.indisprimary
		ORDER BY a.attnum`, qualifiedTable)
	require.NoError(t, err)
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		require.NoError(t, rows.Scan(&c))
		cols = append(cols, c)
	}
	require.NoError(t, rows.Err())
	return cols
}

func TestStrategyStateMigrationFromLegacyShape(t *testing.T) {
	ctx := context.Background()
	pool := testenv.OpenPoolForTest(t)

	// 临时 schema：隔离在 quant 之外，测试完整个 DROP——真库零残留。
	schema := "qt16mig_" + uuid.NewString()[:8]
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = pool.Exec(cctx, fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE`, schema))
	})

	// ① 旧形状起点（先建 schema：迁移语句里的 CREATE SCHEMA 要到 ② 才跑）。
	_, err := pool.Exec(ctx, fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS %s`, schema))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, fmt.Sprintf(legacyStrategyStateDDL, schema))
	require.NoError(t, err)
	require.Equal(t, []string{"strategy_id", "run_id"}, pkColumns(t, pool, schema+".strategy_state"),
		"前置：起点应是旧 PK 二元组")

	// 旧行一条（迁移后应平滑拿到 shard_key='' + 空信封哨兵）。
	_, err = pool.Exec(ctx, fmt.Sprintf(
		`INSERT INTO %s.strategy_state (strategy_id, run_id, ts, state) VALUES ('s-old', 'r-old', now(), '\x01')`, schema))
	require.NoError(t, err)

	// ② 全套迁移语句（真实文本，schema 名替换），随后新形状断言。
	runAll := func() {
		for i, stmt := range StrategyStateSchemaDDL {
			replaced := strings.ReplaceAll(stmt, "quant", schema)
			_, err := pool.Exec(ctx, replaced)
			require.NoErrorf(t, err, "迁移语句第 %d 条失败: %s", i+1, replaced)
		}
	}
	runAll()

	require.Equal(t, []string{"strategy_id", "run_id", "shard_key"}, pkColumns(t, pool, schema+".strategy_state"),
		"迁移后 PK 必须是三元组")
	for _, col := range []string{"shard_key", "state_schema_version", "artifact_hash", "abi_version", "run_identity_hash"} {
		var n int
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT count(*) FROM information_schema.columns WHERE table_schema=$1 AND table_name='strategy_state' AND column_name=$2`,
			schema, col).Scan(&n), "查询列 %s 是否存在失败", col)
		require.Equal(t, 1, n, "迁移后缺列 %s", col)
	}

	// ③ 旧行平滑迁移：shard_key='' 兜底、信封空串哨兵，数据未丢。
	var shardKey, schemaVer string
	require.NoError(t, pool.QueryRow(ctx,
		fmt.Sprintf(`SELECT shard_key, state_schema_version FROM %s.strategy_state WHERE strategy_id='s-old'`, schema)).
		Scan(&shardKey, &schemaVer))
	require.Empty(t, shardKey, "旧行应落到 market 分片（shard_key=''）")
	require.Empty(t, schemaVer, "旧行信封应是空串哨兵（恢复时被点名拒绝，不是被解释）")

	// ④ 幂等：整套语句再跑一遍必须原样通过。
	runAll()

	// ⑤ 新形状行可正常插入（PK 三元组生效：同 (strategy, run) 两个分片并存）。
	_, err = pool.Exec(ctx, fmt.Sprintf(
		`INSERT INTO %s.strategy_state (strategy_id, run_id, shard_key, ts, state) VALUES ('s-new', 'r-new', '600000.SH', now(), '\x01')`, schema))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, fmt.Sprintf(
		`INSERT INTO %s.strategy_state (strategy_id, run_id, shard_key, ts, state) VALUES ('s-new', 'r-new', '', now(), '\x02')`, schema))
	require.NoError(t, err)
}
