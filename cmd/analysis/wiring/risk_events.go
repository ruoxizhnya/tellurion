package wiring

// risk_events.go —— QT-42 X3c 的 core 腿：告警生产侧接线 + 发件箱。
//
// 修的是什么：risk 仓的 `GET /api/risk/monitor/alerts` 与 `/monitor/evaluate`
// 只读 `risk.risk_events`，且只投影 `verdict="alert"` 的行 —— 这类事件此前
// **没有任何生产者**：core 的 6 个检测器（`pkg/alert`，含对账 / 双跑差异经
// `Fire` 进来的那些）只写进程内 ring buffer 与日志，两个端点因此恒是空集。
//
// 形态：**outbox（先落一行，再投）**，不是「攒在内存里发」。
// 理由有两条，第二条是本单的直接动因：
//  1. risk 侧的账写着「事件回传允许延迟，**不允许丢**」
//     （`tellurion-risk:docs/dataflow.md` 的 fail-closed 表）。进程内队列做不到
//     这句 —— 队列满要丢、进程重启要丢、risk 不可达的窗口内也要丢。
//  2. 落 PG 的行让「这条告警到底进没进 risk」事后可查（status/attempts/last_error）。
//
// 于是投递失败不再是丢，只是「这一行还是 pending」：下一轮 relay 再试，
// 触到 `events_max_attempts` 转 `failed` 终态并留在表里等人看（不删 —— 删掉
// 等于把投递失败伪装成「没触发过告警」）。
//
// wire 形状逐字段对齐 `tellurion-risk:internal/event.Event` + `eventsEnvelope`
// （外层只允许 `events` 一个键：服务端对信封 `DisallowUnknownFields`）。
//
// 去重键的映射（(run_id, order_id, event_seq) 是 risk 的主键）—— 告警既不属于
// 某个 run 也不属于某笔订单，因此 `run_id` = 固定的生产者作用域
// `core:alert-loop`、`order_id` = `Alert.ID`（进程内单调）、`event_seq` 恒 0。
// 代价如实登记：① `?run_id=` 对告警只有生产者含义；② 进程重启后同一秒内
// 同规则同序号会撞键 ⇒ 服务端计 `duplicates`（响应里看得见，本 sink 转 Warn）；
// ③ 告警不带 notional ⇒ `monitor/exposure` 只计入 `unpriced_events`。
//
// 延迟：`Send` 只写一行，真正发出去最长等一个 `events_relay_interval`
// （默认 15s）。告警本来就是分钟级节拍的产物，用这点延迟换「不丢」是划算的。

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
	"tellurion/pkg/storage"
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
	defaultRiskEventsRelayInterval = 15 * time.Second
	defaultRiskEventsMaxAttempts   = 8

	// riskEventsMaxBatchCeiling 是给 `events_batch_size` 设的硬上限：risk 侧
	// 请求体上限 1 MiB（`internal/httpapi.MaxBodyBytes`），一条告警约 0.2–0.5 KB。
	// 超了当场拒绝，而不是悄悄夹一个数让运维以为配得更大。
	riskEventsMaxBatchCeiling = 1000
	riskEventsMaxResponse     = 1 << 20 // 1 MiB：读响应体也不无限吃内存
)

// AlertOutbox 是告警发件箱的端口（生产实现是 `*storage.PostgresStore`，
// 表 = `quant.alert_outbox`，DDL 在 Migration 060）。
//
// 端口开在消费侧（本包），实现侧不 import 本包：换存储不需要动 sink。
// `SaveAlertOutbox` 的 bool = 「这一条是不是新行」；撞键不报错，但静默就等于丢。
type AlertOutbox interface {
	SaveAlertOutbox(ctx context.Context, rec *storage.AlertOutboxRecord) (bool, error)
	ListPendingAlertOutbox(ctx context.Context, limit int) ([]storage.AlertOutboxRecord, error)
	MarkAlertOutboxSent(ctx context.Context, alertIDs []string) (int, error)
	BumpAlertOutboxAttempts(ctx context.Context, alertIDs []string, reason string, maxAttempts int) error
}

