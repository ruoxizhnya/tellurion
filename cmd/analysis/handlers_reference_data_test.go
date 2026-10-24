package main

// QT-67 护栏：交易日历 / 上市窗口两条只读端点的行为、鉴权与**线形**。
//
// 四条腿：
//  1. 行为：区间校验（400）、条目日期格式、稳定排序、空集合输出 `[]` 而非
//     `null`、存储未装配 503、存储报错 500。
//  2. 鉴权：鉴权开启时无 token → 401；readonly-post 档下 viewer 可读（纯读
//     不得被抬成 trader 档）。
//  3. 线形：docs/openapi.yaml 的 schema 属性集 ↔ handler 的 json tag 集
//     **双向**对拍。QT-67 要求 DTO 由 oapi-codegen 生成，但 contracts 今天
//     没有 dto 包（G0D-C5，见 TASKS 的 QT-50 记录），所以本单没有生成物；
//     这条反射对拍是「yaml 与代码不许各说各话」的替代牙口。
//  4. 路由：由 routes_contract_rev13_test.go 与 agent_coarse_actions_test.go
//     自动覆盖（新端点必须进 yaml 与 agent 动作表）。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"tellurion/docs"
	"tellurion/pkg/auth"
	"tellurion/pkg/authz"
	"tellurion/pkg/storage"
)

type fakeReferenceStore struct {
	entries  []storage.TradingCalendarEntry
	windows  map[string]storage.ListingWindow
	err      error
	calCalls int
	winCalls int
	gotStart time.Time
	gotEnd   time.Time
}

func (f *fakeReferenceStore) GetTradingCalendar(_ context.Context, start, end time.Time) ([]storage.TradingCalendarEntry, error) {
	f.calCalls++
	f.gotStart, f.gotEnd = start, end
	if f.err != nil {
		return nil, f.err
	}
	return f.entries, nil
}

func (f *fakeReferenceStore) GetListingWindows(_ context.Context) (map[string]storage.ListingWindow, error) {
	f.winCalls++
	if f.err != nil {
		return nil, f.err
	}
	return f.windows, nil
}

func refRouter(t *testing.T, store referenceDataStore) *gin.Engine {
	t.Helper()
	r := gin.New()
	NewReferenceDataHandler(store, zerolog.Nop()).RegisterRoutes(r)
	return r
}

func refGet(t *testing.T, r *gin.Engine, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func refPost(t *testing.T, r *gin.Engine, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/listing-windows", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// ─── 交易日历：行为 ──────────────────────────────────────────────────

func TestReferenceData_Calendar_RejectsBadParams(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		url  string
		want string
	}{
		{"缺 start", "/api/v1/trading-calendar?end=2026-01-02", "start and end"},
		{"缺 end", "/api/v1/trading-calendar?start=2026-01-02", "start and end"},
		{"start 非 YYYY-MM-DD", "/api/v1/trading-calendar?start=20260102&end=2026-01-03", "invalid start"},
		{"end 非 YYYY-MM-DD", "/api/v1/trading-calendar?start=2026-01-02&end=2026/01/03", "invalid end"},
		{"start > end", "/api/v1/trading-calendar?start=2026-01-03&end=2026-01-02", "must not be after"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := &fakeReferenceStore{}
			w := refGet(t, refRouter(t, store), tc.url)
			assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), tc.want)
			assert.Zero(t, store.calCalls, "参数不合法不得打到存储（fail-closed 在校验之后）")
		})
	}
}

// parseDate 是本文件日历夹具的唯一构造入口。日期刻意取隔离纪元（<=1991）：
// `trading_calendar` 的主键只有 trade_date，internal/repoguard 的 AUD-62 静态
// 安检只认 `parseDate("YYYY-MM-DD")` 这个形状（见 calendar_seed_isolation_test.go）。
func parseDate(s string) time.Time {
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return d
}

