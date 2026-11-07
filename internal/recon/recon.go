// Package recon 提供「表级 writer 对拍」：按表比对新旧两个库的行数、主键集合摘要、
// 列校验和与逐行差异（QT-81）。
//
// 它服务的不是功能，而是**停写判据**：`SVC-*-G3` 与六条 `SVC-CORE-*-RETIRE` 的
// 「对拍报告归档」这句，在 2026-10-26 的材料里实测是「除 ai 簇外全是待建」。
//
// 四条硬约束：
//
//   - **只读**：只生成 SELECT；`assertReadOnlySQL` 在构建语句时自检，`readonly_test.go`
//     逐条钉死。工具不许顺手改数据。
//   - **差异可判别**：`0 行`、`表不存在`、`权限不足`、`超时` 四种**不能**都折成「相等」。
//     表不存在有独立状态，权限/超时各有独立状态（AUD 线吃过「读 0 行当成一致」的亏）。
//   - **一次性命令**：不是常驻服务，跑完退出码即结论。
//   - **不覆盖动态拼表名**：与材料 §2.4 的同一限制，如实继承。
package recon

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// TableRef 是 schema 限定的表名。
type TableRef struct {
	Schema string `json:"schema"`
	Table  string `json:"table"`
}

// String 返回 `schema.table`（Schema 为空时只返 Table）。
func (t TableRef) String() string {
	if t.Schema == "" {
		return t.Table
	}
	return t.Schema + "." + t.Table
}

// Pair 是一对「同一张逻辑表的新旧两侧」。两侧可以不同名（如 `public.x` ↔ `data.x`）。
type Pair struct {
	Left  TableRef `json:"left"`
	Right TableRef `json:"right"`
}

// Snapshot 是一次表读取的结果。Exists=false 表示表在库的 catalog 里就不存在。
type Snapshot struct {
	Exists          bool
	Columns         []string
	PrimaryKey      []string
	RowDigests      map[string]string
	ColumnChecksums map[string]string
}

// Reader 是表读取端口。生产实现见 PGReader；测试用内存夹具。
type Reader interface {
	Read(ctx context.Context, table TableRef) (Snapshot, error)
}

var (
	// ErrTableMissing 表不存在（SQLSTATE 42P01 或 catalog 查无此表）。
	ErrTableMissing = errors.New("table does not exist")
	// ErrPermissionDenied 读权限不足（SQLSTATE 42501）。
	ErrPermissionDenied = errors.New("permission denied")
	// ErrNoPrimaryKey 表没有主键：无法做行级对拍（fail-loud，不退回「只比行数」）。
	ErrNoPrimaryKey = errors.New("table has no primary key")
)

// 状态取值。**`diff` 与 `table_missing` 必须分开**——把后者折成前者或 `equal` 是本工具
// 唯一重要的反证（QT-81 ⑤ ①）。
const (
	StatusEqual            = "equal"
	StatusDiff             = "diff"
	StatusTableMissing     = "table_missing"
	StatusPermissionDenied = "permission_denied"
	StatusTimeout          = "timeout"
	StatusError            = "error"
)

// TableResult 是一张表的对拍结论。
type TableResult struct {
	Left  TableRef `json:"left"`
	Right TableRef `json:"right"`
	// Status 见上方常量；只有 equal 才算过。
	Status     string `json:"status"`
	LeftRows   int64  `json:"left_rows"`
	RightRows  int64  `json:"right_rows"`
	PKDigest   string `json:"pk_digest_left,omitempty"`
	PKDigestR  string `json:"pk_digest_right,omitempty"`
	Missing    int    `json:"missing_rows"`
	Extra      int    `json:"extra_rows"`
	Mismatched int    `json:"value_mismatches"`
	// ColumnDiffs 列出「只在单侧存在」或「校验和不同」的列，已排序。
	ColumnDiffs []string `json:"column_diffs,omitempty"`
	// Notes 是结构层面的事实（列集不同、主键不同、副本身份不同），不参与行级判定。
	Notes []string `json:"notes,omitempty"`
	// Error 只在非 equal/diff 状态出现，带侧别前缀（`left:` / `right:`）。
	Error string `json:"error,omitempty"`
}

// Report 是一次对拍的全部结果。
type Report struct {
	GeneratedAt time.Time     `json:"generated_at"`
	Timeout     string        `json:"timeout"`
	Tables      []TableResult `json:"tables"`
}

// Equal 报告是否全部相等（退出码 0 的判据）。
func (r Report) Equal() bool {
	for _, t := range r.Tables {
		if t.Status != StatusEqual {
			return false
		}
	}
	return true
}

// Compare 逐对读取两侧并给出结论。读取错误不中断其余表（每张表自带状态）。
func Compare(ctx context.Context, left, right Reader, pairs []Pair, timeout time.Duration) (Report, error) {
	if len(pairs) == 0 {
		return Report{}, fmt.Errorf("recon: at least one pair is required")
	}
	if left == nil || right == nil {
		return Report{}, fmt.Errorf("recon: both readers are required")
	}
	rep := Report{GeneratedAt: time.Now().UTC(), Timeout: timeout.String(), Tables: make([]TableResult, 0, len(pairs))}
	for _, p := range pairs {
		rep.Tables = append(rep.Tables, compareOne(ctx, left, right, p, timeout))
	}
	return rep, nil
}

