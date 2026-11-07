// Command recon 是表级 writer 对拍工具（QT-81）：按表比对新旧两个库的行数与内容，
// 给 SVC-*-G3 的停写判据提供可归档的结论。
//
// 用法：
//
//	go run ./cmd/recon -left-dsn "$LEFT_DSN" -right-dsn "$RIGHT_DSN" \
//	    -pair public.stocks=data.stocks -pair public.ohlcv_daily_qfq=data.ohlcv_daily_qfq
//
// 退出码：0 = 全部相等；1 = 有差异（含表不存在/权限不足/超时）；2 = 用法或连接错误。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"tellurion/internal/recon"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("recon", flag.ContinueOnError)
	fs.SetOutput(stderr)

	leftDSN := fs.String("left-dsn", os.Getenv("RECON_LEFT_DSN"), "左侧（旧写者）PostgreSQL DSN；也可用 RECON_LEFT_DSN")
	rightDSN := fs.String("right-dsn", os.Getenv("RECON_RIGHT_DSN"), "右侧（新写者）PostgreSQL DSN；也可用 RECON_RIGHT_DSN")
	timeout := fs.Duration("timeout", 60*time.Second, "单表读取超时（超时判 timeout，不折成相等）")
	asJSON := fs.Bool("json", false, "输出 JSON（便于归档）")

	var pairs []recon.Pair
	fs.Func("pair", "表对：`schema.a=schema.b` 或 `schema.t`（两侧同名）；可重复", func(v string) error {
		p, err := parsePair(v)
		if err != nil {
			return err
		}
		pairs = append(pairs, p)
		return nil
	})
	fs.Usage = func() {
		fmt.Fprintln(stderr, "recon — 表级 writer 对拍（只读）")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *leftDSN == "" || *rightDSN == "" {
		fmt.Fprintln(stderr, "recon: 必须提供 -left-dsn 与 -right-dsn（或 RECON_LEFT_DSN / RECON_RIGHT_DSN）")
		return 2
	}
	if len(pairs) == 0 {
		fmt.Fprintln(stderr, "recon: 至少给一个 -pair")
		return 2
	}

	ctx := context.Background()
	left, err := recon.NewPGReader(ctx, *leftDSN)
	if err != nil {
		fmt.Fprintln(stderr, "recon:", err)
		return 2
	}
	defer left.Close()
	right, err := recon.NewPGReader(ctx, *rightDSN)
	if err != nil {
		fmt.Fprintln(stderr, "recon:", err)
		return 2
	}
	defer right.Close()

	rep, err := recon.Compare(ctx, left, right, pairs, *timeout)
	if err != nil {
		fmt.Fprintln(stderr, "recon:", err)
		return 2
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			fmt.Fprintln(stderr, "recon:", err)
			return 2
		}
	} else {
		writeHuman(stdout, rep)
	}
	if !rep.Equal() {
		return 1
	}
	return 0
}

func writeHuman(w io.Writer, rep recon.Report) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "LEFT\tRIGHT\tSTATUS\tROWS(L/R)\tMISSING\tEXTRA\tMISMATCH\tCOLUMN_DIFFS")
	for _, t := range rep.Tables {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d/%d\t%d\t%d\t%d\t%s\n",
			t.Left, t.Right, t.Status, t.LeftRows, t.RightRows, t.Missing, t.Extra, t.Mismatched,
			strings.Join(t.ColumnDiffs, ","))
	}
	tw.Flush()
	for _, t := range rep.Tables {
		if t.Error != "" {
			fmt.Fprintf(w, "- %s ↔ %s: %s\n", t.Left, t.Right, t.Error)
		}
		for _, n := range t.Notes {
			fmt.Fprintf(w, "- %s ↔ %s: %s\n", t.Left, t.Right, n)
		}
	}
	if rep.Equal() {
		fmt.Fprintf(w, "结论：%d/%d 表相等（时间 %s，超时 %s）\n", len(rep.Tables), len(rep.Tables), rep.GeneratedAt.Format(time.RFC3339), rep.Timeout)
		return
	}
	equal := 0
	for _, t := range rep.Tables {
		if t.Status == recon.StatusEqual {
			equal++
		}
	}
	fmt.Fprintf(w, "结论：%d/%d 表相等（时间 %s，超时 %s）\n", equal, len(rep.Tables), rep.GeneratedAt.Format(time.RFC3339), rep.Timeout)
}

// parsePair 解析 `schema.a=schema.b` 或 `schema.t`（两侧同名）。
func parsePair(s string) (recon.Pair, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return recon.Pair{}, errors.New("pair 不能为空")
	}
	if i := strings.Index(s, "="); i >= 0 {
		l, err := parseRef(s[:i])
		if err != nil {
			return recon.Pair{}, err
		}
		r, err := parseRef(s[i+1:])
		if err != nil {
			return recon.Pair{}, err
		}
		return recon.Pair{Left: l, Right: r}, nil
	}
	ref, err := parseRef(s)
	if err != nil {
		return recon.Pair{}, err
	}
	return recon.Pair{Left: ref, Right: ref}, nil
}

func parseRef(s string) (recon.TableRef, error) {
	s = strings.TrimSpace(s)
	switch parts := strings.Split(s, "."); len(parts) {
	case 1:
		if parts[0] == "" {
			return recon.TableRef{}, errors.New("表名为空")
		}
		return recon.TableRef{Table: parts[0]}, nil
	case 2:
		if parts[0] == "" || parts[1] == "" {
			return recon.TableRef{}, fmt.Errorf("表引用 %q 不完整（要 schema.table）", s)
		}
		return recon.TableRef{Schema: parts[0], Table: parts[1]}, nil
	default:
		return recon.TableRef{}, fmt.Errorf("表引用 %q 层级过多（要 schema.table）", s)
	}
}
