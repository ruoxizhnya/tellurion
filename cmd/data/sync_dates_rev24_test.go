package main

// REV-24 回归护栏：调用方指定的日期区间必须真的到达 executor 的 fetch 调用。
//
// 旧缺陷：`syncDividendsRequest` / `syncSplitsRequest` 上有 StartDate/EndDate，
// 但 handler 转 `sync.FundamentalSyncParams` 时只搬了 Symbols，executor 又
// 硬编码 `FetchDividends(ctx, symbol, "", "")` ⇒
// `POST /sync/dividends {"symbols":[...],"start_date":"20240101"}`
// 静默拉全历史。既费配额又让「只补某段时间」的运维动作失真。
//
// 护栏分两段钉住：handler → job.Params（持久化的就是调用方给的区间），
// job.Params → fetch 实参（executor 不丢字段）。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/domain"
	"tellurion/pkg/sync"
)

// rev24Call 记录一次 fetch 的实参。
type rev24Call struct {
	symbol    string
	startDate string
	endDate   string
}

// rev24DividendFetcher 是 dividendsFetcher 的假实现。
type rev24DividendFetcher struct {
	calls   []rev24Call
	records int
	failOn  string
}

func (f *rev24DividendFetcher) FetchDividends(_ context.Context, symbol, startDate, endDate string) ([]domain.Dividend, error) {
	f.calls = append(f.calls, rev24Call{symbol, startDate, endDate})
	if symbol == f.failOn {
		return nil, errors.New("upstream boom")
	}
	return make([]domain.Dividend, f.records), nil
}

// rev24SplitFetcher 是 splitsFetcher 的假实现。
type rev24SplitFetcher struct {
	calls   []rev24Call
	records int
}

func (f *rev24SplitFetcher) FetchSplits(_ context.Context, symbol, startDate, endDate string) ([]domain.Split, error) {
	f.calls = append(f.calls, rev24Call{symbol, startDate, endDate})
	return make([]domain.Split, f.records), nil
}

// rev24FundamentalFetcher 是 fundamentalsFetcher 的假实现。
type rev24FundamentalFetcher struct {
	calls   []rev24Call
	records int
}

func (f *rev24FundamentalFetcher) FetchFundamentalsData(_ context.Context, symbol, startDate, endDate string) ([]domain.FundamentalData, error) {
	f.calls = append(f.calls, rev24Call{symbol, startDate, endDate})
	return make([]domain.FundamentalData, f.records), nil
}

// rev24Job 用给定参数造一个 job（executor 只读 job.Params）。
func rev24Job(t *testing.T, params sync.FundamentalSyncParams) *sync.Job {
	t.Helper()
	raw, err := json.Marshal(params)
	require.NoError(t, err)
	return &sync.Job{ID: "rev24-job", JobType: sync.JobTypeDividends, Params: raw}
}

// TestDividendsExecutor_ForwardsDateRangeToFetch 是主护栏：区间到达 fetch。
func TestDividendsExecutor_ForwardsDateRangeToFetch(t *testing.T) {
	t.Parallel()

	f := &rev24DividendFetcher{records: 3}
	exec := &dividendsExecutor{tc: f}

	got, err := exec.Execute(context.Background(),
		rev24Job(t, sync.FundamentalSyncParams{
			Symbols:   []string{"600000.SH", "000001.SZ"},
			StartDate: "20240101",
			EndDate:   "20241231",
		}), &rev24Progress{})

	require.NoError(t, err)
	require.Len(t, f.calls, 2)
	for _, c := range f.calls {
		assert.Equal(t, "20240101", c.startDate, "start_date 必须贯通到 fetch")
		assert.Equal(t, "20241231", c.endDate, "end_date 必须贯通到 fetch")
	}

	m := got.(map[string]any)
	assert.Equal(t, 2, m["stocks_synced"])
	assert.Equal(t, 6, m["records_saved"])
	assert.Equal(t, 0, m["failed_stocks"])
}

// TestDividendsExecutor_EmptyDatesStayEmpty：调用方没给区间时仍传空串
// （= 上游默认全历史）。贯通不得顺手塞一个「默认区间」——那是静默改契约。
func TestDividendsExecutor_EmptyDatesStayEmpty(t *testing.T) {
	t.Parallel()

	f := &rev24DividendFetcher{records: 1}
	exec := &dividendsExecutor{tc: f}

	_, err := exec.Execute(context.Background(),
		rev24Job(t, sync.FundamentalSyncParams{Symbols: []string{"600000.SH"}}), nil)
	require.NoError(t, err)

	require.Len(t, f.calls, 1)
	assert.Empty(t, f.calls[0].startDate)
	assert.Empty(t, f.calls[0].endDate)
}

