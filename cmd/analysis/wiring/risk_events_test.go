package wiring

// QT-42 X3c core 腿的单测：告警 → outbox → risk 的投递状态机。
//
// 本文件回答四件事：
//  1. `Send` 只落一行，不当场发 HTTP（「不允许丢」的形态要求）；
//  2. 请求体是不是 risk 认的形状（信封只允许 `events` 一键，条目键集逐字对齐
//     `internal/event.Event`，`verdict="alert"`）；
//  3. 失败会不会静默或丢：5xx / 207 / 标记失败都要响亮，行必须还是 pending
//     或被转成 failed 终态，而不是消失；
//  4. 开关关了是不是真的没接（risk 侧空集是设定，不是坏配置伪装成好配置）。
//
// outbox 用内存替身（`fakeAlertOutbox`）——它按 DDL 的语义实现状态机
// （pending→sent、attempts 触顶转 failed、failed 不再被 relay 取到），
// 所以第 3 条测的是行为而不是「有没有调对方法名」。真库腿在
// `pkg/storage/alert_outbox_integration_test.go`。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/alert"
	"tellurion/pkg/storage"
)

// ─── outbox 替身 ────────────────────────────────────────────────────────

type fakeAlertOutbox struct {
	mu              sync.Mutex
	byID            map[string]*storage.AlertOutboxRecord
	order           []string
	saveErr         error
	listErr         error
	markErr         error
	bumpErr         error
	markReturnsZero bool // 模拟「主键对不上 / 行已被别的进程删掉」：n=0 且无 error
	saves           int
}

func newFakeAlertOutbox() *fakeAlertOutbox {
	return &fakeAlertOutbox{byID: make(map[string]*storage.AlertOutboxRecord)}
}

// SaveAlertOutbox 的 bool = 「这一条是不是新行」，与 PG 的 RowsAffected 同义。
func (f *fakeAlertOutbox) SaveAlertOutbox(_ context.Context, rec *storage.AlertOutboxRecord) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saves++
	if f.saveErr != nil {
		return false, f.saveErr
	}
	if _, dup := f.byID[rec.AlertID]; dup {
		return false, nil // 与 PG 的 ON CONFLICT DO NOTHING 同义
	}
	clone := *rec
	clone.Status = storage.AlertOutboxStatusPending
	f.byID[rec.AlertID] = &clone
	f.order = append(f.order, rec.AlertID)
	return true, nil
}

// mustSaveQT42 把端口的 (bool, error) 收进用例：绝大多数用例只关心「落成了」，
// 那个 bool 单独由 TestRiskEventSink_DuplicateAlertID… 钉住。
func mustSaveQT42(t *testing.T, ob *fakeAlertOutbox, rec *storage.AlertOutboxRecord) bool {
	t.Helper()
	inserted, err := ob.SaveAlertOutbox(context.Background(), rec)
	require.NoError(t, err)
	return inserted
}

func (f *fakeAlertOutbox) ListPendingAlertOutbox(_ context.Context, limit int) ([]storage.AlertOutboxRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []storage.AlertOutboxRecord
	for _, id := range f.order {
		row := f.byID[id]
		if row.Status != storage.AlertOutboxStatusPending {
			continue // failed / sent 都不该再占 relay 的批次
		}
		if len(out) >= limit {
			break
		}
		out = append(out, *row)
	}
	return out, nil
}

func (f *fakeAlertOutbox) MarkAlertOutboxSent(_ context.Context, ids []string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.markErr != nil {
		return 0, f.markErr
	}
	if f.markReturnsZero {
		return 0, nil // 与「传进去的键一行都不存在」同形：没有 error，只有 n=0
	}
	n := 0
	for _, id := range ids {
		if row, ok := f.byID[id]; ok && row.Status != storage.AlertOutboxStatusSent {
			row.Status = storage.AlertOutboxStatusSent
			row.UpdatedAt = time.Now()
			n++
		}
	}
	return n, nil
}

func (f *fakeAlertOutbox) BumpAlertOutboxAttempts(_ context.Context, ids []string, reason string, maxAttempts int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.bumpErr != nil {
		return f.bumpErr
	}
	for _, id := range ids {
		row, ok := f.byID[id]
		if !ok || row.Status != storage.AlertOutboxStatusPending {
			continue
		}
		row.Attempts++
		row.LastError = reason
		if row.Attempts >= maxAttempts {
			row.Status = storage.AlertOutboxStatusFailed
		}
	}
	return nil
}

func (f *fakeAlertOutbox) statusOf(t *testing.T, id string) (storage.AlertOutboxRecord, bool) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.byID[id]
	if !ok {
		return storage.AlertOutboxRecord{}, false
	}
	return *row, true
}

func (f *fakeAlertOutbox) mustRow(t *testing.T, id string) storage.AlertOutboxRecord {
	t.Helper()
	row, ok := f.statusOf(t, id)
	require.True(t, ok, "outbox 里没有 %s", id)
	return row
}

