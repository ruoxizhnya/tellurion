package storage

// QT-72 破坏验证的可执行锚点（无库）：X0 读面的**结论轴**只能来自
// backtest_jobs，不能来自 quant.run_provenance 的任意 hash 列。
//
// 为什么放在 SQL 常量上而不是 handler 上：handler 拿到的就是一个已经取好的
// ResultStatus 字段；把来源改坏的入口在这条 SELECT 里。CI 的无库环境也要
// 能咬住它，所以这里对表达式本身做结构化断言（值域/列名），而不是跑真库。

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQT72_ProvenanceResultStatusComesFromJobs(t *testing.T) {
	t.Parallel()

	expr := provenanceResultStatusExpr
	require.Contains(t, expr, "b.result_status",
		"结论轴必须读 backtest_jobs.result_status（QT-63 单一同源）")

	// 反向：表达式里不得出现 provenance 一侧的别名 r.（任意 hash 列都属于 r）。
	assert.False(t, strings.Contains(expr, "r."),
		"结论轴不得来自 quant.run_provenance 的任何列：%q", expr)
	for _, col := range []string{"run_spec_hash", "dataset_snapshot_hash", "artifact_hash", "version_hash"} {
		assert.NotContains(t, expr, col,
			"把 hash 列 %s 当结论用会让 invalid 被读成 failed：%q", col, expr)
	}
}

func TestQT72_ProvenanceSelectJoinsBothTables(t *testing.T) {
	t.Parallel()

	q := backtestProvenanceSelectSQL
	assert.Contains(t, q, "FROM backtest_jobs b", "结论轴的宿主表")
	assert.Contains(t, q, "LEFT JOIN quant.run_provenance r ON r.run_id = b.id",
		"留痕按 run_id 对齐；LEFT JOIN 让「有 job 无留痕」仍可判定三态")
	assert.Contains(t, q, "WHERE b.id = $1")
	// 只读：这条路径不允许写 run_artifacts / backtest_jobs（core 不双写）。
	lower := strings.ToLower(q)
	for _, verb := range []string{"insert ", "update ", "delete "} {
		assert.NotContains(t, lower, verb, "X0 读面必须是纯读：%q", verb)
	}
}
