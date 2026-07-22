package resp

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"testing"
)

func TestReader_ReadFrame_Happy(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  Value
	}{
		{"simple", "+OK\r\n", String("OK")},
		{"simple-empty", "+\r\n", String("")},
		{"error", "-ERR bad\r\n", Error("ERR bad")},
		{"int-zero", ":0\r\n", Int(0)},
		{"int-pos", ":42\r\n", Int(42)},
		{"int-neg", ":-7\r\n", Int(-7)},
		{"bulk", "$5\r\nhello\r\n", Bulk([]byte("hello"))},
		{"bulk-empty", "$0\r\n\r\n", Bulk([]byte{})},
		{"bulk-null", "$-1\r\n", NullBulk()},
		{"bulk-binary", "$3\r\n\x00\xffA\r\n", Bulk([]byte{0x00, 0xff, 'A'})},
		{"array-empty", "*0\r\n", Array()},
		{"array-null", "*-1\r\n", NullArray()},
		{"array-mixed", "*3\r\n:1\r\n+two\r\n$5\r\nthree\r\n",
			Array(Int(1), String("two"), Bulk([]byte("three")))},
		{"array-nested", "*2\r\n*1\r\n:1\r\n*1\r\n:2\r\n",
			Array(Array(Int(1)), Array(Int(2)))},
		// RESP3 kinds — the reader now decodes these symmetrically with the
		// writer, so a HELLO 3 client can consume the server's replies.
		{"map", "%2\r\n$1\r\nk\r\n:1\r\n$1\r\nj\r\n_\r\n",
			Map(Bulk([]byte("k")), Int(1), Bulk([]byte("j")), Null())},
		{"map-empty", "%0\r\n", Map()},
		{"set", "~2\r\n:1\r\n:2\r\n", Set(Int(1), Int(2))},
		{"set-empty", "~0\r\n", Set()},
		{"push", ">2\r\n+message\r\n$2\r\nch\r\n",
			Push(String("message"), Bulk([]byte("ch")))},
		{"double-int", ",3\r\n", Double(3)},
		{"double-frac", ",3.5\r\n", Double(3.5)},
		{"double-neg", ",-0.25\r\n", Double(-0.25)},
		{"double-inf", ",inf\r\n", Double(math.Inf(1))},
		{"double-ninf", ",-inf\r\n", Double(math.Inf(-1))},
		{"double-nan", ",nan\r\n", Double(math.NaN())},
		{"bool-true", "#t\r\n", Boolean(true)},
		{"bool-false", "#f\r\n", Boolean(false)},
		{"null", "_\r\n", Null()},
		{"verbatim", "=6\r\ntxt:hi\r\n", Verbatim("txt", []byte("hi"))},
		{"verbatim-empty-body", "=4\r\ntxt:\r\n", Verbatim("txt", []byte(""))},
		{"map-nested", "%1\r\n+k\r\n~2\r\n:1\r\n:2\r\n",
			Map(String("k"), Set(Int(1), Int(2)))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewReader(strings.NewReader(tc.input))
			got, err := r.ReadFrame()
			if err != nil {
				t.Fatalf("ReadFrame: %v", err)
			}
			if !valueEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestReader_ReadFrame_Malformed(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  error
	}{
		{"unknown-prefix", "?garbage\r\n", ErrProtocol},
		{"missing-crlf-simple", "+OK\n", ErrProtocol},
		{"missing-cr-simple", "+OK\rX\n", ErrProtocol},
		{"bad-int", ":not-a-number\r\n", ErrProtocol},
		{"bulk-negative-length", "$-2\r\n", ErrProtocol},
		{"bulk-missing-tail", "$5\r\nhelloXX", ErrProtocol},
		{"array-negative-length", "*-2\r\n", ErrProtocol},
		// RESP3 grammar violations.
		{"bad-double", ",not-a-double\r\n", ErrProtocol},
		{"bad-boolean", "#x\r\n", ErrProtocol},
		{"boolean-word", "#true\r\n", ErrProtocol},
		{"null-with-body", "_x\r\n", ErrProtocol},
		{"map-negative-length", "%-1\r\n", ErrProtocol},
		{"set-negative-length", "~-1\r\n", ErrProtocol},
		{"verbatim-too-short", "=3\r\ntxt\r\n", ErrProtocol},
		{"verbatim-missing-colon", "=6\r\ntxtXhi\r\n", ErrProtocol},
		{"verbatim-missing-tail", "=6\r\ntxt:hiXX", ErrProtocol},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewReader(strings.NewReader(tc.input))
			_, err := r.ReadFrame()
			if !errors.Is(err, tc.want) {
				t.Fatalf("got err %v, want errors.Is(_, %v)", err, tc.want)
			}
		})
	}
}