func (f *fakeAlertOutbox) counts() (rows int, saves int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.byID), f.saves
}

// ─── HTTP 替身 ──────────────────────────────────────────────────────────

type qt42Server struct {
	mu        sync.Mutex
	calls     int
	bodies    []string
	paths     []string
	auths     []string
	types     []string
	delivered chan string
	responder func(call int) (int, string)
	release   chan struct{}
}

func newQT42Server(t *testing.T, responder func(call int) (int, string)) (*qt42Server, *httptest.Server) {
	t.Helper()
	s := &qt42Server{delivered: make(chan string, 8), responder: responder}
	ts := httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(ts.Close)
	return s, ts
}

func (s *qt42Server) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	call := s.calls
	s.calls++
	s.bodies = append(s.bodies, string(body))
	s.paths = append(s.paths, r.URL.Path)
	s.auths = append(s.auths, r.Header.Get("Authorization"))
	s.types = append(s.types, r.Header.Get("Content-Type"))
	s.mu.Unlock()

	select {
	case s.delivered <- string(body):
	default:
	}
	if s.release != nil {
		select {
		case <-s.release:
		case <-r.Context().Done():
		}
	}
	status, resp := s.responder(call)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(resp))
}

func (s *qt42Server) waitBody(t *testing.T) string {
	t.Helper()
	select {
	case body := <-s.delivered:
		return body
	case <-time.After(5 * time.Second):
		t.Fatal("risk events: no request reached the server within 5s")
		return ""
	}
}

func (s *qt42Server) counts() (calls int, paths, auths, types []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, append([]string(nil), s.paths...), append([]string(nil), s.auths...),
		append([]string(nil), s.types...)
}

// ─── 夹具 ───────────────────────────────────────────────────────────────

// qt42Stamp 是测试告警的固定时间戳：ts 必须原样进事件，不许被墙上时钟顶掉。
var qt42Stamp = time.Date(2026, 10, 25, 3, 4, 5, 0, time.UTC)

func qt42Alert(id, rule string, sev alert.Severity) alert.Alert {
	return alert.Alert{
		ID: id, Rule: rule, Severity: sev, Message: rule + " breached",
		Value: 0.21, Threshold: 0.20, Symbol: "600000.SH", Timestamp: qt42Stamp,
	}
}