// riskEvent 是 `POST /api/risk/events` 批量里的一条事件（risk 侧字段逐一对应）。
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

// riskEventsResponse 是 200/207 的响应体（failed 明细也要读出来点名）。
type riskEventsResponse struct {
	Accepted   int                 `json:"accepted"`
	Duplicates int                 `json:"duplicates"`
	Failed     []riskEventsFailure `json:"failed,omitempty"`
	Code       string              `json:"code,omitempty"`
}

// RiskEventSinkConfig 是告警回传的投递参数；零值字段取默认。
type RiskEventSinkConfig struct {
	// Timeout 是单次批量 POST 与单次 outbox 写的预算。
	Timeout time.Duration
	// Token 是 `Authorization: Bearer <token>` 的凭据。空串 ⇒ 不带该头，
	// 只对 risk 服务处于 open-access（空 JWT secret + 回环绑定）时可用；
	// risk 侧默认要求 trader/admin 角色，缺凭据会收到 401 并响亮记录。
	Token string
	// BatchSize 是单次 POST 携带的最大事件数（也是每轮 relay 的取行数上限）。
	BatchSize int
	// RelayInterval 是「落库 → 发出」的最长驻留。
	RelayInterval time.Duration
	// MaxAttempts 是一行从 pending 转 failed 的尝试上限。
	MaxAttempts int
}

// RiskEventSink 是 `alert.Channel` 的 risk 服务实现：每条告警先落成
// `quant.alert_outbox` 的一行，后台 relay 再按批 `POST /api/risk/events`。
type RiskEventSink struct {
	endpoint    string
	token       string
	client      *http.Client
	timeout     time.Duration
	maxBatch    int
	relayEvery  time.Duration
	maxAttempts int
	outbox      AlertOutbox
	logger      zerolog.Logger

	done    chan struct{}
	stop    chan struct{}
	closeMu sync.Mutex
	closed  bool
}

// NewRiskEventSink 构造并启动后台 relay goroutine。baseURL 只允许 http/https，
// outbox 必填 —— 没有发件箱就没有「不丢」这条保证，宁可装配期失败。
// 调用方必须 Close（或经 AlertManager.Close 间接 Close）。
func NewRiskEventSink(baseURL string, cfg RiskEventSinkConfig, outbox AlertOutbox, logger zerolog.Logger) (*RiskEventSink, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("risk events: invalid base URL %q (want http(s)://host[:port])", baseURL)
	}
	if outbox == nil {
		return nil, fmt.Errorf("risk events: alert outbox is required (没有发件箱就做不到「允许延迟、不允许丢」)")
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
	relayEvery := cfg.RelayInterval
	if relayEvery <= 0 {
		relayEvery = defaultRiskEventsRelayInterval
	}
	maxAttempts := cfg.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultRiskEventsMaxAttempts
	}
	s := &RiskEventSink{
		endpoint:    base + riskEventsPath,
		token:       strings.TrimSpace(cfg.Token),
		client:      &http.Client{Timeout: timeout},
		timeout:     timeout,
		maxBatch:    batchSize,
		relayEvery:  relayEvery,
		maxAttempts: maxAttempts,
		outbox:      outbox,
		logger:      logger.With().Str("component", "risk_event_sink").Logger(),
		done:        make(chan struct{}),
		stop:        make(chan struct{}),
	}
	go s.run()
	return s, nil
}

// Endpoint 返回装配后的完整 URL（装配日志与测试用它核对回落链）。
func (s *RiskEventSink) Endpoint() string { return s.endpoint }

