package wasm

// 极简 WASM binary 编码器（测试用）。参考 wazero 官方测试的 encodeModule
// 做法，只支持本测试需要的子集：type / import / function / memory / global /
// export / code / data 八个 section。无 table、无 element、无 start。
//
// 本文件只服务测试，不引入生产代码路径。
//
// QT-01 之后这里多了两样能力，都是「坏 artifact」造证据的前置：
//   - global（i32 常量 + 导出）：N5 的 __host_params_ptr / __host_scratch_ptr /
//     __host_scratch_size 就是导出 i32 全局，没有它就测不了宿主区校验；
//   - data（活动数据段）：要造「ptr 落在模块自己的数据段」那种坏 artifact，
//     数据段必须真的在那儿，否则测试只是在测一个假想的地址。

// 值类型
const (
	valI32 byte = 0x7f
	valI64 byte = 0x7e
	valF64 byte = 0x7c
)

// section id（WebAssembly 1.0 二进制布局）
const (
	secType   byte = 1
	secImport byte = 2
	secFunc   byte = 3
	secMemory byte = 5
	secGlobal byte = 6
	secExport byte = 7
	secCode   byte = 10
	secData   byte = 11
)

// export kind
const (
	expKindFunc   byte = 0x00
	expKindGlobal byte = 0x03
)

// 指令 opcode（本测试用到的子集）
const (
	opLocalGet      byte = 0x20
	opI64ExtendI32U byte = 0xad
	opI64Const      byte = 0x42
	opI64Shl        byte = 0x86
	opI64Or         byte = 0x84
	opI32Const      byte = 0x41
	opF64Const      byte = 0x44
	opCall          byte = 0x10
	opDrop          byte = 0x1a
	opBr            byte = 0x0c
	opBrIf          byte = 0x0d
	opBlock         byte = 0x02
	opLoop          byte = 0x03
	opEnd           byte = 0x0b
	opI32Eqz        byte = 0x45
	opI32GeS        byte = 0x4e
	opI32Add        byte = 0x6a
	opLocalSet      byte = 0x21
)

// blocktype void（block/loop 后跟的立即数：0x40 = 空结果类型）。
const blockTypeVoid byte = 0x40

// wasmFuncType 描述一个函数签名。
type wasmFuncType struct {
	params  []byte
	results []byte
}

// wasmImport 描述一个函数 import（module + name + 类型索引）。kind 恒为 0。
// 导入函数按声明序编号，位于本地 funcs 之前（wasm 函数索引空间规则）。
type wasmImport struct {
	module  string
	name    string
	typeIdx uint32
}

// wasmExport 描述一个导出（name + kind + index）。
// kind: 0=func, 2=memory。
type wasmExport struct {
	name string
	kind byte
	idx  uint32
}

// wasmMemory 描述一个内存声明（min 页）。
type wasmMemory struct {
	min uint32
}

// wasmGlobal 是一个常量全局。N5 的三个声明符号用 i32；valType 也允许 i64，
// 是为了能造出「声明符号类型不对」这种坏 artifact。
type wasmGlobal struct {
	valType byte
	i32     int32
	i64     int64
}

// wasmDataSegment 是一段**活动**数据段（memory 0，偏移为 i32.const）。
// 这就是「模块自己的静态数据」——host 往这段地址写就是踩模块的变量。
type wasmDataSegment struct {
	offset int32
	bytes  []byte
}

// wasmModule 是本编码器支持的最小模块模型。
type wasmModule struct {
	types   []wasmFuncType
	imports []wasmImport // 导入函数（函数索引空间：先 imports 后 funcs）
	funcs   []uint32     // 本地函数 → 类型索引
	memory  *wasmMemory
	globals []wasmGlobal
	data    []wasmDataSegment
	export  []wasmExport
	codes   [][]byte // 本地函数体（不含 locals 前缀，本测试无局部变量）
}

// funcIndexBase 是本地函数的起始索引（= 导入函数数）。
func (m *wasmModule) funcIndexBase() uint32 { return uint32(len(m.imports)) }