func qt42SeedRow(id string) *storage.AlertOutboxRecord {
	return &storage.AlertOutboxRecord{
		AlertID: id, RunID: RiskAlertProducerScope, OrderID: id, EventSeq: 0,
		Symbol: "600000.SH", Reason: "drawdown", EventTs: qt42Stamp,
		Payload: []byte(`{"id":"` + id + `","rule":"drawdown","severity":"critical","message":"dd"}`),
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// newQT42Sink 用足够长的 relay 间隔构造 sink：测试自己按决定性的时机投。
func newQT42Sink(t *testing.T, baseURL string, outbox AlertOutbox, cfg RiskEventSinkConfig, logger zerolog.Logger) *RiskEventSink {
	t.Helper()
	if cfg.RelayInterval == 0 {
		cfg.RelayInterval = time.Hour
	}
	sink, err := NewRiskEventSink(baseURL, cfg, outbox, logger)
	require.NoError(t, err)
	t.Cleanup(sink.Close)
	return sink
}

// ─── Send：只落一行，不发 HTTP ──────────────────────────────────────────

func TestRiskEventSink_SendRecordsOutboxRowWithoutHTTP(t *testing.T) {
	srv, ts := newQT42Server(t, func(int) (int, string) { return http.StatusOK, `{"accepted":1,"duplicates":0}` })
	outbox := newFakeAlertOutbox()
	sink := newQT42Sink(t, ts.URL, outbox, RiskEventSinkConfig{}, zerolog.Nop())

	sink.Send(context.Background(), qt42Alert("ALR-drawdown-1-1", "drawdown", alert.SeverityCritical))

	rows, saves := outbox.counts()
	assert.Equal(t, 1, rows, "告警必须先落 outbox —— 这是「允许延迟、不允许丢」的形态")
	assert.Equal(t, 1, saves)
	calls, _, _, _ := srv.counts()
	assert.Zero(t, calls, "Send 不发 HTTP：请求路径不该被出站调用钉住")

	row := outbox.mustRow(t, "ALR-drawdown-1-1")
	assert.Equal(t, storage.AlertOutboxStatusPending, row.Status)
	assert.Equal(t, RiskAlertProducerScope, row.RunID, "run_id = 生产者作用域，不是一次真实 run")
	assert.Equal(t, "ALR-drawdown-1-1", row.OrderID, "order_id = Alert.ID（risk 去重键第二段）")
	assert.EqualValues(t, 0, row.EventSeq)
	assert.Equal(t, "drawdown", row.Reason)
	assert.Equal(t, qt42Stamp, row.EventTs, "ts 取告警时间，不是投递时刻")
	assert.Contains(t, string(row.Payload), `"severity":"critical"`)
}

func TestRiskEventSink_SendIgnoresCancelledCallerContext(t *testing.T) {
	// `alert.Channel` 的一般约定是尊重入参 ctx。这条路径上 ctx 来自
	// `POST /api/alerts/force-check` 的请求上下文 —— 客户端挂断不许让一条
	// 已经触发的告警消失。偏离约定就写在测试名里，别让它悄悄退回去。
	srv, ts := newQT42Server(t, func(int) (int, string) { return http.StatusOK, `{}` })
	outbox := newFakeAlertOutbox()
	sink := newQT42Sink(t, ts.URL, outbox, RiskEventSinkConfig{}, zerolog.Nop())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sink.Send(ctx, qt42Alert("ALR-cancelled-1", "drawdown", alert.SeverityWarning))

	row := outbox.mustRow(t, "ALR-cancelled-1")
	assert.Equal(t, storage.AlertOutboxStatusPending, row.Status)
	calls, _, _, _ := srv.counts()
	assert.Zero(t, calls)
}

func TestRiskEventSink_UnserializableAlertIsNotRecorded(t *testing.T) {
	// NaN 是量化链路真会产出的值，json.Marshal 对它报错。
	// 处置 = 当场报错 + 不落一行残缺的：本仓不接受「缺失落成一个看起来合理的值」。
	srv, ts := newQT42Server(t, func(int) (int, string) { return http.StatusOK, `{}` })
	outbox := newFakeAlertOutbox()
	buf := &lockedBuffer{}
	sink := newQT42Sink(t, ts.URL, outbox, RiskEventSinkConfig{}, zerolog.New(buf))

	a := qt42Alert("ALR-nan-1", "drawdown", alert.SeverityCritical)
	a.Attributes = map[string]any{"drawdown": math.NaN()}
	sink.Send(context.Background(), a)

	rows, _ := outbox.counts()
	assert.Zero(t, rows, "编不出合法 payload 就不该落一行残的")
	calls, _, _, _ := srv.counts()
	assert.Zero(t, calls)
	text := buf.String()
	assert.Contains(t, text, "not serializable")
	assert.Contains(t, text, "ALR-nan-1")
}

func TestRiskEventSink_OutboxSaveFailureIsLoud(t *testing.T) {
	srv, ts := newQT42Server(t, func(int) (int, string) { return http.StatusOK, `{}` })
	outbox := newFakeAlertOutbox()
	outbox.saveErr = fmt.Errorf("dial tcp: connection refused")
	buf := &lockedBuffer{}
	sink := newQT42Sink(t, ts.URL, outbox, RiskEventSinkConfig{}, zerolog.New(buf))

	sink.Send(context.Background(), qt42Alert("ALR-nosave-1", "drawdown", alert.SeverityCritical))

	// Channel.Send 没有 error 返回，所以「没落库」只能靠日志说得清楚。
	require.Eventually(t, func() bool {
		text := buf.String()
		return strings.Contains(text, "NOT recorded in outbox") && strings.Contains(text, "ALR-nosave-1")
	}, 2*time.Second, 10*time.Millisecond)
	calls, _, _, _ := srv.counts()
	assert.Zero(t, calls)
}

func TestRiskEventSink_DuplicateAlertIDSaysSoInsteadOfDroppingSilently(t *testing.T) {
	// `Alert.ID` = `ALR-<rule>-<epoch 秒>-<seq>`（pkg/alert/manager.go:362），而 seq 随
	// 进程归零 ⇒ 同一秒内重启（崩溃循环恰好就是这么发生的）能造出两条 id 相同、
	// 内容不同的告警。outbox 的主键 + `ON CONFLICT DO NOTHING` 让第二条连一行都不留；
	// 若那一行已经是 failed，这条告警就到不了 risk —— 而「不允许丢」正是这张表
	// 存在的理由。id 本身改不得（它是 risk 侧的去重坐标），所以本条钉的是：
	// **撞键必须响亮**，运维对着日志能找到那条没有新行的告警。
	_, ts := newQT42Server(t, func(int) (int, string) { return http.StatusOK, `{"accepted":1,"duplicates":0}` })
	outbox := newFakeAlertOutbox()
	buf := &lockedBuffer{}
	sink := newQT42Sink(t, ts.URL, outbox, RiskEventSinkConfig{}, zerolog.New(buf))

	sink.Send(context.Background(), qt42Alert("ALR-collide-1", "drawdown", alert.SeverityInfo))
	sink.Send(context.Background(), qt42Alert("ALR-collide-1", "stale", alert.SeverityCritical))

	rows, _ := outbox.counts()
	assert.Equal(t, 1, rows, "撞键不该多出第二行（DO NOTHING 的既定语义）")
	require.Eventually(t, func() bool {
		text := buf.String()
		return strings.Contains(text, "already in the outbox") && strings.Contains(text, "ALR-collide-1")
	}, 2*time.Second, 10*time.Millisecond, "撞键静默 = 这条告警没了而没人知道")
}

// ─── relay：形状与时机 ──────────────────────────────────────────────────

func TestRiskEventSink_RelayPostsPendingRowsWithRiskWireShape(t *testing.T) {
	srv, ts := newQT42Server(t, func(int) (int, string) { return http.StatusOK, `{"accepted":2,"duplicates":0}` })
	outbox := newFakeAlertOutbox()
	// 两行是「上一个进程留下的 pending」形态：没被取走之前它们就躺在表里，
	// 下一拍 relay（这里显式调，图确定性）把它们一批发出去。
	mustSaveQT42(t, outbox, qt42SeedRow("ALR-1"))
	row2 := qt42SeedRow("ALR-2")
	row2.Symbol = ""
	row2.Payload = []byte(`{"id":"ALR-2","rule":"risk_metric_breach","severity":"warning","message":"vol"}`)
	mustSaveQT42(t, outbox, row2)

	// 不手工 relay 的话，上一进程留下的行由节拍带走；这里用显式 relay 拿确定性。
	sink := newQT42Sink(t, ts.URL, outbox, RiskEventSinkConfig{BatchSize: 50}, zerolog.Nop())
	sink.relay()
	body := srv.waitBody(t)

	// 外层：risk 对信封用 DisallowUnknownFields ⇒ 只许有 events 一个键。
	var envelope map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(body), &envelope))
	assert.Equal(t, []string{"events"}, sortedKeys(envelope), "信封只允许 events 一个键")

	var items []map[string]any
	require.NoError(t, json.Unmarshal(envelope["events"], &items))
	require.Len(t, items, 2)

	// 条目键集逐字对齐 risk `internal/event.Event`（omitempty 决定省掉哪些）。
	assert.Equal(t,
		[]string{"event_seq", "order_id", "payload", "reason", "run_id", "symbol", "ts", "verdict"},
		sortedKeys(items[0]), "带 symbol 的行应给出这 8 个键")
	assert.Equal(t,
		[]string{"event_seq", "order_id", "payload", "reason", "run_id", "ts", "verdict"},
		sortedKeys(items[1]), "无 symbol 的行不该编出一个空 symbol 键")

	assert.Equal(t, RiskAlertProducerScope, items[0]["run_id"])
	assert.Equal(t, "ALR-1", items[0]["order_id"])
	assert.EqualValues(t, 0, items[0]["event_seq"])
	assert.Equal(t, "alert", items[0]["verdict"], "risk 的 monitor/alerts 只投影 verdict=alert")
	assert.Equal(t, "drawdown", items[0]["reason"])
	assert.Equal(t, "2026-10-25T03:04:05Z", items[0]["ts"])

	calls, paths, _, types := srv.counts()
	assert.Equal(t, 1, calls)
	assert.Equal(t, []string{"/api/risk/events"}, paths)
	assert.Equal(t, []string{"application/json"}, types)

	// 被接受 ⇒ 行必须转 sent（否则下一轮永久重发同一批）。
	require.Eventually(t, func() bool {
		r1 := outbox.mustRow(t, "ALR-1")
		r2 := outbox.mustRow(t, "ALR-2")
		return r1.Status == storage.AlertOutboxStatusSent && r2.Status == storage.AlertOutboxStatusSent
	}, 2*time.Second, 10*time.Millisecond)
}

