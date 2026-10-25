package storage

// QT-42 X3c 的建表护栏：`quant.alert_outbox` 必须走 `migrationStatements()`
// 这条唯一路径（SPEC-R-01），且 DDL 的形状要与代码里的用法一致。
//
// 为什么是静态读源码而不是连库：本仓「测试不许调迁移入口」是有护栏的
// （internal/repoguard/store_migration_isolation_test.go）—— 默认构建里跑一次
// migrate() 就会真的作用在共享开发库上。建表语句对不对，读源码就能判定一半；
// 真库那一半在 alert_outbox_integration_test.go（-tags integration）。

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

// last_error 是 PG 的 TEXT 列：非法 UTF-8 会被服务端直接拒收
// （error 22021 invalid byte sequence），而那一拒发生在「记这次投递失败」的
// 写语句上 —— 于是告警没投出去、attempts 也没记上，运维两头都看不见。
// 两条来源都能造出这种输入：按字节夹断一个汉字，以及把 risk 响应体
// （外部输入）原样折进原因里。
func TestSanitizeOutboxErrorDetail_keepsColumnWritable(t *testing.T) {
	t.Run("夹断不切半个汉字", func(t *testing.T) {
		// "投递失败" = 4 字 × 3 字节 = 12 字节；上限 7 落在第 3 个字中间。
		in := "投递失败"
		got := sanitizeOutboxErrorDetail(in, 7)
		if !utf8.ValidString(got) {
			t.Fatalf("产出非法 UTF-8（%d 字节 %q）—— 这一行会写不进 TEXT", len(got), got)
		}
		if got != "投递" || len(got) != 6 {
			t.Errorf("got %q（%d 字节）want %q（6 字节）—— 夹断必须停在字边界", got, len(got), "投递")
		}
	})

	t.Run("外部响应体里的坏字节被替换而不是整条丢掉", func(t *testing.T) {
		got := sanitizeOutboxErrorDetail("status 502: \xff\xfe upstream", 2000)
		if !utf8.ValidString(got) {
			t.Fatalf("产出非法 UTF-8 %q —— 上游报文可以打死本仓的投递记账", got)
		}
		if !strings.Contains(got, "status 502") || !strings.Contains(got, "upstream") {
			t.Errorf("诊断信息被吞掉了：%q", got)
		}
	})

	t.Run("合法且够短的输入原样返回", func(t *testing.T) {
		for _, in := range []string{"", "ok", "POST http://127.0.0.1:8086/api/risk/events: dial refused"} {
			if got := sanitizeOutboxErrorDetail(in, 2000); got != in {
				t.Errorf("sanitizeOutboxErrorDetail(%q) = %q，不该改写", in, got)
			}
		}
	})

	t.Run("上限为 0 时产出空串而不是 panic", func(t *testing.T) {
		if got := sanitizeOutboxErrorDetail("投递失败", 0); got != "" {
			t.Errorf("got %q want 空串", got)
		}
	})
}

func TestAlertOutbox_DDLIsExecutedByMigratePath(t *testing.T) {
	// 行为断言，不是文本断言：读源码找字符串会被一行注释骗过去
	// （破坏验证实测：把 `migrations = append(...)` 注释掉，字面串仍在注释里，
	// 静态断言照样绿 —— 那种护栏比没护栏更糟）。这里直接取 migrate() 实际
	// 执行的语句清单，逐条核对建表语句在不在里面。
	statements := migrationStatements()

	got := make(map[string]int, len(statements))
	for _, s := range statements {
		got[s]++
	}
	for _, want := range AlertOutboxSchemaDDL {
		if got[want] == 0 {
			t.Errorf("migrationStatements() 里没有这条 DDL —— 新库不会建 quant.alert_outbox：\n%s", want)
		}
	}

	// 反向腿：清单里确有这张表的建表语句（防止常量被改成空切片蒙过去）。
	if len(AlertOutboxSchemaDDL) < 2 {
		t.Fatalf("AlertOutboxSchemaDDL 只有 %d 条语句，测试本身失效", len(AlertOutboxSchemaDDL))
	}
	var created bool
	for _, s := range statements {
		if strings.Contains(s, "CREATE TABLE IF NOT EXISTS quant.alert_outbox") {
			created = true
		}
	}
	if !created {
		t.Error("migrate() 的语句清单里没有 quant.alert_outbox 的 CREATE TABLE")
	}
}

func TestAlertOutbox_DDLMatchesTheQueriesThatReadIt(t *testing.T) {
	src, err := os.ReadFile("alert_outbox.go")
	if err != nil {
		t.Fatalf("读取 alert_outbox.go 失败: %v", err)
	}
	ddl := string(src)

	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS quant.alert_outbox",
		"alert_id     VARCHAR(128) PRIMARY KEY",
		"CHECK (status IN ('pending','sent','failed'))",
		"payload      JSONB        NOT NULL",
		// relay 每轮都是「按先后取 pending 的一批」：没有这个部分索引就全表扫，
		// 而已 sent 的行永远比待发的多。
		"WHERE status = 'pending'",
	} {
		if !strings.Contains(ddl, want) {
			t.Errorf("DDL 缺 %q —— 与代码的用法不再一致（改列要同步改这里的断言）", want)
		}
	}

	// 状态词表：Go 常量与 DDL 的 CHECK 必须同一套词。任一侧改名，另一侧红。
	for _, status := range []string{
		AlertOutboxStatusPending, AlertOutboxStatusSent, AlertOutboxStatusFailed,
	} {
		if !strings.Contains(ddl, "'"+status+"'") {
			t.Errorf("Go 常量 %q 在 DDL 的 CHECK 里找不到", status)
		}
	}
	check := regexp.MustCompile(`CHECK \(status IN \(([^)]*)\)\)`)
	m := check.FindStringSubmatch(ddl)
	if m == nil {
		t.Fatal("找不到 status 的 CHECK —— 测试的正则失效了")
	}
	if got := strings.Count(m[1], "'") / 2; got != 3 {
		t.Errorf("status CHECK 允许 %d 个值，代码只有 3 个（多出来的值无人写 = 死词表）", got)
	}
}

func TestAlertOutbox_JSONTagsMatchRiskEventWire(t *testing.T) {
	// 这张表的列名与 risk 侧 `internal/event.Event` 的 JSON 键刻意保持一致，
	// 是为了 relay 能逐字段搬运而不做名字翻译。改名要两边一起改。
	rec := AlertOutboxRecord{AlertID: "a", RunID: "r", OrderID: "o", EventSeq: 0, Symbol: "600000.SH"}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal 失败: %v", err)
	}
	body := string(raw)
	for _, key := range []string{`"run_id":"r"`, `"order_id":"o"`, `"event_seq":0`, `"symbol":"600000.SH"`} {
		if !strings.Contains(body, key) {
			t.Errorf("AlertOutboxRecord 的 JSON 缺 %s —— 与 risk 事件的字段名漂移了", key)
		}
	}
}
