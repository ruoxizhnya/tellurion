package main

// DBT-04：pkg/mining 的生产编排入口（cmd/data 子命令）。
//
// 此前 mining 流水线（Pipeline.Run + Effective + EvaluateOnSnapshot + D4
// 登记注册表）在库层闭环但无生产导入者，按 P2-9wire 登记在 repoguard 的
// unwiredPackages 白名单。本文件把入口接上：
//
//	go run ./cmd/data mining \
//	    -candidates mining_candidates.json \
//	    -symbols 600000.SH,000001.SZ -days 120 \
//	    -registrar data-ops
//
// 形态边界（D8 既有裁决，本单不越界）：
//   - 候选从 **JSON 文件**读（fileProposer）——真实 LLM/MCTS Proposer 是
//     另立的挖掘算法工单；本入口只要求候选符合 Candidate 的 JSON 形态
//     （公式文本 + 来源留痕），治理/沙箱/登记链路原样复用。
//   - Evaluator 是本文件的最小 IC 评估器：EvaluateOnSnapshot 在只读快照
//     上求因子序列，与 forward horizon 收益做逐日截面 Pearson 相关，样本
//     前 70% 为 in-sample、后 30% 为 out-of-sample（治理 OOS 保留比的输入）。
//   - 快照从真库 ohlcv_daily_qfq 预加载（启动前一次性取数，防偷看红线
//     与 D6 一致：求值期不碰运行时查询）。
//
// DBT-12 补上 D4 登记表的持久化接线（此前是内存账本，「重启即失」）：
//   - 起跑时用 factor_registrations 重建账本 → 历史登记跨运行存活，重复
//     挖同一版会在 Register 处被拦（ErrRegistrationDuplicate），不再静默
//     二次入库；
//   - 裁决后写穿：本轮新增登记 → factor_registrations（审计留痕由
//     SaveFactorRegistration 自带），每条有版本哈希的候选（含被拒的）→
//     factor_mining_records；
//   - 写失败逐条 PERSIST-FAILED 打印并以退出码 1 收口——留痕不完整不能算
//     这一轮成功。库读侧不可用时降级为内存账本，降级走哪条路径写在输出里。
//
// 不改 Pipeline / EvaluateOnSnapshot 的签名与语义；不新增外部依赖。

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"

	"tellurion/pkg/data"
	"tellurion/pkg/domain"
	"tellurion/pkg/domain/market"
	"tellurion/pkg/marketdata"
	"tellurion/pkg/mining"
)

// miningStore 是编排层用到的持久化面（D4 登记账本 + D8 挖掘留痕的 DB 形态）。
//
// 生产实现**只有一个**：*storage.PostgresStore（三个方法都长在它身上，本单
// 不新增实现、不加新入口）。抽成接口的理由是可把两条行为腿钉成测试——
// 「从库重建账本」与「裁决后写穿」；否则这两条只能在真库上跑一轮人眼看，
// 而「跑过一轮」抓不住「下次不写了」。
type miningStore interface {
	LoadFactorRegistrations(ctx context.Context) ([]domain.FactorRegistration, error)
	SaveFactorRegistration(ctx context.Context, reg domain.FactorRegistration) error
	SaveFactorMiningRecord(ctx context.Context, rec domain.FactorMiningRecord) error
}

// runMiningSubcommand 是子命令外壳：复用服务的 config/store builder，
// 跑一轮编排后退出。
func runMiningSubcommand(args []string) int {
	if err := loadConfig(); err != nil {
		fmt.Fprintf(os.Stderr, "mining: load config: %v\n", err)
		return 2
	}
	// 子命令以 stdout/exit code 为输出面，不写结构化日志。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 与 data-service 启动共用 openStore 这一条构造路径（连库 + 跑幂等迁移，
	// 迁移只应由启动/部署路径触发 —— 见 setup.go openStore 的 DATA-09 说明）。
	// 规范层 Router 复用同一个池：一个进程一个池。
	store, err := openStore(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mining: %v\n", err)
		return 1
	}
	defer store.Close()

	return runMiningCmd(ctx, marketdata.NewRouter(store.DB()), store, args, os.Stdout, os.Stderr)
}

// fileProposer 从候选 JSON 文件产出候选（生产编排的最小 Proposer）。
//
// 文件形态：Candidate JSON 数组（name/formula/lookahead/lookback/
// effective_from/provenance，字段名见 mining.Candidate 的 json tag）。
type fileProposer struct {
	cands []mining.Candidate
}

func (f *fileProposer) Propose(_ context.Context) ([]mining.Candidate, error) {
	return f.cands, nil
}