// Send 实现 `alert.Channel`：把告警落成 outbox 的一行，不当场发。
//
// 两处刻意的选择：
//   - **不遵守入参 ctx**：`Channel.Send` 的一般约定是尊重调用方 ctx，但这条
//     路径上 ctx 来自 `POST /api/alerts/force-check` 的请求上下文 —— 客户端
//     挂断不该让一条已经触发的告警消失。改用独立预算 ctx（本包唯一一次偏离该
//     约定，偏离的理由就写在行上）。
//   - **写失败不外抛**：`Channel` 没有 error 返回。PG 写不进去就只能记 Error
//     日志（告警仍在 LogChannel / RecorderChannel 里可见），并显式说明
//     「这条没进 risk」——不降级成「发一个看起来像的载荷」。
//     撞键（同一 id 没有新行）同理走 Warn：静默的丢告警搜不出来。
func (s *RiskEventSink) Send(_ context.Context, a alert.Alert) {
	payload, err := json.Marshal(a)
	if err != nil {
		// Attributes 里进了不可编码的值（NaN / Inf 是量化链路真会产出的）。
		s.logger.Error().Err(err).
			Str("alert_id", a.ID).
			Str("rule", a.Rule).
			Msg("risk events: alert not serializable, NOT sent to risk service")
		return
	}
	rec := &storage.AlertOutboxRecord{
		AlertID:  a.ID,
		RunID:    RiskAlertProducerScope,
		OrderID:  a.ID,
		EventSeq: 0,
		Symbol:   a.Symbol,
		Reason:   a.Rule,
		Payload:  payload,
		EventTs:  a.Timestamp,
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	inserted, err := s.outbox.SaveAlertOutbox(ctx, rec)
	if err != nil {
		s.logger.Error().Err(err).
			Str("alert_id", a.ID).
			Str("rule", a.Rule).
			Msg("risk events: alert NOT recorded in outbox — it will not reach risk service")
		return
	}
	if !inserted {
		// `Alert.ID` 带秒级时间戳与进程内 seq（`pkg/alert/manager.go:362`），
		// 同一秒内重启就能撞出同一个 id 的两条不同告警。主键 + DO NOTHING 让
		// 第二条没有行；若那一行已 sent，risk 收了同一条，不算丢——但若它是
		// pending/failed，这条就真的到不了 risk。这一条路径本会静默，故响亮。
		s.logger.Warn().
			Str("alert_id", a.ID).
			Str("rule", a.Rule).
			Msg("risk events: alert_id already in the outbox — no new row for this occurrence (dropped if that row is failed)")
		return
	}
	s.logger.Debug().Str("alert_id", a.ID).Str("rule", a.Rule).Msg("risk events: alert recorded in outbox (pending)")
}

// Close 停后台 relay，并在退出前再做一轮投递（尽力而为，受 Timeout 约束）。
// 可重复调用。
func (s *RiskEventSink) Close() {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.stop)
	<-s.done
}

func (s *RiskEventSink) run() {
	defer close(s.done)
	ticker := time.NewTicker(s.relayEvery)
	defer ticker.Stop()

	// 刻意不在构造时先 relay 一次：上一进程留下的 pending 行由第一拍（默认 15s）
	// 带走就够了，而「构造即投」会和调用方的 relay 并发读同一批 pending，
	// 造成同一条告警被投两遍（risk 侧靠去重键兜住，但那是要修的行为，不是要接受的行为）。
	for {
		select {
		case <-s.stop:
			s.relay() // 停机前最后一轮：能发出去的别让它在表里躺着
			return
		case <-ticker.C:
			s.relay()
		}
	}
}

// relay 取一批 pending 投出去。行数受 maxBatch 限制，顺序按 created_at。
func (s *RiskEventSink) relay() {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	pending, err := s.outbox.ListPendingAlertOutbox(ctx, s.maxBatch)
	cancel()
	if err != nil {
		s.logger.Error().Err(err).Msg("risk events: relay could not read the outbox")
		return
	}
	if len(pending) == 0 {
		return
	}
	s.deliver(pending)
}

