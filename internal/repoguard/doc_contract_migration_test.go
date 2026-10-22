package repoguard

// QT-05 B3（2026-10-23）文档触发器护栏。契约包整包迁到 tellurion-contracts 之后，
// 代码里已经不可能再 import 旧路径（编译器 + contract_identity_test.go 钉住），但
// **文档**说错没有任何东西会红：AGENTS.md 的目录树仍把 domain/ 画在本仓、
// ARCHITECTURE 仍说 contracts/ 是内核模块契约、SPEC 仍指 contracts/filltiming.go ——
// 新人照文档去 core 找包，找不到，然后「先拷回来应急」，B1 警告的双份真相窗口就
// 重新打开。
//
// 本护栏把「文档与代码同源」做成可执行判据，分两条腿：
//
//	腿 A（目录树）：AGENTS.md §3 与 ARCHITECTURE.md §5 的 directory tree 里，已迁走
//	  的三个包不得再作为条目出现；且 pkg/backtest 条目必须写明契约本体在契约仓。
//	腿 B（正文路径）：三份文档正文里，旧路径只允许出现在**同一行也写了
//	  tellurion-contracts** 的迁移声明行（AGENTS「已迁出」表）；core 的 contracts/
//	  是运行时数据资产目录，正文里不许出现 contracts/*.go 这类 Go 源码路径；SPEC 里
//	  指 domain/market 的位置必须指向契约仓，不得再指向 core 里已被删掉的锚点。
//
// 两条腿都带正证据下限（树条目数 / 必须存在的迁移声明与跨仓指针）：否则有人把整段
// 文档删掉、或把解析器改坏，检查会因为「一条都没匹配到」而恒绿 —— 那是最糟的假绿。

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	// migratedContractDocPaths 是 B1 迁走的三个包在本仓的旧目录路径。
	migratedContractDocPaths = []string{"pkg/domain", "pkg/fees", "pkg/backtest/contracts"}

	// contractMigrationDocFiles 是 Rule 1 要求随代码同步的三份文档（相对仓根）。
	contractMigrationDocFiles = []string{"AGENTS.md", "docs/ARCHITECTURE.md", "docs/SPEC.md"}

	// coreContractsGoFileRef 匹配正文里把 core 的 contracts/ 当成 Go 包来引用的路径。
	// core/contracts/ 只装 field_dictionary.yaml 与 schema（数据资产），不该有 .go；
	// 契约源码在 tellurion-contracts:*。注意不会误伤 snapshot.schema.json 这类资产路径。
	coreContractsGoFileRef = regexp.MustCompile(`contracts/[A-Za-z0-9_]+\.go`)
)

// TestCoreDocTreesOmitMigratedContractPackages 腿 A：目录树不得再把已迁走的契约包
// 画在本仓。
func TestCoreDocTreesOmitMigratedContractPackages(t *testing.T) {
	t.Parallel()

	trees := []struct {
		file    string
		heading string
		min     int
	}{
		{"AGENTS.md", "## 3. 目录结构", 15},
		{"docs/ARCHITECTURE.md", "## 5. 目录结构（本仓）", 8},
	}
	for _, tree := range trees {
		tree := tree
		t.Run(tree.file, func(t *testing.T) {
			t.Parallel()

			entries := coreDocTreeEntries(t, tree.file, tree.heading, tree.min)

			var offenders []string
			for _, legacy := range migratedContractDocPaths {
				if line, ok := entries[legacy]; ok {
					offenders = append(offenders,
						legacy+" —— 原行："+strings.TrimSpace(line))
				}
			}
			assert.Empty(t, offenders,
				"%s 的目录树仍把已整包迁到 tellurion-contracts 的包画在本仓（Rule 1：代码动了"+
					"文档必须跟着动）。删掉这些条目，按 AGENTS.md「已迁出」表的口径指向契约仓：\n%s",
				tree.file, strings.Join(offenders, "\n"))

			// 正证据：backtest 条目必须写明契约本体已外置 —— 防止「把行删干净」的同时
			// 也把迁移线索删了。
			backtestLine, ok := entries["pkg/backtest"]
			require.True(t, ok,
				"%s 的目录树里找不到 pkg/backtest 条目 —— 树结构变了，先修护栏再修文档", tree.file)
			assert.True(t,
				strings.Contains(backtestLine, "tellurion-contracts") ||
					strings.Contains(backtestLine, "contracts 仓"),
				"%s 的 pkg/backtest 条目没写契约本体已外置（期望含 tellurion-contracts 或"+
					" contracts 仓）：%s", tree.file, strings.TrimSpace(backtestLine))

			// 正证据：core 的 contracts/ 是运行时数据资产目录，不是 Go 包 —— 两棵树都
			// 必须这么写（否则又会有人把它当内核契约包来找）。
			contractsLine, ok := entries["contracts"]
			require.True(t, ok,
				"%s 的目录树里找不到 contracts/ 条目 —— 树结构变了，先修护栏再修文档", tree.file)
			assert.True(t,
				strings.Contains(contractsLine, "不是 Go 包") ||
					strings.Contains(contractsLine, "数据契约资产"),
				"%s 的 contracts/ 条目没写清它是运行时数据资产、不是 Go 包：%s",
				tree.file, strings.TrimSpace(contractsLine))

			// 正证据：两把读 core docs/SPEC.md 的 SPEC 互锁守卫留在 e2e/guard/，
			// 目录树必须登记（QT-05 A-7；它们随包迁走就会去读不存在的 SPEC）。
			e2eLine, ok := entries["e2e"]
			require.True(t, ok,
				"%s 的目录树里找不到 e2e/ 条目 —— 树结构变了，先修护栏再修文档", tree.file)
			assert.Contains(t, e2eLine, "e2e/guard/",
				"%s 的 e2e/ 条目没登记 e2e/guard/ 下那两把跨仓 SPEC 互锁守卫：%s",
				tree.file, strings.TrimSpace(e2eLine))
		})
	}
}

