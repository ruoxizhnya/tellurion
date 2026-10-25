package wiring

// risk_events.go —— QT-42 X3c 的 core 腿：告警生产侧接线。
//
// 修的是什么：risk 仓的 `GET /api/risk/monitor/alerts` 与 `/monitor/evaluate`
// 只读 `risk.risk_events`，且只投影 `verdict="alert"` 的行 —— 今天**没有任何
// 生产者**：core 的 6 个检测器（`pkg/alert`，含对账 / 双跑差异经 `Fire` 进来的
// 那些）只写进程内 ring buffer 与日志，所以这两个端点恒返回空集。
// 本文件把告警按 `POST /api/risk/events` 异步回传。
//
// wire 形状逐字段对齐 `tellurion-risk:internal/event.Event` + `eventsEnvelope`
// （外层只允许 `events` 一个键：服务端 `DisallowUnknownFields`）。
//
// 去重键的映射（(run_id, order_id, event_seq)，risk 的主键）—— 告警既不属于
// 某个 run 也不属于某笔订单，因此：
//   - `run_id` = 固定的生产者作用域 `core:alert-loop`；
//   - `order_id` = `Alert.ID`（`ALR-<rule>-<unix 秒>-<进程内单调序号>`）；
//   - `event_seq` 恒 0。
//
// 代价如实登记：① `?run_id=` 对告警没有「一次回测」的含义，只有生产者含义；
// ② 进程重启后同一秒内同规则同序号会撞键 —— 服务端把它计成 `duplicates`
// 并在响应里回传（本 sink 以 Warn 记录），不是静默丢；③ 告警不带 notional，
// 所以 `monitor/exposure` 只会把它们计入 `unpriced_events`，本单不改敞口口径。
//
// 交付语义：异步、best-effort、非阻塞。`Send` 只入队（队列满则丢弃并记日志）——
// 检测器跑在周期循环和 `POST /api/alerts/force-check` 的请求路径上，不能让
// 一次出站 HTTP 把它们钉住。批量部分失败（207）与 5xx **不自动重发**：
// 本腿只做「一次投递 + 一条响亮日志」，重发队列不在本单范围（见 TASKS 未落项）。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"

	"tellurion/pkg/alert"
)

const (
	// riskEventsPath 是 risk 仓的事件回传端点（X3c）。
	riskEventsPath = "/api/risk/events"

	// RiskAlertProducerScope 是告警事件在 risk 侧的 run_id 取值。导出是给
	// 读端（`GET /api/risk/monitor/alerts?run_id=`）与文档一个共同的字符串。
	RiskAlertProducerScope = "core:alert-loop"

	// riskAlertVerdict 是 risk 监控投影认的告警标记（event.VerdictAlert 同值）。
	riskAlertVerdict = "alert"

	defaultRiskEventsTimeout       = 10 * time.Second
	defaultRiskEventsBatchSize     = 50
	defaultRiskEventsFlushInterval = 30 * time.Second

	// riskEventsQueueCapacity 是背压缓冲：一批告警在告警通道里最多攒这么多条
	// 未投递，超出即丢弃并告警日志（与 WebhookChannel 同型取舍）。
	riskEventsQueueCapacity = 256
	riskEventsMaxResponse   = 1 << 20 // 1 MiB：与 risk 侧的请求体上限同量级

	// riskEventsMaxBatchCeiling 是给 `events_batch_size` 设的硬上限：risk 侧
	// 请求体上限 1 MiB（`internal/httpapi.MaxBodyBytes`），一条告警约 0.2–0.5 KB。
	// 超了当场拒绝，而不是悄悄夹一个数让运维以为配得更大。
	riskEventsMaxBatchCeiling = 1000
)

