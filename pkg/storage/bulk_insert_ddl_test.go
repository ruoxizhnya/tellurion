package storage

import (
	"os"
	"regexp"
	"testing"
)

// SVC-CORE-DATA-RETIRE（2026-11-08）：本用例原身是
// TestTableMapper_TablesDeclaredInInlineDDL —— 断言 ETL 的 TableMapper 写入
// 目标 ⊆ 内联 DDL。TableMapper/BulkInsert 与多源数据面一起退役后，
// 「写入目标」这一侧已不存在（写点归数据服务仓），因此这里只保留原来的
// 第二段判据：**代码直接 SQL 引用的表**必须仍在内联 DDL 里声明。
//
// 数据面退役同时把 sync_jobs / sync_schedules 从清单里移除（同步队列随
// cmd/data 一并删除）；factor_genes / strategy_genes / market.index_daily
// 仍由本仓持有建表 DDL（前两张是 agent 仓的基因池，AI 线尚未拆走 DDL；
// 后者是指数日线的 core owner，裁决 ⑰）。
//
// DDL 的唯一真相是 postgres.go 里的 migrate() 数组，所以「被代码直接引用的表
// ⊆ 内联 DDL」必须是一条断言，而不是一句约定 —— 约定没有执法者就必然漂移。
//
// 这个测试刻意**零 DB 依赖**：静态读源码就够了，CI 上不需要起 Postgres。
func TestCodeReferencedTablesDeclaredInInlineDDL(t *testing.T) {
	src, err := os.ReadFile("postgres.go")
	if err != nil {
		t.Fatalf("读取 postgres.go 失败: %v", err)
	}

	re := regexp.MustCompile(`(?i)CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+([a-zA-Z_][a-zA-Z0-9_.]*)`)
	declared := make(map[string]struct{})
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		declared[m[1]] = struct{}{}
	}
	if len(declared) == 0 {
		t.Fatalf("正则没匹配到任何 CREATE TABLE —— 测试本身失效，检查 postgres.go 是否还在原位")
	}

	// 代码直接 SQL 引用、但**不由 ETL 写入**的表。
	codeReferenced := map[string]string{
		"factor_genes":   "tellurion-agent:pkg/ai/gene_pool/factor_pool.go（因子基因池；AI 已拆仓，core 只保留建表 DDL）",
		"strategy_genes": "tellurion-agent:pkg/ai/gene_pool/strategy_pool.go（策略基因池；同上）",
		// DATA-12：指数日线表由 pkg/storage/index_daily.go 直接 SQL 读写
		// （LatestIndexBars 已在 2026-11-08 随 ohlcv.go 退役，剩
		// SaveIndexDailyBatch / GetIndexDailyRange）。
		"market.index_daily": "pkg/storage/index_daily.go（指数日线读写）",
	}
	for table, where := range codeReferenced {
		if _, ok := declared[table]; !ok {
			t.Errorf("表 %q 未在内联 DDL 中声明 —— %s 会在新环境 relation does not exist",
				table, where)
		}
	}
}
