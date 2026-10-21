package contracts

// QT-03 · 可复现留痕体系：三份 hash 的**唯一定义**。
//
// 立单理由（p0-round2-review N6/N7）：
//   - `run_spec_hash` 全设计集只被引用、从未定义 —— 参数只是 run 输入的一小部分，
//     只哈希参数等于没哈希。
//   - `dataset_snapshot_hash` 有两处不一致的定义（数据服务文档 4 项 vs adr-029
//     要求含 Axis），实现者会随机挑一个。
//   - `version_hash` 不含 toolchain，而 `strategy_ref@hash` 的 `@` 却指向它 ——
//     「锁定版本」锁不住字节码。
//
// 本文件把三者一次定死，并**成为唯一正文**：其余文档改为引用这里。
// 落库位置见文件末的说明（前提被证伪，另立单）。

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// sha256Hex 是留痕 hash 的统一摘要函数（域标签与帧结构在 Canonical 里，摘要在这里）。
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ─── artifact_hash：执行真相 ────────────────────────────────────

// ArtifactHash 返回 wasm / 插件字节码的 sha256（hex）。
//
// 刻意**不加域标签**：这一份要能被编译服务、缓存层、外部工具各自独立算出来并
// 逐字节相同（内容寻址）。域标签是给「我们自己的语义 hash」用的，字节码不是。
//
// N7 裁决：**执行与结果一律以 artifact_hash 为真相**；version_hash 降级为
// 「源码版本的人读标签」，两者是 1:N（同一份源码、两套工具链 → 两个 artifact_hash）。
func ArtifactHash(bytes []byte) string {
	return sha256Hex(bytes)
}

// ─── 分量 hash：universe / calendar / 复权锚点 ──────────────────

// UniverseHash 是票池**集合**的指纹（排序 + 去重后编码）。
//
// 集合语义：`["A","B"]` 与 `["B","A"]` 同一个 hash。反过来 calendar 是序列语义，
// 见 CalendarHash —— 这两个函数一个是 set 一个是 list，区别是刻意的，别互换。
func UniverseHash(symbols []string) string {
	c := NewCanonical(DomainUniverseHash)
	c.SymbolSet("symbols", symbols)
	return c.MustHex()
}

// CalendarHash 是交易日轴（Axis）的指纹，**保序**。
//
// 为什么 Axis 必须进 dataset_snapshot_hash（§7.1 / B1）：日历变了（供应商换了、
// 补了节假日）而 OHLCV 一个都没变时，T 的推进序列会变、结果会变。不把它纳入
// hash，就永远解释不了「同 hash 不同结果」。
//
// 保序不排序：`[d1,d2]` 与 `[d2,d1]` 是两条不同的轴。
func CalendarHash(days []time.Time) string {
	c := NewCanonical(DomainCalendarHash)
	encoded := make([]string, len(days))
	for i, d := range days {
		if d.IsZero() {
			encoded[i] = ""
			continue
		}
		encoded[i] = d.UTC().Format("2006-01-02")
	}
	c.Sequence("days", encoded)
	return c.MustHex()
}

// AdjustmentAnchorDigestsHash 是复权锚点 digest 集合的指纹（A11：按
// (symbol, start_date) 排序后的集合）。
//
// 为什么它是 run_spec_hash 的分量而不是 dataset 的分量：锚点快照是**这个 run
// 区间**冻结出来的（首写者胜、PK (symbol,start_date)），换一个区间就换一组锚点。
// A11 已经证明它能让 run 失败 —— 能让结果不同的东西必须能进 hash。
func AdjustmentAnchorDigestsHash(digests []string) string {
	c := NewCanonical(DomainAnchorDigestsHash)
	c.SymbolSet("digests", digests)
	return c.MustHex()
}

// ─── dataset_snapshot_hash：唯一正文 ───────────────────────────

// DatasetSnapshotInput 是一次数据快照的身份分量（N6-2 冻结的六项）。
//
// ⚠️ 本类型是 `dataset_snapshot_hash` 的**唯一定义处**。
// `data-service-contract-and-ownership.md` §2.1 那份四项定义（票池+窗口+频率+
// 数据版本）缺 `calendar_hash` 与 `adjustment_basis`，已在文档侧改为引用这里。
// 缺 calendar 的那一份会导致「日历补一个节假日 → 同 hash 不同结果」，正是
// A4/B1 反复要防的事。
type DatasetSnapshotInput struct {
	Universe        []string    // 票池集合（内部折成 universe_hash）
	Window          DateRange   // 请求区间
	Frequency       string      // "1d" / "1m" / …
	DataVersion     string      // 数据版本（供应商批次 / 表水位）
	Calendar        []time.Time // Axis：交易日轴（保序）
	AdjustmentBasis string      // 复权口径 + 基准锚点摘要（qfq/hfq/none + 锚点）
}

