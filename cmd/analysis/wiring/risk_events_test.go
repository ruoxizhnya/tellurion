package wiring

// QT-42 X3c core 腿的单测：告警回传的 wire 形状、投递语义与装配开关。
//
// 这里不跑检测器（那在 pkg/alert 的 31 条单测里）；本文件只回答三件事：
//  1. 请求体是不是 risk 仓认的形状（外层只允许 `events` 一个键，条目的
//     键集与 `internal/event.Event` 逐字对齐，`verdict="alert"`）；
//  2. 投递失败/部分失败/去重会不会静默（必须响亮）；
//  3. 开关关了是不是真的没接（risk 侧空集是设定，不是坏配置伪装成好配置）。

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
)

// qt42Server 记录每个请求，并把响应交给可编排的 responder（call 从 0 起）。
type qt42Server struct {
	mu        sync.Mutex
	calls     int
	bodies    []string
	paths     []string
	auths     []string
	types     []string
	delivered chan string
	responder func(call int) (int, string)
	release   chan struct{} // 非 nil ⇒ handler 等它关闭后才回写响应
}

func newQT42Server(t *testing.T, responder func(call int) (int, string)) (*qt42Server, *httptest.Server) {
	t.Helper()
	s := &qt42Server{
		delivered: make(chan string, 8),
		responder: responder,
	}
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

func (s *qt42Server) counts() (calls int, paths, auths, types []string, bodies []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, append([]string(nil), s.paths...), append([]string(nil), s.auths...),
		append([]string(nil), s.types...), append([]string(nil), s.bodies...)
}

// qt42Stamp 是测试告警的固定时间戳：ts 字段必须原样进事件，不许被墙上时钟顶掉。
var qt42Stamp = time.Date(2026, 10, 25, 3, 4, 5, 0, time.UTC)

func qt42Alert(id, rule string, sev alert.Severity) alert.Alert {
	return alert.Alert{
		ID:        id,
		Rule:      rule,
		Severity:  sev,
		Message:   rule + " breached",
		Value:     0.21,
		Threshold: 0.20,
		Symbol:    "600000.SH",
		Timestamp: qt42Stamp,
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

// ─── wire 形状 ──────────────────────────────────────────────────────────

func TestRiskEventSink_BatchMatchesRiskEventWireShape(t *testing.T) {
	srv, ts := newQT42Server(t, func(int) (int, string) {
		return http.StatusOK, `{"accepted":2,"duplicates":0}`
	})

	sink, err := NewRiskEventSink(ts.URL+"/", RiskEventSinkConfig{
		BatchSize: 2, FlushInterval: time.Hour, Timeout: 2 * time.Second,
	}, zerolog.Nop())
	require.NoError(t, err)
	defer sink.Close()

	sink.Send(context.Background(), qt42Alert("ALR-drawdown-1-1", "drawdown", alert.SeverityCritical))
	sink.Send(context.Background(), alert.Alert{
		ID: "ALR-risk_metric_breach-2-2", Rule: "risk_metric_breach",
		Severity:  alert.SeverityWarning,
		Message:   "vol over target",
		Timestamp: qt42Stamp, // 无 Symbol：一条「不该编出空 symbol 键」的告警
	})

	body := srv.waitBody(t)

	// 外层：risk 侧对信封用 DisallowUnknownFields ⇒ 只许有 events 一个键。
	var envelope map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(body), &envelope))
	assert.Equal(t, []string{"events"}, sortedKeys(envelope), "信封只允许 events 一个键")

	var items []map[string]any
	require.NoError(t, json.Unmarshal(envelope["events"], &items))
	require.Len(t, items, 2)

	// 条目键集逐字对齐 risk `internal/event.Event`（omitempty 决定省掉哪些）。
	assert.Equal(t,
		[]string{"event_seq", "order_id", "payload", "reason", "run_id", "symbol", "ts", "verdict"},
		sortedKeys(items[0]), "带 symbol 的告警应给出这 8 个键")
	assert.Equal(t,
		[]string{"event_seq", "order_id", "payload", "reason", "run_id", "ts", "verdict"},
		sortedKeys(items[1]), "无 symbol 的告警不该编出一个空 symbol 键")

	assert.Equal(t, RiskAlertProducerScope, items[0]["run_id"], "run_id = 生产者作用域")
	assert.Equal(t, "ALR-drawdown-1-1", items[0]["order_id"], "order_id = Alert.ID（去重键第二段）")
	assert.EqualValues(t, 0, items[0]["event_seq"], "event_seq 恒 0")
	assert.Equal(t, "alert", items[0]["verdict"], "risk 的 monitor/alerts 只投影 verdict=alert")
	assert.Equal(t, "drawdown", items[0]["reason"])
	assert.Equal(t, "600000.SH", items[0]["symbol"])
	assert.Equal(t, "2026-10-25T03:04:05Z", items[0]["ts"], "ts 取告警时间，不是墙上时钟")

	// payload 必须是 JSON 对象（risk 侧对非合法 JSON 的 payload 直接判 400）；
	// 逐字段投影在下一条测试里验。
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(string(mustKeyRaw(t, items[0], "payload"))), &payload))
	assert.IsType(t, map[string]any{}, payload)
}

