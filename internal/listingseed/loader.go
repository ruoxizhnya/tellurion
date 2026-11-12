// Package listingseed 取 tellurion-data 的上市/退市日历并接进回测引擎
// （CORE-LISTING-SEED-UNWIRED）。
//
// 数据面契约（tellurion-data `72b96aa`）：
//
//	GET {data_service.url}/api/v1/stocks/listing-windows
//	200 {"as_of":"2026-11-12","trade_date":null,"count":5923,
//	     "windows":{"600519.SH":{"list_date":"2001-08-27","delist_date":null,
//	                             "delisted":false,"status":"active"}, ...}}
//
// 映射：`list_date` → ListingWindow.List（null ⇒ 零值，左端不过滤）；
// `delist_date` → ListingWindow.Delist（null ⇒ nil）；`delisted` 与
// `status` 只做自述校验，不作为过滤依据 —— 过滤口径由 IsListed 按日期判。
//
// **失败语义（与引擎既有口径一致）**：接口不可用/格式不认识时**不合成**
// 任何窗口，Snapshot.Windows 留空、Degraded=true 并记下原因。引擎拿到空
// 日历的行为是「不过滤 + 已知偏差 pool_source=current」；绝不能把「没取到」
// 翻译成「整批股票都在市」—— 那正是幸存者偏差本身。
package listingseed

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-kit/httpclient"

	"tellurion/pkg/backtest"
)

const (
	// ListingWindowsPath 是 tellurion-data 的上市/退市日历读面。
	ListingWindowsPath = "/api/v1/stocks/listing-windows"

	listingFetchTimeout = 20 * time.Second
	listingFetchRetries = 2

	// PoolSourcePointInTime 表示票池按「每个回放日的真实在市名单」过滤
	// （接入成功）。PoolSourceCurrent 表示按**今天的**名单回测历史 ——
	// 幸存者偏差仍然存在。两个值与 agent 的 validation.PoolSource* 同名，
	// 便于跨仓对读。
	PoolSourcePointInTime = "point_in_time"
	PoolSourceCurrent     = "current"
)

// Snapshot 是一次取数的产物与来源状态。
type Snapshot struct {
	// Windows 为空表示没有可用日历（未配置、取数失败或响应里没有窗口）。
	Windows map[string]backtest.ListingWindow
	// AsOf 是数据面的口径日期（响应里的 as_of），解析失败为零值。
	AsOf time.Time
	// FetchedAt 是本地取数完成时刻。
	FetchedAt time.Time
	// Degraded=true 表示本次没有拿到可信日历；Reason 说明原因。
	Degraded bool
	Reason   string
}

// WindowCount 返回窗口条数（nil 安全）。
func (s Snapshot) WindowCount() int { return len(s.Windows) }

// Status 是偏差标记的机器可读面（GET /api/listing-windows/status）。
type Status struct {
	// PoolSource 取 PoolSourcePointInTime / PoolSourceCurrent。
	PoolSource string `json:"pool_source"`
	// Windows 是接入的窗口条数；Delisted 是其中带退市日的条数。
	Windows  int `json:"windows"`
	Delisted int `json:"delisted"`
	// AsOf 是数据面的口径日期；FetchedAt 是本地成功取数时刻。未取到则为零 -> 省略。
	AsOf      *time.Time `json:"as_of,omitempty"`
	FetchedAt *time.Time `json:"fetched_at,omitempty"`
	Degraded  bool       `json:"degraded"`
	Reason    string     `json:"reason,omitempty"`
}

// Loader 负责取数、缓存快照并把快照交给引擎。
//
// 并发：Refresh 与 Status/Windows 可并发；Refresh 内部串行（mu 保护写入）。
type Loader struct {
	client *httpclient.Client
	token  string
	logger zerolog.Logger

	mu   sync.RWMutex
	snap Snapshot
}

// NewLoader 构造加载器。baseURL 为空返回 nil —— 调用方据此跳过接线，
// 由 status 读面如实报「未配置」（而不是假装接入成功）。
func NewLoader(baseURL, token string, logger zerolog.Logger) *Loader {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return nil
	}
	return &Loader{
		client: httpclient.New(base, listingFetchTimeout, listingFetchRetries),
		token:  strings.TrimSpace(token),
		logger: logger.With().Str("component", "listing_seed").Logger(),
	}
}

// listingWindowsResponse 是数据面响应。
//
// 字段用指针区分「null（上市日未知 / 未退市）」与「零值」；未知字段忽略。
type listingWindowsResponse struct {
	AsOf    string                       `json:"as_of"`
	Count   int                          `json:"count"`
	Windows map[string]listingWindowItem `json:"windows"`
}

type listingWindowItem struct {
	ListDate   *string `json:"list_date"`
	DelistDate *string `json:"delist_date"`
	Delisted   bool    `json:"delisted"`
	Status     string  `json:"status"`
}

