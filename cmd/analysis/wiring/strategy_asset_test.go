package wiring

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"
)

const (
	gateTestVersion  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	gateTestArtifact = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// fakeAssetService 是资产服务的替身：只回 resolve / promotions 两个读面。
type fakeAssetService struct {
	status      string
	audit       bool
	resolveCode int
	// resolveHits / auditHits 用于证明「空引用不联网」。
	resolveHits int
	auditHits   int
}

func (f *fakeAssetService) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/strategies/st_ai/resolve", func(w http.ResponseWriter, r *http.Request) {
		f.resolveHits++
		if f.resolveCode != 0 {
			w.WriteHeader(f.resolveCode)
			_, _ = w.Write([]byte(`{"error":"boom","code":"internal"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"strategy_id":   "st_ai",
			"version_hash":  gateTestVersion,
			"artifact_hash": gateTestArtifact,
			"carrier":       "wasm",
			"status":        f.status,
		})
	})
	mux.HandleFunc("/api/strategies/st_ai/promotions", func(w http.ResponseWriter, r *http.Request) {
		f.auditHits++
		if !f.audit {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = fmt.Fprintf(w, `[{"strategy_id":"st_ai","version_hash":%q,"artifact_hash":%q,"actor":"alice","outcome":"promoted","decided_at":"2026-11-10T09:00:00Z"}]`,
			gateTestVersion, gateTestArtifact)
	})
	// 未知 id：模拟资产服务 404。
	mux.HandleFunc("/api/strategies/st_missing/resolve", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found","code":"strategy_not_found"}`))
	})
	return mux
}

func newFakeGate(t *testing.T, f *fakeAssetService) *StrategyAssetGate {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	gate, err := NewStrategyAssetGate(srv.URL, "", 2*time.Second)
	if err != nil {
		t.Fatalf("NewStrategyAssetGate: %v", err)
	}
	return gate
}

func gateErrorCode(t *testing.T, err error) (int, string) {
	t.Helper()
	var ge *GateError
	if !errors.As(err, &ge) {
		t.Fatalf("error %v is not *GateError", err)
	}
	return ge.Status, ge.Code
}

func TestCheckStrategyRef_EmptyRefNeverCallsTheService(t *testing.T) {
	f := &fakeAssetService{}
	gate := newFakeGate(t, f)

	id, err := CheckStrategyRef(context.Background(), gate, "   ")
	if err != nil || id != nil {
		t.Fatalf("empty ref = (%v, %v), want (nil, nil)", id, err)
	}
	// nil 闸门 + 空引用同样放行（内置策略路径不看配置）。
	if id, err := CheckStrategyRef(context.Background(), nil, ""); err != nil || id != nil {
		t.Fatalf("nil gate + empty ref = (%v, %v), want (nil, nil)", id, err)
	}
	if f.resolveHits != 0 || f.auditHits != 0 {
		t.Fatalf("empty ref touched the asset service: resolve=%d audit=%d", f.resolveHits, f.auditHits)
	}
}

func TestCheckStrategyRef_NilGateRejectsNonEmptyRef(t *testing.T) {
	_, err := CheckStrategyRef(context.Background(), nil, "st_ai")
	status, code := gateErrorCode(t, err)
	if status != http.StatusServiceUnavailable || code != "strategy_asset_not_configured" {
		t.Fatalf("nil gate = %d/%s, want 503/strategy_asset_not_configured", status, code)
	}
}

func TestCheckStrategyRef_PromotedWithAudit(t *testing.T) {
	f := &fakeAssetService{status: "promoted", audit: true}
	gate := newFakeGate(t, f)

	ref := "st_ai@" + gateTestVersion
	id, err := CheckStrategyRef(context.Background(), gate, ref)
	if err != nil {
		t.Fatalf("CheckStrategyRef: %v", err)
	}
	if id == nil {
		t.Fatal("identity is nil for a promoted + audited ref")
	}
	want := struct {
		Ref, ID, Version, Artifact string
	}{ref, "st_ai", gateTestVersion, gateTestArtifact}
	if id.RefInput != want.Ref || id.StrategyID != want.ID ||
		id.VersionHash != want.Version || id.ArtifactHash != want.Artifact {
		t.Fatalf("identity = %+v, want %+v", id, want)
	}
	if f.resolveHits != 1 || f.auditHits != 1 {
		t.Fatalf("hits = resolve:%d audit:%d, want 1/1", f.resolveHits, f.auditHits)
	}
}