// mustKeyRaw 取条目里某个键的原始 JSON（条目是按 map[string]any 解的，
// 这里回查原始字节用信封里的 RawMessage 不够用，因此重新按 RawMessage 解一次）。
func mustKeyRaw(t *testing.T, item map[string]any, key string) string {
	t.Helper()
	require.Contains(t, item, key)
	raw, err := json.Marshal(item[key])
	require.NoError(t, err)
	return string(raw)
}

// TestRiskEventSink_PayloadCarriesTheFieldsRiskProjects 单独把 payload 拆开看：
// 上条测试用 map[string]any 解，payload 已经被折成 map，这里按 RawMessage 再走一次。
func TestRiskEventSink_PayloadCarriesTheFieldsRiskProjects(t *testing.T) {
	srv, ts := newQT42Server(t, func(int) (int, string) { return http.StatusOK, `{"accepted":1,"duplicates":0}` })
	sink, err := NewRiskEventSink(ts.URL, RiskEventSinkConfig{BatchSize: 1, FlushInterval: time.Hour}, zerolog.Nop())
	require.NoError(t, err)
	defer sink.Close()

	a := qt42Alert("ALR-max_position_weight-1-1", "max_position_weight", alert.SeverityWarning)
	a.Attributes = map[string]any{"market_value": 123.0}
	sink.Send(context.Background(), a)

	var envelope struct {
		Events []struct {
			Payload map[string]any `json:"payload"`
		} `json:"events"`
	}
	require.NoError(t, json.Unmarshal([]byte(srv.waitBody(t)), &envelope))
	require.Len(t, envelope.Events, 1)
	got := envelope.Events[0].Payload
	assert.Equal(t, "max_position_weight", got["rule"])
	assert.Equal(t, "warning", got["severity"], "Severity 词表与 risk 的 info/warning/critical 同源")
	assert.Equal(t, "max_position_weight breached", got["message"])
	assert.Equal(t, "ALR-max_position_weight-1-1", got["id"])
	assert.Equal(t, 0.21, got["value"])
	assert.Equal(t, 0.20, got["threshold"])
	assert.Contains(t, got, "attributes")
}

// ─── 投递时机 ───────────────────────────────────────────────────────────

func TestRiskEventSink_FlushesPartialBatchOnClose(t *testing.T) {
	srv, ts := newQT42Server(t, func(int) (int, string) { return http.StatusOK, `{"accepted":1,"duplicates":0}` })
	sink, err := NewRiskEventSink(ts.URL, RiskEventSinkConfig{
		BatchSize: 50, FlushInterval: time.Hour,
	}, zerolog.Nop())
	require.NoError(t, err)

	sink.Send(context.Background(), qt42Alert("ALR-1-1", "drawdown", alert.SeverityInfo))
	sink.Close() // 没攒满也必须发出去：停机不许把最后一批告警留在内存里

	calls, paths, _, _, _ := srv.counts()
	assert.Equal(t, 1, calls, "Close 必须排水剩余告警")
	assert.Equal(t, []string{"/api/risk/events"}, paths)
}

