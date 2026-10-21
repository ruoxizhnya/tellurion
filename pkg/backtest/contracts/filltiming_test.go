package contracts

// QT-02 · FillTiming 契约护栏。
//
// 三条牙口：
//  1. **字面量**钉住两个口径名与 known_bias Code —— 判据与 QT-01 的
//     TestFrozenWireNames 相同：改一个字符 = 一次口径破坏，而引用常量写的
//     断言会跟着常量一起平移，看不出任何东西变了。hash 分量（fill_timing
//     进 run_spec_hash）与跨内核比对（K15）都按字符串身份对齐，名字漂了
//     两边就各说各话。
//  2. **默认值**钉死（§8.1 切换纪律：差异报告归档前不动），且默认值不写回
//     请求 —— 序列化那条腿由 runspec_test.go 的 golden JSON 代证。
//  3. 三个哨兵错误**各管一件事**且可被 errors.Is 检索：混成一个，调用方
//     就分不清「该补声明」还是「该改拼写」还是「两侧内核不一致」。

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestFillTiming_FrozenCaliberNamesAreLiterals(t *testing.T) {
	// 刻意不写 FillTimingNextOpen == FillTimingNextOpen：左边是断言，右边也
	// 得是断言里唯一的独立信息源。
	if string(FillTimingNextOpen) != "next_open" {
		t.Errorf("next_open 口径名变了: %q, want %q —— 改名 = 破坏 hash 分量与 K15 对齐", FillTimingNextOpen, "next_open")
	}
	if string(FillTimingSameClose) != "same_close" {
		t.Errorf("same_close 口径名变了: %q, want %q", FillTimingSameClose, "same_close")
	}
	if KnownBiasSameCloseFill != "same_close_fill" {
		t.Errorf("known_bias Code 变了: %q, want %q —— 它是稳定标识，自动化层按它判断", KnownBiasSameCloseFill, "same_close_fill")
	}
}

func TestFillTiming_DefaultIsSameCloseUntilReportSwitch(t *testing.T) {
	// QT-02 ④：先出报告再切默认。这条断言是那条纪律的机械形态 ——
	// 改默认值必须同时改这里（= 一次显式的基准变更，带报告依据）。
	if string(DefaultFillTiming) != "same_close" {
		t.Errorf("DefaultFillTiming = %q, want %q —— 切换默认前必须先看 docs/test-cases/QT02_FILL_TIMING_REPORT.md 并同步 SPEC",
			DefaultFillTiming, "same_close")
	}
}

