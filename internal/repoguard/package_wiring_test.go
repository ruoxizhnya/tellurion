package repoguard

// AUD-45 / AUD-46 (ODR-065) structural guard: **every package under pkg/ and
// internal/ must have at least one importer inside this module**, unless it is
// named in unwiredPackages with a reason; and **a package named in
// retiredPackages must not exist at all**.
//
// Why this exists. AUD-43 was registered as "pkg/testutil has zero importers".
// That was true — and true of **13** packages, not one. The repo deliberately
// keeps capability packages that are written and tested *before* they are
// wired (P2-5 「删的是服务不是能力」, P2-9wire 「零件先写、后接线」). So
// "zero importers" is not by itself a defect. But it is a state that should be
// **visible and deliberate** rather than discovered by accident, which is
// exactly what happened here.
//
// AUD-46 then split the 13: pkg/metrics turned out not to be "awaiting wiring"
// but a **competing implementation** (ADR-017 §1's four core metrics live in
// pkg/observability; wiring both would double-register a metric and panic), so
// it was retired — and retirement needs the second half, the assertion that it
// does not come back.
//
// The importer set is derived from the source (go/ast over every .go file), not
// from a hand-written list — a hand-written list goes stale the moment someone
// adds a package, and then the guard is green while the repo is not
// (PITFALLS §41). The allowlist therefore only has to name the exceptions, and
// it is self-policing: an entry whose package has since gained an importer is
// reported as stale and must be deleted.
//
// Boundary of this check:
//   - Only pkg/** and internal/** are inspected. cmd/** are entry points and
//     have no importers by design.
//   - A directory with no non-test .go file is skipped: a test-only package is
//     not importable, so "zero importers" says nothing about it. (This package
//     is itself test-only, so it does not check itself.)
//   - Imports are collected from every parsed .go file **regardless of build
//     constraints**, so a package imported only by a platform-gated file still
//     counts as imported. The check is therefore conservative: it can miss a
//     dead package, never invent one.
//   - Transitive deadness is not detected. If a wired package imports an
//     unwired one, the latter is counted as imported.
//   - Files that fail to parse are reported as errors, not skipped silently.

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unwiredPackages maps an import path to the reason it currently has no
// importer. Every entry is a decision: "we are keeping this on purpose, and
// here is what would wire it". An entry that has gained an importer is stale
// and must be removed (the test reports it).
//
// 2026-09-22 (AUD-45) initial inventory — 13 packages.
// 2026-09-22 (AUD-46) pkg/metrics removed from this list and retired instead:
// it was a competing implementation of ADR-017 §1's four core metrics, which
// pkg/observability already implements. See retiredPackages below.
// 2026-10-09 (K4) pkg/backtest/marketimpact removed from this list: K4
// (commit 6e87f8c) 把平方根冲击模型接进了 pkg/backtest/execution/execution.go
// 的 "impact" 滑点分支，它不再是死代码。本条由本护栏自己报出 —— 白名单
// 一旦列了「已经有人用」的包，就在替死代码打掩护。
var unwiredPackages = map[string]string{
	"tellurion/pkg/api":               "P2-16 API 版本化基础设施（APIVersionMiddleware）；各服务目前各自手写 /api/v1，待统一接线",
	"tellurion/pkg/backtest/auction":  "P1-6 集合竞价撮合（9:15-9:25 / 14:57-15:00），待回测引擎接线",
	"tellurion/pkg/alert/systemalert": "系统级运维告警（数据同步停滞 / 回测失败 / 策略退化），待运维链路接线",
	// 2026-10-24 (QT-26)：pkg/sandbox/wasm 从本表移除 —— pkg/backtest 的
	// wasmSignalProvider 已把它接进真实回测（artifact → 逐 bar → 信号 → 成交）。
	"tellurion/pkg/trading/margin":   "融资融券账户管理 / 可融券判定，待实盘链路接线",
	"tellurion/pkg/strategy/options": "期权策略（Black-Scholes / 二叉树），待策略层接线",
	"tellurion/pkg/testutil":         "预留的 DB 集成测试底座（AUD-43 裁决：保留，不删；归档审查报告曾把它列为「有 DB 环境下可选集成验证」的设想）",
	// 2026-11-10（SVC-CORE-BROKER-RETIRE 步骤 B）：进程内订单存储删除后，
	// pkg/id 的唯一消费者消失。broker 通道的 client_order_id 由
	// pkg/brokerhttp 自己生成；ULID 生成器保留为订单域重建的候选。
	"tellurion/pkg/id": "ULID 生成器；原消费者（进程内订单存储）已随 SVC-CORE-BROKER-RETIRE 删除，当前无调用者",
	// AI 拆仓阶段 2：消费者随 pkg/ai 迁往 tellurion-agent 仓，core 侧
	// 不再有导入者。包本体留在 core（通用基础设施），agent 经 module 依赖使用。
	"tellurion/pkg/sandbox/runner":      "copilot build 沙箱 runner；消费者（cmd/ai 的 adapter）已随 AI 拆仓迁入 agent 仓",
	"tellurion/pkg/sandbox/staticcheck": "LLM 生成代码的 staticcheck 校验；消费者已随 AI 拆仓迁入 agent 仓",
	"tellurion/pkg/strategy/monitor":    "策略健康监测（滚动指标 + 漂移）；消费者（tools 的 strategy_health 工具）已随 AI 拆仓迁入 agent 仓",
	// 2026-11-08（SVC-CORE-DATA-RETIRE）：pkg/mining 回到本表——它唯一的生产
	// 导入者是 cmd/data 的 `mining` 子命令，该目录已随数据面退役整删；挖掘
	// 流水线本体与登记账本保留（治理语义仍由 pkg/mining 自己的用例覆盖），
	// 生产接线待 AI 线的执行入口（`SVC-CORE-AI-RETIRE` 的邻域）落地。
	"tellurion/pkg/mining": "挖掘流水线；唯一生产入口 cmd/data 已随 SVC-CORE-DATA-RETIRE 退役，等待 AI 线新的执行入口接线",
	// 2026-11-08（SVC-CORE-DATA-RETIRE）：pkg/sync 的生产导入者是 cmd/data
	// 的同步 handler（NewSyncHandler 注册 workerPool.Cancel），该目录已整删。
	// 包本体与用例保留（AUD-49 的 SetRunningCanceller 语义仍成立），但当前
	// 没有任何 HTTP 入口能触发它 —— 重建数据服务时必须重新接线。
	"tellurion/pkg/sync": "同步作业队列 / worker pool；唯一生产入口 cmd/data 已随 SVC-CORE-DATA-RETIRE 退役，等待数据服务重建",
}

