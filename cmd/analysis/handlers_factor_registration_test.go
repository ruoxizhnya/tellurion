package main

// QT-50 ③ 第 6 类动作的 handler 行为：因子登记的四样校验、哈希派生与
// 登记人来源（JWT 优先、匿名拒收）。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/auth"
)

type fakeRegStore struct {
	saved []domain.FactorRegistration
	err   error
}

func (f *fakeRegStore) SaveFactorRegistration(_ context.Context, reg domain.FactorRegistration) error {
	if f.err != nil {
		return f.err
	}
	f.saved = append(f.saved, reg)
	return nil
}

func regRouter(t *testing.T, store factorRegistrationStore, svc *auth.Service) *gin.Engine {
	t.Helper()
	r := gin.New()
	if svc != nil {
		r.Use(svc.Middleware())
	}
	NewFactorRegistrationHandler(store, svc, zerolog.Nop()).RegisterRoutes(r)
	return r
}

func validDefinition(t *testing.T) domain.FactorDefinition {
	t.Helper()
	for _, d := range domain.BuiltinFactorDefinitions {
		if d.Kernel == domain.KernelFormula || d.Kernel == domain.KernelLeaf {
			return d
		}
	}
	t.Fatal("内置因子定义里没有 leaf/formula 型")
	return domain.FactorDefinition{}
}

func postRegistration(t *testing.T, r *gin.Engine, body any, token string) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/factor/registrations", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestFactorRegistration_StoreUnavailable(t *testing.T) {
	t.Parallel()
	r := regRouter(t, nil, nil)
	w := postRegistration(t, r, map[string]any{"registrar": "local"}, "")
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestFactorRegistration_InvalidDefinition(t *testing.T) {
	t.Parallel()
	store := &fakeRegStore{}
	r := regRouter(t, store, nil)

	w := postRegistration(t, r, map[string]any{
		"definition": map[string]any{},
		"registrar":  "local",
	}, "")
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Empty(t, store.saved, "校验失败不得落库（fail-closed）")
}

func TestFactorRegistration_RegistrarRequiredWhenAuthDisabled(t *testing.T) {
	t.Parallel()
	store := &fakeRegStore{}
	r := regRouter(t, store, nil)

	w := postRegistration(t, r, map[string]any{"definition": validDefinition(t)}, "")
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "registrar")
	assert.Empty(t, store.saved, "匿名登记一律拒收")
}

func TestFactorRegistration_DerivesHashAndSaves(t *testing.T) {
	t.Parallel()
	store := &fakeRegStore{}
	r := regRouter(t, store, nil)

	def := validDefinition(t)
	w := postRegistration(t, r, map[string]any{
		"definition": def,
		"registrar":  "local-dev",
	}, "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, store.saved, 1)

	saved := store.saved[0]
	assert.Equal(t, def.VersionHash(), saved.VersionHash,
		"version_hash 必须由服务端从定义派生，客户端不能自带")
	assert.Equal(t, "local-dev", saved.Registrar)
	assert.Equal(t, domain.RegistrationActive, saved.Status)
	assert.False(t, saved.RegisteredAt.IsZero())

	var resp registerFactorResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, def.Name, resp.Factor)
	assert.Equal(t, saved.VersionHash, resp.VersionHash)
}

func TestFactorRegistration_RegistrarComesFromJWT(t *testing.T) {
	t.Parallel()
	store := &fakeRegStore{}
	svc := auth.NewService(nil, auth.Config{JWTSecret: []byte("qt50-reg")})
	r := regRouter(t, store, svc)

	w := postRegistration(t, r, map[string]any{
		"definition": validDefinition(t),
		"registrar":  "someone-else", // 鉴权开启时这个字段必须被忽略
	}, tokenFor(t, svc, auth.RoleAdmin))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, store.saved, 1)
	assert.Equal(t, string(auth.RoleAdmin), store.saved[0].Registrar,
		"鉴权开启时登记人只信 JWT，不接受客户端自报")
}

func TestFactorRegistration_NoTokenUnauthorized(t *testing.T) {
	t.Parallel()
	store := &fakeRegStore{}
	svc := auth.NewService(nil, auth.Config{JWTSecret: []byte("qt50-reg")})
	r := regRouter(t, store, svc)

	w := postRegistration(t, r, map[string]any{
		"definition": validDefinition(t),
		"registrar":  "x",
	}, "")
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Empty(t, store.saved)
}
