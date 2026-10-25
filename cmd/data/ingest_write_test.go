package main

// QT-40 H4 ① 的跨仓写面客户端证据。
//
// 这个文件钉的是**边界变了之后新出现的那些失败**：
//   - 未配置 ⇒ 响亮失败，且不回落本地 BulkInsert（回落 = 把刚消灭的第二写者
//     藏进一条 fallback 分支里）；
//   - 投送面上没有 `visible_at` / `ingest_time`；
//   - 有界（分片按点数，报文按字节），超限不截断；
//   - 对端的每种失败（401 / 403 / 409 / 413 / 503）在作业 error 里说的是
//     人话，运维处置动作各不相同 —— 409 尤其不能读成「重试一下就好」。
//
// 分片与计数用绝对数值断言，不从常量推导（阈值类守卫的老规矩）。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/data/source"
)

// testPoint 是一个最小合法点。
func testPoint(symbol string, day int) source.UnifiedDataPoint {
	return source.UnifiedDataPoint{
		Symbol:     symbol,
		TradeTime:  time.Date(2026, 1, day, 0, 0, 0, 0, time.UTC),
		Source:     "tushare",
		DataType:   source.DataTypeCapitalFlow,
		Data:       map[string]interface{}{"close": 1.5},
		IngestTime: time.Date(2026, 10, 25, 3, 4, 5, 0, time.UTC),
		VisibleAt:  time.Date(2026, 1, 20, 7, 0, 0, 0, time.UTC),
	}
}

// pointsOf 造 n 个票（symbol 互不相同、坐标互不相同）。
func pointsOf(n int) []source.UnifiedDataPoint {
	out := make([]source.UnifiedDataPoint, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, testPoint(fmt.Sprintf("6%05d.SZ", i), 1+i%20))
	}
	return out
}

func clientFor(svc *stubIngestService, maxPoints int) *ingestWriteClient {
	srv := httptest.NewServer(svc.handler())
	return newIngestWriteClient(ingestWriteConfig{
		BaseURL:   srv.URL,
		Token:     "tok",
		Timeout:   5 * time.Second,
		MaxPoints: maxPoints,
		Client:    srv.Client(),
	})
}

// 未配置 ⇒ 构造出 nil，且**工厂**返回的是 nil 接口（不是装着 nil 指针的接口），
// 于是执行器的 `poster == nil` 判据真的成立。
func TestIngestWriteFactoryReturnsNilInterfaceWhenUnconfigured(t *testing.T) {
	assert.Nil(t, newIngestWriteClient(ingestWriteConfig{BaseURL: "  "}), "空 base_url 不该造出客户端")

	// Go 的经典坑：`return client`（client 是类型化的 nil 指针）赋给接口后
	// `== nil` 为假，执行器的 nil 判据从此形同虚设 —— 所以钉的是工厂的返回值，
	// 不是构造函数的。
	defer viper.Set("ingest_write.base_url", nil)
	viper.Set("ingest_write.base_url", "")
	var poster pointsPoster = newIngestWritePoster()
	assert.True(t, poster == nil, "未配置时工厂必须交出 nil 接口，否则 nil 判据不成立")

	viper.Set("ingest_write.base_url", "http://127.0.0.1:8081")
	poster = newIngestWritePoster()
	require.False(t, poster == nil, "配了 base_url 就该造出客户端（对照腿，否则上面那条恒真）")
	c, ok := poster.(*ingestWriteClient)
	require.True(t, ok, "配好时交出的就是那条客户端")
	assert.Equal(t, "http://127.0.0.1:8081", c.cfg.BaseURL)

	// 直接对 nil 客户端调用也必须落在同一个哨兵上（防「空指针 panic 当失败」）。
	var nilClient *ingestWriteClient
	_, _, err := nilClient.PostPoints(context.Background(), source.DataTypeCapitalFlow, pointsOf(1))
	require.ErrorIs(t, err, ErrIngestWriteUnconfigured)
}

// 一次合法投点：报文形状、请求头、回执计数。
func TestIngestWriteClientSendsTheBoundedWireShape(t *testing.T) {
	svc := &stubIngestService{}
	persisted, skipped, err := clientFor(svc, 10000).PostPoints(context.Background(),
		source.DataTypeCapitalFlow, []source.UnifiedDataPoint{testPoint("600000.SH", 15), testPoint("000001.SZ", 16)})
	require.NoError(t, err)
	assert.Equal(t, 2, persisted)
	assert.Zero(t, skipped)

	raw, auth := svc.seen()
	require.Len(t, raw, 1)
	assert.Equal(t, "Bearer tok", auth[0], "服务间凭证必须带上；不带上就是对端 401")

	assert.NotContains(t, raw[0], "visible_at", "可见时刻由对端按声明锚点推导，不接受投送")
	assert.NotContains(t, raw[0], "ingest_time", "入库时刻由对端时钟填，不接受投送")
	var req ingestWireRequest
	require.NoError(t, json.Unmarshal([]byte(raw[0]), &req))
	assert.Equal(t, source.DataTypeCapitalFlow, req.DataType)
	require.Len(t, req.Points, 2)
	assert.Equal(t, "600000.SH", req.Points[0].Symbol)
	assert.False(t, req.Points[0].TradeTime.IsZero())
	assert.Equal(t, map[string]any{"close": 1.5}, req.Points[0].Data)
}

