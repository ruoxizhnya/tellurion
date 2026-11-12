//go:build integration
// +build integration

package storage

// CORE-MIGRATION-REPLAY-GUARD 的**行为**用例（真库，dockertest 一次性容器）。
//
// 要证明三件事：
//  1. 迁移半途失败 ⇒ 失败那条所在的事务整体回滚、账本只停在「已提交的前缀」，
//     失败点之后的迁移一条都不跑（不是「跑到一半的半套 schema」）；
//  2. 重启后能判定要不要重放 ⇒ 已应用的前缀**不重放**（用非幂等 DDL 当探针：
//     一旦重放就会报 already exists），只补跑缺的那几条；
//  3. 指纹不符 / 账本领先于代码 ⇒ 拒绝启动，且账本与 schema 都不被改动。
//
// 账本表名用独立名字，避免和 TestMain 里 NewPostgresStore 写出的生产账本
// core_schema_migrations 搅在一起：被测的是语义，不是表名。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// guardLedger 准备一张干净的独立账本表，并在用例结束时连同 scratch 表一起清掉。
func guardLedger(t *testing.T, store *PostgresStore, name string) string {
	t.Helper()

	drop := func(ctx context.Context) {
		_, _ = store.pool.Exec(ctx, `DROP TABLE IF EXISTS `+name)
		for _, tbl := range []string{"guard_t_a", "guard_t_b", "guard_t_c"} {
			_, _ = store.pool.Exec(ctx, `DROP TABLE IF EXISTS `+tbl)
		}
	}
	drop(context.Background())
	t.Cleanup(func() { drop(context.Background()) })
	return name
}

func ledgerRows(t *testing.T, store *PostgresStore, ledger string) map[int]string {
	t.Helper()

	ctx := context.Background()
	rows, err := store.pool.Query(ctx, `SELECT version, checksum FROM `+ledger)
	require.NoError(t, err)
	defer rows.Close()

	applied := map[int]string{}
	for rows.Next() {
		var version int
		var checksum string
		require.NoError(t, rows.Scan(&version, &checksum))
		applied[version] = checksum
	}
	require.NoError(t, rows.Err())
	return applied
}

func ledgerAppliedAt(t *testing.T, store *PostgresStore, ledger string) time.Time {
	t.Helper()

	var latest time.Time
	err := store.pool.QueryRow(context.Background(),
		`SELECT COALESCE(MAX(applied_at), TIMESTAMPTZ '1970-01-01') FROM `+ledger).Scan(&latest)
	require.NoError(t, err)
	return latest
}

func tableExists(t *testing.T, store *PostgresStore, table string) bool {
	t.Helper()

	var oid *string
	err := store.pool.QueryRow(context.Background(),
		`SELECT to_regclass($1)::text`, table).Scan(&oid)
	require.NoError(t, err)
	return oid != nil
}

// halfwayMigrations 是一条「中间夹着坏语句」的迁移序列。前两条刻意不用
// IF NOT EXISTS：这样一旦被重放，Postgres 会直接报 already exists —— 重放
// 本身就在用例的判据里，而不是靠脑子推。
func halfwayMigrations() []string {
	return []string{
		`CREATE TABLE guard_t_a (id INT PRIMARY KEY)`,
		`THIS IS NOT SQL`,
		`CREATE TABLE guard_t_b (id INT PRIMARY KEY)`,
	}
}

func TestIntegration_MigrateGuardHalfwayFailureLeavesLedgerAtAppliedPrefix(t *testing.T) {
	store, ctx := newTestStore(t)
	ledger := guardLedger(t, store, "core_schema_migrations_guard_half")

	err := store.applyMigrations(ctx, ledger, halfwayMigrations())
	require.Error(t, err, "坏语句必须让迁移失败，而不是被吞掉")
	assert.Contains(t, err.Error(), "migration 002", "错误必须点名失败的那一条")
	assert.Contains(t, err.Error(), "事务已回滚")

	// 失败点之前的那条已提交 —— 账本是「连续前缀」，重启可从 2 续跑。
	assert.True(t, tableExists(t, store, "guard_t_a"),
		"失败前已成功的迁移应当保留（它的账本行也在）")
	// 失败点之后的一条根本没跑。
	assert.False(t, tableExists(t, store, "guard_t_b"),
		"失败点之后的迁移不该被执行")

	applied := ledgerRows(t, store, ledger)
	require.Len(t, applied, 1, "账本只能停在已提交的前缀上")
	_, recorded := applied[2]
	assert.False(t, recorded, "失败的那条不许在账本里留下行（事务回滚应带走它）")
}

