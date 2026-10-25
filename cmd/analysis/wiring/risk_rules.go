package wiring

// risk_rules.go —— QT-66 的 risk 规则快照取数接线（裁-10 / 裁-11）。
//
// 修的是什么：`quant.run_provenance.risk_rules_hash` 在 QT-63 之后仍恒写空串
// （`pkg/backtest/provenance.go`），「同 hash 不同结果」的最后一列没有兑现。
// 本文件把「按 run 取一次 hash」做成 provider：
//
//  1. `HTTPRiskRulesProvider` —— run 提交时调 risk 仓的
//     `POST /api/risk/resolve`；run_spec 钉了 hash 就按锁定 hash 解析，
//     否则 `{"ruleset":"latest"}`。core 不按规则文件重算（第二真相）。
//  2. `AttachRiskRulesFromConfig` —— 主引擎（`StrategyRuntimeModule.Init`）与
//     walk-forward 的窗口引擎（`cmd/analysis` 的 newEngine 工厂）都经这
//     **同一个装配函数**接线；漏一处的路径会退回「恒空串」而没人发现。
//
// 配置（`risk_service.*`）：
//   - `resolve_enabled`：显式开关。false = 本部署未接入 risk 服务，
//     provenance 记空串 + `not_configured`（裁-11 唯一允许空串的情形）。
//   - `resolve_url`：risk 服务基址（必填；**不**回落到 `risk_service.url`，
//     后者指向 analysis 自己的进程内风控，没有该端点）。
//   - `resolve_token`：服务间凭证（Bearer）。risk 开 secret 后必填，否则 401。
//   - `resolve_timeout`：单次 resolve 超时，缺省 10s。
//
// 配置了但取不到（no_approved_rules / snapshot_not_found / 5xx / 超时）
// 一律失败 —— 引擎侧转成 `ErrRiskRulesUnavailable`，run = failed。

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
	"github.com/spf13/viper"

	"tellurion/pkg/backtest"
)

const (
	// riskResolvePath 是 risk 仓的规则集引用解析端点（X3b）。
	riskResolvePath = "/api/risk/resolve"

	defaultRiskRulesResolveTimeout = 10 * time.Second
	riskResolveMaxBody             = 1 << 20 // 1 MiB：快照元数据不该比这更大
)

// HTTPRiskRulesProvider 是 `backtest.RiskRulesProvider` 的 HTTP 实现。
type HTTPRiskRulesProvider struct {
	endpoint string
	token    string
	client   *http.Client
}

// NewHTTPRiskRulesProvider 构造 provider（不带凭证）。baseURL 只允许 http/https。
func NewHTTPRiskRulesProvider(baseURL string, timeout time.Duration) (*HTTPRiskRulesProvider, error) {
	return NewHTTPRiskRulesProviderWithToken(baseURL, "", timeout)
}

// NewHTTPRiskRulesProviderWithToken 构造带 `Authorization: Bearer` 的 provider。
//
// 为什么必须有：risk 仓的 `POST /api/risk/resolve` 在 policy 表里是
// trader|admin（auth.go 的 requireAuth 一律 fail-closed）。risk 一旦配了
// secret，不带凭证的调用恒 401 ⇒ 裁-11 口径下每个 run 直接 failed。
// token 为空时不发头（回环 + 无 secret 的本地开发仍免密）。
func NewHTTPRiskRulesProviderWithToken(baseURL, token string, timeout time.Duration) (*HTTPRiskRulesProvider, error) {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("risk resolve: invalid base URL %q (want http(s)://host[:port])", baseURL)
	}
	if timeout <= 0 {
		timeout = defaultRiskRulesResolveTimeout
	}
	return &HTTPRiskRulesProvider{
		endpoint: base + riskResolvePath,
		token:    strings.TrimSpace(token),
		client:   &http.Client{Timeout: timeout},
	}, nil
}

type riskResolveRequest struct {
	Hash    string `json:"hash,omitempty"`
	Ruleset string `json:"ruleset,omitempty"`
}

type riskResolveResponse struct {
	RiskRulesHash string `json:"risk_rules_hash"`
}

