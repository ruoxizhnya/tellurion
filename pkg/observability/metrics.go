// Package observability provides Prometheus metrics and request_id /
// trace propagation middleware for the Quant Lab services.
//
// Sprint 6 P0-3 (ADR-017 §1): the four core metrics exposed at
// /metrics are:
//
//  1. backtest_duration_seconds{strategy,universe}     — Histogram
//  2. http_client_requests_total{service,status}       — Counter
//  3. llm_tokens_total{provider,model}                 — Counter
//  4. cache_hit_ratio{kind}                            — Gauge
//
// Plus a request_id (X-Request-ID header) that is generated for
// inbound requests and propagated to outbound HTTP calls so log
// lines can be correlated end-to-end across services.
//
// K16 ② · 侧车 round-trip 观测（NautilusTrader 双内核）：另注册两条
// NT 侧车专用指标 —— nt_sidecar_calls_total{outcome}（成败计数，失败率 =
// outcome!="ok" 的速率占比）与 nt_sidecar_call_duration_seconds{outcome}
// （延迟分布）。ObserveNTCall 隐式实现 nautilus.RunObserver（消费方定义的
// 窄接口；编译期断言在装配侧 cmd/analysis/setup.go）。outcome 取值封闭为
// ok/unavailable/timeout/internal，与 pkg/errors 错误码族对齐，标签基数有界。
//
// QT-12 ⑤ · run 准入与内存账：另注册四条 Gauge —— backtest_runs_active、
// backtest_runs_queued、backtest_cache_bytes{kind}、backtest_wasm_bytes。
// 对应的四个方法（SetRunsActive / SetRunsQueued / SetCacheBytes /
// SetWasmBytes）隐式实现 backtest.RunObserver，编译期断言同样在
// cmd/analysis/setup.go。四条都是「现在是多少」而不是「累计多少」，
// 所以都不带 _total 后缀 —— 那个后缀在 Prometheus 约定里专指单调递增。
package observability

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// RequestIDHeader is the canonical header used to propagate the
// per-request correlation token. It is intentionally distinct from
// the W3C `traceparent` header so that we can layer OTel on top
// later without breaking existing tooling that scrapes X-Request-ID.
const RequestIDHeader = "X-Request-ID"

// Metrics is the central handle to the four core metrics described
// in ADR-017 §1. Construct it once at process startup via NewMetrics
// and share it via dependency injection.
type Metrics struct {
	BacktestDuration *prometheus.HistogramVec
	HTTPRequests     *prometheus.CounterVec
	LLMTokens        *prometheus.CounterVec
	CacheHitRatio    *prometheus.GaugeVec

	// K16 ② · NT 侧车 round-trip 观测（outcome 取值封闭：ok/unavailable/
	// timeout/internal，与 pkg/errors 错误码族对齐）。
	NTCallCount    *prometheus.CounterVec
	NTCallDuration *prometheus.HistogramVec

	// QT-12 ⑤ · run 准入与缓存账（四个都是 Gauge：读的是「现在」，
	// 不是累计。刻意不带 _total 后缀 —— 那个后缀在 Prometheus 约定里
	// 专指单调递增的 Counter，挂在一个会往下走的量上会让 rate() 出鬼值）。
	RunsActive prometheus.Gauge
	RunsQueued prometheus.Gauge
	CacheBytes *prometheus.GaugeVec
	WasmBytes  prometheus.Gauge

	registry *prometheus.Registry
}