// 分片：25 点上界 10 ⇒ 三片 10/10/5，计数按片累加。
func TestIngestWriteClientShardsByTheConfiguredLimit(t *testing.T) {
	svc := &stubIngestService{}
	persisted, _, err := clientFor(svc, 10).PostPoints(context.Background(), source.DataTypeCapitalFlow, pointsOf(25))
	require.NoError(t, err)
	assert.Equal(t, 25, persisted, "每片回执都要累进总计数，漏一片就是假少")

	raw, _ := svc.seen()
	require.Len(t, raw, 3, "25 点 / 每片 10 必须是 3 片")
	var sizes []int
	for _, body := range raw {
		var req ingestWireRequest
		require.NoError(t, json.Unmarshal([]byte(body), &req))
		sizes = append(sizes, len(req.Points))
	}
	assert.Equal(t, []int{10, 10, 5}, sizes, "分片大小写死判：静默少投一片就是把数据丢在网络上")
}

// 一片失败时，**已经落的前面几片**必须如实带在计数里。
//
// 这条是 H4 ① 的边界变化直接造出来的形状：老实现里「落库失败 = 这个事务一行
// 都没落」是真的，而跨仓之后片与片不共享事务。计数丢了，`sync_jobs.result.persisted`
// 就系统性少报 —— 少报的 persisted 与「静默丢数据」在读侧长得一模一样。
func TestIngestWriteClientCarriesPartialShardCountsOnError(t *testing.T) {
	svc := &stubIngestService{failOnRequest: 2}
	persisted, _, err := clientFor(svc, 2).PostPoints(context.Background(), source.DataTypeCapitalFlow, pointsOf(5))

	require.Error(t, err, "第 2 片被拒 ⇒ 整次调用是失败")
	assert.Equal(t, 2, persisted, "第 1 片真的落了 2 个点，不许因为后面失败就报 0")
	raw, _ := svc.seen()
	assert.Len(t, raw, 2, "第 2 片失败就该停在这里，不该再去投第 3 片：%d 片", len(raw))
}

// 配置把上界写成大于对端硬限时必须夹住（夹住而不是拒绝：拒绝让运维以为
// 999999 是个合法意图，而真实原因是对端的 10000）。
func TestIngestWriteClientClampsOverLargeLimit(t *testing.T) {
	c := newIngestWriteClient(ingestWriteConfig{BaseURL: "http://127.0.0.1:1", MaxPoints: 999999})
	require.NotNil(t, c)
	assert.Equal(t, ingestWriteHardMaxPoints, c.cfg.MaxPoints, "上界必须夹到对端硬限")

	c = newIngestWriteClient(ingestWriteConfig{BaseURL: "http://127.0.0.1:1", MaxPoints: 0})
	assert.Equal(t, ingestWriteHardMaxPoints, c.cfg.MaxPoints, "非正数按默认上界处理")

	c = newIngestWriteClient(ingestWriteConfig{BaseURL: "http://127.0.0.1:1", MaxPoints: 250})
	assert.Equal(t, 250, c.cfg.MaxPoints, "合法的小于硬限的值必须原样保留，运维要能调")
}

// 409 ⇒ ErrIngestContentConflict。这条必须能被 errors.Is 分诊：它的处置
// 是「人工判定上游是否改了历史」，与「网络抖了」是两回事。
func TestIngestWriteClientSurfacesContentConflict(t *testing.T) {
	svc := &stubIngestService{status: http.StatusConflict, code: "content_conflict", message: "row exists with different content"}
	_, _, err := clientFor(svc, 10000).PostPoints(context.Background(), source.DataTypeCapitalFlow, pointsOf(1))
	require.Error(t, err)
	require.ErrorIs(t, err, ErrIngestContentConflict)
	assert.Contains(t, err.Error(), "content_conflict", "对端的码要留在报文里，否则人只能猜")
}

