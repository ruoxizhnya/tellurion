package backtest

// CORE-RISK-REMOTE-FALLBACK-DEAD 的护栏：engine 不许再长出「没有 RiskManager
// 就回落 HTTP risk-service」这条路。
//
// 被删的原貌：Engine 曾持有 riskServiceURL / httpClient，在 detectRegime、
// calculatePosition、checkStopLosses 三处 `rm == nil` 分支里拼
// `/detect_regime`、`/calculate_position`、`/check_stoploss` 发出去。两个生产
// 入口（cmd/analysis、cmd/worker）都无条件注入进程内 RiskManager，这条回落
// **从来没有生产调用者**，且缺省基址（http://localhost:8083）在那两个进程里
// 也没有对应的服务 —— 它是「看起来有兜底、其实一按就 404 / 连接拒绝」的死路。
// 方向是删，不是接线：接线需要有这三条端点的契约，contracts/risk 里没有，
// 而引擎内核本来就不该直连外部（BE-10）。
//
// 本文件钉两侧：
//   A. 生产源码里 `risk_service.url`、三处路径字面量、riskServiceURL /
//      httpClient 标识符的**出现次数为零** —— 用 AST 而不是文本 grep，因为
//      engine.go 的注释里正解释着这些名字，文本扫描会在正确状态下报红。
//   B. 没有 RiskManager 时三个入口**显式 fail-closed**（返回 ErrCodeInternal
//      与「no remote fallback」），而不是静默返回零值或去发 HTTP。
//
// B 是「删干净了」的行为侧证据，A 是「不许回来」的静态侧证据；缺一条，
// 回落都可能以另一种写法复活。

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	cdom "github.com/ruoxizhnya/tellurion-contracts/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tellurion/pkg/domain"
)

// forbiddenRemoteRiskNames 是「回落不许回来」的名字面。
//
// 每一条都对应被删掉的一段真实代码，不是猜测：
//   - risk_service.url —— 读配置拼基址；
//   - 三个路径字面量 —— 三处 rm == nil 分支的请求路径；
//   - riskServiceURL / httpClient —— 承载它们的两条 Engine 字段。
var forbiddenRemoteRiskNames = map[string]string{
	"risk_service.url":    "配置键：回落基址的来源",
	"/detect_regime":      "detectRegime 的回落路径",
	"/calculate_position": "calculatePosition 的回落路径",
	"/check_stoploss":     "checkStopLosses 的回落路径",
	"riskServiceURL":      "承载回落基址的 Engine 字段",
	"httpClient":          "执行回落的 HTTP 客户端字段",
}

// TestNoRemoteRiskFallbackNamesInProductionSources 是 Leg A：名字面归零。
func TestNoRemoteRiskFallbackNamesInProductionSources(t *testing.T) {
	t.Parallel()

	var hits []string
	files := 0
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files++

		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.BasicLit:
				if x.Kind != token.STRING {
					return true
				}
				lit := strings.Trim(x.Value, "`\"")
				if reason, bad := forbiddenRemoteRiskNames[lit]; bad {
					hits = append(hits, path+": 字面量 "+lit+"（"+reason+"）")
				}
			case *ast.Ident:
				if reason, bad := forbiddenRemoteRiskNames[x.Name]; bad {
					hits = append(hits, path+": 标识符 "+x.Name+"（"+reason+"）")
				}
			}
			return true
		})
		return nil
	})
	require.NoError(t, err)
	require.Greater(t, files, 0, "一个生产文件都没扫到：扫描面坏了")

	sort.Strings(hits)
	assert.Empty(t, hits,
		"pkg/backtest 生产源码里又出现了远程 risk 回落的名字（扫了 %d 个文件）：\n%s\n"+
			"方向已定：删，不是接线。没有 RiskManager 就 fail-closed —— 生产入口都无条件注入它。",
		files, strings.Join(hits, "\n"))
}

// newEngineWithoutRiskManager 构造一台**没有** RiskManager 的 Engine。
//
// 不走 NewEngine：本护栏要的是「rm == nil」这一个条件，直接摆出该状态，
// 不让配置/依赖把用例的真实意图淹掉。
func newEngineWithoutRiskManager() *Engine {
	return &Engine{logger: zerolog.Nop()}
}

// TestDetectRegimeFailsClosedWithoutRiskManager 钉 detectRegime 的 rm == nil 分支。
func TestDetectRegimeFailsClosedWithoutRiskManager(t *testing.T) {
	t.Parallel()

	// ≥20 根 bar：低于这个数函数会在查 RiskManager **之前**返回 sideways，
	// 那走的是另一条分支，钉不到回落。
	bars := make([]domain.OHLCV, 0, 25)
	base := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 25; i++ {
		bars = append(bars, domain.OHLCV{
			Symbol: "600000.SH",
			Date:   base.AddDate(0, 0, i),
			Open:   10 + float64(i)*0.1,
			High:   10.2 + float64(i)*0.1,
			Low:    9.8 + float64(i)*0.1,
			Close:  10.1 + float64(i)*0.1,
			Volume: 1_000_000,
		})
	}

	eng := newEngineWithoutRiskManager()
	regime, err := eng.detectRegime(context.Background(), map[string][]domain.OHLCV{"600000.SH": bars})

	require.Error(t, err, "没有 RiskManager 必须响亮失败，不许静默给一个 regime")
	assert.Nil(t, regime)
	assert.Contains(t, err.Error(), "no remote fallback",
		"错误必须点名「没有回落可用」，否则排障的人会以为是 risk 服务挂了")
	assert.Contains(t, err.Error(), "CORE-RISK-REMOTE-FALLBACK-DEAD")
}

// TestCalculatePositionFailsClosedWithoutRiskManager 钉 calculatePosition 的 rm == nil 分支。
func TestCalculatePositionFailsClosedWithoutRiskManager(t *testing.T) {
	t.Parallel()

	eng := newEngineWithoutRiskManager()
	pos, err := eng.calculatePosition(context.Background(), nil,
		domain.Signal{Symbol: "600000.SH"}, nil, &cdom.MarketRegime{}, 10.0)

	require.Error(t, err)
	assert.Equal(t, domain.PositionSize{}, pos, "失败时不许返回一个看似可用的仓位")
	assert.Contains(t, err.Error(), "no remote fallback")
	assert.Contains(t, err.Error(), "CORE-RISK-REMOTE-FALLBACK-DEAD")
}

// TestCheckStopLossesFailsClosedWithoutRiskManager 钉 checkStopLosses 的 rm == nil 分支。
func TestCheckStopLossesFailsClosedWithoutRiskManager(t *testing.T) {
	t.Parallel()

	logger := zerolog.Nop()
	tracker := NewTracker(1_000_000, 0.0003, 0.0001, defaultTradingConfig(), logger)
	_, err := tracker.ExecuteTrade("600000.SH", cdom.DirectionLong, 1000, 10.0,
		time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC), nil)
	require.NoError(t, err, "夹具建仓失败，护栏测不到回落分支")

	eng := newEngineWithoutRiskManager()
	events, err := eng.checkStopLosses(context.Background(), tracker, map[string]float64{"600000.SH": 10.0})

	require.Error(t, err)
	assert.Nil(t, events)
	assert.Contains(t, err.Error(), "no remote fallback")
	assert.Contains(t, err.Error(), "CORE-RISK-REMOTE-FALLBACK-DEAD")
}