func TestRiskEventSink_PayloadCarriesTheFieldsRiskProjects(t *testing.T) {
	srv, ts := newQT42Server(t, func(int) (int, string) { return http.StatusOK, `{"accepted":1,"duplicates":0}` })
	outbox := newFakeAlertOutbox()
	sink := newQT42Sink(t, ts.URL, outbox, RiskEventSinkConfig{BatchSize: 1}, zerolog.Nop())

	a := qt42Alert("ALR-max_position_weight-1-1", "max_position_weight", alert.SeverityWarning)
	a.Attributes = map[string]any{"market_value": 123.0}
	sink.Send(context.Background(), a)
	sink.relay() // 攒批靠 relay，不靠 HTTP 侧的等待

	var envelope struct {
		Events []struct {
			Payload map[string]any `json:"payload"`
		} `json:"events"`
	}
	require.NoError(t, json.Unmarshal([]byte(srv.waitBody(t)), &envelope))
	require.Len(t, envelope.Events, 1)
	got := envelope.Events[0].Payload
	// risk 的 projectAlert 要这三样，缺任一个即计入 invalid_events（不是兜默认值）。
	assert.Equal(t, "max_position_weight", got["rule"])
	assert.Equal(t, "warning", got["severity"], "Severity 词表与 risk 的 info/warning/critical 同源")
	assert.Equal(t, "max_position_weight breached", got["message"])
	assert.Equal(t, "ALR-max_position_weight-1-1", got["id"])
	assert.Equal(t, 0.21, got["value"])
	assert.Equal(t, 0.20, got["threshold"])
	assert.Contains(t, got, "attributes")
}

