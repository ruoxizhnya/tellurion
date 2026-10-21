package contracts

// QT-03 · 留痕 hash 的唯一编码器。
//
// 为什么要有这一个文件而不是各处 `json.Marshal` + `sha256`：
//   - **编码不确定性**。`encoding/json` 的 map 键会排序，但 struct 字段按声明序，
//     且浮点走它自己的最短表示。两个模块各自 marshal 同一个 spec，字节可能不同，
//     于是「同 hash 同结果」这条承诺从根上不成立。
//   - **域混淆**。run_spec_hash 与 dataset_snapshot_hash 若都是裸 sha256，
//     一个恰好等于另一个输入串的结果可以互相冒充。每个 hash 前缀自己的域标签。
//   - **静默跳过比报错更糟**。参数里出现一个编不出来的类型（func、chan、
//     自定义 struct）如果被 skip 掉，改了它就 hash 不变 —— 这正是 N6 要防的病。
//     所以这里 fail-closed：编不出来就报错，不产出 hash。
//
// 纪律：**只增不改**。要改任一分量的编码方式，等于让所有历史 run_spec_hash 失效，
// 那必须升域标签的版本（v1 → v2），不能就地改（同 abi 的增量命名哲学）。

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"
)

// Canonical 是一个「按声明顺序写入、自定界」的字节流累加器。
//
// 用法（顺序就是契约，字段不许漏）：
//
//	c := NewCanonical(DomainRunSpecHash)
//	c.Str("start", "2024-01-02").Float("initial_capital", 1e6).SymbolSet("stock_pool", pool)
//	hex, err := c.Hex()
//
// 出错时（只可能是 Value 遇到不可编码的类型）后续写入被忽略，Hex() 返回该错误 ——
// 这样调用点可以链式写，最后统一判错，不会写出半截字节流还拿到 hash。
type Canonical struct {
	buf []byte
	err error
}

// 域标签：每类 hash 各自独立，带版本号。
const (
	DomainRunSpecHash         = "tellurion.run_spec_hash.v1"
	DomainDatasetSnapshotHash = "tellurion.dataset_snapshot_hash.v1"
	DomainUniverseHash        = "tellurion.universe_hash.v1"
	DomainCalendarHash        = "tellurion.calendar_hash.v1"
	DomainAnchorDigestsHash   = "tellurion.adjustment_anchor_digests.v1"
)

// NewCanonical 开启一段新的编码，先写入域标签。
func NewCanonical(domain string) *Canonical {
	c := &Canonical{}
	c.bytes([]byte(domain))
	return c
}

func (c *Canonical) fail(err error) {
	if c.err == nil {
		c.err = err
	}
}

// bytes 写入一段长度前缀 + 内容（自定界，内容里出现任何字节都不会与帧结构混淆）。
func (c *Canonical) bytes(b []byte) {
	if c.err != nil {
		return
	}
	var hdr [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(hdr[:], uint64(len(b)))
	c.buf = append(c.buf, hdr[:n]...)
	c.buf = append(c.buf, b...)
}

// keyed 写入一个「字段名 + 值」对。字段名也参与编码：漏写一个字段与写空值
// 必须是两件事，所以调用点必须显式把每个字段都写一遍。
func (c *Canonical) keyed(key, encodedValue string) {
	c.bytes([]byte(key))
	c.bytes([]byte(encodedValue))
}

// Str 写入一个字符串分量。
func (c *Canonical) Str(key, value string) *Canonical {
	c.keyed(key, "s;"+value)
	return c
}

// Int 写入一个整数分量（seed、lot_size 这类）。
//
// ⚠️ 数值的 kind 标签是**统一的 "n;"**，int / uint / float 共用：留痕 hash 要判的是
// 「这次跑的输入是什么数」，不是「那个数在 Go 里是哪个类型」。参数从 JSON 来时是
// float64、从 Go 字面量来时是 int，`lookback=20` 与 `lookback=20.0` 是同一个输入，
// 必须得到同一个 hash —— 否则「同参复现」会因为一次反序列化而假失败。
// 小数不受影响：20.5 与 20 的表示本就不同。
func (c *Canonical) Int(key string, value int64) *Canonical {
	c.keyed(key, "n;"+strconv.FormatInt(value, 10))
	return c
}

// Float 写入一个浮点分量。
//
// 表示用 Go 的**最短往返**格式（'g' / 精度 -1）：0.1 编出来是 "0.1" 而不是
// "0.10000000000000000555"，同一 float64 值在任何平台上字节相同。
// 两条刻意规则：NaN/±Inf 报错（它们不是数，是「出错了」的信号，不该被哈希掉）；
// -0 归一为 0（-0 与 0 在回测里是同一个输入，不该产生两个 hash）。
func (c *Canonical) Float(key string, value float64) *Canonical {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		c.fail(fmt.Errorf("%w: field %q is %v; non-finite numbers cannot enter a provenance hash",
			ErrCanonicalUnsupported, key, value))
		return c
	}
	if value == 0 {
		value = 0 // 抹掉 -0
	}
	c.keyed(key, "n;"+strconv.FormatFloat(value, 'g', -1, 64))
	return c
}

