package main

// A-04 断言 5 的后端腿：顶栏「待审阅 N」的聚合读面（G1）。
//
// 端点：GET /api/review/pending
//
// 为什么在 core 做聚合而不是让 UI 直连：审阅对象（探索候选 / 采纳台账）住在
// ai-service 的 ai.experiments、ai.adoption_ledger 里，core 本仓没有这份数据。
// 但 UI 需要一个**单一、稳定、可机器判定**的入口，且必须能区分「真的 0 条
// 待审」与「上游不知道」。因此这里只做两件事：
//
//  1. 向已配置的上游（当前仅 ai-service）取待审数；
//  2. 把「取不到」编码成 `pending: null` + 逐源 status，而不是 0 或 503。
//
// 上游契约（agent 仓待实现，属跨仓契约需求）：
//   GET {ai_service.url}/api/explore/review/pending
//   200 {"pending": <int>, "as_of": "<RFC3339>"}
//
// 未配置上游（ai_service.url 为空）或上游不可达时，`pending` 为 null、
// `sources[].status = "unavailable"` —— UI 继续显示「—」。**不画恒 0 角标**：
// 0 会被读成「没有待审」，那是谎报（与 A-04 断言 5 的原裁决一致）。
//
// 读面不挂 RBAC（与其余只读面同姿态），也不进 route_policy 表。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
)

const (
	reviewPendingPath    = "/api/explore/review/pending"
	reviewPendingTimeout = 3 * time.Second
	reviewPendingMaxBody = 64 << 10
)

// reviewPendingSource 是一个「待审阅数」的取数源。接口只留一个方法：
// 聚合面按已配置源的集合逐个调用，任一源失败只影响该源的 status。
type reviewPendingSource interface {
	Name() string
	Pending(ctx context.Context) (int, error)
}

// aiReviewPendingSource 从 ai-service 的审阅队列摘要取数。
type aiReviewPendingSource struct {
	baseURL string
	client  *http.Client
}

func newAIReviewPendingSource(baseURL string) *aiReviewPendingSource {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return nil
	}
	return &aiReviewPendingSource{
		baseURL: base,
		client:  &http.Client{Timeout: reviewPendingTimeout},
	}
}

func (s *aiReviewPendingSource) Name() string { return "ai_explore" }

type aiReviewPendingResponse struct {
	Pending *int   `json:"pending"`
	AsOf    string `json:"as_of"`
}

// Pending 取上游待审数。上游未实现（404）、不可达、格式不对都返回错误，
// 由聚合层编码成 unavailable —— 不把「不知道」伪装成 0。
func (s *aiReviewPendingSource) Pending(ctx context.Context) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, reviewPendingTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+reviewPendingPath, nil)
	if err != nil {
		return 0, fmt.Errorf("build review pending request: %w", err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("review pending upstream unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, reviewPendingMaxBody))
	if err != nil {
		return 0, fmt.Errorf("read review pending response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("review pending upstream status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out aiReviewPendingResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, fmt.Errorf("decode review pending response: %w", err)
	}
	if out.Pending == nil {
		return 0, errors.New("review pending response missing 'pending'")
	}
	if *out.Pending < 0 {
		return 0, fmt.Errorf("review pending response has negative pending=%d", *out.Pending)
	}
	return *out.Pending, nil
}

// reviewPendingSourceStatus 是逐源状态：available 的 pending 可信，
// unavailable 的 pending 恒为 null（不参与聚合）。
type reviewPendingSourceStatus struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Pending *int   `json:"pending"`
	Error   string `json:"error,omitempty"`
}

// reviewPendingResponse 是聚合读面的响应。
//
// pending 为指针：null 表示「至少一个源不可用且有源未知」时无法给出可信总数；
// 全部源可用时才是数字（含真的 0）。
type reviewPendingResponse struct {
	Pending *int                        `json:"pending"`
	AsOf    time.Time                   `json:"as_of"`
	Sources []reviewPendingSourceStatus `json:"sources"`
}

// ReviewPendingHandler 聚合各源的待审数。
type ReviewPendingHandler struct {
	sources []reviewPendingSource
	logger  zerolog.Logger
	now     func() time.Time
}

// NewReviewPendingHandler 构造聚合读面。sources 为空 = 本部署没有任何可查
// 源头 ⇒ 请求时返回 pending:null + 空 sources（UI 显示「—」）。
func NewReviewPendingHandler(sources []reviewPendingSource, logger zerolog.Logger) *ReviewPendingHandler {
	return &ReviewPendingHandler{
		sources: sources,
		logger:  logger.With().Str("component", "review_pending_handler").Logger(),
		now:     time.Now,
	}
}

// RegisterRoutes wires GET /api/review/pending.
func (h *ReviewPendingHandler) RegisterRoutes(router *gin.Engine) {
	router.GET("/api/review/pending", h.getPending)
}

func (h *ReviewPendingHandler) getPending(c *gin.Context) {
	ctx := c.Request.Context()
	resp := reviewPendingResponse{
		AsOf:    h.now().UTC(),
		Sources: make([]reviewPendingSourceStatus, 0, len(h.sources)),
	}

	total := 0
	available := 0
	for _, src := range h.sources {
		if src == nil {
			continue
		}
		n, err := src.Pending(ctx)
		status := reviewPendingSourceStatus{Name: src.Name()}
		if err != nil {
			// 源不可用：pending 留 null，原因如实回传（UI 可展示 tooltip）。
			status.Status = "unavailable"
			status.Error = err.Error()
			h.logger.Warn().Err(err).Str("source", src.Name()).Msg("review pending source unavailable")
		} else {
			v := n
			status.Status = "available"
			status.Pending = &v
			total += n
			available++
		}
		resp.Sources = append(resp.Sources, status)
	}

	// 只有「至少一个源可用且没有源不可用」时才给总数；任何未知源都会让
	// 总数偏小，宁可 null 也不谎报。
	if available > 0 && available == len(resp.Sources) {
		resp.Pending = &total
	}

	c.JSON(http.StatusOK, resp)
}

// reviewPendingSources 依据配置装配源头集合。当前只有 ai-service 一个源；
// 未配置时返回空集合（读面照常注册，返回 pending:null）。
func reviewPendingSources(aiBaseURL string, logger zerolog.Logger) []reviewPendingSource {
	if src := newAIReviewPendingSource(aiBaseURL); src != nil {
		logger.Info().Str("source", src.Name()).Str("base_url", src.baseURL).
			Msg("review pending aggregate enabled")
		return []reviewPendingSource{src}
	}
	logger.Warn().Msg("review pending aggregate has no configured source; GET /api/review/pending returns pending=null")
	return nil
}
