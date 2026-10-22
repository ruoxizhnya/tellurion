package wasm

// QT-62 · 静态腿：从 artifact 字节自己解 Data section（id=11），给「宿主区是否
// 落在模块自己的静态数据上」一个**与地址无关**的判据。
//
// 为什么不能在运行时问：wazero v1.12 的 CompiledModule 不暴露 Data section，
// instantiate 之后拿得到的只有 global 值与 Memory().Size()。于是"声明的
// __host_scratch_ptr 正好落在模块数据段中间"这件事，在 ptr >= 4096 时完全
// 看不出来 —— host 一写 params 就把策略变量改掉，且不报错（QT-01 ⑤ 的第二类
// 坏 artifact，QT-62 的立项证据）。
//
// 解析范围刻意收窄：只认 WebAssembly 1.0 的三种数据段模式（0 活动/memory 0、
// 1 被动、2 活动/显式 memory index），offset 表达式只认单个 `i32.const <n>`。
// 其余一律标成 contracts.StaticDataImage.Unprovable 交调用方 fail-closed：
// **读不懂不等于没风险**，尤其不能在解不出来时返回一张空图。
//
// 本文件不执行任何 wasm，也不依赖 wazero —— 给它字节，还它区间。

import (
	"errors"
	"fmt"
	"math"

	"tellurion/pkg/backtest/contracts"
)

const (
	// sectionIDData 是 Data section（WebAssembly 1.0 二进制布局）。
	sectionIDData byte = 11
	// sectionIDDataCount 是 bulk-memory 的 DataCount section。本解析器**不**据它
	// 单独下结论（有它不代表有被动段），但保留常量，说明该节会被跳过而非误当 Data。
	sectionIDDataCount byte = 12
)

// wasm 二进制里本解析器要认的字节。
const (
	wasmOpI32Const  byte = 0x41
	wasmOpGlobalGet byte = 0x23
	wasmOpEnd       byte = 0x0b
)

// 数据段模式（bulk-memory 提案后的 flag 编码）。用 uint32 是因为它由 u32 读出，
// 与解析器的读取类型保持一致（换 byte 会在 switch 上产生类型不匹配）。
const (
	dataModeActiveMemory0 uint32 = 0
	dataModePassive       uint32 = 1
	dataModeActiveIndexed uint32 = 2
)

// errStaticDataMalformed 是「字节流本身不完整」——它只可能来自解析器与
// 编码器之间的 bug，因此与 Unprovable（合法但证明不了）分开报。
var errStaticDataMalformed = errors.New("wasm: malformed wasm binary while reading the data section")

// ParseStaticDataImage 解出 artifact 里**活动数据段**的常量区间图。
//
// 返回 error 的只有"字节流读不下去"；"能读但证明不了"走 StaticDataImage.Unprovable。
// 两者最终都是 fail-closed，区别在于排障方向：前者指向解析器/字节流，后者指向
// artifact 用了本仓静态判定覆盖不到的构造（被动段 / global.get offset）。
func ParseStaticDataImage(wasmBytes []byte) (contracts.StaticDataImage, error) {
	r := &wasmReader{buf: wasmBytes}
	if err := r.readHeader(); err != nil {
		return contracts.StaticDataImage{}, err
	}

	var img contracts.StaticDataImage
	seenData := false
	for r.remaining() > 0 {
		id, err := r.u8()
		if err != nil {
			return contracts.StaticDataImage{}, err
		}
		size, err := r.u32()
		if err != nil {
			return contracts.StaticDataImage{}, err
		}
		payload, err := r.take(size)
		if err != nil {
			return contracts.StaticDataImage{}, err
		}
		switch id {
		case sectionIDData:
			if seenData {
				return contracts.StaticDataImage{}, fmt.Errorf("%w: duplicate data section", errStaticDataMalformed)
			}
			seenData = true
			parsed, perr := parseDataSection(payload)
			if perr != nil {
				return contracts.StaticDataImage{}, perr
			}
			img = parsed
		case sectionIDDataCount:
			// 刻意什么都不做：DataCount 只是 memory.init/data.drop 的索引声明，
			// 真正的被动段会以 flag=1 出现在 Data section 里，那里才判 Unprovable。
			// 在这里凭"有 DataCount"就拒绝，会把合法使用 bulk memory 的
			// TinyGo 产物一起打死（假红比没护栏更糟）。
			continue
		default:
			continue
		}
	}
	return img, nil
}

