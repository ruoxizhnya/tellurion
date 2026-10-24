package backtest

// P1-19 (Sprint 6, ODR-013 CQ-005, ADR-020):
// EngineOption — 函数式依赖注入模式（Go 惯用法）。
//
// 该文件实现 ADR-020 §3 "EngineOption 函数式注入"：
//
//   type EngineOption func(*Engine)
//
//   func WithRiskManager(rm *risk.RiskManager) EngineOption { ... }
//   func NewEngineWithOptions(cfg, provider, opts...) (*Engine, error)
//
// 收益：
//   - 10+ Setter → 1 variadic options pattern
//   - 必需参数（config, provider）vs 可选参数（riskManager, liveTrader）
//     在签名层面分离
//   - 测试构造更简洁：
//       NewEngineWithOptions(cfg, prov, WithRiskManager(mockRM), WithLiveTrader(fakeT))
//   - 未来扩展无需修改 Engine struct（直接新加一个 With* 选项）
//
// Backward Compat (per ADR-020 §2)：
//   - NewEngine(v, provider, logger) 旧签名保留 6 个月
//   - SetDataAdapter / SetStore / SetRiskManager / SetLiveTrader / SetExecutionService
//     5 个 engine setter 全部保留为 shim，内部委托到对应 bridge / field
//   - pkg/strategy.SetFactorCache 同步保留
//
// 与桥接组件的关系：
//   - WithLiveTrader / WithExecutionService 直接调用 bridge.Set
//   - WithRiskManager 写入 e.riskManager（in-process 风控不走 bridge）
//   - WithDataAdapter 写入 e.dataAdapter（multi-source 适配）
//   - WithStore 写入 e.store（factor DB 预热）

import (
	"github.com/rs/zerolog"
	"tellurion/pkg/live"
	"tellurion/pkg/marketdata"
	"tellurion/pkg/risk"
	"tellurion/pkg/sandbox/wasm"
	"tellurion/pkg/storage"
	"tellurion/pkg/strategy"
)

// EngineOption 函数式注入：每个选项返回的闭包会修改 *Engine。
//
// 设计要点：
//   - 全部"幂等"：重复应用同一选项以最后一次为准
//   - "失败" 仅来自 NewEngineWithOptions 内部（config 解析、provider 校验）
//   - 不在选项内部做重活（不发请求、不开连接）——只设引用
type EngineOption func(*Engine)

// WithRiskManager 注入 in-process 风控管理器。传 nil 退回 HTTP 风控服务。
func WithRiskManager(rm *risk.RiskManager) EngineOption {
	return func(e *Engine) {
		e.mu.Lock()
		e.riskManager = rm
		e.mu.Unlock()
	}
}

// WithDataAdapter 注入多数据源适配器。传 nil 退回原始 provider。
func WithDataAdapter(adapter *marketdata.DataAdapter) EngineOption {
	return func(e *Engine) {
		e.mu.Lock()
		e.dataAdapter = adapter
		e.mu.Unlock()
		if adapter != nil {
			e.logger.Info().Str("source", adapter.Primary()).Msg("DataAdapter attached via WithDataAdapter")
		}
	}
}

// WithStore 注入 Postgres 存储（用于 factor cache 预热、index constituents）。
func WithStore(store *storage.PostgresStore) EngineOption {
	return func(e *Engine) {
		e.mu.Lock()
		e.store = store
		e.mu.Unlock()
		if store != nil {
			e.logger.Info().Msg("PostgresStore attached via WithStore — factor cache warming enabled")
		}
	}
}

// WithAdjustmentAnchorStore 注入 qfq 复权基准快照的持久化端口（REV-18）。
// 传 nil 恢复「跟随 e.store 自动适配」的默认；两者都没有时快照完全直通。
func WithAdjustmentAnchorStore(st AdjustmentAnchorStore) EngineOption {
	return func(e *Engine) {
		e.mu.Lock()
		e.adjStore = st
		e.mu.Unlock()
	}
}

// WithRunProvenanceWriter 注入 run 留痕（QT-63）的写入端口。
// 传 nil 恢复「跟随 e.store」的默认；两者都没有时引擎不写 provenance。
func WithRunProvenanceWriter(w RunProvenanceWriter) EngineOption {
	return func(e *Engine) {
		e.mu.Lock()
		e.provWriter = w
		e.mu.Unlock()
	}
}