// TestCoreDocProseRoutesMigratedPackagesToContractsRepo 腿 B：正文里的旧路径必须与
// 新家同现，SPEC 的跨仓指针不得回指 core 死锚点。
func TestCoreDocProseRoutesMigratedPackagesToContractsRepo(t *testing.T) {
	t.Parallel()

	var offenders []string
	for _, rel := range contractMigrationDocFiles {
		for i, line := range coreDocLines(t, rel) {
			where := rel + ":" + strconv.Itoa(i+1)
			for _, legacy := range migratedContractDocPaths {
				if !strings.Contains(line, legacy) {
					continue
				}
				if strings.Contains(line, "tellurion-contracts") {
					continue // 迁移声明行（AGENTS「已迁出」表）：旧路径与新家同现，合法
				}
				offenders = append(offenders,
					where+" 裸提旧路径 "+legacy+"："+strings.TrimSpace(line))
			}
			if m := coreContractsGoFileRef.FindString(line); m != "" &&
				!strings.Contains(line, "tellurion-contracts") {
				offenders = append(offenders, where+" 把 core 的 contracts/ 当成 Go 包引用（"+
					m+"）—— 契约源码在 tellurion-contracts："+strings.TrimSpace(line))
			}
		}
	}

	// AGENTS.md §6 的「包结构」单行同样是包布局说明：契约层已外置，这一行必须写明
	// tellurion-contracts（否则读者又会按 domain/ → data/ … 去本仓找 domain）。
	pkgStructSeen := false
	for i, line := range coreDocLines(t, "AGENTS.md") {
		if !strings.Contains(line, "包结构：") {
			continue
		}
		pkgStructSeen = true
		if !strings.Contains(line, "tellurion-contracts") {
			offenders = append(offenders, "AGENTS.md:"+strconv.Itoa(i+1)+
				"「包结构」仍把已迁走的 domain/ 画在本仓："+strings.TrimSpace(line))
		}
	}
	assert.True(t, pkgStructSeen,
		"AGENTS.md 的「包结构：」行被删了 —— 包布局说明是 Rule 1 的触发目标，不能整行消失")

	assert.Empty(t, offenders,
		"这些文档行还把人指向本仓已不存在的契约包/源码（Rule 1 文档触发器）：\n%s",
		strings.Join(offenders, "\n"))

	// 正证据 1：迁移知识不许丢 —— AGENTS.md 必须有一行同时列出三个旧路径与新家。
	assert.True(t,
		coreDocHasLineContaining(coreDocLines(t, "AGENTS.md"),
			"pkg/domain/", "pkg/fees/", "pkg/backtest/contracts/", "tellurion-contracts"),
		"AGENTS.md 的「已迁出」表缺了契约包这一行：三个旧路径必须与新家 tellurion-contracts "+
			"写在同一行，否则后人不知道它们搬到哪了（QT-05 交接单 A-7）")

	spec := strings.Join(coreDocLines(t, "docs/SPEC.md"), "\n")
	assert.Contains(t, spec, "tellurion-contracts:engine/filltiming.go",
		"SPEC.md 里 FillTiming.KnownBiases() 的生产者路径必须指向 tellurion-contracts:engine/")
	assert.NotContains(t, spec, "ARCHITECTURE.md#domain-market",
		"SPEC.md 仍在链接 core ARCHITECTURE 里已被删除的「Domain Market 软分层架构」锚点"+
			"（check_doc_links.py 只查文件存在、查不到锚点，所以由本护栏钉）")
	assert.Contains(t, spec, "tellurion-contracts:docs/API.md",
		"SPEC.md 的 domain/market 指针应改为跨仓纯文本指针 tellurion-contracts:docs/API.md §4")
}

