package main

// QT-32 K11：回测入口的 AI 产物闸门（handler 腿）。
//
// 这里只证入口行为：闸门拒绝发生在任何执行之前，且拒绝原因带状态码原样回给
// 调用方。闸门本身的解析/审计细节在 `cmd/analysis/wiring` 的用例里。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-contracts/engine"

	"tellurion/cmd/analysis/wiring"
	"tellurion/pkg/backtest"
)

const (
	gateHandlerVersion  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	gateHandlerArtifact = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// newGateAssetServer 是资产服务替身：只回 resolve / promotions。
func newGateAssetServer(t *testing.T, status string, audited bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/strategies/st_ai/resolve", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"strategy_id":   "st_ai",
			"version_hash":  gateHandlerVersion,
			"artifact_hash": gateHandlerArtifact,
			"carrier":       "wasm",
			"status":        status,
		})
	})
	mux.HandleFunc("/api/strategies/st_ai/promotions", func(w http.ResponseWriter, r *http.Request) {
		if !audited {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = fmt.Fprintf(w, `[{"strategy_id":"st_ai","version_hash":%q,"artifact_hash":%q,"actor":"alice","outcome":"promoted","decided_at":"2026-11-10T09:00:00Z"}]`,
			gateHandlerVersion, gateHandlerArtifact)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func postBacktestWithGate(t *testing.T, gate *wiring.StrategyAssetGate, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := gin.New()
	// 零值引擎：闸门拒绝的腿根本不进引擎；通过腿不在本文件。
	eng := &backtest.Engine{}
	reg := contracts.NewEngineRegistry()
	reg.Register(contracts.DefaultKernel, eng)
	registerBacktestRoutes(r, eng, reg, nil, nil, nil, nil, zerolog.Nop(), gate)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/backtest", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

const gatedBacktestBody = `{"strategy":"momentum","stock_pool":["600000.SH"],"start_date":"2024-01-01","end_date":"2024-03-31","strategy_ref":"st_ai"}`

func TestBacktestRoute_RejectsNonPromotedStrategyRef(t *testing.T) {
	srv := newGateAssetServer(t, "validated", true)
	gate, err := wiring.NewStrategyAssetGate(srv.URL, "", 2*time.Second)
	if err != nil {
		t.Fatalf("NewStrategyAssetGate: %v", err)
	}

	w := postBacktestWithGate(t, gate, gatedBacktestBody)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409（响应体 %s）", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "promoted") {
		t.Fatalf("rejection must name the promotion gate, body=%s", w.Body.String())
	}
}

func TestBacktestRoute_RejectsMissingPromotionAudit(t *testing.T) {
	srv := newGateAssetServer(t, "promoted", false)
	gate, err := wiring.NewStrategyAssetGate(srv.URL, "", 2*time.Second)
	if err != nil {
		t.Fatalf("NewStrategyAssetGate: %v", err)
	}

	w := postBacktestWithGate(t, gate, gatedBacktestBody)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409（响应体 %s）", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "audit") {
		t.Fatalf("rejection must name the audit trail, body=%s", w.Body.String())
	}
}

func TestBacktestRoute_StrategyRefWithoutGateIsUnavailable(t *testing.T) {
	w := postBacktestWithGate(t, nil, gatedBacktestBody)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503（响应体 %s）", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "strategy_ref") {
		t.Fatalf("rejection must name strategy_ref, body=%s", w.Body.String())
	}
}

// 反证腿：空 strategy_ref 不碰闸门（nil 闸门也不 503）——内置策略路径不变。
func TestBacktestRoute_EmptyStrategyRefBypassesGate(t *testing.T) {
	const body = `{"strategy":"momentum","stock_pool":["600000.SH"],"start_date":"2024-13-99","end_date":"2024-01-31"}`
	w := postBacktestWithGate(t, nil, body)
	// 日期非法 ⇒ 引擎类别映射成 400；关键是**不是** 503/409，证明闸门没被触发。
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400（空 strategy_ref 必须绕过闸门，响应体 %s）", w.Code, w.Body.String())
	}
}