// WithoutRunProvenance 关闭 provenance 写入。
//
// 只给「挂了一个只读库 / 纯内存演示」的装配用：默认语义是「有 store 就写」，
// 而那些装配连接池没有写权限、也没有 run_provenance 表。生产路径不要用它 ——
// 没有留痕的 run 查不到自己的输入。
func WithoutRunProvenance() EngineOption {
	return func(e *Engine) {
		e.mu.Lock()
		e.provenanceOff = true
		e.mu.Unlock()
	}
}

// WithRiskRulesProvider 注入 risk 规则快照 hash 的取数口（QT-66）。
// 传 nil 恢复「未接入 risk 服务」语义（provenance 的 risk_rules_hash 为空 +
// not_configured）；非 nil 时每次 run 提交取一次 hash，取不到即 run 失败。
func WithRiskRulesProvider(p RiskRulesProvider) EngineOption {
	return func(e *Engine) {
		e.SetRiskRulesProvider(p)
	}
}

// WithLiveTrader 注入 live / paper trading trader。等价于 SetLiveTrader。
func WithLiveTrader(trader live.LiveTrader) EngineOption {
	return func(e *Engine) {
		e.liveBridge.Set(trader)
	}
}

// WithExecutionService 注入 ExecutionService。等价于 SetExecutionService。
func WithExecutionService(svc ExecutionService) EngineOption {
	return func(e *Engine) {
		e.executionBridge.Set(svc)
	}
}

// WithParallelWorkers 设置并发拉取 worker 数。<= 0 表示串行（1 worker）。
// 必须在 RunBacktest 之前调用。
func WithParallelWorkers(n int) EngineOption {
	return func(e *Engine) {
		e.parallelWorkers = n
	}
}

// WithLogger 覆盖 engine 默认 logger。
// 主要用于测试（zerolog.New(nil) 静默输出）。
func WithLogger(logger zerolog.Logger) EngineOption {
	return func(e *Engine) {
		e.mu.Lock()
		e.logger = logger
		e.mu.Unlock()
	}
}

// WithSignalProvider 注入可插拔信号供给（K8 · SignalSpec）。传 nil 退回
// 默认 strategy.LocalSignalProvider（注册表 + 批式/流式双模式，行为不变）。
// 远程/自定义实现（HTTP、Redis 等）在阶段 2 经本选项接入。
func WithSignalProvider(p strategy.SignalProvider) EngineOption {
	return func(e *Engine) {
		e.mu.Lock()
		e.signalProvider = p
		e.mu.Unlock()
	}
}

// WithRouter 注入规范层透明路由（D1 产物）—— D6 日内分钟快照预加载的
// 唯一取数通道（回测启动前经 Router.Bars 取 1m；运行时禁止策略直查
// provider，红线）。传 nil = 不预热，IntradayAware 策略用到日内特征
// 时明确失败。等价于 SetRouter。
func WithRouter(r *marketdata.Router) EngineOption {
	return func(e *Engine) {
		e.SetRouter(r)
	}
}

// WithStateStore 注入自定义 StateStore（P1-18, ADR-020）。
//
// 默认 Engine 使用 LRUStateStore（capacity=1000）。以下场景可注入：
//   - 测试：WithStateStore(NewNoopStateStore()) 关闭 LRU 行为
//   - 生产：WithStateStore(newPersistentStateStore(db)) 落 PG
//   - 批处理：WithStateStore(NewLRUStateStore(10000)) 调高容量
//
// 注意：Option 在 NewEngineWithOptions 构造时立即覆盖默认 store;
// 后续 RunBacktest 创建的状态会写入新 store,旧 store 不会自动迁移。
func WithStateStore(store StateStore) EngineOption {
	return func(e *Engine) {
		if store == nil {
			e.logger.Warn().Msg("WithStateStore(nil) — keeping default LRUStateStore")
			return
		}
		e.mu.Lock()
		e.stateStore = store
		e.mu.Unlock()
		e.logger.Info().Msg("Custom StateStore attached via WithStateStore")
	}
}