// loadCandidatesFile 读并解析候选文件（空数组合法——跑一轮零候选也是
// 留痕：本轮「什么都没挖出来」与「没跑」是两回事）。
func loadCandidatesFile(path string) ([]mining.Candidate, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read candidates file: %w", err)
	}
	var cands []mining.Candidate
	if err := json.Unmarshal(raw, &cands); err != nil {
		return nil, fmt.Errorf("parse candidates JSON: %w", err)
	}
	return cands, nil
}

// icEvaluator 是编排侧的最小评估器：快照求值 → forward-return 截面 IC。
type icEvaluator struct {
	// horizon 是 forward return 的前瞻天数（默认 5）。
	horizon int
	// inFraction 是 in-sample 占比（默认 0.7，其余为 out-of-sample）。
	inFraction float64
}

func (e *icEvaluator) Evaluate(_ context.Context, c mining.Candidate, snap *mining.Snapshot) (mining.Metrics, error) {
	def, err := c.Definition()
	if err != nil {
		return mining.Metrics{}, err
	}
	factor, err := mining.EvaluateOnSnapshot(def, snap)
	if err != nil {
		return mining.Metrics{}, fmt.Errorf("evaluate %q on snapshot: %w", c.Name, err)
	}

	// close 序列直接取自快照（评估器只读快照，与防偷看红线一致）。
	closes := snap.Fields["close"]
	if len(closes) == 0 {
		return mining.Metrics{}, fmt.Errorf("snapshot has no close field (IC needs forward returns)")
	}

	dailyIC := e.dailyCrossSectionIC(factor, closes)
	if len(dailyIC) == 0 {
		return mining.Metrics{}, fmt.Errorf("factor %q: no evaluable cross-section (need ≥3 symbols with non-NaN factor and forward return)", c.Name)
	}

	split := int(float64(len(dailyIC)) * e.inFraction)
	if split <= 0 || split >= len(dailyIC) {
		return mining.Metrics{}, fmt.Errorf("factor %q: sample split degenerate (%d periods)", c.Name, len(dailyIC))
	}
	return mining.Metrics{
		InSampleIC:  mean(dailyIC[:split]),
		OutSampleIC: mean(dailyIC[split:]),
		NumPeriods:  len(dailyIC),
	}, nil
}

// dailyCrossSectionIC 逐日计算「因子值 vs forward horizon 收益」的截面
// Pearson 相关。因子序列是**尾部对齐**（snapshotProvider.GetField 的
// lookback 语义：只取最近 lookback 根，末点 = as-of），故因子下标 t 对应
// close 下标 offset+t（offset = clen - flen）。非有限值（NaN 缺失语义）
// 跳过；有效截面 < 3 只的日不计（相关系数无意义）。
func (e *icEvaluator) dailyCrossSectionIC(factor, closes map[string][]float64) []float64 {
	horizon := e.horizon
	if horizon <= 0 {
		horizon = 5
	}

	type aligned struct {
		offset int
		f      []float64
		c      []float64
	}
	var series []aligned
	minFLen := math.MaxInt
	for sym := range closes {
		f := factor[sym]
		if len(f) == 0 || len(f) > len(closes[sym]) {
			continue
		}
		series = append(series, aligned{offset: len(closes[sym]) - len(f), f: f, c: closes[sym]})
		if len(f) < minFLen {
			minFLen = len(f)
		}
	}
	if minFLen == math.MaxInt || minFLen <= horizon {
		return nil
	}

	ics := make([]float64, 0, minFLen-horizon)
	for t := 0; t+horizon < minFLen; t++ {
		var fs, rs []float64
		for _, s := range series {
			fv, c0, c1 := s.f[t], s.c[s.offset+t], s.c[s.offset+t+horizon]
			if math.IsNaN(fv) || math.IsInf(fv, 0) || c0 <= 0 || math.IsNaN(c1) || c1 <= 0 {
				continue
			}
			fs = append(fs, fv)
			rs = append(rs, c1/c0-1)
		}
		if len(fs) < 3 {
			continue // 截面太窄，相关系数无意义
		}
		ics = append(ics, pearson(fs, rs))
	}
	// 过滤 NaN 截面（方差为 0 的日）。
	out := ics[:0]
	for _, ic := range ics {
		if !math.IsNaN(ic) && !math.IsInf(ic, 0) {
			out = append(out, ic)
		}
	}
	return out
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	sum := 0.0
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

// pearson 是教科书画法的 Pearson 相关（编排侧评估专用；不引入统计库
// 依赖——「不新增外部依赖」约束）。
func pearson(xs, ys []float64) float64 {
	n := len(xs)
	if n == 0 || n != len(ys) {
		return math.NaN()
	}
	mx, my := mean(xs), mean(ys)
	var cov, vx, vy float64
	for i := 0; i < n; i++ {
		dx, dy := xs[i]-mx, ys[i]-my
		cov += dx * dy
		vx += dx * dx
		vy += dy * dy
	}
	if vx == 0 || vy == 0 {
		return math.NaN()
	}
	return cov / math.Sqrt(vx*vy)
}

// loadCloseSnapshot 经**规范层 Router** 预加载 close 快照（repoguard
// canonical-layer 棘轮：生产代码不得新开直连物理表的路径；D6 红线同向——
// 求值期唯一数据来源 = 启动前一次性预加载的快照）。
func loadCloseSnapshot(ctx context.Context, router *marketdata.Router, symbols []string, days int) (*mining.Snapshot, time.Time, error) {
	if len(symbols) == 0 {
		return nil, time.Time{}, fmt.Errorf("no symbols selected for snapshot")
	}
	end := time.Now().Truncate(24 * time.Hour)
	start := end.AddDate(0, 0, -days)

	fields := map[string]map[string][]float64{"close": {}}
	loaded := 0
	for _, sym := range symbols {
		bars, err := router.Bars(ctx, market.Freq1d, sym, start, end)
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("load %s: %w", sym, err)
		}
		if len(bars) == 0 {
			continue // 无数据的 symbol 跳过（快照缺失语义）
		}
		series := make([]float64, 0, len(bars))
		for _, b := range bars {
			series = append(series, b.Close)
		}
		fields["close"][sym] = series
		loaded++
	}
	if loaded == 0 {
		return nil, time.Time{}, fmt.Errorf("no daily bars for %d symbols in %s..%s — is the daily table loaded?", len(symbols), start.Format("2006-01-02"), end.Format("2006-01-02"))
	}
	return mining.NewSnapshot(fields), end, nil
}

