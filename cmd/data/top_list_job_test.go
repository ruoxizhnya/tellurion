package main

// DBT-10 ⑤ 的主证据：top_list（龙虎榜）从**作业层真的走一次**。
//
// 路径与 capital_flow_job_test.go 同一条形状：HTTP 门 → JobService → Queue →
// WorkerPool 派发 → topListExecutor → NewETLPipeline → Registry.Fetch
// （链 top_list:[eastmoney_toplist]）→ BulkInsert，落的是**真库**
// `quant_trading.top_list`，落完逐字段读回与源响应比对。
//
// 两处替身，理由如实：
//   - **作业行用内存 store**（复用 memJobStore）：Queue.Dequeue 取「最老的
//     pending」而不看 job_type，起真队列会把别的 agent 正在排的作业抢过来
//     打失败 —— 那是拿别人的作业给自己作证。落库那一半没有打折。
//   - **东财 datacenter 用 httptest 替身**，并且它**认真实现分页与日期 filter**：
//     本执行器全部的独有逻辑（翻页到空页为止、预算用尽仍未取尽就报错）都
//     建立在「按页答数」上，一个「问什么都答同一张表」的替身会让翻页腿假绿。
//
// 探针 symbol 用 888888.SH / 888889.SH / 888890.SH（不是真实 A 股代码），
// 前后各删一次并断言清零。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	stdsync "sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/data/source"
	"tellurion/pkg/storage"
	"tellurion/pkg/sync"
)

// 探针 symbol 用 888888.SZ / 888889.SZ / 888890.SZ（不是真实 A 股代码），
// 前后各删一次并断言清零。
//
// ⚠️ 后缀不是我挑的，是**适配器给的**：源侧 SECURITY_CODE 是 6 位数字，
// `fetchTopList` 按首位补后缀（`6`/`9` → .SH，其余 → .SZ），所以 8 字头落成
// .SZ。本替身保持「源给 6 位码」这个真实形状，把期望的规范 symbol 写死在下面。
const (
	topListSymA = "888888.SZ"
	topListSymB = "888889.SZ"
	topListSymC = "888890.SZ"
)

// topListStubRow 是一行源侧数据 + 它的**手算**期望（期望不来自被测代码）。
type topListStubRow struct {
	Symbol     string
	Name       string
	TradeDate  string // 源格式 "2006-01-02"
	NetBuy     float64
	BuyAmount  float64
	SellAmount float64
	Turnover   float64
	Reason     string
}

// topListStubRows 是替身的「表」，顺序即源返回顺序（分页按它切）。
var topListStubRows = []topListStubRow{
	{topListSymA, "探针甲", "2026-09-30", 1234.5, 7000, 5765.5, 987654, "日涨幅偏离值达7%"},
	{topListSymB, "探针乙", "2026-09-30", -500.25, 3000, 3500.25, 123456, "日换手率达20%"},
	{topListSymC, "探针丙", "2026-09-30", 0, 1000, 1000, 55, "日跌幅偏离值达7%"},
}

// topListStubRequest 是一次上游请求的**已解码**形状。
// 记解码后的值而不是 RawQuery：url.Values.Encode 会把空格写成 `+`、
// 把 `'` 写成 `%27`，拿编码串做断言等于把测试钉在编码细节上，
// 而这里要钉的是「窗口与页码真的到达了源」。
type topListStubRequest struct {
	PageNumber int
	PageSize   int
	Filter     string
	Start      string // 从 filter 里解出的窗口两端（YYYY-MM-DD）
	End        string
	Raw        string
}

// topListStub 是东财 datacenter 的替身：按 filter 里的 TRADE_DATE 收窄、
// 按 pageNumber/pageSize 切片。
type topListStub struct {
	mu       stdsync.Mutex
	requests []topListStubRequest
	status   int
	respond  bool // false = 一律答 0 行（模拟窗口内没有榜单）
	failPage int  // >0 时该页答 502（单页失败腿）
}