// WithStateStoreCapacity 便捷选项：用给定容量构建 LRUStateStore。
// 等价于 WithStateStore(NewLRUStateStore(capacity))。
// capacity <= 0 时回退为 NoopStateStore（无界）。
func WithStateStoreCapacity(capacity int) EngineOption {
	return func(e *Engine) {
		if capacity <= 0 {
			e.mu.Lock()
			e.stateStore = NewNoopStateStore()
			e.mu.Unlock()
			return
		}
		e.mu.Lock()
		e.stateStore = NewLRUStateStore(capacity)
		e.mu.Unlock()
	}
}

// WithWASMArtifacts 注入 WASM 策略制品（QT-26）。制品按策略名匹配：
// 命中该名字的 run 走 wasmSignalProvider（从 artifact 加载 → 逐 bar →
// 信号），其余策略仍走本地注册表。传空表 = 关闭（默认）。
//
// 为什么是「装配期 map」而不是运行时加载器：今天是手工 artifact 验收
// （无需编译服务），内循环零网络；资产服务的 Resolve/Fetch 门（QT-41）
// 落地后由装配方预取成同一张表。
func WithWASMArtifacts(artifacts ...WASMArtifact) EngineOption {
	return func(e *Engine) {
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.wasmArtifacts == nil {
			e.wasmArtifacts = make(map[string]WASMArtifact, len(artifacts))
		}
		for _, a := range artifacts {
			if a.Name == "" {
				continue
			}
			e.wasmArtifacts[a.Name] = a
		}
	}
}

// WithWASMShardBudget 注入 symbol scope 的分片预算（QT-25）。
//
// 只影响 scope=symbol 的 WASM 制品：market scope 是单实例模型，预算与
// 并发没有可调项。零值字段沿用 wasm.ShardBudget 的默认（Workers 取本 run
// 的 parallelWorkers，预算 512 MiB，每实例 64 MiB）。
//
// 这条选项只改「同时起几个实例」，不改信号与成交 —— W 变小只是变慢。
func WithWASMShardBudget(b wasm.ShardBudget) EngineOption {
	return func(e *Engine) {
		e.mu.Lock()
		e.wasmShardBudget = b
		e.mu.Unlock()
	}
}

// WithWASMShardObserver 注入 symbol scope 分片指标的观察面（QT-25 ③）。
// 接口与 RunObserver 同口径：pkg/backtest 只定义窄接口，不 import
// pkg/observability；*observability.Metrics 隐式实现。
func WithWASMShardObserver(ob WASMShardObserver) EngineOption {
	return func(e *Engine) {
		e.mu.Lock()
		e.wasmShardObserver = ob
		e.mu.Unlock()
	}
}

// ─── 资源准入（QT-12 ④⑤）─────────────────────────────────────────────
//
// 这一族选项改的是**准入闸**，不是引擎行为：闸只决定「这笔现在能不能开工、
// 预加载多少」，不决定成交与损益。所以有一条贯穿口径 ——
// **任何档位都不改回测结果**，改档只会改「多慢」和「拒不拒」。
// 全部必须在 RunBacktest 之前调用（装配期）；run 中途换闸等于把
// 已在跑的 run 的凭证发给别人的槽位。

// WithRunObserver 注入准入/缓存账的观察面（QT-12 ⑤ 的 backtest_runs_active /
// backtest_runs_queued / backtest_cache_bytes / backtest_wasm_bytes 的
// 生产者接线点）。
//
// 只接接口不引依赖：*observability.Metrics 隐式实现 RunObserver，
// pkg/backtest 不 import pkg/observability（口径同 nautilus.RunObserver，
// 编译期断言在 cmd/analysis/setup.go）。
//
// 传 nil = 不报错也不「顺手关掉闸」：闸照常工作，只是没有指标。闸的行为
// 不该依赖观察者存在与否 —— 否则接 metrics 这件事会悄悄改掉并发上限。
func WithRunObserver(ob RunObserver) EngineOption {
	return func(e *Engine) {
		e.mu.Lock()
		gate := e.admission
		e.mu.Unlock()
		if gate == nil {
			// WithRunAdmissionDisabled 之后没有闸可挂观察者。记一笔而不是
			// 静默：装配方以为接上了、实际永远没有指标，是最难查的那类配置错。
			e.logger.Warn().Msg("WithRunObserver 被忽略：准入闸已通过 WithRunAdmissionDisabled 关闭")
			return
		}
		gate.attachObserver(ob)
	}
}

