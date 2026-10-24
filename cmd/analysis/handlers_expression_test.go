package main

// QT-69 验收腿：
//  1. 校验端点的 valid 结论与 core 内 Parse + Validate **逐例一致**；
//  2. 目录从注册表派生（算子数/声明七项/字段三态）；
//  3. 「字段不可用 ≠ 返回空」：?fields= 点名的未登记字段返回结构化
//     unavailable，而不是被数组吞掉；
//  4. 未装探测 ⇒ unknown（不冒充 available）；探测失败 ⇒ 503（不降级成
//     「没数据」）；
//  5. 鉴权：validate 是 readonly-post，viewer 可过鉴权层。
//
// 破坏验证（本单记录）：把 catalogFields 的未登记分支改成 `continue`
// ⇒ 第 3 条红；把 probe 失败改成返回空 map ⇒ 第 4 条红；把 validate 的
// parsed.Validate() 删掉 ⇒ `CROSS(MA(...))` 那条从 invalid 变 valid ⇒ 第 1 条红。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/auth"
	"tellurion/pkg/expression"
)

type fakeAvailabilityStore struct {
	rows map[string]bool
	err  error
}

func (f fakeAvailabilityStore) TableHasRows(_ context.Context, table string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.rows[table], nil
}

func expressionTestRouter(store expressionAvailabilityStore) *gin.Engine {
	router := gin.New()
	NewExpressionHandler(store, zerolog.Nop()).RegisterRoutes(router)
	return router
}

func postExpressionValidate(t *testing.T, router *gin.Engine, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/expression/validate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func decodeValidate(t *testing.T, w *httptest.ResponseRecorder) expressionValidateResponse {
	t.Helper()
	var out expressionValidateResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out), w.Body.String())
	return out
}

// 1) 校验结论必须等于 core 内 Parse + Validate 的结论。
func TestExpressionValidate_MatchesCoreValidate(t *testing.T) {
	router := expressionTestRouter(nil)

	cases := []string{
		"ts_mean(close, 20)",
		"(close - ts_mean(close, 20)) / ts_std(close, 20)",
		"cs_rank(ts_delta(close, 5))",
		"CROSS(MA(close,5), MA(close,20))",
		"ts_mean(close)",
		"ts_mean(close, volume)",
		"not_a_field + 1",
		"ts_mean(close, ",
	}

	for _, formula := range cases {
		t.Run(formula, func(t *testing.T) {
			// 期望值直接来自 core 的同一条实现。
			wantValid := true
			parsed, err := expression.NewParser().Parse(formula)
			if err != nil {
				wantValid = false
			} else if err := parsed.Validate(); err != nil {
				wantValid = false
			}

			w := postExpressionValidate(t, router, `{"expression":`+quoteJSON(formula)+`}`)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			got := decodeValidate(t, w)

			assert.Equal(t, wantValid, got.Valid, "valid 与 core Validate 不一致（%s）", formula)
			if wantValid {
				assert.Empty(t, got.Error)
				assert.Equal(t, parsed.AST.String(), got.AST)
				require.Len(t, got.Inputs, len(parsed.Inputs))
				assert.True(t, isSorted(got.Inputs), "inputs 必须稳定排序：%v", got.Inputs)
			} else {
				assert.NotEmpty(t, got.Error)
				assert.Contains(t, []string{"parse", "gate"}, got.Stage)
				assert.Empty(t, got.Inputs)
			}
		})
	}
}

func TestExpressionValidate_RejectsMissingExpression(t *testing.T) {
	router := expressionTestRouter(nil)
	for _, body := range []string{`{}`, `{"expression":"   "}`} {
		w := postExpressionValidate(t, router, body)
		assert.Equal(t, http.StatusBadRequest, w.Code, body)
	}
}

