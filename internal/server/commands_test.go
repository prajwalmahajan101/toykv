package server

import (
	"sort"
	"strings"
	"testing"

	"github.com/prajwalmahajan101/toykv/internal/resp"
)

// withRunningServer is the test rig for the M2 command suite: spin up a
// server, dial it, run fn against (r, w), and tear down.
func withRunningServer(t *testing.T, fn func(r *resp.Reader, w *resp.Writer)) {
	t.Helper()
	s := setupServer(t)
	_, cancel, errCh := runServer(t, s)
	defer func() {
		cancel()
		<-errCh
	}()
	c, r, w := dial(t, s.Addr())
	defer c.Close()
	fn(r, w)
}

func expectSimple(t *testing.T, r *resp.Reader, want string) {
	t.Helper()
	got := readReply(t, r)
	if got.Kind != resp.KindSimpleString || got.Str != want {
		t.Fatalf("got %+v, want +%s", got, want)
	}
}

func expectBulk(t *testing.T, r *resp.Reader, want string) {
	t.Helper()
	got := readReply(t, r)
	if got.Kind != resp.KindBulkString || got.IsNull || string(got.Bytes) != want {
		t.Fatalf("got %+v, want bulk %q", got, want)
	}
}

func expectNullBulk(t *testing.T, r *resp.Reader) {
	t.Helper()
	got := readReply(t, r)
	if got.Kind != resp.KindBulkString || !got.IsNull {
		t.Fatalf("got %+v, want nil-bulk", got)
	}
}

func expectInt(t *testing.T, r *resp.Reader, want int64) {
	t.Helper()
	got := readReply(t, r)
	if got.Kind != resp.KindInteger || got.Int != want {
		t.Fatalf("got %+v, want :%d", got, want)
	}
}

func expectErrContains(t *testing.T, r *resp.Reader, substr string) {
	t.Helper()
	got := readReply(t, r)
	if got.Kind != resp.KindError || !strings.Contains(got.Str, substr) {
		t.Fatalf("got %+v, want error containing %q", got, substr)
	}
}

func TestGetSetRoundTrip(t *testing.T) {
	withRunningServer(t, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "SET", "k", "hello")
		expectSimple(t, r, "OK")
		writeCmd(t, w, "GET", "k")
		expectBulk(t, r, "hello")
		writeCmd(t, w, "GET", "missing")
		expectNullBulk(t, r)
	})
}

func TestSet_NX_XX(t *testing.T) {
	withRunningServer(t, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "SET", "k", "v1", "XX") // missing → nil
		expectNullBulk(t, r)
		writeCmd(t, w, "SET", "k", "v1", "NX") // missing → OK
		expectSimple(t, r, "OK")
		writeCmd(t, w, "SET", "k", "v2", "NX") // exists → nil
		expectNullBulk(t, r)
		writeCmd(t, w, "SET", "k", "v2", "XX") // exists → OK
		expectSimple(t, r, "OK")
		writeCmd(t, w, "GET", "k")
		expectBulk(t, r, "v2")
		writeCmd(t, w, "SET", "k", "v3", "WAT") // syntax error
		expectErrContains(t, r, "syntax")
	})
}

func TestDelExists(t *testing.T) {
	withRunningServer(t, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "SET", "a", "1")
		expectSimple(t, r, "OK")
		writeCmd(t, w, "SET", "b", "2")
		expectSimple(t, r, "OK")
		writeCmd(t, w, "EXISTS", "a", "a", "b", "missing")
		expectInt(t, r, 3)
		writeCmd(t, w, "DEL", "a", "a", "missing", "b")
		expectInt(t, r, 2)
		writeCmd(t, w, "EXISTS", "a", "b")
		expectInt(t, r, 0)
	})
}

func TestIncrDecr(t *testing.T) {
	withRunningServer(t, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "INCR", "ctr")
		expectInt(t, r, 1)
		writeCmd(t, w, "INCR", "ctr")
		expectInt(t, r, 2)
		writeCmd(t, w, "DECR", "ctr")
		expectInt(t, r, 1)
		writeCmd(t, w, "SET", "bad", "abc")
		expectSimple(t, r, "OK")
		writeCmd(t, w, "INCR", "bad")
		expectErrContains(t, r, "not an integer")
	})
}

func TestKeysAndDBSize(t *testing.T) {
	withRunningServer(t, func(r *resp.Reader, w *resp.Writer) {
		for _, k := range []string{"foo", "foobar", "baz"} {
			writeCmd(t, w, "SET", k, "v")
			expectSimple(t, r, "OK")
		}
		writeCmd(t, w, "DBSIZE")
		expectInt(t, r, 3)

		writeCmd(t, w, "KEYS", "foo*")
		got := readReply(t, r)
		if got.Kind != resp.KindArray || len(got.Array) != 2 {
			t.Fatalf("got %+v, want 2-element array", got)
		}
		keys := make([]string, len(got.Array))
		for i, v := range got.Array {
			keys[i] = string(v.Bytes)
		}
		sort.Strings(keys)
		if keys[0] != "foo" || keys[1] != "foobar" {
			t.Fatalf("got %v, want [foo foobar]", keys)
		}

		writeCmd(t, w, "KEYS", "[bad")
		expectErrContains(t, r, "bad pattern")
	})
}

func TestFlushDB(t *testing.T) {
	withRunningServer(t, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "SET", "k", "v")
		expectSimple(t, r, "OK")
		writeCmd(t, w, "FLUSHDB")
		expectSimple(t, r, "OK")
		writeCmd(t, w, "DBSIZE")
		expectInt(t, r, 0)
		writeCmd(t, w, "GET", "k")
		expectNullBulk(t, r)
	})
}

func TestIncrOverflow(t *testing.T) {
	withRunningServer(t, func(r *resp.Reader, w *resp.Writer) {
		writeCmd(t, w, "SET", "n", "9223372036854775807") // MaxInt64
		expectSimple(t, r, "OK")
		writeCmd(t, w, "INCR", "n")
		expectErrContains(t, r, "overflow")
	})
}