func (s *topListStub) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		filter := q.Get("filter")
		start, end, hasWindow := topListParseFilter(filter)
		rec := topListStubRequest{
			PageNumber: atoiOr(q.Get("pageNumber"), 1),
			PageSize:   atoiOr(q.Get("pageSize"), 100),
			Filter:     filter,
			Start:      start.Format("2006-01-02"),
			End:        end.Format("2006-01-02"),
			Raw:        r.URL.RawQuery,
		}

		s.mu.Lock()
		s.requests = append(s.requests, rec)
		status := s.status
		failPage := s.failPage
		s.mu.Unlock()

		if failPage > 0 && rec.PageNumber == failPage {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("datacenter 502"))
			return
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte("datacenter exploded"))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if !hasWindow {
			// 无 filter 时源按自己的默认排序答第一页 —— 本执行器不该走到这里，
			// 替身在这里如实返回空并让断言去质问窗口为什么没到源上。
			_, _ = w.Write([]byte(`{"result":{"data":[]}}`))
			return
		}

		var matched []topListStubRow
		for _, row := range topListStubRows {
			d, err := time.Parse("2006-01-02", row.TradeDate)
			if err != nil {
				continue
			}
			if !d.Before(start) && !d.After(end) {
				matched = append(matched, row)
			}
		}
		if !s.respond {
			matched = nil
		}
		lo := (rec.PageNumber - 1) * rec.PageSize
		if lo > len(matched) {
			lo = len(matched)
		}
		hi := lo + rec.PageSize
		if hi > len(matched) {
			hi = len(matched)
		}
		page := matched[lo:hi]

		data := make([]map[string]any, 0, len(page))
		for _, row := range page {
			data = append(data, map[string]any{
				"SECURITY_CODE":      row.Symbol[:6], // 源给 6 位代码，适配器补交易所后缀
				"SECURITY_NAME_ABBR": row.Name,
				"TRADE_DATE":         row.TradeDate,
				"BILLBOARD_NET_AMT":  row.NetBuy,
				"BILLBOARD_BUY_AMT":  row.BuyAmount,
				"BILLBOARD_SELL_AMT": row.SellAmount,
				"TURNOVER":           row.Turnover,
				"EXPLAIN":            row.Reason,
				"CLOSE_PRICE":        17.5,
				"CHANGE_RATE":        2.3,
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"result": map[string]any{"data": data},
			"rc":     0,
		})
	}
}

func (s *topListStub) seen() []topListStubRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]topListStubRequest(nil), s.requests...)
}

func atoiOr(v string, fallback int) int {
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n <= 0 {
		return fallback
	}
	return n
}

// topListParseFilter 从 filter 串里取出窗口两端。filter 由执行器经
// FetchRequest.StartDate/EndDate 生成，形状固定为
// `(TRADE_DATE>='YYYY-MM-DD') and (TRADE_DATE<='YYYY-MM-DD')`；
// 只给一端时另一端按同一日处理（本测试都用单日窗口）。
func topListParseFilter(filter string) (time.Time, time.Time, bool) {
	bounds := topListDateBounds(filter)
	if len(bounds) == 0 {
		return time.Time{}, time.Time{}, false
	}
	start, err := time.Parse("2006-01-02", bounds[0])
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	if len(bounds) == 1 {
		return start, start, true
	}
	end, err := time.Parse("2006-01-02", bounds[1])
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	return start, end, true
}

// topListDateBounds 抓出 filter 里所有 YYYY-MM-DD 字面量（按出现顺序）。
func topListDateBounds(filter string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`\d{4}-\d{2}-\d{2}`).FindAllString(filter, -1) {
		out = append(out, m)
	}
	return out
}

// ---- 装配 ----

type topListHarness struct {
	handler *SyncHandler
	router  *gin.Engine
	stub    *topListStub
}

