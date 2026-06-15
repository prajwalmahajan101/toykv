package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

func TestTokenise(t *testing.T) {
	cases := []struct {
		in   string
		want []string
		err  bool
	}{
		{"", nil, false},
		{"   ", nil, false},
		{"PING", []string{"PING"}, false},
		{"SET k v", []string{"SET", "k", "v"}, false},
		{"  SET  k   v  ", []string{"SET", "k", "v"}, false},
		{`SET k "hello world"`, []string{"SET", "k", "hello world"}, false},
		{`SET k 'hello world'`, []string{"SET", "k", "hello world"}, false},
		{`SET k "a\tb\nc"`, []string{"SET", "k", "a\tb\nc"}, false},
		{`SET k "say \"hi\""`, []string{"SET", "k", `say "hi"`}, false},
		{`SET k 'no \t escapes'`, []string{"SET", "k", `no \t escapes`}, false},
		{`SET k "unterminated`, nil, true},
		{`SET k 'unterminated`, nil, true},
		{`SET "" ""`, []string{"SET", "", ""}, false},
	}
	for _, c := range cases {
		got, err := tokenise(c.in)
		if c.err {
			if err == nil {
				t.Errorf("tokenise(%q): want error, got %v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("tokenise(%q): unexpected error: %v", c.in, err)
			continue
		}
		if !equalSlices(got, c.want) {
			t.Errorf("tokenise(%q):\n  got  %#v\n  want %#v", c.in, got, c.want)
		}
	}
}

func equalSlices(a, b []string) bool {
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

func TestPrinter_Pretty(t *testing.T) {
	cases := []struct {
		name string
		v    resp.Value
		out  string
		err  string
	}{
		{"ok", resp.OK(), "OK\n", ""},
		{"integer", resp.Int(42), "(integer) 42\n", ""},
		{"bulk", resp.Bulk([]byte("hello")), "\"hello\"\n", ""},
		{"bulk-bytes", resp.Bulk([]byte("a\tb")), "\"a\\tb\"\n", ""},
		{"nil-bulk", resp.NullBulk(), "(nil)\n", ""},
		{"err", resp.Error("ERR boom"), "", "(error) ERR boom\n"},
		{"empty-array", resp.Array(), "(empty array)\n", ""},
		{
			"array-of-bulks",
			resp.Array(resp.Bulk([]byte("a")), resp.Bulk([]byte("b"))),
			"1) \"a\"\n2) \"b\"\n",
			"",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			p := &printer{out: &out, err: &errb}
			p.print(c.v)
			if out.String() != c.out {
				t.Errorf("stdout: got %q want %q", out.String(), c.out)
			}
			if errb.String() != c.err {
				t.Errorf("stderr: got %q want %q", errb.String(), c.err)
			}
		})
	}
}

func TestPrinter_Raw(t *testing.T) {
	var out, errb bytes.Buffer
	p := &printer{out: &out, err: &errb, raw: true}
	p.print(resp.Bulk([]byte("hello\tworld")))
	if out.String() != "hello\tworld\n" {
		t.Errorf("raw bulk: got %q", out.String())
	}
	out.Reset()
	p.print(resp.Int(7))
	if out.String() != "7\n" {
		t.Errorf("raw int: got %q", out.String())
	}
	out.Reset()
	p.print(resp.NullBulk())
	if out.String() != "\n" {
		t.Errorf("raw nil: got %q", out.String())
	}
	errb.Reset()
	out.Reset()
	p.print(resp.Error("ERR x"))
	if !strings.HasPrefix(errb.String(), "(error)") {
		t.Errorf("raw error still goes to stderr; got %q", errb.String())
	}
}
