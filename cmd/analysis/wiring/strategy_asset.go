package wiring

// strategy_asset.go —— QT-32 K11：AI 产物的回测入口闸门。
//
// 修的是什么：`strategy_ref` 非空的 run 今天可以直接进引擎 —— 资产服务里
// 一个 `built` / `validated` 甚至 `rejected` 的版本，只要调用方写对 id，
// 就能拿到一条「看起来完全正常」的回测结果。本文件把「非空 strategy_ref
// 必须先过人工 promote 且留痕」做成回测入口的前置检查：
//
//  1. `GET {asset}/api/strategies/{id}/resolve?ref=` → 审批态与两个 hash；
//  2. `GET {asset}/api/strategies/{id}/promotions?version_hash=` → 审批留痕；
//  3. `status != promoted` 或缺一条 `outcome=promoted` 的同 artifact 审计 ⇒ 拒绝。
//
// 空 `strategy_ref` 的内置策略路径**逐字节不变**（不解析、不联网）。
//
// 配置（`strategy_asset.*`）：
//   - `resolve_enabled`：显式开关。false = 本部署不接资产服务 ⇒ 非空
//     `strategy_ref` 一律拒绝（fail-closed），内置策略不受影响。
//   - `resolve_url`：资产服务基址（`tellurion-strategy` 默认 :8090）。
//   - `resolve_token`：服务间凭证（Bearer）。读面今天端口隔离、不校验；
//     预留以便网关统一鉴权。
//   - `resolve_timeout`：单次调用超时，缺省 10s。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rs/zerolog"
	contracts "github.com/ruoxizhnya/tellurion-contracts/engine"
	"github.com/spf13/viper"

	"tellurion/pkg/backtest"
)

const (
	// strategyAssetPromotedStatus 是唯一放行的审批态（冻结枚举值）。
	strategyAssetPromotedStatus = "promoted"

	defaultStrategyAssetTimeout = 10 * time.Second
	strategyAssetMaxBody        = 1 << 20 // 1 MiB：元数据 / 审计行不该比这更大
)

// GateError 是资产闸的拒绝原因。带 HTTP 状态是为了让 analysis 入口原样映射
// （400 引用形状 / 404 资产不存在 / 409 未过审批 / 502 资产服务异常 / 503 未配置）。
type GateError struct {
	Status  int
	Code    string
	Message string
}

func (e *GateError) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

// StrategyAssetGate 是资产服务的只读客户端（resolve + promotions）。
type StrategyAssetGate struct {
	baseURL string
	token   string
	client  *http.Client
}

// NewStrategyAssetGate 构造闸门。baseURL 只允许 http/https。
func NewStrategyAssetGate(baseURL, token string, timeout time.Duration) (*StrategyAssetGate, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("strategy asset gate: invalid base URL %q (want http(s)://host[:port])", baseURL)
	}
	if timeout <= 0 {
		timeout = defaultStrategyAssetTimeout
	}
	return &StrategyAssetGate{
		baseURL: base,
		token:   strings.TrimSpace(token),
		client:  &http.Client{Timeout: timeout},
	}, nil
}

type strategyResolveResponse struct {
	StrategyID   string `json:"strategy_id"`
	VersionHash  string `json:"version_hash"`
	ArtifactHash string `json:"artifact_hash"`
	Carrier      string `json:"carrier"`
	Status       string `json:"status"`
}

type strategyPromotionEvent struct {
	StrategyID   string    `json:"strategy_id"`
	VersionHash  string    `json:"version_hash"`
	ArtifactHash string    `json:"artifact_hash"`
	Actor        string    `json:"actor"`
	Outcome      string    `json:"outcome"`
	DecidedAt    time.Time `json:"decided_at"`
}

