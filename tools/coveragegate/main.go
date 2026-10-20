// Command coveragegate enforces coverage thresholds on a Go coverage profile.
//
// REV-21: CI 的 go test 只跑 -race，没有 -coverprofile/阈值——「覆盖率往下滑」
// 从来没有红过，TEST.md 的「目标不是门禁」是如实自陈。本工具把阈值固化成
// 机械判据：总量阈值 + 核心包包级阈值，任一不达标 exit 1 并点名差多少。
//
// 口径说明（如实登记，避免误读）：
//   - 只统计**出现在 profile 里的语句块**。`go test ./... -coverprofile`
//     的标准口径下，没有测试文件的包不进 profile——本工具对「被点名阈值却
//     不在 profile 里」的包按 0% 处理并判红（fail-closed：拼错包名或删光了
//     测试都必须红，不能静默通过）。
//   - 语句加权（statement-weighted），与 go tool cover 的 total 同口径；
//     单条覆盖率取「该包覆盖语句数 / 该包总语句数」。
//
// 用法：
//
//	go test ./... -count=1 -coverprofile=coverage.out
//	go run ./tools/coveragegate -profile=coverage.out \
//	  -total-min=60 -pkg-min=tellurion/pkg/backtest=60
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// pkgCoverage 是一个包（或全量）的语句加权覆盖率汇总。
type pkgCoverage struct {
	total   int64 // 总语句数
	covered int64 // 被执行过的语句数（count > 0 即算执行过）
	present bool  // 是否在 profile 里出现过（无测试文件的包不会出现）
}

func (p pkgCoverage) percent() float64 {
	if p.total == 0 {
		return 0
	}
	return float64(p.covered) * 100 / float64(p.total)
}

func main() {
	var (
		profile  = flag.String("profile", "coverage.out", "go 覆盖率 profile 路径")
		totalMin = flag.Float64("total-min", 60, "总量覆盖率下限（百分比）")
	)
	var pkgMins multiFlag
	flag.Var(&pkgMins, "pkg-min", "包级阈值，形如 <importpath>=<百分比>，可重复")
	flag.Parse()

	stats, err := parseProfile(*profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "coveragegate: 读取 profile 失败: %v\n", err)
		os.Exit(2)
	}

	total := stats[""]
	exit := 0

	// 汇总表（按包名排序，total 行打头）。
	fmt.Printf("coveragegate: %s\n", *profile)
	fmt.Printf("  %-70s %6s\n", "package", "cov%")
	fmt.Printf("  %-70s %5.1f%%\n", "TOTAL", total.percent())
	names := make([]string, 0, len(stats))
	for name := range stats {
		if name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Printf("  %-70s %5.1f%%\n", name, stats[name].percent())
	}

	// 判定：总量。
	if total.percent() < *totalMin {
		fmt.Printf("FAIL: 总量覆盖率 %.1f%% < 阈值 %.1f%%（差 %.1f 个百分点）\n",
			total.percent(), *totalMin, *totalMin-total.percent())
		exit = 1
	}

	// 判定：包级。
	for _, spec := range pkgMins {
		importPath, minStr, ok := strings.Cut(spec, "=")
		if !ok {
			fmt.Printf("FAIL: -pkg-min %q 缺少 '='，形如 <importpath>=<百分比>\n", spec)
			exit = 1
			continue
		}
		min, err := strconv.ParseFloat(strings.TrimSpace(minStr), 64)
		if err != nil {
			fmt.Printf("FAIL: -pkg-min %q 的百分比不是数字: %v\n", spec, err)
			exit = 1
			continue
		}
		importPath = strings.TrimSpace(importPath)
		pc := stats[importPath]
		if !pc.present {
			fmt.Printf("FAIL: 包 %s 不在 profile 里（无测试文件？按 0%% 判红）< 阈值 %.1f%%\n",
				importPath, min)
			exit = 1
			continue
		}
		if pc.percent() < min {
			fmt.Printf("FAIL: 包 %s 覆盖率 %.1f%% < 阈值 %.1f%%（差 %.1f 个百分点）\n",
				importPath, pc.percent(), min, min-pc.percent())
			exit = 1
		}
	}

	if exit == 0 {
		fmt.Printf("PASS: 总量 %.1f%% ≥ %.1f%%，包级阈值全部达标\n", total.percent(), *totalMin)
	}
	os.Exit(exit)
}

// multiFlag 收集可重复的字符串 flag。
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

// parseProfile 解析 go coverage profile，按 import path 汇总语句加权覆盖率。
// key "" 是全量合计。
func parseProfile(path string) (map[string]pkgCoverage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	stats := map[string]pkgCoverage{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if lineNo == 1 {
			if !strings.HasPrefix(line, "mode:") {
				return nil, fmt.Errorf("首行应为 \"mode: ...\"，实得 %q", line)
			}
			continue
		}
		if line == "" {
			continue
		}
		// 行格式：<file>:<startLine>.<startCol>,<endLine>.<endCol> <numStmts> <count>
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, fmt.Errorf("第 %d 行不是三字段: %q", lineNo, line)
		}
		loc := fields[0]
		colon := strings.LastIndex(loc, ":")
		if colon < 0 {
			return nil, fmt.Errorf("第 %d 行 location 缺少 ':': %q", lineNo, line)
		}
		filePath := loc[:colon]
		numStmts, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("第 %d 行 numStmts 不是整数: %q", lineNo, line)
		}
		count, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("第 %d 行 count 不是整数: %q", lineNo, line)
		}

		importPath := importPathOf(filePath)
		pc := stats[importPath]
		pc.present = true
		pc.total += numStmts
		if count > 0 {
			pc.covered += numStmts
		}
		stats[importPath] = pc

		all := stats[""]
		all.total += numStmts
		if count > 0 {
			all.covered += numStmts
		}
		stats[""] = all
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if _, ok := stats[""]; !ok {
		return nil, fmt.Errorf("profile 没有任何覆盖数据（空文件？）")
	}
	return stats, nil
}

// importPathOf 从 profile 里的文件路径推出 import path：
// 丢掉最后一段文件名，剩下的目录即包路径（go 的 import path 与目录同形）。
func importPathOf(filePath string) string {
	slash := strings.LastIndex(filePath, "/")
	if slash < 0 {
		return filePath // 没有目录（理论上不会出现）
	}
	return filePath[:slash]
}