// coreDocLines 读取仓内文档并按行切分。
func coreDocLines(t *testing.T, rel string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
	require.NoError(t, err,
		"读不到文档 %s —— 它是 Rule 1 的触发目标，删了要先改本护栏", rel)
	return strings.Split(string(data), "\n")
}

// coreDocHasLineContaining 判断是否存在同时包含全部 needles 的一行。
func coreDocHasLineContaining(lines []string, needles ...string) bool {
	for _, line := range lines {
		hit := true
		for _, needle := range needles {
			if !strings.Contains(line, needle) {
				hit = false
				break
			}
		}
		if hit {
			return true
		}
	}
	return false
}

// coreDocTreeEntries 解析文档某个二级标题后的第一个围栏目录树，返回「路径 -> 条目
// 原始行」。路径按 box-drawing 缩进推导：depth = `── ` 之前的 rune 数 / 4
// （`├` = 0、`│   ├` = 1、`│   │   ├` = 2）；条目名取 `── ` 之后的第一个空白分隔
// token（一行画两个包的条目只认第一个）。
//
// fail-closed：标题找不到、围栏找不到/不闭合、条目数低于 minEntries，一律 fail ——
// 解析走空会让所有「不得出现旧包」的断言恒绿。
func coreDocTreeEntries(t *testing.T, rel, heading string, minEntries int) map[string]string {
	t.Helper()
	lines := coreDocLines(t, rel)

	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, heading) {
			start = i
			break
		}
	}
	require.GreaterOrEqual(t, start, 0,
		"%s 里找不到标题 %q —— 目录树被改名/删了，先修标题再修护栏", rel, heading)

	open := -1
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "```" {
			open = i
			break
		}
	}
	require.GreaterOrEqual(t, open, 0, "%s 的 %q 节下没有围栏代码块 —— 目录树被删了", rel, heading)

	end := -1
	for i := open + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "```" {
			end = i
			break
		}
	}
	require.GreaterOrEqual(t, end, 0, "%s 的 %q 节下围栏代码块没有闭合", rel, heading)

	stack := make([]string, 0, 4)
	entries := map[string]string{}
	for _, line := range lines[open+1 : end] {
		glyph := strings.Index(line, "── ")
		if glyph < 0 {
			continue // 树根（`tellurion/`）与说明行
		}
		// depth 按 **rune** 数算，不能按 byte：`│` / `├` / `—` 都是多字节字符，
		// 用 byte 下标会把第 1 层（9 字节）算成 2 层、整棵子树被跳过。
		depth := utf8.RuneCountInString(line[:glyph]) / 4 // `├` = 0；`│   ├` = 1；`│   │   ├` = 2 …
		name := line[glyph+len("── "):]
		if hash := strings.Index(name, "  #"); hash >= 0 {
			name = name[:hash]
		}
		fields := strings.Fields(name)
		if len(fields) == 0 {
			continue
		}
		name = strings.TrimSuffix(fields[0], "/")
		if name == "" || depth > len(stack) {
			continue // 缩进跳级不猜父级，宁可少解析（条数下限会兜底）
		}
		stack = append(stack[:depth], name)
		entries[strings.Join(stack, "/")] = line
	}

	require.GreaterOrEqual(t, len(entries), minEntries,
		"%s 的 %q 目录树只解析出 %d 个条目（下限 %d）—— 解析走空会让「不得出现旧包」"+
			"的断言恒绿", rel, heading, len(entries), minEntries)
	return entries
}
