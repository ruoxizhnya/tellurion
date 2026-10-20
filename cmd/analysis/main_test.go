package main

import (
	"os"
	"testing"

	"github.com/gin-gonic/gin"

	"tellurion/pkg/testenv"
)

// TestMain sets gin to test mode exactly once for the whole package.
//
// AUD-12: individual tests must NOT call gin.SetMode() themselves. gin
// keeps the mode in two package-level globals (ginMode and modeName,
// written by plain assignment — see gin's mode.go), and most of the
// handler tests here call t.Parallel(). Two parallel tests calling
// gin.SetMode() therefore race with each other, and also with gin's own
// IsDebugging() read performed while a *different* parallel test
// registers routes (RouterGroup.handle → debugPrintRoute → IsDebugging).
//
// The symptom was 17-18 "WARNING: DATA RACE" reports and 16 failed tests
// in this package under `go test -race`, all pointing at gin's globals.
// Nothing in production code was involved: the only writers were test
// helpers.
//
// This is the same fix already applied in pkg/auth (S7-P0-14 / ODR-043);
// the precedent just had not been propagated to this package. Doing it in
// TestMain runs the write before any test goroutine exists, so there is
// nothing left to race with.
//
// REV-21: m.Run 包一层 testenv.RunWithDBSummary —— 包末打印 DB 测试跳过
// 汇总（TEST_DATABASE_DSN 未设置时的显式跳过清单），不再静默。
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(testenv.RunWithDBSummary(m))
}
