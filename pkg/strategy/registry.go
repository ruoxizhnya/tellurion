// Package strategy provides strategy management and plugin architecture.
package strategy

import (
	"context"
	"errors"
	"fmt"
	"plugin"
	"sync"

	"github.com/rs/zerolog"
)

// StrategyFactory is a function that creates a new Strategy instance.
// Used for hot-reloadable strategies.
type StrategyFactory func() Strategy

// ErrSharedStrategy 表示「这个策略是以共享实例注册的」，拿不出新实例。
//
// 它是 QT-11 的 fail-closed 面：执行路径只能用 NewInstance 要一份自己的实例，
// 而 `Register(Strategy)` 注册的那一个在多个 run 之间是同一份内存 ——
// 流式策略的游标 / EMA 状态会跨 run 存活（`SignalStrategy.cursor` 就是这个）。
// 报错而不是"那就用共享的那个"，是因为串状态的回测曲线看起来完全正常。
var ErrSharedStrategy = errors.New("strategy is registered as a shared instance")

// registryEntry 钉住「这个策略怎么被实例化」。
//
// factory 非空 = 每次执行各拿一份新实例（QT-11 要求的形态）；
// factory 为空 = 只有注册时那一份（共享实例），只能查元数据。
// meta 恒非空，是 Name/Description/Parameters 与类型探测的读取源。
type registryEntry struct {
	factory StrategyFactory
	meta    Strategy
}

// Registry manages strategy registration and retrieval.
type Registry struct {
	strategies map[string]*registryEntry
	mu         sync.RWMutex
}

// NewRegistry creates a new strategy registry.
func NewRegistry() *Registry {
	return &Registry{
		strategies: make(map[string]*registryEntry),
	}
}

// Register registers a strategy **instance** — the entry is marked shared.
// Returns an error if a strategy with the same name is already registered.
//
// 注册实例只适合「只要元数据」的策略。执行路径取不到它（`NewInstance` 返回
// ErrSharedStrategy）；要能被回测跑，请用 `RegisterFactory`。
func (r *Registry) Register(s Strategy) error {
	if s == nil {
		return fmt.Errorf("strategy cannot be nil")
	}

	name := s.Name()
	if name == "" {
		return fmt.Errorf("strategy name cannot be empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.strategies[name]; exists {
		return fmt.Errorf("strategy already registered: %s", name)
	}

	r.strategies[name] = &registryEntry{meta: s}
	return nil
}

// RegisterFactory registers a constructor for a strategy, so every run gets
// its own instance via NewInstance. The strategy name is taken from the
// prototype the factory produces at registration time — it cannot drift away
// from `Strategy.Name()` because there is no second name to disagree with.
//
// 构造一次只为拿名字与元数据；代价是注册期就多造一个实例，因此工厂必须是
// 纯构造（不做 I/O）。注册期工厂报错没有意义：那时没有调用方可救，
// 所以这里直接返回 error，让 init/装配路径自己决定要不要 panic。
func (r *Registry) RegisterFactory(f StrategyFactory) error {
	if f == nil {
		return fmt.Errorf("strategy factory cannot be nil")
	}

	proto := f()
	if proto == nil {
		return fmt.Errorf("strategy factory returned a nil strategy")
	}

	name := proto.Name()
	if name == "" {
		return fmt.Errorf("strategy name cannot be empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.strategies[name]; exists {
		return fmt.Errorf("strategy already registered: %s", name)
	}

	r.strategies[name] = &registryEntry{factory: f, meta: proto}
	return nil
}

// Get retrieves the registered strategy instance for **metadata only**
// (Name / Description / Parameters / interface capability probing).
// Returns nil if the strategy is not found.
//
// 执行路径不要用它的返回值：工厂注册的条目返回的是注册期那个原型实例，
// 拿它跑信号等于把 per-run 隔离让给调用方的自觉。执行一律走 NewInstance
// —— 护栏在 internal/repoguard/registry_instance_test.go（pkg/backtest 内
// Registry.Get 命中即红）。
func (r *Registry) Get(name string) (Strategy, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, exists := r.strategies[name]
	if !exists {
		return nil, fmt.Errorf("strategy not found: %s", name)
	}

	return entry.meta, nil
}

// NewInstance returns a fresh strategy instance for one run. It is the only
// way the execution path may obtain a strategy.
//
// 三种失败各有明确语义：未注册 → "strategy not found"（与 Get 同口径，
// LocalSignalProvider 的 OBS-11 错误消息依赖它）；以实例注册 →
// ErrSharedStrategy；工厂返回 nil → 工厂自身的缺陷，报出来而不是把 nil
// 递给调用方。
func (r *Registry) NewInstance(name string) (Strategy, error) {
	r.mu.RLock()
	entry, exists := r.strategies[name]
	r.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("strategy not found: %s", name)
	}
	if entry.factory == nil {
		return nil, fmt.Errorf("%w: %s (registered with Register; a per-run instance needs RegisterFactory)",
			ErrSharedStrategy, name)
	}

	s := entry.factory()
	if s == nil {
		return nil, fmt.Errorf("strategy factory for %s returned a nil strategy", name)
	}
	return s, nil
}

// Describe returns the metadata of a registered strategy without handing out
// an instance. It exists so callers that only need Name/Description/Parameters
// never reach for Get's return value by accident.
func (r *Registry) Describe(name string) (StrategyInfo, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, exists := r.strategies[name]
	if !exists {
		return StrategyInfo{}, fmt.Errorf("strategy not found: %s", name)
	}

	return StrategyInfo{
		Name:        entry.meta.Name(),
		Description: entry.meta.Description(),
		Parameters:  entry.meta.Parameters(),
	}, nil
}

// IsShared reports whether a registered strategy can only ever be the one
// instance handed to Register (i.e. NewInstance would fail for it).
//
// 存在的理由：`ConfigureStrategy` 这类「改注册表里那份实例」的入口，对工厂
// 注册的条目是**静默无效**的——run 用的实例是新造的，改原型改不到它。
// 静默无效比报错危险，所以调用方必须先问这一句。
func (r *Registry) IsShared(name string) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, exists := r.strategies[name]
	if !exists {
		return false, fmt.Errorf("strategy not found: %s", name)
	}
	return entry.factory == nil, nil
}

