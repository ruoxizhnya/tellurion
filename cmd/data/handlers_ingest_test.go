package main

// Tests for the L0 ingest write door (ADR-022 §2/§3, TASKS.md L0-1).
// The door is the only way an external producer's raw response becomes
// citable, so these tests pin two things: that malformed input is rejected
// before any write is attempted, and that a valid post lands one row whose
// content_hash is exactly what a citation must quote.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/storage"
	"tellurion/pkg/testenv"
)

// postIngestRaw sends one already-rendered request body through the router.
func postIngestRaw(t *testing.T, router *gin.Engine, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/ingest/raw", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	return w
}

// validationRouter wires the handler with a nil store on purpose. Reaching
// SaveRawIngest would panic, so a 400 response is proof the request was
// rejected before any write was attempted.
func validationRouter() *gin.Engine {
	// gin 的 mode 由 TestMain 设一次（AUD-28）—— 别在这里调 gin.SetMode。
	router := gin.New()
	router.POST("/api/ingest/raw", ingestRawHandler(nil))
	return router
}

func TestIngestRawHandler_RejectsInvalidRequests(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
	}{
		{"missing source", `{"dataset":"akshare.stock","key":"k","payload":{"a":1}}`},
		{"missing dataset", `{"source":"akshare","key":"k","payload":{"a":1}}`},
		{"missing key", `{"source":"akshare","dataset":"akshare.stock","payload":{"a":1}}`},
		{"missing payload", `{"source":"akshare","dataset":"akshare.stock","key":"k"}`},
		{"malformed body", `{"source":"akshare",`},
		{"content_hash mismatch", `{"source":"akshare","dataset":"akshare.stock","key":"k","payload":{"a":1},"content_hash":"deadbeef"}`},
		{"unparseable as_of", `{"source":"akshare","dataset":"akshare.stock","key":"k","payload":{"a":1},"as_of":"30/09/2024"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := postIngestRaw(t, validationRouter(), tc.body)
			assert.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
		})
	}
}

// TestParseIngestAsOf pins the two accepted wire formats and the trailing
// error case: a payload with no single observation date stores NULL.
func TestParseIngestAsOf(t *testing.T) {
	t.Parallel()
	got, err := parseIngestAsOf("")
	require.NoError(t, err)
	assert.Nil(t, got)

	got, err = parseIngestAsOf("2024-09-30")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "2024-09-30", got.Format("2006-01-02"))

	got, err = parseIngestAsOf("2024-09-30T12:00:00Z")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.True(t, got.Equal(time.Date(2024, 9, 30, 12, 0, 0, 0, time.UTC)))

	_, err = parseIngestAsOf("30/09/2024")
	assert.Error(t, err)
}

// TestIngestRawHandler_ArchivesAndIsIdempotent exercises the exit criterion
// of P1 end to end: an external producer posts a raw response, learns the
// content_hash to cite, and the platform can resolve that hash back to the
// exact archived record. Re-posting must not rewrite it.
func TestIngestRawHandler_ArchivesAndIsIdempotent(t *testing.T) {
	dsn := testenv.DatabaseDSN()
	if dsn == "" {
		testenv.SkipNoDB(t)
	}
	store, err := storage.NewPostgresStoreConnected(context.Background(), dsn)
	if err != nil {
		t.Fatalf("[testdb] TEST_DATABASE_DSN 已设置但连不上（fail-loud）: %v", err)
	}
	// 用 t.Cleanup 而不是 defer：本测试后面还要注册一条删除腿，cleanup 按 LIFO
	// 运行才能保证「先删探针行、再关连接」（defer 会在 cleanup 之前就把 store 关掉）。
	t.Cleanup(store.Close)

	router := gin.New()
	router.POST("/api/ingest/raw", ingestRawHandler(store))

	// A payload unique to this run, so the assertions below cannot be
	// satisfied by a row left behind by an earlier run.
	payload := fmt.Sprintf(`{"instrument":"600519.SH","probe":"ingest-handler-%d","items":[1,2,3]}`, time.Now().UnixNano())
	body := fmt.Sprintf(
		`{"source":"akshare","dataset":"akshare.stock_financial_abstract","key":"600519.SH/2024Q1","as_of":"2024-09-30","payload":%s}`,
		payload,
	)

	w := postIngestRaw(t, router, body)
	require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())

	var created storage.RawIngest
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))

	wantHash, err := storage.ContentHashOf([]byte(payload))
	require.NoError(t, err)

	// 自清理：`ingest.raw` 是**内容寻址的不可变归档表**，而这里写进去的是一条
	// 探针假数据（payload 里的 `probe` 键）。它和真归档混在同一张表里，会让任何
	// 读这张表的取证拿到脏读数 —— DBT-11 那次「fina_indicator 的 fields 到底有
	// 哪些」就是纯读库取证。按本 run 的 hash 精确删自己那一行，不碰任何别人的归档。
	// （登记在案：本仓此前每跑一次留一行，共享开发库里已积出同族探针行。）
	t.Cleanup(func() {
		ctx := context.Background()
		if _, derr := store.DB().Exec(ctx, `DELETE FROM ingest.raw WHERE content_hash = $1`, wantHash); derr != nil {
			t.Errorf("cleanup: delete probe row %s: %v", wantHash, derr)
			return
		}
		left, gerr := store.GetRawIngest(ctx, wantHash)
		if gerr != nil {
			t.Errorf("cleanup: read-back %s: %v", wantHash, gerr)
			return
		}
		if left != nil {
			t.Errorf("cleanup: 探针行 %s 仍在 ingest.raw 里 —— 清理是假把式", wantHash)
		}
	})

	assert.Equal(t, wantHash, created.ContentHash)
	assert.Equal(t, "akshare", created.Source)
	assert.Equal(t, "akshare.stock_financial_abstract", created.Dataset)
	require.NotNil(t, created.AsOf)
	assert.Equal(t, "2024-09-30", created.AsOf.Format("2006-01-02"))

	// The archived row is what the Evidence API returns for this hash.
	stored, err := store.GetRawIngest(context.Background(), wantHash)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "akshare", stored.Source)
	// 用 JSONEq 而非字符串相等：归档时会重新 marshal（键序按字典序、带空格），
	// 逐字节比较会因格式差异误报，但语义完全一致。
	assert.JSONEq(t, payload, string(stored.Payload))

	// Re-posting the identical response is a no-op on an immutable table.
	assert.Equal(t, http.StatusCreated, postIngestRaw(t, router, body).Code)
	again, err := store.GetRawIngest(context.Background(), wantHash)
	require.NoError(t, err)
	require.NotNil(t, again)
	assert.Equal(t, stored.FetchedAt.UTC(), again.FetchedAt.UTC())
}