// deliver 发送一批并逐条点名失败项，然后把投递结果写回 outbox。
//
// 「发出去了」必须以 risk 的接受为准（200 全收 / 207 的 accepted 部分），
// 网络错、5xx、被拒的条目一律回写 attempts —— 它们仍是 pending（或触顶后
// failed），下一轮还会被取到。
func (s *RiskEventSink) deliver(batch []storage.AlertOutboxRecord) {
	events := make([]riskEvent, 0, len(batch))
	for i := range batch {
		rec := batch[i]
		events = append(events, riskEvent{
			RunID:    rec.RunID,
			OrderID:  rec.OrderID,
			EventSeq: rec.EventSeq,
			Symbol:   rec.Symbol,
			Verdict:  riskAlertVerdict,
			Reason:   rec.Reason,
			Payload:  json.RawMessage(rec.Payload),
			Ts:       rec.EventTs,
		})
	}
	ids := make([]string, 0, len(batch))
	for i := range batch {
		ids = append(ids, batch[i].AlertID)
	}

	reason, accepted, rejected, err := s.post(events)
	if err != nil {
		s.markAttempts(ids, reason)
		return
	}
	if len(accepted) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
		n, markErr := s.outbox.MarkAlertOutboxSent(ctx, accepted)
		cancel()
		if markErr != nil {
			// risk 已经收了，本地还 pending ⇒ 下一轮会重发同一批。risk 侧的
			// 去重键保证不会双写，但这必须是 Error 日志：状态机与事实不一致。
			s.logger.Error().Err(markErr).Int("accepted", len(accepted)).Int("marked", n).
				Msg("risk events: delivered but outbox NOT marked sent (next relay will re-send)")
		}
	}
	if len(rejected) > 0 {
		s.markAttempts(rejected, "risk service rejected this event")
	}
}

// post 发一次批量。返回 (失败原因, 被接受的 id, 被逐条拒绝的 id, error)。
func (s *RiskEventSink) post(events []riskEvent) (string, []string, []string, error) {
	body, err := json.Marshal(riskEventsRequest{Events: events})
	if err != nil {
		s.logger.Error().Err(err).Int("count", len(events)).Msg("risk events: marshal envelope failed")
		return "marshal envelope failed", nil, nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		s.logger.Error().Err(err).Int("count", len(events)).Msg("risk events: build request failed")
		return "build request failed", nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "tellurion-core-alert-events/1.0")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		s.logger.Warn().Err(err).Str("endpoint", s.endpoint).Int("count", len(events)).
			Msg("risk events: POST failed, batch stays pending in the outbox")
		return fmt.Sprintf("POST %s: %v", s.endpoint, err), nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, riskEventsMaxResponse))
	if readErr != nil {
		s.logger.Warn().Err(readErr).Int("count", len(events)).Msg("risk events: response body unreadable")
		return "response body unreadable", nil, nil, readErr
	}

	var out riskEventsResponse
	decoded := json.Unmarshal(raw, &out) == nil

	switch {
	case resp.StatusCode == http.StatusOK:
		evt := s.logger.Info()
		if out.Duplicates > 0 {
			evt = s.logger.Warn() // 撞键 = 这条告警被 risk 的幂等拒绝了（见文件头代价 ②）
		}
		evt.Int("sent", len(events)).Int("accepted", out.Accepted).Int("duplicates", out.Duplicates).
			Msg("risk events: delivered")
		ids := make([]string, 0, len(events))
		for i := range events {
			ids = append(ids, events[i].OrderID)
		}
		return "", ids, nil, nil

	case resp.StatusCode == http.StatusMultiStatus && decoded:
		// 部分失败：逐条点名，绝不把「有告警没落库」折成一个总数。
		failedIdx := make(map[int]struct{}, len(out.Failed))
		for _, f := range out.Failed {
			failedIdx[f.Index] = struct{}{}
			s.logger.Warn().
				Int("index", f.Index).
				Str("code", f.Code).
				Str("message", f.Message).
				Str("alert_id", idAtIndex(events, f.Index)).
				Msg("risk events: item rejected by risk service")
		}
		accepted := make([]string, 0, len(events))
		rejected := make([]string, 0, len(out.Failed))
		for i := range events {
			if _, bad := failedIdx[i]; bad {
				rejected = append(rejected, events[i].OrderID)
				continue
			}
			accepted = append(accepted, events[i].OrderID)
		}
		s.logger.Warn().Int("sent", len(events)).Int("accepted", out.Accepted).
			Int("duplicates", out.Duplicates).Int("failed", len(out.Failed)).Str("code", out.Code).
			Msg("risk events: partial delivery")
		return "", accepted, rejected, nil

	default:
		s.logger.Error().Int("status", resp.StatusCode).Int("sent", len(events)).
			Str("detail", errorDetail(raw)).
			Msg("risk events: batch rejected, events stay pending in the outbox")
		return fmt.Sprintf("status %d: %s", resp.StatusCode, errorDetail(raw)), nil, nil,
			fmt.Errorf("risk events: POST rejected with status %d", resp.StatusCode)
	}
}