func TestReferenceData_Calendar_ReturnsEntries(t *testing.T) {
	t.Parallel()

	store := &fakeReferenceStore{entries: []storage.TradingCalendarEntry{
		{TradeDate: parseDate("1990-01-02"), Exchange: "SSE", IsTradingDay: true},
		{TradeDate: parseDate("1990-01-03"), Exchange: "SSE", IsTradingDay: false},
	}}
	w := refGet(t, refRouter(t, store), "/api/v1/trading-calendar?start=2026-01-01&end=2026-01-31")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp tradingCalendarResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "2026-01-01", resp.StartDate)
	assert.Equal(t, "2026-01-31", resp.EndDate)
	assert.Equal(t, 2, resp.Count)
	require.Len(t, resp.Entries, 2)
	assert.Equal(t, "1990-01-02", resp.Entries[0].TradeDate, "日期按 YYYY-MM-DD 输出，不是 RFC3339")
	assert.True(t, resp.Entries[0].IsTradingDay)
	assert.False(t, resp.Entries[1].IsTradingDay, "休市日也要返回（is_trading_day=false 是数据，不是过滤条件）")

	assert.Equal(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), store.gotStart)
	assert.Equal(t, time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC), store.gotEnd)
}

func TestReferenceData_Calendar_EmptyIsArrayNotNull(t *testing.T) {
	t.Parallel()

	w := refGet(t, refRouter(t, &fakeReferenceStore{}), "/api/v1/trading-calendar?start=2026-01-01&end=2026-01-02")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"entries":[]`,
		"空结果必须是 [] —— null 会让调用方分不清「没有数据」与「字段缺失」")
}

func TestReferenceData_Calendar_StoreErrorIs500(t *testing.T) {
	t.Parallel()

	store := &fakeReferenceStore{err: errors.New("boom")}
	w := refGet(t, refRouter(t, store), "/api/v1/trading-calendar?start=2026-01-01&end=2026-01-02")
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.NotContains(t, w.Body.String(), "boom", "5xx 详情只进日志，不回给客户端")
}

// ─── 上市窗口：行为 ──────────────────────────────────────────────────

func TestReferenceData_ListingWindows_AllSortedWithNulls(t *testing.T) {
	t.Parallel()

	delist := time.Date(2020, 5, 6, 0, 0, 0, 0, time.UTC)
	store := &fakeReferenceStore{windows: map[string]storage.ListingWindow{
		"600000.SH": {List: time.Date(1999, 11, 10, 0, 0, 0, 0, time.UTC)},
		"000001.SZ": {List: time.Date(1991, 4, 3, 0, 0, 0, 0, time.UTC), Delist: &delist},
		"300001.SZ": {}, // 上市日未知：list_date 必须是 null，不是 0001-01-01
	}}
	w := refPost(t, refRouter(t, store), "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp listingWindowsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 3, resp.Count)
	require.Len(t, resp.Windows, 3)
	assert.Equal(t, []string{"000001.SZ", "300001.SZ", "600000.SH"},
		[]string{resp.Windows[0].Symbol, resp.Windows[1].Symbol, resp.Windows[2].Symbol},
		"map 迭代顺序随机，响应必须按 symbol 稳定排序")

	assert.Equal(t, "1991-04-03", *resp.Windows[0].ListDate)
	assert.Equal(t, "2020-05-06", *resp.Windows[0].DelistDate)
	assert.Nil(t, resp.Windows[1].ListDate, "零值 list_date 输出 null")
	assert.Nil(t, resp.Windows[1].DelistDate)
	assert.Nil(t, resp.Windows[2].DelistDate, "仍在市 ⇒ delist_date 为 null")
}

func TestReferenceData_ListingWindows_FiltersAndOmitsUnknown(t *testing.T) {
	t.Parallel()

	store := &fakeReferenceStore{windows: map[string]storage.ListingWindow{
		"600000.SH": {List: time.Date(1999, 11, 10, 0, 0, 0, 0, time.UTC)},
		"000001.SZ": {List: time.Date(1991, 4, 3, 0, 0, 0, 0, time.UTC)},
	}}
	w := refPost(t, refRouter(t, store), `{"symbols":[" 000001.SZ ","999999.SZ"]}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var resp listingWindowsResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 1, resp.Count)
	require.Len(t, resp.Windows, 1)
	assert.Equal(t, "000001.SZ", resp.Windows[0].Symbol, "symbol 需 trim；未知标的省略")
}