func TestRiskEventSink_RelayRunsOnInterval(t *testing.T) {
	srv, ts := newQT42Server(t, func(int) (int, string) { return http.StatusOK, `{"accepted":1,"duplicates":0}` })
	outbox := newFakeAlertOutbox()
	sink := newQT42Sink(t, ts.URL, outbox, RiskEventSinkConfig{RelayInterval: 20 * time.Millisecond}, zerolog.Nop())

	sink.Send(context.Background(), qt42Alert("ALR-tick-1", "drawdown", alert.SeverityInfo))
	_ = srv.waitBody(t) // 不手工 relay：到点必须自己发
}

func TestRiskEventSink_CloseRelaysRemainingRows(t *testing.T) {
	srv, ts := newQT42Server(t, func(int) (int, string) { return http.StatusOK, `{"accepted":1,"duplicates":0}` })
	outbox := newFakeAlertOutbox()
	sink := newQT42Sink(t, ts.URL, outbox, RiskEventSinkConfig{RelayInterval: time.Hour}, zerolog.Nop())

	sink.Send(context.Background(), qt42Alert("ALR-last-1", "drawdown", alert.SeverityInfo))
	sink.Close()

	calls, _, _, _ := srv.counts()
	assert.Equal(t, 1, calls, "Close 前必须再做一轮：没发出去的行不该因为停机而躺着")
}

func TestRiskEventSink_ReadFailureKeepsRowsAndIsLoud(t *testing.T) {
	srv, ts := newQT42Server(t, func(int) (int, string) { return http.StatusOK, `{}` })
	outbox := newFakeAlertOutbox()
	outbox.listErr = fmt.Errorf("read-only transaction") // 设在读之前：模拟库读失败
	mustSaveQT42(t, outbox, qt42SeedRow("ALR-listfail-1"))
	buf := &lockedBuffer{}
	sink := newQT42Sink(t, ts.URL, outbox, RiskEventSinkConfig{}, zerolog.New(buf))

	sink.relay()

	require.Eventually(t, func() bool {
		return strings.Contains(buf.String(), "could not read the outbox")
	}, 2*time.Second, 10*time.Millisecond)
	calls, _, _, _ := srv.counts()
	assert.Zero(t, calls)
	row := outbox.mustRow(t, "ALR-listfail-1")
	assert.Equal(t, storage.AlertOutboxStatusPending, row.Status, "读不到不等于发掉了")
}

// ─── 失败要响亮，且不许丢 ───────────────────────────────────────────────

func TestRiskEventSink_ServerErrorKeepsPendingAndReSends(t *testing.T) {
	srv, ts := newQT42Server(t, func(call int) (int, string) {
		if call == 0 {
			return http.StatusInternalServerError, `{"error":"store unavailable","code":"internal"}`
		}
		return http.StatusOK, `{"accepted":1,"duplicates":0}`
	})
	outbox := newFakeAlertOutbox()
	buf := &lockedBuffer{}
	sink := newQT42Sink(t, ts.URL, outbox, RiskEventSinkConfig{}, zerolog.New(buf))
	// 播种放在构造之后：构造会起第一腿 relay，与下面的手工 relay 撞车就会
	// 两条并发（生产上单进程单 sink 不会，测试里必须自己排掉这个噪声）。
	mustSaveQT42(t, outbox, qt42SeedRow("ALR-500-1"))

	sink.relay() // 第一次：500
	require.Eventually(t, func() bool {
		text := buf.String()
		return strings.Contains(text, "batch rejected") && strings.Contains(text, "internal")
	}, 2*time.Second, 10*time.Millisecond, "5xx 要报错点名原因")

	row := outbox.mustRow(t, "ALR-500-1")
	assert.Equal(t, storage.AlertOutboxStatusPending, row.Status, "500 之后行必须还是 pending —— 这就是「不丢」")
	assert.Equal(t, 1, row.Attempts)
	assert.Contains(t, row.LastError, "status 500")

	sink.relay() // 第二次：成功
	_ = srv.waitBody(t)
	calls, _, _, _ := srv.counts()
	assert.Equal(t, 2, calls, "失败的批必须被下一轮重发")
	require.Eventually(t, func() bool {
		return outbox.mustRow(t, "ALR-500-1").Status == storage.AlertOutboxStatusSent
	}, 2*time.Second, 10*time.Millisecond)
}