// List returns a list of all registered strategy names.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.strategies))
	for name := range r.strategies {
		names = append(names, name)
	}

	return names
}

// ListWithInfo returns strategy names with their descriptions and parameters.
func (r *Registry) ListWithInfo() []StrategyInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	infos := make([]StrategyInfo, 0, len(r.strategies))
	for _, entry := range r.strategies {
		infos = append(infos, StrategyInfo{
			Name:        entry.meta.Name(),
			Description: entry.meta.Description(),
			Parameters:  entry.meta.Parameters(),
		})
	}

	return infos
}

// StrategyInfo contains information about a strategy.
type StrategyInfo struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  []Parameter `json:"parameters"`
}

// DefaultRegistry is the global strategy registry instance.
var DefaultRegistry = NewRegistry()

// Logger for the default registry
var defaultLogger zerolog.Logger

// Init initializes the default registry with a logger.
func Init(logger zerolog.Logger) {
	defaultLogger = logger.With().Str("component", "strategy_registry").Logger()
	DefaultRegistry = NewRegistry()
}

// GlobalRegister registers a strategy instance with the default registry
// (shared — see Registry.Register).
func GlobalRegister(s Strategy) error {
	return DefaultRegistry.Register(s)
}

// GlobalRegisterFactory registers a strategy constructor with the default
// registry, so the execution path can get a per-run instance.
func GlobalRegisterFactory(f StrategyFactory) error {
	return DefaultRegistry.RegisterFactory(f)
}

// GlobalGet retrieves the registered strategy instance from the default
// registry for metadata only — see Registry.Get.
func GlobalGet(name string) (Strategy, error) {
	return DefaultRegistry.Get(name)
}

// GlobalNewInstance returns a fresh strategy instance from the default
// registry — the only global lookup the execution path may use.
func GlobalNewInstance(name string) (Strategy, error) {
	return DefaultRegistry.NewInstance(name)
}