// LoadStrategyAssetGate 从配置装配闸门。
//
// `resolve_enabled=false` 返回 (nil, nil)：调用方把它当成「本部署未接入资产
// 服务」，非空 strategy_ref 在 CheckStrategyRef 里被拒（503），而不是放行。
func LoadStrategyAssetGate(v *viper.Viper, logger zerolog.Logger) (*StrategyAssetGate, error) {
	if v == nil || !v.GetBool("strategy_asset.resolve_enabled") {
		logger.Warn().
			Bool("strategy_asset_resolve_enabled", false).
			Msg("strategy asset gate is NOT enabled — runs carrying a non-empty strategy_ref will be rejected (fail-closed); " +
				"builtin strategies are unaffected")
		return nil, nil
	}
	base := strings.TrimSpace(v.GetString("strategy_asset.resolve_url"))
	if base == "" {
		return nil, fmt.Errorf("strategy_asset.resolve_enabled=true 但 strategy_asset.resolve_url 为空" +
			"（tellurion-strategy 的默认地址是 http://localhost:8090）")
	}
	timeout := v.GetDuration("strategy_asset.resolve_timeout")
	if timeout <= 0 {
		timeout = defaultStrategyAssetTimeout
	}
	gate, err := NewStrategyAssetGate(base, v.GetString("strategy_asset.resolve_token"), timeout)
	if err != nil {
		return nil, err
	}
	logger.Info().
		Str("endpoint", base).
		Msg("strategy asset gate enabled (non-empty strategy_ref must be promoted + audited)")
	return gate, nil
}

// CheckStrategyRef 是入口唯一调用点：空引用直接放行；非空引用必须过闸。
//
// 返回的 identity 交给 `backtest.WithStrategyAssetIdentity` 放进 ctx，
// 让 provenance 记下资产服务签发的 version_hash / artifact_hash。
func CheckStrategyRef(ctx context.Context, gate *StrategyAssetGate, rawRef string) (*backtest.StrategyAssetIdentity, error) {
	ref := strings.TrimSpace(rawRef)
	if ref == "" {
		return nil, nil
	}
	if gate == nil {
		return nil, &GateError{
			Status:  http.StatusServiceUnavailable,
			Code:    "strategy_asset_not_configured",
			Message: "a non-empty strategy_ref requires the strategy asset service (strategy_asset.resolve_url is not configured)",
		}
	}
	return gate.Gate(ctx, ref)
}

// Gate 解析并校验一个非空 strategy_ref。
func (g *StrategyAssetGate) Gate(ctx context.Context, rawRef string) (*backtest.StrategyAssetIdentity, error) {
	ref, err := contracts.ParseStrategyRef(rawRef)
	if err != nil {
		return nil, &GateError{Status: http.StatusBadRequest, Code: "strategy_ref_malformed", Message: err.Error()}
	}

	query := "latest"
	if !ref.IsLatest() {
		query = ref.Hash
	}
	resolved, err := g.resolve(ctx, ref.ID, query)
	if err != nil {
		return nil, err
	}
	if resolved.StrategyID != ref.ID {
		return nil, &GateError{
			Status:  http.StatusBadGateway,
			Code:    "strategy_resolve_mismatch",
			Message: fmt.Sprintf("asset service resolved %q for requested id %q", resolved.StrategyID, ref.ID),
		}
	}
	if !isLowerHex64(resolved.ArtifactHash) || !isLowerHex64(resolved.VersionHash) {
		return nil, &GateError{
			Status:  http.StatusBadGateway,
			Code:    "strategy_resolve_malformed",
			Message: fmt.Sprintf("asset service returned version_hash=%q artifact_hash=%q (want 64 lowercase hex)", resolved.VersionHash, resolved.ArtifactHash),
		}
	}
	if resolved.Status != strategyAssetPromotedStatus {
		return nil, &GateError{
			Status: http.StatusConflict,
			Code:   "strategy_not_promoted",
			Message: fmt.Sprintf("strategy %s version %s artifact %s is %q, want promoted",
				resolved.StrategyID, resolved.VersionHash, resolved.ArtifactHash, resolved.Status),
		}
	}

	// 状态位必须与留痕互相自证：没有 promote 审计行的 promoted 行仍然拒绝
	// （审批人 / 时间 / 结论是 QT-32 ③ 的硬要求，不是一个布尔位能替代的）。
	if err := g.requirePromotionAudit(ctx, ref.ID, resolved); err != nil {
		return nil, err
	}

	return &backtest.StrategyAssetIdentity{
		RefInput:     rawRef,
		StrategyID:   resolved.StrategyID,
		VersionHash:  resolved.VersionHash,
		ArtifactHash: resolved.ArtifactHash,
	}, nil
}