func TestRiskEventSink_MaxAttemptsTurnsRowFailedNotGone(t *testing.T) {
	srv, ts := newQT42Server(t, func(int) (int, string) {
		return http.StatusServiceUnavailable, `{"error":"down","code":"unavailable"}`
	})
	outbox := newFakeAlertOutbox()
	sink := newQT42Sink(t, ts.URL, outbox, RiskEventSinkConfig{MaxAttempts: 2}, zerolog.Nop())
	mustSaveQT42(t, outbox, qt42SeedRow("ALR-dead-1"))

	sink.relay()
	assert.Equal(t, storage.AlertOutboxStatusPending, outbox.mustRow(t, "ALR-dead-1").Status)

	sink.relay() // 第二次触顶
	row := outbox.mustRow(t, "ALR-dead-1")
	assert.Equal(t, storage.AlertOutboxStatusFailed, row.Status, "触顶转终态 failed")
	assert.Equal(t, 2, row.Attempts)

	// 终态不是删除：行还在表里（可查），但不再占 relay 的批次。
	pending, err := outbox.ListPendingAlertOutbox(context.Background(), 10)
	require.NoError(t, err)
	assert.Len(t, pending, 0, "failed 的行不该再被 relay 取")
	calls, _, _, _ := srv.counts()
	assert.Equal(t, 2, calls)
}

func TestRiskEventSink_PartialFailureNamesEachRejectedAlert(t *testing.T) {
	srv, ts := newQT42Server(t, func(int) (int, string) {
		return http.StatusMultiStatus, `{"accepted":1,"duplicates":0,` +
			`"failed":[{"index":0,"code":"invalid_params","message":"run_id is required"}],` +
			`"code":"partial_failure"}`
	})
	outbox := newFakeAlertOutbox()
	buf := &lockedBuffer{}
	sink := newQT42Sink(t, ts.URL, outbox, RiskEventSinkConfig{}, zerolog.New(buf))
	mustSaveQT42(t, outbox, qt42SeedRow("ALR-rejected"))
	mustSaveQT42(t, outbox, qt42SeedRow("ALR-accepted"))

	sink.relay()
	body := srv.waitBody(t)
	assert.Contains(t, body, "ALR-rejected")

	// 207 必须点名是哪条、为什么 —— 不许把「有告警没落库」折成一个总数。
	require.Eventually(t, func() bool {
		text := buf.String()
		return strings.Contains(text, "ALR-rejected") &&
			strings.Contains(text, "invalid_params") &&
			strings.Contains(text, "partial delivery")
	}, 2*time.Second, 10*time.Millisecond)

	assert.Equal(t, storage.AlertOutboxStatusSent, outbox.mustRow(t, "ALR-accepted").Status)
	rejected := outbox.mustRow(t, "ALR-rejected")
	assert.Equal(t, storage.AlertOutboxStatusPending, rejected.Status, "被逐条拒绝的那条仍要重试")
	assert.Equal(t, 1, rejected.Attempts)
}

func TestRiskEventSink_DuplicatesWarnButStillCountAsSent(t *testing.T) {
	// 去重键撞车（重启后同秒同规则同序号）在 risk 侧计成 duplicates：
	// 数据已经在 risk 里 ⇒ 本地转 sent 是对的，但必须 Warn 可见。
	srv, ts := newQT42Server(t, func(int) (int, string) { return http.StatusOK, `{"accepted":0,"duplicates":1}` })
	outbox := newFakeAlertOutbox()
	mustSaveQT42(t, outbox, qt42SeedRow("ALR-dup-1"))
	buf := &lockedBuffer{}
	sink := newQT42Sink(t, ts.URL, outbox, RiskEventSinkConfig{}, zerolog.New(buf))

	sink.relay()
	_ = srv.waitBody(t)
	require.Eventually(t, func() bool {
		text := buf.String()
		return strings.Contains(text, `"level":"warn"`) && strings.Contains(text, "duplicates")
	}, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, storage.AlertOutboxStatusSent, outbox.mustRow(t, "ALR-dup-1").Status)
}

func TestRiskEventSink_MarkSentFailureIsLoud(t *testing.T) {
	// risk 已收、本地还是 pending ⇒ 下一轮重发同一批。有去重键兜着不会双写，
	// 但状态机与事实不一致必须是 Error，不能悄悄过。
	srv, ts := newQT42Server(t, func(int) (int, string) { return http.StatusOK, `{"accepted":1,"duplicates":0}` })
	outbox := newFakeAlertOutbox()
	buf := &lockedBuffer{}
	sink := newQT42Sink(t, ts.URL, outbox, RiskEventSinkConfig{}, zerolog.New(buf))
	outbox.markErr = fmt.Errorf("connection reset") // 构造后、播种后设：避开第一腿 relay
	mustSaveQT42(t, outbox, qt42SeedRow("ALR-markfail-1"))

	sink.relay()
	_ = srv.waitBody(t)

	require.Eventually(t, func() bool {
		text := buf.String()
		return strings.Contains(text, "outbox NOT marked sent") && strings.Contains(text, "re-send")
	}, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, storage.AlertOutboxStatusPending, outbox.mustRow(t, "ALR-markfail-1").Status)
}

