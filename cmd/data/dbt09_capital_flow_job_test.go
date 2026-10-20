package main

// DBT-09 的作业层证据：capital_flow 这条链接上 Tushare moneyflow 之后，
// **从 HTTP 门真的走一次**，落的是真库 `quant_trading.capital_flow`。
//
// 与 capital_flow_job_test.go 的关键差别是「用哪条链」：那边手搭一条
// `capital_flow:[eastmoney]` 的替身链（DATA-10 时点的生产形状），本文件走
// **生产的 `buildDataSourceRegistry`** —— 只有真的经过 configureFallbackChains，
// 「Tushare 优先、eastmoney 兜底」这条例由才有证据，也就才有破坏验证的靶子
// （把 registry_init.go 的链顺序换回 eastmoney 优先，本文件的落库腿会当场
// 失败在真源不可达上，而不是悄悄由 Tushare 腿替它答完）。
//
// 三处替身，理由如实写在这儿：
//   - **作业行用内存 store**（复用 capital_flow_job_test.go 的 memJobStore）：
//     Queue.Dequeue 取「最老的 pending」而不看 job_type，起真队列会把别的
//     agent 正在排的作业抢过来打失败。落库那一半没有打折。
//   - **Tushare 用 httptest 替身**：默认套件不许赌网络与配额。真源那条腿在
//     dbt09_moneyflow_live_test.go（build tag `dbt09live`）。
//   - **TushareClient 的 store 传 nil**：这条链的落库面是 ETL 的 BulkInsert，
//     导入面不写库；nil store 同时让归档闸（call → ingest.raw）在测试里
//     自然短路，不给共享开发库的 ingest.raw 追加测试噪声。
//
// 探针 symbol 沿用 888888.SH / 888889.SH（不是真实 A 股代码），前后各清一次
// 并断言清零 —— 与 capital_flow_job_test.go 同一批锚点，不给开发库留野数据。

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/data"
	"tellurion/pkg/data/source"
	"tellurion/pkg/storage"
	"tellurion/pkg/sync"
)

// dbt09 夹具（单位万元；落库期望全部**手算**，不是拿被测代码再算一遍）：
//
//	888888.SH 20260930: elg 100.5−0.5 = 100 万 ⇒ 1_000_000 元
//	                   lg   50.25−20.25 = 30 万 ⇒   300_000 元
//	                   md   10−40 = −30 万      ⇒  −300_000 元
//	                   sm   1−1 = 0             ⇒        0 元（真 0，必须落 0 不是 NULL）
//	888888.SH 20260929: elg 200−100 = 100 万 ⇒ 1_000_000；lg 10−5 = 5 万 ⇒ 50_000；
//	                   md 1−2 = −1 万 ⇒ −10_000；sm 0.5−0.5 = 0 ⇒ 0
//	888889.SH 两日：四档买卖各自相等 ⇒ 八个 0.0
//
// 888889.SH 这一票专证「真 0 与 NULL 在库里必须长得不一样」—— 把缺失折成 0
// 的实现（DBT-07 那一族）会让它「通过」，而这里它是独立的一腿。
//
// dbt09StubBody 按票返回自己的行。用 switch + 切片而不是 map：
// map 迭代顺序随机，会让「两票两发、每发四行」这类计数断言偶发红。
func dbt09StubBody(tsCode string) string {
	var rows []string
	switch tsCode {
	case capitalFlowSymA:
		rows = []string{
			`["888888.SH","20260930",100.5,0.5,50.25,20.25,10.0,40.0,1.0,1.0]`,
			`["888888.SH","20260929",200.0,100.0,10.0,5.0,1.0,2.0,0.5,0.5]`,
		}
	case capitalFlowSymB:
		rows = []string{
			`["888889.SH","20260930",7.0,7.0,3.0,3.0,2.0,2.0,1.0,1.0]`,
			`["888889.SH","20260929",9.0,9.0,4.0,4.0,2.0,2.0,1.0,1.0]`,
		}
	}
	joined := ""
	for _, r := range rows {
		if joined != "" {
			joined += ","
		}
		joined += r
	}
	return `{"code":0,"msg":"","data":{"fields":[` +
		`"ts_code","trade_date",` +
		`"buy_elg_amount","sell_elg_amount",` +
		`"buy_lg_amount","sell_lg_amount",` +
		`"buy_md_amount","sell_md_amount",` +
		`"buy_sm_amount","sell_sm_amount"],` +
		`"items":[` + joined + `]}}`
}

// dbt09TushareStub 记录请求并按 ts_code 答数 —— 替身必须**认票**，
// 否则逐 symbol 循环会把两票的数各自答成四行，行数断言就失去意义。
type dbt09TushareStub struct {
	queries []string
	bodies  []string
}