// runMiningCmd 是 `cmd/data mining` 子命令的实现。返回进程退出码。
func runMiningCmd(ctx context.Context, router *marketdata.Router, store miningStore, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mining", flag.ContinueOnError)
	fs.SetOutput(stderr)
	candidatesPath := fs.String("candidates", "", "candidates JSON file (mining.Candidate array)")
	symbolsFlag := fs.String("symbols", "600000.SH,000001.SZ", "comma-separated snapshot symbols")
	days := fs.Int("days", 120, "snapshot window length in calendar days")
	registrar := fs.String("registrar", "data-service-mining", "registrar identity (D4 four-required)")
	horizon := fs.Int("horizon", 5, "forward return horizon in trading days")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*candidatesPath) == "" {
		fmt.Fprintln(stderr, "mining: -candidates is required")
		return 2
	}

	cands, err := loadCandidatesFile(*candidatesPath)
	if err != nil {
		fmt.Fprintf(stderr, "mining: %v\n", err)
		return 2
	}

	var symbols []string
	for _, s := range strings.Split(*symbolsFlag, ",") {
		if s = strings.TrimSpace(s); s != "" {
			symbols = append(symbols, s)
		}
	}

	snap, asOf, err := loadCloseSnapshot(ctx, router, symbols, *days)
	if err != nil {
		fmt.Fprintf(stderr, "mining: %v\n", err)
		return 1
	}

	return runMiningPipeline(cands, snap, asOf, *registrar, *horizon, store, stdout)
}

// regKey 是一条登记的复合键 (因子名, 版本哈希)：与 factor_registrations 的
// 主键同键，写穿差分按它判「哪几条是本轮新增」。
type regKey struct {
	name domain.FactorType
	hash string
}

