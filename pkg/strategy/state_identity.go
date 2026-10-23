// strategy-runtime 检查点身份（QT-16 / A9 + N3）。
//
// 回答一个问题：「这份检查点字节属于哪个实例、哪个票池、哪版参数」。
//
// 三层身份，各有职责：
//
//  1. 行坐标 StateKey = (strategy_id, run_id, shard_key) —— market scope 的
//     状态落 shard_key=""；symbol scope 按标的各占一行，5000 个分片不再
//     互相覆盖（N3 的病根）。
//  2. 复合身份 run_identity_hash —— 覆盖 {run_universe_hash, scope,
//     shard_key, param_effective_hash} 四分量；「同一 artifact 换了票池 /
//     参数」产生不同 hash。计算走 contracts.Canonical + contracts 新域标签
//     DomainRunIdentityHash（w1-repo-ownership §2.5 裁决：任何仓都不得自写
//     json.Marshal + sha256；常量住 contracts，计算函数留 core 消费方）。
//  3. 信封 StateEnvelope 四元组 {state_schema_version, artifact_hash,
//     abi_version, run_identity_hash} —— 落库随行，恢复时全匹配才放行。
//
// fail-closed 红线：恢复时任一项不符 → ErrStateIdentityMismatch 并点名
// 差异项；**不静默重置、不「尽力解释旧格式」**（A9 ④）。QT-16 之前的旧行
// 信封是空串哨兵，对上任何非空期望都会被点名拒绝——旧行不是新格式，这是
// 有意行为而非迁移缺陷。
package strategy

import (
	"errors"
	"fmt"
	"strings"

	contracts "github.com/ruoxizhnya/tellurion-contracts/engine"
)

// Scope 取值：检查点按执行范围分片。
const (
	// ScopeMarket：整段序列单实例推进，状态一份（shard_key 恒 ""）。
	ScopeMarket = "market"
	// ScopeSymbol：每标的一个实例 / 一份状态（shard_key = 标的）。
	ScopeSymbol = "symbol"
)

// ErrStateIdentityMismatch：检查点信封与当前 run 的身份不符。fail-loud 哨兵
// ——调用方用 errors.Is 判定；错误消息点名全部差异项，让人能直接回答
// 「是票池、参数还是 artifact 变了」。
var ErrStateIdentityMismatch = errors.New("strategy: 检查点身份不符（fail-closed，不恢复）")

// StateIdentity 是一次 run 的身份输入：RunIdentityHash 的计算材料。
//
// 由调用方（引擎 / QT-15 的重放路径）装配；检查点层（checkpoint.go）只消费
// 算好的 StateEnvelope，不反推这些字段。
type StateIdentity struct {
	// Scope：ScopeMarket / ScopeSymbol。
	Scope string
	// ShardKey：market scope 必须为 ""；symbol scope 必须为标的。不一致组合
	// 在计算处 fail-loud，不静默归一。
	ShardKey string
	// Universe：RunUniverse（排序 / 去重由 contracts.UniverseHash 的集合语义
	// 处理，调用方不必先排）。
	Universe []string
	// ParamsHash：生效参数指纹（contracts.ParamsHash 的产物）。参数投递通路
	// 归 QT-20，在那之前调用方对「无参数」传 ParamsHash(nil) 的结果即可。
	ParamsHash string
}

// RunIdentityHash 计算复合身份。顺序就是契约（漏字段与空值是两件事），
// 每个分量都显式写一遍。
func (id StateIdentity) RunIdentityHash() (string, error) {
	switch {
	case id.Scope != ScopeMarket && id.Scope != ScopeSymbol:
		return "", fmt.Errorf("strategy: RunIdentityHash: scope %q 非法（只能是 %q / %q）",
			id.Scope, ScopeMarket, ScopeSymbol)
	case id.Scope == ScopeMarket && id.ShardKey != "":
		return "", fmt.Errorf("strategy: RunIdentityHash: market scope 的 shard_key 必须为空串，got %q",
			id.ShardKey)
	case id.Scope == ScopeSymbol && id.ShardKey == "":
		return "", fmt.Errorf("strategy: RunIdentityHash: symbol scope 的 shard_key 必须是标的，got 空串")
	}
	c := contracts.NewCanonical(contracts.DomainRunIdentityHash)
	c.Str("run_universe_hash", contracts.UniverseHash(id.Universe))
	c.Str("scope", id.Scope)
	c.Str("shard_key", id.ShardKey)
	c.Str("param_effective_hash", id.ParamsHash)
	return c.Hex()
}

// StateEnvelope 是检查点信封四元组（N3 冻结设计）：Save 随行落库，Load 原样
// 读回，恢复前经 Verify 全量对照。
type StateEnvelope struct {
	// StateSchemaVersion：策略清单声明的状态结构版本；改状态结构必须自增
	//（清单机制归 QT-22，在那之前由调用方如实声明）。
	StateSchemaVersion string
	// ArtifactHash：写这份状态的策略 artifact（contracts.ArtifactHash 的产物）。
	ArtifactHash string
	// AbiVersion：artifact 的 abi 家族 / 版本。
	AbiVersion string
	// RunIdentityHash：StateIdentity.RunIdentityHash 的产物。
	RunIdentityHash string
}

// Verify 对照期望信封（当前 run 的身份）与库中存出的信封：四项全匹配才放行；
// 任一不符 → 包装 ErrStateIdentityMismatch 并**点名全部差异项**。
//
// run_identity_hash 是复合项，行内不存子分量，因此这一项只能点到「复合身份
// 变了」并给出覆盖轴清单（票池 / scope / 分片 / 参数）——不反解、不猜哪个子
// 项变了。换票池 / 换参数的处置一致：从零跑，不要恢复。
func (want StateEnvelope) Verify(stored StateEnvelope) error {
	var diffs []string
	if stored.StateSchemaVersion != want.StateSchemaVersion {
		diffs = append(diffs, fmt.Sprintf("state_schema_version 存=%q 期望=%q —— 状态结构版本变了",
			stored.StateSchemaVersion, want.StateSchemaVersion))
	}
	if stored.ArtifactHash != want.ArtifactHash {
		diffs = append(diffs, fmt.Sprintf("artifact_hash 存=%q 期望=%q —— 策略 artifact 变了",
			stored.ArtifactHash, want.ArtifactHash))
	}
	if stored.AbiVersion != want.AbiVersion {
		diffs = append(diffs, fmt.Sprintf("abi_version 存=%q 期望=%q —— abi 版本变了",
			stored.AbiVersion, want.AbiVersion))
	}
	if stored.RunIdentityHash != want.RunIdentityHash {
		diffs = append(diffs, fmt.Sprintf("run_identity_hash 存=%q 期望=%q —— run 身份变了（覆盖轴：票池 / scope / 分片 / 生效参数 其中之一；换票池或换参数请从零跑，不要恢复）",
			stored.RunIdentityHash, want.RunIdentityHash))
	}
	if len(diffs) == 0 {
		return nil
	}
	return fmt.Errorf("%w:\n  - %s", ErrStateIdentityMismatch, strings.Join(diffs, "\n  - "))
}