// retiredPackages maps an import path that was deliberately deleted to the
// reason it must not come back. Retiring something is not just deleting the
// code: without this assertion a later change can silently re-introduce it and
// nothing goes red (AUD-33 taught this — deleting the old `mustHave` lines left
// no resistance at all).
//
// 2026-09-22 (AUD-46): pkg/metrics.
// 2026-10-25 (QT-73): 横切五包（errors / decimal / clock / logging / httpclient）
// 已整目录切到 github.com/ruoxizhnya/tellurion-kit，core 侧不留转发壳。同一能力
// 只保留一份实现，旧副本若再出现就说明有人把两份实现又摆回来了。
var retiredPackages = map[string]string{
	"tellurion/pkg/metrics":    "ADR-017 §1 的四个核心指标（backtest_duration_seconds / http_client_requests_total / llm_tokens_total / cache_hit_ratio）由 pkg/observability 实现（deps.Metrics *observability.Metrics）。pkg/metrics 是竞争实现，只与它重叠一个指标名 —— 两者同时接线会重复注册 backtest_duration_seconds 并 panic。要加指标请加进 observability（一个注册表）",
	"tellurion/pkg/errors":     "已切至 github.com/ruoxizhnya/tellurion-kit/errors（QT-73 一期）",
	"tellurion/pkg/decimal":    "已切至 github.com/ruoxizhnya/tellurion-kit/decimal（QT-73 一期）",
	"tellurion/pkg/clock":      "已切至 github.com/ruoxizhnya/tellurion-kit/clock（QT-73 一期）",
	"tellurion/pkg/logging":    "已切至 github.com/ruoxizhnya/tellurion-kit/logging（QT-73 一期）",
	"tellurion/pkg/httpclient": "已切至 github.com/ruoxizhnya/tellurion-kit/httpclient（QT-73 一期）",
	// 2026-11-07 (QT-75)：二期两包（statistics / httpserver）整目录切 kit。
	// httpserver 的 gin 回环包装（LoopbackOnly）按 kit 的分工留在应用层
	// （pkg/bootstrap/loopback.go），core 侧不留横切包。
	"tellurion/pkg/statistics": "已切至 github.com/ruoxizhnya/tellurion-kit/statistics（QT-75 二期）",
	"tellurion/pkg/httpserver": "已切至 github.com/ruoxizhnya/tellurion-kit/httpserver（QT-75 二期）；gin 回环包装留在 pkg/bootstrap/loopback.go",
	// 2026-11-08（SVC-CORE-DATA-RETIRE）：数据面整套退役，数据同步/ETL/
	// 因子计算的唯一 owner 是数据服务仓；core 只经 HTTP 读。
	"tellurion/pkg/data":        "数据面（connector / ETL / 因子计算）已退役，owner 迁至数据服务仓；core 不得再持有实现或 DDL/写点（SVC-CORE-DATA-RETIRE）",
	"tellurion/pkg/data/source": "多源同步 / ETL 管线已退役，随 pkg/data 整目录删除（SVC-CORE-DATA-RETIRE）",
	// 2026-11-10（SVC-CORE-BROKER-RETIRE 步骤 B）：交易通道实现整套退役。
	"tellurion/pkg/trading/broker/xtp": "XTP 券商适配已删除；core 的唯一真实通道是 broker HTTP（pkg/brokerhttp），券商适配 owner 在 tellurion-broker 仓",
}