// GlobalDescribe returns a strategy's metadata from the default registry
// without handing out an instance.
func GlobalDescribe(name string) (StrategyInfo, error) {
	return DefaultRegistry.Describe(name)
}

// GlobalList returns all registered strategy names from the default registry.
func GlobalList() []string {
	return DefaultRegistry.List()
}

// GlobalListWithInfo returns all registered strategies with info from the default registry.
func GlobalListWithInfo() []StrategyInfo {
	return DefaultRegistry.ListWithInfo()
}

// LoadPlugins loads strategies from dynamically linked plugin files.
// This enables adding new strategies without rebuilding the main binary.
//
// 插件导出的是一个实例（`Strategy` 符号），所以它按共享方式注册：
// NewInstance 取不到它，回测执行路径会明确失败而不是悄悄共用一份跨 run 的
// 状态。要让插件也能 per-run，插件得导出构造函数——那属 abi v2 / QT-26 的
// 接线范围，不在本单里假装支持。
func LoadPlugins(pluginPaths []string) error {
	for _, path := range pluginPaths {
		plug, err := plugin.Open(path)
		if err != nil {
			return fmt.Errorf("failed to open plugin %s: %w", path, err)
		}

		symStrategy, err := plug.Lookup("Strategy")
		if err != nil {
			return fmt.Errorf("plugin %s does not export Strategy symbol: %w", path, err)
		}

		strategy, ok := symStrategy.(Strategy)
		if !ok {
			return fmt.Errorf("plugin %s Strategy symbol does not implement Strategy interface", path)
		}

		if err := GlobalRegister(strategy); err != nil {
			return fmt.Errorf("failed to register plugin strategy %s: %w", path, err)
		}
	}

	return nil
}

// AutoRegister registers all built-in strategies.
// This is called at startup to register hardcoded strategies.
func AutoRegister() error {
	// Strategies register themselves via init() in their respective files
	return nil
}

// RegisterByReflection registers all Strategy implementations found in the given slice.
func RegisterByReflection(strategies []Strategy) error {
	for _, s := range strategies {
		if err := GlobalRegister(s); err != nil {
			return err
		}
	}
	return nil
}

// NewPlugin creates a plugin symbol for dynamic loading.
func NewPlugin(s Strategy) Strategy {
	return s
}

// ─── Backward Compatibility with domain.Strategy ──────────────────────────────────

// Deprecated: domain.Strategy is removed in ODR-013 P1-25. The old registry
// is no longer used; retained as a thin wrapper only so the file compiles
// during the transition. It will be deleted in a follow-up commit.
//
// All callers should migrate to the canonical strategy.Strategy interface
// (which is what `Registry` below uses).
type oldRegistry struct {
	factories map[string]StrategyFactory
	instances map[string]Strategy
	mu        sync.RWMutex
	logger    zerolog.Logger
}

func newOldRegistry(logger zerolog.Logger) *oldRegistry {
	return &oldRegistry{
		factories: make(map[string]StrategyFactory),
		instances: make(map[string]Strategy),
		logger:    logger.With().Str("component", "strategy_registry_legacy").Logger(),
	}
}

// DefaultOldRegistry is the global registry for the deprecated API.
// It is intentionally uninitialised — old callers will get a clear error.
var DefaultOldRegistry *oldRegistry

// OldInit is a no-op retained for library consumers of the deprecated API.
// Its original reason for existing — satisfying the cmd/strategy main entry
// point — is gone with that service (REV-16, ADR-012 closed). The old strategy
// registration API (Register/GetStrategy) returns errors instead of mutating
// state, so calling this only logs a deprecation warning.
func OldInit(logger zerolog.Logger) {
	logger.Warn().Msg("strategy.OldInit is deprecated (ODR-013 P1-25) and is a no-op")
}

// Register is the deprecated old-API strategy registration. It always
// returns an error directing the caller to use the new Registry.
func Register(name string, factory StrategyFactory) error {
	return fmt.Errorf("strategy.Register(%q) is removed in ODR-013 P1-25; "+
		"use strategy.GlobalRegister with pkg/strategy.Strategy instead", name)
}