// parseDataSection 解一段 Data section payload。
//
// 一旦某段证明不了，立即带着已解出的部分返回：判定已经成立（fail-closed），
// 再往下解只会把"读不懂"伪装成"读完了"。
func parseDataSection(payload []byte) (contracts.StaticDataImage, error) {
	r := &wasmReader{buf: payload}
	count, err := r.u32()
	if err != nil {
		return contracts.StaticDataImage{}, err
	}

	img := contracts.StaticDataImage{}
	for i := uint32(0); i < count; i++ {
		mode, err := r.u32()
		if err != nil {
			return contracts.StaticDataImage{}, err
		}
		switch mode {
		case dataModeActiveMemory0:
			seg, unprovable, err := readActiveSegment(r, i)
			if err != nil {
				return contracts.StaticDataImage{}, err
			}
			if unprovable != "" {
				img.Unprovable = unprovable
				return img, nil
			}
			img.Segments = append(img.Segments, seg)
		case dataModeActiveIndexed:
			memIdx, err := r.u32()
			if err != nil {
				return contracts.StaticDataImage{}, err
			}
			if memIdx != 0 {
				// host 只写线性内存 0；别的 memory 上的段与宿主区无交集，
				// 但本仓的沙箱也只支持单内存，走到这里说明 artifact 超出了
				// 本仓可证明的范围 —— 拒绝而不是猜。
				img.Unprovable = fmt.Sprintf(
					"data segment #%d targets memory %d, not the linear memory the host writes", i, memIdx)
				return img, nil
			}
			seg, unprovable, err := readActiveSegment(r, i)
			if err != nil {
				return contracts.StaticDataImage{}, err
			}
			if unprovable != "" {
				img.Unprovable = unprovable
				return img, nil
			}
			img.Segments = append(img.Segments, seg)
		case dataModePassive:
			img.Unprovable = fmt.Sprintf(
				"data segment #%d is passive (bulk memory); its destination address only exists at run time", i)
			return img, nil
		default:
			img.Unprovable = fmt.Sprintf("data segment #%d uses unknown mode %d", i, mode)
			return img, nil
		}
	}
	return img, nil
}

// readActiveSegment 读「offset 表达式 + 段长度 + 段字节」，返回常量区间。
func readActiveSegment(r *wasmReader, idx uint32) (contracts.StaticDataSegment, string, error) {
	offset, reason, err := r.constOffsetExpr()
	if err != nil {
		return contracts.StaticDataSegment{}, "", err
	}
	if reason != "" {
		return contracts.StaticDataSegment{}, fmt.Sprintf("data segment #%d: %s", idx, reason), nil
	}
	length, err := r.u32()
	if err != nil {
		return contracts.StaticDataSegment{}, "", err
	}
	if err := r.skip(length); err != nil {
		return contracts.StaticDataSegment{}, "", err
	}
	return contracts.StaticDataSegment{Offset: offset, Length: length}, "", nil
}

// constOffsetExpr 解 offset 表达式，只接受 `i32.const <非负 32 位> end`。
//
// 返回 (offset, 不可证明的原因, 读取错误)。原因非空 = 合法但证明不了；
// 读取错误 = 字节流坏了。
func (r *wasmReader) constOffsetExpr() (uint32, string, error) {
	op, err := r.u8()
	if err != nil {
		return 0, "", err
	}
	switch op {
	case wasmOpI32Const:
		v, err := r.s32()
		if err != nil {
			return 0, "", err
		}
		end, err := r.u8()
		if err != nil {
			return 0, "", err
		}
		if end != wasmOpEnd {
			return 0, "offset expression is not a single i32.const", nil
		}
		if v < 0 {
			// wasm 的 i32 偏移是 u32 解释，负值在实例化时就会 trap；
			// 静态图里不替它圆成一个大正数。
			return 0, fmt.Sprintf("offset expression is negative (%d)", v), nil
		}
		return uint32(v), "", nil
	case wasmOpGlobalGet:
		return 0, "offset expression is global.get, so the address is not known before instantiation", nil
	default:
		return 0, fmt.Sprintf(
			"offset expression starts with opcode 0x%02x; only a constant i32.const is provable", op), nil
	}
}

