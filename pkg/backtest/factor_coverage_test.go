package backtest

// 因子覆盖度闸（2026-11-11）。真栈实测的缺陷：`public.factor_cache` 有表
// 无写者，`Warm` 空结果只打 Info 就继续，`multi_factor` 读到一串「不存在」
// 后一路跑成 `invalid/zero_trades` —— 看起来像「策略不赚钱」，实际是
// 「库里没有因子」。这组用例钉住三件事：
//
//  1. 必需因子**整体**无缓存 ⇒ 拒跑（DATA_QUALITY，HTTP 409），且不产出结果行；
//  2. 缓存里有条目就放行 —— 包括 z-score 全为 0（「值为 0」不是「缺失」）；
//  3. 价量策略（不声明因子）完全不受影响，窗口长短也不参与判断。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	cdom "github.com/ruoxizhnya/tellurion-contracts/domain"
	apperrors "github.com/ruoxizhnya/tellurion-kit/errors"
	"tellurion/pkg/domain"
)

// factorDay 是测试里的统一日期，落缓存用。
var factorDay = time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)

// seedFactors 造一份指定因子的缓存种子（每因子一条 600519.SH）。
func seedFactors(eng *Engine, z float64, factors ...cdom.FactorType) {
	seed := make(map[cdom.FactorType]map[time.Time]map[string]float64, len(factors))
	for _, factor := range factors {
		seed[factor] = map[time.Time]map[string]float64{
			factorDay: {"600519.SH": z},
		}
	}
	eng.LoadFactorCache(seed)
}

// noBarsAxisProvider 有完整交易日日历、但没有任何行情。
//
// 用来把 run 推到「因子覆盖度闸」这一站：日历完整 ⇒ 过覆盖度校验；
// 行情为空 ⇒ 按需求值算不出任何截面 ⇒ 闸必须拒跑。
type noBarsAxisProvider struct {
	mockProvider
	days []time.Time
}

func (p *noBarsAxisProvider) GetTradingDays(_ context.Context, start, end time.Time) ([]time.Time, error) {
	out := make([]time.Time, 0, len(p.days))
	for _, d := range p.days {
		if !d.Before(start) && !d.After(end) {
			out = append(out, d)
		}
	}
	return out, nil
}

func (p *noBarsAxisProvider) CheckCalendarExists(ctx context.Context, start, end time.Time) (bool, error) {
	days, err := p.GetTradingDays(ctx, start, end)
	return len(days) > 0, err
}

func TestEnsureRequiredFactors_AllMissingRefuses(t *testing.T) {
	eng := newTestEngine(t)
	rc := newTestRunSpan(t, eng, domain.BacktestParams{StrategyName: "multi_factor"})

	err := rc.ensureRequiredFactors("multi_factor")
	if err == nil {
		t.Fatal("三个必需因子一条缓存都没有时必须拒跑，实际放行了")
	}

	var appErr *apperrors.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("应当是 AppError，实际 %T", err)
	}
	if appErr.Code != apperrors.ErrCodeDataQuality {
		t.Fatalf("分类应当是 DATA_QUALITY（缺的是库里的数据），实际 %s", appErr.Code)
	}
	if appErr.Operation != "RunBacktest" {
		t.Fatalf("操作名应当保留，实际 %q", appErr.Operation)
	}

	// 文案必须点名缺哪些因子 + 说清「表已退役、按需求值也没算出来」，否则
	// 运维只能看到一句「没有因子」，仍不知道是数据没同步、还是管线根本
	// 不存在、还是输入字段缺失。
	msg := appErr.Message
	for _, want := range []string{"multi_factor", "momentum", "quality", "value", "retired", "on-demand"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("错误文案缺少 %q：%s", want, msg)
		}
	}
}

// 破坏验证的反面：缓存里有条目（z=0）就放行。如果把「值为 0」误判成缺失，
// 这条会红。
func TestEnsureRequiredFactors_ZeroValuesCountAsPresent(t *testing.T) {
	eng := newTestEngine(t)
	seedFactors(eng, 0, cdom.FactorMomentum, cdom.FactorQuality, cdom.FactorValue)
	rc := newTestRunSpan(t, eng, domain.BacktestParams{StrategyName: "multi_factor"})

	if err := rc.ensureRequiredFactors("multi_factor"); err != nil {
		t.Fatalf("z-score 为 0 是合法读数，不该被当成缺失：%v", err)
	}
}

// 部分缺失允许：策略按可用因子加权，这是既有语义；只有全缺才是致命。
func TestEnsureRequiredFactors_PartialMissingPasses(t *testing.T) {
	eng := newTestEngine(t)
	seedFactors(eng, 1.2, cdom.FactorMomentum)
	rc := newTestRunSpan(t, eng, domain.BacktestParams{StrategyName: "multi_factor"})

	if err := rc.ensureRequiredFactors("multi_factor"); err != nil {
		t.Fatalf("部分因子可用时应当继续（按可用因子加权）：%v", err)
	}
}

// 价量策略不声明因子依赖 ⇒ 缓存全空也照跑，不能被这条闸误伤。
func TestEnsureRequiredFactors_PriceOnlyStrategyUnaffected(t *testing.T) {
	eng := newTestEngine(t)
	rc := newTestRunSpan(t, eng, domain.BacktestParams{StrategyName: "momentum"})

	if err := rc.ensureRequiredFactors("momentum"); err != nil {
		t.Fatalf("不依赖因子的策略不该被覆盖度闸拦住：%v", err)
	}
}

// RunBacktest 层：全空时在产出任何结果之前就返回错误。
//
// 2026-11-12：原本挂 `mockProvider{}`（交易日轴为空），重排后会在
// 「no trading days」处提前失败 —— 那样这条用例就不再验证因子闸了。
// 换成显式替身：有交易日轴、没有任何行情，让流程真正走到因子闸。
func TestRunBacktest_AllFactorsMissingReturnsErrorNoResult(t *testing.T) {
	prov := &noBarsAxisProvider{days: weekdayDays(mustDate("2023-12-01"), mustDate("2024-02-15"))}
	eng := newTestEngineWithProvider(t, prov)
	eng.admission = nil

	resp, err := eng.RunBacktest(context.Background(), BacktestRequest{
		Strategy:       "multi_factor",
		StockPool:      []string{"600519.SH"},
		StartDate:      "2024-01-02",
		EndDate:        "2024-01-10",
		InitialCapital: 1_000_000,
	})
	if err == nil {
		t.Fatal("因子全缺时必须返回错误，实际拿到了结果")
	}
	// 必须真的败在因子闸（而不是更早的「没有交易日」分支）。
	if !strings.Contains(err.Error(), "retired") {
		t.Fatalf("应当败在因子覆盖度闸，实际错误：%v", err)
	}
	// RunBacktest 的契约是「失败也回一份 failed 信封」（handler 只认 err），
	// 所以这里断言的是：信封里没有任何成交/权益行，状态是 failed ——
	// 不能出现一份「看起来跑完了」的结果。
	if resp != nil {
		if resp.Status != StatusFailed {
			t.Fatalf("拒跑时信封状态应当是 failed，实际 %q", resp.Status)
		}
		if len(resp.Trades) != 0 || len(resp.PortfolioValues) != 0 {
			t.Fatalf("拒跑时不得产出成交/权益行，实际 trades=%d equity=%d", len(resp.Trades), len(resp.PortfolioValues))
		}
	}
}
