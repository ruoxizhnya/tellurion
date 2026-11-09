// Package orchestrationworker 让 core 的引擎当 tellurion-orchestration 的 worker：
// 经 HTTP 注册 / 心跳 / 领取 / 续租 / 上报，执行 run_spec 并把结果落回 core。
//
// 边界（director 口径，H5）：
//   - 本仓（core）不 import orchestration 的实现包，只消费它的 HTTP 面；
//   - orchestration 也不 import core——claim 等已上 wire 的形状归
//     `tellurion-contracts/run`（CT-04 收编），两边引同一份；
//     其余 wire 形状以 `tellurion-orchestration:docs/api-schema.md` §1「算力」为唯一真相；
//   - 结果内容留在 core（`backtest_jobs`），编排侧只拿 `result_uri` 指针与结论。
package orchestrationworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ruoxizhnya/tellurion-contracts/run"
)

// MaxResponseBytes 是 worker 侧单个响应的读取上限（run_spec 可能较大，
// 但 8 MiB 已远超单条 BacktestRequest）。
const MaxResponseBytes = 8 << 20

// worker API 的哨兵错误（与 orchestration 的 wire code 一一对应）。
var (
	ErrWorkerNotFound          = errors.New("orchestrationworker: worker not found")
	ErrJobNotFound             = errors.New("orchestrationworker: job not found")
	ErrLeaseLost               = errors.New("orchestrationworker: lease lost")
	ErrWorkerDraining          = errors.New("orchestrationworker: worker draining")
	ErrKernelFilterUnavailable = errors.New("orchestrationworker: kernel filter unavailable")
)

// APIError 是编排侧返回的非 2xx。Code 是 wire code（docs/api-schema.md §2）。
type APIError struct {
	StatusCode int
	Code       string
	Message    string
	sentinel   error
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("orchestration: %s (HTTP %d)", e.Code, e.StatusCode)
	}
	return fmt.Sprintf("orchestration: %s: %s (HTTP %d)", e.Code, e.Message, e.StatusCode)
}

// Unwrap 让 errors.Is(err, ErrLeaseLost) 这类判断成立。
func (e *APIError) Unwrap() error { return e.sentinel }

func sentinelFor(code string) error {
	switch code {
	case "worker_not_found":
		return ErrWorkerNotFound
	case "job_not_found":
		return ErrJobNotFound
	case "lease_lost":
		return ErrLeaseLost
	case "worker_draining":
		return ErrWorkerDraining
	case "kernel_filter_unavailable":
		return ErrKernelFilterUnavailable
	default:
		return nil
	}
}

// Client 是编排 worker 端点的 HTTP 客户端。
type Client struct {
	BaseURL string
	HTTP    *http.Client
	// Token 是调用 orchestration 的 service token（`ORCHESTRATION_SERVICE_TOKEN`）。
	// 空 = 不带 Authorization；服务端未启用鉴权时仍是合法调用。
	Token string
}

// NewClient 构造客户端。httpClient 为 nil 时用带 15s 超时的默认值。
func NewClient(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), HTTP: httpClient}
}

// WorkerInfo 是注册 / 心跳的响应。
type WorkerInfo struct {
	WorkerID string   `json:"worker_id"`
	Status   string   `json:"status"`
	Kernels  []string `json:"kernels"`
}

// JobState 是运行期查询（续租时顺带检查 cancel_requested）的最小视图。
type JobState struct {
	JobID           string  `json:"job_id"`
	Status          string  `json:"status"`
	ResultStatus    *string `json:"result_status"`
	Error           string  `json:"error"`
	CancelRequested bool    `json:"cancel_requested"`
}

type claimBody struct {
	Job *run.ClaimView `json:"job"`
}

// Register 注册 worker（幂等；kernels 空 = 不声明能力 = 不过滤）。
func (c *Client) Register(ctx context.Context, workerID string, kernels []string) (WorkerInfo, error) {
	body := struct {
		WorkerID string   `json:"worker_id"`
		Kernels  []string `json:"kernels"`
	}{WorkerID: workerID, Kernels: kernels}
	var out WorkerInfo
	if err := c.post(ctx, "/api/runs/workers", body, &out); err != nil {
		return WorkerInfo{}, err
	}
	return out, nil
}

