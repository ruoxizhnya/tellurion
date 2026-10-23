package conformance

// core 侧的契约一致性腿（QT-09 ③）：与契约仓、agent 读**同一份** cases.json，
// 对 core 自己的实现实算比对 —— 任一侧语义漂移即红。
//
// 语料不从本仓复制、也不从契约仓 import 加载器：两侧刻意各自读取同一份 JSON，
// 这样「解析口径」本身也在比对范围内（共享加载器会把解析漂移一起藏掉）。
// 语料目录的定位顺序见 locateCorpus；全都找不到就 t.Fatalf，绝不 t.Skip ——
// 环境问题不能伪装成「本侧不适用」。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/ruoxizhnya/tellurion-contracts/domain"
	contracts "github.com/ruoxizhnya/tellurion-contracts/engine"
	"tellurion/pkg/backtest/tracker"
)

// contractsModule / corpusDir / side 是本侧在语料覆盖矩阵里的身份。
const (
	contractsModule = "github.com/ruoxizhnya/tellurion-contracts"
	corpusDir       = "conformance"
	side            = "core"
)

// knownKinds 是语料的全部 kind（三仓共用）。
//
// 用于区分两类红：语料写了一个**没人认识**的 kind，还是语料把一个**别的侧**
// 的 kind 错标成了 core。两种都必须当场红，但报错要说清楚是哪一种。
var knownKinds = map[string]bool{
	"stamp_tax_rate_for":    true,
	"fee_defaults":          true,
	"daily_visible_at":      true,
	"intraday_visible_at":   true,
	"visible_by":            true,
	"time_semantics":        true,
	"run_spec_hash":         true,
	"trade_fees":            true,
	"fee_defaults_via_yaml": true,
}

// TestContractConformance 是 core 侧入口。
func TestContractConformance(t *testing.T) {
	dir := locateCorpus(t)
	cases, err := loadCases(filepath.Join(dir, corpusDir))
	if err != nil {
		t.Fatalf("加载契约语料失败: %v", err)
	}
	t.Logf("语料目录: %s（%d 条）", dir, len(cases))

	handlers := map[string]func(*testing.T, caseSpec){
		"run_spec_hash": checkRunSpecHash,
		"trade_fees":    checkTradeFees,
	}

	ran, skipped := 0, 0
	for _, c := range cases {
		c := c
		if !declaresSide(c.Sides, side) {
			skipped++
			t.Logf("跳过 %s（sides=%v，本侧不跑）", c.ID, c.Sides)
			continue
		}
		handler, ok := handlers[c.Kind]
		if !ok {
			if knownKinds[c.Kind] {
				t.Fatalf("用例 %q：kind %q 不是 core 侧的实现，但 sides 声明了 core（覆盖矩阵自相矛盾）", c.ID, c.Kind)
			}
			t.Fatalf("用例 %q：未知 kind %q，但 sides 声明了 core（未知 kind 不许静默跳过）", c.ID, c.Kind)
		}
		ran++
		t.Run(c.ID, func(t *testing.T) { handler(t, c) })
	}

	t.Logf("core 侧：执行 %d 条 / 跳过 %d 条 / 语料共 %d 条", ran, skipped, len(cases))
	if ran == 0 {
		t.Fatalf("core 侧一条用例都没执行 —— 语料与 sides 声明脱节")
	}
}

