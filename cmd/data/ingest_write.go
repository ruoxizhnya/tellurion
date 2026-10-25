// Package main — 跨仓写面客户端（QT-40 H4 ①）。
//
// core 不再是 `capital_flow` / `top_list` 两张表的写者：这两条作业的**落库
// 那一跳**改投 `tellurion-data` 的 `POST /ingest/points`（契约见
// `tellurion-data:docs/api-schema.md` §1，决策见其 ADR-0006）。取数与归一
// 仍在 core —— 传输的所有权没动（`tellurion-data:AGENTS.md` §3 禁区）。
//
// 三条不能糊过去的口径：
//
//  1. **未配置就失败，不降级**。`ingest_write.base_url` 为空 ⇒ 当场报错。
//     「回落 core 自己的 BulkInsert」看着像可用性，实际是把第二写者藏进
//     fallback 里 —— 那正是本单要消灭的东西，而且藏起来之后没人能证明它
//     哪一天又活了。
//  2. **投送面上没有 `ingest_time` / `visible_at`**。这两个字段由本仓
//     请求 DTO 结构性地排除（不是「填零值」），对端解码时见到未知字段
//     直接 400。可见时刻由对端按声明锚点推导 —— 落库那一侧才有权判定
//     「这条什么时候可见」。
//  3. **有界**。对端单次上限 10000 点；这里按 `maxPoints` 分片投递，
//     一请求一片。代价如实登记：一片失败时**已落的前面几片不回滚**，
//     跨请求的「整池原子性」从来不在这一侧（`tellurion:docs/SPEC.md`
//     DATA-10 的作业层原子性只覆盖到「一次 Process 一事务」为止）。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"tellurion/pkg/data/source"
)

// ingestWriteHardMaxPoints 是对端 `assembly.MaxPointsPerRequest` 的镜像。
// 两个仓各写一次是刻意的：本仓不许 import 服务仓的常量（跨仓只认 HTTP 契约），
// 所以漂移的后果由对端那句 413 承担 —— 分片仍然会成功，只是那一片被拒。
// 改任何一侧都要读另一侧的 `TestWriteRejectsBatchAboveThePerRequestLimit`。
const ingestWriteHardMaxPoints = 10000

const (
	ingestWriteDefaultTimeout = 30 * time.Second
	ingestWriteBodyLimit      = 8 << 20 // 与对端 maxIngestBodyBytes 同值；本侧只用来防自己发疯
)

// ErrIngestWriteUnconfigured 是「没配写面端点」这一种失败。它必须是哨兵
// 而不是自由文本：作业 error_message 里读到它，运维要知道的去改部署配置，
// 不是去查数据质量。
var ErrIngestWriteUnconfigured = errors.New("ingest_write: 写面端点未配置（ingest_write.base_url 为空）")

// ErrIngestContentConflict 是对端 409 的分诊结果：同一主键已有**不同内容**的行。
//
// 这一类失败**不能靠重试消解**（重放同一批还是 409）。老语义是
// `ON CONFLICT DO UPDATE` 静默覆盖，改投对端之后变成响亮失败 —— 这是
// 一次真实的语义变更，处置只有两种：确认上游改了历史数据后走一次受控修正，
// 或者承认「同一主键换源」本身是数据事故。运维要能一眼分得开它和
// 「网络抖了一下」。
var ErrIngestContentConflict = errors.New("ingest_write: 对端判定同主键不同内容（整批回滚，不覆盖旧行）")

// pointsPoster 是执行器依赖的写面 port。抽成接口的理由是取证：
// 作业层测试要钉的是「投出去的那一批点长什么样」，不是对端落没落库。
type pointsPoster interface {
	PostPoints(ctx context.Context, dataType string, points []source.UnifiedDataPoint) (persisted, skipped int, err error)
}

// ingestWriteConfig 来自 `config/data-service.yaml`（token 只从 env 注入）。
type ingestWriteConfig struct {
	BaseURL   string
	Token     string
	Timeout   time.Duration
	MaxPoints int
	Client    *http.Client
}