// GetStrategy is the deprecated lookup. It always returns an error.
func GetStrategy(name string) (Strategy, error) {
	return nil, fmt.Errorf("strategy.GetStrategy(%q) is removed in ODR-013 P1-25; "+
		"use strategy.GlobalGet with pkg/strategy.Strategy instead", name)
}

// ListStrategies is the deprecated list. It returns an empty slice.
func ListStrategies() []string {
	return nil
}

// ReloadStrategy is the deprecated reload. It always returns an error.
func ReloadStrategy(name string) error {
	return fmt.Errorf("strategy.ReloadStrategy(%q) is removed in ODR-013 P1-25; "+
		"use strategy.GlobalRegister with the new pkg/strategy.Strategy instead", name)
}

// ConfigureStrategy applies parameter overrides to a registered strategy.
// The strategy must implement the Configurable sub-interface (P1-24,
// ADR-020 §4). Strategies that omit Configurable get a clear error.
//
// New code should call `strategy.AsConfigurable(s).Configure(params)`
// directly rather than going through this registry helper — the
// registry path is retained for backward compatibility with callers
// that look up strategies by name.
//
// 工厂注册（RegisterFactory）的条目在这里**明确拒绝**（QT-11）：本函数改的是
// 注册表里那一份，而 run 用的是 NewInstance 造的新实例，改了等于没改。
// run 级参数投递是 QT-20 的口径，不在这里假装生效。
func ConfigureStrategy(name string, params map[string]any) error {
	shared, err := DefaultRegistry.IsShared(name)
	if err != nil {
		return fmt.Errorf("strategy not found: %s", name)
	}
	if !shared {
		return fmt.Errorf("strategy %s is registered with a factory: ConfigureStrategy would only mutate the "+
			"registration prototype, not the per-run instances NewInstance produces (run-level params are QT-20)", name)
	}
	s, err := DefaultRegistry.Get(name)
	if err != nil {
		return fmt.Errorf("strategy not found: %s", name)
	}
	c := AsConfigurable(s)
	if c == nil {
		return fmt.Errorf("strategy %s does not implement strategy.Configurable", name)
	}
	if err := c.Configure(params); err != nil {
		return fmt.Errorf("failed to configure strategy %s: %w", name, err)
	}
	if defaultLogger.GetLevel() != zerolog.Disabled {
		defaultLogger.Info().Str("strategy", name).Interface("params", params).Msg("Strategy reconfigured")
	}
	return nil
}

// ReloadFromDB reloads strategy parameters from the database.
// It looks up the strategy in the DB, applies the saved params, and returns the strategy name.
func ReloadFromDB(ctx context.Context, db *StrategyDB, name string) error {
	params, err := db.GetStrategyParams(ctx, name)
	if err != nil {
		return fmt.Errorf("failed to get params for %s: %w", name, err)
	}
	return ConfigureStrategy(name, params)
}

// ReloadAllFromDB reloads all registered strategies from the database.
func ReloadAllFromDB(ctx context.Context, db *StrategyDB) map[string]string {
	names := DefaultRegistry.List()
	results := make(map[string]string)
	for _, name := range names {
		if err := ReloadFromDB(ctx, db, name); err != nil {
			results[name] = "error: " + err.Error()
		} else {
			results[name] = "ok"
		}
	}
	return results
}

// ReloadAll is the deprecated bulk reload. It always returns an error.
func ReloadAll() error {
	return fmt.Errorf("strategy.ReloadAll is removed in ODR-013 P1-25; " +
		"use the new strategy.Registry directly")
}

// ReloadAllStrategies is an alias for ReloadAll (for backward compatibility).
func ReloadAllStrategies() error {
	return ReloadAll()
}

// GetStrategyInfo is the deprecated info lookup. It always returns an error.
func GetStrategyInfo(name string) (*StrategyInfo, error) {
	return nil, fmt.Errorf("strategy.GetStrategyInfo(%q) is removed in ODR-013 P1-25; "+
		"use the new strategy.Registry.ListWithInfo() instead", name)
}

func init() {
	// Auto-register is called at startup
}