func newTopListHarness(t *testing.T, persist storage.BulkInserter) *topListHarness {
	t.Helper()
	stub := &topListStub{status: http.StatusOK, respond: true}
	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)

	jobs := &memJobStore{}
	queue := sync.NewQueue(jobs)
	jobService := sync.NewJobService(jobs)
	jobService.SetPendingNotifier(queue.NotifyJobAvailable)
	pool := sync.NewWorkerPool(queue, 1)
	jobService.SetRunningCanceller(pool.Cancel)

	reg := source.NewRegistry()
	require.NoError(t, reg.Register(source.NewEastmoneyTopListAdapter(&source.EastmoneyClient{
		BaseURL:       srv.URL,
		DataCenterURL: srv.URL,
		HTTPClient:    &http.Client{Timeout: 5 * time.Second},
	})))
	reg.SetChain(source.DataTypeTopList, []string{"eastmoney_toplist"})
	pool.RegisterExecutor(&topListExecutor{reg: reg, persist: persist})

	h := &SyncHandler{jobService: jobService, queue: queue, workerPool: pool, registry: reg}
	router := gin.New()
	router.POST("/api/sync/jobs", h.createJobHandler)
	return &topListHarness{handler: h, router: router, stub: stub}
}

func (h *topListHarness) createJob(t *testing.T, params map[string]any) (int, string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"type": "top_list", "params": params})
	require.NoError(t, err)
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/sync/jobs", bytes.NewReader(body)))
	return w.Code, w.Body.String()
}

func (h *topListHarness) executor(t *testing.T) sync.JobExecutor {
	t.Helper()
	exec, ok := h.handler.workerPool.GetExecutor(sync.JobTypeTopList)
	require.True(t, ok, "top_list 执行器必须注册在作业层上")
	return exec
}

func topListJobWithParams(id, params string) *sync.Job {
	return &sync.Job{
		ID:         id,
		JobType:    sync.JobTypeTopList,
		Status:     sync.JobStatusRunning,
		Params:     json.RawMessage(params),
		MaxRetries: 3,
	}
}

// ---- 真库读写 ----

func cleanTopListProbes(t *testing.T, store *storage.PostgresStore) {
	t.Helper()
	_, err := store.DB().Exec(context.Background(),
		`DELETE FROM top_list WHERE symbol IN ($1, $2, $3)`, topListSymA, topListSymB, topListSymC)
	require.NoError(t, err)
}

type topListRow struct {
	TradeDate  time.Time
	Symbol     string
	Name       string
	NetBuy     *float64
	BuyAmount  *float64
	SellAmount *float64
	Turnover   *float64
	Reason     *string
	Source     string
	IngestTime time.Time
}