// DateRange 是一个闭区间日期对（UTC 日历日参与编码）。
type DateRange struct {
	Start time.Time
	End   time.Time
}

// Hash 产出 dataset_snapshot_hash。
func (in DatasetSnapshotInput) Hash() string {
	c := NewCanonical(DomainDatasetSnapshotHash)
	c.Str("universe_hash", UniverseHash(in.Universe))
	c.Time("window_start", in.Window.Start)
	c.Time("window_end", in.Window.End)
	c.Str("frequency", in.Frequency)
	c.Str("data_version", in.DataVersion)
	c.Str("calendar_hash", CalendarHash(in.Calendar))
	c.Str("adjustment_basis", in.AdjustmentBasis)
	return c.MustHex()
}

// ─── run_spec_hash：结果的一等身份 ────────────────────────────

// RunSpecInput 是一次回测的**全部决定性输入**（N6-1 冻结的 canonical 集合）。
//
// 写这个结构体的规矩：
//   - 影响结果的输入**必须**是这里的字段。加一个不影响 hash 的输入字段 = 造出
//     「同 hash 不同结果」，那是本体系唯一要防的失败模式。
//   - 字段的**编码顺序**在 Hash() 里逐行写死，与本结构的字段声明顺序无关；
//     想调整顺序等于让所有历史 hash 失效，必须升域标签（v1 → v2）。
//   - 空串/零值是**合法输入**（例如 builtin 载体没有 artifact_hash、批式策略没有
//     seed），Hash() 不判必填 —— 必填性由各契约自己管（如 QT-02 的
//     ErrFillTimingMissing）。哈希函数保持全函数，才能被任何调用点安全使用。
type RunSpecInput struct {
	// 区间与资金
	Start          time.Time
	End            time.Time
	InitialCapital float64
	RiskFreeRate   float64

	// 策略身份（N7：artifact_hash 是执行真相，version_hash 是人读标签）
	StrategyArtifactHash    string
	StrategyVersionHash     string
	StrategyParamsEffective map[string]any // 定义默认 ⊕ run 覆写后的**生效值**（A3）

	// 票池（B4：RunUniverse 是 run 级稳定超集，当日可交易性不进 hash）
	StockPool []string
	IndexCode string
	Universe  []string

	// 频率与成交口径
	Frequency string
	// FillTiming 是 QT-02 冻结的成交时点口径。底型 string ⇒ canonical 字节编码
	// 与它还是裸 string 时逐字节相同（黄金向量在 provenance_vector_test.go 钉住
	// 这一条：换成命名类型不该让任何历史 hash 变化）。
	FillTiming FillTiming

	// 内核与引擎版本（N6-5：结果必须记**实际执行核**，路由改写也要记）
	Kernel        string
	EngineVersion string

	// 成本与交易规则（今天完全不在任何 hash 里，却直接决定净值）
	Fees           map[string]float64 // 费率项名 → 费率（stamp_tax / commission / transfer_fee …）
	Slippage       float64
	CostModel      string
	LotSize        int64
	TPlus          string // "t+1" / "t+0"
	PriceLimitRule string // 涨跌停口径名（档位数值本身在 Fees/规则里，不重复登记）

	// 其它 hash 分量（各自有唯一正文）
	RiskRulesHash       string
	DatasetSnapshotHash string

	// 复权锚点集合（A11）
	AdjustmentAnchorDigests []string

	// 随机源（N6-4：默认 0 且必须显式）
	Seed int64
}

// Hash 产出 run_spec_hash。
//
// 不可编码的参数值（如 map 里塞了 struct）会返回错误而不是「跳过那个字段」：
// 跳过的字段改了也不变 hash，那就是一份会撒谎的留痕。
func (in RunSpecInput) Hash() (string, error) {
	c := NewCanonical(DomainRunSpecHash)
	c.Time("start", in.Start)
	c.Time("end", in.End)
	c.Float("initial_capital", in.InitialCapital)
	c.Float("risk_free_rate", in.RiskFreeRate)

	c.Str("strategy_artifact_hash", in.StrategyArtifactHash)
	c.Str("strategy_version_hash", in.StrategyVersionHash)
	c.Value("strategy_params_effective", in.StrategyParamsEffective)

	c.SymbolSet("stock_pool", in.StockPool)
	c.Str("index_code", in.IndexCode)
	c.Str("universe_hash", UniverseHash(in.Universe))

	c.Str("frequency", in.Frequency)
	c.Str("fill_timing", string(in.FillTiming))

	c.Str("kernel", in.Kernel)
	c.Str("engine_version", in.EngineVersion)

	c.Value("fees", in.Fees)
	c.Float("slippage", in.Slippage)
	c.Str("cost_model", in.CostModel)
	c.Int("lot_size", in.LotSize)
	c.Str("t_plus", in.TPlus)
	c.Str("price_limit_rule", in.PriceLimitRule)

	c.Str("risk_rules_hash", in.RiskRulesHash)
	c.Str("dataset_snapshot_hash", in.DatasetSnapshotHash)

	c.SymbolSet("adjustment_anchor_digests", in.AdjustmentAnchorDigests)
	c.Int("seed", in.Seed)

	return c.Hex()
}