func TestRiskEventSink_MarksSentByOutboxKeyNotRiskOrderID(t *testing.T) {
	// `alert_id`（outbox 主键）与 `order_id`（risk 的去重坐标）今天是同一个字符串
	// （都取 `Alert.ID`），所以「哪几条被接受了」按哪个回写看不见区别。但它们是两个
	// 概念：按 order_id 回写，一旦两者分开就标到 0 行 —— 于是这一批每轮重发，
	// 而日志已经在说「delivered」。这条用例把两者写成不同的串，把那个坑钉住。
	srv, ts := newQT42Server(t, func(int) (int, string) { return http.StatusOK, `{"accepted":1,"duplicates":0}` })
	outbox := newFakeAlertOutbox()
	mustSaveQT42(t, outbox, &storage.AlertOutboxRecord{
		AlertID: "OBX-key-1", RunID: RiskAlertProducerScope, OrderID: "RISK-order-1", EventSeq: 0,
		Symbol: "600000.SH", Reason: "drawdown", EventTs: qt42Stamp,
		Payload: []byte(`{"id":"OBX-key-1","rule":"drawdown","severity":"critical","message":"dd"}`),
	})
	sink := newQT42Sink(t, ts.URL, outbox, RiskEventSinkConfig{}, zerolog.New(io.Discard))

	sink.relay()
	_ = srv.waitBody(t)

	assert.Equal(t, storage.AlertOutboxStatusSent, outbox.mustRow(t, "OBX-key-1").Status,
		"回写必须按 outbox 主键；按 order_id 会标不到行 ⇒ 同一批永久重发")
}

func TestRiskEventSink_DeliveredButZeroRowsMarkedIsLoud(t *testing.T) {
	// 主键对不上、或行被别的进程删掉时，`MarkAlertOutboxSent` 返回 (0, nil)：
	// 没有 error。放任不管的话「已发出」与「每轮重发同一条」在日志里长得一模一样。
	srv, ts := newQT42Server(t, func(int) (int, string) { return http.StatusOK, `{"accepted":1,"duplicates":0}` })
	outbox := newFakeAlertOutbox()
	outbox.markReturnsZero = true
	buf := &lockedBuffer{}
	sink := newQT42Sink(t, ts.URL, outbox, RiskEventSinkConfig{}, zerolog.New(buf))
	mustSaveQT42(t, outbox, qt42SeedRow("ALR-zeromark-1"))

	sink.relay()
	_ = srv.waitBody(t)

	require.Eventually(t, func() bool {
		text := buf.String()
		return strings.Contains(text, "marked no outbox rows") && strings.Contains(text, "ALR-zeromark-1")
	}, 2*time.Second, 10*time.Millisecond, "n=0 且无 error 也要响亮：否则重发是静默的")
}

// ─── 鉴权与构造期校验 ───────────────────────────────────────────────────

func TestRiskEventSink_AuthorizationHeaderFollowsToken(t *testing.T) {
	cases := []struct {
		name     string
		token    string
		wantAuth string
	}{
		{"配置了 token 就带 Bearer", "svc-token-abc", "Bearer svc-token-abc"},
		{"空 token 不带该头（open-access 形态）", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, ts := newQT42Server(t, func(int) (int, string) { return http.StatusOK, `{"accepted":1,"duplicates":0}` })
			outbox := newFakeAlertOutbox()
			sink := newQT42Sink(t, ts.URL, outbox, RiskEventSinkConfig{Token: tc.token, BatchSize: 1}, zerolog.Nop())

			sink.Send(context.Background(), qt42Alert("ALR-auth-1", "drawdown", alert.SeverityInfo))
			sink.relay()
			_ = srv.waitBody(t)
			_, _, auths, _ := srv.counts()
			require.Len(t, auths, 1)
			assert.Equal(t, tc.wantAuth, auths[0])
		})
	}
}

func TestNewRiskEventSink_RejectsBadBaseURL(t *testing.T) {
	for _, base := range []string{"", "localhost:8086", "ftp://example.com", "http://"} {
		_, err := NewRiskEventSink(base, RiskEventSinkConfig{}, newFakeAlertOutbox(), zerolog.Nop())
		assert.Error(t, err, "base URL %q 必须被拒", base)
	}
}

func TestNewRiskEventSink_RequiresOutbox(t *testing.T) {
	// 没有发件箱就做不到「允许延迟、不允许丢」——不许退化成进程内队列。
	_, err := NewRiskEventSink("http://127.0.0.1:8086", RiskEventSinkConfig{}, nil, zerolog.Nop())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outbox")
}

func TestNewRiskEventSink_RejectsBatchOverBodyCap(t *testing.T) {
	_, err := NewRiskEventSink("http://127.0.0.1:8086", RiskEventSinkConfig{BatchSize: riskEventsMaxBatchCeiling + 1},
		newFakeAlertOutbox(), zerolog.Nop())
	require.Error(t, err, "攒批超过 risk 侧 1 MiB 请求体上限要在装配期拒绝，不是悄悄夹小")
}

