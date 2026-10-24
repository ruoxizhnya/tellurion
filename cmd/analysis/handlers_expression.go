package main

// handlers_expression.go —— 表达式静态校验 + 算子/字段目录（QT-69）。
//
// 为什么需要这一族：agent 的因子工具链（`validate_factor` / 研究提示词）今天
// 直接 import core 的 `pkg/expression`，那是删 `replace` 的剩余前置之一
// （`tellurion-agent:docs/s3-decoupling-plan.md` §2 #13 / §5 #3）。两条端点
// 把「静态校验」与「目录」搬到 core，求值仍只在引擎进程内：
//
//   POST /api/expression/validate  —— Parse + Expression.Validate（与 core
//       内同一条实现，不复制闸门）；纯计算，不写库，[readonly-post]。
//   GET  /api/expression/catalog   —— OperatorDef 的七项声明 + 字段注册表 +
//       当前可用性；只读，[已认证]。
//
// 「字段不可用 ≠ 返回空」是本族的核心语义（OBS-08 切片 2）：目录对每个字段
// 返回三态 `available` / `unavailable` / `unknown`，并允许用 `?fields=` 点名
// 查询；**未登记的字段返回一条结构化的 unavailable 记录，而不是从数组里
// 悄悄消失**。可用性探测失败（库连不上 / 查询报错）一律 503 fail-closed，
// 不降级成「没有数据」。

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"

	"tellurion/pkg/expression"
	"tellurion/pkg/httpserver"
)

// expressionAvailabilityStore 是字段可用性探测的最小存储面
// （*storage.PostgresStore 满足）。窄接口让 handler 单测不必拖真库。
type expressionAvailabilityStore interface {
	TableHasRows(ctx context.Context, table string) (bool, error)
}

// ExpressionHandler serves the two read-only expression endpoints.
type ExpressionHandler struct {
	store  expressionAvailabilityStore
	logger zerolog.Logger
}

// NewExpressionHandler 构造 handler。store 可为 nil：目录的静态部分仍可读，
// 但每个字段的可用性会如实报 `unknown`（未探测），不会冒充 available。
func NewExpressionHandler(store expressionAvailabilityStore, logger zerolog.Logger) *ExpressionHandler {
	return &ExpressionHandler{
		store:  store,
		logger: logger.With().Str("component", "expression_handler").Logger(),
	}
}

// RegisterRoutes wires the expression endpoints.
func (h *ExpressionHandler) RegisterRoutes(router *gin.Engine) {
	router.POST("/api/expression/validate", h.validate)
	router.GET("/api/expression/catalog", h.catalog)
}

// ─── 校验 ────────────────────────────────────────────────────────────

type expressionValidateRequest struct {
	Expression string `json:"expression"`
}

// expressionValidateResponse 是 200 的形状。`valid` 与 core 内
// `(*expression.Expression).Validate()` 的结论完全一致（parse 失败也算
// invalid —— 调用方拿到的是一份判定，不是 HTTP 4xx 的二选一）。
type expressionValidateResponse struct {
	Valid  bool     `json:"valid"`
	Inputs []string `json:"inputs"`
	AST    string   `json:"ast"`
	Error  string   `json:"error,omitempty"`
	Stage  string   `json:"stage,omitempty"` // parse | gate
}

func (h *ExpressionHandler) validate(c *gin.Context) {
	var req expressionValidateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpserver.Fail(c, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Expression) == "" {
		httpserver.Fail(c, http.StatusBadRequest, "expression is required")
		return
	}

	// Parser 持有可变的 tokens/pos，不并发安全：每次请求新建（与 agent 的
	// validate_factor 同一取向）。
	parsed, err := expression.NewParser().Parse(req.Expression)
	if err != nil {
		c.JSON(http.StatusOK, expressionValidateResponse{
			Valid:  false,
			Inputs: []string{},
			Error:  err.Error(),
			Stage:  "parse",
		})
		return
	}
	if err := parsed.Validate(); err != nil {
		c.JSON(http.StatusOK, expressionValidateResponse{
			Valid:  false,
			Inputs: []string{},
			Error:  err.Error(),
			Stage:  "gate",
		})
		return
	}

	inputs := append([]string(nil), parsed.Inputs...)
	sort.Strings(inputs) // map 派生的 Inputs 顺序不稳定，wire 必须可对拍
	if inputs == nil {
		inputs = []string{}
	}
	c.JSON(http.StatusOK, expressionValidateResponse{
		Valid:  true,
		Inputs: inputs,
		AST:    parsed.AST.String(),
	})
}