type riskResolveError struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// ResolveRiskRulesHash 实现 backtest.RiskRulesProvider。
//
// 钉 hash 与 latest 二选一，形态与 risk 仓 `rule.ResolveRequest` 一致；
// 响应里的 hash 必须是 64 位小写 hex（risk 的 DDL CHECK 同口径），
// 形状不符即失败 —— 不做「拿一个像 hash 的串顶上」。
func (p *HTTPRiskRulesProvider) ResolveRiskRulesHash(ctx context.Context, pinnedHash string) (string, error) {
	req := riskResolveRequest{Ruleset: "latest"}
	if pin := strings.TrimSpace(pinnedHash); pin != "" {
		req = riskResolveRequest{Hash: pin}
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("risk resolve: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return "", fmt.Errorf("risk resolve: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.token)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("risk resolve: POST %s: %w", p.endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, riskResolveMaxBody))
	if err != nil {
		return "", fmt.Errorf("risk resolve: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("risk resolve: POST %s: status %d: %s", p.endpoint, resp.StatusCode, errorDetail(body))
	}

	var out riskResolveResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("risk resolve: decode response: %w", err)
	}
	if !isRiskRulesHash(out.RiskRulesHash) {
		return "", fmt.Errorf("risk resolve: response risk_rules_hash %q is not 64 lowercase hex", out.RiskRulesHash)
	}
	return out.RiskRulesHash, nil
}

// errorDetail 把 risk 的错误体折成「code: message」，没有 code 就回原文。
func errorDetail(body []byte) string {
	var e riskResolveError
	if err := json.Unmarshal(body, &e); err == nil && e.Code != "" {
		return strings.TrimSpace(e.Code + ": " + e.Error)
	}
	text := strings.TrimSpace(string(body))
	if text == "" {
		return "(empty body)"
	}
	return text
}

// isRiskRulesHash 与 risk 仓 `rule_snapshots.risk_rules_hash` 的 CHECK 同口径。
func isRiskRulesHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// AttachRiskRulesFromConfig 是 QT-66 的唯一装配函数（主引擎与窗口引擎共用）。
//
// 返回 error 的只有「显式启用但配置不合法」：把坏配置推迟到第一次 run
// 才暴露，等于让整批回测在提交时集体失败。
func AttachRiskRulesFromConfig(eng *backtest.Engine, v *viper.Viper, logger zerolog.Logger) error {
	if eng == nil {
		return nil
	}
	if v == nil || !v.GetBool("risk_service.resolve_enabled") {
		logger.Info().
			Str("risk_rules_status", "not_configured").
			Msg("risk rules resolve is not configured — provenance.risk_rules_hash will be empty (not_configured)")
		eng.SetRiskRulesProvider(nil)
		return nil
	}

	base := strings.TrimSpace(v.GetString("risk_service.resolve_url"))
	if base == "" {
		// 刻意**不**回落到 `risk_service.url`：那一项指向 analysis 自己
		// （:8085，P1-15 合并后的进程内风控），它没有 `/api/risk/resolve`。
		// 回落到那里等于把「少配一项」变成「每个 run 都 404 → failed」，
		// 且失败点推迟到提交时才出现。宁可装配期直接拒。
		return fmt.Errorf("risk_service.resolve_enabled=true 但 risk_service.resolve_url 为空" +
			"（tellurion-risk 的默认地址是 http://localhost:8086；risk_service.url 是 analysis 自己的进程内风控，不能用于 resolve）")
	}
	timeout := v.GetDuration("risk_service.resolve_timeout")
	if timeout <= 0 {
		timeout = defaultRiskRulesResolveTimeout
	}

	provider, err := NewHTTPRiskRulesProviderWithToken(base, v.GetString("risk_service.resolve_token"), timeout)
	if err != nil {
		return err
	}
	eng.SetRiskRulesProvider(provider)
	logger.Info().
		Str("endpoint", base+riskResolvePath).
		Dur("timeout", timeout).
		Bool("with_token", strings.TrimSpace(v.GetString("risk_service.resolve_token")) != "").
		Msg("risk rules resolve provider attached (QT-66)")
	return nil
}