func (s *dbt09TushareStub) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			APIName string `json:"api_name"`
			Params  struct {
				TSCode string `json:"ts_code"`
				Start  string `json:"start_date"`
				End    string `json:"end_date"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		s.bodies = append(s.bodies, req.APIName+"|"+req.Params.TSCode)
		s.queries = append(s.queries, req.Params.Start+".."+req.Params.End)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(dbt09StubBody(req.Params.TSCode)))
	}
}

// newDBT09Registry 用**生产**装配函数造链，只把 TushareClient 的上游指向替身。
// eastmoney 那一员拿的是真 URL（本机不可达）—— 这是刻意的：链顺序一旦被改回
// eastmoney 优先，作业就会失败在真源上，可见、可归因，而不是静默由 Tushare 代答。
func newDBT09Registry(t *testing.T, stubURL string) *source.Registry {
	t.Helper()
	tc := data.NewTushareClient("test-token", stubURL, 0, nil, nil)
	reg := buildDataSourceRegistry(tc, zerolog.Nop())

	require.NotNil(t, reg.GetAdapter("tushare_moneyflow"),
		"生产装配必须注册这扇门（DATA_DISABLE_TUSHARE_MONEYFLOW 不许在测试里开着）")
	assert.Equal(t, []string{"tushare_moneyflow", "eastmoney"},
		reg.ListChains()[source.DataTypeCapitalFlow],
		"链顺序就是本单的裁决本身：Tushare 优先、eastmoney 兜底。换回去必须让这条断言红")
	return reg
}

// newDBT09Harness 的装配顺序与 NewSyncHandler 一致，只把作业行 store 换成
// 内存替身、把 Tushare 上游指向替身；落库传真库 store。
type dbt09Harness struct {
	handler *SyncHandler
	router  *gin.Engine
	stub    *dbt09TushareStub
}

func newDBT09Harness(t *testing.T, persist storage.BulkInserter) *dbt09Harness {
	t.Helper()
	stub := &dbt09TushareStub{}
	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)

	jobs := &memJobStore{}
	queue := sync.NewQueue(jobs)
	jobService := sync.NewJobService(jobs)
	jobService.SetPendingNotifier(queue.NotifyJobAvailable)
	pool := sync.NewWorkerPool(queue, 1)
	jobService.SetRunningCanceller(pool.Cancel)

	reg := newDBT09Registry(t, srv.URL)
	pool.RegisterExecutor(&capitalFlowExecutor{reg: reg, persist: persist})

	h := &SyncHandler{jobService: jobService, queue: queue, workerPool: pool, registry: reg}
	router := gin.New()
	router.POST("/api/sync/jobs", h.createJobHandler)
	return &dbt09Harness{handler: h, router: router, stub: stub}
}

// createAndWait 走一次真实的 HTTP 门，再等作业落定。
func (h *dbt09Harness) createAndWait(t *testing.T, jobType string, params map[string]any) *sync.Job {
	t.Helper()
	body, err := json.Marshal(map[string]any{"type": jobType, "params": params})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/sync/jobs", bytes.NewReader(body)))
	require.Equal(t, http.StatusAccepted, w.Code, "作业门必须 202，实际响应：%s", w.Body.String())

	var created struct {
		JobID string `json:"job_id"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	require.NotEmpty(t, created.JobID, "响应没带 job_id：%s", w.Body.String())
	return waitForMemJob(t, h.handler, created.JobID)
}

