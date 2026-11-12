package repoguard

// CORE-FACTOR-CACHE-RETIRE 反向护栏：`public.factor_cache` 的持久化是
// **不可逆**退役（ADR-037 D2：因子值由消费点按需求值、物化到 run 级内存）。
//
// 本护栏钉三件事：
//
//  1. 生产代码（pkg / internal / cmd 的非测试 .go）里，除了行注释外**零**
//     `factor_cache` 的读写 SQL —— SELECT / INSERT / UPDATE / JOIN /
//     CREATE / ALTER / TRUNCATE 都不许再回来。
//  2. 读写方法与端口签名不许复活：`GetFactorCacheRange` /
//     `SaveFactorCacheBatch` / `GetFactorCache(` 在非测试代码中出现即红。
//  3. 正向对照：`pkg/storage/postgres.go` 必须仍有且仅有 Migration 066 的
//     `DROP TABLE IF EXISTS public.factor_cache` —— 存量库确实要删表，不是
//     只把建表语句删掉了事；若有人整块删空 migrate()，这条先红。
//
// 为什么是静态断言而不是行为测试：表已被删掉，连不上库时「没有这张表」什么
// 也不证明；SQL 文本是纯事实，静态检查在 CI 无库环境同样有效。
//
// 破坏验证（2026-11-12 施工时实做）：往 `pkg/storage/cache.go` 加回一行
// `INSERT INTO public.factor_cache …` ⇒ 本护栏红；删掉该行、逐字节还原后复绿。

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestFactorCachePersistenceIsRetired(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	readWriteSQL := regexp.MustCompile(
		`(?i)\b(?:FROM|INTO|UPDATE|JOIN)\s+(?:public\.)?factor_cache\b` +
			`|(?:CREATE|ALTER|TRUNCATE)\s+(?:TABLE[^;]{0,40})?(?:IF\s+(?:NOT\s+)?EXISTS\s+)?(?:public\.)?factor_cache\b`)
	legacyAPI := regexp.MustCompile(`\b(?:GetFactorCacheRange|SaveFactorCacheBatch|GetFactorCache)\b`)

	for _, dir := range []string{"pkg", "internal", "cmd"} {
		base := filepath.Join(root, dir)
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			body := stripGoLineComments(readFile(t, path))
			if m := readWriteSQL.FindString(body); m != "" {
				t.Errorf("%s 仍出现 factor_cache 读写 SQL %q —— 该表已退役（CORE-FACTOR-CACHE-RETIRE / ADR-037 D2），"+
					"因子值应派生后物化到 run 级缓存，不得落库", rel, m)
			}
			if m := legacyAPI.FindString(body); m != "" {
				t.Errorf("%s 仍出现已删除的存储方法 %q —— factor_cache 的读写面已随表退役，"+
					"若确需恢复，须先重新裁决因子值 owner（CORE-FACTOR-CACHE-RETIRE 重开条件）", rel, m)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("扫描 %s 失败: %v", dir, err)
		}
	}

	// 正向对照：DROP 必须真实存在且只有一条。
	postgres := stripGoLineComments(readFile(t, filepath.Join(root, "pkg", "storage", "postgres.go")))
	dropRe := regexp.MustCompile(`(?i)DROP\s+TABLE\s+IF\s+EXISTS\s+public\.factor_cache\b`)
	if got := len(dropRe.FindAllString(postgres, -1)); got != 1 {
		t.Errorf("pkg/storage/postgres.go 里 DROP TABLE IF EXISTS public.factor_cache 出现 %d 次，期望恰好 1 次 —— "+
			"存量库必须真正删表（Migration 066）", got)
	}
}
