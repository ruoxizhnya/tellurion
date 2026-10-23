package cache

// P1-16 (Sprint 6, ODR-013 CQ-001, ADR-020):
// FactorCacheAccessor — L1 因子 z-score 缓存子组件，从 Engine 抽离。
//
// 该文件从 pkg/backtest/engine.go 中抽离出以下职责：
//
//   - 持有 factorCache (factorType → date → symbol → z-score)
//   - 持有 quintileCache (factorType → date → symbol → quintile 1-5) — P1-C
//   - 提供 LoadFactorCache (直接注入)
//   - 提供 GetFactorZScore (lock-free 读取)
//   - 提供 GetQuintile (读取预计算的 quintile) — P1-C
//   - 提供 ComputeQuintile (z-score → 1-5 quintile 映射) — P1-C
//   - 提供 Warm (从 storage.PostgresStore 批量预热，含 quintile 预计算)
//
// Engine 通过 Engine.FactorCache() 访问；旧字段 e.factorCache 与
// 旧方法 (LoadFactorCache / GetFactorZScore / warmFactorCache) 保留为
// backward-compat shim (6 个月)。
//
// 设计动机：
//   - 因子缓存与 OHLCV 缓存是**两套独立关注点**：
//     * OHLCV 缓存由 CacheManager 持有（L1 volatile）
//     * 因子缓存由 FactorCacheAccessor 持有（可由 DB 持久层 warm）
//   - 拆分后两个组件可独立单测、独立演进
//   - future: 因子缓存可引入 lazy load / TTL / 持久化而不影响 OHLCV 路径

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-contracts/domain"
	"tellurion/pkg/storage"
)

// FactorStore is the minimal interface FactorCacheAccessor needs from a
// persistence layer. *storage.PostgresStore satisfies this.
type FactorStore interface {
	GetFactorCacheRange(ctx context.Context, factor domain.FactorType, start, end time.Time) ([]*domain.FactorCacheEntry, error)
}

// FactorCacheAccessor manages the L1 factor z-score cache.
type FactorCacheAccessor struct {
	mu    sync.RWMutex
	cache map[domain.FactorType]map[time.Time]map[string]float64
	// quintileCache mirrors the structure of cache but stores the
	// pre-computed quintile (1-5) derived from each z-score. Populated
	// alongside cache in Load / Warm so that GetQuintile is a pure lookup.
	quintileCache map[domain.FactorType]map[time.Time]map[string]int

	logger zerolog.Logger
}

// quintileThresholds holds the standard normal distribution quantiles at
// the 20th / 40th / 60th / 80th percentiles. Used by computeQuintile to
// map a z-score into one of 5 equipopulated buckets:
//
//	q1 (bottom 20%): z < -0.843
//	q2:              -0.843 <= z < -0.253
//	q3 (middle 20%): -0.253 <= z < 0.253
//	q4:              0.253 <= z < 0.843
//	q5 (top 20%):    z >= 0.843
//
// Values are the inverse CDF of N(0,1) at p=0.2/0.4/0.6/0.8 (rounded to
// 3 decimals, matching the spec in P1-C).
var quintileThresholds = [4]float64{-0.843, -0.253, 0.253, 0.843}

// ErrQuintileNotFound is returned by GetQuintile when the requested
// (factor, date, symbol) tuple is absent from the quintile cache — either
// because the cache is uninitialized or because no z-score was loaded for
// that key.
var ErrQuintileNotFound = errors.New("quintile not found in factor cache")

// computeQuintile is the pure, allocation-free implementation backing
// ComputeQuintile. Split out so buildQuintileCache / Warm can derive
// quintiles without an accessor receiver.
func computeQuintile(zScore float64) int {
	for i, t := range quintileThresholds {
		if zScore < t {
			return i + 1
		}
	}
	return 5
}

// ComputeQuintile maps a z-score to a quintile in [1, 5] using standard
// normal distribution quantiles (20% / 40% / 60% / 80%). The mapping is
// pure and deterministic — it does not consult the cache.
func (f *FactorCacheAccessor) ComputeQuintile(zScore float64) int {
	return computeQuintile(zScore)
}