// Time 写入一个日期分量（只取 UTC 日历日）。
//
// 刻意不带时刻：回测的区间是**交易日**，把时区/时分秒混进 hash 会让「同一天」
// 因为服务器时区不同而算出两个 run_spec_hash。零值时间编成空串（表示未给）。
func (c *Canonical) Time(key string, value time.Time) *Canonical {
	if value.IsZero() {
		c.keyed(key, "t;")
		return c
	}
	c.keyed(key, "t;"+value.UTC().Format("2006-01-02"))
	return c
}

// Sequence 写入一个**保序**列表分量（日历轴：日期的先后就是语义本身）。
func (c *Canonical) Sequence(key string, values []string) *Canonical {
	var b []byte
	for _, v := range values {
		b = binary.AppendUvarint(b, uint64(len(v)))
		b = append(b, v...)
	}
	c.keyed(key, "q;"+string(b))
	return c
}

// SymbolSet 写入一个**集合语义**的分量（排序 + 去重）。
//
// 票池是集合不是序列：`["A","B"]` 与 `["B","A"]` 是同一个票池，必须得到同一个 hash
// （否则「同参复现」会因为一次 map 遍历顺序不同而失败）。
// 但**去重不是幂等噪声**：`["A","A"]` 与 `["A"]` 也同一个 hash —— 多写一次同一个
// 标的从来不构成第二个输入。
func (c *Canonical) SymbolSet(key string, values []string) *Canonical {
	dedup := make(map[string]struct{}, len(values))
	for _, v := range values {
		dedup[v] = struct{}{}
	}
	sorted := make([]string, 0, len(dedup))
	for v := range dedup {
		sorted = append(sorted, v)
	}
	sort.Strings(sorted)
	c.Sequence(key, sorted)
	return c
}

// Value 写入一个任意但必须可确定性编码的分量（策略生效参数走这里）。
//
// 支持：nil / bool / string / 全部整数与浮点 kind / []any / []string /
// map[string]any / map[string]string / map[string]float64。其它一律报错，
// **包括 `json.Number`**（它是有名字的类型，落不进 `case string`）—— 用
// `json.Decoder.UseNumber()` 的调用方要先把它转成 float64 再传进来。
// 宁可拒绝并让人把参数摊平，也不要「跳过一个字段」这种让 hash 失去覆盖力的
// 失败模式。
func (c *Canonical) Value(key string, value any) *Canonical {
	encoded, err := canonicalValue(value)
	if err != nil {
		c.fail(fmt.Errorf("%w: field %q: %v", ErrCanonicalUnsupported, key, err))
		return c
	}
	c.keyed(key, encoded)
	return c
}

