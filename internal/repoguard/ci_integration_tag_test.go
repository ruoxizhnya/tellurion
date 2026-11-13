package repoguard

// CORE-INTEGRATION-TAG-CI（2026-11-13）的结构护栏：`//go:build integration`
// 的用例必须真的有一条 CI 执行路径，且**新加的 tag 包不会掉队**。
//
// 缺陷现场：仓里有 `-tags=integration` 用例（pkg/storage 的真库腿、pkg/backtest
// 的 Router 接线腿），但默认 `go test ./...` 与 CI 都不带 tag ⇒ 这批用例长期
// 既不在绿里也不在红里。它不像编译错误那样会自己叫 —— 只会在某天有人手动
// 跑 tag 时集中爆出来。
//
// 护栏判据（全部落在 `.github/workflows/ci.yml` 的结构上，不匹配注释文案）：
//  1. 存在一个 step，其 run 里既有 `go vet -tags=integration ./...`（tag 包
//     全量可编译），也有 `go test -tags=integration`（真跑）；
//  2. 该 go test 行点名的包模式必须覆盖**仓内每一个**含 build-tag integration
//     文件的目录 —— 扫盘计算，不维护手写清单，新增 tag 包即红；
//  3. 该 step 与迁移 step 同 job，且迁移在前、集成腿在后（共用已迁移的库）；
//  4. TEST_DATABASE_DSN 在该 step 或所在 job 的 env 里非空（pkg/backtest 的真库腿
//     靠它；缺了会变成「显式 skip」而不是执行）。
//
// 为什么不做成 YAML 字符串匹配：注释里写「-tags=integration」也能命中，而真正
// 的缺陷是**执行结构**。这里用 yaml.v3 解析 steps，再只看 run/env。

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type ciStep struct {
	Name string            `yaml:"name"`
	Run  string            `yaml:"run"`
	Env  map[string]string `yaml:"env"`
	// CORE-TEST-EVIDENCE-VERSIONED 护栏需要看 upload-artifact 的结构，
	// 不是只在注释里 grep 到关键字。
	Uses string            `yaml:"uses"`
	If   string            `yaml:"if"`
	With map[string]string `yaml:"with"`
}

type ciJob struct {
	Env   map[string]string `yaml:"env"`
	Steps []ciStep          `yaml:"steps"`
}

type ciWorkflow struct {
	Jobs map[string]ciJob `yaml:"jobs"`
}

// buildConstraintLine 取出 //go:build 行的表达式。
var buildConstraintLine = regexp.MustCompile(`(?m)^//go:build[ \t]+(.+)$`)

// hasIntegrationTag 判断 build 约束表达式里是否**正向**要求 integration。
//
// 只按字段判 `integration`：`//go:build !integration` 是「默认构建才跑」的
// 另一种文件（dbsummary_test.go 用它避开 integration TestMain 冲突），
// 把它算成 tag 腿会要求 CI 去跑一个刻意排除的文件。
func hasIntegrationTag(head []byte) bool {
	for _, m := range buildConstraintLine.FindAllSubmatch(head, -1) {
		for _, field := range strings.Fields(string(m[1])) {
			if field == "integration" {
				return true
			}
		}
	}
	return false
}

// ciPkgPattern 从 go test 命令行里取出 `./x/y...` 形状的包模式。
var ciPkgPattern = regexp.MustCompile(`\./[A-Za-z0-9_./-]+`)

// integrationTaggedDirs 返回仓内所有含 integration 构建约束的 .go 文件所在目录
// （相对仓根、正斜杠）。扫盘而不是手写清单 —— 手写清单在新增包的那天就过期。
func integrationTaggedDirs(t *testing.T, root string) []string {
	t.Helper()
	seen := map[string]bool{}
	var dirs []string

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", "reports":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		head, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// build 约束必须在 package 子句之前；前 2KB 足够且避免把源码里的
		// 字符串（测试自己引用的 tag 文案）误判成约束。
		if len(head) > 2048 {
			head = head[:2048]
		}
		if !hasIntegrationTag(head) {
			return nil
		}
		rel, relErr := filepath.Rel(root, filepath.Dir(path))
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if !seen[rel] {
			seen[rel] = true
			dirs = append(dirs, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("扫描 integration 构建约束失败: %v", err)
	}
	if len(dirs) == 0 {
		t.Fatal("仓内**没有**任何 //go:build integration 文件 —— 本护栏的前提不成立，" +
			"要么 tag 被删光了，要么扫描逻辑失效（不要改断言来让它变绿）")
	}
	return dirs
}

// patternCovers 判断 CI 的包模式是否覆盖某个相对目录（支持 Go 的 `/...` 通配）。
func patternCovers(pattern, dir string) bool {
	pattern = strings.TrimPrefix(pattern, "./")
	dir = strings.TrimPrefix(dir, "./")
	if strings.HasSuffix(pattern, "/...") {
		base := strings.TrimSuffix(pattern, "/...")
		return dir == base || strings.HasPrefix(dir, base+"/")
	}
	return pattern == dir
}

func loadCIWorkflow(t *testing.T, root string) ciWorkflow {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(".github/workflows/ci.yml")))
	if err != nil {
		t.Fatalf("读 .github/workflows/ci.yml 失败: %v", err)
	}
	var wf ciWorkflow
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatalf("解析 ci.yml 失败: %v", err)
	}
	if len(wf.Jobs) == 0 {
		t.Fatal("ci.yml 里一个 job 都没解析到 —— 结构或解析器坏了")
	}
	return wf
}

