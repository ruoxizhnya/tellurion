package wasm

// QT-62 ⑤ 的验收腿：
//
//   ① 立项证据：数据段在 32768、__host_scratch_ptr 也声明在 32768 的 v2 模块
//      ⇒ **被拒且错误可检索**（本单之前这条过不了，因为下界只拦 < 4096）；
//   ② 破坏验证两腿：把数据段挪走 ⇒ 放行；把 params 声明挪到数据段上 ⇒ 同样被拒；
//   ③ 解析器自身的正/负腿：活动段能解出区间，被动段 / global.get offset /
//      越界字节流一律 fail-closed（不是"没数据"）。
//
// 全部用真 wasm 字节走真实加载路径（buildAbiModule / NewOnBarSession），
// 与 QT-01 的测试同一套纪律：不喂结构体给生产函数，喂 artifact。

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/backtest/contracts"
)

// ─── 立项证据：高地址数据段重叠 ──────────────────────────────────

// TestNewOnBarSession_V2ScratchPtrInsideHighDataSegment 是本单的立项证据。
//
// 声明本身完全合法：params [4096,8192)、scratch [32768,98304)，都 ≥ 下界、都
// 8 字节对齐、都在 2 页内存内 —— QT-01 的四类违规一条都没踩。唯一的问题是
// 模块把自己的数据段也放在 32768：host 一写 scratch 就改掉策略变量。
func TestNewOnBarSession_V2ScratchPtrInsideHighDataSegment(t *testing.T) {
	globals := []namedGlobal{
		{name: contracts.ExportHostParamsPtr, i32: 4096},
		{name: contracts.ExportHostScratchPtr, i32: 32768},
		{name: contracts.ExportHostScratchSize, i32: contracts.MinHostScratchSize},
	}
	data := []wasmDataSegment{{offset: 32768, bytes: make([]byte, 128)}}

	_, err := newSessionWith(t, []string{contracts.HostFnGetBar2}, globals, data, 2)
	require.Error(t, err, "宿主区落在模块自己的数据段必须被拒 —— 这正是本单之前判不住的那条")
	assert.ErrorIs(t, err, contracts.ErrHostRegionOverlapsData)
	assert.Contains(t, err.Error(), contracts.ExportHostScratchPtr, "报错要点名是哪一段声明")
	assert.Contains(t, err.Error(), "32768", "报错要给出重叠的地址，否则运维无从改起")

	// 破坏验证腿（放行）：把同一个数据段挪到 params 之下、且不与任何声明区间相交
	// ⇒ 必须放行。没有这条对照，上面的拒绝也可能只是"v2 一律加载失败"。
	moved := []wasmDataSegment{{offset: 1024, bytes: make([]byte, 128)}}
	sess, err := newSessionWith(t, []string{contracts.HostFnGetBar2}, globals, moved, 2)
	require.NoError(t, err, "数据段挪走后必须放行（否则新门变成了无条件拒绝）")
	defer sess.Close()

	// 破坏验证腿（另一侧被拒）：params 声明正好落在数据段起点。
	// 4096 ≥ MinHostParamsPtr，所以下界挡不住它 —— 只有静态数据判定能拦。
	paramsOnData := []namedGlobal{
		{name: contracts.ExportHostParamsPtr, i32: 4096},
		{name: contracts.ExportHostScratchPtr, i32: 32768},
		{name: contracts.ExportHostScratchSize, i32: contracts.MinHostScratchSize},
	}
	_, err = newSessionWith(t, []string{contracts.HostFnGetBar2}, paramsOnData,
		[]wasmDataSegment{{offset: 4096, bytes: make([]byte, 64)}}, 2)
	require.Error(t, err)
	assert.ErrorIs(t, err, contracts.ErrHostRegionOverlapsData)
	assert.Contains(t, err.Error(), contracts.ExportHostParamsPtr)
}

// TestValidateLoadedArtifact_UnprovableImageFailsClosed 钉「读不懂 ≠ 没风险」。
//
// 解析器一旦给出 Unprovable（被动段 / global.get offset），加载门必须把它变成
// 拒绝 —— 而不是当成"没有数据段"放行。这里用真 v2 会话 + 一张 Unprovable 的图，
// 单独测**门**的传播；"哪种字节会产出 Unprovable"由解析器的用例覆盖。
func TestValidateLoadedArtifact_UnprovableImageFailsClosed(t *testing.T) {
	sess, err := newSessionWith(t, []string{contracts.HostFnGetBar2}, validRegionGlobals(), nil, 2)
	require.NoError(t, err)
	defer sess.Close()

	_, err = ValidateLoadedArtifact(sess.inst, contracts.HostAbiV2, contracts.StaticDataImage{
		Unprovable: "data segment #0 is passive (bulk memory)",
	})
	require.Error(t, err, "不可证明的数据布局必须 fail-closed")
	assert.ErrorIs(t, err, contracts.ErrHostRegionDataUnprovable)
	assert.Contains(t, err.Error(), "passive")
}