// ─── 最小字节读取器 ─────────────────────────────────────────────

type wasmReader struct {
	buf []byte
	pos int
}

func (r *wasmReader) remaining() int { return len(r.buf) - r.pos }

func (r *wasmReader) readHeader() error {
	const headerLen = 8
	if len(r.buf) < headerLen {
		return fmt.Errorf("%w: %d bytes is shorter than the 8-byte wasm header", errStaticDataMalformed, len(r.buf))
	}
	magic := []byte{0x00, 0x61, 0x73, 0x6d}
	for i, want := range magic {
		if r.buf[i] != want {
			return fmt.Errorf("%w: bad magic", errStaticDataMalformed)
		}
	}
	if r.buf[4] != 0x01 || r.buf[5] != 0x00 || r.buf[6] != 0x00 || r.buf[7] != 0x00 {
		return fmt.Errorf("%w: unsupported wasm version %v", errStaticDataMalformed, r.buf[4:8])
	}
	r.pos = headerLen
	return nil
}

func (r *wasmReader) u8() (byte, error) {
	if r.pos >= len(r.buf) {
		return 0, fmt.Errorf("%w: unexpected end of input at byte %d", errStaticDataMalformed, r.pos)
	}
	b := r.buf[r.pos]
	r.pos++
	return b, nil
}

func (r *wasmReader) take(n uint32) ([]byte, error) {
	// 先按剩余长度比，再转 int：直接算 r.pos+int(n) 在 32 位上可能溢出成负数。
	if uint64(n) > uint64(r.remaining()) {
		return nil, fmt.Errorf("%w: section length %d exceeds the remaining %d bytes",
			errStaticDataMalformed, n, r.remaining())
	}
	end := r.pos + int(n)
	out := r.buf[r.pos:end]
	r.pos = end
	return out, nil
}

func (r *wasmReader) skip(n uint32) error {
	_, err := r.take(n)
	return err
}

// u32 读无符号 LEB128（wasm 的段长度、段数、flags、memory index）。
func (r *wasmReader) u32() (uint32, error) {
	var result uint64
	var shift uint
	for {
		b, err := r.u8()
		if err != nil {
			return 0, err
		}
		result |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			if result > math.MaxUint32 {
				return 0, fmt.Errorf("%w: uleb128 value overflows uint32", errStaticDataMalformed)
			}
			return uint32(result), nil
		}
		shift += 7
		if shift >= 35 {
			return 0, fmt.Errorf("%w: uleb128 is longer than 5 bytes", errStaticDataMalformed)
		}
	}
}

// s32 读有符号 LEB128（i32.const 的操作数）。
//
// 刻意**不做**「超出 int32 就报错」的检查：wasm 对 i32 立即数的语义就是取低 32 位，
// 合法的 -2^31（5 字节 0x…0x78）在 int64 里累加后是 32212254720，截断到 int32
// 才是 -2147483648。加范围检查会把合法的负偏移误报成坏字节流。
// 非规范编码由 wasm 校验器在 compile 期拒绝，不需要这里重复。
func (r *wasmReader) s32() (int32, error) {
	var result int64
	var shift uint
	var last byte
	for {
		b, err := r.u8()
		if err != nil {
			return 0, err
		}
		last = b
		result |= int64(b&0x7f) << shift
		shift += 7
		if b&0x80 == 0 {
			break
		}
		if shift >= 35 {
			return 0, fmt.Errorf("%w: sleb128 is longer than 5 bytes", errStaticDataMalformed)
		}
	}
	if shift < 32 && last&0x40 != 0 {
		result |= -1 << shift
	}
	return int32(result), nil
}