func TestIntegration_MigrateGuardRestartResumesWithoutReplaying(t *testing.T) {
	store, ctx := newTestStore(t)
	ledger := guardLedger(t, store, "core_schema_migrations_guard_resume")

	require.Error(t, store.applyMigrations(ctx, ledger, halfwayMigrations()))

	// 「改好之后重启」：列表已修复，第一条保持不变。
	// 它是非幂等 DDL —— 若守卫没把它判成「已应用」，这次启动会因
	// 「guard_t_a already exists」而失败。所以本用例的通过本身就是
	// 「重启知道该跳过哪条」的证据。
	fixed := []string{
		`CREATE TABLE guard_t_a (id INT PRIMARY KEY)`,
		`CREATE TABLE guard_t_b (id INT PRIMARY KEY)`,
		`CREATE TABLE guard_t_c (id INT PRIMARY KEY)`,
	}
	require.NoError(t, store.applyMigrations(ctx, ledger, fixed),
		"重启应当只补跑未应用的两条")

	applied := ledgerRows(t, store, ledger)
	require.Len(t, applied, 3)
	for _, version := range []int{1, 2, 3} {
		_, ok := applied[version]
		assert.True(t, ok, "版本 %d 应当已记账", version)
	}
	assert.True(t, tableExists(t, store, "guard_t_c"))

	// 再启动一次：零 pending，账本一行不动（不刷新 applied_at）。
	before := ledgerAppliedAt(t, store, ledger)
	require.NoError(t, store.applyMigrations(ctx, ledger, fixed))
	assert.Equal(t, before, ledgerAppliedAt(t, store, ledger),
		"账本齐了以后重启不该重放，也不该改写账本")
}

func TestIntegration_MigrateGuardRejectsChangedStatement(t *testing.T) {
	store, ctx := newTestStore(t)
	ledger := guardLedger(t, store, "core_schema_migrations_guard_checksum")

	require.NoError(t, store.applyMigrations(ctx, ledger, []string{
		`CREATE TABLE guard_t_a (id INT PRIMARY KEY)`,
	}))
	before := ledgerRows(t, store, ledger)

	// 已应用的第 1 条被后人改了文本（INT → BIGINT）。
	err := store.applyMigrations(ctx, ledger, []string{
		`CREATE TABLE guard_t_a (id BIGINT PRIMARY KEY)`,
	})
	require.Error(t, err, "指纹不符必须拒绝启动")
	assert.Contains(t, err.Error(), "Migration 001")
	assert.Contains(t, err.Error(), "指纹不符")
	assert.Contains(t, err.Error(), "拒绝启动")

	assert.Equal(t, before, ledgerRows(t, store, ledger), "拒绝启动时账本不能被改写")
}

func TestIntegration_MigrateGuardRejectsLedgerAheadOfCode(t *testing.T) {
	store, ctx := newTestStore(t)
	ledger := guardLedger(t, store, "core_schema_migrations_guard_ahead")

	both := []string{
		`CREATE TABLE guard_t_a (id INT PRIMARY KEY)`,
		`CREATE TABLE guard_t_b (id INT PRIMARY KEY)`,
	}
	require.NoError(t, store.applyMigrations(ctx, ledger, both))

	// 代码被回退成只剩一条：账本里的版本 2 代码不认识。
	err := store.applyMigrations(ctx, ledger, both[:1])
	require.Error(t, err, "账本领先于代码必须拒绝启动")
	assert.Contains(t, err.Error(), "账本领先于代码")
	assert.Contains(t, err.Error(), "拒绝启动")

	assert.Len(t, ledgerRows(t, store, ledger), 2, "拒绝启动时账本不能被改写")
}

// TestIntegration_MigrateGuardAdoptsLegacyDatabaseWithoutLedger 覆盖真实升级
// 现场：旧版本 migrate() 不留执行记录，所以新代码第一次启动时「表全在、账本
// 为空」。全部语句幂等，此时必须能重放一遍并完成记账，而不是拒绝启动。
func TestIntegration_MigrateGuardAdoptsLegacyDatabaseWithoutLedger(t *testing.T) {
	store, ctx := newTestStore(t)
	legacyLedger := guardLedger(t, store, "core_schema_migrations_guard_legacy_old")
	newLedger := guardLedger(t, store, "core_schema_migrations_guard_legacy_new")

	migrations := []string{
		`CREATE TABLE IF NOT EXISTS guard_t_a (id INT PRIMARY KEY)`,
		`CREATE INDEX IF NOT EXISTS idx_guard_t_a ON guard_t_a (id)`,
	}
	require.NoError(t, store.applyMigrations(ctx, legacyLedger, migrations))

	// 模拟升级前的库：schema 已在，账本不存在。
	_, err := store.pool.Exec(ctx, `DROP TABLE `+legacyLedger)
	require.NoError(t, err)

	require.NoError(t, store.applyMigrations(ctx, newLedger, migrations),
		"存量库（表已存在、账本缺失）应当能重放幂等 DDL 并被接管")
	assert.Len(t, ledgerRows(t, store, newLedger), len(migrations),
		"接管后账本必须把重放过的条目全部记上")
}

// TestIntegration_MigrateProductionPathRecordsLedger 钉住生产装配：
// migrate() 跑的是 migrationStatements()+索引，并且真的把它们记进了
// core_schema_migrations；第二次启动零重放。
func TestIntegration_MigrateProductionPathRecordsLedger(t *testing.T) {
	store, ctx := newTestStore(t)

	require.NoError(t, store.migrate(ctx))

	steps := buildMigrationSteps(append(migrationStatements(), migrationIndexStatements()...))
	applied := ledgerRows(t, store, migrationLedgerTable)
	require.Len(t, applied, len(steps),
		"生产账本必须覆盖全部迁移（含索引 DDL），而不是只记建表")

	for _, step := range steps {
		assert.Equal(t, step.Checksum, applied[step.Version], "版本 %d 的账本指纹应等于代码指纹", step.Version)
	}

	before := ledgerAppliedAt(t, store, migrationLedgerTable)
	require.NoError(t, store.migrate(ctx))
	assert.Equal(t, before, ledgerAppliedAt(t, store, migrationLedgerTable),
		"第二次启动应当零重放")
}
