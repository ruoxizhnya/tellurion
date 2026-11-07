package recon

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

type fakeReader struct {
	snap  Snapshot
	err   error
	block time.Duration
}

func (f fakeReader) Read(ctx context.Context, _ TableRef) (Snapshot, error) {
	if f.err != nil {
		return Snapshot{}, f.err
	}
	if f.block > 0 {
		select {
		case <-time.After(f.block):
		case <-ctx.Done():
			return Snapshot{}, ctx.Err()
		}
	}
	return f.snap, nil
}

func snapOf(rows map[string]string, checksums map[string]string) Snapshot {
	return Snapshot{
		Exists:          true,
		Columns:         []string{"id", "v"},
		PrimaryKey:      []string{"id"},
		RowDigests:      rows,
		ColumnChecksums: checksums,
	}
}

var (
	pair       = Pair{Left: TableRef{"public", "t"}, Right: TableRef{"data", "t"}}
	baseRows   = map[string]string{"1": "aa", "2": "bb", "3": "cc", "4": "dd", "5": "ee"}
	baseSumMap = map[string]string{"id": "s-id", "v": "s-v"}
)

func mustCompare(t *testing.T, left, right Reader, timeout time.Duration) TableResult {
	t.Helper()
	rep, err := Compare(context.Background(), left, right, []Pair{pair}, timeout)
	if err != nil {
		t.Fatalf("Compare error = %v", err)
	}
	if len(rep.Tables) != 1 {
		t.Fatalf("tables = %d", len(rep.Tables))
	}
	return rep.Tables[0]
}

func TestCompare_Equal(t *testing.T) {
	res := mustCompare(t, fakeReader{snap: snapOf(baseRows, baseSumMap)}, fakeReader{snap: snapOf(baseRows, baseSumMap)}, time.Second)
	if res.Status != StatusEqual {
		t.Fatalf("status = %s, error = %s", res.Status, res.Error)
	}
	if res.Missing != 0 || res.Extra != 0 || res.Mismatched != 0 || len(res.ColumnDiffs) != 0 {
		t.Fatalf("相等却有差异：%+v", res)
	}
	if res.LeftRows != 5 || res.RightRows != 5 {
		t.Fatalf("rows = %d / %d", res.LeftRows, res.RightRows)
	}
}

func TestCompare_MissingRows(t *testing.T) {
	right := map[string]string{"1": "aa", "2": "bb"}
	res := mustCompare(t, fakeReader{snap: snapOf(baseRows, baseSumMap)}, fakeReader{snap: snapOf(right, baseSumMap)}, time.Second)
	if res.Status != StatusDiff || res.Missing != 3 {
		t.Fatalf("status = %s, missing = %d", res.Status, res.Missing)
	}
}

func TestCompare_ExtraRows(t *testing.T) {
	right := map[string]string{"1": "aa", "2": "bb", "3": "cc", "4": "dd", "5": "ee", "6": "ff"}
	res := mustCompare(t, fakeReader{snap: snapOf(baseRows, baseSumMap)}, fakeReader{snap: snapOf(right, baseSumMap)}, time.Second)
	if res.Status != StatusDiff || res.Extra != 1 {
		t.Fatalf("status = %s, extra = %d", res.Status, res.Extra)
	}
}

func TestCompare_ValueMismatch(t *testing.T) {
	right := map[string]string{"1": "aa", "2": "bb", "3": "XX", "4": "dd", "5": "ee"}
	res := mustCompare(t, fakeReader{snap: snapOf(baseRows, baseSumMap)}, fakeReader{snap: snapOf(right, baseSumMap)}, time.Second)
	if res.Status != StatusDiff || res.Mismatched != 1 || res.Missing != 0 || res.Extra != 0 {
		t.Fatalf("status = %s, res = %+v", res.Status, res)
	}
}

func TestCompare_ColumnChecksumDiff(t *testing.T) {
	res := mustCompare(t,
		fakeReader{snap: snapOf(baseRows, map[string]string{"id": "s-id", "v": "s-v"})},
		fakeReader{snap: snapOf(baseRows, map[string]string{"id": "s-id", "v": "other"})},
		time.Second)
	if res.Status != StatusDiff || len(res.ColumnDiffs) != 1 || res.ColumnDiffs[0] != "v" {
		t.Fatalf("status = %s, column_diffs = %v", res.Status, res.ColumnDiffs)
	}
}