func TestFillTiming_ResolveIsFailClosedNotDefaulting(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    FillTiming
		wantErr error
	}{
		{"next_open 原样透传", "next_open", FillTimingNextOpen, nil},
		{"same_close 原样透传", "same_close", FillTimingSameClose, nil},
		{"空声明是缺失不是默认", "", "", ErrFillTimingMissing},
		{"连字符拼法不认识", "next-open", "", ErrFillTimingUnknown},
		{"大小写不是身份", "Next_Open", "", ErrFillTimingUnknown},
		{"裸 open 不是口径", "open", "", ErrFillTimingUnknown},
		{"旧行为的别名不豁免", "close", "", ErrFillTimingUnknown},
		{"带空格是脏数据", " same_close", "", ErrFillTimingUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveFillTiming(tc.raw)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("ResolveFillTiming(%q) 错误不可检索: got %v, want errors.Is %v", tc.raw, err, tc.wantErr)
				}
				if got.Valid() {
					t.Errorf("被拒的取值竟自称有效: %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveFillTiming(%q) 不该失败: %v", tc.raw, err)
			}
			if got != tc.want {
				t.Errorf("ResolveFillTiming(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestFillTiming_ResolveNeverSilentlyDefaults(t *testing.T) {
	// 「缺失就回落默认」是 §8.1 明令禁止的形状：那会让「没声明」和
	// 「声明了 same_close」在留痕里长得一模一样。
	_, err := ResolveFillTiming("")
	if err == nil {
		t.Fatal("空声明被接受了 —— 引擎从此可以猜口径")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("错误文本没点明「必填」，调用方无从知道该补什么: %v", err)
	}
}

func TestFillTiming_EffectiveFillsDefaultWithoutMutatingRequest(t *testing.T) {
	req := BacktestRequest{Strategy: "momentum"}
	if got := req.EffectiveFillTiming(); got != FillTimingSameClose {
		t.Errorf("未声明时生效口径 = %q, want %q", got, FillTimingSameClose)
	}
	if req.FillTiming != "" {
		t.Errorf("EffectiveFillTiming 把默认值回填进了请求字段 (%q) —— 存量请求序列化会变", req.FillTiming)
	}

	// 显式声明的未知值必须**原样透传**给拒绝它的那一层：在这里就吞成默认值，
	// 调用方会以为 next-open 生效了。
	unknown := BacktestRequest{FillTiming: "next-open"}
	if got := unknown.EffectiveFillTiming(); got != FillTiming("next-open") {
		t.Errorf("未声明之外的值被改写了: got %q, want %q", got, "next-open")
	}
	if got := unknown.EffectiveFillTiming().Valid(); got {
		t.Error("next-open 竟自称有效口径")
	}
}

func TestFillTiming_DeclaredValueSerializes(t *testing.T) {
	// 新字段可寻址（跑一次 next_open 的请求要能被原样存进 job 再放出来），
	// 而空值不进 JSON 由 runspec_test.go 的 golden JSON 守。
	raw, err := json.Marshal(BacktestRequest{FillTiming: "next_open"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"fill_timing":"next_open"`) {
		t.Errorf("声明的口径没有出现在序列化里: %s", raw)
	}

	var round BacktestRequest
	if err := json.Unmarshal(raw, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if round.EffectiveFillTiming() != FillTimingNextOpen {
		t.Errorf("往返后口径漂移: %q", round.EffectiveFillTiming())
	}
}

func TestFillTiming_KnownBiasesMarksOnlySameClose(t *testing.T) {
	// §8.1 兼容行：same_close 保留可以，但结果必须自己说清带了哪个已知偏差。
	// next_open 不配任何偏差标识 —— 它的名字已经在 fill_timing 字段里。
	if got := FillTimingSameClose.KnownBiases(); len(got) != 1 || got[0] != "same_close_fill" {
		t.Errorf("same_close 的 known_bias = %v, want [same_close_fill]", got)
	}
	if got := FillTimingNextOpen.KnownBiases(); len(got) != 0 {
		t.Errorf("next_open 不该带已知偏差标识, got %v", got)
	}
	// 未知/空口径也不许编出一个偏差名来：没有口径 = 没有结论。
	if got := FillTiming("").KnownBiases(); len(got) != 0 {
		t.Errorf("空口径竟产出了偏差标识: %v", got)
	}
}

func TestRunSpec_ValidateFillTimingRejectsUnknownDeclaresEmptyOK(t *testing.T) {
	base := func(ft string) BacktestRequest {
		return BacktestRequest{
			Strategy:  "momentum",
			StartDate: "2024-01-01",
			EndDate:   "2024-06-30",
			StockPool: []string{"600000.SH"},

			FillTiming: ft,
		}
	}

	// 空值在**门**上放行：默认语义归 EffectiveFillTiming，必填的执行点在引擎
	// 日循环入口（那里没走过解析的 params 才会被拒）。
	if err := base("").ValidateRunSpec(); err != nil {
		t.Errorf("未声明口径的请求被拒 (%v) —— 存量请求必须继续可用", err)
	}
	for _, ok := range []string{"next_open", "same_close"} {
		if err := base(ok).ValidateRunSpec(); err != nil {
			t.Errorf("口径 %q 不该被拒: %v", ok, err)
		}
	}

	for _, bad := range []string{"next-open", "NEXT_OPEN", "close", "nope"} {
		err := base(bad).ValidateRunSpec()
		if err == nil {
			t.Fatalf("未声明之外的野口径 %q 被放行 —— 静默按默认成交，调用方以为 next_open 生效了", bad)
		}
		if !errors.Is(err, ErrFillTimingUnknown) {
			t.Errorf("口径 %q 的拒绝错误不可检索: %v", bad, err)
		}
		if !strings.Contains(err.Error(), bad) {
			t.Errorf("错误文本没点名被拒的取值 %q: %v", bad, err)
		}
	}
}

func TestFillTiming_MismatchSentinelIsDistinctFromMissing(t *testing.T) {
	// K15 的前置门用 Mismatch：「两侧口径不同」与「一侧根本没声明」是两种
	// 修法，混成一个错误会让排障者去补错东西。
	if errors.Is(ErrFillTimingMismatch, ErrFillTimingMissing) ||
		errors.Is(ErrFillTimingMissing, ErrFillTimingMismatch) {
		t.Fatal("Mismatch 与 Missing 互相可检索 —— 两个哨兵被合并成了一个语义")
	}
	if errors.Is(ErrFillTimingUnknown, ErrFillTimingMissing) {
		t.Fatal("Unknown 与 Missing 互相可检索 —— 拼错与没给是同一条错误")
	}
}

func TestCompareKernelCalibers(t *testing.T) {
	same := KernelCaliber{Kernel: "go/wrReplay", FillTiming: "same_close"}
	other := KernelCaliber{Kernel: "nautilus", FillTiming: "same_close"}

	t.Run("一致时放行", func(t *testing.T) {
		if err := CompareKernelCalibers(same, other); err != nil {
			t.Fatalf("同口径被判成不符: %v", err)
		}
	})

	t.Run("不一致时报文点名两侧内核与两个口径", func(t *testing.T) {
		err := CompareKernelCalibers(same, KernelCaliber{Kernel: "nautilus", FillTiming: "next_open"})
		if !errors.Is(err, ErrFillTimingMismatch) {
			t.Fatalf("错误不可检索: %v", err)
		}
		for _, want := range []string{"go/wrReplay", "nautilus", "same_close", "next_open"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("报文缺 %q，读者不知道该改哪一侧: %v", want, err)
			}
		}
	})

	// 空值 / 野值不能被判成「与某个已知口径一致」，也不能被折成默认值：
	// 未知就是未知，报出来的是 Missing / Unknown 而不是 Mismatch。
	t.Run("未声明的一侧报 Missing 不是 Mismatch", func(t *testing.T) {
		err := CompareKernelCalibers(same, KernelCaliber{Kernel: "nautilus", FillTiming: ""})
		if !errors.Is(err, ErrFillTimingMissing) {
			t.Fatalf("got %v, want ErrFillTimingMissing", err)
		}
		if errors.Is(err, ErrFillTimingMismatch) {
			t.Errorf("缺声明被报成口径不符 —— 排障者会去改错的那一侧: %v", err)
		}
		if !strings.Contains(err.Error(), "nautilus") {
			t.Errorf("报文没点名是右侧内核缺声明: %v", err)
		}
	})

	t.Run("野值的一侧报 Unknown 不是 Mismatch", func(t *testing.T) {
		err := CompareKernelCalibers(same, KernelCaliber{Kernel: "nautilus", FillTiming: "close"})
		if !errors.Is(err, ErrFillTimingUnknown) {
			t.Fatalf("got %v, want ErrFillTimingUnknown", err)
		}
	})
}