// locateCorpus 依次尝试：
//  1. `go list -m -f {{.Dir}}` 指到的 module 目录（含 conformance/cases.json 才算数）；
//  2. runtime.GOROOT()/bin/go 再试一次（PATH 没有 go 的环境）；
//  3. 从当前工作目录逐级向上找同级的 tellurion-contracts 工作树。
//
// 全部失败 → t.Fatalf。不用 t.Skip：语料不可达是「本侧没有真正执行」，
// 不是「本侧不适用」，静默跳过会让这条腿在 CI 里永远假绿。
func locateCorpus(t *testing.T) string {
	t.Helper()

	var candidates []string
	tryGo := func(goBin string) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, goBin, "list", "-m", "-f", "{{.Dir}}", contractsModule).Output()
		if err != nil {
			t.Logf("go list -m（%s）失败: %v", goBin, err)
			return
		}
		if dir := string(bytes.TrimSpace(out)); dir != "" {
			candidates = append(candidates, dir)
		}
	}

	if bin, err := exec.LookPath("go"); err == nil {
		tryGo(bin)
	}
	if root := runtime.GOROOT(); root != "" {
		for _, name := range []string{"go", "go.exe"} {
			bin := filepath.Join(root, "bin", name)
			if info, err := os.Stat(bin); err == nil && !info.IsDir() {
				tryGo(bin)
				break
			}
		}
	}
	if wd, err := os.Getwd(); err == nil {
		for dir := wd; ; {
			candidates = append(candidates, filepath.Join(dir, "tellurion-contracts"))
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	for _, c := range candidates {
		if info, err := os.Stat(filepath.Join(c, corpusDir, "cases.json")); err == nil && !info.IsDir() {
			return c
		}
	}
	t.Fatalf("找不到契约仓语料 %s/%s/cases.json（已尝试: %v）—— 语料不可达不是「本侧不适用」",
		contractsModule, corpusDir, candidates)
	return ""
}

// ─── 各 kind 的断言 ──────────────────────────────────────────

// runSpecCase 是语料里 run_spec_hash 的入参（与 contracts.RunSpecInput 一一对应）。
type runSpecCase struct {
	Start                   string             `json:"start"`
	End                     string             `json:"end"`
	InitialCapital          float64            `json:"initial_capital"`
	RiskFreeRate            float64            `json:"risk_free_rate"`
	StrategyArtifactHash    string             `json:"strategy_artifact_hash"`
	StrategyVersionHash     string             `json:"strategy_version_hash"`
	StrategyParams          map[string]any     `json:"strategy_params"`
	StockPool               []string           `json:"stock_pool"`
	IndexCode               string             `json:"index_code"`
	Universe                []string           `json:"universe"`
	Frequency               string             `json:"frequency"`
	FillTiming              string             `json:"fill_timing"`
	Kernel                  string             `json:"kernel"`
	EngineVersion           string             `json:"engine_version"`
	Fees                    map[string]float64 `json:"fees"`
	Slippage                float64            `json:"slippage"`
	CostModel               string             `json:"cost_model"`
	LotSize                 int64              `json:"lot_size"`
	TPlus                   string             `json:"t_plus"`
	PriceLimitRule          string             `json:"price_limit_rule"`
	RiskRulesHash           string             `json:"risk_rules_hash"`
	DatasetSnapshotHash     string             `json:"dataset_snapshot_hash"`
	AdjustmentAnchorDigests []string           `json:"adjustment_anchor_digests"`
	Seed                    int64              `json:"seed"`
}

func checkRunSpecHash(t *testing.T, c caseSpec) {
	t.Helper()
	var in runSpecCase
	var want struct {
		Hash string `json:"hash"`
	}
	decodePair(t, c, &in, &want)

	spec := contracts.RunSpecInput{
		Start:                   parseDateArg(t, c, "start", in.Start),
		End:                     parseDateArg(t, c, "end", in.End),
		InitialCapital:          in.InitialCapital,
		RiskFreeRate:            in.RiskFreeRate,
		StrategyArtifactHash:    in.StrategyArtifactHash,
		StrategyVersionHash:     in.StrategyVersionHash,
		StrategyParamsEffective: in.StrategyParams,
		StockPool:               in.StockPool,
		IndexCode:               in.IndexCode,
		Universe:                in.Universe,
		Frequency:               in.Frequency,
		FillTiming:              contracts.FillTiming(in.FillTiming),
		Kernel:                  in.Kernel,
		EngineVersion:           in.EngineVersion,
		Fees:                    in.Fees,
		Slippage:                in.Slippage,
		CostModel:               in.CostModel,
		LotSize:                 in.LotSize,
		TPlus:                   in.TPlus,
		PriceLimitRule:          in.PriceLimitRule,
		RiskRulesHash:           in.RiskRulesHash,
		DatasetSnapshotHash:     in.DatasetSnapshotHash,
		AdjustmentAnchorDigests: in.AdjustmentAnchorDigests,
		Seed:                    in.Seed,
	}

	got, err := spec.Hash()
	if err != nil {
		t.Fatalf("用例 %q: RunSpecInput.Hash 报错: %v", c.ID, err)
	}
	if got != want.Hash {
		t.Errorf("用例 %q: run_spec_hash 期望 %s，实际 %s（core 与契约仓的编码口径漂移）", c.ID, want.Hash, got)
	}
}

// tradeFeesCase 是语料里 trade_fees 的入参：真实走 tracker.NewTracker + ExecuteTrade。
type tradeFeesCase struct {
	InitialCapital     float64 `json:"initial_capital"`
	CommissionRate     float64 `json:"commission_rate"`
	SlippageRate       float64 `json:"slippage_rate"`
	StampTaxRate       float64 `json:"stamp_tax_rate"`
	StampTaxRateBefore float64 `json:"stamp_tax_rate_before"`
	TransferFeeRate    float64 `json:"transfer_fee_rate"`
	MinCommission      float64 `json:"min_commission"`
	Symbol             string  `json:"symbol"`
	Direction          string  `json:"direction"`
	Quantity           float64 `json:"quantity"`
	Price              float64 `json:"price"`
	Timestamp          string  `json:"timestamp"`
}

func checkTradeFees(t *testing.T, c caseSpec) {
	t.Helper()
	var in tradeFeesCase
	var want struct {
		Commission  float64 `json:"commission"`
		TransferFee float64 `json:"transfer_fee"`
		StampTax    float64 `json:"stamp_tax"`
	}
	decodePair(t, c, &in, &want)

	trading := contracts.TradingConfig{
		StampTaxRate:       in.StampTaxRate,
		StampTaxRateBefore: in.StampTaxRateBefore,
		MinCommission:      in.MinCommission,
		TransferFeeRate:    in.TransferFeeRate,
	}
	tr := tracker.NewTracker(in.InitialCapital, in.CommissionRate, in.SlippageRate, trading, zerolog.Nop())
	trade, err := tr.ExecuteTrade(in.Symbol, domain.Direction(in.Direction), in.Quantity, in.Price,
		parseTimeArg(t, c, "timestamp", in.Timestamp), nil)
	if err != nil {
		t.Fatalf("用例 %q: ExecuteTrade 报错: %v", c.ID, err)
	}

	for _, f := range []struct {
		name     string
		got      float64
		expected float64
	}{
		{"commission", trade.Commission, want.Commission},
		{"transfer_fee", trade.TransferFee, want.TransferFee},
		{"stamp_tax", trade.StampTax, want.StampTax},
	} {
		if !almostEqual(f.got, f.expected) {
			t.Errorf("用例 %q: %s 期望 %v，实际 %v（成交额 %.2f @ %v，方向 %s）",
				c.ID, f.name, f.expected, f.got, in.Quantity*in.Price, trade.Price, in.Direction)
		}
	}
}

// ─── 语料读取与公共工具 ──────────────────────────────────────

type caseSpec struct {
	ID     string          `json:"id"`
	Kind   string          `json:"kind"`
	Sides  []string        `json:"sides"`
	Input  json.RawMessage `json:"input"`
	Expect json.RawMessage `json:"expect"`
}

// loadCases 读取并校验语料；任何缺陷都返回错误（空语料/重复 id/未知侧…）。
func loadCases(dir string) ([]caseSpec, error) {
	path := filepath.Join(dir, "cases.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取语料 %s: %w", path, err)
	}
	var cases []caseSpec
	if err := json.Unmarshal(raw, &cases); err != nil {
		return nil, fmt.Errorf("解析语料 %s: %w", path, err)
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("语料 %s 为空（空语料 = 假绿）", path)
	}
	seen := make(map[string]bool, len(cases))
	for i, c := range cases {
		if c.ID == "" {
			return nil, fmt.Errorf("语料 %s 第 %d 条缺少 id", path, i)
		}
		if seen[c.ID] {
			return nil, fmt.Errorf("语料 %s 里 id %q 重复", path, c.ID)
		}
		seen[c.ID] = true
		if c.Kind == "" {
			return nil, fmt.Errorf("用例 %q 缺少 kind", c.ID)
		}
		if len(c.Sides) == 0 {
			return nil, fmt.Errorf("用例 %q 的 sides 为空（不再明侧的用例永远不会被执行）", c.ID)
		}
		for _, s := range c.Sides {
			if s != "contracts" && s != "core" && s != "agent" {
				return nil, fmt.Errorf("用例 %q 声明了未知侧 %q（已知：agent/contracts/core）", c.ID, s)
			}
		}
		if len(c.Input) == 0 {
			return nil, fmt.Errorf("用例 %q 缺少 input", c.ID)
		}
		if len(c.Expect) == 0 {
			return nil, fmt.Errorf("用例 %q 缺少 expect", c.ID)
		}
	}
	return cases, nil
}

