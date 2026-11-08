package repoguard

// SVC-CORE-DATA-RETIRE 反向护栏：数据面退役是**不可逆**的。
//
// 本护栏钉两件事：
//
//  1. `pkg/storage/postgres.go` 的 migrate() 内联 DDL 里，21 张已退役的
//     market / ingest 表**零** `CREATE TABLE`。数据面的唯一 owner 是
//     `tellurion-data`；本仓一旦重新建表，就又是双 owner（ADR-036 §6）。
//  2. `pkg/data` 目录不存在。connector / ETL / 因子实现整包迁出后，本仓
//     不得再出现这个包——它的回归会把「core 只经 HTTP 读」重新撕开。
//
// 为什么是静态断言而不是行为测试：表是外部仓建的，本仓测试连不上那条
// schema 路径时「没有这张表」什么也不证明；而 `CREATE TABLE` 是纯文本事实，
// 静态检查在 CI 无库环境同样有效。
//
// 正向对照（防止护栏因「文件被删空」而假绿）：同一份 DDL 里必须仍能看到
// `market.index_daily` 的 CREATE TABLE —— 第三轮裁决 ⑰ 定它 owner = core，
// 是 22 名里唯一被保留的一张。若哪天 postgres.go 被整块删空，这条先红。
//
// 破坏验证（2026-11-08 施工时实做）：往 postgres.go 的 migrations 里加回
// 一行 `CREATE TABLE IF NOT EXISTS stocks (...)` ⇒ 本护栏红；逐字节还原后
// 复绿。同型验证 `pkg/data` 断言：临时建空目录 `pkg/data` ⇒ 红，删除后复绿。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// retiredDataPlaneTables 是 SVC-CORE-DATA-RETIRE 退役的 21 张表
// （材料 §4.1 的 22 名减去 `market.index_daily`，见任务单 ④ 例外）。
var retiredDataPlaneTables = []string{
	"stocks",
	"ohlcv_daily_qfq",
	"trading_calendar",
	"dividends",
	"index_constituents",
	"splits",
	"realtime_quote",
	"ohlcv_minute",
	"capital_flow",
	"sectors",
	"stock_sector_map",
	"top_list",
	"limit_up_pool",
	"announcements",
	"news",
	"hot_search",
	"global_ohlcv",
	"market_activity",
	"ingest.raw",
	"sync_jobs",
	"sync_schedules",
}

// TestRetiredDataPlaneTablesHaveNoInlineDDL 断言 21 张退役表在 migrate()
// 的内联 DDL 里零 CREATE TABLE，且保留表 market.index_daily 仍在。
func TestRetiredDataPlaneTablesHaveNoInlineDDL(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	src := stripSQLLineComments(readFile(t, filepath.Join(root, "pkg", "storage", "postgres.go")))

	for _, table := range retiredDataPlaneTables {
		pattern := regexp.MustCompile(
			`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(?:market\.)?` + regexp.QuoteMeta(table) + `\b`)
		if loc := pattern.FindStringIndex(src); loc != nil {
			line := 1 + strings.Count(src[:loc[0]], "\n")
			t.Errorf("pkg/storage/postgres.go:%d 重新建了已退役的表 %s —— "+
				"数据面唯一 owner 是 tellurion-data；core 不得再持有该表 DDL（SVC-CORE-DATA-RETIRE）",
				line, table)
		}
	}

	// 正向对照：保留表必须还在，否则本护栏可能因「DDL 被整块删空」而假绿。
	kept := regexp.MustCompile(`(?i)CREATE\s+TABLE\s+IF\s+NOT\s+EXISTS\s+market\.index_daily\b`)
	if !kept.MatchString(src) {
		t.Error("pkg/storage/postgres.go 里找不到 market.index_daily 的 CREATE TABLE —— " +
			"裁决 ⑰ 定它 owner = core，必须保留；若确已改判，请同批订正裁决记录与 retiredDataPlaneTables")
	}
}

// TestDataPlanePackageDirectoryIsGone 断言 pkg/data 与 cmd/data 两条目录
// 路径不存在。
func TestDataPlanePackageDirectoryIsGone(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	for _, rel := range []string{
		filepath.Join("pkg", "data"),
		filepath.Join("cmd", "data"),
	} {
		abs := filepath.Join(root, rel)
		if _, err := os.Stat(abs); err == nil {
			t.Errorf("%s 目录仍存在 —— 数据面实现（connector / ETL / 同步腿）已迁出本仓；"+
				"该目录回归会把「core 只经 HTTP 读」重新撕开（SVC-CORE-DATA-RETIRE）", rel)
		} else if !os.IsNotExist(err) {
			t.Errorf("无法判定 %s 是否存在：%v", rel, err)
		}
	}
}

// stripSQLLineComments 去掉 SQL 的 `--` 行注释。注释里提表名是文档，不是
// 建表；不剥掉的话 postgres.go 里「Migration 056 已退役」这类说明会把护栏
// 逼成假红（与 canonical_layer_test.go 的注释豁免同一口径）。
func stripSQLLineComments(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}
