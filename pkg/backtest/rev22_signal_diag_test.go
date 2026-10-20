package backtest

// REV-22-(3)：信号失败计数的 run 级诊断测试（execAlgoRunDiag 扩展）。

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

// lockedBuffer 是并发安全的 bytes.Buffer。
type rev22LockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *rev22LockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *rev22LockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestREV22_SignalFailuresSummarized(t *testing.T) {
	var buf rev22LockedBuffer
	dg := newExecAlgoRunDiag()

	d1 := time.Date(2024, 1, 5, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2024, 1, 8, 0, 0, 0, 0, time.UTC)
	dg.recordSignalFailure(d1, errors.New("strategy exploded"))
	dg.recordSignalFailure(d2, errors.New("strategy exploded again"))

	dg.logSummary(zerolog.New(&buf))
	out := buf.String()
	assert.Contains(t, out, `"signal_failure_days":2`, "失败天数必须汇总可见")
	assert.Contains(t, out, "2024-01-05", "必须点名首个现场日期")
	assert.Contains(t, out, "Signal failures summary")
}

func TestREV22_SignalFailuresDoNotAffectInvalidReasons(t *testing.T) {
	dg := newExecAlgoRunDiag()
	dg.recordSignalFailure(time.Now(), errors.New("x"))
	assert.Nil(t, dg.invalidReasonCodes(),
		"信号失败不进 invalid_reasons（单日策略故障不否定整段回测）——计数可见即可")
}