// 401 / 403 / 503 / 413 各自点名处置方向。
func TestIngestWriteClientNamesTheOperatorAction(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		code     string
		token    string
		contains []string
	}{
		{"401 without token", http.StatusUnauthorized, "unauthenticated", "", []string{"ingest_write.token 为空"}},
		{"401 with token", http.StatusUnauthorized, "unauthenticated", "tok", []string{"JWT_SECRET"}},
		{"403 viewer", http.StatusForbidden, "forbidden", "tok", []string{"写面档位只开 service / admin"}},
		{"503 unwired", http.StatusServiceUnavailable, "dependency_unavailable", "tok", []string{"DATABASE_URL"}},
		{"413 too large", http.StatusRequestEntityTooLarge, "batch_too_large", "tok", []string{"ingest_write.max_points"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &stubIngestService{status: tc.status, code: tc.code, message: "peer says no"}
			srv := httptest.NewServer(svc.handler())
			defer srv.Close()
			client := newIngestWriteClient(ingestWriteConfig{
				BaseURL: srv.URL, Token: tc.token, MaxPoints: 10000, Client: srv.Client(), Timeout: 5 * time.Second,
			})
			_, _, err := client.PostPoints(context.Background(), source.DataTypeCapitalFlow, pointsOf(1))
			require.Error(t, err)
			for _, want := range tc.contains {
				assert.Contains(t, err.Error(), want, "失败报文要点名该改的那一处：%v", err)
			}
		})
	}
}

// 对端 200 但计数对不上账 ⇒ 响亮失败，不把可疑计数写进 sync_jobs.result。
func TestIngestWriteClientRejectsImpossibleCounts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data_type":"capital_flow","received":1,"persisted":5,"skipped":5}`))
	}))
	defer srv.Close()
	client := newIngestWriteClient(ingestWriteConfig{BaseURL: srv.URL, MaxPoints: 10000, Client: srv.Client()})

	_, _, err := client.PostPoints(context.Background(), source.DataTypeCapitalFlow, pointsOf(1))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "超过投送量", "persisted+skipped 大于投出去的量就是口径漂移，必须拒绝：%v", err)

	// 计数一并归零（与上面那条「部分成功要带出来」相反，理由是不同的事）：
	// 部分成功是「知道落了几行」，账对不上是「不知道」—— 不知道就不许报一个数，
	// 因为那个数会原样进 sync_jobs.result 被人当事实。
	persisted, skipped, err := client.PostPoints(context.Background(), source.DataTypeCapitalFlow, pointsOf(1))
	require.Error(t, err)
	assert.Zero(t, persisted, "不自洽的计数不许带出去（persisted=%d）", persisted)
	assert.Zero(t, skipped, "同上（skipped=%d）", skipped)
}

// 回执 data_type 与请求不符 ⇒ 失败。读起来像吹毛求疵，但一片被对端按别的表
// 落库是**静默换表**，比失败坏得多。
func TestIngestWriteClientRejectsMismatchedEcho(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data_type":"ohlcv_daily","received":1,"persisted":1}`))
	}))
	defer srv.Close()
	client := newIngestWriteClient(ingestWriteConfig{BaseURL: srv.URL, MaxPoints: 10000, Client: srv.Client()})

	_, _, err := client.PostPoints(context.Background(), source.DataTypeCapitalFlow, pointsOf(1))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "与请求不符")
}

// ctx 取消要挡住后面的片：前两片已落，第三片不该再投。
func TestIngestWriteClientStopsBetweenShardsOnCancel(t *testing.T) {
	svc := &stubIngestService{}
	client := clientFor(svc, 10)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := client.PostPoints(ctx, source.DataTypeCapitalFlow, pointsOf(25))
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	raw, _ := svc.seen()
	assert.Empty(t, raw, "已取消的上下文一次都不该投出去")
}

// 空批次不是「投一个空数组」：对端会按 invalid_batch 拒，白跑一跳网络。
func TestIngestWriteClientSkipsEmptyBatch(t *testing.T) {
	svc := &stubIngestService{}
	persisted, skipped, err := clientFor(svc, 100).PostPoints(context.Background(), source.DataTypeCapitalFlow, nil)
	require.NoError(t, err)
	assert.Zero(t, persisted)
	assert.Zero(t, skipped)
	raw, _ := svc.seen()
	assert.Empty(t, raw, "没有点就不该发请求")
}

// 字节上限腿：点数在上界内但载荷很宽 ⇒ 当场失败并点名配置，不让对端回 400
// 被人误读成「数据有问题」。
func TestIngestWriteClientRejectsOversizedChunkBeforeSending(t *testing.T) {
	fat := []source.UnifiedDataPoint{{
		Symbol: "600000.SH", TradeTime: time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC),
		Source: "tushare", DataType: source.DataTypeCapitalFlow,
		Data: map[string]interface{}{"blob": strings.Repeat("x", ingestWriteBodyLimit+1024)},
	}}
	svc := &stubIngestService{}
	_, _, err := clientFor(svc, 10000).PostPoints(context.Background(), source.DataTypeCapitalFlow, fat)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max_points", "要指得到出路（把每片调小）：%v", err)
	raw, _ := svc.seen()
	assert.Empty(t, raw, "本侧就知道超限，不该占对端一跳")
}