func readTopListRows(t *testing.T, store *storage.PostgresStore) []topListRow {
	t.Helper()
	rows, err := store.DB().Query(context.Background(),
		`SELECT trade_date, symbol, name, net_buy, buy_amount, sell_amount, turnover,
		        reason, source, ingest_time
		 FROM top_list WHERE symbol IN ($1, $2, $3) ORDER BY symbol, trade_date`,
		topListSymA, topListSymB, topListSymC)
	require.NoError(t, err)
	defer rows.Close()

	var out []topListRow
	for rows.Next() {
		var r topListRow
		require.NoError(t, rows.Scan(&r.TradeDate, &r.Symbol, &r.Name, &r.NetBuy, &r.BuyAmount,
			&r.SellAmount, &r.Turnover, &r.Reason, &r.Source, &r.IngestTime))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

// ---- 主证据：作业层真的走一次 + 落库逐字段对拍 ----

func TestDBT10TopListJobLayerPersistsRowsMatchingSourceResponse(t *testing.T) {
	store := capitalFlowStore(t)
	cleanTopListProbes(t, store)
	t.Cleanup(func() { cleanTopListProbes(t, store) })
	require.Empty(t, readTopListRows(t, store), "前置失败：探针 symbol 上还有残留行")

	h := newTopListHarness(t, store)
	h.handler.StartWorkerPool()
	t.Cleanup(h.handler.StopWorkerPool)

	code, bodyText := h.createJob(t, map[string]any{
		"start_date": "20260930",
		"end_date":   "20260930",
	})
	require.Equal(t, http.StatusAccepted, code, "作业门必须 202，实际响应：%s", bodyText)

	var created struct {
		JobID string `json:"job_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(bodyText), &created))
	settled := waitForMemJob(t, h.handler, created.JobID)
	require.Equal(t, sync.JobStatusCompleted, settled.Status,
		"作业应成功落定；error_message=%q result=%s", settled.ErrorMessage, string(settled.Result))

	var result struct {
		Persisted int    `json:"persisted"`
		Fetched   int    `json:"fetched"`
		Skipped   int    `json:"skipped"`
		Pages     int    `json:"pages"`
		PageSize  int    `json:"page_size"`
		DataType  string `json:"data_type"`
	}
	require.NoError(t, json.Unmarshal(settled.Result, &result))
	assert.Equal(t, 3, result.Persisted, "单日 3 条榜单")
	assert.Equal(t, 3, result.Fetched)
	assert.Equal(t, source.DataTypeTopList, result.DataType)
	assert.Equal(t, topListDefaultPageSize, result.PageSize)

	// 翻页必须翻到空页为止：3 行一页装完，再问一次得到空页 ⇒ 2 次请求。
	requests := h.stub.seen()
	require.Len(t, requests, 2, "第 1 页装满、第 2 页为空才叫取尽；只发 1 发就是把「一页」当成了「全部」：%+v", requests)
	assert.Equal(t, 1, requests[0].PageNumber)
	assert.Equal(t, 2, requests[1].PageNumber)
	for _, rq := range requests {
		assert.Equal(t, "2026-09-30", rq.Start,
			"窗口必须带到源上，否则「作业 completed 但库里只有源默认第 1 页」就是 CR-41 重演：%s", rq.Filter)
		assert.Equal(t, "2026-09-30", rq.End, "同上：%s", rq.Filter)
		assert.Equal(t, topListDefaultPageSize, rq.PageSize)
	}

	got := readTopListRows(t, store)
	require.Len(t, got, len(topListStubRows))
	bySymbol := map[string]topListRow{}
	for _, r := range got {
		bySymbol[r.Symbol] = r
	}
	for _, want := range topListStubRows {
		r, ok := bySymbol[want.Symbol]
		require.True(t, ok, "%s 没落进库", want.Symbol)
		label := want.Symbol

		assert.Equal(t, want.TradeDate, r.TradeDate.UTC().Format("2006-01-02"), label+" trade_date")
		assert.Equal(t, want.Name, r.Name, label+" name（NOT NULL 身份列）")
		assert.Equal(t, "eastmoney_toplist", r.Source,
			label+" source 必须是链上真正供数的那扇门，读侧据此判口径")
		assert.False(t, r.IngestTime.IsZero(), label+" ingest_time 由 pipeline 盖章")

		assert.InDelta(t, want.NetBuy, derefFloat(t, r.NetBuy, label+" net_buy"), 1e-9)
		assert.InDelta(t, want.BuyAmount, derefFloat(t, r.BuyAmount, label+" buy_amount"), 1e-9)
		assert.InDelta(t, want.SellAmount, derefFloat(t, r.SellAmount, label+" sell_amount"), 1e-9,
			label+" 卖出大于买入必须是负数净额，不许被截成 0")
		assert.InDelta(t, want.Turnover, derefFloat(t, r.Turnover, label+" turnover"), 1e-9)
		require.NotNil(t, r.Reason, label+" reason 是源给的 EXPLAIN，应有值")
		assert.Equal(t, want.Reason, *r.Reason)
	}

	// 888890.SZ 这一行专证「真 0 与 NULL 长得不一样」：源给了 net_buy=0、
	// turnover=55，落库必须是 0.0 与 55.0 而不是 NULL。
	zero := bySymbol[topListSymC]
	require.NotNil(t, zero.NetBuy, "源给了 0 就必须是 0，不许折成 NULL")
	assert.InDelta(t, 0.0, *zero.NetBuy, 1e-9)
	require.NotNil(t, zero.Turnover, "同上：turnover=55 是有值")
	assert.InDelta(t, 55.0, *zero.Turnover, 1e-9)
}

// TestDBT10TopListJobRerunIsIdempotent 钉 (trade_date, symbol) 冲突键的幂等：
// 同一窗口重跑不产生重复行。
func TestDBT10TopListJobRerunIsIdempotent(t *testing.T) {
	store := capitalFlowStore(t)
	cleanTopListProbes(t, store)
	t.Cleanup(func() { cleanTopListProbes(t, store) })

	h := newTopListHarness(t, store)
	h.handler.StartWorkerPool()
	t.Cleanup(h.handler.StopWorkerPool)

	params := map[string]any{"start_date": "20260930", "end_date": "20260930"}
	for run := 1; run <= 2; run++ {
		code, bodyText := h.createJob(t, params)
		require.Equal(t, http.StatusAccepted, code, "第 %d 次：门要 202，响应 %s", run, bodyText)
		var created struct {
			JobID string `json:"job_id"`
		}
		require.NoError(t, json.Unmarshal([]byte(bodyText), &created))
		settled := waitForMemJob(t, h.handler, created.JobID)
		require.Equal(t, sync.JobStatusCompleted, settled.Status, "第 %d 次跑失败：%s", run, settled.ErrorMessage)

		got := readTopListRows(t, store)
		assert.Len(t, got, 3, "第 %d 次重跑后行数变了 ⇒ upsert 幂等没成立", run)
	}
}

// TestDBT10TopListPaginatesUntilEmptyPage 是真·翻页腿：page_size=1 时
// 3 行必须分 3 页取完 + 1 页空 ⇒ 4 发请求、3 行入库。
// 只发 1 发的实现（「一页就是全部」）在这里当场红。
func TestDBT10TopListPaginatesUntilEmptyPage(t *testing.T) {
	h := newTopListHarness(t, &countingBulkInserter{})

	rec := &recordingReporter{}
	res, err := h.executor(t).Execute(context.Background(),
		topListJobWithParams("dbt10-paging", `{"start_date":"20260930","end_date":"20260930","page_size":1}`), rec)
	require.NoError(t, err, "翻页腿不该失败：%v", res)

	got := res.(map[string]any)
	assert.Equal(t, 3, got["persisted"], "page_size=1 也要把 3 行取完")
	assert.Equal(t, 4, got["pages"], "3 页有数 + 第 4 页为空才叫取尽：%v", got)
	assert.Len(t, h.stub.seen(), 4, "每页一发")
	assert.Equal(t, 4, rec.progressCalls, "进度按页上报")
}

// TestDBT10TopListPageBudgetExhaustionFailsLoud 是截断闸门：
// max_pages 用尽而最后一页仍是满的 ⇒ 报错点名「未取尽」，
// 不许报 completed 而库里只有前几页（CR-41 的静默截断）。
func TestDBT10TopListPageBudgetExhaustionFailsLoud(t *testing.T) {
	h := newTopListHarness(t, &countingBulkInserter{})

	rec := &recordingReporter{}
	res, err := h.executor(t).Execute(context.Background(),
		topListJobWithParams("dbt10-budget",
			`{"start_date":"20260930","end_date":"20260930","page_size":1,"max_pages":2}`), rec)

	require.Error(t, err, "预算用尽仍未取尽必须报错，不许静默丢掉剩下的页")
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "not exhausted", "报错要给出可执行的下一步（抬 max_pages 或分片窗口）：%v", err)
	assert.Len(t, h.stub.seen(), 2, "只用掉预算内的 2 发，不多打")
}

// TestDBT10TopListAbortsOnFirstFailedPage 钉「单页失败即中止整窗」：
// 跳过一页继续下一页 = 在库里留一个没人知道的洞。
func TestDBT10TopListAbortsOnFirstFailedPage(t *testing.T) {
	h := newTopListHarness(t, &countingBulkInserter{})
	h.stub.mu.Lock()
	h.stub.failPage = 2
	h.stub.mu.Unlock()

	rec := &recordingReporter{}
	res, err := h.executor(t).Execute(context.Background(),
		topListJobWithParams("dbt10-page-fail",
			`{"start_date":"20260930","end_date":"20260930","page_size":1}`), rec)

	require.Error(t, err, "第 2 页 502 时必须中止，而不是把第 3 页当成全部")
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "502", "源侧原话要留在 error 里进 error_message：%v", err)
	assert.Contains(t, err.Error(), "page 2", "报错要点名是第几页：%v", err)
	assert.Len(t, h.stub.seen(), 2, "第 2 页失败后不许再翻第 3 页")
	assert.GreaterOrEqual(t, rec.errorCalls, 0)
}

// TestDBT10TopExecutorRejectsMissingWindow 钉窗口必填：不给 end_date 时
// 源会按它自己的默认排序答第 1 页 —— 「作业 completed 但库里只有最近 100 条」。
func TestDBT10TopListExecutorRejectsMissingWindow(t *testing.T) {
	h := newTopListHarness(t, &countingBulkInserter{})

	for _, params := range []string{
		`{}`,
		`{"start_date":"20260930"}`,
		`{"end_date":"20260930"}`,
		`{"start_date":"20260930","end_date":""}`,
		`{"start_date":"20261301","end_date":"20261305"}`,
		`{"start_date":"20260930","end_date":"20260929"}`,
	} {
		rec := &recordingReporter{}
		_, err := h.executor(t).Execute(context.Background(),
			topListJobWithParams("dbt10-window", params), rec)
		require.Error(t, err, "params=%s 必须被拦下", params)
	}

	assert.Empty(t, h.stub.seen(), "窗口非法时一发请求都不该发出去（配额与页序都不该被动）")
}

// TestDBT10TopListContextCancelStopsBeforeSource 钉 ctx 取消真的挡在源前面。
func TestDBT10TopListContextCancelStopsBeforeSource(t *testing.T) {
	h := newTopListHarness(t, &countingBulkInserter{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 起跑前就取消：第一页之前就该返回

	rec := &recordingReporter{}
	res, err := h.executor(t).Execute(ctx,
		topListJobWithParams("dbt10-cancel", `{"start_date":"20260930","end_date":"20260930"}`), rec)
	assert.ErrorIs(t, err, context.Canceled, "取消要如实上抛 ctx.Err()（worker 据此不写状态，AUD-49/50）")
	assert.Nil(t, res)
	assert.Empty(t, h.stub.seen(), "已取消的作业不该再去打源")
}

// TestDBT10TopListExecutorFailsWhenPersistBreaks 是 DATA-06 家族在作业层的同形：
// 取到了但一行没落进去 ⇒ 不许报成功。这里由 BulkInsert 失败触发。
func TestDBT10TopListExecutorFailsWhenPersistBreaks(t *testing.T) {
	failing := &failingBulkInserter{}
	h := newTopListHarness(t, failing)

	rec := &recordingReporter{}
	res, err := h.executor(t).Execute(context.Background(),
		topListJobWithParams("dbt10-persist-fail", `{"start_date":"20260930","end_date":"20260930"}`), rec)

	require.Error(t, err, "取到了却一行没落 ⇒ 必须报错，不能交一个看起来成功的 result")
	assert.Nil(t, res)
	assert.Equal(t, 1, failing.calls, "错误确实来自落库那一跳，不是前置校验拦的")
	assert.Contains(t, err.Error(), "page 1", "落库失败也要点名是哪一页：%v", err)
}

// TestDBT10TopListEmptyWindowIsNotAnError 钉「合法的空」：窗口内确实没有
// 榜单（整窗休市）时源答 0 行 ⇒ 作业成功收口，persisted=0。
// 与上面那条（取到了没落进去）的区别必须可分辨。
func TestDBT10TopListEmptyWindowIsNotAnError(t *testing.T) {
	h := newTopListHarness(t, &countingBulkInserter{})
	h.stub.mu.Lock()
	h.stub.respond = false
	h.stub.mu.Unlock()

	rec := &recordingReporter{}
	res, err := h.executor(t).Execute(context.Background(),
		topListJobWithParams("dbt10-empty", `{"start_date":"20260101","end_date":"20260101"}`), rec)
	require.NoError(t, err, "源答了、答案是「这个窗口没有记录」是合法收口：%v", err)
	require.NotNil(t, res)

	got := res.(map[string]any)
	assert.Equal(t, 0, got["persisted"])
	assert.Equal(t, 1, got["pages"], "一发就拿到空页 ⇒ 不需要再翻")
}

// ---- 门（typed door）的校验腿 ----

func TestDBT10TopListJobDoorRejectsBadRequests(t *testing.T) {
	h := newTopListHarness(t, &countingBulkInserter{})

	tests := []struct {
		name   string
		params map[string]any
		wants  string
	}{
		{"missing_both_dates", map[string]any{}, "both required"},
		{"missing_end_date", map[string]any{"start_date": "20260930"}, "both required"},
		// 给空串与不给**同判**：判据在执行器那一份里（topListWindow），
		// 门上不再写第二套，所以这里期望的是同一句话。
		{"empty_end_date", map[string]any{"start_date": "20260930", "end_date": ""}, "both required"},
		{"bad_date", map[string]any{"start_date": "20261301", "end_date": "20261305"}, "YYYYMMDD"},
		{"inverted_window", map[string]any{"start_date": "20260930", "end_date": "20260929"}, "before start_date"},
		{"symbols_not_supported", map[string]any{
			"start_date": "20260930", "end_date": "20260930", "symbols": []string{"600519.SH"},
		}, "no params.symbols"},
		{"negative_page_size", map[string]any{
			"start_date": "20260930", "end_date": "20260930", "page_size": -1,
		}, "positive"},
		{"span_too_wide", map[string]any{
			"start_date": "20240101", "end_date": "20261231",
		}, "spans"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, bodyText := h.createJob(t, tc.params)
			assert.Equal(t, http.StatusBadRequest, code, "响应：%s", bodyText)
			assert.Contains(t, bodyText, tc.wants)
		})
	}
}

// TestDBT10TopListJobDoorAcceptsTopListType 是负例的对照腿：门里现在真的认
// 这个名字 —— 不认的话上面所有负例都会以 400 假绿。
func TestDBT10TopListJobDoorAcceptsTopListType(t *testing.T) {
	h := newTopListHarness(t, &countingBulkInserter{})

	code, bodyText := h.createJob(t, map[string]any{"start_date": "20260930", "end_date": "20260930"})
	require.Equal(t, http.StatusAccepted, code, bodyText)

	body, err := json.Marshal(map[string]any{"type": "not_a_job_type", "params": map[string]any{}})
	require.NoError(t, err)
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/sync/jobs", bytes.NewReader(body)))
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "top_list",
		"拒绝文案里的 supported 名单要跟着门一起改，否则界面在撒谎")
}

// TestNewSyncHandlerRegistersTopListExecutor 钉住生产装配点本身（SYNC-01 那一族
// 「写得对但没人调」的形状）：执行器注册进去了、Registry 就是执行器用的那一个。
// 不调 gin.SetMode —— AUD-29 只允许它在 pkg/httpserver/ginmode.go 出现。
func TestNewSyncHandlerRegistersTopListExecutor(t *testing.T) {
	reg := source.NewRegistry()

	h := NewSyncHandler(nil, nil, nil, reg)
	require.NotNil(t, h)
	h.RegisterExecutors()

	assert.True(t, h.workerPool.HasExecutor(sync.JobTypeTopList),
		"NewSyncHandler 必须把 top_list 执行器注册进 worker pool")
	assert.Same(t, reg, h.registry, "compose root 传进来的 Registry 必须就是执行器用的那一个")
}

// TestTopListCronMembership 钉 cron 名单这一处裁决（top_list 进、capital_flow
// 不进），用**源码静态断言**而不是给 Scheduler 加一个测试专用的 getter ——
// AGENTS.md §6「不要为测试在生产代码里加只供测试的行为/接口」是同一条纪律，
// 而名单本身是装配语句，读源码就是它的真相。
//
// 裁决与代价写在 sync_handlers.go 的 RegisterJobType 旁边：判据是配额形状
// （整市场翻页 1–2 发 vs 逐 symbol ≈5900 发），不是数据重要性。
func TestTopListCronMembership(t *testing.T) {
	src := readSource(t, "sync_handlers.go")
	assert.Contains(t, src, `scheduler.RegisterJobType("top_list", sync.JobTypeTopList)`,
		"top_list 必须排进 cron 名单，否则 sync_schedules 想排它会在类型校验处失败")
	assert.NotContains(t, src, `scheduler.RegisterJobType("capital_flow"`,
		"capital_flow 刻意不入名单（DATA-10 的既有裁决：全池逐 symbol 扇出会烧完整桶配额）")
}