func TestCheckStrategyRef_RejectsNonPromoted(t *testing.T) {
	for _, status := range []string{"built", "validated", "rejected", "deprecated"} {
		t.Run(status, func(t *testing.T) {
			f := &fakeAssetService{status: status, audit: true}
			gate := newFakeGate(t, f)
			_, err := CheckStrategyRef(context.Background(), gate, "st_ai")
			code, detail := gateErrorCode(t, err)
			if code != http.StatusConflict || detail != "strategy_not_promoted" {
				t.Fatalf("%s = %d/%s, want 409/strategy_not_promoted", status, code, detail)
			}
			if f.auditHits != 0 {
				t.Fatal("non-promoted asset must be rejected before the audit read")
			}
		})
	}
}

func TestCheckStrategyRef_RejectsMissingAuditTrail(t *testing.T) {
	f := &fakeAssetService{status: "promoted", audit: false}
	gate := newFakeGate(t, f)
	_, err := CheckStrategyRef(context.Background(), gate, "st_ai")
	status, code := gateErrorCode(t, err)
	if status != http.StatusConflict || code != "strategy_promotion_audit_missing" {
		t.Fatalf("missing audit = %d/%s, want 409/strategy_promotion_audit_missing", status, code)
	}
}

func TestCheckStrategyRef_ResolveNotFound(t *testing.T) {
	f := &fakeAssetService{status: "promoted", audit: true}
	gate := newFakeGate(t, f)
	_, err := CheckStrategyRef(context.Background(), gate, "st_missing")
	status, code := gateErrorCode(t, err)
	if status != http.StatusNotFound || code != "strategy_not_found" {
		t.Fatalf("missing asset = %d/%s, want 404/strategy_not_found", status, code)
	}
}

func TestCheckStrategyRef_Resolve5xxIsBadGateway(t *testing.T) {
	f := &fakeAssetService{status: "promoted", audit: true, resolveCode: http.StatusInternalServerError}
	gate := newFakeGate(t, f)
	_, err := CheckStrategyRef(context.Background(), gate, "st_ai")
	status, code := gateErrorCode(t, err)
	if status != http.StatusBadGateway || code != "strategy_resolve_failed" {
		t.Fatalf("5xx = %d/%s, want 502/strategy_resolve_failed", status, code)
	}
}

func TestCheckStrategyRef_MalformedRef(t *testing.T) {
	gate := newFakeGate(t, &fakeAssetService{status: "promoted", audit: true})
	for _, raw := range []string{"st_ai@deadbeef", "st_ai@a@b", "@" + gateTestVersion} {
		_, err := CheckStrategyRef(context.Background(), gate, raw)
		status, code := gateErrorCode(t, err)
		if status != http.StatusBadRequest || code != "strategy_ref_malformed" {
			t.Fatalf("%q = %d/%s, want 400/strategy_ref_malformed", raw, status, code)
		}
	}
}

func TestLoadStrategyAssetGate(t *testing.T) {
	logger := zerolog.Nop()

	t.Run("disabled_returns_nil", func(t *testing.T) {
		v := viper.New()
		v.Set("strategy_asset.resolve_enabled", false)
		gate, err := LoadStrategyAssetGate(v, logger)
		if err != nil || gate != nil {
			t.Fatalf("disabled = (%v, %v), want (nil, nil)", gate, err)
		}
	})

	t.Run("enabled_without_url_fails_fast", func(t *testing.T) {
		v := viper.New()
		v.Set("strategy_asset.resolve_enabled", true)
		if _, err := LoadStrategyAssetGate(v, logger); err == nil {
			t.Fatal("enabled + empty url should fail fast")
		}
	})

	t.Run("enabled_builds_gate", func(t *testing.T) {
		v := viper.New()
		v.Set("strategy_asset.resolve_enabled", true)
		v.Set("strategy_asset.resolve_url", "http://127.0.0.1:18090/")
		gate, err := LoadStrategyAssetGate(v, logger)
		if err != nil || gate == nil {
			t.Fatalf("enabled = (%v, %v), want a gate", gate, err)
		}
		if strings.HasSuffix(gate.baseURL, "/") {
			t.Fatalf("baseURL %q must be trimmed", gate.baseURL)
		}
	})
}