// Heartbeat 上报存活（服务端时钟）。未知 worker → ErrWorkerNotFound。
func (c *Client) Heartbeat(ctx context.Context, workerID string) (WorkerInfo, error) {
	var out WorkerInfo
	if err := c.post(ctx, "/api/runs/workers/"+pathEscape(workerID)+"/heartbeat", nil, &out); err != nil {
		return WorkerInfo{}, err
	}
	return out, nil
}

// Claim 领取一条可运行的 job；无活返回 (nil, nil)。
func (c *Client) Claim(ctx context.Context, workerID string) (*run.ClaimView, error) {
	var out claimBody
	if err := c.post(ctx, "/api/runs/workers/"+pathEscape(workerID)+"/claim", nil, &out); err != nil {
		return nil, err
	}
	return out.Job, nil
}

// RenewLease 续租。别人持有 / 租约过期 / 行已终态 → ErrLeaseLost。
func (c *Client) RenewLease(ctx context.Context, runID, workerID string) error {
	body := struct {
		WorkerID string `json:"worker_id"`
	}{WorkerID: workerID}
	var out struct {
		RunID      string     `json:"run_id"`
		LeaseUntil *time.Time `json:"lease_until"`
	}
	return c.post(ctx, "/api/runs/jobs/"+pathEscape(runID)+"/lease", body, &out)
}

// Complete 上报 completed / invalid 结论。resultURI 是 core 结果的指针。
func (c *Client) Complete(ctx context.Context, runID, workerID, resultStatus, resultURI string) error {
	body := struct {
		WorkerID     string `json:"worker_id"`
		Status       string `json:"status"`
		ResultStatus string `json:"result_status"`
		ResultURI    string `json:"result_uri"`
	}{WorkerID: workerID, Status: "completed", ResultStatus: resultStatus, ResultURI: resultURI}
	return c.post(ctx, "/api/runs/jobs/"+pathEscape(runID)+"/result", body, nil)
}

// Fail 上报失败结论。
func (c *Client) Fail(ctx context.Context, runID, workerID, errMsg string) error {
	body := struct {
		WorkerID     string `json:"worker_id"`
		Status       string `json:"status"`
		ResultStatus string `json:"result_status"`
		Error        string `json:"error"`
	}{WorkerID: workerID, Status: "failed", ResultStatus: "failed", Error: errMsg}
	return c.post(ctx, "/api/runs/jobs/"+pathEscape(runID)+"/result", body, nil)
}

// AckCancel 确认取消（owner 才能确认）。
func (c *Client) AckCancel(ctx context.Context, runID, workerID string) error {
	body := struct {
		WorkerID string `json:"worker_id"`
	}{WorkerID: workerID}
	return c.post(ctx, "/api/runs/jobs/"+pathEscape(runID)+"/cancel-ack", body, nil)
}

// Job 读一条 run 的当前状态（用于发现 cancel_requested）。
func (c *Client) Job(ctx context.Context, runID string) (JobState, error) {
	var out JobState
	if err := c.get(ctx, "/api/runs/jobs/"+pathEscape(runID), &out); err != nil {
		return JobState{}, err
	}
	return out, nil
}

// SubmitResult 是任务面提交 / 取消的响应（orchestration `docs/api-schema.md` §9.4）。
type SubmitResult struct {
	JobID            string  `json:"job_id"`
	Status           string  `json:"status"`
	ResultStatus     *string `json:"result_status"`
	IdempotentReplay bool    `json:"idempotent_replay"`
}

// JobView 是任务面 GET / list 的冻结视图（§9.4）。客户端只声明自己消费的
// 字段；未列出的字段由服务端继续演进，不影响解码。
type JobView struct {
	JobID           string     `json:"job_id"`
	Status          string     `json:"status"`
	ResultStatus    *string    `json:"result_status"`
	Error           string     `json:"error,omitempty"`
	ResultURI       string     `json:"result_uri,omitempty"`
	RunSpecHash     string     `json:"run_spec_hash"`
	CancelRequested bool       `json:"cancel_requested"`
	CreatedAt       *time.Time `json:"created_at"`
	StartedAt       *time.Time `json:"started_at"`
	CompletedAt     *time.Time `json:"completed_at"`
}

// JobPage 是 list 的响应。
type JobPage struct {
	Jobs  []JobView `json:"jobs"`
	Total int       `json:"total"`
}