// ─── 目录 ────────────────────────────────────────────────────────────

// expressionOperatorSpecDTO 对应 ADR-028 §4 的声明七项。参数化算子的
// Lookback / Warmup 存 0 哨兵（真实值由实参推导）；State=true 时
// Lookback=0 表示 ∞ —— 见 thresholds。
type expressionOperatorSpecDTO struct {
	Name      string `json:"name"`
	Signature string `json:"signature"`
	Lookback  int    `json:"lookback"`
	Causal    bool   `json:"causal"`
	State     bool   `json:"state"`
	Warmup    int    `json:"warmup"`
	Init      string `json:"init"`
	NaNPolicy string `json:"nan_policy"`
}

type expressionOperatorDTO struct {
	Name         string                    `json:"name"`
	Category     string                    `json:"category"`
	Arity        int                       `json:"arity"`
	ScalarParams []int                     `json:"scalar_params"`
	Spec         expressionOperatorSpecDTO `json:"spec"`
}

// expressionFieldDTO 的 Status 三态：
//   - available：已登记，且（探测时）源表有数据 / 无物理表依赖；
//   - unavailable：已登记但源表为空，**或**请求的名字根本未登记
//     （reason 区分 `source_table_empty` / `field_not_registered`）；
//   - unknown：已登记，但服务端没装可用性探测（不冒充 available）。
type expressionFieldDTO struct {
	Name      string `json:"name"`
	Source    string `json:"source"`
	DependsOn string `json:"depends_on,omitempty"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
}

// expressionThresholdsDTO 把注册表里 0 哨兵的语义写成机器可读的约定，
// 避免调用方把「参数化 / ∞」误读成「阈值就是 0」。
type expressionThresholdsDTO struct {
	LookbackUnit      string `json:"lookback_unit"`
	WarmupUnit        string `json:"warmup_unit"`
	ZeroLookbackMeans string `json:"zero_lookback_means"`
	ZeroWarmupMeans   string `json:"zero_warmup_means"`
}

type expressionCatalogResponse struct {
	Availability string                  `json:"availability"` // probed | unknown
	Operators    []expressionOperatorDTO `json:"operators"`
	Fields       []expressionFieldDTO    `json:"fields"`
	Thresholds   expressionThresholdsDTO `json:"thresholds"`
}

const (
	expressionFieldAvailable   = "available"
	expressionFieldUnavailable = "unavailable"
	expressionFieldUnknown     = "unknown"

	reasonSourceTableEmpty   = "source_table_empty"
	reasonFieldNotRegistered = "field_not_registered"
	reasonProbeNotMounted    = "availability_probe_not_mounted"
)

func (h *ExpressionHandler) catalog(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	// availability 三态：probed（真探测过）/ unknown（未装探测）。
	// 探测失败不是「不可用」，是服务故障 ⇒ 503（不降级成「表为空」）。
	avail, probed, err := h.fieldAvailability(ctx)
	if err != nil {
		h.logger.Error().Err(err).Msg("expression catalog availability probe failed")
		httpserver.FailCause(c, http.StatusServiceUnavailable, "expression field availability probe failed", err)
		return
	}

	fields, ok := h.catalogFields(c, avail, probed)
	if !ok {
		return
	}

	c.JSON(http.StatusOK, expressionCatalogResponse{
		Availability: map[bool]string{true: "probed", false: "unknown"}[probed],
		Operators:    catalogOperators(),
		Fields:       fields,
		Thresholds: expressionThresholdsDTO{
			LookbackUnit:      "bars",
			WarmupUnit:        "bars",
			ZeroLookbackMeans: "parameterized (derived from the window argument) or infinite when state=true",
			ZeroWarmupMeans:   "parameterized (derived from the window argument)",
		},
	})
}

// fieldAvailability 返回字段可用性地图；probed=false 表示未装探测。
func (h *ExpressionHandler) fieldAvailability(ctx context.Context) (map[string]bool, bool, error) {
	if h.store == nil {
		return nil, false, nil
	}
	avail, err := expression.FieldAvailability(ctx, h.store.TableHasRows)
	if err != nil {
		return nil, false, err
	}
	return avail, true, nil
}

// catalogFields 组装字段列表。带 `?fields=a,b` 时**只**返回点名的字段，
// 未登记的名字也保留一条 unavailable 记录（结构化，不是空数组）。
func (h *ExpressionHandler) catalogFields(
	c *gin.Context,
	avail map[string]bool,
	probed bool,
) ([]expressionFieldDTO, bool) {
	raw := c.Query("fields")
	if raw == "" {
		names := expression.AvailableFields()
		out := make([]expressionFieldDTO, 0, len(names))
		for _, name := range names {
			out = append(out, catalogField(name, avail, probed))
		}
		return out, true
	}

	parts := strings.Split(raw, ",")
	seen := make(map[string]bool, len(parts))
	out := make([]expressionFieldDTO, 0, len(parts))
	for i, part := range parts {
		name := strings.TrimSpace(part)
		if name == "" {
			httpserver.Failf(c, http.StatusBadRequest, "fields[%d] is empty", i)
			return nil, false
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, catalogField(name, avail, probed))
	}
	return out, true
}

func catalogField(name string, avail map[string]bool, probed bool) expressionFieldDTO {
	source, registered := expression.FieldSourceOf(name)
	if !registered {
		return expressionFieldDTO{
			Name:   name,
			Status: expressionFieldUnavailable,
			Reason: reasonFieldNotRegistered,
		}
	}

	dto := expressionFieldDTO{
		Name:   name,
		Source: string(source),
		Status: expressionFieldUnknown,
		Reason: reasonProbeNotMounted,
	}
	if dependsOn, _ := expression.FieldDependsOn(name); dependsOn != "" {
		dto.DependsOn = dependsOn
	}
	if !probed {
		return dto
	}
	if avail[name] {
		dto.Status = expressionFieldAvailable
		dto.Reason = ""
		return dto
	}
	dto.Status = expressionFieldUnavailable
	dto.Reason = reasonSourceTableEmpty
	return dto
}

// catalogOperators 从注册表派生全部算子（AvailableOperators 已按字典序）。
func catalogOperators() []expressionOperatorDTO {
	names := expression.AvailableOperators()
	out := make([]expressionOperatorDTO, 0, len(names))
	for _, name := range names {
		def := expression.LookupOperator(name)
		if !def.Found {
			// AvailableOperators 与注册表不同步是注册表自身的错误：跳过会让
			// 目录与闸门悄悄分叉，故宁可让目录少一条也不编造；这里由
			// registry_test 的自洽护栏兜底。
			continue
		}
		scalar := append([]int(nil), def.ScalarParams...)
		if scalar == nil {
			scalar = []int{}
		}
		out = append(out, expressionOperatorDTO{
			Name:         def.Name,
			Category:     string(def.Category),
			Arity:        def.Arity,
			ScalarParams: scalar,
			Spec: expressionOperatorSpecDTO{
				Name:      def.Spec.Name,
				Signature: def.Spec.Signature,
				Lookback:  def.Spec.Lookback,
				Causal:    def.Spec.Causal,
				State:     def.Spec.State,
				Warmup:    def.Spec.Warmup,
				Init:      def.Spec.Init,
				NaNPolicy: def.Spec.NaNPolicy,
			},
		})
	}
	return out
}