// canonicalValue 把任意受支持的值编成自定界字符串。
func canonicalValue(v any) (string, error) {
	switch t := v.(type) {
	case nil:
		return "z;", nil
	case bool:
		if t {
			return "b;1", nil
		}
		return "b;0", nil
	case string:
		return "s;" + t, nil
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return "", fmt.Errorf("non-finite float %v", t)
		}
		if t == 0 {
			t = 0
		}
		return "n;" + strconv.FormatFloat(t, 'g', -1, 64), nil
	case float32:
		// 升到 float64 再编：整值（20）与 `Int` 同一个身份，但 0.1 这类 float32
		// 存不下的小数会带上它自己的精度痕迹（"0.10000000149011612"），与
		// float64(0.1) 得到不同 hash —— 精度是输入的一部分，这里不抹平。
		return canonicalValue(float64(t))
	case int:
		return "n;" + strconv.FormatInt(int64(t), 10), nil
	case int64:
		return "n;" + strconv.FormatInt(t, 10), nil
	case int32:
		return "n;" + strconv.FormatInt(int64(t), 10), nil
	case int16:
		return "n;" + strconv.FormatInt(int64(t), 10), nil
	case int8:
		return "n;" + strconv.FormatInt(int64(t), 10), nil
	case uint:
		return "n;" + strconv.FormatUint(uint64(t), 10), nil
	case uint64:
		return "n;" + strconv.FormatUint(t, 10), nil
	case uint32:
		return "n;" + strconv.FormatUint(uint64(t), 10), nil
	case uint16:
		return "n;" + strconv.FormatUint(uint64(t), 10), nil
	case uint8:
		return "n;" + strconv.FormatUint(uint64(t), 10), nil
	case []string:
		out := "l;" + strconv.Itoa(len(t)) + ";"
		for _, item := range t {
			enc, err := canonicalValue(item)
			if err != nil {
				return "", err
			}
			out += enc
		}
		return out, nil
	case []any:
		out := "l;" + strconv.Itoa(len(t)) + ";"
		for i, item := range t {
			enc, err := canonicalValue(item)
			if err != nil {
				return "", fmt.Errorf("index %d: %v", i, err)
			}
			out += enc
		}
		return out, nil
	case map[string]any:
		return canonicalMap(t)
	case map[string]string:
		keys := sortedKeysAny(t)
		out := "m;" + strconv.Itoa(len(keys)) + ";"
		for _, k := range keys {
			out += "s;" + k
			out += "s;" + t[k]
		}
		return out, nil
	case map[string]float64:
		// 费率块：值域就是浮点，单独支持免得调用方为了过类型检查去摊成 map[string]any。
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := "m;" + strconv.Itoa(len(keys)) + ";"
		for _, k := range keys {
			enc, err := canonicalValue(t[k])
			if err != nil {
				return "", fmt.Errorf("key %q: %v", k, err)
			}
			out += "s;" + k
			out += enc
		}
		return out, nil
	default:
		return "", fmt.Errorf("type %T is not canonically encodable (flatten it into supported kinds)", v)
	}
}

func canonicalMap(m map[string]any) (string, error) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := "m;" + strconv.Itoa(len(keys)) + ";"
	for _, k := range keys {
		enc, err := canonicalValue(m[k])
		if err != nil {
			return "", fmt.Errorf("key %q: %v", k, err)
		}
		out += "s;" + k
		out += enc
	}
	return out, nil
}

func sortedKeysAny(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Hex 产出这段编码的 sha256（hex）。编不出来时返回错误而不是半个 hash。
func (c *Canonical) Hex() (string, error) {
	if c.err != nil {
		return "", c.err
	}
	return sha256Hex(c.buf), nil
}

// MustHex 只在「编码必然成功」的调用点使用（分量全是受支持类型的常量输入）。
//
// 用它要有理由：panic 在库侧是禁手（AGENTS §6「错误处理：返回 error，生产代码中
// 绝不 panic」），所以这里只给已经由类型保证不会失败的场景。
func (c *Canonical) MustHex() string {
	hex, err := c.Hex()
	if err != nil {
		panic(fmt.Sprintf("contracts: canonical hash encoding failed: %v", err))
	}
	return hex
}

// ErrCanonicalUnsupported 是「分量编不进 hash」。
var ErrCanonicalUnsupported = fmt.Errorf("provenance: value is not canonically encodable")