// MustHash 是给「输入已被上层校验过」的调用点用的便捷出口。
func (in RunSpecInput) MustHash() string {
	hex, err := in.Hash()
	if err != nil {
		panic(fmt.Sprintf("contracts: run_spec_hash: %v", err))
	}
	return hex
}

// ─── strategy_ref 三形态（N7-3）──────────────────────────────

// StrategyRef 是一次运行引用的解析结果。
//
// 三种形态（asset-api §1.1/§3.3 + D25 + N7-3 写死的语义）：
//
//	形式                 语义                                   可复现性
//	id@artifact_hash    直接锁定字节码（CI / K15 / 台账推荐）      强
//	id@version_hash     解析到该源码版本当前 promote 的 artifact，  中（依赖 promote 不变）
//	                     并把实际 artifact_hash 回写进结果
//	id                  latest：提交边界解析 → 固化为 artifact_hash  强（提交后）
type StrategyRef struct {
	ID   string
	Hash string // 64 位 hex；空 = latest 语法糖
}

// IsLatest 报告这是不是不带 hash 的 latest 引用。
func (r StrategyRef) IsLatest() bool { return r.Hash == "" }

// String 还原成线上形态（与 ParseStrategyRef 互为逆运算）。
func (r StrategyRef) String() string {
	if r.IsLatest() {
		return r.ID
	}
	return r.ID + StrategyRefSeparator + r.Hash
}

// StrategyRefSeparator 是 id 与 hash 之间的分隔符。
const StrategyRefSeparator = "@"

// ErrStrategyRefMalformed 是引用串形状不对（空 id、多 @、hash 非 64 hex）。
var ErrStrategyRefMalformed = fmt.Errorf("provenance: malformed strategy_ref")

// ParseStrategyRef 解析 `id` / `id@hash`。
//
// ⚠️ **形态不可自证**：artifact_hash 与 version_hash 都是 sha256 的 64 位 hex，
// 光看字符串无法区分。谁是谁由 resolve 侧的查表决定（先查 artifact 行，查不到再
// 当 version_hash 解析到当前 promote 的 artifact）。⇒ 结果必须**同时记两个 hash**
// （`{strategy_ref_input, version_hash, artifact_hash}`，N7-5），否则「当初用的
// 是哪个引用」这个问题无法回答。这条限制在代码里留了测试钉住（TestStrategyRef_HashKindIsUndecidable）。
func ParseStrategyRef(raw string) (StrategyRef, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return StrategyRef{}, fmt.Errorf("%w: empty", ErrStrategyRefMalformed)
	}
	id, hash, hasSep := strings.Cut(trimmed, StrategyRefSeparator)
	if id == "" {
		return StrategyRef{}, fmt.Errorf("%w: %q has an empty strategy id", ErrStrategyRefMalformed, raw)
	}
	if !hasSep {
		return StrategyRef{ID: id}, nil
	}
	if strings.Contains(hash, StrategyRefSeparator) {
		return StrategyRef{}, fmt.Errorf("%w: %q has more than one %q", ErrStrategyRefMalformed, raw, StrategyRefSeparator)
	}
	if len(hash) != artifactHexLen {
		return StrategyRef{}, fmt.Errorf("%w: %q hash is %d chars, want %d hex",
			ErrStrategyRefMalformed, raw, len(hash), artifactHexLen)
	}
	if _, err := hex.DecodeString(hash); err != nil || strings.ContainsAny(hash, "ABCDEF") {
		// 严格小写：hash 的**字节**才是身份，容许大写就会有两个字符串指向同一个
		// hash、又各自被当成不同引用存进台账（N7-5 要回答的正是「当初用的哪个引用」）。
		return StrategyRef{}, fmt.Errorf("%w: %q hash is not lowercase hex", ErrStrategyRefMalformed, raw)
	}
	return StrategyRef{ID: id, Hash: hash}, nil
}

// artifactHexLen 是 sha256 的 hex 长度。
const artifactHexLen = 64