// 本工具唯一重要的反证：表不存在**不能**折成「0 行相等」。
func TestCompare_TableMissingIsNotZeroRowsEqual(t *testing.T) {
	res := mustCompare(t, fakeReader{snap: snapOf(baseRows, baseSumMap)}, fakeReader{snap: Snapshot{Exists: false}}, time.Second)
	if res.Status != StatusTableMissing {
		t.Fatalf("status = %s（必须 table_missing，不能是 equal/diff）", res.Status)
	}
	if res.Status == StatusEqual || res.Status == StatusDiff {
		t.Fatalf("表不存在被折成了 %s", res.Status)
	}
	if res.Error == "" || res.RightRows != 0 {
		t.Fatalf("error/right_rows 未记录：%+v", res)
	}
}

func TestCompare_TableMissingAsError(t *testing.T) {
	res := mustCompare(t, fakeReader{snap: snapOf(baseRows, baseSumMap)},
		fakeReader{err: fmt.Errorf("%w: data.t", ErrTableMissing)}, time.Second)
	if res.Status != StatusTableMissing {
		t.Fatalf("status = %s", res.Status)
	}
}

func TestCompare_PermissionDenied(t *testing.T) {
	res := mustCompare(t, fakeReader{snap: snapOf(baseRows, baseSumMap)},
		fakeReader{err: fmt.Errorf("%w: data.t", ErrPermissionDenied)}, time.Second)
	if res.Status != StatusPermissionDenied {
		t.Fatalf("status = %s", res.Status)
	}
}

func TestCompare_Timeout(t *testing.T) {
	res := mustCompare(t, fakeReader{snap: snapOf(baseRows, baseSumMap)}, fakeReader{block: 200 * time.Millisecond}, 10*time.Millisecond)
	if res.Status != StatusTimeout {
		t.Fatalf("status = %s, error = %s", res.Status, res.Error)
	}
}

func TestCompare_RequiresPairs(t *testing.T) {
	if _, err := Compare(context.Background(), fakeReader{}, fakeReader{}, nil, time.Second); err == nil {
		t.Fatal("空 pairs 必须报错")
	}
}

func TestReport_Equal(t *testing.T) {
	rep := Report{Tables: []TableResult{{Status: StatusEqual}, {Status: StatusEqual}}}
	if !rep.Equal() {
		t.Fatal("全 equal 应为 true")
	}
	rep.Tables[1].Status = StatusTableMissing
	if rep.Equal() {
		t.Fatal("有 table_missing 应为 false")
	}
}

// 只读自证：所有自动生成的语句都不能含写关键字，且表名/列名里的 `update` / `delete`
// 不能误伤（词边界判定）。
func TestReadOnly_GeneratedStatements(t *testing.T) {
	ref := TableRef{Schema: "public", Table: "update_log"}
	cols := []string{"id", "deleted_at", "drop_reason"}
	pk := []string{"id", "deleted_at"}
	for name, q := range map[string]string{
		"row_digest":      rowDigestQuery(ref, pk),
		"column_checksum": columnChecksumQuery(ref, cols, pk),
	} {
		if err := assertReadOnlySQL(q); err != nil {
			t.Errorf("%s 被拒：%v\nSQL=%s", name, err, q)
		}
	}
}

func TestAssertReadOnlySQL_RejectsWrites(t *testing.T) {
	bad := []string{
		"INSERT INTO t VALUES (1)",
		"UPDATE t SET a = 1",
		"DELETE FROM t",
		"ALTER TABLE t ADD COLUMN c int",
		"DROP TABLE t",
		"TRUNCATE t",
		"CREATE TABLE t (id int)",
		"GRANT SELECT ON t TO r",
		"COPY t FROM stdin",
		"VACUUM t",
	}
	for _, q := range bad {
		if err := assertReadOnlySQL(q); err == nil {
			t.Errorf("未拒绝：%s", q)
		}
	}
	if err := assertReadOnlySQL("SELECT 1"); err != nil {
		t.Errorf("误拒 SELECT：%v", err)
	}
	if err := assertReadOnlySQL("SHOW search_path"); err == nil {
		t.Error("非 SELECT 语句必须拒绝")
	}
}

func TestQuoteIdent(t *testing.T) {
	cases := map[string]string{
		"t":      `"t"`,
		`we"ird`: `"we""ird"`,
		"public": `"public"`,
	}
	for in, want := range cases {
		if got := quoteIdent(in); got != want {
			t.Errorf("quoteIdent(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestStatusFor(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("x: %w", ErrTableMissing), StatusTableMissing},
		{fmt.Errorf("x: %w", ErrPermissionDenied), StatusPermissionDenied},
		{context.DeadlineExceeded, StatusTimeout},
		{errors.New("boom"), StatusError},
	}
	for _, c := range cases {
		if got := statusFor(c.err); got != c.want {
			t.Errorf("statusFor(%v) = %s, want %s", c.err, got, c.want)
		}
	}
}