func TestRiskEventSink_FlushesOnInterval(t *testing.T) {
	srv, ts := newQT42Server(t, func(int) (int, string) { return http.StatusOK, `{"accepted":1,"duplicates":0}` })
	sink, err := NewRiskEventSink(ts.URL, RiskEventSinkConfig{
		BatchSize: 50, FlushInterval: 20 * time.Millisecond,
	}, zerolog.Nop())
	require.NoError(t, err)
	defer sink.Close()

	sink.Send(context.Background(), qt42Alert("ALR-1-1", "drawdown", alert.SeverityInfo))
	// 不 Close：到点必须自己发，否则 risk 侧看到的告警最多滞后一个循环周期。
	_ = srv.waitBody(t)
}

func TestRiskEventSink_SendDoesNotBlockUnderBackPressure(t *testing.T) {
	// maxBatch=1 ⇒ 后台 goroutine 最多在队列外持有 1 条，其余全压在队列上；
	// handler 卡在 release 上不放，队列必然溢出。测试能跑完本身就是「Send 不阻塞」
	// 的证据（告警链路在循环与 force-check 请求路径上，不能被出站 HTTP 钉住）。
	release := make(chan struct{})
	s := &qt42Server{
		delivered: make(chan string, 8),
		responder: func(int) (int, string) { return http.StatusOK, `{"accepted":1,"duplicates":0}` },
		release:   release,
	}
	ts := httptest.NewServer(http.HandlerFunc(s.handle))
	defer ts.Close()

	buf := &lockedBuffer{}
	sink, err := NewRiskEventSink(ts.URL, RiskEventSinkConfig{
		BatchSize: 1, FlushInterval: time.Hour, Timeout: 30 * time.Second,
	}, zerolog.New(buf))
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < riskEventsQueueCapacity+64; i++ {
			sink.Send(context.Background(), qt42Alert(fmt.Sprintf("ALR-x-%d", i), "drawdown", alert.SeverityInfo))
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Send blocked the alert dispatch path")
	}

	require.Eventually(t, func() bool {
		return strings.Contains(buf.String(), "queue full")
	}, 2*time.Second, 10*time.Millisecond, "溢出必须记在日志里，不许静默丢")

	close(release)
	sink.Close()
}

// ─── 失败要响亮 ─────────────────────────────────────────────────────────

func TestRiskEventSink_PartialFailureNamesEachRejectedAlert(t *testing.T) {
	srv, ts := newQT42Server(t, func(call int) (int, string) {
		if call == 0 {
			return http.StatusMultiStatus, `{"accepted":1,"duplicates":0,` +
				`"failed":[{"index":0,"code":"invalid_params","message":"run_id is required"}],` +
				`"code":"partial_failure"}`
		}
		return http.StatusOK, `{"accepted":2,"duplicates":0}`
	})
	buf := &lockedBuffer{}
	sink, err := NewRiskEventSink(ts.URL, RiskEventSinkConfig{BatchSize: 2, FlushInterval: time.Hour}, zerolog.New(buf))
	require.NoError(t, err)
	defer sink.Close()

	sink.Send(context.Background(), qt42Alert("ALR-dropped-1", "drawdown", alert.SeverityCritical))
	sink.Send(context.Background(), qt42Alert("ALR-kept-2", "daily_loss_limit", alert.SeverityWarning))
	body := srv.waitBody(t)
	assert.Contains(t, body, "ALR-dropped-1")

	require.Eventually(t, func() bool {
		text := buf.String()
		return strings.Contains(text, "ALR-dropped-1") &&
			strings.Contains(text, "invalid_params") &&
			strings.Contains(text, "partial delivery")
	}, 2*time.Second, 10*time.Millisecond, "207 必须点名是哪条告警被拒")

	// 一批被拒不能把通道打死：后续批次仍要投递。
	sink.Send(context.Background(), qt42Alert("ALR-next-3", "drawdown", alert.SeverityInfo))
	sink.Send(context.Background(), qt42Alert("ALR-next-4", "drawdown", alert.SeverityInfo))
	_ = srv.waitBody(t)
	calls, _, _, _, _ := srv.counts()
	assert.Equal(t, 2, calls)
}