func TestCIIntegrationTagLegExists(t *testing.T) {
	root := repoRoot(t)
	wf := loadCIWorkflow(t, root)

	var (
		jobName    string
		stepIdx    int
		step       ciStep
		job        ciJob
		testLine   string
		patterns   []string
		vetPresent bool
	)
	for name, j := range wf.Jobs {
		for i, s := range j.Steps {
			if !strings.Contains(s.Run, "-tags=integration") {
				continue
			}
			if !strings.Contains(s.Run, "go test") {
				continue
			}
			jobName, stepIdx, step, job = name, i, s, j
			for _, line := range strings.Split(s.Run, "\n") {
				if strings.Contains(line, "go test") && strings.Contains(line, "-tags=integration") {
					testLine = line
				}
				if strings.Contains(line, "go vet") && strings.Contains(line, "-tags=integration") {
					vetPresent = true
				}
			}
			for _, m := range ciPkgPattern.FindAllString(testLine, -1) {
				patterns = append(patterns, m)
			}
			break
		}
		if testLine != "" {
			break
		}
	}

	if testLine == "" {
		t.Fatal("ci.yml 里没有 `go test -tags=integration` 步骤 —— 带 tag 的用例会重新变成零执行" +
			"（CORE-INTEGRATION-TAG-CI）")
	}
	if !vetPresent {
		t.Errorf("job %q 的 integration 步骤缺 `go vet -tags=integration ./...` —— "+
			"只跑两个包挡不住「别的包新增 tag 文件编译不过」（CORE-INTEGRATION-TAG-CI ②）", jobName)
	}

	// 判据 2：扫盘得到的每个 tag 目录都必须被 go test 的包模式覆盖。
	for _, dir := range integrationTaggedDirs(t, root) {
		covered := false
		for _, p := range patterns {
			if patternCovers(p, dir) {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("目录 %s 含 //go:build integration 用例，但 CI 的 go test 行（%q）没覆盖它 —— "+
				"新增 tag 包必须同批加进 CORE-INTEGRATION-TAG-CI 腿，否则又是零执行", dir, testLine)
		}
	}

	// 判据 3：迁移在前。dockertest 包自己起库，但 pkg/backtest 的真库腿依赖
	// 本 job 已迁移的 service 库（ohlcv_minute 等由迁移创建）。
	applyIdx := -1
	for i, s := range job.Steps {
		if strings.Contains(s.Run, "tools/ci/applymigrations") {
			applyIdx = i
			break
		}
	}
	if applyIdx < 0 {
		t.Fatalf("job %q（integration 腿所在 job）里找不到迁移步 tools/ci/applymigrations —— "+
			"没有迁移，pkg/backtest 的真库腿会因缺表红/跳过", jobName)
	}
	if applyIdx > stepIdx {
		t.Errorf("job %q 的迁移步在第 %d 步、integration 腿在第 %d 步 —— 迁移必须在前"+
			"（CORE-INTEGRATION-TAG-CI ①「与 DB 腿共用 postgres service 与迁移步」）",
			jobName, applyIdx, stepIdx)
	}

	// 判据 4：DSN。step 级或 job 级均可（GitHub Actions 都生效）。
	dsn := strings.TrimSpace(step.Env["TEST_DATABASE_DSN"])
	if dsn == "" {
		dsn = strings.TrimSpace(job.Env["TEST_DATABASE_DSN"])
	}
	if dsn == "" {
		t.Errorf("job %q 的 integration 腿没有 TEST_DATABASE_DSN —— pkg/backtest 的真库腿"+
			"会退化成「显式 skip」，CI 绿了但没验过", jobName)
	}
}