// riskEvent 是 `POST /api/risk/events` 批量里的一条事件。字段与 risk 仓
// `internal/event.Event` 的 JSON 标签逐一对应；多一个键不会失败（服务端按
// 条宽松解码并保留原文），少 `run_id`/`order_id`/`ts` 会被判 400。
type riskEvent struct {
	RunID    string          `json:"run_id"`
	OrderID  string          `json:"order_id"`
	EventSeq int64           `json:"event_seq"`
	Symbol   string          `json:"symbol,omitempty"`
	Verdict  string          `json:"verdict,omitempty"`
	Reason   string          `json:"reason,omitempty"`
	Payload  json.RawMessage `json:"payload,omitempty"`
	Ts       time.Time       `json:"ts"`
}

// riskEventsRequest 是外层信封：服务端只认这一个键。
type riskEventsRequest struct {
	Events []riskEvent `json:"events"`
}

// riskEventsFailure 是 207 里的逐条失败项。
type riskEventsFailure struct {
	Index   int    `json:"index"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// riskEventsResponse 是 200/207 的响应体（多余的 failed 明细也要读出来点名）。
type riskEventsResponse struct {
	Accepted   int                 `json:"accepted"`
	Duplicates int                 `json:"duplicates"`
	Failed     []riskEventsFailure `json:"failed,omitempty"`
	Code       string              `json:"code,omitempty"`
}

// RiskEventSinkConfig 是告警回传的投递参数；零值字段取默认。
type RiskEventSinkConfig struct {
	// Timeout 是单次批量 POST 的预算。
	Timeout time.Duration
	// Token 是 `Authorization: Bearer <token>` 的凭据。空串 ⇒ 不带该头，
	// 只对 risk 服务处于 open-access（空 JWT secret + 回环绑定）时可用；
	// risk 侧默认要求 trader/admin 角色，缺凭据会收到 401 并响亮记录。
	Token string
	// BatchSize 是单次 POST 携带的最大事件数（攒到即发）。
	BatchSize int
	// FlushInterval 是未攒满 BatchSize 时的最长驻留时间。
	FlushInterval time.Duration
}

// RiskEventSink 是 `alert.Channel` 的 risk 服务实现：把每条告警投影成
// `verdict="alert"` 的事件，攒批异步 POST 到 `POST /api/risk/events`。
//
// 并发：Send 只入队，投递在唯一后台 goroutine 里串行发生；Close 关队列并等
// 该 goroutine 把剩余告警发完（受 Timeout 约束）。
type RiskEventSink struct {
	endpoint   string
	token      string
	client     *http.Client
	timeout    time.Duration
	maxBatch   int
	flushEvery time.Duration
	logger     zerolog.Logger
	queue      chan riskEvent
	done       chan struct{}
	mu         sync.Mutex
	closed     bool
}

// NewRiskEventSink 构造并启动后台投递 goroutine。baseURL 只允许 http/https；
// 调用方必须 Close（或经 AlertManager.Close 间接 Close）以排水在途告警。
func NewRiskEventSink(baseURL string, cfg RiskEventSinkConfig, logger zerolog.Logger) (*RiskEventSink, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("risk events: invalid base URL %q (want http(s)://host[:port])", baseURL)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultRiskEventsTimeout
	}
	batchSize := cfg.BatchSize
	if batchSize <= 0 {
		batchSize = defaultRiskEventsBatchSize
	}
	if batchSize > riskEventsMaxBatchCeiling {
		return nil, fmt.Errorf("risk events: events_batch_size %d exceeds %d (request body would pass risk service's 1 MiB cap)", batchSize, riskEventsMaxBatchCeiling)
	}
	flushEvery := cfg.FlushInterval
	if flushEvery <= 0 {
		flushEvery = defaultRiskEventsFlushInterval
	}
	s := &RiskEventSink{
		endpoint:   base + riskEventsPath,
		token:      strings.TrimSpace(cfg.Token),
		client:     &http.Client{Timeout: timeout},
		timeout:    timeout,
		maxBatch:   batchSize,
		flushEvery: flushEvery,
		logger:     logger.With().Str("component", "risk_event_sink").Logger(),
		queue:      make(chan riskEvent, riskEventsQueueCapacity),
		done:       make(chan struct{}),
	}
	go s.run()
	return s, nil
}

// Endpoint 返回装配后的完整 URL（装配日志与测试用它核对回落链）。
func (s *RiskEventSink) Endpoint() string { return s.endpoint }

// Send 实现 `alert.Channel`：非阻塞入队。队列满时丢弃并记 Warn —— 告警链路
// 不反向施压给检测器（同一批次里的告警仍在 LogChannel / RecorderChannel 里）。
func (s *RiskEventSink) Send(_ context.Context, a alert.Alert) {
	payload, err := json.Marshal(a)
	if err != nil {
		// Attributes 里进了不可编码的值（NaN / Inf 是量化链路真会出现的）。
		// 不降级成一个「看起来像」的载荷：本条不回传，但当场报错。
		s.logger.Error().Err(err).
			Str("alert_id", a.ID).
			Str("rule", a.Rule).
			Msg("risk events: alert not serializable, NOT sent to risk service")
		return
	}
	ev := riskEvent{
		RunID:    RiskAlertProducerScope,
		OrderID:  a.ID,
		EventSeq: 0,
		Symbol:   a.Symbol,
		Verdict:  riskAlertVerdict,
		Reason:   a.Rule,
		Payload:  payload,
		Ts:       a.Timestamp,
	}

	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return
	}
	select {
	case s.queue <- ev:
	default:
		s.logger.Warn().
			Str("alert_id", a.ID).
			Str("rule", a.Rule).
			Int("queue_capacity", riskEventsQueueCapacity).
			Msg("risk events: queue full, alert dropped")
	}
}

// Close 关闭队列并等待后台 goroutine 投递完剩余告警。可重复调用。
func (s *RiskEventSink) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.queue)
	s.mu.Unlock()
	<-s.done
}

func (s *RiskEventSink) run() {
	defer close(s.done)
	ticker := time.NewTicker(s.flushEvery)
	defer ticker.Stop()

	batch := make([]riskEvent, 0, s.maxBatch)
	for {
		select {
		case ev, ok := <-s.queue:
			if !ok {
				s.deliver(batch)
				return
			}
			batch = append(batch, ev)
			if len(batch) >= s.maxBatch {
				s.deliver(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				s.deliver(batch)
				batch = batch[:0]
			}
		}
	}
}

// deliver 发送一批并逐条点名失败项。响应里的 duplicates 非零要用 Warn：
// 那意味着 risk 侧按去重键拒绝了这条告警（见文件头代价 ②）。
func (s *RiskEventSink) deliver(batch []riskEvent) {
	if len(batch) == 0 {
		return
	}
	body, err := json.Marshal(riskEventsRequest{Events: batch})
	if err != nil {
		s.logger.Error().Err(err).Int("count", len(batch)).Msg("risk events: marshal envelope failed")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		s.logger.Error().Err(err).Int("count", len(batch)).Msg("risk events: build request failed")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "tellurion-core-alert-events/1.0")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		s.logger.Warn().Err(err).
			Str("endpoint", s.endpoint).
			Int("count", len(batch)).
			Msg("risk events: POST failed, batch NOT delivered")
		return
	}
	defer func() { _ = resp.Body.Close() }()

	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, riskEventsMaxResponse))
	if readErr != nil {
		s.logger.Warn().Err(readErr).Int("count", len(batch)).
			Msg("risk events: response body unreadable")
		return
	}

	var out riskEventsResponse
	decoded := json.Unmarshal(raw, &out) == nil

	switch {
	case resp.StatusCode == http.StatusOK:
		evt := s.logger.Info()
		if out.Duplicates > 0 {
			evt = s.logger.Warn()
		}
		evt.Int("sent", len(batch)).
			Int("accepted", out.Accepted).
			Int("duplicates", out.Duplicates).
			Msg("risk events: alerts delivered")
	case resp.StatusCode == http.StatusMultiStatus && decoded:
		// 部分失败：逐条点名，绝不把「有告警没落库」折成一个总数。
		for _, f := range out.Failed {
			s.logger.Warn().
				Int("index", f.Index).
				Str("code", f.Code).
				Str("message", f.Message).
				Str("alert_id", idAtIndex(batch, f.Index)).
				Msg("risk events: item rejected by risk service")
		}
		s.logger.Warn().
			Int("sent", len(batch)).
			Int("accepted", out.Accepted).
			Int("duplicates", out.Duplicates).
			Int("failed", len(out.Failed)).
			Str("code", out.Code).
			Msg("risk events: partial delivery")
	default:
		s.logger.Error().
			Int("status", resp.StatusCode).
			Int("sent", len(batch)).
			Str("detail", errorDetail(raw)).
			Msg("risk events: batch rejected, alerts NOT stored")
	}
}

// idAtIndex 把 207 的失败下标折回告警号，便于运维对着日志找那条告警。
func idAtIndex(batch []riskEvent, index int) string {
	if index < 0 || index >= len(batch) {
		return ""
	}
	return batch[index].OrderID
}

// AttachRiskEventsFromConfig 是告警回传的唯一装配函数（QT-42 X3c core 腿）。
//
// `risk_service.events_enabled=false` ⇒ 不注册通道，本部署视为「未接入 risk
// 服务」并记 `not_configured`。返回 error 的只有「显式启用但配置不合法」：
// 把坏配置拖到第一次告警才暴露，等于让 risk 侧的 alerts / evaluate 一直空集
// 而没人发现（与 QT-66 的装配函数同一条取舍）。
func AttachRiskEventsFromConfig(v *viper.Viper, am *alert.AlertManager, logger zerolog.Logger) error {
	if am == nil {
		return nil
	}
	if v == nil || !v.GetBool("risk_service.events_enabled") {
		logger.Info().
			Str("alert_events_status", "not_configured").
			Msg("risk alert events are not configured — risk `monitor/alerts` stays empty by design")
		return nil
	}

	base := strings.TrimSpace(v.GetString("risk_service.events_url"))
	if base == "" {
		// 刻意**不**回落到 `risk_service.url`：那一项指向 analysis 自己
		// （:8085，P1-15 合并后的进程内风控），它没有 `/api/risk/events`。
		// 回落等于把「少配一项」变成「每条告警都 404」，且失败点推迟到告警
		// 真的触发时才出现。宁可装配期直接拒（与 QT-66 的 resolve_url 同口径）。
		return fmt.Errorf("risk_service.events_enabled=true 但 risk_service.events_url 为空" +
			"（risk 服务的默认可执行文件绑定 127.0.0.1:8086 —— `cmd/riskservice -addr`；" +
			"不能用 risk_service.url，那是 analysis 自己的进程内风控）")
	}

	cfg := RiskEventSinkConfig{
		Timeout:       v.GetDuration("risk_service.events_timeout"),
		Token:         strings.TrimSpace(v.GetString("risk_service.events_token")),
		BatchSize:     v.GetInt("risk_service.events_batch_size"),
		FlushInterval: v.GetDuration("risk_service.events_flush_interval"),
	}
	sink, err := NewRiskEventSink(base, cfg, logger)
	if err != nil {
		return err
	}
	if !am.AddChannel(sink) {
		sink.Close()
		return fmt.Errorf("alert manager already closed — risk event sink not attached")
	}
	logger.Info().
		Str("endpoint", sink.Endpoint()).
		Int("batch_size", sink.maxBatch).
		Dur("flush_interval", sink.flushEvery).
		Dur("timeout", sink.timeout).
		Bool("token_configured", sink.token != "").
		Str("producer_scope", RiskAlertProducerScope).
		Msg("risk alert event sink attached (QT-42 X3c)")
	return nil
}