// WithMaxConcurrentRuns 设在同时跑的 run 上限（默认 min(GOMAXPROCS, 4)）。
//
// n <= 0 归一成 1（串行）。已在跑的 run 不受影响：槽位 channel 被换成新的，
// 它们手上的凭证 Release 时写进新 channel —— 这正是「装配期才准改」的原因，
// 也是为什么本选项只给启动装配用，不给运行时调参用。
func WithMaxConcurrentRuns(n int) EngineOption {
	return func(e *Engine) {
		e.mu.Lock()
		gate := e.admission
		e.mu.Unlock()
		if gate == nil {
			return // 闸已关闭：不复活
		}
		gate.setMaxConcurrentRuns(n)
	}
}

// WithRunQueueLimit 设等待槽位的排队上限。
//
// 0 是合法档位（不给排队：槽位满即 ErrRunQueueFull 当场拒），负数归一成 0。
// 上限必须有界 —— 无界队列只是把 OOM 换成 goroutine 堆积，而且调用方
// 看不出「还要等 3 笔」和「永远等不到」的区别。
func WithRunQueueLimit(n int) EngineOption {
	return func(e *Engine) {
		e.mu.Lock()
		gate := e.admission
		e.mu.Unlock()
		if gate == nil {
			return
		}
		gate.setMaxQueuedRuns(n)
	}
}

// WithRunMemoryBudget 设单 run 的缓存预算（字节，估算口径见 estimateRunBytes）。
//
// 超过预算不会直接拒：先按「降 W → 降预加载窗口」让步，降到地板仍装不下
// 才 ErrRunBudgetExceeded。<= 0 保持默认档（不视同「无限」，也不视同「全拒」
// —— 这两种解释都比「写错了」走得更远）。
func WithRunMemoryBudget(bytes int64) EngineOption {
	return func(e *Engine) {
		e.mu.Lock()
		gate := e.admission
		e.mu.Unlock()
		if gate == nil || bytes <= 0 {
			return
		}
		gate.setRunBudget(bytes)
	}
}

// WithProcessMemoryBudget 设进程内在跑 run 的缓存总账上限（字节）。
//
// 这是「同机多笔并发」的那道闸：单 run 预算管不住 4 笔各自 1 GiB。
// 语义与 WithRunMemoryBudget 一致（<= 0 保持默认）。
func WithProcessMemoryBudget(bytes int64) EngineOption {
	return func(e *Engine) {
		e.mu.Lock()
		gate := e.admission
		e.mu.Unlock()
		if gate == nil || bytes <= 0 {
			return
		}
		gate.setProcessBudget(bytes)
	}
}

// WithRunAdmissionDisabled 关掉准入闸（槽位、队列、内存账全不生效）。
//
// 给谁用：单测与「就要一口气并发到底」的离线批算。生产路径不要用它 ——
// 那等于把 QT-12 修掉的「并发 run 各持一份缓存、没有上限」重新打开。
// 关掉后 applyAdmission / rc.close 都走 nil 分支，行为与没有闸一致。
func WithRunAdmissionDisabled() EngineOption {
	return func(e *Engine) {
		e.mu.Lock()
		e.admission = nil
		e.mu.Unlock()
		e.logger.Warn().Msg("run 准入闸已关闭（WithRunAdmissionDisabled）：并发上限、排队与内存预算均不生效")
	}
}

// WithWasmMemoryAccounting 注入 wasm 会话线性内存字节的取数闭包
// （QT-12 ⑤ 的 backtest_wasm_bytes 生产者）。
//
// 今天仓内没有 wasm 信号供给（接线是 QT-26），所以默认不注入 ⇒ 指标报 0。
// 指标存在但恒 0 比指标不存在更容易被发现是空的；注入后每次上报账都读它。
func WithWasmMemoryAccounting(fn func() int64) EngineOption {
	return func(e *Engine) {
		e.mu.Lock()
		e.wasmBytesAccounting = fn
		e.mu.Unlock()
	}
}

// applyOptions 按顺序应用所有选项。
func applyOptions(e *Engine, opts []EngineOption) {
	for _, opt := range opts {
		if opt != nil {
			opt(e)
		}
	}
}