// TestDividendsExecutor_FailedSymbolDoesNotAbortBatch：一只失败不影响其余
// （与 helper 口径一致；executor 层不得回退成「一处失败整批失败」）。
func TestDividendsExecutor_FailedSymbolDoesNotAbortBatch(t *testing.T) {
	t.Parallel()

	f := &rev24DividendFetcher{records: 2, failOn: "000001.SZ"}
	exec := &dividendsExecutor{tc: f}

	got, err := exec.Execute(context.Background(),
		rev24Job(t, sync.FundamentalSyncParams{Symbols: []string{"600000.SH", "000001.SZ", "600519.SH"}}), nil)

	require.NoError(t, err)
	m := got.(map[string]any)
	assert.Equal(t, 2, m["stocks_synced"])
	assert.Equal(t, 4, m["records_saved"])
	assert.Equal(t, 1, m["failed_stocks"])
}

// TestSplitsExecutor_ForwardsDateRangeToFetch：splits 同尺。
func TestSplitsExecutor_ForwardsDateRangeToFetch(t *testing.T) {
	t.Parallel()

	f := &rev24SplitFetcher{records: 1}
	exec := &splitsExecutor{tc: f}

	_, err := exec.Execute(context.Background(),
		rev24Job(t, sync.FundamentalSyncParams{
			Symbols:   []string{"600000.SH"},
			StartDate: "20220101",
			EndDate:   "20221231",
		}), nil)

	require.NoError(t, err)
	require.Len(t, f.calls, 1)
	assert.Equal(t, "20220101", f.calls[0].startDate)
	assert.Equal(t, "20221231", f.calls[0].endDate)
}

// TestFundamentalsExecutor_UsesDateForBothBounds：fundamentals 只有单个
// `date` 字段，历史上两端都传它（快照式取数）。改去重时不得把这条语义改掉。
func TestFundamentalsExecutor_UsesDateForBothBounds(t *testing.T) {
	t.Parallel()

	f := &rev24FundamentalFetcher{records: 1}
	exec := &fundamentalsExecutor{tc: f}

	_, err := exec.Execute(context.Background(),
		rev24Job(t, sync.FundamentalSyncParams{Symbols: []string{"600000.SH"}, Date: "20240630"}), nil)

	require.NoError(t, err)
	require.Len(t, f.calls, 1)
	assert.Equal(t, "20240630", f.calls[0].startDate)
	assert.Equal(t, "20240630", f.calls[0].endDate)
}

// TestSyncHandlers_ForwardDateRangeIntoJobParams：handler 段——调用方给区间，
// 落进 job.Params 的必须还是同一对日期（旧代码在这里丢的）。
func TestSyncHandlers_ForwardDateRangeIntoJobParams(t *testing.T) {
	t.Parallel()

	cases := []struct {
		path     string
		jobType  sync.JobType
		withDate bool
	}{
		{"/sync/dividends", sync.JobTypeDividends, false},
		{"/sync/splits", sync.JobTypeSplits, false},
		{"/sync/fundamentals", sync.JobTypeFundamentals, true},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			jobStore := newRev12JobStore()
			h := &SyncHandler{jobService: sync.NewJobService(jobStore)}
			r := rev12Routes(t, h)

			body := `{"symbols":["600000.SH"],"start_date":"20240101","end_date":"20241231"}`
			if tc.withDate {
				body = `{"symbols":["600000.SH"],"date":"20240630"}`
			}
			w := rev12Post(r, tc.path, body)
			require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())

			var resp struct {
				JobID string `json:"job_id"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

			jobStore.mu.Lock()
			job, ok := jobStore.jobs[resp.JobID]
			jobStore.mu.Unlock()
			require.True(t, ok, "作业应已创建")
			require.Equal(t, tc.jobType, job.JobType)

			var params sync.FundamentalSyncParams
			require.NoError(t, json.Unmarshal(job.Params, &params))
			if tc.withDate {
				assert.Equal(t, "20240630", params.Date)
				return
			}
			assert.Equal(t, "20240101", params.StartDate, "start_date 不得在转 params 时丢掉")
			assert.Equal(t, "20241231", params.EndDate, "end_date 不得在转 params 时丢掉")
		})
	}
}