func declaresSide(sides []string, want string) bool {
	for _, s := range sides {
		if s == want {
			return true
		}
	}
	return false
}

// decodePair 严格解析 input/expect：未知字段报错，避免语料键名打错静默变零值。
func decodePair(t *testing.T, c caseSpec, input, expect any) {
	t.Helper()
	if err := decodeStrict(c.Input, input); err != nil {
		t.Fatalf("用例 %q 的 input 解析失败: %v", c.ID, err)
	}
	if err := decodeStrict(c.Expect, expect); err != nil {
		t.Fatalf("用例 %q 的 expect 解析失败: %v", c.ID, err)
	}
}

func decodeStrict(raw json.RawMessage, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

func parseTimeArg(t *testing.T, c caseSpec, field, value string) time.Time {
	t.Helper()
	if value == "" {
		return time.Time{}
	}
	ts, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("用例 %q 的 %s=%q 不是 RFC3339: %v", c.ID, field, value, err)
	}
	return ts
}

func parseDateArg(t *testing.T, c caseSpec, field, value string) time.Time {
	t.Helper()
	ts, err := time.Parse("2006-01-02", value)
	if err != nil {
		t.Fatalf("用例 %q 的 %s=%q 不是 YYYY-MM-DD: %v", c.ID, field, value, err)
	}
	return ts
}

func almostEqual(got, want float64) bool {
	return math.Abs(got-want) <= 1e-12
}