// TestNewOnBarSession_V1KeepsLegacyLayoutDespiteData 是"新门不打死 v1"的对照腿。
//
// v1 artifact 没有宿主区声明（走 legacy 布局），因此静态数据判定对它不适用 ——
// 即使它把数据放在 32768，也必须照常加载并给出 legacy_host_layout 诊断。
func TestNewOnBarSession_V1KeepsLegacyLayoutDespiteData(t *testing.T) {
	globals := []namedGlobal{}
	data := []wasmDataSegment{{offset: 32768, bytes: make([]byte, 128)}}
	sess, err := newSessionWith(t, []string{contracts.HostFnGetBar}, globals, data, 2)
	require.NoError(t, err, "v1 走 legacy 布局，不得被 QT-62 的新门打死")
	defer sess.Close()

	diags, err := ValidateLoadedArtifact(sess.inst, contracts.HostAbiV1, contracts.StaticDataImage{})
	require.NoError(t, err)
	assert.Equal(t, []string{DiagLegacyHostLayout}, diags)
}

// ─── 解析器：活动段能解出区间 ────────────────────────────────────

func TestParseStaticDataImage_ActiveSegments(t *testing.T) {
	// 端到端：真模块（buildAbiModule 编出来的活动段）能解出与编码一致的区间。
	bytes := buildAbiModule(t, []string{contracts.HostFnGetBar2}, validRegionGlobals(),
		[]wasmDataSegment{
			{offset: 1024, bytes: make([]byte, 256)},
			{offset: 90000, bytes: make([]byte, 16)},
		}, 2)

	img, err := ParseStaticDataImage(bytes)
	require.NoError(t, err)
	require.Empty(t, img.Unprovable, "全部是活动段，不该有不可证明项")
	require.Len(t, img.Segments, 2)
	assert.Equal(t, contracts.StaticDataSegment{Offset: 1024, Length: 256}, img.Segments[0])
	assert.Equal(t, contracts.StaticDataSegment{Offset: 90000, Length: 16}, img.Segments[1])
}

func TestParseStaticDataImage_NoDataSection(t *testing.T) {
	bytes := buildAbiModule(t, []string{contracts.HostFnGetBar2}, validRegionGlobals(), nil, 2)
	img, err := ParseStaticDataImage(bytes)
	require.NoError(t, err)
	assert.Empty(t, img.Segments)
	assert.Empty(t, img.Unprovable, "没有数据段 = 真的没有可重叠的区间，不是读不懂")
}

func TestParseStaticDataImage_ExplicitMemoryZero(t *testing.T) {
	var payload []byte
	payload = appendUleb128(payload, 1) // 段数
	payload = appendUleb128(payload, 2) // mode 2: 显式 memory index
	payload = appendUleb128(payload, 0) // memory 0
	payload = append(payload, opI32Const)
	payload = appendSleb128(payload, 70000)
	payload = append(payload, opEnd)
	payload = appendUleb128(payload, 8)
	payload = append(payload, make([]byte, 8)...)

	img, err := ParseStaticDataImage(moduleWithDataPayload(payload))
	require.NoError(t, err)
	require.Empty(t, img.Unprovable)
	require.Len(t, img.Segments, 1)
	assert.Equal(t, contracts.StaticDataSegment{Offset: 70000, Length: 8}, img.Segments[0])
}

// ─── 解析器：证明不了的形态一律 fail-closed ──────────────────────

func TestParseStaticDataImage_UnprovableModes(t *testing.T) {
	globalGetPayload := func() []byte {
		var p []byte
		p = appendUleb128(p, 1)
		p = appendUleb128(p, 0) // mode 0
		p = append(p, 0x23)     // global.get
		p = appendUleb128(p, 0) // global index
		p = append(p, opEnd)
		p = appendUleb128(p, 4)
		return append(p, make([]byte, 4)...)
	}
	nonConstExprPayload := func() []byte {
		var p []byte
		p = appendUleb128(p, 1)
		p = appendUleb128(p, 0)
		p = append(p, opI32Const)
		p = appendSleb128(p, 4096)
		p = append(p, 0x6a) // i32.add —— 不是单条 i32.const
		p = append(p, opEnd)
		p = appendUleb128(p, 4)
		return append(p, make([]byte, 4)...)
	}

	tests := []struct {
		name       string
		payload    []byte
		wantPhrase string
	}{
		{"passive segment", passiveSegmentPayload([]byte("blob")), "passive"},
		{"global.get offset", globalGetPayload(), "global.get"},
		{"negative offset", activeSegmentPayload(-16, []byte("x")), "negative"},
		{"non-constant offset expression", nonConstExprPayload(), "single i32.const"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			img, err := ParseStaticDataImage(moduleWithDataPayload(tc.payload))
			require.NoError(t, err, "这些字节流本身是完整可读的，只是证明不了")
			assert.NotEmpty(t, img.Unprovable, "证明不了必须显式登记，不能返回空图")
			assert.Contains(t, img.Unprovable, tc.wantPhrase)
		})
	}
}

