package resp

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestWriter_WriteFrame_Golden(t *testing.T) {
	cases := []struct {
		name string
		in   Value
		want string
	}{
		{"simple", String("OK"), "+OK\r\n"},
		{"error", Error("ERR bad"), "-ERR bad\r\n"},
		{"int-zero", Int(0), ":0\r\n"},
		{"int-pos", Int(42), ":42\r\n"},
		{"int-neg", Int(-7), ":-7\r\n"},
		{"bulk", Bulk([]byte("hello")), "$5\r\nhello\r\n"},
		{"bulk-empty", Bulk([]byte{}), "$0\r\n\r\n"},
		{"bulk-null", NullBulk(), "$-1\r\n"},
		{"array-empty", Array(), "*0\r\n"},
		{"array-null", NullArray(), "*-1\r\n"},
		{"array-mixed",
			Array(Int(1), String("two"), Bulk([]byte("three"))),
			"*3\r\n:1\r\n+two\r\n$5\r\nthree\r\n"},
		{"ok-shortcut", OK(), "+OK\r\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			w := NewWriter(&buf)
			if err := w.WriteFrame(tc.in); err != nil {
				t.Fatalf("WriteFrame: %v", err)
			}
			if err := w.Flush(); err != nil {
				t.Fatalf("Flush: %v", err)
			}
			if got := buf.String(); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWriter_UnknownKind(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	err := w.WriteFrame(Value{Kind: '?'})
	if err == nil {
		t.Fatal("want error for unknown kind, got nil")
	}
}

func TestRoundTrip(t *testing.T) {
	values := []Value{
		String("hello"),
		Error("ERR x"),
		Int(0), Int(123), Int(-456),
		Bulk([]byte("data")),
		Bulk([]byte{}),
		NullBulk(),
		Array(),
		NullArray(),
		Array(Int(1), Bulk([]byte("x")), String("y")),
		Array(Array(Int(1)), Array(NullBulk(), Bulk([]byte("z")))),
	}
	for _, want := range values {
		var buf bytes.Buffer
		w := NewWriter(&buf)
		if err := w.WriteFrame(want); err != nil {
			t.Fatalf("write %+v: %v", want, err)
		}
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
		got, err := NewReader(strings.NewReader(buf.String())).ReadFrame()
		if err != nil {
			t.Fatalf("read %+v: %v (wire=%q)", want, err, buf.String())
		}
		if !valueEqual(got, want) {
			t.Fatalf("round-trip drift: got %+v, want %+v (wire=%q)", got, want, buf.String())
		}
	}
}

// failingWriter returns errFailed once n bytes have been written.
type failingWriter struct {
	limit, written int
}

var errFailed = errors.New("forced failure")

func (f *failingWriter) Write(p []byte) (int, error) {
	if f.written >= f.limit {
		return 0, errFailed
	}
	allowed := f.limit - f.written
	if allowed > len(p) {
		allowed = len(p)
	}
	f.written += allowed
	if allowed < len(p) {
		return allowed, errFailed
	}
	return allowed, nil
}

func TestWriter_FailingWriter(t *testing.T) {
	// Buffer flush surfaces the underlying error.
	w := NewWriter(&failingWriter{limit: 0})
	_ = w.WriteFrame(Bulk([]byte("hello world this exceeds the limit immediately")))
	if err := w.Flush(); !errors.Is(err, errFailed) {
		t.Fatalf("got err %v, want errFailed via Flush", err)
	}
}

// TestWriter_LargeBulk forces an intermediate bufio flush by writing a
// payload larger than the default 4 KiB buffer. The failingWriter then
// surfaces an error mid-frame, which exercises the error-return paths
// inside writeBulk that smaller frames cannot reach.
func TestWriter_LargeBulkMidFrameFailure(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 8<<10) // 8 KiB > default bufio buffer
	w := NewWriter(&failingWriter{limit: 100})
	err := w.WriteFrame(Bulk(payload))
	if err == nil {
		err = w.Flush()
	}
	if !errors.Is(err, errFailed) {
		t.Fatalf("got err %v, want errFailed", err)
	}
}

// TestWriter_LargeArrayMidFrameFailure does the same for arrays.
func TestWriter_LargeArrayMidFrameFailure(t *testing.T) {
	// Build an array large enough to force intermediate flushes.
	elems := make([]Value, 1024)
	for i := range elems {
		elems[i] = Bulk(bytes.Repeat([]byte("y"), 16))
	}
	w := NewWriter(&failingWriter{limit: 200})
	err := w.WriteFrame(Array(elems...))
	if err == nil {
		err = w.Flush()
	}
	if !errors.Is(err, errFailed) {
		t.Fatalf("got err %v, want errFailed", err)
	}
}