// ─── 装配开关 ───────────────────────────────────────────────────────────

func TestAttachRiskEventsFromConfig_DisabledMeansNotConfigured(t *testing.T) {
	buf := &lockedBuffer{}
	am := alert.NewAlertManager(alert.AlertManagerConfig{}, zerolog.Nop())
	base := am.ChannelCount()

	require.NoError(t, AttachRiskEventsFromConfig(viper.New(), am, newFakeAlertOutbox(), zerolog.New(buf)))
	assert.Equal(t, base, am.ChannelCount(), "未启用时不许悄悄接一个通道")
	assert.Contains(t, buf.String(), "not_configured")
}

func TestAttachRiskEventsFromConfig_FiredAlertReachesRiskService(t *testing.T) {
	// 端到端一条（除真 PG 外）：经 AlertManager.Fire 进来的告警（对账 / 双跑差异
	// 走这条路）与检测器告警共用同一组通道，因此也必须落 outbox 再进 risk。
	srv, ts := newQT42Server(t, func(int) (int, string) { return http.StatusOK, `{"accepted":1,"duplicates":0}` })
	outbox := newFakeAlertOutbox()
	v := viper.New()
	v.Set("risk_service.events_enabled", true)
	v.Set("risk_service.events_url", ts.URL)
	v.Set("risk_service.events_batch_size", 1)
	v.Set("risk_service.events_relay_interval", "20ms")

	am := alert.NewAlertManager(alert.AlertManagerConfig{}, zerolog.Nop())
	base := am.ChannelCount()
	require.NoError(t, AttachRiskEventsFromConfig(v, am, outbox, zerolog.Nop()))
	require.Equal(t, base+1, am.ChannelCount())

	am.Fire(context.Background(), alert.Alert{
		Rule: "reconciliation", Severity: alert.SeverityCritical, Message: "local vs broker differ",
	})
	body := srv.waitBody(t)
	assert.Contains(t, body, `"verdict":"alert"`)
	assert.Contains(t, body, "reconciliation")
	assert.Contains(t, body, RiskAlertProducerScope)
	am.Close() // 关 manager 也要能关掉 sink（不泄漏 relay goroutine）
}

func TestAttachRiskEventsFromConfig_DoesNotFallBackToSelfURL(t *testing.T) {
	// `risk_service.url` 指的是 analysis 自己（:8085，P1-15 合并后的进程内风控），
	// 它没有 /api/risk/events。回落到那里 = 「少配一项」变成每条告警 404，
	// 且要到告警真触发时才看得见。装配期直接拒。
	v := viper.New()
	v.Set("risk_service.events_enabled", true)
	v.Set("risk_service.url", "http://localhost:8085")

	am := alert.NewAlertManager(alert.AlertManagerConfig{}, zerolog.Nop())
	base := am.ChannelCount()
	err := AttachRiskEventsFromConfig(v, am, newFakeAlertOutbox(), zerolog.Nop())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "events_url")
	assert.Equal(t, base, am.ChannelCount())
}

func TestAttachRiskEventsFromConfig_EnabledWithoutStoreFailsFast(t *testing.T) {
	// 显式启用但 PG 没装配 ⇒ 没有发件箱，宁可拒绝启动也不退化成「会丢」的队列。
	v := viper.New()
	v.Set("risk_service.events_enabled", true)
	v.Set("risk_service.events_url", "http://127.0.0.1:8086")

	am := alert.NewAlertManager(alert.AlertManagerConfig{}, zerolog.Nop())
	base := am.ChannelCount()
	err := AttachRiskEventsFromConfig(v, am, nil, zerolog.Nop())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outbox")
	assert.Equal(t, base, am.ChannelCount())
}

func TestAttachRiskEventsFromConfig_EnabledWithBadConfigFailsFast(t *testing.T) {
	am := alert.NewAlertManager(alert.AlertManagerConfig{}, zerolog.Nop())
	base := am.ChannelCount()

	v2 := viper.New()
	v2.Set("risk_service.events_enabled", true)
	v2.Set("risk_service.events_url", "::::")
	err := AttachRiskEventsFromConfig(v2, am, newFakeAlertOutbox(), zerolog.Nop())
	require.Error(t, err)
	assert.Equal(t, base, am.ChannelCount(), "失败的装配不许留下半接的通道")

	v3 := viper.New()
	v3.Set("risk_service.events_enabled", true)
	v3.Set("risk_service.events_url", "http://127.0.0.1:8086")
	v3.Set("risk_service.events_batch_size", riskEventsMaxBatchCeiling+1)
	err = AttachRiskEventsFromConfig(v3, am, newFakeAlertOutbox(), zerolog.Nop())
	require.Error(t, err)
	assert.Equal(t, base, am.ChannelCount())
}