func TestReader_ReadFrame_OversizedBulk(t *testing.T) {
	// Length declares 65 MiB; we never actually allocate the body.
	input := "$67108865\r\n"
	r := NewReader(strings.NewReader(input))
	_, err := r.ReadFrame()
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("got err %v, want errors.Is(_, ErrTooLarge)", err)
	}
}

func TestReader_ReadFrame_OversizedArray(t *testing.T) {
	// Declares more elements than MaxArrayLen; must be rejected before the
	// make([]Value, n) allocation (pre-auth memory-amplification DoS guard).
	input := fmt.Sprintf("*%d\r\n", MaxArrayLen+1)
	r := NewReader(strings.NewReader(input))
	_, err := r.ReadFrame()
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("got err %v, want errors.Is(_, ErrTooLarge)", err)
	}
}

func TestReader_ReadFrame_OversizedRESP3Aggregates(t *testing.T) {
	// The RESP3 aggregate kinds share the same pre-allocation bound as
	// arrays: a header declaring more elements than MaxArrayLen must be
	// rejected before make([]Value, n). Maps count pairs, so a header just
	// over MaxArrayLen/2 already exceeds the element budget.
	cases := []struct {
		name  string
		input string
	}{
		{"set", fmt.Sprintf("~%d\r\n", MaxArrayLen+1)},
		{"push", fmt.Sprintf(">%d\r\n", MaxArrayLen+1)},
		{"map", fmt.Sprintf("%%%d\r\n", MaxArrayLen/2+1)},
		{"verbatim", "=67108865\r\n"}, // 65 MiB + 1, never allocated
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewReader(strings.NewReader(tc.input))
			_, err := r.ReadFrame()
			if !errors.Is(err, ErrTooLarge) {
				t.Fatalf("got err %v, want errors.Is(_, ErrTooLarge)", err)
			}
		})
	}
}

func TestReader_ReadFrame_OverDeepNesting(t *testing.T) {
	// A stream of nested single-element arrays must be rejected once the
	// nesting passes MaxDepth, instead of recursing until the goroutine
	// stack is exhausted (pre-auth stack-exhaustion DoS guard).
	var b strings.Builder
	for i := 0; i < MaxDepth+2; i++ {
		b.WriteString("*1\r\n")
	}
	r := NewReader(strings.NewReader(b.String()))
	_, err := r.ReadFrame()
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("got err %v, want errors.Is(_, ErrTooLarge)", err)
	}
}

func TestReader_ReadFrame_NestingWithinDepthOK(t *testing.T) {
	// Nesting exactly at the limit still decodes — the bound must not
	// reject legitimate frames. Build MaxDepth arrays wrapping one integer.
	var b strings.Builder
	for i := 0; i < MaxDepth-1; i++ {
		b.WriteString("*1\r\n")
	}
	b.WriteString(":7\r\n")
	r := NewReader(strings.NewReader(b.String()))
	if _, err := r.ReadFrame(); err != nil {
		t.Fatalf("nesting within depth should decode, got err %v", err)
	}
}

func TestReader_ReadFrame_EOF(t *testing.T) {
	r := NewReader(strings.NewReader(""))
	_, err := r.ReadFrame()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("got err %v, want io.EOF", err)
	}
}

