package main

import (
	"os"
	"testing"

	"github.com/gin-gonic/gin"

	"tellurion/pkg/testenv"
)

// TestMain sets gin to test mode once for the entire package.
//
// AUD-28 (ODR-065): individual tests and helpers must NOT call
// gin.SetMode() themselves. gin stores its mode in two package-level
// globals (ginMode / modeName) behind a plain assignment — not an atomic
// one — and RouterGroup.handle reads them on every route registration
// (handle → debugPrintRoute → IsDebugging). A call from one test
// goroutine therefore races with another test's route registration.
// Centralising the call in TestMain runs it exactly once, before any test
// goroutine starts. Same fix as AUD-12 (cmd/analysis/main_test.go,
// pkg/auth/middleware_test.go).
//
// REV-21: m.Run 包一层 testenv.RunWithDBSummary —— 包末打印 DB 测试跳过
// 汇总（TEST_DATABASE_DSN 未设置时的显式跳过清单），不再静默。
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(testenv.RunWithDBSummary(m))
}