// GetQuintile returns the pre-computed quintile (1-5) for the given
// factor/date/symbol. The factor string is interpreted as a domain.FactorType
// (e.g. "momentum", "value"); an unknown factor or a cache miss yields
// ErrQuintileNotFound. Quintiles are pre-computed during Load / Warm, so
// this is a constant-time lookup with no per-call computation.
func (f *FactorCacheAccessor) GetQuintile(symbol string, date time.Time, factor string) (int, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.quintileCache == nil {
		return 0, ErrQuintileNotFound
	}
	dateMap, ok := f.quintileCache[domain.FactorType(factor)]
	if !ok {
		return 0, ErrQuintileNotFound
	}
	symbolMap, ok := dateMap[date]
	if !ok {
		return 0, ErrQuintileNotFound
	}
	q, ok := symbolMap[symbol]
	if !ok {
		return 0, ErrQuintileNotFound
	}
	return q, nil
}

// NewFactorCacheAccessor 构造一个空 factor cache accessor。
func NewFactorCacheAccessor(logger zerolog.Logger) *FactorCacheAccessor {
	return &FactorCacheAccessor{
		logger: logger.With().Str("component", "factor_cache").Logger(),
	}
}

// Load 直接注入数据（典型用法：storage.GetFactorCacheRange 后的结果）。
// 传 nil 清理缓存。Quintile 缓存从注入的 z-score 派生重建，保持与
// z-score 缓存同步（P1-C）。
func (f *FactorCacheAccessor) Load(data map[domain.FactorType]map[time.Time]map[string]float64) {
	f.mu.Lock()
	f.cache = data
	f.quintileCache = buildQuintileCache(data)
	f.mu.Unlock()
}

// Copy 返回一份**独立**的缓存副本（三层 map 都新建，值本体是 float64
// 无需再拷），分位表随之重建。
//
// 存在的理由（QT-12）：run 级缓存要从「装配期注入的那份」起步，但绝不能
// 与引擎级种子共享 map —— Warm 是整体替换语义，替换前的 `combined` 是在
// 现有 map 上叠出来的，共享等于让 A run 的 warm 写进 B run 正在读的那份。
// 空缓存返回一个干净的访问器（不是 nil），调用方不必判空。
func (f *FactorCacheAccessor) Copy() *FactorCacheAccessor {
	f.mu.RLock()
	seeded := make(map[domain.FactorType]map[time.Time]map[string]float64, len(f.cache))
	for factor, byDate := range f.cache {
		cloneDate := make(map[time.Time]map[string]float64, len(byDate))
		for date, bySymbol := range byDate {
			cloneSymbol := make(map[string]float64, len(bySymbol))
			for symbol, z := range bySymbol {
				cloneSymbol[symbol] = z
			}
			cloneDate[date] = cloneSymbol
		}
		seeded[factor] = cloneDate
	}
	logger := f.logger
	f.mu.RUnlock()

	g := NewFactorCacheAccessor(logger)
	g.Load(seeded)
	return g
}

// buildQuintileCache derives a quintile cache from a z-score cache by
// applying computeQuintile to every entry. Returns nil for nil/empty input
// so that GetQuintile cleanly reports ErrQuintileNotFound.
func buildQuintileCache(data map[domain.FactorType]map[time.Time]map[string]float64) map[domain.FactorType]map[time.Time]map[string]int {
	if len(data) == 0 {
		return nil
	}
	out := make(map[domain.FactorType]map[time.Time]map[string]int, len(data))
	for factor, dateMap := range data {
		if len(dateMap) == 0 {
			continue
		}
		outDateMap := make(map[time.Time]map[string]int, len(dateMap))
		for date, symbolMap := range dateMap {
			if len(symbolMap) == 0 {
				continue
			}
			outSymbolMap := make(map[string]int, len(symbolMap))
			for symbol, z := range symbolMap {
				outSymbolMap[symbol] = computeQuintile(z)
			}
			outDateMap[date] = outSymbolMap
		}
		out[factor] = outDateMap
	}
	return out
}

// Get 返回 (z-score, exists)。无命中返回 (0, false)。
// 读路径持 RLock（与 OHLCV cache 的 lock-free 不同，因为命中频率低
// 且 map 体积通常远小于 OHLCV 缓存）。
func (f *FactorCacheAccessor) Get(factor domain.FactorType, date time.Time, symbol string) (float64, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.cache == nil {
		return 0, false
	}
	dateMap, ok := f.cache[factor]
	if !ok {
		return 0, false
	}
	symbolMap, ok := dateMap[date]
	if !ok {
		return 0, false
	}
	z, ok := symbolMap[symbol]
	return z, ok
}

