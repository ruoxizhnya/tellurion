package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
)

// fakeReviewSource 是聚合读面的取数源替身。
type fakeReviewSource struct {
	name string
	n    int
	err  error
}

func (f fakeReviewSource) Name() string { return f.name }

func (f fakeReviewSource) Pending(ctx context.Context) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	return f.n, nil
}

func newReviewPendingRouter(sources []reviewPendingSource) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	NewReviewPendingHandler(sources, zerolog.Nop()).RegisterRoutes(router)
	return router
}

func getReviewPending(t *testing.T, router *gin.Engine) reviewPendingResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/review/pending", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/review/pending = %d, want 200 body=%s", w.Code, w.Body.String())
	}
	var resp reviewPendingResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应解码失败：%v body=%s", err, w.Body.String())
	}
	return resp
}

// 全部源可用：总数是各源之和，且真的 0 也要给出数字（0 与「不知道」必须能区分）。
func TestReviewPending_AllSourcesAvailable(t *testing.T) {
	router := newReviewPendingRouter([]reviewPendingSource{
		fakeReviewSource{name: "ai_explore", n: 12},
		fakeReviewSource{name: "asset_promotions", n: 3},
	})
	resp := getReviewPending(t, router)
	if resp.Pending == nil || *resp.Pending != 15 {
		t.Fatalf("pending = %v, want 15", resp.Pending)
	}
	if len(resp.Sources) != 2 || resp.Sources[0].Status != "available" || resp.Sources[1].Status != "available" {
		t.Fatalf("sources 状态不对：%+v", resp.Sources)
	}

	// 全源可用且都是 0：pending 必须是 0（指针非 nil），不是 null。
	resp = getReviewPending(t, newReviewPendingRouter([]reviewPendingSource{fakeReviewSource{name: "ai_explore"}}))
	if resp.Pending == nil || *resp.Pending != 0 {
		t.Fatalf("全源可用且无待审时 pending 应为 0，got %v", resp.Pending)
	}
}

// 任一源未知 ⇒ 总数不可信，宁可 null 也不给偏小的数（谎报）。
func TestReviewPending_AnySourceUnavailableYieldsNullTotal(t *testing.T) {
	router := newReviewPendingRouter([]reviewPendingSource{
		fakeReviewSource{name: "ai_explore", n: 12},
		fakeReviewSource{name: "asset_promotions", err: errors.New("connection refused")},
	})
	resp := getReviewPending(t, router)
	if resp.Pending != nil {
		t.Fatalf("有源不可用时 pending 必须为 null，got %v", *resp.Pending)
	}
	if len(resp.Sources) != 2 {
		t.Fatalf("sources = %d, want 2", len(resp.Sources))
	}
	unavailable := resp.Sources[1]
	if unavailable.Status != "unavailable" || unavailable.Pending != nil || unavailable.Error == "" {
		t.Fatalf("不可用源必须带原因且 pending=null：%+v", unavailable)
	}
}

// 没配置任何源：pending null + 空 sources（UI 显示「—」，不是 0）。
func TestReviewPending_NoSourcesConfigured(t *testing.T) {
	resp := getReviewPending(t, newReviewPendingRouter(nil))
	if resp.Pending != nil {
		t.Fatalf("无源时 pending 必须为 null，got %v", *resp.Pending)
	}
	if len(resp.Sources) != 0 {
		t.Fatalf("无源时 sources 应为空：%+v", resp.Sources)
	}
}

// 上游客户端：契约形状、非 200、缺字段、负数都必须报错（由聚合层转 unavailable）。
func TestAIReviewPendingSource_UpstreamContract(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantN   int
		wantErr bool
	}{
		{name: "ok", status: 200, body: `{"pending":7,"as_of":"2026-11-12T00:00:00Z"}`, wantN: 7},
		{name: "zero", status: 200, body: `{"pending":0}`, wantN: 0},
		{name: "missing field", status: 200, body: `{}`, wantErr: true},
		{name: "negative", status: 200, body: `{"pending":-1}`, wantErr: true},
		{name: "not implemented yet", status: 404, body: `{"error":"not found"}`, wantErr: true},
		{name: "unavailable", status: 503, body: ``, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != reviewPendingPath {
					t.Errorf("upstream path = %s, want %s", r.URL.Path, reviewPendingPath)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			src := newAIReviewPendingSource(srv.URL)
			if src == nil {
				t.Fatal("source 不应为 nil")
			}
			n, err := src.Pending(context.Background())
			if tc.wantErr {
				if err == nil {
					t.Fatalf("期望错误，got n=%d", n)
				}
				return
			}
			if err != nil {
				t.Fatalf("期望成功，got err=%v", err)
			}
			if n != tc.wantN {
				t.Fatalf("pending = %d, want %d", n, tc.wantN)
			}
		})
	}
}

// 未配置地址 ⇒ 不构造源（读面照常注册，返回 null）。
func TestAIReviewPendingSource_EmptyBaseURL(t *testing.T) {
	if src := newAIReviewPendingSource("  "); src != nil {
		t.Fatalf("空地址应返回 nil，got %+v", src)
	}
}