func TestRiskEventSink_ServerErrorIsLoudAndKeepsWorking(t *testing.T) {
	srv, ts := newQT42Server(t, func(call int) (int, string) {
		if call == 0 {
			return http.StatusInternalServerError, `{"error":"store unavailable","code":"internal"}`
		}
		return http.StatusOK, `{"accepted":1,"duplicates":0}`
	})
	buf := &lockedBuffer{}
	sink, err := NewRiskEventSink(ts.URL, RiskEventSinkConfig{BatchSize: 1, FlushInterval: time.Hour}, zerolog.New(buf))
	require.NoError(t, err)
	defer sink.Close()

	sink.Send(context.Background(), qt42Alert("ALR-1-1", "drawdown", alert.SeverityCritical))
	_ = srv.waitBody(t)
	require.Eventually(t, func() bool {
		text := buf.String()
		return strings.Contains(text, "batch rejected") && strings.Contains(text, "internal")
	}, 2*time.Second, 10*time.Millisecond, "5xx 要报错点名原因（本腿不自动重发，见文件头）")

	sink.Send(context.Background(), qt42Alert("ALR-2-2", "drawdown", alert.SeverityCritical))
	_ = srv.waitBody(t)
}

func TestRiskEventSink_DuplicatesAreWarned(t *testing.T) {
	// 去重键撞车（重启后同秒同规则同序号）在 risk 侧计成 duplicates。
	// 本腿不静默：日志里必须看得见「这条告警没落库」。
	srv, ts := newQT42Server(t, func(int) (int, string) { return http.StatusOK, `{"accepted":0,"duplicates":2}` })
	buf := &lockedBuffer{}
	sink, err := NewRiskEventSink(ts.URL, RiskEventSinkConfig{BatchSize: 2, FlushInterval: time.Hour}, zerolog.New(buf))
	require.NoError(t, err)
	defer sink.Close()

	sink.Send(context.Background(), qt42Alert("ALR-same-id", "drawdown", alert.SeverityCritical))
	sink.Send(context.Background(), qt42Alert("ALR-same-id", "drawdown", alert.SeverityCritical))
	_ = srv.waitBody(t)

	require.Eventually(t, func() bool {
		text := buf.String()
		return strings.Contains(text, `"level":"warn"`) && strings.Contains(text, "duplicates")
	}, 2*time.Second, 10*time.Millisecond)
}

func TestRiskEventSink_UnserializableAlertIsNotSent(t *testing.T) {
	// NaN 是量化链路真会产出的值，json.Marshal 对它报错。
	// 处置 = 当场报错 + 不回传一个「看起来像」的载荷（告警仍在 LogChannel/Recorder）。
	srv, ts := newQT42Server(t, func(int) (int, string) { return http.StatusOK, `{"accepted":1,"duplicates":0}` })
	buf := &lockedBuffer{}
	sink, err := NewRiskEventSink(ts.URL, RiskEventSinkConfig{BatchSize: 1, FlushInterval: time.Hour}, zerolog.New(buf))
	require.NoError(t, err)
	defer sink.Close()

	a := qt42Alert("ALR-nan-1", "drawdown", alert.SeverityCritical)
	a.Attributes = map[string]any{"drawdown": math.NaN()}
	sink.Send(context.Background(), a)
	sink.Close()

	calls, _, _, _, _ := srv.counts()
	assert.Zero(t, calls, "编不出合法 payload 就不该发一个残的过去")
	text := buf.String()
	assert.Contains(t, text, "not serializable")
	assert.Contains(t, text, "ALR-nan-1")
}

// ─── 鉴权与配置 ─────────────────────────────────────────────────────────

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
			sink, err := NewRiskEventSink(ts.URL, RiskEventSinkConfig{
				Token: tc.token, BatchSize: 1, FlushInterval: time.Hour,
			}, zerolog.Nop())
			require.NoError(t, err)
			defer sink.Close()

			sink.Send(context.Background(), qt42Alert("ALR-1-1", "drawdown", alert.SeverityInfo))
			_ = srv.waitBody(t)
			_, _, auths, _, _ := srv.counts()
			require.Len(t, auths, 1)
			assert.Equal(t, tc.wantAuth, auths[0])
		})
	}
}