// runMiningPipeline 是编排的逻辑层（快照与候选就绪后）：从库重建 D4 账本
// → Pipeline.Run → 逐条留痕输出 → 裁决结果写穿。DBT-04 的冒烟腿与 DBT-12 的
// 两条行为腿（从库重建 / 写穿留痕）都钉在本层。
//
// 账本来源：起跑时 LoadFactorRegistrations + NewRegistrationBookFrom，让历史
// 登记跨运行存活（「重启即失」的反面）——同一版被再挖一次会在 Register 处撞
// ErrRegistrationDuplicate 而不是悄悄二次入库。库读侧不可用时降级为内存账本，
// 降级走了哪条路径写在输出里，不静默。
//
// 写穿：本轮**新增**的登记 → SaveFactorRegistration（审计留痕由该方法自带），
// 每条带版本哈希的候选（被拒的也算：「试过但没要」同样是审计信息）→
// SaveFactorMiningRecord。任一写失败打印 PERSIST-FAILED 并以退出码 1 收口——
// 一轮「跑完但库里没留痕」的挖掘不能算成功。
func runMiningPipeline(cands []mining.Candidate, snap *mining.Snapshot, asOf time.Time, registrar string, horizon int, store miningStore, stdout io.Writer) int {
	ctx := context.Background()
	book, registryMode := loadRegistrationBook(ctx, store)
	// 写穿前先取已在册的键：从库重建的历史条目本就在库里，只该落本轮新增的。
	prior := registeredKeys(book)

	runID := time.Now().UTC().Format("20060102T150405Z")
	p := mining.NewPipeline(&fileProposer{cands: cands},
		&icEvaluator{horizon: horizon, inFraction: 0.7},
		snap, book, registrar)

	records, err := p.Run(ctx)
	if err != nil {
		fmt.Fprintf(stdout, "mining run %s failed: %v\n", runID, err)
		return 1
	}

	admitted := 0
	fmt.Fprintf(stdout, "mining run %s: snapshot as_of=%s symbols=%d candidates=%d horizon=%dd\n",
		runID, asOf.Format("2006-01-02"), len(snap.Fields["close"]), len(records), horizon)
	fmt.Fprintf(stdout, "mining run %s: registry = %s\n", runID, registryMode)
	for _, rec := range records {
		if rec.Admitted {
			admitted++
			fmt.Fprintf(stdout, "  ADMITTED %-20s version=%s in_ic=%.4f out_ic=%.4f periods=%d\n",
				rec.Candidate.Name, rec.VersionHash, rec.Metrics.InSampleIC, rec.Metrics.OutSampleIC, rec.Metrics.NumPeriods)
			continue
		}
		fmt.Fprintf(stdout, "  REJECTED %-20s reason=%s\n", rec.Candidate.Name, rec.RejectReason)
	}

	regsWritten, recsWritten, failures := persistRun(ctx, store, book, prior, records)
	for _, f := range failures {
		fmt.Fprintf(stdout, "  PERSIST-FAILED %s\n", f)
	}
	fmt.Fprintf(stdout, "mining run %s complete: %d/%d admitted (registrations written: %d, mining records written: %d, persist failures: %d)\n",
		runID, admitted, len(records), regsWritten, recsWritten, len(failures))
	if len(failures) > 0 {
		// 留痕不完整 = 这一轮不可复现，不能以 0 收口。
		return 1
	}
	return 0
}

// loadRegistrationBook 用库里的历史登记重建 D4 账本。
//
// 第二个返回值是人话版的「这一轮跑在哪条路径上」，必须进输出：账本来源不是
// 实现细节，它决定「这轮的登记下次还在不在」。
func loadRegistrationBook(ctx context.Context, store miningStore) (*data.RegistrationBook, string) {
	regs, err := store.LoadFactorRegistrations(ctx)
	if err != nil {
		return data.NewRegistrationBook(), fmt.Sprintf(
			"in-memory fallback (postgres load failed: %v) — history is not visible and restarts lose this run", err)
	}
	book, err := data.NewRegistrationBookFrom(regs)
	if err != nil {
		return data.NewRegistrationBook(), fmt.Sprintf(
			"in-memory fallback (postgres book rebuild failed: %v) — history is not visible and restarts lose this run", err)
	}
	return book, fmt.Sprintf("postgres (rebuilt from %d existing registration(s))", len(regs))
}

// registeredKeys 快照账本当前的 (名, 哈希) 键集（写穿差分的基线）。
func registeredKeys(book *data.RegistrationBook) map[regKey]bool {
	regs := book.Registrations()
	out := make(map[regKey]bool, len(regs))
	for _, reg := range regs {
		out[regKey{name: reg.Definition.Name, hash: reg.VersionHash}] = true
	}
	return out
}

// persistRun 把本轮裁决结果写穿到库，返回（新增登记数, 留痕数, 失败清单）。
//
// 失败清单非空时调用方必须让它可见并以非零码收口：内存账本「跑得很好」而库里
// 什么都没留下，是最难发现的一类失（本单的存在理由就是这个）。
func persistRun(ctx context.Context, store miningStore, book *data.RegistrationBook, prior map[regKey]bool, records []mining.Record) (int, int, []string) {
	var failures []string
	regsWritten := 0
	for _, reg := range book.Registrations() {
		k := regKey{name: reg.Definition.Name, hash: reg.VersionHash}
		if prior[k] {
			continue // 起跑时就已在册（含从库重建的历史条目）：不是本轮新增
		}
		if err := store.SaveFactorRegistration(ctx, reg); err != nil {
			failures = append(failures, fmt.Sprintf("registration %s@%s: %v", reg.Definition.Name, reg.VersionHash, err))
			continue
		}
		regsWritten++
	}

	recsWritten := 0
	for _, rec := range records {
		ev, ok := rec.Evidence()
		if !ok {
			continue // 公式连解析都没过 → 没有「定义版本」，不进以版本为键的留痕表
		}
		if err := store.SaveFactorMiningRecord(ctx, ev); err != nil {
			failures = append(failures, fmt.Sprintf("mining record %s@%s: %v", ev.FactorName, ev.VersionHash, err))
			continue
		}
		recsWritten++
	}
	return regsWritten, recsWritten, failures
}