// Warm 从 store 批量预热 3 类常用因子（momentum/value/quality）。
// store 为 nil 时 no-op；具体因子加载失败时打 Warn 不中断。
//
// Nil-check 注意事项：FactorStore 是 interface。Go 的 typed-nil 陷阱
// 意味着传入 (*storage.PostgresStore)(nil) 会得到一个**非 nil 的
// interface 值**——直接调用方法会 panic。调用方（特别是
// Engine.warmFactorCache）需要在传入前自行判空 underlying pointer。
// 这里只做 interface==nil 检查。
func (f *FactorCacheAccessor) Warm(ctx context.Context, start, end time.Time, store FactorStore) error {
	if store == nil {
		return nil
	}

	factors := []domain.FactorType{domain.FactorMomentum, domain.FactorValue, domain.FactorQuality}
	combined := make(map[domain.FactorType]map[time.Time]map[string]float64)

	for _, factor := range factors {
		entries, err := store.GetFactorCacheRange(ctx, factor, start, end)
		if err != nil {
			f.logger.Warn().Str("factor", string(factor)).Err(err).Msg("Failed to load factor cache from DB")
			continue
		}
		if len(entries) == 0 {
			f.logger.Info().Str("factor", string(factor)).Msg("No factor cache entries in DB — run sync/factors/all first")
			continue
		}
		for _, entry := range entries {
			if entry == nil {
				continue
			}
			if combined[entry.FactorName] == nil {
				combined[entry.FactorName] = make(map[time.Time]map[string]float64)
			}
			if combined[entry.FactorName][entry.TradeDate] == nil {
				combined[entry.FactorName][entry.TradeDate] = make(map[string]float64)
			}
			combined[entry.FactorName][entry.TradeDate][entry.Symbol] = entry.ZScore
		}
		f.logger.Info().
			Str("factor", string(factor)).
			Int("entries", len(entries)).
			Msg("Factor cache loaded from DB")
	}

	if len(combined) > 0 {
		f.mu.Lock()
		f.cache = combined
		// P1-C: 预计算 quintile 并缓存，使 GetQuintile 成为纯查表操作。
		f.quintileCache = buildQuintileCache(combined)
		f.mu.Unlock()
	}

	return nil
}

// Len 返回当前缓存中的 factor 数量（仅供测试 / 监控）。
func (f *FactorCacheAccessor) Len() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return len(f.cache)
}

// Entries 返回 (factor, date, symbol) 三元组的条数。
//
// 与 Len 的区别：Len 是「几个因子」，Entries 是「多少个数」—— 内存账与
// 准入估算要的是后者（一个因子铺 5000 只票 × 250 天 = 一百二十五万条）。
func (f *FactorCacheAccessor) Entries() int64 {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.entriesLocked()
}

// Bytes 返回 L1 因子缓存**约**占的堆字节（QT-12 ③，口径同 CacheManager.Bytes：
// 准入与指标用的估算，不是 profiler）。
//
// 三层 map 都计：z-score（float64）与分位（int）各一份，外加每层的
// map 条目摊销与 symbol 字符串本体。常数 mapEntryBytes 与 OHLCV 侧共用
// 同一份定义（cache.go），两条腿的口径不许各说各话。
func (f *FactorCacheAccessor) Bytes() int64 {
	f.mu.RLock()
	defer f.mu.RUnlock()

	var total int64
	for factor, byDate := range f.cache {
		total += mapEntryBytes + int64(len(factor))
		total += int64(len(byDate)) * mapEntryBytes
		for _, bySymbol := range byDate {
			for symbol := range bySymbol {
				total += float64Bytes + mapEntryBytes + int64(len(symbol))
			}
		}
	}
	// 分位表是 z-score 表的镜像（Load/Warm 同步填充），值换成 int。
	for _, byDate := range f.quintileCache {
		for _, bySymbol := range byDate {
			total += int64(len(bySymbol)) * (intBytes + mapEntryBytes)
		}
	}
	return total
}

// entriesLocked 是 Entries 的无锁内核，供 Bytes 之外仍需持锁的路径复用。
// 调用方须持有 mu（读锁即可）。
func (f *FactorCacheAccessor) entriesLocked() int64 {
	var n int64
	for _, byDate := range f.cache {
		for _, bySymbol := range byDate {
			n += int64(len(bySymbol))
		}
	}
	return n
}

// Compile-time check that *storage.PostgresStore satisfies FactorStore.
var _ FactorStore = (*storage.PostgresStore)(nil)
