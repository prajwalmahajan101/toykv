package respfmt

import (
	"bytes"
	"math"
	"strings"
	"testing"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

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
		{
			"nested-array",
			resp.Array(
				resp.Bulk([]byte("a")),
				resp.Array(resp.Bulk([]byte("b1")), resp.Bulk([]byte("b2"))),
			),
			"1) \"a\"\n2) \n   1) \"b1\"\n   2) \"b2\"\n",
			"",
		},
		// RESP3 kinds — rendered when the connection negotiated HELLO 3.
		{
			"map",
			resp.Map(
				resp.Bulk([]byte("f1")), resp.Bulk([]byte("v1")),
				resp.Bulk([]byte("f2")), resp.Int(2),
			),
			"1# \"f1\" => \"v1\"\n2# \"f2\" => (integer) 2\n",
			"",
		},
		{"empty-map", resp.Map(), "(empty map)\n", ""},
		{
			"map-with-null-value",
			resp.Map(resp.Bulk([]byte("k")), resp.Null()),
			"1# \"k\" => (nil)\n",
			"",
		},
		{"set", resp.Set(resp.Int(1), resp.Int(2)), "1) (integer) 1\n2) (integer) 2\n", ""},
		{
			"push",
			resp.Push(resp.Bulk([]byte("message")), resp.Bulk([]byte("ch"))),
			"1) \"message\"\n2) \"ch\"\n",
			"",
		},
		{"double", resp.Double(3.5), "(double) 3.5\n", ""},
		{"double-inf", resp.Double(math.Inf(1)), "(double) inf\n", ""},
		{"bool-true", resp.Boolean(true), "(true)\n", ""},
		{"bool-false", resp.Boolean(false), "(false)\n", ""},
		{"null", resp.Null(), "(nil)\n", ""},
		{"verbatim", resp.Verbatim("txt", []byte("line1\nline2")), "line1\nline2\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			p := &Printer{Out: &out, Err: &errb}
			p.Print(c.v)
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
	p := &Printer{Out: &out, Err: &errb, Raw: true}
	p.Print(resp.Bulk([]byte("hello\tworld")))
	if out.String() != "hello\tworld\n" {
		t.Errorf("raw bulk: got %q", out.String())
	}
	out.Reset()
	p.Print(resp.Int(7))
	if out.String() != "7\n" {
		t.Errorf("raw int: got %q", out.String())
	}
	out.Reset()
	p.Print(resp.NullBulk())
	if out.String() != "\n" {
		t.Errorf("raw nil: got %q", out.String())
	}
	errb.Reset()
	out.Reset()
	p.Print(resp.Error("ERR x"))
	if !strings.HasPrefix(errb.String(), "(error)") {
		t.Errorf("raw error still goes to stderr; got %q", errb.String())
	}
}

func TestPrinter_Raw_RESP3(t *testing.T) {
	cases := []struct {
		name string
		v    resp.Value
		out  string
	}{
		// A raw map flattens to one element per line (script-friendly).
		{
			"map",
			resp.Map(resp.Bulk([]byte("f")), resp.Bulk([]byte("v"))),
			"f\nv\n",
		},
		{"double", resp.Double(2.5), "2.5\n"},
		{"bool-true", resp.Boolean(true), "true\n"},
		{"null", resp.Null(), "\n"},
		{"verbatim", resp.Verbatim("txt", []byte("body")), "body\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			p := &Printer{Out: &out, Err: &errb, Raw: true}
			p.Print(c.v)
			if out.String() != c.out {
				t.Errorf("raw %s: got %q want %q", c.name, out.String(), c.out)
			}
		})
	}
}

func TestPrettyString(t *testing.T) {
	if got := PrettyString(resp.Int(42)); got != "(integer) 42" {
		t.Errorf("PrettyString int: got %q", got)
	}
	if got := PrettyString(resp.Bulk([]byte("x"))); got != `"x"` {
		t.Errorf("PrettyString bulk: got %q", got)
	}
	if got := PrettyString(resp.NullBulk()); got != "(nil)" {
		t.Errorf("PrettyString nil: got %q", got)
	}
}

func TestRawString(t *testing.T) {
	if got := RawString(resp.Bulk([]byte("hi"))); got != "hi" {
		t.Errorf("RawString bulk: got %q", got)
	}
}
