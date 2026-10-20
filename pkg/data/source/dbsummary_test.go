package source

import (
	"os"
	"testing"

	"tellurion/pkg/testenv"
)

// TestMain 只做一件事：包末打印 DB 测试跳过汇总（REV-21）。
//
// 本包 DB 测试全部走 TEST_DATABASE_DSN 契约（pkg/testenv/dbenv.go）：
// 未设置 → 显式跳过并登记，由这里汇总打印；已设置但连不上 → fail-loud。
func TestMain(m *testing.M) {
	os.Exit(testenv.RunWithDBSummary(m))
}