func TestEveryPackageHasAConsumer(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")
	mod := readModulePath(t, root)

	imported := map[string]bool{}
	candidates := map[string]bool{}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".workbuddy-ai", "node_modules", "web", "testdata", "vendor":
				return fs.SkipDir
			}
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				return rerr
			}
			rel = filepath.ToSlash(rel)
			if isCheckedTree(rel) && dirHasNonTestGo(path) {
				candidates[mod+"/"+rel] = true
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}

		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		file, perr := parser.ParseFile(token.NewFileSet(), path, raw, parser.ImportsOnly)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", path, perr)
		}
		for _, imp := range file.Imports {
			p, uerr := strconv.Unquote(imp.Path.Value)
			if uerr != nil {
				continue
			}
			if strings.HasPrefix(p, mod+"/") {
				imported[p] = true
			}
		}
		return nil
	})
	require.NoError(t, err)

	var unwired, stale []string
	for p := range candidates {
		if imported[p] {
			continue
		}
		if _, ok := unwiredPackages[p]; !ok {
			unwired = append(unwired, p)
		}
	}
	for p := range unwiredPackages {
		if imported[p] {
			stale = append(stale, p)
		}
	}
	sort.Strings(unwired)
	sort.Strings(stale)

	assert.Empty(t, unwired,
		"这些包在 pkg/ 或 internal/ 下，但没有任何导入者 —— 要么接线，要么加进 "+
			"unwiredPackages 并写明「为什么留着、什么会接上它」：%v", unwired)
	assert.Empty(t, stale,
		"unwiredPackages 里这些包已经有导入者了 —— 删掉它们的白名单条目"+
			"（白名单只该列「当前无人用」的包，否则它就在替死代码打掩护）：%v", stale)

	// A retired package must not come back at all — not even as a test-only
	// directory. Deleting the code is only half of it; this is the other half.
	var resurrected []string
	for p := range retiredPackages {
		dir := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(p, mod+"/")))
		if _, statErr := os.Stat(dir); statErr == nil {
			resurrected = append(resurrected, p)
		}
	}
	sort.Strings(resurrected)
	assert.Empty(t, resurrected,
		"这些包已被刻意退役（理由见 retiredPackages），但目录又出现了 —— "+
			"要么删掉它，要么说明当初的退役判断错在哪：%v", resurrected)
}

// isCheckedTree reports whether rel is the pkg/ or internal/ tree (the trees
// whose packages are expected to have consumers).
func isCheckedTree(rel string) bool {
	return rel == "pkg" || rel == "internal" ||
		strings.HasPrefix(rel, "pkg/") || strings.HasPrefix(rel, "internal/")
}

// dirHasNonTestGo reports whether dir contains at least one non-test .go file,
// i.e. whether it defines an importable package.
func dirHasNonTestGo(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			return true
		}
	}
	return false
}

// readModulePath reads the module path from go.mod rather than hard-coding it,
// so the guard keeps working if the module is ever renamed.
func readModulePath(t *testing.T, root string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "go.mod"))
	require.NoError(t, err)
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	require.FailNow(t, "go.mod has no module directive")
	return ""
}
