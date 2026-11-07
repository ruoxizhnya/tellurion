package main

import (
	"bytes"
	"testing"

	"tellurion/internal/recon"
)

func TestParsePair(t *testing.T) {
	cases := []struct {
		in   string
		want recon.Pair
		err  bool
	}{
		{in: "public.t=data.t", want: recon.Pair{Left: recon.TableRef{Schema: "public", Table: "t"}, Right: recon.TableRef{Schema: "data", Table: "t"}}},
		{in: " public.t = data.t ", want: recon.Pair{Left: recon.TableRef{Schema: "public", Table: "t"}, Right: recon.TableRef{Schema: "data", Table: "t"}}},
		{in: "t", want: recon.Pair{Left: recon.TableRef{Table: "t"}, Right: recon.TableRef{Table: "t"}}},
		{in: "public.t", want: recon.Pair{Left: recon.TableRef{Schema: "public", Table: "t"}, Right: recon.TableRef{Schema: "public", Table: "t"}}},
		{in: "", err: true},
		{in: "a.b.c", err: true},
		{in: "public.=data.t", err: true},
	}
	for _, c := range cases {
		got, err := parsePair(c.in)
		if c.err {
			if err == nil {
				t.Errorf("parsePair(%q) 应报错，却得到 %+v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parsePair(%q) error = %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parsePair(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestRun_UsageErrors(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := run(nil, &out, &errBuf); code != 2 {
		t.Errorf("无参数退出码 = %d, want 2", code)
	}
	if code := run([]string{"-left-dsn", "x", "-right-dsn", "y"}, &out, &errBuf); code != 2 {
		t.Errorf("无 -pair 退出码 = %d, want 2", code)
	}
	out.Reset()
	errBuf.Reset()
	if code := run([]string{"-pair", "a.b=c.b"}, &out, &errBuf); code != 2 {
		t.Errorf("无 DSN 退出码 = %d, want 2", code)
	}
}
