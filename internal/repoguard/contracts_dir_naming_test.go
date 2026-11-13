package repoguard

// CORE-CONTRACTS-DIR-NAMING（2026-11-13）的结构护栏。
//
// 缺陷现场：core 根有个 `contracts/`，装的是 5 份 yaml/sql/json 数据契约资产；
// 独立仓 `tellurion-contracts` 装的是四契约的 Go 类型。同名不同物，而 core 根
// 没有任何一句说明 —— 读者（和 agent）很容易把前者当成后者、或以为它是
// submodule。任务要求「不移动文件、不改目录名、不改契约内容」，只补说明。
//
// 判据：
//  1. `contracts/README.md` 必须在，且明说「不是 Go 契约仓」并指向
//     `tellurion-contracts`；
//  2. README 必须逐条列出目录里的**每一个**资产文件 —— 新增资产不登记即红，
//     否则这份说明会在下一次加文件时悄悄过期；
//  3. `docs/ARCHITECTURE.md` 的数据归属节必须同样点到 `contracts/` 与
//     `tellurion-contracts`（两处口径一致）。

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContractsDirIsDocumentedAsDataAssets(t *testing.T) {
	root := repoRoot(t)

	readmePath := filepath.Join(root, "contracts", "README.md")
	readme := readFile(t, readmePath)

	for _, want := range []string{"tellurion-contracts", "Go 契约"} {
		if !strings.Contains(readme, want) {
			t.Errorf("contracts/README.md 没提到 %q —— 读者仍会把它当成 Go 契约仓", want)
		}
	}
	if !strings.Contains(readme, "不是") {
		t.Error("contracts/README.md 没有一句否定式说明（「不是 Go 契约仓」）—— 性质必须直说")
	}

	// 资产清单自净：README 必须列出目录里的每个资产文件。
	var assets []string
	err := filepath.WalkDir(filepath.Join(root, "contracts"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".yaml", ".yml", ".sql", ".json":
			assets = append(assets, d.Name())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("扫描 contracts/ 失败: %v", err)
	}
	if len(assets) == 0 {
		t.Fatal("contracts/ 下没有任何 yaml/sql/json 资产 —— 本护栏前提不成立，先确认目录是否被搬走")
	}
	for _, name := range assets {
		if !strings.Contains(readme, name) {
			t.Errorf("contracts/%s 没有出现在 README.md 里 —— 新增资产必须同批登记，否则说明会过期", name)
		}
	}

	// 架构文档：数据归属节同口径。
	arch := readFile(t, filepath.Join(root, "docs", "ARCHITECTURE.md"))
	if !strings.Contains(arch, "contracts/README.md") || !strings.Contains(arch, "tellurion-contracts") {
		t.Error("docs/ARCHITECTURE.md §2 数据归属中没有把 contracts/ 与 tellurion-contracts 区分开" +
			"（或没链到 contracts/README.md）—— REPO-MAP/架构文档的口径必须一致")
	}

	// 反向钉：目录里不许冒出 Go 文件（那才是「Go 契约仓」的形态）。
	var goFiles []string
	err = filepath.WalkDir(filepath.Join(root, "contracts"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.HasSuffix(path, ".go") {
			goFiles = append(goFiles, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("扫描 contracts/ 失败: %v", err)
	}
	if len(goFiles) > 0 {
		t.Errorf("contracts/ 里出现了 Go 文件 %v —— 本目录只放数据契约资产；Go 契约属于 tellurion-contracts", goFiles)
	}

	// 兜底：确认 README 是普通文件而不是目录占位。
	if st, err := os.Stat(readmePath); err != nil || st.IsDir() {
		t.Fatalf("contracts/README.md 不可读: %v", err)
	}
}
