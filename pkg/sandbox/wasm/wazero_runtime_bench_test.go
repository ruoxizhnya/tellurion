package wasm

// QT-15 ⑦：WithCloseOnContextDone 的开销量化（C1 的「必须在 PoC 量化后决定」分支）。
//
// C1 冻结的判据：market scope 727 次 on_bar，开/关各跑一遍；开销 > 10% 则
// 改「仅在不可信来源策略上启用」的分级启用。结论落账在 docs/TASKS.md 的
// QT-15 施工记录（本文件只产出可复现的数字）。
//
// 对照腿经 newWazeroRuntime(ctx, max, closeOnDone) 造出：同一份代码只差
// WithCloseOnContextDone 一个开关；生产构造函数 NewWazeroRuntime 恒 true。

import (
	"context"
	"testing"
)

func benchOnBarCloseOnDone(b *testing.B, closeOnDone bool) {
	ctx := context.Background()
	r := newWazeroRuntime(ctx, DefaultMaxMemory, closeOnDone)
	defer r.Close(ctx)
	if err := r.InstantiateHostModule(ctx); err != nil {
		b.Fatal(err)
	}
	sb := NewSandbox(r, Config{})

	sess, err := sb.NewOnBarSession(ctx, wasmAccumulator())
	if err != nil {
		b.Fatal(err)
	}
	defer sess.Close()
	if err := sess.Initialize(ctx, []byte(`{"lookback":5}`)); err != nil {
		b.Fatal(err)
	}

	bc := &BarContext{Series: make([]float64, 727), State: map[string][]byte{}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for t := range bc.Series {
			bc.T = int32(t)
			if err := sess.OnBar(ctx, bc); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// BenchmarkOnBarCloseOnDone_Off 是对照组（关闭周期检查——生产禁用的形态，
// 仅基准用）。
func BenchmarkOnBarCloseOnDone_Off(b *testing.B) { benchOnBarCloseOnDone(b, false) }

// BenchmarkOnBarCloseOnDone_On 是生产形态（WithCloseOnContextDone(true)）。
func BenchmarkOnBarCloseOnDone_On(b *testing.B) { benchOnBarCloseOnDone(b, true) }

// benchBusyCloseOnDone 用「每 bar 做真实计算」的负载测同一个开关：
// 微负载（wasmAccumulator 的 on_bar 只调一次 host 函数）几乎全是调用开销，
// 周期检查的固定代价被放大到失真；真实策略每 bar 有实打实的 wasm 计算，
// 开销占比才是决策依据。
func benchBusyCloseOnDone(b *testing.B, closeOnDone bool) {
	ctx := context.Background()
	r := newWazeroRuntime(ctx, DefaultMaxMemory, closeOnDone)
	defer r.Close(ctx)
	if err := r.InstantiateHostModule(ctx); err != nil {
		b.Fatal(err)
	}
	sb := NewSandbox(r, Config{})

	sess, err := sb.NewOnBarSession(ctx, wasmBusyOnBar(10000))
	if err != nil {
		b.Fatal(err)
	}
	defer sess.Close()
	if err := sess.Initialize(ctx, nil); err != nil {
		b.Fatal(err)
	}

	bc := &BarContext{Series: make([]float64, 727), State: map[string][]byte{}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for t := range bc.Series {
			bc.T = int32(t)
			if err := sess.OnBar(ctx, bc); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// BenchmarkOnBarBusyCloseOnDone_Off 是重负载对照组。
func BenchmarkOnBarBusyCloseOnDone_Off(b *testing.B) { benchBusyCloseOnDone(b, false) }

// BenchmarkOnBarBusyCloseOnDone_On 是重负载生产形态。
func BenchmarkOnBarBusyCloseOnDone_On(b *testing.B) { benchBusyCloseOnDone(b, true) }