// ingestWriteClient 是 `POST /ingest/points` 的最小实现。
type ingestWriteClient struct {
	cfg ingestWriteConfig
}

// newIngestWriteClient 收口配置：BaseURL 空串 ⇒ 返回 nil（执行器随后
// fail-loud），MaxPoints 越界 ⇒ 夹到对端硬上限并如实反映在分片大小上。
func newIngestWriteClient(cfg ingestWriteConfig) *ingestWriteClient {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil
	}
	if cfg.MaxPoints <= 0 || cfg.MaxPoints > ingestWriteHardMaxPoints {
		cfg.MaxPoints = ingestWriteHardMaxPoints
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = ingestWriteDefaultTimeout
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: cfg.Timeout}
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	return &ingestWriteClient{cfg: cfg}
}

// ingestWirePoint 是投送面的点。字段集合**刻意不等于** `source.UnifiedDataPoint`：
// 少了 `IngestTime` 与 `VisibleAt` 两个字段，于是「投送自己的可见时刻」
// 在这侧连编译都做不到，而不是靠运行时把值清零。
type ingestWirePoint struct {
	Symbol    string         `json:"symbol"`
	TradeTime time.Time      `json:"trade_time"`
	Source    string         `json:"source"`
	DataType  string         `json:"data_type,omitempty"`
	Data      map[string]any `json:"data"`
}

type ingestWireRequest struct {
	DataType string            `json:"data_type"`
	Points   []ingestWirePoint `json:"points"`
}

type ingestWireResult struct {
	DataType  string `json:"data_type"`
	Received  int    `json:"received"`
	Persisted int    `json:"persisted"`
	Skipped   int    `json:"skipped"`
}

type ingestWireError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// PostPoints 分片投点并汇总计数。`persisted` 口径随对端：
// 「请求点已就位」（新插入 + 同内容 no-op），不是新增行数。
func (c *ingestWriteClient) PostPoints(ctx context.Context, dataType string, points []source.UnifiedDataPoint) (int, int, error) {
	if c == nil {
		return 0, 0, ErrIngestWriteUnconfigured
	}
	if len(points) == 0 {
		return 0, 0, nil
	}

	var persisted, skipped, sent int
	for i := 0; i < len(points); i += c.cfg.MaxPoints {
		select {
		case <-ctx.Done():
			return persisted, skipped, ctx.Err()
		default:
		}
		stop := i + c.cfg.MaxPoints
		if stop > len(points) {
			stop = len(points)
		}
		chunk := points[i:stop]
		got, chunkSkipped, err := c.postChunk(ctx, dataType, chunk)
		if err != nil {
			// 部分成功要如实带出来：调用方读到的 persisted 是「已经落的那几片」，
			// 不是「这次请求想落的」。error_message 里两者必须分得开。
			return persisted, skipped, err
		}
		persisted += got
		skipped += chunkSkipped
		sent += len(chunk)
	}
	if persisted+skipped > sent {
		// 对端计数与投送量对不上账（口径漂移的形状）。**计数一并归零**而不是
		// 把不自洽的数带出去：这一侧不知道到底落了几行，报一个数就会进
		// `sync_jobs.result`，读的人拿它当事实。真实数字写在 error 文本里。
		return 0, 0, fmt.Errorf("ingest_write: %s 对端回执 persisted=%d skipped=%d 超过投送量 %d（本批计数不可信，已拒报）",
			dataType, persisted, skipped, sent)
	}
	return persisted, skipped, nil
}