func TestReferenceData_ListingWindows_BadBody(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, body, want string }{
		{"非法 JSON", `{`, "invalid request body"},
		{"空 symbol", `{"symbols":["600000.SH"," "] }`, "symbols[1] is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := &fakeReferenceStore{}
			w := refPost(t, refRouter(t, store), tc.body)
			assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), tc.want)
			assert.Zero(t, store.winCalls)
		})
	}
}

// ─── 存储未装配 / 鉴权 ───────────────────────────────────────────────

func TestReferenceData_StoreUnavailableIs503(t *testing.T) {
	t.Parallel()

	r := refRouter(t, nil)
	assert.Equal(t, http.StatusServiceUnavailable,
		refGet(t, r, "/api/v1/trading-calendar?start=2026-01-01&end=2026-01-02").Code)
	assert.Equal(t, http.StatusServiceUnavailable,
		refPost(t, r, `{}`).Code,
		"存储未装配必须 503，不能回一个空窗口冒充「没有上市数据」")
}

func TestReferenceData_AuthFailClosedAndViewerCanRead(t *testing.T) {
	t.Parallel()

	svc := auth.NewService(nil, auth.Config{JWTSecret: []byte("qt67-ref")})
	require.True(t, svc.Enabled())

	r := gin.New()
	r.Use(svc.Middleware())
	r.Use(authz.Middleware(svc, analysisRoutePolicy))
	NewReferenceDataHandler(&fakeReferenceStore{}, zerolog.Nop()).RegisterRoutes(r)

	t.Run("无 token → 401", func(t *testing.T) {
		assert.Equal(t, http.StatusUnauthorized, refGet(t, r, "/api/v1/trading-calendar?start=2026-01-01&end=2026-01-02").Code)
		assert.Equal(t, http.StatusUnauthorized, refPost(t, r, `{}`).Code)
	})

	t.Run("viewer 可读（readonly-post 档）", func(t *testing.T) {
		viewer := tokenFor(t, svc, auth.RoleViewer)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/trading-calendar?start=2026-01-01&end=2026-01-02", nil)
		req.Header.Set("Authorization", "Bearer "+viewer)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code, w.Body.String())

		req = httptest.NewRequest(http.MethodPost, "/api/v1/listing-windows", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+viewer)
		w = httptest.NewRecorder()
		r.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
	})
}

// ─── 线形：openapi schema ↔ Go json tag ─────────────────────────────

// openapiSpec 只解析本案需要的部分：paths 的 200/requestBody schema 引用，
// 以及 components.schemas 的 properties 名集合。
type openapiSpecShape struct {
	Paths      map[string]map[string]openapiOperationShape `yaml:"paths"`
	Components struct {
		Schemas map[string]openapiSchemaShape `yaml:"schemas"`
	} `yaml:"components"`
}

type openapiOperationShape struct {
	Responses map[string]struct {
		Content map[string]struct {
			Schema struct {
				Ref string `yaml:"$ref"`
			} `yaml:"schema"`
		} `yaml:"content"`
	} `yaml:"responses"`
	RequestBody *struct {
		Content map[string]struct {
			Schema struct {
				Ref string `yaml:"$ref"`
			} `yaml:"schema"`
		} `yaml:"content"`
	} `yaml:"requestBody"`
}

type openapiSchemaShape struct {
	Properties map[string]yaml.Node `yaml:"properties"`
}

// itemsRefOf 取某个 array 属性节点上的 items.$ref。
func itemsRefOf(t *testing.T, label string, node yaml.Node) string {
	t.Helper()
	var prop struct {
		Items struct {
			Ref string `yaml:"$ref"`
		} `yaml:"items"`
	}
	require.NoError(t, node.Decode(&prop), "%s: 属性节点必须可解析", label)
	require.NotEmpty(t, prop.Items.Ref, "%s: array 属性必须用 $ref 指向条目 schema", label)
	return prop.Items.Ref
}