// markAttempts 给失败的行记一次尝试；触到上限即在表里转 failed 终态。
func (s *RiskEventSink) markAttempts(ids []string, reason string) {
	if len(ids) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	if err := s.outbox.BumpAlertOutboxAttempts(ctx, ids, reason, s.maxAttempts); err != nil {
		s.logger.Error().Err(err).Int("count", len(ids)).
			Msg("risk events: delivery failed AND attempts not recorded — next relay re-sends")
	}
}

// idAtIndex 把 207 的失败下标折回告警号，便于运维对着日志找那条告警。
func idAtIndex(events []riskEvent, index int) string {
	if index < 0 || index >= len(events) {
		return ""
	}
	return events[index].OrderID
}

// AttachRiskEventsFromConfig 是告警回传的唯一装配函数（QT-42 X3c core 腿）。
//
// `risk_service.events_enabled=false` ⇒ 不注册通道，本部署视为「未接入 risk
// 服务」并记 `not_configured`。返回 error 的只有「显式启用但配置/依赖不合法」：
// 坏配置拖到第一次告警才暴露，等于让 risk 侧的 alerts / evaluate 一直空集而
// 没人发现（与 QT-66 的装配函数同一条取舍）。
func AttachRiskEventsFromConfig(v *viper.Viper, am *alert.AlertManager, outbox AlertOutbox, logger zerolog.Logger) error {
	if am == nil {
		return nil
	}
	if v == nil || !v.GetBool("risk_service.events_enabled") {
		logger.Info().
			Str("alert_events_status", "not_configured").
			Msg("risk alert events are not configured — risk `monitor/alerts` stays empty by design")
		return nil
	}
	if outbox == nil {
		return fmt.Errorf("risk_service.events_enabled=true 但没有 alert outbox（PG 未装配）：投递做不到「允许延迟、不允许丢」")
	}

	base := strings.TrimSpace(v.GetString("risk_service.events_url"))
	if base == "" {
		// 刻意**不**回落到 `risk_service.url`：那一项指向 analysis 自己
		// （:8085，P1-15 合并后的进程内风控），它没有 `/api/risk/events`。
		// 回落等于把「少配一项」变成每条告警 404，且失败点推迟到告警真的
		// 触发时才出现。宁可装配期直接拒。
		return fmt.Errorf("risk_service.events_enabled=true 但 risk_service.events_url 为空" +
			"（risk 服务的默认可执行文件绑定 127.0.0.1:8086 —— `cmd/riskservice -addr`；" +
			"不能用 risk_service.url，那是 analysis 自己的进程内风控）")
	}

	cfg := RiskEventSinkConfig{
		Timeout:       v.GetDuration("risk_service.events_timeout"),
		Token:         strings.TrimSpace(v.GetString("risk_service.events_token")),
		BatchSize:     v.GetInt("risk_service.events_batch_size"),
		RelayInterval: v.GetDuration("risk_service.events_relay_interval"),
		MaxAttempts:   v.GetInt("risk_service.events_max_attempts"),
	}
	sink, err := NewRiskEventSink(base, cfg, outbox, logger)
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
		Dur("relay_interval", sink.relayEvery).
		Dur("timeout", sink.timeout).
		Int("max_attempts", sink.maxAttempts).
		Bool("token_configured", sink.token != "").
		Str("producer_scope", RiskAlertProducerScope).
		Msg("risk alert event sink attached with PG outbox (QT-42 X3c)")
	return nil
}