func (c *ingestWriteClient) postChunk(ctx context.Context, dataType string, chunk []source.UnifiedDataPoint) (int, int, error) {
	body, err := json.Marshal(ingestWireRequest{
		DataType: dataType,
		Points:   projectWirePoints(chunk),
	})
	if err != nil {
		return 0, 0, fmt.Errorf("ingest_write: encode %s batch: %w", dataType, err)
	}
	if len(body) > ingestWriteBodyLimit {
		// 点数没超、字节超了：分片大小按点数算，挡不住宽载荷。
		// 当场失败并点名，比让对端回 400 更难被误读成「数据有问题」。
		return 0, 0, fmt.Errorf("ingest_write: %s chunk of %d point(s) encodes to %d bytes, above the %d byte request limit — lower ingest_write.max_points",
			dataType, len(chunk), len(body), ingestWriteBodyLimit)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+"/ingest/points", bytes.NewReader(body))
	if err != nil {
		return 0, 0, fmt.Errorf("ingest_write: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	}

	resp, err := c.cfg.Client.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("ingest_write: POST /ingest/points (%s, %d point(s)): %w", dataType, len(chunk), err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, 0, fmt.Errorf("ingest_write: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return 0, 0, ingestWriteFailure(resp.StatusCode, raw, dataType, len(chunk), c.cfg.Token == "")
	}

	var result ingestWireResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return 0, 0, fmt.Errorf("ingest_write: decode 200 body for %s: %w (body: %s)", dataType, err, truncateForError(raw))
	}
	if result.DataType != "" && result.DataType != dataType {
		return 0, 0, fmt.Errorf("ingest_write: %s 回执的 data_type=%q 与请求不符", dataType, result.DataType)
	}
	return result.Persisted, result.Skipped, nil
}

// ingestWriteFailure 把对端的错误报文折成 core 侧的分诊结果。
// 未识别的状态一律带原文（截断后），不许把未知失败读成成功或读成 4xx 之一。
func ingestWriteFailure(status int, raw []byte, dataType string, n int, noToken bool) error {
	var wire ingestWireError
	code := ""
	if err := json.Unmarshal(raw, &wire); err == nil {
		code = wire.Error.Code
	}
	message := string(truncateForError(raw))
	if code != "" {
		message = code + ": " + wire.Error.Message
	}

	switch status {
	case http.StatusConflict:
		// 409 = 内容比对拒了这批。重试无用，只有受控修正。
		return fmt.Errorf("%w: %s %d point(s) — %s", ErrIngestContentConflict, dataType, n, message)
	case http.StatusUnauthorized:
		hint := "ingest_write.token 为空"
		if !noToken {
			hint = "ingest_write.token 被对端拒了（与 data-service 的 JWT_SECRET 不是同一份就必然是 401）"
		}
		return fmt.Errorf("ingest_write: 对端 401 unauthenticated on %s — %s; %s", dataType, hint, message)
	case http.StatusForbidden:
		return fmt.Errorf("ingest_write: 对端 403 forbidden on %s（写面档位只开 service / admin）— %s", dataType, message)
	case http.StatusRequestEntityTooLarge:
		// 本侧已按 ingestWriteHardMaxPoints 分片；走到这里说明对端把上界调小了，
		// 或者单片字节数超了。点名配置而不是含糊失败，否则运维只能猜。
		return fmt.Errorf("ingest_write: 对端 413 batch_too_large on %s（本片 %d 点，本侧上界 %d）— 调低 ingest_write.max_points；%s",
			dataType, n, ingestWriteHardMaxPoints, message)
	case http.StatusServiceUnavailable:
		return fmt.Errorf("ingest_write: 对端 503 on %s（写面未接线：data-service 没配 DATABASE_URL，或鉴权闸没装）— %s", dataType, message)
	default:
		return fmt.Errorf("ingest_write: POST /ingest/points on %s 失败 status=%d — %s", dataType, status, message)
	}
}

// projectWirePoints 把采集点位投影成投送点位。
//
// 这里**不复制** IngestTime / VisibleAt：投送面上没有它们（见文件头第 2 条）。
func projectWirePoints(points []source.UnifiedDataPoint) []ingestWirePoint {
	out := make([]ingestWirePoint, 0, len(points))
	for _, p := range points {
		out = append(out, ingestWirePoint{
			Symbol:    p.Symbol,
			TradeTime: p.TradeTime,
			Source:    p.Source,
			DataType:  p.DataType,
			Data:      p.Data,
		})
	}
	return out
}

func truncateForError(raw []byte) []byte {
	const limit = 400
	if len(raw) <= limit {
		return raw
	}
	return append(append([]byte(nil), raw[:limit]...), "…"...)
}

// ingestStepResult 是一次「取数 → 归一 → 投点」的计数。
type ingestStepResult struct {
	SourceName string
	Fetched    int
	Persisted  int
	Skipped    int
}

// runIngestStep 是两条执行器共用的那一步，取代了
// `source.NewETLPipeline(...).Process(...)`。
//
// 分工变了，形状也就变了：core 只做取数 + 归一（传输在这侧），
// 校验 / 闸门 / 去重 / 幂等都在对端（`tellurion-data:internal/assembly`）。
// 因此这里**不再**调 `ValidatePoints` / `DeduplicateWithCount` ——
// 在本侧先筛一遍，等于用一套与对端可能漂移的口径决定「哪些点不被看见」，
// 而那些计数会以 skipped 的名义进 `sync_jobs.result`，读的人无法分辨
// 是哪一侧筛掉的。
//
// nil registry / nil poster 都 fail-loud（沿用 DATA-10 那条纪律：装配漏了
// 谁就在作业 error 里现形，不许静默空表）。
func runIngestStep(ctx context.Context, reg *source.Registry, poster pointsPoster,
	req source.FetchRequest, normalize source.Normalizer) (ingestStepResult, error) {
	if reg == nil {
		return ingestStepResult{}, fmt.Errorf("ingest_write: nil registry for %s", req.DataType)
	}
	if poster == nil {
		return ingestStepResult{}, fmt.Errorf("%w: data_type=%s", ErrIngestWriteUnconfigured, req.DataType)
	}
	if normalize == nil {
		return ingestStepResult{}, fmt.Errorf("ingest_write: nil normalizer for %s", req.DataType)
	}

	resp, err := reg.Fetch(ctx, req)
	if err != nil {
		return ingestStepResult{}, fmt.Errorf("ingest_write: fetch %s: %w", req.DataType, err)
	}

	result := ingestStepResult{SourceName: resp.Source, Fetched: len(resp.Items)}
	points := make([]source.UnifiedDataPoint, 0, len(resp.Items))
	for _, item := range resp.Items {
		dp := normalize(item, resp.Source, req.DataType)
		if dp.Data == nil {
			// 归一层自己声明「这个点没有载荷」：计入 skipped，不投一个空行。
			result.Skipped++
			continue
		}
		if dp.DataType == "" {
			dp.DataType = req.DataType
		}
		if dp.Source == "" {
			// 对端把「来源空白」当批次不合法整批拒（来源是内容比对的一部分），
			// 所以在这一侧当场点名是哪条记录缺源，比收一条 400 报文好读。
			return ingestStepResult{}, fmt.Errorf("ingest_write: %s normalizer returned an empty source for %s", req.DataType, dp.Symbol)
		}
		points = append(points, dp)
	}
	if len(points) == 0 {
		return result, nil
	}

	persisted, storeSkipped, err := poster.PostPoints(ctx, req.DataType, points)
	if err != nil {
		// 部分片可能已经落了：把「已就位」的计数如实带在结果里，由调用方决定
		// 怎么记账（这里只上抛原始错误，不翻译成「取数失败」，两类处置不同）。
		//
		// 这条不是洁癖：老形状里「落库失败 = 那个事务一行都没落」是真的，
		// 而跨仓之后一个 chunk 内部还要分片，**片与片之间不共享事务** ——
		// 把计数丢掉会让 `sync_jobs.result.persisted` 系统性少报，
		// 而少报的 persisted 与「静默丢数据」在读侧长得一模一样。
		return ingestStepResult{SourceName: resp.Source, Fetched: result.Fetched,
			Persisted: persisted, Skipped: result.Skipped + storeSkipped}, err
	}
	result.Persisted = persisted
	result.Skipped += storeSkipped
	return result, nil
}