// NewMetrics creates a fresh Metrics handle with a private
// prometheus.Registry. Using a private registry (rather than the
// global promauto.DefaultRegisterer) means:
//
//   - tests can run in parallel without colliding on duplicate
//     metric registration
//   - production code can choose to expose /metrics with only the
//     four ADR-017 metrics, not the default Go runtime collectors
//     (call RegisterCollectors to opt in)
func NewMetrics() *Metrics {
	return &Metrics{
		BacktestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "backtest_duration_seconds",
			Help:    "Duration of a backtest execution, labeled by strategy and universe.",
			Buckets: prometheus.ExponentialBuckets(0.05, 2, 12), // 50ms .. ~200s
		}, []string{"strategy", "universe"}),

		HTTPRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_client_requests_total",
			Help: "Count of outbound HTTP calls the service has made, labeled by target service and HTTP status.",
		}, []string{"service", "status"}),

		LLMTokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "llm_tokens_total",
			Help: "Total LLM tokens consumed, labeled by provider and model.",
		}, []string{"provider", "model"}),

		CacheHitRatio: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "cache_hit_ratio",
			Help: "Rolling cache hit ratio in [0,1], labeled by cache kind (factor, ohlcv, sync_job, etc.).",
		}, []string{"kind"}),

		NTCallCount: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "nt_sidecar_calls_total",
			Help: "Count of NT sidecar round trips, labeled by outcome (ok/unavailable/timeout/internal). Failure rate = rate(outcome!=\"ok\") share.",
		}, []string{"outcome"}),

		NTCallDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "nt_sidecar_call_duration_seconds",
			Help:    "Duration of NT sidecar round trips, labeled by outcome.",
			Buckets: prometheus.ExponentialBuckets(0.05, 2, 12), // 50ms .. ~200s，与 backtest_duration 同桶口径
		}, []string{"outcome"}),

		RunsActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "backtest_runs_active",
			Help: "Backtest runs currently holding a slot in this process (QT-12 admission). Flatline at the limit = the gate is the bottleneck.",
		}),

		RunsQueued: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "backtest_runs_queued",
			Help: "Backtest runs waiting for a slot inside the bounded queue (QT-12 admission). Rejections beyond the queue are errors, not this gauge.",
		}),

		CacheBytes: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "backtest_cache_bytes",
			Help: "Estimated bytes held by one backtest run's private caches, labeled by kind (ohlcv/factor/intraday/fundamentals).",
		}, []string{"kind"}),

		WasmBytes: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "backtest_wasm_bytes",
			Help: "Estimated linear-memory bytes used by WASM strategy sessions. 0 until the WASM provider is wired (QT-26); the series exists so 'empty' and 'zero' stay distinguishable.",
		}),

		registry: prometheus.NewRegistry(),
	}
}

// Register attaches the four core metrics (plus the K16 ② NT sidecar
// pair and the QT-12 run-admission gauges) to the underlying registry.
// The constructor above does not auto-register so that tests can inspect
// / re-create metrics;
// production code MUST call Register before exposing /metrics.
func (m *Metrics) Register() {
	m.registry.MustRegister(
		m.BacktestDuration,
		m.HTTPRequests,
		m.LLMTokens,
		m.CacheHitRatio,
		m.NTCallCount,
		m.NTCallDuration,
		m.RunsActive,
		m.RunsQueued,
		m.CacheBytes,
		m.WasmBytes,
	)
}

// RegisterCollectors attaches the standard Go process / Go runtime
// collectors. Call this in addition to Register when you want
// memory/CPU/Goroutine visibility alongside the four core metrics.
func (m *Metrics) RegisterCollectors(collectors ...prometheus.Collector) {
	for _, c := range collectors {
		m.registry.MustRegister(c)
	}
}

// Registry returns the underlying registry. Useful for tests that
// need to scrape /metrics directly via testutil.GatherAndCount.
func (m *Metrics) Registry() *prometheus.Registry { return m.registry }

// ObserveBacktest records a single backtest duration. Empty label
// values are allowed (the metric is emitted with an empty label,
// matching the convention used by all four core metrics).
func (m *Metrics) ObserveBacktest(strategyName, universe string, d time.Duration) {
	if m == nil || m.BacktestDuration == nil {
		return
	}
	m.BacktestDuration.WithLabelValues(strategyName, universe).Observe(d.Seconds())
}

