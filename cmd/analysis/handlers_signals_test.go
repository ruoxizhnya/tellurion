package main

// K8（TASKS K8 ③.4）：POST /api/signals 写入门的契约测试。
//
// 覆盖写入门的 fail-loud 口径：全批校验先于落库（单条坏 → 400 整批拒绝、
// 带索引）、未来 as_of 拒绝（DayOf 口径）、空批 400、store 缺失 503、
// 正常批 201 + accepted 计数。「写入 → 消费下单」的端到端在
// signals_e2e_test.go。

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"

	"tellurion/pkg/strategy"
)

func newSignalsTestRouter(store strategy.SignalStore) *gin.Engine {
	// gin 的 mode 由 TestMain 设一次（AUD-12）—— 这里不调 gin.SetMode。
	router := gin.New()
	NewSignalsHandler(store, zerolog.Nop()).RegisterRoutes(router)
	return router
}

func postSignals(t *testing.T, router *gin.Engine, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/signals", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestSignalsWriteDoor_AcceptsBatch(t *testing.T) {
	store := &memorySignalStore{}
	router := newSignalsTestRouter(store)

	// as_of 取过去日期（2024-01-10 / 2024-01-15），写入门只拒未来。
	w := postSignals(t, router, `{"signals":[
		{"model_id":"ml-model","symbol":"600000.SH","direction":"long","strength":0.8,"as_of":"2024-01-10T00:00:00Z"},
		{"model_id":"ml-model","symbol":"600000.SH","direction":"close","strength":0.5,"as_of":"2024-01-15T00:00:00Z"}
	]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("合法批应 201，got %d body=%s", w.Code, w.Body.String())
	}
	var resp createSignalsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应解码失败：%v", err)
	}
	if resp.Accepted != 2 {
		t.Fatalf("accepted 计数漂移：%d, want 2", resp.Accepted)
	}
	if len(store.signals) != 2 {
		t.Fatalf("落库条数漂移：%d, want 2", len(store.signals))
	}
}

func TestSignalsWriteDoor_RejectsBadItemIndexed(t *testing.T) {
	store := &memorySignalStore{}
	router := newSignalsTestRouter(store)

	// signals[1] direction=hold 非法 —— 整批拒绝且索引可见，不落任何行
	//（全批校验先于落库，无半批脏写）。
	w := postSignals(t, router, `{"signals":[
		{"model_id":"ml-model","symbol":"600000.SH","direction":"long","strength":0.8,"as_of":"2024-01-10T00:00:00Z"},
		{"model_id":"ml-model","symbol":"600000.SH","direction":"hold","strength":0.5,"as_of":"2024-01-15T00:00:00Z"}
	]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("坏条目应 400，got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "signals[1]") {
		t.Fatalf("错误消息应带索引 signals[1]，got %s", w.Body.String())
	}
	if len(store.signals) != 0 {
		t.Fatalf("坏批不得落库任何行，got %d", len(store.signals))
	}
}

func TestSignalsWriteDoor_RejectsFutureAsOf(t *testing.T) {
	store := &memorySignalStore{}
	router := newSignalsTestRouter(store)

	future := time.Now().AddDate(0, 0, 1).Format(time.RFC3339)
	w := postSignals(t, router, `{"signals":[
		{"model_id":"ml-model","symbol":"600000.SH","direction":"long","strength":0.8,"as_of":"`+future+`"}
	]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("未来 as_of 应 400，got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "signals[0]") {
		t.Fatalf("错误消息应带索引 signals[0]，got %s", w.Body.String())
	}
	if len(store.signals) != 0 {
		t.Fatalf("未来信号不得落库，got %d", len(store.signals))
	}
}

func TestSignalsWriteDoor_RejectsEmptyBatch(t *testing.T) {
	router := newSignalsTestRouter(&memorySignalStore{})
	w := postSignals(t, router, `{"signals":[]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("空批应 400，got %d body=%s", w.Code, w.Body.String())
	}
}

func TestSignalsWriteDoor_StoreUnavailable(t *testing.T) {
	// store 缺失（未接库）时路由照常注册、请求时 503 —— 不 panic 不 404。
	router := newSignalsTestRouter(nil)
	w := postSignals(t, router, `{"signals":[
		{"model_id":"ml-model","symbol":"600000.SH","direction":"long","strength":0.8,"as_of":"2024-01-10T00:00:00Z"}
	]}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("store 缺失应 503，got %d body=%s", w.Code, w.Body.String())
	}
}

// ─── A-09：GET /api/signals 外部信号流读面 ─────────────────────────────

// feedSignalStore 在写入门替身之上补 ListRecent，模拟 PGSignalStore 的
// 「写 + 流读」双能力。plain memorySignalStore 只有写能力，用于验证
// 「后端不支持流读 ⇒ 503，而不是伪装空流」。
type feedSignalStore struct{ *memorySignalStore }

func (f *feedSignalStore) ListRecent(_ context.Context, modelID, symbol string, upTo time.Time, limit int) ([]strategy.ExternalSignal, error) {
	var out []strategy.ExternalSignal
	for _, s := range f.signals {
		if modelID != "" && s.ModelID != modelID {
			continue
		}
		if symbol != "" && s.Symbol != symbol {
			continue
		}
		if strategy.DayOf(s.AsOf).After(strategy.DayOf(upTo)) {
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].AsOf.Equal(out[j].AsOf) {
			return out[i].AsOf.After(out[j].AsOf)
		}
		if out[i].Symbol != out[j].Symbol {
			return out[i].Symbol < out[j].Symbol
		}
		return out[i].ModelID < out[j].ModelID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func newSignalsFeed(router *gin.Engine, query string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/signals"+query, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func seedFeedSignals(store *feedSignalStore) {
	store.signals = []strategy.ExternalSignal{
		{ModelID: "ml-a", Symbol: "600000.SH", Direction: "long", Strength: 0.8, AsOf: time.Date(2024, 1, 10, 0, 0, 0, 0, time.UTC)},
		{ModelID: "ml-b", Symbol: "000001.SZ", Direction: "short", Strength: 0.6, AsOf: time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)},
		{ModelID: "ml-a", Symbol: "000001.SZ", Direction: "close", Strength: 0.5, AsOf: time.Date(2024, 1, 20, 0, 0, 0, 0, time.UTC)},
	}
}

func TestSignalsFeed_ReturnsRecentDescending(t *testing.T) {
	store := &feedSignalStore{&memorySignalStore{}}
	seedFeedSignals(store)
	router := newSignalsTestRouter(store)

	w := newSignalsFeed(router, "")
	if w.Code != http.StatusOK {
		t.Fatalf("信号流应 200，got %d body=%s", w.Code, w.Body.String())
	}
	var resp listSignalsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应解码失败：%v", err)
	}
	if resp.Count != 3 || len(resp.Signals) != 3 {
		t.Fatalf("count=%d len=%d, want 3", resp.Count, len(resp.Signals))
	}
	if resp.Limit != 100 {
		t.Fatalf("默认 limit = %d, want 100", resp.Limit)
	}
	if !resp.Signals[0].AsOf.Equal(time.Date(2024, 1, 20, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("应按 as_of 倒序，first=%s", resp.Signals[0].AsOf)
	}
}

func TestSignalsFeed_FiltersAndLimit(t *testing.T) {
	store := &feedSignalStore{&memorySignalStore{}}
	seedFeedSignals(store)
	router := newSignalsTestRouter(store)

	w := newSignalsFeed(router, "?model_id=ml-a&limit=1")
	var resp listSignalsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应解码失败：%v", err)
	}
	if resp.Count != 1 || resp.Limit != 1 {
		t.Fatalf("count=%d limit=%d, want 1/1", resp.Count, resp.Limit)
	}
	if resp.Signals[0].ModelID != "ml-a" {
		t.Fatalf("model_id 过滤失效：%+v", resp.Signals[0])
	}

	w = newSignalsFeed(router, "?symbol=600000.SH")
	resp = listSignalsResponse{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应解码失败：%v", err)
	}
	if resp.Count != 1 || resp.Signals[0].Symbol != "600000.SH" {
		t.Fatalf("symbol 过滤失效：count=%d %+v", resp.Count, resp.Signals)
	}

	// up_to 把未来（相对该日）的信号切掉：2024-01-10 只剩 1 条。
	w = newSignalsFeed(router, "?up_to=2024-01-10")
	resp = listSignalsResponse{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应解码失败：%v", err)
	}
	if resp.Count != 1 {
		t.Fatalf("up_to 截断失效：count=%d", resp.Count)
	}
}

func TestSignalsFeed_RejectsBadParams(t *testing.T) {
	store := &feedSignalStore{&memorySignalStore{}}
	seedFeedSignals(store)
	router := newSignalsTestRouter(store)

	if w := newSignalsFeed(router, "?limit=abc"); w.Code != http.StatusBadRequest {
		t.Fatalf("非法 limit 应 400，got %d", w.Code)
	}
	if w := newSignalsFeed(router, "?limit=0"); w.Code != http.StatusBadRequest {
		t.Fatalf("limit=0 应 400，got %d", w.Code)
	}
	if w := newSignalsFeed(router, "?up_to=2024/01/10"); w.Code != http.StatusBadRequest {
		t.Fatalf("非法 up_to 应 400，got %d", w.Code)
	}
}

func TestSignalsFeed_UnavailableWithoutFeedBackend(t *testing.T) {
	// 只有写能力的后端：GET 明确 503，不伪装成空流。
	if w := newSignalsFeed(newSignalsTestRouter(&memorySignalStore{}), ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("无流读能力应 503，got %d body=%s", w.Code, w.Body.String())
	}
	// 未接库（store=nil）：同样 503，路由不消失。
	if w := newSignalsFeed(newSignalsTestRouter(nil), ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("store 缺失应 503，got %d body=%s", w.Code, w.Body.String())
	}
}