func TestDBT09CapitalFlowJobLandsTushareMoneyflowRows(t *testing.T) {
	store := capitalFlowStore(t)
	cleanCapitalFlowProbes(t, store)
	t.Cleanup(func() { cleanCapitalFlowProbes(t, store) })
	require.Empty(t, readCapitalFlowRows(t, store), "前置失败：探针 symbol 上还有残留行")

	h := newDBT09Harness(t, store)
	h.handler.StartWorkerPool()
	t.Cleanup(h.handler.StopWorkerPool)

	settled := h.createAndWait(t, "capital_flow", map[string]any{
		"symbols":    []string{capitalFlowSymA, capitalFlowSymB},
		"start_date": "20260929",
		"end_date":   "20260930",
	})
	require.Equal(t, sync.JobStatusCompleted, settled.Status,
		"作业应成功落定；error_message=%q", settled.ErrorMessage)

	var result struct {
		Persisted int    `json:"persisted"`
		Fetched   int    `json:"fetched"`
		Period    string `json:"period"`
	}
	require.NoError(t, json.Unmarshal(settled.Result, &result))
	assert.Equal(t, 4, result.Persisted, "2 个 symbol × 2 个交易日")
	assert.Equal(t, 4, result.Fetched)
	assert.Equal(t, "daily", result.Period)

	// —— 逐字段读回对拍 ——
	got := readCapitalFlowRows(t, store)
	require.Len(t, got, 4)

	// 请求形状：逐 symbol 各一发（两票 = 两发），窗口必须真的带到源上。
	assert.Len(t, h.stub.bodies, 2, "本门按票逐发请求：两票两发：%v", h.stub.bodies)
	assert.Contains(t, h.stub.bodies, "moneyflow|"+capitalFlowSymA)
	assert.Contains(t, h.stub.bodies, "moneyflow|"+capitalFlowSymB)
	for _, q := range h.stub.queries {
		assert.Equal(t, "20260929..20260930", q, "窗口没到源上，行数就由源而不是请求决定：%s", q)
	}

	for _, r := range got {
		assert.Equal(t, "tushare_moneyflow", r.Source,
			"source 列必须记真正供数的那扇门 —— 读方靠它区分 Tushare 分档口径与东财净流入口径")
		assert.Equal(t, "daily", r.Period)
		assert.False(t, r.IngestTime.IsZero())

		// 本源**不产出**的列一律 NULL（缺失可见，不折 0）。
		assert.Nil(t, r.MainBuy, "%s %s main_buy_amount：moneyflow 没有「主力」这一档 ⇒ NULL", r.Symbol, r.TradeDate)
		assert.Nil(t, r.MainSell, "%s %s main_sell_amount：同上", r.Symbol, r.TradeDate)
		assert.Nil(t, r.MainNet, "%s %s main_net：不拿 super+large 相加硬凑东财口径", r.Symbol, r.TradeDate)
		assert.Nil(t, r.MainRatio, "%s %s main_net_ratio：分母（当日成交额）本源不给", r.Symbol, r.TradeDate)
		assert.Nil(t, r.RetailNet, "%s %s retail_net：散户档是东财定义", r.Symbol, r.TradeDate)
		assert.Nil(t, r.RetailRatio, "%s %s retail_net_ratio：同上", r.Symbol, r.TradeDate)
		assert.Nil(t, r.ClosePrice, "%s %s close_price：moneyflow 不带价格", r.Symbol, r.TradeDate)
		assert.Nil(t, r.ChangePct, "%s %s change_pct：同上", r.Symbol, r.TradeDate)

		switch {
		case r.Symbol == capitalFlowSymA && r.TradeDate.UTC().Format("2006-01-02") == "2026-09-30":
			assert.InDelta(t, 1_000_000.0, derefFloat(t, r.SuperNet, "super_net"), 1e-6)
			assert.InDelta(t, 300_000.0, derefFloat(t, r.LargeNet, "large_net"), 1e-6)
			assert.InDelta(t, -300_000.0, derefFloat(t, r.MediumNet, "medium_net"), 1e-6,
				"卖大于买必须为负，落进库里还是负")
			require.NotNil(t, r.SmallNet, "买卖恰好相等是**真 0**，不许落成 NULL")
			assert.InDelta(t, 0.0, *r.SmallNet, 1e-9)
		case r.Symbol == capitalFlowSymA:
			assert.InDelta(t, 1_000_000.0, derefFloat(t, r.SuperNet, "super_net"), 1e-6)
			assert.InDelta(t, 50_000.0, derefFloat(t, r.LargeNet, "large_net"), 1e-6)
			assert.InDelta(t, -10_000.0, derefFloat(t, r.MediumNet, "medium_net"), 1e-6)
			assert.InDelta(t, 0.0, derefFloat(t, r.SmallNet, "small_net"), 1e-9)
		default:
			// 888889.SH：四档买卖各相等 ⇒ 四个真 0。这一票专门区分
			// 「源给了 0」与「源没给」（NULL），两者在库里必须长得不一样。
			assert.InDelta(t, 0.0, derefFloat(t, r.SuperNet, "super_net"), 1e-9)
			assert.InDelta(t, 0.0, derefFloat(t, r.LargeNet, "large_net"), 1e-9)
			assert.InDelta(t, 0.0, derefFloat(t, r.MediumNet, "medium_net"), 1e-9)
			assert.InDelta(t, 0.0, derefFloat(t, r.SmallNet, "small_net"), 1e-9)
		}
	}
}

// waitForMemJob 与 capitalFlowHarness.waitJobSettled 同一判据，只是这里
// 直接拿着内存 store（harness 结构属于另一个文件，不跨文件改它）。
func waitForMemJob(t *testing.T, h *SyncHandler, jobID string) *sync.Job {
	t.Helper()
	var last *sync.Job
	require.Eventually(t, func() bool {
		j, err := h.jobService.GetJob(context.Background(), jobID)
		if err != nil {
			return false
		}
		last = j
		return j.IsTerminal()
	}, 15*time.Second, 20*time.Millisecond, "作业没有在期限内落定")
	return last
}