func compareOne(parent context.Context, left, right Reader, p Pair, timeout time.Duration) TableResult {
	res := TableResult{Left: p.Left, Right: p.Right, Status: StatusEqual}

	ls, err := readWithTimeout(parent, left, p.Left, timeout)
	if err != nil {
		return fail(res, "left", err)
	}
	rs, err := readWithTimeout(parent, right, p.Right, timeout)
	if err != nil {
		return fail(res, "right", err)
	}
	if !ls.Exists {
		res.Status = StatusTableMissing
		res.Error = fmt.Sprintf("left: %v: %s", ErrTableMissing, p.Left)
		return res
	}
	if !rs.Exists {
		res.Status = StatusTableMissing
		res.Error = fmt.Sprintf("right: %v: %s", ErrTableMissing, p.Right)
		return res
	}

	res.LeftRows = int64(len(ls.RowDigests))
	res.RightRows = int64(len(rs.RowDigests))
	res.Missing, res.Extra, res.Mismatched = diffRows(ls.RowDigests, rs.RowDigests)
	res.PKDigest = digestKeys(ls.RowDigests)
	res.PKDigestR = digestKeys(rs.RowDigests)
	res.ColumnDiffs = diffColumns(ls.ColumnChecksums, rs.ColumnChecksums)

	if !sameStrings(ls.Columns, rs.Columns) {
		res.Notes = append(res.Notes, fmt.Sprintf("列集不同：left=%d 列 / right=%d 列", len(ls.Columns), len(rs.Columns)))
	}
	if !sameStrings(ls.PrimaryKey, rs.PrimaryKey) {
		res.Notes = append(res.Notes, fmt.Sprintf("主键不同：left=%v / right=%v", ls.PrimaryKey, rs.PrimaryKey))
	}
	if res.Missing > 0 || res.Extra > 0 || res.Mismatched > 0 || len(res.ColumnDiffs) > 0 {
		res.Status = StatusDiff
	}
	return res
}

func readWithTimeout(parent context.Context, r Reader, t TableRef, timeout time.Duration) (Snapshot, error) {
	ctx := parent
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(parent, timeout)
		defer cancel()
	}
	return r.Read(ctx, t)
}

func fail(res TableResult, side string, err error) TableResult {
	res.Status = statusFor(err)
	res.Error = side + ": " + err.Error()
	return res
}

func statusFor(err error) string {
	switch {
	case errors.Is(err, ErrTableMissing):
		return StatusTableMissing
	case errors.Is(err, ErrPermissionDenied):
		return StatusPermissionDenied
	case errors.Is(err, context.DeadlineExceeded):
		return StatusTimeout
	default:
		return StatusError
	}
}

// diffRows 按主键集合做三分类：缺行（左有右无）、多行（右有左无）、同键异值。
func diffRows(left, right map[string]string) (missing, extra, mismatched int) {
	for k, lv := range left {
		rv, ok := right[k]
		if !ok {
			missing++
			continue
		}
		if lv != rv {
			mismatched++
		}
	}
	for k := range right {
		if _, ok := left[k]; !ok {
			extra++
		}
	}
	return missing, extra, mismatched
}

// digestKeys 是主键集合的摘要：排序后拼接再交给调用方展示（不用于相等判定）。
func digestKeys(rows map[string]string) string {
	keys := make([]string, 0, len(rows))
	for k := range rows {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return fmt.Sprintf("n=%d;first=%s;last=%s", len(keys), firstOr(keys), lastOr(keys))
}

func firstOr(keys []string) string {
	if len(keys) == 0 {
		return "-"
	}
	return keys[0]
}

func lastOr(keys []string) string {
	if len(keys) == 0 {
		return "-"
	}
	return keys[len(keys)-1]
}

func diffColumns(left, right map[string]string) []string {
	set := make(map[string]struct{}, len(left)+len(right))
	for k := range left {
		set[k] = struct{}{}
	}
	for k := range right {
		set[k] = struct{}{}
	}
	var out []string
	for k := range set {
		lv, lok := left[k]
		rv, rok := right[k]
		if !lok || !rok || lv != rv {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// writeKeywords 是只读自检的黑名单。命中即拒绝执行（fail-closed）。
var writeKeywords = []string{
	"INSERT", "UPDATE", "DELETE", "ALTER", "DROP", "TRUNCATE",
	"CREATE", "GRANT", "REVOKE", "COPY", "VACUUM", "REINDEX", "MERGE",
}

// assertReadOnlySQL 拒绝任何含写关键字的语句。只看词边界，避免 `update_log` 这类表名误伤。
func assertReadOnlySQL(sql string) error {
	upper := strings.ToUpper(sql)
	for _, kw := range writeKeywords {
		if containsWord(upper, kw) {
			return fmt.Errorf("recon: refusing non-read-only SQL (contains %s)", kw)
		}
	}
	if !strings.Contains(upper, "SELECT") {
		return fmt.Errorf("recon: statement is not a SELECT")
	}
	return nil
}

// containsWord 判断 s 里是否有独立的 kw（前后不是字母/数字/下划线）。
func containsWord(s, kw string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], kw)
		if j < 0 {
			return false
		}
		start := i + j
		end := start + len(kw)
		beforeOK := start == 0 || !isWordByte(s[start-1])
		afterOK := end == len(s) || !isWordByte(s[end])
		if beforeOK && afterOK {
			return true
		}
		i = end
	}
}

func isWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}