func TestRiskEventSink_ContentTypeIsJSON(t *testing.T) {
	srv, ts := newQT42Server(t, func(int) (int, string) { return http.StatusOK, `{"accepted":1,"duplicates":0}` })
	sink, err := NewRiskEventSink(ts.URL, RiskEventSinkConfig{BatchSize: 1, FlushInterval: time.Hour}, zerolog.Nop())
	require.NoError(t, err)
	defer sink.Close()

	sink.Send(context.Background(), qt42Alert("ALR-1-1", "drawdown", alert.SeverityInfo))
	_ = srv.waitBody(t)
	_, _, _, types, _ := srv.counts()
	require.Len(t, types, 1)
	assert.Equal(t, "application/json", types[0])
}

func TestNewRiskEventSink_RejectsBadBaseURL(t *testing.T) {
	for _, base := range []string{"", "localhost:8083", "ftp://example.com", "http://"} {
		_, err := NewRiskEventSink(base, RiskEventSinkConfig{}, zerolog.Nop())
		assert.Error(t, err, "base URL %q 必须被拒", base)
	}
}

func TestNewRiskEventSink_RejectsBatchOverBodyCap(t *testing.T) {
	_, err := NewRiskEventSink("http://127.0.0.1:8083", RiskEventSinkConfig{
		BatchSize: riskEventsMaxBatchCeiling + 1,
	}, zerolog.Nop())
	require.Error(t, err, "攒批超过 risk 侧 1 MiB 请求体上限要在装配期拒绝，不是悄悄夹小")
}

// ─── 装配开关 ───────────────────────────────────────────────────────────

func TestAttachRiskEventsFromConfig_DisabledMeansNotConfigured(t *testing.T) {
	buf := &lockedBuffer{}
	am := alert.NewAlertManager(alert.AlertManagerConfig{}, zerolog.Nop())
	base := am.ChannelCount()

	require.NoError(t, AttachRiskEventsFromConfig(viper.New(), am, zerolog.New(buf)))
	assert.Equal(t, base, am.ChannelCount(), "未启用时不许悄悄接一个通道")
	assert.Contains(t, buf.String(), "not_configured")
}

func TestAttachRiskEventsFromConfig_FiresReachRiskService(t *testing.T) {
	// 端到端一条：经 AlertManager.Fire 进来的告警（对账 / 双跑差异走这条路）
	// 与检测器告警共用同一组通道，因此也必须回传给 risk。
	srv, ts := newQT42Server(t, func(int) (int, string) { return http.StatusOK, `{"accepted":1,"duplicates":0}` })
	v := viper.New()
	v.Set("risk_service.events_enabled", true)
	v.Set("risk_service.events_url", ts.URL)
	v.Set("risk_service.events_batch_size", 1)
	v.Set("risk_service.events_flush_interval", "1h")

	am := alert.NewAlertManager(alert.AlertManagerConfig{}, zerolog.Nop())
	base := am.ChannelCount()
	require.NoError(t, AttachRiskEventsFromConfig(v, am, zerolog.Nop()))
	require.Equal(t, base+1, am.ChannelCount())

	am.Fire(context.Background(), alert.Alert{
		Rule: "reconciliation", Severity: alert.SeverityCritical, Message: "local vs broker differ",
	})
	body := srv.waitBody(t)
	assert.Contains(t, body, `"verdict":"alert"`)
	assert.Contains(t, body, "reconciliation")
	am.Close()
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
	err := AttachRiskEventsFromConfig(v, am, zerolog.Nop())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "events_url")
	assert.Equal(t, base, am.ChannelCount())
}

func TestAttachRiskEventsFromConfig_EnabledWithBadConfigFailsFast(t *testing.T) {
	am := alert.NewAlertManager(alert.AlertManagerConfig{}, zerolog.Nop())

	v := viper.New()
	v.Set("risk_service.events_enabled", true)
	base := am.ChannelCount()
	err := AttachRiskEventsFromConfig(v, am, zerolog.Nop())
	require.Error(t, err, "显式启用却没给 events_url 必须装配期失败，不能推迟到第一条告警")
	assert.Equal(t, base, am.ChannelCount())

	v2 := viper.New()
	v2.Set("risk_service.events_enabled", true)
	v2.Set("risk_service.events_url", "::::")
	err = AttachRiskEventsFromConfig(v2, am, zerolog.Nop())
	require.Error(t, err)
	assert.Equal(t, base, am.ChannelCount(), "失败的装配不许留下半接的通道")
}
