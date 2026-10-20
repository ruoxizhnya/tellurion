//go:build !integration

package storage

import (
	"os"
	"testing"

	"tellurion/pkg/testenv"
)

// TestMain 只做一件事：包末打印 DB 测试跳过汇总（REV-21）。
//
// 本包 DB 测试全部走 TEST_DATABASE_DSN 契约（pkg/testenv/dbenv.go）：
// 未设置 → 显式跳过并登记，由这里汇总打印；已设置但连不上 → fail-loud。
//
// 为什么带 //go:build !integration：integration_test.go 在 integration tag
// 下有**自己的** TestMain（dockertest 起真库容器），一个测试二进制只能有
// 一个 TestMain；而 integration 模式由 dockertest 保证有库，本来就不需要
// 跳过汇总——两种模式各取所需，不会撞车。
func TestMain(m *testing.M) {
	os.Exit(testenv.RunWithDBSummary(m))
}
