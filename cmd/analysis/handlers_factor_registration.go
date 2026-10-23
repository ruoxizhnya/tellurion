package main

// handlers_factor_registration.go —— 因子登记写入门（QT-50 ②/③ 的第 6 类
// 粗粒度动作）。
//
// 为什么需要这个端点：D24 允许 agent「因子登记」，而在此之前这条能力只有
// `cmd/data` 的挖矿 CLI 在用（`storage.SaveFactorRegistration`），wire 面上
// 没有门。agent 若绕到 data-service 或直连库，就越过了「agent→core 只走
// HTTP 粗粒度」的边界（ADR-D24 / QT-50）。
//
// 口径（与 D4 治理账本同源，不另起一套）：
//   - **四样齐全**由 `domain.FactorRegistration.Validate()` 在校验一次后
//     fail-closed 拒收（公式 / 哈希 / 登记人 / 生效日）；
//   - `version_hash` **由服务端从定义派生**（`Definition.VersionHash()`），
//     客户端不能自带哈希——自带就是让「哈希与内容不一致」有机会进库；
//   - 登记人取自 JWT（`auth.CtxUsername`），**不接受**已认证客户端自报；
//     只有鉴权关闭的本地开发态才回落到请求体的 `registrar`，且仍非空才收；
//   - 幂等：同一 `(factor_name, version_hash)` 重复登记是 no-op（由存储层
//     `ON CONFLICT DO NOTHING` 保证），客户端可安全重试。
//
// 敏感写：管理员档（`route_policy.go` 的 Admin），因为它改变所有后续回测
// 能引用到的因子定义集合。

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-contracts/domain"

	"tellurion/pkg/auth"
	"tellurion/pkg/httpserver"
)

// factorRegistrationStore 是写入门所需的最小存储能力（*storage.PostgresStore
// 满足）。窄接口让 handler 单测不必拖真库。
type factorRegistrationStore interface {
	SaveFactorRegistration(ctx context.Context, reg domain.FactorRegistration) error
}

// FactorRegistrationHandler serves POST /api/factor/registrations.
type FactorRegistrationHandler struct {
	store  factorRegistrationStore
	auth   *auth.Service
	logger zerolog.Logger
}

// NewFactorRegistrationHandler 构造写入门。store 可为 nil（零值 ServerDeps
// 下路由照常注册，请求时 503）——与 signals / industry 等 handler 同款约束。
func NewFactorRegistrationHandler(store factorRegistrationStore, authSvc *auth.Service, logger zerolog.Logger) *FactorRegistrationHandler {
	return &FactorRegistrationHandler{
		store:  store,
		auth:   authSvc,
		logger: logger.With().Str("component", "factor_registration_handler").Logger(),
	}
}

// RegisterRoutes wires the factor-registration write door.
func (h *FactorRegistrationHandler) RegisterRoutes(router *gin.Engine) {
	router.POST("/api/factor/registrations", h.register)
}

// registerFactorRequest 是请求体：
//
//	{"definition": {...domain.FactorDefinition...}, "registrar": "local-dev"}
//
// `registrar` 只在鉴权关闭时被采纳（见文件头）；鉴权开启时一律取 JWT 身份。
type registerFactorRequest struct {
	Definition domain.FactorDefinition `json:"definition"`
	Registrar  string                  `json:"registrar"`
}

// registerFactorResponse 是成功应答（摘要，不回整份定义快照——快照在库里）。
type registerFactorResponse struct {
	Factor       domain.FactorType         `json:"factor"`
	VersionHash  string                    `json:"version_hash"`
	Registrar    string                    `json:"registrar"`
	Status       domain.RegistrationStatus `json:"status"`
	RegisteredAt time.Time                 `json:"registered_at"`
}

func (h *FactorRegistrationHandler) register(c *gin.Context) {
	if h.store == nil {
		httpserver.Fail(c, http.StatusServiceUnavailable, "factor registration store unavailable")
		return
	}

	var req registerFactorRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpserver.Fail(c, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	registrar := h.registrarOf(c, req.Registrar)
	if registrar == "" {
		httpserver.Fail(c, http.StatusBadRequest,
			"registrar missing: send it in the body only when auth is disabled (open-access dev mode)")
		return
	}

	reg := domain.FactorRegistration{
		Definition:   req.Definition,
		VersionHash:  req.Definition.VersionHash(),
		Registrar:    registrar,
		Status:       domain.RegistrationActive,
		RegisteredAt: time.Now().UTC(),
	}
	// 先校验再落库：四样缺任一样是**客户端**问题（400），不是 500。
	if err := reg.Validate(); err != nil {
		httpserver.Fail(c, http.StatusBadRequest, err.Error())
		return
	}

	if err := h.store.SaveFactorRegistration(c.Request.Context(), reg); err != nil {
		h.logger.Error().Err(err).Str("factor", string(reg.Definition.Name)).Msg("save factor registration failed")
		httpserver.Error(c, http.StatusInternalServerError, err)
		return
	}

	h.logger.Info().
		Str("factor", string(reg.Definition.Name)).
		Str("version_hash", reg.VersionHash).
		Str("registrar", reg.Registrar).
		Msg("factor registration accepted")

	c.JSON(http.StatusOK, registerFactorResponse{
		Factor:       reg.Definition.Name,
		VersionHash:  reg.VersionHash,
		Registrar:    reg.Registrar,
		Status:       reg.Status,
		RegisteredAt: reg.RegisteredAt,
	})
}

// registrarOf 解析登记人：鉴权开启时**只信 JWT**；关闭时才采纳请求体，
// 且仍要求非空（匿名登记一律拒收）。
func (h *FactorRegistrationHandler) registrarOf(c *gin.Context, bodyRegistrar string) string {
	if h.auth != nil && h.auth.Enabled() {
		if username := strings.TrimSpace(c.GetString(auth.CtxUsername)); username != "" {
			return username
		}
		return ""
	}
	return strings.TrimSpace(bodyRegistrar)
}