// TestDBT09CapitalFlowJobRerunIsIdempotent 钉 upsert 幂等：同一作业重跑
// 不产生重复行（(symbol, trade_date, period) 冲突键在换源之后不变）。
func TestDBT09CapitalFlowJobRerunIsIdempotent(t *testing.T) {
	store := capitalFlowStore(t)
	cleanCapitalFlowProbes(t, store)
	t.Cleanup(func() { cleanCapitalFlowProbes(t, store) })

	h := newDBT09Harness(t, store)
	h.handler.StartWorkerPool()
	t.Cleanup(h.handler.StopWorkerPool)

	params := map[string]any{
		"symbols":    []string{capitalFlowSymA},
		"start_date": "20260929",
		"end_date":   "20260930",
	}

	for run := 1; run <= 2; run++ {
		settled := h.createAndWait(t, "capital_flow", params)
		require.Equal(t, sync.JobStatusCompleted, settled.Status, "第 %d 次跑失败：%s", run, settled.ErrorMessage)

		got := readCapitalFlowRows(t, store)
		assert.Len(t, got, 2, "第 %d 次重跑后行数变了 ⇒ upsert 幂等没成立", run)
	}
}

// TestDBT09CapitalFlowJobFailsVisiblyWhenBothLegsAreDown 钉住「失败可见」这一半：
// 链上两员都不可得时，作业以 error 收口，error_message 里看得见试过谁。
//
// 两个替身都在这里造（不借生产 Registry），因为这条断言必须与网络好坏无关：
// 拿真 push2 当「不可达」的证据，等于把「东财今天理不理我们」变成每天的红
// （AGENTS.md §14 [DATA-10] 记的是**状态**，不是让我在默认套件里赌它）。
// 真源那条腿在 dbt09_moneyflow_live_test.go（tag `dbt09live`）。
//
// 它是破坏验证 (c) 的**对照**：链顺序被换回 eastmoney 优先时，
// 红的是 newDBT09Registry 里那条顺序断言 + 主落库腿（真源不可达 ⇒ 作业失败），
// 本用例则证明「失败」从来不会被写成 completed。
func TestDBT09CapitalFlowJobFailsVisiblyWhenBothLegsAreDown(t *testing.T) {
	tushareDown := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("tushare down for the record"))
	}))
	t.Cleanup(tushareDown.Close)
	push2Down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("push2 502"))
	}))
	t.Cleanup(push2Down.Close)

	reg := source.NewRegistry()
	require.NoError(t, reg.Register(source.NewTushareMoneyflowAdapter(
		data.NewTushareClient("test-token", tushareDown.URL, 0, nil, nil))))
	require.NoError(t, reg.Register(source.NewEastmoneyAdapter(&source.EastmoneyClient{
		BaseURL:       push2Down.URL,
		DataCenterURL: push2Down.URL,
		HTTPClient:    &http.Client{Timeout: 5 * time.Second},
	})))
	// 与生产同形的两员链（顺序也同），否则「兜底」二字在这里没有对象。
	reg.SetChain(source.DataTypeCapitalFlow, []string{"tushare_moneyflow", "eastmoney"})

	jobs := &memJobStore{}
	queue := sync.NewQueue(jobs)
	jobService := sync.NewJobService(jobs)
	jobService.SetPendingNotifier(queue.NotifyJobAvailable)
	pool := sync.NewWorkerPool(queue, 1)
	jobService.SetRunningCanceller(pool.Cancel)
	pool.RegisterExecutor(&capitalFlowExecutor{reg: reg, persist: &countingBulkInserter{}})

	exec, ok := pool.GetExecutor(sync.JobTypeCapitalFlow)
	require.True(t, ok)

	rec := &recordingReporter{}
	_, err := exec.Execute(context.Background(),
		jobWithParams("dbt09-both-down",
			`{"symbols":["888888.SH"],"start_date":"20260929","end_date":"20260930"}`), rec)

	require.Error(t, err, "两员都不可得时作业必须红，不许以 completed 收口（DATA-06 家族）")
	msg := err.Error()
	assert.Contains(t, msg, "tushare_moneyflow", "失败要看得见链上哪几员试过：%v", msg)
	assert.Contains(t, msg, "eastmoney", "兜底腿也要出现在归因里，否则分不清「没兜底」与「兜底也失败」：%v", msg)
	assert.GreaterOrEqual(t, rec.errorCalls, 1, "失败要经 progress 上报，界面才看得见")
}
