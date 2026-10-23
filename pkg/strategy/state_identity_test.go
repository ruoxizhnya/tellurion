// QT-16：检查点身份的纯函数单测（零 DB 依赖）。
//
// 被测对象：StateIdentity.RunIdentityHash（canonical 复合身份）与
// StateEnvelope.Verify（恢复门）。两者是 A9/N3 fail-closed 红线的执行点：
// hash 必须对四个轴逐一敏感、对无关变化（票池顺序）不敏感；Verify 必须
// 点名全部差异项且 errors.Is 可命中哨兵。
package strategy

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// idHash 是测试便捷封装：算不出 hash 就让用例当场失败。
func idHash(t *testing.T, id StateIdentity) string {
	t.Helper()
	h, err := id.RunIdentityHash()
	require.NoError(t, err)
	return h
}

func TestRunIdentityHashDeterministic(t *testing.T) {
	a := idHash(t, StateIdentity{Scope: ScopeMarket, Universe: []string{"600000.SH", "000001.SZ"}, ParamsHash: "p1"})
	b := idHash(t, StateIdentity{Scope: ScopeMarket, Universe: []string{"600000.SH", "000001.SZ"}, ParamsHash: "p1"})
	require.Equal(t, a, b, "同输入必须同 hash")
	require.Len(t, a, 64, "run_identity_hash 应是 sha256 hex")
}

// TestRunIdentityHashUniverseOrderInsensitive 票池是集合不是序列：
// ["A","B"] 与 ["B","A"] 是同一个票池，必须同 hash（否则同一次 run 因
// map 遍历顺序不同产生两个身份，恢复永远对不上）。
func TestRunIdentityHashUniverseOrderInsensitive(t *testing.T) {
	a := idHash(t, StateIdentity{Scope: ScopeMarket, Universe: []string{"600000.SH", "000001.SZ"}})
	b := idHash(t, StateIdentity{Scope: ScopeMarket, Universe: []string{"000001.SZ", "600000.SH"}})
	require.Equal(t, a, b)
}

// TestRunIdentityHashDifferentiatesEachAxis 四个轴逐个变，hash 逐个变——
// 漏掉任何一个轴 = 那个轴上的变化偷走别人的检查点（A9 判据 A 的静默错误）。
func TestRunIdentityHashDifferentiatesEachAxis(t *testing.T) {
	base := StateIdentity{Scope: ScopeMarket, ShardKey: "", Universe: []string{"600000.SH"}, ParamsHash: "p1"}
	baseHash := idHash(t, base)

	t.Run("universe", func(t *testing.T) {
		changed := base
		changed.Universe = []string{"600000.SH", "000001.SZ"}
		require.NotEqual(t, baseHash, idHash(t, changed))
	})
	t.Run("scope", func(t *testing.T) {
		changed := base
		changed.Scope = ScopeSymbol
		changed.ShardKey = "600000.SH" // symbol scope 必须带 shard，否则在计算处被拒
		require.NotEqual(t, baseHash, idHash(t, changed))
	})
	t.Run("shard_key", func(t *testing.T) {
		mkt := base
		sym := StateIdentity{Scope: ScopeSymbol, ShardKey: "600000.SH", Universe: base.Universe, ParamsHash: base.ParamsHash}
		sym2 := StateIdentity{Scope: ScopeSymbol, ShardKey: "000001.SZ", Universe: base.Universe, ParamsHash: base.ParamsHash}
		require.NotEqual(t, idHash(t, mkt), idHash(t, sym))
		require.NotEqual(t, idHash(t, sym), idHash(t, sym2), "symbol scope 不同标的必须不同身份")
	})
	t.Run("params", func(t *testing.T) {
		changed := base
		changed.ParamsHash = "p2"
		require.NotEqual(t, baseHash, idHash(t, changed))
	})
}