// Refresh 拉一次全市场上市日历并更新快照。
//
// 失败时：快照被替换为 Degraded（Windows 置空），返回 error —— 调用方必须
// 记日志，不能把错误吞掉。返回的 Snapshot 与内部缓存一致。
func (l *Loader) Refresh(ctx context.Context) (Snapshot, error) {
	resp, err := l.get(ctx, ListingWindowsPath)
	if err != nil {
		return l.fail(err)
	}

	var payload listingWindowsResponse
	if err := json.Unmarshal(resp.Body, &payload); err != nil {
		return l.fail(fmt.Errorf("decode listing windows: %w", err))
	}
	if len(payload.Windows) == 0 {
		return l.fail(fmt.Errorf("listing windows response is empty (count=%d)", payload.Count))
	}

	windows := make(map[string]backtest.ListingWindow, len(payload.Windows))
	delisted := 0
	var badDates []string
	for symbol, item := range payload.Windows {
		if strings.TrimSpace(symbol) == "" {
			continue
		}
		var w backtest.ListingWindow
		if item.ListDate != nil {
			parsed, err := parseDate(*item.ListDate)
			if err != nil {
				// 不猜：这一条留零值（左端不过滤），并记下来。
				badDates = append(badDates, symbol+".list_date="+*item.ListDate)
			} else {
				w.List = parsed
			}
		}
		if item.DelistDate != nil {
			parsed, err := parseDate(*item.DelistDate)
			if err != nil {
				badDates = append(badDates, symbol+".delist_date="+*item.DelistDate)
			} else {
				w.Delist = &parsed
				delisted++
			}
		}
		windows[symbol] = w
	}

	snap := Snapshot{
		Windows:   windows,
		AsOf:      parseAsOf(payload.AsOf),
		FetchedAt: time.Now().UTC(),
	}
	if len(badDates) > 0 {
		// 部分坏日期不阻断整批（数据面单条的格式错误不该让全市场退回
		// pool_source=current），但必须吵：这条票的对应边界被当成「未知」。
		l.logger.Warn().Strs("entries", badDates).
			Msg("listing windows: unparsable dates kept as unknown bounds")
	}
	l.mu.Lock()
	l.snap = snap
	l.mu.Unlock()

	l.logger.Info().
		Int("windows", len(windows)).
		Int("delisted", delisted).
		Time("as_of", snap.AsOf).
		Msg("listing windows seeded into backtest engine")
	return snap, nil
}

// fail 记录失败快照（Windows 置空 = 不合成），并把原因返回给调用方。
func (l *Loader) fail(err error) (Snapshot, error) {
	snap := Snapshot{
		FetchedAt: time.Now().UTC(),
		Degraded:  true,
		Reason:    err.Error(),
	}
	l.mu.Lock()
	l.snap = snap
	l.mu.Unlock()
	l.logger.Error().Err(err).
		Msg("listing windows unavailable; backtest universe stays unfiltered (pool_source=current)")
	return snap, err
}

// Snapshot 返回最近一次取数结果（深拷贝，调用方可自由改）。
func (l *Loader) Snapshot() Snapshot {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.snap.clone()
}

// Windows 返回可直接交给 Engine.SetListingWindows 的窗口表（深拷贝）。
// 未取到（Degraded）时返回 nil —— 不合成。
func (l *Loader) Windows() map[string]backtest.ListingWindow {
	return l.Snapshot().Windows
}

// Status 汇总偏差标记。
func (l *Loader) Status() Status {
	snap := l.Snapshot()
	st := Status{
		PoolSource: PoolSourceCurrent,
		Windows:    len(snap.Windows),
		Degraded:   snap.Degraded,
		Reason:     snap.Reason,
	}
	if !snap.Degraded && len(snap.Windows) > 0 {
		st.PoolSource = PoolSourcePointInTime
		for _, w := range snap.Windows {
			if w.Delist != nil {
				st.Delisted++
			}
		}
	}
	if !snap.AsOf.IsZero() {
		asOf := snap.AsOf
		st.AsOf = &asOf
	}
	if !snap.FetchedAt.IsZero() {
		fetchedAt := snap.FetchedAt
		st.FetchedAt = &fetchedAt
	}
	if st.Degraded && st.Reason == "" {
		st.Reason = "listing windows not loaded"
	}
	return st
}

// UnavailableStatus 是「加载器未配置（data_service.url 为空）」时的状态：
// 明确声明池子来源仍是 current，而不是假装已接入。
func UnavailableStatus(reason string) Status {
	return Status{PoolSource: PoolSourceCurrent, Degraded: true, Reason: reason}
}

func (s Snapshot) clone() Snapshot {
	if s.Windows == nil {
		return s
	}
	out := make(map[string]backtest.ListingWindow, len(s.Windows))
	for symbol, w := range s.Windows {
		if w.Delist != nil {
			delist := *w.Delist
			w.Delist = &delist
		}
		out[symbol] = w
	}
	s.Windows = out
	return s
}

func (l *Loader) get(ctx context.Context, path string) (*httpclient.Response, error) {
	var headers map[string]string
	if l.token != "" {
		headers = map[string]string{"Authorization": "Bearer " + l.token}
	}
	resp, err := l.client.Do(ctx, httpclient.Request{Method: "GET", Path: path, Headers: headers})
	if err != nil {
		return nil, fmt.Errorf("listing windows upstream unavailable: %w", err)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("listing windows upstream status %d: %s",
			resp.StatusCode, strings.TrimSpace(string(resp.Body)))
	}
	return resp, nil
}

func parseDate(raw string) (time.Time, error) {
	return time.Parse("2006-01-02", strings.TrimSpace(raw))
}

func parseAsOf(raw string) time.Time {
	parsed, err := parseDate(raw)
	if err != nil {
		return time.Time{}
	}
	return parsed
}