// ObserveHTTP records one outbound HTTP call.
func (m *Metrics) ObserveHTTP(service string, status int) {
	if m == nil || m.HTTPRequests == nil {
		return
	}
	m.HTTPRequests.WithLabelValues(service, strconv.Itoa(status)).Inc()
}

// AddLLMTokens increments the LLM token counter.
func (m *Metrics) AddLLMTokens(provider, model string, tokens int) {
	if m == nil || m.LLMTokens == nil || tokens <= 0 {
		return
	}
	m.LLMTokens.WithLabelValues(provider, model).Add(float64(tokens))
}

// SetCacheHitRatio sets the cache hit ratio for a named cache kind.
// The ratio is clamped to [0, 1] to avoid emitting invalid gauge
// values that would confuse alerting rules.
func (m *Metrics) SetCacheHitRatio(kind string, ratio float64) {
	if m == nil || m.CacheHitRatio == nil {
		return
	}
	if ratio < 0 {
		ratio = 0
	} else if ratio > 1 {
		ratio = 1
	}
	m.CacheHitRatio.WithLabelValues(kind).Set(ratio)
}

// ObserveNTCall records one NT sidecar round trip (K16 ②). It implicitly
// satisfies the consumer-defined port nautilus.RunObserver — the compile-time
// assertion lives at the assembly site (cmd/analysis/setup.go) so this package
// keeps no dependency on pkg/backtest/nautilus.
//
// outcome 是封闭词表（ok/unavailable/timeout/internal，见
// nautilus.RunObserver）；label 透传不再校验 —— 校验点在生产者侧
// （nautilus.outcomeFor 对未知错误码 fail-closed 折叠为 internal），
// 这里不重复一道双真相。
func (m *Metrics) ObserveNTCall(outcome string, d time.Duration) {
	if m == nil {
		return
	}
	if m.NTCallCount != nil {
		m.NTCallCount.WithLabelValues(outcome).Inc()
	}
	if m.NTCallDuration != nil {
		m.NTCallDuration.WithLabelValues(outcome).Observe(d.Seconds())
	}
}

// ─── QT-12 ⑤ · run 准入与缓存账 ──────────────────────────────────────
//
// 下面四个方法隐式实现 backtest.RunObserver（消费方定义的窄接口）。
// 编译期断言在装配侧 cmd/analysis/setup.go —— 本包不 import pkg/backtest，
// 依赖方向与 ObserveNTCall / nautilus.RunObserver 一致。

// SetRunsActive 报当前占用槽位的 run 数。
func (m *Metrics) SetRunsActive(v float64) {
	if m == nil || m.RunsActive == nil {
		return
	}
	m.RunsActive.Set(v)
}

// SetRunsQueued 报有界队列里正在等待的 run 数。
//
// 队列本身有界，所以这个数有上界；被拒的 run 不体现在这里（它们是错误，
// 走 HTTP 状态码与日志），本指标只回答「还要排多久」。
func (m *Metrics) SetRunsQueued(v float64) {
	if m == nil || m.RunsQueued == nil {
		return
	}
	m.RunsQueued.Set(v)
}

// SetCacheBytes 按 kind 报在跑 run 的缓存字节合计。
//
// kind 由调用方给（ohlcv / factor / intraday / fundamentals）。与
// ObserveNTCall 的 outcome 同理：词表在生产者侧（runAdmission.publishCacheBytes）
// 封闭，这里不重复校验，免得两处各有一份「合法 kind」清单。
func (m *Metrics) SetCacheBytes(kind string, v float64) {
	if m == nil || m.CacheBytes == nil {
		return
	}
	m.CacheBytes.WithLabelValues(kind).Set(v)
}

// SetWasmBytes 报 wasm 会话线性内存字节（QT-26 接线前恒 0）。
func (m *Metrics) SetWasmBytes(v float64) {
	if m == nil || m.WasmBytes == nil {
		return
	}
	m.WasmBytes.Set(v)
}