func (g *StrategyAssetGate) requirePromotionAudit(ctx context.Context, strategyID string, resolved strategyResolveResponse) error {
	endpoint := fmt.Sprintf("%s/api/strategies/%s/promotions?version_hash=%s",
		g.baseURL, url.PathEscape(strategyID), url.QueryEscape(resolved.VersionHash))
	body, status, err := g.get(ctx, endpoint)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return &GateError{
			Status:  http.StatusBadGateway,
			Code:    "strategy_promotion_audit_unavailable",
			Message: fmt.Sprintf("promotion audit read failed: status %d: %s", status, errorDetail(body)),
		}
	}
	var events []strategyPromotionEvent
	if err := json.Unmarshal(body, &events); err != nil {
		return &GateError{
			Status:  http.StatusBadGateway,
			Code:    "strategy_promotion_audit_malformed",
			Message: fmt.Sprintf("promotion audit read: decode response: %v", err),
		}
	}
	for _, ev := range events {
		if ev.Outcome != strategyAssetPromotedStatus {
			continue
		}
		if ev.VersionHash != resolved.VersionHash || ev.ArtifactHash != resolved.ArtifactHash {
			continue
		}
		if strings.TrimSpace(ev.Actor) == "" || ev.DecidedAt.IsZero() {
			break // 半截留痕不算账
		}
		return nil
	}
	return &GateError{
		Status: http.StatusConflict,
		Code:   "strategy_promotion_audit_missing",
		Message: fmt.Sprintf("strategy %s version %s artifact %s has no promote audit trail (actor/decided_at)",
			resolved.StrategyID, resolved.VersionHash, resolved.ArtifactHash),
	}
}

func (g *StrategyAssetGate) resolve(ctx context.Context, strategyID, ref string) (strategyResolveResponse, error) {
	endpoint := fmt.Sprintf("%s/api/strategies/%s/resolve?ref=%s",
		g.baseURL, url.PathEscape(strategyID), url.QueryEscape(ref))
	body, status, err := g.get(ctx, endpoint)
	if err != nil {
		return strategyResolveResponse{}, err
	}
	if status != http.StatusOK {
		code := "strategy_resolve_failed"
		httpStatus := http.StatusBadGateway
		if status == http.StatusNotFound {
			code = "strategy_not_found"
			httpStatus = http.StatusNotFound
		}
		return strategyResolveResponse{}, &GateError{
			Status:  httpStatus,
			Code:    code,
			Message: fmt.Sprintf("resolve %s/%s failed: status %d: %s", strategyID, ref, status, errorDetail(body)),
		}
	}
	var out strategyResolveResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return strategyResolveResponse{}, &GateError{
			Status:  http.StatusBadGateway,
			Code:    "strategy_resolve_malformed",
			Message: fmt.Sprintf("resolve response: %v", err),
		}
	}
	return out, nil
}

func (g *StrategyAssetGate) get(ctx context.Context, endpoint string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, &GateError{Status: http.StatusBadGateway, Code: "strategy_asset_request_failed", Message: err.Error()}
	}
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, 0, &GateError{
			Status:  http.StatusServiceUnavailable,
			Code:    "strategy_asset_unavailable",
			Message: fmt.Sprintf("GET %s: %v", endpoint, err),
		}
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, strategyAssetMaxBody))
	if err != nil {
		return nil, 0, &GateError{
			Status:  http.StatusBadGateway,
			Code:    "strategy_asset_read_failed",
			Message: fmt.Sprintf("GET %s: read response: %v", endpoint, err),
		}
	}
	return body, resp.StatusCode, nil
}

// isLowerHex64 判断是否是 64 位小写 hex（与 contracts.ParseStrategyRef 同口径）。
func isLowerHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			continue
		}
		return false
	}
	return true
}