// Submit 提交一条 run_spec（任务面归 orchestration，SVC-ORCH-G3）。
//
// 新行 → 202 + `status=pending`；同幂等键重放 → 200 + `idempotent_replay=true`。
// run_spec 原样透传，客户端不解析、不改写——形状由 core 的异步入口负责。
func (c *Client) Submit(ctx context.Context, idempotencyKey string, priority int, runSpec []byte) (SubmitResult, error) {
	if len(runSpec) == 0 {
		return SubmitResult{}, fmt.Errorf("orchestrationworker: empty run_spec")
	}
	body := struct {
		IdempotencyKey string          `json:"idempotency_key"`
		Priority       int             `json:"priority"`
		RunSpec        json.RawMessage `json:"run_spec"`
	}{IdempotencyKey: idempotencyKey, Priority: priority, RunSpec: json.RawMessage(runSpec)}
	var out SubmitResult
	if err := c.post(ctx, "/api/runs/jobs", body, &out); err != nil {
		return SubmitResult{}, err
	}
	return out, nil
}

// List 按状态过滤列出任务（空串 = 不过滤；limit<=0 = 服务端默认）。
func (c *Client) List(ctx context.Context, status, resultStatus string, limit int) (JobPage, error) {
	query := url.Values{}
	if status != "" {
		query.Set("status", status)
	}
	if resultStatus != "" {
		query.Set("result_status", resultStatus)
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	path := "/api/runs/jobs"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var out JobPage
	if err := c.get(ctx, path, &out); err != nil {
		return JobPage{}, err
	}
	return out, nil
}

// Cancel 请求取消：`pending` → 200 `cancelled`；`running` → 202 `cancelling`；
// 终态 → 200 原状态（幂等，重复取消不报错）。
func (c *Client) Cancel(ctx context.Context, runID string) (SubmitResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		c.BaseURL+"/api/runs/jobs/"+pathEscape(runID), nil)
	if err != nil {
		return SubmitResult{}, fmt.Errorf("orchestrationworker: build cancel request: %w", err)
	}
	var out SubmitResult
	if err := c.do(req, &out); err != nil {
		return SubmitResult{}, err
	}
	return out, nil
}

// SaveArtifact 回传一次 run 的三 hash 身份（X0 写端点）。
//
// 裁决 ⑱：core worker 不直写 orchestration 的 run.*，身份只经这条 HTTP 写。
// body 是 `queue.ArtifactRecord` 的线形状（由调用方组装）；端点不收载荷，
// 载荷留在 core 自己的分析产物表。
func (c *Client) SaveArtifact(ctx context.Context, runID string, body []byte) error {
	if len(body) == 0 {
		return fmt.Errorf("orchestrationworker: empty artifact body")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/api/runs/jobs/"+pathEscape(runID)+"/artifacts", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("orchestrationworker: build artifact request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, nil)
}

// Drain 请求摘除自己（滚动升级 / 优雅退出）。draining 不再领新活。
func (c *Client) Drain(ctx context.Context, workerID string) error {
	var out WorkerInfo
	return c.post(ctx, "/api/runs/workers/"+pathEscape(workerID)+"/drain", nil, &out)
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return fmt.Errorf("orchestrationworker: build request: %w", err)
	}
	return c.do(req, out)
}

func (c *Client) post(ctx context.Context, path string, body, out any) error {
	var payload []byte
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("orchestrationworker: encode %s: %w", path, err)
		}
		payload = raw
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("orchestrationworker: build request: %w", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("orchestrationworker: %s %s: %w", req.Method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeAPIError(resp)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, MaxResponseBytes))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, MaxResponseBytes)).Decode(out); err != nil {
		return fmt.Errorf("orchestrationworker: decode %s: %w", req.URL.Path, err)
	}
	return nil
}

func decodeAPIError(resp *http.Response) error {
	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	_ = json.Unmarshal(raw, &body)
	if body.Code == "" {
		body.Code = "unknown"
	}
	if body.Error == "" {
		body.Error = strings.TrimSpace(string(raw))
	}
	return &APIError{
		StatusCode: resp.StatusCode,
		Code:       body.Code,
		Message:    body.Error,
		sentinel:   sentinelFor(body.Code),
	}
}

// pathEscape 只转义可能出现在 id 里的字符；worker / run id 由服务端生成，
// 这里用 url.PathEscape 的等价物避免手写不完整。
func pathEscape(s string) string {
	return strings.NewReplacer(" ", "%20", "/", "%2F", "?", "%3F", "#", "%23").Replace(s)
}