func loadOpenAPIShape(t *testing.T) openapiSpecShape {
	t.Helper()
	var spec openapiSpecShape
	require.NoError(t, yaml.Unmarshal(docs.OpenAPISpec, &spec), "docs/openapi.yaml 必须可解析")
	return spec
}

func refName(t *testing.T, ref string) string {
	t.Helper()
	const prefix = "#/components/schemas/"
	require.True(t, strings.HasPrefix(ref, prefix), "只支持本地 schema 引用，得到 %q", ref)
	return strings.TrimPrefix(ref, prefix)
}

func jsonFieldSet(t *testing.T, typ reflect.Type) map[string]bool {
	t.Helper()
	require.Equal(t, reflect.Struct, typ.Kind())
	out := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		name := strings.SplitN(tag, ",", 2)[0]
		if name == "" || name == "-" {
			t.Fatalf("%s 的字段 %s 缺 json tag —— wire 形状必须显式", typ.Name(), typ.Field(i).Name)
		}
		out[name] = true
	}
	return out
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// assertSchemaMatchesStruct 双向比较：schema 的属性集与 Go 结构的 json tag 集。
func assertSchemaMatchesStruct(t *testing.T, label string, schema openapiSchemaShape, typ reflect.Type) {
	t.Helper()
	specFields := map[string]bool{}
	for name := range schema.Properties {
		specFields[name] = true
	}
	assert.Equal(t, keysOf(specFields), keysOf(jsonFieldSet(t, typ)),
		"%s：docs/openapi.yaml 的属性集与 %s 的 json tag 集不一致（单向改 = 线形漂移）",
		label, typ.Name())
}

func TestReferenceData_WireShapeMatchesSpec(t *testing.T) {
	t.Parallel()

	spec := loadOpenAPIShape(t)

	// 1) 交易日历 200：response schema + 嵌套条目 schema。
	cal := spec.Paths["/api/v1/trading-calendar"]["get"]
	calRespRef := cal.Responses["200"].Content["application/json"].Schema.Ref
	calRespName := refName(t, calRespRef)
	calRespSchema, ok := spec.Components.Schemas[calRespName]
	require.True(t, ok, "yaml 缺 schema %s", calRespName)
	assertSchemaMatchesStruct(t, "GET /api/v1/trading-calendar 200", calRespSchema, reflect.TypeOf(tradingCalendarResponse{}))

	entriesNode, ok := calRespSchema.Properties["entries"]
	require.True(t, ok, "TradingCalendarResponse 必须声明 entries")
	entriesSchema := spec.Components.Schemas[refName(t, itemsRefOf(t, "TradingCalendarResponse.entries", entriesNode))]
	assertSchemaMatchesStruct(t, "TradingCalendarEntry", entriesSchema, reflect.TypeOf(tradingCalendarEntryDTO{}))

	// 2) 上市窗口 200：response schema + 嵌套窗口 schema。
	win := spec.Paths["/api/v1/listing-windows"]["post"]
	require.NotNil(t, win.RequestBody, "POST /api/v1/listing-windows 必须声明 requestBody（可为空对象）")
	winReqSchema := spec.Components.Schemas[refName(t, win.RequestBody.Content["application/json"].Schema.Ref)]
	assertSchemaMatchesStruct(t, "ListingWindowsRequest", winReqSchema, reflect.TypeOf(listingWindowsRequest{}))

	winRespSchema := spec.Components.Schemas[refName(t, win.Responses["200"].Content["application/json"].Schema.Ref)]
	assertSchemaMatchesStruct(t, "POST /api/v1/listing-windows 200", winRespSchema, reflect.TypeOf(listingWindowsResponse{}))
	windowsNode, ok := winRespSchema.Properties["windows"]
	require.True(t, ok, "ListingWindowsResponse 必须声明 windows")
	winItemSchema := spec.Components.Schemas[refName(t, itemsRefOf(t, "ListingWindowsResponse.windows", windowsNode))]
	assertSchemaMatchesStruct(t, "ListingWindow", winItemSchema, reflect.TypeOf(listingWindowDTO{}))
}