// encode 序列化为合法 wasm 二进制。
func (m *wasmModule) encode() []byte {
	var buf []byte
	buf = append(buf, 0x00, 0x61, 0x73, 0x6d) // \0asm
	buf = append(buf, 0x01, 0x00, 0x00, 0x00) // version 1

	// Type section (id=1)
	buf = appendSection(buf, secType, func(s []byte) []byte {
		s = appendUleb128(s, uint32(len(m.types)))
		for _, ft := range m.types {
			s = append(s, 0x60) // func type
			s = appendUleb128(s, uint32(len(ft.params)))
			s = append(s, ft.params...)
			s = appendUleb128(s, uint32(len(ft.results)))
			s = append(s, ft.results...)
		}
		return s
	})

	// Import section (id=2) —— wasm 规定 import 在 function 之前。
	if len(m.imports) > 0 {
		buf = appendSection(buf, secImport, func(s []byte) []byte {
			s = appendUleb128(s, uint32(len(m.imports)))
			for _, imp := range m.imports {
				s = appendUleb128(s, uint32(len(imp.module)))
				s = append(s, imp.module...)
				s = appendUleb128(s, uint32(len(imp.name)))
				s = append(s, imp.name...)
				s = append(s, 0x00) // kind = func
				s = appendUleb128(s, imp.typeIdx)
			}
			return s
		})
	}

	// Function section (id=3)
	buf = appendSection(buf, secFunc, func(s []byte) []byte {
		s = appendUleb128(s, uint32(len(m.funcs)))
		for _, idx := range m.funcs {
			s = appendUleb128(s, idx)
		}
		return s
	})

	// Memory section (id=5)
	if m.memory != nil {
		buf = appendSection(buf, secMemory, func(s []byte) []byte {
			s = appendUleb128(s, 1)            // 1 memory
			s = append(s, 0x00)                // flags (no max)
			s = appendUleb128(s, m.memory.min) // min pages
			return s
		})
	}

	// Global section (id=6) —— 常量全局（mutable=false）。
	if len(m.globals) > 0 {
		buf = appendSection(buf, secGlobal, func(s []byte) []byte {
			s = appendUleb128(s, uint32(len(m.globals)))
			for _, g := range m.globals {
				s = append(s, g.valType) // value type
				s = append(s, 0x00)      // mutable = false
				switch g.valType {
				case valI64:
					s = append(s, opI64Const)
					s = appendSleb128(s, g.i64)
				default:
					s = append(s, opI32Const)
					s = appendSleb128(s, int64(g.i32))
				}
				s = append(s, opEnd)
			}
			return s
		})
	}

	// Export section (id=7)
	if len(m.export) > 0 {
		buf = appendSection(buf, secExport, func(s []byte) []byte {
			s = appendUleb128(s, uint32(len(m.export)))
			for _, exp := range m.export {
				s = appendUleb128(s, uint32(len(exp.name)))
				s = append(s, exp.name...)
				s = append(s, exp.kind)
				s = appendUleb128(s, exp.idx)
			}
			return s
		})
	}

	// Code section (id=10)
	buf = appendSection(buf, secCode, func(s []byte) []byte {
		s = appendUleb128(s, uint32(len(m.codes)))
		for _, body := range m.codes {
			funcBody := appendUleb128(nil, 0) // 0 locals
			funcBody = append(funcBody, body...)
			s = appendUleb128(s, uint32(len(funcBody)))
			s = append(s, funcBody...)
		}
		return s
	})

	// Data section (id=11) —— 活动段、memory 0、i32.const 偏移。
	if len(m.data) > 0 {
		buf = appendSection(buf, secData, func(s []byte) []byte {
			s = appendUleb128(s, uint32(len(m.data)))
			for _, d := range m.data {
				s = append(s, 0x00) // mode: active, memory index 0
				s = append(s, opI32Const)
				s = appendSleb128(s, int64(d.offset))
				s = append(s, opEnd)
				s = appendUleb128(s, uint32(len(d.bytes)))
				s = append(s, d.bytes...)
			}
			return s
		})
	}

	return buf
}

// appendSection 包装一个 section：id + 内容长度 + 内容。
func appendSection(buf []byte, id byte, buildContent func([]byte) []byte) []byte {
	content := buildContent(nil)
	buf = append(buf, id)
	buf = appendUleb128(buf, uint32(len(content)))
	buf = append(buf, content...)
	return buf
}

// appendUleb128 追加无符号 LEB128 编码。
func appendUleb128(b []byte, v uint32) []byte {
	for {
		c := byte(v & 0x7f)
		v >>= 7
		if v == 0 {
			return append(b, c)
		}
		b = append(b, c|0x80)
	}
}

// appendSleb128 追加有符号 LEB128 编码（i32.const / i64.const 的操作数用它 ——
// 负偏移如 i32.const -1 用 uleb 编出来是 5 字节且语义错，wasm 校验器会直接拒收模块）。
func appendSleb128(b []byte, v int64) []byte {
	for {
		c := byte(v & 0x7f)
		v >>= 7
		done := (v == 0 && c&0x40 == 0) || (v == -1 && c&0x40 != 0)
		if done {
			return append(b, c)
		}
		b = append(b, c|0x80)
	}
}