// 2)+3) 目录派生与三态；未登记字段必须结构化返回。
func TestExpressionCatalog_FieldsAreThreeStateAndUnknownIsNotDropped(t *testing.T) {
	store := fakeAvailabilityStore{rows: map[string]bool{
		expression.TableOHLCV:          true,
		expression.TableFundamentals:   false,
		expression.TableMarketActivity: true,
		expression.TableSectorMap:      false,
	}}
	router := expressionTestRouter(store)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/expression/catalog?fields=close,pe,nope", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var got expressionCatalogResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "probed", got.Availability)
	require.Len(t, got.Fields, 3, "未登记的名字必须保留一条记录，不能被吞掉：%s", w.Body.String())

	byName := map[string]expressionFieldDTO{}
	for _, f := range got.Fields {
		byName[f.Name] = f
	}
	assert.Equal(t, expressionFieldAvailable, byName["close"].Status)
	assert.Equal(t, "market", byName["close"].Source)
	assert.Equal(t, expression.TableOHLCV, byName["close"].DependsOn)

	assert.Equal(t, expressionFieldUnavailable, byName["pe"].Status)
	assert.Equal(t, reasonSourceTableEmpty, byName["pe"].Reason)

	assert.Equal(t, expressionFieldUnavailable, byName["nope"].Status)
	assert.Equal(t, reasonFieldNotRegistered, byName["nope"].Reason)
	assert.Empty(t, byName["nope"].Source)
}

func TestExpressionCatalog_OperatorsComeFromRegistry(t *testing.T) {
	router := expressionTestRouter(nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/expression/catalog", nil))
	require.Equal(t, http.StatusOK, w.Code)

	var got expressionCatalogResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Len(t, got.Operators, len(expression.AvailableOperators()))

	var tsMean *expressionOperatorDTO
	for i := range got.Operators {
		if got.Operators[i].Name == "ts_mean" {
			tsMean = &got.Operators[i]
		}
	}
	require.NotNil(t, tsMean)
	assert.Equal(t, "ts", tsMean.Category)
	assert.Equal(t, 2, tsMean.Arity)
	assert.Equal(t, []int{1}, tsMean.ScalarParams)
	assert.NotEmpty(t, tsMean.Spec.Signature)
	assert.True(t, tsMean.Spec.Causal)
	assert.False(t, tsMean.Spec.State)

	assert.Equal(t, "bars", got.Thresholds.LookbackUnit)
	assert.NotEmpty(t, got.Thresholds.ZeroLookbackMeans)
}

// 4) 未装探测 ⇒ unknown；探测失败 ⇒ 503（不把故障说成「没有数据」）。
func TestExpressionCatalog_AvailabilityUnknownWithoutProbe(t *testing.T) {
	router := expressionTestRouter(nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/expression/catalog?fields=close", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var got expressionCatalogResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, "unknown", got.Availability)
	require.Len(t, got.Fields, 1)
	assert.Equal(t, expressionFieldUnknown, got.Fields[0].Status)
	assert.Equal(t, reasonProbeNotMounted, got.Fields[0].Reason)
}

func TestExpressionCatalog_ProbeFailureIs503(t *testing.T) {
	router := expressionTestRouter(fakeAvailabilityStore{err: errors.New("db down")})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/expression/catalog", nil))
	assert.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
}

func TestExpressionValidate_ViewerCanUseReadonlyPost(t *testing.T) {
	svc := auth.NewService(nil, auth.Config{JWTSecret: []byte("qt69-policy")})
	require.True(t, svc.Enabled())
	router := policyRouter(t, svc)

	w := policyPost(router, "/api/expression/validate", `{"expression":"ts_mean(close,20)"}`,
		tokenFor(t, svc, auth.RoleViewer))
	assert.NotEqual(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

// ─── 小工具 ──────────────────────────────────────────────────────────

func quoteJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func isSorted(xs []string) bool {
	for i := 1; i < len(xs); i++ {
		if xs[i-1] > xs[i] {
			return false
		}
	}
	return true
}