// TestRunIdentityHashRejectsInconsistentScope scope 与 shard_key 的矛盾组合
// 在计算处 fail-loud，不静默归一（归一 = 身份撒谎）。
func TestRunIdentityHashRejectsInconsistentScope(t *testing.T) {
	cases := []struct {
		name string
		id   StateIdentity
	}{
		{"unknown scope", StateIdentity{Scope: "portfolio", ShardKey: "x"}},
		{"market with shard", StateIdentity{Scope: ScopeMarket, ShardKey: "600000.SH"}},
		{"symbol without shard", StateIdentity{Scope: ScopeSymbol, ShardKey: ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.id.RunIdentityHash()
			require.Error(t, err)
		})
	}
}

// TestStateEnvelopeVerifyNamesAllMismatches 恢复门：任一不符 →
// ErrStateIdentityMismatch（errors.Is 可命中）且**点名全部**差异项；
// 全匹配 → nil。
func TestStateEnvelopeVerifyNamesAllMismatches(t *testing.T) {
	want := StateEnvelope{
		StateSchemaVersion: "v2",
		ArtifactHash:       "artifact-a",
		AbiVersion:         "v1",
		RunIdentityHash:    "ident-a",
	}

	t.Run("all match", func(t *testing.T) {
		require.NoError(t, want.Verify(StateEnvelope{
			StateSchemaVersion: "v2",
			ArtifactHash:       "artifact-a",
			AbiVersion:         "v1",
			RunIdentityHash:    "ident-a",
		}))
	})

	t.Run("each field named", func(t *testing.T) {
		cases := []struct {
			name   string
			stored StateEnvelope
			wantIn string
		}{
			{"schema version", StateEnvelope{StateSchemaVersion: "v1", ArtifactHash: "artifact-a", AbiVersion: "v1", RunIdentityHash: "ident-a"}, "state_schema_version"},
			{"artifact", StateEnvelope{StateSchemaVersion: "v2", ArtifactHash: "artifact-b", AbiVersion: "v1", RunIdentityHash: "ident-a"}, "artifact_hash"},
			{"abi", StateEnvelope{StateSchemaVersion: "v2", ArtifactHash: "artifact-a", AbiVersion: "v9", RunIdentityHash: "ident-a"}, "abi_version"},
			{"run identity", StateEnvelope{StateSchemaVersion: "v2", ArtifactHash: "artifact-a", AbiVersion: "v1", RunIdentityHash: "ident-b"}, "run_identity_hash"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				err := want.Verify(tc.stored)
				require.Error(t, err)
				require.ErrorIs(t, err, ErrStateIdentityMismatch, "必须可用 errors.Is 命中哨兵")
				require.Contains(t, err.Error(), tc.wantIn, "差异项要被点名")
				require.Contains(t, err.Error(), "存=", "错误要给出存值与期望值，让人不用翻库")
			})
		}
	})

	t.Run("multiple mismatches all named", func(t *testing.T) {
		err := want.Verify(StateEnvelope{})
		require.ErrorIs(t, err, ErrStateIdentityMismatch)
		for _, field := range []string{"state_schema_version", "artifact_hash", "abi_version", "run_identity_hash"} {
			require.Contains(t, err.Error(), field)
		}
	})

	t.Run("run identity mismatch names covered axes not a guess", func(t *testing.T) {
		err := want.Verify(StateEnvelope{StateSchemaVersion: "v2", ArtifactHash: "artifact-a", AbiVersion: "v1", RunIdentityHash: "ident-b"})
		// 复合项不反解子轴，但必须把覆盖轴说清、给出处置（从零跑）。
		for _, axis := range []string{"票池", "scope", "分片", "生效参数", "从零跑"} {
			require.Contains(t, err.Error(), axis)
		}
	})
}

// TestStateEnvelopeVerifyLegacyRowRejected QT-16 之前的旧行信封是空串哨兵
// （迁移 DEFAULT ”）；对上任何非空期望都必须被点名拒绝——旧行不是新格式，
// 这是「不做尽力解释旧格式」的有意行为。
func TestStateEnvelopeVerifyLegacyRowRejected(t *testing.T) {
	want := StateEnvelope{StateSchemaVersion: "v1", ArtifactHash: "a", AbiVersion: "v1", RunIdentityHash: "h"}
	err := want.Verify(StateEnvelope{})
	require.ErrorIs(t, err, ErrStateIdentityMismatch)
}
