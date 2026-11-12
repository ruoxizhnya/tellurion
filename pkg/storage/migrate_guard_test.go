package storage

// CORE-MIGRATION-REPLAY-GUARD：迁移重放守卫的**纯逻辑**部分。
//
// 缺陷现场：migrate() 原先是「循环执行 migrationStatements()，无事务、无执行
// 记录」。跑到第 N 条失败退出后，下一次启动只能从第一条重来——哪些已生效、
// 哪些没跑到，代码和数据库都不知道。全部语句幂等时这是「白跑一遍」，一旦
// 混入非幂等 DDL/DML 就是半套 schema 反复重跑。
//
// 修法（方案 ②，见 postgres.go 的 migrate() 注释）：每条迁移一个事务 + 一张
// 账本 core_schema_migrations(version, checksum, applied_at)。版本 = 数组下标
// +1，指纹 = 语句文本的 SHA-256。重启时账本说了算：同指纹跳过、指纹不符或
// 账本领先于代码就拒绝启动。
//
// 本文件只测「账本 → 本次该跑哪几条」这步纯函数（无库，默认构建即跑）。
// 真实事务语义（半途失败、重启续跑、指纹不符拒绝启动）在
// migrate_guard_integration_test.go（//go:build integration）。

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// realMigrationSteps 是生产实际会执行的迁移序列（建表 + 索引 + 后续追加），
// 供下面的用例当输入——避免用自造的三条语句测出一个「和线上无关的绿」。
func realMigrationSteps() []migrationStep {
	return buildMigrationSteps(allMigrationStatements())
}

func TestBuildMigrationStepsUseSequentialVersions(t *testing.T) {
	t.Parallel()

	steps := buildMigrationSteps([]string{"CREATE TABLE a (id INT)", "  CREATE TABLE b (id INT)  "})
	require.Len(t, steps, 2)

	assert.Equal(t, 1, steps[0].Version)
	assert.Equal(t, 2, steps[1].Version)

	// 指纹按 TrimSpace 后的文本算：首尾空白不该改变一条迁移的身份，
	// 否则 gofmt 不碰的字符串换行调整就会让存量库拒绝启动。
	assert.Equal(t, steps[0].Checksum, buildMigrationSteps([]string{"  CREATE TABLE a (id INT)\n"})[0].Checksum)

	// 内容不同 ⇒ 指纹不同（否则「文本被改动」这条判据根本不存在）。
	assert.NotEqual(t, steps[0].Checksum, steps[1].Checksum)
	assert.Len(t, steps[0].Checksum, 64, "SHA-256 的 hex 长度")
}

func TestPlanMigrationsAppliesEmptyLedgerInOrder(t *testing.T) {
	t.Parallel()

	steps := realMigrationSteps()
	require.NotEmpty(t, steps, "生产迁移序列不该是空的——空序列会让下面所有判据变成空转")

	pending, err := planMigrations(map[int]string{}, steps)
	require.NoError(t, err)
	require.Len(t, pending, len(steps), "空账本 ⇒ 全部待执行")
	for i := range steps {
		assert.Equal(t, steps[i].Version, pending[i].Version, "必须按代码顺序执行")
	}
}

func TestPlanMigrationsSkipsIdenticalChecksums(t *testing.T) {
	t.Parallel()

	steps := realMigrationSteps()
	applied := map[int]string{}
	for _, step := range steps {
		applied[step.Version] = step.Checksum
	}

	pending, err := planMigrations(applied, steps)
	require.NoError(t, err)
	assert.Empty(t, pending, "账本与代码逐条一致 ⇒ 一条都不重放")
}

func TestPlanMigrationsAppliesOnlyTheTail(t *testing.T) {
	t.Parallel()

	steps := realMigrationSteps()
	applied := map[int]string{}
	for _, step := range steps[:len(steps)-1] {
		applied[step.Version] = step.Checksum
	}

	pending, err := planMigrations(applied, steps)
	require.NoError(t, err)
	require.Len(t, pending, 1, "只该补跑最后一条")
	assert.Equal(t, len(steps), pending[0].Version)
}

func TestPlanMigrationsRejectsEditedStatement(t *testing.T) {
	t.Parallel()

	steps := realMigrationSteps()
	applied := map[int]string{}
	for _, step := range steps[:3] {
		applied[step.Version] = step.Checksum
	}
	// 模拟「已经跑过的那条被后人改了文本」：指纹对不上。
	applied[2] = strings.Repeat("0", 64)

	pending, err := planMigrations(applied, steps)
	require.Error(t, err, "已应用迁移被改写必须拒绝启动，不能继续按错误前提改 schema")
	assert.Nil(t, pending)
	assert.Contains(t, err.Error(), "Migration 002")
	assert.Contains(t, err.Error(), "指纹不符")
	assert.Contains(t, err.Error(), "拒绝启动")
}

func TestPlanMigrationsRejectsLedgerAheadOfCode(t *testing.T) {
	t.Parallel()

	steps := realMigrationSteps()
	applied := map[int]string{}
	for _, step := range steps {
		applied[step.Version] = step.Checksum
	}
	// 模拟「代码被回退到更少的迁移」：库里有代码不认识的版本。
	applied[len(steps)+1] = strings.Repeat("a", 64)

	pending, err := planMigrations(applied, steps)
	require.Error(t, err, "账本领先于代码必须拒绝启动，静默忽略等于假装 schema 已对齐")
	assert.Nil(t, pending)
	assert.Contains(t, err.Error(), "账本领先于代码")
}

func TestPlanMigrationsRejectsZeroOrNegativeVersion(t *testing.T) {
	t.Parallel()

	steps := realMigrationSteps()
	_, err := planMigrations(map[int]string{0: strings.Repeat("a", 64)}, steps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "拒绝启动")
}
