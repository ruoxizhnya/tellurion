package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// QT-42 X3c · quant.alert_outbox —— 告警投递的发件箱（transactional outbox）。
//
// 为什么需要这张表：risk 仓的账写着「事件回传允许延迟，**不允许丢**」
// （`tellurion-risk:docs/dataflow.md` §fail-closed 表）。而进程内队列做不到那句：
// 队列满要丢、进程重启要丢、risk 不可达的窗口内也要丢。outbox 把「要发什么」
// 先落成一行，再把「发出去」变成可重来的动作 —— 发送失败只是这行还是 pending。
//
// 数据归属：这是**派生计算的留痕**（ADR-023 的 C 类），落 `quant.*`，不是
// 第二个真相源：告警内容本身仍由 `pkg/alert` 产出，这张表只是投递状态机。
//
// 状态机：pending →（投递成功）sent；pending →（投递失败）pending，attempts+1；
// attempts 触到上限 → failed（终态，不再重试，等人看）。没有 down 路径。

// quant.alert_outbox 的状态词表。DDL 的 CHECK 与此一一对应。
const (
	AlertOutboxStatusPending = "pending"
	AlertOutboxStatusSent    = "sent"
	AlertOutboxStatusFailed  = "failed"
)

// AlertOutboxRecord 是 quant.alert_outbox 的一行：一条待发（或已发）的事件。
//
// 字段是 risk 侧 `POST /api/risk/events` 条目的形状 + 本仓的投递元数据：
// (RunID, OrderID, EventSeq) 就是 risk 的去重键，`alert_id` 是本地主键
// （今天等于 OrderID —— 见 risk_events.go 的键映射）。
type AlertOutboxRecord struct {
	AlertID   string    `json:"alert_id"`
	RunID     string    `json:"run_id"`
	OrderID   string    `json:"order_id"`
	EventSeq  int64     `json:"event_seq"`
	Symbol    string    `json:"symbol,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	Payload   []byte    `json:"payload"`
	EventTs   time.Time `json:"event_ts"`
	Status    string    `json:"status"`
	Attempts  int       `json:"attempts"`
	LastError string    `json:"last_error"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AlertOutboxSchemaDDL 建表 DDL —— migrate() 的 Migration 060 执行这组语句。
//
// 索引只服务一件事：relay 每轮都要「按先后取 pending 的一批」。没有这个部分
// 索引，每轮都是全表扫，而已 sent 的行永远比待发的多。
var AlertOutboxSchemaDDL = []string{
	`CREATE SCHEMA IF NOT EXISTS quant`,
	`CREATE TABLE IF NOT EXISTS quant.alert_outbox (
		alert_id     VARCHAR(128) PRIMARY KEY,
		run_id       VARCHAR(64)  NOT NULL,
		order_id     VARCHAR(128) NOT NULL,
		event_seq    BIGINT       NOT NULL DEFAULT 0,
		symbol       TEXT         NOT NULL DEFAULT '',
		reason       TEXT         NOT NULL DEFAULT '',
		payload      JSONB        NOT NULL,
		event_ts     TIMESTAMPTZ  NOT NULL,
		status       VARCHAR(16)  NOT NULL DEFAULT 'pending'
			CHECK (status IN ('pending','sent','failed')),
		attempts     INT          NOT NULL DEFAULT 0,
		last_error   TEXT         NOT NULL DEFAULT '',
		created_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
		updated_at   TIMESTAMPTZ  NOT NULL DEFAULT now()
	)`,
	`CREATE INDEX IF NOT EXISTS idx_alert_outbox_pending
		ON quant.alert_outbox (created_at) WHERE status = 'pending'`,
}

// SaveAlertOutbox 落一行 pending（写前留痕），并报告**这一条是不是新行**。
//
// 幂等：`alert_id` 冲突即 no-op —— 同一条告警重复入队不该变成两行、
// 也不该把已 sent 的行倒回 pending（ON CONFLICT DO NOTHING，不 DO UPDATE）。
// 返回值不是装饰：`Alert.ID` 的秒级 + 进程内 seq 让「同一秒重启」可以造出
// 两条 id 相同、内容不同的告警，而撞键那条**一行都没有**。调用方必须能把它
// 说出来（见 `cmd/analysis/wiring` 的 `TestRiskEventSink_DuplicateAlertID…`）。
func (s *PostgresStore) SaveAlertOutbox(ctx context.Context, rec *AlertOutboxRecord) (bool, error) {
	if rec == nil {
		return false, fmt.Errorf("SaveAlertOutbox: nil record")
	}
	if rec.AlertID == "" {
		return false, fmt.Errorf("SaveAlertOutbox: alert_id is required")
	}
	if len(rec.Payload) == 0 || !json.Valid(rec.Payload) {
		return false, fmt.Errorf("SaveAlertOutbox: payload must be valid JSON for alert %s", rec.AlertID)
	}
	if rec.EventTs.IsZero() {
		return false, fmt.Errorf("SaveAlertOutbox: event_ts is required for alert %s", rec.AlertID)
	}

	const q = `
		INSERT INTO quant.alert_outbox (
			alert_id, run_id, order_id, event_seq, symbol, reason, payload, event_ts
		) VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8)
		ON CONFLICT (alert_id) DO NOTHING
	`
	tag, err := s.pool.Exec(ctx, q,
		rec.AlertID, rec.RunID, rec.OrderID, rec.EventSeq, rec.Symbol, rec.Reason,
		string(rec.Payload), rec.EventTs,
	)
	if err != nil {
		return false, fmt.Errorf("save alert outbox for %s: %w", rec.AlertID, err)
	}
	return tag.RowsAffected() > 0, nil
}

// ListPendingAlertOutbox 按先后取最多 limit 行待投事件（relay 每轮的输入）。
//
// 不含 sent / failed：已发与已判死的都不该再占带宽。limit<=0 返回空切片。
func (s *PostgresStore) ListPendingAlertOutbox(ctx context.Context, limit int) ([]AlertOutboxRecord, error) {
	if limit <= 0 {
		return nil, nil
	}
	const q = `
		SELECT alert_id, run_id, order_id, event_seq, symbol, reason, payload, event_ts,
		       status, attempts, last_error, created_at, updated_at
		FROM quant.alert_outbox
		WHERE status = 'pending'
		ORDER BY created_at
		LIMIT $1
	`
	rows, err := s.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("list pending alert outbox: %w", err)
	}
	defer rows.Close()

	var out []AlertOutboxRecord
	for rows.Next() {
		var rec AlertOutboxRecord
		if err := rows.Scan(
			&rec.AlertID, &rec.RunID, &rec.OrderID, &rec.EventSeq, &rec.Symbol, &rec.Reason,
			&rec.Payload, &rec.EventTs, &rec.Status, &rec.Attempts, &rec.LastError,
			&rec.CreatedAt, &rec.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan alert outbox row: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate alert outbox: %w", err)
	}
	return out, nil
}

// MarkAlertOutboxSent 把投递成功的行标成 sent，返回实际改动的行数。
//
// `WHERE status <> 'sent'` 是有意的：行已经被别的轮次标过，重复标不该报错，
// 但行数要如实反映「这一轮真的推进了几条」。
func (s *PostgresStore) MarkAlertOutboxSent(ctx context.Context, alertIDs []string) (int, error) {
	if len(alertIDs) == 0 {
		return 0, nil
	}
	const q = `
		UPDATE quant.alert_outbox
		SET status = 'sent', updated_at = now()
		WHERE alert_id = ANY($1) AND status <> 'sent'
	`
	tag, err := s.pool.Exec(ctx, q, alertIDs)
	if err != nil {
		return 0, fmt.Errorf("mark alert outbox sent: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// outboxErrorDetailMaxBytes 是 last_error 的字节上限（列本身是 TEXT，不设上限
// 就会让一次上游报文把整张表的记账写挂；2000 足够放下状态码 + 一段响应体）。
const outboxErrorDetailMaxBytes = 2000

// sanitizeOutboxErrorDetail 把投递原因折成能落进 PG TEXT 列的字符串。
//
// 两步都是必需的，缺一都会让「记一次失败」这件事本身失败：
//   - 非法字节 → U+FFFD：原因里会拼进 risk 的响应体（外部输入），坏字节让
//     服务端报 22021 invalid byte sequence，整条 UPDATE 回滚。
//   - 夹断停在字边界：按字节切一个汉字，切出来的半个 rune 同样是非法 UTF-8。
func sanitizeOutboxErrorDetail(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "\uFFFD")
	}
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// BumpAlertOutboxAttempts 给投递失败的行记一次尝试并留下原因；
// attempts 触到 maxAttempts 即转终态 failed（不再占 relay 的批次）。
//
// 上限之后为什么不删：那行是唯一能回答「这条告警为什么没进 risk」的东西。
// 删掉等于把投递失败伪装成「没触发过告警」—— 本仓不接受这种缺失。
func (s *PostgresStore) BumpAlertOutboxAttempts(ctx context.Context, alertIDs []string, reason string, maxAttempts int) error {
	if len(alertIDs) == 0 {
		return nil
	}
	if maxAttempts <= 0 {
		maxAttempts = 8
	}
	const q = `
		UPDATE quant.alert_outbox
		SET attempts = attempts + 1,
		    last_error = $2,
		    status = CASE WHEN attempts + 1 >= $3 THEN 'failed' ELSE 'pending' END,
		    updated_at = now()
		WHERE alert_id = ANY($1) AND status = 'pending'
	`
	reason = sanitizeOutboxErrorDetail(reason, outboxErrorDetailMaxBytes)
	if _, err := s.pool.Exec(ctx, q, alertIDs, reason, maxAttempts); err != nil {
		return fmt.Errorf("bump alert outbox attempts: %w", err)
	}
	return nil
}

// CountAlertOutboxByStatus 给运维与测试一个总数口径（不返回行，避免把整表读进内存）。
func (s *PostgresStore) CountAlertOutboxByStatus(ctx context.Context) (pending, sent, failed int, err error) {
	const q = `
		SELECT
			COUNT(*) FILTER (WHERE status = 'pending'),
			COUNT(*) FILTER (WHERE status = 'sent'),
			COUNT(*) FILTER (WHERE status = 'failed')
		FROM quant.alert_outbox
	`
	if err = s.pool.QueryRow(ctx, q).Scan(&pending, &sent, &failed); err != nil {
		return 0, 0, 0, fmt.Errorf("count alert outbox: %w", err)
	}
	return pending, sent, failed, nil
}