func TestReader_ReadCommand_Happy(t *testing.T) {
	input := "*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$1\r\nv\r\n"
	r := NewReader(strings.NewReader(input))
	argv, err := r.ReadCommand()
	if err != nil {
		t.Fatalf("ReadCommand: %v", err)
	}
	want := [][]byte{[]byte("SET"), []byte("k"), []byte("v")}
	if len(argv) != len(want) {
		t.Fatalf("got %d args, want %d", len(argv), len(want))
	}
	for i := range argv {
		if !bytes.Equal(argv[i], want[i]) {
			t.Fatalf("argv[%d]: got %q, want %q", i, argv[i], want[i])
		}
	}
}

func TestReader_ReadCommand_Errors(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  error
	}{
		{"not-array", "+PING\r\n", ErrProtocol},
		{"nil-array", "*-1\r\n", ErrProtocol},
		{"empty-array", "*0\r\n", ErrInvalidArity},
		{"non-bulk-element", "*1\r\n:1\r\n", ErrProtocol},
		{"nil-bulk-element", "*1\r\n$-1\r\n", ErrProtocol},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewReader(strings.NewReader(tc.input))
			_, err := r.ReadCommand()
			if !errors.Is(err, tc.want) {
				t.Fatalf("got err %v, want errors.Is(_, %v)", err, tc.want)
			}
		})
	}
}

// failingReader returns io.ErrUnexpectedEOF after n bytes.
type failingReader struct{ remaining int }

func (f *failingReader) Read(p []byte) (int, error) {
	if f.remaining <= 0 {
		return 0, io.ErrUnexpectedEOF
	}
	n := len(p)
	if n > f.remaining {
		n = f.remaining
	}
	for i := 0; i < n; i++ {
		p[i] = '+'
	}
	f.remaining -= n
	return n, nil
}

// TestReader_ReadFrame_UnexpectedEOF covers the error path inside
// readLine when the underlying reader returns a non-buffer-full error.
func TestReader_ReadFrame_UnexpectedEOF(t *testing.T) {
	r := NewReader(&failingReader{remaining: 2}) // "++" no newline, then EOF
	_, err := r.ReadFrame()
	if err == nil {
		t.Fatal("want error, got nil")
	}
}

// TestReader_BulkShortTail covers the short-read on the CRLF tail
// after a complete body.
func TestReader_BulkShortTail(t *testing.T) {
	// length 1, payload byte, then EOF (no CRLF tail)
	r := NewReader(strings.NewReader("$1\r\nx"))
	_, err := r.ReadFrame()
	if err == nil {
		t.Fatal("want error, got nil")
	}
}

// valueEqual compares two Values structurally, accommodating the
// distinction between nil and empty slices.
func valueEqual(a, b Value) bool {
	if a.Kind != b.Kind || a.IsNull != b.IsNull || a.Str != b.Str || a.Int != b.Int {
		return false
	}
	switch a.Kind {
	case KindBulkString:
		if a.IsNull {
			return true
		}
		return bytes.Equal(a.Bytes, b.Bytes)
	case KindArray, KindSet, KindPush, KindMap:
		if a.IsNull {
			return true
		}
		return elemsEqual(a.Array, b.Array)
	case KindDouble:
		// NaN never compares equal to itself, so treat NaN==NaN as equal
		// for round-trip purposes (the wire text "nan" is stable).
		if math.IsNaN(a.Float) || math.IsNaN(b.Float) {
			return math.IsNaN(a.Float) && math.IsNaN(b.Float)
		}
		return a.Float == b.Float
	case KindBoolean:
		return a.Bool == b.Bool
	case KindVerbatim:
		return a.VerbatimFmt == b.VerbatimFmt && bytes.Equal(a.Bytes, b.Bytes)
	}
	return true
}

// elemsEqual compares two element slices pairwise with valueEqual.
func elemsEqual(a, b []Value) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !valueEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}