// ---- runIngestStep：core 这一侧只取数与归一 ----

// 执行器未配写面（poster 为 nil）⇒ 响亮失败。这条腿存在的意义是把「不回落」
// 变成可判定的事实：有人日后加了 fallback，这条就红。
func TestRunIngestStepFailsLoudlyWithoutWriteFace(t *testing.T) {
	h := newCapitalFlowHarness(t, nil, http.StatusOK)
	rec := &recordingReporter{}
	_, err := h.executor(t).Execute(context.Background(),
		jobWithParams("h4-unwired", `{"symbols":["888888.SH"],"start_date":"20261015","end_date":"20261016"}`), rec)

	require.Error(t, err, "写面没配 ⇒ 作业必须失败，不许静默空跑")
	assert.ErrorIs(t, err, ErrIngestWriteUnconfigured, "error_message 要指到配置键：%v", err)
	_, paths := h.stub.seen()
	assert.Empty(t, paths, "写面没配就连源都不该打——打了也是取回来扔掉")
}

// core **不再**在本侧筛点：重复坐标也要原样投出去，由对端的幂等坐标去重。
// 在 core 先筛一遍等于用一套可能与对端漂移的口径决定「哪些点不被看见」，
// 而那些点会以 skipped 的名义进 sync_jobs.result，读的人分不清是谁筛的。
func TestRunIngestStepDoesNotDedupeLocally(t *testing.T) {
	svc := &stubIngestService{}
	h := newCapitalFlowHarness(t, ingestPosterFor(t, svc, 10000), http.StatusOK)

	// 同一个票投两次：源替身每次都按这个票回 2 行 ⇒ 共 4 个点，一个都不该在本侧消失。
	rec := &recordingReporter{}
	res, err := h.executor(t).Execute(context.Background(),
		jobWithParams("h4-no-local-dedupe", `{"symbols":["888888.SH","888888.SH"],"start_date":"20261015","end_date":"20261016"}`), rec)
	require.NoError(t, err, "%v", err)

	result := res.(map[string]any)
	assert.Equal(t, 4, result["fetched"], "重复坐标不在本侧筛掉：4 行都取回来了")

	raw, _ := svc.seen()
	require.Len(t, raw, 1)
	var req ingestWireRequest
	require.NoError(t, json.Unmarshal([]byte(raw[0]), &req))
	assert.Len(t, req.Points, 4, "投出去 4 个点 ⇒ 去重完全由对端计数（fetched 与 persisted 的差是它的口径）")
}

// 装配缺谁就报谁：nil registry / nil poster 各自一条，最后才是
// 「归一化给出空来源 ⇒ 当场点名是哪条记录」，而不是收对端一句 400。
func TestRunIngestStepRejectsEmptySourceFromNormalizer(t *testing.T) {
	// 前置门：缺哪个就点名哪个，顺序不许含糊成「Fetch 失败」。
	reg := source.NewRegistry()
	poster := &countingPoster{}
	emptySourceNormalizer := func(item source.DataItem, _, dataType string) source.UnifiedDataPoint {
		return source.UnifiedDataPoint{Symbol: item.Symbol, TradeTime: item.TradeTime, Data: item.Data}
	}

	_, err := runIngestStep(context.Background(), nil, poster,
		source.FetchRequest{DataType: source.DataTypeCapitalFlow}, emptySourceNormalizer)
	require.Error(t, err, "nil registry 必须当场失败")
	assert.Contains(t, err.Error(), "nil registry", "%v", err)

	_, err = runIngestStep(context.Background(), reg, nil,
		source.FetchRequest{DataType: source.DataTypeCapitalFlow}, emptySourceNormalizer)
	require.Error(t, err, "没配写面必须撞那个哨兵，而不是撞 Fetch")
	assert.ErrorIs(t, err, ErrIngestWriteUnconfigured, "%v", err)

	// 装配齐了才轮到「空来源」这条判据：走一条真 Registry（替身源）+ 一个不给 source 的归一化器。
	h := newCapitalFlowHarness(t, poster, http.StatusOK)
	exec, ok := h.executor(t).(*capitalFlowExecutor)
	require.True(t, ok)
	_, err = runIngestStep(context.Background(), exec.reg, poster,
		source.FetchRequest{DataType: source.DataTypeCapitalFlow, Symbols: []string{"888888.SH"},
			StartDate: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC), EndDate: time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)},
		emptySourceNormalizer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty source", "空来源要点名到票：%v", err)
	assert.Zero(t, poster.calls, "被拦下的批次不该已经投出去")
}