// TestParseStaticDataImage_Int32BoundaryOffsets 钉住 sleb128 的 32 位语义。
//
// 合法编码 `i32.const -2147483648` 是 5 字节（末字节 0x78）；若按「累加值超出 int32
// 就报错」实现，它会被误判成坏字节流 —— 而正确语义是取低 32 位 = 负数偏移，
// 应落进 "negative" 这条不可证明分支。正边界同理：2147483647 必须能解出来。
func TestParseStaticDataImage_Int32BoundaryOffsets(t *testing.T) {
	t.Run("min int32 is a negative offset, not a malformed stream", func(t *testing.T) {
		img, err := ParseStaticDataImage(moduleWithDataPayload(activeSegmentPayload(math.MinInt32, []byte("x"))))
		require.NoError(t, err, "这是合法编码，不该被当成坏字节流")
		assert.Contains(t, img.Unprovable, "negative")
	})
	t.Run("max int32 decodes as a concrete offset", func(t *testing.T) {
		img, err := ParseStaticDataImage(moduleWithDataPayload(activeSegmentPayload(math.MaxInt32, []byte("x"))))
		require.NoError(t, err)
		require.Empty(t, img.Unprovable)
		require.Len(t, img.Segments, 1)
		assert.Equal(t, uint32(math.MaxInt32), img.Segments[0].Offset)
	})
}

func TestParseStaticDataImage_MalformedBytesAreErrors(t *testing.T) {
	t.Run("bad magic", func(t *testing.T) {
		_, err := ParseStaticDataImage([]byte{0xff, 0x61, 0x73, 0x6d, 0, 0, 0, 0})
		require.Error(t, err)
		assert.ErrorIs(t, err, errStaticDataMalformed)
	})
	t.Run("short header", func(t *testing.T) {
		_, err := ParseStaticDataImage([]byte{0x00, 0x61})
		require.Error(t, err)
		assert.ErrorIs(t, err, errStaticDataMalformed)
	})
	t.Run("section length beyond the buffer", func(t *testing.T) {
		buf := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
		buf = append(buf, secData)
		buf = appendUleb128(buf, 64) // 声明 64 字节，实际没有
		_, err := ParseStaticDataImage(buf)
		require.Error(t, err)
		assert.ErrorIs(t, err, errStaticDataMalformed)
	})
	t.Run("truncated data payload", func(t *testing.T) {
		// 段数声明 1，但后面的内容在 flags 处就断了。
		payload := appendUleb128(nil, 1)
		_, err := ParseStaticDataImage(moduleWithDataPayload(payload))
		require.Error(t, err)
		assert.ErrorIs(t, err, errStaticDataMalformed)
	})
}

// ─── 测试夹具 ───────────────────────────────────────────────────

// moduleWithDataPayload 造一个只有 header + 指定 Data section 的模块字节流。
// 解析器只看 header 与 Data section，所以不需要其余节。
func moduleWithDataPayload(payload []byte) []byte {
	buf := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	return appendSection(buf, secData, func([]byte) []byte { return payload })
}

// activeSegmentPayload 编一个活动段（mode 0，memory 0）。
func activeSegmentPayload(offset int64, body []byte) []byte {
	var p []byte
	p = appendUleb128(p, 1) // 段数
	p = appendUleb128(p, 0) // mode 0
	p = append(p, opI32Const)
	p = appendSleb128(p, offset)
	p = append(p, opEnd)
	p = appendUleb128(p, uint32(len(body)))
	return append(p, body...)
}

// passiveSegmentPayload 编一个被动段（mode 1）—— 运行期才由 memory.init 决定落点。
func passiveSegmentPayload(body []byte) []byte {
	var p []byte
	p = appendUleb128(p, 1) // 段数
	p = appendUleb128(p, 1) // mode 1: passive
	p = appendUleb128(p, uint32(len(body)))
	return append(p, body...)
}
